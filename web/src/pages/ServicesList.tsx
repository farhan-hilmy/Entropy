import { useState } from 'react'
import type { Environment } from '../api'
import { navigate } from '../route'
import { StatusLabel } from '../ui'
import { HealthCheckText } from './ServiceDetail'

export function ServicesList({ env }: { env: Environment }) {
  const [query, setQuery] = useState('')
  const services = env.services.filter((s) => s.name.toLowerCase().includes(query.toLowerCase()))

  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/')}>Environment</a> / Services
      </div>
      <h1>Services</h1>
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
