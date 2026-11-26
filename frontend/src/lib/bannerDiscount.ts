// Discount banners. A discount banner is a boolean flag (on / off) plus a
// 0-100% value. A flag holds at most 20 variations, so the value lives in two
// hidden number flags: "<key>-discount-tens" serves 0, 10, ... 100 and
// "<key>-discount-ones" serves 0 ... 9. The discount is their sum.

import type { Flag } from '../types'

/** The banner shown on /demo. Always treated as a discount banner. */
export const BANNER_FLAG = 'demo-banner'

export const tensKey = (key: string) => `${key}-discount-tens`
export const onesKey = (key: string) => `${key}-discount-ones`

/** Longest banner key that still leaves room for the suffix (flag keys: 128). */
export const MAX_BANNER_KEY = 128 - tensKey('').length

/** Shown while the discount is not set (flags missing or disabled). */
export const DEFAULT_DISCOUNT = 20

export const DISCOUNT_PARTS = {
  tens: {
    key: tensKey,
    label: 'tens',
    variations: Array.from({ length: 11 }, (_, i) => ({ id: `d${i * 10}`, value: i * 10 })),
  },
  ones: {
    key: onesKey,
    label: 'ones',
    variations: Array.from({ length: 10 }, (_, i) => ({ id: `d${i}`, value: i })),
  },
} as const

export type DiscountPart = keyof typeof DISCOUNT_PARTS

export const clampDiscount = (n: number) => Math.min(100, Math.max(0, Math.round(n)))

/** The variation each flag must serve for `percent`. */
export function splitDiscount(percent: number): Record<DiscountPart, string> {
  const p = clampDiscount(percent)
  return { tens: `d${p - (p % 10)}`, ones: `d${p % 10}` }
}

/** Joins the two evaluated values; anything that is not a number means "use the default". */
export function joinDiscount(tens: unknown, ones: unknown): number {
  if (typeof tens !== 'number' || typeof ones !== 'number') return DEFAULT_DISCOUNT
  return clampDiscount(tens + ones)
}

/** What a flag serves to everyone, or null while it is missing or disabled. */
export function servedValue(flag: Flag | undefined): unknown {
  if (!flag?.config.enabled) return null
  return flag.variations.find((v) => v.id === flag.config.fallthroughVariationId)?.value
}

export interface DiscountBanner {
  tens: Flag | undefined
  ones: Flag | undefined
}

/** Splits the flag list into what to show and, per banner key, its hidden discount flags. */
export function groupBanners(flags: Flag[]): { visible: Flag[]; banners: Map<string, DiscountBanner> } {
  const byKey = new Map(flags.map((f) => [f.key, f]))
  const banners = new Map<string, DiscountBanner>()
  const hidden = new Set<string>()
  for (const flag of flags) {
    if (flag.variationType !== 'boolean') continue
    const part = (key: string) => (byKey.get(key)?.variationType === 'number' ? byKey.get(key) : undefined)
    const tens = part(tensKey(flag.key))
    const ones = part(onesKey(flag.key))
    if (!tens && !ones && flag.key !== BANNER_FLAG) continue
    banners.set(flag.key, { tens, ones })
    if (tens) hidden.add(tens.key)
    if (ones) hidden.add(ones.key)
  }
  return { visible: flags.filter((f) => !hidden.has(f.key)), banners }
}
