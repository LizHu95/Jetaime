import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { LocationAccessError } from './location'

type PolicyDocument = Document & { permissionsPolicy?: { allowsFeature: (feature: string) => boolean }; featurePolicy?: { allowsFeature: (feature: string) => boolean } }
const permissionLabels = { granted: '已允许', denied: '已拒绝（可能来自网站或系统设置）', prompt: '等待询问', unknown: '浏览器未提供权限状态' }

export function LocationPermissionHelp({ error, busy, onRetry }: { error: Error; busy: boolean; onRetry: () => void }) {
  const message = error.message
  const standalone = window.matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone === true
  const [permission, setPermission] = useState<keyof typeof permissionLabels>('unknown')
  const ios = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  const policy = (document as PolicyDocument).permissionsPolicy || (document as PolicyDocument).featurePolicy
  const blocked = policy ? !policy.allowsFeature('geolocation') : null
  useEffect(() => {
    let active = true
    async function inspect() {
      try {
        const status = await navigator.permissions.query({ name: 'geolocation' })
        if (active) setPermission(status.state)
      } catch { /* Safari versions can omit this query; do not infer denial. */ }
    }
    void inspect()
    return () => { active = false }
  }, [message])
  return <div className="location-permission-help">
    <p className="account-error" role="alert">{blocked ? '当前页面的权限策略禁止定位，请联系站点管理员。' : message}</p>
    {ios && !blocked && <ol>
      <li>打开 iPhone「设置 → 隐私与安全性 → 定位服务」，确认定位服务已开启，并检查 Safari 网站或此网页 App 的位置权限。</li>
      <li>在 Safari 中打开本站，进入页面菜单的「网站设置」，将「位置」设为「询问」或「允许」。</li>
      <li>如果从主屏幕打开，请关闭后重新打开，再点下方「重试定位」。</li>
    </ol>}
    <div className="location-recovery-actions"><Button variant="outline" type="button" disabled={busy || blocked === true} onClick={onRetry}>{busy ? '定位中…' : '重试定位'}</Button>{ios && <a href="https://support.apple.com/zh-cn/102647" target="_blank" rel="noreferrer">Apple 定位设置说明</a>}</div>
    <details><summary>查看定位权限检测</summary>
      <p>当前站点：{window.location.origin}</p>
      <p>运行模式：{standalone ? '主屏幕网页 App（PWA）' : '浏览器页面'}</p>
      <p>安全连接：{window.isSecureContext ? '是' : '否'}；定位接口：{navigator.geolocation ? '可用' : '不可用'}</p>
      {error instanceof LocationAccessError && <><p>定位错误码：{error.code}</p><p>原始错误：{error.nativeMessage}</p></>}
      <p>浏览器权限查询：{permissionLabels[permission]}，仅供参考</p>
      <p>页面权限策略：{blocked === null ? '浏览器未提供检测接口' : blocked ? '禁止定位' : '允许定位'}</p>
      <p>浏览器版本：{navigator.userAgent}</p>
    </details>
  </div>
}
