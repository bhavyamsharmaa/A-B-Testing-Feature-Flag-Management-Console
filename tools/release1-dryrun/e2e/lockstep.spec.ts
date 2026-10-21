// What a person sees when the console and the backend are NOT on the same release (the deploys and the migration are not in
// lockstep). Run against whichever pair is up: CONSOLE_URL is the console under test. Prints a one-line verdict per scenario and
// asserts nothing about the outcome (the outcome IS the finding); it only fails if the run itself is broken.
//   copy into frontend/e2e/ of a release-1 checkout, then: CONSOLE_URL=http://127.0.0.1:5174 npx playwright test e2e/lockstep.spec.ts
import { test, request as pwRequest, type Page } from '@playwright/test'
import { AUTH, stamp } from './helpers'

const base = process.env.CONSOLE_URL ?? 'http://127.0.0.1:5173'
const PASSWORD = 'rehearsal-pw-1'

async function observe(page: Page, label: string) {
  const bad: string[] = []
  page.on('response', (r) => { if (r.url().includes('8080') && r.status() >= 400) bad.push(`${r.request().method()} ${new URL(r.url()).pathname.replace(/[0-9a-f-]{36}/g, ':id')} -> ${r.status()}`) })
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message.slice(0, 80)))
  await page.waitForTimeout(4000)
  const text = (await page.locator('body').innerText()).replace(/\s+/g, ' ').slice(0, 220)
  const flags = await page.getByText('flag-001').count()
  console.log(`[${label}] url=${page.url().replace(base, '')} flag-001 visible=${flags > 0}\n  page says: "${text}"\n  failed API calls: ${[...new Set(bad)].join('; ') || 'none'}\n  uncaught page errors: ${errors.join('; ') || 'none'}`)
}

test('an existing user (alice) signs in', async ({ page }) => {
  const api = await pwRequest.newContext()
  const em = { email: 'alice@synthetic.invalid', password: PASSWORD }
  let r = await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: em })
  if (!r.ok()) await api.post(`${AUTH}/auth/v1/signup`, { data: em })
  await page.goto(`${base}/login`)
  await page.getByLabel('Email').fill(em.email)
  await page.getByLabel('Password').fill(em.password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await observe(page, `existing user, console ${base}`)
})

test('a brand-new user signs up', async ({ page }) => {
  await page.goto(`${base}/login`)
  await page.getByRole('button', { name: 'Create an account' }).click()
  await page.getByLabel('Email').fill(`lockstep-${stamp}@test.dev`)
  await page.getByLabel('Password').fill('password123')
  await page.getByRole('button', { name: 'Create account' }).click()
  await observe(page, `new user, console ${base}`)
})
