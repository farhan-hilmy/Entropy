# Entropy

Entropy helps you see how your backend services behave when their dependencies
(Redis, MongoDB, external APIs…) fail. It puts
[Toxiproxy](https://github.com/Shopify/toxiproxy) between each service and its
dependencies, so it can inject faults and check how the services recover.

```
your service ──► Toxiproxy (proxy per dependency) ──► real dependency
                     ▲
                  Entropy  (checks status, injects and clears faults)
```

**Documentation: https://farhan-hilmy.github.io/Entropy/** (the same pages are
in [`docs/`](docs/)).

## Quick start

Needs Go 1.26+, Node.js 20+, and Podman or Docker for the demo Redis.

```sh
podman compose -f examples/compose.yaml up -d   # a Redis to break
cp entropy.example.yaml entropy.yaml            # your config (git-ignored)
(cd web && npm install && npm run build)        # the UI, once
go run ./cmd/entropy up                         # then open http://127.0.0.1:7400
```

[Getting started](docs/getting-started.md) explains each step.

## Where to read next

| If you want to… | Read |
|---|---|
| add your own services and dependencies | [Set up your services](docs/setup.md) |
| break a dependency by hand | [Statuses and faults](docs/faults.md) |
| write and run experiments | [Experiments](docs/experiments.md) |
| use it from Claude Code or another agent | [Agents (MCP)](docs/agents.md) |
| fix something that doesn't work | [Troubleshooting](docs/troubleshooting.md) |
| work on Entropy itself | [Contributing](docs/contributing.md), [REST API](docs/api.md) |

## Status

The MVP is complete: environment setup, faults, experiments with an editor,
runs with history and rerun, and the built-in Toxiproxy. After the MVP, AI
agents can use Entropy through `entropy mcp`, including setting it up for a
service and chaos testing every dependency.
