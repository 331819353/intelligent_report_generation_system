import { apiRequest } from './api'
import type { BusinessDomain } from './administration'
import { clearDomain, selectDomain } from './domain-context'

export type TokenPair = {
  accessToken: string
  accessExpiresAt: string
  refreshToken: string
  refreshExpiresAt: string
  tokenType: 'Bearer'
}

export type CurrentProfile = {
  userId: string
  employeeNo: string
  email: string
  displayName: string
  avatarUrl: string
  status: string
  domainId?: string
  roles: string[]
  tokenVersion: number
}

export async function updateCurrentProfile(displayName: string) {
  await apiRequest<void>('/v1/auth/me', { method: 'PATCH', businessDomain: false, body: JSON.stringify({ displayName }) })
}

export async function changeCurrentPassword(currentPassword: string, newPassword: string) {
  await apiRequest<void>('/v1/auth/password', { method: 'PUT', businessDomain: false, body: JSON.stringify({ currentPassword, newPassword }) })
}

const sessionKey = 'intelligent-report-auth'

/** 登录成功后将令牌对保存到当前标签页会话。 */
export async function login(account: string, password: string) {
  const tokens = await apiRequest<TokenPair>('/v1/auth/login', {
    method: 'POST',
    businessDomain: false,
    body: JSON.stringify({ account, password }),
  })
  clearDomain()
  sessionStorage.setItem(sessionKey, JSON.stringify(tokens))
  return tokens
}

/** 注册最小权限账号并保存服务端签发的登录令牌。 */
export async function register(employeeNo: string, displayName: string, email: string, password: string) {
  const tokens = await apiRequest<TokenPair>('/v1/auth/register', {
    method: 'POST',
    businessDomain: false,
    body: JSON.stringify({ employeeNo, displayName, email, password }),
  })
  clearDomain()
  sessionStorage.setItem(sessionKey, JSON.stringify(tokens))
  return tokens
}

/** 读取当前令牌；不存在或格式损坏时返回空。 */
export function currentTokens(): TokenPair | null {
  const value = sessionStorage.getItem(sessionKey)
  if (!value) return null
  try { return JSON.parse(value) as TokenPair } catch { return null }
}

/** 仅解析令牌中的当前用户标识用于界面约束；服务端仍执行最终授权。 */
export function currentSubject() {
  const token = currentTokens()?.accessToken
  const payload = token?.split('.')[1]
  if (!payload) return ''
  try {
    const normalized = payload.replace(/-/g, '+').replace(/_/g, '/')
    const claims = JSON.parse(atob(normalized)) as { sub?: string }
    return typeof claims.sub === 'string' ? claims.sub : ''
  } catch {
    return ''
  }
}

/** 读取当前租户标识，用于平台级配置的作用域展示与提交。 */
export function currentTenantID() {
  const token = currentTokens()?.accessToken
  const payload = token?.split('.')[1]
  if (!payload) return ''
  try {
    const normalized = payload.replace(/-/g, '+').replace(/_/g, '/')
    const claims = JSON.parse(atob(normalized)) as { tenantId?: string }
    return typeof claims.tenantId === 'string' ? claims.tenantId : ''
  } catch {
    return ''
  }
}

/** 从服务端读取实时用户资料，避免导航栏长期显示匿名占位名称。 */
export async function currentProfile() {
  return apiRequest<CurrentProfile>('/v1/auth/me', {
    businessDomain: false,
    cache: 'no-store',
  })
}

/** 清除当前标签页保存的认证信息。 */
export function clearTokens() {
  sessionStorage.removeItem(sessionKey)
  clearDomain()
}

/** 因领域停用或会话撤销而强制退出，并立即通知受保护路由。 */
export function forceLogout(reason = 'SESSION_EXPIRED') {
  clearTokens()
  window.dispatchEvent(new CustomEvent('auth-expired', { detail: { reason } }))
}

/** 仅同步服务端会话领域，不触发页面重载。 */
export async function bindBusinessDomain(domainID: string) {
  await apiRequest<void>('/v1/auth/domain', {
    method: 'PUT',
    businessDomain: false,
    body: JSON.stringify({ domainId: domainID }),
  })
}

/** 验证并切换服务端会话领域，成功后再更新本地领域和业务页面。 */
export async function switchBusinessDomain(domain: BusinessDomain) {
  await bindBusinessDomain(domain.id)
  selectDomain(domain)
}

/** 尝试撤销服务端会话，并始终清理本地令牌。 */
export async function logout() {
  const tokens = currentTokens()
  try {
    if (tokens?.refreshToken) await apiRequest<void>('/v1/auth/logout', {
      method: 'POST',
      businessDomain: false,
      body: JSON.stringify({ refreshToken: tokens.refreshToken }),
    })
  } finally { clearTokens() }
}
