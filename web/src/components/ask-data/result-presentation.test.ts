import assert from 'node:assert/strict'
import test from 'node:test'
import type { QuestionResult } from '../../lib/ask-data-api.ts'
import {
  formatExactNumber,
  initialResultView,
  questionResultReady,
  resultViewEligible,
} from './result-presentation.ts'

const result: QuestionResult = {
  schemaVersion: 'question-result-v1',
  title: '本月已支付订单销售额',
  resolvedTimeSpec: {
    requestedPeriod: 'CURRENT_MONTH', grain: 'MONTH', policyApplied: 'MTD', policySource: 'TIME_CONTRACT',
    resolvedStart: '2026-08-01T00:00:00+08:00', resolvedEndExclusive: '2026-08-07T00:00:00+08:00',
    dataAvailableThrough: '2026-08-06T10:30:00+08:00', truncatedByDataAvailability: true,
    periodFallbackApplied: false, timezone: 'Asia/Shanghai',
  },
  timeSpec: {
    rangeLabel: '2026-08-01 至 2026-08-06', asOfLabel: '数据截止 2026-08-06', policyLabel: '本月至今（MTD）',
    comparisonLabel: '', truncatedHint: '数据仅更新至 2026-08-06，结果已按可用范围裁剪',
  },
  summary: {
    metricLabel: '已支付订单销售额', value: '12846320', formattedValue: '¥12,846,320', unit: 'CNY',
    time: { label: '本月 MTD', start: '2026-08-01', end: '2026-08-06', timezone: 'Asia/Shanghai' },
  },
  evidenceIds: ['evidence:metric'],
  evidence: {
    definition: '已支付订单金额。', owner: { id: 'owner:finance', displayName: '财务数据组' },
    semanticVersion: 'v3.2', semanticStatus: 'CERTIFIED',
    time: { label: '本月 MTD', start: '2026-08-01', end: '2026-08-06', timezone: 'Asia/Shanghai' },
    quality: { status: 'PASS', scorePermillion: 987000, dataAsOf: '2026-08-06T10:30:00+08:00', rulesPassed: 12, rulesTotal: 12 },
  },
  datasets: [{
    id: 'dataset:trend', label: '趋势', page: 1, pageSize: 2, totalRows: 2,
    columns: [
      { key: 'day', label: '日期', type: 'DATE', role: 'DIMENSION' },
      { key: 'sales', label: '销售额', type: 'DECIMAL', role: 'MEASURE' },
    ],
    rows: [{ day: '2026-08-01', sales: '1' }, { day: '2026-08-02', sales: '2' }],
  }],
  views: [{ id: 'view:trend', type: 'LINE', label: '趋势', datasetId: 'dataset:trend', dimensionKeys: ['day'], measureKeys: ['sales'] }],
  defaultViewId: 'view:trend', recommendedViewId: 'view:trend',
}

test('accepts only deterministically eligible result views', () => {
  assert.equal(questionResultReady(result), true)
  assert.equal(initialResultView(result)?.id, 'view:trend')
  assert.equal(resultViewEligible(result, { ...result.views[0], measureKeys: ['day'] }), false)
})

test('falls back when the recommended chart cannot safely represent the data', () => {
  const table = { id: 'view:table', type: 'TABLE' as const, label: '明细', datasetId: 'dataset:trend', dimensionKeys: ['day'], measureKeys: ['sales'] }
  const unsafe = {
    ...result,
    datasets: [{ ...result.datasets[0], rows: [{ day: '2026-08-01', sales: '9007199254740992' }, { day: '2026-08-02', sales: '2' }] }],
    views: [...result.views, table],
  }
  assert.equal(resultViewEligible(unsafe, unsafe.views[0]), false)
  assert.equal(initialResultView(unsafe)?.id, 'view:table')
})

test('formats exact numeric strings without floating point coercion', () => {
  assert.equal(formatExactNumber('12345678901234567890.50'), '12,345,678,901,234,567,890.50')
  assert.equal(formatExactNumber(null), '—')
})
