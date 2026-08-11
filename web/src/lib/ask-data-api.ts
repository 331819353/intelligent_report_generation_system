import { apiRequest, apiResponse, RequestError, type APIError } from './api.ts'

export type QuestionRunState =
  | 'RECEIVED' | 'AUTHORIZED' | 'CONTEXT_READY' | 'UNDERSTANDING' | 'RETRIEVING'
  | 'BINDING' | 'GRAPH_VALIDATING' | 'IR_READY' | 'PLAN_VALIDATING' | 'EXECUTING'
  | 'RESULT_VERIFYING' | 'ANSWER_VERIFYING' | 'CLARIFICATION_REQUIRED' | 'ANSWERED' | 'BLOCKED'
  | 'CLARIFICATION_EXPIRED'

export type QuestionDisposition = 'PENDING' | 'DIRECT' | 'CLARIFY' | 'REFUSE'
export type QuestionEventType =
  | 'STATE_TRANSITION' | 'LLM_DECISION' | 'TOOL_RESULT' | 'ARTIFACT_RECORDED'
  | 'BUDGET_UPDATED' | 'CORRECTION' | 'ERROR' | 'PROGRESS'
export type QuestionEventStatus = 'STARTED' | 'SUCCEEDED' | 'BLOCKED' | 'FAILED' | 'CANCELED'
export type QuestionArtifactType =
  | 'UNDERSTANDING' | 'CANDIDATE_SET' | 'BINDING_BUNDLE' | 'GRAPH_PLAN'
  | 'SEMANTIC_IR' | 'QUERY_PLAN' | 'RESULT_SUMMARY' | 'RESULT_VERIFICATION'
  | 'EVIDENCE' | 'ANSWER' | 'CLARIFICATION' | 'BLOCK'

