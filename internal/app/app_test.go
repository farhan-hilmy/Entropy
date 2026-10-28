package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"entropy/internal/command"
	"entropy/internal/faults"
	"entropy/internal/store"
	"entropy/internal/toxiproxy"
)

func TestRestoreProxiesCreatesOnlyMissingOnes(t *testing.T) {
	var mu sync.Mutex
	var created []toxiproxy.Proxy
	var other []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == "/proxies" {
			var p toxiproxy.Proxy
			json.NewDecoder(r.Body).Decode(&p)
			created = append(created, p)
			w.WriteHeader(http.StatusCreated)
			return
		}
		other = append(other, r.Method+" "+r.URL.Path)
	}))
	defer fake.Close()

	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	deps := []store.Dependency{
		{Name: "redis", Listen: "0.0.0.0:6379", Upstream: "redis:6379"},
		{Name: "mongo", Listen: "0.0.0.0:27017", Upstream: "mongo:27017"},
		{Name: "gone", Listen: "0.0.0.0:5432", Upstream: "pg:5432", Missing: true},
	}
	// mongo exists with a fault on it, and a different upstream than the config.
	existing := map[string]toxiproxy.Proxy{
		"mongo": {Name: "mongo", Listen: "[::]:27017", Upstream: "old:27017", Enabled: false},
	}

	got := a.restoreProxies(context.Background(), toxiproxy.New(fake.URL), deps, existing)

	if len(created) != 1 || created[0].Name != "redis" || created[0].Upstream != "redis:6379" || !created[0].Enabled {
		t.Errorf("created %+v; want only an enabled redis proxy", created)
	}
	if len(other) != 0 {
		t.Errorf("unexpected requests %v; existing proxies must not be changed", other)
	}
	if _, ok := got["redis"]; !ok {
		t.Error("the recreated proxy should be in the returned map, so this round checks it")
	}
	if got["mongo"].Enabled || got["mongo"].Upstream != "old:27017" {
		t.Errorf("mongo = %+v; want it left as it was", got["mongo"])
	}
}

// fakeResetToxiproxy records changes as "METHOD path"; /reset fails while failReset.
// GET of a proxy answers with a clean, enabled one, as after a reset.
type fakeResetToxiproxy struct {
	mu        sync.Mutex
	requests  []string
	failReset bool
}

func (f *fakeResetToxiproxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(toxiproxy.Proxy{Name: strings.TrimPrefix(r.URL.Path, "/proxies/"), Enabled: true})
		return
	}
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/reset" && f.failReset {
		http.Error(w, "boom", http.StatusInternalServerError)
	}
}

func (f *fakeResetToxiproxy) count(req string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Count(strings.Join(f.requests, "\n"), req)
}

func newFaultApp(t *testing.T) (*App, *fakeResetToxiproxy) {
	fake := &fakeResetToxiproxy{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &App{log: log, tp: toxiproxy.New(srv.URL), kick: make(chan struct{}, 1)}
	a.faults = faults.New(a.client, log)
	t.Cleanup(func() { a.faults.StopAll() })
	return a, fake
}

func TestLeftoversAreClearedOnceToxiproxyAnswers(t *testing.T) {
	a, fake := newFaultApp(t)
	ctx := context.Background()
	fake.failReset = true // unreachable (or failing) at startup
	if err := a.ClearLeftovers(ctx); err == nil {
		t.Fatal("ClearLeftovers should report the failed reset")
	}
	if a.clearPendingLeftovers(ctx, a.client()) {
		t.Error("a round whose reset fails must not count as cleared")
	}
	fake.mu.Lock()
	fake.failReset = false
	fake.mu.Unlock()
	if !a.clearPendingLeftovers(ctx, a.client()) {
		t.Error("the first round that reaches Toxiproxy should clear the leftovers")
	}
	if a.clearPendingLeftovers(ctx, a.client()) {
		t.Error("leftovers should be cleared only once")
	}
	if n := fake.count("POST /reset"); n != 3 {
		t.Errorf("%d resets, want 3 (startup, failed round, successful round)", n)
	}
}

func TestFaultStartedBeforeTheDeferredClearSurvivesIt(t *testing.T) {
	a, fake := newFaultApp(t)
	ctx := context.Background()
	a.leftoversPending = true
	if _, err := a.faults.Start(ctx, "redis", faults.Spec{Type: "latency", DurationSec: 1000}); err != nil {
		t.Fatal(err)
	}
	// One check round: the clear wipes every toxic, then Reapply puts the fault back.
	if !a.clearPendingLeftovers(ctx, a.client()) {
		t.Fatal("expected the deferred clear to run")
	}
	a.faults.Reapply(ctx, map[string]toxiproxy.Proxy{"redis": {Name: "redis", Enabled: true}})
	if n := fake.count("POST /proxies/redis/toxics"); n != 2 {
		t.Errorf("latency toxic added %d times, want 2 (start, then re-applied after the clear)", n)
	}
}

func TestHealthStatus(t *testing.T) {
	cmd := store.Command{Name: "access-token", TimeoutSec: 30}
	cases := []struct {
		res       command.Result
		state     State
		detailHas string
	}{
		{command.Result{ExitCode: 0}, Reachable, "access-token passed"},
		{command.Result{ExitCode: 1, Output: "running...\n  AssertionError: expected 500 to equal 200 \n"}, Unreachable,
			"exited with code 1: AssertionError: expected 500 to equal 200"},
		{command.Result{ExitCode: -1, TimedOut: true}, Unreachable, "timed out after 30s"},
		{command.Result{ExitCode: -1, Err: errors.New("bad dir")}, Unreachable, "could not run access-token: bad dir"},
	}
	for _, c := range cases {
		st := healthStatus(cmd, c.res)
		if st.State != c.state || !strings.Contains(st.Detail, c.detailHas) {
			t.Errorf("healthStatus(%+v) = %+v; want %s containing %q", c.res, st, c.state, c.detailHas)
		}
	}
}

func TestLastLineSkipsBrunoSummary(t *testing.T) {
	// Real `bru run` output (Windows line endings) for a service that isn't running.
	bruno := "Oauth Service\\Access Token B2B (connect ECONNREFUSED 127.0.0.1:8080)\r\n\r\n" +
		"📊 Execution Summary\r\n" +
		"┌───────────────┬──────────────┐\r\n" +
		"│ Metric        │    Result    │\r\n" +
		"├───────────────┼──────────────┤\r\n" +
		"│ Status        │    ✗ FAIL    │\r\n" +
		"└───────────────┴──────────────┘\r\n"
	cases := map[string]string{
		bruno: `Oauth Service\Access Token B2B (connect ECONNREFUSED 127.0.0.1:8080)`,
		"running...\n  AssertionError: expected 500 to equal 200 \n": "AssertionError: expected 500 to equal 200",
		"┌──┐\n└──┘\n":                                               "└──┘", // nothing else: fall back to the last line
	}
	for output, want := range cases {
		if got := lastLine(output); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", output, got, want)
		}
	}
}

func TestSameListen(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"[::]:6379", "0.0.0.0:6379", true},
		{":6379", "0.0.0.0:6379", true},
		{"[::]:6379", "0.0.0.0:6380", false},
		{"127.0.0.1:6379", "0.0.0.0:6379", false},
	}
	for _, c := range cases {
		if got := sameListen(c.a, c.b); got != c.want {
			t.Errorf("sameListen(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
