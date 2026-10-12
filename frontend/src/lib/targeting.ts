// Pure helpers for the targeting rule builder. They mirror the backend's rule
// shape and validation (evaluation/rules.go): clauses within a rule are ANDed,
// rules run top to bottom, `variationId` names the variation to serve, and a
// value's JSON type matters (the string "5" never equals the number 5).

import type { Clause, TargetingRule } from '../types'

export type Operator =
  | 'equals'
  | 'in'
  | 'contains'
  | 'greaterThan'
  | 'exists'
  | 'startsWith'
  | 'endsWith'
  | 'lessThan'
  | 'lessThanOrEqual'
  | 'greaterThanOrEqual'
  | 'semverEquals'
  | 'semverLessThan'
  | 'semverLessThanOrEqual'
  | 'semverGreaterThan'
  | 'semverGreaterThanOrEqual'
  | 'dateBefore'
  | 'dateAfter'
  | 'dateEquals'
  | 'matchesRegex'
  | 'inSegment'
  | 'notInSegment'
export type ValueType = 'text' | 'number' | 'boolean'

export interface OperatorInfo {
  value: Operator
  label: string
  group: 'Compare' | 'Text' | 'Number' | 'Version' | 'Date' | 'Audience'
}

export const OPERATORS: OperatorInfo[] = [
  { value: 'equals', label: 'equals', group: 'Compare' },
  { value: 'in', label: 'in (any of)', group: 'Compare' },
  { value: 'contains', label: 'contains', group: 'Compare' },
  { value: 'exists', label: 'exists', group: 'Compare' },
  { value: 'startsWith', label: 'starts with', group: 'Text' },
  { value: 'endsWith', label: 'ends with', group: 'Text' },
  { value: 'matchesRegex', label: 'matches regex', group: 'Text' },
  { value: 'greaterThan', label: 'greater than (number)', group: 'Number' },
  { value: 'greaterThanOrEqual', label: 'greater than or equal (number)', group: 'Number' },
  { value: 'lessThan', label: 'less than (number)', group: 'Number' },
  { value: 'lessThanOrEqual', label: 'less than or equal (number)', group: 'Number' },
  { value: 'semverEquals', label: 'version equals', group: 'Version' },
  { value: 'semverGreaterThan', label: 'version greater than', group: 'Version' },
  { value: 'semverGreaterThanOrEqual', label: 'version greater than or equal', group: 'Version' },
  { value: 'semverLessThan', label: 'version less than', group: 'Version' },
  { value: 'semverLessThanOrEqual', label: 'version less than or equal', group: 'Version' },
  { value: 'dateBefore', label: 'date before', group: 'Date' },
  { value: 'dateAfter', label: 'date after', group: 'Date' },
  { value: 'dateEquals', label: 'date equals', group: 'Date' },
  { value: 'inSegment', label: 'is in segment', group: 'Audience' },
  { value: 'notInSegment', label: 'is not in segment', group: 'Audience' },
]

export const VALUE_TYPES: { value: ValueType; label: string }[] = [
  { value: 'text', label: 'Text' },
  { value: 'number', label: 'Number' },
  { value: 'boolean', label: 'Boolean' },
]

const OPERATOR_SET = new Set<string>(OPERATORS.map((o) => o.value))

/** equals, in and contains compare typed scalars. */
export const usesValueType = (op: Operator) => op === 'equals' || op === 'in' || op === 'contains'

export const isSegmentOperator = (op: Operator) => op === 'inSegment' || op === 'notInSegment'

/** The shape of the value an operator takes, which decides the input shown. */
export type ValueKind = 'none' | 'scalar' | 'list' | 'number' | 'text' | 'semver' | 'date' | 'regex' | 'segment'

