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

Toxiproxy runs inside Entropy, so `entropy up` is all you start. You can
also point Entropy at a Toxiproxy you run yourself, e.g. in a container
([Use your own Toxiproxy](setup.md#use-your-own-toxiproxy)).

## What you can do

- **Register** your dependencies and services, and see live statuses checked
  through each proxy.
- **Break** a dependency on purpose for a fixed time: latency, dropped or
  hanging connections, an outage, or a flapping one.
- **Verify** that a service really goes through the proxy, so a fault can't
  pass unnoticed.
- **Run experiments**: faults plus requests before, during and after, ending
  in a verdict (passed, failed, inconclusive, aborted), with history and
  reruns.
- **Let an AI agent do it** through `entropy mcp`: set Entropy up for a
  service, chaos test every dependency, and fix what fails.

## Where to start

| If you want to… | Read |
|---|---|
| try it with the demo Redis | [Getting started](getting-started.md) |
| add your own services and dependencies | [Set up your services](setup.md) |
| break a dependency by hand | [Statuses and faults](faults.md) |
| write and run experiments | [Experiments](experiments.md) |
| use it from Claude Code or another agent | [Agents (MCP)](agents.md) |
| fix something that doesn't work | [Troubleshooting](troubleshooting.md) |
