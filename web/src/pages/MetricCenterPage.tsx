import { useEffect, useMemo, useRef, useState } from 'react'
import {
  CalendarBlankIcon,
  CalculatorIcon,
  CheckSquareIcon,
  DatabaseIcon,
  EraserIcon,
  FunctionIcon,
  GitBranchIcon,
  SquareIcon,
  StackIcon,
} from '@phosphor-icons/react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import type {
  DatasetAIFieldHint,
  DatasetAIHintAggregation,
  DatasetAIHintTimeGrain,
  DatasetAIPlanHints,
} from '../lib/dataset-ai'
import {
  metricAIAPI,
  type MetricAuthoringProposal,
} from '../lib/metric-ai'
import {
  buildPreviewParameters,
  datasetAPI,
  type DatasetPreview,
  type DatasetSummary,
  type ParameterOption,
  type PublishedVersionRecord,
  type PublishedVersionSummary,
} from '../lib/datasets'
import {
  createMetricPublishIdempotencyKey,
  metricAPI,
  type MetricDefinition,
  type MetricDimension,
  type MetricPermissionAction,
  type MetricRecord,
  type MetricUsage,
  type MetricVersionRecord,
  type MetricVersionSummary,
  type PreviewMetricInput,
  type PublishMetricInput,
} from '../lib/metrics'
import { PreviewTable } from './DatasetDesignerPage'

const pageSize = 50
const emptyUsage = (): MetricUsage => ({
  reportDraftReferences: 0,
  downstreamDraftReferences: 0,
  downstreamPublishedReferences: 0,
  activeQueryRuns: 0,
})

const emptyDefinition = (): MetricDefinition => ({
  schemaVersion: '1.0',
  metric: { code: '', name: '', description: '', type: 'ATOMIC' },
  datasetId: '',
  datasetVersionId: '',
  expression: { type: 'FIELD_REF', fieldId: '' },
  aggregation: 'SUM',
  unit: '',
  numberFormat: '#,##0.00',
  timeFieldId: '',
  timeGrain: 'NONE',
  additivity: 'ADDITIVE',
  nonAdditiveDimensionFieldIds: [],
  allowedDimensions: [],
  decimalScale: 2,
  roundingMode: 'HALF_UP',
  nullHandling: 'IGNORE',
  divisionByZero: 'NULL',
})

type DatasetField = {
  id: string
  code: string
  name: string
  role: string
  canonicalType: string
  visible: boolean
  tableId: string
  column: string
}

type MetricCapabilities = Record<Lowercase<MetricPermissionAction>, boolean>

type PendingMetricPublication = {
  metricId: string
  fingerprint: string
  idempotencyKey: string
  input: PublishMetricInput
}

const valueAsRecord = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
const valueAsList = (value: unknown): unknown[] => Array.isArray(value) ? value : []
const valueAsText = (value: unknown): string => typeof value === 'string' ? value : ''
const definitionFingerprint = (definition: MetricDefinition) => JSON.stringify(definition)
const ambiguousMutationFailure = (cause: unknown) => !(cause instanceof RequestError) || cause.status >= 500
const reconciliationStatus = new Set([401, 403, 404, 409])
const datasetPermissionDenied = (cause: unknown) => cause instanceof RequestError && cause.status === 403

/** 只有明确的数据集权限拒绝可以降级为空目录或缺失快照；网络或服务故障必须保留为可见错误。 */
async function withDatasetPermissionFallback<T>(request: Promise<T>, fallback: T): Promise<T> {
  try {
    return await request
  } catch (cause) {
    if (datasetPermissionDenied(cause)) return fallback
    throw cause
  }
}

/** 后端规范 JSON 可能省略空时间字段；编辑态补回稳定空值，避免控件在受控/非受控间切换。 */
function normalizeDefinition(definition: MetricDefinition): MetricDefinition {
  return {
    ...definition,
    timeFieldId: definition.timeFieldId ?? '',
    allowedDimensions: definition.allowedDimensions ?? [],
    nonAdditiveDimensionFieldIds: definition.nonAdditiveDimensionFieldIds ?? [],
  }
}

/** 从不可变数据集 DSL 快照读取展示字段；服务端仍负责最终角色和兼容性校验。 */
function datasetFields(version: PublishedVersionRecord | null): DatasetField[] {
  const nodeTableIDs = new Map(valueAsList(version?.dsl.nodes).map(valueAsRecord).flatMap(node => {
    const nodeID = valueAsText(node.id)
    const tableID = valueAsText(node.tableId)
    return nodeID && tableID ? [[nodeID, tableID] as const] : []
  }))
  return valueAsList(version?.dsl.fields).map(valueAsRecord).map(field => ({
    field,
    expression: valueAsRecord(field.expression),
  })).map(({ field, expression }) => ({
    id: valueAsText(field.id),
    code: valueAsText(field.code),
    name: valueAsText(field.name) || valueAsText(field.code),
    role: valueAsText(field.role).toUpperCase(),
    canonicalType: valueAsText(field.canonicalType).toUpperCase(),
    visible: field.visible !== false,
    tableId: valueAsText(expression.type).toUpperCase() === 'FIELD_REF'
      ? nodeTableIDs.get(valueAsText(expression.nodeId)) ?? ''
      : '',
    column: valueAsText(expression.type).toUpperCase() === 'FIELD_REF' ? valueAsText(expression.field) : '',
  })).filter(field => field.id && field.visible)
}

function datasetPhysicalTableIDs(version: PublishedVersionRecord | null): string[] {
  return valueAsList(version?.dsl.nodes).map(valueAsRecord).map(node => valueAsText(node.tableId)).filter(Boolean)
}

function metricAtomicFieldEligible(field: DatasetField, aggregation: MetricDefinition['aggregation']): boolean {
  const numeric = field.canonicalType === 'INTEGER' || field.canonicalType === 'DECIMAL'
  if (aggregation === 'COUNT') return !['DATE', 'DATETIME', 'TIMESTAMP'].includes(field.canonicalType)
  if (aggregation === 'COUNT_DISTINCT') {
    return !['DATE', 'DATETIME', 'TIMESTAMP'].includes(field.canonicalType) &&
      (['IDENTIFIER', 'DIMENSION', 'ATTRIBUTE'].includes(field.role) || numeric)
  }
  return numeric
}

/** 试算参数只从当前精确数据集版本读取，不能复用其他版本或可变草稿。 */
function datasetParameters(version: PublishedVersionRecord | null): ParameterOption[] {
  return valueAsList(version?.dsl.parameters).map(valueAsRecord).map(parameter => ({
    code: valueAsText(parameter.code),
    name: valueAsText(parameter.name),
    dataType: valueAsText(parameter.dataType),
    required: Boolean(parameter.required),
    multiValue: Boolean(parameter.multiValue),
  })).filter(parameter => parameter.code)
}

function versionSummary(version: PublishedVersionRecord): PublishedVersionSummary {
  return {
    id: version.id,
    datasetId: version.datasetId,
    versionNo: version.versionNo,
    status: version.status,
    dslVersion: version.dslVersion,
    dslHash: version.dslHash,
    planHash: version.planHash,
    draftRecordVersion: version.draftRecordVersion,
    publishedAt: version.publishedAt,
    publishedBy: version.publishedBy,
  }
}

