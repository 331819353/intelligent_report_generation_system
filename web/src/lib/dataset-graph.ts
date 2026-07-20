import type { DatasetDSL, DesignerNode, FieldOption, JoinOption } from './datasets'

export type CanvasPoint = { x: number; y: number }
export type GraphInput = { kind: 'NODE' | 'JOIN' | 'GROUP'; id: string }
export type GraphJoin = {
  id: string; name: string; left?: GraphInput; right?: GraphInput
  position: CanvasPoint; outputKeys: string[]
}
export type GraphDimension = { key: string; name: string; code: string; grouping?: string }
export type GraphMetric = { key: string; name: string; code: string; aggregation: string }
export type GraphGroup = {
  id: string; name: string; input?: GraphInput; position: CanvasPoint
  dimensions: GraphDimension[]; metrics: GraphMetric[]
}
export type GraphEndOutput = { key: string; name: string; code: string }
export type GraphEnd = {
  id: 'end_1'; name: string; input?: GraphInput; position: CanvasPoint
  outputs: GraphEndOutput[]
}
export type GraphTarget = { kind: 'JOIN' | 'GROUP' | 'OUTPUT'; id: string }
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
  end?: GraphEnd
}

export type ProducedField = {
  key: string
  name: string
  code: string
  producerName: string
  kind: 'ATTRIBUTE' | 'DIMENSION' | 'METRIC'
  binding: { nodeId: string; field: string }
  canonicalType: string
  aggregation?: string
  grouping?: string
}

type LegacyDSL = DatasetDSL & { designer?: unknown; joins?: unknown; preAggregations?: unknown; groupBy?: unknown }

const record = (value: unknown): Record<string, unknown> => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
const list = (value: unknown): unknown[] => Array.isArray(value) ? value : []
const text = (value: unknown): string => typeof value === 'string' ? value : ''
const finite = (value: unknown, fallback: number): number => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : fallback
const point = (value: unknown, fallback: CanvasPoint): CanvasPoint => {
  const raw = record(value)
  return { x: finite(raw.x, fallback.x), y: finite(raw.y, fallback.y) }
}
const input = (value: unknown): GraphInput | undefined => {
  const raw = record(value), kind = text(raw.kind), id = text(raw.id)
  return id && (kind === 'NODE' || kind === 'JOIN' || kind === 'GROUP') ? { kind, id } : undefined
}
const keyParts = (key: string) => {
  const dot = key.indexOf('.')
  return dot > 0 ? { nodeId: key.slice(0, dot), field: key.slice(dot + 1) } : { nodeId: '', field: '' }
}
const identifier = (value: string) => value.trim().replace(/[^A-Za-z0-9_]/g, '_').replace(/^[^A-Za-z]+/, '') || 'field'

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
  return `结束节点「${graph.end?.name || value.id}」`
}

const graphInputExists = (value: GraphInput, graph: DesignerGraphV1, nodeIDs: ReadonlySet<string>): boolean => {
  if (value.kind === 'NODE') return nodeIDs.has(value.id)
  if (value.kind === 'GROUP') return graph.groups.some(item => item.id === value.id)
  return graph.joins.some(item => item.id === value.id)
}

const graphTargetExists = (value: GraphTarget, graph: DesignerGraphV1): boolean => {
  if (value.kind === 'GROUP') return graph.groups.some(item => item.id === value.id)
  if (value.kind === 'JOIN') return graph.joins.some(item => item.id === value.id)
  return graph.end?.id === value.id
}

export function graphLeaves(value: GraphInput | undefined, graph: Pick<DesignerGraphV1, 'joins' | 'groups'>, visited = new Set<string>()): string[] {
  if (!value) return []
  if (value.kind === 'NODE') return [value.id]
  const visitKey = graphInputKey(value)
  if (visited.has(visitKey)) return []
  const next = new Set(visited).add(visitKey)
  if (value.kind === 'GROUP') {
    const group = graph.groups.find(item => item.id === value.id)
    return group ? graphLeaves(group.input, graph, next) : []
  }
  const join = graph.joins.find(item => item.id === value.id)
  return join ? [...graphLeaves(join.left, graph, next), ...graphLeaves(join.right, graph, next)] : []
}

