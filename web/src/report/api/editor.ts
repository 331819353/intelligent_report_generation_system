import { apiRequest } from '../../lib/api'

export type EditorScope = {
  pageId: string
  sectionId?: string
  blockId?: string
}

export type EditorOperation = {
  op: string
  targetId: string
  payload: Record<string, unknown>
}

export type EditorOperationBundle = {
  schemaVersion: '1.0'
  reportId: string
  baseRevision: number
  source: 'USER' | 'AI' | 'IMPORT' | 'SYSTEM'
  aiRunId: string | null
  scope: EditorScope | null
  operations: EditorOperation[]
}

export type EditorComponent = {
  id: string
  templateRef: { type: string; version: string }
  dataBinding?: {
    bindingMode: 'SEMANTIC_IR' | 'DATASET_FIELD'
    dataContextId?: string
    dimensions?: Array<{ role: string; field: string }>
    measures?: Array<{ role: string; field: string }>
    semanticQueryRef?: Record<string, unknown>
  }
  options: {
    title?: string
    subtitle?: string
    showLegend?: boolean
    showLabel?: boolean
    smooth?: boolean
    orientation?: string
    topN?: number
    colorPaletteRef?: string
    numberFormat?: string
    nullPolicy?: string
    animation?: boolean
    mobileLegendMode?: string
    richText?: string
    imageAssetId?: string
    insightRole?: string
    tablePageSize?: number
  }
}

export type EditorSlot = { id: string; componentId?: string }
export type EditorBlock = {
  id: string
  type: string
  layout?: { desktop?: { x: number; y: number; w: number; h: number }; mobile?: Record<string, unknown> }
  zones: Array<{ id: string; type: string; slots: EditorSlot[] }>
}
export type EditorSection = { id: string; name: string; order: number; blocks: EditorBlock[] }
export type EditorPage = { id: string; name: string; order: number; sections: EditorSection[] }

export type EditorDefinition = {
  schemaVersion: string
  metadata: { id: string; code: string; name: string; description?: string; reportType: 'REPORT' | 'DASHBOARD'; locale?: string }
  dataContexts: Array<{ id: string; datasetId?: string; datasetVersionId?: string; alias?: string }>
  pages: EditorPage[]
  components: EditorComponent[]
  globalFilters?: unknown[]
  interactions?: unknown[]
  runtimePolicy?: Record<string, unknown>
  templateRef?: Record<string, unknown>
  themeRef?: Record<string, unknown>
  canvas?: Record<string, unknown>
  provenance?: Record<string, unknown>
}

export type ReportDraft = {
  reportId: string
  tenantId: string
  definition: EditorDefinition
  definitionHash: string
  schemaVersion: string
  revisionNo: number
  updatedBy: string
  updatedAt: string
}

export type ReportRevision = {
  id: string
  reportId: string
  revisionNo: number
  baseRevisionNo: number
  source: 'USER' | 'AI' | 'IMPORT' | 'SYSTEM' | 'UNDO' | 'REDO'
  operationJson: EditorOperationBundle | EditorOperation[] | Record<string, unknown>
  beforeHash: string
  afterHash: string
  actorUserId: string
  aiRunId?: string
  createdAt: string
}

export type AIPreview = {
  beforeHash: string
  afterHash: string
  affectedComponents: string[]
  bundle: EditorOperationBundle
}

export type AIPreviewResponse = { aiRunId: string; preview: AIPreview }
export type PublicationIssue = { code: string; path?: string; message: string }
export type PublicationGate = {
  id: 'SEMANTIC' | 'FRESHNESS' | 'PERMISSION' | 'EXECUTION' | 'RESPONSIVE' | 'FACT'
  label: string
  status: 'PASSED' | 'WARNING' | 'BLOCKED'
  summary: string
  issues: PublicationIssue[]
}
export type PublicationReviewResponse = {
  reviewRunId: string
  checkedAt: string
  preflight: {
    draft: ReportDraft
    checks: PublicationGate[]
    blockerCodes: string[]
    warningCodes: string[]
  }
  impact: {
    visibleCount: number
    editableCount: number
    activeShareCount: number
    subscriptionCount: number
    currentVersionNo: number
    targetVersionNo: number
  }
  dependencyRefs: string[]
  review: {
    recommendation: 'ALLOW' | 'CONDITIONAL' | 'BLOCK'
    headline: string
    summary: string
    risks: Array<{ code: string; title: string; explanation: string; evidence: string; suggestedAction: string }>
    // 未配置模型提供方时，评审文本直接由确定性门禁生成；发布裁决两者一致。
    source?: 'AI' | 'DETERMINISTIC'
  }
}
export type DraftMutationResponse = { draft: ReportDraft; revision: ReportRevision }
export type AIReportPlan = {
  reportTemplateVersion: string
  sections: Array<{
    title: string
    purpose: string
    blocks: Array<{ purpose: string; recommendedComponent: string }>
  }>
}
export type AICreateReportResponse = {
  report: { id: string; name: string; code: string; reportType: 'REPORT' | 'DASHBOARD' }
  draft: ReportDraft
  revision: ReportRevision
  aiRunId: string
  selection: { dataContextId: string; reportName: string; rationale: string; confidence: 'HIGH' | 'MEDIUM' | 'LOW' }
  plan: AIReportPlan
}

