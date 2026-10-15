export class ApiError extends Error {
  constructor(public code: string, public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

export async function apiRequest<T>(path: string, options: {
  method?: 'GET' | 'POST' | 'DELETE'
  body?: unknown
  token?: string
  signal?: AbortSignal
} = {}): Promise<T> {
  const timeout = AbortSignal.timeout(15_000)
  const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout
  let response: Response
  try {
    response = await fetch(`/api${path}`, {
      method: options.method || 'POST',
      headers: {
        Accept: 'application/json',
        ...(options.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...(options.token ? { Authorization: `Bearer ${options.token}` } : {}),
      },
      body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
      signal,
      cache: 'no-store',
    })
  } catch (error) {
    if (options.signal?.aborted) throw error
    throw new ApiError(timeout.aborted ? 'timeout' : 'network_error', 0,
      timeout.aborted ? '请求超时，请稍后重试。' : '无法连接服务，请检查网络后重试。')
  }
  if (response.status === 204) return undefined as T
  const payload = await response.json().catch(() => null)
  if (!response.ok) {
    throw new ApiError(payload?.error?.code || 'service_unavailable', response.status,
      '服务暂不可用，请稍后重试。')
  }
  if (payload === null) throw new ApiError('invalid_response', response.status, '服务响应异常，请稍后重试。')
  return payload as T
}

const messages: Record<string, string> = {
  push_test_wait: '请确认已开启此设备通知，连续测试需间隔 30 秒。',
  push_disabled: '服务器尚未开启通知。',
  invalid_subscription: '通知订阅无效，请关闭后重新开启。',
  push_failed: '通知服务暂不可用，请稍后重试。',
  group_not_found: '群聊已关闭或你没有访问权限。',
  message_conflict: '这条消息或 AI 任务状态冲突，请刷新后重试。',
  ai_unavailable: 'AI 服务暂未启用，请稍后重试。',
  not_found: '没有找到这个账号或申请，请检查账号大小写。',
  forbidden: '你没有权限处理这条申请。',
  request_conflict: '这条申请已被处理，请刷新后查看。',
  already_connected: '你们已经有一个群了，请在我的群组中查看。',
  invalid_credentials: '账号或密码不正确，请注意账号大小写。',
  username_taken: '这个账号已被注册，请换一个或直接登录。',
  invalid_input: '填写内容不符合要求，请检查后重试。',
  invalid_json: '提交失败，请刷新页面后重试。',
  unauthorized: '登录已失效，请重新登录。',
  auth_busy: '当前登录请求较多，请稍等片刻再试。',
  internal_error: '服务暂时遇到问题，请稍后重试。',
}

export function errorMessage(error: unknown) {
  if (error instanceof ApiError) return messages[error.code] || error.message
  return '操作失败，请稍后重试。'
}
