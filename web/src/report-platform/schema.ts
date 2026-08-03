import Ajv2020, { type ErrorObject } from 'ajv/dist/2020'
import reportSchema from '../../../api/schemas/report-1.0.schema.json'
import type { ReportDefinition, ReportValidationIssue, ReportValidationResult } from './types'

const ajv = new Ajv2020({ allErrors: true, strict: true })
const validateSchema = ajv.compile<ReportDefinition>(reportSchema)

export function validateReportDefinition(source: unknown): ReportValidationResult {
  if (!validateSchema(source)) {
    return { errors: (validateSchema.errors ?? []).map(formatIssue) }
  }
  const errors = validateSemanticReferences(source)
  return { definition: errors.length ? undefined : structuredClone(source), errors }
}

export function validateReportForPublish(source: ReportDefinition): ReportValidationIssue[] {
  const issues = validateSemanticReferences(source)
  source.cards.forEach((card, index) => {
    const path = `cards[${index}].binding`
    const metricCount = card.binding.metrics.length
    const dimensionCount = card.binding.dimensions.length
    if (card.type !== 'TITLE' && !card.binding.semanticModelId) issues.push({ path: `${path}.semanticModelId`, reason: '数据卡片必须绑定语义模型' })
    if (card.type === 'TITLE' && (metricCount || dimensionCount)) issues.push({ path, reason: '标题卡不能绑定指标或维度' })
    if (card.type === 'CONCLUSION' && metricCount !== 1) issues.push({ path: `${path}.metrics`, reason: '结论卡必须绑定一个主指标' })
    if (card.type === 'CHART' && metricCount < 1) issues.push({ path: `${path}.metrics`, reason: '图形卡至少绑定一个指标' })
    if (card.type === 'COMPARISON' && (metricCount < 1 || metricCount > 2)) issues.push({ path: `${path}.metrics`, reason: '对比卡需要一个当前指标和可选基线指标' })
    if (card.type === 'RANKING' && (metricCount < 1 || dimensionCount < 1 || !card.binding.sort.length || !card.binding.limit)) issues.push({ path, reason: '排序卡必须配置指标、维度、排序和 TopN' })
    if (card.type === 'TABLE' && metricCount + dimensionCount < 1) issues.push({ path, reason: '表格卡至少绑定一个字段' })
  })
  return issues
}

function validateSemanticReferences(definition: ReportDefinition): ReportValidationIssue[] {
  const issues: ReportValidationIssue[] = []
  const filterIDs = new Set<string>()
  for (const [index, filter] of definition.globalFilters.entries()) {
    if (filterIDs.has(filter.id)) issues.push({ path: `globalFilters[${index}].id`, reason: '筛选标识重复' })
    filterIDs.add(filter.id)
  }
  const cardIDs = new Set<string>()
  for (const [index, card] of definition.cards.entries()) {
    if (cardIDs.has(card.id)) issues.push({ path: `cards[${index}].id`, reason: '卡片标识重复' })
    cardIDs.add(card.id)
    card.binding.globalFilterBindings.forEach((binding, bindingIndex) => {
      if (!filterIDs.has(binding.filterId)) issues.push({ path: `cards[${index}].binding.globalFilterBindings[${bindingIndex}].filterId`, reason: '引用的全局筛选不存在' })
      if (!binding.targetDimensionId) issues.push({ path: `cards[${index}].binding.globalFilterBindings[${bindingIndex}].targetDimensionId`, reason: '必须映射到卡片语义模型中的筛选维度' })
    })
  }
  const cardsByID = new Map(definition.cards.map(card => [card.id, card]))
  definition.cards.forEach((card, cardIndex) => card.interactions.forEach((interaction, interactionIndex) => {
    if (interaction.action.type !== 'crossFilter') return
    const path = `cards[${cardIndex}].interactions[${interactionIndex}].action`
    const target = interaction.action.targetCardId ? cardsByID.get(interaction.action.targetCardId) : undefined
    if (!target) issues.push({ path: `${path}.targetCardId`, reason: '跨卡筛选的目标卡片不存在' })
  }))
  return issues
}

function formatIssue(error: ErrorObject): ReportValidationIssue {
  const missing = error.keyword === 'required' ? `/${String(error.params.missingProperty)}` : ''
  return { path: pointerToPath(`${error.instancePath}${missing}`), reason: translateReason(error) }
}

function pointerToPath(pointer: string): string {
  if (!pointer) return '$'
  return pointer.split('/').slice(1).map(segment => /^\d+$/.test(segment) ? `[${segment}]` : segment.replaceAll('~1', '/').replaceAll('~0', '~'))
    .reduce((path, segment) => segment.startsWith('[') ? `${path}${segment}` : path ? `${path}.${segment}` : segment, '')
}

function translateReason(error: ErrorObject): string {
  if (error.keyword === 'required') return '缺少必填字段'
  if (error.keyword === 'additionalProperties') return `包含未知字段 ${String(error.params.additionalProperty)}`
  if (error.keyword === 'enum' || error.keyword === 'const') return '取值不符合 Report DSL 合同'
  if (error.keyword === 'minItems') return '至少需要一项'
  if (error.keyword === 'minLength') return '不能为空'
  return error.message ? `合同校验失败：${error.message}` : '合同校验失败'
}
