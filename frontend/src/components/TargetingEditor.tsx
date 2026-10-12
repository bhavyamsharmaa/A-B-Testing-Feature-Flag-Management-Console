import { useState } from 'react'
import { Link } from 'react-router-dom'
import {
  MAX_REGEX_BYTES,
  OPERATORS,
  VALUE_TYPES,
  canNegate,
  changeOperator,
  isSegmentOperator,
  newClause,
  newRule,
  usesValueType,
  valueKind,
  type DraftClause,
  type DraftRule,
  type Operator,
  type RuleErrors,
  type ServerRuleError,
  type ValueType,
} from '../lib/targeting'
import type { SegmentSummary, Variation } from '../types'

export const field =
  'rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:cursor-not-allowed disabled:opacity-50'
export const iconBtn =
  'rounded-md border border-white/10 px-2 py-1 text-xs text-zinc-300 transition hover:border-accent/60 hover:text-white disabled:cursor-not-allowed disabled:opacity-30 disabled:hover:border-white/10 disabled:hover:text-zinc-300'

interface Props {
  rules: DraftRule[]
  variations: Variation[]
  /** Client-side validation results; null until the user tries to save. */
  errors: RuleErrors[] | null
  serverError: ServerRuleError | null
  disabled: boolean
  /** The workspace's segments for the segment picker; null while they load or when they failed to. */
  segments: SegmentSummary[] | null
  /** Why `segments` is null, when it failed. */
  segmentsError?: string | null
  onChange: (rules: DraftRule[]) => void
}

const variationLabel = (v: Variation) => {
  const value = JSON.stringify(v.value)
  return `${v.id} (${value.length > 24 ? `${value.slice(0, 24)}…` : value})`
}

export function TargetingEditor({ rules, variations, errors, serverError, disabled, segments, segmentsError, onChange }: Props) {
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
                      segments={segments}
                      segmentsError={segmentsError ?? null}
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
  /** Segments to pick from; null = not available. Without it (segment rules) the segment operators are not offered. */
  segments: SegmentSummary[] | null
  segmentsError?: string | null
  /** Segment rules cannot reference segments, so they never offer the segment operators. */
  noSegmentOperators?: boolean
  error: string | null
  serverText: string | null
  onChange: (next: DraftClause) => void
  onRemove: () => void
}

const GROUPS = ['Compare', 'Text', 'Number', 'Version', 'Date', 'Audience'] as const

