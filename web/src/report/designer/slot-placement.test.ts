import assert from 'node:assert/strict'
import test from 'node:test'

import { resolveSlotPlacement } from './placement.ts'

const slots = [
  { id: 'a', grid: { x: 0, y: 0, w: 6, h: 2 } },
  { id: 'b', grid: { x: 6, y: 0, w: 6, h: 2 } },
]

test('a slot dragged into free space keeps its drop position', () => {
  const result = resolveSlotPlacement(slots, 'a', { x: 0, y: 2, w: 6, h: 2 }, 12, 4, { w: 2, h: 1 })
  assert.deepEqual(result.changes, [{ slotId: 'a', rect: { x: 0, y: 2, w: 6, h: 2 } }])
  assert.equal(result.requiredRows, 4)
})

test('a slot dropped onto another displaces it rather than snapping back', () => {
  const result = resolveSlotPlacement(slots, 'a', { x: 6, y: 0, w: 6, h: 2 }, 12, 4, { w: 2, h: 1 })
  const moved = new Map(result.changes.map(change => [change.slotId, change.rect]))
  // The dragged slot stays where the user released it.
  assert.deepEqual(moved.get('a'), { x: 6, y: 0, w: 6, h: 2 })
  // The one it landed on moves down instead.
  assert.deepEqual(moved.get('b'), { x: 6, y: 2, w: 6, h: 2 })
})

test('a slot is clamped to the zone columns', () => {
  // The server validates grid.x+w <= zone.columns, so the editor must not
  // propose a slot that overhangs the zone.
  const result = resolveSlotPlacement(slots, 'a', { x: 10, y: 0, w: 6, h: 2 }, 12, 4, { w: 2, h: 1 })
  const rect = result.changes.find(change => change.slotId === 'a')?.rect
  assert.ok(rect && rect.x + rect.w <= 12, `slot overhangs the zone: ${JSON.stringify(rect)}`)
})

test('a slot pushed past the last row reports the rows the zone now needs', () => {
  const result = resolveSlotPlacement(slots, 'a', { x: 0, y: 5, w: 6, h: 2 }, 12, 4, { w: 2, h: 1 })
  // Growing the zone is the caller's job; without it the server would reject
  // grid.y+h > zone.rows and the drag would only produce a failed save.
  assert.equal(result.requiredRows, 7)
})

test('a minimum size is respected when shrinking', () => {
  const result = resolveSlotPlacement(slots, 'a', { x: 0, y: 0, w: 1, h: 1 }, 12, 4, { w: 3, h: 2 })
  const rect = result.changes.find(change => change.slotId === 'a')?.rect
  assert.deepEqual(rect, { x: 0, y: 0, w: 3, h: 2 })
})
