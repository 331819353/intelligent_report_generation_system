import type { DatasetDSL, DesignerNode, FieldOption, JoinOption } from './datasets'

export type CanvasPoint = { x: number; y: number }
export type GraphInput = { kind: 'NODE' | 'JOIN' | 'GROUP' | 'TRANSFORM'; id: string }
export type GraphJoin = {
  id: string; name: string; left?: GraphInput; right?: GraphInput
  position: CanvasPoint; outputKeys: string[]
}
export type GraphDimension = { key: string; outputKey?: string; name: string; code: string; grouping?: string }
export type GraphMetric = { key: string; outputKey?: string; name: string; code: string; aggregation: string; countRows?: boolean }
export type GraphGroupByMode = 'STANDARD' | 'CUBE' | 'ROLLUP' | 'GROUPING_SETS'
export type GraphGroup = {
  id: string; name: string; input?: GraphInput; position: CanvasPoint
  groupByMode?: GraphGroupByMode
  groupingSets?: string[][]
  dimensions: GraphDimension[]; metrics: GraphMetric[]
}
export type GraphTransformFamily = 'DATE' | 'TEXT' | 'CAST' | 'NUMBER' | 'CONDITION' | 'NULL' | 'WINDOW' | 'SPLIT_MERGE'
export type GraphTransformComponentType =
  | 'FILTER'
  | 'WINDOW_FUNCTION'
  | 'DATE_CALCULATION'
  | 'DATE_FORMAT'
  | 'TEXT_CASE'
  | 'TEXT_UPPER'
  | 'TEXT_TRIM'
  | 'TEXT_REPLACE'
  | 'TEXT_LOWER'
  | 'TEXT_SUBSTRING'
  | 'TEXT_CONCAT'
  | 'NUMBER_ABSOLUTE'
  | 'NUMBER_ROUNDING'
  | 'NUMBER_ARITHMETIC'
  | 'CAST'
  | 'CONDITION'
  | 'NULL'
export type GraphTransformOperation = 'WINDOW' | 'CURRENT_DATE' | 'DATE_DIFF' | 'DATE_EXTRACT' | 'DATE_START' | 'DATE_END' | 'DATE_FORMAT' | 'DATE_TRUNC' | 'CAST' | 'ADD' | 'SUBTRACT' | 'MULTIPLY' | 'DIVIDE' | 'ROUND' | 'ABS' | 'FLOOR' | 'CEIL' | 'CONCAT' | 'COALESCE' | 'CASE' | 'SUBSTRING' | 'TRIM' | 'UPPER' | 'LOWER' | 'REPLACE'
export type GraphDateUnit = 'DAY' | 'WEEK' | 'MONTH' | 'QUARTER' | 'YEAR' | 'WEEKDAY' | 'DAY_OF_YEAR'
export type GraphDateSource = 'FIELD' | 'CURRENT_DATE'
export type GraphValueSource = 'LITERAL' | 'FIELD' | 'CURRENT_DATE'
export type GraphConditionOperator = 'EQUALS' | 'NOT_EQUALS' | 'GT' | 'GTE' | 'LT' | 'LTE' | 'CONTAINS' | 'NOT_CONTAINS' | 'IN' | 'NOT_IN' | 'IS_NULL' | 'IS_NOT_NULL'
export type GraphConditionValue = { id: string; mode: 'LITERAL' | 'FIELD'; value: string }
export type GraphWindowFunction = 'ROW_NUMBER' | 'RANK' | 'DENSE_RANK' | 'SUM' | 'AVG' | 'COUNT' | 'MIN' | 'MAX'
export type GraphWindowOrder = { id: string; key: string; direction: 'ASC' | 'DESC' }
export type GraphFilterCondition = {
  id: string
  inputKey: string
  operator: GraphConditionOperator
  valueMode?: 'LITERAL' | 'FIELD'
  value: string
}
export type GraphTransformOutput = { id: string; name: string; code: string; canonicalType: string }
export type GraphTransformRule = {
  id: string
  operation: GraphTransformOperation
  inputKeys: string[]
  output: GraphTransformOutput
  unit?: GraphDateUnit
  dateSource?: GraphDateSource
  startDateSource?: GraphDateSource
  endDateSource?: GraphDateSource
  targetType?: 'STRING' | 'INTEGER' | 'DECIMAL' | 'BOOLEAN' | 'DATE' | 'DATETIME'
  matchValue?: string
  thenMode?: GraphValueSource
  thenValue?: string
  elseMode?: GraphValueSource
  elseValue?: string
  conditionOperator?: GraphConditionOperator
  conditionValues?: GraphConditionValue[]
  fallbackMode?: 'LITERAL' | 'FIELD' | 'CURRENT_DATE'
  fallbackValue?: string
  separator?: string
  precision?: number
  start?: number
  length?: number
  searchValue?: string
  replacementValue?: string
  replaceSourceKey?: string
  windowFunction?: GraphWindowFunction
  windowValueKey?: string
  partitionByKeys?: string[]
  orderBy?: GraphWindowOrder[]
}
export type GraphTransform = {
  id: string; name: string; family: GraphTransformFamily; componentType?: GraphTransformComponentType; input?: GraphInput; position: CanvasPoint
  rules: GraphTransformRule[]
  conditions?: GraphFilterCondition[]
}

const graphTransformComponentTypes: GraphTransformComponentType[] = [
  'FILTER', 'WINDOW_FUNCTION', 'DATE_CALCULATION', 'DATE_FORMAT', 'TEXT_CASE',
  'TEXT_UPPER', 'TEXT_TRIM', 'TEXT_REPLACE', 'TEXT_LOWER', 'TEXT_SUBSTRING',
  'TEXT_CONCAT', 'NUMBER_ABSOLUTE', 'NUMBER_ROUNDING', 'NUMBER_ARITHMETIC',
  'CAST', 'CONDITION', 'NULL',
]

export const normalizeGraphTransformComponentType = (value?: string): GraphTransformComponentType | undefined => {
  if (value === 'TEXT_UPPER' || value === 'TEXT_LOWER') return 'TEXT_CASE'
  return graphTransformComponentTypes.includes(value as GraphTransformComponentType)
    ? value as GraphTransformComponentType
    : undefined
}
export type GraphEndOutput = { key: string; name: string; code: string }
export type GraphEnd = {
  id: 'end_1'; name: string; input?: GraphInput; position: CanvasPoint
  outputs: GraphEndOutput[]
}
export type GraphTarget = { kind: 'JOIN' | 'GROUP' | 'TRANSFORM' | 'OUTPUT'; id: string }
export type GraphValidationIssueCode =
  | 'DUPLICATE_COMPONENT_ID'
  | 'INVALID_COMPONENT_ID'
  | 'MISSING_END'
  | 'MISSING_INPUT'
  | 'INVALID_REFERENCE'
  | 'SELF_LOOP'
  | 'CYCLE'
export type GraphValidationIssue = {
  code: GraphValidationIssueCode
  message: string
  component?: GraphTarget
}
export type GraphValidationResult = {
  valid: boolean
  issues: GraphValidationIssue[]
  errors: string[]
}
export type DesignerGraphV1 = {
  version: '1.0'
  nodePositions: Record<string, CanvasPoint>
  nodeNames: Record<string, string>
  joins: GraphJoin[]
  groups: GraphGroup[]
  transforms?: GraphTransform[]
  end?: GraphEnd
}

export type ProducedField = {
  key: string
  name: string
  code: string
  producerName: string
  kind: 'ATTRIBUTE' | 'DIMENSION' | 'METRIC'
  binding: { nodeId: string; field: string }
  sourceBinding?: { nodeId: string; field: string }
  canonicalType: string
  expression?: Record<string, unknown>
  aggregation?: string
  grouping?: string
}

type LegacyDSL = DatasetDSL & { designer?: unknown; joins?: unknown; preAggregations?: unknown; groupBy?: unknown }

const record = (value: unknown): Record<string, unknown> => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
const list = (value: unknown): unknown[] => Array.isArray(value) ? value : []
const text = (value: unknown): string => typeof value === 'string' ? value : ''
const groupByMode = (value: unknown): Exclude<GraphGroupByMode, 'STANDARD'> | undefined => {
  const mode = text(value).toUpperCase()
  return mode === 'CUBE' || mode === 'ROLLUP' || mode === 'GROUPING_SETS' ? mode : undefined
}
const groupingSets = (value: unknown): string[][] => list(value).map(item => list(item).map(text).filter(Boolean))
const finite = (value: unknown, fallback: number): number => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : fallback
const point = (value: unknown, fallback: CanvasPoint): CanvasPoint => {
  const raw = record(value)
  return { x: finite(raw.x, fallback.x), y: finite(raw.y, fallback.y) }
}
const input = (value: unknown): GraphInput | undefined => {
  const raw = record(value), kind = text(raw.kind), id = text(raw.id)
  return id && (kind === 'NODE' || kind === 'JOIN' || kind === 'GROUP' || kind === 'TRANSFORM') ? { kind, id } : undefined
}
const keyParts = (key: string) => {
  const dot = key.indexOf('.')
  return dot > 0 ? { nodeId: key.slice(0, dot), field: key.slice(dot + 1) } : { nodeId: '', field: '' }
}
const identifier = (value: string) => value.trim().replace(/[^A-Za-z0-9_]/g, '_').replace(/^[^A-Za-z]+/, '') || 'field'
const dateFormatSuffix: Record<'DAY' | 'MONTH' | 'QUARTER' | 'YEAR', string> = {
  YEAR: 'yyyy', MONTH: 'yyyymm', QUARTER: 'yyyyq', DAY: 'yyyymmdd',
}
const dateFormatUnit = (value: unknown): 'DAY' | 'MONTH' | 'QUARTER' | 'YEAR' => {
  const unit = text(value).toUpperCase()
  return unit === 'YEAR' || unit === 'MONTH' || unit === 'QUARTER' || unit === 'DAY' ? unit : 'DAY'
}
const migrateLegacyDateOutputCode = (value: string, unit: 'DAY' | 'MONTH' | 'QUARTER' | 'YEAR') => {
  const code = identifier(value)
  return /_(day|date)$/i.test(code) ? code.replace(/_(day|date)$/i, `_${dateFormatSuffix[unit]}`) : code
}
const migrateLegacyDateOutputName = (value: string, unit: 'DAY' | 'MONTH' | 'QUARTER' | 'YEAR') => {
  const label = { YEAR: '年', MONTH: '年月', QUARTER: '年季', DAY: '年月日' }[unit]
  return value.endsWith('日期处理结果') ? `${value.slice(0, -'日期处理结果'.length)}${label}` : value
}
const typedLiteral = (value: string, canonicalType: string): unknown => {
  const type = canonicalType.toUpperCase()
  if (['NUMBER', 'INT', 'INTEGER', 'DECIMAL', 'FLOAT', 'DOUBLE'].includes(type)) {
    const number = Number(value)
    return Number.isFinite(number) ? number : value
  }
  if (type === 'BOOLEAN') {
    if (value.toLowerCase() === 'true') return true
    if (value.toLowerCase() === 'false') return false
  }
  return value
}
const stringExpression = (expression: Record<string, unknown>, field: ProducedField): Record<string, unknown> => ['STRING', 'TEXT', 'VARCHAR', 'CHAR'].includes(field.canonicalType.toUpperCase())
  ? expression
  : { type: 'CAST', targetType: 'STRING', argument: expression }

