// Validation lifetime for request evidence, not a position cache.
export const LOCATION_EVIDENCE_MAX_AGE = 5 * 60 * 1000
export class LocationAccessError extends Error {
  constructor(message: string, readonly code: number, readonly nativeMessage: string) {
    super(message)
    this.name = 'LocationAccessError'
  }
}
export type ClientLocation = {
  longitude: number
  latitude: number
  accuracyMeters: number
  capturedAt: string
  coordinateSystem: 'wgs84'
}
export type SendMessageInput = {
  content: string
  clientMessageId: string
  mentionsAI: boolean
  clientContext?: { location: ClientLocation }
}
export function freshLocation(location: ClientLocation | null, now = Date.now(), maxAge = LOCATION_EVIDENCE_MAX_AGE): location is ClientLocation {
  if (!location) return false
  const age = now - Date.parse(location.capturedAt)
  return Number.isFinite(age) && age >= -30_000 && age <= maxAge &&
    Number.isFinite(location.longitude) && Math.abs(location.longitude) <= 180 &&
    Number.isFinite(location.latitude) && Math.abs(location.latitude) <= 90 &&
    Number.isFinite(location.accuracyMeters) && location.accuracyMeters > 0 && location.accuracyMeters <= 1000
}
export function getCurrentLocation(): Promise<ClientLocation> {
  if (!window.isSecureContext) return Promise.reject(new Error('定位需要 HTTPS，请手动输入出发地点。'))
  if (!navigator.geolocation) return Promise.reject(new Error('当前浏览器不支持定位，请手动输入出发地点。'))
  return new Promise((resolve, reject) => navigator.geolocation.getCurrentPosition(position => {
    const location: ClientLocation = {
      longitude: position.coords.longitude, latitude: position.coords.latitude,
      accuracyMeters: position.coords.accuracy, capturedAt: new Date(position.timestamp).toISOString(), coordinateSystem: 'wgs84',
    }
    if (!freshLocation(location)) reject(new Error('定位精度不足或已过期，请重新定位或手动输入地点。'))
    else resolve(location)
  }, error => {
    reject(new LocationAccessError(error.code === 1 ? '定位权限被拒绝。请在系统和浏览器的网站设置中允许位置访问，再重新开启；也可手动输入地点。' : error.code === 3 ? '定位超时，请重试或手动输入出发地点。' : '暂时无法获取位置，请手动输入出发地点。', error.code, error.message || '浏览器未提供原始说明'))
  }, { enableHighAccuracy: true, timeout: 10_000, maximumAge: 0 }))
}

const memoryPreferences = new Map<string, boolean>()
export function locationPreference(actor: string): boolean {
  try { return localStorage.getItem(`jetaime.location.auto.${actor}`) === 'true' } catch { return memoryPreferences.get(actor) ?? false }
}
export function saveLocationPreference(actor: string, enabled: boolean) {
  memoryPreferences.set(actor, enabled)
  try { localStorage.setItem(`jetaime.location.auto.${actor}`, String(enabled)) } catch { /* Keep the preference in memory when storage is unavailable. */ }
  window.dispatchEvent(new Event('jetaime:location-preference'))
}
