import { apiRequest } from './api'
import { requestDatasetAIStream, type DatasetAIProgressEvent } from './dataset-ai-stream'
export { requestDatasetAIStream, type DatasetAIProgressEvent } from './dataset-ai-stream'
import {
  graphContains,
  graphLeaves,
  graphOutputKeys,
  graphProducedFields,
  graphRoot,
  layoutDesignerGraph,
  normalizeGraphTransformComponentType,
  validateDesignerGraph,
  type DesignerGraphV1,
  type GraphInput,
  type GraphJoin,
  type GraphGroup,
  type GraphTransform,
  type GraphTransformComponentType,
  type GraphTransformFamily,
  type GraphTransformRule,
} from './dataset-graph'
import {
  joinCardinalityForType,
  type AssetColumn,
  type AssetTable,
  type DatasetDraft,
  type DesignerNode,
  type FieldOption,
  type JoinOption,
} from './datasets'
export { datasetAICanvasMode, datasetAIRequestContext } from './dataset-ai-context'

export type DatasetAIInput = { kind: 'NODE' | 'JOIN' | 'GROUP' | 'TRANSFORM'; id: string }
export type DatasetAIPlanNode = { id: string; tableId: string; alias: string; selectedColumns: string[] }
export type DatasetAIJoinCondition = {
  leftNodeId: string; leftColumn: string; rightNodeId: string; rightColumn: string
}
export type DatasetAIPlanJoin = {
  id: string; name: string; left: DatasetAIInput; right: DatasetAIInput
  joinType: 'INNER' | 'LEFT' | 'RIGHT' | 'FULL'; conditions: DatasetAIJoinCondition[]
}
export type DatasetAIPlanDimension = { nodeId: string; column: string; grouping: '' | 'DAY' | 'WEEK' | 'MONTH' | 'QUARTER' | 'YEAR' }
export type DatasetAIPlanMetric = { nodeId: string; column: string; aggregation: 'SUM' | 'AVG' | 'COUNT' | 'COUNT_DISTINCT' | 'MIN' | 'MAX' }
export type DatasetAIPlanGroup = {
  id: string; name: string; input: DatasetAIInput
  dimensions: DatasetAIPlanDimension[]; metrics: DatasetAIPlanMetric[]
}
export type DatasetAIPlanFilterCondition = {
  id: string; inputKey: string
  operator: 'EQUALS' | 'NOT_EQUALS' | 'GT' | 'GTE' | 'LT' | 'LTE' | 'CONTAINS' | 'NOT_CONTAINS' | 'IN' | 'NOT_IN' | 'IS_NULL' | 'IS_NOT_NULL'
  valueMode: 'LITERAL' | 'FIELD'; value: string
}
export type DatasetAIPlanTransform = {
  id: string; name: string; input: DatasetAIInput
  family: GraphTransformFamily; componentType: GraphTransformComponentType; rules: GraphTransformRule[]
  /** FILTER components carry conditions instead of rules; every upstream field passes through. */
  conditions?: DatasetAIPlanFilterCondition[]
}
export type DatasetAIPlanOutput = { nodeId: string; column: string; key?: string; name: string; code: string }
export type DatasetAIGraphPlan = {
  dataset: { name: string; description: string }
  nodes: DatasetAIPlanNode[]
  joins: DatasetAIPlanJoin[]
  groups: DatasetAIPlanGroup[]
  transforms?: DatasetAIPlanTransform[]
  end: { name: string; input: DatasetAIInput; outputs: DatasetAIPlanOutput[] }
}
export type DatasetAIChangeOperation = {
  action: 'ADD' | 'UPDATE' | 'REMOVE'
  componentKind: 'DATASET' | 'NODE' | 'JOIN' | 'GROUP' | 'TRANSFORM' | 'END'
  componentId: string
  componentName: string
  fields: string[]
  inputChanges: Array<{ field: string; from: DatasetAIInput; to: DatasetAIInput }>
  description: string
}
export type DatasetAIFieldBinding = { nodeId: string; tableId: string; column: string }
export type DatasetAIFieldChange = {
  field: DatasetAIFieldBinding
  selectionAction: 'ADD' | 'KEEP' | 'REMOVE'
  purpose: 'FINAL_OUTPUT' | 'INTERNAL_ONLY' | 'SELECTED_ONLY'
  groupUses: Array<{
    groupId: string
    role: 'DIMENSION' | 'METRIC'
    grouping: '' | 'DAY' | 'WEEK' | 'MONTH' | 'QUARTER' | 'YEAR'
    aggregation: '' | 'SUM' | 'AVG' | 'COUNT' | 'COUNT_DISTINCT' | 'MIN' | 'MAX'
  }>
  joinUses: Array<{ joinId: string; side: 'LEFT' | 'RIGHT'; peer: DatasetAIFieldBinding }>
  outputUses: Array<{ endId: 'end_1'; name: string; code: string }>
}
export type DatasetAIChangeSet = { operations: DatasetAIChangeOperation[]; fieldChanges: DatasetAIFieldChange[] }
export type DatasetAIProposal = {
  schemaVersion: '2.4'
  mode: 'CREATE' | 'MODIFY'
  summary: string
  assumptions: string[]
  warnings: string[]
  changeSet: DatasetAIChangeSet
  plan: DatasetAIGraphPlan
}
export type DatasetAIPlanResult = { requestId: string; proposal: DatasetAIProposal }
export type DatasetAITableSuggestionResult = {
  items: Array<{ tableId: string; score?: number; layer?: string }>
  retrievalMode: 'LEXICAL' | 'SHADOW' | 'HYBRID'
  embeddingReady: boolean
  degraded: boolean
  degradedReason?: string
}

