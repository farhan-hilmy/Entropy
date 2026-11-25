# Use Entropy from an AI agent (MCP)

`entropy mcp` offers Entropy to agents such as Claude Code as MCP tools, so an
agent can run an experiment, read why it failed, fix the service and run it
again. It talks to `entropy up`, and starts it if it isn't running.

## Set it up

Build Entropy, then add it to Claude Code (once, from any folder):

```sh
go build -o entropy.exe ./cmd/entropy
claude mcp add entropy -- D:/path/to/entropy.exe mcp -config D:/path/to/entropy.yaml
```

That line is the whole setup:

- **Entropy already running** (you started `entropy up`, or have the UI open):
  the agent uses it.
- **Not running:** the agent's first Entropy tool call starts `entropy up`
  with that `entropy.yaml`, in its folder, so it uses the same database and
  run history as when you start it yourself. The tool result says it was
  started, with the UI's address. It **stops when the agent session ends**
  (even if the agent is killed), removing every fault; its log is
  `.entropy/entropy-up.log` next to `entropy.yaml`.
- Without `-config`, it looks for `entropy.yaml` in the folder the agent runs
  in. `-autostart=false` turns starting off.

Use `--scope project` to put it in the repo's `.mcp.json` for the whole team,
and `-url http://127.0.0.1:7500` after `mcp` if Entropy runs on another port
(it's only started for a local URL). Check it with `/mcp` in Claude Code.

## Tools

| Tool | What it does |
|---|---|
| `entropy_status` | dependencies (reachable? a fault on it?), services and their commands, any run in progress |
| `list_experiments` | each experiment's faults, last verdict and timing warnings |
| `get_experiment` | one experiment as YAML, as written in `entropy.yaml`, and its recent runs |
| `list_runs`, `get_run` | verdicts, each assertion's outcome, the timeline |
| `run_experiment` | runs it and waits for the verdict (`waitSec: 0` returns at once) |
| `stop_run`, `stop_all_faults` | the safety exits |
| `save_experiment` | the editor's safe save: only that experiment's lines change, an invalid one changes nothing |
| `run_command` | runs a service's command once, without faults, e.g. to check a fix |
| `config_reference` | how `entropy.yaml` works, and good defaults for a suite per dependency |
| `add_dependency`, `update_dependency`, `add_service`, `update_service`, `add_command`, `update_command`, `remove_command` | set Entropy up with the same safe saves as the UI (`update_*` keep the fields not given; a command still in use can't be removed) |
| `verify_route` | checks a service really reaches a dependency through the proxy (**Verify** in the UI) |
| `suite_report` | each dependency against each fault type, with the latest verdicts, and what no experiment covers |

## Prompts: setup and suite

Two prompts guide the agent through the whole job (in Claude Code:
`/mcp__entropy__setup` and `/mcp__entropy__suite`, with a service as the
argument):

- **setup:** reads the service's code and config, finds its dependencies,
  proposes the entries, the commands and the change to the service's own
  config (to use the proxy addresses), saves them once you confirm, and
  checks every route with `verify_route`.
- **suite:** proposes an experiment per dependency and fault (outage and
  latency for each, flap for long-lived connections) with the **expected
  behaviour in plain words for you to confirm**, saves them, runs them one at
  a time, and shows `suite_report` with why each failure happened.

## Safety

- **Agents can't start a fault on their own:** every fault they cause is part of
  a run, with a verdict and Entropy's limits (`maxDurationSec`, one run at a
  time, faults always removed).
- A run really breaks the dependency for every service using it. Claude Code
  asks before each tool call unless you allow it; allowing the read tools and
  the stop tools is safe. Don't allow `add_command` without asking: a command
  is code Entropy runs on your machine.
- Try: *"Run redis-outage-oauth with Entropy. If it fails, find out why in the
  oauth code, fix it, and rerun until it passes."*
