# Contributing

## Develop the UI with hot reload

Run `go run ./cmd/entropy up`, then in `web/` run `npm run dev` and open
http://localhost:5173. API calls go to Entropy on :7400.

## Tests

```sh
go test ./...
```

On Windows, if Defender blocks test binaries in the temp folder, build them
elsewhere: `go test -c -o .entropy/t.exe ./internal/config && .entropy/t.exe`.

Run the race detector after changing anything concurrent (the run engine,
faults, checks). It needs cgo, which Go on Windows usually lacks, so run it
in a Linux container (from Git Bash; paths as for your clone):

```sh
MSYS_NO_PATHCONV=1 podman run --rm -v "$(pwd -W):/src:ro" -w /src \
  -e GOFLAGS=-buildvcs=false -e GOCACHE=/tmp/gocache \
  golang:1.26.3 go test -race ./...
```

## Code layout

```
cmd/entropy/        CLI: up, validate, run, mcp
internal/config/    load and validate entropy.yaml; edit it in place for the UI
internal/command/   run a command line with a timeout, kill its process tree
internal/store/     SQLite: environment, dependencies, services, experiment versions, runs; sync reconcile
internal/toxiproxy/ minimal Toxiproxy API client, and the built-in Toxiproxy server
internal/containerhost/ find the container host for "auto" addresses and upstreams
internal/faults/    apply faults through Toxiproxy, remove them when their time is up
internal/checks/    protocol-level checks through the proxy
internal/localguard/ keep web pages out of the local APIs (Host and cross-origin checks)
internal/mcpserver/ entropy mcp: MCP tools for agents, calling the REST API
internal/app/       sync, status monitor, faults, route checks, run engine, view for the UI
internal/server/    JSON API + embedded UI
web/                React + TypeScript + Vite UI
```
