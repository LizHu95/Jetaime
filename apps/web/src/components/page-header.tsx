import { ArrowLeft } from 'lucide-react'
import { Link } from 'react-router-dom'

export function PageHeader({ title, backTo, backLabel }: { title: string; backTo: string; backLabel: string }) {
  return <header className="subpage-header">
    <Link to={backTo} aria-label={backLabel}><ArrowLeft size={22} /></Link>
    <h1>{title}</h1>
  </header>
}
