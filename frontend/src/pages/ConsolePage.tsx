import { useAuth } from '../auth/AuthProvider'
import { Logo } from '../components/Logo'

// Dot colour per environment key; unknown keys fall back to the accent.
const ENV_DOT: Record<string, string> = {
  dev: 'bg-emerald-400 shadow-[0_0_8px_rgba(52,211,153,0.8)]',
  staging: 'bg-amber-400 shadow-[0_0_8px_rgba(251,191,36,0.8)]',
  production: 'bg-rose-400 shadow-[0_0_8px_rgba(251,113,133,0.8)]',
}

export default function ConsolePage() {
  const { session, me, meLoading, meError, reloadMe, signOut } = useAuth()
  const email = me?.email ?? session?.user.email ?? ''

  return (
    <main className="mx-auto max-w-2xl px-4 py-12">
      <header className="flex animate-fade-up items-center justify-between">
        <h1>
          <Logo className="text-xl" />
        </h1>
        <button
          onClick={() => void signOut()}
          className="rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 backdrop-blur transition hover:border-accent/60 hover:bg-accent/10 hover:text-white"
        >
          Sign out
        </button>
      </header>

      <section
        className="mt-8 animate-fade-up rounded-2xl border border-white/10 bg-surface/70 p-6 shadow-[0_0_80px_-30px_rgba(124,92,255,0.45)] backdrop-blur-xl"
        style={{ animationDelay: '0.1s' }}
      >
        <p className="text-sm text-zinc-400">Signed in as</p>
        <p className="mt-1 text-lg font-medium">{email}</p>

        <h2 className="mt-7 text-xs font-medium uppercase tracking-wider text-zinc-500">Access</h2>

        {meLoading && (
          <div className="mt-3 space-y-2" role="status" aria-label="Loading your access">
            {[0, 1, 2].map((i) => (
              <div
                key={i}
                className="h-10 animate-shimmer rounded-lg bg-white/[0.06]"
                style={{ animationDelay: `${i * 0.15}s` }}
              />
            ))}
          </div>
        )}

        {meError && !meLoading && (
          <div role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            <p>{meError}</p>
            <button onClick={reloadMe} className="mt-2 underline">
              Retry
            </button>
          </div>
        )}

        {me && !meLoading && me.roles.length === 0 && (
          <p className="mt-3 rounded-lg border border-white/10 bg-black/30 px-3 py-2.5 text-sm text-zinc-300">
            This account has no environment access yet. Ask an admin to grant you a role in an environment.
          </p>
        )}

        {me && !meLoading && me.roles.length > 0 && (
          <ul className="mt-3 space-y-2">
            {me.roles.map(({ environment, role }, i) => (
              <li
                key={environment}
                className="flex animate-fade-up items-center justify-between rounded-lg border border-white/10 bg-black/30 px-3.5 py-2.5 text-sm transition hover:border-accent/40"
                style={{ animationDelay: `${0.15 + i * 0.08}s` }}
              >
                <span className="flex items-center gap-2.5 font-mono">
                  <span className={`h-2 w-2 rounded-full ${ENV_DOT[environment] ?? 'bg-accent'}`} />
                  {environment}
                </span>
                <span className="rounded-full bg-accent/15 px-2.5 py-0.5 text-xs text-accent">{role}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  )
}
