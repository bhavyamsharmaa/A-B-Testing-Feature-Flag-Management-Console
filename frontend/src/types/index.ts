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

/** An offer of ownership addressed to the signed-in user (an admin of that workspace). */
export interface OwnershipOffer {
  id: string
  workspaceId: string
  workspaceName: string
  fromEmail: string
  toUserId: string
  toEmail: string
  createdAt: string
  expiresAt: string
}

/** A workspace the caller owns that is scheduled for deletion and can still be restored. */
export interface PendingDeletion {
  id: string
  name: string
  deletedAt: string
  purgeAfter: string
}

export interface FlagHistoryChange {
  field: string
  label: string
  before: string[]
  after: string[]
}

export interface FlagHistoryEntry {
  id: number
  /** RFC 3339, UTC. */
  createdAt: string
  action: string
  summary: string
  severity: string
  actor: { type: 'user' | 'api_token' | 'schedule'; label: string; onBehalfOf?: string }
  changes: FlagHistoryChange[]
  source?: string
}

export interface Me {
  id: string
  email: string
  activeWorkspaceId: string
  workspaces: Workspace[]
  invites: PendingInvite[]
  ownershipOffers: OwnershipOffer[]
  pendingDeletion: PendingDeletion[]
}

export interface Member {
  userId: string
  email: string
  role: WorkspaceRole
  joinedAt: string
  isYou: boolean
}

export type InviteEmailStatus = 'none' | 'queued' | 'sent' | 'failed'

export interface WorkspaceInvite {
  id: string
  email: string
  role: WorkspaceRole
  invitedByEmail: string
  createdAt: string
  expiresAt: string
  /** The latest invite email. `none`: no email was queued (an invite from before email existed). */
  emailStatus: InviteEmailStatus
  emailError?: string
  emailUpdatedAt: string | null
}

/** How the server delivers invite email. `sink`: emails are NOT actually sent in this environment. */
export interface InviteEmailMode {
  mode: 'sink' | 'smtp'
  sent: boolean
}

export type WebhookEventType =
  | 'flag.created'
  | 'flag.updated'
  | 'flag.killed'
  | 'flag.deleted'
  | 'experiment.started'
  | 'experiment.stopped'
  | 'promotion.applied'
  | 'scheduled_change.executed'
  | 'scheduled_change.failed'

export type WebhookEndpointStatus = 'healthy' | 'failing' | 'disabled' | 'idle'

export interface WebhookEndpoint {
  id: string
  url: string
  description: string
  eventTypes: WebhookEventType[]
  environment: { id: string; key: string } | null
  enabled: boolean
  secretHint: string
  previousSecretExpiresAt: string | null
  health: {
    consecutiveFailures: number
    failingSince: string | null
    lastSuccessAt: string | null
    lastFailureAt: string | null
    disabledReason: string | null
    disabledAt: string | null
  }
  createdAt: string
  status: WebhookEndpointStatus
}

export type WebhookDeliveryStatus = 'pending' | 'delivering' | 'delivered' | 'failed' | 'dead'

export interface WebhookDelivery {
  id: string
  eventId: string
  eventType: string
  status: WebhookDeliveryStatus
  attempts: number
  httpStatus: number | null
  durationMs: number | null
  error: string | null
  responseHint: string | null
  createdAt: string
  lastAttemptAt: string | null
  nextAttemptAt: string | null
  deliveredAt: string | null
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
  /** Inverts the operator, but never makes a missing attribute or a wrong type match. */
  negate?: boolean
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
  /** "global" = a workspace-level change, not tied to one environment (flag.create, segment.*); one entry, listed in every environment's log. */
  scope: 'environment' | 'global'
  /** Who acted: a person, or an API token (then `actorEmail` reads "name (hlt_xxxxxxxx)" and `actorDetail` names its creator). */
  actorType?: 'user' | 'api_token'
  actorDetail?: { tokenId: string; name: string; prefix: string; createdBy: string; createdById: string } | null
}

export interface AuditLogPage {
  entries: AuditEntry[]
  nextCursor: number | null
}

export type ExperimentStatus = 'draft' | 'running' | 'stopped'

export interface Experiment {
  id: string
  key: string
  name: string
  hypothesis: string
  flagKey: string
  environment: string
  status: ExperimentStatus
  startedAt: string | null
  stoppedAt: string | null
  metrics: { name: string; eventName: string; type: 'conversion' | 'value'; isPrimary: boolean }[]
  configChangedSinceStart: boolean
  createdAt: string
  /** The flag's variations in its order; exactly one is the control everything is compared with. */
  variations: { id: string; isControl: boolean }[]
  controlVariationId: string
}

/** Raw data arriving for an experiment. Not results: no rates, no significance. */
export interface EventCounts {
  /** Event names (other than $exposure) that exist for the experiment, most frequent first. */
  eventNames: { name: string; count: number; /** the metric kinds the data supports */ kinds: MetricKind[] }[]
  exposures: { variationId: string; count: number }[]
  exposureTotal: number
  /** Events other than exposures. */
  events: number
  lastEventAt: string | null
}

export type MetricKind = 'conversion' | 'count' | 'value'

export interface MetricSelection {
  eventName: string
  kind: MetricKind
}

