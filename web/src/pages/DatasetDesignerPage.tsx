import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import { buildDatasetDSL, buildPreviewParameters, createDatasetPublishIdempotencyKey, datasetAPI, type AssetColumn, type AssetTable, type CalculatedField, type DatasetDraft, type DatasetPreview, type DatasetRecord, type DesignerNode, type FieldOption, type FilterOption, type JoinOption, type ParameterOption, type PublishedVersionRecord, type PublishedVersionSummary, type PublishDatasetInput, type VersionUsage } from '../lib/datasets'

const emptyDraft = (): DatasetDraft => ({ code: '', name: '', description: '', nodes: [], fields: [], joins: [], filters: [], parameters: [], calculations: [], sorts: [], grainDescription: '', grainKeys: [] })
const text = (value: unknown) => typeof value === 'string' ? value : ''
const object = (value: unknown) => (value && typeof value === 'object' ? value as Record<string, unknown> : {})
const list = (value: unknown) => Array.isArray(value) ? value : []
const draftFingerprint = (draft: DatasetDraft) => {
  try { return JSON.stringify(buildDatasetDSL(draft)) } catch { return '' }
}
const emptyVersionUsage = (): VersionUsage => ({ reportDraftReferences: 0, downstreamDraftReferences: 0, downstreamPublishedReferences: 0, activeQueryRuns: 0 })
const versionParameters = (version: PublishedVersionRecord | null): ParameterOption[] => list(version?.dsl.parameters).map(object).map(raw => ({
  code: text(raw.code), name: text(raw.name), dataType: text(raw.dataType), required: Boolean(raw.required), multiValue: Boolean(raw.multiValue),
})).filter(parameter => parameter.code)
type DatasetCapabilities = { read: boolean; manage: boolean; publish: boolean }
// 发布请求发出后的未知异常（包括成功响应体被截断）都可能发生在服务端提交之后；
// 只有收到明确的 HTTP 4xx 才能安全结束原幂等候选。
const isAmbiguousPublishFailure = (cause: unknown) => !(cause instanceof RequestError) || cause.status >= 500
const httpStatusUnauthorized = 401
const httpStatusForbidden = 403
const httpStatusNotFound = 404
const httpStatusConflict = 409
const requiresPublishReconciliation = (cause: unknown) => cause instanceof RequestError &&
  [httpStatusUnauthorized, httpStatusForbidden, httpStatusNotFound, httpStatusConflict].includes(cause.status)

type PendingDatasetPublication = {
  datasetId: string
  fingerprint: string
  idempotencyKey: string
  input: PublishDatasetInput
}

/**
 * 把服务端规范 DSL 还原为可继续编辑的表单状态。
 * 物理列不直接信任 DSL 快照，而是按 tableId 重新读取资产中心；资产已失效时中止
 * 恢复，避免用户在陈旧字段集合上继续保存并覆盖有效草稿。
 */
async function hydrateDraft(record: DatasetRecord, tables: AssetTable[]): Promise<DatasetDraft> {
  const dsl = record.dsl
  const nodeValues = list(dsl.nodes).map(object)
  const nodes = await Promise.all(nodeValues.map(async (value, index): Promise<DesignerNode> => {
    const table = tables.find(item => item.id === text(value.tableId))
    if (!table) throw new Error(`第 ${index + 1} 个节点引用的表资产已不可用`)
    const columns = (await datasetAPI.columns(table.id)).items
    return { id: text(value.id), alias: text(value.alias), table, columns, selected: list(value.projection).map(text) }
  }))
  const idToCode = new Map<string, string>()
  const fields: FieldOption[] = []
  const calculations: CalculatedField[] = []
  for (const raw of list(dsl.fields).map(object)) {
    idToCode.set(text(raw.id), text(raw.code))
    const expression = object(raw.expression)
    if (text(expression.type) === 'AGGREGATE') {
      const source = object(expression.argument)
      fields.push({ key: `${text(source.nodeId)}.${text(source.field)}`, role: 'MEASURE', aggregation: text(expression.function) })
    } else if (text(expression.type) === 'FIELD_REF') {
      fields.push({ key: `${text(expression.nodeId)}.${text(expression.field)}`, role: text(raw.role), aggregation: '' })
    } else {
      const argumentsValue = list(expression.arguments).map(object)
      calculations.push({ id: text(raw.id), code: text(raw.code), name: text(raw.name), operation: text(expression.type), leftKey: `${text(argumentsValue[0]?.nodeId)}.${text(argumentsValue[0]?.field)}`, rightKey: `${text(argumentsValue[1]?.nodeId)}.${text(argumentsValue[1]?.field)}`, canonicalType: text(raw.canonicalType) })
    }
  }
  const joins: JoinOption[] = list(dsl.joins).map(object).map(raw => {
    const condition = object(list(raw.conditions)[0])
    return { id: text(raw.id), leftNodeId: text(raw.leftNodeId), rightNodeId: text(raw.rightNodeId), leftField: text(object(condition.leftExpression).field), rightField: text(object(condition.rightExpression).field), joinType: text(raw.joinType), cardinality: text(raw.cardinality), manualConfirmed: Boolean(raw.manualConfirmed) }
  })
  const filters: FilterOption[] = list(dsl.filters).map(object).map(raw => {
    const expression = object(raw.expression), left = object(expression.left), right = object(expression.right)
    return { id: text(raw.id), nodeId: text(left.nodeId), field: text(left.field), operator: text(expression.type), value: text(right.value), parameterCode: text(right.code) }
  })
  const parameters: ParameterOption[] = list(dsl.parameters).map(object).map(raw => ({ code: text(raw.code), name: text(raw.name), dataType: text(raw.dataType), required: Boolean(raw.required), multiValue: Boolean(raw.multiValue) }))
  const grain = object(dsl.outputGrain)
  return {
    code: record.code, name: record.name, description: record.description, nodes, fields, joins, filters, parameters, calculations,
    sorts: list(dsl.sorts).map(object).map(raw => ({ fieldId: idToCode.get(text(raw.fieldId)) ?? text(raw.fieldId), direction: text(raw.direction) })),
    grainDescription: text(grain.description), grainKeys: list(grain.keyFields).map(text),
  }
}

