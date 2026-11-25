package mcpserver

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for `entropy up`: started with
// ENTROPY_FAKE_UP set, it serves a minimal API on -addr until stdin closes,
// then writes "stopped" in its folder, as the real one removes its faults.
func TestMain(m *testing.M) {
	switch os.Getenv("ENTROPY_FAKE_UP") {
	case "":
		os.Exit(m.Run())
	case "fail":
		fmt.Fprintln(os.Stderr, "entropy: sync failed: entropy.yaml: line 3: bad")
		os.Exit(1)
	}
	var addr string
	for i, a := range os.Args {
		if a == "-addr" && i+1 < len(os.Args) {
			addr = os.Args[i+1]
		}
	}
	if !strings.Contains(strings.Join(os.Args, " "), "up -config entropy.yaml -addr "+addr+" -stop-with-stdin") {
		fmt.Fprintln(os.Stderr, "unexpected args:", os.Args)
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"name":"started","status":"ready","toxiproxy":{"builtin":true,"status":{"state":"reachable"}}}`)
	}))
	io.Copy(io.Discard, os.Stdin)
	os.WriteFile("stopped", []byte("ok"), 0o644)
	os.Exit(0)
}

// freeURL is a local URL nothing listens on.
func freeURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return "http://" + ln.Addr().String()
}

func fakeStarter(t *testing.T, base, mode string) (*Starter, string) {
	t.Helper()
	// Not t.TempDir: on Windows the started process's log can still be held
	// for a moment after it exits, and t.TempDir's cleanup then fails the
	// test. Leftovers are harmless.
	dir, err := os.MkdirTemp("", "entropy-autostart-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := filepath.Join(dir, "entropy.yaml")
	os.WriteFile(cfg, []byte("environment: {name: x}\n"), 0o644)
	t.Setenv("ENTROPY_FAKE_UP", mode) // inherited by the started process
	s, err := NewStarter(base, os.Args[0], cfg)
	if err != nil || s == nil {
		t.Fatalf("NewStarter: %v, %v", s, err)
	}
	s.Ready = 15 * time.Second
	return s, dir
}

func TestAutostart(t *testing.T) {
	base := freeURL(t)
	s, dir := fakeStarter(t, base, "1")
	c := NewClient(base)
	c.Starter = s
	cs2 := connectClient(t, c)

	out, isErr := call(t, cs2, "entropy_status", nil)
	if isErr {
		t.Fatal(out)
	}
	contains(t, "first call", out, "Entropy wasn't running, so it was started for this session with "+filepath.Join(dir, "entropy.yaml"),
		"it stops when the session ends", "Environment started: ready")
	out, _ = call(t, cs2, "entropy_status", nil)
	if strings.Contains(out, "wasn't running") {
		t.Errorf("the notice came twice:\n%s", out)
	}

	s.Stop(10 * time.Second)
	if b, err := os.ReadFile(filepath.Join(dir, "stopped")); err != nil || string(b) != "ok" {
		t.Errorf("the started Entropy didn't stop cleanly on stdin closing: %v", err)
	}

	// Gone (e.g. stopped by hand): the next call starts it again.
	os.Remove(filepath.Join(dir, "stopped"))
	out, isErr = call(t, cs2, "entropy_status", nil)
	if isErr || !strings.Contains(out, "was started for this session") {
		t.Errorf("after a stop: %v\n%s", isErr, out)
	}
	s.Stop(10 * time.Second)
}

func TestAutostartFails(t *testing.T) {
	base := freeURL(t)
	s, _ := fakeStarter(t, base, "fail")
	c := NewClient(base)
	c.Starter = s
	out, isErr := call(t, connectClient(t, c), "entropy_status", nil)
	if !isErr {
		t.Fatalf("not an error: %s", out)
	}
	contains(t, "failed start", out, "starting it failed: started entropy up, but it stopped: entropy: sync failed: entropy.yaml: line 3: bad",
		filepath.Join(".entropy", "entropy-up.log"))
}

func TestNewStarter(t *testing.T) {
	if s, err := NewStarter("https://entropy.team.internal", "entropy", ""); s != nil || err != nil {
		t.Errorf("a remote URL: %v, %v; want no starter", s, err)
	}
	if _, err := NewStarter("http://127.0.0.1:7400", "entropy", filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("a -config that doesn't exist isn't an error")
	}

	// No -config and no entropy.yaml here: the call says how to fix it.
	t.Chdir(t.TempDir())
	base := freeURL(t)
	s, err := NewStarter(base, "entropy", "")
	if err != nil || s == nil || s.Config != "" {
		t.Fatalf("NewStarter: %+v, %v", s, err)
	}
	if s.Addr != strings.TrimPrefix(base, "http://") {
		t.Errorf("Addr = %q", s.Addr)
	}
	c := NewClient(base)
	c.Starter = s
	out, _ := call(t, connectClient(t, c), "entropy_status", nil)
	contains(t, "no config", out, "there's no entropy.yaml in", "add -config")
}
