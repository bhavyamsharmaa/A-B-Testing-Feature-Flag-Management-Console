import { useState, type FormEvent } from 'react'
import { ApiError } from '../api/client'
import { useWorkspace } from '../workspace/WorkspaceProvider'
import { Modal } from './Modal'

export function CreateWorkspaceModal({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string) => void }) {
  const { create } = useWorkspace()
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) {
      setError('Give the workspace a name.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      const created = await create(trimmed)
      onCreated(created.name)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Something went wrong. Try again.')
      setBusy(false)
    }
  }

  return (
    <Modal title="Create workspace" onClose={onClose} busy={busy}>
      <form onSubmit={onSubmit}>
        <p className="mt-1 text-sm text-zinc-400">
          A workspace is a private space with its own flags, environments, members and SDK keys. You will be its owner.
        </p>
        <label htmlFor="workspace-name" className="mt-4 block text-sm text-zinc-300">
          Workspace name
        </label>
        <input
          id="workspace-name"
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={60}
          disabled={busy}
          placeholder="Acme Inc"
          className="mt-1 w-full rounded-lg border border-white/10 bg-black/40 px-3 py-2.5 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:opacity-60"
        />
        {error && (
          <p role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}
        <div className="mt-6 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={busy}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {busy ? 'Creating…' : 'Create workspace'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
