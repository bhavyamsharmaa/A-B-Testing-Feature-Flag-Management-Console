import { useState } from 'react'
import { AccessGate } from '../components/AccessGate'
import { ConsoleCard } from '../components/ConsoleCard'
import { ConsoleHeader } from '../components/ConsoleHeader'
import { Notice, type NoticeState } from '../components/Notice'
import { DeleteSegmentDialog } from '../components/segments/DeleteSegmentDialog'
import { SegmentEditor } from '../components/segments/SegmentEditor'
import { wsAtLeast } from '../lib/permissions'
import { useConsoleEnv } from '../lib/useConsoleEnv'
import { useSegments } from '../lib/useSegments'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { SegmentSummary } from '../types'

const btn =
  'rounded-lg border border-white/10 bg-white/[0.03] px-3.5 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-40'

type Dialog = { kind: 'edit'; key: string | null } | { kind: 'delete'; segment: SegmentSummary } | null

/** Reusable audiences that flag targeting rules can name. Shared by every environment of the workspace. */
export default function SegmentsPage() {
  const { email, me, meLoading, meError, reloadMe, signOut, workspace, workspaceRole, roles } = useConsoleEnv()
  const { segments, loading, error, reload } = useSegments(workspace?.id ?? null)
  const slow = useSlowHint(loading)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [notice, setNotice] = useState<NoticeState | null>(null)
  const canEdit = wsAtLeast(workspaceRole, 'editor')

  return (
    <main className="mx-auto max-w-5xl px-4 py-10">
      <ConsoleHeader email={email} onSignOut={() => void signOut()} />

      <ConsoleCard prod={false}>
        <AccessGate me={me} meLoading={meLoading} meError={meError} reloadMe={reloadMe} roleCount={roles.length} />

        {workspace && (
          <div className="space-y-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 className="text-xs font-medium uppercase tracking-wider text-zinc-500">Segments in {workspace.name}</h2>
                <p className="mt-1 max-w-2xl text-xs text-zinc-500">
                  A segment is a reusable audience: explicit members and/or attribute rules. A flag&rsquo;s targeting rule can serve a
                  variation to people in (or not in) a segment. Segments are shared by every environment of this workspace.
                </p>
              </div>
              <div className="flex gap-2">
                <button className={btn} onClick={() => void reload()} disabled={loading}>
                  {loading ? 'Refreshing…' : 'Refresh'}
                </button>
                {canEdit && (
                  <button
                    className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-3.5 py-1.5 text-sm font-medium text-white transition hover:brightness-110"
                    onClick={() => setDialog({ kind: 'edit', key: null })}
                  >
                    New segment
                  </button>
                )}
              </div>
            </div>

            {notice && <Notice notice={notice} onDismiss={() => setNotice(null)} />}

            {error && (
              <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <p>{error}</p>
                <button onClick={() => void reload()} className="mt-2 underline">
                  Retry
                </button>
              </div>
            )}

            {loading && segments === null && (
              <div className="space-y-2" role="status" aria-label="Loading segments">
                {[0, 1].map((i) => (
                  <div key={i} className="h-24 animate-shimmer rounded-xl bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
                ))}
                {slow && <p className="text-xs text-zinc-500">{SLOW_HINT}</p>}
              </div>
            )}

            {segments !== null && segments.length === 0 && !error && (
              <div className="rounded-lg border border-dashed border-white/10 px-4 py-10 text-center text-sm text-zinc-400" data-testid="segments-empty">
                <p>No segments yet.</p>
                <p className="mt-1 text-xs text-zinc-500">
                  {canEdit ? 'Create one to reuse an audience across flags.' : 'Editors and above can create segments.'}
                </p>
              </div>
            )}

            {segments !== null && segments.length > 0 && (
              <ul className="space-y-3" aria-label="Segments">
                {segments.map((s) => (
                  <li key={s.key} className="rounded-xl border border-white/10 bg-black/30 p-4" data-testid={`segment-${s.key}`}>
                    <div className="flex flex-wrap items-start justify-between gap-3">
                      <div className="min-w-0">
                        <h3 className="truncate text-base font-semibold">{s.name}</h3>
                        <p className="font-mono text-xs text-zinc-500">{s.key}</p>
                        {s.description && <p className="mt-1 text-sm text-zinc-400">{s.description}</p>}
                      </div>
                      <div className="flex gap-2">
                        <button className={btn} onClick={() => setDialog({ kind: 'edit', key: s.key })} aria-label={`${canEdit ? 'Edit' : 'View'} ${s.key}`}>
                          {canEdit ? 'Edit' : 'View'}
                        </button>
                        {canEdit && (
                          <button className={`${btn} hover:!border-red-500/60 hover:!text-red-300`} onClick={() => setDialog({ kind: 'delete', segment: s })} aria-label={`Delete ${s.key}`}>
                            Delete
                          </button>
                        )}
                      </div>
                    </div>
                    <p className="mt-3 text-xs text-zinc-400">
                      {s.memberCount.toLocaleString('en-US')} explicit member{s.memberCount === 1 ? '' : 's'}
                      {' · '}
                      {s.rules.length === 0 ? 'no rules' : `${s.rules.length} rule${s.rules.length === 1 ? '' : 's'} (all must match)`}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </ConsoleCard>

      {workspace && dialog?.kind === 'edit' && (
        <SegmentEditor
          workspaceId={workspace.id}
          segmentKey={dialog.key}
          readOnly={!canEdit}
          onClose={() => setDialog(null)}
          onSaved={(seg) => {
            setNotice({ kind: 'success', text: `Segment ${seg.name} saved.` })
            void reload()
          }}
        />
      )}
      {workspace && dialog?.kind === 'delete' && (
        <DeleteSegmentDialog
          workspaceId={workspace.id}
          segment={dialog.segment}
          onClose={() => setDialog(null)}
          onDeleted={() => {
            setNotice({ kind: 'success', text: `Segment ${dialog.segment.name} deleted.` })
            void reload()
          }}
        />
      )}
    </main>
  )
}
