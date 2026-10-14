import { ApiError, apiRequest } from '@/lib/api'
import { sessionSchema, userSchema, type LoginFields, type RegisterFields } from './schema'

export async function login(fields: LoginFields, signal?: AbortSignal) {
  const result = sessionSchema.safeParse(await apiRequest('/auth/login', {
    body: { username: fields.username.trim(), password: fields.password }, signal,
  }))
  if (!result.success || Date.parse(result.data.expiresAt) <= Date.now()) {
    throw new ApiError('invalid_response', 200, '登录响应异常，请稍后重试。')
  }
  return result.data
}

export async function registerAccount(fields: RegisterFields, signal?: AbortSignal) {
  const result = userSchema.safeParse(await apiRequest('/auth/register', {
    body: { username: fields.username.trim(), password: fields.password, nickname: fields.nickname.trim() }, signal,
  }))
  if (!result.success) throw new ApiError('invalid_response', 201, '注册响应异常，请稍后重试。')
  return result.data
}

export async function logout(token: string) {
  try {
    await apiRequest('/auth/logout', { token })
  } catch (error) {
    // An expired or revoked session is already signed out on the server.
    if (!(error instanceof ApiError && error.status === 401)) throw error
  }
}
