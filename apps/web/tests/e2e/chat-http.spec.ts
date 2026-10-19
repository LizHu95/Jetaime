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
