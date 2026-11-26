// Environments are addressed by UUID in the API (/environments/{id}/...), but
// the console thinks in environment keys ("dev", "production"). The workspace
// provider registers the active workspace's environments here, and the API
// modules turn a key into its path. A key of another workspace is simply not
// in the registry, so it can never be sent by accident.

import type { WorkspaceEnvironment } from '../types'

let idByKey = new Map<string, string>()

/** Called by WorkspaceProvider whenever the active workspace changes. */
export function setActiveEnvironments(environments: WorkspaceEnvironment[]): void {
  idByKey = new Map(environments.map((e) => [e.key, e.id]))
}

export function envPath(key: string): string {
  const id = idByKey.get(key)
  if (!id) throw new Error(`Unknown environment "${key}" in the active workspace`)
  return `/environments/${id}`
}