export function valueKind(op: Operator): ValueKind {
  switch (op) {
    case 'exists':
      return 'none'
    case 'equals':
    case 'contains':
      return 'scalar'
    case 'in':
      return 'list'
    case 'greaterThan':
    case 'greaterThanOrEqual':
    case 'lessThan':
    case 'lessThanOrEqual':
      return 'number'
    case 'startsWith':
    case 'endsWith':
      return 'text'
    case 'matchesRegex':
      return 'regex'
    case 'dateBefore':
    case 'dateAfter':
    case 'dateEquals':
      return 'date'
    case 'inSegment':
    case 'notInSegment':
      return 'segment'
    default:
      return 'semver'
  }
}

/** Negating "exists" would never match (a missing attribute matches nothing), and the segment operators come in both senses. */
export const canNegate = (op: Operator) => op !== 'exists' && !isSegmentOperator(op)

/** Mirrors evaluation.MaxRegexPatternBytes. */
export const MAX_REGEX_BYTES = 256

const SEMVER =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/
export const isSemver = (s: string) => s.length <= 128 && SEMVER.test(s)

const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/
/** RFC 3339 only: a date without a time and zone is not accepted (the server says no too). */
export const isRfc3339 = (s: string) => s.length <= 64 && RFC3339.test(s) && !Number.isNaN(Date.parse(s))

export interface DraftClause {
  id: string
  attribute: string
  operator: Operator
  valueType: ValueType
  /** The value for single-value operators, or the segment key for the segment operators. Booleans are "true" or "false". */
  text: string
  /** Invert the operator ("not"). Never set for exists or the segment operators. */
  negate: boolean
  /** The values for `in`. */
  items: string[]
}

export interface DraftRule {
  id: string
  clauses: DraftClause[]
  variationId: string
  /** A saved rule this builder can't represent (e.g. mixed-type `in`). Kept and re-sent unchanged. */
  locked: TargetingRule | null
}

let counter = 0
const uid = () => `d${++counter}`

export function newClause(): DraftClause {
  return { id: uid(), attribute: '', operator: 'equals', valueType: 'text', text: '', negate: false, items: [] }
}

export function newRule(variationId: string): DraftRule {
  return { id: uid(), clauses: [newClause()], variationId, locked: null }
}

function scalarType(v: unknown): ValueType | null {
  if (typeof v === 'string') return 'text'
  if (typeof v === 'number' && Number.isFinite(v)) return 'number'
  if (typeof v === 'boolean') return 'boolean'
  return null
}

const KNOWN_CLAUSE_FIELDS = new Set(['attribute', 'operator', 'value', 'negate'])

function clauseFromSaved(c: Clause): DraftClause | null {
  if (typeof c.attribute !== 'string' || !OPERATOR_SET.has(c.operator)) return null
  // A field this builder doesn't know would be dropped on save: keep the rule locked instead.
  if (Object.keys(c).some((k) => !KNOWN_CLAUSE_FIELDS.has(k))) return null
  if (c.negate !== undefined && typeof c.negate !== 'boolean') return null
  const operator = c.operator as Operator
  const negate = c.negate === true
  if (negate && !canNegate(operator)) return null
  const base = { id: uid(), attribute: c.attribute, operator, negate }
  switch (valueKind(operator)) {
    case 'none':
      return { ...base, valueType: 'text', text: '', items: [] }
    case 'number':
      return typeof c.value === 'number' && Number.isFinite(c.value)
        ? { ...base, valueType: 'number', text: String(c.value), items: [] }
        : null
    case 'list': {
      if (!Array.isArray(c.value) || c.value.length === 0) return null
      const types = new Set(c.value.map(scalarType))
      if (types.size !== 1 || types.has(null)) return null // mixed or non-scalar: not representable
      return { ...base, valueType: [...types][0] as ValueType, text: '', items: c.value.map(String) }
    }
    case 'scalar': {
      const t = scalarType(c.value)
      return t ? { ...base, valueType: t, text: String(c.value), items: [] } : null
    }
    default:
      // text, semver, date, regex and segment keys are all text
      return typeof c.value === 'string' ? { ...base, valueType: 'text', text: c.value, items: [] } : null
  }
}

