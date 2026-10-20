import { useEffect, useState } from 'react'
import { Link, Navigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, Check, Clock } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { NavBar } from '@/components/nav-bar'
import { useAuth } from '@/features/auth/auth-provider'
import { apiRequest, ApiError, errorMessage } from '@/lib/api'

type Reminder = {
  id: string; userId: string; userName: string; title: string
  dueAt: string | null; timezone: string; status: 'pending' | 'sent' | 'cancelled'
}
const statuses = { pending: '待提醒', sent: '已提醒', cancelled: '已取消' } as const

export function ReminderSettings() {
  const { groupId } = useParams()
  const { session, setSession } = useAuth()
  const client = useQueryClient()
  const [filter, setFilter] = useState<Reminder['status']>('pending')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const key = ['chat', session?.user.id, groupId, 'reminders']
  const base = `/chat/groups/${groupId}/reminders`
  const reminders = useQuery({ queryKey: key, enabled: !!groupId && !!session, queryFn: ({ signal }) => apiRequest<Reminder[]>(base, { method: 'GET', token: session?.token, signal }) })
  useEffect(() => {
    if (reminders.error instanceof ApiError && reminders.error.code === 'unauthorized') setSession(null)
  }, [reminders.error, setSession])
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
  return <section className="reminder-settings" aria-labelledby="settings-title">
        <NavBar title="群聊配置" titleId="settings-title" subtitle="在这里查看本群的一次性提醒。" backTo={`/chat/${groupId}/`} backLabel="返回聊天" />
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
  </section>
}
