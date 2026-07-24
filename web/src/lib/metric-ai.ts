import { apiRequest } from './api'
import type { MetricDefinition } from './metrics'

export type MetricAuthoringStrategy =
  | 'REUSE_METRIC'
  | 'CREATE_ON_DATASET'
  | 'CREATE_DATASET'
  | 'MODIFY_DATASET'
  | 'DATA_GAP'
  | 'NEEDS_CLARIFICATION'

export type MetricAuthoringRequest = {
  requirement: string
}

export type MetricRetrievalEvidence = {
  sourceType: 'DATASET' | 'FIELD' | 'METRIC'
  sourceId: string
  datasetId: string
  datasetVersionId: string
  reason: string
}

export type MetricAuthoringProposal = {
  schemaVersion: '1.0'
  strategy: MetricAuthoringStrategy
  summary: string
  targetDatasetId: string
  targetDatasetVersionId: string
  reuseMetricVersionId: string
  retrievalEvidence: MetricRetrievalEvidence[]
  candidateMetricDefinition: MetricDefinition | null
  datasetInstruction: string
  clarificationQuestions: string[]
  assumptions: string[]
  warnings: string[]
}

export type MetricAuthoringProposalResult = {
  requestId: string
  retrievalContextHash: string
  intent?: {
    businessGoal: string
    preferredDatasetReferences: string[]
    statisticalObjects: string[]
    aggregation: string
    dateReferences: string[]
    dimensions: string[]
    timeGrain: string
    needsGrouping: boolean
    needsJoin: boolean
    searchTerms: string[]
  }
  proposal: MetricAuthoringProposal
}

/** 仅请求基于已授权内部目录的审核提案；接口不会保存或发布指标/数据集。 */
export const metricAIAPI = {
  propose: (request: MetricAuthoringRequest) => apiRequest<MetricAuthoringProposalResult>('/v1/metrics/ai/proposals', {
    method: 'POST', body: JSON.stringify(request),
  }),
}
