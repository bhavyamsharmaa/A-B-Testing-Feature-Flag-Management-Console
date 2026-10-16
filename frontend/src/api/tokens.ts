import type { ApiToken, ApiTokenRole } from '../types'
import { apiClient } from './client'

const base = (workspaceId: string) => `/workspaces/${encodeURIComponent(workspaceId)}/api-tokens`

export async function listApiTokens(workspaceId: string, signal?: AbortSignal): Promise<ApiToken[]> {
  return (await apiClient.get<{ tokens: ApiToken[] }>(base(workspaceId), { signal })).tokens
}

export interface CreateTokenInput {
  name: string
  role: ApiTokenRole
  /** Environment keys; empty or omitted: every environment. */
  environments?: string[]
  expiresInDays: number
}

/** The plaintext comes back once and is never available again. */
export const createApiToken = (workspaceId: string, input: CreateTokenInput) =>
  apiClient.post<{ token: ApiToken; plaintext: string }>(base(workspaceId), input)

export const revokeApiToken = (workspaceId: string, id: string) => apiClient.delete<void>(`${base(workspaceId)}/${encodeURIComponent(id)}`)
