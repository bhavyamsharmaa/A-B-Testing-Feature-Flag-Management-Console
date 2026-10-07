import { Fragment, useState } from 'react'
import { actionLabel } from '../lib/auditLabels'
import { absoluteTime, relativeTime, useNow } from '../lib/time'
import type { AuditEntry } from '../types'
import { DiffView } from './DiffView'

export function AuditTable({ entries }: { entries: AuditEntry[] }) {
  const [open, setOpen] = useState<Set<number>>(new Set())
  const now = useNow()

  const toggle = (id: number) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[760px] text-left text-sm">
        <thead>
          <tr className="text-xs uppercase tracking-wider text-zinc-500">
            <th className="px-3 py-2 font-medium">Time</th>
            <th className="px-3 py-2 font-medium">Actor</th>
            <th className="px-3 py-2 font-medium">Action</th>
            <th className="px-3 py-2 font-medium">Resource</th>
            <th className="px-3 py-2 font-medium">Severity</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => {
            const expanded = open.has(e.id)
            const kill = e.action === 'flag.kill'
            const critical = e.severity === 'critical'
            return (
              <Fragment key={e.id}>
                <tr className={`border-t border-white/5 transition hover:bg-white/[0.03] ${kill ? 'bg-red-500/[0.08]' : ''}`}>
                  <td className={`border-l-2 px-3 py-3 ${kill ? 'border-red-500' : 'border-transparent'}`}>
                    <button
                      onClick={() => toggle(e.id)}
                      aria-expanded={expanded}
                      aria-label={`${expanded ? 'Hide' : 'Show'} changes for entry ${e.id}`}
                      className="flex items-center gap-2 text-left text-zinc-300 hover:text-white"
                    >
                      <span className={`inline-block text-[10px] text-zinc-500 transition ${expanded ? 'rotate-90' : ''}`}>▶</span>
                      <span title={absoluteTime(e.createdAt)}>{relativeTime(e.createdAt, now)}</span>
                    </button>
                  </td>
                  <td className="px-3 py-3 text-zinc-300">{e.actorEmail}</td>
                  <td className="px-3 py-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className={kill ? 'font-semibold text-red-200' : 'text-zinc-100'}>{actionLabel(e.action)}</span>
                      {e.scope === 'global' && (
                        <span
                          title="Not tied to one environment; shown in every environment's log"
                          className="rounded-full bg-accent/15 px-2 py-0.5 text-[10px] font-medium text-accent"
                        >
                          All environments
                        </span>
                      )}
                    </div>
                    <div className="font-mono text-[11px] text-zinc-500">{e.action}</div>
                  </td>
                  <td className="max-w-[240px] px-3 py-3">
                    <div className="text-xs text-zinc-500">{e.resourceType}</div>
                    <div className="truncate font-mono text-[13px] text-zinc-200" title={e.resourceId}>
                      {e.resourceId}
                    </div>
                  </td>
                  <td className="px-3 py-3">
                    <span
                      className={`rounded-full px-2.5 py-0.5 text-xs font-semibold ${
                        critical ? 'bg-red-500/20 text-red-300' : 'bg-white/10 text-zinc-400'
                      }`}
                    >
                      {critical ? 'critical' : 'info'}
                    </span>
                  </td>
                </tr>
                {expanded && (
                  <tr className={kill ? 'bg-red-500/[0.04]' : 'bg-black/20'}>
                    <td colSpan={5} className="px-4 py-4">
                      <DiffView action={e.action} before={e.diffBefore} after={e.diffAfter} />
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
