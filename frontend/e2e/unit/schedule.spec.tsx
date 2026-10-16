import { expect, test } from '@playwright/test'
import {
  KIND_SHORT,
  MAX_HORIZON_MS,
  MIN_LEAD_MS,
  allZones,
  checkRunTime,
  defaultLocalTime,
  formatInZone,
  formatUtc,
  offsetMinutes,
  requiredRole,
  roleAllows,
  scheduleBlockedReason,
  zonedToUtc,
  type ChangeKind,
} from '../../src/lib/schedule'
import { actionLabel } from '../../src/lib/auditLabels'

// Pure checks: no browser, no network, no clock, no sleeping.

const iso = (d: Date) => d.toISOString().replace('.000Z', 'Z')
const ok = (local: string, zone: string) => {
  const z = zonedToUtc(local, zone)
  if (z.kind !== 'ok') throw new Error(`${local} in ${zone} is ${z.kind}`)
  return z
}

test.describe('wall-clock time in a zone to an exact UTC instant', () => {
  test('fixed-offset and UTC zones', () => {
    expect(iso(ok('2026-11-03T15:30', 'Asia/Kolkata').utc)).toBe('2026-11-03T10:00:00Z')
    expect(iso(ok('2026-11-03T10:00', 'UTC').utc)).toBe('2026-11-03T10:00:00Z')
    expect(iso(ok('2026-07-01T09:00', 'Asia/Tokyo').utc)).toBe('2026-07-01T00:00:00Z')
    expect(iso(ok('2026-01-15T00:00', 'Asia/Kathmandu').utc)).toBe('2026-01-14T18:15:00Z') // +5:45
  })

  test('summer and winter in the same zone differ by the DST hour', () => {
    expect(iso(ok('2026-07-01T12:00', 'America/New_York').utc)).toBe('2026-07-01T16:00:00Z') // EDT, -4
    expect(iso(ok('2026-01-15T12:00', 'America/New_York').utc)).toBe('2026-01-15T17:00:00Z') // EST, -5
    expect(iso(ok('2026-07-01T12:00', 'Europe/London').utc)).toBe('2026-07-01T11:00:00Z') // BST
    expect(iso(ok('2026-01-15T12:00', 'Europe/London').utc)).toBe('2026-01-15T12:00:00Z')
  })

  test('the hour skipped when clocks go forward does not exist', () => {
    expect(zonedToUtc('2026-03-08T02:30', 'America/New_York').kind).toBe('gap')
    expect(zonedToUtc('2026-03-08T02:00', 'America/New_York').kind).toBe('gap')
    expect(zonedToUtc('2026-03-29T01:30', 'Europe/London').kind).toBe('gap')
    expect(zonedToUtc('2026-10-04T02:30', 'Australia/Sydney').kind).toBe('gap') // southern hemisphere
    expect(zonedToUtc('2026-10-04T02:15', 'Australia/Lord_Howe').kind).toBe('gap') // a 30-minute shift
    // either side of it is fine, and unambiguous
    const before = ok('2026-03-08T01:59', 'America/New_York')
    const after = ok('2026-03-08T03:00', 'America/New_York')
    expect(iso(before.utc)).toBe('2026-03-08T06:59:00Z') // still EST
    expect(iso(after.utc)).toBe('2026-03-08T07:00:00Z') // EDT: one real minute later
    expect(before.ambiguous || after.ambiguous).toBe(false)
    expect(ok('2026-10-04T02:45', 'Australia/Lord_Howe').ambiguous).toBe(false)
  })

  test('the hour repeated when clocks go back has two readings; the earlier is used and the later is reported', () => {
    const z = ok('2026-11-01T01:30', 'America/New_York')
    expect(z.ambiguous).toBe(true)
    expect(iso(z.utc)).toBe('2026-11-01T05:30:00Z') // the EDT reading
    expect(iso(z.other!)).toBe('2026-11-01T06:30:00Z') // the EST reading, an hour later
    const lon = ok('2026-10-25T01:30', 'Europe/London')
    expect(lon.ambiguous).toBe(true)
    expect(iso(lon.utc)).toBe('2026-10-25T00:30:00Z')
    expect(iso(lon.other!)).toBe('2026-10-25T01:30:00Z')
    expect(ok('2026-04-05T02:30', 'Australia/Sydney').ambiguous).toBe(true)
    // the hour before and after are not ambiguous
    expect(ok('2026-11-01T00:59', 'America/New_York').ambiguous).toBe(false)
    expect(ok('2026-11-01T02:00', 'America/New_York').ambiguous).toBe(false)
  })

  test('things that are not a date, a time or a zone', () => {
    for (const bad of ['', '2026-02-30T10:00', '2026-13-01T10:00', '2026-11-03T25:00', '2026-11-03T10:60', '2026-11-03', '2026-11-03 10:00', 'tomorrow']) {
      expect(zonedToUtc(bad, 'UTC').kind, bad).toBe('invalid')
    }
    expect(zonedToUtc('2026-11-03T10:00', 'Mars/Olympus_Mons').kind).toBe('invalid')
    expect(zonedToUtc('2026-02-29T10:00', 'UTC').kind).toBe('invalid') // 2026 is not a leap year
    expect(zonedToUtc('2028-02-29T10:00', 'UTC').kind).toBe('ok')
  })

  test('every instant read back in its zone gives the wall time that produced it (a round trip, across zones and seasons)', () => {
    const zones = ['UTC', 'America/New_York', 'Europe/London', 'Asia/Kolkata', 'Australia/Lord_Howe', 'Pacific/Auckland', 'America/St_Johns', 'Africa/Casablanca', 'Pacific/Apia']
    let checked = 0
    for (const zone of zones) {
      for (let day = 0; day < 365; day += 7) {
        for (const hour of [0, 1, 2, 3, 12, 23]) {
          const ms = Date.UTC(2026, 0, 1 + day, hour, 0)
          const w = formatInZone(new Date(ms), zone).slice(0, 16).replace(' ', 'T')
          const z = zonedToUtc(w, zone)
          expect(z.kind, `${w} ${zone}`).toBe('ok')
          if (z.kind === 'ok') {
            const back = z.utc.getTime() === ms || (z.ambiguous && z.other?.getTime() === ms)
            expect(back, `${w} in ${zone}`).toBe(true)
          }
          checked++
        }
      }
    }
    expect(checked).toBeGreaterThan(2000)
  })

  test('offsets', () => {
    expect(offsetMinutes(Date.UTC(2026, 6, 1), 'America/New_York')).toBe(-240)
    expect(offsetMinutes(Date.UTC(2026, 0, 1), 'America/New_York')).toBe(-300)
    expect(offsetMinutes(Date.UTC(2026, 0, 1), 'Asia/Kolkata')).toBe(330)
    expect(offsetMinutes(Date.UTC(2026, 0, 1), 'UTC')).toBe(0)
  })
})

