// Pure helpers for scheduling: turning "this wall-clock time in that time zone"
// into an exact UTC instant (and saying so when the wall-clock time is
// impossible or ambiguous), formatting instants for people, and the role a
// kind of change needs. Nothing here reads the clock or the browser's zone
// unless it is handed them, so it can be tested.

import type { Role } from '../types'

export type ChangeKind = 'enable' | 'disable' | 'kill' | 'patch'

export const KIND_LABEL: Record<ChangeKind, string> = {
  enable: 'Enable the flag',
  disable: 'Disable the flag',
  kill: 'Kill the flag (disable at once)',
  patch: 'Change the rollout or fallthrough',
}

/** Short name of a kind, for lists. */
export const KIND_SHORT: Record<ChangeKind, string> = { enable: 'Enable', disable: 'Disable', kill: 'Kill', patch: 'Change configuration' }

/** Mirrors scheduling.MinLead and scheduling.MaxHorizon. The server enforces them; this explains them early. */
export const MIN_LEAD_MS = 60_000
export const MAX_HORIZON_MS = 366 * 24 * 3600 * 1000

const RANK: Record<Role, number> = { viewer: 1, editor: 2, approver: 3, admin: 4 }

/** The role a change needs in an environment: the one for making it now (kill: editor everywhere; the rest: an approver in production). */
export function requiredRole(kind: ChangeKind, production: boolean): Role {
  if (kind === 'kill') return 'editor'
  return production ? 'approver' : 'editor'
}

export const roleAllows = (role: Role | null, need: Role) => role !== null && RANK[role] >= RANK[need]

/** Why a role cannot schedule a kind of change here, or null if it can. */
export function scheduleBlockedReason(role: Role | null, kind: ChangeKind, production: boolean, envName: string): string | null {
  const need = requiredRole(kind, production)
  if (roleAllows(role, need)) return null
  if (role === 'viewer' || role === null) return 'Viewers have read-only access'
  return `Scheduling this in ${envName} needs the ${need} role, the same as making the change now`
}

// ---- time zones -----------------------------------------------------------------

export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** Every IANA zone the browser knows, plus UTC first. */
export function allZones(): string[] {
  let zones: string[] = []
  try {
    zones = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    zones = []
  }
  return ['UTC', ...zones.filter((z) => z !== 'UTC')]
}

interface Wall {
  y: number
  mo: number
  d: number
  h: number
  mi: number
}

const formatterCache = new Map<string, Intl.DateTimeFormat>()

function formatter(zone: string): Intl.DateTimeFormat {
  let f = formatterCache.get(zone)
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', {
      timeZone: zone, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
    })
    formatterCache.set(zone, f)
  }
  return f
}

/** The wall-clock reading of an instant in a zone. */
function wallAt(ms: number, zone: string): Wall & { s: number } {
  const p: Record<string, number> = {}
  for (const part of formatter(zone).formatToParts(new Date(ms))) {
    if (part.type !== 'literal') p[part.type] = Number(part.value)
  }
  return { y: p.year, mo: p.month, d: p.day, h: p.hour === 24 ? 0 : p.hour, mi: p.minute, s: p.second }
}

/** The zone's offset from UTC at an instant, in minutes (positive east of Greenwich). */
export function offsetMinutes(ms: number, zone: string): number {
  const w = wallAt(ms, zone)
  return Math.round((Date.UTC(w.y, w.mo - 1, w.d, w.h, w.mi, w.s) - Math.floor(ms / 1000) * 1000) / 60000)
}

export type Zoned =
  | { kind: 'ok'; utc: Date; ambiguous: boolean; /** the other reading, when ambiguous */ other?: Date }
  | { kind: 'gap' } // the time does not exist in that zone (clocks jumped over it)
  | { kind: 'invalid' }

/**
 * Turns "2026-11-01T01:30" in "America/New_York" into an instant.
 *  - normal time: one instant;
 *  - the repeated hour when clocks go back: TWO instants. The earlier is returned, `ambiguous` is set and
 *    the later is in `other`, so the caller can tell the person;
 *  - the skipped hour when clocks go forward: no instant ('gap').
 */
