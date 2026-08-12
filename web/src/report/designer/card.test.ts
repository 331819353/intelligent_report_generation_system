import assert from 'node:assert/strict'
import test from 'node:test'

import { addToCardOperations, removeComponentOperations } from './operations.ts'
import type { Block, Page, ZoneType } from '../render/schema.ts'
import type { ComponentManifest } from '../render/manifests.ts'

const manifest: ComponentManifest = {
  type: 'insight-text', version: '1.0.0', renderer: 'TEXT', displayName: '智能结论',
  category: 'CONTENT', minSize: { w: 4, h: 2 }, recommendedSize: { w: 8, h: 3 },
  dataContract: { dimensions: { min: 0, max: 0 }, measures: { min: 0, max: 0 }, roles: [] },
  optionSchema: { type: 'object', additionalProperties: false, required: [], properties: {} },
  defaultOptions: {}, mobilePolicy: { supported: true, defaultLegendMode: 'VISIBLE', labelDegradation: 'NONE' },
  supportedInteractions: [],
}

function zone(id: string, type: ZoneType, componentIds: string[]) {
  return {
    id, type,
    layout: { heightMode: 'AUTO' as const, minHeight: 1, columns: 8, rows: 4, overflow: 'EXPAND' as const, emptyPriority: 0 },
    slots: componentIds.map((componentId, index) => ({
      id: `slot-${componentId}`, grid: { x: 0, y: index, w: 8, h: 1 }, componentId,
    })),
  }
}

function card(id: string, zones: ReturnType<typeof zone>[]): Block {
  return {
    id, type: 'ANALYSIS_CARD',
    layout: {
      desktop: { x: 0, y: 0, w: 8, h: 6 },
      mobile: { order: 1, visible: true, heightMode: 'AUTO', slotMode: 'STACK' },
    },
    zones,
  }
}

function pageWith(blocks: Block[]): Page {
  return { id: 'page-1', name: 'p', order: 1, sections: [{ id: 'section-1', name: 's', order: 1, blocks }] }
}

let counter = 0
const newId = () => `generated-${++counter}`

test('a component added into a card becomes a new zone of that card', () => {
  const page = pageWith([card('block-1', [zone('zone-content', 'CONTENT', ['chart'])])])
  const { operations, componentId } = addToCardOperations({
    page, blockId: 'block-1', zoneKind: 'INSIGHT', manifest, title: '结论',
    dataContextId: 'ctx', fields: [], newId,
  })
  const ops = operations.map(operation => operation.op)
  assert.deepEqual(ops, ['COMPONENT_CREATE', 'ZONE_CREATE', 'BLOCK_RESIZE'])

  const created = operations.find(operation => operation.op === 'ZONE_CREATE')
  // The zone joins the existing card rather than creating a second card.
  assert.equal(created?.targetId, 'block-1')
  const payload = created?.payload as { zone: { type: string; slots: Array<{ componentId: string }> } }
  assert.equal(payload.zone.type, 'INSIGHT')
  assert.equal(payload.zone.slots[0].componentId, componentId)

  // The card grows to fit the new region; otherwise the zone would be clipped.
  const resize = operations.find(operation => operation.op === 'BLOCK_RESIZE')
  assert.equal((resize?.payload as { h: number }).h > 6, true)
})

test('a zone spans the full card width so one layout has one representation', () => {
  const page = pageWith([card('block-1', [zone('zone-content', 'CONTENT', ['chart'])])])
  const { operations } = addToCardOperations({
    page, blockId: 'block-1', zoneKind: 'CONTENT', manifest, title: '第二个内容',
    dataContextId: 'ctx', fields: [], newId,
  })
  const payload = operations.find(operation => operation.op === 'ZONE_CREATE')
    ?.payload as { zone: { layout: { columns: number }; slots: Array<{ grid: { w: number } }> } }
  assert.equal(payload.zone.layout.columns, 8)
  assert.equal(payload.zone.slots[0].grid.w, 8)
})

test('removing one component of a composite card deletes only its zone', () => {
  const page = pageWith([card('block-1', [
    zone('zone-content', 'CONTENT', ['chart']),
    zone('zone-insight', 'INSIGHT', ['conclusion']),
  ])])
  const operations = removeComponentOperations(page, 'conclusion')
  assert.deepEqual(operations.map(operation => operation.op), ['ZONE_DELETE', 'COMPONENT_DELETE'])
  assert.equal(operations[0].targetId, 'zone-insight')
})

test('removing a component from a shared zone deletes only its slot', () => {
  const page = pageWith([card('block-1', [
    zone('zone-content', 'CONTENT', ['left', 'right']),
    zone('zone-insight', 'INSIGHT', ['conclusion']),
  ])])
  const operations = removeComponentOperations(page, 'right')
  assert.deepEqual(operations.map(operation => operation.op), ['SLOT_DELETE', 'COMPONENT_DELETE'])
  assert.equal(operations[0].targetId, 'slot-right')
})

test('removing the last component of a card deletes the whole card', () => {
  const page = pageWith([card('block-1', [zone('zone-content', 'CONTENT', ['chart'])])])
  const operations = removeComponentOperations(page, 'chart')
  // Leaving an empty card behind would render as a blank frame nobody can remove.
  assert.deepEqual(operations.map(operation => operation.op), ['BLOCK_DELETE', 'COMPONENT_DELETE'])
  assert.equal(operations[0].targetId, 'block-1')
})
