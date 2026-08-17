import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowClockwise,
  ArrowLeft,
  ArrowRight,
  ArrowsClockwise,
  CheckCircle,
  Circle,
  Database,
  Eye,
  FloppyDisk,
  Funnel,
  Lock,
  MagnifyingGlass,
  MinusCircle,
  Plus,
  Sparkle,
  SpinnerGap,
  Table,
  WarningCircle,
  X,
  XCircle,
} from '@phosphor-icons/react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import { layerFromTags, replaceLayerTag, warehouseLayerLabels, warehouseLayers, type WarehouseLayer } from '../lib/warehouse-layer'
import '../styles/data-source-assets.css'
import {
  dataSourceAPI,
  type DataSourceColumnRecord,
  type DataSourceRecord,
  type DataSourceTablePreview,
  type DataSourceTableRecord,
  type DiscoveredTableRecord,
  type MetadataDiff,
  type MetadataJob,
  type MetadataJobStage,
  type MetadataRefreshMode,
  type MetadataSampleMode,
} from '../lib/data-sources'

type Notice = { tone: 'success' | 'error'; message: string }
type AssetMode = 'catalog' | 'discover' | 'detail'

const snapshotSource: DataSourceRecord = {
  id: 'snapshot-sales-mysql', tenantId: 'snapshot-tenant', code: 'sales_core', name: '销售业务核心库',
  description: '销售订单、渠道与客户经营数据', ownerId: 'snapshot-user', domainId: 'snapshot-enterprise-operations',
  visibility: 'TENANT_PUBLIC', sharingScope: 'DOMAIN', type: 'MYSQL', status: 'ACTIVE',
  config: { host: 'sales-db.internal', port: 3306, database: 'sales_prod', username: 'report_reader' },
  configVersionId: 'snapshot-sales-config-v3', publishedVersionId: 'snapshot-sales-published-v3', configVersion: 3,
  publishedConfigVersion: 3, validationStatus: 'PASSED', publicationStatus: 'PUBLISHED', hasUnpublishedChanges: false,
  reviewStatus: 'APPROVED', lastTestedAt: '2026-08-11T09:18:00+08:00', updatedAt: '2026-08-11T09:18:00+08:00', version: 8,
}

const makeSnapshotTable = (
  id: string,
  tableName: string,
  businessName: string,
  columnCount: number,
  enrichmentStatus: DataSourceTableRecord['enrichmentStatus'],
  sensitivityLevel: DataSourceTableRecord['sensitivityLevel'],
  businessDescription: string,
  tags: string[],
  live: Partial<Pick<DataSourceTableRecord, 'lockedColumnCount' | 'refreshState' | 'refreshStage' | 'refreshNote'>> = {},
): DataSourceTableRecord => ({
  id, dataSourceId: snapshotSource.id, catalogName: 'sales_prod', schemaName: 'sales', tableName, tableType: 'BASE TABLE',
  sourceComment: '', businessName, businessDescription, tags, sensitivityLevel, visibility: 'TENANT_PUBLIC',
  assetStatus: 'ACTIVE', businessVersion: 3, structureHash: 'a'.repeat(64), managementStatus: 'ENABLED', enrichmentStatus,
  columnCount, metadataVersion: 5, lastSyncAt: '2026-08-11T10:18:00+08:00', ...live,
})

const snapshotTables: DataSourceTableRecord[] = [
  makeSnapshotTable('snapshot-table-orders', 'fact_sales_order', '销售订单事实表', 24, 'SUCCEEDED', 'CONFIDENTIAL', '按订单行记录销售、优惠、渠道和履约结果。', ['主题:经营分析', '作用:事实表', '粒度:订单']),
  makeSnapshotTable('snapshot-table-customer', 'dim_customer', '客户主数据', 18, 'SUCCEEDED', 'CONFIDENTIAL', '客户身份、分群与生命周期状态。', ['作用:主数据', '粒度:客户'], { lockedColumnCount: 4, refreshState: 'SUCCEEDED', refreshStage: 'COMPLETE' }),
  makeSnapshotTable('snapshot-table-channel', 'dim_channel', '销售渠道维表', 12, 'SUCCEEDED', 'INTERNAL', '渠道层级、组织归属与经营类型。', ['作用:维度表', '范围:运营分析']),
  makeSnapshotTable('snapshot-table-product', 'dim_product', '商品主数据', 31, 'SUCCEEDED', 'INTERNAL', '商品、品类、品牌和上市状态信息。', ['作用:主数据', '粒度:产品']),
  makeSnapshotTable('snapshot-table-payment', 'fact_payment', '支付流水', 22, 'RUNNING', 'RESTRICTED', '支付尝试、渠道回执与退款状态。', ['功能:业务流水'], { refreshState: 'RUNNING', refreshStage: 'LLM' }),
  makeSnapshotTable('snapshot-table-store', 'dim_store', '门店维表', 16, 'PENDING', 'INTERNAL', '', ['作用:维度表'], { refreshState: 'QUEUED', refreshStage: 'QUEUED' }),
  makeSnapshotTable('snapshot-table-campaign', 'fact_campaign_touch', '营销触达明细', 27, 'FAILED', 'CONFIDENTIAL', '', ['范围:客户分析'], { refreshState: 'FAILED', refreshStage: 'LLM', refreshNote: 'LLM 表结构完善失败，请重试或改为手工完善' }),
  makeSnapshotTable('snapshot-table-calendar', 'dim_calendar', '自然日历', 9, 'SUCCEEDED', 'PUBLIC', '标准自然日、周、月、季度和财年映射。', ['作用:维度表', '粒度:日']),
]

const snapshotColumns: DataSourceColumnRecord[] = [
  ['order_id', '订单编号', '订单全局唯一标识', 'STRING', 'IDENTIFIER', 'INTERNAL', false],
  ['customer_id', '客户编号', '关联客户主数据的稳定标识', 'STRING', 'IDENTIFIER', 'CONFIDENTIAL', false],
  ['channel_code', '渠道编码', '订单归属渠道编码', 'STRING', 'CATEGORY', 'INTERNAL', false],
  ['order_time', '下单时间', '客户完成下单的业务时间', 'TIMESTAMP', 'DATETIME', 'INTERNAL', false],
  ['pay_amount', '实付金额', '扣除优惠后的含税实付金额', 'DECIMAL', 'AMOUNT', 'CONFIDENTIAL', false],
  ['discount_amount', '优惠金额', '订单使用的优惠总额', 'DECIMAL', 'AMOUNT', 'INTERNAL', false],
  ['order_status', '订单状态', '订单当前业务状态', 'STRING', 'CATEGORY', 'INTERNAL', false],
  ['region_name', '区域名称', '订单履约门店所属经营区域', 'STRING', 'REGION', 'INTERNAL', false],
  ['sales_quantity', '销售数量', '订单行商品销售数量', 'INTEGER', 'QUANTITY', 'INTERNAL', false],
  ['refund_flag', '退款标记', '是否发生退款或退货', 'BOOLEAN', 'BOOLEAN', 'INTERNAL', false],
].map((item, index) => ({
  id: `snapshot-column-${index + 1}`, tableId: 'snapshot-table-orders', columnName: item[0] as string,
  ordinalPosition: index + 1, sourceComment: '', nativeType: item[3] as string, canonicalType: item[3] as string,
  nullable: index > 0, businessName: item[1] as string, businessDescription: item[2] as string, tags: [],
  sensitivityLevel: item[5] as DataSourceColumnRecord['sensitivityLevel'], semanticType: item[4] as string,
  manualLocked: item[6] as boolean, assetStatus: 'ACTIVE', businessVersion: 2,
}))

const discoveredNames = [
  ['fact_sales_order', '订单交易主表', 1052341, 24],
  ['dim_customer', '客户主数据', 286420, 18],
  ['dim_channel', '渠道主数据', 68, 12],
  ['dim_product', '商品主数据', 18320, 31],
  ['fact_payment', '支付流水', 1288665, 22],
  ['dim_store', '门店档案', 1246, 16],
  ['fact_campaign_touch', '营销触达明细', 3462088, 27],
  ['dim_calendar', '自然日历', 3653, 9],
  ['fact_order_item', '订单商品明细', 4238500, 19],
  ['fact_after_sales', '售后服务单', 186420, 21],
] as const

