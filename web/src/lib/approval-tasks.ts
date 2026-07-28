import { dataSourceAPI, type DataSourceRecord } from './data-sources'
import {
  datasetAPI,
  type DatasetPublicationRequest,
  type DatasetSummary,
} from './datasets'

export type ApprovalCategory = 'DATA_SOURCE' | 'DATASET'

export type DataSourceApprovalTask = {
  key: string
  category: 'DATA_SOURCE'
  name: string
  subtitle: string
  submittedAt: string
  submittedByCurrentUser: boolean
  source: DataSourceRecord
}

export type DatasetApprovalTask = {
  key: string
  category: 'DATASET'
  name: string
  subtitle: string
  submittedAt: string
  submittedByCurrentUser: boolean
  dataset: DatasetSummary
  request: DatasetPublicationRequest
}

export type ApprovalTask = DataSourceApprovalTask | DatasetApprovalTask

export type ApprovalTaskBatch = {
  category: ApprovalCategory
  tasks: ApprovalTask[]
  assetCount?: number
}

const pageSize = 200

async function loadAllDatasets(): Promise<DatasetSummary[]> {
  const items: DatasetSummary[] = []
  for (let offset = 0; ;) {
    const page = await datasetAPI.list(pageSize, offset)
    const pageItems = Array.isArray(page.items) ? page.items : []
    items.push(...pageItems)
    if (!pageItems.length || items.length >= page.total) return items
    offset += pageItems.length
  }
}

async function loadAllDatasetRequests(datasetID: string): Promise<DatasetPublicationRequest[]> {
  const items: DatasetPublicationRequest[] = []
  for (let offset = 0; ;) {
    const page = await datasetAPI.listPublicationRequests(datasetID, pageSize, offset)
    const pageItems = Array.isArray(page.items) ? page.items : []
    items.push(...pageItems)
    if (!pageItems.length || items.length >= page.total) return items
    offset += pageItems.length
  }
}

/** 加载当前用户具备发布审批权限的数据源申请；本人申请保留展示并由后端权限最终裁决。 */
export async function loadDataSourceApprovalTasks(subject?: string): Promise<ApprovalTaskBatch> {
  const page = await dataSourceAPI.list()
  const sources = Array.isArray(page.items) ? page.items : []
  const pending = sources.filter(source => source.reviewStatus === 'PENDING'
    && Boolean(source.reviewRequestId)
    && Boolean(source.reviewRequestVersion))
  const permitted = await Promise.all(pending.map(async source =>
    (await dataSourceAPI.evaluatePermission(source.id, 'PUBLISH')).allowed ? source : null))
  const tasks = permitted
    .filter((source): source is DataSourceRecord => Boolean(source))
    .map<DataSourceApprovalTask>(source => ({
      key: `DATA_SOURCE:${source.reviewRequestId}`,
      category: 'DATA_SOURCE',
      name: source.name,
      subtitle: `${source.type} · ${source.code}`,
      submittedAt: source.reviewSubmittedAt ?? source.updatedAt ?? '',
      submittedByCurrentUser: Boolean(subject && source.reviewRequesterId === subject),
      source,
    }))
  return { category: 'DATA_SOURCE', tasks, assetCount: sources.length }
}

/** 加载当前用户可审批的数据集发布申请，并固定到申请中的精确草稿版本。 */
export async function loadDatasetApprovalTasks(subject?: string): Promise<ApprovalTaskBatch> {
  const datasets = await loadAllDatasets()
  const permissions = await Promise.all(
    datasets.map(dataset => datasetAPI.evaluatePermission(dataset.id, 'PUBLISH')),
  )
  const requests = await Promise.all(datasets.map((dataset, index) =>
    permissions[index].allowed ? loadAllDatasetRequests(dataset.id) : Promise.resolve([])))
  const tasks = datasets.flatMap((dataset, index) => requests[index]
    .filter(request => request.status === 'PENDING')
    .map<DatasetApprovalTask>(request => ({
      key: `DATASET:${request.id}`,
      category: 'DATASET',
      name: dataset.name,
      subtitle: `${dataset.type} · ${dataset.code}`,
      submittedAt: request.submittedAt,
      submittedByCurrentUser: Boolean(subject && request.requesterId === subject),
      dataset,
      request,
    })))
  return { category: 'DATASET', tasks, assetCount: datasets.length }
}
