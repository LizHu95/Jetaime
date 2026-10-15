import { MessageSquare, UserRound } from 'lucide-react'
import { Link, Outlet, useLocation } from 'react-router-dom'

export function MainLayout() {
  const { pathname } = useLocation()
  const inAccount = pathname === '/me/' || pathname.startsWith('/auth/') || pathname.startsWith('/notifications/')
  return <div className="main-app">
    <div className="main-content"><Outlet /></div>
    <nav className="bottom-tabs" aria-label="主导航">
      <Link to="/chat/" className={!inAccount ? 'tab active' : 'tab'} aria-current={!inAccount ? 'page' : undefined}><MessageSquare size={23} /><span>聊天</span></Link>
      <Link to="/me/" className={inAccount ? 'tab active' : 'tab'} aria-current={inAccount ? 'page' : undefined}><UserRound size={23} /><span>我</span></Link>
    </nav>
  </div>
}