/**
 * 新增分组产物时沿用上游稳定编码生成字段别名。
 * 别名不包含聚合函数，避免用户调整 SUM / AVG 等计算逻辑时破坏下游字段引用。
 * 已保存草稿中的显式 name / code 由解析逻辑原样保留，不会调用此规则覆盖。
 */
export function generatedGraphFieldIdentity(field: Pick<ProducedField, 'key' | 'name' | 'code'>): { name: string; code: string } {
  const sourceField = keyParts(field.key).field
  const code = identifier(field.code || sourceField)
  return { name: field.name.trim() || code, code }
}

export const graphInputKey = (value: GraphInput) => `${value.kind}:${value.id}`

const graphTargetKey = (value: GraphTarget) => `${value.kind}:${value.id}`

const graphComponentName = (value: GraphInput | GraphTarget, graph: DesignerGraphV1): string => {
  if (value.kind === 'NODE') return `数据节点「${graph.nodeNames[value.id] || value.id}」`
  if (value.kind === 'GROUP') {
    const group = graph.groups.find(item => item.id === value.id)
    return `分组组件「${group?.name || value.id}」`
  }
  if (value.kind === 'JOIN') {
    const join = graph.joins.find(item => item.id === value.id)
    return `关联组件「${join?.name || value.id}」`
  }
  if (value.kind === 'TRANSFORM') {
    const transform = graph.transforms?.find(item => item.id === value.id)
    return `字段处理组件「${transform?.name || value.id}」`
  }
  return `结束节点「${graph.end?.name || value.id}」`
}

const graphInputExists = (value: GraphInput, graph: DesignerGraphV1, nodeIDs: ReadonlySet<string>): boolean => {
  if (value.kind === 'NODE') return nodeIDs.has(value.id)
  if (value.kind === 'GROUP') return graph.groups.some(item => item.id === value.id)
  if (value.kind === 'TRANSFORM') return Boolean(graph.transforms?.some(item => item.id === value.id))
  return graph.joins.some(item => item.id === value.id)
}

const graphTargetExists = (value: GraphTarget, graph: DesignerGraphV1): boolean => {
  if (value.kind === 'GROUP') return graph.groups.some(item => item.id === value.id)
  if (value.kind === 'JOIN') return graph.joins.some(item => item.id === value.id)
  if (value.kind === 'TRANSFORM') return Boolean(graph.transforms?.some(item => item.id === value.id))
  return graph.end?.id === value.id
}

export function graphLeaves(value: GraphInput | undefined, graph: Pick<DesignerGraphV1, 'joins' | 'groups' | 'transforms'>, visited = new Set<string>()): string[] {
  if (!value) return []
  if (value.kind === 'NODE') return [value.id]
  const visitKey = graphInputKey(value)
  if (visited.has(visitKey)) return []
  const next = new Set(visited).add(visitKey)
  if (value.kind === 'GROUP') {
    const group = graph.groups.find(item => item.id === value.id)
    return group ? graphLeaves(group.input, graph, next) : []
  }
  if (value.kind === 'TRANSFORM') {
    const transform = graph.transforms?.find(item => item.id === value.id)
    return transform ? graphLeaves(transform.input, graph, next) : []
  }
  const join = graph.joins.find(item => item.id === value.id)
  return join ? [...graphLeaves(join.left, graph, next), ...graphLeaves(join.right, graph, next)] : []
}

export function graphContains(value: GraphInput, target: GraphInput, graph: Pick<DesignerGraphV1, 'joins' | 'groups' | 'transforms'>, visited = new Set<string>()): boolean {
  if (value.kind === target.kind && value.id === target.id) return true
  if (value.kind === 'NODE') return false
  const visitKey = graphInputKey(value)
  if (visited.has(visitKey)) return false
  const next = new Set(visited).add(visitKey)
  if (value.kind === 'GROUP') {
    const group = graph.groups.find(item => item.id === value.id)
    return Boolean(group?.input && graphContains(group.input, target, graph, next))
  }
  if (value.kind === 'TRANSFORM') {
    const transform = graph.transforms?.find(item => item.id === value.id)
    return Boolean(transform?.input && graphContains(transform.input, target, graph, next))
  }
  const join = graph.joins.find(item => item.id === value.id)
  return Boolean(join && ((join.left && graphContains(join.left, target, graph, next)) || (join.right && graphContains(join.right, target, graph, next))))
}

/** 判断把 source 作为 target 的输入后，是否会形成自环或间接循环。 */
export function wouldCreateGraphCycle(source: GraphInput, target: GraphTarget, graph: Pick<DesignerGraphV1, 'joins' | 'groups' | 'transforms'>): boolean {
  if (target.kind === 'OUTPUT') return false
  const targetInput: GraphInput = { kind: target.kind, id: target.id }
  return graphInputKey(source) === graphInputKey(targetInput) || graphContains(source, targetInput, graph)
}

/**
 * 连线阶段的轻量校验。返回 undefined 表示可以连接，否则返回可直接展示的中文错误。
 * target 表示接收输入的关联、分组或结束节点。
 */
export function graphConnectionError(source: GraphInput, target: GraphTarget, graph: DesignerGraphV1, nodeIDs: readonly string[] = Object.keys(graph.nodePositions)): string | undefined {
  const nodeIDSet = new Set(nodeIDs)
  if (!graphInputExists(source, graph, nodeIDSet)) return `${graphComponentName(source, graph)}不存在或已被删除，请重新连线。`
  if (!graphTargetExists(target, graph)) return `${graphComponentName(target, graph)}不存在或已被删除，请刷新画布后重试。`
  if (target.kind !== 'OUTPUT' && graphInputKey(source) === graphTargetKey(target)) return `不能将${graphComponentName(target, graph)}连接到自身。`
  if (wouldCreateGraphCycle(source, target, graph)) return `连接${graphComponentName(source, graph)}与${graphComponentName(target, graph)}会形成循环依赖，请调整连线。`
  return undefined
}

/**
 * 保存前校验整个画布是否为引用完整的 DAG。
 * 除拓扑循环外，也会报告组件缺少输入、引用已删除组件以及全局组件 ID 冲突。
 */
export function validateDesignerGraph(graph: DesignerGraphV1, nodeIDs: readonly string[] = Object.keys(graph.nodePositions)): GraphValidationResult {
  const issues: GraphValidationIssue[] = []
  const nodeIDSet = new Set(nodeIDs)
  const components: Array<{ id: string; kind: GraphInput['kind'] | 'OUTPUT'; name: string }> = [
    ...nodeIDs.map(id => ({ id, kind: 'NODE' as const, name: graph.nodeNames[id] || id })),
    ...graph.groups.map(item => ({ id: item.id, kind: 'GROUP' as const, name: item.name })),
    ...(graph.transforms ?? []).map(item => ({ id: item.id, kind: 'TRANSFORM' as const, name: item.name })),
    ...graph.joins.map(item => ({ id: item.id, kind: 'JOIN' as const, name: item.name })),
    ...(graph.end ? [{ id: graph.end.id, kind: 'OUTPUT' as const, name: graph.end.name }] : []),
  ]
  const owners = new Map<string, typeof components>()
  for (const component of components) {
    if (!component.id.trim()) {
      issues.push({ code: 'INVALID_COMPONENT_ID', message: `${component.kind === 'NODE' ? '数据节点' : component.kind === 'GROUP' ? '分组组件' : component.kind === 'JOIN' ? '关联组件' : '结束节点'}「${component.name || '未命名'}」缺少有效 ID。` })
      continue
    }
    owners.set(component.id, [...(owners.get(component.id) ?? []), component])
  }
  for (const [id, values] of owners) {
    if (values.length > 1) issues.push({ code: 'DUPLICATE_COMPONENT_ID', message: `组件 ID「${id}」被重复使用，无法确定连线目标。` })
  }

  const dependencies = new Map<string, string[]>()
  for (const id of nodeIDs) dependencies.set(`NODE:${id}`, [])
  const validateInput = (value: GraphInput | undefined, target: GraphTarget, slotName: string) => {
    if (!value) {
      issues.push({ code: 'MISSING_INPUT', component: target, message: `${graphComponentName(target, graph)}${slotName}尚未连接输入组件。` })
      return
    }
    if (!graphInputExists(value, graph, nodeIDSet)) {
      issues.push({ code: 'INVALID_REFERENCE', component: target, message: `${graphComponentName(target, graph)}${slotName}引用的${graphComponentName(value, graph)}不存在或已被删除。` })
      return
    }
    if (target.kind !== 'OUTPUT' && graphInputKey(value) === graphTargetKey(target)) {
      issues.push({ code: 'SELF_LOOP', component: target, message: `不能将${graphComponentName(target, graph)}连接到自身。` })
    }
  }

  for (const group of graph.groups) {
    const target: GraphTarget = { kind: 'GROUP', id: group.id }
    validateInput(group.input, target, '')
    dependencies.set(graphTargetKey(target), group.input && graphInputExists(group.input, graph, nodeIDSet) ? [graphInputKey(group.input)] : [])
  }
  for (const transform of graph.transforms ?? []) {
    const target: GraphTarget = { kind: 'TRANSFORM', id: transform.id }
    validateInput(transform.input, target, '')
    dependencies.set(graphTargetKey(target), transform.input && graphInputExists(transform.input, graph, nodeIDSet) ? [graphInputKey(transform.input)] : [])
  }
  for (const join of graph.joins) {
    const target: GraphTarget = { kind: 'JOIN', id: join.id }
    validateInput(join.left, target, '的槽位 1 ')
    validateInput(join.right, target, '的槽位 2 ')
    dependencies.set(graphTargetKey(target), [join.left, join.right].flatMap(value => value && graphInputExists(value, graph, nodeIDSet) ? [graphInputKey(value)] : []))
  }
  if (!graph.end) {
    issues.push({ code: 'MISSING_END', message: '画布缺少结束节点，请添加结束节点并连接最终产物。' })
  } else {
    const target: GraphTarget = { kind: 'OUTPUT', id: graph.end.id }
    validateInput(graph.end.input, target, '')
    dependencies.set(graphTargetKey(target), graph.end.input && graphInputExists(graph.end.input, graph, nodeIDSet) ? [graphInputKey(graph.end.input)] : [])
  }

  const states = new Map<string, 0 | 1 | 2>()
  const stack: string[] = []
  let cycleFound = false
  const labelForKey = (key: string) => {
    const separator = key.indexOf(':')
    const kind = key.slice(0, separator) as GraphInput['kind'] | 'OUTPUT'
    const id = key.slice(separator + 1)
    return graphComponentName({ kind, id } as GraphInput | GraphTarget, graph)
  }
  const visit = (key: string) => {
    if (cycleFound || states.get(key) === 2) return
    states.set(key, 1)
    stack.push(key)
    for (const dependency of dependencies.get(key) ?? []) {
      if (states.get(dependency) === 1) {
        const start = stack.indexOf(dependency)
        const cycle = [...stack.slice(start), dependency].map(labelForKey)
        issues.push({ code: 'CYCLE', message: `画布存在循环依赖：${cycle.join(' → ')}。请删除形成闭环的连线。` })
        cycleFound = true
        break
      }
      if (!states.has(dependency)) visit(dependency)
    }
    stack.pop()
    states.set(key, 2)
  }
  for (const key of dependencies.keys()) {
    if (cycleFound) break
    if (!states.has(key)) visit(key)
  }

  return { valid: issues.length === 0, issues, errors: issues.map(item => item.message) }
}