export function ClauseRow({ clause, disabled, canRemove, segments, segmentsError, noSegmentOperators, error, serverText, onChange, onRemove }: ClauseProps) {
  const set = (patch: Partial<DraftClause>) => onChange({ ...clause, ...patch })
  const op = clause.operator
  const kind = valueKind(op)
  const segmentOp = isSegmentOperator(op)

  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-start gap-2">
        {!segmentOp && (
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
        )}
        {canNegate(op) && (
          <button
            type="button"
            aria-pressed={clause.negate}
            disabled={disabled}
            onClick={() => set({ negate: !clause.negate })}
            title={clause.negate ? 'Negated: matches when the operator does not' : 'Click to negate this operator'}
            className={`${iconBtn} self-start py-1.5 font-semibold tracking-wider ${clause.negate ? '!border-accent !bg-accent/20 !text-white' : ''}`}
          >
            {clause.negate ? '✓ NOT' : 'NOT'}
          </button>
        )}
        <select
          className={field}
          aria-label="Operator"
          disabled={disabled}
          value={op}
          onChange={(e) => onChange(changeOperator(clause, e.target.value as Operator))}
        >
          {GROUPS.filter((g) => !(g === 'Audience' && noSegmentOperators)).map((g) => (
            <optgroup key={g} label={g}>
              {OPERATORS.filter((o) => o.group === g).map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </optgroup>
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

        <ValueInput clause={clause} disabled={disabled} segments={segments} segmentsError={segmentsError ?? null} onChange={onChange} />

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
      {hint(kind) && <p className="text-[11px] text-zinc-500">{hint(kind)}</p>}
      {error && <p className="text-xs text-red-400">{error}</p>}
      {serverText && (
        <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-2.5 py-1.5 text-xs text-red-300">
          {serverText}
        </p>
      )}
    </div>
  )
}

function hint(kind: ReturnType<typeof valueKind>): string | null {
  switch (kind) {
    case 'semver':
      return 'A version like 1.4.0 or 2.0.0-rc.1. Attributes that are not valid versions never match.'
    case 'date':
      return 'RFC 3339, like 2024-05-01T00:00:00Z. Attributes that are not RFC 3339 timestamps never match.'
    case 'regex':
      return `Go (RE2) syntax, no lookahead or backreferences; matches anywhere unless anchored with ^ and $. Up to ${MAX_REGEX_BYTES} bytes; attribute values over 2,048 bytes never match.`
    default:
      return null
  }
}

function ValueInput({
  clause,
  disabled,
  segments,
  segmentsError,
  onChange,
}: {
  clause: DraftClause
  disabled: boolean
  segments: SegmentSummary[] | null
  segmentsError: string | null
  onChange: (c: DraftClause) => void
}) {
  const op = clause.operator
  const kind = valueKind(op)
  const set = (patch: Partial<DraftClause>) => onChange({ ...clause, ...patch })

  switch (kind) {
    case 'none':
      return <span className="self-center text-xs text-zinc-500">no value needed</span>
    case 'list':
      return <ChipsInput items={clause.items} disabled={disabled} onChange={(items) => set({ items })} />
    case 'segment':
      return <SegmentPicker value={clause.text} disabled={disabled} segments={segments} error={segmentsError} onChange={(text) => set({ text })} />
    case 'number':
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
    case 'scalar':
      if (clause.valueType === 'number') {
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
        <input className={`${field} w-36`} placeholder="value" aria-label="Text value" disabled={disabled} value={clause.text} onChange={(e) => set({ text: e.target.value })} />
      )
    default: {
      const label = { text: 'Text value', semver: 'Version', date: 'Timestamp', regex: 'Regular expression' }[kind]
      const placeholder = { text: 'value', semver: '1.4.0', date: '2024-05-01T00:00:00Z', regex: '^pro-.*$' }[kind]
      return (
        <input
          className={`${field} ${kind === 'text' ? 'w-36' : 'w-56 font-mono'}`}
          placeholder={placeholder}
          aria-label={label}
          spellCheck={false}
          autoComplete="off"
          disabled={disabled}
          value={clause.text}
          onChange={(e) => set({ text: e.target.value })}
        />
      )
    }
  }
}

/** Picks one of the workspace's segments. A key that no longer exists stays visible (and is flagged) instead of silently changing. */
function SegmentPicker({
  value,
  disabled,
  segments,
  error,
  onChange,
}: {
  value: string
  disabled: boolean
  segments: SegmentSummary[] | null
  error: string | null
  onChange: (key: string) => void
}) {
  if (segments === null) {
    return (
      <span className="self-center text-xs text-zinc-500" role={error ? 'alert' : 'status'}>
        {error ? `Segments could not be loaded: ${error}` : 'Loading segments…'}
      </span>
    )
  }
  const known = segments.some((s) => s.key === value)
  return (
    <div className="flex flex-wrap items-center gap-2">
      <select className={`${field} min-w-[180px]`} aria-label="Segment" disabled={disabled} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Choose a segment…</option>
        {value !== '' && !known && <option value={value}>{value} (does not exist)</option>}
        {segments.map((s) => (
          <option key={s.key} value={s.key}>
            {s.name} ({s.key})
          </option>
        ))}
      </select>
      {value !== '' && !known && (
        <span className="text-xs text-amber-300">This segment no longer exists, so this rule matches nobody. Pick another or delete the rule.</span>
      )}
      {segments.length === 0 && (
        <span className="text-xs text-zinc-500">
          No segments yet. <Link to="/console/segments" className="text-accent underline">Create one</Link>.
        </span>
      )}
    </div>
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
