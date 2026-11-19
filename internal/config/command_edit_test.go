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
