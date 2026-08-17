import assert from 'node:assert/strict'
import test from 'node:test'
import {
  appendAnalysisNode, countAnalysisNodes, createAnalysisNode, createTemplateSkeleton,
  explanationTemplateDefinitions, findAnalysisNodePath, inferExplanationTemplateType, normalizeAnalysisNode, removeAnalysisNode, updateAnalysisNode,
} from './model.ts'

test('template skeleton starts from one type-free core question', () => {
  const template = createTemplateSkeleton({
    id: 'template-1', code: 'TPL-001', name: '未命名报告模板', description: '', templateType: 'REPORT', now: '2026-08-17T00:00:00Z',
  })
  assert.equal(template.schemaVersion, '1.2')
  assert.equal(countAnalysisNodes(template.analysisTree), 1)
  assert.equal(template.analysisTree[0].title, '这份报告要回答什么核心问题？')
  assert.equal(template.analysisTree[0].children.length, 0)
  assert.deepEqual(template.analysisTree[0].explanationSections.map(section => section.templateType), ['CONCLUSION_OUTPUT'])
  assert.equal('type' in template.analysisTree[0], false)
  assert.equal(JSON.stringify(template).includes('"type":"TOPIC"'), false)

  const table = createTemplateSkeleton({
    id: 'template-2', code: 'TPL-002', name: '未命名报表模板', description: '', templateType: 'TABLE', now: '2026-08-17T00:00:00Z',
  })
  assert.equal(table.analysisTree[0].title, '这张报表要监控什么核心问题？')
})

test('tree operations preserve unlimited generic nested children', () => {
  const root = createAnalysisNode('root')
  const deepNode = createAnalysisNode('deep-node')
  const withChild = appendAnalysisNode([root], 'root', deepNode)
  const withNestedChild = appendAnalysisNode(withChild, 'deep-node', createAnalysisNode('deeper-node'))
  const renamed = updateAnalysisNode(withNestedChild, 'deeper-node', node => ({ ...node, title: '更深层分析项' }))
  assert.equal(findAnalysisNodePath(renamed, 'deeper-node').length, 3)
  assert.equal(findAnalysisNodePath(renamed, 'deeper-node').at(-1)?.title, '更深层分析项')
  assert.equal(countAnalysisNodes(removeAnalysisNode(renamed, 'deep-node')), 1)
  assert.equal('type' in withNestedChild[0].children[0], false)
})

test('explanation templates provide reusable fields and migrate legacy sections', () => {
  assert.deepEqual(explanationTemplateDefinitions.DATA_SCOPE.fields.map(field => field.label), ['数据范围', '统计周期', '口径定义', '数据来源'])
  assert.deepEqual(explanationTemplateDefinitions.CONCLUSION_OUTPUT.fields, [])
  assert.deepEqual(explanationTemplateDefinitions.METRIC_DISPLAY.fields, [])
  assert.equal(explanationTemplateDefinitions.CONCLUSION_OUTPUT.category, 'OUTPUT')
  assert.equal(explanationTemplateDefinitions.ANALYSIS_OBJECTIVE.category, 'ANALYSIS')
  assert.equal(inferExplanationTemplateType('数据口径'), 'DATA_SCOPE')
  assert.equal(inferExplanationTemplateType('临时说明'), 'CUSTOM')
  const legacy = createAnalysisNode('legacy-section')
  legacy.explanationSections = [{
    id: 'legacy-explanation', title: '旧说明', items: [{ id: 'legacy-item', label: '说明', content: '保留内容' }],
  }] as unknown as typeof legacy.explanationSections
  const [section] = normalizeAnalysisNode(legacy).explanationSections
  assert.equal(section.templateType, 'CUSTOM')
  assert.equal(section.items[0].content, '保留内容')
})

test('typed content cards normalize their own independent configuration', () => {
  const node = createAnalysisNode('flexible-node')
  node.explanationSections = [{
    id: 'conclusion-card', templateType: 'CONCLUSION_OUTPUT', title: '管理结论', fields: {}, items: [],
  }, {
    id: 'metric-card', templateType: 'METRIC_DISPLAY', title: '经营指标', fields: {}, items: [],
  }, {
    id: 'scope-card', templateType: 'DATA_SCOPE', title: '数据口径', fields: {}, items: [
      { id: 'legacy-range', label: '统计范围', content: '仅统计已确认收入' },
      { id: 'legacy-source', label: '数据来源', content: '经营分析宽表' },
    ],
  }]
  const [conclusion, metric, scope] = normalizeAnalysisNode(node).explanationSections
  assert.equal(conclusion.conclusionFormat?.style, 'BULLETS')
  assert.deepEqual(metric.itemConfig?.metricDisplays, [])
  assert.equal(scope.fields.dataRange, '仅统计已确认收入')
  assert.equal(scope.fields.source, '经营分析宽表')
})

test('legacy node responsibilities migrate into content cards and remove node type', () => {
  const legacy = {
    id: 'legacy-item',
    type: 'ITEM',
    title: '收入表现',
    description: '识别收入异常',
    conclusionFormat: {
      referenceExample: '收入同比增长 12%。', style: 'BULLETS', instruction: '先结论后原因。', requiredFields: ['核心发现'], maxLength: 200,
    },
    itemConfig: {
      metrics: ['收入'],
      displayForm: 'BAR_CHART',
      comparisonDimensions: ['同比', '环比'],
      displayRequirements: '突出异常月份',
    },
    explanationSections: [],
    children: [],
  } as unknown as Parameters<typeof normalizeAnalysisNode>[0]
  const normalized = normalizeAnalysisNode(legacy)
  const metric = normalized.explanationSections.find(section => section.templateType === 'METRIC_DISPLAY')
  const conclusion = normalized.explanationSections.find(section => section.templateType === 'CONCLUSION_OUTPUT')
  assert.deepEqual(metric?.itemConfig?.metricDisplays[0], {
    id: 'legacy-item-metric-1',
    metric: '收入',
    role: 'CORE',
    displayForm: 'BAR_CHART',
    displayRequirements: '',
  })
  assert.deepEqual(metric?.itemConfig?.comparisons, ['同比', '环比'])
  assert.equal(conclusion?.conclusionFormat?.referenceExample, '收入同比增长 12%。')
  assert.equal('type' in normalized, false)
  assert.equal('itemConfig' in normalized, false)
  assert.equal('conclusionFormat' in normalized, false)
})
