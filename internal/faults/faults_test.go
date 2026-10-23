package faults

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"entropy/internal/toxiproxy"
)

// fakeToxiproxy records every change as "METHOD path body". GET of a proxy
// answers from proxies, or with a clean, enabled proxy. The first
// failRemovals removals answer 500, and removals answer 404 if notFound.
type fakeToxiproxy struct {
	mu           sync.Mutex
	requests     []string
	proxies      map[string]toxiproxy.Proxy
	failRemovals int
	notFound     bool
}

func (f *fakeToxiproxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		name := strings.TrimPrefix(r.URL.Path, "/proxies/")
		p, ok := f.proxies[name]
		if !ok {
			p = toxiproxy.Proxy{Name: name, Enabled: true}
		}
		json.NewEncoder(w).Encode(p)
		return
	}
	f.requests = append(f.requests, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
	removal := r.Method == http.MethodDelete || strings.Contains(string(body), `"enabled":true`)
	switch {
	case removal && f.failRemovals > 0:
		f.failRemovals--
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	case removal && f.notFound:
		http.Error(w, `{"error":"proxy not found"}`, http.StatusNotFound)
	}
}

func (f *fakeToxiproxy) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func newEngine(t *testing.T) (*Engine, *fakeToxiproxy) {
	fake := &fakeToxiproxy{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client := toxiproxy.New(srv.URL)
	e := New(func() *toxiproxy.Client { return client }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.second = 20 * time.Millisecond
	e.retry = 5 * time.Millisecond
	e.maxRetry = 20 * time.Millisecond
	return e, fake
}

// waitRemoved waits until the dependency has no fault, failing after a second.
func waitRemoved(t *testing.T, e *Engine, dep string) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, ok := e.Active()[dep]; !ok {
			return
		}
	}
	t.Fatalf("the fault on %s was not removed", dep)
}

func TestLatencyIsAddedAndRemovedWhenItsTimeIsUp(t *testing.T) {
	e, fake := newEngine(t)
	f, err := e.Start(context.Background(), "redis", Spec{Type: "latency", DurationSec: 2, JitterMs: 50})
	if err != nil {
		t.Fatal(err)
	}
	if f.LatencyMs != 1000 || !f.EndsAt.After(f.StartedAt) {
		t.Errorf("fault = %+v; want the default latency and an end time", f)
	}
	if _, ok := e.Active()["redis"]; !ok {
		t.Error("the fault should be active right after Start")
	}
	waitRemoved(t, e, "redis")

	want := []string{
		`POST /proxies/redis/toxics {"name":"entropy_latency","type":"latency","stream":"downstream","toxicity":1,"attributes":{"jitter":50,"latency":1000}}`,
		`DELETE /proxies/redis/toxics/entropy_latency`,
	}
	if got := fake.log(); !slices.Equal(got, want) {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestOneFaultPerDependency(t *testing.T) {
	e, _ := newEngine(t)
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "outage", DurationSec: 50}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "drop", DurationSec: 50}); !errors.Is(err, ErrBusy) {
		t.Errorf("second fault on redis: err = %v, want ErrBusy", err)
	}
	if _, err := e.Start(context.Background(), "mongodb", Spec{Type: "drop", DurationSec: 50}); err != nil {
		t.Errorf("a fault on another dependency should start: %v", err)
	}
	if err := e.StopAll(); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Active()); n != 0 {
		t.Errorf("%d faults active after StopAll", n)
	}
}

func TestStopEndsAnOutageEarly(t *testing.T) {
	e, fake := newEngine(t)
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "outage", DurationSec: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop("redis"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Active()["redis"]; ok {
		t.Error("the fault should be gone once Stop returns")
	}
	want := []string{`POST /proxies/redis {"enabled":false}`, `POST /proxies/redis {"enabled":true}`}
	if got := fake.log(); !slices.Equal(got, want) {
		t.Errorf("requests = %q, want %q", got, want)
	}
	if err := e.Stop("redis"); !errors.Is(err, ErrNoFault) {
		t.Errorf("stopping again: err = %v, want ErrNoFault", err)
	}
}

func TestFailedRemovalIsRetried(t *testing.T) {
	e, fake := newEngine(t)
	fake.failRemovals = 3
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "timeout", DurationSec: 1}); err != nil {
		t.Fatal(err)
	}
	// While removal fails, the fault stays visible with the error.
	var sawError bool
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && !sawError; time.Sleep(2 * time.Millisecond) {
		f, ok := e.Active()["redis"]
		sawError = ok && f.Removing && f.Error != ""
	}
	if !sawError {
		t.Error("a failing removal should show as Removing with its error")
	}
	waitRemoved(t, e, "redis")
	if n := strings.Count(strings.Join(fake.log(), "\n"), "DELETE"); n != 4 {
		t.Errorf("%d removal attempts, want 4 (3 failures, then success)", n)
	}
}

func TestRemovalTreatsNotFoundAsRemoved(t *testing.T) {
	e, fake := newEngine(t)
	fake.notFound = true // e.g. Toxiproxy restarted during the fault
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "outage", DurationSec: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop("redis"); err != nil {
		t.Errorf("Stop = %v; a proxy that's gone has no fault left", err)
	}
}

