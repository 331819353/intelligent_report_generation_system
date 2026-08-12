import assert from 'node:assert/strict'
import test from 'node:test'
import type { WorkInboxItem } from './home-api.ts'
import { canRunInlineTaskAction, taskResourceID } from './task-actions-model.ts'

const item = (input: Partial<WorkInboxItem>): WorkInboxItem => ({
  type: 'DOMAIN_ACCESS_APPROVAL', objectId: '10000000-0000-4000-8000-000000000001', status: 'PENDING',
  overdue: false, domainId: '20000000-0000-4000-8000-000000000001', summary: '待审批', sourceHref: '',
  allowedActions: ['APPROVE', 'REJECT'], unread: true, updatedAt: '2026-08-10T08:00:00Z', version: '1',
  ...input,
})

test('inline task actions cover every source that publishes a governed work-item command', () => {
  assert.equal(canRunInlineTaskAction(item({}), 'APPROVE'), true)
	assert.equal(canRunInlineTaskAction(item({ type: 'DECISION_APPROVAL' }), 'APPROVE'), true)
	assert.equal(canRunInlineTaskAction(item({ type: 'ACTION_ASSIGNED' }), 'COMPLETE'), true)
	assert.equal(canRunInlineTaskAction(item({ type: 'RUNTIME_CONFIG_APPROVAL' }), 'REJECT'), true)
	assert.equal(canRunInlineTaskAction(item({ type: 'FEEDBACK_TICKET' }), 'APPROVE'), false)
})

test('resource ids are parsed only from canonical governed source links', () => {
  const id = '30000000-0000-4000-8000-000000000001'
  assert.equal(taskResourceID(item({ sourceHref: `/governance/data-sources/${id}` }), 'data-sources'), id)
  assert.equal(taskResourceID(item({ sourceHref: `/governance/datasets/${id}/edit` }), 'datasets'), undefined)
})
