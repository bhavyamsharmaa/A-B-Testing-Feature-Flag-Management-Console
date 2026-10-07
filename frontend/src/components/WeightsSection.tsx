import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { setFlagRollout } from '../api/flags'
import { describeError } from '../lib/errors'
import { TOTAL_BP, formatPercent, hasRollout } from '../lib/rollout'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import {
  describeChanges,
  distributeEvenly,
  parseWeights,
  sameWeights,
  savedWeights,
  textsFromWeights,
  weightsPayload,
} from '../lib/weights'
import type { Flag } from '../types'
import { ConfirmDialog } from './ConfirmDialog'

export interface WeightsState {
  dirty: boolean
  busy: boolean
  /** A confirmation is open, so the drawer must not treat Esc as "close". */
  dialogOpen: boolean
}

interface Props {
  flag: Flag
  env: string
  prod: boolean
  canEdit: boolean
  disabledReason: string
  /** Another section is saving: hold this Save so two PATCHes never overlap. */
  otherBusy: boolean
  onSaved: (flag: Flag) => void
  onStateChange: (state: WeightsState) => void
}

const input =
  'w-24 rounded-lg border bg-black/40 px-2.5 py-1.5 text-right font-mono text-sm outline-none transition focus:ring-4 disabled:cursor-not-allowed disabled:opacity-50'

