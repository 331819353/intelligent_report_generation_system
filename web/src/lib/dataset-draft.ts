import {
  datasetAPI,
  type AssetTable,
  type CalculatedField,
  type DatasetDraft,
  type DatasetRecord,
  type DatasetSummary,
  type DesignerNode,
  type FieldOption,
  type FilterOption,
  type JoinOption,
  type ParameterOption,
  type PublishedVersionRecord,
} from './datasets'
import { expandSystemDWDDesignerGraph, hydrateDesignerGraph } from './dataset-graph'

const text = (value: unknown) => typeof value === 'string' ? value : ''
const object = (value: unknown) => (value && typeof value === 'object' ? value as Record<string, unknown> : {})
const list = (value: unknown) => Array.isArray(value) ? value : []

const expressionFieldReference = (value: unknown): { nodeId: string; field: string } | null => {
  if (!value || typeof value !== 'object') return null
  const expression = object(value)
  if (text(expression.type) === 'FIELD_REF' && text(expression.nodeId) && text(expression.field)) {
    return { nodeId: text(expression.nodeId), field: text(expression.field) }
  }
  for (const child of [expression.argument, expression.left, expression.right, expression.lower, expression.upper, expression.else]) {
    const reference = expressionFieldReference(child)
    if (reference) return reference
  }
  for (const child of list(expression.arguments)) {
    const reference = expressionFieldReference(child)
    if (reference) return reference
  }
  return null
}

type ResolvedDatasetVersion = {
  dataset: DatasetSummary
  version: PublishedVersionRecord
}

async function resolveDatasetVersions(versionIDs: string[], datasets: DatasetSummary[]): Promise<Map<string, ResolvedDatasetVersion>> {
  const targets = new Set(versionIDs.filter(Boolean))
  const resolved = new Map<string, ResolvedDatasetVersion>()
  const direct = datasets.filter(dataset => dataset.currentPublishedVersionId && targets.has(dataset.currentPublishedVersionId))
  await Promise.all(direct.map(async dataset => {
    const versionID = dataset.currentPublishedVersionId!
    resolved.set(versionID, { dataset, version: await datasetAPI.getVersion(dataset.id, versionID) })
  }))
  const unresolved = () => [...targets].filter(versionID => !resolved.has(versionID))
  if (!unresolved().length) return resolved

  // DWD/DWS 必须固定精确上游版本。当前发布指针已经推进时，按可见数据集的发布
  // 历史定位旧版本，避免把草稿悄悄切换到新的上游版本。
  const history = await Promise.all(datasets.map(async dataset => ({
    dataset,
    versions: (await datasetAPI.listVersions(dataset.id, 200, 0)).items,
  })))
  const ownerByVersion = new Map(history.flatMap(item => item.versions.map(version => [version.id, item.dataset] as const)))
  await Promise.all(unresolved().map(async versionID => {
    const dataset = ownerByVersion.get(versionID)
    if (!dataset) return
    resolved.set(versionID, { dataset, version: await datasetAPI.getVersion(dataset.id, versionID) })
  }))
  return resolved
}

function datasetVersionNode(
  value: Record<string, unknown>,
  index: number,
  source: ResolvedDatasetVersion,
): DesignerNode {
  const versionID = text(value.datasetVersionId)
  const tableID = `dataset-version:${versionID}`
  const datasetMeta = object(source.version.dsl.dataset)
  const columns = list(source.version.dsl.fields).map(object).flatMap((field, fieldIndex) => {
    const code = text(field.code)
    if (!code) return []
    return [{
      id: `${versionID}:${text(field.id) || code}`,
      tableId: tableID,
      columnName: code,
      businessName: text(field.name) || code,
      businessDescription: text(field.description),
      canonicalType: text(field.canonicalType) || 'STRING',
      nullable: field.nullable === true,
      semanticType: text(field.semanticType),
      assetStatus: 'ACTIVE',
      ordinalPosition: fieldIndex + 1,
    }]
  })
  const projection = new Set(list(value.projection).map(text).filter(Boolean))
  const selected = projection.size
    ? columns.filter(column => projection.has(column.columnName)).map(column => column.columnName)
    : columns.map(column => column.columnName)
  return {
    id: text(value.id) || `node_${index + 1}`,
    alias: text(value.alias),
    table: {
      id: tableID,
      dataSourceId: `dataset-layer:${source.dataset.layer}`,
      dataSourceName: `${source.dataset.layer} 数据集`,
      dataSourceType: 'DATASET',
      tableName: text(datasetMeta.code) || source.dataset.code,
      schemaName: source.dataset.layer,
      businessName: text(datasetMeta.name) || source.dataset.name,
      businessDescription: text(datasetMeta.description) || source.dataset.description,
      columnCount: columns.length,
      sourceKind: 'DATASET',
      datasetId: source.dataset.id,
      datasetVersionId: versionID,
      datasetLayer: source.dataset.layer,
    },
    columns,
    selected,
  }
}

