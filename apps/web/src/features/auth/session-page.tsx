import { useEffect, useState } from 'react'
import { Link, Navigate } from 'react-router-dom'
import { useMutation } from '@tanstack/react-query'
import { Bell, ChevronRight, KeyRound, LoaderCircle, LogOut, Palette } from 'lucide-react'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { errorMessage } from '@/lib/api'
import { useAuth } from './auth-provider'
import { logout } from './api'

export function SessionPage() {
  const { session, setSession } = useAuth()
  const [error, setError] = useState('')
  const mutation = useMutation({ mutationFn: logout })
  useEffect(() => { document.title = '我 · Jetaime' }, [])
  if (!session) return <Navigate to="/auth/login/" replace />
  async function signOut() {
    setError('')
    try {
      await mutation.mutateAsync(session!.token)
      setSession(null)
    } catch (reason) { setError(errorMessage(reason)) }
    finally { mutation.reset() }
  }
  const nickname = session.user.nickname || session.user.username
  return <main className="profile-page">
    <header className="page-heading"><h1>我</h1></header>
    <section className="profile-card" aria-label="个人资料">
      <div className="profile-avatar" role="img" aria-label={`${nickname}的头像`}>{Array.from(nickname)[0]}</div>
      <div><h2>{nickname}</h2><p>账号：{session.user.username}</p></div>
    </section>
    <section className="account-management" aria-labelledby="settingsTitle">
      <h2 id="settingsTitle">偏好设置</h2>
      <div className="settings-list"><Link className="settings-row" to="/me/settings/"><Palette size={20} /><span>主题设置</span><ChevronRight size={18} /></Link></div>
    </section>
    <section className="account-management" aria-labelledby="accountTitle">
      <h2 id="accountTitle">账号管理</h2>
      <div className="settings-list">
        <Link className="settings-row" to="/auth/password/"><KeyRound size={20} /><span>修改密码</span><ChevronRight size={18} /></Link>
        <button className="settings-row logout-row" onClick={signOut} disabled={mutation.isPending}>
          {mutation.isPending ? <LoaderCircle className="animate-spin" size={20} /> : <LogOut size={20} />}
          <span>{mutation.isPending ? '正在退出…' : '退出登录'}</span><ChevronRight size={18} />
        </button>
      </div>
    </section>
    <section className="account-management" aria-labelledby="notificationTitle">
      <h2 id="notificationTitle">消息通知</h2>
      <div className="settings-list"><Link className="settings-row" to="/notifications/"><Bell size={20} /><span>消息通知设置</span><ChevronRight size={18} /></Link></div>
    </section>
    {error && <Alert className="feedback feedback-error" role="alert"><AlertDescription>{error}</AlertDescription></Alert>}
  </main>
}
