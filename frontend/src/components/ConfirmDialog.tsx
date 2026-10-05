import { useEffect, type ReactNode } from 'react'

interface Props {
  title: string
  children: ReactNode
  confirmLabel: string
  tone: 'danger' | 'primary'
  busy: boolean
  error: string | null
  slowHint: string | null
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({ title, children, confirmLabel, tone, busy, error, slowHint, onConfirm, onCancel }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [busy, onCancel])

  const confirmStyle =
    tone === 'danger'
      ? 'bg-red-600 hover:bg-red-500 shadow-[0_8px_24px_-8px_rgba(239,68,68,0.8)]'
      : 'bg-gradient-to-r from-accent to-indigo-500 hover:brightness-110'

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 px-4 backdrop-blur-sm"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && !busy) onCancel()
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        className="w-full max-w-md animate-fade-up rounded-2xl border border-white/10 bg-surface p-6"
      >
        <h2 id="confirm-title" className="text-lg font-semibold">
          {title}
        </h2>
        <div className="mt-2 space-y-2 text-sm text-zinc-300">{children}</div>

        {slowHint && <p className="mt-3 text-sm text-amber-300">{slowHint}</p>}
        {error && (
          <p role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </p>
        )}

        <div className="mt-6 flex justify-end gap-2">
          <button
            onClick={onCancel}
            disabled={busy}
            className="rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            disabled={busy}
            className={`rounded-lg px-4 py-2 text-sm font-medium text-white transition disabled:cursor-not-allowed disabled:opacity-60 ${confirmStyle}`}
          >
            {busy ? 'Working…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
