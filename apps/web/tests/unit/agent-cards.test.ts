import { describe, expect, it } from 'vitest'
import { formatDistance, formatDuration, routeQuestion, safeMapUrl } from '../../src/features/chat/agent-cards'
import type { PlaceCard } from '../../src/features/chat/types'

describe('Agent cards', () => {
  it('formats known metric units and keeps unknown values hidden', () => {
    expect(formatDistance(1800)).toBe('1.8 公里')
    expect(formatDistance(450)).toBe('450 米')
    expect(formatDuration(330)).toBe('6 分钟')
    expect(formatDuration(3660)).toBe('1 小时 1 分钟')
    for (const value of [null, undefined, NaN, Infinity, -1]) {
      expect(formatDistance(value)).toBeUndefined()
      expect(formatDuration(value)).toBeUndefined()
    }
  })
  it('allows only public Amap marker and navigation links', () => {
    expect(safeMapUrl('https://uri.amap.com/marker?poiid=B001')).toBeDefined()
    for (const url of ['javascript:alert(1)', 'https://evil.example/marker', 'https://uri.amap.com.evil.example/marker', 'https://user@uri.amap.com/marker', 'https://uri.amap.com/unknown', 'not a URL']) expect(safeMapUrl(url)).toBeUndefined()
  })
  it('fills a route question with place context without inventing an origin', () => {
    const card: PlaceCard = { id: 'amap:place:B001', type: 'place', title: '粥铺', source: 'amap', place: { name: '粥铺', address: '大学路1号', city: '上海', rating: null, costPerPerson: null, mapUrl: '' } }
    expect(routeQuestion(card)).toContain('上海，大学路1号')
    expect(routeQuestion(card)).toContain('还不知道我的出发地')
  })
})
