import {
  ArrowClockwise,
  ArrowRight,
  ChartLineUp,
  ChatCircleDots,
  CheckCircle,
  FileText,
  MagnifyingGlass,
  PaperPlaneTilt,
  ShieldCheck,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import { currentDomain, subscribeDomainChange } from '../lib/domain-context'
import {
  conversationToHomeWork,
  decisionToHomeWork,
  reportToHomeWork,
  workItemToHomeTask,
  type HomeTaskItem,
  type HomeWorkItem,
  type HomeWorkKind,
} from '../lib/home-data'
import { homeAPI, type WorkInboxItem } from '../lib/home-api'
import { reportAssetsAPI } from '../report/api/assets'

type LoadState<T> = {
  status: 'loading' | 'ready' | 'error'
  items: T[]
  error?: string
}

type HomeWorkFilter = 'all' | HomeWorkKind

const snapshotSuggestions = [
  '各渠道毛利率变化趋势如何？',
  '新品上市效果如何？',
  '库存健康度异常的产品有哪些？',
]

const snapshotWorkItems: Record<HomeWorkKind, HomeWorkItem[]> = {
  question: [
    { id: 'q-1', title: '本月毛利率下降的主要原因是什么？', meta: '问数 · 企业经营', viewedAt: '今天 10:24', range: '本月 vs 上月', kind: 'question' },
    { id: 'q-2', title: '各渠道毛利率变化趋势如何？', meta: '问数 · 销售分析', viewedAt: '今天 09:41', range: '近 12 个月', kind: 'question' },
    { id: 'q-3', title: '新品上市效果如何？', meta: '问数 · 产品分析', viewedAt: '昨天 16:37', range: '上市后 30 天', kind: 'question' },
    { id: 'q-4', title: '区域销售达成情况如何？', meta: '问数 · 区域分析', viewedAt: '08-08 15:22', range: '本年累计', kind: 'question' },
    { id: 'q-5', title: '库存健康度异常的产品有哪些？', meta: '问数 · 库存分析', viewedAt: '08-08 10:08', range: '全公司', kind: 'question' },
  ],
  report: [
    { id: 'r-1', title: '供应链月度经营报告', meta: '报告 · v7 已发布', viewedAt: '昨天 16:45', range: '2026 年 7 月', kind: 'report' },
    { id: 'r-2', title: '质量成本分析', meta: '报告 · v9 已发布', viewedAt: '昨天 15:20', range: '本季度', kind: 'report' },
    { id: 'r-3', title: '渠道健康度看板', meta: '报告 · v5 已发布', viewedAt: '08-09 14:22', range: '近 12 个月', kind: 'report' },
  ],
  decision: [
    { id: 'd-1', title: '渠道政策调整专项方案', meta: '决策 · 待审批', viewedAt: '08-09 14:18', range: '企业经营', kind: 'decision' },
    { id: 'd-2', title: '区域销售目标纠偏行动', meta: '决策 · 执行中', viewedAt: '昨天 17:10', range: '华东区域', kind: 'decision' },
    { id: 'd-3', title: '库存周转改善计划', meta: '决策 · 待复盘', viewedAt: '08-08 11:20', range: '冰箱产业', kind: 'decision' },
  ],
}

function snapshotSource(type: string, objectId: string, sourceHref = ''): WorkInboxItem {
  return { type, objectId, status: 'PENDING', overdue: false, domainId: 'snapshot-domain', summary: '', sourceHref, allowedActions: ['OPEN'], unread: true, updatedAt: '2026-08-10T00:00:00+08:00', version: '1' }
}

const snapshotTasks: HomeTaskItem[] = [
  { id: 't-1', source: snapshotSource('DATA_REQUEST', 't-1'), title: '毛利率异常波动原因分析', summary: '请在本周内确认 7 月毛利率下降的主要原因', due: '截止：明天 08-12', owner: '张晨', priority: 'high', href: '/ask-data?q=毛利率异常波动原因分析&snapshot=home-question' },
  { id: 't-2', source: snapshotSource('REPORT_EXPORT_FAILED', 't-2'), title: '新品上市复盘报告发布', summary: '新品上市后 30 天复盘报告待发布', due: '截止：08-13（周四）', owner: '刘洋', priority: 'high', href: '/reports?snapshot=assets' },
  { id: 't-3', source: snapshotSource('DECISION_APPROVAL', 't-3'), title: '渠道政策效果评估', summary: '评估 7 月渠道政策对销量的影响', due: '截止：08-14（周五）', owner: '李娜', priority: 'medium' },
  { id: 't-4', source: snapshotSource('ACTION_ASSIGNED', 't-4'), title: '区域销售目标完成跟进', summary: '部分区域达成率未达预期，请跟进原因', due: '截止：08-16（周日）', owner: '张磊', priority: 'medium' },
  { id: 't-5', source: snapshotSource('DATA_SOURCE_PUBLICATION', 't-5'), title: '数据质量问题处理', summary: '库存数据存在缺失，请确认修复方案', due: '截止：08-18（周二）', owner: '周明', priority: 'low', href: '/data-sources?snapshot=home-data' },
  { id: 't-6', source: snapshotSource('REPORT_EXPORT_FAILED', 't-6'), title: '经营报告口径变更确认', summary: '报告引用的销售额口径已更新，请确认影响', due: '截止：08-19（周三）', owner: '王敏', priority: 'high', href: '/reports?snapshot=assets' },
]

const workFilterLabels: Record<HomeWorkFilter, string> = {
  all: '全部',
  question: '分析',
  report: '报告',
  decision: '决策',
}

function WorkIcon({ kind }: { kind: HomeWorkKind }) {
  if (kind === 'report') return <FileText size={17} aria-hidden="true" />
  if (kind === 'decision') return <CheckCircle size={17} aria-hidden="true" />
  return <ChatCircleDots size={17} aria-hidden="true" />
}

function resourceError(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback
}

function formatPageDate(value: Date) {
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'long', day: 'numeric' }).format(value)
}

