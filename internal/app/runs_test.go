package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"entropy/internal/config"
	"entropy/internal/faults"
	"entropy/internal/store"
	"entropy/internal/toxiproxy"
)

// fakeProxy is a Toxiproxy with one proxy, redis, that keeps real state.
type fakeProxy struct {
	mu      sync.Mutex
	enabled bool
	toxics  map[string]string // name -> type
}

func (p *fakeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/proxies":
		var toxics []toxiproxy.Toxic
		for name, typ := range p.toxics {
			toxics = append(toxics, toxiproxy.Toxic{Name: name, Type: typ})
		}
		json.NewEncoder(w).Encode(map[string]toxiproxy.Proxy{"redis": {Name: "redis", Enabled: p.enabled, Toxics: toxics}})
	case r.Method == http.MethodPost && r.URL.Path == "/proxies/redis":
		var in struct{ Enabled bool }
		json.Unmarshal(body, &in)
		p.enabled = in.Enabled
	case r.Method == http.MethodPost && r.URL.Path == "/proxies/redis/toxics":
		var t toxiproxy.Toxic
		json.Unmarshal(body, &t)
		p.toxics[t.Name] = t.Type
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/proxies/redis/toxics/"):
		delete(p.toxics, strings.TrimPrefix(r.URL.Path, "/proxies/redis/toxics/"))
	case r.URL.Path == "/reset":
		p.enabled, p.toxics = true, map[string]string{}
	}
}

// state is what a service going through the proxy experiences now.
func (p *fakeProxy) state() (down, slow bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, typ := range p.toxics {
		slow = slow || typ == "latency"
	}
	return !p.enabled, slow
}

type runFixture struct {
	app   *App
	proxy *fakeProxy
	cfg   *config.Config // as synced; tests can change it and sync again
	// The service: whether it goes through the proxy, and the status it
	// answers during an outage (fails fast with 503, or breaks with 500).
	viaProxy      bool
	outageStatus  int
	baselineError bool
}

func newRunFixture(t *testing.T, exp config.Experiment) *runFixture {
	old := routeProbeLatency
	routeProbeLatency = 200 * time.Millisecond
	t.Cleanup(func() { routeProbeLatency = old })

	f := &runFixture{proxy: &fakeProxy{enabled: true, toxics: map[string]string{}}, viaProxy: true, outageStatus: 503}
	tp := httptest.NewServer(f.proxy)
	t.Cleanup(tp.Close)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond) // a real service takes some time
		if f.baselineError {
			http.Error(w, "broken", http.StatusInternalServerError)
			return
		}
		if !f.viaProxy {
			return // talks to the real redis: never affected
		}
		down, slow := f.proxy.state()
		if slow {
			time.Sleep(routeProbeLatency)
		}
		if down {
			http.Error(w, "redis unavailable", f.outageStatus)
		}
	}))
	t.Cleanup(svc.Close)

	dir, err := os.MkdirTemp("", "entropy-run-test")
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
		Dependencies: []config.Dependency{{Name: "redis", Kind: "redis", Address: "127.0.0.1:6379", Upstream: "redis:6379", Listen: "0.0.0.0:6379"}},
		Services:     []config.Service{{Name: "oauth", BaseURL: svc.URL, HealthCheck: "/health", Uses: []string{"redis"}}},
		Experiments:  []config.Experiment{exp},
	}
	if _, err := st.Sync(context.Background(), cfg, "entropy.yaml"); err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	f.app = New("entropy.yaml", st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.app.tp = toxiproxy.New(tp.URL)
	f.app.deps = map[string]Status{"redis": {State: Reachable}} // as the check round would have it
	t.Cleanup(func() { f.app.abortActiveRun("test ended"); f.app.faults.StopAll() })
	return f
}

