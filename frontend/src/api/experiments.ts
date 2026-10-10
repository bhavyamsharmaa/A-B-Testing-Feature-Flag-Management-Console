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
