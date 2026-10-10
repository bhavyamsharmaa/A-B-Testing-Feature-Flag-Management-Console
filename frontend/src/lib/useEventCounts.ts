import { useEffect, useState } from 'react'
import { getEventCounts } from '../api/experiments'
import type { EventCounts } from '../types'

const POLL_MS = 5_000

/**
 * Live counts for one experiment. A running experiment is polled every few
 * seconds (only while the tab is visible), so data can be watched arriving;
 * any other is fetched once.
 */
export function useEventCounts(env: string, key: string, running: boolean) {
  const [counts, setCounts] = useState<EventCounts | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    const fetchOnce = async () => {
      try {
        const c = await getEventCounts(env, key)
        if (!cancelled) {
          setCounts(c)
          setFailed(false)
        }
      } catch {
        if (!cancelled) setFailed(true)
      }
    }
    void fetchOnce()
    if (!running) return () => void (cancelled = true)
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') void fetchOnce()
    }, POLL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [env, key, running])

  return { counts, failed }
}
