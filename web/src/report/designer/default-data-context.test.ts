import assert from 'node:assert/strict'
import test from 'node:test'
import type { DataContextCandidate } from '../api/editor.ts'
import { defaultFactDataContextId } from './default-data-context.ts'

function candidate(id: string, name: string, description = ''): DataContextCandidate {
  return {
    dataContext: { id, datasetId: `${id}-dataset`, datasetVersionId: `${id}-version` },
    name, description, fields: ['id'],
  }
}

test('components default to the first fact table selected by the report', () => {
  const contexts = [{ id: 'customer', alias: '客户维度表' }, { id: 'orders', alias: '销售订单事实表' }]
  assert.equal(defaultFactDataContextId(contexts, [
    candidate('customer', '客户维度表'), candidate('orders', '销售订单事实表'),
  ]), 'orders')
})

test('fact-table fallback keeps report dataset order when catalog has no role clue', () => {
  assert.equal(defaultFactDataContextId(
    [{ id: 'first' }, { id: 'second' }],
    [candidate('first', '经营数据'), candidate('second', '辅助数据')],
  ), 'first')
})
