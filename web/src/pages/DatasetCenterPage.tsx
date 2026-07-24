import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type DragEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { ApproximateEqualsIcon, ArrowClockwiseIcon, ArrowCounterClockwiseIcon, ArrowsInSimpleIcon, ArrowsLeftRightIcon, ArrowsOutSimpleIcon, CalendarDotsIcon, CaretDownIcon, CaretUpIcon, CheckCircleIcon, DropSlashIcon, GitMergeIcon, LinkSimpleIcon, ListChecksIcon, MagicWandIcon, MathOperationsIcon, PlusMinusIcon, RowsIcon, ScissorsIcon, SwapIcon, TextAaIcon, TextTIcon, TextTSlashIcon, TreeStructureIcon, XIcon, type Icon } from '@phosphor-icons/react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import {
  datasetAIPlanFromEditor,
  datasetAIRequestContext,
  materializeDatasetAIPlan,
  normalizeDatasetAIPlanHints,
  requestDatasetAIProposal,
  type DatasetAIPlanHints,
  type DatasetAIPlanResult,
} from '../lib/dataset-ai'
import { hydrateDatasetDraft } from '../lib/dataset-draft'
import {
  generatedGraphFieldIdentity,
  graphContains,
  graphConnectionError,
  graphInputKey,
  graphLeaves,
  graphOutputKeys,
  graphProducedFieldLabel,
  graphProducedFields,
  hydrateDesignerGraph,
  layoutDesignerGraph,
  serializeDesignerGraph,
  validateDesignerGraph,
  type CanvasPoint,
  type DesignerGraphV1,
  type GraphEnd,
  type GraphGroup,
  type GraphInput,
  type GraphJoin,
  type GraphConditionOperator,
  type GraphTransform,
  type GraphTransformComponentType,
  type GraphTransformFamily,
  type GraphTransformOperation,
  type GraphTransformRule,
  type ProducedField,
} from '../lib/dataset-graph'
import {
  buildComponentPreviewDSL,
  buildDatasetDSL,
  datasetAPI,
  type AssetColumn,
  type AssetTable,
  type AssetTablePreview,
  type DatasetDraft,
  type DatasetPreview,
  type DatasetPublicationRequest,
  type DatasetRecord,
  type DatasetSummary,
  type DesignerNode,
  type FieldOption,
  type JoinOption,
  type PublishedVersionRecord,
  type PublishedVersionSummary,
} from '../lib/datasets'

type RelationInput = GraphInput
type CurveGeometry = { path: string; midpoint: CanvasPoint }
type RelationBox = GraphJoin
type GroupBox = GraphGroup
type TransformBox = GraphTransform
type EndBox = GraphEnd
type CanvasComponentKind = RelationInput['kind'] | 'END'
type CanvasPreviewTarget = RelationInput | { kind: 'END'; id: string }
type NodePreviewState = { loading: boolean; data?: AssetTablePreview; error?: string; suggestion?: string }
type VersionPreviewState = { versionID: string; loading: boolean; data?: DatasetPreview; error?: string }
type DialogState = { mode: 'create' | 'view' | 'metadata' | 'history' | 'publish' | 'disable' | 'restore' | 'delete'; dataset?: DatasetSummary }
type Notice = { tone: 'success' | 'error'; message: string }
type DatasetEditorSnapshot = {
  draft: DatasetDraft
  relationBoxes: RelationBox[]
  groupBoxes: GroupBox[]
  transformBoxes: TransformBox[]
  endBox: EndBox | null
  nodePositions: Record<string, CanvasPoint>
  metadata: { name: string; description: string }
}
type DatasetAIUndo = { before: DatasetEditorSnapshot; appliedFingerprint: string }
type DatasetAIReviewLabels = { nodes: Record<string, string>; fields: Record<string, string> }
type DatasetAIRetryAction = 'GENERATE' | 'APPLY' | null
type PendingMetricAIAutoRun = { key: string; instruction: string }
type DatasetAIErrorView = {
  title: string
  message: string
  suggestion: string
  code?: string
  reasonCode?: string
  stage?: string
  repairAttempted?: boolean
  status?: number
  requestId?: string
  diagnosticCode?: string
}

const statusLabels: Record<string, string> = {
  DRAFT: '草稿', VALIDATING: '校验中', PUBLISHED: '已发布', STALE: '已失效', DEPRECATED: '已废弃', DISABLED: '已停用',
}
const typeLabels: Record<string, string> = { SINGLE_SOURCE: '单数据源', CROSS_SOURCE: '跨数据源' }
const publicationStatusLabels: Record<string, string> = { PENDING: '待审批', APPROVED: '已通过', REJECTED: '已拒绝' }
const metricCandidateGenerationLabels: Record<string, string> = {
  LEGACY: '审批后生成', PENDING: '后台生成中', SUCCEEDED: '已生成', PARTIAL: '已生成，部分待复核', FAILED: '生成失败',
}
type TransformPaletteCategory = 'TEXT' | 'NUMBER' | 'DATE' | 'RULE'
type TransformComponentMeta = {
  componentType: GraphTransformComponentType
  family: GraphTransformFamily
  category: TransformPaletteCategory
  label: string
  description: string
  sortKey: string
  operations: GraphTransformOperation[]
  icon: Icon
}
const transformComponentMeta: TransformComponentMeta[] = [
  { componentType: 'TEXT_UPPER', family: 'TEXT', category: 'TEXT', label: '大写转换', description: '英文字母统一转大写', sortKey: 'DAXIEZHUANHUAN', operations: ['UPPER'], icon: TextAaIcon },
  { componentType: 'TEXT_TRIM', family: 'TEXT', category: 'TEXT', label: '空格清理', description: '去除文本首尾空格', sortKey: 'KONGGEQINGLI', operations: ['TRIM'], icon: TextTSlashIcon },
  { componentType: 'TEXT_REPLACE', family: 'TEXT', category: 'TEXT', label: '文本替换', description: '查找并替换指定文本', sortKey: 'WENBENTIHUAN', operations: ['REPLACE'], icon: SwapIcon },
  { componentType: 'TEXT_LOWER', family: 'TEXT', category: 'TEXT', label: '小写转换', description: '英文字母统一转小写', sortKey: 'XIAOXIEZHUANHUAN', operations: ['LOWER'], icon: TextTIcon },
  { componentType: 'TEXT_SUBSTRING', family: 'TEXT', category: 'TEXT', label: '字段截取', description: '按起始位置截取文本', sortKey: 'ZIDUANJIEQU', operations: ['SUBSTRING'], icon: ScissorsIcon },
  { componentType: 'TEXT_CONCAT', family: 'TEXT', category: 'TEXT', label: '字段拼接', description: '用连接符拼接两字段', sortKey: 'ZIDUANPINJIE', operations: ['CONCAT'], icon: LinkSimpleIcon },
  { componentType: 'NUMBER_ABSOLUTE', family: 'NUMBER', category: 'NUMBER', label: '取绝对值', description: '将负数转换为正数值', sortKey: 'QUJUEDUIZHI', operations: ['ABS'], icon: PlusMinusIcon },
  { componentType: 'NUMBER_ROUNDING', family: 'NUMBER', category: 'NUMBER', label: '数值取整', description: '四舍五入或上下取整', sortKey: 'SHUZHIQUZHENG', operations: ['ROUND', 'FLOOR', 'CEIL'], icon: ApproximateEqualsIcon },
  { componentType: 'NUMBER_ARITHMETIC', family: 'NUMBER', category: 'NUMBER', label: '数值运算', description: '两个字段加减乘除', sortKey: 'SHUZHIYUNSUAN', operations: ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE'], icon: MathOperationsIcon },
  { componentType: 'DATE_FORMAT', family: 'DATE', category: 'DATE', label: '日期转换', description: '输出年、年月、年季或年月日', sortKey: 'RIQIZHUANHUAN', operations: ['DATE_FORMAT'], icon: CalendarDotsIcon },
  { componentType: 'NULL', family: 'NULL', category: 'RULE', label: '空值填充', description: '仅为空时补固定值或字段', sortKey: 'KONGZHITIANCHONG', operations: ['COALESCE'], icon: DropSlashIcon },
  { componentType: 'CAST', family: 'CAST', category: 'RULE', label: '类型转换', description: '规范字段的数据类型', sortKey: 'LEIXINGZHUANHUAN', operations: ['CAST'], icon: ArrowsLeftRightIcon },
  { componentType: 'CONDITION', family: 'CONDITION', category: 'RULE', label: '条件映射', description: '按比较条件输出新值', sortKey: 'TIAOJIANYINGSHE', operations: ['CASE'], icon: ListChecksIcon },
]
const transformCategoryMeta: Array<{ category: TransformPaletteCategory; label: string; className: string }> = [
  { category: 'TEXT', label: '文本组件', className: 'component-text' },
  { category: 'NUMBER', label: '数值组件', className: 'component-number' },
  { category: 'DATE', label: '日期组件', className: 'component-date' },
  { category: 'RULE', label: '规则组件', className: 'component-rule' },
]
const transformComponentDefinition = (componentType: GraphTransformComponentType) => transformComponentMeta.find(item => item.componentType === componentType)
const transformComponentTypeFor = (transform: Pick<GraphTransform, 'family' | 'componentType' | 'rules'>): GraphTransformComponentType => {
  if (transform.componentType) return transform.componentType
  const operation = transform.rules[0]?.operation
  if (transform.family === 'DATE') return 'DATE_FORMAT'
  if (transform.family === 'CAST') return 'CAST'
  if (transform.family === 'CONDITION') return 'CONDITION'
  if (transform.family === 'NULL') return 'NULL'
  if (transform.family === 'NUMBER') {
    if (operation === 'ABS') return 'NUMBER_ABSOLUTE'
    if (operation && ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE'].includes(operation)) return 'NUMBER_ARITHMETIC'
    return 'NUMBER_ROUNDING'
  }
  if (operation === 'UPPER') return 'TEXT_UPPER'
  if (operation === 'TRIM') return 'TEXT_TRIM'
  if (operation === 'REPLACE') return 'TEXT_REPLACE'
  if (operation === 'LOWER') return 'TEXT_LOWER'
  if (operation === 'CONCAT' || transform.family === 'SPLIT_MERGE' && !operation) return 'TEXT_CONCAT'
  return 'TEXT_SUBSTRING'
}
const transformComponentMetaFor = (transform: Pick<GraphTransform, 'family' | 'componentType' | 'rules'>) => transformComponentDefinition(transformComponentTypeFor(transform))
const transformDisplayLabel = (transform: Pick<GraphTransform, 'family' | 'componentType' | 'rules'>) => transformComponentMetaFor(transform)?.label || '字段处理'
const transformColorClass = (transform: Pick<GraphTransform, 'family' | 'componentType' | 'rules'>) => {
  const category = transformComponentMetaFor(transform)?.category || 'RULE'
  return transformCategoryMeta.find(item => item.category === category)?.className || 'component-rule'
}
type DateFormatUnit = 'YEAR' | 'MONTH' | 'QUARTER' | 'DAY'
const dateFormatMeta: Record<DateFormatUnit, { label: string; format: string; example: string; suffix: string }> = {
  YEAR: { label: '年', format: 'YYYY', example: '2026', suffix: 'yyyy' },
  MONTH: { label: '年月', format: 'YYYYMM', example: '202607', suffix: 'yyyymm' },
  QUARTER: { label: '年季', format: 'YYYYQn', example: '2026Q3', suffix: 'yyyyq' },
  DAY: { label: '年月日', format: 'YYYYMMDD', example: '20260715', suffix: 'yyyymmdd' },
}
const dateFormatOptions = (Object.keys(dateFormatMeta) as DateFormatUnit[]).map(value => ({
  value, label: `${dateFormatMeta[value].label}（${dateFormatMeta[value].format}）`,
}))
const conditionOperatorOptions: Array<{ value: GraphConditionOperator; label: string }> = [
  { value: 'EQUALS', label: '等于' }, { value: 'NOT_EQUALS', label: '不等于' },
  { value: 'GT', label: '大于' }, { value: 'GTE', label: '大于等于' },
  { value: 'LT', label: '小于' }, { value: 'LTE', label: '小于等于' },
  { value: 'CONTAINS', label: '包含' }, { value: 'NOT_CONTAINS', label: '不包含' },
  { value: 'IN', label: '在…中' },
  { value: 'IS_NULL', label: '为空' }, { value: 'IS_NOT_NULL', label: '不为空' },
]
const datasetAIChangeActionLabels = { ADD: '新增', UPDATE: '修改', REMOVE: '删除' } as const
const datasetAIChangeComponentLabels = { DATASET: '数据集信息', NODE: '数据节点', JOIN: '关联', GROUP: '分组', TRANSFORM: '字段处理', END: '输出' } as const
const datasetAIChangeFieldLabels: Record<string, string> = {
  name: '名称', description: '说明', alias: '别名', tableId: '数据表', selectedColumns: '选择字段',
  left: '左侧输入', right: '右侧输入', joinType: '关联方式', conditions: '关联条件',
  family: '处理分类', componentType: '组件类型', rules: '转换规则',
  input: '上游输入', dimensions: '分组维度', metrics: '汇总指标', outputs: '输出字段',
}
const metricAIAutoRunStoragePrefix = 'dataset-metric-ai-auto:'
const metricAIAutoRunWasConsumed = (key: string) => {
  try { return sessionStorage.getItem(`${metricAIAutoRunStoragePrefix}${key}`) === '1' } catch { return false }
}
const consumeMetricAIAutoRun = (key: string) => {
  try { sessionStorage.setItem(`${metricAIAutoRunStoragePrefix}${key}`, '1') } catch { /* in-memory ref still protects this mount */ }
}
const isTime = (column: AssetColumn) => ['DATE', 'DATETIME', 'TIMESTAMP'].includes(column.canonicalType.toUpperCase()) || column.semanticType.toUpperCase() === 'DATE'
const emptyDraft = (): DatasetDraft => ({ code: '', name: '', description: '', nodes: [], fields: [], joins: [], filters: [], parameters: [], calculations: [], sorts: [], grainDescription: '', grainKeys: [], groupingEnabled: false, finalConfigured: false, finalGroupingEnabled: false })
const editorFingerprint = (snapshot: DatasetEditorSnapshot) => JSON.stringify(snapshot)

type PreviewIssue = { reason: string; suggestion: string }

const datasetAIReasonSuggestion = (reasonCode = '') => {
  const normalized = reasonCode.toUpperCase()
  if (normalized.includes('FIELD')) return '请在要求中写明数据表和精确字段，例如“订单表.ORDER_ID 使用 COUNT”，再根据修改重新生成。'
  if (normalized.includes('JOIN')) return '请明确两张表的左右方向和关联字段，例如“订单表.CUSTOMER_ID = 客户表.customer_id”。'
  if (normalized.includes('GROUP') || normalized.includes('AGGREGATION')) return '请分别写明统计日期、分组维度、统计字段和聚合方式，避免同时输出未分组的明细字段。'
  if (normalized.includes('TRANSFORM')) return '请明确字段处理动作、输入字段和期望产物，例如“将订单日期转换为年月字段，再按年月汇总”。'
  if (normalized.includes('CHANGE_SCOPE')) return '请区分“仅从最终结果隐藏字段”和“取消选择字段”；若只是控制输出，可直接写明“保留上游选列，仅调整最终输出”。'
  if (normalized.includes('OUTPUT') || normalized.includes('END')) return '请明确最终只保留哪些输出字段，并确保这些字段来自结束节点上游实际产生的数据或字段处理产物。'
  if (normalized.includes('DAG') || normalized.includes('TOPOLOGY') || normalized.includes('DISCONNECT')) return '请按“输入表 → 关联 → 汇总 → 最终输出”的顺序描述流程，并避免引用未连接的节点。'
  return '可补充数据表、关联字段、统计日期、分组维度和聚合方式后重新生成，也可以继续手动配置画布。'
}

/** 保留请求错误的稳定元数据，避免只展示无法排查的 message。 */
function datasetAIRequestIssue(cause: unknown, phase: 'GENERATE' | 'APPLY'): DatasetAIErrorView {
  if (!(cause instanceof RequestError)) {
    return {
      title: phase === 'APPLY' ? '方案未能应用' : '方案暂未生成',
      message: cause instanceof Error ? cause.message : phase === 'APPLY' ? 'AI 方案未通过数据集校验，原画布未发生变化' : 'AI 方案生成失败，请稍后重试',
      suggestion: phase === 'APPLY' ? '可以重新应用；若仍失败，请修改要求后重新生成，原画布不会被覆盖。' : '请按原要求重试，或修改上方要求后重新生成。',
    }
  }
  const detail = cause.detail
  const reasonCode = detail.reasonCode || detail.details?.find(item => item.code)?.code
  const invalidOutput = detail.code === 'DATASET_AI_INVALID_OUTPUT'
  return {
    title: invalidOutput
      ? detail.repairAttempted ? '系统已自动修复一次仍失败' : '方案未通过安全校验'
      : phase === 'APPLY' ? '方案未能应用' : '方案暂未生成',
		message: invalidOutput ? detail.message || 'AI 方案仍未通过数据集安全校验，原画布没有发生变化。' : cause.message,
		suggestion: invalidOutput ? detail.suggestion || datasetAIReasonSuggestion(reasonCode) : phase === 'APPLY'
      ? '可以重新应用；若仍失败，请修改要求后重新生成，原画布不会被覆盖。'
      : '请按原要求重试；也可以修改上方要求，补充表、字段和聚合方式后重新生成。',
    code: detail.code,
    reasonCode,
    stage: detail.stage,
    repairAttempted: detail.repairAttempted,
    status: cause.status,
    requestId: detail.requestId,
    diagnosticCode: detail.diagnosticCode,
  }
}

const datasetAILocalIssue = (message: string, suggestion: string): DatasetAIErrorView => ({
  title: '当前操作未完成', message, suggestion,
})

/** 将预览接口的稳定错误码翻译成用户可直接执行的排查动作。 */
function endPreviewIssue(cause: unknown): PreviewIssue {
  const reason = cause instanceof Error ? cause.message : '无法生成结束节点预览'
  if (!(cause instanceof RequestError)) {
    return { reason, suggestion: '请稍后重新打开结束组件；若持续失败，请检查数据集服务与上游数据源状态。' }
  }
  const requestHint = cause.detail.requestId ? ` 排查时可提供请求标识 ${cause.detail.requestId}。` : ''
  switch (cause.detail.code) {
    case 'DSL-002-INVALID-DOCUMENT':
    case 'DATASET_VERSION_UNAVAILABLE':
      return { reason, suggestion: `上游表、字段或数据集版本可能已变化，请重新选择有效字段，检查画布连线并保存配置。${requestHint}` }
    case 'QUERY-001-INVALID-PREVIEW':
      return { reason, suggestion: `请检查结束节点是否已连接完整上游、至少选择一个输出字段，并保存后重新打开。${requestHint}` }
    case 'QUERY-002-UNSUPPORTED-SOURCE':
      return { reason, suggestion: `请检查上游数据源是否已启用，以及当前连接器是否支持该数据源类型。${requestHint}` }
    case 'QUERY-003-TIMEOUT':
      return { reason, suggestion: `请缩小关联或聚合范围、检查过滤条件，或确认上游数据源当前响应正常。${requestHint}` }
    case 'QUERY-004-EXECUTION-FAILED':
      return { reason, suggestion: `请检查上游数据源连通性、访问凭据和物理表字段是否仍然有效。${requestHint}` }
    case 'DATASET_VERSION_CONFLICT':
      return { reason, suggestion: `该数据集已被其他请求更新，请关闭当前编辑页并重新进入后再预览，避免基于过期版本继续修改。${requestHint}` }
    case 'PERMISSION_DENIED':
      return { reason, suggestion: `当前账号缺少上游数据读取权限，请联系管理员授权后重试。${requestHint}` }
    default:
      return { reason, suggestion: `请稍后重新打开结束组件；若持续失败，请检查数据集服务与上游数据源状态。${requestHint}` }
  }
}

function componentPreviewIssue(cause: unknown): PreviewIssue {
  const issue = endPreviewIssue(cause)
  return {
    reason: issue.reason.replaceAll('结束节点', '当前组件'),
    suggestion: issue.suggestion.replaceAll('结束节点', '当前组件').replaceAll('结束组件', '组件'),
  }
}

/**
 * 用水平切线的三次贝塞尔曲线表示从组件输出端口到下游输入端口的数据流。
 * 即使用户把下游拖到上游左侧，曲线也会从输出端向右离开、从输入端左侧进入，
 * 从而始终能辨认首尾方向。midpoint 同时用于把删除按钮放到真实曲线中点。
 */
function curveGeometry(start: CanvasPoint, end: CanvasPoint): CurveGeometry {
  const deltaX = end.x - start.x
  const suggestedTangent = Math.abs(deltaX) * .46 + Math.abs(end.y - start.y) * .12
  // 正向且距离较近时限制切线长度，避免曲线越过端点形成回环；反向布局则保留
  // 足够的外扩空间，让连接仍从输出端右侧离开、从输入端左侧进入。
  const tangent = deltaX >= 0
    ? Math.max(12, Math.min(220, suggestedTangent, deltaX / 2))
    : Math.max(56, Math.min(220, suggestedTangent))
  const control1 = { x: start.x + tangent, y: start.y }
  const control2 = { x: end.x - tangent, y: end.y }
  return {
    path: `M ${start.x} ${start.y} C ${control1.x} ${control1.y}, ${control2.x} ${control2.y}, ${end.x} ${end.y}`,
    midpoint: {
      x: (start.x + 3 * control1.x + 3 * control2.x + end.x) / 8,
      y: (start.y + 3 * control1.y + 3 * control2.y + end.y) / 8,
    },
  }
}


async function loadAllDatasets(): Promise<DatasetSummary[]> {
  const items: DatasetSummary[] = []
  for (let offset = 0; ;) {
    const page = await datasetAPI.list(200, offset)
    items.push(...page.items)
    if (!page.items.length || items.length >= page.total) return items
    offset += page.items.length
  }
}

async function loadAllPublishedVersions(datasetID: string): Promise<PublishedVersionSummary[]> {
  const items: PublishedVersionSummary[] = []
  for (let offset = 0; ;) {
    const page = await datasetAPI.listVersions(datasetID, 200, offset)
    items.push(...page.items)
    if (!page.items.length || items.length >= page.total) return items
    offset += page.items.length
  }
}

async function loadAllTables(): Promise<AssetTable[]> {
  const items: AssetTable[] = []
  for (let offset = 0; ;) {
    const page = await datasetAPI.mappingTables(200, offset)
    items.push(...page.items)
    if (!page.items.length || page.total == null || items.length >= page.total) return items
    offset += page.items.length
  }
}

const nodeFieldCode = (node: DesignerNode, columnName: string, multiple: boolean) => multiple ? `${node.alias}_${columnName}` : columnName
const safeIdentifier = (value: string) => value.trim().replace(/[^A-Za-z0-9_]/g, '_').replace(/^[^A-Za-z]+/, '') || 'field'
const numericCanonicalTypes = new Set(['NUMBER', 'INT', 'INTEGER', 'DECIMAL', 'FLOAT', 'DOUBLE'])
const dateCanonicalTypes = new Set(['DATE', 'DATETIME', 'TIMESTAMP'])
const transformOperations = (transform: Pick<GraphTransform, 'family' | 'componentType'>): GraphTransformOperation[] => {
  if (transform.componentType) return transformComponentDefinition(transform.componentType)?.operations || []
  if (transform.family === 'DATE') return ['DATE_FORMAT']
  if (transform.family === 'TEXT') return ['SUBSTRING', 'TRIM', 'UPPER', 'LOWER', 'REPLACE', 'CONCAT']
  if (transform.family === 'CAST') return ['CAST']
  if (transform.family === 'NUMBER') return ['ROUND', 'ABS', 'FLOOR', 'CEIL', 'ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE']
  if (transform.family === 'CONDITION') return ['CASE']
  if (transform.family === 'NULL') return ['COALESCE']
  return ['CONCAT', 'SUBSTRING']
}
const transformOperationLabel: Record<GraphTransformOperation, string> = {
  DATE_FORMAT: '日期格式化', DATE_TRUNC: '日期归整', CAST: '转换类型', ADD: '相加', SUBTRACT: '相减', MULTIPLY: '相乘', DIVIDE: '相除',
  ROUND: '四舍五入', ABS: '绝对值', FLOOR: '向下取整', CEIL: '向上取整',
  CONCAT: '合并两个字段', COALESCE: '填充空值', CASE: '按条件映射', SUBSTRING: '按位置拆分', TRIM: '去除首尾空格',
  UPPER: '转为大写', LOWER: '转为小写', REPLACE: '文本替换',
}
const transformFieldCandidates = (family: GraphTransformFamily, fields: ProducedField[]) => family === 'DATE'
  ? fields.filter(field => dateCanonicalTypes.has(field.canonicalType.toUpperCase()))
  : family === 'NUMBER' ? fields.filter(field => numericCanonicalTypes.has(field.canonicalType.toUpperCase()))
    : fields
const defaultFallbackValue = (field?: ProducedField) => {
  const type = field?.canonicalType.toUpperCase() || 'STRING'
  if (numericCanonicalTypes.has(type)) return '0'
  if (type === 'BOOLEAN') return 'false'
  if (dateCanonicalTypes.has(type)) return '1970-01-01'
  return '-'
}
const defaultTransformRule = (transform: TransformBox, fields: ProducedField[], index: number): GraphTransformRule => {
  const operation = transformOperations(transform)[0]
  const candidates = transformFieldCandidates(transform.family, fields)
  const first = candidates[0]
  const binary = ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'CONCAT'].includes(operation)
  const suffix: Record<GraphTransformOperation, string> = {
    DATE_FORMAT: dateFormatMeta.DAY.suffix, DATE_TRUNC: 'date', CAST: 'text', ADD: 'calculated', SUBTRACT: 'calculated', MULTIPLY: 'calculated', DIVIDE: 'calculated',
    ROUND: 'rounded', ABS: 'absolute', FLOOR: 'floor', CEIL: 'ceil', CONCAT: 'merged', COALESCE: 'filled', CASE: 'mapped', SUBSTRING: 'substring',
    TRIM: 'trimmed', UPPER: 'uppercase', LOWER: 'lowercase', REPLACE: 'replaced',
  }
  const outputType = transform.family === 'DATE' ? 'STRING'
    : transform.family === 'CAST' ? 'STRING'
      : transform.family === 'NUMBER' ? 'DECIMAL'
        : transform.family === 'NULL' ? first?.canonicalType || 'STRING' : 'STRING'
  const label = transformDisplayLabel(transform)
  return {
    id: `rule_${index}`,
    operation,
    inputKeys: first ? binary ? [first.key, candidates[1]?.key || first.key] : [first.key] : [],
    output: { id: `output_${index}`, name: first ? transform.family === 'DATE' ? `${first.name}${dateFormatMeta.DAY.label}` : `${first.name}${label}结果` : `${label}结果`, code: safeIdentifier(`${first?.code || 'field'}_${suffix[operation]}`), canonicalType: outputType },
    ...(operation === 'DATE_FORMAT' ? { unit: 'DAY' as const } : {}),
    ...(operation === 'CAST' ? { targetType: 'STRING' as const } : {}),
    ...(operation === 'CASE' ? { conditionOperator: 'EQUALS' as const, matchValue: '', thenValue: '', elseValue: '' } : {}),
    ...(operation === 'COALESCE' ? { fallbackMode: 'LITERAL' as const, fallbackValue: defaultFallbackValue(first) } : {}),
    ...(operation === 'CONCAT' ? { separator: '' } : {}),
    ...(operation === 'ROUND' ? { precision: 2 } : {}),
    ...(operation === 'SUBSTRING' ? { start: 1, length: 10 } : {}),
  }
}
const dateFormatOutputForUnit = (rule: GraphTransformRule, field: ProducedField | undefined, unit: DateFormatUnit): GraphTransformRule['output'] => {
  const output = { ...rule.output, canonicalType: 'STRING' }
  if (!field) return output
  const generatedCodes = Object.values(dateFormatMeta).map(meta => safeIdentifier(`${field.code}_${meta.suffix}`))
  const generatedNames = Object.values(dateFormatMeta).map(meta => `${field.name}${meta.label}`)
  const codeIsGenerated = generatedCodes.includes(safeIdentifier(rule.output.code)) || /_(day|date)$/i.test(rule.output.code)
  const nameIsGenerated = generatedNames.includes(rule.output.name) || rule.output.name === `${field.name}日期处理结果`
  if (codeIsGenerated) output.code = safeIdentifier(`${field.code}_${dateFormatMeta[unit].suffix}`)
  if (nameIsGenerated) output.name = `${field.name}${dateFormatMeta[unit].label}`
  return output
}
const transformRuleInputCount = (operation: GraphTransformOperation, fallbackMode?: GraphTransformRule['fallbackMode']) => ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'CONCAT'].includes(operation) || operation === 'COALESCE' && fallbackMode === 'FIELD' ? 2 : 1
const transformRuleIsComplete = (rule: GraphTransformRule) => {
  const inputCount = transformRuleInputCount(rule.operation, rule.fallbackMode)
  const inputsComplete = rule.inputKeys.length >= inputCount && rule.inputKeys.slice(0, inputCount).every(Boolean)
  const unaryCondition = rule.conditionOperator === 'IS_NULL' || rule.conditionOperator === 'IS_NOT_NULL'
  const inCondition = rule.conditionOperator === 'IN'
  const collectionComplete = !inCondition || Boolean(rule.conditionValues?.length && rule.conditionValues.every(item => item.value.trim()))
  const caseComplete = rule.operation !== 'CASE' || ((unaryCondition || inCondition || Boolean(rule.matchValue?.length)) && collectionComplete && rule.thenValue !== undefined && rule.elseValue !== undefined)
  const fallbackComplete = rule.operation !== 'COALESCE' || rule.fallbackMode === 'FIELD' || rule.fallbackValue !== undefined
  const precisionComplete = rule.operation !== 'ROUND' || Number.isInteger(rule.precision) && (rule.precision ?? 0) >= -10 && (rule.precision ?? 0) <= 10
  const substringComplete = rule.operation !== 'SUBSTRING' || Number.isInteger(rule.start) && Number.isInteger(rule.length) && (rule.start ?? 0) >= 1 && (rule.length ?? -1) >= 0
  const replaceComplete = rule.operation !== 'REPLACE' || Boolean(rule.searchValue?.length)
  return inputsComplete && caseComplete && fallbackComplete && precisionComplete && substringComplete && replaceComplete && Boolean(rule.output.name.trim() && rule.output.code.trim() && rule.output.canonicalType.trim())
}
const endOutputFor = (field: ProducedField, previous?: EndBox['outputs'][number]): EndBox['outputs'][number] => {
  const generated = generatedGraphFieldIdentity(field)
  return { key: field.key, name: previous?.name || generated.name, code: previous?.code || generated.code }
}
const fieldOption = (node: DesignerNode, column: AssetColumn): FieldOption => ({
  key: `${node.id}.${column.columnName}`,
  code: nodeFieldCode(node, column.columnName, true),
  name: column.businessName || column.columnName,
  role: isTime(column) ? 'TIME' : column.semanticType === 'IDENTIFIER' ? 'IDENTIFIER' : 'ATTRIBUTE',
  aggregation: '',
  groupBy: false,
  grouping: '',
  output: true,
  metric: false,
  finalOutput: true,
  finalGroupBy: false,
  finalGrouping: '',
  finalMetric: false,
  finalAggregation: '',
})

