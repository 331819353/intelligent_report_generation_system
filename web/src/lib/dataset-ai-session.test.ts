import assert from 'node:assert/strict'
import test from 'node:test'
import { conversationFromDatasetAISession, pendingDatasetAIBlueprintStages, type DatasetAISessionState } from './dataset-ai-session.ts'

test('rebuilds an ordered conversation from persisted proposals and clarifications', () => {
  const state: DatasetAISessionState = {
    goal: '构建销售订单事实表',
    modelKind: 'DWD',
    proposals: [
      {
        requestId: 'req-1', mode: 'MODIFY', summary: '第一版方案', instruction: '构建订单明细',
        status: 'SUPERSEDED', createdAt: '2026-08-14T10:00:00Z', updatedAt: '2026-08-14T10:05:00Z',
      },
      {
        requestId: 'req-2', mode: 'MODIFY', summary: '第二版方案', instruction: '增加金额汇总',
        status: 'APPLIED', createdAt: '2026-08-14T10:10:00Z', updatedAt: '2026-08-14T10:12:00Z',
      },
    ],
    clarifications: [{
      question: '你想调整哪个分组？',
      askedAt: '2026-08-14T10:05:00Z',
      answer: '输出分组',
      answeredAt: '2026-08-14T10:06:00Z',
    }],
  }
  const conversation = conversationFromDatasetAISession(state)
  assert.equal(conversation.lastInstruction, '增加金额汇总')
  assert.deepEqual(conversation.entries.map(entry => `${entry.role}:${entry.content}`), [
    'USER:构建订单明细',
    'ASSISTANT:第一版方案',
    'ASSISTANT:需要确认：你想调整哪个分组？',
    'USER:输出分组',
    'USER:增加金额汇总',
    'ASSISTANT:第二版方案',
  ])
  assert.equal(conversation.entries[1].status, 'SUPERSEDED')
  assert.equal(conversation.entries[5].status, 'APPLIED')
})

test('a previously staged proposal is restored as history, never as an applicable card', () => {
  const state: DatasetAISessionState = {
    proposals: [{
      requestId: 'req-1', mode: 'CREATE', summary: '候选方案', instruction: '构建维度表',
      status: 'STAGED', createdAt: '2026-08-14T10:00:00Z', updatedAt: '2026-08-14T10:00:00Z',
    }],
  }
  const conversation = conversationFromDatasetAISession(state)
  assert.equal(conversation.entries[1].status, 'SUPERSEDED')
})

test('an unanswered clarification restores without a fabricated answer entry', () => {
  const state: DatasetAISessionState = {
    clarifications: [{ question: '要修改哪个字段？', askedAt: '2026-08-14T10:00:00Z' }],
  }
  const conversation = conversationFromDatasetAISession(state)
  assert.deepEqual(conversation.entries.map(entry => entry.content), ['需要确认：要修改哪个字段？'])
  assert.equal(conversation.lastInstruction, '')
})

test('only proposed blueprint stages block generation', () => {
  const state: DatasetAISessionState = {
    blueprint: {
      generatedAt: '2026-08-16T10:00:00Z',
      stages: [
        { stage: 'GRAIN', status: 'AUTO_CONFIRMED', source: 'LLM', confidence: 0.9, needsUserConfirmation: false, decidedAt: '2026-08-16T10:00:00Z' },
        { stage: 'JOIN', status: 'PROPOSED', source: 'LLM', confidence: 0.6, needsUserConfirmation: true, decidedAt: '2026-08-16T10:00:00Z' },
        { stage: 'TRANSFORM', status: 'SKIPPED', source: 'LLM', confidence: 1, needsUserConfirmation: false, decidedAt: '2026-08-16T10:00:00Z' },
        { stage: 'OUTPUT', status: 'USER_CONFIRMED', source: 'USER', confidence: 1, needsUserConfirmation: false, decidedAt: '2026-08-16T10:00:00Z' },
      ],
    },
  }
  assert.deepEqual(pendingDatasetAIBlueprintStages(state.blueprint), ['JOIN'])
  assert.deepEqual(pendingDatasetAIBlueprintStages(undefined), [])
})
