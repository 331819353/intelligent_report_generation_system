import { PropsWithChildren, useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { currentTokens } from '../lib/auth'

/** 保护需要登录的路由，并在会话失效事件发生时立即跳转登录页。 */
export function RequireAuth({ children }: PropsWithChildren) {
  const location = useLocation()
  const [authenticated, setAuthenticated] = useState(() => Boolean(currentTokens()?.accessToken))
  // API 层刷新失败时通过全局事件通知所有受保护页面退出。
  useEffect(() => {
    const expired = () => setAuthenticated(false)
    window.addEventListener('auth-expired', expired)
    return () => window.removeEventListener('auth-expired', expired)
  }, [])
  if (!authenticated) return <Navigate to="/login" state={{ from: location.pathname }} replace />
  return children
}
