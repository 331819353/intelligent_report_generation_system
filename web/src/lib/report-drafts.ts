import { apiRequest } from './api'
import type { ReportDocument } from './report-contract'
import type { JsonPatchOperation } from './report-json-patch'

export type ReportEditorState = {
  minimumRowsByPage: Record<string, number>
}

export type ReportOperationType =
  | 'BLOCK_MOVE'
  | 'BLOCK_RESIZE'
  | 'BLOCK_CREATE'
  | 'BLOCK_CLEAR'
  | 'BLOCK_DELETE'
  | 'BLOCK_STICKY_UPDATE'
  | 'COMPONENT_MOVE'
  | 'COMPONENT_RESIZE'
  | 'COMPONENT_CREATE'
  | 'COMPONENT_COPY'
  | 'COMPONENT_DELETE'
  | 'COMPONENT_STICKY_UPDATE'
  | 'LEGACY_DRAFT_RECOVERY'
  | 'UNDO'
  | 'REDO'

/** REPORT_CREATE 只会出现在服务端生成的修订响应中，客户端草稿变更不能提交该操作。 */
export type ReportRevisionOperationType = ReportOperationType | 'REPORT_CREATE'
export type ReportRevisionSource = ReportChangeSource | 'IMPORT'

export type ReportChangeSource = 'USER' | 'AI' | 'SYSTEM'

export type ReportChangeTarget = {
  pageId?: string
  blockId?: string
  componentId?: string
  sourceComponentId?: string
  createdComponentId?: string
  referencedOperationId?: string
}

export type ReportDraftChange = {
  clientOperationId: string
  operationType: ReportOperationType
  source: ReportChangeSource
  target?: ReportChangeTarget
  patch: JsonPatchOperation[]
}

export type ReportDraftRecord = {
  id: string
  code: string
  name: string
  description: string
  type: ReportDocument['report']['type']
  status: 'DRAFT'
  revision: number
  definitionHash: string
  definition: ReportDocument
  editorState: ReportEditorState
  createdAt: string
  updatedAt: string
  capabilities: { edit: boolean }
}

export type ReportRevisionRecord = {
  id: string
  baseRevision: number
  revision: number
  /** 首次 REPORT_CREATE 没有客户端操作 ID，后续语义修订才会返回该字段。 */
  clientOperationId?: string
  operationType: ReportRevisionOperationType
  source: ReportRevisionSource
  target?: ReportChangeTarget
  patch: JsonPatchOperation[]
  patchHash: string
  beforeHash: string
  afterHash: string
  actorUserId: string
  createdAt: string
}

export type ReportRevisionPage = {
  items: ReportRevisionRecord[]
  total: number
  limit: number
  offset: number
}

export type CreateReportDraftInput = {
  definition: ReportDocument
  editorState: ReportEditorState
}

export type SaveReportDraftInput = {
  expectedRevision: number
  definition: ReportDocument
  editorState: ReportEditorState
  changes: ReportDraftChange[]
}

/** 创建键由调用方持有并在重试时复用，避免响应丢失后产生重复报告或修订。 */
export function createIdempotencyKey(): string {
  return globalThis.crypto.randomUUID()
}

/** 创建服务端报告草稿；报告 UUID 和审计身份均由服务端生成。 */
export function createReportDraft(input: CreateReportDraftInput, idempotencyKey: string): Promise<ReportDraftRecord> {
  return apiRequest('/v1/reports', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

/** 服务端草稿是刷新后的唯一事实来源，旧会话副本不能在此处自动覆盖它。 */
export function getReportDraft(reportId: string): Promise<ReportDraftRecord> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/draft`)
}

/**
 * 完整定义用于恢复，语义变更和 Patch 用于校验、审计与并发控制。
 * 同一个在途候选重试时必须继续传入原幂等键。
 */
export function saveReportDraft(reportId: string, input: SaveReportDraftInput, idempotencyKey: string): Promise<ReportDraftRecord> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/draft`, {
    method: 'PUT',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

/** 修订列表只返回服务端生成的不可变审计事实。 */
export function listReportRevisions(reportId: string, limit = 50, offset = 0): Promise<ReportRevisionPage> {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/revisions?${query}`)
}
