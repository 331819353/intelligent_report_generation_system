import type { ComponentRuntimeState, ReportComponent, ReportComponentInteractionEvent, ReportDocument, ReportParameter } from './report-contract'

export type ReportEffectScope = {
  kind: 'REPORT' | 'PAGE' | 'BLOCK' | 'COMPONENTS'
  componentIds?: string[]
}

export type ReportInteractionTarget = {
  componentId: string
  datasetVersionId: string
  semanticFieldCode: string
  fieldId: string
  datasetParameterCode: string
  operator: 'EQUALS' | 'IN' | 'BETWEEN' | 'GTE' | 'LTE'
}

export type ReportInteractionCommand = {
  requestRevision?: number
  type: 'PARAMETER_CHANGE' | 'CHART_FILTER' | 'DRILL_DOWN' | 'DRILL_UP'
  sourceComponentId: string
  parameterId?: string
  parameterCode?: string
  value?: unknown
  parameters: Record<string, unknown>
  affectedComponentIds: string[]
  targets: ReportInteractionTarget[]
  drill?: { componentId: string; level: number; fieldId: string; semanticFieldCode: string; label: string }
}

export type ReportInteractionExecutionResult = {
  componentData?: Record<string, ComponentRuntimeState>
}

export type ReportInteractionExecutor = (command: ReportInteractionCommand) => Promise<ReportInteractionExecutionResult | void>

export type ReportInteractionBuildResult = {
  command?: ReportInteractionCommand
  issue?: { path: string; reason: string }
}

type ComponentLocation = {
  pageId: string
  blockId: string
  component: ReportComponent
}

/**
 * 把筛选或图表事件解析为确定性的运行命令。
 * 报告 JSON 始终按参数 ID 建引用，运行参数始终按 code 传输，二者只在此处转换。
 */
export function buildReportInteractionCommand(document: ReportDocument, currentParameters: Record<string, unknown>, sourceComponentId: string, event: ReportComponentInteractionEvent): ReportInteractionBuildResult {
  const locations = componentLocations(document)
  const duplicateComponentID = findDuplicate(locations.map(item => item.component.id))
  if (duplicateComponentID) return { issue: { path: 'pages', reason: `组件标识 ${duplicateComponentID} 重复` } }
  const duplicateParameterID = findDuplicate((document.parameters ?? []).map(item => item.id))
  if (duplicateParameterID) return { issue: { path: 'parameters', reason: `报告参数标识 ${duplicateParameterID} 重复` } }
  const duplicateParameterCode = findDuplicate((document.parameters ?? []).map(item => item.code))
  if (duplicateParameterCode) return { issue: { path: 'parameters', reason: `报告参数编码 ${duplicateParameterCode} 重复` } }
  const source = locations.find(item => item.component.id === sourceComponentId)
  if (!source) return { issue: { path: 'pages', reason: `交互来源组件 ${sourceComponentId} 不存在` } }
  if (source.component.type === 'FILTER') return buildParameterCommand(document, currentParameters, locations, source, event, 'PARAMETER_CHANGE', source.component.binding)
  if (source.component.type !== 'CHART') return { issue: { path: componentPath(document, sourceComponentId), reason: '当前组件不支持数据联动' } }

  const drill = readRecord(source.component.interaction?.drill)
  if (event.drillLevel !== undefined && drill) return buildDrillCommand(document, currentParameters, source, event.drillLevel, event.drillDirection, drill)
  if (source.component.interaction?.clickFilter !== true) return { issue: { path: `${componentPath(document, sourceComponentId)}.interaction.clickFilter`, reason: '图表未启用点击联动' } }
  const linkage = readRecord(source.component.interaction?.linkage)
  if (!linkage) return { issue: { path: `${componentPath(document, sourceComponentId)}.interaction.linkage`, reason: '图表缺少联动配置' } }
  return buildParameterCommand(document, currentParameters, locations, source, event, 'CHART_FILTER', linkage)
}

