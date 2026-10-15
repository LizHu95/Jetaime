import { createBrowserRouter, Navigate, useLocation } from 'react-router-dom'
import { ChatPage } from '@/features/chat/chat-page'
import { NotificationsPage } from '@/features/notifications/notifications-page'
import { AuthLayout } from '@/features/auth/auth-layout'
import { AuthForm } from '@/features/auth/auth-form'
import { ContactsPage } from '@/features/contacts/contacts-page'
import { PasswordPage } from '@/features/auth/password-page'
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
  { path: '/chat/', element: <ChatPage /> },
  { path: '/chat/:groupId/', element: <ChatPage /> },
  { element: <AuthLayout />, children: [
    { path: '/notifications/', element: <NotificationsPage /> },
    { path: '/', element: <Entry /> },
    { path: '/auth/login/', element: <GuestPage /> },
    { path: '/auth/register/', element: <GuestPage /> },
    { path: '/auth/session/', element: <SessionPage /> },
    { path: '/auth/password/', element: <PasswordPage /> },
    { path: '/contacts/', element: <ContactsPage /> },
    { path: '*', element: <Entry /> },
  ] },
], { basename: '/page' })