/** 数据节点只负责字段投影；分组与聚合统一交给独立分组组件。 */
function availableNodeColumns(node: DesignerNode, fields: FieldOption[]): AssetColumn[] {
  const options = new Map(fields.map(field => [field.key, field]))
  return node.columns.filter(column => options.get(`${node.id}.${column.columnName}`)?.output !== false)
}

const nodeLabel = (node: DesignerNode) => `${node.table.businessName || node.table.tableName} (${node.alias})`

const relationInputLabel = (value: RelationInput | undefined, nodes: DesignerNode[], boxes: RelationBox[], groups: GroupBox[], transforms: TransformBox[] = []) => {
  if (!value) return '尚未连接'
  if (value.kind === 'NODE') {
    const node = nodes.find(item => item.id === value.id)
    return node ? `数据节点 · ${nodeLabel(node)}` : '数据节点已失效'
  }
  if (value.kind === 'JOIN') return boxes.find(item => item.id === value.id)?.name || '关联节点已失效'
  if (value.kind === 'TRANSFORM') return transforms.find(item => item.id === value.id)?.name || '字段处理节点已失效'
  return groups.find(item => item.id === value.id)?.name || '分组节点已失效'
}

/** 优先使用同名字段生成关联候选；找不到时保守选择两侧首列并要求用户人工确认。 */
function relationCandidate(left: DesignerNode, right: DesignerNode, index: number, fields: FieldOption[], leftAllowed?: Set<string>, rightAllowed?: Set<string>): JoinOption {
  const leftColumns = availableNodeColumns(left, fields).filter(column => !leftAllowed || leftAllowed.has(`${left.id}.${column.columnName}`))
  const rightColumns = availableNodeColumns(right, fields).filter(column => !rightAllowed || rightAllowed.has(`${right.id}.${column.columnName}`))
  const rightByName = new Map(rightColumns.map(column => [column.columnName.toLocaleLowerCase(), column]))
  const common = leftColumns.find(column => rightByName.has(column.columnName.toLocaleLowerCase()))
  const leftField = common?.columnName ?? leftColumns.find(column => column.semanticType === 'IDENTIFIER')?.columnName ?? leftColumns[0]?.columnName ?? ''
  const rightField = common ? rightByName.get(common.columnName.toLocaleLowerCase())?.columnName ?? '' : rightColumns.find(column => column.semanticType === 'IDENTIFIER')?.columnName ?? rightColumns[0]?.columnName ?? ''
  return {
    id: `join_${index}`, leftNodeId: left.id, rightNodeId: right.id, leftField, rightField,
    joinType: 'LEFT', cardinality: '', manualConfirmed: false,
    conditions: [{ id: `join_${index}_condition_1`, leftField, rightField }],
  }
}

const graphShape = (boxes: RelationBox[], groups: GroupBox[], transforms: TransformBox[] = []) => ({ joins: boxes, groups, transforms })
const relationLeaves = (input: RelationInput | undefined, boxes: RelationBox[], groups: GroupBox[], transforms: TransformBox[] = []) => graphLeaves(input, graphShape(boxes, groups, transforms))
const relationContains = (input: RelationInput, target: RelationInput, boxes: RelationBox[], groups: GroupBox[], transforms: TransformBox[] = []) => graphContains(input, target, graphShape(boxes, groups, transforms))
const relationOutputFields = (input: RelationInput | undefined, boxes: RelationBox[], groups: GroupBox[], nodes: DesignerNode[], fields: FieldOption[], transforms: TransformBox[] = []) => graphProducedFields(input, graphShape(boxes, groups, transforms), nodes, fields)
const relationOutputKeys = (input: RelationInput | undefined, boxes: RelationBox[], groups: GroupBox[], nodes: DesignerNode[], fields: FieldOption[], transforms: TransformBox[] = []) => graphOutputKeys(input, graphShape(boxes, groups, transforms), nodes, fields)
function relationForInputs(leftIDs: string[], rightIDs: string[], nodes: DesignerNode[], fields: FieldOption[], index: number, leftAllowed?: Set<string>, rightAllowed?: Set<string>): JoinOption | null {
  const pairs = leftIDs.flatMap(leftID => rightIDs.map(rightID => ({ left: nodes.find(node => node.id === leftID), right: nodes.find(node => node.id === rightID) })))
    .filter((pair): pair is { left: DesignerNode; right: DesignerNode } => Boolean(pair.left && pair.right))
  const pair = pairs.find(({ left, right }) => {
    const rightNames = new Set(availableNodeColumns(right, fields).filter(column => !rightAllowed || rightAllowed.has(`${right.id}.${column.columnName}`)).map(column => column.columnName.toLocaleLowerCase()))
    return availableNodeColumns(left, fields).filter(column => !leftAllowed || leftAllowed.has(`${left.id}.${column.columnName}`)).some(column => rightNames.has(column.columnName.toLocaleLowerCase()))
  }) ?? pairs[0]
  return pair ? relationCandidate(pair.left, pair.right, index, fields, leftAllowed, rightAllowed) : null
}

const joinConditions = (join: JoinOption) => join.conditions?.length
  ? join.conditions
  : [{ id: `${join.id}_condition_1`, leftField: join.leftField, rightField: join.rightField }]

function firstOutput(nodes: DesignerNode[]): { node: DesignerNode; column: AssetColumn } | null {
  for (const node of nodes) {
    const column = node.columns.find(item => node.selected.includes(item.columnName))
    if (column) return { node, column }
  }
  return null
}

/** 保存前校验关系图连通性，防止看似完成但实际存在孤立表的配置进入 DSL。 */
function isConnected(nodes: DesignerNode[], joins: JoinOption[]): boolean {
  if (nodes.length < 2) return true
  const seen = new Set([nodes[0].id])
  while (true) {
    const size = seen.size
    for (const join of joins) {
      if (seen.has(join.leftNodeId)) seen.add(join.rightNodeId)
      if (seen.has(join.rightNodeId)) seen.add(join.leftNodeId)
    }
    if (seen.size === size) return seen.size === nodes.length
  }
}

function configuredGrainKeys(value: DatasetDraft, end?: EndBox | null): string[] {
  if (end?.outputs.length) return [safeIdentifier(end.outputs[0].code)]
  const options = new Map(value.fields.map(field => [field.key, field]))
  if (value.finalConfigured) {
    const grouped = value.nodes.flatMap(node => node.columns
      .filter(column => options.get(`${node.id}.${column.columnName}`)?.finalGroupBy)
      .map(column => safeIdentifier(options.get(`${node.id}.${column.columnName}`)?.code || nodeFieldCode(node, column.columnName, value.nodes.length > 1))))
    if (grouped.length) return grouped
    const first = value.nodes.flatMap(node => node.columns.map(column => ({ node, column })))
      .find(({ node, column }) => options.get(`${node.id}.${column.columnName}`)?.finalOutput !== false)
    if (!first) return []
    return [safeIdentifier(options.get(`${first.node.id}.${first.column.columnName}`)?.code || nodeFieldCode(first.node, first.column.columnName, value.nodes.length > 1))]
  }
  const grouped = value.nodes.flatMap(node => node.columns
    .filter(column => node.selected.includes(column.columnName))
    .filter(column => options.get(`${node.id}.${column.columnName}`)?.groupBy)
    .map(column => safeIdentifier(options.get(`${node.id}.${column.columnName}`)?.code || nodeFieldCode(node, column.columnName, value.nodes.length > 1))))
  if (grouped.length) return grouped
  const first = value.nodes.flatMap(node => node.columns
    .filter(column => node.selected.includes(column.columnName))
    .map(column => ({ node, column })))
    .find(({ node, column }) => options.get(`${node.id}.${column.columnName}`)?.output !== false) ?? firstOutput(value.nodes)
  if (!first) return []
  const option = options.get(`${first.node.id}.${first.column.columnName}`)
  return [safeIdentifier(option?.code || nodeFieldCode(first.node, first.column.columnName, value.nodes.length > 1))]
}

