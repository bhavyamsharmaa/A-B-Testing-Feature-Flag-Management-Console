import { Link, NavLink, useParams } from 'react-router-dom'
import { AccessGate } from '../components/AccessGate'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { EnvSwitcher } from '../components/EnvSwitcher'
import { ExperimentCard } from '../components/ExperimentCard'
import { ProductionStrip } from '../components/ProductionStrip'
import { ResultsTab } from '../components/results/ResultsTab'
import { useConsoleEnv } from '../lib/useConsoleEnv'
import { useExperiments } from '../lib/useExperiments'

const tab = ({ isActive }: { isActive: boolean }) =>
  `rounded-lg px-3 py-1.5 text-sm transition ${
    isActive ? 'bg-accent/20 text-white' : 'text-zinc-300 hover:bg-white/5 hover:text-white'
  }`

/** One experiment: its data arriving (Data tab) and its results (Results tab). Read-only for every role. */
export default function ExperimentPage({ tab: active }: { tab: 'data' | 'results' }) {
  const key = useParams().key ?? ''
  const { email, me, meLoading, meError, reloadMe, signOut, workspace, roles, env, selectEnv, prod } = useConsoleEnv()
  const { experiments, loading, error, reload } = useExperiments(env, workspace?.id ?? null)
  const experiment = experiments?.find((x) => x.key === key) ?? null
  const base = `/console/experiments/${encodeURIComponent(key)}`

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <ProductionStrip show={prod} />
      <ConsoleHeader email={email} onSignOut={() => void signOut()} />

      <ConsoleCard prod={prod}>
        <AccessGate me={me} meLoading={meLoading} meError={meError} reloadMe={reloadMe} roleCount={roles.length} />

        {env && roles.length > 0 && (
          <div className="space-y-4">
            <EnvSwitcher roles={roles} selected={env} onSelect={selectEnv} onRefresh={() => void reload()} refreshing={loading} />

            <Link to="/console/experiments" className="inline-block text-sm text-zinc-300 underline-offset-2 hover:text-white hover:underline">
              ← All experiments
            </Link>

            {error && (
              <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <p>{error}</p>
                <button onClick={() => void reload()} className="mt-2 underline">Retry</button>
              </div>
            )}

            {loading && experiments === null && <div className="h-24 animate-shimmer rounded-xl bg-white/[0.06]" role="status" aria-label="Loading experiment" />}

            {experiments !== null && !experiment && (
              <p role="status" data-testid="experiment-not-found" className="rounded-lg border border-dashed border-white/15 px-4 py-10 text-center text-sm text-zinc-300">
                <span className="block font-medium text-zinc-100">Experiment not found</span>
                There is no experiment with this key in this environment.
              </p>
            )}

            {experiment && workspace && (
              <>
                <div>
                  <h2 className="text-lg font-medium text-zinc-50">{experiment.name}</h2>
                  <p className="mt-0.5 font-mono text-[12px] text-zinc-400">{experiment.key} · flag {experiment.flagKey}</p>
                </div>
                <nav aria-label="Experiment sections" className="flex gap-1">
                  <NavLink to={base} end className={tab}>Data</NavLink>
                  <NavLink to={`${base}/results`} className={tab}>Results</NavLink>
                </nav>
                {active === 'data' ? (
                  <ExperimentCard experiment={experiment} env={env} />
                ) : (
                  <ResultsTab experiment={experiment} env={env} workspaceId={workspace.id} />
                )}
              </>
            )}
          </div>
        )}
      </ConsoleCard>
    </main>
  )
}