/** Rollout weights for flags that aren't a plain on/off boolean: one percentage per variation. */
export function WeightsSection({ flag, env, prod, canEdit, disabledReason, otherBusy, onSaved, onStateChange }: Props) {
  const ids = useMemo(() => flag.variations.map((v) => v.id), [flag.variations])
  const saved = useMemo(() => savedWeights(flag), [flag])
  const savedKey = JSON.stringify(saved)
  const [texts, setTexts] = useState(() => textsFromWeights(saved))
  const [saving, setSaving] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [dialogError, setDialogError] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const slow = useSlowHint(saving)

  const state = parseWeights(ids, texts)
  const dirty = state.invalid.length > 0 || !sameWeights(state.weights, saved)
  const canSave = state.valid && !sameWeights(state.weights, saved)

  // Follow newer saved weights (the drawer's fresh read), but never discard edits in progress.
  const baseline = useRef(savedKey)
  useEffect(() => {
    if (baseline.current === savedKey) return
    const untouched = JSON.stringify(parseWeights(ids, texts).weights) === baseline.current
    baseline.current = savedKey
    if (untouched) setTexts(textsFromWeights(saved))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedKey])

  useEffect(() => {
    onStateChange({ dirty, busy: saving, dialogOpen: confirming })
  }, [dirty, saving, confirming, onStateChange])

  async function save(): Promise<string | null> {
    setSaving(true)
    try {
      const updated = await setFlagRollout(env, flag.key, weightsPayload(ids, state.weights))
      const next = savedWeights(updated)
      baseline.current = JSON.stringify(next)
      setTexts(textsFromWeights(next))
      setNote('Rollout weights saved.')
      onSaved(updated)
      return null
    } catch (err) {
      return describeError(err, `change the rollout in ${env}`)
    } finally {
      setSaving(false)
    }
  }

  function edit(next: Record<string, string>) {
    setTexts(next)
    setNote(null)
  }

  const changes = describeChanges(saved, state.weights, ids)
  const disabled = !canEdit || saving
  const enabled = flag.config.enabled

  return (
    <div className="mt-3 space-y-4">
      {!enabled && (
        <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
          This flag is disabled in {env}. Weights can be saved, but they have no effect until the flag is enabled.
        </p>
      )}
      {!hasRollout(flag) && (
        <p className="text-xs text-zinc-400">
          No rollout is set: everyone currently gets the default variation, {flag.config.fallthroughVariationId}.
        </p>
      )}

      <div className="space-y-2" title={disabled && !saving ? disabledReason : undefined}>
        {flag.variations.map((v, i) => {
          const invalid = state.invalid.includes(v.id)
          const bp = state.weights[v.id] ?? 0
          return (
            <div key={v.id} className="flex items-center gap-3">
              <span className="w-5 shrink-0 text-xs text-zinc-600">{i + 1}.</span>
              <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-zinc-100" title={v.id}>
                {v.id}
              </span>
              <div className="hidden h-2 w-24 shrink-0 overflow-hidden rounded-full bg-zinc-700 sm:flex" aria-hidden>
                <div className="bg-accent transition-all" style={{ width: `${Math.min(100, (bp / TOTAL_BP) * 100)}%` }} />
              </div>
              <label className="flex shrink-0 items-center gap-1 text-sm text-zinc-300">
                <input
                  className={`${input} ${invalid ? 'border-red-500/60 focus:border-red-500 focus:ring-red-500/20' : 'border-white/10 focus:border-accent focus:ring-accent/20'}`}
                  inputMode="decimal"
                  aria-label={`Percentage for ${v.id}`}
                  aria-invalid={invalid}
                  disabled={disabled}
                  value={texts[v.id] ?? ''}
                  onChange={(e) => edit({ ...texts, [v.id]: e.target.value })}
                />
                %
              </label>
            </div>
          )
        })}
      </div>

      {state.invalid.length > 0 && (
        <p className="text-xs text-red-400">Use a number from 0 to 100 with at most three decimals.</p>
      )}

      <div className="flex items-center justify-between gap-3">
        <p className={`text-sm font-medium ${state.valid ? 'text-emerald-300' : 'text-red-300'}`}>
          Total: {formatPercent(state.total)}
          {state.total !== TOTAL_BP && <span className="font-normal text-red-300"> (must be exactly 100%)</span>}
        </p>
        <button
          onClick={() => edit(textsFromWeights(distributeEvenly(ids)))}
          disabled={disabled}
          className="rounded-lg border border-white/10 px-3 py-1.5 text-xs text-zinc-300 transition hover:border-accent/60 hover:text-white disabled:cursor-not-allowed disabled:opacity-40"
        >
          Distribute evenly
        </button>
      </div>

      <p className="text-xs leading-relaxed text-zinc-500">
        Targeting rules are checked first; everyone else is assigned by a stable hash. Each variation gets a range in the
        flag's variation order, so changing one weight can move users between variations, not only add to one.
      </p>

      {!canEdit && <p className="text-xs text-zinc-500">{disabledReason}.</p>}
      {slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}
      {note && (
        <p role="status" className="rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-200">
          {note}
        </p>
      )}

      <div className="flex gap-2">
        <button
          onClick={() => {
            setDialogError(null)
            setNote(null)
            setConfirming(true)
          }}
          disabled={!canSave || !canEdit || saving || otherBusy}
          title={!canEdit ? disabledReason : undefined}
          className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white shadow-[0_8px_24px_-8px_rgba(124,92,255,0.8)] transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none"
        >
          Save weights
        </button>
        <button
          onClick={() => {
            setTexts(textsFromWeights(saved))
          }}
          disabled={!dirty || saving}
          className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-50"
        >
          Reset
        </button>
      </div>

      {confirming &&
        createPortal(
          <ConfirmDialog
            title={`Change rollout weights of ${flag.key}?`}
            confirmLabel="Save weights"
            tone={prod ? 'danger' : 'primary'}
            busy={saving}
            error={dialogError}
            slowHint={saving && slow ? SLOW_HINT : null}
            onConfirm={async () => {
              const err = await save()
              if (err) setDialogError(err)
              else setConfirming(false)
            }}
            onCancel={() => setConfirming(false)}
          >
            <p>
              Changing weights can move users between variations. Each variation owns a range in the flag's variation order,
              so a change to one weight can shift the boundaries of the variations after it.
            </p>
            {changes.length > 0 && (
              <ul className="rounded-lg border border-white/10 bg-black/30 px-3 py-2 font-mono text-xs text-zinc-200">
                {changes.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            )}
            {prod && <p className="text-red-300">This is production: the change affects live users right away.</p>}
          </ConfirmDialog>,
          document.body,
        )}
    </div>
  )
}
