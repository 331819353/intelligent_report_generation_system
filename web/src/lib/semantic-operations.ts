import { apiRequest } from './api'

export type FeedbackTicketStatus = 'NEW' | 'TRIAGED' | 'ACCEPTED' | 'REJECTED' | 'FIX_PROPOSED' | 'FIX_APPROVED' | 'IN_RELEASE' | 'VERIFIED' | 'CLOSED'
export type FeedbackSeverity = 'P0' | 'P1' | 'P2'
export type FeedbackStage = 'UNDERSTANDING' | 'RETRIEVAL' | 'BINDING' | 'GRAPH' | 'COMPILE' | 'EXECUTION' | 'DATA' | 'NARRATIVE'

export type FeedbackTicket = {
  id: string
  queryFeedbackId: string
  questionRunId: string
  reporterUserId: string
  issueType: string
  severity: FeedbackSeverity
  suggestedStage: FeedbackStage
  attributedStage?: FeedbackStage
  status: FeedbackTicketStatus
  ownerUserId?: string
  slaDueAt: string
  linkedReleaseId?: string
  linkedEvaluationCaseId?: string
  resolutionNote?: string
  userResponse?: string
  fixCandidateType?: string
  fixCandidateId?: string
  recordVersion: number
  createdAt: string
  updatedAt: string
  closedAt?: string
}

export type FeedbackEvent = {
  id: string
  eventNo: number
  fromStatus?: FeedbackTicketStatus
  toStatus: FeedbackTicketStatus
  actorUserId: string
  details: Record<string, unknown>
  createdAt: string
}

export type FeedbackMetrics = { total: number; rejected: number; closed: number; overdue: number; closureRate: number }

export type ActiveLearningCandidate = {
  id: string
  taskType: string
  candidateType: string
  candidateState: string
  reviewStatus: 'PENDING' | 'APPROVED' | 'REJECTED'
  candidateKeyHash: string
  normalizedSummary: Record<string, unknown>
  evidence: Record<string, unknown>
  occurrenceCount: number
  representativeRunIds: string[]
  firstSeenAt: string
  lastSeenAt: string
}

export type FeedbackTransitionInput = {
  ExpectedVersion: number
  TargetStatus: FeedbackTicketStatus
  Severity?: FeedbackSeverity
  AttributedStage?: FeedbackStage
  OwnerUserID?: string
  ResolutionNote?: string
  UserResponse?: string
  FixCandidateType?: string
  FixCandidateID?: string
  LinkedReleaseID?: string
  LinkedEvaluationCaseID?: string
}

export const semanticOperationsAPI = {
  tickets: () => apiRequest<{ items: FeedbackTicket[] }>('/v1/askdata/feedback-tickets'),
  metrics: () => apiRequest<FeedbackMetrics>('/v1/askdata/feedback-tickets/metrics'),
  ticket: (id: string) => apiRequest<{ ticket: FeedbackTicket; events: FeedbackEvent[] }>(`/v1/askdata/feedback-tickets/${encodeURIComponent(id)}`),
  transition: (id: string, input: FeedbackTransitionInput) => apiRequest<FeedbackTicket>(`/v1/askdata/feedback-tickets/${encodeURIComponent(id)}/transition`, {
    method: 'POST', body: JSON.stringify(input),
  }),
  candidates: () => apiRequest<{ items: ActiveLearningCandidate[] }>('/v1/askdata/active-learning-candidates'),
  reviewCandidate: (id: string, decision: 'APPROVED' | 'REJECTED') => apiRequest<ActiveLearningCandidate>(`/v1/askdata/active-learning-candidates/${encodeURIComponent(id)}/review`, {
    method: 'POST', body: JSON.stringify({ decision }),
  }),
}
