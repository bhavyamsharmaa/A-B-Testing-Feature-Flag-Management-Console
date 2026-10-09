import { Link } from 'react-router-dom'

interface Props {
  workspaceName: string
  canCreate: boolean
  canManage: boolean
  onCreate: () => void
}

/** What a brand-new workspace shows instead of an empty table. */
export function OnboardingCard({ workspaceName, canCreate, canManage, onCreate }: Props) {
  if (!canCreate) {
    return (
      <p className="rounded-lg border border-dashed border-white/10 px-4 py-10 text-center text-sm text-zinc-400">
        No flags in this environment yet. Someone with edit access can create the first one.
      </p>
    )
  }
  return (
    <div data-testid="onboarding" className="rounded-xl border border-dashed border-accent/40 bg-accent/[0.04] px-6 py-9 text-center">
      <p className="text-xs font-medium uppercase tracking-wider text-accent">Welcome to {workspaceName}</p>
      <h3 className="mt-2 text-xl font-semibold text-white">Create your first flag</h3>
      <p className="mx-auto mt-2 max-w-md text-sm text-zinc-400">
        This workspace is private to you and the people you invite. A flag lets you turn a feature on or off, or roll it out to
        a percentage of users, without deploying.
      </p>
      <button
        onClick={onCreate}
        className="mt-5 rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-5 py-2.5 text-sm font-medium text-white shadow-[0_8px_24px_-8px_rgba(124,92,255,0.8)] transition hover:brightness-110"
      >
        Create your first flag
      </button>
      <ol className="mx-auto mt-7 grid max-w-xl gap-3 text-left text-sm text-zinc-400 sm:grid-cols-3">
        <li className="rounded-lg border border-white/10 bg-black/20 p-3">
          <span className="text-xs text-accent">1</span>
          <p className="mt-1 text-zinc-200">Create a flag</p>
          <p className="text-xs">It starts disabled in every environment.</p>
        </li>
        <li className="rounded-lg border border-white/10 bg-black/20 p-3">
          <span className="text-xs text-accent">2</span>
          <p className="mt-1 text-zinc-200">Get an SDK key</p>
          <p className="text-xs">
            {canManage ? (
              <>
                In <Link to="/console/settings" className="text-accent underline">Settings</Link>.
              </>
            ) : (
              'Ask an admin for one.'
            )}
          </p>
        </li>
        <li className="rounded-lg border border-white/10 bg-black/20 p-3">
          <span className="text-xs text-accent">3</span>
          <p className="mt-1 text-zinc-200">Evaluate it</p>
          <p className="text-xs">Call the flag from your app with that key.</p>
        </li>
      </ol>
    </div>
  )
}
