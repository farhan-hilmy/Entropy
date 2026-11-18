package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeEntropy answers like entropy up's REST API, for one experiment,
// redis-outage, whose run fails.
type fakeEntropy struct {
	mu       sync.Mutex
	polls    int    // GETs of the run so far; it's done on the second
	active   string // the run in progress
	saved    map[string]any
	stopped  []string
	stopAll  int
	finished bool
	conflict int // PATCHes to answer with 409 first, as if the file changed
	patches  int
}

const failedRun = `{"id":"r1","experiment":"redis-outage","version":3,"state":"done","verdict":"failed",
 "reason":"1 of 2 assertions failed","startedAt":"2026-11-18T07:00:00Z","endedAt":"2026-11-18T07:02:10Z",
 "snapshot":{"experiment":{"name":"redis-outage","maxDurationSec":180,
   "assertions":[{"type":"response","trigger":"before"},{"type":"eventual","trigger":"after"}]}},
 "events":[{"at":"2026-11-18T07:00:00Z","type":"phase","message":"baseline"},
   {"at":"2026-11-18T07:00:30Z","type":"fault","message":"redis: outage for 30 s"},
   {"at":"2026-11-18T07:00:35Z","type":"response","message":"during #1 (orders/token): exit code 1: 500 Internal Server Error in 5012 ms"}],
 "assertions":[{"index":0,"outcome":"pass","detail":"1 of 1 matched"},
   {"index":1,"outcome":"fail","detail":"none of 3 matched exit code 0 within 20 s; last: exit code 1"}]}`