export type ReleaseRef = { releaseId: string; contentHash: string }
export type ReleaseDescriptor = ReleaseRef & { semanticVersion: string; status: string }
export type ReleaseChange = {
  objectType: 'METRIC' | 'DIMENSION'
  objectId: string
  name: string
  fromVersion?: string
  toVersion?: string
  changeKind: 'ADDED' | 'REMOVED' | 'UPDATED'
  summary: string
}
export type ReleaseDrift = {
  conversationId: string
  pinnedAt?: string
  previous: ReleaseDescriptor
  active: ReleaseDescriptor
  changes: ReleaseChange[]
}
export type ReleasePinResult = { conversationId: string; release: ReleaseDescriptor; replayed: boolean }
export type ConversationSummary = {
  conversationId: string
  latestRunId: string
  label: string
  state: QuestionRunState
  pinned: boolean
  archived: boolean
  release: ReleaseRef
  releaseDrifted: boolean
  clarificationPending: boolean
  narrativeDegraded: boolean
  runCount: number
  recordVersion: number
  updatedAt: string
}
export type ConversationPage = { items: ConversationSummary[]; nextCursor?: string }
export type ConversationDetail = {
  conversation: ConversationSummary
  runs: QuestionRun[]
  nextRunCursor?: string
}
export type AddToReportResult = {
  intentId: string
  reportId: string
  runId: string
  status: 'PENDING_CONFIRMATION' | 'PENDING' | 'APPLIED'
  previewHash?: string
  replayed: boolean
}
export type RunHashes = {
  understandingHash?: string
  bindingBundleHash?: string
  graphPlanHash?: string
  semanticIrHash?: string
  queryPlanHash?: string
  resultHash?: string
}
export type BudgetLimits = {
  maxSteps: number
  maxLlmCalls: number
  maxToolCalls: number
  maxFormalQueries: number
  maxValidationQueries: number
  maxDurationMs: number
}
export type BudgetUsage = {
  stepCount: number
  llmCallsUsed: number
  toolCallsUsed: number
  formalQueriesUsed: number
  validationQueriesUsed: number
  elapsedMs: number
  exhausted: boolean
}
export type QuestionOperation = {
  runId: string
  conversationId: string
  state: QuestionRunState
  replayed: boolean
  eventsUrl: string
}
export type ClarificationEvidence = {
  definition: string
  owner: { id: string; displayName: string }
  semanticVersion: string
  semanticStatus: string
  time: { label: string; start: string; end: string; timezone: string }
  quality: {
    status: 'PASS' | 'WARNING' | 'FAIL' | 'UNKNOWN'
    scorePermillion?: number
    dataAsOf: string
    rulesPassed: number
    rulesTotal: number
  }
}
export type ClarificationOption = {
  optionId: string
  label: string
  difference?: string
  evidenceIds: string[]
  evidence?: ClarificationEvidence
}
export type ResultColumnType = 'STRING' | 'INTEGER' | 'DECIMAL' | 'DATE' | 'DATETIME'
export type ResultColumnRole = 'DIMENSION' | 'MEASURE'
export type ResultViewType = 'KPI' | 'LINE' | 'BAR' | 'TABLE'
export type ResolvedComparison = {
  type: 'YEAR_OVER_YEAR' | 'MONTH_OVER_MONTH' | 'QUARTER_OVER_QUARTER' | 'WEEK_OVER_WEEK' | 'PERIOD_OVER_PERIOD'
  periods: number
  alignment: 'SAME_DAY_COUNT' | 'SAME_CALENDAR_RANGE'
  resolvedStart: string
  resolvedEndExclusive: string
  overflowApplied: boolean
}
export type ResolvedTimeSpec = {
  requestedPeriod: string
  grain: 'DAY' | 'WEEK' | 'MONTH' | 'QUARTER' | 'YEAR' | 'FISCAL_MONTH' | 'FISCAL_QUARTER' | 'FISCAL_YEAR'
  policyApplied: 'MTD' | 'FULL_PERIOD' | 'LAST_COMPLETE'
  policySource: 'METRIC' | 'TIME_CONTRACT' | 'DOMAIN' | 'PLATFORM_DEFAULT'
  resolvedStart: string
  resolvedEndExclusive: string
  dataAvailableThrough: string
  truncatedByDataAvailability: boolean
  periodFallbackApplied: boolean
  timezone: string
  calendarVersionId?: string
  comparison?: ResolvedComparison
}
export type TimeSpecView = {
  rangeLabel: string
  asOfLabel: string
  policyLabel: string
  comparisonLabel: string
  truncatedHint: string
}
export type QuestionResultColumn = {
  key: string
  label: string
  type: ResultColumnType
  role: ResultColumnRole
}
export type QuestionResultDataset = {
  id: string
  label: string
  columns: QuestionResultColumn[]
  rows: Record<string, string | null>[]
  page: number
  pageSize: number
  totalRows: number
}
export type QuestionResultView = {
  id: string
  type: ResultViewType
  label: string
  datasetId: string
  dimensionKeys: string[]
  measureKeys: string[]
}
export type QuestionResult = {
  schemaVersion: 'question-result-v1'
  title: string
  resolvedTimeSpec: ResolvedTimeSpec
  timeSpec: TimeSpecView
  summary: {
    metricLabel: string
    value: string
    formattedValue: string
    unit: string
    comparison?: {
      label: string
      direction: 'UP' | 'DOWN' | 'FLAT'
      changePermillion: number
      formattedChange: string
      baselineStart: string
      baselineEnd: string
    }
    time: ClarificationEvidence['time']
  }
  evidenceIds: string[]
  evidence?: ClarificationEvidence
  datasets: QuestionResultDataset[]
  views: QuestionResultView[]
  defaultViewId: string
  recommendedViewId?: string
}
export type QuestionScopeVerdict = {
  schemaVersion: 'question-scope-verdict-v1'
  type:
    | 'METRIC_LOOKUP' | 'GROUPED_ANALYSIS' | 'FILTERED_ANALYSIS' | 'RANKING' | 'COMPARISON'
    | 'MULTI_METRIC' | 'RATIO_TARGET' | 'DEFINITION' | 'BUNDLE' | 'DETAIL_LIST' | 'FORECAST'
    | 'AD_HOC_FORMULA' | 'CAUSAL' | 'CROSS_DOMAIN' | 'UNGOVERNED_SOURCE'
  outcome: 'EXECUTE' | 'DEFINITION' | 'BUNDLE' | 'OUT_OF_SCOPE' | 'BLOCKED'
  reason: string
  userMessage?: string
  nextActions: Array<{
    kind: 'DATA_REQUEST' | 'METRIC_REQUEST' | 'REPHRASE'
    label: string
    payload: {
      target: 'DATA_REQUEST_DIALOG' | 'METRIC_REQUEST_FORM' | 'ASK_DATA_COMPOSER'
      prefill: 'CURRENT_QUESTION'
    }
  }>
  parsedContext?: {
    metricIds?: string[]
    dimensionIds?: string[]
    memberIds?: string[]
    timeRange?: { start: string; endExclusive: string; timezone: string; grain?: string }
  }
  lexiconVersion: string
  lexiconHash: string
  classificationSource: 'RULE' | 'LLM_FALLBACK' | 'RULE_FALLBACK_REJECTED'
}
export type QuestionCompletion = {
  code: string
  artifactType: QuestionArtifactType
  artifactHash: string
  evidenceIds: string[]
  answer?: QuestionAnswerPresentation
  clarification?: {
    clarificationId: string
    conflictCode?: string
    message?: string
    options: ClarificationOption[]
  }
  result?: QuestionResult
  scopeVerdict?: QuestionScopeVerdict
}
export type QuestionAnswerPresentation = {
  schemaVersion: '1.0'
  narrativeDegraded: boolean
  hint?: string
  verification: { attempts: number; passed: boolean }
  narrative?: { summary: string; findings: string[] }
}
export type QuestionRun = {
  runId: string
  conversationId: string
  parentRunId?: string
  state: QuestionRunState
  disposition: QuestionDisposition
  completion?: QuestionCompletion
  release: ReleaseRef
  hashes: RunHashes
  budget: { limits: BudgetLimits; usage: BudgetUsage }
  clarificationDeadline?: string
  budgetFrozenAt?: string
  budgetConsumed?: BudgetUsage
  recordVersion: number
  lastEventId: number
  createdAt: string
  updatedAt: string
  completedAt?: string
}
export type QuestionRunEvent = {
  eventId: string
  eventIndex: number
  runVersion: number
  state: QuestionRunState
  type: QuestionEventType
  stage?: string
  status: QuestionEventStatus
  code?: string
  actionHash?: string
  artifactHash?: string
  evidenceIds: string[]
	graphDegraded: boolean
  durationMs?: number
  createdAt: string
}

