import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import type { Session } from '@supabase/supabase-js'
import { apiClient } from '../api/client'
import { supabase } from '../lib/supabase'
import type { Me } from '../types'

interface AuthState {
  session: Session | null
  /** True until the stored session has been restored on first load. */
  loading: boolean
  signOut: () => Promise<void>
  me: Me | null
  meLoading: boolean
  meError: string | null
  reloadMe: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null)
  const [loading, setLoading] = useState(true)
  const [me, setMe] = useState<Me | null>(null)
  const [meLoading, setMeLoading] = useState(false)
  const [meError, setMeError] = useState<string | null>(null)
  const [meNonce, setMeNonce] = useState(0)

  useEffect(() => {
    // onAuthStateChange emits INITIAL_SESSION once the stored session is
    // restored, and then every sign-in/out/refresh.
    const { data } = supabase.auth.onAuthStateChange((_event, next) => {
      setSession(next)
      setLoading(false)
    })
    return () => data.subscription.unsubscribe()
  }, [])

  const userId = session?.user.id ?? null

  useEffect(() => {
    if (!userId) {
      setMe(null)
      setMeError(null)
      setMeLoading(false)
      return
    }
    let cancelled = false
    setMeLoading(true)
    setMeError(null)
    apiClient
      .get<Me>('/me')
      .then((result) => {
        if (!cancelled) setMe(result)
      })
      .catch((err: unknown) => {
        if (!cancelled) setMeError(err instanceof Error ? err.message : 'Failed to load your account.')
      })
      .finally(() => {
        if (!cancelled) setMeLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [userId, meNonce])

  const signOut = useCallback(async () => {
    await supabase.auth.signOut()
  }, [])

  const reloadMe = useCallback(() => setMeNonce((n) => n + 1), [])

  const value = useMemo<AuthState>(
    () => ({ session, loading, signOut, me, meLoading, meError, reloadMe }),
    [session, loading, signOut, me, meLoading, meError, reloadMe],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}
