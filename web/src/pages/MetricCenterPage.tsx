import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
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
  type MetricSummary,
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
  return valueAsList(version?.dsl.fields).map(valueAsRecord).map(field => ({
    id: valueAsText(field.id),
    code: valueAsText(field.code),
    name: valueAsText(field.name) || valueAsText(field.code),
    role: valueAsText(field.role),
    canonicalType: valueAsText(field.canonicalType),
    visible: field.visible !== false,
  })).filter(field => field.id && field.visible)
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

/** 指标中心只提供原子字段引用编辑，所有业务计算统一交由服务端验证与试算。 */
export function MetricCenterPage() {
  const { metricId } = useParams()
  const navigate = useNavigate()
  const routeMetricId = metricId ?? ''
  const isNew = !routeMetricId
  const [definition, setDefinition] = useState<MetricDefinition>(emptyDefinition)
  const [record, setRecord] = useState<MetricRecord | null>(null)
  const [savedFingerprint, setSavedFingerprint] = useState('')
  const [metrics, setMetrics] = useState<MetricSummary[]>([])
  const [metricsTotal, setMetricsTotal] = useState(0)
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
  const [catalogRefreshKey, setCatalogRefreshKey] = useState(0)
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
  const numericFields = useMemo(() => fields.filter(field => field.canonicalType === 'INTEGER' || field.canonicalType === 'DECIMAL'), [fields])
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
    metricAPI.list(pageSize, 0).then(page => {
      if (!active) return
      setMetrics(page.items)
      setMetricsTotal(page.total)
    }).catch(cause => {
      if (active) setError(cause instanceof Error ? `加载指标目录失败：${cause.message}` : '加载指标目录失败')
    })
    return () => { active = false }
  }, [catalogRefreshKey])

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
      // 指标只读权限不隐含数据集目录权限；目录故障只影响编辑能力，不抹掉指标自身信息。
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
      setCatalogRefreshKey(value => value + 1)
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
      setCatalogRefreshKey(value => value + 1)
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
        setCatalogRefreshKey(value => value + 1)
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

  async function loadMoreMetrics() {
    try {
      const page = await metricAPI.list(pageSize, metrics.length)
      setMetrics(current => [...current, ...page.items.filter(item => !current.some(existing => existing.id === item.id))])
      setMetricsTotal(page.total)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '加载更多指标失败')
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

  const title = record?.name || definition.metric.name || '指标中心'
  const actions = <>
    {writesLocked && <span className="metric-lock-state">等待对账</span>}
    <button className="quiet-button" type="button" onClick={() => navigate('/metrics')}>新建指标</button>
    <button className="quiet-button" type="button" disabled={!record || !selectedDatasetVersion || dirty || busy || writesLocked || !capabilities.read} onClick={previewDraft}>试算</button>
    <button className="primary-button" type="button" disabled={editorDisabled || Boolean(record && !dirty)} onClick={saveDraft}>{record ? '保存草稿' : '创建草稿'}</button>
    {publishOutcomeUnknown
      ? <button className="primary-button" type="button" disabled={busy || datasetSnapshotUnavailable} onClick={() => publication.current && void submitPublication(publication.current)}>重试刚才发布</button>
      : <button className="quiet-button" type="button" disabled={!record || !selectedDatasetVersion || dirty || busy || writesLocked || !capabilities.publish} onClick={publishDraft}>发布指标</button>}
    {reconciliationRequired && <button className="quiet-button" type="button" onClick={() => setReloadKey(value => value + 1)}>重新加载指标</button>}
  </>

  return <AppShell title={title} eyebrow="指标中心" actions={actions}>
    <div className="metric-feedback" aria-live="polite">
      {error && <span className="designer-error">{error}</span>}
      {message && <span className="designer-success">{message}</span>}
      <small>指标只引用精确发布版本；试算、空值和除零语义由服务端执行，跨引擎精确舍入仍按待办边界失败关闭或留待后续统一。</small>
    </div>
    <div className="metric-center-layout">
      <aside className="metric-catalog">
        <header><span className="eyebrow">租户目录</span><h2>指标</h2></header>
        <button className={!routeMetricId ? 'selected' : ''} type="button" onClick={() => navigate('/metrics')}><strong>＋ 新建原子指标</strong><small>选择字段并固定数据集版本</small></button>
        {metrics.map(metric => <button className={metric.id === routeMetricId ? 'selected' : ''} type="button" key={metric.id} onClick={() => navigate(`/metrics/${metric.id}/edit`)}>
          <strong>{metric.name}</strong><small>{metric.code} · {metric.status}</small>
        </button>)}
        {metrics.length < metricsTotal && <button className="load-more-button" type="button" onClick={loadMoreMetrics}>加载更多（{metrics.length}/{metricsTotal}）</button>}
      </aside>

      <main className="metric-editor">
        {pageLoading ? <section className="metric-panel" role="status">正在加载指标定义…</section> : <>
          {!permissionsReady || (!isNew && !capabilities.read) ? <section className="metric-panel">当前账号没有读取该指标的权限。</section> : <fieldset className="metric-definition" disabled={editorDisabled}>
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
              {advancedExpression ? <div className="advanced-expression"><strong>高级表达式只读</strong><p>派生、比率和指标引用的可视化编辑器将在后续增强；本页面不会执行或改写该表达式。</p><pre>{JSON.stringify(definition.expression, null, 2)}</pre></div> : <label>原子字段<select aria-label="原子指标字段" value={atomicFieldId} onChange={event => updateDefinition({ expression: { type: 'FIELD_REF', fieldId: event.target.value } })}><option value="">请选择数值字段</option>{numericFields.map(field => <option key={field.id} value={field.id}>{field.name} · {field.role} · {field.canonicalType}</option>)}</select></label>}
              <label>聚合<select aria-label="指标聚合" value={definition.aggregation} onChange={event => updateDefinition({ aggregation: event.target.value as MetricDefinition['aggregation'] })}>{['NONE', 'SUM', 'AVG', 'MIN', 'MAX', 'COUNT', 'COUNT_DISTINCT'].map(value => <option key={value}>{value}</option>)}</select></label>
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
