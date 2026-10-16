import { useMemo, useRef, useState } from 'react'
import { createScheduledChange } from '../../api/scheduling'
import { describeError } from '../../lib/errors'
import { isProduction, roleIn } from '../../lib/permissions'
import { TOTAL_BP } from '../../lib/rollout'
import {
  KIND_LABEL,
  allZones,
  browserZone,
  checkRunTime,
  defaultLocalTime,
  requiredRole,
  scheduleBlockedReason,
  type ChangeKind,
} from '../../lib/schedule'
import { SLOW_HINT, useSlowHint } from '../../lib/useSlowHint'
import { parseWeights, savedWeights, textsFromWeights } from '../../lib/weights'
import type { EnvironmentRole, Flag, ScheduledChange } from '../../types'
import { Modal } from '../Modal'
import { field } from '../TargetingEditor'

interface Props {
  flag: Flag
  /** The environment the flag drawer shows: the default target. */
  env: string
  roles: EnvironmentRole[]
  onClose: () => void
  onCreated: (change: ScheduledChange) => void
}

const KINDS: ChangeKind[] = ['enable', 'disable', 'kill', 'patch']

/**
 * Schedule enable, disable, kill or a rollout/fallthrough change for one flag in one environment. The time
 * is entered as a wall-clock time in a time zone that is always shown, and the dialog always states the exact
 * UTC time the change will run at. The role needed is the one for making the change now.
 */
