import { useCallback, useEffect, useRef, useState } from 'react'
import { listExperiments } from '../api/experiments'
import type { Experiment } from '../types'
import { describeError } from './errors'

/** The experiments of one environment (read-only). */
export function useExperiments(env: string | null, workspaceId: string | null) {
  const [experiments, setExperiments] = useState<Experiment[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0) // a superseded request must not overwrite newer state

  const load = useCallback(async () => {
    if (!env) return
    const id = ++seq.current
    setLoading(true)
    setError(null)
    try {
      const list = await listExperiments(env)
      if (id === seq.current) setExperiments(list)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view experiments'))
    } finally {
      if (id === seq.current) setLoading(false)
    }
  }, [env, workspaceId])

  useEffect(() => {
    setExperiments(null) // never show another environment's experiments while loading
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  return { experiments, loading, error, reload: load }
}
