import { apiRequest } from './api'

export type SemanticParsingRuleType =
  | 'METRIC_NAME_SUFFIX'
  | 'ADMIN_REGION_SUFFIX'
  | 'QUERY_RESIDUAL_TERM'
  | 'BROAD_METRIC_PHRASE'
export type SemanticParsingRuleStatus = 'ACTIVE' | 'DEPRECATED'
export type SemanticParsingRuleScope = 'PLATFORM' | 'TENANT'
export type SemanticParsingMatchMode = 'EXACT' | 'SUFFIX' | 'CONTAINS'
export type SemanticParsingAction =
  | 'STRIP_SUFFIX'
  | 'MAP_ADMIN_REGION'
  | 'ALLOW_DETERMINISTIC'
  | 'REQUIRE_METRIC_CONFIRMATION'

export type SemanticParsingRule = {
  id: string
  ruleType: SemanticParsingRuleType
  pattern: string
  matchMode: SemanticParsingMatchMode
  action: SemanticParsingAction
  outputName?: string
  outputCode?: string
  minimumLength: number
  maximumLength: number
  priority: number
  scope: SemanticParsingRuleScope
  status: SemanticParsingRuleStatus
  version: number
  createdBy?: string
  updatedBy?: string
  createdAt: string
  updatedAt: string
}

export type SemanticParsingRuleInput = Pick<
  SemanticParsingRule,
  | 'ruleType'
  | 'pattern'
  | 'matchMode'
  | 'action'
  | 'outputName'
  | 'outputCode'
  | 'minimumLength'
  | 'maximumLength'
  | 'priority'
>

export type SemanticParsingRulePage = {
  items: SemanticParsingRule[]
  total: number
  limit: number
  offset: number
}

type RuleFilters = {
  q?: string
  ruleType?: SemanticParsingRuleType | ''
  status?: SemanticParsingRuleStatus | ''
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

const rulePath = (id: string) =>
  `/v1/semantic-parsing-rules/${encodeURIComponent(id)}`

export const semanticParsingRuleAPI = {
  evaluatePermission: (action: 'READ' | 'MANAGE') =>
    apiRequest<{ allowed: boolean }>('/v1/permissions/evaluate', {
      method: 'POST',
      body: JSON.stringify({
        resourceType: 'DATASET', action, objectId: '',
      }),
    }),

  list: ({
    q = '', ruleType = '', status = '', limit = 200, offset = 0,
  }: RuleFilters = {}) =>
    apiRequest<SemanticParsingRulePage>(
      `/v1/semantic-parsing-rules?${queryString({
        q, ruleType, status, limit, offset,
      })}`,
      { cache: 'no-store' },
    ),

  create: (input: SemanticParsingRuleInput) =>
    apiRequest<SemanticParsingRule>('/v1/semantic-parsing-rules', {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  update: (
    id: string,
    expectedVersion: number,
    input: SemanticParsingRuleInput,
  ) => apiRequest<SemanticParsingRule>(rulePath(id), {
    method: 'PUT',
    body: JSON.stringify({ expectedVersion, ...input }),
  }),

  deprecate: (id: string, expectedVersion: number) =>
    apiRequest<SemanticParsingRule>(`${rulePath(id)}/deprecate`, {
      method: 'POST',
      body: JSON.stringify({ expectedVersion }),
    }),
}
