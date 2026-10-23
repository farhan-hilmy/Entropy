// Package app ties config, store, Toxiproxy and status checks together.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"entropy/internal/checks"
	"entropy/internal/config"
	"entropy/internal/containerhost"
	"entropy/internal/faults"
	"entropy/internal/store"
	"entropy/internal/toxiproxy"
)

const CheckInterval = 5 * time.Second

// RedetectInterval is how often, at most, an unreachable Toxiproxy at an
// auto-detected host makes Entropy look for the container host again.
const RedetectInterval = 15 * time.Second

// State is a status Entropy can prove. There is deliberately no "degraded".
type State string

const (
	NotChecked  State = "not_checked"
	Reachable   State = "reachable"
	Unreachable State = "unreachable"
	FaultActive State = "fault_active"
)

type Status struct {
	State     State     `json:"state"`
	Detail    string    `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
}

type SyncResult struct {
	At       time.Time      `json:"at"`
	Summary  *store.Summary `json:"summary,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type App struct {
	configPath string
	store      *store.Store
	log        *slog.Logger
	kick       chan struct{}
	editMu     sync.Mutex // one config edit at a time, so each one's hash check holds
	faults     *faults.Engine

	mu       sync.RWMutex
	tp       *toxiproxy.Client
	tpStatus Status
	deps     map[string]Status
	services map[string]Status
	lastSync SyncResult
	running  map[string]bool // "service/command" currently running
	// cmdHealth holds the last health-command result of services that have
	// one. It's kept apart from services, which every check round replaces.
	cmdHealth map[string]Status
	// autoPort is toxiproxy.api's port when its host is auto, else "".
	autoPort   string
	host       *containerhost.Candidate // the detected container host; nil if none answered
	lastDetect time.Time
	// leftoversPending: clearing leftover faults at startup failed, so the
	// first check round that reaches Toxiproxy does it.
	leftoversPending bool
}

func New(configPath string, st *store.Store, log *slog.Logger) *App {
	a := &App{
		configPath: configPath,
		store:      st,
		log:        log,
		kick:       make(chan struct{}, 1),
		tp:         toxiproxy.New(config.DefaultToxiproxyAPI),
		tpStatus:   Status{State: NotChecked},
		deps:       map[string]Status{},
		services:   map[string]Status{},
		running:    map[string]bool{},
		cmdHealth:  map[string]Status{},
	}
	a.faults = faults.New(a.client, log)
	a.faults.OnRemoved = func(string) { a.CheckSoon() }
	return a
}

// Sync reloads the config file, reconciles it into the store and makes sure
// every dependency has a proxy in Toxiproxy. An invalid file changes nothing.
func (a *App) Sync(ctx context.Context) SyncResult {
	res := SyncResult{At: time.Now().UTC()}
	defer func() {
		a.mu.Lock()
		a.lastSync = res
		a.mu.Unlock()
		a.CheckSoon()
	}()

	cfg, err := config.Load(a.configPath)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if w := a.resolveHost(ctx, cfg); w != "" {
		res.Warnings = append(res.Warnings, w)
	}
	sum, err := a.store.Sync(ctx, cfg, a.configPath)
	if err != nil {
		res.Error = "saving to the database: " + err.Error()
		return res
	}
	res.Summary = &sum

	tp := toxiproxy.New(cfg.Toxiproxy.API)
	a.mu.Lock()
	a.tp = tp
	a.mu.Unlock()
	res.Warnings = append(res.Warnings, ensureProxies(ctx, tp, cfg.Dependencies)...)

	// Every sync refreshes health commands, so a config change shows a current result.
	if svcs, err := a.store.Services(ctx); err == nil {
		a.startHealthCommands(context.WithoutCancel(ctx), svcs, func(store.Service) bool { return true })
	}
	return res
}

// resolveHost replaces the auto host in cfg with the container host, detected
// when toxiproxy.api is auto. When no host answers it falls back to localhost
// and returns a warning; the check loop keeps looking (see redetect).
func (a *App) resolveHost(ctx context.Context, cfg *config.Config) (warning string) {
	port, auto := cfg.AutoAPIPort()
	if !auto {
		cfg.SetContainerHost(cfg.APIHost())
		a.mu.Lock()
		a.autoPort, a.host = "", nil
		a.mu.Unlock()
		return ""
	}
	var found *containerhost.Candidate
	host := "localhost"
	if c, err := containerhost.Find(ctx, port, containerhost.Candidates(ctx)); err != nil {
		warning = err.Error() + ". Using localhost; Entropy keeps looking while Toxiproxy is unreachable."
	} else {
		found, host = &c, c.Host
		a.log.Info("container host detected", "host", c.Host, "via", c.Source)
	}
	cfg.SetContainerHost(host)
	a.mu.Lock()
	a.autoPort, a.host, a.lastDetect = port, found, time.Now()
	a.mu.Unlock()
	return warning
}

