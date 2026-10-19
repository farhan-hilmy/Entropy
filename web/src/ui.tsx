import type { Status } from './api'

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

export function FromConfig() {
  return <span className="hint">Defined in the config file</span>
}
