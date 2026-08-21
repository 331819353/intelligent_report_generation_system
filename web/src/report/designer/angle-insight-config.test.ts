import assert from 'node:assert/strict'
import test from 'node:test'

import type { ReportComponent, Section } from '../render/schema.ts'
import {
  defaultAngleInsightConfig, effectiveAngleInsightConfig, equalAngleInsightItems, validateAngleInsightConfig,
} from './angle-insight-config.ts'

const section: Section = {
  id: 'section-1', name: '经营质量', order: 1,
  blocks: [
    { id: 'subsection-1', type: 'CONTENT', title: '收入', cardKind: 'LAYOUT_SUBSECTION_CONCLUSION_TOP', layout: { desktop: { x: 0, y: 0, w: 24, h: 8 }, mobile: { order: 1, visible: true, heightMode: 'AUTO', slotMode: 'STACK' } }, zones: [] },
    { id: 'angle-summary', type: 'CONTENT', title: '智能结论', cardKind: 'LAYOUT_ANGLE_INSIGHT', layout: { desktop: { x: 0, y: 8, w: 24, h: 3 }, mobile: { order: 2, visible: true, heightMode: 'AUTO', slotMode: 'STACK' } }, zones: [] },
    { id: 'subsection-2', type: 'CONTENT', title: '风险', cardKind: 'LAYOUT_SUBSECTION_CONCLUSION_LEFT', layout: { desktop: { x: 0, y: 11, w: 24, h: 8 }, mobile: { order: 3, visible: true, heightMode: 'AUTO', slotMode: 'STACK' } }, zones: [] },
  ],
}

test('default angle insight selects only subsections and distributes an exact 100 percent', () => {
  const config = defaultAngleInsightConfig(section)
  assert.deepEqual(config.analysisItems, [
    { subsectionId: 'subsection-1', weight: 50 },
    { subsectionId: 'subsection-2', weight: 50 },
  ])
  assert.equal(validateAngleInsightConfig(config), '')
  assert.deepEqual(equalAngleInsightItems(['a', 'b', 'c']).map(item => item.weight), [34, 33, 33])
})

test('persisted selection and weight are retained while stale subsection ids are repaired', () => {
  const component = {
    id: 'summary-component', templateRef: { type: 'rich-text', version: '1.0.0' },
    options: {
      richText: '结论',
      angleInsightConfig: {
        analysisApproach: { howToAnalyze: '步骤', analyzeWhat: '风险', doNotAnalyze: '收入', outputExample: '风险：……' },
        analysisItems: [{ subsectionId: 'subsection-2', weight: 100 }],
      },
    },
  } satisfies ReportComponent
  assert.deepEqual(effectiveAngleInsightConfig(component, section), component.options.angleInsightConfig)

  const stale = structuredClone(component)
  stale.options.angleInsightConfig.analysisItems = [{ subsectionId: 'deleted-subsection', weight: 100 }]
  assert.deepEqual(effectiveAngleInsightConfig(stale, section).analysisItems, [
    { subsectionId: 'subsection-1', weight: 50 },
    { subsectionId: 'subsection-2', weight: 50 },
  ])
})

test('angle insight validation rejects incomplete prompts and invalid weight totals', () => {
  const config = defaultAngleInsightConfig(section)
  config.analysisApproach.outputExample = ''
  assert.match(validateAngleInsightConfig(config), /完整填写/)
  config.analysisApproach.outputExample = '结论：……'
  config.analysisItems[0].weight = 40
  assert.match(validateAngleInsightConfig(config), /100%/)
})
