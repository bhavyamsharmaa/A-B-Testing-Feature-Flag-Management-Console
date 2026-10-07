import {
  MAX_VARIATIONS,
  MIN_VARIATIONS,
  defaultRows,
  makeDefault,
  newRowKey,
  type DraftVariation,
  type NonBooleanType,
  type ServerRowError,
  type Validation,
} from '../lib/variations'

const field =
  'rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:cursor-not-allowed disabled:opacity-50'
const bad = '!border-red-500/60'

interface Props {
  type: NonBooleanType
  rows: DraftVariation[]
  /** Client-side results; null until the user tries to submit. */
  validation: Validation | null
  serverError: ServerRowError | null
  disabled: boolean
  onChange: (rows: DraftVariation[]) => void
}

export function VariationsEditor({ type, rows, validation, serverError, disabled, onChange }: Props) {
  const set = (i: number, patch: Partial<DraftVariation>) => onChange(rows.map((r, k) => (k === i ? { ...r, ...patch } : r)))

  function addRow() {
    let n = rows.length
    while (rows.some((r) => r.id.trim() === `variant-${n}`)) n++
    onChange([...rows, { rowKey: newRowKey(), id: `variant-${n}`, text: defaultRows(type)[0].text }])
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-sm text-zinc-300">
          Variations <span className="text-zinc-500">({rows.length} of {MAX_VARIATIONS})</span>
        </span>
      </div>
      <p className="text-xs text-zinc-500">
        The first variation is the default: it is served when no targeting rule or rollout matches. The order is permanent
        after the flag is created, because rollout ranges follow it.
      </p>

      {rows.map((row, i) => {
        const err = validation?.rows[i]
        const server = serverError && serverError.row === i ? serverError : null
        return (
          <div key={row.rowKey} className="rounded-xl border border-white/10 bg-black/30 p-3">
            <div className="flex items-center justify-between gap-2">
              {i === 0 ? (
                <span className="rounded-full bg-accent/20 px-2.5 py-0.5 text-[11px] font-semibold uppercase tracking-wider text-accent">
                  Default
                </span>
              ) : (
                <button
                  type="button"
                  disabled={disabled}
                  onClick={() => onChange(makeDefault(rows, i))}
                  className="rounded-md border border-white/10 px-2 py-0.5 text-xs text-zinc-300 transition hover:border-accent/60 hover:text-white disabled:opacity-40"
                >
                  Make default
                </button>
              )}
              <button
                type="button"
                disabled={disabled || rows.length <= MIN_VARIATIONS}
                onClick={() => onChange(rows.filter((_, k) => k !== i))}
                aria-label={`Remove variation ${i + 1}`}
                title={rows.length <= MIN_VARIATIONS ? `A flag needs at least ${MIN_VARIATIONS} variations` : 'Remove variation'}
                className="rounded-md border border-white/10 px-2 py-0.5 text-xs text-zinc-300 transition hover:border-red-500/60 hover:text-red-300 disabled:cursor-not-allowed disabled:opacity-30"
              >
                Remove
              </button>
            </div>

            <div className="mt-2 grid gap-2 sm:grid-cols-[160px_1fr]">
              <div>
                <input
                  className={`${field} w-full font-mono ${err?.id || (server?.field === 'id') ? bad : ''}`}
                  placeholder="id"
                  aria-label={`Variation ${i + 1} id`}
                  spellCheck={false}
                  autoComplete="off"
                  disabled={disabled}
                  value={row.id}
                  onChange={(e) => set(i, { id: e.target.value })}
                />
                {err?.id && <p className="mt-1 text-xs text-red-400">{err.id}</p>}
              </div>
              <div>
                {type === 'number' ? (
                  <input
                    className={`${field} w-full font-mono ${err?.value || server?.field === 'value' ? bad : ''}`}
                    type="number"
                    step="any"
                    placeholder="0"
                    aria-label={`Variation ${i + 1} value`}
                    disabled={disabled}
                    value={row.text}
                    onChange={(e) => set(i, { text: e.target.value })}
                  />
                ) : (
                  <textarea
                    className={`${field} w-full ${type === 'json' ? 'font-mono' : ''} ${err?.value || server?.field === 'value' ? bad : ''}`}
                    rows={type === 'json' ? 3 : 1}
                    placeholder={type === 'json' ? '{ "key": "value" }' : 'value'}
                    aria-label={`Variation ${i + 1} value`}
                    spellCheck={false}
                    disabled={disabled}
                    value={row.text}
                    onChange={(e) => set(i, { text: e.target.value })}
                  />
                )}
                {err?.value && <p className="mt-1 text-xs text-red-400">{err.value}</p>}
              </div>
            </div>
            {server && (
              <p role="alert" className="mt-2 rounded-lg border border-red-500/40 bg-red-500/10 px-2.5 py-1.5 text-xs text-red-300">
                {server.text}
              </p>
            )}
          </div>
        )
      })}

      {validation?.form && <p className="text-xs text-red-400">{validation.form}</p>}

      <button
        type="button"
        onClick={addRow}
        disabled={disabled || rows.length >= MAX_VARIATIONS}
        className="rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-40"
      >
        + Add variation
      </button>
    </div>
  )
}