export type MaterializedDatasetAIPlan = {
  draft: DatasetDraft
  graph: DesignerGraphV1
  metadata: { name: string; description: string }
}

const identifier = (value: string) => value.trim().replace(/[^A-Za-z0-9_]/g, '_').replace(/^[^A-Za-z]+/, '') || 'field'
const transformComponentType = (transform: Pick<GraphTransform, 'family' | 'componentType' | 'rules'>): GraphTransformComponentType => {
  if (transform.componentType) return normalizeGraphTransformComponentType(transform.componentType) || transform.componentType
  const operation = transform.rules[0]?.operation
  if (transform.family === 'DATE') return 'DATE_FORMAT'
  if (transform.family === 'CAST') return 'CAST'
  if (transform.family === 'CONDITION') return 'CONDITION'
  if (transform.family === 'NULL') return 'NULL'
  if (transform.family === 'NUMBER') return operation === 'ABS' ? 'NUMBER_ABSOLUTE' : ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE'].includes(operation) ? 'NUMBER_ARITHMETIC' : 'NUMBER_ROUNDING'
  return operation === 'UPPER' || operation === 'LOWER' ? 'TEXT_CASE' : operation === 'TRIM' ? 'TEXT_TRIM' : operation === 'REPLACE' ? 'TEXT_REPLACE' : operation === 'SUBSTRING' ? 'TEXT_SUBSTRING' : 'TEXT_CONCAT'
}
const fieldKey = (nodeId: string, column: string) => `${nodeId}.${column}`
const keyParts = (key: string) => {
  const dot = key.indexOf('.')
  return dot > 0 ? { nodeId: key.slice(0, dot), column: key.slice(dot + 1) } : { nodeId: '', column: '' }
}
const isTime = (column: AssetColumn) => ['DATE', 'DATETIME', 'TIMESTAMP'].includes(column.canonicalType.toUpperCase()) || column.semanticType.toUpperCase() === 'DATE'

const editorFieldOption = (node: DesignerNode, column: AssetColumn, selected: boolean): FieldOption => ({
  key: fieldKey(node.id, column.columnName),
  code: identifier(`${node.alias}_${column.columnName}`),
  name: column.businessName || column.columnName,
  role: isTime(column) ? 'TIME' : column.semanticType === 'IDENTIFIER' ? 'IDENTIFIER' : 'ATTRIBUTE',
  aggregation: '', groupBy: false, grouping: '', output: selected, metric: false,
  finalOutput: selected, finalGroupBy: false, finalGrouping: '', finalMetric: false, finalAggregation: '',
})

