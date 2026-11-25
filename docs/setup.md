# Register your own dependencies and services

Everything Entropy knows about your environment comes from `entropy.yaml`.
You can edit the file, or use the UI:

- **Add** a dependency or service with **+ Add** on the Dependencies or
  Services page.
- **Edit** one with **Edit** on its page, or edit the environment name and
  Toxiproxy address under **Settings** on the Environment page.

The UI writes to `entropy.yaml` and syncs. It changes only the values you
edited, so your comments and layout stay. It refuses to save if the file
changed since you opened the form, or if the result would be invalid.
**Commands can't be added or changed in the UI**, because Entropy runs them
on your machine: edit the file, or let an agent add them through `entropy
mcp` ([Agents](agents.md)), which shows you the command line first. Removing
something is also done in the file.

## Add a dependency

You need two things for each dependency:

1. **An entry in `entropy.yaml`:**

   ```yaml
   dependencies:
     - name: mongodb                  # also the proxy's name in Toxiproxy
       kind: mongodb                  # redis | mongodb | postgres | rabbitmq | http | https
       address: auto:27017            # where your service connects: the proxy (127.0.0.1:27017)
       upstream: 10.0.0.5:27017   # the real MongoDB; auto:<port> if it's a container
   ```

   Entropy opens the proxy's port when it syncs, so nothing else needs
   changing: no compose file, no restart. By default the proxy listens on
   `127.0.0.1` only; set `listen: 0.0.0.0:27017` if services on other
   machines or in containers must reach it.

2. **Your service pointed at the proxy.** Change your service's connection
   setting (e.g. `MONGO_URI`) to the `address` above (`127.0.0.1:27017`),
   **not** the real MongoDB. If the service connects directly, faults have
   no effect on it. To check, press **Verify** next to the service under
   *Used by services* on the dependency's page (see [Verify that a service uses the proxy](faults.md#verify-that-a-service-uses-the-proxy)).

The proxy's port must be free on this machine. If something else holds it,
e.g. a Toxiproxy container you used before, the dependency says *port … is
already in use*: stop that program, or set `listen:` to another port.

## Use your own Toxiproxy

To use a Toxiproxy you run yourself, in Docker, Podman, or shared in an
environment like alpha, set its API in `entropy.yaml` (or under **Settings**
on the Environment page):

```yaml
toxiproxy:
  api: http://auto:8474     # auto = the container host, detected on every sync
```

Entropy then works as before the built-in Toxiproxy existed:

- **Ports:** each proxy port must be published on the Toxiproxy container:
  add it under `ports:` in the compose file and recreate the container
  (`podman compose -f examples/compose.yaml --profile toxiproxy up -d --force-recreate toxiproxy`;
  `podman start` keeps the old ports). Entropy can't do that for you.
- **`upstream`** is the dependency as that Toxiproxy sees it, e.g.
  `redis:6379` on the compose network. `auto` isn't allowed there.
- **`address: auto:<port>`** is the container host, and proxies listen on
  `0.0.0.0` inside the container by default.
- **Detection:** Entropy finds the container host by asking for Toxiproxy's
  version on the API's port. For a Toxiproxy elsewhere, write its host
  instead of `auto`, e.g. `api: http://10.0.0.5:8474`; a dependency's `auto`
  then means that host.

To switch back to the built-in one, remove `toxiproxy.api` (or empty it
under Settings) and press Sync; stop the Toxiproxy container if it holds a
port a proxy needs.

> **Upgrading:** before the built-in Toxiproxy, an `entropy.yaml` without
> `toxiproxy.api` meant `http://localhost:8474`. Such a config now uses the
> built-in one. To keep your container, write `api: http://localhost:8474`.

## Add a service

```yaml
services:
  - name: oauth
    baseUrl: http://localhost:8000
    healthCheck: /health      # optional; without it Entropy only checks the port
    uses: [redis, mongodb]    # dependencies this service talks to
```

## External HTTPS APIs

The certificate only matches the real hostname, so the service must keep
using it. Redirect the hostname to Toxiproxy with the hosts file while
testing, and set `redirect: true`:

```yaml
  - name: payments-api
    kind: https
    address: api.example.com:443
    upstream: api.example.com:443
    redirect: true
```

In `C:\Windows\System32\drivers\etc\hosts` (admin), add:

```
127.0.0.1 api.example.com  # chaos-payments-api
```

then run `ipconfig /flushdns`. Remove the line when you're done. Entropy
marks the dependency ready only when the hostname resolves to 127.0.0.1 **and** the
HTTPS handshake succeeds.

## Add commands (e.g. Bruno requests)

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

### Use a command as the health check

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

Experiments can run commands as triggers (`command: oauth/access-token`),
before, during and after a fault; see [Experiments](experiments.md).

## Apply your changes

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
