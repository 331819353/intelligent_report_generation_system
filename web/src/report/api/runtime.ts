import { apiRequest } from '../../lib/api'
import type { ReportComponentState } from '../runtime/state'

export type RuntimeFilter = {
  id: string
  type: 'SINGLE_SELECT' | 'MULTI_SELECT' | 'DATE' | 'DATE_RANGE' | 'RELATIVE_TIME' | 'NUMBER_RANGE' | 'SEARCH_SELECT' | 'PARAMETER_INPUT' | 'SELECT' | 'BOOLEAN'
  fieldRef: { dataContextId: string; field: string }
  scope: { type: string; targetIds: string[] }
  defaultValue?: { mode?: string; unit?: string; offset?: number; values?: string[]; minimum?: number; maximum?: number; boolean?: boolean }
}

export type RuntimeSlot = { id: string; componentId?: string }
export type RuntimeBlock = { id: string; type: string; zones: Array<{ id: string; type: string; slots: RuntimeSlot[] }> }
export type RuntimeSection = { id: string; name: string; order: number; blocks: RuntimeBlock[] }
export type RuntimePage = { id: string; name: string; order: number; sections: RuntimeSection[] }
export type RuntimeComponent = {
  id: string
  templateRef: { type: string; version: string }
  dataBinding?: { bindingMode: 'SEMANTIC_IR' | 'DATASET_FIELD' }
  options: { title?: string; subtitle?: string; richText?: string; numberFormat?: string; showLegend?: boolean; showLabel?: boolean }
}

export type RuntimeDefinition = {
  metadata: { id: string; code: string; name: string; description?: string; reportType: 'REPORT' | 'DASHBOARD'; locale?: string }
  globalFilters: RuntimeFilter[]
  pages: RuntimePage[]
  components: RuntimeComponent[]
  runtimePolicy: { refreshMode: 'MANUAL' | 'ON_OPEN' | 'INTERVAL'; refreshIntervalSeconds: number; maxConcurrentQueries: number; componentTimeoutMs: number; exportEnabled: boolean; failureMode: 'PARTIAL' | 'STRICT' }
}

export type LoadedReport = {
  reportId: string
  versionId: string
  versionNo: number
  definitionHash: string
  definition: RuntimeDefinition
}

export type RuntimeQueryResult = {
  columns: string[]
  rows: unknown[][]
  plans?: Array<{ role: string; columns: string[]; rows: unknown[][] }>
  hash: string
  partial: boolean
}

export type RuntimeComponentResult = {
  componentId: string
  state: ReportComponentState
  result?: RuntimeQueryResult
  errorCode?: string
}

export type RuntimeExecution = {
  reportId: string
  versionId: string
  versionNo: number
  asOf: string
  timezone: string
  components: RuntimeComponentResult[]
}

export type ReportVersion = {
  id: string
  reportId: string
  versionNo: number
  sourceRevisionNo: number
  definitionHash: string
  publishedBy: string
  publishedAt: string
  rollbackOfVersionNo?: number
  rollbackReason?: string
  staleInsightsAcknowledged: boolean
  artifactState: string
}

export type CreatedReportShare = {
  share: { id: string; reportId: string; reportVersionId?: string; shareType: 'INTERNAL_USER' | 'INTERNAL_GROUP'; principalId: string; filterSnapshot?: Record<string, unknown>; expiresAt: string }
  token: string
}

export type ExportFormat = 'PDF' | 'PNG' | 'CSV' | 'XLSX'
export type ReportExportJob = {
  id: string
  format: ExportFormat
  state: 'PENDING' | 'RUNNING' | 'READY' | 'FAILED' | 'EXPIRED'
  failureCode?: string
  objectUri?: string
  createdAt?: string
  expiresAt?: string
}

export type RuntimePlanInput = {
  pageId: string
  visibleBlockIds?: string[]
  expandedSlotIds?: string[]
  mobile?: boolean
  filterValues?: Record<string, unknown>
}

function idempotencyHeaders() {
  return { 'Idempotency-Key': crypto.randomUUID() }
}

function normalizeExportJob(value: Record<string, unknown>): ReportExportJob {
  const read = (camel: string, pascal: string) => value[camel] ?? value[pascal]
  return {
    id: String(read('id', 'ID') ?? ''),
    format: String(read('format', 'Format') ?? 'PDF') as ExportFormat,
    state: String(read('state', 'State') ?? 'PENDING') as ReportExportJob['state'],
    failureCode: String(read('failureCode', 'FailureCode') ?? '') || undefined,
    objectUri: String(read('objectUri', 'ObjectURI') ?? '') || undefined,
    createdAt: String(read('createdAt', 'CreatedAt') ?? '') || undefined,
    expiresAt: String(read('expiresAt', 'ExpiresAt') ?? '') || undefined,
  }
}

export const reportRuntimeAPI = {
  load(reportId: string, versionNo?: number) {
    const suffix = versionNo ? `?versionNo=${versionNo}` : ''
    return apiRequest<LoadedReport>(`/v1/reports/${encodeURIComponent(reportId)}/runtime${suffix}`)
  },
  execute(reportId: string, input: RuntimePlanInput, options: { versionNo?: number; signal?: AbortSignal } = {}) {
    const suffix = options.versionNo ? `?versionNo=${options.versionNo}` : ''
    return apiRequest<RuntimeExecution>(`/v1/reports/${encodeURIComponent(reportId)}/runtime/execute${suffix}`, {
      method: 'POST', signal: options.signal, body: JSON.stringify(input),
    })
  },
  listVersions(reportId: string) {
    return apiRequest<{ items: ReportVersion[] }>(`/v1/reports/${encodeURIComponent(reportId)}/versions`)
  },
  createShare(reportId: string, input: { reportVersionId: string; shareType: 'INTERNAL_USER' | 'INTERNAL_GROUP'; principalId: string; filterSnapshot?: Record<string, unknown>; expiresAt?: string }) {
    return apiRequest<CreatedReportShare>(`/v1/reports/${encodeURIComponent(reportId)}/shares`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  async createExport(reportId: string, input: { versionNo: number; format: ExportFormat; pageIds?: string[]; filterValues?: Record<string, unknown>; asOf: string; timezone: string }) {
    const value = await apiRequest<Record<string, unknown>>(`/v1/reports/${encodeURIComponent(reportId)}/exports`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
    return normalizeExportJob(value)
  },
  async getExport(reportId: string, exportId: string) {
    const value = await apiRequest<Record<string, unknown>>(`/v1/reports/${encodeURIComponent(reportId)}/exports/${encodeURIComponent(exportId)}`)
    return normalizeExportJob(value)
  },
  async retryExport(reportId: string, exportId: string) {
    const value = await apiRequest<Record<string, unknown>>(`/v1/reports/${encodeURIComponent(reportId)}/exports/${encodeURIComponent(exportId)}/retry`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
    return normalizeExportJob(value)
  },
}
