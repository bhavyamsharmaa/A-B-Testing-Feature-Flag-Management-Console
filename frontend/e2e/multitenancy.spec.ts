import { expect, request as pwRequest, test, type Browser, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const API = 'http://localhost:8080'
const SHOTS = resolve(dirname(fileURLToPath(import.meta.url)), '../../docs/screenshots')
mkdirSync(SHOTS, { recursive: true })

// Unique per run: devauth and the local database keep users between runs.
const stamp = Date.now().toString(36)
const alice = { email: `alice-${stamp}@test.dev`, password: 'password123' }
const bob = { email: `bob-${stamp}@test.dev`, password: 'password123' }
const ALICE_FLAG = 'alice-secret-banner'
const BOB_FLAG = 'bobs-private-flag'

// Let toggles and fade-ins finish. An open menu is captured as the viewport
// only: a full-page capture resizes the window, which is not what a user sees.
const shot = async (page: Page, name: string, opts: { fullPage?: boolean } = {}) => {
  await page.waitForTimeout(600)
  await page.screenshot({ path: resolve(SHOTS, name), fullPage: opts.fullPage ?? true })
}

async function signUp(page: Page, user: { email: string; password: string }) {
  await page.goto('/login')
  await page.getByRole('button', { name: 'Create an account' }).click()
  await page.getByLabel('Email').fill(user.email)
  await page.getByLabel('Password').fill(user.password)
  await page.getByRole('button', { name: 'Create account' }).click()
  await expect(page).toHaveURL(/\/console$/)
}

async function createFlag(page: Page, key: string, name: string, opener: () => Promise<void>) {
  await opener()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Key').fill(key)
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByRole('button', { name: 'Create flag' }).click()
  await expect(page.getByRole('button', { name: key, exact: true })).toBeVisible()
}

async function enableFlag(page: Page, key: string) {
  await page.getByRole('switch', { name: `Enable ${key}` }).click()
  await expect(page.getByRole('switch', { name: `Disable ${key}` })).toBeVisible()
}

const workspaceName = (page: Page) => page.getByTestId('workspace-name')

async function newUserPage(browser: Browser) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  return { context, page: await context.newPage() }
}

