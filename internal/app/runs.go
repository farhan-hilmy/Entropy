package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"entropy/internal/checks"
	"entropy/internal/command"
	"entropy/internal/config"
	"entropy/internal/faults"
	"entropy/internal/store"

	"gopkg.in/yaml.v3"
)

// A run executes one experiment: preflight → baseline → during (faults on) →
// restore → after (recovery window) → evaluate. Its verdict comes from the
// experiment's assertions: passed, failed, inconclusive (it couldn't be
// judged: the environment wasn't ready, the baseline failed, Toxiproxy went
// away…) or aborted (stopped, or over maxDurationSec). One run at a time.

const (
	VerdictPassed       = "passed"
	VerdictFailed       = "failed"
	VerdictInconclusive = "inconclusive"
	VerdictAborted      = "aborted"

	stateDone = "done"
)

var (
	ErrRunActive   = errors.New("another experiment is running; stop it or wait for it to finish")
	ErrNotFinished = errors.New("this run hasn't finished yet")
)

// restoreWait is how long a run waits for its faults to be removed before
// calling it a cleanup problem.
const restoreWait = 60 * time.Second

// outputTail is how much of a command's output or a response body a run keeps.
const outputTail = 2000

// runSnapshot is exactly what a run ran: the experiment and the services and
// dependencies it uses, as they were when it started.
type runSnapshot struct {
	Experiment   config.Experiment           `json:"experiment"`
	Services     map[string]store.Service    `json:"services"`
	Dependencies map[string]store.Dependency `json:"dependencies"`
}

type activeRun struct {
	id     string
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// stopCause tells an aborted run why.
type stopCause struct{ reason string }

func (c stopCause) Error() string { return c.reason }

// StartRun starts the experiment in the background and returns the new run.
func (a *App) StartRun(ctx context.Context, name string) (store.Run, error) {
	exps, err := a.store.Experiments(ctx)
	if err != nil {
		return store.Run{}, err
	}
	var exp *store.Experiment
	for i := range exps {
		if exps[i].Name == name && !exps[i].Missing {
			exp = &exps[i]
		}
	}
	if exp == nil {
		return store.Run{}, fmt.Errorf("%w: no experiment %s in the config file", ErrNotFound, name)
	}
	snap, err := a.snapshot(ctx, exp.Definition)
	if err != nil {
		return store.Run{}, err
	}
	return a.startRun(ctx, store.Run{Experiment: exp.Name, Version: exp.Version}, snap, nil)
}

// Rerun runs a finished run's experiment again exactly as it ran: the same
// faults, triggers, assertions and service commands, whatever entropy.yaml
// says now. Only where things are (dependency addresses, service base URLs)
// comes from the current setup, since e.g. the Podman VM's address can change
// after a reboot; differences are noted in the new run's timeline.
func (a *App) Rerun(ctx context.Context, id string) (store.Run, error) {
	orig, err := a.ExperimentRun(ctx, id)
	if err != nil {
		return store.Run{}, err
	}
	if orig.State != stateDone {
		return store.Run{}, fmt.Errorf("%w: run %s", ErrNotFinished, id)
	}
	var snap runSnapshot
	if err := json.Unmarshal(orig.Snapshot, &snap); err != nil {
		return store.Run{}, fmt.Errorf("reading run %s's copy of the experiment: %w", id, err)
	}
	notes, err := a.relocate(ctx, &snap)
	if err != nil {
		return store.Run{}, err
	}
	return a.startRun(ctx, store.Run{Experiment: orig.Experiment, Version: orig.Version, RerunOf: orig.ID}, snap, notes)
}

// relocate updates where the snapshot's dependencies and services are to the
// current setup, and describes what it changed.
func (a *App) relocate(ctx context.Context, snap *runSnapshot) ([]string, error) {
	deps, err := a.store.Dependencies(ctx)
	if err != nil {
		return nil, err
	}
	svcs, err := a.store.Services(ctx)
	if err != nil {
		return nil, err
	}
	var notes []string
	for name, old := range snap.Dependencies {
		i := slices.IndexFunc(deps, func(d store.Dependency) bool { return d.Name == name && !d.Missing })
		if i < 0 {
			return nil, fmt.Errorf("%w: dependency %s is no longer in the config file, so the run can't be repeated", ErrNotFound, name)
		}
		now := deps[i]
		if now.Address != old.Address || now.Upstream != old.Upstream || now.Listen != old.Listen {
			notes = append(notes, fmt.Sprintf("%s is now at %s (upstream %s); it was at %s (upstream %s)", name, now.Address, now.Upstream, old.Address, old.Upstream))
		}
		snap.Dependencies[name] = now
	}
	for name, old := range snap.Services {
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.Name == name && !s.Missing })
		if i < 0 {
			return nil, fmt.Errorf("%w: service %s is no longer in the config file, so the run can't be repeated", ErrNotFound, name)
		}
		if now := svcs[i].BaseURL; now != old.BaseURL {
			notes = append(notes, fmt.Sprintf("%s is now at %s; it was at %s", name, now, old.BaseURL))
			old.BaseURL = now
			snap.Services[name] = old // its commands and health check stay as they ran
		}
	}
	return notes, nil
}

