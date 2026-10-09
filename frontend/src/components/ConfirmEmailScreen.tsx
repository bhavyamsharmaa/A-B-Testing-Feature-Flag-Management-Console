import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiError } from '../api/client'
import { useAuth } from '../auth/AuthProvider'
import { takePendingInvite } from '../lib/pendingInvite'
import { supabase } from '../lib/supabase'
import { Logo } from './Logo'

const RESEND_COOLDOWN_S = 30

/**
 * Shown instead of the console when the backend answers EMAIL_NOT_CONFIRMED:
 * the account exists but its email address was never confirmed, so it has no
 * workspace yet. Resending asks Supabase for another confirmation email;
 * "I've confirmed" refreshes the session (the token then carries the new
 * state) and asks the backend again.
 */
export function ConfirmEmailScreen() {
  const { session, signOut, refreshMe } = useAuth()
  const navigate = useNavigate()
  const email = session?.user.email ?? ''
  const [cooldown, setCooldown] = useState(0)
  const [sending, setSending] = useState(false)
  const [checking, setChecking] = useState(false)
  const [notice, setNotice] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)

  useEffect(() => {
    if (cooldown <= 0) return
    const t = setTimeout(() => setCooldown((c) => c - 1), 1000)
    return () => clearTimeout(t)
  }, [cooldown])

  async function resend() {
    setSending(true)
    setNotice(null)
    const { error } = await supabase.auth.resend({ type: 'signup', email })
    setSending(false)
    if (error) {
      setNotice({ kind: 'error', text: error.message || 'Could not send the email. Try again in a minute.' })
      return
    }
    setCooldown(RESEND_COOLDOWN_S)
    setNotice({ kind: 'ok', text: `We sent a new confirmation link to ${email}.` })
  }

  async function check() {
    setChecking(true)
    setNotice(null)
    try {
      await supabase.auth.refreshSession() // the new token carries the confirmed state
      await refreshMe() // throws EMAIL_NOT_CONFIRMED while it is not
      // An invite link opened before confirming picks up where it left off.
      const pending = takePendingInvite()
      navigate(pending ? `/invite/${pending}` : '/console', { replace: true })
    } catch (err) {
      setNotice({
        kind: 'error',
        text:
          err instanceof ApiError && err.code === 'EMAIL_NOT_CONFIRMED'
            ? "We can't see the confirmation yet. Open the link in the email, then try again."
            : err instanceof ApiError
              ? err.message
              : 'Could not check right now. Try again.',
      })
    } finally {
      setChecking(false)
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <section
        data-testid="confirm-email-screen"
        className="w-full max-w-md animate-fade-up rounded-2xl border border-white/10 bg-surface/70 p-7 shadow-[0_0_80px_-20px_rgba(124,92,255,0.45)] backdrop-blur-xl"
      >
        <h1>
          <Logo className="text-xl" />
        </h1>
        <h2 className="mt-5 text-lg font-semibold text-white">Please confirm your email first</h2>
        <p className="mt-2 text-sm text-zinc-400">
          Your account <span className="font-medium text-zinc-200">{email}</span> isn't confirmed yet, so Helios hasn't created your
          workspace. Open the link we sent you, then continue.
        </p>

        {notice && (
          <p
            role={notice.kind === 'error' ? 'alert' : 'status'}
            className={`mt-4 rounded-md border px-3 py-2 text-sm ${
              notice.kind === 'error' ? 'border-red-500/40 bg-red-500/10 text-red-300' : 'border-emerald-500/40 bg-emerald-500/10 text-emerald-200'
            }`}
          >
            {notice.text}
          </p>
        )}

        <div className="mt-6 flex flex-wrap gap-2">
          <button
            onClick={() => void check()}
            disabled={checking}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {checking ? 'Checking…' : "I've confirmed my email"}
          </button>
          <button
            onClick={() => void resend()}
            disabled={sending || cooldown > 0}
            className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {sending ? 'Sending…' : cooldown > 0 ? `Resend email (${cooldown}s)` : 'Resend email'}
          </button>
          <button onClick={() => void signOut()} className="ml-auto px-2 py-2 text-sm text-zinc-500 underline hover:text-zinc-300">
            Sign out
          </button>
        </div>
      </section>
    </main>
  )
}
