import {
  ArrowClockwise,
  ArrowSquareOut,
  CalendarBlank,
  CaretDown,
  CaretRight,
  ChatCircleDots,
  Check,
  ClipboardText,
  Clock,
  Database,
  FileText,
  Key,
  LinkSimple,
  MagnifyingGlass,
  Path,
  ShieldCheck,
  User,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState, type ComponentType } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AppButton } from '../components/AppButton'
import { currentDomain } from '../lib/domain-context'
import { formatHomeTime, workItemDestination, workTypeLabel } from '../lib/home-data'
import { homeAPI, type WorkInboxItem } from '../lib/home-api'
import { canRunInlineTaskAction, runInlineTaskAction, type InlineTaskAction } from '../lib/task-actions'

type Category = 'all' | 'access' | 'request' | 'decision' | 'report'
type DueGroup = 'overdue' | 'today' | 'later' | 'none'
type LoadState = 'loading' | 'ready' | 'error'
type WorkCenterMode = 'approvals' | 'tasks'

const categoryConfig: Record<Category, { label: string; icon: ComponentType<{ size?: number; weight?: 'regular' | 'duotone' }> }> = {
  all: { label: '全部事项', icon: ClipboardText },
  access: { label: '访问与发布', icon: ShieldCheck },
  request: { label: '取数与反馈', icon: FileText },
  decision: { label: '决策与行动', icon: Path },
  report: { label: '报告任务', icon: FileText },
}

const dueGroupLabels: Record<DueGroup, string> = {
  overdue: '已逾期',
  today: '今天到期',
  later: '本周稍后',
  none: '无 SLA',
}

const typeMeta: Record<string, { category: Exclude<Category, 'all'>; source: string; icon: typeof Key; tone: string }> = {
  DOMAIN_ACCESS_APPROVAL: { category: 'access', source: '访问与发布 · 领域访问', icon: Key, tone: 'blue' },
  DATA_SOURCE_PUBLICATION: { category: 'access', source: '访问与发布 · 数据源发布', icon: Database, tone: 'green' },
  DATASET_PUBLICATION: { category: 'access', source: '访问与发布 · 数据集发布', icon: Database, tone: 'green' },
  DATA_REQUEST: { category: 'request', source: '取数与反馈 · 取数申请', icon: FileText, tone: 'purple' },
  FEEDBACK_TICKET: { category: 'request', source: '取数与反馈 · 反馈工单', icon: ChatCircleDots, tone: 'cyan' },
  DECISION_APPROVAL: { category: 'decision', source: '决策与行动 · 决策审批', icon: ShieldCheck, tone: 'amber' },
  ACTION_ASSIGNED: { category: 'decision', source: '决策与行动 · 行动执行', icon: Path, tone: 'cyan' },
  ACTION_BLOCKED: { category: 'decision', source: '决策与行动 · 行动阻塞', icon: Path, tone: 'orange' },
  ACTION_OVERDUE: { category: 'decision', source: '决策与行动 · 行动逾期', icon: Clock, tone: 'orange' },
  DECISION_REVIEW_DUE: { category: 'decision', source: '决策与行动 · 决策复盘', icon: Path, tone: 'amber' },
  OUTCOME_REVIEW_DUE: { category: 'decision', source: '决策与行动 · 结果复盘', icon: Path, tone: 'amber' },
  REPORT_EXPORT_FAILED: { category: 'report', source: '报告任务 · 报告导出', icon: FileText, tone: 'orange' },
  REPORT_DELIVERY_READY: { category: 'report', source: '报告任务 · 报告送达', icon: FileText, tone: 'green' },
  REPORT_DELIVERY_FAILED: { category: 'report', source: '报告任务 · 报告分发', icon: FileText, tone: 'orange' },
  RUNTIME_CONFIG_APPROVAL: { category: 'access', source: '访问与发布 · 运行配置', icon: ShieldCheck, tone: 'amber' },
}

