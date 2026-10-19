// Package config loads and validates entropy.yaml, the file that registers
// the environment's services and dependencies.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kinds are the dependency types Entropy knows how to check.
var Kinds = []string{"redis", "mongodb", "postgres", "rabbitmq", "http", "https"}

const DefaultToxiproxyAPI = "http://localhost:8474"

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type Config struct {
	Environment  Environment  `yaml:"environment"`
	Toxiproxy    Toxiproxy    `yaml:"toxiproxy"`
	Dependencies []Dependency `yaml:"dependencies"`
	Services     []Service    `yaml:"services"`
}

type Environment struct {
	Name string `yaml:"name"`
}

type Toxiproxy struct {
	API string `yaml:"api"`
}

type Dependency struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
	// Address is where services, and Entropy's checks, reach the dependency
	// through the proxy, e.g. localhost:6379.
	Address string `yaml:"address"`
	// Upstream is the real dependency as Toxiproxy sees it, e.g. redis:6379.
	Upstream string `yaml:"upstream"`
	// Listen is the proxy's listen address inside Toxiproxy.
	// Defaults to 0.0.0.0 with the port from Address.
	Listen string `yaml:"listen"`
	// Redirect (https only) means the hostname is pointed at Toxiproxy through
	// the hosts file, so the check also verifies it resolves to loopback.
	Redirect bool `yaml:"redirect"`
}

type Service struct {
	Name        string `yaml:"name"`
	BaseURL     string `yaml:"baseUrl"`
	HealthCheck string `yaml:"healthCheck"`
	// HealthCommand names one of Commands to use as the health check instead:
	// exit code 0 means Reachable.
	HealthCommand string `yaml:"healthCommand"`
	// HealthEverySec re-runs HealthCommand in the background. 0, the default,
	// runs it only on Sync, on "Check now" and before experiments, because a
	// command can be slow and have side effects (e.g. issuing tokens).
	HealthEverySec int       `yaml:"healthEverySec"`
	Uses           []string  `yaml:"uses"`
	Commands       []Command `yaml:"commands"`
}

// Command is a command line that exercises the service, e.g. a `bru run`.
// Exit code 0 means the request behaved as expected.
type Command struct {
	Name string `yaml:"name"`
	// Run is the command line as you'd type it: cmd.exe runs it on Windows, sh elsewhere.
	Run string `yaml:"run"`
	// Cwd is the working directory. Relative paths are resolved against the
	// config file's folder, which is also the default.
	Cwd        string `yaml:"cwd"`
	TimeoutSec int    `yaml:"timeoutSec"`
}

const (
	DefaultCommandTimeoutSec = 30
	MaxCommandTimeoutSec     = 600
	MinHealthEverySec        = 15
	MaxHealthEverySec        = 86400
)

// Load reads, defaults and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: file is empty", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.applyDefaults(filepath.Dir(path))
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s:\n%w", path, err)
	}
	return &cfg, nil
}

