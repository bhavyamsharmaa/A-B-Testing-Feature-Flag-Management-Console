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

export interface Me {
  id: string
  email: string
  activeWorkspaceId: string
  workspaces: Workspace[]
  invites: PendingInvite[]
}

export interface Member {
  userId: string
  email: string
  role: WorkspaceRole
  joinedAt: string
  isYou: boolean
}

export interface WorkspaceInvite {
  id: string
  email: string
  role: WorkspaceRole
  invitedByEmail: string
  createdAt: string
  expiresAt: string
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
