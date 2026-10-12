// Pure helpers for the segment member editor. A pasted list can be large and
// messy (a spreadsheet column, a CSV line, an export with blank lines), so
// nothing here trusts its input: size is capped before parsing, every key is
// checked against the server's rules (backend/internal/controlplane/segments),
// duplicates are folded, and a paste that would break the member limit is
// refused whole rather than half-applied.

/** Mirrors quota.MaxSegmentMembers. The server enforces it; this only explains it early. */
export const MAX_MEMBERS = 10_000
/** Mirrors segments.MaxSubjectKeyRunes (characters, not bytes). */
export const MAX_KEY_LENGTH = 256
/** A paste longer than this is refused unread: 10,000 keys of 256 characters is about 2.6 million. */
export const MAX_PASTE_CHARS = 3_000_000
/** How many problems are listed by name; the rest are counted. */
const MAX_REPORTED = 5

const SEPARATORS = /[\r\n\t,]+/
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/

export interface BadKey {
  value: string
  reason: string
}

export interface ParsedMembers {
  /** Distinct, valid keys in first-seen order. */
  keys: string[]
  /** Entries dropped because the same key appeared earlier in the paste. */
  duplicates: number
  /** The first few rejected entries, with why. */
  invalid: BadKey[]
  /** How many were rejected in all. */
  invalidCount: number
  /** The paste was over MAX_PASTE_CHARS and was not read. */
  tooLarge: boolean
}

const length = (s: string) => [...s].length

const preview = (s: string) => (length(s) > 24 ? `${[...s].slice(0, 24).join('')}…` : s)

/** Splits on new lines, tabs and commas; trims each entry; ignores blanks. */
export function parseMemberText(text: string): ParsedMembers {
  const out: ParsedMembers = { keys: [], duplicates: 0, invalid: [], invalidCount: 0, tooLarge: false }
  if (text.length > MAX_PASTE_CHARS) {
    out.tooLarge = true
    return out
  }
  const seen = new Set<string>()
  const reject = (value: string, reason: string) => {
    out.invalidCount++
    if (out.invalid.length < MAX_REPORTED) out.invalid.push({ value: preview(value), reason })
  }
  for (const raw of text.split(SEPARATORS)) {
    const key = raw.trim()
    if (key === '') continue
    if (length(key) > MAX_KEY_LENGTH) {
      reject(key, `longer than ${MAX_KEY_LENGTH} characters`)
    } else if (CONTROL.test(key)) {
      reject(key, 'contains a control character')
    } else if (seen.has(key)) {
      out.duplicates++
    } else {
      seen.add(key)
      out.keys.push(key)
    }
  }
  return out
}

export interface MergeResult {
  /** The member list after the paste (unchanged when `error` is set). */
  members: string[]
  added: number
  /** Pasted keys that were already members. */
  alreadyMembers: number
  error: string | null
}

const plural = (n: number, word: string) => `${n.toLocaleString('en-US')} ${word}${n === 1 ? '' : 's'}`

/** Adds parsed keys to the current members. All or nothing: over the limit, nothing is added. */
export function mergeMembers(current: string[], parsed: ParsedMembers, max = MAX_MEMBERS): MergeResult {
  const have = new Set(current)
  const fresh = parsed.keys.filter((k) => !have.has(k))
  const refuse = (error: string): MergeResult => ({ members: current, added: 0, alreadyMembers: parsed.keys.length - fresh.length, error })

  if (parsed.tooLarge) return refuse(`That paste is too large to read (over ${MAX_PASTE_CHARS.toLocaleString('en-US')} characters). Paste fewer keys at a time.`)
  if (parsed.invalidCount > 0) {
    const first = parsed.invalid.map((b) => `"${b.value}" is ${b.reason}`).join('; ')
    const more = parsed.invalidCount > parsed.invalid.length ? ` (and ${parsed.invalidCount - parsed.invalid.length} more)` : ''
    return refuse(`${plural(parsed.invalidCount, 'entry')} could not be used, so nothing was added: ${first}${more}.`)
  }
  if (current.length + fresh.length > max) {
    const over = current.length + fresh.length - max
    return refuse(
      `Adding ${plural(fresh.length, 'new key')} would give ${(current.length + fresh.length).toLocaleString('en-US')} members; a segment holds at most ${max.toLocaleString('en-US')}. Remove ${plural(over, 'member')} first, or paste fewer. Nothing was added.`,
    )
  }
  if (parsed.keys.length === 0) return { members: current, added: 0, alreadyMembers: 0, error: 'No keys found in what you pasted.' }
  return { members: [...current, ...fresh], added: fresh.length, alreadyMembers: parsed.keys.length - fresh.length, error: null }
}

/** One sentence about what a paste did, for a status line. */
export function describeMerge(r: MergeResult, parsed: ParsedMembers): string {
  const parts = [`Added ${plural(r.added, 'member')}`]
  const skipped = r.alreadyMembers + parsed.duplicates
  if (skipped > 0) parts.push(`${plural(skipped, 'duplicate')} ignored`)
  return `${parts.join('. ')}.`
}

/** Members matching a filter, case-insensitively, capped for rendering. */
export function filterMembers(members: string[], filter: string, limit: number): { shown: string[]; matches: number } {
  const f = filter.trim().toLowerCase()
  const shown: string[] = []
  let matches = 0
  for (const m of members) {
    if (f !== '' && !m.toLowerCase().includes(f)) continue
    matches++
    if (shown.length < limit) shown.push(m)
  }
  return { shown, matches }
}
