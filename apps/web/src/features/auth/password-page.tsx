import { useEffect, useState } from 'react'
import { Navigate } from 'react-router-dom'
import { useAuth } from './auth-provider'
import { apiRequest, ApiError, errorMessage } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SubpageLayout } from '@/components/subpage-layout'
import { LoaderCircle } from 'lucide-react'

export function PasswordPage() {
  const { session, setSession } = useAuth()
  const [completed, setCompleted] = useState<{ username: string; message: string } | null>(null)
  const [oldPassword, setOld] = useState('')
  const [newPassword, setNew] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  useEffect(() => { document.title = '修改密码 · Jetaime' }, [])
  if (!session) return <Navigate to="/auth/login/" replace state={completed} />
  async function submit(event: React.FormEvent) {
    event.preventDefault()
    if (pending) return
    setError('')
    if (Array.from(newPassword).length < 6 || Array.from(newPassword).length > 128) { setError('新密码须为 6–128 个字符'); return }
    if (newPassword !== confirm) { setError('两次输入的新密码不一致'); return }
    if (oldPassword === newPassword) { setError('新密码不能与旧密码相同'); return }
    setPending(true)
    try {
      await apiRequest('/auth/password', { token: session!.token, body: { oldPassword, newPassword } })
      setCompleted({ username: session!.user.username, message: '密码已修改，请使用新密码登录。' })
      setSession(null)
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === 'unauthorized') setSession(null)
      else setError(reason instanceof ApiError && reason.code === 'invalid_credentials' ? '旧密码不正确' : errorMessage(reason))
    } finally { setPending(false) }
  }
  return <SubpageLayout className="password-page" title="修改密码" backTo="/me/" backLabel="返回我的账号">
    <p className="password-description">修改成功后，所有设备需使用新密码重新登录。</p>
    <form onSubmit={submit} className="password-form" aria-busy={pending}>
      <fieldset disabled={pending}>
        <div className="password-field">
          <label htmlFor="oldPassword">旧密码</label>
          <Input id="oldPassword" type="password" autoComplete="current-password" placeholder="输入当前密码" value={oldPassword} onChange={e => setOld(e.target.value)} required />
        </div>
        <div className="password-field">
          <label htmlFor="newPassword">新密码</label>
          <Input id="newPassword" type="password" autoComplete="new-password" placeholder="设置新密码" aria-describedby="newPasswordHint" value={newPassword} onChange={e => setNew(e.target.value)} required />
          <p id="newPasswordHint" className="field-hint">6–128 个字符，支持空格和中文</p>
        </div>
        <div className="password-field">
          <label htmlFor="confirmPassword">确认新密码</label>
          <Input id="confirmPassword" type="password" autoComplete="new-password" placeholder="再次输入新密码" value={confirm} onChange={e => setConfirm(e.target.value)} required />
        </div>
        {error && <p role="alert" className="account-error">{error}</p>}
        <Button type="submit" className="submit-button" disabled={pending}>{pending && <LoaderCircle className="animate-spin" size={18} />}{pending ? '正在修改…' : '保存新密码'}</Button>
      </fieldset>
    </form>
  </SubpageLayout>
}
