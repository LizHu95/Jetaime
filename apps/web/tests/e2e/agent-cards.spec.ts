import { expect, test } from '@playwright/test'
import type { Message, AgentCard } from '../../src/features/chat/types'

const cards: AgentCard[] = [
  { id: 'amap:place:B001', type: 'place', title: '大学路粥铺', source: 'amap', reason: '可以先看看这家，具体少油少盐要求可向店家确认。', place: { name: '大学路粥铺', address: '大学路 88 号', city: '上海市', costPerPerson: 45, rating: 4.5, openingHours: '10:00–21:00', mapUrl: 'https://uri.amap.com/marker?poiid=B001' } },
  { id: 'amap:place:B002', type: 'place', title: '街角小馆', source: 'amap', reason: '另一个可选去处。', place: { name: '街角小馆', address: '政民路 18 号', city: '上海市', costPerPerson: null, rating: null, mapUrl: 'javascript:alert(1)' } },
  { id: 'amap:route:one', type: 'route', title: '公交地铁方案 1', source: 'amap', reason: '乘坐地铁，中途无需换乘。', route: { mode: 'transit', distanceMeters: 2400, durationSeconds: 1080, walkingDistanceMeters: 550, instructions: ['步行到五角场站', '10号线：五角场 → 三门路', '步行到目的地'], mapUrl: 'https://uri.amap.com/navigation?from=121.5,31.3&to=121.51,31.31&mode=bus' } },
]

test('renders evidence cards, preserves missing fields and prepares an editable follow-up', async ({ page }, info) => {
  const me = { id: 'alice', username: 'Alice', nickname: '小林' }, peer = { id: 'bob', username: 'Bob', nickname: '小夏' }
  await page.addInitScript(session => localStorage.setItem('jetaime.auth.session', JSON.stringify(session)), { user: me, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString() })
  await page.routeWebSocket('**/api/chat/events', socket => { socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' }))) })
  const messages: Message[] = [
    { id: 'old', groupId: 'g1', seq: 1, senderType: 'assistant', senderId: null, senderName: 'AI', content: '这是一条旧版纯文字消息。', mentionsAI: false, clientMessageId: null, replyToId: null, createdAt: new Date().toISOString() },
    { id: 'answer', groupId: 'g1', seq: 2, senderType: 'assistant', senderId: null, senderName: 'AI', content: '可以从这两家里选，路线也放在下面。', mentionsAI: false, clientMessageId: null, replyToId: null, createdAt: new Date().toISOString(), agentResult: { kind: 'main', promptVersion: 'test', cards } },
  ]
  const sent: { content: string; mentionsAI: boolean; clientMessageId: string }[] = []
  await page.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (path === '/api/groups') return route.fulfill({ json: [] })
    if (path.endsWith('/runs')) return route.fulfill({ json: [] })
    if (path.endsWith('/read')) return route.fulfill({ json: { readSeq: messages.length } })
    if (path.endsWith('/messages')) {
      if (request.method() === 'POST') {
        const body = request.postDataJSON(); sent.push(body)
        const message: Message = { ...body, id: `sent-${sent.length}`, groupId: 'g1', seq: messages.length + 1, senderType: 'user', senderId: me.id, senderName: me.nickname, replyToId: null, createdAt: new Date().toISOString() }
        messages.push(message)
        return route.fulfill({ json: message })
      }
      const after = Number(new URL(request.url()).searchParams.get('afterSeq') || 0)
      return route.fulfill({ json: { messages: messages.filter(m => m.seq > after), hasMore: false } })
    }
    return route.fulfill({ json: { id: 'g1', members: [me, peer], assistantName: 'AI', aiEnabled: true, readSeq: 2, lastSeq: messages.length, unreadCount: 0 } })
  })
  await page.goto('/page/chat/g1/')
  const restaurant = page.getByRole('article', { name: '大学路粥铺推荐卡片' })
  await expect(restaurant).toContainText('人均 ¥45')
  await expect(restaurant).toContainText('评分 4.5')
  await expect(restaurant.getByRole('link', { name: '查看地图' })).toHaveAttribute('href', /^https:\/\/uri\.amap\.com\/marker/)
  const unknown = page.getByRole('article', { name: '街角小馆推荐卡片' })
  await expect(unknown).not.toContainText('人均')
  await expect(unknown).not.toContainText('评分')
  await expect(unknown.getByRole('link')).toHaveCount(0)
  const routeCard = page.getByRole('article', { name: '公交地铁路线卡片' })
  await expect(routeCard).toContainText('预计 18 分钟')
  await expect(routeCard).toContainText('2.4 公里')
  await routeCard.getByText('路线说明', { exact: true }).click()
  await expect(routeCard).toContainText('10号线：五角场 → 三门路')
  await page.reload()
  await expect(restaurant).toBeVisible()
  await expect(page.getByText('这是一条旧版纯文字消息。', { exact: true })).toBeAttached()
  const input = page.getByLabel('消息内容')
  await input.fill('我想补充：')
  await restaurant.getByRole('button', { name: '怎么去大学路粥铺' }).click()
  await expect(input).toBeFocused()
  await expect(input).toHaveValue(/^我想补充：\n怎么去「大学路粥铺」/)
  await expect(page.getByRole('button', { name: '@AI', exact: true })).toHaveAttribute('aria-pressed', 'true')
  expect(sent).toHaveLength(0)
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(input).toHaveValue('')
  expect(sent).toHaveLength(1)
  expect(sent[0].mentionsAI).toBe(true)
  expect(sent[0].content).toContain('上海市，大学路 88 号')
  await restaurant.scrollIntoViewIfNeeded()
  await page.screenshot({ path: `../../outputs/web-check/${info.project.name}-agent-cards.png`, fullPage: true })
  await page.setViewportSize({ width: 320, height: 568 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
