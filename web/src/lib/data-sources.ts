import { apiRequest, RequestError } from './api'

export type DatabaseSourceType = 'MYSQL' | 'MARIADB' | 'POSTGRESQL' | 'ORACLE' | 'SQLSERVER' | 'CLICKHOUSE'
export type DataSourceType = DatabaseSourceType | 'EXCEL'

export const databaseDriverDefinitions: ReadonlyArray<{ type: DatabaseSourceType; label: string; defaultPort: number }> = [
  { type: 'MYSQL', label: 'MySQL', defaultPort: 3306 },
  { type: 'MARIADB', label: 'MariaDB', defaultPort: 3306 },
  { type: 'POSTGRESQL', label: 'PostgreSQL', defaultPort: 5432 },
  { type: 'ORACLE', label: 'Oracle', defaultPort: 1521 },
  { type: 'SQLSERVER', label: 'SQL Server', defaultPort: 1433 },
  { type: 'CLICKHOUSE', label: 'ClickHouse', defaultPort: 8123 },
]

export const dataSourceTypeLabels: Record<DataSourceType, string> = Object.fromEntries([
  ...databaseDriverDefinitions.map(driver => [driver.type, driver.label]),
  ['EXCEL', 'Excel / CSV'],
]) as Record<DataSourceType, string>

export const defaultDatabasePort = (type: DataSourceType | '') =>
  databaseDriverDefinitions.find(driver => driver.type === type)?.defaultPort ?? 0
export type DataSourceStatus = 'DRAFT' | 'ACTIVE' | 'DISABLED' | 'SYNCING' | 'ERROR' | 'DELETING'
export type DataSourceValidationStatus = 'UNTESTED' | 'PASSED' | 'FAILED'
export type DataSourcePublicationStatus = 'UNPUBLISHED' | 'PUBLISHED'
export type DataSourceVisibility = 'PRIVATE' | 'TENANT_PUBLIC'
export type AssetSharingScope = 'PRIVATE' | 'DOMAIN'
export type DataSourceReviewStatus = 'NOT_SUBMITTED' | 'PENDING' | 'APPROVED' | 'REJECTED' | 'WITHDRAWN'

export type DataSourceRecord = {
  id: string
  tenantId: string
  code: string
  name: string
  description?: string
  ownerId?: string
  visibility?: DataSourceVisibility
  domainId?: string
  sharingScope?: AssetSharingScope
  type: DataSourceType
  status: DataSourceStatus
  config: Record<string, unknown>
  fileAssetId?: string
  fileVersionId?: string
  configVersionId?: string
  publishedVersionId?: string
  configVersion?: number
  publishedConfigVersion?: number
  configHash?: string
  validationStatus?: DataSourceValidationStatus
  publicationStatus?: DataSourcePublicationStatus
  hasUnpublishedChanges?: boolean
  lastTestedAt?: string
  reviewStatus?: DataSourceReviewStatus
  reviewRequestId?: string
  reviewRequestVersion?: number
  reviewNote?: string
  reviewRequesterId?: string
  reviewReviewerId?: string
  reviewSubmittedAt?: string
  reviewReviewedAt?: string
  createdBy?: string
  updatedBy?: string
  createdAt?: string
  updatedAt?: string
  version: number
}

export type DataSourceRetirementDataset = {
  id: string
  name: string
  status: 'DRAFT' | 'VALIDATING' | 'PUBLISHED' | 'STALE' | 'DEPRECATED'
  layer?: string
  dependencyKind: 'ORIGIN_TABLE' | 'VERSION_DEPENDENCY'
}

export type DataSourceRetirementImpact = {
  canRetire: boolean
  blockingDatasetCount: number
  credentialWillBeRevoked: boolean
  sourceFileWillBeRetained: boolean
  datasets: DataSourceRetirementDataset[]
}

