import { type FormEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  ArrowClockwise,
  CheckCircle,
  Circle,
  ClockCountdown,
  Database,
  DotsThree,
  FileXls,
  MagnifyingGlass,
  PencilSimple,
  Plus,
  Sparkle,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import { AssetSharingSelect } from '../components/AssetSharingSelect'
import { DataSourceAIAssistant } from '../components/DataSourceAIAssistant'
import { RequestError } from '../lib/api'
import { currentSubject } from '../lib/auth'
import { currentDomainID, subscribeDomainChange } from '../lib/domain-context'
import { md5Hex } from '../lib/md5'
import {
  dataSourceAPI,
  type DataSourceConnectionInput,
  type DataSourceColumnRecord,
  type DataSourceRecord,
  type DataSourceReviewStatus,
  type DataSourceStatus,
  type DataSourceTableRecord,
  type DataSourceTestResult,
  type DataSourceType,
  type DataSourceVisibility,
  type AssetSharingScope,
  type DiscoveredTableRecord,
	type ExcelFileAsset,
  type ExcelWorkbookInspection,
  type MetadataJob,
  type MetadataJobFailure,
  type MetadataRefreshMode,
  type MetadataSampleMode,
} from '../lib/data-sources'

const statusLabels: Record<DataSourceStatus, string> = {
  DRAFT: '待验证', ACTIVE: '运行中', DISABLED: '已暂停', SYNCING: '同步中', ERROR: '异常', DELETING: '删除中',
}
const typeLabels: Record<DataSourceType, string> = { MYSQL: 'MySQL', ORACLE: 'Oracle', EXCEL: 'Excel / CSV' }

const snapshotSources: DataSourceRecord[] = [
  {
    id: 'snapshot-sales-mysql', tenantId: 'snapshot-tenant', code: 'sales_core', name: '销售业务核心库',
    description: '销售订单、渠道与客户经营数据', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'TENANT_PUBLIC', sharingScope: 'DOMAIN', type: 'MYSQL', status: 'ACTIVE',
    config: { host: 'sales-db.internal', port: 3306, database: 'sales_prod', username: 'report_reader' },
    configVersionId: 'snapshot-sales-config-v3', publishedVersionId: 'snapshot-sales-published-v3', configVersion: 3,
    publishedConfigVersion: 3, validationStatus: 'PASSED', publicationStatus: 'PUBLISHED', hasUnpublishedChanges: false,
    reviewStatus: 'APPROVED', lastTestedAt: '2026-08-11T09:18:00+08:00', updatedAt: '2026-08-11T09:18:00+08:00', version: 8,
  },
  {
    id: 'snapshot-finance-oracle', tenantId: 'snapshot-tenant', code: 'finance_erp', name: '财务 ERP',
    description: '总账、成本中心与预算执行数据', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'PRIVATE', sharingScope: 'PRIVATE', type: 'ORACLE', status: 'DRAFT',
    config: { host: 'finance-oracle.internal', port: 1521, database: 'FINPRD', username: 'bi_reader' },
    configVersionId: 'snapshot-finance-config-v2', configVersion: 2, validationStatus: 'PASSED', publicationStatus: 'UNPUBLISHED',
    hasUnpublishedChanges: true, reviewStatus: 'PENDING', reviewRequestId: 'snapshot-finance-review', reviewRequestVersion: 1,
    reviewRequesterId: 'snapshot-user', reviewSubmittedAt: '2026-08-11T10:32:00+08:00', updatedAt: '2026-08-11T10:32:00+08:00', version: 4,
  },
  {
    id: 'snapshot-channel-excel', tenantId: 'snapshot-tenant', code: 'channel_targets_xlsx', name: '渠道月度目标',
    description: '渠道团队每月维护的目标与预算文件', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'TENANT_PUBLIC', sharingScope: 'DOMAIN', type: 'EXCEL', status: 'ACTIVE', config: {},
    fileAssetId: 'snapshot-file', fileVersionId: 'snapshot-file-v5', configVersionId: 'snapshot-channel-config-v5',
    publishedVersionId: 'snapshot-channel-published-v5', configVersion: 5, publishedConfigVersion: 5,
    validationStatus: 'PASSED', publicationStatus: 'PUBLISHED', hasUnpublishedChanges: false, reviewStatus: 'APPROVED',
    lastTestedAt: '2026-08-10T16:42:00+08:00', updatedAt: '2026-08-10T16:42:00+08:00', version: 7,
  },
  {
    id: 'snapshot-inventory-mysql', tenantId: 'snapshot-tenant', code: 'inventory_legacy', name: '库存历史库',
    description: '旧库存系统，等待凭据轮换后重新验证', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'PRIVATE', sharingScope: 'PRIVATE', type: 'MYSQL', status: 'ERROR',
    config: { host: 'inventory-db.internal', port: 3306, database: 'inventory', username: 'legacy_reader' },
    configVersionId: 'snapshot-inventory-config-v6', configVersion: 6, validationStatus: 'FAILED', publicationStatus: 'UNPUBLISHED',
    hasUnpublishedChanges: true, reviewStatus: 'REJECTED', reviewNote: '连接凭据已过期，请轮换后重新测试。',
    reviewRequesterId: 'snapshot-user', reviewReviewerId: 'snapshot-owner', updatedAt: '2026-08-09T14:20:00+08:00', version: 9,
  },
  {
    id: 'snapshot-member-mysql', tenantId: 'snapshot-tenant', code: 'member_behavior', name: '会员行为日志库',
    description: '会员访问、活动参与与转化行为数据', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'TENANT_PUBLIC', sharingScope: 'DOMAIN', type: 'MYSQL', status: 'ACTIVE',
    config: { host: 'member-db.internal', port: 3306, database: 'member_analytics', username: 'report_reader' },
    configVersionId: 'snapshot-member-config-v4', publishedVersionId: 'snapshot-member-published-v4', configVersion: 4,
    publishedConfigVersion: 4, validationStatus: 'PASSED', publicationStatus: 'PUBLISHED', hasUnpublishedChanges: false,
    reviewStatus: 'APPROVED', lastTestedAt: '2026-08-11T08:42:00+08:00', updatedAt: '2026-08-11T08:42:00+08:00', version: 5,
  },
  {
    id: 'snapshot-marketing-mysql', tenantId: 'snapshot-tenant', code: 'marketing_campaign', name: '营销活动数据库',
    description: '营销活动、触达批次与转化效果数据', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'TENANT_PUBLIC', sharingScope: 'DOMAIN', type: 'MYSQL', status: 'ACTIVE',
    config: { host: 'marketing-db.internal', port: 3306, database: 'campaign_prod', username: 'campaign_reader' },
    configVersionId: 'snapshot-marketing-config-v2', publishedVersionId: 'snapshot-marketing-published-v2', configVersion: 2,
    publishedConfigVersion: 2, validationStatus: 'PASSED', publicationStatus: 'PUBLISHED', hasUnpublishedChanges: false,
    reviewStatus: 'APPROVED', lastTestedAt: '2026-08-11T07:56:00+08:00', updatedAt: '2026-08-11T07:56:00+08:00', version: 3,
  },
  {
    id: 'snapshot-supplier-oracle', tenantId: 'snapshot-tenant', code: 'supplier_mdm', name: '供应商主数据',
    description: '供应商主数据、资质与合作关系信息', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'TENANT_PUBLIC', sharingScope: 'DOMAIN', type: 'ORACLE', status: 'ACTIVE',
    config: { host: 'supplier-oracle.internal', port: 1521, database: 'SUPMDM', username: 'mdm_reader' },
    configVersionId: 'snapshot-supplier-config-v5', publishedVersionId: 'snapshot-supplier-published-v5', configVersion: 5,
    publishedConfigVersion: 5, validationStatus: 'PASSED', publicationStatus: 'PUBLISHED', hasUnpublishedChanges: false,
    reviewStatus: 'APPROVED', lastTestedAt: '2026-08-10T18:21:00+08:00', updatedAt: '2026-08-10T18:21:00+08:00', version: 6,
  },
  {
    id: 'snapshot-store-excel', tenantId: 'snapshot-tenant', code: 'store_master_csv', name: '门店静态信息',
    description: '门店层级、区域归属与经营状态信息', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
    visibility: 'PRIVATE', sharingScope: 'PRIVATE', type: 'EXCEL', status: 'ERROR', config: {},
    fileAssetId: 'snapshot-store-file', fileVersionId: 'snapshot-store-file-v2', configVersionId: 'snapshot-store-config-v2',
    configVersion: 2, validationStatus: 'FAILED', publicationStatus: 'UNPUBLISHED', hasUnpublishedChanges: true,
    reviewStatus: 'NOT_SUBMITTED', updatedAt: '2026-08-10T15:08:00+08:00', version: 3,
  },
]

const snapshotSourceMetrics: Record<string, { readiness: number; tables: string; fields: string; business: string; latency: string }> = {
  'snapshot-sales-mysql': { readiness: 86, tables: '128 / 150', fields: '1,024 / 1,200', business: '86 / 120', latency: '26 ms' },
  'snapshot-finance-oracle': { readiness: 64, tables: '42 / 66', fields: '430 / 672', business: '28 / 48', latency: '152 ms' },
  'snapshot-channel-excel': { readiness: 52, tables: '6 / 8', fields: '54 / 76', business: '20 / 38', latency: '—' },
  'snapshot-inventory-mysql': { readiness: 31, tables: '18 / 58', fields: '206 / 612', business: '8 / 44', latency: '连接失败' },
  'snapshot-member-mysql': { readiness: 78, tables: '96 / 118', fields: '824 / 1,040', business: '62 / 88', latency: '38 ms' },
  'snapshot-marketing-mysql': { readiness: 57, tables: '34 / 60', fields: '316 / 552', business: '24 / 52', latency: '120 ms' },
  'snapshot-supplier-oracle': { readiness: 70, tables: '56 / 80', fields: '486 / 690', business: '38 / 60', latency: '45 ms' },
  'snapshot-store-excel': { readiness: 45, tables: '9 / 20', fields: '82 / 180', business: '12 / 36', latency: '—' },
}
type ConnectionDraft = {
  code: string
  name: string
  description: string
  visibility: DataSourceVisibility
  sharingScope: AssetSharingScope
  type: DataSourceType
  host: string
  port: string
  database: string
  oracleConnectMode: 'SERVICE_NAME' | 'SID'
  username: string
  password: string
}
type DialogState = { mode: 'create' | 'view' | 'edit' | 'delete' | 'select-tables' | 'edit-table' | 'delete-table'; source?: DataSourceRecord; table?: DataSourceTableRecord }
type TableEditorPurpose = 'EDIT' | 'MANUAL_COMPLETE'
type Notice = { tone: 'success' | 'error'; message: string }
type TableDraft = { businessName: string; businessDescription: string; tags: string; sensitivityLevel: string; visibility: string; manualLocked: boolean }
type MetadataJobSnapshot = { job: MetadataJob; title: string }
type MetadataJobPoller = { jobId: string; timeout: number; stopped: boolean }
type ConnectionTestPoller = {
  controller: AbortController
  promise: Promise<DataSourceTestResult>
}
type ColumnDraft = {
  original: DataSourceColumnRecord
  businessName: string
  businessDescription: string
  tags: string
  semanticType: string
  sensitivityLevel: DataSourceColumnRecord['sensitivityLevel']
  manualLocked: boolean
}

const semanticTypes = ['DATE', 'TIME', 'DATETIME', 'REGION', 'COMPANY_NAME', 'AMOUNT', 'PERCENTAGE', 'IDENTIFIER', 'CATEGORY', 'QUANTITY', 'BOOLEAN', 'TEXT']
const metadataJobActive = (job: MetadataJob | null) => job?.status === 'QUEUED' || job?.status === 'RUNNING'
const metadataJobTerminal = (job: MetadataJob) => !metadataJobActive(job)
const metadataStageLabels: Record<string, string> = {
  QUEUED: '等待后台执行', DISCOVERY: '读取源库结构', DIFF: '比对结构变化', SAMPLE: '采集样本', PERSISTENCE: '保存技术元数据', LLM: 'LLM 完善', COMPLETE: '已完成', FAILED: '执行失败',
}
const metadataJobLabel = (job: MetadataJob) => job.kind === 'IMPORT' ? '新增数据表' : job.mode === 'INCREMENTAL' ? '增量刷新' : '全量刷新'
const metadataSampleDescription = (mode: MetadataSampleMode) => mode === 'DENY'
  ? '仅技术元数据，不读取业务行'
  : mode === 'MASK'
    ? '读取最多 10 行并在本地格式脱敏后发送'
    : '读取最多 10 行原值并发送（高风险）'
const metadataJobFailureTable = (failure: MetadataJobFailure) => [failure.schemaName || failure.catalogName, failure.tableName].filter(Boolean).join('.')
const metadataJobFailureMessage = (failure: MetadataJobFailure) => failure.errorMessage?.trim() || failure.errorCode?.trim() || '处理失败'
const metadataJobLogTime = (value: string) => {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime())
    ? value
    : parsed.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
const metadataJobLogDuration = (durationMs?: number) => {
  if (!durationMs) return ''
  if (durationMs < 1000) return `${durationMs} 毫秒`
  const seconds = durationMs / 1000
  return `${seconds < 10 ? seconds.toFixed(1) : Math.round(seconds)} 秒`
}
const connectionTestStillCurrent = (
  source: DataSourceRecord,
  result: { configVersionId?: string },
) => !result.configVersionId || source.configVersionId === result.configVersionId
const metadataJobCompletionNotice = (job: MetadataJob, title: string): Notice => {
  if (job.status === 'SUCCEEDED' && job.total === 0) return { tone: 'success', message: '当前没有可刷新的数据表资产' }
  if (job.status === 'SUCCEEDED') {
    const skipped = job.skipped ? `，跳过 ${job.skipped} 张未变化表` : ''
    return { tone: 'success', message: `${title}完成：${job.succeeded} 张成功${skipped}` }
  }
  return { tone: 'error', message: `${title}完成：${job.succeeded} 张成功，${job.skipped} 张跳过，${job.failed} 张失败${job.errorMessage ? `；${job.errorMessage}` : ''}` }
}
const columnDraftFromRecord = (column: DataSourceColumnRecord): ColumnDraft => ({
  original: column,
  businessName: column.businessName,
  businessDescription: column.businessDescription,
  tags: column.tags.join(', '),
  semanticType: column.semanticType,
  sensitivityLevel: column.sensitivityLevel,
  manualLocked: column.manualLocked,
})
const columnDraftChanged = (draft: ColumnDraft) => draft.businessName.trim() !== draft.original.businessName
  || draft.businessDescription.trim() !== draft.original.businessDescription
  || normalizedTags(draft.tags).join('\u001f') !== draft.original.tags.join('\u001f')
  || draft.semanticType !== draft.original.semanticType
  || draft.sensitivityLevel !== draft.original.sensitivityLevel
  || draft.manualLocked !== draft.original.manualLocked
const normalizedTags = (value: string) => [...new Set(value.split(',').map(tag => tag.trim()).filter(Boolean))]
const dataSourceCodePattern = /^[A-Za-z][A-Za-z0-9_]{0,127}$/
const validationLabels = { UNTESTED: '未测试', PASSED: '测试通过', FAILED: '测试失败' } as const
const publicationLabels = { UNPUBLISHED: '未上线', PUBLISHED: '已上线' } as const
const validationStatusOf = (source: DataSourceRecord) => source.validationStatus
  ?? (source.status === 'ACTIVE' || source.status === 'DISABLED' || source.status === 'SYNCING' ? 'PASSED' : source.status === 'ERROR' ? 'FAILED' : 'UNTESTED')
const publicationStatusOf = (source: DataSourceRecord) => source.publicationStatus
  ?? (source.status === 'ACTIVE' || source.status === 'DISABLED' || source.status === 'SYNCING' ? 'PUBLISHED' : 'UNPUBLISHED')
const hasUnpublishedDraft = (source: DataSourceRecord) => source.hasUnpublishedChanges
  ?? publicationStatusOf(source) === 'UNPUBLISHED'
const reviewStatusOf = (source: DataSourceRecord): DataSourceReviewStatus => source.reviewStatus || 'NOT_SUBMITTED'
const lifecycleLabel = (source: DataSourceRecord) => reviewStatusOf(source) === 'PENDING'
  ? '待审批'
  : reviewStatusOf(source) === 'REJECTED'
    ? '审核失败'
    : source.status === 'DRAFT' && validationStatusOf(source) === 'PASSED'
  ? '待上线'
  : statusLabels[source.status]
const formatDataSourceTime = (value?: string) => {
  if (!value) return '尚未测试'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime())
    ? value
    : parsed.toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
const connectionHealth = (source: DataSourceRecord) => {
  if (validationStatusOf(source) === 'FAILED' || source.status === 'ERROR') return { tone: 'error', label: '异常', detail: '连接失败' }
  if (validationStatusOf(source) === 'PASSED') return { tone: 'healthy', label: '健康', detail: snapshotSourceMetrics[source.id]?.latency || '验证通过' }
  return { tone: 'warning', label: '待验证', detail: '尚无测试收据' }
}
const metadataSummary = (source: DataSourceRecord) => snapshotSourceMetrics[source.id] || {
  readiness: null,
  tables: '查看资产', fields: '查看资产', business: '待完善', latency: validationStatusOf(source) === 'PASSED' ? '验证通过' : '—',
}
const lifecycleSteps = (source: DataSourceRecord) => {
  const summary = metadataSummary(source)
  return [
    { label: '已连接', complete: Boolean(source.configVersionId || source.fileAssetId) },
    { label: '已验证', complete: validationStatusOf(source) === 'PASSED' },
    { label: '已发布', complete: publicationStatusOf(source) === 'PUBLISHED' },
    { label: '完善元数据', complete: summary.readiness === 100 },
  ]
}
const formatFileSize = (bytes: number) => bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`
const fileSourceIdentity = (filename: string) => {
  const extensionMatch = filename.match(/\.([^.]+)$/)
  const extension = extensionMatch?.[1]?.toLocaleLowerCase() || 'file'
  const stem = filename.slice(0, extensionMatch?.index ?? filename.length).trim() || '文件数据源'
  const normalizedStem = stem.normalize('NFKC').toLocaleLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, '_').replace(/^_+|_+$/g, '') || 'file'
  const readableCode = `${normalizedStem}_${extension}`
  return { name: stem, code: dataSourceCodePattern.test(readableCode) ? readableCode : `file_${md5Hex(readableCode)}` }
}
const tableDraftChanged = (draft: TableDraft, table: DataSourceTableRecord) => draft.businessName.trim() !== table.businessName
  || draft.businessDescription.trim() !== table.businessDescription
  || normalizedTags(draft.tags).join('\u001f') !== table.tags.join('\u001f')
  || draft.sensitivityLevel !== table.sensitivityLevel
  || draft.visibility !== table.visibility
  || draft.manualLocked !== table.manualLocked

const emptyDraft = (): ConnectionDraft => ({
  code: '', name: '', description: '', visibility: 'PRIVATE', sharingScope: 'PRIVATE',
  type: 'MYSQL', host: '', port: '3306', database: '', oracleConnectMode: 'SERVICE_NAME', username: '', password: '',
})
const configText = (source: DataSourceRecord, key: string) => {
  const value = source.config?.[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value) : ''
}
const discoveredTableKey = (table: DiscoveredTableRecord) => `${table.catalogName}\u001f${table.schemaName}\u001f${table.name}`
const assetTableKey = (table: DataSourceTableRecord) => `${table.catalogName}\u001f${table.schemaName}\u001f${table.tableName}`
const tableForMetadataFailure = (failure: MetadataJobFailure, tables: DataSourceTableRecord[]) => tables.find(table =>
  table.tableName === failure.tableName
  && (!failure.catalogName || table.catalogName === failure.catalogName)
  && (!failure.schemaName || table.schemaName === failure.schemaName))
const inspectionTables = (inspection: ExcelWorkbookInspection): DiscoveredTableRecord[] => inspection.sheets.map(sheet => ({
  catalogName: '', schemaName: 'WORKBOOK', name: sheet.name, type: 'SHEET', sourceComment: '',
  columns: sheet.columns.map(column => ({ name: column.name, nativeType: column.canonicalType, canonicalType: column.canonicalType, nullable: column.nullable })),
}))
const draftFromSource = (source: DataSourceRecord): ConnectionDraft => ({
  code: source.code,
  name: source.name,
  description: source.description || '',
  visibility: source.visibility || 'PRIVATE',
  sharingScope: source.sharingScope || (source.visibility === 'TENANT_PUBLIC' ? 'DOMAIN' : 'PRIVATE'),
  type: source.type === 'ORACLE' ? 'ORACLE' : 'MYSQL',
  host: configText(source, 'host'),
  port: configText(source, 'port') || (source.type === 'ORACLE' ? '1521' : '3306'),
  database: configText(source, 'database'),
  oracleConnectMode: configText(source, 'oracleConnectMode') === 'SID' ? 'SID' : 'SERVICE_NAME',
  username: configText(source, 'username'),
  password: '',
})
const normalizedDraft = (draft: ConnectionDraft) => ({
  code: draft.code.trim(),
  name: draft.name.trim(),
  description: draft.description.trim(),
  visibility: draft.visibility,
  sharingScope: draft.sharingScope,
  type: draft.type,
  host: draft.host.trim(),
  port: draft.port.trim(),
  database: draft.database.trim(),
  oracleConnectMode: draft.oracleConnectMode,
  username: draft.username.trim(),
})
const draftMatchesSource = (draft: ConnectionDraft, source?: DataSourceRecord) => {
  if (!source || draft.password) return false
  const saved = draftFromSource(source)
  return JSON.stringify(normalizedDraft(draft)) === JSON.stringify(normalizedDraft(saved))
}
const connectionDraftError = (draft: ConnectionDraft, editing: boolean) => {
  if (!dataSourceCodePattern.test(draft.code.trim())) {
    return '数据源编码必须以英文字母开头，且只能包含英文字母、数字和下划线，最长 128 位'
  }
  const port = Number(draft.port)
  if (!draft.name.trim() || !draft.host.trim() || !draft.database.trim() || !draft.username.trim() || !Number.isInteger(port) || port < 1 || port > 65535 || (!editing && !draft.password)) {
    return `请完整填写连接信息${editing ? '' : '和密码'}，端口需为 1–65535 的整数`
  }
  const host = draft.host.trim().toLocaleLowerCase()
  if (host === '127.0.0.1' || host === 'localhost' || host.endsWith('.localhost') || host === '::1') {
    return 'Host 不能填写 127.0.0.1 或 localhost；如果配置服务在 Docker 中、数据库在宿主机，请填写 host.docker.internal'
  }
  if (host.includes('://') || /[/\\@%\s]/.test(host)) {
    return 'Host 只填写主机名或 IP，不要包含 http://、jdbc:、端口、路径或空格'
  }
  return ''
}
const friendlyConnectionError = (cause: unknown) => {
  if (cause instanceof RequestError) {
    if (cause.detail.code === 'DATA_SOURCE_CONNECTION_CONFIGURATION_INVALID') return cause.detail.message
    if (cause.detail.code.includes('AUTH')) return '地址、端口和数据库名已通过；用户名或密码校验失败'
    if (cause.detail.code === 'DATABASE_NOT_FOUND') return '地址和端口已通过；请确认数据库名，或 Oracle Service Name / SID 及连接模式'
    if (cause.detail.code === 'DATABASE_HANDSHAKE_TIMEOUT') return '地址和端口已通过，但数据库握手超时；请确认数据库类型、Service Name / SID 和 TLS 要求'
    if (cause.detail.code.includes('ADDRESS') || cause.detail.code.includes('DNS') || cause.detail.code.includes('NETWORK')) return '地址解析或 Ping 未通过；请确认 Host 可从连接器容器访问'
    if (cause.detail.code.includes('REFUSED')) return '地址和 Ping 已通过，但目标端口拒绝连接；请确认 Port、端口映射和数据库监听状态'
    if (cause.detail.code.includes('TIMEOUT')) return '连接目标端口超时，请检查端口、防火墙和安全组规则'
  }
  const message = cause instanceof Error ? cause.message : '测试数据源连接失败'
  if (/access denied|authentication|password/i.test(message)) return `认证失败：${message}`
  if (/connection refused|connect: refused/i.test(message)) return `目标地址拒绝连接：${message}`
  if (/timeout|deadline exceeded/i.test(message)) return `连接超时：${message}`
  return message
}

/** 提供数据源目录、结构化连接配置和完整生命周期操作，浏览器永不接收已保存密码。 */
export function DataSourceCenterPage() {
  const navigate = useNavigate()
  const searchParams = new URLSearchParams(window.location.search)
  const designSnapshot = import.meta.env.DEV && Boolean(searchParams.get('snapshot'))
  const qaViewport1920 = designSnapshot && searchParams.get('qa') === '1920'
  const [sources, setSources] = useState<DataSourceRecord[]>(designSnapshot ? snapshotSources : [])
  const [sourceManagePermissions, setSourceManagePermissions] = useState<Record<string, boolean>>(
    designSnapshot ? Object.fromEntries(snapshotSources.map(source => [source.id, true])) : {},
  )
  const [loading, setLoading] = useState(!designSnapshot)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [dialog, setDialog] = useState<DialogState | null>(null)
  const [draft, setDraft] = useState<ConnectionDraft>(emptyDraft)
	const [excelFile, setExcelFile] = useState<File | null>(null)
	const [excelAsset, setExcelAsset] = useState<ExcelFileAsset | null>(null)
  const [fileInspection, setFileInspection] = useState<ExcelWorkbookInspection | null>(null)
  const [busyAction, setBusyAction] = useState('')
  const [formError, setFormError] = useState('')
  const [keyword, setKeyword] = useState('')
  const [typeFilter, setTypeFilter] = useState<DataSourceType | 'ALL'>('ALL')
  const [statusFilter, setStatusFilter] = useState<DataSourceStatus | 'ALL'>('ALL')
  const [selectedSourceId, setSelectedSourceId] = useState(designSnapshot ? snapshotSources[0].id : '')
  const [detailDismissed, setDetailDismissed] = useState(false)
  const [assistantOpen, setAssistantOpen] = useState(false)
  const [metadataTables, setMetadataTables] = useState<DataSourceTableRecord[]>([])
  const [metadataColumns, setMetadataColumns] = useState<Record<string, DataSourceColumnRecord[]>>({})
  const [metadataLoading, setMetadataLoading] = useState(false)
  const [metadataError, setMetadataError] = useState('')
  const [columnLoading, setColumnLoading] = useState<Record<string, boolean>>({})
  const [discoveredTables, setDiscoveredTables] = useState<DiscoveredTableRecord[]>([])
  const [selectedTableKeys, setSelectedTableKeys] = useState<string[]>([])
  const [discoveryLoading, setDiscoveryLoading] = useState(false)
  const [tableDraft, setTableDraft] = useState<TableDraft>({ businessName: '', businessDescription: '', tags: '', sensitivityLevel: 'INTERNAL', visibility: 'PRIVATE', manualLocked: false })
  const [columnDrafts, setColumnDrafts] = useState<ColumnDraft[]>([])
  const [tableEditorLoading, setTableEditorLoading] = useState(false)
  const [tableEditorPurpose, setTableEditorPurpose] = useState<TableEditorPurpose>('EDIT')
  const [refreshMode, setRefreshMode] = useState<MetadataRefreshMode>('INCREMENTAL')
  const [sampleDataMode, setSampleDataMode] = useState<MetadataSampleMode>('MASK')
  const [metadataJob, setMetadataJob] = useState<MetadataJob | null>(null)
  const [metadataJobSourceId, setMetadataJobSourceId] = useState('')
  const [metadataJobTitle, setMetadataJobTitle] = useState('')
  const [metadataJobLoading, setMetadataJobLoading] = useState(false)
  const metadataRequest = useRef(0)
  const metadataJobRequests = useRef(new Map<string, number>())
  const metadataJobCache = useRef(new Map<string, MetadataJobSnapshot>())
  const metadataJobPollers = useRef(new Map<string, MetadataJobPoller>())
  const connectionTestPollers = useRef(new Map<string, ConnectionTestPoller>())
  const metadataJobSourceIdRef = useRef('')
  const metadataJobLogRef = useRef<HTMLDivElement>(null)
  const tableEditorRequest = useRef(0)
  const discoveryRequest = useRef(0)
  const notifiedMetadataJobs = useRef(new Set<string>())
  const viewedSourceIdRef = useRef(dialog?.mode === 'view' ? dialog.source?.id || '' : '')
  const signedInSubject = currentSubject()
  const selectedDomainID = useSyncExternalStore(
    subscribeDomainChange,
    currentDomainID,
    () => '',
  )
  useEffect(() => {
    viewedSourceIdRef.current = dialog?.mode === 'view' ? dialog.source?.id || '' : ''
  }, [dialog?.mode, dialog?.source?.id])

  useEffect(() => {
    if (!notice) return
    const timeout = window.setTimeout(() => setNotice(null), 4500)
    return () => window.clearTimeout(timeout)
  }, [notice])

  const filteredSources = useMemo(() => {
    const query = keyword.trim().toLocaleLowerCase()
    return sources.filter(source => {
      const matchesKeyword = !query || source.name.toLocaleLowerCase().includes(query) || source.code.toLocaleLowerCase().includes(query)
      return matchesKeyword && (typeFilter === 'ALL' || source.type === typeFilter) && (statusFilter === 'ALL' || source.status === statusFilter)
    })
  }, [keyword, sources, statusFilter, typeFilter])
  const sourceOverview = useMemo(() => ({
    active: sources.filter(source => source.status === 'ACTIVE').length,
    pending: sources.filter(source => reviewStatusOf(source) === 'PENDING').length,
    drafts: sources.filter(source => hasUnpublishedDraft(source) && reviewStatusOf(source) !== 'PENDING').length,
    attention: sources.filter(source => source.status === 'ERROR' || reviewStatusOf(source) === 'REJECTED').length,
  }), [sources])
  const selectedSource = useMemo(
    () => sources.find(source => source.id === selectedSourceId) || (!detailDismissed ? sources[0] || null : null),
    [detailDismissed, selectedSourceId, sources],
  )
  const selectedCanManage = Boolean(selectedSource && sourceManagePermissions[selectedSource.id])
  const replacingFileSource = useMemo(() => {
    if (draft.type !== 'EXCEL' || !draft.code.trim()) return null
    const code = draft.code.trim().toLocaleLowerCase()
    return sources.find(source => source.type === 'EXCEL' && source.code.toLocaleLowerCase() === code && source.fileAssetId) || null
  }, [draft.code, draft.type, sources])

  const loadManagePermissions = useCallback(async (items: DataSourceRecord[]) => {
    if (designSnapshot) {
      setSourceManagePermissions(Object.fromEntries(items.map(source => [source.id, true])))
      return
    }
    const entries = await Promise.all(items.map(async source => {
      try {
        return [source.id, (await dataSourceAPI.evaluatePermission(source.id, 'MANAGE')).allowed] as const
      } catch {
        return [source.id, false] as const
      }
    }))
    setSourceManagePermissions(Object.fromEntries(entries))
  }, [designSnapshot])

  const loadSources = useCallback(async () => {
    if (designSnapshot) {
      setSources(snapshotSources)
      setLoading(false)
      return snapshotSources
    }
    try {
      const page = await dataSourceAPI.list()
      const items = Array.isArray(page.items) ? page.items : []
      setSources(items)
      await loadManagePermissions(items)
      return items
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载数据源失败' })
      return null
    } finally {
      setLoading(false)
    }
  }, [designSnapshot, loadManagePermissions])

  const runConnectionTest = useCallback((sourceId: string) => {
    const existing = connectionTestPollers.current.get(sourceId)
    if (existing) return existing.promise
    const controller = new AbortController()
    const tracker: ConnectionTestPoller = {
      controller,
      promise: Promise.resolve({ serverVersion: '', latencyMs: 0 }),
    }
    tracker.promise = dataSourceAPI.test(
      sourceId, { signal: controller.signal },
    ).finally(() => {
      if (connectionTestPollers.current.get(sourceId) === tracker) {
        connectionTestPollers.current.delete(sourceId)
      }
    })
    connectionTestPollers.current.set(sourceId, tracker)
    return tracker.promise
  }, [])

  const loadTableStructures = useCallback(async (sourceId: string) => {
    const request = ++metadataRequest.current
    setMetadataLoading(true)
    setMetadataError('')
    setMetadataTables([])
    setMetadataColumns({})
    setColumnLoading({})
    try {
      const result = await dataSourceAPI.tables(sourceId)
      if (request === metadataRequest.current) setMetadataTables(result.items)
    } catch (cause) {
      if (request === metadataRequest.current) setMetadataError(cause instanceof Error ? cause.message : '加载表结构失败')
    } finally {
      if (request === metadataRequest.current) setMetadataLoading(false)
    }
  }, [])

  const startMetadataJobPolling = useCallback((sourceId: string, initialJob: MetadataJob, title: string) => {
    const existing = metadataJobPollers.current.get(sourceId)
    if (existing?.jobId === initialJob.id && !existing.stopped) return
    if (existing) {
      existing.stopped = true
      window.clearTimeout(existing.timeout)
    }
    const tracker: MetadataJobPoller = { jobId: initialJob.id, timeout: 0, stopped: false }
    metadataJobPollers.current.set(sourceId, tracker)
    const current = () => !tracker.stopped && metadataJobPollers.current.get(sourceId) === tracker
    const schedule = (delay: number) => {
      tracker.timeout = window.setTimeout(() => void poll(), delay)
    }
    const poll = async () => {
      try {
        const next = await dataSourceAPI.getMetadataJob(sourceId, tracker.jobId)
        if (!current()) return
        metadataJobCache.current.set(sourceId, { job: next, title })
        if (metadataJobTerminal(next)) {
          // 只刷新当前正在查看的数据源，避免后台 A 任务覆盖 B 数据源的表清单。
          if (viewedSourceIdRef.current === sourceId) await loadTableStructures(sourceId)
          if (!current()) return
          if (metadataJobSourceIdRef.current === sourceId) {
            setMetadataJob(next)
            setMetadataJobTitle(title)
          }
          metadataJobPollers.current.delete(sourceId)
          if (!notifiedMetadataJobs.current.has(next.id)) {
            notifiedMetadataJobs.current.add(next.id)
            setNotice(metadataJobCompletionNotice(next, title))
          }
          return
        }
        if (metadataJobSourceIdRef.current === sourceId) {
          setMetadataJob(next)
          setMetadataJobTitle(title)
        }
        schedule(1200)
      } catch (cause) {
        if (!current()) return
        setNotice({ tone: 'error', message: `${cause instanceof Error ? cause.message : '查询后台元数据任务进度失败'}；将自动重试` })
        schedule(1800)
      }
    }
    schedule(1200)
  }, [loadTableStructures])

  const loadLatestMetadataJob = useCallback(async (sourceId: string) => {
    const request = (metadataJobRequests.current.get(sourceId) || 0) + 1
    metadataJobRequests.current.set(sourceId, request)
    setMetadataJobLoading(true)
    try {
      const result = await dataSourceAPI.latestActiveMetadataJob(sourceId)
      if (request !== metadataJobRequests.current.get(sourceId)) return
      if (result.job) {
        const cached = metadataJobCache.current.get(sourceId)
        const title = cached?.job.id === result.job.id ? cached.title : metadataJobLabel(result.job)
        metadataJobCache.current.set(sourceId, { job: result.job, title })
        startMetadataJobPolling(sourceId, result.job, title)
      }
      if (metadataJobSourceIdRef.current === sourceId) {
        const snapshot = metadataJobCache.current.get(sourceId)
        setMetadataJobSourceId(sourceId)
        setMetadataJob(snapshot?.job || null)
        setMetadataJobTitle(snapshot?.title || '')
      }
    } catch (cause) {
      if (request === metadataJobRequests.current.get(sourceId)) setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载后台元数据任务失败' })
    } finally {
      if (request === metadataJobRequests.current.get(sourceId) && metadataJobSourceIdRef.current === sourceId) setMetadataJobLoading(false)
    }
  }, [startMetadataJobPolling])

  useEffect(() => {
    const pollers = metadataJobPollers.current
    const testPollers = connectionTestPollers.current
    return () => {
      pollers.forEach(poller => {
        poller.stopped = true
        window.clearTimeout(poller.timeout)
      })
      pollers.clear()
      testPollers.forEach(poller => poller.controller.abort())
      testPollers.clear()
    }
  }, [])

  const loadColumns = async (table: DataSourceTableRecord) => {
    if (metadataColumns[table.id] || columnLoading[table.id]) return
    setColumnLoading(current => ({ ...current, [table.id]: true }))
    try {
      const result = await dataSourceAPI.columns(table.id)
      setMetadataColumns(current => ({ ...current, [table.id]: result.items.filter(column => column.assetStatus === 'ACTIVE') }))
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : `加载表 ${table.tableName} 的字段失败` })
    } finally {
      setColumnLoading(current => ({ ...current, [table.id]: false }))
    }
  }

  useEffect(() => {
    if (designSnapshot) return undefined
    let active = true
    dataSourceAPI.list().then(async page => {
      if (!active) return
      const items = Array.isArray(page.items) ? page.items : []
      setSources(items)
      await loadManagePermissions(items)
      if (!active) return
      const resumable = new Set(
        dataSourceAPI.pendingConnectionTestSourceIds(),
      )
      items.filter(source => resumable.has(source.id)).forEach(source => {
        void runConnectionTest(source.id).then(async result => {
          if (!active) return
          const current = await dataSourceAPI.get(source.id)
          if (!active) return
          setSources(items => items.map(item => item.id === current.id ? current : item))
          if (!connectionTestStillCurrent(current, result)) {
            setNotice({ tone: 'error', message: `“${source.name}”后台测试对应的草稿已变化，请重新测试` })
            return
          }
          setNotice({ tone: 'success', message: `“${source.name}”后台连接测试已完成${hasUnpublishedDraft(current) ? '；当前草稿可以上线' : ''}` })
        }).catch(cause => {
          if (!active || (cause instanceof Error && cause.name === 'AbortError')) return
          setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : `恢复“${source.name}”后台连接测试失败` })
        })
      })
    }).catch(cause => {
      if (active) setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载数据源失败' })
    }).finally(() => {
      if (active) setLoading(false)
    })
    return () => { active = false }
  }, [designSnapshot, loadManagePermissions, runConnectionTest, selectedDomainID])

  const openCreate = () => {
    setFormError('')
    setDraft(emptyDraft())
    setExcelFile(null)
    setExcelAsset(null)
    setFileInspection(null)
    setDialog({ mode: 'create' })
  }

  const openExisting = (mode: DialogState['mode'], source: DataSourceRecord) => {
    if (mode === 'view' && (reviewStatusOf(source) === 'PENDING' || reviewStatusOf(source) === 'REJECTED')) {
      setNotice({
        tone: 'error',
        message: reviewStatusOf(source) === 'PENDING'
          ? '该数据源正在审核中，审核完成前不能配置数据表'
          : `该数据源审核失败，修改配置并重新测试提交后才能配置数据表${source.reviewNote ? `；原因：${source.reviewNote}` : ''}`,
      })
      return
    }
    setFormError('')
    setDraft(draftFromSource(source))
		setExcelFile(null)
		setExcelAsset(null)
    setFileInspection(null)
    setDialog({ mode, source })
    if (mode === 'view') {
      setRefreshMode('INCREMENTAL')
      setSampleDataMode('MASK')
      void loadTableStructures(source.id)
      viewedSourceIdRef.current = source.id
      metadataJobSourceIdRef.current = source.id
      const snapshot = metadataJobCache.current.get(source.id)
      setMetadataJobSourceId(source.id)
      setMetadataJob(snapshot?.job || null)
      setMetadataJobTitle(snapshot?.title || '')
      // 每次打开都恢复服务端活动任务，避免同源终态缓存遮蔽其他页面新建的任务。
      void loadLatestMetadataJob(source.id)
    }
  }
  const openTableSelection = async (source: DataSourceRecord) => {
    const request = ++discoveryRequest.current
    setDialog({ mode: 'select-tables', source })
    setDiscoveredTables([])
    setFileInspection(null)
    setSelectedTableKeys([])
    setSampleDataMode('MASK')
    setDiscoveryLoading(true)
    setFormError('')
    try {
      if (source.type === 'EXCEL') {
        const inspection = await dataSourceAPI.inspectExcelSource(source.id)
        if (request === discoveryRequest.current) {
          setFileInspection(inspection)
          setDiscoveredTables(inspectionTables(inspection))
        }
      } else {
        const result = await dataSourceAPI.discoverTables(source.id)
        if (request === discoveryRequest.current) setDiscoveredTables(result.items)
      }
    } catch (cause) {
      if (request === discoveryRequest.current) setFormError(cause instanceof Error ? cause.message : source.type === 'EXCEL' ? '解析文件 Sheet 失败' : '读取源库数据表失败')
    } finally {
      if (request === discoveryRequest.current) setDiscoveryLoading(false)
    }
  }
  const openTableEditor = async (source: DataSourceRecord, table: DataSourceTableRecord, purpose: TableEditorPurpose = 'EDIT') => {
    const request = ++tableEditorRequest.current
    setTableEditorPurpose(purpose)
    setTableDraft({ businessName: table.businessName, businessDescription: table.businessDescription, tags: table.tags.join(', '), sensitivityLevel: table.sensitivityLevel, visibility: table.visibility, manualLocked: purpose === 'MANUAL_COMPLETE' || table.manualLocked })
    setColumnDrafts([])
    setTableEditorLoading(true)
    setFormError('')
    setDialog({ mode: 'edit-table', source, table })
    try {
      // 编辑时重新读取字段，确保 expectedVersion 来自最新资产版本。
      const result = await dataSourceAPI.columns(table.id)
      if (request !== tableEditorRequest.current) return
      const columns = result.items.filter(column => column.assetStatus === 'ACTIVE')
      setMetadataColumns(current => ({ ...current, [table.id]: columns }))
      setColumnDrafts(columns.map(column => {
        const draft = columnDraftFromRecord(column)
        return purpose === 'MANUAL_COMPLETE' ? { ...draft, manualLocked: true } : draft
      }))
    } catch (cause) {
      if (request === tableEditorRequest.current) setFormError(cause instanceof Error ? cause.message : '加载字段映射失败')
    } finally {
      if (request === tableEditorRequest.current) setTableEditorLoading(false)
    }
  }
  const closeDialog = () => {
    if (!busyAction) {
      metadataRequest.current += 1
      tableEditorRequest.current += 1
      discoveryRequest.current += 1
      viewedSourceIdRef.current = ''
      setFileInspection(null)
      setTableEditorPurpose('EDIT')
      setDialog(null)
    }
  }
  const returnToTableAssets = (source: DataSourceRecord) => {
    tableEditorRequest.current += 1
    discoveryRequest.current += 1
    viewedSourceIdRef.current = source.id
    setTableEditorPurpose('EDIT')
    setDialog({ mode: 'view', source })
    void loadTableStructures(source.id)
  }
  const updateDraft = (key: keyof ConnectionDraft, value: string) => setDraft(current => ({ ...current, [key]: value }))
  const updateColumnDraft = (id: string, update: Partial<Omit<ColumnDraft, 'original'>>) => setColumnDrafts(current => current.map(column => column.original.id === id ? { ...column, ...update } : column))

  const acceptMetadataJob = async (job: MetadataJob, sourceId: string, title: string, queuedMessage: string) => {
    notifiedMetadataJobs.current.delete(job.id)
    metadataJobRequests.current.set(sourceId, (metadataJobRequests.current.get(sourceId) || 0) + 1)
    metadataJobCache.current.set(sourceId, { job, title })
    metadataJobSourceIdRef.current = sourceId
    setMetadataJobLoading(false)
    setMetadataJobSourceId(sourceId)
    setMetadataJobTitle(title)
    setMetadataJob(job)
    if (metadataJobActive(job)) {
      startMetadataJobPolling(sourceId, job, title)
      setNotice({ tone: 'success', message: queuedMessage })
      return
    }
    await loadTableStructures(sourceId)
    notifiedMetadataJobs.current.add(job.id)
    setNotice(metadataJobCompletionNotice(job, title))
  }

  const importSelectedTables = async () => {
    const source = dialog?.source
    if (!source || selectedTableKeys.length === 0) {
      setFormError('请至少选择一张数据表')
      return
    }
    const selected = new Set(selectedTableKeys)
    const tables = discoveredTables.filter(table => selected.has(discoveredTableKey(table))).map(table => ({ catalogName: table.catalogName, schemaName: table.schemaName, tableName: table.name }))
    setBusyAction('import-tables')
    setFormError('')
    try {
      const job = await dataSourceAPI.importTables(source.id, tables, sampleDataMode)
      const fileSource = source.type === 'EXCEL'
      const queuedMessage = fileSource
        ? `已提交 ${tables.length} 个 Sheet 的 LLM 元数据完善任务（${metadataSampleDescription(sampleDataMode)}），可关闭当前弹窗`
        : `已提交 ${tables.length} 张表的 LLM 元数据完善任务（${metadataSampleDescription(sampleDataMode)}），可关闭当前弹窗`
      await acceptMetadataJob(job, source.id, fileSource ? 'Sheet 映射' : '新增数据表', queuedMessage)
      setDialog({ mode: 'view', source })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '新增数据表资产失败')
    } finally {
      setBusyAction('')
    }
  }

  const saveTableAsset = async (completeManually = false) => {
    const source = dialog?.source
    const table = dialog?.table
    if (!source || !table || !tableDraft.businessName.trim()) {
      setFormError('请填写数据表业务名称')
      return
    }
    setBusyAction(`${completeManually ? 'manual-complete' : 'edit-table'}:${table.id}`)
    setFormError('')
    let saved = 0
    let saving = '表信息'
    let currentTable = table
    try {
      if (tableDraftChanged(tableDraft, table)) {
        const updated = await dataSourceAPI.updateTable(table.id, {
          businessName: tableDraft.businessName.trim(), businessDescription: tableDraft.businessDescription.trim(),
          tags: normalizedTags(tableDraft.tags), sensitivityLevel: tableDraft.sensitivityLevel,
          visibility: tableDraft.visibility, manualLocked: tableDraft.manualLocked, expectedVersion: table.businessVersion,
        })
        currentTable = updated
        saved += 1
        setDialog(current => current?.mode === 'edit-table' && current.table?.id === updated.id ? { ...current, table: updated } : current)
      }
      for (const column of columnDrafts.filter(columnDraftChanged)) {
        saving = `字段“${column.original.columnName}”`
        const updated = await dataSourceAPI.updateColumn(column.original.id, {
          businessName: column.businessName.trim(), businessDescription: column.businessDescription.trim(),
          tags: normalizedTags(column.tags), sensitivityLevel: column.sensitivityLevel, semanticType: column.semanticType,
          manualLocked: column.manualLocked, expectedVersion: column.original.businessVersion,
        })
        saved += 1
        setColumnDrafts(current => current.map(item => item.original.id === updated.id ? columnDraftFromRecord(updated) : item))
      }
      if (completeManually) {
        saving = '手工完善提交'
        currentTable = await dataSourceAPI.completeTableManually(
          table.id, currentTable.businessVersion, currentTable.structureHash,
        )
        setNotice({ tone: 'success', message: `已完成“${currentTable.businessName || currentTable.tableName}”的手工完善，并生成或刷新对应 ODS/DWS 数据集` })
      } else {
        setNotice({ tone: 'success', message: saved === 0 ? '没有需要保存的修改' : `已修改表资产“${table.businessName || table.tableName}”` })
      }
      setDialog({ mode: 'view', source })
      setTableEditorPurpose('EDIT')
      await loadTableStructures(source.id)
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : '修改数据表资产失败'
      setFormError(saved > 0 ? `已保存 ${saved} 项；${saving}保存失败：${message}。未保存修改已保留，请重试。` : `${saving}保存失败：${message}`)
    } finally {
      setBusyAction('')
    }
  }

  const changeTableStatus = async (source: DataSourceRecord, table: DataSourceTableRecord) => {
    const enable = table.managementStatus === 'DISABLED'
    setBusyAction(`table-status:${table.id}`)
    try {
      if (enable) await dataSourceAPI.enableTable(table.id)
      else await dataSourceAPI.disableTable(table.id)
      setNotice({ tone: 'success', message: `已${enable ? '恢复' : '停用'}表资产“${table.businessName || table.tableName}”` })
      await loadTableStructures(source.id)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '更新表资产状态失败' })
    } finally {
      setBusyAction('')
    }
  }

  const refreshTableAsset = async (source: DataSourceRecord, table: DataSourceTableRecord) => {
    setBusyAction(`refresh-table:${table.id}`)
    try {
      const job = await dataSourceAPI.refreshTables(source.id, 'FULL', [table.id], sampleDataMode)
      await acceptMetadataJob(job, source.id, '刷新表结构', `已提交表资产“${table.businessName || table.tableName}”的后台刷新任务`)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '刷新表结构失败' })
    } finally {
      setBusyAction('')
    }
  }

  const refreshAllTableAssets = async (source: DataSourceRecord) => {
    setBusyAction(`refresh-tables:${source.id}`)
    try {
      const job = await dataSourceAPI.refreshTables(source.id, refreshMode, undefined, sampleDataMode)
      const title = refreshMode === 'INCREMENTAL' ? '增量刷新' : '全量刷新'
      await acceptMetadataJob(job, source.id, title, `已提交${refreshMode === 'INCREMENTAL' ? '增量' : '全量'}元数据后台刷新任务，可关闭当前弹窗`)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '提交元数据刷新任务失败' })
    } finally {
      setBusyAction('')
    }
  }

  const reuploadSourceFile = async (source: DataSourceRecord, file: File) => {
    if (!source.fileAssetId) {
      setNotice({ tone: 'error', message: '当前文件数据源缺少文件资产，请删除后重新创建数据源' })
      return
    }
    setBusyAction(`reupload-file:${source.id}`)
    setNotice(null)
    let uploadedAsset: ExcelFileAsset | null = null
    try {
      uploadedAsset = await dataSourceAPI.uploadExcelVersion(source.fileAssetId, file)
      const testResult = await runConnectionTest(source.id)
      const active = await dataSourceAPI.get(source.id)
      if (!connectionTestStillCurrent(active, testResult)) {
        throw new Error('测试完成后数据源配置已变化，请重新测试当前草稿')
      }
      setSources(current => current.map(item => item.id === active.id ? active : item))
      setDialog(current => current?.mode === 'view' && current.source?.id === active.id ? { ...current, source: active } : current)
      setFileInspection(null)
      setDiscoveredTables([])
      setSelectedTableKeys([])
      await loadTableStructures(active.id)
      setNotice({ tone: 'success', message: `已重新上传“${file.name}”并生成文件版本 ${uploadedAsset.version}，新草稿测试通过；上线后再重新解析并映射 Sheet` })
    } catch (cause) {
      const latest = await loadSources()
      const updated = latest?.find(item => item.id === source.id)
      if (updated) setDialog(current => current?.mode === 'view' && current.source?.id === updated.id ? { ...current, source: updated } : current)
      const message = cause instanceof Error ? cause.message : '重新上传源文件失败'
      setNotice({
        tone: 'error',
        message: uploadedAsset
          ? `文件版本 ${uploadedAsset.version} 已保存，但数据源验证失败：${message}。请重新测试连接后再解析 Sheet`
          : `重新上传源文件失败：${message}`,
      })
    } finally {
      setBusyAction('')
    }
  }

  const deleteTableAsset = async () => {
    const source = dialog?.source
    const table = dialog?.table
    if (!source || !table) return
    setBusyAction(`delete-table:${table.id}`)
    try {
      await dataSourceAPI.deleteTable(table.id)
      setNotice({ tone: 'success', message: `已从 PostgreSQL 删除表资产“${table.businessName || table.tableName}”，源库原表未受影响` })
      setDialog({ mode: 'view', source })
      await loadTableStructures(source.id)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '删除数据表资产失败')
    } finally {
      setBusyAction('')
    }
  }

  const saveConnectionDraft = async () => {
    const editing = dialog?.mode === 'edit' ? dialog.source : undefined
    const validationError = connectionDraftError(draft, Boolean(editing))
    if (validationError) throw new Error(validationError)
    const input: DataSourceConnectionInput = {
      code: draft.code.trim(), name: draft.name.trim(), description: draft.description.trim(), visibility: draft.visibility,
      ...((!editing && draft.sharingScope !== 'PRIVATE') || (
        editing &&
        editing.ownerId === signedInSubject &&
        editing.domainId === selectedDomainID
      ) ? { sharingScope: draft.sharingScope } : {}),
      type: draft.type as 'MYSQL' | 'ORACLE', host: draft.host.trim(), port: Number(draft.port),
      database: draft.database.trim(), oracleConnectMode: draft.type === 'ORACLE' ? draft.oracleConnectMode : undefined,
      username: draft.username.trim(), password: draft.password,
    }
    return editing
      ? dataSourceAPI.update(editing.id, { ...input, expectedVersion: editing.version })
      : dataSourceAPI.create(input)
  }

  const testDraftConnection = async () => {
    const editing = dialog?.mode === 'edit' ? dialog.source : undefined
    setBusyAction(editing ? `form-test:${editing.id}` : 'form-test:create')
    setFormError('')
    try {
      const saved = await saveConnectionDraft()
      setSources(current => editing
        ? current.map(source => source.id === saved.id ? saved : source)
        : [saved, ...current.filter(source => source.id !== saved.id)])
      setDialog({ mode: 'edit', source: saved })
      setDraft(draftFromSource(saved))
      const result = await runConnectionTest(saved.id)
      const active = await dataSourceAPI.get(saved.id)
      if (!connectionTestStillCurrent(active, result)) {
        throw new Error('测试完成后配置已经变化，请重新测试当前表单')
      }
      setSources(current => current.map(source => source.id === active.id ? active : source))
      setDialog({ mode: 'edit', source: active })
      setDraft(draftFromSource(active))
      setNotice({ tone: 'success', message: `“${active.name}”连接成功 · ${result.serverVersion || '版本未知'} · ${result.latencyMs} ms；现在可以提交发布审核` })
    } catch (cause) {
      setFormError(friendlyConnectionError(cause))
      const sourceID = editing?.id
      if (sourceID) {
        const latest = await loadSources()
        const updated = latest?.find(source => source.id === sourceID)
        if (updated) setDialog({ mode: 'edit', source: updated })
      }
    } finally {
      setBusyAction('')
    }
  }

  const submitDraftForReview = async () => {
    const source = dialog?.mode === 'edit' ? dialog.source : undefined
    if (!source || !draftMatchesSource(draft, source) || validationStatusOf(source) !== 'PASSED') {
      setFormError('发布前必须先用当前表单完成一次成功的连接测试；修改任一字段后需要重新测试')
      return
    }
    setBusyAction(`review-submit:${source.id}`)
    setFormError('')
    try {
      await dataSourceAPI.submitPublicationRequest(source.id)
      const latest = await loadSources()
      const updated = latest?.find(item => item.id === source.id)
      setDialog(null)
      setNotice({ tone: 'success', message: `“${updated?.name || source.name}”已提交审核；审核完成前仅可撤销申请` })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '提交数据源审核失败')
    } finally {
      setBusyAction('')
    }
  }

  const submitConnection = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
		if (!dataSourceCodePattern.test(draft.code.trim())) {
			setFormError('数据源编码必须以英文字母开头，且只能包含英文字母、数字和下划线，最长 128 位')
			return
		}
		if (draft.type === 'EXCEL') {
			if (!draft.name.trim() || !draft.code.trim()) {
				setFormError('请填写数据源名称和编码')
				return
			}
			if (!excelFile && !excelAsset) {
				setFormError('请选择 .xlsx、.xls 或 .csv 文件')
				return
			}
			setBusyAction('create-excel')
			setFormError('')
			let saved: DataSourceRecord | null = null
			try {
				if (replacingFileSource) {
					const asset = excelAsset && excelAsset.id === replacingFileSource.fileAssetId
						? excelAsset
						: await dataSourceAPI.uploadExcelVersion(replacingFileSource.fileAssetId!, excelFile!)
					setExcelAsset(asset)
					const testResult = await runConnectionTest(replacingFileSource.id)
					const active = await dataSourceAPI.get(replacingFileSource.id)
					if (!connectionTestStillCurrent(active, testResult)) {
						throw new Error('测试完成后数据源配置已变化，请重新测试当前草稿')
					}
					setSources(current => current.map(source => source.id === active.id ? active : source))
						setDialog(null)
						setNotice({ tone: 'success', message: `已覆盖“${active.name}”的源文件并生成版本 ${asset.version}，新草稿测试通过；请从数据源卡片提交发布审核` })
					return
				}
				const asset = excelAsset || await dataSourceAPI.uploadExcel(excelFile!)
				if (!excelAsset) setExcelAsset(asset)
				saved = await dataSourceAPI.create({
          code: draft.code.trim(), name: draft.name.trim(), description: draft.description.trim(),
          visibility: draft.visibility,
          ...(draft.sharingScope === 'PRIVATE' ? {} : { sharingScope: draft.sharingScope }),
          type: 'EXCEL', fileAssetId: asset.id,
        })
				const testResult = await runConnectionTest(saved.id)
				const active = await dataSourceAPI.get(saved.id)
				if (!connectionTestStillCurrent(active, testResult)) {
					throw new Error('测试完成后数据源配置已变化，请重新测试当前草稿')
				}
				setSources(current => [active, ...current.filter(source => source.id !== active.id)])
					setDialog(null)
					setNotice({ tone: 'success', message: `已上传并创建“${active.name}”，文件草稿测试通过；请从数据源卡片提交发布审核` })
			} catch (cause) {
				const message = cause instanceof Error ? cause.message : '新建文件数据源失败'
				if (saved) {
					setSources(current => [saved!, ...current.filter(source => source.id !== saved!.id)])
					setNotice({ tone: 'error', message: `文件数据源已创建，但文件对象验证失败：${message}。请在数据源清单中重新测试连接` })
					setDialog(null)
				} else {
					if (replacingFileSource) void loadSources()
					setFormError(message)
				}
			} finally {
				setBusyAction('')
			}
			return
		}
	    await testDraftConnection()
	  }

  const testConnection = async (source: DataSourceRecord) => {
    setBusyAction(`test:${source.id}`)
    setNotice(null)
    try {
      const result = await runConnectionTest(source.id)
      const latest = await loadSources()
      const updated = latest?.find(item => item.id === source.id)
      if (updated) setDialog(current => current?.mode === 'view' && current.source?.id === updated.id ? { ...current, source: updated } : current)
      if (updated && !connectionTestStillCurrent(updated, result)) {
        setNotice({ tone: 'error', message: '测试完成后数据源配置已变化，请重新测试当前草稿' })
        return
      }
      const publishRequired = updated ? hasUnpublishedDraft(updated) : Boolean(result.configVersionId)
      setNotice({
        tone: 'success',
        message: `“${source.name}”连接成功 · ${result.serverVersion || '版本未知'} · ${result.latencyMs} ms${publishRequired ? '；当前配置仍是草稿，可以提交发布审核' : ''}`,
      })
    } catch (cause) {
      // 测试失败后服务端会把数据源标记为异常，先刷新状态再保留错误原因。
      await loadSources()
      setNotice({ tone: 'error', message: friendlyConnectionError(cause) })
    } finally {
      setBusyAction('')
    }
  }

  const publishSource = async (source: DataSourceRecord) => {
    setBusyAction(`review-submit:${source.id}`)
    setNotice(null)
    try {
      await dataSourceAPI.submitPublicationRequest(source.id)
      const latest = await loadSources()
      const updated = latest?.find(item => item.id === source.id)
      if (updated) setDialog(current => current?.source?.id === updated.id ? { ...current, source: updated } : current)
      setNotice({ tone: 'success', message: `“${source.name}”已提交发布审核；审核完成前仅可撤销申请` })
    } catch (cause) {
      const latest = await loadSources()
      const updated = latest?.find(item => item.id === source.id)
      if (updated) setDialog(current => current?.source?.id === updated.id ? { ...current, source: updated } : current)
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '提交数据源审核失败' })
    } finally {
      setBusyAction('')
    }
  }

  const withdrawReview = async (source: DataSourceRecord) => {
    if (!source.reviewRequestId || !source.reviewRequestVersion) return
    setBusyAction(`review-withdraw:${source.id}`)
    try {
      await dataSourceAPI.withdrawPublicationRequest(source.id, source.reviewRequestId, source.reviewRequestVersion)
      await loadSources()
      setNotice({ tone: 'success', message: `已撤销“${source.name}”的发布审核申请，可以继续修改配置` })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '撤销审核申请失败' })
    } finally {
      setBusyAction('')
    }
  }

  const deleteSource = async () => {
    const source = dialog?.source
    if (!source) return
    setBusyAction(`delete:${source.id}`)
    setFormError('')
    try {
      await dataSourceAPI.delete(source.id)
      setSources(current => current.filter(item => item.id !== source.id))
      setNotice({ tone: 'success', message: `已删除“${source.name}”` })
      setDialog(null)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '删除数据源失败')
    } finally {
      setBusyAction('')
    }
  }

  const actionBusy = Boolean(busyAction)
  const editableFormSource = dialog?.mode === 'edit' ? dialog.source : undefined
  const currentDraftTested = draft.type !== 'EXCEL'
    && draftMatchesSource(draft, editableFormSource)
    && validationStatusOf(editableFormSource!) === 'PASSED'
    && reviewStatusOf(editableFormSource!) !== 'PENDING'
  const visibleMetadataJob = dialog?.source?.id === metadataJobSourceId ? metadataJob : null
  const metadataTaskActive = metadataJobActive(visibleMetadataJob)
  const metadataTaskBusy = metadataTaskActive || metadataJobLoading
  const fileReuploadDisabled = actionBusy || !dialog?.source?.fileAssetId
    || dialog.source.status === 'DISABLED' || dialog.source.status === 'SYNCING' || dialog.source.status === 'DELETING'
  const visibleMetadataJobTitle = visibleMetadataJob ? metadataJobTitle || metadataJobLabel(visibleMetadataJob) : ''
  const metadataProgressMax = Math.max(visibleMetadataJob?.total || 0, 1)
  const metadataProgressValue = visibleMetadataJob
    ? visibleMetadataJob.total > 0
      ? metadataJobActive(visibleMetadataJob) && visibleMetadataJob.completed === 0
        ? undefined
        : Math.min(visibleMetadataJob.completed, visibleMetadataJob.total)
      : metadataJobTerminal(visibleMetadataJob) ? 1 : undefined
    : undefined
  const metadataProgressPercent = visibleMetadataJob
    ? visibleMetadataJob.total > 0 ? Math.min(100, Math.round(visibleMetadataJob.completed / visibleMetadataJob.total * 100)) : metadataJobTerminal(visibleMetadataJob) ? 100 : 0
    : 0
  const metadataProgressLabel = visibleMetadataJob && metadataJobActive(visibleMetadataJob) && visibleMetadataJob.completed === 0
    ? '处理中'
    : `${metadataProgressPercent}%`
  const metadataProgressText = visibleMetadataJob
    ? visibleMetadataJob.total === 0 && metadataJobTerminal(visibleMetadataJob)
      ? '已完成，无需处理数据表'
      : `已处理 ${visibleMetadataJob.completed} / ${visibleMetadataJob.total} 张，${metadataStageLabels[visibleMetadataJob.stage] || visibleMetadataJob.stage || '处理中'}${visibleMetadataJob.currentTable ? `，当前 ${visibleMetadataJob.currentTable}` : ''}`
    : ''
  const visibleMetadataJobFailures = visibleMetadataJob && (visibleMetadataJob.status === 'FAILED' || visibleMetadataJob.status === 'PARTIAL')
    ? visibleMetadataJob.failures || []
    : []
  const visibleMetadataJobLogs = visibleMetadataJob?.logs || []
  useEffect(() => {
    const log = metadataJobLogRef.current
    if (log) log.scrollTop = log.scrollHeight
  }, [visibleMetadataJob?.id, visibleMetadataJob?.status, visibleMetadataJobLogs.length])
  const selectableDiscoveredTables = dialog?.mode === 'select-tables'
    ? discoveredTables.filter(table => dialog.source?.type === 'EXCEL' || !metadataTables.some(asset =>
        assetTableKey(asset) === discoveredTableKey(table) && asset.managementStatus !== 'DISABLED',
      ))
    : []
  const selectedReviewStatus = selectedSource ? reviewStatusOf(selectedSource) : 'NOT_SUBMITTED'
  const selectedUnavailable = selectedSource ? selectedSource.status === 'SYNCING' || selectedSource.status === 'DELETING' : false
  const selectedPendingDraft = selectedSource ? hasUnpublishedDraft(selectedSource) : false
  const selectedCanTest = Boolean(selectedSource && selectedCanManage && !selectedUnavailable && selectedReviewStatus !== 'PENDING')
  const selectedCanPublish = Boolean(selectedSource && selectedCanManage && !selectedUnavailable && selectedPendingDraft && selectedReviewStatus !== 'PENDING' && validationStatusOf(selectedSource) === 'PASSED')
  const selectedSummary = selectedSource ? metadataSummary(selectedSource) : null
  const selectedHealth = selectedSource ? connectionHealth(selectedSource) : null
  const selectedLifecycle = selectedSource ? lifecycleSteps(selectedSource) : []
  const selectedIsRequester = Boolean(selectedSource && (!signedInSubject || selectedSource.reviewRequesterId === signedInSubject))
  const selectedNextAction = !selectedSource
    ? ''
    : !selectedCanManage
      ? '浏览数据资产'
      : selectedReviewStatus === 'PENDING'
      ? '等待发布审批'
      : selectedReviewStatus === 'REJECTED'
        ? '修改配置并重新验证'
        : validationStatusOf(selectedSource) !== 'PASSED'
          ? '重新测试连接'
          : selectedPendingDraft
            ? '提交发布审批'
            : '完善业务元数据'
  const openAssetWorkspace = (source: DataSourceRecord) => navigate(`/data-sources/${source.id}/assets${designSnapshot ? '?snapshot=data-source-assets' : ''}`)
  return (
    <AppShell
      className={`data-source-shell${selectedSource ? ' has-detail' : ''}${qaViewport1920 ? ' qa-viewport-1920' : ''}`}
      title="数据资产"
      eyebrow="数据与治理"
      titleMeta={<span className="data-source-title-meta">管理当前领域的数据连接、验证状态与元数据资产</span>}
      actions={<>
        <AppButton className="data-source-ai-entry" type="button" onClick={() => setAssistantOpen(true)}><Sparkle size={17} weight="fill" />AI 配置</AppButton>
        <AppButton variant="primary" className="data-source-create-button" onClick={openCreate}><Plus size={17} weight="bold" />新建数据源</AppButton>
      </>}
    >
      {notice && <div className={`data-source-toast ${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'} aria-live="polite">
        <span className="data-source-toast-icon" aria-hidden="true">{notice.tone === 'success' ? <CheckCircle size={17} weight="fill" /> : <WarningCircle size={17} weight="fill" />}</span>
        <span>{notice.message}</span>
        <AppButton text circle aria-label="关闭消息" onClick={() => setNotice(null)}><X size={16} /></AppButton>
      </div>}
      <section className="data-source-center" aria-label="数据源配置中心内容">
        <section className="data-source-summary-strip" aria-label="数据源状态概览">
          <article><span className="is-blue"><Database size={20} weight="duotone" /></span><small>全部</small><strong>{sources.length}</strong></article>
          <article><span className="is-green"><CheckCircle size={20} weight="duotone" /></span><small>运行中</small><strong>{sourceOverview.active}</strong></article>
          <article><span className="is-orange"><ClockCountdown size={20} weight="duotone" /></span><small>待审批</small><strong>{sourceOverview.pending}</strong></article>
          <article><span className="is-red"><WarningCircle size={20} weight="duotone" /></span><small>需要处理</small><strong>{sourceOverview.attention}</strong></article>
        </section>

        <section className="data-source-catalog" aria-labelledby="data-source-catalog-title">
          <div className="data-source-filters" aria-label="数据源筛选">
            <label className="data-source-search"><span className="data-source-search-control"><MagnifyingGlass size={18} /><input aria-label="搜索数据源" type="search" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索数据源名称、编码或用途" /></span></label>
            <label><select aria-label="按类型筛选" value={typeFilter} onChange={event => setTypeFilter(event.target.value as DataSourceType | 'ALL')}><option value="ALL">全部类型</option><option value="MYSQL">MySQL</option><option value="ORACLE">Oracle</option><option value="EXCEL">Excel / CSV</option></select></label>
            <label><select aria-label="按状态筛选" value={statusFilter} onChange={event => setStatusFilter(event.target.value as DataSourceStatus | 'ALL')}><option value="ALL">全部状态</option>{Object.entries(statusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
            <AppButton className="data-source-filter-reset" type="button" aria-label="重置筛选" title="重置筛选" onClick={() => { setKeyword(''); setTypeFilter('ALL'); setStatusFilter('ALL') }}><ArrowClockwise size={17} /><span>重置</span></AppButton>
          </div>

          <div className="data-source-table" role="table" aria-labelledby="data-source-catalog-title">
            <div className="data-source-table-head" role="row">
              <span role="columnheader" id="data-source-catalog-title">数据源名称</span>
              <span role="columnheader">类型</span>
              <span role="columnheader">连接健康</span>
              <span role="columnheader">最近测试</span>
              <span role="columnheader">元数据资产</span>
              <span role="columnheader">操作</span>
            </div>
            {loading ? <div className="data-source-empty">正在加载数据源…</div> : sources.length === 0
              ? <div className="data-source-empty"><strong>还没有数据源</strong><span>点击右上角“新建数据源”或“AI 配置”开始接入。</span></div>
              : filteredSources.length === 0 ? <div className="data-source-empty"><strong>没有符合条件的数据源</strong><span>请调整搜索词或筛选条件。</span></div>
              : <div className="data-source-list" role="rowgroup">{filteredSources.map(source => {
                const reviewStatus = reviewStatusOf(source)
                const reviewLocked = reviewStatus === 'PENDING' || reviewStatus === 'REJECTED'
                const unavailable = source.status === 'SYNCING' || source.status === 'DELETING'
                const canManage = Boolean(sourceManagePermissions[source.id])
                const canTest = canManage && !unavailable && reviewStatus !== 'PENDING'
                const subtitle = `${typeLabels[source.type]} · ${source.code}${source.description ? ` · ${source.description}` : ''}`
                const health = connectionHealth(source)
                const summary = metadataSummary(source)
                const isSelected = selectedSource?.id === source.id
                return <article className={`data-source-card${isSelected ? ' is-selected' : ''}${reviewLocked ? ' review-locked' : ''}`} role="row" aria-selected={isSelected} key={source.id}>
                  <AppButton className="data-source-card-open" type="button" aria-label={`查看${source.name}详情`} onClick={() => { setSelectedSourceId(source.id); setDetailDismissed(false) }}>
                    <span className="data-source-name-cell" role="cell">
                      <span className={`data-source-selection-indicator${isSelected ? ' is-selected' : ''}`} aria-hidden="true">{isSelected ? '✓' : ''}</span>
                      <span className={`data-source-icon ${source.type.toLowerCase()}`}>{source.type === 'EXCEL' ? <FileXls size={23} weight="duotone" /> : <Database size={23} weight="duotone" />}</span>
                      <span className="data-source-main"><strong title={source.name}>{source.name}</strong><small title={subtitle}>{subtitle}</small></span>
                    </span>
                    <span className="data-source-type-cell" role="cell"><strong>{source.type === 'EXCEL' ? '文件数据源' : '数据库'}</strong><small>{typeLabels[source.type]}</small></span>
                    <span className={`data-source-health-cell is-${health.tone}`} role="cell"><strong><i />{health.label}</strong><small>{health.detail}</small></span>
                    <span className="data-source-test-cell" role="cell"><strong className={`is-${validationStatusOf(source).toLowerCase()}`}>{validationLabels[validationStatusOf(source)]}</strong><small>{formatDataSourceTime(source.lastTestedAt || source.updatedAt)}</small></span>
                    <span className="data-source-readiness-cell" role="cell"><strong>{summary.readiness === null ? publicationLabels[publicationStatusOf(source)] : `${summary.readiness}%`}</strong>{summary.readiness === null ? <small>进入资产查看进度</small> : <progress aria-label={`${source.name}元数据完善度`} max="100" value={summary.readiness} />}</span>
                  </AppButton>
                  <div className="data-source-actions" role="cell">
                    {canManage && <AppButton className="action-test" type="button" disabled={actionBusy || !canTest} onClick={() => void testConnection(source)}>{busyAction === `test:${source.id}` ? '测试中…' : '测试连接'}</AppButton>}
                    {canManage && <AppButton className="action-edit" type="button" disabled={actionBusy || unavailable || reviewStatus === 'PENDING' || source.type === 'EXCEL'} aria-label={`编辑${source.name}`} onClick={() => openExisting('edit', source)}><PencilSimple size={15} /><span>编辑</span></AppButton>}
                    <AppButton text circle className="action-more" type="button" disabled={reviewLocked} aria-label={`${canManage ? '管理' : '查看'}${source.name}的数据表资产`} title={reviewLocked ? '审核完成后可查看数据表资产' : canManage ? '管理数据表资产' : '查看数据表资产'} onClick={() => openAssetWorkspace(source)}><DotsThree size={19} weight="bold" /></AppButton>
                  </div>
                </article>
              })}</div>}
          </div>
          <footer className="data-source-list-footer"><span>共 {filteredSources.length} 条</span><span>当前领域 · 企业经营</span></footer>
        </section>
      </section>

      {selectedSource && selectedSummary && selectedHealth && <aside className="data-source-inspector" aria-label={`${selectedSource.name}详情`}>
        <header>
          <div><strong>{selectedSource.name}</strong><small>{selectedSource.code}</small></div>
          <AppButton text circle type="button" aria-label="关闭数据源详情" onClick={() => { setDetailDismissed(true); setSelectedSourceId('') }}><X size={19} /></AppButton>
        </header>
        <div className="data-source-inspector-actions">
          {selectedCanManage && <AppButton variant="primary" className="action-test" type="button" disabled={actionBusy || !selectedCanTest} onClick={() => void testConnection(selectedSource)}>{busyAction === `test:${selectedSource.id}` ? '测试中…' : '测试连接'}</AppButton>}
          {selectedCanManage && <AppButton className="action-edit" type="button" disabled={actionBusy || selectedUnavailable || selectedReviewStatus === 'PENDING' || selectedSource.type === 'EXCEL'} onClick={() => openExisting('edit', selectedSource)}><PencilSimple size={15} />编辑</AppButton>}
          <AppButton className="action-assets" type="button" disabled={selectedReviewStatus === 'PENDING' || selectedReviewStatus === 'REJECTED'} onClick={() => openAssetWorkspace(selectedSource)}>{selectedCanManage ? '管理资产' : '查看资产'}</AppButton>
        </div>

        <div className="data-source-inspector-scroll">
          <section className="data-source-inspector-card lifecycle-card">
            <header><strong>生命周期状态</strong><span className={`data-source-status ${selectedReviewStatus === 'PENDING' ? 'review-pending' : selectedReviewStatus === 'REJECTED' ? 'review-rejected' : selectedSource.status.toLowerCase()}`}>{lifecycleLabel(selectedSource)}</span></header>
            <ol>{selectedLifecycle.map((step, index) => <li className={step.complete ? 'is-complete' : ''} key={step.label}>{step.complete ? <CheckCircle size={21} weight="fill" /> : <Circle size={21} />}<span>{step.label}<small>{index === 0 ? '连接配置' : index === 1 ? formatDataSourceTime(selectedSource.lastTestedAt) : index === 2 ? publicationLabels[publicationStatusOf(selectedSource)] : selectedSummary.readiness === null ? '进入资产查看' : `${selectedSummary.readiness}%`}</small></span></li>)}</ol>
          </section>

          <section className="data-source-inspector-card">
            <header><strong>连接信息</strong></header>
            <dl className="connection-facts">
              <div><dt>数据源类型</dt><dd>{typeLabels[selectedSource.type]}</dd></div>
              <div><dt>Host / IP</dt><dd>{configText(selectedSource, 'host') || (selectedSource.type === 'EXCEL' ? '文件数据源' : '—')}</dd></div>
              <div><dt>端口</dt><dd>{configText(selectedSource, 'port') || '—'}</dd></div>
              <div><dt>数据库</dt><dd>{configText(selectedSource, 'database') || '—'}</dd></div>
              <div><dt>用户名</dt><dd>{configText(selectedSource, 'username') || '—'}</dd></div>
              <div><dt>描述</dt><dd>{selectedSource.description || '未填写'}</dd></div>
            </dl>
          </section>

          <section className="data-source-inspector-card">
            <header><strong>最近测试结果</strong><span className={`receipt-status is-${validationStatusOf(selectedSource).toLowerCase()}`}>{validationLabels[validationStatusOf(selectedSource)]}</span></header>
            <dl className="receipt-facts">
              <div><dt>测试时间</dt><dd>{formatDataSourceTime(selectedSource.lastTestedAt || selectedSource.updatedAt)}</dd></div>
              <div><dt>连接健康</dt><dd>{selectedHealth.label}</dd></div>
              <div><dt>响应延迟</dt><dd>{selectedHealth.detail}</dd></div>
              <div><dt>配置版本</dt><dd>v{selectedSource.configVersion || selectedSource.version}</dd></div>
            </dl>
          </section>

          <section className="data-source-inspector-card metadata-card">
            <header><strong>元数据资产</strong><span>{selectedSummary.readiness === null ? '查看实际进度' : `${selectedSummary.readiness}%`}</span></header>
            {selectedSummary.readiness !== null && <progress aria-label="元数据完善度" max="100" value={selectedSummary.readiness} />}
            <dl><div><dt>数据表</dt><dd>{selectedSummary.tables}</dd></div><div><dt>字段</dt><dd>{selectedSummary.fields}</dd></div><div><dt>业务指标</dt><dd>{selectedSummary.business}</dd></div></dl>
          </section>

          <section className="data-source-inspector-card sharing-card">
            <header><strong>共享范围</strong></header>
            <dl><div><dt>所属领域</dt><dd>企业经营</dd></div><div><dt>当前范围</dt><dd><AssetSharingSelect resourceType="DATA_SOURCE" resourceID={selectedSource.id} value={selectedSource.sharingScope || (selectedSource.visibility === 'TENANT_PUBLIC' ? 'DOMAIN' : 'PRIVATE')} ownerUserID={selectedSource.ownerId} assetDomainID={selectedSource.domainId} disabled={!selectedCanManage || actionBusy || selectedUnavailable} onChange={sharingScope => setSources(current => current.map(item => item.id === selectedSource.id ? { ...item, sharingScope } : item))} /></dd></div></dl>
          </section>
        </div>

        <footer className="data-source-next-action">
          <div><Sparkle size={18} weight="fill" /><span><strong>下一步：{selectedNextAction}</strong><small>{!selectedCanManage ? '以只读方式查看已共享的数据表、字段定义与治理状态。' : selectedReviewStatus === 'PENDING' ? '审核通过后继续元数据发现与资产完善。' : selectedReviewStatus === 'REJECTED' ? (selectedSource.reviewNote || '根据审核意见修改配置后重新提交。') : validationStatusOf(selectedSource) !== 'PASSED' ? '生成新的连接测试收据，确认当前配置可用。' : selectedPendingDraft ? '固定当前已验证版本并进入发布审核。' : '补充业务定义、指标口径与血缘关系。'}</small></span></div>
          <AppButton variant="primary" type="button" disabled={actionBusy || (selectedReviewStatus === 'PENDING' && !selectedIsRequester)} onClick={() => {
            if (!selectedCanManage) openAssetWorkspace(selectedSource)
            else if (selectedReviewStatus === 'PENDING') void withdrawReview(selectedSource)
            else if (selectedReviewStatus === 'REJECTED') openExisting('edit', selectedSource)
            else if (validationStatusOf(selectedSource) !== 'PASSED') void testConnection(selectedSource)
            else if (selectedCanPublish) void publishSource(selectedSource)
            else openAssetWorkspace(selectedSource)
          }}>{!selectedCanManage ? '查看资产' : selectedReviewStatus === 'PENDING' ? selectedIsRequester ? '撤销申请' : '等待审批' : selectedReviewStatus === 'REJECTED' ? '去修改' : validationStatusOf(selectedSource) !== 'PASSED' ? '重新测试' : selectedCanPublish ? '提交审批' : '去完善'}</AppButton>
        </footer>
      </aside>}

		{(dialog?.mode === 'create' || dialog?.mode === 'edit') && <Dialog title={dialog.mode === 'edit' ? '修改数据源' : '新建数据源'} wide={draft.type === 'EXCEL'} onClose={closeDialog}>
        <form className="data-source-form" onSubmit={submitConnection}>
          <div className="data-source-form-grid">
            <label>数据源名称<input autoFocus value={draft.name} onChange={event => updateDraft('name', event.target.value)} placeholder="例如：销售业务库" /></label>
            <label>数据源编码<input aria-label="数据源编码" maxLength={128} value={draft.code} onChange={event => updateDraft('code', event.target.value)} placeholder="例如：sales_mysql" /><small>以英文字母开头，仅支持英文字母、数字和下划线；中文文件名会自动转换为 MD5 编码。</small></label>
            <label>数据源描述<input value={draft.description} onChange={event => updateDraft('description', event.target.value)} placeholder="说明数据范围、用途和使用边界" /></label>
            <label>分享状态<select
              aria-label="分享状态"
              value={draft.sharingScope}
              disabled={dialog.mode === 'edit' && (
                dialog.source?.ownerId !== signedInSubject ||
                dialog.source?.domainId !== selectedDomainID
              )}
              title={dialog.mode === 'edit' && (
                dialog.source?.ownerId !== signedInSubject ||
                dialog.source?.domainId !== selectedDomainID
              ) ? '仅数据源持有者可以在资产所属领域修改共享状态' : undefined}
              onChange={event => setDraft(current => ({ ...current, sharingScope: event.target.value as AssetSharingScope }))}
            ><option value="PRIVATE">仅自己</option><option value="DOMAIN">领域内共享</option></select>{dialog.mode === 'edit' && (
              dialog.source?.ownerId !== signedInSubject ||
              dialog.source?.domainId !== selectedDomainID
            ) && <small>仅数据源持有者可以在资产所属领域修改共享状态。</small>}</label>
            <label>数据源类型<select value={draft.type} onChange={event => {
				const type = event.target.value as DataSourceType
				setExcelFile(null)
				setExcelAsset(null)
				setFormError('')
              setDraft(current => ({ ...current, type, port: current.port === '3306' || current.port === '1521' ? (type === 'ORACLE' ? '1521' : '3306') : current.port }))
			}}><option value="MYSQL">MySQL</option><option value="ORACLE">Oracle</option>{dialog.mode === 'create' && <option value="EXCEL">Excel / CSV</option>}</select></label>
			{draft.type !== 'EXCEL' && <>
            <label>Host<input value={draft.host} onChange={event => updateDraft('host', event.target.value)} placeholder="db.example.internal" /></label>
            <label>Port<input inputMode="numeric" value={draft.port} onChange={event => updateDraft('port', event.target.value)} placeholder={draft.type === 'ORACLE' ? '1521' : '3306'} /></label>
            {draft.type === 'ORACLE' && <label>Oracle 连接模式<select value={draft.oracleConnectMode} onChange={event => updateDraft('oracleConnectMode', event.target.value as 'SERVICE_NAME' | 'SID')}><option value="SERVICE_NAME">Service Name</option><option value="SID">SID</option></select></label>}
            <label>{draft.type === 'ORACLE' ? (draft.oracleConnectMode === 'SID' ? 'Oracle SID' : 'Oracle Service Name') : 'Database'}<input value={draft.database} onChange={event => updateDraft('database', event.target.value)} placeholder={draft.type === 'ORACLE' ? (draft.oracleConnectMode === 'SID' ? 'ORCL' : 'FREEPDB1') : 'sales'} /></label>
            <label>Username<input autoComplete="username" value={draft.username} onChange={event => updateDraft('username', event.target.value)} placeholder="report_reader" /></label>
            <label>Password<input aria-label="Password" type="password" autoComplete="new-password" value={draft.password} onChange={event => updateDraft('password', event.target.value)} placeholder={dialog.mode === 'edit' ? '留空表示保留原密码' : '请输入数据库密码'} /><small>{dialog.mode === 'edit' ? '密码不会回显；仅在需要更换时填写。' : '密码由服务端加密保存，不使用 JDBC 连接串。'}</small></label>
			</>}
          </div>
			{draft.type === 'EXCEL' && <section className="excel-source-upload" aria-label="Excel 文件上传">
				<header><div><strong>{replacingFileSource ? '覆盖数据源文件' : '上传文件并创建数据源'}</strong><small>选择文件后自动填写数据源名称和稳定编码；相同编码会覆盖当前源文件并保留历史版本。</small></div><span>.xlsx / .xls / .csv</span></header>
				<div className="excel-source-file-row upload-only">
					<div className="excel-source-file-picker">
						<label className="excel-source-file-button"><span aria-hidden="true">＋</span>{excelFile ? '重新选择文件' : '选择文件'}<input className="excel-source-file-input" key={draft.type} aria-label="Excel 文件" type="file" accept=".xlsx,.xls,.csv" onChange={event => {
							const file = event.target.files?.[0] || null
							setExcelFile(file)
							setExcelAsset(null)
							setFormError('')
							if (file) {
								const identity = fileSourceIdentity(file.name)
								setDraft(current => ({ ...current, name: identity.name, code: identity.code }))
							}
						}} /></label>
						<span className={`excel-source-file-name${excelFile ? ' selected' : ''}`}><strong>{excelFile?.name || '尚未选择文件'}</strong><small>{excelFile ? `${formatFileSize(excelFile.size)} · 可重新选择其他文件` : '支持 Excel 工作簿或单个 CSV 文件'}</small></span>
					</div>
				</div>
				{replacingFileSource && <div className="excel-source-replace-note" role="status">已识别已有数据源“{replacingFileSource.name}”。提交后会将文件资产切换到新版本，不会重复创建数据源。</div>}
				<small className="excel-source-next-step">完成后先测试并上线，再进入“数据表资产”解析每个 Sheet；LLM 默认只使用技术元数据，业务样本必须在任务中另行明确授权。</small>
				</section>}
	          {draft.type !== 'EXCEL' && <div className={`data-source-test-readiness${currentDraftTested ? ' ready' : ''}`} role="status">
	            <span aria-hidden="true">{currentDraftTested ? '✓' : '1'}</span>
	            <div><strong>{currentDraftTested ? '当前配置已通过连接测试' : '先测试当前配置，再提交发布审核'}</strong><small>{currentDraftTested ? '发布按钮已启用；提交后数据源进入审核中状态。' : '测试会先保存表单草稿；修改任何连接字段后，需要重新测试。'}</small></div>
	          </div>}
	          {formError && <div className="data-source-feedback error" role="alert">{formError}</div>}
				{draft.type === 'EXCEL'
				  ? <footer><AppButton className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</AppButton><AppButton variant="primary" className="primary-button" type="submit" disabled={actionBusy}>{actionBusy ? replacingFileSource ? '正在覆盖源文件…' : '正在上传并创建…' : replacingFileSource ? '覆盖并更新源文件' : '上传并创建数据源'}</AppButton></footer>
				  : <footer className="data-source-config-footer">
				      <AppButton className="test-connection-button" type="submit" disabled={actionBusy}>{busyAction.startsWith('form-test:') ? '正在保存并测试…' : '测试连接'}</AppButton>
				      <span><AppButton className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</AppButton><AppButton variant="primary" className="primary-button" type="button" disabled={actionBusy || !currentDraftTested} title={currentDraftTested ? '提交当前测试版本进入发布审核' : '请先测试当前表单并确保连接成功'} onClick={() => void submitDraftForReview()}>{busyAction.startsWith('review-submit:') ? '正在提交…' : '发布'}</AppButton></span>
				    </footer>}
	        </form>
      </Dialog>}

      {dialog?.mode === 'view' && dialog.source && <Dialog title="数据表资产" wide onClose={closeDialog}>
        <div className="data-source-detail">
          <div className="data-source-detail-actions" aria-label="表资产操作">
            <AppButton variant="primary" className="action-add-table" type="button" disabled={actionBusy || metadataTaskBusy || dialog.source.status !== 'ACTIVE'} onClick={() => void openTableSelection(dialog.source!)}>新增数据表</AppButton>
            {dialog.source.type === 'EXCEL'
              ? <label className={`data-source-file-reupload${fileReuploadDisabled ? ' disabled' : ''}`}><span aria-hidden="true">↻</span>{busyAction === `reupload-file:${dialog.source.id}` ? '正在重新上传…' : '重新上传文件'}<input aria-label="重新上传源文件" type="file" accept=".xlsx,.xls,.csv" disabled={fileReuploadDisabled} onChange={event => {
                const input = event.currentTarget
                const file = input.files?.[0]
                input.value = ''
                if (file) void reuploadSourceFile(dialog.source!, file)
              }} /></label>
              : <><label className="data-source-sample-mode"><span>LLM 样本</span><select aria-label="LLM 样本授权" value={sampleDataMode} disabled={actionBusy || metadataTaskBusy} onChange={event => setSampleDataMode(event.target.value as MetadataSampleMode)}><option value="MASK">默认读取 10 行（格式脱敏）</option><option value="DENY">不读取业务行</option><option value="RAW">10 行原始样本（高风险）</option></select></label>
                <label className="data-source-refresh-mode"><span>刷新方式</span><select aria-label="元数据刷新方式" value={refreshMode} disabled={actionBusy || metadataTaskBusy} onChange={event => setRefreshMode(event.target.value as MetadataRefreshMode)}><option value="INCREMENTAL">增量刷新（仅变化字段）</option><option value="FULL">全量刷新（全部重新处理）</option></select></label>
                <AppButton className="action-refresh-all" type="button" disabled={actionBusy || metadataTaskBusy || dialog.source.status !== 'ACTIVE'} onClick={() => void refreshAllTableAssets(dialog.source!)}>{busyAction === `refresh-tables:${dialog.source.id}` ? '正在提交…' : `开始${refreshMode === 'INCREMENTAL' ? '增量' : '全量'}刷新`}</AppButton></>}
          </div>
          <div className="data-source-job-state" role="note">{dialog.source.type === 'EXCEL'
            ? '重新上传会复用当前文件资产并生成不可变新版本；完成后请点击“新增数据表”重新解析并映射 Sheet。已发布数据集继续引用原固定文件版本。'
            : '增量刷新仅调用 LLM 处理新增或结构发生变化的字段，未变化字段保留现有完善结果；源表被删除时停用对应资产。全量刷新会重新处理全部活动表和字段。'}</div>
          {metadataJobLoading && <div className="data-source-job-state" role="status">正在读取后台元数据任务…</div>}
          {visibleMetadataJob && <section className={`data-source-job-progress ${visibleMetadataJob.status.toLowerCase()}`} aria-label="元数据后台任务">
            <header><div><strong>{visibleMetadataJobTitle}</strong><span>{metadataStageLabels[visibleMetadataJob.stage] || visibleMetadataJob.stage || '处理中'}</span></div><em>{metadataProgressLabel}</em></header>
            <progress aria-label="元数据任务进度" aria-valuetext={metadataProgressText} max={metadataProgressMax} value={metadataProgressValue} />
            <div className="data-source-job-counts" role="status" aria-live="polite"><span>已处理 {visibleMetadataJob.completed} / {visibleMetadataJob.total} 张</span><span className="success">成功 {visibleMetadataJob.succeeded}</span><span>跳过 {visibleMetadataJob.skipped}</span><span className={visibleMetadataJob.failed ? 'failed' : ''}>失败 {visibleMetadataJob.failed}</span>{visibleMetadataJob.currentTable && <span className="current">当前：{visibleMetadataJob.currentTable}</span>}</div>
            {(visibleMetadataJob.errorCode || visibleMetadataJob.errorMessage) && <p role="alert">{[visibleMetadataJob.errorCode, visibleMetadataJob.errorMessage].filter(Boolean).join(' · ')}</p>}
            {visibleMetadataJobFailures.length > 0 && <ul className="data-source-job-failures" aria-label="逐表失败明细">
              {visibleMetadataJobFailures.map((failure, index) => {
                const failedTable = tableForMetadataFailure(failure, metadataTables)
                return <li key={`${failure.catalogName || ''}:${failure.schemaName || ''}:${failure.tableName}:${failure.errorCode || ''}:${index}`}>
                  <span><strong>{metadataJobFailureTable(failure)}</strong><span>：{metadataJobFailureMessage(failure)}</span></span>
                  <AppButton type="button" disabled={actionBusy || metadataTaskActive || !failedTable} title={failedTable ? '填写完整表与字段业务元数据并提交为已完善资产' : '正在等待表资产加载'} onClick={() => failedTable && void openTableEditor(dialog.source!, failedTable, 'MANUAL_COMPLETE')}>手工完善</AppButton>
                </li>
              })}
            </ul>}
          </section>}
          {visibleMetadataJob && <section className="data-source-processing-log" aria-label="处理日志">
            <header><div><strong>处理日志</strong><span>仅显示安全运行摘要，不包含业务样本和模型原文</span></div><em>{metadataJobActive(visibleMetadataJob) ? '实时刷新' : `${visibleMetadataJobLogs.length} 条`}</em></header>
            <div className="data-source-processing-log-list" ref={metadataJobLogRef} role="log" aria-live="polite">
              {visibleMetadataJobLogs.length === 0
                ? <div className="data-source-processing-log-empty">任务日志正在初始化…</div>
                : visibleMetadataJobLogs.map((entry, index) => <div className={`data-source-processing-log-entry ${entry.level.toLowerCase()}`} key={`${entry.timestamp}:${entry.stage}:${entry.message}:${index}`}>
                    <time dateTime={entry.timestamp}>{metadataJobLogTime(entry.timestamp)}</time>
                    <i aria-hidden="true" />
                    <span>{entry.tableName && <b>{entry.tableName}</b>}<strong>{entry.message}</strong>{entry.model && <small>{entry.model}</small>}{entry.durationMs ? <small>{metadataJobLogDuration(entry.durationMs)}</small> : null}</span>
                  </div>)}
            </div>
          </section>}
          <section className="data-source-structure" aria-label="表结构">
            <header><div><span className="eyebrow">物理元数据</span><h3>表结构</h3></div><strong>{metadataTables.length}<small> 张表</small></strong></header>
            {metadataLoading ? <div className="data-source-structure-state" role="status">正在加载表结构…</div>
              : metadataError ? <div className="data-source-structure-state error" role="alert">{metadataError}<AppButton type="button" onClick={() => void loadTableStructures(dialog.source!.id)}>重新加载</AppButton></div>
              : metadataTables.length === 0 ? <div className="data-source-structure-state">暂无物理表结构，请点击“新增数据表”从源库选择。</div>
              : <div className="data-source-table-list">{metadataTables.map(table => <details key={table.id} onToggle={event => { if (event.currentTarget.open) void loadColumns(table) }}>
                  <summary><span><strong>{table.tableName}</strong><small>{[table.catalogName, table.schemaName, table.tableName].filter(Boolean).join('.')}</small></span><span><em className={`table-visibility-status ${table.visibility.toLowerCase()}`}>{table.visibility === 'TENANT_PUBLIC' ? '领域可见' : '仅授权可见'}</em><em className={`table-management-status ${table.managementStatus.toLowerCase()}`}>{table.managementStatus === 'DISABLED' ? '已停用' : '可用'}</em>{table.tableType || 'TABLE'} · {table.columnCount} 字段</span></summary>
                  <div className="data-source-table-actions" aria-label={`${table.tableName}操作`}>
                    <AppButton className="action-edit" type="button" disabled={actionBusy || metadataTaskBusy} title={table.enrichmentStatus === 'SUCCEEDED' ? '修改业务元数据' : '补齐表和字段业务元数据并标记为已完善'} onClick={() => void openTableEditor(dialog.source!, table, table.enrichmentStatus === 'SUCCEEDED' ? 'EDIT' : 'MANUAL_COMPLETE')}>{table.enrichmentStatus === 'SUCCEEDED' ? '修改' : '手工完善'}</AppButton>
                    {dialog.source!.type !== 'EXCEL' && <AppButton className="action-refresh" type="button" disabled={actionBusy || metadataTaskBusy || dialog.source!.status !== 'ACTIVE'} onClick={() => void refreshTableAsset(dialog.source!, table)}>{busyAction === `refresh-table:${table.id}` ? '正在提交…' : '刷新结构'}</AppButton>}
                    <AppButton className={table.managementStatus === 'DISABLED' ? 'action-resume' : 'action-pause'} type="button" disabled={actionBusy} onClick={() => void changeTableStatus(dialog.source!, table)}>{table.managementStatus === 'DISABLED' ? '恢复' : '停用'}</AppButton>
                    <AppButton variant="danger" plain className="action-delete" type="button" disabled={actionBusy} onClick={() => { setFormError(''); setDialog({ mode: 'delete-table', source: dialog.source!, table }) }}>删除</AppButton>
                  </div>
                  {columnLoading[table.id] ? <div className="data-source-column-state">正在加载字段…</div>
                    : metadataColumns[table.id] ? <div className="data-source-column-scroll"><table><thead><tr><th>#</th><th>物理字段</th><th>原始类型</th><th>标准类型</th><th>可空</th><th>状态</th></tr></thead><tbody>{metadataColumns[table.id].map(column => <tr key={column.id}><td>{column.ordinalPosition}</td><td><strong>{column.columnName}</strong></td><td>{column.nativeType || '—'}</td><td>{column.canonicalType || '—'}</td><td>{column.nullable ? '是' : '否'}</td><td>{column.assetStatus === 'ACTIVE' ? '可见' : '不可见'}</td></tr>)}</tbody></table></div>
                    : null}
                </details>)}</div>}
          </section>
          <footer><AppButton className="quiet-button" type="button" onClick={closeDialog}>关闭</AppButton></footer>
        </div>
      </Dialog>}

      {dialog?.mode === 'select-tables' && dialog.source && <Dialog title="新增数据表" wide onClose={closeDialog}>
        <div className="data-source-table-picker">
          <header><div><strong>{dialog.source.name}</strong><span>{dialog.source.type === 'EXCEL' ? '解析并选择需要映射的 Sheet' : '从源库选择需要完善并纳入管理的数据表'}</span></div><small>LLM 默认读取最多 10 行格式脱敏样本辅助识别；可按本次任务改为不读取或使用原始样本，所有选择均写入审计并受平台策略约束。</small></header>
          {formError && <div className="data-source-feedback error" role="alert">{formError}</div>}
          {discoveryLoading ? <div className="data-source-structure-state" role="status">{dialog.source.type === 'EXCEL' ? '正在解析 Sheet 前 10 行并生成预览…' : '正在从数据源刷新表清单…'}</div> : <>
            {dialog.source.type === 'EXCEL' && fileInspection && <div className="file-inspection-summary" role="status"><strong>Sheet 结构解析完成</strong><span>{fileInspection.sheets.length} 个 Sheet · 每个最多预览 {fileInspection.sampleLimit} 行</span></div>}
            <label className="data-source-task-sample-mode"><span><strong>本次 LLM 样本授权</strong><small>{metadataSampleDescription(sampleDataMode)}。管理员在执行前撤权时任务会安全终止。</small></span><select aria-label="本次 LLM 样本授权" value={sampleDataMode} disabled={actionBusy} onChange={event => setSampleDataMode(event.target.value as MetadataSampleMode)}><option value="MASK">MASK · 默认读取 10 行格式脱敏样本</option><option value="DENY">DENY · 仅技术元数据</option><option value="RAW">RAW · 10 行原始样本（高风险）</option></select></label>
            <div className="data-source-table-picker-toolbar">
              <label><input type="checkbox" checked={selectableDiscoveredTables.length > 0 && selectedTableKeys.length === selectableDiscoveredTables.length} onChange={event => {
                if (event.target.checked) setSelectedTableKeys(selectableDiscoveredTables.map(discoveredTableKey))
                else setSelectedTableKeys([])
              }} />{dialog.source.type === 'EXCEL' ? '全选可映射 Sheet' : '全选可新增表'}</label><span>已选择 {selectedTableKeys.length} / {discoveredTables.length}</span>
            </div>
            {dialog.source.type === 'EXCEL' && fileInspection ? <div className="file-sheet-selection-list">{fileInspection.sheets.map(sheet => {
              const table = discoveredTables.find(item => item.name === sheet.name && item.schemaName === 'WORKBOOK')
              if (!table) return null
              const key = discoveredTableKey(table)
              const imported = metadataTables.some(asset => assetTableKey(asset) === key)
              return <article className={`file-sheet-selection-card${imported ? ' imported' : ''}`} key={key}>
                <label><input type="checkbox" disabled={actionBusy} checked={selectedTableKeys.includes(key)} onChange={() => setSelectedTableKeys(current => current.includes(key) ? current.filter(item => item !== key) : [...current, key])} /><span><strong>{sheet.name}</strong><small>表头第 {sheet.headerRow} 行 · {sheet.columns.length} 字段 · {sheet.skipEmptyRows ? '跳过空行' : '保留空行'}</small></span><em>{imported ? '可重新映射' : '待映射'}</em></label>
                <div className="excel-sheet-preview"><table><thead><tr>{sheet.columns.map(column => <th key={column.name}><strong>{column.name}</strong><small>{column.canonicalType}{column.nullable ? ' · 可空' : ''}</small></th>)}</tr></thead><tbody>{sheet.rows.map((row, rowIndex) => <tr key={rowIndex}>{sheet.columns.map((column, columnIndex) => <td key={`${column.name}:${columnIndex}`}>{row[columnIndex] || '—'}</td>)}</tr>)}</tbody></table></div>
              </article>
            })}</div> : <div className="data-source-discovery-list">{discoveredTables.map(table => {
              const key = discoveredTableKey(table)
              const importedAsset = metadataTables.find(asset => assetTableKey(asset) === key)
              const imported = importedAsset?.managementStatus !== 'DISABLED' && Boolean(importedAsset)
              const canRemanage = importedAsset?.managementStatus === 'DISABLED'
              return <label className={imported ? 'imported' : ''} key={key}><input type="checkbox" disabled={imported || actionBusy} checked={selectedTableKeys.includes(key)} onChange={() => setSelectedTableKeys(current => current.includes(key) ? current.filter(item => item !== key) : [...current, key])} /><span><strong>{table.name}</strong><small>{[table.catalogName, table.schemaName, table.name].filter(Boolean).join('.')} · {table.columns.length} 字段</small></span><em>{imported ? '已入库' : canRemanage ? '可重新纳管' : table.type}</em></label>
            })}</div>}
          </>}
          <footer><AppButton className="quiet-button" type="button" disabled={actionBusy} onClick={() => returnToTableAssets(dialog.source!)}>取消</AppButton><AppButton variant="primary" className="primary-button" type="button" disabled={actionBusy || discoveryLoading || selectedTableKeys.length === 0} onClick={() => void importSelectedTables()}>{actionBusy ? '正在提交完善任务…' : dialog.source.type === 'EXCEL' ? `提交 ${selectedTableKeys.length} 个 Sheet 映射` : `新增 ${selectedTableKeys.length} 张表`}</AppButton></footer>
        </div>
      </Dialog>}

      {dialog?.mode === 'edit-table' && dialog.source && dialog.table && <Dialog title={tableEditorPurpose === 'MANUAL_COMPLETE' ? '手工完善数据表' : '修改数据表资产'} wide onClose={closeDialog}>
        <form className="data-source-form" onSubmit={event => { event.preventDefault(); void saveTableAsset(tableEditorPurpose === 'MANUAL_COMPLETE') }}>
          {tableEditorPurpose === 'MANUAL_COMPLETE' && <div className="data-source-manual-completion-note" role="note"><strong>补齐 LLM 未完成的业务元数据</strong><span>表和每个活动字段都必须填写业务名称、业务说明和至少一个标签；字段还必须选择语义类型。提交完成后系统会锁定人工结果、标记当前结构已完善，并按源表粒度生成或刷新对应 ODS/DWS 数据集。</span></div>}
          <label>业务名称<input value={tableDraft.businessName} onChange={event => setTableDraft(current => ({ ...current, businessName: event.target.value }))} /></label>
          <label>业务说明<textarea rows={4} value={tableDraft.businessDescription} onChange={event => setTableDraft(current => ({ ...current, businessDescription: event.target.value }))} /></label>
          <label>标签<input value={tableDraft.tags} onChange={event => setTableDraft(current => ({ ...current, tags: event.target.value }))} placeholder="多个标签使用英文逗号分隔" /></label>
          <div className="data-source-form-grid"><label>敏感级别<select value={tableDraft.sensitivityLevel} onChange={event => setTableDraft(current => ({ ...current, sensitivityLevel: event.target.value }))}><option value="PUBLIC">公开</option><option value="INTERNAL">内部</option><option value="CONFIDENTIAL">机密</option><option value="RESTRICTED">严格限制</option></select></label><label>可见范围<select value={tableDraft.visibility} onChange={event => setTableDraft(current => ({ ...current, visibility: event.target.value }))}><option value="PRIVATE">私有</option><option value="TENANT_PUBLIC">领域公开</option></select></label></div>
          <label className="data-source-checkbox"><input type="checkbox" checked={tableDraft.manualLocked} onChange={event => setTableDraft(current => ({ ...current, manualLocked: event.target.checked }))} />锁定人工修改，后续 LLM 刷新不自动覆盖</label>
          <section className="data-source-field-mapping" aria-label="字段映射">
            <header><div><span className="eyebrow">字段映射</span><strong>源字段与业务字段</strong><small id="field-tag-contract">人工编辑支持自由标签；LLM 自动完善只使用受控词表。多个标签用英文逗号分隔，保存时自动去空、去重（最多 30 个，每个不超过 50 字符）。</small></div><small>{columnDrafts.length} 个字段</small></header>
            {tableEditorLoading ? <div className="data-source-column-state" role="status">正在加载字段映射…</div>
              : columnDrafts.length === 0 ? <div className="data-source-column-state">暂无可修改字段</div>
              : <div className="data-source-field-mapping-scroll"><table><thead><tr><th>源字段</th><th>技术类型</th><th>业务字段名称</th><th>业务说明</th><th>标签</th><th>语义类型</th><th>敏感级别</th><th>人工锁定</th></tr></thead><tbody>{columnDrafts.map(column => <tr key={column.original.id}>
                  <td><strong>{column.original.columnName}</strong><small>#{column.original.ordinalPosition}</small></td>
                  <td><strong>{column.original.nativeType || '—'}</strong><small>{column.original.canonicalType || '—'}</small></td>
                  <td><input aria-label={`${column.original.columnName}业务字段名称`} value={column.businessName} onChange={event => updateColumnDraft(column.original.id, { businessName: event.target.value })} /></td>
                  <td><textarea aria-label={`${column.original.columnName}业务说明`} rows={2} value={column.businessDescription} onChange={event => updateColumnDraft(column.original.id, { businessDescription: event.target.value })} /></td>
                  <td><input aria-label={`${column.original.columnName}标签`} aria-describedby="field-tag-contract" value={column.tags} onChange={event => updateColumnDraft(column.original.id, { tags: event.target.value })} placeholder="英文逗号分隔" /></td>
                  <td><select aria-label={`${column.original.columnName}语义类型`} value={column.semanticType} onChange={event => updateColumnDraft(column.original.id, { semanticType: event.target.value })}><option value="">未设置</option>{semanticTypes.map(value => <option value={value} key={value}>{value}</option>)}</select></td>
                  <td><select aria-label={`${column.original.columnName}敏感级别`} value={column.sensitivityLevel} onChange={event => updateColumnDraft(column.original.id, { sensitivityLevel: event.target.value as DataSourceColumnRecord['sensitivityLevel'] })}><option value="PUBLIC">公开</option><option value="INTERNAL">内部</option><option value="CONFIDENTIAL">机密</option><option value="RESTRICTED">严格限制</option></select></td>
                  <td><input aria-label={`${column.original.columnName}人工锁定`} type="checkbox" checked={column.manualLocked} onChange={event => updateColumnDraft(column.original.id, { manualLocked: event.target.checked })} /></td>
                </tr>)}</tbody></table></div>}
          </section>
          {formError && <div className="data-source-feedback error" role="alert">{formError}</div>}
          <footer><AppButton className="quiet-button" type="button" disabled={actionBusy} onClick={() => returnToTableAssets(dialog.source!)}>取消</AppButton>{tableEditorPurpose === 'MANUAL_COMPLETE' && <AppButton className="quiet-button" type="button" disabled={actionBusy || tableEditorLoading} onClick={() => void saveTableAsset(false)}>{actionBusy ? '正在保存…' : '仅保存，稍后完成'}</AppButton>}<AppButton variant="primary" className="primary-button" type="submit" disabled={actionBusy || tableEditorLoading}>{actionBusy ? tableEditorPurpose === 'MANUAL_COMPLETE' ? '正在校验并完成…' : '正在保存…' : tableEditorPurpose === 'MANUAL_COMPLETE' ? '保存并完成手工完善' : '保存修改'}</AppButton></footer>
        </form>
      </Dialog>}

      {dialog?.mode === 'delete-table' && dialog.source && dialog.table && <Dialog title="删除数据表资产" onClose={closeDialog}>
        <div className="data-source-delete"><p>确认从 PostgreSQL 删除表资产“<strong>{dialog.table.businessName || dialog.table.tableName}</strong>”吗？</p><p className="data-source-safe-note">该操作不会删除或修改源数据库中的原表，之后仍可通过“新增数据表”重新纳入。</p>{formError && <div className="data-source-feedback error" role="alert">{formError}</div>}<footer><AppButton className="quiet-button" type="button" disabled={actionBusy} onClick={() => { setDialog({ mode: 'view', source: dialog.source }); void loadTableStructures(dialog.source!.id) }}>取消</AppButton><AppButton variant="danger" className="data-source-delete-button" type="button" disabled={actionBusy} onClick={() => void deleteTableAsset()}>{actionBusy ? '正在删除…' : '确认删除资产'}</AppButton></footer></div>
      </Dialog>}

      {dialog?.mode === 'delete' && dialog.source && <Dialog title="删除数据源" onClose={closeDialog}>
        <div className="data-source-delete"><p>确认删除“<strong>{dialog.source.name}</strong>”吗？该操作会关闭连接池并从数据源清单移除。</p>{formError && <div className="data-source-feedback error" role="alert">{formError}</div>}<footer><AppButton className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</AppButton><AppButton variant="danger" className="data-source-delete-button" type="button" disabled={actionBusy} onClick={() => void deleteSource()}>{actionBusy ? '正在删除…' : '确认删除'}</AppButton></footer></div>
      </Dialog>}
      <DataSourceAIAssistant
        sources={sources}
        open={assistantOpen}
        onOpenChange={setAssistantOpen}
        hideLauncher
        onSourceChanged={source => {
          setSources(current => {
            const exists = current.some(item => item.id === source.id)
            return exists ? current.map(item => item.id === source.id ? source : item) : [source, ...current]
          })
          setDetailDismissed(false)
          setSelectedSourceId(source.id)
        }}
        onReload={loadSources}
        onNotice={setNotice}
      />
    </AppShell>
  )
}

function Dialog({ title, children, wide = false, onClose }: { title: string; children: ReactNode; wide?: boolean; onClose: () => void }) {
  return <div className="data-source-dialog-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}><section className={`data-source-dialog${wide ? ' wide' : ''}`} role="dialog" aria-modal="true" aria-labelledby="data-source-dialog-title"><header><div><span className="eyebrow">数据源配置</span><h2 id="data-source-dialog-title">{title}</h2></div><AppButton text circle aria-label={`关闭${title}`} onClick={onClose}><X size={18} /></AppButton></header>{children}</section></div>
}
