// Types mirror internal/app View in the Go backend.

export type State = 'not_checked' | 'reachable' | 'unreachable' | 'fault_active'

export interface Status {
  state: State
  detail?: string
  checkedAt?: string
}

export interface SyncSummary {
  added: string[]
  updated: string[]
  missing: string[]
}

export interface SyncResult {
  at: string
  summary?: SyncSummary
  warnings?: string[]
  error?: string
}

export interface Dependency {
  id: string
  name: string
  kind: string
  address: string
  upstream: string
  listen: string
  redirect: boolean
  missing: boolean
  status: Status
  ready: boolean
  notReadyReason?: string
  usedBy: string[]
  fault?: Fault
  routes: Record<string, RouteCheck> // last route check, by service
  verifying: boolean
}

// Mirrors internal/app RouteCheck: does the service reach the dependency through its proxy?
export interface RouteCheck {
  service: string
  dependency: string
  state: 'verified' | 'not_seen' | 'unknown'
  detail: string
  trigger: string
  normalMs: number
  slowedMs: number // the same request with latencyMs of latency on the proxy
  latencyMs: number
  checkedAt: string
}

// Mirrors internal/faults: Types, Spec and Fault.
export const faultTypes = ['latency', 'drop', 'timeout', 'outage', 'flap'] as const
export type FaultType = (typeof faultTypes)[number]
export const maxFaultDurationSec = 3600

// Fields that don't apply to the type must be left out.
export interface FaultSpec {
  type: FaultType
  durationSec: number
  latencyMs?: number
  jitterMs?: number
  timeoutMs?: number
  downSec?: number
  upSec?: number
}

export interface Fault extends FaultSpec {
  dependency: string
  startedAt: string
  endsAt: string
  // removing: its time is up or it was stopped; error is why the last removal attempt failed.
  removing: boolean
  error?: string
}

export interface CommandRun {
  id: string
  service: string
  command: string
  run: string
  startedAt: string
  durationMs: number
  exitCode: number
  timedOut: boolean
  error?: string
  output?: string
}

export interface Command {
  name: string
  run: string
  cwd: string
  timeoutSec: number
  running: boolean
  lastRun?: CommandRun
  recent: CommandRun[]
}

export interface Service {
  id: string
  name: string
  baseUrl: string
  healthCheck: string
  healthCommand: string
  healthEverySec: number
  checking: boolean
  uses: string[]
  commands: Command[]
  missing: boolean
  status: Status
}

// Mirrors internal/config Experiment (as stored, with defaults).
export interface ExperimentDefinition {
  name: string
  description?: string
  maxDurationSec: number
  faults: (FaultSpec & { dependency: string; startOffsetSec: number })[] | null
  triggers: {
    name: string
    phase: 'baseline' | 'during' | 'after'
    command?: string
    service?: string
    request?: { method: string; path: string }
    repeat: number
    everySec: number
  }[]
  recoveryWindowSec: number
  assertions: {
    type: 'response' | 'count' | 'eventual' | 'recovery'
    trigger?: string
    service?: string
    expect: { exitCode?: number; outputContains?: string; status?: number; bodyContains?: string }
    atLeast?: number
    withinSec?: number
  }[]
}

export type Verdict = 'passed' | 'failed' | 'inconclusive' | 'aborted'

// Mirrors internal/store Run. state is the phase while it runs, then "done".
export interface Run {
  id: string
  experiment: string
  version: number
  state: string
  verdict?: Verdict
  reason?: string
  startedAt: string
  endedAt?: string
  snapshot?: { experiment: ExperimentDefinition }
  events?: { at: string; type: 'phase' | 'fault' | 'trigger' | 'response' | 'system'; message: string; data?: unknown }[]
  assertions?: { index: number; outcome: 'pass' | 'fail' | 'not_evaluated'; detail: string }[]
}

export interface Experiment {
  name: string
  version: number
  definition: ExperimentDefinition
  missing: boolean
  lastRun?: Run
  running: boolean
}

export interface Environment {
  name: string
  activeRun?: string
  configPath: string
  syncedAt?: string
  status: 'ready' | 'degraded' | 'unreachable' | 'checking'
  activeFaults: number
  toxiproxy: {
    api: string
    status: Status
    // auto: toxiproxy.api's host is "auto"; detected is the host found, absent while none answers.
    auto: boolean
    detected?: { host: string; source: string }
  }
  lastSync: SyncResult
  dependencies: Dependency[]
  services: Service[]
}

// The config file as written, without defaults (internal/config Config).
// Lists the file leaves out come back as null.
export interface RawConfig {
  environment: { name: string }
  toxiproxy: { api: string }
  dependencies: { name: string; kind: string; address: string; upstream: string; listen: string; redirect: boolean }[] | null
  services: {
    name: string
    baseUrl: string
    healthCheck: string
    healthCommand: string
    healthEverySec: number
    uses: string[] | null
    commands: { name: string }[] | null
  }[] | null
}

