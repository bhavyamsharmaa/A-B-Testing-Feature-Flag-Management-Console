import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react'
import { setActiveEnvironments } from '../api/envPath'
import { createWorkspace as apiCreateWorkspace, switchWorkspace } from '../api/workspaces'
import { useAuth } from '../auth/AuthProvider'
import type { PendingInvite, Workspace } from '../types'

interface WorkspaceState {
  workspaces: Workspace[]
  /** The workspace every console page shows. Null until /me has loaded. */
  active: Workspace | null
  invites: PendingInvite[]
  /** Makes a workspace the active one (remembered server-side, so it survives a reload). */
  activate: (id: string) => Promise<void>
  /** Creates a workspace owned by the caller and switches to it. */
  create: (name: string) => Promise<Workspace>
}

const WorkspaceContext = createContext<WorkspaceState | null>(null)

const NONE: Workspace[] = []
const NO_INVITES: PendingInvite[] = []

/**
 * The active workspace comes from /me (the server's most recently used one);
 * switching tells the server first, then updates local state. Pages are
 * remounted per workspace by <WorkspaceScope>, so nothing from the previous
 * workspace survives a switch.
 */
export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const { me, refreshMe } = useAuth()
  const [chosen, setChosen] = useState<string | null>(null)

  const workspaces = me?.workspaces ?? NONE
  const invites = me?.invites ?? NO_INVITES
  const active =
    workspaces.find((w) => w.id === chosen) ?? workspaces.find((w) => w.id === me?.activeWorkspaceId) ?? workspaces[0] ?? null

  // The API modules turn an environment key into its id through this registry.
  // It is assigned during render on purpose: children's effects run before this
  // component's, and they fetch immediately. The assignment is idempotent.
  useMemo(() => setActiveEnvironments(active?.environments ?? []), [active])

  const activate = useCallback(async (id: string) => {
    await switchWorkspace(id)
    setChosen(id)
  }, [])

  const create = useCallback(
    async (name: string) => {
      const created = await apiCreateWorkspace(name)
      await refreshMe() // the new workspace must be in /me before it can be active
      setChosen(created.id)
      return created
    },
    [refreshMe],
  )

  const value = useMemo<WorkspaceState>(
    () => ({ workspaces, active, invites, activate, create }),
    [workspaces, active, invites, activate, create],
  )
  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>
}

export function useWorkspace(): WorkspaceState {
  const ctx = useContext(WorkspaceContext)
  if (!ctx) throw new Error('useWorkspace must be used inside <WorkspaceProvider>')
  return ctx
}
