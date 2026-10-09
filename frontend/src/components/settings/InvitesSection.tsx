import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { ApiError } from '../../api/client'
import { createInvite, listInvites, revokeInvite } from '../../api/workspaces'
import { assignableRoles, canManageWorkspace, ROLE_LABEL } from '../../lib/permissions'
import type { Workspace, WorkspaceInvite, WorkspaceRole } from '../../types'
import { CopyField } from './CopyField'

const fmt = (iso: string) => new Date(iso).toLocaleDateString(undefined, { dateStyle: 'medium' })

/** Invite people by email and manage pending invites. Admins and owners only. */
export function InvitesSection({ workspace, onChanged }: { workspace: Workspace; onChanged: () => void }) {
  const canManage = canManageWorkspace(workspace.role)
  const [invites, setInvites] = useState<WorkspaceInvite[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<WorkspaceRole>('viewer')
  const [sending, setSending] = useState(false)
  const [created, setCreated] = useState<{ email: string; link: string } | null>(null)
  const [revoking, setRevoking] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!canManage) return
    try {
      setInvites(await listInvites(workspace.id))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not load invites.')
    }
  }, [workspace.id, canManage])

  useEffect(() => {
    void load()
  }, [load])

  if (!canManage) {
    return (
      <section aria-labelledby="invites-heading" className="space-y-2">
        <h2 id="invites-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
          Invite people
        </h2>
        <p className="text-sm text-zinc-500">Only owners and admins can invite people to this workspace.</p>
      </section>
    )
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setSending(true)
    setError(null)
    setCreated(null)
    try {
      const res = await createInvite(workspace.id, email.trim(), role)
      setCreated({ email: res.invite.email, link: `${window.location.origin}/invite/${res.token}` })
      setEmail('')
      await load()
      onChanged()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not create the invite.')
    } finally {
      setSending(false)
    }
  }

  async function onRevoke(id: string) {
    setRevoking(id)
    setError(null)
    try {
      await revokeInvite(workspace.id, id)
      await load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not revoke the invite.')
    } finally {
      setRevoking(null)
    }
  }

  return (
    <section aria-labelledby="invites-heading" className="space-y-3">
      <h2 id="invites-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
        Invite people
      </h2>
      <form onSubmit={onSubmit} className="flex flex-wrap items-end gap-3">
        <div className="min-w-[16rem] flex-1">
          <label htmlFor="invite-email" className="block text-sm text-zinc-300">
            Email
          </label>
          <input
            id="invite-email"
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="teammate@company.com"
            disabled={sending}
            className="mt-1 w-full rounded-lg border border-white/10 bg-black/40 px-3 py-2 text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:opacity-60"
          />
        </div>
        <div>
          <label htmlFor="invite-role" className="block text-sm text-zinc-300">
            Role
          </label>
          <select
            id="invite-role"
            value={role}
            onChange={(e) => setRole(e.target.value as WorkspaceRole)}
            disabled={sending}
            className="mt-1 rounded-lg border border-white/10 bg-black/40 px-3 py-2 text-sm outline-none focus:border-accent"
          >
            {assignableRoles(workspace.role)
              .filter((r) => r !== 'owner')
              .map((r) => (
                <option key={r} value={r}>
                  {ROLE_LABEL[r]}
                </option>
              ))}
          </select>
        </div>
        <button
          type="submit"
          disabled={sending || !email.trim()}
          className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {sending ? 'Inviting…' : 'Create invite'}
        </button>
      </form>
      <p className="text-xs text-zinc-500">Helios does not send email. You get a link to share; it works once, for that address, for 7 days.</p>

      {error && (
        <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {error}
        </p>
      )}

      {created && (
        <div role="status" data-testid="invite-link" className="space-y-2 rounded-lg border border-emerald-500/40 bg-emerald-500/10 p-3 text-sm">
          <p className="text-emerald-200">
            Invite created for <span className="font-medium">{created.email}</span>. Copy the link now; it can't be shown again.
          </p>
          <CopyField value={created.link} label="Invite link" />
        </div>
      )}

      {invites && invites.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[520px] text-left text-sm" data-testid="pending-invites-table">
            <caption className="pb-2 text-left text-xs uppercase tracking-wider text-zinc-500">Pending invites</caption>
            <tbody>
              {invites.map((inv) => (
                <tr key={inv.id} className="border-t border-white/5">
                  <td className="px-3 py-2.5 text-zinc-100">{inv.email}</td>
                  <td className="px-3 py-2.5">
                    <span className="rounded bg-white/10 px-2 py-1 text-xs text-zinc-300">{ROLE_LABEL[inv.role]}</span>
                  </td>
                  <td className="px-3 py-2.5 text-xs text-zinc-400">Expires {fmt(inv.expiresAt)}</td>
                  <td className="px-3 py-2.5 text-right">
                    <button
                      onClick={() => void onRevoke(inv.id)}
                      disabled={revoking === inv.id}
                      className="rounded-lg border border-white/10 px-3 py-1.5 text-xs text-zinc-300 transition hover:border-red-500/50 hover:bg-red-500/10 hover:text-red-200 disabled:opacity-50"
                    >
                      {revoking === inv.id ? 'Revoking…' : 'Revoke'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {invites && invites.length === 0 && <p className="text-sm text-zinc-500">No pending invites.</p>}
    </section>
  )
}
