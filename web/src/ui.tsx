import type { Run, Status } from './api'

export type Tone = 'ok' | 'warn' | 'bad' | 'idle'

export function Dot({ tone }: { tone: Tone }) {
  return <span className={`dot ${tone}`} aria-hidden />
}

const labels: Record<Status['state'], [string, Tone]> = {
  reachable: ['Reachable', 'ok'],
  unreachable: ['Unreachable', 'bad'],
  fault_active: ['Fault active', 'warn'],
  not_checked: ['Not checked yet', 'idle'],
}

export function StatusLabel({ status, missing }: { status: Status; missing?: boolean }) {
  if (missing) {
    return (
      <span className="status">
        <Dot tone="idle" /> Missing from config
      </span>
    )
  }
  const [label, tone] = labels[status.state]
  return (
    <span className="status" title={status.detail}>
      <Dot tone={tone} /> {label}
    </span>
  )
}

const verdicts: Record<string, string> = { passed: 'ok', failed: 'bad', inconclusive: 'warn', aborted: '' }

// RunBadge shows a run's verdict, or its current phase while it runs.
export function RunBadge({ run }: { run: Run }) {
  if (run.state !== 'done') return <span className="badge">Running · {run.state}</span>
  return <span className={`badge ${verdicts[run.verdict ?? ''] ?? ''}`}>{run.verdict}</span>
}

// Trend shows runs' verdicts as dots, oldest left, newest right.
export function Trend({ runs, onOpen }: { runs: Run[]; onOpen: (id: string) => void }) {
  return (
    <span className="trend">
      {[...runs].reverse().map((r) => (
        <button
          key={r.id}
          className={`trend-dot ${r.state === 'done' ? r.verdict : 'running'}`}
          title={`${r.state === 'done' ? r.verdict : 'running'} · v${r.version} · ${new Date(r.startedAt).toLocaleString()}${r.reason ? ` — ${r.reason}` : ''}`}
          onClick={(e) => { e.stopPropagation(); onOpen(r.id) }}
        />
      ))}
    </span>
  )
}

export function seconds(fromIso: string, toIso?: string): string {
  const ms = (toIso ? new Date(toIso).getTime() : Date.now()) - new Date(fromIso).getTime()
  return `${(Math.max(0, ms) / 1000).toFixed(1)} s`
}

export function timeAgo(iso?: string): string {
  if (!iso) return 'never'
  const seconds = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (seconds < 10) return 'just now'
  if (seconds < 60) return `${seconds} s ago`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} h ago`
  return new Date(iso).toLocaleString()
}
