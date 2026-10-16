import { useCallback, useEffect, useRef, useState } from 'react'
import { listApiTokens } from '../api/tokens'
import type { ApiToken } from '../types'
import { describeError } from './errors'

/** The API tokens of one workspace (admins and owners only; others are not asked). */
export function useApiTokens(workspaceId: string | null, enabled: boolean) {
  const [tokens, setTokens] = useState<ApiToken[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0)

  const load = useCallback(async () => {
    if (!workspaceId || !enabled) return
    const id = ++seq.current
    setLoading(true)
    setError(null)
    try {
      const list = await listApiTokens(workspaceId)
      if (id === seq.current) setTokens(list)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view API tokens'))
    } finally {
      if (id === seq.current) setLoading(false)
    }
  }, [workspaceId, enabled])

  useEffect(() => {
    setTokens(null)
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  return { tokens, loading, error, reload: load }
}
