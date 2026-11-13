package config

import (
	"errors"
	"strings"
	"testing"
)

const experimentFixture = editFixture + `
experiments:
  # The one we keep tuning.
  - name: redis-outage
    description: Redis down for 30 s.
    faults:
      - dependency: redis
        type: outage
        durationSec: 30   # long enough for 3 tokens
    triggers:
      - { name: before, phase: baseline, command: orders/token }
    # Steady state first.
    assertions:
      - type: response
        trigger: before
        expect: { outputContains: "(200 OK)" }
  - name: mongo-latency
    faults:
      - { dependency: mongo, type: latency, durationSec: 20, latencyMs: 500 }
    triggers:
      - { name: before, phase: baseline, command: orders/token }
    assertions:
      - { type: response, trigger: before, expect: { exitCode: 0 } }
# The end.
`

const redisOutageText = `name: redis-outage
description: Redis down for 30 s.
faults:
  - dependency: redis
    type: outage
    durationSec: 30   # long enough for 3 tokens
triggers:
  - { name: before, phase: baseline, command: orders/token }
# Steady state first.
assertions:
  - type: response
    trigger: before
    expect: { outputContains: "(200 OK)" }
`

func TestReadExperimentGivesItsLinesWithComments(t *testing.T) {
	path := write(t, experimentFixture)
	src, err := ReadExperiment(path, "redis-outage")
	if err != nil {
		t.Fatal(err)
	}
	if src.YAML != redisOutageText {
		t.Errorf("got\n%s\nwant\n%s", src.YAML, redisOutageText)
	}
	if _, err := ReadExperiment(path, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown experiment: got %v, want ErrNotFound", err)
	}
}

func TestEditExperimentChangesOnlyItsLines(t *testing.T) {
	path, snap := snapshot(t, experimentFixture)
	edited := strings.Replace(redisOutageText, "durationSec: 30   # long enough for 3 tokens", "durationSec: 45   # now 45", 1) +
		"recoveryWindowSec: 20 # watch a bit less\n"
	if _, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{Name: "redis-outage", YAML: edited}}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(experimentFixture, "        durationSec: 30   # long enough for 3 tokens", "        durationSec: 45   # now 45", 1)
	want = strings.Replace(want, `        expect: { outputContains: "(200 OK)" }
`, `        expect: { outputContains: "(200 OK)" }
    recoveryWindowSec: 20 # watch a bit less
`, 1)
	if got := fileContent(t, path); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if e := cfg.Experiments[0]; e.Faults[0].DurationSec != 45 || e.RecoveryWindowSec != 20 {
		t.Errorf("experiment after the edit: %+v", e)
	}
}

func TestAddExperimentPastedAsAListItem(t *testing.T) {
	path, snap := snapshot(t, experimentFixture)
	// As Copy as YAML on a run gives it: a list item, indented under experiments:.
	pasted := `
    - name: redis-flap
      faults:
        - { dependency: redis, type: flap, durationSec: 30, downSec: 5, upSec: 5 }
      triggers:
        - { name: before, phase: baseline, command: orders/token }
      assertions:
        - { type: response, trigger: before, expect: { exitCode: 0 } }

`
	if _, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{YAML: pasted}}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(experimentFixture, "# The end.\n", `
  - name: redis-flap
    faults:
      - { dependency: redis, type: flap, durationSec: 30, downSec: 5, upSec: 5 }
    triggers:
      - { name: before, phase: baseline, command: orders/token }
    assertions:
      - { type: response, trigger: before, expect: { exitCode: 0 } }
# The end.
`, 1)
	if got := fileContent(t, path); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAddFirstExperiment(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	text := "# Tried first.\nname: first\nfaults:\n  - { dependency: redis, type: outage, durationSec: 10 }\ntriggers:\n  - { name: t, phase: baseline, command: orders/token }\nassertions:\n  - { type: response, trigger: t, expect: { exitCode: 0 } }\n"
	if _, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{YAML: text}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Experiments) != 1 || cfg.Experiments[0].Name != "first" {
		t.Fatalf("experiments: %+v", cfg.Experiments)
	}
	if got := fileContent(t, path); !strings.HasSuffix(got, "\nexperiments:\n  # Tried first.\n  - name: first\n    faults:\n") &&
		!strings.Contains(got, "\nexperiments:\n  # Tried first.\n  - name: first\n    faults:\n") {
		t.Errorf("got\n%s", got)
	}
}

func TestRemoveExperiment(t *testing.T) {
	path, snap := snapshot(t, experimentFixture)
	if _, err := Apply(path, snap.Hash, Edit{RemoveExperiment: &Named{Name: "mongo-latency"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Experiments) != 1 || cfg.Experiments[0].Name != "redis-outage" {
		t.Fatalf("experiments: %+v", cfg.Experiments)
	}
	if got := fileContent(t, path); !strings.Contains(got, "        expect: { outputContains: \"(200 OK)\" }\n# The end.\n") {
		t.Errorf("got\n%s", got)
	}
}

func TestInvalidExperimentChangesNothing(t *testing.T) {
	cases := map[string]string{
		"unknown field":     strings.Replace(redisOutageText, "description:", "descripton:", 1),
		"not YAML":          "name: [oops\n",
		"two documents":     redisOutageText + "---\nname: other\n",
		"tabs":              "name: x\n\tfaults: []\n",
		"empty":             "# nothing\n\n",
		"invalid config":    strings.Replace(redisOutageText, "command: orders/token", "command: orders/nope", 1),
		"duplicate name":    strings.Replace(redisOutageText, "name: redis-outage", "name: mongo-latency", 1),
		"a list, not a map": "- a\n- b\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			path, snap := snapshot(t, experimentFixture)
			_, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{Name: "redis-outage", YAML: text}})
			var invalid *InvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("got %v, want an InvalidError", err)
			}
			if got := fileContent(t, path); got != experimentFixture {
				t.Errorf("the file changed:\n%s", got)
			}
		})
	}
}

func TestRenameExperiment(t *testing.T) {
	path, snap := snapshot(t, experimentFixture)
	text := strings.Replace(redisOutageText, "name: redis-outage", "name: redis-down", 1)
	if _, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{Name: "redis-outage", YAML: text}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExperiment(path, "redis-down"); err != nil {
		t.Error(err)
	}
}
