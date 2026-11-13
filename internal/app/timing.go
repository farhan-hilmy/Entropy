package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"entropy/internal/config"
	"entropy/internal/store"
)

// The timing check tells, before a run, whether an experiment's requests fit
// in its fault and recovery window, from how long they actually took:
//
//   - full: every trigger was measured in past runs of this experiment, the
//     "during" ones under the fault (failures are often much slower);
//   - partial: some were only measured outside a fault (command runs, health
//     checks, baseline or after phases); their estimates are lower bounds, so a
//     warning is certain but "fits" isn't;
//   - none: some request was never measured; only the checks config
//     validation does (e.g. maxDurationSec) apply.
//
// It warns, never blocks: timings vary.

type Timing struct {
	Measured string   `json:"measured"` // full | partial | none
	Warnings []string `json:"warnings"`
	Note     string   `json:"note,omitempty"`
	// MinMaxDurationSec is set when the whole run may not fit in
	// maxDurationSec: the smallest value that fits, offered as a one-click fix.
	MinMaxDurationSec int `json:"minMaxDurationSec,omitempty"`
}

// samples are measured durations of one request, under a fault or not.
type samples struct{ underFault, normal []time.Duration }

// timingRuns is how many recent runs of an experiment are read for durations.
const timingRuns = 10

// timingTTL is how long a computed Timing is reused before it's recomputed.
const timingTTL = 15 * time.Second

type timingCache struct {
	mu      sync.Mutex
	entries map[string]timingEntry // by experiment name
}

type timingEntry struct {
	version int
	at      time.Time
	timing  Timing
}

// requestKey identifies a trigger's request across experiments and versions:
// "service/command", or "METHOD service/path".
func requestKey(t config.Trigger) string {
	if t.Command != "" {
		return t.Command
	}
	return t.Request.Method + " " + t.ServiceOf() + t.Request.Path
}

// experimentTiming checks an experiment against the durations Entropy has:
// its past runs, the command history and the last health checks.
func (a *App) experimentTiming(ctx context.Context, e config.Experiment, svcs []store.Service) Timing {
	s := map[string]samples{}
	add := func(key string, underFault bool, d time.Duration) {
		x := s[key]
		if underFault {
			x.underFault = append(x.underFault, d)
		} else {
			x.normal = append(x.normal, d)
		}
		s[key] = x
	}

	if runs, err := a.store.Runs(ctx, e.Name, timingRuns); err == nil {
		for _, listed := range runs {
			run, err := a.store.Run(ctx, listed.ID)
			if err != nil {
				continue
			}
			var snap runSnapshot
			if json.Unmarshal(run.Snapshot, &snap) != nil {
				continue
			}
			byName := map[string]config.Trigger{}
			for _, t := range snap.Experiment.Triggers {
				byName[t.Name] = t
			}
			for _, ev := range run.Events {
				var res result
				if len(ev.Data) == 0 || json.Unmarshal(ev.Data, &res) != nil || res.Trigger == "" || res.DurationMs <= 0 {
					continue
				}
				if t, ok := byName[res.Trigger]; ok {
					add(requestKey(t), t.Phase == "during", time.Duration(res.DurationMs)*time.Millisecond)
				}
			}
		}
	}
	if recent, err := a.store.RecentCommandRuns(ctx, 5); err == nil {
		for key, runs := range recent {
			for _, r := range runs {
				if r.Error == "" && !r.TimedOut {
					add(key, false, time.Duration(r.DurationMs)*time.Millisecond)
				}
			}
		}
	}
	a.mu.RLock()
	for key, d := range a.healthTook {
		add(key, false, d)
	}
	a.mu.RUnlock()

	health := map[string]string{} // service -> the request key of its health command
	uses := map[string][]string{} // service -> the dependencies it uses
	for _, svc := range svcs {
		if hc, ok := healthCommand(svc); ok {
			health[svc.Name] = svc.Name + "/" + hc.Name
		}
		uses[svc.Name] = svc.Uses
	}
	return assessTiming(e, s, health, uses)
}