export function fromSaved(rules: TargetingRule[]): DraftRule[] {
  return rules.map((rule) => {
    const clauses = rule.clauses.map(clauseFromSaved)
    if (clauses.length === 0 || clauses.includes(null)) {
      return { id: uid(), clauses: [], variationId: rule.variationId, locked: rule }
    }
    return { id: uid(), clauses: clauses as DraftClause[], variationId: rule.variationId, locked: null }
  })
}

/** Keeps the value sensible when the operator changes: what carries over does, what can't is cleared. */
export function changeOperator(c: DraftClause, operator: Operator): DraftClause {
  if (operator === c.operator) return c
  const from = valueKind(c.operator)
  const to = valueKind(operator)
  const negate = canNegate(operator) ? c.negate : false
  const attribute = isSegmentOperator(operator) ? '' : c.attribute
  const next = { ...c, operator, negate, attribute }
  // Moving between the segment operators keeps the chosen segment.
  if (to === 'segment') return { ...next, valueType: 'text', text: from === 'segment' ? c.text : '', items: [] }
  if (from === 'segment') return { ...next, valueType: 'text', text: '', items: [] }
  if (to === 'none') return { ...next, text: '', items: [] }
  if (to === 'number') {
    const first = from === 'list' ? (c.items[0] ?? '') : c.text
    return { ...next, valueType: 'number', text: parseNumber(first) === null ? '' : first, items: [] }
  }
  if (to === 'list') {
    const one = c.text.trim()
    return { ...next, items: one ? [one] : [], text: '' }
  }
  if (to === 'scalar') {
    const text = from === 'list' ? (c.items[0] ?? '') : c.text
    return { ...next, text, items: [] }
  }
  // text, semver, date, regex: free text; a number or true/false typed for another operator is kept as its text
  const text = from === 'list' ? (c.items[0] ?? '') : from === 'none' ? '' : c.text
  return { ...next, valueType: 'text', text, items: [] }
}

export function parseNumber(s: string): number | null {
  const t = s.trim()
  if (t === '') return null
  const n = Number(t)
  return Number.isFinite(n) ? n : null
}

function typed(type: ValueType, s: string): string | number | boolean {
  const t = s.trim()
  if (type === 'number') return Number(t)
  if (type === 'boolean') return t === 'true'
  return t
}

function clauseToPayload(c: DraftClause): Clause {
  const attribute = c.attribute.trim()
  const flag = c.negate && canNegate(c.operator) ? { negate: true } : {}
  switch (valueKind(c.operator)) {
    case 'none':
      return { attribute, operator: c.operator, ...flag }
    case 'segment':
      return { attribute: '', operator: c.operator, value: c.text.trim() }
    case 'number':
      return { attribute, operator: c.operator, value: Number(c.text.trim()), ...flag }
    case 'list':
      return { attribute, operator: c.operator, value: c.items.map((i) => typed(c.valueType, i)), ...flag }
    case 'scalar':
      return { attribute, operator: c.operator, value: typed(c.valueType, c.text), ...flag }
    case 'regex':
      return { attribute, operator: c.operator, value: c.text, ...flag } // a pattern is exact: no trimming
    default:
      return { attribute, operator: c.operator, value: c.text.trim(), ...flag }
  }
}

/** The rules array exactly as PATCH will send it. Locked rules go back untouched. */
export function toPayload(rules: DraftRule[]): TargetingRule[] {
  return rules.map((r) => r.locked ?? { clauses: r.clauses.map(clauseToPayload), variationId: r.variationId })
}

/** A key-order-independent string, to tell "changed" from "same rules". */
export function canon(rules: TargetingRule[]): string {
  return JSON.stringify(
    rules.map((r) => ({
      clauses: r.clauses.map((c) => {
        const out: Record<string, unknown> = { attribute: c.attribute, operator: c.operator }
        if (c.operator !== 'exists') out.value = c.value
        if (c.negate === true) out.negate = true
        return out
      }),
      variationId: r.variationId,
    })),
  )
}

export interface RuleErrors {
  rule: string | null
  clauses: (string | null)[]
}

/**
 * The same checks the server makes, so mistakes show before a request is sent.
 * `segmentKeys` is the workspace's segments when known (null while loading or
 * when the list failed: the server then has the last word).
 */
