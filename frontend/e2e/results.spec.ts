import { expect, request as pwRequest, test, type APIRequestContext, type Browser, type Page } from '@playwright/test'
import { API, AUTH, shot, signUp, stamp } from './helpers'
import { fmtCount, fmtDate, fmtP, fmtPercent, fmtPoints, fmtSignedPercent } from '../src/lib/resultsFormat'
import type { ExperimentResults } from '../src/types'

// End to end: data goes in through the REAL POST /track (batched, with the
// SDK's 429 handling), and the Results tab is checked against what the results
// API returns for the same request. Numbers are fixed by seeded generators.

test.describe.configure({ mode: 'serial' })

const alice = { email: `res-alice-${stamp}@test.dev`, password: 'password123' }
const bob = { email: `res-bob-${stamp}@test.dev`, password: 'password123' }

/** A small seeded generator (mulberry32): the same seed always gives the same data. */
function rng(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

interface World {
  api: APIRequestContext
  token: string
  auth: { headers: { Authorization: string } }
  env: string // API base of the dev environment
  workspaceId: string
  state: Awaited<ReturnType<Page['context']>['storageState']>
}

let world: World
const exp: Record<string, { id: string; key: string; flag: string }> = {}
let sdkKeys = 0

async function token(api: APIRequestContext, user: { email: string; password: string }) {
  return ((await (await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: user })).json()).access_token as string)
}

/** POST /track the way an SDK does: batches of 100, same ids on retry, wait out a 429. */
async function track(api: APIRequestContext, sdkKey: string, events: object[]) {
  for (let i = 0; i < events.length; i += 100) {
    const batch = events.slice(i, i + 100)
    for (let attempt = 0; ; attempt++) {
      const res = await api.post(`${API}/track`, { headers: { 'X-Helios-SDK-Key': sdkKey }, data: { events: batch } })
      if (res.status() === 429 && attempt < 60) {
        await new Promise((r) => setTimeout(r, Math.max(1, Number(res.headers()['retry-after'] ?? 1)) * 1000))
        continue
      }
      expect(res.status(), await res.text()).toBe(200)
      const body = await res.json()
      expect(body.rejected, JSON.stringify(body.results.filter((r: { status: string }) => r.status === 'rejected').slice(0, 3))).toBe(0)
      break
    }
  }
}

async function createExperiment(name: string, opts: { rollout?: boolean; start?: boolean } = {}) {
  const { api, auth, env } = world
  const flag = `flag-${name}-${stamp}`
  const key = `exp-${name}-${stamp}`
  await api.post(`${env}/flags`, { ...auth, data: { key: flag, name: flag, variationType: 'boolean', variations: [{ id: 'on', value: true }, { id: 'off', value: false }] } })
  await api.patch(`${env}/flags/${flag}`, { ...auth, data: { enabled: true, ...(opts.rollout === false ? {} : { rollout: { on: 50000, off: 50000 } }) } })
  const created = await (await api.post(`${env}/experiments`, {
    ...auth,
    data: { flagKey: flag, key, name: `Case ${name}`, hypothesis: `Seeded case ${name}`, metrics: [{ name: 'Purchase', eventName: 'purchase', type: 'conversion', isPrimary: true }] },
  })).json()
  if (opts.start !== false) {
    const started = await api.post(`${env}/experiments/${key}/start`, auth)
    expect(started.status(), await started.text()).toBe(200)
  }
  exp[name] = { id: created.id, key, flag }
  return exp[name]
}

/**
 * Exposures for `control` and `variant` subjects ('on' is the control, the flag's first variation),
 * then purchases at the given rates drawn from the seeded generator.
 */
async function seed(name: string, counts: { on: number; off: number }, rates: { on: number; off: number }, seed: number) {
  const { api, auth, env } = world
  const e = exp[name]
  const sdk = (await (await api.post(`${env}/sdk-keys`, { ...auth, data: { name: `results-${name}-${++sdkKeys}` } })).json()).plaintext as string
  const rand = rng(seed)
  const events: object[] = []
  const converts: object[] = []
  for (const arm of ['on', 'off'] as const) {
    for (let i = 0; i < counts[arm]; i++) {
      const subject = `${name}-${arm}-${i}`
      events.push({ id: `x-${subject}`, name: '$exposure', subjectKey: subject, experimentId: e.id, variationId: arm })
      if (rand() < rates[arm]) converts.push({ id: `p-${subject}`, name: 'purchase', subjectKey: subject, experimentId: e.id })
    }
  }
  await track(api, sdk, [...events, ...converts])
}