const snapshotDiscovered: DiscoveredTableRecord[] = discoveredNames.map(([name, comment, rows, columns]) => ({
  catalogName: 'sales_prod', schemaName: 'sales', name, type: 'BASE TABLE', sourceComment: comment,
  estimatedRowCount: rows, columns: Array.from({ length: columns }, (_, index) => ({ name: `field_${index + 1}`, nativeType: 'VARCHAR', canonicalType: 'STRING', nullable: true })),
}))

const snapshotJob: MetadataJob = {
  id: 'snapshot-job-import', dataSourceId: snapshotSource.id, kind: 'IMPORT', mode: 'INCREMENTAL', sampleDataMode: 'MASK', samplePolicyVersion: 1,
  status: 'RUNNING', stage: 'LLM', total: 8, completed: 5, succeeded: 3, skipped: 1, failed: 1, currentTable: 'fact_payment',
  createdAt: '2026-08-11T10:16:00+08:00', startedAt: '2026-08-11T10:16:04+08:00',
  failures: [{ schemaName: 'sales', tableName: 'fact_campaign_touch', errorCode: 'LLM_COMPLETION_FAILED', errorMessage: 'LLM 表结构完善失败，请重试；若持续失败可先手工完善表名、字段和标签' }],
  logs: [
    { timestamp: '2026-08-11T10:16:04+08:00', level: 'SUCCESS', stage: 'DISCOVERY', message: '已读取源库结构，共发现 10 张数据表' },
    { timestamp: '2026-08-11T10:16:18+08:00', level: 'SUCCESS', stage: 'PERSISTENCE', message: '技术元数据已保存', tableName: 'fact_order_item' },
    { timestamp: '2026-08-11T10:16:25+08:00', level: 'INFO', stage: 'LLM', message: '正在生成业务元数据建议', tableName: 'fact_after_sales', model: 'metadata-copilot' },
  ],
}

const snapshotDiffs: MetadataDiff[] = [
  { id: 'snapshot-diff-breaking', dataSourceId: snapshotSource.id, objectType: 'COLUMN', objectKey: 'sales.fact_sales_order.pay_amount', changeType: 'CHANGED', breaking: true, impactCount: 2, staleCount: 2, createdAt: '2026-08-11T10:18:00+08:00', before: { canonical_type: 'DECIMAL', numeric_precision: 18, numeric_scale: 2 }, after: { canonical_type: 'INTEGER', numeric_precision: 12, numeric_scale: 0 }, impact: [
    { id: 'snapshot-dependency-1', downstreamType: 'DATASET', downstreamId: 'snapshot-sales-dataset', downstreamName: '销售经营明细', kind: 'USES', createdAt: '2026-08-11T10:18:00+08:00' },
    { id: 'snapshot-dependency-2', downstreamType: 'DATASET', downstreamId: 'snapshot-channel-dataset', downstreamName: '渠道经营汇总', kind: 'USES', createdAt: '2026-08-11T10:18:00+08:00' },
  ] },
  { id: 'snapshot-diff-safe', dataSourceId: snapshotSource.id, objectType: 'COLUMN', objectKey: 'sales.dim_customer.customer_level', changeType: 'ADDED', breaking: false, impactCount: 0, staleCount: 0, createdAt: '2026-08-11T10:17:00+08:00', after: { canonical_type: 'STRING', nullable: true } },
]

const semanticTypes = ['', 'DATE', 'TIME', 'DATETIME', 'REGION', 'COMPANY_NAME', 'AMOUNT', 'PERCENTAGE', 'IDENTIFIER', 'CATEGORY', 'QUANTITY', 'BOOLEAN', 'TEXT']
const sensitivityLabels: Record<DataSourceTableRecord['sensitivityLevel'], string> = { PUBLIC: '公开', INTERNAL: '内部', CONFIDENTIAL: '机密', RESTRICTED: '受限' }
const enrichmentLabels: Record<DataSourceTableRecord['enrichmentStatus'], string> = { PENDING: '待完善', RUNNING: '处理中', SUCCEEDED: '已完善', FAILED: '需处理' }
const stageLabels: Record<string, string> = { QUEUED: '等待执行', DISCOVERY: '结构发现', DIFF: '结构比对', SAMPLE: '样本处理', PERSISTENCE: '技术元数据', LLM: '业务元数据', COMPLETE: '完成', FAILED: '失败' }
const jobStatusLabels: Record<MetadataJob['status'], string> = { QUEUED: '排队中', RUNNING: '处理中', SUCCEEDED: '已完成', PARTIAL: '部分完成', FAILED: '失败' }
// 任务节点按后台真实阶段枚举展开，页面不再把结构比对和样本处理折叠掉，
// 否则用户无法判断刷新究竟停在哪一步。
const jobStageOrder: MetadataJobStage[] = ['QUEUED', 'DISCOVERY', 'DIFF', 'SAMPLE', 'PERSISTENCE', 'LLM', 'COMPLETE']
const jobNodes = ['DISCOVERY', 'DIFF', 'SAMPLE', 'PERSISTENCE', 'LLM', 'COMPLETE'] as const
const jobStageDetails: Record<string, string> = {
  DISCOVERY: '读取源库表与字段', DIFF: '比对结构变化与影响', SAMPLE: '按授权读取脱敏样本',
  PERSISTENCE: '保存稳定技术标识', LLM: '生成业务定义建议', COMPLETE: '资产可进入数据集建模',
}
type JobNodeState = 'complete' | 'current' | 'failed' | 'pending'
const jobNodeStateLabels: Record<JobNodeState, string> = { complete: '已完成', current: '进行中', failed: '未通过', pending: '待执行' }

// jobNodeState 只根据已落库的任务状态推导节点状态，避免前端凭时间猜测进度。
const jobNodeState = (job: MetadataJob, node: string): JobNodeState => {
  const index = jobStageOrder.indexOf(node as MetadataJobStage)
  const active = jobStageOrder.indexOf(job.stage)
  if (job.status === 'SUCCEEDED') return 'complete'
  if (job.status === 'PARTIAL') return node === 'COMPLETE' ? 'failed' : 'complete'
  if (job.status === 'FAILED') {
    // stage 落在 FAILED 时后台没有留下失败节点，只能按是否已有完成表回退判断。
    if (active < 0) return node === 'COMPLETE' ? 'failed' : job.completed > 0 ? 'complete' : 'pending'
    if (index < active) return 'complete'
    return index === active ? 'failed' : 'pending'
  }
  if (active < 0 || index > active) return 'pending'
  return index < active ? 'complete' : 'current'
}

const jobNodeIcon = (state: JobNodeState) => state === 'complete'
  ? <CheckCircle size={17} weight="fill" />
  : state === 'failed'
    ? <XCircle size={17} weight="fill" />
    : state === 'current'
      ? <SpinnerGap size={17} weight="bold" />
      : <Circle size={17} />

const renderJobStages = (job: MetadataJob) => <ol className="asset-job-stages">{jobNodes.map(node => {
  const state = jobNodeState(job, node)
  return <li className={`is-${state}`} key={node}>
    {jobNodeIcon(state)}
    <span><strong>{stageLabels[node]}</strong><small>{jobStageDetails[node]}</small></span>
    <em>{jobNodeStateLabels[state]}</em>
  </li>
})}</ol>

const jobPercent = (job: MetadataJob) => job.total > 0 ? Math.min(100, Math.round(job.completed / job.total * 100)) : job.status === 'SUCCEEDED' ? 100 : 0
const jobKindLabel = (job: MetadataJob) => job.kind === 'IMPORT'
  ? '导入并完善元数据'
  : job.mode === 'FULL' ? '全量刷新元数据' : '增量刷新元数据'
// metadataJobSignature 是页面拉取逐表状态的触发条件：任何一张表推进都会改变它。
const metadataJobSignature = (job: MetadataJob) => [job.status, job.stage, job.completed, job.succeeded, job.skipped, job.failed, job.currentTable || ''].join('|')

