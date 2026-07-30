import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  ArrowClockwiseIcon,
  CaretDownIcon,
  DatabaseIcon,
  FunnelIcon,
  FunctionIcon,
  GitBranchIcon,
  MagicWandIcon,
  MagnifyingGlassIcon,
  PlusIcon,
  StackIcon,
  TableIcon,
  XIcon,
} from '@phosphor-icons/react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AssetManagementTabs } from '../components/AssetManagementTabs'
import { AssetSharingSelect } from '../components/AssetSharingSelect'
import {
  datasetAPI,
  type AssetTable,
  type DatasetPreview,
  type DatasetSummary,
  type PublishedVersionRecord,
  type WarehouseLineage,
  type WarehouseLineageNode,
} from '../lib/datasets'
import {
  metricAPI,
  type MetricDimensionFilter,
  type MetricDefinition,
  type MetricExpression,
  type MetricRecord,
  type MetricStatus,
  type MetricSummary,
  type MetricUsage,
  type MetricVersionRecord,
} from '../lib/metrics'
import { metricCandidateAPI } from '../lib/metric-candidates'
import {
  semanticGovernanceAPI,
  type Dimension,
  type DimensionMember,
  type DimensionStatus,
  type DimensionType,
  type MemberIndexPolicy,
} from '../lib/semantic-governance'

const catalogPageSize = 200
const statusLabels: Record<string, string> = {
  DRAFT: '草稿', PUBLISHED: '已发布', STALE: '已失效', DEPRECATED: '已废弃',
}
const typeLabels: Record<string, string> = { ATOMIC: '原子指标', DERIVED: '派生指标', RATIO: '复合指标' }
const aggregationLabels: Record<string, string> = {
  NONE: '不聚合', SUM: '求和', AVG: '平均值', MIN: '最小值', MAX: '最大值', COUNT: '计数', COUNT_DISTINCT: '去重计数',
}
const timeGrainLabels: Record<string, string> = {
  NONE: '无', DAY: '日', WEEK: '周', MONTH: '月', QUARTER: '季度', YEAR: '年',
}
const additivityLabels: Record<string, string> = {
  ADDITIVE: '可加', SEMI_ADDITIVE: '半可加', NON_ADDITIVE: '不可加',
}
const valueBehaviorLabels: Record<string, string> = {
  FLOW: '流量值', CUMULATIVE: '累计值', POINT_IN_TIME: '时点值', NON_ADDITIVE: '不可加值',
}
const timeAggregationLabels: Record<string, string> = {
  SUM: '跨时间求和', LAST: '取期末值', NONE: '禁止跨时间聚合',
}
const dimensionTypeLabels: Record<DimensionType, string> = {
  STANDARD: '标准维度',
  TIME: '时间维度',
  GEOGRAPHY: '地理维度',
  ORGANIZATION: '组织维度',
  PRODUCT: '产品维度',
  CUSTOMER: '客户维度',
  OTHER: '其他维度',
}
const memberPolicyLabels: Record<MemberIndexPolicy, string> = {
  FULL: '完整成员索引',
  EXACT_ONLY: '仅精确匹配',
  NONE: '不建立成员索引',
}
const emptyUsage = (): MetricUsage => ({
  reportDraftReferences: 0,
  downstreamDraftReferences: 0,
  downstreamPublishedReferences: 0,
  activeQueryRuns: 0,
})

function synchronizedAssetStatus(
  dataset: DatasetSummary | undefined,
  datasetVersionId: string,
): MetricStatus {
  if (!dataset) return 'STALE'
  if (dataset.status === 'DEPRECATED') return 'DEPRECATED'
  if (dataset.status !== 'PUBLISHED' ||
      dataset.currentPublishedVersionId !== datasetVersionId) return 'STALE'
  return 'PUBLISHED'
}

type DetailTab = 'overview' | 'definition' | 'dimensions' | 'preview' | 'source' | 'lineage'
type MetricDetail = {
  record: MetricRecord
  publishedVersion: MetricVersionRecord | null
  datasetVersion: PublishedVersionRecord | null
  lineage: WarehouseLineage | null
  sourceAssets: AssetTable[]
  usage: MetricUsage
  publishedUnavailable: boolean
  sourceUnavailable: boolean
  lineageUnavailable: boolean
  usageUnavailable: boolean
}
type DatasetField = {
  id: string
  code: string
  name: string
  canonicalType: string
  expression: Record<string, unknown>
}
type SourceNode = {
  id: string
  type: string
  alias: string
  datasourceId: string
  tableId: string
  datasetVersionId: string
  fileVersionId: string
}
type DimensionDraft = Pick<
  Dimension,
  'code' | 'name' | 'description' | 'dimensionType' | 'memberIndexPolicy' |
  'highCardinality' | 'sensitive' | 'status'
>
type DimensionEditorState = { dimension: Dimension; draft: DimensionDraft }
type MetricRequirementState = { dataset: DatasetSummary; requirement: string }
type DimensionRequirementState = { dataset: DatasetSummary; requirement: string }

const asRecord = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
const asText = (value: unknown): string => typeof value === 'string' ? value : ''
const shortId = (value: string): string => value.length > 20 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value || '—'

async function loadAllMetrics(): Promise<MetricSummary[]> {
  const items: MetricSummary[] = []
  for (let offset = 0; ;) {
    const page = await metricAPI.list(catalogPageSize, offset)
    items.push(...page.items)
    if (!page.items.length || items.length >= page.total) return items
    offset += page.items.length
  }
}

async function loadAllDatasets(): Promise<DatasetSummary[]> {
  const items: DatasetSummary[] = []
  for (let offset = 0; ;) {
    const page = await datasetAPI.list(catalogPageSize, offset)
    items.push(...page.items)
    if (!page.items.length || items.length >= page.total) return items
    offset += page.items.length
  }
}

async function loadAllDimensions(): Promise<Dimension[]> {
  const items: Dimension[] = []
  for (let offset = 0; ;) {
    const page = await semanticGovernanceAPI.listDimensions({
      status: '',
      limit: catalogPageSize,
      offset,
    })
    items.push(...page.items)
    if (!page.items.length || items.length >= page.total) return items
    offset += page.items.length
  }
}

function datasetFields(version: PublishedVersionRecord | null): DatasetField[] {
  if (!version) return []
  return version.dsl.fields.map(asRecord).map(field => ({
    id: asText(field.id),
    code: asText(field.code),
    name: asText(field.name) || asText(field.code) || asText(field.id),
    canonicalType: asText(field.canonicalType) || 'STRING',
    expression: asRecord(field.expression),
  })).filter(field => field.id)
}

function sourceNodes(version: PublishedVersionRecord | null): SourceNode[] {
  if (!version) return []
  return version.dsl.nodes.map(asRecord).map(node => ({
    id: asText(node.id),
    type: asText(node.type),
    alias: asText(node.alias),
    datasourceId: asText(node.datasourceId) || asText(node.dataSourceId),
    tableId: asText(node.tableId),
    datasetVersionId: asText(node.datasetVersionId),
    fileVersionId: asText(node.fileVersionId),
  })).filter(node => node.id)
}

function fieldName(fields: DatasetField[], fieldId: string): string {
  const field = fields.find(item => item.id === fieldId)
  return field ? `${field.name}（${field.code || field.id}）` : fieldId || '未指定字段'
}

function expressionLabel(expression: MetricExpression, fields: DatasetField[]): string {
  if (expression.type === 'FIELD_REF') return fieldName(fields, expression.fieldId)
  if (expression.type === 'METRIC_REF') return `指标版本 ${shortId(expression.metricVersionId)}`
  if (expression.type === 'LITERAL') return expression.value
  const operator = { ADD: '+', SUBTRACT: '−', MULTIPLY: '×', DIVIDE: '÷' }[expression.type]
  return `(${expression.arguments.map(argument => expressionLabel(argument, fields)).join(` ${operator} `)})`
}

function formulaLabel(definition: MetricDefinition, fields: DatasetField[]): string {
  if (definition.sourceCalculation?.formula) return definition.sourceCalculation.formula
  const expression = expressionLabel(definition.expression, fields)
  return definition.aggregation === 'NONE' ? expression : `${definition.aggregation}(${expression})`
}

function businessAggregation(definition: MetricDefinition): string {
  return definition.sourceCalculation?.aggregation ?? definition.aggregation
}

function aggregationSummary(definition: MetricDefinition): string {
  const aggregation = businessAggregation(definition)
  const label = aggregationLabels[aggregation] ?? aggregation
  return definition.sourceCalculation ? `${label}（数据集 DAG 已完成）` : label
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date)
}

type DimensionValueSample = {
  label: string
  count: number
}

type DimensionValuePreviewSummary = {
  items: DimensionValueSample[]
  sampledRows: number
  effectiveRows: number
  excludedRows: number
  distinctValues: number
}

const dimensionPreviewSampleSize = 100
const reservedDimensionValuePattern =
  /^(unknown|\+?999999999(?:\.0+)?|1970-01-01(?:[ t]00:00:00(?:\.0+)?(?:z|[+-]00(?::?00)?)?)?)$/i

function dimensionValueText(value: unknown): string {
  if (typeof value === 'string') return value.trim()
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    return String(value)
  }
  return ''
}

function dimensionPreviewFieldIndex(preview: DatasetPreview, dimension: Dimension): number {
  const metadataIndex = preview.columnMetadata?.findIndex(column =>
    column.fieldId === dimension.fieldId ||
    column.code === dimension.fieldCode ||
    column.physicalName === dimension.fieldCode
  ) ?? -1
  if (metadataIndex >= 0) return metadataIndex
  return preview.columns.findIndex(column =>
    column === dimension.fieldId || column === dimension.fieldCode
  )
}

function summarizeDimensionValuePreview(
  preview: DatasetPreview,
  dimension: Dimension,
): DimensionValuePreviewSummary {
  const fieldIndex = dimensionPreviewFieldIndex(preview, dimension)
  if (fieldIndex < 0) throw new Error(`预览结果中未找到来源字段“${dimension.fieldCode}”`)
  const frequencies = new Map<string, DimensionValueSample>()
  let effectiveRows = 0
  let excludedRows = 0
  for (const row of preview.rows) {
    const label = dimensionValueText(row[fieldIndex])
    const normalized = label.normalize('NFKC').toLocaleLowerCase('zh-CN')
    if (!normalized || reservedDimensionValuePattern.test(normalized)) {
      excludedRows += 1
      continue
    }
    effectiveRows += 1
    const existing = frequencies.get(normalized)
    if (existing) {
      existing.count += 1
    } else {
      frequencies.set(normalized, { label, count: 1 })
    }
  }
  const items = [...frequencies.values()]
    .sort((left, right) =>
      right.count - left.count || left.label.localeCompare(right.label, 'zh-CN'))
    .slice(0, 10)
  return {
    items,
    sampledRows: preview.rows.length,
    effectiveRows,
    excludedRows,
    distinctValues: frequencies.size,
  }
}