async function openAs(browser: Browser, state: World['state'], viewport = { width: 1280, height: 900 }) {
  const context = await browser.newContext({ storageState: state, viewport })
  return { context, page: await context.newPage() }
}

async function apiResults(key: string, query = ''): Promise<ExperimentResults> {
  const res = await world.api.get(`${world.env}/experiments/${key}/results${query}`, world.auth)
  expect(res.status()).toBe(200)
  return res.json()
}

const resultsUrl = (key: string) => `/console/experiments/${key}/results`

test.beforeAll(async ({ browser }) => {
  test.setTimeout(420_000)
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await context.newPage()
  await signUp(page, alice)
  const state = await context.storageState()
  await context.close()

  const api = await pwRequest.newContext()
  const t = await token(api, alice)
  const auth = { headers: { Authorization: `Bearer ${t}` } }
  const me = await (await api.get(`${API}/me`, auth)).json()
  const dev = me.workspaces[0].environments.find((e: { key: string }) => e.key === 'dev').id as string
  world = { api, token: t, auth, env: `${API}/environments/${dev}`, workspaceId: me.workspaces[0].id, state }

  for (const [name, opts] of Object.entries({ clear: {}, aa: {}, srm: {}, tiny: {}, change: {}, draft: { start: false }, stopped: {}, running: {} } as Record<string, { start?: boolean }>)) {
    await createExperiment(name, opts)
  }
  // Each case sends through its own SDK key (and so its own rate-limit bucket), in parallel.
  await Promise.all([
    seed('clear', { on: 2000, off: 2000 }, { on: 0.10, off: 0.14 }, 11), // a real +4 point effect
    seed('aa', { on: 2000, off: 2000 }, { on: 0.10, off: 0.10 }, 7), //   no effect
    seed('srm', { on: 1500, off: 2500 }, { on: 0.10, off: 0.10 }, 3), //  a 50/50 rollout delivering 37.5/62.5
    seed('tiny', { on: 40, off: 40 }, { on: 0.12, off: 0.15 }, 5), //     too little data
    seed('change', { on: 300, off: 300 }, { on: 0.10, off: 0.12 }, 9),
    seed('stopped', { on: 150, off: 150 }, { on: 0.10, off: 0.10 }, 13),
    seed('running', { on: 150, off: 150 }, { on: 0.10, off: 0.10 }, 17),
  ])
  // the flag is reconfigured while 'change' runs; 'stopped' is stopped
  await api.patch(`${world.env}/flags/${exp.change.flag}`, { ...auth, data: { rollout: { on: 20000, off: 80000 } } })
  await api.post(`${world.env}/experiments/${exp.stopped.key}/stop`, auth)
})

test.afterAll(async () => {
  await world?.api.dispose()
})

/** The numbers on the page equal what the API returned for the same request. */
async function expectPageMatchesApi(page: Page, data: ExperimentResults) {
  const metric = data.metrics[0]
  for (const v of metric.variations) {
    const row = page.getByTestId(`row-${v.variationId}`)
    await expect(row.getByTestId('exposures')).toHaveText(fmtCount(v.exposures))
    await expect(row.getByTestId('converters')).toHaveText(fmtCount(v.convertingSubjects))
    await expect(row.getByTestId('rate')).toContainText(fmtPercent(v.rate))
  }
  for (const c of metric.comparisons) {
    const block = page.getByTestId(`comparison-${c.variationId}`)
    await expect(block.getByTestId('abs-lift')).toContainText(fmtPoints(c.difference))
    await expect(block.getByTestId('rel-lift')).toContainText(fmtSignedPercent(c.relativeLift))
    await expect(block.getByTestId('p-raw')).toContainText(fmtP(c.pValue))
    await expect(block.getByTestId('p-adjusted')).toContainText(fmtP(c.adjustedPValue))
  }
  await expect(page.getByTestId('sample-size')).toHaveText(`${fmtCount(data.sampleSize)} exposures`)
}

