import { useCallback, useEffect, useRef, useState } from 'react'
import { listFlagHistory } from '../api/audit'
import { describeError } from '../lib/errors'
import type { FlagHistoryEntry } from '../types'

const ACTOR_MARK: Record<FlagHistoryEntry['actor']['type'], string> = { user: 'Person', api_token: 'API token', schedule: 'Schedule' }

/** A time with its zone shown, and the exact UTC time one hover away. */
function when(iso: string) {
  const d = new Date(iso)
  return {
    local: d.toLocaleString(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZoneName: 'short' }),
    utc: iso.replace('T', ' ').replace(/\.\d+Z$|Z$/, ' UTC'),
  }
}

/** Who changed this flag, when, and exactly what. Read only. */
export function FlagHistory({ env, flagKey }: { env: string; flagKey: string }) {
  const [entries, setEntries] = useState<FlagHistoryEntry[] | null>(null)
  const [next, setNext] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const seq = useRef(0)

  const load = useCallback(
    async (before?: number) => {
      const id = ++seq.current
      setLoading(true)
      setError(null)
      try {
        const page = await listFlagHistory(env, flagKey, before)
        if (id !== seq.current) return
        setEntries((prev) => (before === undefined ? page.entries : [...(prev ?? []), ...page.entries]))
        setNext(page.nextCursor)
      } catch (err) {
        if (id === seq.current) setError(describeError(err, 'view this flag’s history'))
      } finally {
        if (id === seq.current) setLoading(false)
      }
    },
    [env, flagKey],
  )

  useEffect(() => {
    setEntries(null)
    void load()
    return () => {
      seq.current++
    }
  }, [load])

  return (
    <section aria-labelledby="history-heading" className="mt-8 space-y-3" data-testid="flag-history">
      <h3 id="history-heading" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
        History
      </h3>
      {error && (
        <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          <p>{error}</p>
          <button onClick={() => void load()} className="mt-1 underline">
            Retry
          </button>
        </div>
      )}
      {entries === null && !error && (
        <div role="status" aria-label="Loading history" className="space-y-2">
          <div className="h-10 animate-shimmer rounded bg-white/[0.06]" />
          <div className="h-10 animate-shimmer rounded bg-white/[0.06]" style={{ animationDelay: '0.15s' }} />
        </div>
      )}
      {entries !== null && entries.length === 0 && <p className="text-sm text-zinc-500">No changes recorded for this flag in {env}.</p>}
      {entries !== null && entries.length > 0 && (
        <ol className="space-y-3">
          {entries.map((e) => {
            const t = when(e.createdAt)
            return (
              <li key={e.id} className="rounded-lg border border-white/10 bg-black/20 p-3 text-sm" data-testid="history-entry">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <p className="font-medium text-zinc-100">
                    {e.summary}
                    {e.source && <span className="ml-2 text-xs font-normal text-zinc-400">({e.source})</span>}
                  </p>
                  <time dateTime={e.createdAt} title={t.utc} className="text-xs text-zinc-400">
                    {t.local}
                  </time>
                </div>
                <p className="mt-0.5 text-xs text-zinc-400">
                  <span className="rounded bg-white/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-zinc-300">{ACTOR_MARK[e.actor.type]}</span>{' '}
                  {e.actor.label}
                  {e.actor.onBehalfOf && <span> · on behalf of {e.actor.onBehalfOf}</span>}
                </p>
                {e.changes.length > 0 && (
                  <dl className="mt-2 space-y-1.5">
                    {e.changes.map((c) => (
                      <div key={c.field}>
                        <dt className="text-xs text-zinc-500">{c.label}</dt>
                        <dd className="grid gap-1 sm:grid-cols-2">
                          <ul className="rounded bg-red-500/10 px-2 py-1 font-mono text-xs text-red-200">
                            <li className="text-[10px] uppercase tracking-wide text-red-300/70">Before</li>
                            {(c.before.length ? c.before : ['(nothing)']).map((l, i) => (
                              <li key={i}>{l}</li>
                            ))}
                          </ul>
                          <ul className="rounded bg-emerald-500/10 px-2 py-1 font-mono text-xs text-emerald-200">
                            <li className="text-[10px] uppercase tracking-wide text-emerald-300/70">After</li>
                            {(c.after.length ? c.after : ['(nothing)']).map((l, i) => (
                              <li key={i}>{l}</li>
                            ))}
                          </ul>
                        </dd>
                      </div>
                    ))}
                  </dl>
                )}
              </li>
            )
          })}
        </ol>
      )}
      {next !== null && (
        <button
          onClick={() => void load(next)}
          disabled={loading}
          className="rounded-lg border border-white/10 px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 disabled:opacity-50"
        >
          {loading ? 'Loading…' : 'Load older changes'}
        </button>
      )}
    </section>
  )
}