/** 提供数据集资产目录、筛选、新建配置和完整生命周期操作。 */
export function DatasetCenterPage() {
  const { datasetId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const [datasets, setDatasets] = useState<DatasetSummary[]>([])
  const [tables, setTables] = useState<AssetTable[]>([])
  const [loading, setLoading] = useState(true)
  const [assetsLoading, setAssetsLoading] = useState(false)
  const [keyword, setKeyword] = useState('')
  const [typeFilter, setTypeFilter] = useState('ALL')
  const [statusFilter, setStatusFilter] = useState('ALL')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [dialog, setDialog] = useState<DialogState | null>(null)
  const [draft, setDraft] = useState<DatasetDraft>(emptyDraft)
  const [relationBoxes, setRelationBoxes] = useState<RelationBox[]>([])
  const [groupBoxes, setGroupBoxes] = useState<GroupBox[]>([])
  const [transformBoxes, setTransformBoxes] = useState<TransformBox[]>([])
  const [endBox, setEndBox] = useState<EndBox | null>(null)
  const [nodePreviews, setNodePreviews] = useState<Record<string, NodePreviewState>>({})
  const [nodePositions, setNodePositions] = useState<Record<string, CanvasPoint>>({})
  const [expandedSources, setExpandedSources] = useState<Set<string>>(new Set())
  const [metadata, setMetadata] = useState({ name: '', description: '' })
  const [detail, setDetail] = useState<DatasetRecord | null>(null)
  const [detailPreview, setDetailPreview] = useState<DatasetPreview | null>(null)
  const [detailPreviewError, setDetailPreviewError] = useState('')
  const [publicationRecord, setPublicationRecord] = useState<DatasetRecord | null>(null)
  const [publicationRequests, setPublicationRequests] = useState<DatasetPublicationRequest[]>([])
  const [publicationCapabilities, setPublicationCapabilities] = useState({ manage: false, publish: false })
  const [publicationNote, setPublicationNote] = useState('')
  const [publicationDecisionNote, setPublicationDecisionNote] = useState('')
  const [selectedPublicationRequestID, setSelectedPublicationRequestID] = useState('')
  const [historyRecord, setHistoryRecord] = useState<DatasetRecord | null>(null)
  const [historyItems, setHistoryItems] = useState<PublishedVersionSummary[]>([])
  const [selectedHistoryVersion, setSelectedHistoryVersion] = useState<PublishedVersionRecord | null>(null)
  const [historyPreview, setHistoryPreview] = useState<VersionPreviewState | null>(null)
  const [historyConfirm, setHistoryConfirm] = useState(false)
  const [editingRecord, setEditingRecord] = useState<DatasetRecord | null>(null)
  const [formError, setFormError] = useState('')
  const [busyAction, setBusyAction] = useState('')
  const [generatedCode, setGeneratedCode] = useState('')
  const [activeNodeID, setActiveNodeID] = useState('')
  const [activeJoinID, setActiveJoinID] = useState('')
  const [activeGroupID, setActiveGroupID] = useState('')
  const [activeTransformID, setActiveTransformID] = useState('')
  const [activeEnd, setActiveEnd] = useState(false)
  const [endPreview, setEndPreview] = useState<NodePreviewState>({ loading: false })
  const [componentPreviews, setComponentPreviews] = useState<Record<string, NodePreviewState>>({})
  const [canvasPreviewTarget, setCanvasPreviewTarget] = useState<CanvasPreviewTarget | null>(null)
  const [canvasNotice, setCanvasNotice] = useState('')
  const [canvasFullscreen, setCanvasFullscreen] = useState(false)
  const [aiPrompt, setAIPrompt] = useState('')
  const [aiResult, setAIResult] = useState<DatasetAIPlanResult | null>(null)
  const [aiError, setAIError] = useState<DatasetAIErrorView | null>(null)
  const [aiBusy, setAIBusy] = useState(false)
  const [aiApplying, setAIApplying] = useState(false)
  const [aiApplied, setAIApplied] = useState(false)
  const [aiDetailsExpanded, setAIDetailsExpanded] = useState(true)
  const [aiUndo, setAIUndo] = useState<DatasetAIUndo | null>(null)
  const [aiReviewLabels, setAIReviewLabels] = useState<DatasetAIReviewLabels>({ nodes: {}, fields: {} })
  const [aiRetryAction, setAIRetryAction] = useState<DatasetAIRetryAction>(null)
  const [aiLastInstruction, setAILastInstruction] = useState('')
  const [aiPlanHints, setAIPlanHints] = useState<DatasetAIPlanHints | undefined>()
  const [pendingMetricAIAutoRun, setPendingMetricAIAutoRun] = useState<PendingMetricAIAutoRun | null>(null)
  const canvasFullscreenTarget = useRef<HTMLElement | null>(null)
  const historySelectionRequest = useRef(0)
  const endPreviewRequest = useRef(0)
  const componentPreviewRequests = useRef<Record<string, number>>({})
  const openedRouteDatasetID = useRef('')
  const aiRequest = useRef(0)
  const aiApplyRequest = useRef(0)
  const editorFingerprintRef = useRef('')
  const lastEditorFingerprintRef = useRef('')
  const metricAIAutoRunKeys = useRef(new Set<string>())
  const autoGenerateDatasetAIPlan = useRef<(instruction: string) => void>(() => undefined)

  const loadDatasets = useCallback(async () => {
    setDatasets(await loadAllDatasets())
  }, [])

  useEffect(() => {
    let active = true
    loadAllDatasets().then(items => { if (active) setDatasets(items) }).catch(cause => {
      if (active) setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载数据集失败' })
    }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [])

  useEffect(() => {
    if (!notice) return
    const timer = window.setTimeout(() => setNotice(null), 4500)
    return () => window.clearTimeout(timer)
  }, [notice])

  useEffect(() => {
    const syncFullscreen = () => setCanvasFullscreen(document.fullscreenElement === canvasFullscreenTarget.current)
    document.addEventListener('fullscreenchange', syncFullscreen)
    return () => document.removeEventListener('fullscreenchange', syncFullscreen)
  }, [])

  const sourceGroups = useMemo(() => {
    const groups = new Map<string, { id: string; name: string; type: string; tables: AssetTable[] }>()
    for (const table of tables) {
      const group = groups.get(table.dataSourceId) ?? { id: table.dataSourceId, name: table.dataSourceName, type: table.dataSourceType, tables: [] }
      group.tables.push(table)
      groups.set(table.dataSourceId, group)
    }
    return [...groups.values()]
  }, [tables])

  const filtered = useMemo(() => {
    const query = keyword.trim().toLocaleLowerCase()
    return datasets.filter(dataset => (!query || dataset.name.toLocaleLowerCase().includes(query) || dataset.code.toLocaleLowerCase().includes(query)) &&
      (typeFilter === 'ALL' || dataset.type === typeFilter) && (statusFilter === 'ALL' || dataset.status === statusFilter))
  }, [datasets, keyword, statusFilter, typeFilter])

  const selectedPublicationRequest = publicationRequests.find(item => item.id === selectedPublicationRequestID) ?? null
  const currentDraftPublicationRequest = publicationRecord
    ? publicationRequests.find(item => item.draftVersionId === publicationRecord.draftVersionId &&
      item.expectedDraftRecordVersion === publicationRecord.draftRecordVersion) ?? null
    : null
  const metricFlowState = location.state as {
    returnTo?: unknown
    metricAIRequirement?: unknown
    preferredDatasetId?: unknown
    safeDatasetExtension?: unknown
  } | null
  const metricReturnTo = typeof metricFlowState?.returnTo === 'string' && metricFlowState.returnTo.startsWith('/')
    ? metricFlowState.returnTo
    : ''
  const metricAIRequirement = typeof metricFlowState?.metricAIRequirement === 'string' ? metricFlowState.metricAIRequirement : ''
  const metricPreferredDatasetId = typeof metricFlowState?.preferredDatasetId === 'string' ? metricFlowState.preferredDatasetId : ''
  const metricSafeDatasetExtension = metricFlowState?.safeDatasetExtension === true && Boolean(metricPreferredDatasetId)

  const currentEditorSnapshot = useMemo<DatasetEditorSnapshot>(() => ({
    draft, relationBoxes, groupBoxes, transformBoxes, endBox, nodePositions, metadata,
  }), [draft, endBox, groupBoxes, metadata, nodePositions, relationBoxes, transformBoxes])
  const currentEditorFingerprint = useMemo(() => editorFingerprint(currentEditorSnapshot), [currentEditorSnapshot])
  const currentDesignerGraph = useMemo<DesignerGraphV1>(() => ({
    version: '1.0', nodePositions,
    nodeNames: Object.fromEntries(draft.nodes.map(node => [node.id, node.table.businessName || node.table.tableName])),
    joins: relationBoxes, groups: groupBoxes, transforms: transformBoxes, ...(endBox ? { end: endBox } : {}),
  }), [draft.nodes, endBox, groupBoxes, nodePositions, relationBoxes, transformBoxes])
  // Async AI responses compare against the latest render, not the closure that started them.
  editorFingerprintRef.current = currentEditorFingerprint

  const activeNode = draft.nodes.find(node => node.id === activeNodeID)
  const activeJoin = draft.joins.find(join => join.id === activeJoinID)
  const activeRelationBox = relationBoxes.find(box => box.id === activeJoinID)
  const activeGroup = groupBoxes.find(group => group.id === activeGroupID)
  const activeTransform = transformBoxes.find(transform => transform.id === activeTransformID)
  const activeLeftOutputFields = relationOutputFields(activeRelationBox?.left, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
  const activeRightOutputFields = relationOutputFields(activeRelationBox?.right, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
  const groupInputFields = relationOutputFields(activeGroup?.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
  const transformInputFields = relationOutputFields(activeTransform?.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
  const endInputFields = relationOutputFields(endBox?.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)

  const completedEditorDraft = useMemo(() => ({
    ...draft,
    code: generatedCode,
    name: metadata.name.trim(),
    description: metadata.description.trim(),
    grainKeys: configuredGrainKeys(draft, endBox),
    designer: serializeDesignerGraph(currentDesignerGraph),
    preAggregation: undefined,
    finalOutputKeys: undefined,
  }), [currentDesignerGraph, draft, endBox, generatedCode, metadata])

  const resetDatasetAI = useCallback(() => {
    aiRequest.current += 1
    aiApplyRequest.current += 1
    setAIPrompt('')
    setAIResult(null)
    setAIError(null)
    setAIBusy(false)
    setAIApplying(false)
    setAIApplied(false)
    setAIDetailsExpanded(true)
    setAIUndo(null)
    setAIReviewLabels({ nodes: {}, fields: {} })
    setAIRetryAction(null)
    setAILastInstruction('')
    setAIPlanHints(undefined)
    setPendingMetricAIAutoRun(null)
  }, [])

  const openCreate = async (metricAIInstruction = '', metricAIAutoRunKey = '', metricAIHints?: DatasetAIPlanHints) => {
    resetDatasetAI()
    setAIPlanHints(metricAIHints)
    endPreviewRequest.current += 1
    setEditingRecord(null)
    setDraft(emptyDraft())
    setRelationBoxes([])
    setGroupBoxes([])
    setTransformBoxes([])
    setEndBox(null)
    setNodePreviews({})
    setNodePositions({})
    setMetadata({ name: '', description: '' })
    setGeneratedCode(`dataset_${Date.now().toString(36)}`)
    setActiveNodeID('')
    setActiveJoinID('')
    setActiveGroupID('')
    setActiveTransformID('')
    setActiveEnd(false)
    setEndPreview({ loading: false })
    setComponentPreviews({})
    setCanvasPreviewTarget(null)
    setCanvasNotice('')
    setCanvasFullscreen(false)
    setFormError('')
    setDialog({ mode: 'create' })
    if (metricAIInstruction.trim()) {
      const instruction = metricAIInstruction.trim().slice(0, 4000)
      setAIPrompt(instruction)
      setCanvasNotice('已从指标提案带入新数据集构建目标，正在自动生成 AI 画布方案。')
      if (metricAIAutoRunKey) setPendingMetricAIAutoRun({ key: metricAIAutoRunKey, instruction })
    }
    if (tables.length) return
    setAssetsLoading(true)
    try {
      const items = await loadAllTables()
      setTables(items)
      setExpandedSources(new Set(items.slice(0, 1).map(table => table.dataSourceId)))
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '加载资产模板失败')
    } finally {
      setAssetsLoading(false)
    }
  }

  const openEdit = useCallback(async (dataset: DatasetSummary | string, metricAIInstruction = '', metricAIAutoRunKey = '', metricAIHints?: DatasetAIPlanHints) => {
    resetDatasetAI()
    setAIPlanHints(metricAIHints)
    endPreviewRequest.current += 1
    const id = typeof dataset === 'string' ? dataset : dataset.id
    setEditingRecord(null)
    setDraft(emptyDraft())
    setRelationBoxes([])
    setGroupBoxes([])
    setTransformBoxes([])
    setEndBox(null)
    setActiveNodeID('')
    setActiveJoinID('')
    setActiveGroupID('')
    setActiveTransformID('')
    setActiveEnd(false)
    setEndPreview({ loading: false })
    setComponentPreviews({})
    setCanvasPreviewTarget(null)
    setNodePreviews({})
    setCanvasNotice('')
    setCanvasFullscreen(false)
    setFormError('')
    setDialog({ mode: 'create', dataset: typeof dataset === 'string' ? undefined : dataset })
    setAssetsLoading(true)
    setBusyAction(`edit:${id}`)
    try {
      const [record, availableTables] = await Promise.all([
        datasetAPI.get(id),
        tables.length ? Promise.resolve(tables) : loadAllTables(),
      ])
      const hydrated = await hydrateDatasetDraft(record, availableTables)
      const graph = (hydrated as DatasetDraft & { designer?: DesignerGraphV1 }).designer ?? hydrateDesignerGraph(record.dsl, hydrated.nodes, hydrated.joins, hydrated.fields)
      const loadedMetadata = { name: record.name, description: record.description }
      setTables(availableTables)
      setExpandedSources(new Set(hydrated.nodes.map(node => node.table.dataSourceId)))
      setDraft(hydrated)
      setRelationBoxes(graph.joins)
      setGroupBoxes(graph.groups)
      setTransformBoxes(graph.transforms ?? [])
      setEndBox(graph.end ?? null)
      setNodePositions(graph.nodePositions)
      setMetadata(loadedMetadata)
      setGeneratedCode(record.code)
      setEditingRecord(record)
      if (metricAIInstruction.trim()) {
        const instruction = metricAIInstruction.trim().slice(0, 4000)
        setAIPrompt(instruction)
        setCanvasNotice('已从指标提案带入数据集改造目标，正在自动生成 AI 画布方案。')
        if (metricAIAutoRunKey) setPendingMetricAIAutoRun({ key: metricAIAutoRunKey, instruction })
      }
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '加载数据集配置失败')
    } finally {
      setAssetsLoading(false)
      setBusyAction('')
    }
  }, [resetDatasetAI, tables])

  const loadNodePreview = useCallback(async (node: DesignerNode) => {
    setNodePreviews(current => ({ ...current, [node.id]: { loading: true } }))
    try {
      const data = await datasetAPI.tablePreview(node.table.id, 5)
      setNodePreviews(current => ({ ...current, [node.id]: { loading: false, data } }))
    } catch (cause) {
      setNodePreviews(current => ({ ...current, [node.id]: { loading: false, error: cause instanceof Error ? cause.message : '加载数据预览失败' } }))
    }
  }, [])

  const openNodeConfig = (nodeID: string) => {
    setActiveJoinID('')
    setActiveGroupID('')
    setActiveTransformID('')
    setActiveEnd(false)
    setActiveNodeID(nodeID)
    setCanvasNotice('')
  }

  useEffect(() => {
    if (!datasetId) {
      openedRouteDatasetID.current = ''
      return
    }
    const routeKey = `${datasetId}:${location.key}`
    if (openedRouteDatasetID.current === routeKey) return
    openedRouteDatasetID.current = routeKey
    if (datasetId === 'new') {
      const state = location.state as { metricAIInstruction?: unknown; metricAIHints?: unknown } | null
      const metricAIInstruction = typeof state?.metricAIInstruction === 'string' ? state.metricAIInstruction : ''
      const metricAIHints = normalizeDatasetAIPlanHints(state?.metricAIHints)
      const autoRunKey = metricAIInstruction.trim() ? `metric-ai:${routeKey}` : ''
      const pendingInstruction = autoRunKey && !metricAIAutoRunWasConsumed(autoRunKey) ? metricAIInstruction : ''
      queueMicrotask(() => void openCreate(pendingInstruction, pendingInstruction ? autoRunKey : '', metricAIHints))
      return
    }
    const state = location.state as { metricAIInstruction?: unknown; metricAIHints?: unknown } | null
    const metricAIInstruction = typeof state?.metricAIInstruction === 'string' ? state.metricAIInstruction : ''
    const metricAIHints = normalizeDatasetAIPlanHints(state?.metricAIHints)
    const autoRunKey = metricAIInstruction.trim() ? `metric-ai:${routeKey}` : ''
    const pendingInstruction = autoRunKey && !metricAIAutoRunWasConsumed(autoRunKey) ? metricAIInstruction : ''
    queueMicrotask(() => void openEdit(datasetId, pendingInstruction, pendingInstruction ? autoRunKey : '', metricAIHints))
    // 路由参数是唯一触发源；打开动作内部会更新表资产状态，不能反向重复触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [datasetId, location.key, location.state, openEdit])

  const selectTable = async (table: AssetTable, position?: CanvasPoint) => {
    const nextNumber = draft.nodes.reduce((largest, node) => Math.max(largest, Number(node.id.replace('node_', '')) || 0), 0) + 1
    const nodeID = `node_${nextNumber}`
    setBusyAction(`asset:${table.id}`)
    setFormError('')
    try {
      // 数据集只允许引用当前有效字段；资产接口中的失效字段只用于历史审计。
      const columns = (await datasetAPI.columns(table.id)).items.filter(column => !column.assetStatus || column.assetStatus === 'ACTIVE')
      if (!columns.length) throw new Error('该数据表没有可用字段')
      setDraft(current => {
        // 同一物理表可作为不同业务角色多次引用，每次保留独立节点与别名。
        const node: DesignerNode = { id: nodeID, alias: `t${nextNumber}`, table, columns, selected: columns.map(column => column.columnName), groupingEnabled: false }
        const nodes = [...current.nodes, node]
        const fields = [...current.fields, ...columns.map(column => fieldOption(node, column))]
        const grain = firstOutput(nodes)
        return {
          ...current, nodes, fields,
          grainDescription: current.grainDescription || (grain ? `每一行代表一个${grain.column.businessName || grain.column.columnName}` : ''),
          grainKeys: grain ? [nodeFieldCode(grain.node, grain.column.columnName, nodes.length > 1)] : [],
        }
      })
      setActiveNodeID(nodeID)
      setActiveJoinID('')
      setActiveGroupID('')
      setActiveTransformID('')
      setActiveEnd(false)
      setNodePositions(current => ({ ...current, [nodeID]: position ?? { x: 42 + (nextNumber - 1) % 2 * 240, y: 58 + Math.floor((nextNumber - 1) / 2) * 145 } }))
      if (!draft.nodes.length) {
        setEndBox(current => ({
          id: 'end_1', name: current?.name || '最终输出', input: { kind: 'NODE', id: nodeID }, position: current?.position ?? { x: 382, y: 58 },
          outputs: columns.map(column => ({ key: `${nodeID}.${column.columnName}`, name: column.businessName || column.columnName, code: safeIdentifier(column.columnName) })),
        }))
      }
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '填充资产模板失败')
    } finally {
      setBusyAction('')
    }
  }

  const removeNode = (nodeID: string) => setDraft(current => {
    const nodes = current.nodes.filter(node => node.id !== nodeID)
    const grain = firstOutput(nodes)
    setRelationBoxes(boxes => boxes.map(box => ({
      ...box,
      left: relationLeaves(box.left, boxes, groupBoxes, transformBoxes).includes(nodeID) ? undefined : box.left,
      right: relationLeaves(box.right, boxes, groupBoxes, transformBoxes).includes(nodeID) ? undefined : box.right,
    })))
    setGroupBoxes(groups => groups.map(group => relationLeaves(group.input, relationBoxes, groups, transformBoxes).includes(nodeID) ? { ...group, input: undefined, dimensions: [], metrics: [] } : group))
    setTransformBoxes(transforms => transforms.map(transform => relationLeaves(transform.input, relationBoxes, groupBoxes, transforms).includes(nodeID) ? { ...transform, input: undefined, rules: [] } : transform))
    setEndBox(value => value && relationLeaves(value.input, relationBoxes, groupBoxes, transformBoxes).includes(nodeID) ? { ...value, input: undefined, outputs: [] } : value)
    setNodePositions(positions => Object.fromEntries(Object.entries(positions).filter(([id]) => id !== nodeID)))
    setNodePreviews(previews => Object.fromEntries(Object.entries(previews).filter(([id]) => id !== nodeID)))
    return {
      ...current, nodes, fields: current.fields.filter(field => !field.key.startsWith(`${nodeID}.`)), joins: current.joins.filter(join => join.leftNodeId !== nodeID && join.rightNodeId !== nodeID),
      calculations: current.calculations.filter(item => !item.leftKey.startsWith(`${nodeID}.`) && !item.rightKey.startsWith(`${nodeID}.`)),
      grainKeys: grain ? [nodeFieldCode(grain.node, grain.column.columnName, nodes.length > 1)] : [],
      grainDescription: grain ? `每一行代表一个${grain.column.businessName || grain.column.columnName}` : '',
    }
  })

  const updateJoin = (joinID: string, patch: Partial<JoinOption>) => setDraft(current => ({
    ...current, joins: current.joins.map(join => join.id === joinID ? { ...join, ...patch } : join),
  }))

  const addRelationBox = (position?: CanvasPoint) => {
    const largest = relationBoxes.reduce((value, box) => Math.max(value, Number(box.id.replace('join_', '')) || 0), draft.joins.length)
    const id = `join_${largest + 1}`
    setRelationBoxes(current => [...current, { id, name: `关联结果 ${largest + 1}`, position: position ?? { x: 510 + current.length * 250, y: 150 + (current.length % 2) * 155 }, outputKeys: [] }])
    setActiveNodeID('')
    setActiveGroupID('')
    setActiveTransformID('')
    setActiveEnd(false)
    setActiveJoinID(id)
    setCanvasNotice('关联组件已加入画布，请配置槽位 1 和槽位 2')
  }

  const dropRelationInput = (boxID: string, side: 'left' | 'right', input?: RelationInput) => setRelationBoxes(current => {
    const target = current.find(box => box.id === boxID)
    const inputGroup = input?.kind === 'GROUP' ? groupBoxes.find(group => group.id === input.id) : undefined
    const groupInput = inputGroup?.input
    const groupCanFeedJoin = Boolean(groupInput
      && relationLeaves(groupInput, current, groupBoxes, transformBoxes).length === 1
      && !current.some(join => relationContains(groupInput, { kind: 'JOIN', id: join.id }, current, groupBoxes, transformBoxes))
      && !groupBoxes.some(group => group.id !== inputGroup?.id && relationContains(groupInput, { kind: 'GROUP', id: group.id }, current, groupBoxes, transformBoxes)))
    if (input) {
      const graph: DesignerGraphV1 = {
        version: '1.0', nodePositions, nodeNames: Object.fromEntries(draft.nodes.map(node => [node.id, nodeLabel(node)])),
        joins: current, groups: groupBoxes, transforms: transformBoxes, ...(endBox ? { end: endBox } : {}),
      }
      const connectionError = graphConnectionError(input, { kind: 'JOIN', id: boxID }, graph, draft.nodes.map(node => node.id))
      if (connectionError) { setFormError(connectionError); return current }
    }
    if (!target || (input && ((input.kind === 'JOIN' && !current.some(box => box.id === input.id)) || (input.kind === 'GROUP' && !groupCanFeedJoin) || relationContains(input, { kind: 'JOIN', id: boxID }, current, groupBoxes, transformBoxes)))) {
      if (inputGroup && !groupCanFeedJoin) setFormError('关联前分组只能接收单个数据节点及其字段处理链；包含关联或其他分组的产物不能再次作为关联槽位输入')
      return current
    }
    const next = current.map(box => box.id === boxID ? { ...box, [side]: input, outputKeys: [] } : box)
    const changed = next.find(box => box.id === boxID)
    const leftIDs = relationLeaves(changed?.left, next, groupBoxes, transformBoxes), rightIDs = relationLeaves(changed?.right, next, groupBoxes, transformBoxes)
    if (leftIDs.some(id => rightIDs.includes(id))) {
      setFormError('同一个表节点不能同时放入关联框两侧；需要重复使用时请从左侧再次引入该表')
      return current
    }
    setFormError('')
    setDraft(draftValue => {
      const without = draftValue.joins.filter(join => join.id !== boxID)
      if (!leftIDs.length || !rightIDs.length) return { ...draftValue, joins: without }
      const leftAllowed = new Set(relationOutputKeys(changed?.left, next, groupBoxes, draftValue.nodes, draftValue.fields, transformBoxes))
      const rightAllowed = new Set(relationOutputKeys(changed?.right, next, groupBoxes, draftValue.nodes, draftValue.fields, transformBoxes))
      const candidate = relationForInputs(leftIDs, rightIDs, draftValue.nodes, draftValue.fields, without.length + 1, leftAllowed, rightAllowed)
      if (!candidate) return draftValue
      // Join 数组同时承担关系树的稳定合并顺序；修改下层关联框时仍按画板层级排序，
      // 避免回载后把父子关系重建成另一棵树。
      const joinsByID = new Map([...without, { ...candidate, id: boxID }].map(join => [join.id, join]))
      return { ...draftValue, joins: next.flatMap(box => joinsByID.has(box.id) ? [joinsByID.get(box.id)!] : []) }
    })
    if (leftIDs.length && rightIDs.length) {
      const joinInput: RelationInput = { kind: 'JOIN', id: boxID }
      const outputs = relationOutputFields(joinInput, next, groupBoxes, draft.nodes, draft.fields, transformBoxes)
      setEndBox(value => {
        if (!(value?.input?.kind === 'JOIN' && value.input.id === boxID)) return value
        return { ...value, outputs: outputs.map(field => endOutputFor(field, value.outputs.find(item => item.key === field.key))) }
      })
    }
    return next
  })

  const updateCanvasPosition = (kind: CanvasComponentKind, id: string, position: CanvasPoint) => {
    const safePosition = { x: Math.max(16, position.x), y: Math.max(20, position.y) }
    if (kind === 'NODE') setNodePositions(current => ({ ...current, [id]: safePosition }))
    else if (kind === 'JOIN') setRelationBoxes(current => current.map(box => box.id === id ? { ...box, position: safePosition } : box))
    else if (kind === 'GROUP') setGroupBoxes(current => current.map(group => group.id === id ? { ...group, position: safePosition } : group))
    else if (kind === 'TRANSFORM') setTransformBoxes(current => current.map(transform => transform.id === id ? { ...transform, position: safePosition } : transform))
    else setEndBox(current => current?.id === id ? { ...current, position: safePosition } : current)
  }

  const arrangeCanvas = () => {
    const layout = layoutDesignerGraph({
      version: '1.0', nodePositions, nodeNames: Object.fromEntries(draft.nodes.map(node => [node.id, node.table.businessName || node.table.tableName])),
      joins: relationBoxes, groups: groupBoxes, transforms: transformBoxes, ...(endBox ? { end: endBox } : {}),
    }, draft.nodes.map(node => node.id))
    setNodePositions(layout.nodePositions)
    setRelationBoxes(layout.joins)
    setGroupBoxes(layout.groups)
    setTransformBoxes(layout.transforms ?? [])
    setEndBox(layout.end ?? null)
    setCanvasNotice('已按数据流层级整理组件')
  }

  const toggleCanvasFullscreen = async () => {
    const target = canvasFullscreenTarget.current
    if (!target) return
    try {
      if (document.fullscreenElement === target) {
        await document.exitFullscreen()
      } else if (target.requestFullscreen) {
        await target.requestFullscreen()
      } else {
        // 测试环境和少数内嵌浏览器没有原生 Fullscreen API，使用同等的视口覆盖样式。
        setCanvasFullscreen(current => !current)
      }
    } catch {
      setCanvasNotice('浏览器未允许进入全屏，请重试')
    }
  }

  const updateRelationOutput = (boxID: string, key: string, checked: boolean) => setRelationBoxes(current => {
    const box = current.find(item => item.id === boxID)
    if (!box) return current
    const withoutSelection = current.map(item => item.id === boxID ? { ...item, outputKeys: [] } : item)
    const available = relationOutputKeys({ kind: 'JOIN', id: boxID }, withoutSelection, groupBoxes, draft.nodes, draft.fields)
    const selected = new Set(box.outputKeys.length ? box.outputKeys : available)
    if (checked) selected.add(key); else selected.delete(key)
    const next = current.map(item => item.id === boxID ? { ...item, outputKeys: [...selected] } : item)
      const changedInput: RelationInput = { kind: 'JOIN', id: boxID }
      const downstream = new Set(current.filter(candidate => candidate.id !== boxID && [candidate.left, candidate.right].some(input => input && relationContains(input, changedInput, current, groupBoxes))).map(candidate => candidate.id))
      setDraft(value => ({ ...value, joins: value.joins.map(join => downstream.has(join.id) ? { ...join, manualConfirmed: false } : join) }))
    if (endBox?.input?.kind === 'JOIN' && endBox.input.id === boxID) {
      const produced = relationOutputFields(endBox.input, next, groupBoxes, draft.nodes, draft.fields)
      setEndBox(value => value ? { ...value, outputs: produced.filter(field => selected.has(field.key)).map(field => endOutputFor(field, value.outputs.find(item => item.key === field.key))) } : value)
    }
    return next
  })

  const removeRelationBox = (boxID: string) => {
    setRelationBoxes(current => current.filter(box => box.id !== boxID).map(box => ({
      ...box,
      left: box.left?.kind === 'JOIN' && box.left.id === boxID ? undefined : box.left,
      right: box.right?.kind === 'JOIN' && box.right.id === boxID ? undefined : box.right,
    })))
    setDraft(current => ({ ...current, joins: current.joins.filter(join => join.id !== boxID) }))
    setGroupBoxes(current => current.map(group => group.input?.kind === 'JOIN' && group.input.id === boxID ? { ...group, input: undefined, dimensions: [], metrics: [] } : group))
    setTransformBoxes(current => current.map(transform => transform.input?.kind === 'JOIN' && transform.input.id === boxID ? { ...transform, input: undefined, rules: [] } : transform))
    setEndBox(current => current?.input && relationContains(current.input, { kind: 'JOIN', id: boxID }, relationBoxes, groupBoxes, transformBoxes) ? { ...current, input: undefined, outputs: [] } : current)
    if (activeJoinID === boxID) setActiveJoinID('')
  }

  const updateOutputField = (key: string, patch: Partial<FieldOption>) => {
    const nodeID = key.split('.')[0]
    const columnName = key.slice(nodeID.length + 1)
    setDraft(current => ({
      ...current,
      // 数据节点的字段勾选同时写入 DSL node.projection；否则最终分组只保存最终字段，
      // 重新打开时无法还原节点真正对下游开放的字段。
      nodes: patch.output === undefined ? current.nodes : current.nodes.map(node => node.id !== nodeID ? node : {
        ...node,
        selected: patch.output
          ? [...new Set([...node.selected, columnName])]
          : node.selected.filter(item => item !== columnName),
      }),
      fields: current.fields.map(field => field.key === key ? { ...field, ...patch } : field),
      joins: current.joins.map(join => join.leftNodeId === nodeID || join.rightNodeId === nodeID ? { ...join, manualConfirmed: false } : join),
    }))
    // 直接单表数据流中，数据节点取消投影后该字段已经不再是结束节点的合法
    // 上游产物；同步移除，避免用户保存时才收到“输出字段已不可用”。
    if (patch.output === false) {
      setEndBox(current => current?.input?.kind === 'NODE' && current.input.id === nodeID
        ? { ...current, outputs: current.outputs.filter(output => output.key !== key) }
        : current)
    }
  }

  const joinConditions = (join: JoinOption) => join.conditions?.length
    ? join.conditions
    : [{ id: `${join.id}_condition_1`, leftField: join.leftField, rightField: join.rightField }]

  const updateJoinCondition = (joinID: string, conditionID: string, patch: { leftField?: string; rightField?: string }) => setDraft(current => ({
    ...current,
    joins: current.joins.map(join => {
      if (join.id !== joinID) return join
      const conditions = joinConditions(join).map(condition => condition.id === conditionID ? { ...condition, ...patch } : condition)
      return { ...join, conditions, leftField: conditions[0]?.leftField ?? '', rightField: conditions[0]?.rightField ?? '', manualConfirmed: false }
    }),
  }))

  const addJoinCondition = (joinID: string) => setDraft(current => ({
    ...current,
    joins: current.joins.map(join => {
      if (join.id !== joinID) return join
      const left = current.nodes.find(node => node.id === join.leftNodeId)
      const right = current.nodes.find(node => node.id === join.rightNodeId)
      const box = relationBoxes.find(item => item.id === joinID)
      const leftAllowed = new Set(relationOutputKeys(box?.left, relationBoxes, groupBoxes, current.nodes, current.fields))
      const rightAllowed = new Set(relationOutputKeys(box?.right, relationBoxes, groupBoxes, current.nodes, current.fields))
      const conditions = joinConditions(join)
      return { ...join, conditions: [...conditions, { id: `${join.id}_condition_${Date.now().toString(36)}`, leftField: left ? availableNodeColumns(left, current.fields).find(column => leftAllowed.has(`${left.id}.${column.columnName}`))?.columnName ?? '' : '', rightField: right ? availableNodeColumns(right, current.fields).find(column => rightAllowed.has(`${right.id}.${column.columnName}`))?.columnName ?? '' : '' }], manualConfirmed: false }
    }),
  }))

  const addGroupBox = (position?: CanvasPoint) => {
    const nextNumber = groupBoxes.reduce((largest, group) => Math.max(largest, Number(group.id.replace('group_', '')) || 0), 0) + 1
    const id = `group_${nextNumber}`
    const group: GroupBox = { id, name: `分组结果 ${nextNumber}`, position: position ?? { x: 420 + (nextNumber - 1) * 80, y: 165 + (nextNumber - 1) * 145 }, dimensions: [], metrics: [] }
    setGroupBoxes(current => [...current, group])
    setActiveNodeID('')
    setActiveJoinID('')
    setActiveTransformID('')
    setActiveGroupID(id)
    setActiveEnd(false)
    setCanvasNotice('分组组件已加入画布，请从上游组件手动连线')
  }

  const connectGroupInput = (groupID: string, input?: RelationInput) => {
    if (input) {
      const graph: DesignerGraphV1 = {
        version: '1.0', nodePositions, nodeNames: Object.fromEntries(draft.nodes.map(node => [node.id, nodeLabel(node)])),
        joins: relationBoxes, groups: groupBoxes, transforms: transformBoxes, ...(endBox ? { end: endBox } : {}),
      }
      const connectionError = graphConnectionError(input, { kind: 'GROUP', id: groupID }, graph, draft.nodes.map(node => node.id))
      if (connectionError) { setFormError(connectionError); return }
    }
    if (input?.kind === 'GROUP') { setFormError('分组组件不能直接串联另一个分组组件'); return }
    if (input?.kind === 'NODE' && groupBoxes.some(group => group.id !== groupID && group.input?.kind === 'NODE' && group.input.id === input.id)) {
      setFormError('同一数据节点只能进入一个分组组件；需要不同口径时请再次引入该数据表')
      return
    }
    const available = new Set(relationOutputKeys(input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes))
    setGroupBoxes(current => current.map(group => group.id === groupID ? {
      ...group, input,
      dimensions: group.dimensions.filter(field => available.has(field.key)),
      metrics: group.metrics.filter(field => available.has(field.key)),
    } : group))
    setDraft(current => ({ ...current, joins: current.joins.map(join => relationBoxes.some(box => box.id === join.id && (box.left?.id === groupID || box.right?.id === groupID)) ? { ...join, manualConfirmed: false } : join) }))
    setFormError('')
  }

  const updateGroupName = (groupID: string, name: string) => {
    setGroupBoxes(current => current.map(group => group.id === groupID ? { ...group, name } : group))
    setFormError('')
  }

  const commitGroupFields = (groupID: string, transform: (group: GroupBox) => GroupBox) => {
    const next = groupBoxes.map(group => group.id === groupID ? transform(group) : group)
    setGroupBoxes(next)
    setFormError('')
    if (endBox?.input && relationContains(endBox.input, { kind: 'GROUP', id: groupID }, relationBoxes, next, transformBoxes)) {
      const produced = relationOutputFields(endBox.input, relationBoxes, next, draft.nodes, draft.fields, transformBoxes)
      setEndBox(current => current ? { ...current, outputs: produced.map(field => endOutputFor(field, current.outputs.find(item => item.key === field.key))) } : current)
    }
  }

  const updateGroupDimension = (groupID: string, field: ProducedField, enabled: boolean) => commitGroupFields(groupID, group => {
    const existing = group.dimensions.find(item => item.key === field.key)
    const generated = generatedGraphFieldIdentity(field)
    const dimensions = enabled
      ? [...group.dimensions.filter(item => item.key !== field.key), { key: field.key, name: existing?.name || generated.name, code: existing?.code || generated.code }]
      : group.dimensions.filter(item => item.key !== field.key)
    return { ...group, dimensions, metrics: enabled ? group.metrics.filter(item => item.key !== field.key) : group.metrics }
  })

  const updateGroupMetric = (groupID: string, field: ProducedField, enabled: boolean, patch: { aggregation?: string } = {}) => commitGroupFields(groupID, group => {
    const existing = group.metrics.find(item => item.key === field.key)
    const generated = generatedGraphFieldIdentity(field)
    const aggregation = patch.aggregation ?? existing?.aggregation ?? ''
    const metrics = enabled
      ? [...group.metrics.filter(item => item.key !== field.key), { key: field.key, name: existing?.name || generated.name, code: existing?.code || generated.code, aggregation }]
      : group.metrics.filter(item => item.key !== field.key)
    return { ...group, metrics, dimensions: enabled ? group.dimensions.filter(item => item.key !== field.key) : group.dimensions }
  })

  const removeGroupBox = (groupID: string) => {
    const consumers = new Set(relationBoxes.filter(box => box.left?.id === groupID || box.right?.id === groupID).map(box => box.id))
    setRelationBoxes(current => current.map(box => ({
      ...box,
      left: box.left?.kind === 'GROUP' && box.left.id === groupID ? undefined : box.left,
      right: box.right?.kind === 'GROUP' && box.right.id === groupID ? undefined : box.right,
    })))
    setGroupBoxes(current => current.filter(group => group.id !== groupID))
    setTransformBoxes(current => current.map(transform => transform.input?.kind === 'GROUP' && transform.input.id === groupID ? { ...transform, input: undefined, rules: [] } : transform))
    setEndBox(current => current?.input && relationContains(current.input, { kind: 'GROUP', id: groupID }, relationBoxes, groupBoxes, transformBoxes) ? { ...current, input: undefined, outputs: [] } : current)
    setActiveGroupID(current => current === groupID ? '' : current)
    setDraft(current => ({ ...current, joins: current.joins.filter(join => !consumers.has(join.id)) }))
  }

  const refreshTransformOutputs = (next: TransformBox[]) => {
    setTransformBoxes(next)
    setEndBox(current => {
      if (!current?.input) return current
      const produced = relationOutputFields(current.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, next)
      return { ...current, outputs: produced.map(field => endOutputFor(field)) }
    })
    setEndPreview({ loading: false })
  }

  const addTransformBox = (componentType: GraphTransformComponentType, position?: CanvasPoint) => {
    const definition = transformComponentDefinition(componentType)
    if (!definition) return
    const nextNumber = transformBoxes.reduce((largest, transform) => Math.max(largest, Number(transform.id.replace('transform_', '')) || 0), 0) + 1
    const id = `transform_${nextNumber}`
    const transform: TransformBox = {
      id, family: definition.family, componentType, name: `${definition.label} ${nextNumber}`,
      position: position ?? { x: 620 + (nextNumber - 1) * 85, y: 175 + (nextNumber - 1) * 125 }, rules: [],
    }
    const next = [...transformBoxes, transform]
    setTransformBoxes(next)
    setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveTransformID(id); setActiveEnd(false)
    setFormError('')
    setCanvasNotice(`${definition.label}已加入画布，请从上游组件手动连线`)
  }

  const connectTransformInput = (transformID: string, input?: RelationInput) => {
    const graph: DesignerGraphV1 = {
      version: '1.0', nodePositions, nodeNames: Object.fromEntries(draft.nodes.map(node => [node.id, nodeLabel(node)])),
      joins: relationBoxes, groups: groupBoxes, transforms: transformBoxes, ...(endBox ? { end: endBox } : {}),
    }
    if (input) {
      const connectionError = graphConnectionError(input, { kind: 'TRANSFORM', id: transformID }, graph, draft.nodes.map(node => node.id))
      if (connectionError) { setFormError(connectionError); return }
    }
    const availableFields = relationOutputFields(input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
    const available = new Set(availableFields.map(field => field.key))
    const next = transformBoxes.map(transform => {
      if (transform.id !== transformID) return transform
      const retained = transform.rules.map(rule => ({ ...rule, inputKeys: rule.inputKeys.filter(key => available.has(key)) })).filter(rule => rule.inputKeys.length)
      return { ...transform, input, rules: retained.length ? retained : input && availableFields.length ? [defaultTransformRule(transform, availableFields, 1)] : [] }
    })
    refreshTransformOutputs(next)
    setFormError('')
  }

  const updateTransformName = (transformID: string, name: string) => {
    refreshTransformOutputs(transformBoxes.map(transform => transform.id === transformID ? { ...transform, name } : transform))
    setFormError('')
  }

  const updateTransformRule = (transformID: string, ruleID: string, patch: Partial<GraphTransformRule>) => {
    const next = transformBoxes.map(transform => transform.id !== transformID ? transform : {
      ...transform,
      rules: transform.rules.map(rule => {
        if (rule.id !== ruleID) return rule
        const updated = { ...rule, ...patch, output: patch.output ? { ...rule.output, ...patch.output } : rule.output }
        const needed = transformRuleInputCount(updated.operation, updated.fallbackMode)
        const inputKeys = [...updated.inputKeys]
        while (inputKeys.length < needed) inputKeys.push(inputKeys[0] || '')
        const targetType = updated.targetType || 'STRING'
        const output = {
          ...updated.output,
          canonicalType: updated.operation === 'CAST' ? targetType : updated.operation === 'DATE_FORMAT' ? 'STRING' : updated.operation === 'DATE_TRUNC' ? updated.output.canonicalType || 'DATE' : updated.operation === 'CASE' || updated.operation === 'CONCAT' || updated.operation === 'SUBSTRING' || updated.operation === 'TRIM' || updated.operation === 'UPPER' || updated.operation === 'LOWER' || updated.operation === 'REPLACE' ? 'STRING' : ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'ROUND', 'ABS', 'FLOOR', 'CEIL'].includes(updated.operation) ? 'DECIMAL' : updated.output.canonicalType,
        }
        return { ...updated, inputKeys: inputKeys.slice(0, needed), output }
      }),
    })
    refreshTransformOutputs(next)
    setFormError('')
  }

  const addTransformRule = (transformID: string) => {
    const transform = transformBoxes.find(item => item.id === transformID)
    if (!transform) return
    const available = relationOutputFields(transform.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
    if (!available.length) { setFormError('请先为字段处理组件连接一个有可用字段的输入组件'); return }
    refreshTransformOutputs(transformBoxes.map(item => item.id === transformID ? { ...item, rules: [...item.rules, defaultTransformRule(item, available, item.rules.length + 1)] } : item))
    setFormError('')
  }

  const removeTransformRule = (transformID: string, ruleID: string) => {
    refreshTransformOutputs(transformBoxes.map(transform => transform.id === transformID ? { ...transform, rules: transform.rules.filter(rule => rule.id !== ruleID) } : transform))
  }

  const removeTransformBox = (transformID: string) => {
    const removed: RelationInput = { kind: 'TRANSFORM', id: transformID }
    const consumers = new Set(relationBoxes.filter(box => box.left?.kind === 'TRANSFORM' && box.left.id === transformID || box.right?.kind === 'TRANSFORM' && box.right.id === transformID).map(box => box.id))
    setRelationBoxes(current => current.map(box => ({
      ...box,
      left: box.left?.kind === 'TRANSFORM' && box.left.id === transformID ? undefined : box.left,
      right: box.right?.kind === 'TRANSFORM' && box.right.id === transformID ? undefined : box.right,
    })))
    setDraft(current => ({ ...current, joins: current.joins.filter(join => !consumers.has(join.id)) }))
    const next = transformBoxes.filter(transform => transform.id !== transformID).map(transform => transform.input?.kind === 'TRANSFORM' && transform.input.id === transformID ? { ...transform, input: undefined, rules: [] } : transform)
    setTransformBoxes(next)
    setEndBox(current => current?.input && relationContains(current.input, removed, relationBoxes, groupBoxes, transformBoxes) ? { ...current, input: undefined, outputs: [] } : current)
    setActiveTransformID(current => current === transformID ? '' : current)
    setEndPreview({ loading: false })
  }

  const loadComponentPreview = useCallback(async (target: RelationInput) => {
    const key = graphInputKey(target)
    const request = (componentPreviewRequests.current[key] ?? 0) + 1
    componentPreviewRequests.current[key] = request
    const previewFingerprint = editorFingerprintRef.current
    let candidateDSL: ReturnType<typeof buildDatasetDSL>
    try {
      candidateDSL = buildComponentPreviewDSL(completedEditorDraft, target)
    } catch (cause) {
      const issue = componentPreviewIssue(cause)
      setComponentPreviews(current => ({ ...current, [key]: { loading: false, error: issue.reason, suggestion: issue.suggestion } }))
      return
    }
    setComponentPreviews(current => ({ ...current, [key]: { loading: true } }))
    try {
      const queryID = crypto.randomUUID()
      const data = editingRecord
        ? await datasetAPI.previewDraft(editingRecord.id, editingRecord.version, candidateDSL, queryID, {}, 5)
        : await datasetAPI.previewCandidate(candidateDSL, queryID, {}, 5)
      if (componentPreviewRequests.current[key] !== request || editorFingerprintRef.current !== previewFingerprint) return
      setComponentPreviews(current => ({ ...current, [key]: { loading: false, data: { columns: data.columns, rows: data.rows.slice(0, 5) } } }))
    } catch (cause) {
      if (componentPreviewRequests.current[key] !== request || editorFingerprintRef.current !== previewFingerprint) return
      const issue = componentPreviewIssue(cause)
      setComponentPreviews(current => ({ ...current, [key]: { loading: false, error: issue.reason, suggestion: issue.suggestion } }))
    }
  }, [completedEditorDraft, editingRecord])

  const loadEndPreview = useCallback(async () => {
    const request = ++endPreviewRequest.current
    const record = editingRecord
    const previewFingerprint = editorFingerprintRef.current
    let candidateDSL: ReturnType<typeof buildDatasetDSL>
    try {
      candidateDSL = buildDatasetDSL(record ? completedEditorDraft : { ...completedEditorDraft, name: completedEditorDraft.name.trim() || '组件数据预览' })
    } catch (cause) {
      const issue = endPreviewIssue(cause)
      setEndPreview({ loading: false, error: issue.reason, suggestion: issue.suggestion })
      return
    }
    setEndPreview({ loading: true })
    try {
      // 已保存数据集绑定乐观锁基线；新建画布使用独立候选审计，两者都不保存候选。
      const queryID = crypto.randomUUID()
      const data = record
        ? await datasetAPI.previewDraft(record.id, record.version, candidateDSL, queryID, {}, 5)
        : await datasetAPI.previewCandidate(candidateDSL, queryID, {}, 5)
      if (request !== endPreviewRequest.current || editorFingerprintRef.current !== previewFingerprint) return
      setEndPreview({ loading: false, data: { columns: data.columns, rows: data.rows.slice(0, 5) } })
    } catch (cause) {
      if (request !== endPreviewRequest.current || editorFingerprintRef.current !== previewFingerprint) return
      const issue = endPreviewIssue(cause)
      setEndPreview({ loading: false, error: issue.reason, suggestion: issue.suggestion })
    }
  }, [completedEditorDraft, editingRecord])

  const openCanvasPreview = useCallback((target: CanvasPreviewTarget) => {
    setCanvasPreviewTarget(target)
    if (target.kind === 'NODE') {
      const node = draft.nodes.find(item => item.id === target.id)
      if (node) void loadNodePreview(node)
      return
    }
    if (target.kind === 'END') {
      void loadEndPreview()
      return
    }
    void loadComponentPreview(target)
  }, [draft.nodes, loadComponentPreview, loadEndPreview, loadNodePreview])

  const canvasPreview = canvasPreviewTarget?.kind === 'NODE'
    ? nodePreviews[canvasPreviewTarget.id]
    : canvasPreviewTarget?.kind === 'END'
      ? endPreview
      : canvasPreviewTarget ? componentPreviews[graphInputKey(canvasPreviewTarget)] : undefined
  const canvasPreviewNode = canvasPreviewTarget?.kind === 'NODE' ? draft.nodes.find(node => node.id === canvasPreviewTarget.id) : undefined
  const canvasPreviewLabel = canvasPreviewTarget?.kind === 'NODE'
    ? canvasPreviewNode ? nodeLabel(canvasPreviewNode) : '数据节点'
    : canvasPreviewTarget?.kind === 'JOIN'
      ? relationBoxes.find(box => box.id === canvasPreviewTarget.id)?.name || '关联组件'
      : canvasPreviewTarget?.kind === 'GROUP'
        ? groupBoxes.find(group => group.id === canvasPreviewTarget.id)?.name || '分组组件'
        : canvasPreviewTarget?.kind === 'TRANSFORM'
          ? transformBoxes.find(transform => transform.id === canvasPreviewTarget.id)?.name || '字段处理组件'
          : endBox?.name || '结束节点'

  useEffect(() => {
    const changed = Boolean(lastEditorFingerprintRef.current) && lastEditorFingerprintRef.current !== currentEditorFingerprint
    lastEditorFingerprintRef.current = currentEditorFingerprint
    if (!changed) return
    endPreviewRequest.current += 1
    for (const key of Object.keys(componentPreviewRequests.current)) componentPreviewRequests.current[key] += 1
    setEndPreview({ loading: false })
    setComponentPreviews({})
    setCanvasPreviewTarget(null)
  }, [currentEditorFingerprint])

  const addEndBox = (position?: CanvasPoint) => {
    if (endBox) {
      setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveTransformID(''); setActiveEnd(true)
      return
    }
    setEndBox({ id: 'end_1', name: '最终输出', position: position ?? { x: 820, y: 165 }, outputs: [] })
    setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveTransformID(''); setActiveEnd(true)
    setCanvasNotice('结束节点已加入画布，请从最终上游组件手动连线')
  }

  const connectEndInput = (input?: RelationInput) => {
    if (input && endBox) {
      const graph: DesignerGraphV1 = {
        version: '1.0', nodePositions, nodeNames: Object.fromEntries(draft.nodes.map(node => [node.id, nodeLabel(node)])),
        joins: relationBoxes, groups: groupBoxes, transforms: transformBoxes, end: endBox,
      }
      const connectionError = graphConnectionError(input, { kind: 'OUTPUT', id: endBox.id }, graph, draft.nodes.map(node => node.id))
      if (connectionError) { setFormError(connectionError); return }
    }
    const produced = relationOutputFields(input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
    setEndBox(current => {
      if (!current) return current
      const sameInput = Boolean(input && current.input && input.kind === current.input.kind && input.id === current.input.id)
      return { ...current, input, outputs: produced.map(field => endOutputFor(field, sameInput ? current.outputs.find(item => item.key === field.key) : undefined)) }
    })
    setEndPreview({ loading: false })
  }

  const updateEndOutput = (field: ProducedField, checked: boolean) => setEndBox(current => {
    if (!current) return current
    const previous = current.outputs.find(item => item.key === field.key)
    return { ...current, outputs: checked ? [...current.outputs.filter(item => item.key !== field.key), endOutputFor(field, previous)] : current.outputs.filter(item => item.key !== field.key) }
  })
  const removeEndBox = () => {
    endPreviewRequest.current += 1
    setEndBox(null)
    setActiveEnd(false)
    setEndPreview({ loading: false })
  }

  const removeJoinCondition = (joinID: string, conditionID: string) => setDraft(current => ({
    ...current,
    joins: current.joins.map(join => {
      if (join.id !== joinID) return join
      const conditions = joinConditions(join).filter(condition => condition.id !== conditionID)
      const remaining = conditions.length ? conditions : joinConditions(join).slice(0, 1)
      return { ...join, conditions: remaining, leftField: remaining[0]?.leftField ?? '', rightField: remaining[0]?.rightField ?? '', manualConfirmed: false }
    }),
  }))

  const closeCanvasEditor = () => {
    if (activeJoinID) {
      const complete = draft.joins.some(join => join.id === activeJoinID && joinConditions(join).every(condition => condition.leftField && condition.rightField))
      setDraft(current => ({ ...current, joins: current.joins.map(join => join.id === activeJoinID ? { ...join, manualConfirmed: joinConditions(join).every(condition => condition.leftField && condition.rightField) } : join) }))
      setCanvasNotice(complete ? '关联配置已暂存' : '关联配置已暂存，请继续完善输入和关联字段')
    } else if (activeNodeID) {
      setCanvasNotice('表配置已暂存')
    } else if (activeGroupID) {
      const group = groupBoxes.find(item => item.id === activeGroupID)
      const complete = Boolean(group?.input && group.dimensions.length && group.metrics.length && group.metrics.every(metric => metric.aggregation))
      setCanvasNotice(complete ? '分组组件配置已暂存' : '分组组件配置已暂存，请继续完善')
    } else if (activeTransformID) {
      const transform = transformBoxes.find(item => item.id === activeTransformID)
      const complete = Boolean(transform?.input && transform.name.trim() && transform.rules.length && transform.rules.every(transformRuleIsComplete))
      setCanvasNotice(complete ? '字段处理组件配置已暂存' : '字段处理组件配置已暂存，请继续完善')
    } else if (activeEnd) {
      setCanvasNotice('结束节点配置已暂存')
    }
    setFormError('')
    setActiveNodeID('')
    setActiveJoinID('')
    setActiveGroupID('')
    setActiveTransformID('')
    setActiveEnd(false)
  }

  const openMetadata = () => {
    if (!draft.nodes.length || !draft.nodes.some(node => node.selected.length)) {
      setFormError('请先从左侧点选或拖入数据表，并至少保留一个输出字段')
      return
    }
    const graphValidation = validateDesignerGraph({
      version: '1.0', nodePositions, nodeNames: Object.fromEntries(draft.nodes.map(node => [node.id, nodeLabel(node)])),
      joins: relationBoxes, groups: groupBoxes, transforms: transformBoxes, ...(endBox ? { end: endBox } : {}),
    }, draft.nodes.map(node => node.id))
    if (!graphValidation.valid) {
      const cycle = graphValidation.issues.find(issue => issue.code === 'CYCLE' || issue.code === 'SELF_LOOP')
      setFormError(cycle?.message || graphValidation.errors[0] || '画布不是有效的有向无环图，请检查组件连线')
      return
    }
    if (relationBoxes.some(box => !box.left || !box.right || !draft.joins.some(join => join.id === box.id))) {
      setFormError('请完成每个关联组件的两个输入槽位、连接方式和关联字段')
      return
    }
    if (draft.nodes.length > 1 && (draft.joins.length !== draft.nodes.length - 1 || draft.joins.some(join => {
      const box = relationBoxes.find(item => item.id === join.id)
      const leftAvailable = new Set(relationOutputFields(box?.left, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
        .filter(field => field.binding.nodeId === join.leftNodeId).map(field => field.binding.field))
      const rightAvailable = new Set(relationOutputFields(box?.right, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
        .filter(field => field.binding.nodeId === join.rightNodeId).map(field => field.binding.field))
      return join.leftNodeId === join.rightNodeId || joinConditions(join).some(condition => !leftAvailable.has(condition.leftField) || !rightAvailable.has(condition.rightField)) || !join.manualConfirmed
    }))) {
      setFormError('请先用关联组件连接全部数据节点，并完成每个关联组件的槽位、连接方式和关联字段')
      return
    }
    if (!isConnected(draft.nodes, draft.joins)) {
      setFormError('当前关联图存在孤立表，请调整关联两端，确保所有表互相连通')
      return
    }
    for (const group of groupBoxes) {
      if (!group.input) { setFormError(`请为分组组件“${group.name}”连接输入`); return }
      if (!group.name.trim()) { setFormError('请为每个分组组件填写清晰的产物名称'); return }
      if (!group.dimensions.length) { setFormError(`请为“${group.name}”至少选择一个分组字段`); return }
      if (!group.metrics.length || group.metrics.some(metric => !metric.aggregation)) { setFormError(`请为“${group.name}”至少配置一个完整的聚合指标`); return }
      const codes = [...group.dimensions, ...group.metrics].map(field => safeIdentifier(field.code))
      if ([...group.dimensions, ...group.metrics].some(field => !field.name.trim() || !field.code.trim()) || new Set(codes).size !== codes.length) {
        setFormError(`“${group.name}”自动生成的字段别名为空或重复，请检查上游字段编码`); return
      }
    }
    for (const transform of transformBoxes) {
      if (!transform.input) { setFormError(`请为字段处理组件“${transform.name}”连接输入`); return }
      if (!transform.name.trim()) { setFormError('请为每个字段处理组件填写清晰的产物名称'); return }
      if (!transform.rules.length || transform.rules.some(rule => !transformRuleIsComplete(rule))) {
        setFormError(`请为“${transform.name}”至少配置一条完整的转换规则`); return
      }
      const available = new Set(relationOutputFields(transform.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes).map(field => field.key))
      if (transform.rules.some(rule => rule.inputKeys.some(key => !available.has(key)))) {
        setFormError(`“${transform.name}”引用了已不可用的上游字段，请重新选择`); return
      }
      const codes = transform.rules.map(rule => safeIdentifier(rule.output.code))
      if (new Set(codes).size !== codes.length) { setFormError(`“${transform.name}”的输出字段编码不能重复`); return }
    }
    for (const node of draft.nodes) {
      const nodeFields = draft.fields.filter(field => field.key.startsWith(`${node.id}.`))
      if (!nodeFields.some(field => field.output !== false)) {
        setFormError(`请为“${node.table.businessName || node.table.tableName}”保留至少一个明细输出字段`)
        return
      }
    }
    if (!endBox?.input) { setFormError('请添加结束节点，并连接画布中的最终产物'); return }
    const graph = graphShape(relationBoxes, groupBoxes, transformBoxes)
    const missingNode = draft.nodes.find(node => !graphContains(endBox.input!, { kind: 'NODE', id: node.id }, graph))
    const missingJoin = relationBoxes.find(join => !graphContains(endBox.input!, { kind: 'JOIN', id: join.id }, graph))
    const missingGroup = groupBoxes.find(group => !graphContains(endBox.input!, { kind: 'GROUP', id: group.id }, graph))
    const missingTransform = transformBoxes.find(transform => !graphContains(endBox.input!, { kind: 'TRANSFORM', id: transform.id }, graph))
    if (missingNode || missingJoin || missingGroup || missingTransform) { setFormError('结束节点之前仍有未接入最终数据流的组件，请连接或删除孤立组件'); return }
    const endAvailable = new Set(endInputFields.map(field => field.key))
    const endCodes = endBox.outputs.map(field => safeIdentifier(field.code))
    if (!endBox.outputs.length || endBox.outputs.some(field => !endAvailable.has(field.key) || !field.name.trim() || !field.code.trim())) {
      setFormError('请在结束节点选择至少一个有效输出字段；字段别名会按上游编码自动生成'); return
    }
    if (new Set(endCodes).size !== endCodes.length) { setFormError('结束节点自动生成的字段别名重复，请检查上游字段编码'); return }
    setFormError('')
    setDialog({ mode: 'metadata' })
  }

  const saveDataset = async () => {
    if (!metadata.name.trim() || !metadata.description.trim()) {
      setFormError('请填写数据集名称和说明')
      return
    }
    setBusyAction(editingRecord ? 'update' : 'create')
    setFormError('')
    try {
      const completed = completedEditorDraft
      const dsl = buildDatasetDSL(completed)
      const validation = await datasetAPI.validate(dsl)
      let saved: DatasetRecord
      if (editingRecord) {
        await datasetAPI.update(editingRecord.id, editingRecord.version, completed, dsl)
        const persisted = await datasetAPI.get(editingRecord.id)
        if (persisted.version <= editingRecord.version || persisted.dslHash !== validation.dslHash ||
          persisted.name !== completed.name || persisted.description !== completed.description) {
          throw new Error('服务端未确认最新配置已保存，请保留当前页面后重试')
        }
        saved = persisted
      } else {
        saved = await datasetAPI.create(dsl)
      }
      await loadDatasets()
      setDialog(null)
      setEditingRecord(null)
      if (metricReturnTo) {
        const savedSummary: DatasetSummary = {
          id: saved.id, code: saved.code, name: saved.name, description: saved.description, type: saved.type,
          status: saved.status, originTableId: saved.originTableId, version: saved.version, dslHash: saved.dslHash,
          currentPublishedVersionId: saved.currentPublishedVersionId, updatedAt: saved.updatedAt,
        }
        navigate('/datasets', { replace: true, state: location.state })
        setNotice({ tone: 'success', message: `已保存“${saved.name}”，请继续提交发布审批` })
        await openPublication(savedSummary)
      } else {
        if (datasetId) navigate('/datasets', { replace: true })
        setNotice({ tone: 'success', message: editingRecord ? `已保存“${saved.name}”的最新配置` : `已创建“${saved.name}”，可继续进入修改完善配置` })
      }
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : editingRecord ? '保存数据集失败' : '创建数据集失败')
    } finally {
      setBusyAction('')
    }
  }

  const openPublication = async (dataset: DatasetSummary) => {
    setDialog({ mode: 'publish', dataset })
    setPublicationRecord(null)
    setPublicationRequests([])
    setPublicationCapabilities({ manage: false, publish: false })
    setPublicationNote('')
    setPublicationDecisionNote('')
    setSelectedPublicationRequestID('')
    setFormError('')
    setBusyAction(`publication:${dataset.id}`)
    const [recordResult, requestsResult, manageResult, publishResult] = await Promise.allSettled([
      datasetAPI.get(dataset.id),
      datasetAPI.listPublicationRequests(dataset.id, 50, 0),
      datasetAPI.evaluatePermission(dataset.id, 'MANAGE'),
      datasetAPI.evaluatePermission(dataset.id, 'PUBLISH'),
    ])
    if (recordResult.status === 'fulfilled') setPublicationRecord(recordResult.value)
    if (requestsResult.status === 'fulfilled') {
      setPublicationRequests(requestsResult.value.items)
      setSelectedPublicationRequestID(requestsResult.value.items.find(item => item.status === 'PENDING')?.id ?? requestsResult.value.items[0]?.id ?? '')
    }
    setPublicationCapabilities({
      manage: manageResult.status === 'fulfilled' && manageResult.value.allowed,
      publish: publishResult.status === 'fulfilled' && publishResult.value.allowed,
    })
    const failure = [recordResult, requestsResult].find(result => result.status === 'rejected')
    if (failure?.status === 'rejected') setFormError(failure.reason instanceof Error ? failure.reason.message : '加载发布审批信息失败')
    setBusyAction('')
  }

  const refreshPublication = useCallback(async (datasetID: string, refreshCatalog = true) => {
    const [record, requests] = await Promise.all([
      datasetAPI.get(datasetID),
      datasetAPI.listPublicationRequests(datasetID, 50, 0),
    ])
    setPublicationRecord(record)
    setPublicationRequests(requests.items)
    setSelectedPublicationRequestID(current => requests.items.some(item => item.id === current)
      ? current
      : requests.items.find(item => item.status === 'PENDING')?.id ?? requests.items[0]?.id ?? '')
    if (refreshCatalog) await loadDatasets()
  }, [loadDatasets])

  useEffect(() => {
    const datasetID = dialog?.mode === 'publish' ? publicationRecord?.id : ''
    const waiting = publicationRequests.some(request =>
      request.status === 'PENDING' && request.metricCandidateStatus === 'PENDING')
    if (!datasetID || !waiting) return
    let active = true
    const timer = window.setInterval(() => {
      void refreshPublication(datasetID, false).catch(cause => {
        if (active) setFormError(cause instanceof Error ? cause.message : '刷新后台指标候选状态失败')
      })
    }, 2000)
    return () => {
      active = false
      window.clearInterval(timer)
    }
  }, [dialog?.mode, publicationRecord?.id, publicationRequests, refreshPublication])

  const submitPublicationRequest = async () => {
    if (!publicationRecord || !publicationCapabilities.manage || busyAction) return
    setBusyAction('publication-submit')
    setFormError('')
    try {
      const request = await datasetAPI.requestPublication(publicationRecord.id, {
        draftVersionId: publicationRecord.draftVersionId,
        expectedVersion: publicationRecord.version,
        expectedDraftRecordVersion: publicationRecord.draftRecordVersion,
        expectedDslHash: publicationRecord.dslHash,
        validationParameters: {},
      }, publicationNote.trim())
      await refreshPublication(publicationRecord.id)
      setSelectedPublicationRequestID(request.id)
      setPublicationNote('')
      setNotice({
        tone: 'success',
        message: request.status === 'PENDING'
          ? `“${publicationRecord.name}”已提交发布审批，指标候选正在后台生成`
          : `当前草稿已有${publicationStatusLabels[request.status] ?? request.status}记录`,
      })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '提交发布审批失败')
    } finally {
      setBusyAction('')
    }
  }

  const approvePublicationRequest = async () => {
    if (!publicationRecord || !selectedPublicationRequest || selectedPublicationRequest.status !== 'PENDING' || !publicationCapabilities.publish || busyAction) return
    setBusyAction('publication-approve')
    setFormError('')
    try {
      const result = await datasetAPI.approvePublication(
        publicationRecord.id, selectedPublicationRequest.id, selectedPublicationRequest.version, publicationDecisionNote.trim(),
      )
      await refreshPublication(publicationRecord.id)
      setPublicationDecisionNote('')
      setSelectedPublicationRequestID(result.request.id)
      setNotice({ tone: 'success', message: `“${publicationRecord.name}”审批通过并发布为 V${result.publishedVersion.versionNo}；内部原子度量事实正在自动提取` })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '审批并发布数据集失败')
    } finally {
      setBusyAction('')
    }
  }

  const rejectPublicationRequest = async () => {
    const reason = publicationDecisionNote.trim()
    if (!publicationRecord || !selectedPublicationRequest || selectedPublicationRequest.status !== 'PENDING' || !publicationCapabilities.publish || !reason || busyAction) return
    setBusyAction('publication-reject')
    setFormError('')
    try {
      const rejected = await datasetAPI.rejectPublication(
        publicationRecord.id, selectedPublicationRequest.id, selectedPublicationRequest.version, reason,
      )
      await refreshPublication(publicationRecord.id)
      setPublicationDecisionNote('')
      setSelectedPublicationRequestID(rejected.id)
      setNotice({ tone: 'success', message: `“${publicationRecord.name}”的发布申请已拒绝` })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '拒绝发布申请失败')
    } finally {
      setBusyAction('')
    }
  }

  const openView = async (dataset: DatasetSummary) => {
    setDialog({ mode: 'view', dataset })
    setDetail(null)
    setDetailPreview(null)
    setDetailPreviewError('')
    setFormError('')
    setBusyAction(`view:${dataset.id}`)
    const [recordResult, previewResult] = await Promise.allSettled([
      datasetAPI.get(dataset.id),
      datasetAPI.preview(dataset.id, crypto.randomUUID(), {}, 5),
    ])
    if (recordResult.status === 'fulfilled') setDetail(recordResult.value)
    else setFormError(recordResult.reason instanceof Error ? recordResult.reason.message : '加载数据集详情失败')
    if (previewResult.status === 'fulfilled') setDetailPreview(previewResult.value)
    else setDetailPreviewError(previewResult.reason instanceof Error ? previewResult.reason.message : '加载预览数据失败')
    setBusyAction('')
  }

  const openHistory = async (dataset: DatasetSummary) => {
    const request = ++historySelectionRequest.current
    setDialog({ mode: 'history', dataset })
    setHistoryRecord(null)
    setHistoryItems([])
    setSelectedHistoryVersion(null)
    setHistoryPreview(null)
    setHistoryConfirm(false)
    setFormError('')
    setBusyAction(`history:${dataset.id}`)
    try {
      const [record, versions] = await Promise.all([datasetAPI.get(dataset.id), loadAllPublishedVersions(dataset.id)])
      if (request !== historySelectionRequest.current) return
      setHistoryRecord(record)
      setHistoryItems(versions)
      if (versions[0]) {
        setHistoryPreview({ versionID: versions[0].id, loading: true })
        const previewRequest = datasetAPI.previewVersion(dataset.id, versions[0].id, crypto.randomUUID(), {}, 5).then(data => ({ data })).catch(cause => ({ error: cause instanceof Error ? cause.message : '加载发布版本数据预览失败' }))
        const version = await datasetAPI.getVersion(dataset.id, versions[0].id)
        if (request === historySelectionRequest.current) { setSelectedHistoryVersion(version); setBusyAction('') }
        const preview = await previewRequest
        if (request === historySelectionRequest.current) {
          setHistoryPreview({ versionID: versions[0].id, loading: false, ...preview })
        }
      }
    } catch (cause) {
      if (request === historySelectionRequest.current) setFormError(cause instanceof Error ? cause.message : '加载发布版本失败')
    } finally {
      if (request === historySelectionRequest.current) setBusyAction('')
    }
  }

  const selectHistoryVersion = async (versionID: string) => {
    const dataset = dialog?.dataset
    if (!dataset) return
    const request = ++historySelectionRequest.current
    setHistoryConfirm(false)
    setSelectedHistoryVersion(null)
    setHistoryPreview({ versionID, loading: true })
    setFormError('')
    setBusyAction(`version:${versionID}`)
    try {
      const previewRequest = datasetAPI.previewVersion(dataset.id, versionID, crypto.randomUUID(), {}, 5).then(data => ({ data })).catch(cause => ({ error: cause instanceof Error ? cause.message : '加载发布版本数据预览失败' }))
      const version = await datasetAPI.getVersion(dataset.id, versionID)
      if (request === historySelectionRequest.current) { setSelectedHistoryVersion(version); setBusyAction('') }
      const preview = await previewRequest
      if (request === historySelectionRequest.current) {
        setHistoryPreview({ versionID, loading: false, ...preview })
      }
    } catch (cause) {
      if (request === historySelectionRequest.current) setFormError(cause instanceof Error ? cause.message : '加载发布版本详情失败')
    } finally {
      if (request === historySelectionRequest.current) setBusyAction('')
    }
  }

  const rollbackHistoryVersion = async () => {
    const dataset = dialog?.dataset
    if (!dataset || !historyRecord || !selectedHistoryVersion) return
    setBusyAction(`rollback:${selectedHistoryVersion.id}`)
    setFormError('')
    try {
      const restored = await datasetAPI.rollbackVersion(dataset.id, selectedHistoryVersion.id, historyRecord.version)
      setHistoryRecord(restored)
      setHistoryConfirm(false)
      setDatasets(current => current.map(item => item.id === restored.id ? {
        ...item, name: restored.name, description: restored.description, type: restored.type, status: restored.status,
        version: restored.version, dslHash: restored.dslHash, currentPublishedVersionId: restored.currentPublishedVersionId,
        updatedAt: restored.updatedAt,
      } : item))
      setNotice({ tone: 'success', message: `已将发布 V${selectedHistoryVersion.versionNo} 回滚为新的当前配置 V${restored.version}` })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '回滚历史版本失败')
    } finally {
      setBusyAction('')
    }
  }

  const disableDataset = async () => {
    const dataset = dialog?.dataset
    if (!dataset) return
    setBusyAction(`disable:${dataset.id}`)
    try {
      await datasetAPI.disable(dataset.id, dataset.version)
      await loadDatasets()
      setDialog(null)
      setNotice({ tone: 'success', message: `已停用“${dataset.name}”` })
    } catch (cause) { setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '停用数据集失败' }) }
    finally { setBusyAction('') }
  }

  const restoreDataset = async () => {
    const dataset = dialog?.dataset
    if (!dataset) return
    setBusyAction(`restore:${dataset.id}`)
    setFormError('')
    try {
      const restored = await datasetAPI.restore(dataset.id, dataset.version)
      await loadDatasets()
      setDialog(null)
      setNotice({ tone: 'success', message: `已恢复“${dataset.name}”（${statusLabels[restored.status] ?? restored.status}）` })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '恢复数据集失败')
    } finally {
      setBusyAction('')
    }
  }

  const deleteDataset = async () => {
    const dataset = dialog?.dataset
    if (!dataset) return
    setBusyAction(`delete:${dataset.id}`)
    setFormError('')
    try {
      await datasetAPI.delete(dataset.id, dataset.version)
      setDatasets(current => current.filter(item => item.id !== dataset.id))
      setDialog(null)
      setNotice({ tone: 'success', message: `已删除“${dataset.name}”` })
    } catch (cause) { setFormError(cause instanceof Error ? cause.message : '删除数据集失败') }
    finally { setBusyAction('') }
  }

  const generateDatasetAIPlan = async (retryInstruction?: string, useActualCanvas = false) => {
    const instruction = (retryInstruction ?? aiPrompt).trim()
    if (!instruction || aiBusy || aiApplying || assetsLoading || busyAction) return
    const requestID = ++aiRequest.current
    const baseFingerprint = editorFingerprintRef.current
    const actualCurrent = datasetAIPlanFromEditor(draft, currentDesignerGraph, metadata)
    // Once the canvas contains nodes it is the single source of truth for every AI
    // modification. A staged proposal is only reusable while a brand-new canvas is
    // still empty; this prevents a follow-up prompt from silently ignoring manual edits.
    const current = datasetAIRequestContext(actualCurrent, aiResult?.proposal.plan, {
      forceLiveCanvas: useActualCanvas,
      stagedProposalApplied: aiApplied,
    })
    setAILastInstruction(instruction)
    setAIRetryAction(null)
    setAIBusy(true)
    setAIError(null)
    try {
      const result = await requestDatasetAIProposal(editingRecord?.id, instruction, current, aiPlanHints)
      if (requestID !== aiRequest.current) return
      const tableIDs = [...new Set(result.proposal.plan.nodes.map(node => node.tableId))]
      const columnEntries = await Promise.all(tableIDs.map(async tableID => {
        try { return [tableID, (await datasetAPI.columns(tableID)).items] as const } catch { return [tableID, [] as AssetColumn[]] as const }
      }))
      if (requestID !== aiRequest.current) return
      if (editorFingerprintRef.current !== baseFingerprint) {
        setAIError(datasetAILocalIssue(
          '生成期间画布已发生变化，为避免覆盖你的修改，本次方案未应用。',
          '可以按原要求基于当前画布重试，也可以修改上方要求后重新生成。',
        ))
        setAIRetryAction('GENERATE')
        return
      }
      const columnsByTable = new Map(columnEntries)
      setAIReviewLabels({
        nodes: Object.fromEntries(result.proposal.plan.nodes.map(node => {
          const table = tables.find(item => item.id === node.tableId)
          return [node.id, `${table?.businessName || table?.tableName || '数据表'}（${node.alias}）`]
        })),
        fields: Object.fromEntries(result.proposal.plan.nodes.flatMap(node => {
          const columns = columnsByTable.get(node.tableId) ?? []
          return node.selectedColumns.map(columnName => {
            const column = columns.find(item => item.columnName === columnName)
            const label = column?.businessName && column.businessName !== columnName ? `${column.businessName}（${columnName}）` : columnName
            return [`${node.id}.${columnName}`, label]
          })
        })),
      })
      setAIResult(result)
      setAIApplied(false)
      setAIDetailsExpanded(true)
      setAIPrompt('')
      setAIRetryAction(null)
    } catch (cause) {
      if (requestID !== aiRequest.current) return
      setAIError(datasetAIRequestIssue(cause, 'GENERATE'))
      setAIRetryAction('GENERATE')
    } finally {
      if (requestID === aiRequest.current) setAIBusy(false)
    }
  }

  autoGenerateDatasetAIPlan.current = instruction => { void generateDatasetAIPlan(instruction, true) }

  useEffect(() => {
    if (!pendingMetricAIAutoRun || dialog?.mode !== 'create' || assetsLoading || busyAction || aiBusy || aiApplying) return
    const targetReady = datasetId === 'new' ? !editingRecord : editingRecord?.id === datasetId
    if (!targetReady) return
    if (metricAIAutoRunKeys.current.has(pendingMetricAIAutoRun.key) || metricAIAutoRunWasConsumed(pendingMetricAIAutoRun.key)) {
      setPendingMetricAIAutoRun(null)
      return
    }
    // Mark before invoking so React StrictMode's development effect replay and normal
    // re-renders cannot submit the same metric handoff twice.
    metricAIAutoRunKeys.current.add(pendingMetricAIAutoRun.key)
    consumeMetricAIAutoRun(pendingMetricAIAutoRun.key)
    const instruction = pendingMetricAIAutoRun.instruction
    setPendingMetricAIAutoRun(null)
    autoGenerateDatasetAIPlan.current(instruction)
  }, [aiApplying, aiBusy, assetsLoading, busyAction, datasetId, dialog?.mode, editingRecord, pendingMetricAIAutoRun])

  const applyDatasetAIPlan = async () => {
    if (!aiResult || aiBusy || aiApplying) return
    const requestID = ++aiApplyRequest.current
    const baseFingerprint = editorFingerprintRef.current
    setAIApplying(true)
    setAIError(null)
    setAIRetryAction(null)
    try {
      const materialized = await materializeDatasetAIPlan(
        aiResult.proposal.plan,
        tables,
        async tableID => (await datasetAPI.columns(tableID)).items,
        draft,
        generatedCode,
        currentDesignerGraph,
      )
      if (requestID !== aiApplyRequest.current) return
      // The AI contract is validated independently, then the deterministic editor conversion
      // still passes through the existing authoritative DSL validator before any React state changes.
      await datasetAPI.validate(buildDatasetDSL(materialized.draft))
      if (requestID !== aiApplyRequest.current) return
      if (editorFingerprintRef.current !== baseFingerprint) {
        setAIError(datasetAILocalIssue(
          '校验期间画布已发生变化，本次方案未应用。',
          '请重新生成方案，确认无误后再应用；当前画布内容已保留。',
        ))
        setAIRetryAction(aiLastInstruction.trim() ? 'GENERATE' : null)
        return
      }
      const appliedTransforms = materialized.graph.transforms ?? []
      const appliedSnapshot: DatasetEditorSnapshot = {
        draft: materialized.draft,
        relationBoxes: materialized.graph.joins,
        groupBoxes: materialized.graph.groups,
        transformBoxes: appliedTransforms,
        endBox: materialized.graph.end ?? null,
        nodePositions: materialized.graph.nodePositions,
        metadata: materialized.metadata,
      }
      setAIUndo({ before: currentEditorSnapshot, appliedFingerprint: editorFingerprint(appliedSnapshot) })
      setDraft(materialized.draft)
      setRelationBoxes(materialized.graph.joins)
      setGroupBoxes(materialized.graph.groups)
      setTransformBoxes(appliedTransforms)
      setEndBox(materialized.graph.end ?? null)
      setNodePositions(materialized.graph.nodePositions)
      setMetadata(materialized.metadata)
      setActiveNodeID('')
      setActiveJoinID('')
      setActiveGroupID('')
      setActiveTransformID('')
      setActiveEnd(false)
      endPreviewRequest.current += 1
      setEndPreview({ loading: false })
      setFormError('')
      setCanvasNotice(`AI 方案已应用：${aiResult.proposal.summary}`)
      setAIApplied(true)
      setAIRetryAction(null)
    } catch (cause) {
      if (requestID !== aiApplyRequest.current) return
      setAIError(datasetAIRequestIssue(cause, 'APPLY'))
      setAIRetryAction('APPLY')
    } finally {
      if (requestID === aiApplyRequest.current) setAIApplying(false)
    }
  }

  const undoDatasetAIPlan = () => {
    if (!aiUndo) return
    if (editorFingerprintRef.current !== aiUndo.appliedFingerprint) {
      setAIError(datasetAILocalIssue(
        '应用后画布又有新的修改，不能安全撤销 AI 方案。',
        '请继续让 AI 修改，或保留当前内容并手动调整。',
      ))
      return
    }
    const previous = aiUndo.before
    setDraft(previous.draft)
    setRelationBoxes(previous.relationBoxes)
    setGroupBoxes(previous.groupBoxes)
    setTransformBoxes(previous.transformBoxes)
    setEndBox(previous.endBox)
    setNodePositions(previous.nodePositions)
    setMetadata(previous.metadata)
    setActiveNodeID('')
    setActiveJoinID('')
    setActiveGroupID('')
    setActiveTransformID('')
    setActiveEnd(false)
    endPreviewRequest.current += 1
    setEndPreview({ loading: false })
    aiRequest.current += 1
    aiApplyRequest.current += 1
    setAIUndo(null)
    setAIApplied(false)
    setAIError(null)
    setAIResult(null)
    setAIReviewLabels({ nodes: {}, fields: {} })
    setAIDetailsExpanded(true)
    setAIRetryAction(null)
    setAILastInstruction('')
    setCanvasNotice('已撤销本次 AI 方案，恢复到应用前的画布')
  }

  const retryDatasetAI = (mode: 'ORIGINAL' | 'MODIFIED' = 'ORIGINAL') => {
    if (!aiRetryAction || aiBusy || aiApplying) return
    if (aiRetryAction === 'GENERATE') {
      const instruction = mode === 'MODIFIED' ? aiPrompt.trim() : aiLastInstruction.trim()
      if (!instruction) return
      void generateDatasetAIPlan(instruction, true)
      return
    }
    void applyDatasetAIPlan()
  }

  const dismissDatasetAIError = () => {
    setAIError(null)
    setAIRetryAction(null)
  }

  const closeDialog = () => {
    if (busyAction || aiApplying) return
    resetDatasetAI()
    historySelectionRequest.current += 1
    endPreviewRequest.current += 1
    if (document.fullscreenElement === canvasFullscreenTarget.current) void document.exitFullscreen()
    setCanvasFullscreen(false)
    setDialog(null)
    setEditingRecord(null)
    setHistoryRecord(null)
    setHistoryItems([])
    setSelectedHistoryVersion(null)
    setHistoryPreview(null)
    setHistoryConfirm(false)
    setPublicationRecord(null)
    setPublicationRequests([])
    setPublicationCapabilities({ manage: false, publish: false })
    setPublicationNote('')
    setPublicationDecisionNote('')
    setSelectedPublicationRequestID('')
    setFormError('')
    if (datasetId) navigate('/datasets', { replace: true })
  }
  const actionBusy = Boolean(busyAction)
  const editingCanvas = Boolean(editingRecord || busyAction.startsWith('edit:') || dialog?.mode === 'create' && dialog.dataset)

  return <AppShell title="数据集配置中心" eyebrow="数据资产" actions={<button className="primary-button" type="button" disabled={actionBusy} onClick={() => void openCreate()}>新建数据集</button>}>
    {notice && <div className={`dataset-center-toast ${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}><strong>{notice.tone === 'success' ? '✓' : '!'}</strong><span>{notice.message}</span><button type="button" aria-label="关闭消息" onClick={() => setNotice(null)}>×</button></div>}
    <section className="dataset-center" aria-label="数据集配置中心内容">
      <header className="dataset-center-summary"><div><span className="eyebrow">数据集资产</span><h2>全部数据集</h2><p>集中查看、修改和管理当前租户的数据集资产。</p></div><strong>{datasets.length}<small> 个数据集</small></strong></header>
      <div className="dataset-center-filters" aria-label="数据集筛选">
        <label><span>搜索</span><input aria-label="搜索数据集" type="search" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="名称或编码" /></label>
        <label><span>类型</span><select aria-label="按数据集类型筛选" value={typeFilter} onChange={event => setTypeFilter(event.target.value)}><option value="ALL">全部类型</option><option value="SINGLE_SOURCE">单数据源</option><option value="CROSS_SOURCE">跨数据源</option></select></label>
        <label><span>状态</span><select aria-label="按数据集状态筛选" value={statusFilter} onChange={event => setStatusFilter(event.target.value)}><option value="ALL">全部状态</option>{Object.entries(statusLabels).map(([status, label]) => <option key={status} value={status}>{label}</option>)}</select></label>
        <small>显示 {filtered.length} / {datasets.length}</small>
      </div>
      {loading ? <Empty>正在加载数据集…</Empty> : !datasets.length ? <Empty title="还没有数据集">点击右上角“新建数据集”开始配置。</Empty> : !filtered.length ? <Empty title="没有符合条件的数据集">请调整搜索词或筛选条件。</Empty> :
        <div className="dataset-asset-list" role="list" aria-label="数据集资产清单">{filtered.map(dataset => <article key={dataset.id} role="listitem" className="dataset-asset-card">
          <div className="dataset-asset-icon" aria-hidden="true">DS</div>
          <div className="dataset-asset-main"><div><h3>{dataset.name}</h3><span className={`dataset-asset-status ${dataset.status.toLowerCase()}`}>{statusLabels[dataset.status] ?? dataset.status}</span>{dataset.originTableId && <span className="dataset-asset-origin" title="由已完成映射的数据资产自动创建">映射表数据集</span>}</div><p>{dataset.description || '暂无说明'}</p><small>{dataset.originDataSourceName ? `${dataset.originDataSourceName} · ` : ''}{dataset.code}</small></div>
          <dl><div><dt>类型</dt><dd>{typeLabels[dataset.type] ?? dataset.type}</dd></div><div><dt>版本</dt><dd>V{dataset.version}</dd></div><div><dt>更新时间</dt><dd>{new Date(dataset.updatedAt).toLocaleString('zh-CN', { hour12: false })}</dd></div></dl>
          <div className="dataset-asset-actions"><button className="action-view" type="button" disabled={actionBusy} onClick={() => void openView(dataset)}>查看</button><button className="action-edit" type="button" disabled={actionBusy} onClick={() => void openEdit(dataset)}>修改</button><button className="action-publish" type="button" disabled={actionBusy || dataset.status === 'DISABLED' || dataset.status === 'DEPRECATED'} title={dataset.status === 'DISABLED' || dataset.status === 'DEPRECATED' ? '请先恢复可用状态再提交发布审批' : '冻结当前草稿并提交发布审批'} onClick={() => void openPublication(dataset)}>发布</button><button className="action-history" type="button" disabled={actionBusy} onClick={() => void openHistory(dataset)}>历史版本</button>{dataset.status === 'DISABLED' ? <button className="action-resume" type="button" disabled={actionBusy} title="恢复到停用前的数据集状态" onClick={() => { setFormError(''); setDialog({ mode: 'restore', dataset }) }}>恢复</button> : <button className="action-pause" type="button" disabled={actionBusy || dataset.status === 'DEPRECATED'} title={dataset.status === 'DEPRECATED' ? '已废弃数据集不能再次停用' : '停用后将阻止新的查询绑定'} onClick={() => { setFormError(''); setDialog({ mode: 'disable', dataset }) }}>停用</button>}<button className="action-delete" type="button" disabled={actionBusy} onClick={() => { setFormError(''); setDialog({ mode: 'delete', dataset }) }}>删除</button></div>
        </article>)}</div>}
    </section>

    {dialog?.mode === 'create' && <Dialog title={editingCanvas ? '修改数据集' : '新建数据集'} eyebrow="图形化配置" wide closeDisabled={aiApplying} onClose={closeDialog}>
      <div className="dataset-create-layout">
        <aside className="dataset-template-tree"><header><strong>数据资产节点</strong><small>已完成映射 / 可重复引用</small></header>{assetsLoading ? <p>正在加载映射表…</p> : !sourceGroups.length ? <p>暂无已完成 LLM 映射的启用表，请先完成资产映射。</p> : sourceGroups.map(source => <section key={source.id}><button className="source-tree-node" type="button" aria-expanded={expandedSources.has(source.id)} onClick={() => setExpandedSources(current => { const next = new Set(current); if (next.has(source.id)) next.delete(source.id); else next.add(source.id); return next })}><span>{expandedSources.has(source.id) ? '▾' : '▸'}</span><strong>{source.name}</strong><small>{source.type}</small></button>{expandedSources.has(source.id) && <div className="source-tree-children">{source.tables.map(table => { const instanceCount = draft.nodes.filter(node => node.table.id === table.id).length; return <button key={table.id} type="button" draggable onDragStart={event => event.dataTransfer.setData('text/dataset-table-id', table.id)} onClick={() => void selectTable(table)}><span>▦</span><span><strong>{table.businessName || table.tableName}</strong><small>已映射 · {table.schemaName}.{table.tableName} · {table.columnCount} 字段</small></span>{instanceCount > 0 && <em>已引用 {instanceCount} 次</em>}</button> })}</div>}</section>)}</aside>
        <main ref={canvasFullscreenTarget} className={`dataset-template-canvas ${canvasFullscreen ? 'is-fullscreen' : ''}`} onClick={closeCanvasEditor} onDragOver={event => event.preventDefault()} onDrop={(event: DragEvent<HTMLElement>) => { event.preventDefault(); const table = tables.find(item => item.id === event.dataTransfer.getData('text/dataset-table-id')); if (table) void selectTable(table) }}>
          <DatasetAIComposer prompt={aiPrompt} lastInstruction={aiLastInstruction} result={aiResult} labels={aiReviewLabels} error={aiError} busy={aiBusy} applying={aiApplying} applied={aiApplied} detailsExpanded={aiDetailsExpanded} ready={!assetsLoading && !busyAction} hasAssets={tables.length > 0} canUndo={Boolean(aiUndo)} canRetry={Boolean(aiRetryAction)} retryRequiresGeneration={aiRetryAction === 'GENERATE'} hasGraph={draft.nodes.length > 0} onPromptChange={setAIPrompt} onSubmit={() => void generateDatasetAIPlan()} onApply={() => void applyDatasetAIPlan()} onUndo={undoDatasetAIPlan} onRetryOriginal={() => retryDatasetAI('ORIGINAL')} onRetryModified={() => retryDatasetAI('MODIFIED')} onDismissError={dismissDatasetAIError} onDetailsExpandedChange={setAIDetailsExpanded} />
          {!draft.nodes.length ? <div className="dataset-canvas-empty"><strong>选择第一张映射表开始建模</strong><p>表会成为数据节点；点击节点可预览真实数据并选择输出字段。</p>{canvasNotice && <small role="status">{canvasNotice}</small>}</div> : <div className="dataset-node-graph">
            <div className="dataset-graph-heading"><div><strong>组件关系画布</strong><small>{draft.nodes.length} 个数据节点 · {relationBoxes.length} 个关联 · {groupBoxes.length} 个分组 · {endBox ? '1 个结束节点' : '尚无结束节点'} · {transformBoxes.length} 个字段处理</small></div><span>{canvasNotice || '拖入组件并连线，结束节点定义最终产物'}</span></div>
            <RelationCanvas nodes={draft.nodes} fields={draft.fields} joins={draft.joins} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} end={endBox} nodePositions={nodePositions} activeNodeID={activeNodeID} activeJoinID={activeJoinID} activeGroupID={activeGroupID} activeTransformID={activeTransformID} activeEnd={activeEnd} tables={tables} isFullscreen={canvasFullscreen} previewTarget={canvasPreviewTarget} preview={canvasPreview} previewLabel={canvasPreviewLabel} onPreview={openCanvasPreview} onRefreshPreview={() => { if (canvasPreviewTarget) openCanvasPreview(canvasPreviewTarget) }} onClosePreview={() => setCanvasPreviewTarget(null)} onArrange={arrangeCanvas} onToggleFullscreen={() => void toggleCanvasFullscreen()} onAddJoin={addRelationBox} onAddGroup={addGroupBox} onAddTransform={addTransformBox} onAddEnd={addEndBox} onAddTable={(table, position) => void selectTable(table, position)} onMove={updateCanvasPosition} onConnect={dropRelationInput} onConnectGroup={connectGroupInput} onConnectTransform={connectTransformInput} onConnectEnd={connectEndInput} onRemoveBox={removeRelationBox} onRemoveGroup={removeGroupBox} onRemoveTransform={removeTransformBox} onRemoveEnd={removeEndBox} onNodeClick={openNodeConfig} onJoinClick={joinID => { setActiveNodeID(''); setActiveGroupID(''); setActiveTransformID(''); setActiveEnd(false); setActiveJoinID(joinID); setCanvasNotice('') }} onGroupClick={groupID => { setActiveNodeID(''); setActiveJoinID(''); setActiveTransformID(''); setActiveEnd(false); setActiveGroupID(groupID); setCanvasNotice('') }} onTransformClick={transformID => { setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveEnd(false); setActiveTransformID(transformID); setCanvasNotice('') }} onEndClick={() => { setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveTransformID(''); setActiveEnd(true); setCanvasNotice('') }} onRemoveNode={removeNode} />
          </div>}
          {activeNode && <NodeConfigDrawer node={activeNode} fields={draft.fields.filter(field => field.key.startsWith(`${activeNode.id}.`))} onFieldPatch={updateOutputField} onDone={closeCanvasEditor} />}
          {activeRelationBox && <JoinConfigDrawer box={activeRelationBox} join={activeJoin} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} nodes={draft.nodes} leftOutputFields={activeLeftOutputFields} rightOutputFields={activeRightOutputFields} onNameChange={name => setRelationBoxes(current => current.map(box => box.id === activeRelationBox.id ? { ...box, name } : box))} onJoinPatch={patch => activeJoin && updateJoin(activeJoin.id, { ...patch, manualConfirmed: false })} onConditionPatch={(conditionID, patch) => activeJoin && updateJoinCondition(activeJoin.id, conditionID, patch)} onAddCondition={() => activeJoin && addJoinCondition(activeJoin.id)} onRemoveCondition={conditionID => activeJoin && removeJoinCondition(activeJoin.id, conditionID)} onOutputChange={(key, checked) => updateRelationOutput(activeRelationBox.id, key, checked)} onDone={closeCanvasEditor} />}
          {activeGroup && <GroupingConfigDrawer box={activeGroup} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} nodes={draft.nodes} availableFields={groupInputFields} error={formError} onNameChange={name => updateGroupName(activeGroup.id, name)} onDimensionChange={(field, enabled) => updateGroupDimension(activeGroup.id, field, enabled)} onMetricChange={(field, enabled, patch) => updateGroupMetric(activeGroup.id, field, enabled, patch)} onDone={closeCanvasEditor} />}
          {activeTransform && <TransformConfigDrawer transform={activeTransform} inputs={transformInputFields} nodes={draft.nodes} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} error={formError} onNameChange={name => updateTransformName(activeTransform.id, name)} onRuleChange={(ruleID, patch) => updateTransformRule(activeTransform.id, ruleID, patch)} onAddRule={() => addTransformRule(activeTransform.id)} onRemoveRule={ruleID => removeTransformRule(activeTransform.id, ruleID)} onDone={closeCanvasEditor} />}
          {activeEnd && endBox && <EndConfigDrawer end={endBox} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} nodes={draft.nodes} availableFields={endInputFields} onNameChange={name => setEndBox(current => current ? { ...current, name } : current)} onOutputChange={updateEndOutput} onDone={closeCanvasEditor} />}
          {formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}
        </main>
      </div>
      <footer className="dataset-dialog-footer"><button className="quiet-button" type="button" disabled={actionBusy || aiApplying} onClick={closeDialog}>取消</button><button className="primary-button" type="button" disabled={actionBusy || assetsLoading || aiBusy || aiApplying} onClick={openMetadata}>{busyAction.startsWith('asset:') ? '正在填充…' : aiBusy ? '正在生成 AI 方案…' : aiApplying ? '正在应用 AI 方案…' : '保存配置'}</button></footer>
    </Dialog>}

    {dialog?.mode === 'metadata' && <Dialog title={editingRecord ? '保存数据集修改' : '完善数据集信息'} eyebrow="保存配置" onClose={() => { if (!busyAction) setDialog({ mode: 'create' }) }}><div className="dataset-metadata-form"><p>图形化配置已完成，请确认数据集名称和说明后保存。</p><label>数据集名称<input autoFocus value={metadata.name} onChange={event => setMetadata(current => ({ ...current, name: event.target.value }))} placeholder="例如：客户订单明细" /></label><label>数据集说明<textarea value={metadata.description} onChange={event => setMetadata(current => ({ ...current, description: event.target.value }))} placeholder="说明数据范围、业务口径和使用场景" /></label><small>{editingRecord ? `数据集编码保持不变：${generatedCode}` : `系统将自动生成唯一编码：${generatedCode}`}</small>{formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}<footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={() => setDialog({ mode: 'create' })}>返回配置</button><button className="primary-button" type="button" disabled={actionBusy} onClick={() => void saveDataset()}>{busyAction === 'update' ? '正在保存…' : busyAction === 'create' ? '正在创建…' : editingRecord ? '保存修改' : '创建数据集'}</button></footer></div></Dialog>}

    {dialog?.mode === 'view' && dialog.dataset && <Dialog title="数据集详情" eyebrow="资产信息" wide onClose={closeDialog}>{busyAction.startsWith('view:') ? <Empty>正在加载详情与预览数据…</Empty> : detail ? <div className="dataset-detail"><header><div><strong>{detail.name}</strong><span className={`dataset-asset-status ${detail.status.toLowerCase()}`}>{statusLabels[detail.status] ?? detail.status}</span>{detail.originTableId && <span className="dataset-asset-origin" title="由已完成映射的数据资产自动创建">映射表数据集</span>}</div><p>{detail.description || '暂无说明'}</p></header><dl><div><dt>编码</dt><dd>{detail.code}</dd></div><div><dt>类型</dt><dd>{typeLabels[detail.type] ?? detail.type}</dd></div><div><dt>聚合版本</dt><dd>V{detail.version}</dd></div><div><dt>草稿版本</dt><dd>V{detail.draftVersionNo}</dd></div><div><dt>数据节点</dt><dd>{Array.isArray(detail.dsl.nodes) ? detail.dsl.nodes.length : 0}</dd></div><div><dt>输出字段</dt><dd>{Array.isArray(detail.dsl.fields) ? detail.dsl.fields.length : 0}</dd></div></dl><section className="dataset-detail-preview" aria-label="预览数据"><div><h3>预览数据</h3><span>前 5 行</span></div>{detailPreview ? <PreviewRows preview={detailPreview} /> : <div className="dataset-center-feedback error" role="alert">{detailPreviewError || '暂无可预览数据'}</div>}</section><footer><button className="quiet-button" type="button" onClick={closeDialog}>关闭</button><button className="primary-button" type="button" onClick={() => { setDialog(null); void openEdit(detail) }}>修改配置</button></footer></div> : <div className="dataset-center-feedback error" role="alert">{formError}</div>}</Dialog>}

    {dialog?.mode === 'history' && dialog.dataset && <Dialog title={`${dialog.dataset.name} · 历史版本`} eyebrow="发布快照与安全回滚" wide onClose={closeDialog}><PublishedVersionHistoryPanel record={historyRecord} items={historyItems} selected={selectedHistoryVersion} preview={historyPreview} loading={busyAction.startsWith('history:') || busyAction.startsWith('version:')} busy={actionBusy} confirming={historyConfirm} error={formError} onSelect={versionID => void selectHistoryVersion(versionID)} onStartRollback={() => setHistoryConfirm(true)} onCancelRollback={() => { setHistoryConfirm(false); setFormError('') }} onRollback={() => void rollbackHistoryVersion()} onClose={closeDialog} /></Dialog>}

    {dialog?.mode === 'publish' && dialog.dataset && <Dialog title={`${dialog.dataset.name} · 发布审批`} eyebrow="冻结草稿与审批发布" wide onClose={closeDialog}>
      <div className="dataset-publication">
        {busyAction.startsWith('publication:') ? <Empty>正在加载发布审批信息…</Empty> : publicationRecord ? <>
          <section className="dataset-publication-current" aria-label="当前发布候选">
            <div><span>当前草稿</span><strong>草稿 V{publicationRecord.draftVersionNo}</strong><small>{publicationRecord.dslHash.slice(0, 12)}…</small></div>
            <div><span>数据集聚合版本</span><strong>V{publicationRecord.version}</strong><small>提交时会冻结当前精确版本</small></div>
            <div><span>当前草稿审批</span><strong className={currentDraftPublicationRequest?.status.toLowerCase()}>{currentDraftPublicationRequest ? publicationStatusLabels[currentDraftPublicationRequest.status] : '未提交'}</strong><small>{currentDraftPublicationRequest?.publishedVersionId ? `已发布版本 ${currentDraftPublicationRequest.publishedVersionId} · 候选正在进入资产管理中心` : currentDraftPublicationRequest ? `${metricCandidateGenerationLabels[currentDraftPublicationRequest.metricCandidateStatus] ?? currentDraftPublicationRequest.metricCandidateStatus} · 审批前不展示候选` : '指标不会读取未审批草稿'}</small></div>
          </section>

          <div className="dataset-publication-layout">
            <section className="dataset-publication-submit" aria-label="提交发布审批">
              <header><div><span>申请人操作</span><h3>提交当前草稿</h3></div><small>{publicationCapabilities.manage ? '具备提交权限' : '仅可查看'}</small></header>
              <p>系统将立即冻结当前草稿版本、DSL 与校验参数；指标事实提取与 LLM 语义补全在后台执行。候选仅暂存，审批通过后才进入资产管理中心。</p>
              <label>申请说明（选填）<textarea value={publicationNote} onChange={event => setPublicationNote(event.target.value)} placeholder="例如：订单与客户区域关联已由 AI 完成，请审批用于指标设计" /></label>
              {currentDraftPublicationRequest?.status === 'PENDING' && <div className="dataset-publication-hint">当前精确草稿已经在审批中，无需重复提交。</div>}
              {currentDraftPublicationRequest?.status === 'APPROVED' && <div className="dataset-publication-hint success">当前精确草稿已审批发布。再次修改并保存后可提交新的审批。</div>}
              <button className="primary-button" type="button" disabled={actionBusy || !publicationCapabilities.manage || currentDraftPublicationRequest?.status === 'APPROVED' || currentDraftPublicationRequest?.status === 'PENDING' && currentDraftPublicationRequest.metricCandidateStatus !== 'FAILED'} onClick={() => void submitPublicationRequest()}>{busyAction === 'publication-submit' ? '正在提交审批…' : currentDraftPublicationRequest?.status === 'PENDING' ? '重试后台生成' : '提交审批并后台生成候选'}</button>
            </section>

            <section className="dataset-publication-review" aria-label="审批发布申请">
              <header><div><span>审批人操作</span><h3>审批并发布</h3></div><small>{publicationCapabilities.publish ? '具备审批权限' : '仅可查看'}</small></header>
              {!publicationRequests.length ? <div className="dataset-publication-empty">暂无发布申请</div> : <>
                <label>选择申请<select aria-label="选择发布申请" value={selectedPublicationRequestID} onChange={event => { setSelectedPublicationRequestID(event.target.value); setPublicationDecisionNote(''); setFormError('') }}>{publicationRequests.map(request => <option key={request.id} value={request.id}>{publicationStatusLabels[request.status]} · 草稿记录 V{request.expectedDraftRecordVersion} · {new Date(request.submittedAt).toLocaleString('zh-CN', { hour12: false })}</option>)}</select></label>
                {selectedPublicationRequest && <dl>
                  <div><dt>申请状态</dt><dd><span className={`dataset-publication-status ${selectedPublicationRequest.status.toLowerCase()}`}>{publicationStatusLabels[selectedPublicationRequest.status]}</span></dd></div>
                  <div><dt>冻结草稿</dt><dd>{selectedPublicationRequest.draftVersionId}</dd></div>
                  <div><dt>DSL 摘要</dt><dd>{selectedPublicationRequest.expectedDslHash.slice(0, 16)}…</dd></div>
                  <div><dt>指标候选</dt><dd>{metricCandidateGenerationLabels[selectedPublicationRequest.metricCandidateStatus] ?? selectedPublicationRequest.metricCandidateStatus} · 共 {selectedPublicationRequest.metricCandidateTotal ?? 0} 项</dd></div>
                  <div><dt>申请说明</dt><dd>{selectedPublicationRequest.requestNote || '未填写'}</dd></div>
                  {selectedPublicationRequest.reviewNote && <div><dt>审批意见</dt><dd>{selectedPublicationRequest.reviewNote}</dd></div>}
                  {selectedPublicationRequest.publishedVersionId && <div><dt>发布版本</dt><dd>{selectedPublicationRequest.publishedVersionId}</dd></div>}
                </dl>}
                {selectedPublicationRequest?.status === 'PENDING' && <>
                  <label>审批意见<textarea value={publicationDecisionNote} onChange={event => setPublicationDecisionNote(event.target.value)} placeholder="通过时可选；拒绝时必须说明原因" /></label>
                  <div className="dataset-publication-review-actions"><button className="dataset-publication-reject" type="button" disabled={actionBusy || !publicationCapabilities.publish || !publicationDecisionNote.trim()} onClick={() => void rejectPublicationRequest()}>{busyAction === 'publication-reject' ? '正在拒绝…' : '拒绝'}</button><button className="primary-button" type="button" disabled={actionBusy || !publicationCapabilities.publish || !['LEGACY', 'SUCCEEDED', 'PARTIAL'].includes(selectedPublicationRequest.metricCandidateStatus)} title={!['LEGACY', 'SUCCEEDED', 'PARTIAL'].includes(selectedPublicationRequest.metricCandidateStatus) ? '指标候选尚未生成完成' : ''} onClick={() => void approvePublicationRequest()}>{busyAction === 'publication-approve' ? '正在校验并发布…' : '审批通过并发布'}</button></div>
                </>}
              </>}
            </section>
          </div>

          {metricReturnTo && currentDraftPublicationRequest?.status === 'APPROVED' && <section className="dataset-publication-metric-return" aria-label="继续指标设计"><div><strong>数据集已可用于指标设计</strong><small>指标 AI 将只读取本次审批生成的精确已发布版本。</small></div><button className="primary-button" type="button" onClick={() => navigate(metricReturnTo, { state: { metricAIRequirement, ...(metricPreferredDatasetId ? { preferredDatasetId: metricPreferredDatasetId } : {}), ...(metricSafeDatasetExtension ? { safeDatasetExtension: true } : {}) } })}>返回资产管理中心继续生成</button></section>}
          {formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}
          <footer className="dataset-publication-footer"><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>关闭</button></footer>
        </> : <div className="dataset-center-feedback error" role="alert">{formError || '无法加载数据集发布信息'}</div>}
      </div>
    </Dialog>}

    {dialog?.mode === 'disable' && dialog.dataset && <Dialog title="停用数据集" eyebrow="生命周期操作" onClose={closeDialog}><div className="dataset-delete-confirm"><p>确认停用“<strong>{dialog.dataset.name}</strong>”吗？</p><small>停用会阻止新的查询绑定；草稿、发布快照与历史审计都会保留，之后可以恢复。</small>{formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}<footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</button><button className="primary-button" type="button" disabled={actionBusy} onClick={() => void disableDataset()}>{busyAction ? '正在停用…' : '确认停用'}</button></footer></div></Dialog>}

    {dialog?.mode === 'restore' && dialog.dataset && <Dialog title="恢复数据集" eyebrow="生命周期操作" onClose={closeDialog}><div className="dataset-delete-confirm"><p>确认恢复“<strong>{dialog.dataset.name}</strong>”吗？</p><small>系统会优先恢复到停用前的发布、失效或草稿状态；迁移前没有可靠状态记录的数据集将安全恢复为草稿。</small>{formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}<footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</button><button className="primary-button" type="button" disabled={actionBusy} onClick={() => void restoreDataset()}>{busyAction ? '正在恢复…' : '确认恢复'}</button></footer></div></Dialog>}

    {dialog?.mode === 'delete' && dialog.dataset && <Dialog title="删除数据集" eyebrow="危险操作" onClose={closeDialog}><div className="dataset-delete-confirm"><p>确认删除“<strong>{dialog.dataset.name}</strong>”吗？数据集会从资产清单中移除，历史审计仍会保留。</p><small>仍被指标、下游数据集、报告草稿或运行中查询占用时，系统会拒绝删除。</small>{formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}<footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</button><button className="dataset-delete-button" type="button" disabled={actionBusy} onClick={() => void deleteDataset()}>{busyAction ? '正在删除…' : '确认删除'}</button></footer></div></Dialog>}
  </AppShell>
}

function DatasetAIComposer({ prompt, lastInstruction, result, labels, error, busy, applying, applied, detailsExpanded, ready, hasAssets, canUndo, canRetry, retryRequiresGeneration, hasGraph, onPromptChange, onSubmit, onApply, onUndo, onRetryOriginal, onRetryModified, onDismissError, onDetailsExpandedChange }: {
  prompt: string
  lastInstruction: string
  result: DatasetAIPlanResult | null
  labels: DatasetAIReviewLabels
  error: DatasetAIErrorView | null
  busy: boolean
  applying: boolean
  applied: boolean
  detailsExpanded: boolean
  ready: boolean
  hasAssets: boolean
  canUndo: boolean
  canRetry: boolean
  retryRequiresGeneration: boolean
  hasGraph: boolean
  onPromptChange: (value: string) => void
  onSubmit: () => void
  onApply: () => void
  onUndo: () => void
  onRetryOriginal: () => void
  onRetryModified: () => void
  onDismissError: () => void
  onDetailsExpandedChange: (value: boolean) => void
}) {
  const proposal = result?.proposal
  const detailsID = useId()
  const headingID = useId()
  const promptRef = useRef<HTMLTextAreaElement>(null)
  const actionLabel = busy ? '正在生成…' : proposal ? '继续修改' : hasGraph ? 'AI 修改流程' : 'AI 生成流程'
  const hasNoChanges = proposal?.mode === 'MODIFY' && proposal.changeSet.operations.length === 0
  const actionsBusy = busy || applying
  const canApply = Boolean(proposal) && !applied && !hasNoChanges && !actionsBusy && !retryRequiresGeneration
  const canUseUndo = canUndo && (!proposal || applied) && !actionsBusy
  const promptChanged = Boolean(prompt.trim()) && prompt.trim() !== lastInstruction.trim()
  const retryLabel = !canRetry ? '重试' : retryRequiresGeneration ? promptChanged ? '根据修改重新生成' : '按原要求重试' : '重新应用'
  const retryAction = retryRequiresGeneration && promptChanged ? onRetryModified : onRetryOriginal
  const nodeLabel = (nodeID: string) => labels.nodes[nodeID]
    || proposal?.plan.nodes.find(node => node.id === nodeID)?.alias
    || proposal?.plan.transforms?.find(transform => transform.id === nodeID)?.name
    || nodeID
  const fieldLabel = (nodeID: string, column: string) => {
    const transformOutput = proposal?.plan.transforms?.find(transform => transform.id === nodeID)?.rules.find(rule => rule.output.id === column)?.output
    return `${nodeLabel(nodeID)} · ${labels.fields[`${nodeID}.${column}`] || transformOutput?.name || column}`
  }
  const joinMeaning = (joinType: 'INNER' | 'LEFT') => joinType === 'INNER' ? '仅保留两侧匹配数据' : '保留左侧全部数据'
  useLayoutEffect(() => {
    const textarea = promptRef.current
    if (!textarea) return
    textarea.style.height = '0px'
    textarea.style.height = `${Math.min(Math.max(textarea.scrollHeight, 28), 128)}px`
  }, [prompt])
  return <section className={`dataset-ai-composer ${proposal ? 'has-proposal' : ''}`} aria-label="AI 自动配置数据流" onMouseDown={event => event.stopPropagation()} onClick={event => event.stopPropagation()} onDrop={event => event.stopPropagation()}>
    <form onSubmit={event => { event.preventDefault(); onSubmit() }}>
      <span className="dataset-ai-icon" aria-hidden="true"><MagicWandIcon size={19} weight="fill" /></span>
      <label>
        <strong>{hasGraph ? '告诉 AI 接下来怎么改' : '用一句话描述想要的数据结果'}</strong>
        <textarea ref={promptRef} rows={1} aria-label="描述数据集生成或修改目标" maxLength={4000} value={prompt} disabled={busy || applying || !hasAssets || !ready} onChange={event => onPromptChange(event.target.value)} placeholder={hasGraph ? '例如：把客户与订单改为 INNER 关联，按地区汇总订单金额' : '例如：关联客户和订单，按地区汇总订单金额，保留客户名称'} />
      </label>
      <button type="submit" disabled={!hasAssets || !ready || !prompt.trim() || busy || applying}><MagicWandIcon aria-hidden="true" size={15} weight="bold" />{actionLabel}</button>
      <div className="dataset-ai-toolbar" role="toolbar" aria-label="AI 方案操作">
        <button type="button" aria-controls={detailsID} aria-expanded={Boolean(proposal && detailsExpanded)} disabled={!proposal || detailsExpanded} onClick={() => onDetailsExpandedChange(true)}><CaretDownIcon aria-hidden="true" size={14} weight="bold" />展开</button>
        <button type="button" aria-controls={detailsID} aria-expanded={Boolean(proposal && detailsExpanded)} disabled={!proposal || !detailsExpanded} onClick={() => onDetailsExpandedChange(false)}><CaretUpIcon aria-hidden="true" size={14} weight="bold" />折叠</button>
        <button type="button" disabled={!canApply} onClick={onApply}><CheckCircleIcon aria-hidden="true" size={14} weight="bold" />应用</button>
        <button type="button" disabled={!canUseUndo} onClick={onUndo}><ArrowCounterClockwiseIcon aria-hidden="true" size={14} weight="bold" />撤销</button>
        <button type="button" disabled={!canRetry || actionsBusy} onClick={retryAction}><ArrowClockwiseIcon aria-hidden="true" size={14} weight="bold" />{retryLabel}</button>
      </div>
    </form>
    {!ready && <p className="dataset-ai-helper" role="status">正在准备当前画布与可用数据资产…</p>}
    {ready && !hasAssets && <p className="dataset-ai-helper" role="status">请先完成至少一张数据表的 LLM 映射，再使用自动配置。</p>}
    {busy && <div className="dataset-ai-progress" role="status" aria-live="polite"><MagicWandIcon aria-hidden="true" size={18} weight="duotone" /><p><strong>正在理解业务目标并规划 DAG</strong><small>只读取表和字段业务元数据，不发送数据样例；原画布不会被覆盖。</small></p></div>}
    {error && <div className="dataset-ai-error" role="alert">
      <div className="dataset-ai-error-copy">
        <strong>{error.title}</strong>
        <p>{error.message}</p>
        <small><b>处理建议</b>{error.suggestion}</small>
		{(error.code || error.diagnosticCode || error.reasonCode || error.stage || error.repairAttempted !== undefined || error.status || error.requestId) && <dl aria-label="错误诊断信息">
          {error.code && <div><dt>错误码</dt><dd>{error.code}</dd></div>}
			{error.diagnosticCode && <div><dt>校验规则</dt><dd>{error.diagnosticCode}</dd></div>}
          {error.reasonCode && <div><dt>原因码</dt><dd>{error.reasonCode}</dd></div>}
          {error.stage && <div><dt>失败阶段</dt><dd>{error.stage}</dd></div>}
          {error.repairAttempted !== undefined && <div><dt>自动修复</dt><dd>{error.repairAttempted ? '已尝试' : '未尝试'}</dd></div>}
          {error.status && <div><dt>HTTP</dt><dd>{error.status}</dd></div>}
          {error.requestId && <div><dt>请求标识</dt><dd>{error.requestId}</dd></div>}
        </dl>}
      </div>
      <div className="dataset-ai-error-actions" aria-label="错误恢复操作">
        {canRetry && retryRequiresGeneration && <button type="button" disabled={actionsBusy || !lastInstruction.trim()} onClick={onRetryOriginal}><ArrowClockwiseIcon aria-hidden="true" size={14} weight="bold" />按原要求重试</button>}
        {canRetry && retryRequiresGeneration && promptChanged && <button className="is-primary" type="button" disabled={actionsBusy} onClick={onRetryModified}><MagicWandIcon aria-hidden="true" size={14} weight="bold" />根据修改重新生成</button>}
        {canRetry && !retryRequiresGeneration && <button type="button" disabled={actionsBusy} onClick={onRetryOriginal}><ArrowClockwiseIcon aria-hidden="true" size={14} weight="bold" />重新应用</button>}
        <button type="button" disabled={actionsBusy} onClick={onDismissError}>继续手动配置</button>
      </div>
    </div>}
    {proposal && <article className={`dataset-ai-proposal ${detailsExpanded ? '' : 'is-collapsed'}`} aria-labelledby={headingID}>
      <header>
        <div className="dataset-ai-proposal-heading"><span aria-live="polite" aria-atomic="true">{applied ? '已应用到画布' : proposal.mode === 'CREATE' ? '待确认的新方案' : '待确认的修改方案'}</span><h3 id={headingID}>{proposal.summary}</h3></div>
        <dl><div><dt>数据节点</dt><dd>{proposal.plan.nodes.length}</dd></div><div><dt>字段处理</dt><dd>{proposal.plan.transforms?.length ?? 0}</dd></div><div><dt>关联</dt><dd>{proposal.plan.joins.length}</dd></div><div><dt>分组</dt><dd>{proposal.plan.groups.length}</dd></div><div><dt>输出</dt><dd>{proposal.plan.end.outputs.length}</dd></div></dl>
      </header>
      <div className="dataset-ai-proposal-details" id={detailsID} hidden={!detailsExpanded}>
        {proposal.mode === 'MODIFY' && <section className="dataset-ai-change-review" aria-label="本次修改">
          <h4>本次修改</h4>
          {proposal.changeSet.operations.length > 0 ? <>
            <p>已按你的要求识别以下变更，未列出的组件保持不变。</p>
            <ul aria-label="本次修改清单">{proposal.changeSet.operations.map((operation, index) => <li key={`${operation.action}:${operation.componentKind}:${operation.componentId}:${index}`}>
              <span className={`is-${operation.action.toLowerCase()}`}>{datasetAIChangeActionLabels[operation.action]}</span>
              <div>
                <strong>{operation.componentName}</strong>
                <small>{datasetAIChangeComponentLabels[operation.componentKind]}{operation.fields.length > 0 ? ` · 修改字段：${operation.fields.map(field => datasetAIChangeFieldLabels[field] || field).join('、')}` : ''}</small>
                <small>{operation.description}</small>
              </div>
            </li>)}</ul>
          </> : <p className="dataset-ai-no-changes" role="status">当前流程已符合要求，无需变更。</p>}
        </section>}
        <section className="dataset-ai-flow-review"><h4>方案流程</h4><ol>
          <li><span>数据</span><strong>{proposal.plan.nodes.map(node => nodeLabel(node.id)).join('、')}</strong></li>
          {(proposal.plan.transforms?.length ?? 0) > 0 && <li><span>处理</span><strong>{proposal.plan.transforms?.map(transform => transform.name).join('、')}</strong></li>}
          {proposal.plan.joins.length > 0 && <li><span>关联</span><strong>{proposal.plan.joins.map(join => join.name).join('、')}</strong></li>}
          {proposal.plan.groups.length > 0 && <li><span>汇总</span><strong>{proposal.plan.groups.map(group => group.name).join('、')}</strong></li>}
          <li><span>输出</span><strong>{proposal.plan.end.outputs.slice(0, 8).map(output => output.name).join('、')}{proposal.plan.end.outputs.length > 8 ? ` 等 ${proposal.plan.end.outputs.length} 项` : ''}</strong></li>
        </ol></section>
        {proposal.plan.joins.length > 0 && <section className="dataset-ai-join-review"><h4>请确认关联字段</h4><p>下面字段决定两张表如何匹配；点击应用即确认这些关联。</p><ul>{proposal.plan.joins.map(join => <li key={join.id}><span>{join.joinType}</span><div><strong>{join.name}<small>{joinMeaning(join.joinType)}</small></strong>{join.conditions.map((condition, index) => <small key={`${join.id}:${index}`}><b>{fieldLabel(condition.leftNodeId, condition.leftColumn)}</b><i>=</i><b>{fieldLabel(condition.rightNodeId, condition.rightColumn)}</b></small>)}</div></li>)}</ul></section>}
        {proposal.plan.groups.length > 0 && <section className="dataset-ai-group-review"><h4>汇总口径</h4><ul>{proposal.plan.groups.map(group => <li key={group.id}><strong>{group.name}</strong><small>按 {group.dimensions.map(item => fieldLabel(item.nodeId, item.column)).join('、')} 分组</small><small>计算 {group.metrics.map(item => `${fieldLabel(item.nodeId, item.column)} · ${item.aggregation}`).join('、')}</small></li>)}</ul></section>}
        {(proposal.assumptions.length > 0 || proposal.warnings.length > 0) && <section className="dataset-ai-notes"><h4>生成依据</h4>{proposal.assumptions.map(item => <p key={`assumption:${item}`}>{item}</p>)}{proposal.warnings.map(item => <p className="warning" key={`warning:${item}`}>{item}</p>)}</section>}
      </div>
    </article>}
  </section>
}

function RelationCanvas({ nodes, fields, joins, boxes, groups, transforms, end, nodePositions, activeNodeID, activeJoinID, activeGroupID, activeTransformID, activeEnd, tables, isFullscreen, previewTarget, preview, previewLabel, onPreview, onRefreshPreview, onClosePreview, onArrange, onToggleFullscreen, onAddJoin, onAddGroup, onAddTransform, onAddEnd, onAddTable, onMove, onConnect, onConnectGroup, onConnectTransform, onConnectEnd, onRemoveBox, onRemoveGroup, onRemoveTransform, onRemoveEnd, onNodeClick, onJoinClick, onGroupClick, onTransformClick, onEndClick, onRemoveNode }: {
  nodes: DesignerNode[]; fields: FieldOption[]; joins: JoinOption[]; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; end: EndBox | null; nodePositions: Record<string, CanvasPoint>
  activeNodeID: string; activeJoinID: string; activeGroupID: string; activeTransformID: string; activeEnd: boolean; tables: AssetTable[]
  isFullscreen: boolean; previewTarget: CanvasPreviewTarget | null; preview?: NodePreviewState; previewLabel: string
  onPreview: (target: CanvasPreviewTarget) => void; onRefreshPreview: () => void; onClosePreview: () => void; onArrange: () => void; onToggleFullscreen: () => void
  onAddJoin: (position?: CanvasPoint) => void; onAddGroup: (position?: CanvasPoint) => void; onAddTransform: (componentType: GraphTransformComponentType, position?: CanvasPoint) => void; onAddEnd: (position?: CanvasPoint) => void; onAddTable: (table: AssetTable, position: CanvasPoint) => void
  onMove: (kind: CanvasComponentKind, id: string, position: CanvasPoint) => void
  onConnect: (boxID: string, side: 'left' | 'right', input?: RelationInput) => void
  onConnectGroup: (groupID: string, input?: RelationInput) => void; onConnectTransform: (transformID: string, input?: RelationInput) => void; onConnectEnd: (input?: RelationInput) => void
  onRemoveBox: (boxID: string) => void; onRemoveGroup: (groupID: string) => void; onRemoveTransform: (transformID: string) => void; onRemoveEnd: () => void
  onNodeClick: (nodeID: string) => void; onJoinClick: (joinID: string) => void; onGroupClick: (groupID: string) => void; onTransformClick: (transformID: string) => void; onEndClick: () => void; onRemoveNode: (nodeID: string) => void
}) {
  const [draggingConnection, setDraggingConnection] = useState<RelationInput | null>(null)
  const [connectionPoint, setConnectionPoint] = useState<CanvasPoint | null>(null)
  const [sourcePortPositions, setSourcePortPositions] = useState<Record<string, CanvasPoint>>({})
  const [targetPortPositions, setTargetPortPositions] = useState<Record<string, CanvasPoint>>({})
  const canvasRef = useRef<HTMLDivElement>(null)
  const lineLayerRef = useRef<SVGSVGElement>(null)
  const connectionPointerIDRef = useRef<number | null>(null)
  const measureSourcePorts = useCallback(() => {
    const canvas = canvasRef.current
    const layer = lineLayerRef.current
    if (!canvas || !layer) return
    const layerBounds = layer.getBoundingClientRect()
    // JSDOM 等无布局环境返回零尺寸，此时保留确定性的组件尺寸回退值。
    if (layerBounds.width <= 0 || layerBounds.height <= 0) return
    const next: Record<string, CanvasPoint> = {}
    const nextTargets: Record<string, CanvasPoint> = {}
    canvas.querySelectorAll<HTMLButtonElement>('.output-port[data-source-key]').forEach(port => {
      const key = port.dataset.sourceKey
      const bounds = port.getBoundingClientRect()
      if (!key || bounds.width <= 0 || bounds.height <= 0) return
      next[key] = {
        // output-port 是组件右半区的大热区，连线从卡片右边缘发出。
        x: bounds.right - layerBounds.left - 1,
        y: bounds.top + bounds.height / 2 - layerBounds.top,
      }
    })
    const measureTarget = (port: HTMLButtonElement | null, key: string) => {
      if (!port) return
      const bounds = port.getBoundingClientRect()
      if (bounds.width <= 0 || bounds.height <= 0) return
      nextTargets[key] = {
        x: bounds.left - layerBounds.left,
        y: bounds.top + bounds.height / 2 - layerBounds.top,
      }
    }
    canvas.querySelectorAll<HTMLElement>('.dataset-canvas-component.relation').forEach((component, index) => {
      const box = boxes[index]
      if (!box) return
      measureTarget(component.querySelector<HTMLButtonElement>('.input-port.slot-one'), `JOIN:${box.id}:left`)
      measureTarget(component.querySelector<HTMLButtonElement>('.input-port.slot-two'), `JOIN:${box.id}:right`)
    })
    canvas.querySelectorAll<HTMLElement>('.dataset-canvas-component.group').forEach((component, index) => {
      if (groups[index]) measureTarget(component.querySelector<HTMLButtonElement>('.input-port'), `GROUP:${groups[index].id}:input`)
    })
    canvas.querySelectorAll<HTMLElement>('.dataset-canvas-component.transform').forEach((component, index) => {
      if (transforms[index]) measureTarget(component.querySelector<HTMLButtonElement>('.input-port'), `TRANSFORM:${transforms[index].id}:input`)
    })
    if (end) measureTarget(canvas.querySelector<HTMLButtonElement>('.dataset-canvas-component.end .input-port'), `END:${end.id}:input`)
    setSourcePortPositions(current => {
      const keys = Object.keys(next)
      if (keys.length === Object.keys(current).length && keys.every(key => current[key]?.x === next[key].x && current[key]?.y === next[key].y)) return current
      return next
    })
    setTargetPortPositions(current => {
      const keys = Object.keys(nextTargets)
      if (keys.length === Object.keys(current).length && keys.every(key => current[key]?.x === nextTargets[key].x && current[key]?.y === nextTargets[key].y)) return current
      return nextTargets
    })
  }, [boxes, end, groups, transforms])
  useLayoutEffect(() => {
    measureSourcePorts()
    const canvas = canvasRef.current
    if (!canvas || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measureSourcePorts)
    observer.observe(canvas)
    canvas.querySelectorAll<HTMLElement>('.dataset-canvas-component').forEach(component => observer.observe(component))
    return () => observer.disconnect()
  }, [boxes, end, groups, isFullscreen, measureSourcePorts, nodePositions, nodes, transforms])
  const positionOf = (input: RelationInput): CanvasPoint | undefined => input.kind === 'NODE' ? nodePositions[input.id] : input.kind === 'GROUP' ? groups.find(group => group.id === input.id)?.position : input.kind === 'TRANSFORM' ? transforms.find(transform => transform.id === input.id)?.position : boxes.find(box => box.id === input.id)?.position
  const inputLabel = (input?: RelationInput) => {
    if (!input) return '未配置'
    if (input.kind === 'NODE') {
      const node = nodes.find(item => item.id === input.id)
      return node ? nodeLabel(node) : '节点已不可用'
    }
    if (input.kind === 'GROUP') return groups.find(group => group.id === input.id)?.name || '分组产物已不可用'
    if (input.kind === 'TRANSFORM') return transforms.find(transform => transform.id === input.id)?.name || '字段处理产物已不可用'
    return boxes.find(box => box.id === input.id)?.name || '关联产物已不可用'
  }
  const dropOnCanvas = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault(); event.stopPropagation()
    const bounds = event.currentTarget.getBoundingClientRect()
    // 测试环境可能不提供 DragEvent 坐标；真实浏览器使用实际落点，缺失时回退到
    // 画布中部，避免生成 NaN 导致组件不可见。
    const clientX = Number.isFinite(Number(event.clientX)) ? Number(event.clientX) : bounds.left + 610
    const clientY = Number.isFinite(Number(event.clientY)) ? Number(event.clientY) : bounds.top + 260
    const point = { x: clientX - bounds.left + (event.currentTarget.scrollLeft || 0) - 100, y: clientY - bounds.top + (event.currentTarget.scrollTop || 0) - 55 }
    const moved = event.dataTransfer.getData('text/dataset-canvas-item')
    if (moved) {
      try { const input = JSON.parse(moved) as { kind: CanvasComponentKind; id: string }; onMove(input.kind, input.id, point) } catch { /* 忽略无效的画布拖拽数据 */ }
      return
    }
    const component = event.dataTransfer.getData('text/dataset-component')
    if (component === 'JOIN') { onAddJoin(point); return }
    if (component === 'GROUP') { onAddGroup(point); return }
    if (component.startsWith('TRANSFORM:')) { onAddTransform(component.slice('TRANSFORM:'.length) as GraphTransformComponentType, point); return }
    if (component === 'END') { onAddEnd(point); return }
    const table = tables.find(item => item.id === event.dataTransfer.getData('text/dataset-table-id'))
    if (table) onAddTable(table, point)
    setDraggingConnection(null); setConnectionPoint(null)
  }
  const dragConnection = (event: DragEvent<HTMLElement>, input: RelationInput) => {
    event.stopPropagation()
    event.dataTransfer.effectAllowed = 'link'
    event.dataTransfer.setData('text/dataset-relation-input', JSON.stringify(input))
    setDraggingConnection(input)
    setConnectionPoint(null)
  }
  const beginPointerConnection = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || !event.isPrimary) return
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>('.output-side[data-source-key]') : null
    if (!target || target.getAttribute('aria-disabled') === 'true' || !target.draggable) return
    const sourceKey = target.dataset.sourceKey
    const candidates: RelationInput[] = [
      ...nodes.map(node => ({ kind: 'NODE' as const, id: node.id })),
      ...boxes.map(box => ({ kind: 'JOIN' as const, id: box.id })),
      ...groups.map(group => ({ kind: 'GROUP' as const, id: group.id })),
      ...transforms.map(transform => ({ kind: 'TRANSFORM' as const, id: transform.id })),
    ]
    const input = candidates.find(candidate => graphInputKey(candidate) === sourceKey)
    if (!input) return
    event.preventDefault()
    event.stopPropagation()
    const bounds = event.currentTarget.getBoundingClientRect()
    connectionPointerIDRef.current = event.pointerId
    setDraggingConnection(input)
    setConnectionPoint({
      x: event.clientX - bounds.left + event.currentTarget.scrollLeft,
      y: event.clientY - bounds.top + event.currentTarget.scrollTop,
    })
  }
  const relationInputFromDrop = (event: DragEvent<HTMLElement>): RelationInput | null => {
    try {
      const value = JSON.parse(event.dataTransfer.getData('text/dataset-relation-input')) as RelationInput
      return value && ['NODE', 'JOIN', 'GROUP', 'TRANSFORM'].includes(value.kind) && typeof value.id === 'string' ? value : null
    } catch { return null }
  }
  const acceptConnectionOver = (event: DragEvent<HTMLElement>) => {
    if (!draggingConnection) return
    event.preventDefault(); event.stopPropagation()
    event.dataTransfer.dropEffect = 'link'
  }
  const finishConnectionDrop = (event: DragEvent<HTMLElement>, connect: (input: RelationInput) => void) => {
    const input = relationInputFromDrop(event)
    if (!input) return
    event.preventDefault(); event.stopPropagation()
    connect(input)
    setDraggingConnection(null); setConnectionPoint(null)
  }
  const dropOnJoinNode = (event: DragEvent<HTMLElement>, box: RelationBox) => finishConnectionDrop(event, input => {
    const bounds = event.currentTarget.getBoundingClientRect()
    const side = !box.left ? 'left' : !box.right ? 'right' : event.clientY - bounds.top < bounds.height / 2 ? 'left' : 'right'
    onConnect(box.id, side, input)
  })
  const dropConnection = (event: DragEvent<HTMLButtonElement>, boxID: string, side: 'left' | 'right') => {
    event.preventDefault(); event.stopPropagation()
    try { onConnect(boxID, side, JSON.parse(event.dataTransfer.getData('text/dataset-relation-input')) as RelationInput) } catch { /* 非连接拖拽不改变槽位 */ }
    setDraggingConnection(null); setConnectionPoint(null)
  }
  const dropGroupConnection = (event: DragEvent<HTMLButtonElement>, groupID: string) => {
    event.preventDefault(); event.stopPropagation()
    try { onConnectGroup(groupID, JSON.parse(event.dataTransfer.getData('text/dataset-relation-input')) as RelationInput) } catch { /* 非连接拖拽不改变分组输入 */ }
    setDraggingConnection(null); setConnectionPoint(null)
  }
  const dropTransformConnection = (event: DragEvent<HTMLButtonElement>, transformID: string) => {
    event.preventDefault(); event.stopPropagation()
    try { onConnectTransform(transformID, JSON.parse(event.dataTransfer.getData('text/dataset-relation-input')) as RelationInput) } catch { /* 非连接拖拽不改变字段处理输入 */ }
    setDraggingConnection(null); setConnectionPoint(null)
  }
  const dropEndConnection = (event: DragEvent<HTMLButtonElement>) => {
    event.preventDefault(); event.stopPropagation()
    try { onConnectEnd(JSON.parse(event.dataTransfer.getData('text/dataset-relation-input')) as RelationInput) } catch { /* 非连接拖拽不改变结束节点输入 */ }
    setDraggingConnection(null); setConnectionPoint(null)
  }
  useEffect(() => {
    if (!draggingConnection || connectionPointerIDRef.current === null) return
    const canvas = canvasRef.current
    if (!canvas) return
    const moveConnection = (event: globalThis.PointerEvent) => {
      if (event.pointerId !== connectionPointerIDRef.current) return
      event.preventDefault()
      const bounds = canvas.getBoundingClientRect()
      setConnectionPoint({ x: event.clientX - bounds.left + canvas.scrollLeft, y: event.clientY - bounds.top + canvas.scrollTop })
    }
    const finishPointerConnection = (event: globalThis.PointerEvent) => {
      if (event.pointerId !== connectionPointerIDRef.current) return
      const element = document.elementFromPoint(event.clientX, event.clientY)
      const inputPort = element instanceof Element ? element.closest<HTMLButtonElement>('.input-side') : null
      const component = inputPort?.closest<HTMLElement>('.dataset-canvas-component')
      if (inputPort && component && canvas.contains(component)) {
        event.preventDefault()
        if (component.classList.contains('relation')) {
          const relationCards = Array.from(canvas.querySelectorAll<HTMLElement>('.dataset-canvas-component.relation'))
          const box = boxes[relationCards.indexOf(component)]
          if (box) onConnect(box.id, inputPort.classList.contains('slot-two') ? 'right' : 'left', draggingConnection)
        } else if (component.classList.contains('group')) {
          const groupCards = Array.from(canvas.querySelectorAll<HTMLElement>('.dataset-canvas-component.group'))
          const group = groups[groupCards.indexOf(component)]
          if (group) onConnectGroup(group.id, draggingConnection)
        } else if (component.classList.contains('transform')) {
          const transformCards = Array.from(canvas.querySelectorAll<HTMLElement>('.dataset-canvas-component.transform'))
          const transform = transforms[transformCards.indexOf(component)]
          if (transform) onConnectTransform(transform.id, draggingConnection)
        } else if (component.classList.contains('end')) onConnectEnd(draggingConnection)
      }
      connectionPointerIDRef.current = null
      setDraggingConnection(null)
      setConnectionPoint(null)
    }
    const cancelPointerConnection = (event: globalThis.PointerEvent) => {
      if (event.pointerId !== connectionPointerIDRef.current) return
      connectionPointerIDRef.current = null
      setDraggingConnection(null)
      setConnectionPoint(null)
    }
    window.addEventListener('pointermove', moveConnection, { passive: false })
    window.addEventListener('pointerup', finishPointerConnection, { passive: false })
    window.addEventListener('pointercancel', cancelPointerConnection)
    return () => {
      window.removeEventListener('pointermove', moveConnection)
      window.removeEventListener('pointerup', finishPointerConnection)
      window.removeEventListener('pointercancel', cancelPointerConnection)
    }
  }, [boxes, draggingConnection, groups, onConnect, onConnectEnd, onConnectGroup, onConnectTransform, transforms])
  const sourcePort = (input: RelationInput, position: CanvasPoint) => ({
    // 首次布局前保留稳定回退；布局完成后使用真实端口圆心，避免内容把卡片撑高
    // 时曲线仍按 min-height 猜测位置。
    ...(sourcePortPositions[graphInputKey(input)] ?? {
      x: position.x + (input.kind === 'NODE' ? 210 : input.kind === 'GROUP' ? 190 : input.kind === 'TRANSFORM' ? 200 : 180) + 1,
      y: position.y + (input.kind === 'NODE' ? 56 : input.kind === 'GROUP' || input.kind === 'TRANSFORM' ? 58 : 75),
    }),
  })
  const edge = (input: RelationInput, targetKey: string, fallbackTarget: CanvasPoint) => {
    const position = positionOf(input)
    if (!position) return null
    const start = sourcePort(input, position)
    // 输入热区高度会随卡片内容变化；优先使用真实 DOM 边界，避免按固定高度猜终点。
    const geometry = curveGeometry(start, targetPortPositions[targetKey] ?? fallbackTarget)
    return { path: geometry.path, deletePosition: geometry.midpoint }
  }
  const edges = boxes.flatMap((box, boxIndex) => {
    const target = box.position ?? { x: 510, y: 150 }
    return ([box.left, box.right] as Array<RelationInput | undefined>).flatMap((input, slot) => {
      if (!input) return []
      const side = slot === 0 ? 'left' : 'right'
      const geometry = edge(input, `JOIN:${box.id}:${side}`, { x: target.x, y: target.y + (slot === 0 ? 43 : 82) })
      return geometry ? [{ key: `${box.id}-${slot}`, source: input, geometry, label: `删除关联节点 ${boxIndex + 1} 槽位 ${slot + 1} 连线`, remove: () => onConnect(box.id, slot === 0 ? 'left' : 'right') }] : []
    })
  })
  for (const group of groups) if (group.input) {
    const geometry = edge(group.input, `GROUP:${group.id}:input`, { x: group.position.x, y: group.position.y + 58 })
    if (geometry) edges.push({ key: `${group.id}-input`, source: group.input, geometry, label: `删除“${group.name}”输入连线`, remove: () => onConnectGroup(group.id) })
  }
  for (const transform of transforms) if (transform.input) {
    const geometry = edge(transform.input, `TRANSFORM:${transform.id}:input`, { x: transform.position.x, y: transform.position.y + 58 })
    if (geometry) edges.push({ key: `${transform.id}-input`, source: transform.input, geometry, label: `删除“${transform.name}”输入连线`, remove: () => onConnectTransform(transform.id) })
  }
  if (end?.input) {
    const geometry = edge(end.input, `END:${end.id}:input`, { x: end.position.x, y: end.position.y + 58 })
    if (geometry) edges.push({ key: 'end-input', source: end.input, geometry, label: '删除结束节点输入连线', remove: () => onConnectEnd() })
  }
  const draggingPosition = draggingConnection ? positionOf(draggingConnection) : undefined
  const draggingStart = draggingConnection && draggingPosition ? {
    ...sourcePort(draggingConnection, draggingPosition),
  } : null
  const componentPositions = [
    ...nodes.map((node, index) => nodePositions[node.id] ?? { x: 42, y: 58 + index * 145 }),
    ...boxes.map((box, index) => box.position ?? { x: 510 + index * 250, y: 150 }),
    ...groups.map(group => group.position),
    ...transforms.map(transform => transform.position),
    ...(end ? [end.position] : []),
  ]
  const canvasExtent = {
    width: Math.max(1400, ...componentPositions.map(position => position.x + 330)),
    height: Math.max(800, ...componentPositions.map(position => position.y + 220)),
  }
  const previewTrigger = (target: CanvasPreviewTarget, label: string) => <button
    className="dataset-component-preview-trigger"
    type="button"
    draggable={false}
    aria-label={`预览${label}数据`}
    onDragStart={event => { event.preventDefault(); event.stopPropagation() }}
    onClick={event => { event.stopPropagation(); onPreview(target) }}
  ><span>点击预览</span><small>前 5 行</small></button>
  return <section className="dataset-component-builder" onClick={event => event.stopPropagation()}>
    <aside className="dataset-component-toolbar" aria-label="画布组件栏">
      <div><strong>组件</strong><small>点击组件进行配置；从右半区拖线，放到下游左半区完成连接</small></div>
      <div className="dataset-component-palette">
        <section className="dataset-component-palette-group component-flow" aria-label="流程组件">
          <header><strong>流程组件</strong><small>按拼音 A–Z</small></header>
          <button type="button" draggable onDragStart={event => event.dataTransfer.setData('text/dataset-component', 'GROUP')} onClick={() => onAddGroup()}><RowsIcon data-component-icon="GROUP" aria-hidden="true" size={18} weight="bold" /><strong>分组组件</strong><small>可添加多个 / 分组聚合</small></button>
          <button type="button" draggable onDragStart={event => event.dataTransfer.setData('text/dataset-component', 'JOIN')} onClick={() => onAddJoin()}><GitMergeIcon data-component-icon="JOIN" aria-hidden="true" size={18} weight="bold" /><strong>关联组件</strong><small>双输入 / 可继续连接</small></button>
          <button type="button" draggable={!end} disabled={Boolean(end)} onDragStart={event => event.dataTransfer.setData('text/dataset-component', 'END')} onClick={() => onAddEnd()}><CheckCircleIcon data-component-icon="END" aria-hidden="true" size={18} weight="bold" /><strong>结束节点</strong><small>唯一 / 定义最终输出</small></button>
        </section>
        {transformCategoryMeta.map(category => <section key={category.category} className={`dataset-component-palette-group ${category.className}`} aria-label={category.label}>
          <header><strong>{category.label}</strong><small>按拼音 A–Z</small></header>
          {transformComponentMeta.filter(item => item.category === category.category).sort((left, right) => left.sortKey.localeCompare(right.sortKey, 'en')).map(item => { const ComponentIcon = item.icon; return <button key={item.componentType} type="button" draggable onDragStart={event => event.dataTransfer.setData('text/dataset-component', `TRANSFORM:${item.componentType}`)} onClick={() => onAddTransform(item.componentType)}><ComponentIcon data-component-icon={item.componentType} aria-hidden="true" size={18} weight="bold" /><strong>{item.label}</strong><small>{item.description}</small></button> })}
        </section>)}
      </div>
    </aside>
    <div className="dataset-component-canvas-frame">
      <div className="dataset-canvas-actions" role="toolbar" aria-label="画布工具">
        <button type="button" onClick={onArrange}><TreeStructureIcon aria-hidden="true" size={15} weight="bold" /><span>整理</span></button>
        <button type="button" aria-pressed={isFullscreen} onClick={onToggleFullscreen}>{isFullscreen ? <ArrowsInSimpleIcon aria-hidden="true" size={15} weight="bold" /> : <ArrowsOutSimpleIcon aria-hidden="true" size={15} weight="bold" />}<span>{isFullscreen ? '退出全屏' : '全屏'}</span></button>
      </div>
      <div ref={canvasRef} className="dataset-component-canvas" aria-label="关系组件画布" onPointerDown={beginPointerConnection} onDragOver={event => { event.preventDefault(); if (draggingConnection) { const bounds = event.currentTarget.getBoundingClientRect(); setConnectionPoint({ x: event.clientX - bounds.left + (event.currentTarget.scrollLeft || 0), y: event.clientY - bounds.top + (event.currentTarget.scrollTop || 0) }) } }} onDrop={dropOnCanvas}>
      <svg ref={lineLayerRef} className="dataset-component-lines" style={canvasExtent} aria-hidden="true"><defs><marker id="dataset-edge-arrow" markerWidth="10" markerHeight="10" refX="8.5" refY="5" orient="auto" markerUnits="userSpaceOnUse"><path d="M0,0 L10,5 L0,10 Z" /></marker></defs>{edges.map(item => <path className="dataset-flow-edge" data-source-key={graphInputKey(item.source)} key={item.key} d={item.geometry.path} markerEnd="url(#dataset-edge-arrow)" />)}{draggingStart && connectionPoint && <path className="preview" d={curveGeometry(draggingStart, connectionPoint).path} markerEnd="url(#dataset-edge-arrow)" />}</svg>
      {edges.map(item => <button key={`delete-${item.key}`} type="button" className="dataset-line-delete" style={{ left: item.geometry.deletePosition.x, top: item.geometry.deletePosition.y }} aria-label={item.label} onClick={event => { event.stopPropagation(); item.remove() }}><XIcon aria-hidden="true" size={11} weight="bold" /></button>)}
      {nodes.map((node, index) => { const position = nodePositions[node.id] ?? { x: 42, y: 58 + index * 145 }; const nodeFields = fields.filter(field => field.key.startsWith(`${node.id}.`)); return <article key={node.id} role="button" tabIndex={0} aria-label={`配置数据节点 ${index + 1}`} style={{ left: position.x, top: position.y }} className={`dataset-canvas-component data ${activeNodeID === node.id ? 'active' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'NODE', id: node.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onClick={() => onNodeClick(node.id)}><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'NODE', id: node.id })} aria-label={`从数据节点 ${index + 1} 拖出连接`} draggable onDragStart={event => dragConnection(event, { kind: 'NODE', id: node.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>数据节点 {index + 1}</span><button type="button" aria-label={`移除${nodeLabel(node)}`} onClick={event => { event.stopPropagation(); onRemoveNode(node.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{node.table.businessName || node.table.tableName}</strong><small>{node.table.dataSourceName} · {node.alias}</small><footer><span>原始数据</span><b>{nodeFields.filter(field => field.output !== false).length} 字段</b></footer>{previewTrigger({ kind: 'NODE', id: node.id }, `数据节点 ${index + 1}`)}</article> })}
      {boxes.map((box, index) => { const position = box.position; const join = joins.find(item => item.id === box.id); const outputs = relationOutputKeys({ kind: 'JOIN', id: box.id }, boxes, groups, nodes, fields, transforms); const complete = Boolean(box.left && box.right); return <article key={box.id} role="button" tabIndex={0} aria-label={`配置关联 ${index + 1}`} style={{ left: position.x, top: position.y }} className={`dataset-canvas-component relation ${activeJoinID === box.id ? 'active' : ''} ${join?.manualConfirmed ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'JOIN', id: box.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onDragOver={acceptConnectionOver} onDrop={event => dropOnJoinNode(event, box)} onClick={() => onJoinClick(box.id)}><button type="button" className="input-port component-side input-side slot-one" aria-label={`连接到关联节点 ${index + 1} 槽位 1`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropConnection(event, box.id, 'left')} /><button type="button" className="input-port component-side input-side slot-two" aria-label={`连接到关联节点 ${index + 1} 槽位 2`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropConnection(event, box.id, 'right')} /><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'JOIN', id: box.id })} aria-label={`从关联节点 ${index + 1} 拖出连接`} draggable={complete} aria-disabled={!complete} onDragStart={event => complete && dragConnection(event, { kind: 'JOIN', id: box.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>关联组件</span><button type="button" aria-label={`删除关联组件 ${index + 1}`} onClick={event => { event.stopPropagation(); onRemoveBox(box.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{box.name}</strong><small>{join?.joinType ? `${join.joinType} JOIN` : '尚未完成关联'}</small><div><span>槽位 1</span><b>{inputLabel(box.left)}</b></div><div><span>槽位 2</span><b>{inputLabel(box.right)}</b></div><footer><span>{join?.manualConfirmed ? `${joinConditions(join).length} 个关联条件` : '点击完成配置'}</span><b>{outputs.length} 字段</b></footer>{previewTrigger({ kind: 'JOIN', id: box.id }, `关联组件 ${index + 1}`)}</article> })}
      {groups.map((group, index) => { const complete = Boolean(group.input && group.dimensions.length && group.metrics.length && group.metrics.every(metric => metric.aggregation)); return <article key={group.id} role="button" tabIndex={0} aria-label={`打开分组组件 ${index + 1} 配置`} style={{ left: group.position.x, top: group.position.y }} className={`dataset-canvas-component group ${activeGroupID === group.id ? 'active' : ''} ${complete ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'GROUP', id: group.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onDragOver={acceptConnectionOver} onDrop={event => finishConnectionDrop(event, input => onConnectGroup(group.id, input))} onClick={() => onGroupClick(group.id)}><button type="button" className="input-port component-side input-side group-input" aria-label={`连接到分组组件 ${index + 1} 输入槽位`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropGroupConnection(event, group.id)} /><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'GROUP', id: group.id })} aria-label={`从分组组件 ${index + 1} 拖出连接`} draggable={complete} aria-disabled={!complete} onDragStart={event => complete && dragConnection(event, { kind: 'GROUP', id: group.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>分组组件 {index + 1}</span><button type="button" aria-label={`删除分组组件 ${index + 1}`} onClick={event => { event.stopPropagation(); onRemoveGroup(group.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{group.name}</strong><div><span>输入</span><b>{inputLabel(group.input)}</b></div><footer><span>{group.dimensions.length} 个维度</span><b>{group.metrics.length} 个指标</b></footer>{previewTrigger({ kind: 'GROUP', id: group.id }, `分组组件 ${index + 1}`)}</article> })}
      {transforms.map((transform, index) => {
        const complete = Boolean(transform.input && transform.rules.length && transform.rules.every(transformRuleIsComplete))
        const label = transformDisplayLabel(transform)
        return <article key={transform.id} role="button" tabIndex={0} aria-label={`打开${label} ${index + 1} 配置`} style={{ left: transform.position.x, top: transform.position.y }} className={`dataset-canvas-component transform ${transformColorClass(transform)} ${activeTransformID === transform.id ? 'active' : ''} ${complete ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'TRANSFORM', id: transform.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onDragOver={acceptConnectionOver} onDrop={event => finishConnectionDrop(event, input => onConnectTransform(transform.id, input))} onClick={() => onTransformClick(transform.id)}><button type="button" className="input-port component-side input-side group-input" aria-label={`连接到${label} ${index + 1} 输入槽位`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropTransformConnection(event, transform.id)} /><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'TRANSFORM', id: transform.id })} aria-label={`从${label} ${index + 1} 拖出连接`} draggable={complete} aria-disabled={!complete} onDragStart={event => complete && dragConnection(event, { kind: 'TRANSFORM', id: transform.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>{label}</span><button type="button" aria-label={`删除${label} ${index + 1}`} onClick={event => { event.stopPropagation(); onRemoveTransform(transform.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{transform.name}</strong><div><span>输入</span><b>{inputLabel(transform.input)}</b></div><footer><span>{transform.rules.length} 条规则</span><b>{complete ? '已配置' : '待完善'}</b></footer>{previewTrigger({ kind: 'TRANSFORM', id: transform.id }, `${label} ${index + 1}`)}</article>
      })}
      {end && <article role="button" tabIndex={0} aria-label="打开结束节点配置" style={{ left: end.position.x, top: end.position.y }} className={`dataset-canvas-component end ${activeEnd ? 'active' : ''} ${end.input && end.outputs.length ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => event.dataTransfer.setData('text/dataset-canvas-item', JSON.stringify({ kind: 'END', id: end.id }))} onDragOver={acceptConnectionOver} onDrop={event => finishConnectionDrop(event, onConnectEnd)} onClick={onEndClick}><button type="button" className="input-port component-side input-side group-input" aria-label="连接到结束节点输入槽位" onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={dropEndConnection} /><header><span>结束节点</span><button type="button" aria-label="删除结束节点" onClick={event => { event.stopPropagation(); onRemoveEnd() }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{end.name}</strong><div><span>最终输入</span><b>{inputLabel(end.input)}</b></div><footer><span>输出结果</span><b>{end.outputs.length} 个字段</b></footer>{previewTrigger({ kind: 'END', id: end.id }, '结束节点')}</article>}
      {!boxes.length && !groups.length && !transforms.length && !end && <div className="dataset-component-canvas-hint"><strong>拖入组件建立数据流</strong><p>数据节点、字段处理、分组、关联与结束节点之间会用有方向的曲线连接。</p></div>}
      </div>
      {previewTarget && <CanvasPreviewDialog label={previewLabel} preview={preview} onRefresh={onRefreshPreview} onClose={onClosePreview} />}
    </div>
  </section>
}

function NodeConfigDrawer({ node, fields, onFieldPatch, onDone }: {
  node: DesignerNode; fields: FieldOption[]
  onFieldPatch: (key: string, patch: Partial<FieldOption>) => void; onDone: () => void
}) {
  const optionFor = (column: AssetColumn) => fields.find(field => field.key === `${node.id}.${column.columnName}`) ?? fieldOption(node, column)
  return <aside className="dataset-canvas-drawer" aria-label={`配置表 ${node.table.businessName || node.table.tableName}`} onClick={event => event.stopPropagation()}>
    <header><div><span>数据节点</span><strong>{node.table.businessName || node.table.tableName}</strong><small>{node.table.schemaName}.{node.table.tableName}</small></div><button type="button" aria-label="保存并关闭表配置" onClick={onDone}>×</button></header>
    <section><div className="dataset-drawer-title"><div><h3>输出字段</h3><p>数据节点只负责投影；分组与聚合请连接独立分组组件。</p></div><span>{fields.filter(field => field.output !== false).length} 已选</span></div><div className="dataset-drawer-field-list">{node.columns.map(column => { const option = optionFor(column); return <label key={column.id}><input aria-label={`输出字段 ${column.columnName}`} type="checkbox" checked={option.output !== false} onChange={event => onFieldPatch(option.key, { output: event.target.checked })} /><span><strong>{column.businessName || column.columnName}</strong><small>{column.columnName} · {column.canonicalType}</small></span></label> })}</div></section>
    <footer><small>点击画板空白处也会自动保存并收起</small><button type="button" onClick={onDone}>完成</button></footer>
  </aside>
}

function GroupingConfigDrawer({ box, boxes, groups, transforms, nodes, availableFields, error, onNameChange, onDimensionChange, onMetricChange, onDone }: {
  box: GroupBox; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; nodes: DesignerNode[]; availableFields: ProducedField[]
  error: string; onNameChange: (name: string) => void
  onDimensionChange: (field: ProducedField, enabled: boolean) => void
  onMetricChange: (field: ProducedField, enabled: boolean, patch?: { aggregation?: string }) => void
  onDone: () => void
}) {
  const shape = graphShape(boxes, groups, transforms)
  return <aside className="dataset-canvas-drawer output" aria-label="配置分组组件" onClick={event => event.stopPropagation()}>
    <header><div><span>分组组件</span><strong>{box.name}</strong><small>先定义输入粒度，再为下游自动生成带稳定别名的维度和指标</small></div><button type="button" aria-label="保存并关闭分组配置" onClick={onDone}>×</button></header>
    <section><h3>组件与产物</h3><div className="dataset-group-input"><label><span>产物名称</span><input aria-label="分组产物名称" value={box.name} onChange={event => onNameChange(event.target.value)} placeholder="例如：客户月度汇总" /></label><div aria-label="分组组件输入" className={`dataset-connected-input ${box.input ? 'connected' : 'empty'}`}><span>输入组件</span><strong>{relationInputLabel(box.input, nodes, boxes, groups, transforms)}</strong><small>{box.input ? '输入由画布连线确定；删除连线后可重新连接' : '请回到画布，从上游组件拖线到该组件输入端口'}</small></div></div></section>
    <section><div className="dataset-drawer-title"><div><h3>分组字段</h3><p>可多选；这里只确定分组维度，日期年月日请先连接独立日期转换组件。</p></div><span>{box.dimensions.length} 已选</span></div><div className="dataset-drawer-field-list configured">{availableFields.map(field => { const configured = box.dimensions.find(item => item.key === field.key); return <div className={configured ? 'selected' : ''} key={field.key}><label><input aria-label={`分组维度 ${field.code}`} type="checkbox" checked={Boolean(configured)} onChange={event => onDimensionChange(field, event.target.checked)} /><span><strong>{field.name}</strong><small>{graphProducedFieldLabel(field)}</small></span></label>{configured && <div className="dataset-product-fields generated"><output className="dataset-generated-field-alias" aria-label={`${field.code} 字段别名`}><small>字段别名</small><strong>{configured.code}</strong></output></div>}</div> })}</div></section>
    <section><div className="dataset-drawer-title"><div><h3>聚合指标</h3><p>选择字段与计算逻辑；字段别名由规则自动生成并保持稳定。</p></div><span>{box.metrics.length} 已选</span></div><div className="dataset-drawer-field-list configured metrics">{availableFields.map(field => { const configured = box.metrics.find(item => item.key === field.key); const numeric = ['NUMBER', 'INT', 'INTEGER', 'DECIMAL', 'FLOAT', 'DOUBLE'].includes(field.canonicalType.toUpperCase()); return <div className={configured ? 'selected' : ''} key={field.key}><label><input aria-label={`聚合指标 ${field.code}`} type="checkbox" checked={Boolean(configured)} onChange={event => onMetricChange(field, event.target.checked)} /><span><strong>{field.name}</strong><small>{graphProducedFieldLabel(field)}</small></span></label>{configured && <div className="dataset-product-fields generated"><select aria-label={`${field.code} 聚合逻辑`} value={configured.aggregation} onChange={event => onMetricChange(field, true, { aggregation: event.target.value })}><option value="">选择逻辑</option>{numeric && <><option>SUM</option><option>AVG</option></>}<option>COUNT</option><option>COUNT_DISTINCT</option><option>MIN</option><option>MAX</option></select><output className="dataset-generated-field-alias" aria-label={`${field.code} 字段别名`}><small>字段别名</small><strong>{configured.code}</strong></output></div>}</div> })}</div></section>
    <footer>{error && <span className="dataset-drawer-error" role="alert">{error}</span>}<small>{graphLeaves({ kind: 'GROUP', id: box.id }, shape).length ? '该分组产物可继续连接关联组件或结束节点' : '请先连接输入组件'}</small><button type="button" onClick={onDone}>完成</button></footer>
  </aside>
}

function TransformConfigDrawer({ transform, inputs, nodes, boxes, groups, transforms, error, onNameChange, onRuleChange, onAddRule, onRemoveRule, onDone }: {
  transform: TransformBox; inputs: ProducedField[]; nodes: DesignerNode[]; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; error: string
  onNameChange: (name: string) => void; onRuleChange: (ruleID: string, patch: Partial<GraphTransformRule>) => void
  onAddRule: () => void; onRemoveRule: (ruleID: string) => void; onDone: () => void
}) {
  const candidates = transformFieldCandidates(transform.family, inputs)
  const options = candidates
  const componentMeta = transformComponentMetaFor(transform)
  const componentLabel = transformDisplayLabel(transform)
  const operations = transformOperations(transform)
  const changePrimaryInput = (rule: GraphTransformRule, key: string) => {
    const previous = inputs.find(field => field.key === rule.inputKeys[0])
    const source = inputs.find(field => field.key === key)
    const patch: Partial<GraphTransformRule> = {
      inputKeys: [key, ...rule.inputKeys.slice(1)],
      ...(rule.replaceSourceKey === rule.inputKeys[0] ? { replaceSourceKey: key } : {}),
    }
    if (rule.operation === 'COALESCE') {
      patch.output = { ...rule.output, canonicalType: source?.canonicalType || 'STRING' }
      if (rule.fallbackMode !== 'FIELD' && (rule.fallbackValue === undefined || rule.fallbackValue === defaultFallbackValue(previous))) {
        patch.fallbackValue = defaultFallbackValue(source)
      }
    }
    onRuleChange(rule.id, patch)
  }
  const changeOperation = (rule: GraphTransformRule, operation: GraphTransformOperation) => {
    const fallbackMode = operation === 'COALESCE' ? rule.fallbackMode || 'LITERAL' : rule.fallbackMode
    const count = transformRuleInputCount(operation, fallbackMode)
    const first = rule.inputKeys[0] || options[0]?.key || ''
    const inputKeys = count === 1 ? [first] : [first, rule.inputKeys[1] || options[1]?.key || first]
    const source = inputs.find(field => field.key === first)
    const canonicalType = operation === 'CAST' ? rule.targetType || 'STRING' : operation === 'DATE_FORMAT' ? 'STRING' : operation === 'DATE_TRUNC' ? source?.canonicalType || 'DATE' : operation === 'COALESCE' ? source?.canonicalType || 'STRING' : ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'ROUND', 'ABS', 'FLOOR', 'CEIL'].includes(operation) ? 'DECIMAL' : 'STRING'
    onRuleChange(rule.id, {
      operation, inputKeys, output: { ...rule.output, canonicalType },
      ...(operation === 'DATE_FORMAT' || operation === 'DATE_TRUNC' ? { unit: rule.unit || 'DAY' } : {}),
      ...(operation === 'CAST' ? { targetType: rule.targetType || 'STRING' } : {}),
      ...(operation === 'CASE' ? { conditionOperator: rule.conditionOperator || 'EQUALS', matchValue: rule.matchValue ?? '', conditionValues: rule.conditionValues?.length ? rule.conditionValues : [{ id: `condition_value_${rule.id}_1`, mode: 'LITERAL' as const, value: '' }], thenValue: rule.thenValue ?? '', elseValue: rule.elseValue ?? '' } : {}),
      ...(operation === 'COALESCE' ? { fallbackMode, fallbackValue: rule.fallbackValue ?? defaultFallbackValue(source) } : {}),
      ...(operation === 'CONCAT' ? { separator: rule.separator ?? '' } : {}),
      ...(operation === 'ROUND' ? { precision: rule.precision ?? 2 } : {}),
      ...(operation === 'SUBSTRING' ? { start: rule.start || 1, length: rule.length ?? 10 } : {}),
      ...(operation === 'REPLACE' ? { searchValue: rule.searchValue ?? '', replacementValue: rule.replacementValue ?? '' } : {}),
    })
  }
  const conditionCollectionEditor = (rule: GraphTransformRule, ruleIndex: number) => {
    const values = rule.conditionValues?.length ? rule.conditionValues : [{ id: `condition_value_${ruleIndex + 1}_1`, mode: 'LITERAL' as const, value: '' }]
    const updateValue = (id: string, patch: Partial<(typeof values)[number]>) => onRuleChange(rule.id, { conditionValues: values.map(item => item.id === id ? { ...item, ...patch } : item) })
    return <div className="dataset-condition-values" aria-label={`规则 ${ruleIndex + 1} 条件值数组`}>
      <div className="dataset-condition-values-heading"><span>候选值数组</span><small>每项可选上游字段或填写固定值</small></div>
      {values.map((item, valueIndex) => <div className="dataset-condition-value" key={item.id}>
        <select aria-label={`规则 ${ruleIndex + 1} 候选值 ${valueIndex + 1} 来源`} value={item.mode} onChange={event => updateValue(item.id, { mode: event.target.value as 'LITERAL' | 'FIELD', value: '' })}><option value="LITERAL">自定义值</option><option value="FIELD">上游字段</option></select>
        {item.mode === 'FIELD'
          ? <select aria-label={`规则 ${ruleIndex + 1} 候选值 ${valueIndex + 1} 字段`} value={item.value} onChange={event => updateValue(item.id, { value: event.target.value })}><option value="">选择字段</option>{inputs.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select>
          : <input aria-label={`规则 ${ruleIndex + 1} 候选值 ${valueIndex + 1}`} value={item.value} placeholder="输入一个候选值" onChange={event => updateValue(item.id, { value: event.target.value })} />}
        <button type="button" aria-label={`删除规则 ${ruleIndex + 1} 候选值 ${valueIndex + 1}`} disabled={values.length === 1} onClick={() => onRuleChange(rule.id, { conditionValues: values.filter(value => value.id !== item.id) })}>×</button>
      </div>)}
      <button type="button" onClick={() => onRuleChange(rule.id, { conditionValues: [...values, { id: `condition_value_${Date.now().toString(36)}`, mode: 'LITERAL', value: '' }] })}>＋ 添加候选值</button>
    </div>
  }
  return <aside className={`dataset-canvas-drawer transform ${transformColorClass(transform)}`} aria-label={`配置${componentLabel}`} onClick={event => event.stopPropagation()}>
    <header><div><span>{componentLabel}</span><strong>{transform.name}</strong><small>{componentMeta?.description || '把上游字段转换为可继续使用的新字段'}</small></div><button type="button" aria-label="保存并关闭字段处理配置" onClick={onDone}>×</button></header>
    <section><h3>组件与输入</h3><div className="dataset-group-input"><label><span>产物名称</span><input aria-label="字段处理产物名称" value={transform.name} onChange={event => onNameChange(event.target.value)} placeholder="例如：订单日期标准化" /></label><div aria-label="字段处理组件输入" className={`dataset-connected-input ${transform.input ? 'connected' : 'empty'}`}><span>输入组件</span><strong>{relationInputLabel(transform.input, nodes, boxes, groups, transforms)}</strong><small>{transform.input ? `${inputs.length} 个可用字段` : '请从上游组件拖线到输入端口'}</small></div></div></section>
    <section><div className="dataset-drawer-title"><div><h3>转换规则</h3><p>{transform.family === 'NULL' ? '默认用固定值填充 NULL；只有选择“其他字段”时才需要第二字段。' : transformComponentTypeFor(transform) === 'TEXT_CONCAT' ? '按字段一、连接符、字段二的顺序生成拼接结果。' : transformComponentTypeFor(transform) === 'TEXT_SUBSTRING' ? '按从 1 开始的字符位置与长度提取文本。' : transform.family === 'CONDITION' ? '选择比较方式，命中和未命中时分别输出对应值。' : transformComponentTypeFor(transform) === 'NUMBER_ARITHMETIC' ? '选择加减乘除，并指定参与运算的两个数值字段。' : transformComponentTypeFor(transform) === 'NUMBER_ROUNDING' ? '可选择四舍五入、向下取整或向上取整。' : '每条规则生成一个新字段，也可替换原字段。'}</p></div><span>{transform.rules.length} 条</span></div>{transform.input && !options.length && <p className="dataset-relation-pending">当前输入没有符合该处理类型的字段，请更换输入或先使用类型转换组件。</p>}
      <div className="dataset-transform-rules">{transform.rules.map((rule, index) => <article key={rule.id}>
        <header><strong>规则 {index + 1}</strong><button type="button" aria-label={`删除转换规则 ${index + 1}`} onClick={() => onRemoveRule(rule.id)}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header>
        <div className="dataset-transform-rule-grid">
          <label><span>处理逻辑</span><select aria-label={`规则 ${index + 1} 处理逻辑`} value={rule.operation} disabled={operations.length === 1} onChange={event => changeOperation(rule, event.target.value as GraphTransformOperation)}>{operations.map(operation => <option key={operation} value={operation}>{transformOperationLabel[operation]}</option>)}</select></label>
          <label><span>输入字段</span><select aria-label={`规则 ${index + 1} 输入字段 1`} value={rule.inputKeys[0] || ''} onChange={event => changePrimaryInput(rule, event.target.value)}><option value="">选择字段</option>{options.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select></label>
          {rule.operation === 'COALESCE' && <label><span>补值来源</span><select aria-label={`规则 ${index + 1} 补值来源`} value={rule.fallbackMode || 'LITERAL'} onChange={event => onRuleChange(rule.id, { fallbackMode: event.target.value as GraphTransformRule['fallbackMode'] })}><option value="LITERAL">固定值</option><option value="FIELD">其他字段</option></select></label>}
          {transformRuleInputCount(rule.operation, rule.fallbackMode) === 2 && <label><span>{rule.operation === 'COALESCE' ? '补值字段' : rule.operation === 'CONCAT' ? '合并字段' : '第二字段'}</span><select aria-label={`规则 ${index + 1} 输入字段 2`} value={rule.inputKeys[1] || ''} onChange={event => onRuleChange(rule.id, { inputKeys: [rule.inputKeys[0] || '', event.target.value] })}><option value="">选择字段</option>{options.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select></label>}
          {rule.operation === 'COALESCE' && rule.fallbackMode !== 'FIELD' && <label><span>填充值</span><input aria-label={`规则 ${index + 1} 空值填充值`} value={rule.fallbackValue ?? ''} onChange={event => onRuleChange(rule.id, { fallbackValue: event.target.value })} /><small>仅当输入字段为 NULL 时使用该值</small></label>}
          {rule.operation === 'DATE_FORMAT' && <label><span>输出格式</span><select aria-label={`规则 ${index + 1} 输出格式`} value={rule.unit || 'DAY'} onChange={event => { const unit = event.target.value as DateFormatUnit; onRuleChange(rule.id, { unit, output: dateFormatOutputForUnit(rule, inputs.find(field => field.key === rule.inputKeys[0]), unit) }) }}>{dateFormatOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select><small>输出示例：{dateFormatMeta[(rule.unit as DateFormatUnit) || 'DAY'].example}</small></label>}
          {rule.operation === 'CAST' && <label><span>目标类型</span><select aria-label={`规则 ${index + 1} 目标类型`} value={rule.targetType || 'STRING'} onChange={event => onRuleChange(rule.id, { targetType: event.target.value as GraphTransformRule['targetType'], output: { ...rule.output, canonicalType: event.target.value } })}>{['STRING', 'INTEGER', 'DECIMAL', 'BOOLEAN', 'DATE', 'DATETIME'].map(type => <option key={type}>{type}</option>)}</select></label>}
          {rule.operation === 'ROUND' && <label><span>保留小数位</span><input aria-label={`规则 ${index + 1} 保留小数位`} type="number" min="-10" max="10" step="1" value={rule.precision ?? 2} onChange={event => onRuleChange(rule.id, { precision: Number(event.target.value) })} /><small>0 表示整数，负数可按十位、百位取整</small></label>}
          {rule.operation === 'SUBSTRING' && <><label><span>起始位置</span><input aria-label={`规则 ${index + 1} 截取起始位置`} type="number" min="1" step="1" value={rule.start ?? 1} onChange={event => onRuleChange(rule.id, { start: Number(event.target.value) })} /></label><label><span>截取长度</span><input aria-label={`规则 ${index + 1} 截取长度`} type="number" min="0" step="1" value={rule.length ?? 10} onChange={event => onRuleChange(rule.id, { length: Number(event.target.value) })} /></label></>}
          {rule.operation === 'CONCAT' && <label><span>连接符</span><input aria-label={`规则 ${index + 1} 字段连接符`} value={rule.separator ?? ''} placeholder="可留空，例如 -、/ 或空格" onChange={event => onRuleChange(rule.id, { separator: event.target.value })} /><small>输出顺序：输入字段 + 连接符 + 合并字段；NULL 按空文本合并</small></label>}
          {rule.operation === 'REPLACE' && <><label><span>查找文本</span><input aria-label={`规则 ${index + 1} 查找文本`} value={rule.searchValue ?? ''} onChange={event => onRuleChange(rule.id, { searchValue: event.target.value })} /></label><label><span>替换为</span><input aria-label={`规则 ${index + 1} 替换文本`} value={rule.replacementValue ?? ''} onChange={event => onRuleChange(rule.id, { replacementValue: event.target.value })} /></label></>}
          {rule.operation === 'CASE' && <>
            <label><span>判断条件</span><select aria-label={`规则 ${index + 1} 判断条件`} value={rule.conditionOperator || 'EQUALS'} onChange={event => {
              const conditionOperator = event.target.value as GraphConditionOperator
              onRuleChange(rule.id, { conditionOperator, ...(conditionOperator === 'IN' && !rule.conditionValues?.length ? { conditionValues: [{ id: `condition_value_${Date.now().toString(36)}`, mode: 'LITERAL', value: '' }] } : {}) })
            }}>{conditionOperatorOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label>
            {rule.conditionOperator === 'IN'
              ? conditionCollectionEditor(rule, index)
              : rule.conditionOperator !== 'IS_NULL' && rule.conditionOperator !== 'IS_NOT_NULL' && <label><span>比较值</span><input aria-label={`规则 ${index + 1} 匹配值`} value={rule.matchValue || ''} onChange={event => onRuleChange(rule.id, { matchValue: event.target.value })} /></label>}
            <label><span>命中时输出</span><input aria-label={`规则 ${index + 1} 命中值`} value={rule.thenValue || ''} onChange={event => onRuleChange(rule.id, { thenValue: event.target.value })} /></label>
            <label><span>未命中输出</span><input aria-label={`规则 ${index + 1} 默认值`} value={rule.elseValue || ''} onChange={event => onRuleChange(rule.id, { elseValue: event.target.value })} /></label>
          </>}
          <label><span>输出名称</span><input aria-label={`规则 ${index + 1} 输出名称`} value={rule.output.name} onChange={event => onRuleChange(rule.id, { output: { ...rule.output, name: event.target.value } })} /></label>
          <label><span>输出编码</span><input aria-label={`规则 ${index + 1} 输出编码`} value={rule.output.code} onChange={event => onRuleChange(rule.id, { output: { ...rule.output, code: event.target.value } })} /></label>
        </div>
        <label className="dataset-transform-replace"><input type="checkbox" checked={Boolean(rule.replaceSourceKey)} onChange={event => onRuleChange(rule.id, { replaceSourceKey: event.target.checked ? rule.inputKeys[0] : undefined })} /><span>用转换结果替换第一个输入字段</span></label>
      </article>)}</div>
      <button className="dataset-add-condition" type="button" disabled={!transform.input || !inputs.length} onClick={onAddRule}>添加转换规则</button>
    </section>
    <footer>{error && <span className="dataset-drawer-error" role="alert">{error}</span>}<small>可继续连接分组、其他字段处理或结束节点</small><button type="button" onClick={onDone}>完成</button></footer>
  </aside>
}

function JoinConfigDrawer({ box, join, boxes, groups, transforms, nodes, leftOutputFields, rightOutputFields, onNameChange, onJoinPatch, onConditionPatch, onAddCondition, onRemoveCondition, onOutputChange, onDone }: {
  box: RelationBox; join?: JoinOption; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; nodes: DesignerNode[]
  leftOutputFields: ProducedField[]; rightOutputFields: ProducedField[]; onNameChange: (name: string) => void
  onJoinPatch: (patch: Partial<JoinOption>) => void
  onConditionPatch: (conditionID: string, patch: { leftField?: string; rightField?: string }) => void
  onAddCondition: () => void; onRemoveCondition: (conditionID: string) => void
  onOutputChange: (key: string, checked: boolean) => void; onDone: () => void
}) {
  // 当前 Join DSL 的关联键仍引用物理字段；转换产物可以作为关联输入和输出，
  // 但不能伪装成同名物理字段参与关联条件，否则保存后会悄悄改变语义。
  const physicalJoinField = (field: ProducedField) => field.key === `${field.binding.nodeId}.${field.binding.field}`
  const leftFields = leftOutputFields.filter(field => (!join || field.binding.nodeId === join.leftNodeId) && physicalJoinField(field))
  const rightFields = rightOutputFields.filter(field => (!join || field.binding.nodeId === join.rightNodeId) && physicalJoinField(field))
  const conditions = join ? joinConditions(join) : []
  const outputItems = [...new Map([...leftOutputFields, ...rightOutputFields].map(field => [field.key, field])).values()]
  const selectedOutputs = new Set(box.outputKeys.length ? box.outputKeys : outputItems.map(field => field.key))
  return <aside className="dataset-canvas-drawer relation" aria-label="配置表关联" onClick={event => event.stopPropagation()}>
    <header><div><span>关联组件</span><strong>{box.name}</strong><small>关联接收两个上游数据集；转换结果会保留在关联产物中</small></div><button type="button" aria-label="保存并关闭关系配置" onClick={onDone}>×</button></header>
    <section><h3>组件与输入槽位</h3><div className="dataset-group-input"><label><span>产物名称</span><input aria-label="关联产物名称" value={box.name} onChange={event => onNameChange(event.target.value)} placeholder="例如：客户订单关联结果" /></label></div><div className="dataset-relation-inputs readonly"><div aria-label="关联槽位 1" className={`dataset-connected-input ${box.left ? 'connected' : 'empty'}`}><span>槽位 1</span><strong>{relationInputLabel(box.left, nodes, boxes, groups, transforms)}</strong></div><div aria-label="关联槽位 2" className={`dataset-connected-input ${box.right ? 'connected' : 'empty'}`}><span>槽位 2</span><strong>{relationInputLabel(box.right, nodes, boxes, groups, transforms)}</strong></div></div>{!join && <p className="dataset-relation-pending">请在画布中把两个上游组件分别连接到槽位 1 和槽位 2。</p>}</section>
    {join && <><section><h3>连接方式</h3><div className="dataset-join-types">{['INNER', 'LEFT', 'RIGHT', 'FULL'].map(type => <button key={type} type="button" className={join.joinType === type ? 'selected' : ''} aria-pressed={join.joinType === type} onClick={() => onJoinPatch({ joinType: type })}>{type === 'INNER' ? 'INNER JOIN' : `${type} JOIN`}</button>)}</div></section>
      <section><div className="dataset-drawer-title"><div><h3>关联字段</h3><p>关联键使用两侧稳定的原始字段，避免把派生字段误当物理列；多个条件使用 AND。</p></div><span>{conditions.length} 个条件</span></div><div className="dataset-join-conditions">{conditions.map((condition, index) => <div key={condition.id}><span>{index + 1}</span><select aria-label={`条件 ${index + 1} 左字段`} value={condition.leftField} onChange={event => onConditionPatch(condition.id, { leftField: event.target.value })}><option value="">选择槽位 1 字段</option>{leftFields.map(field => <option key={field.key} value={field.binding.field}>{graphProducedFieldLabel(field)}</option>)}</select><em>=</em><select aria-label={`条件 ${index + 1} 右字段`} value={condition.rightField} onChange={event => onConditionPatch(condition.id, { rightField: event.target.value })}><option value="">选择槽位 2 字段</option>{rightFields.map(field => <option key={field.key} value={field.binding.field}>{graphProducedFieldLabel(field)}</option>)}</select><button type="button" disabled={conditions.length === 1} aria-label={`删除条件 ${index + 1}`} onClick={() => onRemoveCondition(condition.id)}>×</button></div>)}</div><button className="dataset-add-condition" type="button" onClick={onAddCondition}>＋ 添加关联字段</button></section>
      <section><div className="dataset-drawer-title"><div><h3>输出字段</h3><p>勾选字段组成“{box.name}”，并作为下游组件可识别的产物。</p></div><span>{selectedOutputs.size} 已选</span></div><div className="dataset-drawer-field-list">{outputItems.map(field => <label key={field.key}><input aria-label={`关联输出 ${field.code}`} type="checkbox" checked={selectedOutputs.has(field.key)} onChange={event => onOutputChange(field.key, event.target.checked)} /><span><strong>{field.name}</strong><small>{graphProducedFieldLabel(field)}</small></span></label>)}</div></section></>}
    <footer><small>点击画板空白处也会自动保存并收起</small><button type="button" onClick={onDone}>完成</button></footer>
  </aside>
}

function EndConfigDrawer({ end, boxes, groups, transforms, nodes, availableFields, onNameChange, onOutputChange, onDone }: {
  end: EndBox; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; nodes: DesignerNode[]; availableFields: ProducedField[]
  onNameChange: (name: string) => void
  onOutputChange: (field: ProducedField, checked: boolean) => void; onDone: () => void
}) {
  const selected = new Map(end.outputs.map(output => [output.key, output]))
  return <aside className="dataset-canvas-drawer end" aria-label="配置结束节点" onClick={event => event.stopPropagation()}>
    <header><div><span>结束节点</span><strong>{end.name}</strong><small>唯一的最终出口：定义数据集对外字段；数据预览请使用画布按钮</small></div><button type="button" aria-label="保存并关闭结束节点配置" onClick={onDone}>×</button></header>
    <section><h3>最终产物</h3><div className="dataset-group-input"><label><span>产物名称</span><input aria-label="结束节点产物名称" value={end.name} onChange={event => onNameChange(event.target.value)} placeholder="例如：客户订单分析数据集" /></label><div aria-label="结束节点输入" className={`dataset-connected-input ${end.input ? 'connected' : 'empty'}`}><span>最终输入</span><strong>{relationInputLabel(end.input, nodes, boxes, groups, transforms)}</strong><small>{end.input ? '最终输入由画布连线确定' : '请从最终上游组件拖线到结束节点'}</small></div></div></section>
    <section><div className="dataset-drawer-title"><div><h3>输出字段</h3><p>选择最终对外字段；勾选后按上游稳定编码自动生成字段别名。</p></div><span>{end.outputs.length} 已选</span></div><div className="dataset-drawer-field-list configured end-fields">{availableFields.map(field => { const output = selected.get(field.key); return <div className={output ? 'selected' : ''} key={field.key}><label><input aria-label={`最终输出 ${field.code}`} type="checkbox" checked={Boolean(output)} onChange={event => onOutputChange(field, event.target.checked)} /><span><strong>{field.name}</strong><small>{graphProducedFieldLabel(field)}</small></span></label>{output && <div className="dataset-product-fields generated"><output className="dataset-generated-field-alias" aria-label={`${field.code} 字段别名`}><small>字段别名</small><strong>{output.code}</strong></output></div>}</div> })}</div></section>
    <footer><small>保存数据集时会以此节点的字段作为最终 DSL 输出</small><button type="button" onClick={onDone}>完成</button></footer>
  </aside>
}

function CanvasPreviewDialog({ preview, label, onRefresh, onClose }: { preview?: NodePreviewState; label: string; onRefresh: () => void; onClose: () => void }) {
  const dialogRef = useRef<HTMLElement>(null)
  const previewRowCount = preview?.data?.rows.length ?? 0
  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      if (typeof dialogRef.current?.scrollIntoView === 'function') dialogRef.current.scrollIntoView({ behavior: 'smooth', block: 'end' })
    })
    return () => window.cancelAnimationFrame(frame)
  }, [label, preview?.error, preview?.loading, previewRowCount])
  return <section ref={dialogRef} className="dataset-canvas-preview-dialog" role="dialog" aria-modal="false" aria-label={`${label}数据预览`} onClick={event => event.stopPropagation()}>
    <header><div><span>组件数据预览</span><strong>{label}</strong><small>仅执行当前组件及其上游数据流，最多展示 5 行</small></div><div><button className="quiet-button" type="button" disabled={preview?.loading} onClick={onRefresh}>{preview?.loading ? '加载中' : '刷新'}</button><button type="button" aria-label="关闭组件数据预览" onClick={onClose}><XIcon aria-hidden="true" size={15} weight="bold" /></button></div></header>
    {preview?.loading
      ? <div className="dataset-node-preview-state">正在执行“{label}”并读取真实数据…</div>
      : preview?.data
        ? <PreviewRows preview={{ queryId: '', columns: preview.data.columns, rows: preview.data.rows, rowCount: preview.data.rows.length, durationMs: 0 }} />
        : preview?.error
          ? <div className="dataset-preview-diagnostic" role="alert"><div><strong>异常原因</strong><span>{preview.error}</span></div><div><strong>处理建议</strong><span>{preview.suggestion || '请完善当前组件配置并确认上游数据源可用。'}</span></div></div>
          : <div className="dataset-node-preview-state">点击刷新以加载当前组件的前 5 行数据。</div>}
  </section>
}

function PreviewRows({ preview }: { preview: DatasetPreview }) {
  if (!preview.rows.length) return <Empty>当前查询没有返回数据。</Empty>
  return <div className="dataset-preview-table-wrap"><table><thead><tr>{preview.columns.map((column, index) => <th key={`${column}-${index}`}>{column}</th>)}</tr></thead><tbody>{preview.rows.slice(0, 5).map((row, rowIndex) => <tr key={rowIndex}>{preview.columns.map((_, columnIndex) => <td key={columnIndex}>{row[columnIndex] == null ? '—' : String(row[columnIndex])}</td>)}</tr>)}</tbody></table></div>
}

function PublishedVersionTopologyPreview({ version }: { version: PublishedVersionRecord }) {
  const designer = version.dsl.designer
  const rawNodes = Array.isArray(version.dsl.nodes) ? version.dsl.nodes : []
  const nodes = rawNodes.map((raw, index) => {
    const id = typeof raw.id === 'string' ? raw.id : `node_${index + 1}`
    return { id, name: designer?.nodeNames[id] || (typeof raw.alias === 'string' ? raw.alias : `数据节点 ${index + 1}`), position: designer?.nodePositions[id] ?? { x: 42, y: 48 + index * 130 } }
  })
  const joins = designer?.joins ?? []
  const groups = designer?.groups ?? []
  const transforms = designer?.transforms ?? []
  const end = designer?.end
  const positions = [...nodes.map(node => node.position), ...joins.map(join => join.position), ...groups.map(group => group.position), ...transforms.map(transform => transform.position), ...(end ? [end.position] : [])]
  if (!positions.length) return <Empty>该版本没有可展示的画布组件。</Empty>
  const minX = Math.min(...positions.map(position => position.x)), minY = Math.min(...positions.map(position => position.y))
  const maxX = Math.max(...positions.map(position => position.x + 160)), maxY = Math.max(...positions.map(position => position.y + 66))
  const scale = Math.min(1, 720 / Math.max(1, maxX - minX), 250 / Math.max(1, maxY - minY))
  const normalize = (position: CanvasPoint) => ({ x: 18 + (position.x - minX) * scale, y: 18 + (position.y - minY) * scale })
  const positionByKey = new Map<string, CanvasPoint>([
    ...nodes.map(node => [`NODE:${node.id}`, normalize(node.position)] as const),
    ...joins.map(join => [`JOIN:${join.id}`, normalize(join.position)] as const),
    ...groups.map(group => [`GROUP:${group.id}`, normalize(group.position)] as const),
    ...transforms.map(transform => [`TRANSFORM:${transform.id}`, normalize(transform.position)] as const),
  ])
  const edgeFor = (source: RelationInput | undefined, target: CanvasPoint, slot = 0) => {
    if (!source) return null
    const start = positionByKey.get(`${source.kind}:${source.id}`)
    if (!start) return null
    return curveGeometry({ x: start.x + 144, y: start.y + 32 }, { x: target.x, y: target.y + 28 + slot * 12 }).path
  }
  const edges = [
    ...joins.flatMap(join => [edgeFor(join.left, normalize(join.position), -1), edgeFor(join.right, normalize(join.position), 1)]),
    ...groups.map(group => edgeFor(group.input, normalize(group.position))),
    ...transforms.map(transform => edgeFor(transform.input, normalize(transform.position))),
    ...(end ? [edgeFor(end.input, normalize(end.position))] : []),
  ].filter((path): path is string => Boolean(path))
  const extent = { width: Math.max(760, 36 + (maxX - minX) * scale), height: Math.max(180, 36 + (maxY - minY) * scale) }
  return <div className="dataset-revision-topology" style={extent} aria-label={`发布 V${version.versionNo} 画布排布`}>
    <svg style={extent} aria-hidden="true">{edges.map((path, index) => <path key={index} d={path} />)}</svg>
    {nodes.map(node => { const position = normalize(node.position); return <div key={node.id} className="node" style={{ left: position.x, top: position.y }}><small>数据节点</small><strong>{node.name}</strong></div> })}
    {groups.map(group => { const position = normalize(group.position); return <div key={group.id} className="group" style={{ left: position.x, top: position.y }}><small>分组组件</small><strong>{group.name}</strong><span>{group.dimensions.length} 维度 · {group.metrics.length} 指标</span></div> })}
    {transforms.map(transform => { const position = normalize(transform.position); return <div key={transform.id} className={`transform ${transformColorClass(transform)}`} style={{ left: position.x, top: position.y }}><small>{transformDisplayLabel(transform)}</small><strong>{transform.name}</strong><span>{transform.rules.length} 条规则</span></div> })}
    {joins.map(join => { const position = normalize(join.position); return <div key={join.id} className="join" style={{ left: position.x, top: position.y }}><small>关联组件</small><strong>{join.name}</strong></div> })}
    {end && (() => { const position = normalize(end.position); return <div className="end" style={{ left: position.x, top: position.y }}><small>结束节点</small><strong>{end.name}</strong><span>{end.outputs.length} 个输出</span></div> })()}
    {!designer && <div className="legacy-note">旧版本未保存组件坐标，仅展示可恢复的数据节点。</div>}
  </div>
}

function PublishedVersionHistoryPanel({ record, items, selected, preview, loading, busy, confirming, error, onSelect, onStartRollback, onCancelRollback, onRollback, onClose }: {
  record: DatasetRecord | null; items: PublishedVersionSummary[]; selected: PublishedVersionRecord | null
  preview: VersionPreviewState | null
  loading: boolean; busy: boolean; confirming: boolean; error: string
  onSelect: (versionID: string) => void; onStartRollback: () => void; onCancelRollback: () => void; onRollback: () => void; onClose: () => void
}) {
  const dateText = (value: string) => {
    const date = new Date(value)
    return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false })
  }
  const isCurrent = Boolean(record && selected && selected.dslHash === record.dslHash && selected.planHash === record.planHash)
  const isCurrentPublishedVersion = Boolean(record && selected && record.currentPublishedVersionId === selected.id)
  return <div className="dataset-version-history">
    <aside className="dataset-revision-list" aria-label="数据集发布版本列表">
      <header><strong>发布历史</strong><small>{items.length} 个已发布快照</small></header>
      {items.map(item => <button type="button" key={item.id} className={item.id === selected?.id ? 'selected' : ''} aria-pressed={item.id === selected?.id} disabled={busy} onClick={() => onSelect(item.id)}>
        <span><strong>V{item.versionNo}</strong><em>{statusLabels[item.status] ?? item.status}</em>{record?.currentPublishedVersionId === item.id && <b>当前发布</b>}</span>
        <small>{dateText(item.publishedAt)}</small>
      </button>)}
      {!items.length && !loading && <div className="dataset-revision-empty"><strong>暂无发布版本</strong><span>草稿审批通过并成功发布后，才会在这里生成不可变快照。</span></div>}
    </aside>
    <main className="dataset-revision-detail">
      {loading && !selected ? <Empty>正在加载发布版本…</Empty> : selected ? <>
        <header><div><span>发布快照</span><strong>V{selected.versionNo}</strong><em>{statusLabels[selected.status] ?? selected.status}</em>{isCurrentPublishedVersion && <b>当前发布</b>}</div><small>{dateText(selected.publishedAt)}</small></header>
        <p>{selected.dsl.dataset.description || '该发布版本暂无说明'}</p>
        <section className="dataset-revision-stats" aria-label="发布版本配置摘要">
          <span><small>数据节点</small><strong>{Array.isArray(selected.dsl.nodes) ? selected.dsl.nodes.length : 0}</strong></span>
          <span><small>输出字段</small><strong>{Array.isArray(selected.dsl.fields) ? selected.dsl.fields.length : 0}</strong></span>
          <span><small>数据集类型</small><strong>{typeLabels[selected.dsl.dataset.type] ?? selected.dsl.dataset.type}</strong></span>
        </section>
        <dl className="dataset-revision-metadata">
          <div><dt>数据集名称</dt><dd>{selected.dsl.dataset.name}</dd></div>
          <div><dt>发布状态</dt><dd>{statusLabels[selected.status] ?? selected.status}</dd></div>
          <div><dt>发布时间</dt><dd>{dateText(selected.publishedAt)}</dd></div>
          <div><dt>发布人</dt><dd>{selected.publishedBy || '系统'}</dd></div>
          <div><dt>源草稿记录</dt><dd>R{selected.draftRecordVersion}</dd></div>
          <div><dt>精确版本 ID</dt><dd>{selected.id}</dd></div>
          <div><dt>DSL 摘要</dt><dd title={selected.dslHash}>{selected.dslHash.slice(0, 16)}</dd></div>
          <div><dt>计划摘要</dt><dd title={selected.planHash}>{selected.planHash.slice(0, 16)}</dd></div>
        </dl>
        <section className="dataset-revision-evidence" aria-label="发布版本画布和数据预览">
          <div><h3>画布排布</h3><span>该发布版本冻结时的组件拓扑与位置</span></div>
          <div className="dataset-revision-topology-wrap"><PublishedVersionTopologyPreview version={selected} /></div>
          <div><h3>数据生成预览</h3><span>按不可变发布版本 DSL 执行 · 前 5 行</span></div>
          {preview?.versionID === selected.id && preview.loading ? <div className="dataset-node-preview-state">正在执行发布版本预览…</div> : preview?.versionID === selected.id && preview.data ? <PreviewRows preview={preview.data} /> : <div className="dataset-node-preview-state error"><span>{preview?.versionID === selected.id ? preview.error || '该发布版本暂无预览数据' : '正在加载发布版本预览…'}</span></div>}
          <small>预览严格使用该不可变发布 DSL；底层数据资产和当前权限策略按现状读取。</small>
        </section>
        {confirming && <section className="dataset-rollback-confirm" aria-label="确认回滚发布版本"><strong>确认回滚到发布 V{selected.versionNo}？</strong><p>系统会精确查找该发布版本对应的源草稿修订，将其复制为新的当前草稿；已有发布版本和当前发布指针不会被改写。</p><div><button className="quiet-button" type="button" disabled={busy} onClick={onCancelRollback}>取消</button><button className="dataset-rollback-button" type="button" disabled={busy} onClick={onRollback}>{busy ? '正在回滚…' : '确认回滚'}</button></div></section>}
        {error && <div className="dataset-center-feedback error" role="alert">{error}</div>}
        <footer><span>{isCurrent ? '当前草稿已与该发布版本一致' : `回滚后将生成新的当前配置 V${(record?.version ?? selected.datasetRecordVersion) + 1}`}</span><div><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>关闭</button>{!confirming && <button className="primary-button" type="button" disabled={busy || isCurrent} onClick={onStartRollback}>回滚到此版本</button>}</div></footer>
      </> : <>{error && <div className="dataset-center-feedback error" role="alert">{error}</div>}<Empty>请选择一个发布版本查看详情。</Empty></>}
    </main>
  </div>
}

function Dialog({ title, eyebrow, wide = false, closeDisabled = false, children, onClose }: { title: string; eyebrow: string; wide?: boolean; closeDisabled?: boolean; children: ReactNode; onClose: () => void }) {
  return <div className="dataset-dialog-backdrop" role="presentation" onMouseDown={event => { if (!closeDisabled && event.target === event.currentTarget) onClose() }}><section className={`dataset-dialog ${wide ? 'wide' : ''}`} role="dialog" aria-modal="true" aria-labelledby="dataset-dialog-title"><header><div><span className="eyebrow">{eyebrow}</span><h2 id="dataset-dialog-title">{title}</h2></div><button type="button" disabled={closeDisabled} aria-label={`关闭${title}`} onClick={onClose}>×</button></header>{children}</section></div>
}

function Empty({ title, children }: { title?: string; children: ReactNode }) {
  return <div className="dataset-center-empty">{title && <strong>{title}</strong>}<span>{children}</span></div>
}