const statusLabels: Record<string, string> = {
  PENDING: '待处理',
  SUBMITTED: '待审批',
  APPROVED: '待开始',
  REJECTED: '已驳回',
  IN_PROGRESS: '处理中',
  UNRESOLVED: '待处理',
  FAILED: '失败',
  READY: '已送达',
  PAUSED: '已暂停',
  NEW: '待分诊',
  TRIAGED: '已分诊',
}

function snapshotItem(input: Partial<WorkInboxItem> & Pick<WorkInboxItem, 'type' | 'objectId' | 'summary'>): WorkInboxItem {
  return {
    status: 'PENDING', overdue: false, domainId: 'snapshot-enterprise-operations', sourceHref: '',
    allowedActions: ['OPEN'], unread: true, updatedAt: '2026-08-10T09:24:00+08:00', version: '1',
    ...input,
  }
}

const snapshotItems: WorkInboxItem[] = [
  snapshotItem({ type: 'DOMAIN_ACCESS_APPROVAL', objectId: '10000000-0000-4000-8000-000000000001', summary: '领域访问申请待审批', requesterUserId: 'liuyang-privacy-id', requesterDisplayName: '刘洋', slaDueAt: '2026-08-09T18:00:00+08:00', overdue: true, allowedActions: ['APPROVE', 'REJECT'] }),
  snapshotItem({ type: 'DATA_SOURCE_PUBLICATION', objectId: '10000000-0000-4000-8000-000000000002', summary: '数据源发布申请待审批', requesterUserId: 'chenchen-privacy-id', requesterDisplayName: '陈晨', slaDueAt: '2026-08-08T18:00:00+08:00', overdue: true, updatedAt: '2026-08-09T08:50:00+08:00', allowedActions: ['APPROVE', 'REJECT'] }),
  snapshotItem({ type: 'DATASET_PUBLICATION', objectId: '10000000-0000-4000-8000-000000000003', summary: '数据集发布申请待审批', requesterUserId: 'liming-privacy-id', requesterDisplayName: '李明', slaDueAt: '2026-08-10T18:00:00+08:00', updatedAt: '2026-08-10T10:18:00+08:00', allowedActions: ['APPROVE', 'REJECT'] }),
  snapshotItem({ type: 'DATA_REQUEST', objectId: '10000000-0000-4000-8000-000000000004', summary: '取数申请待处理', requesterUserId: 'zhanglei-privacy-id', requesterDisplayName: '张磊', slaDueAt: '2026-08-10T19:00:00+08:00', status: 'SUBMITTED', updatedAt: '2026-08-10T09:48:00+08:00', allowedActions: ['APPROVE', 'REJECT'] }),
  snapshotItem({ type: 'FEEDBACK_TICKET', objectId: '10000000-0000-4000-8000-000000000005', summary: '反馈工单待处理', requesterUserId: 'zhaolei-privacy-id', slaDueAt: '2026-08-10T21:00:00+08:00', updatedAt: '2026-08-10T08:52:00+08:00' }),
  snapshotItem({ type: 'DECISION_APPROVAL', objectId: '10000000-0000-4000-8000-000000000006', summary: '决策待审批', requesterUserId: 'wangmin-privacy-id', slaDueAt: '2026-08-11T18:00:00+08:00', status: 'UNRESOLVED', updatedAt: '2026-08-10T09:05:00+08:00', allowedActions: ['APPROVE', 'REJECT'] }),
  snapshotItem({ type: 'ACTION_ASSIGNED', objectId: '10000000-0000-4000-8000-000000000007', summary: '行动待开始', requesterUserId: 'zhouming-privacy-id', slaDueAt: '2026-08-12T18:00:00+08:00', status: 'APPROVED', updatedAt: '2026-08-10T09:22:00+08:00', allowedActions: ['START', 'BLOCK', 'COMPLETE'] }),
  snapshotItem({ type: 'DOMAIN_ACCESS_APPROVAL', objectId: '10000000-0000-4000-8000-000000000008', summary: '领域访问申请待审批', requesterUserId: 'sunqiang-privacy-id', requesterDisplayName: '孙强', slaDueAt: '2026-08-13T18:00:00+08:00', updatedAt: '2026-08-09T16:37:00+08:00', allowedActions: ['APPROVE', 'REJECT'] }),
  snapshotItem({ type: 'DATA_SOURCE_PUBLICATION', objectId: '10000000-0000-4000-8000-000000000009', summary: '数据源发布申请待审批', requesterUserId: 'chenchen-privacy-id', requesterDisplayName: '陈晨', slaDueAt: '2026-08-14T18:00:00+08:00', updatedAt: '2026-08-09T14:42:00+08:00', allowedActions: ['APPROVE', 'REJECT'] }),
  snapshotItem({ type: 'REPORT_EXPORT_FAILED', objectId: '10000000-0000-4000-8000-000000000010', summary: '报告导出任务失败', requesterUserId: 'sunqiang-privacy-id', status: 'FAILED', updatedAt: '2026-08-09T10:08:00+08:00', allowedActions: ['RETRY'] }),
  snapshotItem({ type: 'DATA_REQUEST', objectId: '10000000-0000-4000-8000-000000000011', summary: '渠道明细取数申请待开始', requesterUserId: 'zhanglei-privacy-id', requesterDisplayName: '张磊', slaDueAt: '2026-08-12T17:00:00+08:00', status: 'APPROVED', updatedAt: '2026-08-10T10:26:00+08:00', allowedActions: ['START'] }),
]