export type DataSourcePublicationRequest = {
  id: string
  dataSourceId: string
  configVersionId: string
  configHash: string
  status: Exclude<DataSourceReviewStatus, 'NOT_SUBMITTED'>
  version: number
  requesterUserId: string
  requestNote: string
  reviewerUserId?: string
  reviewNote?: string
  submittedAt: string
  reviewedAt?: string
  updatedAt: string
  publishedVersionId?: string
}

export type DataSourceConnectionInput = {
  code: string
  name: string
  description?: string
  visibility?: DataSourceVisibility
  sharingScope?: AssetSharingScope
  ownerId?: string
  type: Exclude<DataSourceType, 'EXCEL'>
  host: string
  port: number
  database: string
  oracleConnectMode?: 'SERVICE_NAME' | 'SID'
  username: string
  password: string
  config?: Record<string, unknown>
}

export type ExcelDataSourceInput = {
  code: string
  name: string
  description?: string
  visibility?: DataSourceVisibility
  sharingScope?: AssetSharingScope
  ownerId?: string
  type: 'EXCEL'
  fileAssetId: string
}

export type DataSourceInput = DataSourceConnectionInput | ExcelDataSourceInput
export type DataSourceUpdateInput = DataSourceInput & { expectedVersion: number }

export type ExcelColumnInspection = { name: string; canonicalType: string; nullable: boolean }
export type ExcelSheetInspection = {
  name: string
  headerRow: number
  skipEmptyRows: boolean
  columns: ExcelColumnInspection[]
  rows: string[][]
}
export type ExcelWorkbookInspection = { sampleLimit: number; sheets: ExcelSheetInspection[] }
export type ExcelFileAsset = {
  id: string
  filename: string
  version: number
  versionId: string
  sizeBytes: number
  sha256: string
  workbookSummary: Record<string, unknown>
  inspection?: ExcelWorkbookInspection
}

export type DataSourceTestResult = {
  serverVersion: string
  latencyMs: number
  configVersionId?: string
  testedAt?: string
}

export class DataSourceConnectionTestError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = 'DataSourceConnectionTestError'
    this.code = code
  }
}

export type DataSourceAIMessage = { role: 'user' | 'assistant'; content: string }
export type DataSourceAIDraft = {
  code: string
  name: string
  description: string
  type: DataSourceType | ''
  host: string
  port: number
  database: string
  oracleConnectMode: 'SERVICE_NAME' | 'SID'
  username: string
  visibility: DataSourceVisibility
  sharingScope: AssetSharingScope
}
export type DataSourceAITestFailure = { code: string; message: string }
export type DataSourceAITurnInput = {
  instruction: string
  history: DataSourceAIMessage[]
  draft: DataSourceAIDraft
  passwordProvided: boolean
  fileProvided: boolean
  testFailure?: DataSourceAITestFailure
}
export type DataSourceAITurnResult = {
  reply: string
  draft: DataSourceAIDraft
  missingFields: string[]
  readyToTest: boolean
  suggestedAction: 'ASK' | 'TEST' | 'WAIT'
  diagnosis: string
  suggestedChecks: string[]
  autoFixes: string[]
  autoRetry: boolean
  requestId: string
}

export type ConnectionTestJobStatus = 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'FAILED' | 'CANCELLED'
export type ConnectionTestStage = 'QUEUED' | 'ADDRESS' | 'PORT' | 'DATABASE' | 'AUTHENTICATION'
export type ConnectionTestJob = {
  id: string
  dataSourceId: string
  configVersionId: string
  status: ConnectionTestJobStatus
  stage: ConnectionTestStage
  attempt: number
  maxAttempts: number
  errorCode?: string
  errorMessage?: string
  serverVersion?: string
  latencyMs?: number
  requestedAt: string
  startedAt?: string
  completedAt?: string
  testedAt?: string
}

