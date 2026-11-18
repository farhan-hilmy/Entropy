// Package mcpserver offers Entropy to AI agents as MCP tools. Each tool calls
// a running Entropy's REST API, so `entropy up` stays the one place that holds
// faults and runs, and the same tools could be served over HTTP later.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `Entropy breaks a service's dependencies on purpose (Redis, MongoDB, … down,
slow or flapping) and checks how the service copes. An experiment says which
faults to apply, which requests to send before, during and after them, and what
must hold. A run gives a verdict: passed, failed, inconclusive or aborted.

Typical loop: entropy_status, list_experiments, run_experiment, read why it
failed, fix the service's code, run_experiment again until it passes.

A run really breaks the dependency for every service on this machine that uses
it, for up to the experiment's maxDurationSec. Faults are always timed and
removed afterwards. stop_run and stop_all_faults end them at once.`

// New returns the MCP server with Entropy's tools, calling the API through c.
func New(c *Client, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "entropy", Title: "Entropy", Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	t := &tools{c: c, poll: 2 * time.Second}

	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false
	stop := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}

	mcp.AddTool(s, &mcp.Tool{Name: "entropy_status", Annotations: readOnly,
		Description: "The environment: each dependency (reachable? a fault on it?), each service (healthy? its commands), and any run in progress."},
		t.status)
	mcp.AddTool(s, &mcp.Tool{Name: "list_experiments", Annotations: readOnly,
		Description: "Every experiment with its faults, its last run's verdict, and timing warnings (whether it fits in maxDurationSec)."},
		t.listExperiments)
	mcp.AddTool(s, &mcp.Tool{Name: "get_experiment", Annotations: readOnly,
		Description: "One experiment as YAML, exactly as written in entropy.yaml (comments included), plus its recent runs. Edit it and pass it to save_experiment."},
		t.getExperiment)
	mcp.AddTool(s, &mcp.Tool{Name: "list_runs", Annotations: readOnly,
		Description: "The latest runs, newest first, with verdict and reason."},
		t.listRuns)
	mcp.AddTool(s, &mcp.Tool{Name: "get_run", Annotations: readOnly,
		Description: "One run: verdict, reason, each assertion's outcome, and the timeline (phases, faults, every request's result)."},
		t.getRun)
	mcp.AddTool(s, &mcp.Tool{Name: "run_experiment", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &no},
		Description: "Runs an experiment: applies its faults (they really break the dependency for every service using it), sends its requests, and waits for the verdict. Only one run at a time. Returns the result as get_run does."},
		t.runExperiment)
	mcp.AddTool(s, &mcp.Tool{Name: "stop_run", Annotations: stop,
		Description: "Aborts a run and removes its faults. Without an id, stops the run in progress."},
		t.stopRun)
	mcp.AddTool(s, &mcp.Tool{Name: "stop_all_faults", Annotations: stop,
		Description: "Removes every fault now, from runs or set by hand. The safety exit."},
		t.stopAllFaults)
	mcp.AddTool(s, &mcp.Tool{Name: "save_experiment", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &no},
		Description: "Saves one experiment (YAML, as get_experiment gives it) in entropy.yaml: replaces the experiment called name, or adds a new one when name is empty. Only that experiment's lines change; the whole config is checked first, and an invalid one changes nothing. The YAML given replaces the experiment as it is now, so call get_experiment first if a person may have edited it meanwhile."},
		t.saveExperiment)
	mcp.AddTool(s, &mcp.Tool{Name: "run_command", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &no},
		Description: "Runs one of a service's commands (from entropy.yaml, e.g. a request to the service) once, without any fault, and returns its exit code and output. A quick check, e.g. that a fix still works normally, before a full run."},
		t.runCommand)
	return s
}

type tools struct {
	c      *Client
	poll   time.Duration // how often run_experiment checks the run
	saveMu sync.Mutex
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

type noInput struct{}

func (t *tools) status(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	env, err := t.c.Environment(ctx)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Environment %s: %s (config %s)\n", env.Name, env.Status, env.ConfigPath)
	tp := env.Toxiproxy.API
	if env.Toxiproxy.Builtin {
		tp = "built-in"
	}
	fmt.Fprintf(&b, "Toxiproxy: %s, %s\n", tp, state(env.Toxiproxy.Status))
	if env.ActiveRun != "" {
		fmt.Fprintf(&b, "Run in progress: %s\n", env.ActiveRun)
	} else {
		b.WriteString("Run in progress: none\n")
	}
	fmt.Fprintf(&b, "Active faults: %d\n", env.ActiveFaults)
	if env.LastSync.Error != "" {
		fmt.Fprintf(&b, "Config error: %s\n", env.LastSync.Error)
	}
	for _, w := range env.LastSync.Warnings {
		fmt.Fprintf(&b, "Config warning: %s\n", w)
	}

	b.WriteString("\nDependencies:\n")
	if len(env.Dependencies) == 0 {
		b.WriteString("  none\n")
	}
	for _, d := range env.Dependencies {
		if d.Missing {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s", d.Name, d.Kind, state(d.Status))
		if d.Fault != nil {
			fmt.Fprintf(&b, "; FAULT %s until %s", d.Fault.Type, d.Fault.EndsAt.Local().Format("15:04:05"))
		}
		if !d.Ready && d.NotReadyReason != "" {
			fmt.Fprintf(&b, "; not ready: %s", d.NotReadyReason)
		}
		if len(d.UsedBy) > 0 {
			fmt.Fprintf(&b, "; used by %s", strings.Join(d.UsedBy, ", "))
		}
		fmt.Fprintf(&b, "; services connect to %s, which forwards to %s\n", d.Address, d.Upstream)
	}

	b.WriteString("\nServices:\n")
	if len(env.Services) == 0 {
		b.WriteString("  none\n")
	}
	for _, s := range env.Services {
		if s.Missing {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s", s.Name, state(s.Status))
		if len(s.Uses) > 0 {
			fmt.Fprintf(&b, "; uses %s", strings.Join(s.Uses, ", "))
		}
		b.WriteString("\n")
		for _, c := range s.Commands {
			fmt.Fprintf(&b, "    command %s/%s: %s\n", s.Name, c.Name, c.Run)
		}
	}
	return text(b.String()), nil, nil
}

func state(s Status) string {
	st := strings.ReplaceAll(s.State, "_", " ")
	if s.Detail != "" && s.State != "reachable" {
		st += " (" + s.Detail + ")"
	}
	return st
}

func (t *tools) listExperiments(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	exps, err := t.c.Experiments(ctx)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	n := 0
	for _, e := range exps {
		if e.Missing {
			continue
		}
		n++
		fmt.Fprintf(&b, "- %s (v%d)", e.Name, e.Version)
		if e.Definition.Description != "" {
			fmt.Fprintf(&b, ": %s", e.Definition.Description)
		}
		b.WriteString("\n")
		var faults []string
		for _, f := range e.Definition.Faults {
			faults = append(faults, fmt.Sprintf("%s %s for %d s", f.Dependency, f.Type, f.DurationSec))
		}
		if len(faults) == 0 {
			faults = []string{"none"}
		}
		fmt.Fprintf(&b, "    faults: %s; maxDurationSec %d\n", strings.Join(faults, ", "), e.Definition.MaxDurationSec)
		switch {
		case e.Running:
			b.WriteString("    running now\n")
		case e.LastRun != nil:
			fmt.Fprintf(&b, "    last run %s: %s\n", e.LastRun.ID, outcome(*e.LastRun))
		default:
			b.WriteString("    never run\n")
		}
		for _, w := range e.Timing.Warnings {
			fmt.Fprintf(&b, "    timing warning: %s\n", w)
		}
	}
	if n == 0 {
		return text("No experiments yet. Write one with save_experiment (get_experiment on an existing one shows the format)."), nil, nil
	}
	return text(b.String()), nil, nil
}

type nameInput struct {
	Name string `json:"name" jsonschema:"the experiment's name"`
}

func (t *tools) getExperiment(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, any, error) {
	src, err := t.c.ExperimentSource(ctx, in.Name)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Experiment %s, as written in entropy.yaml:\n\n%s", in.Name, src.YAML)
	if exps, err := t.c.Experiments(ctx); err == nil {
		if i := slices.IndexFunc(exps, func(e Experiment) bool { return e.Name == in.Name }); i >= 0 {
			for _, w := range exps[i].Timing.Warnings {
				fmt.Fprintf(&b, "\nTiming warning: %s", w)
			}
		}
	}
	if runs, err := t.c.Runs(ctx, in.Name); err == nil && len(runs) > 0 {
		b.WriteString("\n\nRecent runs:\n")
		for _, r := range runs[:min(5, len(runs))] {
			fmt.Fprintf(&b, "- %s (v%d, %s): %s\n", r.ID, r.Version, r.StartedAt.Local().Format("2006-01-02 15:04"), outcome(r))
		}
	}
	return text(b.String()), nil, nil
}

type listRunsInput struct {
	Experiment string `json:"experiment,omitempty" jsonschema:"only this experiment's runs"`
	Limit      int    `json:"limit,omitempty" jsonschema:"at most this many (default 10)"`
}

func (t *tools) listRuns(ctx context.Context, _ *mcp.CallToolRequest, in listRunsInput) (*mcp.CallToolResult, any, error) {
	runs, err := t.c.Runs(ctx, in.Experiment)
	if err != nil {
		return nil, nil, err
	}
	if len(runs) == 0 {
		return text("No runs yet."), nil, nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	var b strings.Builder
	for _, r := range runs[:min(limit, len(runs))] {
		fmt.Fprintf(&b, "- %s: %s v%d, %s: %s\n", r.ID, r.Experiment, r.Version, r.StartedAt.Local().Format("2006-01-02 15:04"), outcome(r))
	}
	return text(b.String()), nil, nil
}

// outcome is a run's verdict and reason, or its phase while it runs.
func outcome(r Run) string {
	if !r.Done() {
		return "running (" + r.State + ")"
	}
	s := strings.ToUpper(r.Verdict)
	if r.Reason != "" {
		s += ": " + r.Reason
	}
	return s
}

type runIDInput struct {
	ID string `json:"id" jsonschema:"the run's id"`
}

func (t *tools) getRun(ctx context.Context, _ *mcp.CallToolRequest, in runIDInput) (*mcp.CallToolResult, any, error) {
	r, err := t.c.Run(ctx, in.ID)
	if err != nil {
		return nil, nil, err
	}
	return text(report(r)), nil, nil
}

// maxEvents bounds the timeline in a report; runs rarely have more.
const maxEvents = 200

// report is a run as an agent needs it: the verdict first, then each
// assertion, then the timeline.
func report(r Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s of %s (v%d)", r.ID, r.Experiment, r.Version)
	if r.RerunOf != "" {
		fmt.Fprintf(&b, ", a rerun of %s", r.RerunOf)
	}
	fmt.Fprintf(&b, "\nResult: %s\n", outcome(r))
	if r.EndedAt != nil {
		took := r.EndedAt.Sub(r.StartedAt)
		if took < 10*time.Second {
			took = took.Round(100 * time.Millisecond)
		} else {
			took = took.Round(time.Second)
		}
		fmt.Fprintf(&b, "Took %s\n", took)
	}
	if len(r.Assertions) > 0 {
		b.WriteString("\nAssertions:\n")
		for _, a := range r.Assertions {
			label := fmt.Sprintf("#%d", a.Index+1)
			if r.Snapshot != nil && a.Index < len(r.Snapshot.Experiment.Assertions) {
				x := r.Snapshot.Experiment.Assertions[a.Index]
				label += " " + x.Type
				if x.Trigger != "" {
					label += " on " + x.Trigger
				} else if x.Service != "" {
					label += " of " + x.Service
				}
			}
			fmt.Fprintf(&b, "- %s: %s", label, strings.ToUpper(strings.ReplaceAll(a.Outcome, "_", " ")))
			if a.Detail != "" {
				fmt.Fprintf(&b, ": %s", a.Detail)
			}
			b.WriteString("\n")
		}
	}
	if len(r.Events) > 0 {
		b.WriteString("\nTimeline:\n")
		start := r.StartedAt
		for i, e := range r.Events {
			if i == maxEvents {
				fmt.Fprintf(&b, "… %d more events (the UI shows them all)\n", len(r.Events)-maxEvents)
				break
			}
			fmt.Fprintf(&b, "%6.1fs %-8s %s\n", e.At.Sub(start).Seconds(), e.Type, e.Message)
		}
	}
	return b.String()
}

type runExperimentInput struct {
	Name    string `json:"name" jsonschema:"the experiment to run"`
	WaitSec *int   `json:"waitSec,omitempty" jsonschema:"how long to wait for the verdict; 0 returns at once with the run id (then use get_run). Default: until it finishes (at most its maxDurationSec)"`
}

func (t *tools) runExperiment(ctx context.Context, req *mcp.CallToolRequest, in runExperimentInput) (*mcp.CallToolResult, any, error) {
	run, err := t.c.StartRun(ctx, in.Name)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			if env, e2 := t.c.Environment(ctx); e2 == nil && env.ActiveRun != "" {
				return nil, nil, fmt.Errorf("%v. Run %s is in progress: wait for it (get_run), or stop it with stop_run", err, env.ActiveRun)
			}
		}
		return nil, nil, err
	}

	// Entropy aborts a run at maxDurationSec, so waiting a bit longer than
	// that always sees the end.
	limit := 60 * time.Minute
	if exps, err := t.c.Experiments(ctx); err == nil {
		if i := slices.IndexFunc(exps, func(e Experiment) bool { return e.Name == in.Name }); i >= 0 && exps[i].Definition.MaxDurationSec > 0 {
			limit = time.Duration(exps[i].Definition.MaxDurationSec)*time.Second + time.Minute
		}
	}
	if in.WaitSec != nil {
		limit = min(limit, time.Duration(max(*in.WaitSec, 0))*time.Second)
	}

	deadline := time.Now().Add(limit)
	token := req.Params.GetProgressToken()
	phase, step := "", 0.0
	for {
		if run.Done() {
			return text(report(run)), nil, nil
		}
		if run.State != phase {
			phase = run.State
			step++
			if token != nil {
				req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token, Progress: step, Message: "run " + run.ID + ": " + phase,
				}) // best effort
			}
		}
		if !time.Now().Before(deadline) {
			return text(fmt.Sprintf("Run %s of %s started and is still running (%s). Call get_run with id %s for the verdict, or stop_run to abort it.",
				run.ID, run.Experiment, run.State, run.ID)), nil, nil
		}
		select {
		case <-ctx.Done():
			// The run goes on in Entropy; its own limits still apply.
			return nil, nil, fmt.Errorf("stopped waiting; run %s goes on in Entropy (stop_run aborts it): %w", run.ID, ctx.Err())
		case <-time.After(min(t.poll, time.Until(deadline))):
		}
		if run, err = t.c.Run(ctx, run.ID); err != nil {
			return nil, nil, err
		}
	}
}

type stopRunInput struct {
	ID string `json:"id,omitempty" jsonschema:"the run to stop; empty: the run in progress"`
}

func (t *tools) stopRun(ctx context.Context, _ *mcp.CallToolRequest, in stopRunInput) (*mcp.CallToolResult, any, error) {
	id := in.ID
	if id == "" {
		env, err := t.c.Environment(ctx)
		if err != nil {
			return nil, nil, err
		}
		if env.ActiveRun == "" {
			return text("No run is in progress."), nil, nil
		}
		id = env.ActiveRun
	}
	if err := t.c.StopRun(ctx, id); err != nil {
		return nil, nil, err
	}
	return text(fmt.Sprintf("Run %s aborted; its faults are removed.", id)), nil, nil
}

func (t *tools) stopAllFaults(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	if err := t.c.StopAllFaults(ctx); err != nil {
		return nil, nil, err
	}
	return text("Every fault is removed."), nil, nil
}

type saveExperimentInput struct {
	Name string `json:"name,omitempty" jsonschema:"the experiment to replace; empty adds a new one. Changing name: inside the YAML renames it (past runs stay under the old name)"`
	YAML string `json:"yaml" jsonschema:"the whole experiment as YAML, keys at column 0 (name:, faults:, triggers:, assertions:, …), as get_experiment gives it"`
}

func (t *tools) saveExperiment(ctx context.Context, _ *mcp.CallToolRequest, in saveExperimentInput) (*mcp.CallToolResult, any, error) {
	// An agent may save several experiments at once; one at a time keeps
	// them from tripping over each other's hash.
	t.saveMu.Lock()
	defer t.saveMu.Unlock()

	// The hash guards against the file changing between reading and writing
	// it. The edit replaces just this experiment's lines with the whole
	// experiment given, and the file is checked again, so if something else
	// changed the file in between, saving on top of that is safe: retry once.
	var res EditResult
	for attempt := 0; ; attempt++ {
		cfg, err := t.c.Config(ctx)
		if err != nil {
			return nil, nil, err
		}
		res, err = t.c.SaveExperiment(ctx, cfg.Hash, in.Name, in.YAML)
		var ae *APIError
		if err != nil && errors.As(err, &ae) && ae.Status == http.StatusConflict && attempt == 0 {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		break
	}
	var b strings.Builder
	saved := yamlName(in.YAML)
	if saved == "" {
		saved = in.Name
	}
	fmt.Fprintf(&b, "Saved %s in entropy.yaml.", saved)
	if exps, err := t.c.Experiments(ctx); err == nil {
		if i := slices.IndexFunc(exps, func(e Experiment) bool { return e.Name == saved }); i >= 0 {
			fmt.Fprintf(&b, " It is now version %d.", exps[i].Version)
			for _, w := range exps[i].Timing.Warnings {
				fmt.Fprintf(&b, "\nTiming warning: %s", w)
			}
		}
	}
	for _, w := range res.Sync.Warnings {
		fmt.Fprintf(&b, "\nConfig warning: %s", w)
	}
	return text(b.String()), nil, nil
}

// yamlName is the experiment's top-level name: in its YAML (a line starting
// with "name:" or "- name:", not indented), or "".
func yamlName(yaml string) string {
	for _, l := range strings.Split(strings.ReplaceAll(yaml, "\r\n", "\n"), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimPrefix(l, "- "), "name:"); ok {
			if i := strings.Index(v, " #"); i >= 0 {
				v = v[:i]
			}
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

type runCommandInput struct {
	Service string `json:"service" jsonschema:"the service"`
	Command string `json:"command" jsonschema:"the command's name (entropy_status lists them)"`
}

// maxOutput bounds the command output in a result: its end is what matters.
const maxOutput = 8000

func (t *tools) runCommand(ctx context.Context, _ *mcp.CallToolRequest, in runCommandInput) (*mcp.CallToolResult, any, error) {
	r, err := t.c.RunCommand(ctx, in.Service, in.Command)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s (%s): ", r.Service, r.Command, r.Run)
	switch {
	case r.TimedOut:
		b.WriteString("timed out")
	case r.Error != "":
		b.WriteString("error: " + r.Error)
	default:
		fmt.Fprintf(&b, "exit code %d", r.ExitCode)
	}
	fmt.Fprintf(&b, " in %d ms\n", r.DurationMs)
	out := r.Output
	if len(out) > maxOutput {
		out = "…" + out[len(out)-maxOutput:]
	}
	if out != "" {
		fmt.Fprintf(&b, "\nOutput:\n%s", out)
	}
	return text(b.String()), nil, nil
}
