import { useState } from 'react'
import { Link } from 'react-router-dom'
import { setExperimentControl } from '../api/experiments'
import { describeError } from '../lib/errors'
import { useEventCounts } from '../lib/useEventCounts'
import { absoluteTime, relativeTime, useNow } from '../lib/time'
import type { Experiment, ExperimentStatus } from '../types'

const STATUS: Record<ExperimentStatus, { label: string; cls: string }> = {
  draft: { label: 'Draft', cls: 'bg-white/10 text-zinc-300' },
  running: { label: 'Running', cls: 'bg-emerald-500/15 text-emerald-300' },
  stopped: { label: 'Stopped', cls: 'bg-zinc-500/15 text-zinc-400' },
}

/** One experiment and the raw data arriving for it. Read-only for every role; no results here. */
export function ExperimentCard({
  experiment,
  env,
  link = false,
  canEdit = false,
  onChanged,
}: {
  experiment: Experiment
  env: string
  link?: boolean
  canEdit?: boolean
  onChanged?: () => void
}) {
  const [picking, setPicking] = useState(experiment.controlVariationId)
  const [saving, setSaving] = useState(false)
  const [controlError, setControlError] = useState<string | null>(null)
  const { counts, failed } = useEventCounts(env, experiment.key, experiment.status === 'running')
  const now = useNow()
  const status = STATUS[experiment.status]
  const max = Math.max(1, ...(counts?.exposures.map((e) => e.count) ?? [0]))

  return (
    <article className="rounded-xl border border-white/10 bg-white/[0.03] p-4" aria-label={`Experiment ${experiment.key}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h3 className="text-sm font-medium text-zinc-100">{experiment.name}</h3>
          <p className="mt-0.5 font-mono text-[12px] text-zinc-500">
            {experiment.key} · flag {experiment.flagKey}
          </p>
        </div>
        <span className={`rounded-full px-2.5 py-0.5 text-xs font-medium ${status.cls}`}>{status.label}</span>
      </div>
      {experiment.hypothesis && <p className="mt-2 text-sm text-zinc-400">{experiment.hypothesis}</p>}
      <p className="mt-2 text-xs text-zinc-400" data-testid="control-line">
        Control: <span className="font-mono text-zinc-200">{experiment.controlVariationId || 'none'}</span>
        <span className="text-zinc-500"> (every other variation is compared with it)</span>
      </p>
      {canEdit && counts && counts.exposureTotal === 0 && experiment.variations.length > 1 && (
        <div className="mt-2 flex flex-wrap items-center gap-2" data-testid="control-picker">
          <label htmlFor={`control-${experiment.key}`} className="text-xs text-zinc-400">
            Change control
          </label>
          <select
            id={`control-${experiment.key}`}
            value={picking}
            disabled={saving}
            onChange={(e) => setPicking(e.target.value)}
            className="rounded-lg border border-white/10 bg-black/40 px-2 py-1 text-xs"
          >
            {experiment.variations.map((v) => (
              <option key={v.id} value={v.id}>
                {v.id}
              </option>
            ))}
          </select>
          <button
            disabled={saving || picking === experiment.controlVariationId}
            onClick={async () => {
              setSaving(true)
              setControlError(null)
              try {
                await setExperimentControl(env, experiment.key, picking)
                onChanged?.()
              } catch (err) {
                setControlError(describeError(err, 'change the control'))
              } finally {
                setSaving(false)
              }
            }}
            className="rounded-lg border border-white/10 px-2.5 py-1 text-xs text-zinc-200 hover:border-accent/60 hover:bg-accent/10 disabled:opacity-50"
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
          <span className="text-[11px] text-zinc-500">Fixed once the first exposure is recorded.</span>
        </div>
      )}
      {canEdit && counts && counts.exposureTotal > 0 && <p className="mt-1 text-[11px] text-zinc-500">The control is fixed: this experiment has exposures.</p>}
      {controlError && (
        <p role="alert" className="mt-1 text-xs text-red-300">
          {controlError}
        </p>
      )}

      <div className="mt-4">
        <div className="flex items-baseline justify-between text-xs uppercase tracking-wider text-zinc-500">
          <span>Exposures by variation</span>
          <span data-testid="exposure-total" className="font-mono normal-case tracking-normal text-zinc-300">
            {counts ? counts.exposureTotal.toLocaleString() : '—'} total
          </span>
        </div>
        {counts ? (
          <ul className="mt-2 space-y-1.5">
            {counts.exposures.map((e) => (
              <li key={e.variationId} className="flex items-center gap-3 text-sm" data-testid={`exposure-${e.variationId}`}>
                <span className="w-28 truncate font-mono text-[13px] text-zinc-300" title={e.variationId}>
                  {e.variationId}
                </span>
                <span className="h-2 flex-1 overflow-hidden rounded-full bg-white/[0.06]" aria-hidden="true">
                  <span className="block h-full rounded-full bg-accent/70 transition-all" style={{ width: `${(e.count / max) * 100}%` }} />
                </span>
                <span className="w-16 text-right font-mono text-[13px] text-zinc-200">{e.count.toLocaleString()}</span>
              </li>
            ))}
          </ul>
        ) : (
          <div className="mt-2 h-10 animate-shimmer rounded-lg bg-white/[0.06]" role="status" aria-label="Loading counts" />
        )}
      </div>

      <p className="mt-3 text-xs text-zinc-500">
        <span data-testid="event-total" className="font-mono text-zinc-300">
          {counts ? counts.events.toLocaleString() : '—'}
        </span>{' '}
        other events
        {counts?.lastEventAt && (
          <>
            {' '}
            · last <span title={absoluteTime(counts.lastEventAt)}>{relativeTime(counts.lastEventAt, now)}</span>
          </>
        )}
        {experiment.status === 'running' && !failed && <span className="ml-2 text-emerald-400/80">· updating live</span>}
        {failed && <span className="ml-2 text-red-300">· could not refresh</span>}
      </p>
      {link && (
        <p className="mt-3 text-sm">
          <Link to={`/console/experiments/${encodeURIComponent(experiment.key)}/results`} className="text-zinc-200 underline-offset-2 hover:text-white hover:underline" data-testid="open-results">
            Open data and results →
          </Link>
        </p>
      )}
      {experiment.configChangedSinceStart && (
        <p className="mt-2 text-xs text-amber-300/90">The flag's targeting or rollout changed since this experiment started.</p>
      )}
    </article>
  )
}
