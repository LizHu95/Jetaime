import { expect, test } from '@playwright/test'
import type { Message } from '../../src/features/chat/types'

test('sending and retrying preserve an editable draft without secure-context randomUUID', async ({ page }) => {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }
  const peer = { id: 'bob', username: 'Bob', nickname: '小夏' }
  await page.addInitScript(session => {
    localStorage.setItem('jetaime.auth.session', JSON.stringify(session))
    Object.defineProperty(crypto, 'randomUUID', { value: undefined, configurable: true })
  }, { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
  await page.routeWebSocket('**/api/chat/events', socket => {
    socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' })))
  })
  const attempts: { clientMessageId: string; content: string }[] = []
  const messages: Message[] = []
  let finishRetry!: () => void
  const retryResponse = new Promise<void>(resolve => { finishRetry = resolve })
  const browserErrors: string[] = []
  page.on('pageerror', error => browserErrors.push(error.message))
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/groups') return route.fulfill({ json: [{ id: 'group-1', peer, assistantName: 'AI', unreadCount: 0, lastReadSeq: 0, lastMessageSeq: messages.length, lastMessage: '' }] })
    if (path.endsWith('/runs')) return route.fulfill({ json: [] })
    if (path.endsWith('/read')) return route.fulfill({ json: { readSeq: messages.length } })
    if (path.endsWith('/messages')) {
      if (route.request().method() === 'GET') return route.fulfill({ json: { messages, hasMore: false } })
      const body = route.request().postDataJSON()
      attempts.push(body)
      if (attempts.length === 1) return route.fulfill({ status: 500, json: { error: { code: 'internal_error' } } })
      if (attempts.length === 2) await retryResponse
      const seq = messages.length + 1
      const message: Message = { ...body, id: `message-${seq}`, groupId: 'group-1', seq, senderType: 'user', senderId: me.id, senderName: me.nickname, replyToId: null, createdAt: new Date().toISOString() }
      messages.push(message)
      return route.fulfill({ json: message })
    }
    return route.fulfill({ json: { id: 'group-1', members: [me, peer], lastSeq: messages.length, readSeq: 0, unreadCount: 0, assistantName: 'AI', aiEnabled: false } })
  })
  await page.goto('/page/chat/group-1/')
  await page.getByLabel('消息内容').fill('HTTP 部署发送测试')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByRole('button', { name: '重试发送' })).toBeVisible()
  expect(attempts).toHaveLength(1)
  expect(attempts[0].clientMessageId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  await page.getByRole('button', { name: '重试发送' }).click()
  const input = page.getByLabel('消息内容')
  try {
    await expect(page.getByRole('button', { name: '发送中…', exact: true })).toBeVisible()
    await expect(input).toBeEditable()
    await input.click()
    await expect(input).toBeFocused()
    await input.fill('下一条草稿')
    await input.press('Enter')
    expect(attempts).toHaveLength(2)
  } finally { finishRetry() }
  await expect(page.getByText('HTTP 部署发送测试', { exact: true })).toBeVisible()
  await expect(input).toHaveValue('下一条草稿')
  await expect(input).toBeFocused()
  expect(attempts).toHaveLength(2)
  expect(attempts[1].clientMessageId).toBe(attempts[0].clientMessageId)
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByText('下一条草稿', { exact: true })).toBeVisible()
  await expect(input).toHaveValue('')
  expect(attempts).toHaveLength(3)
  expect(browserErrors).toEqual([])
})

test('AI failures show a status and small retry beside the explicit mention', async ({ page }) => {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  const messages = [true, false].map((mentionsAI, index) => ({ id: `m${index}`, groupId: 'g1', seq: index + 1, senderType: 'user', senderId: me.id, senderName: me.nickname, content: mentionsAI ? '帮我想想' : '普通聊天', mentionsAI, createdAt: new Date().toISOString() }))
  const runs = messages.map(message => ({ id: `r${message.id}`, triggerMessageId: message.id, requestedBy: me.id, status: 'failed', errorCode: 'ai_unavailable' }))
  let attempts = 0
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/groups') return route.fulfill({ json: [] })
    if (path.endsWith('/retry')) {
      attempts++
      if (attempts === 1) return route.fulfill({ status: 500, json: { error: { code: 'internal_error' } } })
      runs[0].status = 'running'
      return route.fulfill({ json: { ok: true } })
    }
    if (path.endsWith('/runs')) return route.fulfill({ json: runs })
    if (path.endsWith('/messages')) return route.fulfill({ json: { messages, hasMore: false } })
    if (path.endsWith('/read')) return route.fulfill({ json: { readSeq: 2 } })
    return route.fulfill({ json: { id: 'g1', members: [me], assistantName: 'AI', aiEnabled: true, readSeq: 0 } })
  })
  await page.goto('/page/chat/g1/')
  const question = page.locator('[data-message-id="m0"]')
  const retry = question.getByRole('button', { name: '重试 AI 回复' })
  await expect(retry).toBeVisible()
  await expect(question.locator('.message-ai-label').getByRole('button', { name: '查看失败原因' })).toHaveText('回复失败')
  await expect(question.locator('.message-ai-label').getByRole('button', { name: '重试 AI 回复' })).toBeVisible()
  await expect(page.getByRole('button', { name: '重试 AI 回复' })).toHaveCount(1)
  await expect(page.getByText('AI 回复失败', { exact: false })).toHaveCount(0)
  expect(await retry.evaluate(node => getComputedStyle(node).fontSize)).toBe('11px')
  const failure = question.getByRole('button', { name: '查看失败原因' })
  const details = page.getByRole('dialog', { name: 'AI 回复失败' })
  await failure.click()
  await expect(details).toBeVisible()
  await expect(details).toContainText('AI 服务暂未启用，请稍后重试。')
  expect(attempts).toBe(0)
  await details.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(details).toHaveCount(0)
  await expect(failure).toBeFocused()
  runs[0].errorCode = 'invalid_model_response'
  await page.reload()
  await failure.click()
  await expect(details).toContainText('AI 返回的内容未通过校验，请重试。')
  await page.keyboard.press('Escape')
  await expect(details).toHaveCount(0)
  await retry.click()
  await expect(question.getByRole('alert')).toBeVisible()
  await expect(page.locator('.message-composer').getByRole('alert')).toHaveCount(0)
  await retry.click()
  await expect(retry).toHaveCount(0)
  await expect(page.getByText('AI 正在回复…')).toBeVisible()
})
