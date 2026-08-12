import { apiRequest } from './api'

export type DecisionStatus = 'DRAFT' | 'IN_REVIEW' | 'APPROVED' | 'REJECTED' | 'IN_EXECUTION' | 'REVIEW_DUE' | 'CLOSED' | 'REOPENED' | 'CANCELED'
export type DecisionEvidenceMode = 'PLATFORM_VERIFIED' | 'MANUAL_WITHOUT_PLATFORM_EVIDENCE'
export type DecisionScope = 'MINE' | 'APPROVALS' | 'ACTIONS' | 'REVIEWS'
export type DecisionActionStatus = 'TODO' | 'DOING' | 'BLOCKED' | 'DONE' | 'CANCELED'

export type DecisionRecord = {
  schemaVersion: string
  id: string
  ownerUserId: string
  createdBy: string
  title: string
  question: string
  decision: string
  expectedEffect: string
  risks: string[]
  status: DecisionStatus
  evidenceMode: DecisionEvidenceMode
  approvalPolicyId: string
  requiredApprovals: number
  reviewAt: string
  terminalReason?: string
  recordVersion: number
  createdAt: string
  updatedAt: string
}

export type DecisionOption = {
  id: string
  title: string
  description: string
  selected: boolean
}

export type DecisionEvidence = {
  schemaVersion: string
  id: string
  sourceType: 'ANSWER_ARTIFACT' | 'REPORT_VERSION' | 'INSIGHT_ARTIFACT'
  sourceId: string
  summary: string
  verified: boolean
  asOf: string
}

export type DecisionEvidenceInput = {
  sourceType: DecisionEvidence['sourceType']
  sourceId: string
  sourceHash: string
  semanticReleaseId: string
  semanticReleaseHash: string
  dataSnapshotId?: string
  asOf: string
  policyScopeHash: string
  summary: string
}

export type DecisionApproval = {
  id: string
  approverUserId: string
  sequenceNo: number
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'CANCELED'
  comment: string
  decidedAt?: string
}

export type DecisionAction = {
  schemaVersion: string
  id: string
  decisionId: string
  title: string
  description: string
  assigneeUserId: string
  dueAt: string
  status: DecisionActionStatus
  blockReason?: string
  completionEvidence?: string
  deliverableRefs: string[]
  recordVersion: number
  createdAt: string
  updatedAt: string
}

export type DecisionOutcomeMetric = {
  id: string
  decisionId: string
  metricVersionId: string
  baselineValue: string
  targetDirection: 'INCREASE' | 'DECREASE' | 'AT_LEAST' | 'AT_MOST' | 'RANGE'
  targetValue?: string
  targetUpperValue?: string
  reviewAt: string
  attributionNote: string
  currentValue?: string
  currentAsOf?: string
  drifted: boolean
  refreshStatus: string
  recordVersion: number
}

export type DecisionOutcomeReview = {
  schemaVersion: string
  id: string
  decisionId: string
  status: 'PENDING' | 'GENERATED' | 'CONFIRMED' | 'INCONCLUSIVE'
  conclusion?: 'ACHIEVED' | 'PARTIALLY_ACHIEVED' | 'NOT_ACHIEVED' | 'INCONCLUSIVE'
  notes: string
  recordVersion: number
}

export type DecisionApprovalPolicy = {
  id: string
  name: string
  requiredApprovals: number
  approverSummary: string
}

export type DecisionAggregate = {
  decision: DecisionRecord
  options: DecisionOption[]
  evidence: DecisionEvidence[]
  approvals: DecisionApproval[]
  actions: DecisionAction[]
  outcomeMetrics: DecisionOutcomeMetric[]
  outcomeReview?: DecisionOutcomeReview
}

export type DecisionCreateInput = {
  ownerUserId: string
  title: string
  question: string
  decision: string
  expectedEffect: string
  risks: string[]
  evidenceMode: DecisionEvidenceMode
  approvalPolicyId: string
  reviewAt: string
  options: Array<{ title: string; description: string; selected: boolean }>
  evidence: DecisionEvidenceInput[]
}

