import {
  ArrowClockwise,
  ArrowRight,
  CalendarBlank,
  ChartLineUp,
  CheckCircle,
  FileText,
  PaperPlaneTilt,
  Question,
  ShieldCheck,
  WarningCircle,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { currentDomain } from '../lib/domain-context'
import {
  conversationToHomeWork,
  decisionToHomeWork,
  reportToHomeWork,
  workItemToHomeTask,
  type HomeTaskItem,
  type HomeTaskPriority,
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

const snapshotSuggestions = [
  '本月毛利率下降的主要原因是什么？',
  '各渠道毛利率变化趋势如何？',
  '新品上市效果如何？',
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
    { id: 'r-1', title: '供应链月度经营报告', meta: '报告 · v7 已发布', viewedAt: '今天 10:12', range: '2026 年 7 月', kind: 'report' },
    { id: 'r-2', title: '质量成本分析', meta: '报告 · v9 已发布', viewedAt: '昨天 16:47', range: '本季度', kind: 'report' },
    { id: 'r-3', title: '渠道健康度看板', meta: '报告 · v5 已发布', viewedAt: '08-09 14:22', range: '近 12 个月', kind: 'report' },
  ],
  decision: [
    { id: 'd-1', title: '渠道政策调整专项方案', meta: '决策 · 待审批', viewedAt: '今天 09:58', range: '企业经营', kind: 'decision' },
    { id: 'd-2', title: '区域销售目标纠偏行动', meta: '决策 · 执行中', viewedAt: '昨天 17:10', range: '华东区域', kind: 'decision' },
    { id: 'd-3', title: '库存周转改善计划', meta: '决策 · 待复盘', viewedAt: '08-08 11:20', range: '冰箱产业', kind: 'decision' },
  ],
}

function snapshotSource(type: string, objectId: string, sourceHref = ''): WorkInboxItem {
  return { type, objectId, status: 'PENDING', overdue: false, domainId: 'snapshot-domain', summary: '', sourceHref, allowedActions: ['OPEN'], unread: true, updatedAt: '2026-08-10T00:00:00+08:00', version: '1' }
}

const snapshotTasks: HomeTaskItem[] = [
  { id: 't-1', source: snapshotSource('DATA_REQUEST', 't-1'), title: '毛利率异常波动原因分析', summary: '请在本周内确认 7 月毛利率下降的主要原因', due: '截止：2026-08-12（周三）', owner: '陈晨', priority: 'high', href: '/ask-data?q=毛利率异常波动原因分析&snapshot=home-question' },
  { id: 't-2', source: snapshotSource('REPORT_EXPORT_FAILED', 't-2'), title: '新品上市复盘报告发布', summary: '新品上市后 30 天复盘报告待发布', due: '截止：2026-08-11（周二）', owner: '刘洋', priority: 'high', href: '/reports?snapshot=assets' },
  { id: 't-3', source: snapshotSource('DECISION_APPROVAL', 't-3'), title: '渠道政策效果评估', summary: '评估 7 月渠道政策对销量的影响', due: '截止：2026-08-14（周五）', owner: '李娜', priority: 'medium' },
  { id: 't-4', source: snapshotSource('ACTION_ASSIGNED', 't-4'), title: '区域销售目标达成跟进', summary: '部分区域达成率未达预期，请跟进原因', due: '截止：2026-08-15（周六）', owner: '张磊', priority: 'medium' },
  { id: 't-5', source: snapshotSource('DATA_SOURCE_PUBLICATION', 't-5'), title: '数据质量问题处理', summary: '库存数据存在缺失，请确认修复方案', due: '截止：2026-08-18（周二）', owner: '周明', priority: 'low', href: '/data-sources?snapshot=home-data' },
  { id: 't-6', source: snapshotSource('REPORT_EXPORT_FAILED', 't-6'), title: '经营报告口径变更确认', summary: '报告引用的销售额口径已更新，请确认影响', due: '截止：2026-08-19（周三）', owner: '王敏', priority: 'high', href: '/reports?snapshot=assets' },
  { id: 't-7', source: snapshotSource('ACTION_ASSIGNED', 't-7'), title: '供应链行动进度更新', summary: '两项行动即将到期，请补充最新执行进度', due: '截止：2026-08-20（周四）', owner: '陈晨', priority: 'medium' },
  { id: 't-8', source: snapshotSource('REPORT_EXPORT_FAILED', 't-8'), title: '关注报告订阅确认', summary: '月度经营报告订阅规则需要重新确认', due: '截止：2026-08-21（周五）', owner: '刘洋', priority: 'low', href: '/reports?snapshot=assets' },
]

const tabLabels: Record<HomeWorkKind, string> = {
  question: '最近分析',
  report: '最近报告',
  decision: '进行中决策',
}

const priorityLabels: Record<HomeTaskPriority | 'all', string> = {
  all: '全部',
  high: '高',
  medium: '中',
  low: '低',
}

function WorkIcon({ kind }: { kind: HomeWorkKind }) {
  if (kind === 'report') return <FileText size={17} aria-hidden="true" />
  if (kind === 'decision') return <CheckCircle size={17} aria-hidden="true" />
  return <Question size={17} aria-hidden="true" />
}

function resourceError(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback
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
  const [activeTab, setActiveTab] = useState<HomeWorkKind>('question')
  const [priority, setPriority] = useState<HomeTaskPriority | 'all'>('all')
  const [notice, setNotice] = useState('')
  const [reloadRevision, setReloadRevision] = useState(0)
  const [suggestions, setSuggestions] = useState(snapshot ? snapshotSuggestions : [])
  const [work, setWork] = useState<Record<HomeWorkKind, LoadState<HomeWorkItem>>>(() => ({
    question: initialWorkState('question'),
    report: initialWorkState('report'),
    decision: initialWorkState('decision'),
  }))
  const [taskState, setTaskState] = useState<LoadState<HomeTaskItem>>(snapshot
    ? { status: 'ready', items: snapshotTasks }
    : { status: 'loading', items: [] })

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

  const visibleTasks = useMemo(
    () => priority === 'all' ? taskState.items.slice(0, 5) : taskState.items.filter(task => task.priority === priority),
    [priority, taskState.items],
  )

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
    if (activeTab === 'report') {
      navigate(snapshot ? '/reports?snapshot=assets' : '/reports')
      return
    }
    setNotice(activeTab === 'question' ? '完整会话历史页正在接入现有会话 API。' : '决策中心页面尚待确认设计。')
  }

  const activeWork = work[activeTab]
  const taskCounts = {
    all: taskState.items.length,
    high: taskState.items.filter(item => item.priority === 'high').length,
    medium: taskState.items.filter(item => item.priority === 'medium').length,
    low: taskState.items.filter(item => item.priority === 'low').length,
  }

  return <AppShell className="home-shell" title="分析首页" eyebrow="">
    <div className="home-layout">
      <div className="home-primary-column">
        <section className="home-question-card" aria-labelledby="home-question-title">
          <header>
            <div><h2 id="home-question-title">今天想了解什么？</h2><p>用自然语言提问，获得可信、可追溯的分析结果</p></div>
          </header>
          <form onSubmit={startQuestion}>
            <label>
              <span className="sr-only">输入分析问题</span>
              <textarea value={question} maxLength={500} aria-invalid={Boolean(questionError)} aria-describedby={questionError ? 'home-question-error' : undefined} onChange={event => { setQuestion(event.target.value); setQuestionError('') }} placeholder="请输入您的问题，例如：本月毛利率下降的主要原因是什么？" />
              <small>{question.length} / 500</small>
            </label>
            {questionError && <p className="home-question-error" id="home-question-error" role="alert"><WarningCircle size={14} />{questionError}</p>}
            <div className="home-question-actions">
              <div><span>{suggestions.length > 0 ? '来自我的保存问题' : '暂无保存问题，可直接输入新问题'}</span>{suggestions.length > 0 && <div>{suggestions.map(item => <button type="button" key={item} onClick={() => chooseSuggestion(item)}>{item}</button>)}</div>}</div>
              <button className="primary-button home-ask-button" type="submit"><PaperPlaneTilt size={18} weight="fill" aria-hidden="true" />开始问数</button>
            </div>
          </form>
          <footer><ShieldCheck size={18} weight="duotone" aria-hidden="true" /><span>已接入企业数据权限体系，回答可信可追溯</span><button type="button" onClick={() => setNotice('可信问数会固定当前领域、语义版本、权限与证据链。')}>了解更多<ArrowRight size={13} /></button></footer>
        </section>

        <section className="home-work-card" aria-labelledby="home-work-title">
          <header><h2 id="home-work-title">继续工作</h2></header>
          <div className="home-work-tabs" role="tablist" aria-label="继续工作的资产类型">
            {(Object.keys(tabLabels) as HomeWorkKind[]).map(kind => <button type="button" role="tab" aria-selected={activeTab === kind} key={kind} onClick={() => setActiveTab(kind)}>{tabLabels[kind]}</button>)}
          </div>
          <div className="home-work-table" role="table" aria-label={tabLabels[activeTab]} aria-busy={activeWork.status === 'loading'}>
            <div className="home-work-table-header" role="row"><span role="columnheader">内容</span><span role="columnheader">最近更新时间</span><span role="columnheader">状态</span><span role="columnheader">操作</span></div>
            {activeWork.items.map(item => <button className="home-work-row" type="button" role="row" key={item.id} onClick={() => openWork(item)}>
              <span className={`home-work-type is-${item.kind}`} aria-hidden="true"><WorkIcon kind={item.kind} /></span>
              <span className="home-work-name" role="cell"><strong>{item.title}</strong><small>{item.meta}</small></span>
              <span role="cell">{item.viewedAt}</span>
              <span role="cell">{item.range}</span>
              <span className="home-work-action" role="cell">继续查看<ArrowRight size={13} /></span>
            </button>)}
            {activeWork.status === 'loading' && <div className="home-data-state"><span className="home-loading-dot" />正在加载当前领域数据…</div>}
            {activeWork.status === 'error' && <div className="home-data-state is-error"><WarningCircle size={20} /><strong>该模块暂时无法加载</strong><span>{activeWork.error}</span><button type="button" onClick={reloadHome}><ArrowClockwise size={13} />重新加载</button></div>}
            {activeWork.status === 'ready' && activeWork.items.length === 0 && <div className="home-data-state"><CheckCircle size={20} /><strong>暂无{tabLabels[activeTab]}</strong><span>这里不会使用演示数据填充空态</span></div>}
          </div>
          <button className="home-view-all" type="button" onClick={viewAllWork}>查看全部<ArrowRight size={13} /></button>
        </section>
      </div>

      <aside className="home-task-rail" aria-labelledby="home-task-title" aria-busy={taskState.status === 'loading'}>
        <header><h2 id="home-task-title">我的待办</h2><button type="button" onClick={() => setNotice('完整任务中心页面尚待确认；首页已接入统一工作箱 API。')}>查看全部</button></header>
        <div className="home-task-filters" role="tablist" aria-label="待办紧急度筛选">
          {(Object.keys(priorityLabels) as Array<HomeTaskPriority | 'all'>).map(value => <button type="button" role="tab" aria-selected={priority === value} key={value} onClick={() => setPriority(value)}>{priorityLabels[value]} <span>{taskCounts[value]}</span></button>)}
        </div>
        <div className="home-task-list">
          {visibleTasks.map(task => <article className="home-task-item" key={task.id}>
            <div className="home-task-title-row"><span className={`home-priority is-${task.priority}`}>{priorityLabels[task.priority]}</span><h3>{task.title}</h3>{task.source.unread && <span className="home-task-unread">未读</span>}</div>
            <p>{task.summary}</p>
            <time><CalendarBlank size={14} />{task.due}</time>
            <footer><span><span className="home-task-avatar" aria-hidden="true">{task.owner.replace('发起人 ', '').slice(0, 1)}</span>{task.owner}</span><button type="button" onClick={() => openTask(task)}>去处理<ArrowRight size={13} /></button></footer>
          </article>)}
          {taskState.status === 'loading' && <div className="home-task-empty"><span className="home-loading-dot" /><strong>正在加载待办</strong><span>仅查询当前用户与领域</span></div>}
          {taskState.status === 'error' && <div className="home-task-empty is-error"><WarningCircle size={24} /><strong>待办暂时无法加载</strong><span>{taskState.error}</span><button type="button" onClick={reloadHome}><ArrowClockwise size={13} />重新加载</button></div>}
          {taskState.status === 'ready' && visibleTasks.length === 0 && <div className="home-task-empty"><CheckCircle size={24} weight="duotone" /><strong>当前没有待办</strong><span>切换紧急度查看其他任务</span></div>}
        </div>
        <button className="home-task-more" type="button" onClick={() => setNotice('完整任务中心页面尚待确认；首页已接入统一工作箱 API。')}>查看全部待办<ArrowRight size={13} /></button>
      </aside>
    </div>
    {notice && <div className="home-notice" role="status"><ChartLineUp size={17} /><span>{notice}</span><button type="button" aria-label="关闭提示" onClick={() => setNotice('')}>×</button></div>}
  </AppShell>
}
