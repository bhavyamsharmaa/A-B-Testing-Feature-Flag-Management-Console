// Pure helpers for rollout weights on flags with any number of variations.
// A rollout is { variationId: basisPoints } summing to exactly 100000 (1% = 1000).
// The backend assigns users by walking the flag's variations IN ORDER and giving
// each a cumulative range, so changing one weight can shift every later boundary.

import type { Flag } from '../types'
import { TOTAL_BP, formatPercent, hasRollout } from './rollout'

export type Weights = Record<string, number>

/** Weights in effect now. With no rollout, everyone gets the fallthrough variation. */
export function savedWeights(flag: Flag): Weights {
  const ids = flag.variations.map((v) => v.id)
  if (hasRollout(flag)) {
    const rollout = flag.config.rollout ?? {}
    return Object.fromEntries(ids.map((id) => [id, rollout[id] ?? 0]))
  }
  const fall = ids.includes(flag.config.fallthroughVariationId) ? flag.config.fallthroughVariationId : ids[0]
  return Object.fromEntries(ids.map((id) => [id, id === fall ? TOTAL_BP : 0]))
}

/** floor(100000 / n) each; any remainder goes to the first variation so the total is exact. */
export function distributeEvenly(ids: string[]): Weights {
  const base = Math.floor(TOTAL_BP / ids.length)
  const remainder = TOTAL_BP - base * ids.length
  return Object.fromEntries(ids.map((id, i) => [id, i === 0 ? base + remainder : base]))
}

/** "33.334" -> 33334. Up to three decimals (1 basis point = 0.001%). Null if invalid. */
export function parsePercent(text: string): number | null {
  const t = text.trim()
  if (!/^\d+(\.\d{1,3})?$/.test(t)) return null
  const bp = Math.round(Number(t) * 1000)
  return bp >= 0 && bp <= TOTAL_BP ? bp : null
}

/** 33334 -> "33.334", 50000 -> "50". */
export function bpToText(bp: number): string {
  const p = bp / 1000
  return Number.isInteger(p) ? String(p) : p.toFixed(3).replace(/0+$/, '')
}

export function textsFromWeights(w: Weights): Record<string, string> {
  return Object.fromEntries(Object.entries(w).map(([id, bp]) => [id, bpToText(bp)]))
}

export interface WeightsState {
  /** Parsed basis points; invalid entries count as 0 and are listed in `invalid`. */
  weights: Weights
  invalid: string[]
  total: number
  /** Exactly 100% and every entry valid: the only state that may be saved. */
  valid: boolean
}

export function parseWeights(ids: string[], texts: Record<string, string>): WeightsState {
  const weights: Weights = {}
  const invalid: string[] = []
  for (const id of ids) {
    const bp = parsePercent(texts[id] ?? '')
    if (bp === null) invalid.push(id)
    weights[id] = bp ?? 0
  }
  const total = ids.reduce((sum, id) => sum + weights[id], 0)
  return { weights, invalid, total, valid: invalid.length === 0 && total === TOTAL_BP }
}

export const sameWeights = (a: Weights, b: Weights): boolean =>
  Object.keys({ ...a, ...b }).every((id) => (a[id] ?? 0) === (b[id] ?? 0))

/** Only the variations whose weight differs, as "A: 50% → 40%". */
export function describeChanges(from: Weights, to: Weights, ids: string[]): string[] {
  return ids.filter((id) => (from[id] ?? 0) !== (to[id] ?? 0)).map((id) => `${id}: ${formatPercent(from[id] ?? 0)} → ${formatPercent(to[id] ?? 0)}`)
}

/** The PATCH value: every variation id, in the flag's order, summing to exactly 100000. */
export function weightsPayload(ids: string[], w: Weights): Record<string, number> {
  return Object.fromEntries(ids.map((id) => [id, w[id] ?? 0]))
}
