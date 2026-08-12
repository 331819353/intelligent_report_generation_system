import assert from 'node:assert/strict'
import test from 'node:test'

import { RequestError } from './api.ts'
import { semanticCreatePayload } from './semantic-create.ts'

test('semantic create requests default to the first immutable version', () => {
  assert.deepEqual(semanticCreatePayload('models', { code: 'sales_model' }), { code: 'sales_model', versionNo: 1 })
  assert.equal(semanticCreatePayload('models', { versionNo: 3 }).versionNo, 3)
  assert.deepEqual(semanticCreatePayload('metrics', { code: 'sales_revenue' }), { code: 'sales_revenue' })
})

test('registry validation issues remain actionable in the client message', () => {
  const error = new RequestError({
    code: 'REG_VALIDATION_FAILED',
    message: 'semantic draft validation failed',
    issues: [{ path: 'versionNo', code: 'REG_REQUIRED', message: 'must be positive' }],
  }, 400)

  assert.match(error.message, /versionNo \[REG_REQUIRED\] must be positive/)
})
