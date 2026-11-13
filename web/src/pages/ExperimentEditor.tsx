import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { api, faultTypes, type Environment } from '../api'
import { navigate } from '../route'

// ExperimentEditor edits one experiment of the config file as YAML. name is
// the experiment to edit (none: a new one); fromRun loads the experiment as
// that run ran it, for "Edit and rerun". Saving replaces just that
// experiment's lines in the file, so comments elsewhere stay as they are.
export function ExperimentEditor({ env, name, fromRun, onChanged }: {
  env: Environment
  name?: string
  fromRun?: string
  onChanged: () => void
}) {
  const [text, setText] = useState('')
  const [hash, setHash] = useState('')
  const [target, setTarget] = useState('') // the experiment in the file it replaces; '' = add
  const [loaded, setLoaded] = useState(false)
  const [note, setNote] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const area = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoaded(false)
      setError(null)
      try {
        let src: { hash: string; yaml: string } | null = null
        if (name) {
          try {
            src = await api.experimentSource(name)
          } catch (e) {
            if (!fromRun || (e as { status?: number }).status !== 404) throw e
          }
        }
        let body = src?.yaml
        let hashNow = src?.hash
        if (!hashNow) hashNow = (await api.config()).hash
        let msg: string | null = null
        if (fromRun) {
          const run = await api.run(fromRun)
          body = listItemToPlain(await api.runExperimentYaml(fromRun))
          msg = src
            ? `Loaded as run ${fromRun} ran it (v${run.version}). Saving replaces ${name} in the config file with this.`
            : `Loaded as run ${fromRun} ran it (v${run.version}). ${name} is no longer in the config file, so saving adds it back.`
        }
        if (cancelled) return
        setText(body ?? newExperimentTemplate(env))
        setHash(hashNow)
        setTarget(src ? name! : '')
        setNote(msg)
        setLoaded(true)
      } catch (e) {
        if (!cancelled) setError(message(e))
      }
    }
    load()
    return () => { cancelled = true }
    // env is only read for the template, on open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name, fromRun])

  async function save(andRun: boolean) {
    setBusy(true)
    setError(null)
    try {
      await api.editConfig(hash, { experiment: { name: target, yaml: text } })
      onChanged()
      const saved = nameIn(text) ?? target
      if (andRun && saved) {
        const run = await api.startRun(saved)
        navigate(`/runs/${run.id}`)
      } else {
        navigate('/experiments')
      }
    } catch (e) {
      setError(message(e))
      setBusy(false)
    }
  }

  async function remove() {
    if (!target || !window.confirm(`Remove ${target} from the config file? Its past runs stay in Runs.`)) return
    setBusy(true)
    setError(null)
    try {
      await api.editConfig(hash, { removeExperiment: { name: target } })
      onChanged()
      navigate('/experiments')
    } catch (e) {
      setError(message(e))
      setBusy(false)
    }
  }

  // Tab indents with two spaces (YAML can't use tabs); Ctrl/Cmd+S saves.
  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault()
      if (!busy) save(false)
      return
    }
    if (e.key === 'Tab' && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey) {
      e.preventDefault()
      const el = e.currentTarget
      const { selectionStart: start, selectionEnd: end } = el
      setText(text.slice(0, start) + '  ' + text.slice(end))
      requestAnimationFrame(() => {
        if (area.current) area.current.selectionStart = area.current.selectionEnd = start + 2
      })
    }
  }

  const renamed = target !== '' && nameIn(text) !== null && nameIn(text) !== target
  const title = target ? `Edit ${target}` : fromRun && name ? `Add ${name} back` : 'New experiment'
  return (
    <>
      <div className="crumbs">
        <a onClick={() => navigate('/experiments')}>Experiments</a> / {target || name || 'new'}
      </div>
      <div className="page-header">
        <div>
          <h1>{title}</h1>
          <p className="muted small">
            Saved under <code>experiments:</code> in <code>{env.configPath}</code>; only this experiment's lines change, and each
            save makes a new version. Commands are still added in the file, on the service.
          </p>
        </div>
      </div>
      {note && <div className="banner warn"><span>{note}</span></div>}
      {renamed && (
        <div className="banner warn">
          <span>Renaming makes it a new experiment: past runs stay under <strong>{target}</strong>.</span>
        </div>
      )}
      {error && <div className="banner error"><pre>{error}</pre></div>}
      {!loaded ? (!error && <p className="muted">Loading…</p>) : (
        <div className="editor-layout">
          <div>
            <textarea
              ref={area}
              className="code-editor"
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={onKeyDown}
              spellCheck={false}
              aria-label="Experiment as YAML"
            />
            <div className="editor-actions">
              <button className="btn primary" onClick={() => save(false)} disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
              <button className="btn" onClick={() => save(true)} disabled={busy || !!env.activeRun}
                title={env.activeRun ? 'Another run is in progress' : 'Save, then start a run of it'}>
                {fromRun ? 'Save and rerun' : 'Save and run'}
              </button>
              <button className="btn" onClick={() => navigate(fromRun ? `/runs/${fromRun}` : '/experiments')} disabled={busy}>Cancel</button>
              <span className="spacer" />
              {target && <button className="btn danger" onClick={remove} disabled={busy}>Remove</button>}
            </div>
            <p className="hint">Ctrl+S saves. Saving checks the whole config, then syncs.</p>
          </div>
          <Reference env={env} />
        </div>
      )}
    </>
  )
}

