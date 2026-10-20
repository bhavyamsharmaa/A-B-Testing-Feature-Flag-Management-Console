import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '../../api/client'
import { cancelOffer, getOpenOffer, offerOwnership } from '../../api/ownership'
import { listMembers } from '../../api/workspaces'
import type { Member, OwnershipOffer, Workspace } from '../../types'

const stamp = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short', timeZoneName: 'short' })

/** Hand the workspace to an admin. Owners only. Shows the open offer and lets the owner cancel it. */
export function OwnershipSection({ workspace, reloadKey }: { workspace: Workspace; reloadKey: number }) {
  const [offer, setOffer] = useState<OwnershipOffer | null | undefined>(undefined)
  const [admins, setAdmins] = useState<Member[]>([])
  const [target, setTarget] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const [o, members] = await Promise.all([getOpenOffer(workspace.id), listMembers(workspace.id)])
      setOffer(o)
      setAdmins(members.filter((m) => m.role === 'admin'))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not load the ownership state.')
    }
  }, [workspace.id])

  useEffect(() => {
    void load()
  }, [load, reloadKey])

  if (workspace.role !== 'owner') return null

  async function run(fn: () => Promise<unknown>) {
    setBusy(true)
    setError(null)
    try {
      await fn()
      await load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'That did not work. Try again.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section aria-labelledby="ownership-heading" className="space-y-3" data-testid="ownership-section">
      <h2 id="ownership-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
        Ownership
      </h2>
      {offer ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-white/10 bg-black/30 px-3 py-3 text-sm" data-testid="ownership-status">
          <p className="text-zinc-300">
            Waiting for <span className="font-medium text-white">{offer.toEmail}</span> to accept ownership. The offer expires {stamp(offer.expiresAt)}.
          </p>
          <button
            onClick={() => void run(() => cancelOffer(workspace.id, offer.id))}
            disabled={busy}
            className="rounded-lg border border-white/10 px-3 py-1.5 text-xs text-zinc-300 transition hover:border-red-500/50 hover:bg-red-500/10 hover:text-red-200 disabled:opacity-50"
          >
            Cancel offer
          </button>
        </div>
      ) : (
        <>
          <p className="max-w-2xl text-sm text-zinc-400" data-testid="ownership-status">
            You are an owner. To hand the workspace over, offer ownership to one of its admins. They must accept (the offer lasts 7 days); you then become an admin. An
            owner cannot leave a workspace until another owner exists, so transfer first.
          </p>
          {offer === null && admins.length === 0 && <p className="text-sm text-zinc-500">There are no admins to offer it to. Make someone an admin first.</p>}
          {admins.length > 0 && (
            <div className="flex flex-wrap items-end gap-3">
              <div>
                <label htmlFor="ownership-target" className="block text-sm text-zinc-300">
                  New owner
                </label>
                <select
                  id="ownership-target"
                  value={target}
                  onChange={(e) => setTarget(e.target.value)}
                  disabled={busy}
                  className="mt-1 min-w-[16rem] rounded-lg border border-white/10 bg-black/40 px-3 py-2 text-sm outline-none focus:border-accent"
                >
                  <option value="">Choose an admin…</option>
                  {admins.map((a) => (
                    <option key={a.userId} value={a.userId}>
                      {a.email}
                    </option>
                  ))}
                </select>
              </div>
              <button
                onClick={() => void run(() => offerOwnership(workspace.id, target))}
                disabled={busy || !target}
                className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-200 transition hover:border-accent/60 hover:bg-accent/10 disabled:cursor-not-allowed disabled:opacity-50"
              >
                {busy ? 'Offering…' : 'Offer ownership'}
              </button>
            </div>
          )}
        </>
      )}
      {error && (
        <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {error}
        </p>
      )}
    </section>
  )
}
