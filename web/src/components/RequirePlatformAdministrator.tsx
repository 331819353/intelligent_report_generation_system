import { PropsWithChildren, useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { administrationAPI } from '../lib/administration'

/**
 * 平台管理中心只向平台管理员开放。侧栏隐藏入口只是展示约束，这里再对
 * 直接访问路由做授权，避免普通用户短暂看到管理页面或触发管理数据请求。
 */
export function RequirePlatformAdministrator({ children }: PropsWithChildren) {
  const location = useLocation()
  const designSnapshot = import.meta.env.DEV && Boolean(new URLSearchParams(location.search).get('snapshot'))
  const [allowed, setAllowed] = useState<boolean | null>(null)

  useEffect(() => {
    if (designSnapshot) return undefined
    let cancelled = false
    void administrationAPI.canManage()
      .then(result => {
        if (!cancelled) setAllowed(result)
      })
      .catch(() => {
        if (!cancelled) setAllowed(false)
      })
    return () => { cancelled = true }
  }, [designSnapshot])

  const resolvedAllowed = designSnapshot ? true : allowed
  if (resolvedAllowed === null) return null
  if (!resolvedAllowed) return <Navigate to="/data-sources" replace />
  return children
}
