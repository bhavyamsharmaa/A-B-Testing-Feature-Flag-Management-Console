import { useState } from 'react'
import { changedFields } from '../lib/diff'

const KIND_STYLE = {
  added: 'text-emerald-300',
  removed: 'text-red-300',
  changed: 'text-amber-200',
} as const

const pretty = (v: unknown) => JSON.stringify(v ?? null, null, 2)

/** What an audit entry changed. All values are rendered as plain text. */
export function DiffView({ action, before, after }: { action: string; before: unknown; after: unknown }) {
  const [raw, setRaw] = useState(false)
  const changes = changedFields(before, after)

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-xs font-medium uppercase tracking-wider text-zinc-500">Changes</h3>
        <button
          onClick={() => setRaw((r) => !r)}
          aria-pressed={raw}
          className="rounded-md border border-white/10 px-2.5 py-1 text-xs text-zinc-300 transition hover:border-accent/60 hover:text-white"
        >
          {raw ? 'Show changes' : 'Show raw JSON'}
        </button>
      </div>

      {raw ? (
        <div className="grid gap-3 md:grid-cols-2">
          {[
            ['Before', before],
            ['After', after],
          ].map(([label, value]) => (
            <div key={label as string}>
              <p className="mb-1 text-xs text-zinc-500">{label as string}</p>
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-white/10 bg-black/40 p-3 font-mono text-xs text-zinc-200">
                {pretty(value)}
              </pre>
            </div>
          ))}
        </div>
      ) : changes.length === 0 ? (
        <p className="text-sm text-zinc-400">
          {action === 'flag.kill'
            ? 'No field changed: the flag was already disabled. Every kill is logged, even a repeat.'
            : 'No field-level changes were recorded.'}
        </p>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-white/10">
          <table className="w-full min-w-[480px] text-left text-xs">
            <thead>
              <tr className="text-zinc-500">
                <th className="px-3 py-2 font-medium">Field</th>
                <th className="px-3 py-2 font-medium">Before</th>
                <th className="px-3 py-2 font-medium">After</th>
              </tr>
            </thead>
            <tbody>
              {changes.map((c) => (
                <tr key={c.field} className="border-t border-white/5 align-top">
                  <td className={`px-3 py-2 font-mono ${KIND_STYLE[c.kind]}`}>{c.field}</td>
                  <td className="max-w-[260px] whitespace-pre-wrap break-all px-3 py-2 font-mono text-zinc-300">
                    {c.kind === 'added' ? <span className="text-zinc-600">(none)</span> : c.before}
                  </td>
                  <td className="max-w-[260px] whitespace-pre-wrap break-all px-3 py-2 font-mono text-zinc-100">
                    {c.kind === 'removed' ? <span className="text-zinc-600">(none)</span> : c.after}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