/** 提供单源与跨源数据集的可视化建模、校验、保存和重新加载能力。 */
export function DatasetDesignerPage() {
  const { datasetId } = useParams()
  const navigate = useNavigate()
  const [tables, setTables] = useState<AssetTable[]>([])
  const [draft, setDraft] = useState<DatasetDraft>(emptyDraft)
  const [version, setVersion] = useState(0)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [previewValues, setPreviewValues] = useState<Record<string, string>>({})
  const [preview, setPreview] = useState<DatasetPreview | null>(null)
  const [activeQuery, setActiveQuery] = useState<{ datasetId: string; queryId: string } | null>(null)
  const [persistedRecord, setPersistedRecord] = useState<DatasetRecord | null>(null)
  const [savedDraftFingerprint, setSavedDraftFingerprint] = useState('')
  const [publishOutcomeUnknown, setPublishOutcomeUnknown] = useState(false)
  const [reconciliationRequired, setReconciliationRequired] = useState(false)
  const [capabilities, setCapabilities] = useState<DatasetCapabilities>({ read: false, manage: false, publish: false })
  const [permissionsReady, setPermissionsReady] = useState(false)
  const [capabilitiesFor, setCapabilitiesFor] = useState('')
  const [versions, setVersions] = useState<PublishedVersionSummary[]>([])
  const [selectedVersion, setSelectedVersion] = useState<PublishedVersionRecord | null>(null)
  const [selectedVersionUsage, setSelectedVersionUsage] = useState<VersionUsage>(emptyVersionUsage)
  const [versionPreviewValues, setVersionPreviewValues] = useState<Record<string, string>>({})
  const [versionPreview, setVersionPreview] = useState<DatasetPreview | null>(null)
  const [versionLoading, setVersionLoading] = useState(false)
  const [versionBusy, setVersionBusy] = useState(false)
  const [versionRefreshKey, setVersionRefreshKey] = useState(0)
  const cancelRequested = useRef(false)
  const publishAttempt = useRef<PendingDatasetPublication | null>(null)
  const versionSelectionRequest = useRef(0)
  const selectedVersionID = useRef('')
  const routeDatasetID = useRef(datasetId)
  const permissionObjectID = datasetId && datasetId !== 'new' ? datasetId : ''
  const permissionsResolved = permissionsReady && capabilitiesFor === permissionObjectID

  useEffect(() => {
    // 异步加载可能晚于路由切换完成；active 标记阻止旧页面请求覆盖新页面状态。
    let active = true
    routeDatasetID.current = datasetId
    publishAttempt.current = null
    versionSelectionRequest.current += 1
    selectedVersionID.current = ''
    queueMicrotask(() => {
      if (!active) return
      setDraft(emptyDraft())
      setVersion(0)
      setPersistedRecord(null)
      setSavedDraftFingerprint('')
      setPreview(null)
      setVersions([])
      setSelectedVersion(null)
      setSelectedVersionUsage(emptyVersionUsage())
      setVersionPreview(null)
      setVersionPreviewValues({})
      setVersionLoading(false)
      setVersionBusy(false)
      setActiveQuery(null)
      setCapabilities({ read: false, manage: false, publish: false })
      setPermissionsReady(false)
      setCapabilitiesFor('')
      setError('')
      setMessage('')
    })
    Promise.all([
      datasetAPI.evaluatePermission(permissionObjectID, 'READ'),
      datasetAPI.evaluatePermission(permissionObjectID, 'MANAGE'),
      datasetAPI.evaluatePermission(permissionObjectID, 'PUBLISH'),
    ]).then(([read, manage, publish]) => {
      if (!active) return
      setCapabilities({ read: read.allowed, manage: manage.allowed, publish: publish.allowed })
      setCapabilitiesFor(permissionObjectID)
      setPermissionsReady(true)
    }).catch(cause => {
      if (!active) return
      // 权限探测失败时前端保持最小权限；真正的授权边界仍由每个后端接口复核。
      setCapabilitiesFor(permissionObjectID)
      setPermissionsReady(true)
      setError(cause instanceof Error ? `权限检查失败：${cause.message}` : '权限检查失败')
    })
    datasetAPI.tables().then(async response => {
      if (!active) return
      setPublishOutcomeUnknown(false)
      setReconciliationRequired(false)
      setTables(response.items)
      if (datasetId && datasetId !== 'new') {
        const record = await datasetAPI.get(datasetId)
        const restored = await hydrateDraft(record, response.items)
        if (active) {
          setDraft(restored)
          setVersion(record.version)
          setPersistedRecord(record)
          setSavedDraftFingerprint(draftFingerprint(restored))
        }
      }
    }).catch(cause => { if (active) setError(cause instanceof Error ? cause.message : '加载设计器失败') })
    return () => { active = false }
  }, [datasetId, permissionObjectID])

  useEffect(() => {
    if (!datasetId || datasetId === 'new' || !permissionsResolved || !capabilities.read) return
    let active = true
    const requestedDatasetID = datasetId
    const selectionRequest = ++versionSelectionRequest.current
    queueMicrotask(() => { if (active) setVersionLoading(true) })
    datasetAPI.listVersions(requestedDatasetID).then(async page => {
      if (!active || routeDatasetID.current !== requestedDatasetID || selectionRequest !== versionSelectionRequest.current) return
      setVersions(page.items)
      const preferredID = page.items.some(item => item.id === selectedVersionID.current)
        ? selectedVersionID.current
        : page.items.find(item => item.id === persistedRecord?.currentPublishedVersionId)?.id ?? page.items[0]?.id ?? ''
      if (!preferredID) {
        selectedVersionID.current = ''
        setSelectedVersion(null)
        setSelectedVersionUsage(emptyVersionUsage())
        return
      }
      selectedVersionID.current = preferredID
      const [detail, usage] = await Promise.all([
        datasetAPI.getVersion(requestedDatasetID, preferredID),
        datasetAPI.getVersionUsage(requestedDatasetID, preferredID),
      ])
      if (!active || routeDatasetID.current !== requestedDatasetID || selectionRequest !== versionSelectionRequest.current) return
      setSelectedVersion(detail)
      setSelectedVersionUsage(usage)
      setVersionPreviewValues({})
      setVersionPreview(null)
    }).catch(cause => {
      if (active && routeDatasetID.current === requestedDatasetID && selectionRequest === versionSelectionRequest.current) {
        setError(cause instanceof Error ? cause.message : '加载发布版本失败')
      }
    }).finally(() => {
      if (active && routeDatasetID.current === requestedDatasetID && selectionRequest === versionSelectionRequest.current) setVersionLoading(false)
    })
    return () => { active = false }
  }, [datasetId, permissionsResolved, capabilities.read, versionRefreshKey, persistedRecord?.currentPublishedVersionId])

  // 这是表单控件共用的派生索引，不单独保存；节点选择变化后统一重算可避免字段、
  // 排序和粒度选项持有不同步的副本。
  const selectedFields = useMemo(() => draft.nodes.flatMap(node => node.columns.filter(column => node.selected.includes(column.columnName)).map(column => ({ key: `${node.id}.${column.columnName}`, code: draft.nodes.length > 1 ? `${node.alias}_${column.columnName}` : column.columnName, label: `${node.alias}.${column.businessName || column.columnName}` }))), [draft.nodes])
  const selectedVersionParameters = useMemo(() => versionParameters(selectedVersion), [selectedVersion])
  const currentDraftFingerprint = useMemo(() => draftFingerprint(draft), [draft])

  const selectPublishedVersion = async (versionID: string) => {
    if (!datasetId || datasetId === 'new' || !capabilities.read) return
    const requestedDatasetID = datasetId
    const selectionRequest = ++versionSelectionRequest.current
    selectedVersionID.current = versionID
    setVersionLoading(true)
    setVersionPreview(null)
    setVersionPreviewValues({})
    setError('')
    try {
      const [detail, usage] = await Promise.all([
        datasetAPI.getVersion(requestedDatasetID, versionID),
        datasetAPI.getVersionUsage(requestedDatasetID, versionID),
      ])
      // 路由或选择变化后丢弃旧响应，防止旧版本详情覆盖用户刚选择的新版本。
      if (routeDatasetID.current !== requestedDatasetID || selectionRequest !== versionSelectionRequest.current) return
      setSelectedVersion(detail)
      setSelectedVersionUsage(usage)
    } catch (cause) {
      if (routeDatasetID.current === requestedDatasetID && selectionRequest === versionSelectionRequest.current) {
        setError(cause instanceof Error ? cause.message : '加载发布版本失败')
      }
    } finally {
      if (routeDatasetID.current === requestedDatasetID && selectionRequest === versionSelectionRequest.current) setVersionLoading(false)
    }
  }

  const previewPublishedVersion = async () => {
    if (!datasetId || datasetId === 'new' || !selectedVersion || !capabilities.read) return
    const requestedDatasetID = datasetId
    const requestedVersionID = selectedVersion.id
    const selectionRequest = versionSelectionRequest.current
    setVersionBusy(true); setError(''); setMessage(''); setVersionPreview(null); cancelRequested.current = false
    try {
      // 精确版本预览只读取不可变快照里的参数定义，不复用当前可编辑草稿参数。
      const parameters = buildPreviewParameters(selectedVersionParameters, versionPreviewValues)
      const queryId = crypto.randomUUID()
      setActiveQuery({ datasetId: requestedDatasetID, queryId })
      const result = await datasetAPI.previewVersion(requestedDatasetID, requestedVersionID, queryId, parameters)
      if (routeDatasetID.current !== requestedDatasetID || selectedVersionID.current !== requestedVersionID || selectionRequest !== versionSelectionRequest.current) return
      setVersionPreview(result)
      setMessage(`精确版本 V${selectedVersion.versionNo} 预览完成 · ${result.rowCount} 行`)
    } catch (cause) {
      if (routeDatasetID.current === requestedDatasetID && selectedVersionID.current === requestedVersionID && selectionRequest === versionSelectionRequest.current) {
        if (cancelRequested.current) setMessage('查询已取消')
        else setError(cause instanceof Error ? cause.message : '精确版本预览失败')
      }
    } finally {
      if (routeDatasetID.current === requestedDatasetID && selectedVersionID.current === requestedVersionID && selectionRequest === versionSelectionRequest.current) {
        setActiveQuery(null)
        setVersionBusy(false)
      }
    }
  }

  const transitionPublishedVersion = async (targetStatus: 'STALE' | 'DEPRECATED') => {
    if (!datasetId || datasetId === 'new' || !selectedVersion || !persistedRecord || !capabilities.publish) return
    const requestedDatasetID = datasetId
    const requestedVersionID = selectedVersion.id
    const baseline = persistedRecord
    setVersionBusy(true); setError(''); setMessage('')
    let changed: PublishedVersionRecord
    try {
      changed = await datasetAPI.transitionVersion(requestedDatasetID, requestedVersionID, {
        expectedVersion: baseline.version,
        expectedStatus: selectedVersion.status,
        targetStatus,
      })
    } catch (cause) {
      if (routeDatasetID.current === requestedDatasetID) {
        setError(cause instanceof Error ? cause.message : '更新版本状态失败')
        setVersionBusy(false)
      }
      return
    }
    if (routeDatasetID.current !== requestedDatasetID) return
    // 状态接口成功返回即表示迁移已经提交；后续聚合读取失败不能把它误报为迁移失败。
    setSelectedVersion(changed)
    setVersions(current => current.map(item => item.id === changed.id ? { ...item, status: changed.status } : item))
    setMessage(`版本 V${changed.versionNo} 已更新为 ${changed.status}`)
    selectedVersionID.current = changed.id
    try {
      const current = await datasetAPI.get(requestedDatasetID)
      if (routeDatasetID.current !== requestedDatasetID) return
      if (current.version < changed.datasetRecordVersion) {
        setReconciliationRequired(true)
        setError('版本状态已更新，但聚合版本低于状态迁移响应下界，请重新加载草稿后继续操作')
        return
      }
      const sameDraft = current.draftVersionId === baseline.draftVersionId &&
        current.draftRecordVersion === baseline.draftRecordVersion && current.dslHash === baseline.dslHash
      setVersion(current.version)
      setPersistedRecord(current)
      if (!sameDraft) {
        setReconciliationRequired(true)
        setError('版本状态已更新，但远端草稿同时发生变化，请重新加载草稿后继续编辑')
      }
      setVersionRefreshKey(value => value + 1)
    } catch (cause) {
      if (routeDatasetID.current === requestedDatasetID) {
        setReconciliationRequired(true)
        setError(`版本状态已更新，但无法确认最新聚合状态，请重新加载草稿后继续操作。${cause instanceof Error ? ` ${cause.message}` : ''}`)
      }
    } finally {
      if (routeDatasetID.current === requestedDatasetID) setVersionBusy(false)
    }
  }

  const update = (patch: Partial<DatasetDraft>) => setDraft(current => ({ ...current, ...patch }))
  const addTable = async (table: AssetTable) => {
    setError('')
    if (draft.nodes.some(node => node.table.id === table.id)) return
    try {
      const columns = (await datasetAPI.columns(table.id)).items
      const id = `node_${draft.nodes.length + 1}`
      const node: DesignerNode = { id, alias: `t${draft.nodes.length + 1}`, table, columns, selected: columns.map(column => column.columnName) }
      const fields = [...draft.fields, ...columns.map(column => ({ key: `${id}.${column.columnName}`, role: column.semanticType === 'DATE' ? 'TIME' : 'ATTRIBUTE', aggregation: '' }))]
      const nodes = [...draft.nodes, node]
      // 新表先与前一节点建立可见的占位 Join，用户必须在保存前确认两侧字段和基数；
      // 这样节点图始终保持连通，服务端仍会再次校验引用与方向。
      const joins = nodes.length > 1 ? [...draft.joins, { id: `join_${nodes.length - 1}_${nodes.length}`, leftNodeId: nodes[nodes.length - 2].id, rightNodeId: id, leftField: nodes[nodes.length - 2].columns[0]?.columnName ?? '', rightField: columns[0]?.columnName ?? '', joinType: 'INNER', cardinality: 'MANY_TO_ONE', manualConfirmed: false }] : draft.joins
      update({ nodes, fields, joins })
    } catch (cause) { setError(cause instanceof Error ? cause.message : '加载字段失败') }
  }
  const toggleField = (nodeID: string, column: AssetColumn) => setDraft(current => ({ ...current, nodes: current.nodes.map(node => node.id === nodeID ? { ...node, selected: node.selected.includes(column.columnName) ? node.selected.filter(item => item !== column.columnName) : [...node.selected, column.columnName] } : node) }))
  const removeNode = (nodeID: string) => setDraft(current => ({ ...current, nodes: current.nodes.filter(node => node.id !== nodeID), fields: current.fields.filter(field => !field.key.startsWith(`${nodeID}.`)), joins: current.joins.filter(join => join.leftNodeId !== nodeID && join.rightNodeId !== nodeID), filters: current.filters.filter(filter => filter.nodeId !== nodeID), grainKeys: [] }))
  const setField = (key: string, patch: Partial<FieldOption>) => setDraft(current => ({ ...current, fields: current.fields.map(field => field.key === key ? { ...field, ...patch } : field) }))
  const validate = async () => {
    setBusy(true); setError(''); setMessage('')
    try { const result = await datasetAPI.validate(buildDatasetDSL(draft)); setMessage(`DSL 校验通过 · 计划 ${result.planHash.slice(0, 12)}`) }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'DSL 校验失败') }
    finally { setBusy(false) }
  }
  const persistDraft = async () => {
    if (publishOutcomeUnknown) throw new Error('上一次发布结果尚未确认，请先重试刚才的发布')
    const dsl = buildDatasetDSL(draft)
    // 保存前显式调用无副作用校验接口，先向用户返回完整 DSL 问题；更新接口中的
    // expectedVersion 仍是最终并发保护，不能被这次预校验替代。
    await datasetAPI.validate(dsl)
    const record = datasetId && datasetId !== 'new' ? await datasetAPI.update(datasetId, version, draft, dsl) : await datasetAPI.create(dsl)
    // 只有新草稿信封保存成功后才丢弃旧候选，校验或写入失败不能破坏可重试状态。
    publishAttempt.current = null
    setVersion(record.version)
    setPersistedRecord(record)
    setSavedDraftFingerprint(JSON.stringify(dsl))
    return record
  }
  const save = async () => {
    setBusy(true); setError(''); setMessage('')
    try {
      const record = await persistDraft()
      setMessage(`草稿已保存 · 版本 ${record.version}`)
      if (!datasetId || datasetId === 'new') navigate(`/datasets/${record.id}/edit`, { replace: true })
    } catch (cause) { setError(cause instanceof Error ? cause.message : '保存失败') }
    finally { setBusy(false) }
  }
  const runPreview = async () => {
    setBusy(true); setError(''); setMessage(''); setPreview(null); cancelRequested.current = false
    try {
      const parameters = buildPreviewParameters(draft.parameters, previewValues)
      // 先保存再预览，确保执行的是用户当前看到的 DSL，而不是旧草稿。queryId 在
      // 发请求前生成，使取消按钮可以引用与服务端运行记录完全相同的标识。
      const record = await persistDraft()
      if (!datasetId || datasetId === 'new') navigate(`/datasets/${record.id}/edit`, { replace: true })
      const queryId = crypto.randomUUID()
      setActiveQuery({ datasetId: record.id, queryId })
      const result = await datasetAPI.preview(record.id, queryId, parameters)
      setPreview(result)
      setMessage(`预览完成 · ${result.rowCount} 行 · ${result.durationMs} ms${result.warnings?.length ? ` · ${result.warnings.length} 条风险提示` : ''}`)
    } catch (cause) {
      if (cancelRequested.current) setMessage('查询已取消')
      else setError(cause instanceof Error ? cause.message : '数据预览失败')
    } finally { setActiveQuery(null); setBusy(false) }
  }
  const publishDataset = async () => {
    setBusy(true); setError(''); setMessage('')
    const retryingUnknownOutcome = publishOutcomeUnknown
    try {
      let attempt = publishAttempt.current
      if (!retryingUnknownOutcome) {
        const validationParameters = buildPreviewParameters(draft.parameters, previewValues)
        const fingerprint = JSON.stringify({ draft: currentDraftFingerprint, validationParameters })
        if (attempt && (attempt.datasetId !== persistedRecord?.id || attempt.fingerprint !== fingerprint)) {
          attempt = null
          publishAttempt.current = null
        }
        if (!attempt) {
          // 保存和发布是两种独立权限；发布入口只使用最近保存的确定草稿，不能暗中先执行 MANAGE 写入。
          if (!persistedRecord || currentDraftFingerprint !== savedDraftFingerprint) {
            throw new Error('当前草稿有未保存修改，请先保存草稿后再发布')
          }
          attempt = {
            datasetId: persistedRecord.id,
            fingerprint,
            idempotencyKey: createDatasetPublishIdempotencyKey(),
            input: {
              draftVersionId: persistedRecord.draftVersionId,
              expectedVersion: persistedRecord.version,
              expectedDraftRecordVersion: persistedRecord.draftRecordVersion,
              expectedDslHash: persistedRecord.dslHash,
              validationParameters,
            },
          }
          publishAttempt.current = attempt
        }
      }
      if (!attempt) throw new Error('上一次发布候选已丢失，请重新加载数据集后确认发布状态')
      // 模糊失败后始终优先重放冻结候选，不读取已变化的表单，也不生成新的幂等键。
      const published = await datasetAPI.publish(attempt.datasetId, attempt.input, attempt.idempotencyKey)
      publishAttempt.current = null
      setPublishOutcomeUnknown(false)
      selectedVersionID.current = published.id
      setVersionRefreshKey(value => value + 1)

      // 幂等重放可能发生在其他用户又修改草稿之后，必须重新加载当前基线，不能盲信首次响应中的旧版本号。
      try {
        const current = await datasetAPI.get(attempt.datasetId)
        const sameDraft = current.draftVersionId === attempt.input.draftVersionId &&
          current.draftRecordVersion === attempt.input.expectedDraftRecordVersion &&
          current.dslHash === attempt.input.expectedDslHash
        const currentEnough = current.version >= published.datasetRecordVersion
        if (sameDraft && currentEnough) {
          setVersion(current.version)
          setPersistedRecord(current)
          setReconciliationRequired(false)
          setMessage(`数据集已发布 · V${published.versionNo} · 精确版本 ${published.id}`)
        } else {
          setReconciliationRequired(true)
          setMessage(`数据集已发布 · V${published.versionNo} · 精确版本 ${published.id}`)
          setError(sameDraft ? '发布已成功，但服务端返回的草稿基线过旧，请重新加载后继续编辑' : '发布已成功，但远端草稿随后发生变化，请重新加载后继续编辑')
        }
      } catch {
        setReconciliationRequired(true)
        setMessage(`数据集已发布 · V${published.versionNo} · 精确版本 ${published.id}；请重新加载以确认最新草稿`)
      }
    } catch (cause) {
      if (publishAttempt.current && isAmbiguousPublishFailure(cause)) {
        setPublishOutcomeUnknown(true)
        setError(`发布结果尚未确认，请点击“重试刚才发布”；确认前已锁定编辑和保存。${cause instanceof Error ? ` ${cause.message}` : ''}`)
      } else {
        publishAttempt.current = null
        setPublishOutcomeUnknown(false)
        if (retryingUnknownOutcome && requiresPublishReconciliation(cause)) {
          setReconciliationRequired(true)
          setError(`${cause instanceof Error ? cause.message : '数据集发布失败'}；请重新加载草稿核对远端状态`)
        } else {
          setError(cause instanceof Error ? cause.message : '数据集发布失败')
        }
      }
    }
    finally { setBusy(false) }
  }
  const reloadCurrentDataset = async () => {
    if (!datasetId || datasetId === 'new') return
    setBusy(true); setError(''); setMessage('')
    try {
      const record = await datasetAPI.get(datasetId)
      const restored = await hydrateDraft(record, tables)
      setDraft(restored)
      setVersion(record.version)
      setPersistedRecord(record)
      setSavedDraftFingerprint(draftFingerprint(restored))
      setReconciliationRequired(false)
      setMessage(`已重新加载远端草稿 · 版本 ${record.version}`)
    } catch (cause) { setError(cause instanceof Error ? cause.message : '重新加载草稿失败') }
    finally { setBusy(false) }
  }
  const cancelPreview = async () => {
    if (!activeQuery) return
    cancelRequested.current = true
    try {
      await datasetAPI.cancel(activeQuery.datasetId, activeQuery.queryId)
      setMessage('正在取消查询…')
    } catch (cause) { setError(cause instanceof Error ? cause.message : '取消查询失败') }
  }

  return (
    <AppShell title={draft.name || '新建数据集'} eyebrow="数据集设计器" actions={<><button className="quiet-button" onClick={validate} disabled={busy || versionBusy || !permissionsResolved || !capabilities.manage || publishOutcomeUnknown || reconciliationRequired}>校验 DSL</button>{activeQuery ? <button className="danger-button" onClick={cancelPreview}>取消查询</button> : <button className="quiet-button" onClick={runPreview} disabled={busy || versionBusy || !permissionsResolved || !capabilities.read || publishOutcomeUnknown || reconciliationRequired}>数据预览</button>}{reconciliationRequired && <button className="quiet-button" onClick={reloadCurrentDataset} disabled={busy || versionBusy}>重新加载草稿</button>}<button className="quiet-button" onClick={save} disabled={busy || versionBusy || !permissionsResolved || !capabilities.manage || publishOutcomeUnknown || reconciliationRequired}>保存草稿</button><button className="primary-button" onClick={publishDataset} disabled={busy || versionBusy || !permissionsResolved || !capabilities.publish || reconciliationRequired}>{publishOutcomeUnknown ? '重试刚才发布' : '发布版本'}</button></>}>
      <div className="dataset-status" aria-live="polite">{error && <span className="designer-error">{error}</span>}{message && <span className="designer-success">{message}</span>}<small>预览会先保存草稿；发布只使用最近保存的确定版本，并应用参数绑定、权限、超时与行数限制</small></div>
      {datasetId && datasetId !== 'new' && <PublishedVersionManager
        permissionsReady={permissionsResolved} canRead={capabilities.read} canPublish={capabilities.publish}
        versions={versions} currentPublishedVersionID={persistedRecord?.currentPublishedVersionId ?? ''}
        selectedVersion={selectedVersion} usage={selectedVersionUsage} parameters={selectedVersionParameters}
        parameterValues={versionPreviewValues} preview={versionPreview} loading={versionLoading} busy={versionBusy}
        aggregateReady={Boolean(persistedRecord)} writesLocked={publishOutcomeUnknown || reconciliationRequired}
        onSelect={selectPublishedVersion}
        onParameterChange={(code, value) => setVersionPreviewValues(current => ({ ...current, [code]: value }))}
        onPreview={previewPublishedVersion} onTransition={transitionPublishedVersion}
      />}
      <fieldset className="dataset-designer" disabled={busy || versionBusy || !permissionsResolved || !capabilities.manage || publishOutcomeUnknown || reconciliationRequired} aria-label="数据集编辑区">
        <aside className="dataset-assets"><span className="eyebrow">资产目录</span><h2>选择数据表</h2>{tables.map(table => <button key={table.id} onClick={() => addTable(table)}><strong>{table.businessName || table.tableName}</strong><small>{table.dataSourceName} · {table.dataSourceType}</small></button>)}</aside>
        <section className="dataset-workbench">
          <div className="dataset-meta"><label>数据集编码<input value={draft.code} disabled={version > 0} onChange={event => update({ code: event.target.value })} /></label><label>数据集名称<input value={draft.name} onChange={event => update({ name: event.target.value })} /></label><label className="wide">说明<input value={draft.description} onChange={event => update({ description: event.target.value })} /></label></div>
          {!draft.nodes.length && <div className="dataset-empty"><strong>从左侧选择第一张表</strong><span>字段会自动载入，可继续配置语义角色、聚合和粒度。</span></div>}
          {draft.nodes.map(node => <article className="dataset-node" key={node.id}><header><div><span className="eyebrow">{node.table.dataSourceType}</span><h3>{node.table.businessName || node.table.tableName}</h3></div><div className="node-actions"><label>别名<input value={node.alias} onChange={event => setDraft(current => ({ ...current, nodes: current.nodes.map(item => item.id === node.id ? { ...item, alias: event.target.value } : item) }))} /></label><button className="quiet-button" onClick={() => removeNode(node.id)}>移除</button></div></header><div className="dataset-field-head"><span>输出</span><span>字段</span><span>类型</span><span>角色</span><span>聚合</span></div>{node.columns.map(column => { const key = `${node.id}.${column.columnName}`, option = draft.fields.find(item => item.key === key); return <div className="dataset-field" key={column.id}><input aria-label={`选择 ${column.columnName}`} type="checkbox" checked={node.selected.includes(column.columnName)} onChange={() => toggleField(node.id, column)} /><strong>{column.businessName || column.columnName}<small>{column.columnName}</small></strong><span>{column.canonicalType}</span><select value={option?.role ?? 'ATTRIBUTE'} onChange={event => setField(key, { role: event.target.value })}><option>ATTRIBUTE</option><option>DIMENSION</option><option>TIME</option><option>IDENTIFIER</option><option>MEASURE</option></select><select value={option?.aggregation ?? ''} onChange={event => setField(key, { aggregation: event.target.value })}><option value="">不聚合</option><option>SUM</option><option>AVG</option><option>MIN</option><option>MAX</option><option>COUNT</option><option>COUNT_DISTINCT</option></select></div>})}</article>)}
          {draft.joins.map((join, index) => <JoinEditor key={join.id} join={join} nodes={draft.nodes} onChange={next => update({ joins: draft.joins.map((item, itemIndex) => itemIndex === index ? next : item) })} />)}
          {preview && <PreviewTable preview={preview} />}
        </section>
        <aside className="dataset-config"><span className="eyebrow">建模设置</span><h2>过滤与粒度</h2><label>每一行代表什么<textarea value={draft.grainDescription} onChange={event => update({ grainDescription: event.target.value })} /></label><fieldset><legend>粒度键</legend>{selectedFields.map(field => <label className="check-row" key={field.key}><input type="checkbox" checked={draft.grainKeys.includes(field.code)} onChange={() => update({ grainKeys: draft.grainKeys.includes(field.code) ? draft.grainKeys.filter(item => item !== field.code) : [...draft.grainKeys, field.code] })} />{field.label}</label>)}</fieldset><ConfigList title="参数" onAdd={() => update({ parameters: [...draft.parameters, { code: `param_${draft.parameters.length + 1}`, name: '新参数', dataType: 'STRING', required: false, multiValue: false }] })}>{draft.parameters.map((parameter, index) => <ParameterEditor key={index} value={parameter} onChange={next => update({ parameters: draft.parameters.map((item, itemIndex) => itemIndex === index ? next : item) })} />)}</ConfigList>{draft.parameters.length > 0 && <section className="preview-parameters"><strong>预览参数值</strong>{draft.parameters.map(parameter => <label key={parameter.code}>{parameter.name || parameter.code}<input aria-label={`预览参数 ${parameter.code}`} type={parameter.dataType === 'DATE' ? 'date' : 'text'} placeholder={parameter.multiValue ? '多个值请用逗号分隔' : parameter.dataType} value={previewValues[parameter.code] ?? ''} onChange={event => setPreviewValues(current => ({ ...current, [parameter.code]: event.target.value }))} /></label>)}</section>}<ConfigList title="过滤条件" onAdd={() => { const first = draft.nodes[0], column = first?.columns[0]; if (first && column) update({ filters: [...draft.filters, { id: `filter_${draft.filters.length + 1}`, nodeId: first.id, field: column.columnName, operator: 'EQUALS', value: '', parameterCode: '' }] }) }}>{draft.filters.map((filter, index) => <FilterEditor key={filter.id} value={filter} nodes={draft.nodes} parameters={draft.parameters} onChange={next => update({ filters: draft.filters.map((item, itemIndex) => itemIndex === index ? next : item) })} />)}</ConfigList><ConfigList title="计算字段" onAdd={() => { const first = selectedFields[0], second = selectedFields[1] ?? first; if (first && second) update({ calculations: [...draft.calculations, { id: `field_calc_${draft.calculations.length + 1}`, code: `calculated_${draft.calculations.length + 1}`, name: '计算字段', operation: 'ADD', leftKey: first.key, rightKey: second.key, canonicalType: 'DECIMAL' }] }) }}>{draft.calculations.map((item, index) => <CalculatedEditor key={item.id} value={item} fields={selectedFields} onChange={next => update({ calculations: draft.calculations.map((field, fieldIndex) => fieldIndex === index ? next : field) })} />)}</ConfigList><ConfigList title="排序" onAdd={() => { const first = selectedFields[0]; if (first) update({ sorts: [...draft.sorts, { fieldId: first.code, direction: 'ASC' }] }) }}>{draft.sorts.map((item, index) => <div className="mini-editor" key={index}><select value={item.fieldId} onChange={event => update({ sorts: draft.sorts.map((sort, sortIndex) => sortIndex === index ? { ...sort, fieldId: event.target.value } : sort) })}>{selectedFields.map(field => <option key={field.key} value={field.code}>{field.label}</option>)}</select><select value={item.direction} onChange={event => update({ sorts: draft.sorts.map((sort, sortIndex) => sortIndex === index ? { ...sort, direction: event.target.value } : sort) })}><option>ASC</option><option>DESC</option></select></div>)}</ConfigList>
        </aside>
      </fieldset>
    </AppShell>
  )
}

