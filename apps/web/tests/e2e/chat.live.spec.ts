import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import type { Session } from '../../src/features/auth/schema'
test.skip(!process.env.WEB_LIVE_CHAT, 'Requires isolated API and fixture model')
async function createPair(request: APIRequestContext) {
  const unique = crypto.randomUUID().replaceAll('-', '').slice(0, 12)
  const sessions: Session[] = []
  for (const username of [`alice_${unique}`, `bob_${unique}`]) {
    expect((await request.post('/api/auth/register', { data: { username, password: 'pass123', nickname: username.startsWith('alice') ? '小林' : '小夏' } })).status()).toBe(201)
    sessions.push(await (await request.post('/api/auth/login', { data: { username, password: 'pass123' } })).json())
  }
  const headers = (index: number) => ({ Authorization: `Bearer ${sessions[index].token}` })
  const pending = await (await request.post('/api/contacts/requests', { headers: headers(0), data: { username: sessions[1].user.username } })).json()
  const accepted = await (await request.post(`/api/contacts/requests/${pending.id}/accept`, { headers: headers(1) })).json()
  return { sessions, headers, group: accepted.groupId as string }
}
async function loginPage(page: Page, session: unknown) {
  await page.addInitScript(s => localStorage.setItem('jetaime.auth.session', JSON.stringify(s)), session)
  await page.goto('/page/chat/')
  await expect(page.getByRole('status')).toContainText('实时消息已连接')
}

test('realtime, unread/read synchronization, offline catchup and member isolation', async ({ page, browser, request }, info) => {
  const { sessions, headers, group } = await createPair(request)
  const second = await browser.newContext({ baseURL: info.project.use.baseURL })
  const bob = await second.newPage()
  try {
    await loginPage(page, sessions[0]); await loginPage(bob, sessions[1])
    await page.bringToFront()
    await page.locator('.chat-group').filter({ hasText: '小夏' }).click()
    await page.getByLabel('消息内容').fill('第一条实时消息')
    await page.getByRole('button', { name: '发送', exact: true }).click()
    await expect(bob.getByLabel('小林：1 条未读')).toBeVisible()
    await bob.bringToFront()
    await bob.locator('.chat-group').filter({ hasText: '小林' }).click()
    await expect(bob.getByText('第一条实时消息', { exact: true }).last()).toBeVisible()
    await expect.poll(async () => (await (await request.get('/api/groups', { headers: headers(1) })).json())[0].unreadCount).toBe(0)
    await bob.getByLabel('消息内容').fill('对方已收到，回复你')
    await bob.getByRole('button', { name: '发送', exact: true }).click()
    await expect(page.getByText('对方已收到，回复你', { exact: true }).last()).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: `../../outputs/web-check/${info.project.name}-chat.png`, fullPage: true })
    // Offline reader catches up from PostgreSQL rather than trusting delivery.
    await second.setOffline(true)
    const payload = { content: '断线期间的消息', clientMessageId: crypto.randomUUID(), mentionsAI: false }
    const sent = await (await request.post(`/api/chat/groups/${group}/messages`, { headers: headers(0), data: payload })).json()
    const duplicate = await (await request.post(`/api/chat/groups/${group}/messages`, { headers: headers(0), data: payload })).json()
    expect(duplicate.id).toBe(sent.id)
    await second.setOffline(false)
    await expect(bob.getByText('断线期间的消息', { exact: true }).last()).toBeVisible({ timeout: 20_000 })
    const runs = await (await request.get(`/api/chat/groups/${group}/runs`, { headers: headers(0) })).json()
    expect(runs).toHaveLength(0)
    await request.post(`/api/chat/groups/${group}/read`, { headers: headers(1), data: { seq: sent.seq } })
    await request.post(`/api/chat/groups/${group}/read`, { headers: headers(1), data: { seq: 1 } })
    const state = await (await request.get(`/api/chat/groups/${group}`, { headers: headers(1) })).json()
    expect(state.readSeq).toBe(sent.seq)
    expect((await request.get(`/api/chat/groups/${group}/messages`)).status()).toBe(401)
  } finally { await second.close() }
})

