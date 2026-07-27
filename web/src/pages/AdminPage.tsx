import {
  ArrowClockwise,
  ChartBar,
  CheckCircle,
  ClockCounterClockwise,
  ClipboardText,
  Database,
  Sparkle,
  SpinnerGap,
  Stack,
  StopCircle,
  Tag,
  X,
  XCircle,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import {
  loadDataSourceApprovalTasks,
  loadDatasetApprovalTasks,
  loadDimensionApprovalTasks,
  loadMetricApprovalTasks,
  type ApprovalCategory,
  type ApprovalTask,
  type ApprovalTaskBatch,
} from '../lib/approval-tasks'
import { currentSubject } from '../lib/auth'
import {
  backgroundTaskAPI,
  takeBackgroundTaskFocus,
  type BackgroundTask,
  type BackgroundTaskFocus,
  type BackgroundTaskPage,
  type BackgroundTaskStatus,
  type BackgroundTaskView,
} from '../lib/background-tasks'
import { dataSourceAPI, type DataSourceRecord } from '../lib/data-sources'
import { datasetAPI } from '../lib/datasets'

const sourceTypeLabels = { MYSQL: 'MySQL', ORACLE: 'Oracle', EXCEL: 'Excel / CSV' } as const
const categoryOrder: ApprovalCategory[] = ['DATA_SOURCE', 'DATASET', 'METRIC', 'DIMENSION']
const categoryCopy: Record<ApprovalCategory, { label: string; empty: string; description: string }> = {
  DATA_SOURCE: {
    label: '数据源',
    empty: '当前没有待审批的数据源发布申请',
    description: '审核连接测试通过的冻结配置，审批通过后自动上线。',
  },
  DATASET: {
    label: '数据集',
    empty: '当前没有待审批的数据集发布申请',
    description: '审核冻结的精确草稿与执行计划；批准后再启动加工。',
  },
  METRIC: {
    label: '指标',
    empty: '当前没有待审核的指标候选',
    description: '审核系统提取的指标候选，并在资产管理中心完成试算与发布。',
  },
  DIMENSION: {
    label: '维度',
    empty: '当前没有待治理的维度候选',
    description: '审核 DWS 维度勘测结果、成员索引策略与敏感性。',
  },
}
const emptyTasks = (): Record<ApprovalCategory, ApprovalTask[]> => ({
  DATA_SOURCE: [],
  DATASET: [],
  METRIC: [],
  DIMENSION: [],
})

const formatSubmittedAt = (value?: string) => {
  if (!value) return '提交时间未知'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(date)
}
const formatTaskTime = (value?: string) => {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).format(date)
}
const configText = (source: DataSourceRecord, key: string) => {
  const value = source.config?.[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value) : '—'
}
const candidatePreparationLabel: Record<string, string> = {
  LEGACY: '历史申请',
  PENDING: '批准后加工',
  SUCCEEDED: '历史准备已完成',
  PARTIAL: '历史准备部分完成',
  FAILED: '历史准备失败',
}
const metricCandidateStatusLabel: Record<string, string> = {
  READY: '规则校验通过',
  NEEDS_REVIEW: '需要人工复核',
}
const backgroundStatusCopy: Record<BackgroundTaskStatus, string> = {
  QUEUED: '等待执行',
  RUNNING: '运行中',
  SUCCEEDED: '已成功',
  PARTIAL: '部分完成',
  FAILED: '失败',
  CANCELLED: '已中止',
  SKIPPED: '已跳过',
  STALE: '已失效',
}
const emptyBackgroundPage = (): BackgroundTaskPage => ({
  items: [],
  activeCount: 0,
  generatedAt: '',
})
const backgroundFocusCopy: Record<BackgroundTaskFocus, string> = {
  DIM_MODELING: '维度建模任务',
  DWD_MODELING: '明细建模任务',
  DWS_MODELING: '主题建模任务',
}
const dimensionModelingKinds = new Set([
  'ODS_DOMAIN_CLASSIFICATION',
  'DIM_MODELING',
])
const matchesBackgroundFocus = (
  task: BackgroundTask,
  focus: BackgroundTaskFocus | null,
) => !focus || (focus === 'DWS_MODELING'
  ? task.kind === 'DWS_MODELING'
  : focus === 'DWD_MODELING'
    ? task.kind === 'DWD_FACT_MODELING'
    : dimensionModelingKinds.has(task.kind))