/** 资产管理中心的指标编辑页只提供受控定义编辑，所有业务计算统一交由服务端验证与试算。 */
export function MetricCenterPage() {
  const { metricId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const routeMetricId = metricId ?? ''
  const isNew = !routeMetricId
  const routeState = location.state as {
    metricAIRequirement?: unknown
    preferredDatasetId?: unknown
    safeDatasetExtension?: unknown
  } | null
  const initialAIRequirement = typeof routeState?.metricAIRequirement === 'string' ? routeState.metricAIRequirement : ''
  const preferredDatasetId = typeof routeState?.preferredDatasetId === 'string' ? routeState.preferredDatasetId : ''
  const safeDatasetExtension = routeState?.safeDatasetExtension === true && Boolean(preferredDatasetId)
  const [definition, setDefinition] = useState<MetricDefinition>(emptyDefinition)
  const [record, setRecord] = useState<MetricRecord | null>(null)
  const [savedFingerprint, setSavedFingerprint] = useState('')
  const [datasets, setDatasets] = useState<DatasetSummary[]>([])
  const [datasetVersions, setDatasetVersions] = useState<PublishedVersionSummary[]>([])
  const [selectedDatasetVersion, setSelectedDatasetVersion] = useState<PublishedVersionRecord | null>(null)
  const [capabilities, setCapabilities] = useState<MetricCapabilities>({ read: false, manage: false, publish: false })
  const [permissionsReady, setPermissionsReady] = useState(false)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [pageLoading, setPageLoading] = useState(true)
  const [previewValues, setPreviewValues] = useState<Record<string, string>>({})
  const [preview, setPreview] = useState<DatasetPreview | null>(null)
  const [versions, setVersions] = useState<MetricVersionSummary[]>([])
  const [versionsTotal, setVersionsTotal] = useState(0)
  const [selectedMetricVersion, setSelectedMetricVersion] = useState<MetricVersionRecord | null>(null)
  const [selectedVersionDataset, setSelectedVersionDataset] = useState<PublishedVersionRecord | null>(null)
  const [versionDatasetUnavailable, setVersionDatasetUnavailable] = useState(false)
  const [selectedVersionUsage, setSelectedVersionUsage] = useState<MetricUsage>(emptyUsage)
  const [versionPreviewValues, setVersionPreviewValues] = useState<Record<string, string>>({})
  const [versionPreview, setVersionPreview] = useState<DatasetPreview | null>(null)
  const [versionLoading, setVersionLoading] = useState(false)
  const [publishOutcomeUnknown, setPublishOutcomeUnknown] = useState(false)
  const [reconciliationRequired, setReconciliationRequired] = useState(false)
  const [versionRefreshKey, setVersionRefreshKey] = useState(0)
  const [reloadKey, setReloadKey] = useState(0)
  const routeIdentity = useRef(routeMetricId)
  const permissionIdentity = useRef('')
  const datasetRequest = useRef(0)
  const versionRequest = useRef(0)
  const selectedMetricVersionId = useRef('')
  const publication = useRef<PendingMetricPublication | null>(null)
  const pendingRouteMessage = useRef('')

  const fingerprint = useMemo(() => definitionFingerprint(definition), [definition])
  const fields = useMemo(() => datasetFields(selectedDatasetVersion), [selectedDatasetVersion])
  const atomicFields = useMemo(() => fields.filter(field => metricAtomicFieldEligible(field, definition.aggregation)), [definition.aggregation, fields])
  const dimensionFields = useMemo(() => fields.filter(field => ['DIMENSION', 'TIME', 'ATTRIBUTE', 'IDENTIFIER'].includes(field.role)), [fields])
  const timeFields = useMemo(() => fields.filter(field => field.role === 'TIME' && (field.canonicalType === 'DATE' || field.canonicalType === 'DATETIME')), [fields])
  const parameters = useMemo(() => datasetParameters(selectedDatasetVersion), [selectedDatasetVersion])
  const versionParameters = useMemo(() => datasetParameters(selectedVersionDataset), [selectedVersionDataset])
  const dirty = Boolean(record) && fingerprint !== savedFingerprint
  const writesLocked = publishOutcomeUnknown || reconciliationRequired
  const advancedExpression = definition.expression.type !== 'FIELD_REF'
  // 已有指标缺少精确数据集快照时必须整体只读，避免用空字段集改写口径。
  const datasetSnapshotUnavailable = Boolean(record && !selectedDatasetVersion)
  const editorDisabled = busy || pageLoading || writesLocked || datasetSnapshotUnavailable || advancedExpression || !permissionsReady || !capabilities.manage
  const atomicFieldId = definition.expression.type === 'FIELD_REF' ? definition.expression.fieldId : ''

  useEffect(() => {
    let active = true
    let datasetCatalogFailure: unknown
    routeIdentity.current = routeMetricId
    permissionIdentity.current = ''
    datasetRequest.current += 1
    versionRequest.current += 1
    selectedMetricVersionId.current = ''
    publication.current = null
    queueMicrotask(() => {
      if (!active) return
      setDefinition(emptyDefinition())
      setRecord(null)
      setSavedFingerprint('')
      setDatasets([])
      setDatasetVersions([])
      setSelectedDatasetVersion(null)
      setCapabilities({ read: false, manage: false, publish: false })
      setPermissionsReady(false)
      setPreviewValues({})
      setPreview(null)
      setVersions([])
      setVersionsTotal(0)
      setSelectedMetricVersion(null)
      setSelectedVersionDataset(null)
      setVersionDatasetUnavailable(false)
      setSelectedVersionUsage(emptyUsage())
      setVersionPreviewValues({})
      setVersionPreview(null)
      setVersionLoading(false)
      setPublishOutcomeUnknown(false)
      setReconciliationRequired(false)
      setMessage('')
      setError('')
      setBusy(false)
      setPageLoading(true)
    })
    const permissionObjectId = routeMetricId
    Promise.all([
      // 新建页只把授权的发布数据集作为可选提示；AI 仍可在未选择时自行检索补全。
      datasetAPI.list(200, 0).catch(cause => {
        if (!datasetPermissionDenied(cause)) datasetCatalogFailure = cause
        return { items: [], total: 0, limit: 200, offset: 0 }
      }),
      metricAPI.evaluatePermission(permissionObjectId, 'READ'),
      metricAPI.evaluatePermission(permissionObjectId, 'MANAGE'),
      metricAPI.evaluatePermission(permissionObjectId, 'PUBLISH'),
      routeMetricId ? metricAPI.get(routeMetricId) : Promise.resolve(null),
    ]).then(async ([datasetPage, read, manage, publish, loaded]) => {
      if (!active || routeIdentity.current !== routeMetricId) return
      permissionIdentity.current = routeMetricId
      setDatasets(datasetPage.items)
      setCapabilities({ read: read.allowed, manage: manage.allowed, publish: publish.allowed })
      setPermissionsReady(true)
      if (datasetCatalogFailure !== undefined) {
        setError(datasetCatalogFailure instanceof Error
          ? `加载数据集目录失败：${datasetCatalogFailure.message}`
          : '加载数据集目录失败')
      }
      if (!loaded) return
      const normalized = normalizeDefinition(loaded.definition)
      setRecord(loaded)
      setDefinition(normalized)
      setSavedFingerprint(definitionFingerprint(normalized))
      const request = ++datasetRequest.current
      const [versionPage, exactVersion] = await Promise.all([
        withDatasetPermissionFallback(datasetAPI.listVersions(loaded.definition.datasetId), { items: [], total: 0, limit: 50, offset: 0 }),
        withDatasetPermissionFallback(datasetAPI.getVersion(loaded.definition.datasetId, loaded.definition.datasetVersionId), null),
      ])
      if (!active || routeIdentity.current !== routeMetricId || request !== datasetRequest.current) return
      setDatasetVersions(exactVersion && !versionPage.items.some(item => item.id === exactVersion.id)
        ? [versionSummary(exactVersion), ...versionPage.items]
        : versionPage.items)
      setSelectedDatasetVersion(exactVersion)
      if (pendingRouteMessage.current) {
        setMessage(pendingRouteMessage.current)
        pendingRouteMessage.current = ''
      }
    }).catch(cause => {
      if (active && routeIdentity.current === routeMetricId) {
        setPermissionsReady(true)
        setError(cause instanceof Error ? cause.message : '加载指标编辑器失败')
      }
    }).finally(() => {
      if (active && routeIdentity.current === routeMetricId) setPageLoading(false)
    })
    return () => { active = false }
  }, [reloadKey, routeMetricId])

  useEffect(() => {
    if (!routeMetricId || permissionIdentity.current !== routeMetricId || !permissionsReady || !capabilities.read) return
    let active = true
    const request = ++versionRequest.current
    queueMicrotask(() => { if (active) setVersionLoading(true) })
    metricAPI.listVersions(routeMetricId, pageSize, 0).then(async page => {
      if (!active || routeIdentity.current !== routeMetricId || request !== versionRequest.current) return
      setVersions(page.items)
      setVersionsTotal(page.total)
      const preferredId = page.items.some(item => item.id === selectedMetricVersionId.current)
        ? selectedMetricVersionId.current
        : page.items.find(item => item.id === record?.currentPublishedVersionId)?.id ?? page.items[0]?.id ?? ''
      if (!preferredId) {
        setSelectedMetricVersion(null)
        setSelectedVersionDataset(null)
        setVersionDatasetUnavailable(false)
        setSelectedVersionUsage(emptyUsage())
        return
      }
      selectedMetricVersionId.current = preferredId
      const [metricVersion, usage] = await Promise.all([
        metricAPI.getVersion(routeMetricId, preferredId),
        metricAPI.getVersionUsage(routeMetricId, preferredId),
      ])
      if (!active || routeIdentity.current !== routeMetricId || request !== versionRequest.current) return
      setSelectedMetricVersion(metricVersion)
      setSelectedVersionDataset(null)
      setVersionDatasetUnavailable(false)
      setSelectedVersionUsage(usage)
      setVersionPreviewValues({})
      setVersionPreview(null)
      // 指标版本和占用有独立读取权限；数据集快照不可读时仍保留前两者，只禁用试算。
      try {
        const dataVersion = await datasetAPI.getVersion(metricVersion.definition.datasetId, metricVersion.definition.datasetVersionId)
        if (!active || routeIdentity.current !== routeMetricId || request !== versionRequest.current) return
        setSelectedVersionDataset(dataVersion)
      } catch (cause) {
        if (datasetPermissionDenied(cause) && active && routeIdentity.current === routeMetricId && request === versionRequest.current) {
          setVersionDatasetUnavailable(true)
        } else {
          throw cause
        }
      }
    }).catch(cause => {
      if (active && routeIdentity.current === routeMetricId && request === versionRequest.current) {
        setError(cause instanceof Error ? `加载指标版本失败：${cause.message}` : '加载指标版本失败')
      }
    }).finally(() => {
      if (active && routeIdentity.current === routeMetricId && request === versionRequest.current) setVersionLoading(false)
    })
    return () => { active = false }
  }, [capabilities.read, permissionsReady, record?.currentPublishedVersionId, routeMetricId, versionRefreshKey])

  function updateDefinition(patch: Partial<MetricDefinition>) {
    setDefinition(current => ({ ...current, ...patch }))
  }

  function updateMetric(patch: Partial<MetricDefinition['metric']>) {
    setDefinition(current => ({ ...current, metric: { ...current.metric, ...patch } }))
  }

  async function selectDataset(datasetId: string) {
    const request = ++datasetRequest.current
    setDatasetVersions([])
    setSelectedDatasetVersion(null)
    setPreview(null)
    updateDefinition({
      datasetId,
      datasetVersionId: '',
      expression: { type: 'FIELD_REF', fieldId: '' },
      timeFieldId: '',
      timeGrain: 'NONE',
      allowedDimensions: [],
      nonAdditiveDimensionFieldIds: [],
    })
    if (!datasetId) return
    try {
      const page = await datasetAPI.listVersions(datasetId)
      if (request !== datasetRequest.current || routeIdentity.current !== routeMetricId) return
      setDatasetVersions(page.items)
    } catch (cause) {
      if (request === datasetRequest.current) setError(cause instanceof Error ? cause.message : '加载数据集版本失败')
    }
  }

  async function selectDatasetVersion(versionId: string) {
    const datasetId = definition.datasetId
    const request = ++datasetRequest.current
    setSelectedDatasetVersion(null)
    setPreview(null)
    updateDefinition({
      datasetVersionId: versionId,
      expression: { type: 'FIELD_REF', fieldId: '' },
      timeFieldId: '',
      timeGrain: 'NONE',
      allowedDimensions: [],
      nonAdditiveDimensionFieldIds: [],
    })
    if (!datasetId || !versionId) return
    try {
      const exact = await datasetAPI.getVersion(datasetId, versionId)
      if (request !== datasetRequest.current || routeIdentity.current !== routeMetricId) return
      setSelectedDatasetVersion(exact)
    } catch (cause) {
      if (request === datasetRequest.current) setError(cause instanceof Error ? cause.message : '加载精确数据集版本失败')
    }
  }

  async function createAIMetricDraft(candidate: MetricDefinition) {
    if (!isNew || record || !capabilities.manage || candidate.metric.type !== 'ATOMIC') {
      throw new Error('当前页面不能确认该 AI 指标配置')
    }
    const request = ++datasetRequest.current
    setError('')
    const exact = await datasetAPI.getVersion(candidate.datasetId, candidate.datasetVersionId)
    if (request !== datasetRequest.current || routeIdentity.current !== '') throw new Error('页面已经切换，本次 AI 配置未创建')
    if (exact.status !== 'PUBLISHED' || exact.datasetId !== candidate.datasetId || exact.id !== candidate.datasetVersionId) {
      throw new Error('AI 配置引用的数据集版本已不可用，请重新生成')
    }
    const saved = await metricAPI.create(normalizeDefinition(candidate))
    if (request !== datasetRequest.current || routeIdentity.current !== '') return
    pendingRouteMessage.current = 'AI 生成的指标草稿已创建。'
    navigate(`/metrics/${saved.id}/edit`, { replace: true })
  }

  function openDatasetAI(
    strategy: 'CREATE_DATASET' | 'MODIFY_DATASET',
    datasetId: string,
    instruction: string,
    requirement: string,
    hints?: DatasetAIPlanHints,
  ) {
    const pathname = strategy === 'CREATE_DATASET'
      ? '/datasets/new/edit'
      : `/datasets/${encodeURIComponent(datasetId)}/edit`
    navigate(pathname, {
      state: {
        metricAIInstruction: instruction,
        metricAIRequirement: requirement,
        ...(hints ? { metricAIHints: hints } : {}),
        returnTo: '/metrics/new',
        ...(preferredDatasetId ? { preferredDatasetId } : {}),
        ...(safeDatasetExtension ? { safeDatasetExtension: true } : {}),
      },
    })
  }

  function toggleDimension(field: DatasetField) {
    const exists = definition.allowedDimensions.some(item => item.fieldId === field.id)
    if (exists) {
      updateDefinition({
        allowedDimensions: definition.allowedDimensions.filter(item => item.fieldId !== field.id),
        nonAdditiveDimensionFieldIds: definition.nonAdditiveDimensionFieldIds.filter(id => id !== field.id),
        ...(definition.timeFieldId === field.id ? { timeFieldId: '', timeGrain: 'NONE' as const } : {}),
      })
      return
    }
    const dimension: MetricDimension = {
      fieldId: field.id,
      name: field.name,
      hierarchyFieldIds: [field.id],
      sortDirection: 'ASC',
      nullLabel: '未分类',
    }
    updateDefinition({ allowedDimensions: [...definition.allowedDimensions, dimension] })
  }

  function updateDimension(fieldId: string, patch: Partial<MetricDimension>) {
    updateDefinition({
      allowedDimensions: definition.allowedDimensions.map(item => item.fieldId === fieldId ? { ...item, ...patch } : item),
    })
  }

  function updateAggregation(aggregation: MetricDefinition['aggregation']) {
    const currentFieldId = definition.expression.type === 'FIELD_REF' ? definition.expression.fieldId : ''
    const currentField = fields.find(field => field.id === currentFieldId)
    const fieldRemainsEligible = currentField && metricAtomicFieldEligible(currentField, aggregation)
    const count = aggregation === 'COUNT' || aggregation === 'COUNT_DISTINCT'
    updateDefinition({
      aggregation,
      ...(definition.expression.type === 'FIELD_REF' && !fieldRemainsEligible ? { expression: { type: 'FIELD_REF' as const, fieldId: '' } } : {}),
      ...(count ? { decimalScale: 0, numberFormat: '#,##0', unit: '' } : {}),
      ...(aggregation === 'COUNT_DISTINCT' || aggregation === 'AVG' ? { additivity: 'NON_ADDITIVE' as const } : {}),
      ...(aggregation === 'COUNT' || aggregation === 'SUM' ? { additivity: 'ADDITIVE' as const } : {}),
    })
  }

  function validateLocalDefinition() {
    if (!definition.metric.code.trim()) throw new Error('请填写指标编码')
    if (!definition.metric.name.trim()) throw new Error('请填写指标名称')
    if (!definition.datasetId || !definition.datasetVersionId) throw new Error('请选择精确的已发布数据集版本')
    if (definition.expression.type === 'FIELD_REF' && !definition.expression.fieldId) throw new Error('请选择原子指标字段')
    if (definition.decimalScale < 0 || definition.decimalScale > 12) throw new Error('小数位数必须在 0 到 12 之间')
  }

  async function saveDraft() {
    if (busy || writesLocked || datasetSnapshotUnavailable || !capabilities.manage) return
    const requestedRouteId = routeIdentity.current
    setBusy(true); setError(''); setMessage('')
    try {
      validateLocalDefinition()
      const saved = record
        ? await metricAPI.update(record.id, {
            expectedVersion: record.version,
            expectedDraftRecordVersion: record.draftRecordVersion,
            expectedDefinitionHash: record.definitionHash,
            definition,
          })
        : await metricAPI.create(definition)
      if (routeIdentity.current !== requestedRouteId) return
      const normalized = normalizeDefinition(saved.definition)
      setRecord(saved)
      setDefinition(normalized)
      setSavedFingerprint(definitionFingerprint(normalized))
      if (!record) {
        pendingRouteMessage.current = '指标草稿已创建。'
        navigate(`/metrics/${saved.id}/edit`, { replace: true })
      } else {
        setMessage(`指标草稿已保存 · 版本 ${saved.version}`)
      }
    } catch (cause) {
      if (routeIdentity.current === requestedRouteId) setError(cause instanceof Error ? cause.message : '保存指标草稿失败')
    } finally {
      if (routeIdentity.current === requestedRouteId) setBusy(false)
    }
  }

  function buildPreviewInput(
    snapshot: PublishedVersionRecord | null,
    values: Record<string, string>,
    dimensions: string[],
  ): PreviewMetricInput {
    return {
      queryId: globalThis.crypto.randomUUID(),
      parameters: buildPreviewParameters(datasetParameters(snapshot), values),
      dimensionFieldIds: dimensions,
      // 传 0 由服务端按数据集 previewLimit 与平台默认值取更小者，避免前端固定值放大版本限额。
      maxRows: 0,
    }
  }

  async function previewDraft() {
    if (!record || !selectedDatasetVersion || busy || writesLocked || !capabilities.read) return
    if (dirty) {
      setError('当前指标有未保存修改，请先保存草稿后再试算')
      return
    }
    const requestedMetricId = record.id
    setBusy(true); setError(''); setMessage(''); setPreview(null)
    try {
      const result = await metricAPI.preview(record.id, buildPreviewInput(
        selectedDatasetVersion,
        previewValues,
        definition.allowedDimensions.map(item => item.fieldId),
      ))
      if (routeIdentity.current !== requestedMetricId) return
      setPreview(result)
      setMessage(`指标试算完成 · ${result.rowCount} 行`)
    } catch (cause) {
      if (routeIdentity.current === requestedMetricId) setError(cause instanceof Error ? cause.message : '指标试算失败')
    } finally {
      if (routeIdentity.current === requestedMetricId) setBusy(false)
    }
  }

  async function publishDraft() {
    if (!record || !selectedDatasetVersion || busy || !capabilities.publish || writesLocked) return
    if (dirty) {
      setError('当前指标有未保存修改，请先保存草稿后再发布')
      return
    }
    try {
      const input: PublishMetricInput = {
        draftVersionId: record.draftVersionId,
        expectedVersion: record.version,
        expectedDraftRecordVersion: record.draftRecordVersion,
        expectedDefinitionHash: record.definitionHash,
        validationParameters: buildPreviewParameters(parameters, previewValues),
      }
      const candidate: PendingMetricPublication = {
        metricId: record.id,
        fingerprint,
        idempotencyKey: createMetricPublishIdempotencyKey(),
        input,
      }
      publication.current = candidate
      await submitPublication(candidate)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '无法生成发布请求')
    }
  }

  async function submitPublication(candidate: PendingMetricPublication) {
    const reconcilingUnknownOutcome = publishOutcomeUnknown
    if (routeIdentity.current !== candidate.metricId || definitionFingerprint(definition) !== candidate.fingerprint) {
      publication.current = null
      setPublishOutcomeUnknown(false)
      setReconciliationRequired(true)
      setError('发布候选与当前指标页面不一致，请重新加载指标后再操作')
      return
    }
    setBusy(true); setError(''); setMessage('')
    try {
      const published = await metricAPI.publish(candidate.metricId, candidate.input, candidate.idempotencyKey)
      if (routeIdentity.current !== candidate.metricId) return
      publication.current = null
      setPublishOutcomeUnknown(false)
      setMessage(`指标已发布 · V${published.versionNo} · 精确版本 ${published.id}`)
      await reconcileAggregate(candidate, published)
    } catch (cause) {
      if (routeIdentity.current !== candidate.metricId) return
      if (ambiguousMutationFailure(cause)) {
        publication.current = candidate
        setPublishOutcomeUnknown(true)
        setError(`发布结果尚未确认，编辑已锁定，请使用原请求重试。${cause instanceof Error ? ` ${cause.message}` : ''}`)
      } else {
        publication.current = null
        setPublishOutcomeUnknown(false)
        if (reconcilingUnknownOutcome && cause instanceof RequestError && reconciliationStatus.has(cause.status)) {
          setReconciliationRequired(true)
          setError(`原发布请求已返回 ${cause.status}，无法从客户端确认远端聚合状态，请重新加载指标`)
        } else {
          setError(cause instanceof Error ? cause.message : '指标发布失败')
        }
      }
    } finally {
      if (routeIdentity.current === candidate.metricId) setBusy(false)
    }
  }

  async function reconcileAggregate(candidate: PendingMetricPublication, published: MetricVersionRecord) {
    try {
      const latest = await metricAPI.get(candidate.metricId)
      if (routeIdentity.current !== candidate.metricId) return
      if (published.metricRecordVersion != null && latest.version < published.metricRecordVersion) {
        setReconciliationRequired(true)
        setError('指标已发布，但聚合版本低于发布响应下界，请重新加载指标')
        return
      }
      const sameDraft = latest.draftVersionId === candidate.input.draftVersionId &&
        latest.draftRecordVersion === candidate.input.expectedDraftRecordVersion &&
        latest.definitionHash === candidate.input.expectedDefinitionHash
      if (!sameDraft) {
        setReconciliationRequired(true)
        setError('指标已发布，但远端草稿同时发生变化，请重新加载指标后继续编辑')
        return
      }
      const normalized = normalizeDefinition(latest.definition)
      setRecord(latest)
      setDefinition(normalized)
      setSavedFingerprint(definitionFingerprint(normalized))
      setVersionRefreshKey(value => value + 1)
    } catch (cause) {
      if (routeIdentity.current === candidate.metricId) {
        setReconciliationRequired(true)
        setError(`指标已发布，但无法确认最新聚合状态，请重新加载指标。${cause instanceof Error ? ` ${cause.message}` : ''}`)
      }
    }
  }

  async function selectMetricVersion(versionId: string) {
    if (!record || !capabilities.read) return
    const request = ++versionRequest.current
    selectedMetricVersionId.current = versionId
    setVersionLoading(true); setError(''); setVersionPreview(null); setVersionPreviewValues({})
    try {
      const [metricVersion, usage] = await Promise.all([
        metricAPI.getVersion(record.id, versionId),
        metricAPI.getVersionUsage(record.id, versionId),
      ])
      if (request !== versionRequest.current || routeIdentity.current !== record.id) return
      setSelectedMetricVersion(metricVersion)
      setSelectedVersionDataset(null)
      setVersionDatasetUnavailable(false)
      setSelectedVersionUsage(usage)
      try {
        const dataVersion = await datasetAPI.getVersion(metricVersion.definition.datasetId, metricVersion.definition.datasetVersionId)
        if (request !== versionRequest.current || routeIdentity.current !== record.id) return
        setSelectedVersionDataset(dataVersion)
      } catch (cause) {
        if (datasetPermissionDenied(cause) && request === versionRequest.current && routeIdentity.current === record.id) {
          setVersionDatasetUnavailable(true)
        } else {
          throw cause
        }
      }
    } catch (cause) {
      if (request === versionRequest.current) setError(cause instanceof Error ? cause.message : '加载指标版本失败')
    } finally {
      if (request === versionRequest.current) setVersionLoading(false)
    }
  }

  async function previewExactVersion() {
    if (!record || !selectedMetricVersion || !selectedVersionDataset || selectedMetricVersion.status !== 'PUBLISHED' || busy || writesLocked) return
    const requestedVersionId = selectedMetricVersion.id
    setBusy(true); setError(''); setMessage(''); setVersionPreview(null)
    try {
      const result = await metricAPI.previewVersion(record.id, requestedVersionId, buildPreviewInput(
        selectedVersionDataset,
        versionPreviewValues,
        selectedMetricVersion.definition.allowedDimensions.map(item => item.fieldId),
      ))
      if (selectedMetricVersionId.current !== requestedVersionId) return
      setVersionPreview(result)
      setMessage(`精确指标版本 V${selectedMetricVersion.versionNo} 试算完成 · ${result.rowCount} 行`)
    } catch (cause) {
      if (selectedMetricVersionId.current === requestedVersionId) setError(cause instanceof Error ? cause.message : '精确指标版本试算失败')
    } finally {
      if (selectedMetricVersionId.current === requestedVersionId) setBusy(false)
    }
  }

  async function deprecateVersion() {
    if (!record || !selectedMetricVersion || selectedMetricVersion.status !== 'PUBLISHED' || busy || writesLocked || !capabilities.publish) return
    const baseline = record
    const requestedVersionId = selectedMetricVersion.id
    setBusy(true); setError(''); setMessage('')
    try {
      const changed = await metricAPI.transitionVersion(record.id, requestedVersionId, {
        expectedVersion: selectedMetricVersion.metricRecordVersion,
        expectedStatus: selectedMetricVersion.status,
        targetStatus: 'DEPRECATED',
      })
      if (routeIdentity.current !== baseline.id) return
      setSelectedMetricVersion(changed)
      setVersions(current => current.map(item => item.id === changed.id ? { ...item, status: changed.status } : item))
      setMessage(`指标版本 V${changed.versionNo} 已废弃`)
      try {
        const latest = await metricAPI.get(record.id)
        if (routeIdentity.current !== baseline.id) return
        if (changed.metricRecordVersion != null && latest.version < changed.metricRecordVersion) {
          setReconciliationRequired(true)
          setError('版本已废弃，但聚合版本低于状态迁移响应下界，请重新加载指标')
          return
        }
        const sameDraft = latest.draftVersionId === baseline.draftVersionId &&
          latest.draftRecordVersion === baseline.draftRecordVersion && latest.definitionHash === baseline.definitionHash
        setRecord(latest)
        if (!sameDraft) {
          setReconciliationRequired(true)
          setError('版本已废弃，但远端草稿同时发生变化，请重新加载指标')
          return
        }
        setVersionRefreshKey(value => value + 1)
      } catch (cause) {
        if (routeIdentity.current !== baseline.id) return
        setReconciliationRequired(true)
        setError(`版本已废弃，但无法确认最新聚合状态，请重新加载指标。${cause instanceof Error ? ` ${cause.message}` : ''}`)
      }
    } catch (cause) {
      if (routeIdentity.current === baseline.id) setError(cause instanceof Error ? cause.message : '废弃指标版本失败')
    } finally {
      if (routeIdentity.current === baseline.id) setBusy(false)
    }
  }

  async function loadMoreVersions() {
    if (!record) return
    const requestedMetricId = record.id
    try {
      const page = await metricAPI.listVersions(record.id, pageSize, versions.length)
      if (routeIdentity.current !== requestedMetricId) return
      setVersions(current => [...current, ...page.items.filter(item => !current.some(existing => existing.id === item.id))])
      setVersionsTotal(page.total)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '加载更多指标版本失败')
    }
  }

  const title = isNew ? 'AI 新建指标' : record?.name || definition.metric.name || '资产管理中心'
  const actions = <>
    <button className="quiet-button" type="button" onClick={() => navigate('/metrics')}>返回目录</button>
    {record && <>
      {writesLocked && <span className="metric-lock-state">等待对账</span>}
      <button className="quiet-button" type="button" onClick={() => navigate('/metrics/new')}>新建指标</button>
      <button className="quiet-button" type="button" disabled={!selectedDatasetVersion || dirty || busy || writesLocked || !capabilities.read} onClick={previewDraft}>试算</button>
      <button className="primary-button" type="button" disabled={editorDisabled || !dirty} onClick={saveDraft}>保存草稿</button>
      {publishOutcomeUnknown
        ? <button className="primary-button" type="button" disabled={busy || datasetSnapshotUnavailable} onClick={() => publication.current && void submitPublication(publication.current)}>重试刚才发布</button>
        : <button className="quiet-button" type="button" disabled={!selectedDatasetVersion || dirty || busy || writesLocked || !capabilities.publish} onClick={publishDraft}>发布指标</button>}
      {reconciliationRequired && <button className="quiet-button" type="button" onClick={() => setReloadKey(value => value + 1)}>重新加载指标</button>}
    </>}
  </>

  return <AppShell title={title} eyebrow="资产管理中心 · 指标资产" actions={actions}>
    {(!isNew || error || message) && <div className="metric-feedback" aria-live="polite">
      {error && <span className="designer-error">{error}</span>}
      {message && <span className="designer-success">{message}</span>}
      {!isNew && <small>指标只引用精确发布版本；试算、空值和除零语义由服务端执行，跨引擎精确舍入仍按待办边界失败关闭或留待后续统一。</small>}
    </div>}
    <div className="metric-center-layout">
      <main className="metric-editor">
        {pageLoading ? <section className="metric-panel" role="status">正在加载指标定义…</section> : <>
          {!permissionsReady || (!isNew && !capabilities.read) ? <section className="metric-panel">当前账号没有读取该指标的权限。</section> : <>
            {isNew && (capabilities.manage ? <MetricAIAssistant
              initialRequirement={initialAIRequirement}
              preferredDatasetId={preferredDatasetId}
              safeDatasetExtension={safeDatasetExtension}
              datasets={datasets.filter(dataset => dataset.status === 'PUBLISHED' && Boolean(dataset.currentPublishedVersionId))}
              onConfirm={createAIMetricDraft}
              onModifyDataset={openDatasetAI}
            /> : <section className="metric-panel">当前账号没有创建指标的权限。</section>)}
            {!isNew && <fieldset className="metric-definition" disabled={editorDisabled}>
            <section className="metric-panel metric-meta">
              <header><span className="eyebrow">基础信息</span><h2>指标定义</h2></header>
              <label>指标编码<input aria-label="指标编码" value={definition.metric.code} disabled={Boolean(record)} onChange={event => updateMetric({ code: event.target.value })} /></label>
              <label>指标名称<input aria-label="指标名称" value={definition.metric.name} onChange={event => updateMetric({ name: event.target.value })} /></label>
              <label>指标类型<select aria-label="指标类型" value={definition.metric.type} disabled><option value="ATOMIC">原子指标</option>{definition.metric.type !== 'ATOMIC' && <option value={definition.metric.type}>{definition.metric.type}</option>}</select></label>
              <label className="wide">说明<textarea aria-label="指标说明" value={definition.metric.description} onChange={event => updateMetric({ description: event.target.value })} /></label>
            </section>

            <section className="metric-panel metric-source">
              <header><span className="eyebrow">不可变来源</span><h2>数据集版本</h2></header>
              <label>数据集<select aria-label="指标数据集" value={definition.datasetId} disabled={Boolean(record)} onChange={event => void selectDataset(event.target.value)}><option value="">请选择</option>{datasets.map(dataset => <option key={dataset.id} value={dataset.id}>{dataset.name} · {dataset.status}</option>)}</select></label>
              <label>精确发布版本<select aria-label="指标数据集版本" value={definition.datasetVersionId} onChange={event => void selectDatasetVersion(event.target.value)}><option value="">请选择</option>{datasetVersions.map(version => <option key={version.id} value={version.id} disabled={version.status !== 'PUBLISHED' && version.id !== definition.datasetVersionId}>V{version.versionNo} · {version.status} · {version.id}</option>)}</select></label>
              {selectedDatasetVersion && <dl className="metric-source-snapshot"><div><dt>版本 ID</dt><dd>{selectedDatasetVersion.id}</dd></div><div><dt>DSL 摘要</dt><dd>{selectedDatasetVersion.dslHash.slice(0, 16)}</dd></div><div><dt>状态</dt><dd>{selectedDatasetVersion.status}</dd></div></dl>}
            </section>

            {record && !selectedDatasetVersion && <section className="metric-panel metric-definition-fallback" aria-label="指标只读口径">
              <header><span className="eyebrow">服务端指标定义</span><h2>只读精确口径</h2></header>
              <p className="metric-empty">精确数据集字段元数据当前不可用，以下内容直接来自指标定义，不会按空字段解释。</p>
              <dl><div><dt>数据集 / 版本</dt><dd>{definition.datasetId} / {definition.datasetVersionId}</dd></div><div><dt>表达式</dt><dd className="metric-expression-json"><code>{JSON.stringify(definition.expression)}</code></dd></div><div><dt>聚合 / 可加性</dt><dd>{definition.aggregation} / {definition.additivity}</dd></div><div><dt>时间口径</dt><dd>{definition.timeFieldId || '未设置'} / {definition.timeGrain}</dd></div><div><dt>允许维度</dt><dd>{definition.allowedDimensions.length ? definition.allowedDimensions.map(item => `${item.name}（${item.fieldId}）`).join('、') : '无'}</dd></div></dl>
              <MetricDefinitionJSON definition={definition} label="指标草稿完整定义 JSON" />
            </section>}

            <section className="metric-panel metric-expression">
              <header><span className="eyebrow">结构化口径</span><h2>表达式与聚合</h2></header>
              {advancedExpression ? <div className="advanced-expression"><strong>高级表达式只读</strong><p>派生、比率和指标引用的可视化编辑器将在后续增强；本页面不会执行或改写该表达式。</p><pre>{JSON.stringify(definition.expression, null, 2)}</pre></div> : <label>原子字段<select aria-label="原子指标字段" value={atomicFieldId} onChange={event => updateDefinition({ expression: { type: 'FIELD_REF', fieldId: event.target.value } })}><option value="">{definition.aggregation === 'COUNT' || definition.aggregation === 'COUNT_DISTINCT' ? '请选择计数字段' : '请选择数值字段'}</option>{atomicFields.map(field => <option key={field.id} value={field.id}>{field.name} · {field.role} · {field.canonicalType}</option>)}</select></label>}
              <label>聚合<select aria-label="指标聚合" value={definition.aggregation} onChange={event => updateAggregation(event.target.value as MetricDefinition['aggregation'])}>{['NONE', 'SUM', 'AVG', 'MIN', 'MAX', 'COUNT', 'COUNT_DISTINCT'].map(value => <option key={value}>{value}</option>)}</select></label>
              <label>单位<input aria-label="指标单位" value={definition.unit} onChange={event => updateDefinition({ unit: event.target.value })} /></label>
              <label>数字格式<input aria-label="指标数字格式" value={definition.numberFormat} onChange={event => updateDefinition({ numberFormat: event.target.value })} /></label>
              <label>小数位数<input aria-label="指标小数位数" type="number" min={0} max={12} value={definition.decimalScale} onChange={event => updateDefinition({ decimalScale: Number(event.target.value) })} /></label>
              <label>可加性<select aria-label="指标可加性" value={definition.additivity} onChange={event => updateDefinition({ additivity: event.target.value as MetricDefinition['additivity'], nonAdditiveDimensionFieldIds: [] })}>{['ADDITIVE', 'SEMI_ADDITIVE', 'NON_ADDITIVE'].map(value => <option key={value}>{value}</option>)}</select></label>
              <label>时间字段<select aria-label="指标时间字段" value={definition.timeFieldId} onChange={event => {
                const fieldId = event.target.value
                const field = timeFields.find(item => item.id === fieldId)
                const allowedDimensions = field && !definition.allowedDimensions.some(item => item.fieldId === fieldId)
                  ? [...definition.allowedDimensions, { fieldId, name: field.name, hierarchyFieldIds: [fieldId], sortDirection: 'ASC' as const, nullLabel: '未分类' }]
                  : definition.allowedDimensions
                updateDefinition({ timeFieldId: fieldId, timeGrain: fieldId ? definition.timeGrain === 'NONE' ? 'MONTH' : definition.timeGrain : 'NONE', allowedDimensions })
              }}><option value="">不使用时间字段</option>{timeFields.map(field => <option key={field.id} value={field.id}>{field.name}</option>)}</select></label>
              <label>时间粒度<select aria-label="指标时间粒度" value={definition.timeGrain} disabled={!definition.timeFieldId} onChange={event => updateDefinition({ timeGrain: event.target.value as MetricDefinition['timeGrain'] })}>{(definition.timeFieldId ? ['DAY', 'WEEK', 'MONTH', 'QUARTER', 'YEAR'] : ['NONE']).map(value => <option key={value}>{value}</option>)}</select></label>
              <div className="metric-fixed-semantics"><span>舍入声明<strong>HALF_UP</strong></span><span>空值<strong>IGNORE</strong></span><span>除零<strong>NULL</strong></span></div>
            </section>

            <section className="metric-panel metric-dimensions">
              <header><span className="eyebrow">适用范围</span><h2>允许维度</h2></header>
              {!dimensionFields.length && <p className="metric-empty">当前数据集版本没有可作为维度的非度量字段。</p>}
              {dimensionFields.map(field => {
                const dimension = definition.allowedDimensions.find(item => item.fieldId === field.id)
                return <article key={field.id}>
                  <label className="metric-dimension-toggle"><input aria-label={`允许维度 ${field.name}`} type="checkbox" checked={Boolean(dimension)} onChange={() => toggleDimension(field)} />{field.name}<small>{field.role} · {field.code}</small></label>
                  {dimension && <div className="metric-dimension-config">
                    <label>显示名称<input aria-label={`维度名称 ${field.name}`} value={dimension.name} onChange={event => updateDimension(field.id, { name: event.target.value })} /></label>
                    <label>层级字段<select multiple aria-label={`维度层级 ${field.name}`} value={dimension.hierarchyFieldIds} onChange={event => updateDimension(field.id, { hierarchyFieldIds: Array.from(event.target.selectedOptions, option => option.value) })}>{dimensionFields.map(candidate => <option key={candidate.id} value={candidate.id}>{candidate.name}</option>)}</select></label>
                    <label>排序<select aria-label={`维度排序 ${field.name}`} value={dimension.sortDirection} onChange={event => updateDimension(field.id, { sortDirection: event.target.value as 'ASC' | 'DESC' })}><option>ASC</option><option>DESC</option></select></label>
                    <label>空值标签<input aria-label={`维度空值标签 ${field.name}`} value={dimension.nullLabel} onChange={event => updateDimension(field.id, { nullLabel: event.target.value })} /></label>
                    {definition.additivity === 'SEMI_ADDITIVE' && <label className="metric-dimension-toggle"><input aria-label={`不可加维度 ${field.name}`} type="checkbox" checked={definition.nonAdditiveDimensionFieldIds.includes(field.id)} onChange={() => updateDefinition({ nonAdditiveDimensionFieldIds: definition.nonAdditiveDimensionFieldIds.includes(field.id) ? definition.nonAdditiveDimensionFieldIds.filter(id => id !== field.id) : [...definition.nonAdditiveDimensionFieldIds, field.id] })} />该维度不可直接求和</label>}
                  </div>}
                </article>
              })}
            </section>

            </fieldset>}
          </>}
          {permissionsReady && capabilities.read && parameters.length > 0 && <section className="metric-panel metric-parameters"><header><span className="eyebrow">服务端试算</span><h2>验证参数</h2></header>{parameters.map(parameter => <label key={parameter.code}>{parameter.name || parameter.code}<input aria-label={`指标参数 ${parameter.code}`} type={parameter.dataType === 'DATE' ? 'date' : 'text'} placeholder={parameter.multiValue ? '多个值请用逗号分隔' : parameter.dataType} value={previewValues[parameter.code] ?? ''} disabled={busy || writesLocked} onChange={event => setPreviewValues(current => ({ ...current, [parameter.code]: event.target.value }))} /></label>)}</section>}
          {preview && <PreviewTable preview={preview} />}
          {record && <MetricVersionManager
            versions={versions}
            total={versionsTotal}
            currentVersionId={record.currentPublishedVersionId ?? ''}
            selected={selectedMetricVersion}
            usage={selectedVersionUsage}
            parameters={versionParameters}
            datasetUnavailable={versionDatasetUnavailable}
            datasetReady={Boolean(selectedVersionDataset)}
            parameterValues={versionPreviewValues}
            preview={versionPreview}
            loading={versionLoading}
            busy={busy}
            writesLocked={writesLocked}
            canPublish={capabilities.publish}
            onSelect={selectMetricVersion}
            onLoadMore={loadMoreVersions}
            onParameterChange={(code, value) => setVersionPreviewValues(current => ({ ...current, [code]: value }))}
            onPreview={previewExactVersion}
            onDeprecate={deprecateVersion}
          />}
        </>}
      </main>
    </div>
  </AppShell>
}

