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

> **Current status:** environment setup and faults (MVP build steps 1–2). You
> can register dependencies and services, see live statuses, apply a timed
> fault to a dependency and stop all faults. Experiments and runs are not
> built yet.

---

## 1. Before you start

Install:

- **Go** 1.26 or newer
- **Node.js** 20 or newer
- **Podman** (or Docker), with the Podman machine running:
  `podman machine start`

## 2. First run with the demo setup

The demo runs Redis behind Toxiproxy and registers it in Entropy.

**Step 1 – start Redis and Toxiproxy**

```sh
podman compose -f examples/compose.yaml up -d
```

or 

```sh
podman start examples-redis-1 examples-toxiproxy-1
```

if the containers already exists. Or if you make changes in compose, use this command:

```sh
podman compose -f examples/compose.yaml up -d --force-recreate toxiproxy
```

**Step 2 – create your config**

```sh
cp entropy.example.yaml entropy.yaml
```

`entropy.yaml` is yours: git ignores it, so your addresses and paths stay on
your machine. Change `entropy.example.yaml` only to update the template for
everyone.

**Step 3 – the container host (usually nothing to do)**

Container ports are published on the **container host**: `localhost` with
Docker Desktop and most setups, but on Windows Podman usually publishes on
the Podman VM's address (e.g. `172.28.142.159`). The template uses `auto`
for it:

```yaml
toxiproxy:
  api: http://auto:8474
dependencies:
  - name: redis
    address: auto:6379
```

