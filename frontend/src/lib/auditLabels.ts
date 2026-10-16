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
  'member.upsert': 'Member role set',
  'member.remove': 'Member removed',
  'api_key.create': 'API key created',
}

export const KNOWN_ACTIONS = Object.keys(LABELS)

export const actionLabel = (action: string) => LABELS[action] ?? action
