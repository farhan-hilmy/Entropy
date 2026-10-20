import { useState, type ReactNode } from 'react'
import { api, type ConfigEdit, type RawConfig, type SyncResult } from './api'

// useConfigForm edits one part of the config file. open() reads the file as
// written (not the defaulted values the pages show) and remembers its hash, so
// saving fails instead of overwriting changes made in the file meanwhile.
export function useConfigForm<T>(
  pick: (c: RawConfig) => T | undefined,
  toEdit: (form: T) => ConfigEdit,
  onSaved: (form: T) => void,
) {
  const [form, setForm] = useState<T | null>(null)
  const [config, setConfig] = useState<RawConfig | null>(null)
  const [hash, setHash] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState<SyncResult | null>(null)

  async function open() {
    setBusy(true)
    setError(null)
    setSaved(null)
    try {
      const snap = await api.config()
      const picked = pick(snap.config)
      if (picked === undefined) throw new Error('It is no longer in the config file. Press Sync.')
      setConfig(snap.config)
      setHash(snap.hash)
      setForm(picked)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }

  async function save() {
    if (!form) return
    setBusy(true)
    setError(null)
    try {
      const res = await api.editConfig(hash, toEdit(form))
      setForm(null)
      setSaved(res.sync)
      onSaved(form)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }

  function cancel() {
    setForm(null)
    setError(null)
  }

  const update = (patch: Partial<T>) => setForm((f) => (f ? { ...f, ...patch } : f))

  return { form, config, busy, error, saved, open, save, cancel, update, dismissSaved: () => setSaved(null) }
}

export type ConfigForm<T> = ReturnType<typeof useConfigForm<T>>

function message(e: unknown) {
  return e instanceof Error ? e.message : String(e)
}

// EditFooter is the bottom of a panel: the "Edit" link, or Save/Cancel while editing,
// plus the result of the last save.
export function EditFooter<T>({ f }: { f: ConfigForm<T> }) {
  return (
    <>
      {f.error && <div className="banner error"><pre>{f.error}</pre></div>}
      {f.saved && <SavedNotice result={f.saved} onDismiss={f.dismissSaved} />}
      {f.form ? (
        <div className="edit-actions">
          <button className="btn primary" onClick={f.save} disabled={f.busy}>{f.busy ? 'Saving…' : 'Save'}</button>
          <button className="btn" onClick={f.cancel} disabled={f.busy}>Cancel</button>
          <span className="hint">Saves to the config file and syncs.</span>
        </div>
      ) : (
        <span className="hint">
          Defined in the config file ·{' '}
          <button className="link" onClick={f.open} disabled={f.busy}>{f.busy ? 'Loading…' : 'Edit'}</button>
        </span>
      )}
    </>
  )
}

// SaveButtons is the Save/Cancel row for a form opened some other way than EditFooter.
export function SaveButtons<T>({ f, label = 'Save' }: { f: ConfigForm<T>; label?: string }) {
  return (
    <>
      {f.error && <div className="banner error"><pre>{f.error}</pre></div>}
      <div className="edit-actions">
        <button className="btn primary" onClick={f.save} disabled={f.busy}>{f.busy ? 'Saving…' : label}</button>
        <button className="btn" onClick={f.cancel} disabled={f.busy}>Cancel</button>
        <span className="hint">Saves to the config file and syncs.</span>
      </div>
    </>
  )
}

function SavedNotice({ result, onDismiss }: { result: SyncResult; onDismiss: () => void }) {
  const problems = [...(result.error ? [result.error] : []), ...(result.warnings ?? [])]
  return (
    <div className={`banner ${problems.length ? 'warn' : 'ok'}`}>
      <div>
        <strong>Saved and synced.</strong>
        {problems.map((p) => <div key={p} className="small">⚠ {p}</div>)}
      </div>
      <button className="link" onClick={onDismiss}>Dismiss</button>
    </div>
  )
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="hint">{hint}</span>}
    </label>
  )
}

export function TextInput({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder?: string }) {
  return <input className="input" value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} spellCheck={false} />
}
