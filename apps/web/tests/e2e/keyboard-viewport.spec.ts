import { expect, test } from '@playwright/test'

test('composer follows the keyboard viewport and tabs return after dismissal', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('jetaime.auth.session', JSON.stringify({
      user: { id: 'alice', username: 'Alice', nickname: '小林' },
      token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    }))
    // Mobile browser emulation does not open an OS keyboard. Reproduce iOS's
    // visual viewport shrinking and panning while the layout viewport stays put.
    const viewport = window.visualViewport!
    Object.defineProperties(viewport, {
      height: { value: window.innerHeight, writable: true, configurable: true },
      offsetTop: { value: 0, writable: true, configurable: true },
      scale: { value: 1, writable: true, configurable: true },
    })
  })
  await page.routeWebSocket('**/api/chat/events', socket => {
    socket.onMessage(() => socket.send(JSON.stringify({ type: 'ready' })))
  })
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/groups' || path.endsWith('/runs')) return route.fulfill({ json: [] })
    if (path.endsWith('/messages')) return route.fulfill({ json: { messages: [], hasMore: false } })
    return route.fulfill({ json: { id: 'group-1', members: [{ id: 'alice', nickname: '小林' }, { id: 'bob', nickname: '小夏' }], lastSeq: 0, readSeq: 0, assistantName: 'AI', aiEnabled: false } })
  })
  await page.goto('/page/chat/group-1/')
  const input = page.getByLabel('消息内容')
  const tabs = page.getByRole('navigation', { name: '主导航' })
  await expect(input).toBeVisible()
  await expect(tabs).toBeVisible()
  await input.click()
  // Focus and browser-toolbar movement alone must not hide navigation.
  await page.evaluate(() => {
    Object.assign(window.visualViewport!, { height: window.innerHeight - 60 })
    window.visualViewport!.dispatchEvent(new Event('resize'))
  })
  await expect(tabs).toBeVisible()
  await page.evaluate(() => {
    Object.assign(window.visualViewport!, { height: window.innerHeight - 320, offsetTop: 70 })
    window.visualViewport!.dispatchEvent(new Event('resize'))
  })
  await expect(tabs).toBeHidden()
  const fitsViewport = () => page.evaluate(() => {
    const viewport = window.visualViewport!
    const input = document.querySelector('#messageInput')!.getBoundingClientRect()
    const composer = document.querySelector('.message-composer')!.getBoundingClientRect()
    return input.top >= viewport.offsetTop && composer.bottom <= viewport.offsetTop + viewport.height + 1
  })
  await expect.poll(fitsViewport).toBe(true)
  await input.fill('键盘上方的草稿')
  await page.evaluate(() => {
    Object.assign(window.visualViewport!, { offsetTop: 110 })
    window.visualViewport!.dispatchEvent(new Event('scroll'))
  })
  await expect.poll(fitsViewport).toBe(true)
  // Dismissal may leave the textarea focused (the iOS keyboard Done button).
  await page.evaluate(() => {
    Object.assign(window.visualViewport!, { height: window.innerHeight, offsetTop: 0 })
    window.visualViewport!.dispatchEvent(new Event('resize'))
  })
  await expect(tabs).toBeVisible()
  await expect(page.locator('.main-app')).not.toHaveClass(/keyboard-open/)
  await expect(input).toHaveValue('键盘上方的草稿')
})
