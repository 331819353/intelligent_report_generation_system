import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties, type DragEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { ApproximateEqualsIcon, ArrowClockwiseIcon, ArrowCounterClockwiseIcon, ArrowDownIcon, ArrowUpIcon, ArrowsInSimpleIcon, ArrowsLeftRightIcon, ArrowsOutSimpleIcon, CalendarDotsIcon, CaretDownIcon, CaretUpIcon, CheckCircleIcon, DotsSixVerticalIcon, DropSlashIcon, FunnelIcon, GitMergeIcon, LinkSimpleIcon, ListChecksIcon, MagicWandIcon, MagnifyingGlassIcon, MathOperationsIcon, PlusIcon, PlusMinusIcon, RowsIcon, ScissorsIcon, SwapIcon, TextAaIcon, TextTSlashIcon, TreeStructureIcon, WarningCircleIcon, XIcon, type Icon } from '@phosphor-icons/react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import '../styles/dataset-designer.css'
import '../styles/dataset-center.css'
import { AssetSharingSelect } from '../components/AssetSharingSelect'
import { DatasetAIDock } from '../components/dataset/DatasetAIDock'
import { DatasetComponentToolbar } from '../components/dataset/DatasetComponentToolbar'
import { DatasetDesignWorkspace } from '../components/dataset/DatasetDesignWorkspace'
import { RequestError } from '../lib/api'
import { currentDomain, currentDomainID, subscribeDomainChange } from '../lib/domain-context'
import {
  datasetAIPlanFromEditor,
  datasetAIRequestContext,
  materializeDatasetAIPlan,
  requestDatasetAIProposal,
  type DatasetAIPlanResult,
  type DatasetAIProgressEvent,
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
  normalizeGraphTransformComponentType,
  serializeDesignerGraph,
  validateDesignerGraph,
  type CanvasPoint,
  type DesignerGraphV1,
  type GraphEnd,
  type GraphGroup,
  type GraphGroupByMode,
  type GraphInput,
  type GraphJoin,
  type GraphConditionOperator,
  type GraphFilterCondition,
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
  datasetLayerChoices,
  datasetAPI,
  joinCardinalityForType,
  type AssetColumn,
  type AssetTable,
  type AssetTablePreview,
  type DatasetDraft,
  type DatasetDAGRun,
  type DatasetDAGRunDetail,
  type DatasetLayer,
  type DatasetLLMTrigger,
  type DatasetLifecycleImpact,
  type DatasetPreview,
  type DatasetPreviewColumn,
  type DatasetPublicationRequest,
  type DatasetRecord,
  type DatasetSummary,
  type DesignerNode,
  type FieldOption,
  type JoinOption,
  type PublishedVersionRecord,
  type PublishedVersionSummary,
  type VersionUsage,
} from '../lib/datasets'
import {
  backgroundTaskAPI,
  rememberBackgroundTaskFocus,
  type BackgroundTask,
  type BackgroundTaskStatus,
} from '../lib/background-tasks'

type RelationInput = GraphInput
type CurveGeometry = { path: string; midpoint: CanvasPoint }
type RelationBox = GraphJoin
type GroupBox = GraphGroup
type GroupMetricSelection = { key: string; aggregation: string }
type TransformBox = GraphTransform
type EndBox = GraphEnd
type CanvasComponentKind = RelationInput['kind'] | 'END'
type CanvasPreviewTarget = RelationInput | { kind: 'END'; id: string }
type CanvasEdgeTarget =
  | { kind: 'JOIN'; id: string; side: 'left' | 'right' }
  | { kind: 'GROUP'; id: string }
  | { kind: 'TRANSFORM'; id: string }
  | { kind: 'END'; id: string }
type PendingEdgeInsertion = { inserted: RelationInput; source: RelationInput; target: CanvasEdgeTarget }
type NodePreviewState = { loading: boolean; data?: AssetTablePreview; error?: string; suggestion?: string }
type VersionPreviewState = { versionID: string; loading: boolean; data?: DatasetPreview; error?: string }
type DatasetVersionDiff = {
  addedFields: string[]; removedFields: string[]; changedFields: string[]
  addedNodes: string[]; removedNodes: string[]; changedNodes: string[]
  metadataChanges: string[]; breakingChanges: number
}
type DialogState = { mode: 'create' | 'view' | 'metadata' | 'edit-metadata' | 'history' | 'materialization' | 'publish' | 'lifecycle'; dataset?: DatasetSummary; lifecycleAction?: 'disable' | 'restore' | 'delete' }
type DatasetBatchAction = 'publish' | 'run' | 'stop' | 'delete'
type DatasetBatchOutcome = { dataset: DatasetSummary; error?: string }
type Notice = { tone: 'success' | 'error'; message: string }
type DraftConflict = { currentVersion?: number; currentHash?: string }
type ModelingLogEntry = {
  id: string
  timestamp: string
  label: string
  message: string
  tone: 'queued' | 'running' | 'success' | 'warning' | 'error'
}
type ModelingMonitorState = {
  tasks: BackgroundTask[]
  ready: boolean
  expected: boolean
  syncError: string
  logsPinned: boolean
}
type ModelingMonitorConfig = {
  trigger: DatasetLLMTrigger
  label: string
  taskKinds: Set<string>
  idleTitle: string
}
type DatasetDetailField = {
  id: string; physicalName: string; code: string; name: string; description: string
  role: string; canonicalType: string; semanticType: string; nullable: boolean; visible: boolean
}
type DatasetMetadataForm = { name: string; description: string; domain: string; subject: string }
type DatasetMetadataEditForm = {
  name: string
  description: string
  subject: string
  fields: DatasetDetailField[]
}
type DatasetEditorSnapshot = {
  draft: DatasetDraft
  relationBoxes: RelationBox[]
  groupBoxes: GroupBox[]
  transformBoxes: TransformBox[]
  endBox: EndBox | null
  nodePositions: Record<string, CanvasPoint>
  metadata: DatasetMetadataForm
}
type DatasetAIUndo = { before: DatasetEditorSnapshot; appliedFingerprint: string }
type DatasetAIReviewLabels = { nodes: Record<string, string>; fields: Record<string, string> }
type DatasetAIRetryAction = 'GENERATE' | 'APPLY' | null
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

const snapshotDatasets: DatasetSummary[] = [
  {
    id: 'snapshot-sales-order-detail', code: 'dwd_sales_order_detail', name: '销售订单经营明细',
    description: '统一订单、客户、商品和渠道口径，支撑经营分析与报告取数。', type: 'MODEL', status: 'DRAFT',
    domainId: 'snapshot-enterprise-operations', originTableId: 'snapshot-table-orders', originTableName: 'fact_sales_order',
    originDataSourceName: '销售业务核心库', layer: 'DWD', tags: ['经营分析', '订单', '待发布'], version: 3,
    dslHash: 'b8a73c62e7210f51', updatedAt: '2026-08-11T09:32:00+08:00',
  },
  {
    id: 'snapshot-customer-dim', code: 'dim_customer_profile', name: '客户主数据维度',
    description: '沉淀客户统一身份、区域与分层标签，已被 8 个下游数据集引用。', type: 'MODEL', status: 'PUBLISHED',
    domainId: 'snapshot-enterprise-operations', originTableId: 'snapshot-table-customers', originTableName: 'dim_customer',
    originDataSourceName: '销售业务核心库', layer: 'DIM', tags: ['客户', '主数据'], version: 6,
    dslHash: 'e92c64a8317bb740', currentPublishedVersionId: 'snapshot-customer-v6', updatedAt: '2026-08-11T08:48:00+08:00',
  },
  {
    id: 'snapshot-channel-summary', code: 'dws_channel_sales_daily', name: '渠道销售日汇总',
    description: '按自然日、渠道和区域汇总订单金额、销量、折扣与履约指标。', type: 'MODEL', status: 'PUBLISHED',
    domainId: 'snapshot-enterprise-operations', layer: 'DWS', tags: ['渠道', '日报'], version: 12,
    dslHash: 'd182abf950d50d11', currentPublishedVersionId: 'snapshot-channel-v12', updatedAt: '2026-08-10T18:16:00+08:00',
  },
  {
    id: 'snapshot-inventory-ods', code: 'ods_inventory_snapshot', name: '库存快照贴源数据',
    description: '保留库存历史表的原始字段结构，等待负责人确认新增字段。', type: 'MAPPING', status: 'VALIDATING',
    domainId: 'snapshot-enterprise-operations', originTableId: 'snapshot-table-inventory', originTableName: 'inventory_snapshot',
    originDataSourceName: '库存历史库', layer: 'ODS', tags: ['库存', '结构变更'], version: 2,
    dslHash: 'a76c927fa49e0281', updatedAt: '2026-08-10T15:05:00+08:00',
  },
  {
    id: 'snapshot-executive-ads', code: 'ads_executive_operation_view', name: '经营驾驶舱应用数据',
    description: '面向管理层驾驶舱交付收入、毛利、订单与库存周转核心指标。', type: 'MODEL', status: 'PUBLISHED',
    domainId: 'snapshot-enterprise-operations', layer: 'ADS', tags: ['驾驶舱', '核心指标'], version: 9,
    dslHash: 'f2b70860a18fd5a2', currentPublishedVersionId: 'snapshot-executive-v9', updatedAt: '2026-08-09T20:10:00+08:00',
  },
]

const snapshotAssetTables: AssetTable[] = [
  {
    id: 'snapshot-table-orders', dataSourceId: 'snapshot-sales-mysql', dataSourceName: '销售业务核心库', dataSourceType: 'MYSQL',
    tableName: 'fact_sales_order', schemaName: 'sales_prod', businessName: '销售订单事实表',
    businessDescription: '按订单行记录销售、优惠、渠道和履约结果。', tags: ['主题:经营分析', '作用:事实表'],
    columnCount: 7, managementStatus: 'ACTIVE', enrichmentStatus: 'SUCCEEDED',
  },
  {
    id: 'snapshot-table-customers', dataSourceId: 'snapshot-sales-mysql', dataSourceName: '销售业务核心库', dataSourceType: 'MYSQL',
    tableName: 'dim_customer', schemaName: 'sales_prod', businessName: '客户主数据表',
    businessDescription: '客户统一身份、区域与等级信息。', tags: ['主题:客户', '作用:维度表'],
    columnCount: 5, managementStatus: 'ACTIVE', enrichmentStatus: 'SUCCEEDED',
  },
  {
    id: 'snapshot-table-products', dataSourceId: 'snapshot-sales-mysql', dataSourceName: '销售业务核心库', dataSourceType: 'MYSQL',
    tableName: 'dim_product', schemaName: 'sales_prod', businessName: '商品主数据表',
    businessDescription: '商品、品类与品牌层级信息。', tags: ['主题:商品', '作用:维度表'],
    columnCount: 5, managementStatus: 'ACTIVE', enrichmentStatus: 'SUCCEEDED',
  },
]

const snapshotAssetColumns: Record<string, AssetColumn[]> = {
  'snapshot-table-orders': [
    ['order_id', '订单编号', 'STRING', 'IDENTIFIER'], ['customer_id', '客户编号', 'STRING', 'IDENTIFIER'],
    ['product_id', '商品编号', 'STRING', 'IDENTIFIER'], ['order_date', '下单日期', 'DATE', 'DATE'],
    ['channel_name', '销售渠道', 'STRING', 'CATEGORY'], ['sales_amount', '销售金额', 'DECIMAL', 'MEASURE'],
    ['quantity', '销售数量', 'INTEGER', 'MEASURE'],
  ].map(([columnName, businessName, canonicalType, semanticType], index) => ({
    id: `snapshot-order-column-${index + 1}`, tableId: 'snapshot-table-orders', columnName, businessName,
    canonicalType, semanticType, nullable: false, assetStatus: 'ACTIVE', ordinalPosition: index + 1,
  })),
  'snapshot-table-customers': [
    ['customer_id', '客户编号', 'STRING', 'IDENTIFIER'], ['customer_name', '客户名称', 'STRING', 'TEXT'],
    ['region_name', '所属区域', 'STRING', 'CATEGORY'], ['customer_level', '客户等级', 'STRING', 'CATEGORY'],
    ['created_at', '建档时间', 'DATETIME', 'DATE'],
  ].map(([columnName, businessName, canonicalType, semanticType], index) => ({
    id: `snapshot-customer-column-${index + 1}`, tableId: 'snapshot-table-customers', columnName, businessName,
    canonicalType, semanticType, nullable: index > 1, assetStatus: 'ACTIVE', ordinalPosition: index + 1,
  })),
  'snapshot-table-products': [
    ['product_id', '商品编号', 'STRING', 'IDENTIFIER'], ['product_name', '商品名称', 'STRING', 'TEXT'],
    ['category_name', '商品品类', 'STRING', 'CATEGORY'], ['brand_name', '品牌', 'STRING', 'CATEGORY'],
    ['standard_price', '标准售价', 'DECIMAL', 'MEASURE'],
  ].map(([columnName, businessName, canonicalType, semanticType], index) => ({
    id: `snapshot-product-column-${index + 1}`, tableId: 'snapshot-table-products', columnName, businessName,
    canonicalType, semanticType, nullable: index > 1, assetStatus: 'ACTIVE', ordinalPosition: index + 1,
  })),
}
const modelingMonitorConfigs: ModelingMonitorConfig[] = [
  {
    trigger: 'DIM_MODELING',
    label: '维度建模',
    taskKinds: new Set(['ODS_DOMAIN_CLASSIFICATION', 'DIM_MODELING']),
    idleTitle: '按 ODS 领域分析业务实体并生成待审批的 DIM 草稿',
  },
  {
    trigger: 'DWD_MODELING',
    label: '明细建模',
    taskKinds: new Set(['DWD_FACT_MODELING']),
    idleTitle: '基于已审批发布的 DIM 和同批次事实分类生成 DWD 草稿',
  },
  {
    trigger: 'DWS_MODELING',
    label: '主题建模',
    taskKinds: new Set(['DWS_MODELING']),
    idleTitle: '通过 LLM 基于已发布的 DWD 与可选 DIM 上下文创建 DWS DAG 草稿',
  },
]
const emptyModelingMonitorState = (): ModelingMonitorState => ({
  tasks: [],
  ready: false,
  expected: false,
  syncError: '',
  logsPinned: false,
})
const emptyModelingMonitors = (): Record<DatasetLLMTrigger, ModelingMonitorState> => ({
  DIM_MODELING: emptyModelingMonitorState(),
  DWD_MODELING: emptyModelingMonitorState(),
  DWS_MODELING: emptyModelingMonitorState(),
})

const modelingSelectionError = (
  trigger: DatasetLLMTrigger,
  selected: DatasetSummary[],
) => {
  if (!selected.length) return ''
  const allowed: Record<DatasetLLMTrigger, Set<DatasetLayer>> = {
    DIM_MODELING: new Set(['ODS']),
    DWD_MODELING: new Set(['ODS', 'DIM']),
    DWS_MODELING: new Set(['DWD', 'DIM']),
  }
  const invalidState = selected.filter(dataset =>
    dataset.status !== 'PUBLISHED' || !dataset.currentPublishedVersionId
  )
  if (invalidState.length) {
    const examples = invalidState.slice(0, 3).map(dataset => dataset.name).join('、')
    return `所选数据集“${examples}”没有当前已发布版本，请完成发布后再建模`
  }
  const invalidLayer = selected.filter(dataset => !allowed[trigger].has(dataset.layer))
  if (invalidLayer.length) {
    const rules: Record<DatasetLLMTrigger, string> = {
      DIM_MODELING: '维度建模只能选择 ODS 数据集',
      DWD_MODELING: '明细建模只能选择 ODS 数据集和可选的 DIM 数据集',
      DWS_MODELING: '主题建模只能选择 DWD 数据集和可选的 DIM 数据集',
    }
    return rules[trigger]
  }
  if (trigger === 'DWD_MODELING' && !selected.some(dataset => dataset.layer === 'ODS')) {
    return '明细建模至少需要选择一个 ODS 数据集，DIM 只能作为可选维度输入'
  }
  if (trigger === 'DWS_MODELING' && !selected.some(dataset => dataset.layer === 'DWD')) {
    return '主题建模至少需要选择一个 DWD 数据集，DIM 只能作为可选维度上下文'
  }
  return ''
}
const activeBackgroundTaskStatuses = new Set<BackgroundTaskStatus>(['QUEUED', 'RUNNING'])
const activeDAGRunStatuses = new Set<DatasetDAGRun['status']>(['QUEUED', 'RUNNING'])
const retryableDAGRunStatuses = new Set<DatasetDAGRun['status']>(['FAILED', 'CANCELLED'])
const isRetryableDAGRun = (run?: DatasetDAGRun) => Boolean(run && retryableDAGRunStatuses.has(run.status))

function dagRunLabel(run?: DatasetDAGRun) {
  if (!run) return '尚未运行物化'
  if (run.status === 'QUEUED') return run.slaStatus === 'AT_RISK' ? '排队中 · SLA 临期' : '物化排队中'
  if (run.status === 'RUNNING') return run.slaBreached ? '执行中 · SLA 已超时' : run.slaStatus === 'AT_RISK' ? '执行中 · SLA 临期' : '物化执行中'
  if (run.status === 'SUCCEEDED') return run.slaBreached ? '物化成功 · SLA 超时' : '物化已完成'
  if (run.status === 'FAILED') return '物化失败 · 可重试'
  return '物化已取消 · 可重试'
}
const backgroundTaskStatusLabels: Record<BackgroundTaskStatus, string> = {
  QUEUED: '排队中',
  RUNNING: '执行中',
  SUCCEEDED: '已完成',
  PARTIAL: '部分完成',
  FAILED: '失败',
  CANCELLED: '已中止',
  SKIPPED: '已跳过',
  STALE: '已失效',
}

const isActiveModelingTask = (task: BackgroundTask) =>
  activeBackgroundTaskStatuses.has(task.status)
const modelingProgress = (tasks: BackgroundTask[]) => {
  if (!tasks.length || !tasks.some(isActiveModelingTask)) return undefined
  const values = tasks.map(task => {
    if (!activeBackgroundTaskStatuses.has(task.status)) return 100
    if (typeof task.progressPercent === 'number') return task.progressPercent
    if (task.status === 'QUEUED') return 0
    return undefined
  })
  if (values.some(value => value === undefined)) return undefined
  return Math.round(values.reduce<number>((sum, value) => sum + (value ?? 0), 0) / values.length)
}
const modelingLogTone = (status: BackgroundTaskStatus): ModelingLogEntry['tone'] => {
  if (status === 'SUCCEEDED' || status === 'SKIPPED') return 'success'
  if (status === 'PARTIAL' || status === 'CANCELLED' || status === 'STALE') return 'warning'
  if (status === 'FAILED') return 'error'
  return status === 'RUNNING' ? 'running' : 'queued'
}
const modelingLogEntries = (
  tasks: BackgroundTask[],
  awaitingDiscovery: boolean,
  trigger: DatasetLLMTrigger,
): ModelingLogEntry[] => {
  const entries: ModelingLogEntry[] = []
  for (const task of tasks) {
    entries.push({
      id: `${task.id}:queued`,
      timestamp: task.createdAt,
      label: '已提交',
      message: `${task.name}已进入${task.kindLabel}队列`,
      tone: 'queued',
    })
    if (task.startedAt) {
      entries.push({
        id: `${task.id}:started`,
        timestamp: task.startedAt,
        label: '执行中',
        message: `${task.name}开始执行（第 ${task.attempt} / ${task.maxAttempts} 次）`,
        tone: 'running',
      })
    }
    if (isActiveModelingTask(task) && task.updatedAt !== task.startedAt &&
      task.updatedAt !== task.createdAt) {
      entries.push({
        id: `${task.id}:updated:${task.updatedAt}`,
        timestamp: task.updatedAt,
        label: task.status === 'QUEUED' ? '排队中' : '处理中',
        message: `${task.name}：${task.progressText}`,
        tone: task.status === 'QUEUED' ? 'queued' : 'running',
      })
    }
    if (!activeBackgroundTaskStatuses.has(task.status)) {
      entries.push({
        id: `${task.id}:completed`,
        timestamp: task.completedAt || task.updatedAt,
        label: backgroundTaskStatusLabels[task.status],
        message: task.errorMessage
          ? `${task.name}：${task.errorMessage}`
          : `${task.name}${task.progressText ? `：${task.progressText}` : ''}`,
        tone: modelingLogTone(task.status),
      })
    }
  }
  if (awaitingDiscovery && !entries.length) {
    entries.push({
      id: `${trigger}:connecting`,
      timestamp: new Date().toISOString(),
      label: '连接中',
      message: '任务已提交，正在读取持久化任务记录…',
      tone: 'running',
    })
  }
  return entries
    .sort((left, right) => new Date(left.timestamp).getTime() - new Date(right.timestamp).getTime())
    .slice(-40)
}

function DatasetModelingAction({
  config,
  monitor,
  actionBusy,
  submitting,
  logID,
  onTrigger,
  onTogglePinned,
}: {
  config: ModelingMonitorConfig
  monitor: ModelingMonitorState
  actionBusy: boolean
  submitting: boolean
  logID: string
  onTrigger: () => void
  onTogglePinned: () => void
}) {
  const activeTasks = monitor.tasks.filter(isActiveModelingTask)
  const busy = submitting || monitor.expected || activeTasks.length > 0
  const progressPercent = modelingProgress(monitor.tasks)
  const displayProgressPercent = progressPercent ??
    (!busy && monitor.tasks.length ? 100 : undefined)
  const progressLabel = displayProgressPercent === undefined
    ? busy ? '执行中' : '—'
    : `${displayProgressPercent}%`
  const completedCount = monitor.tasks.length - activeTasks.length
  const logs = modelingLogEntries(
    monitor.tasks,
    busy && !monitor.tasks.length,
    config.trigger,
  )
  const hasLogs = busy || monitor.tasks.length > 0 || Boolean(monitor.syncError)
  const buttonLabel = !monitor.ready
    ? '状态同步中'
    : submitting
      ? '正在提交…'
      : busy && progressPercent !== undefined
        ? `${config.label} ${progressPercent}%`
        : busy
          ? `${config.label}中`
          : config.label
  const buttonStyle = progressPercent === undefined
    ? undefined
    : { '--dataset-modeling-progress': `${progressPercent}%` } as CSSProperties
  const status = monitor.syncError
    ? '同步重试中'
    : busy
      ? '每 3 秒刷新'
      : `${logs.length} 条`

  return <div
    className={`dataset-modeling-action${busy ? ' is-running' : ''}${progressPercent === undefined ? ' is-indeterminate' : ' is-determinate'}${hasLogs ? ' has-logs' : ''}${monitor.logsPinned ? ' is-pinned' : ''}`}
    onKeyDown={event => {
      if (event.key === 'Escape' && monitor.logsPinned) onTogglePinned()
    }}
  >
    <button
      className="dataset-modeling-trigger"
      type="button"
      disabled={actionBusy || !monitor.ready || busy}
      aria-busy={busy}
      aria-describedby={hasLogs ? logID : undefined}
      style={buttonStyle}
      title={busy ? `${config.label}正在运行；点击右上角日志按钮查看进度` : config.idleTitle}
      onClick={onTrigger}
    >
      <span>{buttonLabel}</span>
    </button>
    {hasLogs && <button
      className="dataset-modeling-log-toggle"
      type="button"
      aria-label={monitor.logsPinned ? `取消固定${config.label}日志` : `固定显示${config.label}日志`}
      aria-controls={logID}
      aria-expanded={monitor.logsPinned}
      onClick={onTogglePinned}
    >
      <ListChecksIcon size={13} weight="bold" aria-hidden="true" />
    </button>}
    {hasLogs && <section
      className="dataset-modeling-log-popover"
      id={logID}
      role="region"
      aria-label={`${config.label}实时日志`}
    >
      <header>
        <div><MagicWandIcon size={17} weight="duotone" aria-hidden="true" /><span><strong>{config.label}运行日志</strong><small>真实任务状态 · 安全摘要</small></span></div>
        <em>{status}</em>
      </header>
      <div className="dataset-modeling-log-progress">
        <span>{monitor.tasks.length
          ? `已结束 ${completedCount} / ${monitor.tasks.length} 个任务`
          : monitor.ready ? '正在发现任务阶段…' : '正在同步任务状态…'}</span>
        <strong>{progressLabel}</strong>
        <progress
          max={100}
          value={displayProgressPercent}
          aria-label={`${config.label}总体进度`}
          aria-valuetext={displayProgressPercent === undefined ? '任务执行中，暂时无法可靠估算百分比' : `${displayProgressPercent}%`}
        />
      </div>
      {monitor.syncError && <div className="dataset-modeling-log-error" role="alert">
        状态同步暂时失败：{monitor.syncError}
      </div>}
      <ol className="dataset-modeling-live-log" role="log" aria-live="polite" aria-relevant="additions text">
        {!logs.length && <li className="running">
          <time aria-hidden="true">--:--:--</time><span>连接中</span><strong>正在读取{config.label}任务…</strong>
        </li>}
        {logs.map(entry => <li className={entry.tone} key={entry.id}>
          <time dateTime={entry.timestamp}>{new Date(entry.timestamp).toLocaleTimeString('zh-CN', { hour12: false })}</time>
          <span>{entry.label}</span>
          <strong>{entry.message}</strong>
        </li>)}
      </ol>
      <footer>点击右上日志按钮打开或关闭；日志不展示模型输入、原始输出或业务数据。</footer>
    </section>}
  </div>
}

