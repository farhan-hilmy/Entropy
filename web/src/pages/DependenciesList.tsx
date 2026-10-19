import { useState } from 'react'
import type { Environment } from '../api'
import { navigate } from '../route'
import { StatusLabel } from '../ui'

export function DependenciesList({ env }: { env: Environment }) {
  const [query, setQuery] = useState('')
  const deps = env.dependencies.filter((d) => d.name.toLowerCase().includes(query.toLowerCase()))

  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/')}>Environment</a> / Dependencies
      </div>
      <h1>Dependencies</h1>
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
