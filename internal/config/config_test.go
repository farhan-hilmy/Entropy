package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	// Not t.TempDir: its cleanup fails the test when, on Windows, a virus
	// scanner still holds a file Apply just wrote. Leftovers are harmless.
	dir, err := os.MkdirTemp("", "entropy-config-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "entropy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(write(t, `
dependencies:
  - name: redis
    kind: redis
    address: localhost:6379
    upstream: redis:6379
services:
  - name: oauth
    baseUrl: http://localhost:8000
    uses: [redis]
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment.Name != "local" {
		t.Errorf("environment name = %q, want local", cfg.Environment.Name)
	}
	if cfg.Toxiproxy.API != DefaultToxiproxyAPI {
		t.Errorf("toxiproxy api = %q", cfg.Toxiproxy.API)
	}
	if got := cfg.Dependencies[0].Listen; got != "0.0.0.0:6379" {
		t.Errorf("listen = %q, want 0.0.0.0:6379", got)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(write(t, `
dependencies:
  - name: redis
    kind: redsi
    address: localhost
    upstream: redis:6379
  - name: redis
    kind: redis
    address: localhost:6380
    upstream: redis:6379
    redirect: true
services:
  - name: orders-api
    baseUrl: localhost:8080
    healthCheck: health
    uses: [redis, kafka]
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		`kind "redsi" is not one of`,
		`address "localhost" must be host:port`,
		`dependency "redis" is declared twice`,
		`redirect only applies to kind https`,
		`baseUrl "localhost:8080" must be an http(s) URL`,
		`healthCheck "health" must be a path`,
		`orders-api uses "kafka": no such dependency`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q\n%v", want, err)
		}
	}
}

func TestLoadCommandDefaults(t *testing.T) {
	path := write(t, `
services:
  - name: oauth
    baseUrl: http://localhost:8080
    commands:
      - name: token
        run: bru run "Access Token B2B.yml" --env local
      - name: in-sub
        run: echo hi
        cwd: sub
        timeoutSec: 5
`)
	dir := filepath.Dir(path)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cmds := cfg.Services[0].Commands
	if cmds[0].Cwd != dir || cmds[0].TimeoutSec != DefaultCommandTimeoutSec {
		t.Errorf("token: cwd %q timeout %d; want the config folder and %d", cmds[0].Cwd, cmds[0].TimeoutSec, DefaultCommandTimeoutSec)
	}
	if cmds[1].Cwd != filepath.Join(dir, "sub") {
		t.Errorf("in-sub: cwd %q, want it resolved against the config folder", cmds[1].Cwd)
	}
}

func TestLoadReportsCommandProblems(t *testing.T) {
	_, err := Load(write(t, `
services:
  - name: oauth
    baseUrl: http://localhost:8080
    commands:
      - name: token
        run: "  "
        cwd: D:/surely/not/here
        timeoutSec: 9999
      - name: token
        run: echo hi
      - run: echo no name
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"oauth/token: run is required",
		"oauth/token: timeoutSec must be between 1 and 600",
		`oauth/token: cwd "D:`,
		`oauth command "token" is declared twice`,
		"oauth/commands[2]: name is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q\n%v", want, err)
		}
	}
}

func TestLoadReportsHealthCommandProblems(t *testing.T) {
	_, err := Load(write(t, `
services:
  - name: oauth
    baseUrl: http://localhost:8080
    healthCheck: /health
    healthCommand: tokn
    healthEverySec: 5
    commands:
      - name: token
        run: echo ok
  - name: other
    baseUrl: http://localhost:8081
    healthEverySec: 60
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"oauth: use either healthCheck or healthCommand, not both",
		`oauth: healthCommand "tokn" is not one of its commands`,
		"oauth: healthEverySec must be 0 (on demand) or between 15",
		"other: healthEverySec only applies with healthCommand",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q\n%v", want, err)
		}
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	_, err := Load(write(t, "dependencies:\n  - name: redis\n    kidn: redis\n"))
	if err == nil || !strings.Contains(err.Error(), "kidn") {
		t.Fatalf("expected an unknown field error, got %v", err)
	}
}

func TestSetContainerHost(t *testing.T) {
	cfg, err := Load(write(t, `
toxiproxy:
  api: http://auto:8474
dependencies:
  - name: redis
    kind: redis
    address: auto:6379
    upstream: redis:6379
  - name: api
    kind: http
    address: 10.0.0.5:8080
    upstream: api:8080
`))
	if err != nil {
		t.Fatal(err)
	}
	if port, ok := cfg.AutoAPIPort(); !ok || port != "8474" {
		t.Fatalf("AutoAPIPort = %q, %v", port, ok)
	}
	cfg.SetContainerHost("172.28.142.159")
	if cfg.Toxiproxy.API != "http://172.28.142.159:8474" {
		t.Errorf("api = %q", cfg.Toxiproxy.API)
	}
	if got := cfg.Dependencies[0].Address; got != "172.28.142.159:6379" {
		t.Errorf("auto address = %q", got)
	}
	if got := cfg.Dependencies[0].Listen; got != "0.0.0.0:6379" {
		t.Errorf("listen = %q", got)
	}
	if got := cfg.Dependencies[1].Address; got != "10.0.0.5:8080" {
		t.Errorf("explicit address changed to %q", got)
	}
}

func TestAutoAddressWithExplicitAPI(t *testing.T) {
	cfg, err := Load(write(t, `
toxiproxy:
  api: http://10.0.0.5:8474
dependencies:
  - name: redis
    kind: redis
    address: auto:6379
    upstream: redis:6379
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.AutoAPIPort(); ok {
		t.Fatal("explicit API reported as auto")
	}
	cfg.SetContainerHost(cfg.APIHost())
	if got := cfg.Dependencies[0].Address; got != "10.0.0.5:6379" {
		t.Errorf("address = %q, want the API's host", got)
	}
}

func TestRedirectRejectsAutoAddress(t *testing.T) {
	_, err := Load(write(t, `
dependencies:
  - name: cidm
    kind: https
    address: auto:443
    upstream: cidm.example.com:443
    redirect: true
`))
	if err == nil || !strings.Contains(err.Error(), "cidm: redirect needs the hostname in address") {
		t.Fatalf("expected a redirect error, got %v", err)
	}
}
