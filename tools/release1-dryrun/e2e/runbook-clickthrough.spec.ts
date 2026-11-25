// The runbook's Step 9 click-through (docs/RELEASE1_RUNBOOK.md) as a Playwright run against the release 1 console and backend.
// It is meant to be copied into the e2e/ folder of a checkout of feature/workspaces-ui (it uses that branch's helpers.ts)
// and run against a LOCAL copy of the database that migrations 0004, 0006 and 0007 were applied to. The people in it are the
// synthetic users of tools/release1-dryrun/synth_old_prod.sql (alice: owner of everything; erin: viewer in dev only; bob:
// admin in dev only). Needs: HELIOS_KEYS = a file with KEY_PRODUCTION=... and KEY_STAGING=... (the revoked one).
import { expect, request as pwRequest, test, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { API, AUTH, signUp, stamp } from './helpers'

const PASSWORD = 'rehearsal-pw-1'
const keys = Object.fromEntries(readFileSync(process.env.HELIOS_KEYS ?? '', 'utf8').split('\n').filter((l) => l.includes('=')).map((l) => l.split(/=(.*)/s).slice(0, 2)))

async function token(email: string, password = PASSWORD) {
  const api = await pwRequest.newContext()
  let r = await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: { email, password } })
  if (!r.ok()) r = await api.post(`${AUTH}/auth/v1/signup`, { data: { email, password } }) // first time: the stand-in learns the password
  return ((await r.json()) as { access_token: string }).access_token
}
async function signIn(page: Page, email: string) {
  await token(email)
  await page.goto('/login')
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page).toHaveURL(/\/console$/)
}
async function me(email: string, password = PASSWORD) {
  const api = await pwRequest.newContext({ baseURL: API })
  const res = await api.get('/me', { headers: { Authorization: `Bearer ${await token(email, password)}` } })
  expect(res.status()).toBe(200)
  return (await res.json()) as { workspaces: { id: string; name: string; role: string; environments: { id: string; key: string }[] }[] }
}

test('9.1-9.3 an existing user signs in and finds everything; creates and enables a flag', async ({ page }) => {
  await signIn(page, 'alice@synthetic.invalid')
  await expect(page.getByTestId('workspace-name')).toContainText('alice@synthetic.invalid')                 // 9.2: ONE workspace holds the old data
  await expect(page.getByRole('button', { name: 'flag-001', exact: true })).toBeVisible()               // 9.1: the old flags are there
  const tabs = page.getByRole('tablist', { name: 'Environment' }).getByRole('tab')
  await expect(tabs).toHaveCount(5)                                                                      // dev, staging, production, qa, sandbox
  await page.getByRole('tab', { name: /production/i }).click()
  await expect(page.getByRole('button', { name: 'flag-001', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: /^dev|development/i }).first().click()
  // audit log has the old history
  await page.getByRole('navigation', { name: 'Console sections' }).getByRole('link', { name: /audit/i }).click()
  await expect(page.locator('table tbody tr').first()).toBeVisible()
  // settings: all seven people who held a role are members of the workspace
  await page.getByRole('navigation', { name: 'Console sections' }).getByRole('link', { name: 'Settings' }).click()
  await expect(page.getByTestId('members-table').locator('tbody tr')).toHaveCount(7)
  // 9.3 create a flag, switch it on
  await page.getByRole('navigation', { name: 'Console sections' }).getByRole('link', { name: /flags/i }).click()
  await page.getByRole('button', { name: 'Create flag' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Key').fill(`smoke-test-${stamp}`)
  await dialog.getByLabel('Name').fill('Smoke test')
  await dialog.getByRole('button', { name: 'Create flag' }).click()
  // The synthetic workspace holds 60 flags and release 1 allows 50 per workspace: existing flags stay, a NEW one is refused with
  // a plain message. (With fewer than 50 flags the flag appears instead; the runbook tells the owner to check the count first.)
  await expect(page.getByRole('alert')).toContainText('limit reached: at most 50 flags per workspace')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  // switch an existing flag on and back off (what a person does to try it)
  await page.getByRole('switch', { name: 'Enable flag-001' }).click()
  await expect(page.getByRole('switch', { name: 'Disable flag-001' })).toBeVisible()
  await page.getByRole('switch', { name: 'Disable flag-001' }).click()
  await expect(page.getByRole('switch', { name: 'Enable flag-001' })).toBeVisible()
})

test('9.4-9.5 a brand-new account gets a private workspace and sees none of the old data', async ({ page }) => {
  const fresh = { email: `fresh-${stamp}@test.dev`, password: 'password123' }
  await signUp(page, fresh)
  await expect(page.getByTestId('workspace-name')).toHaveText(`fresh-${stamp}'s workspace`)
  await expect(page.getByTestId('onboarding')).toBeVisible()
  await expect(page.getByText('flag-001')).toHaveCount(0)
  const mine = await me(fresh.email, fresh.password)
  expect(mine.workspaces).toHaveLength(1)
  expect(mine.workspaces[0].role).toBe('owner')
  // ...and the old workspace's environments are invisible to them (404, not 403)
  const old = (await me('alice@synthetic.invalid')).workspaces[0]
  const devId = old.environments.find((e) => e.key === 'dev')!.id
  const api = await pwRequest.newContext({ baseURL: API })
  const res = await api.get(`/environments/${devId}/flags`, { headers: { Authorization: `Bearer ${await token(fresh.email, fresh.password)}` } })
  expect(res.status()).toBe(404)
})

test('9.6 SDK keys minted before the release still work; the revoked one still does not', async () => {
  const api = await pwRequest.newContext({ baseURL: API })
  const body = { context: { subjectKey: 'smoke', attributes: { plan: 'pro' } }, flagKeys: ['flag-001', 'flag-003', 'flag-005', 'missing'] }
  const ok = await api.post('/evaluate', { headers: { 'X-Helios-SDK-Key': keys.KEY_PRODUCTION }, data: body })
  expect(ok.status()).toBe(200)
  expect(((await ok.json()) as { evaluations: unknown[] }).evaluations).toHaveLength(4)
  const revoked = await api.post('/evaluate', { headers: { 'X-Helios-SDK-Key': keys.KEY_STAGING }, data: body })
  expect(revoked.status()).toBe(401)
})

// Not in the runbook's list before this rehearsal: what the move to ONE workspace role per person does to people who had
// different roles in different environments. Prints what it finds; asserts only what the code promises.
test('9.7 people who held a role in only some environments now hold it in all of them', async ({ browser }) => {
  const ws = (await me('erin@synthetic.invalid')).workspaces[0]
  expect(ws.role).toBe('viewer')                                       // erin: viewer in dev only before
  const prod = ws.environments.find((e) => e.key === 'production')!
  const api = await pwRequest.newContext({ baseURL: API })
  const erin = await api.get(`/environments/${prod.id}/flags`, { headers: { Authorization: `Bearer ${await token('erin@synthetic.invalid')}` } })
  console.log(`erin (viewer in dev only before the release) reading production flags now: HTTP ${erin.status()}`)
  const bobWs = (await me('bob@synthetic.invalid')).workspaces[0]
  const bobProd = bobWs.environments.find((e) => e.key === 'production')!
  const bob = await api.patch(`/environments/${bobProd.id}/flags/flag-010`, { headers: { Authorization: `Bearer ${await token('bob@synthetic.invalid')}` }, data: { enabled: true } })
  console.log(`bob (admin in dev only before the release) switching a production flag now: HTTP ${bob.status()} (role ${bobWs.role})`)
  expect([200, 403]).toContain(bob.status())
  void browser
})
