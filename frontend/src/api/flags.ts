import type { Flag } from '../types'
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
