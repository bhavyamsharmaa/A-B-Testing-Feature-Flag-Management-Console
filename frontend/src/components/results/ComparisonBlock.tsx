import {
  DASH, differenceFormatter, fmtInterval, fmtLevel, fmtP, fmtSignedPercent, missingReason, statusInfo,
} from '../../lib/resultsFormat'
import type { MetricKind, ResultsComparison } from '../../types'
import { IntervalPlot } from './IntervalPlot'
import { StatusBadge } from './StatusBadge'

interface Props {
  comparison: ResultsComparison
  kind: MetricKind
  /** Number of comparisons in the family (Bonferroni): the response's comparisonCount. */
  family: number
  /** The level of every interval in the response (its top-level confidenceLevel). */
  confidenceLevel: number
  /** Alpha each comparison was tested at (its top-level alphaPerComparison). */
  alphaPerComparison: number
  /** False while the experiment is stopped: a verdict is no longer provisional. */
  running: boolean
  /** False when the sample ratio check failed: nothing here can be relied on. */
  trustworthy: boolean
}

/** One variation against the control. */
export function ComparisonBlock({ comparison: c, kind, family, confidenceLevel, alphaPerComparison, running, trustworthy }: Props) {
  const info = statusInfo(c.status, trustworthy, running)
  const fmtDiff = differenceFormatter(kind)
  const level = fmtLevel(confidenceLevel)
  const alpha = fmtLevel(alphaPerComparison)
  // "Insufficient data" is said once for the metric (above the comparisons), not repeated here.
  const insufficient = c.status === 'insufficient_data' && trustworthy

  // A value that is missing is a dash, with the reason in words (tooltip, screen-reader text, and listed below).
  const reasons = new Set<string>()
  const cell = (value: string, field: string) => {
    if (value !== DASH) return value
    const why = missingReason(c, field)
    if (why && !insufficient) reasons.add(why)
    return (
      <span title={why ?? 'not available'} tabIndex={0}>
        {DASH}
        <span className="sr-only"> ({why ?? 'not available'})</span>
      </span>
    )
  }

  return (
    <section
      aria-label={`${c.variationId} compared with ${c.against}`}
      data-testid={`comparison-${c.variationId}`}
      data-status={info.key}
      className="rounded-xl border border-white/10 bg-white/[0.03] p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-sm font-medium text-zinc-100">
          <span className="font-mono">{c.variationId}</span> compared with <span className="font-mono">{c.against}</span>
        </h4>
        <StatusBadge info={info} />
      </div>
      {!insufficient && <p className="mt-1.5 text-xs text-zinc-300">{info.detail}</p>}

      <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-3 text-sm sm:grid-cols-4">
        <Stat label={kind === 'conversion' ? 'Absolute lift' : 'Difference in means'} testid="abs-lift">
          {cell(fmtDiff(c.difference), 'difference')}
        </Stat>
        <Stat label="Relative lift" testid="rel-lift">
          {cell(fmtSignedPercent(c.relativeLift), 'relativeLift')}
        </Stat>
        <Stat label="p-value (unadjusted)" testid="p-raw">
          {cell(fmtP(c.pValue), 'pValue')}
        </Stat>
        <Stat label={family > 1 ? `p-value adjusted for ${family} comparisons` : 'p-value adjusted'} testid="p-adjusted">
          {cell(fmtP(c.adjustedPValue), 'pValue')}
        </Stat>
        <Stat label={`${level} interval, absolute`} testid="abs-interval" wide>
          {cell(fmtInterval(c.differenceInterval.low, c.differenceInterval.high, fmtDiff), 'differenceInterval')}
        </Stat>
        <Stat label={`${level} interval, relative`} testid="rel-interval" wide>
          {cell(fmtInterval(c.relativeLiftInterval.low, c.relativeLiftInterval.high, fmtSignedPercent), 'relativeLiftInterval')}
        </Stat>
      </dl>

      <div className="mt-3">
        <IntervalPlot
          title={`${kind === 'conversion' ? 'Absolute lift' : 'Difference in means'}, ${level} interval`}
          estimate={c.difference}
          low={c.differenceInterval.low}
          high={c.differenceInterval.high}
          format={fmtDiff}
        />
      </div>

      <p className="mt-3 text-xs text-zinc-300" data-testid="adjustment-note">
        {family > 1
          ? level === DASH || alpha === DASH
            ? `Bonferroni adjustment applied for ${family} comparisons: each is tested more strictly than 5%, so the intervals are wider than 95% and the adjusted p-value is the unadjusted one multiplied by ${family}.`
            : `Bonferroni adjustment applied: with ${family} comparisons each is tested at ${alpha} instead of 5%, so the intervals are ${level} (wider than 95%) and the adjusted p-value is the unadjusted one multiplied by ${family}.`
          : 'One comparison, so no adjustment was needed: the interval is 95%.'}
      </p>
      {reasons.size > 0 && (
        <p className="mt-2 text-xs text-zinc-300" data-testid="missing-reasons">
          <span className="font-medium">Why some values show {DASH}: </span>
          {[...reasons].join('; ')}.
        </p>
      )}
    </section>
  )
}

function Stat({ label, testid, children, wide }: { label: string; testid: string; children: React.ReactNode; wide?: boolean }) {
  return (
    <div className={wide ? 'col-span-2' : ''}>
      <dt className="text-xs text-zinc-400">{label}</dt>
      <dd className="mt-0.5 font-mono tabular-nums text-zinc-50" data-testid={testid}>
        {children}
      </dd>
    </div>
  )
}