/**
 * 把服务端规范 DSL 还原为两个设计入口共用的画板状态。
 * 物理列始终重新读取资产中心，避免继续编辑时使用已经失效的字段快照。
 */
export async function hydrateDatasetDraft(record: DatasetRecord, tables: AssetTable[], datasets: DatasetSummary[] = []): Promise<DatasetDraft> {
  const dsl = record.dsl
  const systemGeneratedDWD = record.layer === 'DWD' && record.code.startsWith('dwd_auto_')
  const nodeValues = list(dsl.nodes).map(object)
  const datasetVersionIDs = nodeValues
    .filter(value => text(value.type).toUpperCase() === 'DATASET' || Boolean(text(value.datasetVersionId)))
    .map(value => text(value.datasetVersionId))
    .filter(Boolean)
  const datasetVersions = datasetVersionIDs.length ? await resolveDatasetVersions(datasetVersionIDs, datasets) : new Map<string, ResolvedDatasetVersion>()
  const hydratedNodes = await Promise.all(nodeValues.map(async (value, index): Promise<DesignerNode> => {
    const datasetVersionID = text(value.datasetVersionId)
    if (text(value.type).toUpperCase() === 'DATASET' || datasetVersionID) {
      const source = datasetVersions.get(datasetVersionID)
      if (!source) throw new Error(`第 ${index + 1} 个节点引用的上游数据集版本已不可用`)
      return datasetVersionNode(value, index, source)
    }
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
  // 兼容修复上线前已经生成的 fact / dim_N 草稿。系统 DWD 的节点数组按
  // LLM 计划的嵌套输入顺序保存：最内层事实输入为 t1，随后每个扩维输入依次
  // 为 t2、t3……；重新保存后该规范别名会回写 DSL。
  const nodes = systemGeneratedDWD
    ? hydratedNodes.map((node, index) => ({ ...node, alias: `t${index + 1}` }))
    : hydratedNodes
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
    } else if (['TRIM', 'COALESCE', 'CAST'].includes(text(expression.type))) {
      const source = expressionFieldReference(expression)
      if (source) {
        fields.push({
          key: `${source.nodeId}.${source.field}`,
          role: text(raw.role),
          aggregation: '',
          code: text(raw.code),
          name: text(raw.name),
          description: text(raw.description),
          canonicalType: text(raw.canonicalType),
          semanticType: text(raw.semanticType),
          nullable: raw.nullable === true,
          persistedExpression: expression,
          output: true,
          finalOutput: true,
        })
      }
    } else {
      const argumentsValue = list(expression.arguments).map(object)
      calculations.push({ id: text(raw.id), code: text(raw.code), name: text(raw.name), operation: text(expression.type), leftKey: `${text(argumentsValue[0]?.nodeId)}.${text(argumentsValue[0]?.field)}`, rightKey: `${text(argumentsValue[1]?.nodeId)}.${text(argumentsValue[1]?.field)}`, canonicalType: text(raw.canonicalType), aggregation: '' })
    }
  }
  const persistedOutputKeys = new Set(fields.map(field => field.key))
  const joins: JoinOption[] = list(dsl.joins).map(object).map(raw => {
    const conditions = list(raw.conditions).map(object).map((condition, index) => ({ id: `${text(raw.id)}_condition_${index + 1}`, leftField: text(object(condition.leftExpression).field), rightField: text(object(condition.rightExpression).field) }))
    const first = conditions[0] ?? { leftField: '', rightField: '' }
    const generatedJoinReady = systemGeneratedDWD && conditions.length > 0 && conditions.every(condition => condition.leftField && condition.rightField)
    return { id: text(raw.id), leftNodeId: text(raw.leftNodeId), rightNodeId: text(raw.rightNodeId), leftField: first.leftField, rightField: first.rightField, joinType: text(raw.joinType), cardinality: text(raw.cardinality) || 'UNKNOWN', manualConfirmed: generatedJoinReady || Boolean(raw.manualConfirmed), conditions }
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
  const hydratedDesigner = hydrateDesignerGraph(dsl, nodes, joins, graphFields)
  const designer = systemGeneratedDWD && !hasFixedDesigner
    ? expandSystemDWDDesignerGraph(hydratedDesigner, nodes, graphFields)
    : hydratedDesigner
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
