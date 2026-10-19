import { useState } from 'react'
import { api, type Command, type CommandRun, type Environment, type Service, type ServiceFields } from '../api'
import { EditFooter, Field, TextInput, useConfigForm, type ConfigForm } from '../editing'
import { navigate } from '../route'
import { StatusLabel, timeAgo } from '../ui'

// How the service's health is checked; the file says it through which fields are set.
type HealthMode = 'port' | 'path' | 'command'
type ServiceDraft = ServiceFields & { mode: HealthMode; commands: string[]; dependencies: string[] }

export function ServiceDetail({ env, name, onChanged }: { env: Environment; name: string; onChanged: () => void }) {
  const edit = useConfigForm<ServiceDraft>(
    (c) => {
      const raw = c.services?.find((x) => x.name === name)
      if (!raw) return undefined
      return {
        name,
        baseUrl: raw.baseUrl,
        healthCheck: raw.healthCheck,
        healthCommand: raw.healthCommand,
        healthEverySec: raw.healthEverySec,
        uses: raw.uses ?? [],
        mode: raw.healthCommand ? 'command' : raw.healthCheck ? 'path' : 'port',
        commands: (raw.commands ?? []).map((c) => c.name),
        dependencies: (c.dependencies ?? []).map((d) => d.name),
      }
    },
    ({ mode, commands: _c, dependencies: _d, ...f }) => ({
      service: {
        ...f,
        healthCheck: mode === 'path' ? f.healthCheck : '',
        healthCommand: mode === 'command' ? f.healthCommand : '',
        healthEverySec: mode === 'command' ? f.healthEverySec : 0,
      },
    }),
    onChanged,
  )
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
        {edit.form ? <ServiceForm f={edit} /> : (
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
        )}
        {!s.missing && <EditFooter f={edit} />}
      </section>

      <h2 className="section-title">Commands</h2>
      {s.commands.length > 0 && (
        <p className="hint">Commands can only be added or changed in the config file, because Entropy runs them on this machine.</p>
      )}
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

function ServiceForm({ f }: { f: ConfigForm<ServiceDraft> }) {
  const form = f.form!
  const toggleUse = (dep: string, on: boolean) =>
    f.update({ uses: on ? [...form.uses, dep] : form.uses.filter((x) => x !== dep) })
  return (
    <div className="form">
      <Field label="Base URL">
        <TextInput value={form.baseUrl} onChange={(baseUrl) => f.update({ baseUrl })} />
      </Field>
      <Field label="Health check">
        <select
          className="input"
          value={form.mode === 'command' ? `command:${form.healthCommand}` : form.mode}
          onChange={(e) => {
            const v = e.target.value
            if (v.startsWith('command:')) f.update({ mode: 'command', healthCommand: v.slice('command:'.length) })
            else f.update({ mode: v as HealthMode })
          }}
        >
          <option value="port">Port only, every 5s</option>
          <option value="path">HTTP GET a path, every 5s</option>
          {form.commands.map((c) => <option key={c} value={`command:${c}`}>Run command “{c}”</option>)}
        </select>
      </Field>
      {form.mode === 'path' && (
        <Field label="Health path" hint="Starts with /, relative to the base URL.">
          <TextInput value={form.healthCheck} placeholder="/health" onChange={(healthCheck) => f.update({ healthCheck })} />
        </Field>
      )}
      {form.mode === 'command' && (
        <Field label="Re-run every (seconds)" hint="0 runs it only on Sync and “Check now”. Otherwise 15 to 86400.">
          <input
            className="input narrow"
            type="number"
            min={0}
            value={form.healthEverySec}
            onChange={(e) => f.update({ healthEverySec: Number(e.target.value) || 0 })}
          />
        </Field>
      )}
      <Field label="Depends on">
        {form.dependencies.length === 0 ? <span className="muted small">No dependencies in the config file.</span> : (
          <div className="checks">
            {form.dependencies.map((d) => (
              <label key={d} className="check">
                <input type="checkbox" checked={form.uses.includes(d)} onChange={(e) => toggleUse(d, e.target.checked)} />
                {d}
              </label>
            ))}
          </div>
        )}
      </Field>
    </div>
  )
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
