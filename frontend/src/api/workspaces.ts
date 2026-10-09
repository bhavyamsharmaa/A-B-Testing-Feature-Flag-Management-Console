import type { Member, SdkKey, Workspace, WorkspaceInvite, WorkspaceRole } from '../types'
import { apiClient } from './client'
import { envPath } from './envPath'

const ws = (id: string) => `/workspaces/${encodeURIComponent(id)}`

export const createWorkspace = (name: string) => apiClient.post<Workspace>('/workspaces', { name })

export const switchWorkspace = (id: string) =>
  apiClient.post<{ activeWorkspaceId: string }>(`${ws(id)}/switch`)

export const renameWorkspace = (id: string, name: string) =>
  apiClient.patch<{ id: string; name: string }>(ws(id), { name })

export async function listMembers(id: string): Promise<Member[]> {
  return (await apiClient.get<{ members: Member[] }>(`${ws(id)}/members`)).members
}

export const changeMemberRole = (id: string, userId: string, role: WorkspaceRole) =>
  apiClient.patch<{ userId: string; role: WorkspaceRole }>(`${ws(id)}/members/${encodeURIComponent(userId)}`, { role })

/** Removes a member; with your own id this leaves the workspace. */
export const removeMember = (id: string, userId: string) =>
  apiClient.delete<void>(`${ws(id)}/members/${encodeURIComponent(userId)}`)

export async function listInvites(id: string): Promise<WorkspaceInvite[]> {
  return (await apiClient.get<{ invites: WorkspaceInvite[] }>(`${ws(id)}/invites`)).invites
}

/** The token comes back once; the console turns it into a link to share. */
export const createInvite = (id: string, email: string, role: WorkspaceRole) =>
  apiClient.post<{ invite: WorkspaceInvite; token: string }>(`${ws(id)}/invites`, { email, role })

export const revokeInvite = (id: string, inviteId: string) =>
  apiClient.delete<void>(`${ws(id)}/invites/${encodeURIComponent(inviteId)}`)

export const acceptInviteByToken = (token: string) =>
  apiClient.post<{ workspaceId: string }>('/invites/accept', { token })

export const acceptInviteById = (inviteId: string) =>
  apiClient.post<{ workspaceId: string }>('/invites/accept', { inviteId })

export async function listSdkKeys(env: string): Promise<SdkKey[]> {
  return (await apiClient.get<{ keys: SdkKey[] }>(`${envPath(env)}/sdk-keys`)).keys
}

/** `plaintext` is shown exactly once; only its hash is stored. */
export const createSdkKey = (env: string, name: string) =>
  apiClient.post<{ key: SdkKey; plaintext: string }>(`${envPath(env)}/sdk-keys`, { name })

export const revokeSdkKey = (env: string, keyId: string) =>
  apiClient.delete<void>(`${envPath(env)}/sdk-keys/${encodeURIComponent(keyId)}`)