export function zonedToUtc(local: string, zone: string): Zoned {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(local)
  if (!m) return { kind: 'invalid' }
  const [y, mo, d, h, mi] = m.slice(1).map(Number)
  const asUtc = Date.UTC(y, mo - 1, d, h, mi)
  const check = new Date(asUtc)
  // 2026-02-30 is not a date
  if (check.getUTCFullYear() !== y || check.getUTCMonth() !== mo - 1 || check.getUTCDate() !== d || h > 23 || mi > 59) return { kind: 'invalid' }
  try {
    formatter(zone)
  } catch {
    return { kind: 'invalid' }
  }
  // The zone's offset a day either side of the wall time covers any transition near it.
  const offsets = new Set([offsetMinutes(asUtc - 86_400_000, zone), offsetMinutes(asUtc + 86_400_000, zone)])
  const hits: number[] = []
  for (const o of offsets) {
    const t = asUtc - o * 60_000
    const w = wallAt(t, zone)
    if (w.y === y && w.mo === mo && w.d === d && w.h === h && w.mi === mi && !hits.includes(t)) hits.push(t)
  }
  hits.sort((a, b) => a - b)
  if (hits.length === 0) return { kind: 'gap' }
  if (hits.length === 1) return { kind: 'ok', utc: new Date(hits[0]), ambiguous: false }
  return { kind: 'ok', utc: new Date(hits[0]), ambiguous: true, other: new Date(hits[1]) }
}

const pad = (n: number) => String(n).padStart(2, '0')

/** "2026-11-03 10:00 UTC" */
export function formatUtc(d: Date | string): string {
  const t = typeof d === 'string' ? new Date(d) : d
  return `${t.getUTCFullYear()}-${pad(t.getUTCMonth() + 1)}-${pad(t.getUTCDate())} ${pad(t.getUTCHours())}:${pad(t.getUTCMinutes())} UTC`
}

/** "2026-11-03 15:30 GMT+5:30 (Asia/Kolkata)" — the instant as a wall-clock reading in a zone, with the zone named. */
export function formatInZone(d: Date | string, zone: string): string {
  const t = typeof d === 'string' ? new Date(d) : d
  const w = wallAt(t.getTime(), zone)
  let abbr = ''
  try {
    abbr = new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: 'short' }).formatToParts(t).find((p) => p.type === 'timeZoneName')?.value ?? ''
  } catch {
    /* unknown zone: leave it out */
  }
  return `${w.y}-${pad(w.mo)}-${pad(w.d)} ${pad(w.h)}:${pad(w.mi)}${abbr ? ` ${abbr}` : ''} (${zone})`
}

export interface RunTimeCheck {
  utc: Date | null
  /** Shown under the fields: what will happen, or what is wrong. */
  line: string
  /** Blocks scheduling. */
  error: string | null
  /** Said, not blocked: an ambiguous time was resolved to the earlier one. */
  note: string | null
}

/**
 * Everything the dialog says about the chosen date, time and zone: the exact UTC instant, or why it cannot be scheduled.
 * `now` is passed in, so the same input always gives the same answer.
 */
export function checkRunTime(local: string, zone: string, now: Date): RunTimeCheck {
  if (local.trim() === '') return { utc: null, line: 'Choose a date and a time.', error: 'Choose a date and a time.', note: null }
  const z = zonedToUtc(local, zone)
  if (z.kind === 'invalid') return { utc: null, line: 'That is not a valid date and time.', error: 'That is not a valid date and time.', note: null }
  if (z.kind === 'gap') {
    const msg = `${local.replace('T', ' ')} does not exist in ${zone}: the clocks skip it when they go forward. Choose a time before or after.`
    return { utc: null, line: msg, error: msg, note: null }
  }
  const lead = z.utc.getTime() - now.getTime()
  let error: string | null = null
  if (lead < MIN_LEAD_MS) error = 'Choose a time at least one minute from now.'
  else if (lead > MAX_HORIZON_MS) error = 'Choose a time within a year from now.'
  const line = `This runs at ${formatUtc(z.utc)}.`
  const note = z.ambiguous && z.other
    ? `${local.replace('T', ' ')} happens twice in ${zone} (the clocks go back). It is read as the first one, ${formatUtc(z.utc)}; the second would be ${formatUtc(z.other)}.`
    : null
  return { utc: z.utc, line, error, note }
}

/** A date and time input pair's default: the next whole hour, at least an hour out, as "YYYY-MM-DDTHH:mm" in a zone. */
export function defaultLocalTime(now: Date, zone: string): string {
  const t = new Date(Math.ceil((now.getTime() + 3_600_000) / 3_600_000) * 3_600_000)
  const w = wallAt(t.getTime(), zone)
  return `${w.y}-${pad(w.mo)}-${pad(w.d)}T${pad(w.h)}:${pad(w.mi)}`
}