// startRun stores the run and executes it in the background. notes go first
// in its timeline.
func (a *App) startRun(ctx context.Context, run store.Run, snap runSnapshot, notes []string) (store.Run, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return store.Run{}, err
	}

	a.mu.Lock()
	if a.run != nil {
		a.mu.Unlock()
		return store.Run{}, ErrRunActive
	}
	runCtx, cancel := context.WithCancelCause(context.Background())
	active := &activeRun{cancel: cancel, done: make(chan struct{})}
	a.run = active
	a.mu.Unlock()

	run.Snapshot, run.State, run.StartedAt = raw, "preflight", time.Now().UTC()
	if err := a.store.CreateRun(ctx, &run); err != nil {
		cancel(nil)
		a.mu.Lock()
		a.run = nil
		a.mu.Unlock()
		return store.Run{}, err
	}
	active.id = run.ID
	if run.RerunOf != "" {
		notes = append([]string{fmt.Sprintf("Rerun of run %s: version %d of %s, exactly as it ran", run.RerunOf, run.Version, run.Experiment)}, notes...)
	}
	for _, n := range notes {
		a.store.AddRunEvent(ctx, run.ID, store.RunEvent{At: time.Now().UTC(), Type: "system", Message: n})
	}
	go a.execute(runCtx, active, run, snap)
	return run, nil
}

// StopRun aborts the run; its faults are removed.
func (a *App) StopRun(id string) error {
	a.mu.RLock()
	r := a.run
	a.mu.RUnlock()
	if r == nil || r.id != id {
		return fmt.Errorf("%w: run %s is not running", ErrNotFound, id)
	}
	r.cancel(stopCause{"stopped by you"})
	<-r.done
	return nil
}

// abortActiveRun stops the running experiment, if any, for reason.
func (a *App) abortActiveRun(reason string) {
	a.mu.RLock()
	r := a.run
	a.mu.RUnlock()
	if r != nil {
		r.cancel(stopCause{reason})
		<-r.done
	}
}

func (a *App) activeRunID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.run == nil {
		return ""
	}
	return a.run.id
}

// EndUnfinishedRuns marks runs a previous Entropy left in progress as aborted.
func (a *App) EndUnfinishedRuns(ctx context.Context) (int64, error) {
	return a.store.EndUnfinishedRuns(ctx, []string{stateDone}, "Entropy stopped during the run; its faults were cleared on the next start")
}

// ExperimentRun returns one run with its snapshot, timeline and assertion results.
func (a *App) ExperimentRun(ctx context.Context, id string) (store.Run, error) {
	r, err := a.store.Run(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: no run %s", ErrNotFound, id)
	}
	return r, err
}

// ExperimentView is an experiment with its latest run, for the UI.
type ExperimentView struct {
	store.Experiment
	LastRun *store.Run  `json:"lastRun,omitempty"`
	Recent  []store.Run `json:"recent"`  // the latest runs, newest first, for its trend
	Running bool        `json:"running"` // its run is in progress now
}

// recentRunsPerExperiment is how many runs an experiment's trend shows.
const recentRunsPerExperiment = 10

