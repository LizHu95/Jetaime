import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { describe, expect, it, vi } from 'vitest'

function worker() {
  const listeners: Record<string, (event: any) => void> = {}
  const showNotification = vi.fn().mockResolvedValue(undefined)
  const openWindow = vi.fn().mockResolvedValue(undefined)
  runInNewContext(readFileSync('public/sw.js', 'utf8'), {
    URL,
    self: {
      location: { origin: 'https://example.com' },
      addEventListener: (type: string, listener: (event: any) => void) => { listeners[type] = listener },
      registration: { showNotification },
      clients: { matchAll: async () => [], openWindow }
    }
  })
  return { listeners, showNotification, openWindow }
}
describe('push service worker', () => {
  it('shows a notification even when a payload is malformed', async () => {
    const w = worker()
    let task: Promise<unknown> | undefined
    w.listeners.push({ data: { json: () => { throw new Error('bad JSON') } }, waitUntil: (p: Promise<unknown>) => { task = p } })
    await task
    expect(w.showNotification).toHaveBeenCalledWith('Jetaime', expect.objectContaining({ body: '你收到一条新消息' }))
  })
  it('never opens an external URL from notification data', async () => {
    const w = worker()
    let task: Promise<unknown> | undefined
    w.listeners.notificationclick({ notification: { close: vi.fn(), data: { url: 'https://attacker.test/' } }, waitUntil: (p: Promise<unknown>) => { task = p } })
    await task
    expect(w.openWindow).toHaveBeenCalledWith('https://example.com/page/chat/')
  })
  it('opens the correct group from a valid notification', async () => {
    const w = worker()
    const path = '/page/chat/12345678-1234-1234-1234-123456789abc/'
    let task: Promise<unknown> | undefined
    w.listeners.notificationclick({ notification: { close: vi.fn(), data: { url: path } }, waitUntil: (p: Promise<unknown>) => { task = p } })
    await task
    expect(w.openWindow).toHaveBeenCalledWith(`https://example.com${path}`)
  })
})
