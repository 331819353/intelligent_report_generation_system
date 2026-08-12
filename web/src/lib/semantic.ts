import { apiRequest, apiResponse } from './api'
import { semanticCreatePayload } from './semantic-create'

export type SemanticResource =
  | 'models'
  | 'measures'
  | 'metrics'
  | 'metric-versions'
  | 'dimensions'
  | 'terms'
  | 'kpi-bundles'
  | 'relationships'
  | 'members'
  | 'hierarchies'
  | 'certified-examples'
  | 'metric-dimensions'

export type SemanticStatus = 'DRAFT' | 'CERTIFIED' | 'ACTIVE' | 'DEPRECATED'

export type SemanticObject = {
  id: string
  objectId?: string
  versionNo?: number
  version?: number
  code?: string
  name?: string
  description?: string
  status: SemanticStatus
  ownerId?: string
  contentHash?: string
  updatedAt?: string
  createdAt?: string
  datasetId?: string
  datasetVersionId?: string
  materializationId?: string
  datasetSchemaHash?: string
  layer?: string
  grainContract?: Record<string, unknown>
  primaryTimeFieldId?: string
  timeContractVersionId?: string
  semanticModelVersionId?: string
  formulaAst?: Record<string, unknown>
  additivity?: string
  additivitySuggestion?: string
  timeGrain?: string
  unit?: string
  currency?: string
  kind?: string
  sensitivity?: string
  memberIndexPolicy?: string
  logicalFieldId?: string
  term?: string
  aliases?: string[]
  definition?: string
  [key: string]: unknown
}

export type SemanticPage = { items: SemanticObject[]; nextCursor?: string }

export type AdditivityReadiness = {
  domainId: string
  metricCount: number
  confirmedCount: number
  unconfirmedCount: number
  confirmationRate: number
}

export type SemanticWriteResult = {
  resourceType: string
  resourceId: string
  objectId?: string
  contentHash?: string
  status: string
  recordVersion?: number
  semanticVersion?: string
  updatedAt?: string
  replayed: boolean
}

export type ReleaseCatalogItem = {
  id: string
  semanticVersion: string
  contentHash: string
  status: string
  objectCount: number
  version: number
  readyProjectionCount: number
  approvalCount: number
  createdAt: string
  updatedAt: string
  readyAt?: string
  activatedAt?: string
}

export type ReleaseCatalogPage = { items: ReleaseCatalogItem[]; nextCursor?: string }

export type EvaluationSetCatalogItem = {
  id: string
  code: string
  versionNo: number
  name: string
  description: string
  datasetSplit: string
  evaluationMode: string
  status: string
  targetReleaseId?: string
  sealedCaseCount: number
  sealedReviewCount: number
  recordVersion: number
  updatedAt: string
}

export type EvaluationReviewCase = {
  id: string
  caseKey: string
  approvedQuestion: string
  priority: string
  expectedDisposition: string
  securityExpectation: string
  complexity: string
  ambiguity: string
  shardId: number
  contentHash: string
  independentReviewCount: number
  actorReviewed: boolean
  actorEligible: boolean
}

export type EvaluationReviewPage = {
  evaluationSetId: string
  status: string
  items: EvaluationReviewCase[]
  total: number
  actorReviewed: number
  actorEligible: boolean
  fullyReviewed: number
  nextOffset?: number
  requiredReviewers: number
}

export type SemanticImportState = 'UPLOADED' | 'VALIDATING' | 'VALIDATED' | 'PARTIALLY_COMMITTED' | 'COMMITTED' | 'WITHDRAWN' | 'FAILED'

export type SemanticImport = {
  id: string
  assetType: string
  fileName: string
  state: SemanticImportState
  totalRows: number
  validRows: number
  invalidRows: number
  failureReason?: string
  validationCompletedAt?: string
  committedAt?: string
  updatedAt: string
}

export type SemanticImportCommitResult = {
  importId: string
  state: SemanticImportState
  committed: Array<{ ObjectID?: string; VersionID?: string; objectId?: string; versionId?: string; status: string }>
}

export type TimeContractCatalogItem = {
  id: string
  timeContractId: string
  code: string
  name: string
  versionNo: number
  status: string
  timezone: string
  incompletePeriodPolicy?: string
  expectedLagHours: number
  contentHash: string
  updatedAt: string
}