// redetect syncs again when Toxiproxy, at an auto-detected host, is
// unreachable or wasn't found on the last sync, and turns up at another host
// or at all: the containers may have started after Entropy, or the Podman
// VM's address changed.
func (a *App) redetect(ctx context.Context, unreachable bool) {
	a.mu.Lock()
	due := a.autoPort != "" && (unreachable || a.host == nil) && time.Since(a.lastDetect) >= RedetectInterval
	if due {
		a.lastDetect = time.Now()
	}
	port, current := a.autoPort, a.host
	a.mu.Unlock()
	if !due {
		return
	}
	c, err := containerhost.Find(ctx, port, containerhost.Candidates(ctx))
	if err != nil || (current != nil && current.Host == c.Host) {
		return
	}
	a.log.Info("Toxiproxy found, syncing", "host", c.Host, "via", c.Source)
	a.Sync(ctx)
}

// ensureProxies creates or updates one Toxiproxy proxy per dependency, named
// after it. Proxies Entropy didn't declare are left alone.
func ensureProxies(ctx context.Context, tp *toxiproxy.Client, deps []config.Dependency) []string {
	existing, err := tp.Proxies(ctx)
	if err != nil {
		return []string{err.Error() + " — proxies were not created"}
	}
	var warnings []string
	for _, d := range deps {
		want := toxiproxy.Proxy{Name: d.Name, Listen: d.Listen, Upstream: d.Upstream, Enabled: true}
		p, ok := existing[d.Name]
		switch {
		case !ok:
			err = tp.CreateProxy(ctx, want)
		case !sameListen(p.Listen, d.Listen) || p.Upstream != d.Upstream:
			want.Enabled = p.Enabled
			err = tp.UpdateProxy(ctx, want)
		default:
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("proxy for %s: %v", d.Name, err))
		}
	}
	return warnings
}

// sameListen treats all wildcard hosts as equal: Toxiproxy reports a proxy
// created with 0.0.0.0:6379 as [::]:6379.
func sameListen(a, b string) bool {
	if a == b {
		return true
	}
	ah, ap, errA := net.SplitHostPort(a)
	bh, bp, errB := net.SplitHostPort(b)
	wildcard := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	return errA == nil && errB == nil && ap == bp && wildcard(ah) && wildcard(bh)
}

func (a *App) client() *toxiproxy.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tp
}

// CheckSoon asks the monitor to run a check round now instead of at the next tick.
func (a *App) CheckSoon() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// Run checks every dependency and service until ctx is done.
func (a *App) Run(ctx context.Context) {
	t := time.NewTicker(CheckInterval)
	defer t.Stop()
	for {
		a.checkAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.kick:
		}
	}
}

func (a *App) checkAll(ctx context.Context) {
	deps, err := a.store.Dependencies(ctx)
	if err != nil {
		a.log.Error("loading dependencies", "err", err)
		return
	}
	svcs, err := a.store.Services(ctx)
	if err != nil {
		a.log.Error("loading services", "err", err)
		return
	}

	tpStatus := Status{State: Reachable, CheckedAt: time.Now().UTC()}
	tp := a.client()
	proxies, tpErr := tp.Proxies(ctx)
	if tpErr == nil && a.clearPendingLeftovers(ctx, tp) {
		proxies, tpErr = tp.Proxies(ctx) // the rest of the round sees the cleared state
	}
	if tpErr != nil {
		tpStatus = Status{State: Unreachable, Detail: tpErr.Error(), CheckedAt: time.Now().UTC()}
	} else {
		proxies = a.restoreProxies(ctx, tp, deps, proxies)
		a.faults.Reapply(ctx, proxies)
	}
	a.redetect(ctx, tpErr != nil)

	var mu sync.Mutex
	depStatus := map[string]Status{}
	svcStatus := map[string]Status{}
	var wg sync.WaitGroup
	for _, d := range deps {
		if d.Missing {
			continue
		}
		wg.Go(func() {
			st := checkDependency(ctx, d, proxies, tpErr)
			mu.Lock()
			depStatus[d.Name] = st
			mu.Unlock()
		})
	}
	a.startHealthCommands(ctx, svcs, a.dueByInterval)
	for _, s := range svcs {
		if _, ok := healthCommand(s); ok || s.Missing {
			continue
		}
		wg.Go(func() {
			st := Status{State: Reachable, CheckedAt: time.Now().UTC()}
			if err := checks.Service(ctx, s.BaseURL, s.HealthCheck); err != nil {
				st = Status{State: Unreachable, Detail: err.Error(), CheckedAt: st.CheckedAt}
			}
			mu.Lock()
			svcStatus[s.Name] = st
			mu.Unlock()
		})
	}
	wg.Wait()

	a.mu.Lock()
	a.tpStatus = tpStatus
	a.deps = depStatus
	a.services = svcStatus
	a.mu.Unlock()
}

