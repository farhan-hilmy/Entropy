package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client calls a running Entropy's REST API (the one entropy up serves).
type Client struct {
	base string
	http *http.Client

	// Starter, if set, starts Entropy when nothing answers at base.
	Starter *Starter
	mu      sync.Mutex
	notice  string // said once in the next tool result, e.g. that Entropy was started
}

// TakeNotice returns the pending notice, once.
func (c *Client) TakeNotice() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.notice
	c.notice = ""
	return n
}

// NewClient talks to the Entropy at base, e.g. http://127.0.0.1:7400.
func NewClient(base string) *Client {
	// Requests that wait for a command or a stop answer when it's done; the
	// longest (a command's timeoutSec) is bounded by Entropy itself.
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 15 * time.Minute}}
}

// APIError is an error answer from Entropy, with its HTTP status.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// notRunning is returned when nothing answers at the URL, with what to do.
type notRunning struct {
	base string
	err  error
}

func (e *notRunning) Error() string {
	return fmt.Sprintf("Entropy isn't running at %s (%v). Ask the user to start it with `entropy up` in the folder with entropy.yaml, then try again.", e.base, e.err)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	send := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.http.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) && ctx.Err() == nil && refused(ue.Err) {
				return nil, &notRunning{c.base, ue.Err}
			}
			return nil, err
		}
		return res, nil
	}
	res, err := send()
	var nr *notRunning
	if errors.As(err, &nr) && c.Starter != nil {
		started, serr := c.Starter.Ensure(ctx, c.base)
		if serr != nil {
			return fmt.Errorf("Entropy isn't running at %s, and starting it failed: %w", c.base, serr)
		}
		if started {
			c.mu.Lock()
			c.notice = fmt.Sprintf("(Entropy wasn't running, so it was started for this session with %s; it stops when the session ends. UI: %s)", c.Starter.Config, c.base)
			c.mu.Unlock()
		}
		res, err = send()
	}
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
			if e.Error == "" {
				e.Error = res.Status
			}
		}
		return &APIError{Status: res.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		*s = string(data)
		return nil
	}
	return json.Unmarshal(data, out)
}

// refused reports whether err is from connecting (nothing listens there), as
// opposed to an Entropy that answered slowly or broke off.
func refused(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func esc(s string) string { return url.PathEscape(s) }

func (c *Client) Environment(ctx context.Context) (Environment, error) {
	var v Environment
	return v, c.do(ctx, "GET", "/api/environment", nil, &v)
}

func (c *Client) Experiments(ctx context.Context) ([]Experiment, error) {
	var v []Experiment
	return v, c.do(ctx, "GET", "/api/experiments", nil, &v)
}

func (c *Client) ExperimentSource(ctx context.Context, name string) (Source, error) {
	var v Source
	return v, c.do(ctx, "GET", "/api/experiments/"+esc(name)+"/source", nil, &v)
}

// Config is the config file as written (no defaults), and its hash.
func (c *Client) Config(ctx context.Context) (ConfigSnapshot, error) {
	var v ConfigSnapshot
	return v, c.do(ctx, "GET", "/api/config", nil, &v)
}

// Edit applies one config edit (one key: experiment, addDependency, …) to
// the file with this hash, as the UI does (PATCH /api/config).
func (c *Client) Edit(ctx context.Context, hash, part string, value any) (EditResult, error) {
	var v EditResult
	return v, c.do(ctx, "PATCH", "/api/config", map[string]any{"hash": hash, part: value}, &v)
}

// VerifyRoute runs one of the service's requests (command "" = its health
// check) and reports whether it went through the dependency's proxy.
func (c *Client) VerifyRoute(ctx context.Context, dependency, service, command string) (RouteCheck, error) {
	var v RouteCheck
	body := map[string]string{"service": service, "command": command}
	return v, c.do(ctx, "POST", "/api/dependencies/"+esc(dependency)+"/verify", body, &v)
}

func (c *Client) StartRun(ctx context.Context, experiment string) (Run, error) {
	var v Run
	return v, c.do(ctx, "POST", "/api/experiments/"+esc(experiment)+"/run", nil, &v)
}

func (c *Client) Runs(ctx context.Context, experiment string) ([]Run, error) {
	var v []Run
	path := "/api/runs"
	if experiment != "" {
		path += "?experiment=" + url.QueryEscape(experiment)
	}
	return v, c.do(ctx, "GET", path, nil, &v)
}

func (c *Client) Run(ctx context.Context, id string) (Run, error) {
	var v Run
	return v, c.do(ctx, "GET", "/api/runs/"+esc(id), nil, &v)
}

func (c *Client) StopRun(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/api/runs/"+esc(id)+"/stop", nil, nil)
}

func (c *Client) StopAllFaults(ctx context.Context) error {
	return c.do(ctx, "POST", "/api/faults/stop-all", nil, nil)
}

func (c *Client) RunCommand(ctx context.Context, service, command string) (CommandRun, error) {
	var v CommandRun
	return v, c.do(ctx, "POST", "/api/services/"+esc(service)+"/commands/"+esc(command)+"/run", nil, &v)
}

// The API's JSON, as far as the tools use it (web/src/api.ts has all of it).

type Status struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type Environment struct {
	Name         string `json:"name"`
	ActiveRun    string `json:"activeRun"`
	ConfigPath   string `json:"configPath"`
	Status       string `json:"status"`
	ActiveFaults int    `json:"activeFaults"`
	Toxiproxy    struct {
		API     string `json:"api"`
		Status  Status `json:"status"`
		Builtin bool   `json:"builtin"`
	} `json:"toxiproxy"`
	LastSync struct {
		Warnings []string `json:"warnings"`
		Error    string   `json:"error"`
	} `json:"lastSync"`
	Dependencies []Dependency `json:"dependencies"`
	Services     []Service    `json:"services"`
}

type Dependency struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Address        string   `json:"address"`
	Upstream       string   `json:"upstream"`
	Missing        bool     `json:"missing"`
	Status         Status   `json:"status"`
	Ready          bool     `json:"ready"`
	NotReadyReason string   `json:"notReadyReason"`
	UsedBy         []string `json:"usedBy"`
	Fault          *Fault   `json:"fault"`
}

