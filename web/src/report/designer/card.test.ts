import assert from 'node:assert/strict'
import test from 'node:test'

import {
  addToCardOperations, angleInsightBlock, createAngleInsightOperations, createLayoutFrameOperations, createSectionOperations, createStructuredBlockOperations, createSubsectionFrameOperations, createSubsectionInsightOperations, decodePalettePayload, encodePalettePayload,
  frameSlotRole,
  findCompatibleTemplateSlot, placeComponentInSlotOperations, removeComponentOperations, renameBlockOperations, renameSectionOperations, updateAngleInsightOperations, updateSubsectionInsightOperations,
} from './operations.ts'
import type { Block, Page, ReportDefinition, ZoneType } from '../render/schema.ts'
import type { ComponentManifest } from '../render/manifests.ts'

const manifest: ComponentManifest = {
  type: 'insight-text', version: '1.0.0', renderer: 'TEXT', displayName: '智能结论',
  category: 'CONTENT', minSize: { w: 4, h: 2 }, recommendedSize: { w: 8, h: 3 },
  dataContract: { dimensions: { min: 0, max: 0 }, measures: { min: 0, max: 0 }, roles: [] },
  optionSchema: { type: 'object', additionalProperties: false, required: [], properties: {} },
  defaultOptions: {}, mobilePolicy: { supported: true, defaultLegendMode: 'VISIBLE', labelDegradation: 'NONE' },
  supportedInteractions: [],
}

function zone(id: string, type: ZoneType, componentIds: string[], order = 1) {
  return {
    id, order, type,
    layout: { heightMode: 'AUTO' as const, minHeight: 1, columns: 8, rows: 4, overflow: 'EXPAND' as const, emptyPriority: 0 },
    slots: componentIds.map((componentId, index) => ({
      id: `slot-${componentId}`, grid: { x: 0, y: index, w: 8, h: 1 }, componentId,
    })),
  }
}

function card(id: string, zones: Block['zones']): Block {
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
  const payload = created?.payload as { zone: { type: string; order: number; slots: Array<{ componentId: string }> } }
  assert.equal(payload.zone.type, 'INSIGHT')
  // A new region goes beneath what the card already shows.
  assert.equal(payload.zone.order, 2)
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
    zone('zone-content', 'CONTENT', ['chart'], 1),
    zone('zone-insight', 'INSIGHT', ['conclusion'], 2),
  ])])
  const operations = removeComponentOperations(page, 'conclusion')
  assert.deepEqual(operations.map(operation => operation.op), ['ZONE_DELETE', 'COMPONENT_DELETE'])
  assert.equal(operations[0].targetId, 'zone-insight')
})

