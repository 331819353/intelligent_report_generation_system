import { apiRequest } from './api'

export type DataSourceType = 'MYSQL' | 'ORACLE' | 'EXCEL'
export type DataSourceStatus = 'DRAFT' | 'ACTIVE' | 'DISABLED' | 'SYNCING' | 'ERROR' | 'DELETING'

export type DataSourceRecord = {
  id: string
  tenantId: string
  code: string
  name: string
  type: DataSourceType
  status: DataSourceStatus
  config: Record<string, unknown>
  fileAssetId?: string
  version: number
}

export type DataSourceConnectionInput = {
  code: string
  name: string
  type: Exclude<DataSourceType, 'EXCEL'>
  host: string
  port: number
  database: string
  username: string
  password: string
  config?: Record<string, unknown>
}

export type DataSourceTestResult = {
  serverVersion: string
  latencyMs: number
}

export type DataSourceTableRecord = {
  id: string
  dataSourceId: string
  catalogName: string
  schemaName: string
  tableName: string
  tableType: string
  businessName: string
  businessDescription: string
  tags: string[]
  sensitivityLevel: 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED'
  visibility: 'PRIVATE' | 'TENANT_PUBLIC'
  manualLocked: boolean
  businessVersion: number
  managementStatus: 'ENABLED' | 'DISABLED'
  enrichmentStatus: 'PENDING' | 'RUNNING' | 'SUCCEEDED' | 'FAILED'
  columnCount: number
  metadataVersion: number
  lastSyncAt: string
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

export type DataSourceColumnRecord = {
  id: string
  tableId: string
  columnName: string
  ordinalPosition: number
  nativeType: string
  canonicalType: string
  nullable: boolean
  businessName: string
  assetStatus: string
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
    const query = new URLSearchParams({ dataSourceId, status: 'ACTIVE', enrichedOnly: 'true', limit: String(limit), offset: String(items.length) })
    const page = await apiRequest<DataSourceTablePage>(`/v1/assets/tables?${query}`, { cache: 'no-store' })
    items.push(...page.items)
    total = page.total
    if (page.items.length === 0) break
  } while (items.length < total)
  return { items, total }
}

export const dataSourceAPI = {
  // 数据源状态会被测试、同步等后台操作改变，目录读取不复用缓存。
  list: () => apiRequest<{ items: DataSourceRecord[] }>('/v1/data-sources', { cache: 'no-store' }),
  get: (id: string) => apiRequest<DataSourceRecord>(`/v1/data-sources/${encodeURIComponent(id)}`, { cache: 'no-store' }),
  create: (input: DataSourceConnectionInput) => apiRequest<DataSourceRecord>('/v1/data-sources', {
    method: 'POST',
    body: JSON.stringify(input),
  }),
  update: (id: string, input: DataSourceConnectionInput) => apiRequest<DataSourceRecord>(`/v1/data-sources/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  }),
  test: (id: string) => apiRequest<DataSourceTestResult>(`/v1/data-sources/${encodeURIComponent(id)}/test`, { method: 'POST', body: '{}' }),
  sync: (id: string) => apiRequest<{ assets: number; snapshotHash: string }>(`/v1/data-sources/${encodeURIComponent(id)}/sync`, { method: 'POST', body: '{}' }),
  discoverTables: (id: string) => apiRequest<{ items: DiscoveredTableRecord[]; total: number }>(`/v1/data-sources/${encodeURIComponent(id)}/tables/discovery`, { cache: 'no-store' }),
  importTables: (id: string, tables: Array<{ catalogName: string; schemaName: string; tableName: string }>) => apiRequest<{ items: Array<{ id: string }>; total: number }>(`/v1/data-sources/${encodeURIComponent(id)}/tables/import`, { method: 'POST', body: JSON.stringify({ tables }) }),
  tables: listAllTables,
  columns: (tableId: string) => apiRequest<{ items: DataSourceColumnRecord[] }>(`/v1/assets/tables/${encodeURIComponent(tableId)}/columns`, { cache: 'no-store' }),
  updateTable: (tableId: string, input: { businessName: string; businessDescription: string; tags: string[]; sensitivityLevel: string; visibility: string; manualLocked: boolean; expectedVersion: number }) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}/business-metadata`, { method: 'PUT', body: JSON.stringify(input) }),
  disableTable: (tableId: string) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}/disable`, { method: 'POST', body: '{}' }),
  enableTable: (tableId: string) => apiRequest<DataSourceTableRecord>(`/v1/assets/tables/${encodeURIComponent(tableId)}/enable`, { method: 'POST', body: '{}' }),
  deleteTable: (tableId: string) => apiRequest<void>(`/v1/assets/tables/${encodeURIComponent(tableId)}`, { method: 'DELETE' }),
  disable: (id: string) => apiRequest<void>(`/v1/data-sources/${encodeURIComponent(id)}/disable`, { method: 'POST', body: '{}' }),
  enable: (id: string) => apiRequest<void>(`/v1/data-sources/${encodeURIComponent(id)}/enable`, { method: 'POST', body: '{}' }),
  delete: (id: string) => apiRequest<void>(`/v1/data-sources/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}
