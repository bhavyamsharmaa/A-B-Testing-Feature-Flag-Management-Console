import { useEffect, useRef, useState } from 'react'
import { getFlag, setFlagRollout } from '../api/flags'
import { describeError } from '../lib/errors'
import { toggleDisabledReason } from '../lib/permissions'
import {
  booleanVariations,
  describeSplit,
  formatPercent,
  hasRollout,
  rolloutPayload,
  savedOnBp,
} from '../lib/rollout'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { EnvironmentRole, Flag, Role } from '../types'
import { ConfirmDialog } from './ConfirmDialog'
import { DeleteFlagSection, type DeleteState } from './DeleteFlagSection'
import { ProductionStrip } from './ProductionStrip'
import { RolloutControl } from './RolloutControl'
import { TargetingSection, type TargetingState } from './TargetingSection'

interface Props {
  flag: Flag
  env: string
  prod: boolean
  role: Role | null
  /** The user's roles in every environment (from /me), to decide whether deleting is allowed. */
  roles: EnvironmentRole[]
  /** Whether the user's role may change this flag here (same rule as toggling). */
  canEdit: boolean
  onClose: () => void
  /** Called with the server's version of the flag after a successful save. */
  onSaved: (flag: Flag) => void
  onDeleted: (key: string) => void
  onGone: () => void
}

type Dialog = 'discard' | 'lower' | 'prod' | null

