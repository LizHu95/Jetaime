import { useEffect, useState } from 'react'
import { Link, Navigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/features/auth/auth-provider'
import { apiRequest, ApiError, errorMessage } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { Session } from '@/features/auth/schema'

type User = Session['user']
type ContactRequest = { id: string; from: User; to: User; status: string; groupId: string | null; createdAt: string }
type Group = { id: string; peer: User; status: string; assistantId: string; assistantName: string; unreadCount: number }
const statusLabel: Record<string, string> = { pending: '等待确认', accepted: '已接受', rejected: '已拒绝', cancelled: '已取消' }

export function ContactsPage() {
  const { session, setSession } = useAuth()
  const client = useQueryClient()
  const [username, setUsername] = useState('')
  const [found, setFound] = useState<User | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const token = session?.token
  const requests = useQuery({ queryKey: ['contacts', session?.user.id], queryFn: ({ signal }) => apiRequest<ContactRequest[]>('/contacts/requests', { method: 'GET', token, signal }), enabled: !!session, refetchInterval: 10_000 })
  const groups = useQuery({ queryKey: ['groups', session?.user.id], queryFn: ({ signal }) => apiRequest<Group[]>('/groups', { method: 'GET', token, signal }), enabled: !!session, refetchInterval: 10_000 })
  useEffect(() => { document.title = '好友与群组 · Jetaime' }, [])
  useEffect(() => {
    if ([requests.error, groups.error].some(e => e instanceof ApiError && e.code === 'unauthorized')) setSession(null)
  }, [requests.error, groups.error, setSession])
  if (!session) return <Navigate to="/auth/login/" replace />
  async function act(key: string, operation: () => Promise<unknown>, message?: string) {
    if (busy) return
    setBusy(key); setError(''); setNotice('')
    try {
      await operation()
      if (message) {
        setNotice(message)
        await Promise.all([client.invalidateQueries({ queryKey: ['contacts'] }), client.invalidateQueries({ queryKey: ['groups'] })])
      }
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
      else setError(errorMessage(reason))
    } finally { setBusy('') }
  }
  function resolve(request: ContactRequest, action: string) {
    void act(request.id, () => apiRequest(`/contacts/requests/${request.id}/${action}`, { token }), action === 'accept' ? '已接受，双人群和共享 AI 已创建。' : action === 'cancel' ? '已取消申请。' : '已拒绝申请。')
  }
  return <div className="auth-card contacts-card">
    <h2>好友与群组</h2><p className="form-description">找到对方账号，确认添加后一起开启双人群。</p>
    <form className="account-form" onSubmit={e => { e.preventDefault(); setFound(null); void act('search', async () => setFound(await apiRequest<User>(`/contacts/users?username=${encodeURIComponent(username.trim())}`, { method: 'GET', token }))) }}>
      <label htmlFor="friendUsername">对方账号（区分大小写）</label><div className="account-actions"><Input id="friendUsername" value={username} onChange={e => { setUsername(e.target.value); setFound(null) }} pattern="[A-Za-z0-9_]{2,24}" required disabled={!!busy} placeholder="输入完整账号" /><Button disabled={!!busy}>{busy === 'search' ? '查找中…' : '查找'}</Button></div>
    </form>
    {found && <div className="contact-row"><div><strong>{found.nickname}</strong><small>{found.username}</small></div>{found.id === session.user.id ? <span>这是你自己</span> : <Button disabled={!!busy} onClick={() => { void act('send', async () => {
      const r = await apiRequest<ContactRequest>('/contacts/requests', { token, body: { username: found.username } })
      if (r.to.id === session.user.id) setFound(null)
    }, '申请已记录，请在下方查看；收到对方申请时可直接接受。') }}>添加好友</Button>}</div>}
    {error && <p role="alert" className="account-error">{error}</p>}{notice && <p role="status" className="account-notice">{notice}</p>}
    <section aria-labelledby="requestTitle"><h3 id="requestTitle">添加申请</h3>
      {requests.isPending ? <p role="status">正在加载申请…</p> : requests.isError ? <p role="alert">{errorMessage(requests.error)} <button onClick={() => { void requests.refetch() }}>重试</button></p> : requests.data?.length === 0 ? <p className="form-description">暂无申请。</p> : requests.data?.map(r => {
        const incoming = r.to.id === session.user.id
        const peer = incoming ? r.from : r.to
        return <div className="contact-row" key={r.id}><div><strong>{peer.nickname}</strong><small>{peer.username} · {incoming ? '收到的申请' : '发出的申请'} · {statusLabel[r.status]}</small></div>{r.status === 'pending' && <div className="account-actions">{incoming ? <><Button disabled={!!busy} onClick={() => resolve(r, 'accept')}>接受并创建群</Button><Button variant="outline" disabled={!!busy} onClick={() => resolve(r, 'reject')}>拒绝</Button></> : <Button variant="outline" disabled={!!busy} onClick={() => resolve(r, 'cancel')}>取消</Button>}</div>}</div>
      })}
    </section>
    <section aria-labelledby="groupTitle"><h3 id="groupTitle">我的群组</h3>
      {groups.isPending ? <p role="status">正在加载群组…</p> : groups.isError ? <p role="alert">{errorMessage(groups.error)} <button onClick={() => { void groups.refetch() }}>重试</button></p> : groups.data?.length === 0 ? <p className="form-description">还没有群组。对方接受申请后，群组会显示在这里。</p> : groups.data?.map(g => <div className="contact-row" key={g.id}><div><strong>与 {g.peer.nickname} 的群</strong><small>{g.peer.username} · 两位成员 + {g.assistantName}</small></div><Link className="account-link" to={`/chat/${g.id}/`}>进入群聊{g.unreadCount > 0 ? ` · ${g.unreadCount} 条未读` : ''}</Link></div>)}
    </section><Link className="account-link" to="/auth/session/">返回我的账号</Link>
  </div>
}
