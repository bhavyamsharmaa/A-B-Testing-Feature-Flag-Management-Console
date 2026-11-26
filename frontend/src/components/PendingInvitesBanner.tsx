import { useState } from 'react'
import { acceptInviteById } from '../api/workspaces'
import { ApiError } from '../api/client'
import { useAuth } from '../auth/AuthProvider'
import { ROLE_LABEL } from '../lib/permissions'
import { useWorkspace } from '../workspace/WorkspaceProvider'

/** Invites addressed to the signed-in user's email, with a one-click Accept. */
export function PendingInvitesBanner() {
  const { invites, activate } = useWorkspace()
  const { refreshMe } = useAuth()
  const [busyId, setBusyId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  if (invites.length === 0) return null

  async function accept(inviteId: string) {
    setBusyId(inviteId)
    setError(null)
    try {
      const { workspaceId } = await acceptInviteById(inviteId)
      await refreshMe()
      await activate(workspaceId)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not accept the invite. Try again.')
      setBusyId(null)
    }
  }

  return (
    <section aria-label="Pending invites" className="mb-4 space-y-2" data-testid="pending-invites">
      {invites.map((inv) => (
        <div
          key={inv.id}
          className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-accent/40 bg-accent/10 px-4 py-3 text-sm"
        >
          <p className="text-zinc-200">
            <span className="font-medium text-white">{inv.invitedByEmail || 'Someone'}</span> invited you to{' '}
            <span className="font-medium text-white">{inv.workspaceName}</span> as {ROLE_LABEL[inv.role].toLowerCase()}.
          </p>
          <button
            onClick={() => void accept(inv.id)}
            disabled={busyId !== null}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-3.5 py-1.5 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {busyId === inv.id ? 'Joining…' : 'Accept'}
          </button>
        </div>
      ))}
      {error && (
        <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {error}
        </p>
      )}
    </section>
  )
}
