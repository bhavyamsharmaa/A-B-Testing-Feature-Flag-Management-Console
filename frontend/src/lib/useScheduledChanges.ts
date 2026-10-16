import { useCallback, useEffect, useRef, useState } from 'react'
import { listScheduledChanges } from '../api/scheduling'
import type { ScheduledChange } from '../types'
import { describeError } from './errors'

/** The scheduled changes of one flag in one environment (workspaceId is part of the identity). */
export function useScheduledChanges(env: string, flagKey: string, workspaceId: string | null) {
  const [changes, setChanges] = useState<ScheduledChange[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0) // a superseded request must not overwrite newer state

  const load = useCallback(async () => {
    const id = ++seq.current
    setLoading(true)
    setError(null)
    try {
      const list = await listScheduledChanges(env, flagKey)
      if (id === seq.current) setChanges(list)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view scheduled changes'))
    } finally {
      if (id === seq.current) setLoading(false)
    }
  }, [env, flagKey, workspaceId])

  useEffect(() => {
    setChanges(null)
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  return { changes, loading, error, reload: load }
}