test('a clear effect (10% vs 14%): significant at this sample size, with an interval that excludes zero', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  await page.goto(resultsUrl(exp.clear.key))
  const block = page.getByTestId('comparison-off')
  await expect(block).toBeVisible()
  await expect(block.getByTestId('status')).toHaveText('Significant at this sample size')
  await expect(block.getByTestId('status')).toHaveAttribute('data-status', 'significant')

  const api = await apiResults(exp.clear.key)
  const c = api.metrics[0].comparisons[0]
  expect(c.status).toBe('significant')
  expect(c.differenceInterval.low!).toBeGreaterThan(0) // sensible: the whole interval is above zero
  expect(c.difference!).toBeGreaterThan(0.015)
  expect(c.difference!).toBeLessThan(0.07)
  await expectPageMatchesApi(page, api)

  // no verdict language, the fixed-sample-size note sits with the sample size, the plot is drawn and described
  await expect(page.getByTestId('peeking-note')).toContainText('fixed in advance')
  await expect(page.getByTestId('results')).not.toContainText(/winner|congrat|trophy/i)
  await expect(block.getByTestId('interval-plot')).toHaveAttribute('data-drawn', 'true')
  await expect(block.getByRole('img')).toHaveAttribute('aria-label', /does not include zero/)
  await expect(page.getByTestId('adjustment-note')).toContainText('One comparison')
  await expect(page.getByText('Control', { exact: true })).toBeVisible()
  await expect(page.getByText('Significant at this sample size')).toBeVisible()
  await expect(page.getByTestId('status')).toHaveCount(1)
  await page.getByTestId('method').locator('summary').click()
  await expect(page.getByTestId('method')).toContainText('Why not to stop early')
  await shot(page, '13-results-clear-effect.png')
  await context.close()
})

test('an A/A experiment: not significant', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  await page.goto(resultsUrl(exp.aa.key))
  const block = page.getByTestId('comparison-off')
  await expect(block.getByTestId('status')).toHaveText('Not significant')
  const api = await apiResults(exp.aa.key)
  expect(api.metrics[0].comparisons[0].status).toBe('not_significant')
  await expectPageMatchesApi(page, api)
  await expect(block.getByRole('img')).toHaveAttribute('aria-label', /includes zero/)
  await shot(page, '14-results-aa.png')
  await context.close()
})

test('a sample ratio mismatch: a blocking banner, dimmed numbers, "not reliable" in text', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  await page.goto(resultsUrl(exp.srm.key))
  const banner = page.getByTestId('warning-SRM_FAILED')
  await expect(banner).toBeVisible()
  await expect(banner).toHaveAttribute('data-tone', 'blocking')
  await expect(banner).toContainText('Sample ratio mismatch')
  await expect(banner).toContainText('What to do')
  await expect(page.getByTestId('dimmed-explanation')).toContainText('dimmed because they are not reliable')
  await expect(page.locator('[data-dimmed="true"]')).toHaveCount(1)
  await expect(page.getByTestId('comparison-off').getByTestId('status')).toHaveText('Not reliable (sample ratio mismatch)')
  await expect(page.getByTestId('results')).not.toContainText('Significant at this sample size')
  // the banner comes before the numbers
  const bannerY = (await banner.boundingBox())!.y
  const tableY = (await page.getByTestId('variations-table').boundingBox())!.y
  expect(bannerY).toBeLessThan(tableY)
  const api = await apiResults(exp.srm.key)
  expect(api.trustworthy).toBe(false)
  expect(api.srm.detected).toBe(true)
  await expectPageMatchesApi(page, api)
  await shot(page, '15-results-srm.png')
  await context.close()
})

test('too little data: insufficient data and no verdict', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  await page.goto(resultsUrl(exp.tiny.key))
  const block = page.getByTestId('comparison-off')
  await expect(block.getByTestId('status')).toHaveText('Insufficient data')
  await expect(page.getByTestId('warning-INSUFFICIENT_DATA')).toBeVisible()
  await expect(block.getByTestId('p-raw')).toContainText('—')
  await expect(block.getByTestId('abs-interval')).toContainText('—')
  await expect(block.getByTestId('missing-reasons')).toContainText('fewer than 100 exposures')
  await expect(block.getByTestId('interval-plot')).toHaveAttribute('data-drawn', 'false')
  await expect(page.getByText(/Significant at this sample size|Not significant/)).toHaveCount(0)
  const api = await apiResults(exp.tiny.key)
  expect(api.metrics[0].comparisons[0].status).toBe('insufficient_data')
  expect(api.metrics[0].comparisons[0].pValue).toBeNull()
  await expectPageMatchesApi(page, api)
  await shot(page, '16-results-insufficient-data.png')
  await context.close()
})

