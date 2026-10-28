package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"entropy/internal/config"
)

func experiment(name, description string) config.Experiment {
	return config.Experiment{Name: name, Description: description, MaxDurationSec: 60,
		Triggers:   []config.Trigger{{Name: "t", Phase: "during", Command: "oauth/token", Repeat: 1}},
		Assertions: []config.Assertion{{Type: "recovery", Service: "oauth", WithinSec: 10}}}
}

func TestExperimentVersions(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	sync := func(exps ...config.Experiment) Summary {
		t.Helper()
		sum, err := st.Sync(ctx, &config.Config{Environment: config.Environment{Name: "local"}, Experiments: exps}, "entropy.yaml")
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	version := func(name string) (int, bool) {
		t.Helper()
		exps, err := st.Experiments(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range exps {
			if e.Name == name {
				return e.Version, e.Missing
			}
		}
		t.Fatalf("no experiment %s", name)
		return 0, false
	}

	if sum := sync(experiment("a", "first")); !slices.Equal(sum.Added, []string{"experiment a"}) {
		t.Errorf("added = %v", sum.Added)
	}
	sync(experiment("a", "first")) // unchanged: same version
	if v, _ := version("a"); v != 1 {
		t.Errorf("version after an unchanged sync = %d, want 1", v)
	}
	if sum := sync(experiment("a", "edited")); !slices.Equal(sum.Updated, []string{"experiment a"}) {
		t.Errorf("updated = %v", sum.Updated)
	}
	if v, _ := version("a"); v != 2 {
		t.Errorf("version after an edit = %d, want 2", v)
	}
	sync()
	if v, missing := version("a"); v != 2 || !missing {
		t.Errorf("removed experiment: version %d missing %v; want kept as missing", v, missing)
	}
}

func TestRunKeepsItsSnapshotAndTimeline(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	snap, _ := json.Marshal(experiment("a", "as it ran"))
	r := Run{Experiment: "a", Version: 1, Snapshot: snap, State: "baseline", StartedAt: time.Now()}
	if err := st.CreateRun(ctx, &r); err != nil {
		t.Fatal(err)
	}
	st.AddRunEvent(ctx, r.ID, RunEvent{At: time.Now(), Type: "phase", Message: "baseline"})
	st.AddRunEvent(ctx, r.ID, RunEvent{At: time.Now(), Type: "fault", Message: "outage on redis", Data: json.RawMessage(`{"durationSec":30}`)})
	st.SaveAssertionResults(ctx, r.ID, []AssertionResult{{Index: 0, Outcome: "pass", Detail: "ok"}})
	if err := st.SetRunState(ctx, r.ID, "done", "passed", "all assertions hold", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Editing the experiment afterwards must not touch the run.
	st.Sync(ctx, &config.Config{Environment: config.Environment{Name: "local"}, Experiments: []config.Experiment{experiment("a", "edited")}}, "entropy.yaml")

	got, err := st.Run(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ran config.Experiment
	json.Unmarshal(got.Snapshot, &ran)
	if ran.Description != "as it ran" {
		t.Errorf("snapshot description = %q; the run must keep what it ran", ran.Description)
	}
	if got.Verdict != "passed" || len(got.Events) != 2 || got.Events[1].Type != "fault" || len(got.Assertions) != 1 {
		t.Errorf("run = %+v", got)
	}
	list, _ := st.Runs(ctx, "a", 10)
	if len(list) != 1 || list[0].Snapshot != nil {
		t.Errorf("run list = %+v; want one run without its snapshot", list)
	}
}

func TestEndUnfinishedRuns(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	running := Run{Experiment: "a", Version: 1, Snapshot: json.RawMessage(`{}`), State: "during", StartedAt: time.Now()}
	done := Run{Experiment: "a", Version: 1, Snapshot: json.RawMessage(`{}`), State: "done", StartedAt: time.Now()}
	st.CreateRun(ctx, &running)
	st.CreateRun(ctx, &done)
	n, err := st.EndUnfinishedRuns(ctx, []string{"done"}, "Entropy stopped during the run")
	if err != nil || n != 1 {
		t.Fatalf("ended %d runs, err %v; want 1", n, err)
	}
	got, _ := st.Run(ctx, running.ID)
	if got.Verdict != "aborted" || got.State != "done" || got.EndedAt.IsZero() {
		t.Errorf("unfinished run = %+v; want aborted and ended", got)
	}
}
