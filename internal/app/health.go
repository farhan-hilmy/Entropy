package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"entropy/internal/command"
	"entropy/internal/store"
)

// A service with a healthCommand is checked by running that command instead of
// the 5-second port/URL check. It runs on Sync, on "Check now" and, when
// healthEverySec is set, in the background. Health runs don't go into the
// command's run history; only runs started by hand do.

func healthCommand(s store.Service) (store.Command, bool) {
	if s.HealthCommand == "" || s.Missing {
		return store.Command{}, false
	}
	for _, c := range s.Commands {
		if c.Name == s.HealthCommand {
			return c, true
		}
	}
	return store.Command{}, false
}

// healthFreshFor is how old a failed health check may be for an experiment's
// preflight to trust it. Health commands only run on Sync or Check now, so an
// older failure may be outdated: then the baseline judges instead.
const healthFreshFor = time.Minute

// recentlyUnhealthy is why the service's last health check failed, if it
// failed within healthFreshFor; "" otherwise.
func (a *App) recentlyUnhealthy(s store.Service) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st := a.services[s.Name] // the 5-second URL or port check
	if _, ok := healthCommand(s); ok {
		st = a.cmdHealth[s.Name]
	}
	if st.State != Unreachable || time.Since(st.CheckedAt) > healthFreshFor {
		return ""
	}
	if st.Detail == "" {
		return "Unreachable"
	}
	return st.Detail
}

// CheckService runs a service's health command now and returns the new status.
func (a *App) CheckService(ctx context.Context, name string) (Status, error) {
	svcs, err := a.store.Services(ctx)
	if err != nil {
		return Status{}, err
	}
	for _, s := range svcs {
		if s.Name != name || s.Missing {
			continue
		}
		if _, ok := healthCommand(s); !ok {
			return Status{}, fmt.Errorf("%w: %s has no healthCommand; it's checked automatically every %s", ErrNotFound, name, CheckInterval)
		}
		return a.runHealthCommand(ctx, s)
	}
	return Status{}, fmt.Errorf("%w: no service %s", ErrNotFound, name)
}

func (a *App) runHealthCommand(ctx context.Context, s store.Service) (Status, error) {
	cmd, ok := healthCommand(s)
	if !ok {
		return Status{}, fmt.Errorf("%w: %s has no healthCommand", ErrNotFound, s.Name)
	}
	release, ok := a.claim(s.Name, cmd.Name)
	if !ok {
		return Status{}, ErrAlreadyRunning
	}
	defer release()

	healthyBefore := a.dependenciesHealthy(s.Uses)
	res := command.Run(ctx, cmd.Run, cmd.Cwd, time.Duration(cmd.TimeoutSec)*time.Second, CommandEnv(s.Name, s.BaseURL))
	st := healthStatus(cmd, res)
	// Only a run made while everything the service uses was up says how long
	// the request normally takes: with a dependency down or faulted, the
	// service may wait for its own timeouts (and a command can still exit 0).
	typical := healthyBefore && a.dependenciesHealthy(s.Uses)
	a.mu.Lock()
	a.cmdHealth[s.Name] = st
	if res.Err == nil && !res.TimedOut && typical {
		if a.healthTook == nil {
			a.healthTook = map[string]time.Duration{}
		}
		a.healthTook[s.Name+"/"+cmd.Name] = res.Duration // a timing sample for experiments' feasibility check
	}
	a.mu.Unlock()
	return st, nil
}

// dependenciesHealthy says whether every dependency in uses was Reachable at
// its last check and has no fault on it.
func (a *App) dependenciesHealthy(uses []string) bool {
	active := a.faults.Active()
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, d := range uses {
		if _, faulted := active[d]; faulted || a.deps[d].State != Reachable {
			return false
		}
	}
	return true
}

func healthStatus(cmd store.Command, res command.Result) Status {
	now := time.Now().UTC()
	switch {
	case res.Err != nil:
		return Status{State: Unreachable, Detail: fmt.Sprintf("could not run %s: %v", cmd.Name, res.Err), CheckedAt: now}
	case res.TimedOut:
		return Status{State: Unreachable, Detail: fmt.Sprintf("%s timed out after %ds", cmd.Name, cmd.TimeoutSec), CheckedAt: now}
	case res.ExitCode != 0:
		detail := fmt.Sprintf("%s exited with code %d", cmd.Name, res.ExitCode)
		if line := lastLine(res.Output); line != "" {
			detail += ": " + line
		}
		return Status{State: Unreachable, Detail: detail, CheckedAt: now}
	}
	return Status{State: Reachable, Detail: cmd.Name + " passed", CheckedAt: now}
}

// lastLine is the last line of output that can say why a command failed. It
// skips a summary table at the end, drawn with box characters, and its
// heading, as Bruno prints them after the request that failed.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !isTableLine(line) && !isSummaryHeading(line) {
			return line
		}
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// isTableLine reports whether line is a border or row of a box-drawn table.
func isTableLine(line string) bool {
	r, _ := utf8.DecodeRuneInString(line)
	return r >= 0x2500 && r <= 0x257F // Unicode box drawing
}

// isSummaryHeading matches a table heading like "📊 Execution Summary".
func isSummaryHeading(line string) bool {
	text := strings.TrimLeftFunc(line, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.HasSuffix(strings.ToLower(text), "summary")
}

// startHealthCommands runs, in the background, the health commands of every
// service where due reports true. A command that's already running is skipped.
func (a *App) startHealthCommands(ctx context.Context, svcs []store.Service, due func(store.Service) bool) {
	for _, s := range svcs {
		if _, ok := healthCommand(s); !ok || !due(s) {
			continue
		}
		go func() {
			if _, err := a.runHealthCommand(ctx, s); err != nil && !errors.Is(err, ErrAlreadyRunning) {
				a.log.Warn("health command", "service", s.Name, "err", err)
			}
		}()
	}
}

// dueByInterval is true for services whose healthEverySec has passed since their last check.
func (a *App) dueByInterval(s store.Service) bool {
	if s.HealthEverySec == 0 {
		return false
	}
	a.mu.RLock()
	last := a.cmdHealth[s.Name].CheckedAt
	a.mu.RUnlock()
	return time.Since(last) >= time.Duration(s.HealthEverySec)*time.Second
}
