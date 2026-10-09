import { useState } from 'react'

/** A read-only value with a Copy button; selects the text if the clipboard API is unavailable. */
export function CopyField({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      /* the field is selectable, so the user can copy it by hand */
    }
  }

  return (
    <div className="flex gap-2">
      <input
        readOnly
        aria-label={label}
        value={value}
        onFocus={(e) => e.currentTarget.select()}
        className="min-w-0 flex-1 rounded-lg border border-white/10 bg-black/40 px-3 py-2 font-mono text-xs text-zinc-200 outline-none focus:border-accent"
      />
      <button
        type="button"
        onClick={() => void copy()}
        className="rounded-lg border border-white/10 px-3 py-2 text-xs text-zinc-200 transition hover:border-accent/60 hover:bg-accent/10"
      >
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}