func TestFlapTogglesUntilItEndsEnabled(t *testing.T) {
	e, fake := newEngine(t)
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "flap", DurationSec: 10, DownSec: 2, UpSec: 2}); err != nil {
		t.Fatal(err)
	}
	waitRemoved(t, e, "redis")
	got := fake.log()
	if len(got) < 3 || got[0] != `POST /proxies/redis {"enabled":false}` || got[1] != `POST /proxies/redis {"enabled":true}` {
		t.Fatalf("requests = %q; want it to start down, then come up", got)
	}
	if got[len(got)-1] != `POST /proxies/redis {"enabled":true}` {
		t.Errorf("last request = %q; a flap must end with the proxy enabled", got[len(got)-1])
	}
}

func TestReapplyPutsBackFaultsToxiproxyLost(t *testing.T) {
	e, fake := newEngine(t)
	ctx := context.Background()
	if _, err := e.Start(ctx, "redis", Spec{Type: "latency", DurationSec: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, "mongodb", Spec{Type: "outage", DurationSec: 1000}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.StopAll() })
	started := len(fake.log())

	// Faults in place, or proxy unknown: nothing to do.
	e.Reapply(ctx, map[string]toxiproxy.Proxy{
		"redis":   {Name: "redis", Enabled: true, Toxics: []toxiproxy.Toxic{{Name: "entropy_latency"}}},
		"mongodb": {Name: "mongodb", Enabled: false},
	})
	e.Reapply(ctx, map[string]toxiproxy.Proxy{})
	if got := fake.log()[started:]; len(got) != 0 {
		t.Errorf("requests %q; faults in place must be left alone", got)
	}

	// Toxiproxy restarted: proxies recreated clean.
	e.Reapply(ctx, map[string]toxiproxy.Proxy{"redis": {Name: "redis", Enabled: true}, "mongodb": {Name: "mongodb", Enabled: true}})
	got := fake.log()[started:]
	slices.Sort(got)
	want := []string{
		`POST /proxies/mongodb {"enabled":false}`,
		`POST /proxies/redis/toxics {"name":"entropy_latency","type":"latency","stream":"downstream","toxicity":1,"attributes":{"jitter":0,"latency":1000}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestReapplyIgnoresAnOutdatedRead(t *testing.T) {
	e, fake := newEngine(t)
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "latency", DurationSec: 1000}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.StopAll() })
	started := len(fake.log())
	// The check round read redis before the fault started; Toxiproxy has it now.
	fake.mu.Lock()
	fake.proxies = map[string]toxiproxy.Proxy{"redis": {Name: "redis", Enabled: true, Toxics: []toxiproxy.Toxic{{Name: "entropy_latency"}}}}
	fake.mu.Unlock()
	e.Reapply(context.Background(), map[string]toxiproxy.Proxy{"redis": {Name: "redis", Enabled: true}})
	if got := fake.log()[started:]; len(got) != 0 {
		t.Errorf("requests %q; the fault is in place, so nothing should change", got)
	}
}

func TestReapplySkipsAFaultBeingRemoved(t *testing.T) {
	e, fake := newEngine(t)
	fake.failRemovals = 1000 // the removal keeps failing, so the fault stays in Removing
	if _, err := e.Start(context.Background(), "redis", Spec{Type: "drop", DurationSec: 1}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); ; time.Sleep(2 * time.Millisecond) {
		if e.Active()["redis"].Removing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fault never started being removed")
		}
	}
	e.Reapply(context.Background(), map[string]toxiproxy.Proxy{"redis": {Name: "redis", Enabled: true}})
	if n := strings.Count(strings.Join(fake.log(), "\n"), "POST /proxies/redis/toxics"); n != 1 {
		t.Errorf("toxic added %d times; a fault being removed must not be put back", n)
	}
	fake.mu.Lock()
	fake.failRemovals = 0
	fake.mu.Unlock()
	waitRemoved(t, e, "redis")
}

func TestSpecWithDefaults(t *testing.T) {
	ok := []Spec{
		{Type: "latency", DurationSec: 30},
		{Type: "drop", DurationSec: 1},
		{Type: "timeout", DurationSec: 30, TimeoutMs: 500},
		{Type: "outage", DurationSec: MaxDurationSec},
		{Type: "flap", DurationSec: 60, DownSec: 3},
	}
	for _, s := range ok {
		if _, err := s.WithDefaults(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []Spec{
		{Type: "chaos", DurationSec: 30},
		{Type: "outage"},
		{Type: "outage", DurationSec: MaxDurationSec + 1},
		{Type: "outage", DurationSec: 30, LatencyMs: 100},
		{Type: "latency", DurationSec: 30, LatencyMs: -1},
		{Type: "drop", DurationSec: 30, TimeoutMs: 100},
		{Type: "latency", DurationSec: 30, DownSec: 1},
	}
	for _, s := range bad {
		if _, err := s.WithDefaults(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: err = %v, want ErrInvalid", s, err)
		}
	}
	if s, _ := (Spec{Type: "flap", DurationSec: 60}).WithDefaults(); s.DownSec != 5 || s.UpSec != 5 {
		t.Errorf("flap defaults = %d/%d, want 5/5", s.DownSec, s.UpSec)
	}
}