// restoreProxies creates the proxies of declared dependencies that Toxiproxy
// lacks, as happens whenever its container restarts: it keeps proxies in
// memory only. Existing proxies are never changed here, only on Sync, so a
// check round can't interfere with a proxy that has a fault on it. Faults
// that were on a recreated proxy are put back by faults.Engine.Reapply.
func (a *App) restoreProxies(ctx context.Context, tp *toxiproxy.Client, deps []store.Dependency, proxies map[string]toxiproxy.Proxy) map[string]toxiproxy.Proxy {
	if proxies == nil {
		proxies = map[string]toxiproxy.Proxy{}
	}
	for _, d := range deps {
		if _, ok := proxies[d.Name]; ok || d.Missing {
			continue
		}
		p := toxiproxy.Proxy{Name: d.Name, Listen: d.Listen, Upstream: d.Upstream, Enabled: true}
		if err := tp.CreateProxy(ctx, p); err != nil {
			a.log.Warn("could not recreate missing proxy", "dependency", d.Name, "err", err)
			continue
		}
		a.log.Info("recreated missing proxy; Toxiproxy was probably restarted", "dependency", d.Name)
		proxies[d.Name] = p
	}
	return proxies
}

func checkDependency(ctx context.Context, d store.Dependency, proxies map[string]toxiproxy.Proxy, tpErr error) Status {
	now := time.Now().UTC()
	if tpErr != nil {
		return Status{State: Unreachable, Detail: "Toxiproxy is not reachable", CheckedAt: now}
	}
	p, ok := proxies[d.Name]
	switch {
	case !ok:
		return Status{State: Unreachable, Detail: "no proxy in Toxiproxy — run Sync", CheckedAt: now}
	case !p.Enabled:
		return Status{State: FaultActive, Detail: "proxy is disabled", CheckedAt: now}
	case len(p.Toxics) > 0:
		names := make([]string, len(p.Toxics))
		for i, t := range p.Toxics {
			names[i] = t.Type
		}
		return Status{State: FaultActive, Detail: "toxics active: " + strings.Join(names, ", "), CheckedAt: now}
	}
	if err := checks.Dependency(ctx, d.Kind, d.Address, d.Redirect); err != nil {
		return Status{State: Unreachable, Detail: err.Error(), CheckedAt: now}
	}
	return Status{State: Reachable, CheckedAt: now}
}

type View struct {
	Name         string           `json:"name"`
	ConfigPath   string           `json:"configPath"`
	SyncedAt     time.Time        `json:"syncedAt,omitzero"`
	Status       string           `json:"status"` // ready | degraded | unreachable | checking
	ActiveFaults int              `json:"activeFaults"`
	Toxiproxy    ToxiproxyView    `json:"toxiproxy"`
	LastSync     SyncResult       `json:"lastSync"`
	Dependencies []DependencyView `json:"dependencies"`
	Services     []ServiceView    `json:"services"`
}

type ToxiproxyView struct {
	API    string `json:"api"`
	Status Status `json:"status"`
	// Auto: toxiproxy.api's host is auto. Detected is the host found then,
	// nil while none answers.
	Auto     bool                     `json:"auto"`
	Detected *containerhost.Candidate `json:"detected,omitempty"`
}

type DependencyView struct {
	store.Dependency
	Status         Status   `json:"status"`
	Ready          bool     `json:"ready"`
	NotReadyReason string   `json:"notReadyReason,omitempty"`
	UsedBy         []string `json:"usedBy"`
	// Fault is the fault Entropy applied to this dependency, until it's removed.
	Fault *faults.Fault `json:"fault,omitempty"`
}

