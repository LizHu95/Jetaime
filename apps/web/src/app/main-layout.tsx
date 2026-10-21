import { MessageSquare, UserRound } from 'lucide-react'
import { Link, Outlet, useLocation, useMatch } from 'react-router-dom'
import { useKeyboardViewport } from './use-keyboard-viewport'

export function MainLayout() {
  const { pathname } = useLocation()
  const inConversation = !!useMatch('/chat/:groupId/*')
  const root = useKeyboardViewport()
  const inAccount = pathname.startsWith('/me/') || pathname.startsWith('/auth/') || pathname.startsWith('/notifications/')
  return <div className={`main-app${inConversation ? ' without-tabs' : ''}`} ref={root}>
    <div className="main-content"><Outlet /></div>
    {!inConversation && <nav className="bottom-tabs" aria-label="主导航">
      <Link to="/chat/" className={!inAccount ? 'tab active' : 'tab'} aria-current={!inAccount ? 'page' : undefined}><MessageSquare size={23} /><span>聊天</span></Link>
      <Link to="/me/" className={inAccount ? 'tab active' : 'tab'} aria-current={inAccount ? 'page' : undefined}><UserRound size={23} /><span>我</span></Link>
    </nav>}
  </div>
}
