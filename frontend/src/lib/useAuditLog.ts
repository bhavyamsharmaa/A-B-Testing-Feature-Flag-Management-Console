import { useCallback, useEffect, useRef, useState } from 'react'
import { listAuditLogs, type AuditFilters } from '../api/audit'
import type { AuditEntry } from '../types'
import { describeError } from './errors'

/** Newest-first audit entries for one environment, filtered server-side and paged by cursor. */
export function useAuditLog(env: string | null, filters: AuditFilters) {
  const { action, resourceId, severity } = filters
  const [entries, setEntries] = useState<AuditEntry[] | null>(null)
  const [nextCursor, setNextCursor] = useState<number | null>(null)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [moreError, setMoreError] = useState<string | null>(null)
  const seq = useRef(0) // a superseded request must not overwrite newer state

  /** Fetches from the top. Keeps the current rows visible until the new page arrives. */
  const load = useCallback(async () => {
    if (!env) return
    const id = ++seq.current
    setLoading(true)
    setLoadingMore(false)
    setError(null)
    setMoreError(null)
    try {
      const page = await listAuditLogs(env, { action, resourceId, severity })
      if (id !== seq.current) return
      setEntries(page.entries)
      setNextCursor(page.nextCursor)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view the audit log'))
    } finally {
      if (id === seq.current) setLoading(false)
    }
  }, [env, action, resourceId, severity])

  useEffect(() => {
    setEntries(null) // never show another environment's or filter's rows while loading
    setNextCursor(null)
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  const loadMore = useCallback(async () => {
    if (!env || nextCursor === null || loadingMore) return
    const id = ++seq.current
    setLoadingMore(true)
    setMoreError(null)
    try {
      const page = await listAuditLogs(env, { action, resourceId, severity }, nextCursor)
      if (id !== seq.current) return
      setEntries((prev) => [...(prev ?? []), ...page.entries])
      setNextCursor(page.nextCursor)
    } catch (err) {
      if (id === seq.current) setMoreError(describeError(err, 'view the audit log'))
    } finally {
      if (id === seq.current) setLoadingMore(false)
    }
  }, [env, action, resourceId, severity, nextCursor, loadingMore])

  return { entries, nextCursor, loading, loadingMore, error, moreError, reload: load, loadMore }
}
