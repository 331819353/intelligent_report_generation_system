import { apiRequest } from './api'

export type OperationalWindow = '1h' | '6h' | '24h' | '7d'
export type OperationalHealth = 'HEALTHY' | 'ATTENTION' | 'CRITICAL'

export type AIUsage = {
  enabled: boolean
  requestsToday: number
  requestsDailyLimit: number
  requestUtilization: number
  tokensThisMonth: number
  tokensMonthlyLimit: number
  tokenUtilization: number
  costMicrosThisMonth: number
  costMicrosMonthlyLimit: number
  costUtilization: number
  requestsInWindow: number
  succeededInWindow: number
  failedInWindow: number
  runningInWindow: number
  successRate: number
  averageLatencyMs: number
  p95LatencyMs: number
}

export type AskDataUsage = {
  runsInWindow: number
  answeredInWindow: number
  blockedInWindow: number
  clarificationInWindow: number
  activeInWindow: number
  answerRate: number
  averageDurationMs: number
  p95DurationMs: number
}

export type OperationalQueue = {
  code: string
  name: string
  pending: number
  running: number
  failed: number
  oldestPendingSeconds: number
  status: OperationalHealth
}

export type OperationalFailure = { source: string; code: string; count: number }
export type OperationalPurpose = { purpose: string; count: number; tokens: number; costMicros: number }
export type OperationalSnapshot = {
  generatedAt: string
  window: OperationalWindow
  health: OperationalHealth
  ai: AIUsage
  askData: AskDataUsage
  queues: OperationalQueue[]
  failureCodes: OperationalFailure[]
  purposes: OperationalPurpose[]
}

export const operationalObservabilityAPI = {
  snapshot(window: OperationalWindow) {
    return apiRequest<OperationalSnapshot>(`/v1/platform-management/observability?window=${window}`, { businessDomain: false })
  },
}
