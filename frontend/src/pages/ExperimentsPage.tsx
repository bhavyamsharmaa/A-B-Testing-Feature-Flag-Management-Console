import { AccessGate } from '../components/AccessGate'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { EnvSwitcher } from '../components/EnvSwitcher'
import { ExperimentCard } from '../components/ExperimentCard'
import { ProductionStrip } from '../components/ProductionStrip'
import { useConsoleEnv } from '../lib/useConsoleEnv'
import { useExperiments } from '../lib/useExperiments'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'

/** Experiments of the active environment and the data arriving for each. Read-only; results come later. */
export default function ExperimentsPage() {
  const { email, me, meLoading, meError, reloadMe, signOut, workspace, roles, env, selectEnv, prod } = useConsoleEnv()
  const { experiments, loading, error, reload } = useExperiments(env, workspace?.id ?? null)
  const slow = useSlowHint(loading)

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <ProductionStrip show={prod} />
      <ConsoleHeader email={email} onSignOut={() => void signOut()} />

      <ConsoleCard prod={prod}>
        <AccessGate me={me} meLoading={meLoading} meError={meError} reloadMe={reloadMe} roleCount={roles.length} />

        {env && roles.length > 0 && (
          <div className="space-y-4">
            <EnvSwitcher roles={roles} selected={env} onSelect={selectEnv} onRefresh={() => void reload()} refreshing={loading} />

            <div>
              <h2 className="text-xs font-medium uppercase tracking-wider text-zinc-500">
                Experiments in <span className={prod ? 'text-red-300' : 'text-zinc-300'}>{env}</span>
              </h2>
              <p className="mt-1 text-xs text-zinc-500">Data collected so far. Results and statistics are not shown yet.</p>
            </div>

            {error && (
              <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <p>{error}</p>
                <button onClick={() => void reload()} className="mt-2 underline">
                  Retry
                </button>
              </div>
            )}

            {loading && experiments === null && (
              <div className="space-y-2" role="status" aria-label="Loading experiments">
                {[0, 1].map((i) => (
                  <div key={i} className="h-28 animate-shimmer rounded-xl bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
                ))}
                {slow && <p className="text-xs text-zinc-500">{SLOW_HINT}</p>}
              </div>
            )}

            {experiments !== null && experiments.length === 0 && !error && (
              <p className="rounded-lg border border-dashed border-white/10 px-4 py-10 text-center text-sm text-zinc-400">
                No experiments in this environment yet.
              </p>
            )}

            {experiments !== null && experiments.length > 0 && (
              <div className="space-y-3">
                {experiments.map((x) => (
                  <ExperimentCard key={x.id} experiment={x} env={env} />
                ))}
              </div>
            )}
          </div>
        )}
      </ConsoleCard>
    </main>
  )
}
