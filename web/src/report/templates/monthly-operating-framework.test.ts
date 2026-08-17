import assert from 'node:assert/strict'
import test from 'node:test'
import { analysisTemplateFixtures } from './fixtures.ts'
import { countAnalysisNodes, type AnalysisNode } from './model.ts'
import { createMonthlyOperatingAnalysisTemplate } from './monthly-operating-template.ts'

function flatten(nodes: AnalysisNode[]): AnalysisNode[] {
  return nodes.flatMap(node => [node, ...flatten(node.children)])
}

test('monthly operating template lands the complete reference framework as editable content', () => {
  const template = createMonthlyOperatingAnalysisTemplate()
  const nodes = flatten(template.analysisTree)
  assert.equal(template.name, '经营分析报告模板')
  assert.equal(template.version, 8)
  assert.equal(countAnalysisNodes(template.analysisTree), 49)
  assert.deepEqual(template.analysisTree[0].children.map(node => node.title), [
    '核心结论摘要',
    '一、损益分析-整体',
    '二、国内损益',
    '三、海外损益',
    '四、损益预测',
    '五、应收分析',
    '六、库存',
    '七、行动建议',
  ])
  assert.equal(nodes.every(node => node.children.length > 0 || node.explanationSections.length > 0), true)
  assert.equal(nodes.every(node => !('type' in node)), true)
})

test('bundled catalog contains only the operating analysis design', () => {
  assert.equal(analysisTemplateFixtures.length, 1)
  assert.equal(analysisTemplateFixtures[0].id, 'tpl-sales-monthly')
  assert.equal(analysisTemplateFixtures[0].name, '经营分析报告模板')
})

test('reference metrics, dimensions, comparisons and rules are structured rather than flattened text', () => {
  const template = createMonthlyOperatingAnalysisTemplate()
  const nodes = flatten(template.analysisTree)
  const find = (title: string) => nodes.find(node => node.title === title)

  const coreMetrics = find('核心指标')?.explanationSections.find(section => section.templateType === 'METRIC_DISPLAY')?.itemConfig
  assert.deepEqual(coreMetrics?.metricDisplays.map(metric => metric.metric), ['收入', '销售毛利率', '费用率', '管理利润', '利润率'])
  assert.deepEqual(coreMetrics?.comparisons, ['目标', '完成率（仅收入、利润）', '目标比', '同比', '同期'])

  const chainIncome = find('2.1.2 链群收入情况')?.explanationSections.find(section => section.templateType === 'METRIC_DISPLAY')?.itemConfig
  assert.deepEqual(chainIncome?.dimensions, ['链群'])
  assert.deepEqual(chainIncome?.breakdownRules, ['分区逻辑：大差、小差、零差'])

  const expenseTree = find('2.3.1 国内费用树')?.explanationSections.find(section => section.templateType === 'METRIC_DISPLAY')?.itemConfig
  assert.equal(expenseTree?.comparisons.includes('费率较目标（率差=实际费率-目标费率）'), true)
  assert.equal(expenseTree?.breakdownRules.includes('单独列示票折费用'), true)

  const ltl = find('零担运输')?.explanationSections.find(section => section.templateType === 'METRIC_DISPLAY')?.itemConfig
  assert.deepEqual(ltl?.metricDisplays.map(metric => metric.metric), ['零担运费', '零担体积', '零担单价', '零担占比', '占比同比差', '产比环比差', '单价差'])
  assert.equal(ltl?.breakdownRules.includes('按照零担占比从高到低取 Top5 路线'), true)

  const forecast = find('损益整体预测')?.explanationSections.find(section => section.templateType === 'DATA_SCOPE')
  assert.equal(forecast?.fields.period, '预测时点：次月')
})

test('receivables and inventory are native usable branches with valid unique ids', () => {
  const template = createMonthlyOperatingAnalysisTemplate()
  const nodes = flatten(template.analysisTree)
  const receivables = nodes.find(node => node.title === '五、应收分析')
  const inventory = nodes.find(node => node.title === '六、库存')
  assert.deepEqual(receivables?.children.map(node => node.title), ['应收整体情况', '账龄与风险结构', '回款与重点客户'])
  assert.deepEqual(inventory?.children.map(node => node.title), ['库存整体与周转', '高库存与呆滞风险', '缺货与供应保障'])

  const ids = nodes.flatMap(node => [
    node.id,
    ...node.explanationSections.flatMap(section => [
      section.id,
      ...(section.itemConfig?.metricDisplays.map(metric => metric.id) ?? []),
      ...section.items.map(item => item.id),
    ]),
  ])
  assert.equal(new Set(ids).size, ids.length)
  assert.equal(JSON.stringify(template).includes('复用应收报告'), false)
  assert.equal(JSON.stringify(template).includes('复用库存报告'), false)

  const metricSections = nodes.flatMap(node => node.explanationSections).filter(section => section.templateType === 'METRIC_DISPLAY')
  assert.equal(metricSections.every(section => (section.itemConfig?.metricDisplays.length ?? 0) > 0), true)
  assert.equal(metricSections.every(section => section.itemConfig?.metricDisplays.every(metric => metric.metric && metric.displayForm && metric.displayRequirements)), true)
})
