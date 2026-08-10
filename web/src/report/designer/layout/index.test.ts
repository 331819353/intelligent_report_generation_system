import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import {
  calculateZoneHeights,
  compactBlocks,
  desktopPixelRect,
  detectCollisions,
  mobileBlockHeight,
  slotRenderSize,
  toMobileLayout,
  validateSlotMerge,
  type LayoutBlock,
  type MobilePageSource,
  type Slot,
  type Zone,
} from './index.ts'

type Contract = {
  collisionCases: Array<{
    name: string
    blocks: Array<{ id: string; x: number; y: number; w: number; h: number }>
    expected: Array<[string, string]>
  }>
  mergeCases: Array<{
    name: string
    slots: Array<{ id: string; x: number; y: number; w: number; h: number; componentId: string }>
    slotIds: string[]
    minimum: { w: number; h: number }
    expectedCode: string
  }>
}

const fixtureURL = new URL('../../../../../api/examples/report-layout-contract-v1.json', import.meta.url)
const contract = JSON.parse(readFileSync(fixtureURL, 'utf8')) as Contract

test('TypeScript collision and merge decisions consume the Go contract fixture', () => {
  for (const fixture of contract.collisionCases) {
    const blocks: LayoutBlock[] = fixture.blocks.map(({ id, ...desktop }) => ({ id, layout: { desktop } }))
    assert.deepEqual(
      detectCollisions(blocks).map(item => [item.firstId, item.secondId]),
      fixture.expected,
      fixture.name,
    )
  }
  for (const fixture of contract.mergeCases) {
    const slots: Slot[] = fixture.slots.map(({ id, componentId, ...grid }) => ({ id, componentId, grid }))
    assert.equal(validateSlotMerge(slots, fixture.slotIds, fixture.minimum) ?? '', fixture.expectedCode, fixture.name)
  }
})

test('desktop pixels, compact mode and four zone height modes match the server rules', () => {
  assert.deepEqual(desktopPixelRect({
    columns: 24, baseRowHeight: 32, gapX: 8, gapY: 8, paddingX: 16, paddingY: 12,
  }, { x: 2, y: 3, w: 4, h: 5 }, 1200), { x: 114, y: 132, width: 188, height: 192 })
  assert.deepEqual(slotRenderSize({ w: 12, h: 6 }, { w: 4, h: 2 }, { w: 2, h: 1 }), { w: 6, h: 3 })
  const blocks: LayoutBlock[] = [
    { id: 'block_a', layout: { desktop: { x: 0, y: 4, w: 6, h: 2 } } },
    { id: 'block_b', layout: { desktop: { x: 0, y: 9, w: 6, h: 2 } } },
    { id: 'block_c', layout: { desktop: { x: 8, y: 7, w: 6, h: 2 } } },
  ]
  assert.deepEqual(compactBlocks(blocks, 'VERTICAL').map(block => block.layout.desktop.y), [0, 2, 0])
  assert.deepEqual(blocks.map(block => block.layout.desktop.y), [4, 9, 7])

  const zones: Zone[] = [
    { id: 'fixed', type: 'HEADER', layout: { heightMode: 'FIXED', minHeight: 40, fixedHeight: 80 }, slots: [slot('sf')] },
    { id: 'auto', type: 'CONTENT', layout: { heightMode: 'AUTO', minHeight: 40 }, slots: [slot('sa')] },
    { id: 'fr', type: 'INSIGHT', layout: { heightMode: 'FR', minHeight: 50, maxHeight: 220, fr: 1 }, slots: [slot('sr')] },
    { id: 'hidden', type: 'FOOTER', layout: { heightMode: 'HIDDEN', minHeight: 0 }, slots: [slot('sh')] },
  ]
  assert.deepEqual(calculateZoneHeights(zones, 300), { fixed: 80, auto: 40, fr: 180, hidden: 0 })
})

test('mobile conversion covers all slot modes, drawer, primary query and height modes', () => {
  const source = mobileSource()
  for (const mode of ['STACK', 'CAROUSEL', 'COLLAPSE', 'PRIMARY_ONLY'] as const) {
    const block = source.sections[0].blocks[1]
    block.layout.mobile.slotMode = mode
    block.layout.mobile.primarySlotId = mode === 'PRIMARY_ONLY' ? 'slot_primary' : undefined
    const mobile = toMobileLayout(source, [
      { id: 'component_primary', templateRef: { type: 'line-trend', version: '1.0.0' } },
    ], [{
      type: 'line-trend', version: '1.0.0',
      mobilePolicy: { supported: true, defaultLegendMode: 'HIDDEN', labelDegradation: 'HIDE_WHEN_DENSE' },
    }])
    assert.equal(mobile.blocks.length, 1)
    assert.equal(mobile.blocks[0].fullWidth, true)
    assert.equal(mobile.blocks[0].filterDrawerSlots.length, 1)
    if (mode === 'PRIMARY_ONLY') {
      assert.deepEqual(mobile.blocks[0].slots.map(item => item.id), ['slot_primary'])
      assert.deepEqual(mobile.blocks[0].queriedComponentIds, ['component_primary'])
      assert.deepEqual(mobile.blocks[0].componentPolicies, [{
        componentId: 'component_primary', supported: true,
        legendMode: 'HIDDEN', labelDegradation: 'HIDE_WHEN_DENSE',
      }])
    } else {
      assert.equal(mobile.blocks[0].slots.length, 2)
    }
  }
  assert.equal(mobileBlockHeight({ order: 1, visible: true, heightMode: 'AUTO', slotMode: 'STACK' }, 320, 123), 123)
  assert.equal(mobileBlockHeight({ order: 1, visible: true, heightMode: 'FIXED', slotMode: 'STACK', fixedHeight: 180 }, 320, 123), 180)
  assert.equal(mobileBlockHeight({ order: 1, visible: true, heightMode: 'ASPECT_RATIO', slotMode: 'STACK', aspectRatio: 2 }, 320, 123), 160)
})

function slot(id: string, componentId = id): Slot {
  return { id, componentId, grid: { x: 0, y: 0, w: 1, h: 1 } }
}

function mobileSource(): MobilePageSource {
  return {
    id: 'page_mobile',
    sections: [{ blocks: [
      {
        id: 'block_hidden',
        layout: { mobile: { order: 1, visible: false, heightMode: 'AUTO', slotMode: 'STACK' } },
        zones: [],
      },
      {
        id: 'block_mobile',
        layout: { mobile: { order: 2, visible: true, heightMode: 'AUTO', slotMode: 'STACK' } },
        zones: [
          { id: 'zone_filter', type: 'FILTER', layout: { heightMode: 'AUTO', minHeight: 40 }, slots: [slot('slot_filter', 'component_filter')] },
          { id: 'zone_content', type: 'CONTENT', layout: { heightMode: 'AUTO', minHeight: 40 }, slots: [
            slot('slot_secondary', 'component_secondary'),
            { ...slot('slot_primary', 'component_primary'), grid: { x: 1, y: 0, w: 1, h: 1 } },
          ] },
        ],
      },
    ] }],
  }
}
