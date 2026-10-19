import type { DependencyFields, Environment } from '../api'
import { EditFooter, Field, TextInput, useConfigForm, type ConfigForm } from '../editing'
import { navigate } from '../route'
import { StatusLabel, timeAgo } from '../ui'

export function DependencyDetail({ env, name, onChanged }: { env: Environment; name: string; onChanged: () => void }) {
  const edit = useConfigForm<DependencyFields>(
    (c) => {
      const raw = c.dependencies?.find((x) => x.name === name)
      return raw && { name, address: raw.address, upstream: raw.upstream, listen: raw.listen, redirect: raw.redirect }
    },
    (form) => ({ dependency: form }),
    onChanged,
  )
  const d = env.dependencies.find((x) => x.name === name)
  if (!d) {
    return (
      <>
        <div className="crumbs"><a onClick={() => navigate('/dependencies')}>Dependencies</a> / {name}</div>
        <p className="muted">No dependency called “{name}”.</p>
      </>
    )
  }

  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/')}>Environment</a> / <a onClick={() => navigate('/dependencies')}>Dependencies</a> / {d.name}
      </div>
      <div className="page-header">
        <div className="title-row">
          <h1>{d.name}</h1>
          <StatusLabel status={d.status} missing={d.missing} />
        </div>
        <button className="btn primary" disabled title="Experiments come in build step 3">
          Start experiment
        </button>
      </div>

      <section className="panel">
        {edit.form ? <DependencyForm f={edit} kind={d.kind} /> : (
          <dl className="facts">
            <dt>Kind</dt><dd>{d.kind}</dd>
            <dt>Address (through proxy)</dt><dd><code>{d.address}</code></dd>
            <dt>Upstream (real)</dt><dd><code>{d.upstream}</code></dd>
            <dt>Proxy listens on</dt><dd><code>{d.listen}</code></dd>
            {d.redirect && (<><dt>Hosts-file redirect</dt><dd>Required: hostname must resolve to 127.0.0.1</dd></>)}
            <dt>Last check</dt><dd>{timeAgo(d.status.checkedAt)}{d.status.detail && <div className="muted small">{d.status.detail}</div>}</dd>
            <dt>Experiment-ready</dt><dd>{d.ready ? 'Yes' : <>No <span className="muted">— {d.notReadyReason}</span></>}</dd>
          </dl>
        )}
        {!d.missing && <EditFooter f={edit} />}
      </section>

      <section className="panel">
        <h2>Used by services</h2>
        {d.usedBy.length === 0 ? (
          <p className="muted small">No service declares this dependency in its <code>uses</code> list.</p>
        ) : (
          <div className="chips">{d.usedBy.map((s) => <span key={s} className="chip">{s}</span>)}</div>
        )}
        {d.usedBy.length > 0 && !d.missing && (
          <p className="muted small">
            Make sure these services connect to <code>{d.address}</code>, not straight to <code>{d.upstream}</code>.
            Otherwise faults have no effect.
          </p>
        )}
      </section>
    </>
  )
}

function DependencyForm({ f, kind }: { f: ConfigForm<DependencyFields>; kind: string }) {
  const form = f.form!
  const port = form.address.split(':').pop() || '<port>'
  return (
    <div className="form">
      <Field label="Kind" hint="Name and kind can only be changed in the config file."><span>{kind}</span></Field>
      <Field label="Address (through proxy)" hint="Where services and Entropy's checks connect, as host:port.">
        <TextInput value={form.address} onChange={(address) => f.update({ address })} />
      </Field>
      <Field label="Upstream (real)" hint="The real dependency, as Toxiproxy sees it.">
        <TextInput value={form.upstream} onChange={(upstream) => f.update({ upstream })} />
      </Field>
      <Field label="Proxy listens on" hint="Leave empty for the default.">
        <TextInput value={form.listen} placeholder={`0.0.0.0:${port}`} onChange={(listen) => f.update({ listen })} />
      </Field>
      {(kind === 'https' || form.redirect) && (
        <label className="check">
          <input type="checkbox" checked={form.redirect} onChange={(e) => f.update({ redirect: e.target.checked })} />
          Hosts-file redirect (the hostname must resolve to 127.0.0.1)
        </label>
      )}
    </div>
  )
}
