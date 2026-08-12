import { apiRequest } from '../../lib/api'
import type { ReportComponentState } from '../runtime/state'
import type {
  Block, GlobalFilter, Page, ReportComponent, ReportDefinition, Section, Slot,
} from '../render/schema'

/**
 * 运行页读取的就是发布制品里那份不可变的 Report Definition。这里只做类型别名，
 * 不再声明「运行时够用」的裁剪结构——那会让画布、区块布局与槽位网格在运行侧
 * 消失，渲染器也就无从按定义还原布局。
 */
export type RuntimeFilter = GlobalFilter
export type RuntimeSlot = Slot
export type RuntimeBlock = Block
export type RuntimeSection = Section
export type RuntimePage = Page
export type RuntimeComponent = ReportComponent
export type RuntimeDefinition = ReportDefinition

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

export type AccessedReportShare = {
  version: ReportVersion & { definition: RuntimeDefinition }
  filterSnapshot: Record<string, unknown>
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

export type ReportSchedule = {
  id: string
  reportId: string
  reportVersionId: string
  name: string
  scheduleKind: 'DAILY' | 'WEEKLY' | 'MONTHLY'
  localTime: string
  weekdays: number[]
  dayOfMonth?: number
  timezone: string
  businessCalendar: 'CALENDAR_DAYS' | 'WEEKDAYS'
  state: 'ACTIVE' | 'PAUSED' | 'DISABLED'
  nextRunAt: string
  consecutiveFailures: number
  maxConsecutiveFailures: number
  ownerUserId: string
  recordVersion: number
  lastFailureCode?: string
}

export type ReportSubscription = {
  id: string
  scheduleId: string
  recipientUserId: string
  channel: 'IN_APP'
  state: 'ACTIVE' | 'REVOKED'
  recordVersion: number
}

export type ReportDelivery = {
  id: string
  scheduleId: string
  subscriptionId: string
  reportId: string
  reportVersionId: string
  recipientUserId: string
  scheduledFor: string
  channel: 'IN_APP'
  state: 'PENDING' | 'RUNNING' | 'READY' | 'FAILED' | 'MISSED' | 'SKIPPED'
  attempt: number
  reportLink?: string
  failureCode?: string
  readAt?: string
  createdAt: string
}

export type ReportShareRecord = {
  id: string
  reportId: string
  reportVersionId?: string
  shareType: 'INTERNAL_USER' | 'INTERNAL_GROUP'
  principalId: string
  filterSnapshot?: Record<string, unknown>
  createdAt: string
  expiresAt: string
  expiredAt?: string
  revokedAt?: string
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
  listShares(reportId: string) {
    return apiRequest<{ items: ReportShareRecord[] }>(`/v1/reports/${encodeURIComponent(reportId)}/shares`)
  },
  revokeShare(reportId: string, shareId: string) {
    return apiRequest<{ revoked: boolean }>(`/v1/reports/${encodeURIComponent(reportId)}/shares/${encodeURIComponent(shareId)}/revoke`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
  },
  accessShare(token: string) {
    return apiRequest<AccessedReportShare>(`/v1/report-shares/${encodeURIComponent(token)}`)
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
  exportDownloadURL(reportId: string, exportId: string) {
    return `/api/v1/reports/${encodeURIComponent(reportId)}/exports/${encodeURIComponent(exportId)}/download`
  },
  async retryExport(reportId: string, exportId: string) {
    const value = await apiRequest<Record<string, unknown>>(`/v1/reports/${encodeURIComponent(reportId)}/exports/${encodeURIComponent(exportId)}/retry`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
    return normalizeExportJob(value)
  },
  listSchedules(reportId: string) {
    return apiRequest<{ items: ReportSchedule[] }>(`/v1/reports/${encodeURIComponent(reportId)}/schedules?limit=100`)
  },
  createSchedule(reportId: string, input: {
    reportVersionId: string
    name: string
    scheduleKind: ReportSchedule['scheduleKind']
    localTime: string
    weekdays: number[]
    dayOfMonth?: number
    timezone: string
    businessCalendar: ReportSchedule['businessCalendar']
    maxConsecutiveFailures?: number
    missAfterSeconds?: number
  }) {
    return apiRequest<ReportSchedule>(`/v1/reports/${encodeURIComponent(reportId)}/schedules`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  getSchedule(scheduleId: string) {
    return apiRequest<{ schedule: ReportSchedule; subscriptions: ReportSubscription[] }>(`/v1/report-schedules/${encodeURIComponent(scheduleId)}`)
  },
  setScheduleState(scheduleId: string, state: 'pause' | 'resume', expectedVersion: number) {
    return apiRequest<ReportSchedule>(`/v1/report-schedules/${encodeURIComponent(scheduleId)}/${state}`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ expectedVersion }),
    })
  },
  subscribeSchedule(scheduleId: string, recipientUserId: string) {
    return apiRequest<ReportSubscription>(`/v1/report-schedules/${encodeURIComponent(scheduleId)}/subscriptions`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ recipientUserId, channel: 'IN_APP' }),
    })
  },
  unsubscribeSchedule(scheduleId: string, subscriptionId: string) {
    return apiRequest<{ revoked: boolean }>(`/v1/report-schedules/${encodeURIComponent(scheduleId)}/subscriptions/${encodeURIComponent(subscriptionId)}`, {
      method: 'DELETE', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
  },
  backfillSchedule(scheduleId: string, scheduledFor: string) {
    return apiRequest<{ deliveryCount: number }>(`/v1/report-schedules/${encodeURIComponent(scheduleId)}/backfill`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ scheduledFor }),
    })
  },
  listDeliveries() {
    return apiRequest<{ items: ReportDelivery[] }>('/v1/report-deliveries?limit=100')
  },
  markDeliveryRead(deliveryId: string) {
    return apiRequest<ReportDelivery>(`/v1/report-deliveries/${encodeURIComponent(deliveryId)}/read`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
  },
}
