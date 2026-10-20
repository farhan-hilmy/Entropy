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

> **Current status:** environment setup only (MVP build step 1). You can
> register dependencies and services, see live statuses and stop all faults.
> Experiments and runs are not built yet.

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

**Step 2 – check where Podman publishes ports**

On Windows, Podman usually publishes container ports on the Podman VM's
address, not on `localhost`. Check which one answers:

```sh
curl http://localhost:8474/version
curl http://172.28.142.159:8474/version   # replace with your Podman VM address
```

Whichever prints `{"version": ...}` is your **container host**. Put it in
`entropy.yaml`, in both `toxiproxy.api` and the dependency's `address`.

**Step 3 – build the UI** (only needed once, and after UI changes)

```sh
cd web
npm install
npm run build
cd ..
```

**Step 4 – start Entropy**

```sh
go run ./cmd/entropy up
```

You should see:

```
msg=synced config=entropy.yaml added="[dependency redis service oauth]"
msg="cleared leftover faults"
msg="Entropy is running" url=http://127.0.0.1:7400
```

**Step 5 – open http://127.0.0.1:7400**

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
       address: 172.28.142.159:27017  # where your service connects: the proxy
       upstream: mongodb:27017        # the real MongoDB, as Toxiproxy sees it
   ```

   Entropy creates the proxy itself when it syncs, and recreates it if
   Toxiproxy restarts. You don't need to create it in Toxiproxy by hand.

3. **Your service pointed at the proxy.** Change your service's connection
   setting (e.g. `MONGO_URI`) to the `address` above, **not** the real
   MongoDB. If the service connects directly, faults have no effect on it.

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

**Experiment-ready** means the dependency is Reachable. If it isn't, the
Dependency page says why.

## 5. Stop all faults

Press **Stop all faults** in the top bar, on any page. It enables every proxy
and removes every toxic. Entropy also does this on startup and on exit,
because Toxiproxy faults never expire on their own.

---

## 6. Troubleshooting

| Problem | Fix |
|---|---|
| `cannot listen on 127.0.0.1:7400` | Another program has the port. Use `go run ./cmd/entropy up -addr 127.0.0.1:7500`. |
| Banner: *Toxiproxy is not reachable* | Start the containers (`podman compose … up -d`), check `toxiproxy.api` uses your container host (step 2), press Sync. |
| Everything Unreachable after a reboot | The Podman VM address may have changed. Repeat step 2, then update the Toxiproxy address (Environment → Settings) and each dependency's address. |
| Dependency Unreachable: *no reply to PING* (or similar) | Toxiproxy is up but the real dependency isn't. Check the container and `upstream`. |
| Dependency Unreachable: *no proxy in Toxiproxy* | Entropy recreates missing proxies within about 5 s. If it persists, Entropy's log says why (e.g. the port is in use). |
| New dependency stays Unreachable | Its port isn't published on the Toxiproxy container. Add it under `ports:` in the compose file and restart the container. |
| Save in the UI: *the config file changed since it was read* | The file was edited elsewhere meanwhile. Cancel, open Edit again, redo the change. |
| *The UI is not built yet* | Run `npm install && npm run build` in `web/`, then restart Entropy. |
| CIDM: *resolves to …, not 127.0.0.1* | The hosts-file line is missing or DNS is cached. Add it and run `ipconfig /flushdns`. |
| Faults have no effect on my service | The service connects to the real dependency. Point it at the `address` in `entropy.yaml`. |

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
internal/checks/    protocol-level checks through the proxy
internal/app/       sync, status monitor, Stop all faults, view for the UI
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
| POST | `/api/faults/stop-all` | Reset Toxiproxy |
| POST | `/api/services/{service}/commands/{command}/run` | Run a command; answers when it finishes (409 if already running) |
| GET | `/api/command-runs/{id}` | One recorded run with its full output |
| POST | `/api/services/{service}/check` | Run the service's health command now |

Data lives in `.entropy/entropy.db` (SQLite). Deleting it is safe: the next
sync rebuilds it from `entropy.yaml`.
