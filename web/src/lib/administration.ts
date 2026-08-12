import { apiRequest } from './api'

export type BusinessDomainStatus = 'ACTIVE' | 'DISABLED'
export type BusinessDomain = {
  id: string
  code: string
  name: string
  description: string
  status: BusinessDomainStatus
  default: boolean
  version: number
  createdAt: string
  accessSensitivity: 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED'
  administrators: DomainAdministrator[]
}

export type DomainAdministrator = {
  id: string
  employeeNo: string
  email: string
  displayName: string
}

export type DomainCatalogItem = BusinessDomain & {
  accessStatus: 'AVAILABLE' | 'PENDING' | 'APPROVED' | 'REJECTED' | 'CANCELLED' | 'MEMBER' | 'DOMAIN_ADMIN' | 'PLATFORM_ADMIN'
}

export type DomainApplication = {
  id: string
  domainId: string
  domainCode: string
  domainName: string
  applicantUserId: string
  applicantEmail: string
  applicantDisplayName: string
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'CANCELLED'
  reason: string
  reviewComment: string
  reviewedBy?: string
  reviewedAt?: string
  createdAt: string
  // Two-seat approval governance. Every field below is derived server-side; the
  // browser never decides whether a security co-signature is required.
  domainSensitivity: 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED'
  requiresDualApproval: boolean
  slaDueAt?: string
  slaStatus: 'ON_TRACK' | 'DUE_SOON' | 'OVERDUE' | 'NOT_APPLICABLE'
  escalationLevel: number
  approvals: DomainAccessApproval[]
  approvedRoles: DomainReviewRole[]
  actorApproval?: 'APPROVED' | 'REJECTED'
  openSeats: DomainReviewRole[]
  escalations: DomainAccessEscalation[]
}

export type DomainReviewRole = 'DOMAIN_OWNER' | 'SECURITY'

export type DomainAccessApproval = {
  reviewerId: string
  reviewerDisplayName: string
  reviewRole: DomainReviewRole
  decision: 'APPROVED' | 'REJECTED'
  comment: string
  createdAt: string
}

export type DomainAccessEscalation = {
  level: number
  escalatedBy: string
  note: string
  createdAt: string
}

export type AdminUserDomain = {
  id: string
  code: string
  name: string
  default: boolean
  memberRole: 'MEMBER' | 'DOMAIN_ADMIN'
}

export type AdminUser = {
  id: string
  employeeNo: string
  email: string
  displayName: string
  status: 'ACTIVE' | 'DISABLED' | 'LOCKED'
  platformAdministrator: boolean
  domains: AdminUserDomain[]
  lastLoginAt?: string
  createdAt: string
}

// A reader's administered business attributes. Governed row access policies
// match against these, so they are granted by an administrator and never
// self-asserted.
export type SubjectAttribute = {
  userId: string
  attributeKey: string
  attributeValues: string[]
  grantedBy: string
  updatedAt: string
  displayName?: string
}

export type ShareTarget = {
  id: string
  type: 'USER' | 'ROLE'
  name: string
  detail: string
}

export type PlatformApproval = {
  id: string
  kind: 'DOMAIN_ACCESS' | 'DATA_SOURCE' | 'DATASET'
  version: number
  resourceId: string
  resourceName: string
  domainId: string
  domainCode: string
  domainName: string
  requesterUserId: string
  requesterEmail: string
  requesterDisplayName: string
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'WITHDRAWN' | 'CANCELLED'
  note: string
  reviewerDisplayName?: string
  submittedAt: string
  reviewedAt?: string
  // Two-seat governance, populated for DOMAIN_ACCESS requests only. Other
  // approval kinds report no open seats and no SLA.
  requiresDualApproval: boolean
  approvedRoles: DomainReviewRole[]
  openSeats: DomainReviewRole[]
  slaDueAt?: string
  slaStatus: 'ON_TRACK' | 'DUE_SOON' | 'OVERDUE' | 'NOT_APPLICABLE'
  escalationLevel: number
}

export type PlatformAuditLog = {
  id: string
  action: string
  resourceType: string
  resourceId: string
  result: 'SUCCESS' | 'FAILURE' | 'DENIED'
  actorDisplayName: string
  actorEmail: string
  requestId?: string
  occurredAt: string
}

export type UserLifecycleDisposition = 'TRANSFER' | 'AUTO_CLOSE' | 'READ_ONLY' | 'BLOCK'

export type UserLifecycleItem = {
  category: string
  domainId: string
  objectId: string
  disposition: UserLifecycleDisposition
  receiverUserId?: string
  sourceVersion: string
  executedAt?: string
}

