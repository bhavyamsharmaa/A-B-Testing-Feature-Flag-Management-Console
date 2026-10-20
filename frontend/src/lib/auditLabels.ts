// Every action the backend writes today (audit.Write call sites). Unknown
// actions still render, using their raw name.
const LABELS: Record<string, string> = {
  'flag.create': 'Flag created',
  'flag.update': 'Flag updated',
  'flag.delete': 'Flag deleted',
  'flag.force_delete': 'Flag force-deleted',
  'flag.kill': 'Flag killed',
  'segment.create': 'Segment created',
  'segment.update': 'Segment updated',
  'segment.delete': 'Segment deleted',
  'segment.force_delete': 'Segment force-deleted',
  'scheduled_change.create': 'Change scheduled',
  'scheduled_change.cancel': 'Scheduled change cancelled',
  'scheduled_change.succeeded': 'Scheduled change applied',
  'scheduled_change.failed': 'Scheduled change failed',
  'scheduled_change.missed': 'Scheduled change missed',
  'scheduled_change.retry': 'Scheduled change will be retried',
  'api_token.create': 'API token created',
  'api_token.revoke': 'API token revoked',
  'api_token.rejected': 'API token refused',
  'webhook_endpoint.create': 'Webhook added',
  'webhook_endpoint.update': 'Webhook changed',
  'webhook_endpoint.delete': 'Webhook deleted',
  'webhook_endpoint.rotate_secret': 'Webhook secret rotated',
  'webhook_endpoint.test': 'Webhook test sent',
  'webhook_endpoint.resend': 'Webhook delivery resent',
  'webhook_endpoint.auto_disable': 'Webhook disabled automatically after repeated failures',
  'outbox.overflow': 'Webhook queue full: the oldest deliveries were dropped',
  'invite.resend': 'Invite email sent again',
  'ownership.offer': 'Ownership offered',
  'ownership.accept': 'Ownership accepted',
  'ownership.cancel': 'Ownership offer cancelled',
  'ownership.decline': 'Ownership offer declined',
  'ownership.expire': 'Ownership offer expired',
  'member.upsert': 'Member role set',
  'member.remove': 'Member removed',
  'api_key.create': 'API key created',
}

export const KNOWN_ACTIONS = Object.keys(LABELS)

export const actionLabel = (action: string) => LABELS[action] ?? action
