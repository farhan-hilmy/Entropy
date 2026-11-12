package app

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"entropy/internal/store"
)

// A health check made while a dependency the service uses is down (the
// service may wait out its own timeouts) isn't a timing sample; one made with
// everything up is.
func TestHealthTimingOnlyCountsWithEveryDependencyUp(t *testing.T) {
	a := New("entropy.yaml", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := store.Service{Name: "oauth", HealthCommand: "token", Uses: []string{"redis", "mongodb"},
		Commands: []store.Command{{Name: "token", Run: "go version", Cwd: t.TempDir(), TimeoutSec: 30}}}
	took := func() bool {
		t.Helper()
		a.mu.Lock()
		a.healthTook = nil
		a.mu.Unlock()
		if _, err := a.runHealthCommand(context.Background(), svc); err != nil {
			t.Fatal(err)
		}
		a.mu.RLock()
		defer a.mu.RUnlock()
		_, ok := a.healthTook["oauth/token"]
		return ok
	}

	a.deps = map[string]Status{"redis": {State: Unreachable}, "mongodb": {State: Reachable}}
	if took() {
		t.Error("health timing recorded while redis was Unreachable")
	}
	a.deps = map[string]Status{"mongodb": {State: Reachable}} // redis not checked yet
	if took() {
		t.Error("health timing recorded before redis was checked")
	}
	a.deps = map[string]Status{"redis": {State: Reachable}, "mongodb": {State: Reachable}}
	if !took() {
		t.Error("health timing not recorded with every dependency Reachable")
	}
}
