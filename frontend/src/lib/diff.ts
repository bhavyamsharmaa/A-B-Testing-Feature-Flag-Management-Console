// Pure helpers for showing an audit entry's before/after as a list of changed
// fields. Values are only ever turned into strings; nothing here produces HTML.

export interface FieldChange {
  field: string
  kind: 'added' | 'removed' | 'changed'
  before: string
  after: string
}

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

/** Flattens nested objects into dotted paths (rollout.on). Arrays and scalars are leaves. */
function flatten(value: unknown, prefix: string, out: Map<string, unknown>) {
  if (isPlainObject(value) && Object.keys(value).length > 0) {
    for (const [k, v] of Object.entries(value)) flatten(v, prefix ? `${prefix}.${k}` : k, out)
    return
  }
  out.set(prefix || '(value)', value)
}

export function formatValue(v: unknown): string {
  if (typeof v === 'string') return v
  if (v === undefined) return ''
  return JSON.stringify(v) ?? String(v)
}

/** Fields that differ between two snapshots. A null snapshot means "did not exist". */
export function changedFields(before: unknown, after: unknown): FieldChange[] {
  const b = new Map<string, unknown>()
  const a = new Map<string, unknown>()
  if (before !== null && before !== undefined) flatten(before, '', b)
  if (after !== null && after !== undefined) flatten(after, '', a)

  const fields = [...b.keys(), ...[...a.keys()].filter((k) => !b.has(k))]
  const changes: FieldChange[] = []
  for (const field of fields) {
    const inBefore = b.has(field)
    const inAfter = a.has(field)
    if (inBefore && inAfter && JSON.stringify(b.get(field)) === JSON.stringify(a.get(field))) continue
    changes.push({
      field,
      kind: inBefore && inAfter ? 'changed' : inAfter ? 'added' : 'removed',
      before: inBefore ? formatValue(b.get(field)) : '',
      after: inAfter ? formatValue(a.get(field)) : '',
    })
  }
  return changes
}
