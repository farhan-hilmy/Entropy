package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"entropy/internal/command"
	"entropy/internal/store"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrAlreadyRunning = errors.New("this command is already running")
)

const recentRunsPerCommand = 5

// CommandEnv is the environment a command gets on top of Entropy's own, so a
// command can target the service without repeating its URL.
func CommandEnv(service, baseURL string) []string {
	return []string{"ENTROPY_SERVICE=" + service, "ENTROPY_BASE_URL=" + baseURL}
}

// RunCommand runs one of a service's commands and records the result.
// The same command never runs twice at once.
func (a *App) RunCommand(ctx context.Context, service, name string) (store.CommandRun, error) {
	svc, cmd, err := a.findCommand(ctx, service, name)
	if err != nil {
		return store.CommandRun{}, err
	}

	release, ok := a.claim(service, name)
	if !ok {
		return store.CommandRun{}, ErrAlreadyRunning
	}
	defer release()

	started := time.Now()
	res := command.Run(ctx, cmd.Run, cmd.Cwd, time.Duration(cmd.TimeoutSec)*time.Second, CommandEnv(svc.Name, svc.BaseURL))
	run := store.CommandRun{
		Service:    svc.Name,
		Command:    cmd.Name,
		Run:        cmd.Run,
		StartedAt:  started.UTC(),
		DurationMs: res.Duration.Milliseconds(),
		ExitCode:   res.ExitCode,
		TimedOut:   res.TimedOut,
		Output:     res.Output,
	}
	if res.Err != nil {
		run.Error = res.Err.Error()
	}
	// Record the run even if the request that started it has gone away.
	if err := a.store.SaveCommandRun(context.WithoutCancel(ctx), &run); err != nil {
		return run, fmt.Errorf("saving the run: %w", err)
	}
	return run, nil
}

// claim marks a command as running, so the same command (a manual run or a
// health check) never runs twice at once. ok is false if it's already running.
func (a *App) claim(service, name string) (release func(), ok bool) {
	key := service + "/" + name
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running[key] {
		return nil, false
	}
	a.running[key] = true
	return func() {
		a.mu.Lock()
		delete(a.running, key)
		a.mu.Unlock()
	}, true
}

// claimWhenFree is claim, waiting while the command runs (e.g. a health check
// started by Sync), for up to wait. ok is false if it's still running then,
// or ctx ended.
func (a *App) claimWhenFree(ctx context.Context, service, name string, wait time.Duration) (release func(), ok bool) {
	deadline := time.Now().Add(wait)
	for {
		if release, ok := a.claim(service, name); ok {
			return release, true
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return nil, false
		}
	}
}

func (a *App) findCommand(ctx context.Context, service, name string) (store.Service, store.Command, error) {
	svcs, err := a.store.Services(ctx)
	if err != nil {
		return store.Service{}, store.Command{}, err
	}
	for _, s := range svcs {
		if s.Name != service || s.Missing {
			continue
		}
		for _, c := range s.Commands {
			if c.Name == name {
				return s, c, nil
			}
		}
	}
	return store.Service{}, store.Command{}, fmt.Errorf("%w: no command %s/%s in the config file", ErrNotFound, service, name)
}

// CommandRun returns one recorded run with its full output.
func (a *App) CommandRun(ctx context.Context, id string) (store.CommandRun, error) {
	run, err := a.store.CommandRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return run, fmt.Errorf("%w: no run %s", ErrNotFound, id)
	}
	return run, err
}

type CommandView struct {
	store.Command
	Running bool               `json:"running"`
	LastRun *store.CommandRun  `json:"lastRun,omitempty"`
	Recent  []store.CommandRun `json:"recent"`
}

func commandViews(s store.Service, running map[string]bool, recent map[string][]store.CommandRun) []CommandView {
	out := make([]CommandView, 0, len(s.Commands))
	for _, c := range s.Commands {
		key := s.Name + "/" + c.Name
		cv := CommandView{Command: c, Running: running[key], Recent: recent[key]}
		if cv.Recent == nil {
			cv.Recent = []store.CommandRun{}
		}
		if len(cv.Recent) > 0 {
			cv.LastRun = &cv.Recent[0]
		}
		out = append(out, cv)
	}
	return out
}
