import { apiRequest } from './api.ts'
import { requestDatasetAIStream, type DatasetAIProgressEvent } from './dataset-ai-stream.ts'

// The persisted modeling session is the server-side source of truth for one AI
// modeling conversation: the confirmed goal/type/table scope, every clarification
// asked and answered, and each proposal's lifecycle. The client records decisions
// through these APIs instead of stitching them into instruction prose, and can
// rebuild a readable conversation from the session after a reload.

export type DatasetAIScopedTable = { tableId: string; source: 'RETRIEVED' | 'USER_ADDED'; role?: 'PRIMARY' | 'DIMENSION' }
export type DatasetAISessionModelKind = 'DIM' | 'DWD' | 'DWS' | 'ADS'

// --- Modeling workflow blueprint (docs/10 §2, server: internal/datasetai/workflow.go) ---

export type DatasetAIWorkflowStage =
  | 'INTAKE' | 'KIND' | 'GRAIN' | 'METRIC_DEFINITION' | 'PRIMARY_SOURCE' | 'DIMENSION_SOURCE'
  | 'JOIN' | 'METRIC_BINDING' | 'TRANSFORM' | 'FILTER' | 'OUTPUT' | 'GENERATE'
export type DatasetAIBlueprintStage = 'GRAIN' | 'METRIC_DEFINITION' | 'JOIN' | 'METRIC_BINDING' | 'TRANSFORM' | 'FILTER' | 'OUTPUT'
export type DatasetAIStageStatus = 'PROPOSED' | 'AUTO_CONFIRMED' | 'USER_CONFIRMED' | 'SKIPPED'
export type DatasetAIFieldRef = { tableId: string; column: string }
export type DatasetAIGrainDecision = { description: string; keys: string[]; timeField?: DatasetAIFieldRef; timeGrain?: '' | 'DAY' | 'WEEK' | 'MONTH' | 'QUARTER' | 'YEAR' }
export type DatasetAIMetricDefinition = { id: string; name: string; definition: string; origin: 'REGISTRY' | 'NEW'; registryCode?: string }
export type DatasetAIJoinKey = { leftColumn: string; rightColumn: string }
export type DatasetAIJoinDecision = {
  id: string; leftTableId: string; rightTableId: string; joinType: 'INNER' | 'LEFT'; keys: DatasetAIJoinKey[]
  cardinality?: string; provenance: string; reason?: string
  alternatives?: Array<{ keys: DatasetAIJoinKey[]; reason: string }>
}
export type DatasetAIMetricAggregation = 'NONE' | 'SUM' | 'AVG' | 'COUNT' | 'COUNT_DISTINCT' | 'MIN' | 'MAX'
export type DatasetAIMetricBinding = {
  metricId: string
  mode?: 'AGGREGATE' | 'PASSTHROUGH' | 'DERIVED'
  tableId?: string
  column?: string
  inputs?: DatasetAIFieldRef[]
  operation?: 'ADD' | 'SUBTRACT' | 'MULTIPLY' | 'DIVIDE'
  aggregation: DatasetAIMetricAggregation
  distinct: boolean
  note?: string
}
export type DatasetAITransformDecision = { componentType: string; operation?: string; inputs: DatasetAIFieldRef[]; description: string; placement: 'BEFORE_GROUP' | 'AFTER_GROUP' }
export type DatasetAIFilterDecision = { tableId: string; column: string; operator: string; value: string; valueMode: 'LITERAL' | 'FIELD' }
export type DatasetAIOutputDecision = { name: string; code: string; source?: DatasetAIFieldRef; metricId?: string }
export type DatasetAIStageDecision = {
  stage: DatasetAIBlueprintStage
  status: DatasetAIStageStatus
  source: 'RULE' | 'RETRIEVAL' | 'LLM' | 'USER'
  confidence: number
  needsUserConfirmation: boolean
  reason?: string
  grain?: DatasetAIGrainDecision
  metrics?: DatasetAIMetricDefinition[]
  joins?: DatasetAIJoinDecision[]
  bindings?: DatasetAIMetricBinding[]
  transforms?: DatasetAITransformDecision[]
  filters?: DatasetAIFilterDecision[]
  outputs?: DatasetAIOutputDecision[]
  decidedAt: string
}
export type DatasetAIKnowledgeSummary = {
  available: boolean
  terms?: number; metrics?: number; dimensions?: number; relationships?: number
  metricCodes?: string[]
  degraded?: boolean; degradedReason?: string
}
export type DatasetAIBlueprintRevision = { instruction: string; summary?: string; changedStages: DatasetAIBlueprintStage[]; at: string }
export type DatasetAIBlueprint = {
	phase?: 'BUSINESS' | 'PHYSICAL'
  requestId?: string
  promptVersion?: string
  summary?: string
  generatedAt: string
  stages: DatasetAIStageDecision[]
  knowledge?: DatasetAIKnowledgeSummary
  revisions?: DatasetAIBlueprintRevision[]
}
// --- Evidence-based source screening (server: internal/datasetai/screening.go) ---
export type DatasetAIScreeningJoinHint = {
  anchorTableId: string; anchorColumn: string; column: string
  sampleCompatibility: 'SAMPLE_MATCH' | 'COMPATIBLE' | 'FORMAT_MISMATCH' | 'UNKNOWN'
  sampleOverlap?: number; note?: string
}
export type DatasetAITableVerdict = {
  tableId: string; layer?: string
  verdict: 'SELECTED' | 'UNSURE' | 'REJECTED' | 'LIKELY' | 'POSSIBLE'
  confidence: number; reason: string; joinHints?: DatasetAIScreeningJoinHint[]
}
export type DatasetAISourceScreening = {
  role: 'PRIMARY' | 'DIMENSION'
  requestId?: string; anchorTableIds?: string[]
  poolSize: number; chunkCount: number; shortlistSize: number
  selected: DatasetAITableVerdict[]; uncertain: DatasetAITableVerdict[]; rejectedCount: number
  needsUserConfirmation: boolean; reason?: string
  truncated?: boolean; sampleEvidence: boolean; degraded?: boolean; degradedReason?: string
  generatedAt: string
}

