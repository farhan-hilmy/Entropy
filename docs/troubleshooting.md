# Troubleshooting

| Problem | Fix |
|---|---|
| `cannot listen on 127.0.0.1:7400` | Another program has the port. Use `go run ./cmd/entropy up -addr 127.0.0.1:7500`. |
| Dependency Unreachable: *no proxy: port … is already in use by another program* | Something else holds the proxy's port, often a Toxiproxy container you used before: `podman stop examples-toxiproxy-1`. Or set `listen:` to a free port (and point your service at it). Entropy retries every 5 s. |
| Sync warning: *nothing answered on port 16379 at any of: …* | The container behind an `auto` upstream isn't running. Start it (`podman compose -f examples/compose.yaml up -d`); Entropy finds it within about 15 s. |
| Dependency Unreachable: *no reply to PING* (or similar) | The proxy is up but the real dependency isn't. Check the container and `upstream`. |
| Everything Unreachable after a reboot | A written-out container host (e.g. the Podman VM's address) may have changed: use `auto` instead ([Getting started](getting-started.md#first-run-with-the-demo-setup), step 3), or update it and press Sync. |
| *Own Toxiproxy* — banner: *Toxiproxy is not reachable* | Start the container (`podman compose … --profile toxiproxy up -d`). With `auto`, Entropy finds Toxiproxy again within about 15 s; with a fixed host, check it and press Sync. |
| *Own Toxiproxy* — Sync warning: *Toxiproxy didn't answer on port 8474 at any of: …* | The container isn't running, or Toxiproxy is on a host Entropy doesn't know. Start it, or write the host instead of `auto`. |
| *Own Toxiproxy* — new dependency stays Unreachable (or *dial tcp …: i/o timeout*) | Its port isn't published on the Toxiproxy container. Add it under `ports:` and recreate the container (see [Use your own Toxiproxy](setup.md#use-your-own-toxiproxy)). |
| Save in the UI: *the config file changed since it was read* | The file was edited elsewhere meanwhile. Cancel, open Edit again, redo the change. |
| *The UI is not built yet* | Run `npm install && npm run build` in `web/`, then restart Entropy. |
| HTTPS dependency: *resolves to …, not 127.0.0.1* | The hosts-file line is missing or DNS is cached. Add it and run `ipconfig /flushdns`. |
| Faults have no effect on my service | The service connects to the real dependency. Press **Verify** on the dependency's page to confirm, then point the service at the `address` in `entropy.yaml`. |
| Verify says *Not seen in the proxy*, but the service is set up right | The request may not touch the dependency (e.g. answered from a cache). Pick a request that does, from the list next to **Verify**. |
| Run: *inconclusive: … doesn't seem to reach redis through the proxy* | Preflight verified the route with one of the experiment's commands, or the health check. Point the service at the proxy, or make sure that request uses the dependency. |
| Run: *inconclusive: the steady state didn't hold before any fault* | A `baseline` assertion failed: the service wasn't healthy to begin with. Its report shows what it got. |
| Run: *another experiment is running* | One runs at a time. Wait, or open it from the banner on Experiments and press **Stop run**. |
| Agent: *… there's no entropy.yaml in … to start it with* | Add `-config D:/path/to/entropy.yaml` after `mcp` in the `claude mcp add` line, or start `entropy up` yourself. |
| Agent: *starting it failed: started entropy up, but it stopped: …* | The rest of the message is Entropy's own error (e.g. the config is invalid, or the port is taken). The full log is `.entropy/entropy-up.log` next to `entropy.yaml`. |
| Agent: *Entropy isn't running at …* | Started with `-autostart=false`, or `-url` isn't on this machine: start that Entropy yourself. |
| API answers 403: *refused a request addressed to "…"* | Entropy only answers requests addressed to `localhost` or `127.0.0.1` (or the exact `-addr` host), so a web page can't reach it through a DNS trick. Use one of those in the URL. |
| API answers 403: *refused a change sent by a web page on another site* | Changes (POST, PATCH, DELETE) are refused when a browser sends them from another site, so a page you visit can't start a run or break a dependency. Entropy's UI, curl, scripts and agents are not affected. |
