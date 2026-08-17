import { apiRequest, apiResponse } from './api'
import type { SemanticImport, SemanticImportState } from './semantic'

// semantic-bundle/v1：四分区语义资产的统一 JSON 导入合同与血缘图 API。
// 引用一律使用稳定 code；上传后服务端确定性展开为受治理的导入行。

const semanticBase = '/v1/askdata/semantic'

export type BundleImportResolution = 'CREATE' | 'UPDATE' | 'UNCHANGED' | 'SKIPPED' | 'FAILED'

export type BundleImportIssue = {
  column: string
  code: string
  message: string
  expected: string
  actual?: string
}

export type BundleImportRow = {
  rowNo: number
  assetType: string
  bundleAsset?: string
  code?: string
  name?: string
  state: 'VALID' | 'INVALID' | 'SKIPPED' | 'COMMITTED'
  resolution: BundleImportResolution
  issues: BundleImportIssue[]
  createdObjectId?: string
  createdVersionId?: string
}

export type BundleImportCounts = {
  created: number
  updated: number
  unchanged: number
  skipped: number
  failed: number
  pending: number
}

export type BundleImportIndexSummary = {
  createdVersions: number
  indexed: number
  embeddingReady: number
  embeddingFailed: number
  awaitingRelease: number
}

export type BundleImportReport = {
  import: SemanticImport
  counts: BundleImportCounts
  byAssetType: Record<string, BundleImportCounts>
  index: BundleImportIndexSummary
  rows: BundleImportRow[]
}

export type LineageFamily = 'PHYSICAL' | 'SEMANTIC'

export type LineageNodeType =
  | 'DATASET_VERSION'
  | 'MODEL'
  | 'MODEL_FIELD'
  | 'MEASURE'
  | 'METRIC'
  | 'DIMENSION'
  | 'HIERARCHY'
  | 'KNOWLEDGE'

export type LineageNode = { type: LineageNodeType; id: string; code?: string }

export type LineageEdge = {
  id: string
  family: LineageFamily
  kind: string
  from: LineageNode
  to: LineageNode
  derivation: 'COMPUTED' | 'DECLARED' | 'IMPORTED'
  validFrom: string
}

export type LineageNeighbourhood = {
  center: LineageNode
  nodes: LineageNode[]
  edges: LineageEdge[]
  truncated: boolean
}

export type LineageImpactHop = {
  hop: number
  nodes: LineageNode[]
  edges: LineageEdge[]
}

export type LineageImpactReport = {
  root: LineageNode
  hops: LineageImpactHop[]
  total: number
  truncated: boolean
}

export type DiscoverySection = 'MODEL' | 'METRIC' | 'DIMENSION' | 'KNOWLEDGE'

export type DiscoverySource = 'EXACT' | 'LEXICAL' | 'VECTOR' | 'EXPANDED'

export type DiscoveryCandidate = {
  section: DiscoverySection
  objectType: string
  objectId: string
  versionId?: string
  code: string
  name: string
  status: string
  summary?: string
  score: number
  sources: DiscoverySource[]
  expandedFrom?: string
}

export type DiscoveryResult = {
  candidates: DiscoveryCandidate[]
  degraded: boolean
  degradedReason?: string
}

export const semanticBundleAPI = {
  // 四分区混合发现检索：确定性目录巷道 + Release 向量巷道 + 血缘扩展。
  discover: (query: string, sections?: DiscoverySection[], expand = true) =>
    apiRequest<DiscoveryResult>(`${semanticBase}/retrieval`, {
      method: 'POST',
      body: JSON.stringify({ query, sections: sections ?? [], expand }),
    }),
  // Bundle 上传复用统一导入通道：批级类型 BUNDLE，文件为 semantic-bundle/v1 JSON。
  uploadBundle: (domainId: string, file: File) => {
    const form = new FormData()
    form.append('assetType', 'BUNDLE')
    form.append('domainId', domainId)
    form.append('file', file)
    return apiRequest<{ importId: string; assetType: string; state: SemanticImportState; created: boolean }>(
      `${semanticBase}/imports`,
      { method: 'POST', body: form },
    )
  },
  bundleSchema: () => apiRequest<Record<string, unknown>>(`${semanticBase}/imports/schema`),
  importRows: (importId: string) =>
    apiRequest<BundleImportReport>(`${semanticBase}/imports/${encodeURIComponent(importId)}/rows`),
  exportBundle: (domainId: string, assetTypes: string[], releaseId?: string) => {
    const query = new URLSearchParams({
      domainId,
      assetTypes: assetTypes.join(','),
      format: 'json',
    })
    if (releaseId) query.set('releaseId', releaseId)
    return apiResponse(`${semanticBase}/exports?${query}`)
  },
  lineageNeighbourhood: (node: LineageNode, family?: LineageFamily, depth = 2) => {
    const query = new URLSearchParams({
      nodeType: node.type,
      nodeId: node.id,
      depth: String(depth),
    })
    if (family) query.set('family', family)
    return apiRequest<LineageNeighbourhood>(`${semanticBase}/graph/neighbourhood?${query}`)
  },
  lineageImpact: (node: LineageNode, family?: LineageFamily) =>
    apiRequest<LineageImpactReport>(`${semanticBase}/graph/impact`, {
      method: 'POST',
      body: JSON.stringify({ nodeType: node.type, nodeId: node.id, family: family ?? '' }),
    }),
  rebuildLineage: () =>
    apiRequest<{ edges: number }>(`${semanticBase}/graph/rebuild`, {
      method: 'POST',
      body: '{}',
    }),
}

// bundleExportAssetTypes 是 Bundle JSON 导出覆盖的四分区资产类型全集。
export const bundleExportAssetTypes = [
  'MODEL',
  'MEASURE',
  'METRIC',
  'METRIC_DIMENSION',
  'DIMENSION',
  'MEMBER',
  'HIERARCHY',
  'RELATIONSHIP',
  'TERM',
  'KPI_BUNDLE',
  'KNOWLEDGE',
]

export const resolutionLabel: Record<BundleImportResolution, string> = {
  CREATE: '新建',
  UPDATE: '更新',
  UNCHANGED: '未变化',
  SKIPPED: '跳过',
  FAILED: '失败',
}

export const lineageNodeTypeLabel: Record<LineageNodeType, string> = {
  DATASET_VERSION: '数据集版本',
  MODEL: '语义模型',
  MODEL_FIELD: '逻辑字段',
  MEASURE: '度量',
  METRIC: '指标',
  DIMENSION: '维度',
  HIERARCHY: '层级',
  KNOWLEDGE: '业务知识',
}