export type DatasetAIStageResolution = {
  stage?: DatasetAIBlueprintStage
  action: 'CONFIRM' | 'SKIP' | 'REOPEN' | 'CONFIRM_ALL'
  reason?: string
  decision?: Partial<DatasetAIStageDecision>
}

export const datasetAIBlueprintStageOrder: DatasetAIBlueprintStage[] = ['GRAIN', 'METRIC_DEFINITION', 'JOIN', 'METRIC_BINDING', 'TRANSFORM', 'FILTER', 'OUTPUT']
export const datasetAIStageLabels: Record<DatasetAIWorkflowStage, string> = {
  INTAKE: '需求理解', KIND: '模型类型', GRAIN: '粒度与时间口径', METRIC_DEFINITION: '指标定义',
  PRIMARY_SOURCE: '主来源表', DIMENSION_SOURCE: '维度表', JOIN: '关联关系', METRIC_BINDING: '指标实现口径',
  TRANSFORM: '字段转换', FILTER: '过滤条件', OUTPUT: '输出字段', GENERATE: '生成 DAG',
}

/** Stages that still block generation: proposed and not yet confirmed. */
export function pendingDatasetAIBlueprintStages(blueprint: DatasetAIBlueprint | undefined): DatasetAIBlueprintStage[] {
  return (blueprint?.stages ?? []).filter(stage => stage.status === 'PROPOSED').map(stage => stage.stage)
}

export type DatasetAISessionClarification = {
  question: string
  candidates?: Array<{ componentKind: string; componentId: string; componentName: string }>
  askedAt: string
  answer?: string
  selectedComponent?: { componentKind: string; componentId: string }
  answeredAt?: string
}

export type DatasetAIExecutionWarning = { code: string; message?: string; joinId?: string }
/** Preview outcome of an applied proposal: counts and warning codes only, never values. */
export type DatasetAIProposalExecution = {
  rowCount: number
  durationMs?: number
  warnings?: DatasetAIExecutionWarning[]
  error?: string
  executedAt?: string
}
export type DatasetAIExecutionFinding = { code: string; message: string; joinId?: string; requestId?: string; at: string }

export type DatasetAISessionProposal = {
  requestId: string
  mode: 'CREATE' | 'MODIFY'
  summary: string
  instruction: string
  status: 'STAGED' | 'SUPERSEDED' | 'APPLIED' | 'REVERTED'
  createdAt: string
  updatedAt: string
  execution?: DatasetAIProposalExecution
}

export type DatasetAISessionState = {
  domainId?: string
  goal?: string
  modelKind?: DatasetAISessionModelKind
  modelKindSource?: 'KEYWORD_RULE' | 'LLM_INTENT' | 'USER_CONFIRMED'
	intent?: {
		entities?: string[]; measures?: string[]; dimensions?: string[]; timeExpressions?: string[]; filters?: string[]
	}
  scope?: {
    tables: DatasetAIScopedTable[]
    autoConfirmed?: boolean
    reason?: string
    confirmedAt: string
		sourceDecisions?: Array<{
			role: 'PRIMARY' | 'DIMENSION'; tableIds: string[]; status: DatasetAIStageStatus
			autoConfirmed?: boolean; reason?: string; confirmedAt: string
		}>
  }
  blueprint?: DatasetAIBlueprint
  clarifications?: DatasetAISessionClarification[]
  proposals?: DatasetAISessionProposal[]
  executionFindings?: DatasetAIExecutionFinding[]
  sourceScreenings?: DatasetAISourceScreening[]
}

