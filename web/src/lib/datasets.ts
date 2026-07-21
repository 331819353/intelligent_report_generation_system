import { apiRequest } from './api'
import { graphContains, graphLeaves, graphProducedFields, serializeDesignerGraph, type DesignerGraphV1, type GraphInput } from './dataset-graph'
export type { CanvasPoint as GraphPosition, DesignerGraphV1, GraphDimension, GraphEnd, GraphEndOutput, GraphGroup, GraphInput, GraphJoin, GraphMetric } from './dataset-graph'

export type AssetTable = {
  id: string; dataSourceId: string; dataSourceName: string; dataSourceType: string
  tableName: string; schemaName: string; businessName: string; businessDescription?: string; columnCount: number; fileVersionId?: string
  managementStatus?: string; enrichmentStatus?: string
}
export type AssetColumn = {
  id: string; tableId: string; columnName: string; businessName: string
  canonicalType: string; nullable: boolean; semanticType: string; assetStatus?: string
}
export type DesignerNode = { id: string; alias: string; table: AssetTable; columns: AssetColumn[]; selected: string[]; groupingEnabled?: boolean }
export type FieldOption = {
  key: string; role: string; aggregation: string; code?: string; name?: string
  groupBy?: boolean; grouping?: string; output?: boolean; metric?: boolean
  finalOutput?: boolean; finalGroupBy?: boolean; finalGrouping?: string; finalMetric?: boolean; finalAggregation?: string
}
export type JoinConditionOption = { id: string; leftField: string; rightField: string }
export type JoinOption = { id: string; leftNodeId: string; rightNodeId: string; leftField: string; rightField: string; joinType: string; cardinality: string; manualConfirmed: boolean; conditions?: JoinConditionOption[] }
export type FilterOption = { id: string; nodeId: string; field: string; operator: string; value: string; parameterCode: string }
export type ParameterOption = { code: string; name: string; dataType: string; required: boolean; multiValue: boolean }
export type CalculatedField = { id: string; code: string; name: string; operation: string; leftKey: string; rightKey: string; canonicalType: string; aggregation?: string }
export type SortOption = { fieldId: string; direction: string }
export type PreAggregationDraft = { id: string; nodeId: string; joinId: string; joinSide: 'LEFT' | 'RIGHT' }
export type DatasetDraft = {
  code: string; name: string; description: string; nodes: DesignerNode[]; fields: FieldOption[]
  joins: JoinOption[]; filters: FilterOption[]; parameters: ParameterOption[]; calculations: CalculatedField[]
  sorts: SortOption[]; grainDescription: string; grainKeys: string[]; groupingEnabled?: boolean
  finalConfigured?: boolean; finalGroupingEnabled?: boolean
  preAggregation?: PreAggregationDraft; preAggregations?: PreAggregationDraft[]; finalOutputKeys?: string[]
  designer?: DesignerGraphV1
}
export type DatasetRecord = {
  id: string; code: string; name: string; description: string; type: string; status: string
  originTableId?: string
  version: number; draftVersionId: string; draftVersionNo: number; draftRecordVersion: number; currentPublishedVersionId?: string
  dslHash: string; planHash: string; dsl: DatasetDSL; logicalPlan: unknown
  createdAt: string; updatedAt: string
}
export type DatasetSummary = {
  id: string; code: string; name: string; description: string; type: string; status: string
  originTableId?: string
  version: number; dslHash: string; currentPublishedVersionId?: string; updatedAt: string
}
export type DatasetPage = {
  items: DatasetSummary[]; total: number; limit: number; offset: number
}
export type DatasetRevisionSummary = {
  id: string; datasetId: string; versionNo: number; operationType: 'CREATE' | 'SAVE' | 'ROLLBACK'
  sourceRevisionId?: string; name: string; description: string; type: string
  draftVersionId: string; draftRecordVersion: number; dslVersion: string; dslHash: string; planHash: string
  createdAt: string; createdBy: string
}
export type DatasetRevisionRecord = DatasetRevisionSummary & {
  dsl: DatasetDSL; logicalPlan: unknown
}
export type DatasetRevisionPage = {
  items: DatasetRevisionSummary[]; total: number; limit: number; offset: number
}
export type PublishedVersionRecord = {
  id: string; datasetId: string; versionNo: number; status: 'PUBLISHED' | 'STALE' | 'DEPRECATED'
  dslVersion: string; dslHash: string; planHash: string; dsl: DatasetDSL; logicalPlan: unknown
  publishedAt: string; publishedBy: string
  datasetRecordVersion: number; draftVersionId: string; draftRecordVersion: number
}
export type PublishedVersionSummary = Pick<PublishedVersionRecord,
  'id' | 'datasetId' | 'versionNo' | 'status' | 'dslVersion' | 'dslHash' | 'planHash' |
  'draftRecordVersion' | 'publishedAt' | 'publishedBy'>