test('shared AI reply counts unread for both, failure retries one run, close blocks late writes', async ({ page, browser, request }, info) => {
  const { sessions, headers, group } = await createPair(request)
  const second = await browser.newContext({ baseURL: info.project.use.baseURL })
  const bob = await second.newPage()
  const mirrorContext = await browser.newContext({ baseURL: info.project.use.baseURL })
  const mirror = await mirrorContext.newPage()
  const base = `/api/chat/groups/${group}`
  try {
    await loginPage(page, sessions[0]); await loginPage(bob, sessions[1]); await loginPage(mirror, sessions[0])
    expect((await request.post(`${base}/messages`, { headers: headers(0), data: { content: '@AI 请帮我回答', clientMessageId: crypto.randomUUID() } })).status()).toBe(200)
    await expect(page.getByLabel('小夏：1 条未读')).toBeVisible({ timeout: 10_000 })
    await expect(bob.getByLabel('小林：2 条未读')).toBeVisible()
    await expect(mirror.getByLabel('小夏：1 条未读')).toBeVisible()
    await page.bringToFront()
    await page.locator('.chat-group').filter({ hasText: '小夏' }).click()
    await expect(page.locator('.ai-message')).toContainText('测试 AI 回答')
    await expect.poll(async () => (await (await request.get('/api/groups', { headers: headers(0) })).json())[0].unreadCount).toBe(0)
    await expect(mirror.getByLabel('小夏：1 条未读')).toHaveCount(0)
    await expect(bob.getByLabel('小林：2 条未读')).toBeVisible()
    await page.getByLabel('消息内容').fill(`@AI AI_FAIL_ONCE ${group}`)
    await page.getByRole('button', { name: '发送', exact: true }).click()
    await expect(page.getByText('AI 正在回复…', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: '重试 AI 回复' }).click()
    await expect(page.locator('.ai-message').filter({ hasText: 'AI_FAIL_ONCE' })).toHaveCount(1)
    const runs = await (await request.get(`${base}/runs`, { headers: headers(0) })).json()
    expect(runs).toHaveLength(2)
    expect(runs.every((run: { status: string }) => run.status === 'succeeded')).toBe(true)
    expect((await (await request.get(`${base}/messages`, { headers: headers(0) })).json()).messages).toHaveLength(4)
    await page.getByRole('button', { name: '关闭群聊', exact: true }).click()
    await page.getByRole('button', { name: '确认关闭', exact: true }).click()
    await expect(page).toHaveURL(/\/page\/chat\/$/)
    await expect(bob.getByText('还没有群聊。')).toBeVisible()
    expect((await request.post(`${base}/messages`, { headers: headers(1), data: { content: '晚到消息', clientMessageId: crypto.randomUUID() } })).status()).toBe(404)
  } finally { await second.close(); await mirrorContext.close() }
})

test('original Agent sources survive reload and extraction replies use the same push', async ({ page, request }, info) => {
  const { sessions, headers, group } = await createPair(request)
  const base = `/api/chat/groups/${group}`
  const evidence = await (await request.post(`${base}/messages`, { headers: headers(0), data: { content: '我明天负责准备食材', clientMessageId: crypto.randomUUID() } })).json()
  await loginPage(page, sessions[0])
  await page.bringToFront()
  await page.locator('.chat-group').filter({ hasText: '小夏' }).click()
  await page.getByRole('button', { name: '@AI', exact: true }).click()
  await expect(page.getByLabel('AI 能力')).toHaveCount(0)
  await page.getByLabel('消息内容').fill('请提取待办')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.locator('.ai-message')).toContainText('准备食材')
  await expect(page.locator('.ai-message')).toContainText('尚未创建')
  const messages = (await (await request.get(`${base}/messages`, { headers: headers(0) })).json()).messages
  const answer = messages.find((m: { senderType: string }) => m.senderType === 'assistant')
  expect(answer.agentResult.kind).toBe('extract')
  expect(answer.agentResult.route.kind).toBe('extract')
  expect(answer.agentResult.suggestions[0].sourceMessageIds).toEqual([evidence.id])
  expect(answer.agentResult.suggestions[0].assigneeId).toBe(sessions[0].user.id)
  await page.reload()
  await page.locator('.ai-message').getByRole('button', { name: '来源 1' }).click()
  await expect(page.locator('.highlighted-source')).toContainText('我明天负责准备食材')
  await page.screenshot({ path: `../../outputs/web-check/${info.project.name}-agent-extraction.png`, fullPage: true })
  expect((await request.get(`${base}/messages/${evidence.id}`, { headers: headers(1) })).status()).toBe(200)
  await page.getByLabel('消息内容').fill('@AI 邻居鞋子放在门口很臭，有什么除味方法？')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.locator('.ai-message')).toHaveCount(2)
  await expect(page.locator('.ai-message').last()).toContainText('AI 回答')
  const updated = (await (await request.get(`${base}/messages`, { headers: headers(0) })).json()).messages
  const replies = updated.filter((m: { senderType: string }) => m.senderType === 'assistant')
  expect(replies.at(-1).agentResult.kind).toBe('main')
  expect(replies.at(-1).agentResult.route.kind).toBe('main')
})
