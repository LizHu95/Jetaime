import { useEffect, useState } from 'react'
import { Link, Navigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, Check, Clock } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { SubpageLayout } from '@/components/subpage-layout'
import { useAuth } from '@/features/auth/auth-provider'
import { apiRequest, ApiError, errorMessage } from '@/lib/api'
import { LocationPermissionHelp } from './location-permission-help'
import { getCurrentLocation, saveLocationPreference } from './location'
import { useLocationPreference } from './use-location-preference'
import type { ChatState } from './types'

type Reminder = {
  id: string; userId: string; userName: string; title: string
  dueAt: string | null; timezone: string; status: 'pending' | 'sent' | 'cancelled'
}
const statuses = { pending: '待提醒', sent: '已提醒', cancelled: '已取消' } as const

export function ReminderSettings() {
  const { groupId } = useParams()
  const { session, setSession } = useAuth()
  const client = useQueryClient()
  const autoLocation = useLocationPreference(session?.user.id || '')
  const [locating, setLocating] = useState(false)
  const [locationError, setLocationError] = useState<Error | null>(null)
  const [filter, setFilter] = useState<Reminder['status']>('pending')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [savingSettings, setSavingSettings] = useState(false)
  const stateKey = ['chat', session?.user.id, groupId, 'state']
  const state = useQuery({ queryKey: stateKey, enabled: !!groupId && !!session, queryFn: ({ signal }) => apiRequest<ChatState>(`/chat/groups/${groupId}`, { method: 'GET', token: session?.token, signal }), refetchOnWindowFocus: true, refetchInterval: 5000 })
  const key = ['chat', session?.user.id, groupId, 'reminders']
  const base = `/chat/groups/${groupId}/reminders`
  const reminders = useQuery({ queryKey: key, enabled: !!groupId && !!session, queryFn: ({ signal }) => apiRequest<Reminder[]>(base, { method: 'GET', token: session?.token, signal }) })
  useEffect(() => {
    if ((state.error instanceof ApiError && state.error.code === 'unauthorized') || (reminders.error instanceof ApiError && reminders.error.code === 'unauthorized')) setSession(null)
  }, [state.error, reminders.error, setSession])
  async function toggleProactive() {
    if (!state.data || savingSettings) return
    setSavingSettings(true); setError('')
    try {
      const updated = await apiRequest<ChatState>(`/chat/groups/${groupId}/settings`, { token: session?.token, body: { proactiveEnabled: !state.data.proactiveEnabled } })
      client.setQueryData(stateKey, updated)
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
      else setError(errorMessage(reason))
    } finally { setSavingSettings(false) }
  }
  async function toggleLocation() {
    if (!session || locating) return
    setLocationError(null)
    if (autoLocation) { saveLocationPreference(session.user.id, false); return }
    setLocating(true)
    try {
      await getCurrentLocation()
      saveLocationPreference(session.user.id, true)
    } catch (reason) {
      saveLocationPreference(session.user.id, false)
      setLocationError(reason instanceof Error ? reason : new Error('暂时无法获取位置，请重试或手动输入地点。'))
    } finally { setLocating(false) }
  }
  async function cancel(id: string) {
    setBusy(id); setError('')
    try {
      await apiRequest(`${base}/${id}/cancel`, { token: session?.token })
      await client.invalidateQueries({ queryKey: key })
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
      else setError(errorMessage(reason))
    } finally { setBusy('') }
  }
  if (!session) return <Navigate to="/auth/login/" replace state={{ returnTo: `/chat/${groupId}/settings/` }} />
  return <SubpageLayout as="section" className="reminder-settings" title="群聊配置" subtitle="管理 AI 主动参与、定位与本群提醒。" backTo={`/chat/${groupId}/`} backLabel="返回聊天">
        <div className="reminder-card proactive-setting">
          <div className="reminder-card-heading"><h3>AI 主动参与</h3><Button variant="outline" type="button" role="switch" aria-label="AI 主动参与" aria-checked={state.data?.proactiveEnabled ?? false} disabled={!state.data || savingSettings || !state.data.aiEnabled} onClick={() => { void toggleProactive() }}>{savingSettings ? '保存中…' : state.data?.proactiveEnabled ? '已开启' : '已关闭'}</Button></div>
          <p>开启后，大家停止发言 5 秒，AI 会结合最近 30 条对话，在需要帮助时参与。@AI 始终可用。</p>
          {state.isError && <p role="alert">{errorMessage(state.error)} <button onClick={() => { void state.refetch() }}>重试</button></p>}
        </div>
        <div className="reminder-card proactive-setting location-setting">
          <div className="reminder-card-heading"><h3>发送消息时使用位置</h3><Button variant="outline" type="button" role="switch" aria-label="发送消息时使用位置" aria-checked={autoLocation} disabled={locating} onClick={() => { void toggleLocation() }}>{locating ? '正在授权…' : autoLocation ? '已开启' : '已关闭'}</Button></div>
          <p>开启后，每条消息发送前都重新获取并附带当前位置，包括普通群聊，不复用上次定位。明确指定出发地时，AI 优先使用指定地点。仅对当前账号在此设备生效，适用于所有群聊。</p>
          <p>首次开启需要允许网站和系统访问位置；关闭后不再获取或附带位置，也可在聊天中手动填写地点。</p>
          {locationError && <LocationPermissionHelp error={locationError} busy={locating} onRetry={() => { void toggleLocation() }} />}
        </div>
        <div className="reminder-intro"><span className="reminder-icon"><Bell size={22} /></span><div><h3>提醒列表</h3><p>到点后，AI 会在群里提醒一次。时间均为北京时间。</p></div></div>
        <div className="reminder-filters" role="group" aria-label="提醒状态">{(Object.keys(statuses) as Reminder['status'][]).map(status => <button key={status} aria-pressed={filter === status} onClick={() => setFilter(status)}>{statuses[status]}{reminders.data && <small>{reminders.data.filter(item => item.status === status).length}</small>}</button>)}</div>
        {error && <p role="alert" className="account-error">{error}</p>}
        <div className="reminder-list" aria-live="polite">
          {reminders.isPending ? <p role="status">正在加载提醒…</p> : reminders.isError ? <p role="alert">{errorMessage(reminders.error)} <button onClick={() => { void reminders.refetch() }}>重试</button></p> : reminders.data?.filter(item => item.status === filter).length === 0 ? <div className="reminder-empty"><Clock size={32} /><h4>{filter === 'pending' ? '还没有待提醒事项' : `暂无${statuses[filter]}记录`}</h4><p>在群里 @AI 说「提醒我吃饭」，再补充日期和几点。时间确认后才会出现在这里。</p><Button asChild variant="outline"><Link to={`/chat/${groupId}/`}>回到聊天</Link></Button></div> : reminders.data?.filter(item => item.status === filter).map(item => <article className="reminder-card" key={item.id}>
            <div className="reminder-card-heading"><h4>{item.title}</h4><span>{item.status === 'sent' ? <Check size={14} /> : <Clock size={14} />}{statuses[item.status]}</span></div>
            <time dateTime={item.dueAt || undefined}>{item.dueAt ? new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(item.dueAt)) : '时间未补全'}</time>
            <footer><small>{item.userName} 的提醒 · 仅一次</small>{item.status === 'pending' && item.userId === session?.user.id && <Button variant="outline" disabled={!!busy} onClick={() => { void cancel(item.id) }}>{busy === item.id ? '取消中…' : '取消提醒'}</Button>}</footer>
          </article>)}
        </div>
  </SubpageLayout>
}
