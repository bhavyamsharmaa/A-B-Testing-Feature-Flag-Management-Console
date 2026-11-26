import { expect, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const API = 'http://localhost:8080'
export const AUTH = 'http://127.0.0.1:54321'
export const SHOTS = resolve(dirname(fileURLToPath(import.meta.url)), '../../docs/screenshots')
mkdirSync(SHOTS, { recursive: true })

// Unique per run: devauth and the local database keep users between runs.
export const stamp = Date.now().toString(36)

export interface Person {
  email: string
  password: string
}

// Let toggles and fade-ins finish. An open menu is captured as the viewport
// only: a full-page capture resizes the window, which is not what a user sees.
export async function shot(page: Page, name: string, opts: { fullPage?: boolean } = {}) {
  await page.waitForTimeout(600)
  await page.screenshot({ path: resolve(SHOTS, name), fullPage: opts.fullPage ?? true })
}

export async function signUp(page: Page, user: Person) {
  await page.goto('/login')
  await page.getByRole('button', { name: 'Create an account' }).click()
  await page.getByLabel('Email').fill(user.email)
  await page.getByLabel('Password').fill(user.password)
  await page.getByRole('button', { name: 'Create account' }).click()
  await expect(page).toHaveURL(/\/console$/)
}
