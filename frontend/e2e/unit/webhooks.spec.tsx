import { expect, test } from '@playwright/test'
import { actionLabel } from '../../src/lib/auditLabels'
import { DELIVERY_MARK, EMAIL_MARK, ENDPOINT_MARK, EVENT_LABEL, canResend, durationText, environmentText, eventsText, healthLine, nextTryText, urlProblem } from '../../src/lib/webhooks'
import type { WebhookDelivery, WebhookEndpoint, WebhookEventType } from '../../src/types'

// Pure checks: no browser, no network, no clock.

const ALL = Object.keys(EVENT_LABEL) as WebhookEventType[]

const endpoint = (over: Partial<WebhookEndpoint> = {}): WebhookEndpoint => ({
  id: 'e1', url: 'https://example.com/h', description: '', eventTypes: ALL, environment: null, enabled: true, secretHint: '…abcd', previousSecretExpiresAt: null,
  health: { consecutiveFailures: 0, failingSince: null, lastSuccessAt: null, lastFailureAt: null, disabledReason: null, disabledAt: null },
  createdAt: '2026-11-03T10:00:00Z', status: 'idle', ...over,
})

const delivery = (over: Partial<WebhookDelivery> = {}): WebhookDelivery => ({
  id: 'd1', eventId: 'v1', eventType: 'flag.updated', status: 'delivered', attempts: 1, httpStatus: 200, durationMs: 12, error: null, responseHint: null,
  createdAt: '2026-11-03T10:00:00Z', lastAttemptAt: '2026-11-03T10:00:01Z', nextAttemptAt: null, deliveredAt: '2026-11-03T10:00:01Z', ...over,
})

test('there is a label for each of the nine events', () => {
  expect(ALL).toHaveLength(9)
  for (const t of ALL) expect(EVENT_LABEL[t].length).toBeGreaterThan(3)
})

test('every state has a word AND a symbol: nothing is colour only', () => {
  for (const m of [...Object.values(ENDPOINT_MARK), ...Object.values(DELIVERY_MARK), ...Object.values(EMAIL_MARK)]) {
    expect(m.label.length).toBeGreaterThan(2)
    expect(m.mark.length).toBeGreaterThan(0)
  }
  // distinct words, so two states never read the same
  const words = Object.values(DELIVERY_MARK).map((m) => m.label)
  expect(new Set(words).size).toBe(words.length)
  expect(DELIVERY_MARK.failed.label).toMatch(/retry/i)
  expect(DELIVERY_MARK.dead.label).toMatch(/gave up/i)
})

test('only a failed delivery can be resent', () => {
  expect(canResend(delivery({ status: 'failed' }))).toBe(true)
  expect(canResend(delivery({ status: 'dead' }))).toBe(true)
  for (const s of ['delivered', 'pending', 'delivering'] as const) expect(canResend(delivery({ status: s }))).toBe(false)
})

test('events and environments read plainly', () => {
  expect(eventsText(ALL, 9)).toBe('All events')
  expect(eventsText(['flag.killed'], 9)).toBe('Flag killed')
  expect(eventsText(['flag.killed', 'flag.created'], 9)).toBe('Flag killed, Flag created')
  expect(eventsText(['flag.killed', 'flag.created', 'flag.updated'], 9)).toBe('3 events')
  expect(environmentText(endpoint())).toBe('All environments')
  expect(environmentText(endpoint({ environment: { id: 'x', key: 'production' } }))).toBe('production')
})

test('durations and health lines are honest', () => {
  expect(durationText(null)).toBe('–')
  expect(durationText(12)).toBe('12 ms')
  expect(durationText(1450)).toBe('1.4 s')
  expect(healthLine(endpoint())).toBe('Nothing delivered yet')
  expect(healthLine(endpoint({ health: { ...endpoint().health, lastSuccessAt: '2026-11-03T10:00:00Z' } }))).toBe('Last delivered 2026-11-03 10:00 UTC')
  expect(healthLine(endpoint({ health: { ...endpoint().health, consecutiveFailures: 7 } }))).toBe('7 failed in a row')
  expect(healthLine(endpoint({ enabled: false, health: { ...endpoint().health, disabledReason: 'disabled automatically after 50 failed deliveries in a row' } }))).toContain('disabled automatically')
  expect(nextTryText(delivery({ status: 'failed', nextAttemptAt: '2026-11-03T11:00:00Z' }))).toBe('next try 2026-11-03 11:00 UTC')
  expect(nextTryText(delivery({ status: 'delivered' }))).toBe('')
  expect(nextTryText(delivery({ status: 'dead', nextAttemptAt: '2026-11-03T11:00:00Z' }))).toBe('')
})

test('the URL check explains the obvious mistakes and leaves the rest to the server', () => {
  expect(urlProblem('')).toMatch(/required/)
  expect(urlProblem('   ')).toMatch(/required/)
  expect(urlProblem('not a url')).toMatch(/not a valid URL/)
  expect(urlProblem('https://' + 'a'.repeat(2100) + '.com')).toMatch(/2048/)
  expect(urlProblem('https://example.com/hooks')).toBeNull()
  expect(urlProblem('http://127.0.0.1:9000/h')).toBeNull() // allowed in development: the server decides
})

test('webhook and invite actions have audit labels', () => {
  for (const a of ['webhook_endpoint.create', 'webhook_endpoint.update', 'webhook_endpoint.delete', 'webhook_endpoint.rotate_secret', 'webhook_endpoint.test', 'webhook_endpoint.resend', 'webhook_endpoint.auto_disable', 'outbox.overflow', 'invite.resend']) {
    expect(actionLabel(a), a).not.toBe(a)
  }
})
