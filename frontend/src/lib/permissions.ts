// Cosmetic only: hides or disables controls the user's role cannot use. The
// backend (rbac package) is the real boundary and still answers 403.

import type { EnvironmentRole, Role } from '../types'

const RANK: Record<Role, number> = { viewer: 1, editor: 2, approver: 3, admin: 4 }

// /me doesn't expose is_production, so the environment key is the signal.
export const isProduction = (env: string) => env === 'production'

export function roleIn(roles: EnvironmentRole[], env: string): Role | null {
  return roles.find((r) => r.environment === env)?.role ?? null
}

const atLeast = (role: Role | null, min: Role) => role !== null && RANK[role] >= RANK[min]

/** Creating a flag needs editor in the selected environment (even production). */
export const canCreate = (role: Role | null) => atLeast(role, 'editor')

/** Toggling needs editor in dev/staging, approver in production. */
export const canToggle = (role: Role | null, env: string) =>
  atLeast(role, isProduction(env) ? 'approver' : 'editor')

/** Kill is allowed for editors in every environment, production included. */
export const canKill = (role: Role | null) => atLeast(role, 'editor')

export function toggleDisabledReason(role: Role | null, env: string): string {
  if (role === 'viewer') return 'Viewers have read-only access'
  return isProduction(env) ? 'Changing production requires the approver role' : 'Requires the editor role'
}

/**
 * Deleting removes a flag from EVERY environment, so the backend requires admin
 * in all of them, not just the one being viewed. /me only lists environments the
 * user holds a role in, so an environment with no role at all can't be seen here;
 * the server's 403 names it if that is the case.
 */
export function canDelete(roles: EnvironmentRole[], env: string): boolean {
  return roleIn(roles, env) === 'admin' && roles.every((r) => r.role === 'admin')
}

export function deleteDisabledReason(roles: EnvironmentRole[], env: string): string {
  if (roleIn(roles, env) !== 'admin') return 'Only admins can delete flags'
  const others = roles.filter((r) => r.role !== 'admin').map((r) => `${r.environment} (${r.role})`)
  return `Deleting removes a flag from every environment, so you must be an admin in all of them. You are not an admin in: ${others.join(', ')}`
}
