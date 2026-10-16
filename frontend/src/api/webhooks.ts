import type { WebhookDelivery, WebhookEndpoint, WebhookEventType } from '../types'
import { apiClient } from './client'

const base = (workspaceId: string) => `/workspaces/${encodeURIComponent(workspaceId)}/webhooks`
const one = (workspaceId: string, id: string) => `${base(workspaceId)}/${encodeURIComponent(id)}`

export interface WebhookList {
  endpoints: WebhookEndpoint[]
  /** false: the server has no encryption key, so endpoints cannot be created. */
  available: boolean
  eventTypes: WebhookEventType[]
  limit: number
}

export const listWebhooks = (workspaceId: string, signal?: AbortSignal) => apiClient.get<WebhookList>(base(workspaceId), { signal })

export interface EndpointInput {
  url: string
  description: string
  eventTypes: WebhookEventType[]
  /** null/omitted: every environment. */
  environmentId?: string | null
}

/** The secret comes back once. */
export const createWebhook = (workspaceId: string, input: EndpointInput) =>
  apiClient.post<{ endpoint: WebhookEndpoint; secret: string }>(base(workspaceId), input)

export const updateWebhook = (workspaceId: string, id: string, input: Partial<EndpointInput> & { enabled?: boolean }) =>
  apiClient.patch<WebhookEndpoint>(one(workspaceId, id), input)

export const deleteWebhook = (workspaceId: string, id: string) => apiClient.delete<void>(one(workspaceId, id))

export const rotateWebhookSecret = (workspaceId: string, id: string) =>
  apiClient.post<{ endpoint: WebhookEndpoint; secret: string }>(`${one(workspaceId, id)}/rotate-secret`)

export const testWebhook = (workspaceId: string, id: string) => apiClient.post<WebhookDelivery>(`${one(workspaceId, id)}/test`)

export async function listDeliveries(workspaceId: string, id: string, signal?: AbortSignal): Promise<WebhookDelivery[]> {
  return (await apiClient.get<{ deliveries: WebhookDelivery[] }>(`${one(workspaceId, id)}/deliveries?limit=50`, { signal })).deliveries
}

export const resendDelivery = (workspaceId: string, id: string, deliveryId: string) =>
  apiClient.post<WebhookDelivery>(`${one(workspaceId, id)}/deliveries/${encodeURIComponent(deliveryId)}/resend`)
