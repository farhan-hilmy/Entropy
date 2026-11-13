package app

import (
	"strings"
	"testing"
	"time"

	"entropy/internal/config"
	"entropy/internal/faults"
)

// oauthOutage is the real oauth experiment as it was first written: a 30 s
// outage with 4 Bruno runs during it.
func oauthOutage() config.Experiment {
	cmd := func(name, phase string, repeat, every int) config.Trigger {
		return config.Trigger{Name: name, Phase: phase, Command: "oauth/oauth", Repeat: repeat, EverySec: every}
	}
	return config.Experiment{
		Name: "redis-outage-oauth", MaxDurationSec: 180, RecoveryWindowSec: 30,
		Faults:   []config.ExperimentFault{{Dependency: "redis", Spec: faults.Spec{Type: "outage", DurationSec: 30}}},
		Triggers: []config.Trigger{cmd("token-before", "baseline", 1, 0), cmd("token-during", "during", 4, 6), cmd("token-after", "after", 2, 6)},
		Assertions: []config.Assertion{
			{Type: "count", Trigger: "token-during", Expect: config.Expect{OutputContains: "(500"}, AtLeast: 3},
			{Type: "recovery", Service: "oauth", WithinSec: 20},
		},
	}
}

var oauthHealth = map[string]string{"oauth": "oauth/oauth"} // its health command is the same Bruno request
var oauthUses = map[string][]string{"oauth": {"redis"}}

func secs(ss ...float64) []time.Duration {
	out := make([]time.Duration, len(ss))
	for i, s := range ss {
		out[i] = time.Duration(s * float64(time.Second))
	}
	return out
}

func TestTimingWarnsWhenNoServiceUsesTheFaultedDependency(t *testing.T) {
	got := assessTiming(oauthOutage(), map[string]samples{}, oauthHealth, map[string][]string{"oauth": {"mongodb"}})
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "no service this experiment sends to uses redis") {
		t.Errorf("warnings = %q; want the one about breaking redis testing nothing", got.Warnings)
	}
}

func TestTimingNotMeasuredYet(t *testing.T) {
	got := assessTiming(oauthOutage(), map[string]samples{}, oauthHealth, oauthUses)
	if got.Measured != "none" || len(got.Warnings) != 0 || !strings.Contains(got.Note, "Not measured yet: token-before (oauth/oauth)") {
		t.Errorf("timing = %+v; want none, no warnings (no guessing), and a note saying what to run", got)
	}
}

func TestTimingLowerBoundFromNormalRuns(t *testing.T) {
	// Only health checks so far: ~8 s each, never under a fault.
	got := assessTiming(oauthOutage(), map[string]samples{"oauth/oauth": {normal: secs(7.5, 8, 8.5)}}, oauthHealth, oauthUses)
	if got.Measured != "partial" || !strings.Contains(got.Note, "Not measured during a fault yet: token-during") {
		t.Errorf("timing = %+v; want partial, with a note about the lower bound", got)
	}
	// 4 runs of at least 8 s, 6 s apart (so 8 s apart): 32 s > 30 s.
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "token-during: 4 run(s) of at least ~8.0 s") ||
		!strings.Contains(got.Warnings[0], "so only 3 would count") {
		t.Errorf("warnings = %q; want the certain one about token-during", got.Warnings)
	}
}

func TestTimingFullFromARunUnderTheFault(t *testing.T) {
	// The real first run: each request took ~23 s during the outage.
	s := map[string]samples{"oauth/oauth": {normal: secs(8, 9.5, 8), underFault: secs(23, 22.5, 23.5)}}
	got := assessTiming(oauthOutage(), s, oauthHealth, oauthUses)
	if got.Measured != "full" || got.Note != "" {
		t.Errorf("timing = %+v; want full", got)
	}
	want := "token-during: 4 run(s) of ~23 s (measured during the fault) need ~92 s, but the faults last 30 s, so only 1 would count and its count assertion needs at least 3"
	if len(got.Warnings) == 0 || !strings.HasPrefix(got.Warnings[0], want) {
		t.Errorf("warnings = %q;\nwant first %q", got.Warnings, want)
	}

	// The fixed experiment: 100 s outage, 3 runs back to back: no warning.
	fixed := oauthOutage()
	fixed.Faults[0].DurationSec, fixed.MaxDurationSec = 100, 300
	fixed.Triggers[1].Repeat, fixed.Triggers[1].EverySec = 3, 0
	fixed.Assertions[0].AtLeast = 3
	if got := assessTiming(fixed, s, oauthHealth, oauthUses); len(got.Warnings) != 0 {
		t.Errorf("the fixed experiment fits, but got %q", got.Warnings)
	}
}

func TestTimingAfterRecoveryAndTotal(t *testing.T) {
	e := oauthOutage()
	e.Faults[0].DurationSec = 100
	e.Triggers[1].Repeat, e.Triggers[1].EverySec = 1, 0
	e.Assertions[0].AtLeast = 1
	e.Triggers[2].Repeat = 4      // 4 × 9 s (typical) in a 30 s window
	e.Assertions[1].WithinSec = 5 // shorter than one health check
	e.MaxDurationSec = 135
	// Three runs of the Bruno request: typical (median) 9 s, slowest 12 s.
	s := map[string]samples{"oauth/oauth": {normal: secs(8, 9, 12), underFault: secs(23)}}
	got := strings.Join(assessTiming(e, s, oauthHealth, oauthUses).Warnings, "\n")
	for _, want := range []string{
		// Whether requests fit: the typical time.
		"token-after: 4 run(s) of ~9.0 s need ~36 s, but recoveryWindowSec is 30, so the last 1 would be cut off",
		"recovery of oauth: its health command oauth/oauth takes ~9.0 s, and recovery counts when it finishes, so withinSec 5 can never pass",
		// The cap: the slowest time. Preflight = 2 × 12 s + 1.5 s latency; baseline 12 s;
		// 25.5 + 12 + 100 + 30 = 167.5 s.
		"with the slowest times measured (each from at least 3 measurement(s)), the preflight (~26 s), baseline (~12 s), faults (100 s) and recovery window (30 s) take ~168 s, more than maxDurationSec 135, so the run could be aborted. Make maxDurationSec at least 168.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings are missing %q\n%s", want, got)
		}
	}

	e.MaxDurationSec = 168 // the suggested value: enough
	if timing := assessTiming(e, s, oauthHealth, oauthUses); timing.MinMaxDurationSec != 0 {
		t.Errorf("with maxDurationSec 168: MinMaxDurationSec = %d, want 0", timing.MinMaxDurationSec)
	}
	e.MaxDurationSec = 170 // enough even on the slowest day measured
	for _, w := range assessTiming(e, s, oauthHealth, oauthUses).Warnings {
		if strings.Contains(w, "maxDurationSec") {
			t.Errorf("maxDurationSec 170 holds 167.5 s, but got %q", w)
		}
	}
}