/** 服务端裁剪后的受治理数据上下文；字段列表已按当前用户的列权限过滤。 */
export type DataContextCandidate = {
  dataContext: { id: string; datasetId: string; datasetVersionId: string; alias?: string }
  name: string
  description: string
  fields: string[]
}

/** 组件模板合同：数据绑定面板只允许提交合同内的角色与维度/度量数量。 */
export type ComponentManifest = {
  type: string
  version: string
  displayName: string
  category: 'CHART' | 'TABLE' | 'TEXT' | 'METRIC' | 'CONTROL' | 'MEDIA' | string
  recommendedSize: { w: number; h: number }
  dataContract: {
    dimensions: { min: number; max: number }
    measures: { min: number; max: number }
    timeField?: { required: boolean }
    roles: string[]
  }
}

export type BlankCreateReportResponse = {
  report: { id: string; name: string; code: string; reportType: 'REPORT' | 'DASHBOARD' }
  draft: ReportDraft
}

function idempotencyHeaders() {
  return { 'Idempotency-Key': crypto.randomUUID() }
}

export const reportEditorAPI = {
  createAI(input: { intent: string }) {
    return apiRequest<AICreateReportResponse>('/v1/reports/ai/create', {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  // 空白新建不依赖模型提供方，是未配置 LLM 时报告主链的保底入口。
  createBlank(input: { name: string; description?: string; dataContextId?: string }) {
    return apiRequest<BlankCreateReportResponse>('/v1/reports/blank', {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  listDataContexts() {
    return apiRequest<{ items: DataContextCandidate[] }>('/v1/report-data-contexts')
  },
  listComponentManifests() {
    return apiRequest<{ items: ComponentManifest[] }>('/v1/report-component-manifests')
  },
  getDraft(reportId: string) {
    return apiRequest<ReportDraft>(`/v1/reports/${encodeURIComponent(reportId)}/draft`)
  },
  listRevisions(reportId: string) {
    return apiRequest<{ items: ReportRevision[] }>(`/v1/reports/${encodeURIComponent(reportId)}/revisions`)
  },
  previewAI(reportId: string, input: { intent: string; dataContextId: string; scope: EditorScope }) {
    return apiRequest<AIPreviewResponse>(`/v1/reports/${encodeURIComponent(reportId)}/ai/preview`, {
      method: 'POST', body: JSON.stringify(input),
    })
  },
  applyOperations(reportId: string, bundle: EditorOperationBundle) {
    return apiRequest<DraftMutationResponse>(`/v1/reports/${encodeURIComponent(reportId)}/operations`, {
      method: 'POST', body: JSON.stringify(bundle),
    })
  },
  undo(reportId: string) {
    return apiRequest<DraftMutationResponse>(`/v1/reports/${encodeURIComponent(reportId)}/undo`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
  },
  redo(reportId: string) {
    return apiRequest<DraftMutationResponse>(`/v1/reports/${encodeURIComponent(reportId)}/redo`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
  },
  reviewPublication(reportId: string, input: { sourceRevisionNo: number }) {
    return apiRequest<PublicationReviewResponse>(`/v1/reports/${encodeURIComponent(reportId)}/publish-review`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  publish(reportId: string, input: {
    sourceRevisionNo: number
    acknowledgeStaleInsights: boolean
    desktopPreviewHash: string
    mobilePreviewHash: string
    reviewRunId: string
    humanComment: string
    acknowledgedIssueCodes: string[]
  }) {
    return apiRequest<Record<string, unknown>>(`/v1/reports/${encodeURIComponent(reportId)}/publish`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
}