function PublishedVersionManager({
  permissionsReady, canRead, canPublish, versions, currentPublishedVersionID, selectedVersion, usage,
  parameters, parameterValues, preview, loading, busy, aggregateReady, writesLocked, onSelect, onParameterChange, onPreview, onTransition,
}: {
  permissionsReady: boolean
  canRead: boolean
  canPublish: boolean
  versions: PublishedVersionSummary[]
  currentPublishedVersionID: string
  selectedVersion: PublishedVersionRecord | null
  usage: VersionUsage
  parameters: ParameterOption[]
  parameterValues: Record<string, string>
  preview: DatasetPreview | null
  loading: boolean
  busy: boolean
  aggregateReady: boolean
  writesLocked: boolean
  onSelect: (versionID: string) => void
  onParameterChange: (code: string, value: string) => void
  onPreview: () => void
  onTransition: (status: 'STALE' | 'DEPRECATED') => void
}) {
  const previewDisabled = busy || loading || writesLocked || selectedVersion?.status !== 'PUBLISHED'
  const transitionDisabled = busy || loading || writesLocked || !aggregateReady || !canPublish
  return <section className="dataset-version-manager" aria-label="已发布版本管理">
    <header className="version-manager-heading">
      <div><span className="eyebrow">不可变快照</span><h2>已发布版本</h2></div>
      {writesLocked && <small>发布状态未完成对账，当前仅允许查看版本</small>}
    </header>
    {!permissionsReady ? <p className="version-empty">正在检查版本访问权限…</p> : !canRead ? <p className="version-empty">当前账号没有读取发布版本的权限。</p> : <>
      <nav className="version-list" aria-label="发布版本列表">
        {!versions.length && !loading && <span className="version-empty">暂无已发布版本</span>}
        {versions.map(item => {
          const current = item.id === currentPublishedVersionID
          const selected = item.id === selectedVersion?.id
          return <button key={item.id} className={selected ? 'selected' : ''} aria-pressed={selected} disabled={loading || busy} onClick={() => onSelect(item.id)}>
            <strong>V{item.versionNo}</strong><span>{item.status}</span>{current && <em>当前发布</em>}
          </button>
        })}
      </nav>
      <div className="version-detail">
        {loading && !selectedVersion ? <p className="version-empty">正在加载版本详情…</p> : selectedVersion ? <>
          <header>
            <div><strong>V{selectedVersion.versionNo}</strong><span className={`version-status status-${selectedVersion.status.toLowerCase()}`}>{selectedVersion.status}</span>{selectedVersion.id === currentPublishedVersionID && <span className="current-version">当前发布版本</span>}</div>
            <small>发布于 {new Date(selectedVersion.publishedAt).toLocaleString('zh-CN')}</small>
          </header>
          <dl className="version-metadata">
            <div><dt>精确版本 ID</dt><dd>{selectedVersion.id}</dd></div>
            <div><dt>DSL 摘要</dt><dd>{selectedVersion.dslHash.slice(0, 16)}</dd></div>
            <div><dt>计划摘要</dt><dd>{selectedVersion.planHash.slice(0, 16)}</dd></div>
          </dl>
          <section className="version-usage" aria-label="版本使用汇总">
            <span>报告草稿引用<strong>{usage.reportDraftReferences}</strong></span>
            <span>下游草稿引用<strong>{usage.downstreamDraftReferences}</strong></span>
            <span>下游发布引用<strong>{usage.downstreamPublishedReferences}</strong></span>
            <span>运行中查询<strong>{usage.activeQueryRuns}</strong></span>
          </section>
          {parameters.length > 0 && <section className="version-parameters" aria-label="精确版本预览参数">
            {parameters.map(parameter => <label key={parameter.code}>{parameter.name || parameter.code}
              <input aria-label={`版本参数 ${parameter.code}`} type={parameter.dataType === 'DATE' ? 'date' : 'text'} placeholder={parameter.multiValue ? '多个值请用逗号分隔' : parameter.dataType} value={parameterValues[parameter.code] ?? ''} disabled={previewDisabled} onChange={event => onParameterChange(parameter.code, event.target.value)} />
            </label>)}
          </section>}
          <div className="version-actions">
            <button className="quiet-button" disabled={previewDisabled} onClick={onPreview}>预览精确版本</button>
            {selectedVersion.status === 'PUBLISHED' && <button className="quiet-button" disabled={transitionDisabled} onClick={() => onTransition('STALE')}>标记为失效</button>}
            {(selectedVersion.status === 'PUBLISHED' || selectedVersion.status === 'STALE') && <button className="danger-button" disabled={transitionDisabled} onClick={() => onTransition('DEPRECATED')}>废弃版本</button>}
            {selectedVersion.status !== 'PUBLISHED' && <small>失效或废弃版本仅供审计查看，不能执行预览。</small>}
          </div>
          {preview && <PreviewTable preview={preview} />}
        </> : <p className="version-empty">选择一个发布版本查看精确快照。</p>}
      </div>
    </>}
  </section>
}

