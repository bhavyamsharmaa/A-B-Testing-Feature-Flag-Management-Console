// Pure helpers for creating non-boolean flags. They mirror the backend's checks
// (flags/validate.go): 2-20 variations, non-empty unique ids, each value matching
// the flag type, and at most 4096 bytes of JSON per value. The order is
// permanent: the first variation is the default (fallthrough) and rollout
// ranges follow the array order.

export type VariationType = 'boolean' | 'string' | 'number' | 'json'
export type NonBooleanType = Exclude<VariationType, 'boolean'>

export const MIN_VARIATIONS = 2
export const MAX_VARIATIONS = 20
export const MAX_VALUE_BYTES = 4096

/** One editable row. `text` is what the user typed; it is parsed per the flag type on submit. */
export interface DraftVariation {
  rowKey: string
  id: string
  text: string
}

let counter = 0
export const newRowKey = () => `v${++counter}`

export function defaultRows(type: NonBooleanType): DraftVariation[] {
  const value = type === 'number' ? ['0', '1'] : type === 'json' ? ['{}', '{}'] : ['', '']
  return [
    { rowKey: newRowKey(), id: 'control', text: value[0] },
    { rowKey: newRowKey(), id: 'variant', text: value[1] },
  ]
}

/** Keeps ids, resets values: "5" the text is not 5 the number. */
export function rowsForType(type: NonBooleanType, rows: DraftVariation[]): DraftVariation[] {
  const blank = defaultRows(type)[0].text
  return rows.map((r) => ({ ...r, text: blank }))
}

/** Moves a row to the top: the first variation is the default and owns the first rollout range. */
export function makeDefault(rows: DraftVariation[], index: number): DraftVariation[] {
  if (index <= 0 || index >= rows.length) return rows
  return [rows[index], ...rows.slice(0, index), ...rows.slice(index + 1)]
}

/** Size of a value as the server measures it: Go's json.Marshal, which HTML-escapes < > & and U+2028/9. */
export function goJsonBytes(value: unknown): number {
  const json = JSON.stringify(value) ?? ''
  const escaped = json.replace(/[<>&\u2028\u2029]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'))
  return new TextEncoder().encode(escaped).length
}

export type ParsedValue = { ok: true; value: unknown } | { ok: false; error: string }

export function parseValue(type: NonBooleanType, text: string): ParsedValue {
  if (type === 'string') return { ok: true, value: text }
  if (type === 'number') {
    const t = text.trim()
    const n = Number(t)
    return t === '' || !Number.isFinite(n) ? { ok: false, error: 'Enter a number.' } : { ok: true, value: n }
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (e) {
    return { ok: false, error: `Not valid JSON: ${e instanceof Error ? e.message : 'parse error'}` }
  }
  if (typeof parsed !== 'object' || parsed === null) {
    return { ok: false, error: 'A JSON variation must be an object or an array.' }
  }
  return { ok: true, value: parsed }
}

export interface RowErrors {
  id: string | null
  value: string | null
}

export interface Validation {
  form: string | null
  rows: RowErrors[]
  ok: boolean
}

export function validateRows(type: NonBooleanType, rows: DraftVariation[]): Validation {
  const form =
    rows.length < MIN_VARIATIONS || rows.length > MAX_VARIATIONS
      ? `A flag needs ${MIN_VARIATIONS} to ${MAX_VARIATIONS} variations.`
      : null
  const seen = new Set<string>()
  const result = rows.map((r) => {
    const id = r.id.trim()
    let idError: string | null = null
    if (id === '') idError = 'Id is required.'
    else if (seen.has(id)) idError = 'Id must be unique.'
    seen.add(id)

    let valueError: string | null = null
    const parsed = parseValue(type, r.text)
    if (!parsed.ok) valueError = parsed.error
    else {
      const bytes = goJsonBytes(parsed.value)
      if (bytes > MAX_VALUE_BYTES) valueError = `Value is ${bytes} bytes; the limit is ${MAX_VALUE_BYTES}.`
    }
    return { id: idError, value: valueError }
  })
  const ok = form === null && result.every((r) => r.id === null && r.value === null)
  return { form, rows: result, ok }
}

/** The variations array as the backend expects it. Call only after validateRows says ok. */
export function toVariations(type: NonBooleanType, rows: DraftVariation[]): { id: string; value: unknown }[] {
  return rows.map((r) => {
    const parsed = parseValue(type, r.text)
    return { id: r.id.trim(), value: parsed.ok ? parsed.value : null }
  })
}

export interface ServerRowError {
  row: number
  field: 'id' | 'value'
  text: string
}

/** Go formats ids with %q; undo the quoting so the id can be matched to a row. */
function unquote(token: string): string {
  try {
    return JSON.parse(`"${token}"`) as string
  } catch {
    return token
  }
}

/** Attaches a backend 400 message to the row it is about, when it names one. */
export function mapServerError(message: string, rows: DraftVariation[]): ServerRowError | null {
  let m = /^variation (\d+): id is required$/.exec(message)
  if (m) return { row: Number(m[1]), field: 'id', text: message }

  m = /^variation id "((?:[^"\\]|\\.)*)" appears twice$/.exec(message)
  if (m) {
    const id = unquote(m[1])
    const matches = rows.map((r, i) => (r.id.trim() === id ? i : -1)).filter((i) => i >= 0)
    return matches.length > 1 ? { row: matches[1], field: 'id', text: message } : null
  }

  m = /^variation "((?:[^"\\]|\\.)*)" (is larger than|is not a)\b/.exec(message)
  if (m) {
    const id = unquote(m[1])
    const row = rows.findIndex((r) => r.id.trim() === id)
    return row >= 0 ? { row, field: 'value', text: message } : null
  }
  return null
}
