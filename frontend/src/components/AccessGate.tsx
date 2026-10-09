import type { Me } from '../types'

interface Props {
  me: Me | null
  meLoading: boolean
  meError: string | null
  reloadMe: () => void
  roleCount: number
}

/** The "loading your access / failed / no access" states shared by every console page. */
export function AccessGate({ me, meLoading, meError, reloadMe, roleCount }: Props) {
  return (
    <>
      {meLoading && (
        <div className="space-y-2" role="status" aria-label="Loading your access">
          <div className="h-10 animate-shimmer rounded-lg bg-white/[0.06]" />
          <div className="h-10 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: '0.15s' }} />
        </div>
      )}

      {meError && !meLoading && (
        <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          <p>{meError}</p>
          <button onClick={reloadMe} className="mt-2 underline">
            Retry
          </button>
        </div>
      )}

      {me && !meLoading && roleCount === 0 && (
        <p className="rounded-lg border border-white/10 bg-black/30 px-3 py-2.5 text-sm text-zinc-300">
          This workspace has no environments you can see yet.
        </p>
      )}
    </>
  )
}
