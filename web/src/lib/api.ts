export type APIError = {
  code: string
  message: string
  requestId?: string
  reasonCode?: string
  stage?: string
  repairAttempted?: boolean
  diagnosticCode?: string
  suggestion?: string
  details?: Array<{ path: string; code?: string; reason: string }>
  /** 乐观锁冲突返回服务端最新基线，页面仍需显式让用户选择是否加载。 */
  currentRevision?: number
  currentHash?: string
}

export class RequestError extends Error {
  /** 保留服务端错误结构和 HTTP 状态，便于页面精确展示。 */
  constructor(public readonly detail: APIError, public readonly status: number) {
    // 发布校验可能同时返回多个路径；完整保留稳定代码，避免设计器只展示第一条遗漏。
    const issues = detail.details?.map(issue => `${issue.path}${issue.code ? ` [${issue.code}]` : ''} ${issue.reason}`).join('；')
    super(issues ? `${detail.message}：${issues}` : detail.message)
  }
}

const authSessionKey = 'intelligent-report-auth'
type StoredTokens = { accessToken: string; refreshToken: string; accessExpiresAt: string; refreshExpiresAt: string; tokenType: 'Bearer' }

/** 从当前标签页会话中安全读取令牌，损坏数据按未登录处理。 */
function tokens(): StoredTokens | null {
  const value = sessionStorage.getItem(authSessionKey)
  if (!value) return null
  try { return JSON.parse(value) as StoredTokens } catch { return null }
}

/** 清理失效会话并通知路由守卫。 */
function expireSession() {
  sessionStorage.removeItem(authSessionKey)
  window.dispatchEvent(new Event('auth-expired'))
}

let refreshRequest: Promise<StoredTokens> | null = null
/** 合并并发刷新请求，避免同一旧刷新令牌被重复轮换。 */
async function refreshTokens(): Promise<StoredTokens> {
  if (refreshRequest) return refreshRequest
  const current = tokens()
  if (!current?.refreshToken) throw new Error('refresh token is missing')
  refreshRequest = fetch('/api/v1/auth/refresh', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refreshToken: current.refreshToken }),
  }).then(async response => {
    if (!response.ok) throw new Error('refresh failed')
    const next = await response.json() as StoredTokens
    sessionStorage.setItem(authSessionKey, JSON.stringify(next))
    return next
  }).finally(() => { refreshRequest = null })
  return refreshRequest
}

/** 发送统一 API 请求；遇到 401 时仅刷新一次并重放原请求。 */
export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const isMultipart = typeof FormData !== 'undefined' && init.body instanceof FormData
  const request = async (accessToken?: string) => fetch(`/api${path}`, {
    ...init,
    headers: { ...(!isMultipart ? { 'Content-Type': 'application/json' } : {}), ...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}), ...init.headers },
  })
  let current = tokens()
  let response = await request(current?.accessToken)
  // 认证端点不参与自动刷新，防止失败请求形成递归循环。
  if (response.status === 401 && !['/v1/auth/login', '/v1/auth/refresh', '/v1/auth/logout'].includes(path) && current?.refreshToken) {
    try { current = await refreshTokens(); response = await request(current.accessToken) } catch { expireSession() }
  }
  if (!response.ok) {
    const fallback: APIError = { code: 'REQUEST_FAILED', message: '请求失败，请稍后重试' }
    const detail = await response.json().catch(() => fallback) as APIError
    throw new RequestError(detail, response.status)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
