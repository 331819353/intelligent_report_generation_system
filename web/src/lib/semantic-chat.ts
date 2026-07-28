import { apiRequest } from './api'

export type SemanticQueryEvidence = {
  index: number
  nodeKey: string
  relationType?: string
  subjectType: string
  subjectRef: string
  label: string
  authority: string
  confidence: number
  evidenceHash: string
}

export type SemanticQueryResolutionStep = {
  stage: 'INTENT_RECOGNITION' | 'DOMAIN_CATALOG' | 'METRIC_CATALOG' | 'DIMENSION_MEMBER' | 'DATASET_LOCK'
  status: string
  candidateCount?: number
  selectedCode?: string
  decision?: string
}

export type SemanticQueryPlan = {
  id: string
  graphGenerationId: string
  graphGeneration: number
  questionHash: string
  intent: string
  status: 'READY' | 'AMBIGUOUS' | 'GAP' | 'REJECTED' | 'EXECUTED' | 'FAILED'
  confidence: number
  selectedMetricId?: string
  selectedMetricVersionId?: string
  selectedDimensionId?: string
  selectedDatasetVersionId?: string
  selectedMaterializationId?: string
  pathHash?: string
  failureCode?: string
  evidence: SemanticQueryEvidence[]
  resolution: SemanticQueryResolutionStep[]
  conditions?: {
    domain: string
    metricCode: string
    metricVersionId: string
    datasetVersionId: string
    dimensions: Array<{
      dimensionCode: string
      dimensionId: string
      memberKey?: string
      memberKeys?: string[]
    }>
    timeRange?: { start: string; endExclusive: string }
  }
  executedQueryId?: string
  executionErrorCode?: string
  executionDurationMs?: number
  executionRowCount?: number
  createdAt: string
}

export type SemanticQueryTurn = {
  questionHash: string
  intent: string
  metricCodes: string[]
  contextQueryPlanIds: string[]
  contextInherited: boolean
  plans: SemanticQueryPlan[]
  trace: SemanticQueryTurnTrace
}

export type SemanticQueryTurnTrace = {
  conversationQuestions: string[]
  contextPolicy: string
  standaloneQuestion: string
  extraction: {
    intent: string
    metricTerms: string[]
    dimensionValueTerms: string[]
  }
  metricCandidates: Array<{
    code: string
    label: string
    matchedTerm?: string
    matchMethod: string
    score: number
    selected: boolean
    source: string
  }>
  dimensionValueLookups: Array<{
    term: string
    canonicalValue?: string
    aliasValues?: string[]
    metricCode: string
    metricName?: string
    metricFieldId: string
    metricVersionId?: string
    datasetVersionId?: string
    materializationId?: string
    tableSchema?: string
    tableName?: string
    decisionId?: string
    dimensionId?: string
    dimensionCode: string
    dimensionName: string
    dimensionFieldId: string
    dimensionFieldName: string
    dimensionFieldDescription: string
    vectorQuery: string
    vectorModel?: string
    vectorDimensions?: number
    vectorSearchStatus: string
    vectorCandidateCount: number
    vectorCandidateMemberKeys?: string[]
    vectorTopScore?: number
    whereDesignStatus: string
    whereDesignOperator?: string
    whereDesignReason?: string
    whereDesignModel?: string
    matchMethod: string
    candidateCount: number
    candidateMemberKeys?: string[]
    selectedMemberKeys?: string[]
    whereCondition: string
    compiledCondition: string
    candidateFilter: {
      inputCount: number
      acceptedCount: number
      rejectedCount: number
      status: string
      rules: string[]
    }
    selected: boolean
    source: string
    sensitive: boolean
  }>
  finalSelections: Array<{
    metricCode: string
    metricName: string
    metricFieldId: string
    metricVersionId: string
    datasetVersionId: string
    dimensions: Array<{
      dimensionCode: string
      dimensionName: string
      memberKeys: string[]
    }>
    whereCondition: string
    compiledCondition: string
    planId: string
    planStatus: string
  }>
  assessments: Array<{
    step: string
    status: 'PASS' | 'WARN' | 'BLOCKED' | string
    decision: string
    detail: string
  }>
}

export type SemanticPreviewResult = {
  queryId: string
  columns: string[]
  rows: unknown[][]
  rowCount: number
  durationMs: number
  warnings?: Array<{ code: string; message: string }>
}