function buildParameterCommand(document: ReportDocument, currentParameters: Record<string, unknown>, locations: ComponentLocation[], source: ComponentLocation, event: ReportComponentInteractionEvent, type: 'PARAMETER_CHANGE' | 'CHART_FILTER', config: unknown): ReportInteractionBuildResult {
  const record = readRecord(config)
  const parameterID = readString(record, 'parameterId')
  const path = componentPath(document, source.component.id)
  if (!parameterID) return { issue: { path: `${path}.${source.component.type === 'FILTER' ? 'binding' : 'interaction.linkage'}.parameterId`, reason: '尚未绑定报告参数' } }
  const parameter = document.parameters?.find(item => item.id === parameterID)
  if (!parameter) return { issue: { path, reason: `引用的报告参数 ${parameterID} 不存在` } }
  if (parameter.scope === 'PAGE' && parameter.pageId !== source.pageId) {
    return { issue: { path, reason: `页面参数 ${parameter.code} 不能从其他页面触发` } }
  }
  const normalized = normalizeReportParameterValue(parameter, event.value)
  if (normalized.issue) return { issue: { path: `${path}.value`, reason: normalized.issue } }
  const scope = readEffectScope(record?.effectScope)
  if (!scope) return { issue: { path, reason: '联动影响范围配置无效' } }
  const candidates = resolveTargets(locations, source, scope)
  if (candidates.issue) return { issue: { path, reason: candidates.issue } }
  if (parameter.scope === 'PAGE' && candidates.locations.some(item => item.pageId !== parameter.pageId)) {
    return { issue: { path, reason: `页面参数 ${parameter.code} 不能影响其他页面` } }
  }
  const binding = parameter.semanticBinding
  if (!binding || !binding.semanticFieldCode.trim() || !Array.isArray(binding.datasetFields)) return { issue: { path, reason: `参数 ${parameter.code} 缺少语义字段映射` } }
  const mappings = new Map<string, typeof binding.datasetFields[number]>()
  for (const mapping of binding.datasetFields) {
    if (!mapping.datasetVersionId?.trim() || !mapping.fieldId?.trim() || !mapping.datasetParameterCode?.trim()) {
      return { issue: { path, reason: '语义字段映射不完整' } }
    }
    if (mappings.has(mapping.datasetVersionId)) {
      return { issue: { path, reason: `语义字段 ${binding.semanticFieldCode} 在数据集 ${mapping.datasetVersionId} 中存在歧义映射` } }
    }
    mappings.set(mapping.datasetVersionId, mapping)
  }
  const operator = readOperator(record)
  if (!operator) return { issue: { path, reason: '联动操作符无效' } }
  const operatorIssue = validateOperatorValue(operator, parameter, normalized.value)
  if (operatorIssue) return { issue: { path: `${path}.value`, reason: operatorIssue } }
  const targets: ReportInteractionTarget[] = []
  for (const candidate of candidates.locations) {
    const datasetVersionID = readString(candidate.component.binding, 'datasetVersionId')
    if (!datasetVersionID) {
      // 显式目标代表调用方要求刷新该组件，缺少数据集绑定时不能静默跳过。
      if (scope.kind === 'COMPONENTS') return { issue: { path, reason: `目标组件 ${candidate.component.id} 缺少数据集版本绑定` } }
      continue
    }
    const mapping = mappings.get(datasetVersionID)
    if (!mapping) return { issue: { path, reason: `语义字段 ${binding.semanticFieldCode} 在数据集 ${datasetVersionID} 中没有映射` } }
    targets.push({
      componentId: candidate.component.id,
      datasetVersionId: datasetVersionID,
      semanticFieldCode: binding.semanticFieldCode,
      fieldId: mapping.fieldId,
      datasetParameterCode: mapping.datasetParameterCode,
      operator,
    })
  }
  if (targets.length === 0) return { issue: { path, reason: '联动作用域内没有可绑定的数据组件' } }
  if (type === 'CHART_FILTER') {
    const sourceDatasetVersionID = readString(source.component.binding, 'datasetVersionId')
    const sourceMapping = sourceDatasetVersionID ? mappings.get(sourceDatasetVersionID) : undefined
    const dimensions = Array.isArray(source.component.binding?.dimensions) ? source.component.binding.dimensions : []
    if (!sourceMapping || !dimensions.some(item => readString(item, 'fieldId') === sourceMapping.fieldId)) {
      return { issue: { path, reason: '图表点击值不属于已声明的联动维度' } }
    }
  }
  const targetIDs = new Set(targets.map(item => item.componentId))
  const affectedComponentIds = candidates.locations
    .filter(item => targetIDs.has(item.component.id) || isDependentConclusion(item.component, targetIDs))
    .filter(item => isRefreshable(item.component))
    .map(item => item.component.id)
  const parameters = cloneParameters(currentParameters)
  parameters[parameter.code] = cloneRuntimeValue(normalized.value)
  return {
    command: {
      type,
      sourceComponentId: source.component.id,
      parameterId: parameter.id,
      parameterCode: parameter.code,
      value: normalized.value,
      parameters,
      affectedComponentIds,
      targets,
    },
  }
}