const metricAIStrategyLabels: Record<MetricAuthoringProposal['strategy'], string> = {
  REUSE_METRIC: '复用已有指标',
  CREATE_ON_DATASET: '可基于现有数据集创建',
  CREATE_DATASET: '需要新建数据集',
  MODIFY_DATASET: '需要先改造数据集',
  DATA_GAP: '当前授权数据存在缺口',
  NEEDS_CLARIFICATION: '需要补充业务口径',
}

const uuidText = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
const uuidInParenthesesPattern = new RegExp(`[（(][^（）()]*${uuidText}[^（）()]*[）)]`, 'gi')
const uuidPattern = new RegExp(uuidText, 'gi')

/** 面向业务审核隐藏检索主键；完整追踪信息仍收纳在折叠的技术信息中。 */
function readableProposalText(value: string): string {
  return value
    .replace(uuidInParenthesesPattern, '')
    .replace(uuidPattern, '')
    .replace(/(?:数据集|草稿版本|版本)?\s*ID\s*[：:]\s*/gi, '')
    .replace(/[（(]\s*[,，/·\s]*[）)]/g, '')
    .replace(/\s+([，。；：])/g, '$1')
    .replace(/([，；：])\s*[,，；：]+/g, '$1')
    .replace(/[ \t]{2,}/g, ' ')
    .trim()
}

