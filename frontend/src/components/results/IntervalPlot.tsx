interface Props {
  /** What the plot shows, e.g. "Difference from control (97.5% interval)". */
  title: string
  estimate: number | null
  low: number | null
  high: number | null
  format: (n: number | null | undefined) => string
}

const W = 360
const H = 74
const PAD = 22

const ok = (n: number | null): n is number => typeof n === 'number' && Number.isFinite(n)

/**
 * A horizontal interval plot: the estimate (a diamond) with a whiskered
 * interval on a labelled axis with a dashed zero line. The same numbers are
 * printed as text underneath, so nothing depends on seeing the drawing.
 */
export function IntervalPlot({ title, estimate, low, high, format }: Props) {
  const drawable = ok(estimate) && ok(low) && ok(high)
  const summary = drawable
    ? `${title}: estimate ${format(estimate)}, interval ${format(low)} to ${format(high)}, ${low <= 0 && high >= 0 ? 'includes zero' : 'does not include zero'}.`
    : `${title}: not available.`

  if (!drawable) {
    return (
      <figure className="m-0" data-testid="interval-plot" data-drawn="false">
        <figcaption className="text-xs text-zinc-400">{summary}</figcaption>
      </figure>
    )
  }

  const lo = Math.min(low, 0, estimate)
  const hi = Math.max(high, 0, estimate)
  const span = hi - lo || 1
  const min = lo - span * 0.12
  const max = hi + span * 0.12
  const x = (v: number) => PAD + ((v - min) / (max - min)) * (W - 2 * PAD)
  const y = 30

  return (
    <figure className="m-0" data-testid="interval-plot" data-drawn="true">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={summary} className="w-full max-w-md" preserveAspectRatio="xMidYMid meet">
        {/* axis */}
        <line x1={PAD} x2={W - PAD} y1={y + 16} y2={y + 16} stroke="#52525b" strokeWidth="1" />
        {/* zero line, labelled */}
        <line x1={x(0)} x2={x(0)} y1={6} y2={y + 20} stroke="#d4d4d8" strokeWidth="1.2" strokeDasharray="3 3" />
        <text x={x(0)} y={H - 6} textAnchor="middle" fontSize="10.5" fill="#d4d4d8">
          0 (no difference)
        </text>
        {/* whiskered interval */}
        <line x1={x(low)} x2={x(high)} y1={y} y2={y} stroke="#a1a1aa" strokeWidth="2.4" strokeLinecap="round" />
        <line x1={x(low)} x2={x(low)} y1={y - 7} y2={y + 7} stroke="#a1a1aa" strokeWidth="2" />
        <line x1={x(high)} x2={x(high)} y1={y - 7} y2={y + 7} stroke="#a1a1aa" strokeWidth="2" />
        {/* estimate */}
        <path d={`M${x(estimate)} ${y - 7} l7 7 l-7 7 l-7 -7Z`} fill="#a78bfa" stroke="#ede9fe" strokeWidth="1" />
        <text x={x(low)} y={y - 11} textAnchor="middle" fontSize="10.5" fill="#d4d4d8">{format(low)}</text>
        <text x={x(high)} y={y - 11} textAnchor="middle" fontSize="10.5" fill="#d4d4d8">{format(high)}</text>
      </svg>
      <figcaption className="mt-1 text-xs text-zinc-400">{summary}</figcaption>
    </figure>
  )
}