function itemCategory(item: WorkInboxItem) {
  return typeMeta[item.type]?.category ?? 'request'
}

function isApprovalItem(item: WorkInboxItem) {
  return item.allowedActions.includes('APPROVE') || item.allowedActions.includes('REJECT')
}

function dueGroup(item: WorkInboxItem, now: Date): DueGroup {
  if (!item.slaDueAt) return 'none'
  const due = new Date(item.slaDueAt)
  if (item.overdue || due.getTime() < now.getTime()) return 'overdue'
  if (due.getFullYear() === now.getFullYear() && due.getMonth() === now.getMonth() && due.getDate() === now.getDate()) return 'today'
  return 'later'
}

function dueLabel(item: WorkInboxItem, now: Date) {
  if (!item.slaDueAt) return '无 SLA'
  const due = new Date(item.slaDueAt)
  const difference = due.getTime() - now.getTime()
  if (item.overdue || difference < 0) {
    const days = Math.max(1, Math.ceil(Math.abs(difference) / 86_400_000))
    return `逾期 ${days} 天`
  }
  if (difference < 86_400_000) return `剩余 ${Math.max(1, Math.ceil(difference / 3_600_000))} 小时`
  return `剩余 ${Math.max(1, Math.ceil(difference / 86_400_000))} 天`
}

function exactDue(item: WorkInboxItem) {
  if (!item.slaDueAt) return '未设置 SLA 截止时间'
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(item.slaDueAt))
}

function requesterLabel(value?: string, displayName?: string) {
  if (displayName?.trim()) return displayName
  if (!value) return '系统'
  return `${value.slice(0, 2)}** · ${value.slice(-6)}`
}

function loadError(error: unknown) {
  return error instanceof Error ? error.message : '任务列表加载失败，请稍后重试'
}

/** TASK-001：统一工作箱列表与详情，业务处理仍回到来源模块完成。 */
export function TasksPage() {
  return <WorkCenterPage mode="tasks" />
}

export function ApprovalsPage() {
  return <WorkCenterPage mode="approvals" />
}