func (a *App) Experiments(ctx context.Context) ([]ExperimentView, error) {
	exps, err := a.store.Experiments(ctx)
	if err != nil {
		return nil, err
	}
	active := a.activeRunID()
	out := make([]ExperimentView, 0, len(exps))
	for _, e := range exps {
		v := ExperimentView{Experiment: e, Recent: []store.Run{}}
		if runs, err := a.store.Runs(ctx, e.Name, recentRunsPerExperiment); err == nil && len(runs) > 0 {
			v.Recent = runs
			v.LastRun = &runs[0]
			v.Running = runs[0].ID == active
		}
		out = append(out, v)
	}
	return out, nil
}

// RunExperimentYAML returns the experiment a run ran as YAML, as one item to
// paste under `experiments:` in entropy.yaml.
func (a *App) RunExperimentYAML(ctx context.Context, id string) (string, error) {
	run, err := a.ExperimentRun(ctx, id)
	if err != nil {
		return "", err
	}
	var snap runSnapshot
	if err := json.Unmarshal(run.Snapshot, &snap); err != nil {
		return "", err
	}
	out, err := yaml.Marshal([]config.Experiment{snap.Experiment})
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i] // indented to sit under experiments:
	}
	return fmt.Sprintf("  # %s, version %d, as run %s ran it\n%s\n", run.Experiment, run.Version, run.ID, strings.Join(lines, "\n")), nil
}

// ExperimentRuns returns the latest runs, newest first; experiment narrows them.
func (a *App) ExperimentRuns(ctx context.Context, experiment string) ([]store.Run, error) {
	return a.store.Runs(ctx, experiment, 50)
}

func (a *App) snapshot(ctx context.Context, e config.Experiment) (runSnapshot, error) {
	snap := runSnapshot{Experiment: e, Services: map[string]store.Service{}, Dependencies: map[string]store.Dependency{}}
	deps, err := a.store.Dependencies(ctx)
	if err != nil {
		return snap, err
	}
	svcs, err := a.store.Services(ctx)
	if err != nil {
		return snap, err
	}
	for _, f := range e.Faults {
		for _, d := range deps {
			if d.Name == f.Dependency {
				snap.Dependencies[d.Name] = d
			}
		}
	}
	want := map[string]bool{}
	for _, t := range e.Triggers {
		want[t.ServiceOf()] = true
	}
	for _, as := range e.Assertions {
		if as.Service != "" {
			want[as.Service] = true
		}
	}
	for _, s := range svcs {
		if want[s.Name] {
			snap.Services[s.Name] = s
		}
	}
	return snap, nil
}

