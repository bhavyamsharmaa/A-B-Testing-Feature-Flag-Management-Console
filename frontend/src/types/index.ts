// Shared API types. Mirrors ../../../api/openapi.yaml (Me, Role, Error).

export type Role = 'viewer' | 'editor' | 'approver' | 'admin'

export interface EnvironmentRole {
  environment: string
  role: Role
}

export interface Me {
  id: string
  email: string
  roles: EnvironmentRole[]
}

export interface Variation {
  id: string
  value: unknown
}

export interface FlagConfig {
  enabled: boolean
  targetingRules: unknown[]
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
