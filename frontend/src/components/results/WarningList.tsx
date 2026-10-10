import { sortedWarnings, type WarningInfo } from '../../lib/resultsFormat'
import type { ResultsWarning } from '../../types'

const TONE: Record<WarningInfo['tone'], { box: string; label: string; word: string; icon: string }> = {
  blocking: { box: 'border-red-400/60 bg-red-500/15 text-red-50', label: 'text-red-100', word: 'Blocking', icon: '⚠' },
  caution: { box: 'border-amber-400/40 bg-amber-400/10 text-amber-50', label: 'text-amber-100', word: 'Caution', icon: '◭' },
  note: { box: 'border-white/10 bg-white/[0.03] text-zinc-100', label: 'text-zinc-300', word: 'Note', icon: 'ⓘ' },
}

/** Data-quality warnings, most serious first. Each says what it is and what to do. */
export function WarningList({ warnings }: { warnings: ResultsWarning[] }) {
  const items = sortedWarnings(warnings)
  if (items.length === 0) return null
  return (
    <section aria-label="Data-quality warnings" data-testid="warnings" className="space-y-2">
      {items.map((w, i) => {
        const t = TONE[w.tone]
        return (
          <div
            key={`${w.code}-${i}`}
            data-testid={`warning-${w.code}`}
            data-tone={w.tone}
            role={w.tone === 'blocking' ? 'alert' : undefined}
            className={`rounded-xl border px-4 py-3 text-sm ${t.box} ${w.tone === 'blocking' ? 'border-2' : ''}`}
          >
            <p className={`flex flex-wrap items-center gap-2 font-medium ${t.label}`}>
              <span aria-hidden="true">{t.icon}</span>
              <span className="rounded border border-current/40 px-1.5 text-[11px] uppercase tracking-wider">{t.word}</span>
              <span>{w.title}</span>
            </p>
            <p className="mt-1.5 opacity-90">{w.what}</p>
            <p className="mt-1 opacity-90">
              <span className="font-medium">What to do: </span>
              {w.action}
            </p>
          </div>
        )
      })}
    </section>
  )
}
