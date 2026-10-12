import type { Clause, Segment, SegmentSummary, SegmentUsage } from '../types'
import { apiClient } from './client'

const base = (workspaceId: string) => `/workspaces/${encodeURIComponent(workspaceId)}/segments`
const one = (workspaceId: string, key: string) => `${base(workspaceId)}/${encodeURIComponent(key)}`

export async function listSegments(workspaceId: string, signal?: AbortSignal): Promise<SegmentSummary[]> {
  return (await apiClient.get<{ segments: SegmentSummary[] }>(base(workspaceId), { signal })).segments
}

export const getSegment = (workspaceId: string, key: string) => apiClient.get<Segment>(one(workspaceId, key))

export interface SegmentInput {
  key: string
  name: string
  description: string
  rules: Clause[]
  members: string[]
}

export const createSegment = (workspaceId: string, input: SegmentInput) => apiClient.post<Segment>(base(workspaceId), input)

/** `members`, when present, replaces the whole list. `expectedVersion` makes a concurrent edit a 409 instead of a silent overwrite. */
export const updateSegment = (
  workspaceId: string,
  key: string,
  patch: Partial<Omit<SegmentInput, 'key'>> & { expectedVersion?: number },
) => apiClient.patch<Segment>(one(workspaceId, key), patch)

/** An in-use segment needs `confirm`: the rules that name it then match nobody. */
export const deleteSegment = (workspaceId: string, key: string, confirm: boolean) =>
  apiClient.delete<void>(`${one(workspaceId, key)}${confirm ? '?confirm=true' : ''}`)

export const segmentUsage = (workspaceId: string, key: string) => apiClient.get<SegmentUsage>(`${one(workspaceId, key)}/usage`)
