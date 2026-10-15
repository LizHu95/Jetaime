import React from 'react'
import ReactDOM from 'react-dom/client'
import { RouterProvider } from 'react-router-dom'
import { Providers } from '@/app/providers'
import { router } from '@/app/router'
import './styles.css'
import { pushSupported, registerPushWorker } from '@/features/notifications/push'

if (pushSupported()) void registerPushWorker().catch(() => { /* Settings shows registration failures with a retry action. */ })

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode><Providers><RouterProvider router={router} /></Providers></React.StrictMode>,
)
