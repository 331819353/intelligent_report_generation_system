import { useEffect, useMemo, useState } from 'react'
import {
  ArrowRightIcon,
  CheckSquareIcon,
  DatabaseIcon,
  FunctionIcon,
  GitBranchIcon,
  MagicWandIcon,
  MagnifyingGlassIcon,
  PlusIcon,
  SquareIcon,
  StackIcon,
  TableIcon,
  XIcon,
} from '@phosphor-icons/react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AssetManagementTabs } from '../components/AssetManagementTabs'
import {
  datasetAPI,
  type DatasetSummary,
  type PublishedVersionRecord,
} from '../lib/datasets'
import {
  createMetricPublishIdempotencyKey,
  metricAPI,
  type MetricDefinition,
  type MetricExpression,
  type MetricRecord,
  type MetricSummary,
  type MetricUsage,
  type MetricVersionRecord,
} from '../lib/metrics'
import {
  metricCandidateAPI,
  type MetricCandidate,
  type MetricCandidateStatus,
} from '../lib/metric-candidates'

const catalogPageSize = 200
const statusLabels: Record<string, string> = {
  DRAFT: '草稿', PUBLISHED: '已发布', STALE: '已失效', DEPRECATED: '已废弃',
}
const typeLabels: Record<string, string> = { ATOMIC: '原子指标', DERIVED: '派生指标', RATIO: '复合指标' }
const candidateStatusLabels: Record<MetricCandidateStatus, string> = {
  READY: '可发布', NEEDS_REVIEW: '待复核', BLOCKED: '已阻塞', ACCEPTED: '已接收', REJECTED: '已拒绝',
}
const candidateMethodLabels: Record<string, string> = {
  RULE: '规则生成', LLM: 'LLM 生成', HYBRID: '规则 + LLM',
}
const aggregationLabels: Record<string, string> = {
  NONE: '不聚合', SUM: '求和', AVG: '平均值', MIN: '最小值', MAX: '最大值', COUNT: '计数', COUNT_DISTINCT: '去重计数',
}
const timeGrainLabels: Record<string, string> = {
  NONE: '无', DAY: '日', WEEK: '周', MONTH: '月', QUARTER: '季度', YEAR: '年',
}
const additivityLabels: Record<string, string> = {
  ADDITIVE: '可加', SEMI_ADDITIVE: '半可加', NON_ADDITIVE: '不可加',
}
const emptyUsage = (): MetricUsage => ({
  reportDraftReferences: 0,
  downstreamDraftReferences: 0,
  downstreamPublishedReferences: 0,
  activeQueryRuns: 0,
})

type DetailTab = 'overview' | 'definition' | 'dimensions' | 'source' | 'lineage'
type DirectoryView = 'datasets' | 'candidates'
type MetricDetail = {
  record: MetricRecord
  publishedVersion: MetricVersionRecord | null
  datasetVersion: PublishedVersionRecord | null
  usage: MetricUsage
  publishedUnavailable: boolean
  sourceUnavailable: boolean
  usageUnavailable: boolean
}
type DatasetField = { id: string; code: string; name: string; expression: Record<string, unknown> }
type SourceNode = {
  id: string
  type: string
  alias: string
  datasourceId: string
  tableId: string
  datasetVersionId: string
  fileVersionId: string
}

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

async function loadAllCandidates(): Promise<MetricCandidate[]> {
  const items: MetricCandidate[] = []
  for (let offset = 0; ;) {
    const page = await metricCandidateAPI.list(catalogPageSize, offset)
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
    expression: asRecord(field.expression),
  })).filter(field => field.id)
}

