import { expect, request as pwRequest, test, type APIRequestContext, type Browser, type Page } from '@playwright/test'
import { API, AUTH, shot, signUp, stamp } from './helpers'
import { zonedToUtc } from '../src/lib/schedule'

test.describe.configure({ mode: 'serial' })

const alice = { email: `sch-alice-${stamp}@test.dev`, password: 'password123' }
const bob = { email: `sch-bob-${stamp}@test.dev`, password: 'password123' }
const vera = { email: `sch-vera-${stamp}@test.dev`, password: 'password123' } // a viewer of Alice's workspace
const eve = { email: `sch-eve-${stamp}@test.dev`, password: 'password123' } // an editor of Alice's workspace
const FLAG = 'sched-banner'

async function newUserPage(browser: Browser, viewport = { width: 1280, height: 900 }) {
  const context = await browser.newContext({ viewport })
  return { context, page: await context.newPage() }
}

async function token(api: APIRequestContext, user: { email: string; password: string }) {
  return (await (await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: user })).json()).access_token as string
}

const authHeader = (t: string) => ({ headers: { Authorization: `Bearer ${t}` } })
const dialog = (page: Page) => page.getByRole('dialog').last()
const tab = (page: Page, env: string) => page.getByRole('tab', { name: env })

async function envIds(api: APIRequestContext, t: string) {
  const me = (await (await api.get(`${API}/me`, authHeader(t))).json()) as { activeWorkspaceId: string; workspaces: { id: string; environments: { id: string; key: string }[] }[] }
  const ws = me.workspaces.find((w) => w.id === me.activeWorkspaceId)!
  return { ws: ws.id, env: Object.fromEntries(ws.environments.map((e) => [e.key, e.id])) as Record<string, string> }
}

/** The first date within a year whose 02:30 does not exist (or 01:30 happens twice) in a zone. */
function nextTransition(zone: string, want: 'gap' | 'ambiguous'): string {
  const hour = want === 'gap' ? '02:30' : '01:30'
  for (let i = 5; i < 360; i++) {
    const d = new Date(Date.now() + i * 86_400_000).toISOString().slice(0, 10)
    const z = zonedToUtc(`${d}T${hour}`, zone)
    if (want === 'gap' ? z.kind === 'gap' : z.kind === 'ok' && z.ambiguous) return `${d}T${hour}`
  }
  throw new Error(`no ${want} found for ${zone}`)
}

const tomorrowAt = (hhmm: string) => `${new Date(Date.now() + 2 * 86_400_000).toISOString().slice(0, 10)}T${hhmm}`

