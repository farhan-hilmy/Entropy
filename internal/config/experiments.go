package config

import (
	"fmt"
	"slices"
	"strings"

	"entropy/internal/faults"
)

// Experiment breaks one or more dependencies on purpose, sends requests to
// services before, during and after, and checks how they behaved.
type Experiment struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// MaxDurationSec is a safety cap: a run still going after this is aborted
	// and every fault removed.
	MaxDurationSec int               `yaml:"maxDurationSec,omitempty" json:"maxDurationSec"`
	Faults         []ExperimentFault `yaml:"faults,omitempty" json:"faults"`
	Triggers       []Trigger         `yaml:"triggers" json:"triggers"`
	// RecoveryWindowSec is how long the run keeps watching after the faults
	// are removed; the "after" triggers run in it.
	RecoveryWindowSec int         `yaml:"recoveryWindowSec,omitempty" json:"recoveryWindowSec"`
	Assertions        []Assertion `yaml:"assertions" json:"assertions"`
}

// ExperimentFault is a fault from the Fault panel, started StartOffsetSec
// after the baseline phase.
type ExperimentFault struct {
	Dependency     string `yaml:"dependency" json:"dependency"`
	faults.Spec    `yaml:",inline"`
	StartOffsetSec int `yaml:"startOffsetSec,omitempty" json:"startOffsetSec"`
}

// Trigger is a request sent to a service in one phase of a run: one of the
// service's commands ("service/command"), or an HTTP request to its baseUrl.
type Trigger struct {
	Name    string   `yaml:"name" json:"name"`
	Phase   string   `yaml:"phase" json:"phase"`
	Command string   `yaml:"command,omitempty" json:"command,omitempty"`
	Service string   `yaml:"service,omitempty" json:"service,omitempty"` // with request; derived from command
	Request *Request `yaml:"request,omitempty" json:"request,omitempty"`
	Repeat  int      `yaml:"repeat,omitempty" json:"repeat"`
	// EverySec is the time between repeats, from one start to the next.
	EverySec int `yaml:"everySec,omitempty" json:"everySec"`
}

