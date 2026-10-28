package app

import (
	"context"
	"errors"
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
	onRequest  func(w http.ResponseWriter)
}

func newRouteFixture(t *testing.T, healthCheck string) *routeFixture {
	old := routeProbeLatency
	routeProbeLatency = 200 * time.Millisecond
	t.Cleanup(func() { routeProbeLatency = old })

	f := &routeFixture{onRequest: func(http.ResponseWriter) {}}
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
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.onRequest(w) }))
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
		Services:     []config.Service{{Name: "oauth", BaseURL: svc.URL, HealthCheck: healthCheck, Uses: []string{"redis"}}},
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
	var pausedDuringRequest bool
	f.onRequest = func(w http.ResponseWriter) {
		pausedDuringRequest = f.app.isVerifying("redis")
		f.throughProxy(w)
	}
	rc, err := f.app.VerifyRoute(context.Background(), "redis", "oauth", "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.State != RouteVerified || rc.Trigger != "GET /health" || rc.SlowedMs-rc.NormalMs < 150 {
		t.Errorf("route = %+v; want verified, GET /health, slowed by about 200 ms", rc)
	}
	if !pausedDuringRequest {
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

func TestVerifyRouteCountsAFailureUnderLatency(t *testing.T) {
	f := newRouteFixture(t, "/health")
	f.onRequest = func(w http.ResponseWriter) {
		if f.latencyOn.Load() { // e.g. the service's Redis timeout is shorter than the latency
			http.Error(w, "redis timeout", http.StatusInternalServerError)
		}
	}
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
	f.onRequest = func(w http.ResponseWriter) { http.Error(w, "down", http.StatusServiceUnavailable) }
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