test('a flag configuration change during the run is named with its date and what changed', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  await page.goto(resultsUrl(exp.change.key))
  const warning = page.getByTestId('warning-FLAG_CONFIG_CHANGED')
  await expect(warning).toBeVisible()
  const api = await apiResults(exp.change.key)
  const w = api.warnings.find((x) => x.code === 'FLAG_CONFIG_CHANGED')!
  expect(w.detail!.changed).toContain('rollout')
  await expect(warning).toContainText(fmtDate(w.detail!.at as string))
  await expect(warning).toContainText('the rollout split')
  await expect(warning).toContainText('What to do')
  await shot(page, '17-results-flag-change.png')
  await context.close()
})

test('a draft experiment: a friendly empty state', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  await page.goto(resultsUrl(exp.draft.key))
  await expect(page.getByTestId('state-draft')).toContainText('Start the experiment to see results')
  await expect(page.getByTestId('results')).toHaveCount(0)
  await shot(page, '18-results-draft.png')
  await context.close()
})

test('metrics: the picker offers the experiment’s events, allows three at most, and the page matches the API', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  await page.goto(resultsUrl(exp.clear.key))
  const picker = page.getByTestId('metric-picker')
  await expect(picker).toContainText('purchase')
  await picker.getByLabel('Event').selectOption('purchase')
  await picker.getByLabel('Measured as').selectOption('count')
  await picker.getByRole('button', { name: 'Add metric' }).click()
  await expect(page).toHaveURL(/metrics=purchase%3Acount|metrics=purchase:count/)
  await expect(page.getByTestId('metric-purchase-count')).toBeVisible()
  const api = await apiResults(exp.clear.key, '?metrics=purchase:count')
  const c = api.metrics[0].comparisons[0]
  await expect(page.getByTestId('comparison-off').getByTestId('p-raw')).toContainText(fmtP(c.pValue))
  await expect(page.getByTestId('row-on').getByTestId('mean')).toBeVisible() // a count metric shows a mean, not a rate
  // remove it: back to the primary metric
  await picker.getByRole('button', { name: /Remove metric purchase · count per subject/ }).click()
  await expect(page.getByTestId('metric-purchase-conversion')).toBeVisible()
  // fill all three slots: the form closes
  for (const kind of ['conversion', 'count', 'value']) {
    await picker.getByLabel('Event').selectOption('purchase')
    await picker.getByLabel('Measured as').selectOption(kind)
    await picker.getByRole('button', { name: 'Add metric' }).click()
  }
  await expect(picker.getByRole('button', { name: 'Add metric' })).toBeDisabled()
  await expect(picker).toContainText('limit is 3')
  await context.close()
})

test('polling: a running experiment is re-read while the tab is visible, pauses when hidden, and a stopped one is read once', async ({ browser }) => {
  test.setTimeout(120_000)
  const reads = (page: Page, key: string) => {
    const seen: string[] = []
    // finished reads only: in development React runs every effect twice and cancels the first request
    page.on('requestfinished', (r) => r.url().includes(`/environments/`) && r.url().includes(`/experiments/${key}/results`) && r.method() === 'GET' && seen.push(r.url()))
    return seen
  }
  // running
  {
    const { context, page } = await openAs(browser, world.state)
    const seen = reads(page, exp.running.key)
    await page.goto(resultsUrl(exp.running.key))
    await expect(page.getByTestId('results')).toBeVisible()
    await expect.poll(() => seen.length, { timeout: 25_000 }).toBeGreaterThanOrEqual(3)
    // hide the tab: reading stops
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await page.waitForTimeout(500)
    const atHide = seen.length
    await page.waitForTimeout(10_000)
    expect(seen.length).toBe(atHide)
    // show it again: one read at once
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await expect.poll(() => seen.length, { timeout: 5_000 }).toBeGreaterThan(atHide)
    await context.close()
  }
  // stopped: exactly one read
  {
    const { context, page } = await openAs(browser, world.state)
    const seen = reads(page, exp.stopped.key)
    await page.goto(resultsUrl(exp.stopped.key))
    await expect(page.getByTestId('run-status')).toHaveText('Stopped')
    await page.waitForTimeout(9_000)
    expect(seen.length).toBe(1)
    await context.close()
  }
})

