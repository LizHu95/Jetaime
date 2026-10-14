import { ArrowUpRight, Check, CircleDot, MessageSquare, Sparkles } from 'lucide-react'
import { Link, Outlet, useLocation } from 'react-router-dom'

export function Brand() {
  return <Link className="brand" to="/" aria-label="Jetaime 首页">
    <span className="brand-mark"><Sparkles size={23} strokeWidth={1.6} /></span>
    <span>Jetaime<span className="brand-dot">.</span></span>
  </Link>
}

export function AuthLayout() {
  const { pathname } = useLocation()
  const isRegister = pathname.includes('/register')
  const isSession = pathname.includes('/session')
  return <div className="auth-shell">
    <header className="site-header">
      <Brand />
      {!isSession && <div className="header-action">
        <span>{isRegister ? '已经有账号？' : '还没有账号？'}</span>
        <Link to={isRegister ? '/auth/login/' : '/auth/register/'}>
          {isRegister ? '登录' : '创建账号'} <ArrowUpRight size={15} />
        </Link>
      </div>}
    </header>
    <main className="auth-main" id="main-content">
      <section className="story-panel" aria-label="关于 Jetaime">
        <div className="eyebrow"><span className="live-dot" /> 日常协同 AI 助理</div>
        <h1>让对话，<br /><span>成为行动。</span></h1>
        <p className="story-description">一起聊过的事，值得被记住。<br />让 AI 帮你找回共识，理清计划，迈出下一步。</p>
        <div className="collaboration-visual" aria-hidden="true">
          <div className="visual-orbit orbit-one" /><div className="visual-orbit orbit-two" />
          <div className="orbit-point" />
          <div className="visual-axis" />
          <div className="conversation-chip"><span className="mini-avatar">Y</span><span>周末一起出去走走吧</span><span className="chip-time">刚刚</span></div>
          <div className="assistant-card">
            <div className="assistant-card-head"><span className="ai-avatar"><Sparkles size={18} /></span><span>Jetaime AI<small>让想法更进一步</small></span><span className="ai-badge">AI</span></div>
            <p>根据刚才的讨论，整理了一个待办。</p>
            <div className="demo-task"><span className="demo-check"><Check size={15} /></span><div>确认周末行程<small>明天 · 待确认</small></div><ArrowUpRight size={17} /></div>
            <div className="source-line"><MessageSquare size={12} /> 来自对话的线索<span>协作场景示意</span></div>
          </div>
          <div className="memory-chip"><span><CircleDot size={15} /></span>记住共识，连接下一步</div>
          <span className="visual-coordinate">CONVERSATION → ACTION</span>
        </div>
        <div className="story-features"><span>对话里的线索</span><i /><span>有依据的回答</span><i /><span>一起完成的计划</span></div>
      </section>
      <section className="form-panel" aria-label="账号认证"><Outlet /></section>
    </main>
    <footer className="site-footer"><span>© {new Date().getFullYear()} Jetaime</span><span>让日常协作，多一点默契。<Sparkles size={12} /></span></footer>
  </div>
}