test.describe('showing an instant', () => {
  test('UTC is explicit and fixed', () => {
    expect(formatUtc(new Date('2026-11-03T10:00:00Z'))).toBe('2026-11-03 10:00 UTC')
    expect(formatUtc('2026-01-02T03:04:59Z')).toBe('2026-01-02 03:04 UTC')
    expect(formatUtc(new Date('2026-11-03T04:00:00-06:00'))).toBe('2026-11-03 10:00 UTC')
  })

  test('a zone is named, with its abbreviation, and follows daylight saving', () => {
    expect(formatInZone('2026-11-03T10:00:00Z', 'Asia/Kolkata')).toMatch(/^2026-11-03 15:30 .*\(Asia\/Kolkata\)$/)
    expect(formatInZone('2026-07-01T16:00:00Z', 'America/New_York')).toMatch(/^2026-07-01 12:00 EDT \(America\/New_York\)$/)
    expect(formatInZone('2026-01-15T17:00:00Z', 'America/New_York')).toMatch(/^2026-01-15 12:00 EST \(America\/New_York\)$/)
    expect(formatInZone('2026-11-01T05:30:00Z', 'America/New_York')).toContain('01:30 EDT')
    expect(formatInZone('2026-11-01T06:30:00Z', 'America/New_York')).toContain('01:30 EST') // the same wall time, an hour later, told apart
    expect(formatInZone('2026-11-03T10:00:00Z', 'UTC')).toContain('10:00')
    // midnight is 00:00, never 24:00
    expect(formatInZone('2026-11-03T00:00:00Z', 'UTC')).toMatch(/^2026-11-03 00:00/)
  })

  test('the zone list starts with UTC and has no repeats', () => {
    const zones = allZones()
    expect(zones[0]).toBe('UTC')
    expect(new Set(zones).size).toBe(zones.length)
    expect(zones).toContain('America/New_York')
  })
})