export function graphContains(value: GraphInput, target: GraphInput, graph: Pick<DesignerGraphV1, 'joins' | 'groups'>, visited = new Set<string>()): boolean {
  if (value.kind === target.kind && value.id === target.id) return true
  if (value.kind === 'NODE') return false
  const visitKey = graphInputKey(value)
  if (visited.has(visitKey)) return false
  const next = new Set(visited).add(visitKey)
  if (value.kind === 'GROUP') {
    const group = graph.groups.find(item => item.id === value.id)
    return Boolean(group?.input && graphContains(group.input, target, graph, next))
  }
  const join = graph.joins.find(item => item.id === value.id)
  return Boolean(join && ((join.left && graphContains(join.left, target, graph, next)) || (join.right && graphContains(join.right, target, graph, next))))
}

/** 判断把 source 作为 target 的输入后，是否会形成自环或间接循环。 */
export function wouldCreateGraphCycle(source: GraphInput, target: GraphTarget, graph: Pick<DesignerGraphV1, 'joins' | 'groups'>): boolean {
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

export function graphRoot(nodeIDs: string[], graph: Pick<DesignerGraphV1, 'joins' | 'groups'>): GraphInput | undefined {
  const used = new Set<string>()
  for (const join of graph.joins) for (const value of [join.left, join.right]) if (value) used.add(graphInputKey(value))
  for (const group of graph.groups) if (group.input) used.add(graphInputKey(group.input))
  const candidates: GraphInput[] = [
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
      kind: 'ATTRIBUTE' as const, binding: { nodeId: node.id, field: column.columnName }, canonicalType: column.canonicalType,
    }
  })
}

/** 返回一个组件对下游公开的稳定产物；展示名称与物理字段绑定分离。 */
export function graphProducedFields(value: GraphInput | undefined, graph: Pick<DesignerGraphV1, 'joins' | 'groups'> & { nodeNames?: Record<string, string> }, nodes: DesignerNode[], fields: FieldOption[], visited = new Set<string>()): ProducedField[] {
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
    const upstream = new Map(graphProducedFields(group.input, graph, nodes, fields, next).map(item => [item.key, item]))
    return [
      ...group.dimensions.flatMap(item => {
        const source = upstream.get(item.key)
        return source ? [{ ...source, name: item.name, code: item.code, producerName: group.name, kind: 'DIMENSION' as const, grouping: item.grouping, aggregation: undefined }] : []
      }),
      ...group.metrics.flatMap(item => {
        const source = upstream.get(item.key)
        return source ? [{ ...source, name: item.name, code: item.code, producerName: group.name, kind: 'METRIC' as const, aggregation: item.aggregation, grouping: undefined, canonicalType: item.aggregation === 'COUNT' || item.aggregation === 'COUNT_DISTINCT' ? 'INTEGER' : source.canonicalType }] : []
      }),
    ]
  }
  const join = graph.joins.find(item => item.id === value.id)
  if (!join) return []
  const upstream = [...graphProducedFields(join.left, graph, nodes, fields, next), ...graphProducedFields(join.right, graph, nodes, fields, next)]
  const allowed = new Set(join.outputKeys.length ? join.outputKeys : upstream.map(item => item.key))
  return upstream.filter(item => allowed.has(item.key)).map(item => ({ ...item, producerName: join.name }))
}

export const graphOutputKeys = (value: GraphInput | undefined, graph: Pick<DesignerGraphV1, 'joins' | 'groups'>, nodes: DesignerNode[], fields: FieldOption[]) => graphProducedFields(value, graph, nodes, fields).map(item => item.key)

export const graphProducedFieldLabel = (field: ProducedField) => `${field.producerName} / ${field.name} · ${field.code} · ${field.kind === 'DIMENSION' ? '维度' : field.kind === 'METRIC' ? `${field.aggregation || '聚合'} 指标` : '字段'}`

export function layoutDesignerGraph(graph: DesignerGraphV1, nodeIDs: string[]): DesignerGraphV1 {
  const keys = [
    ...nodeIDs.map(id => `NODE:${id}`),
    ...graph.groups.map(item => `GROUP:${item.id}`),
    ...graph.joins.map(item => `JOIN:${item.id}`),
    ...(graph.end ? [`OUTPUT:${graph.end.id}`] : []),
  ]
  const stableOrder = new Map(keys.map((key, index) => [key, index]))
  const dependencies = new Map<string, string[]>()
  for (const group of graph.groups) dependencies.set(`GROUP:${group.id}`, group.input ? [graphInputKey(group.input)] : [])
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
    for (const nodeID of [...graphLeaves(left, { joins: result, groups: [] }), ...graphLeaves(right, { joins: result, groups: [] })]) component.set(nodeID, root)
  }
  return result
}