test('failure states: slower refresh on 429, timeout, network error, a metric that is gone', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state)
  const url = resultsUrl(exp.aa.key)
  const pattern = new RegExp(`/environments/[^/]+/experiments/${exp.aa.key}/results`) // the API call, not the page

  await page.route(pattern, (r) => r.fulfill({ status: 429, headers: { 'Retry-After': '30', 'Content-Type': 'application/json', 'Access-Control-Expose-Headers': 'Retry-After' }, body: JSON.stringify({ code: 'RATE_LIMITED', message: 'too many requests' }) }))
  await page.goto(url)
  await expect(page.getByTestId('state-rate-limited')).toContainText('Refreshing slower')
  await expect(page.getByTestId('state-rate-limited')).toContainText('30 seconds')

  await page.unroute(pattern)
  await page.route(pattern, (r) => r.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ code: 'RESULTS_TIMEOUT', message: 'slow' }) }))
  await page.goto(url)
  await expect(page.getByTestId('state-timeout')).toContainText('This is taking too long')
  await expect(page.getByTestId('state-timeout')).toContainText('fewer metrics')

  await page.unroute(pattern)
  await page.route(pattern, (r) => r.abort('failed'))
  await page.goto(url)
  await expect(page.getByTestId('state-network')).toContainText('Could not reach the server')
  await page.unroute(pattern)
  await page.getByTestId('state-network').getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByTestId('results')).toBeVisible() // the retry reaches the real API

  await page.unroute(pattern)
  await page.route(pattern, (r) => r.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ code: 'INVALID_REQUEST', message: 'the experiment has no event named gone' }) }))
  await page.goto(`${url}?metrics=gone:conversion`)
  await expect(page.getByTestId('state-metric-gone')).toContainText('no data for this experiment')
  await context.close()
})

test('a phone-width screen keeps the results readable', async ({ browser }) => {
  const { context, page } = await openAs(browser, world.state, { width: 390, height: 844 })
  await page.goto(resultsUrl(exp.clear.key))
  await expect(page.getByTestId('comparison-off')).toBeVisible()
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1) // the page itself does not scroll sideways (tables scroll inside their own box)
  const plot = await page.getByTestId('comparison-off').getByRole('img').boundingBox()
  expect(plot!.width).toBeGreaterThan(200)
  await shot(page, '20-results-phone.png')
  await context.close()
})

test('another tenant cannot open the results by URL: not found, and nothing of the owner in the DOM or the network', async ({ browser }) => {
  const bContext = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const page = await bContext.newPage()
  await signUp(page, bob)

  const bodies: string[] = []
  page.on('response', async (res) => {
    if (res.url().startsWith(API)) bodies.push(await res.text().catch(() => ''))
  })
  await page.goto(resultsUrl(exp.clear.key))
  await expect(page.getByTestId('experiment-not-found')).toContainText('Experiment not found')
  await page.goto(`/console/experiments/${exp.clear.key}`)
  await expect(page.getByTestId('experiment-not-found')).toBeVisible()

  // (the experiment key itself is in the address B typed, so it is not a secret here)
  const secrets = [exp.clear.id, exp.clear.flag, world.workspaceId, alice.email, 'clear-on-0', 'Case clear']
  const dom = await page.content()
  for (const s of secrets) expect(dom, `the page contains ${s}`).not.toContain(s)
  expect(bodies.length).toBeGreaterThan(0)
  for (const body of bodies) for (const s of secrets) expect(body).not.toContain(s)

  // and the API itself answers B with a clean 404 for A's results
  const api = await pwRequest.newContext()
  const bt = await token(api, bob)
  const res = await api.get(`${world.env}/experiments/${exp.clear.key}/results`, { headers: { Authorization: `Bearer ${bt}` } })
  expect(res.status()).toBe(404)
  const text = await res.text()
  for (const s of secrets) expect(text).not.toContain(s)
  await api.dispose()
  await shot(page, '19-results-cross-tenant.png')
  await bContext.close()
})