export type UserDeactivationPreview = {
  targetUserId: string
  items: UserLifecycleItem[]
  counts: Record<string, number>
  canDisable: boolean
}

export type UserLifecycleMapping = {
  category: string
  domainId: string
  receiverUserId: string
}

export type UserLifecycleBatch = {
  id: string
  targetUserId: string
  status: 'PLANNED' | 'EXECUTING' | 'COMPLETED' | 'TRANSFER_FAILED'
  planHash: string
  failureCode?: string
  recordVersion: number
  items: UserLifecycleItem[]
  createdAt: string
  updatedAt: string
  completedAt?: string
}

type ItemsResponse<T> = { items?: T[] }

const safeItems = <T,>(value: ItemsResponse<T>) =>
  Array.isArray(value.items) ? value.items : []
const administrationRequest = <T,>(path: string, init: RequestInit = {}) =>
  apiRequest<T>(path, { ...init, businessDomain: false })
const domainAssetRequest = <T,>(domainID: string, path: string, init: RequestInit = {}) => {
  const headers: Record<string, string> = {}
  new Headers(init.headers).forEach((value, key) => { headers[key] = value })
  headers['X-Business-Domain-ID'] = domainID
  return apiRequest<T>(path, {
    ...init,
    businessDomain: false,
    headers,
  })
}

