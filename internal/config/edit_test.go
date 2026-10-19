package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const editFixture = `# Demo config.

environment:
  name: local

# Podman publishes on the VM's address.
toxiproxy:
  api: http://172.28.142.159:8474

dependencies:
  - name: redis
    kind: redis
    address: 172.28.142.159:6379   # services connect here
    upstream: redis:6379      # the real Redis
    listen: 0.0.0.0:6379
  - name: mongo
    kind: mongodb
    address: 172.28.142.159:27017
    upstream: mongo:27017

services:
  # Replace with your own service.
  - name: orders
    baseUrl: http://127.0.0.1:8080/orders
    uses: [redis]
    healthCommand: token
    # Commands stay file-only.
    commands:
      - name: token
        run: 'bru run "token.yml" --sandbox="dev (ex: developer)"'
        timeoutSec: 30                          # default 30
`

// snapshot writes content to a config file and reads it back for editing.
func snapshot(t *testing.T, content string) (string, Snapshot) {
	t.Helper()
	path := write(t, content)
	snap, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, snap
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplyKeepsCommentsAndLayout(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	_, err := Apply(path, snap.Hash, Edit{Dependency: &DependencyFields{
		Name:     "redis",
		Address:  "localhost:6379",
		Upstream: "redis:6379",
		Listen:   "0.0.0.0:6379",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editFixture,
		"    address: 172.28.142.159:6379   # services connect here",
		"    address: localhost:6379   # services connect here", 1)
	if got := fileContent(t, path); got != want {
		t.Errorf("only the address should change; got:\n%s", got)
	}
}

func TestApplyRemovesEmptyOptionalFieldsAndAddsNewOnes(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	_, err := Apply(path, snap.Hash, Edit{Service: &ServiceFields{
		Name:        "orders",
		BaseURL:     "http://127.0.0.1:9090/orders",
		HealthCheck: "/health",
		Uses:        []string{"redis", "mongo"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editFixture, `    baseUrl: http://127.0.0.1:8080/orders
    uses: [redis]
    healthCommand: token
`, `    baseUrl: http://127.0.0.1:9090/orders
    uses: [redis, mongo]
    healthCheck: /health
`, 1)
	if got := fileContent(t, path); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := cfg.Services[0]; s.HealthCommand != "" || s.HealthCheck != "/health" || len(s.Uses) != 2 {
		t.Errorf("service after edit = %+v", s)
	}
}

func TestApplyEnvironmentAddsMissingSection(t *testing.T) {
	path, snap := snapshot(t, "dependencies: []\n")
	_, err := Apply(path, snap.Hash, Edit{Environment: &EnvironmentFields{Name: "staging", ToxiproxyAPI: "http://10.0.0.5:8474"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "dependencies: []\n\nenvironment:\n  name: staging\n\ntoxiproxy:\n  api: http://10.0.0.5:8474\n"
	if got := fileContent(t, path); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestApplyKeepsWindowsLineEndings(t *testing.T) {
	crlf := strings.ReplaceAll(editFixture, "\n", "\r\n")
	path, snap := snapshot(t, crlf)
	_, err := Apply(path, snap.Hash, Edit{Environment: &EnvironmentFields{Name: "local", ToxiproxyAPI: "http://localhost:8474"}})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(crlf, "http://172.28.142.159:8474", "http://localhost:8474", 1)
	if got := fileContent(t, path); got != want {
		t.Errorf("got:\n%q", got)
	}
}

func TestApplyRefusesStaleHash(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	if err := os.WriteFile(path, []byte(editFixture+"# edited in the IDE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(path, snap.Hash, Edit{Environment: &EnvironmentFields{Name: "x"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestApplyRefusesInvalidResult(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	_, err := Apply(path, snap.Hash, Edit{Dependency: &DependencyFields{Name: "redis", Address: "no-port", Upstream: "redis:6379"}})
	var invalid *InvalidError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), `address "no-port" must be host:port`) {
		t.Fatalf("err = %v, want an InvalidError about the address", err)
	}
	if got := fileContent(t, path); got != editFixture {
		t.Error("an invalid edit must leave the file unchanged")
	}
}

func TestApplyUnknownName(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	_, err := Apply(path, snap.Hash, Edit{Service: &ServiceFields{Name: "nope", BaseURL: "http://x"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestApplyNeedsExactlyOnePart(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	for _, e := range []Edit{
		{},
		{Environment: &EnvironmentFields{}, Service: &ServiceFields{Name: "orders"}},
	} {
		if _, err := Apply(path, snap.Hash, e); err == nil {
			t.Errorf("Apply(%+v) succeeded, want an error", e)
		}
	}
}
