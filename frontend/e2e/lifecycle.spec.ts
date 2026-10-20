import { expect, request as pwRequest, test, type APIRequestContext, type Browser, type Page } from '@playwright/test'
import { API, AUTH, shot, signUp, stamp } from './helpers'

test.describe.configure({ mode: 'serial' })

const person = (n: string) => ({ email: `lc-${n}-${stamp}@test.dev`, password: 'password123' })
const dialog = (page: Page) => page.getByRole('dialog').last()
const bearer = (token: string) => ({ headers: { Authorization: `Bearer ${token}` } })

async function newUserPage(browser: Browser, viewport = { width: 1280, height: 900 }) {
  const context = await browser.newContext({ viewport })
  return { context, page: await context.newPage() }
}

async function jwt(api: APIRequestContext, user: { email: string; password: string }) {
  return (await (await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: user })).json()).access_token as string
}

async function me(api: APIRequestContext, t: string) {
  return (await (await api.get(`${API}/me`, bearer(t))).json()) as {
    activeWorkspaceId: string
    workspaces: { id: string; name: string; environments: { id: string; key: string }[] }[]
    ownershipOffers: { id: string }[]
    pendingDeletion: { id: string; name: string; purgeAfter: string }[]
  }
}

const bool = [{ id: 'on', value: true }, { id: 'off', value: false }]

/** Adds a person to the workspace with a role (they sign up, are invited by the owner's API session, and accept). */
async function addMember(browser: Browser, api: APIRequestContext, ownerToken: string, wsId: string, who: ReturnType<typeof person>, role: string) {
  const u = await newUserPage(browser)
  await signUp(u.page, who)
  expect((await api.post(`${API}/workspaces/${wsId}/invites`, { ...bearer(ownerToken), data: { email: who.email, role } })).status()).toBe(201)
  await u.page.goto('/console')
  await u.page.getByTestId('pending-invites').getByRole('button', { name: 'Accept' }).click()
  await expect(u.page.getByTestId('workspace-name')).toBeVisible()
  return u
}

test('ownership: offer, accept in the console, roles swap; a cancelled offer disappears', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const alice = person('own-alice'), bob = person('own-bob')
  const a = await newUserPage(browser)
  await signUp(a.page, alice)
  const at = await jwt(api, alice)
  const ws = (await me(api, at)).workspaces[0]
  const b = await addMember(browser, api, at, ws.id, bob, 'admin')

  // the owner's settings: a status line, a chooser of admins only
  await a.page.goto('/console/settings')
  const section = a.page.getByTestId('ownership-section')
  await expect(section).toContainText('You are an owner')
  await expect(section.getByLabel('New owner').locator('option')).toHaveText(['Choose an admin…', bob.email])
  await shot(a.page, '58-ownership-owner.png')
  await section.getByLabel('New owner').selectOption({ label: bob.email })
  await section.getByRole('button', { name: 'Offer ownership' }).click()
  await expect(a.page.getByTestId('ownership-status')).toContainText(`Waiting for ${bob.email} to accept ownership`)
  await expect(a.page.getByTestId('ownership-status')).toContainText('expires')

  // cancel it: the chooser is back, and Bob's banner never offers it
  await a.page.getByRole('button', { name: 'Cancel offer' }).click()
  await expect(a.page.getByTestId('ownership-status')).toContainText('You are an owner')
  await b.page.goto('/console')
  await expect(b.page.getByTestId('ownership-offers')).toHaveCount(0)

  // offer again; Bob sees a banner and accepts
  await a.page.getByLabel('New owner').selectOption({ label: bob.email })
  await a.page.getByRole('button', { name: 'Offer ownership' }).click()
  await expect(a.page.getByTestId('ownership-status')).toContainText('Waiting for')
  await b.page.goto('/console')
  const banner = b.page.getByTestId('ownership-offers')
  await expect(banner).toContainText(`${alice.email} offers you ownership`)
  await expect(banner).toContainText('they become an admin')
  await shot(b.page, '59-ownership-offer-banner.png', { fullPage: false })
  await banner.getByRole('button', { name: 'Accept ownership' }).click()
  await expect(b.page.getByTestId('ownership-offers')).toHaveCount(0)
  await expect(b.page.getByTestId('workspace-switcher')).toContainText('Owner')

  // Alice is an admin now: no Ownership section, no Delete zone, and the API agrees
  await a.page.reload()
  await a.page.goto('/console/settings')
  await expect(a.page.getByTestId('workspace-switcher')).toContainText('Admin')
  await expect(a.page.getByTestId('ownership-section')).toHaveCount(0)
  await expect(a.page.getByTestId('delete-workspace')).toHaveCount(0)
  const members = (await (await api.get(`${API}/workspaces/${ws.id}/members`, bearer(at))).json()) as { members: { email: string; role: string }[] }
  expect(Object.fromEntries(members.members.map((m) => [m.email, m.role]))).toEqual({ [bob.email]: 'owner', [alice.email]: 'admin' })
  // Bob, the only owner, cannot leave; Alice, no longer an owner, can
  const bt = await jwt(api, bob)
  const bobId = (await (await api.get(`${API}/me`, bearer(bt))).json() as { id: string }).id
  const aliceId = (await (await api.get(`${API}/me`, bearer(at))).json() as { id: string }).id
  const lastOwner = await api.delete(`${API}/workspaces/${ws.id}/members/${bobId}`, bearer(bt))
  expect(lastOwner.status()).toBe(409)
  expect(await lastOwner.text()).toContain('LAST_OWNER')
  expect((await api.delete(`${API}/workspaces/${ws.id}/members/${aliceId}`, bearer(at))).status()).toBe(204)
  await api.dispose()
  for (const c of [a, b]) await c.context.close()
})