/** 管理中心与领域切换器共用的平台治理 API。 */
export const administrationAPI = {
  async canManage() {
    const result = await administrationRequest<{ platformAdministrator: boolean }>(
      '/v1/platform-management/access',
      { cache: 'no-store' },
    )
    return Boolean(result.platformAdministrator)
  },
  async listDomains() {
    const result = await administrationRequest<ItemsResponse<BusinessDomain>>('/v1/domains', {
      cache: 'no-store',
    })
    return safeItems(result).filter(item =>
      Boolean(item?.id && item?.code && item?.name && item?.status),
    )
  },
  async listManagedDomains() {
    const result = await administrationRequest<ItemsResponse<BusinessDomain>>('/v1/managed-domains', {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async createDomain(input: {
    code: string
    name: string
    description: string
  }) {
    return administrationRequest<BusinessDomain>('/v1/domains', {
      method: 'POST',
      body: JSON.stringify(input),
    })
  },
  async updateDomainStatus(domainID: string, status: BusinessDomainStatus) {
    return administrationRequest<BusinessDomain>(`/v1/domains/${domainID}`, {
      method: 'PATCH',
      body: JSON.stringify({ status }),
    })
  },
  async replaceDomainAdministrators(domainID: string, userIds: string[]) {
    return administrationRequest<void>(`/v1/domains/${domainID}/administrators`, {
      method: 'PUT',
      body: JSON.stringify({ userIds }),
    })
  },
  async listDomainCatalog() {
    const result = await administrationRequest<ItemsResponse<DomainCatalogItem>>('/v1/domain-catalog', {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async applyDomain(domainID: string, reason: string) {
    return administrationRequest<DomainApplication>(`/v1/domains/${domainID}/applications`, {
      method: 'POST',
      body: JSON.stringify({ reason }),
    })
  },
  async listMyDomainApplications() {
    const result = await administrationRequest<ItemsResponse<DomainApplication>>('/v1/domain-applications', {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async withdrawDomainApplication(id: string) {
    return administrationRequest<void>(`/v1/domain-applications/${encodeURIComponent(id)}/withdraw`, {
      method: 'POST',
    })
  },
  async listPendingDomainApplications(domainID: string) {
    const result = await administrationRequest<ItemsResponse<DomainApplication>>(`/v1/domains/${domainID}/applications`, {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async reviewDomainApplication(
    id: string,
    decision: 'APPROVED' | 'REJECTED',
    comment = '',
    reviewRole: 'DOMAIN_OWNER' | 'SECURITY' = 'DOMAIN_OWNER',
  ) {
    return administrationRequest<void>(`/v1/domain-applications/${id}/decision`, {
      method: 'POST',
      body: JSON.stringify({ decision, comment, reviewRole }),
    })
  },
  async escalateDomainApplication(id: string, note = '') {
    return administrationRequest<{ escalationLevel: number }>(`/v1/domain-applications/${encodeURIComponent(id)}/escalate`, {
      method: 'POST',
      body: JSON.stringify({ note }),
    })
  },
  async updateDomainAccessSensitivity(
    id: string,
    accessSensitivity: 'PUBLIC' | 'INTERNAL' | 'CONFIDENTIAL' | 'RESTRICTED',
  ) {
    return administrationRequest<BusinessDomain>(`/v1/domains/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify({ accessSensitivity }),
    })
  },
  async listSubjectAttributes(userID: string) {
    const result = await administrationRequest<ItemsResponse<SubjectAttribute>>(
      `/v1/users/${encodeURIComponent(userID)}/subject-attributes`, { cache: 'no-store' },
    )
    return safeItems(result)
  },
  async setSubjectAttribute(userID: string, attributeKey: string, attributeValues: string[]) {
    return administrationRequest<SubjectAttribute>(
      `/v1/users/${encodeURIComponent(userID)}/subject-attributes/${encodeURIComponent(attributeKey)}`,
      { method: 'PUT', body: JSON.stringify({ attributeValues }) },
    )
  },
  async deleteSubjectAttribute(userID: string, attributeKey: string) {
    return administrationRequest<void>(
      `/v1/users/${encodeURIComponent(userID)}/subject-attributes/${encodeURIComponent(attributeKey)}`,
      { method: 'DELETE' },
    )
  },
  async listUsers() {
    const result = await administrationRequest<ItemsResponse<AdminUser>>('/v1/users', {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async listShareTargets() {
    const result = await apiRequest<ItemsResponse<ShareTarget>>('/v1/share-targets', { cache: 'no-store' })
    return safeItems(result)
  },
  async listPlatformApprovals(limit = 100) {
    const result = await administrationRequest<ItemsResponse<PlatformApproval>>(`/v1/platform-management/approvals?limit=${limit}`, {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async reviewPublication(
    approval: Pick<PlatformApproval, 'domainId' | 'id' | 'kind' | 'resourceId' | 'version'>,
    decision: 'APPROVED' | 'REJECTED',
    reason = '',
    reviewRole: DomainReviewRole = 'DOMAIN_OWNER',
  ) {
    const reviewReason = decision === 'APPROVED'
      ? '平台管理中心审核通过'
      : reason.trim() || '申请不符合当前发布要求'
    if (approval.kind === 'DOMAIN_ACCESS') {
      return administrationRequest<void>(`/v1/domain-applications/${approval.id}/decision`, {
        method: 'POST',
        body: JSON.stringify({
          decision,
          comment: reviewReason,
          reviewRole,
        }),
      })
    }
    const collection = approval.kind === 'DATA_SOURCE' ? 'data-sources' : 'datasets'
    const operation = decision === 'APPROVED' ? 'approve' : 'reject'
    const body = approval.kind === 'DATA_SOURCE'
      ? { expectedVersion: approval.version, reason: reviewReason }
      : decision === 'APPROVED'
        ? { expectedVersion: approval.version, note: '平台管理中心审核通过' }
        : { expectedVersion: approval.version, reason: reviewReason }
    return domainAssetRequest(
      approval.domainId,
      `/v1/${collection}/${encodeURIComponent(approval.resourceId)}/publish-requests/${encodeURIComponent(approval.id)}/${operation}`,
      { method: 'POST', body: JSON.stringify(body) },
    )
  },
  async listPlatformAuditLogs(limit = 100) {
    const result = await administrationRequest<ItemsResponse<PlatformAuditLog>>(`/v1/platform-management/audit-logs?limit=${limit}`, {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async updateUserStatus(userID: string, status: 'ACTIVE' | 'DISABLED') {
    return administrationRequest<void>(`/v1/users/${userID}`, {
      method: 'PATCH',
      body: JSON.stringify({ status }),
    })
  },
  async previewUserDeactivation(userID: string) {
    return administrationRequest<UserDeactivationPreview>(`/v1/users/${userID}/deactivation-preview`, {
      cache: 'no-store',
    })
  },
  async executeUserDeactivation(userID: string, mappings: UserLifecycleMapping[]) {
    return administrationRequest<UserLifecycleBatch>(`/v1/users/${userID}/deactivation-batches`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ mappings }),
    })
  },
  async retryUserDeactivation(batchID: string, expectedVersion: number) {
    return administrationRequest<UserLifecycleBatch>(`/v1/user-lifecycle-batches/${batchID}/retry`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ expectedVersion }),
    })
  },
  async setPlatformAdministrator(userID: string, enabled: boolean) {
    return administrationRequest<void>(`/v1/users/${userID}/platform-administrator`, {
      method: 'PUT',
      body: JSON.stringify({ enabled }),
    })
  },
  async assignUserDomain(userID: string, domainID: string) {
    return administrationRequest<void>(`/v1/users/${userID}/domains`, {
      method: 'POST',
      body: JSON.stringify({ domainId: domainID }),
    })
  },
  async revokeUserDomain(userID: string, domainID: string) {
    return administrationRequest<void>(`/v1/users/${userID}/domains/${domainID}`, {
      method: 'DELETE',
    })
  },
}
