import { CheckCircle, ClipboardText, Database, X, XCircle } from '@phosphor-icons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { AppShell } from '../components/AppShell'
import { currentSubject } from '../lib/auth'
import { dataSourceAPI, type DataSourceRecord } from '../lib/data-sources'

const sourceTypeLabels = { MYSQL: 'MySQL', ORACLE: 'Oracle', EXCEL: 'Excel / CSV' } as const
const formatSubmittedAt = (value?: string) => {
  if (!value) return '提交时间未知'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(date)
}
const configText = (source: DataSourceRecord, key: string) => {
  const value = source.config?.[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value) : '—'
}

/** 展示租户工作台，并集中承载有审批权限用户的待处理数据源发布任务。 */
export function AdminPage() {
  const subject = currentSubject()
  const [sourceCount, setSourceCount] = useState<number | null>(null)
  const [approvalTasks, setApprovalTasks] = useState<DataSourceRecord[]>([])
  const [tasksLoading, setTasksLoading] = useState(true)
  const [tasksError, setTasksError] = useState('')
  const [queueOpen, setQueueOpen] = useState(false)
  const [selectedTaskId, setSelectedTaskId] = useState('')
  const [reviewNote, setReviewNote] = useState('')
  const [reviewError, setReviewError] = useState('')
  const [reviewNotice, setReviewNotice] = useState('')
  const [busyAction, setBusyAction] = useState('')

  const loadApprovalTasks = useCallback(async () => {
    setTasksLoading(true)
    setTasksError('')
    try {
      const page = await dataSourceAPI.list()
      const sources = Array.isArray(page.items) ? page.items : []
      setSourceCount(sources.length)
      const pending = sources.filter(source => source.reviewStatus === 'PENDING'
        && Boolean(source.reviewRequestId)
        && Boolean(source.reviewRequestVersion)
        && (!subject || source.reviewRequesterId !== subject))
      const permitted = await Promise.all(pending.map(async source => {
        try {
          return (await dataSourceAPI.evaluatePermission(source.id, 'PUBLISH')).allowed ? source : null
        } catch {
          return null
        }
      }))
      setApprovalTasks(permitted.filter((source): source is DataSourceRecord => Boolean(source)))
    } catch (cause) {
      setTasksError(cause instanceof Error ? cause.message : '加载待处理任务失败')
      setApprovalTasks([])
    } finally {
      setTasksLoading(false)
    }
  }, [subject])

  useEffect(() => {
    const timeout = window.setTimeout(() => void loadApprovalTasks(), 0)
    return () => window.clearTimeout(timeout)
  }, [loadApprovalTasks])

  const selectedTask = useMemo(
    () => approvalTasks.find(source => source.id === selectedTaskId) || approvalTasks[0],
    [approvalTasks, selectedTaskId],
  )

  const openApprovalQueue = () => {
    setSelectedTaskId(approvalTasks[0]?.id || '')
    setReviewNote('')
    setReviewError('')
    setReviewNotice('')
    setQueueOpen(true)
  }

  const chooseTask = (source: DataSourceRecord) => {
    setSelectedTaskId(source.id)
    setReviewNote('')
    setReviewError('')
    setReviewNotice('')
  }

  const finishTask = (source: DataSourceRecord, message: string) => {
    const remaining = approvalTasks.filter(item => item.id !== source.id)
    setApprovalTasks(remaining)
    setSelectedTaskId(remaining[0]?.id || '')
    setReviewNote('')
    setReviewError('')
    setReviewNotice(message)
  }

  const approve = async () => {
    if (!selectedTask?.reviewRequestId || !selectedTask.reviewRequestVersion) return
    setBusyAction('approve')
    setReviewError('')
    try {
      await dataSourceAPI.approvePublicationRequest(
        selectedTask.id, selectedTask.reviewRequestId, selectedTask.reviewRequestVersion, reviewNote.trim(),
      )
      finishTask(selectedTask, `“${selectedTask.name}”已审批通过，测试版本已自动上线`)
    } catch (cause) {
      setReviewError(cause instanceof Error ? cause.message : '审批通过失败')
    } finally {
      setBusyAction('')
    }
  }

  const reject = async () => {
    if (!selectedTask?.reviewRequestId || !selectedTask.reviewRequestVersion) return
    if (!reviewNote.trim()) {
      setReviewError('驳回时必须填写明确原因，便于申请人修改后重新提交')
      return
    }
    setBusyAction('reject')
    setReviewError('')
    try {
      await dataSourceAPI.rejectPublicationRequest(
        selectedTask.id, selectedTask.reviewRequestId, selectedTask.reviewRequestVersion, reviewNote.trim(),
      )
      finishTask(selectedTask, `已驳回“${selectedTask.name}”的发布申请`)
    } catch (cause) {
      setReviewError(cause instanceof Error ? cause.message : '驳回审批失败')
    } finally {
      setBusyAction('')
    }
  }

  const pendingValue = tasksLoading ? '—' : String(approvalTasks.length)
  return (
    <AppShell title="工作台" eyebrow="概览" actions={<button className="primary-button">新建报告</button>}>
      <section className="content-stack">
        <div className="welcome-card"><div><span className="eyebrow light">数据工作空间</span><h2>下午好，报告设计师</h2><p>从数据源到正式归档，所有工作都在统一的租户边界内完成。</p></div><div className="orbit">AI</div></div>
        <div className="metric-grid">
          <article className="metric-card"><span>数据源</span><strong>{sourceCount ?? '—'}</strong><small>{sourceCount ? '已接入当前租户' : '当前未配置'}</small></article>
          <article className="metric-card"><span>已发布报告</span><strong>0</strong><small>当前未发布</small></article>
          <article className="metric-card"><span>数据集</span><strong>0</strong><small>当前未配置</small></article>
          <button className="metric-card workbench-task-card" type="button" aria-haspopup="dialog" aria-label={`待处理任务 ${pendingValue}`} onClick={openApprovalQueue}>
            <span>待处理任务</span><strong>{pendingValue}</strong><small>{tasksError ? '加载失败，点击查看' : approvalTasks.length ? '点击进入审批' : '任务队列为空'}</small>
            <ClipboardText aria-hidden="true" size={22} weight="duotone" />
          </button>
        </div>
        <section className="panel"><div className="panel-heading"><div><span className="eyebrow">最近报告</span><h2>继续你的工作</h2></div><button className="quiet-button">查看全部</button></div><div className="report-list"><p>暂无报告，从配置数据源和数据集开始。</p></div></section>
      </section>

      {queueOpen && <div className="workbench-review-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !busyAction) setQueueOpen(false) }}>
        <section className="workbench-review-dialog" role="dialog" aria-modal="true" aria-labelledby="workbench-review-title">
          <header>
            <div><span className="eyebrow">待处理任务</span><h2 id="workbench-review-title">数据源发布审批</h2><p>审批通过后，已测试的冻结版本会自动上线，无需再次发布。</p></div>
            <button type="button" aria-label="关闭数据源发布审批" disabled={Boolean(busyAction)} onClick={() => setQueueOpen(false)}><X size={20} /></button>
          </header>

          {tasksError ? <div className="workbench-review-empty error" role="alert"><strong>待处理任务加载失败</strong><span>{tasksError}</span><button className="quiet-button" type="button" onClick={() => void loadApprovalTasks()}>重新加载</button></div>
            : tasksLoading ? <div className="workbench-review-empty" role="status">正在加载待处理任务…</div>
            : approvalTasks.length === 0 ? <div className="workbench-review-empty"><CheckCircle size={34} weight="duotone" aria-hidden="true" /><strong>当前没有待审批的数据源</strong><span>本人提交的申请不会进入自己的审批队列。</span>{reviewNotice && <p role="status">{reviewNotice}</p>}</div>
            : <div className="workbench-review-layout">
              <nav aria-label="待审批数据源列表">
                <div><strong>{approvalTasks.length} 项待处理</strong><small>仅展示本人可审批的任务</small></div>
                {approvalTasks.map(source => <button className={selectedTask?.id === source.id ? 'active' : ''} type="button" key={source.reviewRequestId} onClick={() => chooseTask(source)}>
                  <Database size={20} weight="duotone" aria-hidden="true" />
                  <span><strong>{source.name}</strong><small>{sourceTypeLabels[source.type]} · {source.code}</small></span>
                  <time>{formatSubmittedAt(source.reviewSubmittedAt)}</time>
                </button>)}
              </nav>
              {selectedTask && <form className="workbench-review-detail" onSubmit={event => { event.preventDefault(); void approve() }}>
                <div className="workbench-review-heading"><div><span>发布审批</span><h3>{selectedTask.name}</h3></div><em>审核中</em></div>
                <dl>
                  <div><dt>数据源类型</dt><dd>{sourceTypeLabels[selectedTask.type]}</dd></div>
                  <div><dt>配置版本</dt><dd>V{selectedTask.configVersion ?? selectedTask.version}</dd></div>
                  <div><dt>提交时间</dt><dd>{formatSubmittedAt(selectedTask.reviewSubmittedAt)}</dd></div>
                  <div><dt>申请人</dt><dd>{selectedTask.reviewRequesterId || '—'}</dd></div>
                </dl>
                {selectedTask.type !== 'EXCEL' && <section className="workbench-review-connection" aria-label="待审批连接摘要">
                  <div><small>Host</small><strong>{configText(selectedTask, 'host')}</strong></div>
                  <div><small>Port</small><strong>{configText(selectedTask, 'port')}</strong></div>
                  <div><small>Database</small><strong>{configText(selectedTask, 'database')}</strong></div>
                  <div><small>Username</small><strong>{configText(selectedTask, 'username')}</strong></div>
                </section>}
                <label>审批意见 <small>审批通过时可选；驳回时必填</small><textarea rows={4} maxLength={1000} value={reviewNote} disabled={Boolean(busyAction)} onChange={event => setReviewNote(event.target.value)} placeholder="填写核验结论或需要申请人修改的原因" /><span>{reviewNote.trim().length} / 1000</span></label>
                {reviewError && <div className="workbench-review-feedback error" role="alert">{reviewError}</div>}
                {reviewNotice && <div className="workbench-review-feedback success" role="status">{reviewNotice}</div>}
                <footer><button className="workbench-reject-button" type="button" disabled={Boolean(busyAction) || !reviewNote.trim()} onClick={() => void reject()}><XCircle size={18} weight="bold" />{busyAction === 'reject' ? '正在驳回…' : '驳回'}</button><button className="primary-button" type="submit" disabled={Boolean(busyAction)}><CheckCircle size={18} weight="bold" />{busyAction === 'approve' ? '正在审批…' : '审批通过'}</button></footer>
              </form>}
            </div>}
        </section>
      </div>}
    </AppShell>
  )
}
