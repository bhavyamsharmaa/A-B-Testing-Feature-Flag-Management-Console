import { expect, test } from '@playwright/test'
import { renderToStaticMarkup } from 'react-dom/server'
import { MetricPicker } from '../../src/components/results/MetricPicker'
import { ResultsView } from '../../src/components/results/ResultsView'
import {
  DASH, fmtCount, fmtInterval, fmtLevel, fmtNumber, fmtP, fmtPercent, fmtPoints, fmtSignedNumber, fmtSignedPercent,
  metricStatusText, peekingText, reasonText, safeText, sortedWarnings, statusInfo,
} from '../../src/lib/resultsFormat'
import type { ExperimentResults, ResultsComparison, ResultsWarning } from '../../src/types'

// Pure and server-rendered checks: no browser, no network.

/** Text that must never reach a person. */
const LEAK = /NaN|Infinity|undefined|\bnull\b/
const textOf = (html: string) => html.replace(/<[^>]*>/g, ' ').replace(/&[a-z]+;|&#\d+;/g, ' ')

test.describe('formatting', () => {
  test('counts, percentages and points use fixed, consistent rounding', () => {
    expect(fmtCount(1234567)).toBe('1,234,567')
    expect(fmtCount(0)).toBe('0')
    expect(fmtPercent(0.1)).toBe('10.00%')
    expect(fmtPercent(0.10345)).toBe('10.35%')
    expect(fmtSignedPercent(0.4)).toBe('+40.00%')
    expect(fmtSignedPercent(-0.052)).toBe('-5.20%')
    expect(fmtSignedPercent(0.00001)).toBe('0.00%') // no "+0.00%"
    expect(fmtPoints(0.04)).toBe('+4.00 pp')
    expect(fmtPoints(-0.0123)).toBe('-1.23 pp')
    expect(fmtNumber(12.3456)).toBe('12.35')
    expect(fmtNumber(0.004567)).toBe('0.0046')
    expect(fmtSignedNumber(3)).toBe('+3')
    expect(fmtLevel(0.975)).toBe('97.5%')
    expect(fmtLevel(0.95)).toBe('95%')
  })

  test('p-values: "< 0.001" when tiny, else three decimals', () => {
    expect(fmtP(0.0004)).toBe('< 0.001')
    expect(fmtP(0)).toBe('< 0.001')
    expect(fmtP(0.001)).toBe('0.001')
    expect(fmtP(0.0321)).toBe('0.032')
    expect(fmtP(1)).toBe('1.000')
  })

  test('anything missing or not finite is an em dash, never its own text', () => {
    for (const bad of [null, undefined, NaN, Infinity, -Infinity]) {
      for (const f of [fmtCount, fmtPercent, fmtSignedPercent, fmtPoints, fmtNumber, fmtSignedNumber, fmtP, fmtLevel]) {
        expect(f(bad as number | null | undefined)).toBe(DASH)
      }
    }
    expect(fmtInterval(0.1, NaN, fmtPoints)).toBe(DASH)
    expect(fmtInterval(null, 0.2, fmtPoints)).toBe(DASH)
    expect(fmtInterval(0.01, 0.03, fmtPoints)).toBe('+1.00 pp to +3.00 pp')
  })

  test('safeText replaces any string that would show a non-value', () => {
    expect(safeText('fine')).toBe('fine')
    for (const s of ['x NaN y', 'Infinity', 'is undefined', 'was null']) expect(safeText(s)).toBe(DASH)
    expect(LEAK.test('a NaN here')).toBe(true) // the guard the tests below rely on can fail
  })
})

test.describe('reasons and statuses', () => {
  test('reason codes become plain words; unknown codes never leak', () => {
    expect(reasonText('ZERO_VARIANCE')).toMatch(/same value/)
    expect(reasonText('CONTROL_RATE_ZERO')).toMatch(/relative lift cannot be worked out/)
    expect(reasonText('EXPOSURES_BELOW_100')).toBe('a variation has fewer than 100 exposures')
    expect(reasonText('CONVERSIONS_BELOW_25')).toBe('fewer than 25 conversions in total')
    expect(reasonText('SOMETHING_NEW')).toBe('not available for this data')
    expect(reasonText(null)).toBe('not available')
    expect(reasonText('SOMETHING_NEW')).not.toMatch(/SOMETHING/)
  })

  test('status wording is neutral, and a sample ratio mismatch overrides everything', () => {
    expect(statusInfo('significant', true, false).label).toBe('Significant at this sample size')
    expect(statusInfo('not_significant', true, false).label).toBe('Not significant')
    expect(statusInfo('insufficient_data', true, true).label).toBe('Insufficient data')
    expect(statusInfo('invalid', true, true).label).toBe('Not available')
    for (const s of ['significant', 'not_significant', 'insufficient_data', 'invalid'] as const) {
      expect(statusInfo(s, false, true).label).toBe('Not reliable (sample ratio mismatch)')
    }
    for (const s of ['significant', 'not_significant', 'insufficient_data', 'invalid'] as const) {
      for (const running of [true, false]) {
        const t = statusInfo(s, true, running)
        expect(`${t.label} ${t.detail}`).not.toMatch(/winner|win\b|success|congrat|best/i)
      }
    }
    expect(statusInfo('significant', true, true).detail).toMatch(/Not a verdict/)
  })

  test('warnings are ordered by severity and name the date and what changed', () => {
    const ws: ResultsWarning[] = [
      { code: 'SINGLE_VARIATION', severity: 'warning', message: '', detail: {} },
      { code: 'SHORT_RUN', severity: 'info', message: '', detail: {} },
      { code: 'EXPERIMENT_RESTARTED', severity: 'warning', message: '', detail: { experimentKey: 'older' } },
      { code: 'EVENTS_WITHOUT_EXPOSURE', severity: 'warning', message: '', detail: { events: 1234 } },
      { code: 'INSUFFICIENT_DATA', severity: 'warning', message: '', detail: {} },
      { code: 'FLAG_CONFIG_CHANGED', severity: 'warning', message: '', detail: { at: '2026-10-01T10:30:00Z', changed: ['rollout', 'targetingRules'] } },
      { code: 'SRM_FAILED', severity: 'critical', message: '', detail: {} },
    ]
    const order = sortedWarnings(ws).map((w) => w.code)
    expect(order).toEqual(['SRM_FAILED', 'FLAG_CONFIG_CHANGED', 'INSUFFICIENT_DATA', 'EVENTS_WITHOUT_EXPOSURE', 'EXPERIMENT_RESTARTED', 'SHORT_RUN', 'SINGLE_VARIATION'])
    const change = sortedWarnings(ws).find((w) => w.code === 'FLAG_CONFIG_CHANGED')!
    expect(change.title).toContain('Oct 1, 2026')
    expect(change.what).toContain('the rollout split and the targeting rules')
    expect(sortedWarnings(ws)[0].tone).toBe('blocking')
    for (const w of sortedWarnings(ws)) {
      expect(w.what.length).toBeGreaterThan(10)
      expect(w.action.length).toBeGreaterThan(10)
    }
    expect(sortedWarnings([{ code: 'EVENTS_WITHOUT_EXPOSURE', severity: 'warning', message: '', detail: { events: 1 } }])[0].title).toBe('1 event left out: no exposure')
  })
})

// ---- rendering never shows a non-value --------------------------------------

const comparison = (over: Partial<ResultsComparison> = {}): ResultsComparison => ({
  variationId: 'variant', against: 'control', status: 'not_significant', difference: 0.01,
  differenceInterval: { low: -0.01, high: 0.03 }, relativeLift: 0.1, relativeLiftInterval: { low: -0.1, high: 0.3 },
  statistic: 1, degreesOfFreedom: null, pValue: 0.2, adjustedPValue: 0.2, trustworthy: true,
  ...over,
})

function results(over: Partial<ExperimentResults> = {}): ExperimentResults {
  return {
    experimentKey: 'e', flagKey: 'f', status: 'running', comparisonCount: 1, alphaPerComparison: 0.05, confidenceLevel: 0.95, peekingNoteCode: 'FIXED_SAMPLE_SIZE', readAt: '2026-10-10T10:00:00Z',
    window: { start: '2026-10-09T10:00:00Z', end: '2026-10-10T10:00:00Z', stoppedAt: null, running: true, hours: 24 },
    control: 'control', sampleSize: 4000, trustworthy: true,
    srm: { observed: [], expected: [], expectedSource: 'rollout_at_start', confidence: 'high', chiSquare: 1, degreesOfFreedom: 1, pValue: 0.5, threshold: 0.001, detected: false },
    metrics: [{
      status: 'ok',
      metric: { eventName: 'buy', kind: 'conversion', isPrimary: true, name: 'Purchase' },
      variations: [
        { variationId: 'control', isControl: true, exposures: 2000, convertingSubjects: 200, events: 200, rate: 0.1, mean: null, stdDev: null, trustworthy: true },
        { variationId: 'variant', isControl: false, exposures: 2000, convertingSubjects: 210, events: 210, rate: 0.105, mean: null, stdDev: null, trustworthy: true },
      ],
      comparisons: [comparison()],
    }],
    warnings: [], alpha: 0.05, thresholds: { minExposuresPerVariation: 100, minConversionsTotal: 25, srmPValue: 0.001 },
    peekingNote: 'Default text.', method: ['Subjects are the unit.'],
    ...over,
  }
}

test.describe('rendering', () => {
  test('a normal result shows the sample size and the fixed-horizon note next to the numbers', () => {
    const text = textOf(renderToStaticMarkup(<ResultsView data={results()} />))
    expect(text).toContain('4,000 exposures')
    expect(text).toContain('valid only for a sample size fixed in advance')
    expect(text).toContain('10.00%')
    expect(text).toContain('Not significant')
    expect(text).toContain('One comparison, so no adjustment was needed')
    expect(text).not.toMatch(/winner|trophy|congrat/i)
    expect(text).toMatch(/Control/)
  })

  test('every nullable field null, and non-finite numbers smuggled in: no NaN, Infinity, null or undefined text', () => {
    const nulls = comparison({
      status: 'invalid', statusReasons: ['ZERO_VARIANCE'], difference: null, differenceInterval: { low: null, high: null },
      relativeLift: null, relativeLiftInterval: { low: null, high: null }, statistic: null, pValue: null, adjustedPValue: null,
      reasons: { pValue: 'ZERO_VARIANCE', relativeLift: 'CONTROL_RATE_ZERO', differenceInterval: 'WHATEVER_NEW' },
    })
    const nasty = comparison({
      difference: NaN, differenceInterval: { low: -Infinity, high: Infinity }, relativeLift: NaN, relativeLiftInterval: { low: NaN, high: 1 },
      pValue: Infinity, adjustedPValue: NaN, alphaPerComparison: NaN, confidenceLevel: NaN,
    })
    const base = results()
    const weird = results({
      sampleSize: NaN,
      alphaPerComparison: NaN,
      confidenceLevel: Infinity,
      comparisonCount: 2,
      peekingNoteCode: 'SOMETHING_NEW',
      peekingNote: 'Seen NaN times',
      method: ['p = NaN here', 'ok'],
      metrics: [
        { ...base.metrics[0], comparisons: [nulls, nasty], variations: base.metrics[0].variations.map((v) => ({ ...v, rate: null, exposures: NaN, convertingSubjects: Infinity })) },
        { status: 'unavailable', statusReasons: ['SINGLE_VARIATION'], metric: { eventName: 'rev', kind: 'value', isPrimary: false }, variations: base.metrics[0].variations.map((v) => ({ ...v, rate: null, mean: null, stdDev: NaN })), comparisons: [] },
      ],
      warnings: [{ code: 'EVENTS_WITHOUT_EXPOSURE', severity: 'warning', message: 'NaN', detail: { events: NaN } }, { code: 'BRAND_NEW', severity: 'info', message: 'undefined', detail: {} }],
    })
    const text = textOf(renderToStaticMarkup(<ResultsView data={weird} />))
    expect(text, text).not.toMatch(LEAK)
    expect(text).toContain(DASH)
    expect(text).toContain('every subject had the same value')
    expect(text).not.toContain('WHATEVER_NEW')
    expect(text).not.toContain('ZERO_VARIANCE')
  })

  test('the leak guard really fails when a non-value is rendered', () => {
    // A deliberately broken fixture the page cannot defend against (raw text from a field it prints verbatim).
    const broken = textOf(renderToStaticMarkup(<div>{String(NaN)}</div>))
    expect(LEAK.test(broken)).toBe(true)
  })

  test('a sample ratio mismatch dims the numbers, explains why, and says not reliable in text', () => {
    const base = results()
    const bad = results({
      trustworthy: false,
      warnings: [{ code: 'SRM_FAILED', severity: 'critical', message: '', detail: {} }],
      metrics: [{ ...base.metrics[0], comparisons: [comparison({ status: 'significant', trustworthy: false })] }],
    })
    const html = renderToStaticMarkup(<ResultsView data={bad} />)
    const text = textOf(html)
    expect(html).toContain('data-dimmed="true"')
    expect(text).toContain('Not reliable (sample ratio mismatch)')
    expect(text).toContain('dimmed because they are not reliable')
    expect(text).not.toContain('Significant at this sample size')
    expect(html).toMatch(/data-testid="warning-SRM_FAILED"[^>]*data-tone="blocking"/)
  })

  test('with three variations the adjustment and the wider level are stated', () => {
    const base = results()
    const c = (id: string) => comparison({ variationId: id })
    const three = results({ comparisonCount: 2, alphaPerComparison: 0.025, confidenceLevel: 0.975, metrics: [{ ...base.metrics[0], comparisons: [c('a'), c('b')] }] })
    const text = textOf(renderToStaticMarkup(<ResultsView data={three} />))
    expect(text).toContain('Bonferroni adjustment applied')
    expect(text).toContain('97.5%')
    expect(text).toContain('p-value adjusted for 2 comparisons')
  })

  test('every status has a shape and text, not just colour', () => {
    const base = results()
    for (const status of ['significant', 'not_significant', 'insufficient_data', 'invalid'] as const) {
      const html = renderToStaticMarkup(<ResultsView data={results({ metrics: [{ ...base.metrics[0], comparisons: [comparison({ status })] }] })} />)
      const badge = /data-testid="status"[^>]*>(.*?)<\/span>/.exec(html)?.[1] ?? ''
      expect(badge).toContain('<svg')
      expect(textOf(badge).trim().length).toBeGreaterThan(5)
    }
  })

  test('the interval plot has an accessible name and a text fallback', () => {
    const html = renderToStaticMarkup(<ResultsView data={results()} />)
    expect(html).toMatch(/<svg[^>]*role="img"[^>]*aria-label="[^"]*interval/)
    expect(textOf(html)).toMatch(/estimate [+-]1\.00 pp, interval -1\.00 pp to \+3\.00 pp, includes zero/)
  })
})