export type PublishedVersionPage = {
  items: PublishedVersionSummary[]; total: number; limit: number; offset: number
}
export type VersionUsage = {
  reportDraftReferences: number
  downstreamDraftReferences: number
  downstreamPublishedReferences: number
  activeQueryRuns: number
}
export type VersionTransitionInput = {
  expectedVersion: number
  expectedStatus: PublishedVersionRecord['status']
  targetStatus: 'STALE' | 'DEPRECATED'
}
export type DatasetPermissionAction = 'READ' | 'MANAGE' | 'PUBLISH'
export type PublishDatasetInput = {
  draftVersionId: string
  expectedVersion: number
  expectedDraftRecordVersion: number
  expectedDslHash: string
  validationParameters: Record<string, unknown>
}
export type DatasetPublicationRequestStatus = 'PENDING' | 'APPROVED' | 'REJECTED'
export type DatasetPublicationRequest = {
  id: string; datasetId: string; status: DatasetPublicationRequestStatus; version: number
  draftVersionId: string; expectedDatasetVersion: number; expectedDraftRecordVersion: number
  expectedDslHash: string; expectedPlanHash: string; requesterId: string; requestNote: string
  reviewerId?: string; reviewNote?: string; publishedVersionId?: string
  submittedAt: string; reviewedAt?: string; updatedAt: string
}
export type DatasetPublicationRequestPage = {
  items: DatasetPublicationRequest[]; total: number; limit: number; offset: number
}
export type DatasetPublicationApprovalResult = {
  request: DatasetPublicationRequest
  publishedVersion: PublishedVersionRecord
}
export type DatasetPreview = {
  queryId: string; columns: string[]; rows: unknown[][]; rowCount: number; durationMs: number
  warnings?: Array<{ code: string; message: string; joinId?: string; estimatedRows?: number }>
}
export type DatasetDraftPreview = DatasetPreview & {
  dslHash: string; planHash: string; baseVersion: number
}
export type DatasetCandidatePreview = DatasetPreview & { dslHash: string; planHash: string }
export type AssetTablePreview = { columns: string[]; rows: unknown[][] }
export type DatasetDSL = Record<string, unknown> & {
  dslVersion: string; dataset: { code: string; name: string; description?: string; type: string }
  nodes: Array<Record<string, unknown>>; fields: Array<Record<string, unknown>>
  designer?: DesignerGraphV1
}

/** 将资产中心类型收敛为 DSL V1 的规范类型。 */
export function canonicalType(value: string): string {
  const type = value.toUpperCase()
  if (type === 'NUMBER' || type === 'INT' || type === 'INTEGER') return 'INTEGER'
  if (type === 'DECIMAL' || type === 'FLOAT' || type === 'DOUBLE') return 'DECIMAL'
  if (type === 'DATE') return 'DATE'
  if (type === 'DATETIME' || type === 'TIMESTAMP') return 'DATETIME'
  if (type === 'BOOLEAN') return 'BOOLEAN'
  return 'STRING'
}

const identifier = (value: string) => {
  // 生成符合服务端格式的可编辑候选值；最终合法性仍由服务端严格校验，前端不构成
  // 安全边界。清理后为空时使用稳定兜底，避免产生空字段编码。
  const cleaned = value.trim().replace(/[^A-Za-z0-9_]/g, '_').replace(/^[^A-Za-z]+/, '')
  return cleaned || 'field'
}
const fieldID = (node: DesignerNode, column: AssetColumn) => `field_${identifier(node.alias)}_${identifier(column.columnName)}`
const fieldCode = (node: DesignerNode, column: AssetColumn, multiple: boolean) => identifier(multiple ? `${node.alias}_${column.columnName}` : column.columnName)

const graphItemKey = (value: unknown): string => {
  if (typeof value === 'string') return value
  if (!value || typeof value !== 'object') return ''
  const item = value as Record<string, unknown>
  return [item.key, item.sourceKey, item.fieldKey, item.field].find(candidate => typeof candidate === 'string') as string ?? ''
}

const graphItemText = (value: unknown, ...keys: string[]): string => {
  if (!value || typeof value !== 'object') return ''
  const item = value as Record<string, unknown>
  const candidate = keys.map(key => item[key]).find(entry => typeof entry === 'string')
  return typeof candidate === 'string' ? candidate.trim() : ''
}

/**
 * Designer V1 是画布的持久化真值。执行 DSL 仍只使用结构化 FIELD_REF、Join 与
 * Aggregate：坐标和展示名不会被拼接到查询文本中。
 */
