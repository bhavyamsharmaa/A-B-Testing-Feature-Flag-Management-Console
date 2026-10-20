import { useEffect, useState } from 'react'
import { createExperiment } from '../api/experiments'
import { listFlags } from '../api/flags'
import { describeError } from '../lib/errors'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { Flag } from '../types'
import { Modal } from './Modal'
import { field } from './TargetingEditor'

type MetricRow = { name: string; eventName: string; type: 'conversion' | 'value' }

/** Creates a draft experiment on a flag. The creator chooses the control: the variation everything else is compared with. */
export function CreateExperimentDialog({ env, onClose, onCreated }: { env: string; onClose: () => void; onCreated: () => void }) {
  const [flags, setFlags] = useState<Flag[] | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [flagKey, setFlagKey] = useState('')
  const [key, setKey] = useState('')
  const [name, setName] = useState('')
  const [hypothesis, setHypothesis] = useState('')
  const [control, setControl] = useState('')
  const [metrics, setMetrics] = useState<MetricRow[]>([{ name: 'Conversion', eventName: '', type: 'conversion' }])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showErrors, setShowErrors] = useState(false)
  const slow = useSlowHint(busy)

  useEffect(() => {
    let live = true
    listFlags(env)
      .then((f) => live && setFlags(f))
      .catch((err) => live && setLoadError(describeError(err, 'load flags')))
    return () => {
      live = false
    }
  }, [env])

  const flag = flags?.find((f) => f.key === flagKey)
  const variations = flag?.variations ?? []
  // The default control is the flag's first variation; the select shows it chosen.
  const effectiveControl = control && variations.some((v) => v.id === control) ? control : (variations[0]?.id ?? '')

  const problems: string[] = []
  if (!flagKey) problems.push('Choose a flag.')
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(key)) problems.push('Key is 1-128 letters, digits, "_", "." or "-", starting with a letter or digit.')
  if (!name.trim() || name.length > 120) problems.push('Name is required (at most 120 characters).')
  const rows = metrics.filter((m) => m.name.trim() || m.eventName.trim())
  if (rows.length === 0) problems.push('Add at least one metric.')
  if (rows.some((m) => !m.name.trim() || !m.eventName.trim())) problems.push('Each metric needs a name and an event name.')
  if (rows.some((m) => m.eventName.startsWith('$'))) problems.push('Event names starting with "$" are reserved.')
  if (new Set(rows.map((m) => m.eventName)).size !== rows.length) problems.push('Each metric needs its own event name.')

  async function submit() {
    setShowErrors(true)
    setError(null)
    if (problems.length) return
    setBusy(true)
    try {
      await createExperiment(env, {
        flagKey,
        key,
        name: name.trim(),
        hypothesis: hypothesis.trim(),
        controlVariationId: effectiveControl,
        metrics: rows.map((m, i) => ({ name: m.name.trim(), eventName: m.eventName.trim(), type: m.type, isPrimary: i === 0 })),
      })
      onCreated()
    } catch (err) {
      setError(describeError(err, 'create experiments'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="Create an experiment" onClose={onClose} busy={busy} wide>
      <div className="mt-4 space-y-5">
        <p className="text-sm text-zinc-400">
          An experiment compares the variations of one flag. It starts as a draft and changes nothing for users until you start it.
        </p>
        {loadError && (
          <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {loadError}
          </p>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label htmlFor="ex-flag" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Flag
            </label>
            <select id="ex-flag" className={`${field} mt-1 w-full`} value={flagKey} disabled={busy || flags === null} onChange={(e) => { setFlagKey(e.target.value); setControl('') }}>
              <option value="">{flags === null ? 'Loading…' : 'Choose a flag…'}</option>
              {flags?.map((f) => (
                <option key={f.key} value={f.key}>
                  {f.name} ({f.key})
                </option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor="ex-control" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Control
            </label>
            <select id="ex-control" className={`${field} mt-1 w-full`} value={effectiveControl} disabled={busy || variations.length === 0} onChange={(e) => setControl(e.target.value)}>
              {variations.length === 0 && <option value="">Choose a flag first</option>}
              {variations.map((v, i) => (
                <option key={v.id} value={v.id}>
                  {v.id}
                  {i === 0 ? ' (the flag’s first variation)' : ''}
                </option>
              ))}
            </select>
          </div>
        </div>
        <p className="-mt-3 text-xs text-zinc-400">
          Every other variation is compared with the control. You can change it until the first exposure is recorded; after that it is fixed.
        </p>
        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label htmlFor="ex-key" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Key
            </label>
            <input id="ex-key" className={`${field} mt-1 w-full font-mono`} value={key} disabled={busy} placeholder="checkout-copy" onChange={(e) => setKey(e.target.value)} />
          </div>
          <div>
            <label htmlFor="ex-name" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Name
            </label>
            <input id="ex-name" className={`${field} mt-1 w-full`} value={name} disabled={busy} maxLength={120} placeholder="Checkout button copy" onChange={(e) => setName(e.target.value)} />
          </div>
        </div>
        <div>
          <label htmlFor="ex-hyp" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
            Hypothesis <span className="normal-case text-zinc-600">(optional)</span>
          </label>
          <input id="ex-hyp" className={`${field} mt-1 w-full`} value={hypothesis} disabled={busy} onChange={(e) => setHypothesis(e.target.value)} />
        </div>
        <fieldset disabled={busy}>
          <legend className="text-xs font-medium uppercase tracking-wider text-zinc-500">Metrics (the first is the primary)</legend>
          <div className="mt-2 space-y-2">
            {metrics.map((m, i) => (
              <div key={i} className="grid gap-2 sm:grid-cols-[1fr_1fr_9rem_auto]">
                <input aria-label={`Metric ${i + 1} name`} className={field} placeholder="Name" value={m.name} onChange={(e) => setMetrics(metrics.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
                <input aria-label={`Metric ${i + 1} event name`} className={`${field} font-mono`} placeholder="event_name" value={m.eventName} onChange={(e) => setMetrics(metrics.map((x, j) => (j === i ? { ...x, eventName: e.target.value } : x)))} />
                <select aria-label={`Metric ${i + 1} type`} className={field} value={m.type} onChange={(e) => setMetrics(metrics.map((x, j) => (j === i ? { ...x, type: e.target.value as MetricRow['type'] } : x)))}>
                  <option value="conversion">Conversion</option>
                  <option value="value">Value</option>
                </select>
                {metrics.length > 1 && (
                  <button type="button" aria-label={`Remove metric ${i + 1}`} className="rounded-lg border border-white/10 px-2.5 text-sm text-zinc-300 hover:bg-white/5" onClick={() => setMetrics(metrics.filter((_, j) => j !== i))}>
                    ✕
                  </button>
                )}
              </div>
            ))}
          </div>
          {metrics.length < 5 && (
            <button type="button" className="mt-2 text-xs text-accent underline" onClick={() => setMetrics([...metrics, { name: '', eventName: '', type: 'conversion' }])}>
              Add a metric
            </button>
          )}
        </fieldset>
        {showErrors && problems.length > 0 && (
          <ul className="space-y-0.5 text-xs text-red-400" data-testid="experiment-problems">
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        {error && (
          <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}
        {busy && slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}
        <div className="flex justify-end gap-2 border-t border-white/5 pt-4">
          <button onClick={onClose} disabled={busy} className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50">
            Cancel
          </button>
          <button
            onClick={() => void submit()}
            disabled={busy}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy ? 'Creating…' : 'Create experiment'}
          </button>
        </div>
      </div>
    </Modal>
  )
}
