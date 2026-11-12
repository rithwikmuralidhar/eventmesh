import { expect, test } from '@playwright/test'

const RECEIVER = process.env.EM_E2E_RECEIVER ?? 'http://127.0.0.1:9000'
const API = process.env.EM_E2E_URL ?? 'http://127.0.0.1:8090'

// The whole product in one flow: register, create an endpoint and key, deliver,
// break the receiver, watch retries end in dead letter, fix it, replay.
test('delivery, dead letter and replay', async ({ page, request }) => {
  const email = `e2e-${Date.now()}@example.com`
  const password = 'e2e-password-123'

  // 1. Register (the app signs the new user in).
  await page.goto('/register')
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Create account' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(page.getByRole('status', { name: /Live updates: Live/ })).toBeVisible()

  // 2. Endpoint pointing at the switchable receiver hook, and an API key.
  await page.getByRole('link', { name: 'Endpoints' }).click()
  await page.getByRole('button', { name: 'New endpoint' }).click()
  await page.getByLabel('Name').fill('e2e hook')
  await page.getByLabel('URL').fill(`${RECEIVER}/hooks/switch/e2e`)
  await page.getByLabel('Rate limit (per second)').fill('100')
  await page.getByRole('button', { name: 'Create endpoint' }).click()
  const secret = (await page.getByTestId('secret').textContent())?.trim()
  expect(secret).toMatch(/^whsec_/)
  // The receiver needs the secret to verify signatures.
  const reg = await request.put(`${RECEIVER}/control/secret/e2e`, { data: { secret } })
  expect(reg.ok()).toBeTruthy()
  expect((await request.put(`${RECEIVER}/control/switch/e2e?mode=ok`)).ok()).toBeTruthy()

  await page.getByRole('link', { name: 'API keys' }).click()
  await page.getByLabel('Key name').fill('e2e key')
  await page.getByRole('button', { name: 'Create key' }).click()
  const apiKey = (await page.getByTestId('api-key').textContent())?.trim() ?? ''
  expect(apiKey).toMatch(/^em_/)

  const submit = async (n: number) => {
    const res = await request.post(`${API}/api/v1/events`, {
      headers: { Authorization: `Bearer ${apiKey}` },
      data: { event_type: 'e2e.test', payload: { n } },
    })
    expect(res.status()).toBe(202)
  }

  // 3-4. First event is delivered, and the page updates without a reload.
  await page.getByRole('link', { name: 'Deliveries' }).click()
  await expect(page.getByText('No deliveries match')).toBeVisible()
  await submit(1)
  const firstRow = page.locator('tbody tr').first()
  await expect(firstRow.getByText('Delivered', { exact: true })).toBeVisible()

  // The receiver verified the signature.
  const seen = await (await request.get(`${RECEIVER}/control/requests`)).json()
  const mine = seen.filter((r: { hook: string }) => r.hook === 'e2e')
  expect(mine.length).toBeGreaterThan(0)
  expect(mine.every((r: { signature_verified: boolean }) => r.signature_verified)).toBe(true)

  // 5-7. Break the receiver; the second event retries, then dead-letters.
  expect((await request.put(`${RECEIVER}/control/switch/e2e?mode=fail`)).ok()).toBeTruthy()
  await submit(2)
  await expect(page.locator('tbody tr', { hasText: 'Dead letter' })).toHaveCount(1)

  // 8. The delivery shows its attempts and a plain-language explanation.
  await page.locator('tbody tr', { hasText: 'Dead letter' }).getByRole('link').click()
  await expect(page.getByTestId('explanation')).toContainText('returned 500 on all')
  const attempts = page.getByRole('list', { name: 'Attempts' }).getByRole('listitem')
  await expect(attempts.first()).toContainText('Attempt 1')
  const failedCount = await attempts.count()
  expect(failedCount).toBeGreaterThanOrEqual(2)

  // 9-10. Fix the receiver and replay; earlier attempts stay listed.
  expect((await request.put(`${RECEIVER}/control/switch/e2e?mode=ok`)).ok()).toBeTruthy()
  await page.getByRole('button', { name: 'Replay' }).click()
  await expect(page.getByText('Delivered', { exact: true }).first()).toBeVisible()
  await expect(page.getByTestId('explanation')).toContainText('accepted the delivery')
  await expect(attempts).toHaveCount(failedCount + 1)
  await expect(attempts.nth(failedCount)).toContainText('replay')
  await expect(attempts.first()).toContainText('Attempt 1')
})