// renderJobProgress 是侧边卡片与详情弹窗共用的完整进度视图：百分比、进度条、
// 成功 / 跳过 / 失败 / 待处理计数，以及当前所处节点和正在处理的表。
const renderJobProgress = (job: MetadataJob) => <section className="asset-job-progress">
  <div className="asset-job-progress-head"><strong title={jobKindLabel(job)}>{jobKindLabel(job)}</strong><span>{jobPercent(job)}%</span></div>
  <progress max={Math.max(1, job.total)} value={job.completed} />
  <ul className="asset-job-counters">
    <li><strong>{job.completed}/{job.total}</strong><small>已处理</small></li>
    <li className="is-green"><strong>{job.succeeded}</strong><small>成功</small></li>
    <li className="is-grey"><strong>{job.skipped}</strong><small>跳过</small></li>
    <li className="is-red"><strong>{job.failed}</strong><small>失败</small></li>
    <li><strong>{Math.max(0, job.total - job.completed)}</strong><small>待处理</small></li>
  </ul>
  <p className="asset-job-current">
    <em>{stageLabels[job.stage] || job.stage}</em>
    <small title={job.currentTable}>{job.currentTable ? `正在处理 ${job.currentTable}` : isJobActive(job) ? '正在调度下一张表' : '所有数据表已形成终态'}</small>
  </p>
</section>

const renderJobFailures = (job: MetadataJob) => {
  const failures = job.failures || []
  if (!job.errorMessage && failures.length === 0) return null
  return <section className="asset-job-failures" aria-label="失败信息">
    <header><WarningCircle size={15} weight="fill" /><strong>失败信息</strong><em>{job.failed} 张</em></header>
    {job.errorMessage && <p>{job.errorMessage}</p>}
    <ul>{failures.map((failure, index) => <li key={`${failure.tableName}-${index}`}>
      <strong title={failure.tableName}>{failure.tableName}</strong>
      <small title={failure.errorMessage || failure.errorCode}>{failure.errorMessage || failure.errorCode || '处理失败'}</small>
    </li>)}</ul>
  </section>
}

// 锁定只存在于字段级；表级业务元数据始终参与刷新。
const lockedFieldCount = (table: DataSourceTableRecord) => table.lockedColumnCount || 0

const diffChangeLabels: Record<string, string> = { REMOVED: '已移除', ADDED: '新增字段', REACTIVATED: '重新启用' }

type TableProgress = { tone: string; label: string; detail: string }
// tableProgress 优先使用运行中任务的逐表状态；任务结束后回落到已落库的完善结果，
// 因此页面在刷新期间是执行视图，平时仍是资产完善视图。
const tableProgress = (table: DataSourceTableRecord): TableProgress => {
  switch (table.refreshState) {
    case 'QUEUED':
      return { tone: 'queued', label: '待刷新', detail: '已排队，等待处理' }
    case 'RUNNING':
      return { tone: 'running', label: '刷新中', detail: stageLabels[table.refreshStage || ''] || '正在处理' }
    case 'SKIPPED':
      return { tone: 'skipped', label: '已跳过', detail: table.refreshNote || '本次刷新未处理该表' }
    case 'FAILED':
      return { tone: 'failed', label: '需处理', detail: table.refreshNote || '本次刷新处理失败' }
    default:
      return { tone: table.enrichmentStatus.toLowerCase(), label: enrichmentLabels[table.enrichmentStatus], detail: formatTime(table.lastSyncAt) }
  }
}

