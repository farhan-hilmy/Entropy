# Statuses and faults

## Read the statuses

Entropy checks every dependency **through its proxy** every 5 seconds, with
a real protocol request (Redis `PING`, MongoDB `hello`, an HTTP request, a TLS
handshake…). It only shows statuses it can prove:

| Status | Meaning |
|---|---|
| **Reachable** | The dependency answered through the proxy. |
| **Unreachable** | No answer. Hover the status to see why. |
| **Fault active** | A fault is injected: the proxy is disabled or has toxics. |
| **Not checked yet** | Just added; the first check hasn't run. |

**Experiment-ready** means the dependency is Reachable and has no fault on
it. If it isn't, the Dependency page says why.

## Apply a fault

On a dependency's page, the **Fault** panel applies one fault to its proxy
for a fixed time (1 to 3600 seconds), then removes it:

| Type | What your service sees | Options |
|---|---|---|
| `latency` | Every response is delayed | latency (default 1000 ms), jitter |
| `drop` | Every connection is reset (TCP RST) | |
| `timeout` | Nothing gets through; connections hang | close after N ms (default: hang until the fault ends) |
| `outage` | The proxy is disabled: connections close and new ones are refused | |
| `flap` | Outage and normal take turns, starting with an outage | down for / up for (default 5 s each) |

- A dependency has at most one fault at a time. **Stop fault** ends it early.
- Entropy removes the fault when its time is up, even if no page is open. If
  that fails (e.g. Toxiproxy is unreachable), it keeps retrying until it
  succeeds (after 2 s, then 4 s, 8 s… up to every 30 s), and the panel shows
  the error.
- If your own Toxiproxy restarts (or is reset from outside Entropy) during a fault,
  Entropy puts the fault back within about 5 seconds, so it lasts its full
  duration.
- The fault only affects services that connect through the proxy.

### Verify that a service uses the proxy

A service that connects straight to the real dependency never sees a fault,
so an experiment against it would wrongly look like a pass. On a
dependency's page, *Used by services* has a **Verify** button per service:
Entropy runs one of the service's requests (its health check, or a command
you pick) twice: normally, then with 1.5 s of latency on the proxy.

| Result | Meaning |
|---|---|
| **Goes through the proxy** | The request got clearly slower (or failed) with latency on the proxy. |
| **Not seen in the proxy** | It ran just as fast. Either the service connects straight to `upstream`, or that request doesn't use the dependency (e.g. it's cached): try a request that does. |
| **Couldn't tell** | The request fails even without latency. Fix it or pick another one. |

For a command, Entropy compares the HTTP request time the command prints,
like Bruno's `Access Token B2B (200 OK) - 52 ms`, when both runs print one.
A command's own run time includes its startup, which for Bruno varies by
seconds and would hide the 1.5 s. Otherwise it compares how long the whole
command took.

While the slowed-down request runs (a few seconds with Bruno), everything
using that dependency through the proxy is 1.5 s slower. Entropy pauses its
own checks of the dependency meanwhile. It can't verify during a fault, and
a port-only health check never reaches the dependency, so it isn't offered.

Traffic counters can't do this job: Toxiproxy counts a connection's bytes
only when it closes, so pooled connections stay invisible, and it can't
tell services apart.

## Stop all faults

Press **Stop all faults** in the top bar, on any page. It aborts the running
experiment, ends every fault Entropy started, then enables every proxy and
removes every toxic, including ones added outside Entropy. Entropy also does this on startup and on exit, so
killing Entropy in the middle of a fault leaves nothing broken after the next
start. If Toxiproxy isn't reachable at startup, Entropy does it as soon as
Toxiproxy answers.
