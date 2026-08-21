import assert from 'node:assert/strict'
import test from 'node:test'
import type { Block, ReportComponent, Section } from '../render/schema.ts'
import { sectionChartsReady, smartInsightIsPending, subsectionChartsReady } from './smart-insight-readiness.ts'
import { smartInsightPendingText } from './operations.ts'

const component = (id: string, configured = true): ReportComponent => ({
  id, templateRef: { type: 'line-trend', version: '1.0.0' }, options: { title: id },
  ...(configured ? { dataBinding: {
    bindingMode: 'DATASET_FIELD' as const, dataContextId: 'context', dimensions: [],
    measures: [{ role: 'VALUE' as const, field: 'sales', aggregation: 'SUM' }],
  } } : {}),
})

const block = (componentIds: string[]): Block => ({
  id: `block-${componentIds.join('-')}`, type: 'ANALYSIS_CARD', cardKind: 'LAYOUT_SUBSECTION_CONCLUSION_TOP',
  layout: { desktop: { x: 0, y: 0, w: 24, h: 8 }, mobile: { order: 1, visible: true, heightMode: 'AUTO', slotMode: 'STACK' } },
  zones: [{ id: 'zone', order: 1, type: 'CONTENT', layout: { heightMode: 'AUTO', minHeight: 1, columns: 24, rows: 8, overflow: 'EXPAND', emptyPriority: 1 },
    slots: componentIds.map((componentId, index) => ({ id: `slot-${index}`, cardKind: 'FRAME_EVIDENCE', grid: { x: index * 12, y: 0, w: 12, h: 4 }, componentId })),
  }],
})

test('a subsection becomes ready only after every chart slot has a configured data component', () => {
  const subsection = block(['first', 'second'])
  assert.equal(subsectionChartsReady(subsection, [component('first'), component('second', false)]), false)
  assert.equal(subsectionChartsReady(subsection, [component('first'), component('second')]), true)
})

test('an angle becomes ready only when every subsection is ready', () => {
  const first = block(['first'])
  const second = block(['second'])
  const section = { id: 'section', name: '角度', order: 1, blocks: [first, second] } as Section
  assert.equal(sectionChartsReady(section, [component('first'), component('second', false)]), false)
  assert.equal(sectionChartsReady(section, [component('first'), component('second')]), true)
})

test('only the exact waiting copy is eligible for automatic generation', () => {
  const pending = { ...component('insight', false), options: { richText: smartInsightPendingText } }
  assert.equal(smartInsightIsPending(pending), true)
  assert.equal(smartInsightIsPending({ ...pending, options: { richText: '人工结论' } }), false)
})
