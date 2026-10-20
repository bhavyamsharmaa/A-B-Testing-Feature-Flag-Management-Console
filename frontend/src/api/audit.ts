import type { AuditLogPage } from '../types'
import type { FlagHistoryEntry } from '../types'
import { apiClient, downloadFile } from './client'
import { envPath } from './envPath'

export const PAGE_SIZE = 50

/** All filters are applied by the server. An empty string means "not filtered". */
export interface AuditFilters {
  action: string
  resourceId: string
  severity: '' | 'info' | 'critical'
}

export const NO_FILTERS: AuditFilters = { action: '', resourceId: '', severity: '' }

export function listAuditLogs(env: string, filters: AuditFilters, before?: number): Promise<AuditLogPage> {
  const query = new URLSearchParams({ limit: String(PAGE_SIZE) }) // values are encoded, never concatenated
  if (before !== undefined) query.set('before', String(before))
  if (filters.action) query.set('action', filters.action)
  if (filters.resourceId) query.set('resourceId', filters.resourceId)
  if (filters.severity) query.set('severity', filters.severity)
  return apiClient.get<AuditLogPage>(`${envPath(env)}/audit-logs?${query}`)
}

export interface ExportRange {
  format: 'csv' | 'json'
  /** YYYY-MM-DD */
  from: string
  to: string
}

/** Downloads the audit log of an environment for a date range (admins and owners). */
export function exportAuditLog(env: string, r: ExportRange) {
  const query = new URLSearchParams({ format: r.format, from: r.from, to: r.to })
  return downloadFile(`${envPath(env)}/audit-logs/export?${query}`)
}

export function listFlagHistory(env: string, flagKey: string, before?: number, signal?: AbortSignal) {
  const query = new URLSearchParams({ limit: '20' })
  if (before !== undefined) query.set('before', String(before))
  return apiClient.get<{ entries: FlagHistoryEntry[]; nextCursor: number | null }>(`${envPath(env)}/flags/${encodeURIComponent(flagKey)}/history?${query}`, { signal })
}
