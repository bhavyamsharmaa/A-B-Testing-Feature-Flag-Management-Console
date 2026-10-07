import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { ApiError } from '../api/client'
import { deleteFlag } from '../api/flags'
import { describeError } from '../lib/errors'
import { canDelete, deleteDisabledReason } from '../lib/permissions'
import { SLOW_HINT, useSlowHint } from '../lib/useSlowHint'
import type { EnvironmentRole } from '../types'

export interface DeleteState {
  dialogOpen: boolean
  busy: boolean
}

interface Props {
  flagKey: string
  env: string
  prod: boolean
  roles: EnvironmentRole[]
  onDeleted: (key: string) => void
  /** The flag no longer exists (404): the list needs a refresh. */
  onGone: () => void
  onStateChange: (state: DeleteState) => void
}

type Stage = 'delete' | 'force'

export function DeleteFlagSection({ flagKey, env, prod, roles, onDeleted, onGone, onStateChange }: Props) {
  const allowed = canDelete(roles, env)
  const [open, setOpen] = useState(false)
  const [stage, setStage] = useState<Stage>('delete')
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [inUse, setInUse] = useState<string | null>(null)
  const [gone, setGone] = useState(false)
  const slow = useSlowHint(busy)

  useEffect(() => {
    onStateChange({ dialogOpen: open, busy })
  }, [open, busy, onStateChange])

  // Esc cancels the dialog (never mid-request).
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  function openDialog() {
    setStage('delete')
    setTyped('')
    setError(null)
    setInUse(null)
    setGone(false)
    setOpen(true)
  }

  function close() {
    setOpen(false)
    if (gone) onGone()
  }

  const matches = typed === flagKey // exact and case-sensitive

  async function confirm() {
    if (!matches || busy) return
    setBusy(true)
    setError(null)
    try {
      await deleteFlag(env, flagKey, stage === 'force')
      setOpen(false)
      onDeleted(flagKey)
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && err.code === 'IN_USE' && stage === 'delete') {
        setInUse(err.message) // the backend's own message
        setStage('force')
        setTyped('') // forcing needs its own typed confirmation
      } else if (err instanceof ApiError && err.status === 404) {
        setGone(true)
        setError('This flag no longer exists. It may already have been deleted. Close this to refresh the list.')
      } else {
        setError(describeError(err, 'delete flags'))
      }
    } finally {
      setBusy(false)
    }
  }

  const forcing = stage === 'force'

  return (
    <section className="mt-7 rounded-xl border border-red-500/30 bg-red-500/[0.04] p-4">
      <h3 className="text-xs font-semibold uppercase tracking-wider text-red-300">Danger zone</h3>
      <p className="mt-2 text-sm text-zinc-300">Delete this flag from every environment. This cannot be undone.</p>
      <button
        onClick={openDialog}
        disabled={!allowed}
        title={allowed ? undefined : deleteDisabledReason(roles, env)}
        className="mt-3 rounded-lg border border-red-500/60 bg-red-600/90 px-4 py-2 text-sm font-semibold text-white transition hover:bg-red-500 disabled:cursor-not-allowed disabled:border-white/10 disabled:bg-transparent disabled:text-zinc-600 disabled:hover:bg-transparent"
      >
        Delete flag
      </button>
      {!allowed && <p className="mt-2 text-xs text-zinc-500">{deleteDisabledReason(roles, env)}.</p>}

      {open &&
        createPortal(
          <div
            className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 px-4 backdrop-blur-sm"
            onMouseDown={(e) => {
              if (e.target === e.currentTarget && !busy) close()
            }}
          >
            <div
              role="dialog"
              aria-modal="true"
              aria-labelledby="delete-title"
              className="w-full max-w-md animate-fade-up rounded-2xl border border-red-500/40 bg-surface p-6"
            >
              <h2 id="delete-title" className="text-lg font-semibold text-red-200">
                {forcing ? 'Force delete flag' : 'Delete flag'}
              </h2>

              <p className="mt-3 break-all rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 font-mono text-base font-semibold text-red-100">
                {flagKey}
              </p>

              <div className="mt-3 space-y-2 text-sm text-zinc-300">
                {forcing && inUse && (
                  <p role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-amber-200">
                    {inUse}
                  </p>
                )}
                {forcing ? (
                  <p>
                    Forcing skips that check and deletes the flag anyway. It is recorded in the audit log as a{' '}
                    <span className="font-semibold text-zinc-100">critical</span> entry (flag.force_delete).
                  </p>
                ) : (
                  <p>
                    This permanently deletes <span className="font-mono text-zinc-100">{flagKey}</span> from{' '}
                    <span className="font-semibold text-red-300">every environment, including production</span>, not just {env}.
                    Its rollout and targeting rules in every environment are removed. Apps that evaluate it get no value
                    (FLAG_NOT_FOUND) and fall back to their defaults.
                  </p>
                )}
                {prod && <p className="text-red-300">You opened this from production: the change affects live users right away.</p>}
              </div>

              <label htmlFor="delete-confirm" className="mt-4 block text-sm text-zinc-300">
                {forcing ? 'Type the flag key again to force delete' : 'Type the flag key to confirm'}
              </label>
              <input
                id="delete-confirm"
                name="confirm-flag-key"
                autoFocus
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void confirm()
                }}
                disabled={busy || gone}
                autoComplete="off"
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
                placeholder={flagKey}
                className="mt-1 w-full rounded-lg border border-white/10 bg-black/40 px-3 py-2 font-mono text-sm outline-none transition focus:border-red-500/60 focus:ring-4 focus:ring-red-500/20 disabled:opacity-60"
              />
              {typed !== '' && !matches && <p className="mt-1 text-xs text-zinc-500">The key must match exactly, including capitalization.</p>}

              {slow && <p className="mt-3 text-sm text-amber-300">{SLOW_HINT}</p>}
              {error && (
                <p role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                  {error}
                </p>
              )}

              <div className="mt-6 flex justify-end gap-2">
                <button
                  onClick={close}
                  disabled={busy}
                  className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50"
                >
                  {gone ? 'Close and refresh' : 'Cancel'}
                </button>
                {!gone && (
                  <button
                    onClick={() => void confirm()}
                    disabled={!matches || busy}
                    className="rounded-lg bg-red-600 px-4 py-2 text-sm font-semibold text-white shadow-[0_8px_24px_-8px_rgba(239,68,68,0.8)] transition hover:bg-red-500 disabled:cursor-not-allowed disabled:opacity-40 disabled:shadow-none"
                  >
                    {busy ? 'Deleting…' : forcing ? 'Force delete' : 'Delete flag'}
                  </button>
                )}
              </div>
            </div>
          </div>,
          document.body,
        )}
    </section>
  )
}
