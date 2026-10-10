import type { ExperimentResults, MetricSelection } from '../types'
import { apiClient } from './client'
import { envPath } from './envPath'

/** The `metrics` query value: eventName:kind pairs, comma separated. */
export function metricsParam(metrics: MetricSelection[]): string {
  return metrics.map((m) => `${m.eventName}:${m.kind}`).join(',')
}

export function getResults(env: string, key: string, metrics: MetricSelection[], signal: AbortSignal): Promise<ExperimentResults> {
  const query = metrics.length > 0 ? `?${new URLSearchParams({ metrics: metricsParam(metrics) })}` : ''
  return apiClient.get<ExperimentResults>(`${envPath(env)}/experiments/${encodeURIComponent(key)}/results${query}`, { signal })
}