export type SemanticAnswerEvidence = {
  graphGenerationId: string
  graphGeneration: number
  pathHash: string
  metricId: string
  metricVersionId: string
  dimensionId?: string
  datasetVersionId: string
  materializationId: string
  lineage: SemanticQueryEvidence[]
  permissionDecision: string
  freshnessDecision: string
  compatibilityDecision: string
  executionRevalidated: boolean
}

export type SemanticQueryExecution = {
  queryPlan: SemanticQueryPlan
  result: SemanticPreviewResult
  evidence: SemanticAnswerEvidence
  comparison?: {
    mode: string
    currentRange: { start: string; endExclusive: string }
    baselineRange: { start: string; endExclusive: string }
    baseline: SemanticPreviewResult
  }
}

export type SemanticGraphStatus = {
  status: string
  currentGenerationId?: string
  currentGeneration?: number
  requestedEventVersion: number
  appliedEventVersion: number
  nodeCount?: number
  edgeCount?: number
  lastErrorCode?: string
}

export type GoldenQuestionSet = {
  id: string
  code: string
  name: string
  businessDomain: string
  version: number
  correctnessThreshold: number
  safetyThreshold: number
  status: 'DRAFT' | 'ACTIVE' | 'RETIRED'
  recordVersion: number
  createdAt: string
  updatedAt: string
}

export type GoldenQuestion = {
  id: string
  setId: string
  questionHash: string
  expectedPathHash: string
  expectedStatus: string
  status: string
}

export type GoldenQuestionReplay = {
  id: string
  goldenQuestionId: string
  status: 'PASSED' | 'FAILED' | 'ERROR'
  failureStage?: string
  failureCode?: string
  queryPlan: SemanticQueryPlan
  createdAt: string
}

export type PlanQuestionInput = {
  question: string
  contextQueryPlanId?: string
  signal?: AbortSignal
}

export type PlanTurnInput = {
  question: string
  priorQuestions?: string[]
  contextQueryPlanIds?: string[]
  signal?: AbortSignal
}

const newQueryID = () => typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
  ? crypto.randomUUID()
  : '00000000-0000-4000-8000-000000000001'

export const semanticChatAPI = {
  graphStatus: () => apiRequest<SemanticGraphStatus>('/v1/semantic-qa/graph/status'),

  planTurn: ({ question, priorQuestions, contextQueryPlanIds, signal }: PlanTurnInput) =>
    apiRequest<SemanticQueryTurn>('/v1/semantic-qa/query-turns', {
      method: 'POST',
      signal,
      body: JSON.stringify({
        question,
        maximumPathHops: 8,
        ...(priorQuestions?.length ? { priorQuestions } : {}),
        ...(contextQueryPlanIds?.length ? { contextQueryPlanIds } : {}),
      }),
    }),

  planQuestion: ({ question, contextQueryPlanId, signal }: PlanQuestionInput) =>
    apiRequest<SemanticQueryPlan>('/v1/semantic-qa/query-plans', {
      method: 'POST',
      signal,
      body: JSON.stringify({
        question,
        intent: 'UNKNOWN',
        metricCode: '',
        maximumPathHops: 8,
        ...(contextQueryPlanId ? { contextQueryPlanId } : {}),
      }),
    }),

  executePlan: (plan: SemanticQueryPlan, signal?: AbortSignal) =>
    apiRequest<SemanticQueryExecution>(`/v1/semantic-qa/query-plans/${plan.id}/execute`, {
      method: 'POST',
      signal,
      body: JSON.stringify({
        expectedGraphGenerationId: plan.graphGenerationId,
        expectedPathHash: plan.pathHash,
        queryId: newQueryID(),
        parameters: {},
        maxRows: 100,
      }),
    }),

  listGoldenQuestionSets: () =>
    apiRequest<{ items: GoldenQuestionSet[] } | GoldenQuestionSet[]>('/v1/semantic-qa/golden-question-sets')
      .then(result => Array.isArray(result) ? result : result.items),

  listGoldenQuestions: (setId: string) =>
    apiRequest<{ items: GoldenQuestion[] } | GoldenQuestion[]>(`/v1/semantic-qa/golden-questions?setId=${encodeURIComponent(setId)}`)
      .then(result => Array.isArray(result) ? result : result.items),

  replayGoldenQuestion: (id: string) =>
    apiRequest<GoldenQuestionReplay>(`/v1/semantic-qa/golden-questions/${id}/replay`, { method: 'POST', body: '{}' }),
}
