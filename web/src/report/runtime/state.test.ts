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
