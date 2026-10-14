import { createContext, useContext, useCallback, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { Session } from './schema'
import { persistSession, readSession } from './session'

const AuthContext = createContext<{
  session: Session | null
  setSession: (session: Session | null) => void
} | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, updateSession] = useState(readSession)
  const queryClient = useQueryClient()
  const setSession = useCallback((value: Session | null) => {
    queryClient.removeQueries()
    persistSession(value)
    updateSession(value)
  }, [queryClient])
  useEffect(() => {
    if (!session) return
    const expire = () => {
      if (Date.parse(session.expiresAt) <= Date.now()) {
        persistSession(null)
        updateSession(null)
        queryClient.removeQueries()
      }
    }
    const timer = window.setTimeout(expire, Math.max(0, Math.min(Date.parse(session.expiresAt) - Date.now(), 2_147_483_647)))
    window.addEventListener('focus', expire)
    return () => { clearTimeout(timer); window.removeEventListener('focus', expire) }
  }, [session, queryClient])
  return <AuthContext.Provider value={{ session, setSession }}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const context = useContext(AuthContext)
  if (!context) throw new Error('useAuth must be used within AuthProvider')
  return context
}
