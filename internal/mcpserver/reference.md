# entropy.yaml reference (for agents)

Entropy sits between a service and its dependencies: each dependency gets a
proxy, the service connects to the proxy, and Entropy breaks the proxy on
purpose. A dependency whose service doesn't connect through the proxy can't be
tested; `verify_route` checks that it does.

## Dependencies (add_dependency, update_dependency)

```yaml
dependencies:
  - name: redis              # used in uses:, faults: and tool calls
    kind: redis              # redis | mongodb | postgres | rabbitmq | http | https
    address: 127.0.0.1:16379 # where the SERVICE connects: the proxy (Entropy opens this port)
    upstream: 127.0.0.1:6379 # the REAL dependency; auto:<port> for a container on this machine
    listen: 127.0.0.1:16379  # optional; default: address's port on 127.0.0.1
```

- Pick a free proxy port for `address` that differs from the real one when
  both run on this machine (e.g. 16379 for Redis on 6379, 27018 for MongoDB
  on 27017). The real dependency keeps its port.
- `upstream: auto:<port>` means the container host (Podman/Docker), found
  automatically.
- External HTTPS APIs (`kind: https`): the certificate only matches the real
  hostname, so the service keeps using it and the person points the hostname
  at 127.0.0.1 in the hosts file (needs admin; never edit it yourself, give
  them the line). Then `address: <host>:443`, `upstream: <host>:443`,
  `redirect: true` (set with update_dependency).
- After adding a dependency, the SERVICE must be pointed at `address`: change
  its connection setting (e.g. REDIS_ADDR, MONGO_URI, a base URL in .env) to
  the proxy address, and have the person restart it. Until then faults have
  no effect on it.

## Services (add_service, update_service)

```yaml
services:
  - name: oauth
    baseUrl: http://127.0.0.1:8000   # requests in experiments go here
    healthCheck: /health             # optional URL path; or healthCommand
    healthCommand: token             # optional: one of its commands; exit 0 = healthy
    uses: [redis, mongodb]           # its dependencies (names above)
    commands:                        # add_command
      - name: token
        run: bru run "Access Token.yml" --env local   # as typed in a terminal (cmd.exe on Windows)
        cwd: D:/path/to/collection   # optional; default: the folder of entropy.yaml
        timeoutSec: 30               # optional; default 30, max 600
```

- A command is anything that calls the service and exits 0 on success:
  `bru run …` (Bruno), `curl -f …`, a script. Prefer requests that really use
  the dependency under test (e.g. issuing a token reads and writes Redis).
- Commands get ENTROPY_BASE_URL (the service's baseUrl) and ENTROPY_SERVICE.
- Plain HTTP requests can be used in experiments without a command (see
  `request:` below).

## Experiments (save_experiment)

```yaml
name: redis-outage-oauth
description: Redis down for 30 s; tokens fail cleanly, then work again.
maxDurationSec: 180          # safety cap; the timing check suggests a value
faults:
  - dependency: redis
    type: outage             # see the fault types below
    durationSec: 30
    startOffsetSec: 0        # optional: start later in the "during" phase
triggers:                    # what to send, per phase: baseline | during | after
  - { name: before, phase: baseline, command: oauth/token }
  - { name: during, phase: during, command: oauth/token, repeat: 3, everySec: 5 }
  - { name: after, phase: after, command: oauth/token, repeat: 3, everySec: 5 }
  - name: ping
    phase: after
    service: oauth
    request: { method: GET, path: /health }   # instead of a command
recoveryWindowSec: 30        # how long "after" lasts (default 30)
assertions:
  - { type: response, trigger: before, expect: { exitCode: 0 } }       # every result matches
  - { type: count, trigger: during, atLeast: 1, expect: { outputContains: "503" } }
  - { type: eventual, trigger: after, withinSec: 20, expect: { exitCode: 0 } }  # one matches in time
  - { type: recovery, service: oauth, withinSec: 30 }                   # health check passes again
verifyWith: { oauth: during }   # optional: the trigger the preflight uses to check the route
```

Fault types (each has `durationSec`, 1–3600):

| type | what the service sees | options |
| -- | -- | -- |
| outage | connection refused | |
| latency | every response delayed | latencyMs (default 1000), jitterMs |
| timeout | connection hangs, then is closed | timeoutMs (0: hangs until the fault ends) |
| drop | connections reset | |
| flap | down and up in turns | downSec, upSec (default 5 each) |

`expect`: commands have `exitCode` and `outputContains`; requests have
`status` and `bodyContains`. A baseline assertion that fails makes the run
inconclusive (the service wasn't healthy to begin with).

## A suite per dependency

Good defaults, one experiment per dependency and fault:

- **outage** for every dependency: does it fail cleanly, and recover?
- **latency** (e.g. 5000 ms, longer than the service's own timeout should be):
  does it time out, or hang?
- **flap** only for dependencies the service keeps long-lived connections to
  (Redis, MongoDB, Postgres, RabbitMQ), not for HTTP APIs.

The expected behaviour depends on the role of the dependency, so propose it in
plain words and let the person confirm before saving:

- A cache (e.g. Redis in front of a database): requests should still succeed
  during the fault, maybe slower.
- Primary storage: a clean error during the fault (e.g. 503, not a hang or a
  500 with a stack trace), then success again soon after.
- An external API: depends on the business: fail the request cleanly, or
  carry on with cached data.

Name experiments `<dependency>-<fault>-<service>`, e.g. `redis-outage-oauth`,
so `suite_report` can group them.
