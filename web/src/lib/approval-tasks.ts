import { dataSourceAPI, type DataSourceRecord } from './data-sources'
import {
  datasetAPI,
  type DatasetPublicationRequest,
  type DatasetSummary,
} from './datasets'
import { metricCandidateAPI, type MetricCandidate } from './metric-candidates'
import { metricAPI } from './metrics'
import {
  semanticGovernanceAPI,
  type DimensionSurveyCandidate,
} from './semantic-governance'

export type ApprovalCategory = 'DATA_SOURCE' | 'DATASET' | 'METRIC' | 'DIMENSION'

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

export type MetricApprovalTask = {
  key: string
  category: 'METRIC'
  name: string
  subtitle: string
  submittedAt: string
  candidate: MetricCandidate
}

export type DimensionApprovalTask = {
  key: string
  category: 'DIMENSION'
  name: string
  subtitle: string
  submittedAt: string
  candidate: DimensionSurveyCandidate
}

export type ApprovalTask =
  | DataSourceApprovalTask
  | DatasetApprovalTask
  | MetricApprovalTask
  | DimensionApprovalTask

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

async function loadAllMetricCandidates(): Promise<MetricCandidate[]> {
  const items: MetricCandidate[] = []
  for (let offset = 0; ;) {
    const page = await metricCandidateAPI.list(pageSize, offset)
    const pageItems = Array.isArray(page.items) ? page.items : []
    items.push(...pageItems)
    if (!pageItems.length || items.length >= page.total) return items
    offset += pageItems.length
  }
}

async function loadAllDimensionCandidates(): Promise<DimensionSurveyCandidate[]> {
  const items: DimensionSurveyCandidate[] = []
  for (let offset = 0; ;) {
    const page = await semanticGovernanceAPI.listCandidates({
      status: 'SUGGESTED',
      limit: pageSize,
      offset,
    })
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

/** 指标候选属于系统生成的治理审批，仅向具备指标管理权限的用户展示。 */
export async function loadMetricApprovalTasks(): Promise<ApprovalTaskBatch> {
  const permission = await metricAPI.evaluatePermission('', 'MANAGE')
  if (!permission.allowed) return { category: 'METRIC', tasks: [] }
  const candidates = await loadAllMetricCandidates()
  const tasks = candidates
    .filter(candidate => candidate.status === 'READY' || candidate.status === 'NEEDS_REVIEW')
    .map<MetricApprovalTask>(candidate => ({
      key: `METRIC:${candidate.id}`,
      category: 'METRIC',
      name: candidate.name,
      subtitle: `${candidate.method} · ${Math.round(candidate.confidence * 100)}% 置信度`,
      submittedAt: candidate.createdAt,
      candidate,
    }))
  return { category: 'METRIC', tasks }
}

/** DWS 维度勘测候选需要人工接收或拒绝，归入维度治理审批。 */
export async function loadDimensionApprovalTasks(): Promise<ApprovalTaskBatch> {
  const [read, manage] = await Promise.all([
    semanticGovernanceAPI.evaluatePermission('READ'),
    semanticGovernanceAPI.evaluatePermission('MANAGE'),
  ])
  if (!read.allowed || !manage.allowed) return { category: 'DIMENSION', tasks: [] }
  const candidates = await loadAllDimensionCandidates()
  const tasks = candidates.map<DimensionApprovalTask>(candidate => ({
    key: `DIMENSION:${candidate.id}`,
    category: 'DIMENSION',
    name: candidate.proposedName,
    subtitle: `${candidate.fieldRole} · ${candidate.fieldCode}`,
    submittedAt: candidate.createdAt,
    candidate,
  }))
  return { category: 'DIMENSION', tasks }
}
