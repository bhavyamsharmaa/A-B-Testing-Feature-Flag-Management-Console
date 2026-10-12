import { useEffect, useId, type ReactNode } from 'react'

interface Props {
  title: string
  onClose: () => void
  /** While busy the dialog can't be dismissed. */
  busy?: boolean
  /** A roomier frame for forms with lists (the segment editor). */
  wide?: boolean
  children: ReactNode
}

/** The shared dialog frame: backdrop, Escape and outside-click to close, labelled for screen readers. */
export function Modal({ title, onClose, busy = false, wide = false, children }: Props) {
  const titleId = useId()
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [busy, onClose])

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 px-4 backdrop-blur-sm"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && !busy) onClose()
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={`max-h-[92vh] w-full animate-fade-up overflow-y-auto rounded-2xl border border-white/10 bg-surface p-6 ${wide ? 'max-w-3xl' : 'max-w-md'}`}
      >
        <h2 id={titleId} className="text-lg font-semibold">
          {title}
        </h2>
        {children}
      </div>
    </div>
  )
}
