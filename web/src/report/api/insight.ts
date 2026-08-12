import { apiRequest } from '../../lib/api'

/**
 * 证据与结论接口。
 *
 * 证据只能由服务端从真实执行结果推导：客户端选择「对哪个组件、用哪种分析方法」，
 * 事实、数值与单元格引用都来自那次执行。这条边界是结论可核验的前提——如果调用方
 * 能自带事实，发布前的事实门禁就形同虚设。
 */

/** 已登记的确定性分析方法。 */
export type AnalysisMethod =
  | 'CURRENT_VALUE' | 'PERIOD_COMPARISON' | 'TREND' | 'ANOMALY_POINT' | 'TOP_N'
  | 'CONTRIBUTION' | 'MAX_CHANGE' | 'TARGET_ACHIEVEMENT' | 'GROUP_DIFFERENCE'
  | 'SHARE_OF_TOTAL' | 'DATA_COMPLETENESS'

export const analysisMethodLabels: Record<AnalysisMethod, string> = {
  CURRENT_VALUE: '当前值',
  PERIOD_COMPARISON: '同环比',
  TREND: '趋势',
  ANOMALY_POINT: '异常点',
  TOP_N: 'Top N',
  CONTRIBUTION: '贡献度',
  MAX_CHANGE: '变化最大项',
  TARGET_ACHIEVEMENT: '目标达成',
  GROUP_DIFFERENCE: '分组差异',
  SHARE_OF_TOTAL: '占比',
  DATA_COMPLETENESS: '数据完整度',
}

export type CellRef = { rowKey: string; columnKey: string }

export type EvidenceFact = {
  id: string
  metricVersionId: string
  currentValue: string
  previousValue: string | null
  changeRate: string | null
  unit: string
  cellRefs: CellRef[]
}

export type QualityWarning = {
  code: string
  severity: 'INFO' | 'WARNING' | 'BLOCKING'
  message: string
}

export type EvidenceBundle = {
  schemaVersion: string
  sourceType: 'SEMANTIC_IR' | 'DATASET_QUERY'
  datasetVersionId: string
  asOf: string
  resolvedTimeRange: { start: string; endExclusive: string; timezone: string }
  analysisMethod: AnalysisMethod
  analysisMethodVersion: string
  evidenceAlgorithmVersion: string
  facts: EvidenceFact[]
  qualityWarnings: QualityWarning[]
  generatedAt: string
}

export type EvidenceRecord = {
  id: string
  reportId: string
  componentId: string
  evidenceHash: string
  evidence: EvidenceBundle
  createdAt: string
}

export type InsightContent = {
  summary: string
  findings: string[]
  risks: string[]
  actions: string[]
}

export type Citation = {
  textSpan: [number, number]
  kind: 'RESULT_CELL' | 'CONTRACT' | 'TIME_SPEC'
  rowKey?: string
  columnKey?: string
  contractId?: string
}

export type InsightArtifact = {
  schemaVersion: string
  id: string
  evidenceHash: string
  content: InsightContent
  citations: Citation[]
  status: 'CURRENT' | 'STALE' | 'FAILED'
  humanEdited: boolean
  humanEditedBy?: string | null
  humanEditedAt?: string | null
}

export type ArtifactRecord = {
  id: string
  reportId: string
  componentId: string
  evidenceId: string
  artifact: InsightArtifact
  createdAt: string
}

export type VerifyFailure = {
  element: string
  text?: string
  reason: string
  expected: string[]
}

export type VerifyReport = {
  passed: boolean
  verifierVersion: string
  policyWordlistVersion: string
  /** 用哪一类对象目录完成的核验：语义发布版本，或数据集版本。 */
  source?: 'SEMANTIC_RELEASE' | 'DATASET_VERSION'
  failures: VerifyFailure[]
}

export type GeneratedInsight = {
  artifact: ArtifactRecord
  evidence: EvidenceRecord
  verification: VerifyReport
}

function idempotencyHeaders() {
  return { 'Idempotency-Key': crypto.randomUUID() }
}

export const reportInsightAPI = {
  /** 对某个组件执行一次真实查询并据此推导证据。调用方不提供任何事实。 */
  deriveEvidence(reportId: string, componentId: string, input: { analysisMethod: AnalysisMethod; topN?: number }) {
    return apiRequest<EvidenceRecord>(
      `/v1/reports/${encodeURIComponent(reportId)}/insights/${encodeURIComponent(componentId)}/derive`,
      { method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input) },
    )
  },
  /**
   * 生成结论：服务端先按当前数据重新推导证据，再让模型撰写并通过事实校验。
   * 模型不书写任何数字——它只能引用证据中的事实，数值由服务端代入。
   * 未通过校验时不会保存，返回体里带有校验器给出的具体失败项。
   */
  generate(reportId: string, componentId: string, input: { analysisMethod: AnalysisMethod; topN?: number }) {
    return apiRequest<GeneratedInsight>(
      `/v1/reports/${encodeURIComponent(reportId)}/insights/${encodeURIComponent(componentId)}/generate`,
      { method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input) },
    )
  },
  getCurrent(reportId: string, componentId: string) {
    return apiRequest<ArtifactRecord>(
      `/v1/reports/${encodeURIComponent(reportId)}/insights/${encodeURIComponent(componentId)}`,
    )
  },
  /** 人工改写结论会把制品标记为 humanEdited，从而不再声称通过了生成期核验。 */
  editCurrent(reportId: string, componentId: string, content: InsightContent) {
    return apiRequest<ArtifactRecord>(
      `/v1/reports/${encodeURIComponent(reportId)}/insights/${encodeURIComponent(componentId)}/edit`,
      { method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ content }) },
    )
  },
}
