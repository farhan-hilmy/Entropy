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
