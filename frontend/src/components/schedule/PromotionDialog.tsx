import { useEffect, useRef, useState } from 'react'
import { ApiError } from '../../api/client'
import { applyPromotion, previewPromotion } from '../../api/scheduling'
import { describeError } from '../../lib/errors'
import { SLOW_HINT, useSlowHint } from '../../lib/useSlowHint'
import type { EnvironmentRole, Flag, PromotionPart, PromotionPreview } from '../../types'
import { ConfirmDialog } from '../ConfirmDialog'
import { Modal } from '../Modal'
import { field } from '../TargetingEditor'

interface Props {
  flag: Flag
  /** The TARGET: the environment the drawer shows. */
  env: string
  prod: boolean
  roles: EnvironmentRole[]
  /** Whether the user's role may edit this flag in the target (the gate an edit has). */
  canEdit: boolean
  disabledReason: string
  onClose: () => void
  onApplied: (flag: Flag) => void
}

const PART_LABEL: Record<PromotionPart, string> = {
  rules: 'Targeting rules',
  rollout: 'Rollout',
  fallthrough: 'Fallthrough variation',
  enabled: 'Enabled state',
}

const DEFAULT_PARTS: PromotionPart[] = ['rules', 'rollout', 'fallthrough']

const show = (v: unknown) => {
  const text = JSON.stringify(v, null, 1)
  return text.length > 600 ? `${text.slice(0, 600)}…` : text
}

/**
 * Copy this flag's configuration into {env} from another environment: preview the difference, then apply.
 * The enabled state is not copied unless asked for. Applying is an ordinary edit of this environment (same role,
 * audit and fan-out), and fails, writing nothing, if either side changed since the preview.
 */
