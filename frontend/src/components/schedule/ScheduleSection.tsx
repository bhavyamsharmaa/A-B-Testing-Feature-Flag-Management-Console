import { useEffect, useState } from 'react'
import { cancelScheduledChange } from '../../api/scheduling'
import { describeError } from '../../lib/errors'
import { isProduction, roleIn } from '../../lib/permissions'
import { KIND_SHORT, browserZone, formatInZone, formatUtc, requiredRole, roleAllows, scheduleBlockedReason } from '../../lib/schedule'
import { useScheduledChanges } from '../../lib/useScheduledChanges'
import { SLOW_HINT, useSlowHint } from '../../lib/useSlowHint'
import { useWorkspace } from '../../workspace/WorkspaceProvider'
import type { EnvironmentRole, Flag, ScheduledChange, ScheduledStatus } from '../../types'
import { ConfirmDialog } from '../ConfirmDialog'
import { PromotionDialog } from './PromotionDialog'
import { ScheduleDialog } from './ScheduleDialog'

interface Props {
  flag: Flag
  env: string
  prod: boolean
  roles: EnvironmentRole[]
  /** Signed-in user's email: its own changes can always be cancelled. */
  email: string
  canEdit: boolean
  disabledReason: string
  onFlagChanged: (flag: Flag) => void
  /** Tells the drawer a dialog is open, so Escape closes the dialog, not the drawer. */
  onDialogChange: (open: boolean) => void
}

/** Status as words and a symbol: never colour alone. */
const STATUS: Record<ScheduledStatus, { label: string; mark: string; tone: string }> = {
  pending: { label: 'Pending', mark: '◔', tone: 'bg-accent/15 text-accent' },
  running: { label: 'Running', mark: '▶', tone: 'bg-accent/15 text-accent' },
  succeeded: { label: 'Succeeded', mark: '✓', tone: 'bg-emerald-500/15 text-emerald-300' },
  failed: { label: 'Failed', mark: '✕', tone: 'bg-red-500/20 text-red-300' },
  missed: { label: 'Missed', mark: '!', tone: 'bg-amber-500/20 text-amber-200' },
  cancelled: { label: 'Cancelled', mark: '–', tone: 'bg-white/10 text-zinc-300' },
}

function describeChange(c: ScheduledChange): string {
  if (c.kind !== 'patch') return KIND_SHORT[c.kind]
  const parts = []
  if (c.change.rollout !== undefined) parts.push('rollout')
  if (c.change.fallthroughVariationId !== undefined) parts.push(`fallthrough → ${c.change.fallthroughVariationId}`)
  if (c.change.targetingRules !== undefined) parts.push('targeting rules')
  return `Change ${parts.join(' and ')}`
}

