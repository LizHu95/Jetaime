import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { loginSchema, registerSchema } from '@/features/auth/schema'
import { login, logout, registerAccount } from '@/features/auth/api'
import { readSession, persistSession } from '@/features/auth/session'
import { ApiError, errorMessage } from '@/lib/api'

const fields = { username: ' Liz ', nickname: ' 小李 ', password: ' 密码😀123 ', confirmPassword: ' 密码😀123 ' }
const session = { user: { id: 'test-user', username: 'Liz', nickname: '小李' }, token: 'x'.repeat(43), tokenType: 'Bearer', expiresAt: new Date(Date.now() + 60_000).toISOString() }

afterEach(() => vi.unstubAllGlobals())

describe('authentication contracts', () => {
  it('preserves account case and password whitespace while trimming account and nickname', () => {
    expect(registerSchema.parse(fields)).toEqual({ ...fields, username: 'Liz', nickname: '小李' })
  })
  it('counts Unicode code points instead of UTF-16 units', () => {
    expect(registerSchema.safeParse({ ...fields, password: '😀'.repeat(128), confirmPassword: '😀'.repeat(128) }).success).toBe(true)
    expect(registerSchema.safeParse({ ...fields, password: '😀'.repeat(129), confirmPassword: '😀'.repeat(129) }).success).toBe(false)
    expect(registerSchema.safeParse({ ...fields, nickname: '😀'.repeat(24) }).success).toBe(true)
    expect(registerSchema.safeParse({ ...fields, nickname: '😀'.repeat(25) }).success).toBe(false)
  })
  it('rejects illegal accounts and mismatched confirmation, but permits short existing login passwords', () => {
    expect(registerSchema.safeParse({ ...fields, username: '中文' }).success).toBe(false)
    expect(registerSchema.safeParse({ ...fields, confirmPassword: 'different' }).success).toBe(false)
    expect(loginSchema.safeParse({ username: 'Liz', password: 'x' }).success).toBe(true)
  })
  it('sends only backend-supported registration fields and never normalizes the password', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(session.user), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)
    await registerAccount(fields)
    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/auth/register')
    expect(JSON.parse(options.body)).toEqual({ username: 'Liz', nickname: '小李', password: fields.password })
    expect(options.cache).toBe('no-store')
  })
  it('maps backend errors to user-facing Chinese messages', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'invalid_credentials', message: 'invalid username or password' } }), { status: 401 })))
    await expect(login(fields)).rejects.toMatchObject({ code: 'invalid_credentials', status: 401 })
    expect(errorMessage(new ApiError('invalid_credentials', 401, 'server message'))).toContain('账号或密码不正确')
  })
  it('rejects malformed login success responses', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user: session.user, token: 'bad' }), { status: 200 })))
    await expect(login(fields)).rejects.toMatchObject({ code: 'invalid_response' })
  })
  it('sends bearer authentication on logout and handles an empty 204 response', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(logout(session.token)).resolves.toBeUndefined()
    expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe(`Bearer ${session.token}`)
    expect(fetchMock.mock.calls[0][1].body).toBeUndefined()
  })
  it('treats a server-expired session as logged out', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'unauthorized' } }), { status: 401 })))
    await expect(logout(session.token)).resolves.toBeUndefined()
  })
})

describe('persistent session storage', () => {
  function storage(name: 'localStorage' | 'sessionStorage' = 'localStorage') {
    const entries = new Map<string, string>()
    vi.stubGlobal(name, {
      getItem: (key: string) => entries.get(key) ?? null,
      setItem: (key: string, value: string) => entries.set(key, value),
      removeItem: (key: string) => entries.delete(key),
    })
    return entries
  }
  beforeEach(() => { storage(); storage('sessionStorage') })
  it('restores a valid session and removes it on logout', () => {
    storage()
    persistSession(session as Parameters<typeof persistSession>[0])
    expect(readSession()).toEqual(session)
    persistSession(null)
    expect(readSession()).toBeNull()
  })
  it('migrates existing tab sessions to persistent storage', () => {
    const persistent = storage()
    const legacy = storage('sessionStorage')
    legacy.set('jetaime.auth.session', JSON.stringify(session))
    expect(readSession()).toEqual(session)
    expect(persistent.get('jetaime.auth.session')).toBe(JSON.stringify(session))
    expect(legacy.size).toBe(0)
    storage('sessionStorage')
    expect(readSession()).toEqual(session)
  })
  it('clears both storage locations on logout', () => {
    const persistent = storage()
    const legacy = storage('sessionStorage')
    for (const entries of [persistent, legacy]) entries.set('jetaime.auth.session', JSON.stringify(session))
    persistSession(null)
    expect(persistent.size).toBe(0)
    expect(legacy.size).toBe(0)
    expect(readSession()).toBeNull()
  })
  it('remains usable when browser storage is disabled', () => {
    vi.stubGlobal('localStorage', { getItem: () => { throw new Error('disabled') }, setItem: () => { throw new Error('disabled') }, removeItem: () => { throw new Error('disabled') } })
    expect(() => persistSession(session as Parameters<typeof persistSession>[0])).not.toThrow()
    expect(readSession()).toBeNull()
    expect(() => persistSession(null)).not.toThrow()
  })
  it('discards expired and corrupt sessions', () => {
    const entries = storage()
    entries.set('jetaime.auth.session', JSON.stringify({ ...session, expiresAt: '2020-01-01T00:00:00Z' }))
    expect(readSession()).toBeNull()
    expect(entries.size).toBe(0)
    entries.set('jetaime.auth.session', 'invalid json')
    expect(readSession()).toBeNull()
    expect(entries.size).toBe(0)
  })
})
