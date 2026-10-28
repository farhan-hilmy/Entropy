package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entropy/internal/config"
	"entropy/internal/faults"
	"entropy/internal/store"
	"entropy/internal/toxiproxy"
)

// routeFixture is an App with one dependency (redis) and one service (oauth)
// whose /health handler runs onRequest. Toxiproxy is faked: it only tracks
// whether the route check's latency toxic is on the redis proxy.
type routeFixture struct {
	app        *App
	latencyOn  atomic.Bool
	toxicCalls atomic.Int64 // adds and removes of the probe toxic
	// onRequest is how the fake service answers. Requests can come from
	// another process (a command running curl), so it's read and set atomically.
	onRequest atomic.Pointer[func(w http.ResponseWriter)]
}

// respond sets how the fake service answers.
func (f *routeFixture) respond(h func(w http.ResponseWriter)) { f.onRequest.Store(&h) }

func newRouteFixture(t *testing.T, healthCheck string) *routeFixture {
	old := routeProbeLatency
	routeProbeLatency = 200 * time.Millisecond
	t.Cleanup(func() { routeProbeLatency = old })

	f := &routeFixture{}
	f.respond(func(http.ResponseWriter) {})
	tp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/proxies/redis/toxics":
			f.latencyOn.Store(true)
			f.toxicCalls.Add(1)
		case r.Method == http.MethodDelete && r.URL.Path == "/proxies/redis/toxics/"+probeToxic:
			f.latencyOn.Store(false)
			f.toxicCalls.Add(1)
		}
	}))
	t.Cleanup(tp.Close)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*f.onRequest.Load())(w) }))
	t.Cleanup(svc.Close)

	// Not t.TempDir: on Windows, a virus scanner can still hold the database
	// file at cleanup, which fails the test. Leftovers here are harmless.
	dir, err := os.MkdirTemp("", "entropy-route-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "entropy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Environment:  config.Environment{Name: "local"},
		Dependencies: []config.Dependency{{Name: "redis", Kind: "redis", Address: "172.28.142.159:6379", Upstream: "redis:6379", Listen: "0.0.0.0:6379"}},
		Services: []config.Service{{Name: "oauth", BaseURL: svc.URL, HealthCheck: healthCheck, Uses: []string{"redis"},
			// Plays Bruno: prints a request line with the service's answer.
			Commands: []config.Command{{Name: "bruno", Run: "curl -s " + svc.URL + "/bruno", Cwd: dir, TimeoutSec: 30}}}},
	}
	if _, err := st.Sync(context.Background(), cfg, "entropy.yaml"); err != nil {
		t.Fatal(err)
	}
	f.app = New("entropy.yaml", st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.app.tp = toxiproxy.New(tp.URL)
	t.Cleanup(func() { f.app.faults.StopAll() })
	return f
}

// throughProxy makes the service behave as if it reaches redis through the
// proxy: slowed down while the latency is on.
func (f *routeFixture) throughProxy(w http.ResponseWriter) {
	if f.latencyOn.Load() {
		time.Sleep(routeProbeLatency)
	}
}

func TestVerifyRouteSeesTheSlowdown(t *testing.T) {
	f := newRouteFixture(t, "/health")
	var pausedDuringRequest atomic.Bool
	f.respond(func(w http.ResponseWriter) {
		pausedDuringRequest.Store(f.app.isVerifying("redis"))
		f.throughProxy(w)
	})
	rc, err := f.app.VerifyRoute(context.Background(), "redis", "oauth", "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.State != RouteVerified || rc.Trigger != "GET /health" || rc.SlowedMs-rc.NormalMs < 150 {
		t.Errorf("route = %+v; want verified, GET /health, slowed by about 200 ms", rc)
	}
	if !pausedDuringRequest.Load() {
		t.Error("Entropy's own checks of redis should be paused while the request runs")
	}
	if f.latencyOn.Load() || f.toxicCalls.Load() != 2 {
		t.Errorf("latency on = %v after %d toxic calls; it must be added once and removed", f.latencyOn.Load(), f.toxicCalls.Load())
	}
	if f.app.isVerifying("redis") {
		t.Error("the pause should end with the check")
	}
	if got := f.app.routesByDependency()["redis"]["oauth"]; got.State != RouteVerified {
		t.Errorf("saved route = %+v; the view should show the result", got)
	}
}

