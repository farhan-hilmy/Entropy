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

export interface Environment {
  name: string
  configPath: string
  syncedAt?: string
  status: 'ready' | 'degraded' | 'unreachable' | 'checking'
  activeFaults: number
  toxiproxy: { api: string; status: Status }
  lastSync: SyncResult
  dependencies: Dependency[]
  services: Service[]
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
