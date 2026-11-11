package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"entropy/internal/store"
	"entropy/internal/toxiproxy"
)

// freePort returns a loopback port nothing listens on right now.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func startBuiltin(t *testing.T) *toxiproxy.Builtin {
	t.Helper()
	b, err := toxiproxy.StartBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func depStatus(t *testing.T, a *App, name string) Status {
	t.Helper()
	v, err := a.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range v.Dependencies {
		if d.Name == name {
			return d.Status
		}
	}
	t.Fatalf("no dependency %s in the view", name)
	return Status{}
}

// The whole built-in flow: no toxiproxy block in entropy.yaml, and Entropy
// alone gets dependencies Reachable through proxies it runs itself.
func TestBuiltinToxiproxy(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(upstream.Close)
	real := strings.TrimPrefix(upstream.URL, "http://")

	// Not t.TempDir: on Windows, a virus scanner can still hold the database
	// file at cleanup, which fails the test. Leftovers here are harmless.
	dir, err := os.MkdirTemp("", "entropy-builtin-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "entropy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	path := filepath.Join(dir, "entropy.yaml")
	writeConfig := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sync := func(a *App) {
		t.Helper()
		if res := a.Sync(ctx); res.Error != "" || len(res.Warnings) > 0 {
			t.Fatalf("sync: error %q, warnings %q", res.Error, res.Warnings)
		}
		a.checkAll(ctx)
	}

	b := startBuiltin(t)
	a := New(path, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.UseBuiltin(b)

	apiPort := freePort(t)
	deps := `
dependencies:
  - name: api
    kind: http
    address: auto:` + apiPort + `
    upstream: ` + real + `
`
	writeConfig(deps)
	sync(a)
	if s := depStatus(t, a, "api"); s.State != Reachable {
		t.Fatalf("api through the built-in proxy: %+v", s)
	}
	if v, _ := a.View(ctx); !v.Toxiproxy.Builtin {
		t.Error("view doesn't say the built-in Toxiproxy is in use")
	}
	proxies, err := toxiproxy.New(b.URL()).Proxies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := proxies["api"].Listen; got != "127.0.0.1:"+apiPort {
		t.Errorf("proxy listens on %q, want loopback only", got)
	}

	// A dependency added while running gets its port on the next Sync.
	webPort := freePort(t)
	deps += `  - name: web
    kind: http
    address: auto:` + webPort + `
    upstream: ` + real + `
`
	writeConfig(deps)
	sync(a)
	if s := depStatus(t, a, "web"); s.State != Reachable {
		t.Fatalf("web, added without a restart: %+v", s)
	}

	// A port another program holds is named as the problem.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { taken.Close() })
	_, takenPort, _ := net.SplitHostPort(taken.Addr().String())
	writeConfig(deps + `  - name: busy
    kind: http
    address: auto:` + takenPort + `
    upstream: ` + real + `
`)
	if res := a.Sync(ctx); !strings.Contains(strings.Join(res.Warnings, "\n"), "port "+takenPort+" is already in use") {
		t.Errorf("sync warnings don't name the taken port: %q", res.Warnings)
	}
	a.checkAll(ctx)
	if s := depStatus(t, a, "busy"); s.State != Unreachable || !strings.Contains(s.Detail, "port "+takenPort+" is already in use") {
		t.Errorf("busy: %+v", s)
	}

	// Switching to an external Toxiproxy frees the built-in one's ports.
	external := startBuiltin(t) // stands in for a Toxiproxy container
	writeConfig("toxiproxy:\n  api: " + external.URL() + "\n" + deps)
	sync(a)
	if left, err := toxiproxy.New(b.URL()).Proxies(ctx); err != nil || len(left) > 0 {
		t.Errorf("built-in proxies left after switching to external: %v, %v", left, err)
	}
	if v, _ := a.View(ctx); v.Toxiproxy.Builtin || v.Toxiproxy.API != external.URL() {
		t.Errorf("view after switching: builtin %v, api %q", v.Toxiproxy.Builtin, v.Toxiproxy.API)
	}
}