// Reference lists the names an experiment can use, from the current setup.
function Reference({ env }: { env: Environment }) {
  const deps = env.dependencies.filter((d) => !d.missing)
  const svcs = env.services.filter((s) => !s.missing)
  const commands = svcs.flatMap((s) => s.commands.map((c) => `${s.name}/${c.name}`))
  return (
    <aside className="panel editor-help">
      <h3>Dependencies (faults: dependency)</h3>
      {deps.length ? <ul>{deps.map((d) => <li key={d.name}><code>{d.name}</code> <span className="muted">{d.kind}</span></li>)}</ul>
        : <p className="muted small">None yet.</p>}
      <h3>Fault types</h3>
      <ul>
        {faultTypes.map((t) => <li key={t}><code>{t}</code> <span className="muted">{faultOptions[t]}</span></li>)}
      </ul>
      <h3>Commands (triggers: command)</h3>
      {commands.length ? <ul>{commands.map((c) => <li key={c}><code>{c}</code></li>)}</ul>
        : <p className="muted small">None; use <code>service</code> and <code>request</code> instead.</p>}
      <h3>Services (with request:)</h3>
      {svcs.length ? <ul>{svcs.map((s) => <li key={s.name}><code>{s.name}</code></li>)}</ul> : <p className="muted small">None yet.</p>}
      <h3>Phases</h3>
      <p className="small"><code>baseline</code>, <code>during</code>, <code>after</code></p>
      <h3>Assertions</h3>
      <ul>
        <li><code>response</code> <span className="muted">every result matches</span></li>
        <li><code>count</code> <span className="muted">atLeast results match</span></li>
        <li><code>eventual</code> <span className="muted">one matches withinSec</span></li>
        <li><code>recovery</code> <span className="muted">health passes withinSec</span></li>
      </ul>
      <p className="muted small">
        <code>expect</code>: <code>exitCode</code>, <code>outputContains</code> (commands); <code>status</code>,{' '}
        <code>bodyContains</code> (requests). The README's "Experiments" section has the details.
      </p>
    </aside>
  )
}

const faultOptions: Record<string, string> = {
  latency: 'latencyMs, jitterMs',
  drop: 'connections cut',
  timeout: 'timeoutMs',
  outage: 'refused',
  flap: 'downSec, upSec',
}

// newExperimentTemplate is a working start, using the first dependency and
// the first service's first command (or a request) from the setup.
export function newExperimentTemplate(env: Environment): string {
  const dep = env.dependencies.find((d) => !d.missing)
  const svc = env.services.find((s) => !s.missing && s.commands.length > 0) ?? env.services.find((s) => !s.missing)
  const cmd = svc?.commands[0]
  const send = cmd
    ? `command: ${svc!.name}/${cmd.name}`
    : `service: ${svc?.name ?? 'your-service'}\n    request: { method: GET, path: / }   # replace with a request that uses ${dep?.name ?? 'the dependency'}`
  const ok = cmd ? '{ exitCode: 0 }' : '{ status: 200 }'
  const faults = dep
    ? `faults:
  - dependency: ${dep.name}
    type: outage              # ${faultTypes.join(' | ')}
    durationSec: 30`
    : `faults: []                  # none = an integration test; add a dependency first to break one`
  return `name: ${dep ? `${dep.name}-outage` : 'new-experiment'}
description: What breaks, and what the service should do meanwhile.
maxDurationSec: 180            # safety cap: the run is aborted and every fault removed after this
${faults}
triggers:                      # requests to send, per phase: baseline | during | after
  - name: before
    phase: baseline
    ${send}
  - name: during
    phase: during
    ${send}
    repeat: 3
  - name: after
    phase: after
    ${send}
    repeat: 3
recoveryWindowSec: 30          # how long to watch after the faults are removed
assertions:
  - type: response             # steady state: the baseline must pass
    trigger: before
    expect: ${ok}
  - type: eventual             # works again within 20 s of the fault ending
    trigger: after
    expect: ${ok}
    withinSec: 20
`
}

// listItemToPlain turns Copy as YAML's text (a comment, then a list item
// indented under experiments:) into the editor's form: keys at column 0.
export function listItemToPlain(text: string): string {
  let lines = text.replace(/\r\n/g, '\n').split('\n')
  while (lines.length && (lines[0].trim() === '' || lines[0].trim().startsWith('#'))) lines.shift()
  const indent = (l: string) => l.length - l.trimStart().length
  const min = Math.min(...lines.filter((l) => l.trim()).map(indent))
  lines = lines.map((l) => l.slice(Math.min(min, indent(l))))
  if (lines[0]?.startsWith('- ')) {
    lines[0] = lines[0].slice(2)
    lines = lines.map((l, i) => (i === 0 ? l : l.slice(Math.min(2, indent(l)))))
  }
  return lines.join('\n').replace(/\n*$/, '\n')
}

// nameIn finds the experiment's top-level name: in the text.
export function nameIn(text: string): string | null {
  const m = /^(?:- )?name:[ \t]*(.*?)[ \t]*(?:#.*)?$/m.exec(text)
  if (!m || !m[1]) return null
  const v = m[1]
  return (v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'")) ? v.slice(1, -1) : v
}

function message(e: unknown) {
  return e instanceof Error ? e.message : String(e)
}
