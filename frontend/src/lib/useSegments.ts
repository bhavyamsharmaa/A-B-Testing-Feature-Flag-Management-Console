import { useCallback, useEffect, useRef, useState } from 'react'
import { listSegments } from '../api/segments'
import type { SegmentSummary } from '../types'
import { describeError } from './errors'

/** The segments of one workspace (they are shared by all its environments). */
export function useSegments(workspaceId: string | null) {
  const [segments, setSegments] = useState<SegmentSummary[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0) // a superseded request must not overwrite newer state

  const load = useCallback(async () => {
    if (!workspaceId) return
    const id = ++seq.current
    setLoading(true)
    setError(null)
    try {
      const list = await listSegments(workspaceId)
      if (id === seq.current) setSegments(list)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view segments'))
    } finally {
      if (id === seq.current) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    setSegments(null) // never show another workspace's segments while loading
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  return { segments, loading, error, reload: load }
}
