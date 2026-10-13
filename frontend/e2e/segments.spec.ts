import { expect, request as pwRequest, test, type APIRequestContext, type Browser, type Page } from '@playwright/test'
import { API, AUTH, shot, signUp, stamp } from './helpers'

test.describe.configure({ mode: 'serial' })

const alice = { email: `seg-alice-${stamp}@test.dev`, password: 'password123' }
const bob = { email: `seg-bob-${stamp}@test.dev`, password: 'password123' }
const dave = { email: `seg-dave-${stamp}@test.dev`, password: 'password123' }
const carol = { email: `seg-carol-${stamp}@test.dev`, password: 'password123' }
const FLAG = 'seg-gated-banner'
const SEGMENT = 'beta-testers'

async function newUserPage(browser: Browser, viewport = { width: 1280, height: 900 }) {
  const context = await browser.newContext({ viewport })
  return { context, page: await context.newPage() }
}

async function token(api: APIRequestContext, user: { email: string; password: string }) {
  return (await (await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: user })).json()).access_token as string
}

async function evaluate(api: APIRequestContext, sdkKey: string, subject: string, attributes: Record<string, unknown> = {}) {
  const res = await api.post(`${API}/evaluate`, {
    headers: { 'X-Helios-SDK-Key': sdkKey },
    data: { context: { subjectKey: subject, attributes }, flagKeys: [FLAG] },
  })
  expect(res.status()).toBe(200)
  const e = (await res.json()).evaluations[0] as { variationId: string | null; reason: string }
  return e
}

const dialog = (page: Page) => page.getByRole('dialog')
const noHorizontalScroll = async (page: Page) =>
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)

