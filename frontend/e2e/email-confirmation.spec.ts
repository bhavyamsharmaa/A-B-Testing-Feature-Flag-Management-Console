import { expect, test } from '@playwright/test'
import { AUTH, shot, signUp, stamp } from './helpers'

// devauth treats an address containing "+unconfirmed" like a Supabase project
// that lets people sign in before confirming: a session, but a token (and a
// user record) that say the email is not confirmed.
const carol = { email: `carol+unconfirmed-${stamp}@test.dev`, password: 'password123' }

test('an unconfirmed email gets no workspace until it is confirmed', async ({ page, request }) => {
  await signUp(page, carol)

  // The console is replaced by the confirmation screen; there is no workspace to show.
  const screen = page.getByTestId('confirm-email-screen')
  await expect(screen).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Please confirm your email first' })).toBeVisible()
  await expect(screen).toContainText(carol.email)
  await expect(page.getByTestId('workspace-switcher')).toHaveCount(0)
  await expect(page.getByTestId('onboarding')).toHaveCount(0)
  await shot(page, '11-confirm-email.png')

  // Not confirmed yet: "I've confirmed" says so instead of letting them in.
  await page.getByRole('button', { name: "I've confirmed my email" }).click()
  await expect(page.getByRole('alert')).toContainText("can't see the confirmation yet")

  // Resend asks Supabase for another email and then waits out a cooldown.
  await page.getByRole('button', { name: 'Resend email' }).click()
  await expect(page.getByRole('status')).toContainText('We sent a new confirmation link')
  await expect(page.getByRole('button', { name: /Resend email \(\d+s\)/ })).toBeDisabled()

  // They open the link (devauth's stand-in), then continue: now they get a workspace.
  const confirm = await request.get(`${AUTH}/dev/confirm?email=${encodeURIComponent(carol.email)}`)
  expect(confirm.ok()).toBeTruthy()
  await page.getByRole('button', { name: "I've confirmed my email" }).click()
  await expect(page.getByTestId('workspace-name')).toContainText("carol+unconfirmed-" + stamp)
  await expect(page.getByTestId('onboarding')).toBeVisible()
  await expect(page.getByTestId('confirm-email-screen')).toHaveCount(0)
})