function buildDrillCommand(document: ReportDocument, parameters: Record<string, unknown>, source: ComponentLocation, level: number, direction: 'DOWN' | 'UP' | undefined, drill: Record<string, unknown>): ReportInteractionBuildResult {
  const levels = Array.isArray(drill.levels) ? drill.levels.map(readRecord).filter(Boolean) as Record<string, unknown>[] : []
  if (!Number.isInteger(level) || level < -1 || level >= levels.length) return { issue: { path: `${componentPath(document, source.component.id)}.interaction.drill`, reason: '下钻层级超出配置范围' } }
  const commandType = direction ?? (level === -1 ? 'UP' : 'DOWN')
  if ((level === -1 && commandType !== 'UP') || (level >= 0 && commandType !== 'UP' && commandType !== 'DOWN')) {
    return { issue: { path: `${componentPath(document, source.component.id)}.interaction.drill`, reason: '下钻方向与目标层级不一致' } }
  }
  const datasetVersionID = readString(source.component.binding, 'datasetVersionId')
  if (!datasetVersionID) return { issue: { path: componentPath(document, source.component.id), reason: '下钻图表缺少数据集绑定' } }
  if (level === -1) {
    return { command: { type: 'DRILL_UP', sourceComponentId: source.component.id, parameters: cloneParameters(parameters), affectedComponentIds: [source.component.id], targets: [] } }
  }
  const item = levels[level]
  const fieldID = readString(item, 'fieldId')
  const semanticFieldCode = readString(item, 'semanticFieldCode')
  const label = readString(item, 'label')
  if (!fieldID || !semanticFieldCode || !label) return { issue: { path: `${componentPath(document, source.component.id)}.interaction.drill.levels[${level}]`, reason: '下钻层级配置不完整' } }
  return {
    command: {
      type: commandType === 'UP' ? 'DRILL_UP' : 'DRILL_DOWN', sourceComponentId: source.component.id, parameters: cloneParameters(parameters), affectedComponentIds: [source.component.id], targets: [],
      drill: { componentId: source.component.id, level, fieldId: fieldID, semanticFieldCode, label },
    },
  }
}

