import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useEventCounts } from '../../lib/useEventCounts'
import { useResults } from '../../lib/useResults'
import type { Experiment, MetricKind, MetricSelection } from '../../types'
import { MetricPicker, MAX_METRICS, type MetricOption } from './MetricPicker'
import { ResultsView } from './ResultsView'

const KINDS: MetricKind[] = ['conversion', 'count', 'value']

/** metrics=purchase:conversion,clicks:count in the address bar, so a reload or a shared link keeps the selection. */
function parseMetrics(raw: string | null): MetricSelection[] {
  const out: MetricSelection[] = []
  for (const part of (raw ?? '').split(',')) {
    const i = part.lastIndexOf(':')
    if (i < 1) continue
    const eventName = part.slice(0, i)
    const kind = part.slice(i + 1) as MetricKind
    if (KINDS.includes(kind) && !out.some((m) => m.eventName === eventName && m.kind === kind)) out.push({ eventName, kind })
  }
  return out.slice(0, MAX_METRICS)
}

interface Props {
  experiment: Experiment
  env: string
  workspaceId: string
}

/** The Results tab: metric selection, then the results or the reason there are none. Read-only for every role. */
export function ResultsTab({ experiment, env, workspaceId }: Props) {
  const [params, setParams] = useSearchParams()
  const rawMetrics = params.get('metrics')
  const metrics = useMemo(() => parseMetrics(rawMetrics), [rawMetrics])
  const setMetrics = (next: MetricSelection[]) => {
    const p = new URLSearchParams(params)
    if (next.length === 0) p.delete('metrics')
    else p.set('metrics', next.map((m) => `${m.eventName}:${m.kind}`).join(','))
    setParams(p, { replace: true })
  }

  const { state, retry } = useResults(workspaceId, env, experiment.key, metrics)
  const { counts } = useEventCounts(env, experiment.key, experiment.status === 'running')

  const options = useMemo<MetricOption[]>(() => {
    const byName = new Map<string, MetricOption>()
    for (const m of experiment.metrics) {
      byName.set(m.eventName, { eventName: m.eventName, defaultKind: m.type, hint: m.isPrimary ? 'primary metric' : 'defined metric' })
    }
    for (const e of counts?.eventNames ?? []) {
      if (!byName.has(e.name)) byName.set(e.name, { eventName: e.name, defaultKind: 'conversion', hint: `${e.count.toLocaleString('en-US')} events` })
    }
    return [...byName.values()]
  }, [experiment.metrics, counts])
  const primary = experiment.metrics.find((m) => m.isPrimary)
  const primaryLabel = primary ? `${primary.eventName} · ${primary.type === 'conversion' ? 'conversion' : 'value per subject'}` : null

  return (
    <div className="space-y-4" data-testid="results-tab">
      <MetricPicker selected={metrics} options={options} primaryLabel={primaryLabel} onChange={setMetrics} />

      {state.kind === 'loading' && (
        <div className="space-y-2" role="status" aria-label="Loading results">
          {[0, 1, 2].map((i) => (
            <div key={i} className="h-24 animate-shimmer rounded-xl bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
          ))}
        </div>
      )}

      {state.kind === 'draft' && (
        <Empty testid="state-draft" title="Start the experiment to see results">
          This experiment is a draft. Results appear once it is running and your app reports exposures.
        </Empty>
      )}
      {state.kind === 'notFound' && (
        <Empty testid="state-not-found" title="Experiment not found">
          There is no experiment with this key in this environment.
        </Empty>
      )}
      {state.kind === 'metricGone' && (
        <Empty testid="state-metric-gone" title="A chosen metric has no data for this experiment">
          One of the selected events no longer exists for this experiment.{' '}
          <button onClick={() => setMetrics([])} className="underline">Use the primary metric</button>
        </Empty>
      )}
      {state.kind === 'timeout' && (
        <Empty testid="state-timeout" title="This is taking too long">
          Computing these results timed out. Try fewer metrics, or try again in a moment.{' '}
          <button onClick={retry} className="underline">Try again</button>
        </Empty>
      )}
      {state.kind === 'rateLimited' && (
        <Empty testid="state-rate-limited" title="Refreshing slower for a moment">
          Too many reads in a short time. Waiting {state.retryAfter ? `about ${state.retryAfter} seconds` : 'a little'} before trying again; nothing is wrong.
        </Empty>
      )}
      {state.kind === 'network' && (
        <Empty testid="state-network" title="Could not reach the server">
          {state.message} <button onClick={retry} className="underline">Retry</button>
        </Empty>
      )}
      {state.kind === 'error' && (
        <Empty testid="state-error" title="Could not load results">
          {state.message} <button onClick={retry} className="underline">Retry</button>
        </Empty>
      )}

      {state.kind === 'ready' && (
        <>
          {state.slow && (
            <p role="status" data-testid="note-slow" className="rounded-lg border border-white/10 bg-white/[0.03] px-3 py-2 text-xs text-zinc-300">
              Refreshing slower for a moment to stay within the request limit. The numbers below are from the last reading.
            </p>
          )}
          {state.stale && (
            <p role="status" data-testid="note-stale" className="rounded-lg border border-amber-400/30 bg-amber-400/10 px-3 py-2 text-xs text-amber-100">
              Could not refresh just now; these numbers are from the last reading. <button onClick={retry} className="underline">Retry</button>
            </p>
          )}
          {state.data.sampleSize === 0 && (
            <Empty testid="state-no-data" title="No data yet">
              No exposures have been recorded for this experiment yet. Numbers appear as soon as your app reports them.
            </Empty>
          )}
          <ResultsView data={state.data} />
        </>
      )}
    </div>
  )
}

function Empty({ title, children, testid }: { title: string; children: React.ReactNode; testid: string }) {
  return (
    <div role="status" data-testid={testid} className="rounded-xl border border-dashed border-white/15 px-4 py-8 text-center">
      <p className="text-sm font-medium text-zinc-100">{title}</p>
      <p className="mt-1 text-sm text-zinc-300">{children}</p>
    </div>
  )
}
