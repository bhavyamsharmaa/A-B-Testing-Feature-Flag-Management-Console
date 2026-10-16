import { expect, request as pwRequest, test, type APIRequestContext, type Browser, type Page } from '@playwright/test'
import { createHmac } from 'node:crypto'
import { createServer, type IncomingHttpHeaders, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { API, AUTH, shot, signUp, stamp } from './helpers'

test.describe.configure({ mode: 'serial' })

const alice = { email: `wh-alice-${stamp}@test.dev`, password: 'password123' }
const bob = { email: `wh-bob-${stamp}@test.dev`, password: 'password123' }
const eve = { email: `wh-eve-${stamp}@test.dev`, password: 'password123' } // an editor of Alice's workspace
const OUTBOX_SECRET = process.env.HELIOS_E2E_OUTBOX_SECRET ?? 'local-outbox-secret-0123456789abcdef0123'
const dialog = (page: Page) => page.getByRole('dialog').last()
const bearer = (token: string) => ({ headers: { Authorization: `Bearer ${token}` } })

async function newUserPage(browser: Browser, viewport = { width: 1280, height: 900 }) {
  const context = await browser.newContext({ viewport })
  return { context, page: await context.newPage() }
}

async function jwt(api: APIRequestContext, user: { email: string; password: string }) {
  return (await (await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: user })).json()).access_token as string
}

async function world(api: APIRequestContext, t: string) {
  const me = (await (await api.get(`${API}/me`, bearer(t))).json()) as { activeWorkspaceId: string; workspaces: { id: string; environments: { id: string; key: string }[] }[] }
  const ws = me.workspaces.find((w) => w.id === me.activeWorkspaceId)!
  return { ws: ws.id, env: Object.fromEntries(ws.environments.map((e) => [e.key, e.id])) as Record<string, string> }
}

/** Runs the outbox worker once, the way a scheduler job would. */
async function runOutbox(api: APIRequestContext) {
  const res = await api.post(`${API}/internal/outbox/run`, { headers: { 'X-Helios-Outbox-Secret': OUTBOX_SECRET } })
  expect(res.status()).toBe(200)
  return (await res.json()) as { claimed: number; delivered: number; retrying: number; dead: number }
}

interface Got {
  headers: IncomingHttpHeaders
  body: string
}

