import { z } from 'zod'

const length = (value: string) => Array.from(value).length
const username = z.string().trim().regex(/^[A-Za-z0-9_]{2,24}$/, '账号须为 2–24 位字母、数字或下划线')
const password = (min: number) => z.string().refine(
  value => length(value) >= min && length(value) <= 128,
  min === 1 ? '请输入密码，最多 128 个字符' : '密码须为 6–128 个字符',
)

export const loginSchema = z.object({ username, password: password(1) })
export const registerSchema = z.object({
  username,
  nickname: z.string().trim().refine(value => length(value) <= 24 && !value.includes('\0'), '昵称最多 24 个字符，不能包含空字符'),
  password: password(6),
  confirmPassword: z.string(),
}).refine(value => value.password === value.confirmPassword, {
  message: '两次输入的密码不一致', path: ['confirmPassword'],
})

export type LoginFields = z.infer<typeof loginSchema>
export type RegisterFields = z.infer<typeof registerSchema>

export const userSchema = z.object({ id: z.string().min(1), username: z.string().min(1), nickname: z.string() })
export const sessionSchema = z.object({
  user: userSchema,
  token: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  tokenType: z.literal('Bearer'),
  expiresAt: z.string().refine(value => Number.isFinite(Date.parse(value))),
})
export type Session = z.infer<typeof sessionSchema>
