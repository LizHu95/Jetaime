import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  await page.route('**/api/groups', route => route.fulfill({ json: [] }))
  await page.addInitScript(() => {
    localStorage.setItem('jetaime.auth.session', JSON.stringify({
      user: { id: 'theme-user', username: 'reader', nickname: '小林' },
      token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 600_000).toISOString(),
    }))
  })
})

test('switches all four palettes, persists across reload and navigation, and keeps 我 selected', async ({ page }, info) => {
  await page.goto('/page/me/')
  await page.getByRole('link', { name: '主题设置', exact: true }).click()
  await expect(page.getByRole('heading', { name: '主题切换' })).toBeVisible()
  await expect(page.getByRole('link', { name: '我', exact: true })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('radio', { name: /静谧灰蓝/ })).toBeChecked()
  await expect(page.locator('.theme-option').filter({ has: page.getByRole('radio', { name: /静谧灰蓝/ }) }).getByText('推荐', { exact: true })).toBeVisible()
  const palettes = [
    { name: '静谧灰蓝', id: 'aurora', background: 'rgb(20, 27, 40)', primary: '#7D94B5', meta: '#141B28' },
    { name: '学术暖金', id: 'academic', background: 'rgb(24, 26, 34)', primary: '#C49848', meta: '#181A22' },
    { name: '冷调青金', id: 'cool', background: 'rgb(20, 27, 36)', primary: '#97A890', meta: '#141B24' },
    { name: '极简单色', id: 'minimal', background: 'rgb(23, 25, 33)', primary: '#AD8C46', meta: '#171921' },
  ]
  for (const palette of palettes) {
    await page.getByRole('radio', { name: new RegExp(palette.name) }).check()
    await expect(page.locator('html')).toHaveAttribute('data-theme', palette.id)
    await expect(page.locator('.main-app')).toHaveCSS('background-color', palette.background)
    expect(await page.locator('html').evaluate(el => getComputedStyle(el).getPropertyValue('--primary').trim())).toBe(palette.primary)
    await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', palette.meta)
    await page.reload()
    await expect(page.getByRole('radio', { name: new RegExp(palette.name) })).toBeChecked()
    await expect(page.locator('html')).toHaveAttribute('data-theme', palette.id)
  }
  await page.getByRole('radio', { name: /静谧灰蓝/ }).check()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('theme-settings.png'), fullPage: true })
  await page.getByRole('radio', { name: /静谧灰蓝/ }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByRole('radio', { name: /学术暖金/ })).toBeChecked()
  await page.getByRole('radio', { name: /静谧灰蓝/ }).check()
  await page.getByRole('link', { name: '聊天', exact: true }).click()
  await expect(page.locator('.chat-shell')).toHaveCSS('background-color', palettes[0].background)
  await page.goto('/page/auth/login/')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'aurora')
})

test('recovers from unknown preferences and synchronizes another tab', async ({ page, context }) => {
  await page.addInitScript(() => { if (!sessionStorage.getItem('theme-seeded')) { localStorage.setItem('jetaime.theme', 'unknown'); sessionStorage.setItem('theme-seeded', 'true') } })
  await page.goto('/page/me/settings/')
  await expect(page.getByRole('radio', { name: /静谧灰蓝/ })).toBeChecked()
  const second = await context.newPage()
  await second.goto('/page/me/settings/')
  await page.getByRole('radio', { name: /冷调青金/ }).check()
  await expect(second.locator('html')).toHaveAttribute('data-theme', 'cool')
  await page.evaluate(() => localStorage.removeItem('jetaime.theme'))
  await expect(second.locator('html')).toHaveAttribute('data-theme', 'aurora')
  await second.close()
})

test('theme remains usable without localStorage access', async ({ page }) => {
  await page.addInitScript(() => {
    const originalGet = Storage.prototype.getItem, originalSet = Storage.prototype.setItem
    Storage.prototype.getItem = function(key) { if (key === 'jetaime.theme') throw new Error('Storage unavailable'); return originalGet.call(this, key) }
    Storage.prototype.setItem = function(key, value) { if (key === 'jetaime.theme') throw new Error('Storage unavailable'); originalSet.call(this, key, value) }
  })
  await page.goto('/page/me/settings/')
  await expect(page.getByRole('radio', { name: /静谧灰蓝/ })).toBeChecked()
  await page.getByRole('radio', { name: /极简单色/ }).check()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'minimal')
})