export type QuestionFeedbackRating = 'ACCURATE' | 'INACCURATE'
export type QuestionFeedbackIssueType =
  | 'NONE' | 'METRIC' | 'DIMENSION' | 'MEMBER' | 'TIME'
  | 'RELATIONSHIP' | 'DATA' | 'PERMISSION' | 'EXPRESSION' | 'OTHER'
export type QuestionFeedbackSubmission = {
  runId: string
  runVersion: number
  rating: QuestionFeedbackRating
  issueType: QuestionFeedbackIssueType
  comment: string
}
export type QuestionFeedbackReceipt = {
  feedbackId: string
  runId: string
  rating: QuestionFeedbackRating
  issueType: QuestionFeedbackIssueType
  recordVersion: number
  createdAt: string
  updatedAt: string
  replayed: boolean
}

export type AskDataErrorKind =
  | 'AUTHENTICATION' | 'RELEASE_UNAVAILABLE' | 'SCOPE_CHANGED' | 'RUN_NOT_FOUND'
  | 'CONFLICT' | 'RELEASE_DRIFT' | 'CLARIFICATION_EXPIRED'
  | 'INVALID_REQUEST' | 'STREAM' | 'NETWORK' | 'CANCELED' | 'SERVICE'

const errorMessages: Record<string, { kind: AskDataErrorKind; message: string; retryable: boolean }> = {
  QUESTION_AUTHENTICATION_REQUIRED: { kind: 'AUTHENTICATION', message: '登录状态已失效，请重新登录。', retryable: false },
  QUESTION_RELEASE_UNAVAILABLE: { kind: 'RELEASE_UNAVAILABLE', message: '当前业务域暂无可用的语义发布版本。', retryable: false },
  QUESTION_SCOPE_CHANGED: { kind: 'SCOPE_CHANGED', message: '权限或口径版本已变化，请刷新后重新提问。', retryable: false },
  QUESTION_RUN_NOT_FOUND: { kind: 'RUN_NOT_FOUND', message: '该问数运行不存在或当前账号无权查看。', retryable: false },
  QUESTION_IDEMPOTENCY_CONFLICT: { kind: 'CONFLICT', message: '请求标识已被其他问题使用，请重新提交。', retryable: false },
  QUESTION_RUN_CONFLICT: { kind: 'CONFLICT', message: '运行状态已变化，请刷新后重试。', retryable: true },
  QUESTION_CLARIFICATION_NOT_ACCEPTED: { kind: 'CONFLICT', message: '该运行已不再接受澄清选择。', retryable: false },
  QUESTION_CLARIFICATION_OPTION_INVALID: { kind: 'CONFLICT', message: '澄清选项已失效，请刷新运行状态。', retryable: false },
  QUESTION_CLARIFICATION_ALREADY_ANSWERED: { kind: 'CONFLICT', message: '该口径选择已提交，请加载最新运行。', retryable: false },
  RELEASE_DRIFT_CONFIRM_REQUIRED: { kind: 'RELEASE_DRIFT', message: '会话口径已更新，请确认后再发起新查询。', retryable: false },
  CLARIFICATION_EXPIRED: { kind: 'CLARIFICATION_EXPIRED', message: '本次澄清已超时，无法继续原选择。', retryable: false },
  QUESTION_FEEDBACK_NOT_ACCEPTED: { kind: 'CONFLICT', message: '该运行尚未结束，暂时不能提交反馈。', retryable: false },
  QUESTION_FEEDBACK_CONFLICT: { kind: 'CONFLICT', message: '反馈状态已变化，请刷新后重试。', retryable: true },
  QUESTION_INVALID_REQUEST: { kind: 'INVALID_REQUEST', message: '问题请求格式不正确。', retryable: false },
  QUESTION_EVENT_CURSOR_INVALID: { kind: 'STREAM', message: '事件游标无效，请重新加载运行。', retryable: false },
  QUESTION_EVENT_CURSOR_AHEAD: { kind: 'STREAM', message: '本地事件进度超前，请重新加载运行。', retryable: false },
  QUESTION_STREAM_REFRESH_FAILED: { kind: 'STREAM', message: '事件连接暂时中断，正在尝试恢复。', retryable: true },
  QUESTION_STREAM_REPLAY_INVALID: { kind: 'STREAM', message: '事件回放不一致，请重新加载运行。', retryable: false },
  QUESTION_STREAM_EVENT_GAP: { kind: 'STREAM', message: '事件序列不连续，请重新加载运行。', retryable: false },
  QUESTION_EVENT_PAYLOAD_REJECTED: { kind: 'STREAM', message: '事件内容未通过安全校验。', retryable: false },
  QUESTION_SERVICE_FAILED: { kind: 'SERVICE', message: '问数服务暂时不可用，请稍后重试。', retryable: true },
}

