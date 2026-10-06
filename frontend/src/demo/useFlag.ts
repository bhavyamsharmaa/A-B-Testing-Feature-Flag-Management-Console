import { useEffect, useRef, useState } from 'react'
import { evaluateFlag, openStream } from './helios'

export type StreamStatus = 'connecting' | 'live' | 'reconnecting'

const POLL_MS = 30_000 // degraded fallback while the stream is down
const RECONNECT_GRACE_MS = 1_500 // routine reconnects shouldn't flicker the chip

/**
 * Live boolean flag. `on` always comes from POST /evaluate (the stream event is
 * only a cue to re-evaluate) and is false whenever Helios can't be reached.
 */
export function useFlag(flagKey: string) {
  const [on, setOn] = useState(false)
  const [status, setStatus] = useState<StreamStatus>('connecting')
  const [lastUpdate, setLastUpdate] = useState<Date | null>(null)
  const seq = useRef(0) // a slow older response must not overwrite a newer one

  useEffect(() => {
    let closed = false
    let pollTimer: ReturnType<typeof setInterval> | null = null
    let graceTimer: ReturnType<typeof setTimeout> | null = null

    async function refresh() {
      const id = ++seq.current
      let value = false // fallback
      try {
        value = await evaluateFlag(flagKey)
      } catch {
        /* Helios down or unreachable: hide the feature, keep the page */
      }
      if (!closed && id === seq.current) setOn(value)
    }

    function stopFallbacks() {
      if (pollTimer) clearInterval(pollTimer)
      if (graceTimer) clearTimeout(graceTimer)
      pollTimer = graceTimer = null
    }

    void refresh()
    const close = openStream({
      onOpen() {
        if (closed) return
        stopFallbacks()
        setStatus('live')
        void refresh() // recover any event missed while disconnected (pub/sub is at-most-once)
      },
      onDown() {
        if (closed) return
        if (!pollTimer) pollTimer = setInterval(() => void refresh(), POLL_MS)
        if (!graceTimer) graceTimer = setTimeout(() => setStatus('reconnecting'), RECONNECT_GRACE_MS)
      },
      onUpdate(event) {
        if (closed || event.flagKey !== flagKey) return
        setLastUpdate(new Date())
        void refresh()
      },
    })

    return () => {
      closed = true
      seq.current++
      close()
      stopFallbacks()
    }
  }, [flagKey])

  return { on, status, lastUpdate }
}
