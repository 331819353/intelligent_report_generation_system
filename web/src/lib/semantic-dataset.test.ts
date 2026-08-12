import assert from 'node:assert/strict'
import test from 'node:test'

import type { PublishedVersionRecord } from './datasets.ts'
import { semanticDatasetGrain } from './semantic-dataset.ts'

function version(outputGrain: Record<string, unknown>): PublishedVersionRecord {
  return { dsl: { outputGrain } } as unknown as PublishedVersionRecord
}

test('uses the published compound output grain for aggregated datasets', () => {
  const result = semanticDatasetGrain(version({
    description: '每行代表统计日期、区域和渠道的汇总',
    keyFields: ['stat_date', 'region', 'channel', 'missing'],
    timeField: 'stat_date',
  }), [
    { code: 'stat_date', role: 'TIME' },
    { code: 'region', role: 'DIMENSION' },
    { code: 'channel', role: 'DIMENSION' },
    { code: 'sales_amount', role: 'MEASURE' },
  ])

  assert.deepEqual(result.keys, ['stat_date', 'region', 'channel'])
  assert.equal(result.timeField, 'stat_date')
  assert.equal(result.description, '每行代表统计日期、区域和渠道的汇总')
})

test('falls back to identifier fields for legacy published datasets', () => {
  const result = semanticDatasetGrain(version({}), [
    { code: 'order_id', role: 'IDENTIFIER' },
    { code: 'amount', role: 'MEASURE' },
  ])

  assert.deepEqual(result.keys, ['order_id'])
})
