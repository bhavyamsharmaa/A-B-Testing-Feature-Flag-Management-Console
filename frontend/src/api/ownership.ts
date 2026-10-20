import type { OwnershipOffer } from '../types'
import { apiClient } from './client'

const base = (workspaceId: string) => `/workspaces/${encodeURIComponent(workspaceId)}/ownership-offers`

/** The workspace's open offer, or null. Owners only. */
export async function getOpenOffer(workspaceId: string): Promise<OwnershipOffer | null> {
  return (await apiClient.get<{ offer: OwnershipOffer | null }>(base(workspaceId))).offer
}

export const offerOwnership = (workspaceId: string, userId: string) => apiClient.post<{ offer: OwnershipOffer }>(base(workspaceId), { userId })

export const cancelOffer = (workspaceId: string, offerId: string) => apiClient.delete<void>(`${base(workspaceId)}/${encodeURIComponent(offerId)}`)

export const acceptOffer = (offerId: string) => apiClient.post<{ workspaceId: string }>(`/ownership-offers/${encodeURIComponent(offerId)}/accept`)

export const declineOffer = (offerId: string) => apiClient.post<void>(`/ownership-offers/${encodeURIComponent(offerId)}/decline`)