export function FlagDrawer({ flag, env, prod, role, roles, canEdit, onClose, onSaved, onDeleted, onGone }: Props) {
  const [current, setCurrent] = useState(flag)
  const bools = booleanVariations(current)
  const savedBp = bools ? savedOnBp(current, bools) : 0
  const [pendingBp, setPendingBp] = useState(savedBp)
  const dirty = bools !== null && pendingBp !== savedBp

  const [refreshWarning, setRefreshWarning] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [savedNote, setSavedNote] = useState<string | null>(null)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [dialogError, setDialogError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(true)
  const [targeting, setTargeting] = useState<TargetingState>({ dirty: false, busy: false, dialogOpen: false })
  const [deleting, setDeleting] = useState<DeleteState>({ dialogOpen: false, busy: false })
  const slow = useSlowHint(saving || refreshing)

  // Latest values for the async refresh below, so it never overwrites an edit in progress.
  const live = useRef({ pendingBp, savedBp })
  live.current = { pendingBp, savedBp }
  const closeRef = useRef<HTMLButtonElement>(null)

  // The list may be stale: re-read this flag so the saved value (and the lowering warning) is current.
  useEffect(() => {
    let cancelled = false
    getFlag(env, flag.key)
      .then((fresh) => {
        if (cancelled) return
        const b = booleanVariations(fresh)
        const untouched = live.current.pendingBp === live.current.savedBp
        setCurrent(fresh)
        if (b && untouched) setPendingBp(savedOnBp(fresh, b))
      })
      .catch(() => {
        if (!cancelled) setRefreshWarning(true)
      })
      .finally(() => {
        if (!cancelled) setRefreshing(false)
      })
    return () => {
      cancelled = true
    }
  }, [env, flag.key])

  useEffect(() => closeRef.current?.focus(), [])

  function requestClose() {
    if (saving || targeting.busy || deleting.busy) return
    if (dirty || targeting.dirty) setDialog('discard')
    else onClose()
  }

  // Esc closes the drawer, unless a confirmation is open (it handles Esc itself).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && dialog === null && !targeting.dialogOpen && !deleting.dialogOpen) requestClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  /** Resolves to an error message, or null once the server confirmed the new rollout. */
  async function save(): Promise<string | null> {
    if (!bools) return null
    setSaving(true)
    try {
      const updated = await setFlagRollout(env, current.key, rolloutPayload(bools, pendingBp))
      setCurrent(updated)
      setPendingBp(savedOnBp(updated, bools))
      setSavedNote(`Rollout saved: ${describeSplit(savedOnBp(updated, bools))}.`)
      onSaved(updated)
      return null
    } catch (err) {
      return describeError(err, `change the rollout in ${env}`)
    } finally {
      setSaving(false)
    }
  }

  async function onSaveClick() {
    setSaveError(null)
    setSavedNote(null)
    setDialogError(null)
    if (pendingBp < savedBp) {
      setDialog('lower')
    } else if (prod) {
      setDialog('prod')
    } else {
      setSaveError(await save())
    }
  }

  async function onDialogConfirm() {
    if (dialog === 'discard') {
      onClose()
      return
    }
    const err = await save()
    if (err) setDialogError(err)
    else setDialog(null)
  }

  const reason = toggleDisabledReason(role, env)
  const enabled = current.config.enabled
  const rules = current.config.targetingRules.length

  return (
    <div
      className="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) requestClose()
      }}
    >
      <aside
        role="dialog"
        aria-modal="true"
        aria-labelledby="drawer-title"
        className="absolute right-0 top-0 h-full w-full max-w-md animate-slide-in sm:max-w-2xl overflow-y-auto border-l border-white/10 bg-surface p-6 shadow-2xl"
      >
        <ProductionStrip show={prod} />

        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 id="drawer-title" className="text-lg font-semibold">
              Configure flag
            </h2>
            <p className="mt-1 break-all font-mono text-sm text-zinc-300">{current.key}</p>
          </div>
          <button
            ref={closeRef}
            onClick={requestClose}
            aria-label="Close"
            className="rounded-lg border border-white/10 px-2.5 py-1 text-zinc-400 transition hover:border-accent/60 hover:text-white"
          >
            ✕
          </button>
        </div>

        <dl className="mt-5 space-y-3 text-sm">
          <div>
            <dt className="text-xs uppercase tracking-wider text-zinc-500">Name</dt>
            <dd className="mt-0.5 text-zinc-100">{current.name}</dd>
          </div>
          <div>
            <dt className="text-xs uppercase tracking-wider text-zinc-500">Description</dt>
            <dd className="mt-0.5 whitespace-pre-wrap break-words text-zinc-300">
              {current.description || <span className="text-zinc-500">No description</span>}
            </dd>
          </div>
          <div className="flex flex-wrap gap-2">
            <span className="rounded-full bg-white/10 px-2.5 py-0.5 text-xs text-zinc-300">{current.variationType}</span>
            <span
              className={`rounded-full px-2.5 py-0.5 font-mono text-xs ${
                prod ? 'bg-red-500/20 text-red-300' : 'bg-accent/15 text-accent'
              }`}
            >
              {env}
            </span>
            <span
              className={`rounded-full px-2.5 py-0.5 text-xs ${
                enabled ? 'bg-emerald-500/15 text-emerald-300' : 'bg-white/10 text-zinc-400'
              }`}
            >
              {enabled ? 'Enabled' : 'Disabled'}
            </span>
          </div>
        </dl>

        <TargetingSection
          flag={current}
          env={env}
          prod={prod}
          canEdit={canEdit}
          disabledReason={reason}
          otherBusy={saving}
          onSaved={(updated) => {
            setCurrent(updated)
            onSaved(updated)
          }}
          onStateChange={setTargeting}
        />

        <section className="mt-7 border-t border-white/10 pt-6">
          <h3 className="text-xs font-medium uppercase tracking-wider text-zinc-500">Rollout</h3>

          {refreshWarning && (
            <p className="mt-3 text-xs text-amber-300">Couldn't refresh this flag; showing the last loaded values.</p>
          )}

          {!bools ? (
            <p className="mt-3 text-sm text-zinc-400">
              Percentage rollout is available for boolean flags with one true and one false variation only.
            </p>
          ) : (
            <div className="mt-3 space-y-4">
              {!enabled && (
                <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
                  This flag is disabled in {env}. A rollout can be saved, but it has no effect until the flag is enabled.
                </p>
              )}
              {!hasRollout(current) && (
                <p className="text-xs text-zinc-400">
                  No rollout is set: everyone currently gets {savedBp === 100_000 ? 'ON' : 'OFF'}, the flag's fallthrough value.
                </p>
              )}

              <RolloutControl
                savedBp={savedBp}
                pendingBp={pendingBp}
                disabled={!canEdit || saving}
                disabledReason={reason}
                onChange={(bp) => {
                  setPendingBp(bp)
                  setSavedNote(null)
                  setSaveError(null)
                }}
              />

              <p className="text-xs leading-relaxed text-zinc-500">
                Targeting rules are checked first: users who match a rule get that rule's value and ignore this
                percentage{rules > 0 ? ` (${rules} rule${rules === 1 ? '' : 's'} on this flag)` : ''}. Everyone else is
                assigned by a stable hash, so a user always gets the same value, and raising the percentage only adds
                users; it never removes anyone.
              </p>

              {!canEdit && <p className="text-xs text-zinc-500">{reason}.</p>}
              {slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}
              {savedNote && (
                <p role="status" className="rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-200">
                  {savedNote}
                </p>
              )}
              {saveError && (
                <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                  {saveError}
                </p>
              )}

              <div className="flex gap-2">
                <button
                  onClick={() => void onSaveClick()}
                  disabled={!dirty || !canEdit || saving || targeting.busy}
                  title={!canEdit ? reason : undefined}
                  className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white shadow-[0_8px_24px_-8px_rgba(124,92,255,0.8)] transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none"
                >
                  {saving && dialog === null ? 'Saving…' : 'Save rollout'}
                </button>
                <button
                  onClick={() => {
                    setPendingBp(savedBp)
                    setSaveError(null)
                  }}
                  disabled={!dirty || saving}
                  className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  Reset
                </button>
              </div>
            </div>
          )}
        </section>

        <DeleteFlagSection
          flagKey={current.key}
          env={env}
          prod={prod}
          roles={roles}
          onDeleted={onDeleted}
          onGone={onGone}
          onStateChange={setDeleting}
        />
      </aside>

      {dialog && (
        <ConfirmDialog
          title={
            dialog === 'discard'
              ? 'Discard unsaved changes?'
              : dialog === 'lower'
                ? `Lower rollout of ${current.key}?`
                : `Change rollout of ${current.key} in production?`
          }
          confirmLabel={dialog === 'discard' ? 'Discard' : dialog === 'lower' ? 'Lower rollout' : 'Save rollout'}
          tone={dialog === 'prod' ? 'primary' : 'danger'}
          busy={saving}
          error={dialogError}
          slowHint={saving && slow ? SLOW_HINT : null}
          onConfirm={() => void onDialogConfirm()}
          onCancel={() => setDialog(null)}
        >
          {dialog === 'discard' && (
            <>
              {dirty && <p>Your pending rollout change ({formatPercent(pendingBp)} ON) has not been saved.</p>}
              {targeting.dirty && <p>Your targeting rule changes have not been saved.</p>}
            </>
          )}
          {dialog === 'lower' && (
            <>
              <p>
                Lowering the rollout from <span className="font-semibold text-zinc-100">{formatPercent(savedBp)}</span> to{' '}
                <span className="font-semibold text-zinc-100">{formatPercent(pendingBp)}</span> removes the feature from
                users who currently have it. Raising a rollout never removes anyone.
              </p>
              {prod && (
                <p className="text-red-300">This is production: the change affects live users right away.</p>
              )}
            </>
          )}
          {dialog === 'prod' && (
            <p>
              This changes the live rollout in <span className="font-semibold text-red-300">production</span> from{' '}
              {formatPercent(savedBp)} to {formatPercent(pendingBp)} ON and takes effect for users right away.
            </p>
          )}
        </ConfirmDialog>
      )}
    </div>
  )
}
