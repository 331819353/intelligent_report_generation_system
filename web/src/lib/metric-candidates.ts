import { apiRequest } from './api'
import type { MetricDefinition, MetricRecord } from './metrics'

export type MetricCandidateStatus = 'READY' | 'NEEDS_REVIEW' | 'BLOCKED' | 'ACCEPTED' | 'REJECTED'
export type MetricCandidateMethod = 'RULE' | 'LLM' | 'HYBRID'

export type MetricCandidateSemantic = {
  name: string
  description: string
  caliber: string
  dimensions: string[]
  period: string
  periodDescription: string
  lineageSummary: string
  tags: string[]
  source: 'RULE' | 'HYBRID' | 'RULE_FALLBACK'
}

export type MetricCandidate = {
  id: string
  datasetId: string
  datasetVersionId: string
  dslHash: string
  name: string
  code: string
  description: string
  status: MetricCandidateStatus
  method: MetricCandidateMethod
  confidence: number
  proposedDefinition: MetricDefinition
  sourceFieldIds: string[]
  evidence: Array<{ property: string; source: string; detail: string }>
  assumptions: string[]
  warnings: string[]
  blockReasons: string[]
  semantic?: MetricCandidateSemantic
  fingerprint: string
  version: number
  acceptedMetricId?: string
  createdAt: string
  updatedAt: string
}

export type MetricCandidatePage = {
  items: MetricCandidate[]
  total: number
  limit: number
  offset: number
}

export type AcceptMetricCandidateResult = {
  candidate: MetricCandidate
  metric: MetricRecord
}

const candidatePath = (id: string) => `/v1/metric-candidates/${encodeURIComponent(id)}`

export const metricCandidateAPI = {
  list: (limit = 200, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<MetricCandidatePage>(`/v1/metric-candidates?${query}`, { cache: 'no-store' })
  },
  accept: (id: string, expectedVersion: number) => apiRequest<AcceptMetricCandidateResult>(`${candidatePath(id)}/accept`, {
    method: 'POST', body: JSON.stringify({ expectedVersion }),
  }),
  reject: (id: string, expectedVersion: number, reason: string) => apiRequest<MetricCandidate>(`${candidatePath(id)}/reject`, {
    method: 'POST', body: JSON.stringify({ expectedVersion, reason }),
  }),
}