export class AskDataClientError extends Error {
  readonly kind: AskDataErrorKind
  readonly code: string
  readonly status?: number
  readonly retryable: boolean
  readonly detail?: APIError
  readonly releaseDrift?: ReleaseDrift

  constructor(input: {
    kind: AskDataErrorKind
    code: string
    message: string
    status?: number
    retryable?: boolean
    detail?: APIError
    releaseDrift?: ReleaseDrift
    cause?: unknown
  }) {
    super(input.message, { cause: input.cause })
    this.name = 'AskDataClientError'
    this.kind = input.kind
    this.code = input.code
    this.status = input.status
    this.retryable = input.retryable ?? false
    this.detail = input.detail
    this.releaseDrift = input.releaseDrift
  }
}

export function mapAskDataError(error: unknown): AskDataClientError {
  if (error instanceof AskDataClientError) return error
  if (error instanceof RequestError) {
    const mapped = errorMessages[error.detail.code]
    const releaseDrift = parseReleaseDrift(error.detail.releaseDrift)
    return new AskDataClientError({
      kind: mapped?.kind ?? (error.status >= 500 ? 'SERVICE' : 'INVALID_REQUEST'),
      code: error.detail.code || 'QUESTION_REQUEST_FAILED',
      message: mapped?.message ?? error.detail.message ?? '问数请求失败。',
      status: error.status,
      retryable: mapped?.retryable ?? error.status >= 500,
      detail: error.detail,
      releaseDrift,
      cause: error,
    })
  }
  if (isAbortError(error)) {
    return new AskDataClientError({ kind: 'CANCELED', code: 'QUESTION_REQUEST_CANCELED', message: '问数请求已取消。', cause: error })
  }
  return new AskDataClientError({
    kind: 'NETWORK', code: 'QUESTION_NETWORK_ERROR', message: '网络连接异常，请检查连接后重试。',
    retryable: true, cause: error,
  })
}

export function createIdempotencyKey(): string {
  if (typeof crypto === 'undefined' || typeof crypto.randomUUID !== 'function') {
    throw new AskDataClientError({ kind: 'SERVICE', code: 'QUESTION_CLIENT_CRYPTO_UNAVAILABLE', message: '浏览器无法生成安全请求标识。' })
  }
  return crypto.randomUUID()
}

function requireRunID(runId: string): string {
  const value = runId.trim()
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value)) {
    throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'QUESTION_RUN_ID_INVALID', message: '运行标识无效。' })
  }
  return value
}

function requireQuestion(question: string): string {
  const value = question.trim()
  if (!value || [...value].length > 4096) {
    throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'QUESTION_TEXT_INVALID', message: '请输入 1～4096 个字符的问题。' })
  }
  return value
}

export type ClarificationSubmission = {
  runId: string
  clarificationId: string
  optionId: string
  runVersion: number
}

export function buildClarificationSubmission(input: ClarificationSubmission): ClarificationSubmission {
  const optionId = input.optionId.trim()
  if (!optionId || optionId.length > 128) {
    throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'QUESTION_OPTION_ID_INVALID', message: '澄清选项无效。' })
  }
  if (!Number.isSafeInteger(input.runVersion) || input.runVersion < 1) {
    throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'QUESTION_RUN_VERSION_INVALID', message: '运行版本无效。' })
  }
  return {
    runId: requireRunID(input.runId),
    clarificationId: requireRunID(input.clarificationId),
    optionId,
    runVersion: input.runVersion,
  }
}

const questionFeedbackIssueTypes = new Set<QuestionFeedbackIssueType>([
  'NONE', 'METRIC', 'DIMENSION', 'MEMBER', 'TIME', 'RELATIONSHIP',
  'DATA', 'PERMISSION', 'EXPRESSION', 'OTHER',
])

export function buildFeedbackSubmission(input: QuestionFeedbackSubmission): QuestionFeedbackSubmission {
  if (!Number.isSafeInteger(input.runVersion) || input.runVersion < 1) {
    throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'QUESTION_RUN_VERSION_INVALID', message: '运行版本无效。' })
  }
  if (!questionFeedbackIssueTypes.has(input.issueType) ||
    input.rating === 'ACCURATE' && input.issueType !== 'NONE' ||
    input.rating === 'INACCURATE' && input.issueType === 'NONE') {
    throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'QUESTION_FEEDBACK_SHAPE_INVALID', message: '请选择与评价匹配的问题类型。' })
  }
  const comment = input.comment.trim()
  if ([...comment].length > 2000 || [...comment].some(character => {
    const code = character.charCodeAt(0)
    return code < 32 || code === 127
  })) {
    throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'QUESTION_FEEDBACK_COMMENT_INVALID', message: '反馈说明不能超过 2000 字，且不能包含控制字符。' })
  }
  return {
    runId: requireRunID(input.runId), runVersion: input.runVersion,
    rating: input.rating, issueType: input.issueType, comment,
  }
}

