import { useState } from 'react'
import { api, type Environment, type EnvironmentFields, type SyncResult } from '../api'
import { EditFooter, Field, TextInput, useConfigForm } from '../editing'
import { navigate } from '../route'
import { StatusLabel, timeAgo } from '../ui'

export function Overview({ env, onSynced }: { env: Environment; onSynced: () => void }) {
  const edit = useConfigForm<EnvironmentFields>(
    (c) => ({ name: c.environment.name, toxiproxyApi: c.toxiproxy.api }),
    (form) => ({ environment: form }),
    onSynced,
  )
  const [syncing, setSyncing] = useState(false)
  const [result, setResult] = useState<SyncResult | null>(null)
  const [syncError, setSyncError] = useState<string | null>(null)

  async function sync() {
    setSyncing(true)
    setSyncError(null)
    try {
      setResult(await api.sync())
      onSynced()
    } catch (e) {
      setSyncError(e instanceof Error ? e.message : String(e))
    } finally {
      setSyncing(false)
    }
  }

  const deps = env.dependencies.filter((d) => !d.missing)
  const services = env.services.filter((s) => !s.missing)
  const ready = deps.filter((d) => d.ready).length

  return (
    <>
      <div className="page-header">
        <div>
          <div className="title-row">
            <h1>{env.name} environment</h1>
            <span className={`pill ${env.status}`}>{env.status}</span>
          </div>
          <p className="muted small">
            Synced {timeAgo(env.syncedAt)} from <code>{env.configPath}</code>
          </p>
        </div>
        <button className="btn" onClick={sync} disabled={syncing}>
          {syncing ? 'Syncing…' : '⟳ Sync'}
        </button>
      </div>

      {syncError && <div className="banner error">Sync failed: {syncError}</div>}
      {result && <SyncReport result={result} onDismiss={() => setResult(null)} />}
      {env.toxiproxy.status.state === 'unreachable' && (
        <div className="banner error">
          <span>
            Toxiproxy is not reachable at <code>{env.toxiproxy.api}</code>. Start it, then press Sync. If its address
            changed (e.g. the Podman VM's, after a reboot), edit it under Settings below.
          </span>
        </div>
      )}

      <section className="panel">
        <h2>Settings</h2>
        {edit.form ? (
          <div className="form">
            <Field label="Environment name" hint="Leave empty for the default, local.">
              <TextInput value={edit.form.name} placeholder="local" onChange={(name) => edit.update({ name })} />
            </Field>
            <Field label="Toxiproxy API" hint="Leave empty for the default. Remember to update dependency addresses too if the host changed.">
              <TextInput value={edit.form.toxiproxyApi} placeholder="http://localhost:8474" onChange={(toxiproxyApi) => edit.update({ toxiproxyApi })} />
            </Field>
          </div>
        ) : (
          <dl className="facts">
            <dt>Environment name</dt><dd>{env.name}</dd>
            <dt>Toxiproxy API</dt><dd><code>{env.toxiproxy.api}</code> <StatusLabel status={env.toxiproxy.status} /></dd>
          </dl>
        )}
        <EditFooter f={edit} />
      </section>

      <div className="stats">
        <Stat label="Services" value={services.length} />
        <Stat label="Dependencies" value={deps.length} />
        <Stat label="Experiment-ready" value={ready} />
      </div>

      <div className="panels">
        <section className="panel">
          <div className="panel-head">
            <h2>Dependencies</h2>
            <a onClick={() => navigate('/dependencies')}>View all →</a>
          </div>
          {deps.length === 0 && <p className="muted small">None declared in the config file.</p>}
          {deps.map((d) => (
            <div key={d.id} className="row clickable" onClick={() => navigate(`/dependencies/${encodeURIComponent(d.name)}`)}>
              <span className="name">{d.name}</span>
              <StatusLabel status={d.status} />
            </div>
          ))}
        </section>

        <section className="panel">
          <div className="panel-head">
            <h2>Services</h2>
            <a onClick={() => navigate('/services')}>View all →</a>
          </div>
          {services.length === 0 && <p className="muted small">None declared in the config file.</p>}
          {services.map((s) => (
            <div key={s.id} className="row clickable" onClick={() => navigate(`/services/${encodeURIComponent(s.name)}`)}>
              <span className="name">{s.name}</span>
              <StatusLabel status={s.status} />
            </div>
          ))}
        </section>
      </div>
    </>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="stat">
      <span className="muted small">{label}</span>
      <strong>{value}</strong>
    </div>
  )
}

function SyncReport({ result, onDismiss }: { result: SyncResult; onDismiss: () => void }) {
  if (result.error) {
    return (
      <div className="banner error">
        <div>
          <strong>The config file has problems. Nothing was changed.</strong>
          <pre>{result.error}</pre>
        </div>
        <button className="link" onClick={onDismiss}>Dismiss</button>
      </div>
    )
  }
  const s = result.summary!
  const part = (n: number, word: string, names: string[]) =>
    `${n} ${word}${names.length ? ` (${names.map((x) => x.replace(/^\w+ /, '')).join(', ')})` : ''}`
  return (
    <div className={`banner ${result.warnings?.length ? 'warn' : 'ok'}`}>
      <div>
        <strong>Synced.</strong> {part(s.added.length, 'added', s.added)} · {part(s.updated.length, 'updated', s.updated)} ·{' '}
        {part(s.missing.length, 'missing', s.missing)}
        {result.warnings?.map((w) => <div key={w} className="small">⚠ {w}</div>)}
      </div>
      <button className="link" onClick={onDismiss}>Dismiss</button>
    </div>
  )
}