const inputExists = (value: DatasetAIInput | undefined, nodes: Set<string>, joins: Set<string>, groups: Set<string>, transforms: Set<string> = new Set()) => Boolean(value && (
  value.kind === 'NODE' ? nodes.has(value.id) : value.kind === 'JOIN' ? joins.has(value.id) : value.kind === 'GROUP' ? groups.has(value.id) : transforms.has(value.id)
))
const sameInput = (left: GraphInput | undefined, right: GraphInput | undefined) => Boolean(left && right && left.kind === right.kind && left.id === right.id)

/**
 * Build compact, non-SQL context from the live editor. Incomplete components are omitted while
 * the selected data nodes are retained, so AI can still repair a half-built canvas.
 */
export function datasetAIPlanFromEditor(
  draft: DatasetDraft,
  graph: DesignerGraphV1,
  metadata: { name: string; description: string },
): DatasetAIGraphPlan | undefined {
  const eligibleNodes = draft.nodes.filter(node => node.selected.length > 0)
  if (!eligibleNodes.length) return undefined
  const nodeIDs = new Set(eligibleNodes.map(node => node.id))
  const joinIDs = new Set(graph.joins.map(join => join.id))
  const groupIDs = new Set(graph.groups.map(group => group.id))
  const transformIDs = new Set((graph.transforms ?? []).map(transform => transform.id))
  const joins = graph.joins.flatMap((box): DatasetAIPlanJoin[] => {
    const configured = draft.joins.find(join => join.id === box.id)
    if (!configured || !inputExists(box.left as DatasetAIInput | undefined, nodeIDs, joinIDs, groupIDs, transformIDs) || !inputExists(box.right as DatasetAIInput | undefined, nodeIDs, joinIDs, groupIDs, transformIDs)) return []
    const conditions = configured.conditions?.length
      ? configured.conditions
      : [{ id: `${configured.id}_condition_1`, leftField: configured.leftField, rightField: configured.rightField }]
    if (!conditions.length || conditions.some(condition => !condition.leftField || !condition.rightField)) return []
    const joinType = (['INNER', 'LEFT', 'RIGHT', 'FULL'].includes(configured.joinType) ? configured.joinType : 'LEFT') as DatasetAIPlanJoin['joinType']
    return [{
      id: box.id, name: box.name, left: box.left as DatasetAIInput, right: box.right as DatasetAIInput,
      joinType,
      conditions: conditions.map(condition => ({
        leftNodeId: configured.leftNodeId, leftColumn: condition.leftField,
        rightNodeId: configured.rightNodeId, rightColumn: condition.rightField,
      })),
    }]
  })
  const includedJoinIDs = new Set(joins.map(join => join.id))
  const groups = graph.groups.flatMap((group): DatasetAIPlanGroup[] => {
    if (!group.input || !inputExists(group.input as DatasetAIInput, nodeIDs, includedJoinIDs, groupIDs, transformIDs)) return []
    const dimensions = group.dimensions.flatMap(item => {
      const parts = keyParts(item.key)
      return parts.nodeId && parts.column ? [{ nodeId: parts.nodeId, column: parts.column, grouping: (item.grouping || '') as DatasetAIPlanDimension['grouping'] }] : []
    })
    const metrics = group.metrics.flatMap(item => {
      const parts = keyParts(item.key)
      return parts.nodeId && parts.column ? [{ nodeId: parts.nodeId, column: parts.column, aggregation: item.aggregation as DatasetAIPlanMetric['aggregation'] }] : []
    })
    if (!dimensions.length || !metrics.length) return []
    return [{
      id: group.id, name: group.name, input: group.input as DatasetAIInput,
      dimensions,
      metrics,
    }]
  })
  const includedGroupIDs = new Set(groups.map(group => group.id))
  const transforms: DatasetAIPlanTransform[] = (graph.transforms ?? []).flatMap(transform => {
    if (!transform.input || !inputExists(transform.input as DatasetAIInput, nodeIDs, includedJoinIDs, includedGroupIDs, transformIDs)) return []
    const componentType = transformComponentType(transform)
    const isFilter = componentType === 'FILTER'
    if (isFilter ? !(transform.conditions?.length) : !transform.rules.length) return []
    return [{
      id: transform.id, name: transform.name, input: transform.input as DatasetAIInput,
      family: isFilter ? 'CONDITION' as const : transform.family, componentType,
      rules: isFilter ? [] : transform.rules.map(rule => ({ ...rule, inputKeys: [...rule.inputKeys], output: { ...rule.output }, ...(rule.conditionValues ? { conditionValues: rule.conditionValues.map(value => ({ ...value })) } : {}) })),
      ...(isFilter ? { conditions: (transform.conditions ?? []).map(condition => ({ id: condition.id, inputKey: condition.inputKey, operator: condition.operator as DatasetAIPlanFilterCondition['operator'], valueMode: condition.valueMode ?? 'LITERAL', value: condition.value })) } : {}),
    }]
  })
  const includedTransformIDs = new Set(transforms.map(transform => transform.id))
  const completeGraph = {
    joins: joins.map(join => ({ ...join, position: { x: 0, y: 0 }, outputKeys: [] })),
    groups: groups.map(group => {
      const configured = graph.groups.find(item => item.id === group.id)
      return {
        id: group.id, name: group.name, input: group.input as GraphInput, position: { x: 0, y: 0 },
        ...(configured?.groupByMode ? { groupByMode: configured.groupByMode } : {}),
        ...(configured?.groupingSets ? { groupingSets: configured.groupingSets.map(groupingSet => [...groupingSet]) } : {}),
        dimensions: configured?.dimensions.map(item => ({ ...item })) ?? [],
        metrics: configured?.metrics.map(item => ({ ...item })) ?? [],
      }
    }),
    transforms: transforms.map(transform => ({ ...transform, position: { x: 0, y: 0 } })),
  }
  const fallbackInput = graphRoot([...nodeIDs], completeGraph) ?? { kind: 'NODE' as const, id: eligibleNodes[0].id }
  const endInput = inputExists(graph.end?.input as DatasetAIInput | undefined, nodeIDs, includedJoinIDs, includedGroupIDs, includedTransformIDs)
    ? graph.end!.input as DatasetAIInput
    : fallbackInput as DatasetAIInput
  // A half-built draft may contain disconnected or invalid components. Keep the largest valid
  // executable branch as the trusted MODIFY baseline; the page sends the omitted draft pieces as
  // repair context so the model can complete, replace, or remove them without treating the canvas
  // as a brand-new DAG.
  const activeNodeIDs = new Set(graphLeaves(endInput as GraphInput, completeGraph))
  const activeNodes = eligibleNodes.filter(node => activeNodeIDs.has(node.id))
  const activeJoins = joins.filter(join => graphContains(endInput as GraphInput, { kind: 'JOIN', id: join.id }, completeGraph))
  const activeGroups = groups.filter(group => graphContains(endInput as GraphInput, { kind: 'GROUP', id: group.id }, completeGraph))
  const activeTransforms = transforms.filter(transform => graphContains(endInput as GraphInput, { kind: 'TRANSFORM', id: transform.id }, completeGraph))
  const activeGraph = {
    joins: completeGraph.joins.filter(join => activeJoins.some(item => item.id === join.id)),
    groups: completeGraph.groups.filter(group => activeGroups.some(item => item.id === group.id)),
    transforms: completeGraph.transforms.filter(transform => activeTransforms.some(item => item.id === transform.id)),
  }
  const endFields = new Map(graphProducedFields(endInput as GraphInput, activeGraph, activeNodes, draft.fields).map(field => [field.key, field]))
  const outputs = (graph.end?.outputs ?? []).flatMap(output => {
    const produced = endFields.get(output.key)
    return produced ? [{ nodeId: produced.binding.nodeId, column: produced.binding.field, key: output.key, name: output.name, code: output.code }] : []
  })
  const fallbackOutput = (() => {
    const node = activeNodes[0]
    if (!node) return []
    const column = node.columns.find(item => node.selected.includes(item.columnName))
    return column ? [{ nodeId: node.id, column: column.columnName, key: fieldKey(node.id, column.columnName), name: column.businessName || column.columnName, code: identifier(column.columnName) }] : []
  })()
  return {
    dataset: { name: metadata.name || draft.name, description: metadata.description || draft.description },
    nodes: activeNodes.map(node => ({ id: node.id, tableId: node.table.id, alias: node.alias, selectedColumns: [...node.selected] })),
    joins: activeJoins,
    groups: activeGroups,
    transforms: activeTransforms,
    end: { name: graph.end?.name || '最终输出', input: endInput, outputs: outputs.length ? outputs : fallbackOutput },
  }
}