test.describe('what the dialog says about the chosen time', () => {
  const now = new Date('2026-11-03T10:00:00Z')

  test('a good time: one line, in UTC', () => {
    const c = checkRunTime('2026-11-04T15:30', 'Asia/Kolkata', now)
    expect(c.error).toBeNull()
    expect(c.note).toBeNull()
    expect(c.line).toBe('This runs at 2026-11-04 10:00 UTC.')
    expect(iso(c.utc!)).toBe('2026-11-04T10:00:00Z')
  })

  test('the minimum lead and the maximum horizon are the same numbers the server uses', () => {
    expect(MIN_LEAD_MS).toBe(60_000)
    expect(MAX_HORIZON_MS).toBe(366 * 86_400_000)
    expect(checkRunTime('2026-11-03T10:00', 'UTC', now).error).toMatch(/at least one minute/)
    expect(checkRunTime('2026-11-03T10:01', 'UTC', now).error).toBeNull() // exactly a minute is allowed
    expect(checkRunTime('2026-11-03T09:00', 'UTC', now).error).toMatch(/at least one minute/) // the past
    expect(checkRunTime('2027-11-04T10:01', 'UTC', now).error).toMatch(/within a year/) // a minute past 366 days
    expect(checkRunTime('2027-11-04T10:00', 'UTC', now).error).toBeNull() // exactly 366 days is allowed
    expect(checkRunTime('2027-11-03T10:00', 'UTC', now).error).toBeNull()
  })

  test('a time that does not exist is refused with the reason, one that happens twice is allowed and explained', () => {
    const gap = checkRunTime('2027-03-14T02:30', 'America/New_York', new Date('2027-03-01T00:00:00Z'))
    expect(gap.utc).toBeNull()
    expect(gap.error).toMatch(/does not exist in America\/New_York/)
    const twice = checkRunTime('2026-11-01T01:30', 'America/New_York', new Date('2026-10-01T00:00:00Z'))
    expect(twice.error).toBeNull()
    expect(twice.note).toMatch(/happens twice in America\/New_York/)
    expect(twice.note).toContain('2026-11-01 05:30 UTC')
    expect(twice.note).toContain('2026-11-01 06:30 UTC')
    expect(twice.line).toBe('This runs at 2026-11-01 05:30 UTC.')
  })

  test('empty and invalid input', () => {
    expect(checkRunTime('', 'UTC', now).error).toBe('Choose a date and a time.')
    expect(checkRunTime('2026-02-30T10:00', 'UTC', now).error).toMatch(/not a valid/)
    expect(checkRunTime('2026-11-04T10:00', 'Nowhere/Land', now).error).toMatch(/not a valid/)
  })

  test('the default is on the hour, at least an hour ahead, in the chosen zone', () => {
    expect(defaultLocalTime(new Date('2026-11-03T10:20:00Z'), 'UTC')).toBe('2026-11-03T12:00')
    expect(defaultLocalTime(new Date('2026-11-03T10:00:00Z'), 'UTC')).toBe('2026-11-03T11:00')
    expect(defaultLocalTime(new Date('2026-11-03T10:00:00Z'), 'Asia/Kolkata')).toBe('2026-11-03T16:30')
    const d = defaultLocalTime(new Date('2026-11-03T10:00:00Z'), 'Asia/Kolkata')
    expect(zonedToUtc(d, 'Asia/Kolkata').kind).toBe('ok')
    expect(checkRunTime(d, 'Asia/Kolkata', new Date('2026-11-03T10:00:00Z')).error).toBeNull()
  })
})

test.describe('roles', () => {
  test('a kind of change needs the role of making it now', () => {
    const kinds: ChangeKind[] = ['enable', 'disable', 'kill', 'patch']
    for (const k of kinds) {
      expect(requiredRole(k, false)).toBe('editor')
      expect(requiredRole(k, true)).toBe(k === 'kill' ? 'editor' : 'approver')
    }
    expect(roleAllows('admin', 'approver')).toBe(true)
    expect(roleAllows('approver', 'approver')).toBe(true)
    expect(roleAllows('editor', 'approver')).toBe(false)
    expect(roleAllows('viewer', 'editor')).toBe(false)
    expect(roleAllows(null, 'viewer')).toBe(false)
  })

  test('the reason an unauthorised role is given', () => {
    expect(scheduleBlockedReason('editor', 'enable', true, 'production')).toMatch(/needs the approver role/)
    expect(scheduleBlockedReason('editor', 'kill', true, 'production')).toBeNull()
    expect(scheduleBlockedReason('editor', 'enable', false, 'dev')).toBeNull()
    expect(scheduleBlockedReason('viewer', 'enable', false, 'dev')).toBe('Viewers have read-only access')
    expect(scheduleBlockedReason('admin', 'patch', true, 'production')).toBeNull()
    expect(KIND_SHORT.patch).toBe('Change configuration')
  })

  test('scheduled changes have audit labels', () => {
    expect(actionLabel('scheduled_change.create')).toBe('Change scheduled')
    expect(actionLabel('scheduled_change.succeeded')).toBe('Scheduled change applied')
    expect(actionLabel('scheduled_change.missed')).toBe('Scheduled change missed')
    expect(actionLabel('scheduled_change.failed')).toBe('Scheduled change failed')
    expect(actionLabel('scheduled_change.cancel')).toBe('Scheduled change cancelled')
    expect(actionLabel('scheduled_change.retry')).toBe('Scheduled change will be retried')
  })
})
