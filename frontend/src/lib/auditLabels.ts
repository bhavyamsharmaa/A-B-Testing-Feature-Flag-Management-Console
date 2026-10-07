// Every action the backend writes today (audit.Write call sites). Unknown
// actions still render, using their raw name.
const LABELS: Record<string, string> = {
  'flag.create': 'Flag created',
  'flag.update': 'Flag updated',
  'flag.delete': 'Flag deleted',
  'flag.force_delete': 'Flag force-deleted',
  'flag.kill': 'Flag killed',
  'member.upsert': 'Member role set',
  'member.remove': 'Member removed',
  'api_key.create': 'API key created',
}

export const KNOWN_ACTIONS = Object.keys(LABELS)

export const actionLabel = (action: string) => LABELS[action] ?? action
