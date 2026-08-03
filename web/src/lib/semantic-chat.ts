import { apiRequest, apiResponse, RequestError, type APIError } from './api'

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
  questionRunId: string
  state: SemanticQuestionState
  lifecycle: SemanticQuestionStateEvent[]
  questionHash: string
  status: 'PLANNING' | 'PLANNED' | 'NEEDS_METRIC_CONFIRMATION' | 'NEEDS_DIMENSION_CONFIRMATION' | 'SEMANTIC_GAP'
  intent: string
  metricCodes: string[]
  contextQueryPlanIds: string[]
  contextInherited: boolean
  tokenization?: SemanticQueryTokenization
  clarification?: {
    type: 'METRIC' | 'DIMENSION' | 'SEMANTIC_GAP'
    message: string
    metricCandidates?: SemanticQueryTurnTrace['metricCandidates']
    dimensionCandidates?: Array<{
      metricCode: string
      term?: string
      decisionId: string
      dimensionCode: string
      dimensionName: string
      canonicalValue: string
      tableSchema: string
      tableName: string
    }>
  }
  plans: SemanticQueryPlan[]
  trace: SemanticQueryTurnTrace
}

export type SemanticQuestionState =
  | 'RECEIVED'
  | 'AUTHORIZED'
  | 'CONTEXT_READY'
  | 'VALIDATING'
  | 'PLAN_READY'
  | 'CLARIFICATION_REQUIRED'
  | 'COST_APPROVED'
  | 'EXECUTING'
  | 'RESULT_VERIFIED'
  | 'ANSWERED'
  | 'BLOCKED'

export type SemanticQuestionStateEvent = {
  state: SemanticQuestionState
  timestamp: string
  stage?: string
  status?: string
  code?: string
  durationMs?: number
  summary?: Record<string, unknown>
}

export type SemanticQueryProgressEvent = {
  timestamp: string
  questionId?: string
  stage: string
  status: 'RUNNING' | 'SUCCEEDED' | 'WARN' | string
  message: string
}

