import { useEffect, useRef, useState } from 'react'
import { BANNER_FLAG, DEFAULT_DISCOUNT, ONES_FLAG, TENS_FLAG, joinDiscount } from '../lib/bannerDiscount'
import { evaluateFlags, openStream } from './helios'

export type StreamStatus = 'connecting' | 'live' | 'reconnecting'

const POLL_MS = 30_000 // degraded fallback while the stream is down
const RECONNECT_GRACE_MS = 1_500 // routine reconnects shouldn't flicker the chip
const SETTLE_MS = 600 // the discount is two flags: wait for both updates before re-reading
const KEYS = [BANNER_FLAG, TENS_FLAG, ONES_FLAG]

/**
 * Live demo banner: whether it shows and the discount on it. Values always come
 * from POST /evaluate (the stream event is only a cue to re-evaluate); the
 * banner is hidden whenever Helios can't be reached.
 */
export function useBanner() {
  const [on, setOn] = useState(false)
  const [discount, setDiscount] = useState(DEFAULT_DISCOUNT)
  const [status, setStatus] = useState<StreamStatus>('connecting')
  const [lastUpdate, setLastUpdate] = useState<Date | null>(null)
  const seq = useRef(0) // a slow older response must not overwrite a newer one

  useEffect(() => {
    let closed = false
    let pollTimer: ReturnType<typeof setInterval> | null = null
    let graceTimer: ReturnType<typeof setTimeout> | null = null
    let settleTimer: ReturnType<typeof setTimeout> | null = null

    async function refresh() {
      const id = ++seq.current
      let values = new Map<string, unknown>() // fallback
      try {
        values = await evaluateFlags(KEYS)
      } catch {
        /* Helios down or unreachable: hide the feature, keep the page */
      }
      if (closed || id !== seq.current) return
      // Only an explicit `true` turns the banner on.
      const show = values.get(BANNER_FLAG) === true
      setOn(show)
      // A hidden banner keeps its last text while it animates away.
      if (show) setDiscount(joinDiscount(values.get(TENS_FLAG), values.get(ONES_FLAG)))
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
        if (closed || !KEYS.includes(event.flagKey)) return
        setLastUpdate(new Date())
        if (event.flagKey === BANNER_FLAG) {
          void refresh()
          return
        }
        if (settleTimer) clearTimeout(settleTimer)
        settleTimer = setTimeout(() => void refresh(), SETTLE_MS)
      },
    })

    return () => {
      closed = true
      seq.current++
      close()
      stopFallbacks()
      if (settleTimer) clearTimeout(settleTimer)
    }
  }, [])

  return { on, discount, status, lastUpdate }
}