On every sync Entropy asks each likely host for Toxiproxy's version and uses
the first that answers: `localhost`, the host in `DOCKER_HOST` or
`CONTAINER_HOST` (tcp:// or ssh://), then on Windows the WSL VM's address.
It only asks WSL distributions that are already running, so it never starts
your Podman machine. The Environment page shows the host it found, and
Entropy's log says `container host detected host=… via=…`.

If Toxiproxy runs somewhere else, write that host instead of `auto`, e.g.
`api: http://10.0.0.5:8474`. A dependency's `auto` then means the API's host.

**Step 4 – build the UI** (only needed once, and after UI changes)

```sh
cd web
npm install
npm run build
cd ..
```

**Step 5 – start Entropy**

```sh
go run ./cmd/entropy up
```

You should see:

```
msg=synced config=entropy.yaml added="[dependency redis service oauth]"
msg="cleared leftover faults"
msg="Entropy is running" url=http://127.0.0.1:7400
```

**Step 6 – open http://127.0.0.1:7400**

Redis should show **Reachable**. The demo service `oauth` shows
**Unreachable** until something answers on `http://localhost:8000/health`.
That's expected; replace it with your own service (section 3).

Stop Entropy with `Ctrl+C`. It clears all faults on exit.

---

## 3. Register your own dependencies and services

Everything Entropy knows about your environment comes from `entropy.yaml`.
You can edit the file, or use the UI:

- **Add** a dependency or service with **+ Add** on the Dependencies or
  Services page.
- **Edit** one with **Edit** on its page, or edit the environment name and
  Toxiproxy address under **Settings** on the Environment page.

The UI writes to `entropy.yaml` and syncs. It changes only the values you
edited, so your comments and layout stay. It refuses to save if the file
changed since you opened the form, or if the result would be invalid.
**Commands can only be added or changed in the file**, because Entropy runs
them on your machine. Removing something is also done in the file.

### Add a dependency

You need three things for each dependency:

1. **A proxy port on Toxiproxy.** Publish the port on the Toxiproxy
   container, e.g. for MongoDB on 27017:

   ```yaml
   # compose file
   toxiproxy:
     image: ghcr.io/shopify/toxiproxy:2.12.0
     ports:
       - "8474:8474"
       - "6379:6379"
       - "27017:27017"   # new
   ```

2. **An entry in `entropy.yaml`:**

   ```yaml
   dependencies:
     - name: mongodb                  # also the proxy's name in Toxiproxy
       kind: mongodb                  # redis | mongodb | postgres | rabbitmq | http | https
       address: auto:27017            # where your service connects: the proxy
       upstream: mongodb:27017        # the real MongoDB, as Toxiproxy sees it
   ```

   Entropy creates the proxy itself when it syncs, and recreates it if
   Toxiproxy restarts. You don't need to create it in Toxiproxy by hand.

3. **Your service pointed at the proxy.** Change your service's connection
   setting (e.g. `MONGO_URI`) to the `address` above, **not** the real
   MongoDB. With `auto`, use the container host shown on the Environment
   page. If the service connects directly, faults have no effect on it.
   To check, press **Verify** next to the service under *Used by services*
   on the dependency's page (see [Verify that a service uses the proxy](#verify-that-a-service-uses-the-proxy)).

### Add a service

```yaml
services:
  - name: oauth
    baseUrl: http://localhost:8000
    healthCheck: /health      # optional; without it Entropy only checks the port
    uses: [redis, mongodb]    # dependencies this service talks to
```

### External HTTPS APIs (e.g. CIDM)

The certificate only matches the real hostname, so the service must keep
using it. Redirect the hostname to Toxiproxy with the hosts file while
testing, and set `redirect: true`:

```yaml
  - name: cidm
    kind: https
    address: api-alpha-cid-mgmt.spesandbox.com:443
    upstream: api-alpha-cid-mgmt.spesandbox.com:443
    redirect: true
```

In `C:\Windows\System32\drivers\etc\hosts` (admin), add:

```
127.0.0.1 api-alpha-cid-mgmt.spesandbox.com  # chaos-cidm
```

then run `ipconfig /flushdns`. Remove the line when you're done. Entropy
marks CIDM ready only when the hostname resolves to 127.0.0.1 **and** the
HTTPS handshake succeeds.

`examples/entropy.lab.yaml` has the full chaos-engineering lab setup (Redis,
MongoDB, CIDM, dummy-BNI). Fill in the service ports marked TODO.

### Add commands (e.g. Bruno requests)

A command is something you'd type in a terminal to call the service, such as
a `bru run`. Add it under the service:

```yaml
services:
  - name: oauth
    baseUrl: http://127.0.0.1:8080
    uses: [redis]
    commands:
      - name: access-token
        run: bru run "Access Token B2B.yml" --env local
        cwd: D:/path/to/your/bruno-collection   # default: the folder entropy.yaml is in
        timeoutSec: 30                          # default 30, max 600
```

- **Run it** from the service's page (Services → the service → **▶ Run**), or
  in a terminal: `go run ./cmd/entropy run oauth/access-token`.
- **Exit code 0 means success.** For Bruno, add a test to the request
  (e.g. status is 200) so a bad response makes `bru` exit non-zero.
- The command runs with `cmd.exe` on Windows (`sh` elsewhere), exactly as
  typed, in `cwd`. It's killed, with everything it started, after `timeoutSec`.
  `bru` alone takes a few seconds to start, so don't set the timeout too low.
- It also gets `ENTROPY_BASE_URL` (the service's `baseUrl`) and
  `ENTROPY_SERVICE`, e.g. `--env-var service_oauth_host=%ENTROPY_BASE_URL%/api/v1.0`.
- Each run's exit code, duration and the last 16 KB of output are kept, so
  you can compare recent runs.

#### Use a command as the health check

When a plain URL check isn't enough (no `/health` route, or the service needs
signed requests), let a command decide whether the service is Reachable:

```yaml
  - name: oauth
    baseUrl: http://127.0.0.1:8080
    healthCommand: access-token   # one of the service's commands; exit 0 = Reachable
    healthEverySec: 0             # 0 (default) = on demand; or 15–86400 to re-run in the background
    commands:
      - name: access-token
        run: bru run "Access Token B2B.yml" --env local
        cwd: D:/path/to/your/bruno-collection
```

- With `healthEverySec: 0` the command runs **on Sync, at startup and when
  you press ⟳ Check now** on the service's page, never on its own. Commands
  can be slow and have side effects (this one issues a token each time).
- The status shows how old it is ("Reachable · checked 14 min ago"), and on
  failure the last line of output as the reason.
- `healthCommand` replaces `healthCheck` for that service; use one or the other.
- A health check and a manual run of the same command never overlap. Health
  checks don't appear in the command's run history.

Commands are what experiments will run later, before, during and after a
fault. For now you run them by hand.

### Apply your changes

Changes saved from the UI are applied straight away. After editing the file:

1. Check the file: `go run ./cmd/entropy validate`
2. In the UI, press **⟳ Sync** on the Environment page (or restart Entropy).

Sync matches records by name:

| You did in the file | Entropy does |
|---|---|
| Added a name | Adds it, creates its proxy |
| Changed a field | Updates it, updates the proxy |
| Removed a name | Keeps it, marked **Missing from config** |
| Made a mistake | Changes nothing, lists every problem |

---

## 4. Read the statuses

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

## 5. Apply a fault

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
- If Toxiproxy restarts (or is reset from outside Entropy) during a fault,
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

While the slowed-down request runs (a few seconds with Bruno), everything
using that dependency through the proxy is 1.5 s slower. Entropy pauses its
own checks of the dependency meanwhile. It can't verify during a fault, and
a port-only health check never reaches the dependency, so it isn't offered.

Traffic counters can't do this job: Toxiproxy counts a connection's bytes
only when it closes, so pooled connections stay invisible, and it can't
tell services apart.

## 6. Stop all faults

Press **Stop all faults** in the top bar, on any page. It ends every fault
Entropy started, then enables every proxy and removes every toxic, including
ones added outside Entropy. Entropy also does this on startup and on exit, so
killing Entropy in the middle of a fault leaves nothing broken after the next
start. If Toxiproxy isn't reachable at startup, Entropy does it as soon as
Toxiproxy answers.

---

## 7. Troubleshooting

| Problem | Fix |
|---|---|
| `cannot listen on 127.0.0.1:7400` | Another program has the port. Use `go run ./cmd/entropy up -addr 127.0.0.1:7500`. |
| Banner: *Toxiproxy is not reachable* | Start the containers (`podman compose … up -d`). With `auto`, Entropy finds Toxiproxy again within about 15 s; with a fixed host, check it's your container host (step 3) and press Sync. |
| Sync warning: *Toxiproxy didn't answer on port 8474 at any of: …* | The containers aren't running, or Toxiproxy is on a host Entropy doesn't know. Start them, or write the host instead of `auto`. |
| Everything Unreachable after a reboot | With a fixed host, the Podman VM address may have changed: switch to `auto` (step 3), or update the Toxiproxy address (Environment → Settings) and each dependency's address. |
| Dependency Unreachable: *no reply to PING* (or similar) | Toxiproxy is up but the real dependency isn't. Check the container and `upstream`. |
| Dependency Unreachable: *no proxy in Toxiproxy* | Entropy recreates missing proxies within about 5 s. If it persists, Entropy's log says why (e.g. the port is in use). |
| New dependency stays Unreachable (or *dial tcp …: i/o timeout*) | Its port isn't published on the Toxiproxy container. Add it under `ports:` in the compose file and recreate the container: `podman compose -f examples/compose.yaml up -d --force-recreate toxiproxy`. `podman start` keeps the old ports. |
| Save in the UI: *the config file changed since it was read* | The file was edited elsewhere meanwhile. Cancel, open Edit again, redo the change. |
| *The UI is not built yet* | Run `npm install && npm run build` in `web/`, then restart Entropy. |
| CIDM: *resolves to …, not 127.0.0.1* | The hosts-file line is missing or DNS is cached. Add it and run `ipconfig /flushdns`. |
| Faults have no effect on my service | The service connects to the real dependency. Press **Verify** on the dependency's page to confirm, then point the service at the `address` in `entropy.yaml`. |
| Verify says *Not seen in the proxy*, but the service is set up right | The request may not touch the dependency (e.g. answered from a cache). Pick a request that does, from the list next to **Verify**. |

---

## For contributors

### Develop the UI with hot reload

Run `go run ./cmd/entropy up`, then in `web/` run `npm run dev` and open
http://localhost:5173. API calls go to Entropy on :7400.

### Tests

```sh
go test ./...
```

On Windows, if Defender blocks test binaries in the temp folder, build them
elsewhere: `go test -c -o .entropy/t.exe ./internal/config && .entropy/t.exe`.

### Code layout

```
cmd/entropy/        CLI: up, validate, run
internal/config/    load and validate entropy.yaml; edit it in place for the UI
internal/command/   run a command line with a timeout, kill its process tree
internal/store/     SQLite: environment, dependencies, services; sync reconcile
internal/toxiproxy/ minimal Toxiproxy API client
internal/containerhost/ find the container host for "auto" addresses
internal/faults/    apply faults through Toxiproxy, remove them when their time is up
internal/checks/    protocol-level checks through the proxy
internal/app/       sync, status monitor, faults, view for the UI
internal/server/    JSON API + embedded UI
web/                React + TypeScript + Vite UI
```

### API

| Method | Path | Does |
|---|---|---|
| GET | `/api/environment` | Environment, dependencies, services and statuses |
| POST | `/api/sync` | Re-read `entropy.yaml` (422 with errors if invalid) |
| GET | `/api/config` | `entropy.yaml` as written (no defaults), plus a `hash` of its content |
| PATCH | `/api/config` | Save one change and sync: `{hash, <part>}` with one of `environment`, `dependency`, `service`, `addDependency`, `addService`. 409 if the file changed since `hash`, 422 if the result is invalid |
| POST | `/api/dependencies/{name}/fault` | Apply a fault: `{type, durationSec, …options}` (see step 5: `latencyMs`, `jitterMs`, `timeoutMs`, `downSec`, `upSec`). 409 if it already has one, 422 if invalid |
| DELETE | `/api/dependencies/{name}/fault` | Stop the dependency's fault now |
| POST | `/api/dependencies/{name}/verify` | Check that a service goes through the proxy: `{service, command}` (empty command = its health check). 409 during a fault, 422 if there's no request to run |
| POST | `/api/faults/stop-all` | End every fault, then reset Toxiproxy |
| POST | `/api/services/{service}/commands/{command}/run` | Run a command; answers when it finishes (409 if already running) |
| GET | `/api/command-runs/{id}` | One recorded run with its full output |
| POST | `/api/services/{service}/check` | Run the service's health command now |

Data lives in `.entropy/entropy.db` (SQLite). Deleting it is safe: the next
sync rebuilds it from `entropy.yaml`.
