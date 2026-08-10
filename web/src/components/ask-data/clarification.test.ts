import assert from 'node:assert/strict'
import test from 'node:test'
import type { ClarificationOption } from '../../lib/ask-data-api.ts'
import { clarificationOptionReady, freshnessLabel, qualityScoreLabel, timeRangeLabel } from './clarification.ts'

const option: ClarificationOption = {
  optionId: 'metric:paid-sales',
  label: '已支付订单销售额',
  difference: '是否扣除已确认退款',
  evidenceIds: ['evidence:metric-paid-sales'],
  evidence: {
    definition: '已支付订单金额，扣除取消订单，不扣除后续退款。',
    owner: { id: 'owner:finance', displayName: '财务数据组' },
    semanticVersion: 'v3.2',
    semanticStatus: 'CERTIFIED',
    time: { label: '本月 MTD', start: '2026-08-01', end: '2026-08-06', timezone: 'Asia/Shanghai' },
    quality: { status: 'PASS', scorePermillion: 987000, dataAsOf: '2026-08-06T10:30:00+08:00', rulesPassed: 12, rulesTotal: 12 },
  },
}

test('clarification option becomes actionable only with complete governed evidence', () => {
  assert.equal(clarificationOptionReady(option), true)
  assert.equal(clarificationOptionReady({ ...option, evidenceIds: [] }), false)
  assert.equal(clarificationOptionReady({ ...option, evidence: undefined }), false)
  assert.equal(clarificationOptionReady({
    ...option,
    evidence: { ...option.evidence!, owner: { id: '', displayName: '财务数据组' } },
  }), false)
})

test('clarification evidence formatting is deterministic', () => {
  assert.equal(qualityScoreLabel(option), '98.7')
  assert.equal(timeRangeLabel(option), '本月 MTD · 2026-08-01 至 2026-08-06')
  assert.equal(freshnessLabel(option), '10:30 更新')
})