type ServiceView struct {
	store.Service
	Status   Status        `json:"status"`
	Checking bool          `json:"checking"` // its health command is running now
	Commands []CommandView `json:"commands"` // replaces store.Service's commands in the JSON
}

// View assembles everything the UI shows about the environment.
func (a *App) View(ctx context.Context) (View, error) {
	env, err := a.store.Environment(ctx)
	if err != nil {
		return View{}, err
	}
	deps, err := a.store.Dependencies(ctx)
	if err != nil {
		return View{}, err
	}
	svcs, err := a.store.Services(ctx)
	if err != nil {
		return View{}, err
	}
	recent, err := a.store.RecentCommandRuns(ctx, recentRunsPerCommand)
	if err != nil {
		return View{}, err
	}

	a.mu.RLock()
	depStatus := maps.Clone(a.deps)
	svcStatus := maps.Clone(a.services)
	cmdHealth := maps.Clone(a.cmdHealth)
	running := maps.Clone(a.running)
	tpStatus := a.tpStatus
	tpView := ToxiproxyView{API: a.tp.Base(), Status: tpStatus, Auto: a.autoPort != "", Detected: a.host}
	lastSync := a.lastSync
	a.mu.RUnlock()

	v := View{
		Name:         env.Name,
		ConfigPath:   env.ConfigPath,
		SyncedAt:     env.SyncedAt,
		Toxiproxy:    tpView,
		LastSync:     lastSync,
		Dependencies: []DependencyView{},
		Services:     []ServiceView{},
	}

	usedBy := map[string][]string{}
	var states []State
	for _, s := range svcs {
		st := statusOr(svcStatus, s.Name)
		checking := false
		if hc, ok := healthCommand(s); ok {
			st = statusOr(cmdHealth, s.Name)
			checking = running[s.Name+"/"+hc.Name]
		}
		if !s.Missing {
			states = append(states, st.State)
			for _, dep := range s.Uses {
				usedBy[dep] = append(usedBy[dep], s.Name)
			}
		}
		v.Services = append(v.Services, ServiceView{Service: s, Status: st, Checking: checking, Commands: commandViews(s, running, recent)})
	}
	active := a.faults.Active()
	for _, d := range deps {
		st := statusOr(depStatus, d.Name)
		dv := DependencyView{Dependency: d, Status: st, UsedBy: usedBy[d.Name]}
		if dv.UsedBy == nil {
			dv.UsedBy = []string{}
		}
		if f, ok := active[d.Name]; ok {
			dv.Fault = &f
			// Shown at once, not after the next check round, and also while a flap has the proxy up.
			st = faultStatus(f, st)
			dv.Status = st
		}
		dv.Ready, dv.NotReadyReason = readiness(d, st)
		if !d.Missing {
			states = append(states, st.State)
			if st.State == FaultActive {
				v.ActiveFaults++
			}
		}
		v.Dependencies = append(v.Dependencies, dv)
	}
	v.Status = environmentStatus(tpStatus.State, states)
	return v, nil
}

func faultStatus(f faults.Fault, checked Status) Status {
	detail := f.Type + " fault from Entropy"
	if f.Removing {
		detail = f.Type + " fault ended; removing it"
		if f.Error != "" {
			detail += " (last try: " + f.Error + ")"
		}
	}
	return Status{State: FaultActive, Detail: detail, CheckedAt: checked.CheckedAt}
}

func statusOr(m map[string]Status, name string) Status {
	if st, ok := m[name]; ok {
		return st
	}
	return Status{State: NotChecked}
}

// readiness: the check through the proxy passes and nothing else is using the
// dependency. Until experiments exist, that means no fault is on it.
func readiness(d store.Dependency, st Status) (bool, string) {
	if d.Missing {
		return false, "Removed from the config file"
	}
	switch st.State {
	case Reachable:
		return true, ""
	case FaultActive:
		return false, "A fault is active"
	case NotChecked:
		return false, "Not checked yet"
	}
	return false, "Check failed: " + st.Detail
}

func environmentStatus(tp State, states []State) string {
	switch {
	case tp == Unreachable:
		return "unreachable"
	case tp == NotChecked || slices.Contains(states, NotChecked):
		return "checking"
	case slices.ContainsFunc(states, func(s State) bool { return s != Reachable }):
		return "degraded"
	}
	return "ready"
}
