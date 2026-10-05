export interface NoticeState {
  kind: 'success' | 'error'
  text: string
}

export function Notice({ notice, onDismiss }: { notice: NoticeState; onDismiss: () => void }) {
  const style =
    notice.kind === 'success'
      ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-200'
      : 'border-red-500/40 bg-red-500/10 text-red-200'
  return (
    <div role={notice.kind === 'error' ? 'alert' : 'status'} className={`flex items-start justify-between gap-3 rounded-lg border px-3.5 py-2.5 text-sm ${style}`}>
      <p>{notice.text}</p>
      <button onClick={onDismiss} aria-label="Dismiss" className="opacity-60 transition hover:opacity-100">
        ✕
      </button>
    </div>
  )
}
