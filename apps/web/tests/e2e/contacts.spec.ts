import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket('**/api/chat/events', socket => {
    socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' })))
  })
})
const me = { id: 'alice', username: 'Alice', nickname: '小林' }
const peer = { id: 'bob', username: 'Bob', nickname: '小夏' }
const invitation = { id: 'request-1', from: peer, to: me, status: 'pending', groupId: null, createdAt: new Date().toISOString() }
test.beforeEach(async ({ page }) => {
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
})
test('find and accept an incoming request; history is collapsed and group list is removed', async ({ page }, info) => {
  let accepted = false
  await page.route('**/api/contacts/users?*', route => route.fulfill({ json: peer }))
  await page.route('**/api/contacts/requests', route => route.fulfill({ json: [{ ...invitation, status: accepted ? 'accepted' : 'pending', groupId: accepted ? 'group-1' : null }] }))
  await page.route('**/api/contacts/requests/request-1/accept', async route => { accepted = true; await route.fulfill({ json: { ...invitation, status: 'accepted', groupId: 'group-1' } }) })
  await page.goto('/page/contacts/')
  await page.getByLabel('对方账号（区分大小写）').fill('Bob')
  await page.getByRole('button', { name: '查找', exact: true }).click()
  await expect(page.getByText('对方已申请，请在下方处理', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '添加好友', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '接受', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('已添加好友')
  await expect(page.getByRole('status').getByRole('link', { name: '去聊天' })).toHaveAttribute('href', '/page/chat/group-1/')
  await expect(page.getByRole('heading', { name: '我的群组' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '接受', exact: true })).toHaveCount(0)
  await expect(page.locator('.contact-history')).not.toHaveAttribute('open', '')
  await page.getByText('历史申请 · 1', { exact: true }).click()
  await expect(page.locator('.contact-history').getByRole('link', { name: '去聊天' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByText('历史申请 · 1', { exact: true }).click()
  await page.screenshot({ path: `../../outputs/web-check/${info.project.name}-contacts.png`, fullPage: true })
  await page.setViewportSize({ width: 320, height: 568 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('send, prevent duplicate sending and cancel an outgoing request', async ({ page }) => {
  let sent = false, cancelled = false
  const outgoing = { ...invitation, from: me, to: peer }
  await page.route('**/api/contacts/users?*', route => route.fulfill({ json: peer }))
  await page.route('**/api/contacts/requests', async route => {
    if (route.request().method() === 'POST') { sent = true; await route.fulfill({ json: outgoing }); return }
    await route.fulfill({ json: sent ? [{ ...outgoing, status: cancelled ? 'cancelled' : 'pending' }] : [] })
  })
  await page.route('**/api/contacts/requests/request-1/cancel', async route => { cancelled = true; await route.fulfill({ json: { ...outgoing, status: 'cancelled' } }) })
  await page.goto('/page/contacts/')
  await page.getByLabel('对方账号（区分大小写）').fill('Bob')
  await page.getByRole('button', { name: '查找', exact: true }).click()
  await page.getByRole('button', { name: '添加好友', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('申请已发送')
  await expect(page.getByRole('region', { name: '发出的申请' })).toContainText('等待对方接受')
  await page.getByRole('button', { name: '查找', exact: true }).click()
  await expect(page.getByRole('button', { name: '添加好友', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '取消', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('已取消申请')
  await expect(page.getByRole('button', { name: '添加好友', exact: true })).toBeVisible()
  await expect(page.getByText('暂无待处理申请')).toBeVisible()
})

test('failed rejection keeps request actionable, successful retry moves it to history', async ({ page }) => {
  let rejected = false, fail = true
  await page.route('**/api/contacts/requests', route => route.fulfill({ json: [{ ...invitation, status: rejected ? 'rejected' : 'pending' }] }))
  await page.route('**/api/contacts/requests/request-1/reject', async route => {
    if (fail) { fail = false; await route.fulfill({ status: 500, json: { error: { code: 'internal_error' } } }); return }
    rejected = true
    await route.fulfill({ json: { ...invitation, status: 'rejected' } })
  })
  await page.goto('/page/contacts/')
  await page.getByRole('button', { name: '拒绝', exact: true }).click()
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page.getByRole('button', { name: '拒绝', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: '拒绝', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('已拒绝申请')
  await expect(page.getByRole('button', { name: '拒绝', exact: true })).toHaveCount(0)
  await page.getByText('历史申请 · 1', { exact: true }).click()
  await expect(page.locator('.contact-history')).toContainText('已拒绝')
})
test('password mismatch, incorrect old password and successful change', async ({ page }, info) => {
  await page.route('**/api/auth/password', async route => {
    if (route.request().postDataJSON().oldPassword === 'wrong-old') await route.fulfill({ status: 401, json: { error: { code: 'invalid_credentials' } } })
    else await route.fulfill({ status: 204 })
  })
  await page.goto('/page/auth/password/')
  await page.getByLabel('旧密码', { exact: true }).fill('wrong-old')
  await page.getByLabel('新密码', { exact: true }).fill('new-password')
  await page.getByLabel('确认新密码').fill('different')
  await page.getByRole('button', { name: '保存新密码' }).click()
  await expect(page.getByRole('alert')).toContainText('两次输入')
  await page.getByLabel('确认新密码').fill('new-password')
  await page.getByRole('button', { name: '保存新密码' }).click()
  await expect(page.getByRole('alert')).toContainText('旧密码不正确')
  await page.screenshot({ path: `../../outputs/web-check/${info.project.name}-password.png`, fullPage: true })
  await page.getByLabel('旧密码', { exact: true }).fill('correct-old')
  await page.getByRole('button', { name: '保存新密码' }).click()
  await expect(page).toHaveURL(/\/auth\/login\/$/)
  await expect(page.getByRole('status')).toContainText('密码已修改')
  expect(await page.evaluate(() => localStorage.getItem('jetaime.auth.session'))).toBeNull()
})
test('revoked session redirects to login', async ({ page }) => {
  for (const path of ['contacts/requests', 'groups']) await page.route(`**/api/${path}`, route => route.fulfill({ status: 401, json: { error: { code: 'unauthorized' } } }))
  await page.goto('/page/contacts/')
  await expect(page).toHaveURL(/\/auth\/login\/$/)
})