/** 按参数声明规范化值，禁止把展示字符串原样带入运行服务。 */
export function normalizeReportParameterValue(parameter: ReportParameter, rawValue: unknown): { value?: unknown; issue?: string } {
  if (parameter.dataType === 'DATE_RANGE') {
    if (parameter.multiValue) return { issue: `${parameter.name}的日期范围不能声明为多值参数` }
    if (rawValue === null || rawValue === undefined || rawValue === '') return parameter.required ? { issue: `${parameter.name}不能为空` } : { value: undefined }
    if (!Array.isArray(rawValue) || rawValue.length !== 2 || rawValue.some(item => typeof item !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(item))) {
      return { issue: `${parameter.name}必须为两个日期组成的范围` }
    }
    return { value: [...rawValue] }
  }
  if (parameter.multiValue) {
    if (!Array.isArray(rawValue)) return { issue: `${parameter.name}必须为多值数组` }
    if (parameter.required && rawValue.length === 0) return { issue: `${parameter.name}不能为空` }
    if (rawValue.length > 1000) return { issue: `${parameter.name}最多允许 1000 项` }
    const values = rawValue.map(item => normalizeScalar(parameter.dataType, item))
    const failed = values.find(item => item.issue)
    return failed ? { issue: failed.issue } : { value: values.map(item => item.value) }
  }
  if (Array.isArray(rawValue)) return { issue: `${parameter.name}必须为单值` }
  if (rawValue === '' || rawValue === null || rawValue === undefined) return parameter.required ? { issue: `${parameter.name}不能为空` } : { value: undefined }
  return normalizeScalar(parameter.dataType, rawValue)
}

function normalizeScalar(dataType: string, value: unknown): { value?: unknown; issue?: string } {
  if (dataType === 'INTEGER') {
    const number = typeof value === 'number' ? value : Number(value)
    return Number.isSafeInteger(number) ? { value: number } : { issue: '参数必须为安全整数' }
  }
  if (dataType === 'DECIMAL') {
    const number = typeof value === 'number' ? value : Number(value)
    return Number.isFinite(number) ? { value: number } : { issue: '参数必须为有限数字' }
  }
  if (dataType === 'BOOLEAN') {
    if (typeof value === 'boolean') return { value }
    if (value === 'true' || value === 'false') return { value: value === 'true' }
    return { issue: '参数必须为布尔值' }
  }
  if (dataType === 'SINGLE_SELECT' || dataType === 'MULTI_SELECT') {
    return ['string', 'number', 'boolean'].includes(typeof value) ? { value } : { issue: '选择参数必须为字符串、数字或布尔值' }
  }
  if (typeof value !== 'string') return { issue: '参数必须为字符串' }
  const patterns: Record<string, RegExp> = {
    DATE: /^\d{4}-\d{2}-\d{2}$/,
    DATETIME: /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?(?:Z|[+-]\d{2}:\d{2})?$/,
    DATE_YEAR: /^\d{4}$/,
    DATE_MONTH: /^\d{4}-(0[1-9]|1[0-2])$/,
    DATE_QUARTER: /^\d{4}-Q[1-4]$/,
  }
  return patterns[dataType] && !patterns[dataType].test(value) ? { issue: '参数格式与声明类型不匹配' } : { value }
}

function resolveTargets(locations: ComponentLocation[], source: ComponentLocation, scope: ReportEffectScope): { locations: ComponentLocation[]; issue?: string } {
  if (scope.kind === 'REPORT') return { locations }
  if (scope.kind === 'PAGE') return { locations: locations.filter(item => item.pageId === source.pageId) }
  if (scope.kind === 'BLOCK') return { locations: locations.filter(item => item.pageId === source.pageId && item.blockId === source.blockId) }
  const ids = scope.componentIds ?? []
  if (ids.length === 0) return { locations: [], issue: '指定组件作用域至少需要一个目标组件' }
  if (new Set(ids).size !== ids.length) return { locations: [], issue: '指定组件作用域不能包含重复目标' }
  const result = ids.map(id => locations.find(item => item.component.id === id))
  const missing = ids.find((_, index) => !result[index])
  return missing ? { locations: [], issue: `指定的目标组件 ${missing} 不存在` } : { locations: result as ComponentLocation[] }
}