function sourceNodes(version: PublishedVersionRecord | null): SourceNode[] {
  if (!version) return []
  return version.dsl.nodes.map(asRecord).map(node => ({
    id: asText(node.id),
    type: asText(node.type),
    alias: asText(node.alias),
    datasourceId: asText(node.datasourceId),
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
  const expression = expressionLabel(definition.expression, fields)
  return definition.aggregation === 'NONE' ? expression : `${definition.aggregation}(${expression})`
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date)
}

/** 指标目录负责发现与理解；高风险的编辑、试算和发布继续由独立编辑路由承载。 */
export function MetricCatalogPage() {
  const navigate = useNavigate()
  const [metrics, setMetrics] = useState<MetricSummary[]>([])
  const [candidates, setCandidates] = useState<MetricCandidate[]>([])
  const [datasets, setDatasets] = useState<DatasetSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [candidateError, setCandidateError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [view, setView] = useState<DirectoryView>('datasets')
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('ALL')
  const [type, setType] = useState('ALL')
  const [candidateStatus, setCandidateStatus] = useState('ALL')
  const [candidateMethod, setCandidateMethod] = useState('ALL')
  const [datasetId, setDatasetId] = useState('ALL')
  const [selectedCandidateIDs, setSelectedCandidateIDs] = useState<string[]>([])
  const [candidatePublishing, setCandidatePublishing] = useState(false)
  const [candidateNotice, setCandidateNotice] = useState('')
  const [selected, setSelected] = useState<MetricSummary | null>(null)
  const [detail, setDetail] = useState<MetricDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [tab, setTab] = useState<DetailTab>('overview')
  const [deletingMetric, setDeletingMetric] = useState<MetricSummary | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteError, setDeleteError] = useState('')

  useEffect(() => {
    let active = true
    queueMicrotask(() => {
      if (!active) return
      setLoading(true)
      setError('')
    })
    Promise.allSettled([loadAllMetrics(), loadAllDatasets(), loadAllCandidates()]).then(([metricResult, datasetResult, candidateResult]) => {
      if (!active) return
      if (metricResult.status === 'rejected') {
        setMetrics([])
        setError(metricResult.reason instanceof Error ? `加载指标目录失败：${metricResult.reason.message}` : '加载指标目录失败')
      } else {
        setMetrics(metricResult.value)
      }
      if (datasetResult.status === 'fulfilled') setDatasets(datasetResult.value)
      if (candidateResult.status === 'fulfilled') {
        setCandidates(candidateResult.value)
        setCandidateError('')
      } else {
        setCandidates([])
        setCandidateError(candidateResult.reason instanceof Error ? `加载候选指标失败：${candidateResult.reason.message}` : '加载候选指标失败')
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
      const [datasetResult, usageResult] = await Promise.allSettled([
        definition ? datasetAPI.getVersion(definition.datasetId, definition.datasetVersionId) : Promise.resolve(null),
        publishedVersion ? metricAPI.getVersionUsage(record.id, publishedVersion.id) : Promise.resolve(emptyUsage()),
      ])
      if (!active) return
      setDetail({
        record,
        publishedVersion,
        datasetVersion: datasetResult.status === 'fulfilled' ? datasetResult.value : null,
        usage: usageResult.status === 'fulfilled' ? usageResult.value : emptyUsage(),
        publishedUnavailable,
        sourceUnavailable: Boolean(definition) && datasetResult.status === 'rejected',
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
  const ordinaryDatasets = useMemo(() => datasets.filter(dataset => !dataset.originTableId), [datasets])
  const displayMetrics = useMemo(() => metrics.filter(metric => metric.type === 'DERIVED' || metric.type === 'RATIO'), [metrics])
  const filteredMetrics = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    return displayMetrics.filter(metric => {
      const matchesQuery = !keyword || [metric.name, metric.code, metric.description]
        .some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))
      return matchesQuery &&
        (status === 'ALL' || metric.status === status) &&
        (type === 'ALL' || metric.type === type) &&
        (datasetId === 'ALL' || metric.datasetId === datasetId)
    })
  }, [datasetId, displayMetrics, query, status, type])
  const datasetSections = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    const metricsByDataset = new Map<string, MetricSummary[]>()
    for (const metric of filteredMetrics) {
      const current = metricsByDataset.get(metric.datasetId) ?? []
      current.push(metric)
      metricsByDataset.set(metric.datasetId, current)
    }
    return ordinaryDatasets.flatMap(dataset => {
      if (datasetId !== 'ALL' && dataset.id !== datasetId) return []
      const datasetMatches = !keyword || [dataset.name, dataset.code, dataset.description]
        .some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))
      const datasetMetrics = metricsByDataset.get(dataset.id) ?? []
      if (keyword && !datasetMatches && !datasetMetrics.length) return []
      if ((status !== 'ALL' || type !== 'ALL') && !datasetMetrics.length) return []
      return [{ dataset, metrics: datasetMetrics }]
    })
  }, [datasetId, filteredMetrics, ordinaryDatasets, query, status, type])
  const filteredCandidates = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    return candidates.filter(candidate => {
      const values = [
        candidate.name, candidate.code, candidate.description,
        candidate.semantic?.name ?? '', candidate.semantic?.description ?? '',
        candidate.semantic?.caliber ?? '', ...(candidate.semantic?.tags ?? []),
      ]
      return (!keyword || values.some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))) &&
        (candidateStatus === 'ALL' || candidate.status === candidateStatus) &&
        (candidateMethod === 'ALL' || candidate.method === candidateMethod) &&
        (datasetId === 'ALL' || candidate.datasetId === datasetId)
    })
  }, [candidateMethod, candidateStatus, candidates, datasetId, query])
  const publishableCandidates = useMemo(() => filteredCandidates.filter(candidate =>
    (candidate.status === 'READY' || candidate.status === 'NEEDS_REVIEW') && !candidate.blockReasons.length
  ), [filteredCandidates])
  const counts = useMemo(() => ({
    published: displayMetrics.filter(metric => metric.status === 'PUBLISHED').length,
    draft: displayMetrics.filter(metric => metric.status === 'DRAFT').length,
    attention: displayMetrics.filter(metric => metric.status === 'STALE' || metric.status === 'DEPRECATED').length,
  }), [displayMetrics])
  const pendingCandidateCount = useMemo(() => candidates.filter(candidate =>
    candidate.status === 'READY' || candidate.status === 'NEEDS_REVIEW'
  ).length, [candidates])
  const filterActive = Boolean(query.trim()) || datasetId !== 'ALL' || (view === 'datasets'
    ? status !== 'ALL' || type !== 'ALL'
    : candidateStatus !== 'ALL' || candidateMethod !== 'ALL')

  function resetFilters() {
    setQuery('')
    setStatus('ALL')
    setType('ALL')
    setCandidateStatus('ALL')
    setCandidateMethod('ALL')
    setDatasetId('ALL')
  }

  function openDetail(metric: MetricSummary) {
    setTab('overview')
    setSelected(metric)
  }
  function createMetricForDataset(dataset: DatasetSummary) {
    if (!dataset.currentPublishedVersionId) return
    navigate('/metrics/new', {
      state: {
        preferredDatasetId: dataset.id,
        safeDatasetExtension: true,
      },
    })
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
  function toggleCandidate(candidateID: string) {
    setSelectedCandidateIDs(current => current.includes(candidateID)
      ? current.filter(id => id !== candidateID)
      : [...current, candidateID])
  }

  function toggleAllCandidates() {
    const visibleIDs = publishableCandidates.map(candidate => candidate.id)
    const allSelected = visibleIDs.length > 0 && visibleIDs.every(id => selectedCandidateIDs.includes(id))
    setSelectedCandidateIDs(current => allSelected
      ? current.filter(id => !visibleIDs.includes(id))
      : [...new Set([...current, ...visibleIDs])])
  }

  async function publishSelectedCandidates() {
    const selectedCandidates = candidates.filter(candidate => selectedCandidateIDs.includes(candidate.id))
    if (!selectedCandidates.length || candidatePublishing) return
    setCandidatePublishing(true)
    setCandidateError('')
    setCandidateNotice('')
    let published = 0
    const failures: string[] = []
    for (const candidate of selectedCandidates) {
      try {
        const accepted = await metricCandidateAPI.accept(candidate.id, candidate.version)
        const metric = accepted.metric
        await metricAPI.publish(metric.id, {
          draftVersionId: metric.draftVersionId,
          expectedVersion: metric.version,
          expectedDraftRecordVersion: metric.draftRecordVersion,
          expectedDefinitionHash: metric.definitionHash,
          validationParameters: {},
        }, createMetricPublishIdempotencyKey())
        published++
      } catch (cause) {
        failures.push(`${candidate.name}：${cause instanceof Error ? cause.message : '发布失败'}`)
      }
    }
    setSelectedCandidateIDs([])
    setCandidatePublishing(false)
    if (failures.length) {
      setCandidateError(`已发布 ${published} 项，${failures.length} 项失败。${failures.join('；')}`)
    } else {
      setCandidateNotice(`已成功发布 ${published} 个指标`)
    }
    setReloadKey(value => value + 1)
  }

  const selectedPublishableCount = selectedCandidateIDs.filter(id =>
    candidates.some(candidate => candidate.id === id &&
      (candidate.status === 'READY' || candidate.status === 'NEEDS_REVIEW') &&
      !candidate.blockReasons.length)
  ).length

  return <AppShell title="资产管理中心" eyebrow="指标 · 语义 · 维度值">
    <AssetManagementTabs />
    <section className="metric-directory" aria-label="指标资产目录">
      <header className="metric-directory-summary">
        <div><span className="eyebrow">按数据集组织</span><h2>一个数据集，一个指标展示区</h2><p>这里只展示派生指标和复合指标。原子指标与映射数据集保留为 DAG 内部构成，不进入展示中心。</p></div>
        <dl aria-label="指标目录统计">
          <div><dt>普通数据集</dt><dd>{ordinaryDatasets.length}</dd></div>
          <div><dt>展示指标</dt><dd>{displayMetrics.length}</dd></div>
          <div><dt>已发布</dt><dd>{counts.published}</dd></div>
          <div><dt>需关注</dt><dd>{counts.attention}</dd></div>
          <div><dt>候选待发布</dt><dd>{pendingCandidateCount}</dd></div>
        </dl>
      </header>

      <div className="metric-directory-modes" role="tablist" aria-label="指标展示分区">
        <button type="button" role="tab" aria-selected={view === 'datasets'} onClick={() => { setView('datasets'); setSelectedCandidateIDs([]) }}>数据集指标 <span>{displayMetrics.length}</span></button>
        <button type="button" role="tab" aria-selected={view === 'candidates'} onClick={() => setView('candidates')}>候选区 <span>{pendingCandidateCount}</span></button>
      </div>

      <div className="metric-directory-filters" aria-label={view === 'datasets' ? '数据集指标筛选' : '候选指标筛选'}>
        <label className="metric-search-field"><span>{view === 'datasets' ? '搜索数据集或指标' : '搜索候选'}</span><span className="metric-search-input"><MagnifyingGlassIcon size={17} /><input aria-label={view === 'datasets' ? '搜索数据集或指标' : '搜索候选指标'} value={query} placeholder="搜索名称、编码或说明" onChange={event => setQuery(event.target.value)} /></span></label>
        {view === 'datasets' ? <>
          <label><span>指标状态</span><select aria-label="指标状态" value={status} onChange={event => setStatus(event.target.value)}><option value="ALL">全部状态</option>{Object.entries(statusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label><span>指标类型</span><select aria-label="指标类型筛选" value={type} onChange={event => setType(event.target.value)}><option value="ALL">全部类型</option><option value="DERIVED">派生指标</option><option value="RATIO">复合指标</option></select></label>
        </> : <>
          <label><span>候选状态</span><select aria-label="候选状态" value={candidateStatus} onChange={event => setCandidateStatus(event.target.value)}><option value="ALL">全部状态</option>{Object.entries(candidateStatusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label><span>生成方式</span><select aria-label="候选生成方式" value={candidateMethod} onChange={event => setCandidateMethod(event.target.value)}><option value="ALL">全部方式</option>{Object.entries(candidateMethodLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        </>}
        <label><span>来源数据集</span><select aria-label="来源数据集" value={datasetId} onChange={event => setDatasetId(event.target.value)}><option value="ALL">全部数据集</option>{ordinaryDatasets.map(dataset => <option key={dataset.id} value={dataset.id}>{dataset.name}</option>)}</select></label>
        <button className="metric-reset-filters" type="button" disabled={!filterActive} onClick={resetFilters}>重置</button>
      </div>

      {view === 'datasets' ? <div className="metric-directory-resultbar"><div><strong>数据集指标展示区</strong><span>新建指标默认以所在数据集的当前发布 DAG 为基线</span></div><small>显示 {datasetSections.length} / {ordinaryDatasets.length} 个数据集</small></div> :
        <div className="metric-directory-resultbar metric-candidate-batchbar"><div><strong>待审批候选</strong><span>多选后将逐项创建指标、试算并正式发布</span></div><div className="metric-candidate-batch-actions"><button type="button" className="quiet-button" disabled={!publishableCandidates.length || candidatePublishing} onClick={toggleAllCandidates}>{publishableCandidates.length > 0 && publishableCandidates.every(candidate => selectedCandidateIDs.includes(candidate.id)) ? '取消全选' : '全选可发布项'}</button><button type="button" className="primary-button" disabled={!selectedPublishableCount || candidatePublishing} onClick={() => void publishSelectedCandidates()}>{candidatePublishing ? '正在批量发布…' : `发布选中指标（${selectedPublishableCount}）`}</button><small>显示 {filteredCandidates.length} / {candidates.length}</small></div></div>}
      {candidateNotice && view === 'candidates' && <div className="metric-directory-toast" role="status"><span>{candidateNotice}</span><button type="button" aria-label="关闭提示" onClick={() => setCandidateNotice('')}>×</button></div>}
      {(view === 'datasets' ? error : candidateError) && <div className="metric-directory-error" role="alert"><span>{view === 'datasets' ? error : candidateError}</span><button type="button" onClick={() => setReloadKey(value => value + 1)}>重新加载</button></div>}
      {loading ? <div className="metric-directory-empty" role="status"><FunctionIcon size={34} /><strong>正在加载指标展示区…</strong></div> : view === 'datasets' ? datasetSections.length ? <div className="metric-dataset-zones">
        {datasetSections.map(({ dataset, metrics: datasetMetrics }) => <section className="metric-dataset-zone" key={dataset.id} aria-label={`${dataset.name}指标展示区`}>
          <header>
            <div className="metric-dataset-identity"><span aria-hidden="true"><DatabaseIcon size={21} weight="duotone" /></span><div><h3>{dataset.name}</h3><p>{dataset.description || '暂无数据集说明'}</p><small>{dataset.code} · {dataset.status === 'PUBLISHED' ? '已发布 DAG' : statusLabels[dataset.status] ?? dataset.status}</small></div></div>
            <div className="metric-dataset-zone-actions"><span>{datasetMetrics.length} 个指标</span><button className="primary-button" type="button" disabled={!dataset.currentPublishedVersionId} title={dataset.currentPublishedVersionId ? '基于该数据集 DAG 新建指标' : '数据集发布后才可新建指标'} onClick={() => createMetricForDataset(dataset)}><PlusIcon size={16} weight="bold" />新建指标</button></div>
          </header>
          <div className="metric-dataset-safety"><GitBranchIcon size={15} weight="bold" /><span>拓展只会产生新的数据集/指标版本；既有指标继续引用原精确发布版本，原实现逻辑不会被覆盖。</span></div>
          {datasetMetrics.length ? <div className="metric-directory-list">
            {datasetMetrics.map(metric => <article className="metric-directory-row" key={metric.id}>
              <div className="metric-asset-icon" aria-hidden="true"><FunctionIcon size={22} weight="bold" /></div>
              <div className="metric-asset-main"><div><button type="button" onClick={() => openDetail(metric)}>{metric.name}</button><span className={`metric-status status-${metric.status.toLowerCase()}`}>{statusLabels[metric.status] ?? metric.status}</span></div><p>{metric.description || '暂无指标说明'}</p><small>{metric.code}</small></div>
              <dl><div><dt>类型</dt><dd>{typeLabels[metric.type] ?? metric.type}</dd></div><div><dt>绑定版本</dt><dd title={metric.datasetVersionId}>{shortId(metric.datasetVersionId)}</dd></div><div><dt>指标版本</dt><dd>{metric.currentPublishedVersionId ? '已发布精确版本' : `草稿 V${metric.version}`}</dd></div><div><dt>更新时间</dt><dd>{formatDate(metric.updatedAt)}</dd></div></dl>
              <div className="metric-asset-actions"><button className="action-view" type="button" onClick={() => openDetail(metric)}>查看</button><button className="action-edit" type="button" onClick={() => navigate(`/metrics/${metric.id}/edit`)}>编辑</button><button className="action-delete" type="button" onClick={() => requestMetricDeletion(metric)}>删除</button></div>
            </article>)}
          </div> : <div className="metric-dataset-empty"><FunctionIcon size={25} /><div><strong>暂无派生或复合指标</strong><p>{dataset.currentPublishedVersionId ? '可从该数据集的当前发布 DAG 开始创建。' : '先发布数据集，再基于其 DAG 创建指标。'}</p></div></div>}
        </section>)}
      </div> : <DirectoryEmpty hasItems={ordinaryDatasets.length > 0} filterActive={filterActive} onReset={resetFilters} /> :
        filteredCandidates.length ? <div className="metric-directory-list" aria-label="候选指标列表">
          {filteredCandidates.map(candidate => {
            const dataset = datasetById.get(candidate.datasetId)
            const publishable = (candidate.status === 'READY' || candidate.status === 'NEEDS_REVIEW') && !candidate.blockReasons.length
            const checked = selectedCandidateIDs.includes(candidate.id)
            return <article className={`metric-directory-row metric-candidate-row ${checked ? 'is-selected' : ''}`} key={candidate.id}>
              <label className="metric-candidate-selector" title={publishable ? '选择候选指标' : '当前候选不可发布'}>
                <input type="checkbox" aria-label={`选择候选指标 ${candidate.name}`} checked={checked} disabled={!publishable || candidatePublishing} onChange={() => toggleCandidate(candidate.id)} />
                {checked ? <CheckSquareIcon size={21} weight="fill" /> : <SquareIcon size={21} />}
                <MagicWandIcon size={20} weight="bold" />
              </label>
              <div className="metric-asset-main"><div><strong>{candidate.name}</strong><span className={`metric-status candidate-${candidate.status.toLowerCase()}`}>{candidateStatusLabels[candidate.status]}</span></div><p>{candidate.semantic?.description || candidate.description || '暂无候选说明'}</p><small>{candidate.code}</small></div>
              <dl><div><dt>生成方式</dt><dd>{candidateMethodLabels[candidate.method] ?? candidate.method}</dd></div><div><dt>来源数据集</dt><dd title={dataset?.name || candidate.datasetId}>{dataset?.name || shortId(candidate.datasetId)}</dd></div><div><dt>置信度</dt><dd>{Math.round(candidate.confidence * 100)}%</dd></div><div><dt>更新时间</dt><dd>{formatDate(candidate.updatedAt)}</dd></div></dl>
              <div className="metric-asset-actions">{candidate.acceptedMetricId ? <button className="action-edit" type="button" onClick={() => navigate(`/metrics/${candidate.acceptedMetricId}/edit`)}>查看指标</button> : <span className="metric-candidate-state">{publishable ? '可加入批量发布' : candidate.blockReasons[0] || '不可发布'}</span>}</div>
            </article>
          })}
        </div> : <div className="metric-directory-empty"><MagicWandIcon size={38} /><strong>{candidates.length ? '没有符合条件的候选指标' : '还没有候选指标'}</strong><p>{candidates.length ? '调整搜索词或筛选条件后再试。' : '提交数据集发布审批时会同步生成；审批通过后才会在这里展示。'}</p>{filterActive && <button className="quiet-button" type="button" onClick={resetFilters}>清除筛选</button>}</div>}
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
    {selected && <MetricDetailDialog summary={selected} detail={detail} loading={detailLoading} error={detailError} tab={tab} datasetName={datasetById.get((detail?.publishedVersion?.definition ?? detail?.record.definition)?.datasetId ?? selected.datasetId)?.name ?? ''} onTab={setTab} onClose={() => setSelected(null)} onEdit={() => navigate(`/metrics/${selected.id}/edit`)} />}
  </AppShell>
}

function DirectoryEmpty({ hasItems, filterActive, onReset }: { hasItems: boolean; filterActive: boolean; onReset: () => void }) {
  return <div className="metric-directory-empty"><MagnifyingGlassIcon size={38} /><strong>{hasItems ? '没有符合条件的数据集或指标' : '还没有可展示的普通数据集'}</strong><p>{hasItems ? '调整搜索词或筛选条件后再试。' : '先创建并发布普通数据集，再从数据集展示区新建指标。'}</p>{filterActive && <button className="quiet-button" type="button" onClick={onReset}>清除筛选</button>}</div>
}

function MetricDetailDialog({ summary, detail, loading, error, tab, datasetName, onTab, onClose, onEdit }: {
  summary: MetricSummary
  detail: MetricDetail | null
  loading: boolean
  error: string
  tab: DetailTab
  datasetName: string
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
  ]
  return <div className="metric-detail-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
    <section className="metric-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="metric-detail-title">
      <header><div><span className="eyebrow">指标核心信息</span><h2 id="metric-detail-title">指标详情</h2></div><button type="button" aria-label="关闭指标详情" onClick={onClose}><XIcon size={18} weight="bold" /></button></header>
      <div className="metric-detail-content">
        <section className="metric-detail-identity">
          <div><div className="metric-detail-badges"><span className={`metric-status status-${summary.status.toLowerCase()}`}>{statusLabels[summary.status] ?? summary.status}</span><span>{typeLabels[summary.type] ?? summary.type}</span><span>{versionLabel}</span></div><h3>{detail?.record.name ?? summary.name}</h3><code>{detail?.record.code ?? summary.code}</code><p>{detail?.record.description || summary.description || '暂无指标说明'}</p></div>
          {definition && <dl><div><dt>聚合</dt><dd>{aggregationLabels[definition.aggregation] ?? definition.aggregation}</dd></div><div><dt>单位</dt><dd>{definition.unit || '—'}</dd></div><div><dt>时间粒度</dt><dd>{timeGrainLabels[definition.timeGrain] ?? definition.timeGrain}</dd></div><div><dt>允许维度</dt><dd>{definition.allowedDimensions.length}</dd></div></dl>}
        </section>
        <nav className="metric-detail-tabs" role="tablist" aria-label="指标详情信息"><div>{tabs.map(item => <button key={item.id} id={`metric-tab-${item.id}`} role="tab" aria-selected={tab === item.id} aria-controls={`metric-panel-${item.id}`} type="button" onClick={() => onTab(item.id)}>{item.label}</button>)}</div></nav>
        <div className="metric-detail-panel" role="tabpanel" id={`metric-panel-${tab}`} aria-labelledby={`metric-tab-${tab}`}>
          {loading && <div className="metric-detail-state" role="status"><FunctionIcon size={32} /><strong>正在读取精确指标信息…</strong></div>}
          {!loading && error && <div className="metric-detail-state error" role="alert"><strong>{error}</strong><p>关闭后可重新打开详情重试。</p></div>}
          {!loading && !error && detail?.publishedUnavailable && <div className="metric-detail-state error" role="alert"><strong>精确发布版本暂时不可读取</strong><p>为避免把草稿误认为发布口径，详情已停止展示；关闭后可重新打开重试。</p></div>}
          {!loading && !error && detail && definition && tab === 'overview' && <MetricOverview detail={detail} definition={definition} datasetName={datasetName} />}
          {!loading && !error && detail && definition && tab === 'definition' && <MetricDefinitionView detail={detail} definition={definition} fields={fields} />}
          {!loading && !error && detail && definition && tab === 'dimensions' && <MetricDimensionsView definition={definition} fields={fields} />}
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
    <section className="metric-detail-section"><span className="eyebrow">口径摘要</span><h4>{aggregationLabels[definition.aggregation] ?? definition.aggregation} · {definition.unit || '无单位'}</h4><dl className="metric-fact-grid"><div><dt>数字格式</dt><dd>{definition.numberFormat}</dd></div><div><dt>小数位</dt><dd>{definition.decimalScale}</dd></div><div><dt>可加性</dt><dd>{additivityLabels[definition.additivity] ?? definition.additivity}</dd></div><div><dt>时间粒度</dt><dd>{timeGrainLabels[definition.timeGrain] ?? definition.timeGrain}</dd></div></dl></section>
    <section className="metric-detail-section metric-overview-source"><span className="eyebrow">精确来源</span><h4>{datasetName || shortId(definition.datasetId)}</h4><p>指标固定到数据集版本 <code>{shortId(definition.datasetVersionId)}</code>，不会静默跟随其他版本。</p></section>
  </div>
}

function MetricDefinitionView({ detail, definition, fields }: { detail: MetricDetail; definition: MetricDefinition; fields: DatasetField[] }) {
  return <div className="metric-definition-view">
    <section className="metric-formula-card"><span className="eyebrow">业务可读口径</span><h4>{definition.metric.name}</h4><div className="metric-formula"><FunctionIcon size={22} weight="bold" /><code>{formulaLabel(definition, fields)}</code></div><p>{definition.metric.description || '暂无口径说明'}</p></section>
    <section className="metric-detail-section"><span className="eyebrow">执行语义</span><h4>计算与展示规则</h4><dl className="metric-semantics-grid"><div><dt>聚合</dt><dd>{aggregationLabels[definition.aggregation] ?? definition.aggregation}</dd></div><div><dt>可加性</dt><dd>{additivityLabels[definition.additivity] ?? definition.additivity}</dd></div><div><dt>单位 / 格式</dt><dd>{definition.unit || '—'} · {definition.numberFormat}</dd></div><div><dt>精度 / 舍入</dt><dd>{definition.decimalScale} 位 · {definition.roundingMode}</dd></div><div><dt>空值处理</dt><dd>{definition.nullHandling}</dd></div><div><dt>除零处理</dt><dd>{definition.divisionByZero}</dd></div></dl></section>
    <details className="metric-catalog-json"><summary>查看完整定义 JSON</summary><pre aria-label="指标详情完整定义 JSON">{JSON.stringify(detail.publishedVersion?.definition ?? detail.record.definition, null, 2)}</pre></details>
  </div>
}

function MetricDimensionsView({ definition, fields }: { definition: MetricDefinition; fields: DatasetField[] }) {
  return <div className="metric-dimensions-view">
    <header><div><span className="eyebrow">允许分组范围</span><h4>共 {definition.allowedDimensions.length} 个维度</h4></div><p>只有当前精确指标版本声明的维度才可用于分组与下钻。</p></header>
    {definition.allowedDimensions.length ? <div className="metric-dimension-table"><table><thead><tr><th>显示名称</th><th>来源字段</th><th>层级</th><th>排序</th><th>空值标签</th><th>可加性约束</th></tr></thead><tbody>{definition.allowedDimensions.map(dimension => <tr key={dimension.fieldId}><td><strong>{dimension.name}</strong></td><td>{fieldName(fields, dimension.fieldId)}</td><td>{dimension.hierarchyFieldIds.length ? dimension.hierarchyFieldIds.map(id => fieldName(fields, id)).join(' → ') : '—'}</td><td>{dimension.sortDirection}</td><td>{dimension.nullLabel || '—'}</td><td>{definition.nonAdditiveDimensionFieldIds.includes(dimension.fieldId) ? '不可直接求和' : '继承指标规则'}</td></tr>)}</tbody></table></div> : <div className="metric-detail-state"><StackIcon size={34} /><strong>当前指标未开放分组维度</strong><p>试算与报告使用时只能查看指标总值。</p></div>}
  </div>
}

function MetricSourceView({ detail, definition, datasetName, fields, nodes }: { detail: MetricDetail; definition: MetricDefinition; datasetName: string; fields: DatasetField[]; nodes: SourceNode[] }) {
  const atomicFieldId = definition.expression.type === 'FIELD_REF' ? definition.expression.fieldId : ''
  return <div className="metric-source-view">
    {detail.sourceUnavailable && <div className="metric-detail-notice" role="note">当前账号无法读取精确数据集快照；指标保存的来源 ID 仍会保留展示，字段或表不会被替换成其他版本。</div>}
    <section className="metric-source-hero"><div className="metric-source-icon"><DatabaseIcon size={24} weight="bold" /></div><div><span className="eyebrow">精确数据集版本</span><h4>{datasetName || detail.datasetVersion?.dsl.dataset.name || shortId(definition.datasetId)}</h4><p>{detail.datasetVersion ? `V${detail.datasetVersion.versionNo} · ${detail.datasetVersion.status}` : '版本元数据不可用'}</p></div><dl><div><dt>数据集 ID</dt><dd title={definition.datasetId}>{shortId(definition.datasetId)}</dd></div><div><dt>版本 ID</dt><dd title={definition.datasetVersionId}>{shortId(definition.datasetVersionId)}</dd></div><div><dt>DSL 摘要</dt><dd>{detail.datasetVersion ? shortId(detail.datasetVersion.dslHash) : '—'}</dd></div></dl></section>
    <section className="metric-detail-section"><span className="eyebrow">指标取值字段</span><h4>{atomicFieldId ? fieldName(fields, atomicFieldId) : '派生指标表达式'}</h4>{atomicFieldId && <p>字段 ID：<code>{atomicFieldId}</code></p>}</section>
    <section className="metric-source-nodes"><header><div><span className="eyebrow">上游节点</span><h4>{nodes.length} 个登记来源</h4></div><p>名称不可用时展示不可变 ID，避免推断未授权资产信息。</p></header>{nodes.length ? <div>{nodes.map(node => <article key={node.id}><TableIcon size={20} weight="bold" /><div><strong>{node.alias || node.type || '数据节点'}</strong><small>{node.type || 'SOURCE'} · {node.id}</small></div><dl><div><dt>数据源</dt><dd title={node.datasourceId}>{shortId(node.datasourceId)}</dd></div><div><dt>表 / 上游版本</dt><dd title={node.tableId || node.datasetVersionId || node.fileVersionId}>{shortId(node.tableId || node.datasetVersionId || node.fileVersionId)}</dd></div></dl></article>)}</div> : <div className="metric-detail-state compact"><TableIcon size={30} /><strong>暂无可读取的上游节点</strong></div>}</section>
  </div>
}

function MetricLineageView({ detail, definition, datasetName, fields, nodes }: { detail: MetricDetail; definition: MetricDefinition; datasetName: string; fields: DatasetField[]; nodes: SourceNode[] }) {
  const atomicFieldId = definition.expression.type === 'FIELD_REF' ? definition.expression.fieldId : ''
  const downstream = [
    { label: '报告草稿引用', value: detail.usage.reportDraftReferences },
    { label: '下游指标草稿', value: detail.usage.downstreamDraftReferences },
    { label: '下游已发布指标', value: detail.usage.downstreamPublishedReferences },
    { label: '运行中查询', value: detail.usage.activeQueryRuns },
  ]
  return <div className="metric-lineage-view">
    <header><div><span className="eyebrow">版本级登记血缘</span><h4>从可信来源到当前指标版本</h4></div><span>{detail.publishedVersion ? `发布 V${detail.publishedVersion.versionNo}` : '草稿口径'}</span></header>
    <div className="metric-lineage-flow" aria-label="指标上游血缘">
      <div className="metric-lineage-source-group">{nodes.length ? nodes.map(node => <article className="metric-lineage-node source" key={node.id}><DatabaseIcon size={19} weight="bold" /><span><small>{node.type || 'SOURCE'}</small><strong>{node.alias || shortId(node.tableId || node.id)}</strong></span></article>) : <article className="metric-lineage-node muted"><DatabaseIcon size={19} /><span><small>来源</small><strong>元数据不可读</strong></span></article>}</div>
      <ArrowRightIcon className="metric-lineage-arrow" size={24} weight="bold" />
      <article className="metric-lineage-node dataset"><StackIcon size={19} weight="bold" /><span><small>数据集版本</small><strong>{datasetName || shortId(definition.datasetVersionId)}</strong></span></article>
      <ArrowRightIcon className="metric-lineage-arrow" size={24} weight="bold" />
      <article className="metric-lineage-node field"><TableIcon size={19} weight="bold" /><span><small>取值口径</small><strong>{atomicFieldId ? fieldName(fields, atomicFieldId) : '指标表达式'}</strong></span></article>
      <ArrowRightIcon className="metric-lineage-arrow" size={24} weight="bold" />
      <article className="metric-lineage-node metric"><FunctionIcon size={19} weight="bold" /><span><small>指标版本</small><strong>{definition.metric.name}</strong></span></article>
    </div>
    <section className="metric-lineage-downstream"><header><div><GitBranchIcon size={21} weight="bold" /><span><strong>下游占用汇总</strong><small>只展示有权限的聚合计数</small></span></div></header><dl>{downstream.map(item => <div key={item.label}><dt>{item.label}</dt><dd>{detail.usageUnavailable ? '—' : item.value}</dd></div>)}</dl></section>
    <div className="metric-lineage-boundary" role="note"><strong>当前血缘覆盖边界</strong><p>已展示精确数据集版本、来源节点和下游占用汇总。对象级“源字段 → 组件 → 报告版本”链路尚未由服务端开放，页面不会推断或暴露无权限对象名称。</p></div>
  </div>
}