const layerOverview: Array<{ layer: DatasetLayer; name: string; description: string }> = [
  { layer: 'ODS', name: '贴源层', description: '结构映射 · 数据留在来源' },
  { layer: 'DIM', name: '维度层', description: '实体说明' },
  { layer: 'DWD', name: '明细层', description: '动作与维度' },
  { layer: 'DWS', name: '汇总层', description: '分析视角' },
  { layer: 'ADS', name: '应用数据', description: '按需交付' },
]
const typeLabels: Record<string, string> = { SINGLE_SOURCE: '单数据源', CROSS_SOURCE: '跨数据源' }
const publicationStatusLabels: Record<string, string> = {
  PENDING: '待审批',
  APPROVED: '已通过',
  REJECTED: '已拒绝',
  CANCELLED: '已取消',
}
type TransformPaletteCategory = 'TEXT' | 'NUMBER' | 'DATE' | 'WINDOW' | 'RULE'
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
  { componentType: 'WINDOW_FUNCTION', family: 'WINDOW', category: 'WINDOW', label: '窗口计算', description: '按分区与排序排名或聚合', sortKey: 'CHUANGKOUJISUAN', operations: ['WINDOW'], icon: RowsIcon },
  { componentType: 'TEXT_CASE', family: 'TEXT', category: 'TEXT', label: '大小写转换', description: '英文字母统一转为大写或小写', sortKey: 'DAXIAOXIEZHUANHUAN', operations: ['UPPER', 'LOWER'], icon: TextAaIcon },
  { componentType: 'TEXT_TRIM', family: 'TEXT', category: 'TEXT', label: '空格清理', description: '去除文本首尾空格', sortKey: 'KONGGEQINGLI', operations: ['TRIM'], icon: TextTSlashIcon },
  { componentType: 'TEXT_REPLACE', family: 'TEXT', category: 'TEXT', label: '文本替换', description: '查找并替换指定文本', sortKey: 'WENBENTIHUAN', operations: ['REPLACE'], icon: SwapIcon },
  { componentType: 'TEXT_SUBSTRING', family: 'TEXT', category: 'TEXT', label: '字段截取', description: '按起始位置截取文本', sortKey: 'ZIDUANJIEQU', operations: ['SUBSTRING'], icon: ScissorsIcon },
  { componentType: 'TEXT_CONCAT', family: 'TEXT', category: 'TEXT', label: '字段拼接', description: '用连接符拼接两字段', sortKey: 'ZIDUANPINJIE', operations: ['CONCAT'], icon: LinkSimpleIcon },
  { componentType: 'NUMBER_ABSOLUTE', family: 'NUMBER', category: 'NUMBER', label: '取绝对值', description: '将负数转换为正数值', sortKey: 'QUJUEDUIZHI', operations: ['ABS'], icon: PlusMinusIcon },
  { componentType: 'NUMBER_ROUNDING', family: 'NUMBER', category: 'NUMBER', label: '数值取整', description: '四舍五入或上下取整', sortKey: 'SHUZHIQUZHENG', operations: ['ROUND', 'FLOOR', 'CEIL'], icon: ApproximateEqualsIcon },
  { componentType: 'NUMBER_ARITHMETIC', family: 'NUMBER', category: 'NUMBER', label: '数值运算', description: '两个字段加减乘除', sortKey: 'SHUZHIYUNSUAN', operations: ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE'], icon: MathOperationsIcon },
  { componentType: 'DATE_CALCULATION', family: 'DATE', category: 'DATE', label: '日期计算', description: '日期差、CURRENT_DATE 与周期首末日', sortKey: 'RIQIJISUAN', operations: ['DATE_DIFF', 'DATE_EXTRACT', 'DATE_START', 'DATE_END', 'CURRENT_DATE'], icon: CalendarDotsIcon },
  { componentType: 'DATE_FORMAT', family: 'DATE', category: 'DATE', label: '日期转换', description: '输出年、年月、年季或年月日', sortKey: 'RIQIZHUANHUAN', operations: ['DATE_FORMAT'], icon: CalendarDotsIcon },
  { componentType: 'FILTER', family: 'CONDITION', category: 'RULE', label: '过滤组件', description: '按固定值或字段关系筛选数据行', sortKey: 'GUOLVZUJI', operations: [], icon: FunnelIcon },
  { componentType: 'NULL', family: 'NULL', category: 'RULE', label: '空值填充', description: '为空时补固定值、字段或 CURRENT_DATE', sortKey: 'KONGZHITIANCHONG', operations: ['COALESCE'], icon: DropSlashIcon },
  { componentType: 'CAST', family: 'CAST', category: 'RULE', label: '类型转换', description: '规范字段的数据类型', sortKey: 'LEIXINGZHUANHUAN', operations: ['CAST'], icon: ArrowsLeftRightIcon },
  { componentType: 'CONDITION', family: 'CONDITION', category: 'RULE', label: '条件映射', description: '按条件输出固定值、原字段或 CURRENT_DATE', sortKey: 'TIAOJIANYINGSHE', operations: ['CASE'], icon: ListChecksIcon },
]
const transformCategoryMeta: Array<{ category: TransformPaletteCategory; label: string; className: string }> = [
  // 日期处理是高频字段加工能力，放在流程组件之后，避免在短视口中被文本和
  // 数值组件挤到不可见区域。
  { category: 'DATE', label: '日期组件', className: 'component-date' },
  { category: 'TEXT', label: '文本组件', className: 'component-text' },
  { category: 'NUMBER', label: '数值组件', className: 'component-number' },
  { category: 'WINDOW', label: '分析组件', className: 'component-window' },
  { category: 'RULE', label: '规则组件', className: 'component-rule' },
]
const transformComponentDefinition = (componentType: GraphTransformComponentType) => transformComponentMeta.find(item => item.componentType === componentType)
const transformComponentTypeFor = (transform: Pick<GraphTransform, 'family' | 'componentType' | 'rules'>): GraphTransformComponentType => {
  if (transform.componentType) return normalizeGraphTransformComponentType(transform.componentType) || transform.componentType
  const operation = transform.rules[0]?.operation
  if (transform.family === 'DATE') return 'DATE_FORMAT'
  if (transform.family === 'CAST') return 'CAST'
  if (transform.family === 'CONDITION') return 'CONDITION'
  if (transform.family === 'NULL') return 'NULL'
  if (transform.family === 'WINDOW') return 'WINDOW_FUNCTION'
  if (transform.family === 'NUMBER') {
    if (operation === 'ABS') return 'NUMBER_ABSOLUTE'
    if (operation && ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE'].includes(operation)) return 'NUMBER_ARITHMETIC'
    return 'NUMBER_ROUNDING'
  }
  if (operation === 'UPPER' || operation === 'LOWER') return 'TEXT_CASE'
  if (operation === 'TRIM') return 'TEXT_TRIM'
  if (operation === 'REPLACE') return 'TEXT_REPLACE'
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
const dateCalculationUnitOptions = {
  DATE_DIFF: [
    { value: 'YEAR', label: '自然年差' }, { value: 'MONTH', label: '自然月差' }, { value: 'DAY', label: '自然日差' },
  ],
  DATE_EXTRACT: [
    { value: 'YEAR', label: '年' }, { value: 'QUARTER', label: '季度' }, { value: 'MONTH', label: '月' },
    { value: 'WEEK', label: 'ISO 周' }, { value: 'DAY', label: '日' }, { value: 'WEEKDAY', label: '星期（周一为 1）' },
    { value: 'DAY_OF_YEAR', label: '年内第几天' },
  ],
  DATE_START: [
    { value: 'WEEK', label: '周第一天（周一）' }, { value: 'MONTH', label: '月第一天' },
    { value: 'QUARTER', label: '季度第一天' }, { value: 'YEAR', label: '年第一天' },
  ],
  DATE_END: [
    { value: 'WEEK', label: '周最后一天（周日）' }, { value: 'MONTH', label: '月最后一天' },
    { value: 'QUARTER', label: '季度最后一天' }, { value: 'YEAR', label: '年最后一天' },
  ],
} as const
type DateCalculationOperation = keyof typeof dateCalculationUnitOptions
const dateCalculationDefaultUnit = (operation: DateCalculationOperation): GraphTransformRule['unit'] =>
  dateCalculationUnitOptions[operation][0].value
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
const isTime = (column: AssetColumn) => ['DATE', 'DATETIME', 'TIMESTAMP'].includes(column.canonicalType.toUpperCase()) || column.semanticType.toUpperCase() === 'DATE'
const emptyDraft = (): DatasetDraft => ({ code: '', name: '', description: '', layer: 'DWD', nodes: [], fields: [], joins: [], filters: [], parameters: [], calculations: [], sorts: [], grainDescription: '', grainKeys: [], groupingEnabled: false, finalConfigured: false, finalGroupingEnabled: false })
const datasetLayers: DatasetLayer[] = ['ODS', 'DIM', 'DWD', 'DWS', 'ADS']
const datasetLayerLabels: Record<DatasetLayer, string> = {
  ODS: 'ODS · 贴源层',
  DIM: 'DIM · 维度层',
  DWD: 'DWD · 明细层',
  DWS: 'DWS · 汇总层',
  ADS: 'ADS · 应用交付数据',
}
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

async function mapDatasetBatch(
  items: DatasetSummary[],
  operation: (dataset: DatasetSummary) => Promise<void>,
  concurrency = 5,
): Promise<DatasetBatchOutcome[]> {
  const outcomes = new Array<DatasetBatchOutcome>(items.length)
  let nextIndex = 0
  const worker = async () => {
    for (;;) {
      const index = nextIndex++
      if (index >= items.length) return
      const dataset = items[index]
      try {
        await operation(dataset)
        outcomes[index] = { dataset }
      } catch (cause) {
        outcomes[index] = {
          dataset,
          error: cause instanceof Error ? cause.message : '操作失败',
        }
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, worker))
  return outcomes
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

const designerAssetLayers: DatasetLayer[] = ['ODS', 'DIM', 'DWD', 'DWS']
const designerLayerLabels: Record<DatasetLayer, { name: string; description: string }> = {
  ODS: { name: '贴源层', description: '字段结构映射 · 数据不复制入仓' },
  DIM: { name: '维度层', description: '统一业务实体说明' },
  DWD: { name: '明细层', description: '标准业务过程明细' },
  DWS: { name: '汇总层', description: '跨事实主题聚合' },
  ADS: { name: 'ADS 应用数据', description: '面向应用的数据交付' },
}

function datasetVersionColumns(table: AssetTable, version: PublishedVersionRecord): AssetColumn[] {
  return version.dsl.fields.flatMap((raw, index) => {
    const field = raw && typeof raw === 'object' ? raw as Record<string, unknown> : {}
    const code = typeof field.code === 'string' ? field.code : ''
    if (!code) return []
    const name = typeof field.name === 'string' ? field.name : code
    return [{
      id: `${version.id}:${typeof field.id === 'string' && field.id ? field.id : code}`,
      tableId: table.id,
      columnName: code,
      businessName: name,
      businessDescription: typeof field.description === 'string' ? field.description : '',
      canonicalType: typeof field.canonicalType === 'string' && field.canonicalType ? field.canonicalType : 'STRING',
      nullable: field.nullable === true,
      semanticType: typeof field.semanticType === 'string' ? field.semanticType : '',
      assetStatus: 'ACTIVE',
      ordinalPosition: index + 1,
    }]
  })
}

function withDesignerNodePreviewMetadata(node: DesignerNode, preview: AssetTablePreview): AssetTablePreview {
  const columnsByCode = new Map(node.columns.map(column => [column.columnName, column]))
  const columnsByNormalizedCode = new Map(node.columns.map(column => [column.columnName.toLocaleLowerCase(), column]))
  const metadataByCode = new Map((preview.columnMetadata ?? []).flatMap(metadata => {
    const keys = [metadata.code, metadata.physicalName].filter((value): value is string => Boolean(value))
    return keys.map(key => [key.toLocaleLowerCase(), metadata] as const)
  }))
  const columnMetadata: DatasetPreviewColumn[] = preview.columns.map((code, index) => {
    const serverMetadata = metadataByCode.get(code.toLocaleLowerCase()) ?? preview.columnMetadata?.[index]
    if (serverMetadata) return serverMetadata
    const column = columnsByCode.get(code) ?? columnsByNormalizedCode.get(code.toLocaleLowerCase())
    return {
      fieldId: column?.id,
      code,
      name: column?.businessName?.trim() || column?.columnName || code,
      description: column?.businessDescription,
      physicalName: column?.columnName || code,
      canonicalType: column?.canonicalType,
      semanticType: column?.semanticType,
      nullable: column?.nullable ?? true,
    }
  })
  return { ...preview, columnMetadata }
}

function publishedDatasetAsset(dataset: DatasetSummary, version: PublishedVersionRecord): AssetTable {
  return {
    id: `dataset-version:${version.id}`,
    dataSourceId: `dataset-layer:${dataset.layer}`,
    dataSourceName: designerLayerLabels[dataset.layer].name,
    dataSourceType: dataset.layer,
    tableName: dataset.code,
    schemaName: dataset.layer,
    businessName: dataset.name,
    businessDescription: dataset.description,
    tags: dataset.tags,
    columnCount: version.dsl.fields.length,
    sourceKind: 'DATASET',
    datasetId: dataset.id,
    datasetVersionId: version.id,
    datasetLayer: dataset.layer,
  }
}

async function loadDesignerAssets(datasetItems: DatasetSummary[], excludedDatasetID = ''): Promise<AssetTable[]> {
  const rawTablesPromise = loadAllTables()
  const published = datasetItems.filter(dataset =>
    dataset.id !== excludedDatasetID &&
    designerAssetLayers.includes(dataset.layer) && Boolean(dataset.currentPublishedVersionId),
  )
  const versionResults = await Promise.allSettled(published.map(async dataset => {
    const version = await datasetAPI.getVersion(dataset.id, dataset.currentPublishedVersionId!)
    return publishedDatasetAsset(dataset, version)
  }))
  const rawTables = await rawTablesPromise
  return [
    ...rawTables,
    ...versionResults.flatMap(result => result.status === 'fulfilled' ? [result.value] : []),
  ]
}

const nodeFieldCode = (node: DesignerNode, columnName: string, multiple: boolean) => multiple ? `${node.alias}_${columnName}` : columnName
const safeIdentifier = (value: string) => value.trim().replace(/[^A-Za-z0-9_]/g, '_').replace(/^[^A-Za-z]+/, '') || 'field'
const groupFieldOutputKey = (groupID: string, role: 'dimension' | 'metric', field: ProducedField) =>
  `${safeIdentifier(groupID)}.${role}_${safeIdentifier(field.code)}`
const numericCanonicalTypes = new Set(['NUMBER', 'INT', 'INTEGER', 'DECIMAL', 'FLOAT', 'DOUBLE'])
const dateCanonicalTypes = new Set(['DATE', 'DATETIME', 'TIMESTAMP'])
const rankingWindowFunctions = new Set(['ROW_NUMBER', 'RANK', 'DENSE_RANK'])
const transformOperations = (transform: Pick<GraphTransform, 'family' | 'componentType'>): GraphTransformOperation[] => {
  if (transform.componentType) return transformComponentDefinition(transform.componentType)?.operations || []
  if (transform.family === 'DATE') return ['DATE_FORMAT']
  if (transform.family === 'TEXT') return ['SUBSTRING', 'TRIM', 'UPPER', 'LOWER', 'REPLACE', 'CONCAT']
  if (transform.family === 'CAST') return ['CAST']
  if (transform.family === 'NUMBER') return ['ROUND', 'ABS', 'FLOOR', 'CEIL', 'ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE']
  if (transform.family === 'CONDITION') return ['CASE']
  if (transform.family === 'NULL') return ['COALESCE']
  if (transform.family === 'WINDOW') return ['WINDOW']
  return ['CONCAT', 'SUBSTRING']
}
const transformOperationLabel: Record<GraphTransformOperation, string> = {
  WINDOW: 'OVER 窗口函数',
  CURRENT_DATE: '获取当前日期', DATE_DIFF: '计算两个日期的差', DATE_EXTRACT: '提取日期部分', DATE_START: '获取周期第一天', DATE_END: '获取周期最后一天',
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
  if (numericCanonicalTypes.has(type)) return '999999999'
  if (type === 'BOOLEAN') return 'False'
  if (dateCanonicalTypes.has(type)) return '1970-01-01'
  return 'UNKNOWN'
}
const defaultTransformRule = (transform: TransformBox, fields: ProducedField[], index: number): GraphTransformRule => {
  const operation = transformOperations(transform)[0]
  const candidates = transformFieldCandidates(transform.family, fields)
  const first = candidates[0]
  const binary = ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'CONCAT', 'DATE_DIFF'].includes(operation)
  const suffix: Record<GraphTransformOperation, string> = {
    WINDOW: 'row_number',
    CURRENT_DATE: 'current_date', DATE_DIFF: 'date_diff', DATE_EXTRACT: 'date_part', DATE_START: 'period_start', DATE_END: 'period_end',
    DATE_FORMAT: dateFormatMeta.DAY.suffix, DATE_TRUNC: 'date', CAST: 'text', ADD: 'calculated', SUBTRACT: 'calculated', MULTIPLY: 'calculated', DIVIDE: 'calculated',
    ROUND: 'rounded', ABS: 'absolute', FLOOR: 'floor', CEIL: 'ceil', CONCAT: 'merged', COALESCE: 'filled', CASE: 'mapped', SUBSTRING: 'substring',
    TRIM: 'trimmed', UPPER: 'uppercase', LOWER: 'lowercase', REPLACE: 'replaced',
  }
  const outputType = transform.componentType === 'DATE_CALCULATION'
    ? ['CURRENT_DATE', 'DATE_START', 'DATE_END'].includes(operation) ? 'DATE' : 'INTEGER'
    : transform.family === 'DATE' ? 'STRING'
    : transform.family === 'CAST' ? 'STRING'
      : transform.family === 'NUMBER' ? 'DECIMAL'
        : transform.family === 'WINDOW' ? 'INTEGER'
        : transform.family === 'NULL' ? first?.canonicalType || 'STRING' : 'STRING'
  const label = transformDisplayLabel(transform)
  return {
    id: `rule_${index}`,
    operation,
    inputKeys: operation === 'WINDOW' ? [] : first ? binary ? [first.key, candidates[1]?.key || first.key] : [first.key] : [],
    output: { id: `output_${index}`, name: operation === 'WINDOW' ? '分区行号' : operation === 'CURRENT_DATE' ? '当前日期' : first ? transform.componentType === 'DATE_CALCULATION' ? `${first.name}${transformOperationLabel[operation]}` : transform.family === 'DATE' ? `${first.name}${dateFormatMeta.DAY.label}` : `${first.name}${label}结果` : `${label}结果`, code: safeIdentifier(operation === 'WINDOW' ? 'partition_row_number' : operation === 'CURRENT_DATE' ? 'current_date' : `${first?.code || 'field'}_${suffix[operation]}`), canonicalType: outputType },
    ...(operation === 'WINDOW' ? {
      windowFunction: 'ROW_NUMBER' as const,
      partitionByKeys: first ? [first.key] : [],
      orderBy: candidates[1] || first ? [{ id: `window_order_${index}_1`, key: (candidates[1] || first)!.key, direction: 'ASC' as const }] : [],
    } : {}),
    ...(operation === 'DATE_FORMAT' ? { unit: 'DAY' as const } : {}),
    ...(operation === 'DATE_DIFF' ? { unit: 'DAY' as const, startDateSource: 'FIELD' as const, endDateSource: 'FIELD' as const } : {}),
    ...(operation === 'DATE_EXTRACT' ? { unit: 'YEAR' as const, dateSource: 'FIELD' as const } : {}),
    ...(operation === 'DATE_START' || operation === 'DATE_END' ? { unit: 'MONTH' as const, dateSource: 'FIELD' as const } : {}),
    ...(operation === 'CAST' ? { targetType: 'STRING' as const } : {}),
    ...(operation === 'CASE' ? { conditionOperator: 'EQUALS' as const, matchValue: '', thenMode: 'LITERAL' as const, thenValue: '', elseMode: 'LITERAL' as const, elseValue: '' } : {}),
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
const transformRuleInputCount = (
  operation: GraphTransformOperation,
  fallbackMode?: GraphTransformRule['fallbackMode'],
  dateSource?: GraphTransformRule['dateSource'],
  startDateSource?: GraphTransformRule['startDateSource'],
  endDateSource?: GraphTransformRule['endDateSource'],
) =>
  operation === 'WINDOW' || operation === 'CURRENT_DATE' || (operation === 'DATE_EXTRACT' || operation === 'DATE_START' || operation === 'DATE_END') && dateSource === 'CURRENT_DATE'
    ? 0
    : operation === 'DATE_DIFF'
      ? Number(startDateSource !== 'CURRENT_DATE') + Number(endDateSource !== 'CURRENT_DATE')
      : ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'CONCAT'].includes(operation) || operation === 'COALESCE' && fallbackMode === 'FIELD' ? 2 : 1
const transformRuleIsComplete = (rule: GraphTransformRule) => {
  const inputCount = transformRuleInputCount(rule.operation, rule.fallbackMode, rule.dateSource, rule.startDateSource, rule.endDateSource)
  const inputsComplete = rule.inputKeys.length >= inputCount && rule.inputKeys.slice(0, inputCount).every(Boolean)
  const unaryCondition = rule.conditionOperator === 'IS_NULL' || rule.conditionOperator === 'IS_NOT_NULL'
  const inCondition = rule.conditionOperator === 'IN'
  const collectionComplete = !inCondition || Boolean(rule.conditionValues?.length && rule.conditionValues.every(item => item.value.trim()))
  const thenComplete = rule.thenMode === 'CURRENT_DATE' || rule.thenMode === 'FIELD' || rule.thenValue !== undefined
  const elseComplete = rule.elseMode === 'CURRENT_DATE' || rule.elseMode === 'FIELD' || rule.elseValue !== undefined
  const caseComplete = rule.operation !== 'CASE' || ((unaryCondition || inCondition || Boolean(rule.matchValue?.length)) && collectionComplete && thenComplete && elseComplete)
  const fallbackComplete = rule.operation !== 'COALESCE' || rule.fallbackMode === 'FIELD' || rule.fallbackMode === 'CURRENT_DATE' || rule.fallbackValue !== undefined
  const precisionComplete = rule.operation !== 'ROUND' || Number.isInteger(rule.precision) && (rule.precision ?? 0) >= -10 && (rule.precision ?? 0) <= 10
  const substringComplete = rule.operation !== 'SUBSTRING' || Number.isInteger(rule.start) && Number.isInteger(rule.length) && (rule.start ?? 0) >= 1 && (rule.length ?? -1) >= 0
  const replaceComplete = rule.operation !== 'REPLACE' || Boolean(rule.searchValue?.length)
  const windowComplete = rule.operation !== 'WINDOW' || Boolean(
    rule.windowFunction &&
    (rankingWindowFunctions.has(rule.windowFunction) || rule.windowValueKey) &&
    rule.partitionByKeys?.length &&
    rule.orderBy?.length &&
    rule.orderBy.every(item => item.key && (item.direction === 'ASC' || item.direction === 'DESC')),
  )
  return inputsComplete && caseComplete && fallbackComplete && precisionComplete && substringComplete && replaceComplete && windowComplete && Boolean(rule.output.name.trim() && rule.output.code.trim() && rule.output.canonicalType.trim())
}
const filterOperatorNeedsValue = (operator: GraphConditionOperator) => operator !== 'IS_NULL' && operator !== 'IS_NOT_NULL'
const filterOperatorSupportsField = (operator: GraphConditionOperator) => filterOperatorNeedsValue(operator) && operator !== 'IN' && operator !== 'NOT_IN'
const filterComparableType = (value: string) => {
  const type = value.toUpperCase()
  if (numericCanonicalTypes.has(type)) return 'NUMBER'
  if (dateCanonicalTypes.has(type)) return 'TEMPORAL'
  if (['STRING', 'TEXT', 'VARCHAR', 'CHAR'].includes(type)) return 'STRING'
  return type
}
const filterFieldsAreCompatible = (left: ProducedField | undefined, right: ProducedField) => Boolean(
  left && filterComparableType(left.canonicalType) === filterComparableType(right.canonicalType),
)
const filterConditionIsComplete = (condition: GraphFilterCondition) => Boolean(
  condition.inputKey &&
  condition.operator &&
  (!filterOperatorNeedsValue(condition.operator) || condition.value.trim()) &&
  (condition.valueMode !== 'FIELD' || filterOperatorSupportsField(condition.operator)),
)
const transformIsFilter = (transform: Pick<GraphTransform, 'componentType'>) => transform.componentType === 'FILTER'
const transformIsComplete = (transform: GraphTransform) => Boolean(
  transform.input && transform.name.trim() && (
    transformIsFilter(transform)
      ? transform.conditions?.length && transform.conditions.every(filterConditionIsComplete)
      : transform.rules.length && transform.rules.every(transformRuleIsComplete)
  ),
)
const groupingSetsAreComplete = (group: GraphGroup) => {
  if (group.groupByMode !== 'GROUPING_SETS') return true
  const dimensionKeys = new Set(group.dimensions.map(dimension => dimension.key))
  const groupingSets = group.groupingSets ?? []
  if (!groupingSets.length || groupingSets.length > 64 || groupingSets.some(groupingSet => groupingSet.some(key => !dimensionKeys.has(key)))) return false
  const canonical = groupingSets.map(groupingSet => [...new Set(groupingSet)].sort().join('\u0000'))
  return groupingSets.every(groupingSet => new Set(groupingSet).size === groupingSet.length) && new Set(canonical).size === canonical.length
}
const groupIsComplete = (group: GraphGroup) => Boolean(
  group.input &&
  group.dimensions.length &&
  group.metrics.length &&
  group.metrics.every(metric => metric.aggregation) &&
  (group.groupByMode !== 'CUBE' || group.dimensions.length >= 2 && group.dimensions.length <= 8) &&
  groupingSetsAreComplete(group),
)
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

type SnapshotEditorState = {
  draft: DatasetDraft
  relationBoxes: RelationBox[]
  groupBoxes: GroupBox[]
  transformBoxes: TransformBox[]
  endBox: EndBox
  nodePositions: Record<string, CanvasPoint>
  metadata: DatasetMetadataForm
}

const snapshotEditorState = (): SnapshotEditorState => {
  const orderNode: DesignerNode = {
    id: 'node_1', alias: 'orders', table: snapshotAssetTables[0], columns: snapshotAssetColumns['snapshot-table-orders'],
    selected: snapshotAssetColumns['snapshot-table-orders'].map(column => column.columnName),
  }
  const customerNode: DesignerNode = {
    id: 'node_2', alias: 'customer', table: snapshotAssetTables[1], columns: snapshotAssetColumns['snapshot-table-customers'],
    selected: snapshotAssetColumns['snapshot-table-customers'].map(column => column.columnName),
  }
  const nodes = [orderNode, customerNode]
  const fields = nodes.flatMap(node => node.columns.map(column => fieldOption(node, column)))
  const outputKeys = [
    'node_1.order_id', 'node_1.order_date', 'node_1.channel_name', 'node_1.sales_amount', 'node_1.quantity',
    'node_2.customer_name', 'node_2.region_name', 'node_2.customer_level',
  ]
  const relationBoxes: RelationBox[] = [{
    id: 'join_1', name: '关联客户区域', left: { kind: 'NODE', id: orderNode.id }, right: { kind: 'NODE', id: customerNode.id },
    position: { x: 560, y: 248 }, outputKeys,
  }]
  const joins: JoinOption[] = [{
    id: 'join_1', leftNodeId: orderNode.id, rightNodeId: customerNode.id,
    leftField: 'customer_id', rightField: 'customer_id', joinType: 'LEFT', cardinality: 'MANY_TO_ONE', manualConfirmed: true,
    conditions: [{ id: 'join_1_condition_1', leftField: 'customer_id', rightField: 'customer_id', operator: 'EQUALS' }],
  }]
  const endBox: EndBox = {
    id: 'end_1', name: '销售订单经营明细', input: { kind: 'JOIN', id: 'join_1' }, position: { x: 970, y: 248 },
    outputs: outputKeys.map(key => {
      const option = fields.find(field => field.key === key)
      return { key, name: option?.name || key, code: option?.code || key.replace('.', '_') }
    }),
  }
  const nodePositions = { node_1: { x: 90, y: 120 }, node_2: { x: 90, y: 320 } }
  const designer: DesignerGraphV1 = {
    version: '1.0', nodePositions,
    nodeNames: { node_1: orderNode.table.businessName, node_2: customerNode.table.businessName },
    joins: relationBoxes, groups: [], transforms: [], end: endBox,
  }
  return {
    draft: {
      ...emptyDraft(), code: 'dwd_sales_order_detail', name: '销售订单经营明细',
      description: '统一订单、客户、商品和渠道口径，支撑经营分析与报告取数。',
      domain: '企业经营', subject: '经营分析', layer: 'DWD', nodes, fields, joins,
      grainDescription: '每一行代表一笔销售订单明细', grainKeys: ['orders_order_id'],
      finalConfigured: true, finalOutputKeys: outputKeys, designer,
    },
    relationBoxes, groupBoxes: [], transformBoxes: [], endBox, nodePositions,
    metadata: {
      name: '销售订单经营明细', description: '统一订单、客户、商品和渠道口径，支撑经营分析与报告取数。',
      domain: '企业经营', subject: '经营分析',
    },
  }
}

const snapshotDatasetRecord = (summary: DatasetSummary): DatasetRecord => ({
  ...summary,
  draftVersionId: `${summary.id}-draft-v${summary.version}`,
  draftVersionNo: summary.version,
  draftRecordVersion: summary.version,
  planHash: `plan-${summary.dslHash}`,
  dsl: {
    dslVersion: '1.0',
    dataset: {
      code: summary.code, name: summary.name, description: summary.description, domain: '企业经营', subject: '经营分析',
      type: summary.type, layer: summary.layer,
    },
    nodes: [], fields: [],
  },
  logicalPlan: {},
  createdAt: '2026-08-08T10:00:00+08:00',
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
    joinType: 'LEFT', cardinality: joinCardinalityForType('LEFT'), manualConfirmed: false,
    conditions: [{ id: `join_${index}_condition_1`, leftField, rightField, operator: 'EQUALS' }],
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
  : [{ id: `${join.id}_condition_1`, leftField: join.leftField, rightField: join.rightField, operator: 'EQUALS' as const }]

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
  if (value.layer === 'DWD' && !value.groupingEnabled) return value.grainKeys
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

function datasetDetailFields(record: DatasetRecord): DatasetDetailField[] {
  if (!Array.isArray(record.dsl.fields)) return []
  return record.dsl.fields.flatMap((raw, index) => {
    if (!raw || typeof raw !== 'object') return []
    const field = raw as Record<string, unknown>
    const expression = field.expression && typeof field.expression === 'object'
      ? field.expression as Record<string, unknown>
      : {}
    const text = (key: string) => typeof field[key] === 'string' ? field[key] as string : ''
    return [{
      id: text('id') || `field-${index + 1}`,
      physicalName: typeof expression.field === 'string' ? expression.field : '',
      code: text('code'),
      name: text('name') || text('code') || `字段 ${index + 1}`,
      description: text('description'),
      role: text('role'),
      canonicalType: text('canonicalType'),
      semanticType: text('semanticType'),
      nullable: field.nullable === true,
      visible: field.visible !== false,
    }]
  })
}

function publishedVersionDiff(from: PublishedVersionRecord, current: DatasetRecord): DatasetVersionDiff {
  const keyOf = (value: Record<string, unknown>, index: number) => String(value.id || value.code || `item-${index + 1}`)
  const labelOf = (value: Record<string, unknown>, fallback: string) => String(value.name || value.code || fallback)
  const compareCollection = (before: Array<Record<string, unknown>> = [], after: Array<Record<string, unknown>> = []) => {
    const left = new Map(before.map((value, index) => [keyOf(value, index), value]))
    const right = new Map(after.map((value, index) => [keyOf(value, index), value]))
    return {
      added: [...right].filter(([key]) => !left.has(key)).map(([key, value]) => labelOf(value, key)),
      removed: [...left].filter(([key]) => !right.has(key)).map(([key, value]) => labelOf(value, key)),
      changed: [...left].filter(([key, value]) => right.has(key) && JSON.stringify(value) !== JSON.stringify(right.get(key))).map(([key, value]) => labelOf(value, key)),
    }
  }
  const fields = compareCollection(from.dsl.fields, current.dsl.fields)
  const nodes = compareCollection(from.dsl.nodes, current.dsl.nodes)
  const metadataChanges: string[] = []
  if (from.dsl.dataset.name !== current.name) metadataChanges.push('名称')
  if ((from.dsl.dataset.description || '') !== current.description) metadataChanges.push('说明')
  if (from.dsl.dataset.type !== current.type) metadataChanges.push('类型')
  if ((from.dsl.dataset.layer || '') !== current.layer) metadataChanges.push('数仓分层')
  return {
    addedFields: fields.added, removedFields: fields.removed, changedFields: fields.changed,
    addedNodes: nodes.added, removedNodes: nodes.removed, changedNodes: nodes.changed,
    metadataChanges,
    breakingChanges: fields.removed.length + fields.changed.length + nodes.removed.length,
  }
}

/** 提供数据集资产目录、筛选、新建配置和完整生命周期操作。 */
export function DatasetCenterPage() {
  const { datasetId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const pageParams = new URLSearchParams(location.search)
  const designSnapshot = import.meta.env.DEV && pageParams.has('snapshot')
  const qaViewport1920 = import.meta.env.DEV && pageParams.get('qa') === '1920'
  const modelingLogIDPrefix = useId()
  const selectedBusinessDomainID = useSyncExternalStore(
    subscribeDomainChange,
    currentDomainID,
    () => '',
  )
  const selectedBusinessDomain = currentDomain()
  const selectedBusinessDomainName = selectedBusinessDomain?.name.trim() || (designSnapshot ? '企业经营' : '')
  const [datasets, setDatasets] = useState<DatasetSummary[]>(designSnapshot ? snapshotDatasets : [])
  const [tables, setTables] = useState<AssetTable[]>(designSnapshot ? snapshotAssetTables : [])
  const [loading, setLoading] = useState(!designSnapshot)
  const [assetsLoading, setAssetsLoading] = useState(false)
  const [keyword, setKeyword] = useState('')
  const [statusFilter, setStatusFilter] = useState('ALL')
  const [layerFilter, setLayerFilter] = useState<DatasetLayer | 'ALL'>('ALL')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [dialog, setDialog] = useState<DialogState | null>(null)
  const [selectedDatasetIDs, setSelectedDatasetIDs] = useState<Set<string>>(new Set())
  const [batchAction, setBatchAction] = useState<DatasetBatchAction | null>(null)
  const [dagRuns, setDAGRuns] = useState<Record<string, DatasetDAGRun>>(designSnapshot ? {
    'snapshot-customer-dim': {
      id: 'snapshot-run-customer', datasetId: 'snapshot-customer-dim', datasetVersionId: 'snapshot-customer-v6', layer: 'DIM',
      mode: 'FULL', status: 'FAILED', attempt: 3, maxAttempts: 3, errorCode: 'WAREHOUSE_TIMEOUT', errorMessage: '目标仓库写入超时，已保留成功节点产物并回滚可用快照。',
      createdAt: '2026-08-11T07:00:00+08:00', updatedAt: '2026-08-11T07:34:00+08:00',
      startedAt: '2026-08-11T07:00:08+08:00', completedAt: '2026-08-11T07:34:00+08:00', slaDueAt: '2026-08-11T07:30:00+08:00', slaStatus: 'BREACHED', slaBreached: true, durationSeconds: 2032,
    },
    'snapshot-channel-summary': {
      id: 'snapshot-run-channel', datasetId: 'snapshot-channel-summary', datasetVersionId: 'snapshot-channel-v12', layer: 'DWS',
      mode: 'FULL', status: 'SUCCEEDED', attempt: 1, maxAttempts: 3,
      createdAt: '2026-08-11T06:00:00+08:00', updatedAt: '2026-08-11T06:04:00+08:00',
      startedAt: '2026-08-11T06:00:08+08:00', completedAt: '2026-08-11T06:04:00+08:00',
    },
  } : {})
  const [materializationDetail, setMaterializationDetail] = useState<DatasetDAGRunDetail | null>(null)
  const [lifecycleImpact, setLifecycleImpact] = useState<DatasetLifecycleImpact | null>(null)
  const [datasetManagePermissions, setDatasetManagePermissions] = useState<Record<string, boolean>>(
    designSnapshot ? Object.fromEntries(snapshotDatasets.map(item => [item.id, true])) : {},
  )
  const [draft, setDraft] = useState<DatasetDraft>(emptyDraft)
  const [relationBoxes, setRelationBoxes] = useState<RelationBox[]>([])
  const [groupBoxes, setGroupBoxes] = useState<GroupBox[]>([])
  const [transformBoxes, setTransformBoxes] = useState<TransformBox[]>([])
  const [endBox, setEndBox] = useState<EndBox | null>(null)
  const [nodePreviews, setNodePreviews] = useState<Record<string, NodePreviewState>>({})
  const [nodePositions, setNodePositions] = useState<Record<string, CanvasPoint>>({})
  const [metadata, setMetadata] = useState<DatasetMetadataForm>({ name: '', description: '', domain: '', subject: '' })
  const [detail, setDetail] = useState<DatasetRecord | null>(null)
  const [detailAsset, setDetailAsset] = useState<AssetTable | null>(null)
  const [detailAssetColumns, setDetailAssetColumns] = useState<AssetColumn[]>([])
  const [detailPreview, setDetailPreview] = useState<DatasetPreview | null>(null)
  const [detailPreviewError, setDetailPreviewError] = useState('')
  const [metadataEdit, setMetadataEdit] = useState<DatasetMetadataEditForm | null>(null)
  const [publicationRecord, setPublicationRecord] = useState<DatasetRecord | null>(null)
  const [publicationRequests, setPublicationRequests] = useState<DatasetPublicationRequest[]>([])
  const [publicationCapabilities, setPublicationCapabilities] = useState({ manage: false, publish: false })
  const [publicationNote, setPublicationNote] = useState('')
  const [publicationDecisionNote, setPublicationDecisionNote] = useState('')
  const [selectedPublicationRequestID, setSelectedPublicationRequestID] = useState('')
  const [historyRecord, setHistoryRecord] = useState<DatasetRecord | null>(null)
  const [historyItems, setHistoryItems] = useState<PublishedVersionSummary[]>([])
  const [selectedHistoryVersion, setSelectedHistoryVersion] = useState<PublishedVersionRecord | null>(null)
  const [historyUsage, setHistoryUsage] = useState<VersionUsage | null>(null)
  const [historyPreview, setHistoryPreview] = useState<VersionPreviewState | null>(null)
  const [historyConfirm, setHistoryConfirm] = useState(false)
  const [editingRecord, setEditingRecord] = useState<DatasetRecord | null>(null)
  const [draftConflict, setDraftConflict] = useState<DraftConflict | null>(null)
  const [formError, setFormError] = useState('')
  const [busyAction, setBusyAction] = useState('')
  const [modelingMonitors, setModelingMonitors] = useState(() => {
    const state = emptyModelingMonitors()
    if (designSnapshot) {
      state.DIM_MODELING.ready = true
      state.DWD_MODELING.ready = true
      state.DWS_MODELING.ready = true
    }
    return state
  })
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
  const [aiProgressLogs, setAIProgressLogs] = useState<DatasetAIProgressEvent[]>([])
  const [aiError, setAIError] = useState<DatasetAIErrorView | null>(null)
  const [aiBusy, setAIBusy] = useState(false)
  const [aiApplying, setAIApplying] = useState(false)
  const [aiApplied, setAIApplied] = useState(false)
  const [aiDetailsExpanded, setAIDetailsExpanded] = useState(true)
  const [aiUndo, setAIUndo] = useState<DatasetAIUndo | null>(null)
  const [aiReviewLabels, setAIReviewLabels] = useState<DatasetAIReviewLabels>({ nodes: {}, fields: {} })
  const [aiRetryAction, setAIRetryAction] = useState<DatasetAIRetryAction>(null)
  const [aiLastInstruction, setAILastInstruction] = useState('')
  const canvasFullscreenTarget = useRef<HTMLElement | null>(null)
  const historySelectionRequest = useRef(0)
  const endPreviewRequest = useRef(0)
  const componentPreviewRequests = useRef<Record<string, number>>({})
  const openedRouteDatasetID = useRef('')
  const aiRequest = useRef(0)
  const aiApplyRequest = useRef(0)
  const editorFingerprintRef = useRef('')
  const lastEditorFingerprintRef = useRef('')
  const selectFilteredCheckbox = useRef<HTMLInputElement | null>(null)
  const modelingRunTaskIDs = useRef<Record<DatasetLLMTrigger, Set<string>>>({
    DIM_MODELING: new Set(),
    DWD_MODELING: new Set(),
    DWS_MODELING: new Set(),
  })
  const modelingRequestedAt = useRef<Record<DatasetLLMTrigger, number | null>>({
    DIM_MODELING: null,
    DWD_MODELING: null,
    DWS_MODELING: null,
  })
  const modelingExpectedRef = useRef<Record<DatasetLLMTrigger, boolean>>({
    DIM_MODELING: false,
    DWD_MODELING: false,
    DWS_MODELING: false,
  })
  const modelingSyncRequest = useRef(0)
  const dagSyncRequest = useRef(0)

  const loadDatasets = useCallback(async () => {
    if (designSnapshot) {
      setDatasets(current => current.length ? current : snapshotDatasets)
      return
    }
    const next = await loadAllDatasets()
    setDatasets(next)
    const permissions = await Promise.all(next.map(async dataset => {
      try { return [dataset.id, (await datasetAPI.evaluatePermission(dataset.id, 'MANAGE')).allowed] as const }
      catch { return [dataset.id, false] as const }
    }))
    setDatasetManagePermissions(Object.fromEntries(permissions))
  }, [designSnapshot])

  const refreshDAGRuns = useCallback(async () => {
    const request = ++dagSyncRequest.current
    const targets = datasets.filter(dataset => dataset.currentPublishedVersionId)
    const entries = await Promise.all(targets.map(async dataset => {
      try {
        const page = await datasetAPI.listDAGRuns(dataset.id, 20, 0)
        const latest = page.items[0]
        const active = page.items.find(run => activeDAGRunStatuses.has(run.status))
        // A newly completed run clears an older incident; otherwise catalog
        // cards would keep showing a historical failure after a successful
        // retry. Historical receipts remain available from the run history.
        const actionable = latest && (latest.status === 'FAILED' || latest.slaBreached)
          ? latest
          : undefined
        return [dataset.id, active ?? actionable ?? latest] as const
      } catch {
        return [dataset.id, undefined] as const
      }
    }))
    if (request !== dagSyncRequest.current) return
    setDAGRuns(Object.fromEntries(entries.filter((entry): entry is readonly [string, DatasetDAGRun] => Boolean(entry[1]))))
  }, [datasets])

  const expectModelingTasks = useCallback((
    trigger: DatasetLLMTrigger,
    expected: boolean,
  ) => {
    modelingExpectedRef.current[trigger] = expected
    setModelingMonitors(current => ({
      ...current,
      [trigger]: { ...current[trigger], expected },
    }))
  }, [])

  const refreshModelingTasks = useCallback(async () => {
    const request = ++modelingSyncRequest.current
    try {
      const page = await backgroundTaskAPI.list('ALL', 200)
      if (request !== modelingSyncRequest.current) return
      const updates = {} as Record<DatasetLLMTrigger, Pick<ModelingMonitorState, 'tasks' | 'ready' | 'expected' | 'syncError'>>
      const completionNotices: Notice[] = []
      let refreshCatalog = false
      for (const config of modelingMonitorConfigs) {
        const { trigger } = config
        const relevant = page.items.filter(task => config.taskKinds.has(task.kind))
        const active = relevant.filter(isActiveModelingTask)
        const requestedAt = modelingRequestedAt.current[trigger]
        const runTaskIDs = modelingRunTaskIDs.current[trigger]
        if (!runTaskIDs.size) {
          const requestedTasks = requestedAt === null
            ? []
            : relevant.filter(task => new Date(task.createdAt).getTime() >= requestedAt - 5_000)
          const activeBatchTasks = active.length
            ? relevant.filter(task => active.some(activeTask =>
                Math.abs(new Date(task.createdAt).getTime() - new Date(activeTask.createdAt).getTime()) <= 5_000
              ))
            : []
          const candidates = requestedTasks.length ? requestedTasks : activeBatchTasks
          modelingRunTaskIDs.current[trigger] = new Set(candidates.map(task => task.id))
        } else if (requestedAt !== null) {
          relevant
            .filter(task => new Date(task.createdAt).getTime() >= requestedAt - 5_000)
            .forEach(task => runTaskIDs.add(task.id))
        } else if (modelingExpectedRef.current[trigger]) {
          active.forEach(task => runTaskIDs.add(task.id))
        }
        const selected = relevant
          .filter(task => modelingRunTaskIDs.current[trigger].has(task.id))
          .sort((left, right) => new Date(left.createdAt).getTime() - new Date(right.createdAt).getTime())
        const selectedActive = selected.filter(isActiveModelingTask)
        let expected = modelingExpectedRef.current[trigger]
        if (selectedActive.length) {
          expected = true
          modelingExpectedRef.current[trigger] = true
        } else if (selected.length && expected) {
          expected = false
          modelingExpectedRef.current[trigger] = false
          modelingRequestedAt.current[trigger] = null
          refreshCatalog = true
          const failed = selected.filter(task => ['FAILED', 'CANCELLED', 'STALE'].includes(task.status))
          const partial = selected.filter(task => task.status === 'PARTIAL')
          const skipped = selected.filter(task => task.status === 'SKIPPED')
          if (failed.length) {
            completionNotices.push({
              tone: 'error',
              message: `${config.label}已结束：${failed.length} 个任务失败或中止。悬停“${config.label}”可查看运行日志。`,
            })
          } else if (partial.length) {
            completionNotices.push({
              tone: 'error',
              message: `${config.label}部分完成：${partial.length} 个任务需要处理。悬停“${config.label}”可查看运行日志。`,
            })
          } else if (skipped.length) {
            completionNotices.push({
              tone: 'error',
              message: `${config.label}未生成结果：${skipped.length} 个任务已跳过。悬停“${config.label}”可查看具体原因后重试。`,
            })
          } else {
            completionNotices.push({
              tone: 'success',
              message: `${config.label}已完成，数据集目录已刷新。`,
            })
          }
        }
        updates[trigger] = {
          tasks: selected,
          ready: true,
          expected,
          syncError: '',
        }
      }
      setModelingMonitors(current => {
        const next = { ...current }
        for (const config of modelingMonitorConfigs) {
          next[config.trigger] = {
            ...current[config.trigger],
            ...updates[config.trigger],
          }
        }
        return next
      })
      if (completionNotices.length) {
        setNotice(completionNotices[completionNotices.length - 1])
      }
      if (refreshCatalog) {
        void loadDatasets()
      }
    } catch (cause) {
      if (request !== modelingSyncRequest.current) return
      const message = cause instanceof Error ? cause.message : '读取建模任务失败'
      setModelingMonitors(current => ({
        DIM_MODELING: { ...current.DIM_MODELING, syncError: message },
        DWD_MODELING: { ...current.DWD_MODELING, syncError: message },
        DWS_MODELING: { ...current.DWS_MODELING, syncError: message },
      }))
    }
  }, [loadDatasets])

  useEffect(() => {
    if (designSnapshot) {
      setDatasets(snapshotDatasets)
      setLoading(false)
      return
    }
    let active = true
    setLoading(true)
    loadDatasets().catch(cause => {
      if (active) setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载数据集失败' })
    }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [designSnapshot, loadDatasets, selectedBusinessDomainID])

  useEffect(() => {
    if (designSnapshot) return
    void refreshModelingTasks()
    const timer = window.setInterval(() => void refreshModelingTasks(), 3_000)
    return () => window.clearInterval(timer)
  }, [designSnapshot, refreshModelingTasks])

  useEffect(() => {
    if (designSnapshot) return
    void refreshDAGRuns()
    const timer = window.setInterval(() => void refreshDAGRuns(), 3_000)
    return () => window.clearInterval(timer)
  }, [designSnapshot, refreshDAGRuns])

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
    const publishedMappedOrigins = new Set(datasets
      .filter(dataset => dataset.id !== editingRecord?.id && dataset.currentPublishedVersionId && dataset.originTableId)
      .map(dataset => dataset.originTableId!))
    return designerAssetLayers.map(layer => {
      const label = designerLayerLabels[layer]
      const layerTables = tables.filter(table => {
        if (table.sourceKind === 'DATASET') return table.datasetLayer === layer
        return layer === 'ODS' && !publishedMappedOrigins.has(table.id)
      }).sort((left, right) => (left.businessName || left.tableName).localeCompare(
        right.businessName || right.tableName, 'zh-CN',
      ))
      const physicalSourceGroups = new Map<string, {
        id: string; name: string; type: string; tables: AssetTable[]
      }>()
      if (layer === 'ODS') {
        for (const table of layerTables.filter(item => item.sourceKind !== 'DATASET')) {
          const group = physicalSourceGroups.get(table.dataSourceId) ?? {
            id: table.dataSourceId, name: table.dataSourceName,
            type: table.dataSourceType, tables: [],
          }
          group.tables.push(table)
          physicalSourceGroups.set(table.dataSourceId, group)
        }
      }
      return {
        id: `dataset-layer:${layer}`,
        layer,
        name: label.name,
        type: label.description,
        tables: layerTables,
        datasetTables: layerTables.filter(table => table.sourceKind === 'DATASET'),
        physicalSourceGroups: [...physicalSourceGroups.values()],
      }
    })
  }, [datasets, editingRecord?.id, tables])

  const filtered = useMemo(() => {
    const query = keyword.trim().toLocaleLowerCase()
    return datasets.filter(dataset => (!query || dataset.name.toLocaleLowerCase().includes(query) || dataset.code.toLocaleLowerCase().includes(query)) &&
      (statusFilter === 'ALL' || dataset.status === statusFilter) &&
      (layerFilter === 'ALL' || dataset.layer === layerFilter))
  }, [datasets, keyword, layerFilter, statusFilter])
  const layerCounts = useMemo(
    () => Object.fromEntries(layerOverview.map(item => [
      item.layer,
      datasets.reduce((total, dataset) => total + Number(dataset.layer === item.layer), 0),
    ])) as Record<DatasetLayer, number>,
    [datasets],
  )
  const selectedDatasets = useMemo(
    () => datasets.filter(dataset => selectedDatasetIDs.has(dataset.id)),
    [datasets, selectedDatasetIDs],
  )
  const selectedActiveDAGCount = useMemo(
    () => selectedDatasets.reduce((total, dataset) => total + Number(Boolean(
      dagRuns[dataset.id] && activeDAGRunStatuses.has(dagRuns[dataset.id].status),
    )), 0),
    [dagRuns, selectedDatasets],
  )
  const selectedRunnableCount = useMemo(
    () => selectedDatasets.reduce((total, dataset) => total + Number(Boolean(
      dataset.status === 'PUBLISHED' && dataset.currentPublishedVersionId &&
      datasetManagePermissions[dataset.id] &&
      (!dagRuns[dataset.id] || !activeDAGRunStatuses.has(dagRuns[dataset.id].status)),
    )), 0),
    [dagRuns, datasetManagePermissions, selectedDatasets],
  )
  const filteredSelectedCount = useMemo(
    () => filtered.reduce((total, dataset) => total + Number(selectedDatasetIDs.has(dataset.id)), 0),
    [filtered, selectedDatasetIDs],
  )
  const allFilteredSelected = filtered.length > 0 && filteredSelectedCount === filtered.length

  useEffect(() => {
    setSelectedDatasetIDs(current => {
      const available = new Set(datasets.map(dataset => dataset.id))
      const next = new Set([...current].filter(id => available.has(id)))
      return next.size === current.size ? current : next
    })
  }, [datasets])

  useEffect(() => {
    if (selectFilteredCheckbox.current) {
      selectFilteredCheckbox.current.indeterminate = filteredSelectedCount > 0 && !allFilteredSelected
    }
  }, [allFilteredSelected, filteredSelectedCount])

  const selectedPublicationRequest = publicationRequests.find(item => item.id === selectedPublicationRequestID) ?? null
  const currentDraftPublicationRequest = publicationRecord
    ? publicationRequests.find(item => item.draftVersionId === publicationRecord.draftVersionId &&
      item.expectedDraftRecordVersion === publicationRecord.draftRecordVersion) ?? null
    : null
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
    domain: selectedBusinessDomainName,
    subject: metadata.subject.trim(),
    grainKeys: configuredGrainKeys(draft, endBox),
    designer: serializeDesignerGraph(currentDesignerGraph),
    preAggregation: undefined,
    finalOutputKeys: undefined,
  }), [currentDesignerGraph, draft, endBox, generatedCode, metadata, selectedBusinessDomainName])
  const layerChoices = useMemo(() => {
    if (designSnapshot) return ['DWD'] as DatasetLayer[]
    try {
      return draft.nodes.length ? datasetLayerChoices(draft) : ['DWD'] as DatasetLayer[]
    } catch {
      return ['DWD'] as DatasetLayer[]
    }
  }, [designSnapshot, draft])
  const classificationSuggestions = useMemo(() => {
    const tags = [
      ...(editingRecord?.tags ?? []),
      ...draft.nodes.flatMap(node => node.table.tags ?? []),
    ]
    const values = (prefix: string) => [...new Set(tags.flatMap(tag => {
      const normalized = tag.trim()
      if (!normalized.startsWith(prefix)) return []
      const value = normalized.slice(prefix.length).trim()
      return value ? [value] : []
    }))].sort((left, right) => left.localeCompare(right, 'zh-CN'))
    return { subjects: values('主题:') }
  }, [draft.nodes, editingRecord?.tags])

  const resetDatasetAI = useCallback(() => {
    aiRequest.current += 1
    aiApplyRequest.current += 1
    setAIPrompt('')
    setAIResult(null)
    setAIProgressLogs([])
    setAIError(null)
    setAIBusy(false)
    setAIApplying(false)
    setAIApplied(false)
    setAIDetailsExpanded(true)
    setAIUndo(null)
    setAIReviewLabels({ nodes: {}, fields: {} })
    setAIRetryAction(null)
    setAILastInstruction('')
  }, [])

  const openCreate = async () => {
    resetDatasetAI()
    endPreviewRequest.current += 1
    if (designSnapshot) {
      const snapshot = snapshotEditorState()
      setEditingRecord(null)
      setTables(snapshotAssetTables)
      setDraft(snapshot.draft)
      setRelationBoxes(snapshot.relationBoxes)
      setGroupBoxes(snapshot.groupBoxes)
      setTransformBoxes(snapshot.transformBoxes)
      setEndBox(snapshot.endBox)
      setNodePositions(snapshot.nodePositions)
      setMetadata(snapshot.metadata)
      setGeneratedCode(`dwd_sales_order_${Date.now().toString(36).slice(-4)}`)
      setActiveNodeID('')
      setActiveJoinID('')
      setActiveGroupID('')
      setActiveTransformID('')
      setActiveEnd(false)
      setCanvasNotice('已根据上游资产预置订单与客户关联，可继续拖入商品表或调整字段。')
      setFormError('')
      setAssetsLoading(false)
      setDialog({ mode: 'create' })
      return
    }
    setEditingRecord(null)
    setDraft(emptyDraft())
    setRelationBoxes([])
    setGroupBoxes([])
    setTransformBoxes([])
    setEndBox(null)
    setNodePreviews({})
    setNodePositions({})
    setMetadata({ name: '', description: '', domain: selectedBusinessDomainName, subject: '' })
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
    setAssetsLoading(true)
    try {
      const items = await loadDesignerAssets(datasets)
      setTables(items)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '加载资产模板失败')
    } finally {
      setAssetsLoading(false)
    }
  }

  const openEdit = useCallback(async (dataset: DatasetSummary | string) => {
    setDraftConflict(null)
    resetDatasetAI()
    endPreviewRequest.current += 1
    const id = typeof dataset === 'string' ? dataset : dataset.id
    if (designSnapshot) {
      const summary = typeof dataset === 'string'
        ? snapshotDatasets.find(item => item.id === dataset) ?? snapshotDatasets[0]
        : dataset
      const snapshot = snapshotEditorState()
      setTables(snapshotAssetTables)
      setDraft({ ...snapshot.draft, code: summary.code, name: summary.name, description: summary.description, layer: summary.layer })
      setRelationBoxes(snapshot.relationBoxes)
      setGroupBoxes(snapshot.groupBoxes)
      setTransformBoxes(snapshot.transformBoxes)
      setEndBox({ ...snapshot.endBox, name: summary.name })
      setNodePositions(snapshot.nodePositions)
      setMetadata({ ...snapshot.metadata, name: summary.name, description: summary.description })
      setGeneratedCode(summary.code)
      setEditingRecord(snapshotDatasetRecord(summary))
      setActiveNodeID('')
      setActiveJoinID('')
      setActiveGroupID('')
      setActiveTransformID('')
      setActiveEnd(false)
      setCanvasNotice('草稿 V3 · 已自动保存于 09:32')
      setFormError('')
      setAssetsLoading(false)
      setBusyAction('')
      setDialog({ mode: 'create', dataset: summary })
      return
    }
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
      const [record, availableDatasets] = await Promise.all([
        datasetAPI.get(id),
        datasets.length ? Promise.resolve(datasets) : loadAllDatasets(),
      ])
      // 编辑器与新建入口必须加载同一份分层资产目录。仅加载物理表会与
      // sourceGroups 的“已有 ODS 隐藏物理表”规则组合成空列表；同时排除当前
      // 数据集自身，避免用户把其发布版本拖回画布形成自引用。
      const availableTables = await loadDesignerAssets(availableDatasets, id)
      const hydrated = await hydrateDatasetDraft(record, availableTables, availableDatasets)
      const graph = (hydrated as DatasetDraft & { designer?: DesignerGraphV1 }).designer ?? hydrateDesignerGraph(record.dsl, hydrated.nodes, hydrated.joins, hydrated.fields)
      const loadedMetadata: DatasetMetadataForm = {
        name: record.name,
        description: record.description,
        domain: selectedBusinessDomainName,
        subject: record.dsl.dataset.subject ?? '',
      }
      setTables(availableTables)
      setDraft(hydrated)
      setRelationBoxes(graph.joins)
      setGroupBoxes(graph.groups)
      setTransformBoxes(graph.transforms ?? [])
      setEndBox(graph.end ?? null)
      setNodePositions(graph.nodePositions)
      setMetadata(loadedMetadata)
      setGeneratedCode(record.code)
      setEditingRecord(record)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '加载数据集配置失败')
    } finally {
      setAssetsLoading(false)
      setBusyAction('')
    }
  }, [datasets, designSnapshot, resetDatasetAI, selectedBusinessDomainName])

  const loadNodePreview = useCallback(async (node: DesignerNode) => {
    setNodePreviews(current => ({ ...current, [node.id]: { loading: true } }))
    try {
      const data = node.table.sourceKind === 'DATASET' && node.table.datasetId && node.table.datasetVersionId
        ? await datasetAPI.previewVersion(node.table.datasetId, node.table.datasetVersionId, crypto.randomUUID(), {}, 5)
        : await datasetAPI.tablePreview(node.table.id, 10)
      setNodePreviews(current => ({ ...current, [node.id]: { loading: false, data: withDesignerNodePreviewMetadata(node, data) } }))
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
      queueMicrotask(() => void openCreate())
      return
    }
    queueMicrotask(() => void openEdit(datasetId))
    // 路由参数是唯一触发源；打开动作内部会更新表资产状态，不能反向重复触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [datasetId, location.key, openEdit])

  const selectTable = async (table: AssetTable, position?: CanvasPoint) => {
    const nextNumber = draft.nodes.reduce((largest, node) => Math.max(largest, Number(node.id.replace('node_', '')) || 0), 0) + 1
    const nodeID = `node_${nextNumber}`
    setBusyAction(`asset:${table.id}`)
    setFormError('')
    try {
      // 数据集只允许引用当前有效字段；资产接口中的失效字段只用于历史审计。
      const columns = designSnapshot && snapshotAssetColumns[table.id]
        ? snapshotAssetColumns[table.id]
        : table.sourceKind === 'DATASET' && table.datasetId && table.datasetVersionId
        ? datasetVersionColumns(table, await datasetAPI.getVersion(table.datasetId, table.datasetVersionId))
        : (await datasetAPI.columns(table.id)).items.filter(column => !column.assetStatus || column.assetStatus === 'ACTIVE')
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
    setTransformBoxes(transforms => transforms.map(transform => relationLeaves(transform.input, relationBoxes, groupBoxes, transforms).includes(nodeID) ? { ...transform, input: undefined, rules: [], conditions: [] } : transform))
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
    ...current,
    joins: current.joins.map(join => {
      if (join.id !== joinID) return join
      const joinType = patch.joinType || join.joinType
      return {
        ...join,
        ...patch,
        joinType,
        cardinality: joinCardinalityForType(joinType),
        relationshipType: undefined,
        relationshipRole: undefined,
        fanoutPolicy: undefined,
        bridge: undefined,
        temporal: undefined,
      }
    }),
  }))

  const addRelationBox = (position?: CanvasPoint, input?: RelationInput) => {
    const largest = relationBoxes.reduce((value, box) => Math.max(value, Number(box.id.replace('join_', '')) || 0), draft.joins.length)
    const id = `join_${largest + 1}`
    setRelationBoxes(current => [...current, { id, name: `关联结果 ${largest + 1}`, position: position ?? { x: 510 + current.length * 250, y: 150 + (current.length % 2) * 155 }, ...(input ? { left: input } : {}), outputKeys: [] }])
    setActiveNodeID('')
    setActiveGroupID('')
    setActiveTransformID('')
    setActiveEnd(false)
    setActiveJoinID(id)
    setCanvasNotice(input ? '关联组件已插入连线，槽位 1 已连接，请继续连接槽位 2' : '关联组件已加入画布，请配置槽位 1 和槽位 2')
    return id
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
    setTransformBoxes(current => current.map(transform => transform.input?.kind === 'JOIN' && transform.input.id === boxID ? { ...transform, input: undefined, rules: [], conditions: [] } : transform))
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
    : [{ id: `${join.id}_condition_1`, leftField: join.leftField, rightField: join.rightField, operator: 'EQUALS' as const }]

  const updateJoinCondition = (joinID: string, conditionID: string, patch: { leftField?: string; rightField?: string; operator?: 'EQUALS' | 'NOT_EQUALS' | 'GT' | 'GTE' | 'LT' | 'LTE' }) => setDraft(current => ({
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
      return { ...join, conditions: [...conditions, { id: `${join.id}_condition_${Date.now().toString(36)}`, leftField: left ? availableNodeColumns(left, current.fields).find(column => leftAllowed.has(`${left.id}.${column.columnName}`))?.columnName ?? '' : '', rightField: right ? availableNodeColumns(right, current.fields).find(column => rightAllowed.has(`${right.id}.${column.columnName}`))?.columnName ?? '' : '', operator: 'EQUALS' as const }], manualConfirmed: false }
    }),
  }))

  const addGroupBox = (position?: CanvasPoint, input?: RelationInput) => {
    const nextNumber = groupBoxes.reduce((largest, group) => Math.max(largest, Number(group.id.replace('group_', '')) || 0), 0) + 1
    const id = `group_${nextNumber}`
    const group: GroupBox = { id, name: `分组结果 ${nextNumber}`, position: position ?? { x: 420 + (nextNumber - 1) * 80, y: 165 + (nextNumber - 1) * 145 }, ...(input ? { input } : {}), dimensions: [], metrics: [] }
    setGroupBoxes(current => [...current, group])
    setActiveNodeID('')
    setActiveJoinID('')
    setActiveTransformID('')
    setActiveGroupID(id)
    setActiveEnd(false)
    setCanvasNotice(input ? '分组组件已插入连线，请继续配置维度和指标' : '分组组件已加入画布，请从上游组件手动连线')
    return id
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
      ...(group.groupingSets ? { groupingSets: group.groupingSets.map(groupingSet => groupingSet.filter(key => available.has(key))) } : {}),
      metrics: group.metrics.filter(field => field.key === '*' || field.countRows || available.has(field.key)),
    } : group))
    setDraft(current => ({ ...current, joins: current.joins.map(join => relationBoxes.some(box => box.id === join.id && (box.left?.id === groupID || box.right?.id === groupID)) ? { ...join, manualConfirmed: false } : join) }))
    setFormError('')
  }

  const updateGroupName = (groupID: string, name: string) => {
    setGroupBoxes(current => current.map(group => group.id === groupID ? { ...group, name } : group))
    setFormError('')
  }

  const updateGroupByMode = (groupID: string, groupByMode: GraphGroupByMode) => {
    setGroupBoxes(current => current.map(group => group.id === groupID ? {
      ...group,
      ...(groupByMode === 'STANDARD' ? { groupByMode: undefined } : { groupByMode }),
      ...(groupByMode === 'GROUPING_SETS'
        ? { groupingSets: group.groupingSets?.length ? group.groupingSets : [group.dimensions.map(dimension => dimension.key)] }
        : { groupingSets: undefined }),
    } : group))
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

  const updateGroupDimensions = (groupID: string, fields: ProducedField[]) => commitGroupFields(groupID, group => {
    const seen = new Set<string>()
    const orderedFields = fields.filter(field => {
      if (seen.has(field.key)) return false
      seen.add(field.key)
      return true
    })
    const previous = new Map(group.dimensions.map(dimension => [dimension.key, dimension]))
    const dimensions = orderedFields.map(field => {
      const existing = previous.get(field.key)
      const generated = generatedGraphFieldIdentity(field)
      return {
        key: field.key,
        ...(existing?.outputKey ? { outputKey: existing.outputKey } : {}),
        name: existing?.name || generated.name,
        code: existing?.code || generated.code,
      }
    })
    const dimensionKeys = new Set(dimensions.map(dimension => dimension.key))
    const previousGroupingSets = group.groupingSets?.length ? group.groupingSets : [[]]
    const groupingSets = group.groupByMode === 'GROUPING_SETS'
      ? !dimensions.length
        ? [[]]
        : !group.dimensions.length && previousGroupingSets.length === 1 && !previousGroupingSets[0].length
        ? [dimensions.map(dimension => dimension.key)]
        : previousGroupingSets.map(groupingSet => groupingSet.filter(key => dimensionKeys.has(key)))
      : undefined
    return {
      ...group,
      dimensions,
      groupingSets,
      metrics: group.metrics.map(metric => {
        const source = orderedFields.find(field => field.key === metric.key)
        if (!source) return metric
        const generated = generatedGraphFieldIdentity(source)
        return {
          ...metric,
          outputKey: metric.outputKey || groupFieldOutputKey(group.id, 'metric', source),
          name: metric.name === generated.name ? `${generated.name}指标` : metric.name,
          code: safeIdentifier(metric.code) === safeIdentifier(generated.code) ? safeIdentifier(`${generated.code}_metric`) : metric.code,
        }
      }),
    }
  })

  const updateGroupingSets = (groupID: string, groupingSets: string[][]) => commitGroupFields(groupID, group => ({
    ...group,
    groupingSets: groupingSets.map(groupingSet => [...groupingSet]),
  }))

  const updateGroupMetrics = (groupID: string, fields: ProducedField[], selections: GroupMetricSelection[]) => commitGroupFields(groupID, group => {
    const fieldsByKey = new Map(fields.map(field => [field.key, field]))
    const previous = new Map(group.metrics.map(metric => [metric.key, metric]))
    const seen = new Set<string>()
    const metrics = selections.flatMap(selection => {
      if (seen.has(selection.key)) return []
      seen.add(selection.key)
      if (selection.key === '*') {
        const existing = group.metrics.find(metric => metric.key === '*' || metric.countRows)
        return [{
          key: '*',
          outputKey: existing?.outputKey || `${group.id}.metric_row_count`,
          name: existing?.name || '总行数',
          code: existing?.code || 'row_count',
          aggregation: 'COUNT',
          countRows: true,
        }]
      }
      const field = fieldsByKey.get(selection.key)
      if (!field) return []
      if (!groupMetricAggregationOptions(field).some(option => option.value === selection.aggregation)) return []
      const existing = previous.get(field.key)
      const generated = generatedGraphFieldIdentity(field)
      const dimension = group.dimensions.find(item => item.key === field.key)
      return [{
        key: field.key,
        ...(existing?.outputKey || dimension ? { outputKey: existing?.outputKey || groupFieldOutputKey(group.id, 'metric', field) } : {}),
        name: existing?.name || (dimension ? `${generated.name}指标` : generated.name),
        code: existing?.code || (dimension ? safeIdentifier(`${generated.code}_metric`) : generated.code),
        aggregation: selection.aggregation,
      }]
    })
    return { ...group, metrics }
  })

  const removeGroupBox = (groupID: string) => {
    const consumers = new Set(relationBoxes.filter(box => box.left?.id === groupID || box.right?.id === groupID).map(box => box.id))
    setRelationBoxes(current => current.map(box => ({
      ...box,
      left: box.left?.kind === 'GROUP' && box.left.id === groupID ? undefined : box.left,
      right: box.right?.kind === 'GROUP' && box.right.id === groupID ? undefined : box.right,
    })))
    setGroupBoxes(current => current.filter(group => group.id !== groupID))
    setTransformBoxes(current => current.map(transform => transform.input?.kind === 'GROUP' && transform.input.id === groupID ? { ...transform, input: undefined, rules: [], conditions: [] } : transform))
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

  const addTransformBox = (componentType: GraphTransformComponentType, position?: CanvasPoint, input?: RelationInput) => {
    const definition = transformComponentDefinition(componentType)
    if (!definition) return
    const nextNumber = transformBoxes.reduce((largest, transform) => Math.max(largest, Number(transform.id.replace('transform_', '')) || 0), 0) + 1
    const id = `transform_${nextNumber}`
    const availableFields = relationOutputFields(input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
    const filterFields = availableFields.filter(field => field.kind !== 'METRIC' && !field.aggregation)
    let transform: TransformBox = {
      id, family: definition.family, componentType, name: `${definition.label} ${nextNumber}`,
      position: position ?? { x: 620 + (nextNumber - 1) * 85, y: 175 + (nextNumber - 1) * 125 }, ...(input ? { input } : {}), rules: [],
      ...(componentType === 'FILTER' ? {
        conditions: input && filterFields.length
          ? [{ id: `${id}_condition_1`, inputKey: filterFields[0].key, operator: 'EQUALS' as const, value: '' }]
          : [],
      } : {}),
    }
    if (componentType !== 'FILTER' && input && availableFields.length) transform = { ...transform, rules: [defaultTransformRule(transform, availableFields, 1)] }
    const next = [...transformBoxes, transform]
    setTransformBoxes(next)
    setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveTransformID(id); setActiveEnd(false)
    setFormError('')
    setCanvasNotice(input ? `${definition.label}已插入连线，请继续完善配置` : `${definition.label}已加入画布，请从上游组件手动连线`)
    return id
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
      if (transformIsFilter(transform)) {
        const filterFields = availableFields.filter(field => field.kind !== 'METRIC' && !field.aggregation)
        const retained = (transform.conditions ?? [])
          .filter(condition => available.has(condition.inputKey))
          .map(condition => condition.valueMode === 'FIELD' && !available.has(condition.value) ? { ...condition, value: '' } : condition)
        return {
          ...transform,
          input,
          rules: [],
          conditions: retained.length
            ? retained
            : input && filterFields.length
              ? [{ id: `${transform.id}_condition_1`, inputKey: filterFields[0].key, operator: 'EQUALS' as const, value: '' }]
              : [],
        }
      }
      const retained = transform.rules.flatMap(rule => {
        if (rule.operation === 'WINDOW') {
          const partitionByKeys = (rule.partitionByKeys ?? []).filter(key => available.has(key))
          const orderBy = (rule.orderBy ?? []).filter(item => available.has(item.key))
          const windowValueKey = rule.windowValueKey && available.has(rule.windowValueKey) ? rule.windowValueKey : undefined
          return partitionByKeys.length || orderBy.length ? [{ ...rule, partitionByKeys, orderBy, windowValueKey }] : []
        }
        const inputKeys = rule.inputKeys.filter(key => available.has(key))
        const needed = transformRuleInputCount(
          rule.operation,
          rule.fallbackMode,
          rule.dateSource,
          rule.startDateSource,
          rule.endDateSource,
        )
        return inputKeys.length || needed === 0 ? [{ ...rule, inputKeys: inputKeys.slice(0, needed) }] : []
      })
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
        const needed = transformRuleInputCount(
          updated.operation,
          updated.fallbackMode,
          updated.dateSource,
          updated.startDateSource,
          updated.endDateSource,
        )
        const inputKeys = [...updated.inputKeys]
        while (inputKeys.length < needed) inputKeys.push(inputKeys[0] || '')
        const targetType = updated.targetType || 'STRING'
        const output = {
          ...updated.output,
          canonicalType: updated.operation === 'WINDOW' ? updated.output.canonicalType || 'INTEGER' : updated.operation === 'CAST' ? targetType : updated.operation === 'DATE_FORMAT' ? 'STRING' : updated.operation === 'DATE_TRUNC' ? updated.output.canonicalType || 'DATE' : updated.operation === 'CASE' ? updated.thenMode === 'CURRENT_DATE' || updated.elseMode === 'CURRENT_DATE' ? 'DATE' : 'STRING' : updated.operation === 'CONCAT' || updated.operation === 'SUBSTRING' || updated.operation === 'TRIM' || updated.operation === 'UPPER' || updated.operation === 'LOWER' || updated.operation === 'REPLACE' ? 'STRING' : ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'ROUND', 'ABS', 'FLOOR', 'CEIL'].includes(updated.operation) ? 'DECIMAL' : updated.output.canonicalType,
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

  const updateFilterCondition = (transformID: string, conditionID: string, patch: Partial<GraphFilterCondition>) => {
    refreshTransformOutputs(transformBoxes.map(transform => transform.id === transformID ? {
      ...transform,
      conditions: (transform.conditions ?? []).map(condition => condition.id === conditionID ? { ...condition, ...patch } : condition),
    } : transform))
    setFormError('')
  }

  const addFilterCondition = (transformID: string) => {
    const transform = transformBoxes.find(item => item.id === transformID)
    if (!transform) return
    const available = relationOutputFields(transform.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
      .filter(field => field.kind !== 'METRIC' && !field.aggregation)
    if (!available.length) { setFormError('请先为过滤组件连接一个包含明细字段的输入组件'); return }
    const index = (transform.conditions?.length ?? 0) + 1
    refreshTransformOutputs(transformBoxes.map(item => item.id === transformID ? {
      ...item,
      conditions: [...(item.conditions ?? []), { id: `${item.id}_condition_${index}`, inputKey: available[0].key, operator: 'EQUALS', value: '' }],
    } : item))
    setFormError('')
  }

  const removeFilterCondition = (transformID: string, conditionID: string) => {
    refreshTransformOutputs(transformBoxes.map(transform => transform.id === transformID ? {
      ...transform,
      conditions: (transform.conditions ?? []).filter(condition => condition.id !== conditionID),
    } : transform))
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
    const next = transformBoxes.filter(transform => transform.id !== transformID).map(transform => transform.input?.kind === 'TRANSFORM' && transform.input.id === transformID ? { ...transform, input: undefined, rules: [], conditions: [] } : transform)
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
        ? await datasetAPI.previewDraft(editingRecord.id, editingRecord.version, candidateDSL, queryID, {}, 10)
        : await datasetAPI.previewCandidate(candidateDSL, queryID, {}, 10)
      if (componentPreviewRequests.current[key] !== request || editorFingerprintRef.current !== previewFingerprint) return
      setComponentPreviews(current => ({
        ...current,
        [key]: {
          loading: false,
          data: { columns: data.columns, columnMetadata: data.columnMetadata, rows: data.rows.slice(0, 5) },
        },
      }))
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
        ? await datasetAPI.previewDraft(record.id, record.version, candidateDSL, queryID, {}, 10)
        : await datasetAPI.previewCandidate(candidateDSL, queryID, {}, 10)
      if (request !== endPreviewRequest.current || editorFingerprintRef.current !== previewFingerprint) return
      setEndPreview({
        loading: false,
        data: { columns: data.columns, columnMetadata: data.columnMetadata, rows: data.rows.slice(0, 5) },
      })
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
      const complete = Boolean(group && groupIsComplete(group))
      setCanvasNotice(complete ? '分组组件配置已暂存' : '分组组件配置已暂存，请继续完善')
    } else if (activeTransformID) {
      const transform = transformBoxes.find(item => item.id === activeTransformID)
      const complete = Boolean(transform && transformIsComplete(transform))
      setCanvasNotice(complete ? `${transformIsFilter(transform!) ? '过滤' : '字段处理'}组件配置已暂存` : `${transform && transformIsFilter(transform) ? '过滤' : '字段处理'}组件配置已暂存，请继续完善`)
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
      if (group.groupByMode === 'CUBE' && group.dimensions.length < 2) { setFormError(`“${group.name}”使用 GROUP BY CUBE 时至少需要两个分组字段`); return }
      if (group.groupByMode === 'CUBE' && group.dimensions.length > 8) { setFormError(`“${group.name}”使用 GROUP BY CUBE 时最多支持 8 个分组字段`); return }
      if (group.groupByMode === 'GROUPING_SETS' && !groupingSetsAreComplete(group)) { setFormError(`“${group.name}”的 GROUPING SETS 需要 1–64 个不重复且仅引用已选维度的分组集`); return }
      if (!group.metrics.length || group.metrics.some(metric => !metric.aggregation)) { setFormError(`请为“${group.name}”至少配置一个完整的聚合指标`); return }
      const codes = [...group.dimensions, ...group.metrics].map(field => safeIdentifier(field.code))
      if ([...group.dimensions, ...group.metrics].some(field => !field.name.trim() || !field.code.trim()) || new Set(codes).size !== codes.length) {
        setFormError(`“${group.name}”自动生成的字段别名为空或重复，请检查上游字段编码`); return
      }
    }
    for (const transform of transformBoxes) {
      const filter = transformIsFilter(transform)
      if (!transform.input) { setFormError(`请为${filter ? '过滤' : '字段处理'}组件“${transform.name}”连接输入`); return }
      if (!transform.name.trim()) { setFormError(`请为每个${filter ? '过滤' : '字段处理'}组件填写清晰的名称`); return }
      const availableFields = relationOutputFields(transform.input, relationBoxes, groupBoxes, draft.nodes, draft.fields, transformBoxes)
      const available = new Set(availableFields.map(field => field.key))
      if (filter) {
        if (!transform.conditions?.length || transform.conditions.some(condition => !filterConditionIsComplete(condition))) {
          setFormError(`请为“${transform.name}”至少配置一条完整的过滤条件`); return
        }
        if (transform.conditions.some(condition => !available.has(condition.inputKey))) {
          setFormError(`“${transform.name}”引用了已不可用的上游字段，请重新选择`); return
        }
        const filterable = new Set(availableFields.filter(field => field.kind !== 'METRIC' && !field.aggregation).map(field => field.key))
        if (transform.conditions.some(condition => !filterable.has(condition.inputKey))) {
          setFormError(`“${transform.name}”只能过滤明细字段，不能用于聚合指标`); return
        }
        const fieldsByKey = new Map(availableFields.map(field => [field.key, field]))
        for (const condition of transform.conditions) {
          if (condition.valueMode !== 'FIELD') continue
          if (!filterOperatorSupportsField(condition.operator)) {
            setFormError(`“${transform.name}”的 IN / NOT IN 只支持固定值集合`); return
          }
          const left = fieldsByKey.get(condition.inputKey)
          const right = fieldsByKey.get(condition.value)
          if (!right || !filterable.has(condition.value)) {
            setFormError(`“${transform.name}”引用了已不可用的比较字段`); return
          }
          if (condition.inputKey === condition.value) {
            setFormError(`“${transform.name}”请选择两个不同字段进行比较`); return
          }
          if (!filterFieldsAreCompatible(left, right)) {
            setFormError(`“${transform.name}”的比较字段类型不兼容`); return
          }
        }
        continue
      }
      if (!transform.rules.length || transform.rules.some(rule => !transformRuleIsComplete(rule))) {
        setFormError(`请为“${transform.name}”至少配置一条完整的转换规则`); return
      }
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
    if (!selectedBusinessDomainName) {
      setFormError('当前账号没有可用的所属领域，请先选择业务领域')
      return
    }
    if (!metadata.name.trim() || !metadata.description.trim()) {
      setFormError('请填写数据集名称和说明')
      return
    }
    if (designSnapshot) {
      const id = editingRecord?.id ?? 'snapshot-sales-order-new'
      const next: DatasetSummary = {
        ...(datasets.find(item => item.id === id) ?? snapshotDatasets[0]),
        id, code: generatedCode, name: metadata.name.trim(), description: metadata.description.trim(),
        layer: draft.layer ?? 'DWD', status: 'DRAFT', version: (editingRecord?.version ?? 0) + 1,
        updatedAt: '2026-08-11T10:18:00+08:00', currentPublishedVersionId: undefined,
      }
      setDatasets(current => [next, ...current.filter(item => item.id !== id)])
      setDialog(null)
      setEditingRecord(null)
      if (datasetId) navigate('/datasets?snapshot=assets', { replace: true })
      setNotice({ tone: 'success', message: `“${next.name}”已保存为草稿 V${next.version}，可提交校验与发布。` })
      return
    }
    setBusyAction(editingRecord ? 'update' : 'create')
    setFormError('')
    try {
      const completed = completedEditorDraft
      const dsl = buildDatasetDSL(completed)
      const validation = await datasetAPI.validate(dsl)
      const semanticNamed = completed.layer === 'DWD' || completed.layer === 'DWS' || completed.layer === 'ADS'
      let saved: DatasetRecord
      if (editingRecord) {
        saved = await datasetAPI.update(editingRecord.id, editingRecord.version, completed, dsl)
        if (saved.version <= editingRecord.version ||
          (!semanticNamed && (saved.dslHash !== validation.dslHash ||
            saved.name !== completed.name || saved.description !== completed.description)) ||
          (saved.dsl.dataset.domain ?? '') !== completed.domain ||
          (saved.dsl.dataset.subject ?? '') !== completed.subject) {
          throw new Error('服务端未确认最新配置已保存，请保留当前页面后重试')
        }
      } else {
        saved = await datasetAPI.create(dsl)
      }
      await loadDatasets()
      setDraftConflict(null)
      setDialog(null)
      setEditingRecord(null)
      if (datasetId) navigate('/datasets', { replace: true })
      setNotice({ tone: 'success', message: semanticNamed
        ? `已保存“${saved.name}”，LLM 已完成表名、字段名和受控标签语义校正`
        : editingRecord ? `已保存“${saved.name}”的最新配置` : `已创建“${saved.name}”，可继续进入修改完善配置` })
    } catch (cause) {
      if (editingRecord && cause instanceof RequestError && cause.status === 409 && cause.detail.code === 'DATASET_VERSION_CONFLICT') {
        setDraftConflict({ currentVersion: cause.detail.currentVersion, currentHash: cause.detail.currentHash })
        setFormError('草稿已被其他协作者更新。你的未保存配置仍保留在当前页面，请加载最新草稿后再合并修改。')
      } else {
        setFormError(cause instanceof Error ? cause.message : editingRecord ? '保存数据集失败' : '创建数据集失败')
      }
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
    setDetailAsset(null)
    setDetailAssetColumns([])
    setFormError('')
    if (designSnapshot) {
      const record = snapshotDatasetRecord(dataset)
      const pending: DatasetPublicationRequest = {
        id: `${dataset.id}-request-1`, datasetId: dataset.id, status: 'PENDING', version: 1,
        draftVersionId: record.draftVersionId, expectedDatasetVersion: record.version,
        expectedDraftRecordVersion: record.draftRecordVersion, expectedDslHash: record.dslHash,
        expectedPlanHash: record.planHash, requesterId: '王敏', requestNote: '关联关系与输出字段已完成校验，请审批发布。',
        submittedAt: '2026-08-11T09:45:00+08:00', updatedAt: '2026-08-11T09:45:00+08:00',
      }
      setPublicationRecord(record)
      setPublicationRequests(dataset.status === 'DRAFT' ? [pending] : [])
      setSelectedPublicationRequestID(dataset.status === 'DRAFT' ? pending.id : '')
      setPublicationCapabilities({ manage: true, publish: true })
      setBusyAction('')
      return
    }
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

  const submitPublicationRequest = async () => {
    if (!publicationRecord || !publicationCapabilities.manage || busyAction) return
    setBusyAction('publication-submit')
    setFormError('')
    if (designSnapshot) {
      const request: DatasetPublicationRequest = {
        id: `${publicationRecord.id}-request-${publicationRequests.length + 1}`, datasetId: publicationRecord.id,
        status: 'PENDING', version: 1, draftVersionId: publicationRecord.draftVersionId,
        expectedDatasetVersion: publicationRecord.version, expectedDraftRecordVersion: publicationRecord.draftRecordVersion,
        expectedDslHash: publicationRecord.dslHash, expectedPlanHash: publicationRecord.planHash,
        requesterId: '王敏', requestNote: publicationNote.trim(), submittedAt: '2026-08-11T10:20:00+08:00', updatedAt: '2026-08-11T10:20:00+08:00',
      }
      setPublicationRequests(current => [request, ...current])
      setSelectedPublicationRequestID(request.id)
      setPublicationNote('')
      setBusyAction('')
      setNotice({ tone: 'success', message: `“${publicationRecord.name}”已提交发布申请。` })
      return
    }
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
          ? `“${publicationRecord.name}”已提交发布申请；审批通过前不会启动加工`
          : `当前草稿已有${publicationStatusLabels[request.status] ?? request.status}记录`,
      })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '提交发布申请失败')
    } finally {
      setBusyAction('')
    }
  }

  const approvePublicationRequest = async () => {
    if (!publicationRecord || !selectedPublicationRequest || selectedPublicationRequest.status !== 'PENDING' || !publicationCapabilities.publish || busyAction) return
    setBusyAction('publication-approve')
    setFormError('')
    if (designSnapshot) {
      setPublicationRequests(current => current.map(item => item.id === selectedPublicationRequest.id ? {
        ...item, status: 'APPROVED', reviewerId: '数据治理管理员', reviewNote: publicationDecisionNote.trim(),
        publishedVersionId: `${publicationRecord.id}-v${publicationRecord.version + 1}`,
        reviewedAt: '2026-08-11T10:22:00+08:00', updatedAt: '2026-08-11T10:22:00+08:00',
      } : item))
      setDatasets(current => current.map(item => item.id === publicationRecord.id ? {
        ...item, status: 'PUBLISHED', version: item.version + 1,
        currentPublishedVersionId: `${publicationRecord.id}-v${item.version + 1}`, updatedAt: '2026-08-11T10:22:00+08:00',
      } : item))
      setPublicationDecisionNote('')
      setBusyAction('')
      setNotice({ tone: 'success', message: `“${publicationRecord.name}”审批通过，物化任务已进入队列。` })
      return
    }
    try {
      const result = await datasetAPI.approvePublication(
        publicationRecord.id, selectedPublicationRequest.id, selectedPublicationRequest.version, publicationDecisionNote.trim(),
      )
      await refreshPublication(publicationRecord.id)
      setPublicationDecisionNote('')
      setSelectedPublicationRequestID(result.request.id)
      const sourceMappedDWS = publicationRecord.originTableId &&
        publicationRecord.dsl.dataset.sourceMode === 'PRE_AGGREGATED'
      const processing = sourceMappedDWS
        ? '源端汇总映射已生效'
        : publicationRecord.layer === 'ODS'
          ? '指标候选提取任务已启动'
          : `${publicationRecord.layer} PostgreSQL 物化任务已启动`
      setNotice({ tone: 'success', message: `“${publicationRecord.name}”审批通过并发布为 V${result.publishedVersion.versionNo}；${processing}` })
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
    if (designSnapshot) {
      setPublicationRequests(current => current.map(item => item.id === selectedPublicationRequest.id ? {
        ...item, status: 'REJECTED', reviewerId: '数据治理管理员', reviewNote: reason,
        reviewedAt: '2026-08-11T10:22:00+08:00', updatedAt: '2026-08-11T10:22:00+08:00',
      } : item))
      setPublicationDecisionNote('')
      setBusyAction('')
      setNotice({ tone: 'success', message: `“${publicationRecord.name}”已退回修改，草稿和审批意见均已保留。` })
      return
    }
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

  const withdrawPublicationRequest = async () => {
    if (!publicationRecord || !currentDraftPublicationRequest || currentDraftPublicationRequest.status !== 'PENDING' || !publicationCapabilities.manage || busyAction) return
    setBusyAction('publication-withdraw')
    setFormError('')
    if (designSnapshot) {
      setPublicationRequests(current => current.map(item => item.id === currentDraftPublicationRequest.id ? {
        ...item, status: 'CANCELLED', version: item.version + 1, reviewNote: '申请人撤回',
        reviewedAt: '2026-08-11T10:24:00+08:00', updatedAt: '2026-08-11T10:24:00+08:00',
      } : item))
      setBusyAction('')
      setNotice({ tone: 'success', message: `已撤回“${publicationRecord.name}”的发布申请，可继续修改或重新提交。` })
      return
    }
    try {
      const withdrawn = await datasetAPI.withdrawPublication(
        publicationRecord.id, currentDraftPublicationRequest.id, currentDraftPublicationRequest.version,
      )
      await refreshPublication(publicationRecord.id)
      setSelectedPublicationRequestID(withdrawn.id)
      setNotice({ tone: 'success', message: `已撤回“${publicationRecord.name}”的发布申请，可继续修改或重新提交。` })
    } catch (cause) {
      await refreshPublication(publicationRecord.id)
      const stale = cause instanceof RequestError && ['DATASET_PUBLICATION_REQUEST_CONFLICT', 'DATASET_PUBLICATION_REQUEST_NOT_PENDING'].includes(cause.detail.code)
      setFormError(stale ? '申请状态已被其他人更新，页面已同步最新结果。' : cause instanceof Error ? cause.message : '撤回发布申请失败')
    } finally {
      setBusyAction('')
    }
  }

  const openView = async (dataset: DatasetSummary) => {
    setDialog({ mode: 'view', dataset })
    setDetail(null)
    setDetailAsset(null)
    setDetailAssetColumns([])
    setDetailPreview(null)
    setDetailPreviewError('')
    setFormError('')
    setBusyAction(`view:${dataset.id}`)
    const [recordResult, previewResult, assetResult, columnsResult] = await Promise.allSettled([
      datasetAPI.get(dataset.id),
      datasetAPI.preview(dataset.id, crypto.randomUUID(), {}, 10),
      dataset.originTableId ? datasetAPI.table(dataset.originTableId) : Promise.resolve(null),
      dataset.originTableId ? datasetAPI.allColumns(dataset.originTableId) : Promise.resolve({ items: [] }),
    ])
    if (recordResult.status === 'fulfilled') setDetail(recordResult.value)
    else setFormError(recordResult.reason instanceof Error ? recordResult.reason.message : '加载数据集详情失败')
    if (previewResult.status === 'fulfilled') setDetailPreview(previewResult.value)
    else setDetailPreviewError(previewResult.reason instanceof Error ? previewResult.reason.message : '加载预览数据失败')
    if (assetResult.status === 'fulfilled') setDetailAsset(assetResult.value)
    if (columnsResult.status === 'fulfilled') setDetailAssetColumns(columnsResult.value.items)
    setBusyAction('')
  }

  const openMetadataEdit = () => {
    if (!detail || !dialog?.dataset) return
    setMetadataEdit({
      name: detail.name,
      description: detail.description,
      subject: detail.dsl.dataset.subject ?? '',
      fields: datasetDetailFields(detail),
    })
    setFormError('')
    setDialog({ mode: 'edit-metadata', dataset: dialog.dataset })
  }

  const updateMetadataField = (id: string, patch: Partial<DatasetDetailField>) => {
    setMetadataEdit(current => current ? {
      ...current,
      fields: current.fields.map(field => field.id === id ? { ...field, ...patch } : field),
    } : current)
  }

  const saveMetadataEdit = async () => {
    if (!detail || !metadataEdit || !metadataEdit.name.trim() || !metadataEdit.description.trim()) {
      setFormError('请填写数据集名称和说明')
      return
    }
    if (metadataEdit.fields.some(field => !field.name.trim())) {
      setFormError('每个字段都必须填写业务名称')
      return
    }
    setBusyAction('metadata-update')
    setFormError('')
    try {
      const saved = await datasetAPI.updateMetadata(
        detail.id,
        detail.version,
        {
          name: metadataEdit.name.trim(),
          description: metadataEdit.description.trim(),
          subject: metadataEdit.subject.trim(),
          fields: metadataEdit.fields.map(field => ({
            id: field.id,
            name: field.name.trim(),
            description: field.description.trim(),
            role: field.role,
            semanticType: field.semanticType.trim(),
            nullable: field.nullable,
            visible: field.visible,
          })),
        },
      )
      setDetail(saved)
      await loadDatasets()
      setDialog(null)
      setMetadataEdit(null)
      setNotice({ tone: 'success', message: `“${saved.name}”元信息已保存，DAG 未改变` })
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '保存元信息失败')
    } finally {
      setBusyAction('')
    }
  }

  const openHistory = async (dataset: DatasetSummary) => {
    const request = ++historySelectionRequest.current
    setDialog({ mode: 'history', dataset })
    setHistoryRecord(null)
    setHistoryItems([])
    setSelectedHistoryVersion(null)
    setHistoryUsage(null)
    setHistoryPreview(null)
    setHistoryConfirm(false)
    setFormError('')
    setBusyAction(`history:${dataset.id}`)
    if (designSnapshot) {
      const record = snapshotDatasetRecord(dataset)
      record.dsl.nodes = [{ id: 'source_current', name: dataset.originTableName || '当前发布输入', type: 'TABLE' }]
      record.dsl.fields = [
        { id: 'field_date', code: 'biz_date', name: '业务日期', canonicalType: 'DATE', visible: true },
        { id: 'field_amount', code: 'sales_amount', name: '销售金额', canonicalType: 'DECIMAL', visible: true },
        { id: 'field_region', code: 'region_name', name: '所属区域', canonicalType: 'STRING', visible: true },
      ]
      const oldVersion: PublishedVersionRecord = {
        id: dataset.currentPublishedVersionId || `${dataset.id}-published-v${Math.max(1, dataset.version - 1)}`,
        datasetId: dataset.id, versionNo: Math.max(1, dataset.version - 1), status: 'PUBLISHED', dslVersion: '1.0',
        dslHash: `previous-${dataset.dslHash}`, planHash: `previous-plan-${dataset.dslHash}`,
        dsl: { ...record.dsl, dataset: { ...record.dsl.dataset, description: '稳定发布版本，用于展示回滚前的差异与影响。' }, nodes: [{ id: 'source_legacy', name: '历史发布输入', type: 'TABLE' }], fields: record.dsl.fields.slice(0, 2) },
        logicalPlan: {}, publishedAt: '2026-08-05T16:30:00+08:00', publishedBy: '数据管理员', datasetRecordVersion: dataset.version - 1,
        draftVersionId: `${dataset.id}-draft-v${dataset.version - 1}`, draftRecordVersion: dataset.version - 1,
      }
      const summary: PublishedVersionSummary = oldVersion
      setHistoryRecord(record)
      setHistoryItems([summary])
      setSelectedHistoryVersion(oldVersion)
      setHistoryUsage({ downstreamDraftReferences: 2, downstreamPublishedReferences: 5, activeQueryRuns: 1 })
      setHistoryPreview({ versionID: oldVersion.id, loading: false, data: { queryId: 'snapshot-version-preview', columns: ['biz_date', 'sales_amount'], rows: [['2026-08-10', 1286000], ['2026-08-11', 1394000]], rowCount: 2, durationMs: 184 } })
      setBusyAction('')
      return
    }
    try {
      const [record, versions] = await Promise.all([datasetAPI.get(dataset.id), loadAllPublishedVersions(dataset.id)])
      if (request !== historySelectionRequest.current) return
      setHistoryRecord(record)
      setHistoryItems(versions)
      if (versions[0]) {
        setHistoryPreview({ versionID: versions[0].id, loading: true })
        const previewRequest = datasetAPI.previewVersion(dataset.id, versions[0].id, crypto.randomUUID(), {}, 5).then(data => ({ data })).catch(cause => ({ error: cause instanceof Error ? cause.message : '加载发布版本数据预览失败' }))
        const [version, usage] = await Promise.all([datasetAPI.getVersion(dataset.id, versions[0].id), datasetAPI.getVersionUsage(dataset.id, versions[0].id)])
        if (request === historySelectionRequest.current) { setSelectedHistoryVersion(version); setHistoryUsage(usage); setBusyAction('') }
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
    setHistoryUsage(null)
    setHistoryPreview({ versionID, loading: true })
    setFormError('')
    setBusyAction(`version:${versionID}`)
    try {
      const previewRequest = datasetAPI.previewVersion(dataset.id, versionID, crypto.randomUUID(), {}, 5).then(data => ({ data })).catch(cause => ({ error: cause instanceof Error ? cause.message : '加载发布版本数据预览失败' }))
      const [version, usage] = await Promise.all([datasetAPI.getVersion(dataset.id, versionID), datasetAPI.getVersionUsage(dataset.id, versionID)])
      if (request === historySelectionRequest.current) { setSelectedHistoryVersion(version); setHistoryUsage(usage); setBusyAction('') }
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

  const runDatasetDAG = async (dataset: DatasetSummary) => {
    if (dataset.status !== 'PUBLISHED' || !dataset.currentPublishedVersionId) {
      setNotice({ tone: 'error', message: `“${dataset.name}”没有当前已发布版本，请先发布再运行 DAG` })
      return
    }
    setBusyAction(`dag-run:${dataset.id}`)
    if (designSnapshot) {
      const run: DatasetDAGRun = {
        id: `${dataset.id}-run-${Date.now()}`, datasetId: dataset.id, datasetVersionId: dataset.currentPublishedVersionId,
        layer: dataset.layer, mode: 'FULL', status: 'RUNNING', attempt: 1, maxAttempts: 3,
        createdAt: '2026-08-11T10:25:00+08:00', updatedAt: '2026-08-11T10:25:00+08:00', startedAt: '2026-08-11T10:25:00+08:00',
      }
      setDAGRuns(current => ({ ...current, [dataset.id]: run }))
      setBusyAction('')
      setNotice({ tone: 'success', message: `“${dataset.name}”的物化任务已启动，可在运行诊断中跟踪。` })
      return
    }
    try {
      const record = await datasetAPI.get(dataset.id)
      if (record.status !== 'PUBLISHED' || record.currentPublishedVersionId !== dataset.currentPublishedVersionId) {
        throw new Error('当前发布版本已变化，请刷新后重试')
      }
      const published = await datasetAPI.getVersion(dataset.id, record.currentPublishedVersionId)
      if (published.status !== 'PUBLISHED') throw new Error('当前版本尚未发布')
      const previousRun = dagRuns[dataset.id]
      const mode: 'FULL' | 'INCREMENTAL' = previousRun?.status === 'SUCCEEDED' &&
        ['DIM', 'DWD', 'DWS', 'ADS'].includes(dataset.layer) ? 'INCREMENTAL' : 'FULL'
      let effectiveMode = mode
      let run: DatasetDAGRun
      try {
        run = await datasetAPI.runDAG(dataset.id, published.id, crypto.randomUUID(), mode)
      } catch (cause) {
        if (mode !== 'INCREMENTAL' || !(cause instanceof RequestError) ||
          !['MATERIALIZATION_CONFLICT', 'MATERIALIZATION_INVALID_REQUEST'].includes(cause.detail.code)) throw cause
        effectiveMode = 'FULL'
        run = await datasetAPI.runDAG(dataset.id, published.id, crypto.randomUUID(), 'FULL')
      }
      setDAGRuns(current => ({ ...current, [dataset.id]: run }))
      setNotice({ tone: 'success', message: `“${dataset.name}”已按发布 V${published.versionNo} 提交${effectiveMode === 'INCREMENTAL' ? '水位增量刷新' : mode === 'INCREMENTAL' ? '完整替换（当前快照不满足增量条件，已安全降级）' : '完整替换'} DAG；质量门禁通过后才会原子切换，当前${run.status === 'RUNNING' ? '执行中' : '排队中'}` })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : 'DAG 运行失败' })
    } finally {
      setBusyAction('')
    }
  }

  const openMaterialization = async (dataset: DatasetSummary) => {
    const run = dagRuns[dataset.id]
    if (!run) return
    setDialog({ mode: 'materialization', dataset })
    setMaterializationDetail(null)
    setFormError('')
    setBusyAction(`dag-detail:${dataset.id}`)
    if (designSnapshot) {
      setMaterializationDetail({
        ...run,
        inputs: [{ ordinal: 1, type: 'DATASET', layer: 'DWD', sourceVersion: 'V6', schemaHash: 'e92c64a8317bb740', snapshotHash: 'bfe8410cc278', rowCount: 286940 }],
        nodes: [
          { id: 'extract', kind: 'EXTRACT', engine: 'POSTGRESQL', status: 'SUCCEEDED', attempt: 1, outputRowCount: 286940 },
          { id: 'transform', kind: 'TRANSFORM', engine: 'POSTGRESQL', status: run.status === 'FAILED' ? 'FAILED' : 'SUCCEEDED', attempt: run.attempt, errorCode: run.errorCode, errorMessage: run.errorMessage },
          { id: 'quality', kind: 'QUALITY', engine: 'POSTGRESQL', status: run.status === 'FAILED' ? 'SKIPPED' : 'SUCCEEDED', attempt: 1 },
        ],
        quality: run.status === 'FAILED' ? [] : [{
          nodeId: 'quality', ruleCode: 'ROW_COUNT_NONNEGATIVE', ruleVersion: '1', scope: 'DATASET',
          severity: 'ERROR', status: 'PASSED', expectation: { minimum: 0 }, observed: { rowCount: 286940 },
          message: 'warehouse output row count is valid', measuredAt: '2026-08-11T10:26:00+08:00',
        }],
        succeededNodes: run.status === 'FAILED' ? 1 : 3, failedNodes: run.status === 'FAILED' ? 1 : 0, pendingNodes: run.status === 'FAILED' ? 1 : 0,
        partialSuccess: run.status === 'FAILED',
      })
      setBusyAction('')
      return
    }
    try {
      setMaterializationDetail(await datasetAPI.getDAGRun(dataset.id, run.id))
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '加载物化运行诊断失败')
    } finally {
      setBusyAction('')
    }
  }

  const stopDatasetDAG = async (dataset: DatasetSummary) => {
    const materializationRun = dagRuns[dataset.id]
    if (!materializationRun || !activeDAGRunStatuses.has(materializationRun.status)) return
    setBusyAction(`dag-stop:${dataset.id}`)
    try {
      const stopped = await datasetAPI.stopDAG(dataset.id, materializationRun.id)
      setDAGRuns(current => ({ ...current, [dataset.id]: stopped }))
      setNotice({ tone: 'success', message: `已停止“${dataset.name}”的本次 DAG 运行` })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '停止 DAG 失败' })
    } finally {
      setBusyAction('')
    }
  }

  const openLifecycle = async (dataset: DatasetSummary, lifecycleAction: 'disable' | 'restore' | 'delete') => {
    setDialog({ mode: 'lifecycle', dataset, lifecycleAction })
    setLifecycleImpact(null)
    setFormError('')
    setBusyAction(`lifecycle-impact:${dataset.id}`)
    if (designSnapshot) {
      const blocked = dataset.id === 'snapshot-customer-dim'
      setLifecycleImpact({
        datasetId: dataset.id, status: dataset.status,
        downstreamDraftReferences: blocked ? 2 : 0, downstreamPublishedReferences: blocked ? 5 : 0,
        activeQueryRuns: blocked ? 1 : 0, activeBuildRuns: 0, materializations: dataset.currentPublishedVersionId ? 1 : 0,
        canDisable: dataset.status !== 'DISABLED', canRestore: dataset.status === 'DISABLED', canDelete: !blocked,
        blockers: blocked ? ['下游草稿仍引用该数据集版本', '下游发布版本仍依赖该数据集', '仍有查询正在运行'] : [],
      })
      setBusyAction('')
      return
    }
    try {
      setLifecycleImpact(await datasetAPI.getLifecycleImpact(dataset.id))
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '加载数据集生命周期影响失败')
    } finally {
      setBusyAction('')
    }
  }

  const executeLifecycle = async () => {
    const dataset = dialog?.dataset
    const action = dialog?.lifecycleAction
    if (!dataset || !action) return
    setBusyAction(`${action}:${dataset.id}`)
    setFormError('')
    try {
      if (designSnapshot) {
        if (action === 'delete') setDatasets(current => current.filter(item => item.id !== dataset.id))
        else setDatasets(current => current.map(item => item.id === dataset.id ? { ...item, status: action === 'disable' ? 'DISABLED' : 'PUBLISHED', version: item.version + 1 } : item))
      } else if (action === 'disable') {
        const record = await datasetAPI.disable(dataset.id, dataset.version)
        setDatasets(current => current.map(item => item.id === record.id ? { ...item, status: record.status, version: record.version, currentPublishedVersionId: record.currentPublishedVersionId, updatedAt: record.updatedAt } : item))
      } else if (action === 'restore') {
        const record = await datasetAPI.restore(dataset.id, dataset.version)
        setDatasets(current => current.map(item => item.id === record.id ? { ...item, status: record.status, version: record.version, currentPublishedVersionId: record.currentPublishedVersionId, updatedAt: record.updatedAt } : item))
      } else {
        await datasetAPI.delete(dataset.id, dataset.version)
        setDatasets(current => current.filter(item => item.id !== dataset.id))
      }
      setDialog(null)
      setLifecycleImpact(null)
      setNotice({ tone: 'success', message: action === 'delete' ? `已安全删除“${dataset.name}”，历史审计和异步清理记录已保留` : action === 'disable' ? `已停用“${dataset.name}”，随时可恢复到停用前状态` : `已恢复“${dataset.name}”到停用前稳定状态` })
    } catch (cause) { setFormError(cause instanceof Error ? cause.message : '数据集生命周期操作失败') }
    finally { setBusyAction('') }
  }

  const toggleDatasetSelection = (datasetID: string) => {
    setSelectedDatasetIDs(current => {
      const next = new Set(current)
      if (next.has(datasetID)) next.delete(datasetID)
      else next.add(datasetID)
      return next
    })
  }

  const toggleFilteredSelection = () => {
    setSelectedDatasetIDs(current => {
      const next = new Set(current)
      if (allFilteredSelected) filtered.forEach(dataset => next.delete(dataset.id))
      else filtered.forEach(dataset => next.add(dataset.id))
      return next
    })
  }

  const closeBatchDialog = () => {
    if (busyAction) return
    setBatchAction(null)
    setFormError('')
  }

  const executeBatchAction = async () => {
    if (!batchAction || !selectedDatasets.length || busyAction) return
    const activeAction = batchAction
    const actionDatasets = activeAction === 'stop'
      ? selectedDatasets.filter(dataset => dagRuns[dataset.id] && activeDAGRunStatuses.has(dagRuns[dataset.id].status))
      : activeAction === 'run'
        ? selectedDatasets.filter(dataset =>
            dataset.status === 'PUBLISHED' && dataset.currentPublishedVersionId &&
            (!dagRuns[dataset.id] || !activeDAGRunStatuses.has(dagRuns[dataset.id].status))
          )
      : selectedDatasets
    if (!actionDatasets.length) return
    setBusyAction(`batch:${activeAction}`)
    setFormError('')
    try {
      const outcomes = await mapDatasetBatch(actionDatasets, async dataset => {
        if (activeAction === 'publish') {
          if (dataset.status === 'DISABLED' || dataset.status === 'DEPRECATED') {
            throw new Error('当前状态不能提交发布申请')
          }
          const record = await datasetAPI.get(dataset.id)
          await datasetAPI.requestPublication(record.id, {
            draftVersionId: record.draftVersionId,
            expectedVersion: record.version,
            expectedDraftRecordVersion: record.draftRecordVersion,
            expectedDslHash: record.dslHash,
            validationParameters: {},
          })
          return
        }
        if (activeAction === 'stop') {
          const run = dagRuns[dataset.id]
          if (!run || !activeDAGRunStatuses.has(run.status)) {
            throw new Error('当前没有运行中的 DAG')
          }
          await datasetAPI.stopDAG(dataset.id, run.id)
          return
        }
        if (activeAction === 'run') {
          const record = await datasetAPI.get(dataset.id)
          if (record.status !== 'PUBLISHED' || !record.currentPublishedVersionId ||
            record.currentPublishedVersionId !== dataset.currentPublishedVersionId) {
            throw new Error('当前发布版本已变化，请刷新后重试')
          }
          const published = await datasetAPI.getVersion(dataset.id, record.currentPublishedVersionId)
          if (published.status !== 'PUBLISHED') throw new Error('当前版本尚未发布')
          const run = await datasetAPI.runDAG(dataset.id, published.id, crypto.randomUUID())
          setDAGRuns(current => ({ ...current, [dataset.id]: run }))
          return
        }
        await datasetAPI.delete(dataset.id, dataset.version)
      })
      const failures = outcomes.filter(outcome => outcome.error)
      const succeeded = outcomes.length - failures.length
      setSelectedDatasetIDs(new Set(failures.map(outcome => outcome.dataset.id)))
      if (activeAction === 'publish' || activeAction === 'delete') await loadDatasets()
      if (activeAction === 'run' || activeAction === 'stop') {
        await refreshDAGRuns()
      }
      setBatchAction(null)
      const actionLabel = activeAction === 'publish' ? '提交发布申请' : activeAction === 'run' ? '运行 DAG' : activeAction === 'stop' ? '停止 DAG' : '删除'
      if (!failures.length) {
        setNotice({ tone: 'success', message: `批量${actionLabel}完成，共处理 ${succeeded} 个数据集` })
      } else {
        const examples = failures.slice(0, 2).map(outcome => `${outcome.dataset.name}：${outcome.error}`).join('；')
        setNotice({
          tone: 'error',
          message: `批量${actionLabel}完成：成功 ${succeeded} 个，失败 ${failures.length} 个。${examples}${failures.length > 2 ? '；其余失败项仍保持选中' : ''}`,
        })
      }
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '批量操作失败')
    } finally {
      setBusyAction('')
    }
  }

  const triggerDatasetLLM = async (trigger: DatasetLLMTrigger, label: string) => {
    if (busyAction) return
    const selectionError = modelingSelectionError(trigger, selectedDatasets)
    if (selectionError) {
      setNotice({ tone: 'error', message: `${label}尚未触发：${selectionError}` })
      return
    }
    if (designSnapshot) {
      setBusyAction(`llm:${trigger}`)
      setModelingMonitors(current => ({
        ...current,
        [trigger]: {
          ...current[trigger],
          tasks: [],
          expected: false,
          ready: true,
          logsPinned: false,
          syncError: '',
        },
      }))
      setNotice({
        tone: 'success',
        message: `${label}已根据当前数据范围生成建议，新草稿已进入数据集目录。`,
      })
      setBusyAction('')
      return
    }
    const selectedIDs = selectedDatasets.map(dataset => dataset.id)
    modelingRunTaskIDs.current[trigger] = new Set()
    modelingRequestedAt.current[trigger] = Date.now()
    expectModelingTasks(trigger, true)
    setModelingMonitors(current => ({
      ...current,
      [trigger]: {
        ...current[trigger],
        tasks: [],
        expected: true,
        logsPinned: false,
        syncError: '',
      },
    }))
    setBusyAction(`llm:${trigger}`)
    try {
      const result = await datasetAPI.triggerLLM(trigger, selectedIDs)
      if (result.enqueuedCount + result.existingCount === 0) {
        expectModelingTasks(trigger, false)
        modelingRequestedAt.current[trigger] = null
      }
      if (result.blockedReason === 'DIM_MODELING_REQUIRED') {
        setNotice({
          tone: 'error',
          message: `明细建模尚未提交：${result.blockedCount ?? 0} 个领域尚未完成本轮维度建模。请先执行“维度建模”，待任务完成并发布 DIM 后再试。`,
        })
        return
      }
      if (result.blockedReason === 'DIM_PUBLICATION_REQUIRED') {
        setNotice({
          tone: 'error',
          message: `明细建模尚未提交：${result.blockedCount ?? 0} 个领域的 DIM 仍是草稿。请先提交发布并完成审批，再触发明细建模。`,
        })
        return
      }
      if (result.blockedReason === 'NO_FACT_MODEL_AVAILABLE') {
        setNotice({
          tone: 'error',
          message: `明细建模没有可提交任务：${result.blockedCount ?? 0} 个领域经维度建模确认暂无事实表。`,
        })
        return
      }
      if (result.blockedReason === 'DWD_MODELING_COMPLETED') {
        setNotice({
          tone: 'success',
          message: '当前最新已发版 DIM 批次的明细建模已经完成，无需重复提交。如需重新生成，请先执行新一轮维度建模并发布 DIM。',
        })
        return
      }
      if (result.blockedReason === 'DWD_MODELING_RETRY_REQUIRED') {
        setNotice({
          tone: 'error',
          message: '当前最新 DIM 批次的明细建模未完成，请在运行日志中重试失败任务；DWD 重试仍只使用当前最新已发版 DIM。',
        })
        return
      }
      if (result.blockedReason === 'DWD_PUBLICATION_REQUIRED') {
        setNotice({
          tone: 'error',
          message: `主题建模尚未提交：${result.blockedCount ?? 0} 个 DWD/DIM 仍是草稿。请先完成发布，再由 LLM 创建 DWS DAG。`,
        })
        return
      }
      const unit = '个批次'
      const existing = result.existingCount
        ? `；${result.existingCount} ${unit}已有待处理或运行中任务`
        : ''
      const noFact = trigger === 'DWD_MODELING' && result.blockedCount
        ? `；${result.blockedCount} 个纯维度领域无需创建 DWD`
        : ''
      const unpublished = trigger === 'DWS_MODELING' && result.blockedCount
        ? `；另有 ${result.blockedCount} 个 DWD/DIM 草稿未纳入，发布后才可参与主题建模`
        : ''
      const submitted = trigger === 'DIM_MODELING'
        ? `已提交 ${result.enqueuedCount} 个维度建模批次（纳入 ${result.eligibleCount} 张 ODS）`
        : trigger === 'DWD_MODELING'
          ? `已提交 ${result.enqueuedCount} 个明细建模批次（只执行事实落地；符合条件 ${result.eligibleCount} 个范围）`
          : `已提交 ${result.enqueuedCount} 个主题建模任务（LLM 创建 DWS DAG 草稿；符合条件 ${result.eligibleCount} 个范围）`
      if (result.enqueuedCount > 0 || result.existingCount > 0) {
        rememberBackgroundTaskFocus(trigger)
      }
      if (result.enqueuedCount + result.existingCount > 0) {
        await refreshModelingTasks()
      }
      setNotice({
        tone: 'success',
        message: `${selectedIDs.length ? `已按所选 ${selectedIDs.length} 个数据集校验并执行：` : '已按默认全量范围执行：'}${submitted}${existing}${noFact}${unpublished}`,
      })
    } catch (cause) {
      expectModelingTasks(trigger, false)
      modelingRequestedAt.current[trigger] = null
      modelingRunTaskIDs.current[trigger] = new Set()
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : `${label}触发失败` })
    } finally {
      setBusyAction('')
    }
  }

  const generateDatasetAIPlan = async (retryInstruction?: string, useActualCanvas = false) => {
    const instruction = (retryInstruction ?? aiPrompt).trim()
    if (!instruction || aiBusy || aiApplying || assetsLoading || busyAction) return
    const requestID = ++aiRequest.current
    const baseFingerprint = editorFingerprintRef.current
    const actualCurrent = datasetAIPlanFromEditor(draft, currentDesignerGraph, metadata)
    const visibleComponentCount = relationBoxes.length + groupBoxes.length + transformBoxes.length
    const serializedComponentCount = actualCurrent
      ? actualCurrent.joins.length + actualCurrent.groups.length + (actualCurrent.transforms?.length ?? 0)
      : 0
    const endInputChanged = Boolean(endBox?.input && actualCurrent && (
      actualCurrent.end.input.kind !== endBox.input.kind || actualCurrent.end.input.id !== endBox.input.id
    ))
    if (draft.nodes.length > 0 && (!actualCurrent || serializedComponentCount !== visibleComponentCount || endInputChanged)) {
      setAIError(datasetAILocalIssue(
        '当前未保存画布中存在尚未完成配置或连线的组件，系统已停止提交，避免 AI 忽略这些组件。',
        '请补全画布上每个组件的输入、必填字段和结束节点连线后重试；完整画布会作为 current 基线发送。',
      ))
      setAIRetryAction(null)
      return
    }
    // Once the canvas contains nodes it is the single source of truth for every AI
    // modification. A staged proposal is only reusable while a brand-new canvas is
    // still empty; this prevents a follow-up prompt from silently ignoring manual edits.
    const current = datasetAIRequestContext(actualCurrent, aiResult?.proposal.plan, {
      forceLiveCanvas: useActualCanvas,
      stagedProposalApplied: aiApplied,
    })
    setAILastInstruction(instruction)
    setAIRetryAction(null)
    setAIProgressLogs([])
    setAIBusy(true)
    setAIError(null)
    try {
      const result = await requestDatasetAIProposal(editingRecord?.id, instruction, current, event => {
        if (requestID !== aiRequest.current) return
        setAIProgressLogs(logs => [...logs, event].slice(-30))
      })
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
      const appliedMetadata = { ...metadata, ...materialized.metadata }
      const appliedSnapshot: DatasetEditorSnapshot = {
        draft: materialized.draft,
        relationBoxes: materialized.graph.joins,
        groupBoxes: materialized.graph.groups,
        transformBoxes: appliedTransforms,
        endBox: materialized.graph.end ?? null,
        nodePositions: materialized.graph.nodePositions,
        metadata: appliedMetadata,
      }
      setAIUndo({ before: currentEditorSnapshot, appliedFingerprint: editorFingerprint(appliedSnapshot) })
      setDraft(materialized.draft)
      setRelationBoxes(materialized.graph.joins)
      setGroupBoxes(materialized.graph.groups)
      setTransformBoxes(appliedTransforms)
      setEndBox(materialized.graph.end ?? null)
      setNodePositions(materialized.graph.nodePositions)
      setMetadata(appliedMetadata)
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
    if (document.fullscreenElement &&
      document.fullscreenElement === canvasFullscreenTarget.current) {
      void document.exitFullscreen()
    }
    setCanvasFullscreen(false)
    setDialog(null)
    setDraftConflict(null)
    setMetadataEdit(null)
    setEditingRecord(null)
    setHistoryRecord(null)
    setHistoryItems([])
    setSelectedHistoryVersion(null)
    setHistoryUsage(null)
    setHistoryPreview(null)
    setHistoryConfirm(false)
    setMaterializationDetail(null)
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
  const completeDetailFields = detail ? datasetDetailFields(detail) : []

  return <AppShell className={`dataset-center-shell ${qaViewport1920 ? 'qa-1920-dataset-workflow' : ''}`} title="数据集资产" eyebrow="数据资产" actions={<button className="primary-button dataset-create-button" type="button" disabled={actionBusy} onClick={() => void openCreate()}><PlusIcon size={18} weight="bold" />新建数据集</button>}>
    {notice && <div className={`dataset-center-toast ${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}>{notice.tone === 'success' ? <CheckCircleIcon size={20} weight="fill" /> : <DropSlashIcon size={20} weight="fill" />}<span>{notice.message}</span><button type="button" aria-label="关闭消息" onClick={() => setNotice(null)}><XIcon size={17} /></button></div>}
    <section className="dataset-center" aria-label="数据集配置中心内容">
      <header className="dataset-center-summary">
        <div><span className="eyebrow">数据资产化 · 第 2 段</span><h2>数据集建模与发布</h2><p>将已治理的数据表组织成可复用的数据模型，并完成校验、审批和物化交付。</p></div>
        <div className="dataset-center-total"><strong>{datasets.length}</strong><span>领域内数据集</span></div>
      </header>
      <ol className="dataset-chain-progress" aria-label="数据集资产化主流程">
        <li className="complete"><span><CheckCircleIcon size={18} weight="fill" /></span><div><strong>数据表资产</strong><small>字段与业务语义已完善</small></div></li>
        <li className="active"><span><TreeStructureIcon size={18} weight="bold" /></span><div><strong>数据集建模</strong><small>配置字段、关联和粒度</small></div></li>
        <li><span><ListChecksIcon size={18} /></span><div><strong>校验与发布</strong><small>预览、审批、冻结版本</small></div></li>
        <li><span><ArrowClockwiseIcon size={18} /></span><div><strong>物化交付</strong><small>运行任务并服务下游</small></div></li>
      </ol>
      <div className="dataset-overview-grid" aria-label="数据集运行概览">
        <article><span><RowsIcon size={21} /></span><div><small>全部数据集</small><strong>{datasets.length}</strong></div><em>当前领域</em></article>
        <article><span className="is-success"><CheckCircleIcon size={21} /></span><div><small>已发布</small><strong>{datasets.filter(item => item.status === 'PUBLISHED').length}</strong></div><em>可被下游使用</em></article>
        <article><span className="is-warning"><CalendarDotsIcon size={21} /></span><div><small>待处理</small><strong>{datasets.filter(item => item.status === 'DRAFT' || item.status === 'VALIDATING').length}</strong></div><em>草稿或校验中</em></article>
        <article><span className="is-blue"><ArrowClockwiseIcon size={21} /></span><div><small>物化运行</small><strong>{Object.values(dagRuns).filter(run => activeDAGRunStatuses.has(run.status)).length}</strong></div><em>{Object.values(dagRuns).filter(run => run.status === 'FAILED' || run.slaBreached).length} 个需处理告警</em></article>
      </div>
      <section className="dataset-catalog-panel">
        <header>
          <div><span className="eyebrow">数据集目录</span><h3>模型资产清单</h3><p>按数仓分层查看数据集，进入画布完成建模后再提交发布。</p></div>
          <section className="dataset-intelligent-modeling" aria-label="智能建模">
            <span><MagicWandIcon size={15} weight="fill" />智能建模{selectedDatasets.length ? ` · 已选 ${selectedDatasets.length} 个` : ''}</span>
            <div>
              {modelingMonitorConfigs.map(config => <DatasetModelingAction
                key={config.trigger}
                config={config}
                monitor={modelingMonitors[config.trigger]}
                actionBusy={actionBusy}
                submitting={busyAction === `llm:${config.trigger}`}
                logID={`${modelingLogIDPrefix}-${config.trigger.toLowerCase()}`}
                onTrigger={() => void triggerDatasetLLM(config.trigger, config.label)}
                onTogglePinned={() => setModelingMonitors(current => ({
                  DIM_MODELING: { ...current.DIM_MODELING, logsPinned: config.trigger === 'DIM_MODELING' && !current[config.trigger].logsPinned },
                  DWD_MODELING: { ...current.DWD_MODELING, logsPinned: config.trigger === 'DWD_MODELING' && !current[config.trigger].logsPinned },
                  DWS_MODELING: { ...current.DWS_MODELING, logsPinned: config.trigger === 'DWS_MODELING' && !current[config.trigger].logsPinned },
                }))}
              />)}
            </div>
          </section>
        </header>
        <div className="dataset-catalog-toolbar" aria-label="数据集筛选">
          <label className="dataset-search-field"><MagnifyingGlassIcon size={17} /><input aria-label="搜索数据集" type="search" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索名称、编码或说明" /></label>
          <label><span>分层</span><select aria-label="按数据集分层筛选" value={layerFilter} onChange={event => setLayerFilter(event.target.value as DatasetLayer | 'ALL')}><option value="ALL">全部分层</option>{layerOverview.map(item => <option value={item.layer} key={item.layer}>{item.layer} · {item.name}（{layerCounts[item.layer]}）</option>)}</select></label>
          <label><span>状态</span><select aria-label="按数据集状态筛选" value={statusFilter} onChange={event => setStatusFilter(event.target.value)}><option value="ALL">全部状态</option>{Object.entries(statusLabels).map(([status, label]) => <option key={status} value={status}>{label}</option>)}</select></label>
          <button className="dataset-filter-button" type="button" onClick={() => { setKeyword(''); setLayerFilter('ALL'); setStatusFilter('ALL') }}><FunnelIcon size={16} />重置筛选</button>
          <small>显示 {filtered.length} / {datasets.length}</small>
        </div>
      {!!selectedDatasets.length && <div className="dataset-bulk-toolbar" aria-label="数据集批量操作">
        <label><input ref={selectFilteredCheckbox} type="checkbox" checked={allFilteredSelected} disabled={actionBusy || !filtered.length} onChange={toggleFilteredSelection} /><span>选择当前结果</span></label>
        <strong>已选择 {selectedDatasets.length} 个</strong>
        <div>
          <button className="action-publish" type="button" disabled={actionBusy || !selectedDatasets.length} onClick={() => { setFormError(''); setBatchAction('publish') }}>批量提交发布申请</button>
          <button className="action-resume" type="button" disabled={actionBusy || !selectedRunnableCount} title="逐个锁定所选数据集的当前发布版本，执行一次完整替换入仓 DAG" onClick={() => { setFormError(''); setBatchAction('run') }}>批量运行</button>
          <button className="action-pause" type="button" disabled={actionBusy || !selectedActiveDAGCount} title="停止所选数据集中正在排队或执行的本次 DAG" onClick={() => { setFormError(''); setBatchAction('stop') }}>批量停止</button>
          <button className="action-delete" type="button" disabled={actionBusy || !selectedDatasets.length} onClick={() => { setFormError(''); setBatchAction('delete') }}>批量删除</button>
          <button className="quiet-button" type="button" disabled={actionBusy || !selectedDatasets.length} onClick={() => setSelectedDatasetIDs(new Set())}>清空选择</button>
        </div>
      </div>}
      <div className="dataset-catalog-head" aria-hidden="true"><span>数据集</span><span>分层 / 状态</span><span>版本与运行</span><span>最近更新</span><span>操作</span></div>
      {loading ? <Empty>正在加载数据集…</Empty> : !datasets.length ? <Empty title="还没有数据集">点击右上角“新建数据集”开始配置。</Empty> : !filtered.length ? <Empty title="没有符合条件的数据集">请调整搜索词或筛选条件。</Empty> :
        <div className="dataset-asset-list" role="list" aria-label="数据集资产清单">{filtered.map(dataset => <article key={dataset.id} role="listitem" className="dataset-asset-card">
          <label className="dataset-asset-select"><input type="checkbox" aria-label={`选择数据集 ${dataset.name}`} checked={selectedDatasetIDs.has(dataset.id)} disabled={actionBusy} onChange={() => toggleDatasetSelection(dataset.id)} /></label>
          <div className="dataset-asset-open" role="button" tabIndex={actionBusy ? -1 : 0} aria-disabled={actionBusy} aria-label={`打开数据集 ${dataset.name}`} onClick={() => { if (!actionBusy) void openView(dataset) }} onKeyDown={event => { if (!actionBusy && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); void openView(dataset) } }}>
            <div className="dataset-asset-icon" aria-hidden="true"><RowsIcon size={22} weight="duotone" /></div>
            <div className="dataset-asset-main"><div><h3>{dataset.name}</h3>{(dataset.tags || []).slice(0, 2).map(tag => <span className="dataset-asset-tag" key={tag}>{tag}</span>)}</div><p>{dataset.description || '暂无说明'}</p><small>{dataset.code}{dataset.originDataSourceName ? ` · ${dataset.originDataSourceName}` : ''}</small></div>
            <div className="dataset-catalog-state"><span className={`dataset-asset-layer ${dataset.layer.toLowerCase()}`}>{dataset.layer}</span><span className={`dataset-asset-status ${dataset.status.toLowerCase()}`}>{statusLabels[dataset.status] ?? dataset.status}</span></div>
            <div className={`dataset-catalog-version ${dagRuns[dataset.id]?.status.toLowerCase() || ''} ${dagRuns[dataset.id]?.slaBreached ? 'sla-breached' : ''}`}><strong>V{dataset.version}</strong><small>{dagRuns[dataset.id] ? dagRunLabel(dagRuns[dataset.id]) : dataset.currentPublishedVersionId ? '已冻结发布版本' : '草稿版本'}</small>{dagRuns[dataset.id]?.errorCode && <em>{dagRuns[dataset.id].errorCode}</em>}</div>
            <time>{new Date(dataset.updatedAt).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })}</time>
          </div>
          <div className="dataset-asset-actions">
            {datasetManagePermissions[dataset.id] && <button className="action-edit" type="button" disabled={actionBusy} onClick={() => void openEdit(dataset)}><TreeStructureIcon size={15} />建模</button>}
            {datasetManagePermissions[dataset.id] && dagRuns[dataset.id] && activeDAGRunStatuses.has(dagRuns[dataset.id].status)
              ? <button className="action-pause" type="button" disabled={actionBusy} title={`停止本次 DAG${dagRuns[dataset.id]?.status === 'QUEUED' ? '排队' : '执行'}`} onClick={() => void stopDatasetDAG(dataset)}><DropSlashIcon size={15} />停止</button>
              : datasetManagePermissions[dataset.id] && dataset.status === 'PUBLISHED' && dataset.currentPublishedVersionId && <button className={`action-resume ${isRetryableDAGRun(dagRuns[dataset.id]) ? 'is-retry' : ''}`} type="button" disabled={actionBusy} title="锁定当前发布版本并创建一次新的可审计运行" onClick={() => void runDatasetDAG(dataset)}><ArrowClockwiseIcon size={15} />{isRetryableDAGRun(dagRuns[dataset.id]) ? '重试' : '运行'}</button>}
            {dagRuns[dataset.id] && <button className="action-diagnose" type="button" disabled={actionBusy} onClick={() => void openMaterialization(dataset)}><WarningCircleIcon size={15} />诊断</button>}
            {dataset.status === 'PUBLISHED' && dataset.currentPublishedVersionId
              ? <button className="action-history" type="button" disabled={actionBusy} onClick={() => void openHistory(dataset)}><CalendarDotsIcon size={15} />版本</button>
              : <button className="action-publish" type="button" disabled={actionBusy || dataset.status === 'DISABLED' || dataset.status === 'DEPRECATED'} title="提交校验与发布审批" onClick={() => void openPublication(dataset)}><ArrowUpIcon size={15} />发布</button>}
            {datasetManagePermissions[dataset.id] && dataset.status === 'DISABLED'
              ? <button className="action-resume" type="button" disabled={actionBusy} onClick={() => void openLifecycle(dataset, 'restore')}><ArrowCounterClockwiseIcon size={15} />恢复</button>
              : datasetManagePermissions[dataset.id] && ['DRAFT', 'PUBLISHED', 'STALE'].includes(dataset.status) && <button className="action-pause" type="button" disabled={actionBusy} onClick={() => void openLifecycle(dataset, 'disable')}><DropSlashIcon size={15} />停用</button>}
            {datasetManagePermissions[dataset.id] && <button className="action-delete" type="button" disabled={actionBusy} onClick={() => void openLifecycle(dataset, 'delete')}><XIcon size={15} />删除</button>}
          </div>
        </article>)}</div>}
      </section>
    </section>

    {dialog?.mode === 'create' && <Dialog title={editingCanvas ? '修改数据集' : '新建数据集'} eyebrow="图形化配置" wide closeDisabled={aiApplying} onClose={closeDialog}>
      <DatasetDesignWorkspace
        ref={canvasFullscreenTarget}
        loading={assetsLoading}
        groups={sourceGroups}
        tables={tables}
        nodes={draft.nodes}
        isFullscreen={canvasFullscreen}
        relationCount={relationBoxes.length}
        groupCount={groupBoxes.length}
        transformCount={transformBoxes.length}
        hasEnd={Boolean(endBox)}
        notice={canvasNotice}
        onCanvasClick={closeCanvasEditor}
        onSelectTable={table => void selectTable(table)}
        assistant={<DatasetAIComposer prompt={aiPrompt} lastInstruction={aiLastInstruction} result={aiResult} progressLogs={aiProgressLogs} labels={aiReviewLabels} error={aiError} busy={aiBusy} applying={aiApplying} applied={aiApplied} detailsExpanded={aiDetailsExpanded} ready={!assetsLoading && !busyAction} hasAssets={tables.length > 0} canUndo={Boolean(aiUndo)} canRetry={Boolean(aiRetryAction)} retryRequiresGeneration={aiRetryAction === 'GENERATE'} hasGraph={draft.nodes.length > 0} onPromptChange={setAIPrompt} onSubmit={() => void generateDatasetAIPlan()} onApply={() => void applyDatasetAIPlan()} onUndo={undoDatasetAIPlan} onRetryOriginal={() => retryDatasetAI('ORIGINAL')} onRetryModified={() => retryDatasetAI('MODIFIED')} onDismissError={dismissDatasetAIError} onDetailsExpandedChange={setAIDetailsExpanded} />}
        canvas={<RelationCanvas nodes={draft.nodes} fields={draft.fields} joins={draft.joins} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} end={endBox} nodePositions={nodePositions} activeNodeID={activeNodeID} activeJoinID={activeJoinID} activeGroupID={activeGroupID} activeTransformID={activeTransformID} activeEnd={activeEnd} tables={tables} isFullscreen={canvasFullscreen} previewTarget={canvasPreviewTarget} preview={canvasPreview} previewLabel={canvasPreviewLabel} onPreview={openCanvasPreview} onRefreshPreview={() => { if (canvasPreviewTarget) openCanvasPreview(canvasPreviewTarget) }} onClosePreview={() => setCanvasPreviewTarget(null)} onArrange={arrangeCanvas} onToggleFullscreen={() => void toggleCanvasFullscreen()} onAddJoin={addRelationBox} onAddGroup={addGroupBox} onAddTransform={addTransformBox} onAddEnd={addEndBox} onAddTable={(table, position) => void selectTable(table, position)} onMove={updateCanvasPosition} onConnect={dropRelationInput} onConnectGroup={connectGroupInput} onConnectTransform={connectTransformInput} onConnectEnd={connectEndInput} onRemoveBox={removeRelationBox} onRemoveGroup={removeGroupBox} onRemoveTransform={removeTransformBox} onRemoveEnd={removeEndBox} onNodeClick={openNodeConfig} onJoinClick={joinID => { setActiveNodeID(''); setActiveGroupID(''); setActiveTransformID(''); setActiveEnd(false); setActiveJoinID(joinID); setCanvasNotice('') }} onGroupClick={groupID => { setActiveNodeID(''); setActiveJoinID(''); setActiveTransformID(''); setActiveEnd(false); setActiveGroupID(groupID); setCanvasNotice('') }} onTransformClick={transformID => { setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveEnd(false); setActiveTransformID(transformID); setCanvasNotice('') }} onEndClick={() => { setActiveNodeID(''); setActiveJoinID(''); setActiveGroupID(''); setActiveTransformID(''); setActiveEnd(true); setCanvasNotice('') }} onRemoveNode={removeNode} />}
        panels={<>
          {activeNode && <NodeConfigDrawer node={activeNode} fields={draft.fields.filter(field => field.key.startsWith(`${activeNode.id}.`))} onFieldPatch={updateOutputField} onDone={closeCanvasEditor} />}
          {activeRelationBox && <JoinConfigDrawer box={activeRelationBox} join={activeJoin} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} nodes={draft.nodes} leftOutputFields={activeLeftOutputFields} rightOutputFields={activeRightOutputFields} onNameChange={name => setRelationBoxes(current => current.map(box => box.id === activeRelationBox.id ? { ...box, name } : box))} onJoinPatch={patch => activeJoin && updateJoin(activeJoin.id, { ...patch, manualConfirmed: false })} onConditionPatch={(conditionID, patch) => activeJoin && updateJoinCondition(activeJoin.id, conditionID, patch)} onAddCondition={() => activeJoin && addJoinCondition(activeJoin.id)} onRemoveCondition={conditionID => activeJoin && removeJoinCondition(activeJoin.id, conditionID)} onOutputChange={(key, checked) => updateRelationOutput(activeRelationBox.id, key, checked)} onDone={closeCanvasEditor} />}
          {activeGroup && <GroupingConfigDrawer box={activeGroup} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} nodes={draft.nodes} availableFields={groupInputFields} error={formError} onNameChange={name => updateGroupName(activeGroup.id, name)} onGroupByModeChange={mode => updateGroupByMode(activeGroup.id, mode)} onGroupingSetsChange={groupingSets => updateGroupingSets(activeGroup.id, groupingSets)} onDimensionsChange={fields => updateGroupDimensions(activeGroup.id, fields)} onMetricsChange={selections => updateGroupMetrics(activeGroup.id, groupInputFields, selections)} onDone={closeCanvasEditor} />}
          {activeTransform && <TransformConfigDrawer transform={activeTransform} inputs={transformInputFields} nodes={draft.nodes} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} error={formError} onNameChange={name => updateTransformName(activeTransform.id, name)} onRuleChange={(ruleID, patch) => updateTransformRule(activeTransform.id, ruleID, patch)} onAddRule={() => addTransformRule(activeTransform.id)} onRemoveRule={ruleID => removeTransformRule(activeTransform.id, ruleID)} onFilterConditionChange={(conditionID, patch) => updateFilterCondition(activeTransform.id, conditionID, patch)} onAddFilterCondition={() => addFilterCondition(activeTransform.id)} onRemoveFilterCondition={conditionID => removeFilterCondition(activeTransform.id, conditionID)} onDone={closeCanvasEditor} />}
          {activeEnd && endBox && <EndConfigDrawer end={endBox} boxes={relationBoxes} groups={groupBoxes} transforms={transformBoxes} nodes={draft.nodes} availableFields={endInputFields} onNameChange={name => setEndBox(current => current ? { ...current, name } : current)} onOutputChange={updateEndOutput} onDone={closeCanvasEditor} />}
        </>}
        feedback={formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}
      />
      <footer className="dataset-dialog-footer"><button className="quiet-button" type="button" disabled={actionBusy || aiApplying} onClick={closeDialog}>取消</button><button className="primary-button" type="button" disabled={actionBusy || assetsLoading || aiBusy || aiApplying} onClick={openMetadata}>{busyAction.startsWith('asset:') ? '正在填充…' : aiBusy ? '正在生成 AI 方案…' : aiApplying ? '正在应用 AI 方案…' : '保存配置'}</button></footer>
    </Dialog>}

    {dialog?.mode === 'metadata' && <Dialog title={editingRecord ? '保存数据集修改' : '完善数据集信息'} eyebrow="保存配置" onClose={() => { if (!busyAction) setDialog({ mode: 'create' }) }}>
      <div className="dataset-metadata-form">
        <p>图形化配置已完成，请确认物理落层，并补充数据集的主题、名称和说明后保存。业务领域自动继承当前用户所属领域。</p>
        <label>
          数据集层级
          <select
            aria-label="数据集层级"
            value={layerChoices.includes(draft.layer as DatasetLayer) ? draft.layer : layerChoices[0]}
            disabled={Boolean(editingRecord?.currentPublishedVersionId)}
            onChange={event => setDraft(current => ({ ...current, layer: event.target.value as DatasetLayer }))}
          >
            {datasetLayers.map(layer => <option key={layer} value={layer} disabled={!layerChoices.includes(layer)}>
              {datasetLayerLabels[layer]}{layerChoices.includes(layer) ? '' : '（当前血缘不可用）'}
            </option>)}
          </select>
          <small>完整展示五个数仓层级；单个物理数据集选择一个落地层级，系统按当前上游血缘校验可用范围。</small>
        </label>
        <div className="dataset-metadata-grid">
          <label>
            业务领域（当前用户所属）
            <input
              aria-label="业务领域"
              readOnly
              value={selectedBusinessDomainName}
              placeholder="请先选择业务领域"
            />
            <small>由登录用户当前选择的所属领域统一确定，不参与 LLM 标签生成。</small>
          </label>
          <label>
            业务主题（可选）
            <input
              aria-label="业务主题"
              autoComplete="off"
              list="dataset-subject-options"
              maxLength={128}
              value={metadata.subject}
              onChange={event => setMetadata(current => ({ ...current, subject: event.target.value }))}
              placeholder="例如：企业画像、经营分析"
            />
            <datalist id="dataset-subject-options">{classificationSuggestions.subjects.map(value => <option key={value} value={value} />)}</datalist>
          </label>
        </div>
        <label>
          数据集名称
          <input autoFocus value={metadata.name} onChange={event => setMetadata(current => ({ ...current, name: event.target.value }))} placeholder="例如：客户订单明细" />
        </label>
        <label>
          数据集说明
          <textarea value={metadata.description} onChange={event => setMetadata(current => ({ ...current, description: event.target.value }))} placeholder="说明数据范围、业务口径和使用场景" />
        </label>
        <small>{editingRecord ? `数据集编码保持不变：${generatedCode}` : `系统将自动生成唯一编码：${generatedCode}`}</small>
        {formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}
        {draftConflict && editingRecord && <section className="dataset-draft-conflict" aria-label="草稿版本冲突恢复">
          <div><WarningCircleIcon size={20} weight="fill" /><span><strong>检测到更新后的协作草稿</strong><small>你打开时为 V{editingRecord.version}，服务端当前为 V{draftConflict.currentVersion ?? '更新版本'}{draftConflict.currentHash ? ` · ${draftConflict.currentHash.slice(0, 10)}…` : ''}。当前表单未被覆盖。</small></span></div>
          <button type="button" disabled={actionBusy} onClick={() => void openEdit(editingRecord.id)}>加载最新草稿</button>
        </section>}
        <footer>
          <button className="quiet-button" type="button" disabled={actionBusy} onClick={() => setDialog({ mode: 'create' })}>返回配置</button>
          <button className="primary-button" type="button" disabled={actionBusy} onClick={() => void saveDataset()}>{busyAction === 'update' ? '正在校正语义并保存…' : busyAction === 'create' ? '正在校正语义并创建…' : editingRecord ? '保存修改' : '创建数据集'}</button>
        </footer>
      </div>
    </Dialog>}

    {dialog?.mode === 'view' && dialog.dataset && <Dialog title="数据集详情" eyebrow="资产信息" wide onClose={closeDialog}>
      {busyAction.startsWith('view:') ? <Empty>正在加载完整元数据与预览数据…</Empty> : detail ? <div className="dataset-detail">
        <header>
          <div><strong>{detail.name}</strong><span className={`dataset-asset-status ${detail.status.toLowerCase()}`}>{statusLabels[detail.status] ?? detail.status}</span><span className={`dataset-asset-layer ${detail.layer.toLowerCase()}`}>{detail.layer}</span>{(detail.tags || []).map(tag => <span className="dataset-asset-tag" key={tag}>{tag}</span>)}</div>
          <p>{detail.description || '暂无说明'}</p>
        </header>
        <dl><div><dt>编码</dt><dd>{detail.code}</dd></div><div><dt>类型</dt><dd>{typeLabels[detail.type] ?? detail.type}</dd></div><div><dt>业务领域</dt><dd>{selectedBusinessDomainName || '未配置'}</dd></div><div><dt>共享范围</dt><dd><AssetSharingSelect resourceType="DATASET" resourceID={detail.id} value={detail.sharingScope ?? 'PRIVATE'} ownerUserID={detail.ownerUserId} assetDomainID={detail.domainId} disabled={actionBusy} onChange={sharingScope => { setDetail(current => current ? { ...current, sharingScope } : current); setDatasets(current => current.map(item => item.id === detail.id ? { ...item, sharingScope } : item)) }} /></dd></div><div><dt>业务主题</dt><dd>{detail.dsl.dataset.subject || '未配置'}</dd></div><div><dt>聚合版本</dt><dd>V{detail.version}</dd></div><div><dt>草稿版本</dt><dd>V{detail.draftVersionNo}</dd></div><div><dt>数据节点</dt><dd>{Array.isArray(detail.dsl.nodes) ? detail.dsl.nodes.length : 0}</dd></div><div><dt>输出字段</dt><dd>{completeDetailFields.length}</dd></div></dl>
        <section className="dataset-detail-metadata" aria-label="LLM 生成的完整元数据">
          <div className="dataset-detail-section-heading"><div><span className="eyebrow">LLM 元数据</span><h3>完整业务语义</h3></div><span>{detailAsset ? `${detailAsset.schemaName}.${detailAsset.tableName}` : `${detail.layer} 数据集`}</span></div>
          {detailAsset && <dl className="dataset-detail-table-summary">
            <div><dt>物理表</dt><dd>{[detailAsset.catalogName, detailAsset.schemaName, detailAsset.tableName].filter(Boolean).join('.')}</dd></div>
            <div><dt>业务名称</dt><dd>{detailAsset.businessName || '—'}</dd></div>
            <div className="wide"><dt>业务说明</dt><dd>{detailAsset.businessDescription || '—'}</dd></div>
            <div><dt>敏感级别</dt><dd>{detailAsset.sensitivityLevel || '—'}</dd></div>
            <div><dt>可视状态</dt><dd>{detailAsset.visibility === 'TENANT_PUBLIC' ? '领域可见' : '仅授权可见'}</dd></div>
            <div><dt>完善状态</dt><dd>{detailAsset.enrichmentStatus === 'SUCCEEDED' ? 'LLM 已完善' : detailAsset.enrichmentStatus || '—'}</dd></div>
          </dl>}
          <div className="dataset-detail-tag-list" aria-label="数据集标签"><strong>标签</strong>{(detail.tags || []).length > 0 ? (detail.tags || []).map(tag => <span key={tag}>{tag}</span>) : <small>暂无数据集标签</small>}</div>
          <div className="dataset-detail-field-scroll">
            {detailAsset ? <table><thead><tr><th>#</th><th>物理字段</th><th>LLM 业务字段</th><th>业务说明</th><th>类型 / 语义</th><th>标签</th><th>可视状态</th></tr></thead><tbody>{detailAssetColumns.map((column, index) => <tr key={column.id}><td>{column.ordinalPosition ?? index + 1}</td><td><strong>{column.columnName}</strong><small>{column.nativeType || column.canonicalType}</small></td><td><strong>{column.businessName || '—'}</strong></td><td>{column.businessDescription || '—'}</td><td><strong>{column.canonicalType}</strong><small>{column.semanticType || '未识别'}</small></td><td><div className="dataset-detail-field-tags">{column.tags?.length ? column.tags.map(tag => <span key={tag}>{tag}</span>) : '—'}</div></td><td>{column.assetStatus === 'ACTIVE' ? '可见' : '不可见'}<small>{column.sensitivityLevel || ''}</small></td></tr>)}</tbody></table>
              : <table><thead><tr><th>#</th><th>字段编码</th><th>业务名称</th><th>业务说明</th><th>类型 / 语义</th><th>角色</th><th>可视状态</th></tr></thead><tbody>{completeDetailFields.map((field, index) => <tr key={field.id}><td>{index + 1}</td><td><strong>{field.code}</strong>{field.physicalName && <small>{field.physicalName}</small>}</td><td><strong>{field.name}</strong></td><td>{field.description || '—'}</td><td><strong>{field.canonicalType || '—'}</strong><small>{field.semanticType || '未识别'}</small></td><td>{field.role || '—'}</td><td>{field.visible ? '可见' : '不可见'}<small>{field.nullable ? '可空' : '必填'}</small></td></tr>)}</tbody></table>}
          </div>
        </section>
        <section className="dataset-detail-preview" aria-label="当前草稿 DAG 演示结果"><div><h3>当前草稿 DAG 结果</h3><span>前 10 行 · 仅演示，不入仓</span></div>{detailPreview ? <PreviewRows preview={detailPreview} /> : <div className="dataset-center-feedback error" role="alert">{detailPreviewError || '暂无可预览数据'}</div>}</section>
        <footer><button className="quiet-button" type="button" onClick={closeDialog}>关闭</button><button className="quiet-button" type="button" onClick={openMetadataEdit}>修改元信息</button><button className="primary-button" type="button" onClick={() => { setDialog(null); void openEdit(detail) }}>修改 DAG</button></footer>
      </div> : <div className="dataset-center-feedback error" role="alert">{formError}</div>}
    </Dialog>}

    {dialog?.mode === 'edit-metadata' && dialog.dataset && detail && metadataEdit && <Dialog
      title={`${detail.name} · 修改元信息`}
      eyebrow="仅修改业务语义，不改变 DAG、字段编码和逻辑类型"
      wide
      onClose={closeDialog}
    >
      <div className="dataset-metadata-form dataset-metadata-editor">
        <div className="dataset-metadata-grid">
          <label>数据集名称<input autoFocus value={metadataEdit.name} onChange={event => setMetadataEdit(current => current ? { ...current, name: event.target.value } : current)} /></label>
          <label>业务主题<input value={metadataEdit.subject} onChange={event => setMetadataEdit(current => current ? { ...current, subject: event.target.value } : current)} /></label>
        </div>
        <label>数据集说明<textarea value={metadataEdit.description} onChange={event => setMetadataEdit(current => current ? { ...current, description: event.target.value } : current)} /></label>
        <section>
          <div className="dataset-detail-section-heading"><div><span className="eyebrow">字段元信息</span><h3>业务名称、说明与语义</h3></div><span>编码和类型只读</span></div>
          <div className="dataset-metadata-field-scroll">
            <table>
              <thead><tr><th>字段编码 / 类型</th><th>业务名称</th><th>业务说明</th><th>角色</th><th>语义类型</th><th>约束</th></tr></thead>
              <tbody>{metadataEdit.fields.map(field => <tr key={field.id}>
                <td><strong>{field.code}</strong><small>{field.canonicalType || '—'}</small></td>
                <td><input aria-label={`${field.code}业务名称`} value={field.name} onChange={event => updateMetadataField(field.id, { name: event.target.value })} /></td>
                <td><textarea aria-label={`${field.code}业务说明`} value={field.description} onChange={event => updateMetadataField(field.id, { description: event.target.value })} /></td>
                <td><select aria-label={`${field.code}角色`} value={field.role} onChange={event => updateMetadataField(field.id, { role: event.target.value })}>{['IDENTIFIER', 'DIMENSION', 'ATTRIBUTE', 'TIME', 'MEASURE'].map(role => <option key={role} value={role}>{role}</option>)}</select></td>
                <td><input aria-label={`${field.code}语义类型`} value={field.semanticType} onChange={event => updateMetadataField(field.id, { semanticType: event.target.value })} /></td>
                <td><label><input type="checkbox" checked={field.nullable} onChange={event => updateMetadataField(field.id, { nullable: event.target.checked })} />可空</label><label><input type="checkbox" checked={field.visible} onChange={event => updateMetadataField(field.id, { visible: event.target.checked })} />可见</label></td>
              </tr>)}</tbody>
            </table>
          </div>
        </section>
        {formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}
        <footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={() => setDialog({ mode: 'view', dataset: dialog.dataset })}>取消</button><button className="primary-button" type="button" disabled={actionBusy} onClick={() => void saveMetadataEdit()}>{busyAction === 'metadata-update' ? '正在保存…' : '保存元信息'}</button></footer>
      </div>
    </Dialog>}

    {dialog?.mode === 'history' && dialog.dataset && <Dialog title={`${dialog.dataset.name} · 历史版本`} eyebrow="发布快照、差异影响与安全回滚" wide onClose={closeDialog}><PublishedVersionHistoryPanel record={historyRecord} items={historyItems} selected={selectedHistoryVersion} usage={historyUsage} preview={historyPreview} loading={busyAction.startsWith('history:') || busyAction.startsWith('version:')} busy={actionBusy} confirming={historyConfirm} error={formError} onSelect={versionID => void selectHistoryVersion(versionID)} onStartRollback={() => setHistoryConfirm(true)} onCancelRollback={() => { setHistoryConfirm(false); setFormError('') }} onRollback={() => void rollbackHistoryVersion()} onClose={closeDialog} /></Dialog>}

    {dialog?.mode === 'materialization' && dialog.dataset && <Dialog title={`${dialog.dataset.name} · 物化诊断`} eyebrow="运行、质量与刷新 SLA" wide onClose={closeDialog}>
      <MaterializationRunPanel dataset={dialog.dataset} run={materializationDetail} loading={busyAction.startsWith('dag-detail:')} busy={actionBusy} error={formError} onRetry={() => { const dataset = dialog.dataset!; closeDialog(); void runDatasetDAG(dataset) }} onStop={() => { const dataset = dialog.dataset!; closeDialog(); void stopDatasetDAG(dataset) }} onClose={closeDialog} />
    </Dialog>}

    {dialog?.mode === 'lifecycle' && dialog.dataset && <Dialog
      title={`${dialog.lifecycleAction === 'delete' ? '删除' : dialog.lifecycleAction === 'restore' ? '恢复' : '停用'}数据集`}
      eyebrow="生命周期与下游依赖保护"
      onClose={closeDialog}
    ><DatasetLifecyclePanel dataset={dialog.dataset} action={dialog.lifecycleAction || 'disable'} impact={lifecycleImpact} loading={busyAction.startsWith('lifecycle-impact:')} busy={actionBusy} error={formError} onConfirm={() => void executeLifecycle()} onClose={closeDialog} /></Dialog>}

    {dialog?.mode === 'publish' && dialog.dataset && <Dialog
      title={`${dialog.dataset.name} · 发布`}
      eyebrow="发布申请与审批"
      wide
      onClose={closeDialog}
    >
      <div className="dataset-publication">
        {busyAction.startsWith('publication:') ? <Empty>正在加载发布信息…</Empty> : publicationRecord ? <>
          <section className="dataset-publication-current" aria-label="当前发布候选">
            <div><span>当前草稿</span><strong>草稿 V{publicationRecord.draftVersionNo}</strong><small>{publicationRecord.dslHash.slice(0, 12)}…</small></div>
            <div><span>数据集聚合版本</span><strong>V{publicationRecord.version}</strong><small>提交时会冻结当前精确版本</small></div>
            <div><span>当前草稿审批</span><strong className={currentDraftPublicationRequest?.status.toLowerCase()}>{currentDraftPublicationRequest ? publicationStatusLabels[currentDraftPublicationRequest.status] : '未提交'}</strong><small>{currentDraftPublicationRequest?.publishedVersionId ? `已发布版本 ${currentDraftPublicationRequest.publishedVersionId} · 后台加工已启动` : currentDraftPublicationRequest ? '等待审批；审批前不会启动加工' : '尚未提交发布申请'}</small></div>
          </section>

          <div className="dataset-publication-body">
            <section className="dataset-publication-submit" aria-label="提交发布申请">
              <header><div><span>申请人操作</span><h3>提交当前草稿</h3></div><small>{publicationCapabilities.manage ? '具备提交权限' : '仅可查看'}</small></header>
              <p>系统只冻结当前草稿版本、DSL 与校验参数。审批通过后才生成不可变发布版本，并启动指标提取或 DIM/DWD/DWS/ADS PostgreSQL 加工。</p>
              <label>申请说明（选填）<textarea value={publicationNote} onChange={event => setPublicationNote(event.target.value)} placeholder="例如：订单与客户区域关联已由 AI 完成，请审批用于指标设计" /></label>
              {currentDraftPublicationRequest?.status === 'PENDING' && <div className="dataset-publication-hint">当前精确草稿已经在审批中，无需重复提交。</div>}
              {currentDraftPublicationRequest?.status === 'APPROVED' && <div className="dataset-publication-hint success">当前精确草稿已审批发布。再次修改并保存后可提交新的审批。</div>}
              {publicationRequests[0]?.status === 'CANCELLED' && !currentDraftPublicationRequest && <div className="dataset-publication-hint">上次申请已因草稿变更自动取消；当前草稿可重新提交审批。</div>}
              <div className="dataset-publication-submit-actions">{currentDraftPublicationRequest?.status === 'PENDING' && <button className="quiet-button" type="button" disabled={actionBusy || !publicationCapabilities.manage} onClick={() => void withdrawPublicationRequest()}>{busyAction === 'publication-withdraw' ? '正在撤回…' : '撤回申请'}</button>}<button className="primary-button" type="button" disabled={actionBusy || !publicationCapabilities.manage || currentDraftPublicationRequest?.status === 'APPROVED' || currentDraftPublicationRequest?.status === 'PENDING'} onClick={() => void submitPublicationRequest()}>{busyAction === 'publication-submit' ? '正在提交申请…' : '提交发布申请'}</button></div>
            </section>

            <section className="dataset-publication-review" aria-label="审批发布申请">
              <header><div><span>审批人操作</span><h3>审批并发布</h3></div><small>{publicationCapabilities.publish ? '具备审批权限' : '仅可查看'}</small></header>
              {!publicationRequests.length ? <div className="dataset-publication-empty">暂无发布申请</div> : <>
                <label>选择申请<select aria-label="选择发布申请" value={selectedPublicationRequestID} onChange={event => { setSelectedPublicationRequestID(event.target.value); setPublicationDecisionNote(''); setFormError('') }}>{publicationRequests.map(request => <option key={request.id} value={request.id}>{publicationStatusLabels[request.status]} · 草稿记录 V{request.expectedDraftRecordVersion} · {new Date(request.submittedAt).toLocaleString('zh-CN', { hour12: false })}</option>)}</select></label>
                {selectedPublicationRequest && <dl>
                  <div><dt>申请状态</dt><dd><span className={`dataset-publication-status ${selectedPublicationRequest.status.toLowerCase()}`}>{publicationStatusLabels[selectedPublicationRequest.status]}</span></dd></div>
                  <div><dt>冻结草稿</dt><dd>{selectedPublicationRequest.draftVersionId}</dd></div>
                  <div><dt>DSL 摘要</dt><dd>{selectedPublicationRequest.expectedDslHash.slice(0, 16)}…</dd></div>
                  <div><dt>加工策略</dt><dd>{selectedPublicationRequest.status === 'PENDING' ? '审批通过后启动' : '已按发布版本执行'}</dd></div>
                  <div><dt>申请说明</dt><dd>{selectedPublicationRequest.requestNote || '未填写'}</dd></div>
                  {selectedPublicationRequest.reviewNote && <div><dt>审批意见</dt><dd>{selectedPublicationRequest.reviewNote}</dd></div>}
                  {selectedPublicationRequest.publishedVersionId && <div><dt>发布版本</dt><dd>{selectedPublicationRequest.publishedVersionId}</dd></div>}
                </dl>}
                {selectedPublicationRequest?.status === 'PENDING' && <>
                  <label>审批意见<textarea value={publicationDecisionNote} onChange={event => setPublicationDecisionNote(event.target.value)} placeholder="通过时可选；拒绝时必须说明原因" /></label>
                  <div className="dataset-publication-review-actions"><button className="dataset-publication-reject" type="button" disabled={actionBusy || !publicationCapabilities.publish || !publicationDecisionNote.trim()} onClick={() => void rejectPublicationRequest()}>{busyAction === 'publication-reject' ? '正在拒绝…' : '拒绝'}</button><button className="primary-button" type="button" disabled={actionBusy || !publicationCapabilities.publish} onClick={() => void approvePublicationRequest()}>{busyAction === 'publication-approve' ? '正在批准并登记加工…' : '审批通过并启动加工'}</button></div>
                </>}
              </>}
            </section>
          </div>

          {formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}
          <footer className="dataset-publication-footer"><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>关闭</button></footer>
        </> : <div className="dataset-center-feedback error" role="alert">{formError || '无法加载数据集发布信息'}</div>}
      </div>
    </Dialog>}

    {batchAction && <Dialog
      title={batchAction === 'publish' ? '批量提交发布申请' : batchAction === 'run' ? '批量运行 DAG' : batchAction === 'stop' ? '批量停止 DAG' : '批量删除数据集'}
      eyebrow={batchAction === 'delete' ? '危险操作' : '批量操作'}
      onClose={closeBatchDialog}
    ><div className="dataset-delete-confirm">
      <p>确认对已选择的 <strong>{batchAction === 'run' ? selectedRunnableCount : batchAction === 'stop' ? selectedActiveDAGCount : selectedDatasets.length}</strong> 个{batchAction === 'stop' ? '活动 DAG' : '数据集'}执行{batchAction === 'publish' ? '发布申请' : batchAction === 'run' ? '运行' : batchAction === 'stop' ? '停止' : '删除'}吗？</p>
      <small>{batchAction === 'publish'
        ? '系统会按当前精确草稿逐条提交审批申请，不会绕过审批直接上线。'
        : batchAction === 'run'
          ? '只运行每个数据集的当前已发布版本；每次均重建空白目标后完整写入，不会追加旧数据，草稿修改不会进入数仓。'
        : batchAction === 'stop'
          ? '仅停止当前排队或执行中的这一次 DAG，不改变数据集发布状态和历史版本。'
          : '系统会逐条检查下游数据集、构建任务和运行中查询引用；被占用的数据集会保留并返回失败原因。'}</small>
      {formError && <div className="dataset-center-feedback error" role="alert">{formError}</div>}
      <footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeBatchDialog}>取消</button><button className={batchAction === 'delete' ? 'dataset-delete-button' : 'primary-button'} type="button" disabled={actionBusy || (batchAction === 'run' ? !selectedRunnableCount : batchAction === 'stop' ? !selectedActiveDAGCount : !selectedDatasets.length)} onClick={() => void executeBatchAction()}>{actionBusy ? '正在处理…' : '确认执行'}</button></footer>
    </div></Dialog>}

  </AppShell>
}

function DatasetAIComposer({ prompt, lastInstruction, result, progressLogs, labels, error, busy, applying, applied, detailsExpanded, ready, hasAssets, canUndo, canRetry, retryRequiresGeneration, hasGraph, onPromptChange, onSubmit, onApply, onUndo, onRetryOriginal, onRetryModified, onDismissError, onDetailsExpandedChange }: {
  prompt: string
  lastInstruction: string
  result: DatasetAIPlanResult | null
  progressLogs: DatasetAIProgressEvent[]
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
  const joinMeaning = (joinType: 'INNER' | 'LEFT' | 'RIGHT' | 'FULL') => ({
    INNER: '仅保留两侧匹配数据 · 一对一',
    LEFT: '保留左侧全部数据 · 多对一',
    RIGHT: '保留右侧全部数据 · 一对多',
    FULL: '保留两侧全部数据 · 多对多',
  })[joinType]
  useLayoutEffect(() => {
    const textarea = promptRef.current
    if (!textarea) return
    textarea.style.height = '0px'
    textarea.style.height = `${Math.min(Math.max(textarea.scrollHeight, 28), 128)}px`
  }, [prompt])
  return <DatasetAIDock hasProposal={Boolean(proposal)}>
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
    {busy && <DatasetAIGenerationProgress logs={progressLogs} />}
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
  </DatasetAIDock>
}

function DatasetAIGenerationProgress({ logs }: { logs: DatasetAIProgressEvent[] }) {
  const statusLabel = (status: DatasetAIProgressEvent['status']) => status === 'SUCCEEDED' ? '已完成' : status === 'WARN' ? '校正中' : '处理中'
  return <div className="dataset-ai-progress" role="status" aria-live="polite">
    <MagicWandIcon aria-hidden="true" size={18} weight="duotone" />
    <div className="dataset-ai-progress-copy">
      <p><strong>正在理解业务目标并规划 DAG</strong><small>以下进度由后端生成；只读取表和字段业务元数据，不发送数据样例。</small></p>
      <ol className="dataset-ai-progress-log" role="log" aria-label="AI 生成日志">
        {!logs.length && <li className="is-active is-placeholder"><time aria-hidden="true">--:--:--</time><span>连接中</span><strong>正在建立后端进度通道…</strong></li>}
        {logs.map((entry, index) => <li className={`${entry.status === 'RUNNING' ? 'is-active' : ''} ${entry.status === 'WARN' ? 'is-warning' : ''}`} key={`${entry.timestamp}:${entry.stage}:${index}`}>
          <time>{new Date(entry.timestamp).toLocaleTimeString('zh-CN', { hour12: false })}</time>
          <span>{statusLabel(entry.status)}</span>
          <strong>{entry.message}</strong>
        </li>)}
      </ol>
    </div>
  </div>
}

function RelationCanvas({ nodes, fields, joins, boxes, groups, transforms, end, nodePositions, activeNodeID, activeJoinID, activeGroupID, activeTransformID, activeEnd, tables, isFullscreen, previewTarget, preview, previewLabel, onPreview, onRefreshPreview, onClosePreview, onArrange, onToggleFullscreen, onAddJoin, onAddGroup, onAddTransform, onAddEnd, onAddTable, onMove, onConnect, onConnectGroup, onConnectTransform, onConnectEnd, onRemoveBox, onRemoveGroup, onRemoveTransform, onRemoveEnd, onNodeClick, onJoinClick, onGroupClick, onTransformClick, onEndClick, onRemoveNode }: {
  nodes: DesignerNode[]; fields: FieldOption[]; joins: JoinOption[]; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; end: EndBox | null; nodePositions: Record<string, CanvasPoint>
  activeNodeID: string; activeJoinID: string; activeGroupID: string; activeTransformID: string; activeEnd: boolean; tables: AssetTable[]
  isFullscreen: boolean; previewTarget: CanvasPreviewTarget | null; preview?: NodePreviewState; previewLabel: string
  onPreview: (target: CanvasPreviewTarget) => void; onRefreshPreview: () => void; onClosePreview: () => void; onArrange: () => void; onToggleFullscreen: () => void
  onAddJoin: (position?: CanvasPoint, input?: RelationInput) => string; onAddGroup: (position?: CanvasPoint, input?: RelationInput) => string; onAddTransform: (componentType: GraphTransformComponentType, position?: CanvasPoint, input?: RelationInput) => string | undefined; onAddEnd: (position?: CanvasPoint) => void; onAddTable: (table: AssetTable, position: CanvasPoint) => void
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
  const [openEdgeMenuKey, setOpenEdgeMenuKey] = useState('')
  const [pendingEdgeInsertion, setPendingEdgeInsertion] = useState<PendingEdgeInsertion | null>(null)
  const canvasRef = useRef<HTMLDivElement>(null)
  const lineLayerRef = useRef<SVGSVGElement>(null)
  const connectionPointerIDRef = useRef<number | null>(null)
  const edgeInsertionInFlightRef = useRef<PendingEdgeInsertion | null>(null)
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
        // output-port 是贴在组件右侧内缘的半圆端口，连线仍从卡片右边缘发出。
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
  useEffect(() => {
    if (!pendingEdgeInsertion) return
    const { inserted, source, target } = pendingEdgeInsertion
    const ready = inserted.kind === 'JOIN'
      ? boxes.some(box => box.id === inserted.id && box.left?.kind === source.kind && box.left.id === source.id)
      : inserted.kind === 'GROUP'
      ? groups.some(group => group.id === inserted.id && group.input?.kind === source.kind && group.input.id === source.id)
      : inserted.kind === 'TRANSFORM'
        ? transforms.some(transform => transform.id === inserted.id && transform.input?.kind === source.kind && transform.input.id === source.id)
        : false
    if (!ready || edgeInsertionInFlightRef.current === pendingEdgeInsertion) return
    // 连接回调会改写父组件状态并重建函数引用；用 ref 标记已消费的事务，
    // 再在同一个微任务中清除 pending 与改写下游，避免 effect 重复连接。
    edgeInsertionInFlightRef.current = pendingEdgeInsertion
    queueMicrotask(() => {
      setPendingEdgeInsertion(current => current === pendingEdgeInsertion ? null : current)
      if (target.kind === 'JOIN') onConnect(target.id, target.side, inserted)
      else if (target.kind === 'GROUP') onConnectGroup(target.id, inserted)
      else if (target.kind === 'TRANSFORM') onConnectTransform(target.id, inserted)
      else onConnectEnd(inserted)
      edgeInsertionInFlightRef.current = null
    })
  }, [boxes, groups, onConnect, onConnectEnd, onConnectGroup, onConnectTransform, pendingEdgeInsertion, transforms])
  useEffect(() => {
    if (!openEdgeMenuKey) return
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpenEdgeMenuKey('')
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [openEdgeMenuKey])
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
  type CanvasEdge = {
    key: string
    source: RelationInput
    target: CanvasEdgeTarget
    targetLabel: string
    geometry: { path: string; deletePosition: CanvasPoint }
    deleteLabel: string
    remove: () => void
  }
  const edges: CanvasEdge[] = boxes.flatMap((box, boxIndex) => {
    const target = box.position ?? { x: 510, y: 150 }
    return ([box.left, box.right] as Array<RelationInput | undefined>).flatMap((input, slot) => {
      if (!input) return []
      const side = slot === 0 ? 'left' : 'right'
      const geometry = edge(input, `JOIN:${box.id}:${side}`, { x: target.x, y: target.y + (slot === 0 ? 43 : 82) })
      return geometry ? [{
        key: `${box.id}-${slot}`,
        source: input,
        target: { kind: 'JOIN' as const, id: box.id, side: side as 'left' | 'right' },
        targetLabel: `关联节点 ${boxIndex + 1} 槽位 ${slot + 1}`,
        geometry,
        deleteLabel: `删除关联节点 ${boxIndex + 1} 槽位 ${slot + 1} 连线`,
        remove: () => onConnect(box.id, side),
      }] : []
    })
  })
  for (const group of groups) if (group.input) {
    const geometry = edge(group.input, `GROUP:${group.id}:input`, { x: group.position.x, y: group.position.y + 58 })
    if (geometry) edges.push({ key: `${group.id}-input`, source: group.input, target: { kind: 'GROUP', id: group.id }, targetLabel: `“${group.name}”输入`, geometry, deleteLabel: `删除“${group.name}”输入连线`, remove: () => onConnectGroup(group.id) })
  }
  for (const transform of transforms) if (transform.input) {
    const geometry = edge(transform.input, `TRANSFORM:${transform.id}:input`, { x: transform.position.x, y: transform.position.y + 58 })
    if (geometry) edges.push({ key: `${transform.id}-input`, source: transform.input, target: { kind: 'TRANSFORM', id: transform.id }, targetLabel: `“${transform.name}”输入`, geometry, deleteLabel: `删除“${transform.name}”输入连线`, remove: () => onConnectTransform(transform.id) })
  }
  if (end?.input) {
    const geometry = edge(end.input, `END:${end.id}:input`, { x: end.position.x, y: end.position.y + 58 })
    if (geometry) edges.push({ key: 'end-input', source: end.input, target: { kind: 'END', id: end.id }, targetLabel: '结束节点输入', geometry, deleteLabel: '删除结束节点输入连线', remove: () => onConnectEnd() })
  }
  const insertComponentOnEdge = (item: CanvasEdge, selection: { kind: 'JOIN' } | { kind: 'GROUP' } | { kind: 'TRANSFORM'; componentType: GraphTransformComponentType }) => {
    const width = selection.kind === 'JOIN' ? 180 : selection.kind === 'GROUP' ? 190 : 200
    const position = {
      x: Math.max(16, item.geometry.deletePosition.x - width / 2),
      y: Math.max(20, item.geometry.deletePosition.y - 58),
    }
    const id = selection.kind === 'JOIN'
      ? onAddJoin(position, item.source)
      : selection.kind === 'GROUP'
        ? onAddGroup(position, item.source)
        : onAddTransform(selection.componentType, position, item.source)
    if (!id) return
    setOpenEdgeMenuKey('')
    setPendingEdgeInsertion({
      inserted: { kind: selection.kind, id },
      source: item.source,
      target: item.target,
    })
  }
  const edgeComponentPicker = (item: CanvasEdge) => {
    const sourceAlreadyGrouped = item.source.kind === 'NODE' && groups.some(group => group.input?.kind === 'NODE' && group.input.id === item.source.id)
    const groupDisabled = item.source.kind === 'GROUP' || item.target.kind === 'GROUP' || sourceAlreadyGrouped
    const sourceGroup = item.source.kind === 'GROUP' ? groups.find(group => group.id === item.source.id) : undefined
    const sourceGroupInput = sourceGroup?.input
    const sourceGroupCanFeedJoin = Boolean(sourceGroupInput
      && relationLeaves(sourceGroupInput, boxes, groups, transforms).length === 1
      && !boxes.some(join => relationContains(sourceGroupInput, { kind: 'JOIN', id: join.id }, boxes, groups, transforms))
      && !groups.some(group => group.id !== sourceGroup?.id && relationContains(sourceGroupInput, { kind: 'GROUP', id: group.id }, boxes, groups, transforms)))
    const joinDisabled = Boolean(sourceGroup && !sourceGroupCanFeedJoin)
    const groupDescription = item.source.kind === 'GROUP' || item.target.kind === 'GROUP'
      ? '分组组件不能连续串联'
      : sourceAlreadyGrouped
        ? '该数据节点已进入其他分组组件'
        : '按维度聚合指标'
    return <div
      className="dataset-edge-component-picker"
      role="dialog"
      aria-modal="false"
      aria-label={`在${item.targetLabel}的连线上插入组件`}
      onClick={event => event.stopPropagation()}
      onPointerDown={event => event.stopPropagation()}
      onWheel={event => event.stopPropagation()}
      onTouchMove={event => event.stopPropagation()}
    >
      <header>
        <div><strong>插入组件</strong><small>{inputLabel(item.source)} → {item.targetLabel}</small></div>
        <button type="button" aria-label="关闭组件选择" onClick={() => setOpenEdgeMenuKey('')}><XIcon aria-hidden="true" size={13} weight="bold" /></button>
      </header>
      <div className="dataset-edge-component-list">
        <section className="component-flow" aria-label="流程组件">
          <strong>流程组件</strong>
          <button type="button" disabled={groupDisabled} title={groupDisabled ? groupDescription : undefined} onClick={() => insertComponentOnEdge(item, { kind: 'GROUP' })}>
            <RowsIcon data-component-icon="GROUP" aria-hidden="true" size={17} weight="bold" />
            <span><b>分组组件</b><small>{groupDescription}</small></span>
          </button>
          <button type="button" disabled={joinDisabled} title={joinDisabled ? '该分组产物不满足关联槽位输入约束' : undefined} onClick={() => insertComponentOnEdge(item, { kind: 'JOIN' })}>
            <GitMergeIcon data-component-icon="JOIN" aria-hidden="true" size={17} weight="bold" />
            <span><b>关联组件</b><small>{joinDisabled ? '当前分组产物不可再关联' : '当前上游接入槽位 1'}</small></span>
          </button>
        </section>
        {transformCategoryMeta.map(category => <section key={category.category} className={category.className} aria-label={category.label}>
          <strong>{category.label}</strong>
          {transformComponentMeta.filter(component => component.category === category.category).sort((left, right) => left.sortKey.localeCompare(right.sortKey, 'en')).map(component => {
            const ComponentIcon = component.icon
            return <button key={component.componentType} type="button" onClick={() => insertComponentOnEdge(item, { kind: 'TRANSFORM', componentType: component.componentType })}>
              <ComponentIcon data-component-icon={component.componentType} aria-hidden="true" size={17} weight="bold" />
              <span><b>{component.label}</b><small>{component.description}</small></span>
            </button>
          })}
        </section>)}
      </div>
    </div>
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
    <DatasetComponentToolbar
      categories={transformCategoryMeta}
      components={transformComponentMeta}
      hasEnd={Boolean(end)}
      onAddGroup={() => onAddGroup()}
      onAddJoin={() => onAddJoin()}
      onAddTransform={onAddTransform}
      onAddEnd={onAddEnd}
    />
    <div className="dataset-component-canvas-frame">
      <div className="dataset-canvas-actions" role="toolbar" aria-label="画布工具">
        <button type="button" onClick={onArrange}><TreeStructureIcon aria-hidden="true" size={15} weight="bold" /><span>整理</span></button>
        <button type="button" aria-pressed={isFullscreen} onClick={onToggleFullscreen}>{isFullscreen ? <ArrowsInSimpleIcon aria-hidden="true" size={15} weight="bold" /> : <ArrowsOutSimpleIcon aria-hidden="true" size={15} weight="bold" />}<span>{isFullscreen ? '退出全屏' : '全屏'}</span></button>
      </div>
      <div ref={canvasRef} className={`dataset-component-canvas ${draggingConnection ? 'is-connecting' : ''}`} aria-label="关系组件画布" onClick={() => setOpenEdgeMenuKey('')} onPointerDown={beginPointerConnection} onDragOver={event => { event.preventDefault(); if (draggingConnection) { const bounds = event.currentTarget.getBoundingClientRect(); setConnectionPoint({ x: event.clientX - bounds.left + (event.currentTarget.scrollLeft || 0), y: event.clientY - bounds.top + (event.currentTarget.scrollTop || 0) }) } }} onDrop={dropOnCanvas}>
      <svg ref={lineLayerRef} className="dataset-component-lines" style={canvasExtent} aria-hidden="true"><defs><marker id="dataset-edge-arrow" markerWidth="10" markerHeight="10" refX="8.5" refY="5" orient="auto" markerUnits="userSpaceOnUse"><path d="M0,0 L10,5 L0,10 Z" /></marker></defs>{edges.map(item => <path className="dataset-flow-edge" data-source-key={graphInputKey(item.source)} key={item.key} d={item.geometry.path} markerEnd="url(#dataset-edge-arrow)" />)}{draggingStart && connectionPoint && <path className="preview" d={curveGeometry(draggingStart, connectionPoint).path} markerEnd="url(#dataset-edge-arrow)" />}</svg>
      {edges.map(item => <div key={`actions-${item.key}`} className={`dataset-line-actions ${openEdgeMenuKey === item.key ? 'is-open' : ''}`} style={{ left: item.geometry.deletePosition.x, top: item.geometry.deletePosition.y }} onClick={event => event.stopPropagation()}>
        <button type="button" className="dataset-line-add" aria-label={`在${item.targetLabel}的连线上插入组件`} aria-expanded={openEdgeMenuKey === item.key} aria-haspopup="dialog" onClick={() => setOpenEdgeMenuKey(current => current === item.key ? '' : item.key)}><PlusIcon aria-hidden="true" size={12} weight="bold" /></button>
        <button type="button" className="dataset-line-delete" aria-label={item.deleteLabel} onClick={item.remove}><XIcon aria-hidden="true" size={11} weight="bold" /></button>
      </div>)}
      {nodes.map((node, index) => { const position = nodePositions[node.id] ?? { x: 42, y: 58 + index * 145 }; const nodeFields = fields.filter(field => field.key.startsWith(`${node.id}.`)); return <article key={node.id} role="button" tabIndex={0} aria-label={`配置数据节点 ${index + 1}`} style={{ left: position.x, top: position.y }} className={`dataset-canvas-component data ${activeNodeID === node.id ? 'active' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'NODE', id: node.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onClick={() => onNodeClick(node.id)}><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'NODE', id: node.id })} aria-label={`从数据节点 ${index + 1} 拖出连接`} draggable onDragStart={event => dragConnection(event, { kind: 'NODE', id: node.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>数据节点 {index + 1}</span><button type="button" aria-label={`移除${nodeLabel(node)}`} onClick={event => { event.stopPropagation(); onRemoveNode(node.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{node.table.businessName || node.table.tableName}</strong><small>{node.table.dataSourceName} · {node.alias}</small><footer><span>原始数据</span><b>{nodeFields.filter(field => field.output !== false).length} 字段</b></footer>{previewTrigger({ kind: 'NODE', id: node.id }, `数据节点 ${index + 1}`)}</article> })}
      {boxes.map((box, index) => { const position = box.position; const join = joins.find(item => item.id === box.id); const outputs = relationOutputKeys({ kind: 'JOIN', id: box.id }, boxes, groups, nodes, fields, transforms); const complete = Boolean(box.left && box.right); return <article key={box.id} role="button" tabIndex={0} aria-label={`配置关联 ${index + 1}`} style={{ left: position.x, top: position.y }} className={`dataset-canvas-component relation ${activeJoinID === box.id ? 'active' : ''} ${join?.manualConfirmed ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'JOIN', id: box.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onDragOver={acceptConnectionOver} onDrop={event => dropOnJoinNode(event, box)} onClick={() => onJoinClick(box.id)}><button type="button" className="input-port component-side input-side slot-one" aria-label={`连接到关联节点 ${index + 1} 槽位 1`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropConnection(event, box.id, 'left')} /><button type="button" className="input-port component-side input-side slot-two" aria-label={`连接到关联节点 ${index + 1} 槽位 2`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropConnection(event, box.id, 'right')} /><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'JOIN', id: box.id })} aria-label={`从关联节点 ${index + 1} 拖出连接`} draggable={complete} aria-disabled={!complete} onDragStart={event => complete && dragConnection(event, { kind: 'JOIN', id: box.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>关联组件</span><button type="button" aria-label={`删除关联组件 ${index + 1}`} onClick={event => { event.stopPropagation(); onRemoveBox(box.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{box.name}</strong><small>{join?.joinType ? `${join.joinType} JOIN` : '尚未完成关联'}</small><div><span>槽位 1</span><b>{inputLabel(box.left)}</b></div><div><span>槽位 2</span><b>{inputLabel(box.right)}</b></div><footer><span>{join?.manualConfirmed ? `${joinConditions(join).length} 个关联条件` : '点击完成配置'}</span><b>{outputs.length} 字段</b></footer>{previewTrigger({ kind: 'JOIN', id: box.id }, `关联组件 ${index + 1}`)}</article> })}
          {groups.map((group, index) => { const complete = groupIsComplete(group); const modeLabel = group.groupByMode === 'CUBE' ? 'CUBE · ' : group.groupByMode === 'ROLLUP' ? 'ROLLUP · ' : group.groupByMode === 'GROUPING_SETS' ? 'SETS · ' : ''; return <article key={group.id} role="button" tabIndex={0} aria-label={`打开分组组件 ${index + 1} 配置`} style={{ left: group.position.x, top: group.position.y }} className={`dataset-canvas-component group ${activeGroupID === group.id ? 'active' : ''} ${complete ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'GROUP', id: group.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onDragOver={acceptConnectionOver} onDrop={event => finishConnectionDrop(event, input => onConnectGroup(group.id, input))} onClick={() => onGroupClick(group.id)}><button type="button" className="input-port component-side input-side group-input" aria-label={`连接到分组组件 ${index + 1} 输入槽位`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropGroupConnection(event, group.id)} /><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'GROUP', id: group.id })} aria-label={`从分组组件 ${index + 1} 拖出连接`} draggable={complete} aria-disabled={!complete} onDragStart={event => complete && dragConnection(event, { kind: 'GROUP', id: group.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>分组组件 {index + 1}</span><button type="button" aria-label={`删除分组组件 ${index + 1}`} onClick={event => { event.stopPropagation(); onRemoveGroup(group.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{group.name}</strong><div><span>输入</span><b>{inputLabel(group.input)}</b></div><footer><span>{modeLabel}{group.dimensions.length} 个维度</span><b>{group.metrics.length} 个指标</b></footer>{previewTrigger({ kind: 'GROUP', id: group.id }, `分组组件 ${index + 1}`)}</article> })}
      {transforms.map((transform, index) => {
        const complete = transformIsComplete(transform)
        const label = transformDisplayLabel(transform)
        const itemCount = transformIsFilter(transform) ? transform.conditions?.length ?? 0 : transform.rules.length
        return <article key={transform.id} role="button" tabIndex={0} aria-label={`打开${label} ${index + 1} 配置`} style={{ left: transform.position.x, top: transform.position.y }} className={`dataset-canvas-component transform ${transformColorClass(transform)} ${activeTransformID === transform.id ? 'active' : ''} ${complete ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => { const value = JSON.stringify({ kind: 'TRANSFORM', id: transform.id }); event.dataTransfer.setData('text/dataset-canvas-item', value); event.dataTransfer.setData('text/dataset-relation-input', value) }} onDragOver={acceptConnectionOver} onDrop={event => finishConnectionDrop(event, input => onConnectTransform(transform.id, input))} onClick={() => onTransformClick(transform.id)}><button type="button" className="input-port component-side input-side group-input" aria-label={`连接到${label} ${index + 1} 输入槽位`} onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={event => dropTransformConnection(event, transform.id)} /><button type="button" className="output-port component-side output-side" data-source-key={graphInputKey({ kind: 'TRANSFORM', id: transform.id })} aria-label={`从${label} ${index + 1} 拖出连接`} draggable={complete} aria-disabled={!complete} onDragStart={event => complete && dragConnection(event, { kind: 'TRANSFORM', id: transform.id })} onDragEnd={() => { setDraggingConnection(null); setConnectionPoint(null) }} /><header><span>{label}</span><button type="button" aria-label={`删除${label} ${index + 1}`} onClick={event => { event.stopPropagation(); onRemoveTransform(transform.id) }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{transform.name}</strong><div><span>输入</span><b>{inputLabel(transform.input)}</b></div><footer><span>{itemCount} 条{transformIsFilter(transform) ? '条件' : '规则'}</span><b>{complete ? '已配置' : '待完善'}</b></footer>{previewTrigger({ kind: 'TRANSFORM', id: transform.id }, `${label} ${index + 1}`)}</article>
      })}
      {end && <article role="button" tabIndex={0} aria-label="打开结束节点配置" style={{ left: end.position.x, top: end.position.y }} className={`dataset-canvas-component end ${activeEnd ? 'active' : ''} ${end.input && end.outputs.length ? 'configured' : ''} ${draggingConnection ? 'connection-target' : ''}`} draggable onDragStart={event => event.dataTransfer.setData('text/dataset-canvas-item', JSON.stringify({ kind: 'END', id: end.id }))} onDragOver={acceptConnectionOver} onDrop={event => finishConnectionDrop(event, onConnectEnd)} onClick={onEndClick}><button type="button" className="input-port component-side input-side group-input" aria-label="连接到结束节点输入槽位" onDragOver={event => { event.preventDefault(); event.stopPropagation() }} onDrop={dropEndConnection} /><header><span>结束节点</span><button type="button" aria-label="删除结束节点" onClick={event => { event.stopPropagation(); onRemoveEnd() }}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header><strong>{end.name}</strong><div><span>最终输入</span><b>{inputLabel(end.input)}</b></div><footer><span>输出结果</span><b>{end.outputs.length} 个字段</b></footer>{previewTrigger({ kind: 'END', id: end.id }, '结束节点')}</article>}
      {!boxes.length && !groups.length && !transforms.length && !end && <div className="dataset-component-canvas-hint"><strong>{nodes.length ? '从顶部选择组件建立数据流' : '从左侧拖入数据集开始建模'}</strong><p>{nodes.length ? '字段处理、分组、关联与结束节点之间会用有方向的曲线连接。' : '数据集会成为画布节点，随后可从顶部横向组件栏继续搭建流程。'}</p></div>}
      </div>
      {edges.find(item => item.key === openEdgeMenuKey) && <div
        className="dataset-edge-component-modal"
        role="presentation"
        onClick={() => setOpenEdgeMenuKey('')}
        onPointerDown={event => event.stopPropagation()}
        onWheel={event => event.stopPropagation()}
        onTouchMove={event => event.stopPropagation()}
      >
        {edgeComponentPicker(edges.find(item => item.key === openEdgeMenuKey)!)}
      </div>}
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
    <section><div className="dataset-drawer-title"><div><h3>输出字段</h3><p>数据节点只负责投影；分组与聚合请连接独立分组组件。</p></div><span>{fields.filter(field => field.output !== false).length} 已选</span></div><div className="dataset-drawer-field-list">{node.columns.map(column => { const option = optionFor(column); return <label key={column.id}><input aria-label={`输出字段 ${column.columnName}`} type="checkbox" checked={option.output !== false} onChange={event => onFieldPatch(option.key, { output: event.target.checked })} /><span><strong>{column.businessName || column.columnName}</strong><small>{node.table.businessName || node.table.tableName}</small></span></label> })}</div></section>
    <footer><small>点击画板空白处也会自动保存并收起</small><button type="button" onClick={onDone}>完成</button></footer>
  </aside>
}

type OrderedPickerField = Pick<ProducedField, 'key' | 'name' | 'code'> & Partial<Pick<ProducedField, 'producerName' | 'canonicalType'>>

function OrderedFieldSequence({ fields, keys, onChange, emptyText, compact = false }: {
  fields: OrderedPickerField[]
  keys: string[]
  onChange: (keys: string[]) => void
  emptyText: string
  compact?: boolean
}) {
  const [draggingKey, setDraggingKey] = useState('')
  const draggingKeyRef = useRef('')
  const fieldsByKey = new Map(fields.map(field => [field.key, field]))
  const orderedFields = keys.flatMap(key => {
    const field = fieldsByKey.get(key)
    return field ? [field] : []
  })
  const move = (key: string, offset: number) => {
    const currentIndex = keys.indexOf(key)
    const nextIndex = currentIndex + offset
    if (currentIndex < 0 || nextIndex < 0 || nextIndex >= keys.length) return
    const next = [...keys]
    const [moved] = next.splice(currentIndex, 1)
    next.splice(nextIndex, 0, moved)
    onChange(next)
  }
  const moveBefore = (targetKey: string) => {
    const sourceKey = draggingKeyRef.current
    if (!sourceKey || sourceKey === targetKey || !keys.includes(sourceKey)) return
    const next = keys.filter(key => key !== sourceKey)
    next.splice(Math.max(0, next.indexOf(targetKey)), 0, sourceKey)
    onChange(next)
  }
  useEffect(() => {
    if (!draggingKey) return
    const stopDragging = () => {
      draggingKeyRef.current = ''
      setDraggingKey('')
    }
    window.addEventListener('pointerup', stopDragging)
    window.addEventListener('pointercancel', stopDragging)
    return () => {
      window.removeEventListener('pointerup', stopDragging)
      window.removeEventListener('pointercancel', stopDragging)
    }
  }, [draggingKey])
  if (!orderedFields.length) return <div className={`dataset-ordered-fields-empty ${compact ? 'compact' : ''}`}>{emptyText}</div>
  return <div className={`dataset-ordered-fields ${compact ? 'compact' : ''}`} aria-label="已选字段顺序">
    {orderedFields.map((field, index) => <div
      className={draggingKey === field.key ? 'is-dragging' : ''}
      draggable
      key={field.key}
      onPointerEnter={() => moveBefore(field.key)}
      onPointerUp={() => {
        draggingKeyRef.current = ''
        setDraggingKey('')
      }}
      onDragStart={event => {
        draggingKeyRef.current = field.key
        setDraggingKey(field.key)
        event.dataTransfer.effectAllowed = 'move'
        event.dataTransfer.setData('text/dataset-group-field', field.key)
      }}
      onDragOver={event => {
        event.preventDefault()
        event.dataTransfer.dropEffect = 'move'
      }}
      onDrop={event => {
        event.preventDefault()
        event.stopPropagation()
        draggingKeyRef.current = event.dataTransfer.getData('text/dataset-group-field') || draggingKeyRef.current
        moveBefore(field.key)
        draggingKeyRef.current = ''
        setDraggingKey('')
      }}
      onDragEnd={() => {
        draggingKeyRef.current = ''
        setDraggingKey('')
      }}
    >
      <button type="button" className="dataset-field-drag-handle" aria-label={`拖拽调整 ${field.name} 的顺序`} onPointerDown={event => {
        if (event.button !== 0) return
        draggingKeyRef.current = field.key
        setDraggingKey(field.key)
      }}><DotsSixVerticalIcon aria-hidden="true" size={16} weight="bold" /></button>
      <b>{index + 1}</b>
      <span><strong>{field.name}</strong><small>{field.producerName || '上游产物'}</small></span>
      <div>
        <button type="button" aria-label={`上移 ${field.name}`} disabled={index === 0} onClick={() => move(field.key, -1)}><ArrowUpIcon size={13} weight="bold" /></button>
        <button type="button" aria-label={`下移 ${field.name}`} disabled={index === orderedFields.length - 1} onClick={() => move(field.key, 1)}><ArrowDownIcon size={13} weight="bold" /></button>
        <button type="button" aria-label={`移除 ${field.name}`} onClick={() => onChange(keys.filter(key => key !== field.key))}><XIcon size={13} weight="bold" /></button>
      </div>
    </div>)}
  </div>
}

function OrderedFieldSelectionDialog({ title, description, fields, selectedKeys, maxSelected, onCancel, onApply }: {
  title: string
  description: string
  fields: OrderedPickerField[]
  selectedKeys: string[]
  maxSelected?: number
  onCancel: () => void
  onApply: (keys: string[]) => void
}) {
  const availableKeys = new Set(fields.map(field => field.key))
  const [draftKeys, setDraftKeys] = useState(() => selectedKeys.filter(key => availableKeys.has(key)))
  const [keyword, setKeyword] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)
  const normalizedKeyword = keyword.trim().toLocaleLowerCase()
  const filteredFields = fields.filter(field => !normalizedKeyword || [field.name, field.producerName || ''].some(value => value.toLocaleLowerCase().includes(normalizedKeyword)))
  const selected = new Set(draftKeys)
  const atLimit = Boolean(maxSelected && draftKeys.length >= maxSelected)
  useEffect(() => {
    searchRef.current?.focus()
    const previousOverflow = document.body.style.overflow
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCancel()
    }
    document.body.style.overflow = 'hidden'
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.body.style.overflow = previousOverflow
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [onCancel])
  const toggle = (key: string, enabled: boolean) => {
    if (enabled) {
      if (selected.has(key) || atLimit) return
      setDraftKeys(current => [...current, key])
      return
    }
    setDraftKeys(current => current.filter(item => item !== key))
  }
  return <div className="dataset-field-picker-backdrop" onMouseDown={event => event.target === event.currentTarget && onCancel()}>
    <section className="dataset-field-picker-dialog" role="dialog" aria-modal="true" aria-label={title} onMouseDown={event => event.stopPropagation()} onWheel={event => event.stopPropagation()} onTouchMove={event => event.stopPropagation()}>
      <header>
        <div><span>有序多选</span><strong>{title}</strong><small>{description}</small></div>
        <button type="button" aria-label={`关闭${title}`} onClick={onCancel}><XIcon size={16} weight="bold" /></button>
      </header>
      <div className="dataset-field-picker-body">
        <section className="dataset-field-picker-candidates">
          <header><div><strong>可选字段</strong><small>点击字段后按选择先后加入右侧</small></div><span>{fields.length} 个</span></header>
          <label className="dataset-field-picker-search">
            <MagnifyingGlassIcon aria-hidden="true" size={15} />
            <input ref={searchRef} aria-label="搜索可选字段" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索数据集或字段名称" />
          </label>
          <div className="dataset-field-picker-options">
            {filteredFields.map(field => {
              const checked = selected.has(field.key)
              const order = checked ? draftKeys.indexOf(field.key) + 1 : 0
              return <label className={checked ? 'selected' : ''} key={field.key}>
                <input type="checkbox" checked={checked} disabled={!checked && atLimit} onChange={event => toggle(field.key, event.target.checked)} />
                <span><strong>{field.name}</strong><small>{field.producerName || '上游产物'}</small></span>
                {checked && <b aria-label={`选择顺序 ${order}`}>{order}</b>}
              </label>
            })}
            {!filteredFields.length && <p>没有匹配的字段</p>}
          </div>
        </section>
        <section className="dataset-field-picker-selected">
          <header><div><strong>字段顺序</strong><small>拖拽调整；也可使用上移、下移按钮</small></div><span>{draftKeys.length}{maxSelected ? ` / ${maxSelected}` : ''}</span></header>
          <OrderedFieldSequence fields={fields} keys={draftKeys} onChange={setDraftKeys} emptyText="尚未选择字段，请从左侧依次点击加入" />
        </section>
      </div>
      <footer>
        <button type="button" className="quiet" onClick={() => setDraftKeys([])}>清空</button>
        <span>{maxSelected && atLimit ? `当前模式最多选择 ${maxSelected} 个字段` : '保存后仍可在抽屉中拖拽调整顺序'}</span>
        <button type="button" className="quiet" onClick={onCancel}>取消</button>
        <button type="button" onClick={() => onApply(draftKeys)}>完成选择</button>
      </footer>
    </section>
  </div>
}

function OrderedDimensionPicker({ title, description, fields, selectedKeys, onChange, emptyText, maxSelected, compact = false }: {
  title: string
  description: string
  fields: OrderedPickerField[]
  selectedKeys: string[]
  onChange: (keys: string[]) => void
  emptyText: string
  maxSelected?: number
  compact?: boolean
}) {
  const [open, setOpen] = useState(false)
  return <div className={`dataset-ordered-field-picker ${compact ? 'compact' : ''}`}>
    <button type="button" className="dataset-ordered-field-trigger" disabled={!fields.length} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(true)}>
      <span><small>维度字段</small><strong>{selectedKeys.length ? `已选择 ${selectedKeys.length} 个，点击修改` : '点击选择维度字段'}</strong></span>
      <CaretDownIcon aria-hidden="true" size={15} weight="bold" />
    </button>
    <OrderedFieldSequence fields={fields} keys={selectedKeys} onChange={onChange} emptyText={emptyText} compact={compact} />
    {open && <OrderedFieldSelectionDialog
      title={title}
      description={description}
      fields={fields}
      selectedKeys={selectedKeys}
      maxSelected={maxSelected}
      onCancel={() => setOpen(false)}
      onApply={keys => {
        onChange(keys)
        setOpen(false)
      }}
    />}
  </div>
}

const groupMetricAggregationOptions = (field: ProducedField) => [
  ...(numericCanonicalTypes.has(field.canonicalType.toUpperCase()) ? [
    { value: 'SUM', label: 'SUM · 求和' },
    { value: 'AVG', label: 'AVG · 平均值' },
  ] : []),
  { value: 'COUNT', label: 'COUNT · 非空计数' },
  { value: 'COUNT_DISTINCT', label: 'COUNT DISTINCT · 去重计数' },
  { value: 'MIN', label: 'MIN · 最小值' },
  { value: 'MAX', label: 'MAX · 最大值' },
]

function GroupMetricSelectionDialog({ fields, metrics, onCancel, onApply }: {
  fields: ProducedField[]
  metrics: GroupBox['metrics']
  onCancel: () => void
  onApply: (metrics: GroupMetricSelection[]) => void
}) {
  const [draftMetrics, setDraftMetrics] = useState<GroupMetricSelection[]>(() => {
    const availableFields = new Map(fields.map(field => [field.key, field]))
    const seen = new Set<string>()
    return metrics.flatMap(metric => {
      const key = metric.key === '*' || metric.countRows ? '*' : metric.key
      if (seen.has(key)) return []
      seen.add(key)
      if (key === '*') return [{ key, aggregation: 'COUNT' }]
      const field = availableFields.get(key)
      if (!field) return []
      const aggregation = groupMetricAggregationOptions(field).some(option => option.value === metric.aggregation) ? metric.aggregation : ''
      return [{ key, aggregation }]
    })
  })
  const [keyword, setKeyword] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)
  const normalizedKeyword = keyword.trim().toLocaleLowerCase()
  const filteredFields = fields.filter(field => !normalizedKeyword || [field.name, field.producerName].some(value => value.toLocaleLowerCase().includes(normalizedKeyword)))
  const rowCountVisible = !normalizedKeyword || ['总行数', '全部输入行', 'count'].some(value => value.includes(normalizedKeyword))
  const selectedKeys = new Set(draftMetrics.map(metric => metric.key))
  const allFieldsSelected = fields.length > 0 && fields.every(field => selectedKeys.has(field.key))
  const incomplete = draftMetrics.some(metric => {
    if (metric.key === '*') return metric.aggregation !== 'COUNT'
    const field = fields.find(item => item.key === metric.key)
    return !field || !groupMetricAggregationOptions(field).some(option => option.value === metric.aggregation)
  })
  useEffect(() => {
    searchRef.current?.focus()
    const previousOverflow = document.body.style.overflow
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCancel()
    }
    document.body.style.overflow = 'hidden'
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.body.style.overflow = previousOverflow
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [onCancel])
  const toggleMetric = (key: string, enabled: boolean) => {
    if (!enabled) {
      setDraftMetrics(current => current.filter(metric => metric.key !== key))
      return
    }
    if (selectedKeys.has(key)) return
    const field = fields.find(item => item.key === key)
    setDraftMetrics(current => [...current, {
      key,
      aggregation: key === '*' ? 'COUNT' : field && numericCanonicalTypes.has(field.canonicalType.toUpperCase()) ? 'SUM' : 'COUNT',
    }])
  }
  const updateAggregation = (key: string, aggregation: string) => {
    setDraftMetrics(current => current.map(metric => metric.key === key ? { ...metric, aggregation } : metric))
  }
  const toggleAllFields = () => {
    if (allFieldsSelected) {
      const fieldKeys = new Set(fields.map(field => field.key))
      setDraftMetrics(current => current.filter(metric => !fieldKeys.has(metric.key)))
      return
    }
    setDraftMetrics(current => {
      const currentKeys = new Set(current.map(metric => metric.key))
      return [...current, ...fields.flatMap(field => currentKeys.has(field.key) ? [] : [{
        key: field.key,
        aggregation: numericCanonicalTypes.has(field.canonicalType.toUpperCase()) ? 'SUM' : 'COUNT',
      }])]
    })
  }
  return <div className="dataset-field-picker-backdrop" onMouseDown={event => event.target === event.currentTarget && onCancel()}>
    <section className="dataset-field-picker-dialog dataset-metric-picker-dialog" role="dialog" aria-modal="true" aria-label="选择聚合指标并配置计算公式" onMouseDown={event => event.stopPropagation()} onWheel={event => event.stopPropagation()} onTouchMove={event => event.stopPropagation()}>
      <header>
        <div><span>指标配置</span><strong>选择聚合指标并配置计算公式</strong><small>先选择指标字段，再为每个指标指定聚合计算公式。</small></div>
        <button type="button" aria-label="关闭聚合指标选择" onClick={onCancel}><XIcon size={16} weight="bold" /></button>
      </header>
      <div className="dataset-field-picker-body">
        <section className="dataset-field-picker-candidates">
          <header><div><strong>可选指标</strong><small>字段按数据集名称与字段名称展示</small></div><span>{fields.length + 1} 个</span></header>
          <label className="dataset-field-picker-search">
            <MagnifyingGlassIcon aria-hidden="true" size={15} />
            <input ref={searchRef} aria-label="搜索可选指标" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索数据集或字段名称" />
          </label>
          <div className="dataset-field-picker-options">
            {rowCountVisible && <label className={selectedKeys.has('*') ? 'selected' : ''}>
              <input type="checkbox" checked={selectedKeys.has('*')} onChange={event => toggleMetric('*', event.target.checked)} />
              <span><strong>总行数</strong><small>全部输入行</small></span>
              {selectedKeys.has('*') && <b aria-label="已选择总行数"><CheckCircleIcon aria-hidden="true" size={13} weight="fill" /></b>}
            </label>}
            {filteredFields.map(field => <label className={selectedKeys.has(field.key) ? 'selected' : ''} key={field.key}>
              <input type="checkbox" checked={selectedKeys.has(field.key)} onChange={event => toggleMetric(field.key, event.target.checked)} />
              <span><strong>{field.name}</strong><small>{field.producerName}</small></span>
              {selectedKeys.has(field.key) && <b aria-label={`已选择 ${field.name}`}><CheckCircleIcon aria-hidden="true" size={13} weight="fill" /></b>}
            </label>)}
            {!rowCountVisible && !filteredFields.length && <p>没有匹配的指标字段</p>}
          </div>
        </section>
        <section className="dataset-field-picker-selected dataset-metric-picker-selected">
          <header><div><strong>已选指标与计算公式</strong><small>每个指标必须配置一种计算公式</small></div><span>{draftMetrics.length}</span></header>
          <div className="dataset-metric-formulas">
            {draftMetrics.map((metric, index) => {
              const field = metric.key === '*' ? undefined : fields.find(item => item.key === metric.key)
              if (metric.key !== '*' && !field) return null
              return <article key={metric.key}>
                <header>
                  <b>{index + 1}</b>
                  <span><strong>{field?.name || '总行数'}</strong><small>{field?.producerName || '全部输入行'}</small></span>
                  <button type="button" aria-label={`移除指标 ${field?.name || '总行数'}`} onClick={() => toggleMetric(metric.key, false)}><XIcon size={13} weight="bold" /></button>
                </header>
                <label><span>计算公式</span><select aria-label={`${field?.name || '总行数'} 计算公式`} value={metric.aggregation} disabled={metric.key === '*'} onChange={event => updateAggregation(metric.key, event.target.value)}>
                  {metric.key === '*'
                    ? <option value="COUNT">COUNT(*) · 总行数</option>
                    : <><option value="">选择计算公式</option>{groupMetricAggregationOptions(field!).map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</>}
                </select></label>
              </article>
            })}
            {!draftMetrics.length && <div className="dataset-ordered-fields-empty">尚未选择聚合指标，请从左侧勾选</div>}
          </div>
        </section>
      </div>
      <footer>
        <button type="button" className="quiet" disabled={!fields.length} onClick={toggleAllFields}>{allFieldsSelected ? '取消全选字段' : '全选字段'}</button>
        <span>{incomplete ? '请为全部已选指标配置计算公式' : `已配置 ${draftMetrics.length} 个指标`}</span>
        <button type="button" className="quiet" onClick={onCancel}>取消</button>
        <button type="button" disabled={incomplete} onClick={() => onApply(draftMetrics)}>完成配置</button>
      </footer>
    </section>
  </div>
}

function GroupMetricPicker({ fields, metrics, onChange }: {
  fields: ProducedField[]
  metrics: GroupBox['metrics']
  onChange: (metrics: GroupMetricSelection[]) => void
}) {
  const [open, setOpen] = useState(false)
  const fieldsByKey = new Map(fields.map(field => [field.key, field]))
  return <div className="dataset-group-metric-picker">
    <button type="button" className="dataset-ordered-field-trigger" aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(true)}>
      <span><small>指标字段与计算公式</small><strong>{metrics.length ? `已配置 ${metrics.length} 个指标，点击修改` : '点击选择指标并配置计算公式'}</strong></span>
      <CaretDownIcon aria-hidden="true" size={15} weight="bold" />
    </button>
    <div className="dataset-group-metric-summary">
      {metrics.map(metric => {
        const field = fieldsByKey.get(metric.key)
        return <div key={metric.key}>
          <span><strong>{metric.key === '*' || metric.countRows ? '总行数' : field?.name || metric.name}</strong><small>{metric.key === '*' || metric.countRows ? '全部输入行' : field?.producerName || '上游产物'}</small></span>
          <b>{metric.key === '*' || metric.countRows ? 'COUNT(*)' : metric.aggregation || '未配置'}</b>
        </div>
      })}
      {!metrics.length && <div className="dataset-ordered-fields-empty">尚未选择聚合指标</div>}
    </div>
    {open && <GroupMetricSelectionDialog
      fields={fields}
      metrics={metrics}
      onCancel={() => setOpen(false)}
      onApply={next => {
        onChange(next)
        setOpen(false)
      }}
    />}
  </div>
}

function GroupingConfigDrawer({ box, boxes, groups, transforms, nodes, availableFields, error, onNameChange, onGroupByModeChange, onGroupingSetsChange, onDimensionsChange, onMetricsChange, onDone }: {
  box: GroupBox; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; nodes: DesignerNode[]; availableFields: ProducedField[]
  error: string; onNameChange: (name: string) => void
  onGroupByModeChange: (mode: GraphGroupByMode) => void
  onGroupingSetsChange: (groupingSets: string[][]) => void
  onDimensionsChange: (fields: ProducedField[]) => void
  onMetricsChange: (metrics: GroupMetricSelection[]) => void
  onDone: () => void
}) {
  const shape = graphShape(boxes, groups, transforms)
  const availableFieldsByKey = new Map(availableFields.map(field => [field.key, field]))
  const dimensionPickerFields: OrderedPickerField[] = box.dimensions.map(dimension => availableFieldsByKey.get(dimension.key) ?? dimension)
  const updateDimensionKeys = (keys: string[]) => onDimensionsChange(keys.flatMap(key => {
    const field = availableFieldsByKey.get(key)
    return field ? [field] : []
  }))
  const placeholderHelp = '未分组维度按类型显示：文本 UNKNOWN、日期 1970-01-01、数值 999999999、布尔 False。'
  const modeHelp = box.groupByMode === 'CUBE'
    ? `为全部维度组合生成明细、小计与总计；${placeholderHelp}`
    : box.groupByMode === 'ROLLUP'
      ? `按维度顺序逐级汇总并生成总计；${placeholderHelp}`
      : box.groupByMode === 'GROUPING_SETS'
        ? `只生成下方明确配置的维度组合；空分组集表示总计。${placeholderHelp}`
        : '按所有已选维度生成单一粒度的聚合结果。'
  const orderHelp = box.groupByMode === 'ROLLUP'
    ? '字段顺序就是逐级汇总路径，例如“区域 → 门店 → 商品”。'
    : box.groupByMode === 'GROUPING_SETS'
      ? '这里先建立可用维度池；每个分组集可在下方独立选择并排序。'
      : box.groupByMode === 'CUBE'
        ? '字段顺序决定维度输出顺序；CUBE 会计算这些字段的全部组合。'
        : '字段顺序决定分组维度在结果中的输出顺序。'
  return <aside className="dataset-canvas-drawer output" aria-label="配置分组组件" onClick={event => event.stopPropagation()}>
    <header><div><span>分组组件</span><strong>{box.name}</strong><small>先定义输入粒度，再为下游自动生成带稳定别名的维度和指标</small></div><button type="button" aria-label="保存并关闭分组配置" onClick={onDone}>×</button></header>
    <section><h3>组件与产物</h3><div className="dataset-group-input"><label><span>产物名称</span><input aria-label="分组产物名称" value={box.name} onChange={event => onNameChange(event.target.value)} placeholder="例如：客户月度汇总" /></label><label><span>分组方式</span><select aria-label="分组方式" value={box.groupByMode || 'STANDARD'} onChange={event => onGroupByModeChange(event.target.value as GraphGroupByMode)}><option value="STANDARD">普通分组（GROUP BY）</option><option value="CUBE">多维分组（GROUP BY CUBE）</option><option value="ROLLUP">逐级汇总（GROUP BY ROLLUP）</option><option value="GROUPING_SETS">自定义组合（GROUPING SETS）</option></select><small>{modeHelp}</small></label><div aria-label="分组组件输入" className={`dataset-connected-input ${box.input ? 'connected' : 'empty'}`}><span>输入组件</span><strong>{relationInputLabel(box.input, nodes, boxes, groups, transforms)}</strong><small>{box.input ? '输入由画布连线确定；删除连线后可重新连接' : '请回到画布，从上游组件拖线到该组件输入端口'}</small></div></div></section>
    <section>
      <div className="dataset-drawer-title"><div><h3>分组字段</h3><p>{orderHelp}</p></div><span>{box.dimensions.length} 已选</span></div>
      <OrderedDimensionPicker
        title="选择并排序分组字段"
        description="点击字段决定初始顺序，选择完成后可继续拖拽调整。"
        fields={availableFields}
        selectedKeys={box.dimensions.map(dimension => dimension.key)}
        onChange={updateDimensionKeys}
        emptyText="尚未选择分组字段"
        maxSelected={box.groupByMode === 'CUBE' ? 8 : undefined}
      />
      <p className="dataset-ordered-field-note">同一字段仍可作为聚合指标；日期年月日请先连接独立日期转换组件。</p>
    </section>
    {box.groupByMode === 'GROUPING_SETS' && <section><div className="dataset-drawer-title"><div><h3>自定义分组集</h3><p>每个分组集是一种输出粒度；不选维度的空分组集会生成总计。</p></div><span>{box.groupingSets?.length ?? 0} 组</span></div><div className="dataset-grouping-sets">{(box.groupingSets ?? []).map((groupingSet, index) => {
      return <article key={index}>
        <header><strong>分组集 {index + 1}</strong><span>{groupingSet.length ? `${groupingSet.length} 个维度` : '总计（空分组集）'}</span><button type="button" aria-label={`删除分组集 ${index + 1}`} disabled={(box.groupingSets?.length ?? 0) <= 1} onClick={() => onGroupingSetsChange((box.groupingSets ?? []).filter((_, itemIndex) => itemIndex !== index))}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header>
        <OrderedDimensionPicker
          compact
          title={`选择并排序分组集 ${index + 1} 的字段`}
          description="每个分组集独立保存字段及顺序；不选择任何字段时表示总计。"
          fields={dimensionPickerFields}
          selectedKeys={groupingSet}
          onChange={keys => onGroupingSetsChange((box.groupingSets ?? []).map((item, itemIndex) => itemIndex === index ? keys : item))}
          emptyText="当前为空分组集，将生成总计"
        />
      </article>
    })}</div><button className="dataset-add-condition" type="button" aria-label="添加分组集" disabled={(box.groupingSets?.length ?? 0) >= 64} onClick={() => onGroupingSetsChange([...(box.groupingSets ?? []), []])}>添加分组集</button></section>}
    <section>
      <div className="dataset-drawer-title">
        <div><h3>聚合指标</h3><p>在弹窗中选择指标字段，并为每个指标配置计算公式；总行数使用 COUNT(*)。</p></div>
        <span>{box.metrics.length} 已选</span>
      </div>
      <GroupMetricPicker fields={availableFields} metrics={box.metrics} onChange={onMetricsChange} />
    </section>
    <footer>{error && <span className="dataset-drawer-error" role="alert">{error}</span>}<small>{graphLeaves({ kind: 'GROUP', id: box.id }, shape).length ? '该分组产物可继续连接关联组件或结束节点' : '请先连接输入组件'}</small><button type="button" onClick={onDone}>完成</button></footer>
  </aside>
}

function TransformConfigDrawer({ transform, inputs, nodes, boxes, groups, transforms, error, onNameChange, onRuleChange, onAddRule, onRemoveRule, onFilterConditionChange, onAddFilterCondition, onRemoveFilterCondition, onDone }: {
  transform: TransformBox; inputs: ProducedField[]; nodes: DesignerNode[]; boxes: RelationBox[]; groups: GroupBox[]; transforms: TransformBox[]; error: string
  onNameChange: (name: string) => void; onRuleChange: (ruleID: string, patch: Partial<GraphTransformRule>) => void
  onAddRule: () => void; onRemoveRule: (ruleID: string) => void; onDone: () => void
  onFilterConditionChange: (conditionID: string, patch: Partial<GraphFilterCondition>) => void
  onAddFilterCondition: () => void; onRemoveFilterCondition: (conditionID: string) => void
}) {
  if (transformIsFilter(transform)) {
    const filterableInputs = inputs.filter(field => field.kind !== 'METRIC' && !field.aggregation)
    const conditions = transform.conditions ?? []
    const filterOptions: Array<{ value: GraphConditionOperator; label: string }> = conditionOperatorOptions.flatMap(option =>
      option.value === 'IN' ? [option, { value: 'NOT_IN', label: '不在…中' }] : [option],
    )
    const operatorOptions = (field?: ProducedField) => filterOptions.filter(option => {
      const stringField = ['STRING', 'TEXT', 'VARCHAR', 'CHAR'].includes(field?.canonicalType.toUpperCase() || '')
      return stringField || option.value !== 'CONTAINS' && option.value !== 'NOT_CONTAINS'
    })
    return <aside className={`dataset-canvas-drawer transform ${transformColorClass(transform)}`} aria-label="配置过滤组件" onClick={event => event.stopPropagation()}>
      <header><div><span>过滤组件</span><strong>{transform.name}</strong><small>按固定值或字段之间的关系保留数据行，并生成 SQL WHERE 条件</small></div><button type="button" aria-label="保存并关闭过滤配置" onClick={onDone}>×</button></header>
      <section><h3>组件与输入</h3><div className="dataset-group-input"><label><span>组件名称</span><input aria-label="过滤组件名称" value={transform.name} onChange={event => onNameChange(event.target.value)} placeholder="例如：仅保留已支付订单" /></label><div aria-label="过滤组件输入" className={`dataset-connected-input ${transform.input ? 'connected' : 'empty'}`}><span>输入组件</span><strong>{relationInputLabel(transform.input, nodes, boxes, groups, transforms)}</strong><small>{transform.input ? `${filterableInputs.length} 个可过滤字段` : '请从上游组件拖线到输入端口'}</small></div></div></section>
      <section><div className="dataset-drawer-title"><div><h3>过滤条件</h3><p>多条条件按 AND 组合；“在…中”可用逗号或换行分隔多个值。</p></div><span>{conditions.length} 条</span></div>
        {transform.input && !filterableInputs.length && <p className="dataset-relation-pending">当前输入只有聚合指标；WHERE 过滤仅支持明细字段，请调整组件位置。</p>}
        <div className="dataset-transform-rules dataset-filter-conditions">{conditions.map((condition, index) => {
          const field = filterableInputs.find(item => item.key === condition.inputKey)
          const needsValue = filterOperatorNeedsValue(condition.operator)
          const collection = condition.operator === 'IN' || condition.operator === 'NOT_IN'
          const valueMode = condition.valueMode ?? 'LITERAL'
          const comparableFields = filterableInputs.filter(item =>
            item.key !== condition.inputKey && filterFieldsAreCompatible(field, item),
          )
          return <article key={condition.id}>
            <header><strong>条件 {index + 1}{index > 0 ? ' · AND' : ''}</strong><button type="button" aria-label={`删除过滤条件 ${index + 1}`} onClick={() => onRemoveFilterCondition(condition.id)}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header>
            <div className="dataset-transform-rule-grid">
              <label><span>过滤字段</span><select aria-label={`过滤条件 ${index + 1} 字段`} value={condition.inputKey} onChange={event => {
                const inputKey = event.target.value
                const nextField = filterableInputs.find(item => item.key === inputKey)
                const currentRight = filterableInputs.find(item => item.key === condition.value)
                onFilterConditionChange(condition.id, {
                  inputKey,
                  ...(valueMode === 'FIELD' && (!currentRight || currentRight.key === inputKey || !filterFieldsAreCompatible(nextField, currentRight)) ? { value: '' } : {}),
                })
              }}><option value="">选择字段</option>{filterableInputs.map(item => <option key={item.key} value={item.key}>{graphProducedFieldLabel(item)}</option>)}</select></label>
              <label><span>比较方式</span><select aria-label={`过滤条件 ${index + 1} 运算符`} value={condition.operator} onChange={event => {
                const operator = event.target.value as GraphConditionOperator
                onFilterConditionChange(condition.id, {
                  operator,
                  ...(!filterOperatorNeedsValue(operator) ? { value: '', valueMode: 'LITERAL' as const } : {}),
                  ...(condition.valueMode === 'FIELD' && !filterOperatorSupportsField(operator) ? { value: '', valueMode: 'LITERAL' as const } : {}),
                })
              }}>{operatorOptions(field).map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select></label>
              {needsValue && !collection && <label><span>比较对象</span><select aria-label={`过滤条件 ${index + 1} 比较对象`} value={valueMode} onChange={event => onFilterConditionChange(condition.id, { valueMode: event.target.value as GraphFilterCondition['valueMode'], value: '' })}><option value="LITERAL">固定值</option><option value="FIELD">其他字段</option></select></label>}
              {needsValue && valueMode === 'FIELD' && !collection
                ? <label className="dataset-filter-value"><span>比较字段</span><select aria-label={`过滤条件 ${index + 1} 比较字段`} value={condition.value} onChange={event => onFilterConditionChange(condition.id, { value: event.target.value })}><option value="">选择类型兼容的字段</option>{comparableFields.map(item => <option key={item.key} value={item.key}>{graphProducedFieldLabel(item)}</option>)}</select><small>{field ? `仅显示与“${graphProducedFieldLabel(field)}”类型兼容的其他字段` : '先选择过滤字段'}</small></label>
                : needsValue && <label className="dataset-filter-value"><span>{collection ? '候选值' : '比较值'}</span><textarea aria-label={`过滤条件 ${index + 1} 值`} value={condition.value} rows={collection ? 3 : 1} placeholder={collection ? '例如：华东, 华南' : '输入要匹配的值'} onChange={event => onFilterConditionChange(condition.id, { value: event.target.value })} /><small>{field ? graphProducedFieldLabel(field) : '先选择过滤字段'}</small></label>}
            </div>
          </article>
        })}</div>
        <button className="dataset-add-condition" type="button" disabled={!transform.input || !filterableInputs.length} onClick={onAddFilterCondition}>添加过滤条件</button>
      </section>
      <footer>{error && <span className="dataset-drawer-error" role="alert">{error}</span>}<small>过滤后的字段保持不变，可继续连接其他组件或结束节点</small><button type="button" onClick={onDone}>完成</button></footer>
    </aside>
  }
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
      if (rule.fallbackMode === 'CURRENT_DATE' && !dateCanonicalTypes.has(source?.canonicalType.toUpperCase() || '')) {
        patch.fallbackMode = 'LITERAL'
      }
      if (rule.fallbackMode !== 'FIELD' && (rule.fallbackValue === undefined || rule.fallbackValue === defaultFallbackValue(previous))) {
        patch.fallbackValue = defaultFallbackValue(source)
      }
    }
    onRuleChange(rule.id, patch)
  }
  const dateDiffFieldValues = (rule: GraphTransformRule) => {
    let fieldIndex = 0
    const startDateSource = rule.startDateSource || 'FIELD'
    const endDateSource = rule.endDateSource || 'FIELD'
    const startKey = startDateSource === 'FIELD' ? rule.inputKeys[fieldIndex++] || '' : ''
    const endKey = endDateSource === 'FIELD' ? rule.inputKeys[fieldIndex] || '' : ''
    return { startDateSource, endDateSource, startKey, endKey }
  }
  const changeDateDiffSource = (rule: GraphTransformRule, side: 'START' | 'END', source: NonNullable<GraphTransformRule['startDateSource']>) => {
    const current = dateDiffFieldValues(rule)
    const startDateSource = side === 'START' ? source : current.startDateSource
    const endDateSource = side === 'END' ? source : current.endDateSource
    const startKey = startDateSource === 'FIELD' ? current.startKey || options[0]?.key || '' : ''
    const endKey = endDateSource === 'FIELD' ? current.endKey || options[1]?.key || options[0]?.key || '' : ''
    onRuleChange(rule.id, {
      startDateSource,
      endDateSource,
      inputKeys: [...(startDateSource === 'FIELD' ? [startKey] : []), ...(endDateSource === 'FIELD' ? [endKey] : [])],
      ...(startDateSource === 'CURRENT_DATE' ? { replaceSourceKey: undefined } : {}),
    })
  }
  const changeDateDiffField = (rule: GraphTransformRule, side: 'START' | 'END', key: string) => {
    const current = dateDiffFieldValues(rule)
    const startKey = side === 'START' ? key : current.startKey
    const endKey = side === 'END' ? key : current.endKey
    onRuleChange(rule.id, {
      inputKeys: [
        ...(current.startDateSource === 'FIELD' ? [startKey] : []),
        ...(current.endDateSource === 'FIELD' ? [endKey] : []),
      ],
      ...(side === 'START' && rule.replaceSourceKey === current.startKey ? { replaceSourceKey: key } : {}),
    })
  }
  const changeOperation = (rule: GraphTransformRule, operation: GraphTransformOperation) => {
    const fallbackMode = operation === 'COALESCE' ? rule.fallbackMode || 'LITERAL' : rule.fallbackMode
    const dateSource = operation === 'DATE_EXTRACT' || operation === 'DATE_START' || operation === 'DATE_END' ? 'FIELD' as const : undefined
    const startDateSource = operation === 'DATE_DIFF' ? 'FIELD' as const : undefined
    const endDateSource = operation === 'DATE_DIFF' ? 'FIELD' as const : undefined
    const count = transformRuleInputCount(operation, fallbackMode, dateSource, startDateSource, endDateSource)
    const first = rule.inputKeys[0] || options[0]?.key || ''
    const inputKeys = count === 0 ? [] : count === 1 ? [first] : [first, rule.inputKeys[1] || options[1]?.key || first]
    const source = inputs.find(field => field.key === first)
    const canonicalType = operation === 'WINDOW' || operation === 'DATE_DIFF' || operation === 'DATE_EXTRACT' ? 'INTEGER' : operation === 'CURRENT_DATE' || operation === 'DATE_START' || operation === 'DATE_END' ? 'DATE' : operation === 'CAST' ? rule.targetType || 'STRING' : operation === 'DATE_FORMAT' ? 'STRING' : operation === 'DATE_TRUNC' ? source?.canonicalType || 'DATE' : operation === 'COALESCE' ? source?.canonicalType || 'STRING' : ['ADD', 'SUBTRACT', 'MULTIPLY', 'DIVIDE', 'ROUND', 'ABS', 'FLOOR', 'CEIL'].includes(operation) ? 'DECIMAL' : 'STRING'
    onRuleChange(rule.id, {
      operation, inputKeys, dateSource, startDateSource, endDateSource, output: { ...rule.output, canonicalType },
      ...(count === 0 ? { replaceSourceKey: undefined } : {}),
      ...(operation === 'WINDOW' ? {
        windowFunction: rule.windowFunction || 'ROW_NUMBER',
        partitionByKeys: rule.partitionByKeys?.length ? rule.partitionByKeys : options[0] ? [options[0].key] : [],
        orderBy: rule.orderBy?.length ? rule.orderBy : options[1] || options[0] ? [{ id: `window_order_${rule.id}_1`, key: (options[1] || options[0]).key, direction: 'ASC' }] : [],
      } : {}),
      ...(operation === 'DATE_FORMAT' || operation === 'DATE_TRUNC' ? { unit: rule.unit || 'DAY' } : {}),
      ...(operation === 'DATE_DIFF' || operation === 'DATE_EXTRACT' || operation === 'DATE_START' || operation === 'DATE_END' ? {
        unit: dateCalculationDefaultUnit(operation),
      } : {}),
      ...(operation === 'CAST' ? { targetType: rule.targetType || 'STRING' } : {}),
      ...(operation === 'CASE' ? { conditionOperator: rule.conditionOperator || 'EQUALS', matchValue: rule.matchValue ?? '', conditionValues: rule.conditionValues?.length ? rule.conditionValues : [{ id: `condition_value_${rule.id}_1`, mode: 'LITERAL' as const, value: '' }], thenMode: rule.thenMode || 'LITERAL', thenValue: rule.thenValue ?? '', elseMode: rule.elseMode || 'LITERAL', elseValue: rule.elseValue ?? '' } : {}),
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
  const windowEditor = (rule: GraphTransformRule, ruleIndex: number) => {
    const partitions = new Set(rule.partitionByKeys ?? [])
    const orders = rule.orderBy ?? []
    const aggregateWindow = !rankingWindowFunctions.has(rule.windowFunction || 'ROW_NUMBER')
    const windowValueOptions = rule.windowFunction === 'SUM' || rule.windowFunction === 'AVG'
      ? inputs.filter(field => numericCanonicalTypes.has(field.canonicalType.toUpperCase()))
      : inputs
    const updateOrder = (id: string, patch: Partial<(typeof orders)[number]>) => onRuleChange(rule.id, { orderBy: orders.map(item => item.id === id ? { ...item, ...patch } : item) })
    return <div className="dataset-window-config" aria-label={`规则 ${ruleIndex + 1} OVER 配置`}>
      <label><span>窗口函数</span><select aria-label={`规则 ${ruleIndex + 1} 窗口函数`} value={rule.windowFunction || 'ROW_NUMBER'} onChange={event => {
        const windowFunction = event.target.value as NonNullable<GraphTransformRule['windowFunction']>
        const ranking = rankingWindowFunctions.has(windowFunction)
        const valueCandidates = windowFunction === 'SUM' || windowFunction === 'AVG'
          ? inputs.filter(field => numericCanonicalTypes.has(field.canonicalType.toUpperCase()))
          : inputs
        const valueKey = ranking ? undefined : rule.windowValueKey && valueCandidates.some(field => field.key === rule.windowValueKey) ? rule.windowValueKey : valueCandidates[0]?.key
        const valueField = inputs.find(field => field.key === valueKey)
        const generatedNames: Record<NonNullable<GraphTransformRule['windowFunction']>, string> = {
          ROW_NUMBER: '分区行号', RANK: '分区排名', DENSE_RANK: '分区密集排名',
          SUM: '分区合计', AVG: '分区平均', COUNT: '分区计数', MIN: '分区最小值', MAX: '分区最大值',
        }
        const generatedCodes = Object.fromEntries(Object.keys(generatedNames).map(key => [key, `partition_${key.toLowerCase()}`])) as Record<NonNullable<GraphTransformRule['windowFunction']>, string>
        const generatedNameValues = Object.values(generatedNames)
        const generatedCodeValues = Object.values(generatedCodes)
        onRuleChange(rule.id, {
          windowFunction,
          windowValueKey: valueKey,
          output: {
            ...rule.output,
            name: generatedNameValues.includes(rule.output.name) ? generatedNames[windowFunction] : rule.output.name,
            code: generatedCodeValues.includes(rule.output.code) ? generatedCodes[windowFunction] : rule.output.code,
            canonicalType: ranking || windowFunction === 'COUNT' ? 'INTEGER' : windowFunction === 'SUM' || windowFunction === 'AVG' ? 'DECIMAL' : valueField?.canonicalType || 'STRING',
          },
        })
      }}><optgroup label="排名函数"><option value="ROW_NUMBER">ROW_NUMBER（连续行号）</option><option value="RANK">RANK（并列跳号）</option><option value="DENSE_RANK">DENSE_RANK（并列不跳号）</option></optgroup><optgroup label="组内聚合"><option value="SUM">SUM（分区合计）</option><option value="AVG">AVG（分区平均）</option><option value="COUNT">COUNT（分区计数）</option><option value="MIN">MIN（分区最小值）</option><option value="MAX">MAX（分区最大值）</option></optgroup></select></label>
      {aggregateWindow && <label><span>计算字段</span><select aria-label={`规则 ${ruleIndex + 1} 窗口计算字段`} value={rule.windowValueKey || ''} onChange={event => {
        const windowValueKey = event.target.value
        const field = inputs.find(item => item.key === windowValueKey)
        onRuleChange(rule.id, {
          windowValueKey,
          output: {
            ...rule.output,
            canonicalType: rule.windowFunction === 'COUNT' ? 'INTEGER' : rule.windowFunction === 'SUM' || rule.windowFunction === 'AVG' ? 'DECIMAL' : field?.canonicalType || 'STRING',
          },
        })
      }}><option value="">选择字段</option>{windowValueOptions.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select><small>{rule.windowFunction === 'SUM' || rule.windowFunction === 'AVG' ? '仅显示数值字段' : '在每个分区内计算该字段'}</small></label>}
      <fieldset><legend>PARTITION BY 分区字段</legend><small>同一组内独立计算排名；至少选择一个字段。</small><div>{inputs.map(field => <label key={field.key}><input type="checkbox" aria-label={`规则 ${ruleIndex + 1} 分区字段 ${field.code}`} checked={partitions.has(field.key)} onChange={event => {
        const partitionByKeys = event.target.checked ? [...partitions, field.key] : [...partitions].filter(key => key !== field.key)
        onRuleChange(rule.id, { partitionByKeys })
      }} /><span>{field.name}<small>{field.code}</small></span></label>)}</div></fieldset>
      <fieldset><legend>ORDER BY 排序字段</legend><small>按声明顺序决定排名；可配置多个排序字段。</small>{orders.map((item, orderIndex) => <div className="dataset-window-order" key={item.id}>
        <select aria-label={`规则 ${ruleIndex + 1} 排序字段 ${orderIndex + 1}`} value={item.key} onChange={event => updateOrder(item.id, { key: event.target.value })}><option value="">选择字段</option>{inputs.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select>
        <select aria-label={`规则 ${ruleIndex + 1} 排序方向 ${orderIndex + 1}`} value={item.direction} onChange={event => updateOrder(item.id, { direction: event.target.value as 'ASC' | 'DESC' })}><option value="ASC">升序 ASC</option><option value="DESC">降序 DESC</option></select>
        <button type="button" aria-label={`删除规则 ${ruleIndex + 1} 排序字段 ${orderIndex + 1}`} disabled={orders.length === 1} onClick={() => onRuleChange(rule.id, { orderBy: orders.filter(order => order.id !== item.id) })}>×</button>
      </div>)}<button type="button" onClick={() => onRuleChange(rule.id, { orderBy: [...orders, { id: `window_order_${Date.now().toString(36)}`, key: inputs[0]?.key || '', direction: 'ASC' }] })}>＋ 添加排序字段</button></fieldset>
      <output><code>{rule.windowFunction || 'ROW_NUMBER'}({aggregateWindow ? '字段' : ''}) OVER (PARTITION BY … ORDER BY …)</code><small>系统保存结构化表达式，不保存或拼接自定义 SQL。</small></output>
    </div>
  }
  return <aside className={`dataset-canvas-drawer transform ${transformColorClass(transform)}`} aria-label={`配置${componentLabel}`} onClick={event => event.stopPropagation()}>
    <header><div><span>{componentLabel}</span><strong>{transform.name}</strong><small>{componentMeta?.description || '把上游字段转换为可继续使用的新字段'}</small></div><button type="button" aria-label="保存并关闭字段处理配置" onClick={onDone}>×</button></header>
    <section><h3>组件与输入</h3><div className="dataset-group-input"><label><span>产物名称</span><input aria-label="字段处理产物名称" value={transform.name} onChange={event => onNameChange(event.target.value)} placeholder="例如：订单日期标准化" /></label><div aria-label="字段处理组件输入" className={`dataset-connected-input ${transform.input ? 'connected' : 'empty'}`}><span>输入组件</span><strong>{relationInputLabel(transform.input, nodes, boxes, groups, transforms)}</strong><small>{transform.input ? `${inputs.length} 个可用字段` : '请从上游组件拖线到输入端口'}</small></div></div></section>
    <section><div className="dataset-drawer-title"><div><h3>{transform.family === 'WINDOW' ? '窗口规则' : '转换规则'}</h3><p>{transform.family === 'WINDOW' ? '配置窗口函数、PARTITION BY 分区字段和 ORDER BY 排序字段；每条规则生成一个排名或组内聚合字段。' : transformComponentTypeFor(transform) === 'DATE_CALCULATION' ? '日期差的开始、结束日期及周期边界都可选择输入字段或 SQL CURRENT_DATE。' : transform.family === 'NULL' ? 'NULL 可用固定值、其他字段或 SQL CURRENT_DATE 填充。' : transformComponentTypeFor(transform) === 'TEXT_CONCAT' ? '按字段一、连接符、字段二的顺序生成拼接结果。' : transformComponentTypeFor(transform) === 'TEXT_SUBSTRING' ? '按从 1 开始的字符位置与长度提取文本。' : transform.family === 'CONDITION' ? '选择比较方式，命中和未命中分支可输出固定值、原字段或 SQL CURRENT_DATE。' : transformComponentTypeFor(transform) === 'NUMBER_ARITHMETIC' ? '选择加减乘除，并指定参与运算的两个数值字段。' : transformComponentTypeFor(transform) === 'NUMBER_ROUNDING' ? '可选择四舍五入、向下取整或向上取整。' : '每条规则生成一个新字段，也可替换原字段。'}</p></div><span>{transform.rules.length} 条</span></div>{transform.input && !options.length && <p className="dataset-relation-pending">当前输入没有符合该处理类型的字段，请更换输入或先使用类型转换组件。</p>}
      <div className="dataset-transform-rules">{transform.rules.map((rule, index) => <article key={rule.id}>
        <header><strong>规则 {index + 1}</strong><button type="button" aria-label={`删除转换规则 ${index + 1}`} onClick={() => onRemoveRule(rule.id)}><XIcon aria-hidden="true" size={14} weight="bold" /></button></header>
        <div className="dataset-transform-rule-grid">
          <label><span>处理逻辑</span><select aria-label={`规则 ${index + 1} 处理逻辑`} value={rule.operation} disabled={operations.length === 1} onChange={event => changeOperation(rule, event.target.value as GraphTransformOperation)}>{operations.map(operation => <option key={operation} value={operation}>{transformOperationLabel[operation]}</option>)}</select></label>
          {(rule.operation === 'DATE_EXTRACT' || rule.operation === 'DATE_START' || rule.operation === 'DATE_END') && <label><span>日期来源</span><select aria-label={`规则 ${index + 1} 日期来源`} value={rule.dateSource || 'FIELD'} onChange={event => {
            const nextSource = event.target.value as NonNullable<GraphTransformRule['dateSource']>
            onRuleChange(rule.id, { dateSource: nextSource, inputKeys: nextSource === 'CURRENT_DATE' ? [] : [rule.inputKeys[0] || options[0]?.key || ''], ...(nextSource === 'CURRENT_DATE' ? { replaceSourceKey: undefined } : {}) })
          }}><option value="FIELD">输入日期字段</option><option value="CURRENT_DATE">SQL CURRENT_DATE</option></select></label>}
          {rule.operation === 'DATE_DIFF' && (() => {
            const dates = dateDiffFieldValues(rule)
            return <>
              <label><span>开始日期来源</span><select aria-label={`规则 ${index + 1} 开始日期来源`} value={dates.startDateSource} onChange={event => changeDateDiffSource(rule, 'START', event.target.value as NonNullable<GraphTransformRule['startDateSource']>)}><option value="FIELD">输入日期字段</option><option value="CURRENT_DATE">SQL CURRENT_DATE</option></select></label>
              {dates.startDateSource === 'FIELD' && <label><span>开始日期</span><select aria-label={`规则 ${index + 1} 开始日期字段`} value={dates.startKey} onChange={event => changeDateDiffField(rule, 'START', event.target.value)}><option value="">选择字段</option>{options.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select></label>}
              <label><span>结束日期来源</span><select aria-label={`规则 ${index + 1} 结束日期来源`} value={dates.endDateSource} onChange={event => changeDateDiffSource(rule, 'END', event.target.value as NonNullable<GraphTransformRule['endDateSource']>)}><option value="FIELD">输入日期字段</option><option value="CURRENT_DATE">SQL CURRENT_DATE</option></select></label>
              {dates.endDateSource === 'FIELD' && <label><span>结束日期</span><select aria-label={`规则 ${index + 1} 结束日期字段`} value={dates.endKey} onChange={event => changeDateDiffField(rule, 'END', event.target.value)}><option value="">选择字段</option>{options.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select></label>}
            </>
          })()}
          {rule.operation !== 'WINDOW' && rule.operation !== 'CURRENT_DATE' && rule.operation !== 'DATE_DIFF' && transformRuleInputCount(rule.operation, rule.fallbackMode, rule.dateSource, rule.startDateSource, rule.endDateSource) > 0 && <label><span>输入字段</span><select aria-label={`规则 ${index + 1} 输入字段 1`} value={rule.inputKeys[0] || ''} onChange={event => changePrimaryInput(rule, event.target.value)}><option value="">选择字段</option>{options.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select></label>}
          {rule.operation === 'WINDOW' && windowEditor(rule, index)}
          {rule.operation === 'COALESCE' && <label><span>补值来源</span><select aria-label={`规则 ${index + 1} 补值来源`} value={rule.fallbackMode || 'LITERAL'} onChange={event => onRuleChange(rule.id, { fallbackMode: event.target.value as GraphTransformRule['fallbackMode'] })}><option value="LITERAL">固定值</option><option value="FIELD">其他字段</option><option value="CURRENT_DATE" disabled={!dateCanonicalTypes.has(inputs.find(field => field.key === rule.inputKeys[0])?.canonicalType.toUpperCase() || '')}>SQL CURRENT_DATE（日期字段）</option></select></label>}
          {rule.operation !== 'DATE_DIFF' && transformRuleInputCount(rule.operation, rule.fallbackMode, rule.dateSource, rule.startDateSource, rule.endDateSource) === 2 && <label><span>{rule.operation === 'COALESCE' ? '补值字段' : rule.operation === 'CONCAT' ? '合并字段' : '第二字段'}</span><select aria-label={`规则 ${index + 1} 输入字段 2`} value={rule.inputKeys[1] || ''} onChange={event => onRuleChange(rule.id, { inputKeys: [rule.inputKeys[0] || '', event.target.value] })}><option value="">选择字段</option>{options.map(field => <option key={field.key} value={field.key}>{graphProducedFieldLabel(field)}</option>)}</select></label>}
          {rule.operation === 'COALESCE' && (rule.fallbackMode || 'LITERAL') === 'LITERAL' && <label><span>填充值</span><input aria-label={`规则 ${index + 1} 空值填充值`} value={rule.fallbackValue ?? ''} onChange={event => onRuleChange(rule.id, { fallbackValue: event.target.value })} /><small>仅当输入字段为 NULL 时使用该值</small></label>}
          {rule.operation === 'COALESCE' && rule.fallbackMode === 'CURRENT_DATE' && <output><code>CURRENT_DATE</code><small>生成 SQL 时使用数据库原生 CURRENT_DATE</small></output>}
          {rule.operation === 'DATE_FORMAT' && <label><span>输出格式</span><select aria-label={`规则 ${index + 1} 输出格式`} value={rule.unit || 'DAY'} onChange={event => { const unit = event.target.value as DateFormatUnit; onRuleChange(rule.id, { unit, output: dateFormatOutputForUnit(rule, inputs.find(field => field.key === rule.inputKeys[0]), unit) }) }}>{dateFormatOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select><small>输出示例：{dateFormatMeta[(rule.unit as DateFormatUnit) || 'DAY'].example}</small></label>}
          {(rule.operation === 'DATE_DIFF' || rule.operation === 'DATE_EXTRACT' || rule.operation === 'DATE_START' || rule.operation === 'DATE_END') && <label><span>{rule.operation === 'DATE_DIFF' ? '差值单位' : rule.operation === 'DATE_EXTRACT' ? '日期部分' : '周期边界'}</span><select aria-label={`规则 ${index + 1} 日期计算单位`} value={rule.unit || dateCalculationDefaultUnit(rule.operation)} onChange={event => onRuleChange(rule.id, { unit: event.target.value as GraphTransformRule['unit'] })}>{dateCalculationUnitOptions[rule.operation].map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select>{rule.operation === 'DATE_DIFF' && <small>结果方向：结束日期 - 开始日期</small>}</label>}
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
            <label><span>命中值来源</span><select aria-label={`规则 ${index + 1} 命中值来源`} value={rule.thenMode || 'LITERAL'} onChange={event => onRuleChange(rule.id, { thenMode: event.target.value as GraphTransformRule['thenMode'] })}><option value="LITERAL">固定值</option><option value="FIELD">原字段</option><option value="CURRENT_DATE">SQL CURRENT_DATE</option></select></label>
            {(rule.thenMode || 'LITERAL') === 'LITERAL'
              ? <label><span>命中时输出</span><input aria-label={`规则 ${index + 1} 命中值`} value={rule.thenValue || ''} onChange={event => onRuleChange(rule.id, { thenValue: event.target.value })} /></label>
              : rule.thenMode === 'FIELD' ? <output><code>原字段</code><small>命中时保留第一个输入字段</small></output> : <output><code>CURRENT_DATE</code><small>生成 SQL 时使用数据库原生 CURRENT_DATE</small></output>}
            <label><span>未命中值来源</span><select aria-label={`规则 ${index + 1} 未命中值来源`} value={rule.elseMode || 'LITERAL'} onChange={event => onRuleChange(rule.id, { elseMode: event.target.value as GraphTransformRule['elseMode'] })}><option value="LITERAL">固定值</option><option value="FIELD">原字段</option><option value="CURRENT_DATE">SQL CURRENT_DATE</option></select></label>
            {(rule.elseMode || 'LITERAL') === 'LITERAL'
              ? <label><span>未命中输出</span><input aria-label={`规则 ${index + 1} 默认值`} value={rule.elseValue || ''} onChange={event => onRuleChange(rule.id, { elseValue: event.target.value })} /></label>
              : rule.elseMode === 'FIELD' ? <output><code>原字段</code><small>未命中时保留第一个输入字段</small></output> : <output><code>CURRENT_DATE</code><small>生成 SQL 时使用数据库原生 CURRENT_DATE</small></output>}
          </>}
          <label><span>输出名称</span><input aria-label={`规则 ${index + 1} 输出名称`} value={rule.output.name} onChange={event => onRuleChange(rule.id, { output: { ...rule.output, name: event.target.value } })} /></label>
          <label><span>输出编码</span><input aria-label={`规则 ${index + 1} 输出编码`} value={rule.output.code} onChange={event => onRuleChange(rule.id, { output: { ...rule.output, code: event.target.value } })} /></label>
        </div>
        {rule.operation !== 'WINDOW' && rule.operation !== 'CURRENT_DATE' && rule.inputKeys[0] && <label className="dataset-transform-replace"><input type="checkbox" checked={Boolean(rule.replaceSourceKey)} onChange={event => onRuleChange(rule.id, { replaceSourceKey: event.target.checked ? rule.inputKeys[0] : undefined })} /><span>用转换结果替换第一个输入字段</span></label>}
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
  onConditionPatch: (conditionID: string, patch: { leftField?: string; rightField?: string; operator?: 'EQUALS' | 'NOT_EQUALS' | 'GT' | 'GTE' | 'LT' | 'LTE' }) => void
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
      <section><div className="dataset-drawer-title"><div><h3>关联字段</h3><p>选择槽位 1 与槽位 2 的关联键；多个字段用于复合键关联。</p></div><span>{conditions.length} 个条件</span></div><div className="dataset-join-conditions">{conditions.map((condition, index) => <div key={condition.id}><span>{index + 1}</span><select aria-label={`条件 ${index + 1} 左字段`} value={condition.leftField} onChange={event => onConditionPatch(condition.id, { leftField: event.target.value })}><option value="">选择槽位 1 字段</option>{leftFields.map(field => <option key={field.key} value={field.binding.field}>{graphProducedFieldLabel(field)}</option>)}</select><select aria-label={`条件 ${index + 1} 运算符`} value={condition.operator || 'EQUALS'} onChange={event => onConditionPatch(condition.id, { operator: event.target.value as 'EQUALS' | 'NOT_EQUALS' | 'GT' | 'GTE' | 'LT' | 'LTE' })}><option value="EQUALS">=</option><option value="NOT_EQUALS">≠</option><option value="GT">&gt;</option><option value="GTE">≥</option><option value="LT">&lt;</option><option value="LTE">≤</option></select><select aria-label={`条件 ${index + 1} 右字段`} value={condition.rightField} onChange={event => onConditionPatch(condition.id, { rightField: event.target.value })}><option value="">选择槽位 2 字段</option>{rightFields.map(field => <option key={field.key} value={field.binding.field}>{graphProducedFieldLabel(field)}</option>)}</select><button type="button" disabled={conditions.length === 1} aria-label={`删除条件 ${index + 1}`} onClick={() => onRemoveCondition(condition.id)}>×</button></div>)}</div><button className="dataset-add-condition" type="button" onClick={onAddCondition}>＋ 添加关联字段</button></section>
      <section><div className="dataset-drawer-title"><div><h3>输出字段</h3><p>勾选字段组成“{box.name}”，并作为下游组件可识别的产物。</p></div><span>{selectedOutputs.size} 已选</span></div><div className="dataset-drawer-field-list">{outputItems.map(field => <label key={field.key}><input aria-label={`关联输出 ${field.code}`} type="checkbox" checked={selectedOutputs.has(field.key)} onChange={event => onOutputChange(field.key, event.target.checked)} /><span><strong>{field.name}</strong><small>{field.producerName}</small></span></label>)}</div></section></>}
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
    <section><div className="dataset-drawer-title"><div><h3>输出字段</h3><p>选择最终对外字段；勾选后按上游稳定编码自动生成字段别名。</p></div><span>{end.outputs.length} 已选</span></div><div className="dataset-drawer-field-list configured end-fields">{availableFields.map(field => { const output = selected.get(field.key); return <div className={output ? 'selected' : ''} key={field.key}><label><input aria-label={`最终输出 ${field.code}`} type="checkbox" checked={Boolean(output)} onChange={event => onOutputChange(field, event.target.checked)} /><span><strong>{field.name}</strong><small>{field.producerName}</small></span></label>{output && <div className="dataset-product-fields generated"><output className="dataset-generated-field-alias" aria-label={`${field.code} 字段别名`}><small>字段别名</small><strong>{output.code}</strong></output></div>}</div> })}</div></section>
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
        ? <PreviewRows preview={{ queryId: '', columns: preview.data.columns, columnMetadata: preview.data.columnMetadata, rows: preview.data.rows, rowCount: preview.data.rows.length, durationMs: 0 }} />
        : preview?.error
          ? <div className="dataset-preview-diagnostic" role="alert"><div><strong>异常原因</strong><span>{preview.error}</span></div><div><strong>处理建议</strong><span>{preview.suggestion || '请完善当前组件配置并确认上游数据源可用。'}</span></div></div>
          : <div className="dataset-node-preview-state">点击刷新以加载当前组件的前 5 行数据。</div>}
  </section>
}

function PreviewRows({ preview }: { preview: DatasetPreview }) {
  if (!preview.rows.length) return <Empty>当前查询没有返回数据。</Empty>
  return <div className="dataset-preview-table-wrap"><table><thead><tr>{preview.columns.map((column, index) => {
    const metadata = preview.columnMetadata?.[index]
    const label = metadata?.name?.trim() || metadata?.physicalName?.trim() || column
    const technicalNames = [metadata?.physicalName, metadata?.code || column]
      .map(value => value?.trim()).filter((value): value is string => Boolean(value) && value !== label)
      .filter((value, valueIndex, values) => values.indexOf(value) === valueIndex)
    return <th key={`${column}-${index}`} title={metadata?.description || undefined}><strong>{label}</strong>{technicalNames.length > 0 && <small>{technicalNames.join(' · ')}</small>}</th>
  })}</tr></thead><tbody>{preview.rows.slice(0, 5).map((row, rowIndex) => <tr key={rowIndex}>{preview.columns.map((_, columnIndex) => <td key={columnIndex}>{row[columnIndex] == null ? preview.columnMetadata?.[columnIndex]?.groupingPlaceholder || '—' : String(row[columnIndex])}</td>)}</tr>)}</tbody></table></div>
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
    {transforms.map(transform => { const position = normalize(transform.position); return <div key={transform.id} className={`transform ${transformColorClass(transform)}`} style={{ left: position.x, top: position.y }}><small>{transformDisplayLabel(transform)}</small><strong>{transform.name}</strong><span>{transformIsFilter(transform) ? transform.conditions?.length ?? 0 : transform.rules.length} 条{transformIsFilter(transform) ? '条件' : '规则'}</span></div> })}
    {joins.map(join => { const position = normalize(join.position); return <div key={join.id} className="join" style={{ left: position.x, top: position.y }}><small>关联组件</small><strong>{join.name}</strong></div> })}
    {end && (() => { const position = normalize(end.position); return <div className="end" style={{ left: position.x, top: position.y }}><small>结束节点</small><strong>{end.name}</strong><span>{end.outputs.length} 个输出</span></div> })()}
    {!designer && <div className="legacy-note">旧版本未保存组件坐标，仅展示可恢复的数据节点。</div>}
  </div>
}

function MaterializationRunPanel({ dataset, run, loading, busy, error, onRetry, onStop, onClose }: {
  dataset: DatasetSummary
  run: DatasetDAGRunDetail | null
  loading: boolean
  busy: boolean
  error: string
  onRetry: () => void
  onStop: () => void
  onClose: () => void
}) {
  const statusLabels: Record<string, string> = {
    QUEUED: '排队中', RUNNING: '执行中', SUCCEEDED: '成功', FAILED: '失败', CANCELLED: '已取消',
    PENDING: '待执行', SKIPPED: '已跳过',
  }
  const dateText = (value?: string) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'
  const durationText = (seconds = 0) => seconds >= 3600
    ? `${Math.floor(seconds / 3600)}小时${Math.floor(seconds % 3600 / 60)}分`
    : seconds >= 60 ? `${Math.floor(seconds / 60)}分${seconds % 60}秒` : `${seconds}秒`
  if (loading && !run) return <Empty>正在加载运行诊断…</Empty>
  if (!run) return <div className="dataset-materialization-panel">{error && <div className="dataset-center-feedback error" role="alert">{error}</div>}<Empty>暂时无法读取该次物化运行。</Empty><footer><button className="quiet-button" type="button" onClick={onClose}>关闭</button></footer></div>
  const succeededNodes = run.succeededNodes ?? run.nodes.filter(node => node.status === 'SUCCEEDED').length
  const failedNodes = run.failedNodes ?? run.nodes.filter(node => node.status === 'FAILED').length
  const pendingNodes = run.pendingNodes ?? run.nodes.filter(node => !['SUCCEEDED', 'FAILED'].includes(node.status)).length
  const quality = run.quality ?? []
  const qualityFailed = quality.filter(item => item.status === 'FAILED').length
  const qualityPassed = quality.filter(item => item.status === 'PASSED').length
  const qualityLabels: Record<string, string> = {
    ROW_COUNT_NONNEGATIVE: '输出行数有效',
    OUTPUT_GRAIN_UNIQUE_NOT_NULL: '声明粒度唯一且非空',
  }
  return <div className="dataset-materialization-panel">
    <section className={`dataset-materialization-summary ${run.status.toLowerCase()} ${run.slaBreached ? 'sla-breached' : ''}`}>
      <div><span className="dataset-materialization-status">{statusLabels[run.status] ?? run.status}</span><h3>{run.partialSuccess ? '部分节点已成功，当前可用快照未切换' : dagRunLabel(run)}</h3><p>{run.errorMessage || (activeDAGRunStatuses.has(run.status) ? '系统正在按冻结的发布版本执行；取消不会影响上一份可用快照。' : '该次运行已形成完整审计证据。')}</p></div>
      <dl>
        <div><dt>运行 ID</dt><dd title={run.id}>{run.id.slice(0, 12)}</dd></div>
        <div><dt>运行方式</dt><dd>{run.mode === 'FULL' ? '完整替换' : run.mode === 'INCREMENTAL' ? '水位增量' : '区间回填'}</dd></div>
        <div><dt>尝试次数</dt><dd>{run.attempt} / {run.maxAttempts}</dd></div>
        <div><dt>执行耗时</dt><dd>{durationText(run.durationSeconds)}</dd></div>
        <div><dt>刷新 SLA</dt><dd className={run.slaBreached ? 'is-danger' : run.slaStatus === 'AT_RISK' ? 'is-warning' : ''}>{run.slaBreached ? '已超时' : run.slaStatus === 'AT_RISK' ? '临近截止' : run.status === 'SUCCEEDED' ? '按时完成' : '正常'}</dd></div>
        <div><dt>SLA 截止</dt><dd>{dateText(run.slaDueAt)}</dd></div>
      </dl>
    </section>
    <section className="dataset-materialization-node-summary" aria-label="物化节点统计">
      <article><small>成功节点</small><strong>{succeededNodes}</strong><span>产物已留存，失败时不会被误激活</span></article>
      <article><small>失败节点</small><strong>{failedNodes}</strong><span>{failedNodes ? '查看错误后可创建新运行重试' : '未发现失败节点'}</span></article>
      <article><small>待处理节点</small><strong>{pendingNodes}</strong><span>取消或上游失败后会安全跳过</span></article>
      <article><small>冻结输入</small><strong>{run.inputs.length}</strong><span>重试会重新校验当前发布版本</span></article>
    </section>
    <section className="dataset-materialization-nodes">
      <header><div><h3>执行节点</h3><p>逐节点展示状态、行数和可安全披露的失败原因。</p></div><span>{run.partialSuccess ? '部分成功' : `${run.nodes.length} 个节点`}</span></header>
      <div>{run.nodes.map((node, index) => <article key={node.id} className={node.status.toLowerCase()}>
        <span>{index + 1}</span><div><strong>{node.kind}</strong><small>{node.engine} · 第 {node.attempt} 次尝试</small></div>
        <em>{statusLabels[node.status] ?? node.status}</em>
        <p>{node.errorMessage || (node.outputRowCount !== undefined ? `输出 ${node.outputRowCount.toLocaleString('zh-CN')} 行` : node.status === 'SKIPPED' ? '因前序失败或取消未执行' : '运行证据已记录')}</p>
      </article>)}</div>
      {!run.nodes.length && <Empty>任务仍在排队，执行节点将在 Worker 领取后显示。</Empty>}
    </section>
    <section className="dataset-materialization-quality">
      <header><div><h3>质量证据</h3><p>展示本次运行真实执行并写入审计库的规则结果，不使用前端模拟状态。</p></div><span className={qualityFailed ? 'failed' : quality.length ? 'passed' : ''}>{qualityFailed ? `${qualityFailed} 项未通过` : quality.length ? `${qualityPassed} / ${quality.length} 通过` : '等待校验'}</span></header>
      {quality.length ? <div>{quality.map((item, index) => <article key={`${item.ruleCode}:${item.nodeId || index}`} className={item.status.toLowerCase()}>
        <span><CheckCircleIcon size={18} weight="fill" /></span>
        <div><strong>{qualityLabels[item.ruleCode] ?? item.ruleCode}</strong><small>{item.scope === 'FIELD' ? `字段 ${item.fieldId}` : '数据集级'} · 规则 V{item.ruleVersion} · {dateText(item.measuredAt)}</small></div>
        <em>{item.status === 'PASSED' ? '已通过' : item.status === 'FAILED' ? '未通过' : '已跳过'}</em>
        <p>{item.message || '规则已完成校验并保存证据。'}</p>
      </article>)}</div> : <Empty>{activeDAGRunStatuses.has(run.status) ? '质量节点完成后将在此展示校验证据。' : '该历史运行没有质量结果；新运行会记录可追溯证据。'}</Empty>}
    </section>
    <section className="dataset-materialization-safety">
      <WarningCircleIcon size={20} weight="fill" />
      <div><strong>稳定性保护已生效</strong><p>质量失败、部分成功或用户取消都不会切换 ACTIVE 快照；重试会生成新的运行 ID，并保留本次失败证据。</p></div>
    </section>
    {error && <div className="dataset-center-feedback error" role="alert">{error}</div>}
    <footer><span>发布版本 {run.datasetVersionId.slice(0, 12)} · {dataset.layer} 数据层</span><div><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>关闭</button>{activeDAGRunStatuses.has(run.status) ? <button className="dataset-stop-button" type="button" disabled={busy} onClick={onStop}>停止本次运行</button> : retryableDAGRunStatuses.has(run.status) ? <button className="primary-button" type="button" disabled={busy} onClick={onRetry}><ArrowClockwiseIcon size={16} />创建新运行重试</button> : null}</div></footer>
  </div>
}

function DatasetLifecyclePanel({ dataset, action, impact, loading, busy, error, onConfirm, onClose }: {
  dataset: DatasetSummary
  action: 'disable' | 'restore' | 'delete'
  impact: DatasetLifecycleImpact | null
  loading: boolean
  busy: boolean
  error: string
  onConfirm: () => void
  onClose: () => void
}) {
  const label = action === 'delete' ? '删除' : action === 'restore' ? '恢复' : '停用'
  const permitted = action === 'delete' ? impact?.canDelete : action === 'restore' ? impact?.canRestore : impact?.canDisable
  return <div className="dataset-lifecycle-panel">
    {loading ? <Empty>正在检查下游依赖和运行占用…</Empty> : impact ? <>
      <section className={`dataset-lifecycle-heading ${action} ${permitted ? '' : 'blocked'}`}>
        <span><WarningCircleIcon size={24} weight="fill" /></span>
        <div><h3>{permitted ? `可以安全${label}“${dataset.name}”` : `暂时不能${label}“${dataset.name}”`}</h3><p>{action === 'disable' ? '停用会清除目录的活动发布指针，但草稿、不可变发布快照、物化历史和审计全部保留。' : action === 'restore' ? '系统将恢复到停用前记录的稳定状态和精确发布版本，不会猜测或重写历史。' : '删除会隐藏目录项、废弃发布版本并排队清理物化；源库物理表不会被修改。'}</p></div>
      </section>
      <section className="dataset-lifecycle-impact" aria-label="数据集生命周期影响预览">
        <article><small>下游草稿</small><strong>{impact.downstreamDraftReferences}</strong><span>引用当前任一发布版本</span></article>
        <article><small>已发布下游</small><strong>{impact.downstreamPublishedReferences}</strong><span>需要先迁移或废弃</span></article>
        <article><small>运行中查询</small><strong>{impact.activeQueryRuns}</strong><span>结束后才能删除</span></article>
        <article><small>活动物化</small><strong>{impact.activeBuildRuns}</strong><span>{impact.materializations} 份物化待保留或清理</span></article>
      </section>
      {impact.blockers.length > 0 && <section className="dataset-lifecycle-blockers"><strong>先完成以下处理</strong><ul>{impact.blockers.map(blocker => <li key={blocker}>{blocker}</li>)}</ul><p>停用可作为可恢复的临时措施；彻底删除前需让这些计数归零。</p></section>}
      <section className="dataset-lifecycle-retention"><CheckCircleIcon size={19} weight="fill" /><div><strong>保留与恢复策略</strong><p>{action === 'delete' ? '软删除保留版本与审计；数仓物化通过后台清理队列安全释放，业务源表永不删除。' : '停用和恢复都是带乐观锁的原子操作；并发保存或发布发生时会拒绝覆盖并要求刷新。'}</p></div></section>
    </> : error ? null : <Empty>暂无生命周期影响信息。</Empty>}
    {error && <div className="dataset-center-feedback error" role="alert">{error}</div>}
    <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className={action === 'delete' ? 'dataset-delete-button' : 'primary-button'} type="button" disabled={busy || loading || !permitted} onClick={onConfirm}>{busy && !loading ? `正在${label}…` : `确认${label}`}</button></footer>
  </div>
}

function PublishedVersionHistoryPanel({ record, items, selected, usage, preview, loading, busy, confirming, error, onSelect, onStartRollback, onCancelRollback, onRollback, onClose }: {
  record: DatasetRecord | null; items: PublishedVersionSummary[]; selected: PublishedVersionRecord | null
  usage: VersionUsage | null
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
  const difference = record && selected ? publishedVersionDiff(selected, record) : null
  const downstreamImpact = (usage?.downstreamDraftReferences ?? 0) + (usage?.downstreamPublishedReferences ?? 0) + (usage?.activeQueryRuns ?? 0)
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
        {difference && <section className="dataset-version-diff" aria-label="发布版本差异和影响">
          <header><div><h3>与当前草稿的差异</h3><p>回滚会复制该快照生成新草稿，不会覆盖现有发布版本。</p></div><span className={difference.breakingChanges ? 'has-breaking' : ''}>{difference.breakingChanges ? `${difference.breakingChanges} 项破坏性变化` : '无破坏性变化'}</span></header>
          <div className="dataset-version-diff-grid">
            <article><small>字段变化</small><strong>+{difference.addedFields.length} / −{difference.removedFields.length} / ~{difference.changedFields.length}</strong><p>{[...difference.addedFields.map(name => `新增 ${name}`), ...difference.removedFields.map(name => `移除 ${name}`), ...difference.changedFields.map(name => `调整 ${name}`)].slice(0, 4).join('；') || '字段定义一致'}</p></article>
            <article><small>DAG 变化</small><strong>+{difference.addedNodes.length} / −{difference.removedNodes.length} / ~{difference.changedNodes.length}</strong><p>{[...difference.addedNodes.map(name => `新增 ${name}`), ...difference.removedNodes.map(name => `移除 ${name}`), ...difference.changedNodes.map(name => `调整 ${name}`)].slice(0, 4).join('；') || '节点定义一致'}</p></article>
            <article><small>业务元信息</small><strong>{difference.metadataChanges.length}</strong><p>{difference.metadataChanges.join('、') || '名称、说明、类型与分层一致'}</p></article>
          </div>
          <div className={`dataset-version-impact ${downstreamImpact ? 'has-impact' : ''}`}>
            <WarningCircleIcon size={19} weight="fill" /><div><strong>{downstreamImpact ? `该快照当前关联 ${downstreamImpact} 项下游使用` : '未发现当前下游占用'}</strong><p>草稿引用 {usage?.downstreamDraftReferences ?? 0} · 已发布引用 {usage?.downstreamPublishedReferences ?? 0} · 运行中查询 {usage?.activeQueryRuns ?? 0}</p></div>
          </div>
        </section>}
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
