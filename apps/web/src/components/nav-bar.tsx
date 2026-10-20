import type { ReactNode } from 'react'
import { ArrowLeft } from 'lucide-react'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'

type NavBarProps = {
  title: string
  titleId?: string
  subtitle?: ReactNode
  backTo: string
  backLabel: string
  backVisibility?: 'always' | 'mobile'
  actions?: ReactNode
}

export function NavBar({ title, titleId, subtitle, backTo, backLabel, backVisibility = 'always', actions }: NavBarProps) {
  return <header className="nav-bar">
    <Button asChild variant="ghost" className={`nav-bar-back${backVisibility === 'mobile' ? ' mobile-only' : ''}`}>
      <Link to={backTo} aria-label={backLabel}><ArrowLeft size={20} /></Link>
    </Button>
    <div className="nav-bar-content">
      <h1 id={titleId}>{title}</h1>
      {subtitle && <p>{subtitle}</p>}
    </div>
    {actions && <div className="nav-bar-actions">{actions}</div>}
  </header>
}