test.describe('API fields the console now relies on', () => {
  test('the interval level and adjustment come from the response, not from each comparison', () => {
    const base = results()
    const c = comparison({ alphaPerComparison: 0.001, confidenceLevel: 0.999 }) // deprecated per-comparison values must be ignored
    const text = textOf(renderToStaticMarkup(<ResultsView data={results({ metrics: [{ ...base.metrics[0], comparisons: [c] }] })} />))
    expect(text).toContain('95% interval, absolute')
    expect(text).not.toContain('99.9%')
  })

  test('"insufficient data" is said once per metric, not once per comparison', () => {
    const base = results()
    const mk = (id: string) => comparison({ variationId: id, status: 'insufficient_data', statusReasons: ['EXPOSURES_BELOW_100'], pValue: null, adjustedPValue: null, differenceInterval: { low: null, high: null }, relativeLiftInterval: { low: null, high: null } })
    const data = results({
      comparisonCount: 2, alphaPerComparison: 0.025, confidenceLevel: 0.975,
      metrics: [{ ...base.metrics[0], status: 'insufficient_data', statusReasons: ['EXPOSURES_BELOW_100'], comparisons: [mk('a'), mk('b')] }],
    })
    const html = renderToStaticMarkup(<ResultsView data={data} />)
    expect(html.match(/data-testid="metric-status"/g)?.length).toBe(1)
    // (each em dash keeps its own screen-reader explanation; the visible sentence appears once)
    expect(textOf(html).match(/Insufficient data: /g)?.length).toBe(1)
    expect(html.match(/data-testid="missing-reasons"/g)).toBeNull()
    expect(metricStatusText('ok', undefined)).toBeNull()
    expect(metricStatusText('unavailable', ['SINGLE_VARIATION'])).toMatch(/Nothing can be compared/)
    expect(metricStatusText('insufficient_data', ['EXPOSURES_BELOW_100', 'CONVERSIONS_BELOW_25'])).toContain('fewer than 25 conversions in total')
  })

  test('the caveat is worded by code, with the API text as the fallback for a code the page does not know', () => {
    expect(peekingText('FIXED_SAMPLE_SIZE', 'ignored')).toMatch(/fixed in advance/)
    expect(peekingText('SOMETHING_NEW', 'The API says this.')).toBe('The API says this.')
    expect(peekingText('SOMETHING_NEW', 'broken NaN')).toMatch(/fixed in advance/) // a fallback that would show a non-value is replaced
    expect(peekingText(undefined, undefined)).toMatch(/fixed in advance/)
  })

  test('new warnings are described in words', () => {
    const [unverified] = sortedWarnings([{ code: 'EXPOSURES_UNVERIFIED', severity: 'warning', message: '', detail: { exposures: 12 } }])
    expect(unverified.title).toBe('12 exposures could not be verified')
    expect(unverified.action).toMatch(/exposure token/)
    const [clock] = sortedWarnings([{ code: 'CLIENT_CLOCK_ADJUSTED', severity: 'info', message: '', detail: { events: 1 } }])
    expect(clock.title).toBe('1 event had a client clock far off')
  })

  test('the picker offers only the kinds the data supports', () => {
    const html = renderToStaticMarkup(
      <MetricPicker selected={[]} primaryLabel="buy · conversion" onChange={() => {}} options={[{ eventName: 'page_view', defaultKind: 'conversion', kinds: ['conversion', 'count'], hint: '10 events' }]} />,
    )
    // before an event is chosen all kinds are shown; the per-event restriction is applied once one is chosen
    expect(textOf(html)).toContain('page_view')
  })
})
