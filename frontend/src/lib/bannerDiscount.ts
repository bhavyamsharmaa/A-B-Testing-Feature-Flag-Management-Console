// The discount on the demo banner (0-100%), stored in two number flags.
// A flag holds at most 20 variations, so one flag cannot list 101 values:
// "tens" serves 0, 10, ... 100 and "ones" serves 0 ... 9. The shown discount
// is their sum. Moving the slider only changes which variation each serves.

export const BANNER_FLAG = 'demo-banner'
export const TENS_FLAG = 'demo-banner-discount-tens'
export const ONES_FLAG = 'demo-banner-discount-ones'

/** Shown while the discount flags are missing or disabled. */
export const DEFAULT_DISCOUNT = 20

export const DISCOUNT_PARTS = [
  {
    key: TENS_FLAG,
    name: 'Banner discount (tens)',
    variations: Array.from({ length: 11 }, (_, i) => ({ id: `d${i * 10}`, value: i * 10 })),
  },
  {
    key: ONES_FLAG,
    name: 'Banner discount (ones)',
    variations: Array.from({ length: 10 }, (_, i) => ({ id: `d${i}`, value: i })),
  },
] as const

export const clampDiscount = (n: number) => Math.min(100, Math.max(0, Math.round(n)))

/** The variation each flag must serve for `percent`. */
export function splitDiscount(percent: number): { tens: string; ones: string } {
  const p = clampDiscount(percent)
  return { tens: `d${p - (p % 10)}`, ones: `d${p % 10}` }
}

/** Joins the two evaluated values; anything that is not a number means "use the default". */
export function joinDiscount(tens: unknown, ones: unknown): number {
  if (typeof tens !== 'number' || typeof ones !== 'number') return DEFAULT_DISCOUNT
  return clampDiscount(tens + ones)
}
