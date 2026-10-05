import { useAuth } from '../auth/AuthProvider'

export default function ConsolePage() {
  const { session, me, meLoading, meError, reloadMe, signOut } = useAuth()
  const email = me?.email ?? session?.user.email ?? ''

  return (
    <main className="mx-auto max-w-2xl px-4 py-12">
      <header className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">
          <span className="text-accent">Helios</span> console
        </h1>
        <button
          onClick={() => void signOut()}
          className="rounded-md border border-border px-3 py-1.5 text-sm text-zinc-300 transition hover:border-accent hover:text-white"
        >
          Sign out
        </button>
      </header>

      <section className="mt-8 rounded-xl border border-border bg-surface p-6">
        <p className="text-sm text-zinc-400">Signed in as</p>
        <p className="mt-1 font-medium">{email}</p>

        <h2 className="mt-6 text-sm font-medium text-zinc-300">Access</h2>

        {meLoading && <p className="mt-2 text-sm text-zinc-400">Loading your access…</p>}

        {meError && !meLoading && (
          <div role="alert" className="mt-2 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            <p>{meError}</p>
            <button onClick={reloadMe} className="mt-2 underline">
              Retry
            </button>
          </div>
        )}

        {me && !meLoading && me.roles.length === 0 && (
          <p className="mt-2 rounded-md border border-border bg-bg px-3 py-2 text-sm text-zinc-300">
            This account has no environment access yet. Ask an admin to grant you a role in an environment.
          </p>
        )}

        {me && !meLoading && me.roles.length > 0 && (
          <ul className="mt-2 divide-y divide-border rounded-md border border-border">
            {me.roles.map(({ environment, role }) => (
              <li key={environment} className="flex items-center justify-between px-3 py-2 text-sm">
                <span className="font-mono">{environment}</span>
                <span className="rounded-full bg-accent/15 px-2 py-0.5 text-xs text-accent">{role}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  )
}