function proposalSentences(value: string): string[] {
  const readable = readableProposalText(value)
  return (readable.match(/[^。！？；\n]+[。！？；]?/g) ?? [])
    .map(item => item.trim())
    .filter(Boolean)
}

function proposalHeading(proposal: MetricAuthoringProposal): string {
  if (proposal.strategy === 'CREATE_ON_DATASET' && proposal.candidateMetricDefinition?.metric.name) {
    return `创建“${proposal.candidateMetricDefinition.metric.name}”指标`
  }
  if (proposal.strategy === 'CREATE_DATASET') return '新建数据集后创建指标'
  if (proposal.strategy === 'MODIFY_DATASET') return '完善数据集后创建指标'
  if (proposal.strategy === 'REUSE_METRIC') return '直接复用已有指标'
  if (proposal.strategy === 'NEEDS_CLARIFICATION') return '补充口径后继续生成'
  if (proposal.strategy === 'DATA_GAP') return '补充可用数据后继续生成'
  return metricAIStrategyLabels[proposal.strategy]
}

const evidenceLabels: Record<MetricAuthoringProposal['retrievalEvidence'][number]['sourceType'], string> = {
  DATASET: '数据集',
  FIELD: '字段',
  METRIC: '指标',
}

type MetricAIHintField = DatasetField & {
  key: string
  datasetId: string
  datasetName: string
}

type MetricAIHintSelection = {
  datasetIds: string[]
  measureFieldKeys: string[]
  dateFieldKey: string
  dimensionFieldKeys: string[]
  aggregation: string
}

const metricAIHintAggregationOptions = [
  { value: '', label: '由 AI 判断', shortLabel: 'AI 判断' },
  { value: 'SUM', label: '求和（SUM）', shortLabel: '求和' },
  { value: 'COUNT', label: '计数（COUNT）', shortLabel: '计数' },
  { value: 'COUNT_DISTINCT', label: '去重计数（COUNT DISTINCT）', shortLabel: '去重计数' },
  { value: 'AVG', label: '平均值（AVG）', shortLabel: '平均' },
  { value: 'MIN', label: '最小值（MIN）', shortLabel: '最小' },
  { value: 'MAX', label: '最大值（MAX）', shortLabel: '最大' },
] as const
const metricAIHintBlockMarker = '\n\n【AI 参考条件】'
const metricAIHintNumericAggregations = new Set(['SUM', 'AVG', 'MIN', 'MAX'])
const metricAIHintNumericTypes = new Set(['INTEGER', 'DECIMAL'])
const metricAIHintDistinctRoles = new Set(['IDENTIFIER', 'DIMENSION', 'ATTRIBUTE'])
const metricAIHintTemporalTypes = new Set(['DATE', 'DATETIME', 'TIMESTAMP'])

function metricAIHintFieldKey(datasetId: string, fieldId: string): string {
  return `${datasetId}::${fieldId}`
}

function metricAIHintFieldIsTemporal(field: MetricAIHintField): boolean {
  return field.role === 'TIME' || metricAIHintTemporalTypes.has(field.canonicalType)
}

function metricAIHintFieldIsAggregatableNumeric(field: MetricAIHintField): boolean {
  return metricAIHintNumericTypes.has(field.canonicalType) && ['MEASURE', 'ATTRIBUTE'].includes(field.role)
}

