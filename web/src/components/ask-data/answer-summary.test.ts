import assert from 'node:assert/strict'
import test from 'node:test'

import { answerSummarySnapshot, degradedAnswerHint } from '../../askdata/answer-summary.ts'

test('ANS-003 degraded answer presentation snapshot keeps L1 and withholds L2/L3', () => {
  assert.deepEqual(answerSummarySnapshot({
    schemaVersion: '1.0',
    narrativeDegraded: true,
    verification: { attempts: 2, passed: false },
  }), {
    status: ['结构化结果已核验', '文字结论未展示'],
    headline: '为什么没有自动结论？',
    message: degradedAnswerHint,
    layers: [
      'L1 结构化结果 · 已展示',
      'L2 文字结论 · 已隐藏',
      'L3 业务解读 · 问数默认关闭',
    ],
  })
})

test('ANS-003 verified answer presentation snapshot exposes only verified L2', () => {
  assert.deepEqual(answerSummarySnapshot({
    schemaVersion: '1.0',
    narrativeDegraded: false,
    verification: { attempts: 1, passed: true },
    narrative: { summary: '销售额为 1,280 万元。', findings: [] },
  }), {
    status: ['结构化结果已核验', '文字结论已核验'],
    headline: '已核验结论',
    message: '销售额为 1,280 万元。',
    layers: [
      'L1 结构化结果 · 已展示',
      'L2 文字结论 · 已展示',
      'L3 业务解读 · 问数默认关闭',
    ],
  })
})
