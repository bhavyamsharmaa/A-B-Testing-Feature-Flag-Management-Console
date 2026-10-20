import { useState } from 'react'
import { ApiError } from '../api/client'
import { restoreWorkspace } from '../api/workspaces'
import { useAuth } from '../auth/AuthProvider'
import { useWorkspace } from '../workspace/WorkspaceProvider'
import { CreateWorkspaceModal } from './CreateWorkspaceModal'
import { Logo } from './Logo'

/**
 * Shown instead of the console when the signed-in user belongs to no workspace: typically because the only one they
 * owned is scheduled for deletion. Without it there is nothing to click (the workspace switcher hides itself).
 * They can restore what is pending, or start a new workspace. Pending deletions still count toward the limit of
 * owned workspaces until they are purged, which the server says when it refuses a create.
 */
export function NoWorkspaceScreen() {
  const { pendingDeletion, activate } = useWorkspace()
  const { refreshMe, signOut, session } = useAuth()
  const [creating, setCreating] = useState(false)
  const [restoring, setRestoring] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  async function restore(id: string) {
    setRestoring(id)
    setError(null)
    try {
      await restoreWorkspace(id)
      await refreshMe()
      await activate(id)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not restore the workspace. Try again.')
    } finally {
      setRestoring(null)
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <section
        data-testid="no-workspace-screen"
        className="w-full max-w-md animate-fade-up rounded-2xl border border-white/10 bg-surface/70 p-7 shadow-[0_0_80px_-20px_rgba(124,92,255,0.45)] backdrop-blur-xl"
      >
        <h1>
          <Logo className="text-xl" />
        </h1>
        <h2 className="mt-5 text-lg font-semibold text-white">You don't have a workspace right now</h2>
        <p className="mt-2 text-sm text-zinc-400">
          {session?.user.email ? (
            <>
              <span className="font-medium text-zinc-200">{session.user.email}</span> isn't a member of any workspace.{' '}
            </>
          ) : null}
          {pendingDeletion.length > 0
            ? 'The ones you owned are scheduled for deletion. You can restore one until its date, or start a new workspace.'
            : 'Create one to start, or open an invite link if someone invited you.'}
        </p>

        {pendingDeletion.length > 0 && (
          <ul className="mt-4 divide-y divide-white/5 rounded-lg border border-white/10" data-testid="no-workspace-pending">
            {pendingDeletion.map((p) => (
              <li key={p.id} className="flex items-center justify-between gap-3 px-3.5 py-2.5 text-sm">
                <span className="min-w-0">
                  <span className="block truncate text-zinc-200">{p.name}</span>
                  <span className="block text-[11px] text-amber-300">
                    <span aria-hidden="true">⏳ </span>
                    Deleted for good on {new Date(p.purgeAfter).toLocaleDateString(undefined, { dateStyle: 'medium' })}
                  </span>
                </span>
                <button
                  onClick={() => void restore(p.id)}
                  disabled={restoring !== null}
                  aria-label={`Restore ${p.name}`}
                  className="shrink-0 rounded-lg border border-white/10 px-2.5 py-1 text-xs text-zinc-200 transition hover:border-accent/60 hover:bg-accent/10 disabled:opacity-50"
                >
                  {restoring === p.id ? 'Restoring…' : 'Restore'}
                </button>
              </li>
            ))}
          </ul>
        )}

        {error && (
          <p role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}

        <div className="mt-6 flex flex-wrap items-center gap-2">
          <button
            onClick={() => setCreating(true)}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110"
          >
            Create workspace
          </button>
          <button onClick={() => void signOut()} className="ml-auto px-2 py-2 text-sm text-zinc-500 underline hover:text-zinc-300">
            Sign out
          </button>
        </div>
      </section>
      {creating && <CreateWorkspaceModal onClose={() => setCreating(false)} onCreated={() => setCreating(false)} />}
    </main>
  )
}