export const questionAPI = {
  create: (input: { question: string; conversationId?: string; idempotencyKey?: string; signal?: AbortSignal }) =>
    apiRequest<QuestionOperation>('/v1/questions', {
      method: 'POST', signal: input.signal,
      headers: { 'Idempotency-Key': input.idempotencyKey ?? createIdempotencyKey() },
      body: JSON.stringify({
        question: requireQuestion(input.question),
        ...(input.conversationId ? { conversationId: input.conversationId } : {}),
      }),
    }),

  get: (runId: string, signal?: AbortSignal) =>
    apiRequest<QuestionRun>(`/v1/questions/${encodeURIComponent(requireRunID(runId))}`, { signal }),

  clarify: (input: {
    runId: string
    clarificationId: string
    optionId: string
    runVersion: number
    idempotencyKey?: string
    signal?: AbortSignal
  }) => {
    const submission = buildClarificationSubmission(input)
    return apiRequest<QuestionOperation>(`/v1/questions/${encodeURIComponent(submission.runId)}/clarifications`, {
      method: 'POST', signal: input.signal,
      headers: { 'Idempotency-Key': input.idempotencyKey ?? createIdempotencyKey() },
      body: JSON.stringify({
        clarificationId: submission.clarificationId,
        optionId: submission.optionId,
        runVersion: submission.runVersion,
      }),
    })
  },

  confirmReleaseDrift: (input: {
    conversationId: string
    previousReleaseId: string
    activeReleaseId: string
    idempotencyKey?: string
    signal?: AbortSignal
  }) => apiRequest<ReleasePinResult>(`/v1/conversations/${encodeURIComponent(requireRunID(input.conversationId))}/release-drift`, {
    method: 'POST', signal: input.signal,
    headers: { 'Idempotency-Key': input.idempotencyKey ?? createIdempotencyKey() },
    body: JSON.stringify({
      previousReleaseId: requireRunID(input.previousReleaseId),
      activeReleaseId: requireRunID(input.activeReleaseId),
    }),
  }),

  feedback: (input: QuestionFeedbackSubmission & { signal?: AbortSignal }) => {
    const submission = buildFeedbackSubmission(input)
    return apiRequest<QuestionFeedbackReceipt>(`/v1/questions/${encodeURIComponent(submission.runId)}/feedback`, {
      method: 'POST', signal: input.signal,
      body: JSON.stringify({
        rating: submission.rating,
        issueType: submission.issueType,
        comment: submission.comment,
        runVersion: submission.runVersion,
      }),
    })
  },

  listConversations(input: { search?: string; archived?: boolean; limit?: number; cursor?: string; signal?: AbortSignal } = {}) {
    const query = new URLSearchParams({ limit: String(input.limit ?? 50) })
    if (input.search?.trim()) query.set('search', input.search.trim())
    if (input.archived) query.set('archived', 'true')
    if (input.cursor) query.set('cursor', input.cursor)
    return apiRequest<ConversationPage>(`/v1/conversations?${query}`, { signal: input.signal })
  },

  getConversation(conversationId: string, input: { runLimit?: number; runCursor?: string; signal?: AbortSignal } = {}) {
    const query = new URLSearchParams({ runLimit: String(input.runLimit ?? 50) })
    if (input.runCursor) query.set('runCursor', input.runCursor)
    return apiRequest<ConversationDetail>(`/v1/conversations/${encodeURIComponent(requireRunID(conversationId))}?${query}`, { signal: input.signal })
  },

  mutateConversation(conversationId: string, action: 'pin' | 'unpin' | 'archive' | 'restore', expectedVersion: number) {
    return apiRequest<ConversationSummary>(`/v1/conversations/${encodeURIComponent(requireRunID(conversationId))}/${action}`, {
      method: 'POST', headers: { 'Idempotency-Key': createIdempotencyKey() }, body: JSON.stringify({ expectedVersion }),
    })
  },

  renameConversation(conversationId: string, expectedVersion: number, label: string) {
    const normalized = label.trim()
    if (!normalized || normalized !== label || [...normalized].length > 120) {
      throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'CONVERSATION_LABEL_INVALID', message: '会话名称需为 1～120 个字符，且首尾不能有空格。' })
    }
    return apiRequest<ConversationSummary>(`/v1/conversations/${encodeURIComponent(requireRunID(conversationId))}/rename`, {
      method: 'POST', headers: { 'Idempotency-Key': createIdempotencyKey() }, body: JSON.stringify({ expectedVersion, label: normalized }),
    })
  },

  addToReport(input: { runId: string; reportId: string; runVersion: number; targetPageId?: string; targetSectionId?: string }) {
    return apiRequest<AddToReportResult>(`/v1/questions/${encodeURIComponent(requireRunID(input.runId))}/add-to-report`, {
      method: 'POST', headers: { 'Idempotency-Key': createIdempotencyKey() }, body: JSON.stringify({
        reportId: requireRunID(input.reportId), runVersion: input.runVersion,
        ...(input.targetPageId ? { targetPageId: input.targetPageId } : {}),
        ...(input.targetSectionId ? { targetSectionId: input.targetSectionId } : {}),
      }),
    })
  },

  getAddToReportIntent(intentId: string) {
    return apiRequest<AddToReportResult>(`/v1/add-to-report-intents/${encodeURIComponent(requireRunID(intentId))}`)
  },

  confirmAddToReport(intentId: string, previewHash: string) {
    if (!/^[0-9a-f]{64}$/.test(previewHash)) {
      throw new AskDataClientError({ kind: 'INVALID_REQUEST', code: 'ADD_TO_REPORT_PREVIEW_INVALID', message: '报告变更预览已失效，请重新生成。' })
    }
    return apiRequest<AddToReportResult>(`/v1/add-to-report-intents/${encodeURIComponent(requireRunID(intentId))}/confirm`, {
      method: 'POST', body: JSON.stringify({ previewHash }),
    })
  },
}

