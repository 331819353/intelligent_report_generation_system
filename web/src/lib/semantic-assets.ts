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
