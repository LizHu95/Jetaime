import { expect, test, type Page } from '@playwright/test'
import type { SendMessageInput } from '../../src/features/chat/location'

async function conversation(page: Page, failFirst = false) {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }, peer = { id: 'bob', username: 'Bob', nickname: '小夏' }
  const sent: SendMessageInput[] = []
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  await page.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (path === '/api/groups' || path.endsWith('/runs') || path.endsWith('/reminders')) return route.fulfill({ json: [] })
    if (path.endsWith('/read')) return route.fulfill({ json: { readSeq: sent.length } })
    if (path.endsWith('/messages')) {
      if (request.method() === 'POST') {
        const body: SendMessageInput = request.postDataJSON(); sent.push(body)
        if (failFirst && sent.length === 1) return route.fulfill({ status: 503, json: { error: { code: 'unavailable', message: '暂不可用' } } })
        return route.fulfill({ json: { id: `sent-${sent.length}`, content: body.content, mentionsAI: body.mentionsAI, clientMessageId: body.clientMessageId, groupId: 'g1', seq: sent.length, senderType: 'user', senderId: me.id, senderName: me.nickname, replyToId: null, createdAt: new Date().toISOString() } })
      }
      return route.fulfill({ json: { messages: [], hasMore: false } })
    }
    return route.fulfill({ json: { id: 'g1', members: [me, peer], assistantName: 'AI', aiEnabled: true, readSeq: 0, lastSeq: 0, unreadCount: 0 } })
  })
  await page.goto('/page/chat/g1/')
  return sent
}

async function configureLocation(page: Page, enabled: boolean) {
  await page.getByRole('link', { name: '群聊配置', exact: true }).click()
  const toggle = page.getByRole('switch', { name: '发送消息时使用位置', exact: true })
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-checked', String(enabled))
  await page.getByRole('link', { name: '返回聊天', exact: true }).click()
  await expect(page.getByRole('button', { name: '发送消息时使用位置', exact: true })).toHaveCount(0)
  await expect(page.getByText('Enter 发送 · Shift + Enter 换行')).toHaveCount(0)
}

test('a casual meal question acquires location before posting when automatic location is enabled', async ({ page, context }) => {
  await context.grantPermissions(['geolocation'])
  await context.setGeolocation({ longitude: 121.5, latitude: 31.3, accuracy: 30 })
  await page.addInitScript(() => localStorage.setItem('jetaime.location.auto.alice', 'true'))
  const sent = await conversation(page)
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  await page.getByLabel('消息内容').fill('有什么可以吃的')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByLabel('消息内容')).toHaveValue('')
  expect(sent).toHaveLength(1)
  expect(sent[0].clientContext?.location).toMatchObject({ longitude: 121.5, latitude: 31.3, coordinateSystem: 'wgs84' })
})

test('client location requires a click, accompanies AI requests and is stable across retries', async ({ page, context }) => {
  await context.grantPermissions(['geolocation'])
  await context.setGeolocation({ longitude: 121.5, latitude: 31.3, accuracy: 30 })
  const sent = await conversation(page, true)
  await expect(page.getByText(/已开启：/)).toHaveCount(0)
  await configureLocation(page, true)
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  await page.getByLabel('消息内容').fill('附近吃点清淡的')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await page.getByRole('button', { name: '重试发送', exact: true }).click()
  await expect(page.getByLabel('消息内容')).toHaveValue('')
  expect(sent).toHaveLength(2)
  expect(sent[1]).toEqual(sent[0])
  expect(sent[0].mentionsAI).toBe(true)
  expect(sent[0].clientContext?.location).toMatchObject({ longitude: 121.5, latitude: 31.3, accuracyMeters: 30, coordinateSystem: 'wgs84' })
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  await page.getByLabel('消息内容').fill('普通聊天')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByLabel('消息内容')).toHaveValue('')
  expect(sent[2].clientContext?.location.coordinateSystem).toBe('wgs84')
  await page.setViewportSize({ width: 320, height: 568 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('permission denial offers diagnostics and recovers after permission is restored', async ({ page, isMobile }) => {
  await page.addInitScript(() => Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition: (_: unknown, fail: (e: { code: number; message: string }) => void) => fail({ code: 1, message: 'Origin does not have permission to use Geolocation service' }) } }))
  const sent = await conversation(page)
  await page.getByRole('link', { name: '群聊配置', exact: true }).click()
  await page.getByRole('switch', { name: '发送消息时使用位置', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('定位权限被拒绝')
  await expect(page.getByRole('switch', { name: '发送消息时使用位置' })).toHaveAttribute('aria-checked', 'false')
  if (isMobile) await expect(page.getByText(/设置 → 隐私与安全性 → 定位服务/)).toBeVisible()
  await page.getByText('查看定位权限检测', { exact: true }).click()
  await expect(page.getByText(/当前站点：/)).toBeVisible()
  await expect(page.getByText(/页面权限策略：/)).toBeVisible()
  await expect(page.getByText('定位错误码：1', { exact: true })).toBeVisible()
  await expect(page.getByText('原始错误：Origin does not have permission to use Geolocation service', { exact: true })).toBeVisible()
  await expect(page.getByText(/运行模式：/)).toBeVisible()
  await page.evaluate(() => {
    navigator.geolocation.getCurrentPosition = done => done({ coords: { longitude: 121.5, latitude: 31.3, accuracy: 30 }, timestamp: Date.now() } as GeolocationPosition)
  })
  await page.getByRole('button', { name: '重试定位', exact: true }).click()
  await expect(page.getByRole('switch', { name: '发送消息时使用位置' })).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByRole('alert')).toHaveCount(0)
  await page.getByRole('link', { name: '返回聊天', exact: true }).click()
  await page.getByLabel('消息内容').fill('@AI 从五角场去三门路')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByLabel('消息内容')).toHaveValue('')
  expect(sent[0].clientContext?.location.coordinateSystem).toBe('wgs84')
})

