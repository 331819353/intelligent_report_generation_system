import { apiRequest } from './api'
import type { MetricDefinition, MetricRecord } from './metrics'

export type MetricCandidateStatus = 'READY' | 'NEEDS_REVIEW' | 'BLOCKED' | 'ACCEPTED' | 'REJECTED'
export type MetricCandidateMethod = 'RULE' | 'LLM' | 'HYBRID'

export type MetricCandidateEvidence = {
  property: string
  source: string
  detail: string
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
  evidence: MetricCandidateEvidence[]
  assumptions: string[]
  warnings: string[]
  blockReasons: string[]
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

export type ListMetricCandidatesInput = {
  status?: MetricCandidateStatus
  datasetId?: string
  limit?: number
  offset?: number
}

export type AcceptMetricCandidateResult = {
  candidate: MetricCandidate
  metric: MetricRecord
}

const candidatePath = (id: string) => `/v1/metric-candidates/${encodeURIComponent(id)}`

/** 候选接口只传递结构化、可审计的提取结果；接受时由服务端原子创建指标草稿。 */
export const metricCandidateAPI = {
  list: ({ status, datasetId, limit = 200, offset = 0 }: ListMetricCandidatesInput = {}) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    if (status) query.set('status', status)
    if (datasetId) query.set('datasetId', datasetId)
    return apiRequest<MetricCandidatePage>(`/v1/metric-candidates?${query}`, { cache: 'no-store' })
  },
  accept: (id: string, expectedVersion: number) => apiRequest<AcceptMetricCandidateResult>(`${candidatePath(id)}/accept`, {
    method: 'POST', body: JSON.stringify({ expectedVersion }),
  }),
  reject: (id: string, expectedVersion: number, reason: string) => apiRequest<MetricCandidate>(`${candidatePath(id)}/reject`, {
    method: 'POST', body: JSON.stringify({ expectedVersion, reason }),
  }),
}
