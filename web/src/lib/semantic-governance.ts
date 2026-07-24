import { apiRequest } from './api'

export type SemanticPage<T> = {
  items: T[]
  total: number
  limit: number
  offset: number
}

export type DimensionType =
  | 'STANDARD'
  | 'TIME'
  | 'GEOGRAPHY'
  | 'ORGANIZATION'
  | 'PRODUCT'
  | 'CUSTOMER'
  | 'OTHER'

export type MemberIndexPolicy = 'FULL' | 'EXACT_ONLY' | 'NONE'
export type DimensionStatus = 'DRAFT' | 'PUBLISHED' | 'DEPRECATED'
export type DimensionSurveyStatus = 'SUGGESTED' | 'ACCEPTED' | 'REJECTED' | 'STALE'
export type DimensionFieldRole = 'DIMENSION' | 'ATTRIBUTE' | 'TIME' | 'IDENTIFIER'

export type DimensionProfileStatus =
  | 'NOT_QUEUED'
  | 'QUEUED'
  | 'RUNNING'
  | 'SUCCEEDED'
  | 'SKIPPED_POLICY'
  | 'FAILED'
  | 'STALE'

export type DimensionProfile = {
  id: string
  status: DimensionProfileStatus
  profileVersion: string
  policyVersion: string
  materializationId: string
  materializationSnapshotHash: string
  expectedRowCount: number
  rowCount?: number
  nonNullCount?: number
  nullCount?: number
  distinctCount?: number
  distinctOverflow: boolean
  distinctCap: number
  distinctRatio?: number
  riskHighCardinality: boolean
  recommendedMemberIndexPolicy?: MemberIndexPolicy
  resultCode?: string
  evidenceHash?: string
  attempt: number
  maxAttempts: number
  createdAt: string
  updatedAt: string
  startedAt?: string
  completedAt?: string
}

export type DimensionSurveyCandidate = {
  id: string
  surveyRunId: string
  datasetId: string
  datasetVersionId: string
  schemaHash: string
  materializationId: string
  materializationSnapshotHash: string
  materializationRowCount: number
  fieldId: string
  fieldCode: string
  fieldRole: DimensionFieldRole
  canonicalType: string
  semanticType: string
  riskHighCardinality: boolean
  riskSensitive: boolean
  evidence: unknown
  proposedCode: string
  proposedName: string
  proposedDescription: string
  proposedDimensionType: DimensionType
  proposedMemberIndexPolicy: MemberIndexPolicy
  proposedHighCardinality: boolean
  proposedSensitive: boolean
  status: DimensionSurveyStatus
  version: number
  acceptedDimensionId?: string
  decisionReason?: string
  generatedBy: string
  updatedBy: string
  reviewedBy?: string
  reviewedAt?: string
  createdAt: string
  updatedAt: string
  profile: DimensionProfile
}

export type UpdateDimensionSurveyCandidateInput = {
  expectedVersion: number
  code: string
  name: string
  description: string
  dimensionType: DimensionType
  memberIndexPolicy: MemberIndexPolicy
  highCardinality: boolean
  sensitive: boolean
}

export type Dimension = {
  id: string
  datasetId: string
  datasetVersionId: string
  fieldId: string
  fieldCode: string
  code: string
  name: string
  description: string
  dimensionType: DimensionType
  memberIndexPolicy: MemberIndexPolicy
  highCardinality: boolean
  sensitive: boolean
  status: DimensionStatus
  definitionHash: string
  version: number
  memberRefreshGeneration?: string
  memberCount?: number
  memberRefreshedAt?: string
  lastMemberRefreshJobId?: string
  createdBy: string
  updatedBy: string
  createdAt: string
  updatedAt: string
}

export type DimensionMember = {
  id: string
  dimensionId: string
  memberKey: string
  canonicalLabel: string
  normalizedValue: string
  status: 'ACTIVE' | 'DEPRECATED'
  firstSeenAt: string
  lastSeenAt: string
  validFrom?: string
  validTo?: string
  refreshGeneration?: string
  lastRefreshJobId?: string
  updatedAt: string
}

