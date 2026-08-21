import assert from 'node:assert/strict'
import test from 'node:test'

import type { Block, ReportComponent } from '../render/schema.ts'
import {
  defaultSubsectionInsightConfig, effectiveSubsectionInsightConfig, subsectionInsightCandidates,
} from './subsection-insight-config.ts'

const block = {
  id: 'subsection', title: '小节', cardKind: 'LAYOUT_SUBSECTION_CONCLUSION_TOP', type: 'ANALYSIS_CARD',
  layout: { desktop: { x: 0, y: 0, w: 24, h: 10 }, mobile: { order: 1, visible: true, heightMode: 'AUTO', slotMode: 'STACK' } },
  zones: [{ id: 'zone', order: 1, type: 'CONTENT', layout: { heightMode: 'FR', minHeight: 1, columns: 24, rows: 10, overflow: 'EXPAND', emptyPriority: 1 }, slots: [
    { id: 'conclusion-slot', cardKind: 'FRAME_CONCLUSION', grid: { x: 0, y: 0, w: 24, h: 4 }, componentId: 'conclusion' },
    { id: 'evidence-slot', cardKind: 'FRAME_EVIDENCE', grid: { x: 0, y: 4, w: 12, h: 6 }, componentId: 'chart' },
    { id: 'detail-slot', cardKind: 'FRAME_DETAIL', grid: { x: 12, y: 4, w: 12, h: 6 }, componentId: 'detail' },
  ] }],
} as Block

const components = [
  { id: 'conclusion', templateRef: { type: 'rich-text', version: '1.2.0' }, options: { richText: '结论' } },
  { id: 'chart', templateRef: { type: 'line-trend', version: '1.0.0' }, options: { title: '收入趋势' } },
  { id: 'detail', templateRef: { type: 'analysis-detail-query', version: '1.0.0' }, options: {} },
] as ReportComponent[]

test('subsection insight candidates contain content slots but never the conclusion itself', () => {
  const candidates = subsectionInsightCandidates(block, components)
  assert.deepEqual(candidates.map(item => [item.componentId, item.role]), [['chart', 'EVIDENCE'], ['detail', 'DETAIL']])
  assert.deepEqual(defaultSubsectionInsightConfig(candidates).analysisItems, [
    { componentId: 'chart', weight: 100 },
  ])
})

test('invalid persisted selections fall back to the remaining available content', () => {
  const component = {
    ...components[0], options: {
      richText: '结论', subsectionInsightConfig: {
        analysisApproach: { howToAnalyze: '', analyzeWhat: '', doNotAnalyze: '', outputExample: '' },
        analysisItems: [{ componentId: 'missing', weight: 100 }],
      },
    },
  } as ReportComponent
  const config = effectiveSubsectionInsightConfig(component, subsectionInsightCandidates(block, components))
  assert.deepEqual(config.analysisItems, [{ componentId: 'chart', weight: 100 }])
  assert.ok(config.analysisApproach.howToAnalyze)
})
