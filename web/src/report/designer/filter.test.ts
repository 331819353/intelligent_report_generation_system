import assert from 'node:assert/strict'
import test from 'node:test'

import { createFilterOperations, updateFilterOperations } from './operations.ts'
import type { GlobalFilter, ReportDefinition } from '../render/schema.ts'

test('a created select filter persists its governed label and options separately from defaults', () => {
  const definition = { metadata: { id: 'report-1' } } as ReportDefinition
  const [operation] = createFilterOperations(definition, {
    dataContextId: 'context-1', field: 'return_flag', type: 'SINGLE_SELECT',
    label: '退货标记', options: ['是', '否'], scope: { type: 'REPORT' },
  }, () => 'filter-1')
  const filter = (operation.payload as { filter: GlobalFilter }).filter
  assert.equal(filter.label, '退货标记')
  assert.deepEqual(filter.options, ['是', '否'])
  assert.equal(filter.defaultValue, undefined)
})

test('changing filter scope preserves existing label and candidate values', () => {
  const filter: GlobalFilter = {
    id: 'filter-1', type: 'SINGLE_SELECT', label: '退货标记', options: ['是', '否'],
    fieldRef: { dataContextId: 'context-1', field: 'return_flag' },
    scope: { type: 'REPORT', targetIds: [] },
  }
  const [operation] = updateFilterOperations(filter, {
    dataContextId: 'context-1', field: 'return_flag', type: 'SINGLE_SELECT',
    scope: { type: 'BLOCK', targetIds: ['block-1'] },
  })
  const updated = (operation.payload as { filter: GlobalFilter }).filter
  assert.equal(updated.label, '退货标记')
  assert.deepEqual(updated.options, ['是', '否'])
  assert.deepEqual(updated.scope, { type: 'BLOCK', targetIds: ['block-1'] })
})