export type SSEFrame = { event: string; id?: string; data: string; retry?: number }

// Incremental decoder for fetch-based SSE. EventSource cannot attach this
// application's bearer token and business-domain header, so WEB-002 uses the
// shared authenticated fetch path and parses the bounded stream explicitly.
export class SSEDecoder {
  private buffer = ''
  private event = ''
  private id: string | undefined
  private data: string[] = []
  private retry: number | undefined

  push(chunk: string): SSEFrame[] {
    this.buffer += chunk
    if (this.buffer.length > 64 << 10) throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
    const frames: SSEFrame[] = []
    let newline = this.buffer.indexOf('\n')
    while (newline >= 0) {
      let line = this.buffer.slice(0, newline)
      this.buffer = this.buffer.slice(newline + 1)
      if (line.endsWith('\r')) line = line.slice(0, -1)
      this.consumeLine(line, frames)
      newline = this.buffer.indexOf('\n')
    }
    return frames
  }

  finish(): SSEFrame[] {
    const frames: SSEFrame[] = []
    if (this.buffer) this.consumeLine(this.buffer.endsWith('\r') ? this.buffer.slice(0, -1) : this.buffer, frames)
    this.buffer = ''
    this.dispatch(frames)
    return frames
  }

  private consumeLine(line: string, frames: SSEFrame[]) {
    if (line === '') {
      this.dispatch(frames)
      return
    }
    if (line.startsWith(':')) return
    const separator = line.indexOf(':')
    const field = separator < 0 ? line : line.slice(0, separator)
    let value = separator < 0 ? '' : line.slice(separator + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    switch (field) {
      case 'event': this.event = value; break
      case 'id': if (!value.includes('\0')) this.id = value; break
      case 'data': this.data.push(value); break
      case 'retry': if (/^[0-9]{1,5}$/.test(value)) this.retry = Number(value); break
    }
  }

  private dispatch(frames: SSEFrame[]) {
    if (this.data.length || this.retry !== undefined) {
      frames.push({ event: this.event || 'message', id: this.id, data: this.data.join('\n'), retry: this.retry })
    }
    this.event = ''
    this.data = []
    this.retry = undefined
  }
}

export type QuestionStreamStatus = 'CONNECTING' | 'OPEN' | 'RECONNECTING' | 'CLOSED' | 'CANCELED'
export type QuestionStreamOutcome = { lastEventId: number; terminal: boolean; canceled: boolean }
export type QuestionEventSubscription = { cancel: () => void; done: Promise<QuestionStreamOutcome> }
type StreamRequest = (path: string, init?: RequestInit) => Promise<Response>

export type SubscribeQuestionEventsOptions = {
  lastEventId?: number
  signal?: AbortSignal
  maxReconnects?: number
  onEvent?: (event: QuestionRunEvent) => void
  onStatus?: (status: QuestionStreamStatus) => void
  onError?: (error: AskDataClientError) => void
  request?: StreamRequest
  wait?: (milliseconds: number, signal: AbortSignal) => Promise<void>
}

const terminalStates = new Set<QuestionRunState>(['ANSWERED', 'CLARIFICATION_REQUIRED', 'CLARIFICATION_EXPIRED', 'BLOCKED'])
const retryableStreamCodes = new Set(['QUESTION_STREAM_REFRESH_FAILED'])

export function subscribeQuestionEvents(
  runId: string,
  options: SubscribeQuestionEventsOptions = {},
): QuestionEventSubscription {
  const normalizedRunID = requireRunID(runId)
  let cursor = options.lastEventId ?? 0
  if (!Number.isInteger(cursor) || cursor < 0 || cursor > 1_000_000) {
    throw streamContractError('QUESTION_EVENT_CURSOR_INVALID')
  }
  const controller = new AbortController()
  const maxReconnects = options.maxReconnects ?? 5
  const request = options.request ?? ((path, init) => apiResponse(path, init))
  const wait = options.wait ?? waitForReconnect
  let terminal = false
  let canceled = false
  let retryMilliseconds = 1000
  let reconnects = 0
  const abortFromCaller = () => controller.abort(options.signal?.reason)
  if (options.signal?.aborted) abortFromCaller()
  else options.signal?.addEventListener('abort', abortFromCaller, { once: true })

  const acceptFrame = (frame: SSEFrame) => {
    if (frame.retry !== undefined) retryMilliseconds = Math.min(30_000, Math.max(250, frame.retry))
    if (!frame.data) return
    if (new TextEncoder().encode(frame.data).byteLength > 16 << 10) {
      throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
    }
    if (frame.event === 'question.error') {
      const code = parseStreamErrorCode(frame.data)
      throw streamContractError(code, retryableStreamCodes.has(code))
    }
    if (!questionEventNames.has(frame.event)) throw streamContractError('QUESTION_STREAM_EVENT_UNKNOWN')
    const event = parseQuestionRunEvent(frame.data)
    if ((frame.event === 'answer.degraded' && event.code !== 'ANSWER_DEGRADED') ||
      (frame.event === 'answer.verifying' && event.state !== 'ANSWER_VERIFYING')) {
      throw streamContractError('QUESTION_STREAM_EVENT_PAYLOAD_REJECTED')
    }
    if (frame.id !== String(event.eventIndex)) throw streamContractError('QUESTION_STREAM_REPLAY_INVALID')
    if (event.eventIndex <= cursor) return
    if (event.eventIndex !== cursor + 1) throw streamContractError('QUESTION_STREAM_EVENT_GAP')
    cursor = event.eventIndex
    options.onEvent?.(event)
    terminal = terminalStates.has(event.state)
  }

  const run = async (): Promise<QuestionStreamOutcome> => {
    try {
      while (!terminal) {
        options.onStatus?.(reconnects === 0 ? 'CONNECTING' : 'RECONNECTING')
        try {
          const response = await request(`/v1/questions/${encodeURIComponent(normalizedRunID)}/events`, {
            method: 'GET', signal: controller.signal,
            headers: { Accept: 'text/event-stream', ...(cursor ? { 'Last-Event-ID': String(cursor) } : {}) },
          })
          if (!response.body) throw streamContractError('QUESTION_STREAM_BODY_MISSING', true)
          options.onStatus?.('OPEN')
          const reader = response.body.getReader()
          const text = new TextDecoder()
          const decoder = new SSEDecoder()
          while (true) {
            const part = await reader.read()
            if (part.done) break
            for (const frame of decoder.push(text.decode(part.value, { stream: true }))) acceptFrame(frame)
          }
          for (const frame of decoder.push(text.decode())) acceptFrame(frame)
          for (const frame of decoder.finish()) acceptFrame(frame)
          if (terminal) break
          throw streamContractError('QUESTION_STREAM_DISCONNECTED', true)
        } catch (error) {
          if (controller.signal.aborted || isAbortError(error)) throw error
          const mapped = mapAskDataError(error)
          if (!mapped.retryable || reconnects >= maxReconnects) throw mapped
          reconnects += 1
          await wait(Math.min(30_000, retryMilliseconds * 2 ** (reconnects - 1)), controller.signal)
        }
      }
      options.onStatus?.('CLOSED')
      return { lastEventId: cursor, terminal, canceled: false }
    } catch (error) {
      const mapped = mapAskDataError(error)
      if (mapped.kind === 'CANCELED') {
        canceled = true
        options.onStatus?.('CANCELED')
        return { lastEventId: cursor, terminal: false, canceled: true }
      }
      options.onError?.(mapped)
      throw mapped
    } finally {
      options.signal?.removeEventListener('abort', abortFromCaller)
    }
  }

  return {
    cancel: () => { canceled = true; controller.abort() },
    done: run().then(outcome => canceled ? { ...outcome, canceled: true } : outcome),
  }
}

function parseQuestionRunEvent(raw: string): QuestionRunEvent {
  let value: unknown
  try { value = JSON.parse(raw) } catch { throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED') }
  if (!isRecord(value)) throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
  const eventIndex = boundedInteger(value.eventIndex, 1, 1_000_000)
  const runVersion = boundedInteger(value.runVersion, 1, Number.MAX_SAFE_INTEGER)
  const state = enumString(value.state, questionStates)
  const type = enumString(value.type, questionEventTypes)
  const status = enumString(value.status, questionEventStatuses)
  const eventId = boundedString(value.eventId, 1, 128)
  const createdAt = boundedString(value.createdAt, 1, 64)
  if (Number.isNaN(Date.parse(createdAt))) throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
  const evidenceIds = stringArray(value.evidenceIds, 64, 128)
	if (typeof value.graphDegraded !== 'boolean') throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
	const event: QuestionRunEvent = {
		eventId, eventIndex, runVersion, state, type, status, evidenceIds,
		graphDegraded: value.graphDegraded, createdAt,
	}
  for (const [key, max] of [['stage', 64], ['code', 128]] as const) {
    if (value[key] !== undefined) event[key] = boundedString(value[key], 1, max) as never
  }
  for (const key of ['actionHash', 'artifactHash'] as const) {
    if (value[key] !== undefined) {
      const hash = boundedString(value[key], 64, 64)
      if (!/^[0-9a-f]{64}$/.test(hash)) throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
      event[key] = hash
    }
  }
  if (value.durationMs !== undefined) event.durationMs = boundedInteger(value.durationMs, 0, 600_000)
  return event
}

function parseStreamErrorCode(raw: string): string {
  let value: unknown
  try { value = JSON.parse(raw) } catch { throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED') }
  if (!isRecord(value) || typeof value.code !== 'string' || !/^[A-Z][A-Z0-9_]{0,127}$/.test(value.code)) {
    throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
  }
  return value.code
}

function streamContractError(code: string, retryable = false): AskDataClientError {
  const mapped = errorMessages[code]
  return new AskDataClientError({
    kind: mapped?.kind ?? 'STREAM', code,
    message: mapped?.message ?? '事件连接未通过一致性校验。',
    retryable: mapped?.retryable ?? retryable,
  })
}

function waitForReconnect(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(resolve, milliseconds)
    signal.addEventListener('abort', () => {
      window.clearTimeout(timer)
      reject(new DOMException('aborted', 'AbortError'))
    }, { once: true })
  })
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function boundedString(value: unknown, min: number, max: number): string {
  if (typeof value !== 'string' || value.length < min || value.length > max ||
    [...value].some(character => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127)) {
    throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
  }
  return value
}

function boundedInteger(value: unknown, min: number, max: number): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < min || value > max) {
    throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
  }
  return value
}