export interface ConfigSnapshot {
  hash: string
  config: RawConfig
}

export interface EnvironmentFields { name: string; toxiproxyApi: string }
export interface DependencyFields { name: string; address: string; upstream: string; listen: string; redirect: boolean }
export interface ServiceFields {
  name: string
  baseUrl: string
  healthCheck: string
  healthCommand: string
  healthEverySec: number
  uses: string[]
}

export interface NewDependency { name: string; kind: string; address: string; upstream: string; listen: string }
// Commands are added in the config file, so a new service has none.
export interface NewService { name: string; baseUrl: string; healthCheck: string; uses: string[] }

// Mirrors config.Kinds in the Go backend.
export const dependencyKinds = ['redis', 'mongodb', 'postgres', 'rabbitmq', 'http', 'https']

// Exactly one part per edit. Empty optional fields are removed from the file.
export type ConfigEdit =
  | { environment: EnvironmentFields }
  | { dependency: DependencyFields }
  | { service: ServiceFields }
  | { addDependency: NewDependency }
  | { addService: NewService }

export interface EditResult {
  hash: string
  sync: SyncResult
}

async function errorFrom(res: Response): Promise<Error> {
  const body = await res.json().catch(() => ({ error: res.statusText }))
  return new Error(body.error ?? res.statusText)
}

export const api = {
  async environment(): Promise<Environment> {
    const res = await fetch('/api/environment')
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  // An invalid config comes back as 422 with a SyncResult whose error is set.
  async sync(): Promise<SyncResult> {
    const res = await fetch('/api/sync', { method: 'POST' })
    if (!res.ok && res.status !== 422) throw await errorFrom(res)
    return res.json()
  },

  async config(): Promise<ConfigSnapshot> {
    const res = await fetch('/api/config')
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  // Fails with 409 if the file changed since `hash` was read, 422 if the edit makes it invalid.
  async editConfig(hash: string, edit: ConfigEdit): Promise<EditResult> {
    const res = await fetch('/api/config', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hash, ...edit }),
    })
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  // Fails with 409 if the dependency already has a fault, 422 if the spec is invalid.
  async startFault(dependency: string, spec: FaultSpec): Promise<Fault> {
    const res = await fetch(`/api/dependencies/${encodeURIComponent(dependency)}/fault`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(spec),
    })
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  async stopFault(dependency: string): Promise<void> {
    const res = await fetch(`/api/dependencies/${encodeURIComponent(dependency)}/fault`, { method: 'DELETE' })
    if (!res.ok) throw await errorFrom(res)
  },

  // Runs one of the service's requests (command; empty = its health check) and
  // reports whether it went through the dependency's proxy. 409 while a fault is on it.
  async verifyRoute(dependency: string, service: string, command: string): Promise<RouteCheck> {
    const res = await fetch(`/api/dependencies/${encodeURIComponent(dependency)}/verify`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ service, command }),
    })
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  async experiments(): Promise<Experiment[]> {
    const res = await fetch('/api/experiments')
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  // Starts the run in the background. 409 while another run is in progress.
  async startRun(experiment: string): Promise<Run> {
    const res = await fetch(`/api/experiments/${encodeURIComponent(experiment)}/run`, { method: 'POST' })
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  async runs(experiment = ''): Promise<Run[]> {
    const res = await fetch(`/api/runs${experiment ? `?experiment=${encodeURIComponent(experiment)}` : ''}`)
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  async run(id: string): Promise<Run> {
    const res = await fetch(`/api/runs/${encodeURIComponent(id)}`)
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  // Resolves once the run is aborted and its faults are removed.
  async stopRun(id: string): Promise<void> {
    const res = await fetch(`/api/runs/${encodeURIComponent(id)}/stop`, { method: 'POST' })
    if (!res.ok) throw await errorFrom(res)
  },

  async stopAllFaults(): Promise<void> {
    const res = await fetch('/api/faults/stop-all', { method: 'POST' })
    if (!res.ok) throw await errorFrom(res)
  },

  // Resolves when the command finishes, at most its timeoutSec later.
  async runCommand(service: string, command: string): Promise<CommandRun> {
    const path = `/api/services/${encodeURIComponent(service)}/commands/${encodeURIComponent(command)}/run`
    const res = await fetch(path, { method: 'POST' })
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  // Runs the service's health command now.
  async checkService(service: string): Promise<Status> {
    const res = await fetch(`/api/services/${encodeURIComponent(service)}/check`, { method: 'POST' })
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },

  async commandRun(id: string): Promise<CommandRun> {
    const res = await fetch(`/api/command-runs/${encodeURIComponent(id)}`)
    if (!res.ok) throw await errorFrom(res)
    return res.json()
  },
}