// applyDefaults fills in optional fields; dir is the config file's folder.
func (c *Config) applyDefaults(dir string) {
	if c.Environment.Name == "" {
		c.Environment.Name = "local"
	}
	if c.Toxiproxy.API == "" {
		c.Toxiproxy.API = DefaultToxiproxyAPI
	}
	for i := range c.Dependencies {
		d := &c.Dependencies[i]
		if d.Listen == "" {
			if _, port, err := net.SplitHostPort(d.Address); err == nil {
				d.Listen = net.JoinHostPort("0.0.0.0", port)
			}
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for i := range c.Services {
		for j := range c.Services[i].Commands {
			cmd := &c.Services[i].Commands[j]
			if cmd.TimeoutSec == 0 {
				cmd.TimeoutSec = DefaultCommandTimeoutSec
			}
			switch {
			case cmd.Cwd == "":
				cmd.Cwd = dir
			case !filepath.IsAbs(cmd.Cwd):
				cmd.Cwd = filepath.Join(dir, cmd.Cwd)
			}
			cmd.Cwd = filepath.Clean(cmd.Cwd)
		}
	}
}

// Validate reports every problem in the config, not just the first one.
func (c *Config) Validate() error {
	v := &validator{}
	if u, err := url.Parse(c.Toxiproxy.API); err != nil || u.Scheme == "" || u.Host == "" {
		v.bad("toxiproxy.api: %q is not a URL", c.Toxiproxy.API)
	}
	deps := map[string]bool{}
	for i, d := range c.Dependencies {
		v.dependency(fmt.Sprintf("dependencies[%d]", i), d, deps)
	}
	services := map[string]bool{}
	for i, s := range c.Services {
		v.service(fmt.Sprintf("services[%d]", i), s, services, deps)
	}
	return errors.Join(v.errs...)
}

type validator struct{ errs []error }

func (v *validator) bad(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

// name checks a name and returns the label for later messages: the name
// itself, or its position when the name is missing.
func (v *validator) name(what, position, name string, seen map[string]bool) string {
	switch {
	case name == "":
		v.bad("%s: name is required", position)
		return position
	case !namePattern.MatchString(name):
		v.bad("%s %q: name may only contain letters, digits, '.', '_' and '-'", what, name)
	case seen[name]:
		v.bad("%s %q is declared twice", what, name)
	}
	seen[name] = true
	return name
}

func (v *validator) dependency(position string, d Dependency, seen map[string]bool) {
	label := v.name("dependency", position, d.Name, seen)
	if !slices.Contains(Kinds, d.Kind) {
		v.bad("%s: kind %q is not one of %s", label, d.Kind, strings.Join(Kinds, ", "))
	}
	if !isHostPort(d.Address) {
		v.bad("%s: address %q must be host:port", label, d.Address)
	}
	if !isHostPort(d.Upstream) {
		v.bad("%s: upstream %q must be host:port", label, d.Upstream)
	}
	// An empty listen only means the default couldn't be derived from a bad address, already reported.
	if d.Listen != "" && !isHostPort(d.Listen) {
		v.bad("%s: listen %q must be host:port", label, d.Listen)
	}
	if d.Redirect && d.Kind != "https" {
		v.bad("%s: redirect only applies to kind https", label)
	}
}

func (v *validator) service(position string, s Service, seen, deps map[string]bool) {
	label := v.name("service", position, s.Name, seen)
	if u, err := url.Parse(s.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		v.bad("%s: baseUrl %q must be an http(s) URL", label, s.BaseURL)
	}
	if s.HealthCheck != "" && !strings.HasPrefix(s.HealthCheck, "/") {
		v.bad("%s: healthCheck %q must be a path starting with /", label, s.HealthCheck)
	}
	for _, dep := range s.Uses {
		if !deps[dep] {
			v.bad("%s uses %q: no such dependency", label, dep)
		}
	}
	commands := map[string]bool{}
	for j, cmd := range s.Commands {
		v.command(label, fmt.Sprintf("%s/commands[%d]", label, j), cmd, commands)
	}
	v.health(label, s, commands)
}

func (v *validator) health(label string, s Service, commands map[string]bool) {
	if s.HealthCommand != "" {
		if s.HealthCheck != "" {
			v.bad("%s: use either healthCheck or healthCommand, not both", label)
		}
		if !commands[s.HealthCommand] {
			v.bad("%s: healthCommand %q is not one of its commands", label, s.HealthCommand)
		}
	}
	switch {
	case s.HealthEverySec == 0:
	case s.HealthCommand == "":
		v.bad("%s: healthEverySec only applies with healthCommand", label)
	case s.HealthEverySec < MinHealthEverySec || s.HealthEverySec > MaxHealthEverySec:
		v.bad("%s: healthEverySec must be 0 (on demand) or between %d and %d", label, MinHealthEverySec, MaxHealthEverySec)
	}
}

func (v *validator) command(service, position string, cmd Command, seen map[string]bool) {
	label := v.name(service+" command", position, cmd.Name, seen)
	if label != position {
		label = service + "/" + label
	}
	if strings.TrimSpace(cmd.Run) == "" {
		v.bad("%s: run is required", label)
	}
	if cmd.TimeoutSec < 1 || cmd.TimeoutSec > MaxCommandTimeoutSec {
		v.bad("%s: timeoutSec must be between 1 and %d", label, MaxCommandTimeoutSec)
	}
	if info, err := os.Stat(cmd.Cwd); err != nil || !info.IsDir() {
		v.bad("%s: cwd %q is not a folder", label, cmd.Cwd)
	}
}

func isHostPort(s string) bool {
	_, port, err := net.SplitHostPort(s)
	return err == nil && port != ""
}