function WorkCenterPage({ mode }: { mode: WorkCenterMode }) {
  const navigate = useNavigate()
  const pageParams = new URLSearchParams(window.location.search)
  const snapshot = import.meta.env.DEV && pageParams.get('snapshot') === mode
  const incomingItemID = /^[0-9a-f-]{36}$/i.test(pageParams.get('itemId') ?? '') ? pageParams.get('itemId') ?? '' : ''
  const now = useMemo(() => snapshot ? new Date('2026-08-10T12:00:00+08:00') : new Date(), [snapshot])
  const domainName = snapshot ? '企业经营' : currentDomain()?.name ?? '当前领域'
  const [items, setItems] = useState<WorkInboxItem[]>(snapshot ? snapshotItems : [])
  const [state, setState] = useState<LoadState>(snapshot ? 'ready' : 'loading')
  const [error, setError] = useState('')
  const [category, setCategory] = useState<Category>('all')
  const [query, setQuery] = useState('')
  const [onlyUnread, setOnlyUnread] = useState(!incomingItemID)
  const [selectedID, setSelectedID] = useState(incomingItemID || (mode === 'tasks' ? snapshotItems[10].objectId : snapshotItems[0].objectId))
  const [reloadRevision, setReloadRevision] = useState(0)
  const [notice, setNotice] = useState('')
  const [nextPath, setNextPath] = useState('')
  const [actionMode, setActionMode] = useState<InlineTaskAction | null>(null)
  const [actionNote, setActionNote] = useState('')
  const [busyAction, setBusyAction] = useState(false)

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    void homeAPI.listWorkItems({ unread: onlyUnread, limit: 200 })
      .then(result => {
        if (cancelled) return
        setItems(result.items)
        setState('ready')
        setError('')
      })
      .catch(cause => {
        if (cancelled) return
        setItems([])
        setState('error')
        setError(loadError(cause))
      })
    return () => { cancelled = true }
  }, [onlyUnread, reloadRevision, snapshot])

  const centerItems = useMemo(() => items
    .filter(item => !onlyUnread || item.unread)
    .filter(item => mode === 'approvals' ? isApprovalItem(item) : !isApprovalItem(item)), [items, mode, onlyUnread])
  const categoryKeys: Category[] = mode === 'approvals' ? ['all', 'access', 'request', 'decision'] : ['all', 'request', 'decision', 'report']
  const counts = useMemo(() => ({
    all: centerItems.length,
    access: centerItems.filter(item => itemCategory(item) === 'access').length,
    request: centerItems.filter(item => itemCategory(item) === 'request').length,
    decision: centerItems.filter(item => itemCategory(item) === 'decision').length,
    report: centerItems.filter(item => itemCategory(item) === 'report').length,
  }), [centerItems])

  const visibleItems = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase('zh-CN')
    return centerItems.filter(item => {
      if (category !== 'all' && itemCategory(item) !== category) return false
      if (!normalized) return true
      return [item.summary, workTypeLabel(item.type), item.requesterUserId ?? '', item.status]
        .some(value => value.toLocaleLowerCase('zh-CN').includes(normalized))
    })
  }, [category, centerItems, query])

  const groups = useMemo(() => (['overdue', 'today', 'later', 'none'] as DueGroup[])
    .map(key => ({ key, items: visibleItems.filter(item => dueGroup(item, now) === key) }))
    .filter(group => group.items.length > 0), [now, visibleItems])

  const selected = visibleItems.find(item => item.objectId === selectedID) ?? visibleItems[0] ?? centerItems[0]
  const inlineActions = selected?.allowedActions.filter(action => canRunInlineTaskAction(selected, action)) ?? []

  const markRead = async (item: WorkInboxItem) => {
    if (snapshot) {
      setItems(current => current.map(value => value.objectId === item.objectId ? { ...value, unread: false } : value))
      setNotice('已标记为已读')
      return
    }
    try {
      await homeAPI.markWorkItemRead(item)
      setItems(current => onlyUnread ? current.filter(value => value.objectId !== item.objectId) : current.map(value => value.objectId === item.objectId ? { ...value, unread: false } : value))
      setNotice('已标记为已读')
    } catch (cause) {
      setNotice(loadError(cause))
    }
  }

  const openSource = async (item: WorkInboxItem) => {
    if (item.unread) await markRead(item)
    const destination = snapshot ? undefined : workItemDestination(item)
    if (destination) {
      navigate(destination)
      return
    }
    setNotice(snapshot ? '视觉快照保持在当前任务中心。' : '该事项没有独立来源页面，可直接使用当前任务中心提供的操作处理。')
  }

  const executeAction = async (item: WorkInboxItem, action: InlineTaskAction) => {
		if (['REJECT', 'BLOCK', 'COMPLETE'].includes(action) && !actionNote.trim()) return
    setBusyAction(true)
    try {
      if (snapshot) {
				const status = action === 'APPROVE' ? 'APPROVED' : action === 'REJECT' ? 'REJECTED' : action === 'COMPLETE' ? 'DONE' : action === 'BLOCK' ? 'BLOCKED' : 'IN_PROGRESS'
        setItems(current => current.map(value => value.objectId === item.objectId
          ? { ...value, status, unread: false, allowedActions: [] }
          : value))
      } else {
        await runInlineTaskAction(item, action, actionNote.trim())
        setItems(current => current.filter(value => value.objectId !== item.objectId))
        setReloadRevision(value => value + 1)
      }
      const accessApproved = action === 'APPROVE' && item.type === 'DOMAIN_ACCESS_APPROVAL'
      setNextPath(accessApproved
        ? `/platform-management/users?userId=${encodeURIComponent(item.requesterUserId ?? '')}&from=approval${snapshot ? '&snapshot=user-permissions' : ''}`
        : '')
      setNotice(accessApproved
        ? '领域申请已批准，可继续确认成员角色与生效权限'
				: action === 'APPROVE' ? '事项已批准，来源状态已同步更新'
					: action === 'REJECT' ? '事项已驳回，意见已提交给申请人'
						: action === 'BLOCK' ? '行动已标记为阻塞，原因已同步给决策负责人'
							: action === 'COMPLETE' ? '行动已完成，交付凭证已写入决策记录'
								: '事项已开始处理')
      setActionMode(null)
      setActionNote('')
    } catch (cause) {
      setNotice(loadError(cause))
      if (!snapshot) setReloadRevision(value => value + 1)
    } finally {
      setBusyAction(false)
    }
  }

  const reload = () => {
    if (snapshot) return
    setState('loading')
    setReloadRevision(value => value + 1)
  }

  const pageTitle = mode === 'approvals' ? '审批中心' : '任务中心'

  return <AppShell className="tasks-shell" title={pageTitle} eyebrow="" titleMeta={<span className="tasks-title-domain">领域：{domainName}<CaretDown size={13} /></span>}>
    <section className="tasks-workspace" aria-label={`${pageTitle}工作区`}>
      <aside className="tasks-category-rail" aria-label="事项分类">
        <h2>{mode === 'approvals' ? '审批分类' : '任务分类'}</h2>
        {categoryKeys.map(key => {
          const item = categoryConfig[key]
          const Icon = item.icon
          return <button key={key} type="button" aria-pressed={category === key} onClick={() => setCategory(key)}>
            <Icon size={18} weight="duotone" /><span>{item.label}</span><strong>{counts[key]}</strong>
          </button>
        })}
      </aside>

      <div className="tasks-queue">
        <header className="tasks-queue-toolbar">
          <label className="tasks-search"><MagnifyingGlass size={17} /><span className="sr-only">搜索任务</span><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索任务标题、来源或申请人" /></label>
          <label className="tasks-unread-toggle"><span>仅看未读</span><button type="button" role="switch" aria-checked={onlyUnread} onClick={() => { setOnlyUnread(value => !value); if (!snapshot) setState('loading') }}><span /></button></label>
          <span className="tasks-sort" title="任务已按 SLA 紧急程度自动分组">按 SLA 排序</span>
          <button className="tasks-refresh" type="button" aria-label="刷新任务列表" onClick={reload}><ArrowClockwise size={17} /></button>
        </header>

        <div className="tasks-queue-body" aria-live="polite" aria-busy={state === 'loading'}>
          {state === 'loading' && <div className="tasks-state"><span className="home-loading-dot" /><strong>正在加载当前领域的任务</strong><small>不同来源会独立校验权限</small></div>}
          {state === 'error' && <div className="tasks-state is-error"><WarningCircle size={28} /><strong>任务列表暂时无法加载</strong><small>{error}</small><button type="button" onClick={reload}><ArrowClockwise size={14} />重新加载</button></div>}
          {state === 'ready' && groups.map(group => <section className={`tasks-due-group is-${group.key}`} key={group.key} aria-labelledby={`tasks-group-${group.key}`}>
            <header><h2 id={`tasks-group-${group.key}`}>{dueGroupLabels[group.key]}</h2><span>（{group.items.length}）</span></header>
            {group.items.map(item => {
              const meta = typeMeta[item.type] ?? { category: 'request', source: workTypeLabel(item.type), icon: ClipboardText, tone: 'blue' }
              const Icon = meta.icon
              const active = selected?.objectId === item.objectId
              return <button className={`tasks-row ${active ? 'is-selected' : ''}`} type="button" key={`${item.type}:${item.objectId}`} onClick={() => { setSelectedID(item.objectId); setActionMode(null); setActionNote('') }}>
                <span className={`tasks-unread-dot ${item.unread ? 'is-unread' : ''}`} aria-label={item.unread ? '未读' : '已读'} />
                <span className={`tasks-source-icon is-${meta.tone}`}><Icon size={20} weight="duotone" /></span>
                <span className="tasks-row-copy"><strong>{item.summary}</strong><small>{meta.source}</small></span>
                <span className={`tasks-row-due is-${group.key}`}><strong>{dueLabel(item, now)}</strong><small>{item.slaDueAt ? `应 ${new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(item.slaDueAt))}` : '无截止时间'}</small></span>
                <CaretRight className="tasks-row-caret" size={16} />
              </button>
            })}
          </section>)}
          {state === 'ready' && visibleItems.length === 0 && <div className="tasks-state"><Check size={28} /><strong>{query ? `没有匹配的${mode === 'approvals' ? '审批' : '任务'}` : `当前分类没有${mode === 'approvals' ? '待审批事项' : '任务'}`}</strong><small>{query ? '可调整关键词或切换分类继续查找' : '新的事项进入当前领域后会自动显示在这里'}</small></div>}
        </div>
      </div>

      <aside className="tasks-detail" aria-label="事项详情">
        <header><h2>事项详情</h2>{selected && <span>{statusLabels[selected.status] ?? selected.status}</span>}</header>
        {selected ? <>
          <div className="tasks-detail-title"><strong>{selected.summary}</strong><small>{typeMeta[selected.type]?.source ?? workTypeLabel(selected.type)}</small></div>
          <dl>
            <div><dt><User size={17} />申请人</dt><dd>{requesterLabel(selected.requesterUserId, selected.requesterDisplayName)}</dd></div>
            <div><dt><Clock size={17} />SLA</dt><dd className={dueGroup(selected, now) === 'overdue' ? 'is-overdue' : ''}><strong>{dueLabel(selected, now)}</strong><span>{exactDue(selected)}</span></dd></div>
            <div><dt><CalendarBlank size={17} />更新时间</dt><dd>{formatHomeTime(selected.updatedAt, now)}</dd></div>
            <div><dt><Database size={17} />来源模块</dt><dd>{typeMeta[selected.type]?.source ?? workTypeLabel(selected.type)}</dd></div>
            <div><dt><LinkSimple size={17} />来源页面</dt><dd className="tasks-source-href">{selected.sourceHref || '由来源模块生成'}</dd></div>
          </dl>
          <div className="tasks-detail-description"><strong>说明</strong><p>{selected.summary}。该工作箱只展示受权摘要，具体业务信息与处理状态以来源模块为准。</p></div>
          {actionMode && <form className={`tasks-action-confirm is-${actionMode.toLocaleLowerCase()}`} onSubmit={event => { event.preventDefault(); void executeAction(selected, actionMode) }}>
			<strong>{actionMode === 'APPROVE' ? '确认批准此事项？' : actionMode === 'REJECT' ? '填写驳回原因' : actionMode === 'BLOCK' ? '说明阻塞原因' : actionMode === 'COMPLETE' ? '填写完成凭证' : '确认开始处理？'}</strong>
			<p>{actionMode === 'REJECT' ? '驳回原因将记录在来源业务的审计事件中。' : actionMode === 'BLOCK' ? '阻塞原因会同步给决策负责人。' : actionMode === 'COMPLETE' ? '请填写交付结果、文档地址或其他可核验凭证。' : '提交后由来源模块重新校验权限、版本和业务门禁。'}</p>
			{['REJECT', 'BLOCK', 'COMPLETE'].includes(actionMode) && <textarea autoFocus value={actionNote} onChange={event => setActionNote(event.target.value)} placeholder={actionMode === 'COMPLETE' ? '例如：预算方案已更新并完成投放配置，凭证编号 OP-2026-0812' : actionMode === 'BLOCK' ? '请说明阻塞原因和需要的协助' : '请说明需要补充或调整的内容'} />}
			<div><button type="button" disabled={busyAction} onClick={() => { setActionMode(null); setActionNote('') }}>取消</button><button className="primary-button" type="submit" disabled={busyAction || (['REJECT', 'BLOCK', 'COMPLETE'].includes(actionMode) && !actionNote.trim())}>{busyAction ? '正在提交…' : actionMode === 'APPROVE' ? '确认批准' : actionMode === 'REJECT' ? '确认驳回' : actionMode === 'BLOCK' ? '确认阻塞' : actionMode === 'COMPLETE' ? '确认完成' : '确认开始'}</button></div>
          </form>}
          <div className="tasks-detail-actions">
            {inlineActions.includes('REJECT') && <button className="danger-button" type="button" disabled={busyAction} onClick={() => setActionMode('REJECT')}>驳回</button>}
			{inlineActions.includes('BLOCK') && <button className="danger-button" type="button" disabled={busyAction} onClick={() => setActionMode('BLOCK')}>标记阻塞</button>}
            {inlineActions.includes('START') && <button className="primary-button" type="button" disabled={busyAction} onClick={() => setActionMode('START')}>开始处理</button>}
			{inlineActions.includes('COMPLETE') && <button className="primary-button" type="button" disabled={busyAction} onClick={() => setActionMode('COMPLETE')}>完成行动</button>}
            {inlineActions.includes('APPROVE') && <button className="primary-button" type="button" disabled={busyAction} onClick={() => setActionMode('APPROVE')}>批准</button>}
            <button type="button" disabled={busyAction} onClick={() => void openSource(selected)}>{inlineActions.length ? '查看完整详情' : '打开源页面处理'}<ArrowSquareOut size={17} /></button>
            <button type="button" disabled={busyAction || !selected.unread} onClick={() => void markRead(selected)}>{selected.unread ? '标记已读' : '已标记为已读'}</button>
          </div>
          <p className="tasks-authority-note"><ShieldCheck size={16} />具体处理以来源模块为准</p>
        </> : <div className="tasks-state"><ClipboardText size={28} /><strong>选择一项任务</strong><small>查看来源、SLA 和可执行动作</small></div>}
      </aside>
    </section>
    {notice && <div className="home-notice" role="status"><ClipboardText size={17} /><span>{notice}</span>{nextPath && <AppButton link className="home-notice-next" type="button" onClick={() => navigate(nextPath)}>配置成员权限<CaretRight size={14} /></AppButton>}<AppButton text circle type="button" aria-label="关闭提示" onClick={() => { setNotice(''); setNextPath('') }}><X size={15} /></AppButton></div>}
  </AppShell>
}
