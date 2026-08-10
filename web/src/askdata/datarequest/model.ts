export type DataRequestState =
  | 'DRAFT'
  | 'SUBMITTED'
  | 'APPROVED'
  | 'REJECTED'
  | 'IN_PROGRESS'
  | 'DELIVERED'
  | 'CLOSED'

export type DataRequestSensitivity = 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED'
export type DataRequestDeliveryType = 'EXISTING_REPORT' | 'NEW_DATASET' | 'ONE_TIME_EXPORT'

export type DataRequestFieldRef = {
  datasetVersionId: string
  fieldId: string
}

export type DataRequestTimeRange = {
  start: string
  endExclusive: string
  timezone: string
  grain?: string
}

export type DataRequestParsedContext = {
  metricIds?: string[]
  dimensionIds?: string[]
  memberIds?: string[]
  timeRange?: DataRequestTimeRange
}

export type DataRequestEvent = {
  id: string
  requestId: string
  sequenceNo: number
  fromState?: DataRequestState
  toState: DataRequestState
  actorUserId: string
  note?: string
  createdAt: string
}

export type DataRequest = {
  id: string
  domainId: string
  requesterUserId: string
  sourceQuestionRunId?: string
  requestText: string
  parsedContext: DataRequestParsedContext
  businessPurpose: string
  requiredFields: DataRequestFieldRef[]
  sensitivityLevel: DataRequestSensitivity
  state: DataRequestState
  approverUserIds: string[]
  securityCosignUserId?: string
  assigneeUserId?: string
  slaDueAt: string
  deliveryType?: DataRequestDeliveryType
  deliveryRef?: string
  statusNote?: string
  recordVersion: number
  createdAt: string
  updatedAt: string
  submittedAt?: string
  approvedAt?: string
  rejectedAt?: string
  startedAt?: string
  deliveredAt?: string
  closedAt?: string
  events?: DataRequestEvent[]
}

export type DataRequestFieldOption = DataRequestFieldRef & {
  datasetId: string
  datasetName: string
  fieldCode: string
  fieldName: string
  sensitivityLevel: DataRequestSensitivity
}

export type CreateDataRequestInput = {
  sourceQuestionRunId?: string
  requestText: string
  parsedContext: DataRequestParsedContext
  businessPurpose: string
  requiredFields: DataRequestFieldRef[]
  slaDueAt: string
}

const sensitivityRank: Record<DataRequestSensitivity, number> = {
  PUBLIC: 0,
  INTERNAL: 1,
  CONFIDENTIAL: 2,
  RESTRICTED: 3,
}

export const dataRequestStateLabels: Record<DataRequestState, string> = {
  DRAFT: '草稿',
  SUBMITTED: '审批中',
  APPROVED: '已批准',
  REJECTED: '已驳回',
  IN_PROGRESS: '处理中',
  DELIVERED: '已交付',
  CLOSED: '已关闭',
}

export const dataRequestSensitivityLabels: Record<DataRequestSensitivity, string> = {
  PUBLIC: '公开',
  INTERNAL: '内部',
  CONFIDENTIAL: '机密',
  RESTRICTED: '受限',
}

export const dataRequestDeliveryLabels: Record<DataRequestDeliveryType, string> = {
  EXISTING_REPORT: '现有报告',
  NEW_DATASET: '新建 ADS 数据集',
  ONE_TIME_EXPORT: '一次性导出',
}

export const dataRequestTimeline = [
  { state: 'DRAFT' as const, label: '草稿' },
  { state: 'SUBMITTED' as const, label: '已提交' },
  { state: 'APPROVED' as const, label: '审批中' },
  { state: 'IN_PROGRESS' as const, label: '处理中' },
  { state: 'DELIVERED' as const, label: '已交付' },
  { state: 'CLOSED' as const, label: '已关闭' },
]

export function deriveDataRequestSensitivity(
  selected: DataRequestFieldRef[],
  options: DataRequestFieldOption[],
): DataRequestSensitivity {
  const optionMap = new Map(options.map(option => [`${option.datasetVersionId}:${option.fieldId}`, option]))
  return selected.reduce<DataRequestSensitivity>((highest, field) => {
    const next = optionMap.get(`${field.datasetVersionId}:${field.fieldId}`)?.sensitivityLevel ?? 'INTERNAL'
    return sensitivityRank[next] > sensitivityRank[highest] ? next : highest
  }, 'PUBLIC')
}

/**
 * Question Run 预填只允许受控语义对象与解析时间。任何结果行、SQL、答案或任意扩展字段都会被丢弃。
 */
export function sanitizeDataRequestContext(value: unknown): DataRequestParsedContext {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  const input = value as Record<string, unknown>
  const uuidList = (candidate: unknown) => Array.isArray(candidate)
    ? candidate.filter(item => typeof item === 'string') as string[]
    : undefined
  const metricIds = uuidList(input.metricIds)
  const dimensionIds = uuidList(input.dimensionIds)
  const memberIds = uuidList(input.memberIds)
  const range = input.timeRange && typeof input.timeRange === 'object' && !Array.isArray(input.timeRange)
    ? input.timeRange as Record<string, unknown>
    : undefined
  const timeRange = range && typeof range.start === 'string' && typeof range.endExclusive === 'string' && typeof range.timezone === 'string'
    ? {
        start: range.start,
        endExclusive: range.endExclusive,
        timezone: range.timezone,
        ...(typeof range.grain === 'string' ? { grain: range.grain } : {}),
      }
    : undefined
  return {
    ...(metricIds?.length ? { metricIds } : {}),
    ...(dimensionIds?.length ? { dimensionIds } : {}),
    ...(memberIds?.length ? { memberIds } : {}),
    ...(timeRange ? { timeRange } : {}),
  }
}

export function dataRequestStepStatus(state: DataRequestState, step: DataRequestState) {
  if (state === 'REJECTED') {
    const index = dataRequestTimeline.findIndex(item => item.state === step)
    return index < 2 ? 'complete' : index === 2 ? 'rejected' : 'pending'
  }
  const current = dataRequestTimeline.findIndex(item => item.state === state)
  const target = dataRequestTimeline.findIndex(item => item.state === step)
  if (state === 'SUBMITTED' && step === 'SUBMITTED') return 'complete'
  if (state === 'SUBMITTED' && step === 'APPROVED') return 'active'
  if (target < current || target === current && state !== 'SUBMITTED') return 'complete'
  if (target === current) return 'active'
  return 'pending'
}

export function validateDataRequestDraft(input: CreateDataRequestInput, now = new Date()) {
  if (!input.requestText.trim()) return '请填写取数需求。'
  if (!input.businessPurpose.trim()) return '请填写业务用途。'
  if (input.requiredFields.length === 0) return '请至少选择一个需要交付的字段。'
  const dueAt = new Date(input.slaDueAt)
  if (Number.isNaN(dueAt.getTime()) || dueAt.getTime() <= now.getTime() + 60 * 60 * 1000) {
    return '期望交付时间需晚于当前时间至少 1 小时。'
  }
  return ''
}