export function graphRoot(nodeIDs: string[], graph: Pick<DesignerGraphV1, 'joins' | 'groups' | 'transforms'>): GraphInput | undefined {
  const used = new Set<string>()
  for (const join of graph.joins) for (const value of [join.left, join.right]) if (value) used.add(graphInputKey(value))
  for (const group of graph.groups) if (group.input) used.add(graphInputKey(group.input))
  for (const transform of graph.transforms ?? []) if (transform.input) used.add(graphInputKey(transform.input))
  const candidates: GraphInput[] = [
    ...(graph.transforms ?? []).map(item => ({ kind: 'TRANSFORM' as const, id: item.id })),
    ...graph.groups.map(item => ({ kind: 'GROUP' as const, id: item.id })),
    ...graph.joins.map(item => ({ kind: 'JOIN' as const, id: item.id })),
    ...nodeIDs.map(id => ({ kind: 'NODE' as const, id })),
  ]
  const roots = candidates.filter(value => !used.has(graphInputKey(value)) && graphLeaves(value, graph).length)
  return roots.length === 1 ? roots[0] : undefined
}

function nodeFields(node: DesignerNode, fields: FieldOption[]): ProducedField[] {
  const options = new Map(fields.map(item => [item.key, item]))
  return node.columns.filter(column => node.selected.includes(column.columnName) && options.get(`${node.id}.${column.columnName}`)?.output !== false).map(column => {
    const key = `${node.id}.${column.columnName}`, option = options.get(key)
    return {
      key, name: option?.name?.trim() || column.businessName || column.columnName,
      code: identifier(option?.code || column.columnName), producerName: node.table.businessName || node.table.tableName,
      kind: 'ATTRIBUTE' as const, binding: { nodeId: node.id, field: column.columnName }, sourceBinding: { nodeId: node.id, field: column.columnName }, canonicalType: column.canonicalType,
      expression: { type: 'FIELD_REF', nodeId: node.id, field: column.columnName },
    }
  })
}

/** key 绑定分组输入；outputKey 用于区分同一输入产生的维度和指标。 */
export const graphGroupOutputKey = (item: GraphDimension | GraphMetric) => item.outputKey?.trim() || item.key

