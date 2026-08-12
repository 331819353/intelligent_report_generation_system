import { apiRequest } from './api'

export type SupportTicket = {
  id: string
  category: 'QUESTION' | 'DATA' | 'REPORT' | 'ACCESS' | 'SYSTEM' | 'OTHER'
  priority: 'NORMAL' | 'HIGH' | 'URGENT'
  subject: string
  description: string
  pageUrl: string
  errorCode: string
  status: 'OPEN' | 'IN_PROGRESS' | 'RESOLVED' | 'CLOSED'
  resolutionNote: string
  reporterUserId: string
  reporterName: string
  assigneeUserId?: string
  assigneeName?: string
  recordVersion: number
  createdAt: string
  updatedAt: string
  resolvedAt?: string
}

export type CreateSupportTicket = {
  clientRequestId: string
  category: SupportTicket['category']
  priority: SupportTicket['priority']
  subject: string
  description: string
  pageUrl: string
  errorCode: string
}

export const supportAPI = {
  async list(scope: 'mine' | 'queue' = 'mine') {
    const result = await apiRequest<{ items: SupportTicket[] }>(`/v1/support-tickets?scope=${scope}&limit=100`, { businessDomain: scope !== 'queue' })
    return result.items
  },
  create(input: CreateSupportTicket) {
    return apiRequest<SupportTicket>('/v1/support-tickets', {
      method: 'POST', body: JSON.stringify(input),
    })
  },
  transition(id: string, status: 'IN_PROGRESS' | 'RESOLVED' | 'CLOSED', resolutionNote: string, recordVersion: number) {
    return apiRequest<SupportTicket>(`/v1/support-tickets/${encodeURIComponent(id)}/transition`, {
      method: 'POST', businessDomain: false, body: JSON.stringify({ status, resolutionNote, recordVersion }),
    })
  },
}