function readEffectScope(value: unknown): ReportEffectScope | undefined {
  const record = readRecord(value)
  const kind = readString(record, 'kind')
  if (!kind || !['REPORT', 'PAGE', 'BLOCK', 'COMPONENTS'].includes(kind)) return undefined
  const hasComponentIDs = record !== undefined && Object.prototype.hasOwnProperty.call(record, 'componentIds')
  if (kind !== 'COMPONENTS' && hasComponentIDs) return undefined
  if (kind === 'COMPONENTS' && (!Array.isArray(record?.componentIds) || record.componentIds.some(item => typeof item !== 'string') || record.componentIds.length > 100)) return undefined
  const ids = Array.isArray(record?.componentIds) ? record.componentIds as string[] : undefined
  return { kind: kind as ReportEffectScope['kind'], componentIds: ids }
}

/** 草稿可暂缺操作符，但进入运行解析后必须显式声明，避免前后端各自猜测默认值。 */
function readOperator(record: Record<string, unknown> | undefined): ReportInteractionTarget['operator'] | undefined {
  const value = record?.operator
  return typeof value === 'string' && ['EQUALS', 'IN', 'BETWEEN', 'GTE', 'LTE'].includes(value) ? value as ReportInteractionTarget['operator'] : undefined
}

function isRefreshable(component: ReportComponent): boolean {
  return component.type !== 'FILTER' && component.refreshPolicy?.mode !== 'NONE'
}

function isDependentConclusion(component: ReportComponent, targetIDs: Set<string>): boolean {
  if (component.type !== 'CONCLUSION' || !Array.isArray(component.binding?.chartComponentIds)) return false
  return component.binding.chartComponentIds.some(item => typeof item === 'string' && targetIDs.has(item))
}

function validateOperatorValue(operator: ReportInteractionTarget['operator'], parameter: ReportParameter, value: unknown): string | undefined {
  const dateRange = parameter.dataType === 'DATE_RANGE'
  if (operator === 'IN' && !parameter.multiValue) return 'IN 操作符只允许多值参数'
  if (operator === 'BETWEEN' && ((!parameter.multiValue && !dateRange) || !Array.isArray(value) || value.length !== 2)) return 'BETWEEN 操作符要求恰好两个参数值'
  if ((operator === 'EQUALS' || operator === 'GTE' || operator === 'LTE') && (parameter.multiValue || dateRange)) return `${operator} 操作符只允许单值参数`
  return undefined
}

function componentLocations(document: ReportDocument): ComponentLocation[] {
  return document.pages.flatMap(page => page.blocks.flatMap(block => block.components.map(component => ({ pageId: page.id, blockId: block.id, component }))))
}

function findDuplicate(values: string[]): string | undefined {
  const seen = new Set<string>()
  for (const value of values) {
    if (seen.has(value)) return value
    seen.add(value)
  }
  return undefined
}

function cloneParameters(parameters: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(parameters).map(([key, value]) => [key, cloneRuntimeValue(value)]))
}

/** 运行参数可能包含多选、日期范围或级联对象，命令必须与调用方状态彻底隔离。 */
function cloneRuntimeValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(cloneRuntimeValue)
  const record = readRecord(value)
  if (record) return Object.fromEntries(Object.entries(record).map(([key, item]) => [key, cloneRuntimeValue(item)]))
  return value
}

function componentPath(document: ReportDocument, componentID: string): string {
  for (let pageIndex = 0; pageIndex < document.pages.length; pageIndex += 1) {
    for (let blockIndex = 0; blockIndex < document.pages[pageIndex].blocks.length; blockIndex += 1) {
      const componentIndex = document.pages[pageIndex].blocks[blockIndex].components.findIndex(item => item.id === componentID)
      if (componentIndex >= 0) return `pages[${pageIndex}].blocks[${blockIndex}].components[${componentIndex}]`
    }
  }
  return 'pages'
}

function readRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function readString(value: unknown, key: string): string | undefined {
  const candidate = readRecord(value)?.[key]
  return typeof candidate === 'string' ? candidate : undefined
}