export function ScheduleDialog({ flag, env, roles, onClose, onCreated }: Props) {
  const zones = useMemo(allZones, [])
  const [zone, setZone] = useState(browserZone)
  const [kind, setKind] = useState<ChangeKind>('enable')
  const [targetEnv, setTargetEnv] = useState(env)
  const [local, setLocal] = useState(() => defaultLocalTime(new Date(), browserZone()))
  const ids = flag.variations.map((v) => v.id)
  const [changeRollout, setChangeRollout] = useState(true)
  const [changeFallthrough, setChangeFallthrough] = useState(false)
  const [texts, setTexts] = useState<Record<string, string>>(() => textsFromWeights(savedWeights(flag)))
  const [fallthrough, setFallthrough] = useState(flag.config.fallthroughVariationId)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showErrors, setShowErrors] = useState(false)
  const idempotencyKey = useRef(crypto.randomUUID()) // one per dialog: a retry or a double click is the same request
  const slow = useSlowHint(busy)

  const prod = isProduction(targetEnv)
  const role = roleIn(roles, targetEnv)
  const blocked = scheduleBlockedReason(role, kind, prod, targetEnv)
  const when = checkRunTime(local, zone, new Date())
  const weights = parseWeights(ids, texts)
  const patchProblem =
    kind !== 'patch'
      ? null
      : !changeRollout && !changeFallthrough
        ? 'Choose what to change: the rollout, the fallthrough variation, or both.'
        : changeRollout && !weights.valid
          ? `Rollout percentages must be numbers adding up to exactly 100% (now ${(weights.total / 1000).toFixed(3).replace(/\.?0+$/, '')}%).`
          : null

  async function submit() {
    setShowErrors(true)
    setError(null)
    if (blocked || when.error || patchProblem || !when.utc) return
    setBusy(true)
    try {
      const change =
        kind === 'patch'
          ? {
              ...(changeRollout ? { rollout: Object.fromEntries(ids.map((id) => [id, weights.weights[id]])) } : {}),
              ...(changeFallthrough ? { fallthroughVariationId: fallthrough } : {}),
            }
          : undefined
      const created = await createScheduledChange(targetEnv, flag.key, {
        idempotencyKey: idempotencyKey.current,
        kind,
        runAt: when.utc.toISOString(),
        ...(change ? { change } : {}),
      })
      onCreated(created)
      onClose()
    } catch (err) {
      setError(describeError(err, `schedule changes in ${targetEnv}`))
    } finally {
      setBusy(false)
    }
  }

  const envNames = roles.map((r) => r.environment)

  return (
    <Modal title={`Schedule a change to ${flag.key}`} onClose={onClose} busy={busy} wide>
      <div className="mt-4 space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label htmlFor="sch-kind" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Change
            </label>
            <select id="sch-kind" className={`${field} mt-1 w-full`} value={kind} disabled={busy} onChange={(e) => setKind(e.target.value as ChangeKind)}>
              {KINDS.map((k) => (
                <option key={k} value={k}>
                  {KIND_LABEL[k]}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor="sch-env" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Environment
            </label>
            <select id="sch-env" className={`${field} mt-1 w-full`} value={targetEnv} disabled={busy} onChange={(e) => setTargetEnv(e.target.value)}>
              {envNames.map((n) => (
                <option key={n} value={n}>
                  {n}
                  {isProduction(n) ? ' (production)' : ''}
                </option>
              ))}
            </select>
          </div>
        </div>

        <p className="text-xs text-zinc-400">
          {prod && kind !== 'kill'
            ? `A change in production needs the ${requiredRole(kind, true)} role, the same as making it now. It is checked again when the change runs, using your role at that time.`
            : `Needs the ${requiredRole(kind, prod)} role in ${targetEnv}. It is checked again when the change runs, using your role at that time.`}
        </p>
        {blocked && (
          <p role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
            {blocked}.
          </p>
        )}

        {kind === 'patch' && (
          <fieldset className="space-y-3 rounded-lg border border-white/10 p-3" disabled={busy}>
            <legend className="px-1 text-xs font-medium uppercase tracking-wider text-zinc-500">What to change</legend>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={changeRollout} onChange={(e) => setChangeRollout(e.target.checked)} />
              Rollout percentages
            </label>
            {changeRollout && (
              <div className="grid gap-2 sm:grid-cols-2">
                {ids.map((id) => (
                  <label key={id} className="flex items-center gap-2 text-sm text-zinc-300">
                    <span className="w-24 truncate font-mono text-xs" title={id}>
                      {id}
                    </span>
                    <input
                      className={`${field} w-24 font-mono`}
                      inputMode="decimal"
                      aria-label={`Percent of users who get ${id}`}
                      value={texts[id] ?? ''}
                      onChange={(e) => setTexts({ ...texts, [id]: e.target.value })}
                    />
                    <span className="text-zinc-500">%</span>
                  </label>
                ))}
              </div>
            )}
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={changeFallthrough} onChange={(e) => setChangeFallthrough(e.target.checked)} />
              Fallthrough variation (served when no rule matches and there is no rollout)
            </label>
            {changeFallthrough && (
              <select className={`${field} w-full sm:w-64`} aria-label="Fallthrough variation" value={fallthrough} onChange={(e) => setFallthrough(e.target.value)}>
                {ids.map((id) => (
                  <option key={id} value={id}>
                    {id}
                  </option>
                ))}
              </select>
            )}
            <p className="text-xs text-zinc-500">
              The change applies only if these settings are still as they are now when it runs ({(TOTAL_BP / 1000).toFixed(0)}% in total for the rollout);
              if someone changes them first it is marked failed with a conflict, and nothing is overwritten. Targeting rules can be scheduled through the API.
            </p>
            {showErrors && patchProblem && <p className="text-xs text-red-400">{patchProblem}</p>}
          </fieldset>
        )}

        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label htmlFor="sch-when" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Date and time
            </label>
            <input id="sch-when" type="datetime-local" className={`${field} mt-1 w-full`} value={local} disabled={busy} aria-describedby="sch-when-line" onChange={(e) => setLocal(e.target.value)} />
          </div>
          <div>
            <label htmlFor="sch-zone" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
              Time zone
            </label>
            <select id="sch-zone" className={`${field} mt-1 w-full`} value={zone} disabled={busy} onChange={(e) => setZone(e.target.value)}>
              {!zones.includes(zone) && <option value={zone}>{zone}</option>}
              {zones.map((z) => (
                <option key={z} value={z}>
                  {z}
                  {z === browserZone() ? ' (your time zone)' : ''}
                </option>
              ))}
            </select>
          </div>
        </div>
        <div id="sch-when-line" aria-live="polite" className="space-y-1 text-sm">
          <p className={when.error ? 'text-red-400' : 'font-medium text-zinc-100'} data-testid="run-line">
            {when.error && !when.utc ? when.line : when.error ? `${when.line} ${when.error}` : when.line}
          </p>
          {when.note && <p className="text-xs text-amber-300">{when.note}</p>}
        </div>

        {error && (
          <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}
        {busy && slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}

        <div className="flex justify-end gap-2 border-t border-white/5 pt-4">
          <button onClick={onClose} disabled={busy} className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50">
            Cancel
          </button>
          <button
            onClick={() => void submit()}
            disabled={busy || blocked !== null}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy ? 'Scheduling…' : 'Schedule'}
          </button>
        </div>
      </div>
    </Modal>
  )
}
