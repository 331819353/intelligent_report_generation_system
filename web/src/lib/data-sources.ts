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
  secretRef?: string
  fileAssetId?: string
  version: number
}

export type CreateDataSourceInput = {
  code: string
  name: string
  type: Exclude<DataSourceType, 'EXCEL'>
  config: Record<string, unknown>
  secretRef: string
}

export const dataSourceAPI = {
  // 数据源状态会被测试、同步等后台操作改变，目录读取不复用缓存。
  list: () => apiRequest<{ items: DataSourceRecord[] }>('/v1/data-sources', { cache: 'no-store' }),
  create: (input: CreateDataSourceInput) => apiRequest<DataSourceRecord>('/v1/data-sources', {
    method: 'POST',
    body: JSON.stringify(input),
  }),
}