export type DimensionMemberAlias = {
  id: string
  dimensionId: string
  dimensionMemberId: string
  alias: string
  normalizedAlias: string
  aliasType: 'CODE' | 'BUSINESS' | 'ABBREVIATION' | 'LEGACY' | 'LLM' | 'USER'
  validFrom?: string
  validTo?: string
  version: number
  createdBy?: string
  updatedBy?: string
  createdAt: string
  updatedAt: string
}

export type DimensionRefreshJob = {
  id: string
  dimensionId: string
  dimensionVersion: number
  datasetId: string
  datasetVersionId: string
  fieldId: string
  fieldCode: string
  memberIndexPolicy: MemberIndexPolicy
  materializationId?: string
  refreshGeneration: string
  status: 'QUEUED' | 'RUNNING' | 'SUCCEEDED' | 'FAILED' | 'SKIPPED'
  maxMembers: number
  timeoutSeconds: number
  requestHash: string
  requestedBy: string
  attempt: number
  maxAttempts: number
  memberCount?: number
  resultCode?: string
  errorMessage?: string
  createdAt: string
  updatedAt: string
  startedAt?: string
  completedAt?: string
}

export type DimensionMetricCompatibility = {
  id: string
  dimensionId: string
  metricId: string
  metricVersionId: string
  metricDatasetVersionId: string
  compatibilityType: 'DIRECT' | 'BRIDGE' | 'DERIVED'
  fanoutPolicy: 'SAFE' | 'DEDUPLICATE' | 'UNSAFE'
  joinPath: unknown
  evidenceSource: 'RULE' | 'PROFILE' | 'LLM' | 'HUMAN'
  confidence?: number
  status: 'PROPOSED' | 'VERIFIED' | 'REJECTED'
  version: number
  verifiedBy?: string
  verifiedAt?: string
  createdBy?: string
  updatedBy?: string
  createdAt: string
  updatedAt: string
}

export type DimensionSurveyAcceptance = {
  candidate: DimensionSurveyCandidate
  dimension: Dimension
  memberRefreshJob?: DimensionRefreshJob
  memberSearchReady: boolean
  nextAction: 'WAIT_FOR_MEMBER_REFRESH' | 'USE_EXACT_MATCH_ONLY' | 'MEMBER_INDEX_DISABLED'
}

type CandidateFilters = {
  status?: DimensionSurveyStatus | ''
  fieldRole?: DimensionFieldRole | ''
  datasetId?: string
  datasetVersionId?: string
  limit?: number
  offset?: number
}

type DimensionFilters = {
  q?: string
  datasetVersionId?: string
  dimensionType?: DimensionType | ''
  status?: DimensionStatus | ''
  limit?: number
  offset?: number
}

const queryString = (values: Record<string, string | number | undefined>) => {
  const query = new URLSearchParams()
  Object.entries(values).forEach(([key, value]) => {
    if (value !== undefined && value !== '') query.set(key, String(value))
  })
  return query.toString()
}

const semanticPath = (suffix: string) => `/v1/semantic/${suffix}`
const candidatePath = (id: string) =>
  semanticPath(`dimension-survey-candidates/${encodeURIComponent(id)}`)
const dimensionPath = (id: string) => semanticPath(`dimensions/${encodeURIComponent(id)}`)

