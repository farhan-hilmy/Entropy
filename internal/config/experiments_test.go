package config

import (
	"strings"
	"testing"
)

const experimentBase = `
dependencies:
  - name: redis
    kind: redis
    address: localhost:6379
    upstream: redis:6379
services:
  - name: oauth
    baseUrl: http://localhost:8000
    healthCheck: /health
    uses: [redis]
    commands:
      - name: access-token
        run: bru run token.yml
`

func TestLoadExperimentDefaults(t *testing.T) {
	cfg, err := Load(write(t, experimentBase+`
experiments:
  - name: redis-outage-oauth
    faults:
      - dependency: redis
        type: latency
        durationSec: 30
    triggers:
      - name: token-during
        phase: during
        command: oauth/access-token
      - name: token-after
        phase: after
        service: oauth
        request: { path: /access-token, body: { grant_type: client_credentials } }
    assertions:
      - type: response
        trigger: token-during
        expect: { exitCode: 0 }
      - type: recovery
`))
	if err != nil {
		t.Fatal(err)
	}
	e := cfg.Experiments[0]
	if e.MaxDurationSec != DefaultExperimentMaxSec || e.RecoveryWindowSec != DefaultRecoveryWindowSec {
		t.Errorf("maxDurationSec %d, recoveryWindowSec %d; want the defaults", e.MaxDurationSec, e.RecoveryWindowSec)
	}
	if e.Faults[0].LatencyMs != 1000 {
		t.Errorf("latencyMs = %d; the fault's own defaults should apply", e.Faults[0].LatencyMs)
	}
	if e.Triggers[0].Repeat != 1 || e.Triggers[1].Request.Method != "GET" {
		t.Errorf("triggers = %+v; want repeat 1 and method GET", e.Triggers)
	}
	if e.Triggers[0].ServiceOf() != "oauth" || e.Triggers[0].CommandName() != "access-token" {
		t.Errorf("command trigger resolves to %s/%s", e.Triggers[0].ServiceOf(), e.Triggers[0].CommandName())
	}
	if r := e.Assertions[1]; r.Service != "oauth" || r.WithinSec != DefaultRecoveryWithinSec {
		t.Errorf("recovery = %+v; want service oauth (the only one the triggers use) and the default withinSec", r)
	}
	if code := e.Assertions[0].Expect.ExitCode; code == nil || *code != 0 {
		t.Error("exitCode: 0 must be kept, not treated as unset")
	}
}

func TestLoadReportsExperimentProblems(t *testing.T) {
	_, err := Load(write(t, experimentBase+`
experiments:
  - name: bad
    maxDurationSec: 99999
    faults:
      - dependency: kafka
        type: outage
        durationSec: 10
      - dependency: redis
        type: chaos
        durationSec: 10
    triggers:
      - name: t1
        phase: sometime
        command: oauth/nope
      - name: t1
        phase: during
        service: oauth
        request: { path: token, method: FETCH }
      - name: both
        phase: during
        command: oauth/access-token
        service: oauth
        request: { path: /x }
    assertions:
      - type: count
        trigger: t1
        expect: { status: 200 }
        atLeast: 5
      - type: response
        trigger: missing
        expect: { status: 200 }
      - type: magic
  - name: empty
    triggers: []
    assertions: []
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"experiment bad: maxDurationSec must be between 1 and 3600",
		`faults[0]: dependency "kafka": no such dependency`,
		`faults[1]: type "chaos": must be one of`,
		`trigger t1: phase "sometime" is not one of baseline, during, after`,
		`trigger t1: command "oauth/nope": oauth has no command nope`,
		"trigger t1: declared twice",
		`request path "token" must start with /`,
		`request method "FETCH" is not one of`,
		"trigger both: needs exactly one of command (service/command) or request",
		"atLeast must be between 1 and the trigger's repeat",
		`trigger "missing": no such trigger in this experiment`,
		`type "magic" is not one of response, count, eventual, recovery`,
		"experiment empty: needs at least one trigger",
		"experiment empty: needs at least one assertion",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q\n%v", want, err)
		}
	}
}

func TestExpectMustFitTheTrigger(t *testing.T) {
	_, err := Load(write(t, experimentBase+`
experiments:
  - name: mismatch
    triggers:
      - { name: cmd, phase: during, command: oauth/access-token }
      - { name: req, phase: during, service: oauth, request: { path: /x } }
    assertions:
      - { type: response, trigger: cmd, expect: { status: 503 } }
      - { type: response, trigger: req, expect: { exitCode: 0 } }
      - { type: response, trigger: req, expect: {} }
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"trigger cmd runs a command: expect exitCode or outputContains",
		"trigger req sends a request: expect status or bodyContains",
		"expect needs at least one of",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q\n%v", want, err)
		}
	}
}