function buildDesignerDatasetDSL(draft: DatasetDraft, designer: DesignerGraphV1): DatasetDSL {
  if (designer.version !== '1.0') throw new Error('当前版本无法读取该画布配置')
  const selected = draft.nodes.flatMap(node => node.columns
    .filter(column => node.selected.includes(column.columnName))
    .map(column => ({ node, column, key: `${node.id}.${column.columnName}` })))
  if (!selected.length) throw new Error('请至少选择一个源字段')

  const selectedByKey = new Map(selected.map(item => [item.key, item]))
  const options = new Map(draft.fields.map(item => [item.key, item]))
  const endOutputs = (Array.isArray(designer.end?.outputs) ? designer.end.outputs : [])
    .map(output => ({ value: output, key: graphItemKey(output) }))
    .filter(output => output.key)
  if (!endOutputs.length) throw new Error('请在结束节点至少选择一个输出字段')
  if (new Set(endOutputs.map(output => output.key)).size !== endOutputs.length) throw new Error('结束节点不能重复选择同一个输出字段')

  const groups = Array.isArray(designer.groups) ? designer.groups : []
  const endInput = designer.end?.input
  const globalGroup = (() => {
    let input = endInput
    const visited = new Set<string>()
    while (input?.kind === 'TRANSFORM' && !visited.has(input.id)) {
      visited.add(input.id)
      input = designer.transforms?.find(transform => transform.id === input!.id)?.input
    }
    return input?.kind === 'GROUP' ? groups.find(group => group.id === input.id) : undefined
  })()
  const globalDimensions = new Map((globalGroup?.dimensions ?? []).map(item => [graphItemKey(item), item]))
  const globalMetrics = new Map((globalGroup?.metrics ?? []).map(item => [graphItemKey(item), item]))
  if (globalGroup && (!globalDimensions.size || !globalMetrics.size)) throw new Error('结束节点前的分组组件需要同时配置分组字段和指标字段')
  const preAggregatedGroupIDs = new Set<string>()
  const preAggregations = groups.flatMap(group => {
    if (globalGroup?.id === group.id || !group.input) return []
    const consumer = designer.joins.find(join => join.left?.kind === 'GROUP' && join.left.id === group.id || join.right?.kind === 'GROUP' && join.right.id === group.id)
    if (!consumer) return []
    const containsJoin = designer.joins.some(item => graphContains(group.input!, { kind: 'JOIN', id: item.id }, designer))
    const containsGroup = designer.groups.some(item => item.id !== group.id && graphContains(group.input!, { kind: 'GROUP', id: item.id }, designer))
    const leaves = graphLeaves(group.input, designer)
    if (containsJoin || containsGroup || leaves.length !== 1) return []
    const nodeId = leaves[0]
    const upstream = new Map(graphProducedFields(group.input, designer, draft.nodes, draft.fields).map(item => [item.key, item]))
    const outputAlias = (key: string, fallback: string) => identifier(key.includes('.') ? key.slice(key.indexOf('.') + 1) : fallback)
    const sourceExpression = (key: string, source: ReturnType<typeof graphProducedFields>[number]) => {
      const plainPhysical = key === `${nodeId}.${source.binding.field}` && source.expression?.type === 'FIELD_REF' && source.expression.nodeId === nodeId && source.expression.field === source.binding.field
      return plainPhysical ? {} : { expression: source.expression ?? { type: 'FIELD_REF', nodeId, field: source.binding.field } }
    }
    const dimensions = (group.dimensions ?? []).flatMap(item => {
      const key = graphItemKey(item), source = upstream.get(key)
      return key && source ? [{
        field: outputAlias(key, source.binding.field),
        ...(graphItemText(item, 'unit', 'grouping') ? { unit: graphItemText(item, 'unit', 'grouping') } : {}),
        ...sourceExpression(key, source),
      }] : []
    })
    const metrics = (group.metrics ?? []).flatMap(item => {
      const key = graphItemKey(item), source = upstream.get(key), fn = graphItemText(item, 'function', 'aggregation')
      return key && source && fn ? [{ field: outputAlias(key, source.binding.field), function: fn, ...sourceExpression(key, source) }] : []
    })
    if (!dimensions.length || !metrics.length) throw new Error(`请完成分组组件“${group.name || group.id}”的分组字段和指标配置`)
    const aliases = [...dimensions, ...metrics].map(item => item.field)
    if (new Set(aliases).size !== aliases.length) throw new Error(`分组组件“${group.name || group.id}”的输出字段编码重复，请调整字段处理产物编码`)
    preAggregatedGroupIDs.add(group.id)
    return [{
      id: group.id, nodeId,
      joinId: consumer.id,
      joinSide: consumer.left?.kind === 'GROUP' && consumer.left.id === group.id ? 'LEFT' : 'RIGHT',
      groupBy: dimensions,
      metrics,
    }]
  })

  // 非根分组必须明确连接到 Join；静默忽略会造成画布显示有分组、实际查询却读取
  // 明细数据，因此保存时失败并指出拓扑问题。
  for (const group of groups) {
    if (globalGroup?.id === group.id) continue
    if (!preAggregations.some(item => item.id === group.id)) throw new Error(`分组组件“${group.name || group.id}”需要连接到关联节点或结束节点`)
  }

  const groupProduced = globalGroup ? graphProducedFields({ kind: 'GROUP', id: globalGroup.id }, designer, draft.nodes, draft.fields, new Set(), globalGroup.id, preAggregatedGroupIDs) : []
  const producedByKey = new Map([
    ...groupProduced,
    ...graphProducedFields(endInput, designer, draft.nodes, draft.fields, new Set(), globalGroup?.id ?? '', preAggregatedGroupIDs),
  ].map(item => [item.key, item]))

  // 结束节点控制“对外可见字段”，不能反向改变上游分组口径。未勾选的根分组
  // 维度仍以 invisible 字段进入 DSL/groupBy，确保指标粒度与画布配置一致。
  const visibleKeys = new Set(endOutputs.map(output => output.key))
  const visibleCodes = new Set(endOutputs.map(output => {
    const produced = producedByKey.get(output.key)
    return identifier(graphItemText(output.value, 'code') || produced?.code || output.key)
  }))
  const outputSpecs = [
    ...endOutputs.map(output => ({ ...output, visible: true })),
    ...(globalGroup?.dimensions ?? []).flatMap(value => {
      const key = graphItemKey(value)
      return key && !visibleKeys.has(key) ? [{ value, key, visible: false }] : []
    }),
  ]
  const fields = outputSpecs.map(({ value, key, visible }) => {
    const produced = producedByKey.get(key)
    if (!produced) throw new Error(`结束节点输出字段 ${key} 不属于上游组件产物`)
    const sourceBinding = produced.sourceBinding ?? produced.binding
    const sourceValue = selectedByKey.get(key) ?? selectedByKey.get(`${sourceBinding.nodeId}.${sourceBinding.field}`)
    if (!sourceValue) throw new Error(`结束节点输出字段 ${key} 已不可用`)
    const { node, column } = sourceValue
    const option = options.get(key)
    const dimension = globalDimensions.get(key)
    const metric = globalMetrics.get(key)
    const source = produced.expression ?? { type: 'FIELD_REF', nodeId: node.id, field: column.columnName }
    const expression = source
    const preferredCode = identifier(graphItemText(value, 'code') || graphItemText(metric ?? dimension, 'code') || option?.code || fieldCode(node, column, draft.nodes.length > 1))
    const configuredCode = !visible && visibleCodes.has(preferredCode) && globalGroup
      ? identifier(`group_${globalGroup.id}_${preferredCode}`)
      : preferredCode
    const configuredName = graphItemText(value, 'name') || graphItemText(metric ?? dimension, 'name') || option?.name || column.businessName || column.columnName
    return {
      id: selectedByKey.has(key) ? fieldID(node, column) : `field_${identifier(key)}`,
      code: identifier(configuredCode),
      name: configuredName,
      role: produced.kind === 'METRIC' ? 'MEASURE' : graphItemText(value, 'role') || (dimension || produced.kind === 'DIMENSION' ? 'DIMENSION' : option?.role || 'ATTRIBUTE'),
      expression,
      canonicalType: produced.aggregation === 'COUNT' || produced.aggregation === 'COUNT_DISTINCT' ? 'INTEGER' : canonicalType(produced.canonicalType || column.canonicalType),
      nullable: selectedByKey.has(key) ? column.nullable : true,
      visible,
    }
  })
  if (new Set(fields.map(field => field.code)).size !== fields.length) throw new Error('最终输出与分组维度的字段编码不能重复')
  const fieldIDs = new Map(fields.map(field => [field.code, field.id]))
  // 所有根分组维度（包含结束节点隐藏的维度）共同决定实际聚合粒度。
  const finalGroupBy = globalGroup
    ? outputSpecs.flatMap((output, index) => globalDimensions.has(output.key) ? [fields[index].id] : [])
    : []

  const parameterCodes = new Set(draft.parameters.map(item => item.code))
  const filters = draft.filters.map(item => {
    const right = item.parameterCode
      ? (parameterCodes.has(item.parameterCode) ? { type: 'PARAM_REF', code: item.parameterCode } : null)
      : { type: 'LITERAL', value: item.value }
    if (!right) throw new Error(`过滤条件引用了不存在的参数 ${item.parameterCode}`)
    return { id: item.id, stage: 'PRE_AGGREGATION', optional: Boolean(item.parameterCode), expression: { type: item.operator, left: { type: 'FIELD_REF', nodeId: item.nodeId, field: item.field }, right } }
  })
  const requestedGrainKeys = globalGroup
    ? finalGroupBy.map(id => fields.find(field => field.id === id)!.code)
    : draft.grainKeys.filter(code => fieldIDs.has(code))
  const grainKeys = requestedGrainKeys.length ? requestedGrainKeys : (finalGroupBy.length
    ? finalGroupBy.map(id => fields.find(field => field.id === id)!.code)
    : [fields[0].code])
  const grainDescription = draft.grainDescription.trim() || '每行代表一条结束节点输出记录'
  const sourceCount = new Set(draft.nodes.map(node => node.table.dataSourceId)).size
  return {
    dslVersion: '1.0',
    dataset: { code: identifier(draft.code), name: draft.name.trim(), description: draft.description.trim(), type: sourceCount > 1 ? 'CROSS_SOURCE' : 'SINGLE_SOURCE' },
    nodes: draft.nodes.map(node => ({ id: node.id, type: 'TABLE', datasourceId: node.table.dataSourceId, tableId: node.table.id, ...(node.table.fileVersionId ? { fileVersionId: node.table.fileVersionId } : {}), alias: identifier(node.alias), projection: [...node.selected], sourceFilters: [] })),
    joins: draft.joins.map(join => ({ id: join.id, leftNodeId: join.leftNodeId, rightNodeId: join.rightNodeId, joinType: join.joinType, cardinality: 'UNKNOWN', manualConfirmed: join.manualConfirmed, conditions: (join.conditions && join.conditions.length > 1 ? join.conditions : [{ id: `${join.id}_condition_1`, leftField: join.leftField, rightField: join.rightField }]).map(condition => ({ leftExpression: { type: 'FIELD_REF', nodeId: join.leftNodeId, field: condition.leftField }, operator: 'EQUALS', rightExpression: { type: 'FIELD_REF', nodeId: join.rightNodeId, field: condition.rightField } })) })),
    preAggregations,
    fields,
    filters,
    groupBy: finalGroupBy,
    having: [],
    sorts: draft.sorts.flatMap(item => fieldIDs.get(item.fieldId) ? [{ fieldId: fieldIDs.get(item.fieldId)!, direction: item.direction }] : []),
    parameters: draft.parameters.map(item => ({ ...item, code: identifier(item.code) })),
    outputGrain: { description: grainDescription, keyFields: grainKeys },
    executionPolicy: { mode: 'REALTIME', timeoutMs: 5000, previewLimit: 200, resultLimit: 10000, cacheTtlSeconds: 300, materialization: { enabled: false } },
    designer: serializeDesignerGraph(designer),
  }
}

