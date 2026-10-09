import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import type { Session } from '@supabase/supabase-js'
import { ApiError, apiClient } from '../api/client'
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
  /** True when the backend refused the account because its email is not confirmed yet. */
  emailUnconfirmed: boolean
  reloadMe: () => void
  /** Re-fetches /me in place (no loading state), e.g. after creating a workspace or accepting an invite. */
  refreshMe: () => Promise<Me>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null)
  const [loading, setLoading] = useState(true)
  const [me, setMe] = useState<Me | null>(null)
  const [meLoading, setMeLoading] = useState(false)
  const [meError, setMeError] = useState<string | null>(null)
  const [meErrorCode, setMeErrorCode] = useState<string | null>(null)
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
      setMeErrorCode(null)
      setMeLoading(false)
      return
    }
    let cancelled = false
    setMeLoading(true)
    setMeError(null)
    setMeErrorCode(null)
    apiClient
      .get<Me>('/me')
      .then((result) => {
        if (!cancelled) setMe(result)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setMeError(err instanceof Error ? err.message : 'Failed to load your account.')
        setMeErrorCode(err instanceof ApiError ? err.code : null)
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

  const refreshMe = useCallback(async () => {
    const result = await apiClient.get<Me>('/me')
    setMe(result)
    setMeError(null)
    setMeErrorCode(null)
    return result
  }, [])

  const value = useMemo<AuthState>(
    () => ({ session, loading, signOut, me, meLoading, meError, emailUnconfirmed: meErrorCode === 'EMAIL_NOT_CONFIRMED', reloadMe, refreshMe }),
    [session, loading, signOut, me, meLoading, meError, meErrorCode, reloadMe, refreshMe],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}
