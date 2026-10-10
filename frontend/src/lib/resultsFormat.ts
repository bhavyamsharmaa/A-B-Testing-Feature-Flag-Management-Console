// Pure helpers for showing experiment results. Nothing here may ever produce
// the text "NaN", "Infinity", "null" or "undefined": a missing or non-finite
// number is an em dash, and the reason is given in words (reasonText).

import type { ComparisonStatus, MetricKind, MetricSelection, ResultsComparison, ResultsWarning } from '../types'

export const DASH = '—'
const LOCALE = 'en-US' // fixed: the same numbers read the same everywhere (and in tests)

const finite = (n: number | null | undefined): n is number => typeof n === 'number' && Number.isFinite(n)

/** Counts with thousands separators. */
export function fmtCount(n: number | null | undefined): string {
  return finite(n) ? Math.round(n).toLocaleString(LOCALE) : DASH
}

/** A rate (0.1034) as a percentage with two decimals: 10.34%. */
export function fmtPercent(rate: number | null | undefined): string {
  return finite(rate) ? `${(rate * 100).toLocaleString(LOCALE, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}%` : DASH
}

const withSign = (s: string, n: number) => (n > 0 && s[0] !== '+' ? `+${s}` : s)

/** A relative lift (0.39) as a signed percentage: +39.00%. */
export function fmtSignedPercent(rel: number | null | undefined): string {
  if (!finite(rel)) return DASH
  return withSign(fmtPercent(rel), roundedSign(rel * 100, 2))
}

