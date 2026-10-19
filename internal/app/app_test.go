package app

import (
	"errors"
	"strings"
	"testing"

	"entropy/internal/command"
	"entropy/internal/store"
)

func TestHealthStatus(t *testing.T) {
	cmd := store.Command{Name: "access-token", TimeoutSec: 30}
	cases := []struct {
		res       command.Result
		state     State
		detailHas string
	}{
		{command.Result{ExitCode: 0}, Reachable, "access-token passed"},
		{command.Result{ExitCode: 1, Output: "running...\n  AssertionError: expected 500 to equal 200 \n"}, Unreachable,
			"exited with code 1: AssertionError: expected 500 to equal 200"},
		{command.Result{ExitCode: -1, TimedOut: true}, Unreachable, "timed out after 30s"},
		{command.Result{ExitCode: -1, Err: errors.New("bad dir")}, Unreachable, "could not run access-token: bad dir"},
	}
	for _, c := range cases {
		st := healthStatus(cmd, c.res)
		if st.State != c.state || !strings.Contains(st.Detail, c.detailHas) {
			t.Errorf("healthStatus(%+v) = %+v; want %s containing %q", c.res, st, c.state, c.detailHas)
		}
	}
}

func TestSameListen(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"[::]:6379", "0.0.0.0:6379", true},
		{":6379", "0.0.0.0:6379", true},
		{"[::]:6379", "0.0.0.0:6380", false},
		{"127.0.0.1:6379", "0.0.0.0:6379", false},
	}
	for _, c := range cases {
		if got := sameListen(c.a, c.b); got != c.want {
			t.Errorf("sameListen(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
