import { useState } from 'react'
import { api, type Dependency, type Environment, type RouteCheck, type Service } from '../api'
import { Dot, timeAgo, type Tone } from '../ui'

const states: Record<RouteCheck['state'], [string, Tone]> = {
  verified: ['Goes through the proxy', 'ok'],
  not_seen: ['Not seen in the proxy', 'bad'],
  unknown: ["Couldn't tell", 'warn'],
}

// RoutesPanel lists the services that use the dependency and checks, on demand,
// that each one really reaches it through the proxy. Otherwise faults on the
// proxy have no effect on that service.
export function RoutesPanel({ d, env, onChanged }: { d: Dependency; env: Environment; onChanged: () => void }) {
  return (
    <section className="panel">
      <h2>Used by services</h2>
      {d.usedBy.length === 0 ? (
        <p className="muted small">No service declares this dependency in its <code>uses</code> list.</p>
      ) : (
        <>
          <p className="muted small">
            Faults only reach services that connect to <code>{d.address}</code>, not straight to <code>{d.upstream}</code>.
            <strong> Verify</strong> runs one of the service's requests twice, the second time with 1.5 s of latency on
            this proxy: if it gets slower (or fails), it goes through the proxy. Everything using {d.name} through the
            proxy is slowed down meanwhile.
          </p>
          <div className="routes">
            {d.usedBy.map((name) => {
              const s = env.services.find((x) => x.name === name)
              return s && <RouteRow key={name} d={d} s={s} check={d.routes[name]} onChanged={onChanged} />
            })}
          </div>
        </>
      )}
    </section>
  )
}

function RouteRow({ d, s, check, onChanged }: { d: Dependency; s: Service; check?: RouteCheck; onChanged: () => void }) {
  const options = triggerOptions(s)
  const [trigger, setTrigger] = useState(options[0]?.value ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function verify() {
    setBusy(true)
    setError(null)
    try {
      await api.verifyRoute(d.name, s.name, trigger)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
      onChanged()
    }
  }

  const [label, tone] = check ? states[check.state] : ['Not verified yet', 'idle' as Tone]
  return (
    <div className="route">
      <div className="route-head">
        <span className="name">{s.name}</span>
        <span className="status"><Dot tone={tone} /> {label}</span>
        {check && <span className="muted small">{timeAgo(check.checkedAt)}</span>}
      </div>
      {check && <div className="small">{check.detail}</div>}
      {error && <div className="error-text small">{error}</div>}
      {options.length === 0 ? (
        <div className="hint">
          {s.name} has no health check URL, health command or command to run, so Entropy can't check this. Add one in the config file.
        </div>
      ) : (
        <div className="edit-actions">
          <select className="input narrow-select" value={trigger} onChange={(e) => setTrigger(e.target.value)} disabled={busy}>
            {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
          <button className="btn" onClick={verify} disabled={busy || d.verifying || !!d.fault}>
            {busy ? 'Verifying…' : 'Verify'}
          </button>
          {d.fault && <span className="hint">Stop the fault first: it would distort the check.</span>}
        </div>
      )}
    </div>
  )
}

// The requests Entropy can run for a service: its health check (unless it's
// only a port check, which never reaches a dependency), then each command.
function triggerOptions(s: Service) {
  const out: { value: string; label: string }[] = []
  if (s.healthCommand) out.push({ value: '', label: `Health command (${s.healthCommand})` })
  else if (s.healthCheck) out.push({ value: '', label: `Health check (GET ${s.healthCheck})` })
  for (const c of s.commands) {
    if (c.name !== s.healthCommand) out.push({ value: c.name, label: `Command ${c.name}` })
  }
  return out
}
