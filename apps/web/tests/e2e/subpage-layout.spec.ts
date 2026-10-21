import { expect, test } from '@playwright/test'

test('secondary and deeper pages align navigation and scroll content independently', async ({ page }) => {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }
  const peer = { id: 'bob', username: 'Bob', nickname: '小夏' }
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), {
    user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString(),
  })
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/contacts/requests') return route.fulfill({ json: Array.from({ length: 30 }, (_, i) => ({ id: `r${i}`, from: { ...peer, id: `peer${i}`, nickname: `好友 ${i}` }, to: me, status: 'pending', groupId: null, createdAt: new Date().toISOString() })) })
    if (path === '/api/groups' || path.endsWith('/reminders')) return route.fulfill({ json: [] })
    if (path.endsWith('/public-key')) return route.fulfill({ json: { enabled: false } })
    if (path.endsWith('/messages')) return route.fulfill({ json: { messages: [], hasMore: false } })
    if (path.endsWith('/runs')) return route.fulfill({ json: [] })
    return route.fulfill({ json: { id: 'g1', members: [me, peer], aiEnabled: true, proactiveEnabled: false } })
  })
  let reference: { x: number; width: number } | undefined
  for (const [path, title] of [
    ['/contacts/', '加好友'], ['/me/settings/', '主题设置'], ['/auth/password/', '修改密码'], ['/notifications/', '消息通知'], ['/chat/g1/settings/', '群聊配置'],
  ]) {
    await page.goto(`/page${path}`)
    const layout = page.locator('.subpage-layout')
    await expect(layout.getByRole('heading', { name: title, exact: true })).toBeVisible()
    const nav = await layout.locator('.nav-bar').boundingBox()
    const body = await layout.locator('.subpage-content').boundingBox()
    expect(nav).not.toBeNull()
    expect(body).not.toBeNull()
    expect(body!.x).toBe(nav!.x)
    expect(body!.width).toBe(nav!.width)
    if (!reference) reference = { x: nav!.x, width: nav!.width }
    expect({ x: nav!.x, width: nav!.width }).toEqual(reference)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (path === '/contacts/') {
      await expect(page.getByText('好友 29', { exact: true })).toBeAttached()
      await page.getByText('好友 29', { exact: true }).scrollIntoViewIfNeeded()
      expect(await layout.locator('.subpage-body').evaluate(el => el.scrollTop)).toBeGreaterThan(0)
      expect((await layout.locator('.nav-bar').boundingBox())!.y).toBe(nav!.y)
      expect(await page.locator('.main-content').evaluate(el => el.scrollTop)).toBe(0)
      await expect(layout.getByRole('link', { name: '返回聊天', exact: true })).toBeInViewport()
    }
  }
  await page.getByRole('link', { name: '返回聊天', exact: true }).click()
  await expect(page).toHaveURL(/\/page\/chat\/g1\/$/)
})
