import assert from 'node:assert/strict'
import test from 'node:test'
import { conversationToHomeWork, workItemDestination, workItemToHomeTask } from './home-data.ts'

test('conversation projection resumes the authoritative latest run', () => {
  const result = conversationToHomeWork({
    conversationId: 'conversation-id', latestRunId: 'run-id', label: '毛利率分析', state: 'ANSWERED',
    pinned: false, archived: false, release: { releaseId: 'release-id', contentHash: 'a'.repeat(64) },
    releaseDrifted: false, clarificationPending: false, narrativeDegraded: false, runCount: 2,
    recordVersion: 1, updatedAt: '2026-08-10T02:00:00Z',
  }, '企业经营')
  assert.equal(result.href, '/ask-data?runId=run-id')
  assert.equal(result.range, '2 轮 · 已完成')
})

test('work item urgency is derived from authoritative SLA without fixture priority', () => {
  const result = workItemToHomeTask({
    type: 'DATA_REQUEST', objectId: 'request-id', status: 'SUBMITTED', requesterUserId: '12345678-1234',
    slaDueAt: '2026-08-11T01:00:00Z', overdue: false, domainId: 'domain-id', summary: '取数申请待处理',
    sourceHref: '/data-requests/request-id', allowedActions: ['APPROVE'], unread: true,
    updatedAt: '2026-08-10T01:00:00Z', version: '1',
  }, new Date('2026-08-10T01:00:00Z'))
  assert.equal(result.priority, 'high')
  assert.equal(result.href, '/ask-data?workspace=data-requests')
})

test('decision work items deep-link to the authoritative decision drawer', () => {
	assert.equal(workItemDestination({
		type: 'DECISION_APPROVAL', objectId: 'notification-id', status: 'UNRESOLVED', overdue: false,
		domainId: 'domain-id', summary: '决策待审批', sourceHref: '/decisions/10000000-0000-4000-8000-000000000006', allowedActions: ['APPROVE'],
		unread: true, updatedAt: '2026-08-10T01:00:00Z', version: '1',
	}), '/decisions?decisionId=10000000-0000-4000-8000-000000000006')
})

test('feedback work items deep-link to semantic quality operations', () => {
  assert.equal(workItemDestination({
    type: 'FEEDBACK_TICKET', objectId: '10000000-0000-4000-8000-000000000010', status: 'NEW', overdue: false,
    domainId: 'domain-id', summary: '反馈工单', sourceHref: '/operations/feedback/10000000-0000-4000-8000-000000000010',
    allowedActions: ['OPEN'], unread: true, updatedAt: '2026-08-12T01:00:00Z', version: '1',
  }), '/semantic?workspace=feedback&ticketId=10000000-0000-4000-8000-000000000010')
})
