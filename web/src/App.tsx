import { useCallback, useEffect, useState } from 'react'
import { api, type Environment } from './api'
import { useRoute, navigate } from './route'
import { Overview } from './pages/Overview'
import { DependenciesList } from './pages/DependenciesList'
import { DependencyDetail } from './pages/DependencyDetail'
import { ServicesList } from './pages/ServicesList'
import { ServiceDetail } from './pages/ServiceDetail'
import { ExperimentsList } from './pages/ExperimentsList'
import { RunView } from './pages/RunView'
import { RunsList } from './pages/RunsList'
import { Dot } from './ui'

const POLL_MS = 3000

export default function App() {
  const route = useRoute()
  const [env, setEnv] = useState<Environment | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [notice, setNotice] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)
  const [stopping, setStopping] = useState(false)

  const refresh = useCallback(async () => {
    try {
      setEnv(await api.environment())
      setLoadError(null)
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, POLL_MS)
    return () => clearInterval(id)
  }, [refresh])

  async function stopAll() {
    setStopping(true)
    try {
      await api.stopAllFaults()
      setNotice({ kind: 'ok', text: 'All faults stopped. Every proxy is enabled and has no toxics.' })
    } catch (e) {
      setNotice({ kind: 'error', text: `Could not stop faults: ${e instanceof Error ? e.message : e}` })
    } finally {
      setStopping(false)
      refresh()
    }
  }

  let page = null
  if (env) {
    if (route[0] === 'dependencies' && route[1]) {
      page = <DependencyDetail env={env} name={decodeURIComponent(route[1])} onChanged={refresh} />
    } else if (route[0] === 'dependencies') {
      page = <DependenciesList env={env} onChanged={refresh} />
    } else if (route[0] === 'services' && route[1]) {
      page = <ServiceDetail env={env} name={decodeURIComponent(route[1])} onChanged={refresh} />
    } else if (route[0] === 'services') {
      page = <ServicesList env={env} onChanged={refresh} />
    } else if (route[0] === 'experiments') {
      page = <ExperimentsList env={env} />
    } else if (route[0] === 'runs' && route[1]) {
      page = <RunView id={decodeURIComponent(route[1])} onChanged={refresh} />
    } else if (route[0] === 'runs') {
      page = <RunsList />
    } else {
      page = <Overview env={env} onSynced={refresh} />
    }
  }

  const section = route[0] || 'environment'
  return (
    <div className="shell">
      <header className="topbar">
        <span className="brand">Entropy</span>
        {env && (
          <span className="topbar-status">
            <Dot tone={envTone(env.status)} />
            Env {env.status} · {env.activeFaults} active {env.activeFaults === 1 ? 'fault' : 'faults'}
          </span>
        )}
        <button className="btn danger" onClick={stopAll} disabled={stopping}>
          {stopping ? 'Stopping…' : 'Stop all faults'}
        </button>
      </header>

      <div className="body">
        <nav className="sidebar">
          <a className={['environment', 'dependencies', 'services'].includes(section) ? 'active' : ''} onClick={() => navigate('/')}>
            Environment
          </a>
          <a className={section === 'experiments' ? 'active' : ''} onClick={() => navigate('/experiments')}>Experiments</a>
          <a className={section === 'runs' ? 'active' : ''} onClick={() => navigate('/runs')}>Runs</a>
        </nav>

        <main>
          {notice && (
            <div className={`banner ${notice.kind}`}>
              <span>{notice.text}</span>
              <button className="link" onClick={() => setNotice(null)}>Dismiss</button>
            </div>
          )}
          {loadError && <div className="banner error">Can't reach Entropy: {loadError}</div>}
          {!env && !loadError && <p className="muted">Loading…</p>}
          {page}
        </main>
      </div>
    </div>
  )
}

function envTone(status: Environment['status']) {
  return status === 'ready' ? 'ok' : status === 'unreachable' ? 'bad' : status === 'degraded' ? 'warn' : 'idle'
}