export function PromotionDialog({ flag, env, prod, roles, canEdit, disabledReason, onClose, onApplied }: Props) {
  const sources = roles.map((r) => r.environment).filter((e) => e !== env)
  const [source, setSource] = useState(sources.includes('staging') && env !== 'staging' ? 'staging' : (sources[0] ?? ''))
  const [parts, setParts] = useState<PromotionPart[]>(DEFAULT_PARTS)
  const [preview, setPreview] = useState<PromotionPreview | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const seq = useRef(0)
  const slow = useSlowHint(busy)

  useEffect(() => {
    if (!source) return
    const id = ++seq.current
    setLoading(true)
    setError(null)
    setStale(false)
    previewPromotion(env, flag.key, { sourceEnvironment: source, parts })
      .then((p) => {
        if (id === seq.current) setPreview(p)
      })
      .catch((err) => {
        if (id === seq.current) {
          setPreview(null)
          setError(describeError(err, `preview a promotion into ${env}`))
        }
      })
      .finally(() => {
        if (id === seq.current) setLoading(false)
      })
  }, [env, flag.key, source, parts, attempt])

  function toggle(p: PromotionPart) {
    setParts((cur) => (cur.includes(p) ? cur.filter((x) => x !== p) : [...cur, p]))
    setApplyError(null)
  }

  async function apply() {
    if (!preview) return
    setBusy(true)
    setApplyError(null)
    try {
      const updated = await applyPromotion(env, flag.key, {
        sourceEnvironment: source, parts, expectedVersion: preview.expectedVersion, sourceVersion: preview.sourceVersion,
      })
      onApplied(updated)
      onClose()
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && ['VERSION_CONFLICT', 'SOURCE_CHANGED'].includes(err.code)) setStale(true)
      setApplyError(describeError(err, `promote into ${env}`))
      setConfirming(false)
    } finally {
      setBusy(false)
    }
  }

  const selected = preview?.parts.filter((p) => p.selected) ?? []
  const canApply = preview?.canApply === true && canEdit && !stale

  return (
    <>
      <Modal title={`Promote ${flag.key} into ${env}`} onClose={onClose} busy={busy} wide>
        <div className="mt-4 space-y-4">
          <p className="text-sm text-zinc-400">
            Copy this flag&rsquo;s configuration from another environment into <span className="font-mono text-zinc-200">{env}</span>. You see the
            difference first; nothing changes until you apply. Applying is an ordinary edit of {env}: it needs the same role, is recorded in the
            audit log with the source named, and is refused (writing nothing) if either side changed since this preview.
          </p>

          {sources.length === 0 ? (
            <p className="rounded-lg border border-dashed border-white/10 px-4 py-6 text-center text-sm text-zinc-400">This workspace has no other environment to promote from.</p>
          ) : (
            <>
              <div>
                <label htmlFor="promo-source" className="block text-xs font-medium uppercase tracking-wider text-zinc-500">
                  Copy from
                </label>
                <select id="promo-source" className={`${field} mt-1 w-full sm:w-64`} value={source} disabled={busy} onChange={(e) => setSource(e.target.value)}>
                  {sources.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
              </div>

              <fieldset className="space-y-2" disabled={busy}>
                <legend className="text-xs font-medium uppercase tracking-wider text-zinc-500">What to copy</legend>
                {(['rules', 'rollout', 'fallthrough', 'enabled'] as PromotionPart[]).map((p) => (
                  <label key={p} className="flex items-start gap-2 text-sm">
                    <input type="checkbox" className="mt-1" checked={parts.includes(p)} onChange={() => toggle(p)} />
                    <span>
                      {PART_LABEL[p]}
                      {p === 'enabled' && (
                        <span className="block text-xs text-zinc-500">
                          Not copied by default: turning a flag on in {prod ? 'production' : env} is a separate decision.
                        </span>
                      )}
                    </span>
                  </label>
                ))}
              </fieldset>

              {error && (
                <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                  <p>{error}</p>
                  <button className="mt-2 underline" onClick={() => setAttempt((n) => n + 1)}>
                    Retry
                  </button>
                </div>
              )}
              {loading && !preview && (
                <div className="space-y-2" role="status" aria-label="Comparing environments">
                  {[0, 1].map((i) => (
                    <div key={i} className="h-12 animate-shimmer rounded-lg bg-white/[0.06]" style={{ animationDelay: `${i * 0.15}s` }} />
                  ))}
                </div>
              )}

              {preview && (
                <section aria-label="Difference" aria-busy={loading} className="space-y-2">
                  <h3 className="text-xs font-medium uppercase tracking-wider text-zinc-500">
                    Difference: {preview.source.environment} (version {preview.source.version}) into {preview.target.environment} (version {preview.target.version})
                  </h3>
                  <ul className="space-y-2">
                    {preview.parts.map((p) => (
                      <li key={p.part} className={`rounded-lg border p-3 text-sm ${p.selected ? 'border-white/15' : 'border-white/5 opacity-70'}`} data-testid={`promo-part-${p.part}`}>
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <span className="font-medium">{PART_LABEL[p.part]}</span>
                          <span className="text-xs text-zinc-300">
                            {p.changed ? '≠ Differs' : '= Same in both'} · {p.selected ? 'will be copied' : 'not selected'}
                          </span>
                        </div>
                        {p.changed && (
                          <div className="mt-2 grid gap-2 sm:grid-cols-2">
                            <div>
                              <p className="text-[11px] uppercase tracking-wider text-zinc-500">Now in {preview.target.environment}</p>
                              <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded bg-black/40 p-2 font-mono text-xs text-zinc-300">{show(p.target)}</pre>
                            </div>
                            <div>
                              <p className="text-[11px] uppercase tracking-wider text-zinc-500">After, from {preview.source.environment}</p>
                              <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded bg-black/40 p-2 font-mono text-xs text-zinc-300">{show(p.source)}</pre>
                            </div>
                          </div>
                        )}
                      </li>
                    ))}
                  </ul>
                  {preview.problems.length > 0 && (
                    <div role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                      <p className="font-medium">This cannot be applied:</p>
                      <ul className="list-disc pl-5">
                        {preview.problems.map((x) => (
                          <li key={x}>{x}</li>
                        ))}
                      </ul>
                    </div>
                  )}
                  {preview.nothingToApply && selected.length > 0 && <p className="text-sm text-zinc-400">The selected parts are already the same in both environments: nothing to copy.</p>}
                  {selected.length === 0 && <p className="text-sm text-zinc-400">Select at least one part to copy.</p>}
                </section>
              )}
            </>
          )}

          {!canEdit && <p className="text-xs text-zinc-500">{disabledReason}.</p>}
          {stale && (
            <p role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
              Something changed since this preview. Nothing was written.{' '}
              <button className="underline" onClick={() => setAttempt((n) => n + 1)}>
                Preview again
              </button>
            </p>
          )}
          {applyError && !stale && (
            <p role="alert" className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
              {applyError}
            </p>
          )}

          <div className="flex justify-end gap-2 border-t border-white/5 pt-4">
            <button onClick={onClose} disabled={busy} className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50">
              Close
            </button>
            <button
              onClick={() => (prod ? setConfirming(true) : void apply())}
              disabled={!canApply || busy || loading}
              className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {busy ? 'Applying…' : `Apply to ${env}`}
            </button>
          </div>
        </div>
      </Modal>
      {confirming && (
        <ConfirmDialog
          title={`Change ${flag.key} in production?`}
          confirmLabel="Apply to production"
          tone="primary"
          busy={busy}
          error={applyError}
          slowHint={busy && slow ? SLOW_HINT : null}
          onConfirm={() => void apply()}
          onCancel={() => setConfirming(false)}
        >
          <p>
            This copies {selected.filter((p) => p.changed).map((p) => PART_LABEL[p.part].toLowerCase()).join(', ') || 'nothing'} from{' '}
            <span className="font-mono">{source}</span> into production.
          </p>
        </ConfirmDialog>
      )}
    </>
  )
}
