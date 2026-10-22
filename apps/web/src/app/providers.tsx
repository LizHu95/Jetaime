import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RealtimeProvider } from '@/features/chat/realtime-provider'
import { AuthProvider } from '@/features/auth/auth-provider'
import type { ReactNode } from 'react'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, retry: 1 },
    mutations: { retry: false, gcTime: 0 },
  },
})

export function Providers({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}><AuthProvider><RealtimeProvider>{children}</RealtimeProvider></AuthProvider></QueryClientProvider>
}
