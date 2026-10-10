import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '../api/client'
import { getResults } from '../api/results'
import type { ExperimentResults, MetricSelection } from '../types'

/** Polling interval while an experiment runs and the tab is visible. */
export const POLL_MS = 7_000
const MAX_BACKOFF_MS = 60_000

export type ResultsState =
  | { kind: 'loading' }
  | { kind: 'ready'; data: ExperimentResults; slow: boolean; stale: boolean }
  | { kind: 'draft' } //                 409: not started
  | { kind: 'notFound' } //              404: no such experiment here
  | { kind: 'metricGone' } //            400: a chosen metric has no data any more
  | { kind: 'timeout' } //               503 RESULTS_TIMEOUT
  | { kind: 'rateLimited'; retryAfter: number | null }
  | { kind: 'network'; message: string }
  | { kind: 'error'; message: string }

/**
 * Results of one experiment for the selected metrics.
 *
 * - The effect is keyed on workspace, environment, experiment and metrics: a
 *   change in any of them cancels the in-flight request, clears what was
 *   shown and starts over, so another experiment's numbers are never visible.
 * - While the experiment runs and the tab is visible it re-reads every few
 *   seconds; hiding the tab pauses, showing it reads again at once. A stopped
 *   experiment is read once.
 * - 429 slows the polling down (honouring Retry-After) and keeps the last
 *   numbers on screen with a note.
 * - Unmounting (leaving the page, logging out) aborts the request.
 */
export function useResults(workspaceId: string | null, env: string | null, key: string, metrics: MetricSelection[]) {
  const [state, setState] = useState<ResultsState>({ kind: 'loading' })
  const [nonce, setNonce] = useState(0)
  const retry = useCallback(() => setNonce((n) => n + 1), [])
  const metricsKey = metrics.map((m) => `${m.eventName}:${m.kind}`).join(',')

  useEffect(() => {
    if (!env || !workspaceId) return
    setState({ kind: 'loading' })
    const ac = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let delay = POLL_MS
    let running = false
    let alive = true

    const schedule = () => {
      clearTimeout(timer)
      if (alive && document.visibilityState === 'visible') timer = setTimeout(() => void read(), delay)
    }

    const read = async () => {
      if (!alive) return
      try {
        const data = await getResults(env, key, metrics, ac.signal)
        if (!alive) return
        delay = POLL_MS
        running = data.status === 'running'
        setState({ kind: 'ready', data, slow: false, stale: false })
        if (running) schedule()
      } catch (err) {
        if (!alive || ac.signal.aborted) return
        if (!(err instanceof ApiError)) {
          setState({ kind: 'error', message: 'Something went wrong. Try again.' })
          return
        }
        switch (true) {
          case err.status === 429: {
            delay = Math.min(MAX_BACKOFF_MS, Math.max((err.retryAfter ?? 0) * 1000, delay * 2))
            setState((prev) =>
              prev.kind === 'ready' ? { ...prev, slow: true } : { kind: 'rateLimited', retryAfter: err.retryAfter ?? null },
            )
            schedule() // keep trying, more slowly
            return
          }
          case err.status === 409:
            setState({ kind: 'draft' })
            return
          case err.status === 404:
            setState({ kind: 'notFound' })
            return
          case err.status === 400 && /no event named|no primary metric/i.test(err.message):
            setState({ kind: 'metricGone' })
            return
          case err.code === 'RESULTS_TIMEOUT':
            setState({ kind: 'timeout' })
            return
          case err.status === 0:
            // keep what is on screen (marked as stale) and try again later when polling
            setState((prev) => (prev.kind === 'ready' ? { ...prev, stale: true } : { kind: 'network', message: err.message }))
            if (running) schedule()
            return
          default:
            setState({ kind: 'error', message: err.message })
        }
      }
    }

    const onVisibility = () => {
      if (document.visibilityState === 'hidden') clearTimeout(timer)
      else if (running) void read() // back on screen: read now, which also re-arms the timer
    }
    document.addEventListener('visibilitychange', onVisibility)
    void read()

    return () => {
      alive = false
      ac.abort()
      clearTimeout(timer)
      document.removeEventListener('visibilitychange', onVisibility)
    }
    // metricsKey stands for metrics
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, env, key, metricsKey, nonce])

  return { state, retry }
}
