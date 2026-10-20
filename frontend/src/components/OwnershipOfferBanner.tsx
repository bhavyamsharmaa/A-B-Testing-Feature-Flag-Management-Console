import { useState } from 'react'
import { ApiError } from '../api/client'
import { acceptOffer, declineOffer } from '../api/ownership'
import { useAuth } from '../auth/AuthProvider'
import { useWorkspace } from '../workspace/WorkspaceProvider'

const stamp = (iso: string) => new Date(iso).toLocaleDateString(undefined, { dateStyle: 'medium' })

/** Offers of ownership addressed to the signed-in user, with Accept and Decline. Accepting makes you the owner; the offerer becomes an admin. */
export function OwnershipOfferBanner() {
  const { ownershipOffers, activate } = useWorkspace()
  const { refreshMe } = useAuth()
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  if (ownershipOffers.length === 0) return null

  async function answer(id: string, accept: boolean) {
    setBusy(id)
    setError(null)
    try {
      if (accept) {
        const { workspaceId } = await acceptOffer(id)
        await refreshMe()
        await activate(workspaceId)
      } else {
        await declineOffer(id)
        await refreshMe()
      }
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not answer the offer. Try again.')
    } finally {
      setBusy(null)
    }
  }

  return (
    <section aria-label="Offers of ownership" className="mb-4 space-y-2" data-testid="ownership-offers">
      {ownershipOffers.map((o) => (
        <div key={o.id} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm">
          <p className="max-w-2xl text-zinc-200">
            <span className="font-medium text-white">{o.fromEmail || 'The owner'}</span> offers you ownership of <span className="font-medium text-white">{o.workspaceName}</span>.
            If you accept, you become its owner and they become an admin. The offer expires {stamp(o.expiresAt)}.
          </p>
          <div className="flex gap-2">
            <button
              onClick={() => void answer(o.id, true)}
              disabled={busy !== null}
              className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-3.5 py-1.5 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {busy === o.id ? 'Working…' : 'Accept ownership'}
            </button>
            <button
              onClick={() => void answer(o.id, false)}
              disabled={busy !== null}
              className="rounded-lg border border-white/10 px-3.5 py-1.5 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50"
            >
              Decline
            </button>
          </div>
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