export type TimeContractCreateInput = {
  code: string
  name: string
  timezone: string
  weekStart: 'MONDAY' | 'SUNDAY'
  weekNumbering: 'ISO' | 'US'
  fiscalYearStartMonth: number
  fiscalMonthRule: 'CALENDAR' | 'FOUR_FOUR_FIVE' | 'CUSTOM_TABLE'
  incompletePeriodPolicy?: 'MTD' | 'FULL_PERIOD' | 'LAST_COMPLETE'
  comparisonAlignment: 'SAME_DAY_COUNT' | 'SAME_CALENDAR_RANGE'
  monthEndOverflowRule: 'CLAMP_TO_LAST_DAY' | 'SKIP'
  supportedGrains: Array<'DAY' | 'WEEK' | 'MONTH' | 'QUARTER' | 'YEAR' | 'FISCAL_MONTH' | 'FISCAL_QUARTER' | 'FISCAL_YEAR'>
  dataAvailableThroughExpr: string
  expectedLagHours: number
  calendarDatasetVersionId?: string
}

export type ReleaseLifecycle = {
  releaseId: string
  status: string
  contentHash: string
  releaseVersion: number
  releaseStateVersion: number
  activeReleaseId?: string
  readyProjectionCount: number
  latestGate?: { passed: boolean; receiptHash: string; failures: string[]; facts: unknown; evaluationSetId?: string; evaluationBatchId?: string }
  reviewReportCount: number
  approvalCount: number
  approvedRoles: string[]
  actorHasApproved: boolean
	rejectionCount: number
	rejectedRoles: string[]
	actorApprovalRole?: string
	approvalDueAt?: string
	approvalSlaStatus: string
	escalationLevel: number
	projections: Array<{ target: string; status: string; expectedContentHash: string; appliedContentHash?: string; attempt: number; maxAttempts: number; errorCode?: string; hashMatched: boolean }>
}

export type ReleaseRollout = {
  id: string
  candidateReleaseId: string
  controlReleaseId?: string
  stage: 'SHADOW' | 'CANARY_5' | 'CANARY_20' | 'CANARY_50' | 'ACCEPTED_95'
  state: 'RUNNING' | 'PAUSED' | 'STOPPED' | 'ACCEPTED' | 'COMPLETED' | 'ROLLED_BACK'
  canaryPercent: number
  version: number
  startedAt: string
  stageStartedAt: string
  updatedAt: string
}

export type ReleaseOperationalImpact = {
  releaseId: string
  status: string
  retentionUntil?: string
  canRetire: boolean
  blockedCode?: string
  activeReferenceCount: number
  references: Array<{
    id: string
    releaseId: string
    referenceType: 'REPORT_VERSION' | 'SAVED_QUESTION'
    referenceId: string
    referenceName: string
    ownerId: string
    createdAt: string
  }>
  rollout?: ReleaseRollout
  observability?: {
    stage: string
    state: string
    stageElapsedSeconds: number
    minimumDurationSeconds: number
    minimumSamples: number
    gatePassed: boolean
    controlSamples: number
    candidateSamples: number
    controlAnswered: number
    candidateAnswered: number
    controlP95LatencyMs: number
    candidateP95LatencyMs: number
    stopRequired: boolean
    stopCodes: string[]
    advanceAllowed: boolean
    advanceBlockedCodes: string[]
  }
}

export type ReleaseObjectInput = {
  type: string
  objectId: string
  objectVersionId: string
  contentHash: string
  sensitivity: string
  contract: Record<string, unknown>
}

const idempotencyHeaders = () => ({ 'Idempotency-Key': crypto.randomUUID() })
const semanticBase = '/v1/askdata/semantic'

