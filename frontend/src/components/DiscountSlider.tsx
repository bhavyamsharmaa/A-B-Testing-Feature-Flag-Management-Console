import { useCallback, useEffect, useRef, useState } from 'react'
import { joinDiscount, servedValue, type DiscountBanner } from '../lib/bannerDiscount'
import { describeError } from '../lib/errors'
import { saveDiscount } from '../lib/saveDiscount'
import type { Flag } from '../types'

const SAVE_DELAY_MS = 300 // while dragging, save once the slider pauses

interface Props {
  env: string
  bannerKey: string
  banner: DiscountBanner
  disabled: boolean
  disabledReason: string
  onFlag: (flag: Flag) => void
  onFailed: () => void
}

/** The 0-100% control shown under a discount banner. Changes go live as it moves. */
export function DiscountSlider({ env, bannerKey, banner, disabled, disabledReason, onFlag, onFailed }: Props) {
  const live = joinDiscount(servedValue(banner.tens), servedValue(banner.ones))
  const [draft, setDraft] = useState<number | null>(null) // null: show the live value
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const parts = useRef(banner)
  const wanted = useRef<number | null>(null) // newest value not yet saved
  const running = useRef(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const callbacks = useRef({ onFlag, onFailed })
  callbacks.current = { onFlag, onFailed }
  if (!running.current) parts.current = banner

  /** Saves the newest value; one save at a time, so an older one can never land last. */
  const flush = useCallback(async () => {
    if (running.current || wanted.current === null) return
    running.current = true
    setSaving(true)
    while (wanted.current !== null) {
      const target = wanted.current
      wanted.current = null
      try {
        await saveDiscount(env, bannerKey, parts.current, target, (part, flag) => {
          parts.current = { ...parts.current, [part]: flag }
          callbacks.current.onFlag(flag)
        })
      } catch (err) {
        wanted.current = null
        setError(describeError(err, `change the discount in ${env}`))
        callbacks.current.onFailed() // show what is really live
      }
    }
    running.current = false
    setSaving(false)
    setDraft(null)
  }, [env, bannerKey])

  // A change still waiting for the timer is saved if the row goes away.
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current)
      void flush()
    },
    [flush],
  )

  function change(next: number) {
    setDraft(next)
    setError(null)
    wanted.current = next
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => void flush(), SAVE_DELAY_MS)
  }

  const value = draft ?? live

  return (
    <div>
      <div className="flex items-center gap-4">
        <span className="text-xs text-zinc-400">Discount</span>
        <input
          type="range"
          min={0}
          max={100}
          step={1}
          value={value}
          disabled={disabled}
          title={disabled ? disabledReason : undefined}
          aria-label={`Discount percent for ${bannerKey}`}
          onChange={(e) => change(Number(e.target.value))}
          className="min-w-0 flex-1 accent-accent disabled:cursor-not-allowed disabled:opacity-50"
        />
        <span className="w-20 text-right text-sm font-semibold text-white">{value}% off</span>
        <span role="status" className="w-14 text-xs text-zinc-500">
          {saving || draft !== null ? 'Saving…' : 'Saved'}
        </span>
      </div>
      {error && (
        <p role="alert" className="mt-2 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-1.5 text-xs text-red-300">
          {error}
        </p>
      )}
    </div>
  )
}
