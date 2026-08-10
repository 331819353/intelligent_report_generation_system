import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const fixture = (name: string): unknown => JSON.parse(readFileSync(
  new URL(`../../../api/fixtures/decision/${name}`, import.meta.url),
  'utf8',
))

const object = (value: unknown): Record<string, unknown> => {
  assert.ok(value && typeof value === 'object' && !Array.isArray(value))
  return value as Record<string, unknown>
}

function assertNoForbiddenFacts(value: unknown): void {
  if (Array.isArray(value)) {
    value.forEach(assertNoForbiddenFacts)
    return
  }
  if (!value || typeof value !== 'object') return
  for (const [key, child] of Object.entries(value)) {
    const normalized = key.toLowerCase().replaceAll('_', '')
    assert.ok(!['sql', 'rawsql', 'rows', 'resultrows', 'chainofthought', 'reasoning'].includes(normalized), `forbidden decision field: ${key}`)
    assertNoForbiddenFacts(child)
  }
}

test('Go and TypeScript share the governed decision fixtures', () => {
  const decision = object(fixture('decision.valid.json'))
  assert.equal(decision.schemaVersion, '1.0')
  assert.equal(typeof decision.tenantId, 'string')
  assert.equal(typeof decision.domainId, 'string')
  assert.equal(typeof decision.ownerUserId, 'string')
  assert.ok(Array.isArray(decision.options))
  assertNoForbiddenFacts(decision)

  const evidence = object(fixture('evidence.valid.json'))
  assert.equal(evidence.verified, true)
  assert.match(String(evidence.sourceHash), /^[0-9a-f]{64}$/)
  assert.match(String(evidence.semanticReleaseHash), /^[0-9a-f]{64}$/)
  assert.match(String(evidence.policyScopeHash), /^[0-9a-f]{64}$/)
  assertNoForbiddenFacts(evidence)

  const action = object(fixture('action.valid.json'))
  assert.ok(['TODO', 'DOING', 'BLOCKED', 'DONE', 'CANCELED'].includes(String(action.status)))
  assert.equal(typeof action.recordVersion, 'number')
  assertNoForbiddenFacts(action)

  const review = object(fixture('outcome-review.valid.json'))
  assert.ok(Array.isArray(review.metrics) && review.metrics.length > 0)
  assert.equal(review.status, 'CONFIRMED')
  assertNoForbiddenFacts(review)
})

test('negative shared fixtures preserve each contract redline', () => {
  assert.equal(typeof object(fixture('decision.invalid.json')).rawSql, 'string')
  assert.equal(object(fixture('evidence.invalid.json')).verified, false)
  const blocked = object(fixture('action.invalid.json'))
  assert.equal(blocked.status, 'BLOCKED')
  assert.equal(typeof blocked.rawSql, 'string')
  const review = object(fixture('outcome-review.invalid.json'))
  assert.ok(Array.isArray(review.rows))
})
