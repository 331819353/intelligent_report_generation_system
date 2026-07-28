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
}

export type AdminRole = {
  id: string
  code: string
  name: string
  description: string
  status: 'ACTIVE' | 'DISABLED'
  system: boolean
  permissionCodes: string[]
  userCount: number
}

export type AdminUserRole = {
  id: string
  code: string
  name: string
}

export type AdminUser = {
  id: string
  email: string
  displayName: string
  status: 'ACTIVE' | 'DISABLED' | 'LOCKED'
  roles: AdminUserRole[]
  domains?: Array<Pick<BusinessDomain, 'id' | 'code' | 'name' | 'default'>>
  lastLoginAt?: string
}

export type PermissionDefinition = {
  code: string
  name: string
  resourceType: string
  action: string
  description: string
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
  async createDomain(input: { code: string; name: string; description: string }) {
    return administrationRequest<BusinessDomain>('/v1/domains', {
      method: 'POST',
      body: JSON.stringify(input),
    })
  },
  async updateDomainStatus(id: string, status: BusinessDomainStatus) {
    return administrationRequest<BusinessDomain>(`/v1/domains/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ status }),
    })
  },
  async listRoles() {
    const result = await administrationRequest<ItemsResponse<AdminRole>>('/v1/roles', {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async createRole(input: { code: string; name: string; description: string }) {
    return administrationRequest<AdminRole>('/v1/roles', {
      method: 'POST',
      body: JSON.stringify(input),
    })
  },
  async listUsers() {
    const result = await administrationRequest<ItemsResponse<AdminUser>>('/v1/users', {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async listPermissions() {
    const result = await administrationRequest<ItemsResponse<PermissionDefinition>>('/v1/permissions', {
      cache: 'no-store',
    })
    return safeItems(result)
  },
  async replaceRolePermissions(roleID: string, permissionCodes: string[]) {
    return administrationRequest<void>(`/v1/roles/${roleID}/permissions`, {
      method: 'PUT',
      body: JSON.stringify({ permissionCodes }),
    })
  },
  async assignUserRole(userID: string, roleID: string) {
    return administrationRequest<void>(`/v1/users/${userID}/roles`, {
      method: 'POST',
      body: JSON.stringify({ roleId: roleID }),
    })
  },
  async revokeUserRole(userID: string, roleID: string) {
    return administrationRequest<void>(`/v1/users/${userID}/roles/${roleID}`, {
      method: 'DELETE',
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
