import { useEffect, useState } from 'react'
import { api, faultTypes, maxFaultDurationSec, type Dependency, type Fault, type FaultSpec, type FaultType } from '../api'
import { Field, TextInput } from '../editing'

const descriptions: Record<FaultType, string> = {
  latency: 'Adds a delay to every response.',
  drop: 'Resets every connection (TCP RST), as if the server dropped it.',
  timeout: 'Lets nothing through: connections hang until the fault ends, or close after the timeout you set.',
  outage: 'Disables the proxy: open connections close and new ones are refused.',
  flap: 'Alternates between outage and normal, starting with an outage.',
}

// Text inputs, so a field can be empty: empty means the default.
interface Form {
  type: FaultType
  durationSec: string
  latencyMs: string
  jitterMs: string
  timeoutMs: string
  downSec: string
  upSec: string
}

const emptyForm: Form = { type: 'latency', durationSec: '30', latencyMs: '', jitterMs: '', timeoutMs: '', downSec: '', upSec: '' }

// The fields of the form's type only: the server rejects the others.
function toSpec(f: Form): FaultSpec {
  const n = (s: string) => (s.trim() === '' ? undefined : Number(s))
  const spec: FaultSpec = { type: f.type, durationSec: Number(f.durationSec) }
  if (f.type === 'latency') Object.assign(spec, { latencyMs: n(f.latencyMs), jitterMs: n(f.jitterMs) })
  if (f.type === 'timeout') Object.assign(spec, { timeoutMs: n(f.timeoutMs) })
  if (f.type === 'flap') Object.assign(spec, { downSec: n(f.downSec), upSec: n(f.upSec) })
  return spec
}

export function FaultPanel({ d, onChanged }: { d: Dependency; onChanged: () => void }) {
  const [form, setForm] = useState<Form>(emptyForm)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const update = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }))

  async function act(action: () => Promise<unknown>) {
    setBusy(true)
    setError(null)
    try {
      await action()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
      onChanged()
    }
  }

  return (
    <section className="panel">
      <h2>Fault</h2>
      {error && <div className="banner error"><pre>{error}</pre></div>}
      {d.fault ? (
        <ActiveFault fault={d.fault} busy={busy} onStop={() => act(() => api.stopFault(d.name))} />
      ) : (
        <>
          <p className="muted small">
            Applies a fault to this dependency's proxy for a fixed time, then removes it, even if you close this page.
          </p>
          <div className="form">
            <Field label="Type" hint={descriptions[form.type]}>
              <select className="input" value={form.type} onChange={(e) => update({ type: e.target.value as FaultType })}>
                {faultTypes.map((t) => <option key={t} value={t}>{t}</option>)}
              </select>
            </Field>
            <Field label="Duration (seconds)" hint={`1 to ${maxFaultDurationSec}. The fault is removed when it ends.`}>
              <TextInput value={form.durationSec} onChange={(durationSec) => update({ durationSec })} />
            </Field>
            {form.type === 'latency' && (
              <>
                <Field label="Latency (ms)"><TextInput value={form.latencyMs} placeholder="1000" onChange={(latencyMs) => update({ latencyMs })} /></Field>
                <Field label="Jitter (ms)" hint="Random ± on top of the latency."><TextInput value={form.jitterMs} placeholder="0" onChange={(jitterMs) => update({ jitterMs })} /></Field>
              </>
            )}
            {form.type === 'timeout' && (
              <Field label="Close connections after (ms)" hint="Leave empty to hold them open until the fault ends.">
                <TextInput value={form.timeoutMs} placeholder="0" onChange={(timeoutMs) => update({ timeoutMs })} />
              </Field>
            )}
            {form.type === 'flap' && (
              <>
                <Field label="Down for (seconds)"><TextInput value={form.downSec} placeholder="5" onChange={(downSec) => update({ downSec })} /></Field>
                <Field label="Up for (seconds)"><TextInput value={form.upSec} placeholder="5" onChange={(upSec) => update({ upSec })} /></Field>
              </>
            )}
          </div>
          <div className="edit-actions">
            <button className="btn primary" disabled={busy || d.missing} onClick={() => act(() => api.startFault(d.name, toSpec(form)))}>
              {busy ? 'Starting…' : 'Start fault'}
            </button>
            {d.usedBy.length > 0 && <span className="hint">Affects {d.usedBy.join(', ')} only if they connect through the proxy.</span>}
          </div>
        </>
      )}
    </section>
  )
}

function ActiveFault({ fault, busy, onStop }: { fault: Fault; busy: boolean; onStop: () => void }) {
  const now = useNow()
  const left = Math.max(0, Math.ceil((new Date(fault.endsAt).getTime() - now) / 1000))
  return (
    <>
      <dl className="facts">
        <dt>Type</dt><dd><strong>{fault.type}</strong> <span className="muted">— {describe(fault)}</span></dd>
        <dt>Duration</dt><dd>{fault.durationSec} s, started {new Date(fault.startedAt).toLocaleTimeString()}</dd>
        <dt>Ends</dt>
        <dd>
          {fault.removing ? 'Now: removing it…' : `in ${formatSeconds(left)}`}
          {fault.error && <div className="error-text small">Removal failed, retrying: {fault.error}</div>}
        </dd>
      </dl>
      <div className="edit-actions">
        <button className="btn danger" onClick={onStop} disabled={busy || fault.removing}>{busy ? 'Stopping…' : 'Stop fault'}</button>
      </div>
    </>
  )
}

function describe(f: Fault) {
  switch (f.type) {
    case 'latency': return `${f.latencyMs} ms${f.jitterMs ? ` ± ${f.jitterMs} ms` : ''} on every response`
    case 'timeout': return f.timeoutMs ? `connections close after ${f.timeoutMs} ms` : 'connections hang until the fault ends'
    case 'flap': return `${f.downSec} s down, ${f.upSec} s up`
    default: return descriptions[f.type]
  }
}

function formatSeconds(s: number) {
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`
}

// useNow re-renders every second, for countdowns.
function useNow() {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}
