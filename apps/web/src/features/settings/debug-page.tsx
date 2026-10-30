import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Navigate } from 'react-router-dom'
import { SubpageLayout } from '@/components/subpage-layout'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/features/auth/auth-provider'
import { apiRequest, errorMessage } from '@/lib/api'

type Deployment = { branch: string; commit: string; dirty: string }

export function DebugPage() {
  const { session } = useAuth()
  useEffect(() => { document.title = '开发调试 · Jetaime' }, [])
  const deployment = useQuery({
    queryKey: ['deployment', session?.user.id],
    queryFn: ({ signal }) => apiRequest<Deployment>('/deployment', { method: 'GET', token: session!.token, signal }),
    enabled: !!session,
    staleTime: 0,
  })
  if (!session) return <Navigate to="/auth/login/" replace />
  return <SubpageLayout className="settings-page" title="开发调试" backTo="/me/" backLabel="返回我">
    <section aria-labelledby="deployment-title">
      <h2 id="deployment-title">当前服务部署</h2>
      <p className="theme-description">运行中后端服务的代码版本，部署时自动记录。</p>
      {deployment.isPending ? <p role="status">正在读取部署信息…</p>
        : deployment.isError ? <div role="alert"><p>{errorMessage(deployment.error)}</p><Button onClick={() => deployment.refetch()}>重试</Button></div>
          : <dl className="deployment-info">
            <div><dt>分支</dt><dd>{deployment.data.branch || '未记录'}</dd></div>
            <div><dt>提交哈希</dt><dd><code>{deployment.data.commit || '未记录'}</code></dd></div>
            <div><dt>打包时的代码状态</dt><dd>{deployment.data.dirty === 'true' ? '包含未提交修改' : deployment.data.dirty === 'false' ? '全部已提交' : '未记录'}</dd></div>
          </dl>}
    </section>
  </SubpageLayout>
}