test('automatic location failure still sends the question and turns the preference off', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('jetaime.location.auto.alice', 'true')
    Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition: (_: unknown, fail: (e: { code: number }) => void) => fail({ code: 1 }) } })
  })
  const sent = await conversation(page)
  await page.getByLabel('消息内容').fill('@AI 附近吃什么')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByLabel('消息内容')).toHaveValue('')
  await expect(page.getByText(/定位权限被拒绝/)).toBeVisible()
  await expect(page.getByRole('button', { name: '发送消息时使用位置', exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => localStorage.getItem('jetaime.location.auto.alice'))).toBe('false')
  expect(sent).toHaveLength(1)
  expect(sent[0].clientContext).toBeUndefined()
})

test('every message obtains a new position without a reuse window', async ({ page }) => {
  await page.clock.install()
  await page.addInitScript(() => {
    const state = { calls: 0, longitude: 121.5 }
    Object.assign(window, { locationTest: state })
    Object.defineProperty(navigator, 'geolocation', { value: { getCurrentPosition: (done: PositionCallback) => {
      state.calls++
      done({ coords: { longitude: state.longitude, latitude: 31.3, accuracy: 30 }, timestamp: Date.now() } as GeolocationPosition)
    } } })
  })
  const sent = await conversation(page)
  const calls = () => page.evaluate(() => (window as unknown as { locationTest: { calls: number } }).locationTest.calls)
  await configureLocation(page, true)
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  expect(await calls()).toBe(1)
  const send = async (text: string) => {
    await page.getByLabel('消息内容').fill(text)
    await page.getByRole('button', { name: '发送', exact: true }).click()
    await expect(page.getByLabel('消息内容')).toHaveValue('')
  }
  await send('附近吃什么')
  expect(await calls()).toBe(2)
  await page.evaluate(() => { (window as unknown as { locationTest: { longitude: number } }).locationTest.longitude = 121.6 })
  await send('清淡点')
  expect(await calls()).toBe(3)
  expect(sent[1].clientContext?.location.longitude).toBe(121.6)
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  await send('普通聊天')
  expect(await calls()).toBe(4)
  expect(sent[2].mentionsAI).toBe(false)
  expect(sent[2].clientContext?.location.longitude).toBe(121.6)
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  await send('解释一下递归')
  expect(await calls()).toBe(5)
  expect(sent[3].clientContext?.location.longitude).toBe(121.6)
  await send('怎么去三门路，远不远')
  expect(await calls()).toBe(6)
  expect(sent[4].clientContext?.location.longitude).toBe(121.6)
  await send('从五角场去三门路')
  expect(await calls()).toBe(7)
  expect(sent[5].clientContext?.location.longitude).toBe(121.6)
  await page.reload()
  expect(await page.evaluate(() => localStorage.getItem('jetaime.location.auto.alice'))).toBe('true')
  expect(await calls()).toBe(0)
  await send('重新打开后的普通消息')
  expect(await calls()).toBe(1)
  expect(sent.at(-1)?.clientContext?.location.longitude).toBe(121.5)
  await configureLocation(page, false)
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  await send('附近吃什么')
  // Returning from settings remounts the chat without making a device request.
  expect(await calls()).toBe(1)
  expect(sent.at(-1)?.clientContext).toBeUndefined()
})
