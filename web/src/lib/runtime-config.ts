import { apiRequest } from './api'

export type RuntimeConfigScope = 'TENANT' | 'DOMAIN' | 'WORKER'
export type RuntimeConfigState =
  | 'DRAFT'
  | 'IN_REVIEW'
  | 'APPROVED'
  | 'ROLLING_OUT'
  | 'ACTIVE'
  | 'SUPERSEDED'
  | 'REJECTED'
  | 'FAILED'
  | 'ROLLED_BACK'

export type RuntimeConfigDefinition = {
  key: string
  type: 'boolean' | 'integer' | 'string'
  scopeTypes: RuntimeConfigScope[]
  compatibility: 'HOT_RELOAD' | 'NEXT_RESTART'
  minimum?: number
  maximum?: number
  enum?: string[]
  description: string
}

export type DeploymentParameter = {
  name: string
  category: 'DEPLOYMENT_PARAMETER' | 'SECRET_REFERENCE'
  configured: boolean
  mutableOnline: false
  changeGuidance: string
}

export type RuntimeConfigRolloutNode = {
  id: string
  consumerType: string
  ordinal: number
  state: 'PENDING' | 'APPLIED' | 'WAITING_RESTART' | 'FAILED' | 'CANCELED'
  expectedHash: string
  appliedHash?: string
  failureCode?: string
  attempt: number
  appliedAt?: string
}

export type RuntimeConfigVersion = {
  id: string
  scopeType: RuntimeConfigScope
  scopeId: string
  versionNo: number
  baseVersionId?: string
  config: Record<string, boolean | number | string>
  configHash: string
  state: RuntimeConfigState
  compatibility: 'HOT_RELOAD' | 'NEXT_RESTART'
  impactSummary: string
  createdBy: string
  approvedBy?: string
  rejectedBy?: string
  rejectionReason?: string
  recordVersion: number
  createdAt: string
  updatedAt: string
  submittedAt?: string
  approvedAt?: string
  rejectedAt?: string
  activatedAt?: string
  rolloutNodes?: RuntimeConfigRolloutNode[]
}

type ItemsResponse<T> = { items?: T[] }
const items = <T,>(value: ItemsResponse<T>) => Array.isArray(value.items) ? value.items : []
const governedHeaders = () => ({ 'Idempotency-Key': crypto.randomUUID() })
const request = <T,>(path: string, init: RequestInit = {}) => apiRequest<T>(path, {
  ...init,
  businessDomain: false,
  cache: init.method ? init.cache : 'no-store',
})

/** 平台运行配置控制面；所有写入都保留服务端版本锁和幂等边界。 */
export const runtimeConfigAPI = {
  async definitions() {
    return items(await request<ItemsResponse<RuntimeConfigDefinition>>('/v1/runtime-config/definitions'))
  },
  async deploymentParameters() {
    return items(await request<ItemsResponse<DeploymentParameter>>('/v1/runtime-config/deployment-parameters'))
  },
  async list(limit = 100) {
    return items(await request<ItemsResponse<RuntimeConfigVersion>>(`/v1/runtime-config/versions?limit=${limit}`))
  },
  get(id: string) {
    return request<RuntimeConfigVersion>(`/v1/runtime-config/versions/${encodeURIComponent(id)}`)
  },
  create(input: {
    scopeType: RuntimeConfigScope
    scopeId: string
    baseVersionId?: string
    config: Record<string, boolean | number | string>
    impactSummary: string
  }) {
    return request<RuntimeConfigVersion>('/v1/runtime-config/versions', {
      method: 'POST', headers: governedHeaders(), body: JSON.stringify(input),
    })
  },
  transition(id: string, operation: 'submit' | 'approve' | 'apply' | 'rollback', expectedVersion: number) {
    return request<RuntimeConfigVersion>(`/v1/runtime-config/versions/${encodeURIComponent(id)}/${operation}`, {
      method: 'POST', headers: governedHeaders(), body: JSON.stringify({ expectedVersion }),
    })
  },
  reject(id: string, expectedVersion: number, reason: string) {
    return request<RuntimeConfigVersion>(`/v1/runtime-config/versions/${encodeURIComponent(id)}/reject`, {
      method: 'POST', headers: governedHeaders(), body: JSON.stringify({ expectedVersion, reason }),
    })
  },
  acknowledgeRestart(versionId: string, nodeId: string) {
    return request<RuntimeConfigVersion>(`/v1/runtime-config/versions/${encodeURIComponent(versionId)}/rollout-nodes/${encodeURIComponent(nodeId)}/restart-ack`, {
      method: 'POST', headers: governedHeaders(), body: '{}',
    })
  },
}
