import type { Flag } from '../types'

const preview = (value: unknown) => {
  const text = typeof value === 'string' ? JSON.stringify(value) : (JSON.stringify(value) ?? '')
  return text.length > 80 ? `${text.slice(0, 80)}…` : text
}

/** Read-only list of a flag's variations, in the order that defines rollout ranges. */
export function VariationsList({ flag }: { flag: Flag }) {
  return (
    <section className="mt-7 border-t border-white/10 pt-6">
      <h3 className="text-xs font-medium uppercase tracking-wider text-zinc-500">
        Variations <span className="normal-case text-zinc-600">({flag.variations.length})</span>
      </h3>
      <ol className="mt-3 divide-y divide-white/5 rounded-lg border border-white/10 bg-black/20 text-sm">
        {flag.variations.map((v, i) => (
          <li key={v.id} className="flex items-center gap-3 px-3 py-2">
            <span className="w-5 shrink-0 text-xs text-zinc-600">{i + 1}.</span>
            <span className="shrink-0 font-mono text-[13px] text-zinc-100">{v.id}</span>
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-zinc-400" title={JSON.stringify(v.value)}>
              {preview(v.value)}
            </span>
            {v.id === flag.config.fallthroughVariationId && (
              <span
                title="Served when no targeting rule or rollout matches"
                className="shrink-0 rounded-full bg-accent/15 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-accent"
              >
                Default
              </span>
            )}
          </li>
        ))}
      </ol>
      <p className="mt-2 text-xs text-zinc-500">The order is fixed. Rollout ranges follow it.</p>
    </section>
  )
}
