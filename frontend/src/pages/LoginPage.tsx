import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router-dom'
import { isAuthRetryableFetchError } from '@supabase/supabase-js'
import { useAuth } from '../auth/AuthProvider'
import { Splash } from '../components/Splash'
import { supabase } from '../lib/supabase'

export default function LoginPage() {
  const { session, loading } = useAuth()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (loading) return <Splash />
  if (session) return <Navigate to="/console" replace />

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      const { error: authError } = await supabase.auth.signInWithPassword({ email: email.trim(), password })
      if (authError) {
        if (isAuthRetryableFetchError(authError)) {
          setError('Could not reach the server. Check your connection and try again.')
        } else if (authError.code === 'invalid_credentials' || authError.status === 400) {
          setError('Incorrect email or password.')
        } else {
          setError(authError.message)
        }
      }
      // On success the auth listener sets the session and this page redirects.
    } catch {
      setError('Could not reach the server. Check your connection and try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <form onSubmit={onSubmit} className="w-full max-w-sm rounded-xl border border-border bg-surface p-6">
        <h1 className="text-xl font-semibold">
          <span className="text-accent">Helios</span> console
        </h1>
        <p className="mt-1 text-sm text-zinc-400">Sign in to manage feature flags.</p>

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
          className="mt-1 w-full rounded-md border border-border bg-bg px-3 py-2 text-sm outline-none focus:border-accent disabled:opacity-60"
        />

        <label className="mt-4 block text-sm text-zinc-300" htmlFor="password">
          Password
        </label>
        <input
          id="password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={submitting}
          className="mt-1 w-full rounded-md border border-border bg-bg px-3 py-2 text-sm outline-none focus:border-accent disabled:opacity-60"
        />

        {error && (
          <p role="alert" className="mt-4 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}

        <button
          type="submit"
          disabled={submitting}
          className="mt-6 w-full rounded-md bg-accent px-3 py-2 text-sm font-medium text-white transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60"
        >
          {submitting ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}