/** A difference of two rates (0.04) in percentage points: +4.00 pp. */
export function fmtPoints(diff: number | null | undefined): string {
  if (!finite(diff)) return DASH
  const pts = (diff * 100).toLocaleString(LOCALE, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return `${withSign(pts, roundedSign(diff * 100, 2))} pp`
}

/** Sign after rounding, so a value that rounds to 0.00 is not shown as +0.00 or -0.00. */
function roundedSign(v: number, digits: number): number {
  const r = Math.round(v * 10 ** digits) / 10 ** digits
  return r === 0 ? 0 : v
}

/** A plain number (a mean, a standard deviation) with up to 2 decimals, more when it is small. */
export function fmtNumber(n: number | null | undefined): string {
  if (!finite(n)) return DASH
  const small = Math.abs(n) > 0 && Math.abs(n) < 0.01
  return n.toLocaleString(LOCALE, { maximumFractionDigits: small ? 4 : 2 })
}

export function fmtSignedNumber(n: number | null | undefined): string {
  return finite(n) ? withSign(fmtNumber(n), roundedSign(n, 2)) : DASH
}

/** p-values: "< 0.001" when tiny, else three decimals. */
export function fmtP(p: number | null | undefined): string {
  if (!finite(p)) return DASH
  if (p < 0.001) return '< 0.001'
  return p.toLocaleString(LOCALE, { minimumFractionDigits: 3, maximumFractionDigits: 3 })
}

/** A confidence level (0.975) as "97.5%". */
export function fmtLevel(level: number | null | undefined): string {
  return finite(level) ? `${(level * 100).toLocaleString(LOCALE, { maximumFractionDigits: 1 })}%` : DASH
}

/** Formats an interval with the same formatter as its point estimate. */
export function fmtInterval(low: number | null | undefined, high: number | null | undefined, f: (n: number | null | undefined) => string): string {
  return finite(low) && finite(high) ? `${f(low)} to ${f(high)}` : DASH
}

/** The formatter for a difference of the given kind of metric. */
export const differenceFormatter = (kind: MetricKind) => (kind === 'conversion' ? fmtPoints : fmtSignedNumber)

/** Event name and kind, for headings and chips. */
export const metricLabel = (m: MetricSelection) => `${m.eventName} · ${KIND_WORDS[m.kind]}`

export const KIND_WORDS: Record<MetricKind, string> = {
  conversion: 'conversion',
  count: 'count per subject',
  value: 'value per subject',
}

// ---- reasons --------------------------------------------------------------

const REASONS: Record<string, string> = {
  ZERO_VARIANCE: 'every subject had the same value, so there is no spread to test against',
  TOO_FEW_SUBJECTS: 'a variation has fewer than 2 subjects',
  CONTROL_RATE_ZERO: 'the control rate is 0, so a relative lift cannot be worked out',
  VARIANT_RATE_ZERO: 'the variation had no conversions, so this interval cannot be computed',
  CONTROL_MEAN_NOT_POSITIVE: 'the control mean is not above 0, so a relative lift cannot be worked out',
  CONTROL_HAS_NO_EXPOSURES: 'the control has no exposures yet',
  VARIANT_HAS_NO_EXPOSURES: 'this variation has no exposures yet',
  INSUFFICIENT_DATA: 'there is not enough data yet for a verdict',
  NOT_COMPUTABLE: 'it could not be computed from this data',
}

/** Plain words for an API reason code. An unknown code gets a generic sentence, never the code itself. */
export function reasonText(code: string | null | undefined): string {
  if (!code) return 'not available'
  if (REASONS[code]) return REASONS[code]
  const below = /^EXPOSURES_BELOW_(\d+)$/.exec(code)
  if (below) return `a variation has fewer than ${Number(below[1]).toLocaleString(LOCALE)} exposures`
  const conv = /^CONVERSIONS_BELOW_(\d+)$/.exec(code)
  if (conv) return `fewer than ${Number(conv[1]).toLocaleString(LOCALE)} conversions in total`
  return 'not available for this data'
}

/** Why a field of a comparison is missing, in words (or null when it is not missing). */
export function missingReason(c: ResultsComparison, field: string): string | null {
  // Below the data minimum every inferential field is empty for the same reason: say which minimum is missed.
  if (c.status === 'insufficient_data') {
    return c.statusReasons?.length ? c.statusReasons.map(reasonText).join('; and ') : reasonText('INSUFFICIENT_DATA')
  }
  const code = c.reasons?.[field]
  if (code) return reasonText(code)
  if (c.status === 'invalid' && c.statusReasons?.length) return reasonText(c.statusReasons[0])
  return null
}

/** The fixed-sample-size caveat, worded here (so it can be translated) and keyed by the API's stable code. */
const PEEKING: Record<string, string> = {
  FIXED_SAMPLE_SIZE:
    'These p-values and intervals are valid only for a sample size fixed in advance. Looking at results repeatedly while the experiment runs and stopping when a difference appears makes false alarms far more common than 5%. No sequential correction is applied.',
}

/** The caveat for a code, or the API's own default text for a code this page does not know. */
export function peekingText(code: string | null | undefined, fallback: string | null | undefined): string {
  if (code && PEEKING[code]) return PEEKING[code]
  const text = safeText(fallback ?? '')
  return text && text !== DASH ? text : PEEKING.FIXED_SAMPLE_SIZE
}

/** One sentence for a metric whose status is not ok (said once for the metric, not per comparison). */
export function metricStatusText(status: string, reasons: string[] | undefined): string | null {
  if (status === 'ok') return null
  const why = (reasons ?? []).map(reasonText).join('; and ')
  if (status === 'insufficient_data') return `Insufficient data: ${why || reasonText('INSUFFICIENT_DATA')}. Only the raw numbers are shown, with no verdict.`
  return `Nothing can be compared yet${why ? `: ${why}` : ''}.`
}

// ---- statuses -------------------------------------------------------------

export type StatusKey = 'significant' | 'not_significant' | 'insufficient' | 'not_reliable' | 'unavailable'

export interface StatusInfo {
  key: StatusKey
  label: string
  /** One line saying what the status does and does not mean. */
  detail: string
}

/**
 * The wording of a comparison's status. A sample ratio mismatch overrides it:
 * the numbers cannot be trusted whatever their p-value says.
 */
export function statusInfo(status: ComparisonStatus, trustworthy: boolean, running: boolean): StatusInfo {
  if (!trustworthy) {
    return {
      key: 'not_reliable',
      label: 'Not reliable (sample ratio mismatch)',
      detail: 'The split of subjects between variations is not what was configured, so these numbers cannot be trusted.',
    }
  }
  switch (status) {
    case 'significant':
      return {
        key: 'significant',
        label: 'Significant at this sample size',
        detail: running
          ? 'Not a verdict: the experiment is still running, and a result that looks significant now can disappear with more data.'
          : 'The difference is unlikely to be chance if the sample size was fixed in advance. It does not say the change is worth shipping.',
      }
    case 'not_significant':
      return { key: 'not_significant', label: 'Not significant', detail: 'No difference can be told apart from chance at this sample size. That is not proof there is none.' }
    case 'insufficient_data':
      return { key: 'insufficient', label: 'Insufficient data', detail: 'There is not enough data for a verdict yet. Only the raw numbers are shown.' }
    default:
      return { key: 'unavailable', label: 'Not available', detail: 'This comparison cannot be computed from the data so far.' }
  }
}

// ---- warnings ---------------------------------------------------------------

export interface WarningInfo {
  code: string
  rank: number
  tone: 'blocking' | 'caution' | 'note'
  title: string
  /** One plain sentence about what it is. */
  what: string
  /** What to do about it. */
  action: string
}

const ORDER = [
  'SRM_FAILED',
  'FLAG_CONFIG_CHANGED',
  'INSUFFICIENT_DATA',
  'NO_EXPOSURES',
  'EVENTS_WITHOUT_EXPOSURE',
  'EVENTS_BEFORE_EXPOSURE',
  'EVENTS_OUTSIDE_WINDOW',
  'CLIENT_CLOCK_ADJUSTED',
  'EXPOSURES_UNVERIFIED',
  'EXPERIMENT_RESTARTED',
  'SHORT_RUN',
  'SINGLE_VARIATION',
  'SRM_LOW_CONFIDENCE',
]

const FIELD_WORDS: Record<string, string> = {
  rollout: 'the rollout split',
  targetingRules: 'the targeting rules',
  fallthroughVariationId: 'the default variation',
  enabled: 'whether the flag is on',
  variations: 'the variations',
}

const asList = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
const asNumber = (v: unknown): number | null => (finite(v as number) ? (v as number) : null)

export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return DASH
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? DASH : d.toLocaleString(LOCALE, { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' }) + ' UTC'
}

function joinWords(items: string[]): string {
  if (items.length <= 1) return items[0] ?? ''
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
}

export function warningInfo(w: ResultsWarning): WarningInfo {
  const rank = ORDER.indexOf(w.code)
  const base = { code: w.code, rank: rank === -1 ? ORDER.length : rank }
  const d = w.detail ?? {}
  switch (w.code) {
    case 'SRM_FAILED':
      return {
        ...base, tone: 'blocking', title: 'Sample ratio mismatch: do not rely on these numbers',
        what: 'The number of subjects in each variation is not what the rollout was set to deliver, which usually means assignment or exposure reporting is broken, or bots or redirects are skewing who is counted.',
        action: 'Find the cause before reading anything below: check that every client reports its exposure, that nothing filters one variation, and that the rollout did not change.',
      }
    case 'FLAG_CONFIG_CHANGED': {
      const fields = asList(d.changed)
      const only = fields.length === 1 && fields[0] === 'enabled'
      return {
        ...base, tone: only ? 'note' : 'caution',
        title: `The flag was changed during the run on ${fmtDate(typeof d.at === 'string' ? d.at : null)}`,
        what: `What changed: ${joinWords(fields.map((f) => FIELD_WORDS[f] ?? 'its configuration'))}. Subjects exposed before and after the change may have been assigned differently.`,
        action: only ? 'Check whether the flag being off for a while affects the comparison.' : 'Consider stopping this experiment and starting a new one so the data comes from one configuration.',
      }
    }
    case 'INSUFFICIENT_DATA':
      return {
        ...base, tone: 'caution', title: 'Insufficient data: no verdict is shown',
        what: 'A comparison needs at least the minimum number of exposures in each variation (and conversions, for a conversion metric) before it is tested.',
        action: 'Keep collecting data and decide the sample size in advance rather than stopping when a number looks good.',
      }
    case 'NO_EXPOSURES':
      return { ...base, tone: 'caution', title: 'No exposures recorded yet', what: 'Nothing has reported that a subject saw a variation, so there is nothing to compare.', action: 'Check that your SDK reports an exposure when it shows a variation.' }
    case 'EVENTS_WITHOUT_EXPOSURE': {
      const n = asNumber(d.events)
      return {
        ...base, tone: 'caution', title: `${fmtCount(n)} event${n === 1 ? '' : 's'} left out: no exposure`,
        what: 'These events came from subjects with no recorded exposure to this experiment, so they cannot be placed in a variation.',
        action: 'Report an exposure for every subject before (or together with) their events.',
      }
    }
    case 'EVENTS_BEFORE_EXPOSURE': {
      const n = asNumber(d.events)
      return {
        ...base, tone: 'note', title: `${fmtCount(n)} event${n === 1 ? '' : 's'} left out: before the exposure`,
        what: 'These events happened before the subject was exposed, so they cannot have been caused by the variation.',
        action: 'Usually nothing: it is expected for subjects who were active before the experiment. A large share may mean exposures are reported late.',
      }
    }
    case 'EVENTS_OUTSIDE_WINDOW': {
      const n = asNumber(d.events)
      return {
        ...base, tone: 'note', title: `${fmtCount(n)} event${n === 1 ? '' : 's'} left out: outside the run`,
        what: 'These events are dated before the experiment started or after it stopped.',
        action: 'Nothing, unless you expected them to count; check client clocks if there are many.',
      }
    }
    case 'EXPOSURES_UNVERIFIED': {
      const n = asNumber(d.exposures)
      return {
        ...base, tone: 'caution', title: `${fmtCount(n)} exposure${n === 1 ? '' : 's'} could not be verified`,
        what: 'These exposures arrived without a valid exposure token, so nothing shows that Helios actually served the variation they report. They are included in the numbers.',
        action: 'Update your SDK to pass the exposure token from the evaluation, then check this count falls to zero.',
      }
    }
    case 'CLIENT_CLOCK_ADJUSTED': {
      const n = asNumber(d.events)
      return {
        ...base, tone: 'note', title: `${fmtCount(n)} event${n === 1 ? '' : 's'} had a client clock far off`,
        what: 'These events carried a timestamp well outside the run, so the time they reached the server was used to bound them. They are counted.',
        action: 'Usually nothing; many of them suggest devices with wrong clocks or events replayed long after they happened.',
      }
    }
    case 'EXPERIMENT_RESTARTED':
      return {
        ...base, tone: 'caution', title: 'Another experiment ran on the same flag',
        what: `Experiment ${typeof d.experimentKey === 'string' ? `"${d.experimentKey}" ` : ''}also used this flag in this environment, so some subjects may have seen both.`,
        action: 'Compare with care; each run keeps its own data, but earlier exposure can carry over.',
      }
    case 'SHORT_RUN':
      return {
        ...base, tone: 'note', title: 'The run is very short',
        what: 'Under a day of data misses daily and weekly patterns in how people behave.',
        action: 'Let it run for at least one full week if your traffic has weekly cycles.',
      }
    case 'SINGLE_VARIATION':
      return { ...base, tone: 'caution', title: 'The flag has only one variation', what: 'There is nothing to compare against.', action: 'Add a second variation to the flag and start a new experiment.' }
    case 'SRM_LOW_CONFIDENCE':
      return {
        ...base, tone: 'note', title: 'The sample-ratio check is low confidence',
        what: 'No rollout split was recorded at start, or targeting rules existed, so the expected split is approximate.',
        action: 'Treat a passed check as weak evidence; set a rollout split before starting next time.',
      }
    default:
      return { ...base, tone: 'note', title: 'Data-quality notice', what: 'The results API raised a notice this page does not describe yet.', action: 'See the API response for details.' }
  }
}

/** Warnings most serious first: the order the page shows them in. */
export function sortedWarnings(ws: ResultsWarning[]): WarningInfo[] {
  return ws.map(warningInfo).sort((a, b) => a.rank - b.rank)
}

/** Guard for any text the page is about to show: never NaN/Infinity/null/undefined. */
export function safeText(s: string): string {
  return /\b(NaN|Infinity|undefined|null)\b/.test(s) ? DASH : s
}
