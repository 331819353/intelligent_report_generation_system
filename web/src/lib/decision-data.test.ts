import assert from 'node:assert/strict'
import test from 'node:test'
import { actionProgress, decisionOwnerLabel, filterDecisions, urgentDecisions } from './decision-data.ts'
import type { DecisionRecord } from './decision-api.ts'

const decision = (patch: Partial<DecisionRecord> = {}): DecisionRecord => ({
  schemaVersion: '1.0', id: 'decision-1', ownerUserId: 'owner-1', createdBy: 'owner-1', title: '渠道策略', question: '是否调整渠道策略', decision: '调整', expectedEffect: '', risks: [], status: 'IN_REVIEW', evidenceMode: 'PLATFORM_VERIFIED', approvalPolicyId: 'policy', requiredApprovals: 1, reviewAt: '2026-08-14T00:00:00+08:00', recordVersion: 1, createdAt: '2026-08-01T00:00:00+08:00', updatedAt: '2026-08-10T00:00:00+08:00', ...patch,
})

test('decision filters use real contract fields and review date boundaries', () => {
  const result = filterDecisions([decision(), decision({ id: 'decision-2', title: '库存计划', evidenceMode: 'MANUAL_WITHOUT_PLATFORM_EVIDENCE', reviewAt: '2026-09-01T00:00:00+08:00' })], {
    query: '渠道', status: 'IN_REVIEW', evidenceMode: 'PLATFORM_VERIFIED', startDate: '2026-08-01', endDate: '2026-08-31',
  })
  assert.deepEqual(result.map(item => item.id), ['decision-1'])
})

test('drafts without a review date remain visible until a date filter is applied', () => {
  const draft = decision({ id: 'draft', status: 'DRAFT', reviewAt: '' })
  assert.deepEqual(filterDecisions([draft], { query: '', status: '', evidenceMode: '', startDate: '', endDate: '' }).map(item => item.id), ['draft'])
  assert.equal(filterDecisions([draft], { query: '', status: '', evidenceMode: '', startDate: '2026-08-01', endDate: '' }).length, 0)
})

test('urgent decisions exclude terminal states and order by review date', () => {
  const result = urgentDecisions([
    decision({ id: 'later', reviewAt: '2026-08-15T00:00:00+08:00' }),
    decision({ id: 'closed', status: 'CLOSED', reviewAt: '2026-08-11T00:00:00+08:00' }),
    decision({ id: 'sooner', reviewAt: '2026-08-12T00:00:00+08:00' }),
  ], new Date('2026-08-10T00:00:00+08:00'))
  assert.deepEqual(result.map(item => item.id), ['sooner', 'later'])
})

test('action progress ignores canceled actions and owner label masks foreign IDs', () => {
  const progress = actionProgress([
    { id: '1', status: 'DONE' }, { id: '2', status: 'DOING' }, { id: '3', status: 'CANCELED' },
  ] as never)
  assert.deepEqual(progress, { completed: 1, total: 2, percent: 50 })
  assert.equal(decisionOwnerLabel('11111111-2222-3333-4444-555555555555', 'different'), '用户 ···555555')
  assert.equal(decisionOwnerLabel('same', 'same'), '我')
})
