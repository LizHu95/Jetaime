import { useEffect, useState } from 'react'
import { Link, Navigate } from 'react-router-dom'
import { useMutation } from '@tanstack/react-query'
import { Check, LoaderCircle, LogOut } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { errorMessage } from '@/lib/api'
import { useAuth } from './auth-provider'
import { logout } from './api'

export function SessionPage() {
  const { session, setSession } = useAuth()
  const [error, setError] = useState('')
  const mutation = useMutation({ mutationFn: logout })
  useEffect(() => { document.title = '登录成功 · Jetaime' }, [])
  if (!session) return <Navigate to="/auth/login/" replace />
  async function signOut() {
    setError('')
    try {
      await mutation.mutateAsync(session!.token)
      setSession(null)
    } catch (reason) { setError(errorMessage(reason)) }
    finally { mutation.reset() }
  }
  return <div className="auth-card session-card">
    <div className="session-check"><Check size={27} /></div>
    <p className="form-eyebrow">CONNECTED TO JETAIME</p>
    <h2>你好，{session.user.nickname || session.user.username}</h2>
    <p className="form-description">你已成功登录，新的协作从这里开始。</p>
    <div className="session-details"><span>当前账号</span><strong>{session.user.username}</strong><span>登录状态</span><strong className="connected-state"><i />已连接</strong></div>
    <div className="account-actions session-links"><Link className="account-link" to="/chat/">我的群聊</Link><Link className="account-link" to="/contacts/">加好友 / 创建群</Link><Link className="account-link" to="/auth/password/">修改密码</Link></div>
    {error && <Alert className="feedback feedback-error" role="alert"><AlertDescription>{error}</AlertDescription></Alert>}
    <Button className="submit-button" onClick={signOut} disabled={mutation.isPending}>
      {mutation.isPending ? <LoaderCircle className="animate-spin" size={17} /> : <LogOut size={17} />}
      {mutation.isPending ? '正在退出…' : '退出当前账号'}
    </Button>
  </div>
}
