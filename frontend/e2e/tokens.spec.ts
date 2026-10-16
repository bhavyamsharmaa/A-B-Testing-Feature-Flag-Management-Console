import { expect, request as pwRequest, test, type APIRequestContext, type Browser, type Page } from '@playwright/test'
import { API, AUTH, shot, signUp, stamp } from './helpers'

test.describe.configure({ mode: 'serial' })

const alice = { email: `tok-alice-${stamp}@test.dev`, password: 'password123' }
const bob = { email: `tok-bob-${stamp}@test.dev`, password: 'password123' }
const eve = { email: `tok-eve-${stamp}@test.dev`, password: 'password123' } // an editor of Alice's workspace
const FLAG = 'token-flag'

const dialog = (page: Page) => page.getByRole('dialog').last()

async function newUserPage(browser: Browser, viewport = { width: 1280, height: 900 }) {
  const context = await browser.newContext({ viewport })
  return { context, page: await context.newPage() }
}

async function jwt(api: APIRequestContext, user: { email: string; password: string }) {
  return (await (await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: user })).json()).access_token as string
}

async function world(api: APIRequestContext, t: string) {
  const me = (await (await api.get(`${API}/me`, { headers: { Authorization: `Bearer ${t}` } })).json()) as { activeWorkspaceId: string; workspaces: { id: string; environments: { id: string; key: string }[] }[] }
  const ws = me.workspaces.find((w) => w.id === me.activeWorkspaceId)!
  return { ws: ws.id, env: Object.fromEntries(ws.environments.map((e) => [e.key, e.id])) as Record<string, string> }
}

const bearer = (token: string) => ({ headers: { Authorization: `Bearer ${token}` } })

