import { expect, test } from '@playwright/test'
import { actionLabel } from '../../src/lib/auditLabels'
import { DEFAULT_EXPIRY_DAYS, EXPIRY_DAYS, STATUS_MARK, assignableTokenRoles, environmentsText, expiryWords, lastUsedWords, utcStamp } from '../../src/lib/tokens'
import type { ApiToken } from '../../src/types'

// Pure checks: no browser, no network, no clock.

const now = Date.parse('2026-11-03T10:00:00Z')

test('who may give which role: up to admin, only admins and owners', () => {
  expect(assignableTokenRoles('owner')).toEqual(['viewer', 'editor', 'admin']) // never "owner"
  expect(assignableTokenRoles('admin')).toEqual(['viewer', 'editor', 'admin'])
  expect(assignableTokenRoles('editor')).toEqual([])
  expect(assignableTokenRoles('viewer')).toEqual([])
  expect(assignableTokenRoles(null)).toEqual([])
  expect(assignableTokenRoles(undefined)).toEqual([])
})

test('expiry choices never include "never" and stop at a year', () => {
  expect(Math.max(...EXPIRY_DAYS)).toBe(366)
  expect(Math.min(...EXPIRY_DAYS)).toBeGreaterThan(0)
  expect(EXPIRY_DAYS).toContain(DEFAULT_EXPIRY_DAYS)
  expect(DEFAULT_EXPIRY_DAYS).toBe(90)
})

test('expiry is worded honestly in both directions, and the exact time is always available in UTC', () => {
  const at = (ms: number) => new Date(now + ms).toISOString()
  const h = 3_600_000
  const d = 86_400_000
  expect(expiryWords(at(30 * 60_000), now)).toBe('in under an hour')
  expect(expiryWords(at(5 * h), now)).toBe('in 5 hours')
  expect(expiryWords(at(d + h), now)).toBe('in 1 day')
  expect(expiryWords(at(89 * d + 5 * h), now)).toBe('in 89 days')
  expect(expiryWords(at(-h), now)).toBe('expired today')
  expect(expiryWords(at(-d - h), now)).toBe('expired 1 day ago')
  expect(expiryWords(at(-3 * d), now)).toBe('expired 3 days ago')
  expect(utcStamp('2026-11-03T10:00:00Z')).toBe('2026-11-03 10:00 UTC')
  expect(utcStamp('2026-01-02T03:04:59+05:30')).toBe('2026-01-01 21:34 UTC')
})

test('last use never claims more precision than the server has (it is written at most once a minute)', () => {
  const ago = (ms: number) => new Date(now - ms).toISOString()
  expect(lastUsedWords(null, now)).toBe('Never used')
  expect(lastUsedWords(ago(10_000), now)).toBe('Within the last couple of minutes')
  expect(lastUsedWords(ago(90_000), now)).toBe('Within the last couple of minutes')
  expect(lastUsedWords(new Date(now + 5_000).toISOString(), now)).toBe('Within the last couple of minutes') // a little clock skew
  expect(lastUsedWords(ago(10 * 60_000), now)).toBe('10 minutes ago')
  expect(lastUsedWords(ago(5 * 3_600_000), now)).toBe('5 hours ago')
  expect(lastUsedWords(ago(86_400_000 + 1), now)).toBe('1 day ago')
  expect(lastUsedWords(ago(40 * 86_400_000), now)).toBe('40 days ago')
})

test('status is a word and a symbol, never colour alone', () => {
  for (const s of ['active', 'expired', 'revoked'] as const) {
    expect(STATUS_MARK[s].label.length).toBeGreaterThan(3)
    expect(STATUS_MARK[s].mark.length).toBe(1)
  }
  expect(new Set(Object.values(STATUS_MARK).map((s) => s.mark)).size).toBe(3)
})

test('environments: all, or listed', () => {
  const base: ApiToken = { id: 'i', name: 'n', prefix: 'hlt_00000000', role: 'viewer', environments: [], expiresAt: '', lastUsedAt: null, createdAt: '', createdBy: '', revokedAt: null, status: 'active' }
  expect(environmentsText(base)).toBe('All environments')
  expect(environmentsText({ ...base, environments: [{ id: '1', key: 'dev' }, { id: '2', key: 'staging' }] })).toBe('dev, staging')
})

test('token actions have audit labels', () => {
  expect(actionLabel('api_token.create')).toBe('API token created')
  expect(actionLabel('api_token.revoke')).toBe('API token revoked')
  expect(actionLabel('api_token.rejected')).toBe('API token refused')
})