export type DatasetAISession = {
  id: string
  datasetId?: string
  status: 'ACTIVE' | 'CLOSED'
  revision: number
  state: DatasetAISessionState
  createdAt: string
  updatedAt: string
}

export type DatasetAIScopeConfirmation = {
  goal: string
  modelKind: DatasetAISessionModelKind
  modelKindSource: 'KEYWORD_RULE' | 'LLM_INTENT' | 'USER_CONFIRMED'
  autoConfirmed: boolean
  reason: string
  tables: DatasetAIScopedTable[]
}

export type DatasetAIIntakeClassification = {
  modelKind: DatasetAISessionModelKind | 'UNKNOWN'
  reason: string
}

export type DatasetAISessionIntentRequest = {
	goal: string
	modelKind: DatasetAISessionModelKind
	modelKindSource: 'KEYWORD_RULE' | 'LLM_INTENT' | 'USER_CONFIRMED'
}

/**
 * Open a modeling session. The business domain scopes governed-knowledge lookups
 * (terms, metrics, relationships); for a saved dataset the server falls back to
 * the dataset's own domain when none is given.
 */
export function openDatasetAISession(datasetId?: string, domainId?: string): Promise<DatasetAISession> {
  const path = datasetId
    ? `/v1/datasets/${encodeURIComponent(datasetId)}/ai/session`
    : '/v1/datasets/ai/sessions'
  return apiRequest<DatasetAISession>(path, { method: 'POST', body: JSON.stringify(domainId ? { domainId } : {}) })
}

export function fetchActiveDatasetAISession(datasetId: string): Promise<DatasetAISession> {
  return apiRequest<DatasetAISession>(`/v1/datasets/${encodeURIComponent(datasetId)}/ai/session`)
}

export function confirmDatasetAISessionScope(
  sessionId: string,
  scope: DatasetAIScopeConfirmation,
): Promise<DatasetAISession> {
  return apiRequest<DatasetAISession>(`/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/scope`, {
    method: 'POST',
    body: JSON.stringify(scope),
  })
}

/**
 * Resolve business language, governed knowledge, grain and metric definitions
 * before any physical source table is suggested.
 */
export function prepareDatasetAISessionIntent(
	sessionId: string,
	input: DatasetAISessionIntentRequest,
): Promise<DatasetAISession> {
	return apiRequest<DatasetAISession>(`/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/intent`, {
		method: 'POST',
		body: JSON.stringify(input),
	})
}

/**
 * Run the blueprint turn: one structured model call that fixes grain, metrics, joins,
 * transforms, filters and outputs as per-stage decisions on the session. Progress
 * frames are forwarded so the dock can show the same log as DAG generation.
 */
export function generateDatasetAIBlueprint(
  sessionId: string,
  onProgress?: (event: DatasetAIProgressEvent) => void,
): Promise<DatasetAISession> {
  return requestDatasetAIStream<DatasetAISession>(
    `/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/blueprint`,
    { method: 'POST', body: '{}' },
    'session',
    onProgress,
  )
}

/**
 * Chunked, evidence-based screening of the whole eligible table pool for one
 * source role (docs/10 §7): every table is judged in pages, the shortlist is
 * re-judged with columns + sample rows (+ sample-key compatibility against the
 * confirmed primary tables for DIMENSION), and anything still uncertain is
 * handed to the user with the reasons.
 */
export function screenDatasetAISources(
  sessionId: string,
  input: { role: 'PRIMARY' | 'DIMENSION'; anchorTableIds?: string[]; excludeTableIds?: string[] },
  onProgress?: (event: DatasetAIProgressEvent) => void,
): Promise<DatasetAISession> {
  return requestDatasetAIStream<DatasetAISession>(
    `/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/sources/screen`,
    { method: 'POST', body: JSON.stringify(input) },
    'session',
    onProgress,
  )
}

/**
 * Natural-language turn on the blueprint: the model rewrites only what the
 * instruction asks; untouched stages keep their confirmation, changed ones come
 * back for review.
 */
