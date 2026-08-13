import {
  ArrowClockwise,
  ArrowRight,
  ChartLineUp,
  ChatCircleDots,
  CheckCircle,
  FileText,
  HandWaving,
  Paperclip,
  PaperPlaneTilt,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState, type ChangeEvent, type DragEvent, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import '../styles/home.css'
import { currentDomain, subscribeDomainChange } from '../lib/domain-context'
import { currentProfile } from '../lib/auth'
import {
  conversationToHomeWork,
  decisionToHomeWork,
  reportToHomeWork,
  workItemToHomeTask,
  type HomeTaskItem,
  type HomeWorkItem,
  type HomeWorkKind,
} from '../lib/home-data'
import { homeAPI, type SavedQuestionSummary, type WorkInboxItem } from '../lib/home-api'
import { reportAssetsAPI } from '../report/api/assets'
import {
  askDataAttachmentAccept,
  askDataAttachmentLimit,
  askDataAttachmentMaxBytes,
  clearAskDataAttachmentDraft,
  saveAskDataAttachmentDraft,
  type AskDataAttachmentDraftItem,
} from '../lib/ask-data-attachments'

type LoadState<T> = {
  status: 'loading' | 'ready' | 'error'
  items: T[]
  error?: string
}

type HomeWorkFilter = 'all' | HomeWorkKind

const snapshotSuggestions: SavedQuestionSummary[] = [
  { id: 'saved-1', name: '渠道毛利率趋势', questionText: '各渠道毛利率变化趋势如何？', visibility: 'PRIVATE', status: 'ACTIVE', updatedAt: '2026-08-10T10:00:00+08:00' },
  { id: 'saved-2', name: '新品效果', questionText: '新品上市效果如何？', visibility: 'TEAM', status: 'ACTIVE', updatedAt: '2026-08-09T10:00:00+08:00' },
  { id: 'saved-3', name: '库存健康度', questionText: '库存健康度异常的产品有哪些？', visibility: 'PRIVATE', status: 'ACTIVE', updatedAt: '2026-08-08T10:00:00+08:00' },
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

function combinedRecentWork(work: Record<HomeWorkKind, LoadState<HomeWorkItem>>) {
  return [
    work.question.items[0],
    work.report.items[0],
    work.question.items[1],
    work.decision.items[0],
    work.question.items[2] ?? work.question.items[3],
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
  const pageSearch = new URLSearchParams(window.location.search)
  const snapshot = import.meta.env.DEV && pageSearch.get('snapshot') === 'home'
  const snapshotEmptyTasks = snapshot && pageSearch.get('taskState') === 'empty'
  const initialWorkState = (kind: HomeWorkKind): LoadState<HomeWorkItem> => snapshot
    ? { status: 'ready', items: snapshotWorkItems[kind] }
    : { status: 'loading', items: [] }
  const [question, setQuestion] = useState('')
  const [questionError, setQuestionError] = useState('')
  const [attachments, setAttachments] = useState<AskDataAttachmentDraftItem[]>([])
  const [attachmentDropActive, setAttachmentDropActive] = useState(false)
  const attachmentInputRef = useRef<HTMLInputElement>(null)
  const [displayName, setDisplayName] = useState(snapshot ? '程' : '')
  const [activeFilter, setActiveFilter] = useState<HomeWorkFilter>('all')
  const [notice, setNotice] = useState('')
  const [reloadRevision, setReloadRevision] = useState(0)
  const [suggestions, setSuggestions] = useState<SavedQuestionSummary[]>(snapshot ? snapshotSuggestions : [])
  const [work, setWork] = useState<Record<HomeWorkKind, LoadState<HomeWorkItem>>>(() => ({
    question: initialWorkState('question'),
    report: initialWorkState('report'),
    decision: initialWorkState('decision'),
  }))
  const [taskState, setTaskState] = useState<LoadState<HomeTaskItem>>(snapshot
    ? { status: 'ready', items: snapshotEmptyTasks ? [] : snapshotTasks }
    : { status: 'loading', items: [] })

  useEffect(() => subscribeDomainChange(() => {
    if (snapshot) return
    setWork({ question: { status: 'loading', items: [] }, report: { status: 'loading', items: [] }, decision: { status: 'loading', items: [] } })
    setTaskState({ status: 'loading', items: [] })
    setReloadRevision(value => value + 1)
  }), [snapshot])

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    void currentProfile()
      .then(profile => { if (!cancelled) setDisplayName(profile.displayName.trim()) })
      .catch(() => { if (!cancelled) setDisplayName('') })
    return () => { cancelled = true }
  }, [snapshot])

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
        if (!cancelled) setSuggestions(result.items.filter(item => item.status === 'ACTIVE').slice(0, 3))
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

  const reloadHome = () => {
    if (snapshot) return
    setWork({ question: { status: 'loading', items: [] }, report: { status: 'loading', items: [] }, decision: { status: 'loading', items: [] } })
    setTaskState({ status: 'loading', items: [] })
    setReloadRevision(value => value + 1)
  }

  const startQuestion = (event?: FormEvent) => {
    event?.preventDefault()
    const value = question.trim() || (attachments.length > 0 ? '请分析附件中的关键信息、异常与趋势' : '')
    if (!value) {
      setQuestionError('请输入需要分析的问题')
      return
    }
    setQuestionError('')
    if (attachments.length > 0) saveAskDataAttachmentDraft(attachments)
    else clearAskDataAttachmentDraft()
    const params = new URLSearchParams({ q: value, autoSubmit: '1' })
    if (attachments.length > 0) params.set('attachments', '1')
    if (snapshot) params.set('snapshot', 'home-question')
    navigate(`/ask-data?${params.toString()}`)
  }

  const acceptAttachments = async (files: File[]) => {
    if (files.length === 0) return
    const remaining = Math.max(0, askDataAttachmentLimit - attachments.length)
    if (remaining === 0) {
      setNotice(`最多添加 ${askDataAttachmentLimit} 个附件`)
      return
    }
    const existing = new Set(attachments.map(item => item.id))
    const accepted: AskDataAttachmentDraftItem[] = []
    const rejected: string[] = []
    for (const file of files.slice(0, remaining)) {
      const id = `${file.name}:${file.size}:${file.lastModified}`
      if (existing.has(id)) continue
      if (!/\.(txt|csv|json|md)$/i.test(file.name)) {
        rejected.push(`${file.name}：格式不支持`)
        continue
      }
      if (file.size > askDataAttachmentMaxBytes) {
        rejected.push(`${file.name}：超过 1MB`)
        continue
      }
      const excerpt = (await file.text()).replaceAll('\u0000', '').trim().slice(0, 900)
      if (!excerpt) {
        rejected.push(`${file.name}：文件为空`)
        continue
      }
      accepted.push({ id, name: file.name, size: file.size, type: file.type, excerpt })
      existing.add(id)
    }
    if (accepted.length > 0) setAttachments(current => [...current, ...accepted].slice(0, askDataAttachmentLimit))
    const omitted = Math.max(0, files.length - remaining)
    if (rejected.length > 0 || omitted > 0) {
      setNotice([rejected.join('；'), omitted > 0 ? `另有 ${omitted} 个文件超出数量限制` : ''].filter(Boolean).join('；'))
    }
  }

  const chooseAttachments = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.currentTarget.files ?? [])
    event.currentTarget.value = ''
    await acceptAttachments(files)
  }

  const dropAttachments = (event: DragEvent<HTMLFormElement>) => {
    event.preventDefault()
    setAttachmentDropActive(false)
    void acceptAttachments(Array.from(event.dataTransfer.files ?? []))
  }

  const removeAttachment = (id: string) => {
    setAttachments(current => current.filter(item => item.id !== id))
  }

  const formatAttachmentSize = (size: number) => size < 1024 ? `${size} B` : `${Math.ceil(size / 1024)} KB`

  const chooseSuggestion = async (item: SavedQuestionSummary) => {
    if (snapshot) {
      setQuestion(item.questionText)
      setQuestionError('')
      return
    }
    setNotice(`正在运行“${item.name}”…`)
    try {
      const result = await homeAPI.openSavedQuestion(item.id)
      navigate(`/ask-data?runId=${encodeURIComponent(result.runId)}`)
    } catch (cause) {
      setNotice(resourceError(cause, '常用问题运行失败，请稍后重试'))
    }
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
    navigate(snapshot ? '/decisions?snapshot=decisions' : `/decisions?decisionId=${encodeURIComponent(item.id)}`)
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
    const center = task.source.allowedActions.some(action => action === 'APPROVE' || action === 'REJECT') ? '/approvals' : '/tasks'
    navigate(`${center}?itemType=${encodeURIComponent(task.source.type)}&itemId=${encodeURIComponent(task.source.objectId)}`)
  }

  const viewAllWork = () => {
    if (activeFilter === 'report') {
      navigate(snapshot ? '/reports?snapshot=assets' : '/reports')
      return
    }
    navigate(activeFilter === 'decision' ? '/decisions' : '/ask-data')
  }

  return <AppShell
    className="home-shell"
    hidePageHeader
  >
    <div className="home-layout">
      <header className="home-intro">
        <h1>您好，{displayName || '欢迎回来'} <HandWaving size={26} weight="duotone" aria-hidden="true" /></h1>
        <p>向我提出业务问题，或上传文件，我将为您进行智能分析与洞察。</p>
      </header>

      <section className="home-question-launcher" aria-labelledby="home-question-title">
        <h2 className="sr-only" id="home-question-title">开始智能分析</h2>
        <form
          className={`home-chat-composer${attachmentDropActive ? ' is-dragging' : ''}`}
          onSubmit={startQuestion}
          onDragEnter={event => { event.preventDefault(); setAttachmentDropActive(true) }}
          onDragOver={event => { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; setAttachmentDropActive(true) }}
          onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setAttachmentDropActive(false) }}
          onDrop={dropAttachments}
        >
          <label className="home-chat-field">
            <span className="sr-only">输入分析问题</span>
            <textarea rows={3} value={question} maxLength={500} aria-invalid={Boolean(questionError)} aria-describedby={questionError ? 'home-question-error' : undefined} onChange={event => { setQuestion(event.target.value); setQuestionError('') }} placeholder="今天想分析什么？（支持多行输入）" />
          </label>
          {attachments.length > 0 && <div className="home-attachment-list" aria-label="已添加附件">
            {attachments.map(item => <span className="home-attachment-chip" key={item.id}><FileText size={15} /><span><strong>{item.name}</strong><small>{formatAttachmentSize(item.size)}</small></span><AppButton text circle size="small" type="button" aria-label={`移除附件 ${item.name}`} onClick={() => removeAttachment(item.id)}><X size={13} /></AppButton></span>)}
          </div>}
          {questionError && <p className="home-question-error" id="home-question-error" role="alert"><WarningCircle size={14} />{questionError}</p>}
          <footer className="home-chat-toolbar">
            <div className="home-upload-control">
              <input ref={attachmentInputRef} className="sr-only" type="file" multiple accept={askDataAttachmentAccept} onChange={event => void chooseAttachments(event)} />
              <AppButton text type="button" onClick={() => attachmentInputRef.current?.click()}><Paperclip size={18} />上传文件或拖拽到此处</AppButton>
              <small>支持 TXT、CSV、JSON、MD，单个文件不超过 1MB</small>
            </div>
            <span className="home-question-count" aria-hidden="true">{question.length}/500</span>
            <AppButton className="home-ask-button" variant="primary" circle type="submit" aria-label="发送问题"><PaperPlaneTilt size={20} weight="fill" aria-hidden="true" /></AppButton>
          </footer>
        </form>
        <div className="home-suggestions" aria-label="常用问题">
          {suggestions.length > 0
            ? suggestions.map(item => <AppButton plain type="button" key={item.id} title={item.questionText} onClick={() => void chooseSuggestion(item)}><ChartLineUp size={16} />{item.questionText}</AppButton>)
            : <span>暂无保存问题，可直接输入新问题</span>}
        </div>
      </section>

      <div className="home-lower-grid">
        <section className="home-work-card" aria-labelledby="home-work-title">
          <header><h2 id="home-work-title">最近工作</h2></header>
          <div className="home-work-tabs" role="tablist" aria-label="最近工作的资产类型">
            {(Object.keys(workFilterLabels) as HomeWorkFilter[]).map(value => <AppButton text type="button" role="tab" aria-selected={activeFilter === value} key={value} onClick={() => setActiveFilter(value)}>{workFilterLabels[value]}</AppButton>)}
          </div>
          <div className="home-work-columns" role="row" aria-hidden="true">
            <span />
            <span>名称</span>
            <span>类型 / 领域</span>
            <span>更新时间</span>
            <span>状态</span>
            <span />
          </div>
          <div className="home-work-table" role="table" aria-label={workFilterLabels[activeFilter]} aria-busy={recentState.status === 'loading'}>
            {visibleWork.map(item => {
              const status = statusForWork(item)
              const isResumeRow = item === continueItem && (activeFilter === 'all' || activeFilter === 'question')
              return <AppButton text className={`home-work-row${isResumeRow ? ' is-resume' : ''}`} type="button" role="row" key={`${item.kind}:${item.id}`} onClick={() => openWork(item)}>
                <span className={`home-work-type is-${item.kind}`} aria-hidden="true"><WorkIcon kind={item.kind} /></span>
                <span className="home-work-name" role="cell"><strong>{isResumeRow ? `继续上次分析 · ${item.title}` : item.title}</strong></span>
                <span className="home-work-meta" role="cell">{item.meta.replace('问数 · ', '分析 · ')}</span>
                <span className="home-work-time" role="cell">{item.viewedAt}</span>
                <span className={`home-work-status ${status.className}`} role="cell"><CheckCircle size={14} />{status.label}</span>
                <ArrowRight className="home-work-arrow" size={15} aria-hidden="true" />
              </AppButton>
            })}
            {recentState.status === 'loading' && <div className="home-data-state"><span className="home-loading-dot" />正在加载当前领域数据…</div>}
            {recentState.status === 'error' && <div className="home-data-state is-error"><WarningCircle size={20} /><strong>最近工作暂时无法加载</strong><span>{recentState.error}</span><AppButton plain size="small" type="button" onClick={reloadHome}><ArrowClockwise size={13} />重新加载</AppButton></div>}
            {recentState.status === 'ready' && visibleWork.length === 0 && <div className="home-data-state"><CheckCircle size={20} /><strong>暂无{workFilterLabels[activeFilter]}记录</strong><span>完成相关工作后，最近更新会自动汇总在这里</span></div>}
          </div>
          <AppButton link className="home-view-all" type="button" onClick={viewAllWork}>查看全部<ArrowRight size={14} /></AppButton>
        </section>

        <aside className="home-task-rail" aria-labelledby="home-task-title" aria-busy={taskState.status === 'loading'}>
          <header><h2 id="home-task-title">待我处理 <span>{visibleTasks.length}</span></h2></header>
          <div className="home-task-list">
            {visibleTasks.map(task => <article className="home-task-item" key={task.id}>
              <span className={`home-task-dot is-${task.priority}`} aria-label={`${task.priority === 'high' ? '高' : task.priority === 'medium' ? '中' : '低'}优先级`} />
              <div className="home-task-copy"><h3>{task.title}</h3><p>{task.due}{task.owner && <> <span>·</span> {task.owner.replace('发起人 ', '')}</>}</p></div>
              <AppButton link type="button" onClick={() => openTask(task)}>去处理<ArrowRight size={14} /></AppButton>
            </article>)}
            {taskState.status === 'loading' && <div className="home-task-empty"><span className="home-loading-dot" /><strong>正在加载待办</strong><span>仅查询当前用户与领域</span></div>}
            {taskState.status === 'error' && <div className="home-task-empty is-error"><WarningCircle size={24} /><strong>待办暂时无法加载</strong><span>{taskState.error}</span><AppButton plain size="small" type="button" onClick={reloadHome}><ArrowClockwise size={13} />重新加载</AppButton></div>}
            {taskState.status === 'ready' && visibleTasks.length === 0 && <div className="home-task-empty"><img className="home-task-empty-visual" src="/home-empty-task.png" alt="" /><strong>当前没有待办</strong><span>所有事项都已处理完成</span></div>}
          </div>
        </aside>
      </div>
    </div>
    {notice && <div className="home-notice" role="status"><ChartLineUp size={17} /><span>{notice}</span><AppButton text circle type="button" aria-label="关闭提示" onClick={() => setNotice('')}><X size={15} /></AppButton></div>}
  </AppShell>
}