function taskIcon(category: ApprovalCategory) {
  if (category === 'DATA_SOURCE') return <Database size={20} weight="duotone" aria-hidden="true" />
  if (category === 'DATASET') return <Stack size={20} weight="duotone" aria-hidden="true" />
  if (category === 'METRIC') return <ChartBar size={20} weight="duotone" aria-hidden="true" />
  return <Tag size={20} weight="duotone" aria-hidden="true" />
}

function isOwnSubmission(task?: ApprovalTask) {
  return Boolean(task
    && (task.category === 'DATA_SOURCE' || task.category === 'DATASET')
    && task.submittedByCurrentUser)
}

/** 展示租户工作台，并把所有人工审批与治理入口收敛到分类任务中心。 */
export function AdminPage() {
  const navigate = useNavigate()
  const subject = currentSubject()
  const [sourceCount, setSourceCount] = useState<number | null>(null)
  const [datasetCount, setDatasetCount] = useState<number | null>(null)
  const [tasks, setTasks] = useState<Record<ApprovalCategory, ApprovalTask[]>>(emptyTasks)
  const [taskErrors, setTaskErrors] = useState<Partial<Record<ApprovalCategory, string>>>({})
  const [tasksLoading, setTasksLoading] = useState(true)
  const [queueOpen, setQueueOpen] = useState(false)
  const [activeCategory, setActiveCategory] = useState<ApprovalCategory>('DATA_SOURCE')
  const [selectedTaskKey, setSelectedTaskKey] = useState('')
  const [reviewNote, setReviewNote] = useState('')
  const [reviewError, setReviewError] = useState('')
  const [reviewNotice, setReviewNotice] = useState('')
  const [busyAction, setBusyAction] = useState('')
  const [backgroundOpen, setBackgroundOpen] = useState(false)
  const [backgroundView, setBackgroundView] = useState<BackgroundTaskView>('ACTIVE')
  const [backgroundPage, setBackgroundPage] = useState<BackgroundTaskPage>(emptyBackgroundPage)
  const [backgroundLoading, setBackgroundLoading] = useState(false)
  const [backgroundError, setBackgroundError] = useState('')
  const [backgroundNotice, setBackgroundNotice] = useState('')
  const [backgroundFocus, setBackgroundFocus] = useState<BackgroundTaskFocus | null>(null)
  const [cancellingTaskId, setCancellingTaskId] = useState('')
  const [retryingTaskId, setRetryingTaskId] = useState('')

  const loadApprovalTasks = useCallback(async () => {
    setTasksLoading(true)
    setTaskErrors({})
    const results = await Promise.allSettled([
      loadDataSourceApprovalTasks(subject),
      loadDatasetApprovalTasks(subject),
      loadMetricApprovalTasks(),
      loadDimensionApprovalTasks(),
    ])
    const nextTasks = emptyTasks()
    const nextErrors: Partial<Record<ApprovalCategory, string>> = {}
    results.forEach((result, index) => {
      const category = categoryOrder[index]
      if (result.status === 'fulfilled') {
        const batch: ApprovalTaskBatch = result.value
        nextTasks[batch.category] = batch.tasks
        if (batch.category === 'DATA_SOURCE') setSourceCount(batch.assetCount ?? 0)
        if (batch.category === 'DATASET') setDatasetCount(batch.assetCount ?? 0)
        return
      }
      nextErrors[category] = result.reason instanceof Error ? result.reason.message : '加载失败'
      if (category === 'DATA_SOURCE') setSourceCount(null)
      if (category === 'DATASET') setDatasetCount(null)
    })
    setTasks(nextTasks)
    setTaskErrors(nextErrors)
    setTasksLoading(false)
  }, [subject])

  useEffect(() => {
    const timeout = window.setTimeout(() => void loadApprovalTasks(), 0)
    return () => window.clearTimeout(timeout)
  }, [loadApprovalTasks])

  const loadBackgroundTasks = useCallback(async (
    view: BackgroundTaskView,
    quiet = false,
  ) => {
    if (!quiet) setBackgroundLoading(true)
    setBackgroundError('')
    try {
      setBackgroundPage(await backgroundTaskAPI.list(view))
    } catch (cause) {
      setBackgroundError(cause instanceof Error ? cause.message : '后台任务加载失败')
    } finally {
      if (!quiet) setBackgroundLoading(false)
    }
  }, [])

  useEffect(() => {
    const timeout = window.setTimeout(() => void loadBackgroundTasks('ACTIVE', true), 0)
    return () => window.clearTimeout(timeout)
  }, [loadBackgroundTasks])

  useEffect(() => {
    if (!backgroundOpen) return undefined
    const interval = window.setInterval(
      () => void loadBackgroundTasks(backgroundView, true),
      3000,
    )
    return () => window.clearInterval(interval)
  }, [backgroundOpen, backgroundView, loadBackgroundTasks])

  const totalTasks = useMemo(
    () => categoryOrder.reduce((total, category) => total + tasks[category].length, 0),
    [tasks],
  )
  const visibleBackgroundTasks = useMemo(
    () => backgroundPage.items.filter(task => matchesBackgroundFocus(task, backgroundFocus)),
    [backgroundFocus, backgroundPage.items],
  )
  const activeTasks = tasks[activeCategory]
  const selectedTask = useMemo(
    () => activeTasks.find(task => task.key === selectedTaskKey) || activeTasks[0],
    [activeTasks, selectedTaskKey],
  )

  const resetReviewState = () => {
    setReviewNote('')
    setReviewError('')
    setReviewNotice('')
  }

  const openApprovalQueue = () => {
    const firstCategory = categoryOrder.find(category => tasks[category].length) ?? 'DATA_SOURCE'
    setActiveCategory(firstCategory)
    setSelectedTaskKey(tasks[firstCategory][0]?.key || '')
    resetReviewState()
    setQueueOpen(true)
  }

  const chooseCategory = (category: ApprovalCategory) => {
    setActiveCategory(category)
    setSelectedTaskKey(tasks[category][0]?.key || '')
    resetReviewState()
  }

  const chooseTask = (task: ApprovalTask) => {
    setSelectedTaskKey(task.key)
    resetReviewState()
  }

  const openBackgroundTasks = () => {
    const focus = takeBackgroundTaskFocus()
    const view: BackgroundTaskView = focus ? 'ALL' : 'ACTIVE'
    setBackgroundOpen(true)
    setBackgroundFocus(focus)
    setBackgroundView(view)
    setBackgroundNotice('')
    void loadBackgroundTasks(view)
  }

  const chooseBackgroundView = (view: BackgroundTaskView) => {
    setBackgroundFocus(null)
    setBackgroundView(view)
    setBackgroundNotice('')
    void loadBackgroundTasks(view)
  }

  const cancelBackgroundTask = async (task: BackgroundTask) => {
    if (!task.canCancel || !window.confirm(`确认中止“${task.name}”的${task.kindLabel}任务吗？`)) return
    setCancellingTaskId(task.id)
    setBackgroundError('')
    setBackgroundNotice('')
    try {
      await backgroundTaskAPI.cancel(task)
      setBackgroundNotice(`“${task.name}”的${task.kindLabel}任务已中止`)
      await loadBackgroundTasks(backgroundView, true)
    } catch (cause) {
      setBackgroundError(cause instanceof Error ? cause.message : '中止后台任务失败')
    } finally {
      setCancellingTaskId('')
    }
  }

  const retryBackgroundTask = async (task: BackgroundTask) => {
    if (!task.canRetry || !window.confirm(`确认重试“${task.name}”的${task.kindLabel}任务吗？`)) return
    setRetryingTaskId(task.id)
    setBackgroundError('')
    setBackgroundNotice('')
    try {
      await backgroundTaskAPI.retry(task)
      setBackgroundNotice(`“${task.name}”的${task.kindLabel}任务已重新提交`)
      await loadBackgroundTasks(backgroundView, true)
    } catch (cause) {
      setBackgroundError(cause instanceof Error ? cause.message : '重试后台任务失败')
    } finally {
      setRetryingTaskId('')
    }
  }

  const finishTask = (task: ApprovalTask, message: string) => {
    const remaining = tasks[task.category].filter(item => item.key !== task.key)
    setTasks(current => ({ ...current, [task.category]: remaining }))
    setSelectedTaskKey(remaining[0]?.key || '')
    setReviewNote('')
    setReviewError('')
    setReviewNotice(message)
  }

  const approve = async () => {
    if (!selectedTask || selectedTask.category === 'METRIC' || selectedTask.category === 'DIMENSION') return
    setBusyAction('approve')
    setReviewError('')
    try {
      if (selectedTask.category === 'DATA_SOURCE') {
        const source = selectedTask.source
        if (!source.reviewRequestId || !source.reviewRequestVersion) return
        await dataSourceAPI.approvePublicationRequest(
          source.id, source.reviewRequestId, source.reviewRequestVersion, reviewNote.trim(),
        )
        finishTask(selectedTask, `“${source.name}”已审批通过，测试版本已自动上线`)
      } else {
        const { dataset, request } = selectedTask
        await datasetAPI.approvePublication(dataset.id, request.id, request.version, reviewNote.trim())
        await loadBackgroundTasks('ACTIVE', true)
        finishTask(selectedTask, `“${dataset.name}”已审批通过；不可变发布版本已生成，后台加工任务已启动`)
      }
    } catch (cause) {
      if (cause instanceof RequestError &&
          cause.detail.code === 'DATASET_PUBLICATION_REQUEST_CANCELLED') {
        await loadApprovalTasks()
        setReviewNotice('数据集草稿已变更，原审批申请已自动取消；请等待申请人按最新草稿重新提交')
      } else {
        setReviewError(cause instanceof Error ? cause.message : '审批通过失败')
      }
    } finally {
      setBusyAction('')
    }
  }

  const reject = async () => {
    if (!selectedTask || selectedTask.category === 'METRIC' || selectedTask.category === 'DIMENSION') return
    if (!reviewNote.trim()) {
      setReviewError('驳回时必须填写明确原因，便于申请人修改后重新提交')
      return
    }
    setBusyAction('reject')
    setReviewError('')
    try {
      if (selectedTask.category === 'DATA_SOURCE') {
        const source = selectedTask.source
        if (!source.reviewRequestId || !source.reviewRequestVersion) return
        await dataSourceAPI.rejectPublicationRequest(
          source.id, source.reviewRequestId, source.reviewRequestVersion, reviewNote.trim(),
        )
        finishTask(selectedTask, `已驳回“${source.name}”的数据源发布申请`)
      } else {
        const { dataset, request } = selectedTask
        await datasetAPI.rejectPublication(dataset.id, request.id, request.version, reviewNote.trim())
        finishTask(selectedTask, `已驳回“${dataset.name}”的数据集发布申请`)
      }
    } catch (cause) {
      if (cause instanceof RequestError &&
          cause.detail.code === 'DATASET_PUBLICATION_REQUEST_CANCELLED') {
        await loadApprovalTasks()
        setReviewNotice('数据集草稿已变更，原审批申请已自动取消；无需再驳回')
      } else {
        setReviewError(cause instanceof Error ? cause.message : '驳回审批失败')
      }
    } finally {
      setBusyAction('')
    }
  }

  const pendingValue = tasksLoading ? '—' : String(totalTasks)
  const hasAnyLoadError = Object.keys(taskErrors).length > 0
  return (
    <AppShell title="工作台" eyebrow="概览" actions={<button className="primary-button">新建报告</button>}>
      <section className="content-stack">
        <div className="welcome-card">
          <div><span className="eyebrow">数据工作空间</span><h2>下午好，报告设计师</h2><p>从数据源到正式归档，所有工作都在统一的租户边界内完成。</p></div>
          <div className="welcome-assistant"><Sparkle aria-hidden="true" size={22} weight="fill" /><span><strong>智能助手已就绪</strong><small>可基于当前租户资产开始分析</small></span></div>
        </div>
        <div className="metric-grid workbench-metric-grid">
          <article className="metric-card"><span>数据源</span><strong>{sourceCount ?? '—'}</strong><small>{sourceCount ? '已接入当前租户' : '当前未配置'}</small></article>
          <article className="metric-card"><span>已发布报告</span><strong>0</strong><small>当前未发布</small></article>
          <article className="metric-card"><span>数据集</span><strong>{datasetCount ?? '—'}</strong><small>{datasetCount ? '当前租户数据集' : '当前未配置'}</small></article>
          <button className="metric-card workbench-task-card" type="button" aria-haspopup="dialog" aria-label={`待处理任务 ${pendingValue}`} onClick={openApprovalQueue}>
            <span>待处理任务</span><strong>{pendingValue}</strong><small>{hasAnyLoadError ? '部分分类加载失败，点击查看' : totalTasks ? '按类型进入审批' : '任务队列为空'}</small>
            <ClipboardText aria-hidden="true" size={22} weight="duotone" />
          </button>
          <button className="metric-card workbench-task-card" type="button" aria-haspopup="dialog" aria-label={`后台任务 ${backgroundPage.activeCount}`} onClick={openBackgroundTasks}>
            <span>后台任务</span><strong>{backgroundPage.activeCount}</strong><small>{backgroundError ? '加载失败，点击重试' : backgroundPage.activeCount ? '查看运行进度与中止任务' : '当前没有运行中的任务'}</small>
            <ClockCounterClockwise aria-hidden="true" size={22} weight="duotone" />
          </button>
        </div>
        <section className="panel"><div className="panel-heading"><div><span className="eyebrow">最近报告</span><h2>继续你的工作</h2></div><button className="quiet-button">查看全部</button></div><div className="report-list"><p>暂无报告，从配置数据源和数据集开始。</p></div></section>
      </section>

      {queueOpen && <div className="workbench-review-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !busyAction) setQueueOpen(false) }}>
        <section className="workbench-review-dialog" role="dialog" aria-modal="true" aria-labelledby="workbench-review-title">
          <header>
            <div><span className="eyebrow">待处理任务</span><h2 id="workbench-review-title">统一审批中心</h2><p>数据源、数据集、指标和维度治理任务按业务类型集中展示。</p></div>
            <button type="button" aria-label="关闭统一审批中心" disabled={Boolean(busyAction)} onClick={() => setQueueOpen(false)}><X size={20} /></button>
          </header>

          <nav className="workbench-review-categories" aria-label="审批任务分类">
            {categoryOrder.map(category => <button
              type="button"
              key={category}
              className={activeCategory === category ? 'active' : ''}
              aria-current={activeCategory === category ? 'page' : undefined}
              onClick={() => chooseCategory(category)}
            >
              {taskIcon(category)}
              <span>{categoryCopy[category].label}</span>
              <strong>{tasksLoading ? '—' : tasks[category].length}</strong>
            </button>)}
          </nav>

          {tasksLoading ? <div className="workbench-review-empty" role="status">正在加载全部审批分类…</div>
            : taskErrors[activeCategory] ? <div className="workbench-review-empty error" role="alert"><strong>{categoryCopy[activeCategory].label}任务加载失败</strong><span>{taskErrors[activeCategory]}</span><button className="quiet-button" type="button" onClick={() => void loadApprovalTasks()}>重新加载全部分类</button></div>
            : activeTasks.length === 0 ? <div className="workbench-review-empty"><CheckCircle size={34} weight="duotone" aria-hidden="true" /><strong>{categoryCopy[activeCategory].empty}</strong><span>{categoryCopy[activeCategory].description}</span>{reviewNotice && <p role="status">{reviewNotice}</p>}</div>
            : <div className="workbench-review-layout">
              <nav aria-label={`待审批${categoryCopy[activeCategory].label}列表`}>
                <div><strong>{activeTasks.length} 项{categoryCopy[activeCategory].label}任务</strong><small>{categoryCopy[activeCategory].description}</small></div>
                {activeTasks.map(task => <button className={selectedTask?.key === task.key ? 'active' : ''} type="button" key={task.key} onClick={() => chooseTask(task)}>
                  {taskIcon(task.category)}
                  <span><strong>{task.name}</strong><small>{task.subtitle}{isOwnSubmission(task) ? ' · 我的申请' : ''}</small></span>
                  <time>{formatSubmittedAt(task.submittedAt)}</time>
                </button>)}
              </nav>
              {selectedTask?.category === 'DATA_SOURCE' && <DataSourceReviewDetail
                task={selectedTask}
                reviewNote={reviewNote}
                reviewError={reviewError}
                reviewNotice={reviewNotice}
                busyAction={busyAction}
                onReviewNote={setReviewNote}
                onApprove={approve}
                onReject={reject}
              />}
              {selectedTask?.category === 'DATASET' && <DatasetReviewDetail
                task={selectedTask}
                reviewNote={reviewNote}
                reviewError={reviewError}
                reviewNotice={reviewNotice}
                busyAction={busyAction}
                onReviewNote={setReviewNote}
                onApprove={approve}
                onReject={reject}
              />}
              {selectedTask?.category === 'METRIC' && <article className="workbench-review-detail">
                <div className="workbench-review-heading"><div><span>指标候选审核</span><h3>{selectedTask.candidate.name}</h3></div><em>{metricCandidateStatusLabel[selectedTask.candidate.status] ?? selectedTask.candidate.status}</em></div>
                <dl>
                  <div><dt>指标编码</dt><dd>{selectedTask.candidate.code}</dd></div>
                  <div><dt>生成方式</dt><dd>{selectedTask.candidate.method}</dd></div>
                  <div><dt>置信度</dt><dd>{Math.round(selectedTask.candidate.confidence * 100)}%</dd></div>
                  <div><dt>来源字段</dt><dd>{selectedTask.candidate.sourceFieldIds.length} 个</dd></div>
                </dl>
                <section className="workbench-review-summary"><strong>业务口径</strong><p>{selectedTask.candidate.semantic?.caliber || selectedTask.candidate.description || '暂无业务口径说明'}</p></section>
                {selectedTask.candidate.warnings.length > 0 && <section className="workbench-review-summary warning"><strong>复核提示</strong><p>{selectedTask.candidate.warnings.join('；')}</p></section>}
                <footer><button className="primary-button" type="button" onClick={() => navigate('/assets/metrics?view=candidates')}>前往指标审批</button></footer>
              </article>}
              {selectedTask?.category === 'DIMENSION' && <article className="workbench-review-detail">
                <div className="workbench-review-heading"><div><span>维度候选治理</span><h3>{selectedTask.candidate.proposedName}</h3></div><em>待治理</em></div>
                <dl>
                  <div><dt>字段编码</dt><dd>{selectedTask.candidate.fieldCode}</dd></div>
                  <div><dt>字段用途</dt><dd>{selectedTask.candidate.fieldRole}</dd></div>
                  <div><dt>维度类型</dt><dd>{selectedTask.candidate.proposedDimensionType}</dd></div>
                  <div><dt>成员索引</dt><dd>{selectedTask.candidate.proposedMemberIndexPolicy}</dd></div>
                </dl>
                <section className="workbench-review-summary"><strong>候选说明</strong><p>{selectedTask.candidate.proposedDescription || '暂无候选说明'}</p></section>
                {(selectedTask.candidate.riskSensitive || selectedTask.candidate.riskHighCardinality) && <section className="workbench-review-summary warning"><strong>风险标记</strong><p>{[selectedTask.candidate.riskSensitive && '敏感字段', selectedTask.candidate.riskHighCardinality && '高基数字段'].filter(Boolean).join('；')}</p></section>}
                <footer><button className="primary-button" type="button" onClick={() => navigate('/assets/semantics')}>前往维度治理</button></footer>
              </article>}
            </div>}
        </section>
      </div>}

      {backgroundOpen && <div className="workbench-review-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !cancellingTaskId && !retryingTaskId) setBackgroundOpen(false) }}>
        <section className="workbench-review-dialog workbench-background-dialog" role="dialog" aria-modal="true" aria-labelledby="workbench-background-title">
          <header>
            <div><span className="eyebrow">后台任务</span><h2 id="workbench-background-title">任务运行中心</h2><p>集中查看当前租户的业务后台任务、真实进度、失败原因并协作式中止。</p></div>
            <button type="button" aria-label="关闭任务运行中心" disabled={Boolean(cancellingTaskId || retryingTaskId)} onClick={() => setBackgroundOpen(false)}><X size={20} /></button>
          </header>

          <div className="workbench-background-toolbar">
            <nav aria-label="后台任务范围">
              <button type="button" className={backgroundView === 'ACTIVE' ? 'active' : ''} aria-current={backgroundView === 'ACTIVE' ? 'page' : undefined} onClick={() => chooseBackgroundView('ACTIVE')}>运行中 <strong>{backgroundPage.activeCount}</strong></button>
              <button type="button" className={backgroundView === 'RECENT' ? 'active' : ''} aria-current={backgroundView === 'RECENT' ? 'page' : undefined} onClick={() => chooseBackgroundView('RECENT')}>最近完成</button>
              <button type="button" className={backgroundView === 'ALL' ? 'active' : ''} aria-current={backgroundView === 'ALL' ? 'page' : undefined} onClick={() => chooseBackgroundView('ALL')}>全部</button>
            </nav>
            <button className="quiet-button" type="button" disabled={backgroundLoading} onClick={() => void loadBackgroundTasks(backgroundView)}>
              <ClockCounterClockwise size={16} />刷新
            </button>
          </div>

          {backgroundFocus && <div className="workbench-background-focus" role="status">
            <span>当前仅显示{backgroundFocusCopy[backgroundFocus]}，已隐藏无关的自动标签、指标提取等任务。</span>
            <button className="quiet-button" type="button" onClick={() => setBackgroundFocus(null)}>显示全部任务</button>
          </div>}
          {backgroundNotice && <div className="workbench-background-notice" role="status">{backgroundNotice}</div>}
          {backgroundError && <div className="workbench-background-error" role="alert">{backgroundError}<button className="quiet-button" type="button" onClick={() => void loadBackgroundTasks(backgroundView)}>重试</button></div>}
          {backgroundLoading && visibleBackgroundTasks.length === 0
            ? <div className="workbench-review-empty" role="status"><SpinnerGap className="spin" size={32} /><strong>正在读取后台任务…</strong></div>
            : visibleBackgroundTasks.length === 0
              ? <div className="workbench-review-empty"><CheckCircle size={34} weight="duotone" aria-hidden="true" /><strong>{backgroundFocus ? `当前范围没有${backgroundFocusCopy[backgroundFocus]}` : backgroundView === 'ACTIVE' ? '当前没有运行中的后台任务' : '当前范围没有任务记录'}</strong><span>任务启动后会自动出现在这里，并每 3 秒刷新一次。</span></div>
              : <div className="workbench-background-list" aria-label="后台任务列表">
                {visibleBackgroundTasks.map(task => <article key={`${task.kind}:${task.id}`} className={`workbench-background-task status-${task.status.toLowerCase()}`}>
                  <header>
                    <div><span>{task.kindLabel}</span><h3>{task.name}</h3><p>{task.description}</p></div>
                    <em>{backgroundStatusCopy[task.status] ?? task.status}</em>
                  </header>
                  <div className="workbench-background-progress">
                    <div><span>{task.progressText}</span><strong>{task.progressPercent === undefined ? '执行中' : `${task.progressPercent}%`}</strong></div>
                    <progress max={100} value={task.progressPercent} aria-label={`${task.name}进度`} />
                  </div>
                  <dl>
                    <div><dt>尝试次数</dt><dd>{task.attempt} / {task.maxAttempts}</dd></div>
                    <div><dt>开始时间</dt><dd>{formatTaskTime(task.startedAt || task.createdAt)}</dd></div>
                    <div><dt>最后更新</dt><dd>{formatTaskTime(task.updatedAt)}</dd></div>
                    <div><dt>任务编号</dt><dd title={task.id}>{task.id.slice(0, 8)}</dd></div>
                  </dl>
                  {(task.errorMessage || task.errorCode) && <div className="workbench-background-diagnostic"><strong>{task.errorCode || 'TASK_ERROR'}</strong><span>{task.errorMessage || '任务未返回详细错误信息'}</span></div>}
                  <footer>
                    <small>{task.canCancel
                      ? '中止会撤销任务租约并阻止结果写回，已完成结果保留。'
                      : task.canRetry
                        ? '重试会保留有效检查点，只重新执行缺失或失败阶段。'
                        : task.retryDisabledReason || task.cancelDisabledReason}</small>
                    <div className="workbench-background-actions">
                    {task.canRetry && <button className="workbench-retry-button" type="button" disabled={Boolean(cancellingTaskId || retryingTaskId)} onClick={() => void retryBackgroundTask(task)}>
                      {retryingTaskId === task.id ? <SpinnerGap className="spin" size={17} /> : <ArrowClockwise size={17} weight="bold" />}
                      {retryingTaskId === task.id ? '正在重试…' : '重试'}
                    </button>}
                    {task.canCancel && <button className="workbench-stop-button" type="button" disabled={Boolean(cancellingTaskId || retryingTaskId)} onClick={() => void cancelBackgroundTask(task)}>
                      {cancellingTaskId === task.id ? <SpinnerGap className="spin" size={17} /> : <StopCircle size={17} weight="bold" />}
                      {cancellingTaskId === task.id ? '正在中止…' : '中止'}
                    </button>}
                    </div>
                  </footer>
                </article>)}
              </div>}
        </section>
      </div>}
    </AppShell>
  )
}

