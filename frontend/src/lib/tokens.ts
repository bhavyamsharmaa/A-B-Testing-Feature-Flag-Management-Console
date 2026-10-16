// Pure helpers for the API tokens page: what a person may create, and honest wording for expiry and last use.

import type { ApiTokenRole, ApiToken, WorkspaceRole } from '../types'

/** The roles a person may give a token: up to their own, and never above admin (a token is never an owner). */
export function assignableTokenRoles(actor: WorkspaceRole | null | undefined): ApiTokenRole[] {
  switch (actor) {
    case 'owner':
    case 'admin':
      return ['viewer', 'editor', 'admin']
    default:
      return [] // below admin, nobody creates tokens
  }
}

export const TOKEN_ROLE_HINT: Record<ApiTokenRole, string> = {
  viewer: 'Read only: flags, experiments, results, the audit log',
  editor: 'Also edit flags and kill them outside production, promote, schedule, run experiments',
  admin: 'Also change production (the approval gate applies as to a person with this role)',
}

/** Mirrors apitokens.DefaultExpiryDays and MaxExpiryDays. There is no "never". */
export const EXPIRY_DAYS = [7, 30, 90, 180, 366] as const
export const DEFAULT_EXPIRY_DAYS = 90

const DAY = 86_400_000

/** "2026-11-03 10:00 UTC": exact, never relative. */
export function utcStamp(iso: string): string {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())} UTC`
}

/** "in 89 days", "tomorrow", "expired 3 days ago": honest about both directions. */
export function expiryWords(iso: string, now: number): string {
  const diff = new Date(iso).getTime() - now
  const days = Math.floor(Math.abs(diff) / DAY)
  if (diff >= 0) {
    if (diff < 3_600_000) return 'in under an hour'
    if (diff < DAY) return `in ${Math.floor(diff / 3_600_000)} hours`
    return days === 1 ? 'in 1 day' : `in ${days} days`
  }
  if (-diff < DAY) return 'expired today'
  return days === 1 ? 'expired 1 day ago' : `expired ${days} days ago`
}

/**
 * Last use, as the server knows it: it is written at most once a minute, so it can be up to a minute behind,
 * and a token that has never made a request says so.
 */
export function lastUsedWords(iso: string | null, now: number): string {
  if (!iso) return 'Never used'
  const diff = now - new Date(iso).getTime()
  if (diff < 0 || diff < 2 * 60_000) return 'Within the last couple of minutes'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} minutes ago`
  if (diff < DAY) return `${Math.floor(diff / 3_600_000)} hours ago`
  const days = Math.floor(diff / DAY)
  return days === 1 ? '1 day ago' : `${days} days ago`
}

export function environmentsText(t: ApiToken): string {
  return t.environments.length === 0 ? 'All environments' : t.environments.map((e) => e.key).join(', ')
}

/** Status as a word and a symbol: never colour alone. */
export const STATUS_MARK: Record<ApiToken['status'], { label: string; mark: string }> = {
  active: { label: 'Active', mark: '✓' },
  expired: { label: 'Expired', mark: '!' },
  revoked: { label: 'Revoked', mark: '✕' },
}
