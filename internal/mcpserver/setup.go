package mcpserver

// Setup tools and the suite report: an agent reads a service's code, works
// out its dependencies, and sets Entropy up for it through these tools, with
// the same safe edits as the UI. Entropy itself never reads the code.

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed reference.md
var reference string

func (t *tools) registerSetup(s *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false
	writes := &mcp.ToolAnnotations{OpenWorldHint: &no}

	mcp.AddTool(s, &mcp.Tool{Name: "config_reference", Annotations: readOnly,
		Description: "How entropy.yaml works: dependencies (proxy address vs real upstream), services, commands, experiments, fault types, and good defaults for a suite per dependency. Read it before setting Entropy up or writing experiments."},
		t.configReference)
	mcp.AddTool(s, &mcp.Tool{Name: "add_dependency", Annotations: writes,
		Description: "Adds a dependency (Redis, MongoDB, an HTTP API, …) to entropy.yaml and opens its proxy. The service must then be pointed at the address (its connection setting changed and the service restarted) for faults to reach it."},
		t.addDependency)
	mcp.AddTool(s, &mcp.Tool{Name: "update_dependency", Annotations: writes,
		Description: "Changes a dependency's address, upstream, listen or redirect. Fields left out keep their value."},
		t.updateDependency)
	mcp.AddTool(s, &mcp.Tool{Name: "add_service", Annotations: writes,
		Description: "Adds a service (the code under test) to entropy.yaml, with the dependencies it uses."},
		t.addService)
	mcp.AddTool(s, &mcp.Tool{Name: "update_service", Annotations: writes,
		Description: "Changes a service's baseUrl, healthCheck, healthCommand, healthEverySec or uses. Fields left out keep their value."},
		t.updateService)
	mcp.AddTool(s, &mcp.Tool{Name: "add_command", Annotations: writes,
		Description: "Adds a command to a service: a command line Entropy runs on this machine to call the service (e.g. `bru run …`, `curl -f …`), used in experiments and as a health check. Exit code 0 means success. Show the person the command line before adding it."},
		t.addCommand)
	mcp.AddTool(s, &mcp.Tool{Name: "update_command", Annotations: writes,
		Description: "Changes a command's run, cwd or timeoutSec, e.g. when verify_route shows it doesn't reach the service. Fields left out keep their value. Show the person the new command line first."},
		t.updateCommand)
	mcp.AddTool(s, &mcp.Tool{Name: "remove_command", Annotations: writes,
		Description: "Removes a command from a service. Refused (nothing changes) while the health check or an experiment still uses it."},
		t.removeCommand)
	mcp.AddTool(s, &mcp.Tool{Name: "verify_route", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &no, IdempotentHint: true},
		Description: "Checks that a service really reaches a dependency through Entropy's proxy: runs one of the service's commands (or its health check) normally and with latency on the proxy, and compares. Not verified means faults won't affect the service: point it at the dependency's address."},
		t.verifyRoute)
	mcp.AddTool(s, &mcp.Tool{Name: "suite_report", Annotations: readOnly,
		Description: "A table of each dependency against each fault type, with the latest verdict of the experiments that cover it, and the dependencies no experiment covers."},
		t.suiteReport)

	s.AddPrompt(&mcp.Prompt{Name: "setup", Title: "Set Entropy up for a service",
		Description: "Read a service's code, find its dependencies, and set Entropy up for it: dependencies, the service, its commands, and checked routes.",
		Arguments:   []*mcp.PromptArgument{{Name: "service", Description: "the service's name, or the path to its code (default: the current project)"}},
	}, setupPrompt)
	s.AddPrompt(&mcp.Prompt{Name: "suite", Title: "Chaos test every dependency",
		Description: "Propose an experiment per dependency and fault with the expected behaviour, save the ones you confirm, run them, and report.",
		Arguments:   []*mcp.PromptArgument{{Name: "service", Description: "the service to test (default: every service in entropy.yaml)"}},
	}, suitePrompt)
}

