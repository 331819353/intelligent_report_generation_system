import assert from 'node:assert/strict'
import test from 'node:test'
import { formatConversationTime, groupConversations } from './conversation-data.ts'
import type { ConversationSummary } from './ask-data-api.ts'

const fixture = (id: string, updatedAt: string, pinned = false): ConversationSummary => ({
  conversationId: id, latestRunId: id, label: id, state: 'ANSWERED', pinned, archived: false,
  release: { releaseId: id, contentHash: 'a'.repeat(64) }, releaseDrifted: false,
  clarificationPending: false, narrativeDegraded: false, runCount: 1, recordVersion: 1, updatedAt,
})

test('groups pinned and recent conversations in stable sections', () => {
  const now = new Date('2026-08-10T12:00:00+08:00')
  const groups = groupConversations([
    fixture('today', '2026-08-10T10:12:00+08:00'),
    fixture('yesterday', '2026-08-09T09:10:00+08:00'),
    fixture('pinned', '2026-08-01T09:10:00+08:00', true),
  ], now)
  assert.deepEqual(groups.map(group => group.label), ['置顶', '今天', '昨天'])
  assert.equal(groups[0].items[0].conversationId, 'pinned')
  assert.equal(formatConversationTime('2026-08-10T10:12:00+08:00', now), '10:12')
})