// assessTiming is the check itself. health maps a service to its health
// command's request key, for recovery assertions; uses maps a service to the
// dependencies it uses, for the preflight's route checks (nil: unknown, so
// every service counts as using every faulted dependency).
func assessTiming(e config.Experiment, s map[string]samples, health map[string]string, uses map[string][]string) Timing {
	t := Timing{Measured: "full", Warnings: []string{}}
	if uses != nil {
		for _, f := range e.Faults {
			if len(routePairs(e, f.Dependency, uses)) == 0 {
				t.Warnings = append(t.Warnings, fmt.Sprintf("no service this experiment sends to uses %s (by their uses: lists), so breaking it may test nothing, and preflight won't check that anything goes through its proxy. Add %s to the right service's uses:, or remove the fault.",
					f.Dependency, f.Dependency))
			}
		}
	}
	var unmeasured, lowerBound []string
	note := func(level string, trig config.Trigger, list *[]string) {
		*list = append(*list, fmt.Sprintf("%s (%s)", trig.Name, requestKey(trig)))
		if level == "none" || t.Measured == "full" {
			t.Measured = level
		}
	}

	// estimate is a trigger's typical duration; lower is true when it was only
	// measured outside a fault while it runs during one.
	estimate := func(trig config.Trigger) (d time.Duration, lower, ok bool) {
		x := s[requestKey(trig)]
		if trig.Phase == "during" && len(x.underFault) > 0 {
			return median(x.underFault), false, true
		}
		if len(x.normal) > 0 {
			return median(x.normal), trig.Phase == "during", true
		}
		if len(x.underFault) > 0 { // measured only under a fault: an upper bound, still useful
			return median(x.underFault), false, true
		}
		return 0, false, false
	}

	span := time.Duration(e.FaultSpanSec()) * time.Second
	window := time.Duration(e.RecoveryWindowSec) * time.Second
	atLeast := map[string]int{}
	for _, as := range e.Assertions {
		if as.Type == "count" {
			atLeast[as.Trigger] = max(atLeast[as.Trigger], as.AtLeast)
		}
	}

	for _, trig := range e.Triggers {
		d, lower, ok := estimate(trig)
		if !ok {
			note("none", trig, &unmeasured)
			continue
		}
		if lower {
			note("partial", trig, &lowerBound)
		}
		took := fmt.Sprintf("~%s", roundSec(d))
		switch {
		case lower:
			took = fmt.Sprintf("at least ~%s (measured outside a fault; usually slower during one)", roundSec(d))
		case trig.Phase == "during":
			took += " (measured during the fault)"
		}
		need := runsTake(trig, d)
		switch trig.Phase {
		case "during":
			if len(e.Faults) == 0 || need <= span {
				continue
			}
			fit := runsFit(trig, d, span)
			msg := fmt.Sprintf("%s: %d run(s) of %s need ~%s, but the faults last %s, so only %d would count",
				trig.Name, trig.Repeat, took, roundSec(need), roundSec(span), fit)
			if n := atLeast[trig.Name]; n > fit {
				msg += fmt.Sprintf(" and its count assertion needs at least %d", n)
			}
			t.Warnings = append(t.Warnings, msg+fmt.Sprintf(". Make the fault's durationSec at least ~%d or repeat %d.", int(need.Seconds())+10, max(fit, 1)))
		case "after":
			if need <= window {
				continue
			}
			fit := runsFit(trig, d, window)
			t.Warnings = append(t.Warnings, fmt.Sprintf("%s: %d run(s) of %s need ~%s, but recoveryWindowSec is %d, so the last %d would be cut off. Make recoveryWindowSec at least ~%d or repeat %d.",
				trig.Name, trig.Repeat, took, roundSec(need), e.RecoveryWindowSec, trig.Repeat-fit, int(need.Seconds())+5, max(fit, 1)))
		}
	}

	// Recovery counts when the health check passes, i.e. when it finishes.
	for _, as := range e.Assertions {
		if as.Type != "recovery" {
			continue
		}
		key, ok := health[as.Service]
		if !ok || len(s[key].normal) == 0 {
			continue
		}
		if d := median(s[key].normal); d > time.Duration(as.WithinSec)*time.Second {
			t.Warnings = append(t.Warnings, fmt.Sprintf("recovery of %s: its health command %s takes ~%s, and recovery counts when it finishes, so withinSec %d can never pass. Make withinSec at least ~%d.",
				as.Service, key, roundSec(d), as.WithinSec, int(d.Seconds())+5))
		}
	}

	if w, need := totalWarning(e, s, uses); w != "" {
		t.Warnings = append(t.Warnings, w)
		t.MinMaxDurationSec = need
	}

	switch {
	case len(unmeasured) > 0:
		t.Note = "Not measured yet: " + strings.Join(unmeasured, ", ") + ". Run the experiment once, or run the command from the service page, and Entropy will check whether its requests fit."
	case len(lowerBound) > 0:
		t.Note = "Not measured during a fault yet: " + strings.Join(lowerBound, ", ") + ". The check uses their normal times, which are lower bounds."
	}
	return t
}