func (f *fakeEntropy) handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/environment", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		io.WriteString(w, `{"name":"local","activeRun":"`+f.active+`","configPath":"entropy.yaml","status":"ready","activeFaults":0,
		 "toxiproxy":{"api":"http://127.0.0.1:1","builtin":true,"status":{"state":"reachable"}},"lastSync":{},
		 "dependencies":[{"name":"redis","kind":"redis","address":"127.0.0.1:16379","upstream":"127.0.0.1:6379","ready":true,
		   "status":{"state":"reachable"},"usedBy":["orders"]}],
		 "services":[{"name":"orders","uses":["redis"],"status":{"state":"unreachable","detail":"exit code 1"},
		   "commands":[{"name":"token","run":"bru run token.bru"}]}]}`)
	})
	m.HandleFunc("GET /api/experiments", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"name":"redis-outage","version":3,"definition":{"name":"redis-outage","description":"Redis down.",
		  "maxDurationSec":180,"faults":[{"dependency":"redis","type":"outage","durationSec":30}]},
		  "timing":{"measured":"full","warnings":["it may not fit"]},"lastRun":`+failedRun+`},
		 {"name":"gone","version":1,"missing":true,"definition":{"name":"gone"},"timing":{}}]`)
	})
	m.HandleFunc("GET /api/experiments/{name}/source", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "redis-outage" {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"not found: no experiment \"`+r.PathValue("name")+`\""}`)
			return
		}
		io.WriteString(w, `{"hash":"h1","yaml":"name: redis-outage\n# keep\nfaults: []\n"}`)
	})
	m.HandleFunc("POST /api/experiments/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.active != "" {
			w.WriteHeader(409)
			io.WriteString(w, `{"error":"another experiment is running"}`)
			return
		}
		f.active = "r1"
		io.WriteString(w, `{"id":"r1","experiment":"redis-outage","version":3,"state":"preflight","startedAt":"2026-11-18T07:00:00Z"}`)
	})
	m.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "["+failedRun+"]")
	})
	m.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		if f.finished || f.polls >= 2 {
			f.active = ""
			io.WriteString(w, failedRun)
			return
		}
		io.WriteString(w, `{"id":"r1","experiment":"redis-outage","version":3,"state":"during","startedAt":"2026-11-18T07:00:00Z"}`)
	})
	m.HandleFunc("POST /api/runs/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.stopped = append(f.stopped, r.PathValue("id"))
		f.active = ""
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true}`)
	})
	m.HandleFunc("POST /api/faults/stop-all", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.stopAll++
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true}`)
	})
	m.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"hash":"h1","config":{}}`)
	})
	m.HandleFunc("PATCH /api/config", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.saved = body
		f.patches++
		conflict := f.conflict > 0
		f.conflict--
		f.mu.Unlock()
		if conflict {
			w.WriteHeader(409)
			io.WriteString(w, `{"error":"the config file changed since it was read; reload it and try again"}`)
			return
		}
		if strings.Contains(body["experiment"].(map[string]any)["yaml"].(string), "bogus") {
			w.WriteHeader(422)
			io.WriteString(w, `{"error":"experiment: line 2: field bogus not found in type config.Experiment"}`)
			return
		}
		io.WriteString(w, `{"hash":"h2","sync":{"warnings":["a sync warning"]}}`)
	})
	m.HandleFunc("POST /api/services/{s}/commands/{c}/run", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"service":"orders","command":"token","run":"bru run token.bru","durationMs":812,"exitCode":0,
		 "output":"`+strings.Repeat("x", maxOutput)+`END"}`)
	})
	return m
}

// connect starts the MCP server for the API at base and returns a client session.
func connect(t *testing.T, base string, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, NewClient(base), opts)
}

// connectClient starts the MCP server on c and returns a client session.
func connectClient(t *testing.T, c *Client) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, c, nil)
}

func connectWith(t *testing.T, c *Client, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	srv := New(c, "test")
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, opts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Wait() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	p := &mcp.CallToolParams{Name: name, Arguments: args}
	p.SetProgressToken(name) // as a client that shows progress does
	res, err := cs.CallTool(context.Background(), p)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func contains(t *testing.T, tool, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q in:\n%s", tool, w, got)
		}
	}
}

func TestTools(t *testing.T) {
	f := &fakeEntropy{}
	api := httptest.NewServer(f.handler())
	defer api.Close()

	var progress []string
	var pmu sync.Mutex
	cs := connect(t, api.URL, &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
		pmu.Lock()
		progress = append(progress, r.Params.Message)
		pmu.Unlock()
	}})

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "run_experiment") || strings.Contains(got, "start_fault") {
		t.Errorf("tools: %s", got)
	}

	out, isErr := call(t, cs, "entropy_status", nil)
	if isErr {
		t.Fatal(out)
	}
	contains(t, "entropy_status", out, "Environment local: ready", "Toxiproxy: built-in, reachable", "Run in progress: none",
		"- redis (redis): reachable; used by orders; services connect to 127.0.0.1:16379, which forwards to 127.0.0.1:6379",
		"- orders: unreachable (exit code 1); uses redis", "command orders/token: bru run token.bru")

	out, _ = call(t, cs, "list_experiments", nil)
	contains(t, "list_experiments", out, "- redis-outage (v3): Redis down.", "faults: redis outage for 30 s; maxDurationSec 180",
		"last run r1: FAILED: 1 of 2 assertions failed", "timing warning: it may not fit")
	if strings.Contains(out, "gone") {
		t.Errorf("list_experiments shows a missing experiment:\n%s", out)
	}

	out, _ = call(t, cs, "get_experiment", map[string]any{"name": "redis-outage"})
	contains(t, "get_experiment", out, "name: redis-outage\n# keep\nfaults: []\n", "Timing warning: it may not fit", "- r1 (v3, ")
	out, isErr = call(t, cs, "get_experiment", map[string]any{"name": "nope"})
	if !isErr {
		t.Errorf("get_experiment of an unknown experiment isn't an error: %s", out)
	}

	out, _ = call(t, cs, "run_experiment", map[string]any{"name": "redis-outage"})
	contains(t, "run_experiment", out, "Run r1 of redis-outage (v3)", "Result: FAILED: 1 of 2 assertions failed", "Took 2m10s",
		"- #1 response on before: PASS", "- #2 eventual on after: FAIL: none of 3 matched exit code 0 within 20 s",
		"5.0s response during #1 (orders/token): exit code 1: 500")
	// Progress notifications may arrive just after the result.
	for range 50 {
		pmu.Lock()
		n := len(progress)
		pmu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pmu.Lock()
	if got := strings.Join(progress, " | "); got != "run r1: preflight | run r1: during" {
		t.Errorf("progress: %q", got)
	}
	pmu.Unlock()

	// Another run in progress: the error says which, and what to do.
	f.mu.Lock()
	f.active, f.polls = "r9", 0
	f.mu.Unlock()
	out, isErr = call(t, cs, "run_experiment", map[string]any{"name": "redis-outage"})
	if !isErr {
		t.Errorf("run_experiment during another run isn't an error: %s", out)
	}
	contains(t, "run_experiment (busy)", out, "another experiment is running", "Run r9 is in progress", "stop_run")

	out, _ = call(t, cs, "stop_run", nil)
	contains(t, "stop_run", out, "Run r9 aborted")
	out, _ = call(t, cs, "stop_run", nil)
	contains(t, "stop_run (none)", out, "No run is in progress.")
	call(t, cs, "stop_all_faults", nil)
	if f.stopAll != 1 || len(f.stopped) != 1 || f.stopped[0] != "r9" {
		t.Errorf("stops: all %d, runs %v", f.stopAll, f.stopped)
	}

	// waitSec 0: returns at once with the run's id.
	f.mu.Lock()
	f.polls = -10
	f.mu.Unlock()
	out, _ = call(t, cs, "run_experiment", map[string]any{"name": "redis-outage", "waitSec": 0})
	contains(t, "run_experiment (no wait)", out, "Run r1 of redis-outage started and is still running (preflight)", "get_run")
	f.mu.Lock()
	f.active, f.finished = "", true
	f.mu.Unlock()

	out, _ = call(t, cs, "get_run", map[string]any{"id": "r1"})
	contains(t, "get_run", out, "Result: FAILED")
	out, _ = call(t, cs, "list_runs", map[string]any{"experiment": "redis-outage"})
	contains(t, "list_runs", out, "- r1: redis-outage v3, ")

	yaml := "name: redis-outage\nfaults: []\ntriggers:\n  - name: before\n    phase: baseline\n"
	out, isErr = call(t, cs, "save_experiment", map[string]any{"name": "redis-outage", "yaml": yaml})
	if isErr {
		t.Fatal(out)
	}
	contains(t, "save_experiment", out, "Saved redis-outage in entropy.yaml. It is now version 3.", "Config warning: a sync warning")
	if f.saved["hash"] != "h1" || f.saved["experiment"].(map[string]any)["name"] != "redis-outage" {
		t.Errorf("PATCH body: %v", f.saved)
	}
	// The file changed in between: saved on a fresh read; twice in a row: an error.
	f.conflict, f.patches = 1, 0
	if out, isErr = call(t, cs, "save_experiment", map[string]any{"name": "redis-outage", "yaml": yaml}); isErr || f.patches != 2 {
		t.Errorf("save after one conflict: %d PATCHes, %s", f.patches, out)
	}
	f.conflict, f.patches = 2, 0
	if out, isErr = call(t, cs, "save_experiment", map[string]any{"name": "redis-outage", "yaml": yaml}); !isErr || f.patches != 2 {
		t.Errorf("save after two conflicts: %d PATCHes, error %v, %s", f.patches, isErr, out)
	}

	out, isErr = call(t, cs, "save_experiment", map[string]any{"yaml": "name: x\nbogus: 1\n"})
	if !isErr {
		t.Errorf("an invalid experiment isn't an error: %s", out)
	}
	contains(t, "save_experiment (invalid)", out, "field bogus not found")

	out, _ = call(t, cs, "run_command", map[string]any{"service": "orders", "command": "token"})
	contains(t, "run_command", out, "orders/token (bru run token.bru): exit code 0 in 812 ms", "xEND")
	if len(out) > maxOutput+200 {
		t.Errorf("run_command output not cut: %d bytes", len(out))
	}
}

func TestEntropyNotRunning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	ln.Close() // nothing answers there now
	cs := connect(t, base, nil)
	out, isErr := call(t, cs, "entropy_status", nil)
	if !isErr {
		t.Errorf("not an error: %s", out)
	}
	contains(t, "entropy_status", out, "Entropy isn't running at "+base, "entropy up")
}

func TestYAMLName(t *testing.T) {
	for in, want := range map[string]string{
		"name: a\nfaults: []\n":                             "a",
		"# c\n- name: \"b\" # quoted\n  faults: []\n":       "b",
		"description: x\ntriggers:\n  - name: t\nname: c\n": "c",
		"faults: []\n": "",
	} {
		if got := yamlName(in); got != want {
			t.Errorf("yamlName(%q) = %q, want %q", in, got, want)
		}
	}
}
