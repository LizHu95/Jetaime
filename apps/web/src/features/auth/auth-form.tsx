import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { z } from 'zod'
import { AlertCircle, ArrowRight, CheckCircle2, Eye, EyeOff, LoaderCircle, LockKeyhole, Sparkles, UserRound } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { ApiError, errorMessage } from '@/lib/api'
import { login, registerAccount } from './api'
import { loginSchema, registerSchema, type RegisterFields } from './schema'
import { useAuth } from './auth-provider'

function FormField({ name, label, error, hint, children }: {
  name: string; label: string; error?: string; hint?: string; children: ReactNode
}) {
  return <div className="form-field">
    <Label htmlFor={name}>{label}</Label>
    {children}
    {(error || hint) && <p id={`${name}-hint`} className={error ? 'field-error' : 'field-hint'}>{error || hint}</p>}
  </div>
}

export function AuthForm({ mode }: { mode: 'login' | 'register' }) {
  const isRegister = mode === 'register'
  const navigate = useNavigate()
  const location = useLocation()
  const { setSession } = useAuth()
  const [serverError, setServerError] = useState('')
  const [visible, setVisible] = useState<Record<string, boolean>>({})
  const controller = useRef<AbortController | null>(null)
  const state = location.state as { registered?: boolean; username?: string; message?: string } | null
  const form = useForm<RegisterFields>({
    resolver: zodResolver(isRegister ? registerSchema : loginSchema.extend({ nickname: z.string(), confirmPassword: z.string() })),
    defaultValues: { username: typeof state?.username === 'string' ? state.username : '', nickname: '', password: '', confirmPassword: '' },
    mode: 'onSubmit', reValidateMode: 'onChange',
  })
  const loginMutation = useMutation({ mutationFn: (fields: RegisterFields) => login(fields, controller.current?.signal) })
  const registerMutation = useMutation({ mutationFn: (fields: RegisterFields) => registerAccount(fields, controller.current?.signal) })
  const busy = form.formState.isSubmitting
  useEffect(() => {
    document.title = `${isRegister ? '注册' : '登录'} · Jetaime`
    return () => controller.current?.abort()
  }, [isRegister])
  useEffect(() => {
    if (!busy && form.formState.errors.username?.type === 'server') form.setFocus('username')
  }, [busy, form.formState.errors.username, form.setFocus])

  async function submit(fields: RegisterFields) {
    setServerError('')
    controller.current = new AbortController()
    try {
      if (isRegister) {
        const user = await registerMutation.mutateAsync(fields)
        form.reset()
        navigate('/auth/login/', { replace: true, state: { registered: true, username: user.username } })
      } else {
        const session = await loginMutation.mutateAsync(fields)
        form.reset()
        setSession(session)
        navigate('/auth/session/', { replace: true })
      }
    } catch (error) {
      if (controller.current.signal.aborted) return
      setServerError(errorMessage(error))
      if (error instanceof ApiError && error.code === 'username_taken') {
        form.setError('username', { type: 'server', message: '请换一个账号，或返回登录' })
      }
    } finally {
      loginMutation.reset()
      registerMutation.reset()
    }
  }

  function passwordField(name: 'password' | 'confirmPassword', label: string) {
    const error = form.formState.errors[name]?.message
    return <FormField name={name} label={label} error={error} hint={isRegister && name === 'password' ? '6–128 个字符，支持空格和中文' : undefined}>
      <div className="input-wrap">
        <LockKeyhole className="input-icon" size={17} />
        <Input id={name} {...form.register(name)} type={visible[name] ? 'text' : 'password'}
          placeholder={name === 'confirmPassword' ? '再次输入密码' : isRegister ? '设置你的密码' : '输入你的密码'}
          autoComplete={isRegister ? 'new-password' : 'current-password'}
          aria-invalid={!!error} aria-describedby={error || (isRegister && name === 'password') ? `${name}-hint` : undefined}
          className="auth-input password-input" />
        <button type="button" className="password-toggle" aria-label={`${visible[name] ? '隐藏' : '显示'}${label}`} aria-pressed={!!visible[name]}
          onClick={() => setVisible(previous => ({ ...previous, [name]: !previous[name] }))}>
          {visible[name] ? <EyeOff size={17} /> : <Eye size={17} />}
        </button>
      </div>
    </FormField>
  }

  return <div className={`auth-card ${isRegister ? 'register-card' : ''}`}>
    <div className="form-emblem"><Sparkles size={23} strokeWidth={1.6} /></div>
    <p className="form-eyebrow">YOUR DAILY AI COMPANION</p>
    <h2>{isRegister ? '开启你的协作空间' : '欢迎回来'}</h2>
    <p className="form-description">{isRegister ? '创建账号，让每一次对话都有下一步。' : '登录 Jetaime，接着聊，一起做。'}</p>
    {!isRegister && state?.message && <Alert className="feedback feedback-success" role="status"><CheckCircle2 /><AlertDescription>{state.message}</AlertDescription></Alert>}
    {!isRegister && state?.registered && <Alert className="feedback feedback-success" role="status"><CheckCircle2 /><AlertDescription>注册成功！请使用刚创建的账号登录。</AlertDescription></Alert>}
    {serverError && <Alert className="feedback feedback-error" role="alert"><AlertCircle /><AlertDescription>{serverError}</AlertDescription></Alert>}
    <form noValidate onSubmit={form.handleSubmit(submit)} aria-busy={busy}>
      <fieldset disabled={busy}>
        <FormField name="username" label="账号" error={form.formState.errors.username?.message}
          hint={isRegister ? '2–24 位字母、数字或下划线，区分大小写' : undefined}>
          <div className="input-wrap"><UserRound className="input-icon" size={17} />
            <Input id="username" {...form.register('username')} placeholder={isRegister ? '设置你的账号' : '输入你的账号'}
              autoComplete="username" autoCapitalize="none" autoCorrect="off" spellCheck={false}
              aria-invalid={!!form.formState.errors.username}
              aria-describedby={isRegister || form.formState.errors.username ? 'username-hint' : undefined} className="auth-input" />
          </div>
        </FormField>
        {isRegister && <FormField name="nickname" label="昵称（可选）" error={form.formState.errors.nickname?.message}>
          <div className="input-wrap"><Sparkles className="input-icon" size={17} /><Input id="nickname" {...form.register('nickname')}
            placeholder="怎么称呼你？" autoComplete="nickname" className="auth-input"
            aria-invalid={!!form.formState.errors.nickname} aria-describedby={form.formState.errors.nickname ? 'nickname-hint' : undefined} /></div>
        </FormField>}
        {passwordField('password', '密码')}
        {isRegister && passwordField('confirmPassword', '确认密码')}
        {!isRegister && <p className="login-note">账号区分大小写，请使用注册时的写法。</p>}
        <Button type="submit" className="submit-button" disabled={busy}>
          {busy ? <><LoaderCircle className="animate-spin" size={18} />{isRegister ? '正在创建账号…' : '正在登录…'}</>
            : <>{isRegister ? '创建账号' : '登录'}<ArrowRight size={18} /></>}
        </Button>
      </fieldset>
    </form>
    <div className="auth-divider"><span />让日常，更有默契<span /></div>
    <p className="form-switch">{isRegister ? '已经有账号？' : '第一次来这里？'} <Link to={isRegister ? '/auth/login/' : '/auth/register/'}>
      {isRegister ? '返回登录' : '创建一个账号'}<ArrowUpRightIcon />
    </Link></p>
  </div>
}

function ArrowUpRightIcon() {
  return <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true"><path d="M7 17L17 7M7 7h10v10" /></svg>
}
