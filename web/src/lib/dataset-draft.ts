import {
  datasetAPI,
  type AssetTable,
  type CalculatedField,
  type DatasetDraft,
  type DatasetRecord,
  type DesignerNode,
  type FieldOption,
  type FilterOption,
  type JoinOption,
  type ParameterOption,
} from './datasets'
import { hydrateDesignerGraph } from './dataset-graph'

const text = (value: unknown) => typeof value === 'string' ? value : ''
const object = (value: unknown) => (value && typeof value === 'object' ? value as Record<string, unknown> : {})
const list = (value: unknown) => Array.isArray(value) ? value : []

/**
 * 把服务端规范 DSL 还原为两个设计入口共用的画板状态。
 * 物理列始终重新读取资产中心，避免继续编辑时使用已经失效的字段快照。
 */
export async function hydrateDatasetDraft(record: DatasetRecord, tables: AssetTable[]): Promise<DatasetDraft> {
  const dsl = record.dsl
  const nodeValues = list(dsl.nodes).map(object)
  const nodes = await Promise.all(nodeValues.map(async (value, index): Promise<DesignerNode> => {
    const table = tables.find(item => item.id === text(value.tableId))
    if (!table) throw new Error(`第 ${index + 1} 个节点引用的表资产已不可用`)
    const columns = (await datasetAPI.columns(table.id)).items
    const projection = new Set(list(value.projection).map(text).filter(Boolean))
    // projection 是数据节点输出字段的持久化真值；旧草稿缺少 projection 时才兼容为
    // 全部当前有效列，避免重新打开后把用户已取消的字段悄悄勾选回来。
    const selected = projection.size
      ? columns.filter(column => projection.has(column.columnName)).map(column => column.columnName)
      : columns.map(column => column.columnName)
    return { id: text(value.id), alias: text(value.alias), table, columns, selected }
  }))
  const idToCode = new Map<string, string>()
  const groupByIDs = new Set(list(dsl.groupBy).map(text))
  const preAggregationValues = list(dsl.preAggregations).map(object)
  // 分组组件不再是画布级单例。完整读取每一项，并以“节点.字段”为键，避免两个
  // 数据节点都包含 customer_id 时把产物配置串到一起。
  const preAggregationGroups = new Map(preAggregationValues.flatMap(item => list(item.groupBy).map(object).map(field => [`${text(item.nodeId)}.${text(field.field)}`, text(field.unit)])))
  const preAggregationMetrics = new Map(preAggregationValues.flatMap(item => list(item.metrics).map(object).map(field => [`${text(item.nodeId)}.${text(field.field)}`, text(field.function)])))
  const fields: FieldOption[] = []
  const calculations: CalculatedField[] = []
  for (const raw of list(dsl.fields).map(object)) {
    idToCode.set(text(raw.id), text(raw.code))
    const expression = object(raw.expression)
    if (text(expression.type) === 'AGGREGATE') {
      const source = object(expression.argument)
      if (text(source.type) === 'FIELD_REF') {
        fields.push({ key: `${text(source.nodeId)}.${text(source.field)}`, role: 'MEASURE', aggregation: text(expression.function), code: text(raw.code), name: text(raw.name), output: true, metric: true, finalOutput: true })
      } else {
        const argumentsValue = list(source.arguments).map(object)
        calculations.push({ id: text(raw.id), code: text(raw.code), name: text(raw.name), operation: text(source.type), leftKey: `${text(argumentsValue[0]?.nodeId)}.${text(argumentsValue[0]?.field)}`, rightKey: `${text(argumentsValue[1]?.nodeId)}.${text(argumentsValue[1]?.field)}`, canonicalType: text(raw.canonicalType), aggregation: text(expression.function) })
      }
    } else if (text(expression.type) === 'FIELD_REF') {
      fields.push({ key: `${text(expression.nodeId)}.${text(expression.field)}`, role: text(raw.role), aggregation: '', code: text(raw.code), name: text(raw.name), groupBy: groupByIDs.has(text(raw.id)), output: true, finalOutput: true })
    } else if (text(expression.type) === 'DATE_TRUNC') {
      const source = object(expression.argument)
      fields.push({ key: `${text(source.nodeId)}.${text(source.field)}`, role: 'DIMENSION', aggregation: '', code: text(raw.code), name: text(raw.name), groupBy: true, grouping: text(expression.unit), output: true, finalOutput: true })
    } else {
      const argumentsValue = list(expression.arguments).map(object)
      calculations.push({ id: text(raw.id), code: text(raw.code), name: text(raw.name), operation: text(expression.type), leftKey: `${text(argumentsValue[0]?.nodeId)}.${text(argumentsValue[0]?.field)}`, rightKey: `${text(argumentsValue[1]?.nodeId)}.${text(argumentsValue[1]?.field)}`, canonicalType: text(raw.canonicalType), aggregation: '' })
    }
  }
  const persistedOutputKeys = new Set(fields.map(field => field.key))
  const joins: JoinOption[] = list(dsl.joins).map(object).map(raw => {
    const conditions = list(raw.conditions).map(object).map((condition, index) => ({ id: `${text(raw.id)}_condition_${index + 1}`, leftField: text(object(condition.leftExpression).field), rightField: text(object(condition.rightExpression).field) }))
    const first = conditions[0] ?? { leftField: '', rightField: '' }
    return { id: text(raw.id), leftNodeId: text(raw.leftNodeId), rightNodeId: text(raw.rightNodeId), leftField: first.leftField, rightField: first.rightField, joinType: text(raw.joinType), cardinality: '', manualConfirmed: Boolean(raw.manualConfirmed), conditions }
  })
  const configuredKeys = new Set(fields.map(field => field.key))
  for (const node of nodes) {
    for (const column of node.columns) {
      const key = `${node.id}.${column.columnName}`
      if (configuredKeys.has(key)) continue
      fields.push({
        key,
        role: column.semanticType === 'IDENTIFIER' ? 'IDENTIFIER' : 'ATTRIBUTE',
        aggregation: '',
        code: column.columnName,
        name: column.businessName || column.columnName,
        output: node.selected.includes(column.columnName),
        finalOutput: false,
      })
    }
  }
  const filters: FilterOption[] = list(dsl.filters).map(object).map(raw => {
    const expression = object(raw.expression), left = object(expression.left), right = object(expression.right)
    return { id: text(raw.id), nodeId: text(left.nodeId), field: text(left.field), operator: text(expression.type), value: text(right.value), parameterCode: text(right.code) }
  })
  const parameters: ParameterOption[] = list(dsl.parameters).map(object).map(raw => ({ code: text(raw.code), name: text(raw.name), dataType: text(raw.dataType), required: Boolean(raw.required), multiValue: Boolean(raw.multiValue) }))
  const grain = object(dsl.outputGrain)
  // 全局 groupBy 也会包含未做表内聚合的明细节点字段，不能据此把明细表误还原为
  // 聚合模式；只有节点确实产出聚合指标时才恢复为表内分组聚合。
  const preAggregations = preAggregationValues.flatMap(raw => {
    const id = text(raw.id), nodeId = text(raw.nodeId), joinId = text(raw.joinId), joinSide = text(raw.joinSide)
    return id && nodeId && joinId && (joinSide === 'LEFT' || joinSide === 'RIGHT')
      ? [{ id, nodeId, joinId, joinSide: joinSide as 'LEFT' | 'RIGHT' }]
      : []
  })
  const hasPreAggregation = preAggregations.length > 0
  const hasGroupingComponent = hasPreAggregation || groupByIDs.size > 0 && fields.some(field => Boolean(field.aggregation))
  const groupedNodeIDs = hasGroupingComponent ? new Set<string>() : new Set(fields.filter(field => field.aggregation).map(field => field.key.split('.')[0]))
  const projectedKeys = new Set(nodes.flatMap(node => node.selected.map(columnName => `${node.id}.${columnName}`)))
  const configuredFields = fields.map(field => ({
    ...field,
    output: projectedKeys.has(field.key),
    finalOutput: persistedOutputKeys.has(field.key),
    finalGroupBy: hasPreAggregation ? preAggregationGroups.has(field.key) : hasGroupingComponent && Boolean(field.groupBy),
    finalGrouping: hasPreAggregation ? preAggregationGroups.get(field.key) || '' : hasGroupingComponent ? field.grouping || '' : '',
    finalMetric: hasPreAggregation ? preAggregationMetrics.has(field.key) : hasGroupingComponent && Boolean(field.aggregation),
    finalAggregation: hasPreAggregation ? preAggregationMetrics.get(field.key) || '' : hasGroupingComponent ? field.aggregation : '',
  }))
  // 新固定图已经在 group/end 中单独保存各层产物名称。重新打开时，数据节点
  // 仍应显示资产字段本名，不能被结束节点的对外重命名反向污染。
  const hasFixedDesigner = Boolean(dsl.designer && (dsl.designer.nodePositions || dsl.designer.joins || dsl.designer.groups || dsl.designer.end))
  const graphFields = hasFixedDesigner ? configuredFields.map(field => {
    const [nodeID, ...fieldParts] = field.key.split('.'), columnName = fieldParts.join('.')
    const node = nodes.find(item => item.id === nodeID), column = node?.columns.find(item => item.columnName === columnName)
    if (!node || !column) return field
    return {
      ...field,
      code: `${node.alias}_${column.columnName}`,
      name: column.businessName || column.columnName,
      role: column.semanticType === 'IDENTIFIER' ? 'IDENTIFIER' : column.semanticType === 'DATE' ? 'TIME' : 'ATTRIBUTE',
    }
  }) : configuredFields
  const designer = hydrateDesignerGraph(dsl, nodes, joins, graphFields)
  return {
    code: record.code, name: record.name, description: record.description,
    nodes: nodes.map(node => ({ ...node, groupingEnabled: groupedNodeIDs.has(node.id) })),
    fields: graphFields, joins, filters, parameters, calculations,
    sorts: list(dsl.sorts).map(object).map(raw => ({ fieldId: idToCode.get(text(raw.fieldId)) ?? text(raw.fieldId), direction: text(raw.direction) })),
    grainDescription: text(grain.description), grainKeys: list(grain.keyFields).map(text),
    groupingEnabled: fields.some(field => Boolean(field.aggregation)) || calculations.some(field => Boolean(field.aggregation)),
    finalConfigured: hasGroupingComponent,
    finalGroupingEnabled: hasGroupingComponent,
    designer,
    ...(hasPreAggregation ? { preAggregation: preAggregations[0], preAggregations, finalOutputKeys: [...persistedOutputKeys] } : {}),
  }
}