export type DatasetAIClarificationAnswer = {
  question: string
  text?: string
  selectedComponent?: { componentKind: string; componentId: string }
}

export type DatasetAIProposalOptions = {
  sessionId?: string
  clarificationAnswer?: DatasetAIClarificationAnswer
}

export async function requestDatasetAIProposal(
  datasetId: string | undefined,
  instruction: string,
  current?: DatasetAIGraphPlan,
  onProgress?: (event: DatasetAIProgressEvent) => void,
  options: DatasetAIProposalOptions = {},
): Promise<DatasetAIPlanResult> {
  const path = datasetId ? `/v1/datasets/${encodeURIComponent(datasetId)}/ai/proposals` : '/v1/datasets/ai/proposals'
  const init: RequestInit = {
    method: 'POST',
    body: JSON.stringify({
      instruction,
      ...(current ? { current } : {}),
      ...(options.sessionId ? { sessionId: options.sessionId } : {}),
      ...(options.clarificationAnswer ? { clarificationAnswer: options.clarificationAnswer } : {}),
    }),
  }
  return requestDatasetAIStream<DatasetAIPlanResult>(path, init, 'result', onProgress)
}

/** Rank source tables with the same metadata/tag hybrid retrieval used by DAG planning. */
export function requestDatasetAITableSuggestions(
  instruction: string,
  currentTableIds: string[] = [],
  limit = 4,
  modelKind?: 'DIM' | 'DWD' | 'DWS' | 'ADS',
	role?: 'PRIMARY' | 'DIMENSION',
	sessionId?: string,
): Promise<DatasetAITableSuggestionResult> {
  return apiRequest<DatasetAITableSuggestionResult>('/v1/datasets/ai/table-suggestions', {
    method: 'POST',
    body: JSON.stringify({ instruction, currentTableIds, limit, ...(modelKind ? { modelKind } : {}), ...(role ? { role } : {}), ...(sessionId ? { sessionId } : {}) }),
  })
}

