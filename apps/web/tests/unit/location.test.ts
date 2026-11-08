import { describe, expect, it } from 'vitest'
import { freshLocation, type ClientLocation } from '../../src/features/chat/location'

describe('client location', () => {
  const now = Date.now()
  const fix: ClientLocation = { longitude: 121.5, latitude: 31.3, accuracyMeters: 50, capturedAt: new Date(now).toISOString(), coordinateSystem: 'wgs84' }
  it('rejects stale, future, inaccurate and invalid positions', () => {
    expect(freshLocation(fix, now)).toBe(true)
    expect(freshLocation(fix, now + 300_001)).toBe(false)
    expect(freshLocation(fix, now - 30_001)).toBe(false)
    expect(freshLocation({ ...fix, accuracyMeters: 1500 }, now)).toBe(false)
    expect(freshLocation({ ...fix, longitude: NaN }, now)).toBe(false)
    expect(freshLocation(null, now)).toBe(false)
  })
})
