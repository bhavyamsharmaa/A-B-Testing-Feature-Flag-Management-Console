import { useEffect, useState } from 'react'

/** True once `pending` has stayed true for `delayMs` (Render's free tier sleeps when idle). */
export function useSlowHint(pending: boolean, delayMs = 4000): boolean {
  const [slow, setSlow] = useState(false)
  useEffect(() => {
    if (!pending) {
      setSlow(false)
      return
    }
    const t = setTimeout(() => setSlow(true), delayMs)
    return () => clearTimeout(t)
  }, [pending, delayMs])
  return slow
}

export const SLOW_HINT = 'Waking up the server. This can take up to a minute on the first request…'
