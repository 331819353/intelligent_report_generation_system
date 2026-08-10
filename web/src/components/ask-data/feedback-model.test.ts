import assert from 'node:assert/strict'
import test from 'node:test'

import { FEEDBACK_ISSUES, feedbackIssue } from './feedback-model.ts'

test('structured feedback exposes every governed negative issue exactly once', () => {
  assert.equal(FEEDBACK_ISSUES.length, 9)
  assert.equal(new Set(FEEDBACK_ISSUES.map(issue => issue.type)).size, 9)
  assert.deepEqual(FEEDBACK_ISSUES.map(issue => issue.type), [
    'METRIC', 'DIMENSION', 'MEMBER', 'TIME', 'RELATIONSHIP',
    'DATA', 'PERMISSION', 'EXPRESSION', 'OTHER',
  ])
  assert.match(feedbackIssue('PERMISSION')?.helper ?? '', /敏感信息/)
  assert.equal(feedbackIssue('NONE'), undefined)
})
