import { expect, test } from '@playwright/test'

test('group settings shows reminders, supports cancellation, and hides group closure', async ({ page }, testInfo) => {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }
  const peer = { id: 'bob', username: 'Bob', nickname: '小夏' }
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  const reminders = [
    { id: 'r1', userId: me.id, userName: me.nickname, title: '吃饭', dueAt: '2026-10-21T12:00:00+08:00', timezone: 'Asia/Shanghai', status: 'pending' },
    { id: 'r2', userId: peer.id, userName: peer.nickname, title: '带上雨伞', dueAt: '2026-10-22T08:00:00+08:00', timezone: 'Asia/Shanghai', status: 'pending' },
    { id: 'r4', userId: me.id, userName: me.nickname, title: '未确认的提醒', dueAt: null, timezone: 'Asia/Shanghai', status: 'cancelled' },
    { id: 'r3', userId: me.id, userName: me.nickname, title: '喝水', dueAt: '2026-10-20T12:00:00+08:00', timezone: 'Asia/Shanghai', status: 'sent' },
  ]
  let failCancel = true
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/groups') return route.fulfill({ json: [{ id: 'g1', peer, assistantName: 'AI', unreadCount: 0, lastReadSeq: 0, lastMessageSeq: 0, lastMessage: '' }] })
    if (path.endsWith('/reminders')) return route.fulfill({ json: reminders })
    if (path.endsWith('/r1/cancel')) {
      if (failCancel) { failCancel = false; return route.fulfill({ status: 500, json: { error: { code: 'internal_error' } } }) }
      reminders[0].status = 'cancelled'
      return route.fulfill({ json: { ok: true } })
    }
    if (path.endsWith('/runs')) return route.fulfill({ json: [] })
    if (path.endsWith('/messages')) return route.fulfill({ json: { messages: [], hasMore: false } })
    return route.fulfill({ json: { id: 'g1', members: [me, peer], lastSeq: 0, readSeq: 0, unreadCount: 0, assistantName: 'AI', aiEnabled: true } })
  })
  await page.goto('/page/chat/g1/')
  await expect(page.getByRole('button', { name: '关闭群聊', exact: true })).toHaveCount(0)
  await page.getByRole('link', { name: '群聊配置' }).click()
  const dialog = page.getByRole('region', { name: '群聊配置' })
  await expect(page).toHaveURL(/\/chat\/g1\/settings\//)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('navigation', { name: '主导航' })).toHaveCount(0)
  expect(await dialog.boundingBox()).toMatchObject({ x: 0, y: 0, ...page.viewportSize()! })
  await expect(dialog.getByRole('heading', { name: '吃饭' })).toBeVisible()
  await expect(dialog.getByText(/2026年10月21日.*12:00/)).toBeVisible()
  await expect(dialog.getByRole('button', { name: '取消提醒', exact: true })).toHaveCount(1)
  await page.screenshot({ path: testInfo.outputPath('reminder-settings.png'), fullPage: true })
  await dialog.getByRole('button', { name: '取消提醒', exact: true }).click()
  await expect(dialog.getByRole('alert')).toBeVisible()
  await expect(dialog.getByRole('heading', { name: '吃饭' })).toBeVisible()
  await dialog.getByRole('button', { name: '取消提醒', exact: true }).click()
  await expect(dialog.getByRole('heading', { name: '吃饭' })).toHaveCount(0)
  await dialog.getByRole('button', { name: /已取消/ }).click()
  await expect(dialog.getByRole('heading', { name: '吃饭' })).toBeVisible()
  await expect(dialog.getByText('时间未补全')).toBeVisible()
  await dialog.getByRole('button', { name: /已提醒/ }).click()
  await expect(dialog.getByRole('heading', { name: '喝水' })).toBeVisible()
  await page.getByRole('link', { name: '返回聊天', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page).toHaveURL(/\/chat\/g1\/$/)
  await page.goBack()
  await expect(dialog).toBeVisible()
})

test('reminder settings has a useful empty state', async ({ page }) => {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/groups' || path.endsWith('/runs') || path.endsWith('/reminders')) return route.fulfill({ json: [] })
    if (path.endsWith('/messages')) return route.fulfill({ json: { messages: [], hasMore: false } })
    return route.fulfill({ json: { id: 'g1', members: [me], assistantName: 'AI', aiEnabled: true, lastSeq: 0, readSeq: 0 } })
  })
  await page.goto('/page/chat/g1/')
  await page.getByRole('link', { name: '群聊配置' }).click()
  await expect(page.getByText('还没有待提醒事项')).toBeVisible()
  await page.getByRole('link', { name: '回到聊天' }).click()
  await expect(page).toHaveURL(/\/chat\/g1\/$/)
})

test('proactive switch persists across reloads and preserves state on failure', async ({ page }) => {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  let enabled = false
  let failSave = false
  const state = () => ({ id: 'g1', members: [me], assistantName: 'AI', aiEnabled: true, proactiveEnabled: enabled, lastSeq: 0, readSeq: 0 })
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/settings')) {
      if (failSave) return route.fulfill({ status: 500, json: { error: { code: 'internal_error' } } })
      enabled = route.request().postDataJSON().proactiveEnabled
      return route.fulfill({ json: state() })
    }
    if (path === '/api/groups' || path.endsWith('/reminders')) return route.fulfill({ json: [] })
    return route.fulfill({ json: state() })
  })
  await page.goto('/page/chat/g1/settings/')
  const toggle = page.getByRole('switch', { name: 'AI 主动参与' })
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-checked', 'true')
  await page.reload()
  await expect(toggle).toHaveAttribute('aria-checked', 'true')
  failSave = true
  await toggle.click()
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(toggle).toHaveAttribute('aria-checked', 'true')
})