// totalWarning checks the whole run against maxDurationSec: the preflight's
// route checks, the baseline, the faults and the recovery window. Being
// aborted wastes a whole run, so it uses the slowest times measured, not the
// typical ones, and no made-up margin; it says how many measurements they
// come from. Without measured preflight or baseline requests it says nothing:
// config validation already rejects faults + window alone being too long.
func totalWarning(e config.Experiment, s map[string]samples, uses map[string][]string) (warning string, need int) {
	fewest := 0 // the fewest measurements behind any of the estimates
	slowest := func(key string) (time.Duration, bool) {
		ds := s[key].normal // preflight and baseline run without a fault
		if len(ds) == 0 {
			ds = s[key].underFault
		}
		if len(ds) == 0 {
			return 0, false
		}
		if fewest == 0 || len(ds) < fewest {
			fewest = len(ds)
		}
		return slices.Max(ds), true
	}

	// For each faulted dependency and each service that uses it, the route
	// check runs the service's probe trigger twice, the second time with
	// routeProbeLatency added (see runner.preflight).
	var preflight time.Duration
	for _, f := range e.Faults {
		for _, svc := range routePairs(e, f.Dependency, uses) {
			probe, _ := e.ProbeTrigger(svc)
			if d, ok := slowest(requestKey(probe)); ok {
				preflight += 2*d + routeProbeLatency
			}
		}
	}
	var baseline time.Duration
	for _, trig := range e.Triggers {
		if trig.Phase != "baseline" {
			continue
		}
		if d, ok := slowest(requestKey(trig)); ok {
			baseline = max(baseline, runsTake(trig, d))
		}
	}
	if preflight+baseline == 0 {
		return "", 0
	}
	total := preflight + baseline + time.Duration(e.FaultsEndSec()+e.RecoveryWindowSec)*time.Second
	if total < time.Duration(e.MaxDurationSec)*time.Second {
		return "", 0
	}
	// The smallest whole number of seconds the total stays under.
	need = int(math.Floor(total.Seconds())) + 1
	return fmt.Sprintf("with the slowest times measured (each from at least %d measurement(s)), the preflight (~%s), baseline (~%s), faults (%d s) and recovery window (%d s) take ~%s, more than maxDurationSec %d, so the run could be aborted. Make maxDurationSec at least %d.",
		fewest, roundSec(preflight), roundSec(baseline), e.FaultsEndSec(), e.RecoveryWindowSec, roundSec(total), e.MaxDurationSec, need), need
}

// routePairs is the experiment's trigger services that the preflight
// route-checks against the dependency: those that use it, as runner.preflight
// does; with uses unknown (nil), all of them.
func routePairs(e config.Experiment, dependency string, uses map[string][]string) []string {
	var out []string
	for _, t := range e.Triggers {
		s := t.ServiceOf()
		if slices.Contains(out, s) || (uses != nil && !slices.Contains(uses[s], dependency)) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// runsTake is how long a trigger's repeats take, back to back or EverySec apart.
func runsTake(t config.Trigger, d time.Duration) time.Duration {
	step := max(d, time.Duration(t.EverySec)*time.Second)
	return time.Duration(t.Repeat-1)*step + d
}

// runsFit is how many of a trigger's repeats finish within window.
func runsFit(t config.Trigger, d, window time.Duration) int {
	if d > window {
		return 0
	}
	step := max(d, time.Duration(t.EverySec)*time.Second)
	return min(t.Repeat, int((window-d)/step)+1)
}

func median(ds []time.Duration) time.Duration {
	sorted := slices.Clone(ds)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

func roundSec(d time.Duration) string {
	if d < 10*time.Second {
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
	return fmt.Sprintf("%d s", int(d.Round(time.Second).Seconds()))
}

// timingFor returns an experiment's Timing, recomputed when it's older than
// timingTTL or the experiment changed.
func (a *App) timingFor(ctx context.Context, e store.Experiment, svcs []store.Service) Timing {
	a.timings.mu.Lock()
	cached, ok := a.timings.entries[e.Name]
	a.timings.mu.Unlock()
	if ok && cached.version == e.Version && time.Since(cached.at) < timingTTL {
		return cached.timing
	}
	t := a.experimentTiming(ctx, e.Definition, svcs)
	a.timings.mu.Lock()
	if a.timings.entries == nil {
		a.timings.entries = map[string]timingEntry{}
	}
	a.timings.entries[e.Name] = timingEntry{version: e.Version, at: time.Now(), timing: t}
	a.timings.mu.Unlock()
	return t
}

// timingWarnings checks every experiment's timing for Sync's result, each
// warning prefixed with its experiment.
func (a *App) timingWarnings(ctx context.Context, exps []config.Experiment) []string {
	if len(exps) == 0 {
		return nil
	}
	a.forgetTimings() // the file may have changed the experiments
	svcs, err := a.store.Services(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range exps {
		for _, w := range a.experimentTiming(ctx, e, svcs).Warnings {
			out = append(out, "experiment "+e.Name+": "+w)
		}
	}
	return out
}

// forgetTimings makes the next look recompute every experiment's Timing,
// e.g. after a run produced new durations.
func (a *App) forgetTimings() {
	a.timings.mu.Lock()
	a.timings.entries = nil
	a.timings.mu.Unlock()
}
