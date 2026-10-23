// Package faults applies faults to dependencies through Toxiproxy and makes
// sure each one is removed when its time is up. Toxiproxy faults never expire
// on their own, so every fault here has a timer, and a removal that fails is
// retried until it succeeds.
package faults

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"entropy/internal/toxiproxy"
)

// Types are the fault types, in the order the UI offers them.
var Types = []string{"latency", "drop", "timeout", "outage", "flap"}

const (
	MaxDurationSec = 3600
	// A failed removal (e.g. while Toxiproxy is unreachable) is retried until
	// it succeeds: first after RetryInterval, then twice as long each time, up
	// to MaxRetryInterval.
	RetryInterval    = 2 * time.Second
	MaxRetryInterval = 30 * time.Second
	// retryLogEvery limits the warnings about a removal that keeps failing.
	retryLogEvery = time.Minute
	// stopWait is how long Stop waits for a removal before answering anyway;
	// the removal keeps being retried after that.
	stopWait = 5 * time.Second
)

var (
	ErrBusy    = errors.New("this dependency already has a fault; stop it first")
	ErrInvalid = errors.New("invalid fault")
	ErrNoFault = errors.New("this dependency has no fault")
)

// Spec says what to do to a dependency and for how long. Fields that don't
// apply to the type must be zero.
type Spec struct {
	Type        string `json:"type"`
	DurationSec int    `json:"durationSec"`
	LatencyMs   int    `json:"latencyMs,omitempty"` // latency: added to every response (default 1000)
	JitterMs    int    `json:"jitterMs,omitempty"`  // latency: random ± on top
	TimeoutMs   int    `json:"timeoutMs,omitempty"` // timeout: close the connection after this; 0 holds it until the fault ends
	DownSec     int    `json:"downSec,omitempty"`   // flap: time disabled per cycle (default 5)
	UpSec       int    `json:"upSec,omitempty"`     // flap: time enabled per cycle (default 5)
}

