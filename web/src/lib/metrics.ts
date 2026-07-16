import { apiRequest } from './api'
import type { DatasetPreview } from './datasets'

export type MetricType = 'ATOMIC' | 'DERIVED' | 'RATIO'
export type MetricAggregation = 'NONE' | 'SUM' | 'AVG' | 'MIN' | 'MAX' | 'COUNT' | 'COUNT_DISTINCT'
export type MetricTimeGrain = 'NONE' | 'DAY' | 'WEEK' | 'MONTH' | 'QUARTER' | 'YEAR'
export type MetricAdditivity = 'ADDITIVE' | 'SEMI_ADDITIVE' | 'NON_ADDITIVE'
export type MetricStatus = 'DRAFT' | 'PUBLISHED' | 'STALE' | 'DEPRECATED'
export type MetricVersionStatus = 'PUBLISHED' | 'STALE' | 'DEPRECATED'

export type MetricExpression =
  | { type: 'FIELD_REF'; fieldId: string }
  | { type: 'METRIC_REF'; metricVersionId: string }
  | { type: 'LITERAL'; value: string }
  | { type: 'ADD' | 'SUBTRACT' | 'MULTIPLY' | 'DIVIDE'; arguments: MetricExpression[] }

export type MetricDimension = {
  fieldId: string
  name: string
  hierarchyFieldIds: string[]
  sortDirection: 'ASC' | 'DESC'
  nullLabel: string
}

export type MetricDefinition = {
  schemaVersion: '1.0'
  metric: { code: string; name: string; description: string; type: MetricType }
  datasetId: string
  datasetVersionId: string
  expression: MetricExpression
  aggregation: MetricAggregation
  unit: string
  numberFormat: string
  timeFieldId?: string
  timeGrain: MetricTimeGrain
  additivity: MetricAdditivity
  nonAdditiveDimensionFieldIds: string[]
  allowedDimensions: MetricDimension[]
  decimalScale: number
  roundingMode: 'HALF_UP'
  nullHandling: 'IGNORE'
  divisionByZero: 'NULL'
}

export type MetricRecord = {
  id: string
  code: string
  name: string
  description: string
  type: MetricType
  status: MetricStatus
  version: number
  draftVersionId: string
  draftVersionNo: number
  draftRecordVersion: number
  currentPublishedVersionId?: string
  datasetId: string
  datasetVersionId: string
  definitionHash: string
  definition: MetricDefinition
  createdAt: string
  updatedAt: string
}

export type MetricSummary = Pick<MetricRecord,
  'id' | 'code' | 'name' | 'description' | 'status' | 'version' | 'currentPublishedVersionId'> & {
  datasetId: string
  datasetVersionId: string
  type: MetricType
  updatedAt: string
}

export type MetricPage = {
  items: MetricSummary[]
  total: number
  limit: number
  offset: number
}

export type MetricVersionRecord = {
  id: string
  metricId: string
  metricRecordVersion: number
  datasetId: string
  datasetVersionId: string
  draftVersionId: string
  draftRecordVersion: number
  versionNo: number
  status: MetricVersionStatus
  definitionHash: string
  definition: MetricDefinition
  publishedAt: string
  publishedBy: string
}

export type MetricVersionSummary = Pick<MetricVersionRecord,
  'id' | 'metricId' | 'versionNo' | 'status' | 'datasetId' | 'datasetVersionId' |
  'draftRecordVersion' | 'definitionHash' | 'publishedAt' | 'publishedBy'>

export type MetricVersionPage = {
  items: MetricVersionSummary[]
  total: number
  limit: number
  offset: number
}

export type MetricUsage = {
  reportDraftReferences: number
  downstreamDraftReferences: number
  downstreamPublishedReferences: number
  activeQueryRuns: number
}

export type MetricPermissionAction = 'READ' | 'MANAGE' | 'PUBLISH'

export type UpdateMetricInput = {
  expectedVersion: number
  expectedDraftRecordVersion: number
  expectedDefinitionHash: string
  definition: MetricDefinition
}

export type PublishMetricInput = {
  draftVersionId: string
  expectedVersion: number
  expectedDraftRecordVersion: number
  expectedDefinitionHash: string
  validationParameters: Record<string, unknown>
}

export type PreviewMetricInput = {
  queryId: string
  parameters: Record<string, unknown>
  dimensionFieldIds: string[]
  maxRows: number
}

export type MetricVersionTransitionInput = {
  expectedVersion: number
  expectedStatus: MetricVersionStatus
  targetStatus: 'DEPRECATED'
}

const metricPath = (id: string) => `/v1/metrics/${encodeURIComponent(id)}`
const metricVersionPath = (id: string, versionId: string) =>
  `${metricPath(id)}/versions/${encodeURIComponent(versionId)}`

/** 指标 API 始终以精确草稿或发布版本为输入，前端不计算任何业务指标。 */
export const metricAPI = {
  list: (limit = 50, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<MetricPage>(`/v1/metrics?${query}`, { cache: 'no-store' })
  },
  get: (id: string) => apiRequest<MetricRecord>(metricPath(id), { cache: 'no-store' }),
  create: (definition: MetricDefinition) => apiRequest<MetricRecord>('/v1/metrics', {
    method: 'POST', body: JSON.stringify({ definition }),
  }),
  update: (id: string, input: UpdateMetricInput) => apiRequest<MetricRecord>(`${metricPath(id)}/draft`, {
    method: 'PUT', body: JSON.stringify(input),
  }),
  validate: (id: string) => apiRequest<{
    valid: boolean; definition?: MetricDefinition; definitionHash?: string
  }>(`${metricPath(id)}/validate`, {
    // 当前草稿已由指标 ID 唯一确定，严格接口不接受客户端夹带另一份定义或参数。
    method: 'POST', body: '{}',
  }),
  preview: (id: string, input: PreviewMetricInput) => apiRequest<DatasetPreview>(`${metricPath(id)}/preview`, {
    method: 'POST', body: JSON.stringify(input),
  }),
  publish: (id: string, input: PublishMetricInput, idempotencyKey: string) => apiRequest<MetricVersionRecord>(`${metricPath(id)}/publish`, {
    method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(input),
  }),
  listVersions: (id: string, limit = 50, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<MetricVersionPage>(`${metricPath(id)}/versions?${query}`, { cache: 'no-store' })
  },
  getVersion: (id: string, versionId: string) => apiRequest<MetricVersionRecord>(metricVersionPath(id, versionId), { cache: 'no-store' }),
  getVersionUsage: (id: string, versionId: string) => apiRequest<MetricUsage>(`${metricVersionPath(id, versionId)}/usage`, { cache: 'no-store' }),
  previewVersion: (id: string, versionId: string, input: PreviewMetricInput) => apiRequest<DatasetPreview>(`${metricVersionPath(id, versionId)}/preview`, {
    method: 'POST', body: JSON.stringify(input),
  }),
  transitionVersion: (id: string, versionId: string, input: MetricVersionTransitionInput) => apiRequest<MetricVersionRecord>(`${metricVersionPath(id, versionId)}/status`, {
    method: 'POST', body: JSON.stringify(input),
  }),
  evaluatePermission: (id: string, action: MetricPermissionAction) => apiRequest<{ allowed: boolean }>('/v1/permissions/evaluate', {
    method: 'POST', body: JSON.stringify({ resourceType: 'METRIC', action, objectId: id }),
  }),
}

/** 为冻结后的发布候选生成可在模糊失败时复用的幂等键。 */
export function createMetricPublishIdempotencyKey(): string {
  return globalThis.crypto.randomUUID()
}