export type SemanticQueryTurnTrace = {
  conversationQuestions: string[]
  contextPolicy: string
  standaloneQuestion: string
  metricToolLoop?: {
    auditRequestId: string
    model: string
    rounds: number
    toolCalls: number
    steps: SemanticEvidenceLoopStep[]
  }
  dimensionToolLoops?: Array<{
    metricCode: string
    auditRequestId: string
    model: string
    rounds: number
    toolCalls: number
    steps: SemanticEvidenceLoopStep[]
  }>
  extraction: {
    intent: string
    metricTerms: string[]
    dimensionValueTerms: string[]
  }
  metricCandidates: Array<{
    code: string
    label: string
    domain?: string
    datasetVersionId?: string
    tableSchema?: string
    tableName?: string
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
    decisionCandidates?: Array<{
      decisionId: string
      canonicalValue: string
      metricCode: string
      metricName: string
      tableSchema: string
      tableName: string
      whereCondition: string
      compiledCondition: string
      predicateOperator: string
      score: number
      selected: boolean
    }>
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
    timeRange?: { start: string; endExclusive: string }
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

export type SemanticEvidenceLoopStep = {
  round: number
  toolName: string
  argumentsHash: string
  stateHash: string
  evidenceIds: string[]
  newEvidenceCount: number
  errorCode?: string
  terminal: boolean
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
  semanticVersion?: string
  pathHash: string
  queryPlanHash?: string
  resultHash?: string
  queryTraceId?: string
  verifiedAt?: string
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
  validatorChecks?: string[]
}

export type SemanticQueryExecution = {
  questionRunId: string
  state: SemanticQuestionState
  lifecycle: SemanticQuestionStateEvent[]
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

export type SemanticQuestionResponse = {
  questionId: string
  conversationId: string
  parentQuestionId?: string
  questionHash: string
  state: SemanticQuestionState
  status: 'PROCESSING' | 'ANSWERED' | 'CLARIFICATION_REQUIRED' | 'BLOCKED'
  route: 'SEMANTIC_IR' | 'GOVERNED_TEXT_TO_SQL' | 'CLARIFY_OR_REFUSE'
  routing: {
    selected: 'SEMANTIC_IR' | 'GOVERNED_TEXT_TO_SQL' | 'CLARIFY_OR_REFUSE'
    reasonCode: string
    capabilities: Array<{
      route: 'SEMANTIC_IR' | 'GOVERNED_TEXT_TO_SQL' | 'CLARIFY_OR_REFUSE'
      enabled: boolean
      reasonCode?: string
    }>
  }
  semanticVersion?: string
  semanticContentHash?: string
  understanding?: {
    normalizerVersion: string
    normalizedText: string
    mentions: Array<{
      type: string
      startByte: number
      endByte: number
      mentionText: string
      detector: string
      evidenceId: string
      candidates: Array<{ objectType: string; objectId: string; objectVersion: string; code: string; label: string; vid: string }>
    }>
    certifiedExamples: Array<{ objectId: string; objectVersion: string; score: number; evidenceId: string }>
  }
  graphPlan?: {
    id: string
    semanticVersion: string
    contentHash: string
    normalizerVersion: string
    bindingBundleHash: string
    metricVids: string[]
    dimensionVids: string[]
    datasetVids: string[]
    authorizedVids: string[]
    evidenceIds: string[]
    maximumHops: number
    fanoutRisk: string
  }
  executionRegistry?: {
    releaseId: string
    semanticVersion: string
    semanticContentHash: string
    projectionResourceVersion: string
    registryObjectIds: string[]
    metricVersionIds: string[]
    datasetVersionIds: string[]
    materializationIds: string[]
    qualityRuleIds: string[]
    freshnessObservedAt: string
    qualityDecision: string
    proofHash: string
  }
  preflightProofs?: Array<{
    dialect: string
    queryHash: string
    parameterHash: string
    datasetId: string
    datasetVersionId: string
    materializationIds: string[]
    referencedFieldIds: string[]
    argumentCount: number
    maximumRows: number
    parserDecision: string
    allowlistDecision: string
    explainDecision: string
    estimatedRows: number
    estimatedTotalCost: number
    maximumEstimatedRows: number
    maximumEstimatedCost: number
  }>
  lifecycle: SemanticQuestionStateEvent[]
  intent?: {
    intentId: string
    semanticVersion: string
    taskType: string
    metrics: Array<{ id: string; versionId: string; code: string; label: string; bindingEvidence: string[] }>
    dimensions: Array<{ id: string; versionId: string; code: string; label: string; bindingEvidence: string[] }>
    time?: { start: string; endExclusive: string }
    executionPath: string
    graphPlanIds: string[]
    ambiguities: string[]
  }
  semanticIr?: {
    schemaVersion: string
    mode: 'semantic'
    semanticVersion: string
    metrics: Array<{ metricId: string; metricVersionId: string; code: string }>
    dimensions: string[]
    time?: { start: string; endExclusive: string }
    filters: Array<{ dimensionId: string; dimensionCode: string; operator: string; valueIds: string[] }>
    orderBy: Array<{ member: string; direction: string }>
    limit: number
    evidenceIds: string[]
  }
  executionGraph?: {
    generationId: string
    generation: number
    queryPlanIds: string[]
    pathHashes: string[]
    datasetVersionIds: string[]
    materializationIds: string[]
  }
  sqlGuard?: {
    status: 'PASS' | 'BLOCKED'
    mode: string
    maximumRows: number
    checks: Array<{ code: string; status: 'PASS' | 'BLOCKED'; detail: string }>
  }
  resultVerification?: {
    status: 'PASS' | 'BLOCKED'
    trustLevel: 'A' | 'B' | 'C' | 'D'
    checks: Array<{ code: string; status: 'PASS' | 'BLOCKED'; detail: string }>
  }
  answer?: {
    text: string
    resultSets: Array<{ metricCode: string; columns: string[]; rows: unknown[][]; rowCount: number }>
    chart: { type: string; xField?: string; yField?: string }
    asOf: string
  }
  clarification?: SemanticQueryTurn['clarification']
  planning?: SemanticQueryTurn
  queryPlans: SemanticQueryPlan[]
  executions: SemanticQueryExecution[]
  accuracyEvidence?: {
    semanticVersion: string
    intentHash: string
    bindingEvidence: string[]
    metricContracts: string[]
    graphPlanIds: string[]
    queryPlanHash: string
    resultHash: string
    validatorChecks: string[]
    toolLoop: { iterations: number; newEvidenceIds: string[] }
    answerFidelity: 'PASS' | 'BLOCKED'
  }
  failure?: { code: string; message: string }
  budgets: {
    maximumToolLoopRounds: number
    maximumMetricQueries: number
    maximumRowsPerQuery: number
    deadlineMs: number
  }
  toolRegistry: Array<{
    name: string
    canonicalName: string
    allowedStates: SemanticQuestionState[]
    warehouseAccess: boolean
    terminal: boolean
  }>
}

export type AskQuestionInput = {
  question: string
  conversationId: string
  parentQuestionId?: string
  confirmedMetricCodes?: string[]
  confirmedDecisions?: Array<{ metricCode: string; decisionId: string }>
  signal?: AbortSignal
  onProgress?: (event: SemanticQueryProgressEvent) => void
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

export type SemanticQueryToken = {
  text: string
  normalized: string
  partOfSpeech?: string
  entityType:
    | 'METRIC'
    | 'DIMENSION'
    | 'DIMENSION_VALUE'
    | 'PERSON'
    | 'LOCATION'
    | 'ORGANIZATION'
    | 'PROPER_NOUN'
    | 'NOUN_CANDIDATE'
    | 'TIME'
    | 'NUMBER'
    | 'COMPARISON_WORD'
    | 'ANALYSIS_WORD'
    | 'QUERY_WORD'
    | 'TEXT'
    | 'PUNCTUATION'
    | string
  entityName?: string
  entityCode?: string
  start: number
  end: number
  source: string
  confidence: number
}

export type SemanticQueryTokenization = {
  questionHash: string
  strategy: string
  tokens: SemanticQueryToken[]
  entityCount: number
  dictionaryEntityCount: number
  indexPrerequisites: SemanticIndexPrerequisite[]
  questionEmbedding: {
    status: string
    model?: string
    dimensions?: number
  }
  questionMetricTop5: SemanticTokenSemanticCandidate[]
  semanticRetrievalMode?: string
  semanticRetrievals?: SemanticTokenSemanticRetrieval[]
  llmCompletion?: SemanticTokenLLMCompletion
}

export type SemanticIndexPrerequisite = {
  indexType: string
  keyShape: string
  valueShape: string
  total: number
  ready: number
  pending: number
  status: string
  model?: string
}

export type SemanticTokenSemanticCandidate = {
  semanticType: 'METRIC' | 'DIMENSION' | 'DIMENSION_VALUE' | string
  name: string
  code: string
  description?: string
  dimensionName?: string
  dimensionCode?: string
  dimensionType?: string
  valueType?: string
  fieldId?: string
  value?: string
  geographic?: boolean
  score: number
  matchMethod: string
}

export type SemanticTokenSemanticRetrieval = {
  token: string
  partOfSpeech?: string
  entityType: string
  start: number
  end: number
  retrievalStatus: string
  metricCandidates: SemanticTokenSemanticCandidate[]
  dimensionCandidates: SemanticTokenSemanticCandidate[]
}

export type SemanticTokenLLMCompletion = {
  status: string
  model?: string
  intent: string
  augmentedQuestion: string
  metricNames: string[]
  dimensionValues: Array<{
    sourceToken: string
    value: string
    dimensionName: string
    dimensionCode: string
    dimensionType?: string
    valueType?: string
    fieldId?: string
    timeRange?: { start: string; endExclusive: string }
    confidence: number
  }>
  referenceTime?: string
  timezone?: string
  confidence: number
  errorCode?: string
}

export type GoldenQuestionSet = {
  id: string
  code: string
  name: string
  businessDomain: string
  version: number
  correctnessThreshold: number
  safetyThreshold: number
  datasetSplit: 'DEVELOPMENT' | 'VALIDATION' | 'SEALED' | 'PRODUCTION_REGRESSION'
  evaluationMode: 'FIXTURE_REGRESSION' | 'END_TO_END_RESULT_EQUIVALENCE'
  sealedContentHash?: string
  sealedAt?: string
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
  evaluationMode: 'FIXTURE_REGRESSION' | 'END_TO_END_RESULT_EQUIVALENCE'
  semanticVersion?: string
  semanticContentHash?: string
  expectedResultHash?: string
  actualResultHash?: string
  directAnswer: boolean
  refusal: boolean
  unauthorizedBlocked: boolean
  sensitiveLeakDetected: boolean
  queryPlan: SemanticQueryPlan
  createdAt: string
}

export type EvaluationMetric = {
  numerator: number
  denominator: number
  pointEstimate: number
  wilsonLowerBound?: number
  required: number
  passed: boolean
}

export type EvaluationReleaseGate = {
  setId: string
  setVersion: number
  datasetSplit: GoldenQuestionSet['datasetSplit']
  evaluationMode: GoldenQuestionSet['evaluationMode']
  setStatus: GoldenQuestionSet['status']
  sealedContentHash?: string
  decision: 'PASSED' | 'BLOCKED'
  totalCases: number
  evaluatedCases: number
  dualReviewedCases: number
  strictAccuracy: EvaluationMetric
  p0Accuracy: EvaluationMetric
  safetyBlockRate: EvaluationMetric
  unauthorizedBlockRate: EvaluationMetric
  directAnswerCoverage: EvaluationMetric
  refusalPrecision: EvaluationMetric
  sensitiveLeakCount: number
  failureStageCounts: Record<string, number>
  semanticVersions: string[]
  semanticContentHashes: string[]
  blockers: string[]
  calculatedAt: string
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
  confirmedMetricCodes?: string[]
  confirmedDecisions?: Array<{ metricCode: string; decisionId: string }>
  semanticHints?: {
    intent?: string
    metricNames?: string[]
    dimensionValues?: Array<{
      sourceToken?: string
      value: string
      dimensionName: string
      dimensionCode: string
      dimensionType?: string
      valueType?: string
      timeRange?: { start: string; endExclusive: string }
    }>
  }
  signal?: AbortSignal
  onProgress?: (event: SemanticQueryProgressEvent) => void
}

const newQueryID = () => typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
  ? crypto.randomUUID()
  : '00000000-0000-4000-8000-000000000001'

export const semanticChatAPI = {
  graphStatus: () => apiRequest<SemanticGraphStatus>('/v1/semantic-qa/graph/status'),

  askQuestion: async ({
    question, conversationId, parentQuestionId,
    confirmedMetricCodes, confirmedDecisions, signal, onProgress,
  }: AskQuestionInput) => {
    const endpoint = parentQuestionId
      ? `/v1/questions/${encodeURIComponent(parentQuestionId)}/clarifications`
      : '/v1/questions'
    const init: RequestInit = {
      method: 'POST',
      signal,
      body: JSON.stringify({
        question,
        conversationId,
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
        locale: 'zh-CN',
        display: { preferredChart: 'AUTO' },
        ...(confirmedMetricCodes?.length ? { confirmedMetricCodes } : {}),
        ...(confirmedDecisions?.length ? { confirmedDecisions } : {}),
      }),
    }
    if (!onProgress) return apiRequest<SemanticQuestionResponse>(endpoint, init)
    const response = await apiResponse(endpoint, {
      ...init,
      headers: { Accept: 'application/x-ndjson' },
    })
    if (!response.body || !response.headers.get('Content-Type')?.includes('application/x-ndjson')) {
      return response.json() as Promise<SemanticQuestionResponse>
    }
    type StreamFrame =
      | { type: 'progress'; progress: SemanticQueryProgressEvent }
      | { type: 'result'; result: SemanticQuestionResponse }
      | { type: 'error'; status: number; error: APIError }
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let result: SemanticQuestionResponse | undefined
    const consume = (line: string) => {
      if (!line.trim()) return
      const frame = JSON.parse(line) as StreamFrame
      if (frame.type === 'progress') onProgress(frame.progress)
      if (frame.type === 'result') result = frame.result
      if (frame.type === 'error') throw new RequestError(frame.error, frame.status)
    }
    while (true) {
      const chunk = await reader.read()
      buffer += decoder.decode(chunk.value, { stream: !chunk.done })
      const lines = buffer.split('\n')
      buffer = lines.pop() ?? ''
      for (const line of lines) consume(line)
      if (chunk.done) break
    }
    consume(buffer)
    if (!result) {
      throw new RequestError({ code: 'QUESTION_STREAM_INCOMPLETE', message: '问答进度连接提前结束，请重试' }, 502)
    }
    return result
  },

  submitQuestionFeedback: (
    questionId: string,
    rating: 'ACCURATE' | 'INACCURATE',
    comment = '',
  ) => apiRequest<{ items: Array<{ id: string; queryPlanId: string; rating: string }> }>(
    `/v1/questions/${encodeURIComponent(questionId)}/feedback`,
    {
      method: 'POST',
      body: JSON.stringify({ rating, ...(comment.trim() ? { comment: comment.trim() } : {}) }),
    },
  ),

  cancelQuestion: (questionId: string) => apiRequest<{
    questionId: string
    state: SemanticQuestionState
    status: 'CANCELLED'
  }>(`/v1/questions/${encodeURIComponent(questionId)}/cancel`, {
    method: 'POST',
  }),

  tokenize: (question: string, signal?: AbortSignal) =>
    apiRequest<SemanticQueryTokenization>('/v1/semantic-qa/tokenize', {
      method: 'POST',
      signal,
      body: JSON.stringify({
        question,
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
      }),
    }),

  planTurn: async ({ question, priorQuestions, contextQueryPlanIds, confirmedMetricCodes, confirmedDecisions, semanticHints, signal, onProgress }: PlanTurnInput) => {
    const init: RequestInit = {
      method: 'POST',
      signal,
      body: JSON.stringify({
        question,
        maximumPathHops: 8,
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
        ...(priorQuestions?.length ? { priorQuestions } : {}),
        ...(contextQueryPlanIds?.length ? { contextQueryPlanIds } : {}),
        ...(confirmedMetricCodes?.length ? { confirmedMetricCodes } : {}),
        ...(confirmedDecisions?.length ? { confirmedDecisions } : {}),
        ...(semanticHints ? { semanticHints } : {}),
      }),
    }
    if (!onProgress) return apiRequest<SemanticQueryTurn>('/v1/semantic-qa/query-turns', init)
    const response = await apiResponse('/v1/semantic-qa/query-turns', {
      ...init,
      headers: { Accept: 'application/x-ndjson' },
    })
    if (!response.body || !response.headers.get('Content-Type')?.includes('application/x-ndjson')) {
      return response.json() as Promise<SemanticQueryTurn>
    }
    type StreamFrame =
      | { type: 'progress'; progress: SemanticQueryProgressEvent }
      | { type: 'result'; result: SemanticQueryTurn }
      | { type: 'error'; status: number; error: APIError }
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let result: SemanticQueryTurn | undefined
    const consume = (line: string) => {
      if (!line.trim()) return
      const frame = JSON.parse(line) as StreamFrame
      if (frame.type === 'progress') onProgress(frame.progress)
      if (frame.type === 'result') result = frame.result
      if (frame.type === 'error') throw new RequestError(frame.error, frame.status)
    }
    while (true) {
      const chunk = await reader.read()
      buffer += decoder.decode(chunk.value, { stream: !chunk.done })
      const lines = buffer.split('\n')
      buffer = lines.pop() ?? ''
      for (const line of lines) consume(line)
      if (chunk.done) break
    }
    consume(buffer)
    if (!result) {
      throw new RequestError({ code: 'QUERY_STREAM_INCOMPLETE', message: '问答进度连接提前结束，请重试' }, 502)
    }
    return result
  },

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

  submitFeedback: (planId: string, rating: 'ACCURATE' | 'INACCURATE', comment = '') =>
    apiRequest<{ id: string; queryPlanId: string; rating: string; comment?: string; createdAt: string; updatedAt: string }>(
      `/v1/semantic-qa/query-plans/${encodeURIComponent(planId)}/feedback`,
      {
        method: 'POST',
        body: JSON.stringify({ rating, ...(comment.trim() ? { comment: comment.trim() } : {}) }),
      },
    ),

  listGoldenQuestionSets: () =>
    apiRequest<{ items: GoldenQuestionSet[] } | GoldenQuestionSet[]>('/v1/semantic-qa/golden-question-sets')
      .then(result => Array.isArray(result) ? result : result.items),

  listGoldenQuestions: (setId: string) =>
    apiRequest<{ items: GoldenQuestion[] } | GoldenQuestion[]>(`/v1/semantic-qa/golden-questions?setId=${encodeURIComponent(setId)}`)
      .then(result => Array.isArray(result) ? result : result.items),

  getEvaluationReleaseGate: (setId: string) =>
    apiRequest<EvaluationReleaseGate>(`/v1/semantic-qa/golden-question-sets/${encodeURIComponent(setId)}/evaluation-gate`),

  replayGoldenQuestion: (id: string) =>
    apiRequest<GoldenQuestionReplay>(`/v1/semantic-qa/golden-questions/${id}/replay`, { method: 'POST', body: '{}' }),
}
