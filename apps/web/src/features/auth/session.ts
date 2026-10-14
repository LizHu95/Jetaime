import { sessionSchema, type Session } from './schema'

const key = 'jetaime.auth.session'

export function readSession(): Session | null {
  try {
    const stored = sessionStorage.getItem(key)
    if (!stored) return null
    const result = sessionSchema.safeParse(JSON.parse(stored))
    if (result.success && Date.parse(result.data.expiresAt) > Date.now()) return result.data
    sessionStorage.removeItem(key)
  } catch {
    try { sessionStorage.removeItem(key) } catch { /* Storage can be unavailable. */ }
  }
  return null
}

export function persistSession(session: Session | null) {
  try {
    if (session) sessionStorage.setItem(key, JSON.stringify(session))
    else sessionStorage.removeItem(key)
  } catch { /* Keep the in-memory session usable if browser storage is disabled. */ }
}