export const semanticAPI = {
  list: (resource: SemanticResource, status?: SemanticStatus, limit = 100) => {
    const query = new URLSearchParams({ limit: String(limit) })
    if (status) query.set('status', status)
    return apiRequest<SemanticPage>(`${semanticBase}/${resource}?${query}`)
  },
  get: (resource: SemanticResource, id: string) =>
    apiRequest<SemanticObject>(`${semanticBase}/${resource}/${encodeURIComponent(id)}`),
  create: (resource: Exclude<SemanticResource, 'members' | 'hierarchies' | 'certified-examples'>, payload: Record<string, unknown>, idempotencyKey?: string) =>
    apiRequest<SemanticWriteResult>(`${semanticBase}/${resource}`, {
      method: 'POST', headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : idempotencyHeaders(), body: JSON.stringify(semanticCreatePayload(resource, payload)),
    }),
  update: (resource: Exclude<SemanticResource, 'members' | 'hierarchies' | 'certified-examples'>, id: string, payload: Record<string, unknown>) =>
    apiRequest<SemanticWriteResult>(`${semanticBase}/${resource}/${encodeURIComponent(id)}`, {
      method: 'PUT', headers: idempotencyHeaders(), body: JSON.stringify(payload),
    }),
  certify: (domainId: string, objectVersionIds: string[], note: string) =>
    apiRequest<{ certifiedObjectVersionIds: string[] }>(`${semanticBase}/bulk-certify`, {
      method: 'POST', body: JSON.stringify({ domainId, objectVersionIds, note }),
    }),
  readiness: (domainId: string) =>
    apiRequest<AdditivityReadiness>(`${semanticBase}/domains/${encodeURIComponent(domainId)}/readiness`),
  confirmAdditivity: (metricVersionIds: string[], suggestion: string) =>
    apiRequest<{ metricVersionIds: string[]; confirmedCount: number; replayed: boolean }>(`${semanticBase}/metrics/additivity/confirm`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ metricVersionIds, suggestion }),
    }),
  releases: (limit = 100) => apiRequest<ReleaseCatalogPage>(`${semanticBase}/releases?limit=${limit}`),
  evaluationSets: (limit = 100) => apiRequest<{ items: EvaluationSetCatalogItem[] }>(`${semanticBase}/evaluation-sets?limit=${limit}`),
  createEvaluationSet: (releaseId: string, input: { code?: string; name?: string; description?: string } = {}) =>
    apiRequest<{ evaluationSetId: string; releaseId: string; caseCount: number; status: string }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/evaluation-sets`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ code: input.code ?? '', name: input.name ?? '', description: input.description ?? '' }),
    }),
  evaluationCases: (evaluationSetId: string, limit = 100, offset = 0) =>
    apiRequest<EvaluationReviewPage>(`${semanticBase}/evaluation-sets/${encodeURIComponent(evaluationSetId)}/cases?limit=${limit}&offset=${offset}`),
  reviewEvaluationSet: (evaluationSetId: string, caseIds: string[], decision: 'APPROVED' | 'REJECTED', comment: string) =>
    apiRequest<{ evaluationSetId: string; decision: string; reviewedCount: number; totalCount: number }>(`${semanticBase}/evaluation-sets/${encodeURIComponent(evaluationSetId)}/reviews`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ caseIds, decision, comment }),
    }),
  sealEvaluationSet: (evaluationSetId: string) =>
    apiRequest<{ evaluationSetId: string; sealed: boolean; status: string; caseCount: number; reviewCount: number; contentHash?: string }>(`${semanticBase}/evaluation-sets/${encodeURIComponent(evaluationSetId)}/seal`, {
      method: 'POST', headers: idempotencyHeaders(), body: '{}',
    }),
  downloadImportTemplate: async (domainId: string, assetType = 'EVAL_CASE') => {
    const query = new URLSearchParams({ assetType, domainId, format: 'xlsx' })
    return apiResponse(`${semanticBase}/imports/template?${query}`)
  },
  uploadImport: (domainId: string, file: File, assetType = 'EVAL_CASE') => {
    const form = new FormData()
    form.append('assetType', assetType)
    form.append('domainId', domainId)
    form.append('file', file)
    return apiRequest<{ importId: string; assetType: string; state: SemanticImportState; created: boolean }>(`${semanticBase}/imports`, {
      method: 'POST', body: form,
    })
  },
  importStatus: (importId: string) => apiRequest<SemanticImport>(`${semanticBase}/imports/${encodeURIComponent(importId)}`),
  downloadImportReport: (importId: string) => apiResponse(`${semanticBase}/imports/${encodeURIComponent(importId)}/report?format=xlsx`),
  commitImport: (importId: string) => apiRequest<SemanticImportCommitResult>(`${semanticBase}/imports/${encodeURIComponent(importId)}/commit`, {
    method: 'POST', body: JSON.stringify({ all: true, acknowledgeImpact: true }),
  }),
  timeContracts: (limit = 100) => apiRequest<{ items: TimeContractCatalogItem[] }>(`${semanticBase}/time-contracts?limit=${limit}`),
  createTimeContract: (input: TimeContractCreateInput) =>
    apiRequest<SemanticWriteResult>(`${semanticBase}/time-contracts`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    }),
  createRelease: (semanticVersion: string, objects: ReleaseObjectInput[]) =>
    apiRequest<SemanticWriteResult>(`${semanticBase}/releases`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ semanticVersion, objects }),
    }),
  composeRelease: (semanticVersion: string) =>
    apiRequest<SemanticWriteResult>(`${semanticBase}/releases/compose`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ semanticVersion }),
    }),
  releaseLifecycle: (releaseId: string) =>
    apiRequest<ReleaseLifecycle>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/lifecycle`),
  validateProject: (releaseId: string) =>
    apiRequest<{ preflight: { passed: boolean; objectCount: number; issues: unknown[] }; status: string; started: boolean }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/validate-project`, {
      method: 'POST', headers: idempotencyHeaders(), body: '{}',
    }),
  retryProjections: (releaseId: string) =>
    apiRequest<{ releaseId: string; status: string; retriedCount: number }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/retry-projections`, {
      method: 'POST', headers: idempotencyHeaders(), body: '{}',
    }),
  planEvaluation: (releaseId: string, evaluationSetId: string, evaluationBatchId: string, runKind = 'FIRST_95_CLAIM') =>
    apiRequest<{ shardIds: number[]; canIssue95Percent: boolean }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/evaluation-batches`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ evaluationSetId, evaluationBatchId, runKind }),
    }),
  gate: (releaseId: string, evaluationSetId: string, evaluationBatchId: string) =>
    apiRequest<{ passed: boolean; receiptHash: string; failures: string[] }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/gate`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ evaluationSetId, evaluationBatchId }),
    }),
  generateReview: (releaseId: string, evaluationSetId: string, evaluationBatchId: string) =>
    apiRequest<{ persistedReportHash: string }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/review-report/generate`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({
        evaluationSetId, evaluationBatchId, promptVersion: 'release-review-v1', preferredModel: '',
      }),
    }),
  approve: (releaseId: string, input: { evaluationSetId: string; evaluationBatchId: string; gateReceiptHash: string; reviewRole: string; decision: string; commentHash: string }) =>
    apiRequest<{ releaseId: string; approvalHash: string }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/approvals`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    }),
  withdrawApproval: (releaseId: string, gateReceiptHash: string, reviewRole: string, reasonHash: string) =>
    apiRequest<{ releaseId: string; withdrawalId: string }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/approvals/withdraw`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ gateReceiptHash, reviewRole, reasonHash }),
    }),
  resetRejectedApprovals: (releaseId: string, gateReceiptHash: string, reasonHash: string) =>
    apiRequest<{ releaseId: string; resetCount: number }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/approvals/reset-rejection`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ gateReceiptHash, reasonHash }),
    }),
  escalateApproval: (releaseId: string, gateReceiptHash: string, reasonHash: string) =>
    apiRequest<{ releaseId: string; escalationLevel: number }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/approvals/escalate`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ gateReceiptHash, reasonHash }),
    }),
  activate: (releaseId: string, evaluationSetId: string, evaluationBatchId: string, expectedStateVersion: number) =>
    apiRequest<{ activated: boolean; activeReleaseId?: string; releaseStateVersion: number; failures: string[] }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/activate`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ evaluationSetId, evaluationBatchId, expectedStateVersion }),
    }),
  releaseOperations: (releaseId: string) =>
    apiRequest<ReleaseOperationalImpact>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/operations`),
  startRollout: (releaseId: string, reasonHash: string) =>
    apiRequest<ReleaseRollout>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/rollouts`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ reasonHash }),
    }),
  mutateRollout: (releaseId: string, action: 'advance' | 'pause' | 'resume' | 'stop', expectedVersion: number, reasonHash: string) =>
    apiRequest<ReleaseRollout>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/rollouts/${action}`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ expectedVersion, reasonHash }),
    }),
  rollback: (releaseId: string, expectedStateVersion: number, reasonHash: string) =>
    apiRequest<{ rolledBack: boolean; activeReleaseId: string; replacedReleaseId: string; releaseStateVersion: number }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/rollback`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ expectedStateVersion, reasonHash }),
    }),
  retire: (releaseId: string) =>
    apiRequest<{ releaseId: string; retired: boolean }>(`${semanticBase}/releases/${encodeURIComponent(releaseId)}/retire`, {
      method: 'POST', headers: idempotencyHeaders(), body: '{}',
    }),
}
