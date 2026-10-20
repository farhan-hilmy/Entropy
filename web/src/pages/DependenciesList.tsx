import { useState } from 'react'
import { dependencyKinds, type Environment, type NewDependency } from '../api'
import { Field, SaveButtons, TextInput, useConfigForm, type ConfigForm } from '../editing'
import { navigate } from '../route'
import { StatusLabel } from '../ui'

export function DependenciesList({ env, onChanged }: { env: Environment; onChanged: () => Promise<void> }) {
  const [query, setQuery] = useState('')
  const add = useConfigForm<NewDependency>(
    () => ({ name: '', kind: 'redis', address: '', upstream: '', listen: '' }),
    (form) => ({ addDependency: form }),
    async (form) => {
      await onChanged()
      navigate(`/dependencies/${encodeURIComponent(form.name)}`)
    },
  )
  const deps = env.dependencies.filter((d) => d.name.toLowerCase().includes(query.toLowerCase()))

  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/')}>Environment</a> / Dependencies
      </div>
      <div className="page-header">
        <h1>Dependencies</h1>
        {!add.form && <button className="btn" onClick={add.open} disabled={add.busy}>+ Add dependency</button>}
      </div>
      {add.error && !add.form && <div className="banner error">{add.error}</div>}
      {add.form && <AddDependency f={add} />}
      <input className="search" placeholder="Search dependencies…" value={query} onChange={(e) => setQuery(e.target.value)} />
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Kind</th>
            <th>Address (through proxy)</th>
            <th>Status</th>
            <th>Experiment-ready</th>
          </tr>
        </thead>
        <tbody>
          {deps.map((d) => (
            <tr key={d.id} className="clickable" onClick={() => navigate(`/dependencies/${encodeURIComponent(d.name)}`)}>
              <td className="name">{d.name}</td>
              <td>{d.kind}</td>
              <td><code>{d.address}</code></td>
              <td><StatusLabel status={d.status} missing={d.missing} /></td>
              <td title={d.notReadyReason}>{d.ready ? 'Yes' : 'No'}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {deps.length === 0 && <p className="muted small">No dependencies match.</p>}
    </>
  )
}

function AddDependency({ f }: { f: ConfigForm<NewDependency> }) {
  const form = f.form!
  const port = form.address.includes(':') ? form.address.split(':').pop() || '<port>' : '<port>'
  return (
    <section className="panel">
      <h2>New dependency</h2>
      <div className="form">
        <Field label="Name" hint="Also the proxy's name in Toxiproxy. Letters, digits, '.', '_' and '-'.">
          <TextInput value={form.name} placeholder="mongodb" onChange={(name) => f.update({ name })} />
        </Field>
        <Field label="Kind">
          <select className="input" value={form.kind} onChange={(e) => f.update({ kind: e.target.value })}>
            {dependencyKinds.map((k) => <option key={k} value={k}>{k}</option>)}
          </select>
        </Field>
        <Field label="Address (through proxy)" hint="Where services and Entropy's checks connect, as host:port.">
          <TextInput value={form.address} placeholder="localhost:27017" onChange={(address) => f.update({ address })} />
        </Field>
        <Field label="Upstream (real)" hint="The real dependency, as Toxiproxy sees it.">
          <TextInput value={form.upstream} placeholder="mongodb:27017" onChange={(upstream) => f.update({ upstream })} />
        </Field>
        <Field label="Proxy listens on" hint="Leave empty for the default.">
          <TextInput value={form.listen} placeholder={`0.0.0.0:${port}`} onChange={(listen) => f.update({ listen })} />
        </Field>
        <div className="banner warn">
          <div className="small">
            Toxiproxy must also publish this port, or the dependency stays Unreachable. Entropy can't do that for you:
            add it to the <code>toxiproxy</code> service in your compose file and restart the container.
            <pre className="cmdline">{`  toxiproxy:\n    ports:\n      - "${port}:${port}"`}</pre>
          </div>
        </div>
      </div>
      <SaveButtons f={f} label="Add dependency" />
    </section>
  )
}