func (t *tools) configReference(context.Context, *mcp.CallToolRequest, noInput) (*mcp.CallToolResult, any, error) {
	return text(reference), nil, nil
}

// saved reports an edit's sync: warnings, then the state of what was added.
func (t *tools) saved(ctx context.Context, what string, res EditResult, extra func(Environment, *strings.Builder)) *mcp.CallToolResult {
	var b strings.Builder
	if strings.HasPrefix(what, "Removed ") {
		b.WriteString(what) // a whole sentence
	} else {
		fmt.Fprintf(&b, "Saved %s in entropy.yaml.", what)
	}
	for _, w := range res.Sync.Warnings {
		fmt.Fprintf(&b, "\nConfig warning: %s", w)
	}
	if extra != nil {
		if env, err := t.c.Environment(ctx); err == nil {
			extra(env, &b)
		}
	}
	return text(b.String())
}

type addDependencyInput struct {
	Name     string `json:"name" jsonschema:"a short name, e.g. redis, mongodb, payments-api"`
	Kind     string `json:"kind" jsonschema:"redis, mongodb, postgres, rabbitmq, http or https"`
	Address  string `json:"address" jsonschema:"host:port the service will connect to: the proxy, e.g. 127.0.0.1:16379"`
	Upstream string `json:"upstream" jsonschema:"host:port of the real dependency, e.g. 127.0.0.1:6379, or auto:6379 for a container on this machine"`
	Listen   string `json:"listen,omitempty" jsonschema:"optional: where the proxy listens; default: the address's port on 127.0.0.1"`
}

func (t *tools) addDependency(ctx context.Context, _ *mcp.CallToolRequest, in addDependencyInput) (*mcp.CallToolResult, any, error) {
	res, err := t.edit(ctx, "addDependency", func(ConfigSnapshot) (any, error) { return in, nil })
	if err != nil {
		return nil, nil, err
	}
	return t.saved(ctx, "dependency "+in.Name, res, func(env Environment, b *strings.Builder) {
		if i := slices.IndexFunc(env.Dependencies, func(d Dependency) bool { return d.Name == in.Name }); i >= 0 {
			d := env.Dependencies[i]
			fmt.Fprintf(b, "\nIts proxy: %s", state(d.Status))
			if !d.Ready && d.NotReadyReason != "" {
				fmt.Fprintf(b, "; not ready: %s", d.NotReadyReason)
			}
		}
		fmt.Fprintf(b, "\nNext: point the service at %s instead of %s (its connection setting), have it restarted, then add_service or update_service with uses, and verify_route.", in.Address, in.Upstream)
	}), nil, nil
}

type updateDependencyInput struct {
	Name     string  `json:"name" jsonschema:"the dependency to change"`
	Address  *string `json:"address,omitempty"`
	Upstream *string `json:"upstream,omitempty"`
	Listen   *string `json:"listen,omitempty" jsonschema:"empty string: back to the default"`
	Redirect *bool   `json:"redirect,omitempty" jsonschema:"https only: the hostname points at 127.0.0.1 in the hosts file"`
}

