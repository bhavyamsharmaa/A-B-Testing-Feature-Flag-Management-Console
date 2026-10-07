import { useState } from 'react'
import {
  OPERATORS,
  VALUE_TYPES,
  changeOperator,
  newClause,
  newRule,
  usesValueType,
  type DraftClause,
  type DraftRule,
  type Operator,
  type RuleErrors,
  type ServerRuleError,
  type ValueType,
} from '../lib/targeting'
import type { Variation } from '../types'

const field =
  'rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:cursor-not-allowed disabled:opacity-50'
const iconBtn =
  'rounded-md border border-white/10 px-2 py-1 text-xs text-zinc-300 transition hover:border-accent/60 hover:text-white disabled:cursor-not-allowed disabled:opacity-30 disabled:hover:border-white/10 disabled:hover:text-zinc-300'

interface Props {
  rules: DraftRule[]
  variations: Variation[]
  /** Client-side validation results; null until the user tries to save. */
  errors: RuleErrors[] | null
  serverError: ServerRuleError | null
  disabled: boolean
  onChange: (rules: DraftRule[]) => void
}

const variationLabel = (v: Variation) => {
  const value = JSON.stringify(v.value)
  return `${v.id} (${value.length > 24 ? `${value.slice(0, 24)}…` : value})`
}

export function TargetingEditor({ rules, variations, errors, serverError, disabled, onChange }: Props) {
  const setRule = (i: number, patch: Partial<DraftRule>) =>
    onChange(rules.map((r, k) => (k === i ? { ...r, ...patch } : r)))
  const setClause = (i: number, j: number, next: DraftClause) =>
    setRule(i, { clauses: rules[i].clauses.map((c, k) => (k === j ? next : c)) })
  const move = (i: number, delta: number) => {
    const k = i + delta
    if (k < 0 || k >= rules.length) return
    const next = [...rules]
    ;[next[i], next[k]] = [next[k], next[i]]
    onChange(next)
  }

  return (
    <div className="space-y-3">
      {rules.length === 0 && (
        <p className="rounded-lg border border-dashed border-white/10 px-4 py-6 text-center text-sm text-zinc-400">
          No targeting rules. Every user falls through to the rollout.
        </p>
      )}

      {rules.map((rule, i) => {
        const err = errors?.[i]
        const server = serverError && serverError.rule === i ? serverError : null
        return (
          <div key={rule.id} className="rounded-xl border border-white/10 bg-black/30 p-3">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-semibold uppercase tracking-wider text-zinc-400">Rule {i + 1}</span>
              <div className="flex gap-1.5">
                <button className={iconBtn} disabled={disabled || i === 0} onClick={() => move(i, -1)} aria-label={`Move rule ${i + 1} up`}>
                  ↑
                </button>
                <button
                  className={iconBtn}
                  disabled={disabled || i === rules.length - 1}
                  onClick={() => move(i, 1)}
                  aria-label={`Move rule ${i + 1} down`}
                >
                  ↓
                </button>
                <button
                  className={`${iconBtn} hover:!border-red-500/60 hover:!text-red-300`}
                  disabled={disabled}
                  onClick={() => onChange(rules.filter((_, k) => k !== i))}
                  aria-label={`Delete rule ${i + 1}`}
                >
                  Delete
                </button>
              </div>
            </div>

            {rule.locked ? (
              <div className="mt-3 space-y-2">
                <p className="text-xs text-amber-300">
                  This rule uses a shape the builder can't edit (for example a mixed-type list). It is kept exactly as it
                  is when you save. You can reorder or delete it.
                </p>
                <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-white/10 bg-black/40 p-2 font-mono text-xs text-zinc-300">
                  {JSON.stringify(rule.locked, null, 2)}
                </pre>
              </div>
            ) : (
              <div className="mt-3 space-y-2">
                {rule.clauses.map((clause, j) => (
                  <div key={clause.id}>
                    {j > 0 && <div className="py-1 text-[11px] font-semibold tracking-widest text-accent">AND</div>}
                    <ClauseRow
                      clause={clause}
                      disabled={disabled}
                      canRemove={rule.clauses.length > 1}
                      error={err?.clauses[j] ?? null}
                      serverText={server && server.clause === j ? server.text : null}
                      onChange={(next) => setClause(i, j, next)}
                      onRemove={() => setRule(i, { clauses: rule.clauses.filter((_, k) => k !== j) })}
                    />
                  </div>
                ))}
                <button
                  className="text-xs text-accent transition hover:underline disabled:cursor-not-allowed disabled:opacity-40"
                  disabled={disabled}
                  onClick={() => setRule(i, { clauses: [...rule.clauses, newClause()] })}
                >
                  + Add clause (AND)
                </button>

                <div className="flex items-center gap-2 border-t border-white/5 pt-3">
                  <label htmlFor={`serve-${rule.id}`} className="text-sm text-zinc-300">
                    Serve
                  </label>
                  <select
                    id={`serve-${rule.id}`}
                    className={`${field} min-w-[160px]`}
                    disabled={disabled}
                    value={rule.variationId}
                    onChange={(e) => setRule(i, { variationId: e.target.value })}
                  >
                    {!variations.some((v) => v.id === rule.variationId) && <option value={rule.variationId}>{rule.variationId || 'Choose…'}</option>}
                    {variations.map((v) => (
                      <option key={v.id} value={v.id}>
                        {variationLabel(v)}
                      </option>
                    ))}
                  </select>
                </div>
              </div>
            )}

            {err?.rule && <p className="mt-2 text-xs text-red-400">{err.rule}</p>}
            {server && (server.clause === null || rule.locked) && (
              <p role="alert" className="mt-2 rounded-lg border border-red-500/40 bg-red-500/10 px-2.5 py-1.5 text-xs text-red-300">
                {server.text}
              </p>
            )}
          </div>
        )
      })}

      <button
        onClick={() => onChange([...rules, newRule(variations[0]?.id ?? '')])}
        disabled={disabled}
        className="rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-40"
      >
        + Add rule
      </button>
    </div>
  )
}

