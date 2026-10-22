import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useAuth } from '@/features/auth/auth-provider'
import { apiRequest, ApiError } from '@/lib/api'
import type { Message } from './types'

function SourcePreview({ id, groupId, loaded, onSelect }: { id: string; groupId: string; loaded?: Message; onSelect: (id: string) => void }) {
  const { session, setSession } = useAuth()
  const query = useQuery({
    queryKey: ['chat', session?.user.id, groupId, 'message', id],
    queryFn: ({ signal }) => apiRequest<Message>(`/chat/groups/${groupId}/messages/${id}`, { method: 'GET', token: session?.token, signal }),
    enabled: !loaded && !!session,
    staleTime: Infinity,
  })
  useEffect(() => {
    if (query.error instanceof ApiError && query.error.code === 'unauthorized') setSession(null)
  }, [query.error, setSession])
  const source = loaded || query.data
  return <button type="button" className="source-preview" onClick={() => onSelect(id)}>
    {source ? <>
      <span className="source-preview-meta"><strong>{source.senderName}</strong><time dateTime={source.createdAt}>{new Date(source.createdAt).toLocaleString('zh-CN', { year: 'numeric', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</time></span>
      <span className="source-preview-text">{source.content.replace(/\s+/g, ' ').trim().slice(0, 120)}</span>
    </> : <span className="source-preview-text">{query.isError ? '引用暂不可用，点击重试' : '正在加载引用…'}</span>}
  </button>
}

export function MessageSources({ message, messages, groupId, onSelect }: { message: Message; messages: Message[]; groupId: string; onSelect: (id: string) => void }) {
  const [expanded, setExpanded] = useState(false)
  const ids = [...new Set([...(message.agentResult?.answer?.sourceMessageIds || []), ...(message.agentResult?.suggestions?.flatMap(suggestion => suggestion.sourceMessageIds) || [])])]
  if (!ids.length) return null
  const previews = () => ids.map(id => <SourcePreview key={id} id={id} groupId={groupId} loaded={messages.find(source => source.id === id)} onSelect={onSelect} />)
  return <div className="message-sources" aria-label="回答引用">
    {ids.length === 1 ? previews() : <details onToggle={event => setExpanded(event.currentTarget.open)}>
      <summary>{expanded ? '收起' : '查看'} {ids.length} 条引用</summary>
      {expanded && <div className="source-preview-list">{previews()}</div>}
    </details>}
  </div>
}
