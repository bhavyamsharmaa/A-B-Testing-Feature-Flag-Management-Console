import type { Flag, TargetingRule } from '../types'
import { apiClient } from './client'

const flagsPath = (env: string) => `/environments/${encodeURIComponent(env)}/flags`
const flagPath = (env: string, key: string) => `${flagsPath(env)}/${encodeURIComponent(key)}`

export async function listFlags(env: string): Promise<Flag[]> {
  const res = await apiClient.get<{ flags: Flag[] }>(flagsPath(env))
  return res.flags
}

// Boolean flags only. The backend sets the fallthrough to the first
// variation, so "on" comes first: enabling the flag then serves true.
export function createBooleanFlag(
  env: string,
  input: { key: string; name: string; description: string },
): Promise<Flag> {
  const description = input.description.trim()
  return apiClient.post<Flag>(flagsPath(env), {
    key: input.key,
    name: input.name.trim(),
    ...(description ? { description } : {}),
    variationType: 'boolean',
    variations: [
      { id: 'on', value: true },
      { id: 'off', value: false },
    ],
  })
}

export function setFlagEnabled(env: string, key: string, enabled: boolean): Promise<Flag> {
  return apiClient.patch<Flag>(flagPath(env, key), { enabled })
}

export function killFlag(env: string, key: string): Promise<Flag> {
  return apiClient.post<Flag>(`${flagPath(env, key)}/kill`)
}

export function getFlag(env: string, key: string): Promise<Flag> {
  return apiClient.get<Flag>(flagPath(env, key))
}

/** PATCH is a partial update: sending only `rollout` leaves enabled, rules and fallthrough alone. */
export function setFlagRollout(env: string, key: string, rollout: Record<string, number>): Promise<Flag> {
  return apiClient.patch<Flag>(flagPath(env, key), { rollout })
}

/** Sends only the rules: PATCH is partial, so enabled, rollout and fallthrough are left alone. [] clears them. */
export function setFlagTargetingRules(env: string, key: string, rules: TargetingRule[]): Promise<Flag> {
  return apiClient.patch<Flag>(flagPath(env, key), { targetingRules: rules })
}

/**
 * Deletes the flag from EVERY environment. A flag that is enabled with targeting
 * rules anywhere is refused with 409 IN_USE unless `force` is set (?force=true).
 */
export function deleteFlag(env: string, key: string, force: boolean): Promise<void> {
  return apiClient.delete<void>(`${flagPath(env, key)}${force ? '?force=true' : ''}`)
}

/** Creates a flag of any type. Boolean creation keeps using createBooleanFlag. */
export function createFlag(
  env: string,
  input: {
    key: string
    name: string
    description: string
    variationType: 'string' | 'number' | 'json'
    variations: { id: string; value: unknown }[]
  },
): Promise<Flag> {
  const description = input.description.trim()
  return apiClient.post<Flag>(flagsPath(env), {
    key: input.key,
    name: input.name.trim(),
    ...(description ? { description } : {}),
    variationType: input.variationType,
    variations: input.variations,
  })
}
