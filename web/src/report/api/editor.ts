import { apiRequest } from '../../lib/api'
import type { Block, FieldBinding, Page, ReportComponent, ReportDefinition, Section } from '../render/schema'
import type { ComponentManifest } from '../render/manifests'
import type { RuntimeComponentResult } from './runtime'

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

/**
 * 编辑器不再声明自己的报告结构类型。草稿、发布制品、AI 生成结果都是同一份
 * Report Definition 1.0，前端只有 render/schema 一套镜像类型；曾经的
 * EditorDefinition 子集会在编辑器一侧静默丢掉 canvas、区块布局与槽位网格。
 */
export type EditorComponent = ReportComponent
export type EditorBlock = Block
export type EditorSection = Section
export type EditorPage = Page
export type EditorDefinition = ReportDefinition

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
  fieldDefinitions?: DataContextField[]
}

export type DataContextField = {
  code: string
  name: string
  canonicalType: string
  semanticType: string
  role: 'DIMENSION' | 'MEASURE' | 'TIME' | 'IDENTIFIER' | string
  aggregation: string
}

/**
 * 组件清单由 render/manifests 统一定义（含 renderer 与 optionSchema），
 * 编辑器与渲染器读取的是同一个注册表，不再各自维护一份裁剪过的合同类型。
 */
export type { ComponentManifest }

export type BlankCreateReportResponse = {
  report: { id: string; name: string; code: string; reportType: 'REPORT' | 'DASHBOARD' }
  draft: ReportDraft
}

export type ReportStarterTemplate = {
  id: 'executive-overview' | 'trend-analysis' | 'data-detail'
  name: string
  description: string
  category: string
  componentCount: number
  requiresDimension: boolean
}

export type DraftExecutionInput = {
  pageId: string
  visibleBlockIds?: string[]
  filterValues?: Record<string, unknown>
}

/**
 * 草稿执行结果与已发布运行结果结构相同，但携带的是修订号与定义哈希而不是版本号，
 * 因此二者在界面上永远不会被混为一谈。
 */
export type DraftExecution = {
  reportId: string
  draft: true
  revisionNo: number
  definitionHash: string
  asOf: string
  timezone: string
  components: RuntimeComponentResult[]
}

function idempotencyHeaders() {
  return { 'Idempotency-Key': crypto.randomUUID() }
}

export const reportEditorAPI = {
  createAI(input: { intent: string; reportType?: 'REPORT' | 'DASHBOARD' }) {
    return apiRequest<AICreateReportResponse>('/v1/reports/ai/create', {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  // 空白新建不依赖模型提供方，是未配置 LLM 时报告主链的保底入口。
  createBlank(input: { name: string; description?: string; dataContextId?: string; reportType?: 'REPORT' | 'DASHBOARD' }) {
    return apiRequest<BlankCreateReportResponse>('/v1/reports/blank', {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  /**
   * 以 JSON 配置文件为核心：任何一份合法的 Report Definition 1.0 都可以直接
   * 导入成新的报告草稿（同一份 JSON、同一条校验与发布链）。调用前应刷新
   * metadata.id/code，避免与已有报告冲突。
   */
  createFromDefinition(definition: ReportDefinition) {
    return apiRequest<BlankCreateReportResponse>('/v1/reports', {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ definition }),
    })
  },
  listStarterTemplates() {
    return apiRequest<{ items: ReportStarterTemplate[] }>('/v1/report-templates')
  },
  instantiateStarterTemplate(templateId: string, input: { name: string; description?: string; dataContextId: string; reportType?: 'REPORT' | 'DASHBOARD' }) {
    return apiRequest<BlankCreateReportResponse>(`/v1/report-templates/${encodeURIComponent(templateId)}/instantiate`, {
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
  /**
   * 按当前草稿执行组件查询，让作者在绑定字段时就能看到真实数据，而不是先发布
   * 一个版本才知道绑定是否成立。执行仍按调用者自己的行/列权限进行，也不会写入
   * 任何版本或制品；返回体里的 draft/revisionNo 用于与已发布运行结果区分。
   */
  executeDraft(reportId: string, input: DraftExecutionInput, options: { signal?: AbortSignal } = {}) {
    return apiRequest<DraftExecution>(`/v1/reports/${encodeURIComponent(reportId)}/draft/execute`, {
      method: 'POST', signal: options.signal, body: JSON.stringify(input),
    })
  },
  listRevisions(reportId: string) {
    return apiRequest<{ items: ReportRevision[] }>(`/v1/reports/${encodeURIComponent(reportId)}/revisions`)
  },
  previewAI(reportId: string, input: { intent: string; dataContextId: string; scope: EditorScope }) {
    return apiRequest<AIPreviewResponse>(`/v1/reports/${encodeURIComponent(reportId)}/ai/preview`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  /**
   * 让模型在所选数据集的受治理字段目录里为一张卡片识别度量与维度。返回的是建议，
   * 不写草稿；编辑器把它填入绑定面板，由人确认后再以 USER 操作保存。
   */
  suggestCardBinding(reportId: string, input: {
    componentId?: string; dataContextId: string; manifestType: string; manifestVersion?: string; title?: string; intent?: string
  }) {
    return apiRequest<{ aiRunId: string; suggestion: { dimensions: FieldBinding[]; measures: FieldBinding[]; rationale: string } }>(
      `/v1/reports/${encodeURIComponent(reportId)}/ai/card-binding`,
      { method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input) },
    )
  },
  applyOperations(reportId: string, bundle: EditorOperationBundle) {
    return apiRequest<DraftMutationResponse>(`/v1/reports/${encodeURIComponent(reportId)}/operations`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(bundle),
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
    /** 发布人对两种布局的确认声明，不是客户端算出的回显哈希。 */
    previewedDesktop: boolean
    previewedMobile: boolean
    reviewRunId: string
    humanComment: string
    acknowledgedIssueCodes: string[]
  }) {
    return apiRequest<Record<string, unknown>>(`/v1/reports/${encodeURIComponent(reportId)}/publish`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
}