/** 从设计器状态生成可由服务端严格校验的 DSL，不生成或保存 SQL。 */
export function buildDatasetDSL(draft: DatasetDraft): DatasetDSL {
  if (!draft.code.trim() || !draft.name.trim()) throw new Error('请填写数据集编码和名称')
  if (!draft.nodes.length) throw new Error('请至少选择一张表')
  if (draft.designer) return buildDesignerDatasetDSL(draft, draft.designer)
  // selected 是字段、计算、分组和粒度映射的唯一物理列来源；未勾选列不会被
  // 意外写入 projection 或表达式树。
  const selected = draft.nodes.flatMap(node => node.columns.filter(column => node.selected.includes(column.columnName)).map(column => ({ node, column })))
  if (!selected.length) throw new Error('请至少选择一个源字段')
  const options = new Map(draft.fields.map(item => [item.key, item]))
  // 汇总模式把“参与读取”和“最终输出”分开：Join 或计算可以继续引用源字段，只有
  // 用户明确选中的分组维度和聚合指标才进入 fields。
  const hasNodeModes = draft.nodes.some(node => node.groupingEnabled !== undefined)
  const hasGrouping = hasNodeModes ? draft.nodes.some(node => node.groupingEnabled) : draft.groupingEnabled === true
  const useFinalConfiguration = draft.finalConfigured === true
  const preAggregation = draft.preAggregation
  const finalOutputKeys = new Set(draft.finalOutputKeys ?? [])
  const effectiveOption = (option: FieldOption | undefined) => ({
    output: useFinalConfiguration ? option?.finalOutput !== false : option?.output !== false,
    groupBy: useFinalConfiguration ? Boolean(option?.finalGroupBy) : Boolean(option?.groupBy),
    grouping: useFinalConfiguration ? option?.finalGrouping ?? '' : option?.grouping ?? '',
    metric: useFinalConfiguration ? Boolean(option?.finalMetric) : Boolean(option?.metric),
    aggregation: useFinalConfiguration ? option?.finalAggregation ?? '' : option?.aggregation ?? '',
  })
  const outputSelected = selected.filter(({ node, column }) => {
    const option = options.get(`${node.id}.${column.columnName}`)
    const effective = effectiveOption(option)
    if (preAggregation && finalOutputKeys.size) return finalOutputKeys.has(`${node.id}.${column.columnName}`)
    if (useFinalConfiguration) return draft.finalGroupingEnabled ? effective.groupBy || effective.metric : effective.output
    if (hasNodeModes) return node.groupingEnabled ? Boolean(option?.groupBy || option?.aggregation) : option?.output !== false
    return draft.groupingEnabled ? Boolean(option?.groupBy || option?.aggregation) : option?.output !== false
  })
  if (!outputSelected.length && !draft.calculations.length) throw new Error('请至少选择一个输出字段')
  // 聚合设置会把源字段包装成 AGGREGATE 表达式并强制为 MEASURE；非聚合字段
  // 则保留设计器中的语义角色。
  const baseFields = outputSelected.map(({ node, column }) => {
    const option: FieldOption = options.get(`${node.id}.${column.columnName}`) ?? { key: `${node.id}.${column.columnName}`, role: 'ATTRIBUTE', aggregation: '' }
    const effective = effectiveOption(option)
    const source = { type: 'FIELD_REF', nodeId: node.id, field: column.columnName }
    // 关联前分组已经在节点派生结果中完成；最终字段继续以 FIELD_REF 引用该结果，
    // 避免保存后被解释成 Join 完成后的全局二次聚合。
    const expression = preAggregation
      ? source
      : effective.aggregation
      ? { type: 'AGGREGATE', function: effective.aggregation, argument: source }
      : effective.grouping ? { type: 'DATE_TRUNC', unit: effective.grouping, argument: source } : source
    return {
      id: fieldID(node, column), code: identifier(option.code || fieldCode(node, column, draft.nodes.length > 1)),
      name: option.name?.trim() || column.businessName || column.columnName, role: effective.aggregation ? 'MEASURE' : effective.groupBy ? 'DIMENSION' : option.role,
      expression,
      canonicalType: canonicalType(column.canonicalType), nullable: column.nullable, visible: true,
    }
  })
  const sourceByKey = new Map(selected.map(({ node, column }) => [`${node.id}.${column.columnName}`, { type: 'FIELD_REF', nodeId: node.id, field: column.columnName }]))
  const calculated = draft.calculations.map(item => {
    const left = sourceByKey.get(item.leftKey), right = sourceByKey.get(item.rightKey)
    if (!left || !right) throw new Error(`计算字段 ${item.name || item.code} 引用了未选择字段`)
    const expression = { type: item.operation, arguments: [left, right] }
    return { id: item.id, code: identifier(item.code), name: item.name, role: 'MEASURE', expression: item.aggregation ? { type: 'AGGREGATE', function: item.aggregation, argument: expression } : expression, canonicalType: item.canonicalType, nullable: true, visible: true }
  })
  const fields = [...baseFields, ...calculated]
  const fieldIDs = new Map(fields.map(field => [field.code, field.id]))
  const parameterCodes = new Set(draft.parameters.map(item => item.code))
  // 参数过滤标记为 optional：未提供参数时服务端跳过整个表达式；固定值过滤始终
  // 执行。这里仅构造表达式树，从不生成或拼接 SQL。
  const filters = draft.filters.map(item => {
    const right = item.parameterCode
      ? (parameterCodes.has(item.parameterCode) ? { type: 'PARAM_REF', code: item.parameterCode } : null)
      : { type: 'LITERAL', value: item.value }
    if (!right) throw new Error(`过滤条件引用了不存在的参数 ${item.parameterCode}`)
    return { id: item.id, stage: 'PRE_AGGREGATION', optional: Boolean(item.parameterCode), expression: { type: item.operator, left: { type: 'FIELD_REF', nodeId: item.nodeId, field: item.field }, right } }
  })
  // 新汇总流程只采用用户显式多选的分组字段；旧草稿没有 groupingEnabled 标记时
  // 保留原有自动分组行为，确保历史 DSL 可继续编辑和保存。
  const groupBy = preAggregation ? [] : useFinalConfiguration
    ? draft.finalGroupingEnabled
      ? outputSelected.flatMap(({ node, column }, index) => effectiveOption(options.get(`${node.id}.${column.columnName}`)).groupBy ? [baseFields[index].id] : [])
      : []
    : hasNodeModes
    ? hasGrouping ? outputSelected.flatMap(({ node, column }, index) => {
      const option = options.get(`${node.id}.${column.columnName}`)
      return option?.groupBy || (!node.groupingEnabled && !option?.aggregation) ? [baseFields[index].id] : []
    }) : []
    : draft.groupingEnabled === true
      ? outputSelected.flatMap(({ node, column }, index) => options.get(`${node.id}.${column.columnName}`)?.groupBy ? [baseFields[index].id] : [])
      : draft.groupingEnabled === false ? [] : baseFields.filter(field => field.role !== 'MEASURE').map(field => field.id)
  const grainKeys = draft.grainKeys.filter(code => fieldIDs.has(code))
  if (!draft.grainDescription.trim() || !grainKeys.length) throw new Error('请填写输出粒度并选择至少一个粒度键')
  const sourceCount = new Set(draft.nodes.map(node => node.table.dataSourceId)).size
  return {
    dslVersion: '1.0',
    dataset: { code: identifier(draft.code), name: draft.name.trim(), description: draft.description.trim(), type: sourceCount > 1 ? 'CROSS_SOURCE' : 'SINGLE_SOURCE' },
    nodes: draft.nodes.map(node => ({ id: node.id, type: 'TABLE', datasourceId: node.table.dataSourceId, tableId: node.table.id, ...(node.table.fileVersionId ? { fileVersionId: node.table.fileVersionId } : {}), alias: identifier(node.alias), projection: [...node.selected], sourceFilters: [] })),
    joins: draft.joins.map(join => ({ id: join.id, leftNodeId: join.leftNodeId, rightNodeId: join.rightNodeId, joinType: join.joinType, cardinality: 'UNKNOWN', manualConfirmed: join.manualConfirmed, conditions: (join.conditions && join.conditions.length > 1 ? join.conditions : [{ id: `${join.id}_condition_1`, leftField: join.leftField, rightField: join.rightField }]).map(condition => ({ leftExpression: { type: 'FIELD_REF', nodeId: join.leftNodeId, field: condition.leftField }, operator: 'EQUALS', rightExpression: { type: 'FIELD_REF', nodeId: join.rightNodeId, field: condition.rightField } })) })),
    preAggregations: preAggregation ? [{
      id: preAggregation.id,
      nodeId: preAggregation.nodeId,
      joinId: preAggregation.joinId,
      joinSide: preAggregation.joinSide,
      groupBy: draft.fields.filter(field => field.key.startsWith(`${preAggregation.nodeId}.`) && field.finalGroupBy).map(field => ({ field: field.key.slice(preAggregation.nodeId.length + 1), ...(field.finalGrouping ? { unit: field.finalGrouping } : {}) })),
      metrics: draft.fields.filter(field => field.key.startsWith(`${preAggregation.nodeId}.`) && field.finalMetric && field.finalAggregation).map(field => ({ field: field.key.slice(preAggregation.nodeId.length + 1), function: field.finalAggregation })),
    }] : [],
    fields, filters, groupBy, having: [],
    sorts: draft.sorts.filter(item => item.fieldId).map(item => ({ fieldId: fieldIDs.get(item.fieldId) ?? item.fieldId, direction: item.direction })),
    parameters: draft.parameters.map(item => ({ ...item, code: identifier(item.code) })),
    outputGrain: { description: draft.grainDescription.trim(), keyFields: grainKeys },
    executionPolicy: { mode: 'REALTIME', timeoutMs: 5000, previewLimit: 200, resultLimit: 10000, cacheTtlSeconds: 300, materialization: { enabled: false } },
  }
}

