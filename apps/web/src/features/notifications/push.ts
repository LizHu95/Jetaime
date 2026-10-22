import { apiRequest } from '@/lib/api'

let worker: Promise<ServiceWorkerRegistration> | undefined
export function pushEnvironment() {
  const standalone = matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone === true
  const ios = /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  return {
    origin: location.origin,
    secure: window.isSecureContext,
    standalone,
    ios,
    serviceWorker: 'serviceWorker' in navigator,
    pushManager: 'PushManager' in window,
    notification: 'Notification' in window,
    userAgent: navigator.userAgent
  }
}
export function pushSupported() {
  return window.isSecureContext && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}
export function registerPushWorker() {
  if (!pushSupported()) return Promise.reject(new Error('此环境暂不支持通知，请通过 HTTPS 在主屏幕网页 App 中打开。'))
  worker ??= navigator.serviceWorker.register('/page/sw.js', { scope: '/page/', updateViaCache: 'none' })
    .then(() => navigator.serviceWorker.ready).catch(error => { worker = undefined; throw error })
  return worker
}
function keyBytes(value: string) {
  const raw = atob(value.replace(/-/g, '+').replace(/_/g, '/'))
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
  return bytes
}
export async function bindSubscription(token: string, subscription: PushSubscription) {
  await apiRequest('/push/subscriptions', { token, body: subscription.toJSON() })
}
export async function enablePush(token: string) {
  // Permission must be requested synchronously from the user's button handler.
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') throw new Error('未获得通知权限，请在 iPhone 设置中允许 Jetaime 通知。')
  const config = await apiRequest<{ enabled: boolean; publicKey: string }>('/push/public-key', { method: 'GET' })
  if (!config.enabled) throw new Error('服务器尚未开启通知。')
  const registration = await registerPushWorker()
  let subscription = await registration.pushManager.getSubscription()
  const publicKey = keyBytes(config.publicKey)
  if (subscription?.options.applicationServerKey && new Uint8Array(subscription.options.applicationServerKey).toString() !== publicKey.toString()) {
    if (!await subscription.unsubscribe()) throw new Error('旧通知订阅未能解除，请重试。')
    subscription = null
  }
  subscription ??= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: publicKey })
  await bindSubscription(token, subscription)
  return subscription
}
export async function disablePush(token: string) {
  const registration = await registerPushWorker()
  const subscription = await registration.pushManager.getSubscription()
  if (subscription) {
    await apiRequest('/push/subscriptions', { method: 'DELETE', token, body: { endpoint: subscription.endpoint } })
    if (!await subscription.unsubscribe()) throw new Error('服务器已关闭通知，但设备订阅未能解除，请重试。')
  }
  for (const notification of await registration.getNotifications()) notification.close()
}
