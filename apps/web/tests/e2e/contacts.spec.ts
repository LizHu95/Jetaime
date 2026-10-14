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
test('find, send and accept creates a group; layout fits mobile', async ({ page }, info) => {
  let accepted = false
  await page.route('**/api/contacts/users?*', route => route.fulfill({ json: peer }))
  await page.route('**/api/contacts/requests', route => route.fulfill({ json: route.request().method() === 'POST' ? invitation : [{ ...invitation, status: accepted ? 'accepted' : 'pending' }] }))
  await page.route('**/api/contacts/requests/request-1/accept', async route => { accepted = true; await route.fulfill({ json: { ...invitation, status: 'accepted', groupId: 'group-1' } }) })
  await page.route('**/api/groups', route => route.fulfill({ json: accepted ? [{ id: 'group-1', peer, status: 'active', assistantId: 'ai-1', assistantName: 'AI 助手' }] : [] }))
  await page.goto('/page/contacts/')
  await page.getByLabel('对方账号（区分大小写）').fill('Bob')
  await page.getByRole('button', { name: '查找', exact: true }).click()
  await page.getByRole('button', { name: '添加好友', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('申请已记录')
  await page.getByRole('button', { name: '接受并创建群' }).click()
  await expect(page.getByRole('status')).toContainText('共享 AI 已创建')
  await expect(page.getByText('与 小夏 的群', { exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: `../../outputs/web-check/${info.project.name}-contacts.png`, fullPage: true })
  await page.setViewportSize({ width: 320, height: 568 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
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
