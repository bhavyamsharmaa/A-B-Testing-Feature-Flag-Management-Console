import type { EnvironmentRole } from '../types'
import { isProduction } from '../lib/permissions'

interface Props {
  roles: EnvironmentRole[]
  selected: string
  onSelect: (env: string) => void
  onRefresh: () => void
  refreshing: boolean
}

export function EnvSwitcher({ roles, selected, onSelect, onRefresh, refreshing }: Props) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div role="tablist" aria-label="Environment" className="flex gap-1 rounded-xl border border-white/10 bg-black/30 p-1">
        {roles.map(({ environment, role }) => {
          const active = environment === selected
          const prod = isProduction(environment)
          const activeStyle = prod
            ? 'bg-red-500/20 text-red-200 shadow-[0_0_20px_-6px_rgba(239,68,68,0.7)]'
            : 'bg-accent/20 text-white shadow-[0_0_20px_-6px_rgba(124,92,255,0.7)]'
          return (
            <button
              key={environment}
              role="tab"
              aria-selected={active}
              onClick={() => onSelect(environment)}
              className={`rounded-lg px-3.5 py-1.5 text-sm transition ${
                active ? activeStyle : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-200'
              }`}
            >
              <span className="font-mono">{environment}</span>
              <span className={`ml-2 text-[11px] ${active ? 'opacity-80' : 'opacity-50'}`}>{role}</span>
            </button>
          )
        })}
      </div>

      <button
        onClick={onRefresh}
        disabled={refreshing}
        className="flex items-center gap-2 rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-60"
      >
        <svg
          viewBox="0 0 24 24"
          className={`h-4 w-4 ${refreshing ? 'animate-spin' : ''}`}
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden
        >
          <path d="M21 12a9 9 0 1 1-3-6.7" />
          <path d="M21 3v6h-6" />
        </svg>
        Refresh
      </button>
    </div>
  )
}