/**
 * 只物化目标组件及其上游子图，并临时把目标连接到预览结束节点。这样每个组件
 * 都按“输入数据集 -> 组件处理 -> 输出数据集”的同一合同执行，且不会把下游未
 * 完成的配置混入当前组件预览。
 */
export function buildComponentPreviewDSL(draft: DatasetDraft, target: GraphInput): DatasetDSL {
  const designer = draft.designer
  if (!designer) throw new Error('当前画布缺少组件拓扑，无法生成预览')

  const nodeIDs = new Set<string>()
  const joinIDs = new Set<string>()
  const groupIDs = new Set<string>()
  const transformIDs = new Set<string>()
  const visiting = new Set<string>()
  const visit = (input: GraphInput | undefined) => {
    if (!input) return
    const visitKey = `${input.kind}:${input.id}`
    if (visiting.has(visitKey)) throw new Error('组件拓扑存在循环，无法生成预览')
    visiting.add(visitKey)
    if (input.kind === 'NODE') nodeIDs.add(input.id)
    else if (input.kind === 'JOIN') {
      const join = designer.joins.find(item => item.id === input.id)
      if (!join) throw new Error('关联组件已失效，无法生成预览')
      joinIDs.add(join.id); visit(join.left); visit(join.right)
    } else if (input.kind === 'GROUP') {
      const group = designer.groups.find(item => item.id === input.id)
      if (!group) throw new Error('分组组件已失效，无法生成预览')
      groupIDs.add(group.id); visit(group.input)
    } else {
      const transform = designer.transforms?.find(item => item.id === input.id)
      if (!transform) throw new Error('字段处理组件已失效，无法生成预览')
      transformIDs.add(transform.id); visit(transform.input)
    }
    visiting.delete(visitKey)
  }
  visit(target)
  if (!nodeIDs.size) throw new Error('请先连接包含数据节点的上游组件')

  const previewNodes = draft.nodes.filter(node => nodeIDs.has(node.id))
  const produced = graphProducedFields(target, designer, draft.nodes, draft.fields)
  if (!produced.length) throw new Error('当前组件还没有可预览的输出字段')
  const usedCodes = new Set<string>()
  const outputs = [...new Map(produced.map(field => [field.key, field])).values()].map((field, index) => {
    const base = identifier(field.code || `field_${index + 1}`)
    let code = base
    let suffix = 2
    while (usedCodes.has(code)) code = identifier(`${base}_${suffix++}`)
    usedCodes.add(code)
    return { key: field.key, name: field.name || code, code }
  })
  const positions = Object.fromEntries(Object.entries(designer.nodePositions).filter(([id]) => nodeIDs.has(id)))
  const previewDesigner: DesignerGraphV1 = {
    version: '1.0',
    nodePositions: positions,
    nodeNames: Object.fromEntries(Object.entries(designer.nodeNames ?? {}).filter(([id]) => nodeIDs.has(id))),
    joins: designer.joins.filter(join => joinIDs.has(join.id)),
    groups: designer.groups.filter(group => groupIDs.has(group.id)),
    transforms: (designer.transforms ?? []).filter(transform => transformIDs.has(transform.id)),
    end: { id: 'end_1', name: '组件数据预览', input: target, position: { x: 0, y: 0 }, outputs },
  }
  const parameterCodes = new Set(draft.filters.filter(filter => nodeIDs.has(filter.nodeId)).map(filter => filter.parameterCode).filter(Boolean))
  return buildDatasetDSL({
    ...draft,
    code: identifier(draft.code || 'component_preview'),
    name: draft.name.trim() || '组件数据预览',
    nodes: previewNodes,
    fields: draft.fields.filter(field => nodeIDs.has(field.key.split('.')[0])),
    joins: draft.joins.filter(join => joinIDs.has(join.id)),
    filters: draft.filters.filter(filter => nodeIDs.has(filter.nodeId)),
    parameters: draft.parameters.filter(parameter => parameterCodes.has(parameter.code)),
    calculations: [],
    sorts: [],
    grainKeys: outputs.map(output => output.code).slice(0, 1),
    grainDescription: '每行代表一条组件预览记录',
    designer: previewDesigner,
  })
}