export const semanticGovernanceAPI = {
  evaluatePermission: (action: 'READ' | 'MANAGE') =>
    apiRequest<{ allowed: boolean }>('/v1/permissions/evaluate', {
      method: 'POST',
      body: JSON.stringify({ resourceType: 'DATASET', action, objectId: '' }),
    }),

  listCandidates: ({
    status = '',
    fieldRole = '',
    datasetId = '',
    datasetVersionId = '',
    limit = 200,
    offset = 0,
  }: CandidateFilters = {}) =>
    apiRequest<SemanticPage<DimensionSurveyCandidate>>(
      `${semanticPath('dimension-survey-candidates')}?${queryString({
        status, fieldRole, datasetId, datasetVersionId, limit, offset,
      })}`,
      { cache: 'no-store' },
    ),

  updateCandidate: (id: string, input: UpdateDimensionSurveyCandidateInput) =>
    apiRequest<DimensionSurveyCandidate>(candidatePath(id), {
      method: 'PUT',
      body: JSON.stringify(input),
    }),

  acceptCandidate: (id: string, expectedVersion: number) =>
    apiRequest<DimensionSurveyAcceptance>(`${candidatePath(id)}/accept`, {
      method: 'POST',
      body: JSON.stringify({ expectedVersion }),
    }),

  rejectCandidate: (id: string, expectedVersion: number, reason: string) =>
    apiRequest<DimensionSurveyCandidate>(`${candidatePath(id)}/reject`, {
      method: 'POST',
      body: JSON.stringify({ expectedVersion, reason }),
    }),

  listDimensions: ({
    q = '',
    datasetVersionId = '',
    dimensionType = '',
    status = '',
    limit = 200,
    offset = 0,
  }: DimensionFilters = {}) =>
    apiRequest<SemanticPage<Dimension>>(
      `${semanticPath('dimensions')}?${queryString({
        q, datasetVersionId, dimensionType, status, limit, offset,
      })}`,
      { cache: 'no-store' },
    ),

  getDimension: (id: string) =>
    apiRequest<Dimension>(dimensionPath(id), { cache: 'no-store' }),

  listMembers: (dimensionId: string, q = '', status: '' | 'ACTIVE' | 'DEPRECATED' = 'ACTIVE', limit = 200, offset = 0) =>
    apiRequest<SemanticPage<DimensionMember>>(
      `${dimensionPath(dimensionId)}/members?${queryString({ q, status, limit, offset })}`,
      { cache: 'no-store' },
    ),

  listAliases: (dimensionId: string, dimensionMemberId = '', limit = 200, offset = 0) =>
    apiRequest<SemanticPage<DimensionMemberAlias>>(
      `${semanticPath('dimension-member-aliases')}?${queryString({
        dimensionId, dimensionMemberId, limit, offset,
      })}`,
      { cache: 'no-store' },
    ),

  createAlias: (input: {
    dimensionId: string
    dimensionMemberId: string
    alias: string
    aliasType: DimensionMemberAlias['aliasType']
  }) =>
    apiRequest<DimensionMemberAlias>(semanticPath('dimension-member-aliases'), {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  createRefreshJob: (dimensionId: string, expectedDimensionVersion: number, idempotencyKey: string) =>
    apiRequest<DimensionRefreshJob>(`${dimensionPath(dimensionId)}/member-refresh-jobs`, {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: JSON.stringify({ expectedDimensionVersion }),
    }),

  listRefreshJobs: (dimensionId: string, limit = 50, offset = 0) =>
    apiRequest<SemanticPage<DimensionRefreshJob>>(
      `${semanticPath('dimension-member-refresh-jobs')}?${queryString({
        dimensionId, limit, offset,
      })}`,
      { cache: 'no-store' },
    ),

  listCompatibilities: (dimensionId: string, status: '' | DimensionMetricCompatibility['status'] = '', limit = 200, offset = 0) =>
    apiRequest<SemanticPage<DimensionMetricCompatibility>>(
      `${semanticPath('dimension-metric-compatibilities')}?${queryString({
        dimensionId, status, limit, offset,
      })}`,
      { cache: 'no-store' },
    ),

  verifyCompatibility: (id: string, expectedVersion: number) =>
    apiRequest<DimensionMetricCompatibility>(
      `${semanticPath(`dimension-metric-compatibilities/${encodeURIComponent(id)}`)}/verify`,
      {
        method: 'POST',
        body: JSON.stringify({ expectedVersion }),
      },
    ),
}

export function createDimensionRefreshIdempotencyKey(): string {
  return globalThis.crypto.randomUUID()
}
