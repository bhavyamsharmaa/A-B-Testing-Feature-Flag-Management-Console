import type { EventCounts, Experiment } from '../types'
import { apiClient } from './client'
import { envPath } from './envPath'

export async function listExperiments(env: string): Promise<Experiment[]> {
  const res = await apiClient.get<{ experiments: Experiment[] }>(`${envPath(env)}/experiments`)
  return res.experiments
}

export function getEventCounts(env: string, key: string): Promise<EventCounts> {
  return apiClient.get<EventCounts>(`${envPath(env)}/experiments/${encodeURIComponent(key)}/event-counts`)
}

export interface NewExperiment {
  flagKey: string
  key: string
  name: string
  hypothesis: string
  metrics: { name: string; eventName: string; type: 'conversion' | 'value'; isPrimary: boolean }[]
  /** Omitted: the flag's first variation. */
  controlVariationId?: string
}

export const createExperiment = (env: string, input: NewExperiment) => apiClient.post<Experiment>(`${envPath(env)}/experiments`, input)

/** Allowed until the experiment has any exposure (then 409 CONTROL_FROZEN). */
export const setExperimentControl = (env: string, key: string, controlVariationId: string) =>
  apiClient.patch<Experiment>(`${envPath(env)}/experiments/${encodeURIComponent(key)}`, { controlVariationId })
