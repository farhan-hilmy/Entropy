package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"entropy/internal/checks"
	"entropy/internal/command"
	"entropy/internal/store"
	"entropy/internal/toxiproxy"
)

// A route check finds out whether a service really reaches a dependency
// through its proxy, so faults on the proxy reach the service. Entropy runs one
// of the service's requests twice: normally, then with extra latency on the
// proxy. If the second run is clearly slower, or fails, the request went
// through the proxy. Toxics apply to open connections too, so this works with
// connection pools; Toxiproxy's traffic counters don't (they count a
// connection only when it closes, and can't tell services apart).

type RouteState string

const (
	RouteVerified RouteState = "verified" // the request slowed down or failed with latency on the proxy
	RouteNotSeen  RouteState = "not_seen" // it ran just as fast: it didn't go through the proxy
	RouteUnknown  RouteState = "unknown"  // couldn't tell, e.g. the request fails anyway
)

// routeProbeLatency is the latency added for the second run, and a request
// counts as slowed down from half of it. Tests shorten it.
var routeProbeLatency = 1500 * time.Millisecond

const probeToxic = "entropy_verify"

var (
	ErrVerifying   = errors.New("this dependency is already being verified")
	ErrFaultActive = errors.New("this dependency has a fault, which would distort the check; stop the fault first")
	ErrNoTrigger   = errors.New("no request to run")
)

type RouteCheck struct {
	Service    string     `json:"service"`
	Dependency string     `json:"dependency"`
	State      RouteState `json:"state"`
	Detail     string     `json:"detail"`
	Trigger    string     `json:"trigger"` // what Entropy ran, e.g. "command access-token"
	NormalMs   int64      `json:"normalMs"`
	SlowedMs   int64      `json:"slowedMs"` // the same request with latency on the proxy
	LatencyMs  int64      `json:"latencyMs"`
	CheckedAt  time.Time  `json:"checkedAt"`
}

// VerifyRoute runs one of the service's requests with and without latency on
// the dependency's proxy and reports whether it went through the proxy.
// command names one of the service's commands; empty means its health check.
func (a *App) VerifyRoute(ctx context.Context, dependency, service, command string) (RouteCheck, error) {
	d, s, err := a.findRoute(ctx, dependency, service)
	if err != nil {
		return RouteCheck{}, err
	}
	if _, ok := a.faults.Active()[dependency]; ok {
		return RouteCheck{}, ErrFaultActive
	}
	trigger, run, err := routeTrigger(s, command)
	if err != nil {
		return RouteCheck{}, err
	}
	release, ok := a.pauseChecks(dependency)
	if !ok {
		return RouteCheck{}, ErrVerifying
	}
	defer release()

	latency := routeProbeLatency
	rc := RouteCheck{Service: s.Name, Dependency: d.Name, State: RouteUnknown, Trigger: trigger, LatencyMs: latency.Milliseconds()}
	done := func() (RouteCheck, error) {
		rc.CheckedAt = time.Now().UTC()
		a.saveRoute(rc)
		return rc, nil
	}

	normal, failure, err := timed(ctx, a, run)
	if err != nil {
		return RouteCheck{}, err
	}
	rc.NormalMs = normal.Milliseconds()
	if failure != "" {
		rc.Detail = fmt.Sprintf("Can't tell: %s fails even without latency on the proxy (%s). Fix it or pick another request.", trigger, failure)
		return done()
	}

	tp := a.client()
	toxic := toxiproxy.Toxic{Name: probeToxic, Type: "latency", Stream: "downstream", Toxicity: 1,
		Attributes: map[string]any{"latency": latency.Milliseconds(), "jitter": 0}}
	if err := tp.AddToxic(ctx, d.Name, toxic); err != nil {
		rc.Detail = "Can't tell: adding latency to the proxy failed: " + err.Error()
		return done()
	}
	slowed, failure, err := timed(ctx, a, run)
	// Always remove the latency, even if the request couldn't start or ctx ended.
	if rmErr := tp.RemoveToxic(context.WithoutCancel(ctx), d.Name, probeToxic); rmErr != nil && !toxiproxy.IsNotFound(rmErr) {
		a.log.Warn("could not remove the route check's latency; Stop all faults removes it", "dependency", d.Name, "err", rmErr)
	}
	if err != nil {
		return RouteCheck{}, err
	}
	rc.SlowedMs = slowed.Milliseconds()

	switch {
	case failure != "":
		rc.State = RouteVerified
		rc.Detail = fmt.Sprintf("%s failed with %s of latency on the %s proxy, so it goes through the proxy (%s).", trigger, latency, d.Name, failure)
	case slowed-normal >= latency/2:
		rc.State = RouteVerified
		rc.Detail = fmt.Sprintf("%s took %s instead of %s with %s of latency on the %s proxy, so it goes through the proxy.",
			trigger, slowed.Round(time.Millisecond), normal.Round(time.Millisecond), latency, d.Name)
	default:
		rc.State = RouteNotSeen
		rc.Detail = fmt.Sprintf("%s took %s, about as long as without latency (%s), with %s of latency on the %s proxy. Either %s connects straight to %s instead of %s, or this request doesn't use %s: try another one.",
			trigger, slowed.Round(time.Millisecond), normal.Round(time.Millisecond), latency, d.Name, s.Name, d.Upstream, d.Address, d.Name)
	}
	return done()
}

