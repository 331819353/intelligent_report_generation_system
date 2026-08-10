import assert from 'node:assert/strict'
import test from 'node:test'

import type { CreateDataRequestInput, DataRequestFieldOption } from './model.ts'
import {
  dataRequestStepStatus,
  deriveDataRequestSensitivity,
  sanitizeDataRequestContext,
  validateDataRequestDraft,
} from './model.ts'

const fields: DataRequestFieldOption[] = [
  { datasetId: 'a', datasetName: '订单', datasetVersionId: 'v1', fieldId: 'order_no', fieldCode: 'order_no', fieldName: '订单号', sensitivityLevel: 'INTERNAL' },
  { datasetId: 'a', datasetName: '订单', datasetVersionId: 'v1', fieldId: 'phone', fieldCode: 'phone', fieldName: '手机号', sensitivityLevel: 'CONFIDENTIAL' },
]

test('question prefill only keeps governed semantic ids and parsed time', () => {
  const sanitized = sanitizeDataRequestContext({
    metricIds: ['11111111-1111-4111-8111-111111111111'],
    dimensionIds: ['22222222-2222-4222-8222-222222222222'],
    memberIds: ['33333333-3333-4333-8333-333333333333'],
    timeRange: { start: '2026-08-01T00:00:00+08:00', endExclusive: '2026-09-01T00:00:00+08:00', timezone: 'Asia/Shanghai', grain: 'MONTH', rows: [{ orderNo: 'secret' }] },
    rows: [{ orderNo: 'secret' }],
    result: { rows: [['secret']] },
    sql: 'select * from orders',
    answer: 'secret',
  })
  assert.deepEqual(sanitized, {
    metricIds: ['11111111-1111-4111-8111-111111111111'],
    dimensionIds: ['22222222-2222-4222-8222-222222222222'],
    memberIds: ['33333333-3333-4333-8333-333333333333'],
    timeRange: { start: '2026-08-01T00:00:00+08:00', endExclusive: '2026-09-01T00:00:00+08:00', timezone: 'Asia/Shanghai', grain: 'MONTH' },
  })
  assert.equal(JSON.stringify(sanitized).includes('secret'), false)
  assert.equal(JSON.stringify(sanitized).includes('select'), false)
})

test('sensitivity is the highest governed field level and stays read-only derived state', () => {
  assert.equal(deriveDataRequestSensitivity([], fields), 'PUBLIC')
  assert.equal(deriveDataRequestSensitivity([{ datasetVersionId: 'v1', fieldId: 'order_no' }], fields), 'INTERNAL')
  assert.equal(deriveDataRequestSensitivity([
    { datasetVersionId: 'v1', fieldId: 'order_no' },
    { datasetVersionId: 'v1', fieldId: 'phone' },
  ], fields), 'CONFIDENTIAL')
})

test('draft validation enforces purpose, fields and future SLA', () => {
  const now = new Date('2026-08-08T00:00:00.000Z')
  const valid: CreateDataRequestInput = {
    requestText: '导出订单明细', parsedContext: {}, businessPurpose: '月度复盘',
    requiredFields: [{ datasetVersionId: 'v1', fieldId: 'order_no' }],
    slaDueAt: '2026-08-09T00:00:00.000Z',
  }
  assert.equal(validateDataRequestDraft(valid, now), '')
  assert.match(validateDataRequestDraft({ ...valid, businessPurpose: '' }, now), /业务用途/)
  assert.match(validateDataRequestDraft({ ...valid, requiredFields: [] }, now), /至少选择/)
  assert.match(validateDataRequestDraft({ ...valid, slaDueAt: '2026-08-08T00:30:00.000Z' }, now), /至少 1 小时/)
})

test('six-stage timeline renders submitted approval gate and rejected branch', () => {
  assert.equal(dataRequestStepStatus('SUBMITTED', 'DRAFT'), 'complete')
  assert.equal(dataRequestStepStatus('SUBMITTED', 'SUBMITTED'), 'complete')
  assert.equal(dataRequestStepStatus('SUBMITTED', 'APPROVED'), 'active')
  assert.equal(dataRequestStepStatus('DELIVERED', 'IN_PROGRESS'), 'complete')
  assert.equal(dataRequestStepStatus('DELIVERED', 'DELIVERED'), 'complete')
  assert.equal(dataRequestStepStatus('REJECTED', 'APPROVED'), 'rejected')
  assert.equal(dataRequestStepStatus('REJECTED', 'IN_PROGRESS'), 'pending')
})