export interface Interval {
  low: number | null
  high: number | null
}

export type ComparisonStatus = 'significant' | 'not_significant' | 'insufficient_data' | 'invalid'

export interface ResultsVariation {
  variationId: string
  isControl: boolean
  exposures: number
  convertingSubjects: number
  events: number
  rate: number | null
  mean: number | null
  stdDev: number | null
  trustworthy: boolean
}

export interface ResultsComparison {
  variationId: string
  against: string
  status: ComparisonStatus
  statusReasons?: string[]
  difference: number | null
  differenceInterval: Interval
  relativeLift: number | null
  relativeLiftInterval: Interval
  statistic: number | null
  degreesOfFreedom: number | null
  pValue: number | null
  adjustedPValue: number | null
  /** Deprecated: use the response's alphaPerComparison. */
  alphaPerComparison?: number
  /** Deprecated: use the response's confidenceLevel. */
  confidenceLevel?: number
  reasons?: Record<string, string>
  trustworthy: boolean
}

export interface ResultsWarning {
  code: string
  severity: 'info' | 'warning' | 'critical'
  message: string
  /** The contract: structured facts for the code. `message` is an English convenience and is not used. */
  detail: Record<string, unknown>
}

export type MetricStatus = 'ok' | 'insufficient_data' | 'unavailable'

export interface ResultsMetric {
  /** The metric as a whole; say "insufficient data" once, not per comparison. */
  status: MetricStatus
  statusReasons?: string[]
  metric: { name?: string; eventName: string; kind: MetricKind; isPrimary: boolean }
  variations: ResultsVariation[]
  comparisons: ResultsComparison[]
}

export interface ExperimentResults {
  experimentKey: string
  flagKey: string
  status: 'running' | 'stopped'
  readAt: string
  window: { start: string; end: string; stoppedAt: string | null; running: boolean; hours: number }
  control: string
  sampleSize: number
  trustworthy: boolean
  srm: {
    observed: { variationId: string; count: number }[]
    expected: { variationId: string; share: number }[]
    expectedSource: 'rollout_at_start' | 'equal_assumed'
    confidence: 'high' | 'low'
    confidenceReason?: string
    chiSquare: number | null
    degreesOfFreedom: number
    pValue: number | null
    threshold: number
    detected: boolean
    reason?: string
  }
  metrics: ResultsMetric[]
  warnings: ResultsWarning[]
  alpha: number
  comparisonCount: number
  alphaPerComparison: number
  /** The level of every interval in this response (wider than 95% with several comparisons). */
  confidenceLevel: number
  thresholds: { minExposuresPerVariation: number; minConversionsTotal: number; srmPValue: number }
  /** Stable code of the fixed-sample-size caveat; the page words it itself and falls back to `peekingNote`. */
  peekingNoteCode: string
  peekingNote: string
  method: string[]
}

/** A reusable audience: explicit members OR (all of) its attribute rules. Workspace-wide. */
export interface SegmentSummary {
  key: string
  name: string
  description: string
  /** ANDed; empty means "explicit members only". */
  rules: Clause[]
  memberCount: number
  version: number
  createdAt: string
  updatedAt: string
}

/** A single segment read: the list omits `members`. */
export interface Segment extends SegmentSummary {
  members: string[]
}

export interface SegmentUsage {
  segmentKey: string
  flags: { flagKey: string; flagName: string; environments: { key: string; enabled: boolean }[] }[]
}

export type ScheduledKind = 'enable' | 'disable' | 'kill' | 'patch'
export type ScheduledStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'missed' | 'cancelled'

/** A change to one flag in one environment at a future time. */
export interface ScheduledChange {
  id: string
  flagKey: string
  environment: string
  kind: ScheduledKind
  change: { rollout?: Record<string, number> | null; fallthroughVariationId?: string; targetingRules?: TargetingRule[] | null }
  runAt: string
  status: ScheduledStatus
  idempotencyKey: string
  baseVersion: number
  attempts: number
  resultCode: string | null
  resultMessage: string | null
  /** The creator's email. */
  createdBy: string
  createdAt: string
  updatedAt: string
  finishedAt: string | null
}

export type PromotionPart = 'rules' | 'rollout' | 'fallthrough' | 'enabled'

export interface PromotionPreview {
  flagKey: string
  source: { environment: string; version: number; enabled: boolean }
  target: { environment: string; version: number; enabled: boolean }
  parts: { part: PromotionPart; selected: boolean; changed: boolean; source: unknown; target: unknown }[]
  defaultParts: PromotionPart[]
  problems: string[]
  nothingToApply: boolean
  canApply: boolean
  expectedVersion: number
  sourceVersion: number
}

export type ApiTokenRole = 'viewer' | 'editor' | 'admin'
export type ApiTokenStatus = 'active' | 'expired' | 'revoked'

/** An API token as the API lists it: never the secret. */
export interface ApiToken {
  id: string
  name: string
  /** "hlt_" and 8 hex characters: identifies the token, is not secret. */
  prefix: string
  role: ApiTokenRole
  /** Empty: every environment of the workspace. */
  environments: { id: string; key: string }[]
  expiresAt: string
  lastUsedAt: string | null
  createdAt: string
  createdBy: string
  revokedAt: string | null
  status: ApiTokenStatus
}