/**
 * 统计对象由聚合语义决定：数值聚合不接受 ID，计数不把日期当业务对象，
 * 去重计数优先业务标识/维度/属性。未指定聚合时只暴露这些安全候选与可聚合数值。
 */
function metricAIHintStatisticalFields(fields: MetricAIHintField[], aggregation: string): MetricAIHintField[] {
  const eligible = fields.filter(field => {
    if (metricAIHintFieldIsTemporal(field)) return false
    if (metricAIHintNumericAggregations.has(aggregation)) return metricAIHintFieldIsAggregatableNumeric(field)
    if (aggregation === 'COUNT') return true
    if (aggregation === 'COUNT_DISTINCT') {
      return metricAIHintDistinctRoles.has(field.role) || metricAIHintNumericTypes.has(field.canonicalType)
    }
    return metricAIHintDistinctRoles.has(field.role) || metricAIHintFieldIsAggregatableNumeric(field)
  })
  if (aggregation !== 'COUNT_DISTINCT') return eligible
  const priority = (field: MetricAIHintField) => {
    if (field.role === 'IDENTIFIER') return 0
    if (field.role === 'DIMENSION') return 1
    if (field.role === 'ATTRIBUTE') return 2
    return 3
  }
  return [...eligible].sort((left, right) => priority(left) - priority(right))
}

function metricAIHintStatisticalFieldScope(aggregation: string): string {
  if (metricAIHintNumericAggregations.has(aggregation)) {
    return `${aggregation} 仅可选择数值型度量或数值属性字段。`
  }
  if (aggregation === 'COUNT') {
    return 'COUNT 可选择任意非日期的可见输出字段并按非空值计数；留空时可由 AI 判断是否统计记录数。'
  }
  if (aggregation === 'COUNT_DISTINCT') {
    return 'COUNT DISTINCT 优先选择标识符、维度或属性字段，也支持数值业务字段。'
  }
  return 'AI 判断时可参考标识符、维度、属性及可聚合数值字段；日期时间不作为统计对象。'
}

function metricAIHintStatisticalFieldQualifier(aggregation: string): string {
  if (metricAIHintNumericAggregations.has(aggregation)) return `${aggregation} 数值聚合对象`
  if (aggregation === 'COUNT') return 'COUNT 非空计数对象'
  if (aggregation === 'COUNT_DISTINCT') return 'COUNT DISTINCT 去重计数对象'
  return '候选统计对象，最终由 AI 判断'
}

function metricAIDatasetReferenceLabel(dataset: DatasetSummary): string {
  const sourceName = dataset.originDataSourceName?.trim()
  const tableName = dataset.originTableName?.trim()
  const sourceParts = [
    sourceName ? `数据源：${sourceName}` : '',
    tableName ? `源表：${tableName}` : '',
  ].filter(Boolean)
  return sourceParts.length ? `${dataset.name}（${sourceParts.join('；')}）` : dataset.name
}

function metricAIDatasetSourceLabel(dataset: DatasetSummary): string {
  const sourceName = dataset.originDataSourceName?.trim()
  const tableName = dataset.originTableName?.trim()
  if (sourceName && tableName) return `${sourceName} · ${tableName}`
  if (sourceName) return sourceName
  if (tableName) return `源表 · ${tableName}`
  return dataset.originTableId ? '映射表数据集' : '普通数据集'
}

/** 把可选控件稳定翻译为自然语言，保持现有只接收 requirement 的后端契约。 */
function metricAIRequirementWithHints(
  requirement: string,
  datasets: DatasetSummary[],
  fields: MetricAIHintField[],
  selection: MetricAIHintSelection,
): string {
  const rawRequirement = requirement.trim()
  const existingBlock = rawRequirement.indexOf(metricAIHintBlockMarker)
  const base = existingBlock >= 0 ? rawRequirement.slice(0, existingBlock).trim() : rawRequirement
  const selectedDatasetIds = new Set(selection.datasetIds)
  const selectedMeasureKeys = new Set(selection.measureFieldKeys)
  const selectedDimensionKeys = new Set(selection.dimensionFieldKeys)
  const selectedDatasets = datasets.filter(dataset => selectedDatasetIds.has(dataset.id))
  const measures = fields.filter(field => selectedMeasureKeys.has(field.key))
  const dateField = fields.find(field => field.key === selection.dateFieldKey)
  const dimensions = fields.filter(field => selectedDimensionKeys.has(field.key))
  const aggregation = metricAIHintAggregationOptions.find(option => option.value === selection.aggregation)
  const lines: string[] = []
  if (selectedDatasets.length) {
    lines.push(`- 优先参考的已发布数据集：${selectedDatasets.map(metricAIDatasetReferenceLabel).join('、')}`)
  }
  if (measures.length) {
    lines.push(`- 统计字段（${metricAIHintStatisticalFieldQualifier(selection.aggregation)}）：${measures.map(field => `${field.datasetName} / ${field.name}（${field.code}）`).join('、')}`)
  }
  if (dateField) lines.push(`- 统计日期字段：${dateField.datasetName} / ${dateField.name}（${dateField.code}）`)
  if (dimensions.length) {
    lines.push(`- 分析维度：${dimensions.map(field => `${field.datasetName} / ${field.name}（${field.code}）`).join('、')}`)
  }
  if (aggregation?.value) lines.push(`- 统计口径与聚合：${aggregation.label}`)
  if (!lines.length) return rawRequirement
  return `${base}${metricAIHintBlockMarker}\n以下条件由用户选择；未指定的内容请由 AI 基于授权资产补全：\n${lines.join('\n')}`
}

function metricAISafeExtensionBlock(dataset: DatasetSummary | undefined): string {
  if (!dataset) return ''
  return [
    '',
    '【数据集 DAG 安全拓展约束】',
    `- 拓展基线：${metricAIDatasetReferenceLabel(dataset)}`,
    '- 优先直接使用该数据集当前发布版本生成指标。',
    '- 只有当前 DAG 无法提供准确输出时才设计新版本；只允许追加组件、字段或输出，不得删除、改名或改变现有输出、过滤、关联、分组与聚合逻辑。',
    '- 既有指标继续固定引用原精确发布版本；新指标只能引用通过审批的新精确发布版本。',
  ].join('\n')
}

const metricDatasetAIHintAggregations = new Set<DatasetAIHintAggregation>(['', 'SUM', 'AVG', 'COUNT', 'COUNT_DISTINCT', 'MIN', 'MAX'])