/** 将设计器文本输入转换为后端参数校验可识别的标量或多值数组。 */
export function buildPreviewParameters(parameters: ParameterOption[], values: Record<string, string>): Record<string, unknown> {
  // 前端只拆分多值文本并做必填提示；整数、日期等真实类型转换统一由服务端完成，
  // 避免浏览器输入格式在数据库与文件执行路径中得到不同解释。
  const result: Record<string, unknown> = {}
  for (const parameter of parameters) {
    const code = identifier(parameter.code)
    const raw = values[parameter.code]?.trim() ?? values[code]?.trim() ?? ''
    if (!raw) {
      if (parameter.required) throw new Error(`请填写预览参数 ${parameter.name || code}`)
      continue
    }
    result[code] = parameter.multiValue ? raw.split(',').map(item => item.trim()).filter(Boolean) : raw
    if (parameter.multiValue && !(result[code] as string[]).length) throw new Error(`预览参数 ${parameter.name || code} 至少需要一个值`)
  }
  return result
}

/** 为一次冻结后的发布候选生成重试时可复用的幂等键。 */
export function createDatasetPublishIdempotencyKey(): string {
  return globalThis.crypto.randomUUID()
}

const datasetPath = (id: string) => `/v1/datasets/${encodeURIComponent(id)}`

