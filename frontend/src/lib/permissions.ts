// Cosmetic only: hides or disables controls the user's role cannot use. The
// backend (rbac package) is the real boundary and still answers 403.

import type { EnvironmentRole, Role, WorkspaceRole } from '../types'

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

// ---- Workspace roles -------------------------------------------------------
// The workspace role applies to every environment of the workspace. The mapping
// mirrors the backend's rbac.WorkspaceRole.EnvRole.

/** What a workspace role may do inside each environment. */
export function envRoleOf(role: WorkspaceRole): Role {
  switch (role) {
    case 'owner':
    case 'admin':
      return 'admin'
    case 'editor':
      return 'editor'
    default:
      return 'viewer'
  }
}

const WS_RANK: Record<WorkspaceRole, number> = { viewer: 1, editor: 2, admin: 3, owner: 4 }

export const wsAtLeast = (role: WorkspaceRole | null | undefined, min: WorkspaceRole) =>
  !!role && WS_RANK[role] >= WS_RANK[min]

/** Rename the workspace, invite people, change roles, manage SDK keys. */
export const canManageWorkspace = (role: WorkspaceRole | null | undefined) => wsAtLeast(role, 'admin')

/** Roles `actor` may give someone: owners any, admins everything but owner. */
export function assignableRoles(actor: WorkspaceRole): WorkspaceRole[] {
  return actor === 'owner' ? ['owner', 'admin', 'editor', 'viewer'] : ['admin', 'editor', 'viewer']
}

/** Whether `actor` may change `target`'s role or remove them (admins can't touch owners). */
export const canModifyMember = (actor: WorkspaceRole, target: WorkspaceRole) =>
  wsAtLeast(actor, 'admin') && (actor === 'owner' || target !== 'owner')

export const ROLE_LABEL: Record<WorkspaceRole, string> = {
  owner: 'Owner',
  admin: 'Admin',
  editor: 'Editor',
  viewer: 'Viewer',
}

export const ROLE_HINT: Record<WorkspaceRole, string> = {
  owner: 'Full control, including ownership and removing other owners',
  admin: 'Manage members, invites, SDK keys and production flags',
  editor: 'Create and edit flags outside production, and kill switches',
  viewer: 'Read-only access',
}
