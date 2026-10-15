import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Send, Sparkles, UserPlus } from 'lucide-react'
import { useAuth } from '@/features/auth/auth-provider'
import { Button } from '@/components/ui/button'
import { apiRequest, ApiError, errorMessage } from '@/lib/api'
import { createMessageId } from '@/lib/message-id'
import { useRealtime } from './realtime-provider'
import { mergeMessages, type ChatState, type Group, type Message, type MessagePage, type Run } from './types'

export function ChatPage() {
  const { session, setSession } = useAuth()
  const { groupId } = useParams()
  const connected = useRealtime()
  const groups = useQuery({ queryKey: ['groups', session?.user.id], queryFn: ({ signal }) => apiRequest<Group[]>('/groups', { method: 'GET', token: session?.token, signal }), enabled: !!session })
  useEffect(() => { document.title = '双人群聊 · Jetaime' }, [])
  useEffect(() => { if (groups.error instanceof ApiError && groups.error.code === 'unauthorized') setSession(null) }, [groups.error, setSession])
  if (!session) return <Navigate to="/auth/login/" replace state={{ returnTo: groupId ? `/chat/${groupId}/` : '/chat/' }} />
  return <main className={`chat-shell ${groupId ? 'chat-selected' : ''}`}>
    <aside className="chat-sidebar">
      <header><h1>聊天</h1><Link to="/contacts/" className="add-friend"><UserPlus size={19} />加好友</Link></header>
      <p className="chat-connection" role="status">{connected ? '实时消息已连接' : '正在连接，消息会自动补齐'}</p>
      {groups.isPending ? <p>正在加载…</p> : groups.isError ? <p role="alert">{errorMessage(groups.error)} <button onClick={() => { void groups.refetch() }}>重试</button></p> : groups.data?.length === 0 ? <p>还没有群聊。<Link to="/contacts/">添加好友</Link></p> : groups.data?.map(g => <Link to={`/chat/${g.id}/`} className={`chat-group ${g.id === groupId ? 'active' : ''}`} key={g.id}>
        <span className="chat-avatar">{g.peer.nickname.slice(0, 1)}</span><div><strong>{g.peer.nickname}</strong><small>{g.lastMessage || '开始一起聊聊吧'}</small></div>{g.unreadCount > 0 && <span className="unread-badge" aria-label={`${g.peer.nickname}：${g.unreadCount} 条未读`}>{g.unreadCount > 99 ? '99+' : g.unreadCount}</span>}
      </Link>)}
    </aside>
    {groupId ? <Conversation key={`${session.user.id}:${groupId}`} groupId={groupId} /> : null}
  </main>
}