/** 返回一个组件对下游公开的稳定产物；展示名称与物理字段绑定分离。 */
export function graphProducedFields(value: GraphInput | undefined, graph: Pick<DesignerGraphV1, 'joins' | 'groups' | 'transforms'> & { nodeNames?: Record<string, string> }, nodes: DesignerNode[], fields: FieldOption[], visited = new Set<string>(), materializedGroupID = '', preAggregatedGroupIDs: ReadonlySet<string> = new Set()): ProducedField[] {
  if (!value) return []
  if (value.kind === 'NODE') {
    const node = nodes.find(item => item.id === value.id)
    return node ? nodeFields(node, fields).map(item => ({ ...item, producerName: graph.nodeNames?.[node.id] || item.producerName })) : []
  }
  const visitKey = graphInputKey(value)
  if (visited.has(visitKey)) return []
  const next = new Set(visited).add(visitKey)
  if (value.kind === 'GROUP') {
    const group = graph.groups.find(item => item.id === value.id)
    if (!group) return []
    const upstream = new Map(graphProducedFields(group.input, graph, nodes, fields, next, materializedGroupID, preAggregatedGroupIDs).map(item => [item.key, item]))
    const preAggregated = preAggregatedGroupIDs.has(group.id)
    return [
      ...group.dimensions.flatMap(item => {
        const source = upstream.get(item.key)
        if (!source) return []
        const argument = source.expression ?? { type: 'FIELD_REF', nodeId: source.binding.nodeId, field: source.binding.field }
        const outputField = item.outputKey
          ? identifier(item.code || keyParts(item.key).field || source.binding.field)
          : keyParts(item.key).field || source.binding.field
        return [{
          ...source, key: graphGroupOutputKey(item), name: item.name, code: item.code, producerName: group.name, kind: 'DIMENSION' as const,
          binding: preAggregated ? { nodeId: source.binding.nodeId, field: outputField } : source.binding,
          sourceBinding: source.sourceBinding ?? source.binding,
          grouping: item.grouping, aggregation: undefined,
          expression: preAggregated
            ? { type: 'FIELD_REF', nodeId: source.binding.nodeId, field: outputField }
            : materializedGroupID === group.id && item.grouping ? { type: 'DATE_TRUNC', unit: item.grouping, argument } : argument,
        }]
      }),
      ...group.metrics.flatMap(item => {
        const countRows = item.aggregation === 'COUNT' && (item.key === '*' || item.countRows)
        // COUNT(*) 不绑定某个业务字段；这里只借用首个上游字段承载节点与类型元数据，
        // 真正的表达式没有 argument，因此不会退化成 COUNT(field)。
        const source = countRows ? upstream.values().next().value : upstream.get(item.key)
        if (!source) return []
        const argument = source.expression ?? { type: 'FIELD_REF', nodeId: source.binding.nodeId, field: source.binding.field }
        const outputField = item.outputKey
          ? identifier(item.code || keyParts(item.key).field || source.binding.field)
          : countRows ? identifier(item.code || 'row_count') : keyParts(item.key).field || source.binding.field
        return [{
          ...source, key: graphGroupOutputKey(item), name: item.name, code: item.code, producerName: group.name, kind: 'METRIC' as const,
          binding: preAggregated ? { nodeId: source.binding.nodeId, field: outputField } : source.binding,
          sourceBinding: source.sourceBinding ?? source.binding,
          aggregation: item.aggregation, grouping: undefined,
          canonicalType: item.aggregation === 'COUNT' || item.aggregation === 'COUNT_DISTINCT' ? 'INTEGER' : source.canonicalType,
          expression: preAggregated
            ? { type: 'FIELD_REF', nodeId: source.binding.nodeId, field: outputField }
            : materializedGroupID === group.id
              ? countRows
                ? { type: 'AGGREGATE', function: 'COUNT' }
                : { type: 'AGGREGATE', function: item.aggregation, argument }
              : argument,
        }]
      }),
    ]
  }
  if (value.kind === 'TRANSFORM') {
    const transform = graph.transforms?.find(item => item.id === value.id)
    if (!transform) return []
    const upstream = graphProducedFields(transform.input, graph, nodes, fields, next, materializedGroupID, preAggregatedGroupIDs)
    // 过滤组件只改变行集合，不改变字段集合；实际布尔表达式由 datasets.ts
    // 转换到 DSL filters，确保执行层统一生成参数化 WHERE。
    if (transform.componentType === 'FILTER') {
      return upstream.map(item => ({ ...item, producerName: transform.name }))
    }
    const upstreamByKey = new Map(upstream.map(item => [item.key, item]))
    const replaced = new Set(transform.rules.flatMap(rule =>
      rule.inputKeys[0] && rule.replaceSourceKey === rule.inputKeys[0] ? [rule.replaceSourceKey] : [],
    ))
    const derived = transform.rules.flatMap(rule => {
      const referencedKeys = rule.operation === 'WINDOW'
        ? [...new Set([...(rule.windowValueKey ? [rule.windowValueKey] : []), ...(rule.partitionByKeys ?? []), ...(rule.orderBy ?? []).map(item => item.key)])]
        : rule.inputKeys
      const inputs = referencedKeys.map(key => upstreamByKey.get(key)).filter((field): field is ProducedField => Boolean(field))
      const permitsNoFieldInput = rule.operation === 'CURRENT_DATE' ||
        rule.operation === 'DATE_DIFF' && rule.startDateSource === 'CURRENT_DATE' && rule.endDateSource === 'CURRENT_DATE' ||
        (rule.operation === 'DATE_EXTRACT' || rule.operation === 'DATE_START' || rule.operation === 'DATE_END') && rule.dateSource === 'CURRENT_DATE'
      if ((!inputs.length && !permitsNoFieldInput) || inputs.length !== referencedKeys.length) return []
      const expressions = inputs.map(field => field.expression ?? { type: 'FIELD_REF', nodeId: field.binding.nodeId, field: field.binding.field })
      const expressionByKey = new Map(referencedKeys.map((key, index) => [key, expressions[index]]))
      const textExpressions = expressions.map((expression, index) => stringExpression(expression, inputs[index]))
      const mergeExpressions = textExpressions.map(expression => ({ type: 'COALESCE', arguments: [expression, { type: 'LITERAL', value: '' }] }))
      let expression: Record<string, unknown>
      if (rule.operation === 'WINDOW') expression = {
        type: 'WINDOW',
        function: rule.windowFunction || 'ROW_NUMBER',
        ...(rule.windowValueKey && expressionByKey.get(rule.windowValueKey) ? { argument: expressionByKey.get(rule.windowValueKey)! } : {}),
        partitionBy: (rule.partitionByKeys ?? []).flatMap(key => expressionByKey.get(key) ? [expressionByKey.get(key)!] : []),
        orderBy: (rule.orderBy ?? []).flatMap(item => expressionByKey.get(item.key)
          ? [{ expression: expressionByKey.get(item.key)!, direction: item.direction || 'ASC' }]
          : []),
      }
      else if (rule.operation === 'CURRENT_DATE') expression = { type: 'CURRENT_DATE' }
      else if (rule.operation === 'DATE_DIFF') {
        let fieldIndex = 0
        const dateArgument = (source: GraphDateSource | undefined) =>
          source === 'CURRENT_DATE' ? { type: 'CURRENT_DATE' } : expressions[fieldIndex++]
        expression = {
          type: 'DATE_DIFF',
          unit: rule.unit || 'DAY',
          arguments: [dateArgument(rule.startDateSource), dateArgument(rule.endDateSource)],
        }
      }
      else if (rule.operation === 'DATE_EXTRACT') expression = { type: 'DATE_EXTRACT', unit: rule.unit || 'YEAR', argument: rule.dateSource === 'CURRENT_DATE' ? { type: 'CURRENT_DATE' } : expressions[0] }
      else if (rule.operation === 'DATE_START' || rule.operation === 'DATE_END') expression = { type: rule.operation, unit: rule.unit || 'MONTH', argument: rule.dateSource === 'CURRENT_DATE' ? { type: 'CURRENT_DATE' } : expressions[0] }
      else if (rule.operation === 'DATE_FORMAT') expression = { type: 'DATE_FORMAT', unit: rule.unit || 'DAY', argument: expressions[0] }
      else if (rule.operation === 'DATE_TRUNC') expression = { type: 'DATE_TRUNC', unit: rule.unit || 'DAY', argument: expressions[0] }
      else if (rule.operation === 'CAST') expression = { type: 'CAST', targetType: rule.targetType || rule.output.canonicalType, argument: expressions[0] }
      else if (rule.operation === 'SUBSTRING') expression = { type: 'SUBSTRING', arguments: [textExpressions[0], { type: 'LITERAL', value: rule.start || 1 }, { type: 'LITERAL', value: rule.length ?? 10 }] }
      else if (rule.operation === 'TRIM' || rule.operation === 'UPPER' || rule.operation === 'LOWER') expression = { type: rule.operation, argument: textExpressions[0] }
      else if (rule.operation === 'REPLACE') expression = { type: 'REPLACE', arguments: [textExpressions[0], { type: 'LITERAL', value: rule.searchValue ?? '' }, { type: 'LITERAL', value: rule.replacementValue ?? '' }] }
      else if (rule.operation === 'ROUND') expression = { type: 'ROUND', arguments: [expressions[0], { type: 'LITERAL', value: rule.precision ?? 2 }] }
      else if (rule.operation === 'ABS' || rule.operation === 'FLOOR' || rule.operation === 'CEIL') expression = { type: rule.operation, argument: expressions[0] }
      else if (rule.operation === 'COALESCE') expression = {
        type: 'COALESCE',
        arguments: [
          expressions[0],
          rule.fallbackMode === 'FIELD'
            ? expressions[1]
            : rule.fallbackMode === 'CURRENT_DATE'
              ? { type: 'CURRENT_DATE' }
              : { type: 'LITERAL', value: typedLiteral(rule.fallbackValue ?? '', inputs[0].canonicalType) },
        ],
      }
      else if (rule.operation === 'CASE') {
        const operator = rule.conditionOperator || 'EQUALS'
        const unary = operator === 'IS_NULL' || operator === 'IS_NOT_NULL'
        const contains = operator === 'CONTAINS' || operator === 'NOT_CONTAINS'
        const collection = operator === 'IN' ? (rule.conditionValues ?? []).flatMap(item => {
          if (item.mode === 'FIELD') {
            const field = upstreamByKey.get(item.value)
            return field ? [field.expression ?? { type: 'FIELD_REF', nodeId: field.binding.nodeId, field: field.binding.field }] : []
          }
          return item.value.length ? [{ type: 'LITERAL', value: typedLiteral(item.value, inputs[0].canonicalType) }] : []
        }) : []
        const when = unary
          ? { type: operator, argument: expressions[0] }
          : operator === 'IN'
            ? { type: 'IN', left: expressions[0], right: { type: 'ARRAY', arguments: collection } }
            : { type: operator, left: contains ? textExpressions[0] : expressions[0], right: { type: 'LITERAL', value: contains ? rule.matchValue ?? '' : typedLiteral(rule.matchValue ?? '', inputs[0].canonicalType) } }
        expression = {
          type: 'CASE',
          whens: [{
            when,
            then: rule.thenMode === 'CURRENT_DATE'
              ? { type: 'CURRENT_DATE' }
              : rule.thenMode === 'FIELD'
                ? expressions[0]
                : { type: 'LITERAL', value: rule.thenValue ?? '' },
          }],
          else: rule.elseMode === 'CURRENT_DATE'
            ? { type: 'CURRENT_DATE' }
            : rule.elseMode === 'FIELD'
              ? expressions[0]
              : { type: 'LITERAL', value: rule.elseValue ?? '' },
        }
      }
      else if (rule.operation === 'CONCAT') expression = { type: 'CONCAT', arguments: rule.separator === undefined ? mergeExpressions : [mergeExpressions[0], { type: 'LITERAL', value: rule.separator }, mergeExpressions[1]] }
      else expression = { type: rule.operation, arguments: expressions }
      return [{
        key: `${transform.id}.${rule.output.id}`,
        name: rule.output.name,
        code: identifier(rule.output.code),
        producerName: transform.name,
        kind: rule.operation === 'WINDOW' || !inputs.length ? 'ATTRIBUTE' as const : inputs.some(field => field.kind === 'METRIC') ? 'METRIC' as const : inputs.every(field => field.kind === 'DIMENSION') ? 'DIMENSION' as const : 'ATTRIBUTE' as const,
        binding: (inputs[0] ?? upstream[0]).binding,
        sourceBinding: inputs[0]?.sourceBinding ?? inputs[0]?.binding ?? upstream[0].sourceBinding ?? upstream[0].binding,
        canonicalType: rule.output.canonicalType,
        grouping: rule.operation === 'DATE_TRUNC' ? rule.unit || 'DAY' : undefined,
        expression,
      }]
    })
    return [...upstream.filter(field => !replaced.has(field.key)), ...derived]
  }
  const join = graph.joins.find(item => item.id === value.id)
  if (!join) return []
  const upstream = [...graphProducedFields(join.left, graph, nodes, fields, next, materializedGroupID, preAggregatedGroupIDs), ...graphProducedFields(join.right, graph, nodes, fields, next, materializedGroupID, preAggregatedGroupIDs)]
  const allowed = new Set(join.outputKeys.length ? join.outputKeys : upstream.map(item => item.key))
  return upstream.filter(item => allowed.has(item.key)).map(item => ({ ...item, producerName: join.name }))
}

export const graphOutputKeys = (value: GraphInput | undefined, graph: Pick<DesignerGraphV1, 'joins' | 'groups' | 'transforms'>, nodes: DesignerNode[], fields: FieldOption[]) => graphProducedFields(value, graph, nodes, fields).map(item => item.key)

/** 组件字段选择只展示用户可识别的数据集（上游产物）与业务字段名称。 */
export const graphProducedFieldLabel = (field: ProducedField) => `${field.producerName} / ${field.name}`

export function layoutDesignerGraph(graph: DesignerGraphV1, nodeIDs: string[]): DesignerGraphV1 {
  const keys = [
    ...nodeIDs.map(id => `NODE:${id}`),
    ...graph.groups.map(item => `GROUP:${item.id}`),
    ...(graph.transforms ?? []).map(item => `TRANSFORM:${item.id}`),
    ...graph.joins.map(item => `JOIN:${item.id}`),
    ...(graph.end ? [`OUTPUT:${graph.end.id}`] : []),
  ]
  const stableOrder = new Map(keys.map((key, index) => [key, index]))
  const dependencies = new Map<string, string[]>()
  for (const group of graph.groups) dependencies.set(`GROUP:${group.id}`, group.input ? [graphInputKey(group.input)] : [])
  for (const transform of graph.transforms ?? []) dependencies.set(`TRANSFORM:${transform.id}`, transform.input ? [graphInputKey(transform.input)] : [])
  for (const join of graph.joins) dependencies.set(`JOIN:${join.id}`, [join.left, join.right].flatMap(item => item ? [graphInputKey(item)] : []))
  if (graph.end) dependencies.set(`OUTPUT:${graph.end.id}`, graph.end.input ? [graphInputKey(graph.end.input)] : [])
  const depthCache = new Map<string, number>()
  const depthOf = (key: string, visiting = new Set<string>()): number => {
    if (key.startsWith('NODE:')) return 0
    if (depthCache.has(key)) return depthCache.get(key)!
    if (visiting.has(key)) return 1
    const parents = (dependencies.get(key) ?? []).filter(item => stableOrder.has(item))
    const depth = parents.length ? Math.max(...parents.map(item => depthOf(item, new Set(visiting).add(key)))) + 1 : 1
    depthCache.set(key, depth)
    return depth
  }
  const layers = new Map<number, string[]>()
  for (const key of keys) {
    const depth = depthOf(key)
    layers.set(depth, [...(layers.get(depth) ?? []), key])
  }
  const ranks = new Map<string, number>()
  for (const depth of [...layers.keys()].sort((a, b) => a - b)) {
    const layer = layers.get(depth) ?? []
    const center = (key: string) => {
      const parentRanks = (dependencies.get(key) ?? []).flatMap(item => ranks.has(item) ? [ranks.get(item)!] : [])
      return parentRanks.length ? parentRanks.reduce((sum, value) => sum + value, 0) / parentRanks.length : stableOrder.get(key) ?? 0
    }
    layer.sort((a, b) => center(a) - center(b) || (stableOrder.get(a) ?? 0) - (stableOrder.get(b) ?? 0))
    let previous = -1
    layer.forEach((key, index) => {
      const rank = Math.max(depth === 0 ? index : center(key), previous + 1)
      ranks.set(key, rank)
      previous = rank
    })
  }
  const position = (key: string): CanvasPoint => ({ x: 42 + depthOf(key) * 300, y: 48 + (ranks.get(key) ?? 0) * 150 })
  return {
    ...graph,
    nodePositions: Object.fromEntries(nodeIDs.map(id => [id, position(`NODE:${id}`)])),
    groups: graph.groups.map(item => ({ ...item, position: position(`GROUP:${item.id}`) })),
    transforms: (graph.transforms ?? []).map(item => ({ ...item, position: position(`TRANSFORM:${item.id}`) })),
    joins: graph.joins.map(item => ({ ...item, position: position(`JOIN:${item.id}`) })),
    ...(graph.end ? { end: { ...graph.end, position: position(`OUTPUT:${graph.end.id}`) } } : {}),
  }
}

