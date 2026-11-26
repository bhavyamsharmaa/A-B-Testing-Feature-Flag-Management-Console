import { useCallback, useEffect, useRef, useState } from 'react'
import { listFlags } from '../api/flags'
import type { Flag } from '../types'
import { describeError } from './errors'

export function useFlags(env: string | null) {
  const [flags, setFlags] = useState<Flag[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seq = useRef(0) // ignore responses from a superseded request

  const load = useCallback(async () => {
    if (!env) return
    const id = ++seq.current
    setLoading(true)
    setError(null)
    try {
      const list = await listFlags(env)
      if (id === seq.current) setFlags(list)
    } catch (err) {
      if (id === seq.current) setError(describeError(err, 'view flags'))
    } finally {
      if (id === seq.current) setLoading(false)
    }
  }, [env])

  useEffect(() => {
    setFlags(null) // never show another environment's flags while loading
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  /** Swap in the server's version of one flag after a toggle or kill. */
  const replace = useCallback(
    (flag: Flag) => {
      // A response for an environment the user has since left must not touch this list.
      if (flag.environment !== env) return
      setFlags((prev) => prev && prev.map((f) => (f.key === flag.key ? flag : f)))
    },
    [env],
  )

  return { flags, loading, error, reload: load, replace }
}
