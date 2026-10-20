import { useState } from 'react'
import type { Environment, NewService } from '../api'
import { Field, SaveButtons, TextInput, useConfigForm, type ConfigForm } from '../editing'
import { navigate } from '../route'
import { StatusLabel } from '../ui'
import { HealthCheckText } from './ServiceDetail'

type ServiceDraft = NewService & { dependencies: string[] }

export function ServicesList({ env, onChanged }: { env: Environment; onChanged: () => Promise<void> }) {
  const [query, setQuery] = useState('')
  const add = useConfigForm<ServiceDraft>(
    (c) => ({ name: '', baseUrl: '', healthCheck: '', uses: [], dependencies: (c.dependencies ?? []).map((d) => d.name) }),
    ({ dependencies: _d, ...form }) => ({ addService: form }),
    async (form) => {
      await onChanged()
      navigate(`/services/${encodeURIComponent(form.name)}`)
    },
  )
  const services = env.services.filter((s) => s.name.toLowerCase().includes(query.toLowerCase()))

  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/')}>Environment</a> / Services
      </div>
      <div className="page-header">
        <h1>Services</h1>
        {!add.form && <button className="btn" onClick={add.open} disabled={add.busy}>+ Add service</button>}
      </div>
      {add.error && !add.form && <div className="banner error">{add.error}</div>}
      {add.form && <AddService f={add} />}
      <input className="search" placeholder="Search services…" value={query} onChange={(e) => setQuery(e.target.value)} />
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Base URL</th>
            <th>Health check</th>
            <th>Status</th>
            <th>Depends on</th>
            <th>Commands</th>
          </tr>
        </thead>
        <tbody>
          {services.map((s) => (
            <tr key={s.id} className="clickable" onClick={() => navigate(`/services/${encodeURIComponent(s.name)}`)}>
              <td className="name">{s.name}</td>
              <td><code>{s.baseUrl}</code></td>
              <td><HealthCheckText service={s} /></td>
              <td><StatusLabel status={s.status} missing={s.missing} /></td>
              <td>
                {s.uses.length === 0 ? '—' : s.uses.map((d, i) => (
                  <span key={d}>
                    {i > 0 && ', '}
                    <a onClick={(e) => { e.stopPropagation(); navigate(`/dependencies/${encodeURIComponent(d)}`) }}>{d}</a>
                  </span>
                ))}
              </td>
              <td>{s.commands.length || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {services.length === 0 && <p className="muted small">No services match.</p>}
    </>
  )
}

function AddService({ f }: { f: ConfigForm<ServiceDraft> }) {
  const form = f.form!
  const toggleUse = (dep: string, on: boolean) =>
    f.update({ uses: on ? [...form.uses, dep] : form.uses.filter((x) => x !== dep) })
  return (
    <section className="panel">
      <h2>New service</h2>
      <div className="form">
        <Field label="Name" hint="Letters, digits, '.', '_' and '-'.">
          <TextInput value={form.name} placeholder="orders-api" onChange={(name) => f.update({ name })} />
        </Field>
        <Field label="Base URL">
          <TextInput value={form.baseUrl} placeholder="http://127.0.0.1:8080" onChange={(baseUrl) => f.update({ baseUrl })} />
        </Field>
        <Field label="Health path" hint="Optional, e.g. /health. Empty checks only that the port answers. To check with a command instead, add it in the config file.">
          <TextInput value={form.healthCheck} placeholder="/health" onChange={(healthCheck) => f.update({ healthCheck })} />
        </Field>
        <Field label="Depends on">
          {form.dependencies.length === 0 ? <span className="muted small">No dependencies in the config file yet.</span> : (
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
      <SaveButtons f={f} label="Add service" />
    </section>
  )
}
