import { useEffect, useState } from 'react'
import { Link, Navigate } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/features/auth/auth-provider'
import { ApiError, apiRequest, errorMessage } from '@/lib/api'
import { bindSubscription, disablePush, enablePush, pushEnvironment, pushSupported, registerPushWorker } from './push'

export function NotificationsPage() {
  const { session } = useAuth()
  const [subscribed, setSubscribed] = useState(false)
  const [ready, setReady] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const supported = pushSupported()
  const environment = pushEnvironment()
  const needsInstall = environment.ios && !environment.standalone
  const insecure = !environment.secure
  useEffect(() => {
    document.title = '消息通知 · Jetaime'
    if (!session || !supported || needsInstall) { setReady(true); return }
    let stopped = false
    void registerPushWorker().then(async registration => {
      const subscription = await registration.pushManager.getSubscription()
      if (subscription && Notification.permission === 'granted') {
        await bindSubscription(session.token, subscription)
        if (!stopped) setSubscribed(true)
      }
    }).catch(error => { if (!stopped) setMessage(errorMessage(error)) })
      .finally(() => { if (!stopped) setReady(true) })
    return () => { stopped = true }
  }, [session, supported, needsInstall])
  if (!session) return <Navigate to="/auth/login/" replace />
  async function perform(action: 'enable' | 'disable' | 'test') {
    setBusy(true); setMessage('')
    try {
      if (action === 'enable') { await enablePush(session!.token); setSubscribed(true); setMessage('通知已开启，可以发送一条测试通知。') }
      else if (action === 'disable') { await disablePush(session!.token); setSubscribed(false); setMessage('此设备通知已关闭。') }
      else {
        const subscription = await (await registerPushWorker()).pushManager.getSubscription()
        if (!subscription) throw new Error('请先开启通知。')
        await apiRequest('/push/test', { token: session!.token, body: { endpoint: subscription.endpoint } })
        setMessage('测试通知已提交。请切到后台或锁屏查看，连续测试需间隔 30 秒。')
      }
    } catch (error) { setMessage(error instanceof ApiError ? errorMessage(error) : error instanceof Error ? error.message : errorMessage(error)) }
    finally { setBusy(false) }
  }
  return <div className="auth-card">
    <h2>消息通知</h2>
    <p className="form-description">收到聊天消息或 AI 回复时提醒你，通知不显示聊天正文。登录过期或退出后停止推送。</p>
    {insecure ? <div role="status"><p>当前使用的是非安全地址，无法开启通知。原来的桌面图标可能仍保存着 HTTP 地址。</p><p>请用 Safari 打开下方 HTTPS 地址，再重新添加到主屏幕，并从新图标进入。</p><a className="account-link" href={`https://${location.host}/page/notifications/`}>打开 HTTPS 通知设置</a></div>
      : needsInstall ? <p role="status">请先用 Safari 的分享菜单“添加到主屏幕”，再从桌面图标打开并登录，开启通知。若有“作为网页 App 打开”选项，请开启。</p>
      : !supported ? <p role="status">当前系统没有提供通知所需的能力。iPhone 需要 iOS 16.4 或更新版本；如果已满足版本要求，请展开下方检测结果，以便定位原因。</p>
      : <div className="account-form">
        <p role="status">{!ready ? '正在检查通知状态…' : subscribed ? '此设备通知已开启' : '此设备通知未开启'}</p>
        <Button disabled={busy || !ready} onClick={() => { void perform(subscribed ? 'disable' : 'enable') }}>{busy ? '正在处理…' : subscribed ? '关闭此设备通知' : '开启通知'}</Button>
        {subscribed && <Button variant="outline" disabled={busy || !ready} onClick={() => { void perform('test') }}>发送测试通知</Button>}
      </div>}
    {(!supported || needsInstall) && <details className="form-description"><summary>查看通知环境检测</summary><p>地址：{environment.origin}</p><p>安全连接：{environment.secure ? '是' : '否'}；桌面 App 模式：{environment.standalone ? '是' : '否'}</p><p>后台处理：{environment.serviceWorker ? '可用' : '不可用'}；推送：{environment.pushManager ? '可用' : '不可用'}；通知：{environment.notification ? '可用' : '不可用'}</p></details>}
    {message && <p role="status" className="form-description">{message}</p>}
    <Link className="account-link" to="/auth/session/">返回我的账号</Link>
  </div>
}
