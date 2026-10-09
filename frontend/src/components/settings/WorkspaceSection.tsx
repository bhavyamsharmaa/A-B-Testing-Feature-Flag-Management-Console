import { useState, type FormEvent } from 'react'
import { ApiError } from '../../api/client'
import { renameWorkspace } from '../../api/workspaces'
import { useAuth } from '../../auth/AuthProvider'
import { canManageWorkspace, ROLE_HINT, ROLE_LABEL } from '../../lib/permissions'
import type { Workspace } from '../../types'

export function WorkspaceSection({ workspace }: { workspace: Workspace }) {
  const { refreshMe } = useAuth()
  const canManage = canManageWorkspace(workspace.role)
  const [name, setName] = useState(workspace.name)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || trimmed === workspace.name) return
    setSaving(true)
    setMessage(null)
    try {
      await renameWorkspace(workspace.id, trimmed)
      await refreshMe()
      setMessage({ kind: 'ok', text: 'Workspace renamed.' })
    } catch (err) {
      setMessage({ kind: 'error', text: err instanceof ApiError ? err.message : 'Could not rename the workspace.' })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section aria-labelledby="ws-heading" className="space-y-3">
      <h2 id="ws-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
        Workspace
      </h2>
      <form onSubmit={onSubmit} className="flex flex-wrap items-end gap-3">
        <div className="min-w-[16rem] flex-1">
          <label htmlFor="workspace-rename" className="block text-sm text-zinc-300">
            Name
          </label>
          <input
            id="workspace-rename"
            value={name}
            onChange={(e) => setName(e.target.value)}
            readOnly={!canManage}
            maxLength={60}
            className="mt-1 w-full rounded-lg border border-white/10 bg-black/40 px-3 py-2 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 read-only:opacity-70"
          />
        </div>
        {canManage && (
          <button
            type="submit"
            disabled={saving || !name.trim() || name.trim() === workspace.name}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
        )}
      </form>
      {message && (
        <p
          role={message.kind === 'error' ? 'alert' : 'status'}
          className={`text-sm ${message.kind === 'error' ? 'text-red-300' : 'text-emerald-300'}`}
        >
          {message.text}
        </p>
      )}
      <p className="text-xs text-zinc-500">
        Your role: <span className="text-zinc-300">{ROLE_LABEL[workspace.role]}</span>. {ROLE_HINT[workspace.role]}.
        {!canManage && ' Only owners and admins can rename the workspace, invite people or manage SDK keys.'}
      </p>
    </section>
  )
}
