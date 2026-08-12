import { apiRequest } from './api'

export type ConversationSummary = {
  conversationId: string
  latestRunId: string
  label: string
  state: string
  pinned: boolean
  archived: boolean
  release: { releaseId: string; contentHash: string }
  releaseDrifted: boolean
  clarificationPending: boolean
  narrativeDegraded: boolean
  runCount: number
  recordVersion: number
  updatedAt: string
}

export type SavedQuestionSummary = {
  id: string
  name: string
  questionText: string
  visibility: 'PRIVATE' | 'TEAM' | 'CERTIFIED_CANDIDATE'
  status: string
  updatedAt: string
}

export type DecisionSummary = {
  id: string
  ownerUserId: string
  createdBy: string
  title: string
  question: string
  decision: string
  expectedEffect: string
  status: string
  reviewAt: string
  updatedAt: string
}

export type WorkInboxItem = {
  type: string
  objectId: string
  status: string
  requesterUserId?: string
  requesterDisplayName?: string
  assigneeDisplayName?: string
  priority?: 'HIGH' | 'MEDIUM' | 'LOW' | string
  slaDueAt?: string
  overdue: boolean
  domainId: string
  summary: string
  sourceHref: string
  allowedActions: string[]
  unread: boolean
  updatedAt: string
  version: string
}

export type WorkItemCommand = {
  action: string
  method: string
  href: string
  idempotencyRequired: boolean
  fixedValues?: Record<string, string>
  fields?: Array<{ name: string; type: string; required: boolean }>
}

export type WorkItemDetail = {
  item: WorkInboxItem
  actionContext: {
    expectedVersion: string
    references?: Record<string, string>
    commands: WorkItemCommand[]
  }
}

function idempotencyHeaders() {
  return { 'Idempotency-Key': crypto.randomUUID() }
}

export const homeAPI = {
  listConversations(limit = 5) {
    return apiRequest<{ items: ConversationSummary[]; nextCursor?: string }>(`/v1/conversations?limit=${limit}`)
  },

  listSavedQuestions() {
    return apiRequest<{ items: SavedQuestionSummary[] }>('/v1/askdata/saved-questions')
  },

  openSavedQuestion(id: string) {
    return apiRequest<{ runId: string; conversationId: string; replayed: boolean }>(`/v1/askdata/saved-questions/${encodeURIComponent(id)}/open`, {
      method: 'POST', headers: idempotencyHeaders(),
    })
  },

  listDecisions(limit = 5) {
    return apiRequest<{ items: DecisionSummary[]; nextCursor?: string }>(`/v1/decisions?scope=MINE&limit=${limit}`)
  },

  listWorkItems(input: { unread?: boolean; limit?: number } = {}) {
    const query = new URLSearchParams()
    query.set('unread', input.unread === false ? 'false' : 'true')
    query.set('limit', String(input.limit ?? 50))
    return apiRequest<{ items: WorkInboxItem[]; nextCursor?: string; total: number; unreadTotal: number }>(`/v1/work-items?${query}`)
  },

  markWorkItemRead(item: Pick<WorkInboxItem, 'type' | 'objectId'>) {
    return apiRequest<WorkInboxItem>(`/v1/work-items/${encodeURIComponent(item.type)}/${encodeURIComponent(item.objectId)}/read`, {
      method: 'POST',
      headers: idempotencyHeaders(),
    })
  },

  getWorkItemDetail(item: Pick<WorkInboxItem, 'type' | 'objectId'>) {
    return apiRequest<WorkItemDetail>(`/v1/work-items/${encodeURIComponent(item.type)}/${encodeURIComponent(item.objectId)}`)
  },
}