test('two tenants: isolation, switching, invites, roles, SDK keys', async ({ browser }) => {
  const a = await newUserPage(browser)
  const b = await newUserPage(browser)

  // ---- Alice signs up and lands in her own empty workspace ------------------
  await signUp(a.page, alice)
  await expect(workspaceName(a.page)).toHaveText(`alice-${stamp}'s workspace`)
  await expect(a.page.getByTestId('onboarding')).toBeVisible()
  await expect(a.page.getByRole('heading', { name: 'Create your first flag' })).toBeVisible()
  await shot(a.page, '01-signup-onboarding.png')

  // ---- She creates and enables a flag ---------------------------------------
  await createFlag(a.page, ALICE_FLAG, 'Alice secret banner', () =>
    a.page.getByRole('button', { name: 'Create your first flag' }).click(),
  )
  await enableFlag(a.page, ALICE_FLAG)
  await expect(a.page.getByTestId('onboarding')).toHaveCount(0)
  await shot(a.page, '02-alice-flag.png')

  // ---- Workspace switcher: a second workspace starts empty -------------------
  await a.page.getByTestId('workspace-switcher').getByRole('button').click()
  await expect(a.page.getByRole('menu', { name: 'Your workspaces' })).toBeVisible()
  await shot(a.page, '03-workspace-switcher.png', { fullPage: false })
  await a.page.getByRole('menuitem', { name: /Create workspace/ }).click()
  await a.page.getByRole('dialog').getByLabel('Workspace name').fill('Side project')
  await a.page.getByRole('dialog').getByRole('button', { name: 'Create workspace' }).click()
  await expect(workspaceName(a.page)).toHaveText('Side project')
  await expect(a.page.getByTestId('onboarding')).toBeVisible() // nothing of the other workspace
  await expect(a.page.getByText(ALICE_FLAG)).toHaveCount(0)
  await shot(a.page, '04-second-workspace-empty.png')

  // ...and switching back refetches the first workspace's flags.
  await a.page.getByTestId('workspace-switcher').getByRole('button').click()
  await a.page.getByRole('menuitemradio', { name: new RegExp(`alice-${stamp}'s workspace`) }).click()
  await expect(workspaceName(a.page)).toHaveText(`alice-${stamp}'s workspace`)
  await expect(a.page.getByRole('button', { name: ALICE_FLAG, exact: true })).toBeVisible()

  // ---- Settings: an SDK key for dev, and an invite for Bob -------------------
  await a.page.getByRole('link', { name: 'Settings' }).click()
  await expect(a.page.getByRole('heading', { name: /Settings for/ })).toContainText(`alice-${stamp}'s workspace`)
  await a.page.getByRole('button', { name: 'Create key for dev' }).click()
  const aliceSdkKey = await a.page.getByLabel('New SDK key').inputValue()
  expect(aliceSdkKey).toMatch(/^hsdk_/)

  await a.page.getByLabel('Email', { exact: true }).fill(bob.email)
  await a.page.getByLabel('Role', { exact: true }).selectOption('viewer')
  await a.page.getByRole('button', { name: 'Create invite' }).click()
  const inviteLink = await a.page.getByLabel('Invite link').inputValue()
  expect(inviteLink).toContain('/invite/hinv_')

  // ---- Bob signs up: his workspace is his own; Alice's flag does not exist for him
  await signUp(b.page, bob)
  await expect(workspaceName(b.page)).toHaveText(`bob-${stamp}'s workspace`)
  await expect(b.page.getByTestId('onboarding')).toBeVisible()
  await expect(b.page.getByText(ALICE_FLAG)).toHaveCount(0)
  await b.page.getByTestId('workspace-switcher').getByRole('button').click()
  await expect(b.page.getByRole('menuitemradio')).toHaveCount(1) // only his own workspace
  await b.page.keyboard.press('Escape')
  // He is told about the invite (addressed to his email), but has no access yet.
  await expect(b.page.getByTestId('pending-invites')).toContainText(`alice-${stamp}'s workspace`)
  await shot(b.page, '05-bob-isolated.png')

  // Bob has his own flag; Alice's SDK key can't read it, and Bob can't read hers.
  await createFlag(b.page, BOB_FLAG, 'Bob private flag', () =>
    b.page.getByRole('button', { name: 'Create your first flag' }).click(),
  )
  await enableFlag(b.page, BOB_FLAG)
  const api = await pwRequest.newContext({ baseURL: API })
  const res = await api.post('/evaluate', {
    headers: { 'X-Helios-SDK-Key': aliceSdkKey },
    data: { context: { subjectKey: 'user-1' }, flagKeys: [ALICE_FLAG, BOB_FLAG] },
  })
  expect(res.status()).toBe(200)
  const evals = Object.fromEntries(
    ((await res.json()) as { evaluations: { flagKey: string; value: unknown; reason: string }[] }).evaluations.map((e) => [e.flagKey, e]),
  )
  expect(evals[ALICE_FLAG].value).toBe(true)
  expect(evals[BOB_FLAG].reason).toBe('FLAG_NOT_FOUND')
  expect(evals[BOB_FLAG].value).toBeNull()
  writeFileSync(
    resolve(SHOTS, 'sdk-key-isolation.txt'),
    `Alice's SDK key (hsdk_…, dev) evaluating both flags:\n${JSON.stringify(evals, null, 2)}\n`,
  )

  // Two-user isolation, side by side (Alice's workspace has a flag; Bob's own workspace does not).
  const png = (name: string) => `data:image/png;base64,${readFileSync(resolve(SHOTS, name)).toString('base64')}`
  const composite = await a.context.newPage()
  await composite.setViewportSize({ width: 2640, height: 1000 })
  await composite.setContent(`<body style="margin:0;background:#08080b;color:#ececf1;font:600 22px system-ui;display:flex;gap:24px;padding:24px">
    <figure style="margin:0;width:1280px"><figcaption style="padding:0 0 12px">Alice, in her workspace: has a flag</figcaption><img style="width:1280px;border:1px solid #333;border-radius:12px" src="${png('02-alice-flag.png')}"></figure>
    <figure style="margin:0;width:1280px"><figcaption style="padding:0 0 12px">Bob, in his own workspace: sees none of it</figcaption><img style="width:1280px;border:1px solid #333;border-radius:12px" src="${png('05-bob-isolated.png')}"></figure></body>`)
  await composite.screenshot({ path: resolve(SHOTS, '06-two-user-isolation.png') })
  await composite.close()

  // ---- Bob accepts: now a read-only viewer of Alice's workspace --------------
  await b.page.getByTestId('pending-invites').getByRole('button', { name: 'Accept' }).click()
  await expect(workspaceName(b.page)).toHaveText(`alice-${stamp}'s workspace`)
  await expect(b.page.getByRole('button', { name: ALICE_FLAG, exact: true })).toBeVisible()
  await expect(b.page.getByRole('switch')).toHaveCount(0) // no toggle
  await expect(b.page.getByRole('button', { name: 'Kill' })).toHaveCount(0)
  await expect(b.page.getByRole('button', { name: 'Create flag' })).toHaveCount(0)
  await expect(b.page.getByRole('button', { name: 'View' })).toBeVisible()
  await expect(b.page.getByText('read-only access')).toBeVisible()
  await shot(b.page, '07-bob-viewer-readonly.png')

  await b.page.getByTestId('workspace-switcher').getByRole('button').click()
  await expect(b.page.getByRole('menuitemradio')).toHaveCount(2)
  await shot(b.page, '08-bob-two-workspaces.png', { fullPage: false })
  await b.page.keyboard.press('Escape')

  await b.page.getByRole('link', { name: 'Settings' }).click()
  await expect(b.page.getByTestId('members-table')).toContainText(alice.email)
  await expect(b.page.getByRole('combobox', { name: /Role of/ })).toHaveCount(0) // can't change roles
  await expect(b.page.getByText('Only owners and admins can invite people')).toBeVisible()
  await expect(b.page.getByText('Only owners and admins can see and manage SDK keys')).toBeVisible()
  await shot(b.page, '09-bob-settings-readonly.png')

  // ---- Alice's members page, and promoting Bob to editor ----------------------
  await a.page.reload()
  await expect(a.page.getByTestId('members-table')).toContainText(bob.email)
  await shot(a.page, '10-members-page.png')
  await a.page.getByRole('combobox', { name: `Role of ${bob.email}` }).selectOption('editor')
  await expect(a.page.getByRole('combobox', { name: `Role of ${bob.email}` })).toHaveValue('editor')

  await b.page.goto('/console')
  await expect(b.page.getByRole('button', { name: 'Create flag' })).toBeVisible() // editors can create
  await expect(b.page.getByRole('button', { name: 'Kill' })).toBeVisible()

  // ---- Alice removes Bob: his access to her workspace is gone ----------------
  await a.page.getByRole('row', { name: new RegExp(bob.email) }).getByRole('button', { name: 'Remove' }).click()
  await a.page.getByRole('dialog').getByRole('button', { name: 'Remove member' }).click()
  await expect(a.page.getByTestId('members-table')).not.toContainText(bob.email)
  await b.page.reload()
  await expect(workspaceName(b.page)).toHaveText(`bob-${stamp}'s workspace`)
  await expect(b.page.getByText(ALICE_FLAG)).toHaveCount(0)

  await api.dispose()
  await a.context.close()
  await b.context.close()
})
