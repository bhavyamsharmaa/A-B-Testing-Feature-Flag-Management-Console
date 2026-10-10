import { DASH, fmtCount, fmtNumber, fmtPercent } from '../../lib/resultsFormat'
import type { ResultsMetric } from '../../types'

/** Per-variation numbers of one metric. The control row is marked in text. */
export function VariationsTable({ metric }: { metric: ResultsMetric }) {
  const conversion = metric.metric.kind === 'conversion'
  return (
    <div className="overflow-x-auto" tabIndex={0} aria-label="Per-variation numbers (scrolls sideways on narrow screens)">
      <table className="w-full min-w-[520px] text-left text-sm" data-testid="variations-table">
        <caption className="sr-only">Exposures and outcomes per variation</caption>
        <thead>
          <tr className="text-xs uppercase tracking-wider text-zinc-400">
            <th scope="col" className="px-3 py-2 font-medium">Variation</th>
            <th scope="col" className="px-3 py-2 text-right font-medium">Exposures</th>
            <th scope="col" className="px-3 py-2 text-right font-medium">Converting subjects</th>
            {conversion ? (
              <th scope="col" className="px-3 py-2 text-right font-medium">Conversion rate</th>
            ) : (
              <>
                <th scope="col" className="px-3 py-2 text-right font-medium">Mean per subject</th>
                <th scope="col" className="px-3 py-2 text-right font-medium">Standard deviation</th>
              </>
            )}
          </tr>
        </thead>
        <tbody>
          {metric.variations.map((v) => (
            <tr key={v.variationId} className="border-t border-white/5" data-testid={`row-${v.variationId}`}>
              <th scope="row" className="px-3 py-3 font-normal">
                <span className="font-mono text-[13px] text-zinc-100">{v.variationId}</span>
                {v.isControl && (
                  <span
                    className="ml-2 rounded border border-white/25 px-1.5 py-0.5 text-[11px] uppercase tracking-wider text-zinc-200"
                    title="The control is the flag's first variation; every other variation is compared with it."
                  >
                    Control
                  </span>
                )}
              </th>
              <td className="px-3 py-3 text-right font-mono tabular-nums text-zinc-100" data-testid="exposures">{fmtCount(v.exposures)}</td>
              <td className="px-3 py-3 text-right font-mono tabular-nums text-zinc-100" data-testid="converters">{fmtCount(v.convertingSubjects)}</td>
              {conversion ? (
                <td className="px-3 py-3 text-right font-mono tabular-nums text-zinc-100" data-testid="rate">
                  {v.rate === null ? <Missing why="no exposures yet" /> : fmtPercent(v.rate)}
                </td>
              ) : (
                <>
                  <td className="px-3 py-3 text-right font-mono tabular-nums text-zinc-100" data-testid="mean">
                    {v.mean === null ? <Missing why="no exposures yet" /> : fmtNumber(v.mean)}
                  </td>
                  <td className="px-3 py-3 text-right font-mono tabular-nums text-zinc-100" data-testid="stddev">
                    {v.stdDev === null ? <Missing why="fewer than 2 exposures" /> : fmtNumber(v.stdDev)}
                  </td>
                </>
              )}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-1 px-3 text-xs text-zinc-400">The control is the flag's first variation. Exposures are subjects, counted once each.</p>
    </div>
  )
}

function Missing({ why }: { why: string }) {
  return (
    <span title={why} tabIndex={0}>
      {DASH}
      <span className="sr-only"> ({why})</span>
    </span>
  )
}
