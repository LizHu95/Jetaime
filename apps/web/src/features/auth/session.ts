import { sessionSchema, type Session } from './schema'

const key = 'jetaime.auth.session'

function clearStoredSession() {
  try { localStorage.removeItem(key) } catch { /* Storage can be unavailable. */ }
  try { sessionStorage.removeItem(key) } catch { /* Storage can be unavailable. */ }
}

export function readSession(): Session | null {
  try {
    // Migrate sessions saved by earlier versions without requiring another login.
    const stored = localStorage.getItem(key) ?? sessionStorage.getItem(key)
    if (!stored) return null
    const result = sessionSchema.safeParse(JSON.parse(stored))
    if (result.success && Date.parse(result.data.expiresAt) > Date.now()) {
      persistSession(result.data)
      return result.data
    }
  } catch { /* Invalid or inaccessible storage is treated as logged out. */ }
  clearStoredSession()
  return null
}

export function persistSession(session: Session | null) {
  if (!session) {
    clearStoredSession()
    return
  }
  try {
    localStorage.setItem(key, JSON.stringify(session))
    sessionStorage.removeItem(key)
  } catch { /* Keep the in-memory session usable if browser storage is disabled. */ }
}