type Request struct {
	Method  string            `yaml:"method,omitempty" json:"method"`
	Path    string            `yaml:"path" json:"path"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// Body is sent as JSON, or as is when it's a string.
	Body any `yaml:"body,omitempty" json:"body,omitempty"`
}

// Assertion is what the service should have done. response: every result of
// the trigger matches; count: at least AtLeast do; eventual: one does within
// WithinSec of the trigger's phase starting; recovery: the service's health
// check passes again within WithinSec of the faults ending.
type Assertion struct {
	Type      string `yaml:"type" json:"type"`
	Trigger   string `yaml:"trigger,omitempty" json:"trigger,omitempty"`
	Service   string `yaml:"service,omitempty" json:"service,omitempty"` // recovery; derived when the triggers use one service
	Expect    Expect `yaml:"expect,omitempty" json:"expect"`
	AtLeast   int    `yaml:"atLeast,omitempty" json:"atLeast,omitempty"`
	WithinSec int    `yaml:"withinSec,omitempty" json:"withinSec,omitempty"`
}

// Expect is what one result must match. Commands have an exit code and
// output; requests a status and body.
type Expect struct {
	ExitCode       *int   `yaml:"exitCode,omitempty" json:"exitCode,omitempty"`
	OutputContains string `yaml:"outputContains,omitempty" json:"outputContains,omitempty"`
	Status         int    `yaml:"status,omitempty" json:"status,omitempty"`
	BodyContains   string `yaml:"bodyContains,omitempty" json:"bodyContains,omitempty"`
}

var (
	Phases         = []string{"baseline", "during", "after"}
	AssertionTypes = []string{"response", "count", "eventual", "recovery"}
	httpMethods    = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}
)

const (
	DefaultExperimentMaxSec  = 600
	MaxExperimentSec         = 3600
	DefaultRecoveryWindowSec = 30
	MaxRecoveryWindowSec     = 600
	MaxTriggerRepeat         = 100
	MaxTriggerEverySec       = 600
	DefaultRecoveryWithinSec = 30
)

// ServiceOf is the service a trigger sends to.
func (t Trigger) ServiceOf() string {
	if t.Command != "" {
		svc, _, _ := strings.Cut(t.Command, "/")
		return svc
	}
	return t.Service
}

// CommandName is the command part of "service/command", or "".
func (t Trigger) CommandName() string {
	_, cmd, _ := strings.Cut(t.Command, "/")
	return cmd
}

func (e *Experiment) applyDefaults() {
	if e.MaxDurationSec == 0 {
		e.MaxDurationSec = DefaultExperimentMaxSec
	}
	if e.RecoveryWindowSec == 0 {
		e.RecoveryWindowSec = DefaultRecoveryWindowSec
	}
	for i := range e.Faults {
		if spec, err := e.Faults[i].Spec.WithDefaults(); err == nil {
			e.Faults[i].Spec = spec
		}
	}
	services := map[string]bool{}
	for i := range e.Triggers {
		t := &e.Triggers[i]
		if t.Repeat == 0 {
			t.Repeat = 1
		}
		if t.Request != nil {
			t.Request.Method = strings.ToUpper(t.Request.Method)
			if t.Request.Method == "" {
				t.Request.Method = "GET"
			}
		}
		if s := t.ServiceOf(); s != "" {
			services[s] = true
		}
	}
	for i := range e.Assertions {
		a := &e.Assertions[i]
		if a.Type != "recovery" {
			continue
		}
		if a.WithinSec == 0 {
			a.WithinSec = DefaultRecoveryWithinSec
		}
		if a.Service == "" && len(services) == 1 {
			for s := range services {
				a.Service = s
			}
		}
	}
}

func (v *validator) experiment(position string, e Experiment, seen, deps map[string]bool, services map[string]Service) {
	label := "experiment " + v.name("experiment", position, e.Name, seen)
	if e.MaxDurationSec < 1 || e.MaxDurationSec > MaxExperimentSec {
		v.bad("%s: maxDurationSec must be between 1 and %d", label, MaxExperimentSec)
	}
	if e.RecoveryWindowSec < 0 || e.RecoveryWindowSec > MaxRecoveryWindowSec {
		v.bad("%s: recoveryWindowSec must be between 0 and %d", label, MaxRecoveryWindowSec)
	}

	faulted := map[string]bool{}
	for i, f := range e.Faults {
		fl := fmt.Sprintf("%s: faults[%d]", label, i)
		switch {
		case !deps[f.Dependency]:
			v.bad("%s: dependency %q: no such dependency", fl, f.Dependency)
		case faulted[f.Dependency]:
			v.bad("%s: %s already has a fault in this experiment; a dependency has one at a time", fl, f.Dependency)
		}
		faulted[f.Dependency] = true
		if _, err := f.Spec.WithDefaults(); err != nil {
			v.bad("%s: %s", fl, strings.TrimPrefix(err.Error(), faults.ErrInvalid.Error()+": "))
		}
		if f.StartOffsetSec < 0 {
			v.bad("%s: startOffsetSec can't be negative", fl)
		}
	}

	if len(e.Triggers) == 0 {
		v.bad("%s: needs at least one trigger", label)
	}
	triggers := map[string]Trigger{}
	for i, t := range e.Triggers {
		tl := fmt.Sprintf("%s: triggers[%d]", label, i)
		if t.Name != "" {
			tl = fmt.Sprintf("%s: trigger %s", label, t.Name)
		}
		v.trigger(tl, t, triggers, services)
		if t.Name != "" {
			triggers[t.Name] = t
		}
	}

	if len(e.Assertions) == 0 {
		v.bad("%s: needs at least one assertion; the verdict comes from them", label)
	}
	for i, a := range e.Assertions {
		al := fmt.Sprintf("%s: assertions[%d]", label, i)
		v.assertion(al, a, triggers, services)
		// After the faults, the run only watches for recoveryWindowSec.
		afterPhase := a.Type == "recovery" || (a.Type == "eventual" && triggers[a.Trigger].Phase == "after")
		if afterPhase && a.WithinSec > e.RecoveryWindowSec {
			v.bad("%s: withinSec %d is longer than recoveryWindowSec %d, so it could never be judged", al, a.WithinSec, e.RecoveryWindowSec)
		}
	}
}

func (v *validator) trigger(label string, t Trigger, seen map[string]Trigger, services map[string]Service) {
	switch {
	case t.Name == "":
		v.bad("%s: name is required", label)
	case !namePattern.MatchString(t.Name):
		v.bad("%s: name may only contain letters, digits, '.', '_' and '-'", label)
	default:
		if _, dup := seen[t.Name]; dup {
			v.bad("%s: declared twice", label)
		}
	}
	if !slices.Contains(Phases, t.Phase) {
		v.bad("%s: phase %q is not one of %s", label, t.Phase, strings.Join(Phases, ", "))
	}
	switch {
	case (t.Command == "") == (t.Request == nil):
		v.bad("%s: needs exactly one of command (service/command) or request", label)
	case t.Command != "":
		svc, cmd, ok := strings.Cut(t.Command, "/")
		s, known := services[svc]
		switch {
		case !ok || svc == "" || cmd == "":
			v.bad("%s: command %q must be service/command", label, t.Command)
		case !known:
			v.bad("%s: command %q: no service %s", label, t.Command, svc)
		case !slices.ContainsFunc(s.Commands, func(c Command) bool { return c.Name == cmd }):
			v.bad("%s: command %q: %s has no command %s", label, t.Command, svc, cmd)
		}
		if t.Service != "" && t.Service != svc {
			v.bad("%s: service %q doesn't match command %q; leave service out", label, t.Service, t.Command)
		}
	default:
		if _, known := services[t.Service]; !known {
			v.bad("%s: a request needs service, one of the services (got %q)", label, t.Service)
		}
		if !strings.HasPrefix(t.Request.Path, "/") {
			v.bad("%s: request path %q must start with /", label, t.Request.Path)
		}
		if !slices.Contains(httpMethods, t.Request.Method) {
			v.bad("%s: request method %q is not one of %s", label, t.Request.Method, strings.Join(httpMethods, ", "))
		}
	}
	if t.Repeat < 1 || t.Repeat > MaxTriggerRepeat {
		v.bad("%s: repeat must be between 1 and %d", label, MaxTriggerRepeat)
	}
	if t.EverySec < 0 || t.EverySec > MaxTriggerEverySec {
		v.bad("%s: everySec must be between 0 and %d", label, MaxTriggerEverySec)
	}
}

func (v *validator) assertion(label string, a Assertion, triggers map[string]Trigger, services map[string]Service) {
	if !slices.Contains(AssertionTypes, a.Type) {
		v.bad("%s: type %q is not one of %s", label, a.Type, strings.Join(AssertionTypes, ", "))
		return
	}
	if a.Type == "recovery" {
		if a.Trigger != "" || a.Expect != (Expect{}) || a.AtLeast != 0 {
			v.bad("%s: recovery uses the service's health check; only service and withinSec apply", label)
		}
		switch _, known := services[a.Service]; {
		case a.Service == "":
			v.bad("%s: recovery needs service: the triggers use more than one", label)
		case !known:
			v.bad("%s: no service %s", label, a.Service)
		}
		if a.WithinSec < 1 {
			v.bad("%s: withinSec must be at least 1", label)
		}
		return
	}

	t, known := triggers[a.Trigger]
	if !known {
		v.bad("%s: trigger %q: no such trigger in this experiment", label, a.Trigger)
		return
	}
	if a.Service != "" {
		v.bad("%s: service only applies to recovery", label)
	}
	e := a.Expect
	switch {
	case e == (Expect{}):
		v.bad("%s: expect needs at least one of exitCode, outputContains (commands) or status, bodyContains (requests)", label)
	case t.Command != "" && (e.Status != 0 || e.BodyContains != ""):
		v.bad("%s: trigger %s runs a command: expect exitCode or outputContains, not status or bodyContains", label, t.Name)
	case t.Request != nil && (e.ExitCode != nil || e.OutputContains != ""):
		v.bad("%s: trigger %s sends a request: expect status or bodyContains, not exitCode or outputContains", label, t.Name)
	}
	switch a.Type {
	case "count":
		if a.AtLeast < 1 || a.AtLeast > t.Repeat {
			v.bad("%s: atLeast must be between 1 and the trigger's repeat (%d)", label, t.Repeat)
		}
	case "eventual":
		if a.WithinSec < 1 {
			v.bad("%s: withinSec must be at least 1", label)
		}
	}
	if a.Type != "count" && a.AtLeast != 0 {
		v.bad("%s: atLeast only applies to count", label)
	}
	if a.Type == "response" && a.WithinSec != 0 {
		v.bad("%s: withinSec doesn't apply to response", label)
	}
}