async function openFlag(page: Page, env: string) {
  await page.goto('/console')
  await tab(page, env).click()
  await expect(tab(page, env)).toHaveAttribute('aria-selected', 'true')
  await page.getByRole('button', { name: FLAG, exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Schedule and promote' })).toBeVisible()
}

test('scheduling and promotion in the console', async ({ browser }) => {
  const a = await newUserPage(browser)
  const page = a.page
  const api = await pwRequest.newContext()
  await signUp(page, alice)
  await page.getByRole('button', { name: 'Create your first flag' }).click()
  await dialog(page).getByLabel('Key').fill(FLAG)
  await dialog(page).getByLabel('Name').fill('Scheduled banner')
  await dialog(page).getByRole('button', { name: 'Create flag' }).click()
  await expect(page.getByRole('button', { name: FLAG, exact: true })).toBeVisible()

  const t = await token(api, alice)
  const ids = await envIds(api, t)
  const rulesBody = {
    enabled: true, fallthroughVariationId: 'off', rollout: { on: 20000, off: 80000 },
    targetingRules: [{ clauses: [{ attribute: 'plan', operator: 'equals', value: 'pro' }], variationId: 'on' }],
  }
  expect((await api.patch(`${API}/environments/${ids.env.dev}/flags/${FLAG}`, { ...authHeader(t), data: rulesBody })).status()).toBe(200)

  // ---- Promotion: dev into staging ------------------------------------------------------
  await openFlag(page, 'staging')
  await page.getByRole('button', { name: 'Promote into staging…' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Promote sched-banner into staging' })).toBeVisible()
  await expect(dialog(page).getByLabel('Copy from')).toHaveValue('dev')
  const rules = dialog(page).getByTestId('promo-part-rules')
  await expect(rules).toContainText('≠ Differs')
  await expect(rules).toContainText('will be copied')
  await expect(rules).toContainText('Now in staging')
  await expect(rules).toContainText('After, from dev')
  // the enabled state differs too, but is not copied unless asked for, and the dialog says why
  await expect(dialog(page).getByTestId('promo-part-enabled')).toContainText('not selected')
  await expect(dialog(page).getByText('Not copied by default: turning a flag on in staging is a separate decision.')).toBeVisible()
  await shot(page, '33-promotion-diff.png', { fullPage: false })

  // someone edits staging after the preview: applying refuses, writes nothing, and offers a fresh preview
  expect((await api.patch(`${API}/environments/${ids.env.staging}/flags/${FLAG}`, { ...authHeader(t), data: { fallthroughVariationId: 'on' } })).status()).toBe(200)
  await dialog(page).getByRole('button', { name: 'Apply to staging' }).click()
  await expect(dialog(page).getByText('Something changed since this preview. Nothing was written.')).toBeVisible()
  const afterConflict = (await (await api.get(`${API}/environments/${ids.env.staging}/flags/${FLAG}`, authHeader(t))).json()) as { config: { targetingRules: unknown[]; fallthroughVariationId: string } }
  expect(afterConflict.config.targetingRules).toHaveLength(0)
  expect(afterConflict.config.fallthroughVariationId).toBe('on') // the concurrent edit stands
  await dialog(page).getByRole('button', { name: 'Preview again' }).click()
  await expect(dialog(page).getByRole('button', { name: 'Apply to staging' })).toBeEnabled()
  await dialog(page).getByRole('button', { name: 'Apply to staging' }).click()
  await expect(page.getByText('Promoted into staging.')).toBeVisible()
  const promoted = (await (await api.get(`${API}/environments/${ids.env.staging}/flags/${FLAG}`, authHeader(t))).json()) as { config: { enabled: boolean; targetingRules: unknown[]; fallthroughVariationId: string; rollout: Record<string, number> } }
  expect(promoted.config.targetingRules).toHaveLength(1)
  expect(promoted.config.fallthroughVariationId).toBe('off')
  expect(promoted.config.rollout.on).toBe(20000)
  expect(promoted.config.enabled).toBe(false) // never copied by default

  // production: applying asks for confirmation first
  await openFlag(page, 'production')
  await page.getByRole('button', { name: 'Promote into production…' }).click()
  await dialog(page).getByLabel('Copy from').selectOption('dev')
  await expect(dialog(page).getByTestId('promo-part-rules')).toContainText('≠ Differs')
  await dialog(page).getByRole('button', { name: 'Apply to production' }).click()
  await expect(page.getByRole('dialog', { name: 'Change sched-banner in production?' })).toBeVisible()
  await page.getByRole('dialog', { name: 'Change sched-banner in production?' }).getByRole('button', { name: 'Cancel' }).click()
  const prodStill = (await (await api.get(`${API}/environments/${ids.env.production}/flags/${FLAG}`, authHeader(t))).json()) as { config: { targetingRules: unknown[] } }
  expect(prodStill.config.targetingRules).toHaveLength(0)
  await page.keyboard.press('Escape')
  await page.keyboard.press('Escape')

  // ---- The schedule dialog: the time zone is always shown, and so is the exact UTC time --------
  await openFlag(page, 'staging')
  await expect(page.getByTestId('no-scheduled')).toBeVisible()
  await page.getByRole('button', { name: 'Schedule a change…' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Schedule a change to sched-banner' })).toBeVisible()
  await dialog(page).getByLabel('Time zone').selectOption('America/New_York')
  // a time that does not exist (clocks jump forward): refused, with the reason
  await dialog(page).getByLabel('Date and time').fill(nextTransition('America/New_York', 'gap'))
  await expect(dialog(page).getByTestId('run-line')).toContainText('does not exist in America/New_York')
  await dialog(page).getByRole('button', { name: 'Schedule', exact: true }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Schedule a change to sched-banner' })).toBeVisible() // still open
  // a time that happens twice (clocks go back): allowed, read as the first one, and said
  await dialog(page).getByLabel('Date and time').fill(nextTransition('America/New_York', 'ambiguous'))
  await expect(dialog(page).getByText(/happens twice in America\/New_York/)).toBeVisible()
  await expect(dialog(page).getByTestId('run-line')).toContainText('This runs at')
  await expect(dialog(page).getByTestId('run-line')).toContainText('UTC.')
  // too soon
  await dialog(page).getByLabel('Time zone').selectOption('UTC')
  await dialog(page).getByLabel('Date and time').fill(new Date(Date.now() - 3600_000).toISOString().slice(0, 16))
  await expect(dialog(page).getByTestId('run-line')).toContainText('at least one minute from now')
  // a good one, entered in another zone: the UTC line is exact
  await dialog(page).getByLabel('Time zone').selectOption('Asia/Tokyo')
  const day = new Date(Date.now() + 2 * 86_400_000).toISOString().slice(0, 10)
  await dialog(page).getByLabel('Date and time').fill(`${day}T15:30`)
  await expect(dialog(page).getByTestId('run-line')).toHaveText(`This runs at ${day} 06:30 UTC.`)
  await shot(page, '34-schedule-dialog.png', { fullPage: false })
  await dialog(page).getByRole('button', { name: 'Schedule', exact: true }).click()
  await expect(page.getByText(`Scheduled: enable in staging at ${day} 06:30 UTC.`)).toBeVisible()
  const upcoming = page.getByRole('list', { name: 'Upcoming' })
  await expect(upcoming.getByRole('listitem')).toHaveCount(1)
  await expect(upcoming).toContainText(`Runs at ${day} 06:30 UTC`)
  await expect(upcoming).toContainText('Pending')
  await expect(upcoming).toContainText(`by ${alice.email}`)

  // a rollout patch
  await page.getByRole('button', { name: 'Schedule a change…' }).click()
  await dialog(page).getByLabel('Change', { exact: true }).selectOption('patch')
  await dialog(page).getByLabel('Time zone').selectOption('UTC')
  await dialog(page).getByLabel('Date and time').fill(tomorrowAt('09:00'))
  await dialog(page).getByLabel('Percent of users who get on').fill('30')
  await dialog(page).getByLabel('Percent of users who get off').fill('60')
  await dialog(page).getByRole('button', { name: 'Schedule', exact: true }).click()
  await expect(dialog(page).getByText(/Rollout percentages must be numbers adding up to exactly 100%/)).toBeVisible()
  await dialog(page).getByLabel('Percent of users who get off').fill('70')
  await dialog(page).getByRole('button', { name: 'Schedule', exact: true }).click()
  await expect(upcoming.getByRole('listitem')).toHaveCount(2)
  await expect(upcoming).toContainText('Change rollout')
  await shot(page, '35-scheduled-list.png', { fullPage: false })

  // cancel one: it moves to the history, with its status in words
  await upcoming.getByRole('button', { name: /^Cancel the change rollout/i }).click()
  await page.getByRole('dialog', { name: 'Cancel this scheduled change?' }).getByRole('button', { name: 'Cancel the change' }).click()
  await expect(page.getByText('Scheduled change cancelled.')).toBeVisible()
  await expect(upcoming.getByRole('listitem')).toHaveCount(1)
  await expect(page.getByRole('list', { name: 'History' })).toContainText('Cancelled')

  // keyboard: the dialog opens from the keyboard and Escape closes the dialog, not the drawer
  await page.getByRole('button', { name: 'Schedule a change…' }).focus()
  await page.keyboard.press('Enter')
  await expect(dialog(page).getByRole('heading', { name: 'Schedule a change to sched-banner' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('heading', { name: 'Schedule and promote' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Schedule a change to sched-banner' })).toHaveCount(0)

  // the audit log names the creator and the workspace-level rows, and the scheduled change is recorded
  await page.keyboard.press('Escape')
  await page.getByRole('link', { name: 'Audit log' }).click()
  await expect(page.getByRole('table').getByText('Change scheduled').first()).toBeVisible()
  await expect(page.getByRole('table').getByText('Scheduled change cancelled').first()).toBeVisible()

  await api.dispose()
  await a.context.close()
})

test('scheduling: roles, and another workspace sees none of it', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const aliceT = await token(api, alice)
  const ids = await envIds(api, aliceT)
  const pending = ((await (await api.get(`${API}/environments/${ids.env.staging}/scheduled-changes?status=pending`, authHeader(aliceT))).json()) as { scheduledChanges: { id: string }[] }).scheduledChanges
  expect(pending).toHaveLength(1)

  // ---- Bob, in his own workspace ----------------------------------------------------------
  const b = await newUserPage(browser)
  await signUp(b.page, bob)
  const bobT = await token(api, bob)
  const bobIds = await envIds(api, bobT)
  for (const [method, path] of [
    ['get', `/environments/${ids.env.staging}/scheduled-changes`],
    ['get', `/environments/${ids.env.staging}/scheduled-changes/${pending[0].id}`],
    ['post', `/environments/${ids.env.staging}/scheduled-changes/${pending[0].id}/cancel`],
    ['post', `/environments/${ids.env.staging}/flags/${FLAG}/scheduled-changes`],
    ['post', `/environments/${ids.env.staging}/flags/${FLAG}/promotions/preview`],
    ['get', `/environments/${bobIds.env.staging}/scheduled-changes/${pending[0].id}`],
    ['post', `/environments/${bobIds.env.staging}/scheduled-changes/${pending[0].id}/cancel`],
  ] as const) {
    const res = await api[method](`${API}${path}`, { ...authHeader(bobT), ...(method === 'post' ? { data: { idempotencyKey: 'x', kind: 'enable', runAt: new Date(Date.now() + 3600_000).toISOString(), sourceEnvironment: 'dev' } } : {}) })
    expect(res.status(), `${method} ${path}`).toBe(404)
    expect(await res.text()).not.toContain(FLAG)
  }
  expect(((await (await api.get(`${API}/environments/${bobIds.env.staging}/scheduled-changes`, authHeader(bobT))).json()) as { scheduledChanges: unknown[] }).scheduledChanges).toHaveLength(0)
  // in the browser: Bob's flag drawer shows nothing scheduled
  await b.page.getByRole('button', { name: 'Create your first flag' }).click()
  await dialog(b.page).getByLabel('Key').fill('bobs-flag')
  await dialog(b.page).getByLabel('Name').fill('Bobs flag')
  await dialog(b.page).getByRole('button', { name: 'Create flag' }).click()
  await b.page.getByRole('button', { name: 'bobs-flag', exact: true }).click()
  await expect(b.page.getByTestId('no-scheduled')).toBeVisible()
  await expect(b.page.getByText(alice.email)).toHaveCount(0)
  await expect(b.page.getByText('Upcoming')).toHaveCount(0)
  // and a promotion cannot name Alice's environment as its source: only his own are offered
  await b.page.getByRole('button', { name: 'Promote into dev…' }).click()
  const options = await dialog(b.page).getByLabel('Copy from').locator('option').allTextContents()
  expect(options.sort()).toEqual(['production', 'staging'])
  await b.page.keyboard.press('Escape')

  // ---- A viewer and an editor of Alice's workspace -----------------------------------------
  for (const [who, role] of [[vera, 'viewer'], [eve, 'editor']] as const) {
    const u = await newUserPage(browser)
    await signUp(u.page, who)
    const inv = await api.post(`${API}/workspaces/${ids.ws}/invites`, { ...authHeader(aliceT), data: { email: who.email, role } })
    expect(inv.status()).toBe(201)
    await u.page.goto('/console')
    await u.page.getByTestId('pending-invites').getByRole('button', { name: 'Accept' }).click()
    await expect(u.page.getByTestId('workspace-name')).toContainText(`sch-alice-${stamp}`)

    if (role === 'viewer') {
      await openFlag(u.page, 'staging')
      // sees the schedule, read-only
      await expect(u.page.getByRole('list', { name: 'Upcoming' }).getByRole('listitem')).toHaveCount(1)
      await expect(u.page.getByRole('button', { name: 'Schedule a change…' })).toHaveCount(0)
      await expect(u.page.getByRole('button', { name: /^Cancel the/ })).toHaveCount(0)
      await expect(u.page.getByText('Viewers can see scheduled changes but not create or cancel them.')).toBeVisible()
      // may preview a promotion, cannot apply it
      await u.page.getByRole('button', { name: 'Promote into staging…' }).click()
      await expect(dialog(u.page).getByTestId('promo-part-rules')).toBeVisible()
      await expect(dialog(u.page).getByRole('button', { name: 'Apply to staging' })).toBeDisabled()
      await expect(dialog(u.page).getByText('Viewers have read-only access.')).toBeVisible()
      await shot(u.page, '36-schedule-viewer-readonly.png', { fullPage: false })
    } else {
      await openFlag(u.page, 'production')
      await u.page.getByRole('button', { name: 'Schedule a change…' }).click()
      await dialog(u.page).getByLabel('Change', { exact: true }).selectOption('enable')
      await expect(dialog(u.page).getByRole('alert')).toContainText('needs the approver role')
      await expect(dialog(u.page).getByRole('button', { name: 'Schedule', exact: true })).toBeDisabled()
      await shot(u.page, '37-schedule-editor-production-blocked.png', { fullPage: false })
      // a kill is allowed for an editor, in production too
      await dialog(u.page).getByLabel('Change', { exact: true }).selectOption('kill')
      await expect(dialog(u.page).getByRole('alert')).toHaveCount(0)
      await expect(dialog(u.page).getByRole('button', { name: 'Schedule', exact: true })).toBeEnabled()
      await dialog(u.page).getByLabel('Date and time').fill(tomorrowAt('08:00'))
      await dialog(u.page).getByLabel('Time zone').selectOption('UTC')
      await dialog(u.page).getByRole('button', { name: 'Schedule', exact: true }).click()
      await expect(u.page.getByRole('list', { name: 'Upcoming' })).toContainText('Kill')
      // the owner can cancel the editor's change (they could make it); the editor can cancel their own
      await expect(u.page.getByRole('button', { name: /^Cancel the kill/i })).toBeVisible()
    }
    await u.context.close()
  }
  await api.dispose()
  await b.context.close()
})

test('scheduling: phone width', async ({ browser }) => {
  const p = await newUserPage(browser, { width: 375, height: 800 })
  const page = p.page
  const user = { email: `sch-phone-${stamp}@test.dev`, password: 'password123' }
  await signUp(page, user)
  await page.getByRole('button', { name: 'Create your first flag' }).click()
  await dialog(page).getByLabel('Key').fill(FLAG)
  await dialog(page).getByLabel('Name').fill('Phone banner')
  await dialog(page).getByRole('button', { name: 'Create flag' }).click()
  await page.getByRole('button', { name: FLAG, exact: true }).click()
  await page.getByRole('button', { name: 'Schedule a change…' }).click()
  await dialog(page).getByLabel('Change', { exact: true }).selectOption('patch')
  const fits = () => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)
  expect(await fits()).toBe(true)
  await shot(page, '38-schedule-dialog-phone.png', { fullPage: false })
  await dialog(page).getByRole('button', { name: 'Cancel' }).click()
  await page.getByRole('button', { name: 'Promote into dev…' }).click()
  await expect(dialog(page).getByTestId('promo-part-rules')).toBeVisible()
  expect(await fits()).toBe(true)
  await shot(page, '39-promotion-dialog-phone.png', { fullPage: false })
  // an error loading the list is shown, with a retry
  await page.keyboard.press('Escape')
  await page.route('**/scheduled-changes**', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"code":"INTERNAL","message":"internal error"}' }))
  await page.getByRole('button', { name: 'Close' }).click()
  await page.getByRole('button', { name: FLAG, exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'The server hit an error (500)' })).toBeVisible()
  await page.unroute('**/scheduled-changes**')
  await page.getByRole('button', { name: 'Retry' }).last().click()
  await expect(page.getByTestId('no-scheduled')).toBeVisible()
  await p.context.close()
})
