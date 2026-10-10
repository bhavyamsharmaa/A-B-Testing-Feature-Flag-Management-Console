import { useId, useState } from 'react'
import { KIND_WORDS, metricLabel } from '../../lib/resultsFormat'
import type { MetricKind, MetricSelection } from '../../types'

export const MAX_METRICS = 3

export interface MetricOption {
  eventName: string
  /** The kind to offer first (a defined metric's own type). */
  defaultKind: MetricKind
  /** The kinds the data supports for this event (from event-counts). */
  kinds: MetricKind[]
  /** Shown beside the name: "primary", "defined" or a count of events. */
  hint: string
}

interface Props {
  selected: MetricSelection[]
  options: MetricOption[]
  primaryLabel: string | null
  onChange: (next: MetricSelection[]) => void
}

/** Up to three metrics: chips to remove, and a small form to add one. Plain form controls, so the keyboard works. */
export function MetricPicker({ selected, options, primaryLabel, onChange }: Props) {
  const nameId = useId()
  const kindId = useId()
  const [eventName, setEventName] = useState('')
  const [kind, setKind] = useState<MetricKind>('conversion')
  const full = selected.length >= MAX_METRICS
  const kindsFor = options.find((o) => o.eventName === eventName)?.kinds ?? (Object.keys(KIND_WORDS) as MetricKind[])
  const duplicate = selected.some((m) => m.eventName === eventName && m.kind === kind)

  return (
    <div className="space-y-2" data-testid="metric-picker">
      <p className="text-xs uppercase tracking-wider text-zinc-400">Metrics</p>
      <ul className="flex flex-wrap items-center gap-2" aria-label="Selected metrics">
        {selected.length === 0 ? (
          <li className="rounded-full border border-white/15 bg-white/[0.04] px-3 py-1 text-sm text-zinc-100">
            {primaryLabel ? `${primaryLabel} (primary, default)` : 'Primary metric (default)'}
          </li>
        ) : (
          selected.map((m) => (
            <li key={`${m.eventName}:${m.kind}`} className="flex items-center gap-1 rounded-full border border-white/15 bg-white/[0.04] py-1 pl-3 pr-1 text-sm text-zinc-100">
              <span className="font-mono">{metricLabel(m)}</span>
              <button
                type="button"
                aria-label={`Remove metric ${metricLabel(m)}`}
                onClick={() => onChange(selected.filter((x) => x !== m))}
                className="rounded-full px-2 text-zinc-300 transition hover:bg-white/10 hover:text-white focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              >
                <span aria-hidden="true">✕</span>
              </button>
            </li>
          ))
        )}
      </ul>

      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (!eventName || full || duplicate) return
          onChange([...selected, { eventName, kind }])
          setEventName('')
        }}
      >
        <div>
          <label htmlFor={nameId} className="block text-xs text-zinc-400">Event</label>
          <select
            id={nameId}
            value={eventName}
            onChange={(e) => {
              setEventName(e.target.value)
              const o = options.find((x) => x.eventName === e.target.value)
              if (o) setKind(o.kinds.includes(o.defaultKind) ? o.defaultKind : o.kinds[0])
            }}
            disabled={full || options.length === 0}
            className="mt-0.5 rounded-lg border border-white/15 bg-surface px-2.5 py-1.5 text-sm text-zinc-100 disabled:opacity-50"
          >
            <option value="">{options.length === 0 ? 'No events yet' : 'Choose an event…'}</option>
            {options.map((o) => (
              <option key={o.eventName} value={o.eventName}>
                {o.eventName} ({o.hint})
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor={kindId} className="block text-xs text-zinc-400">Measured as</label>
          <select
            id={kindId}
            value={kind}
            onChange={(e) => setKind(e.target.value as MetricKind)}
            disabled={full}
            className="mt-0.5 rounded-lg border border-white/15 bg-surface px-2.5 py-1.5 text-sm text-zinc-100 disabled:opacity-50"
          >
            {kindsFor.map((k) => (
              <option key={k} value={k}>{KIND_WORDS[k]}</option>
            ))}
          </select>
        </div>
        <button
          type="submit"
          disabled={!eventName || full || duplicate}
          className="rounded-lg border border-white/15 bg-white/[0.04] px-3 py-1.5 text-sm text-zinc-100 transition hover:border-accent/60 hover:bg-accent/10 disabled:cursor-not-allowed disabled:opacity-50"
        >
          Add metric
        </button>
      </form>
      <p className="text-xs text-zinc-400">
        {full ? `The limit is ${MAX_METRICS} metrics; remove one to add another.` : `Up to ${MAX_METRICS} metrics. With none chosen, the primary metric is used.`}
      </p>
    </div>
  )
}