/** Convert a reviewed proposal into one atomic editor snapshot; no partial state is applied. */
export async function materializeDatasetAIPlan(
  plan: DatasetAIGraphPlan,
  tables: AssetTable[],
  loadColumns: (tableId: string) => Promise<AssetColumn[]>,
  base: DatasetDraft,
  code: string,
  baseGraph?: DesignerGraphV1,
): Promise<MaterializedDatasetAIPlan> {
  // DIM/DWD/DWS/ADS 的当前节点引用的是不可变 dataset-version:* 虚拟资产，并不会出现在
  // 物理元数据表列表中。修改方案必须优先复用编辑器已经精确解析的表和字段快照，
  // 只有新引用的物理资产才调用 columns API。
  const baseNodeByTableID = new Map(base.nodes.map(node => [node.table.id, node]))
  const tableByID = new Map([
    ...tables.map(table => [table.id, table] as const),
    ...base.nodes.map(node => [node.table.id, node.table] as const),
  ])
  const tableIDs = [...new Set(plan.nodes.map(node => node.tableId))]
  const columnEntries = await Promise.all(tableIDs.map(async tableId => {
    const current = baseNodeByTableID.get(tableId)?.columns
    const columns = current ?? await loadColumns(tableId)
    return [tableId, columns.filter(column => !column.assetStatus || column.assetStatus === 'ACTIVE')] as const
  }))
  const columnsByTable = new Map(columnEntries)
  const baseNodeByID = new Map(base.nodes.map(node => [node.id, node]))
  const baseFieldByKey = new Map(base.fields.map(field => [field.key, field]))
  const nodes: DesignerNode[] = plan.nodes.map(spec => {
    const table = tableByID.get(spec.tableId)
    const columns = columnsByTable.get(spec.tableId) ?? []
    if (!table || !columns.length) throw new Error('AI 方案引用的映射表已不可用，请重新生成')
    const available = new Set(columns.map(column => column.columnName))
    if (!spec.selectedColumns.length || spec.selectedColumns.some(column => !available.has(column))) throw new Error(`AI 方案引用了“${table.businessName || table.tableName}”中已失效的字段，请重新生成`)
    const previous = baseNodeByID.get(spec.id)
    return {
      id: spec.id, alias: spec.alias, table, columns, selected: [...spec.selectedColumns],
      groupingEnabled: previous?.table.id === spec.tableId ? previous.groupingEnabled ?? false : false,
    }
  })
  const nodeByID = new Map(nodes.map(node => [node.id, node]))
  const fields = nodes.flatMap(node => node.columns.map(column => {
    const selected = node.selected.includes(column.columnName)
    const generated = editorFieldOption(node, column, selected)
    const previous = baseFieldByKey.get(generated.key)
    return previous ? { ...generated, ...previous, key: generated.key, output: selected } : generated
  }))
  const baseGraphJoinByID = new Map((baseGraph?.joins ?? []).map(join => [join.id, join]))
  let graphJoins: GraphJoin[] = plan.joins.map(join => {
    const previous = baseGraphJoinByID.get(join.id)
    const sameTopology = Boolean(previous && sameInput(previous.left, join.left as GraphInput) && sameInput(previous.right, join.right as GraphInput))
    return {
      id: join.id, name: join.name, left: join.left as GraphInput, right: join.right as GraphInput,
      position: sameTopology ? previous!.position : { x: 0, y: 0 },
      outputKeys: sameTopology ? [...previous!.outputKeys] : [],
    }
  })
  const joins: JoinOption[] = plan.joins.map(join => {
    const first = join.conditions[0]
    if (!first) throw new Error(`AI 方案中的关联“${join.name}”缺少关联字段`)
    return {
      id: join.id, leftNodeId: first.leftNodeId, rightNodeId: first.rightNodeId,
      leftField: first.leftColumn, rightField: first.rightColumn, joinType: join.joinType,
      cardinality: joinCardinalityForType(join.joinType), manualConfirmed: true,
      conditions: join.conditions.map((condition, index) => {
        const previous = base.joins.find(item => item.id === join.id)?.conditions?.[index]
        return {
          id: previous?.leftField === condition.leftColumn && previous.rightField === condition.rightColumn ? previous.id : `${join.id}_condition_${index + 1}`,
          leftField: condition.leftColumn,
          rightField: condition.rightColumn,
        }
      }),
    }
  })
  const fieldName = (nodeId: string, columnName: string) => {
    const column = nodeByID.get(nodeId)?.columns.find(item => item.columnName === columnName)
    if (column) return column.businessName || column.columnName
    const transformOutput = (plan.transforms ?? []).find(item => item.id === nodeId)?.rules.find(rule => rule.output.id === columnName)?.output
    if (transformOutput) return transformOutput.name
    throw new Error(`AI 方案引用的字段 ${nodeId}.${columnName} 已不可用`)
  }
  const baseGraphGroupByID = new Map((baseGraph?.groups ?? []).map(group => [group.id, group]))
  const graphGroups: GraphGroup[] = plan.groups.map(group => {
    const previous = baseGraphGroupByID.get(group.id)
    const sameTopology = Boolean(previous && sameInput(previous.input, group.input as GraphInput))
    return {
      id: group.id, name: group.name, input: group.input as GraphInput,
      position: sameTopology ? previous!.position : { x: 0, y: 0 },
      ...(previous?.groupByMode ? { groupByMode: previous.groupByMode } : {}),
      ...(previous?.groupingSets ? { groupingSets: previous.groupingSets.map(groupingSet => [...groupingSet]) } : {}),
      dimensions: group.dimensions.map(dimension => {
        const key = fieldKey(dimension.nodeId, dimension.column)
        const old = previous?.dimensions.find(item => item.key === key)
        return {
          key, name: old?.name || fieldName(dimension.nodeId, dimension.column),
          code: old?.code || identifier(`${nodeByID.get(dimension.nodeId)?.alias || dimension.nodeId}_${dimension.column}`),
        }
      }),
      metrics: group.metrics.map(metric => {
        const key = fieldKey(metric.nodeId, metric.column)
        const old = previous?.metrics.find(item => item.key === key && item.aggregation === metric.aggregation)
        return {
          key, name: old?.name || fieldName(metric.nodeId, metric.column),
          code: old?.code || identifier(`${nodeByID.get(metric.nodeId)?.alias || metric.nodeId}_${metric.column}`),
          aggregation: metric.aggregation,
        }
      }),
    }
  })
  const baseGraphTransformByID = new Map((baseGraph?.transforms ?? []).map(transform => [transform.id, transform]))
  const graphTransforms: GraphTransform[] = (plan.transforms ?? []).map(transform => {
    const previous = baseGraphTransformByID.get(transform.id)
    const sameTopology = Boolean(previous && sameInput(previous.input, transform.input as GraphInput))
    return {
      id: transform.id, name: transform.name, input: transform.input as GraphInput,
      family: transform.family, componentType: transform.componentType,
      position: sameTopology ? previous!.position : { x: 0, y: 0 },
      rules: transform.rules.map(rule => ({ ...rule, inputKeys: [...rule.inputKeys], output: { ...rule.output }, ...(rule.conditionValues ? { conditionValues: rule.conditionValues.map(value => ({ ...value })) } : {}) })),
      ...(transform.componentType === 'FILTER' && transform.conditions ? { conditions: transform.conditions.map(condition => ({ ...condition })) } : {}),
    }
  })
  const outputFilterGraph: DesignerGraphV1 = {
    version: '1.0', nodePositions: {}, nodeNames: {}, groups: graphGroups,
    transforms: graphTransforms,
    joins: graphJoins.map(join => ({ ...join, outputKeys: [] })),
  }
  graphJoins = graphJoins.map(join => {
    const allowed = new Set([
      ...graphOutputKeys(join.left, outputFilterGraph, nodes, fields),
      ...graphOutputKeys(join.right, outputFilterGraph, nodes, fields),
    ])
    const target: GraphInput = { kind: 'JOIN', id: join.id }
    const leafNodeIDs = new Set(graphLeaves(target, outputFilterGraph))
    const required = new Set<string>()
    for (const downstreamJoin of plan.joins) {
      const consumesTarget = graphContains(downstreamJoin.left as GraphInput, target, outputFilterGraph) || graphContains(downstreamJoin.right as GraphInput, target, outputFilterGraph)
      if (!consumesTarget) continue
      for (const condition of downstreamJoin.conditions) {
        required.add(fieldKey(condition.leftNodeId, condition.leftColumn))
        required.add(fieldKey(condition.rightNodeId, condition.rightColumn))
      }
    }
    for (const group of plan.groups) {
      if (!graphContains(group.input as GraphInput, target, outputFilterGraph)) continue
      for (const dimension of group.dimensions) required.add(fieldKey(dimension.nodeId, dimension.column))
      for (const metric of group.metrics) required.add(fieldKey(metric.nodeId, metric.column))
    }
    for (const transform of plan.transforms ?? []) {
      if (!graphContains(transform.input as GraphInput, target, outputFilterGraph)) continue
      for (const rule of transform.rules) {
        for (const key of rule.inputKeys) required.add(key)
        for (const value of rule.conditionValues ?? []) if (value.mode === 'FIELD') required.add(value.value)
        if (rule.replaceSourceKey) required.add(rule.replaceSourceKey)
      }
    }
    if (graphContains(plan.end.input as GraphInput, target, outputFilterGraph)) {
      for (const output of plan.end.outputs) required.add(output.key || fieldKey(output.nodeId, output.column))
    }
    // A downstream field only needs to pass through joins whose branch contains its source node.
    // This avoids requiring a sibling branch's output from every nested join in a larger DAG.
    const downstreamRequired = [...required].filter(key => allowed.has(key) || leafNodeIDs.has(keyParts(key).nodeId))
    const missing = downstreamRequired.filter(key => !allowed.has(key))
    if (missing.length) throw new Error(`AI 方案中的关联“${join.name}”无法向下游提供字段 ${missing.join('、')}，请重新生成`)
    // An empty outputKeys list means "pass every field through" and must stay empty. When the
    // user has an explicit whitelist, preserve it and add only fields required by the new plan.
    if (!join.outputKeys.length) return join
    const outputKeys = new Set(join.outputKeys.filter(key => allowed.has(key)))
    for (const key of downstreamRequired) outputKeys.add(key)
    return { ...join, outputKeys: [...outputKeys] }
  })
  const finalPlanGraph: DesignerGraphV1 = { ...outputFilterGraph, joins: graphJoins }
  const availableEndKeys = new Set(graphOutputKeys(plan.end.input as GraphInput, finalPlanGraph, nodes, fields))
  const missingEndKeys = plan.end.outputs.map(output => output.key || fieldKey(output.nodeId, output.column)).filter(key => !availableEndKeys.has(key))
  if (missingEndKeys.length) throw new Error(`AI 方案的最终输出引用了不可用字段 ${missingEndKeys.join('、')}，请重新生成`)
  const generatedGraph = layoutDesignerGraph({
    version: '1.0', nodePositions: {}, nodeNames: Object.fromEntries(nodes.map(node => [node.id, node.table.businessName || node.table.tableName])),
    joins: graphJoins, groups: graphGroups, transforms: graphTransforms,
    end: {
      id: 'end_1', name: plan.end.name, input: plan.end.input as GraphInput, position: { x: 0, y: 0 },
      outputs: plan.end.outputs.map(output => ({ key: output.key || fieldKey(output.nodeId, output.column), name: output.name, code: identifier(output.code) })),
    },
  }, nodes.map(node => node.id))
  const stableNodeIDs = new Set(plan.nodes.flatMap(spec => baseNodeByID.get(spec.id)?.table.id === spec.tableId ? [spec.id] : []))
  const stableJoinIDs = new Set(graphJoins.flatMap(join => {
    const previous = baseGraphJoinByID.get(join.id)
    return previous && sameInput(previous.left, join.left) && sameInput(previous.right, join.right) ? [join.id] : []
  }))
  const stableGroupIDs = new Set(graphGroups.flatMap(group => {
    const previous = baseGraphGroupByID.get(group.id)
    return previous && sameInput(previous.input, group.input) ? [group.id] : []
  }))
  const stableTransformIDs = new Set(graphTransforms.flatMap(transform => {
    const previous = baseGraphTransformByID.get(transform.id)
    return previous && sameInput(previous.input, transform.input) ? [transform.id] : []
  }))
  const graph: DesignerGraphV1 = {
    ...generatedGraph,
    nodePositions: Object.fromEntries(Object.entries(generatedGraph.nodePositions).map(([id, position]) => [id, stableNodeIDs.has(id) ? baseGraph?.nodePositions[id] ?? position : position])),
    joins: generatedGraph.joins.map(join => stableJoinIDs.has(join.id) ? { ...join, position: baseGraphJoinByID.get(join.id)!.position } : join),
    groups: generatedGraph.groups.map(group => stableGroupIDs.has(group.id) ? { ...group, position: baseGraphGroupByID.get(group.id)!.position } : group),
    transforms: (generatedGraph.transforms ?? []).map(transform => stableTransformIDs.has(transform.id) ? { ...transform, position: baseGraphTransformByID.get(transform.id)!.position } : transform),
    ...(generatedGraph.end ? { end: { ...generatedGraph.end, position: baseGraph?.end?.id === generatedGraph.end.id ? baseGraph.end.position : generatedGraph.end.position } } : {}),
  }
  const validation = validateDesignerGraph(graph, nodes.map(node => node.id))
  if (!validation.valid) throw new Error(validation.errors[0] || 'AI 方案不是有效的数据流，请重新生成')
  const outputCodes = plan.end.outputs.map(output => identifier(output.code))
  const allowedNodeFields = new Set(nodes.flatMap(node => node.selected.map(column => fieldKey(node.id, column))))
  const draft: DatasetDraft = {
    ...base,
    code,
    name: plan.dataset.name,
    description: plan.dataset.description,
    nodes,
    fields,
    joins,
    filters: base.filters.filter(filter => allowedNodeFields.has(fieldKey(filter.nodeId, filter.field))),
    calculations: base.calculations.filter(item => allowedNodeFields.has(item.leftKey) && allowedNodeFields.has(item.rightKey)),
    grainDescription: base.grainDescription.trim() || `每一行代表一条${plan.dataset.name}记录`,
    grainKeys: base.grainKeys.filter(key => outputCodes.includes(key)).length ? base.grainKeys.filter(key => outputCodes.includes(key)) : outputCodes.length ? [outputCodes[0]] : [],
    groupingEnabled: graphGroups.length > 0,
    finalConfigured: true,
    finalGroupingEnabled: graphGroups.length > 0,
    designer: graph,
  }
  return { draft, graph, metadata: { name: plan.dataset.name, description: plan.dataset.description } }
}
