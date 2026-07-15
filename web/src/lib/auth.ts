import { apiRequest } from './api'

export type TokenPair = {
  accessToken: string
  accessExpiresAt: string
  refreshToken: string
  refreshExpiresAt: string
  tokenType: 'Bearer'
}

const sessionKey = 'intelligent-report-auth'

/** 登录成功后将令牌对保存到当前标签页会话。 */
export async function login(tenantCode: string, email: string, password: string) {
  const tokens = await apiRequest<TokenPair>('/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify({ tenantCode, email, password }),
  })
  sessionStorage.setItem(sessionKey, JSON.stringify(tokens))
  return tokens
}

/** 读取当前令牌；不存在或格式损坏时返回空。 */
export function currentTokens(): TokenPair | null {
  const value = sessionStorage.getItem(sessionKey)
  if (!value) return null
  try { return JSON.parse(value) as TokenPair } catch { return null }
}

/** 清除当前标签页保存的认证信息。 */
export function clearTokens() { sessionStorage.removeItem(sessionKey) }

/** 尝试撤销服务端会话，并始终清理本地令牌。 */
export async function logout() {
  const tokens = currentTokens()
  try {
    if (tokens?.refreshToken) await apiRequest<void>('/v1/auth/logout', { method: 'POST', body: JSON.stringify({ refreshToken: tokens.refreshToken }) })
  } finally { clearTokens() }
}
