import { useState, type FormEvent } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { isAuthRetryableFetchError } from '@supabase/supabase-js'
import { useAuth } from '../auth/AuthProvider'
import { Logo } from '../components/Logo'
import { Splash } from '../components/Splash'
import { takePendingInvite } from '../lib/pendingInvite'
import { supabase } from '../lib/supabase'

type Mode = 'signin' | 'signup'

const INPUT =
  'mt-1 w-full rounded-lg border border-white/10 bg-black/40 px-3 py-2.5 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:opacity-60'

export default function LoginPage() {
  const { session, loading } = useAuth()
  const location = useLocation()
  const [mode, setMode] = useState<Mode>('signin')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmEmail, setConfirmEmail] = useState<string | null>(null)

  if (loading) return <Splash />
  if (session) {
    // An invite link opened while signed out brings the person back to accept it.
    const invite = takePendingInvite()
    const from = (location.state as { from?: { pathname?: string } } | null)?.from?.pathname
    return <Navigate to={invite ? `/invite/${invite}` : (from ?? '/console')} replace />
  }

  const signingUp = mode === 'signup'

  function switchMode(next: Mode) {
    setMode(next)
    setError(null)
    setConfirmEmail(null)
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setSubmitting(true)
    setError(null)
    setConfirmEmail(null)
    try {
      if (signingUp) {
        const { data, error: authError } = await supabase.auth.signUp({ email: email.trim(), password })
        if (authError) {
          if (authError.code === 'user_already_exists') setError('An account with this email already exists. Sign in instead.')
          else if (isAuthRetryableFetchError(authError)) setError('Could not reach the server. Check your connection and try again.')
          else setError(authError.message)
        } else if (data.user && data.user.identities?.length === 0) {
          // Supabase answers an already-registered email with a user that has no identities.
          setError('An account with this email already exists. Sign in instead.')
        } else if (!data.session) {
          // Email confirmation is on: no session until the link in the email is opened.
          setConfirmEmail(email.trim())
        }
        // With a session, the auth listener signs the new user in and this page redirects.
      } else {
        const { error: authError } = await supabase.auth.signInWithPassword({ email: email.trim(), password })
        if (authError) {
          if (isAuthRetryableFetchError(authError)) {
            setError('Could not reach the server. Check your connection and try again.')
          } else if (authError.code === 'email_not_confirmed') {
            setError('Confirm your email first: open the link we sent you, then sign in.')
          } else if (authError.code === 'invalid_credentials' || authError.status === 400) {
            setError('Incorrect email or password.')
          } else {
            setError(authError.message)
          }
        }
        // On success the auth listener sets the session and this page redirects.
      }
    } catch {
      setError('Could not reach the server. Check your connection and try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <form
        onSubmit={onSubmit}
        className="w-full max-w-sm animate-fade-up rounded-2xl border border-white/10 bg-surface/70 p-7 shadow-[0_0_80px_-20px_rgba(124,92,255,0.45)] backdrop-blur-xl"
      >
        <h1>
          <Logo className="text-xl" />
        </h1>
        <p className="mt-2 text-sm text-zinc-400">
          {signingUp ? 'Create an account. You get your own private workspace.' : 'Sign in to manage feature flags.'}
        </p>

        {confirmEmail ? (
          <div role="status" data-testid="confirm-email" className="mt-6 rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3.5 py-3 text-sm text-emerald-200">
            <p className="font-medium">Check your email</p>
            <p className="mt-1 text-emerald-200/80">
              We sent a confirmation link to <span className="font-medium">{confirmEmail}</span>. Open it, then come back and sign in.
              Your workspace is created the first time you do.
            </p>
            <button type="button" onClick={() => switchMode('signin')} className="mt-3 text-sm underline">
              Go to sign in
            </button>
          </div>
        ) : (
          <>
            <label className="mt-6 block text-sm text-zinc-300" htmlFor="email">
              Email
            </label>
            <input
              id="email"
              type="email"
              autoComplete="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              disabled={submitting}
              className={INPUT}
            />

            <label className="mt-4 block text-sm text-zinc-300" htmlFor="password">
              Password
            </label>
            <input
              id="password"
              type="password"
              autoComplete={signingUp ? 'new-password' : 'current-password'}
              required
              minLength={signingUp ? 6 : undefined}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={submitting}
              className={INPUT}
            />
            {signingUp && <p className="mt-1 text-xs text-zinc-500">At least 6 characters.</p>}

            {error && (
              <p role="alert" className="mt-4 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                {error}
              </p>
            )}

            <button
              type="submit"
              disabled={submitting}
              className="mt-6 w-full rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-3 py-2.5 text-sm font-medium text-white shadow-[0_8px_24px_-8px_rgba(124,92,255,0.8)] transition hover:brightness-110 active:scale-[0.99] disabled:cursor-not-allowed disabled:opacity-60"
            >
              {submitting ? (signingUp ? 'Creating account…' : 'Signing in…') : signingUp ? 'Create account' : 'Sign in'}
            </button>

            <p className="mt-5 text-center text-sm text-zinc-400">
              {signingUp ? 'Already have an account?' : 'New to Helios?'}{' '}
              <button type="button" onClick={() => switchMode(signingUp ? 'signin' : 'signup')} className="text-accent underline">
                {signingUp ? 'Sign in' : 'Create an account'}
              </button>
            </p>
          </>
        )}
      </form>
    </main>
  )
}