// result is one execution of a trigger.
type result struct {
	Trigger    string    `json:"trigger"`
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
	ExitCode   *int      `json:"exitCode,omitempty"`
	Output     string    `json:"output,omitempty"`
	Status     int       `json:"status,omitempty"`
	Body       string    `json:"body,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// runner holds one run's state while it executes.
type runner struct {
	a    *App
	id   string
	snap runSnapshot
	exp  config.Experiment

	mu          sync.Mutex
	results     map[string][]result // by trigger
	phaseStart  map[string]time.Time
	restoredAt  time.Time
	recovered   map[string]*time.Duration // by service: time to recover, nil if it didn't
	toxiproxyOK bool
	applied     map[string]bool // faults this run started, by dependency
	windows     []window        // when each started fault is scheduled to be on
	removed     map[string]bool // of those, the ones whose removal is confirmed (and logged)
}

type window struct{ from, to time.Time }

// faultOnAt reports whether one of the run's faults was scheduled to be on at t.
func (r *runner) faultOnAt(t time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.windows {
		if !t.Before(w.from) && t.Before(w.to) {
			return true
		}
	}
	return false
}

func (a *App) execute(ctx context.Context, active *activeRun, run store.Run, snap runSnapshot) {
	r := &runner{a: a, id: run.ID, snap: snap, exp: snap.Experiment,
		results: map[string][]result{}, phaseStart: map[string]time.Time{}, recovered: map[string]*time.Duration{}, toxiproxyOK: true,
		applied: map[string]bool{}, removed: map[string]bool{}}
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, time.Duration(r.exp.MaxDurationSec)*time.Second,
		stopCause{fmt.Sprintf("it ran longer than maxDurationSec (%d s)", r.exp.MaxDurationSec)})
	defer func() {
		cancelTimeout()
		a.mu.Lock()
		a.run = nil
		a.mu.Unlock()
		close(active.done)
	}()

	verdict, reason := r.execute(ctx)
	if cleanup := r.removeFaults(); cleanup != "" {
		verdict, reason = VerdictInconclusive, cleanup
	}
	r.event("system", "Run ended: "+verdict+" — "+reason, nil)
	bg := context.Background()
	if err := a.store.SetRunState(bg, r.id, stateDone, verdict, reason, time.Now()); err != nil {
		a.log.Error("saving the run's verdict", "run", r.id, "err", err)
	}
	a.log.Info("experiment run ended", "experiment", r.exp.Name, "run", r.id, "verdict", verdict, "reason", reason)
	a.CheckSoon()
}

// execute runs the phases and returns the verdict and why.
func (r *runner) execute(ctx context.Context) (verdict, reason string) {
	aborted := func() (string, string) { return VerdictAborted, context.Cause(ctx).Error() }

	stopWatch := r.watchToxiproxy(ctx)
	defer stopWatch()

	r.phase(ctx, "preflight")
	if why := r.preflight(ctx); why != "" {
		if ctx.Err() != nil {
			return aborted()
		}
		return VerdictInconclusive, why
	}

	r.phase(ctx, "baseline")
	r.runTriggers(ctx, "baseline")
	if ctx.Err() != nil {
		return aborted()
	}
	if failed := r.evaluate("baseline"); failed != "" {
		return VerdictInconclusive, "the steady state didn't hold before any fault: " + failed
	}

	r.phase(ctx, "during")
	faultsOn, faultsDone := r.injectFaults(ctx)
	<-faultsOn // the "during" requests must see the fault
	// They only count while the faults are on: once the faults have run their
	// course, unfinished ones are cut off and no new ones start.
	duringCtx, endDuring := context.WithCancelCause(ctx)
	if len(r.exp.Faults) == 0 {
		duringCtx = ctx // an integration test: nothing ends the phase
	}
	var faultErr error
	faultsOver := make(chan struct{})
	go func() {
		faultErr = <-faultsDone
		endDuring(stopCause{"the faults ended"})
		close(faultsOver)
	}()
	r.runTriggers(duringCtx, "during")
	<-faultsOver
	if err := faultErr; err != nil {
		if ctx.Err() != nil {
			return aborted()
		}
		return VerdictInconclusive, err.Error()
	}
	if ctx.Err() != nil {
		return aborted()
	}

	r.phase(ctx, "restoring")
	if cleanup := r.removeFaults(); cleanup != "" {
		return VerdictInconclusive, cleanup
	}
	r.mu.Lock()
	r.restoredAt = time.Now()
	r.mu.Unlock()

	r.phase(ctx, "after")
	afterCtx, cancel := context.WithTimeout(ctx, time.Duration(r.exp.RecoveryWindowSec)*time.Second)
	var wg sync.WaitGroup
	wg.Go(func() { r.runTriggers(afterCtx, "after") })
	wg.Go(func() { r.watchRecovery(afterCtx) })
	wg.Wait()
	cancel()
	if ctx.Err() != nil {
		return aborted()
	}

	r.phase(ctx, "evaluating")
	r.mu.Lock()
	tpOK := r.toxiproxyOK
	r.mu.Unlock()
	if !tpOK {
		r.evaluate("")
		return VerdictInconclusive, "Toxiproxy was unreachable during the run, which looks like an outage the experiment didn't ask for"
	}
	if failed := r.evaluate(""); failed != "" {
		return VerdictFailed, failed
	}
	if r.anyNotEvaluated() {
		return VerdictInconclusive, "some assertions couldn't be evaluated: their trigger never produced a result"
	}
	return VerdictPassed, "all assertions hold"
}

func (r *runner) phase(ctx context.Context, name string) {
	r.mu.Lock()
	r.phaseStart[name] = time.Now()
	r.mu.Unlock()
	if err := r.a.store.SetRunState(context.WithoutCancel(ctx), r.id, name, "", "", time.Time{}); err != nil {
		r.a.log.Warn("saving the run's state", "run", r.id, "err", err)
	}
	r.event("phase", name, nil)
}

func (r *runner) event(typ, message string, data any) {
	e := store.RunEvent{At: time.Now().UTC(), Type: typ, Message: message}
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			e.Data = b
		}
	}
	if err := r.a.store.AddRunEvent(context.Background(), r.id, e); err != nil {
		r.a.log.Warn("saving a run event", "run", r.id, "err", err)
	}
}

// preflight returns why the run can't start, or "".
func (r *runner) preflight(ctx context.Context) string {
	for _, f := range r.exp.Faults {
		d, ok := r.snap.Dependencies[f.Dependency]
		if !ok || d.Missing {
			return fmt.Sprintf("dependency %s is not in the config file", f.Dependency)
		}
		if _, busy := r.a.faults.Active()[f.Dependency]; busy {
			return fmt.Sprintf("%s already has a fault; stop it first", f.Dependency)
		}
		if st := r.a.lastDepStatus(f.Dependency); st.State != Reachable {
			return fmt.Sprintf("%s isn't Reachable through its proxy (%s%s)", f.Dependency, st.State, prefixed(": ", st.Detail))
		}
		r.event("system", f.Dependency+" is Reachable through its proxy", nil)
	}
	for name, s := range r.snap.Services {
		if s.Missing {
			return fmt.Sprintf("service %s is not in the config file", name)
		}
	}
	// A service that doesn't go through the proxy would make the run pass for nothing.
	for _, f := range r.exp.Faults {
		for _, svc := range r.servicesOfTriggers() {
			rc, err := r.a.VerifyRoute(ctx, f.Dependency, svc, r.verifyCommand(svc))
			switch {
			case errors.Is(err, ErrNoTrigger):
				r.event("system", fmt.Sprintf("Couldn't verify that %s reaches %s through the proxy: it has no health check or command to run", svc, f.Dependency), nil)
				continue
			case err != nil:
				return fmt.Sprintf("verifying that %s reaches %s through the proxy: %v", svc, f.Dependency, err)
			}
			r.event("system", "Route check: "+rc.Detail, rc)
			if rc.State == RouteNotSeen {
				return fmt.Sprintf("%s doesn't seem to reach %s through the proxy, so the fault would have no effect on it. %s", svc, f.Dependency, rc.Detail)
			}
		}
	}
	return ""
}

// verifyCommand picks the request the route check runs for a service: one
// the experiment itself uses, so it's known to exercise the service.
func (r *runner) verifyCommand(service string) string {
	for _, t := range r.exp.Triggers {
		if t.Command != "" && t.ServiceOf() == service {
			return t.CommandName()
		}
	}
	return "" // its health check
}

func (r *runner) servicesOfTriggers() []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range r.exp.Triggers {
		if s := t.ServiceOf(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// injectFaults starts each fault at its offset. on is closed once the first
// fault is in place (or none could be, or there are none); done reports once
// every fault's scheduled time is over (nil) or one failed to start. The
// fault engine removes each fault at that moment; removeFaults confirms it.
func (r *runner) injectFaults(ctx context.Context) (on <-chan struct{}, done <-chan error) {
	firstOn := make(chan struct{})
	var onceOn sync.Once
	signalOn := func() { onceOn.Do(func() { close(firstOn) }) }
	finished := make(chan error, 1)
	go func() {
		defer signalOn()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var first error
		for _, f := range r.exp.Faults {
			wg.Go(func() {
				select {
				case <-time.After(time.Duration(f.StartOffsetSec) * time.Second):
				case <-ctx.Done():
					return
				}
				started, err := r.a.faults.Start(ctx, f.Dependency, f.Spec)
				if err != nil {
					mu.Lock()
					if first == nil {
						first = fmt.Errorf("couldn't apply %s to %s: %w", f.Type, f.Dependency, err)
					}
					mu.Unlock()
					return
				}
				r.mu.Lock()
				r.applied[f.Dependency] = true
				r.windows = append(r.windows, window{started.StartedAt, started.EndsAt})
				r.mu.Unlock()
				r.event("fault", fmt.Sprintf("%s on %s for %d s", f.Type, f.Dependency, f.DurationSec), f)
				signalOn()
				r.a.CheckSoon()
				// Its scheduled end, not when its removal is noticed: a request
				// sent after that moment must not count as during the fault.
				select {
				case <-time.After(time.Until(started.EndsAt)):
					r.event("fault", fmt.Sprintf("%s on %s ended", f.Type, f.Dependency), nil)
				case <-ctx.Done():
				}
			})
		}
		wg.Wait()
		finished <- first
	}()
	return firstOn, finished
}

// removeFaults makes sure none of the experiment's faults is left, and
// returns a problem to report if one is.
func (r *runner) removeFaults() string {
	var stuck []string
	for _, f := range r.exp.Faults {
		if _, on := r.a.faults.Active()[f.Dependency]; !on {
			continue
		}
		if err := r.a.faults.Stop(f.Dependency); err != nil && !errors.Is(err, faults.ErrNoFault) {
			stuck = append(stuck, err.Error())
		}
	}
	deadline := time.Now().Add(restoreWait)
	for time.Now().Before(deadline) && len(stuck) > 0 {
		stuck = stuck[:0]
		for _, f := range r.exp.Faults {
			if _, on := r.a.faults.Active()[f.Dependency]; on {
				stuck = append(stuck, f.Dependency)
			}
		}
		if len(stuck) > 0 {
			time.Sleep(time.Second)
		}
	}
	if len(stuck) > 0 {
		r.event("system", "Faults still not removed: "+strings.Join(stuck, ", ")+". Press Stop all faults.", nil)
		return "needs manual cleanup: faults on " + strings.Join(stuck, ", ") + " couldn't be removed; press Stop all faults"
	}
	for _, f := range r.exp.Faults {
		r.mu.Lock()
		first := r.applied[f.Dependency] && !r.removed[f.Dependency]
		r.removed[f.Dependency] = r.removed[f.Dependency] || first
		r.mu.Unlock()
		if first {
			r.event("fault", fmt.Sprintf("%s on %s removed", f.Type, f.Dependency), nil)
		}
	}
	return ""
}

// watchToxiproxy notes, every 2 s, whether Toxiproxy still answers.
func (r *runner) watchToxiproxy(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if _, err := r.a.client().Proxies(ctx); err != nil && ctx.Err() == nil {
				r.mu.Lock()
				first := r.toxiproxyOK
				r.toxiproxyOK = false
				r.mu.Unlock()
				if first {
					r.event("system", "Toxiproxy is unreachable: "+err.Error(), nil)
				}
			}
		}
	}()
	return cancel
}

// runTriggers runs every trigger of the phase, at the same time; each one
// repeats Repeat times, EverySec apart (start to start).
func (r *runner) runTriggers(ctx context.Context, phase string) {
	var wg sync.WaitGroup
	for _, t := range r.exp.Triggers {
		if t.Phase != phase {
			continue
		}
		wg.Go(func() {
			for i := range t.Repeat {
				if ctx.Err() != nil {
					return // the phase is over: no new requests
				}
				start := time.Now()
				res := r.fire(ctx, t, i+1)
				if ctx.Err() != nil {
					// Cut off by the end of its phase: it doesn't count.
					r.event("system", fmt.Sprintf("%s #%d was cut off and doesn't count: %v", t.Name, i+1, context.Cause(ctx)), nil)
					return
				}
				if phase == "during" && len(r.exp.Faults) > 0 && !r.faultOnAt(time.Now()) {
					// It finished after the faults were over, so it says nothing about them.
					r.event("system", fmt.Sprintf("%s #%d finished after the faults ended and doesn't count (%s)", t.Name, i+1, got(res)), res)
					return
				}
				r.mu.Lock()
				r.results[t.Name] = append(r.results[t.Name], res)
				r.mu.Unlock()
				r.event("response", describe(t, res), res)
				if i+1 < t.Repeat {
					wait := time.Duration(t.EverySec)*time.Second - time.Since(start)
					select {
					case <-time.After(max(wait, 0)):
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

// fire sends one trigger: runs the command, or sends the request.
func (r *runner) fire(ctx context.Context, t config.Trigger, attempt int) (res result) {
	res = result{Trigger: t.Name, Attempt: attempt, StartedAt: time.Now().UTC()}
	defer func() { res.DurationMs = time.Since(res.StartedAt).Milliseconds() }() // res is the named result, so this sticks
	svc := r.snap.Services[t.ServiceOf()]
	if t.Command != "" {
		var cmd store.Command
		for _, c := range svc.Commands {
			if c.Name == t.CommandName() {
				cmd = c
			}
		}
		out := command.Run(ctx, cmd.Run, cmd.Cwd, time.Duration(cmd.TimeoutSec)*time.Second, CommandEnv(svc.Name, svc.BaseURL))
		code := out.ExitCode
		res.ExitCode = &code
		res.Output = tail(out.Output)
		switch {
		case out.Err != nil:
			res.Error = out.Err.Error()
		case out.TimedOut:
			res.Error = fmt.Sprintf("timed out after %d s", cmd.TimeoutSec)
		}
		return res
	}

	req := t.Request
	var body io.Reader
	switch b := req.Body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			res.Error = "encoding the body: " + err.Error()
			return res
		}
		body = bytes.NewReader(j)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, strings.TrimRight(svc.BaseURL, "/")+req.Path, body)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if _, isString := req.Body.(string); req.Body != nil && !isString {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(hr)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	res.Status, res.Body = resp.StatusCode, tail(string(b))
	return res
}

// watchRecovery checks each recovery assertion's service until its health
// check passes, or ctx (the recovery window) ends.
func (r *runner) watchRecovery(ctx context.Context) {
	var wg sync.WaitGroup
	for _, as := range r.exp.Assertions {
		if as.Type != "recovery" {
			continue
		}
		svc := r.snap.Services[as.Service]
		wg.Go(func() {
			r.mu.Lock()
			from := r.restoredAt
			r.mu.Unlock()
			for ctx.Err() == nil {
				if ok, why := r.healthy(ctx, svc); ok {
					took := time.Since(from)
					r.mu.Lock()
					r.recovered[svc.Name] = &took
					r.mu.Unlock()
					r.event("system", fmt.Sprintf("%s is healthy again, %s after the faults ended", svc.Name, took.Round(100*time.Millisecond)), nil)
					return
				} else if ctx.Err() == nil {
					r.event("system", fmt.Sprintf("%s not healthy yet: %s", svc.Name, why), nil)
				}
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
				}
			}
		})
	}
	wg.Wait()
}

func (r *runner) healthy(ctx context.Context, s store.Service) (bool, string) {
	if hc, ok := healthCommand(s); ok {
		res := command.Run(ctx, hc.Run, hc.Cwd, time.Duration(hc.TimeoutSec)*time.Second, CommandEnv(s.Name, s.BaseURL))
		st := healthStatus(hc, res)
		return st.State == Reachable, st.Detail
	}
	if err := checks.Service(ctx, s.BaseURL, s.HealthCheck); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// evaluate records the outcome of the assertions on the phase's triggers
// ("" = all assertions) and returns a description of the first failure, or "".
func (r *runner) evaluate(phase string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	triggers := map[string]config.Trigger{}
	for _, t := range r.exp.Triggers {
		triggers[t.Name] = t
	}
	var out []store.AssertionResult
	failed := ""
	for i, as := range r.exp.Assertions {
		if phase != "" && (as.Type == "recovery" || triggers[as.Trigger].Phase != phase) {
			continue
		}
		res := r.assess(i, as, triggers[as.Trigger])
		out = append(out, res)
		if res.Outcome == "fail" && failed == "" {
			failed = res.Detail
		}
	}
	if err := r.a.store.SaveAssertionResults(context.Background(), r.id, out); err != nil {
		r.a.log.Warn("saving assertion results", "run", r.id, "err", err)
	}
	return failed
}

func (r *runner) anyNotEvaluated() bool {
	run, err := r.a.store.Run(context.Background(), r.id)
	if err != nil {
		return false
	}
	for _, as := range run.Assertions {
		if as.Outcome == "not_evaluated" {
			return true
		}
	}
	return false
}

// assess judges one assertion; r.mu is held.
func (r *runner) assess(i int, as config.Assertion, t config.Trigger) store.AssertionResult {
	res := store.AssertionResult{Index: i}
	pass := func(format string, args ...any) store.AssertionResult {
		res.Outcome, res.Detail = "pass", fmt.Sprintf(format, args...)
		return res
	}
	fail := func(format string, args ...any) store.AssertionResult {
		res.Outcome, res.Detail = "fail", fmt.Sprintf(format, args...)
		return res
	}
	if as.Type == "recovery" {
		took := r.recovered[as.Service]
		within := time.Duration(as.WithinSec) * time.Second
		switch {
		case r.restoredAt.IsZero():
			res.Outcome, res.Detail = "not_evaluated", "the run ended before the faults were removed"
			return res
		case took == nil:
			return fail("%s wasn't healthy again within %d s of the faults ending", as.Service, as.WithinSec)
		case *took > within:
			return fail("%s took %s to recover, more than %d s", as.Service, took.Round(100*time.Millisecond), as.WithinSec)
		}
		return pass("%s recovered %s after the faults ended (limit %d s)", as.Service, took.Round(100*time.Millisecond), as.WithinSec)
	}

	results := r.results[as.Trigger]
	if len(results) == 0 {
		res.Outcome, res.Detail = "not_evaluated", fmt.Sprintf("trigger %s produced no result", as.Trigger)
		return res
	}
	want := describeExpect(as.Expect)
	matching := 0
	var firstMiss *result
	for k := range results {
		if matches(as.Expect, results[k]) {
			matching++
		} else if firstMiss == nil {
			firstMiss = &results[k]
		}
	}
	switch as.Type {
	case "response":
		if firstMiss != nil {
			return fail("%s #%d: expected %s, got %s", as.Trigger, firstMiss.Attempt, want, got(*firstMiss))
		}
		return pass("all %d results of %s matched %s", len(results), as.Trigger, want)
	case "count":
		if matching < as.AtLeast {
			return fail("%d of %d results of %s matched %s, expected at least %d (e.g. #%d got %s)",
				matching, len(results), as.Trigger, want, as.AtLeast, firstMiss.Attempt, got(*firstMiss))
		}
		return pass("%d of %d results of %s matched %s (at least %d)", matching, len(results), as.Trigger, want, as.AtLeast)
	default: // eventual
		start := r.phaseStart[t.Phase]
		if t.Phase == "after" && !r.restoredAt.IsZero() {
			start = r.restoredAt
		}
		for _, x := range results {
			if matches(as.Expect, x) && x.StartedAt.Sub(start) <= time.Duration(as.WithinSec)*time.Second {
				return pass("%s matched %s after %s (limit %d s)", as.Trigger, want, x.StartedAt.Sub(start).Round(100*time.Millisecond), as.WithinSec)
			}
		}
		return fail("no result of %s matched %s within %d s; the last got %s", as.Trigger, want, as.WithinSec, got(results[len(results)-1]))
	}
}

func matches(e config.Expect, x result) bool {
	if e.ExitCode != nil && (x.ExitCode == nil || *x.ExitCode != *e.ExitCode || x.Error != "") {
		return false
	}
	if e.OutputContains != "" && !strings.Contains(x.Output, e.OutputContains) {
		return false
	}
	if e.Status != 0 && x.Status != e.Status {
		return false
	}
	if e.BodyContains != "" && !strings.Contains(x.Body, e.BodyContains) {
		return false
	}
	return true
}

func describeExpect(e config.Expect) string {
	var parts []string
	if e.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit code %d", *e.ExitCode))
	}
	if e.OutputContains != "" {
		parts = append(parts, fmt.Sprintf("output containing %q", e.OutputContains))
	}
	if e.Status != 0 {
		parts = append(parts, fmt.Sprintf("status %d", e.Status))
	}
	if e.BodyContains != "" {
		parts = append(parts, fmt.Sprintf("body containing %q", e.BodyContains))
	}
	return strings.Join(parts, " and ")
}

func got(x result) string {
	switch {
	case x.Error != "":
		return x.Error
	case x.ExitCode != nil:
		return fmt.Sprintf("exit code %d%s", *x.ExitCode, prefixed(": ", lastLine(x.Output)))
	default:
		return fmt.Sprintf("status %d", x.Status)
	}
}

func describe(t config.Trigger, x result) string {
	what := t.Command
	if t.Request != nil {
		what = t.Request.Method + " " + t.ServiceOf() + t.Request.Path
	}
	return fmt.Sprintf("%s #%d (%s): %s in %d ms", t.Name, x.Attempt, what, got(x), x.DurationMs)
}

func tail(s string) string {
	if len(s) <= outputTail {
		return s
	}
	return "…" + s[len(s)-outputTail:]
}

func prefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}
