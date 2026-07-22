import { useEffect, useMemo, useState } from 'react'
import {
  ArrowRightIcon,
  DatabaseIcon,
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
import {
  metricCandidateAPI,
  type MetricCandidate,
  type MetricCandidateStatus,
} from '../lib/metric-candidates'
import {
  datasetAPI,
  type DatasetSummary,
  type PublishedVersionRecord,
} from '../lib/datasets'
import {
  metricAPI,
  type MetricDefinition,
  type MetricExpression,
  type MetricRecord,
  type MetricSummary,
  type MetricUsage,
  type MetricVersionRecord,
} from '../lib/metrics'

const catalogPageSize = 200
const statusLabels: Record<string, string> = {
  DRAFT: '草稿', PUBLISHED: '已发布', STALE: '已失效', DEPRECATED: '已废弃',
}
const candidateStatusLabels: Record<MetricCandidateStatus, string> = {
  READY: '可创建', NEEDS_REVIEW: '待确认', BLOCKED: '已阻塞', ACCEPTED: '已接受', REJECTED: '已拒绝',
}
const candidateMethodLabels: Record<string, string> = { RULE: '规则抽取', LLM: 'LLM 补全', HYBRID: '规则 + LLM' }
const typeLabels: Record<string, string> = { ATOMIC: '原子指标', DERIVED: '派生指标', RATIO: '比率指标' }
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
type DirectoryView = 'metrics' | 'candidates'
type CandidateDetailTab = 'proposal' | 'evidence'
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
    const page = await metricCandidateAPI.list({ limit: catalogPageSize, offset })
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

function confidenceLabel(value: number): string {
  if (!Number.isFinite(value)) return '—'
  return `${Math.round(Math.max(0, Math.min(1, value)) * 100)}%`
}

function metricSummaryFromRecord(record: MetricRecord): MetricSummary {
  return {
    id: record.id,
    code: record.code,
    name: record.name,
    description: record.description,
    type: record.type,
    status: record.status,
    version: record.version,
    currentPublishedVersionId: record.currentPublishedVersionId,
    datasetId: record.datasetId,
    datasetVersionId: record.datasetVersionId,
    updatedAt: record.updatedAt,
  }
}

/** 指标目录负责发现与理解；高风险的编辑、试算和发布继续由独立编辑路由承载。 */
export function MetricCatalogPage() {
  const navigate = useNavigate()
  const [view, setView] = useState<DirectoryView>('metrics')
  const [metrics, setMetrics] = useState<MetricSummary[]>([])
  const [candidates, setCandidates] = useState<MetricCandidate[]>([])
  const [datasets, setDatasets] = useState<DatasetSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [candidateError, setCandidateError] = useState('')
  const [notice, setNotice] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('ALL')
  const [type, setType] = useState('ALL')
  const [candidateStatus, setCandidateStatus] = useState('ALL')
  const [candidateMethod, setCandidateMethod] = useState('ALL')
  const [datasetId, setDatasetId] = useState('ALL')
  const [selected, setSelected] = useState<MetricSummary | null>(null)
  const [selectedCandidate, setSelectedCandidate] = useState<MetricCandidate | null>(null)
  const [candidateDatasetVersion, setCandidateDatasetVersion] = useState<PublishedVersionRecord | null>(null)
  const [candidateDetailLoading, setCandidateDetailLoading] = useState(false)
  const [candidateDetailError, setCandidateDetailError] = useState('')
  const [candidateActionBusy, setCandidateActionBusy] = useState(false)
  const [candidateActionError, setCandidateActionError] = useState('')
  const [rejectingCandidate, setRejectingCandidate] = useState(false)
  const [rejectionReason, setRejectionReason] = useState('')
  const [detail, setDetail] = useState<MetricDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [tab, setTab] = useState<DetailTab>('overview')

  useEffect(() => {
    let active = true
    queueMicrotask(() => {
      if (!active) return
      setLoading(true)
      setError('')
      setCandidateError('')
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
    if (!selected && !selectedCandidate) return
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setSelected(null)
      closeCandidateDetail()
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [selected, selectedCandidate])

  useEffect(() => {
    if (!selectedCandidate) {
      queueMicrotask(() => {
        setCandidateDatasetVersion(null)
        setCandidateDetailError('')
      })
      return
    }
    let active = true
    queueMicrotask(() => {
      if (!active) return
      setCandidateDatasetVersion(null)
      setCandidateDetailLoading(true)
      setCandidateDetailError('')
    })
    datasetAPI.getVersion(selectedCandidate.datasetId, selectedCandidate.datasetVersionId).then(version => {
      if (!active) return
      if (version.dslHash !== selectedCandidate.dslHash) {
        setCandidateDetailError('候选记录的数据集摘要与精确版本不一致，请重新提取后再审核')
        return
      }
      setCandidateDatasetVersion(version)
    }).catch(cause => {
      if (active) setCandidateDetailError(cause instanceof Error ? `读取候选来源失败：${cause.message}` : '读取候选来源失败')
    }).finally(() => { if (active) setCandidateDetailLoading(false) })
    return () => { active = false }
  }, [selectedCandidate])

  const datasetById = useMemo(() => new Map(datasets.map(dataset => [dataset.id, dataset])), [datasets])
  const filteredMetrics = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    return metrics.filter(metric => {
      const matchesQuery = !keyword || [metric.name, metric.code, metric.description]
        .some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))
      return matchesQuery &&
        (status === 'ALL' || metric.status === status) &&
        (type === 'ALL' || metric.type === type) &&
        (datasetId === 'ALL' || metric.datasetId === datasetId)
    })
  }, [datasetId, metrics, query, status, type])
  const filteredCandidates = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase('zh-CN')
    return candidates.filter(candidate => {
      const matchesQuery = !keyword || [
        candidate.name, candidate.code, candidate.description,
        candidate.semantic?.name, candidate.semantic?.description, candidate.semantic?.caliber,
        candidate.semantic?.lineageSummary, ...(candidate.semantic?.dimensions ?? []), ...(candidate.semantic?.tags ?? []),
      ].filter((value): value is string => Boolean(value))
        .some(value => value.toLocaleLowerCase('zh-CN').includes(keyword))
      return matchesQuery &&
        (candidateStatus === 'ALL' || candidate.status === candidateStatus) &&
        (candidateMethod === 'ALL' || candidate.method === candidateMethod) &&
        (datasetId === 'ALL' || candidate.datasetId === datasetId)
    })
  }, [candidateMethod, candidateStatus, candidates, datasetId, query])
  const counts = useMemo(() => ({
    published: metrics.filter(metric => metric.status === 'PUBLISHED').length,
    draft: metrics.filter(metric => metric.status === 'DRAFT').length,
    attention: metrics.filter(metric => metric.status === 'STALE' || metric.status === 'DEPRECATED').length,
  }), [metrics])
  const pendingCandidates = useMemo(() => candidates.filter(candidate => ['READY', 'NEEDS_REVIEW', 'BLOCKED'].includes(candidate.status)).length, [candidates])
  const filterActive = Boolean(query.trim()) || datasetId !== 'ALL' || (view === 'metrics'
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

  function changeView(next: DirectoryView) {
    setView(next)
    resetFilters()
    setNotice('')
  }

  function openDetail(metric: MetricSummary) {
    setTab('overview')
    setSelected(metric)
  }

  function openCandidateDetail(candidate: MetricCandidate) {
    setCandidateActionError('')
    setRejectingCandidate(false)
    setRejectionReason('')
    setSelectedCandidate(candidate)
  }

  function closeCandidateDetail() {
    setSelectedCandidate(null)
    setCandidateActionError('')
    setRejectingCandidate(false)
    setRejectionReason('')
  }

  function replaceCandidate(candidate: MetricCandidate) {
    setCandidates(current => current.map(item => item.id === candidate.id ? candidate : item))
    setSelectedCandidate(current => current?.id === candidate.id ? candidate : current)
  }

  async function acceptCandidate(candidate: MetricCandidate) {
    if (candidateActionBusy || candidate.blockReasons.length || candidate.status === 'BLOCKED') return
    setCandidateActionBusy(true)
    setCandidateActionError('')
    try {
      const result = await metricCandidateAPI.accept(candidate.id, candidate.version)
      const acceptedCandidate = result.candidate.acceptedMetricId ? result.candidate : { ...result.candidate, acceptedMetricId: result.metric.id }
      replaceCandidate(acceptedCandidate)
      const summary = metricSummaryFromRecord(result.metric)
      setMetrics(current => [summary, ...current.filter(item => item.id !== summary.id)])
      setNotice(`已接受“${candidate.name}”，指标草稿已创建`)
    } catch (cause) {
      setCandidateActionError(cause instanceof Error ? cause.message : '接受候选指标失败')
    } finally {
      setCandidateActionBusy(false)
    }
  }

  async function rejectCandidate(candidate: MetricCandidate) {
    const reason = rejectionReason.trim()
    if (!reason || candidateActionBusy) return
    setCandidateActionBusy(true)
    setCandidateActionError('')
    try {
      const changed = await metricCandidateAPI.reject(candidate.id, candidate.version, reason)
      replaceCandidate(changed)
      setRejectingCandidate(false)
      setRejectionReason('')
      setNotice(`已拒绝候选“${candidate.name}”`)
    } catch (cause) {
      setCandidateActionError(cause instanceof Error ? cause.message : '拒绝候选指标失败')
    } finally {
      setCandidateActionBusy(false)
    }
  }

  return <AppShell title="指标中心" eyebrow="语义资产" actions={<button className="primary-button metric-create-button" type="button" onClick={() => navigate('/metrics/new')}><PlusIcon size={17} weight="bold" />新建指标</button>}>
    {notice && <div className="metric-directory-toast" role="status"><span>{notice}</span><button type="button" aria-label="关闭消息" onClick={() => setNotice('')}>×</button></div>}
    <section className="metric-directory" aria-label="指标资产目录">
      <header className="metric-directory-summary">
        <div><span className="eyebrow">统一口径目录</span><h2>发现、理解并追溯每一个指标</h2><p>审核数据集自动提取的候选口径，再将确认结果沉淀为可发布的指标资产。</p></div>
        <dl aria-label="指标目录统计">
          <div><dt>全部</dt><dd>{metrics.length}</dd></div>
          <div><dt>已发布</dt><dd>{counts.published}</dd></div>
          <div><dt>草稿</dt><dd>{counts.draft}</dd></div>
          <div><dt>需关注</dt><dd>{counts.attention}</dd></div>
          <div><dt>候选待处理</dt><dd>{pendingCandidates}</dd></div>
        </dl>
      </header>

      <nav className="metric-directory-modes" role="tablist" aria-label="指标目录类型">
        <button type="button" role="tab" aria-selected={view === 'metrics'} onClick={() => changeView('metrics')}>正式指标 <span>{metrics.length}</span></button>
        <button type="button" role="tab" aria-selected={view === 'candidates'} onClick={() => changeView('candidates')}>候选指标 <span>{pendingCandidates}</span></button>
      </nav>

      <div className="metric-directory-filters" aria-label={view === 'metrics' ? '指标筛选' : '候选指标筛选'}>
        <label className="metric-search-field"><span>{view === 'metrics' ? '搜索指标' : '搜索候选'}</span><span className="metric-search-input"><MagnifyingGlassIcon size={17} /><input aria-label={view === 'metrics' ? '搜索指标' : '搜索候选指标'} value={query} placeholder="搜索名称、编码或说明" onChange={event => setQuery(event.target.value)} /></span></label>
        {view === 'metrics' ? <>
          <label><span>指标状态</span><select aria-label="指标状态" value={status} onChange={event => setStatus(event.target.value)}><option value="ALL">全部状态</option>{Object.entries(statusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label><span>指标类型</span><select aria-label="指标类型筛选" value={type} onChange={event => setType(event.target.value)}><option value="ALL">全部类型</option>{Object.entries(typeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        </> : <>
          <label><span>候选状态</span><select aria-label="候选状态" value={candidateStatus} onChange={event => setCandidateStatus(event.target.value)}><option value="ALL">全部状态</option>{Object.entries(candidateStatusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label><span>提取方式</span><select aria-label="提取方式" value={candidateMethod} onChange={event => setCandidateMethod(event.target.value)}><option value="ALL">全部方式</option>{Object.entries(candidateMethodLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        </>}
        <label><span>来源数据集</span><select aria-label="来源数据集" value={datasetId} onChange={event => setDatasetId(event.target.value)}><option value="ALL">全部数据集</option>{datasets.map(dataset => <option key={dataset.id} value={dataset.id}>{dataset.name}</option>)}</select></label>
        <button className="metric-reset-filters" type="button" disabled={!filterActive} onClick={resetFilters}>重置</button>
      </div>

      <div className="metric-directory-resultbar"><div><strong>{view === 'metrics' ? '指标资产' : '候选指标'}</strong><span>{view === 'metrics' ? '按最近更新时间排序' : '审核通过后创建指标草稿'}</span></div><small>显示 {view === 'metrics' ? filteredMetrics.length : filteredCandidates.length} / {view === 'metrics' ? metrics.length : candidates.length}</small></div>
      {(view === 'metrics' ? error : candidateError) && <div className="metric-directory-error" role="alert"><span>{view === 'metrics' ? error : candidateError}</span><button type="button" onClick={() => setReloadKey(value => value + 1)}>重新加载</button></div>}
      {loading ? <div className="metric-directory-empty" role="status"><FunctionIcon size={34} /><strong>正在加载指标目录…</strong></div> : view === 'metrics' ? filteredMetrics.length ? <div className="metric-directory-list">
        {filteredMetrics.map(metric => {
          const dataset = datasetById.get(metric.datasetId)
          return <article className="metric-directory-row" key={metric.id}>
            <div className="metric-asset-icon" aria-hidden="true"><FunctionIcon size={22} weight="bold" /></div>
            <div className="metric-asset-main"><div><button type="button" onClick={() => openDetail(metric)}>{metric.name}</button><span className={`metric-status status-${metric.status.toLowerCase()}`}>{statusLabels[metric.status] ?? metric.status}</span></div><p>{metric.description || '暂无指标说明'}</p><small>{metric.code}</small></div>
            <dl><div><dt>类型</dt><dd>{typeLabels[metric.type] ?? metric.type}</dd></div><div><dt>来源数据集</dt><dd title={dataset?.name || metric.datasetId}>{dataset?.name || shortId(metric.datasetId)}</dd></div><div><dt>当前版本</dt><dd>{metric.currentPublishedVersionId ? '已发布精确版本' : `草稿 V${metric.version}`}</dd></div><div><dt>更新时间</dt><dd>{formatDate(metric.updatedAt)}</dd></div></dl>
            <div className="metric-asset-actions"><button className="action-view" type="button" onClick={() => openDetail(metric)}>查看</button><button className="action-edit" type="button" onClick={() => navigate(`/metrics/${metric.id}/edit`)}>编辑</button></div>
          </article>
        })}
      </div> : <DirectoryEmpty hasItems={metrics.length > 0} filterActive={filterActive} onReset={resetFilters} onCreate={() => navigate('/metrics/new')} /> : filteredCandidates.length ? <div className="metric-directory-list">
        {filteredCandidates.map(candidate => {
          const dataset = datasetById.get(candidate.datasetId)
          return <article className="metric-directory-row metric-candidate-row" key={candidate.id}>
            <div className="metric-asset-icon candidate" aria-hidden="true"><MagicWandIcon size={22} weight="bold" /></div>
            <div className="metric-asset-main"><div><button type="button" onClick={() => openCandidateDetail(candidate)}>{candidate.name}</button><span className={`metric-status candidate-${candidate.status.toLowerCase()}`}>{candidateStatusLabels[candidate.status] ?? candidate.status}</span></div><p>{candidate.description || '暂无候选说明'}</p><small>{candidate.code}</small></div>
            <dl><div><dt>提取方式</dt><dd>{candidateMethodLabels[candidate.method] ?? candidate.method}</dd></div><div><dt>来源数据集</dt><dd title={dataset?.name || candidate.datasetId}>{dataset?.name || shortId(candidate.datasetId)}</dd></div><div><dt>置信度</dt><dd>{confidenceLabel(candidate.confidence)}</dd></div><div><dt>更新时间</dt><dd>{formatDate(candidate.updatedAt)}</dd></div></dl>
            <div className="metric-asset-actions"><button className="action-view" type="button" onClick={() => openCandidateDetail(candidate)}>审核详情</button>{candidate.acceptedMetricId && <button className="action-edit" type="button" onClick={() => navigate(`/metrics/${candidate.acceptedMetricId}/edit`)}>查看指标</button>}</div>
          </article>
        })}
      </div> : <div className="metric-directory-empty"><MagicWandIcon size={38} /><strong>{candidates.length ? '没有符合条件的候选指标' : '还没有候选指标'}</strong><p>{candidates.length ? '调整搜索词或筛选条件后再试。' : '数据集发布后，系统会在这里展示可审核的提取结果。'}</p>{filterActive && <button className="quiet-button" type="button" onClick={resetFilters}>清除筛选</button>}</div>}
    </section>

    {selected && <MetricDetailDialog summary={selected} detail={detail} loading={detailLoading} error={detailError} tab={tab} datasetName={datasetById.get((detail?.publishedVersion?.definition ?? detail?.record.definition)?.datasetId ?? selected.datasetId)?.name ?? ''} onTab={setTab} onClose={() => setSelected(null)} onEdit={() => navigate(`/metrics/${selected.id}/edit`)} />}
    {selectedCandidate && <CandidateDetailDialog candidate={selectedCandidate} datasetName={datasetById.get(selectedCandidate.datasetId)?.name ?? ''} datasetVersion={candidateDatasetVersion} loading={candidateDetailLoading} error={candidateDetailError} actionBusy={candidateActionBusy} actionError={candidateActionError} rejecting={rejectingCandidate} rejectionReason={rejectionReason} onRejectionReason={setRejectionReason} onStartReject={() => setRejectingCandidate(true)} onCancelReject={() => { setRejectingCandidate(false); setRejectionReason(''); setCandidateActionError('') }} onReject={() => void rejectCandidate(selectedCandidate)} onAccept={() => void acceptCandidate(selectedCandidate)} onClose={closeCandidateDetail} onEditMetric={metricId => navigate(`/metrics/${metricId}/edit`)} />}
  </AppShell>
}

function DirectoryEmpty({ hasItems, filterActive, onReset, onCreate }: { hasItems: boolean; filterActive: boolean; onReset: () => void; onCreate: () => void }) {
  return <div className="metric-directory-empty"><MagnifyingGlassIcon size={38} /><strong>{hasItems ? '没有符合条件的指标' : '还没有指标'}</strong><p>{hasItems ? '调整搜索词或筛选条件后再试。' : '创建第一个指标，开始统一业务口径。'}</p>{filterActive ? <button className="quiet-button" type="button" onClick={onReset}>清除筛选</button> : <button className="primary-button" type="button" onClick={onCreate}>新建指标</button>}</div>
}

function CandidateDetailDialog({ candidate, datasetName, datasetVersion, loading, error, actionBusy, actionError, rejecting, rejectionReason, onRejectionReason, onStartReject, onCancelReject, onReject, onAccept, onClose, onEditMetric }: {
  candidate: MetricCandidate
  datasetName: string
  datasetVersion: PublishedVersionRecord | null
  loading: boolean
  error: string
  actionBusy: boolean
  actionError: string
  rejecting: boolean
  rejectionReason: string
  onRejectionReason: (value: string) => void
  onStartReject: () => void
  onCancelReject: () => void
  onReject: () => void
  onAccept: () => void
  onClose: () => void
  onEditMetric: (metricId: string) => void
}) {
  const [tab, setTab] = useState<CandidateDetailTab>('proposal')
  const definition = candidate.proposedDefinition
  const fields = datasetFields(datasetVersion)
  const reviewable = ['READY', 'NEEDS_REVIEW', 'BLOCKED'].includes(candidate.status)
  const canAccept = reviewable && candidate.status !== 'BLOCKED' && !candidate.blockReasons.length && Boolean(datasetVersion) && !loading && !error
  const tabs: Array<{ id: CandidateDetailTab; label: string }> = [
    { id: 'proposal', label: '候选口径' },
    { id: 'evidence', label: '生成依据' },
  ]
  return <div className="metric-detail-backdrop" role="presentation" onMouseDown={event => { if (!actionBusy && event.target === event.currentTarget) onClose() }}>
    <section className="metric-detail-dialog metric-candidate-dialog" role="dialog" aria-modal="true" aria-labelledby="candidate-detail-title">
      <header><div><span className="eyebrow">候选指标审核</span><h2 id="candidate-detail-title">候选指标详情</h2></div><button type="button" aria-label="关闭候选指标详情" disabled={actionBusy} onClick={onClose}><XIcon size={18} weight="bold" /></button></header>
      <div className="metric-detail-content">
        <section className="metric-detail-identity candidate-identity">
          <div><div className="metric-detail-badges"><span className={`metric-status candidate-${candidate.status.toLowerCase()}`}>{candidateStatusLabels[candidate.status] ?? candidate.status}</span><span>{candidateMethodLabels[candidate.method] ?? candidate.method}</span><span>精确数据集版本</span></div><h3>{candidate.name}</h3><code>{candidate.code}</code><p>{candidate.description || '暂无候选说明'}</p></div>
          <dl><div><dt>置信度</dt><dd>{confidenceLabel(candidate.confidence)}</dd></div><div><dt>聚合</dt><dd>{aggregationLabels[definition.aggregation] ?? definition.aggregation}</dd></div><div><dt>时间粒度</dt><dd>{timeGrainLabels[definition.timeGrain] ?? definition.timeGrain}</dd></div><div><dt>允许维度</dt><dd>{definition.allowedDimensions.length}</dd></div></dl>
        </section>
        <nav className="metric-detail-tabs" role="tablist" aria-label="候选指标详情信息"><div>{tabs.map(item => <button key={item.id} id={`candidate-tab-${item.id}`} role="tab" aria-selected={tab === item.id} aria-controls={`candidate-panel-${item.id}`} type="button" onClick={() => setTab(item.id)}>{item.label}</button>)}</div></nav>
        <div className="metric-detail-panel candidate-detail-panel" role="tabpanel" id={`candidate-panel-${tab}`} aria-labelledby={`candidate-tab-${tab}`}>
          {actionError && <div className="metric-directory-error candidate-action-error" role="alert"><span>{actionError}</span></div>}
          {rejecting && <section className="metric-candidate-reject-form" aria-label="拒绝候选指标"><label>拒绝原因<textarea autoFocus aria-label="拒绝原因" maxLength={1000} value={rejectionReason} onChange={event => onRejectionReason(event.target.value)} placeholder="说明该候选不应转为指标草稿的业务原因" /></label><div><button className="quiet-button" type="button" disabled={actionBusy} onClick={onCancelReject}>取消</button><button className="metric-candidate-reject-confirm" type="button" disabled={actionBusy || !rejectionReason.trim()} onClick={onReject}>{actionBusy ? '正在拒绝…' : '确认拒绝'}</button></div></section>}
          {loading && <div className="metric-detail-state compact" role="status"><DatabaseIcon size={30} /><strong>正在读取精确数据集版本…</strong></div>}
          {!loading && error && <div className="metric-detail-notice" role="alert">{error}。候选依据仍可查看，但在来源恢复前不能接受。</div>}
          {!loading && tab === 'proposal' && <div className="metric-candidate-proposal">
            <section className="metric-formula-card"><span className="eyebrow">待确认业务口径</span><h4>{definition.metric.name}</h4><div className="metric-formula"><FunctionIcon size={22} weight="bold" /><code>{formulaLabel(definition, fields)}</code></div><p>{definition.metric.description || candidate.description || '暂无口径说明'}</p></section>
            {candidate.semantic && <section className="metric-detail-section"><span className="eyebrow">LLM 检索语义</span><h4>{candidate.semantic.name}</h4><p>{candidate.semantic.description}</p><dl className="metric-fact-grid"><div><dt>统计口径</dt><dd>{candidate.semantic.caliber}</dd></div><div><dt>统计周期</dt><dd>{candidate.semantic.periodDescription}（{candidate.semantic.period}）</dd></div><div><dt>分析维度</dt><dd>{candidate.semantic.dimensions.join('、') || '无'}</dd></div><div><dt>数据血缘</dt><dd>{candidate.semantic.lineageSummary}</dd></div><div><dt>补全来源</dt><dd>{candidate.semantic.source === 'HYBRID' ? '规则事实 + LLM 补全' : candidate.semantic.source === 'RULE_FALLBACK' ? '规则降级' : '确定性规则'}</dd></div><div><dt>向量标签</dt><dd>{candidate.semantic.tags.join('、') || '无'}</dd></div></dl></section>}
            <section className="metric-detail-section"><span className="eyebrow">执行语义</span><h4>接受后写入指标草稿</h4><dl className="metric-semantics-grid"><div><dt>指标类型</dt><dd>{typeLabels[definition.metric.type] ?? definition.metric.type}</dd></div><div><dt>可加性</dt><dd>{additivityLabels[definition.additivity] ?? definition.additivity}</dd></div><div><dt>单位 / 格式</dt><dd>{definition.unit || '—'} · {definition.numberFormat}</dd></div><div><dt>精度 / 舍入</dt><dd>{definition.decimalScale} 位 · {definition.roundingMode}</dd></div></dl></section>
            <section className="metric-source-hero candidate-source-hero"><div className="metric-source-icon"><DatabaseIcon size={24} weight="bold" /></div><div><span className="eyebrow">候选来源</span><h4>{datasetName || datasetVersion?.dsl.dataset.name || shortId(candidate.datasetId)}</h4><p>{datasetVersion ? `V${datasetVersion.versionNo} · ${datasetVersion.status}` : '精确版本元数据不可用'}</p></div><dl><div><dt>数据集版本</dt><dd title={candidate.datasetVersionId}>{shortId(candidate.datasetVersionId)}</dd></div><div><dt>DSL 摘要</dt><dd title={candidate.dslHash}>{shortId(candidate.dslHash)}</dd></div><div><dt>来源字段</dt><dd>{candidate.sourceFieldIds.length}</dd></div></dl></section>
            <section className="metric-detail-section candidate-dimension-summary"><span className="eyebrow">建议分组范围</span><h4>{definition.allowedDimensions.length} 个允许维度</h4><p>{definition.allowedDimensions.length ? definition.allowedDimensions.map(dimension => `${dimension.name}（${fieldName(fields, dimension.fieldId)}）`).join('、') : '当前候选未开放分组维度。'}</p></section>
            <details className="metric-catalog-json"><summary>查看候选完整定义 JSON</summary><pre aria-label="候选指标完整定义 JSON">{JSON.stringify(definition, null, 2)}</pre></details>
          </div>}
          {!loading && tab === 'evidence' && <CandidateEvidence candidate={candidate} />}
        </div>
      </div>
      <footer><span>{candidate.status === 'ACCEPTED' ? '候选已转为指标草稿' : candidate.status === 'REJECTED' ? '候选已由人工拒绝' : '接受与拒绝都使用当前候选版本进行乐观锁校验'}</span><div><button className="quiet-button" type="button" disabled={actionBusy} onClick={onClose}>关闭</button>{candidate.acceptedMetricId && <button className="primary-button" type="button" onClick={() => onEditMetric(candidate.acceptedMetricId!)}>编辑指标草稿</button>}{reviewable && !rejecting && <><button className="metric-candidate-reject" type="button" disabled={actionBusy} onClick={onStartReject}>拒绝候选</button><button className="primary-button" type="button" disabled={actionBusy || !canAccept} title={!canAccept ? '候选存在阻塞项或精确来源暂时不可读' : ''} onClick={onAccept}>{actionBusy ? '正在接受…' : '接受并创建草稿'}</button></>}</div></footer>
    </section>
  </div>
}

function CandidateEvidence({ candidate }: { candidate: MetricCandidate }) {
  return <div className="metric-candidate-evidence">
    <section className="metric-candidate-provenance"><header><div><span className="eyebrow">字段级证据</span><h4>为什么生成这个候选</h4></div><span>{candidateMethodLabels[candidate.method] ?? candidate.method}</span></header>{candidate.evidence.length ? <div>{candidate.evidence.map((item, index) => <article key={`${item.property}:${item.source}:${index}`}><strong>{item.property}</strong><span>{item.source}</span><p>{item.detail}</p></article>)}</div> : <div className="metric-detail-state compact"><MagicWandIcon size={30} /><strong>暂无可展示的生成证据</strong></div>}</section>
    <section className="metric-candidate-audit"><span className="eyebrow">不可变追踪信息</span><dl className="metric-fact-grid"><div><dt>候选指纹</dt><dd title={candidate.fingerprint}>{shortId(candidate.fingerprint)}</dd></div><div><dt>候选版本</dt><dd>V{candidate.version}</dd></div><div><dt>数据集版本</dt><dd title={candidate.datasetVersionId}>{shortId(candidate.datasetVersionId)}</dd></div><div><dt>DSL 摘要</dt><dd title={candidate.dslHash}>{shortId(candidate.dslHash)}</dd></div></dl></section>
    <CandidateNotes title="提取假设" tone="assumption" items={candidate.assumptions} empty="未记录额外假设" />
    <CandidateNotes title="风险警告" tone="warning" items={candidate.warnings} empty="未检测到额外警告" />
    <CandidateNotes title="阻塞原因" tone="blocked" items={candidate.blockReasons} empty="当前没有阻塞项" />
  </div>
}

function CandidateNotes({ title, tone, items, empty }: { title: string; tone: string; items: string[]; empty: string }) {
  return <section className={`metric-candidate-notes ${tone}`}><h4>{title}</h4>{items.length ? <ul>{items.map((item, index) => <li key={`${item}:${index}`}>{item}</li>)}</ul> : <p>{empty}</p>}</section>
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
