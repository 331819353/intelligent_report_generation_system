import { apiRequest } from './api'

export type SemanticAssetStatus = 'ACTIVE' | 'DEPRECATED'
export type SemanticAssetEmbeddingStatus =
  | 'PENDING'
  | 'SUCCEEDED'
  | 'FAILED'
  | 'SKIPPED'

export type SemanticAsset = {
  id: string
  commonTerm: string
  mappingValue: string
  knowledgeType: string
  domainId?: string
  sharingScope?: 'PRIVATE' | 'DOMAIN' | 'PLATFORM'
  status: SemanticAssetStatus
  version: number
  embeddingStatus: SemanticAssetEmbeddingStatus
  embeddingModel?: string
  embeddingErrorCode?: string
  embeddedAt?: string
  createdBy: string
  updatedBy: string
  createdAt: string
  updatedAt: string
}

export type SemanticAssetPage = {
  items: SemanticAsset[]
  total: number
  limit: number
  offset: number
}

export type SemanticAssetInput = {
  commonTerm: string
  mappingValue: string
  knowledgeType: string
}

export type SemanticAssetImportResult = {
  inserted: number
  updated: number
  unchanged: number
  total: number
}

export type SemanticCatalogReadinessStatus = 'PASS' | 'WARN' | 'BLOCKED'

export type SemanticCatalogReadiness = {
  status: SemanticCatalogReadinessStatus
  questionEnabled: boolean
  semanticVersion?: string
  generatedAt: string
  counts: {
    metrics: { total: number; ready: number }
    dimensions: { total: number; ready: number }
    terms: { total: number; ready: number }
    parsingRules: { total: number; ready: number }
    decisionGraph: { total: number; ready: number }
    decisionEntries: number
  }
  graph: {
    status: string
    generationId?: string
    generation?: number
    requestedEventVersion: number
    appliedEventVersion: number
    nodeCount: number
    edgeCount: number
    errorCode?: string
  }
  checks: Array<{
    code: string
    label: string
    status: SemanticCatalogReadinessStatus
    current: number
    required: number
    detail: string
    route?: string
  }>
  blockerCodes: string[]
}

export type SemanticCatalogObject = {
  objectType: 'METRIC' | 'DIMENSION' | 'TERM' | 'PARSING_RULE'
  id: string
  code: string
  name: string
  description?: string
  domainId?: string
  sharingScope?: string
  status: string
  certification: 'CERTIFIED' | 'UNCERTIFIED'
  version: number
  contentHash?: string
  ownerId?: string
  sensitivity: string
  executionEligible: boolean
  readinessCode: string
  updatedAt: string
}

export type SemanticCatalogView = {
  semanticVersion?: string
  readiness: SemanticCatalogReadiness
  items: SemanticCatalogObject[]
  total: number
  limit: number
  offset: number
}

export type SemanticReleaseProjection = {
  id: string
  target: 'EXECUTION_SEMANTIC_LAYER' | 'POSTGRES_REGISTRY' | 'SEARCH_INDEX' | 'NEBULA_GRAPH'
  status: 'PENDING' | 'RUNNING' | 'READY' | 'FAILED' | 'STALE'
  expectedContentHash: string
  appliedContentHash?: string
  resourceVersion?: string
  objectCount: number
  errorCode?: string
  version: number
  updatedAt: string
}

export type SemanticReleaseObjectType =
  | 'DOMAIN'
  | 'BUSINESS_TERM'
  | 'ENTITY'
  | 'SEMANTIC_MODEL'
  | 'MEASURE'
  | 'METRIC'
  | 'DIMENSION'
  | 'DIMENSION_VALUE'
  | 'TIME'
  | 'COHORT'
  | 'RELATION'
  | 'DATASET'
  | 'TABLE_COLUMN'
  | 'POLICY'
  | 'QUALITY_RULE'
  | 'CERTIFIED_EXAMPLE'
  | 'PARSING_RULE'

export type SemanticReleaseObject = {
  id: string
  objectType: SemanticReleaseObjectType
  objectId: string
  objectVersion: string
  domainId?: string
  ownerId: string
  certification: 'CERTIFIED'
  sensitivity: 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED'
  validFrom: string
  validTo?: string
  contract: Record<string, unknown>
  contentHash: string
  createdAt: string
}

export type SemanticRelease = {
  id: string
  semanticVersion: string
  contentHash: string
  status: 'DRAFT' | 'VALIDATING' | 'PROJECTING' | 'READY' | 'ACTIVE' | 'BLOCKED' | 'SUPERSEDED'
  baseReleaseId?: string
  notes?: string
  objectCount: number
  validationSummary: Record<string, unknown>
  version: number
  createdBy: string
  updatedBy: string
  activatedBy?: string
  evaluationSetId?: string
  evaluationSetContentHash?: string
  createdAt: string
  updatedAt: string
  validatedAt?: string
  activatedAt?: string
  projections: SemanticReleaseProjection[]
  objects?: SemanticReleaseObject[]
}

export type SemanticReleaseValidationIssue = {
  code: string
  objectType?: string
  objectId?: string
  field?: string
  message: string
}

export type SemanticReleaseValidation = {
  status: string
  issues: SemanticReleaseValidationIssue[]
  counts: Record<string, number>
}

