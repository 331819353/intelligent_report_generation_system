import { apiRequest } from '../../lib/api'
import type { ReportAction, ReportAsset, ReportLifecycle, ReportScope } from '../assets/model'

export type PermissionGrant = {
  id: string
  subjectType: 'USER' | 'ROLE'
  subjectId: string
  subjectName: string
  action: Extract<ReportAction, 'VIEW' | 'EDIT' | 'PUBLISH' | 'EXPORT' | 'SHARE' | 'AI_EDIT'>
  grantedBy?: string
  createdAt: string
}

export type AssetEvent = {
  id: string
  eventType: string
  actorUserId?: string
  actorName?: string
  subjectType?: string
  subjectId?: string
  action?: string
  reason?: string
  previousStatus?: string
  newStatus?: string
  payload: Record<string, unknown>
  createdAt: string
}

export type CreatedReportShare = {
  share: { id: string; reportId: string; shareType: 'INTERNAL_USER' | 'INTERNAL_GROUP'; principalId: string; expiresAt: string }
  token: string
}

type AssetPage = { items: Array<Omit<ReportAsset, 'ownerAvatar'>>; nextCursor?: string }

function idempotencyHeaders() {
  return { 'Idempotency-Key': crypto.randomUUID() }
}

export const reportAssetsAPI = {
  async list(input: { scope?: ReportScope; lifecycle?: ReportLifecycle | 'ALL'; search?: string; cursor?: string; limit?: number } = {}) {
    const query = new URLSearchParams()
    query.set('limit', String(input.limit ?? 100))
    if (input.scope && input.scope !== 'all') query.set('scope', input.scope)
    if (input.lifecycle && input.lifecycle !== 'ALL') query.set('lifecycle', input.lifecycle)
    if (input.search?.trim()) query.set('search', input.search.trim())
    if (input.cursor) query.set('cursor', input.cursor)
    return apiRequest<AssetPage>(`/v1/reports?${query}`)
  },
  listPermissions(reportId: string) {
    return apiRequest<{ items: PermissionGrant[] }>(`/v1/reports/${encodeURIComponent(reportId)}/permissions`)
  },
  grantPermission(reportId: string, input: Pick<PermissionGrant, 'subjectType' | 'subjectId' | 'action'>) {
    return apiRequest<PermissionGrant>(`/v1/reports/${encodeURIComponent(reportId)}/permissions`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
  revokePermission(reportId: string, grantId: string) {
    return apiRequest<{ revoked: true; grant: PermissionGrant }>(`/v1/reports/${encodeURIComponent(reportId)}/permissions/${encodeURIComponent(grantId)}`, {
      method: 'DELETE', headers: idempotencyHeaders(), body: JSON.stringify({}),
    })
  },
  archive(reportId: string, reason: string) {
    return apiRequest<{ reportId: string; status: 'ARCHIVED' }>(`/v1/reports/${encodeURIComponent(reportId)}/archive`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ reason }),
    })
  },
  restore(reportId: string, reason: string) {
    return apiRequest<{ reportId: string; status: 'ACTIVE' }>(`/v1/reports/${encodeURIComponent(reportId)}/restore`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify({ reason }),
    })
  },
  delete(reportId: string, reason: string) {
    return apiRequest<{ reportId: string; deleted: true }>(`/v1/reports/${encodeURIComponent(reportId)}`, {
      method: 'DELETE', headers: idempotencyHeaders(), body: JSON.stringify({ reason }),
    })
  },
  listEvents(reportId: string) {
    return apiRequest<{ items: AssetEvent[] }>(`/v1/reports/${encodeURIComponent(reportId)}/asset-events?limit=100`)
  },
  createShare(reportId: string, input: { shareType: 'INTERNAL_USER' | 'INTERNAL_GROUP'; principalId: string; expiresAt?: string }) {
    return apiRequest<CreatedReportShare>(`/v1/reports/${encodeURIComponent(reportId)}/shares`, {
      method: 'POST', headers: idempotencyHeaders(), body: JSON.stringify(input),
    })
  },
}
