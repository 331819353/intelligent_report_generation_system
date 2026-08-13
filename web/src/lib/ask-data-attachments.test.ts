import assert from 'node:assert/strict'
import test from 'node:test'
import {
  askDataAttachmentLimit,
  askDataAttachmentMaxBytes,
  buildAskDataAttachmentContext,
  type AskDataAttachmentDraftItem,
} from './ask-data-attachments.ts'

function attachment(name: string, excerpt: string): AskDataAttachmentDraftItem {
  return { id: name, name, size: excerpt.length, type: 'text/plain', excerpt }
}

test('attachment context keeps filenames and bounded text evidence', () => {
  const context = buildAskDataAttachmentContext([
    attachment('sales.csv', 'month,sales\n2026-08,96'),
    attachment('notes.md', '华东区域促销减少'),
  ])
  assert.match(context, /sales\.csv/)
  assert.match(context, /2026-08,96/)
  assert.match(context, /notes\.md/)
  assert.ok(context.length <= 3000)
})

test('empty attachment selection adds no hidden context', () => {
  assert.equal(buildAskDataAttachmentContext([]), '')
  assert.equal(askDataAttachmentLimit, 3)
  assert.equal(askDataAttachmentMaxBytes, 1024 * 1024)
})
