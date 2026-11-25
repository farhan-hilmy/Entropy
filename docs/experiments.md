# Run an experiment

An experiment breaks dependencies on purpose, sends requests to a service
before, during and after, and checks how it behaved. Experiments live
under `experiments:` in `entropy.yaml` (`entropy.example.yaml` has a full
sample with comments). Write them there and press **Sync**, or in the UI:
on the **Experiments** page, **New experiment** opens the editor with a
working start built from your dependencies and service commands, and
**Edit** opens one experiment as YAML. The editor lists the names you can
use (dependencies, `service/command`s, fault types, phases, assertions).
**Save** checks the whole config and syncs; an invalid experiment is not
saved and the error says why. Only that experiment's lines in the file
change, so your comments elsewhere stay as they are, and comments you write
inside it are kept too. **Save and run** starts it right away; **Remove**
deletes it from the file (its runs stay in **Runs**). Renaming an experiment
makes it a new one: past runs stay under the old name. Then press **Run** on
the **Experiments** page.

## Write one

```yaml
experiments:
  - name: redis-outage-oauth
    maxDurationSec: 180                    # safety cap (default 600)
    faults:                                # none = an integration test
      - { dependency: redis, type: outage, durationSec: 30 }   # + startOffsetSec, and the fault's options
    triggers:                              # per phase: baseline | during | after
      - { name: before, phase: baseline, command: oauth/access-token }
      - { name: during, phase: during, service: oauth, request: { method: POST, path: /token }, repeat: 5, everySec: 5 }
      - { name: after, phase: after, service: oauth, request: { method: POST, path: /token }, repeat: 10, everySec: 2 }
    recoveryWindowSec: 30                  # how long the "after" phase watches (default 30)
    assertions:
      - { type: response, trigger: before, expect: { exitCode: 0 } }               # every result matches
      - { type: count, trigger: during, expect: { status: 503 }, atLeast: 4 }      # at least N match
      - { type: eventual, trigger: after, expect: { status: 200 }, withinSec: 20 } # one matches in time
      - { type: recovery, withinSec: 20 }  # the service's health check passes again in time
```

A trigger is either one of the service's commands (`service/command`; expect
`exitCode`, `outputContains`) or an HTTP `request` to its `baseUrl` (expect
`status`, `bodyContains`). Both can also expect `maxMs`, the longest a result
may take, e.g. "a clean error within 10 s, not a hang":

```yaml
      - { type: response, trigger: during, expect: { status: 503, maxMs: 10000 } }
```

## How a run goes

A run goes:

1. **Preflight**, cheapest first:
   - each service is healthy: a health check that failed in the last
     minute stops the run, naming the service (an older or missing result
     doesn't block; the baseline still catches a down service);
   - each faulted dependency is Reachable and has no other fault;
   - each service that **uses** a faulted dependency (its `uses:` list) is
     verified to go through the proxy: the same check as **Verify**, run
     with the service's first trigger in the experiment, a command or an
     HTTP request. Name another one with `verifyWith`, per service, when
     the first doesn't use the dependency:
     ```yaml
     verifyWith: { oauth: token-before }
     ```
     A faulted dependency that no service here uses is skipped, and Sync
     warns that breaking it may test nothing.
2. **Baseline**: the `baseline` triggers run; their assertions must hold (the
   steady state), or the run stops before any fault.
3. **During**: the faults are applied, then the `during` triggers run. Only
   results that finish while a fault is on count, and no new request
   starts once the faults end. A request still waiting when they end:
   - if it waited at least half the phase (or longer than a `maxMs` on it),
     it **hung**: its result is *"no answer within N s"*, which matches no
     expectation, so its assertions fail. The same goes for one that only
     got its answer once the faults were gone.
   - if it started near the end, it just ran out of time and doesn't count.

   Make the fault long enough for the requests you want (e.g. a Bruno run
   takes seconds). The same rule applies when the recovery window ends.
4. **Restore**: the faults are removed when their time is up.
5. **After**: the `after` triggers run and recovery is watched, for up to
   `recoveryWindowSec`.
6. **Evaluate**: the verdict comes from the assertions.

## Verdicts

| Verdict | Meaning |
|---|---|
| **passed** | Every assertion holds: resilient. |
| **failed** | An assertion was violated. The report says which, and what it got. |
| **inconclusive** | It couldn't be judged: a dependency wasn't Reachable, the service doesn't go through the proxy, the baseline failed, Toxiproxy went away, or a fault couldn't be removed. |
| **aborted** | You pressed **Stop run** or **Stop all faults**, it hit `maxDurationSec`, or Entropy stopped during it. Its faults are removed either way. |

## Will it fit?

**Will it fit?** On Sync and on the Experiments page, Entropy checks each
experiment's timing against how long its requests actually took, and warns
(it never blocks) when they can't fit, e.g. *"token-during: 4 runs of ~20 s
(measured during the fault) need ~79 s, but the faults last 30 s, so only 1
would count"*. It also catches an `after` phase longer than
`recoveryWindowSec`, a `recovery` `withinSec` shorter than the health check
itself, and a run longer than `maxDurationSec`. Whether requests fit uses
their typical (median) time. The run's total against `maxDurationSec` (the
preflight's route checks, the baseline, the faults and the recovery window)
uses the slowest time measured instead, since being aborted wastes a whole
run, and it says how many measurements that comes from. That warning has a
**Set maxDurationSec to N** button on the Experiments page: it writes the
smallest value that fits into that experiment's `maxDurationSec` line (only
that line changes, its comment stays) and syncs, so the warning goes away
because the cap now fits. Round it up by hand if you want a margin. Health
checks made while a dependency the service uses is down or faulted don't
count as measurements, since the service may be waiting out its own
timeouts. How sure the check is depends on what it has measured:

| Measured | From | Means |
|---|---|---|
| **full** | past runs of this experiment, `during` requests timed under the fault | warnings and "fits" are both based on real times |
| **partial** | only normal times: command runs, health checks, other phases | estimates are lower bounds (failures are often slower), so a warning is certain but "fits" isn't |
| **none** | a request was never run | no timing warnings, just a note to run it once |

Whatever was measured, faults plus `recoveryWindowSec` longer than
`maxDurationSec` is an error in `entropy validate`, since every run would be
aborted.

## Reports, reruns and history

The run page follows a run live (phase, assertions, timeline) and is its
report afterwards; **Runs** lists them all, filtered by experiment and
verdict, and **Experiments** shows each experiment's recent verdicts as
dots, oldest left. One experiment runs at a time. Each change to an
experiment makes a new version, and every run keeps an exact copy of what it
ran, so editing an experiment never changes a run.

On a finished run's report:

- **Rerun vN** repeats that run exactly: the same experiment version and the
  same service commands, whatever `entropy.yaml` says now. Only *where things
  are* (dependency addresses, service base URLs) comes from your current
  setup, so a rerun still works after e.g. the Podman VM's address changed;
  the new run's timeline says what moved, and links back to the original.
- **Run current (vN)** appears when the experiment has changed since, and
  runs what's in the file now.
- **Edit and rerun** opens the experiment in the editor as this run ran it.
  Change what you need; **Save and rerun** saves it to `entropy.yaml` as a
  new version (adding it back if it was removed) and runs it.
- **Copy as YAML** copies the run's experiment, ready to paste under
  `experiments:`, e.g. to bring back an older version and adjust it.
