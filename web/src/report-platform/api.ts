import { apiRequest, apiResponse } from '../lib/api'
import { createReportJSONPatch } from '../lib/report-json-patch'
import { migrateReportDefinition } from './migration'
import { validateReportDefinition } from './schema'
import type { ReportDefinition, ReportQueryBatchRequest, ReportQueryBatchResponse } from './types'

export type ReportOperationType = 'REPORT_SETTINGS_UPDATE' | 'FILTER_UPDATE' | 'CARD_CREATE' | 'CARD_DELETE' | 'CARD_LAYOUT_UPDATE' | 'CARD_CONFIG_UPDATE' | 'LEGACY_DRAFT_RECOVERY' | 'UNDO' | 'REDO'
export type ReportChangeTarget = { cardId?: string; filterId?: string; referencedOperationId?: string }
export type ReportDraftChange = { clientOperationId: string; operationType: ReportOperationType; source: 'USER'; target?: ReportChangeTarget; patch: ReturnType<typeof createReportJSONPatch>['forward'] }

export type ReportDraftRecord = {
  id: string; code: string; name: string; description: string; type: 'DASHBOARD' | 'REPORT'; status: 'DRAFT' | 'PUBLISHED' | 'ARCHIVED'
  revision: number; definitionHash: string; definition: ReportDefinition; editorState: { minimumRowsByPage: Record<string, number> }
  createdAt: string; updatedAt: string; capabilities: { edit: boolean }
}

export type PublishedReportVersion = {
  id: string; reportId: string; version: number; sourceRevision: number; schemaVersion: string; sha256: string; sizeBytes: number
  comment?: string; publishedBy: string; publishedAt: string; current: boolean
}
export type ReportManifest = { reportId: string; version: number; schemaVersion: string; definitionUrl: string; sha256: string; sizeBytes: number; publishedAt: string }
export type ReportPublicationIssue = { level: string; code: string; path: string; message: string; componentId?: string; dependencyId?: string }

export function createIdempotencyKey(): string { return crypto.randomUUID() }

export async function getReportDraft(reportId: string): Promise<{ record: ReportDraftRecord; migrated: boolean; migrationWarnings: string[]; migrationChange?: ReportDraftChange }> {
  const raw = await apiRequest<Omit<ReportDraftRecord, 'definition'> & { definition: unknown }>(`/v1/reports/${encodeURIComponent(reportId)}/draft`, { cache: 'no-store' })
  const migration = migrateReportDefinition(raw.definition)
  const validation = validateReportDefinition(migration.definition)
  if (!validation.definition) throw new Error(validation.errors.map(issue => `${issue.path}: ${issue.reason}`).join('；'))
  const migrationChange = migration.migrated ? {
    clientOperationId: crypto.randomUUID(), operationType: 'LEGACY_DRAFT_RECOVERY' as const, source: 'USER' as const,
    patch: createReportJSONPatch(raw.definition, validation.definition).forward,
  } : undefined
  return { record: { ...raw, definition: validation.definition }, migrated: migration.migrated, migrationWarnings: migration.warnings, migrationChange }
}

export function createReportDraft(definition: ReportDefinition, key: string): Promise<ReportDraftRecord> {
  return apiRequest('/v1/reports', { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ definition, editorState: { minimumRowsByPage: {} } }) })
}

export function saveReportDraft(reportId: string, expectedRevision: number, definition: ReportDefinition, changes: ReportDraftChange[], key: string): Promise<ReportDraftRecord> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/draft`, {
    method: 'PUT', headers: { 'Idempotency-Key': key },
    body: JSON.stringify({ expectedRevision, definition, editorState: { minimumRowsByPage: {} }, changes }),
  })
}

export function createDraftChange(before: ReportDefinition, after: ReportDefinition, operationType: ReportOperationType, target?: ReportChangeTarget): ReportDraftChange {
  return { clientOperationId: crypto.randomUUID(), operationType, source: 'USER', ...(target ? { target } : {}), patch: createReportJSONPatch(before, after).forward }
}

export function validatePublication(reportId: string, revision: number): Promise<{ valid: boolean; issues: ReportPublicationIssue[] }> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/validate`, { method: 'POST', body: JSON.stringify({ revision }) })
}

export function publishReport(reportId: string, revision: number, key: string): Promise<PublishedReportVersion> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/publish`, { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify({ revision, prewarm: true }) })
}

export function listPublishedVersions(reportId: string): Promise<{ items: PublishedReportVersion[]; total: number }> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/versions?limit=200&offset=0`, { cache: 'no-store' })
}

export function getReportManifest(reportId: string, version: number): Promise<ReportManifest> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/versions/${version}/manifest`, { cache: 'no-store' })
}

export async function loadPublishedDefinition(manifest: ReportManifest, signal?: AbortSignal): Promise<ReportDefinition> {
  const response = await apiResponse(manifest.definitionUrl.replace(/^\/api/, ''), { signal, cache: 'no-store' })
  const bytes = new Uint8Array(await response.arrayBuffer())
  if (bytes.byteLength !== manifest.sizeBytes) throw new Error('发布物大小校验失败')
  const digest = [...new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))].map(value => value.toString(16).padStart(2, '0')).join('')
  if (digest !== manifest.sha256) throw new Error('发布物 SHA-256 校验失败')
  const parsed = JSON.parse(new TextDecoder().decode(bytes)) as unknown
  const validation = validateReportDefinition(parsed)
  if (!validation.definition || validation.definition.report.status !== 'PUBLISHED') throw new Error('发布物不符合 Report DSL 1.0.0')
  return validation.definition
}

export function queryDraftReport(reportId: string, input: ReportQueryBatchRequest, signal?: AbortSignal): Promise<ReportQueryBatchResponse> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/draft/query-batch`, { method: 'POST', body: JSON.stringify(input), signal })
}

export function queryPublishedReport(reportId: string, version: number, input: ReportQueryBatchRequest, signal?: AbortSignal): Promise<ReportQueryBatchResponse> {
  return apiRequest(`/v1/reports/${encodeURIComponent(reportId)}/versions/${version}/query-batch`, { method: 'POST', body: JSON.stringify(input), signal })
}
