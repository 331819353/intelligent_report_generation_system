import assert from 'node:assert/strict'
import test from 'node:test'

import type { QuestionScopeVerdict } from '../../lib/ask-data-api.ts'
import { detailDataRequestAction } from './scope-exit.ts'

const detailVerdict: QuestionScopeVerdict = {
  schemaVersion: 'question-scope-verdict-v1',
  type: 'DETAIL_LIST', outcome: 'OUT_OF_SCOPE', reason: 'SCOPE_DETAIL_LIST',
  userMessage: '明细数据请提交取数申请。',
  nextActions: [{
    kind: 'DATA_REQUEST', label: '发起明细取数申请',
    payload: { target: 'DATA_REQUEST_DIALOG', prefill: 'CURRENT_QUESTION' },
  }],
  parsedContext: { metricIds: ['00000000-0000-4000-8000-000000000101'] },
  lexiconVersion: 'askdata-scope-lexicon-2026.08', lexiconHash: 'a'.repeat(64), classificationSource: 'RULE',
}

test('detail-list scope exit exposes the approved data-request entry', () => {
  assert.equal(detailDataRequestAction(detailVerdict)?.label, '发起明细取数申请')
})

test('other out-of-scope reasons never expose a detail data-request button', () => {
  assert.equal(detailDataRequestAction({
    ...detailVerdict,
    type: 'FORECAST', reason: 'SCOPE_FORECAST',
    nextActions: [{
      kind: 'REPHRASE', label: '改问历史表现',
      payload: { target: 'ASK_DATA_COMPOSER', prefill: 'CURRENT_QUESTION' },
    }],
  }), undefined)
  assert.equal(detailDataRequestAction({ ...detailVerdict, outcome: 'BLOCKED' }), undefined)
})