test('removing a component from a shared zone deletes only its slot', () => {
  const page = pageWith([card('block-1', [
    zone('zone-content', 'CONTENT', ['left', 'right'], 1),
    zone('zone-insight', 'INSIGHT', ['conclusion'], 2),
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

test('a structured block starts with filter, insight and content slots', () => {
  const page = pageWith([])
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const result = createStructuredBlockOperations({
    definition, page, sectionId: 'section-1', title: '经营概览', sectionName: '章节 1', newId,
  })
  const payload = result.operations[0].payload as { block: Block }
  assert.equal(payload.block.title, '经营概览')
  assert.equal(payload.block.layout.desktop.w, 24)
  assert.deepEqual(payload.block.zones.map(item => item.type), ['FILTER', 'INSIGHT', 'CONTENT'])
  assert.deepEqual(payload.block.zones.map(item => item.slots.length), [2, 1, 2])
  assert.equal(payload.block.zones.every(item => item.slots.every(slot => !slot.componentId)), true)
})

test('report framework creates a persistent three-column container', () => {
  const page = pageWith([])
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const result = createLayoutFrameOperations({
    definition, page, sectionId: 'section-1', kind: 'COLUMNS_3', sectionName: '章节 1', newId,
  })
  const block = (result.operations[0].payload as { block: Block }).block
  assert.equal(block.cardKind, 'LAYOUT_COLUMNS_3')
  assert.equal(block.layout.desktop.w, 24)
  assert.equal(block.zones.length, 1)
  assert.deepEqual(block.zones[0].slots.map(slot => slot.grid.w), [8, 8, 8])
  const chartManifest: ComponentManifest = { ...manifest, type: 'line-trend', category: 'CHART', renderer: 'ECHARTS' }
  assert.equal(findCompatibleTemplateSlot(pageWith([block]), 'section-1', chartManifest)?.blockId, block.id)
})

test('a top-conclusion subsection creates a full-width conclusion and a configurable chart row', () => {
  const page = pageWith([])
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const result = createSubsectionFrameOperations({
    definition, page, sectionId: 'section-1', layout: 'CONCLUSION_TOP', chartCount: 4,
    includeDetail: false, includeAppendix: false, sectionName: '分析角度 1', newId,
  })
  const block = (result.operations[0].payload as { block: Block }).block
  assert.equal(block.cardKind, 'LAYOUT_SUBSECTION_CONCLUSION_TOP')
  assert.deepEqual(block.zones[0].slots.map(slot => frameSlotRole(slot.cardKind)), [
    'CONCLUSION', 'EVIDENCE', 'EVIDENCE', 'EVIDENCE', 'EVIDENCE',
  ])
  assert.deepEqual(block.zones[0].slots[0].grid, { x: 0, y: 0, w: 24, h: 7 })
  assert.deepEqual(block.zones[0].slots.slice(1).map(slot => slot.grid), [
    { x: 0, y: 7, w: 6, h: 4 }, { x: 6, y: 7, w: 6, h: 4 },
    { x: 12, y: 7, w: 6, h: 4 }, { x: 18, y: 7, w: 6, h: 4 },
  ])
})

test('long-form conclusions fit new subsection slots without asking the author to resize', () => {
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const result = createSubsectionFrameOperations({
    definition, page: pageWith([]), sectionId: 'section-1', layout: 'CONCLUSION_TOP', chartCount: 2,
    includeDetail: false, includeAppendix: false, sectionName: '分析角度 1', newId,
  })
  const block = (result.operations[0].payload as { block: Block }).block
  const conclusionSlot = block.zones[0].slots[0]
  const longFormManifest: ComponentManifest = {
    ...manifest, type: 'analysis-long-form-conclusion', renderer: 'REACT',
    minSize: { w: 12, h: 7 }, recommendedSize: { w: 24, h: 10 },
  }
  const placement = placeComponentInSlotOperations({
    page: pageWith([block]), blockId: block.id, zoneId: block.zones[0].id, slotId: conclusionSlot.id,
    manifest: longFormManifest, title: '长文本结论', dataContextId: 'ctx', fields: [], newId,
  })
  assert.equal(placement.error, undefined)
  assert.deepEqual(placement.operations.map(item => item.op), ['COMPONENT_CREATE', 'SLOT_UPDATE'])
})

test('placing a long-form conclusion upgrades compact subsection slots from older reports', () => {
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const result = createSubsectionFrameOperations({
    definition, page: pageWith([]), sectionId: 'section-1', layout: 'CONCLUSION_TOP', chartCount: 2,
    includeDetail: false, includeAppendix: false, sectionName: '分析角度 1', newId,
  })
  const block = (result.operations[0].payload as { block: Block }).block
  const mainZone = block.zones[0]
  const conclusionSlot = mainZone.slots[0]
  conclusionSlot.grid.h = 3
  for (const evidenceSlot of mainZone.slots.slice(1)) evidenceSlot.grid.y -= 4
  mainZone.layout.rows -= 4
  block.layout.desktop.h -= 4

  const longFormManifest: ComponentManifest = {
    ...manifest, type: 'analysis-long-form-conclusion', renderer: 'REACT',
    minSize: { w: 12, h: 7 }, recommendedSize: { w: 24, h: 10 },
  }
  const placement = placeComponentInSlotOperations({
    page: pageWith([block]), blockId: block.id, zoneId: mainZone.id, slotId: conclusionSlot.id,
    manifest: longFormManifest, title: '长文本结论', dataContextId: 'ctx', fields: [], newId,
  })

  assert.equal(placement.error, undefined)
  assert.equal((placement.operations.find(item => item.op === 'ZONE_UPDATE')?.payload as { layout: { rows: number } }).layout.rows, 11)
  assert.equal((placement.operations.find(item => item.op === 'BLOCK_RESIZE')?.payload as { h: number }).h, 14)
  const slotUpdates = placement.operations.filter(item => item.op === 'SLOT_UPDATE')
  assert.deepEqual(slotUpdates.slice(0, 2).map(item => (item.payload as { grid: { y: number } }).grid.y), [7, 7])
  assert.deepEqual((slotUpdates.at(-1)?.payload as { grid: { x: number; y: number; w: number; h: number } }).grid,
    { x: 0, y: 0, w: 24, h: 7 })
})

test('canvas quick add creates an angle before placing its first subsection at position one', () => {
  const emptyPage: Page = { id: 'page-empty', name: '报告正文', order: 1, sections: [] }
  const angle = createSectionOperations(emptyPage, '分析角度 1', newId)
  const section = (angle.operations[0].payload as { section: Page['sections'][number] }).section
  assert.equal(angle.operations[0].op, 'SECTION_CREATE')
  assert.equal(section.blocks.length, 0)

  const pageWithAngle: Page = { ...emptyPage, sections: [section] }
  const subsection = createSubsectionFrameOperations({
    definition: { canvas: { desktop: { columns: 24 } } } as ReportDefinition,
    page: pageWithAngle, sectionId: angle.sectionId, layout: 'CONCLUSION_TOP', chartCount: 4,
    includeDetail: false, includeAppendix: false, title: '小节 1', sectionName: '分析角度 1', newId,
  })
  assert.equal(subsection.operations[0].op, 'BLOCK_CREATE')
  assert.equal(subsection.operations[0].targetId, angle.sectionId)
  const block = (subsection.operations[0].payload as { block: Block }).block
  assert.equal(block.title, '小节 1')
  assert.equal(block.layout.desktop.y, 0)
  assert.equal(block.layout.mobile.order, 1)
})

test('an angle insight is inserted before every subsection and carries one data-free summary component', () => {
  const first = card('subsection-1', [zone('subsection-zone-1', 'CONTENT', ['chart-1'])])
  first.cardKind = 'LAYOUT_SUBSECTION_CONCLUSION_TOP'
  first.layout.desktop = { x: 0, y: 0, w: 24, h: 8 }
  const second = card('subsection-2', [zone('subsection-zone-2', 'CONTENT', ['chart-2'])])
  second.cardKind = 'LAYOUT_SUBSECTION_CONCLUSION_LEFT'
  second.layout.desktop = { x: 0, y: 9, w: 24, h: 8 }
  const richTextManifest: ComponentManifest = {
    ...manifest, type: 'rich-text', renderer: 'TEXT', displayName: '富文本',
    minSize: { w: 2, h: 1 }, recommendedSize: { w: 8, h: 3 },
    dataContract: { dimensions: { min: 0, max: 0 }, measures: { min: 0, max: 0 }, roles: [] },
    defaultOptions: { richText: '' },
  }
  const page = pageWith([first, second])
  const result = createAngleInsightOperations({
    definition: { canvas: { desktop: { columns: 24 } } } as ReportDefinition,
    page, sectionId: 'section-1', manifest: richTextManifest, richText: '综合两个小节形成结论。', newId,
  })

  assert.equal(result.error, undefined)
  assert.deepEqual(result.operations.map(item => item.op), ['BLOCK_MOVE', 'BLOCK_MOVE', 'COMPONENT_CREATE', 'BLOCK_CREATE'])
  assert.deepEqual(result.operations.slice(0, 2).map(item => item.payload), [{ x: 0, y: 3 }, { x: 0, y: 12 }])
  const block = (result.operations.at(-1)?.payload as { block: Block }).block
  assert.equal(block.cardKind, 'LAYOUT_ANGLE_INSIGHT')
  assert.equal(block.zones[0].slots[0].componentId, result.componentId)
  const createdComponent = (result.operations.at(-2)?.payload as { component: ReportDefinition['components'][number] }).component
  assert.equal(createdComponent.dataBinding, undefined)
  assert.deepEqual(createdComponent.options, { richText: '综合两个小节形成结论。' })
  assert.equal(angleInsightBlock({ id: 'section-1', name: '角度', order: 1, blocks: [block] })?.id, block.id)
  assert.deepEqual(updateAngleInsightOperations(createdComponent, '重新生成的结论。'), [{
    op: 'COMPONENT_UPDATE', targetId: result.componentId, payload: { options: { richText: '重新生成的结论。' } },
  }])
})

test('a subsection conclusion is created directly in its native slot and legacy cards migrate without bindings', () => {
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const frame = createSubsectionFrameOperations({
    definition, page: pageWith([]), sectionId: 'section-1', layout: 'CONCLUSION_TOP', chartCount: 2,
    includeDetail: false, includeAppendix: false, sectionName: '分析角度 1', newId,
  })
  const block = (frame.operations[0].payload as { block: Block }).block
  const conclusionSlot = block.zones[0].slots[0]
  const richTextManifest: ComponentManifest = {
    ...manifest, type: 'rich-text', version: '1.2.0', renderer: 'TEXT', displayName: '富文本',
    minSize: { w: 2, h: 1 }, recommendedSize: { w: 8, h: 3 }, defaultOptions: { richText: '' },
  }
  const config = {
    analysisApproach: { howToAnalyze: '如何', analyzeWhat: '分析什么', doNotAnalyze: '不分析什么', outputExample: '示例' },
    analysisItems: [{ componentId: 'chart-1', weight: 100 }],
  }
  const created = createSubsectionInsightOperations({
    page: pageWith([block]), blockId: block.id, zoneId: block.zones[0].id, slotId: conclusionSlot.id,
    manifest: richTextManifest, richText: '等待生成', config, newId,
  })
  assert.equal(created.error, undefined)
  assert.deepEqual(created.operations.map(item => item.op), ['COMPONENT_CREATE', 'SLOT_UPDATE'])
  const component = (created.operations[0].payload as { component: ReportDefinition['components'][number] }).component
  assert.equal(component.dataBinding, undefined)
  assert.deepEqual(component.options.subsectionInsightConfig, config)

  const legacy = {
    id: created.componentId,
    templateRef: { type: 'analysis-long-form-conclusion', version: '1.0.0' },
    dataBinding: { bindingMode: 'DATASET_FIELD', dataContextId: 'ctx', dimensions: [], measures: [] },
    options: { title: '旧结论卡', cardVariant: '01' as const },
  } as ReportDefinition['components'][number]
  const migrated = updateSubsectionInsightOperations(legacy, '原生小节结论', config, richTextManifest)
  assert.equal(migrated[0].op, 'COMPONENT_REPLACE')
  const replacement = (migrated[0].payload as { component: ReportDefinition['components'][number] }).component
  assert.equal(replacement.dataBinding, undefined)
  assert.deepEqual(replacement.options, { richText: '原生小节结论', subsectionInsightConfig: config })
})

test('a left-conclusion subsection adapts two or four charts on the right', () => {
  const page = pageWith([])
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const result = createSubsectionFrameOperations({
    definition, page, sectionId: 'section-1', layout: 'CONCLUSION_LEFT', chartCount: 4,
    includeDetail: true, includeAppendix: true, sectionName: '分析角度 1', newId,
  })
  const block = (result.operations[0].payload as { block: Block }).block
  assert.deepEqual(block.zones[0].slots[0].grid, { x: 0, y: 0, w: 12, h: 8 })
  assert.deepEqual(block.zones[0].slots.slice(1).map(slot => slot.grid), [
    { x: 12, y: 0, w: 6, h: 4 }, { x: 18, y: 0, w: 6, h: 4 },
    { x: 12, y: 4, w: 6, h: 4 }, { x: 18, y: 4, w: 6, h: 4 },
  ])
  assert.deepEqual(block.zones.flatMap(item => item.slots.map(slot => frameSlotRole(slot.cardKind))), [
    'CONCLUSION', 'EVIDENCE', 'EVIDENCE', 'EVIDENCE', 'EVIDENCE', 'DETAIL', 'APPENDIX',
  ])
})

test('subsection slot matching prefers conclusion, detail and appendix by component role', () => {
  const definition = { canvas: { desktop: { columns: 24 } } } as ReportDefinition
  const result = createSubsectionFrameOperations({
    definition, page: pageWith([]), sectionId: 'section-1', layout: 'CONCLUSION_TOP', chartCount: 2,
    includeDetail: true, includeAppendix: true, sectionName: '分析角度 1', newId,
  })
  const block = (result.operations[0].payload as { block: Block }).block
  const page = pageWith([block])
  const chartManifest: ComponentManifest = { ...manifest, type: 'line-trend', category: 'CHART', renderer: 'ECHARTS' }
  const tableManifest: ComponentManifest = { ...manifest, type: 'analysis-detail-query', category: 'TABLE', renderer: 'REACT' }
  const appendixManifest: ComponentManifest = { ...manifest, type: 'analysis-data-explanation', category: 'CONTENT', renderer: 'REACT' }
  const roleFor = (candidate: ComponentManifest) => {
    const target = findCompatibleTemplateSlot(page, 'section-1', candidate)
    return frameSlotRole(block.zones.flatMap(item => item.slots).find(slot => slot.id === target?.slotId)?.cardKind)
  }
  assert.equal(roleFor(manifest), 'CONCLUSION')
  assert.equal(roleFor(chartManifest), 'EVIDENCE')
  assert.equal(roleFor(tableManifest), 'DETAIL')
  assert.equal(roleFor(appendixManifest), 'APPENDIX')
})

test('removing the last component from a report framework keeps its empty slot', () => {
  const block = card('layout-block', [zone('layout-zone', 'CONTENT', ['chart'])])
  block.cardKind = 'LAYOUT_TOPIC'
  const operations = removeComponentOperations(pageWith([block]), 'chart')
  assert.deepEqual(operations.map(operation => operation.op), ['SLOT_UPDATE', 'COMPONENT_DELETE'])
  assert.equal(operations[0].targetId, 'slot-chart')
})

test('dropping a component fills an existing slot without creating another block', () => {
  const emptyInsight: Block['zones'][number] = zone('zone-insight', 'INSIGHT', [])
  emptyInsight.slots = [{ id: 'slot-empty', grid: { x: 0, y: 0, w: 8, h: 4 } }]
  const page = pageWith([card('block-1', [emptyInsight])])
  const result = placeComponentInSlotOperations({
    page, blockId: 'block-1', zoneId: 'zone-insight', slotId: 'slot-empty', manifest,
    title: '结论', dataContextId: 'ctx', fields: [], newId,
  })
  assert.equal(result.error, undefined)
  assert.deepEqual(result.operations.map(item => item.op), ['COMPONENT_CREATE', 'SLOT_UPDATE'])
  assert.equal(result.operations.some(item => item.op === 'BLOCK_CREATE'), false)
})

test('removing a component from a structured block restores its empty slot', () => {
  const block = card('block-1', [
    zone('zone-filter', 'FILTER', [], 1),
    zone('zone-insight', 'INSIGHT', ['conclusion'], 2),
    zone('zone-content', 'CONTENT', [], 3),
  ])
  block.title = '经营概览'
  const operations = removeComponentOperations(pageWith([block]), 'conclusion')
  assert.deepEqual(operations.map(item => item.op), ['SLOT_UPDATE', 'COMPONENT_DELETE'])
  assert.equal((operations[0].payload as { componentId: string }).componentId, '')
})

test('palette additions choose the first compatible report template slot', () => {
  const chartManifest: ComponentManifest = { ...manifest, type: 'line-trend', category: 'CHART', renderer: 'ECHARTS' }
  const tableManifest: ComponentManifest = { ...manifest, type: 'data-table', category: 'TABLE', renderer: 'REACT' }
  const templateBlock = (id: string, type: Block['type']): Block => {
    const block = card(id, [zone(`zone-${id}`, 'CONTENT', [])])
    block.type = type
    block.zones[0].slots = [{ id: `slot-${id}`, grid: { x: 0, y: 0, w: 12, h: 6 }, cardKind: 'TEMPLATE_REPORT_CONTENT' }]
    return block
  }
  const page = pageWith([templateBlock('chart', 'CHART'), templateBlock('table', 'TABLE')])
  assert.equal(findCompatibleTemplateSlot(page, 'section-1', chartManifest)?.slotId, 'slot-chart')
  assert.equal(findCompatibleTemplateSlot(page, 'section-1', tableManifest)?.slotId, 'slot-table')
})

test('removing a component from a report template preserves its fillable position', () => {
  const block = card('block-1', [zone('zone-content', 'CONTENT', ['chart'])])
  block.type = 'CHART'
  block.zones[0].slots[0].cardKind = 'TEMPLATE_REPORT_CONTENT'
  const operations = removeComponentOperations(pageWith([block]), 'chart')
  assert.deepEqual(operations.map(item => item.op), ['SLOT_UPDATE', 'COMPONENT_DELETE'])
  assert.equal((operations[0].payload as { componentId: string }).componentId, '')
})

test('filling a compact template slot expands the block to the component minimum', () => {
  const block = card('table-block', [zone('table-zone', 'CONTENT', [])])
  block.type = 'TABLE'
  block.layout.desktop = { x: 0, y: 0, w: 24, h: 2 }
  block.zones[0].layout = { ...block.zones[0].layout, columns: 24, rows: 4 }
  block.zones[0].slots = [{ id: 'table-slot', grid: { x: 0, y: 0, w: 24, h: 4 }, cardKind: 'TEMPLATE_REPORT_CONTENT' }]
  const tableManifest: ComponentManifest = {
    ...manifest, type: 'data-table', category: 'TABLE', renderer: 'REACT',
    minSize: { w: 6, h: 4 }, recommendedSize: { w: 12, h: 8 },
  }
  const result = placeComponentInSlotOperations({
    page: pageWith([block]), blockId: block.id, zoneId: 'table-zone', slotId: 'table-slot',
    manifest: tableManifest, title: '明细', dataContextId: 'ctx', fields: [], newId,
  })
  assert.deepEqual(result.operations.map(item => item.op), ['COMPONENT_CREATE', 'SLOT_UPDATE', 'BLOCK_RESIZE'])
  assert.equal((result.operations[2].payload as { h: number }).h, 4)
})

test('analysis palette payload preserves the selected visual variant', () => {
  const payload = encodePalettePayload(manifest, { cardVariant: '03' })
  assert.deepEqual(decodePalettePayload(payload), {
    ref: 'insight-text@1.0.0',
    options: { cardVariant: '03' },
  })
  // Existing raw ref payloads remain supported for old drafts and cached browser sessions.
  assert.deepEqual(decodePalettePayload('insight-text@1.0.0'), { ref: 'insight-text@1.0.0' })
})

test('report framework configuration renames angles and subsections through controlled operations', () => {
  const subsection = card('subsection-1', [zone('zone-content', 'CONTENT', [])])
  subsection.title = '原小节标题'
  subsection.cardKind = 'LAYOUT_SUBSECTION_CONCLUSION_TOP'

  assert.deepEqual(renameSectionOperations('angle-1', '收入增长'), [
    { op: 'SECTION_UPDATE', targetId: 'angle-1', payload: { name: '收入增长' } },
  ])
  assert.deepEqual(renameBlockOperations(subsection, '增长结论'), [
    { op: 'BLOCK_UPDATE', targetId: 'subsection-1', payload: { type: 'ANALYSIS_CARD', title: '增长结论' } },
  ])
})