type ReviewDetailProps = {
  reviewNote: string
  reviewError: string
  reviewNotice: string
  busyAction: string
  onReviewNote: (value: string) => void
  onApprove: () => Promise<void>
  onReject: () => Promise<void>
}

function ReviewActions(props: ReviewDetailProps & { approveDisabled?: boolean }) {
  const { reviewNote, reviewError, reviewNotice, busyAction, onReviewNote, onReject, approveDisabled = false } = props
  return <>
    <label>审批意见 <small>审批通过时可选；驳回时必填</small><textarea rows={4} maxLength={1000} value={reviewNote} disabled={Boolean(busyAction)} onChange={event => onReviewNote(event.target.value)} placeholder="填写核验结论或需要申请人修改的原因" /><span>{reviewNote.trim().length} / 1000</span></label>
    {reviewError && <div className="workbench-review-feedback error" role="alert">{reviewError}</div>}
    {reviewNotice && <div className="workbench-review-feedback success" role="status">{reviewNotice}</div>}
    <footer><button className="workbench-reject-button" type="button" disabled={Boolean(busyAction) || !reviewNote.trim()} onClick={() => void onReject()}><XCircle size={18} weight="bold" />{busyAction === 'reject' ? '正在驳回…' : '驳回'}</button><button className="primary-button" type="submit" disabled={Boolean(busyAction) || approveDisabled}><CheckCircle size={18} weight="bold" />{busyAction === 'approve' ? '正在审批…' : '审批通过'}</button></footer>
  </>
}

