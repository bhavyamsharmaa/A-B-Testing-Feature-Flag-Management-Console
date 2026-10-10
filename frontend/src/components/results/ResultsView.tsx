import { fmtCount, fmtDate, metricLabel, metricStatusText, peekingText } from '../../lib/resultsFormat'
import type { ExperimentResults } from '../../types'
import { ComparisonBlock } from './ComparisonBlock'
import { MethodDetails } from './MethodDetails'
import { VariationsTable } from './VariationsTable'
import { WarningList } from './WarningList'

/**
 * Everything the results API returned, laid out: warnings first, then the
 * fixed-sample-size note next to the sample size, then each metric. Purely
 * presentational (no fetching), so it can be rendered from fixtures in tests.
 */
export function ResultsView({ data }: { data: ExperimentResults }) {
  const running = data.status === 'running'
  const unreliable = !data.trustworthy

  return (
    <div className="space-y-4" data-testid="results" data-experiment-status={data.status}>
      <WarningList warnings={data.warnings} />

      <section aria-label="Run and sample size" className="rounded-xl border border-white/10 bg-white/[0.03] p-4">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-4">
          <div>
            <dt className="text-xs text-zinc-400">Experiment</dt>
            <dd className="text-zinc-100" data-testid="run-status">{running ? 'Running' : 'Stopped'}</dd>
          </div>
          <div>
            <dt className="text-xs text-zinc-400">Started</dt>
            <dd className="text-zinc-100">{fmtDate(data.window.start)}</dd>
          </div>
          <div>
            <dt className="text-xs text-zinc-400">{running ? 'Until' : 'Stopped'}</dt>
            <dd className="text-zinc-100">{running ? 'still running' : fmtDate(data.window.stoppedAt ?? data.window.end)}</dd>
          </div>
          <div>
            <dt className="text-xs text-zinc-400">Sample size at this reading</dt>
            <dd className="font-mono tabular-nums text-zinc-50" data-testid="sample-size">{fmtCount(data.sampleSize)} exposures</dd>
          </div>
        </dl>
        <p className="mt-3 rounded-lg border border-white/10 bg-white/[0.04] px-3 py-2 text-sm text-zinc-100" data-testid="peeking-note">
          <span aria-hidden="true">ⓘ </span>
          {peekingText(data.peekingNoteCode, data.peekingNote)}
        </p>
        {running && (
          <p className="mt-2 text-xs text-zinc-300">
            This experiment is still running. What you see is a reading, not a verdict: the next reading can say something different.
          </p>
        )}
      </section>

      {data.metrics.map((m, i) => (
        <section
          key={`${m.metric.eventName}:${m.metric.kind}:${i}`}
          aria-label={`Metric ${metricLabel(m.metric)}`}
          data-testid={`metric-${m.metric.eventName}-${m.metric.kind}`}
          className="space-y-3"
        >
          <h3 className="flex flex-wrap items-baseline gap-2 text-sm font-medium text-zinc-100">
            <span className="font-mono">{metricLabel(m.metric)}</span>
            {m.metric.isPrimary && <span className="rounded border border-white/25 px-1.5 text-[11px] uppercase tracking-wider text-zinc-200">Primary</span>}
            {m.metric.name && <span className="text-xs font-normal text-zinc-400">{m.metric.name}</span>}
            <span className="ml-auto text-xs font-normal text-zinc-400">based on {fmtCount(data.sampleSize)} exposures</span>
          </h3>

          {unreliable && (
            <p
              id={`unreliable-${i}`}
              role="note"
              data-testid="dimmed-explanation"
              className="rounded-lg border-2 border-red-400/60 bg-red-500/10 px-3 py-2 text-sm text-red-50"
            >
              <span aria-hidden="true">⚠ </span>
              The numbers below are dimmed because they are not reliable: the sample ratio check failed. They are shown for diagnosis only. Do not use them to decide.
            </p>
          )}

          <div
            className={`space-y-3 ${unreliable ? 'opacity-60 saturate-50' : ''}`}
            data-dimmed={unreliable ? 'true' : 'false'}
            aria-describedby={unreliable ? `unreliable-${i}` : undefined}
          >
            <VariationsTable metric={m} />
            {metricStatusText(m.status, m.statusReasons) && (
              <p
                role="status"
                data-testid="metric-status"
                data-metric-status={m.status}
                className="rounded-lg border border-dashed border-amber-400/50 bg-amber-400/10 px-3 py-2 text-sm text-amber-50"
              >
                <span aria-hidden="true">◌ </span>
                {metricStatusText(m.status, m.statusReasons)}
              </p>
            )}
            {m.comparisons.length === 0 ? (
              <p className="rounded-lg border border-dashed border-white/15 px-4 py-4 text-sm text-zinc-300">
                There is nothing to compare: the flag needs at least two variations.
              </p>
            ) : (
              m.comparisons.map((c) => (
                <ComparisonBlock
                  key={c.variationId}
                  comparison={c}
                  kind={m.metric.kind}
                  family={data.comparisonCount}
                  confidenceLevel={data.confidenceLevel}
                  alphaPerComparison={data.alphaPerComparison}
                  running={running}
                  trustworthy={data.trustworthy}
                />
              ))
            )}
          </div>
        </section>
      ))}

      <MethodDetails data={data} />
    </div>
  )
}
