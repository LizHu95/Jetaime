import { useEffect, useState } from 'react'
import { Link, Navigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Search } from 'lucide-react'
import { useAuth } from '@/features/auth/auth-provider'
import { apiRequest, ApiError, errorMessage } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { Session } from '@/features/auth/schema'

type User = Session['user']
type ContactRequest = { id: string; from: User; to: User; status: string; groupId: string | null; createdAt: string }
const statusLabel: Record<string, string> = { accepted: '已添加', rejected: '已拒绝', cancelled: '已取消' }

function ContactIdentity({ user }: { user: User }) {
  const name = user.nickname || user.username
  return <div className="contact-identity"><span className="chat-avatar" aria-hidden="true">{Array.from(name)[0]}</span><div><strong>{name}</strong><small>{user.username}</small></div></div>
}

export function ContactsPage() {
  const { session, setSession } = useAuth()
  const client = useQueryClient()
  const [username, setUsername] = useState('')
  const [found, setFound] = useState<User | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [acceptedGroup, setAcceptedGroup] = useState<string | null>(null)
  const token = session?.token
  const queryKey = ['contacts', session?.user.id]
  const requests = useQuery({ queryKey, queryFn: ({ signal }) => apiRequest<ContactRequest[]>('/contacts/requests', { method: 'GET', token, signal }), enabled: !!session, refetchInterval: 10_000 })
  useEffect(() => { document.title = '加好友 · Jetaime' }, [])
  useEffect(() => {
    if (requests.error instanceof ApiError && requests.error.code === 'unauthorized') setSession(null)
  }, [requests.error, setSession])
  if (!session) return <Navigate to="/auth/login/" replace />

  async function act(key: string, operation: () => Promise<void>) {
    if (busy) return
    setBusy(key); setError(''); setNotice(''); setAcceptedGroup(null)
    try { await operation() }
    catch (reason) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
      else setError(errorMessage(reason))
    } finally { setBusy('') }
  }
  async function saveRequest(result: ContactRequest) {
    client.setQueryData<ContactRequest[]>(queryKey, current => [result, ...(current || []).filter(r => r.id !== result.id)])
    await Promise.all([client.invalidateQueries({ queryKey: ['contacts'] }), client.invalidateQueries({ queryKey: ['groups'] })])
  }
  function resolve(request: ContactRequest, action: 'accept' | 'reject' | 'cancel') {
    void act(request.id, async () => {
      const result = await apiRequest<ContactRequest>(`/contacts/requests/${request.id}/${action}`, { token })
      setNotice(action === 'accept' ? '已添加好友。' : action === 'cancel' ? '已取消申请。' : '已拒绝申请。')
      if (action === 'accept') {
        setAcceptedGroup(result.groupId)
        if (found && (found.id === result.from.id || found.id === result.to.id)) setFound(null)
      }
      await saveRequest(result)
    })
  }
  const pending = (requests.data || []).filter(r => r.status === 'pending')
  const incoming = pending.filter(r => r.to.id === session.user.id)
  const outgoing = pending.filter(r => r.from.id === session.user.id)
  const history = (requests.data || []).filter(r => r.status !== 'pending')
  const foundRequest = found && requests.data?.find(r => (r.from.id === found.id || r.to.id === found.id) && (r.status === 'pending' || r.status === 'accepted'))
  function requestRow(r: ContactRequest) {
    const received = r.to.id === session!.user.id
    const peer = received ? r.from : r.to
    return <div className="contact-row" key={r.id}>
      <ContactIdentity user={peer} />
      <div className="contact-request-actions">
        {r.status === 'pending' ? received ? <div className="account-actions"><Button disabled={!!busy} onClick={() => resolve(r, 'accept')}>接受</Button><Button variant="outline" disabled={!!busy} onClick={() => resolve(r, 'reject')}>拒绝</Button></div>
          : <><small>等待对方接受</small><Button variant="outline" disabled={!!busy} onClick={() => resolve(r, 'cancel')}>取消</Button></>
          : <><small>{statusLabel[r.status] || r.status}</small>{r.status === 'accepted' && r.groupId && <Link className="contact-chat-link" to={`/chat/${r.groupId}/`}>去聊天</Link>}</>}
      </div>
    </div>
  }
  return <main className="contacts-page">
    <header className="contacts-header"><Link to="/chat/" aria-label="返回聊天"><ArrowLeft size={22} /></Link><h1>加好友</h1></header>
    <form className="contact-search" onSubmit={e => { e.preventDefault(); void act('search', async () => {
      setFound(null)
      setFound(await apiRequest<User>(`/contacts/users?username=${encodeURIComponent(username.trim())}`, { method: 'GET', token }))
    }) }}>
      <label htmlFor="friendUsername">对方账号（区分大小写）</label>
      <div className="account-actions"><div className="contact-search-input"><Search size={18} aria-hidden="true" /><Input id="friendUsername" value={username} onChange={e => { setUsername(e.target.value); setFound(null); setError(''); setNotice(''); setAcceptedGroup(null) }} pattern="[A-Za-z0-9_]{2,24}" required disabled={!!busy} autoCapitalize="none" autoCorrect="off" spellCheck={false} placeholder="输入完整账号" /></div><Button disabled={!!busy}>{busy === 'search' ? '查找中…' : '查找'}</Button></div>
    </form>
    {found && <div className="contact-row contact-search-result">
      <ContactIdentity user={found} />
      {found.id === session.user.id ? <small>这是你自己</small>
        : foundRequest?.status === 'accepted' ? foundRequest.groupId ? <Link className="contact-chat-link" to={`/chat/${foundRequest.groupId}/`}>去聊天</Link> : <small>已添加</small>
        : foundRequest?.status === 'pending' ? <small>{foundRequest.to.id === session.user.id ? '对方已申请，请在下方处理' : '等待对方接受'}</small>
        : <Button disabled={!!busy} onClick={() => { void act('send', async () => {
          const result = await apiRequest<ContactRequest>('/contacts/requests', { token, body: { username: found.username } })
          setFound(null)
          setNotice(result.status === 'accepted' ? '已添加好友。' : result.to.id === session.user.id ? '对方已申请，请在下方处理。' : '申请已发送，等待对方接受。')
          if (result.status === 'accepted') setAcceptedGroup(result.groupId)
          await saveRequest(result)
        }) }}>添加好友</Button>}
    </div>}
    {error && <p role="alert" className="account-error">{error}</p>}
    {notice && <p role="status" className="account-notice">{notice}{acceptedGroup && <Link className="contact-chat-link" to={`/chat/${acceptedGroup}/`}>去聊天</Link>}</p>}
    <section className="contact-requests" aria-labelledby="requestTitle"><h2 id="requestTitle">好友申请{pending.length > 0 && <span>{pending.length}</span>}</h2>
      {requests.isPending ? <p role="status">正在加载申请…</p> : requests.isError ? <p role="alert">{errorMessage(requests.error)} <button onClick={() => { void requests.refetch() }}>重试</button></p>
        : <>{incoming.length > 0 && <section aria-label="收到的申请"><h3>收到的申请</h3>{incoming.map(requestRow)}</section>}
          {outgoing.length > 0 && <section aria-label="发出的申请"><h3>发出的申请</h3>{outgoing.map(requestRow)}</section>}
          {pending.length === 0 && <p className="contact-empty">暂无待处理申请</p>}
          {history.length > 0 && <details className="contact-history"><summary>历史申请 · {history.length}</summary>{history.map(requestRow)}</details>}</>}
    </section>
  </main>
}