test('segments: create, bulk members, use in a flag rule, new operators, delete with a usage warning', async ({ browser }) => {
  const a = await newUserPage(browser)
  const page = a.page
  const api = await pwRequest.newContext()

  // ---- Alice: a flag, and the empty segments page --------------------------------
  await signUp(page, alice)
  await page.getByRole('button', { name: 'Create your first flag' }).click()
  await dialog(page).getByLabel('Key').fill(FLAG)
  await dialog(page).getByLabel('Name').fill('Gated banner')
  await dialog(page).getByRole('button', { name: 'Create flag' }).click()
  await expect(page.getByRole('button', { name: FLAG, exact: true })).toBeVisible()
  await page.getByRole('switch', { name: `Enable ${FLAG}` }).click()
  await expect(page.getByRole('switch', { name: `Disable ${FLAG}` })).toBeVisible()

  await page.getByRole('link', { name: 'Segments' }).click()
  await expect(page).toHaveURL(/\/console\/segments$/)
  await expect(page.getByTestId('segments-empty')).toBeVisible()
  await shot(page, '21-segments-empty.png')

  // ---- Keyboard: the button opens the dialog, Escape closes it ---------------------
  await page.getByRole('button', { name: 'New segment' }).focus()
  await page.keyboard.press('Enter')
  await expect(dialog(page).getByRole('heading', { name: 'New segment' })).toBeVisible()
  await expect(dialog(page).getByLabel('Key', { exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(dialog(page)).toHaveCount(0)

  // ---- Create: validation first, then a segment with rules and pasted members --------
  await page.getByRole('button', { name: 'New segment' }).click()
  await dialog(page).getByRole('button', { name: 'Create segment' }).click()
  await expect(dialog(page).getByText('Name is required.').first()).toBeVisible()
  await dialog(page).getByLabel('Key', { exact: true }).fill(SEGMENT)
  await dialog(page).getByLabel('Name', { exact: true }).fill('Beta testers')
  await dialog(page).getByLabel('Description').fill('People trying features early')
  await dialog(page).getByRole('button', { name: '+ Add rule (AND)' }).click()
  await dialog(page).getByLabel('Attribute name').fill('plan')
  await dialog(page).getByLabel('Text value').fill('gold')
  // the segment operators are not offered inside a segment
  await expect(dialog(page).getByLabel('Operator').locator('option', { hasText: 'is in segment' })).toHaveCount(0)

  const paste = dialog(page).getByLabel('Add member keys')
  await paste.fill('user-1\nuser-2\n\n  user-2, user-3\tuser-4\n')
  await expect(dialog(page).getByText('4 keys found, 1 repeated.')).toBeVisible()
  await dialog(page).getByRole('button', { name: 'Add keys' }).click()
  await expect(dialog(page).getByText('Added 4 members. 1 duplicate ignored.')).toBeVisible()
  await expect(dialog(page).getByTestId('member-count')).toContainText('4 of 10,000 members')
  await shot(page, '22-segment-editor.png', { fullPage: false })
  await dialog(page).getByRole('button', { name: 'Create segment' }).click()
  await expect(dialog(page)).toHaveCount(0)
  await expect(page.getByText('Segment Beta testers saved.')).toBeVisible()
  const card = page.getByTestId(`segment-${SEGMENT}`)
  await expect(card).toContainText('4 explicit members')
  await expect(card).toContainText('1 rule (all must match)')

  // a second segment with the same key is refused, with a clear message
  await page.getByRole('button', { name: 'New segment' }).click()
  await dialog(page).getByLabel('Key', { exact: true }).fill(SEGMENT)
  await dialog(page).getByLabel('Name', { exact: true }).fill('Again')
  await dialog(page).getByRole('button', { name: 'Create segment' }).click()
  await expect(dialog(page).getByText('A segment with this key already exists in this workspace.')).toBeVisible()
  await dialog(page).getByRole('button', { name: 'Cancel' }).click()
  await dialog(page).getByRole('button', { name: 'Discard' }).click() // it had unsaved input
  await expect(dialog(page)).toHaveCount(0)

  // ---- Bulk members: limits, de-duplication and clear errors -------------------------
  await card.getByRole('button', { name: `Edit ${SEGMENT}` }).click()
  await expect(dialog(page).getByTestId('member-count')).toContainText('4 of 10,000 members')
  const area = dialog(page).getByLabel('Add member keys')
  await area.fill(`${'z'.repeat(300)}\nuser-5`)
  await dialog(page).getByRole('button', { name: 'Add keys' }).click()
  await expect(dialog(page).getByRole('alert').filter({ hasText: 'could not be used, so nothing was added' })).toContainText('longer than 256')
  await expect(dialog(page).getByTestId('member-count')).toContainText('4 of 10,000 members')
  // the pasted text is still there to fix
  await expect(area).toHaveValue(/user-5/)
  // A few hundred members are enough to exercise the editor; the 10,000 limit is covered by the unit spec
  // (mergeMembers) and by the Go tests (a save at the maximum succeeds, one over is refused).
  const bulk = Array.from({ length: 296 }, (_, i) => `bulk-${String(i).padStart(5, '0')}`)
  await area.fill([...bulk, ...bulk.slice(0, 50), 'user-1'].join('\n')) // repeats and an existing member are ignored
  await dialog(page).getByRole('button', { name: 'Add keys' }).click()
  await expect(dialog(page).getByTestId('member-count')).toContainText('300 of 10,000 members')
  // only a screenful is drawn; the filter finds the rest
  expect(await dialog(page).getByRole('list', { name: 'Members' }).getByRole('listitem').count()).toBeLessThanOrEqual(100)
  await dialog(page).getByLabel('Filter members').fill('bulk-00242')
  await expect(dialog(page).getByRole('list', { name: 'Members' }).getByRole('listitem')).toHaveCount(1)
  await dialog(page).getByRole('button', { name: 'Remove member bulk-00242' }).click()
  await expect(dialog(page).getByTestId('member-count')).toContainText('299 of 10,000 members')
  await dialog(page).getByRole('button', { name: 'Save changes' }).click()
  await expect(dialog(page)).toHaveCount(0)
  await expect(card).toContainText('299 explicit members')

  // ---- Use it in a flag rule through the targeting editor ------------------------------
  await page.getByRole('link', { name: 'Flags' }).click()
  await page.getByRole('button', { name: FLAG, exact: true }).click()
  await page.getByRole('button', { name: '+ Add rule' }).click()
  const op = page.getByLabel('Operator')
  await op.selectOption({ label: 'is in segment' })
  await expect(page.getByLabel('Attribute name')).toHaveCount(0) // a segment test has no attribute
  await expect(page.getByRole('button', { name: 'NOT', exact: true })).toHaveCount(0) // "is not in segment" is its own operator
  await page.getByRole('button', { name: 'Save rules' }).click()
  await expect(page.getByText('Choose a segment.')).toBeVisible()
  await page.getByLabel('Segment', { exact: true }).selectOption(SEGMENT)
  await page.getByLabel('Serve').selectOption({ value: 'off' })
  await page.getByRole('button', { name: 'Save rules' }).click()
  await expect(page.getByText('Targeting rules saved (1 rule).')).toBeVisible()
  await shot(page, '23-flag-rule-segment-picker.png', { fullPage: false })

  // An SDK key, then the real /evaluate: members and rule matches are served "off", everyone else "on" (the fallthrough).
  await page.keyboard.press('Escape')
  await page.getByRole('link', { name: 'Settings' }).click()
  await page.getByRole('button', { name: 'Create key for dev' }).click()
  const sdkKey = await page.getByLabel('New SDK key').inputValue()
  expect((await evaluate(api, sdkKey, 'user-1')).variationId).toBe('off') // explicit member
  expect((await evaluate(api, sdkKey, 'bulk-00007')).variationId).toBe('off') // one of the bulk members
  expect((await evaluate(api, sdkKey, 'bulk-00242')).variationId).toBe('on') // removed in the editor
  expect((await evaluate(api, sdkKey, 'stranger', { plan: 'gold' })).variationId).toBe('off') // matches the segment's rule
  expect((await evaluate(api, sdkKey, 'stranger', { plan: 'free' })).variationId).toBe('on')

  // ---- A rule can reference only segments that exist ---------------------------------------
  await page.getByRole('link', { name: 'Flags' }).click()
  await page.getByRole('button', { name: FLAG, exact: true }).click()

  // ---- The new operators, client- and server-side validation --------------------------------
  await page.getByRole('button', { name: '+ Add rule' }).click()
  const second = page.locator('select[aria-label="Operator"]').nth(1)
  await second.selectOption({ label: 'version greater than or equal' })
  await page.getByLabel('Attribute name').fill('appVersion')
  await page.getByLabel('Version', { exact: true }).fill('v2')
  await page.getByRole('button', { name: 'Save rules' }).click()
  await expect(page.getByText(/Enter a version like 1.4.0/)).toBeVisible()
  await page.getByLabel('Version', { exact: true }).fill('2.0.0')
  await second.selectOption({ label: 'matches regex' })
  await page.getByLabel('Regular expression').fill('(unclosed')
  await page.getByLabel('Serve').nth(1).selectOption({ value: 'off' })
  await page.getByRole('button', { name: 'Save rules' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'invalid regular expression' })).toBeVisible() // the server's answer, shown on that clause
  await page.getByLabel('Regular expression').fill('^pro-.*$')
  await page.getByRole('button', { name: 'NOT', exact: true }).click()
  await expect(page.getByRole('button', { name: '✓ NOT' })).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Save rules' }).click()
  await expect(page.getByText('Targeting rules saved (2 rules).')).toBeVisible()
  await shot(page, '24-flag-rule-new-operators.png', { fullPage: false })
  // rule 2 is "appVersion does NOT match ^pro-.*$" -> off
  expect((await evaluate(api, sdkKey, 'someone', { appVersion: 'pro-annual' })).variationId).toBe('on') // matches the pattern, so NOT is false
  expect((await evaluate(api, sdkKey, 'someone', { appVersion: 'basic' })).variationId).toBe('off')
  expect((await evaluate(api, sdkKey, 'someone', {})).variationId).toBe('on') // a missing attribute never matches, negated or not
  expect((await evaluate(api, sdkKey, 'someone', { appVersion: 42 })).variationId).toBe('on') // nor does a value of the wrong type

  // ---- Delete: the usage warning names the flag, and takes an explicit tick ----------------------
  await page.keyboard.press('Escape')
  await page.getByRole('link', { name: 'Segments' }).click()
  await page.getByTestId(`segment-${SEGMENT}`).getByRole('button', { name: `Delete ${SEGMENT}` }).click()
  await expect(dialog(page).getByRole('list', { name: 'Flags using this segment' })).toContainText(FLAG)
  await expect(dialog(page)).toContainText('match nobody')
  await shot(page, '25-segment-delete-usage-warning.png', { fullPage: false })
  await dialog(page).getByRole('button', { name: 'Delete anyway' }).click()
  await expect(dialog(page).getByText('Tick the box to confirm')).toBeVisible()
  await dialog(page).getByLabel('I understand, delete it anyway').check()
  await dialog(page).getByRole('button', { name: 'Delete anyway' }).click()
  await expect(page.getByText('Segment Beta testers deleted.')).toBeVisible()
  await expect(page.getByTestId('segments-empty')).toBeVisible()
  // the rule is stale now and matches nobody: everyone, members included, falls through
  expect((await evaluate(api, sdkKey, 'user-1')).variationId).toBe('on')
  // and the flag editor shows the dangling reference instead of hiding it
  await page.getByRole('link', { name: 'Flags' }).click()
  await page.getByRole('button', { name: FLAG, exact: true }).click()
  await expect(page.getByLabel('Segment', { exact: true })).toHaveValue(SEGMENT)
  await expect(page.getByLabel('Segment', { exact: true }).locator('option', { hasText: `${SEGMENT} (does not exist)` })).toHaveCount(1)
  await expect(page.getByText('This segment no longer exists, so this rule matches nobody.')).toBeVisible()
  // saving is refused until the rule is fixed
  await page.getByLabel('Serve').first().selectOption({ value: 'on' })
  await page.getByRole('button', { name: 'Save rules' }).click()
  await expect(page.getByText(`Segment "${SEGMENT}" does not exist in this workspace.`).first()).toBeVisible()
  await shot(page, '31-flag-rule-deleted-segment.png', { fullPage: false })

  await api.dispose()
  await a.context.close()
})

test('segments: another workspace sees none of it, in the browser and through the API; viewers read only', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const aliceToken = await token(api, alice)
  const auth = (t: string) => ({ headers: { Authorization: `Bearer ${t}` } })
  const aliceWs = ((await (await api.get(`${API}/me`, auth(aliceToken))).json()) as { activeWorkspaceId: string }).activeWorkspaceId

  // Alice makes a segment worth stealing.
  const made = await api.post(`${API}/workspaces/${aliceWs}/segments`, {
    ...auth(aliceToken),
    data: { key: 'acme-secret-audience', name: 'Acme secret audience', members: ['acme-secret-member'], rules: [{ attribute: 'plan', operator: 'equals', value: 'acme-secret-plan' }] },
  })
  expect(made.status()).toBe(201)

  // ---- Bob, in his own workspace --------------------------------------------------------------
  const b = await newUserPage(browser)
  await signUp(b.page, bob)
  await b.page.getByRole('link', { name: 'Segments' }).click()
  await expect(b.page.getByTestId('segments-empty')).toBeVisible()
  await expect(b.page.getByText('acme-secret')).toHaveCount(0)
  await expect(b.page.getByText('Acme secret audience')).toHaveCount(0)
  await shot(b.page, '26-segments-other-workspace-empty.png')

  // his segment picker offers only his own segments (none) and says so
  await b.page.getByRole('link', { name: 'Flags' }).click()
  await b.page.getByRole('button', { name: 'Create your first flag' }).click()
  await dialog(b.page).getByLabel('Key').fill('bobs-flag')
  await dialog(b.page).getByLabel('Name').fill('Bobs flag')
  await dialog(b.page).getByRole('button', { name: 'Create flag' }).click()
  await b.page.getByRole('button', { name: 'bobs-flag', exact: true }).click()
  await b.page.getByRole('button', { name: '+ Add rule' }).click()
  await b.page.getByLabel('Operator').selectOption({ label: 'is in segment' })
  await expect(b.page.getByText('No segments yet.')).toBeVisible()
  await expect(b.page.getByLabel('Segment', { exact: true }).locator('option')).toHaveCount(1) // only "Choose a segment…"
  await b.page.keyboard.press('Escape')

  // ---- Through the API: Alice's workspace id, Bob's token -----------------------------------------
  const bobToken = await token(api, bob)
  const bobWs = ((await (await api.get(`${API}/me`, auth(bobToken))).json()) as { activeWorkspaceId: string }).activeWorkspaceId
  for (const [method, path, data] of [
    ['get', `/workspaces/${aliceWs}/segments`, undefined],
    ['get', `/workspaces/${aliceWs}/segments/acme-secret-audience`, undefined],
    ['get', `/workspaces/${aliceWs}/segments/acme-secret-audience/usage`, undefined],
    ['post', `/workspaces/${aliceWs}/segments`, { key: 'x', name: 'x' }],
    ['patch', `/workspaces/${aliceWs}/segments/acme-secret-audience`, { name: 'mine' }],
    ['delete', `/workspaces/${aliceWs}/segments/acme-secret-audience?confirm=true`, undefined],
    ['get', `/workspaces/${bobWs}/segments/acme-secret-audience`, undefined],
    ['patch', `/workspaces/${bobWs}/segments/acme-secret-audience`, { name: 'mine' }],
    ['delete', `/workspaces/${bobWs}/segments/acme-secret-audience`, undefined],
  ] as const) {
    const res = await api[method](`${API}${path}`, { ...auth(bobToken), ...(data ? { data } : {}) })
    expect(res.status(), `${method} ${path}`).toBe(404)
    // The answer repeats the key the caller typed (as for flags), so compare it with the answer for a key that
    // never existed: if the two differ in anything but the key, Bob could tell Alice's segment exists.
    const body = (await res.text()).replaceAll('acme-secret-audience', 'KEY')
    expect(body).not.toContain('acme-secret')
    const control = await api[method](`${API}${path.replace('acme-secret-audience', 'never-existed')}`, { ...auth(bobToken), ...(data ? { data } : {}) })
    expect(control.status()).toBe(404)
    expect((await control.text()).replaceAll('never-existed', 'KEY')).toBe(body)
  }
  const ownList = await api.get(`${API}/workspaces/${bobWs}/segments`, auth(bobToken))
  expect(((await ownList.json()) as { segments: unknown[] }).segments).toHaveLength(0)
  // ...and a rule in Bob's flag naming Alice's segment is refused
  const bobEnv = ((await (await api.get(`${API}/me`, auth(bobToken))).json()) as { workspaces: { id: string; environments: { id: string; key: string }[] }[] }).workspaces
    .find((w) => w.id === bobWs)!.environments.find((e) => e.key === 'dev')!.id
  const bad = await api.patch(`${API}/environments/${bobEnv}/flags/bobs-flag`, {
    ...auth(bobToken),
    data: { targetingRules: [{ clauses: [{ operator: 'inSegment', value: 'acme-secret-audience' }], variationId: 'on' }] },
  })
  expect(bad.status()).toBe(400)
  expect(await bad.text()).not.toContain('Acme secret')

  // ---- A viewer of Alice's workspace can read segments and change nothing ----------------------------
  const invite = await api.post(`${API}/workspaces/${aliceWs}/invites`, { ...auth(aliceToken), data: { email: bob.email, role: 'viewer' } })
  expect(invite.status()).toBe(201)
  await b.page.goto('/console')
  await b.page.getByTestId('pending-invites').getByRole('button', { name: 'Accept' }).click()
  await expect(b.page.getByTestId('workspace-name')).toContainText(`seg-alice-${stamp}`)
  await b.page.goto('/console/segments')
  await expect(b.page.getByTestId('segment-acme-secret-audience')).toBeVisible()
  await expect(b.page.getByRole('button', { name: 'New segment' })).toHaveCount(0)
  await expect(b.page.getByRole('button', { name: 'Delete acme-secret-audience' })).toHaveCount(0)
  await b.page.getByRole('button', { name: 'View acme-secret-audience' }).click()
  await expect(dialog(b.page).getByLabel('Name', { exact: true })).toBeDisabled()
  await expect(dialog(b.page).getByLabel('Add member keys')).toHaveCount(0)
  await expect(dialog(b.page).getByRole('button', { name: 'Save changes' })).toHaveCount(0)
  await shot(b.page, '27-segment-viewer-readonly.png', { fullPage: false })
  const denied = await api.patch(`${API}/workspaces/${aliceWs}/segments/acme-secret-audience`, { ...auth(await token(api, bob)), data: { name: 'nope' } })
  expect(denied.status()).toBe(403)

  await api.dispose()
  await b.context.close()
})

test('segments: phone width, loading error and keyboard use', async ({ browser }) => {
  const p = await newUserPage(browser, { width: 375, height: 800 })
  const page = p.page
  await signUp(page, carol) // a fresh, empty workspace, on a phone-sized screen
  await page.goto('/console/segments')
  await expect(page.getByTestId('segments-empty')).toBeVisible()
  await noHorizontalScroll(page)
  await shot(page, '28-segments-phone.png', { fullPage: false })

  await page.getByRole('button', { name: 'New segment' }).click()
  await dialog(page).getByLabel('Key', { exact: true }).fill('phone-seg')
  await dialog(page).getByLabel('Name', { exact: true }).fill('Phone segment')
  await dialog(page).getByLabel('Add member keys').fill(Array.from({ length: 30 }, (_, i) => `a-very-long-member-key-for-the-phone-layout-${i}`).join('\n'))
  await dialog(page).getByRole('button', { name: 'Add keys' }).click()
  await dialog(page).getByRole('button', { name: '+ Add rule (AND)' }).click()
  await noHorizontalScroll(page)
  await shot(page, '29-segment-editor-phone.png', { fullPage: false })
  // Everything in the dialog is reachable by Tab, in order, and Escape asks before throwing edits away.
  await dialog(page).getByLabel('Key', { exact: true }).focus()
  await page.keyboard.press('Tab')
  await expect(dialog(page).getByLabel('Name', { exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Discard your changes?' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'New segment' })).toBeVisible()

  // A failed load shows an error with a retry, not an empty list.
  await dialog(page).getByRole('button', { name: 'Cancel' }).click()
  await page.getByRole('dialog', { name: 'Discard your changes?' }).getByRole('button', { name: 'Discard' }).click()
  await page.route('**/segments', (route) => (route.request().method() === 'GET' ? route.fulfill({ status: 500, body: '{"code":"INTERNAL","message":"internal error"}', contentType: 'application/json' }) : route.continue()))
  await page.getByRole('button', { name: 'Refresh' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'The server hit an error (500)' })).toBeVisible()
  await shot(page, '30-segments-error.png', { fullPage: false })
  await page.unroute('**/segments')
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByTestId('segments-empty')).toBeVisible()
  await p.context.close()
})

test('segments: a segment change is one workspace-level audit entry; environment rows stay in their environment', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const p = await newUserPage(browser)
  const page = p.page
  await signUp(page, dave)
  const t = await token(api, dave)
  const auth = { headers: { Authorization: `Bearer ${t}` } }
  const me = (await (await api.get(`${API}/me`, auth)).json()) as { activeWorkspaceId: string; workspaces: { id: string; environments: { id: string; key: string }[] }[] }
  const ws = me.workspaces.find((w) => w.id === me.activeWorkspaceId)!
  const dev = ws.environments.find((e) => e.key === 'dev')!.id
  expect((await api.post(`${API}/workspaces/${ws.id}/segments`, { ...auth, data: { key: 'audited-seg', name: 'Audited', members: ['u1'] } })).status()).toBe(201)
  expect((await api.post(`${API}/environments/${dev}/flags`, { ...auth, data: { key: 'audited-flag', name: 'Audited flag', variationType: 'boolean', variations: [{ id: 'on', value: true }, { id: 'off', value: false }] } })).status()).toBe(201)
  expect((await api.patch(`${API}/environments/${dev}/flags/audited-flag`, { ...auth, data: { enabled: true } })).status()).toBe(200) // flag.update: dev only

  await page.goto('/console/audit')
  const row = (text: string) => page.getByRole('row').filter({ hasText: text })
  // in dev: the segment entry once, labelled workspace-level; the flag.update row is plain
  await expect(row('Segment created')).toHaveCount(1)
  await expect(row('Segment created').getByTestId('workspace-level')).toHaveText('Workspace-level')
  await expect(row('Flag updated')).toHaveCount(1)
  await expect(row('Flag updated').getByTestId('workspace-level')).toHaveCount(0)
  await expect(page.getByText("Entries marked 'Workspace-level' belong to the whole workspace")).toBeVisible()
  await shot(page, '32-audit-workspace-level.png', { fullPage: false })

  // a filter that should include it does (and one that should not, does not)
  await page.getByLabel('Action').selectOption('segment.create')
  await expect(page.getByRole('row').filter({ hasText: 'Segment created' })).toHaveCount(1)
  await expect(page.getByRole('row').filter({ hasText: 'Flag updated' })).toHaveCount(0)
  await page.getByLabel('Action').selectOption('flag.update')
  await expect(page.getByRole('row').filter({ hasText: 'Segment created' })).toHaveCount(0)
  await page.getByLabel('Action').selectOption('')

  // in staging: still one segment entry, labelled the same; the dev-only flag.update is not there
  await page.getByRole('tab', { name: 'staging' }).click()
  await expect(page.getByRole('tab', { name: 'staging' })).toHaveAttribute('aria-selected', 'true')
  await expect(row('Segment created')).toHaveCount(1)
  await expect(row('Segment created').getByTestId('workspace-level')).toHaveCount(1)
  await expect(row('Flag updated')).toHaveCount(0)

  await api.dispose()
  await p.context.close()
})
