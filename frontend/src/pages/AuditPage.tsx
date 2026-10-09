import { useEffect, useMemo, useState } from 'react'
import { NO_FILTERS, type AuditFilters as Filters } from '../api/audit'
import { AccessGate } from '../components/AccessGate'
import { AuditFilters } from '../components/AuditFilters'
import { AuditTable } from '../components/AuditTable'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { EnvSwitcher } from '../components/EnvSwitcher'
import { ProductionStrip } from '../components/ProductionStrip'
import { KNOWN_ACTIONS } from '../lib/auditLabels'
import { useAuditLog } from '../lib/useAuditLog'
import { useConsoleEnv } from '../lib/useConsoleEnv'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'

export default function AuditPage() {
  const { email, me, meLoading, meError, reloadMe, signOut, workspace, roles, env, selectEnv, prod } = useConsoleEnv()
  const [filters, setFilters] = useState<Filters>(NO_FILTERS)
  const { entries, nextCursor, loading, loadingMore, error, moreError, reload, loadMore } = useAuditLog(env, workspace?.id ?? null, filters)
  const slow = useSlowHint(loading || loadingMore)

  // Filter options: every action the backend writes, plus any other seen in results.
  const [seen, setSeen] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (!entries) return
    setSeen((prev) => {
      const next = new Set(prev)
      entries.forEach((e) => next.add(e.action))
      return next.size === prev.size ? prev : next
    })
  }, [entries])
  const actions = useMemo(() => [...new Set([...KNOWN_ACTIONS, ...seen])], [seen])

  const filtered = filters.action !== '' || filters.resourceId !== '' || filters.severity !== ''
  const hasGlobal = entries?.some((e) => e.scope === 'global') ?? false

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
                Audit log in <span className={prod ? 'text-red-300' : 'text-zinc-300'}>{env}</span>
                {workspace && <span className="text-zinc-500"> · {workspace.name}</span>}
              </h2>
              <p className="mt-1 text-xs text-zinc-500">Audit entries are append-only and cannot be edited or deleted.</p>
            </div>

            <AuditFilters value={filters} actions={actions} onChange={setFilters} />

            {slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}

            {error && !loading && (
              <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <p>{error}</p>
                <button onClick={() => void reload()} className="mt-2 underline">
                  Retry
                </button>
              </div>
            )}

            {loading && entries === null && (
              <div className="space-y-2" role="status" aria-label="Loading audit log">
                {[0, 1, 2, 3].map((i) => (
                  <div key={i} className="h-14 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
                ))}
              </div>
            )}

            {entries !== null && entries.length === 0 && !error && (
              <p className="rounded-lg border border-dashed border-white/10 px-4 py-10 text-center text-sm text-zinc-400">
                No audit entries match.
                {filtered && <span className="mt-1 block text-xs text-zinc-500">Try clearing the filters.</span>}
              </p>
            )}

            {entries !== null && entries.length > 0 && (
              <>
                <AuditTable entries={entries} />

                {moreError && (
                  <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                    <p>{moreError}</p>
                    <button onClick={() => void loadMore()} className="mt-2 underline">
                      Retry
                    </button>
                  </div>
                )}

                {nextCursor !== null && (
                  <div className="text-center">
                    <button
                      onClick={() => void loadMore()}
                      disabled={loadingMore}
                      className="rounded-lg border border-white/10 bg-white/[0.03] px-4 py-2 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-60"
                    >
                      {loadingMore ? 'Loading…' : 'Load more'}
                    </button>
                  </div>
                )}

                {hasGlobal && (
                  <p className="text-xs text-zinc-500">Entries marked 'All environments' apply to every environment.</p>
                )}
              </>
            )}
          </div>
        )}
      </ConsoleCard>
    </main>
  )
}
