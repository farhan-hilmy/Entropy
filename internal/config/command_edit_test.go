package config

import (
	"errors"
	"strings"
	"testing"
)

func TestAddCommandToExistingList(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	if _, err := Apply(path, snap.Hash, Edit{AddCommand: &NewCommand{Service: "orders", Name: "refund",
		Run: `bru run "refund.yml" --env local`, Cwd: ".", TimeoutSec: 60}}); err != nil {
		t.Fatal(err)
	}
	want := editFixture + `      - name: refund
        run: bru run "refund.yml" --env local
        cwd: .
        timeoutSec: 60
`
	if got := fileContent(t, path); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestAddFirstCommand(t *testing.T) {
	// A service with no commands: key, in the middle of the list.
	content := strings.Replace(editFixture, "services:\n", `services:
  - name: billing
    baseUrl: http://127.0.0.1:9000
    uses:
      - mongo
`, 1)
	path, snap := snapshot(t, content)
	if _, err := Apply(path, snap.Hash, Edit{AddCommand: &NewCommand{Service: "billing", Name: "health", Run: "curl -f http://127.0.0.1:9000/health"}}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(content, "      - mongo\n", `      - mongo
    commands:
      - name: health
        run: curl -f http://127.0.0.1:9000/health
`, 1)
	if got := fileContent(t, path); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Services[0].Commands; len(c) != 1 || c[0].TimeoutSec != DefaultCommandTimeoutSec {
		t.Errorf("billing's commands: %+v", c)
	}
}

func TestChangeCommandKeepsItsComments(t *testing.T) {
	path, snap := snapshot(t, editFixture)
	if _, err := Apply(path, snap.Hash, Edit{Command: &CommandFields{Service: "orders", Name: "token", Run: "curl -f http://127.0.0.1:8080/token", TimeoutSec: 45}}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editFixture, `        run: 'bru run "token.yml" --sandbox="dev (ex: developer)"'
        timeoutSec: 30                          # default 30`, `        run: curl -f http://127.0.0.1:8080/token
        timeoutSec: 45                          # default 30`, 1)
	if got := fileContent(t, path); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestRemoveCommand(t *testing.T) {
	// One that nothing uses goes.
	path, snap := snapshot(t, editFixture)
	snapAfterAdd := func() Snapshot {
		if _, err := Apply(path, snap.Hash, Edit{AddCommand: &NewCommand{Service: "orders", Name: "spare", Run: "echo"}}); err != nil {
			t.Fatal(err)
		}
		s, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}()
	if _, err := Apply(path, snapAfterAdd.Hash, Edit{RemoveCommand: &CommandRef{Service: "orders", Name: "spare"}}); err != nil {
		t.Fatal(err)
	}
	if got := fileContent(t, path); got != editFixture {
		t.Errorf("after add and remove:\n%q\nwant\n%q", got, editFixture)
	}

	// The health command can't go, and nothing changes.
	path, snap = snapshot(t, editFixture)
	_, err := Apply(path, snap.Hash, Edit{RemoveCommand: &CommandRef{Service: "orders", Name: "token"}})
	var invalid *InvalidError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "healthCommand") {
		t.Errorf("removing the health command: got %v, want an InvalidError about healthCommand", err)
	}
	if got := fileContent(t, path); got != editFixture {
		t.Errorf("the file changed:\n%s", got)
	}
	if _, err := Apply(path, snap.Hash, Edit{RemoveCommand: &CommandRef{Service: "orders", Name: "nope"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown command: got %v, want ErrNotFound", err)
	}
}

func TestYAMLErrorsSayHowToFixThem(t *testing.T) {
	path, snap := snapshot(t, experimentFixture)
	_, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{YAML: "name: x\ndescription: Expected: a clean 500\n"}})
	if err == nil || !strings.Contains(err.Error(), `hint: a value on that line contains ": "; put the value in quotes`) {
		t.Errorf("got %v", err)
	}
}

func TestMaxMsValidation(t *testing.T) {
	text := strings.Replace(redisOutageText, `expect: { outputContains: "(200 OK)" }`, `expect: { maxMs: 5000 }`, 1)
	path, snap := snapshot(t, experimentFixture)
	if _, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{Name: "redis-outage", YAML: text}}); err != nil {
		t.Errorf("maxMs alone should do: %v", err)
	}
	path, snap = snapshot(t, experimentFixture)
	bad := strings.Replace(redisOutageText, `expect: { outputContains: "(200 OK)" }`, `expect: { maxMs: -1 }`, 1)
	if _, err := Apply(path, snap.Hash, Edit{Experiment: &ExperimentText{Name: "redis-outage", YAML: bad}}); err == nil || !strings.Contains(err.Error(), "maxMs") {
		t.Errorf("negative maxMs: got %v", err)
	}
}

func TestAddCommandInvalidChangesNothing(t *testing.T) {
	for name, c := range map[string]NewCommand{
		"duplicate name":   {Service: "orders", Name: "token", Run: "echo"},
		"no run":           {Service: "orders", Name: "x"},
		"timeout too long": {Service: "orders", Name: "x", Run: "echo", TimeoutSec: 601},
		"line break":       {Service: "orders", Name: "x", Run: "echo a\necho b"},
		"no such cwd":      {Service: "orders", Name: "x", Run: "echo", Cwd: "D:/no/such/folder"},
	} {
		t.Run(name, func(t *testing.T) {
			path, snap := snapshot(t, editFixture)
			if _, err := Apply(path, snap.Hash, Edit{AddCommand: &c}); err == nil {
				t.Fatal("no error")
			}
			if got := fileContent(t, path); got != editFixture {
				t.Errorf("the file changed:\n%s", got)
			}
		})
	}
	path, snap := snapshot(t, editFixture)
	if _, err := Apply(path, snap.Hash, Edit{AddCommand: &NewCommand{Service: "nope", Name: "x", Run: "echo"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown service: got %v, want ErrNotFound", err)
	}
}