test('deleting a workspace: type the name, it vanishes at once, an owner restores it from the menu', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const owner = person('del-owner'), editor = person('del-editor')
  const a = await newUserPage(browser)
  await signUp(a.page, owner)
  const ot = await jwt(api, owner)
  const first = (await me(api, ot)).workspaces[0]
  // a second workspace to land on
  expect((await api.post(`${API}/workspaces`, { ...bearer(ot), data: { name: 'Keep me' } })).status()).toBe(201)
  const mine = (await me(api, ot)).workspaces
  const doomed = mine.find((w) => w.name !== 'Keep me')!
  expect(doomed.id).toBe(first.id)
  const dev = doomed.environments.find((e) => e.key === 'dev')!.id
  expect((await api.post(`${API}/environments/${dev}/flags`, { ...bearer(ot), data: { key: 'survives', name: 'Survives', variationType: 'boolean', variations: bool } })).status()).toBe(201)
  const sdk = ((await (await api.post(`${API}/environments/${dev}/sdk-keys`, { ...bearer(ot), data: { name: 'k' } })).json()) as { plaintext: string }).plaintext
  const evalStatus = async () => (await api.post(`${API}/evaluate`, { headers: { 'X-Helios-SDK-Key': sdk }, data: { context: { subjectKey: 'u' }, flagKeys: ['survives'] } })).status()
  expect(await evalStatus()).toBe(200)
  const e = await addMember(browser, api, ot, doomed.id, editor, 'editor')

  // switch to the doomed workspace and delete it
  await a.page.getByTestId('workspace-switcher').getByRole('button').first().click()
  await a.page.getByRole('menuitemradio', { name: new RegExp(doomed.name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')) }).click()
  await expect(a.page.getByTestId('workspace-name')).toHaveText(doomed.name)
  await a.page.getByRole('navigation', { name: 'Console sections' }).getByRole('link', { name: 'Settings' }).click()
  const zone = a.page.getByTestId('delete-workspace')
  await expect(zone).toContainText('14 days')
  await zone.getByRole('button', { name: 'Delete workspace…' }).click()
  const confirm = dialog(a.page).getByRole('button', { name: 'Delete workspace' })
  await expect(confirm).toBeDisabled()
  await dialog(a.page).getByLabel('Workspace name').fill(doomed.name.toUpperCase())
  await expect(confirm).toBeDisabled() // exact match only
  await dialog(a.page).getByLabel('Workspace name').fill(doomed.name)
  await shot(a.page, '60-delete-workspace-dialog.png', { fullPage: false })
  await confirm.click()
  await expect(a.page.getByTestId('workspace-name')).toHaveText('Keep me')

  // gone for everyone, at once
  expect(await evalStatus()).toBe(401)
  expect((await api.get(`${API}/environments/${dev}/flags`, bearer(ot))).status()).toBe(404)
  const et = await jwt(api, editor)
  expect((await api.get(`${API}/environments/${dev}/flags`, bearer(et))).status()).toBe(404)
  await e.page.reload()
  await expect(e.page.getByText('survives')).toHaveCount(0)
  expect((await me(api, et)).workspaces.some((w) => w.id === doomed.id)).toBe(false)
  expect((await me(api, et)).pendingDeletion).toEqual([]) // only the owner is offered a restore

  // the owner finds it in the workspace menu with its date, and restores it
  await a.page.getByTestId('workspace-switcher').getByRole('button').first().click()
  const pending = a.page.getByTestId('pending-deletion')
  await expect(pending).toContainText(doomed.name)
  await expect(pending).toContainText('Deleted for good on')
  await shot(a.page, '61-workspace-scheduled-for-deletion.png', { fullPage: false })
  await pending.getByRole('button', { name: `Restore ${doomed.name}` }).click()
  await expect(a.page.getByTestId('workspace-name')).toHaveText(doomed.name)
  expect(await evalStatus()).toBe(200) // the same SDK key works again
  expect((await api.get(`${API}/environments/${dev}/flags/survives`, bearer(et))).status()).toBe(200)
  await a.page.goto('/console')
  await expect(a.page.getByRole('button', { name: 'survives', exact: true })).toBeVisible()

  // the internal purge endpoint exists in this stack and purges nothing that is not due
  const purge = await api.post(`${API}/internal/workspaces/purge`, { headers: { 'X-Helios-Purge-Secret': process.env.HELIOS_E2E_PURGE_SECRET ?? 'local-purge-secret-0123456789abcdef012345' } })
  expect(purge.status()).toBe(200)
  expect(((await purge.json()) as { purged: number }).purged).toBe(0)
  await api.dispose()
  for (const c of [a, e]) await c.context.close()
})

test('audit export for admins and a flag history with who, when and what', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const owner = person('aud-owner'), viewer = person('aud-viewer')
  const a = await newUserPage(browser)
  await signUp(a.page, owner)
  const ot = await jwt(api, owner)
  const ws = (await me(api, ot)).workspaces[0]
  const dev = ws.environments.find((e) => e.key === 'dev')!.id
  const flag = '=SUM(hostile)'.replace(/[^a-z]/g, '') + '-flag'
  expect((await api.post(`${API}/environments/${dev}/flags`, { ...bearer(ot), data: { key: flag, name: '=HYPERLINK("http://evil.example","x")', variationType: 'boolean', variations: bool } })).status()).toBe(201)
  for (let i = 0; i < 25; i++) {
    expect((await api.patch(`${API}/environments/${dev}/flags/${flag}`, { ...bearer(ot), data: { enabled: i % 2 === 0 } })).status()).toBe(200)
  }
  const v = await addMember(browser, api, ot, ws.id, viewer, 'viewer')

  // the owner exports
  await a.page.goto('/console/audit')
  await a.page.getByRole('button', { name: 'Export…' }).click()
  await expect(a.page.getByTestId('audit-export')).toContainText('Secrets are never included')
  await a.page.getByLabel('From').fill('2000-01-01')
  await expect(a.page.getByTestId('audit-export')).toContainText('at most 90 days')
  await expect(a.page.getByRole('button', { name: 'Download' })).toBeDisabled()
  await a.page.getByLabel('From').fill(new Date(Date.now() - 7 * 86_400_000).toISOString().slice(0, 10))
  await shot(a.page, '62-audit-export.png', { fullPage: false })
  const [download] = await Promise.all([a.page.waitForEvent('download'), a.page.getByRole('button', { name: 'Download' }).click()])
  expect(download.suggestedFilename()).toMatch(/^audit-dev-\d{8}-to-\d{8}\.csv$/)
  const text = (await (await download.createReadStream())!.toArray()).map((c) => c.toString()).join('')
  const rows = text.trim().split('\n')
  expect(rows[0]).toBe('id,createdAt,actorType,actor,action,resourceType,resourceId,severity,scope,diffBefore,diffAfter,actorDetail')
  expect(rows.length).toBeGreaterThan(25)
  expect(/(^|\n)[=+@-]/.test(text)).toBe(false) // no row starts with a formula character
  await expect(a.page.getByRole('status')).toContainText('Downloaded')
  // the export is in the audit log
  await a.page.getByRole('button', { name: 'Refresh' }).first().click()
  await expect(a.page.getByRole('table').getByText('Audit log exported').first()).toBeVisible()

  // a viewer has no export
  await v.page.goto('/console/audit')
  await expect(v.page.getByRole('button', { name: 'Export…' })).toHaveCount(0)
  expect((await api.get(`${API}/environments/${dev}/audit-logs/export`, bearer(await jwt(api, viewer)))).status()).toBe(403)

  // the flag's history in the drawer, for the viewer too
  await v.page.goto('/console')
  await v.page.getByRole('button', { name: flag, exact: true }).click()
  const history = v.page.getByTestId('flag-history')
  await expect(history.getByTestId('history-entry')).toHaveCount(20) // a page
  const first = history.getByTestId('history-entry').first()
  await expect(first).toContainText('Configuration changed')
  await expect(first).toContainText(owner.email)
  await expect(first).toContainText('Person')
  await expect(first.locator('time')).toHaveText(/(UTC|GMT[+-]\d{1,2}(:\d{2})?|\b[A-Z]{3,5})$/) // the zone is shown (not just AM/PM)
  await expect(first.locator('time')).toHaveAttribute('title', /UTC$/)
  await expect(first).toContainText('Enabled')
  await expect(first).toContainText('Before')
  await expect(first).toContainText('After')
  await shot(v.page, '63-flag-history.png', { fullPage: false })
  await history.getByRole('button', { name: 'Load older changes' }).click()
  await expect(history.getByTestId('history-entry')).toHaveCount(26) // 25 edits + the creation
  await expect(history.getByRole('button', { name: 'Load older changes' })).toHaveCount(0)
  await expect(history.getByRole('button', { name: /restore|revert/i })).toHaveCount(0) // read only

  await api.dispose()
  for (const c of [a, v]) await c.context.close()
})

test('experiment control: chosen at creation, shown, changeable until the first exposure', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const owner = person('exp-owner')
  const a = await newUserPage(browser)
  await signUp(a.page, owner)
  const ot = await jwt(api, owner)
  const ws = (await me(api, ot)).workspaces[0]
  const dev = ws.environments.find((e) => e.key === 'dev')!.id
  const env = `${API}/environments/${dev}`
  expect((await api.post(`${env}/flags`, { ...bearer(ot), data: { key: 'colour', name: 'Colour', variationType: 'string', variations: [{ id: 'red', value: 'red' }, { id: 'green', value: 'green' }, { id: 'blue', value: 'blue' }] } })).status()).toBe(201)
  expect((await api.patch(`${env}/flags/colour`, { ...bearer(ot), data: { enabled: true, rollout: { red: 34000, green: 33000, blue: 33000 } } })).status()).toBe(200)

  await a.page.goto('/console/experiments')
  await a.page.getByRole('button', { name: 'Create experiment' }).click()
  await dialog(a.page).getByRole('button', { name: 'Create experiment' }).click() // empty
  await expect(a.page.getByTestId('experiment-problems')).toContainText('Choose a flag.')
  await dialog(a.page).getByLabel('Flag').selectOption('colour')
  await expect(dialog(a.page).getByLabel('Control').locator('option')).toHaveText(['red (the flag’s first variation)', 'green', 'blue'])
  await expect(dialog(a.page).getByLabel('Control')).toHaveValue('red')
  await dialog(a.page).getByLabel('Control').selectOption('green')
  await dialog(a.page).getByLabel('Key', { exact: true }).fill('colour-test')
  await dialog(a.page).getByLabel('Name', { exact: true }).fill('Colour test')
  await dialog(a.page).getByLabel('Metric 1 event name').fill('purchase')
  await shot(a.page, '64-create-experiment.png', { fullPage: false })
  await dialog(a.page).getByRole('button', { name: 'Create experiment' }).click()
  const card = a.page.getByRole('article', { name: 'Experiment colour-test' })
  await expect(card.getByTestId('control-line')).toContainText('Control: green')
  const apiView = async () => (await (await api.get(`${env}/experiments/colour-test`, bearer(ot))).json()) as { controlVariationId: string; variations: { id: string; isControl: boolean }[] }
  expect((await apiView()).controlVariationId).toBe('green')

  // change it while there are no exposures
  await card.getByLabel('Change control').selectOption('blue')
  await card.getByRole('button', { name: 'Save' }).click()
  await expect(card.getByTestId('control-line')).toContainText('Control: blue')
  expect((await apiView()).variations.filter((v) => v.isControl).map((v) => v.id)).toEqual(['blue'])

  // start it and record an exposure: now it is fixed
  expect((await api.post(`${env}/experiments/colour-test/start`, bearer(ot))).status()).toBe(200)
  const sdk = ((await (await api.post(`${env}/sdk-keys`, { ...bearer(ot), data: { name: 'exp' } })).json()) as { plaintext: string }).plaintext
  const id = ((await apiView()) as unknown as { id: string }).id
  const tr = await api.post(`${API}/track`, { headers: { 'X-Helios-SDK-Key': sdk }, data: { events: [{ id: 'x1', name: '$exposure', subjectKey: 'u1', experimentId: id, variationId: 'red' }] } })
  expect(tr.status(), await tr.text()).toBe(200)
  const frozen = await api.patch(`${env}/experiments/colour-test`, { ...bearer(ot), data: { controlVariationId: 'red' } })
  expect(frozen.status()).toBe(409)
  expect(await frozen.text()).toContain('CONTROL_FROZEN')
  await a.page.reload()
  await expect(a.page.getByRole('article', { name: 'Experiment colour-test' }).getByTestId('control-picker')).toHaveCount(0)
  await expect(a.page.getByRole('article', { name: 'Experiment colour-test' })).toContainText('The control is fixed: this experiment has exposures')

  // the results name the designated control, in text
  await a.page.getByTestId('open-results').click()
  await expect(a.page.getByTestId('row-blue')).toContainText('Control')
  await expect(a.page.getByTestId('row-red')).not.toContainText('Control')
  await shot(a.page, '65-experiment-control-results.png')
  await api.dispose()
  await a.context.close()
})
