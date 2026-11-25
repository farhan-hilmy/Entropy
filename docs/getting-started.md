# Getting started

## Before you start

Install:

- **Go** 1.26 or newer
- **Node.js** 20 or newer
- **Podman** (or Docker), only for the demo Redis or other dependencies you
  run in containers, with the Podman machine running: `podman machine start`

## First run with the demo setup

The demo runs a Redis in a container, and Entropy puts its own proxy in
front of it.

**Step 1 – start Redis**

```sh
podman compose -f examples/compose.yaml up -d
```

or, if the container already exists, `podman start examples-redis-1`.

**Step 2 – create your config**

```sh
cp entropy.example.yaml entropy.yaml
```

`entropy.yaml` is yours: git ignores it, so your addresses and paths stay on
your machine. Change `entropy.example.yaml` only to update the template for
everyone.

**Step 3 – the container host (usually nothing to do)**

The template has:

```yaml
dependencies:
  - name: redis
    address: auto:6379     # the proxy: on this machine, 127.0.0.1:6379
    upstream: auto:16379   # the real Redis, on the container host
```

Entropy's proxies run on this machine, so `auto` in `address` is
`127.0.0.1`. Container ports, though, are published on the **container
host**: `localhost` with Docker Desktop and most setups, but on Windows
Podman usually publishes on the Podman VM's address (e.g.
`172.28.142.159`), which can change after a reboot. So `auto` in `upstream`
means the container host, found again on every sync.

To find it, Entropy tries the upstream's port on each likely host and uses
the first that answers: `localhost`, the host in `DOCKER_HOST` or
`CONTAINER_HOST` (tcp:// or ssh://), then on Windows the WSL VM's address.
It only asks WSL distributions that are already running, so it never starts
your Podman machine. The Environment page shows the host it found, and
Entropy's log says `container host detected host=… via=…`. If the
containers start after Entropy, it finds them within about 15 s.

A dependency that isn't in a container (e.g. a shared MongoDB) needs no
`auto`: write its address, e.g. `upstream: 10.0.0.5:27017`.

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
That's expected; replace it with your own service ([Set up your services](setup.md)).

Stop Entropy with `Ctrl+C`. It clears all faults and closes every proxy
port on exit.