function combinedRecentWork(work: Record<HomeWorkKind, LoadState<HomeWorkItem>>) {
  return [
    work.question.items[1],
    work.report.items[0],
    work.question.items[2],
    work.decision.items[0],
    work.question.items[4] ?? work.question.items[3],
  ].filter((item): item is HomeWorkItem => Boolean(item)).slice(0, 5)
}

function statusForWork(item: HomeWorkItem) {
  if (item.kind === 'report') return { label: item.meta.includes('已发布') ? '已发布' : '进行中', className: 'is-published' }
  if (item.kind === 'decision') return { label: item.meta.includes('审批') ? '进行中' : '已完成', className: item.meta.includes('审批') ? 'is-progress' : 'is-complete' }
  return { label: '已完成', className: 'is-complete' }
}

/** P01：分析首页。生产路径只渲染当前用户与领域的受权接口数据；快照仅供开发视觉回归。 */
export function HomePage() {
  const navigate = useNavigate()
  const snapshot = import.meta.env.DEV && new URLSearchParams(window.location.search).get('snapshot') === 'home'
  const initialWorkState = (kind: HomeWorkKind): LoadState<HomeWorkItem> => snapshot
    ? { status: 'ready', items: snapshotWorkItems[kind] }
    : { status: 'loading', items: [] }
  const [question, setQuestion] = useState('')
  const [questionError, setQuestionError] = useState('')
  const [activeFilter, setActiveFilter] = useState<HomeWorkFilter>('all')
  const [notice, setNotice] = useState('')
  const [reloadRevision, setReloadRevision] = useState(0)
  const [pageDomainName, setPageDomainName] = useState(() => snapshot ? '企业经营' : currentDomain()?.name ?? '当前领域')
  const [suggestions, setSuggestions] = useState(snapshot ? snapshotSuggestions : [])
  const [work, setWork] = useState<Record<HomeWorkKind, LoadState<HomeWorkItem>>>(() => ({
    question: initialWorkState('question'),
    report: initialWorkState('report'),
    decision: initialWorkState('decision'),
  }))
  const [taskState, setTaskState] = useState<LoadState<HomeTaskItem>>(snapshot
    ? { status: 'ready', items: snapshotTasks }
    : { status: 'loading', items: [] })

  useEffect(() => subscribeDomainChange(() => {
    setPageDomainName(currentDomain()?.name ?? '当前领域')
    if (snapshot) return
    setWork({ question: { status: 'loading', items: [] }, report: { status: 'loading', items: [] }, decision: { status: 'loading', items: [] } })
    setTaskState({ status: 'loading', items: [] })
    setReloadRevision(value => value + 1)
  }), [snapshot])

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    const domainName = currentDomain()?.name ?? '当前领域'

    void homeAPI.listConversations(5)
      .then(result => { if (!cancelled) setWork(value => ({ ...value, question: { status: 'ready', items: result.items.map(item => conversationToHomeWork(item, domainName)) } })) })
      .catch(error => { if (!cancelled) setWork(value => ({ ...value, question: { status: 'error', items: [], error: resourceError(error, '最近分析加载失败') } })) })

    void reportAssetsAPI.list({ limit: 5 })
      .then(result => { if (!cancelled) setWork(value => ({ ...value, report: { status: 'ready', items: result.items.map(reportToHomeWork) } })) })
      .catch(error => { if (!cancelled) setWork(value => ({ ...value, report: { status: 'error', items: [], error: resourceError(error, '报告资产加载失败') } })) })

    void homeAPI.listDecisions(5)
      .then(result => { if (!cancelled) setWork(value => ({ ...value, decision: { status: 'ready', items: result.items.map(decisionToHomeWork) } })) })
      .catch(error => { if (!cancelled) setWork(value => ({ ...value, decision: { status: 'error', items: [], error: resourceError(error, '决策列表加载失败') } })) })

    void homeAPI.listWorkItems({ unread: false, limit: 200 })
      .then(result => { if (!cancelled) setTaskState({ status: 'ready', items: result.items.map(item => workItemToHomeTask(item)) }) })
      .catch(error => { if (!cancelled) setTaskState({ status: 'error', items: [], error: resourceError(error, '待办加载失败') }) })

    void homeAPI.listSavedQuestions()
      .then(result => {
        if (!cancelled) setSuggestions(result.items.filter(item => item.status === 'ACTIVE').slice(0, 3).map(item => item.questionText))
      })
      .catch(() => { if (!cancelled) setSuggestions([]) })

    return () => { cancelled = true }
  }, [reloadRevision, snapshot])

  const visibleTasks = useMemo(() => taskState.items.slice(0, 4), [taskState.items])
  const visibleWork = useMemo(
    () => activeFilter === 'all' ? combinedRecentWork(work) : work[activeFilter].items.slice(0, 5),
    [activeFilter, work],
  )
  const recentState = activeFilter === 'all'
    ? visibleWork.length > 0
      ? { status: 'ready' as const, error: undefined }
      : Object.values(work).some(item => item.status === 'loading')
        ? { status: 'loading' as const, error: undefined }
        : Object.values(work).every(item => item.status === 'error')
          ? { status: 'error' as const, error: '最近工作暂时无法加载' }
          : { status: 'ready' as const, error: undefined }
    : work[activeFilter]
  const continueItem = work.question.items[0]
  const pageDate = snapshot ? '2026 年 8 月 11 日' : formatPageDate(new Date())

  const reloadHome = () => {
    if (snapshot) return
    setWork({ question: { status: 'loading', items: [] }, report: { status: 'loading', items: [] }, decision: { status: 'loading', items: [] } })
    setTaskState({ status: 'loading', items: [] })
    setReloadRevision(value => value + 1)
  }

  const startQuestion = (event?: FormEvent) => {
    event?.preventDefault()
    const value = question.trim()
    if (!value) {
      setQuestionError('请输入需要分析的问题')
      return
    }
    setQuestionError('')
    navigate(`/ask-data?q=${encodeURIComponent(value)}${snapshot ? '&snapshot=home-question' : ''}`)
  }

  const chooseSuggestion = (value: string) => {
    setQuestion(value)
    setQuestionError('')
  }

  const openWork = (item: HomeWorkItem) => {
    if (item.href) {
      navigate(item.href)
      return
    }
    if (item.kind === 'question') {
      navigate(`/ask-data?q=${encodeURIComponent(item.title)}${snapshot ? '&snapshot=home-question' : ''}`)
      return
    }
    if (item.kind === 'report') {
      navigate(snapshot ? '/reports?snapshot=assets' : '/reports')
      return
    }
    setNotice('决策数据已从后端加载；决策详情页需在下一页面组确认后接入。')
  }

  const openTask = (task: HomeTaskItem) => {
    if (!snapshot) {
      void homeAPI.markWorkItemRead(task.source)
        .then(() => setTaskState(current => ({ ...current, items: current.items.map(item => item.id === task.id ? { ...item, source: { ...item.source, unread: false } } : item) })))
        .catch(error => setNotice(resourceError(error, '待办已打开，但已读状态更新失败')))
    }
    if (task.href) {
      navigate(task.href)
      return
    }
    setNotice('该待办来自真实工作箱，但对应的源对象处理页尚未实现；已记录在后端 TODO。')
  }

  const viewAllWork = () => {
    if (activeFilter === 'report') {
      navigate(snapshot ? '/reports?snapshot=assets' : '/reports')
      return
    }
    setNotice(activeFilter === 'decision' ? '决策中心页面尚待确认设计。' : '完整会话历史页正在接入现有会话 API。')
  }

  return <AppShell
    className="home-shell"
    title="分析首页"
    titleMeta={<span className="home-title-meta">{pageDomainName} · {pageDate}</span>}
    eyebrow=""
  >
    <div className="home-layout">
      <section className="home-resume-panel" aria-labelledby="home-resume-title" aria-busy={work.question.status === 'loading'}>
        <header><h2 id="home-resume-title">继续上次分析</h2></header>
        {continueItem && <div className="home-resume-content">
          <span className="home-resume-icon" aria-hidden="true"><ChatCircleDots size={29} weight="duotone" /></span>
          <div className="home-resume-copy">
            <h3>{continueItem.title}</h3>
            <p>{continueItem.viewedAt} <span>·</span> {continueItem.meta.replace('问数 · ', '')} <span>·</span> {continueItem.range}</p>
            <div className="home-resume-trust"><CheckCircle size={15} weight="fill" aria-hidden="true" /><strong>已完成</strong><i aria-hidden="true" /><ShieldCheck size={15} weight="duotone" aria-hidden="true" /><span>已接入企业数据权限体系，回答可信可追溯</span><AppButton link type="button" onClick={() => setNotice('可信问数会固定当前领域、语义版本、权限与证据链。')}>了解更多<ArrowRight size={13} /></AppButton></div>
          </div>
          <AppButton className="home-resume-button" variant="primary" type="button" onClick={() => openWork(continueItem)}>继续分析</AppButton>
        </div>}
        {work.question.status === 'loading' && <div className="home-resume-state"><span className="home-loading-dot" />正在加载上次分析…</div>}
        {work.question.status === 'error' && <div className="home-resume-state is-error"><WarningCircle size={18} /><span>{work.question.error}</span><AppButton plain size="small" type="button" onClick={reloadHome}><ArrowClockwise size={13} />重新加载</AppButton></div>}
        {work.question.status === 'ready' && !continueItem && <div className="home-resume-state"><ChatCircleDots size={20} /><span>暂无可继续的分析，从下方发起一个新问题吧</span></div>}
      </section>

      <section className="home-question-launcher" aria-labelledby="home-question-title">
        <h2 id="home-question-title">或者，开始一个新问题</h2>
        <form onSubmit={startQuestion}>
          <div className="home-question-input-row">
            <label>
              <MagnifyingGlass size={19} aria-hidden="true" />
              <span className="sr-only">输入分析问题</span>
              <input value={question} maxLength={500} aria-invalid={Boolean(questionError)} aria-describedby={questionError ? 'home-question-error' : undefined} onChange={event => { setQuestion(event.target.value); setQuestionError('') }} placeholder="输入您想分析的业务问题" />
            </label>
            <AppButton className="home-ask-button" variant="primary" type="submit"><PaperPlaneTilt size={18} weight="fill" aria-hidden="true" />开始问数</AppButton>
          </div>
          {questionError && <p className="home-question-error" id="home-question-error" role="alert"><WarningCircle size={14} />{questionError}</p>}
          <div className="home-suggestions">
            <span>{suggestions.length > 0 ? '常用问题：' : '暂无保存问题，可直接输入新问题'}</span>
            {suggestions.map(item => <AppButton plain size="small" type="button" key={item} onClick={() => chooseSuggestion(item)}>{item}</AppButton>)}
          </div>
        </form>
      </section>

      <div className="home-lower-grid">
        <section className="home-work-card" aria-labelledby="home-work-title">
          <header><h2 id="home-work-title">最近工作</h2></header>
          <div className="home-work-tabs" role="tablist" aria-label="最近工作的资产类型">
            {(Object.keys(workFilterLabels) as HomeWorkFilter[]).map(value => <AppButton text type="button" role="tab" aria-selected={activeFilter === value} key={value} onClick={() => setActiveFilter(value)}>{workFilterLabels[value]}</AppButton>)}
          </div>
          <div className="home-work-table" role="table" aria-label={workFilterLabels[activeFilter]} aria-busy={recentState.status === 'loading'}>
            {visibleWork.map(item => {
              const status = statusForWork(item)
              return <AppButton text className="home-work-row" type="button" role="row" key={`${item.kind}:${item.id}`} onClick={() => openWork(item)}>
                <span className={`home-work-type is-${item.kind}`} aria-hidden="true"><WorkIcon kind={item.kind} /></span>
                <span className="home-work-name" role="cell"><strong>{item.title}</strong></span>
                <span className="home-work-meta" role="cell">{item.meta.replace('问数 · ', '分析 · ')}</span>
                <span className="home-work-time" role="cell">{item.viewedAt}</span>
                <span className={`home-work-status ${status.className}`} role="cell"><CheckCircle size={14} />{status.label}</span>
                <ArrowRight className="home-work-arrow" size={15} aria-hidden="true" />
              </AppButton>
            })}
            {recentState.status === 'loading' && <div className="home-data-state"><span className="home-loading-dot" />正在加载当前领域数据…</div>}
            {recentState.status === 'error' && <div className="home-data-state is-error"><WarningCircle size={20} /><strong>最近工作暂时无法加载</strong><span>{recentState.error}</span><AppButton plain size="small" type="button" onClick={reloadHome}><ArrowClockwise size={13} />重新加载</AppButton></div>}
            {recentState.status === 'ready' && visibleWork.length === 0 && <div className="home-data-state"><CheckCircle size={20} /><strong>暂无{workFilterLabels[activeFilter]}记录</strong><span>这里不会使用演示数据填充空态</span></div>}
          </div>
          <AppButton link className="home-view-all" type="button" onClick={viewAllWork}>查看全部<ArrowRight size={14} /></AppButton>
        </section>

        <aside className="home-task-rail" aria-labelledby="home-task-title" aria-busy={taskState.status === 'loading'}>
          <header><h2 id="home-task-title">待我处理 <span>{visibleTasks.length}</span></h2></header>
          <div className="home-task-list">
            {visibleTasks.map(task => <article className="home-task-item" key={task.id}>
              <span className={`home-task-dot is-${task.priority}`} aria-label={`${task.priority === 'high' ? '高' : task.priority === 'medium' ? '中' : '低'}优先级`} />
              <div className="home-task-copy"><h3>{task.title}</h3><p>{task.due} <span>·</span> {task.owner.replace('发起人 ', '')}</p></div>
              <AppButton link type="button" onClick={() => openTask(task)}>去处理<ArrowRight size={14} /></AppButton>
            </article>)}
            {taskState.status === 'loading' && <div className="home-task-empty"><span className="home-loading-dot" /><strong>正在加载待办</strong><span>仅查询当前用户与领域</span></div>}
            {taskState.status === 'error' && <div className="home-task-empty is-error"><WarningCircle size={24} /><strong>待办暂时无法加载</strong><span>{taskState.error}</span><AppButton plain size="small" type="button" onClick={reloadHome}><ArrowClockwise size={13} />重新加载</AppButton></div>}
            {taskState.status === 'ready' && visibleTasks.length === 0 && <div className="home-task-empty"><CheckCircle size={24} weight="duotone" /><strong>当前没有待办</strong><span>所有事项都已处理完成</span></div>}
          </div>
        </aside>
      </div>
    </div>
    {notice && <div className="home-notice" role="status"><ChartLineUp size={17} /><span>{notice}</span><AppButton text circle type="button" aria-label="关闭提示" onClick={() => setNotice('')}><X size={15} /></AppButton></div>}
  </AppShell>
}
