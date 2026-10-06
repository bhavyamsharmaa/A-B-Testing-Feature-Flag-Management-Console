// Minimal Helios SDK for the public demo page. Authenticates with the SDK key
// (X-Helios-SDK-Key), not the user JWT. The key comes from config, never source.

import { fetchEventSource } from '@microsoft/fetch-event-source'
import { config, heliosSdkKey } from '../config'

/** Stable subject so the same "visitor" gets the same answer on every evaluate. */
const SUBJECT_KEY = 'helios-demo-visitor'
const EVALUATE_TIMEOUT_MS = 15_000
const MAX_BACKOFF_MS = 30_000

interface EvaluationResult {
  flagKey: string
  value: unknown
}

/** Evaluates one boolean flag. Throws on any failure; callers serve their fallback. */
export async function evaluateFlag(flagKey: string): Promise<boolean> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), EVALUATE_TIMEOUT_MS)
  try {
    const res = await fetch(`${config.apiBaseUrl}/evaluate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Helios-SDK-Key': heliosSdkKey },
      body: JSON.stringify({ context: { subjectKey: SUBJECT_KEY }, flagKeys: [flagKey] }),
      signal: controller.signal,
    })
    if (!res.ok) throw new Error(`evaluate failed (${res.status})`)
    const body = (await res.json()) as { evaluations: EvaluationResult[] }
    // Only an explicit `true` turns the feature on; null (disabled/missing) is the fallback.
    return body.evaluations.find((e) => e.flagKey === flagKey)?.value === true
  } finally {
    clearTimeout(timer)
  }
}

export interface FlagUpdateEvent {
  action: string
  flagKey: string
  enabled: boolean
  version: number
}

export interface StreamHandlers {
  /** The stream is connected (first connect and every reconnect). */
  onOpen: () => void
  /** The stream dropped or failed to connect; a retry is already scheduled. */
  onDown: () => void
  onUpdate: (event: FlagUpdateEvent) => void
}

class StreamDown extends Error {}

/**
 * Opens GET /sdk/stream and keeps it open, reconnecting with exponential
 * backoff. fetch-event-source (not native EventSource) because the SDK key must
 * travel in a header. Returns a function that closes it for good.
 */
export function openStream(handlers: StreamHandlers): () => void {
  const controller = new AbortController()
  let failures = 0

  void fetchEventSource(`${config.apiBaseUrl}/sdk/stream`, {
    signal: controller.signal,
    headers: { 'X-Helios-SDK-Key': heliosSdkKey },
    // The demo runs in a tab next to the console; a hidden tab must stay live.
    openWhenHidden: true,
    async onopen(res) {
      if (res.ok && res.headers.get('content-type')?.startsWith('text/event-stream')) {
        failures = 0
        handlers.onOpen()
        return
      }
      throw new StreamDown(`stream refused (${res.status})`) // 401/503 etc. -> backoff
    },
    onmessage(msg) {
      if (msg.event !== 'flag_update') return
      try {
        handlers.onUpdate(JSON.parse(msg.data) as FlagUpdateEvent)
      } catch {
        /* a malformed payload is only a missed cue; evaluate stays the source of truth */
      }
    },
    onclose() {
      // The server (or a proxy idle timeout) ended the stream: reconnect.
      throw new StreamDown('stream closed')
    },
    onerror() {
      handlers.onDown()
      const delay = Math.min(MAX_BACKOFF_MS, 1000 * 2 ** failures)
      failures++
      return delay // retry after `delay` ms; the library never gives up on its own
    },
  })

  return () => controller.abort()
}
