import { useId, type ReactNode } from 'react'
import { NavBar } from './nav-bar'
import { cn } from '@/lib/utils'

type SubpageLayoutProps = {
  title: string
  backTo: string
  backLabel: string
  subtitle?: ReactNode
  actions?: ReactNode
  className?: string
  as?: 'main' | 'section'
  children: ReactNode
}

// The navigation stays visible while the shared content area scrolls.
export function SubpageLayout({ title, backTo, backLabel, subtitle, actions, className, as: Component = 'main', children }: SubpageLayoutProps) {
  const titleId = useId()
  return <Component className={cn('subpage-layout', className)} aria-labelledby={titleId}>
    <div className="subpage-navigation"><NavBar title={title} titleId={titleId} backTo={backTo} backLabel={backLabel} subtitle={subtitle} actions={actions} /></div>
    <div className="subpage-body"><div className="subpage-content">{children}</div></div>
  </Component>
}