func TestVerifyRouteUsesTheRequestTimeBrunoPrints(t *testing.T) {
	f := newRouteFixture(t, "")
	// Bruno's startup varies by seconds; here the run with latency even starts
	// faster overall, which made the whole-command comparison say "not seen".
	f.respond(func(w http.ResponseWriter) {
		requestMs := 50
		if f.latencyOn.Load() {
			requestMs += int(routeProbeLatency.Milliseconds()) // what the latency does to the real request
		} else {
			time.Sleep(3 * routeProbeLatency) // a slow startup, on the run without latency
		}
		fmt.Fprintf(w, "Oauth Service\\Access Token B2B (200 OK) - %d ms\nExecution Summary\n", requestMs)
	})
	rc, err := f.app.VerifyRoute(context.Background(), "redis", "oauth", "bruno")
	if err != nil {
		t.Fatal(err)
	}
	if rc.State != RouteVerified || rc.NormalMs != 50 || rc.SlowedMs != 250 || !strings.Contains(rc.Detail, "the HTTP request it printed") {
		t.Errorf("route = %+v; want verified from the printed request times (50 ms -> 250 ms)", rc)
	}
}

func TestVerifyRouteWaitsForTheCommandToBeFree(t *testing.T) {
	f := newRouteFixture(t, "")
	f.respond(func(w http.ResponseWriter) {
		requestMs := 50
		if f.latencyOn.Load() {
			requestMs += int(routeProbeLatency.Milliseconds())
		}
		fmt.Fprintf(w, "Req (200 OK) - %d ms\n", requestMs)
	})
	// The same command is running as a health check, e.g. started by Sync.
	release, _ := f.app.claim("oauth", "bruno")
	go func() { time.Sleep(500 * time.Millisecond); release() }()

	rc, err := f.app.VerifyRoute(context.Background(), "redis", "oauth", "bruno")
	if err != nil {
		t.Fatalf("VerifyRoute = %v; it should wait for the running command, not give up", err)
	}
	if rc.State != RouteVerified {
		t.Errorf("route = %+v; want verified once the command was free", rc)
	}
}

func TestRequestTime(t *testing.T) {
	cases := map[string]time.Duration{
		"Oauth Service\\Access Token B2B (200 OK) - 52 ms\nafter-datanya: {}":   52 * time.Millisecond,
		"A (500 Internal Server Error) - 14493 ms\nB (200 OK) - 7 ms":           14500 * time.Millisecond,
		"Oauth Service\\Access Token B2B (connect ECONNREFUSED 127.0.0.1:8080)": 0, // no response, no time
		"plain output": 0,
	}
	for out, want := range cases {
		if got := requestTime(out); got != want {
			t.Errorf("requestTime(%q) = %s, want %s", out, got, want)
		}
	}
}

func TestVerifyRouteCountsAFailureUnderLatency(t *testing.T) {
	f := newRouteFixture(t, "/health")
	f.respond(func(w http.ResponseWriter) {
		if f.latencyOn.Load() { // e.g. the service's Redis timeout is shorter than the latency
			http.Error(w, "redis timeout", http.StatusInternalServerError)
		}
	})
	rc, err := f.app.VerifyRoute(context.Background(), "redis", "oauth", "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.State != RouteVerified || !strings.Contains(rc.Detail, "failed with") {
		t.Errorf("route = %+v; a request that fails only with latency on goes through the proxy", rc)
	}
}

func TestVerifyRouteNotSeen(t *testing.T) {
	f := newRouteFixture(t, "/health") // the service answers without going through the proxy
	rc, err := f.app.VerifyRoute(context.Background(), "redis", "oauth", "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.State != RouteNotSeen || !strings.Contains(rc.Detail, "straight to redis:6379 instead of 172.28.142.159:6379") {
		t.Errorf("route = %+v; want not_seen naming both addresses", rc)
	}
	if f.latencyOn.Load() {
		t.Error("the latency must be removed")
	}
}

func TestVerifyRouteUnknownWhenTheRequestAlwaysFails(t *testing.T) {
	f := newRouteFixture(t, "/health")
	f.respond(func(w http.ResponseWriter) { http.Error(w, "down", http.StatusServiceUnavailable) })
	rc, err := f.app.VerifyRoute(context.Background(), "redis", "oauth", "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.State != RouteUnknown || f.toxicCalls.Load() != 0 {
		t.Errorf("route = %+v, %d toxic calls; a request failing without latency proves nothing, and needs no latency", rc, f.toxicCalls.Load())
	}
}

func TestVerifyRouteRefusals(t *testing.T) {
	ctx := context.Background()
	noHealth := newRouteFixture(t, "")
	if _, err := noHealth.app.VerifyRoute(ctx, "redis", "oauth", ""); !errors.Is(err, ErrNoTrigger) {
		t.Errorf("only a port check to run: err = %v, want ErrNoTrigger", err)
	}
	if _, err := noHealth.app.VerifyRoute(ctx, "redis", "oauth", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown command: err = %v, want ErrNotFound", err)
	}

	f := newRouteFixture(t, "/health")
	if _, err := f.app.faults.Start(ctx, "redis", faults.Spec{Type: "latency", DurationSec: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.VerifyRoute(ctx, "redis", "oauth", ""); !errors.Is(err, ErrFaultActive) {
		t.Errorf("fault on redis: err = %v, want ErrFaultActive", err)
	}
}
