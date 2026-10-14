import { describe, expect, it } from 'vitest'
import { mergeMessages, type Message } from '../../src/features/chat/types'
describe('message reconciliation', () => {
  it('deduplicates HTTP retries and websocket catchup, preserving sequence order', () => {
    const message = (id: string, seq: number, content = id) => ({ id, seq, content } as Message)
    expect(mergeMessages([message('b', 2), message('c', 3)], [message('a', 1), message('b', 2, 'updated')]).map(m => [m.id, m.content])).toEqual([['a', 'a'], ['b', 'updated'], ['c', 'c']])
  })
})
