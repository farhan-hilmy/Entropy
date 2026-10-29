import { useCallback, useEffect, useState } from 'react'
import { api, type Experiment, type Run } from '../api'
import { navigate } from '../route'
import { RunBadge, seconds } from '../ui'
import { breaks } from './ExperimentsList'

const phases = ['preflight', 'baseline', 'during', 'restoring', 'after', 'evaluating']

// RunView follows a run live and, once it's done, is its report.
export function RunView({ id, onChanged }: { id: string; onChanged: () => void }) {
  const [run, setRun] = useState<Run | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [stopping, setStopping] = useState(false)
  const [starting, setStarting] = useState(false)
  const [copied, setCopied] = useState(false)
  const [current, setCurrent] = useState<Experiment | null>(null)

  const load = useCallback(async () => {
    try {
      setRun(await api.run(id))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [id])
  const running = run !== null && run.state !== 'done'
  useEffect(() => {
    load()
  }, [load])
  useEffect(() => {
    if (!running) return
    const t = setInterval(load, 1000)
    return () => clearInterval(t)
  }, [running, load])

  // The experiment as it is now, to offer "Run current" when it changed since.
  useEffect(() => {
    if (!run || running) return
    api.experiments().then((xs) => setCurrent(xs.find((x) => x.name === run.experiment && !x.missing) ?? null), () => {})
  }, [run?.experiment, running])

  async function start(action: () => Promise<Run>) {
    setStarting(true)
    setError(null)
    try {
      const next = await action()
      navigate(`/runs/${next.id}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setStarting(false)
      onChanged()
    }
  }

  async function copyYaml() {
    try {
      await navigator.clipboard.writeText(await api.runExperimentYaml(id))
      setCopied(true)
      setTimeout(() => setCopied(false), 2500)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  async function stop() {
    setStopping(true)
    try {
      await api.stopRun(id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setStopping(false)
      load()
      onChanged()
    }
  }

  if (!run) return error ? <div className="banner error">{error}</div> : <p className="muted">Loading…</p>
  const exp = run.snapshot?.experiment
  const changed = !running && current !== null && current.version !== run.version
  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/experiments')}>Experiments</a> / <a onClick={() => navigate('/runs')}>Runs</a> / {run.id}
      </div>
      <div className="page-header">
        <div>
          <div className="title-row">
            <h1>{run.experiment}</h1>
            <RunBadge run={run} />
          </div>
          <p className="muted small">
            Version {run.version} · started {new Date(run.startedAt).toLocaleString()} · {seconds(run.startedAt, run.endedAt || undefined)}
            {run.rerunOf && <> · rerun of <a onClick={() => navigate(`/runs/${run.rerunOf}`)}>{run.rerunOf}</a></>}
          </p>
        </div>
        {running ? (
          <button className="btn danger" onClick={stop} disabled={stopping}>{stopping ? 'Stopping…' : 'Stop run'}</button>
        ) : (
          <div className="edit-actions">
            <button className="btn primary" onClick={() => start(() => api.rerun(run.id))} disabled={starting}
              title="Repeat this run exactly: same experiment version and commands; addresses from your current setup">
              Rerun v{run.version}
            </button>
            {changed && current && (
              <button className="btn" onClick={() => start(() => api.startRun(run.experiment))} disabled={starting}>
                Run current (v{current.version})
              </button>
            )}
            <button className="btn" onClick={copyYaml} title="This run's experiment as YAML, to paste under experiments: in entropy.yaml">
              {copied ? 'Copied ✓' : 'Copy as YAML'}
            </button>
          </div>
        )}
      </div>
      {error && <div className="banner error">{error}</div>}
      {changed && current && (
        <div className="banner warn">
          <span>
            The experiment has changed since this run (v{run.version} → v{current.version}). <strong>Rerun</strong> repeats
            v{run.version} exactly; <strong>Run current</strong> uses what's in the config file now.
          </span>
        </div>
      )}

      {run.state === 'done' ? (
        <div className={`banner ${run.verdict === 'passed' ? 'ok' : run.verdict === 'failed' ? 'error' : 'warn'}`}>
          <span><strong>{verdictTitle(run.verdict)}</strong> {run.reason}</span>
        </div>
      ) : (
        <div className="phases">
          {phases.map((p) => (
            <span key={p} className={`chip ${p === run.state ? 'current' : phases.indexOf(p) < phases.indexOf(run.state) ? 'past' : ''}`}>{p}</span>
          ))}
        </div>
      )}

      {exp && (
        <section className="panel">
          <h2>What it did</h2>
          <dl className="facts">
            <dt>Breaks</dt><dd>{breaks(exp)}</dd>
            <dt>Triggers</dt>
            <dd>
              {exp.triggers.map((t) => (
                <div key={t.name} className="small">
                  <strong>{t.name}</strong> ({t.phase}): {t.command ?? `${t.request?.method} ${t.service}${t.request?.path}`}
                  {t.repeat > 1 && ` × ${t.repeat}${t.everySec ? `, every ${t.everySec} s` : ''}`}
                </div>
              ))}
            </dd>
            <dt>Limits</dt><dd className="small">Recovery window {exp.recoveryWindowSec} s · aborted after {exp.maxDurationSec} s</dd>
          </dl>
        </section>
      )}

      <section className="panel">
        <h2>Assertions</h2>
        {(exp?.assertions ?? []).map((a, i) => {
          const result = run.assertions?.find((r) => r.index === i)
          const tone = result?.outcome === 'pass' ? 'ok' : result?.outcome === 'fail' ? 'bad' : ''
          return (
            <div key={i} className="assertion">
              <span className={`badge ${tone}`}>{result ? result.outcome.replace('_', ' ') : 'pending'}</span>
              <div>
                <div className="small"><strong>{a.type}</strong> {a.trigger ?? a.service}</div>
                {result && <div className="muted small">{result.detail}</div>}
              </div>
            </div>
          )
        })}
      </section>

      <section className="panel">
        <h2>Timeline</h2>
        <table className="timeline">
          <tbody>
            {(run.events ?? []).map((e, i) => (
              <tr key={i} className={`event-${e.type}`}>
                <td className="muted small">+{seconds(run.startedAt, e.at)}</td>
                <td><span className="badge">{e.type}</span></td>
                <td className="small">{e.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </>
  )
}

function verdictTitle(v?: string) {
  switch (v) {
    case 'passed': return 'Passed: resilient.'
    case 'failed': return 'Failed: an assertion was violated.'
    case 'inconclusive': return "Inconclusive: the run couldn't be judged."
    default: return 'Aborted.'
  }
}
