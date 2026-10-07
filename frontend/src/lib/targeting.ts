// Pure helpers for the targeting rule builder. They mirror the backend's rule
// shape and validation (evaluation/rules.go): clauses within a rule are ANDed,
// rules run top to bottom, `variationId` names the variation to serve, and a
// value's JSON type matters (the string "5" never equals the number 5).

import type { Clause, TargetingRule } from '../types'

export type Operator = 'equals' | 'in' | 'contains' | 'greaterThan' | 'exists'
export type ValueType = 'text' | 'number' | 'boolean'

export const OPERATORS: { value: Operator; label: string }[] = [
  { value: 'equals', label: 'equals' },
  { value: 'in', label: 'in (any of)' },
  { value: 'contains', label: 'contains' },
  { value: 'greaterThan', label: 'greaterThan (number)' },
  { value: 'exists', label: 'exists' },
]

export const VALUE_TYPES: { value: ValueType; label: string }[] = [
  { value: 'text', label: 'Text' },
  { value: 'number', label: 'Number' },
  { value: 'boolean', label: 'Boolean' },
]

const OPERATOR_SET = new Set<string>(OPERATORS.map((o) => o.value))

/** equals, in and contains compare typed scalars; greaterThan is always a number. */
export const usesValueType = (op: Operator) => op === 'equals' || op === 'in' || op === 'contains'

export interface DraftClause {
  id: string
  attribute: string
  operator: Operator
  valueType: ValueType
  /** The value for equals, contains and greaterThan. Booleans are "true" or "false". */
  text: string
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
  return { id: uid(), attribute: '', operator: 'equals', valueType: 'text', text: '', items: [] }
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

function clauseFromSaved(c: Clause): DraftClause | null {
  if (typeof c.attribute !== 'string' || !OPERATOR_SET.has(c.operator)) return null
  const base = { id: uid(), attribute: c.attribute, operator: c.operator as Operator }
  switch (base.operator) {
    case 'exists':
      return { ...base, valueType: 'text', text: '', items: [] }
    case 'greaterThan':
      return typeof c.value === 'number' && Number.isFinite(c.value)
        ? { ...base, valueType: 'number', text: String(c.value), items: [] }
        : null
    case 'in': {
      if (!Array.isArray(c.value) || c.value.length === 0) return null
      const types = new Set(c.value.map(scalarType))
      if (types.size !== 1 || types.has(null)) return null // mixed or non-scalar: not representable
      return { ...base, valueType: [...types][0] as ValueType, text: '', items: c.value.map(String) }
    }
    default: {
      const t = scalarType(c.value)
      return t ? { ...base, valueType: t, text: String(c.value), items: [] } : null
    }
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

/** Keeps the value sensible when the operator changes. */
export function changeOperator(c: DraftClause, operator: Operator): DraftClause {
  if (operator === c.operator) return c
  if (operator === 'exists') return { ...c, operator, text: '', items: [] }
  if (operator === 'greaterThan') {
    const first = c.operator === 'in' ? (c.items[0] ?? '') : c.text
    return { ...c, operator, valueType: 'number', text: parseNumber(first) === null ? '' : first, items: [] }
  }
  if (operator === 'in') {
    const one = c.text.trim()
    return { ...c, operator, items: c.operator === 'in' ? c.items : one ? [one] : [], text: '' }
  }
  // equals or contains
  const text = c.operator === 'in' ? (c.items[0] ?? '') : c.text
  return { ...c, operator, text, items: [] }
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
  switch (c.operator) {
    case 'exists':
      return { attribute, operator: 'exists' }
    case 'greaterThan':
      return { attribute, operator: 'greaterThan', value: Number(c.text.trim()) }
    case 'in':
      return { attribute, operator: 'in', value: c.items.map((i) => typed(c.valueType, i)) }
    default:
      return { attribute, operator: c.operator, value: typed(c.valueType, c.text) }
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
      clauses: r.clauses.map((c) =>
        c.operator === 'exists'
          ? { attribute: c.attribute, operator: c.operator }
          : { attribute: c.attribute, operator: c.operator, value: c.value },
      ),
      variationId: r.variationId,
    })),
  )
}

export interface RuleErrors {
  rule: string | null
  clauses: (string | null)[]
}

/** The same checks the server makes, so mistakes show before a request is sent. */
export function validate(rules: DraftRule[], variationIds: Set<string>): RuleErrors[] {
  return rules.map((r) => {
    if (r.locked) return { rule: null, clauses: [] }
    const rule =
      r.clauses.length === 0
        ? 'Add at least one clause.'
        : !variationIds.has(r.variationId)
          ? 'Choose a variation to serve.'
          : null
    const clauses = r.clauses.map((c) => {
      if (c.attribute.trim() === '') return 'Attribute is required.'
      if (c.operator === 'exists') return null
      if (c.operator === 'greaterThan') return parseNumber(c.text) === null ? 'Enter a number.' : null
      if (c.operator === 'in') {
        if (c.items.length === 0) return 'Add at least one value.'
        if (c.valueType === 'number' && c.items.some((i) => parseNumber(i) === null)) return 'Every value must be a number.'
        if (c.valueType === 'boolean' && c.items.some((i) => i !== 'true' && i !== 'false')) return 'Values must be true or false.'
        return null
      }
      if (c.valueType === 'boolean') return null
      if (c.valueType === 'number') return parseNumber(c.text) === null ? 'Enter a number.' : null
      return c.text.trim() === '' ? 'Value is required.' : null
    })
    return { rule, clauses }
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
