import assert from 'node:assert/strict'
import test from 'node:test'
import type { QuestionRunEvent } from '../../lib/ask-data-api.ts'
import { buildConversationProgress, latestQuestionState } from './conversation-progress.ts'

const event = (eventIndex: number, state: QuestionRunEvent['state']): QuestionRunEvent => ({
  eventId: `00000000-0000-4000-8000-${String(eventIndex).padStart(12, '0')}`,
  eventIndex,
  runVersion: eventIndex,
  state,
  type: 'STATE_TRANSITION',
  status: 'SUCCEEDED',
  evidenceIds: [],
	graphDegraded: false,
  createdAt: `2026-08-06T10:38:${String(eventIndex).padStart(2, '0')}+08:00`,
})

test('projects a running state into controlled progress copy', () => {
  const events = [
    event(1, 'RECEIVED'),
    event(2, 'AUTHORIZED'),
    event(3, 'UNDERSTANDING'),
    event(4, 'RETRIEVING'),
    event(5, 'BINDING'),
    event(6, 'GRAPH_VALIDATING'),
    event(7, 'IR_READY'),
    event(8, 'EXECUTING'),
    event(9, 'RESULT_VERIFYING'),
  ]
  const items = buildConversationProgress('RESULT_VERIFYING', events)
  assert.deepEqual(items.map(item => item.status), [
    'complete', 'complete', 'complete', 'complete', 'complete',
    'complete', 'complete', 'complete', 'active', 'pending',
  ])
  assert.equal(items[8].detail, '正在核验结果与生成文字的事实一致性')
  assert.equal(items.some(item => item.detail.includes('SQL')), false)
})

test('keeps answer verification visible before the terminal result', () => {
  const events = [event(1, 'RESULT_VERIFYING'), event(2, 'ANSWER_VERIFYING')]
  const items = buildConversationProgress('ANSWER_VERIFYING', events)
  assert.equal(items.find(item => item.key === 'verification')?.status, 'active')
  assert.equal(items.find(item => item.key === 'complete')?.status, 'pending')
})

test('marks terminal blocked state without leaking its raw code', () => {
  const events = [event(1, 'RECEIVED'), event(2, 'BINDING'), event(3, 'BLOCKED')]
  const items = buildConversationProgress('BLOCKED', events)
  assert.equal(items.find(item => item.key === 'binding')?.status, 'blocked')
  assert.equal(items.find(item => item.key === 'graph')?.status, 'pending')
  assert.equal(items.at(-1)?.status, 'pending')
})

test('does not mark future steps complete when retrieval is blocked', () => {
  const events = [event(1, 'RECEIVED'), event(2, 'AUTHORIZED'), event(3, 'UNDERSTANDING'), event(4, 'RETRIEVING'), event(5, 'BLOCKED')]
  const items = buildConversationProgress('BLOCKED', events)
  assert.deepEqual(items.map(item => item.status), [
    'complete', 'complete', 'complete', 'blocked', 'pending',
    'pending', 'pending', 'pending', 'pending', 'pending',
  ])
})

test('uses the latest event after a bounded correction', () => {
  const events = [event(1, 'RESULT_VERIFYING'), event(2, 'BINDING')]
  assert.equal(latestQuestionState('RECEIVED', events), 'BINDING')
  const items = buildConversationProgress('BINDING', events)
  assert.equal(items.find(item => item.key === 'binding')?.status, 'active')
  assert.equal(items.find(item => item.key === 'verification')?.status, 'pending')
})