/** A customer's server: records what it is sent and answers with a status the test controls. */
async function startReceiver() {
  const got: Got[] = []
  const state = { status: 200 }
  const server: Server = createServer((req, res) => {
    let body = ''
    req.on('data', (c) => (body += c))
    req.on('end', () => {
      got.push({ headers: req.headers, body })
      res.statusCode = state.status
      res.end('receiver says hello')
    })
  })
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/hooks/helios`
  return { got, state, url, close: () => new Promise<void>((r) => server.close(() => r())) }
}

/** What a receiver does: verify timestamp.deliveryId.body with HMAC-SHA256 (docs/WEBHOOKS.md). */
function verifies(secret: string, g: Got): boolean {
  const ts = String(g.headers['helios-timestamp'])
  const id = String(g.headers['helios-delivery-id'])
  if (Math.abs(Date.now() / 1000 - Number(ts)) > 300) return false
  const want = createHmac('sha256', secret).update(`${ts}.${id}.${g.body}`).digest('hex')
  return String(g.headers['helios-signature']).split(',').some((p) => p.trim() === `v1=${want}`)
}

test('webhooks: add, show the secret once, test, fail, resend, rotate, disable, delete', async ({ browser }) => {
  const a = await newUserPage(browser)
  const page = a.page
  const api = await pwRequest.newContext()
  const recv = await startReceiver()
  await signUp(page, alice)
  const t = await jwt(api, alice)
  const w = await world(api, t)
  const flag = await api.post(`${API}/environments/${w.env.dev}/flags`, { ...bearer(t), data: { key: 'hooked', name: 'Hooked', variationType: 'boolean', variations: [{ id: 'on', value: true }, { id: 'off', value: false }] } })
  expect(flag.status()).toBe(201)

  // ---- the empty page ------------------------------------------------------------------------
  await page.getByRole('link', { name: 'Webhooks' }).click()
  await expect(page).toHaveURL(/\/console\/webhooks$/)
  await expect(page.getByTestId('webhooks-empty')).toBeVisible()
  await shot(page, '48-webhooks-empty.png')

  // ---- the add dialog: validation, then created ---------------------------------------------------
  await page.getByRole('button', { name: 'Add webhook' }).click()
  await dialog(page).getByRole('button', { name: 'Add webhook' }).click() // no URL yet
  await expect(dialog(page).getByText('URL is required.')).toBeVisible()
  await dialog(page).getByLabel('URL').fill('https://example.com:22/h')
  await dialog(page).getByRole('button', { name: 'Add webhook' }).click()
  await expect(dialog(page).getByRole('alert')).toContainText('443') // the server explains the port rule
  await dialog(page).getByLabel('URL').fill('https://169.254.169.254/latest/meta-data')
  await dialog(page).getByRole('button', { name: 'Add webhook' }).click()
  await expect(dialog(page).getByRole('alert')).toContainText(/link-local|not allowed/i) // metadata address refused, with a reason
  await dialog(page).getByLabel('URL').fill(recv.url)
  await dialog(page).getByLabel('Description').fill('E2E receiver')
  await dialog(page).getByRole('button', { name: 'Clear' }).click()
  await dialog(page).getByRole('button', { name: 'Add webhook' }).click()
  await expect(dialog(page).getByText('Choose at least one event.')).toBeVisible()
  await dialog(page).getByRole('button', { name: 'Select all' }).click()
  await shot(page, '49-webhook-add-dialog.png', { fullPage: false })
  await dialog(page).getByRole('button', { name: 'Add webhook' }).click()

  // ---- shown once ---------------------------------------------------------------------------------------
  await expect(dialog(page).getByRole('heading', { name: 'Copy your signing secret' })).toBeVisible()
  await expect(dialog(page).getByRole('alert')).toContainText('only time the secret is shown')
  const secret = await dialog(page).getByLabel('Signing secret').inputValue()
  expect(secret).toMatch(/^whsec_[A-Za-z0-9_-]{43}$/)
  await shot(page, '50-webhook-secret-once.png', { fullPage: false })
  await page.keyboard.press('Escape')
  await expect(dialog(page).getByRole('heading', { name: 'Copy your signing secret' })).toBeVisible()
  await page.mouse.click(5, 5)
  await expect(dialog(page).getByRole('heading', { name: 'Copy your signing secret' })).toBeVisible()
  await expect(dialog(page).getByRole('button', { name: 'Done' })).toBeDisabled()
  await dialog(page).getByLabel('I have copied the secret and stored it safely').check()
  await dialog(page).getByRole('button', { name: 'Done' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await page.reload()
  await expect(page.getByText(secret)).toHaveCount(0)
  await expect(page.getByText(secret.slice(6))).toHaveCount(0)
  const row = page.getByTestId('webhook-idle')
  await expect(row).toContainText(recv.url)
  await expect(row).toContainText('E2E receiver')
  await expect(row).toContainText(`secret …${secret.slice(-4)}`)
  await expect(row).toContainText('All events')
  await expect(row).toContainText('No deliveries yet')

  // ---- a test ping reaches the receiver, signed ---------------------------------------------------------
  await row.getByRole('button', { name: /Send test/ }).click()
  await expect(page.getByText(/Test delivered to .* \(HTTP 200/)).toBeVisible()
  expect(recv.got).toHaveLength(1)
  expect(recv.got[0].headers['helios-event-type']).toBe('webhook.ping')
  expect(verifies(secret, recv.got[0])).toBe(true)
  expect(verifies(secret.slice(0, -1) + 'x', recv.got[0])).toBe(false)
  await expect(page.getByTestId('webhook-idle')).toContainText('No deliveries yet') // a test ping does not make an endpoint look healthy or sick

  // ---- a real event, a failing receiver, the log, a resend ------------------------------------------------
  recv.state.status = 503
  expect((await api.patch(`${API}/environments/${w.env.dev}/flags/hooked`, { ...bearer(t), data: { enabled: true } })).status()).toBe(200)
  expect((await runOutbox(api)).retrying).toBeGreaterThanOrEqual(1)
  await page.reload()
  await expect(page.getByTestId('webhook-failing')).toContainText('Failing')
  await expect(page.getByTestId('webhook-failing')).toContainText('1 failed in a row')
  await page.getByRole('button', { name: /Deliveries for/ }).click()
  const failedRow = dialog(page).getByTestId('delivery-failed')
  await expect(failedRow).toContainText('Failed, will retry')
  await expect(failedRow).toContainText('503')
  await expect(failedRow).toContainText('the receiver answered 503')
  await expect(failedRow).toContainText('next try')
  await shot(page, '51-webhook-deliveries-failed.png', { fullPage: false })
  const failedHeader = recv.got[recv.got.length - 1].headers['helios-delivery-id']
  recv.state.status = 200
  await failedRow.getByRole('button', { name: /Resend flag.updated/ }).click()
  await expect(dialog(page).getByTestId('delivery-delivered').first()).toContainText('Delivered')
  await expect(dialog(page).getByTestId('delivery-delivered').first().getByRole('button', { name: /Resend/ })).toHaveCount(0) // only a failure can be resent
  const resent = recv.got[recv.got.length - 1]
  expect(resent.headers['helios-delivery-id']).toBe(failedHeader) // same delivery id on the new attempt
  expect(verifies(secret, resent)).toBe(true)
  expect(JSON.parse(resent.body)).toMatchObject({ type: 'flag.updated', version: 1, data: { flag: { key: 'hooked' } } })
  expect(resent.body).not.toContain(alice.email) // no member emails in events
  await dialog(page).getByRole('button', { name: 'Close' }).click()
  await expect(page.getByTestId('webhook-healthy')).toContainText('Healthy')

  // ---- rotate: confirmation, a new secret shown once, both signatures during the grace ----------------------
  await page.getByRole('button', { name: /Rotate secret of/ }).click()
  await dialog(page).getByRole('button', { name: 'Rotate secret' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Your new signing secret' })).toBeVisible()
  const secret2 = await dialog(page).getByLabel('Signing secret').inputValue()
  expect(secret2).not.toBe(secret)
  await dialog(page).getByLabel('I have copied the secret and stored it safely').check()
  await dialog(page).getByRole('button', { name: 'Done' }).click()
  await expect(page.getByTestId('webhook-healthy')).toContainText('old secret still signing')
  const before = recv.got.length
  expect((await api.patch(`${API}/environments/${w.env.dev}/flags/hooked`, { ...bearer(t), data: { enabled: false } })).status()).toBe(200)
  await runOutbox(api)
  expect(recv.got).toHaveLength(before + 1)
  expect(String(recv.got[before].headers['helios-signature']).split(',')).toHaveLength(2)
  expect(verifies(secret, recv.got[before])).toBe(true) // a receiver on the old secret still verifies
  expect(verifies(secret2, recv.got[before])).toBe(true) // and one on the new

  // ---- disable stops queueing; enable resumes ------------------------------------------------------------
  await page.getByRole('button', { name: /^Disable / }).click()
  await expect(page.getByTestId('webhook-disabled')).toContainText('Disabled')
  const n = recv.got.length
  await api.patch(`${API}/environments/${w.env.dev}/flags/hooked`, { ...bearer(t), data: { enabled: true } })
  await runOutbox(api)
  expect(recv.got).toHaveLength(n)
  await page.getByRole('button', { name: /^Enable / }).click()
  await expect(page.getByTestId('webhook-healthy')).toBeVisible()
  await shot(page, '52-webhooks-list.png')

  // ---- audit: the actions are there, the secret is not ----------------------------------------------------
  const audit = await (await api.get(`${API}/environments/${w.env.dev}/audit-logs`, bearer(t))).text()
  expect(audit).toContain('webhook_endpoint.create')
  expect(audit).toContain('webhook_endpoint.rotate_secret')
  expect(audit).not.toContain(secret.slice(6))
  expect(audit).not.toContain(secret2.slice(6))

  // ---- delete asks first ---------------------------------------------------------------------------------------
  await page.getByRole('button', { name: /^Delete / }).click()
  await expect(dialog(page)).toContainText('cannot be undone')
  await dialog(page).getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByTestId('webhook-healthy')).toBeVisible()
  await page.getByRole('button', { name: /^Delete / }).click()
  await dialog(page).getByRole('button', { name: 'Delete webhook' }).click()
  await expect(page.getByTestId('webhooks-empty')).toBeVisible()

  await recv.close()
  await api.dispose()
  await a.context.close()
})

test('webhooks across tenants and roles: B sees nothing of A, an editor is told no, A never gets B events', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const recv = await startReceiver()
  const a = await newUserPage(browser)
  await signUp(a.page, { email: `wh-alice2-${stamp}@test.dev`, password: 'password123' })
  const aliceT = await jwt(api, { email: `wh-alice2-${stamp}@test.dev`, password: 'password123' })
  const aw = await world(api, aliceT)
  const created = await api.post(`${API}/workspaces/${aw.ws}/webhooks`, { ...bearer(aliceT), data: { url: recv.url, description: 'alice-private-hook', eventTypes: ['flag.created', 'flag.updated'] } })
  expect(created.status()).toBe(201)
  const endpointId = ((await created.json()) as { endpoint: { id: string } }).endpoint.id

  // Bob, in a workspace of his own
  const b = await newUserPage(browser)
  await signUp(b.page, bob)
  const bobT = await jwt(api, bob)
  const bw = await world(api, bobT)
  await b.page.getByRole('link', { name: 'Webhooks' }).click()
  await expect(b.page.getByTestId('webhooks-empty')).toBeVisible()
  await expect(b.page.getByText('alice-private-hook')).toHaveCount(0)
  await expect(b.page.getByText(recv.url)).toHaveCount(0)
  for (const [method, path] of [
    ['get', `/workspaces/${aw.ws}/webhooks`], ['patch', `/workspaces/${aw.ws}/webhooks/${endpointId}`], ['delete', `/workspaces/${aw.ws}/webhooks/${endpointId}`],
    ['post', `/workspaces/${aw.ws}/webhooks/${endpointId}/test`], ['post', `/workspaces/${aw.ws}/webhooks/${endpointId}/rotate-secret`], ['get', `/workspaces/${aw.ws}/webhooks/${endpointId}/deliveries`],
  ] as const) {
    const res = await api[method](`${API}${path}`, { ...bearer(bobT), ...(method === 'patch' ? { data: { enabled: false } } : {}) })
    expect(res.status(), `${method} ${path}`).toBe(404)
    expect(await res.text()).not.toContain('alice-private-hook')
  }
  // Bob's flags make no event for Alice's endpoint
  expect((await api.post(`${API}/environments/${bw.env.dev}/flags`, { ...bearer(bobT), data: { key: 'bobs-private-flag', name: 'Bob', variationType: 'boolean', variations: [{ id: 'on', value: true }, { id: 'off', value: false }] } })).status()).toBe(201)
  expect((await api.patch(`${API}/environments/${bw.env.dev}/flags/bobs-private-flag`, { ...bearer(bobT), data: { enabled: true } })).status()).toBe(200)
  await runOutbox(api)
  expect(recv.got.filter((g) => g.body.includes('bobs-private-flag'))).toHaveLength(0)
  expect(recv.got).toHaveLength(0)

  // an editor of Alice's workspace is told, and offered nothing
  const e = await newUserPage(browser)
  await signUp(e.page, eve)
  expect((await api.post(`${API}/workspaces/${aw.ws}/invites`, { ...bearer(aliceT), data: { email: eve.email, role: 'editor' } })).status()).toBe(201)
  await e.page.goto('/console')
  await e.page.getByTestId('pending-invites').getByRole('button', { name: 'Accept' }).click()
  await expect(e.page.getByTestId('workspace-name')).toContainText(`wh-alice2-${stamp}`)
  await e.page.getByRole('link', { name: 'Webhooks' }).click()
  await expect(e.page.getByTestId('webhooks-forbidden')).toContainText('Only owners and admins')
  await expect(e.page.getByRole('button', { name: 'Add webhook' })).toHaveCount(0)
  await expect(e.page.getByText('alice-private-hook')).toHaveCount(0)
  await shot(e.page, '53-webhooks-editor-readonly.png', { fullPage: false })
  const eveT = await jwt(api, eve)
  expect((await api.get(`${API}/workspaces/${aw.ws}/webhooks`, bearer(eveT))).status()).toBe(403)

  await recv.close()
  await api.dispose()
  for (const c of [a, b, e]) await c.context.close()
})

test('invite email: status, resend, and the honest note that the dev sink sends nothing', async ({ browser }) => {
  const api = await pwRequest.newContext()
  const a = await newUserPage(browser)
  const page = a.page
  const owner = { email: `wh-owner-${stamp}@test.dev`, password: 'password123' }
  await signUp(page, owner)
  const t = await jwt(api, owner)
  await page.goto('/console/settings')
  await expect(page.getByTestId('mail-sink-note')).toContainText('not actually sent')

  await page.getByLabel('Email').fill(`wh-invitee-${stamp}@test.dev`)
  await page.getByRole('button', { name: 'Create invite' }).click()
  await expect(page.getByTestId('invite-link')).toBeVisible() // the copy-link fallback is still there
  const status = page.getByTestId('invite-email-status').first()
  await expect(status).toContainText('Email: queued')
  await expect(status).toContainText('not actually sent here')
  await shot(page, '54-invite-email-queued.png')

  await runOutbox(api) // the dev sink "delivers": nothing leaves the machine
  await page.reload()
  await expect(page.getByTestId('invite-email-status').first()).toContainText('Email: sent')
  await expect(page.getByTestId('invite-email-status').first()).toContainText('not actually sent here')

  // resend inside the cooldown is refused, plainly
  await page.getByRole('button', { name: /Resend the email to/ }).click()
  await expect(page.getByRole('alert')).toContainText(/wait/i)
  await shot(page, '55-invite-resend-cooldown.png')

  // phone width
  await page.setViewportSize({ width: 375, height: 800 })
  const fits = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)
  expect(fits).toBe(true)
  await shot(page, '56-invite-email-phone.png', { fullPage: false })

  await page.goto('/console/webhooks')
  await expect(page.getByTestId('webhooks-empty')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
  await shot(page, '57-webhooks-phone.png', { fullPage: false })
  await page.route('**/webhooks', (r) => (r.request().method() === 'GET' ? r.fulfill({ status: 500, contentType: 'application/json', body: '{"code":"INTERNAL","message":"internal error"}' }) : r.continue()))
  await page.getByRole('button', { name: 'Refresh' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'The server hit an error (500)' })).toBeVisible()
  await page.unroute('**/webhooks')
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByTestId('webhooks-empty')).toBeVisible()

  await api.dispose()
  await a.context.close()
})
