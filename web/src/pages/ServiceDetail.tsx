import { useState } from 'react'
import { api, type Command, type CommandRun, type Environment, type Service } from '../api'
import { navigate } from '../route'
import { FromConfig, StatusLabel, timeAgo } from '../ui'

export function ServiceDetail({ env, name, onChanged }: { env: Environment; name: string; onChanged: () => void }) {
  const s = env.services.find((x) => x.name === name)
  if (!s) {
    return (
      <>
        <div className="crumbs"><a onClick={() => navigate('/services')}>Services</a> / {name}</div>
        <p className="muted">No service called “{name}”.</p>
      </>
    )
  }

  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/')}>Environment</a> / <a onClick={() => navigate('/services')}>Services</a> / {s.name}
      </div>
      <div className="page-header">
        <div className="title-row">
          <h1>{s.name}</h1>
          <StatusLabel status={s.status} missing={s.missing} />
          {s.healthCommand && <span className="muted small">checked {timeAgo(s.status.checkedAt)}</span>}
        </div>
        {s.healthCommand && <CheckNow service={s} onChanged={onChanged} />}
      </div>

      <section className="panel">
        <dl className="facts">
          <dt>Base URL</dt><dd><code>{s.baseUrl}</code></dd>
          <dt>Health check</dt><dd><HealthCheckText service={s} /></dd>
          <dt>Depends on</dt>
          <dd>
            {s.uses.length === 0 ? '—' : s.uses.map((d, i) => (
              <span key={d}>{i > 0 && ', '}<a onClick={() => navigate(`/dependencies/${encodeURIComponent(d)}`)}>{d}</a></span>
            ))}
          </dd>
          <dt>Last check</dt><dd>{timeAgo(s.status.checkedAt)}{s.status.detail && <div className="muted small">{s.status.detail}</div>}</dd>
        </dl>
        <FromConfig />
      </section>

      <h2 className="section-title">Commands</h2>
      {s.commands.length === 0 ? <NoCommands service={s} /> : s.commands.map((c) => (
        <CommandCard key={c.name} service={s} command={c} onChanged={onChanged} />
      ))}
    </>
  )
}

export function HealthCheckText({ service: s }: { service: Service }) {
  if (s.healthCommand) {
    return (
      <>
        runs command <code>{s.healthCommand}</code>{' '}
        <span className="muted">
          {s.healthEverySec ? `every ${s.healthEverySec}s` : 'on Sync and on “Check now”'}; exit 0 = Reachable
        </span>
      </>
    )
  }
  if (s.healthCheck) return <><code>GET {s.healthCheck}</code> <span className="muted">every 5s</span></>
  return <span className="muted">port only, every 5s</span>
}

function CheckNow({ service, onChanged }: { service: Service; onChanged: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const checking = busy || service.checking

  async function check() {
    setBusy(true)
    setError(null)
    try {
      await api.checkService(service.name)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
      onChanged()
    }
  }

  return (
    <div className="check-now">
      <button className="btn" onClick={check} disabled={checking || service.missing}>
        {checking ? 'Checking…' : '⟳ Check now'}
      </button>
      {error && <span className="small error-text">{error}</span>}
    </div>
  )
}

function CommandCard({ service, command, onChanged }: { service: Service; command: Command; onChanged: () => void }) {
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [shown, setShown] = useState<CommandRun | null>(null)
  const running = starting || command.running
  const run = shown ?? command.lastRun

  async function start() {
    setStarting(true)
    setError(null)
    setShown(null)
    try {
      await api.runCommand(service.name, command.name)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setStarting(false)
      onChanged()
    }
  }

  async function show(id: string) {
    try {
      setShown(await api.commandRun(id))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <section className="panel command">
      <div className="panel-head">
        <h2>
          {command.name}
          {service.healthCommand === command.name && <span className="badge"> health check</span>}
        </h2>
        <button className="btn" onClick={start} disabled={running || service.missing}>
          {running ? 'Running…' : '▶ Run'}
        </button>
      </div>
      <pre className="cmdline">{command.run}</pre>
      <p className="muted small">
        in <code>{command.cwd}</code> · stops after {command.timeoutSec}s
      </p>
      {running && <p className="small">Running, this can take up to {command.timeoutSec}s…</p>}
      {error && <div className="banner error">{error}</div>}

      {run && (
        <div className="run">
          <div className="run-head">
            <RunBadge run={run} />
            <span className="muted small">{(run.durationMs / 1000).toFixed(1)}s · {timeAgo(run.startedAt)}</span>
            {shown && <button className="link small" onClick={() => setShown(null)}>back to latest</button>}
          </div>
          {run.error && <p className="small">Could not run: {run.error}</p>}
          <pre className="output">{run.output || '(no output)'}</pre>
        </div>
      )}

      {command.recent.length > 1 && (
        <div className="recent">
          <span className="muted small">Recent:</span>
          {command.recent.map((r) => (
            <button key={r.id} className={`chip ${r.id === run?.id ? 'selected' : ''}`} onClick={() => show(r.id)} title={new Date(r.startedAt).toLocaleString()}>
              <RunBadge run={r} compact /> {timeAgo(r.startedAt)}
            </button>
          ))}
        </div>
      )}
    </section>
  )
}

function RunBadge({ run, compact }: { run: CommandRun; compact?: boolean }) {
  if (run.error) return <span className="badge bad">{compact ? '!' : 'Failed to start'}</span>
  if (run.timedOut) return <span className="badge warn">{compact ? '⏱' : 'Timed out'}</span>
  return <span className={`badge ${run.exitCode === 0 ? 'ok' : 'bad'}`}>{compact ? run.exitCode : `Exit ${run.exitCode}`}</span>
}

function NoCommands({ service }: { service: Service }) {
  return (
    <section className="panel">
      <p className="small">
        No commands yet. Add the commands you use to call this service, for example a Bruno request, under
        the service in the config file, then press Sync:
      </p>
      <pre className="cmdline">{`services:
  - name: ${service.name}
    baseUrl: ${service.baseUrl}
    commands:
      - name: access-token
        run: bru run "Access Token B2B.yml" --env local
        cwd: D:/path/to/bruno-collection
        timeoutSec: 30`}</pre>
      <p className="muted small">
        Exit code 0 counts as success. Commands also get <code>ENTROPY_BASE_URL</code> and <code>ENTROPY_SERVICE</code>.
      </p>
    </section>
  )
}