export type BootstrapSemanticReleaseInput = {
  semanticVersion: string
  defaultTimezone: string
  defaultCalendar: 'GREGORIAN' | 'ISO_WEEK' | 'FISCAL'
  completePeriodPolicy: 'EXCLUDE_INCOMPLETE' | 'INCLUDE_INCOMPLETE' | 'NOT_APPLICABLE'
  notes?: string
}

export type BootstrapSemanticReleasePreview = {
  eligible: boolean
  sourceCounts: Record<string, number>
  candidateCount: number
  issues: Array<{
    code: string
    severity: 'BLOCKER' | 'WARNING'
    objectType?: string
    objectId?: string
    message: string
  }>
}

export type SemanticReleaseState = {
  activeReleaseId?: string
  semanticVersion?: string
  contentHash?: string
  version: number
  updatedAt: string
}

type SemanticAssetFilters = {
  q?: string
  knowledgeType?: string
  status?: SemanticAssetStatus | ''
  embeddingStatus?: SemanticAssetEmbeddingStatus | ''
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

const assetPath = (id: string) =>
  `/v1/semantic-assets/${encodeURIComponent(id)}`

export const semanticAssetAPI = {
	catalog: ({
		q = '', objectType = '', status = '', ready = '', limit = 200, offset = 0,
	}: {
		q?: string
		objectType?: SemanticCatalogObject['objectType'] | ''
		status?: string
		ready?: 'READY' | 'NOT_READY' | ''
		limit?: number
		offset?: number
	} = {}) => apiRequest<SemanticCatalogView>(
		`/v1/semantic-assets/catalog?${queryString({ q, objectType, status, ready, limit, offset })}`,
		{ cache: 'no-store' },
	),

	readiness: () =>
		apiRequest<SemanticCatalogReadiness>('/v1/semantic-assets/readiness', {
			cache: 'no-store',
		}),

  activeRelease: () =>
    apiRequest<SemanticReleaseState>('/v1/semantic-assets/releases/active', {
      cache: 'no-store',
    }),

  releases: ({ limit = 20, offset = 0 } = {}) =>
    apiRequest<{ items: SemanticRelease[]; total: number; limit: number; offset: number }>(
      `/v1/semantic-assets/releases?${queryString({ limit, offset })}`,
      { cache: 'no-store' },
    ),

  release: (id: string) =>
    apiRequest<SemanticRelease>(
      `/v1/semantic-assets/releases/${encodeURIComponent(id)}`,
      { cache: 'no-store' },
    ),

  previewBootstrapRelease: (input: BootstrapSemanticReleaseInput) =>
    apiRequest<BootstrapSemanticReleasePreview>(
      '/v1/semantic-assets/releases/bootstrap/preview',
      { method: 'POST', body: JSON.stringify(input) },
    ),

  bootstrapRelease: (input: BootstrapSemanticReleaseInput) =>
    apiRequest<SemanticRelease>('/v1/semantic-assets/releases/bootstrap', {
      method: 'POST', body: JSON.stringify(input),
    }),

  validateRelease: (id: string, expectedVersion: number) =>
    apiRequest<SemanticRelease>(
      `/v1/semantic-assets/releases/${encodeURIComponent(id)}/validate`,
      { method: 'POST', body: JSON.stringify({ expectedVersion }) },
    ),

  activateRelease: (
    id: string,
    expectedVersion: number,
    expectedStateVersion: number,
    evaluationSetId = '',
  ) => apiRequest<SemanticReleaseState>(
    `/v1/semantic-assets/releases/${encodeURIComponent(id)}/activate`,
    {
      method: 'POST',
      body: JSON.stringify({
        expectedVersion,
        expectedStateVersion,
        ...(evaluationSetId ? { evaluationSetId } : {}),
      }),
    },
  ),

  evaluatePermission: (action: 'READ' | 'MANAGE') =>
    apiRequest<{ allowed: boolean }>('/v1/permissions/evaluate', {
      method: 'POST',
      body: JSON.stringify({
        resourceType: 'DATASET', action, objectId: '',
      }),
    }),

  list: ({
    q = '',
    knowledgeType = '',
    status = 'ACTIVE',
    embeddingStatus = '',
    limit = 200,
    offset = 0,
  }: SemanticAssetFilters = {}) =>
    apiRequest<SemanticAssetPage>(
      `/v1/semantic-assets?${queryString({
        q, knowledgeType, status, embeddingStatus, limit, offset,
      })}`,
      { cache: 'no-store' },
    ),

  listKnowledgeTypes: () =>
    apiRequest<{ items: string[] }>('/v1/semantic-assets/types', {
      cache: 'no-store',
    }),

  create: (input: SemanticAssetInput) =>
    apiRequest<SemanticAsset>('/v1/semantic-assets', {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  update: (
    id: string,
    expectedVersion: number,
    input: SemanticAssetInput,
  ) =>
    apiRequest<SemanticAsset>(assetPath(id), {
      method: 'PUT',
      body: JSON.stringify({ expectedVersion, ...input }),
    }),

  deprecate: (id: string, expectedVersion: number) =>
    apiRequest<SemanticAsset>(`${assetPath(id)}/deprecate`, {
      method: 'POST',
      body: JSON.stringify({ expectedVersion }),
    }),

  importItems: (items: SemanticAssetInput[]) =>
    apiRequest<SemanticAssetImportResult>('/v1/semantic-assets/import', {
      method: 'POST',
      body: JSON.stringify({ items }),
    }),
}