export type DataSourceTableRecord = {
  id: string
  dataSourceId: string
  catalogName: string
  schemaName: string
  tableName: string
  tableType: string
  sourceComment?: string
  businessName: string
  businessDescription: string
  tags: string[]
  sensitivityLevel: 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED'
  visibility: 'PRIVATE' | 'TENANT_PUBLIC'
  assetStatus?: string
  businessVersion: number
  structureHash: string
  managementStatus: 'ENABLED' | 'DISABLED'
  enrichmentStatus: 'PENDING' | 'RUNNING' | 'SUCCEEDED' | 'FAILED'
  columnCount: number
  /** 已锁定人工定义的字段数；锁定是字段级保护，不影响同表其余字段刷新。 */
  lockedColumnCount?: number
  metadataVersion: number
  lastSyncAt: string
  // 以下三项只在该表参与一个尚未结束的元数据任务时返回，用于在整批完成之前
  // 逐表展示真实进度；任务结束后服务端立即回落为空。
  refreshState?: MetadataJobItemStatus
  refreshStage?: MetadataJobStage
  refreshNote?: string
}

export type DataSourceTablePreview = {
  columns: string[]
  rows: unknown[][]
  rowCount: number
}

export type DiscoveredTableRecord = {
  catalogName: string
  schemaName: string
  name: string
  type: string
  sourceComment: string
  estimatedRowCount?: number
  columns: Array<{ name: string; nativeType: string; canonicalType: string; nullable: boolean }>
}

export type MetadataRefreshMode = 'INCREMENTAL' | 'FULL'
export type MetadataSampleMode = 'DENY' | 'MASK' | 'RAW'
export type MetadataJobStatus = 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'PARTIAL' | 'FAILED'
export type MetadataJobStage = 'QUEUED' | 'DISCOVERY' | 'DIFF' | 'SAMPLE' | 'PERSISTENCE' | 'LLM' | 'COMPLETE' | 'FAILED'
export type MetadataJobItemStatus = 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'SKIPPED' | 'FAILED'

export type MetadataJobFailure = {
  catalogName?: string
  schemaName?: string
  tableName: string
  errorCode?: string
  errorMessage?: string
}

export type MetadataJobLog = {
  timestamp: string
  level: 'INFO' | 'SUCCESS' | 'WARN' | 'ERROR'
  stage: MetadataJobStage
  message: string
  tableName?: string
  model?: string
  durationMs?: number
}

export type MetadataJob = {
  id: string
  dataSourceId: string
  kind: 'IMPORT' | 'REFRESH'
  mode: MetadataRefreshMode
  sampleDataMode: MetadataSampleMode
  samplePolicyVersion: number
  status: MetadataJobStatus
  stage: MetadataJobStage
  total: number
  completed: number
  succeeded: number
  skipped: number
  failed: number
  currentTable?: string
  errorCode?: string
  errorMessage?: string
  failures?: MetadataJobFailure[]
  logs?: MetadataJobLog[]
  createdAt: string
  startedAt?: string
  completedAt?: string
}

export type MetadataDiffDependency = {
  id: string
  downstreamType: string
  downstreamId: string
  downstreamName: string
  kind: string
  createdAt: string
}

export type MetadataDiff = {
  id: string
  dataSourceId: string
  objectType: 'TABLE' | 'COLUMN'
  objectKey: string
  changeType: 'ADDED' | 'CHANGED' | 'REMOVED' | 'REACTIVATED'
  before?: Record<string, unknown>
  after?: Record<string, unknown>
  breaking: boolean
  impactCount: number
  staleCount: number
  impact?: MetadataDiffDependency[]
  createdAt: string
}

export type DataSourceColumnRecord = {
  id: string
  tableId: string
  columnName: string
  ordinalPosition: number
  sourceComment: string
  nativeType: string
  canonicalType: string
  nullable: boolean
  businessName: string
  businessDescription: string
  tags: string[]
  sensitivityLevel: 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED'
  semanticType: string
  manualLocked: boolean
  assetStatus: string
  businessVersion: number
}

