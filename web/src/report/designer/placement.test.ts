import assert from 'node:assert/strict'
import test from 'node:test'

import { clampRect, findFreeRect, resolveLayout } from './placement.ts'
import type { Block } from '../render/schema.ts'

function block(id: string, x: number, y: number, w: number, h: number): Block {
  return {
    id, type: 'CHART',
    layout: {
      desktop: { x, y, w, h },
      mobile: { order: 1, visible: true, heightMode: 'AUTO', slotMode: 'STACK' },
    },
    zones: [],
  }
}

test('clampRect keeps a block inside the canvas and above its manifest minimum', () => {
  assert.deepEqual(clampRect({ x: 22, y: 3, w: 6, h: 4 }, 24, { w: 4, h: 3 }), { x: 18, y: 3, w: 6, h: 4 })
  assert.deepEqual(clampRect({ x: -2, y: -5, w: 1, h: 1 }, 24, { w: 4, h: 3 }), { x: 0, y: 0, w: 4, h: 3 })
  assert.deepEqual(clampRect({ x: 0, y: 0, w: 40, h: 2 }, 24, { w: 4, h: 3 }), { x: 0, y: 0, w: 24, h: 3 })
})

test('a new component lands beside existing ones before starting a new row', () => {
  const blocks = [block('a', 0, 0, 8, 5)]
  // 同一行右侧还放得下，就并排放置——这正是拖拽编排存在的前提。
  assert.deepEqual(findFreeRect(blocks, { w: 8, h: 5 }, 24), { x: 8, y: 0, w: 8, h: 5 })
  const full = [block('a', 0, 0, 12, 5), block('b', 12, 0, 12, 5)]
  assert.deepEqual(findFreeRect(full, { w: 10, h: 4 }, 24), { x: 0, y: 5, w: 10, h: 4 })
  assert.deepEqual(findFreeRect([], { w: 6, h: 4 }, 24), { x: 0, y: 0, w: 6, h: 4 })
})

test('a drag that does not collide leaves every other block untouched', () => {
  const blocks = [block('a', 0, 0, 8, 5), block('b', 12, 0, 8, 5)]
  const changes = resolveLayout(blocks, 'a', { x: 0, y: 6, w: 8, h: 5 }, 24, { w: 2, h: 2 })
  // 用户刻意留出的空白不应被压缩掉。
  assert.deepEqual(changes, [{ blockId: 'a', rect: { x: 0, y: 6, w: 8, h: 5 }, moved: true, resized: false }])
})

test('a drag onto an occupied cell pushes the collided block down instead of failing to save', () => {
  const blocks = [block('a', 0, 0, 8, 5), block('b', 0, 5, 8, 5)]
  const changes = resolveLayout(blocks, 'b', { x: 0, y: 0, w: 8, h: 5 }, 24, { w: 2, h: 2 })
  const byId = new Map(changes.map(change => [change.blockId, change.rect]))
  assert.deepEqual(byId.get('b'), { x: 0, y: 0, w: 8, h: 5 })
  assert.deepEqual(byId.get('a'), { x: 0, y: 5, w: 8, h: 5 })
})

test('a resize reports both the resize and any move it forces', () => {
  const blocks = [block('a', 0, 0, 8, 4), block('b', 0, 4, 8, 4)]
  const changes = resolveLayout(blocks, 'a', { x: 0, y: 0, w: 8, h: 7 }, 24, { w: 2, h: 2 })
  const first = changes.find(change => change.blockId === 'a')
  assert.equal(first?.resized, true)
  assert.deepEqual(changes.find(change => change.blockId === 'b')?.rect.y, 7)
})