// outageExperiment: 1 s outage on redis; oauth should answer 503 during it
// and be back to 200 within 5 s.
func outageExperiment() config.Experiment {
	req := func(name, phase string, repeat int) config.Trigger {
		return config.Trigger{Name: name, Phase: phase, Service: "oauth", Repeat: repeat,
			Request: &config.Request{Method: "GET", Path: "/token"}}
	}
	return config.Experiment{
		Name: "redis-outage-oauth", MaxDurationSec: 60, RecoveryWindowSec: 5,
		Faults:   []config.ExperimentFault{{Dependency: "redis", Spec: faults.Spec{Type: "outage", DurationSec: 1}}},
		Triggers: []config.Trigger{req("before", "baseline", 1), req("during", "during", 2), req("after", "after", 1)},
		Assertions: []config.Assertion{
			{Type: "response", Trigger: "before", Expect: config.Expect{Status: 200}},
			{Type: "count", Trigger: "during", Expect: config.Expect{Status: 503}, AtLeast: 2},
			{Type: "eventual", Trigger: "after", Expect: config.Expect{Status: 200}, WithinSec: 5},
			{Type: "recovery", Service: "oauth", WithinSec: 5},
		},
	}
}

// runToEnd starts the experiment and waits for its verdict.
func (f *runFixture) runToEnd(t *testing.T) store.Run {
	t.Helper()
	run, err := f.app.StartRun(context.Background(), "redis-outage-oauth")
	if err != nil {
		t.Fatal(err)
	}
	return f.waitDone(t, run.ID)
}

// waitDone waits for the run's verdict.
func (f *runFixture) waitDone(t *testing.T, id string) store.Run {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		got, err := f.app.ExperimentRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == stateDone && f.app.activeRunID() == "" {
			return got
		}
	}
	t.Fatal("the run didn't finish")
	return store.Run{}
}

func (f *runFixture) noFaultLeft(t *testing.T) {
	t.Helper()
	if down, slow := f.proxy.state(); down || slow || len(f.app.faults.Active()) > 0 {
		t.Errorf("faults left after the run: proxy down %v, slow %v, active %v", down, slow, f.app.faults.Active())
	}
}

func timeline(r store.Run) string {
	var lines []string
	for _, e := range r.Events {
		lines = append(lines, e.Type+": "+e.Message)
	}
	return strings.Join(lines, "\n")
}

func TestRunPassesWhenTheServiceDegradesAndRecovers(t *testing.T) {
	f := newRunFixture(t, outageExperiment())
	r := f.runToEnd(t)
	if r.Verdict != VerdictPassed {
		t.Fatalf("verdict %s (%s)\n%s", r.Verdict, r.Reason, timeline(r))
	}
	if len(r.Assertions) != 4 {
		t.Errorf("assertion results = %+v; want all 4", r.Assertions)
	}
	for _, as := range r.Assertions {
		if as.Outcome != "pass" {
			t.Errorf("assertion %d: %s (%s)", as.Index, as.Outcome, as.Detail)
		}
	}
	var during []result
	for _, e := range r.Events {
		var x result
		if e.Type == "response" && json.Unmarshal(e.Data, &x) == nil && x.Trigger == "during" {
			during = append(during, x)
		}
	}
	if len(during) != 2 || during[0].Status != 503 || during[0].DurationMs < 5 {
		t.Errorf("during results = %+v; want 2, the first already seeing the outage, each with its duration", during)
	}
	for _, want := range []string{"phase: preflight", "Route check:", "phase: baseline", "fault: outage on redis for 1 s", "phase: during",
		"fault: outage on redis removed", "phase: after", "healthy again", "phase: evaluating"} {
		if !strings.Contains(timeline(r), want) {
			t.Errorf("timeline is missing %q\n%s", want, timeline(r))
		}
	}
	f.noFaultLeft(t)
}

func TestDuringRequestsOnlyCountWhileTheFaultIsOn(t *testing.T) {
	exp := outageExperiment()
	exp.Triggers[1].Repeat, exp.Triggers[1].EverySec = 20, 1 // far more than fit in the 1 s outage
	exp.Assertions[1] = config.Assertion{Type: "response", Trigger: "during", Expect: config.Expect{Status: 503}}
	f := newRunFixture(t, exp)
	r := f.runToEnd(t)
	if r.Verdict != VerdictPassed {
		t.Fatalf("verdict %s (%s); requests after the outage must not count as during it\n%s", r.Verdict, r.Reason, timeline(r))
	}
	if !strings.Contains(timeline(r), "was cut off and doesn't count: the faults ended") &&
		strings.Count(timeline(r), "response: during") >= 20 {
		t.Errorf("the during requests should stop when the fault ends\n%s", timeline(r))
	}
}

