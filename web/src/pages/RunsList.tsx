import { useEffect, useState } from 'react'
import { api, type Run } from '../api'
import { navigate } from '../route'
import { RunBadge, seconds } from '../ui'

const verdicts = ['passed', 'failed', 'inconclusive', 'aborted']

export function RunsList() {
  const [runs, setRuns] = useState<Run[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [experiment, setExperiment] = useState('')
  const [verdict, setVerdict] = useState('')
  useEffect(() => {
    const load = () => api.runs(experiment).then(setRuns, (e) => setError(e instanceof Error ? e.message : String(e)))
    load()
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
  }, [experiment])

  // The experiments to filter by.
  const [names, setNames] = useState<string[]>([])
  useEffect(() => {
    api.experiments().then((xs) => setNames(xs.map((x) => x.name)), () => {})
  }, [])

  const shown = (runs ?? []).filter((r) => !verdict || (verdict === 'running' ? r.state !== 'done' : r.verdict === verdict))
  return (
    <>
      <div className="page-header"><h1>Runs</h1></div>
      {error && <div className="banner error">{error}</div>}
      <div className="filters">
        <select className="input narrow-select" value={experiment} onChange={(e) => setExperiment(e.target.value)}>
          <option value="">All experiments</option>
          {names.map((n) => <option key={n} value={n}>{n}</option>)}
        </select>
        <select className="input narrow-select" value={verdict} onChange={(e) => setVerdict(e.target.value)}>
          <option value="">Any verdict</option>
          <option value="running">Running</option>
          {verdicts.map((v) => <option key={v} value={v}>{v}</option>)}
        </select>
      </div>
      {runs === null ? <p className="muted">Loading…</p> : shown.length === 0 ? (
        runs.length === 0 && !experiment
          ? <p className="muted">No runs yet. Start one from <a onClick={() => navigate('/experiments')}>Experiments</a>.</p>
          : <p className="muted">No runs match.</p>
      ) : (
        <table>
          <thead>
            <tr><th>Experiment</th><th>Started</th><th>Took</th><th>Verdict</th><th>Why</th></tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.id} className="clickable" onClick={() => navigate(`/runs/${r.id}`)}>
                <td>
                  <span className="name">{r.experiment}</span> <span className="muted small">v{r.version}</span>
                  {r.rerunOf && <div className="muted small">rerun of {r.rerunOf}</div>}
                </td>
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