function enumString<T extends string>(value: unknown, values: ReadonlySet<T>): T {
  if (typeof value !== 'string' || !values.has(value as T)) throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
  return value as T
}

function stringArray(value: unknown, maxItems: number, maxLength: number): string[] {
  if (!Array.isArray(value) || value.length > maxItems) throw streamContractError('QUESTION_EVENT_PAYLOAD_REJECTED')
  return value.map(item => boundedString(item, 1, maxLength))
}

const questionStates = new Set<QuestionRunState>([
  'RECEIVED', 'AUTHORIZED', 'CONTEXT_READY', 'UNDERSTANDING', 'RETRIEVING', 'BINDING',
  'GRAPH_VALIDATING', 'IR_READY', 'PLAN_VALIDATING', 'EXECUTING', 'RESULT_VERIFYING',
  'ANSWER_VERIFYING', 'CLARIFICATION_REQUIRED', 'CLARIFICATION_EXPIRED', 'ANSWERED', 'BLOCKED',
])
const questionEventNames = new Set(['question.run', 'answer.verifying', 'answer.degraded'])

function parseReleaseDrift(value: unknown): ReleaseDrift | undefined {
  if (!isRecord(value) || typeof value.conversationId !== 'string' ||
    !isRecord(value.previous) || !isRecord(value.active) || !Array.isArray(value.changes)) return undefined
  const descriptor = (candidate: Record<string, unknown>): ReleaseDescriptor | undefined => {
    if (typeof candidate.releaseId !== 'string' || typeof candidate.contentHash !== 'string' ||
      typeof candidate.semanticVersion !== 'string' || typeof candidate.status !== 'string' ||
      !/^[0-9a-f]{64}$/.test(candidate.contentHash)) return undefined
    return {
      releaseId: candidate.releaseId, contentHash: candidate.contentHash,
      semanticVersion: candidate.semanticVersion, status: candidate.status,
    }
  }
  const previous = descriptor(value.previous)
  const active = descriptor(value.active)
  if (!previous || !active || value.changes.length > 20) return undefined
  const changes: ReleaseChange[] = []
  for (const item of value.changes) {
    if (!isRecord(item) || (item.objectType !== 'METRIC' && item.objectType !== 'DIMENSION') ||
      typeof item.objectId !== 'string' || typeof item.name !== 'string' || typeof item.summary !== 'string' ||
      (item.changeKind !== 'ADDED' && item.changeKind !== 'REMOVED' && item.changeKind !== 'UPDATED') ||
      item.fromVersion !== undefined && typeof item.fromVersion !== 'string' ||
      item.toVersion !== undefined && typeof item.toVersion !== 'string') return undefined
    changes.push({
      objectType: item.objectType, objectId: item.objectId, name: item.name,
      changeKind: item.changeKind, summary: item.summary,
      ...(item.fromVersion ? { fromVersion: item.fromVersion } : {}),
      ...(item.toVersion ? { toVersion: item.toVersion } : {}),
    })
  }
  return {
    conversationId: value.conversationId,
    ...(typeof value.pinnedAt === 'string' ? { pinnedAt: value.pinnedAt } : {}),
    previous, active, changes,
  }
}
const questionEventTypes = new Set<QuestionEventType>([
  'STATE_TRANSITION', 'LLM_DECISION', 'TOOL_RESULT', 'ARTIFACT_RECORDED',
  'BUDGET_UPDATED', 'CORRECTION', 'ERROR', 'PROGRESS',
])
const questionEventStatuses = new Set<QuestionEventStatus>(['STARTED', 'SUCCEEDED', 'BLOCKED', 'FAILED', 'CANCELED'])
