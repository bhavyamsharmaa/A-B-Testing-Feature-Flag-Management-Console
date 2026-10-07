import { useEffect, useRef, useState } from 'react'
import { NO_FILTERS, type AuditFilters as Filters } from '../api/audit'
import { actionLabel } from '../lib/auditLabels'

const MAX_RESOURCE_ID = 256 // the backend rejects longer values with a 400
const input =
  'rounded-lg border border-white/10 bg-black/40 px-3 py-1.5 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20'

interface Props {
  value: Filters
  actions: string[] // known plus seen so far
  onChange: (next: Filters) => void
}

export function AuditFilters({ value, actions, onChange }: Props) {
  const [resourceText, setResourceText] = useState(value.resourceId)
  const latest = useRef({ value, onChange })
  latest.current = { value, onChange }

  // The resource id is free text: apply it once typing pauses, not per keystroke.
  useEffect(() => {
    const trimmed = resourceText.trim()
    if (trimmed.length > MAX_RESOURCE_ID || trimmed === latest.current.value.resourceId) return
    const t = setTimeout(() => latest.current.onChange({ ...latest.current.value, resourceId: trimmed }), 400)
    return () => clearTimeout(t)
  }, [resourceText])

  const tooLong = resourceText.trim().length > MAX_RESOURCE_ID
  const active = value.action !== '' || value.resourceId !== '' || value.severity !== '' || resourceText !== ''

  return (
    <div className="flex flex-wrap items-end gap-3">
      <label className="text-xs text-zinc-500">
        Action
        <select
          value={value.action}
          onChange={(e) => onChange({ ...value, action: e.target.value })}
          className={`${input} mt-1 block min-w-[180px]`}
        >
          <option value="">All actions</option>
          {actions.map((a) => (
            <option key={a} value={a}>
              {actionLabel(a)}
              {actionLabel(a) !== a ? ` (${a})` : ''}
            </option>
          ))}
        </select>
      </label>

      <label className="text-xs text-zinc-500">
        Severity
        <select
          value={value.severity}
          onChange={(e) => onChange({ ...value, severity: e.target.value as Filters['severity'] })}
          className={`${input} mt-1 block`}
        >
          <option value="">Any severity</option>
          <option value="info">info</option>
          <option value="critical">critical</option>
        </select>
      </label>

      <label className="text-xs text-zinc-500">
        Resource id
        <input
          value={resourceText}
          onChange={(e) => setResourceText(e.target.value)}
          placeholder="e.g. ui-test-flag"
          spellCheck={false}
          autoComplete="off"
          aria-invalid={tooLong}
          className={`${input} mt-1 block w-56 font-mono ${tooLong ? 'border-red-500/60' : ''}`}
        />
        {tooLong && <span className="text-red-400">At most {MAX_RESOURCE_ID} characters.</span>}
      </label>

      <button
        onClick={() => {
          setResourceText('')
          onChange(NO_FILTERS)
        }}
        disabled={!active}
        className="rounded-lg border border-white/10 px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:text-white disabled:cursor-not-allowed disabled:opacity-40"
      >
        Clear
      </button>
    </div>
  )
}
