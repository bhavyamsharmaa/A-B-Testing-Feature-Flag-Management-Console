import { useState } from 'react'
import { ApiError } from '../../api/client'
import { scheduleWorkspaceDeletion } from '../../api/workspaces'
import { useAuth } from '../../auth/AuthProvider'
import type { Workspace } from '../../types'
import { Modal } from '../Modal'

/** Owners only. Deleting hides the workspace at once and removes its data after 14 days; until then it can be restored. */
export function DeleteWorkspaceSection({ workspace }: { workspace: Workspace }) {
  const { refreshMe } = useAuth()
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  if (workspace.role !== 'owner') return null

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      await scheduleWorkspaceDeletion(workspace.id, typed)
      await refreshMe() // the workspace is gone from the list; the console moves on to another one
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not schedule the deletion. Try again.')
      setBusy(false)
    }
  }

  return (
    <section aria-labelledby="delete-heading" className="space-y-3 rounded-xl border border-red-500/30 bg-red-500/[0.04] p-4" data-testid="delete-workspace">
      <h2 id="delete-heading" className="text-xs font-medium uppercase tracking-wider text-red-300">
        Delete this workspace
      </h2>
      <p className="max-w-2xl text-sm text-zinc-300">
        The workspace disappears for everyone at once: its SDK keys stop working, its API tokens stop, scheduled changes and webhooks stop, and members see nothing. Its
        data is deleted for good after <strong className="text-white">14 days</strong>. Until then you can restore it, exactly as it was, from the workspace menu.
      </p>
      <button
        onClick={() => {
          setTyped('')
          setError(null)
          setOpen(true)
        }}
        className="rounded-lg border border-red-500/50 px-4 py-2 text-sm text-red-200 transition hover:bg-red-500/10"
      >
        Delete workspace…
      </button>
      {open && (
        <Modal title={`Delete ${workspace.name}?`} onClose={() => setOpen(false)} busy={busy}>
          <div className="mt-4 space-y-4">
            <p className="text-sm text-zinc-300">
              This hides <span className="font-medium text-white">{workspace.name}</span> from everyone now and deletes its flags, experiments, events, audit log, SDK keys and
              members after 14 days. Type the workspace&rsquo;s name to confirm.
            </p>
            <div>
              <label htmlFor="delete-confirm" className="block text-sm text-zinc-300">
                Workspace name
              </label>
              <input
                id="delete-confirm"
                autoFocus
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                disabled={busy}
                autoComplete="off"
                placeholder={workspace.name}
                className="mt-1 w-full rounded-lg border border-white/10 bg-black/40 px-3 py-2 text-sm outline-none focus:border-red-400"
              />
            </div>
            {error && (
              <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                {error}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <button onClick={() => setOpen(false)} disabled={busy} className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 hover:bg-white/5 disabled:opacity-50">
                Cancel
              </button>
              <button
                onClick={() => void submit()}
                disabled={busy || typed.trim() !== workspace.name}
                className="rounded-lg bg-red-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-red-500 disabled:cursor-not-allowed disabled:opacity-50"
              >
                {busy ? 'Deleting…' : 'Delete workspace'}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </section>
  )
}