export function PreviewTable({ preview }: { preview: DatasetPreview }) {
  const display = (value: unknown) => value == null ? '—' : typeof value === 'object' ? JSON.stringify(value) : String(value)
  return <article className="dataset-preview"><header><div><span className="eyebrow">安全查询结果</span><h3>数据预览</h3></div><small>{preview.rowCount} 行 · {preview.durationMs} ms</small></header>{Boolean(preview.warnings?.length) && <section className="preview-warnings" aria-label="Join 风险提示">{preview.warnings?.map((warning, index) => <div key={`${warning.code}-${warning.joinId ?? index}`}><strong>{warning.code}</strong><span>{warning.message}</span>{warning.estimatedRows != null && <small>预计 {warning.estimatedRows} 行</small>}</div>)}</section>}<div className="preview-scroll"><table><thead><tr>{preview.columns.map(column => <th key={column}>{column}</th>)}</tr></thead><tbody>{preview.rows.map((row, index) => <tr key={index}>{preview.columns.map((column, columnIndex) => <td key={`${column}-${columnIndex}`}>{display(row[columnIndex])}</td>)}</tr>)}</tbody></table></div></article>
}

function ConfigList({ title, onAdd, children }: { title: string; onAdd: () => void; children: ReactNode }) { return <section className="config-list"><header><strong>{title}</strong><button onClick={onAdd}>＋</button></header>{children}</section> }
function ParameterEditor({ value, onChange }: { value: ParameterOption; onChange: (value: ParameterOption) => void }) { return <div className="mini-editor"><input aria-label="参数编码" value={value.code} onChange={event => onChange({ ...value, code: event.target.value })} /><input aria-label="参数名称" value={value.name} onChange={event => onChange({ ...value, name: event.target.value })} /><select value={value.dataType} onChange={event => onChange({ ...value, dataType: event.target.value })}><option>STRING</option><option>INTEGER</option><option>DECIMAL</option><option>DATE</option><option>DATETIME</option><option>BOOLEAN</option></select><label className="check-row"><input type="checkbox" checked={value.required} onChange={event => onChange({ ...value, required: event.target.checked })} />必填</label><label className="check-row"><input type="checkbox" checked={value.multiValue} onChange={event => onChange({ ...value, multiValue: event.target.checked })} />多值</label></div> }
function FilterEditor({ value, nodes, parameters, onChange }: { value: FilterOption; nodes: DesignerNode[]; parameters: ParameterOption[]; onChange: (value: FilterOption) => void }) { const node = nodes.find(item => item.id === value.nodeId) ?? nodes[0]; return <div className="mini-editor"><select value={value.nodeId} onChange={event => onChange({ ...value, nodeId: event.target.value, field: nodes.find(item => item.id === event.target.value)?.columns[0]?.columnName ?? '' })}>{nodes.map(item => <option key={item.id} value={item.id}>{item.alias}</option>)}</select><select value={value.field} onChange={event => onChange({ ...value, field: event.target.value })}>{node?.columns.map(column => <option key={column.id} value={column.columnName}>{column.columnName}</option>)}</select><select value={value.operator} onChange={event => onChange({ ...value, operator: event.target.value })}><option>EQUALS</option><option>NOT_EQUALS</option><option>GT</option><option>GTE</option><option>LT</option><option>LTE</option><option>LIKE</option></select><select value={value.parameterCode} onChange={event => onChange({ ...value, parameterCode: event.target.value })}><option value="">固定值</option>{parameters.map(parameter => <option key={parameter.code}>{parameter.code}</option>)}</select>{!value.parameterCode && <input aria-label="过滤值" value={value.value} onChange={event => onChange({ ...value, value: event.target.value })} />}</div> }
function CalculatedEditor({ value, fields, onChange }: { value: CalculatedField; fields: Array<{ key: string; label: string }>; onChange: (value: CalculatedField) => void }) { return <div className="mini-editor"><input aria-label="计算字段编码" value={value.code} onChange={event => onChange({ ...value, code: event.target.value })} /><input aria-label="计算字段名称" value={value.name} onChange={event => onChange({ ...value, name: event.target.value })} /><select value={value.leftKey} onChange={event => onChange({ ...value, leftKey: event.target.value })}>{fields.map(field => <option key={field.key} value={field.key}>{field.label}</option>)}</select><select value={value.operation} onChange={event => onChange({ ...value, operation: event.target.value })}><option>ADD</option><option>SUBTRACT</option><option>MULTIPLY</option><option>DIVIDE</option></select><select value={value.rightKey} onChange={event => onChange({ ...value, rightKey: event.target.value })}>{fields.map(field => <option key={field.key} value={field.key}>{field.label}</option>)}</select></div> }
function JoinEditor({ join, nodes, onChange }: { join: JoinOption; nodes: DesignerNode[]; onChange: (value: JoinOption) => void }) { const left = nodes.find(node => node.id === join.leftNodeId), right = nodes.find(node => node.id === join.rightNodeId); return <article className="join-editor"><strong>关联 {left?.alias} → {right?.alias}</strong><select value={join.leftField} onChange={event => onChange({ ...join, leftField: event.target.value, manualConfirmed: false })}>{left?.columns.map(column => <option key={column.id}>{column.columnName}</option>)}</select><span>=</span><select value={join.rightField} onChange={event => onChange({ ...join, rightField: event.target.value, manualConfirmed: false })}>{right?.columns.map(column => <option key={column.id}>{column.columnName}</option>)}</select><select value={join.joinType} onChange={event => onChange({ ...join, joinType: event.target.value, manualConfirmed: false })}><option>INNER</option><option>LEFT</option><option>RIGHT</option><option>FULL</option></select><select value={join.cardinality} onChange={event => onChange({ ...join, cardinality: event.target.value, manualConfirmed: false })}><option>ONE_TO_ONE</option><option>ONE_TO_MANY</option><option>MANY_TO_ONE</option><option>MANY_TO_MANY</option></select><label className="join-confirm"><input type="checkbox" checked={join.manualConfirmed} onChange={event => onChange({ ...join, manualConfirmed: event.target.checked })} />已核对基数</label></article> }