export function ScheduleSection({ flag, env, prod, roles, email, canEdit, disabledReason, onFlagChanged, onDialogChange }: Props) {
  const { active } = useWorkspace()
  const { changes, loading, error, reload } = useScheduledChanges(env, flag.key, active?.id ?? null)
  const [dialog, setDialog] = useState<'schedule' | 'promote' | null>(null)
  const [cancelling, setCancelling] = useState<ScheduledChange | null>(null)
  const [cancelBusy, setCancelBusy] = useState(false)
  const [cancelError, setCancelError] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const slow = useSlowHint(cancelBusy)
  const zone = browserZone()
  const role = roleIn(roles, env)
  const blockedEverything = ['enable', 'disable', 'kill', 'patch'].every((k) => scheduleBlockedReason(role, k as 'enable', prod, env) !== null)

  useEffect(() => {
    onDialogChange(dialog !== null || cancelling !== null)
  }, [dialog, cancelling, onDialogChange])

  const canCancel = (c: ScheduledChange) =>
    (c.status === 'pending' || c.status === 'running') &&
    (c.createdBy === email || roleAllows(role, requiredRole(c.kind, isProduction(c.environment))))

  async function cancel() {
    if (!cancelling) return
    setCancelBusy(true)
    setCancelError(null)
    try {
      await cancelScheduledChange(cancelling.environment, cancelling.id)
      setCancelling(null)
      setNote('Scheduled change cancelled.')
      void reload()
    } catch (err) {
      setCancelError(describeError(err, 'cancel this change'))
      void reload()
    } finally {
      setCancelBusy(false)
    }
  }

  const upcoming = changes?.filter((c) => c.status === 'pending' || c.status === 'running') ?? []
  const past = changes?.filter((c) => c.status !== 'pending' && c.status !== 'running') ?? []

  return (
    <section className="mt-7 border-t border-white/10 pt-6" aria-labelledby="schedule-h">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 id="schedule-h" className="text-xs font-medium uppercase tracking-wider text-zinc-500">
          Schedule and promote
        </h3>
        <div className="flex flex-wrap gap-2">
          <button
            onClick={() => setDialog('promote')}
            className="rounded-lg border border-white/10 bg-white/[0.03] px-3 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white"
          >
            Promote into {env}…
          </button>
          {!blockedEverything && (
            <button
              onClick={() => {
                setNote(null)
                setDialog('schedule')
              }}
              className="rounded-lg border border-white/10 bg-white/[0.03] px-3 py-1.5 text-sm text-zinc-300 transition hover:border-accent/60 hover:bg-accent/10 hover:text-white"
            >
              Schedule a change…
            </button>
          )}
        </div>
      </div>
      <p className="mt-1 text-xs text-zinc-400">
        Scheduled changes run at an exact time in UTC and are checked against your role again when they run. Times here are also shown in {zone}.
      </p>
      {blockedEverything && <p className="mt-2 text-xs text-zinc-500">Viewers can see scheduled changes but not create or cancel them.</p>}

      {note && (
        <p role="status" className="mt-3 rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-200">
          {note}
        </p>
      )}

      {error && (
        <div role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          <p>{error}</p>
          <button onClick={() => void reload()} className="mt-2 underline">
            Retry
          </button>
        </div>
      )}
      {loading && changes === null && (
        <div className="mt-3 space-y-2" role="status" aria-label="Loading scheduled changes">
          <div className="h-12 animate-shimmer rounded-lg bg-white/[0.06]" />
        </div>
      )}

      {changes !== null && changes.length === 0 && !error && (
        <p className="mt-3 rounded-lg border border-dashed border-white/10 px-4 py-5 text-center text-sm text-zinc-400" data-testid="no-scheduled">
          Nothing scheduled for this flag in {env}.
        </p>
      )}

      {upcoming.length > 0 && <List title="Upcoming" items={upcoming} zone={zone} canCancel={canCancel} onCancel={(c) => { setCancelError(null); setCancelling(c) }} />}
      {past.length > 0 && <List title="History" items={past} zone={zone} canCancel={canCancel} onCancel={() => undefined} />}

      {dialog === 'schedule' && (
        <ScheduleDialog
          flag={flag}
          env={env}
          roles={roles}
          onClose={() => setDialog(null)}
          onCreated={(c) => {
            setNote(`Scheduled: ${describeChange(c).toLowerCase()} in ${c.environment} at ${formatUtc(c.runAt)}.`)
            void reload()
          }}
        />
      )}
      {dialog === 'promote' && (
        <PromotionDialog
          flag={flag}
          env={env}
          prod={prod}
          roles={roles}
          canEdit={canEdit}
          disabledReason={disabledReason}
          onClose={() => setDialog(null)}
          onApplied={(f) => {
            setNote(`Promoted into ${env}.`)
            onFlagChanged(f)
          }}
        />
      )}
      {cancelling && (
        <ConfirmDialog
          title="Cancel this scheduled change?"
          confirmLabel="Cancel the change"
          tone="danger"
          busy={cancelBusy}
          error={cancelError}
          slowHint={cancelBusy && slow ? SLOW_HINT : null}
          onConfirm={() => void cancel()}
          onCancel={() => setCancelling(null)}
        >
          <p>
            {describeChange(cancelling)} in <span className="font-mono">{cancelling.environment}</span>, scheduled for {formatUtc(cancelling.runAt)}, will not run.
          </p>
        </ConfirmDialog>
      )}
    </section>
  )
}

function List({ title, items, zone, canCancel, onCancel }: { title: string; items: ScheduledChange[]; zone: string; canCancel: (c: ScheduledChange) => boolean; onCancel: (c: ScheduledChange) => void }) {
  return (
    <div className="mt-4">
      <h4 className="text-xs font-medium text-zinc-400">{title}</h4>
      <ul className="mt-2 space-y-2" aria-label={title}>
        {items.map((c) => {
          const s = STATUS[c.status]
          return (
            <li key={c.id} className="rounded-lg border border-white/10 bg-black/30 p-3 text-sm" data-testid={`scheduled-${c.status}`}>
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div>
                  <p className="font-medium text-zinc-100">
                    {describeChange(c)} <span className="font-normal text-zinc-400">in</span> <span className="font-mono">{c.environment}</span>
                  </p>
                  <p className="text-xs text-zinc-300">{c.status === 'pending' || c.status === 'running' ? 'Runs at' : 'Was due at'} {formatUtc(c.runAt)}</p>
                  <p className="text-xs text-zinc-500">{formatInZone(c.runAt, zone)} · by {c.createdBy}</p>
                </div>
                <div className="flex items-center gap-2">
                  <span className={`rounded-full px-2.5 py-0.5 text-xs font-semibold ${s.tone}`}>
                    <span aria-hidden="true">{s.mark} </span>
                    {s.label}
                  </span>
                  {canCancel(c) && (
                    <button
                      onClick={() => onCancel(c)}
                      aria-label={`Cancel the ${describeChange(c).toLowerCase()} scheduled for ${formatUtc(c.runAt)}`}
                      className="rounded-md border border-white/10 px-2 py-1 text-xs text-zinc-300 transition hover:border-red-500/60 hover:text-red-300"
                    >
                      Cancel
                    </button>
                  )}
                </div>
              </div>
              {c.resultMessage && c.status !== 'succeeded' && <p className="mt-2 text-xs text-zinc-300">{c.resultMessage}</p>}
              {c.status === 'pending' && c.attempts > 0 && <p className="mt-1 text-xs text-zinc-500">Attempt {c.attempts} did not complete; it will be retried.</p>}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
