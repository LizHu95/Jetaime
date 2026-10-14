import { createBrowserRouter, Navigate, useLocation } from 'react-router-dom'
import { AuthLayout } from '@/features/auth/auth-layout'
import { AuthForm } from '@/features/auth/auth-form'
import { SessionPage } from '@/features/auth/session-page'
import { useAuth } from '@/features/auth/auth-provider'

function Entry() {
  const { session } = useAuth()
  return <Navigate to={session ? '/auth/session/' : '/auth/login/'} replace />
}

function GuestPage() {
  const { session } = useAuth()
  const { pathname } = useLocation()
  if (session) return <Navigate to="/auth/session/" replace />
  return <AuthForm key={pathname} mode={pathname.includes('/register') ? 'register' : 'login'} />
}

export const router = createBrowserRouter([
  { element: <AuthLayout />, children: [
    { path: '/', element: <Entry /> },
    { path: '/auth/login/', element: <GuestPage /> },
    { path: '/auth/register/', element: <GuestPage /> },
    { path: '/auth/session/', element: <SessionPage /> },
    { path: '*', element: <Entry /> },
  ] },
], { basename: '/page' })