func (t *tools) updateDependency(ctx context.Context, _ *mcp.CallToolRequest, in updateDependencyInput) (*mcp.CallToolResult, any, error) {
	res, err := t.edit(ctx, "dependency", func(cfg ConfigSnapshot) (any, error) {
		i := slices.IndexFunc(cfg.Config.Dependencies, func(d RawDependency) bool { return d.Name == in.Name })
		if i < 0 {
			return nil, fmt.Errorf("no dependency %q in entropy.yaml (add it with add_dependency)", in.Name)
		}
		d := cfg.Config.Dependencies[i]
		set(&d.Address, in.Address)
		set(&d.Upstream, in.Upstream)
		set(&d.Listen, in.Listen)
		set(&d.Redirect, in.Redirect)
		return map[string]any{"name": d.Name, "address": d.Address, "upstream": d.Upstream, "listen": d.Listen, "redirect": d.Redirect}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return t.saved(ctx, "dependency "+in.Name, res, nil), nil, nil
}

type addServiceInput struct {
	Name        string   `json:"name" jsonschema:"a short name, e.g. oauth"`
	BaseURL     string   `json:"baseUrl" jsonschema:"where the service listens, e.g. http://127.0.0.1:8000"`
	HealthCheck string   `json:"healthCheck,omitempty" jsonschema:"optional URL path that answers 2xx when healthy, e.g. /health"`
	Uses        []string `json:"uses,omitempty" jsonschema:"the dependencies it uses (their names in entropy.yaml)"`
}

func (t *tools) addService(ctx context.Context, _ *mcp.CallToolRequest, in addServiceInput) (*mcp.CallToolResult, any, error) {
	res, err := t.edit(ctx, "addService", func(ConfigSnapshot) (any, error) { return in, nil })
	if err != nil {
		return nil, nil, err
	}
	return t.saved(ctx, "service "+in.Name, res, func(env Environment, b *strings.Builder) {
		if i := slices.IndexFunc(env.Services, func(s Service) bool { return s.Name == in.Name }); i >= 0 {
			fmt.Fprintf(b, "\nIts health: %s", state(env.Services[i].Status))
		}
		b.WriteString("\nNext: add_command for requests that use each dependency, then verify_route for each dependency.")
	}), nil, nil
}

type updateServiceInput struct {
	Name           string    `json:"name" jsonschema:"the service to change"`
	BaseURL        *string   `json:"baseUrl,omitempty"`
	HealthCheck    *string   `json:"healthCheck,omitempty" jsonschema:"URL path; empty string removes it"`
	HealthCommand  *string   `json:"healthCommand,omitempty" jsonschema:"one of its commands, used as the health check instead; empty string removes it"`
	HealthEverySec *int      `json:"healthEverySec,omitempty" jsonschema:"0: run the health command only on demand; or 15 to 86400"`
	Uses           *[]string `json:"uses,omitempty" jsonschema:"the whole list of dependencies it uses"`
}

func (t *tools) updateService(ctx context.Context, _ *mcp.CallToolRequest, in updateServiceInput) (*mcp.CallToolResult, any, error) {
	res, err := t.edit(ctx, "service", func(cfg ConfigSnapshot) (any, error) {
		i := slices.IndexFunc(cfg.Config.Services, func(s RawService) bool { return s.Name == in.Name })
		if i < 0 {
			return nil, fmt.Errorf("no service %q in entropy.yaml (add it with add_service)", in.Name)
		}
		s := cfg.Config.Services[i]
		set(&s.BaseURL, in.BaseURL)
		set(&s.HealthCheck, in.HealthCheck)
		set(&s.HealthCommand, in.HealthCommand)
		set(&s.HealthEverySec, in.HealthEverySec)
		set(&s.Uses, in.Uses)
		if s.Uses == nil {
			s.Uses = []string{}
		}
		return s, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return t.saved(ctx, "service "+in.Name, res, nil), nil, nil
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

type addCommandInput struct {
	Service    string `json:"service" jsonschema:"the service it calls"`
	Name       string `json:"name" jsonschema:"a short name, e.g. token"`
	Run        string `json:"run" jsonschema:"the command line, as typed in a terminal (cmd.exe on Windows, sh elsewhere); exit code 0 = success"`
	Cwd        string `json:"cwd,omitempty" jsonschema:"optional working folder; default: the folder of entropy.yaml"`
	TimeoutSec int    `json:"timeoutSec,omitempty" jsonschema:"optional; default 30, max 600"`
}

func (t *tools) addCommand(ctx context.Context, _ *mcp.CallToolRequest, in addCommandInput) (*mcp.CallToolResult, any, error) {
	res, err := t.edit(ctx, "addCommand", func(ConfigSnapshot) (any, error) { return in, nil })
	if err != nil {
		return nil, nil, err
	}
	r := t.saved(ctx, "command "+in.Service+"/"+in.Name, res, nil)
	r.Content = append(r.Content, &mcp.TextContent{Text: "\nTry it with run_command; use it as the health check with update_service healthCommand."})
	return r, nil, nil
}

type updateCommandInput struct {
	Service    string  `json:"service"`
	Name       string  `json:"name" jsonschema:"the command to change"`
	Run        *string `json:"run,omitempty" jsonschema:"the new command line"`
	Cwd        *string `json:"cwd,omitempty" jsonschema:"empty string: back to the folder of entropy.yaml"`
	TimeoutSec *int    `json:"timeoutSec,omitempty" jsonschema:"0: back to the default (30)"`
}

func (t *tools) updateCommand(ctx context.Context, _ *mcp.CallToolRequest, in updateCommandInput) (*mcp.CallToolResult, any, error) {
	res, err := t.edit(ctx, "command", func(cfg ConfigSnapshot) (any, error) {
		c, err := findCommand(cfg, in.Service, in.Name)
		if err != nil {
			return nil, err
		}
		set(&c.Run, in.Run)
		set(&c.Cwd, in.Cwd)
		set(&c.TimeoutSec, in.TimeoutSec)
		return map[string]any{"service": in.Service, "name": c.Name, "run": c.Run, "cwd": c.Cwd, "timeoutSec": c.TimeoutSec}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return t.saved(ctx, "command "+in.Service+"/"+in.Name, res, nil), nil, nil
}

type commandRefInput struct {
	Service string `json:"service"`
	Name    string `json:"name" jsonschema:"the command to remove"`
}

func (t *tools) removeCommand(ctx context.Context, _ *mcp.CallToolRequest, in commandRefInput) (*mcp.CallToolResult, any, error) {
	res, err := t.edit(ctx, "removeCommand", func(cfg ConfigSnapshot) (any, error) {
		if _, err := findCommand(cfg, in.Service, in.Name); err != nil {
			return nil, err
		}
		return in, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return t.saved(ctx, "Removed command "+in.Service+"/"+in.Name+" from entropy.yaml.", res, nil), nil, nil
}

func findCommand(cfg ConfigSnapshot, service, name string) (RawCommand, error) {
	i := slices.IndexFunc(cfg.Config.Services, func(s RawService) bool { return s.Name == service })
	if i < 0 {
		return RawCommand{}, fmt.Errorf("no service %q in entropy.yaml", service)
	}
	cmds := cfg.Config.Services[i].Commands
	j := slices.IndexFunc(cmds, func(c RawCommand) bool { return c.Name == name })
	if j < 0 {
		return RawCommand{}, fmt.Errorf("service %s has no command %q in entropy.yaml", service, name)
	}
	return cmds[j], nil
}

type verifyRouteInput struct {
	Dependency string `json:"dependency"`
	Service    string `json:"service"`
	Command    string `json:"command,omitempty" jsonschema:"one of the service's commands that uses the dependency; empty: its health check"`
}

func (t *tools) verifyRoute(ctx context.Context, _ *mcp.CallToolRequest, in verifyRouteInput) (*mcp.CallToolResult, any, error) {
	rc, err := t.c.VerifyRoute(ctx, in.Dependency, in.Service, in.Command)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	switch rc.State {
	case "verified":
		fmt.Fprintf(&b, "VERIFIED: %s reaches %s through Entropy's proxy.", in.Service, in.Dependency)
	case "not_seen":
		fmt.Fprintf(&b, "NOT SEEN: %s doesn't seem to reach %s through the proxy, so faults won't affect it.", in.Service, in.Dependency)
	default:
		fmt.Fprintf(&b, "UNKNOWN: couldn't tell whether %s reaches %s through the proxy.", in.Service, in.Dependency)
	}
	if rc.Detail != "" {
		fmt.Fprintf(&b, "\n%s", rc.Detail)
	}
	if rc.NormalMs > 0 || rc.SlowedMs > 0 {
		fmt.Fprintf(&b, "\nWith %s: %d ms normally, %d ms with %d ms of latency on the proxy.", cmp.Or(rc.Trigger, "the health check"), rc.NormalMs, rc.SlowedMs, rc.LatencyMs)
	}
	if rc.State != "verified" {
		b.WriteString("\nCheck that the service's connection setting is the dependency's address (not the real one) and that it was restarted; or pick a command that really uses the dependency.")
	}
	return text(b.String()), nil, nil
}

// faultOrder is the order of the report's columns.
var faultOrder = []string{"outage", "latency", "timeout", "drop", "flap"}

func (t *tools) suiteReport(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	env, err := t.c.Environment(ctx)
	if err != nil {
		return nil, nil, err
	}
	exps, err := t.c.Experiments(ctx)
	if err != nil {
		return nil, nil, err
	}
	type cell struct{ name, mark string }
	cells := map[[2]string][]cell{} // dependency, fault type
	var deps []string
	for _, d := range env.Dependencies {
		if !d.Missing {
			deps = append(deps, d.Name)
		}
	}
	var failures []string
	for _, e := range exps {
		if e.Missing {
			continue
		}
		mark := "· never run"
		switch {
		case e.Running:
			mark = "… running"
		case e.LastRun != nil && e.LastRun.Verdict == "passed":
			mark = "✓ passed"
		case e.LastRun != nil:
			mark = "✗ " + e.LastRun.Verdict
			failures = append(failures, fmt.Sprintf("- %s (run %s): %s", e.Name, e.LastRun.ID, outcome(*e.LastRun)))
		}
		for _, f := range e.Definition.Faults {
			k := [2]string{f.Dependency, f.Type}
			cells[k] = append(cells[k], cell{e.Name, mark})
			if !slices.Contains(deps, f.Dependency) {
				deps = append(deps, f.Dependency)
			}
		}
	}

	var cols []string
	for _, f := range faultOrder {
		for _, d := range deps {
			if len(cells[[2]string{d, f}]) > 0 {
				cols = append(cols, f)
				break
			}
		}
	}
	var b strings.Builder
	if len(cols) == 0 {
		b.WriteString("No experiment has faults yet. Write one per dependency (config_reference has good defaults).\n")
	} else {
		rows := [][]string{append([]string{"dependency"}, cols...)}
		for _, d := range deps {
			row := []string{d}
			for _, f := range cols {
				var parts []string
				for _, c := range cells[[2]string{d, f}] {
					parts = append(parts, c.mark+" ("+c.name+")")
				}
				row = append(row, cmp.Or(strings.Join(parts, "; "), "–"))
			}
			rows = append(rows, row)
		}
		writeTable(&b, rows)
	}

	var uncovered []string
	for _, d := range deps {
		covered := false
		for _, f := range faultOrder {
			covered = covered || len(cells[[2]string{d, f}]) > 0
		}
		if !covered {
			uncovered = append(uncovered, d)
		}
	}
	if len(uncovered) > 0 {
		fmt.Fprintf(&b, "\nNo experiment for: %s\n", strings.Join(uncovered, ", "))
	}
	if len(failures) > 0 {
		b.WriteString("\nNot passed (latest run):\n" + strings.Join(failures, "\n") + "\nget_run shows why.\n")
	}
	return text(b.String()), nil, nil
}

// writeTable writes rows as aligned columns, the first row as the header.
func writeTable(b *strings.Builder, rows [][]string) {
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			width[i] = max(width[i], len([]rune(c)))
		}
	}
	for n, r := range rows {
		for i, c := range r {
			pad := ""
			if i < len(r)-1 {
				pad = strings.Repeat(" ", width[i]-len([]rune(c))+2)
			}
			b.WriteString(c + pad)
		}
		b.WriteString("\n")
		if n == 0 {
			for i := range r {
				b.WriteString(strings.Repeat("-", width[i]))
				if i < len(r)-1 {
					b.WriteString("  ")
				}
			}
			b.WriteString("\n")
		}
	}
}

func promptResult(desc, body string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{Description: desc,
		Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: body}}}}
}

func setupPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	target := cmp.Or(strings.TrimSpace(req.Params.Arguments["service"]), "the service in the current project")
	return promptResult("Set Entropy up for "+target, `Set Entropy up for `+target+`, so its dependencies can be broken on purpose.

1. Call config_reference and entropy_status (to see what's already set up).
2. Read the service's code and config (.env files, config files, compose files, the code that creates clients) and list each dependency it connects to: Redis, databases, queues, external HTTP APIs. For each, note the setting that holds its address and the real address. If the service already points at proxy addresses, its config doesn't show the real ones: take them from the existing entropy.yaml or ask the person. Also note client timeouts and retries: they decide what a fault will do.
3. Propose, in one message, before changing anything:
   - each dependency: name, kind, the proxy address the service will use, and the real upstream;
   - the service: name, baseUrl, health check, uses;
   - commands that call the service and really use each dependency (e.g. Bruno requests in the repo, or curl), shown as the exact command lines;
   - the changes to the service's own config, to point it at the proxy addresses.
   Ask the person to confirm. Never edit the hosts file yourself; give the line to add.
4. After confirmation: add_dependency, add_service, add_command (and update_service healthCommand if a command is the health check). Change the service's config to the proxy addresses, and ask the person to restart the service.
5. When it's restarted, run verify_route for each dependency with a command that uses it. Fix anything NOT SEEN before going on: check the service's connection settings, and where the command really sends its request (a Bruno request can set its own URL variable that overrides --env-var); fix the command with update_command, and remove_command the ones that aren't needed.
6. Summarize what's set up, and suggest running the "suite" prompt to test every dependency.`), nil
}

func suitePrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	target := cmp.Or(strings.TrimSpace(req.Params.Arguments["service"]), "every service in entropy.yaml")
	return promptResult("Chaos test every dependency of "+target, `Build and run a chaos test suite for `+target+`: an experiment per dependency and fault.

1. Call config_reference, entropy_status and suite_report to see the setup and what's covered already.
2. For each dependency the service uses, read how the service uses it (a cache? primary storage? an external API?), its client's timeouts and retries, and which of its commands go through it.
3. Look for side effects of slow or failed requests: alerts or notifications (e.g. a "slow request" message to a chat through a message queue), logs or traces sent to shared systems. Tell the person, and with their agreement point them somewhere harmless for the test (e.g. an unused local address in the service's env, then restart it).
4. Propose the suite in one message, as a table: experiment name (<dependency>-<fault>-<service>), the fault, the requests, and the EXPECTED BEHAVIOUR in plain words (e.g. "tokens still issued, slower", "clean 503 within 10 s during the outage, works again within 20 s"). Express time limits with maxMs. Use the defaults in config_reference: outage for every dependency, latency for every dependency, flap only for long-lived connections. Skip a dependency no command reaches, and say so. Ask the person to confirm or change the expected behaviour: it's their decision, not yours.
5. Save the confirmed experiments with save_experiment (quote values that contain ": "). Fix any timing warning (maxDurationSec) it reports.
6. Run them one at a time with run_experiment, outages first. Only one run can happen at a time, and each breaks the dependency for every service using it.
7. Show suite_report. For each experiment that didn't pass: why (get_run; "no answer within N s" means the request hung), and where in the service's code the behaviour comes from (timeouts, retries, error handling). Propose fixes; don't change the service's code unless the person asks.`), nil
}
