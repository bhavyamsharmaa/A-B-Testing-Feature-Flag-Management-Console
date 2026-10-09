import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '../../api/client'
import { changeMemberRole, listMembers, removeMember } from '../../api/workspaces'
import { useAuth } from '../../auth/AuthProvider'
import { assignableRoles, canModifyMember, ROLE_LABEL } from '../../lib/permissions'
import type { Member, Workspace, WorkspaceRole } from '../../types'
import { ConfirmDialog } from '../ConfirmDialog'

const fmtDate = (iso: string) => new Date(iso).toLocaleDateString(undefined, { dateStyle: 'medium' })

export function MembersSection({ workspace, reloadKey }: { workspace: Workspace; reloadKey: number }) {
  const { refreshMe } = useAuth()
  const [members, setMembers] = useState<Member[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [removing, setRemoving] = useState<Member | null>(null)
  const [dialogError, setDialogError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      setMembers(await listMembers(workspace.id))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not load members.')
    }
  }, [workspace.id])

  useEffect(() => {
    void load()
  }, [load, reloadKey])

  async function onRole(member: Member, role: WorkspaceRole) {
    setBusyId(member.userId)
    setError(null)
    try {
      await changeMemberRole(workspace.id, member.userId, role)
      await load()
      if (member.isYou) await refreshMe() // your own role may have changed what you can do
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not change the role.')
      await load() // snap the select back to the server's value
    } finally {
      setBusyId(null)
    }
  }

  async function onConfirmRemove() {
    if (!removing) return
    setBusyId(removing.userId)
    setDialogError(null)
    try {
      await removeMember(workspace.id, removing.userId)
      const leaving = removing.isYou
      setRemoving(null)
      if (leaving) await refreshMe()
      else await load()
    } catch (err) {
      setDialogError(err instanceof ApiError ? err.message : 'Could not remove the member.')
    } finally {
      setBusyId(null)
    }
  }

  return (
    <section aria-labelledby="members-heading" className="space-y-3">
      <h2 id="members-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
        Members{members ? ` (${members.length})` : ''}
      </h2>
      {error && (
        <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {error}
        </p>
      )}
      {members === null && !error && <div className="h-24 animate-shimmer rounded-lg bg-white/[0.06]" role="status" aria-label="Loading members" />}
      {members && (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[560px] text-left text-sm" data-testid="members-table">
            <thead>
              <tr className="text-xs uppercase tracking-wider text-zinc-500">
                <th className="px-3 py-2 font-medium">Member</th>
                <th className="px-3 py-2 font-medium">Role</th>
                <th className="px-3 py-2 font-medium">Joined</th>
                <th className="px-3 py-2 text-right font-medium">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {members.map((m) => {
                const editable = canModifyMember(workspace.role, m.role)
                const options = assignableRoles(workspace.role)
                return (
                  <tr key={m.userId} className="border-t border-white/5">
                    <td className="px-3 py-3">
                      <span className="text-zinc-100">{m.email}</span>
                      {m.isYou && <span className="ml-2 rounded bg-white/10 px-1.5 py-0.5 text-[11px] text-zinc-400">You</span>}
                    </td>
                    <td className="px-3 py-3">
                      {editable ? (
                        <select
                          aria-label={`Role of ${m.email}`}
                          value={m.role}
                          disabled={busyId === m.userId}
                          onChange={(e) => void onRole(m, e.target.value as WorkspaceRole)}
                          className="rounded-lg border border-white/10 bg-black/40 px-2.5 py-1.5 text-sm outline-none focus:border-accent disabled:opacity-60"
                        >
                          {(options.includes(m.role) ? options : [m.role, ...options]).map((r) => (
                            <option key={r} value={r}>
                              {ROLE_LABEL[r]}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="rounded bg-white/10 px-2 py-1 text-xs text-zinc-300">{ROLE_LABEL[m.role]}</span>
                      )}
                    </td>
                    <td className="px-3 py-3 text-xs text-zinc-400">{fmtDate(m.joinedAt)}</td>
                    <td className="px-3 py-3 text-right">
                      {m.isYou ? (
                        <button
                          onClick={() => {
                            setDialogError(null)
                            setRemoving(m)
                          }}
                          className="rounded-lg border border-white/10 px-3 py-1.5 text-xs text-zinc-300 transition hover:border-red-500/50 hover:bg-red-500/10 hover:text-red-200"
                        >
                          Leave
                        </button>
                      ) : editable ? (
                        <button
                          onClick={() => {
                            setDialogError(null)
                            setRemoving(m)
                          }}
                          className="rounded-lg border border-red-500/50 bg-red-500/10 px-3 py-1.5 text-xs font-medium text-red-300 transition hover:bg-red-600 hover:text-white"
                        >
                          Remove
                        </button>
                      ) : null}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {removing && (
        <ConfirmDialog
          title={removing.isYou ? `Leave ${workspace.name}?` : `Remove ${removing.email}?`}
          confirmLabel={removing.isYou ? 'Leave workspace' : 'Remove member'}
          tone="danger"
          busy={busyId === removing.userId}
          error={dialogError}
          slowHint={null}
          onConfirm={() => void onConfirmRemove()}
          onCancel={() => setRemoving(null)}
        >
          <p>
            {removing.isYou
              ? 'You will lose access to this workspace and everything in it right away. You can only rejoin with a new invite.'
              : `${removing.email} loses access to this workspace right away.`}
          </p>
        </ConfirmDialog>
      )}
    </section>
  )
}