const tagsFromText = (value: string) => [...new Set(value.split(',').map(item => item.trim()).filter(Boolean))]
const tableKey = (table: Pick<DiscoveredTableRecord, 'catalogName' | 'schemaName' | 'name'>) => `${table.catalogName}\u001f${table.schemaName}\u001f${table.name}`
const importedKey = (table: DataSourceTableRecord) => `${table.catalogName}\u001f${table.schemaName}\u001f${table.tableName}`
const formatCount = (value?: number) => value === undefined ? '—' : new Intl.NumberFormat('zh-CN').format(value)
const formatTime = (value?: string) => {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
const isJobActive = (job: MetadataJob | null) => job?.status === 'QUEUED' || job?.status === 'RUNNING'
const querySuffix = (snapshot: boolean, qa: boolean, extra = '') => {
  if (!snapshot && !extra) return ''
  const query = new URLSearchParams()
  if (snapshot) query.set('snapshot', 'data-source-assets')
  if (qa) query.set('qa', '1920')
  if (extra) {
    const values = new URLSearchParams(extra)
    values.forEach((value, key) => query.set(key, value))
  }
  return `?${query.toString()}`
}

function PageNotice({ notice, onClose }: { notice: Notice | null; onClose: () => void }) {
  if (!notice) return null
  return <div className={`asset-page-notice is-${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}>
    {notice.tone === 'success' ? <CheckCircle size={18} weight="fill" /> : <WarningCircle size={18} weight="fill" />}
    <span>{notice.message}</span><AppButton text circle aria-label="关闭消息" onClick={onClose}><X size={15} /></AppButton>
  </div>
}

export function DataSourceAssetsPage() {
  const { sourceId = '', tableId = '' } = useParams()
  const location = useLocation()
  const navigate = useNavigate()
  const params = useMemo(() => new URLSearchParams(location.search), [location.search])
  const designSnapshot = import.meta.env.DEV && params.has('snapshot')
  const qaViewport1920 = designSnapshot && params.get('qa') === '1920'
  const emptySnapshot = designSnapshot && params.get('state') === 'empty'
  const mode: AssetMode = location.pathname.endsWith('/discover') ? 'discover' : tableId ? 'detail' : 'catalog'
  const suffix = querySuffix(designSnapshot, qaViewport1920)
  const snapshotTableForRoute = snapshotTables.find(item => item.id === tableId) || snapshotTables[0]
  const snapshotColumnsForRoute = snapshotTableForRoute.id === snapshotTables[0].id ? snapshotColumns : snapshotColumns.slice(0, 6).map((column, index) => ({ ...column, id: `${snapshotTableForRoute.id}-column-${index}`, tableId: snapshotTableForRoute.id }))
  const [source, setSource] = useState<DataSourceRecord | null>(designSnapshot ? snapshotSource : null)
  const [canManage, setCanManage] = useState(designSnapshot)
  const [tables, setTables] = useState<DataSourceTableRecord[]>(designSnapshot && !emptySnapshot ? snapshotTables : [])
  const [tableRecord, setTableRecord] = useState<DataSourceTableRecord | null>(designSnapshot ? snapshotTableForRoute : null)
  const [columns, setColumns] = useState<DataSourceColumnRecord[]>(designSnapshot ? snapshotColumnsForRoute : [])
  const [originalColumns, setOriginalColumns] = useState<DataSourceColumnRecord[]>(designSnapshot ? snapshotColumnsForRoute : [])
  const [discovered, setDiscovered] = useState<DiscoveredTableRecord[]>(designSnapshot ? snapshotDiscovered : [])
  const [job, setJob] = useState<MetadataJob | null>(designSnapshot && params.get('job') === 'importing' ? snapshotJob : null)
  const [diffs, setDiffs] = useState<MetadataDiff[]>(designSnapshot && !emptySnapshot ? snapshotDiffs : [])
  const [loading, setLoading] = useState(!designSnapshot)
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [keyword, setKeyword] = useState('')
  const [statusFilter, setStatusFilter] = useState('ALL')
  const [selectedKeys, setSelectedKeys] = useState<string[]>([])
  const [sampleMode, setSampleMode] = useState<MetadataSampleMode>('MASK')
  const [tableForm, setTableForm] = useState({ businessName: snapshotTableForRoute.businessName, businessDescription: snapshotTableForRoute.businessDescription, tags: snapshotTableForRoute.tags.join(', '), sensitivityLevel: snapshotTableForRoute.sensitivityLevel, visibility: snapshotTableForRoute.visibility })
  const [preview, setPreview] = useState<DataSourceTablePreview | null>(null)
  const [sidePanel, setSidePanel] = useState<'diffs' | 'job' | null>(null)
  const jobSignature = useRef('')
  const selectAllRef = useRef<HTMLInputElement>(null)
  const visibleJob = designSnapshot && params.get('job') === 'importing' ? snapshotJob : job
  const breakingDiffs = diffs.filter(diff => diff.breaking)

  useEffect(() => {
    if (!notice) return undefined
    const timeout = window.setTimeout(() => setNotice(null), 4200)
    return () => window.clearTimeout(timeout)
  }, [notice])

  useEffect(() => {
    if (designSnapshot) return
    let active = true
    const load = async () => {
      try {
        const loadedSource = await dataSourceAPI.get(sourceId)
        if (!active) return
        setSource(loadedSource)
        const manageAllowed = designSnapshot || (await dataSourceAPI.evaluatePermission(sourceId, 'MANAGE')).allowed
        if (!active) return
        setCanManage(manageAllowed)
        if (!manageAllowed && mode === 'discover') {
          setNotice({ tone: 'error', message: '当前数据源由其他成员共享，你可以查看资产，但不能发现或导入数据表' })
          navigate(`/data-sources/${sourceId}/assets${suffix}`, { replace: true })
          return
        }
        if (mode === 'catalog') {
          const [tablePage, activeJob, metadataDiffs] = await Promise.all([dataSourceAPI.tables(sourceId), params.get('jobId')
            ? dataSourceAPI.getMetadataJob(sourceId, params.get('jobId')!)
            : dataSourceAPI.latestMetadataJob(sourceId).then(result => result.job), dataSourceAPI.metadataDiffs(sourceId)])
          if (!active) return
          setTables(tablePage.items)
          setJob(activeJob)
          setDiffs(metadataDiffs.items)
        } else if (mode === 'discover') {
          const result = loadedSource.type === 'EXCEL'
            ? await dataSourceAPI.inspectExcelSource(sourceId).then(inspection => ({ items: inspection.sheets.map(sheet => ({ catalogName: '', schemaName: '', name: sheet.name, type: 'WORKSHEET', sourceComment: '', columns: sheet.columns.map(column => ({ name: column.name, nativeType: column.canonicalType, canonicalType: column.canonicalType, nullable: column.nullable })) })), total: inspection.sheets.length }))
            : await dataSourceAPI.discoverTables(sourceId)
          const imported = await dataSourceAPI.tables(sourceId)
          if (!active) return
          setDiscovered(result.items)
          setTables(imported.items)
        } else if (tableId) {
          const [loadedTable, loadedColumns] = await Promise.all([
            dataSourceAPI.table(tableId), dataSourceAPI.columns(tableId),
          ])
          if (!active) return
          setTableRecord(loadedTable)
          setColumns(loadedColumns.items)
          setOriginalColumns(loadedColumns.items)
          setTableForm({ businessName: loadedTable.businessName, businessDescription: loadedTable.businessDescription, tags: loadedTable.tags.join(', '), sensitivityLevel: loadedTable.sensitivityLevel, visibility: loadedTable.visibility })
        }
      } catch (cause) {
        if (active) setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载数据源资产失败' })
      } finally {
        if (active) setLoading(false)
      }
    }
    void load()
    return () => { active = false }
  }, [designSnapshot, mode, navigate, params, sourceId, suffix, tableId])

  // 任务运行期间页面是一个执行视图：每当任一张表推进（阶段、成功、跳过或失败），
  // 就同步重新拉取表资产，使行状态和概览指标与任务进度一起增量更新，
  // 而不是等整批完成后才一次性刷新。
  useEffect(() => {
    if (designSnapshot || !job || !isJobActive(job)) return undefined
    let active = true
    const timeout = window.setTimeout(() => {
      void dataSourceAPI.getMetadataJob(sourceId, job.id).then(next => {
        if (!active) return
        setJob(next)
        const signature = metadataJobSignature(next)
        const settled = !isJobActive(next)
        if (signature === jobSignature.current && !settled) return
        jobSignature.current = signature
        void dataSourceAPI.tables(sourceId)
          .then(result => { if (active) setTables(result.items) })
          .catch(() => undefined)
        if (settled) {
          void dataSourceAPI.metadataDiffs(sourceId)
            .then(result => { if (active) setDiffs(result.items) })
            .catch(() => undefined)
        }
      }).catch(() => undefined)
    }, 1200)
    return () => {
      active = false
      window.clearTimeout(timeout)
    }
  }, [designSnapshot, job, sourceId])

  const imported = useMemo(() => new Set(tables.map(importedKey)), [tables])
  // 概览指标与表格行使用同一份实时状态，两者在刷新过程中始终一致。
  const summary = useMemo(() => tables.reduce((totals, table) => {
    const { tone } = tableProgress(table)
    if (tone === 'succeeded') totals.succeeded += 1
    else if (tone === 'failed') totals.failed += 1
    else if (tone === 'skipped') totals.skipped += 1
    else totals.processing += 1
    return totals
  }, { succeeded: 0, processing: 0, failed: 0, skipped: 0 }), [tables])
  // 筛选跟随行上显示的实时状态，否则刷新期间筛选结果会和可见状态对不上。
  const filteredTables = useMemo(() => tables.filter(table => {
    const query = keyword.trim().toLocaleLowerCase()
    const matchesSearch = !query || table.tableName.toLocaleLowerCase().includes(query) || table.businessName.toLocaleLowerCase().includes(query) || table.businessDescription.toLocaleLowerCase().includes(query)
    const { tone } = tableProgress(table)
    const matchesStatus = statusFilter === 'ALL'
      || (statusFilter === 'RUNNING' ? tone === 'running' || tone === 'queued' : tone === statusFilter.toLocaleLowerCase())
    return matchesSearch && matchesStatus
  }), [keyword, statusFilter, tables])
  const filteredDiscovered = useMemo(() => discovered.filter(table => {
    const query = keyword.trim().toLocaleLowerCase()
    return !query || table.name.toLocaleLowerCase().includes(query) || table.sourceComment.toLocaleLowerCase().includes(query)
  }).sort((left, right) => Number(imported.has(tableKey(left))) - Number(imported.has(tableKey(right)))), [discovered, imported, keyword])
  const selectedKeySet = useMemo(() => new Set(selectedKeys), [selectedKeys])
  const selectableDiscoveredKeys = useMemo(() => filteredDiscovered
    .filter(table => !imported.has(tableKey(table)))
    .map(tableKey), [filteredDiscovered, imported])
  const selectedSelectableCount = selectableDiscoveredKeys.filter(key => selectedKeySet.has(key)).length
  const allSelectableDiscoveredSelected = selectableDiscoveredKeys.length > 0
    && selectedSelectableCount === selectableDiscoveredKeys.length
  const someSelectableDiscoveredSelected = selectedSelectableCount > 0 && !allSelectableDiscoveredSelected

  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = someSelectableDiscoveredSelected
  }, [someSelectableDiscoveredSelected])

  const toggleAllDiscovered = (checked: boolean) => {
    setSelectedKeys(current => {
      const visibleKeys = new Set(selectableDiscoveredKeys)
      return checked
        ? Array.from(new Set([...current, ...selectableDiscoveredKeys]))
        : current.filter(key => !visibleKeys.has(key))
    })
  }
  const tableReadiness = useMemo(() => {
    if (!tableRecord || columns.length === 0) return 0
    const tableComplete = Boolean(tableForm.businessName.trim() && tableForm.businessDescription.trim())
    const completeColumns = columns.filter(column => column.businessName.trim() && column.businessDescription.trim() && column.semanticType.trim()).length
    return Math.round(((tableComplete ? 1 : 0) + completeColumns) / (columns.length + 1) * 100)
  }, [columns, tableForm.businessDescription, tableForm.businessName, tableRecord])
  const goCatalog = () => navigate(`/data-sources/${sourceId}/assets${suffix}`)
  const goDiscover = () => navigate(`/data-sources/${sourceId}/assets/discover${suffix}`)
  const goDetail = (id: string) => navigate(`/data-sources/${sourceId}/assets/${id}${suffix}`)

  const importSelected = async () => {
    const selected = discovered.filter(table => selectedKeys.includes(tableKey(table)))
    if (selected.length === 0) {
      setNotice({ tone: 'error', message: '请至少选择一张尚未导入的数据表' })
      return
    }
    setBusy('import')
    try {
      if (designSnapshot) {
        navigate(`/data-sources/${sourceId}/assets${querySuffix(true, qaViewport1920, 'job=importing')}`)
        return
      }
      const created = await dataSourceAPI.importTables(sourceId, selected.map(item => ({ catalogName: item.catalogName, schemaName: item.schemaName, tableName: item.name })), sampleMode)
      navigate(`/data-sources/${sourceId}/assets?jobId=${encodeURIComponent(created.id)}`)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '提交元数据导入失败' })
    } finally {
      setBusy('')
    }
  }

  // 全量 / 单表刷新都走 FULL，否则结构未变的字段会被增量模式跳过。
  // 锁定只保护字段：表级定义和未锁定字段照常刷新。
  const refreshAssets = async (mode: MetadataRefreshMode, table?: DataSourceTableRecord) => {
    setBusy(table ? `refresh:${table.id}` : `refresh:${mode}`)
    try {
      if (designSnapshot) {
        setJob({ ...snapshotJob, kind: 'REFRESH', mode })
        setNotice({ tone: 'success', message: `已提交${mode === 'INCREMENTAL' ? '增量' : '全量'}刷新任务` })
        return
      }
      const created = await dataSourceAPI.refreshTables(sourceId, mode, table ? [table.id] : undefined, 'MASK')
      jobSignature.current = ''
      setJob(created)
      setNotice({
        tone: 'success',
        message: table
          ? `已提交“${table.businessName || table.tableName}”的单表刷新任务`
          : mode === 'INCREMENTAL'
            ? '已提交增量刷新任务，仅处理新增或结构变化的字段'
            : '已提交全量刷新任务，将重新处理全部活动表和未锁定字段',
      })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '提交刷新任务失败' })
    } finally {
      setBusy('')
    }
  }

  const updateColumn = (id: string, changes: Partial<DataSourceColumnRecord>) => setColumns(current => current.map(column => column.id === id ? { ...column, ...changes } : column))

  const saveMetadata = async () => {
    if (!tableRecord) return
    if (!tableForm.businessName.trim() || !tableForm.businessDescription.trim()) {
      setNotice({ tone: 'error', message: '请先填写表的业务名称和业务说明' })
      return
    }
    setBusy('save')
    try {
      if (designSnapshot) {
        setTableRecord(current => current ? { ...current, ...tableForm, tags: tagsFromText(tableForm.tags), businessVersion: current.businessVersion + 1 } : current)
        setOriginalColumns(columns)
        setNotice({ tone: 'success', message: '业务元数据已保存，版本冲突保护已生效' })
        return
      }
      const updatedTable = await dataSourceAPI.updateTable(tableRecord.id, {
        businessName: tableForm.businessName.trim(), businessDescription: tableForm.businessDescription.trim(), tags: tagsFromText(tableForm.tags),
        sensitivityLevel: tableForm.sensitivityLevel, visibility: tableForm.visibility, expectedVersion: tableRecord.businessVersion,
      })
      const originals = new Map(originalColumns.map(column => [column.id, column]))
      const changed = columns.filter(column => {
        const original = originals.get(column.id)
        return !original || JSON.stringify([column.businessName, column.businessDescription, column.tags, column.semanticType, column.sensitivityLevel, column.manualLocked]) !== JSON.stringify([original.businessName, original.businessDescription, original.tags, original.semanticType, original.sensitivityLevel, original.manualLocked])
      })
      const savedColumns = new Map((await Promise.all(changed.map(column => dataSourceAPI.updateColumn(column.id, {
        businessName: column.businessName.trim(), businessDescription: column.businessDescription.trim(), tags: column.tags,
        sensitivityLevel: column.sensitivityLevel, semanticType: column.semanticType, manualLocked: column.manualLocked, expectedVersion: column.businessVersion,
      })))).map(column => [column.id, column]))
      const nextColumns = columns.map(column => savedColumns.get(column.id) || column)
      setTableRecord(updatedTable)
      setColumns(nextColumns)
      setOriginalColumns(nextColumns)
      setNotice({ tone: 'success', message: `已保存表信息和 ${changed.length} 个字段变更` })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '保存业务元数据失败' })
    } finally {
      setBusy('')
    }
  }

  const completeMetadata = async () => {
    if (!tableRecord) return
    if (tableReadiness < 100) {
      setNotice({ tone: 'error', message: '仍有表或字段业务元数据未填写完整' })
      return
    }
    setBusy('complete')
    try {
      const completed = designSnapshot ? { ...tableRecord, enrichmentStatus: 'SUCCEEDED' as const } : await dataSourceAPI.completeTableManually(tableRecord.id, tableRecord.businessVersion, tableRecord.structureHash)
      setTableRecord(completed)
      setNotice({ tone: 'success', message: '该数据表已完成资产化，可以进入数据集建模' })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '提交资产完善失败' })
    } finally {
      setBusy('')
    }
  }

  const openPreview = async () => {
    if (!tableRecord) return
    setBusy('preview')
    try {
      const result = designSnapshot ? {
        columns: ['order_id', 'customer_id', 'order_time', 'pay_amount', 'order_status'],
        rows: [['SO20260811001', 'C009821', '2026-08-11 09:28:16', '1288.00', 'PAID'], ['SO20260811002', 'C003416', '2026-08-11 09:29:04', '699.00', 'SHIPPED'], ['SO20260811003', 'C008095', '2026-08-11 09:31:42', '3299.00', 'PAID']], rowCount: 3,
      } : await dataSourceAPI.previewTable(tableRecord.id)
      setPreview(result)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '读取脱敏样本失败' })
    } finally {
      setBusy('')
    }
  }

  const shellClass = `data-source-assets-shell mode-${mode}${qaViewport1920 ? ' qa-viewport-1920' : ''}`
  const title = mode === 'discover' ? '发现数据表' : mode === 'detail' ? (tableRecord?.businessName || tableRecord?.tableName || '完善数据资产') : (source?.name || '数据源')
  const titleMeta = mode === 'discover' ? '选择源表并设置样本策略' : mode === 'detail' ? `${tableRecord?.schemaName || ''}.${tableRecord?.tableName || ''}` : [source?.type, source?.code].filter(Boolean).join(' · ')
  const refreshBlocked = busy.startsWith('refresh:') || isJobActive(visibleJob)
  const renderDiff = (diff: MetadataDiff) => <article className={diff.breaking ? 'is-breaking' : 'is-safe'} key={diff.id}>
    <span>{diff.breaking ? <WarningCircle size={17} weight="fill" /> : <CheckCircle size={17} weight="fill" />}</span>
    <div><strong>{diff.objectKey}</strong><small>{diffChangeLabels[diff.changeType] || '结构已变化'} · {formatTime(diff.createdAt)}</small></div>
    <em>{diff.breaking ? `${diff.staleCount || diff.impactCount} 个下游已冻结` : '兼容'}</em>
    {diff.breaking && diff.impact && diff.impact.length > 0 && <div className="asset-change-impact">
      {diff.impact.map(item => <AppButton link key={item.id} onClick={() => navigate(`/datasets/${item.downstreamId}/edit`)}>{item.downstreamName}<ArrowRight size={13} /></AppButton>)}
    </div>}
  </article>

  return <AppShell
    className={shellClass}
    title={title}
    titleMeta={mode === 'discover'
      ? <span className="asset-page-title-meta">{titleMeta}</span>
      : <span className="asset-page-title-meta">{titleMeta && <span className="asset-title-table"><Database size={13} weight="duotone" />{titleMeta}</span>}</span>}
    leading={<AppButton
      className="asset-back-button"
      aria-label={mode === 'catalog' ? '返回数据源' : '返回资产目录'}
      title={mode === 'catalog' ? '返回数据源' : '返回资产目录'}
      onClick={mode === 'catalog' ? () => navigate(`/data-sources${suffix}`) : goCatalog}
    ><ArrowLeft size={18} /></AppButton>}
    actions={<>
      {mode === 'catalog' && canManage && <>
        <AppButton title="只重新处理新增或结构变化的字段" disabled={refreshBlocked} onClick={() => void refreshAssets('INCREMENTAL')}><ArrowClockwise size={16} />{busy === 'refresh:INCREMENTAL' ? '提交中…' : '增量刷新'}</AppButton>
        <AppButton title="重新处理全部活动表和字段，耗时和模型调用量更高" disabled={refreshBlocked} onClick={() => void refreshAssets('FULL')}><ArrowsClockwise size={16} />{busy === 'refresh:FULL' ? '提交中…' : '全量刷新'}</AppButton>
        <AppButton variant="primary" onClick={goDiscover}><Plus size={16} weight="bold" />发现数据表</AppButton>
      </>}
      {mode === 'detail' && <><AppButton onClick={() => void openPreview()} disabled={busy === 'preview'}><Eye size={16} />预览样本</AppButton>{canManage && <><AppButton onClick={() => void saveMetadata()} disabled={busy === 'save'}><FloppyDisk size={16} />{busy === 'save' ? '保存中…' : '保存'}</AppButton><AppButton variant="primary" onClick={() => void completeMetadata()} disabled={busy === 'complete' || tableReadiness < 100}><CheckCircle size={16} />完成资产化</AppButton></>}</>}
    </>}
  >
    <PageNotice notice={notice} onClose={() => setNotice(null)} />
    <main className="asset-page-content">
      {loading ? <section className="asset-page-state">正在加载数据源资产…</section> : !source ? <section className="asset-page-state is-error">数据源不存在或无权访问</section> : mode === 'catalog' ? <>
        {/* 计数为 0 的指标整体降噪（is-zero），让真正发生的规模和异常先被看到。 */}
        <section className="asset-summary-strip" aria-label="资产概览">
          {([
            ['blue', <Table size={18} weight="duotone" />, '已纳管表', tables.length, '已从源库导入并纳管的表资产数量，不含尚未导入的源表'],
            ['green', <CheckCircle size={18} weight="duotone" />, '已完善', summary.succeeded, '业务元数据已完成且与当前表结构一致'],
            ['orange', <Sparkle size={18} weight="duotone" />, '处理中', summary.processing, '排队或正在后台处理的表，刷新过程中会实时变化'],
            ['grey', <MinusCircle size={18} weight="duotone" />, '本次跳过', summary.skipped, '本次刷新中结构未变化或无需处理而跳过的表'],
            ['red', <WarningCircle size={18} weight="duotone" />, '处理失败', summary.failed, '元数据处理失败，需要重试刷新或手工完善'],
          ] as const).map(([tone, icon, label, value, hint]) => <article className={`is-summary-${tone}${value === 0 ? ' is-zero' : ''}`} key={label} title={hint}>
            <span className="asset-summary-label"><span className={`is-${tone}`}>{icon}</span><small>{label}</small></span>
            <strong>{value}</strong>
          </article>)}
        </section>
        <div className="asset-catalog-layout">
          <section className="asset-table-catalog" aria-label="数据表资产目录">
            <header>
              <div className="asset-catalog-heading"><span><Table size={19} weight="duotone" /></span><div><h2>数据表资产</h2><p>统一维护业务语义、字段定义与敏感级。</p></div></div>
              <span className="asset-catalog-count"><strong>{filteredTables.length}</strong> / {tables.length} 张</span>
            </header>
            <div className="asset-table-filters">
              <label><MagnifyingGlass size={17} /><input aria-label="搜索数据表资产" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索业务名、物理表名或说明" /></label>
              <select aria-label="按完善状态筛选" value={statusFilter} onChange={event => setStatusFilter(event.target.value)}><option value="ALL">全部状态</option><option value="SUCCEEDED">已完善</option><option value="RUNNING">处理中</option><option value="PENDING">待完善</option><option value="SKIPPED">已跳过</option><option value="FAILED">需处理</option></select>
              <AppButton aria-label="重置资产筛选" title="重置筛选" onClick={() => { setKeyword(''); setStatusFilter('ALL') }}><Funnel size={16} />重置</AppButton>
            </div>
            <div className="asset-table-grid" role="table" aria-label="数据表资产">
              <div className="asset-table-grid-head" role="row"><span role="columnheader">资产名称</span><span role="columnheader">物理表</span><span role="columnheader">字段</span><span role="columnheader">完善状态</span><span role="columnheader">敏感级</span><span role="columnheader">操作</span></div>
              <div className="asset-table-grid-body" role="rowgroup">
                {filteredTables.map(item => {
                  const progress = tableProgress(item)
                  return <article className="asset-table-row" role="row" key={item.id}>
                    <AppButton className="asset-table-row-open" aria-label={`${canManage ? '完善' : '查看'}${item.businessName || item.tableName}`} onClick={() => goDetail(item.id)}>
                      <span className="asset-table-name" role="cell"><span className="asset-table-icon"><Table size={19} weight="duotone" /></span><span className="asset-table-copy"><strong title={item.businessName || item.tableName}>{item.businessName || '待补充业务名称'}</strong><small title={item.businessDescription}>{item.businessDescription || '尚未填写业务说明'}</small></span></span>
                      <span role="cell"><strong title={`${item.schemaName}.${item.tableName}`}>{item.tableName}</strong><small title={`${item.catalogName}.${item.schemaName}`}>{[item.catalogName, item.schemaName].filter(Boolean).join('.') || '默认目录'} · {item.tableType}{(item.warehouseLayer ?? layerFromTags(item.tags)) ? ` · ${item.warehouseLayer ?? layerFromTags(item.tags)}` : ''}</small></span>
                      <span role="cell"><strong>{item.columnCount}</strong><small>个字段</small></span>
                      <span role="cell">
                        <span className="asset-status-line">
                          <em className={`asset-status is-${progress.tone}`}>{progress.label}</em>
                          {lockedFieldCount(item) > 0 && <em className="asset-lock-flag" title={`${lockedFieldCount(item)} 项已锁定人工定义，刷新时保留人工定义，其余字段照常更新`}><Lock size={11} weight="fill" />{lockedFieldCount(item)}</em>}
                        </span>
                        <small title={progress.detail}>{progress.detail}</small>
                      </span>
                      <span role="cell"><em className={`asset-sensitivity is-${item.sensitivityLevel.toLowerCase()}`}>{sensitivityLabels[item.sensitivityLevel]}</em><small>{item.visibility === 'TENANT_PUBLIC' ? '领域内共享' : '仅自己'}</small></span>
                    </AppButton>
                    <div className="asset-row-actions" role="cell">
                      {canManage && <AppButton
                        className="asset-row-refresh"
                        aria-label={`刷新${item.businessName || item.tableName}的结构与业务元数据`}
                        title="重新采集该表结构并重新生成业务元数据；已锁定的字段会保留人工定义"
                        disabled={refreshBlocked}
                        onClick={() => void refreshAssets('FULL', item)}
                      ><ArrowClockwise size={15} /></AppButton>}
                      <AppButton link className="asset-row-action" aria-label={`进入${item.businessName || item.tableName}${canManage ? '完善' : '查看'}页`} onClick={() => goDetail(item.id)}>{canManage ? '去完善' : '查看'}<ArrowRight size={13} /></AppButton>
                    </div>
                  </article>
                })}
                {filteredTables.length === 0 && <div className="asset-page-state is-catalog-empty">
                  <span className="asset-empty-visual"><Database size={30} weight="duotone" /></span>
                  <strong>{tables.length === 0 ? '还没有数据表资产' : '没有符合条件的数据表'}</strong>
                  <span>{tables.length === 0 ? '从已发布的数据源发现表结构，生成可检索、可治理的资产目录。' : '调整搜索词或完善状态筛选后重试。'}</span>
                  {canManage && tables.length === 0 && <AppButton variant="primary" onClick={goDiscover}><Plus size={15} weight="bold" />发现数据表</AppButton>}
                  {tables.length > 0 && <AppButton onClick={() => { setKeyword(''); setStatusFilter('ALL') }}><Funnel size={15} />清除筛选</AppButton>}
                </div>}
              </div>
            </div>
          </section>

          <div className="asset-catalog-side">
          {diffs.length > 0 && <section className={`asset-change-governance${breakingDiffs.length > 0 ? ' has-breaking' : ''}`} aria-label="结构变更治理">
            <button className="asset-panel-open" type="button" aria-label="展开结构变更治理详情" onClick={() => setSidePanel('diffs')}>
              <span><WarningCircle size={18} weight="duotone" /></span>
              <div><strong>结构变更治理</strong><small>{breakingDiffs.length > 0 ? `${breakingDiffs.length} 项破坏性变更已触发下游保护` : '最近变更均可向后兼容'}</small></div>
              <em>{diffs.length} 项</em>
            </button>
            <div className="asset-change-list">{diffs.map(renderDiff)}</div>
          </section>}

          <aside className={`asset-job-panel${visibleJob ? ' has-job' : ''}`} aria-label="元数据处理任务">
            <button className="asset-panel-open" type="button" aria-label="展开元数据处理任务详情" onClick={() => setSidePanel('job')}>
              <span className="is-blue"><Sparkle size={18} weight="duotone" /></span>
              <div><strong>元数据处理任务</strong><small>{visibleJob ? `任务 ${visibleJob.id.slice(-8)}` : '后台任务可断线恢复'}</small></div>
              {visibleJob && <em className={`asset-job-status is-${visibleJob.status.toLowerCase()}`}>{jobStatusLabels[visibleJob.status]}</em>}
            </button>
            {!visibleJob ? <div className="asset-job-empty"><CheckCircle size={32} weight="duotone" /><strong>当前没有运行中的任务</strong><p>{canManage ? '发现数据表或增量刷新后，可在这里持续查看处理阶段。' : '这是其他成员共享的数据源，当前以只读方式展示资产状态。'}</p>{canManage && <AppButton onClick={goDiscover}>发现新数据表</AppButton>}</div> : <div className="asset-job-scroll">
              {renderJobProgress(visibleJob)}
              {renderJobStages(visibleJob)}
              {renderJobFailures(visibleJob)}
            </div>}
          </aside>
          </div>
        </div>
      </> : mode === 'discover' ? <div className="asset-discover-layout">
        <section className="asset-discover-catalog">
          <header><div className="asset-discover-heading"><span><Database size={19} weight="duotone" /></span><div><h2>选择需要资产化的数据表</h2><p>已导入表保持锁定，本次仅提交新增选择。</p></div></div><span className="asset-discover-selection"><strong>{selectedKeys.length}</strong> / {discovered.filter(item => !imported.has(tableKey(item))).length} 张</span></header>
          <div className="asset-discover-toolbar">
            <div className="asset-discover-search"><MagnifyingGlass size={17} /><input aria-label="搜索发现的数据表" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索物理表名或源库备注" /></div>
            <label className="asset-discover-select-all" title={`当前结果中有 ${selectableDiscoveredKeys.length} 张尚未导入的数据表`}>
              <input ref={selectAllRef} type="checkbox" checked={allSelectableDiscoveredSelected} disabled={selectableDiscoveredKeys.length === 0} aria-label={keyword.trim() ? '全选当前筛选结果中的可导入数据表' : '全选所有可导入数据表'} onChange={event => toggleAllDiscovered(event.target.checked)} />
              <span>全选</span>
            </label>
          </div>
          <div className="asset-discovered-table" role="table" aria-label="发现的数据表">
            <div className="asset-discovered-head" role="row"><span role="columnheader">选择</span><span role="columnheader">数据表</span><span role="columnheader">字段</span><span role="columnheader">预估行数</span><span role="columnheader">状态</span></div>
            <div className="asset-discovered-list" role="rowgroup">
            {filteredDiscovered.map(item => {
              const key = tableKey(item)
              const isImported = imported.has(key)
              const checked = isImported || selectedKeySet.has(key)
              return <label className={`${checked ? 'is-selected' : ''}${isImported ? ' is-imported' : ''}`} role="row" key={key}>
                <input type="checkbox" checked={checked} disabled={isImported} onChange={event => setSelectedKeys(current => event.target.checked ? [...current, key] : current.filter(value => value !== key))} />
                <span className="asset-discovered-icon"><Database size={19} weight="duotone" /></span>
                <span><strong>{item.schemaName ? `${item.schemaName}.` : ''}{item.name}</strong><small>{item.sourceComment || '源库未提供表备注'}</small></span>
                <span><strong>{item.columns.length}</strong><small>字段</small></span>
                <span><strong>{formatCount(item.estimatedRowCount)}</strong><small>预估行数</small></span>
                <em>{isImported ? '已导入' : checked ? '本次导入' : '可选择'}</em>
              </label>
            })}
            </div>
          </div>
          <footer><span><CheckCircle size={15} weight="fill" />已导入表已锁定，不会重复提交</span><AppButton variant="primary" disabled={busy === 'import' || selectedKeys.length === 0} onClick={() => void importSelected()}>{busy === 'import' ? '正在提交后台任务…' : `开始导入 ${selectedKeys.length} 张表`}<ArrowRight size={16} /></AppButton></footer>
        </section>
        <aside className="asset-import-policy">
          <header><span><Sparkle size={18} weight="duotone" /></span><div><strong>样本与 AI 策略</strong><small>确定业务数据是否参与元数据生成</small></div></header>
          <fieldset><legend>样本数据授权</legend>{([
            ['MASK', '默认脱敏', '最多读取 10 行，在本地完成格式脱敏后用于生成建议。'],
            ['DENY', '仅技术元数据', '不读取任何业务行，只根据表名、字段名和类型生成。'],
            ['RAW', '原始样本', '最多读取 10 行原值，适用于已确认无敏感信息的源表。'],
          ] as const).map(([value, label, description]) => <label className={sampleMode === value ? 'is-selected' : ''} key={value}><input type="radio" name="sample-mode" value={value} checked={sampleMode === value} onChange={() => setSampleMode(value)} /><span><strong>{label}{value === 'MASK' && <em>推荐</em>}</strong><small>{description}</small></span></label>)}</fieldset>
          <section className="asset-import-summary"><strong>本次导入</strong><dl><div><dt>新增数据表</dt><dd>{selectedKeys.length} 张</dd></div><div><dt>预计字段</dt><dd>{discovered.filter(item => selectedKeys.includes(tableKey(item))).reduce((total, item) => total + item.columns.length, 0)} 个</dd></div><div><dt>样本策略</dt><dd>{sampleMode === 'MASK' ? '脱敏样本' : sampleMode === 'DENY' ? '不读取' : '原始样本'}</dd></div></dl></section>
          <section className="asset-import-guardrail"><CheckCircle size={18} weight="fill" /><span><strong>确定性门禁</strong><small>技术元数据先落库并形成结构哈希；AI 只生成建议，低置信度项必须人工确认。</small></span></section>
        </aside>
      </div> : tableRecord && <div className="asset-detail-layout">
        <aside className="asset-table-form">
          <header><span><Table size={20} weight="duotone" /></span><div><strong>表级业务元数据</strong><small>维护业务语义与访问边界</small></div><em>v{tableRecord.businessVersion}</em></header>
          <div className="asset-table-form-fields">
            <label>业务名称<input disabled={!canManage} value={tableForm.businessName} onChange={event => setTableForm(current => ({ ...current, businessName: event.target.value }))} /></label>
            <label>业务说明<textarea disabled={!canManage} rows={4} value={tableForm.businessDescription} onChange={event => setTableForm(current => ({ ...current, businessDescription: event.target.value }))} placeholder="说明业务范围、粒度和使用边界" /></label>
            <label>数仓层级<select
              aria-label="数仓层级"
              disabled={!canManage}
              value={layerFromTags(tagsFromText(tableForm.tags)) ?? ''}
              onChange={event => setTableForm(current => ({
                ...current,
                tags: replaceLayerTag(tagsFromText(current.tags), event.target.value as WarehouseLayer | '').join(', '),
              }))}
            >
              <option value="">未判定</option>
              {warehouseLayers.map(layer => <option key={layer} value={layer}>{warehouseLayerLabels[layer]}</option>)}
            </select><small title="元数据清洗时判定该表当前所处的数仓层级（写入“层级:”标签），可在此改写；它决定该表默认映射数据集与画布直落的层级。ODS=原始数据（仅业务字段与维度 ID、未补充高频维度信息、未做默认值填充）；DWD=已清洗明细（冗余高频维度属性、空值已按默认值填充）。">标记该表当前所处的数仓层级，并决定数据集与画布的默认落层。</small></label>
            <label>标签<input disabled={!canManage} value={tableForm.tags} onChange={event => setTableForm(current => ({ ...current, tags: event.target.value }))} placeholder="使用英文逗号分隔" /></label>
            <div className="asset-form-grid"><label>敏感级<select disabled={!canManage} value={tableForm.sensitivityLevel} onChange={event => setTableForm(current => ({ ...current, sensitivityLevel: event.target.value as DataSourceTableRecord['sensitivityLevel'] }))}>{Object.entries(sensitivityLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label><label>可见范围<select disabled={!canManage} value={tableForm.visibility} onChange={event => setTableForm(current => ({ ...current, visibility: event.target.value as DataSourceTableRecord['visibility'] }))}><option value="PRIVATE">仅自己</option><option value="TENANT_PUBLIC">领域内共享</option></select></label></div>
          </div>
        </aside>

        <section className="asset-column-editor">
          <header><div className="asset-column-heading"><span><Database size={20} weight="duotone" /></span><div><h2>字段业务元数据</h2><p>业务信息可编辑，技术字段保持只读。</p></div></div><div className="asset-readiness"><span><small>元数据完善度</small><strong>{tableReadiness}%</strong></span><progress max="100" value={tableReadiness} /></div></header>
          <div className="asset-column-toolbar"><label><MagnifyingGlass size={16} /><input aria-label="搜索字段" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索字段名或业务名称" /></label><span>共 {columns.length} 个字段</span></div>
          <div className="asset-column-table" role="table" aria-label="字段业务元数据">
            <div className="asset-column-head" role="row"><span role="columnheader">技术字段</span><span role="columnheader">业务名称</span><span role="columnheader">业务说明</span><span role="columnheader">语义类型</span><span role="columnheader">敏感级</span></div>
            <div className="asset-column-body" role="rowgroup">{columns.filter(column => !keyword.trim() || `${column.columnName} ${column.businessName}`.toLocaleLowerCase().includes(keyword.trim().toLocaleLowerCase())).map(column => <div className={`asset-column-row${column.manualLocked ? ' is-locked' : ''}`} role="row" key={column.id}>
              {/* 字段锁是唯一的刷新保护：锁定字段参与表级识别上下文，但不会被 LLM 写回。 */}
              <span role="cell" className="asset-column-identity">
                <span className="asset-column-name"><strong title={column.columnName}>{column.columnName}</strong><small>{column.nativeType}{column.nullable ? ' · 可空' : ' · 必填'}</small></span>
                {canManage && <AppButton
                  className={`asset-column-lock${column.manualLocked ? ' is-locked' : ''}`}
                  aria-pressed={column.manualLocked}
                  aria-label={`${column.manualLocked ? '解锁' : '锁定'}${column.columnName}的人工定义`}
                  title={column.manualLocked ? '已锁定：刷新时保留该字段的人工定义，不会被 AI 覆盖。点击解锁' : '锁定后刷新会保留该字段的人工定义，其余字段照常更新'}
                  onClick={() => updateColumn(column.id, { manualLocked: !column.manualLocked })}
                ><Lock size={13} weight={column.manualLocked ? 'fill' : 'regular'} /><span>{column.manualLocked ? '已锁' : '锁定'}</span></AppButton>}
              </span>
              <span role="cell"><input disabled={!canManage} aria-label={`${column.columnName}业务名称`} value={column.businessName} onChange={event => updateColumn(column.id, { businessName: event.target.value })} /></span>
              <span role="cell"><input disabled={!canManage} aria-label={`${column.columnName}业务说明`} value={column.businessDescription} onChange={event => updateColumn(column.id, { businessDescription: event.target.value })} /></span>
              <span role="cell"><select disabled={!canManage} aria-label={`${column.columnName}语义类型`} value={column.semanticType} onChange={event => updateColumn(column.id, { semanticType: event.target.value })}>{semanticTypes.map(value => <option key={value || 'none'} value={value}>{value || '未设置'}</option>)}</select></span>
              <span role="cell"><select disabled={!canManage} aria-label={`${column.columnName}敏感级`} value={column.sensitivityLevel} onChange={event => updateColumn(column.id, { sensitivityLevel: event.target.value as DataSourceColumnRecord['sensitivityLevel'] })}>{Object.entries(sensitivityLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></span>
            </div>)}</div>
          </div>
          <footer><span><CheckCircle size={16} />{canManage ? '保存使用业务版本号进行并发冲突保护' : '共享数据源只读展示，不会修改所有者资产'}</span></footer>
        </section>

      </div>}
    </main>

    {preview && <div className="asset-preview-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) setPreview(null) }}><section className="asset-preview-dialog" role="dialog" aria-modal="true" aria-label="脱敏样本预览"><header><div><span className="eyebrow">只读样本</span><h2>{tableRecord?.businessName || tableRecord?.tableName}</h2><p>最多 5 行；敏感字段按当前权限与策略处理。</p></div><AppButton text circle aria-label="关闭样本预览" onClick={() => setPreview(null)}><X size={18} /></AppButton></header><div><table><thead><tr>{preview.columns.map(column => <th key={column}>{column}</th>)}</tr></thead><tbody>{preview.rows.map((row, rowIndex) => <tr key={rowIndex}>{row.map((value, index) => <td key={`${rowIndex}-${index}`}>{String(value ?? '—')}</td>)}</tr>)}</tbody></table></div><footer><span>返回 {preview.rowCount} 行脱敏样本</span><AppButton variant="primary" onClick={() => setPreview(null)}>完成查看</AppButton></footer></section></div>}
    {sidePanel && <div className="asset-preview-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) setSidePanel(null) }}>
      <section className="asset-preview-dialog asset-side-dialog" role="dialog" aria-modal="true" aria-label={sidePanel === 'diffs' ? '结构变更治理' : '元数据处理任务'}>
        <header>
          <div>
            <span className="eyebrow">{sidePanel === 'diffs' ? '结构变更' : '后台任务'}</span>
            <h2>{sidePanel === 'diffs' ? '结构变更治理' : '元数据处理任务'}</h2>
            <p>{sidePanel === 'diffs'
              ? `共 ${diffs.length} 项变更，其中破坏性变更 ${breakingDiffs.length} 项`
              : visibleJob ? `任务 ${visibleJob.id.slice(-8)} · ${jobStatusLabels[visibleJob.status]}` : '当前没有运行中的任务'}</p>
          </div>
          <AppButton text circle aria-label="关闭详情" onClick={() => setSidePanel(null)}><X size={18} /></AppButton>
        </header>
        <div>
          {sidePanel === 'diffs'
            ? <div className="asset-change-list is-dialog">{diffs.map(renderDiff)}</div>
            : !visibleJob ? <div className="asset-job-empty"><CheckCircle size={32} weight="duotone" /><strong>当前没有运行中的任务</strong><p>发现数据表或增量刷新后，可在这里查看完整处理阶段与运行日志。</p></div> : <>
              {renderJobProgress(visibleJob)}
              {renderJobStages(visibleJob)}
              {renderJobFailures(visibleJob)}
              <section className="asset-job-console" aria-label="运行日志">
                <header><span className="asset-console-dots" aria-hidden="true"><i /><i /><i /></span><strong>metadata-job · {visibleJob.id.slice(-8)}</strong><em>{(visibleJob.logs || []).length} 行</em></header>
                <pre>{(visibleJob.logs || []).length === 0
                  ? <code className="is-info"><span className="msg">暂无运行日志</span></code>
                  : (visibleJob.logs || []).map((entry, index) => <code className={`is-${entry.level.toLowerCase()}`} key={`${entry.timestamp}-${index}`}>
                    <span className="ts">{formatTime(entry.timestamp)}</span>
                    <span className="lvl">{entry.level}</span>
                    <span className="stage">{stageLabels[entry.stage] || entry.stage}</span>
                    <span className="msg">{entry.tableName ? `${entry.tableName} · ` : ''}{entry.message}</span>
                  </code>)}</pre>
              </section>
            </>}
        </div>
        <footer><span>{sidePanel === 'diffs' ? '破坏性变更会冻结下游数据集，确认后再重新发布。' : '任务在后台执行，可断线恢复。'}</span><AppButton variant="primary" onClick={() => setSidePanel(null)}>知道了</AppButton></footer>
      </section>
    </div>}
  </AppShell>
}
