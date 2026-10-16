import type { Flag, PromotionPart, PromotionPreview, ScheduledChange, ScheduledKind } from '../types'
import { apiClient } from './client'
import { envPath } from './envPath'

const flagPath = (env: string, key: string) => `${envPath(env)}/flags/${encodeURIComponent(key)}`

export async function listScheduledChanges(env: string, flagKey?: string, signal?: AbortSignal): Promise<ScheduledChange[]> {
  const q = flagKey ? `?flag=${encodeURIComponent(flagKey)}` : ''
  return (await apiClient.get<{ scheduledChanges: ScheduledChange[] }>(`${envPath(env)}/scheduled-changes${q}`, { signal })).scheduledChanges
}

export interface ScheduleInput {
  /** Chosen once per dialog, so a retried or double-clicked request is the same change, not two. */
  idempotencyKey: string
  kind: ScheduledKind
  runAt: string
  change?: { rollout?: Record<string, number>; fallthroughVariationId?: string }
}

export const createScheduledChange = (env: string, flagKey: string, input: ScheduleInput) =>
  apiClient.post<ScheduledChange>(`${flagPath(env, flagKey)}/scheduled-changes`, input)

export const cancelScheduledChange = (env: string, id: string) =>
  apiClient.post<ScheduledChange>(`${envPath(env)}/scheduled-changes/${encodeURIComponent(id)}/cancel`)

export interface PromotionInput {
  sourceEnvironment: string
  parts: PromotionPart[]
}

/** `env` is the TARGET. */
export const previewPromotion = (env: string, flagKey: string, input: PromotionInput) =>
  apiClient.post<PromotionPreview>(`${flagPath(env, flagKey)}/promotions/preview`, input)

export const applyPromotion = (env: string, flagKey: string, input: PromotionInput & { expectedVersion: number; sourceVersion: number }) =>
  apiClient.post<Flag>(`${flagPath(env, flagKey)}/promotions`, input)
