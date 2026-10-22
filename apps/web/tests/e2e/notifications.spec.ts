import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  await page.addInitScript(() => {
    localStorage.setItem('jetaime.auth.session', JSON.stringify({
      user: { id: 'browser-user', username: 'Demo_User', nickname: '小夏' },
      token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 600_000).toISOString()
    }))
  })
})

test('enable, send a test and disable device notifications', async ({ page, isMobile }) => {
  test.skip(isMobile, 'iPhone must install the web app before subscribing')
  const calls: string[] = []
  await page.addInitScript(() => {
    let subscribed = false
    const subscription = {
      endpoint: 'https://web.push.apple.com/test-device', options: {},
      toJSON: () => ({ endpoint: 'https://web.push.apple.com/test-device', expirationTime: null, keys: { auth: 'test-auth', p256dh: 'test-key' } }),
      unsubscribe: async () => { subscribed = false; return true }
    }
    const registration = {
      pushManager: { getSubscription: async () => subscribed ? subscription : null, subscribe: async () => { subscribed = true; return subscription } },
      getNotifications: async () => []
    }
    Object.defineProperty(navigator, 'serviceWorker', { value: { register: async () => registration, ready: Promise.resolve(registration) } })
    Object.defineProperty(Notification, 'requestPermission', { value: async () => 'granted' })
  })
  await page.route('**/api/push/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/public-key')) return route.fulfill({ json: { enabled: true, publicKey: 'AQ' } })
    expect(route.request().headers().authorization).toBe(`Bearer ${'x'.repeat(43)}`)
    calls.push(`${route.request().method()} ${path}`)
    if (path.endsWith('/test')) return route.fulfill({ status: 202, json: { queued: true } })
    return route.fulfill({ status: 204 })
  })
  await page.goto('/page/auth/session/')
  await page.getByRole('link', { name: '消息通知设置' }).click()
  await page.getByRole('button', { name: '开启通知', exact: true }).click()
  await expect(page.getByText('此设备通知已开启', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '发送测试通知' }).click()
  await expect(page.getByText(/测试通知已提交/)).toBeVisible()
  await page.getByRole('button', { name: '关闭此设备通知' }).click()
  await expect(page.getByText('此设备通知未开启', { exact: true })).toBeVisible()
  expect(calls).toContain('POST /api/push/subscriptions')
  expect(calls).toContain('POST /api/push/test')
  expect(calls).toContain('DELETE /api/push/subscriptions')
})

test('iPhone browser explains how to install before enabling notifications', async ({ page, isMobile }) => {
  test.skip(!isMobile, 'iPhone installation instructions')
  await page.goto('/page/notifications/')
  await expect(page.getByText(/请先用 Safari 的分享菜单/)).toBeVisible()
  await expect(page.getByRole('button', { name: '开启通知', exact: true })).toHaveCount(0)
})

test('an insecure pinned address shows an HTTPS recovery link and diagnostics', async ({ page }) => {
  await page.addInitScript(() => { Object.defineProperty(window, 'isSecureContext', { value: false }) })
  await page.goto('/page/notifications/')
  await expect(page.getByText(/当前使用的是非安全地址/)).toBeVisible()
  await expect(page.getByRole('link', { name: '打开 HTTPS 通知设置' })).toHaveAttribute('href', /^https:\/\//)
  await page.getByText('查看通知环境检测', { exact: true }).click()
  await expect(page.getByText(/安全连接：否/)).toBeVisible()
  await expect(page.getByRole('button', { name: '开启通知', exact: true })).toHaveCount(0)
})

test('missing push capability is reported separately from HTTPS and installation', async ({ page, isMobile }) => {
  test.skip(isMobile, 'desktop checks capability failure without iPhone installation gating')
  await page.addInitScript(() => { delete (window as unknown as Record<string, unknown>).PushManager })
  await page.goto('/page/notifications/')
  await expect(page.getByText(/当前系统没有提供通知所需的能力/)).toBeVisible()
  await page.getByText('查看通知环境检测', { exact: true }).click()
  await expect(page.getByText(/推送：不可用/)).toBeVisible()
})
