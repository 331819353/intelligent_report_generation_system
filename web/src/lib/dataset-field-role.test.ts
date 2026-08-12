import assert from 'node:assert/strict'
import test from 'node:test'

import { designerOutputRole } from './dataset-field-role.ts'

test('grouped date output preserves the TIME contract role', () => {
  assert.equal(designerOutputRole('DIMENSION', 'TIME', '', true), 'TIME')
})

test('ordinary group dimensions and metrics retain their governed roles', () => {
  assert.equal(designerOutputRole('DIMENSION', 'ATTRIBUTE', '', true), 'DIMENSION')
  assert.equal(designerOutputRole('METRIC', 'TIME', 'TIME', false), 'MEASURE')
})
