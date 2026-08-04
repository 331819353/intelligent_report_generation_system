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
  administrators: DomainAdministrator[]
}

export type DomainAdministrator = {
  id: string
  email: string
  displayName: string
}

export type DomainCatalogItem = BusinessDomain & {
  accessStatus: 'AVAILABLE' | 'PENDING' | 'APPROVED' | 'REJECTED' | 'MEMBER' | 'DOMAIN_ADMIN'
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
  email: string
  displayName: string
  status: 'ACTIVE' | 'DISABLED' | 'LOCKED'
  platformAdministrator: boolean
  domains: AdminUserDomain[]
  lastLoginAt?: string
}

type ItemsResponse<T> = { items?: T[] }

const safeItems = <T,>(value: ItemsResponse<T>) =>
  Array.isArray(value.items) ? value.items : []
const administrationRequest = <T,>(path: string, init: RequestInit = {}) =>
  apiRequest<T>(path, { ...init, businessDomain: false })

/** 管理中心与领域切换器共用的租户管理 API。 */
export const administrationAPI = {
  async canManage() {
    const result = await administrationRequest<{ allowed: boolean }>('/v1/permissions/evaluate', {
      method: 'POST',
      body: JSON.stringify({ resourceType: 'USER', action: 'MANAGE' }),
    })
    return Boolean(result.allowed)
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
    administratorUserIds: string[]
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
  async listPendingDomainApplications(domainID: string) {
    const result = await administrationRequest<ItemsResponse<DomainApplication>>(`/v1/domains/${domainID}/applications`, {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async reviewDomainApplication(id: string, decision: 'APPROVED' | 'REJECTED', comment = '') {
    return administrationRequest<void>(`/v1/domain-applications/${id}/decision`, {
      method: 'POST',
      body: JSON.stringify({ decision, comment }),
    })
  },
  async listUsers() {
    const result = await administrationRequest<ItemsResponse<AdminUser>>('/v1/users', {
      cache: 'no-store',
    })
    return safeItems(result)
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