export type DataSourceColumnBusinessMetadataInput = {
  businessName: string
  businessDescription: string
  tags: string[]
  sensitivityLevel: string
  semanticType: string
  manualLocked: boolean
  expectedVersion: number
}

type DataSourceTablePage = {
  items: DataSourceTableRecord[]
  total: number
  limit: number
  offset: number
}

const listAllTables = async (dataSourceId: string) => {
  const items: DataSourceTableRecord[] = []
  const limit = 200
  let total: number
  do {
    const query = new URLSearchParams({ dataSourceId, status: 'ACTIVE', limit: String(limit), offset: String(items.length) })
    const page = await apiRequest<DataSourceTablePage>(`/v1/assets/tables?${query}`, { cache: 'no-store' })
    items.push(...page.items)
    total = page.total
    if (page.items.length === 0) break
  } while (items.length < total)
  return { items, total }
}

const connectionTestIdempotencyKey = () => {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  return `connection-test-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

type ConnectionTestWaitOptions = {
  signal?: AbortSignal
  onProgress?: (job: ConnectionTestJob) => void
}
type StoredConnectionTest = {
  sourceId: string
  jobId: string
  configVersionId: string
}
const connectionTestStorageKey = 'intelligent-report-connection-test-jobs-v1'

const storedConnectionTests = (): Record<string, StoredConnectionTest> => {
  if (typeof sessionStorage === 'undefined') return {}
  try {
    return JSON.parse(sessionStorage.getItem(connectionTestStorageKey) || '{}') as Record<string, StoredConnectionTest>
  } catch {
    sessionStorage.removeItem(connectionTestStorageKey)
    return {}
  }
}

const saveStoredConnectionTests = (items: Record<string, StoredConnectionTest>) => {
  if (typeof sessionStorage === 'undefined') return
  if (Object.keys(items).length === 0) sessionStorage.removeItem(connectionTestStorageKey)
  else sessionStorage.setItem(connectionTestStorageKey, JSON.stringify(items))
}

const rememberConnectionTest = (job: ConnectionTestJob) => {
  const items = storedConnectionTests()
  if (job.status === 'QUEUED' || job.status === 'RUNNING') {
    items[job.dataSourceId] = {
      sourceId: job.dataSourceId,
      jobId: job.id,
      configVersionId: job.configVersionId,
    }
  } else {
    delete items[job.dataSourceId]
  }
  saveStoredConnectionTests(items)
}

const clearStoredConnectionTest = (sourceId: string) => {
  const items = storedConnectionTests()
  delete items[sourceId]
  saveStoredConnectionTests(items)
}

const queueConnectionTest = async (
  id: string,
  idempotencyKey = connectionTestIdempotencyKey(),
  options: ConnectionTestWaitOptions = {},
) => {
  const job = await apiRequest<ConnectionTestJob>(`/v1/data-sources/${encodeURIComponent(id)}/test`, {
    method: 'POST',
    body: '{}',
    headers: { 'Idempotency-Key': idempotencyKey },
    signal: options.signal,
  })
  rememberConnectionTest(job)
  return job
}

const getConnectionTest = (
  sourceId: string,
  jobId: string,
  options: ConnectionTestWaitOptions = {},
) =>
  apiRequest<ConnectionTestJob>(
    `/v1/data-sources/${encodeURIComponent(sourceId)}/connection-tests/${encodeURIComponent(jobId)}`,
    { cache: 'no-store', signal: options.signal },
  )

const connectionTestResult = (job: ConnectionTestJob): DataSourceTestResult => ({
  serverVersion: job.serverVersion || '',
  latencyMs: job.latencyMs || 0,
  configVersionId: job.configVersionId,
  testedAt: job.testedAt,
})

const waitForConnectionTestDelay = (signal?: AbortSignal) => new Promise<void>((resolve, reject) => {
  if (signal?.aborted) {
    reject(new DOMException('Connection test polling aborted', 'AbortError'))
    return
  }
  const timeout = window.setTimeout(() => {
    signal?.removeEventListener('abort', abort)
    resolve()
  }, 500)
  const abort = () => {
    window.clearTimeout(timeout)
    reject(new DOMException('Connection test polling aborted', 'AbortError'))
  }
  signal?.addEventListener('abort', abort, { once: true })
})

const waitForConnectionTest = async (
  sourceId: string,
  initial: ConnectionTestJob,
  options: ConnectionTestWaitOptions = {},
) => {
  let job = initial
  options.onProgress?.(job)
  while (job.status === 'QUEUED' || job.status === 'RUNNING') {
    rememberConnectionTest(job)
    await waitForConnectionTestDelay(options.signal)
    job = await getConnectionTest(sourceId, job.id, options)
    options.onProgress?.(job)
  }
  rememberConnectionTest(job)
  if (job.status === 'SUCCEEDED') return connectionTestResult(job)
  throw new DataSourceConnectionTestError(job.errorCode || 'CONNECTION_FAILED', job.errorMessage || (job.status === 'CANCELLED'
    ? '数据源配置已变化，请重新测试'
    : '连接测试失败，请检查配置后重试'))
}

const resumeConnectionTest = async (
  sourceId: string,
  options: ConnectionTestWaitOptions = {},
) => {
  const stored = storedConnectionTests()[sourceId]
  if (!stored) return null
  try {
    const job = await getConnectionTest(sourceId, stored.jobId, options)
    return await waitForConnectionTest(sourceId, job, options)
  } catch (cause) {
    if (cause instanceof Error && cause.name === 'AbortError') throw cause
    if (cause instanceof RequestError && cause.status === 404) {
      clearStoredConnectionTest(sourceId)
    }
    // Terminal jobs are cleared by waitForConnectionTest. A transient network
    // error deliberately retains the reference so a reload can resume again.
    throw cause
  }
}

export const dataSourceAPI = {
  // 数据源状态会被测试、同步等后台操作改变，目录读取不复用缓存。
  list: () => apiRequest<{ items: DataSourceRecord[] }>('/v1/data-sources', { cache: 'no-store' }),
  get: (id: string) => apiRequest<DataSourceRecord>(`/v1/data-sources/${encodeURIComponent(id)}`, { cache: 'no-store' }),
  create: (input: DataSourceInput) => apiRequest<DataSourceRecord>('/v1/data-sources', {
    method: 'POST',
    body: JSON.stringify(input),
  }),
  aiTurn: (sourceId: string | null, input: DataSourceAITurnInput) => apiRequest<DataSourceAITurnResult>(
    sourceId
      ? `/v1/data-sources/${encodeURIComponent(sourceId)}/ai/turns`
      : '/v1/data-sources/ai/turns',
    { method: 'POST', body: JSON.stringify(input) },
  ),
  uploadExcel: (file: File) => {
    const body = new FormData()
    body.set('file', file)
    body.set('config', JSON.stringify({ skipEmptyRows: true }))
    return apiRequest<ExcelFileAsset>('/v1/excel-files', { method: 'POST', body })
  },
  uploadExcelVersion: (fileAssetId: string, file: File) => {
    const body = new FormData()
    body.set('file', file)
    body.set('config', JSON.stringify({ skipEmptyRows: true }))
    return apiRequest<ExcelFileAsset>(`/v1/excel-files/${encodeURIComponent(fileAssetId)}/versions`, { method: 'POST', body })
  },
  inspectExcelAsset: (fileAssetId: string) => apiRequest<ExcelWorkbookInspection>(`/v1/excel-files/${encodeURIComponent(fileAssetId)}/inspection`, { method: 'POST', body: '{}' }),
  inspectExcelSource: (id: string) => apiRequest<ExcelWorkbookInspection>(`/v1/data-sources/${encodeURIComponent(id)}/file-inspection`, { method: 'POST', body: '{}' }),
  update: (id: string, input: DataSourceUpdateInput) => apiRequest<DataSourceRecord>(`/v1/data-sources/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  }),
  queueConnectionTest,
  getConnectionTest,
  pendingConnectionTestSourceIds: () => Object.keys(storedConnectionTests()),
  clearStoredConnectionTest,
  resumeConnectionTest,
  test: async (id: string, options: ConnectionTestWaitOptions = {}) => {
    const resumed = await resumeConnectionTest(id, options)
    if (resumed) return resumed
    return waitForConnectionTest(
      id,
      await queueConnectionTest(id, connectionTestIdempotencyKey(), options),
      options,
    )
  },
  publish: (id: string) => apiRequest<DataSourceRecord>(`/v1/data-sources/${encodeURIComponent(id)}/publish`, { method: 'POST', body: '{}' }),
  submitPublicationRequest: (id: string, note = '') => apiRequest<DataSourcePublicationRequest>(`/v1/data-sources/${encodeURIComponent(id)}/publish-requests`, {
    method: 'POST',
    body: JSON.stringify({ note }),
  }),
  publicationRequests: (id: string) => apiRequest<{ items: DataSourcePublicationRequest[]; total: number }>(`/v1/data-sources/${encodeURIComponent(id)}/publish-requests`, { cache: 'no-store' }),
  withdrawPublicationRequest: (id: string, requestId: string, expectedVersion: number) => apiRequest<DataSourcePublicationRequest>(`/v1/data-sources/${encodeURIComponent(id)}/publish-requests/${encodeURIComponent(requestId)}/withdraw`, {
    method: 'POST',
    body: JSON.stringify({ expectedVersion, reason: '申请人撤销' }),
  }),
  approvePublicationRequest: (id: string, requestId: string, expectedVersion: number, reason = '') => apiRequest<{ request: DataSourcePublicationRequest; source: DataSourceRecord }>(`/v1/data-sources/${encodeURIComponent(id)}/publish-requests/${encodeURIComponent(requestId)}/approve`, {
    method: 'POST',
    body: JSON.stringify({ expectedVersion, reason }),
  }),
  rejectPublicationRequest: (id: string, requestId: string, expectedVersion: number, reason: string) => apiRequest<DataSourcePublicationRequest>(`/v1/data-sources/${encodeURIComponent(id)}/publish-requests/${encodeURIComponent(requestId)}/reject`, {
    method: 'POST',
    body: JSON.stringify({ expectedVersion, reason }),
  }),
  evaluatePermission: (id: string, action: 'MANAGE' | 'PUBLISH') => apiRequest<{ allowed: boolean }>('/v1/permissions/evaluate', {
    method: 'POST',
    body: JSON.stringify({ resourceType: 'DATA_SOURCE', action, objectId: id }),
  }),
  sync: (id: string) => apiRequest<{ assets: number; snapshotHash: string }>(`/v1/data-sources/${encodeURIComponent(id)}/sync`, { method: 'POST', body: '{}' }),
  discoverTables: (id: string) => apiRequest<{ items: DiscoveredTableRecord[]; total: number }>(`/v1/data-sources/${encodeURIComponent(id)}/tables/discovery`, { cache: 'no-store' }),
  importTables: (id: string, tables: Array<{ catalogName: string; schemaName: string; tableName: string }>, sampleDataMode: MetadataSampleMode = 'MASK') => apiRequest<MetadataJob>(`/v1/data-sources/${encodeURIComponent(id)}/tables/import`, { method: 'POST', body: JSON.stringify({ tables, sampleDataMode }) }),
  refreshTables: (id: string, mode: MetadataRefreshMode = 'INCREMENTAL', tableIds?: string[], sampleDataMode: MetadataSampleMode = 'MASK') => apiRequest<MetadataJob>(`/v1/data-sources/${encodeURIComponent(id)}/tables/refresh`, { method: 'POST', body: JSON.stringify({ mode, sampleDataMode, ...(tableIds?.length ? { tableIds } : {}) }) }),
  getMetadataJob: (sourceId: string, jobId: string) => apiRequest<MetadataJob>(`/v1/data-sources/${encodeURIComponent(sourceId)}/metadata-jobs/${encodeURIComponent(jobId)}`, { cache: 'no-store' }),
  latestMetadataJob: (sourceId: string) => apiRequest<{ job: MetadataJob | null }>(`/v1/data-sources/${encodeURIComponent(sourceId)}/metadata-jobs/latest`, { cache: 'no-store' }),
  latestActiveMetadataJob: (sourceId: string) => apiRequest<{ job: MetadataJob | null }>(`/v1/data-sources/${encodeURIComponent(sourceId)}/metadata-jobs/latest-active`, { cache: 'no-store' }),
  retryFailedMetadataJob: (sourceId: string, jobId: string) => apiRequest<MetadataJob>(`/v1/data-sources/${encodeURIComponent(sourceId)}/metadata-jobs/${encodeURIComponent(jobId)}/retry`, { method: 'POST', body: '{}' }),
  metadataDiffs: (sourceId: string, limit = 100) => apiRequest<{ items: MetadataDiff[] }>(`/v1/metadata-diffs?dataSourceId=${encodeURIComponent(sourceId)}&limit=${Math.min(500, Math.max(1, limit))}`, { cache: 'no-store' }),
  tables: listAllTables,
  table: (tableId: string) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}`, { cache: 'no-store' }),
  columns: (tableId: string) => apiRequest<{ items: DataSourceColumnRecord[] }>(`/v1/assets/tables/${encodeURIComponent(tableId)}/columns`, { cache: 'no-store' }),
  previewTable: (tableId: string, maxRows = 5) => apiRequest<DataSourceTablePreview>(`/v1/assets/tables/${encodeURIComponent(tableId)}/preview?maxRows=${Math.min(5, Math.max(1, maxRows))}`, { cache: 'no-store' }),
  updateTable: (tableId: string, input: { businessName: string; businessDescription: string; tags: string[]; sensitivityLevel: string; visibility: string; expectedVersion: number }) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}/business-metadata`, { method: 'PUT', body: JSON.stringify(input) }),
  updateColumn: (columnId: string, input: DataSourceColumnBusinessMetadataInput) => apiRequest<DataSourceColumnRecord>(`/v1/assets/columns/${encodeURIComponent(columnId)}/business-metadata`, { method: 'PUT', body: JSON.stringify(input) }),
  completeTableManually: (tableId: string, expectedVersion: number, expectedStructureHash: string) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}/manual-completion`, {
    method: 'POST',
    body: JSON.stringify({ expectedVersion, expectedStructureHash }),
  }),
  disableTable: (tableId: string) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}/disable`, { method: 'POST', body: '{}' }),
  enableTable: (tableId: string) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}/enable`, { method: 'POST', body: '{}' }),
  deleteTable: (tableId: string) => apiRequest<void>(`/v1/assets/tables/${encodeURIComponent(tableId)}`, { method: 'DELETE' }),
  retirementImpact: (id: string) => apiRequest<DataSourceRetirementImpact>(`/v1/data-sources/${encodeURIComponent(id)}/retirement-impact`, { cache: 'no-store' }),
  disable: (id: string) => apiRequest<void>(`/v1/data-sources/${encodeURIComponent(id)}/disable`, { method: 'POST', body: '{}' }),
  enable: (id: string) => apiRequest<void>(`/v1/data-sources/${encodeURIComponent(id)}/enable`, { method: 'POST', body: '{}' }),
  delete: (id: string) => apiRequest<void>(`/v1/data-sources/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}