/** Creates a token through the dialog and returns its plaintext. */
async function createViaUi(page: Page, opts: { name: string; role?: string; onlyDev?: boolean; days?: number }): Promise<string> {
  await page.getByRole('button', { name: 'Create token' }).click()
  await dialog(page).getByLabel('Name', { exact: true }).fill(opts.name)
  if (opts.role) await dialog(page).getByLabel('Role', { exact: true }).selectOption(opts.role)
  if (opts.onlyDev) {
    await dialog(page).getByLabel('Only these:').check()
    await dialog(page).getByLabel('dev', { exact: true }).check()
  }
  if (opts.days) await dialog(page).getByLabel('Expires').selectOption(String(opts.days))
  await dialog(page).getByRole('button', { name: 'Create token' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Copy your new token' })).toBeVisible()
  const plaintext = await dialog(page).getByLabel('New API token').inputValue()
  expect(plaintext).toMatch(/^hlt_[0-9a-f]{8}_[A-Za-z0-9_-]{43}$/)
  await dialog(page).getByLabel('I have copied the token and stored it safely').check()
  await dialog(page).getByRole('button', { name: 'Done' }).click()
  return plaintext
}

test('API tokens in the console: create, show once, use, audit, revoke', async ({ browser }) => {
  const a = await newUserPage(browser)
  const page = a.page
  const api = await pwRequest.newContext()
  await signUp(page, alice)
  await page.getByRole('button', { name: 'Create your first flag' }).click()
  await dialog(page).getByLabel('Key').fill(FLAG)
  await dialog(page).getByLabel('Name').fill('Token flag')
  await dialog(page).getByRole('button', { name: 'Create flag' }).click()
  await expect(page.getByRole('button', { name: FLAG, exact: true })).toBeVisible()
  const t = await jwt(api, alice)
  const w = await world(api, t)

  // ---- the empty page ------------------------------------------------------------------------
  await page.getByRole('link', { name: 'API tokens' }).click()
  await expect(page).toHaveURL(/\/console\/tokens$/)
  await expect(page.getByTestId('tokens-empty')).toBeVisible()
  await expect(page.getByText(w.ws)).toBeVisible() // the workspace id a pipeline needs
  await shot(page, '40-tokens-empty.png')

  // ---- the create dialog: what you may give, expiry always, no "never" -------------------------------
  await page.getByRole('button', { name: 'Create token' }).click()
  await expect(dialog(page).getByLabel('Role', { exact: true }).locator('option')).toHaveText(['viewer', 'editor', 'admin']) // an owner: never an owner token
  const expiryOptions = await dialog(page).getByLabel('Expires').locator('option').allTextContents()
  expect(expiryOptions.some((o) => /never/i.test(o.replace('(the longest)', '')))).toBe(false)
  expect(expiryOptions.some((o) => o.includes('366'))).toBe(true)
  await dialog(page).getByRole('button', { name: 'Create token' }).click() // no name yet
  await expect(dialog(page).getByText('Name is required.')).toBeVisible()
  await dialog(page).getByLabel('Name', { exact: true }).fill('ci-deploy')
  await dialog(page).getByLabel('Only these:').check()
  await dialog(page).getByRole('button', { name: 'Create token' }).click()
  await expect(dialog(page).getByText('Choose at least one environment')).toBeVisible()
  await dialog(page).getByLabel('dev', { exact: true }).check()
  await dialog(page).getByLabel('Role', { exact: true }).selectOption('admin')
  await dialog(page).getByLabel('Expires').selectOption('30')
  await expect(dialog(page).getByTestId('token-expiry-line')).toContainText('UTC')
  await shot(page, '41-token-create-dialog.png', { fullPage: false })
  await dialog(page).getByRole('button', { name: 'Create token' }).click()

  // ---- shown once: Escape and the backdrop do not close it, only the checkbox and Done ------------------
  await expect(dialog(page).getByRole('heading', { name: 'Copy your new token' })).toBeVisible()
  await expect(dialog(page).getByRole('alert')).toContainText('only time the token is shown')
  const deployToken = await dialog(page).getByLabel('New API token').inputValue()
  expect(deployToken).toMatch(/^hlt_[0-9a-f]{8}_[A-Za-z0-9_-]{43}$/)
  await shot(page, '42-token-show-once.png', { fullPage: false })
  await page.keyboard.press('Escape')
  await expect(dialog(page).getByRole('heading', { name: 'Copy your new token' })).toBeVisible()
  await page.mouse.click(5, 5)
  await expect(dialog(page).getByRole('heading', { name: 'Copy your new token' })).toBeVisible()
  await expect(dialog(page).getByRole('button', { name: 'Done' })).toBeDisabled()
  await dialog(page).getByLabel('I have copied the token and stored it safely').check()
  await dialog(page).getByRole('button', { name: 'Done' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  // gone for good: the page never shows it again, not in the list, not after a reload
  await page.reload()
  await expect(page.getByText(deployToken)).toHaveCount(0)
  await expect(page.getByText(deployToken.slice(13))).toHaveCount(0)
  const row = page.getByTestId('token-active')
  await expect(row).toHaveCount(1)
  await expect(row).toContainText('ci-deploy')
  await expect(row).toContainText(deployToken.slice(0, 12)) // only the display prefix
  await expect(row).toContainText('admin')
  await expect(row).toContainText('dev') // environments
  await expect(row).toContainText('in 29 days') // honest expiry, with the exact UTC time under it
  await expect(row).toContainText(' UTC')
  await expect(row).toContainText('Never used')
  await expect(row).toContainText(alice.email)
  await expect(row).toContainText('Active')

  // ---- use it ---------------------------------------------------------------------------------------------
  expect((await api.get(`${API}/environments/${w.env.dev}/flags`, bearer(deployToken))).status()).toBe(200)
  expect((await api.get(`${API}/environments/${w.env.staging}/flags`, bearer(deployToken))).status()).toBe(404) // limited to dev
  const denied = await api.get(`${API}/workspaces/${w.ws}/api-tokens`, bearer(deployToken))
  expect(denied.status()).toBe(403) // a token cannot manage tokens
  expect(((await denied.json()) as { code: string }).code).toBe('TOKEN_NOT_ALLOWED')
  expect((await api.patch(`${API}/environments/${w.env.dev}/flags/${FLAG}`, { ...bearer(deployToken), data: { enabled: true } })).status()).toBe(200)
  expect((await api.get(`${API}/environments/${w.env.dev}/flags?access_token=${deployToken}`)).status()).toBe(401) // never in a query string
  await page.getByRole('button', { name: 'Refresh' }).click()
  await expect(page.getByTestId('token-active')).toContainText('Within the last couple of minutes')

  // ---- the audit log names the token, with its creator -------------------------------------------------------
  await page.getByRole('link', { name: 'Audit log' }).click()
  const tokenRow = page.getByTestId('actor-api-token').first()
  await expect(tokenRow).toBeVisible()
  await expect(tokenRow).toContainText('API token')
  await expect(tokenRow).toContainText(`ci-deploy (${deployToken.slice(0, 12)})`)
  await expect(tokenRow).toContainText(`created by ${alice.email}`)
  await shot(page, '43-audit-token-actor.png', { fullPage: false })

  // ---- revoke: confirmation first; then it fails at once, with the generic answer -----------------------------
  await page.getByRole('link', { name: 'API tokens' }).click()
  await page.getByRole('button', { name: /^Revoke ci-deploy/ }).click()
  await expect(page.getByRole('dialog', { name: 'Revoke ci-deploy?' })).toContainText('stops working at once')
  await page.getByRole('dialog', { name: 'Revoke ci-deploy?' }).getByRole('button', { name: 'Cancel' }).click()
  expect((await api.get(`${API}/environments/${w.env.dev}/flags`, bearer(deployToken))).status()).toBe(200)
  await page.getByRole('button', { name: /^Revoke ci-deploy/ }).click()
  await page.getByRole('dialog', { name: 'Revoke ci-deploy?' }).getByRole('button', { name: 'Revoke token' }).click()
  await expect(page.getByText(/revoked\. It stops working at once/)).toBeVisible()
  const refused = await api.get(`${API}/environments/${w.env.dev}/flags`, bearer(deployToken))
  expect(refused.status()).toBe(401)
  expect(await refused.text()).toBe('{"code":"UNAUTHORIZED","message":"invalid or expired token"}\n')
  const revokedRow = page.getByTestId('token-revoked')
  await expect(revokedRow).toContainText('Revoked')
  await expect(revokedRow.getByRole('button', { name: /Revoke/ })).toHaveCount(0)
  await shot(page, '44-tokens-list.png', { fullPage: false })

  await api.dispose()
  await a.context.close()
})

test('API tokens: members below admin see nothing, another workspace sees none of it', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const aliceT = await jwt(api, alice)
  const aw = await world(api, aliceT)
  const created = await api.post(`${API}/workspaces/${aw.ws}/api-tokens`, { ...bearer(aliceT), data: { name: 'alice-secret-token', role: 'admin' } })
  expect(created.status()).toBe(201)
  const { token, plaintext } = (await created.json()) as { token: { id: string }; plaintext: string }

  // ---- Bob, in his own workspace --------------------------------------------------------------------
  const b = await newUserPage(browser)
  await signUp(b.page, bob)
  await b.page.getByRole('link', { name: 'API tokens' }).click()
  await expect(b.page.getByTestId('tokens-empty')).toBeVisible()
  await expect(b.page.getByText('alice-secret-token')).toHaveCount(0)
  await expect(b.page.getByText(plaintext.slice(0, 12))).toHaveCount(0)
  const bobT = await jwt(api, bob)
  const bw = await world(api, bobT)
  // Alice's token cannot read or write anything of Bob's, and Bob cannot list or revoke hers
  const bobFlag = await api.post(`${API}/environments/${bw.env.dev}/flags`, { ...bearer(bobT), data: { key: 'bobs-flag', name: 'Bob', variationType: 'boolean', variations: [{ id: 'on', value: true }, { id: 'off', value: false }] } })
  expect(bobFlag.status()).toBe(201)
  for (const [method, path] of [
    ['get', `/environments/${bw.env.dev}/flags`], ['get', `/environments/${bw.env.dev}/flags/bobs-flag`], ['patch', `/environments/${bw.env.dev}/flags/bobs-flag`],
    ['post', `/environments/${bw.env.dev}/flags/bobs-flag/kill`], ['get', `/environments/${bw.env.dev}/audit-logs`], ['get', `/workspaces/${bw.ws}/segments`], ['get', `/workspaces/${bw.ws}/environments`],
  ] as const) {
    const res = await api[method](`${API}${path}`, { ...bearer(plaintext), ...(method === 'patch' ? { data: { enabled: true } } : {}) })
    expect(res.status(), `${method} ${path}`).toBe(404)
    expect(await res.text()).not.toContain('bobs-flag')
  }
  expect((await api.delete(`${API}/workspaces/${aw.ws}/api-tokens/${token.id}`, bearer(bobT))).status()).toBe(404)
  expect((await api.get(`${API}/workspaces/${aw.ws}/api-tokens`, bearer(bobT))).status()).toBe(404)
  const bobsFlag = (await (await api.get(`${API}/environments/${bw.env.dev}/flags/bobs-flag`, bearer(bobT))).json()) as { config: { enabled: boolean } }
  expect(bobsFlag.config.enabled).toBe(false)

  // ---- an editor of Alice's workspace: the page explains, and offers nothing -----------------------------
  const e = await newUserPage(browser)
  await signUp(e.page, eve)
  expect((await api.post(`${API}/workspaces/${aw.ws}/invites`, { ...bearer(aliceT), data: { email: eve.email, role: 'editor' } })).status()).toBe(201)
  await e.page.goto('/console')
  await e.page.getByTestId('pending-invites').getByRole('button', { name: 'Accept' }).click()
  await expect(e.page.getByTestId('workspace-name')).toContainText(`tok-alice-${stamp}`)
  await e.page.getByRole('link', { name: 'API tokens' }).click()
  await expect(e.page.getByTestId('tokens-forbidden')).toContainText('Only owners and admins')
  await expect(e.page.getByRole('button', { name: 'Create token' })).toHaveCount(0)
  await expect(e.page.getByText('alice-secret-token')).toHaveCount(0)
  await shot(e.page, '45-tokens-editor-readonly.png', { fullPage: false })
  const eveT = await jwt(api, eve)
  expect((await api.get(`${API}/workspaces/${aw.ws}/api-tokens`, bearer(eveT))).status()).toBe(403)
  expect((await api.post(`${API}/workspaces/${aw.ws}/api-tokens`, { ...bearer(eveT), data: { name: 'x', role: 'viewer' } })).status()).toBe(403)

  await api.dispose()
  await b.context.close()
  await e.context.close()
})

test('API tokens: phone width, and a failed load shows an error with a retry', async ({ browser }) => {
  const p = await newUserPage(browser, { width: 375, height: 800 })
  const page = p.page
  await signUp(page, { email: `tok-phone-${stamp}@test.dev`, password: 'password123' })
  await page.goto('/console/tokens')
  await expect(page.getByTestId('tokens-empty')).toBeVisible()
  const fits = () => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)
  expect(await fits()).toBe(true)
  await shot(page, '46-tokens-phone.png', { fullPage: false })
  await createViaUi(page, { name: 'phone-token', role: 'viewer' })
  await expect(page.getByTestId('token-active')).toContainText('phone-token')
  expect(await fits()).toBe(true) // the table scrolls inside its own box
  await page.getByRole('button', { name: 'Create token' }).click()
  expect(await fits()).toBe(true)
  await shot(page, '47-token-create-phone.png', { fullPage: false })
  await page.keyboard.press('Escape')
  await page.route('**/api-tokens', (r) => (r.request().method() === 'GET' ? r.fulfill({ status: 500, contentType: 'application/json', body: '{"code":"INTERNAL","message":"internal error"}' }) : r.continue()))
  await page.getByRole('button', { name: 'Refresh' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'The server hit an error (500)' })).toBeVisible()
  await page.unroute('**/api-tokens')
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByTestId('token-active')).toContainText('phone-token')
  await p.context.close()
})
