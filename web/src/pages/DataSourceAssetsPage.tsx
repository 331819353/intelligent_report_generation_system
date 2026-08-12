import { useEffect, useMemo, useState } from 'react'
import {
  ArrowClockwise,
  ArrowLeft,
  ArrowRight,
  CaretRight,
  Check,
  CheckCircle,
  Circle,
  Database,
  Eye,
  FloppyDisk,
  Funnel,
  MagnifyingGlass,
  Plus,
  Sparkle,
  Table,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import {
  dataSourceAPI,
  type DataSourceColumnRecord,
  type DataSourceRecord,
  type DataSourceTablePreview,
  type DataSourceTableRecord,
  type DiscoveredTableRecord,
  type MetadataAISuggestion,
  type MetadataDiff,
  type MetadataJob,
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
): DataSourceTableRecord => ({
  id, dataSourceId: snapshotSource.id, catalogName: 'sales_prod', schemaName: 'sales', tableName, tableType: 'BASE TABLE',
  sourceComment: '', businessName, businessDescription, tags, sensitivityLevel, visibility: 'TENANT_PUBLIC', manualLocked: false,
  assetStatus: 'ACTIVE', businessVersion: 3, structureHash: 'a'.repeat(64), managementStatus: 'ENABLED', enrichmentStatus,
  columnCount, metadataVersion: 5, lastSyncAt: '2026-08-11T10:18:00+08:00',
})

const snapshotTables: DataSourceTableRecord[] = [
  makeSnapshotTable('snapshot-table-orders', 'fact_sales_order', '销售订单事实表', 24, 'SUCCEEDED', 'CONFIDENTIAL', '按订单行记录销售、优惠、渠道和履约结果。', ['主题:经营分析', '作用:事实表', '粒度:订单']),
  makeSnapshotTable('snapshot-table-customer', 'dim_customer', '客户主数据', 18, 'SUCCEEDED', 'CONFIDENTIAL', '客户身份、分群与生命周期状态。', ['作用:主数据', '粒度:客户']),
  makeSnapshotTable('snapshot-table-channel', 'dim_channel', '销售渠道维表', 12, 'SUCCEEDED', 'INTERNAL', '渠道层级、组织归属与经营类型。', ['作用:维度表', '范围:运营分析']),
  makeSnapshotTable('snapshot-table-product', 'dim_product', '商品主数据', 31, 'SUCCEEDED', 'INTERNAL', '商品、品类、品牌和上市状态信息。', ['作用:主数据', '粒度:产品']),
  makeSnapshotTable('snapshot-table-payment', 'fact_payment', '支付流水', 22, 'RUNNING', 'RESTRICTED', '支付尝试、渠道回执与退款状态。', ['功能:业务流水']),
  makeSnapshotTable('snapshot-table-store', 'dim_store', '门店维表', 16, 'PENDING', 'INTERNAL', '', ['作用:维度表']),
  makeSnapshotTable('snapshot-table-campaign', 'fact_campaign_touch', '营销触达明细', 27, 'FAILED', 'CONFIDENTIAL', '', ['范围:客户分析']),
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

const snapshotSuggestions: MetadataAISuggestion[] = [
  {
    id: 'snapshot-suggestion-1', jobId: 'snapshot-job-complete', targetType: 'COLUMN', targetId: 'snapshot-column-3', confidence: .82,
    status: 'PENDING', pendingReason: 'LOW_CONFIDENCE', createdAt: '2026-08-11T10:20:00+08:00',
    value: { targetId: 'snapshot-column-3', businessName: '销售渠道编码', businessDescription: '标识订单来源的销售渠道稳定编码', tags: ['关联:业务键'], sensitivityLevel: 'INTERNAL', semanticType: 'CATEGORY', confidence: .82 },
  },
  {
    id: 'snapshot-suggestion-2', jobId: 'snapshot-job-complete', targetType: 'COLUMN', targetId: 'snapshot-column-8', confidence: .78,
    status: 'PENDING', pendingReason: 'LOW_CONFIDENCE', createdAt: '2026-08-11T10:20:00+08:00',
    value: { targetId: 'snapshot-column-8', businessName: '经营区域', businessDescription: '订单履约门店对应的经营大区名称', tags: ['范围:运营分析'], sensitivityLevel: 'INTERNAL', semanticType: 'REGION', confidence: .78 },
  },
]

const snapshotJob: MetadataJob = {
  id: 'snapshot-job-import', dataSourceId: snapshotSource.id, kind: 'IMPORT', mode: 'INCREMENTAL', sampleDataMode: 'MASK', samplePolicyVersion: 1,
  status: 'RUNNING', stage: 'LLM', total: 5, completed: 3, succeeded: 3, skipped: 0, failed: 0, currentTable: 'fact_after_sales',
  createdAt: '2026-08-11T10:16:00+08:00', startedAt: '2026-08-11T10:16:04+08:00',
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

function AssetFlow({ current, hasTables }: { current: AssetMode; hasTables: boolean }) {
  const steps = [
    { label: '连接已发布', detail: '配置与测试收据', complete: true },
    { label: '发现并导入', detail: '选择源表与样本策略', complete: hasTables && current !== 'discover' },
    { label: '业务元数据', detail: '完善表与字段定义', complete: current === 'detail' },
    { label: '数据集建模', detail: '使用可用资产', complete: false },
  ]
  const currentIndex = current === 'discover' ? 1 : current === 'detail' ? 2 : hasTables ? 2 : 1
  return <section className="asset-flow" aria-label="数据源资产化进度">
    {steps.map((step, index) => <article className={`${step.complete ? 'is-complete' : ''}${index === currentIndex ? ' is-current' : ''}`} key={step.label}>
      <span>{step.complete ? <Check size={14} weight="bold" /> : index + 1}</span>
      <div><strong>{step.label}</strong><small>{step.detail}</small></div>
      {index < steps.length - 1 && <CaretRight size={16} aria-hidden="true" />}
    </article>)}
  </section>
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
  const mode: AssetMode = location.pathname.endsWith('/discover') ? 'discover' : tableId ? 'detail' : 'catalog'
  const suffix = querySuffix(designSnapshot, qaViewport1920)
  const snapshotTableForRoute = snapshotTables.find(item => item.id === tableId) || snapshotTables[0]
  const snapshotColumnsForRoute = snapshotTableForRoute.id === snapshotTables[0].id ? snapshotColumns : snapshotColumns.slice(0, 6).map((column, index) => ({ ...column, id: `${snapshotTableForRoute.id}-column-${index}`, tableId: snapshotTableForRoute.id }))
  const [source, setSource] = useState<DataSourceRecord | null>(designSnapshot ? snapshotSource : null)
  const [canManage, setCanManage] = useState(designSnapshot)
  const [tables, setTables] = useState<DataSourceTableRecord[]>(designSnapshot ? snapshotTables : [])
  const [tableRecord, setTableRecord] = useState<DataSourceTableRecord | null>(designSnapshot ? snapshotTableForRoute : null)
  const [columns, setColumns] = useState<DataSourceColumnRecord[]>(designSnapshot ? snapshotColumnsForRoute : [])
  const [originalColumns, setOriginalColumns] = useState<DataSourceColumnRecord[]>(designSnapshot ? snapshotColumnsForRoute : [])
  const [discovered, setDiscovered] = useState<DiscoveredTableRecord[]>(designSnapshot ? snapshotDiscovered : [])
  const [suggestions, setSuggestions] = useState<MetadataAISuggestion[]>(designSnapshot && snapshotTableForRoute.id === snapshotTables[0].id ? snapshotSuggestions : [])
  const [job, setJob] = useState<MetadataJob | null>(designSnapshot && params.get('job') === 'importing' ? snapshotJob : null)
  const [diffs, setDiffs] = useState<MetadataDiff[]>(designSnapshot ? snapshotDiffs : [])
  const [loading, setLoading] = useState(!designSnapshot)
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [keyword, setKeyword] = useState('')
  const [statusFilter, setStatusFilter] = useState('ALL')
  const [selectedKeys, setSelectedKeys] = useState<string[]>([])
  const [sampleMode, setSampleMode] = useState<MetadataSampleMode>('MASK')
  const [tableForm, setTableForm] = useState({ businessName: snapshotTableForRoute.businessName, businessDescription: snapshotTableForRoute.businessDescription, tags: snapshotTableForRoute.tags.join(', '), sensitivityLevel: snapshotTableForRoute.sensitivityLevel, visibility: snapshotTableForRoute.visibility, manualLocked: snapshotTableForRoute.manualLocked })
  const [preview, setPreview] = useState<DataSourceTablePreview | null>(null)
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
          // Table and column metadata are the primary editing path. AI review is
          // optional assistance and must never blank the editor if temporarily
          // unavailable or disabled for the tenant.
          const [loadedTable, loadedColumns] = await Promise.all([
            dataSourceAPI.table(tableId), dataSourceAPI.columns(tableId),
          ])
          const pendingSuggestions = manageAllowed
            ? await dataSourceAPI.metadataSuggestions('PENDING').catch(() => ({ items: [], total: 0 }))
            : { items: [], total: 0 }
          if (!active) return
          const ids = new Set([loadedTable.id, ...loadedColumns.items.map(column => column.id)])
          setTableRecord(loadedTable)
          setColumns(loadedColumns.items)
          setOriginalColumns(loadedColumns.items)
          setSuggestions(pendingSuggestions.items.filter(item => ids.has(item.targetId)))
          setTableForm({ businessName: loadedTable.businessName, businessDescription: loadedTable.businessDescription, tags: loadedTable.tags.join(', '), sensitivityLevel: loadedTable.sensitivityLevel, visibility: loadedTable.visibility, manualLocked: loadedTable.manualLocked })
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

  useEffect(() => {
    if (designSnapshot || !job || !isJobActive(job)) return undefined
    const timeout = window.setTimeout(() => {
      void dataSourceAPI.getMetadataJob(sourceId, job.id).then(next => {
        setJob(next)
        if (!isJobActive(next)) void dataSourceAPI.tables(sourceId).then(result => setTables(result.items))
      }).catch(() => undefined)
    }, 1200)
    return () => window.clearTimeout(timeout)
  }, [designSnapshot, job, sourceId])

  const imported = useMemo(() => new Set(tables.map(importedKey)), [tables])
  const filteredTables = useMemo(() => tables.filter(table => {
    const query = keyword.trim().toLocaleLowerCase()
    const matchesSearch = !query || table.tableName.toLocaleLowerCase().includes(query) || table.businessName.toLocaleLowerCase().includes(query) || table.businessDescription.toLocaleLowerCase().includes(query)
    return matchesSearch && (statusFilter === 'ALL' || table.enrichmentStatus === statusFilter)
  }), [keyword, statusFilter, tables])
  const filteredDiscovered = useMemo(() => discovered.filter(table => {
    const query = keyword.trim().toLocaleLowerCase()
    return !query || table.name.toLocaleLowerCase().includes(query) || table.sourceComment.toLocaleLowerCase().includes(query)
  }), [discovered, keyword])
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

  const refreshAssets = async () => {
    setBusy('refresh')
    try {
      if (designSnapshot) {
        setJob({ ...snapshotJob, kind: 'REFRESH', mode: 'INCREMENTAL' })
        setNotice({ tone: 'success', message: '已提交增量刷新任务' })
        return
      }
      const created = await dataSourceAPI.refreshTables(sourceId, 'INCREMENTAL', undefined, 'MASK')
      setJob(created)
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
        sensitivityLevel: tableForm.sensitivityLevel, visibility: tableForm.visibility, manualLocked: tableForm.manualLocked, expectedVersion: tableRecord.businessVersion,
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

  const decideSuggestion = async (suggestion: MetadataAISuggestion, decision: 'ACCEPT' | 'REJECT') => {
    setBusy(`suggestion:${suggestion.id}`)
    try {
      if (!designSnapshot) await dataSourceAPI.decideMetadataSuggestion(suggestion.id, decision)
      if (decision === 'ACCEPT') {
        if (suggestion.targetType === 'TABLE') setTableForm(current => ({ ...current, businessName: suggestion.value.businessName, businessDescription: suggestion.value.businessDescription, tags: suggestion.value.tags.join(', '), sensitivityLevel: suggestion.value.sensitivityLevel }))
        else updateColumn(suggestion.targetId, { businessName: suggestion.value.businessName, businessDescription: suggestion.value.businessDescription, tags: suggestion.value.tags, sensitivityLevel: suggestion.value.sensitivityLevel, semanticType: suggestion.value.semanticType || '' })
      }
      setSuggestions(current => current.filter(item => item.id !== suggestion.id))
      setNotice({ tone: 'success', message: decision === 'ACCEPT' ? '已接受 AI 建议，请保存后生效' : '已拒绝该 AI 建议' })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '处理 AI 建议失败' })
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
  const title = mode === 'discover' ? '发现并导入数据表' : mode === 'detail' ? (tableRecord?.businessName || tableRecord?.tableName || '完善数据资产') : `${source?.name || '数据源'} · 数据表资产`
  const titleMeta = mode === 'discover' ? '选择源表、样本策略并启动后台元数据处理' : mode === 'detail' ? `${tableRecord?.schemaName || ''}.${tableRecord?.tableName || ''} · 完善表与字段的业务定义` : '将已发布连接转换为可检索、可治理、可建模的数据资产'

  return <AppShell
    className={shellClass}
    title={title}
    eyebrow="数据源资产化"
    titleMeta={<span className="asset-page-title-meta">{titleMeta}</span>}
    actions={<>
      <AppButton className="asset-back-button" onClick={mode === 'catalog' ? () => navigate(`/data-sources${suffix}`) : goCatalog}><ArrowLeft size={16} />{mode === 'catalog' ? '返回数据源' : '返回资产目录'}</AppButton>
      {mode === 'catalog' && canManage && <><AppButton onClick={() => void refreshAssets()} disabled={busy === 'refresh' || isJobActive(visibleJob)}><ArrowClockwise size={16} />{busy === 'refresh' ? '提交中…' : '增量刷新'}</AppButton><AppButton variant="primary" onClick={goDiscover}><Plus size={16} weight="bold" />发现数据表</AppButton></>}
      {mode === 'discover' && canManage && <AppButton variant="primary" disabled={busy === 'import' || selectedKeys.length === 0} onClick={() => void importSelected()}><ArrowRight size={16} />{busy === 'import' ? '正在提交…' : `导入 ${selectedKeys.length} 张表`}</AppButton>}
      {mode === 'detail' && <><AppButton onClick={() => void openPreview()} disabled={busy === 'preview'}><Eye size={16} />预览样本</AppButton>{canManage && <><AppButton onClick={() => void saveMetadata()} disabled={busy === 'save'}><FloppyDisk size={16} />{busy === 'save' ? '保存中…' : '保存'}</AppButton><AppButton variant="primary" onClick={() => void completeMetadata()} disabled={busy === 'complete' || tableReadiness < 100}><CheckCircle size={16} />完成资产化</AppButton></>}</>}
    </>}
  >
    <PageNotice notice={notice} onClose={() => setNotice(null)} />
    <main className="asset-page-content">
      <AssetFlow current={mode} hasTables={tables.length > 0} />

      {loading ? <section className="asset-page-state">正在加载数据源资产…</section> : !source ? <section className="asset-page-state is-error">数据源不存在或无权访问</section> : mode === 'catalog' ? <>
        <section className="asset-summary-strip" aria-label="资产概览">
          <article><span className="is-blue"><Table size={19} weight="duotone" /></span><small>数据表</small><strong>{tables.length}</strong></article>
          <article><span className="is-green"><CheckCircle size={19} weight="duotone" /></span><small>已完善</small><strong>{tables.filter(item => item.enrichmentStatus === 'SUCCEEDED').length}</strong></article>
          <article><span className="is-orange"><Sparkle size={19} weight="duotone" /></span><small>处理中</small><strong>{tables.filter(item => item.enrichmentStatus === 'RUNNING' || item.enrichmentStatus === 'PENDING').length}</strong></article>
          <article><span className="is-red"><WarningCircle size={19} weight="duotone" /></span><small>需要处理</small><strong>{tables.filter(item => item.enrichmentStatus === 'FAILED').length}</strong></article>
        </section>
        <div className="asset-catalog-layout">
          <section className="asset-table-catalog" aria-label="数据表资产目录">
            <header><div><h2>数据表资产</h2><p>选择数据表继续完善字段、AI 建议与敏感级。</p></div><span>共 {filteredTables.length} 张</span></header>
            <div className="asset-table-filters">
              <label><MagnifyingGlass size={17} /><input aria-label="搜索数据表资产" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索业务名、物理表名或说明" /></label>
              <select aria-label="按完善状态筛选" value={statusFilter} onChange={event => setStatusFilter(event.target.value)}><option value="ALL">全部状态</option><option value="SUCCEEDED">已完善</option><option value="RUNNING">处理中</option><option value="PENDING">待完善</option><option value="FAILED">需处理</option></select>
              <AppButton aria-label="重置资产筛选" title="重置筛选" onClick={() => { setKeyword(''); setStatusFilter('ALL') }}><Funnel size={16} />重置</AppButton>
            </div>
            <div className="asset-table-grid" role="table" aria-label="数据表资产">
              <div className="asset-table-grid-head" role="row"><span role="columnheader">资产名称</span><span role="columnheader">物理表</span><span role="columnheader">字段</span><span role="columnheader">完善状态</span><span role="columnheader">敏感级</span><span role="columnheader">操作</span></div>
              <div className="asset-table-grid-body" role="rowgroup">
                {filteredTables.map(item => <article className="asset-table-row" role="row" key={item.id}>
                  <AppButton className="asset-table-row-open" aria-label={`${canManage ? '完善' : '查看'}${item.businessName || item.tableName}`} onClick={() => goDetail(item.id)}>
                    <span className="asset-table-name" role="cell"><span><Table size={20} weight="duotone" /></span><span><strong>{item.businessName || '待补充业务名称'}</strong><small>{item.businessDescription || '尚未填写业务说明'}</small></span></span>
                    <span role="cell"><strong>{item.schemaName}.{item.tableName}</strong><small>{item.tableType}</small></span>
                    <span role="cell"><strong>{item.columnCount}</strong><small>个字段</small></span>
                    <span role="cell"><em className={`asset-status is-${item.enrichmentStatus.toLowerCase()}`}>{enrichmentLabels[item.enrichmentStatus]}</em><small>{formatTime(item.lastSyncAt)}</small></span>
                    <span role="cell"><em className={`asset-sensitivity is-${item.sensitivityLevel.toLowerCase()}`}>{sensitivityLabels[item.sensitivityLevel]}</em><small>{item.visibility === 'TENANT_PUBLIC' ? '领域内共享' : '仅自己'}</small></span>
                  </AppButton>
                  <AppButton link className="asset-row-action" aria-label={`进入${item.businessName || item.tableName}${canManage ? '完善' : '查看'}页`} onClick={() => goDetail(item.id)}>{canManage ? '去完善' : '查看'}<ArrowRight size={14} /></AppButton>
                </article>)}
                {filteredTables.length === 0 && <div className="asset-page-state"><strong>没有符合条件的数据表</strong><span>调整筛选条件或从源库发现新表。</span></div>}
              </div>
            </div>
          </section>

          <div className="asset-catalog-side">
          {diffs.length > 0 && <section className={`asset-change-governance${breakingDiffs.length > 0 ? ' has-breaking' : ''}`} aria-label="结构变更治理">
            <header>
              <span><WarningCircle size={18} weight="duotone" /></span>
              <div><strong>结构变更治理</strong><small>{breakingDiffs.length > 0 ? `${breakingDiffs.length} 项破坏性变更已触发下游保护` : '最近变更均可向后兼容'}</small></div>
              <em>{diffs.length} 项</em>
            </header>
            <div className="asset-change-list">
              {diffs.slice(0, 3).map(diff => <article className={diff.breaking ? 'is-breaking' : 'is-safe'} key={diff.id}>
                <span>{diff.breaking ? <WarningCircle size={17} weight="fill" /> : <CheckCircle size={17} weight="fill" />}</span>
                <div><strong>{diff.objectKey}</strong><small>{diff.changeType === 'REMOVED' ? '已移除' : diff.changeType === 'ADDED' ? '新增字段' : diff.changeType === 'REACTIVATED' ? '重新启用' : '结构已变化'} · {formatTime(diff.createdAt)}</small></div>
                <em>{diff.breaking ? `${diff.staleCount || diff.impactCount} 个下游已冻结` : '兼容'}</em>
                {diff.breaking && diff.impact && diff.impact.length > 0 && <div className="asset-change-impact">
                  {diff.impact.slice(0, 2).map(item => <AppButton link key={item.id} onClick={() => navigate(`/datasets/${item.downstreamId}/edit`)}>{item.downstreamName}<ArrowRight size={13} /></AppButton>)}
                  {diff.impact.length > 2 && <small>另有 {diff.impact.length - 2} 个受影响下游</small>}
                </div>}
              </article>)}
            </div>
          </section>}

          <aside className="asset-job-panel" aria-label="元数据处理任务">
            <header><div><span className="is-blue"><Sparkle size={18} weight="duotone" /></span><div><strong>元数据处理任务</strong><small>{visibleJob ? `任务 ${visibleJob.id.slice(-8)}` : '后台任务可断线恢复'}</small></div></div>{visibleJob && <em className={`asset-job-status is-${visibleJob.status.toLowerCase()}`}>{visibleJob.status === 'RUNNING' ? '处理中' : visibleJob.status === 'QUEUED' ? '排队中' : visibleJob.status === 'SUCCEEDED' ? '已完成' : visibleJob.status === 'PARTIAL' ? '部分完成' : '失败'}</em>}</header>
            {!visibleJob ? <div className="asset-job-empty"><CheckCircle size={34} weight="duotone" /><strong>当前没有运行中的任务</strong><p>{canManage ? '发现数据表或增量刷新后，可在这里持续查看处理阶段。' : '这是其他成员共享的数据源，当前以只读方式展示资产状态。'}</p>{canManage && <AppButton onClick={goDiscover}>发现新数据表</AppButton>}</div> : <>
              <section className="asset-job-progress"><div><strong>{visibleJob.kind === 'IMPORT' ? '导入并完善元数据' : '增量刷新元数据'}</strong><span>{visibleJob.total ? Math.round(visibleJob.completed / visibleJob.total * 100) : 0}%</span></div><progress max={Math.max(1, visibleJob.total)} value={visibleJob.completed} /><p>已处理 {visibleJob.completed} / {visibleJob.total} 张 · 成功 {visibleJob.succeeded} · 失败 {visibleJob.failed}</p>{visibleJob.currentTable && <small>当前：{visibleJob.currentTable}</small>}</section>
              <ol className="asset-job-stages">{['DISCOVERY', 'PERSISTENCE', 'LLM', 'COMPLETE'].map((stage, index) => {
                const activeIndex = ['QUEUED', 'DISCOVERY', 'DIFF', 'SAMPLE', 'PERSISTENCE', 'LLM', 'COMPLETE'].indexOf(visibleJob.stage)
                const stageIndex = [1, 4, 5, 6][index]
                const complete = activeIndex > stageIndex || visibleJob.status === 'SUCCEEDED'
                const current = activeIndex === stageIndex
                return <li className={`${complete ? 'is-complete' : ''}${current ? ' is-current' : ''}`} key={stage}>{complete ? <CheckCircle size={18} weight="fill" /> : current ? <Circle size={18} weight="fill" /> : <Circle size={18} />}<span><strong>{stageLabels[stage]}</strong><small>{stage === 'DISCOVERY' ? '读取源库表与字段' : stage === 'PERSISTENCE' ? '保存稳定技术标识' : stage === 'LLM' ? '生成业务定义建议' : '资产可进入数据集建模'}</small></span></li>
              })}</ol>
              <section className="asset-job-log"><strong>安全运行摘要</strong>{(visibleJob.logs || []).slice(-4).map((entry, index) => <p key={`${entry.timestamp}-${index}`}><time>{formatTime(entry.timestamp)}</time><span>{entry.tableName ? `${entry.tableName} · ` : ''}{entry.message}</span></p>)}</section>
            </>}
            <footer><Sparkle size={16} weight="fill" /><span><strong>下一步：完善业务元数据</strong><small>处理完成后逐表确认名称、口径与敏感级。</small></span></footer>
          </aside>
          </div>
        </div>
      </> : mode === 'discover' ? <div className="asset-discover-layout">
        <section className="asset-discover-catalog">
          <header><div><h2>选择需要资产化的数据表</h2><p>已导入的表保持勾选锁定；本次只提交新增选择。</p></div><span>已选择 {selectedKeys.length} 张</span></header>
          <div className="asset-discover-search"><MagnifyingGlass size={17} /><input aria-label="搜索发现的数据表" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索物理表名或源库备注" /></div>
          <div className="asset-discovered-list" role="list">
            {filteredDiscovered.map(item => {
              const key = tableKey(item)
              const isImported = imported.has(key)
              const checked = isImported || selectedKeys.includes(key)
              return <label className={`${checked ? 'is-selected' : ''}${isImported ? ' is-imported' : ''}`} key={key}>
                <input type="checkbox" checked={checked} disabled={isImported} onChange={event => setSelectedKeys(current => event.target.checked ? [...current, key] : current.filter(value => value !== key))} />
                <span className="asset-discovered-icon"><Database size={19} weight="duotone" /></span>
                <span><strong>{item.schemaName ? `${item.schemaName}.` : ''}{item.name}</strong><small>{item.sourceComment || '源库未提供表备注'}</small></span>
                <span><strong>{item.columns.length}</strong><small>字段</small></span>
                <span><strong>{formatCount(item.estimatedRowCount)}</strong><small>预估行数</small></span>
                <em>{isImported ? '已导入' : checked ? '本次导入' : '可选择'}</em>
              </label>
            })}
          </div>
        </section>
        <aside className="asset-import-policy">
          <header><span><Sparkle size={18} weight="duotone" /></span><div><strong>样本与 AI 策略</strong><small>确定业务数据是否参与元数据生成</small></div></header>
          <fieldset><legend>样本数据授权</legend>{([
            ['MASK', '默认脱敏', '最多读取 10 行，在本地完成格式脱敏后用于生成建议。'],
            ['DENY', '仅技术元数据', '不读取任何业务行，只根据表名、字段名和类型生成。'],
            ['RAW', '原始样本', '最多读取 10 行原值，适用于已确认无敏感信息的源表。'],
          ] as const).map(([value, label, description]) => <label className={sampleMode === value ? 'is-selected' : ''} key={value}><input type="radio" name="sample-mode" value={value} checked={sampleMode === value} onChange={() => setSampleMode(value)} /><span><strong>{label}{value === 'MASK' && <em>推荐</em>}</strong><small>{description}</small></span></label>)}</fieldset>
          <section className="asset-import-summary"><strong>本次导入</strong><dl><div><dt>新增数据表</dt><dd>{selectedKeys.length} 张</dd></div><div><dt>预计字段</dt><dd>{discovered.filter(item => selectedKeys.includes(tableKey(item))).reduce((total, item) => total + item.columns.length, 0)} 个</dd></div><div><dt>样本策略</dt><dd>{sampleMode === 'MASK' ? '脱敏样本' : sampleMode === 'DENY' ? '不读取' : '原始样本'}</dd></div></dl></section>
          <section className="asset-import-guardrail"><WarningCircle size={18} /><span><strong>确定性门禁</strong><small>技术元数据先落库并形成结构哈希；AI 只生成建议，低置信度项必须人工确认。</small></span></section>
          <AppButton variant="primary" disabled={busy === 'import' || selectedKeys.length === 0} onClick={() => void importSelected()}>{busy === 'import' ? '正在提交后台任务…' : `开始导入 ${selectedKeys.length} 张表`}<ArrowRight size={16} /></AppButton>
        </aside>
      </div> : tableRecord && <div className="asset-detail-layout">
        <aside className="asset-table-form">
          <header><span><Table size={20} weight="duotone" /></span><div><strong>表级业务元数据</strong><small>业务版本 v{tableRecord.businessVersion}</small></div></header>
          <label>业务名称<input disabled={!canManage} value={tableForm.businessName} onChange={event => setTableForm(current => ({ ...current, businessName: event.target.value }))} /></label>
          <label>业务说明<textarea disabled={!canManage} rows={4} value={tableForm.businessDescription} onChange={event => setTableForm(current => ({ ...current, businessDescription: event.target.value }))} placeholder="说明业务范围、粒度和使用边界" /></label>
          <label>标签<input disabled={!canManage} value={tableForm.tags} onChange={event => setTableForm(current => ({ ...current, tags: event.target.value }))} placeholder="使用英文逗号分隔" /></label>
          <div className="asset-form-grid"><label>敏感级<select disabled={!canManage} value={tableForm.sensitivityLevel} onChange={event => setTableForm(current => ({ ...current, sensitivityLevel: event.target.value as DataSourceTableRecord['sensitivityLevel'] }))}>{Object.entries(sensitivityLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label><label>可见范围<select disabled={!canManage} value={tableForm.visibility} onChange={event => setTableForm(current => ({ ...current, visibility: event.target.value as DataSourceTableRecord['visibility'] }))}><option value="PRIVATE">仅自己</option><option value="TENANT_PUBLIC">领域内共享</option></select></label></div>
          <label className="asset-lock-control"><input type="checkbox" disabled={!canManage} checked={tableForm.manualLocked} onChange={event => setTableForm(current => ({ ...current, manualLocked: event.target.checked }))} /><span><strong>锁定人工定义</strong><small>{canManage ? '后续 AI 刷新不覆盖当前表级业务元数据' : '当前为共享资产只读视图'}</small></span></label>
          <section className="asset-technical-facts"><strong>技术身份</strong><dl><div><dt>物理表</dt><dd>{tableRecord.schemaName}.{tableRecord.tableName}</dd></div><div><dt>类型</dt><dd>{tableRecord.tableType}</dd></div><div><dt>字段</dt><dd>{tableRecord.columnCount}</dd></div><div><dt>结构版本</dt><dd>v{tableRecord.metadataVersion}</dd></div></dl></section>
        </aside>

        <section className="asset-column-editor">
          <header><div><h2>字段业务元数据</h2><p>技术字段保持只读；业务名称、定义、语义类型和敏感级可维护。</p></div><div className="asset-readiness"><span><strong>{tableReadiness}%</strong><small>完善度</small></span><progress max="100" value={tableReadiness} /></div></header>
          <div className="asset-column-toolbar"><label><MagnifyingGlass size={16} /><input aria-label="搜索字段" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索字段名或业务名称" /></label><span>共 {columns.length} 个字段</span></div>
          <div className="asset-column-table" role="table" aria-label="字段业务元数据">
            <div className="asset-column-head" role="row"><span role="columnheader">技术字段</span><span role="columnheader">业务名称</span><span role="columnheader">业务说明</span><span role="columnheader">语义类型</span><span role="columnheader">敏感级</span></div>
            <div className="asset-column-body" role="rowgroup">{columns.filter(column => !keyword.trim() || `${column.columnName} ${column.businessName}`.toLocaleLowerCase().includes(keyword.trim().toLocaleLowerCase())).map(column => <div className="asset-column-row" role="row" key={column.id}>
              <span role="cell"><strong>{column.columnName}</strong><small>{column.nativeType}{column.nullable ? ' · 可空' : ' · 必填'}</small></span>
              <span role="cell"><input disabled={!canManage} aria-label={`${column.columnName}业务名称`} value={column.businessName} onChange={event => updateColumn(column.id, { businessName: event.target.value })} /></span>
              <span role="cell"><input disabled={!canManage} aria-label={`${column.columnName}业务说明`} value={column.businessDescription} onChange={event => updateColumn(column.id, { businessDescription: event.target.value })} /></span>
              <span role="cell"><select disabled={!canManage} aria-label={`${column.columnName}语义类型`} value={column.semanticType} onChange={event => updateColumn(column.id, { semanticType: event.target.value })}>{semanticTypes.map(value => <option key={value || 'none'} value={value}>{value || '未设置'}</option>)}</select></span>
              <span role="cell"><select disabled={!canManage} aria-label={`${column.columnName}敏感级`} value={column.sensitivityLevel} onChange={event => updateColumn(column.id, { sensitivityLevel: event.target.value as DataSourceColumnRecord['sensitivityLevel'] })}>{Object.entries(sensitivityLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></span>
            </div>)}</div>
          </div>
          <footer><span><CheckCircle size={16} />{canManage ? '保存使用业务版本号进行并发冲突保护' : '共享数据源只读展示，不会修改所有者资产'}</span>{canManage && <AppButton variant="primary" disabled={busy === 'save'} onClick={() => void saveMetadata()}><FloppyDisk size={16} />保存全部变更</AppButton>}</footer>
        </section>

        {canManage && <aside className="asset-ai-review">
          <header><span><Sparkle size={19} weight="duotone" /></span><div><strong>AI 建议复核</strong><small>{suggestions.length} 项需要人工确认</small></div></header>
          <div className="asset-ai-suggestion-list">{suggestions.map(suggestion => {
            const target = suggestion.targetType === 'TABLE' ? tableRecord.tableName : columns.find(column => column.id === suggestion.targetId)?.columnName || '字段'
            return <article key={suggestion.id}><header><span>{suggestion.targetType === 'TABLE' ? '表' : '字段'} · {target}</span><em>{Math.round(suggestion.confidence * 100)}%</em></header><strong>{suggestion.value.businessName}</strong><p>{suggestion.value.businessDescription}</p><div>{suggestion.value.tags.map(tag => <span key={tag}>{tag}</span>)}</div><footer><AppButton disabled={busy === `suggestion:${suggestion.id}`} onClick={() => void decideSuggestion(suggestion, 'REJECT')}>拒绝</AppButton><AppButton variant="primary" disabled={busy === `suggestion:${suggestion.id}`} onClick={() => void decideSuggestion(suggestion, 'ACCEPT')}>接受建议</AppButton></footer></article>
          })}{suggestions.length === 0 && <div className="asset-ai-empty"><CheckCircle size={30} weight="duotone" /><strong>暂无待确认建议</strong><p>高置信度建议已自动应用，人工定义始终拥有更高优先级。</p></div>}</div>
          <section className="asset-detail-next"><Sparkle size={17} weight="fill" /><span><strong>下一步：进入数据集建模</strong><small>表和字段完善度达到 100% 后，可作为稳定输入创建数据集。</small></span><AppButton variant="primary" disabled={tableReadiness < 100} onClick={() => navigate(`/datasets${designSnapshot ? '?snapshot=assets' : `?sourceTableId=${encodeURIComponent(tableRecord.id)}`}`)}>去建模<ArrowRight size={14} /></AppButton></section>
        </aside>}
      </div>}
    </main>

    {preview && <div className="asset-preview-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) setPreview(null) }}><section className="asset-preview-dialog" role="dialog" aria-modal="true" aria-label="脱敏样本预览"><header><div><span className="eyebrow">只读样本</span><h2>{tableRecord?.businessName || tableRecord?.tableName}</h2><p>最多 5 行；敏感字段按当前权限与策略处理。</p></div><AppButton text circle aria-label="关闭样本预览" onClick={() => setPreview(null)}><X size={18} /></AppButton></header><div><table><thead><tr>{preview.columns.map(column => <th key={column}>{column}</th>)}</tr></thead><tbody>{preview.rows.map((row, rowIndex) => <tr key={rowIndex}>{row.map((value, index) => <td key={`${rowIndex}-${index}`}>{String(value ?? '—')}</td>)}</tr>)}</tbody></table></div><footer><span>返回 {preview.rowCount} 行脱敏样本</span><AppButton variant="primary" onClick={() => setPreview(null)}>完成查看</AppButton></footer></section></div>}
  </AppShell>
}