const governedWriteHeaders = () => ({ 'Idempotency-Key': crypto.randomUUID() })

export const decisionAPI = {
  listApprovalPolicies() {
    return apiRequest<{ items: DecisionApprovalPolicy[] }>('/v1/decisions/approval-policies')
      .then(result => result.items)
  },
  prefillEvidence(sourceType: DecisionEvidence['sourceType'], sourceId: string) {
    const query = new URLSearchParams({ sourceType, sourceId })
    return apiRequest<{ evidence: DecisionEvidenceInput }>(`/v1/decisions/evidence-prefill?${query.toString()}`)
      .then(result => result.evidence)
  },
  list(scope: DecisionScope, limit = 200, cursor = '') {
    const query = new URLSearchParams({ scope, limit: String(limit) })
    if (cursor) query.set('cursor', cursor)
    return apiRequest<{ items: DecisionRecord[]; nextCursor?: string }>(`/v1/decisions?${query.toString()}`)
  },
  get(id: string) {
    return apiRequest<DecisionAggregate>(`/v1/decisions/${encodeURIComponent(id)}`)
  },
  create(input: DecisionCreateInput) {
    return apiRequest<DecisionAggregate>('/v1/decisions', {
      method: 'POST',
      headers: governedWriteHeaders(),
      body: JSON.stringify(input),
    })
  },
  submit(id: string, expectedVersion: number) {
    return apiRequest<DecisionAggregate>(`/v1/decisions/${encodeURIComponent(id)}/submit`, {
      method: 'POST',
      headers: governedWriteHeaders(),
      body: JSON.stringify({ expectedVersion }),
    })
  },
	decideApproval(id: string, expectedVersion: number, decision: 'APPROVE' | 'REJECT', comment: string) {
		return apiRequest<DecisionAggregate>(`/v1/decisions/${encodeURIComponent(id)}/approvals`, {
			method: 'POST',
			headers: governedWriteHeaders(),
			body: JSON.stringify({ expectedVersion, decision, comment }),
		})
	},
	createAction(id: string, input: { title: string; description: string; assigneeUserId: string; dueAt: string; deliverableRefs: string[] }) {
		return apiRequest<DecisionAction>(`/v1/decisions/${encodeURIComponent(id)}/actions`, {
			method: 'POST',
			headers: governedWriteHeaders(),
			body: JSON.stringify(input),
		})
	},
	transitionAction(decisionId: string, actionId: string, input: { expectedVersion: number; target: DecisionActionStatus; reason: string; completionEvidence: string }) {
		return apiRequest<DecisionAction>(`/v1/decisions/${encodeURIComponent(decisionId)}/actions/${encodeURIComponent(actionId)}/transition`, {
			method: 'POST',
			headers: governedWriteHeaders(),
			body: JSON.stringify(input),
		})
	},
	startReview(id: string, expectedVersion: number) {
		return apiRequest<DecisionAggregate>(`/v1/decisions/${encodeURIComponent(id)}/outcome/start`, {
			method: 'POST', headers: governedWriteHeaders(), body: JSON.stringify({ expectedVersion }),
		})
	},
	refreshOutcome(id: string) {
		return apiRequest<{ items: DecisionOutcomeMetric[] }>(`/v1/decisions/${encodeURIComponent(id)}/outcome/refresh`, {
			method: 'POST', headers: governedWriteHeaders(), body: JSON.stringify({}),
		})
	},
	confirmOutcome(id: string, input: { expectedVersion: number; conclusion: DecisionOutcomeReview['conclusion']; notes: string }) {
		return apiRequest<DecisionOutcomeReview>(`/v1/decisions/${encodeURIComponent(id)}/outcome/confirm`, {
			method: 'POST', headers: governedWriteHeaders(), body: JSON.stringify(input),
		})
	},
	transitionDecision(id: string, target: 'close' | 'reopen' | 'cancel', expectedVersion: number, reason: string) {
		return apiRequest<DecisionAggregate>(`/v1/decisions/${encodeURIComponent(id)}/${target}`, {
			method: 'POST', headers: governedWriteHeaders(), body: JSON.stringify({ expectedVersion, reason }),
		})
	},
}