type Fault struct {
	Type        string    `json:"type"`
	DurationSec int       `json:"durationSec"`
	LatencyMs   int       `json:"latencyMs"`
	EndsAt      time.Time `json:"endsAt"`
}

type Service struct {
	Name          string    `json:"name"`
	BaseURL       string    `json:"baseUrl"`
	HealthCheck   string    `json:"healthCheck"`
	HealthCommand string    `json:"healthCommand"`
	Uses          []string  `json:"uses"`
	Commands      []Command `json:"commands"`
	Missing       bool      `json:"missing"`
	Status        Status    `json:"status"`
}

type Command struct {
	Name       string `json:"name"`
	Run        string `json:"run"`
	TimeoutSec int    `json:"timeoutSec"`
}

type CommandRun struct {
	ID         string `json:"id"`
	Service    string `json:"service"`
	Command    string `json:"command"`
	Run        string `json:"run"`
	DurationMs int64  `json:"durationMs"`
	ExitCode   int    `json:"exitCode"`
	TimedOut   bool   `json:"timedOut"`
	Error      string `json:"error"`
	Output     string `json:"output"`
}

type Definition struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	MaxDurationSec int    `json:"maxDurationSec"`
	Faults         []struct {
		Dependency     string `json:"dependency"`
		Type           string `json:"type"`
		DurationSec    int    `json:"durationSec"`
		StartOffsetSec int    `json:"startOffsetSec"`
	} `json:"faults"`
	Assertions []struct {
		Type    string `json:"type"`
		Trigger string `json:"trigger"`
		Service string `json:"service"`
	} `json:"assertions"`
}

type Experiment struct {
	Name       string     `json:"name"`
	Version    int        `json:"version"`
	Definition Definition `json:"definition"`
	Missing    bool       `json:"missing"`
	LastRun    *Run       `json:"lastRun"`
	Running    bool       `json:"running"`
	Timing     struct {
		Measured          string   `json:"measured"`
		Warnings          []string `json:"warnings"`
		Note              string   `json:"note"`
		MinMaxDurationSec int      `json:"minMaxDurationSec"`
	} `json:"timing"`
}

type Run struct {
	ID         string     `json:"id"`
	Experiment string     `json:"experiment"`
	Version    int        `json:"version"`
	RerunOf    string     `json:"rerunOf"`
	State      string     `json:"state"`
	Verdict    string     `json:"verdict"`
	Reason     string     `json:"reason"`
	StartedAt  time.Time  `json:"startedAt"`
	EndedAt    *time.Time `json:"endedAt"`
	Snapshot   *struct {
		Experiment Definition `json:"experiment"`
	} `json:"snapshot"`
	Events []struct {
		At      time.Time `json:"at"`
		Type    string    `json:"type"`
		Message string    `json:"message"`
	} `json:"events"`
	Assertions []struct {
		Index   int    `json:"index"`
		Outcome string `json:"outcome"`
		Detail  string `json:"detail"`
	} `json:"assertions"`
}

// Done reports whether the run has finished (its state is "done").
func (r Run) Done() bool { return r.State == "done" }

type Source struct {
	Hash string `json:"hash"`
	YAML string `json:"yaml"`
}

// ConfigSnapshot is the config file as written: no defaults, so a field the
// file leaves out is empty.
type ConfigSnapshot struct {
	Hash   string `json:"hash"`
	Config struct {
		Dependencies []RawDependency `json:"dependencies"`
		Services     []RawService    `json:"services"`
	} `json:"config"`
}

type RawDependency struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Address  string `json:"address"`
	Upstream string `json:"upstream"`
	Listen   string `json:"listen"`
	Redirect bool   `json:"redirect"`
}

type RawService struct {
	Name           string   `json:"name"`
	BaseURL        string   `json:"baseUrl"`
	HealthCheck    string   `json:"healthCheck"`
	HealthCommand  string   `json:"healthCommand"`
	HealthEverySec int      `json:"healthEverySec"`
	Uses           []string `json:"uses"`
}

type RouteCheck struct {
	State     string `json:"state"` // verified | not_seen | unknown
	Detail    string `json:"detail"`
	Trigger   string `json:"trigger"`
	NormalMs  int64  `json:"normalMs"`
	SlowedMs  int64  `json:"slowedMs"`
	LatencyMs int64  `json:"latencyMs"`
}

type EditResult struct {
	Hash string `json:"hash"`
	Sync struct {
		Warnings []string `json:"warnings"`
		Error    string   `json:"error"`
	} `json:"sync"`
}
