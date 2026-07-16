import { apiRequest } from './api'

export type AssetTable = {
  id: string; dataSourceId: string; dataSourceName: string; dataSourceType: string
  tableName: string; schemaName: string; businessName: string; columnCount: number; fileVersionId?: string
}
export type AssetColumn = {
  id: string; tableId: string; columnName: string; businessName: string
  canonicalType: string; nullable: boolean; semanticType: string
}
export type DesignerNode = { id: string; alias: string; table: AssetTable; columns: AssetColumn[]; selected: string[] }
export type FieldOption = { key: string; role: string; aggregation: string }
export type JoinOption = { id: string; leftNodeId: string; rightNodeId: string; leftField: string; rightField: string; joinType: string; cardinality: string; manualConfirmed: boolean }
export type FilterOption = { id: string; nodeId: string; field: string; operator: string; value: string; parameterCode: string }
export type ParameterOption = { code: string; name: string; dataType: string; required: boolean; multiValue: boolean }
export type CalculatedField = { id: string; code: string; name: string; operation: string; leftKey: string; rightKey: string; canonicalType: string }
export type SortOption = { fieldId: string; direction: string }
export type DatasetDraft = {
  code: string; name: string; description: string; nodes: DesignerNode[]; fields: FieldOption[]
  joins: JoinOption[]; filters: FilterOption[]; parameters: ParameterOption[]; calculations: CalculatedField[]
  sorts: SortOption[]; grainDescription: string; grainKeys: string[]
}
export type DatasetRecord = {
  id: string; code: string; name: string; description: string; type: string; status: string
  version: number; draftVersionId: string; draftRecordVersion: number; currentPublishedVersionId?: string
  dslHash: string; planHash: string; dsl: DatasetDSL; logicalPlan: unknown
}
export type DatasetSummary = {
  id: string; code: string; name: string; description: string; type: string; status: string
  version: number; dslHash: string; currentPublishedVersionId?: string; updatedAt: string
}
export type DatasetPage = {
  items: DatasetSummary[]; total: number; limit: number; offset: number
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
export type DatasetPreview = {
  queryId: string; columns: string[]; rows: unknown[][]; rowCount: number; durationMs: number
  warnings?: Array<{ code: string; message: string; joinId?: string; estimatedRows?: number }>
}
export type DatasetDSL = Record<string, unknown> & {
  dslVersion: string; dataset: { code: string; name: string; description?: string; type: string }
  nodes: Array<Record<string, unknown>>; fields: Array<Record<string, unknown>>
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

/** 从设计器状态生成可由服务端严格校验的 DSL，不生成或保存 SQL。 */
export function buildDatasetDSL(draft: DatasetDraft): DatasetDSL {
  if (!draft.code.trim() || !draft.name.trim()) throw new Error('请填写数据集编码和名称')
  if (!draft.nodes.length) throw new Error('请至少选择一张表')
  // selected 是字段、计算、分组和粒度映射的唯一物理列来源；未勾选列不会被
  // 意外写入 projection 或表达式树。
  const selected = draft.nodes.flatMap(node => node.columns.filter(column => node.selected.includes(column.columnName)).map(column => ({ node, column })))
  if (!selected.length) throw new Error('请至少选择一个输出字段')
  const options = new Map(draft.fields.map(item => [item.key, item]))
  // 聚合设置会把源字段包装成 AGGREGATE 表达式并强制为 MEASURE；非聚合字段
  // 则保留设计器中的语义角色。
  const baseFields = selected.map(({ node, column }) => {
    const option = options.get(`${node.id}.${column.columnName}`) ?? { role: 'ATTRIBUTE', aggregation: '' }
    const source = { type: 'FIELD_REF', nodeId: node.id, field: column.columnName }
    return {
      id: fieldID(node, column), code: fieldCode(node, column, draft.nodes.length > 1),
      name: column.businessName || column.columnName, role: option.aggregation ? 'MEASURE' : option.role,
      expression: option.aggregation ? { type: 'AGGREGATE', function: option.aggregation, argument: source } : source,
      canonicalType: canonicalType(column.canonicalType), nullable: column.nullable, visible: true,
    }
  })
  const sourceByKey = new Map(selected.map(({ node, column }) => [`${node.id}.${column.columnName}`, { type: 'FIELD_REF', nodeId: node.id, field: column.columnName }]))
  const calculated = draft.calculations.map(item => {
    const left = sourceByKey.get(item.leftKey), right = sourceByKey.get(item.rightKey)
    if (!left || !right) throw new Error(`计算字段 ${item.name || item.code} 引用了未选择字段`)
    return { id: item.id, code: identifier(item.code), name: item.name, role: 'MEASURE', expression: { type: item.operation, arguments: [left, right] }, canonicalType: item.canonicalType, nullable: true, visible: true }
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
  // 所有非度量基础字段自动参与分组，使包含聚合字段的 DSL 满足数据库分组规则。
  // 计算字段不会被隐式提升为粒度键，仍由用户显式选择输出粒度。
  const groupBy = baseFields.filter(field => field.role !== 'MEASURE').map(field => field.id)
  const grainKeys = draft.grainKeys.filter(code => fieldIDs.has(code))
  if (!draft.grainDescription.trim() || !grainKeys.length) throw new Error('请填写输出粒度并选择至少一个粒度键')
  const sourceCount = new Set(draft.nodes.map(node => node.table.dataSourceId)).size
  return {
    dslVersion: '1.0',
    dataset: { code: identifier(draft.code), name: draft.name.trim(), description: draft.description.trim(), type: sourceCount > 1 ? 'CROSS_SOURCE' : 'SINGLE_SOURCE' },
    nodes: draft.nodes.map(node => ({ id: node.id, type: 'TABLE', datasourceId: node.table.dataSourceId, tableId: node.table.id, ...(node.table.fileVersionId ? { fileVersionId: node.table.fileVersionId } : {}), alias: identifier(node.alias), projection: [...node.selected], sourceFilters: [] })),
    joins: draft.joins.map(join => ({ id: join.id, leftNodeId: join.leftNodeId, rightNodeId: join.rightNodeId, joinType: join.joinType, cardinality: join.cardinality, manualConfirmed: join.manualConfirmed, conditions: [{ leftExpression: { type: 'FIELD_REF', nodeId: join.leftNodeId, field: join.leftField }, operator: 'EQUALS', rightExpression: { type: 'FIELD_REF', nodeId: join.rightNodeId, field: join.rightField } }] })),
    fields, filters, groupBy, having: [],
    sorts: draft.sorts.filter(item => item.fieldId).map(item => ({ fieldId: fieldIDs.get(item.fieldId) ?? item.fieldId, direction: item.direction })),
    parameters: draft.parameters.map(item => ({ ...item, code: identifier(item.code) })),
    outputGrain: { description: draft.grainDescription.trim(), keyFields: grainKeys },
    executionPolicy: { mode: 'REALTIME', timeoutMs: 5000, previewLimit: 200, resultLimit: 10000, cacheTtlSeconds: 300, materialization: { enabled: false } },
  }
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
  tables: () => apiRequest<{ items: AssetTable[] }>('/v1/assets/tables?limit=200'),
  columns: (tableID: string) => apiRequest<{ items: AssetColumn[] }>(`/v1/assets/tables/${encodeURIComponent(tableID)}/columns`),
  // 指标等下游编辑器只能从租户内目录显式选择数据集，不能猜测或写死资源标识。
  list: (limit = 50, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<DatasetPage>(`/v1/datasets?${query}`, { cache: 'no-store' })
  },
  // 数据集聚合版本会在发布和协作保存时变化，读取时禁止复用浏览器或代理缓存。
  get: (id: string) => apiRequest<DatasetRecord>(datasetPath(id), { cache: 'no-store' }),
  validate: (dsl: DatasetDSL) => apiRequest<{ valid: boolean; dslHash: string; planHash: string; logicalPlan: unknown }>('/v1/datasets/validate', { method: 'POST', body: JSON.stringify({ dsl }) }),
  create: (dsl: DatasetDSL) => apiRequest<DatasetRecord>('/v1/datasets', { method: 'POST', body: JSON.stringify({ code: dsl.dataset.code, name: dsl.dataset.name, description: dsl.dataset.description ?? '', type: dsl.dataset.type, dsl }) }),
  update: (id: string, version: number, draft: DatasetDraft, dsl: DatasetDSL) => apiRequest<DatasetRecord>(`${datasetPath(id)}/draft`, { method: 'PUT', body: JSON.stringify({ name: draft.name, description: draft.description, expectedVersion: version, dsl }) }),
  publish: (id: string, input: PublishDatasetInput, idempotencyKey: string) => apiRequest<PublishedVersionRecord>(`${datasetPath(id)}/publish`, { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(input) }),
  listVersions: (id: string, limit = 50, offset = 0) => {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) })
    return apiRequest<PublishedVersionPage>(`${datasetPath(id)}/versions?${query}`, { cache: 'no-store' })
  },
  getVersion: (id: string, versionId: string) => apiRequest<PublishedVersionRecord>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}`, { cache: 'no-store' }),
  getVersionUsage: (id: string, versionId: string) => apiRequest<VersionUsage>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}/usage`, { cache: 'no-store' }),
  transitionVersion: (id: string, versionId: string, input: VersionTransitionInput) => apiRequest<PublishedVersionRecord>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}/status`, { method: 'POST', body: JSON.stringify(input) }),
  evaluatePermission: (id: string, action: DatasetPermissionAction) => apiRequest<{ allowed: boolean }>('/v1/permissions/evaluate', { method: 'POST', body: JSON.stringify({ resourceType: 'DATASET', action, objectId: id }) }),
  preview: (id: string, queryId: string, parameters: Record<string, unknown>, maxRows = 100) => apiRequest<DatasetPreview>(`${datasetPath(id)}/preview`, { method: 'POST', body: JSON.stringify({ queryId, parameters, maxRows }) }),
  previewVersion: (id: string, versionId: string, queryId: string, parameters: Record<string, unknown>, maxRows = 100) => apiRequest<DatasetPreview>(`${datasetPath(id)}/versions/${encodeURIComponent(versionId)}/preview`, { method: 'POST', body: JSON.stringify({ queryId, parameters, maxRows }) }),
  cancel: (id: string, queryId: string) => apiRequest<{ cancelled: boolean }>(`${datasetPath(id)}/query-runs/${encodeURIComponent(queryId)}/cancel`, { method: 'POST', body: '{}' }),
}