// WithDefaults fills in defaults and checks the spec.
func (s Spec) WithDefaults() (Spec, error) {
	bad := func(format string, args ...any) (Spec, error) {
		return s, fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	if !slices.Contains(Types, s.Type) {
		return bad("type %q: must be one of %v", s.Type, Types)
	}
	if s.DurationSec < 1 || s.DurationSec > MaxDurationSec {
		return bad("durationSec must be between 1 and %d", MaxDurationSec)
	}
	if s.LatencyMs < 0 || s.JitterMs < 0 || s.TimeoutMs < 0 || s.DownSec < 0 || s.UpSec < 0 {
		return bad("values can't be negative")
	}
	if s.Type != "latency" && (s.LatencyMs != 0 || s.JitterMs != 0) {
		return bad("latencyMs and jitterMs only apply to latency")
	}
	if s.Type != "timeout" && s.TimeoutMs != 0 {
		return bad("timeoutMs only applies to timeout")
	}
	if s.Type != "flap" && (s.DownSec != 0 || s.UpSec != 0) {
		return bad("downSec and upSec only apply to flap")
	}
	switch s.Type {
	case "latency":
		if s.LatencyMs == 0 {
			s.LatencyMs = 1000
		}
	case "flap":
		if s.DownSec == 0 {
			s.DownSec = 5
		}
		if s.UpSec == 0 {
			s.UpSec = 5
		}
	}
	return s, nil
}

// toxic is the Toxiproxy toxic for the types that use one; ok is false for
// outage and flap, which disable the proxy instead.
func (s Spec) toxic() (t toxiproxy.Toxic, ok bool) {
	t = toxiproxy.Toxic{Name: "entropy_" + s.Type, Stream: "downstream", Toxicity: 1}
	switch s.Type {
	case "latency":
		t.Type, t.Attributes = "latency", map[string]any{"latency": s.LatencyMs, "jitter": s.JitterMs}
	case "drop":
		t.Type, t.Attributes = "reset_peer", map[string]any{"timeout": 0}
	case "timeout":
		t.Type, t.Attributes = "timeout", map[string]any{"timeout": s.TimeoutMs}
	default:
		return t, false
	}
	return t, true
}

// Fault is a fault Entropy applied and hasn't removed yet.
type Fault struct {
	Dependency string `json:"dependency"`
	Spec
	StartedAt time.Time `json:"startedAt"`
	EndsAt    time.Time `json:"endsAt"`
	// Removing: the fault's time is up or it was stopped, and Entropy is
	// removing it. Error is why the last attempt failed.
	Removing bool   `json:"removing"`
	Error    string `json:"error,omitempty"`
}

type running struct {
	fault   Fault
	end     chan struct{} // closed to end the fault before its time
	endOnce sync.Once
	done    chan struct{} // closed once the fault is removed

	// op serializes this fault's changes in Toxiproxy: applying, flap toggles
	// and Reapply. Removing is set under it before the removal starts, so
	// Reapply can never put back a fault that is being removed.
	op   sync.Mutex
	down bool // outage and flap: the proxy should be disabled now
}

// inPlace reports whether proxy p has this fault as it should be now.
func (r *running) inPlace(p toxiproxy.Proxy) bool {
	if t, ok := r.fault.toxic(); ok {
		return slices.ContainsFunc(p.Toxics, func(x toxiproxy.Toxic) bool { return x.Name == t.Name })
	}
	return p.Enabled == !r.down
}

func (r *running) stop() { r.endOnce.Do(func() { close(r.end) }) }

type Engine struct {
	client func() *toxiproxy.Client // the current client: Sync may replace it
	log    *slog.Logger
	// second is how long a "Sec" field lasts; tests shorten it.
	second   time.Duration
	retry    time.Duration
	maxRetry time.Duration
	// OnRemoved, if set, is called after a fault is removed, e.g. to recheck statuses.
	OnRemoved func(dependency string)

	mu     sync.Mutex
	active map[string]*running // by dependency
}

func New(client func() *toxiproxy.Client, log *slog.Logger) *Engine {
	return &Engine{client: client, log: log, second: time.Second, retry: RetryInterval, maxRetry: MaxRetryInterval, active: map[string]*running{}}
}

// Start applies a fault to a dependency's proxy and starts its timer. A
// dependency has at most one fault at a time.
func (e *Engine) Start(ctx context.Context, dependency string, spec Spec) (Fault, error) {
	spec, err := spec.WithDefaults()
	if err != nil {
		return Fault{}, err
	}
	now := time.Now().UTC()
	r := &running{
		fault: Fault{Dependency: dependency, Spec: spec, StartedAt: now, EndsAt: now.Add(time.Duration(spec.DurationSec) * e.second)},
		end:   make(chan struct{}),
		done:  make(chan struct{}),
		down:  spec.Type == "outage" || spec.Type == "flap", // a flap starts down
	}
	r.op.Lock() // Reapply waits until the fault is applied
	defer r.op.Unlock()
	e.mu.Lock()
	if _, busy := e.active[dependency]; busy {
		e.mu.Unlock()
		return Fault{}, ErrBusy
	}
	e.active[dependency] = r // claimed before applying, so two starts can't race
	e.mu.Unlock()

	if err := e.apply(ctx, r); err != nil {
		e.mu.Lock()
		r.fault.Removing = true
		e.mu.Unlock()
		// Undo whatever part got through, e.g. if the request timed out after Toxiproxy acted.
		if rmErr := e.remove(context.WithoutCancel(ctx), r.fault); rmErr != nil {
			e.log.Warn("could not undo a fault that failed to start", "dependency", dependency, "err", rmErr)
		}
		e.mu.Lock()
		delete(e.active, dependency)
		e.mu.Unlock()
		return Fault{}, fmt.Errorf("applying %s to %s: %w", spec.Type, dependency, err)
	}
	e.log.Info("fault started", "dependency", dependency, "type", spec.Type, "durationSec", spec.DurationSec)
	f := r.fault // copied before run can change it
	go e.run(r)
	return f, nil
}

// apply puts the fault in place as it should be now. Applying it twice is
// harmless, so Reapply can use it on a stale view of Toxiproxy. Call with r.op held.
func (e *Engine) apply(ctx context.Context, r *running) error {
	if t, ok := r.fault.toxic(); ok {
		if err := e.client().AddToxic(ctx, r.fault.Dependency, t); !toxiproxy.IsConflict(err) {
			return err
		}
		return nil // already there
	}
	return e.client().SetEnabled(ctx, r.fault.Dependency, !r.down)
}

// Reapply puts back faults that Toxiproxy lost while they were active. It
// keeps proxies and toxics in memory only, so a restart drops them (and the
// check round recreates the proxies clean); a reset from outside Entropy does
// the same. proxies is Toxiproxy's state as the check round read it; faults
// whose proxy isn't in it are skipped.
func (e *Engine) Reapply(ctx context.Context, proxies map[string]toxiproxy.Proxy) {
	e.mu.Lock()
	all := make([]*running, 0, len(e.active))
	for _, r := range e.active {
		all = append(all, r)
	}
	e.mu.Unlock()
	for _, r := range all {
		if p, ok := proxies[r.fault.Dependency]; ok && !r.inPlace(p) {
			e.reapply(ctx, r)
		}
	}
}

// reapply checks r against a fresh read of its proxy, so a read taken before
// the fault started or before a flap toggled doesn't count as lost, and puts
// it back if it's missing.
func (e *Engine) reapply(ctx context.Context, r *running) {
	r.op.Lock()
	defer r.op.Unlock()
	e.mu.Lock()
	removing := r.fault.Removing
	e.mu.Unlock()
	if removing {
		return
	}
	dep, typ := r.fault.Dependency, r.fault.Type
	p, err := e.client().Proxy(ctx, dep)
	if err != nil || r.inPlace(p) {
		return // not found: the check round recreates the proxy, and the next round re-applies
	}
	if err := e.apply(ctx, r); err != nil {
		e.log.Warn("could not re-apply a fault Toxiproxy lost", "dependency", dep, "type", typ, "err", err)
		return
	}
	e.log.Info("re-applied a fault Toxiproxy lost (restarted or reset?)", "dependency", dep, "type", typ)
}

// remove undoes a fault. Something already gone counts as removed: Toxiproxy
// may have restarted, which drops every proxy and toxic.
func (e *Engine) remove(ctx context.Context, f Fault) error {
	var err error
	if t, ok := f.toxic(); ok {
		err = e.client().RemoveToxic(ctx, f.Dependency, t.Name)
	} else {
		err = e.client().SetEnabled(ctx, f.Dependency, true)
	}
	if toxiproxy.IsNotFound(err) {
		return nil
	}
	return err
}

// run holds the fault until its time is up or it's stopped, then removes it,
// retrying with backoff until that succeeds. Giving up would leave the fault
// in Toxiproxy with nobody tracking it.
func (e *Engine) run(r *running) {
	defer close(r.done)
	e.hold(r)

	r.op.Lock()
	e.mu.Lock()
	r.fault.Removing = true
	f := r.fault
	e.mu.Unlock()
	r.op.Unlock()
	wait, attempts := e.retry, 0
	var logged time.Time
	for {
		attempts++
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := e.remove(ctx, f)
		cancel()
		if err == nil {
			break
		}
		if time.Since(logged) >= retryLogEvery {
			e.log.Warn("could not remove fault; retrying", "dependency", f.Dependency, "type", f.Type, "attempts", attempts, "err", err)
			logged = time.Now()
		}
		e.mu.Lock()
		r.fault.Error = err.Error()
		e.mu.Unlock()
		time.Sleep(wait)
		wait = min(2*wait, e.maxRetry)
	}
	e.mu.Lock()
	delete(e.active, f.Dependency)
	e.mu.Unlock()
	e.log.Info("fault removed", "dependency", f.Dependency, "type", f.Type, "attempts", attempts)
	if e.OnRemoved != nil {
		e.OnRemoved(f.Dependency)
	}
}

// hold waits until the fault's time is up or it's stopped. A flap toggles the
// proxy meanwhile, starting down; a failed toggle is logged and the next one
// tries again.
func (e *Engine) hold(r *running) {
	f := r.fault
	deadline := time.NewTimer(time.Until(f.EndsAt))
	defer deadline.Stop()
	if f.Type != "flap" {
		select {
		case <-deadline.C:
		case <-r.end:
		}
		return
	}
	for {
		phase := time.Duration(f.UpSec) * e.second
		if r.down { // only this goroutine changes r.down, so reading it here needs no lock
			phase = time.Duration(f.DownSec) * e.second
		}
		toggle := time.NewTimer(phase)
		select {
		case <-deadline.C:
			toggle.Stop()
			return
		case <-r.end:
			toggle.Stop()
			return
		case <-toggle.C:
		}
		r.op.Lock()
		r.down = !r.down
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := e.apply(ctx, r); err != nil {
			e.log.Warn("flap could not toggle the proxy", "dependency", f.Dependency, "err", err)
		}
		cancel()
		r.op.Unlock()
	}
}

// Stop ends a dependency's fault now and waits briefly for its removal.
// If the removal is still failing after that it returns the error; Entropy
// keeps retrying in the background.
func (e *Engine) Stop(dependency string) error {
	e.mu.Lock()
	r, ok := e.active[dependency]
	e.mu.Unlock()
	if !ok {
		return ErrNoFault
	}
	r.stop()
	return e.wait(r, time.After(stopWait))
}

// StopAll ends every fault and waits briefly for their removal.
func (e *Engine) StopAll() error {
	e.mu.Lock()
	all := make([]*running, 0, len(e.active))
	for _, r := range e.active {
		all = append(all, r)
	}
	e.mu.Unlock()
	timeout := time.After(stopWait)
	var errs []error
	for _, r := range all {
		r.stop()
	}
	for _, r := range all {
		errs = append(errs, e.wait(r, timeout))
	}
	return errors.Join(errs...)
}

func (e *Engine) wait(r *running, timeout <-chan time.Time) error {
	select {
	case <-r.done:
		return nil
	case <-timeout:
		e.mu.Lock()
		msg := r.fault.Error
		e.mu.Unlock()
		if msg == "" {
			msg = "no answer from Toxiproxy yet"
		}
		return fmt.Errorf("%s fault on %s is not removed yet (%s); Entropy keeps trying", r.fault.Type, r.fault.Dependency, msg)
	}
}

// Active returns the faults Entropy hasn't removed yet, by dependency.
func (e *Engine) Active() map[string]Fault {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]Fault, len(e.active))
	for dep, r := range e.active {
		out[dep] = r.fault
	}
	return out
}