function Conversation({ groupId }: { groupId: string }) {
  const { session, setSession } = useAuth()
  const client = useQueryClient()
  const navigate = useNavigate()
  const token = session!.token, actor = session!.user.id
  const key = ['chat', actor, groupId] as const
  const base = `/chat/groups/${groupId}`
  const viewport = useRef<HTMLDivElement>(null)
  const initialized = useRef(false)
  const pinned = useRef(true)
  const latestSeq = useRef(0)
  const initialUnread = useRef<number | null>(null)
  const confirmedRead = useRef(0)
  const reading = useRef(false)
  const [text, setText] = useState('')
  const [mention, setMention] = useState(false)
  const [agentKind, setAgentKind] = useState<'qa' | 'extract'>('qa')
  const [sourceId, setSourceId] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const [failed, setFailed] = useState<{ content: string; clientMessageId: string; mentionsAI: boolean; agentKind: 'qa' | 'extract' } | null>(null)
  const [older, setOlder] = useState(false)
  const [historyBusy, setHistoryBusy] = useState(false)
  const [hasNew, setHasNew] = useState(false)
  const [closing, setClosing] = useState(false)
  const state = useQuery({ queryKey: [...key, 'state'], queryFn: ({ signal }) => apiRequest<ChatState>(base, { method: 'GET', token, signal }) })
  const runs = useQuery({ queryKey: [...key, 'runs'], queryFn: ({ signal }) => apiRequest<Run[]>(`${base}/runs`, { method: 'GET', token, signal }) })
  const messages = useQuery({
    queryKey: [...key, 'messages'], enabled: !!state.data,
    queryFn: async ({ signal }) => {
      const previous = client.getQueryData<Message[]>([...key, 'messages']) || []
      let after = previous.at(-1)?.seq ?? Math.max(0, (state.data?.readSeq || 0) - 20)
      let result = previous
      for (;;) {
        const page = await apiRequest<MessagePage>(`${base}/messages?afterSeq=${after}`, { method: 'GET', token, signal })
        result = mergeMessages(result, page.messages)
        if (!page.hasMore || !page.messages.length) break
        after = page.messages.at(-1)!.seq
      }
      return mergeMessages(client.getQueryData<Message[]>([...key, 'messages']) || [], result)
    },
  })
  useEffect(() => {
    for (const reason of [state.error, messages.error, runs.error]) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
    }
  }, [state.error, messages.error, runs.error, setSession])
  useEffect(() => {
    if (!state.data) return
    confirmedRead.current = Math.max(confirmedRead.current, state.data.readSeq)
    if (initialUnread.current === null) initialUnread.current = state.data.readSeq + 1
  }, [state.data])
  const markVisible = useCallback(async () => {
    const view = viewport.current
    if (!view || document.visibilityState !== 'visible' || !document.hasFocus() || reading.current) return
    const bounds = view.getBoundingClientRect()
    let seq = confirmedRead.current
    view.querySelectorAll<HTMLElement>('[data-read-seq]').forEach(node => {
      const rect = node.getBoundingClientRect()
      if (rect.bottom > bounds.top && rect.bottom <= bounds.bottom) seq = Math.max(seq, Number(node.dataset.readSeq))
    })
    if (seq <= confirmedRead.current) return
    reading.current = true
    try {
      const result = await apiRequest<{ readSeq: number }>(`${base}/read`, { token, body: { seq } })
      confirmedRead.current = Math.max(confirmedRead.current, result.readSeq)
      void client.invalidateQueries({ queryKey: ['groups'] })
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
    } finally { reading.current = false }
  }, [base, client, token, setSession])
  useEffect(() => {
    const onFocus = () => { void markVisible() }
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onFocus)
    const timer = setInterval(onFocus, 1000)
    return () => { window.removeEventListener('focus', onFocus); document.removeEventListener('visibilitychange', onFocus); clearInterval(timer) }
  }, [markVisible])
  useLayoutEffect(() => {
    const view = viewport.current
    if (!view || !messages.data || !state.data) return
    if (!initialized.current) {
      initialized.current = true
      setOlder((messages.data[0]?.seq || 1) > 1)
      const firstUnread = view.querySelector<HTMLElement>(`[data-message-seq="${initialUnread.current ?? state.data.readSeq + 1}"]`)
      if (firstUnread) view.scrollTop = firstUnread.offsetTop - view.offsetTop
      else view.scrollTop = view.scrollHeight
      pinned.current = view.scrollHeight - view.scrollTop - view.clientHeight < 80
    } else if (pinned.current) view.scrollTop = view.scrollHeight
    else if ((messages.data.at(-1)?.seq || 0) > latestSeq.current) setHasNew(true)
    latestSeq.current = messages.data.at(-1)?.seq || 0
    void markVisible()
  }, [messages.data, state.data, markVisible])
  async function send(input?: NonNullable<typeof failed>) {
    if (sending) return
    const payload = input || (failed && failed.content === text && failed.mentionsAI === mention && failed.agentKind === (mention ? agentKind : 'qa') ? failed : { content: text, clientMessageId: createMessageId(), mentionsAI: mention, agentKind: mention ? agentKind : 'qa' })
    if (!payload.content.trim()) return
    if (Array.from(payload.content).length > 4000) { setError('消息最多 4000 个字符'); return }
    setSending(true); setError('')
    try {
      const message = await apiRequest<Message>(`${base}/messages`, { token, body: payload })
      client.setQueryData<Message[]>([...key, 'messages'], old => {
        // Do not advance catchup beyond messages this client has never fetched.
        if (old && (old.at(-1)?.seq || 0) + 1 === message.seq) return mergeMessages(old, [message])
        return old
      })
      if (text === payload.content) { setText(''); setMention(false); setAgentKind('qa') }
      setFailed(null)
      pinned.current = true
      void client.invalidateQueries({ queryKey: key })
      void client.invalidateQueries({ queryKey: ['groups'] })
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
      else { setFailed(payload); setError(errorMessage(reason)) }
    } finally { setSending(false) }
  }
  async function loadOlder() {
    const view = viewport.current, first = messages.data?.[0]
    if (!view || !first || historyBusy) return
    setHistoryBusy(true)
    try {
      const page = await apiRequest<MessagePage>(`${base}/messages?beforeSeq=${first.seq}`, { method: 'GET', token })
      const height = view.scrollHeight, top = view.scrollTop
      pinned.current = false
      client.setQueryData<Message[]>([...key, 'messages'], old => mergeMessages(page.messages, old || []))
      setOlder(page.hasMore)
      requestAnimationFrame(() => { view.scrollTop = top + view.scrollHeight - height })
    } catch (reason) { setError(errorMessage(reason)) }
    finally { setHistoryBusy(false) }
  }
  async function showSource(id: string) {
    try {
      let source = messages.data?.find(message => message.id === id)
      if (!source) {
        source = await apiRequest<Message>(`${base}/messages/${id}`, { method: 'GET', token })
        client.setQueryData<Message[]>([...key, 'messages'], old => mergeMessages(old || [], [source!]))
      }
      pinned.current = false
      setSourceId(id)
      requestAnimationFrame(() => {
        const node = viewport.current?.querySelector<HTMLElement>(`[data-message-id="${id}"]`)
        node?.scrollIntoView({ block: 'center' })
      })
    } catch (reason) { setError(errorMessage(reason)) }
  }
  function sourceIDs(message: Message) {
    return [...new Set([...(message.agentResult?.answer?.sourceMessageIds || []), ...(message.agentResult?.suggestions?.flatMap(suggestion => suggestion.sourceMessageIds) || [])])]
  }
  async function retry(run: Run) {
    try { await apiRequest(`${base}/runs/${run.id}/retry`, { token }); await client.invalidateQueries({ queryKey: [...key, 'runs'] }) }
    catch (reason) { setError(errorMessage(reason)) }
  }
  async function close() {
    try { await apiRequest(`${base}/close`, { token }); await client.invalidateQueries({ queryKey: ['groups'] }); navigate('/chat/', { replace: true }) }
    catch (reason) { setError(errorMessage(reason)); setClosing(false) }
  }
  if (state.isPending) return <section className="chat-empty" role="status">正在打开群聊…</section>
  if (state.isError) return <section className="chat-empty"><p role="alert">{errorMessage(state.error)}</p><Link to="/chat/">返回群列表</Link></section>
  const peer = state.data.members.find(user => user.id !== actor)
  const activeRuns = runs.data?.filter(run => run.status === 'queued' || run.status === 'running') || []
  return <section className="conversation">
    <header className="conversation-header"><Link to="/chat/" aria-label="返回群列表"><ArrowLeft size={20} /></Link><div><h2>与 {peer?.nickname} 的群</h2><small>两位成员 + {state.data.assistantName}</small></div><Button variant="outline" onClick={() => setClosing(true)}>关闭群聊</Button></header>
    {closing && <div className="chat-close-confirm" role="alert"><p>关闭后，双方都无法再访问这个群的聊天记录。</p><Button variant="outline" onClick={() => setClosing(false)}>保留群聊</Button><Button onClick={() => { void close() }}>确认关闭</Button></div>}
    <div className="message-viewport" ref={viewport} onScroll={() => { const view = viewport.current!; pinned.current = view.scrollHeight - view.scrollTop - view.clientHeight < 80; if (pinned.current) setHasNew(false); void markVisible() }}>
      {older && <button className="history-button" disabled={historyBusy} onClick={() => { void loadOlder() }}>{historyBusy ? '加载中…' : '加载更早的消息'}</button>}
      {messages.isError && <p role="alert">{errorMessage(messages.error)} <button onClick={() => { void messages.refetch() }}>重试</button></p>}
      {messages.isPending ? <p role="status">正在加载消息…</p> : messages.data?.length === 0 ? <p className="message-empty">从一句问候开始。需要帮助时 @AI。</p> : messages.data?.map(m => <div key={m.id} data-message-seq={m.seq} data-message-id={m.id} className={sourceId === m.id ? 'highlighted-source' : undefined}>
        {m.seq === initialUnread.current && state.data.readSeq < m.seq && <p className="unread-divider">以下是未读消息</p>}
        <article className={`message-row ${m.senderId === actor ? 'mine' : ''} ${m.senderType === 'assistant' ? 'ai-message' : ''}`} aria-label={`${m.senderName}的消息`}>
          <span className="chat-avatar">{m.senderType === 'assistant' ? <Sparkles size={19} /> : m.senderName.slice(0, 1)}</span>
          <div className="message-body"><div className="message-meta">{m.senderName}<time>{new Date(m.createdAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</time></div>
            {m.mentionsAI && <small className="ai-reply-label">@AI 提问</small>}
            {m.replyToId && <small className="ai-reply-label">回复群内 @AI 提问</small>}<p className="message-bubble">{m.content}</p>
            {m.agentResult?.answer && <small className="agent-answer-status">{m.agentResult.answer.status === 'answered' ? '依据群聊记录回答' : m.agentResult.answer.status === 'conflicting_evidence' ? '历史证据存在冲突' : '历史证据不足'}</small>}
            {sourceIDs(m).length > 0 && <div className="message-sources" aria-label="回答来源">{sourceIDs(m).map((id, index) => <button type="button" key={id} onClick={() => { void showSource(id) }}>来源 {index + 1}</button>)}</div>}
            <span data-read-seq={m.seq} className="read-sentinel" /></div>
        </article>
      </div>)}
    </div>
    {hasNew && <button className="new-message-button" onClick={() => { const view = viewport.current!; view.scrollTop = view.scrollHeight; pinned.current = true; setHasNew(false); void markVisible() }}>回到最新消息 ↓</button>}
    <div className="ai-run-status" aria-live="polite">{activeRuns.length > 0 && <p>{activeRuns.some(run => run.status === 'running') ? 'AI 正在回复…' : 'AI 请求已排队…'}{activeRuns.length > 1 ? `（${activeRuns.length} 个请求）` : ''}</p>}
      {runs.data?.filter(run => run.status === 'failed').map(run => <p key={run.id}><small className="failed-question">{messages.data?.find(message => message.id === run.triggerMessageId)?.content || '@AI 提问'}</small>AI 回复失败：{run.errorCode === 'ai_unavailable' ? '服务暂未启用' : run.errorCode === 'context_too_large' ? '聊天上下文超过模型容量' : '请稍后重试'}{run.requestedBy === actor && state.data.aiEnabled && <button onClick={() => { void retry(run) }}>重试 AI 回复</button>}</p>)}
    </div>
    <form className="message-composer" onSubmit={e => { e.preventDefault(); void send() }}>
      {error && <div role="alert" className="account-error">{error}{failed && <button type="button" disabled={sending} onClick={() => { void send(failed) }}>重试发送</button>}</div>}
      <label htmlFor="messageInput" className="sr-only">消息内容</label><textarea id="messageInput" value={text} onChange={e => setText(e.target.value)} placeholder="写点什么，需要帮忙时 @AI" disabled={sending} rows={2} onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); void send() } }} />
      <div className="composer-actions"><button type="button" className={`mention-button ${mention ? 'selected' : ''}`} aria-pressed={mention} onClick={() => setMention(!mention)} disabled={sending}><Sparkles size={16} />@AI</button>{mention && <label className="agent-kind-selector">AI 能力<select aria-label="AI 能力" value={agentKind} disabled={sending} onChange={e => setAgentKind(e.target.value as 'qa' | 'extract')}><option value="qa">历史问答</option><option value="extract">提取待办</option></select></label>}<small>Enter 发送 · Shift + Enter 换行</small><Button disabled={sending || !text.trim()} type="submit"><Send size={16} />{sending ? '发送中…' : '发送'}</Button></div>
    </form>
  </section>
}
