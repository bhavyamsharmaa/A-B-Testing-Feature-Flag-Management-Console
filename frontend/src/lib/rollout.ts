// Pure helpers for a boolean flag's percentage rollout. The backend stores a
// rollout as { variationId: basisPoints } summing to exactly 100000 (1% = 1000),
// and assigns buckets in the flag's own variation order. For a two-variation
// flag, raising one variation's share only ever adds users to it.

import type { Flag } from '../types'

export const TOTAL_BP = 100_000
export const BP_PER_PERCENT = 1_000

export interface BooleanVariations {
  onId: string
  offId: string
}

/** The ON (value true) and OFF (value false) variation ids, or null if this isn't a plain boolean flag. */
export function booleanVariations(flag: Flag): BooleanVariations | null {
  if (flag.variationType !== 'boolean' || flag.variations.length !== 2) return null
  const on = flag.variations.find((v) => v.value === true)
  const off = flag.variations.find((v) => v.value === false)
  return on && off ? { onId: on.id, offId: off.id } : null
}

/**
 * Basis points currently served ON. With no rollout set, everyone gets the
 * fallthrough variation: 100% if that is ON, 0% if it is OFF.
 */
export function savedOnBp(flag: Flag, v: BooleanVariations): number {
  const rollout = flag.config.rollout
  if (rollout && Object.keys(rollout).length > 0) return rollout[v.onId] ?? 0
  return flag.config.fallthroughVariationId === v.onId ? TOTAL_BP : 0
}

export const hasRollout = (flag: Flag): boolean =>
  flag.config.rollout !== null && Object.keys(flag.config.rollout).length > 0

export const percentToBp = (percent: number): number =>
  Math.min(100, Math.max(0, Math.round(percent))) * BP_PER_PERCENT

/** 25000 -> "25%", 12345 -> "12.345%". */
export function formatPercent(bp: number): string {
  const p = bp / BP_PER_PERCENT
  return `${Number.isInteger(p) ? p : p.toFixed(3).replace(/0+$/, '')}%`
}

/** "25% of users get ON, 75% get OFF". */
export function describeSplit(onBp: number): string {
  return `${formatPercent(onBp)} of users get ON, ${formatPercent(TOTAL_BP - onBp)} get OFF`
}

/** The PATCH body value. ON first; the two weights always sum to TOTAL_BP. */
export function rolloutPayload(v: BooleanVariations, onBp: number): Record<string, number> {
  return { [v.onId]: onBp, [v.offId]: TOTAL_BP - onBp }
}