function inferDatasetAIAggregation(selected: string, instruction: string): DatasetAIHintAggregation {
  const normalized = selected.trim().toUpperCase() as DatasetAIHintAggregation
  if (metricDatasetAIHintAggregations.has(normalized) && normalized) return normalized
  if (/COUNT[_\s]+DISTINCT|去重计数|去重统计/i.test(instruction)) return 'COUNT_DISTINCT'
  if (/\bCOUNT\s*\(|\bCOUNT\b|计数|交易量|订单量|笔数|数量/i.test(instruction)) return 'COUNT'
  if (/\bAVG\s*\(|\bAVG\b|平均值|平均数|均值/i.test(instruction)) return 'AVG'
  if (/\bMIN\s*\(|\bMIN\b|最小值/i.test(instruction)) return 'MIN'
  if (/\bMAX\s*\(|\bMAX\b|最大值/i.test(instruction)) return 'MAX'
  if (/\bSUM\s*\(|\bSUM\b|求和|总额|销售额|金额合计/i.test(instruction)) return 'SUM'
  return ''
}

function inferDatasetAITimeGrain(instruction: string): DatasetAIHintTimeGrain {
  if (/\bQUARTER\b|按季(?:度)?|季度/i.test(instruction)) return 'QUARTER'
  if (/\bMONTH\b|按月|月度|月份/i.test(instruction)) return 'MONTH'
  if (/\bWEEK\b|按周|周度/i.test(instruction)) return 'WEEK'
  if (/\bYEAR\b|按年|年度|年份/i.test(instruction)) return 'YEAR'
  if (/\bDAY\b|按日|每日|日度/i.test(instruction)) return 'DAY'
  return ''
}

function metricFieldPhysicalHint(field: Pick<DatasetField, 'tableId' | 'column'>): DatasetAIFieldHint | null {
  return field.tableId && field.column ? { tableId: field.tableId, column: field.column } : null
}

function uniqueDatasetAIFieldHints(values: Array<DatasetAIFieldHint | null>): DatasetAIFieldHint[] {
  const seen = new Set<string>()
  return values.flatMap(value => {
    if (!value) return []
    const key = `${value.tableId}\u0000${value.column}`
    if (seen.has(key)) return []
    seen.add(key)
    return [value]
  }).slice(0, 32)
}

function metricEvidenceFieldKey(datasetId: string, datasetVersionId: string, fieldId: string): string {
  return `${datasetId}\u0000${datasetVersionId}\u0000${fieldId}`
}

async function buildMetricDatasetAIHints({
  proposal,
  instruction,
  datasets,
  selectedDatasetIds,
  selectedMeasureFieldKeys,
  selectedDateFieldKey,
  selectedDimensionFieldKeys,
  selectedAggregation,
  visibleFields,
  loadedSnapshots,
}: {
  proposal: MetricAuthoringProposal
  instruction: string
  datasets: DatasetSummary[]
  selectedDatasetIds: string[]
  selectedMeasureFieldKeys: string[]
  selectedDateFieldKey: string
  selectedDimensionFieldKeys: string[]
  selectedAggregation: string
  visibleFields: MetricAIHintField[]
  loadedSnapshots: Record<string, PublishedVersionRecord>
}): Promise<DatasetAIPlanHints | undefined> {
  const summaries = new Map(datasets.map(dataset => [dataset.id, dataset]))
  const versionRefs = new Map<string, { datasetId: string; versionId: string }>()
  const addVersionRef = (datasetId: string, versionId: string) => {
    if (datasetId && versionId) versionRefs.set(`${datasetId}\u0000${versionId}`, { datasetId, versionId })
  }
  for (const datasetId of selectedDatasetIds) {
    addVersionRef(datasetId, summaries.get(datasetId)?.currentPublishedVersionId ?? '')
  }
  for (const evidence of proposal.retrievalEvidence) addVersionRef(evidence.datasetId, evidence.datasetVersionId)
  addVersionRef(proposal.targetDatasetId, proposal.targetDatasetVersionId)

  const snapshots = new Map<string, PublishedVersionRecord>()
  for (const snapshot of Object.values(loadedSnapshots)) {
    snapshots.set(`${snapshot.datasetId}\u0000${snapshot.id}`, snapshot)
  }
  await Promise.all([...versionRefs.entries()].map(async ([key, reference]) => {
    if (snapshots.has(key)) return
    try {
      const snapshot = await datasetAPI.getVersion(reference.datasetId, reference.versionId)
      if (snapshot.datasetId === reference.datasetId && snapshot.id === reference.versionId) snapshots.set(key, snapshot)
    } catch {
      // 权限或版本可能在审核后发生变化；服务端仍会按现有授权检索，不能在前端猜测物理字段。
    }
  }))

  const preferredTableIDs: string[] = []
  const addPreferredTable = (tableId: string | undefined) => {
    if (tableId && !preferredTableIDs.includes(tableId) && preferredTableIDs.length < 16) preferredTableIDs.push(tableId)
  }
  for (const datasetId of selectedDatasetIds) addPreferredTable(summaries.get(datasetId)?.originTableId)
  for (const evidence of proposal.retrievalEvidence) addPreferredTable(summaries.get(evidence.datasetId)?.originTableId)
  for (const snapshot of snapshots.values()) datasetPhysicalTableIDs(snapshot).forEach(addPreferredTable)

  const evidenceFields = new Map<string, DatasetField>()
  for (const evidence of proposal.retrievalEvidence) {
    if (evidence.sourceType !== 'FIELD') continue
    const snapshot = snapshots.get(`${evidence.datasetId}\u0000${evidence.datasetVersionId}`)
    const field = datasetFields(snapshot ?? null).find(item => item.id === evidence.sourceId)
    if (field?.tableId && field.column) {
      evidenceFields.set(metricEvidenceFieldKey(evidence.datasetId, evidence.datasetVersionId, evidence.sourceId), field)
    }
  }
  const evidenceFieldValues = [...evidenceFields.values()]
  const selectedMeasureKeys = new Set(selectedMeasureFieldKeys)
  const selectedDimensionKeys = new Set(selectedDimensionFieldKeys)
  const aggregation = inferDatasetAIAggregation(selectedAggregation, instruction)
  const explicitMeasureFields = visibleFields.filter(field => selectedMeasureKeys.has(field.key))
  const inferredMeasureFields = evidenceFieldValues.filter(field => {
    if (metricAIHintFieldIsTemporal(field as MetricAIHintField)) return false
    if (metricAIHintNumericAggregations.has(aggregation)) return metricAIHintNumericTypes.has(field.canonicalType) && ['MEASURE', 'ATTRIBUTE'].includes(field.role)
    if (aggregation === 'COUNT') return field.role === 'IDENTIFIER'
    if (aggregation === 'COUNT_DISTINCT') return metricAIHintDistinctRoles.has(field.role)
    return field.role === 'MEASURE' || field.role === 'IDENTIFIER'
  })
  const measureFields = uniqueDatasetAIFieldHints(
    (explicitMeasureFields.length ? explicitMeasureFields : inferredMeasureFields).map(metricFieldPhysicalHint),
  )

  const explicitTimeField = visibleFields.find(field => field.key === selectedDateFieldKey)
  const inferredTimeField = evidenceFieldValues.find(field =>
    field.role === 'TIME' || metricAIHintTemporalTypes.has(field.canonicalType))
  const timeField = metricFieldPhysicalHint(explicitTimeField ?? inferredTimeField ?? { tableId: '', column: '' }) ?? undefined
  const occupiedFields = new Set([
    ...measureFields.map(field => `${field.tableId}\u0000${field.column}`),
    ...(timeField ? [`${timeField.tableId}\u0000${timeField.column}`] : []),
  ])
  const explicitDimensionFields = visibleFields.filter(field => selectedDimensionKeys.has(field.key))
  const inferredDimensionFields = evidenceFieldValues.filter(field =>
    ['DIMENSION', 'ATTRIBUTE', 'IDENTIFIER'].includes(field.role) &&
    !metricAIHintTemporalTypes.has(field.canonicalType))
  const dimensionFields = uniqueDatasetAIFieldHints(
    (explicitDimensionFields.length ? explicitDimensionFields : inferredDimensionFields)
      .map(metricFieldPhysicalHint)
      .filter(field => field && !occupiedFields.has(`${field.tableId}\u0000${field.column}`)),
  )
  for (const field of [...measureFields, ...(timeField ? [timeField] : []), ...dimensionFields]) addPreferredTable(field.tableId)
  const timeGrain = inferDatasetAITimeGrain(instruction)
  if (!preferredTableIDs.length && !aggregation && !measureFields.length && !timeField && !dimensionFields.length && !timeGrain) return undefined
  return {
    preferredTableIds: preferredTableIDs,
    aggregation,
    measureFields,
    ...(timeField ? { timeField } : {}),
    dimensionFields,
    timeGrain,
  }
}

function MetricAIAssistant({
  initialRequirement,
  preferredDatasetId,
  safeDatasetExtension,
  datasets,
  onConfirm,
  onModifyDataset,
}: {
  initialRequirement: string
  preferredDatasetId: string
  safeDatasetExtension: boolean
  datasets: DatasetSummary[]
  onConfirm: (definition: MetricDefinition) => Promise<void>
  onModifyDataset: (
    strategy: 'CREATE_DATASET' | 'MODIFY_DATASET',
    datasetId: string,
    instruction: string,
    requirement: string,
    hints?: DatasetAIPlanHints,
  ) => void
}) {
  const [requirement, setRequirement] = useState(initialRequirement)
  const [proposal, setProposal] = useState<MetricAuthoringProposal | null>(null)
  const [proposalOpen, setProposalOpen] = useState(false)
  const [requestID, setRequestID] = useState('')
  const [contextHash, setContextHash] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState('')
  const [activePrompt, setActivePrompt] = useState('')
  const [generationMode, setGenerationMode] = useState<'generate' | 'retry' | 'revise'>('generate')
  const [revisionOpen, setRevisionOpen] = useState(false)
  const [revision, setRevision] = useState('')
  const [hintDatasetIds, setHintDatasetIds] = useState<string[]>(() =>
    preferredDatasetId && datasets.some(dataset => dataset.id === preferredDatasetId) ? [preferredDatasetId] : [])
  const [hintSnapshots, setHintSnapshots] = useState<Record<string, PublishedVersionRecord>>({})
  const [hintMeasureFieldKeys, setHintMeasureFieldKeys] = useState<string[]>([])
  const [hintDateFieldKey, setHintDateFieldKey] = useState('')
  const [hintDimensionFieldKeys, setHintDimensionFieldKeys] = useState<string[]>([])
  const [hintAggregation, setHintAggregation] = useState('')
  const [hintFieldsLoading, setHintFieldsLoading] = useState(false)
  const [hintFieldsError, setHintFieldsError] = useState('')
  const hintSnapshotRequest = useRef(0)

  const selectedHintDatasetIds = useMemo(() => new Set(hintDatasetIds), [hintDatasetIds])
  const selectedHintDatasets = useMemo(
    () => datasets.filter(dataset => selectedHintDatasetIds.has(dataset.id)),
    [datasets, selectedHintDatasetIds],
  )
  const hintFields = useMemo<MetricAIHintField[]>(() => selectedHintDatasets.flatMap(dataset =>
    datasetFields(hintSnapshots[dataset.id] ?? null).map(field => ({
      ...field,
      key: metricAIHintFieldKey(dataset.id, field.id),
      datasetId: dataset.id,
      datasetName: dataset.name,
    }))), [hintSnapshots, selectedHintDatasets])
  const hintTimeFields = useMemo(() => hintFields.filter(field =>
    field.role === 'TIME' && ['DATE', 'DATETIME', 'TIMESTAMP'].includes(field.canonicalType)), [hintFields])
  const hintMeasureFields = useMemo(
    () => metricAIHintStatisticalFields(hintFields, hintAggregation),
    [hintAggregation, hintFields],
  )
  const hintDimensionFields = useMemo(() => hintFields.filter(field =>
    ['DIMENSION', 'IDENTIFIER'].includes(field.role) ||
    (field.role === 'ATTRIBUTE' && !['INTEGER', 'DECIMAL'].includes(field.canonicalType))), [hintFields])
  const extensionDataset = useMemo(
    () => safeDatasetExtension ? datasets.find(dataset => dataset.id === preferredDatasetId) : undefined,
    [datasets, preferredDatasetId, safeDatasetExtension],
  )
  const composedRequirement = useMemo(() => `${metricAIRequirementWithHints(requirement, datasets, hintFields, {
    datasetIds: hintDatasetIds,
    measureFieldKeys: hintMeasureFieldKeys,
    dateFieldKey: hintDateFieldKey,
    dimensionFieldKeys: hintDimensionFieldKeys,
    aggregation: hintAggregation,
  })}${metricAISafeExtensionBlock(extensionDataset)}`, [datasets, extensionDataset, hintAggregation, hintDatasetIds, hintDateFieldKey, hintDimensionFieldKeys, hintFields, hintMeasureFieldKeys, requirement])

  useEffect(() => {
    const request = ++hintSnapshotRequest.current
    let active = true
    if (!selectedHintDatasets.length) {
      queueMicrotask(() => {
        if (!active || request !== hintSnapshotRequest.current) return
        setHintSnapshots({})
        setHintFieldsLoading(false)
        setHintFieldsError('')
      })
      return () => { active = false }
    }
    queueMicrotask(() => {
      if (!active || request !== hintSnapshotRequest.current) return
      setHintFieldsLoading(true)
      setHintFieldsError('')
    })
    Promise.allSettled(selectedHintDatasets.map(async dataset => {
      if (!dataset.currentPublishedVersionId) throw new Error(`${dataset.name}没有可用发布版本`)
      return [dataset.id, await datasetAPI.getVersion(dataset.id, dataset.currentPublishedVersionId)] as const
    })).then(results => {
      if (!active || request !== hintSnapshotRequest.current) return
      const snapshots: Record<string, PublishedVersionRecord> = {}
      let failures = 0
      for (const result of results) {
        if (result.status === 'fulfilled') snapshots[result.value[0]] = result.value[1]
        else failures += 1
      }
      setHintSnapshots(snapshots)
      setHintFieldsError(failures ? `${failures} 个数据集的字段暂时不可读；仍可直接描述需求交给 AI 检索。` : '')
    }).finally(() => {
      if (active && request === hintSnapshotRequest.current) setHintFieldsLoading(false)
    })
    return () => { active = false }
  }, [selectedHintDatasets])

  useEffect(() => {
    const availableKeys = new Set(hintFields.map(field => field.key))
    const availableMeasureKeys = new Set(hintMeasureFields.map(field => field.key))
    queueMicrotask(() => {
      setHintMeasureFieldKeys(current => current.filter(key => availableMeasureKeys.has(key)))
      setHintDateFieldKey(current => current && !availableKeys.has(current) ? '' : current)
      setHintDimensionFieldKeys(current => current.filter(key => availableKeys.has(key)))
    })
  }, [hintFields, hintMeasureFields])

  useEffect(() => {
    if (!proposalOpen) return
    const previousOverflow = document.body.style.overflow
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setProposalOpen(false)
    }
    document.body.style.overflow = 'hidden'
    document.addEventListener('keydown', closeOnEscape)
    return () => {
      document.body.style.overflow = previousOverflow
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [proposalOpen])

  function clearProposal() {
    setProposal(null)
    setProposalOpen(false)
    setRequestID('')
    setContextHash('')
    setActivePrompt('')
    setRevisionOpen(false)
    setRevision('')
    setError('')
  }

  function updateRequirement(value: string) {
    setRequirement(value)
    clearProposal()
  }

  function toggleHintDataset(datasetId: string) {
    setHintDatasetIds(current => current.includes(datasetId)
      ? current.filter(id => id !== datasetId)
      : [...current, datasetId])
    clearProposal()
  }

  function updateHintDateField(value: string) {
    setHintDateFieldKey(value)
    clearProposal()
  }

  function toggleHintMeasure(fieldKey: string) {
    setHintMeasureFieldKeys(current => current.includes(fieldKey)
      ? current.filter(key => key !== fieldKey)
      : [...current, fieldKey])
    clearProposal()
  }

  function toggleHintDimension(fieldKey: string) {
    setHintDimensionFieldKeys(current => current.includes(fieldKey)
      ? current.filter(key => key !== fieldKey)
      : [...current, fieldKey])
    clearProposal()
  }

  function updateHintAggregation(value: string) {
    setHintAggregation(value)
    const legalFieldKeys = new Set(metricAIHintStatisticalFields(hintFields, value).map(field => field.key))
    setHintMeasureFieldKeys(current => current.filter(key => legalFieldKeys.has(key)))
    clearProposal()
  }

  function clearHints() {
    setHintDatasetIds([])
    setHintSnapshots({})
    setHintMeasureFieldKeys([])
    setHintDateFieldKey('')
    setHintDimensionFieldKeys([])
    setHintAggregation('')
    setHintFieldsError('')
    clearProposal()
  }

  async function requestProposal(prompt: string, mode: 'generate' | 'retry' | 'revise') {
    const normalized = prompt.trim()
    if (!normalized || busy || confirming) {
      if (!normalized) setError('请先描述你想创建的指标')
      return false
    }
    if (normalized.length > 4000) {
      setError('需求与补充意见合计不能超过 4000 个字符，请精简后再提交')
      return false
    }
    setGenerationMode(mode)
    setBusy(true)
    setError('')
    if (mode === 'generate') setProposal(null)
    try {
      const result = await metricAIAPI.propose({ requirement: normalized })
      setProposal(result.proposal)
      setProposalOpen(true)
      setRequestID(result.requestId)
      setContextHash(result.retrievalContextHash)
      setActivePrompt(normalized)
      return true
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'AI 指标提案生成失败')
      return false
    } finally {
      setBusy(false)
    }
  }

  async function generateProposal() {
    const normalized = requirement.trim()
    setRequirement(normalized)
    await requestProposal(composedRequirement, 'generate')
  }

  async function retryProposal() {
    await requestProposal(activePrompt || requirement, 'retry')
  }

  async function reviseProposal() {
    const opinion = revision.trim()
    if (!opinion) {
      setError('请先填写希望 AI 调整的意见')
      return
    }
    const base = activePrompt || requirement.trim()
    if (opinion.length > Math.max(0, 4000 - base.length - '\n\n用户补充意见：'.length)) {
      setError('补充意见超过当前可用字数，请精简后再提交')
      return
    }
    const revisedPrompt = `${base}\n\n用户补充意见：${opinion}`
    if (await requestProposal(revisedPrompt, 'revise')) {
      setRevision('')
      setRevisionOpen(false)
    }
  }

  async function confirmProposal() {
    if (proposal?.strategy !== 'CREATE_ON_DATASET' || !proposal.candidateMetricDefinition || confirming || busy) return
    setConfirming(true)
    setError('')
    try {
      await onConfirm(proposal.candidateMetricDefinition)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'AI 生成的指标草稿创建失败')
    } finally {
      setConfirming(false)
    }
  }

  async function continueDatasetProposal() {
    if (!proposal || (proposal.strategy !== 'CREATE_DATASET' && proposal.strategy !== 'MODIFY_DATASET') || confirming || busy) return
    if (!proposal.datasetInstruction || (proposal.strategy === 'MODIFY_DATASET' && !proposal.targetDatasetId)) return
    setConfirming(true)
    setError('')
    try {
      const prompt = activePrompt || requirement.trim()
      const datasetInstruction = `${proposal.datasetInstruction}${metricAISafeExtensionBlock(extensionDataset)}`.trim()
      const hints = await buildMetricDatasetAIHints({
        proposal,
        instruction: `${datasetInstruction}\n${prompt}`,
        datasets,
        selectedDatasetIds: hintDatasetIds,
        selectedMeasureFieldKeys: hintMeasureFieldKeys,
        selectedDateFieldKey: hintDateFieldKey,
        selectedDimensionFieldKeys: hintDimensionFieldKeys,
        selectedAggregation: hintAggregation,
        visibleFields: hintFields,
        loadedSnapshots: hintSnapshots,
      })
      onModifyDataset(proposal.strategy, proposal.targetDatasetId, datasetInstruction, requirement.trim(), hints)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '准备数据集 AI 上下文失败，请重试')
      setConfirming(false)
    }
  }

  const locked = busy || confirming
  const datasetStrategy = proposal?.strategy === 'MODIFY_DATASET' || proposal?.strategy === 'CREATE_DATASET'
    ? proposal.strategy
    : null
  const summaryItems = proposal ? proposalSentences(proposal.summary) : []
  const instructionSteps = proposal ? proposalSentences(proposal.datasetInstruction) : []
  const revisionLimit = Math.max(0, 4000 - (activePrompt || requirement.trim()).length - '\n\n用户补充意见：'.length)
  return <section className="metric-ai-assistant" aria-label="AI 新建指标" aria-busy={locked}>
    <header>
      <div><span className="eyebrow">AI 主导创建</span><h2>告诉 AI 你想要什么指标</h2><p>名称、口径、来源、聚合、时间和维度都由 AI 尽量补全；你只需要审核生成结果。</p></div>
    </header>
    <div className="metric-ai-body">
      <form className="metric-ai-intent" onSubmit={event => { event.preventDefault(); void generateProposal() }}>
        {extensionDataset && <div className="metric-ai-extension-context" role="status"><GitBranchIcon size={18} weight="bold" /><div><strong>正在基于“{extensionDataset.name}”拓展指标</strong><span>当前发布 DAG 是只读基线；如需改造，只能追加能力并生成新版本，既有指标口径不会被覆盖。</span></div></div>}
        <section className="metric-ai-hints" aria-labelledby="metric-ai-hints-title">
          <header>
            <div>
              <span>可选</span>
              <h3 id="metric-ai-hints-title">给 AI 更多参考</h3>
              <p>选择已知条件可以让方案更贴近预期；全部留空也能生成，AI 会自动判断。</p>
            </div>
            <button className="quiet-button metric-ai-hints-clear" type="button" disabled={locked || (!hintDatasetIds.length && !hintMeasureFieldKeys.length && !hintDateFieldKey && !hintDimensionFieldKeys.length && !hintAggregation)} onClick={clearHints}>
              <EraserIcon size={16} aria-hidden="true" />
              清空条件
            </button>
          </header>
          <div className="metric-ai-hint-grid">
            <section className="metric-ai-hint-card metric-ai-hint-datasets" role="group" aria-labelledby="metric-ai-hint-datasets-title">
              <header className="metric-ai-hint-card-heading">
                <span className="metric-ai-hint-card-icon"><DatabaseIcon size={18} aria-hidden="true" /></span>
                <div><strong id="metric-ai-hint-datasets-title">优先参考的数据集</strong><small>只展示当前可用的精确发布版本</small></div>
                <em>可多选</em>
              </header>
              {datasets.length ? <div className="metric-ai-check-tags">{datasets.map(dataset => {
                const selected = hintDatasetIds.includes(dataset.id)
                const referenceLabel = metricAIDatasetReferenceLabel(dataset)
                const sourceLabel = metricAIDatasetSourceLabel(dataset)
                return <label key={dataset.id} className={`metric-ai-check-tag metric-ai-dataset-tag ${selected ? 'is-checked' : ''}`}>
                  <input aria-label={`优先使用数据集 ${referenceLabel}`} type="checkbox" checked={selected} disabled={locked} onChange={() => toggleHintDataset(dataset.id)} />
                  {selected ? <CheckSquareIcon size={17} weight="fill" aria-hidden="true" /> : <SquareIcon size={17} aria-hidden="true" />}
                  <span className="metric-ai-dataset-label"><strong>{dataset.name}</strong><small title={sourceLabel}>{sourceLabel}</small></span>
                </label>
              })}</div> : <div className="metric-ai-hint-empty">暂无已发布数据集，仍可直接描述需求让 AI 检索。</div>}
            </section>

            <section className="metric-ai-hint-card metric-ai-hint-aggregation" role="group" aria-labelledby="metric-ai-hint-aggregation-title">
              <header className="metric-ai-hint-card-heading">
                <span className="metric-ai-hint-card-icon"><CalculatorIcon size={18} aria-hidden="true" /></span>
                <div><strong id="metric-ai-hint-aggregation-title">统计口径 / 聚合</strong><small>先选计算方式，统计对象会随之调整</small></div>
                <em>可不限定</em>
              </header>
              <div className="metric-ai-segmented" role="radiogroup" aria-label="统计口径与聚合">
                {metricAIHintAggregationOptions.map(option => <label key={option.value || 'AUTO'} className={hintAggregation === option.value ? 'is-active' : ''} title={option.label}>
                  <input type="radio" name="metric-ai-hint-aggregation" value={option.value} aria-label={option.label} checked={hintAggregation === option.value} disabled={locked} onChange={() => updateHintAggregation(option.value)} />
                  <span>{option.shortLabel}</span>
                </label>)}
              </div>
              <small className="metric-ai-hint-help">切换聚合会自动移除不适用的已选统计对象。</small>
            </section>

            <section className="metric-ai-hint-card metric-ai-hint-measures" role="group" aria-labelledby="metric-ai-hint-measures-title">
              <header className="metric-ai-hint-card-heading">
                <span className="metric-ai-hint-card-icon"><FunctionIcon size={18} aria-hidden="true" /></span>
                <div><strong id="metric-ai-hint-measures-title">统计对象</strong><small className="metric-ai-hint-scope">{metricAIHintStatisticalFieldScope(hintAggregation)}</small></div>
                <em>可多选</em>
              </header>
              {hintMeasureFields.length ? <div className="metric-ai-check-tags">{hintMeasureFields.map(field => {
                const selected = hintMeasureFieldKeys.includes(field.key)
                return <label key={field.key} className={`metric-ai-check-tag ${selected ? 'is-checked' : ''}`}>
                  <input aria-label={`统计字段 ${field.datasetName} ${field.name}`} type="checkbox" checked={selected} disabled={locked || !hintDatasetIds.length || hintFieldsLoading} onChange={() => toggleHintMeasure(field.key)} />
                  {selected ? <CheckSquareIcon size={17} weight="fill" aria-hidden="true" /> : <SquareIcon size={17} aria-hidden="true" />}
                  <span><small>{field.datasetName}</small><strong>{field.name}</strong></span>
                </label>
              })}</div> : <div className={`metric-ai-hint-empty ${hintFieldsLoading ? 'is-loading' : ''}`} role={hintFieldsLoading ? 'status' : undefined}>{hintDatasetIds.length ? hintFieldsLoading ? '正在读取统计对象…' : '当前聚合方式下没有适用字段，仍可交给 AI 判断。' : '先选择数据集，再选择需要计算或计数的对象。'}</div>}
            </section>

            <section className="metric-ai-hint-card metric-ai-hint-date" role="group" aria-label="统计日期条件">
              <header className="metric-ai-hint-card-heading">
                <span className="metric-ai-hint-card-icon"><CalendarBlankIcon size={18} aria-hidden="true" /></span>
                <div><strong id="metric-ai-hint-date-title">统计日期字段</strong><small>决定指标按哪个业务时间归属</small></div>
                <em>可不限定</em>
              </header>
              <select className="metric-ai-hint-control" aria-label="统计日期字段" value={hintDateFieldKey} disabled={locked || !hintDatasetIds.length || hintFieldsLoading} onChange={event => updateHintDateField(event.target.value)}>
                <option value="">自动判断 / 不限定</option>
                {hintTimeFields.map(field => <option key={field.key} value={field.key}>{field.datasetName} · {field.name}（{field.code}）</option>)}
              </select>
              <small className="metric-ai-hint-help">{hintDatasetIds.length ? hintFieldsLoading ? '正在读取所选数据集字段…' : hintTimeFields.length ? '仅展示所选数据集中的日期字段。' : '所选数据集没有日期字段，仍可由 AI 补全。' : '选择数据集后可以指定日期字段。'}</small>
            </section>

            <section className="metric-ai-hint-card metric-ai-hint-dimensions" role="group" aria-labelledby="metric-ai-hint-dimensions-title">
              <header className="metric-ai-hint-card-heading">
                <span className="metric-ai-hint-card-icon"><StackIcon size={18} aria-hidden="true" /></span>
                <div><strong id="metric-ai-hint-dimensions-title">分析维度</strong><small>补充希望用于筛选、分组或下钻的字段</small></div>
                <em>可多选</em>
              </header>
              {hintDimensionFields.length ? <div className="metric-ai-check-tags">{hintDimensionFields.map(field => {
                const selected = hintDimensionFieldKeys.includes(field.key)
                return <label key={field.key} className={`metric-ai-check-tag ${selected ? 'is-checked' : ''}`}>
                  <input aria-label={`分析维度 ${field.datasetName} ${field.name}`} type="checkbox" checked={selected} disabled={locked || !hintDatasetIds.length || hintFieldsLoading} onChange={() => toggleHintDimension(field.key)} />
                  {selected ? <CheckSquareIcon size={17} weight="fill" aria-hidden="true" /> : <SquareIcon size={17} aria-hidden="true" />}
                  <span><small>{field.datasetName}</small><strong>{field.name}</strong></span>
                </label>
              })}</div> : <div className={`metric-ai-hint-empty ${hintFieldsLoading ? 'is-loading' : ''}`} role={hintFieldsLoading ? 'status' : undefined}>{hintDatasetIds.length ? hintFieldsLoading ? '正在读取分析维度…' : '所选数据集没有适用维度，仍可由 AI 补全。' : '先选择数据集，再选择分析维度。'}</div>}
            </section>
          </div>
          {hintFieldsError && <div className="metric-ai-hint-warning" role="status">{hintFieldsError}</div>}
          <footer aria-live="polite"><span>{composedRequirement.length > 4000 ? '合并内容超过接口限制，请精简需求或减少参考条件。' : hintDatasetIds.length || hintMeasureFieldKeys.length || hintDateFieldKey || hintDimensionFieldKeys.length || hintAggregation ? '已选择的条件会作为固定参考区块提交。' : '当前由 AI 自动判断全部条件。'}</span><strong className={composedRequirement.length > 4000 ? 'over-limit' : ''}>合并后 {composedRequirement.length} / 4000 字</strong></footer>
        </section>
        <section className="metric-ai-requirement">
          <label htmlFor="metric-ai-requirement">需求描述</label>
          <div className="metric-ai-request-row">
            <textarea id="metric-ai-requirement" aria-label="指标创建需求" maxLength={4000} value={requirement} disabled={locked} onChange={event => updateRequirement(event.target.value)} placeholder="例如：创建已支付销售额指标，统计已支付且未退款订单的含税金额，按支付完成月份汇总，并支持按地区查看。" />
            <button className="primary-button metric-ai-generate-button" type="submit" disabled={!requirement.trim() || composedRequirement.length > 4000 || locked}>{busy ? '正在生成…' : '生成指标配置'}</button>
          </div>
          <small>不需要填写配置项。描述业务目标即可，缺少的信息由 AI 从你有权限的数据资产中查找和补全。</small>
        </section>
      </form>
      {busy && <div className="metric-ai-progress" role="status"><strong>{generationMode === 'retry' ? '正在重新生成方案' : generationMode === 'revise' ? '正在根据意见调整方案' : '正在匹配内部语义资产'}</strong><span>AI 正在选择精确数据集版本并补全指标配置。</span></div>}
      {confirming && <div className="metric-ai-progress" role="status"><strong>正在创建指标草稿</strong><span>系统会再次校验 AI 引用的精确数据集版本。</span></div>}
      {error && <div className="metric-ai-error" role="alert">{error}</div>}
      {proposal && !proposalOpen && <div className="metric-ai-result-ready" role="status"><span>指标配置已经生成，可以随时打开查看和确认。</span><button className="quiet-button" type="button" onClick={() => setProposalOpen(true)}>查看生成结果</button></div>}
      {proposal && proposalOpen && <div className="metric-ai-proposal-overlay">
        <div className="metric-ai-proposal-dialog" role="dialog" aria-modal="true" aria-labelledby="metric-ai-proposal-title">
          <article className="metric-ai-proposal" aria-label="AI 指标审核提案">
        <header><div><span>{metricAIStrategyLabels[proposal.strategy]}</span><h3 id="metric-ai-proposal-title">{proposalHeading(proposal)}</h3><p>请审核下面的业务方案，技术主键已收起。</p></div><button className="metric-ai-proposal-close" type="button" autoFocus aria-label="关闭指标提案" onClick={() => setProposalOpen(false)}>×</button></header>
        {summaryItems.length > 0 && <section className="metric-ai-overview" aria-label="方案概览"><h4>方案概览</h4><ul>{summaryItems.map((item, index) => <li key={`${index}:${item}`}>{item}</li>)}</ul></section>}
        {proposal.strategy === 'CREATE_ON_DATASET' && proposal.candidateMetricDefinition && <section className="metric-ai-decision create">
          <ReadonlyMetricConfiguration definition={proposal.candidateMetricDefinition} />
        </section>}
        {datasetStrategy && <section className={`metric-ai-decision modify ${datasetStrategy === 'CREATE_DATASET' ? 'create-dataset' : ''}`} aria-label="数据集执行方案">
          <header><strong>{datasetStrategy === 'CREATE_DATASET' ? '新数据集构建计划' : '现有数据集调整计划'}</strong><small>{datasetStrategy === 'CREATE_DATASET' ? '不会修改作为来源的映射表数据集' : '只调整 AI 已确认可管理的普通数据集草稿'}</small></header>
          {instructionSteps.length > 0 ? <ol>{instructionSteps.map((step, index) => <li key={`${index}:${step}`}><span>{index + 1}</span><p>{step}</p></li>)}</ol> : <p>AI 尚未生成可执行的数据集步骤，请重新生成或提供修改意见。</p>}
          <div className="metric-ai-next-step"><strong>完成后</strong><span>保存并提交数据集发布审批；审批通过后返回资产管理中心，AI 将基于精确发布版本创建指标。</span></div>
        </section>}
        {proposal.strategy === 'REUSE_METRIC' && <section className="metric-ai-decision reuse"><strong>已有指标已经覆盖该口径</strong><p>建议直接复用已匹配的发布指标，本页不会重复创建同义指标。</p></section>}
        {proposal.strategy === 'DATA_GAP' && <section className="metric-ai-decision gap"><strong>当前不能安全创建</strong><p>授权目录中没有足以支撑该口径的数据，模型没有猜测字段或扩张检索范围。</p></section>}
        {proposal.strategy === 'NEEDS_CLARIFICATION' && <section className="metric-ai-decision clarify"><strong>还需要一点业务信息</strong><p>把下方问题的答案作为修改意见提交，AI 会基于原需求生成新方案。</p></section>}
        {(proposal.clarificationQuestions.length > 0 || proposal.assumptions.length > 0 || proposal.warnings.length > 0) && <section className="metric-ai-review-grid" aria-label="审核要点">
          {proposal.clarificationQuestions.length > 0 && <section className="question"><h4>需要你确认</h4><ul>{proposal.clarificationQuestions.map(item => <li key={item}>{readableProposalText(item)}</li>)}</ul></section>}
          {proposal.assumptions.length > 0 && <section><h4>AI 当前假设</h4><ul>{proposal.assumptions.map(item => <li key={item}>{readableProposalText(item)}</li>)}</ul></section>}
          {proposal.warnings.length > 0 && <section className="warning"><h4>风险提醒</h4><ul>{proposal.warnings.map(item => <li key={item}>{readableProposalText(item)}</li>)}</ul></section>}
        </section>}
        {proposal.retrievalEvidence.length > 0 && <details className="metric-ai-evidence"><summary>查看 AI 采用的业务依据（{proposal.retrievalEvidence.length} 项）</summary><ul>{proposal.retrievalEvidence.map((item, index) => <li key={`${item.sourceType}:${item.sourceId}:${index}`}><strong>{evidenceLabels[item.sourceType]}</strong><span>{readableProposalText(item.reason)}</span></li>)}</ul></details>}
        {revisionOpen && <section className="metric-ai-revision" aria-label="根据意见修改方案"><label>告诉 AI 需要怎么调整<textarea autoFocus aria-label="方案修改意见" maxLength={revisionLimit} value={revision} disabled={locked || revisionLimit === 0} onChange={event => setRevision(event.target.value)} placeholder={revisionLimit ? '例如：销售额应使用含税金额；区域取客户当前所属区域；退款在原订单月份冲减。' : '当前需求已达到 4000 字限制，请精简上方需求后再修改方案。'} /></label><small>{revisionLimit ? `你的意见会和原需求一起提交，还可输入 ${revisionLimit - revision.length} 个字符。` : '当前需求已无可用字数，请精简上方需求后再生成。'}</small><div><button className="quiet-button" type="button" disabled={locked} onClick={() => { setRevisionOpen(false); setRevision(''); setError('') }}>取消</button><button className="primary-button" type="button" disabled={locked || !revision.trim() || revisionLimit === 0} onClick={() => void reviseProposal()}>{busy && generationMode === 'revise' ? '正在修改…' : '生成修改方案'}</button></div></section>}
        <details className="metric-ai-technical"><summary>技术信息</summary><dl><div><dt>请求追踪</dt><dd><code>{requestID || '—'}</code></dd></div><div><dt>授权上下文</dt><dd><code>{contextHash || '—'}</code></dd></div>{proposal.targetDatasetId && <div><dt>目标数据集</dt><dd><code>{proposal.targetDatasetId}</code></dd></div>}{proposal.targetDatasetVersionId && <div><dt>数据集版本</dt><dd><code>{proposal.targetDatasetVersionId}</code></dd></div>}{proposal.reuseMetricVersionId && <div><dt>复用指标版本</dt><dd><code>{proposal.reuseMetricVersionId}</code></dd></div>}</dl></details>
        <footer className="metric-ai-actions">
          <div><button className="quiet-button" type="button" disabled={locked} onClick={() => void retryProposal()}>{busy && generationMode === 'retry' ? '正在重新生成…' : '重新生成方案'}</button><button className="quiet-button" type="button" disabled={locked} aria-expanded={revisionOpen} onClick={() => { setRevisionOpen(value => !value); setError('') }}>根据意见修改方案</button></div>
          {proposal.strategy === 'CREATE_ON_DATASET' && proposal.candidateMetricDefinition && <button className="primary-button" type="button" disabled={locked} onClick={() => void confirmProposal()}>{confirming ? '正在创建草稿…' : '确认方案并创建草稿'}</button>}
          {datasetStrategy && <button className="primary-button" type="button" disabled={locked || !proposal.datasetInstruction || (datasetStrategy === 'MODIFY_DATASET' && !proposal.targetDatasetId)} onClick={() => void continueDatasetProposal()}>{confirming ? '正在准备数据集…' : datasetStrategy === 'CREATE_DATASET' ? '确认方案并新建数据集' : '确认方案并继续改造'}</button>}
        </footer>
          </article>
        </div>
      </div>}
    </div>
  </section>
}

function ReadonlyMetricConfiguration({ definition }: { definition: MetricDefinition }) {
  const metricTypeLabels: Record<MetricDefinition['metric']['type'], string> = { ATOMIC: '原子指标', DERIVED: '派生指标', RATIO: '比率指标' }
  const aggregationLabels: Record<MetricDefinition['aggregation'], string> = { NONE: '无需聚合', SUM: '求和', AVG: '平均值', MIN: '最小值', MAX: '最大值', COUNT: '计数', COUNT_DISTINCT: '去重计数' }
  const additivityLabels: Record<MetricDefinition['additivity'], string> = { ADDITIVE: '可累加', SEMI_ADDITIVE: '部分可累加', NON_ADDITIVE: '不可累加' }
  const timeGrainLabels: Record<MetricDefinition['timeGrain'], string> = { NONE: '不限定', DAY: '按日', WEEK: '按周', MONTH: '按月', QUARTER: '按季度', YEAR: '按年' }
  return <section className="metric-ai-config" aria-label="AI 生成配置预览">
    <header><div><span className="eyebrow">AI 生成配置</span><h4>{definition.metric.name}</h4></div><span className="metric-ai-code">指标编码 · {definition.metric.code}</span></header>
    <p className="metric-ai-config-description">{definition.metric.description || '未生成说明'}</p>
    <dl>
      <div className="wide"><dt>数据来源</dt><dd>已匹配并锁定精确发布版本</dd></div>
      <div><dt>指标类型</dt><dd>{metricTypeLabels[definition.metric.type]}</dd></div>
      <div><dt>计算字段</dt><dd>{definition.expression.type === 'FIELD_REF' ? '已匹配原子字段' : '已生成计算表达式'}</dd></div>
      <div><dt>聚合方式</dt><dd>{aggregationLabels[definition.aggregation]}</dd></div>
      <div><dt>单位</dt><dd>{definition.unit || '未设置'}</dd></div>
      <div><dt>数字格式</dt><dd>{definition.numberFormat || '未设置'}</dd></div>
      <div><dt>小数位数</dt><dd>{definition.decimalScale}</dd></div>
      <div><dt>可加性</dt><dd>{additivityLabels[definition.additivity]}</dd></div>
      <div><dt>时间口径</dt><dd>{definition.timeFieldId ? `已匹配时间字段 · ${timeGrainLabels[definition.timeGrain]}` : '未设置'}</dd></div>
      <div className="wide"><dt>不可加维度</dt><dd>{definition.nonAdditiveDimensionFieldIds.length ? `已设置 ${definition.nonAdditiveDimensionFieldIds.length} 个` : '无'}</dd></div>
    </dl>
    <section className="metric-ai-config-dimensions" aria-label="AI 生成允许维度">
      <h5>允许维度</h5>
      {definition.allowedDimensions.length ? <ul>{definition.allowedDimensions.map(dimension => <li key={dimension.fieldId}><strong>{dimension.name}</strong><span>{dimension.hierarchyFieldIds.length > 1 ? `${dimension.hierarchyFieldIds.length} 级层级 · ` : ''}{dimension.sortDirection === 'ASC' ? '升序' : '降序'} · 空值显示为“{dimension.nullLabel}”</span></li>)}</ul> : <p>无</p>}
    </section>
    <details className="metric-ai-config-technical"><summary>查看高级计算设置与完整配置</summary><div className="metric-ai-config-defaults" aria-label="AI 生成默认语义"><span>舍入<strong>{definition.roundingMode}</strong></span><span>空值<strong>{definition.nullHandling}</strong></span><span>除零<strong>{definition.divisionByZero}</strong></span></div><MetricDefinitionJSON definition={definition} label="AI 生成指标完整定义 JSON" /></details>
  </section>
}

function MetricDefinitionJSON({ definition, label }: { definition: MetricDefinition; label: string }) {
  return <details className="metric-definition-json">
    <summary>查看完整定义 JSON</summary>
    <pre aria-label={label}>{JSON.stringify(definition, null, 2)}</pre>
  </details>
}

function MetricVersionManager({
  versions, total, currentVersionId, selected, usage, parameters, parameterValues, preview,
  datasetUnavailable, datasetReady, loading, busy, writesLocked, canPublish, onSelect, onLoadMore, onParameterChange, onPreview, onDeprecate,
}: {
  versions: MetricVersionSummary[]
  total: number
  currentVersionId: string
  selected: MetricVersionRecord | null
  usage: MetricUsage
  parameters: ParameterOption[]
  datasetUnavailable: boolean
  datasetReady: boolean
  parameterValues: Record<string, string>
  preview: DatasetPreview | null
  loading: boolean
  busy: boolean
  writesLocked: boolean
  canPublish: boolean
  onSelect: (versionId: string) => void
  onLoadMore: () => void
  onParameterChange: (code: string, value: string) => void
  onPreview: () => void
  onDeprecate: () => void
}) {
  const previewDisabled = busy || loading || writesLocked || !datasetReady || selected?.status !== 'PUBLISHED'
  return <section className="metric-version-manager" aria-label="指标发布版本管理">
    <header><div><span className="eyebrow">不可变口径</span><h2>发布版本</h2></div>{writesLocked && <small>聚合状态尚未完成对账，当前仅允许查看。</small>}</header>
    <nav aria-label="指标版本列表">
      {!versions.length && !loading && <span className="metric-empty">暂无发布版本</span>}
      {versions.map(version => <button key={version.id} type="button" className={selected?.id === version.id ? 'selected' : ''} disabled={loading || busy} onClick={() => onSelect(version.id)}>
        <strong>V{version.versionNo}</strong><span>{version.status}</span>{version.id === currentVersionId && <em>当前发布</em>}
      </button>)}
      {versions.length < total && <button className="load-more-button" type="button" onClick={onLoadMore}>加载更多（{versions.length}/{total}）</button>}
    </nav>
    <div className="metric-version-detail">
      {loading && !selected ? <p className="metric-empty">正在加载精确版本…</p> : selected ? <>
        <header><div><strong>V{selected.versionNo}</strong><span className={`version-status status-${selected.status.toLowerCase()}`}>{selected.status}</span>{selected.id === currentVersionId && <span className="current-version">当前发布版本</span>}</div><small>{new Date(selected.publishedAt).toLocaleString('zh-CN')}</small></header>
        <dl><div><dt>精确指标版本 ID</dt><dd>{selected.id}</dd></div><div><dt>数据集版本 ID</dt><dd>{selected.datasetVersionId}</dd></div><div><dt>定义摘要</dt><dd>{selected.definitionHash.slice(0, 16)}</dd></div><div><dt>表达式</dt><dd className="metric-expression-json"><code>{JSON.stringify(selected.definition.expression)}</code></dd></div><div><dt>聚合 / 可加性</dt><dd>{selected.definition.aggregation} / {selected.definition.additivity}</dd></div><div><dt>时间口径</dt><dd>{selected.definition.timeFieldId || '未设置'} / {selected.definition.timeGrain}</dd></div><div><dt>允许维度</dt><dd>{selected.definition.allowedDimensions.length ? selected.definition.allowedDimensions.map(item => `${item.name}（${item.fieldId}）`).join('、') : '无'}</dd></div></dl>
        <MetricDefinitionJSON definition={selected.definition} label="指标版本完整定义 JSON" />
        <section className="metric-usage" aria-label="指标版本使用汇总"><span>报告草稿引用<strong>{usage.reportDraftReferences}</strong></span><span>下游草稿引用<strong>{usage.downstreamDraftReferences}</strong></span><span>下游发布引用<strong>{usage.downstreamPublishedReferences}</strong></span><span>运行中查询<strong>{usage.activeQueryRuns}</strong></span></section>
        {datasetUnavailable && <p className="metric-empty">当前无法读取精确数据集版本；指标口径和占用仍可查看，参数与试算已禁用。</p>}
        {parameters.length > 0 && <section className="metric-version-parameters">{parameters.map(parameter => <label key={parameter.code}>{parameter.name || parameter.code}<input aria-label={`指标版本参数 ${parameter.code}`} type={parameter.dataType === 'DATE' ? 'date' : 'text'} value={parameterValues[parameter.code] ?? ''} disabled={previewDisabled} onChange={event => onParameterChange(parameter.code, event.target.value)} /></label>)}</section>}
        <div className="metric-version-actions"><button className="quiet-button" type="button" disabled={previewDisabled} onClick={onPreview}>试算精确版本</button>{selected.status === 'PUBLISHED' && <button className="danger-button" type="button" disabled={busy || loading || writesLocked || !canPublish} onClick={onDeprecate}>废弃版本</button>}{selected.status === 'DEPRECATED' && <small>废弃版本仅供审计查看，不能再次试算或恢复。</small>}</div>
        {preview && <PreviewTable preview={preview} />}
      </> : <p className="metric-empty">选择一个发布版本查看精确口径。</p>}
    </div>
  </section>
}