interface ClauseProps {
  clause: DraftClause
  disabled: boolean
  canRemove: boolean
  error: string | null
  serverText: string | null
  onChange: (next: DraftClause) => void
  onRemove: () => void
}

function ClauseRow({ clause, disabled, canRemove, error, serverText, onChange, onRemove }: ClauseProps) {
  const set = (patch: Partial<DraftClause>) => onChange({ ...clause, ...patch })
  const op = clause.operator

  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-start gap-2">
        <input
          className={`${field} w-32 font-mono`}
          placeholder="attribute"
          aria-label="Attribute name"
          spellCheck={false}
          autoComplete="off"
          disabled={disabled}
          value={clause.attribute}
          onChange={(e) => set({ attribute: e.target.value })}
        />
        <select
          className={field}
          aria-label="Operator"
          disabled={disabled}
          value={op}
          onChange={(e) => onChange(changeOperator(clause, e.target.value as Operator))}
        >
          {OPERATORS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>

        {usesValueType(op) && (
          <select
            className={field}
            aria-label="Value type"
            disabled={disabled}
            value={clause.valueType}
            // Changing the type clears the value: "5" the text is not 5 the number.
            onChange={(e) => set({ valueType: e.target.value as ValueType, text: e.target.value === 'boolean' ? 'true' : '', items: [] })}
          >
            {VALUE_TYPES.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </select>
        )}

        <ValueInput clause={clause} disabled={disabled} onChange={onChange} />

        <button
          className={iconBtn}
          disabled={disabled || !canRemove}
          onClick={onRemove}
          aria-label="Remove clause"
          title={canRemove ? 'Remove clause' : 'A rule needs at least one clause'}
        >
          ✕
        </button>
      </div>
      {error && <p className="text-xs text-red-400">{error}</p>}
      {serverText && (
        <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-2.5 py-1.5 text-xs text-red-300">
          {serverText}
        </p>
      )}
    </div>
  )
}

function ValueInput({ clause, disabled, onChange }: { clause: DraftClause; disabled: boolean; onChange: (c: DraftClause) => void }) {
  const op = clause.operator
  const set = (patch: Partial<DraftClause>) => onChange({ ...clause, ...patch })

  if (op === 'exists') return <span className="self-center text-xs text-zinc-500">no value needed</span>
  if (op === 'in') {
    return <ChipsInput items={clause.items} disabled={disabled} onChange={(items) => set({ items })} />
  }
  if (op === 'greaterThan' || clause.valueType === 'number') {
    return (
      <input
        className={`${field} w-28 font-mono`}
        type="number"
        step="any"
        placeholder="0"
        aria-label="Number value"
        disabled={disabled}
        value={clause.text}
        onChange={(e) => set({ text: e.target.value })}
      />
    )
  }
  if (clause.valueType === 'boolean') {
    return (
      <select className={field} aria-label="Boolean value" disabled={disabled} value={clause.text || 'true'} onChange={(e) => set({ text: e.target.value })}>
        <option value="true">true</option>
        <option value="false">false</option>
      </select>
    )
  }
  return (
    <input
      className={`${field} w-36`}
      placeholder="value"
      aria-label="Text value"
      disabled={disabled}
      value={clause.text}
      onChange={(e) => set({ text: e.target.value })}
    />
  )
}

/** Comma-separated chips: type a value and press Enter or comma; Backspace removes the last. */
function ChipsInput({ items, disabled, onChange }: { items: string[]; disabled: boolean; onChange: (items: string[]) => void }) {
  const [draft, setDraft] = useState('')

  const commit = (raw: string) => {
    const added = raw
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s !== '' && !items.includes(s))
    if (added.length > 0) onChange([...items, ...added])
    setDraft('')
  }

  return (
    <div className={`${field} flex min-w-[180px] flex-1 flex-wrap items-center gap-1.5 !py-1`}>
      {items.map((item) => (
        <span key={item} className="inline-flex items-center gap-1 rounded-full bg-accent/15 px-2 py-0.5 font-mono text-xs text-accent">
          {item}
          <button
            disabled={disabled}
            onClick={() => onChange(items.filter((i) => i !== item))}
            aria-label={`Remove ${item}`}
            className="text-accent/70 transition hover:text-white disabled:cursor-not-allowed"
          >
            ✕
          </button>
        </span>
      ))}
      <input
        className="min-w-[80px] flex-1 bg-transparent text-sm outline-none disabled:cursor-not-allowed"
        placeholder={items.length === 0 ? 'a, b, c' : ''}
        aria-label="Add value"
        disabled={disabled}
        value={draft}
        onChange={(e) => (e.target.value.includes(',') ? commit(e.target.value) : setDraft(e.target.value))}
        onBlur={() => commit(draft)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            commit(draft)
          } else if (e.key === 'Backspace' && draft === '' && items.length > 0) {
            onChange(items.slice(0, -1))
          }
        }}
      />
    </div>
  )
}