export function reviseDatasetAIBlueprint(
  sessionId: string,
  instruction: string,
  onProgress?: (event: DatasetAIProgressEvent) => void,
): Promise<DatasetAISession> {
  return requestDatasetAIStream<DatasetAISession>(
    `/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/blueprint/revisions`,
    { method: 'POST', body: JSON.stringify({ instruction }) },
    'session',
    onProgress,
  )
}

/** Record the user's decision on one blueprint stage (or CONFIRM_ALL proposed stages). */
export function resolveDatasetAIBlueprintStage(
  sessionId: string,
  resolution: DatasetAIStageResolution,
): Promise<DatasetAISession> {
  return apiRequest<DatasetAISession>(`/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/stages`, {
    method: 'POST',
    body: JSON.stringify(resolution),
  })
}

export function reportDatasetAISessionProposal(
  sessionId: string,
  type: 'PROPOSAL_APPLIED' | 'PROPOSAL_REVERTED',
  requestId: string,
): Promise<DatasetAISession> {
  return apiRequest<DatasetAISession>(`/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/events`, {
    method: 'POST',
    body: JSON.stringify({ type, requestId }),
  })
}

/**
 * Feed the end-node preview of an applied proposal back into the session
 * (docs/08 §6.10): the server derives findings (empty result, join fan-out,
 * cardinality mismatch, error), reopens the implicated blueprint stages and
 * gives the next planning turn the findings as trusted context.
 */
export function reportDatasetAIProposalExecution(
  sessionId: string,
  requestId: string,
  execution: DatasetAIProposalExecution,
): Promise<DatasetAISession> {
  return apiRequest<DatasetAISession>(`/v1/datasets/ai/sessions/${encodeURIComponent(sessionId)}/events`, {
    method: 'POST',
    body: JSON.stringify({ type: 'PROPOSAL_EXECUTED', requestId, execution }),
  })
}

/** LLM fallback behind the deterministic keyword rule; UNKNOWN means "ask the user". */
export function classifyDatasetAIIntake(instruction: string): Promise<DatasetAIIntakeClassification> {
  return apiRequest<DatasetAIIntakeClassification>('/v1/datasets/ai/intake', {
    method: 'POST',
    body: JSON.stringify({ instruction }),
  })
}

export type DatasetAIRestoredConversationEntry = {
  id: string
  role: 'USER' | 'ASSISTANT'
  content: string
  status?: 'SUPERSEDED' | 'APPLIED' | 'REVERTED'
}

export type DatasetAIRestoredConversation = {
  entries: DatasetAIRestoredConversationEntry[]
  lastInstruction: string
}

const restoredProposalStatus = (
  status: DatasetAISessionProposal['status'],
): DatasetAIRestoredConversationEntry['status'] =>
  status === 'APPLIED' ? 'APPLIED' : status === 'REVERTED' ? 'REVERTED' : 'SUPERSEDED'

/**
 * Rebuild a readable conversation from the persisted session so reopening a dataset
 * does not silently discard the modeling history. Restored proposal entries are
 * text-only records: the staged candidate graph itself is not persisted, so a
 * previously STAGED proposal is rendered as superseded history rather than as an
 * applicable card.
 */
export function conversationFromDatasetAISession(state: DatasetAISessionState): DatasetAIRestoredConversation {
  const turns: Array<{ at: number; entries: DatasetAIRestoredConversationEntry[] }> = []
  for (const [index, proposal] of (state.proposals ?? []).entries()) {
    turns.push({
      at: Date.parse(proposal.createdAt) || 0,
      entries: [
        { id: `restored-user:${proposal.requestId || index}`, role: 'USER', content: proposal.instruction },
        {
          id: `restored-assistant:${proposal.requestId || index}`,
          role: 'ASSISTANT',
          content: proposal.summary,
          status: restoredProposalStatus(proposal.status),
        },
      ],
    })
  }
  for (const [index, clarification] of (state.clarifications ?? []).entries()) {
    const entries: DatasetAIRestoredConversationEntry[] = [{
      id: `restored-clarification:${index}`,
      role: 'ASSISTANT',
      content: `需要确认：${clarification.question}`,
    }]
    if (clarification.answeredAt) {
      entries.push({
        id: `restored-clarification-answer:${index}`,
        role: 'USER',
        content: clarification.answer
          || (clarification.selectedComponent ? `选择组件 ${clarification.selectedComponent.componentId}` : ''),
      })
    }
    turns.push({ at: Date.parse(clarification.askedAt) || 0, entries })
  }
  turns.sort((left, right) => left.at - right.at)
  const proposals = state.proposals ?? []
  return {
    entries: turns.flatMap(turn => turn.entries.filter(entry => entry.content)),
    lastInstruction: proposals.length ? proposals[proposals.length - 1].instruction : '',
  }
}
