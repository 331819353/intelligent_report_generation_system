import { PropsWithChildren, useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { administrationAPI } from '../lib/administration'
import { bindBusinessDomain, switchBusinessDomain } from '../lib/auth'
import { clearDomain, currentDomain } from '../lib/domain-context'

/**
 * 数据与业务页面要求明确的领域上下文。平台管理员可进入全部启用领域；
 * 领域管理员和普通成员只能进入服务端返回的已授权领域。
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
    ]).then(async ([platformAdministrator, domains]) => {
      const activeDomains = domains.filter(domain => domain.status === 'ACTIVE')
      if (activeDomains.length === 0) {
        if (!cancelled) setDestination(platformAdministrator ? 'platform' : 'access')
        return
      }

      const stored = currentDomain()
      const selected = activeDomains.find(domain => domain.id === stored?.id)
        ?? activeDomains.find(domain => domain.default)
        ?? activeDomains[0]
      if (stored && stored.id !== selected.id) clearDomain()

      // 在业务页面挂载前完成服务端会话绑定和本地请求头上下文写入，避免页面
      // 首批接口与侧栏初始化竞速而随机返回 BUSINESS_DOMAIN_REQUIRED。
      if (stored?.id === selected.id) await bindBusinessDomain(selected.id)
      else await switchBusinessDomain(selected)
      if (!cancelled) setDestination('allowed')
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
