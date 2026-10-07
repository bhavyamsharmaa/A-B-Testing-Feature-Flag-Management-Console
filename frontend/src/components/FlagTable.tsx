import { TOTAL_BP, booleanVariations, formatPercent, hasRollout, savedOnBp } from '../lib/rollout'
import type { Flag } from '../types'

interface Props {
  flags: Flag[]
  busyKeys: Set<string>
  killedKeys: Set<string>
  toggleAllowed: boolean
  toggleDisabledReason: string
  killAllowed: boolean
  onToggle: (flag: Flag) => void
  onKill: (flag: Flag) => void
  onConfigure: (flag: Flag) => void
}

const fmt = (iso: string) =>
  new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export function FlagTable({ flags, busyKeys, killedKeys, toggleAllowed, toggleDisabledReason, killAllowed, onToggle, onKill, onConfigure }: Props) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[720px] text-left text-sm">
        <thead>
          <tr className="text-xs uppercase tracking-wider text-zinc-500">
            <th className="px-3 py-2 font-medium">Flag</th>
            <th className="px-3 py-2 font-medium">Status</th>
            <th className="px-3 py-2 font-medium">Version</th>
            <th className="px-3 py-2 font-medium">Last updated</th>
            <th className="px-3 py-2 text-right font-medium">Actions</th>
          </tr>
        </thead>
        <tbody>
          {flags.map((flag) => {
            const enabled = flag.config.enabled
            const busy = busyKeys.has(flag.key)
            const killed = killedKeys.has(flag.key) && !enabled
            // Badge only for a boolean flag with a rollout set below 100%.
            const bools = booleanVariations(flag)
            const onBp = bools && hasRollout(flag) ? savedOnBp(flag, bools) : null
            return (
              <tr
                key={flag.key}
                className={`border-t border-white/5 transition ${killed ? 'bg-red-500/[0.06]' : ''}`}
              >
                <td className={`px-3 py-3 ${enabled ? '' : 'opacity-60'}`}>
                  <button
                    onClick={() => onConfigure(flag)}
                    title="Configure this flag"
                    className="text-left font-mono text-[13px] text-zinc-100 underline-offset-2 transition hover:text-accent hover:underline"
                  >
                    {flag.key}
                  </button>
                  <div className="text-xs text-zinc-400">{flag.name}</div>
                </td>

                <td className="px-3 py-3">
                  <div className="flex items-center gap-3">
                    <button
                      role="switch"
                      aria-checked={enabled}
                      aria-label={`${enabled ? 'Disable' : 'Enable'} ${flag.key}`}
                      title={toggleAllowed ? undefined : toggleDisabledReason}
                      disabled={!toggleAllowed || busy}
                      onClick={() => onToggle(flag)}
                      className={`relative h-6 w-11 shrink-0 rounded-full transition disabled:cursor-not-allowed disabled:opacity-50 ${
                        enabled ? 'bg-accent shadow-[0_0_14px_rgba(124,92,255,0.6)]' : 'bg-zinc-700'
                      }`}
                    >
                      <span
                        className={`absolute top-0.5 h-5 w-5 rounded-full bg-white transition-all ${
                          enabled ? 'left-[22px]' : 'left-0.5'
                        }`}
                      />
                    </button>
                    {busy ? (
                      <span className="text-xs text-zinc-400">Updating…</span>
                    ) : killed ? (
                      <span className="rounded-full bg-red-500/20 px-2.5 py-0.5 text-xs font-semibold tracking-wide text-red-300">
                        KILLED
                      </span>
                    ) : enabled ? (
                      <span className="text-xs text-emerald-400">Enabled</span>
                    ) : (
                      <span className="text-xs text-zinc-500">Disabled</span>
                    )}
                    {onBp !== null && onBp < TOTAL_BP && (
                      <span
                        title={`Rolled out: ${formatPercent(onBp)} of users get ON`}
                        className="rounded-full bg-accent/15 px-2 py-0.5 text-[11px] font-medium text-accent"
                      >
                        {formatPercent(onBp)}
                      </span>
                    )}
                    {flag.config.targetingRules.length > 0 && (
                      <span
                        title="Targeting rules run before the rollout"
                        className="rounded-full bg-indigo-500/15 px-2 py-0.5 text-[11px] font-medium text-indigo-300"
                      >
                        {flag.config.targetingRules.length} {flag.config.targetingRules.length === 1 ? 'rule' : 'rules'}
                      </span>
                    )}
                  </div>
                </td>

                <td className={`px-3 py-3 font-mono text-xs text-zinc-400 ${enabled ? '' : 'opacity-60'}`}>
                  v{flag.config.version}
                </td>
                <td className={`px-3 py-3 text-xs text-zinc-400 ${enabled ? '' : 'opacity-60'}`}>
                  {fmt(flag.config.updatedAt)}
                </td>

                <td className="px-3 py-3 text-right">
                  <div className="flex justify-end gap-2">
                    <button
                      onClick={() => onConfigure(flag)}
                      className="rounded-lg border border-white/10 px-3 py-1.5 text-xs font-medium text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white"
                    >
                      Configure
                    </button>
                    <button
                      onClick={() => onKill(flag)}
                      disabled={!killAllowed || !enabled || busy}
                      title={
                        !killAllowed
                          ? 'Viewers have read-only access'
                          : !enabled
                            ? 'Already disabled'
                            : undefined
                      }
                      className="rounded-lg border border-red-500/50 bg-red-500/10 px-3 py-1.5 text-xs font-semibold text-red-300 transition hover:bg-red-600 hover:text-white disabled:cursor-not-allowed disabled:border-white/10 disabled:bg-transparent disabled:text-zinc-600 disabled:hover:bg-transparent disabled:hover:text-zinc-600"
                    >
                      Kill
                    </button>
                  </div>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
