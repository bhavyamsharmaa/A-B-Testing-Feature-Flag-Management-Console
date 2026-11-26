import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError } from '../api/client'
import { createFlag, getFlag, setFlagServed } from '../api/flags'
import { DISCOUNT_PARTS, ONES_FLAG, TENS_FLAG, joinDiscount, splitDiscount, DEFAULT_DISCOUNT } from '../lib/bannerDiscount'
import { describeError } from '../lib/errors'
import type { Flag } from '../types'

const SAVE_DELAY_MS = 300 // while dragging, save once the slider pauses

interface Props {
  env: string
  prod: boolean
  canEdit: boolean
  disabledReason: string
  canCreate: boolean
}

type Load = 'loading' | 'missing' | 'ready' | 'failed'

/** What this flag serves to everyone, or null while it is disabled. */
function served(flag: Flag): unknown {
  if (!flag.config.enabled) return null
  return flag.variations.find((v) => v.id === flag.config.fallthroughVariationId)?.value
}

/** Live 0-100% control for the discount printed on the demo banner. */
export function BannerDiscountSection({ env, prod, canEdit, disabledReason, canCreate }: Props) {
  const [load, setLoad] = useState<Load>('loading')
  const [value, setValue] = useState(DEFAULT_DISCOUNT)
  const [saved, setSaved] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const parts = useRef<{ tens: Flag; ones: Flag } | null>(null)
  const wanted = useRef<number | null>(null) // newest value not yet saved
  const saving = useRef(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const refresh = useCallback(async () => {
    try {
      const [tens, ones] = await Promise.all([getFlag(env, TENS_FLAG), getFlag(env, ONES_FLAG)])
      parts.current = { tens, ones }
      const current = joinDiscount(served(tens), served(ones))
      setSaved(current)
      if (wanted.current === null) setValue(current)
      setLoad('ready')
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) setLoad('missing')
      else {
        setError(describeError(err, `read the banner discount in ${env}`))
        setLoad('failed')
      }
    }
  }, [env])

  useEffect(() => void refresh(), [refresh])

  /** Saves the newest value; runs one save at a time so an older one can never land last. */
  const flush = useCallback(async () => {
    if (saving.current) return
    saving.current = true
    setBusy(true)
    while (wanted.current !== null && parts.current) {
      const target = wanted.current
      wanted.current = null
      const ids = splitDiscount(target)
      const changes = (['tens', 'ones'] as const).filter((p) => {
        const f = parts.current![p]
        return !f.config.enabled || f.config.fallthroughVariationId !== ids[p]
      })
      // Sent together so the banner sees both halves at almost the same moment.
      const results = await Promise.allSettled(changes.map((p) => setFlagServed(env, parts.current![p].key, ids[p])))
      let failure: unknown = null
      results.forEach((r, i) => {
        if (r.status === 'fulfilled') parts.current![changes[i]] = r.value
        else failure = r.reason
      })
      setSaved(joinDiscount(served(parts.current.tens), served(parts.current.ones)))
      if (failure) {
        wanted.current = null
        setError(describeError(failure, `change the banner discount in ${env}`))
        await refresh() // show what is really live
      }
    }
    saving.current = false
    setBusy(false)
  }, [env, refresh])

  // A change still waiting for the timer is saved when the drawer closes.
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current)
      void flush()
    },
    [flush],
  )

  function change(next: number) {
    setValue(next)
    setError(null)
    wanted.current = next
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => void flush(), SAVE_DELAY_MS)
  }

  async function setUp() {
    setBusy(true)
    setError(null)
    try {
      for (const part of DISCOUNT_PARTS) {
        try {
          await createFlag(env, {
            key: part.key,
            name: part.name,
            description: 'Used by the discount slider of demo-banner.',
            variationType: 'number',
            variations: [...part.variations],
          })
        } catch (err) {
          if (!(err instanceof ApiError && err.status === 409)) throw err // already there is fine
        }
      }
      await refresh()
    } catch (err) {
      setError(describeError(err, 'set up the banner discount'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="mt-7 border-t border-white/10 pt-6">
      <h3 className="text-xs font-medium uppercase tracking-wider text-zinc-500">Banner discount</h3>

      {load === 'loading' && <p className="mt-3 text-sm text-zinc-400">Loading…</p>}

      {load === 'missing' && (
        <div className="mt-3 space-y-3">
          <p className="text-xs text-zinc-400">
            The banner shows {DEFAULT_DISCOUNT}% off. Set up the slider once to control the discount from here.
          </p>
          <button
            onClick={() => void setUp()}
            disabled={!canCreate || busy}
            className="rounded-lg bg-gradient-to-r from-accent to-indigo-500 px-4 py-2 text-sm font-medium text-white transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy ? 'Setting up…' : 'Set up discount slider'}
          </button>
        </div>
      )}

      {load === 'ready' && (
        <div className="mt-3 space-y-3">
          <div className="flex items-baseline justify-between">
            <span className="text-3xl font-semibold text-white">{value}% off</span>
            <span role="status" className="text-xs text-zinc-400">
              {busy ? 'Saving…' : saved === value ? `Live in ${env}` : ''}
            </span>
          </div>
          <input
            type="range"
            min={0}
            max={100}
            step={1}
            value={value}
            disabled={!canEdit}
            title={canEdit ? undefined : disabledReason}
            aria-label="Banner discount percent"
            onChange={(e) => change(Number(e.target.value))}
            className="w-full accent-accent disabled:cursor-not-allowed disabled:opacity-50"
          />
          <div className="flex justify-between text-xs text-zinc-500">
            <span>0%</span>
            <span>50%</span>
            <span>100%</span>
          </div>
          <p className={`text-xs ${prod ? 'text-red-300' : 'text-zinc-500'}`}>
            {prod ? 'This is production: ' : ''}the slider saves as you move it and the banner updates live. Switching
            {' '}demo-banner off still hides the whole banner.
          </p>
          {!canEdit && <p className="text-xs text-zinc-500">{disabledReason}.</p>}
        </div>
      )}

      {error && (
        <p role="alert" className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {error}
        </p>
      )}
    </section>
  )
}
