// Pure helpers for the webhooks page: honest words for health and delivery status, never colour alone.

import type { InviteEmailStatus, WebhookDelivery, WebhookDeliveryStatus, WebhookEndpoint, WebhookEndpointStatus, WebhookEventType } from '../types'

export const EVENT_LABEL: Record<WebhookEventType, string> = {
  'flag.created': 'Flag created',
  'flag.updated': 'Flag updated',
  'flag.killed': 'Flag killed',
  'flag.deleted': 'Flag deleted',
  'experiment.started': 'Experiment started',
  'experiment.stopped': 'Experiment stopped',
  'promotion.applied': 'Promotion applied',
  'scheduled_change.executed': 'Scheduled change ran',
  'scheduled_change.failed': 'Scheduled change failed',
}

/** A word and a symbol for every state: the colour is a bonus. */
export const ENDPOINT_MARK: Record<WebhookEndpointStatus, { mark: string; label: string; tone: string }> = {
  healthy: { mark: '✓', label: 'Healthy', tone: 'bg-emerald-500/15 text-emerald-300' },
  failing: { mark: '!', label: 'Failing', tone: 'bg-amber-500/20 text-amber-200' },
  disabled: { mark: '⏸', label: 'Disabled', tone: 'bg-red-500/15 text-red-300' },
  idle: { mark: '–', label: 'No deliveries yet', tone: 'bg-white/10 text-zinc-300' },
}

export const DELIVERY_MARK: Record<WebhookDeliveryStatus, { mark: string; label: string; tone: string }> = {
  delivered: { mark: '✓', label: 'Delivered', tone: 'bg-emerald-500/15 text-emerald-300' },
  pending: { mark: '…', label: 'Queued', tone: 'bg-white/10 text-zinc-300' },
  delivering: { mark: '…', label: 'Sending', tone: 'bg-white/10 text-zinc-300' },
  failed: { mark: '!', label: 'Failed, will retry', tone: 'bg-amber-500/20 text-amber-200' },
  dead: { mark: '✕', label: 'Failed, gave up', tone: 'bg-red-500/15 text-red-300' },
}

export const EMAIL_MARK: Record<InviteEmailStatus, { mark: string; label: string; tone: string }> = {
  queued: { mark: '…', label: 'Queued', tone: 'bg-white/10 text-zinc-300' },
  sent: { mark: '✓', label: 'Sent', tone: 'bg-emerald-500/15 text-emerald-300' },
  failed: { mark: '✕', label: 'Failed', tone: 'bg-red-500/15 text-red-300' },
  none: { mark: '–', label: 'No email', tone: 'bg-white/10 text-zinc-300' },
}

/** Only a failed delivery can be sent again. */
export const canResend = (d: WebhookDelivery) => d.status === 'failed' || d.status === 'dead'

/** "all environments" or the key. */
export const environmentText = (e: WebhookEndpoint) => (e.environment ? e.environment.key : 'All environments')

/** Event names, shortened when there are many. */
export function eventsText(types: WebhookEventType[], all: number): string {
  if (types.length >= all) return 'All events'
  if (types.length <= 2) return types.map((t) => EVENT_LABEL[t] ?? t).join(', ')
  return `${types.length} events`
}

/** "12 ms", "1.4 s". */
export function durationText(ms: number | null): string {
  if (ms === null) return '–'
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
}

/** One honest line about when an endpoint last worked or failed. */
export function healthLine(e: WebhookEndpoint): string {
  if (!e.enabled) return e.health.disabledReason ?? 'Disabled'
  if (e.health.consecutiveFailures > 0) return `${e.health.consecutiveFailures} failed in a row`
  if (e.health.lastSuccessAt) return `Last delivered ${new Date(e.health.lastSuccessAt).toISOString().replace('T', ' ').slice(0, 16)} UTC`
  return 'Nothing delivered yet'
}

/** What a person sees for a delivery that has not finished: when the next try is. */
export function nextTryText(d: WebhookDelivery): string {
  if (!d.nextAttemptAt || (d.status !== 'failed' && d.status !== 'pending')) return ''
  return `next try ${new Date(d.nextAttemptAt).toISOString().replace('T', ' ').slice(0, 16)} UTC`
}

/** A client-side check for the obvious mistakes. The server decides everything else (https, public hosts, ports) and says why. */
export function urlProblem(raw: string): string | null {
  const v = raw.trim()
  if (v === '') return 'URL is required.'
  if (v.length > 2048) return 'URL is at most 2048 characters.'
  try {
    new URL(v)
  } catch {
    return 'That is not a valid URL, for example https://example.com/hooks/helios.'
  }
  return null
}
