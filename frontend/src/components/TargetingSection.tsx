import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ApiError } from '../api/client'
import { setFlagTargetingRules } from '../api/flags'
import { describeError } from '../lib/errors'
import {
  canon,
  fromSaved,
  hasErrors,
  parseServerRuleError,
  toPayload,
  validate,
  type DraftRule,
  type RuleErrors,
  type ServerRuleError,
} from '../lib/targeting'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { Flag } from '../types'
import { ConfirmDialog } from './ConfirmDialog'
import { TargetingEditor } from './TargetingEditor'

export interface TargetingState {
  dirty: boolean
  busy: boolean
  /** A confirmation dialog is open, so the drawer must not treat Esc as "close". */
  dialogOpen: boolean
}

interface Props {
  flag: Flag
  env: string
  prod: boolean
  canEdit: boolean
  disabledReason: string
  /** The rollout is saving: hold this section's save so two PATCHes never overlap. */
  otherBusy: boolean
  onSaved: (flag: Flag) => void
  onStateChange: (state: TargetingState) => void
}

type SaveResult = { kind: 'ok' } | { kind: 'rule' } | { kind: 'error'; message: string }

export function TargetingSection({ flag, env, prod, canEdit, disabledReason, otherBusy, onSaved, onStateChange }: Props) {
  const saved = flag.config.targetingRules
  const savedKey = useMemo(() => canon(saved), [saved])
  const [draft, setDraft] = useState<DraftRule[]>(() => fromSaved(saved))
  const [showErrors, setShowErrors] = useState(false)
  const [serverError, setServerError] = useState<ServerRuleError | null>(null)
  const [saving, setSaving] = useState(false)
  const [sectionError, setSectionError] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [dialogError, setDialogError] = useState<string | null>(null)
  const slow = useSlowHint(saving)

  const payload = useMemo(() => toPayload(draft), [draft])
  const dirty = canon(payload) !== savedKey
  const variationIds = useMemo(() => new Set(flag.variations.map((v) => v.id)), [flag.variations])
  const errors: RuleErrors[] = useMemo(() => validate(draft, variationIds), [draft, variationIds])

  // When the saved rules change underneath us (the drawer's fresh read), follow
  // them, but never discard edits in progress.
  const baseline = useRef(savedKey)
  useEffect(() => {
    if (baseline.current === savedKey) return
    const wasUntouched = canon(toPayload(draft)) === baseline.current
    baseline.current = savedKey
    if (wasUntouched) setDraft(fromSaved(saved))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedKey])

  useEffect(() => {
    onStateChange({ dirty, busy: saving, dialogOpen: confirming })
  }, [dirty, saving, confirming, onStateChange])

  function edit(next: DraftRule[]) {
    setDraft(next)
    setServerError(null)
    setSectionError(null)
    setNote(null)
  }

  async function save(): Promise<SaveResult> {
    setSaving(true)
    try {
      const updated = await setFlagTargetingRules(env, flag.key, payload)
      baseline.current = canon(updated.config.targetingRules)
      setDraft(fromSaved(updated.config.targetingRules))
      setShowErrors(false)
      const n = updated.config.targetingRules.length
      setNote(`Targeting rules saved (${n} rule${n === 1 ? '' : 's'}).`)
      onSaved(updated)
      return { kind: 'ok' }
    } catch (err) {
      if (err instanceof ApiError && err.status === 400) {
        const parsed = parseServerRuleError(err.message)
        if (parsed) {
          setServerError(parsed)
          return { kind: 'rule' }
        }
      }
      return { kind: 'error', message: describeError(err, `change targeting rules in ${env}`) }
    } finally {
      setSaving(false)
    }
  }

  async function onSaveClick() {
    setNote(null)
    setSectionError(null)
    setServerError(null)
    setDialogError(null)
    if (hasErrors(errors)) {
      setShowErrors(true)
      return
    }
    if (prod) {
      setConfirming(true)
      return
    }
    const res = await save()
    if (res.kind === 'error') setSectionError(res.message)
  }

  async function onConfirm() {
    const res = await save()
    if (res.kind === 'error') setDialogError(res.message)
    else setConfirming(false) // ok, or a rule error shown inline on that rule
  }

  const disabled = !canEdit || saving
  const enabled = flag.config.enabled

  return (
    <section className="mt-7 border-t border-white/10 pt-6">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-xs font-medium uppercase tracking-wider text-zinc-500">Targeting</h3>
        {dirty && (
          <span className="flex items-center gap-1.5 text-xs text-amber-300">
            <span className="h-1.5 w-1.5 rounded-full bg-amber-400" />
            Unsaved changes
          </span>
        )}
      </div>
      <p className="mt-1 text-xs text-zinc-400">Rules run top to bottom. The first match wins.</p>

      <div className="mt-3 space-y-4">
        {!enabled && (
          <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
            This flag is disabled in {env}. Rules can be saved, but they have no effect until the flag is enabled.
          </p>
        )}

        <TargetingEditor
          rules={draft}
          variations={flag.variations}
          errors={showErrors ? errors : null}
          serverError={serverError}
          disabled={disabled}
          onChange={edit}
        />

        <p className="text-xs leading-relaxed text-zinc-500">
          A user missing an attribute does not match that clause. Rules run before the rollout percentage. Attribute names
          are exact, case-sensitive keys, and values must match in type (the text "5" is not the number 5).
        </p>

        {!canEdit && <p className="text-xs text-zinc-500">{disabledReason}.</p>}
        {slow && <p className="text-sm text-amber-300">{SLOW_HINT}</p>}
        {note && (
          <p role="status" className="rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-200">
            {note}
          </p>
        )}
        {sectionError && (
          <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {sectionError}
          </p>
        )}
        {showErrors && hasErrors(errors) && (
          <p role="alert" className="text-xs text-red-400">
            Fix the highlighted problems before saving.
          </p>
        )}

        <div className="flex gap-2">
          <button
            onClick={() => void onSaveClick()}
            disabled={!dirty || !canEdit || saving || otherBusy}
            title={!canEdit ? disabledReason : undefined}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white shadow-[0_8px_24px_-8px_rgba(124,92,255,0.8)] transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none"
          >
            {saving && !confirming ? 'Saving…' : 'Save rules'}
          </button>
          <button
            onClick={() => {
              setDraft(fromSaved(saved))
              setShowErrors(false)
              setServerError(null)
              setSectionError(null)
            }}
            disabled={!dirty || saving}
            className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Discard
          </button>
        </div>

        <details className="rounded-lg border border-white/10 bg-black/20">
          <summary className="cursor-pointer select-none px-3 py-2 text-xs text-zinc-400 hover:text-zinc-200">Show JSON</summary>
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all border-t border-white/10 p-3 font-mono text-xs text-zinc-300">
            {JSON.stringify({ targetingRules: payload }, null, 2)}
          </pre>
        </details>
      </div>

      {confirming &&
        createPortal(
          <ConfirmDialog
            title={`Change targeting rules of ${flag.key} in production?`}
            confirmLabel="Save rules"
            tone="primary"
            busy={saving}
            error={dialogError}
            slowHint={saving && slow ? SLOW_HINT : null}
            onConfirm={() => void onConfirm()}
            onCancel={() => setConfirming(false)}
          >
            <p>
              This changes live targeting in <span className="font-semibold text-red-300">production</span> and takes effect for
              users right away.
            </p>
          </ConfirmDialog>,
          document.body,
        )}
    </section>
  )
}