export const datasetAPI = {
  tables: (limit = 200, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<{ items: AssetTable[]; total?: number; limit?: number; offset?: number }>(`/v1/assets/tables?${query}`, { cache: 'no-store' })
  },
  // 快速建模只展示启用且已完成当前结构 LLM 映射的表资产；每张映射表可直接作为单表数据集。
  mappingTables: (limit = 200, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset), status: 'ACTIVE', managementStatus: 'ENABLED', enrichedOnly: 'true' })
    return apiRequest<{ items: AssetTable[]; total?: number; limit?: number; offset?: number }>(`/v1/assets/tables?${query}`, { cache: 'no-store' })
  },
  columns: async (tableID: string) => {
    const response = await apiRequest<{ items: AssetColumn[] }>(`/v1/assets/tables/${encodeURIComponent(tableID)}/columns`)
    // 资产详情会保留已失效字段供审计；数据集只能引用当前 ACTIVE 字段，否则保存时
    // 会在依赖快照阶段被服务端拒绝。
    return { ...response, items: response.items.filter(column => !column.assetStatus || column.assetStatus === 'ACTIVE') }
  },
  tablePreview: (tableID: string, maxRows = 5) => apiRequest<AssetTablePreview>(`/v1/assets/tables/${encodeURIComponent(tableID)}/preview?maxRows=${maxRows}`, { cache: 'no-store' }),
  // 指标等下游编辑器只能从租户内目录显式选择数据集，不能猜测或写死资源标识。
  list: (limit = 50, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<DatasetPage>(`/v1/datasets?${query}`, { cache: 'no-store' })
  },
  // 数据集聚合版本会在发布和协作保存时变化，读取时禁止复用浏览器或代理缓存。
  get: (id: string) => apiRequest<DatasetRecord>(datasetPath(id), { cache: 'no-store' }),
  listRevisions: (id: string, limit = 100, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<DatasetRevisionPage>(`${datasetPath(id)}/revisions?${query}`, { cache: 'no-store' })
  },
  getRevision: (id: string, revisionId: string) => apiRequest<DatasetRevisionRecord>(`${datasetPath(id)}/revisions/${encodeURIComponent(revisionId)}`, { cache: 'no-store' }),
  previewRevision: (id: string, revisionId: string, queryId: string, parameters: Record<string, unknown>, maxRows = 5) => apiRequest<DatasetPreview>(`${datasetPath(id)}/revisions/${encodeURIComponent(revisionId)}/preview`, { method: 'POST', cache: 'no-store', body: JSON.stringify({ queryId, parameters, maxRows }) }),
  previewCandidate: (dsl: DatasetDSL, queryId: string, parameters: Record<string, unknown>, maxRows = 5) => apiRequest<DatasetCandidatePreview>('/v1/datasets/candidate/preview', { method: 'POST', cache: 'no-store', body: JSON.stringify({ queryId, dsl, parameters, maxRows }) }),
  rollbackRevision: (id: string, revisionId: string, expectedVersion: number) => apiRequest<DatasetRecord>(`${datasetPath(id)}/revisions/${encodeURIComponent(revisionId)}/rollback`, { method: 'POST', body: JSON.stringify({ expectedVersion }) }),
  validate: (dsl: DatasetDSL) => apiRequest<{ valid: boolean; dslHash: string; planHash: string; logicalPlan: unknown }>('/v1/datasets/validate', { method: 'POST', body: JSON.stringify({ dsl }) }),
  create: (dsl: DatasetDSL) => apiRequest<DatasetRecord>('/v1/datasets', { method: 'POST', body: JSON.stringify({ code: dsl.dataset.code, name: dsl.dataset.name, description: dsl.dataset.description ?? '', type: dsl.dataset.type, dsl }) }),
  update: (id: string, version: number, draft: DatasetDraft, dsl: DatasetDSL) => apiRequest<DatasetRecord>(`${datasetPath(id)}/draft`, { method: 'PUT', body: JSON.stringify({ name: draft.name, description: draft.description, expectedVersion: version, dsl }) }),
  disable: (id: string, expectedVersion: number) => apiRequest<DatasetRecord>(`${datasetPath(id)}/disable`, { method: 'POST', body: JSON.stringify({ expectedVersion }) }),
  restore: (id: string, expectedVersion: number) => apiRequest<DatasetRecord>(`${datasetPath(id)}/restore`, { method: 'POST', body: JSON.stringify({ expectedVersion }) }),
  delete: (id: string, expectedVersion: number) => apiRequest<void>(datasetPath(id), { method: 'DELETE', body: JSON.stringify({ expectedVersion }) }),
  requestPublication: (id: string, input: PublishDatasetInput, note = '') => apiRequest<DatasetPublicationRequest>(`${datasetPath(id)}/publish-requests`, {
    method: 'POST', body: JSON.stringify({ ...input, note }),
  }),
  listPublicationRequests: (id: string, limit = 50, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<DatasetPublicationRequestPage>(`${datasetPath(id)}/publish-requests?${query}`, { cache: 'no-store' })
  },
  approvePublication: (id: string, requestId: string, expectedVersion: number, note = '') => apiRequest<DatasetPublicationApprovalResult>(`${datasetPath(id)}/publish-requests/${encodeURIComponent(requestId)}/approve`, {
    method: 'POST', body: JSON.stringify({ expectedVersion, note }),
  }),
  rejectPublication: (id: string, requestId: string, expectedVersion: number, reason: string) => apiRequest<DatasetPublicationRequest>(`${datasetPath(id)}/publish-requests/${encodeURIComponent(requestId)}/reject`, {
    method: 'POST', body: JSON.stringify({ expectedVersion, reason }),
  }),
  listVersions: (id: string, limit = 50, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<PublishedVersionPage>(`${datasetPath(id)}/versions?${query}`, { cache: 'no-store' })
  },
  getVersion: (id: string, versionId: string) => apiRequest<PublishedVersionRecord>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}`, { cache: 'no-store' }),
  getVersionUsage: (id: string, versionId: string) => apiRequest<VersionUsage>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}/usage`, { cache: 'no-store' }),
  rollbackVersion: (id: string, versionId: string, expectedVersion: number) => apiRequest<DatasetRecord>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}/rollback`, { method: 'POST', body: JSON.stringify({ expectedVersion }) }),
  transitionVersion: (id: string, versionId: string, input: VersionTransitionInput) => apiRequest<PublishedVersionRecord>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}/status`, { method: 'POST', body: JSON.stringify(input) }),
  evaluatePermission: (id: string, action: DatasetPermissionAction) => apiRequest<{ allowed: boolean }>('/v1/permissions/evaluate', { method: 'POST', body: JSON.stringify({ resourceType: 'DATASET', action, objectId: id }) }),
  preview: (id: string, queryId: string, parameters: Record<string, unknown>, maxRows = 100) => apiRequest<DatasetPreview>(`${datasetPath(id)}/preview`, { method: 'POST', cache: 'no-store', body: JSON.stringify({ queryId, parameters, maxRows }) }),
  previewDraft: (id: string, expectedVersion: number, dsl: DatasetDSL, queryId: string, parameters: Record<string, unknown>, maxRows = 5) => apiRequest<DatasetDraftPreview>(`${datasetPath(id)}/draft/preview`, { method: 'POST', cache: 'no-store', body: JSON.stringify({ queryId, expectedVersion, dsl, parameters, maxRows }) }),
  previewVersion: (id: string, versionId: string, queryId: string, parameters: Record<string, unknown>, maxRows = 100) => apiRequest<DatasetPreview>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}/preview`, { method: 'POST', cache: 'no-store', body: JSON.stringify({ queryId, parameters, maxRows }) }),
  cancel: (id: string, queryId: string) => apiRequest<{ cancelled: boolean }>(`${datasetPath(id)}/query-runs/${encodeURIComponent(queryId)}/cancel`, { method: 'POST', body: '{}' }),
}
