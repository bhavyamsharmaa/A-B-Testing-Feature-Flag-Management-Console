import type { StatusInfo, StatusKey } from '../../lib/resultsFormat'

// Every status has text AND a distinct shape: colour is never the only signal.
const SHAPES: Record<StatusKey, React.ReactNode> = {
  significant: <path d="M7 1.5 12.5 7 7 12.5 1.5 7Z" fill="currentColor" />, // filled diamond
  not_significant: <circle cx="7" cy="7" r="4.8" fill="none" stroke="currentColor" strokeWidth="1.6" />, // hollow circle
  insufficient: <circle cx="7" cy="7" r="4.8" fill="none" stroke="currentColor" strokeWidth="1.6" strokeDasharray="2.4 2" />, // dashed circle
  not_reliable: <path d="M7 1.6 13 12.4H1Z M7 5.4v3.4 M7 10.2v.4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" />, // warning triangle
  unavailable: <path d="M2 2h10v10H2Z M4.5 7h5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeDasharray="1.2 1.2" />, // dotted square with a dash
}

const STYLE: Record<StatusKey, string> = {
  significant: 'border-accent/60 bg-accent/15 text-violet-100',
  not_significant: 'border-white/20 bg-white/[0.05] text-zinc-200',
  insufficient: 'border-dashed border-amber-400/60 bg-amber-400/10 text-amber-100',
  not_reliable: 'border-red-400/70 bg-red-500/15 text-red-100',
  unavailable: 'border-dashed border-white/25 bg-transparent text-zinc-300',
}

export function StatusBadge({ info }: { info: StatusInfo }) {
  return (
    <span
      data-testid="status"
      data-status={info.key}
      className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium ${STYLE[info.key]}`}
    >
      <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true" focusable="false">
        {SHAPES[info.key]}
      </svg>
      {info.label}
    </span>
  )
}
