import { PropsWithChildren, useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { administrationAPI } from '../lib/administration'

/**
 * 数据配置页要求明确选择活动领域。平台管理员无需领域成员关系即可进入任意
 * 活动领域；尚未加入领域的普通用户进入领域申请页。
 */
export function RequireBusinessDomain({ children }: PropsWithChildren) {
  const location = useLocation()
  const designSnapshot = import.meta.env.DEV && Boolean(new URLSearchParams(location.search).get('snapshot'))
  const [destination, setDestination] = useState<'allowed' | 'platform' | 'access' | null>(null)

  useEffect(() => {
    if (designSnapshot) return undefined
    let cancelled = false
    void Promise.all([
      administrationAPI.canManage(),
      administrationAPI.listDomains(),
    ]).then(([platformAdministrator, domains]) => {
      if (!cancelled) {
        const hasActiveDomain = domains.some(domain => domain.status === 'ACTIVE')
        setDestination(hasActiveDomain ? 'allowed' : platformAdministrator ? 'platform' : 'access')
      }
    }).catch(() => {
      if (!cancelled) setDestination('access')
    })
    return () => { cancelled = true }
  }, [designSnapshot])

  const resolvedDestination = designSnapshot ? 'allowed' : destination
  if (resolvedDestination === null) return null
  if (resolvedDestination === 'platform') return <Navigate to="/platform-management/domains" replace />
  if (resolvedDestination === 'access') return <Navigate to="/domain-access" replace />
  return children
}