func timed(ctx context.Context, a *App, run func(context.Context, *App) (string, error)) (time.Duration, string, error) {
	start := time.Now()
	failure, err := run(ctx, a)
	return time.Since(start), failure, err
}

func (a *App) findRoute(ctx context.Context, dependency, service string) (store.Dependency, store.Service, error) {
	deps, err := a.store.Dependencies(ctx)
	if err != nil {
		return store.Dependency{}, store.Service{}, err
	}
	svcs, err := a.store.Services(ctx)
	if err != nil {
		return store.Dependency{}, store.Service{}, err
	}
	var d store.Dependency
	for _, x := range deps {
		if x.Name == dependency && !x.Missing {
			d = x
		}
	}
	if d.Name == "" {
		return d, store.Service{}, fmt.Errorf("%w: no dependency %s in the config file", ErrNotFound, dependency)
	}
	for _, x := range svcs {
		if x.Name == service && !x.Missing {
			return d, x, nil
		}
	}
	return d, store.Service{}, fmt.Errorf("%w: no service %s in the config file", ErrNotFound, service)
}

// routeTrigger picks the request to run: the named command, or else the
// service's health command or health URL. run returns why the request failed,
// or "" if it passed; its error is for not being able to start it at all.
func routeTrigger(s store.Service, name string) (trigger string, run func(context.Context, *App) (string, error), err error) {
	if name == "" {
		if hc, ok := healthCommand(s); ok {
			name = hc.Name
		}
	}
	if name != "" {
		for _, c := range s.Commands {
			if c.Name == name {
				return "command " + c.Name, func(ctx context.Context, a *App) (string, error) {
					release, ok := a.claim(s.Name, c.Name)
					if !ok {
						return "", ErrAlreadyRunning
					}
					defer release()
					res := command.Run(ctx, c.Run, c.Cwd, time.Duration(c.TimeoutSec)*time.Second, CommandEnv(s.Name, s.BaseURL))
					if st := healthStatus(c, res); st.State != Reachable {
						return st.Detail, nil
					}
					return "", nil
				}, nil
			}
		}
		return "", nil, fmt.Errorf("%w: no command %s/%s in the config file", ErrNotFound, s.Name, name)
	}
	if s.HealthCheck == "" {
		// Only a port check: it never reaches the dependency, so it proves nothing.
		return "", nil, fmt.Errorf("%w: %s has no health check URL or health command, so pick one of its commands", ErrNoTrigger, s.Name)
	}
	return "GET " + s.HealthCheck, func(ctx context.Context, _ *App) (string, error) {
		if err := checks.Service(ctx, s.BaseURL, s.HealthCheck); err != nil {
			return err.Error(), nil
		}
		return "", nil
	}, nil
}

// pauseChecks stops Entropy's own checks of the dependency until release, so
// the added latency doesn't show as a failed check. It waits for checks
// already in flight. ok is false if the dependency is already being verified.
func (a *App) pauseChecks(dependency string) (release func(), ok bool) {
	a.mu.Lock()
	if a.verifying[dependency] {
		a.mu.Unlock()
		return nil, false
	}
	if a.verifying == nil {
		a.verifying = map[string]bool{}
	}
	a.verifying[dependency] = true
	a.mu.Unlock()
	a.depChecks.Lock() // checks started before now hold a read lock
	a.depChecks.Unlock()
	return func() {
		a.mu.Lock()
		delete(a.verifying, dependency)
		a.mu.Unlock()
	}, true
}

func (a *App) isVerifying(dependency string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.verifying[dependency]
}

func (a *App) anyVerifying() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.verifying) > 0
}

func (a *App) saveRoute(rc RouteCheck) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.routes == nil {
		a.routes = map[string]RouteCheck{}
	}
	a.routes[rc.Service+"/"+rc.Dependency] = rc
}

// routesByDependency returns the last route checks, by dependency, then by service.
func (a *App) routesByDependency() map[string]map[string]RouteCheck {
	a.mu.RLock()
	all := maps.Clone(a.routes)
	a.mu.RUnlock()
	out := map[string]map[string]RouteCheck{}
	for _, rc := range all {
		if out[rc.Dependency] == nil {
			out[rc.Dependency] = map[string]RouteCheck{}
		}
		out[rc.Dependency][rc.Service] = rc
	}
	return out
}