function DataSourceReviewDetail({ task, ...actions }: ReviewDetailProps & { task: Extract<ApprovalTask, { category: 'DATA_SOURCE' }> }) {
  const source = task.source
  return <form className="workbench-review-detail" onSubmit={event => { event.preventDefault(); void actions.onApprove() }}>
    <div className="workbench-review-heading"><div><span>数据源发布审批</span><h3>{source.name}</h3></div><em>审核中</em></div>
    <dl>
      <div><dt>数据源类型</dt><dd>{sourceTypeLabels[source.type]}</dd></div>
      <div><dt>配置版本</dt><dd>V{source.configVersion ?? source.version}</dd></div>
      <div><dt>提交时间</dt><dd>{formatSubmittedAt(source.reviewSubmittedAt)}</dd></div>
      <div><dt>申请人</dt><dd>{source.reviewRequesterId || '—'}</dd></div>
    </dl>
    {source.type !== 'EXCEL' && <section className="workbench-review-connection" aria-label="待审批连接摘要">
      <div><small>Host</small><strong>{configText(source, 'host')}</strong></div>
      <div><small>Port</small><strong>{configText(source, 'port')}</strong></div>
      <div><small>Database</small><strong>{configText(source, 'database')}</strong></div>
      <div><small>Username</small><strong>{configText(source, 'username')}</strong></div>
    </section>}
    <ReviewActions {...actions} />
  </form>
}