export function validateClause(c: DraftClause, segmentKeys: Set<string> | null = null): string | null {
  const kind = valueKind(c.operator)
  if (kind === 'segment') {
    if (c.text.trim() === '') return 'Choose a segment.'
    if (segmentKeys && !segmentKeys.has(c.text.trim())) return `Segment "${c.text.trim()}" does not exist in this workspace.`
    return null
  }
  if (c.attribute.trim() === '') return 'Attribute is required.'
  switch (kind) {
    case 'none':
      return null
    case 'number':
      return parseNumber(c.text) === null ? 'Enter a number.' : null
    case 'list':
      if (c.items.length === 0) return 'Add at least one value.'
      if (c.valueType === 'number' && c.items.some((i) => parseNumber(i) === null)) return 'Every value must be a number.'
      if (c.valueType === 'boolean' && c.items.some((i) => i !== 'true' && i !== 'false')) return 'Values must be true or false.'
      return null
    case 'scalar':
      if (c.valueType === 'boolean') return null
      if (c.valueType === 'number') return parseNumber(c.text) === null ? 'Enter a number.' : null
      return c.text.trim() === '' ? 'Value is required.' : null
    case 'text':
      return c.text.trim() === '' ? 'Value is required.' : null
    case 'semver':
      return isSemver(c.text.trim()) ? null : 'Enter a version like 1.4.0 or 2.0.0-rc.1 (no leading "v").'
    case 'date':
      return isRfc3339(c.text.trim()) ? null : 'Enter an RFC 3339 timestamp like 2024-05-01T00:00:00Z (date, time and zone).'
    case 'regex': {
      if (c.text === '') return 'Enter a regular expression.'
      if (new TextEncoder().encode(c.text).length > MAX_REGEX_BYTES) return `Regular expression is longer than ${MAX_REGEX_BYTES} bytes.`
      return null // JavaScript and Go (RE2) syntax differ: the server checks the pattern when you save
    }
  }
}

export function validate(rules: DraftRule[], variationIds: Set<string>, segmentKeys: Set<string> | null = null): RuleErrors[] {
  return rules.map((r) => {
    if (r.locked) return { rule: null, clauses: [] }
    const rule =
      r.clauses.length === 0
        ? 'Add at least one clause.'
        : !variationIds.has(r.variationId)
          ? 'Choose a variation to serve.'
          : null
    return { rule, clauses: r.clauses.map((c) => validateClause(c, segmentKeys)) }
  })
}

export const hasErrors = (errors: RuleErrors[]) => errors.some((e) => e.rule !== null || e.clauses.some((c) => c !== null))

export interface ServerRuleError {
  rule: number
  clause: number | null
  text: string
}

/** The backend prefixes rule errors: `targetingRules[2]: clause 0: attribute is required`. */
export function parseServerRuleError(message: string): ServerRuleError | null {
  const m = /^targetingRules\[(\d+)\]:\s*([\s\S]*)$/.exec(message)
  if (!m) return null
  const c = /^clause (\d+):\s*([\s\S]*)$/.exec(m[2])
  return c ? { rule: Number(m[1]), clause: Number(c[1]), text: c[2] } : { rule: Number(m[1]), clause: null, text: m[2] }
}

/** The clauses of a segment's rules as drafts, or null when one can't be shown in the builder (kept as-is until edited). */
export function clausesFromSaved(clauses: Clause[]): DraftClause[] | null {
  const drafts = clauses.map(clauseFromSaved)
  return drafts.includes(null) ? null : (drafts as DraftClause[])
}

export function clausesToPayload(drafts: DraftClause[]): Clause[] {
  return drafts.map(clauseToPayload)
}

/** `rules[2]: ...` from the segments API, as the clause it is about (null when it is about no clause in particular). */
export function parseServerSegmentRuleError(message: string): { clause: number | null; text: string } | null {
  const m = /^rules\[(\d+)\]:\s*([\s\S]*)$/.exec(message)
  return m ? { clause: Number(m[1]), text: m[2] } : null
}
