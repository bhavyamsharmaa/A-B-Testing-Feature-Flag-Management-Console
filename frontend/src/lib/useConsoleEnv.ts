import { useMemo, useState } from 'react'
import { useAuth } from '../auth/AuthProvider'
import { useWorkspace } from '../workspace/WorkspaceProvider'
import type { EnvironmentRole } from '../types'
import { envRoleOf, isProduction, roleIn } from './permissions'

const ENV_ORDER = ['dev', 'staging', 'production']
const storageKey = (workspaceId: string) => `helios.selectedEnv.${workspaceId}`

function readStoredEnv(workspaceId: string | undefined): string | null {
  if (!workspaceId) return null
  try {
    return localStorage.getItem(storageKey(workspaceId))
  } catch {
    return null
  }
}

/**
 * Account, the active workspace, its environments with the caller's access in
 * each, and the remembered environment: shared by every console page.
 * `roles` keeps the per-environment shape the flag components were written
 * for; every environment of a workspace carries the access its workspace role
 * gives.
 */
export function useConsoleEnv() {
  const { session, me, meLoading, meError, reloadMe, signOut } = useAuth()
  const { active: workspace } = useWorkspace()
  const email = me?.email ?? session?.user.email ?? ''
  const workspaceRole = workspace?.role ?? null

  const roles = useMemo<EnvironmentRole[]>(() => {
    if (!workspace) return []
    const rank = (e: string) => (ENV_ORDER.includes(e) ? ENV_ORDER.indexOf(e) : ENV_ORDER.length)
    return workspace.environments
      .map((e) => ({ environment: e.key, role: envRoleOf(workspace.role) }))
      .sort((a, b) => rank(a.environment) - rank(b.environment) || a.environment.localeCompare(b.environment))
  }, [workspace])

  const [preferred, setPreferred] = useState<{ workspaceId: string; env: string } | null>(null)
  const stored = preferred && preferred.workspaceId === workspace?.id ? preferred.env : readStoredEnv(workspace?.id)
  const envNames = roles.map((r) => r.environment)
  const env = envNames.includes(stored ?? '') ? (stored as string) : envNames.includes('dev') ? 'dev' : (envNames[0] ?? null)

  function selectEnv(next: string) {
    if (!workspace) return
    setPreferred({ workspaceId: workspace.id, env: next })
    try {
      localStorage.setItem(storageKey(workspace.id), next)
    } catch {
      /* private mode: the choice just won't persist */
    }
  }

  const role = env ? roleIn(roles, env) : null
  const prod = env ? isProduction(env) : false

  return { email, me, meLoading, meError, reloadMe, signOut, workspace, workspaceRole, roles, env, selectEnv, role, prod }
}
