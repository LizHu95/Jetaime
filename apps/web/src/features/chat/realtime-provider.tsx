import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/features/auth/auth-provider'

const RealtimeContext = createContext(false)
export const useRealtime = () => useContext(RealtimeContext)
export function RealtimeProvider({ children }: { children: ReactNode }) {
  const { session, setSession } = useAuth()
  const client = useQueryClient()
  const [connected, setConnected] = useState(false)
  useEffect(() => {
    if (!session) { setConnected(false); return }
    let stopped = false, attempt = 0
    let socket: WebSocket | null = null
    let timer: ReturnType<typeof setTimeout> | undefined
    function refresh(groupId?: string) {
      void client.invalidateQueries({ queryKey: ['groups'] })
      void client.invalidateQueries({ queryKey: groupId ? ['chat', session!.user.id, groupId] : ['chat'] })
    }
    function connect() {
      if (stopped) return
      socket = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/chat/events`)
      socket.onopen = () => socket?.send(JSON.stringify({ type: 'authenticate', token: session!.token }))
      socket.onmessage = ({ data }) => {
        try {
          const event = JSON.parse(data) as { type: string; groupId?: string }
          if (event.type === 'ready') { attempt = 0; setConnected(true); refresh() }
          else if (['message', 'read', 'ai', 'group_closed', 'resync'].includes(event.type)) refresh(event.groupId)
        } catch { /* A malformed notification never replaces durable messages. */ }
      }
      socket.onclose = event => {
        if (stopped) return
        setConnected(false)
        if (event.code === 1008) { stopped = true; setSession(null); return }
        timer = setTimeout(connect, Math.min(1000 * 2 ** attempt++, 15_000))
      }
      socket.onerror = () => socket?.close()
    }
    connect()
    const onFocus = () => refresh()
    window.addEventListener('focus', onFocus)
    // Reconciliation also covers a missed PostgreSQL notification or a proxy
    // that silently drops a connection before the browser notices.
    const reconcile = setInterval(() => { if (document.visibilityState === 'visible') refresh() }, 15_000)
    return () => { stopped = true; clearTimeout(timer); clearInterval(reconcile); window.removeEventListener('focus', onFocus); socket?.close(); setConnected(false) }
  }, [session, setSession, client])
  return <RealtimeContext.Provider value={connected}>{children}</RealtimeContext.Provider>
}
