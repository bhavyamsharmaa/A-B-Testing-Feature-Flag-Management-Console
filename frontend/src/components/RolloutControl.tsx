import { BP_PER_PERCENT, TOTAL_BP, describeSplit, formatPercent, percentToBp } from '../lib/rollout'

interface Props {
  savedBp: number
  pendingBp: number
  disabled: boolean
  disabledReason: string
  onChange: (bp: number) => void
}

/** Slider plus number input for the share of users who get ON. Works in whole percents. */
export function RolloutControl({ savedBp, pendingBp, disabled, disabledReason, onChange }: Props) {
  const percent = Math.round(pendingBp / BP_PER_PERCENT) // a saved 12.345% shows at 12 until moved
  const dirty = pendingBp !== savedBp

  return (
    <div className="space-y-4" title={disabled ? disabledReason : undefined}>
      <div className="flex items-center justify-between gap-3 text-xs text-zinc-400">
        <span>
          Saved: <span className="font-medium text-zinc-200">{formatPercent(savedBp)} ON</span>
        </span>
        <span className={dirty ? 'font-medium text-amber-300' : 'text-zinc-500'}>
          {dirty ? `Pending: ${formatPercent(pendingBp)} ON` : 'No pending change'}
        </span>
      </div>

      <div className="flex items-center gap-4">
        <input
          type="range"
          min={0}
          max={100}
          step={1}
          value={percent}
          disabled={disabled}
          onChange={(e) => onChange(percentToBp(Number(e.target.value)))}
          aria-label="Percentage of users who get ON"
          className="h-2 w-full cursor-pointer accent-accent disabled:cursor-not-allowed disabled:opacity-50"
        />
        <label className="flex shrink-0 items-center gap-1 text-sm text-zinc-300">
          <input
            type="number"
            min={0}
            max={100}
            step={1}
            value={percent}
            disabled={disabled}
            onChange={(e) => {
              if (e.target.value !== '') onChange(percentToBp(Number(e.target.value)))
            }}
            aria-label="Rollout percentage"
            className="w-16 rounded-lg border border-white/10 bg-black/40 px-2 py-1.5 text-right font-mono text-sm outline-none transition focus:border-accent focus:ring-4 focus:ring-accent/20 disabled:opacity-50"
          />
          %
        </label>
      </div>

      <div className="flex h-2 overflow-hidden rounded-full bg-zinc-700" aria-hidden>
        <div className="bg-accent transition-all" style={{ width: `${(pendingBp / TOTAL_BP) * 100}%` }} />
      </div>
      <p className="text-sm text-zinc-200">{describeSplit(pendingBp)}</p>
    </div>
  )
}