/** 指标目录负责发现与理解；高风险的编辑和发布继续由独立编辑路由承载。 */
export function MetricCatalogPage() {
  const navigate = useNavigate()
  const [metrics, setMetrics] = useState<MetricSummary[]>([])
  const [datasets, setDatasets] = useState<DatasetSummary[]>([])
  const [dimensions, setDimensions] = useState<Dimension[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [assetSyncError, setAssetSyncError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('ALL')
  const [type, setType] = useState('ALL')
  const [datasetId, setDatasetId] = useState('ALL')
  const [identifying, setIdentifying] = useState(false)
  const [assetSyncNotice, setAssetSyncNotice] = useState('')
  const [selected, setSelected] = useState<MetricSummary | null>(null)
  const [detail, setDetail] = useState<MetricDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [tab, setTab] = useState<DetailTab>('overview')
  const [deletingMetric, setDeletingMetric] = useState<MetricSummary | null>(null)
  const [selectedDimension, setSelectedDimension] = useState<Dimension | null>(null)
  const [dimensionEditor, setDimensionEditor] = useState<DimensionEditorState | null>(null)
  const [deletingDimension, setDeletingDimension] = useState<Dimension | null>(null)
  const [metricRequirement, setMetricRequirement] = useState<MetricRequirementState | null>(null)
  const [metricRequirementError, setMetricRequirementError] = useState('')
  const [dimensionRequirement, setDimensionRequirement] = useState<DimensionRequirementState | null>(null)
  const [dimensionBusy, setDimensionBusy] = useState(false)
  const [dimensionError, setDimensionError] = useState('')
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteError, setDeleteError] = useState('')

  useEffect(() => {
    let active = true
    queueMicrotask(() => {
      if (!active) return
      setLoading(true)
      setError('')
    })
    Promise.allSettled([loadAllMetrics(), loadAllDatasets(), loadAllDimensions()]).then(([metricResult, datasetResult, dimensionResult]) => {
      if (!active) return
      if (metricResult.status === 'rejected') {
        setMetrics([])
        setError(metricResult.reason instanceof Error ? `加载指标目录失败：${metricResult.reason.message}` : '加载指标目录失败')
      } else {
        setMetrics(metricResult.value)
      }
      if (datasetResult.status === 'fulfilled') setDatasets(datasetResult.value)
      if (dimensionResult.status === 'fulfilled') {
        setDimensions(dimensionResult.value)
      } else {
        setDimensions([])
        setError(current => current || (dimensionResult.reason instanceof Error
          ? `加载维度目录失败：${dimensionResult.reason.message}`
          : '加载维度目录失败'))
      }
    }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [reloadKey])

  useEffect(() => {
    if (!selected) {
      queueMicrotask(() => {
        setDetail(null)
        setDetailError('')
      })
      return
    }
    let active = true
    queueMicrotask(() => {
      if (!active) return
      setDetail(null)
      setDetailLoading(true)
      setDetailError('')
    })
    metricAPI.get(selected.id).then(async record => {
      let publishedVersion: MetricVersionRecord | null = null
      let publishedUnavailable = false
      if (record.currentPublishedVersionId) {
        try {
          publishedVersion = await metricAPI.getVersion(record.id, record.currentPublishedVersionId)
        } catch {
          publishedUnavailable = true
        }
      }
      const definition = publishedVersion?.definition ?? (record.currentPublishedVersionId ? null : record.definition)
      const [datasetResult, lineageResult, usageResult] = await Promise.allSettled([
        definition ? datasetAPI.getVersion(definition.datasetId, definition.datasetVersionId) : Promise.resolve(null),
        definition ? datasetAPI.getWarehouseLineage(definition.datasetVersionId) : Promise.resolve(null),
        publishedVersion ? metricAPI.getVersionUsage(record.id, publishedVersion.id) : Promise.resolve(emptyUsage()),
      ])
      const exactDatasetVersion = datasetResult.status === 'fulfilled' ? datasetResult.value : null
      const tableIDs = [...new Set(sourceNodes(exactDatasetVersion).map(node => node.tableId).filter(Boolean))]
      const tableResults = await Promise.allSettled(tableIDs.map(tableID => datasetAPI.table(tableID)))
      if (!active) return
      setDetail({
        record,
        publishedVersion,
        datasetVersion: exactDatasetVersion,
        lineage: lineageResult.status === 'fulfilled' ? lineageResult.value : null,
        sourceAssets: tableResults.flatMap(result => result.status === 'fulfilled' ? [result.value] : []),
        usage: usageResult.status === 'fulfilled' ? usageResult.value : emptyUsage(),
        publishedUnavailable,
        sourceUnavailable: Boolean(definition) && datasetResult.status === 'rejected',
        lineageUnavailable: Boolean(definition) && lineageResult.status === 'rejected',
        usageUnavailable: usageResult.status === 'rejected',
      })
    }).catch(cause => {
      if (active) setDetailError(cause instanceof Error ? `加载指标详情失败：${cause.message}` : '加载指标详情失败')
    }).finally(() => { if (active) setDetailLoading(false) })
    return () => { active = false }
  }, [selected])

  useEffect(() => {
    if (!selected) return
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setSelected(null)
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [selected])

  const datasetById = useMemo(() => new Map(datasets.map(dataset => [dataset.id, dataset])), [datasets])
  const ordinaryDatasets = useMemo(() => datasets.filter(dataset =>
    !dataset.originTableId && (dataset.layer === 'DWS' || dataset.layer === 'ADS')
  ), [datasets])
  const dataAssetDatasetIDs = useMemo(() => new Set(ordinaryDatasets.map(dataset => dataset.id)), [ordinaryDatasets])
  const displayMetrics = useMemo(() => metrics.flatMap(metric => {
    if (!dataAssetDatasetIDs.has(metric.datasetId)) return []
    if (!metric.syncManaged) return [metric]
    return [{
      ...metric,
      status: synchronizedAssetStatus(
        datasetById.get(metric.datasetId),
        metric.datasetVersionId,
      ),
    }]
  }), [dataAssetDatasetIDs, datasetById, metrics])
  const displayDimensions = useMemo(() => dimensions.flatMap(dimension => {
    if (!dataAssetDatasetIDs.has(dimension.datasetId)) return []
    if (dimension.status !== 'PUBLISHED') return [dimension]
    return [{
      ...dimension,
      status: synchronizedAssetStatus(
        datasetById.get(dimension.datasetId),
        dimension.datasetVersionId,
      ) as DimensionStatus,
    }]
  }), [dataAssetDatasetIDs, datasetById, dimensions])
  const filteredMetrics = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    if (type === 'DIMENSION') return []
    return displayMetrics.filter(metric => {
      const matchesQuery = !keyword || [metric.name, metric.code, metric.description]
        .some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))
      return matchesQuery &&
        (status === 'ALL' || metric.status === status) &&
        (datasetId === 'ALL' || metric.datasetId === datasetId)
    })
  }, [datasetId, displayMetrics, query, status, type])
  const filteredDimensions = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    if (type === 'METRIC') return []
    return displayDimensions.filter(dimension => {
      const matchesQuery = !keyword || [
        dimension.name, dimension.code, dimension.description, dimension.fieldCode,
      ].some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))
      return matchesQuery &&
        (status === 'ALL' || dimension.status === status) &&
        (datasetId === 'ALL' || dimension.datasetId === datasetId)
    })
  }, [datasetId, displayDimensions, query, status, type])
  const datasetSections = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    const metricsByDataset = new Map<string, MetricSummary[]>()
    const dimensionsByDataset = new Map<string, Dimension[]>()
    for (const metric of filteredMetrics) {
      const current = metricsByDataset.get(metric.datasetId) ?? []
      current.push(metric)
      metricsByDataset.set(metric.datasetId, current)
    }
    for (const dimension of filteredDimensions) {
      const current = dimensionsByDataset.get(dimension.datasetId) ?? []
      current.push(dimension)
      dimensionsByDataset.set(dimension.datasetId, current)
    }
    return ordinaryDatasets.flatMap(dataset => {
      if (datasetId !== 'ALL' && dataset.id !== datasetId) return []
      const datasetMatches = !keyword || [dataset.name, dataset.code, dataset.description]
        .some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))
      const datasetMetrics = metricsByDataset.get(dataset.id) ?? []
      const datasetDimensions = dimensionsByDataset.get(dataset.id) ?? []
      if (keyword && !datasetMatches && !datasetMetrics.length && !datasetDimensions.length) return []
      if ((status !== 'ALL' || type !== 'ALL') && !datasetMetrics.length && !datasetDimensions.length) return []
      return [{ dataset, metrics: datasetMetrics, dimensions: datasetDimensions }]
    })
  }, [datasetId, filteredDimensions, filteredMetrics, ordinaryDatasets, query, status, type])
  const counts = useMemo(() => ({
    published: displayMetrics.filter(metric => metric.status === 'PUBLISHED').length,
    dimensions: displayDimensions.filter(dimension => dimension.status === 'PUBLISHED').length,
    attention: displayMetrics.filter(metric => metric.status === 'STALE' || metric.status === 'DEPRECATED').length,
  }), [displayDimensions, displayMetrics])
  const filterActive = Boolean(query.trim()) || datasetId !== 'ALL' || status !== 'ALL' || type !== 'ALL'

  function resetFilters() {
    setQuery('')
    setStatus('ALL')
    setType('ALL')
    setDatasetId('ALL')
  }

  function openDetail(metric: MetricSummary) {
    setTab('overview')
    setSelected(metric)
  }
  function createMetricForDataset(dataset: DatasetSummary) {
    if (!dataset.currentPublishedVersionId) return
    setMetricRequirementError('')
    setMetricRequirement({ dataset, requirement: '' })
  }
  function continueMetricAuthoring(event: FormEvent) {
    event.preventDefault()
    if (!metricRequirement) return
    const requirement = metricRequirement.requirement.trim()
    if (!requirement) {
      setMetricRequirementError('请描述指标名称、业务口径、统计范围和期望分析方式。')
      return
    }
    const { dataset } = metricRequirement
    navigate('/metrics/new', {
      state: {
        metricAIRequirement: requirement,
        preferredDatasetId: dataset.id,
        safeDatasetExtension: true,
      },
    })
  }
  function createDimensionForDataset(dataset: DatasetSummary) {
    if (!dataset.currentPublishedVersionId) return
    setDimensionError('')
    setDimensionRequirement({ dataset, requirement: '' })
  }
  async function continueDimensionAuthoring(event: FormEvent) {
    event.preventDefault()
    if (!dimensionRequirement || dimensionBusy) return
    const requirement = dimensionRequirement.requirement.trim()
    if (!requirement) {
      setDimensionError('请描述要新增的维度字段及业务含义。')
      return
    }
    setDimensionBusy(true)
    setDimensionError('')
    const { dataset } = dimensionRequirement
    try {
      let targetDataset = dataset
      if (dataset.layer === 'ADS' && dataset.currentPublishedVersionId) {
        const lineage = await datasetAPI.getWarehouseLineage(dataset.currentPublishedVersionId)
        const upstreamDWS = [...lineage.topologicalOrder].reverse()
          .map(versionID => lineage.nodes.find(node => node.datasetVersionId === versionID))
          .find(node => node?.layer === 'DWS')
        const resolved = upstreamDWS ? datasetById.get(upstreamDWS.datasetId) : undefined
        if (resolved) targetDataset = resolved
      }
      const instruction = [
        `为数据资产“${dataset.name}”新增维度。业务要求：${requirement}`,
        '先定位相应的 DIM 表及需求字段，再沿完整血缘判断应在哪一步关联；必须评估粒度、基数、扇出、历史口径和下游影响。',
        '如果当前 DWS 可以安全承载，只生成最小化修改方案；如果影响较高、现有粒度不兼容或会改变既有指标口径，明确建议新建 DWS，不要强行覆盖现状。',
        '方案中保留维度业务名称、字段来源、关联键、关联步骤、影响范围、验证项和回滚边界，供人工审核后再应用。',
      ].join('\n')
      const route = targetDataset.layer === 'DWS'
        ? `/datasets/${targetDataset.id}/edit`
        : '/datasets'
      navigate(route, {
        state: {
          metricAIInstruction: instruction,
          assetAuthoringKind: 'DIMENSION',
          returnTo: '/assets/metrics',
        },
      })
    } catch (cause) {
      setDimensionError(cause instanceof Error ? cause.message : '读取数据集血缘失败，请稍后重试。')
      setDimensionBusy(false)
    }
  }
  function requestMetricDeletion(metric: MetricSummary) {
    setDeleteError('')
    setDeletingMetric(metric)
  }
  async function deleteMetric() {
    if (!deletingMetric || deleteBusy) return
    setDeleteBusy(true)
    setDeleteError('')
    try {
      await metricAPI.delete(deletingMetric.id, deletingMetric.version)
      setMetrics(current => current.filter(metric => metric.id !== deletingMetric.id))
      if (selected?.id === deletingMetric.id) setSelected(null)
      setDeletingMetric(null)
    } catch (cause) {
      setDeleteError(cause instanceof Error ? cause.message : '删除指标失败')
    } finally {
      setDeleteBusy(false)
    }
  }
  async function saveDimension(event: FormEvent) {
    event.preventDefault()
    if (!dimensionEditor || dimensionBusy) return
    const { dimension, draft } = dimensionEditor
    const sourceDataset = datasetById.get(dimension.datasetId)
    if (sourceDataset?.status !== 'PUBLISHED' ||
        sourceDataset.currentPublishedVersionId !== dimension.datasetVersionId) {
      setDimensionError('来源数据集当前不是已发布版本，维度已转为失效状态，不能继续修改。')
      return
    }
    if (!draft.name.trim() || !draft.code.trim()) {
      setDimensionError('维度名称和编码不能为空。')
      return
    }
    setDimensionBusy(true)
    setDimensionError('')
    try {
      const updated = await semanticGovernanceAPI.updateDimension(dimension.id, {
        expectedVersion: dimension.version,
        ...draft,
        status: 'PUBLISHED',
        code: draft.code.trim(),
        name: draft.name.trim(),
        description: draft.description.trim(),
      })
      setDimensions(current => current.map(item => item.id === updated.id ? updated : item))
      setSelectedDimension(current => current?.id === updated.id ? updated : current)
      setDimensionEditor(null)
    } catch (cause) {
      setDimensionError(cause instanceof Error ? cause.message : '保存维度失败')
    } finally {
      setDimensionBusy(false)
    }
  }
  async function deleteDimension() {
    if (!deletingDimension || dimensionBusy) return
    setDimensionBusy(true)
    setDimensionError('')
    try {
      const deprecated = await semanticGovernanceAPI.deprecateDimension(
        deletingDimension.id,
        deletingDimension.version,
      )
      setDimensions(current => current.map(item => item.id === deprecated.id ? deprecated : item))
      if (selectedDimension?.id === deprecated.id) setSelectedDimension(null)
      setDeletingDimension(null)
    } catch (cause) {
      setDimensionError(cause instanceof Error ? cause.message : '删除维度失败')
    } finally {
      setDimensionBusy(false)
    }
  }
  async function identifyMetricAssets() {
    if (identifying) return
    setIdentifying(true)
    setAssetSyncError('')
    setAssetSyncNotice('')
    try {
      const result = await metricCandidateAPI.identify()
      setAssetSyncNotice(
        `已扫描 ${result.eligibleDatasetCount} 个可治理数据集：提交 ${result.enqueuedJobCount} 个指标提炼任务，` +
        `同步 ${result.dimensionAssetCount} 个正式维度。LLM 将结合新增、修改、删除记录，仅保留关键指标。`,
      )
      setReloadKey(value => value + 1)
      window.setTimeout(() => setReloadKey(value => value + 1), 1800)
      window.setTimeout(() => setReloadKey(value => value + 1), 5000)
    } catch (cause) {
      setAssetSyncError(cause instanceof Error ? cause.message : '数据资产同步失败')
    } finally {
      setIdentifying(false)
    }
  }

  return <AppShell title="资产管理中心" eyebrow="数据资产 · 语义资产">
    <AssetManagementTabs />
    <section className="metric-directory" aria-label="数据资产目录">
      <header className="metric-directory-summary">
        <div>
          <span className="eyebrow">数据资产</span><h2>以数据集为单元管理指标与维度</h2>
          <p>每个 DWS / ADS 数据集分别维护指标清单与维度清单；新增资产先由 AI 检查血缘、授权字段和变更影响，再进入人工审核。</p>
          <button className="metric-identify-button" type="button" disabled={identifying} onClick={() => void identifyMetricAssets()}>
            <MagicWandIcon size={16} weight="bold" />{identifying ? '正在扫描并提炼资产…' : '同步全部数据资产'}
          </button>
        </div>
        <dl aria-label="数据资产统计">
          <div><dt>数据集</dt><dd>{ordinaryDatasets.length}</dd></div>
          <div><dt>指标</dt><dd>{displayMetrics.length}</dd></div>
          <div><dt>维度</dt><dd>{counts.dimensions}</dd></div>
          <div><dt>需关注</dt><dd>{counts.attention}</dd></div>
        </dl>
      </header>
      <div className="data-asset-sync-rule" role="note">
        <GitBranchIcon size={17} weight="bold" />
        <div><strong>全局同步规则</strong><span>扫描 DWS 及以上层级，提取表内指标与维度；把已新增、修改、删除的资产变更一并交给 LLM 对账，只沉淀关键且有业务价值的指标。</span></div>
      </div>

      <div className="metric-directory-filters" aria-label="数据资产筛选">
        <label className="metric-search-field"><span>搜索数据资产</span><span className="metric-search-input"><MagnifyingGlassIcon size={17} /><input aria-label="搜索数据集、指标或维度" value={query} placeholder="搜索数据集、指标、维度名称或编码" onChange={event => setQuery(event.target.value)} /></span></label>
        <label><span>资产状态</span><select aria-label="资产状态" value={status} onChange={event => setStatus(event.target.value)}><option value="ALL">全部状态</option>{Object.entries(statusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <label><span>资产类型</span><select aria-label="资产类型筛选" value={type} onChange={event => setType(event.target.value)}><option value="ALL">指标与维度</option><option value="METRIC">仅指标</option><option value="DIMENSION">仅维度</option></select></label>
        <label><span>来源数据集</span><select aria-label="来源数据集" value={datasetId} onChange={event => setDatasetId(event.target.value)}><option value="ALL">全部数据集</option>{ordinaryDatasets.map(dataset => <option key={dataset.id} value={dataset.id}>{dataset.name}</option>)}</select></label>
        <button className="metric-reset-filters" type="button" disabled={!filterActive} onClick={resetFilters}>重置</button>
      </div>

      <div className="metric-directory-resultbar"><div><strong>数据集资产单元</strong><span>指标与维度使用不同颜色，所有变更保留版本与审计边界</span></div><small>显示 {datasetSections.length} / {ordinaryDatasets.length} 个数据集</small></div>
      {assetSyncNotice && <div className="metric-directory-toast" role="status"><span>{assetSyncNotice}</span><button type="button" aria-label="关闭提示" onClick={() => setAssetSyncNotice('')}>×</button></div>}
      {(error || assetSyncError) && <div className="metric-directory-error" role="alert"><span>{error || assetSyncError}</span><button type="button" onClick={() => setReloadKey(value => value + 1)}>重新加载</button></div>}
      {loading ? <div className="metric-directory-empty" role="status"><FunctionIcon size={34} /><strong>正在加载数据资产…</strong></div> : datasetSections.length ? <div className="metric-dataset-zones">
        {datasetSections.map(({ dataset, metrics: datasetMetrics, dimensions: datasetDimensions }) => <section className="metric-dataset-zone" key={dataset.id} aria-label={`${dataset.name}数据资产`}>
          <header>
            <div className="metric-dataset-identity"><span aria-hidden="true"><DatabaseIcon size={21} weight="duotone" /></span><div><div className="data-asset-dataset-title"><h3>{dataset.name}</h3><em>{dataset.layer}</em></div><p>{dataset.description || '暂无数据集说明'}</p><small>{dataset.code} · {dataset.status === 'PUBLISHED' ? '已发布 DAG' : statusLabels[dataset.status] ?? dataset.status}</small></div></div>
            <div className="metric-dataset-zone-actions"><span>{datasetMetrics.length} 个指标 · {datasetDimensions.length} 个维度</span></div>
          </header>
          <div className="metric-dataset-safety"><GitBranchIcon size={15} weight="bold" /><span>新增指标会先判断当前血缘能否满足口径；不满足时检索授权 DWD 字段并判断修改或新建 DWS。新增维度会定位 DIM 表、选择关联步骤并评估影响。</span></div>
          <div className="data-asset-columns">
            <section className="data-asset-list metric-assets" aria-label={`${dataset.name}指标清单`}>
              <header><div><FunctionIcon size={18} weight="bold" /><span><strong>指标清单</strong><small>{datasetMetrics.length} 项 · 蓝色标识</small></span></div><button type="button" disabled={!dataset.currentPublishedVersionId} onClick={() => createMetricForDataset(dataset)}><PlusIcon size={14} weight="bold" />新增指标</button></header>
              {datasetMetrics.length ? <div>{datasetMetrics.map(metric => <article className="data-asset-row" key={metric.id}>
                <span className="data-asset-row-icon" aria-hidden="true"><FunctionIcon size={18} weight="bold" /></span>
                <div className="data-asset-row-main"><div><button type="button" onClick={() => openDetail(metric)}>{metric.name}</button><span className={`metric-status status-${metric.status.toLowerCase()}`}>{statusLabels[metric.status] ?? metric.status}</span></div><p>{metric.description || '暂无指标说明'}</p><small>{metric.code} · {typeLabels[metric.type] ?? metric.type}</small></div>
                <div className="data-asset-row-actions">
                  <AssetSharingSelect resourceType="METRIC" resourceID={metric.id} value={metric.sharingScope || 'PRIVATE'} ownerUserID={metric.ownerUserId} assetDomainID={metric.domainId} onChange={sharingScope => setMetrics(current => current.map(item => item.id === metric.id ? { ...item, sharingScope } : item))} />
                  <button className="action-edit" type="button" disabled={metric.syncManaged && metric.status !== 'PUBLISHED'} title={metric.syncManaged && metric.status !== 'PUBLISHED' ? '来源数据集当前未发布，不能修改同步指标' : undefined} onClick={() => navigate(`/metrics/${metric.id}/edit`)}>编辑</button><button className="action-delete" type="button" onClick={() => requestMetricDeletion(metric)}>删除</button>
                </div>
              </article>)}</div> : <div className="data-asset-list-empty"><FunctionIcon size={22} /><span><strong>暂无指标</strong><small>描述业务口径后由 AI 检查血缘与字段。</small></span></div>}
            </section>
            <section className="data-asset-list dimension-assets" aria-label={`${dataset.name}维度清单`}>
              <header><div><TableIcon size={18} weight="bold" /><span><strong>维度清单</strong><small>{datasetDimensions.length} 项 · 紫色标识</small></span></div><button type="button" disabled={!dataset.currentPublishedVersionId} onClick={() => createDimensionForDataset(dataset)}><PlusIcon size={14} weight="bold" />新增维度</button></header>
              {datasetDimensions.length ? <div>{datasetDimensions.map(dimension => <article className="data-asset-row" key={dimension.id}>
                <span className="data-asset-row-icon" aria-hidden="true"><TableIcon size={18} weight="bold" /></span>
                <div className="data-asset-row-main"><div><button type="button" onClick={() => setSelectedDimension(dimension)}>{dimension.name}</button><span className={`metric-status status-${dimension.status.toLowerCase()}`}>{statusLabels[dimension.status] ?? dimension.status}</span></div><p>{dimension.description || '暂无维度说明'}</p><small>{dimension.code} · {dimensionTypeLabels[dimension.dimensionType]}</small></div>
                <div className="data-asset-row-actions">
                  <AssetSharingSelect resourceType="DIMENSION" resourceID={dimension.id} value={dimension.sharingScope || 'PRIVATE'} ownerUserID={dimension.createdBy} assetDomainID={dimension.domainId} onChange={sharingScope => setDimensions(current => current.map(item => item.id === dimension.id ? { ...item, sharingScope } : item))} />
                  <button className="action-edit" type="button" disabled={dimension.status !== 'PUBLISHED'} title={dimension.status !== 'PUBLISHED' ? '来源数据集当前未发布，不能修改同步维度' : undefined} onClick={() => { setDimensionError(''); setDimensionEditor({ dimension, draft: { code: dimension.code, name: dimension.name, description: dimension.description, dimensionType: dimension.dimensionType, memberIndexPolicy: dimension.memberIndexPolicy, highCardinality: dimension.highCardinality, sensitive: dimension.sensitive, status: dimension.status } }) }}>编辑</button><button className="action-delete" type="button" onClick={() => { setDimensionError(''); setDeletingDimension(dimension) }}>删除</button>
                </div>
              </article>)}</div> : <div className="data-asset-list-empty"><TableIcon size={22} /><span><strong>暂无维度</strong><small>AI 会先定位 DIM 表并评估关联影响。</small></span></div>}
            </section>
          </div>
        </section>)}
      </div> : <DirectoryEmpty hasItems={ordinaryDatasets.length > 0} filterActive={filterActive} onReset={resetFilters} />}
    </section>

    {deletingMetric && <div className="metric-delete-backdrop" role="presentation" onMouseDown={event => {
      if (event.target === event.currentTarget && !deleteBusy) setDeletingMetric(null)
    }}>
      <section className="metric-delete-dialog" role="dialog" aria-modal="true" aria-labelledby="metric-delete-title">
        <span>危险操作</span>
        <h2 id="metric-delete-title">删除指标</h2>
        <p>确认删除“<strong>{deletingMetric.name}</strong>”吗？指标会从展示中心移除，历史版本和审计记录仍会保留。</p>
        <small>仍被报告、下游指标或运行中查询占用时，系统会拒绝删除。删除后可以重新创建相同编码的指标。</small>
        {deleteError && <div className="metric-delete-error" role="alert">{deleteError}</div>}
        <footer><button className="quiet-button" type="button" disabled={deleteBusy} onClick={() => setDeletingMetric(null)}>取消</button><button className="danger-button" type="button" disabled={deleteBusy} onClick={() => void deleteMetric()}>{deleteBusy ? '正在删除…' : '确认删除'}</button></footer>
      </section>
    </div>}
    {metricRequirement && <div className="data-asset-dialog-backdrop" role="presentation" onMouseDown={event => {
      if (event.target === event.currentTarget) setMetricRequirement(null)
    }}>
      <form className="data-asset-dialog metric-requirement-dialog" role="dialog" aria-modal="true" aria-labelledby="metric-requirement-title" onSubmit={continueMetricAuthoring}>
        <header><div><span className="eyebrow">AI 指标建模</span><h2 id="metric-requirement-title">为“{metricRequirement.dataset.name}”新增指标</h2></div><button type="button" aria-label="关闭新增指标" onClick={() => setMetricRequirement(null)}><XIcon size={18} weight="bold" /></button></header>
        <section className="dimension-ai-flow metric-ai-flow" aria-label="新增指标分析步骤">
          <article><span>1</span><div><strong>校验数据集血缘</strong><small>分析当前发布 DAG、粒度和已有字段能否满足指标口径</small></div></article>
          <article><span>2</span><div><strong>检索授权 DWD</strong><small>不满足时，在当前领域有权限的 DWD 表中定位需求字段</small></div></article>
          <article><span>3</span><div><strong>决策 DWS 方案</strong><small>由 LLM 判断修改现有 DWS 或新建 DWS，并完善需求材料</small></div></article>
        </section>
        <label>指标需求<textarea autoFocus rows={5} maxLength={4000} value={metricRequirement.requirement} placeholder="例如：创建“有效订单销售额”，统计已支付且未退款订单的含税金额，按支付完成月份汇总，并支持按地区下钻。" onChange={event => {
          setMetricRequirementError('')
          setMetricRequirement({ ...metricRequirement, requirement: event.target.value })
        }} /></label>
        <div className="dimension-ai-boundary metric-ai-boundary" role="note"><GitBranchIcon size={17} weight="bold" /><span>AI 会先判断当前数据集能否安全承载；需要扩展时，将生成可审核的字段来源、口径材料和 DAG 修改方案。现有粒度不兼容或影响较高时，必须转为新建 DWS 建议。</span></div>
        {metricRequirementError && <div className="metric-delete-error" role="alert">{metricRequirementError}</div>}
        <footer><button className="quiet-button" type="button" onClick={() => setMetricRequirement(null)}>取消</button><button className="primary-button" type="submit" disabled={!metricRequirement.requirement.trim()}>开始 AI 分析</button></footer>
      </form>
    </div>}
    {dimensionRequirement && <div className="data-asset-dialog-backdrop" role="presentation" onMouseDown={event => {
      if (event.target === event.currentTarget && !dimensionBusy) setDimensionRequirement(null)
    }}>
      <form className="data-asset-dialog dimension-requirement-dialog" role="dialog" aria-modal="true" aria-labelledby="dimension-requirement-title" onSubmit={event => void continueDimensionAuthoring(event)}>
        <header><div><span className="eyebrow">AI 维度建模</span><h2 id="dimension-requirement-title">为“{dimensionRequirement.dataset.name}”新增维度</h2></div><button type="button" aria-label="关闭新增维度" disabled={dimensionBusy} onClick={() => setDimensionRequirement(null)}><XIcon size={18} weight="bold" /></button></header>
        <section className="dimension-ai-flow" aria-label="新增维度分析步骤">
          <article><span>1</span><div><strong>定位 DIM 表</strong><small>在当前领域与授权范围内检索对应实体和需求字段</small></div></article>
          <article><span>2</span><div><strong>判断关联步骤</strong><small>沿血缘选择关联位置，检查粒度、基数与扇出</small></div></article>
          <article><span>3</span><div><strong>评估影响</strong><small>由 LLM 判断最小修改现有 DWS，还是建议新建 DWS</small></div></article>
        </section>
        <label>维度需求<textarea autoFocus rows={5} maxLength={4000} value={dimensionRequirement.requirement} placeholder="例如：新增“客户所属智家生态圈”维度，取客户当前有效归属，支持按生态圈下钻；历史订单按下单时归属统计。" onChange={event => setDimensionRequirement({ ...dimensionRequirement, requirement: event.target.value })} /></label>
        <div className="dimension-ai-boundary" role="note"><GitBranchIcon size={17} weight="bold" /><span>AI 只生成待审核的 DAG 方案，不会直接覆盖现有数据集。高影响、粒度冲突或可能改变既有指标口径时，方案必须转为新建 DWS 建议。</span></div>
        {dimensionError && <div className="metric-delete-error" role="alert">{dimensionError}</div>}
        <footer><button className="quiet-button" type="button" disabled={dimensionBusy} onClick={() => setDimensionRequirement(null)}>取消</button><button className="primary-button" type="submit" disabled={dimensionBusy || !dimensionRequirement.requirement.trim()}>{dimensionBusy ? '正在读取血缘…' : '开始 AI 分析'}</button></footer>
      </form>
    </div>}
    {selectedDimension && <div className="data-asset-dialog-backdrop" role="presentation" onMouseDown={event => {
      if (event.target === event.currentTarget) setSelectedDimension(null)
    }}>
      <section className="data-asset-dialog dimension-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="dimension-detail-title">
        <header><div><span className="eyebrow">维度资产</span><h2 id="dimension-detail-title">{selectedDimension.name}</h2></div><button type="button" aria-label="关闭维度详情" onClick={() => setSelectedDimension(null)}><XIcon size={18} weight="bold" /></button></header>
        <div className="dimension-detail-hero"><span><TableIcon size={24} weight="bold" /></span><div><strong>{selectedDimension.name}</strong><code>{selectedDimension.code}</code><p>{selectedDimension.description || '暂无维度说明'}</p></div></div>
        <dl className="dimension-detail-facts">
          <div><dt>维度类型</dt><dd>{dimensionTypeLabels[selectedDimension.dimensionType]}</dd></div>
          <div><dt>来源字段</dt><dd>{selectedDimension.fieldCode}</dd></div>
          <div><dt>成员策略</dt><dd>{memberPolicyLabels[selectedDimension.memberIndexPolicy]}</dd></div>
          <div><dt>成员数量</dt><dd>{selectedDimension.memberCount ?? '—'}</dd></div>
          <div><dt>数据集版本</dt><dd title={selectedDimension.datasetVersionId}>{shortId(selectedDimension.datasetVersionId)}</dd></div>
          <div><dt>风险标记</dt><dd>{[selectedDimension.highCardinality ? '高基数' : '', selectedDimension.sensitive ? '敏感' : ''].filter(Boolean).join(' · ') || '无'}</dd></div>
        </dl>
        <DimensionValuePreview key={`${selectedDimension.id}-${selectedDimension.datasetVersionId}`} dimension={selectedDimension} />
        <div className="dimension-detail-note"><GitBranchIcon size={16} weight="bold" /><span>预览固定到该维度绑定的精确数据集版本，并继承数据权限与字段脱敏规则；预览不会修改维度定义。</span></div>
        <footer><button className="quiet-button" type="button" onClick={() => setSelectedDimension(null)}>关闭</button><button className="primary-button" type="button" onClick={() => { const dimension = selectedDimension; setSelectedDimension(null); setDimensionError(''); setDimensionEditor({ dimension, draft: { code: dimension.code, name: dimension.name, description: dimension.description, dimensionType: dimension.dimensionType, memberIndexPolicy: dimension.memberIndexPolicy, highCardinality: dimension.highCardinality, sensitive: dimension.sensitive, status: dimension.status } }) }}>编辑维度</button></footer>
      </section>
    </div>}
    {dimensionEditor && <div className="data-asset-dialog-backdrop" role="presentation">
      <form className="data-asset-dialog dimension-editor-dialog" role="dialog" aria-modal="true" aria-labelledby="dimension-editor-title" onSubmit={event => void saveDimension(event)}>
        <header><div><span className="eyebrow">受控定义编辑</span><h2 id="dimension-editor-title">编辑维度</h2></div><button type="button" aria-label="关闭维度编辑" disabled={dimensionBusy} onClick={() => setDimensionEditor(null)}><XIcon size={18} weight="bold" /></button></header>
        <div className="dimension-editor-grid">
          <label>维度名称<input value={dimensionEditor.draft.name} onChange={event => setDimensionEditor({ ...dimensionEditor, draft: { ...dimensionEditor.draft, name: event.target.value } })} /></label>
          <label>维度编码<input value={dimensionEditor.draft.code} onChange={event => setDimensionEditor({ ...dimensionEditor, draft: { ...dimensionEditor.draft, code: event.target.value } })} /></label>
          <label>维度类型<select value={dimensionEditor.draft.dimensionType} onChange={event => setDimensionEditor({ ...dimensionEditor, draft: { ...dimensionEditor.draft, dimensionType: event.target.value as DimensionType } })}>{Object.entries(dimensionTypeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label>成员索引策略<select value={dimensionEditor.draft.memberIndexPolicy} onChange={event => setDimensionEditor({ ...dimensionEditor, draft: { ...dimensionEditor.draft, memberIndexPolicy: event.target.value as MemberIndexPolicy } })}>{Object.entries(memberPolicyLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label className="wide">业务说明<textarea rows={4} value={dimensionEditor.draft.description} onChange={event => setDimensionEditor({ ...dimensionEditor, draft: { ...dimensionEditor.draft, description: event.target.value } })} /></label>
          <label className="dimension-checkbox"><input type="checkbox" checked={dimensionEditor.draft.highCardinality} onChange={event => setDimensionEditor({ ...dimensionEditor, draft: { ...dimensionEditor.draft, highCardinality: event.target.checked } })} /><span>高基数维度</span></label>
          <label className="dimension-checkbox"><input type="checkbox" checked={dimensionEditor.draft.sensitive} onChange={event => setDimensionEditor({ ...dimensionEditor, draft: { ...dimensionEditor.draft, sensitive: event.target.checked } })} /><span>敏感维度</span></label>
        </div>
        <div className="dimension-ai-boundary" role="note">来源字段、绑定数据集和精确版本不可在此处变更；涉及血缘或字段调整时，请使用“新增维度”的 AI 建模流程。</div>
        {dimensionError && <div className="metric-delete-error" role="alert">{dimensionError}</div>}
        <footer><button className="quiet-button" type="button" disabled={dimensionBusy} onClick={() => setDimensionEditor(null)}>取消</button><button className="primary-button" type="submit" disabled={dimensionBusy}>{dimensionBusy ? '正在保存…' : '保存修改'}</button></footer>
      </form>
    </div>}
    {deletingDimension && <div className="metric-delete-backdrop" role="presentation">
      <section className="metric-delete-dialog" role="dialog" aria-modal="true" aria-labelledby="dimension-delete-title">
        <span>危险操作</span><h2 id="dimension-delete-title">删除维度</h2>
        <p>确认删除“<strong>{deletingDimension.name}</strong>”吗？该维度会从生效目录移除，不再参与新的语义解析。</p>
        <small>为保护历史报告和审计，系统会将维度标记为已废弃，不物理清除历史定义、成员快照与兼容记录。</small>
        {dimensionError && <div className="metric-delete-error" role="alert">{dimensionError}</div>}
        <footer><button className="quiet-button" type="button" disabled={dimensionBusy} onClick={() => setDeletingDimension(null)}>取消</button><button className="danger-button" type="button" disabled={dimensionBusy} onClick={() => void deleteDimension()}>{dimensionBusy ? '正在删除…' : '确认删除'}</button></footer>
      </section>
    </div>}
    {selected && <MetricDetailDialog summary={selected} detail={detail} loading={detailLoading} error={detailError} tab={tab} datasetName={datasetById.get((detail?.publishedVersion?.definition ?? detail?.record.definition)?.datasetId ?? selected.datasetId)?.name ?? ''} semanticDimensions={dimensions} onTab={setTab} onClose={() => setSelected(null)} onEdit={() => navigate(`/metrics/${selected.id}/edit`)} />}
  </AppShell>
}

function DimensionValuePreview({ dimension }: { dimension: Dimension }) {
  const [reloadToken, setReloadToken] = useState(0)
  const [preview, setPreview] = useState<DatasetPreview | null>(null)
  const [summary, setSummary] = useState<DimensionValuePreviewSummary | null>(null)
  const [loading, setLoading] = useState(!dimension.sensitive)
  const [error, setError] = useState('')

  useEffect(() => {
    if (dimension.sensitive) return undefined
    let active = true
    queueMicrotask(() => {
      if (!active) return
      setLoading(true)
      setError('')
    })
    datasetAPI.previewVersion(
      dimension.datasetId,
      dimension.datasetVersionId,
      globalThis.crypto.randomUUID(),
      {},
      dimensionPreviewSampleSize,
    ).then(result => {
      if (!active) return
      setPreview(result)
      setSummary(summarizeDimensionValuePreview(result, dimension))
    }).catch(cause => {
      if (!active) return
      setPreview(null)
      setSummary(null)
      setError(cause instanceof Error ? cause.message : '加载维度值预览失败')
    }).finally(() => {
      if (active) setLoading(false)
    })
    return () => { active = false }
  }, [
    dimension,
    dimension.datasetId,
    dimension.datasetVersionId,
    dimension.fieldCode,
    dimension.fieldId,
    dimension.sensitive,
    reloadToken,
  ])

  const maxCount = summary?.items[0]?.count ?? 0

  return <section className="dimension-value-preview" aria-label="高频有效维度值预览">
    <header>
      <div><span className="eyebrow">数据库样本</span><h3>高频有效维度值预览</h3><p>读取精确版本前 {dimensionPreviewSampleSize} 行，排除空值及建模占位值后，按样本出现次数排序。</p></div>
      {!dimension.sensitive && <button className="quiet-button" type="button" disabled={loading} onClick={() => setReloadToken(value => value + 1)}><ArrowClockwiseIcon className={loading ? 'spinning' : ''} size={14} />{loading ? '查询中…' : '重新查询'}</button>}
    </header>
    {dimension.sensitive ? <div className="dimension-value-preview-state protected"><DatabaseIcon size={24} /><div><strong>敏感维度不提供值预览</strong><span>为避免泄露业务值，本页面不会发起维度值查询。</span></div></div> : loading && !summary ? <div className="dimension-value-preview-state" role="status"><ArrowClockwiseIcon className="spinning" size={22} /><div><strong>正在直连数据库查询…</strong><span>查询仅针对该维度绑定的精确版本。</span></div></div> : error ? <div className="dimension-value-preview-state error" role="alert"><DatabaseIcon size={24} /><div><strong>维度值预览暂不可用</strong><span>{error}</span></div></div> : summary && preview ? <>
      <dl className="dimension-value-preview-stats">
        <div><dt>样本行</dt><dd>{summary.sampledRows}</dd></div>
        <div><dt>有效值行</dt><dd>{summary.effectiveRows}</dd></div>
        <div><dt>样本去重值</dt><dd>{summary.distinctValues}</dd></div>
        <div><dt>排除值行</dt><dd>{summary.excludedRows}</dd></div>
        <div><dt>查询耗时</dt><dd>{preview.durationMs} ms</dd></div>
      </dl>
      {summary.items.length ? <div className="dimension-value-frequency-list">
        <div className="heading"><span>有效维度值</span><span>样本频次</span></div>
        {summary.items.map(item => {
          const percentage = summary.effectiveRows > 0
            ? item.count / summary.effectiveRows * 100
            : 0
          return <div className="dimension-value-frequency-row" key={item.label}>
            <strong title={item.label}>{item.label}</strong>
            <span className="dimension-value-frequency-bar" aria-hidden="true"><i style={{ width: `${maxCount ? item.count / maxCount * 100 : 0}%` }} /></span>
            <span>{item.count} 次 · {percentage.toFixed(1)}%</span>
          </div>
        })}
      </div> : <div className="dimension-value-preview-state"><DatabaseIcon size={24} /><div><strong>样本中没有有效维度值</strong><span>空值及 UNKNOWN、999999999、1970-01-01 等建模占位值不会进入预览。</span></div></div>}
    </> : null}
  </section>
}

function DirectoryEmpty({ hasItems, filterActive, onReset }: { hasItems: boolean; filterActive: boolean; onReset: () => void }) {
  return <div className="metric-directory-empty"><MagnifyingGlassIcon size={38} /><strong>{hasItems ? '没有符合条件的数据资产' : '还没有可展示的 DWS / ADS 数据集'}</strong><p>{hasItems ? '调整搜索词或筛选条件后再试。' : '先创建并发布 DWS 或 ADS 数据集，再同步指标与维度。'}</p>{filterActive && <button className="quiet-button" type="button" onClick={onReset}>清除筛选</button>}</div>
}

function MetricDetailDialog({ summary, detail, loading, error, tab, datasetName, semanticDimensions, onTab, onClose, onEdit }: {
  summary: MetricSummary
  detail: MetricDetail | null
  loading: boolean
  error: string
  tab: DetailTab
  datasetName: string
  semanticDimensions: Dimension[]
  onTab: (tab: DetailTab) => void
  onClose: () => void
  onEdit: () => void
}) {
  const publishedPointer = Boolean(detail?.record.currentPublishedVersionId ?? summary.currentPublishedVersionId)
  const definition = detail?.publishedVersion?.definition ?? (publishedPointer ? undefined : detail?.record.definition)
  const fields = datasetFields(detail?.datasetVersion ?? null)
  const nodes = sourceNodes(detail?.datasetVersion ?? null)
  const versionLabel = detail?.publishedVersion ? `V${detail.publishedVersion.versionNo} · 当前发布` : publishedPointer ? '发布版本不可读取' : '当前草稿'
  const tabs: Array<{ id: DetailTab; label: string }> = [
    { id: 'overview', label: '概览' },
    { id: 'definition', label: '口径' },
    { id: 'dimensions', label: '维度' },
    { id: 'source', label: '来源' },
    { id: 'lineage', label: '血缘' },
    { id: 'preview', label: '预览' },
  ]
  return <div className="metric-detail-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
    <section className="metric-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="metric-detail-title">
      <header><div><span className="eyebrow">指标核心信息</span><h2 id="metric-detail-title">指标详情</h2></div><button type="button" aria-label="关闭指标详情" onClick={onClose}><XIcon size={18} weight="bold" /></button></header>
      <div className="metric-detail-content">
        <section className="metric-detail-identity">
          <div><div className="metric-detail-badges"><span className={`metric-status status-${summary.status.toLowerCase()}`}>{statusLabels[summary.status] ?? summary.status}</span><span>{typeLabels[summary.type] ?? summary.type}</span><span>{versionLabel}</span></div><h3>{detail?.record.name ?? summary.name}</h3><code>{detail?.record.code ?? summary.code}</code><p>{detail?.record.description || summary.description || '暂无指标说明'}</p></div>
          {definition && <dl><div><dt>真实计算</dt><dd>{aggregationSummary(definition)}</dd></div><div><dt>单位</dt><dd>{definition.unit || '—'}</dd></div><div><dt>时间粒度</dt><dd>{timeGrainLabels[definition.timeGrain] ?? definition.timeGrain}</dd></div><div><dt>允许维度</dt><dd>{definition.allowedDimensions.length}</dd></div></dl>}
        </section>
        <nav className="metric-detail-tabs" role="tablist" aria-label="指标详情信息"><div>{tabs.map(item => <button key={item.id} id={`metric-tab-${item.id}`} role="tab" aria-selected={tab === item.id} aria-controls={`metric-panel-${item.id}`} type="button" onClick={() => onTab(item.id)}>{item.label}</button>)}</div></nav>
        <div className="metric-detail-panel" role="tabpanel" id={`metric-panel-${tab}`} aria-labelledby={`metric-tab-${tab}`}>
          {loading && <div className="metric-detail-state" role="status"><FunctionIcon size={32} /><strong>正在读取精确指标信息…</strong></div>}
          {!loading && error && <div className="metric-detail-state error" role="alert"><strong>{error}</strong><p>关闭后可重新打开详情重试。</p></div>}
          {!loading && !error && detail?.publishedUnavailable && <div className="metric-detail-state error" role="alert"><strong>精确发布版本暂时不可读取</strong><p>为避免把草稿误认为发布口径，详情已停止展示；关闭后可重新打开重试。</p></div>}
          {!loading && !error && detail && definition && tab === 'overview' && <MetricOverview detail={detail} definition={definition} datasetName={datasetName} />}
          {!loading && !error && detail && definition && tab === 'definition' && <MetricDefinitionView detail={detail} definition={definition} fields={fields} />}
          {!loading && !error && detail && definition && tab === 'dimensions' && <MetricDimensionsView definition={definition} fields={fields} />}
          {!loading && !error && detail && definition && tab === 'preview' && <MetricPreviewView key={`${detail.record.id}-${detail.publishedVersion?.id ?? detail.record.draftVersionId}`} detail={detail} definition={definition} fields={fields} semanticDimensions={semanticDimensions} />}
          {!loading && !error && detail && definition && tab === 'source' && <MetricSourceView detail={detail} definition={definition} datasetName={datasetName} fields={fields} nodes={nodes} />}
          {!loading && !error && detail && definition && tab === 'lineage' && <MetricLineageView detail={detail} definition={definition} datasetName={datasetName} fields={fields} nodes={nodes} />}
        </div>
      </div>
      <footer><span>{detail?.publishedVersion ? '正在查看不可变发布版本' : publishedPointer ? '精确发布版本读取失败，未回退到草稿' : '正在查看当前草稿口径'}</span><div><button className="quiet-button" type="button" onClick={onClose}>关闭</button><button className="primary-button" type="button" onClick={onEdit}>进入编辑</button></div></footer>
    </section>
  </div>
}

function MetricOverview({ detail, definition, datasetName }: { detail: MetricDetail; definition: MetricDefinition; datasetName: string }) {
  return <div className="metric-overview-grid">
    {(detail.publishedUnavailable || detail.sourceUnavailable) && <div className="metric-detail-notice" role="note">部分精确版本元数据当前不可读，页面仅展示已授权的指标事实，不会自动切换到其他版本。</div>}
    <section className="metric-detail-section"><span className="eyebrow">业务定义</span><h4>这个指标代表什么</h4><p>{detail.record.description || '暂未维护业务说明，建议在编辑页补充适用场景与口径边界。'}</p><dl className="metric-fact-grid"><div><dt>指标编码</dt><dd>{detail.record.code}</dd></div><div><dt>指标类型</dt><dd>{typeLabels[detail.record.type] ?? detail.record.type}</dd></div><div><dt>聚合主版本</dt><dd>V{detail.record.version}</dd></div><div><dt>更新时间</dt><dd>{formatDate(detail.record.updatedAt)}</dd></div></dl></section>
    <section className="metric-detail-section"><span className="eyebrow">口径摘要</span><h4>{aggregationSummary(definition)} · {definition.unit || '无单位'}</h4><dl className="metric-fact-grid"><div><dt>数字格式</dt><dd>{definition.numberFormat}</dd></div><div><dt>小数位</dt><dd>{definition.decimalScale}</dd></div><div><dt>可加性</dt><dd>{additivityLabels[definition.additivity] ?? definition.additivity}</dd></div><div><dt>时间粒度</dt><dd>{timeGrainLabels[definition.timeGrain] ?? definition.timeGrain}</dd></div></dl></section>
    <section className="metric-detail-section metric-overview-source"><span className="eyebrow">精确来源</span><h4>{datasetName || shortId(definition.datasetId)}</h4><p>指标固定到数据集版本 <code>{shortId(definition.datasetVersionId)}</code>，不会静默跟随其他版本。</p></section>
  </div>
}

function MetricDefinitionView({ detail, definition, fields }: { detail: MetricDetail; definition: MetricDefinition; fields: DatasetField[] }) {
  return <div className="metric-definition-view">
    <section className="metric-formula-card"><span className="eyebrow">业务可读口径</span><h4>{definition.metric.name}</h4><div className="metric-formula"><FunctionIcon size={22} weight="bold" /><code>{formulaLabel(definition, fields)}</code></div><p>{definition.metric.description || '暂无口径说明'}</p></section>
    <section className="metric-detail-section"><span className="eyebrow">执行语义</span><h4>计算与展示规则</h4><dl className="metric-semantics-grid"><div><dt>真实聚合</dt><dd>{aggregationSummary(definition)}</dd></div><div><dt>查询层处理</dt><dd>{definition.sourceCalculation ? '直接读取聚合结果，不再二次聚合' : aggregationSummary(definition)}</dd></div><div><dt>可加性</dt><dd>{additivityLabels[definition.additivity] ?? definition.additivity}</dd></div><div><dt>单位 / 格式</dt><dd>{definition.unit || '—'} · {definition.numberFormat}</dd></div><div><dt>精度 / 舍入</dt><dd>{definition.decimalScale} 位 · {definition.roundingMode}</dd></div><div><dt>空值处理</dt><dd>{definition.nullHandling}</dd></div><div><dt>除零处理</dt><dd>{definition.divisionByZero}</dd></div>{definition.sourceCalculation?.valueBehavior && <div><dt>值行为</dt><dd>{valueBehaviorLabels[definition.sourceCalculation.valueBehavior] ?? definition.sourceCalculation.valueBehavior}</dd></div>}{definition.sourceCalculation?.timeAggregation && <div><dt>跨时间规则</dt><dd>{timeAggregationLabels[definition.sourceCalculation.timeAggregation] ?? definition.sourceCalculation.timeAggregation}</dd></div>}</dl></section>
    <details className="metric-catalog-json"><summary>查看完整定义 JSON</summary><pre aria-label="指标详情完整定义 JSON">{JSON.stringify(detail.publishedVersion?.definition ?? detail.record.definition, null, 2)}</pre></details>
  </div>
}

function MetricDimensionsView({ definition, fields }: { definition: MetricDefinition; fields: DatasetField[] }) {
  return <div className="metric-dimensions-view">
    <header><div><span className="eyebrow">允许分组范围</span><h4>共 {definition.allowedDimensions.length} 个维度</h4></div><p>只有当前精确指标版本声明的维度才可用于分组与下钻。</p></header>
    {definition.allowedDimensions.length ? <div className="metric-dimension-table"><table><thead><tr><th>显示名称</th><th>来源字段</th><th>层级</th><th>排序</th><th>空值标签</th><th>可加性约束</th></tr></thead><tbody>{definition.allowedDimensions.map(dimension => <tr key={dimension.fieldId}><td><strong>{dimension.name}</strong></td><td>{fieldName(fields, dimension.fieldId)}</td><td>{dimension.hierarchyFieldIds.length ? dimension.hierarchyFieldIds.map(id => fieldName(fields, id)).join(' → ') : '—'}</td><td>{dimension.sortDirection}</td><td>{dimension.nullLabel || '—'}</td><td>{definition.nonAdditiveDimensionFieldIds.includes(dimension.fieldId) ? '不可直接求和' : '继承指标规则'}</td></tr>)}</tbody></table></div> : <div className="metric-detail-state"><StackIcon size={34} /><strong>当前指标未开放分组维度</strong><p>报告使用时只能查看指标总值。</p></div>}
  </div>
}

type MetricPreviewFilterDraft = {
  query: string
  mode: 'ALL' | 'INCLUDE' | 'EXCLUDE'
  values: string[]
}

type MetricPreviewMemberOptions = {
  items: DimensionMember[]
  total: number
  query: string
  loading: boolean
  error: string
}

function MetricPreviewView({ detail, definition, fields, semanticDimensions }: {
  detail: MetricDetail
  definition: MetricDefinition
  fields: DatasetField[]
  semanticDimensions: Dimension[]
}) {
  const availableDimensions = definition.allowedDimensions
  const dimensionFieldIds = useMemo(
    () => availableDimensions.map(dimension => dimension.fieldId),
    [availableDimensions],
  )
  const exactPublishedVersion = detail.publishedVersion?.status === 'PUBLISHED' ? detail.publishedVersion : null
  const draftPreviewAvailable = !detail.record.currentPublishedVersionId
  const previewAvailable = Boolean(exactPublishedVersion || draftPreviewAvailable)
  const [filterDrafts, setFilterDrafts] = useState<Record<string, MetricPreviewFilterDraft>>({})
  const [memberOptions, setMemberOptions] = useState<Record<string, MetricPreviewMemberOptions>>({})
  const [filterMenuErrors, setFilterMenuErrors] = useState<Record<string, string>>({})
  const [appliedFilters, setAppliedFilters] = useState<MetricDimensionFilter[]>([])
  const [openHeader, setOpenHeader] = useState('')
  const [filterAnchor, setFilterAnchor] = useState({ top: 0, left: 0 })
  const [metricSortDirection, setMetricSortDirection] = useState<'' | 'ASC' | 'DESC'>('')
  const [reloadToken, setReloadToken] = useState(0)
  const [preview, setPreview] = useState<DatasetPreview | null>(null)
  const [previewing, setPreviewing] = useState(previewAvailable)
  const [previewError, setPreviewError] = useState('')
  const memberRequestTokens = useRef<Record<string, string>>({})
  const semanticDimensionByField = useMemo(() => new Map(
    semanticDimensions
      .filter(dimension =>
        dimension.datasetId === definition.datasetId &&
        dimension.datasetVersionId === definition.datasetVersionId &&
        dimension.status === 'PUBLISHED')
      .map(dimension => [dimension.fieldId, dimension]),
  ), [definition.datasetId, definition.datasetVersionId, semanticDimensions])
  useEffect(() => {
    if (!previewAvailable) return
    let active = true
    const input = {
      queryId: globalThis.crypto.randomUUID(),
      parameters: {},
      dimensionFieldIds,
      dimensionFilters: appliedFilters,
      metricSortDirection: metricSortDirection || undefined,
      maxRows: 100,
    }
    const request = exactPublishedVersion
      ? metricAPI.previewVersion(detail.record.id, exactPublishedVersion.id, input)
      : metricAPI.preview(detail.record.id, input)
    void request.then(result => {
      if (active) setPreview(result)
    }).catch(cause => {
      if (!active) return
      setPreview(null)
      setPreviewError(cause instanceof Error ? cause.message : '加载指标预览失败')
    }).finally(() => {
      if (active) setPreviewing(false)
    })
    return () => {
      active = false
    }
  }, [
    appliedFilters,
    detail.record.id,
    dimensionFieldIds,
    exactPublishedVersion,
    metricSortDirection,
    previewAvailable,
    reloadToken,
  ])

  function draftFor(fieldId: string): MetricPreviewFilterDraft {
    const existing = filterDrafts[fieldId]
    if (existing) return existing
    const applied = appliedFilters.find(filter => filter.fieldId === fieldId)
    const values = Array.isArray(applied?.value) ? applied.value : applied ? [applied.value] : []
    if (applied?.operator === 'EQUALS' || applied?.operator === 'IN') {
      return { query: '', mode: 'INCLUDE', values }
    }
    if (applied?.operator === 'NOT_EQUALS' || applied?.operator === 'NOT_IN') {
      return { query: '', mode: 'EXCLUDE', values }
    }
    return { query: '', mode: 'ALL', values: [] }
  }

  function updateFilterDraft(fieldId: string, patch: Partial<MetricPreviewFilterDraft>) {
    setFilterDrafts(current => {
      const existing = current[fieldId] ?? draftFor(fieldId)
      return {
        ...current,
        [fieldId]: {
          query: existing.query,
          mode: existing.mode,
          values: existing.values,
          ...patch,
        },
      }
    })
  }

  async function loadMemberOptions(fieldId: string, query: string) {
    const semanticDimension = semanticDimensionByField.get(fieldId)
    const token = globalThis.crypto.randomUUID()
    memberRequestTokens.current[fieldId] = token
    setMemberOptions(current => ({
      ...current,
      [fieldId]: {
        items: current[fieldId]?.items ?? [],
        total: current[fieldId]?.total ?? 0,
        query,
        loading: true,
        error: '',
      },
    }))
    if (!semanticDimension ||
        semanticDimension.memberIndexPolicy !== 'FULL' ||
        semanticDimension.sensitive) {
      setMemberOptions(current => ({
        ...current,
        [fieldId]: {
          items: [],
          total: 0,
          query,
          loading: false,
          error: '当前维度未建立可枚举成员索引',
        },
      }))
      return
    }
    try {
      const page = await semanticGovernanceAPI.listMembers(
        semanticDimension.id,
        query.trim(),
        'ACTIVE',
        200,
        0,
      )
      if (memberRequestTokens.current[fieldId] !== token) return
      setMemberOptions(current => ({
        ...current,
        [fieldId]: {
          items: page.items,
          total: page.total,
          query,
          loading: false,
          error: '',
        },
      }))
    } catch (cause) {
      if (memberRequestTokens.current[fieldId] !== token) return
      setMemberOptions(current => ({
        ...current,
        [fieldId]: {
          items: [],
          total: 0,
          query,
          loading: false,
          error: cause instanceof Error ? cause.message : '读取维度值失败',
        },
      }))
    }
  }

  function openDimensionFilter(fieldId: string, anchor: { top: number; left: number }) {
    if (openHeader === fieldId) {
      setOpenHeader('')
      return
    }
    setFilterAnchor(anchor)
    setOpenHeader(fieldId)
    setFilterMenuErrors(current => ({ ...current, [fieldId]: '' }))
    const draft = draftFor(fieldId)
    void loadMemberOptions(fieldId, draft.query)
  }

  function searchMemberOptions(fieldId: string, query: string) {
    updateFilterDraft(fieldId, { query })
    setFilterMenuErrors(current => ({ ...current, [fieldId]: '' }))
    void loadMemberOptions(fieldId, query)
  }

  function memberChecked(draft: MetricPreviewFilterDraft, value: string): boolean {
    if (draft.mode === 'ALL') return true
    if (draft.mode === 'INCLUDE') return draft.values.includes(value)
    return !draft.values.includes(value)
  }

  function toggleMember(fieldId: string, value: string) {
    const draft = draftFor(fieldId)
    if (draft.mode === 'ALL') {
      updateFilterDraft(fieldId, { mode: 'EXCLUDE', values: [value] })
    } else if (draft.mode === 'INCLUDE') {
      updateFilterDraft(fieldId, {
        values: draft.values.includes(value)
          ? draft.values.filter(item => item !== value)
          : [...draft.values, value],
      })
    } else {
      const values = draft.values.includes(value)
        ? draft.values.filter(item => item !== value)
        : [...draft.values, value]
      updateFilterDraft(fieldId, {
        mode: values.length ? 'EXCLUDE' : 'ALL',
        values,
      })
    }
    setFilterMenuErrors(current => ({ ...current, [fieldId]: '' }))
  }

  function toggleAllMembers(fieldId: string) {
    const draft = draftFor(fieldId)
    updateFilterDraft(fieldId, draft.mode === 'ALL'
      ? { mode: 'INCLUDE', values: [] }
      : { mode: 'ALL', values: [] })
    setFilterMenuErrors(current => ({ ...current, [fieldId]: '' }))
  }

  function applyFilter(fieldId: string) {
    const draft = draftFor(fieldId)
    let filter: MetricDimensionFilter | null = null
    if (draft.mode === 'INCLUDE' && draft.values.length === 0) {
      setFilterMenuErrors(current => ({ ...current, [fieldId]: '请至少选择一个值' }))
      return
    }
    if (draft.values.length > 128) {
      setFilterMenuErrors(current => ({ ...current, [fieldId]: '单次筛选最多选择或排除 128 个值' }))
      return
    }
    if (draft.mode === 'INCLUDE') {
      filter = draft.values.length === 1
        ? { fieldId, operator: 'EQUALS', value: draft.values[0] }
        : { fieldId, operator: 'IN', value: draft.values }
    } else if (draft.mode === 'EXCLUDE' && draft.values.length > 0) {
      filter = draft.values.length === 1
        ? { fieldId, operator: 'NOT_EQUALS', value: draft.values[0] }
        : { fieldId, operator: 'NOT_IN', value: draft.values }
    }
    setPreviewError('')
    setFilterMenuErrors(current => ({ ...current, [fieldId]: '' }))
    setPreviewing(true)
    setAppliedFilters(current => [
      ...current.filter(item => item.fieldId !== fieldId),
      ...(filter ? [filter] : []),
    ])
    setOpenHeader('')
  }

  function clearFilter(fieldId: string) {
    setFilterDrafts(current => ({ ...current, [fieldId]: { query: '', mode: 'ALL', values: [] } }))
    setPreviewing(true)
    setAppliedFilters(current => current.filter(item => item.fieldId !== fieldId))
    setPreviewError('')
    setOpenHeader('')
  }

  function cancelFilter(fieldId: string) {
    setFilterDrafts(current => {
      const next = { ...current }
      delete next[fieldId]
      return next
    })
    setFilterMenuErrors(current => ({ ...current, [fieldId]: '' }))
    setOpenHeader('')
  }

  function updateMetricSort(direction: '' | 'ASC' | 'DESC') {
    if (direction === metricSortDirection) {
      setOpenHeader('')
      return
    }
    setPreviewError('')
    setPreviewing(true)
    setMetricSortDirection(direction)
    setOpenHeader('')
  }

  function refreshPreview() {
    setPreviewError('')
    setPreviewing(true)
    setReloadToken(current => current + 1)
  }

  const displayColumns = preview?.columns.length
    ? preview.columns.map((column, index) => ({
        column,
        metadata: preview.columnMetadata?.[index],
        dimension: availableDimensions[index],
      }))
    : [
        ...availableDimensions.map(dimension => {
          const field = fields.find(item => item.id === dimension.fieldId)
          return {
            column: field?.code || dimension.fieldId,
            metadata: { code: field?.code || dimension.fieldId, name: dimension.name },
            dimension,
          }
        }),
        {
          column: definition.metric.code,
          metadata: { code: definition.metric.code, name: definition.metric.name },
          dimension: undefined,
        },
      ]

  const display = (value: unknown) => value == null ? '—' : typeof value === 'object' ? JSON.stringify(value) : String(value)
  const rowCount = preview?.rowCount ?? 0
  const columnCount = Math.max(displayColumns.length, 1)

  return <section className="metric-preview-pivot" aria-label="指标数据库透视表">
    <header className="metric-preview-pivot-bar">
      <div><span className="metric-preview-live-dot" aria-hidden="true" /><strong>数据库直连</strong><small>{preview ? `${rowCount} 行 · ${preview.durationMs} ms` : previewing ? '正在查询…' : '等待查询'}</small></div>
      <div>{appliedFilters.length > 0 && <span>{appliedFilters.length} 个筛选</span>}<span>最多 100 行</span><button type="button" aria-label="刷新数据库数据" disabled={!previewAvailable || previewing} onClick={refreshPreview}><ArrowClockwiseIcon size={14} weight="bold" /></button></div>
    </header>
    {Boolean(preview?.warnings?.length) && <div className="metric-preview-warnings" role="note">{preview?.warnings?.map((warning, index) => <p key={`${warning.code}-${index}`}><strong>{warning.code}</strong>{warning.message}</p>)}</div>}
    <div className="metric-preview-table-wrap">
      <table>
        <thead><tr>{displayColumns.map((item, index) => {
          const dimension = item.dimension
          const activeFilter = dimension ? appliedFilters.find(filter => filter.fieldId === dimension.fieldId) : undefined
          const draft = dimension ? draftFor(dimension.fieldId) : null
          const members = dimension ? memberOptions[dimension.fieldId] : undefined
          const headerID = dimension ? dimension.fieldId : '__metric__'
          return <th key={`${item.column}-${index}`} className={activeFilter || (!dimension && metricSortDirection) ? 'active' : ''}>
            <div className="metric-preview-th">
              <span><strong>{item.metadata?.name || item.column}</strong><small>{dimension ? item.metadata?.code || item.column : `${item.metadata?.code || item.column}${definition.unit ? ` · ${definition.unit}` : ''}`}</small></span>
              <button type="button" aria-label={dimension ? `筛选${dimension.name}` : `排序${definition.metric.name}`} aria-expanded={openHeader === headerID} onClick={event => {
                if (!dimension) {
                  setOpenHeader(current => current === headerID ? '' : headerID)
                  return
                }
                const rectangle = event.currentTarget.getBoundingClientRect()
                const popupWidth = 320
                openDimensionFilter(dimension.fieldId, {
                  top: Math.max(8, Math.min(rectangle.bottom + 6, globalThis.innerHeight - 352)),
                  left: Math.max(8, Math.min(rectangle.right - popupWidth, globalThis.innerWidth - popupWidth - 8)),
                })
              }}>{dimension ? <FunnelIcon size={13} weight={activeFilter ? 'fill' : 'bold'} /> : <CaretDownIcon size={13} weight="bold" />}</button>
            </div>
            {openHeader === headerID && dimension && draft && createPortal(<div className="metric-preview-header-filter metric-excel-filter" style={{ top: filterAnchor.top, left: filterAnchor.left }} onClick={event => event.stopPropagation()}>
              <header><strong>{dimension.name}</strong>{activeFilter && <button type="button" disabled={previewing} onClick={() => clearFilter(dimension.fieldId)}>从“{dimension.name}”清除筛选</button>}</header>
              <label className="metric-excel-search"><MagnifyingGlassIcon size={13} /><input autoFocus aria-label={`搜索${dimension.name}筛选值`} type="search" value={draft.query} disabled={previewing} placeholder="搜索" onChange={event => searchMemberOptions(dimension.fieldId, event.target.value)} /></label>
              <div className="metric-excel-select-all">
                <label><input type="checkbox" aria-label={`${dimension.name}全选`} aria-checked={draft.mode === 'ALL' ? 'true' : draft.values.length ? 'mixed' : 'false'} checked={draft.mode === 'ALL'} disabled={previewing || members?.loading} onChange={() => toggleAllMembers(dimension.fieldId)} /><span>（全选）</span></label>
                <small>{members?.total ?? 0} 个值</small>
              </div>
              <div className="metric-excel-member-list" role="group" aria-label={`${dimension.name}可筛选值`}>
                {members?.loading && <div className="metric-excel-filter-state"><ArrowClockwiseIcon className="spinning" size={15} />正在读取数据库维度值…</div>}
                {!members?.loading && members?.error && <div className="metric-excel-filter-state error">{members.error}</div>}
                {!members?.loading && !members?.error && members?.items.map(member => <label key={member.id}><input type="checkbox" checked={memberChecked(draft, member.memberKey)} disabled={previewing} onChange={() => toggleMember(dimension.fieldId, member.memberKey)} /><span>{member.canonicalLabel}</span>{member.memberKey !== member.canonicalLabel && <small>{member.memberKey}</small>}</label>)}
                {!members?.loading && !members?.error && members?.items.length === 0 && <div className="metric-excel-filter-state">没有匹配的维度值</div>}
              </div>
              {members && members.total > members.items.length && <p className="metric-excel-filter-hint">当前显示前 {members.items.length} 项，输入关键词可继续查找。</p>}
              {filterMenuErrors[dimension.fieldId] && <p className="metric-excel-filter-error" role="alert">{filterMenuErrors[dimension.fieldId]}</p>}
              <footer><button type="button" disabled={previewing} onClick={() => cancelFilter(dimension.fieldId)}>取消</button><button type="button" disabled={previewing || members?.loading || Boolean(members?.error)} onClick={() => applyFilter(dimension.fieldId)}>确定</button></footer>
            </div>, document.body)}
            {openHeader === headerID && !dimension && <div className="metric-preview-header-filter metric-sort-filter" onClick={event => event.stopPropagation()}>
              <strong>指标排序</strong>
              <button type="button" className={!metricSortDirection ? 'selected' : ''} onClick={() => updateMetricSort('')}>按维度默认排序</button>
              <button type="button" className={metricSortDirection === 'DESC' ? 'selected' : ''} onClick={() => updateMetricSort('DESC')}>指标值从高到低</button>
              <button type="button" className={metricSortDirection === 'ASC' ? 'selected' : ''} onClick={() => updateMetricSort('ASC')}>指标值从低到高</button>
            </div>}
          </th>
        })}</tr></thead>
        <tbody>
          {!previewAvailable && <tr><td className="metric-preview-table-state error" colSpan={columnCount}>当前版本不能预览，仅支持当前草稿或状态正常的精确发布版本。</td></tr>}
          {previewAvailable && previewing && !preview && <tr><td className="metric-preview-table-state" colSpan={columnCount}><ArrowClockwiseIcon className="spinning" size={18} />正在直连数据库查询…</td></tr>}
          {previewAvailable && previewError && !previewing && <tr><td className="metric-preview-table-state error" colSpan={columnCount}>{previewError}</td></tr>}
          {previewAvailable && preview && !previewing && preview.rows.length === 0 && <tr><td className="metric-preview-table-state" colSpan={columnCount}>没有符合表头筛选条件的数据</td></tr>}
          {preview?.rows.map((row, rowIndex) => <tr key={rowIndex}>{displayColumns.map((item, columnIndex) => <td key={`${item.column}-${columnIndex}`}>{display(row[columnIndex])}</td>)}</tr>)}
        </tbody>
      </table>
    </div>
  </section>
}

function MetricSourceView({ detail, definition, datasetName, fields, nodes }: { detail: MetricDetail; definition: MetricDefinition; datasetName: string; fields: DatasetField[]; nodes: SourceNode[] }) {
  const atomicFieldId = definition.expression.type === 'FIELD_REF' ? definition.expression.fieldId : ''
  const atomicField = fields.find(field => field.id === atomicFieldId)
  const lineageByVersion = new Map((detail.lineage?.nodes ?? []).map(node => [node.datasetVersionId, node]))
  const tableByID = new Map(detail.sourceAssets.map(table => [table.id, table]))
  return <div className="metric-source-view">
    {detail.sourceUnavailable && <div className="metric-detail-notice" role="note">当前账号无法读取精确数据集快照；指标保存的来源 ID 仍会保留展示，字段或表不会被替换成其他版本。</div>}
    <section className="metric-source-hero"><div className="metric-source-icon"><DatabaseIcon size={24} weight="bold" /></div><div><span className="eyebrow">精确数据集版本</span><h4>{datasetName || detail.datasetVersion?.dsl.dataset.name || shortId(definition.datasetId)}</h4><p>{detail.datasetVersion ? `V${detail.datasetVersion.versionNo} · ${detail.datasetVersion.status}` : '版本元数据不可用'}</p></div><dl><div><dt>数据集 ID</dt><dd title={definition.datasetId}>{shortId(definition.datasetId)}</dd></div><div><dt>版本 ID</dt><dd title={definition.datasetVersionId}>{shortId(definition.datasetVersionId)}</dd></div><div><dt>DSL 摘要</dt><dd>{detail.datasetVersion ? shortId(detail.datasetVersion.dslHash) : '—'}</dd></div></dl></section>
    <section className="metric-detail-section"><span className="eyebrow">指标取值字段</span><h4>{atomicFieldId ? fieldName(fields, atomicFieldId) : '派生指标表达式'}</h4>{atomicFieldId && <p>字段编码：<code>{atomicField?.code || atomicFieldId}</code></p>}</section>
    <section className="metric-source-nodes"><header><div><span className="eyebrow">上游节点</span><h4>{nodes.length} 个登记来源</h4></div><p>来源名称取自已登记业务资产；精确版本和物理标识仅用于审计。</p></header>{nodes.length ? <div>{nodes.map(node => {
      const upstreamDataset = node.datasetVersionId ? lineageByVersion.get(node.datasetVersionId) : undefined
      const sourceTable = node.tableId ? tableByID.get(node.tableId) : undefined
      const sourceName = upstreamDataset?.name || sourceTable?.businessName || sourceTable?.tableName ||
        (node.datasetVersionId ? `上游数据集 ${shortId(node.datasetVersionId)}` : `源表 ${shortId(node.tableId || node.fileVersionId || node.id)}`)
      const sourceKind = upstreamDataset ? `${upstreamDataset.layer} 数据集` : sourceTable ? '业务源表' : node.type === 'DATASET' ? '数据集' : '源表'
      return <article key={node.id} title={upstreamDataset ? `精确版本 ${node.datasetVersionId}` : sourceTable?.tableName || node.tableId}>
        {upstreamDataset ? <StackIcon size={20} weight="bold" /> : <TableIcon size={20} weight="bold" />}
        <div><strong>{sourceName}</strong><small>{sourceKind} · 已登记业务资产</small></div>
        <dl>
          <div><dt>{upstreamDataset ? '来源层级' : '数据源'}</dt><dd>{upstreamDataset?.layer || sourceTable?.dataSourceName || shortId(node.datasourceId)}</dd></div>
          <div><dt>{upstreamDataset ? '上游精确版本' : '物理表 / 文件版本'}</dt><dd title={node.datasetVersionId || node.tableId || node.fileVersionId}>{shortId(node.datasetVersionId || (sourceTable ? [sourceTable.schemaName, sourceTable.tableName].filter(Boolean).join('.') : '') || node.tableId || node.fileVersionId)}</dd></div>
        </dl>
      </article>
    })}</div> : <div className="metric-detail-state compact"><TableIcon size={30} /><strong>暂无可读取的上游节点</strong></div>}</section>
  </div>
}

type MetricLineageGraphNode = {
  id: string
  kind: 'dataset' | 'field' | 'metric'
  dataset?: WarehouseLineageNode
  x: number
  y: number
  width: number
  height: number
}

type MetricLineageGraphPath = {
  id: string
  path: string
  arrow: boolean
  bundled: boolean
}

type MetricLineageGraphLayout = {
  nodes: MetricLineageGraphNode[]
  paths: MetricLineageGraphPath[]
  width: number
  height: number
  completeToODS: boolean
}

function warehouseLineageGraphLayout(lineage: WarehouseLineage): MetricLineageGraphLayout | null {
  const byID = new Map(lineage.nodes.map(node => [node.datasetVersionId, node]))
  if (!byID.has(lineage.rootDatasetVersionId)) return null

  const upstream = new Map<string, string[]>()
  for (const edge of lineage.edges) {
    if (!byID.has(edge.fromDatasetVersionId) || !byID.has(edge.toDatasetVersionId)) continue
    upstream.set(edge.toDatasetVersionId, [
      ...(upstream.get(edge.toDatasetVersionId) ?? []),
      edge.fromDatasetVersionId,
    ])
  }

  // Only draw nodes that can reach the metric's exact dataset version. The API
  // normally returns this set already, but filtering prevents unrelated nodes
  // from making the canvas noisy when a partial lineage response is returned.
  const relevant = new Set<string>()
  const pending = [lineage.rootDatasetVersionId]
  while (pending.length) {
    const id = pending.pop()
    if (!id || relevant.has(id)) continue
    relevant.add(id)
    for (const upstreamID of upstream.get(id) ?? []) pending.push(upstreamID)
  }

  const edges = lineage.edges.filter(edge =>
    relevant.has(edge.fromDatasetVersionId) && relevant.has(edge.toDatasetVersionId))
  const outgoing = new Map<string, string[]>()
  const incoming = new Map<string, string[]>()
  const indegree = new Map([...relevant].map(id => [id, 0]))
  for (const edge of edges) {
    if ((outgoing.get(edge.fromDatasetVersionId) ?? []).includes(edge.toDatasetVersionId)) continue
    outgoing.set(edge.fromDatasetVersionId, [
      ...(outgoing.get(edge.fromDatasetVersionId) ?? []),
      edge.toDatasetVersionId,
    ])
    incoming.set(edge.toDatasetVersionId, [
      ...(incoming.get(edge.toDatasetVersionId) ?? []),
      edge.fromDatasetVersionId,
    ])
    indegree.set(edge.toDatasetVersionId, (indegree.get(edge.toDatasetVersionId) ?? 0) + 1)
  }

  const topologicalIndex = new Map(lineage.topologicalOrder.map((id, index) => [id, index]))
  const compareIDs = (left: string, right: string) =>
    (topologicalIndex.get(left) ?? Number.MAX_SAFE_INTEGER) - (topologicalIndex.get(right) ?? Number.MAX_SAFE_INTEGER) ||
    (byID.get(left)?.name ?? left).localeCompare(byID.get(right)?.name ?? right, 'zh-CN')
  const queue = [...relevant].filter(id => (indegree.get(id) ?? 0) === 0).sort(compareIDs)
  const sourceIDs = [...queue]
  const depth = new Map([...relevant].map(id => [id, 0]))
  const workingIndegree = new Map(indegree)
  while (queue.length) {
    const id = queue.shift()
    if (!id) continue
    for (const downstreamID of outgoing.get(id) ?? []) {
      depth.set(downstreamID, Math.max(depth.get(downstreamID) ?? 0, (depth.get(id) ?? 0) + 1))
      const nextIndegree = (workingIndegree.get(downstreamID) ?? 0) - 1
      workingIndegree.set(downstreamID, nextIndegree)
      if (nextIndegree === 0) {
        queue.push(downstreamID)
        queue.sort(compareIDs)
      }
    }
  }

  const rootDepth = depth.get(lineage.rootDatasetVersionId) ?? 0
  const fieldID = '__metric_field__'
  const metricID = '__metric_version__'
  const levels = new Map<number, string[]>()
  for (const id of relevant) {
    const nodeDepth = depth.get(id) ?? 0
    levels.set(nodeDepth, [...(levels.get(nodeDepth) ?? []), id])
  }
  levels.set(rootDepth + 1, [fieldID])
  levels.set(rootDepth + 2, [metricID])

  const orderedLevels = [...levels.entries()].sort(([left], [right]) => left - right)
  const levelPosition = new Map<string, number>()
  for (const [, ids] of orderedLevels) {
    ids.sort((left, right) => {
      const leftParents = incoming.get(left) ?? []
      const rightParents = incoming.get(right) ?? []
      const leftScore = leftParents.length
        ? leftParents.reduce((sum, id) => sum + (levelPosition.get(id) ?? 0), 0) / leftParents.length
        : 0
      const rightScore = rightParents.length
        ? rightParents.reduce((sum, id) => sum + (levelPosition.get(id) ?? 0), 0) / rightParents.length
        : 0
      return leftScore - rightScore || compareIDs(left, right)
    })
    ids.forEach((id, index) => levelPosition.set(id, index))
  }

  const nodeWidth = 156
  const nodeHeight = 76
  const columnGap = 38
  const rowGap = 14
  const padding = 20
  const maxLevelSize = Math.max(...orderedLevels.map(([, ids]) => ids.length), 1)
  const height = Math.max(238, padding * 2 + maxLevelSize * nodeHeight + (maxLevelSize - 1) * rowGap)
  const width = padding * 2 + orderedLevels.length * nodeWidth + (orderedLevels.length - 1) * columnGap
  const graphNodes: MetricLineageGraphNode[] = []

  orderedLevels.forEach(([, ids], columnIndex) => {
    const levelHeight = ids.length * nodeHeight + Math.max(0, ids.length - 1) * rowGap
    const startY = (height - levelHeight) / 2
    ids.forEach((id, rowIndex) => graphNodes.push({
      id,
      kind: id === fieldID ? 'field' : id === metricID ? 'metric' : 'dataset',
      dataset: byID.get(id),
      x: padding + columnIndex * (nodeWidth + columnGap),
      y: startY + rowIndex * (nodeHeight + rowGap),
      width: nodeWidth,
      height: nodeHeight,
    }))
  })

  const graphEdges = [
    ...edges.map(edge => ({ from: edge.fromDatasetVersionId, to: edge.toDatasetVersionId })),
    { from: lineage.rootDatasetVersionId, to: fieldID },
    { from: fieldID, to: metricID },
  ]
  const nodePosition = new Map(graphNodes.map(node => [node.id, node]))
  const edgesByTarget = new Map<string, string[]>()
  for (const edge of graphEdges) {
    if (!nodePosition.has(edge.from) || !nodePosition.has(edge.to)) continue
    edgesByTarget.set(edge.to, [...(edgesByTarget.get(edge.to) ?? []), edge.from])
  }

  const paths: MetricLineageGraphPath[] = []
  for (const [targetID, sourceIDList] of edgesByTarget) {
    const target = nodePosition.get(targetID)
    if (!target) continue
    const sources = [...new Set(sourceIDList)].map(id => nodePosition.get(id)).filter(Boolean) as MetricLineageGraphNode[]
    const targetX = target.x
    const targetY = target.y + target.height / 2
    if (sources.length === 1) {
      const source = sources[0]
      const sourceX = source.x + source.width
      const sourceY = source.y + source.height / 2
      const controlOffset = Math.max(18, (targetX - sourceX) / 2)
      paths.push({
        id: `${source.id}:${target.id}`,
        path: `M ${sourceX} ${sourceY} C ${sourceX + controlOffset} ${sourceY}, ${targetX - controlOffset} ${targetY}, ${targetX} ${targetY}`,
        arrow: true,
        bundled: false,
      })
      continue
    }

    const furthestSourceX = Math.max(...sources.map(source => source.x + source.width))
    const junctionX = Math.max(furthestSourceX + 16, targetX - columnGap / 2)
    for (const source of sources) {
      const sourceX = source.x + source.width
      const sourceY = source.y + source.height / 2
      const controlOffset = Math.max(16, (junctionX - sourceX) * .55)
      paths.push({
        id: `${source.id}:${target.id}:branch`,
        path: `M ${sourceX} ${sourceY} C ${sourceX + controlOffset} ${sourceY}, ${junctionX - controlOffset} ${targetY}, ${junctionX} ${targetY}`,
        arrow: false,
        bundled: true,
      })
    }
    paths.push({
      id: `bundle:${target.id}`,
      path: `M ${junctionX} ${targetY} L ${targetX} ${targetY}`,
      arrow: true,
      bundled: true,
    })
  }

  return {
    nodes: graphNodes,
    paths,
    width,
    height,
    completeToODS: sourceIDs.length > 0 && sourceIDs.every(id => byID.get(id)?.layer === 'ODS'),
  }
}

function MetricLineageView({ detail, definition, fields }: { detail: MetricDetail; definition: MetricDefinition; datasetName: string; fields: DatasetField[]; nodes: SourceNode[] }) {
  const atomicFieldId = definition.expression.type === 'FIELD_REF' ? definition.expression.fieldId : ''
  const graph = detail.lineage ? warehouseLineageGraphLayout(detail.lineage) : null
  const completeToODS = graph?.completeToODS ?? false
  const downstream = [
    { label: '报告草稿引用', value: detail.usage.reportDraftReferences },
    { label: '下游指标草稿', value: detail.usage.downstreamDraftReferences },
    { label: '下游已发布指标', value: detail.usage.downstreamPublishedReferences },
    { label: '运行中查询', value: detail.usage.activeQueryRuns },
  ]
  return <div className="metric-lineage-view">
    <header><div><span className="eyebrow">精确版本数据血缘</span><h4>ODS → DWD / DIM → DWS → 取值口径 → 指标版本</h4></div><span>{completeToODS ? '已追溯至 ODS' : detail.lineageUnavailable ? '血缘读取失败' : '血缘待完善'}</span></header>
    <div className="metric-lineage-flow" aria-label="指标上游血缘">
      {graph?.nodes.length ? <div className="metric-lineage-canvas" style={{ width: graph.width, height: graph.height }}>
        <svg aria-hidden="true" viewBox={`0 0 ${graph.width} ${graph.height}`} width={graph.width} height={graph.height}>
          <defs><marker id="metric-lineage-arrowhead" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" /></marker></defs>
          {graph.paths.map(item => <path
            className={`metric-lineage-edge${item.bundled ? ' bundled' : ''}`}
            d={item.path}
            key={item.id}
            markerEnd={item.arrow ? 'url(#metric-lineage-arrowhead)' : undefined}
          />)}
        </svg>
        {graph.nodes.map(node => {
          if (node.kind === 'field') return <article
            className="metric-lineage-node field"
            key={node.id}
            style={{ left: node.x, top: node.y, width: node.width, height: node.height }}
          ><TableIcon size={19} weight="bold" /><span><small>取值口径</small><strong>{atomicFieldId ? fieldName(fields, atomicFieldId) : '指标表达式'}</strong></span></article>
          if (node.kind === 'metric') return <article
            className="metric-lineage-node metric"
            key={node.id}
            style={{ left: node.x, top: node.y, width: node.width, height: node.height }}
          ><FunctionIcon size={19} weight="bold" /><span><small>指标版本</small><strong>{definition.metric.name}</strong><em>{detail.publishedVersion ? `发布 V${detail.publishedVersion.versionNo}` : '当前草稿'}</em></span></article>
          const dataset = node.dataset
          if (!dataset) return null
          return <article
            className={`metric-lineage-node dataset layer-${dataset.layer.toLowerCase()}`}
            key={node.id}
            style={{ left: node.x, top: node.y, width: node.width, height: node.height }}
            title={`精确版本 ${dataset.datasetVersionId}`}
          ><StackIcon size={19} weight="bold" /><span><small>{dataset.layer} 数据集</small><strong>{dataset.name}</strong><em>{dataset.status === 'PUBLISHED' ? '已发布' : statusLabels[dataset.status] ?? dataset.status} · 精确版本</em></span></article>
        })}
      </div> : <div className="metric-lineage-empty"><DatabaseIcon size={26} /><strong>无法读取数据集版本血缘</strong><p>当前指标仍固定到精确数据集版本，重新加载后可再次追溯到 ODS。</p></div>}
    </div>
    <section className="metric-lineage-downstream"><header><div><GitBranchIcon size={21} weight="bold" /><span><strong>下游占用汇总</strong><small>只展示有权限的聚合计数</small></span></div></header><dl>{downstream.map(item => <div key={item.label}><dt>{item.label}</dt><dd>{detail.usageUnavailable ? '—' : item.value}</dd></div>)}</dl></section>
    <div className={`metric-lineage-boundary ${completeToODS ? 'complete' : 'incomplete'}`} role="note"><strong>{completeToODS ? '血缘完整性已通过' : '血缘完整性待处理'}</strong><p>{completeToODS ? '已沿真实数据集版本依赖完整追溯至 ODS；所有节点均使用已发布资产的业务名称，不展示 DSL 内部别名。' : '当前精确版本尚未形成可验证的 ODS 起点，请检查上游版本依赖或读取权限。'}</p></div>
  </div>
}