function legacyJoins(nodes: DesignerNode[], joins: JoinOption[]): GraphJoin[] {
  const result: GraphJoin[] = []
  const component = new Map<string, GraphInput>(nodes.map(node => [node.id, { kind: 'NODE' as const, id: node.id }]))
  for (const [index, join] of joins.entries()) {
    const left = component.get(join.leftNodeId) ?? { kind: 'NODE' as const, id: join.leftNodeId }
    const right = component.get(join.rightNodeId) ?? { kind: 'NODE' as const, id: join.rightNodeId }
    const item: GraphJoin = { id: join.id, name: `关联结果 ${index + 1}`, left, right, position: { x: 510 + index * 250, y: 150 }, outputKeys: [] }
    result.push(item)
    const root = { kind: 'JOIN' as const, id: item.id }
    for (const nodeID of [...graphLeaves(left, { joins: result, groups: [], transforms: [] }), ...graphLeaves(right, { joins: result, groups: [], transforms: [] })]) component.set(nodeID, root)
  }
  return result
}

function parseExistingDesigner(rawValue: unknown, fallback: DesignerGraphV1): DesignerGraphV1 | null {
  const raw = record(rawValue)
  if (!Object.keys(raw).length || text(raw.version) !== '1.0') return null
  if (!['nodePositions', 'joins', 'groups', 'transforms', 'end'].some(key => key in raw)) return null
  const nodePositionsRaw = record(raw.nodePositions), nodeNamesRaw = record(raw.nodeNames)
  const joins: GraphJoin[] = list(raw.joins).flatMap((value, index) => {
    const item = record(value), id = text(item.id)
    if (!id) return []
    return [{ id, name: text(item.name) || `关联结果 ${index + 1}`, left: input(item.left), right: input(item.right), position: point(item.position, { x: 510 + index * 250, y: 150 }), outputKeys: list(item.outputKeys).map(text).filter(Boolean) }]
  })
  const groups: GraphGroup[] = list(raw.groups).flatMap((value, index) => {
    const item = record(value), id = text(item.id)
    if (!id) return []
    const mode = groupByMode(item.groupByMode)
    const dimensions = list(item.dimensions).flatMap(value => {
      const field = record(value), key = text(field.key), outputKey = text(field.outputKey)
      return key ? [{ key, ...(outputKey ? { outputKey } : {}), name: text(field.name) || key, code: identifier(text(field.code) || keyParts(key).field), grouping: text(field.grouping) || undefined }] : []
    })
    const metrics = list(item.metrics).flatMap(value => {
      const field = record(value), key = text(field.key), outputKey = text(field.outputKey), aggregation = text(field.aggregation)
      const countRows = aggregation === 'COUNT' && (key === '*' || field.countRows === true)
      return key && aggregation ? [{
        key: countRows ? '*' : key,
        ...(outputKey ? { outputKey } : {}),
        name: text(field.name) || key,
        code: identifier(text(field.code) || (countRows ? 'row_count' : `${aggregation.toLowerCase()}_${keyParts(key).field}`)),
        aggregation,
        ...(countRows ? { countRows: true } : {}),
      }] : []
    })
    return [{
      id, name: text(item.name) || `分组结果 ${index + 1}`, input: input(item.input),
      position: point(item.position, { x: 342, y: 48 + index * 150 }),
      ...(mode ? { groupByMode: mode } : {}),
      ...(mode === 'GROUPING_SETS' ? { groupingSets: groupingSets(item.groupingSets) } : {}),
      dimensions, metrics,
    }]
  })
  const transforms: GraphTransform[] = list(raw.transforms).flatMap((value, index) => {
    const item = record(value), id = text(item.id), family = text(item.family) as GraphTransformFamily
    const componentType = normalizeGraphTransformComponentType(text(item.componentType))
    if (!id || !['DATE', 'TEXT', 'CAST', 'NUMBER', 'CONDITION', 'NULL', 'WINDOW', 'SPLIT_MERGE'].includes(family)) return []
    const rules: GraphTransformRule[] = list(item.rules).flatMap((value, ruleIndex) => {
      const rule = record(value), output = record(rule.output), persistedOperation = text(rule.operation) as GraphTransformOperation
      const legacyDateFormat = family === 'DATE' && persistedOperation === 'DATE_TRUNC'
      const operation: GraphTransformOperation = legacyDateFormat ? 'DATE_FORMAT' : persistedOperation
      const outputID = text(output.id), outputName = text(output.name), outputCode = text(output.code), canonicalType = text(output.canonicalType)
      if (!text(rule.id) || !['WINDOW', 'CURRENT_DATE', 'DATE_DIFF', 'DATE_EXTRACT', 'DATE_START', 'DATE_END', 'DATE_FORMAT', 'DATE_TRUNC', 'CAST', 'ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'ROUND', 'ABS', 'FLOOR', 'CEIL', 'CONCAT', 'COALESCE', 'CASE', 'SUBSTRING', 'TRIM', 'UPPER', 'LOWER', 'REPLACE'].includes(operation) || !outputID || !outputCode || !canonicalType) return []
      const persistedUnit = text(rule.unit).toUpperCase()
      const allowedUnits: GraphDateUnit[] = ['DAY', 'WEEK', 'MONTH', 'QUARTER', 'YEAR', 'WEEKDAY', 'DAY_OF_YEAR']
      const unit = (allowedUnits.includes(persistedUnit as GraphDateUnit) ? persistedUnit : 'DAY') as GraphDateUnit
      const legacyUnit = dateFormatUnit(rule.unit)
      return [{
        id: text(rule.id) || `rule_${ruleIndex + 1}`,
        operation,
        inputKeys: list(rule.inputKeys).map(text).filter(Boolean),
        output: {
          id: outputID,
          name: legacyDateFormat ? migrateLegacyDateOutputName(outputName || outputCode, legacyUnit) : outputName || outputCode,
          code: legacyDateFormat ? migrateLegacyDateOutputCode(outputCode, legacyUnit) : identifier(outputCode),
          canonicalType: legacyDateFormat ? 'STRING' : canonicalType,
        },
        ...(text(rule.unit) ? { unit } : {}),
        ...(text(rule.dateSource) === 'CURRENT_DATE' ? { dateSource: 'CURRENT_DATE' as const } : text(rule.dateSource) === 'FIELD' ? { dateSource: 'FIELD' as const } : {}),
        ...(text(rule.startDateSource) === 'CURRENT_DATE' ? { startDateSource: 'CURRENT_DATE' as const } : text(rule.startDateSource) === 'FIELD' ? { startDateSource: 'FIELD' as const } : {}),
        ...(text(rule.endDateSource) === 'CURRENT_DATE' ? { endDateSource: 'CURRENT_DATE' as const } : text(rule.endDateSource) === 'FIELD' ? { endDateSource: 'FIELD' as const } : {}),
        ...(text(rule.targetType) ? { targetType: text(rule.targetType) as GraphTransformRule['targetType'] } : {}),
        ...('matchValue' in rule ? { matchValue: text(rule.matchValue) } : {}),
        ...(text(rule.thenMode) === 'CURRENT_DATE' ? { thenMode: 'CURRENT_DATE' as const } : text(rule.thenMode) === 'FIELD' ? { thenMode: 'FIELD' as const } : text(rule.thenMode) === 'LITERAL' ? { thenMode: 'LITERAL' as const } : {}),
        ...('thenValue' in rule ? { thenValue: text(rule.thenValue) } : {}),
        ...(text(rule.elseMode) === 'CURRENT_DATE' ? { elseMode: 'CURRENT_DATE' as const } : text(rule.elseMode) === 'FIELD' ? { elseMode: 'FIELD' as const } : text(rule.elseMode) === 'LITERAL' ? { elseMode: 'LITERAL' as const } : {}),
        ...('elseValue' in rule ? { elseValue: text(rule.elseValue) } : {}),
        ...(text(rule.conditionOperator) ? { conditionOperator: text(rule.conditionOperator) as GraphConditionOperator } : {}),
        ...('conditionValues' in rule ? { conditionValues: list(rule.conditionValues).flatMap((value, valueIndex) => {
          const item = record(value), mode = text(item.mode) === 'FIELD' ? 'FIELD' as const : 'LITERAL' as const
          const itemValue = text(item.value)
          return itemValue || mode === 'LITERAL' ? [{ id: text(item.id) || `condition_value_${valueIndex + 1}`, mode, value: itemValue }] : []
        }) } : {}),
        ...(text(rule.fallbackMode) === 'CURRENT_DATE'
          ? { fallbackMode: 'CURRENT_DATE' as const }
          : text(rule.fallbackMode) === 'FIELD'
            ? { fallbackMode: 'FIELD' as const }
            : text(rule.fallbackMode) === 'LITERAL'
              ? { fallbackMode: 'LITERAL' as const }
              : operation === 'COALESCE'
                ? { fallbackMode: list(rule.inputKeys).length > 1 ? 'FIELD' as const : 'LITERAL' as const }
                : {}),
        ...('fallbackValue' in rule ? { fallbackValue: text(rule.fallbackValue) } : {}),
        ...('separator' in rule ? { separator: text(rule.separator) } : {}),
        ...(typeof rule.precision === 'number' ? { precision: Math.trunc(rule.precision) } : {}),
        ...(typeof rule.start === 'number' ? { start: Math.trunc(rule.start) } : {}),
        ...(typeof rule.length === 'number' ? { length: Math.trunc(rule.length) } : {}),
        ...('searchValue' in rule ? { searchValue: text(rule.searchValue) } : {}),
        ...('replacementValue' in rule ? { replacementValue: text(rule.replacementValue) } : {}),
        ...(text(rule.replaceSourceKey) ? { replaceSourceKey: text(rule.replaceSourceKey) } : {}),
        ...(operation === 'WINDOW' ? {
          windowFunction: (['ROW_NUMBER', 'RANK', 'DENSE_RANK', 'SUM', 'AVG', 'COUNT', 'MIN', 'MAX'].includes(text(rule.windowFunction).toUpperCase()) ? text(rule.windowFunction).toUpperCase() : 'ROW_NUMBER') as GraphWindowFunction,
          ...(text(rule.windowValueKey) ? { windowValueKey: text(rule.windowValueKey) } : {}),
          partitionByKeys: list(rule.partitionByKeys).map(text).filter(Boolean),
          orderBy: list(rule.orderBy).flatMap((value, orderIndex) => {
            const item = record(value), key = text(item.key), direction = text(item.direction).toUpperCase()
            return key ? [{ id: text(item.id) || `window_order_${orderIndex + 1}`, key, direction: direction === 'DESC' ? 'DESC' as const : 'ASC' as const }] : []
          }),
        } : {}),
      }]
    })
    const conditions: GraphFilterCondition[] = list(item.conditions).flatMap((value, conditionIndex) => {
      const condition = record(value)
      const id = text(condition.id)
      const inputKey = text(condition.inputKey)
      const operator = text(condition.operator) as GraphConditionOperator
      return id && inputKey && ['EQUALS', 'NOT_EQUALS', 'GT', 'GTE', 'LT', 'LTE', 'CONTAINS', 'NOT_CONTAINS', 'IN', 'NOT_IN', 'IS_NULL', 'IS_NOT_NULL'].includes(operator)
        ? [{
            id: id || `filter_condition_${conditionIndex + 1}`,
            inputKey,
            operator,
            ...(text(condition.valueMode).toUpperCase() === 'FIELD' ? { valueMode: 'FIELD' as const } : {}),
            value: text(condition.value),
          }]
        : []
    })
    return [{
      id,
      name: text(item.name) || `字段处理 ${index + 1}`,
      family,
      ...(componentType ? { componentType } : {}),
      input: input(item.input),
      position: point(item.position, { x: 642, y: 48 + index * 150 }),
      rules,
      ...(componentType === 'FILTER' ? { conditions } : {}),
    }]
  })
  const endRaw = record(raw.end), endID = text(endRaw.id)
  const end: GraphEnd | undefined = endID === 'end_1' ? {
    id: 'end_1', name: text(endRaw.name) || '最终输出', input: input(endRaw.input), position: point(endRaw.position, { x: 942, y: 123 }),
    outputs: list(endRaw.outputs).flatMap(value => { const field = record(value), key = text(field.key); return key ? [{ key, name: text(field.name) || key, code: identifier(text(field.code) || keyParts(key).field) }] : [] }),
  } : undefined
  const graph: DesignerGraphV1 = {
    version: '1.0',
    nodePositions: Object.fromEntries(Object.entries(nodePositionsRaw).map(([id, value], index) => [id, point(value, fallback.nodePositions[id] ?? { x: 42, y: 48 + index * 150 })])),
    nodeNames: Object.fromEntries(Object.entries(nodeNamesRaw).map(([id, value]) => [id, text(value)]).filter(([, value]) => value)),
    joins: joins.length || !fallback.joins.length ? joins : fallback.joins,
    groups,
    transforms,
    end,
  }
  for (const [id, value] of Object.entries(fallback.nodePositions)) if (!graph.nodePositions[id]) graph.nodePositions[id] = value
  for (const [id, value] of Object.entries(fallback.nodeNames)) if (!graph.nodeNames[id]) graph.nodeNames[id] = value
  return graph
}

/**
 * 早期固定画布可能只保存了节点坐标，却没有保存结束节点。单表且没有任何中间
 * 组件时，其可执行拓扑没有歧义：映射表本身就是数据集输入，应恢复为
 * “数据节点 → 结束节点”，避免修改旧数据集时要求用户重新搭线。
 */
function repairSingleNodeEnd(graph: DesignerGraphV1, nodes: DesignerNode[], fields: FieldOption[]): DesignerGraphV1 {
  if (graph.end || nodes.length !== 1 || graph.joins.length || graph.groups.length || graph.transforms?.length) return graph
  const node = nodes[0]
  const source: GraphInput = { kind: 'NODE', id: node.id }
  const persisted = new Map(fields.filter(field => field.finalOutput !== false).map(field => [field.key, field]))
  const outputs = graphProducedFields(source, graph, nodes, fields)
    .filter(field => !persisted.size || persisted.has(field.key))
    .map(field => ({
      key: field.key,
      name: persisted.get(field.key)?.name || field.name,
      code: identifier(persisted.get(field.key)?.code || field.code),
    }))
  const position = graph.nodePositions[node.id] ?? { x: 42, y: 48 }
  return {
    ...graph,
    end: {
      id: 'end_1',
      name: '最终输出',
      input: source,
      position: { x: position.x + 300, y: position.y },
      outputs,
    },
  }
}

/** 把早期 components/positions 画布仅作为显示层覆盖到由可执行 DSL 重建的图上。 */
function applyLegacyDesignerLayout(rawValue: unknown, graph: DesignerGraphV1): DesignerGraphV1 {
  const raw = record(rawValue)
  if (!Object.keys(raw).length) return graph
  const positionByID = new Map<string, CanvasPoint>()
  for (const [id, value] of Object.entries(record(raw.positions))) positionByID.set(id, point(value, { x: 42, y: 48 }))
  const names = new Map<string, string>()
  let outputPosition: CanvasPoint | undefined, outputName = ''
  for (const value of list(raw.components)) {
    const component = record(value), id = text(component.id), kind = text(component.kind).toUpperCase(), name = text(component.name)
    if (!id) continue
    if (name) names.set(id, name)
    if ('position' in component || 'x' in component || 'y' in component) positionByID.set(id, point(component.position ?? component, positionByID.get(id) ?? { x: 42, y: 48 }))
    if (kind === 'OUTPUT') {
      outputPosition = positionByID.get(id)
      outputName = name
    }
  }
  return {
    ...graph,
    nodePositions: Object.fromEntries(Object.entries(graph.nodePositions).map(([id, value]) => [id, positionByID.get(id) ?? value])),
    nodeNames: Object.fromEntries(Object.entries(graph.nodeNames).map(([id, value]) => [id, names.get(id) || value])),
    joins: graph.joins.map(join => ({ ...join, name: names.get(join.id) || join.name, position: positionByID.get(join.id) ?? join.position })),
    groups: graph.groups.map(group => ({ ...group, name: names.get(group.id) || group.name, position: positionByID.get(group.id) ?? group.position })),
    transforms: (graph.transforms ?? []).map(transform => ({ ...transform, name: names.get(transform.id) || transform.name, position: positionByID.get(transform.id) ?? transform.position })),
    ...(graph.end ? { end: { ...graph.end, name: outputName || names.get(graph.end.id) || graph.end.name, position: outputPosition ?? positionByID.get(graph.end.id) ?? graph.end.position } } : {}),
  }
}

/** 把旧 DSL 升级为可编辑图；新 DSL 则精确恢复保存的拓扑、名称和坐标。 */
export function hydrateDesignerGraph(dsl: DatasetDSL, nodes: DesignerNode[], joins: JoinOption[], fields: FieldOption[]): DesignerGraphV1 {
  const legacyDSL = dsl as LegacyDSL
  const graph: DesignerGraphV1 = {
    version: '1.0',
    nodePositions: Object.fromEntries(nodes.map((node, index) => [node.id, { x: 42, y: 48 + index * 150 }])),
    nodeNames: Object.fromEntries(nodes.map(node => [node.id, node.table.businessName || node.table.tableName])),
    joins: legacyJoins(nodes, joins), groups: [], transforms: [],
  }
  const parsed = parseExistingDesigner(legacyDSL.designer, graph)
  if (parsed) return repairSingleNodeEnd(parsed, nodes, fields)

  for (const [index, rawValue] of list(legacyDSL.preAggregations).entries()) {
    const raw = record(rawValue), id = text(raw.id), nodeId = text(raw.nodeId), joinId = text(raw.joinId), side = text(raw.joinSide)
    if (!id || !nodeId) continue
    const source = nodes.find(node => node.id === nodeId), optionMap = new Map(fields.map(field => [field.key, field]))
    const dimensions = list(raw.groupBy).flatMap(value => {
      const item = record(value), field = text(item.field), key = `${nodeId}.${field}`, column = source?.columns.find(value => value.columnName === field), option = optionMap.get(key)
      return field ? [{ key, name: option?.name || column?.businessName || field, code: identifier(option?.code || field), grouping: text(item.unit) || undefined }] : []
    })
    const metrics = list(raw.metrics).flatMap(value => {
      const item = record(value), field = text(item.field), aggregation = text(item.function), countRows = aggregation === 'COUNT' && item.countRows === true
      const key = countRows ? '*' : `${nodeId}.${field}`, column = source?.columns.find(value => value.columnName === field), option = optionMap.get(key)
      return field && aggregation ? [{
        key,
        ...(countRows ? { outputKey: `${id}.${field}` } : {}),
        name: countRows ? '总行数' : `${option?.name || column?.businessName || field} ${aggregation}`,
        code: identifier(countRows ? field : `${aggregation.toLowerCase()}_${option?.code || field}`),
        aggregation,
        ...(countRows ? { countRows: true } : {}),
      }] : []
    })
    graph.groups.push({
      id, name: `${source?.table.businessName || source?.table.tableName || '数据'}汇总`,
      input: { kind: 'NODE', id: nodeId }, position: { x: 342, y: 48 + index * 150 },
      ...(groupByMode(raw.groupByMode) ? { groupByMode: groupByMode(raw.groupByMode) } : {}),
      ...(groupByMode(raw.groupByMode) === 'GROUPING_SETS'
        ? { groupingSets: groupingSets(raw.groupingSets).map(groupingSet => groupingSet.map(field => `${nodeId}.${field}`)) }
        : {}),
      dimensions, metrics,
    })
    graph.joins = graph.joins.map(join => join.id === joinId ? { ...join, ...(side === 'RIGHT' ? { right: { kind: 'GROUP' as const, id } } : { left: { kind: 'GROUP' as const, id } }) } : join)
  }

  let root = graphRoot(nodes.map(node => node.id), graph)
  const hasLegacyFinalGroup = !graph.groups.some(group => group.input?.kind !== 'NODE') && fields.some(field => field.finalGroupBy || field.finalMetric && field.finalAggregation)
  if (hasLegacyFinalGroup && root) {
    const dimensions = fields.filter(field => field.finalGroupBy).map(field => ({ key: field.key, name: field.name || keyParts(field.key).field, code: identifier(field.code || keyParts(field.key).field), grouping: field.finalGrouping || undefined }))
    const metrics = fields.filter(field => field.finalMetric && field.finalAggregation).map(field => ({ key: field.key, name: field.name || keyParts(field.key).field, code: identifier(field.code || `${field.finalAggregation!.toLowerCase()}_${keyParts(field.key).field}`), aggregation: field.finalAggregation! }))
    const persistedFieldKeys = new Map(list(legacyDSL.fields).flatMap(value => {
      const raw = record(value), id = text(raw.id), code = text(raw.code)
      const field = fields.find(item => item.code === code)
      return id && field ? [[id, field.key] as const] : []
    }))
    const mode = groupByMode(legacyDSL.groupByMode)
    const id = `group_${graph.groups.length + 1}`
    graph.groups.push({
      id, name: '最终汇总', input: root, position: { x: 642, y: 123 },
      ...(mode ? { groupByMode: mode } : {}),
      ...(mode === 'GROUPING_SETS'
        ? { groupingSets: groupingSets(legacyDSL.groupingSets).map(groupingSet => groupingSet.flatMap(fieldID => persistedFieldKeys.get(fieldID) ?? [])) }
        : {}),
      dimensions, metrics,
    })
    root = { kind: 'GROUP', id }
  } else root = graphRoot(nodes.map(node => node.id), graph)
  const upstream = graphProducedFields(root, graph, nodes, fields)
  const persisted = new Map(fields.filter(field => field.finalOutput !== false).map(field => [field.key, field]))
  graph.end = {
    id: 'end_1', name: '最终输出', input: root, position: { x: 42 + (graph.joins.length + graph.groups.length + 1) * 300, y: 123 },
    outputs: upstream.filter(item => !persisted.size || persisted.has(item.key)).map(item => ({ key: item.key, name: persisted.get(item.key)?.name || item.name, code: identifier(persisted.get(item.key)?.code || item.code) })),
  }
  return applyLegacyDesignerLayout(legacyDSL.designer, layoutDesignerGraph(graph, nodes.map(node => node.id)))
}

type DWDTransformStep = {
  operation: GraphTransformOperation
  additionalInputKeys?: string[]
  unit?: GraphTransformRule['unit']
  targetType?: GraphTransformRule['targetType']
  fallbackMode?: GraphTransformRule['fallbackMode']
  fallbackValue?: string
  separator?: string
  precision?: number
  start?: number
  length?: number
  searchValue?: string
  replacementValue?: string
  matchValue?: string
  thenMode?: GraphTransformRule['thenMode']
  thenValue?: string
  elseMode?: GraphTransformRule['elseMode']
  elseValue?: string
  conditionOperator?: GraphConditionOperator
}

const dwdLiteralText = (value: unknown): string | null => {
  const literal = record(value)
  if (text(literal.type).toUpperCase() !== 'LITERAL') return null
  if (typeof literal.value === 'string') return literal.value
  if (typeof literal.value === 'number' || typeof literal.value === 'boolean') return String(literal.value)
  return null
}

const dwdIsCurrentDate = (value: unknown): boolean =>
  text(record(value).type).toUpperCase() === 'CURRENT_DATE'

const dwdExpressionsEqual = (left: unknown, right: unknown): boolean =>
  JSON.stringify(left) === JSON.stringify(right)

const dwdExpressionSourceKey = (value: unknown): string => {
  const expression = record(value)
  const type = text(expression.type).toUpperCase()
  if (type === 'FIELD_REF') {
    const nodeID = text(expression.nodeId), field = text(expression.field)
    return nodeID && field ? `${nodeID}.${field}` : ''
  }
  if (type === 'CAST') return dwdExpressionSourceKey(expression.argument)
  if (type === 'COALESCE') {
    const args = list(expression.arguments)
    return dwdLiteralText(args[1]) === '' ? dwdExpressionSourceKey(args[0]) : ''
  }
  return ''
}

const dwdUnwrapNullSafeConcat = (value: unknown): unknown => {
  const expression = record(value)
  if (text(expression.type).toUpperCase() !== 'COALESCE') return value
  const args = list(expression.arguments)
  return dwdLiteralText(args[1]) === '' ? args[0] : value
}

function dwdTransformChain(value: unknown): { sourceKey: string; steps: DWDTransformStep[] } | null {
  const expression = record(value)
  const rawType = text(expression.type).toUpperCase()
  if (rawType === 'FIELD_REF') {
    const sourceKey = dwdExpressionSourceKey(expression)
    return sourceKey ? { sourceKey, steps: [] } : null
  }
  const type = rawType as GraphTransformOperation
  const unary = (step: DWDTransformStep) => {
    const inner = dwdTransformChain(expression.argument)
    return inner ? { ...inner, steps: [...inner.steps, step] } : null
  }
  if (type === 'TRIM' || type === 'UPPER' || type === 'LOWER' || type === 'ABS' || type === 'FLOOR' || type === 'CEIL') {
    return unary({ operation: type })
  }
  if (type === 'DATE_FORMAT' || type === 'DATE_TRUNC') {
    const unit = text(expression.unit).toUpperCase()
    if (!['DAY', 'WEEK', 'MONTH', 'QUARTER', 'YEAR'].includes(unit) || type === 'DATE_FORMAT' && unit === 'WEEK') return null
    return unary({ operation: type, unit: unit as GraphTransformRule['unit'] })
  }
  if (type === 'CAST') {
    const targetType = text(expression.targetType).toUpperCase()
    if (!['STRING', 'INTEGER', 'DECIMAL', 'BOOLEAN', 'DATE', 'DATETIME'].includes(targetType)) return null
    return unary({ operation: 'CAST', targetType: targetType as GraphTransformRule['targetType'] })
  }
  if (type === 'COALESCE') {
    const args = list(expression.arguments)
    const inner = dwdTransformChain(args[0])
    const fallbackValue = dwdLiteralText(args[1])
    if (!inner || fallbackValue === null && !dwdIsCurrentDate(args[1])) return null
    return {
      ...inner,
      steps: [...inner.steps, dwdIsCurrentDate(args[1])
        ? { operation: 'COALESCE', fallbackMode: 'CURRENT_DATE' }
        : { operation: 'COALESCE', fallbackMode: 'LITERAL', fallbackValue: fallbackValue! }],
    }
  }
  if (type === 'REPLACE' || type === 'SUBSTRING' || type === 'ROUND') {
    const args = list(expression.arguments)
    const inner = dwdTransformChain(args[0])
    if (!inner) return null
    if (type === 'REPLACE') {
      const searchValue = dwdLiteralText(args[1]), replacementValue = dwdLiteralText(args[2])
      return searchValue !== null && replacementValue !== null
        ? { ...inner, steps: [...inner.steps, { operation: type, searchValue, replacementValue }] }
        : null
    }
    if (type === 'SUBSTRING') {
      const start = Number(dwdLiteralText(args[1])), length = Number(dwdLiteralText(args[2]))
      return Number.isInteger(start) && start >= 1 && Number.isInteger(length) && length >= 0
        ? { ...inner, steps: [...inner.steps, { operation: type, start, length }] }
        : null
    }
    const precision = Number(dwdLiteralText(args[1]))
    return Number.isInteger(precision) && precision >= -10 && precision <= 10
      ? { ...inner, steps: [...inner.steps, { operation: type, precision }] }
      : null
  }
  if (type === 'ADD' || type === 'SUBTRACT' || type === 'MULTIPLY' || type === 'DIVIDE') {
    const args = list(expression.arguments)
    const inner = dwdTransformChain(args[0]), secondaryKey = dwdExpressionSourceKey(args[1])
    return inner && secondaryKey
      ? { ...inner, steps: [...inner.steps, { operation: type, additionalInputKeys: [secondaryKey] }] }
      : null
  }
  if (type === 'CONCAT') {
    const args = list(expression.arguments)
    const separatorLiteral = args.length === 3 ? dwdLiteralText(args[1]) : undefined
    if (separatorLiteral === null) return null
    const separator = separatorLiteral
    const secondaryIndex = separator === undefined ? 1 : 2
    const inner = dwdTransformChain(dwdUnwrapNullSafeConcat(args[0]))
    const secondaryKey = dwdExpressionSourceKey(args[secondaryIndex])
    return inner && secondaryKey
      ? { ...inner, steps: [...inner.steps, { operation: type, additionalInputKeys: [secondaryKey], ...(separator === undefined ? {} : { separator }) }] }
      : null
  }
  if (type === 'CASE') {
    const branches = list(expression.whens)
    if (branches.length !== 1) return null
    const branch = record(branches[0]), condition = record(branch.when)
    const conditionOperator = text(condition.type).toUpperCase() as GraphConditionOperator
    if (!['EQUALS', 'NOT_EQUALS', 'GT', 'GTE', 'LT', 'LTE', 'CONTAINS', 'NOT_CONTAINS', 'IS_NULL', 'IS_NOT_NULL'].includes(conditionOperator)) return null
    const conditionExpression = conditionOperator === 'IS_NULL' || conditionOperator === 'IS_NOT_NULL' ? condition.argument : condition.left
    const inner = dwdTransformChain(conditionExpression)
    const matchValue = conditionOperator === 'IS_NULL' || conditionOperator === 'IS_NOT_NULL' ? '' : dwdLiteralText(condition.right)
    const thenValue = dwdLiteralText(branch.then), elseValue = dwdLiteralText(expression.else)
    const thenMode = dwdIsCurrentDate(branch.then) ? 'CURRENT_DATE' as const : dwdExpressionsEqual(branch.then, conditionExpression) ? 'FIELD' as const : 'LITERAL' as const
    const elseMode = dwdIsCurrentDate(expression.else) ? 'CURRENT_DATE' as const : dwdExpressionsEqual(expression.else, conditionExpression) ? 'FIELD' as const : 'LITERAL' as const
    if (!inner || matchValue === null || thenValue === null && thenMode === 'LITERAL' || elseValue === null && elseMode === 'LITERAL') return null
    return {
      ...inner,
      steps: [...inner.steps, {
        operation: 'CASE', conditionOperator, matchValue, thenMode, thenValue: thenValue ?? '', elseMode, elseValue: elseValue ?? '',
      }],
    }
  }
  return null
}

/**
 * 自动 DWD 把数据库侧处理保存为字段表达式。这里将全部现有处理能力还原为可编辑
 * DAG：同一功能在同一依赖阶段的多字段规则合并到一个组件；只有确有先后依赖时，
 * 才会生成下一个组件。结束节点引用最终转换产物，保存后仍回写等价表达式。
 */
export function expandSystemDWDDesignerGraph(
  value: DesignerGraphV1,
  nodes: DesignerNode[],
  fields: FieldOption[],
): DesignerGraphV1 {
  let graph: DesignerGraphV1 = {
    ...value,
    nodeNames: Object.fromEntries(nodes.map((node, index) => [node.id, node.alias || `t${index + 1}`])),
    joins: value.joins.map((join, index) => ({ ...join, name: `j${index + 1}` })),
    groups: value.groups.map((group, index) => ({ ...group, name: `g${index + 1}` })),
    transforms: [],
    ...(value.end ? { end: { ...value.end, name: 'o1' } } : {}),
  }
  let root = graphRoot(nodes.map(node => node.id), graph)
  if (!root || !graph.end) return layoutDesignerGraph(graph, nodes.map(node => node.id))

  const fieldByKey = new Map(fields.map(field => [field.key, field]))
  const pipelines = fields
    .filter(item => item.finalOutput !== false && item.persistedExpression)
    .flatMap(field => {
      const chain = dwdTransformChain(field.persistedExpression)
      return chain && chain.sourceKey === field.key && chain.steps.length
        ? [{ field, sourceKey: chain.sourceKey, remaining: [...chain.steps] }]
        : []
    })
  const finalKeyBySource = new Map<string, string>()
  const currentKeyBySource = new Map(pipelines.map(({ sourceKey }) => [sourceKey, sourceKey]))
  const canonicalTypeBySource = new Map(pipelines.map(({ field, sourceKey }) => [
    sourceKey,
    nodes
      .find(node => node.id === keyParts(sourceKey).nodeId)
      ?.columns.find(column => column.columnName === keyParts(sourceKey).field)
      ?.canonicalType || field.canonicalType || 'STRING',
  ]))
  const transforms: GraphTransform[] = []
  const componentIdentity = (operation: GraphTransformOperation): { family: GraphTransformFamily; componentType: GraphTransformComponentType } => {
    if (operation === 'DATE_FORMAT' || operation === 'DATE_TRUNC') return { family: 'DATE', componentType: 'DATE_FORMAT' }
    if (operation === 'CAST') return { family: 'CAST', componentType: 'CAST' }
    if (operation === 'TRIM') return { family: 'TEXT', componentType: 'TEXT_TRIM' }
    if (operation === 'UPPER' || operation === 'LOWER') return { family: 'TEXT', componentType: 'TEXT_CASE' }
    if (operation === 'REPLACE') return { family: 'TEXT', componentType: 'TEXT_REPLACE' }
    if (operation === 'SUBSTRING') return { family: 'TEXT', componentType: 'TEXT_SUBSTRING' }
    if (operation === 'CONCAT') return { family: 'SPLIT_MERGE', componentType: 'TEXT_CONCAT' }
    if (operation === 'ABS') return { family: 'NUMBER', componentType: 'NUMBER_ABSOLUTE' }
    if (operation === 'ROUND' || operation === 'FLOOR' || operation === 'CEIL') return { family: 'NUMBER', componentType: 'NUMBER_ROUNDING' }
    if (operation === 'ADD' || operation === 'SUBTRACT' || operation === 'MULTIPLY' || operation === 'DIVIDE') return { family: 'NUMBER', componentType: 'NUMBER_ARITHMETIC' }
    if (operation === 'CASE') return { family: 'CONDITION', componentType: 'CONDITION' }
    return { family: 'NULL', componentType: 'NULL' }
  }
  const outputCanonicalType = (current: string, step: DWDTransformStep): string => {
    if (step.operation === 'CAST') return step.targetType || current
    if (step.operation === 'CASE' && (step.thenMode === 'CURRENT_DATE' || step.elseMode === 'CURRENT_DATE')) return 'DATE'
    if (['DATE_FORMAT', 'TRIM', 'UPPER', 'LOWER', 'REPLACE', 'SUBSTRING', 'CONCAT', 'CASE'].includes(step.operation)) return 'STRING'
    if (['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE'].includes(step.operation)) return 'DECIMAL'
    return current
  }
  while (pipelines.some(pipeline => pipeline.remaining.length)) {
    const nextSteps = pipelines.flatMap(pipeline => pipeline.remaining[0] ? [pipeline.remaining[0]] : [])
    const operation = nextSteps.find(candidate => pipelines.every(pipeline => {
      const nextIndex = pipeline.remaining.findIndex(step => step.operation === candidate.operation)
      return nextIndex < 0 || nextIndex === 0
    }))?.operation || nextSteps[0]?.operation
    if (!operation) break
    const componentIndex = transforms.length + 1
    const id = `transform_${componentIndex}`
    const rules: GraphTransformRule[] = []
    const componentInputKeys = new Map(currentKeyBySource)
    for (const pipeline of pipelines) {
      const step = pipeline.remaining[0]
      if (!step || step.operation !== operation) continue
      const { field, sourceKey } = pipeline
      const currentKey = componentInputKeys.get(sourceKey) || sourceKey
      const canonicalType = outputCanonicalType(
        canonicalTypeBySource.get(sourceKey) || field.canonicalType || 'STRING',
        step,
      )
      const last = pipeline.remaining.length === 1
      const ruleIndex = rules.length + 1
      const outputID = 'value'
      const stableOutputID = `${outputID}_${ruleIndex}`
      const nextKey = `${id}.${stableOutputID}`
      rules.push({
        id: `${id}_rule_${ruleIndex}`,
        operation: step.operation,
        inputKeys: [
          currentKey,
          ...(step.additionalInputKeys ?? []).map(key => componentInputKeys.get(key) || key),
        ],
        output: {
          id: stableOutputID,
          name: last ? field.name || field.code || keyParts(field.key).field : `${field.name || field.code || keyParts(field.key).field}处理中`,
          code: identifier(last ? field.code || keyParts(field.key).field : `${field.code || keyParts(field.key).field}_${componentIndex}_${ruleIndex}`),
          canonicalType,
        },
        replaceSourceKey: currentKey,
        ...(step.operation === 'COALESCE' ? { fallbackMode: step.fallbackMode || 'LITERAL', fallbackValue: step.fallbackValue } : {}),
        ...(step.operation === 'CAST' ? { targetType: step.targetType } : {}),
        ...(step.unit ? { unit: step.unit } : {}),
        ...(step.separator !== undefined ? { separator: step.separator } : {}),
        ...(step.precision !== undefined ? { precision: step.precision } : {}),
        ...(step.start !== undefined ? { start: step.start } : {}),
        ...(step.length !== undefined ? { length: step.length } : {}),
        ...(step.searchValue !== undefined ? { searchValue: step.searchValue } : {}),
        ...(step.replacementValue !== undefined ? { replacementValue: step.replacementValue } : {}),
        ...(step.matchValue !== undefined ? { matchValue: step.matchValue } : {}),
        ...(step.thenMode ? { thenMode: step.thenMode } : {}),
        ...(step.thenValue !== undefined ? { thenValue: step.thenValue } : {}),
        ...(step.elseMode ? { elseMode: step.elseMode } : {}),
        ...(step.elseValue !== undefined ? { elseValue: step.elseValue } : {}),
        ...(step.conditionOperator ? { conditionOperator: step.conditionOperator } : {}),
      })
      pipeline.remaining.shift()
      canonicalTypeBySource.set(sourceKey, canonicalType)
      currentKeyBySource.set(sourceKey, nextKey)
      finalKeyBySource.set(sourceKey, nextKey)
    }
    if (!rules.length) continue
    const identity = componentIdentity(operation)
    transforms.push({
      id,
      name: `c${componentIndex}`,
      ...identity,
      input: root,
      position: { x: 0, y: 0 },
      rules,
    })
    root = { kind: 'TRANSFORM', id }
  }
  if (!transforms.length) return layoutDesignerGraph(graph, nodes.map(node => node.id))
  graph = {
    ...graph,
    transforms,
    end: {
      ...graph.end,
      input: root,
      outputs: graph.end.outputs.map(output => ({
        ...output,
        key: finalKeyBySource.get(output.key) || output.key,
        name: fieldByKey.get(output.key)?.name || output.name,
        code: identifier(fieldByKey.get(output.key)?.code || output.code),
      })),
    },
  }
  return layoutDesignerGraph(graph, nodes.map(node => node.id))
}

export const serializeDesignerGraph = (graph: DesignerGraphV1): DesignerGraphV1 => ({
  version: '1.0',
  nodePositions: Object.fromEntries(Object.entries(graph.nodePositions).map(([id, value]) => [id, point(value, { x: 42, y: 48 })])),
  nodeNames: { ...graph.nodeNames },
  joins: graph.joins.map(item => ({ ...item, position: point(item.position, { x: 342, y: 48 }), outputKeys: [...item.outputKeys] })),
  groups: graph.groups.map(item => ({
    ...item,
    position: point(item.position, { x: 342, y: 48 }),
    ...(item.groupingSets ? { groupingSets: item.groupingSets.map(groupingSet => [...groupingSet]) } : {}),
    dimensions: item.dimensions.map(value => ({ ...value })),
    metrics: item.metrics.map(value => ({ ...value })),
  })),
  transforms: (graph.transforms ?? []).map(item => ({
    ...item,
    ...(normalizeGraphTransformComponentType(item.componentType) ? { componentType: normalizeGraphTransformComponentType(item.componentType) } : {}),
    position: point(item.position, { x: 642, y: 48 }),
    rules: item.rules.map(rule => ({
      ...rule,
      inputKeys: [...rule.inputKeys],
      output: { ...rule.output },
      ...(rule.conditionValues ? { conditionValues: rule.conditionValues.map(value => ({ ...value })) } : {}),
      ...(rule.partitionByKeys ? { partitionByKeys: [...rule.partitionByKeys] } : {}),
      ...(rule.orderBy ? { orderBy: rule.orderBy.map(value => ({ ...value })) } : {}),
    })),
    ...(item.conditions ? { conditions: item.conditions.map(condition => ({ ...condition })) } : {}),
  })),
  ...(graph.end ? { end: { ...graph.end, position: point(graph.end.position, { x: 642, y: 48 }), outputs: graph.end.outputs.map(value => ({ ...value })) } } : {}),
})