func TestEditingTheExperimentDuringARunDoesntChangeIt(t *testing.T) {
	exp := outageExperiment()
	exp.Faults[0].DurationSec = 2
	f := newRunFixture(t, exp)
	run, err := f.app.StartRun(context.Background(), exp.Name)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); len(f.app.faults.Active()) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the fault never started")
		}
	}
	// Mid-run, someone saves an edit that would fail this run (expect 200
	// during the outage), and the oauth service's URL changes; Sync stores both.
	edited := *f.cfg
	edited.Experiments = []config.Experiment{outageExperiment()}
	edited.Experiments[0].Assertions[1].Expect.Status = 200
	edited.Services = []config.Service{f.cfg.Services[0]}
	edited.Services[0].BaseURL = "http://127.0.0.1:1"
	if _, err := f.app.store.Sync(context.Background(), &edited, "entropy.yaml"); err != nil {
		t.Fatal(err)
	}

	r := f.waitDone(t, run.ID)
	if r.Verdict != VerdictPassed {
		t.Fatalf("verdict %s (%s); the run must keep using what it started with\n%s", r.Verdict, r.Reason, timeline(r))
	}
	var snap runSnapshot
	json.Unmarshal(r.Snapshot, &snap)
	if r.Version != 1 || snap.Experiment.Assertions[1].Expect.Status != 503 || snap.Services["oauth"].BaseURL == "http://127.0.0.1:1" {
		t.Errorf("run v%d snapshot = %+v; want version 1 as it started", r.Version, snap.Experiment.Assertions[1])
	}
	exps, _ := f.app.Experiments(context.Background())
	if exps[0].Version != 2 {
		t.Errorf("experiment version after the edit = %d, want 2 (the next run uses it)", exps[0].Version)
	}
}

func TestRunFailsWhenAnAssertionDoesnt(t *testing.T) {
	f := newRunFixture(t, outageExperiment())
	f.outageStatus = 500 // breaks instead of failing fast
	r := f.runToEnd(t)
	if r.Verdict != VerdictFailed || !strings.Contains(r.Reason, "0 of 2 results of during matched status 503") {
		t.Fatalf("verdict %s (%s); want failed on the count assertion\n%s", r.Verdict, r.Reason, timeline(r))
	}
	f.noFaultLeft(t)
}

func TestRunIsInconclusiveWhenTheServiceSkipsTheProxy(t *testing.T) {
	f := newRunFixture(t, outageExperiment())
	f.viaProxy = false
	r := f.runToEnd(t)
	if r.Verdict != VerdictInconclusive || !strings.Contains(r.Reason, "doesn't seem to reach redis through the proxy") {
		t.Fatalf("verdict %s (%s); want inconclusive from the route check, not a false pass", r.Verdict, r.Reason)
	}
	if strings.Contains(timeline(r), "fault: outage") {
		t.Errorf("no fault should be applied when preflight fails\n%s", timeline(r))
	}
}

func TestRunIsInconclusiveWhenTheBaselineFails(t *testing.T) {
	exp := outageExperiment()
	exp.Faults = nil // no route check, so the broken service gets to the baseline
	f := newRunFixture(t, exp)
	f.baselineError = true
	r := f.runToEnd(t)
	if r.Verdict != VerdictInconclusive || !strings.Contains(r.Reason, "steady state didn't hold") {
		t.Fatalf("verdict %s (%s); want inconclusive from the baseline", r.Verdict, r.Reason)
	}
}

func TestStoppingARunAbortsItAndRemovesItsFaults(t *testing.T) {
	exp := outageExperiment()
	exp.Faults[0].DurationSec = 60
	f := newRunFixture(t, exp)
	run, err := f.app.StartRun(context.Background(), exp.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.StartRun(context.Background(), exp.Name); !errors.Is(err, ErrRunActive) {
		t.Errorf("second run: err = %v, want ErrRunActive", err)
	}
	for deadline := time.Now().Add(10 * time.Second); len(f.app.faults.Active()) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the fault never started")
		}
	}
	if err := f.app.StopAllFaults(context.Background()); err != nil { // the kill switch
		t.Fatal(err)
	}
	r, _ := f.app.ExperimentRun(context.Background(), run.ID)
	if r.Verdict != VerdictAborted || !strings.Contains(r.Reason, "Stop all faults") {
		t.Errorf("verdict %s (%s); want aborted by Stop all faults", r.Verdict, r.Reason)
	}
	f.noFaultLeft(t)
}
