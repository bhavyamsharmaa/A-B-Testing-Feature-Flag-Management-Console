import { expect, request as pwRequest, test } from '@playwright/test'
import { API, AUTH, shot, signUp, stamp } from './helpers'

const carol = { email: `carol-${stamp}@test.dev`, password: 'password123' }

// Data collection end to end: an SDK key reports exposures and events through
// POST /track, and the console shows them arriving (counts, not results).
test('experiments page shows exposures and events arriving', async ({ page }) => {
  await signUp(page, carol)

  // Everything but the SDK calls goes through the console's own session.
  const api = await pwRequest.newContext()
  const token = (
    await (await api.post(`${AUTH}/auth/v1/token?grant_type=password`, { data: carol })).json()
  ).access_token as string
  const as = { headers: { Authorization: `Bearer ${token}` } }
  const me = await (await api.get(`${API}/me`, as)).json()
  const devId = me.workspaces[0].environments.find((e: { key: string }) => e.key === 'dev').id as string
  const env = `${API}/environments/${devId}`

  await api.post(`${env}/flags`, {
    ...as,
    data: { key: 'new-checkout', name: 'New checkout', variationType: 'boolean', variations: [{ id: 'on', value: true }, { id: 'off', value: false }] },
  })
  await api.patch(`${env}/flags/new-checkout`, { ...as, data: { enabled: true } })
  const exp = await (
    await api.post(`${env}/experiments`, {
      ...as,
      data: { flagKey: 'new-checkout', key: 'checkout-test', name: 'Checkout test', hypothesis: 'A one-page checkout converts better', metrics: [{ name: 'Purchase', eventName: 'purchase', type: 'conversion', isPrimary: true }] },
    })
  ).json()
  const key = (await (await api.post(`${env}/sdk-keys`, { ...as, data: { name: 'e2e' } })).json()).plaintext as string
  const sdk = { headers: { 'X-Helios-SDK-Key': key } }

  // Draft: nothing reported by /evaluate yet; the page shows zero.
  await page.goto('/console/experiments')
  const card = page.getByRole('article', { name: 'Experiment checkout-test' })
  await expect(card).toBeVisible()
  await expect(card.getByTestId('exposure-total')).toContainText('0 total')
  await expect(card.getByText('Draft')).toBeVisible()

  await api.post(`${env}/experiments/checkout-test/start`, as)
  const ev = await (await api.post(`${API}/evaluate`, { ...sdk, data: { context: { subjectKey: 'u-1' }, flagKeys: ['new-checkout'] } })).json()
  expect(ev.evaluations[0].experimentId).toBe(exp.id)

  const track = (events: object[]) => api.post(`${API}/track`, { ...sdk, data: { events } })
  const expose = (id: string, subject: string, variation: string) => ({ id, name: '$exposure', subjectKey: subject, experimentId: exp.id, variationId: variation })
  await track([
    expose('x1', 'u-1', 'on'), expose('x2', 'u-2', 'off'), expose('x3', 'u-3', 'on'),
    { id: 'p1', name: 'purchase', subjectKey: 'u-1', experimentId: exp.id, value: 20 },
  ])

  await page.reload()
  await expect(card.getByText('Running')).toBeVisible()
  await expect(card.getByTestId('exposure-total')).toContainText('3 total')
  await expect(card.getByTestId('exposure-on')).toContainText('2')
  await expect(card.getByTestId('exposure-off')).toContainText('1')
  await expect(card.getByTestId('event-total')).toHaveText('1')
  await shot(page, '12-experiment-data-arriving.png')

  // Live: more data arrives while the page is open, with no reload.
  await track([expose('x4', 'u-4', 'off'), expose('x1-again', 'u-1', 'off')]) // u-1 already exposed: the first wins
  await expect(card.getByTestId('exposure-total')).toContainText('4 total', { timeout: 20_000 })
  await expect(card.getByTestId('exposure-off')).toContainText('2')
  await expect(card.getByTestId('exposure-on')).toContainText('2')
})
