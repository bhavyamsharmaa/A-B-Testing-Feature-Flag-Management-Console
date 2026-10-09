import { useEffect, useRef, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router-dom'
import { ApiError } from '../api/client'
import { acceptInviteByToken } from '../api/workspaces'
import { useAuth } from '../auth/AuthProvider'
import { Logo } from '../components/Logo'
import { Splash } from '../components/Splash'
import { clearPendingInvite, savePendingInvite } from '../lib/pendingInvite'
import { useWorkspace } from '../workspace/WorkspaceProvider'

/** /invite/:token. Signed out: remember the token, sign in (or up), then come back here to accept. */
export default function InvitePage() {
  const { token = '' } = useParams()
  const { session, loading, refreshMe } = useAuth()
  const { activate } = useWorkspace()
  const navigate = useNavigate()
  const started = useRef(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (loading || !session || started.current) return
    started.current = true
    void (async () => {
      try {
        const { workspaceId } = await acceptInviteByToken(token)
        clearPendingInvite()
        await refreshMe()
        await activate(workspaceId)
        navigate('/console', { replace: true })
      } catch (err) {
        if (err instanceof ApiError && err.code === 'EMAIL_NOT_CONFIRMED') {
          // Keep the token: after confirming, the console brings the person back here.
          savePendingInvite(token)
          navigate('/console', { replace: true })
          return
        }
        clearPendingInvite()
        setError(err instanceof ApiError ? err.message : 'Could not accept the invite. Try again.')
      }
    })()
  }, [loading, session, token, refreshMe, activate, navigate])

  if (loading) return <Splash />
  if (!session) {
    savePendingInvite(token)
    return <Navigate to="/login" replace />
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm animate-fade-up rounded-2xl border border-white/10 bg-surface/70 p-7 text-center backdrop-blur-xl">
        <h1 className="flex justify-center">
          <Logo className="text-xl" />
        </h1>
        {error ? (
          <>
            <p role="alert" className="mt-5 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
              {error}
            </p>
            <Link to="/console" className="mt-5 inline-block text-sm text-accent underline">
              Go to the console
            </Link>
          </>
        ) : (
          <p role="status" className="mt-5 text-sm text-zinc-300">
            Joining the workspace…
          </p>
        )}
      </div>
    </main>
  )
}