function parseExistingDesigner(rawValue: unknown, fallback: DesignerGraphV1): DesignerGraphV1 | null {
  const raw = record(rawValue)
  if (!Object.keys(raw).length || text(raw.version) !== '1.0') return null
  if (!['nodePositions', 'joins', 'groups', 'end'].some(key => key in raw)) return null
  const nodePositionsRaw = record(raw.nodePositions), nodeNamesRaw = record(raw.nodeNames)
  const joins: GraphJoin[] = list(raw.joins).flatMap((value, index) => {
    const item = record(value), id = text(item.id)
    if (!id) return []
    return [{ id, name: text(item.name) || `关联结果 ${index + 1}`, left: input(item.left), right: input(item.right), position: point(item.position, { x: 510 + index * 250, y: 150 }), outputKeys: list(item.outputKeys).map(text).filter(Boolean) }]
  })
  const groups: GraphGroup[] = list(raw.groups).flatMap((value, index) => {
    const item = record(value), id = text(item.id)
    if (!id) return []
    const dimensions = list(item.dimensions).flatMap(value => { const field = record(value), key = text(field.key); return key ? [{ key, name: text(field.name) || key, code: identifier(text(field.code) || keyParts(key).field), grouping: text(field.grouping) || undefined }] : [] })
    const metrics = list(item.metrics).flatMap(value => { const field = record(value), key = text(field.key), aggregation = text(field.aggregation); return key && aggregation ? [{ key, name: text(field.name) || key, code: identifier(text(field.code) || `${aggregation.toLowerCase()}_${keyParts(key).field}`), aggregation }] : [] })
    return [{ id, name: text(item.name) || `分组结果 ${index + 1}`, input: input(item.input), position: point(item.position, { x: 342, y: 48 + index * 150 }), dimensions, metrics }]
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
  if (graph.end || nodes.length !== 1 || graph.joins.length || graph.groups.length) return graph
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
    joins: legacyJoins(nodes, joins), groups: [],
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
      const item = record(value), field = text(item.field), aggregation = text(item.function), key = `${nodeId}.${field}`, column = source?.columns.find(value => value.columnName === field), option = optionMap.get(key)
      return field && aggregation ? [{ key, name: `${option?.name || column?.businessName || field} ${aggregation}`, code: identifier(`${aggregation.toLowerCase()}_${option?.code || field}`), aggregation }] : []
    })
    graph.groups.push({ id, name: `${source?.table.businessName || source?.table.tableName || '数据'}汇总`, input: { kind: 'NODE', id: nodeId }, position: { x: 342, y: 48 + index * 150 }, dimensions, metrics })
    graph.joins = graph.joins.map(join => join.id === joinId ? { ...join, ...(side === 'RIGHT' ? { right: { kind: 'GROUP' as const, id } } : { left: { kind: 'GROUP' as const, id } }) } : join)
  }

  let root = graphRoot(nodes.map(node => node.id), graph)
  const hasLegacyFinalGroup = !graph.groups.some(group => group.input?.kind !== 'NODE') && fields.some(field => field.finalGroupBy || field.finalMetric && field.finalAggregation)
  if (hasLegacyFinalGroup && root) {
    const dimensions = fields.filter(field => field.finalGroupBy).map(field => ({ key: field.key, name: field.name || keyParts(field.key).field, code: identifier(field.code || keyParts(field.key).field), grouping: field.finalGrouping || undefined }))
    const metrics = fields.filter(field => field.finalMetric && field.finalAggregation).map(field => ({ key: field.key, name: field.name || keyParts(field.key).field, code: identifier(field.code || `${field.finalAggregation!.toLowerCase()}_${keyParts(field.key).field}`), aggregation: field.finalAggregation! }))
    const id = `group_${graph.groups.length + 1}`
    graph.groups.push({ id, name: '最终汇总', input: root, position: { x: 642, y: 123 }, dimensions, metrics })
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

export const serializeDesignerGraph = (graph: DesignerGraphV1): DesignerGraphV1 => ({
  version: '1.0',
  nodePositions: Object.fromEntries(Object.entries(graph.nodePositions).map(([id, value]) => [id, point(value, { x: 42, y: 48 })])),
  nodeNames: { ...graph.nodeNames },
  joins: graph.joins.map(item => ({ ...item, position: point(item.position, { x: 342, y: 48 }), outputKeys: [...item.outputKeys] })),
  groups: graph.groups.map(item => ({ ...item, position: point(item.position, { x: 342, y: 48 }), dimensions: item.dimensions.map(value => ({ ...value })), metrics: item.metrics.map(value => ({ ...value })) })),
  ...(graph.end ? { end: { ...graph.end, position: point(graph.end.position, { x: 642, y: 48 }), outputs: graph.end.outputs.map(value => ({ ...value })) } } : {}),
})
