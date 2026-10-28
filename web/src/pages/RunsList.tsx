import { useEffect, useState } from 'react'
import { api, type Run } from '../api'
import { navigate } from '../route'
import { RunBadge, seconds } from '../ui'

export function RunsList() {
  const [runs, setRuns] = useState<Run[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    const load = () => api.runs().then(setRuns, (e) => setError(e instanceof Error ? e.message : String(e)))
    load()
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
  }, [])

  return (
    <>
      <div className="page-header"><h1>Runs</h1></div>
      {error && <div className="banner error">{error}</div>}
      {runs === null ? <p className="muted">Loading…</p> : runs.length === 0 ? (
        <p className="muted">No runs yet. Start one from <a onClick={() => navigate('/experiments')}>Experiments</a>.</p>
      ) : (
        <table>
          <thead>
            <tr><th>Experiment</th><th>Started</th><th>Took</th><th>Verdict</th><th>Why</th></tr>
          </thead>
          <tbody>
            {runs.map((r) => (
              <tr key={r.id} className="clickable" onClick={() => navigate(`/runs/${r.id}`)}>
                <td className="name">{r.experiment} <span className="muted small">v{r.version}</span></td>
                <td className="small">{new Date(r.startedAt).toLocaleString()}</td>
                <td className="small">{r.endedAt ? seconds(r.startedAt, r.endedAt) : '…'}</td>
                <td><RunBadge run={r} /></td>
                <td className="small muted">{r.reason}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}
