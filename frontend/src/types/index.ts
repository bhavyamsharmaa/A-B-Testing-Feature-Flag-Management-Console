// Shared API types. Mirrors ../../../api/openapi.yaml (Me, Workspace, Role, Error).

export type Role = 'viewer' | 'editor' | 'approver' | 'admin'

export interface EnvironmentRole {
  environment: string
  role: Role
}

export type WorkspaceRole = 'owner' | 'admin' | 'editor' | 'viewer'

export interface WorkspaceEnvironment {
  id: string
  key: string
  name: string
  isProduction: boolean
}

/** A workspace as the signed-in user sees it: their role in it, and its environments. */
export interface Workspace {
  id: string
  name: string
  slug: string
  role: WorkspaceRole
  environments: WorkspaceEnvironment[]
}

/** An open invite addressed to the signed-in user's email. */
export interface PendingInvite {
  id: string
  workspaceId: string
  workspaceName: string
  role: WorkspaceRole
  invitedByEmail: string
  expiresAt: string
}

export interface Me {
  id: string
  email: string
  activeWorkspaceId: string
  workspaces: Workspace[]
  invites: PendingInvite[]
}

export interface Member {
  userId: string
  email: string
  role: WorkspaceRole
  joinedAt: string
  isYou: boolean
}

export interface WorkspaceInvite {
  id: string
  email: string
  role: WorkspaceRole
  invitedByEmail: string
  createdAt: string
  expiresAt: string
}

export interface SdkKey {
  id: string
  name: string
  kind: string
  prefix: string
  createdAt: string
  revokedAt: string | null
}

export interface Variation {
  id: string
  value: unknown
}

/** One test on a context attribute. `value` is omitted for the `exists` operator. */
export interface Clause {
  attribute: string
  operator: string
  value?: unknown
}

/** Serves `variationId` when every clause matches (they are ANDed). Rules run top to bottom. */
export interface TargetingRule {
  clauses: Clause[]
  variationId: string
}

export interface FlagConfig {
  enabled: boolean
  targetingRules: TargetingRule[]
  rollout: Record<string, number> | null
  fallthroughVariationId: string
  version: number
  updatedAt: string
}

export interface Flag {
  key: string
  name: string
  description: string
  variationType: 'boolean' | 'string' | 'number' | 'json'
  variations: Variation[]
  environment: string
  config: FlagConfig
  createdAt: string
  updatedAt: string
}

export interface AuditEntry {
  id: number
  createdAt: string
  actorEmail: string
  action: string
  resourceType: string
  resourceId: string
  severity: 'info' | 'critical'
  diffBefore: unknown
  diffAfter: unknown
  /** "global" = not tied to one environment (e.g. flag.create); shown in every environment's log. */
  scope: 'environment' | 'global'
}

export interface AuditLogPage {
  entries: AuditEntry[]
  nextCursor: number | null
}
