import assert from 'node:assert/strict'
import test from 'node:test'
import { componentPresentation, componentStates } from './state.ts'

test('runtime exposes all eight component states and redacts protected binding titles', () => {
  assert.equal(componentStates.length, 8)
  const denied = componentPresentation('NO_PERMISSION', '机密供应商返利率')
  assert.equal(denied.title, '受限组件')
  assert.equal(JSON.stringify(denied).includes('机密供应商返利率'), false)
  assert.equal(componentPresentation('UNKNOWN', '普通指标').state, 'ERROR')
})

test('a failure reason code replaces the generic message with an actionable one', () => {
  const generic = componentPresentation('ERROR', '营业收入')
  assert.equal(generic.title, '营业收入')
  assert.equal(generic.action, '重试')

  const pinned = componentPresentation('ERROR', '营业收入', 'REPORT_DRAFT_PREVIEW_REQUIRES_PUBLISH')
  assert.equal(pinned.title, '草稿预览不可执行')
  assert.match(pinned.message, /发布后即可查看数据/)
  // 这不是可以重试的失败，所以不提供重试入口。
  assert.equal(pinned.action, undefined)

  const nonAdditive = componentPresentation('ERROR', '毛利率', 'REPORT_ROLLUP_MEASURE_NON_ADDITIVE')
  assert.equal(nonAdditive.tone, 'warning')
  assert.match(nonAdditive.message, /无法跨被省略的维度重新汇总/)
})

test('an unknown reason code falls back to the plain state presentation', () => {
  const unknown = componentPresentation('ERROR', '营业收入', 'SOMETHING_ELSE')
  assert.equal(unknown.title, '营业收入')
  assert.equal(unknown.action, '重试')
})

test('a redacted component never leaks its title through a reason code', () => {
  const denied = componentPresentation('NO_PERMISSION', undefined, 'REPORT_DRAFT_PREVIEW_REQUIRES_PUBLISH')
  assert.equal(JSON.stringify(denied).includes('机密'), false)
  assert.equal(denied.state, 'NO_PERMISSION')
})
