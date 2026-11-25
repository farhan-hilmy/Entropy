# REST API

| Method | Path | Does |
|---|---|---|
| GET | `/api/environment` | Environment, dependencies, services and statuses |
| POST | `/api/sync` | Re-read `entropy.yaml` (422 with errors if invalid) |
| GET | `/api/config` | `entropy.yaml` as written (no defaults), plus a `hash` of its content |
| PATCH | `/api/config` | Save one change and sync: `{hash, <part>}` with one of `environment`, `dependency`, `service`, `addDependency`, `addService`, `addCommand` (`{service, name, run, cwd, timeoutSec}`), `command` (same fields: replaces that command's), `removeCommand` (`{service, name}`) (the three command parts are not in the UI), `experiment` (`{name, yaml}`: replaces that experiment, or adds one when `name` is empty), `removeExperiment` (`{name}`), `experimentCap` (`{name, maxDurationSec}`: sets only that line). 409 if the file changed since `hash`, 422 if the result is invalid |
| GET | `/api/experiments/{name}/source` | One experiment as written in `entropy.yaml`, comments included, plus the file's `hash`, for the editor |
| POST | `/api/dependencies/{name}/fault` | Apply a fault: `{type, durationSec, …options}` (see [Apply a fault](faults.md#apply-a-fault): `latencyMs`, `jitterMs`, `timeoutMs`, `downSec`, `upSec`). 409 if it already has one, 422 if invalid |
| DELETE | `/api/dependencies/{name}/fault` | Stop the dependency's fault now |
| POST | `/api/dependencies/{name}/verify` | Check that a service goes through the proxy: `{service, command}` (empty command = its health check). 409 during a fault, 422 if there's no request to run |
| GET | `/api/experiments` | Experiments with their version and last run |
| POST | `/api/experiments/{name}/run` | Start a run in the background; answers with it at once. 409 while another run is in progress |
| GET | `/api/runs` | Latest runs, newest first (`?experiment=` narrows them) |
| GET | `/api/runs/{id}` | One run: its snapshot, timeline and assertion results |
| POST | `/api/runs/{id}/stop` | Abort the run; answers once its faults are removed |
| POST | `/api/runs/{id}/rerun` | Run a finished run's experiment again exactly as it ran (addresses from the current setup); answers with the new run. 409 if it hasn't finished or another run is in progress |
| GET | `/api/runs/{id}/experiment.yaml` | The run's experiment as YAML, to paste under `experiments:` |
| POST | `/api/faults/stop-all` | End every fault, then reset Toxiproxy |
| POST | `/api/services/{service}/commands/{command}/run` | Run a command; answers when it finishes (409 if already running) |
| GET | `/api/command-runs/{id}` | One recorded run with its full output |
| POST | `/api/services/{service}/check` | Run the service's health command now |

Data lives in `.entropy/entropy.db` (SQLite). Deleting it is safe: the next
sync rebuilds it from `entropy.yaml`.
