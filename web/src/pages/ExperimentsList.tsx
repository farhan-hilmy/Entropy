import { useCallback, useEffect, useState } from 'react'
import { api, type Environment, type Experiment, type ExperimentDefinition } from '../api'
import { navigate } from '../route'
import { RunBadge, timeAgo, Trend } from '../ui'

export function ExperimentsList({ env }: { env: Environment }) {
  const [experiments, setExperiments] = useState<Experiment[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [starting, setStarting] = useState('')

  const load = useCallback(async () => {
    try {
      setExperiments(await api.experiments())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])
  useEffect(() => {
    load()
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
  }, [load])

  async function start(name: string) {
    setStarting(name)
    setError(null)
    try {
      const run = await api.startRun(name)
      navigate(`/runs/${run.id}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setStarting('')
    }
  }

  return (
    <>
      <div className="page-header">
        <div>
          <h1>Experiments</h1>
          <p className="muted small">
            Defined under <code>experiments:</code> in <code>{env.configPath}</code>. Edit the file, then press Sync on the
            Environment page; each change makes a new version.
          </p>
        </div>
      </div>
      {error && <div className="banner error"><pre>{error}</pre></div>}
      {env.activeRun && (
        <div className="banner warn">
          <span>An experiment is running. <a onClick={() => navigate(`/runs/${env.activeRun}`)}>Watch it</a></span>
        </div>
      )}
      {experiments === null ? <p className="muted">Loading…</p> : experiments.length === 0 ? (
        <section className="panel">
          <p>No experiments yet. Add one under <code>experiments:</code> in the config file; <code>entropy.example.yaml</code> has a sample.</p>
        </section>
      ) : (
        <table>
          <thead>
            <tr><th>Experiment</th><th>Breaks</th><th>Version</th><th>Last run</th><th>Recent runs</th><th /></tr>
          </thead>
          <tbody>
            {experiments.map((x) => (
              <tr key={x.name}>
                <td>
                  <div className="name">{x.name}</div>
                  {x.definition.description && <div className="muted small">{x.definition.description}</div>}
                  {x.missing && <div className="muted small">Removed from the config file</div>}
                </td>
                <td className="small">{breaks(x.definition)}</td>
                <td>v{x.version}</td>
                <td>
                  {x.lastRun ? (
                    <a className="status" onClick={() => navigate(`/runs/${x.lastRun!.id}`)}>
                      <RunBadge run={x.lastRun} /> <span className="muted small">{timeAgo(x.lastRun.startedAt)}</span>
                    </a>
                  ) : <span className="muted small">Never run</span>}
                </td>
                <td>
                  {x.recent.length > 0
                    ? <Trend runs={x.recent} onOpen={(id) => navigate(`/runs/${id}`)} />
                    : <span className="muted small">—</span>}
                </td>
                <td>
                  <button className="btn primary" onClick={() => start(x.name)} disabled={x.missing || !!env.activeRun || starting !== ''}>
                    {starting === x.name ? 'Starting…' : 'Run'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {experiments && experiments.some((x) => x.recent.length > 0) && <TrendLegend />}
    </>
  )
}

export function TrendLegend() {
  return (
    <p className="legend muted small">
      Recent runs, oldest left:
      {['passed', 'failed', 'inconclusive', 'aborted'].map((v) => (
        <span key={v}><span className={`trend-dot ${v}`} /> {v}</span>
      ))}
    </p>
  )
}

export function breaks(d: ExperimentDefinition): string {
  if (!d.faults?.length) return 'Nothing (integration test)'
  return d.faults.map((f) => `${f.type} on ${f.dependency} for ${f.durationSec} s`).join(', ')
}
