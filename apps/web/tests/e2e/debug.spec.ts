import { expect, test } from '@playwright/test'

test('opens deployment information from 我 without horizontal overflow', async ({ page }, info) => {
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  await page.route('**/api/groups', route => route.fulfill({ json: [] }))
  await page.addInitScript(() => {
    localStorage.setItem('jetaime.auth.session', JSON.stringify({
      user: { id: 'debug-user', username: 'reader', nickname: '小林' },
      token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 600_000).toISOString(),
    }))
  })
  const commit = 'abcdef0123456789abcdef0123456789abcdef0123'
  await page.route('**/api/deployment', route => {
    expect(route.request().headers().authorization).toBe(`Bearer ${'x'.repeat(43)}`)
    return route.fulfill({ json: { branch: 'feat/debug', commit, dirty: 'true' } })
  })
  await page.goto('/page/me/')
  await page.getByRole('link', { name: '开发调试', exact: true }).click()
  await expect(page.getByText(commit, { exact: true })).toBeVisible()
  await expect(page.getByText('feat/debug', { exact: true })).toBeVisible()
  await expect(page.getByText('包含未提交修改', { exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('deployment.png'), fullPage: true })
  await page.route('**/api/deployment', route => route.fulfill({ json: { branch: '', commit: '', dirty: '' } }))
  await page.reload()
  await expect(page.getByText('未记录', { exact: true })).toHaveCount(3)
  await page.getByRole('link', { name: '返回我', exact: true }).click()
  await expect(page).toHaveURL(/\/page\/me\/$/)
})