function DatasetReviewDetail({ task, ...actions }: ReviewDetailProps & { task: Extract<ApprovalTask, { category: 'DATASET' }> }) {
  const { dataset, request } = task
  return <form className="workbench-review-detail" onSubmit={event => { event.preventDefault(); void actions.onApprove() }}>
    <div className="workbench-review-heading"><div><span>数据集发布审批</span><h3>{dataset.name}</h3></div><em>待审批</em></div>
    <dl>
      <div><dt>数据集分层</dt><dd>{dataset.type}</dd></div>
      <div><dt>草稿记录版本</dt><dd>V{request.expectedDraftRecordVersion}</dd></div>
      <div><dt>提交时间</dt><dd>{formatSubmittedAt(request.submittedAt)}</dd></div>
      <div><dt>申请人</dt><dd>{request.requesterId || '—'}</dd></div>
    </dl>
    <section className="workbench-review-connection" aria-label="待审批数据集摘要">
      <div><small>DSL 哈希</small><strong>{request.expectedDslHash.slice(0, 12)}…</strong></div>
      <div><small>计划哈希</small><strong>{request.expectedPlanHash.slice(0, 12)}…</strong></div>
      <div><small>后续加工</small><strong>{dataset.layer === 'ODS' ? '指标候选提取' : `${dataset.layer} PostgreSQL 物化`}</strong></div>
      <div><small>启动时机</small><strong>审批通过后</strong></div>
    </section>
    {request.metricCandidateStatus !== 'PENDING' && <div className="workbench-review-feedback" role="status">该申请存在旧流程记录：{candidatePreparationLabel[request.metricCandidateStatus] ?? request.metricCandidateStatus}。它不再作为审批前置条件。</div>}
    <ReviewActions {...actions} />
  </form>
}
