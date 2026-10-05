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
