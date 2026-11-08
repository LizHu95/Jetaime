import { useEffect } from 'react'
import { Check, Palette } from 'lucide-react'
import { Navigate } from 'react-router-dom'
import { SubpageLayout } from '@/components/subpage-layout'
import { useAuth } from '@/features/auth/auth-provider'
import { themes, useTheme } from './theme-provider'

export function SettingsPage() {
  const { session } = useAuth()
  const { theme, setTheme } = useTheme()
  useEffect(() => { document.title = '主题设置 · Jetaime' }, [])
  if (!session) return <Navigate to="/auth/login/" replace />
  return <SubpageLayout className="settings-page" title="主题设置" backTo="/me/" backLabel="返回我">
    <section aria-labelledby="theme-title">
      <div className="theme-heading"><Palette size={20} /><h2 id="theme-title">主题切换</h2></div>
      <p className="theme-description">选择适合你的阅读氛围。切换立即生效，并自动保存在此设备。</p>
      <fieldset className="theme-options"><legend className="sr-only">选择界面主题</legend>
        {themes.map(option => <label className="theme-option" key={option.id}>
          <input type="radio" name="theme" value={option.id} checked={theme === option.id} onChange={() => setTheme(option.id)} />
          <span className="theme-option-content">
            <span className="theme-preview" data-theme={option.id} aria-hidden="true">
              <span className="theme-preview-orbit" /><span className="theme-preview-orbit second" />
              <span className="theme-preview-node" /><span className="theme-preview-chat">J<span /><span /></span>
              <span className="theme-preview-reply"><span /><span /></span>
            </span>
            <span className="theme-option-heading"><strong>{option.name}</strong>{option.id === 'aurora' && <small>推荐</small>}<Check className="theme-check" size={18} aria-hidden="true" /></span>
            <span className="theme-option-description">{option.description}</span>
          </span>
        </label>)}
      </fieldset>
      <p className="theme-saved" role="status">当前主题：{themes.find(option => option.id === theme)?.name}</p>
    </section>
  </SubpageLayout>
}
