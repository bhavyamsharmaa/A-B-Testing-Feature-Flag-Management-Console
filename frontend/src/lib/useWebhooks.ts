import { useCallback, useEffect, useRef, useState } from 'react'
import { listWebhooks, type WebhookList } from '../api/webhooks'
import { describeError } from './errors'

/** The webhook endpoints of one workspace (admins and owners only; others are not asked). */
export function useWebhooks(workspaceId: string | null, enabled: boolean) {
  const [data, setData] = useState<WebhookList | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0)

  const load = useCallback(async () => {
    if (!workspaceId || !enabled) return
    const id = ++seq.current
    setLoading(true)
    setError(null)
    try {
      const list = await listWebhooks(workspaceId)
      if (id === seq.current) setData(list)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view webhooks'))
    } finally {
      if (id === seq.current) setLoading(false)
    }
  }, [workspaceId, enabled])

  useEffect(() => {
    setData(null)
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  return { data, loading, error, reload: load }
}
