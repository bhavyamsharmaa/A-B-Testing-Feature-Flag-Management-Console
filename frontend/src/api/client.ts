// API client for the Helios backend. Every request carries the current
// Supabase access token as a Bearer header. A 401 signs the user out, which
// flips the auth state and sends them to /login (see RequireAuth).

import { config } from '../config'
import { supabase } from '../lib/supabase'

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

// Longer than Render's free-tier cold start (~50s), short enough that a dead
// connection becomes a visible error instead of an endless spinner.
const REQUEST_TIMEOUT_MS = 90_000

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const { data } = await supabase.auth.getSession()
  const token = data.session?.access_token

  const headers = new Headers(init.headers)
  if (init.body !== undefined) headers.set('Content-Type', 'application/json')
  if (token) headers.set('Authorization', `Bearer ${token}`)

  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
  try {
    let res: Response
    try {
      res = await fetch(`${config.apiBaseUrl}${path}`, { ...init, headers, signal: controller.signal })
    } catch {
      if (controller.signal.aborted) {
        throw new ApiError(0, 'TIMEOUT', 'The server took too long to respond. Try again.')
      }
      throw new ApiError(0, 'NETWORK', 'Could not reach the server. Check your connection and try again.')
    }

    if (res.status === 401) {
      await supabase.auth.signOut()
      throw new ApiError(401, 'UNAUTHORIZED', 'Your session has expired. Please sign in again.')
    }

    if (!res.ok) {
      // Backend errors are { code, message }; fall back if the body is not JSON
      // (e.g. a gateway error page while Render is waking up).
      const body = (await res.json().catch(() => null)) as { code?: string; message?: string } | null
      throw new ApiError(res.status, body?.code ?? 'HTTP_ERROR', body?.message ?? `Request failed (${res.status})`)
    }

    if (res.status === 204) return undefined as T
    return (await res.json()) as T
  } finally {
    clearTimeout(timer)
  }
}

export const apiClient = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) =>
    request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) }),
  patch: <T>(path: string, body: unknown) =>
    request<T>(path, { method: 'PATCH', body: JSON.stringify(body) }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
}
