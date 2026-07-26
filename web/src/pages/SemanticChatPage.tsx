import {
  ChartLineUp,
  ChatCenteredDots,
  CheckCircle,
  ClockCounterClockwise,
  Database,
  Graph,
  PaperPlaneTilt,
  Plus,
  ShieldCheck,
  Sparkle,
  TestTube,
  ThumbsDown,
  ThumbsUp,
  WarningCircle,
} from '@phosphor-icons/react'
import { FormEvent, KeyboardEvent, useEffect, useRef, useState } from 'react'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import {
  semanticChatAPI,
  type GoldenQuestionReplay,
  type GoldenQuestionSet,
  type SemanticGraphStatus,
  type SemanticQueryExecution,
  type SemanticQueryPlan,
} from '../lib/semantic-chat'

type Feedback = 'ACCURATE' | 'INACCURATE'

type ChatMessage = {
  id: string
  role: 'USER' | 'ASSISTANT'
  content: string
  createdAt: string
  pending?: boolean
  question?: string
  plan?: SemanticQueryPlan
  execution?: SemanticQueryExecution
  errorCode?: string
  feedback?: Feedback
}

type ChatSession = {
  id: string
  title: string
  createdAt: string
  messages: ChatMessage[]
}

type GoldenRunState = {
  completed: number
  total: number
  results: GoldenQuestionReplay[]
}

const storageKey = 'intelligent-report-semantic-chat-v1'
const suggestionQuestions = ['本月销售额是多少？', '各区域销售额排名前 10', '最近 30 天销售趋势', '销售额同比有什么变化？']
const newID = () => typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
  ? crypto.randomUUID()
  : `${Date.now()}-${Math.random().toString(16).slice(2)}`
const now = () => new Date().toISOString()
const createSession = (): ChatSession => ({ id: newID(), title: '新的分析对话', createdAt: now(), messages: [] })

function readSessions(): ChatSession[] {
  try {
    const parsed = JSON.parse(sessionStorage.getItem(storageKey) ?? '[]') as ChatSession[]
    if (Array.isArray(parsed) && parsed.length > 0 && parsed.every(item => item.id && Array.isArray(item.messages))) return parsed
  } catch {
    // 损坏的会话缓存按新会话处理，不阻断问答。
  }
  return [createSession()]
}

function formatValue(value: unknown) {
  if (value == null) return '—'
  if (typeof value === 'number') return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value)
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

function buildAnswer(execution: SemanticQueryExecution) {
  const { result, comparison } = execution
  if (result.rowCount === 0 || result.rows.length === 0) return '在当前筛选条件与数据权限范围内没有查询到结果。'
  const first = result.rows[0] ?? []
  let answer = result.rowCount === 1
    ? `查询结果：${result.columns.slice(0, 4).map((column, index) => `${column}为 ${formatValue(first[index])}`).join('，')}。`
    : `已查询到 ${result.rowCount} 条结果，按当前分析口径展示前 ${Math.min(result.rows.length, 100)} 条。`
  if (comparison?.baseline.rows.length) {
    const currentValue = first.find(value => typeof value === 'number')
    const baselineValue = comparison.baseline.rows[0]?.find(value => typeof value === 'number')
    if (typeof currentValue === 'number' && typeof baselineValue === 'number' && baselineValue !== 0) {
      const change = ((currentValue - baselineValue) / Math.abs(baselineValue)) * 100
      answer += ` 相比基准窗口${change >= 0 ? '增长' : '下降'} ${Math.abs(change).toFixed(1)}%。`
    }
  }
  return answer
}

function planFailureAnswer(plan: SemanticQueryPlan) {
  const copy: Record<string, string> = {
    METRIC_NOT_FOUND: '没有找到可以证明的已发布指标。请换用业务指标名称，或先在指标资产中完成发布。',
    METRIC_AMBIGUOUS: '匹配到多个同名指标，当前证据不足以安全选择。请补充业务域或数据集范围。',
    DIMENSION_NOT_FOUND: '没有找到与该指标兼容的已发布维度，请换一个分析视角。',
    DIMENSION_AMBIGUOUS: '匹配到多个同名维度，请进一步说明具体维度。',
    MEMBER_NOT_FOUND: '没有找到唯一匹配的维度值，请确认名称或使用标准成员值。',
    MEMBER_AMBIGUOUS: '该维度值存在多个匹配，当前不会自动猜测，请补充维度名称。',
    MATERIALIZATION_NOT_AVAILABLE: '指标存在，但缺少与当前版本完全一致的可用物化结果。',
    METRIC_TIME_FIELD_NOT_AVAILABLE: '该指标尚未配置可验证的时间字段，无法执行时间分析。',
  }
  return copy[plan.failureCode ?? ''] ?? `当前问题未通过可信查询门禁（${plan.failureCode || plan.status}），系统没有执行数据查询。`
}

function answerError(cause: unknown) {
  if (cause instanceof RequestError) {
    const copy: Record<string, string> = {
      GRAPH_NOT_READY: '语义图正在更新，暂时不能创建新的可信查询计划。',
      SEMANTIC_QA_DISABLED: '当前租户尚未启用智能问答能力。',
      UNPROVEN_PATH: '无法证明指标、维度、数据表和数据源之间的完整路径。',
      CONFLICT: '问答期间语义图发生变化，本轮结果已安全丢弃，请重试。',
    }
    return { code: cause.detail.code, message: copy[cause.detail.code] ?? cause.message }
  }
  if (cause instanceof DOMException && cause.name === 'AbortError') return { code: 'CANCELED', message: '本轮问答已取消。' }
  return { code: 'REQUEST_FAILED', message: cause instanceof Error ? cause.message : '问答失败，请稍后重试。' }
}

function statusLabel(status?: string) {
  const labels: Record<string, string> = {
    READY: '计划已验证', EXECUTED: '查询已执行', GAP: '能力缺口', AMBIGUOUS: '需要澄清',
    REJECTED: '路径被拒绝', FAILED: '执行失败',
  }
  return status ? labels[status] ?? status : '等待验证'
}

export function SemanticChatPage() {
  const [sessions, setSessions] = useState<ChatSession[]>(readSessions)
  const [activeSessionID, setActiveSessionID] = useState(() => sessions[0].id)
  const [selectedMessageID, setSelectedMessageID] = useState('')
  const [question, setQuestion] = useState('')
  const [graphStatus, setGraphStatus] = useState<SemanticGraphStatus>()
  const [graphError, setGraphError] = useState('')
  const [goldenSets, setGoldenSets] = useState<GoldenQuestionSet[]>([])
  const [selectedGoldenSetID, setSelectedGoldenSetID] = useState('')
  const [goldenRun, setGoldenRun] = useState<GoldenRunState>()
  const [goldenError, setGoldenError] = useState('')
  const controllerRef = useRef<AbortController | undefined>(undefined)
  const messageEndRef = useRef<HTMLDivElement>(null)

  const activeSession = sessions.find(item => item.id === activeSessionID) ?? sessions[0]
  const assistantMessages = activeSession.messages.filter(message => message.role === 'ASSISTANT' && !message.pending)
  const selectedMessage = assistantMessages.find(message => message.id === selectedMessageID) ?? assistantMessages.at(-1)
  const isPending = activeSession.messages.some(message => message.pending)

  useEffect(() => {
    sessionStorage.setItem(storageKey, JSON.stringify(sessions.slice(0, 8).map(session => ({
      ...session,
      messages: session.messages.slice(-60),
    }))))
  }, [sessions])

  useEffect(() => {
    let active = true
    Promise.allSettled([semanticChatAPI.graphStatus(), semanticChatAPI.listGoldenQuestionSets()]).then(results => {
      if (!active) return
      const [graph, sets] = results
      if (graph.status === 'fulfilled') setGraphStatus(graph.value)
      else setGraphError(graph.reason instanceof Error ? graph.reason.message : '语义图状态加载失败')
      if (sets.status === 'fulfilled') {
        setGoldenSets(sets.value)
        const preferred = sets.value.find(item => item.status === 'ACTIVE') ?? sets.value[0]
        if (preferred) setSelectedGoldenSetID(preferred.id)
      }
    })
    return () => { active = false; controllerRef.current?.abort() }
  }, [])

  useEffect(() => {
    if (typeof messageEndRef.current?.scrollIntoView === 'function') {
      messageEndRef.current.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
    }
  }, [activeSession.messages.length, isPending])

  const quality = (() => {
    const attempts = assistantMessages.filter(message => !message.pending)
    const executed = attempts.filter(message => message.execution)
    const evidenced = executed.filter(message => {
      const evidence = message.execution?.evidence
      return evidence?.executionRevalidated && evidence.permissionDecision && evidence.freshnessDecision && evidence.compatibilityDecision
    })
    const rated = attempts.filter(message => message.feedback)
    const accurate = rated.filter(message => message.feedback === 'ACCURATE')
    return {
      successRate: attempts.length ? Math.round((executed.length / attempts.length) * 100) : undefined,
      evidenceRate: executed.length ? Math.round((evidenced.length / executed.length) * 100) : undefined,
      manualRate: rated.length ? Math.round((accurate.length / rated.length) * 100) : undefined,
    }
  })()

  function updateSession(sessionID: string, mutate: (session: ChatSession) => ChatSession) {
    setSessions(current => current.map(session => session.id === sessionID ? mutate(session) : session))
  }

  function updateMessage(sessionID: string, messageID: string, mutate: (message: ChatMessage) => ChatMessage) {
    updateSession(sessionID, session => ({
      ...session,
      messages: session.messages.map(message => message.id === messageID ? mutate(message) : message),
    }))
  }

  function startConversation() {
    controllerRef.current?.abort()
    const session = createSession()
    setSessions(current => [session, ...current].slice(0, 8))
    setActiveSessionID(session.id)
    setSelectedMessageID('')
    setQuestion('')
  }

  async function submitQuestion(value = question) {
    const content = value.trim()
    if (!content || isPending) return
    const sessionID = activeSession.id
    const contextPlanID = [...activeSession.messages].reverse().find(message =>
      message.role === 'ASSISTANT' && (message.plan?.status === 'READY' || message.plan?.status === 'EXECUTED'),
    )?.plan?.id
    const userMessage: ChatMessage = { id: newID(), role: 'USER', content, createdAt: now() }
    const assistantMessage: ChatMessage = {
      id: newID(), role: 'ASSISTANT', content: '正在理解问题并验证语义路径…',
      createdAt: now(), pending: true, question: content,
    }
    updateSession(sessionID, session => ({
      ...session,
      title: session.messages.length === 0 ? content.slice(0, 24) : session.title,
      messages: [...session.messages, userMessage, assistantMessage],
    }))
    setQuestion('')
    setSelectedMessageID(assistantMessage.id)
    const controller = new AbortController()
    controllerRef.current = controller
    try {
      const plan = await semanticChatAPI.planQuestion({
        question: content,
        contextQueryPlanId: contextPlanID,
        signal: controller.signal,
      })
      if (plan.status !== 'READY') {
        updateMessage(sessionID, assistantMessage.id, message => ({
          ...message, pending: false, plan, content: planFailureAnswer(plan),
        }))
        return
      }
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message, plan, content: '语义路径已验证，正在执行受控查询…',
      }))
      const execution = await semanticChatAPI.executePlan(plan, controller.signal)
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message,
        pending: false,
        plan: execution.queryPlan,
        execution,
        content: buildAnswer(execution),
      }))
    } catch (cause) {
      const error = answerError(cause)
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message, pending: false, errorCode: error.code, content: error.message,
      }))
    } finally {
      if (controllerRef.current === controller) controllerRef.current = undefined
    }
  }

  function onSubmit(event: FormEvent) {
    event.preventDefault()
    void submitQuestion()
  }

  function onComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      void submitQuestion()
    }
  }

  function rateSelected(feedback: Feedback) {
    if (!selectedMessage) return
    updateMessage(activeSession.id, selectedMessage.id, message => ({
      ...message,
      feedback: message.feedback === feedback ? undefined : feedback,
    }))
  }

  async function runGoldenQuestions() {
    if (!selectedGoldenSetID || goldenRun?.completed !== goldenRun?.total) return
    setGoldenError('')
    try {
      const questions = await semanticChatAPI.listGoldenQuestions(selectedGoldenSetID)
      setGoldenRun({ completed: 0, total: questions.length, results: [] })
      const results: GoldenQuestionReplay[] = []
      for (let index = 0; index < questions.length; index++) {
        const replay = await semanticChatAPI.replayGoldenQuestion(questions[index].id)
        results.push(replay)
        setGoldenRun({ completed: index + 1, total: questions.length, results: [...results] })
      }
    } catch (cause) {
      setGoldenError(cause instanceof Error ? cause.message : '黄金问题回放失败')
    }
  }

  const goldenPassed = goldenRun?.results.filter(result => result.status === 'PASSED').length ?? 0
  const goldenRate = goldenRun?.completed
    ? Math.round((goldenPassed / goldenRun.completed) * 100)
    : undefined

  return (
    <AppShell
      title="智能问答"
      eyebrow="可信分析"
      actions={<button className="quiet-button" type="button" onClick={startConversation}><Plus size={17} />新建对话</button>}
    >
      <section className="semantic-chat-workspace">
        <aside className="semantic-chat-sessions">
          <header><div><span className="eyebrow">对话记录</span><h2>分析会话</h2></div><button type="button" aria-label="新建对话" onClick={startConversation}><Plus size={17} /></button></header>
          <nav aria-label="智能问答会话">
            {sessions.map(session => (
              <button
                type="button"
                key={session.id}
                className={session.id === activeSession.id ? 'active' : ''}
                onClick={() => { setActiveSessionID(session.id); setSelectedMessageID('') }}
              >
                <ChatCenteredDots size={17} />
                <span><strong>{session.title}</strong><small>{session.messages.filter(message => message.role === 'USER').length} 轮对话</small></span>
              </button>
            ))}
          </nav>
          <footer>
            <span className={`semantic-chat-graph-state ${graphStatus?.status === 'READY' ? 'ready' : ''}`}><Graph size={16} /><strong>{graphStatus?.status === 'READY' ? '语义图已就绪' : graphError || '正在检查语义图'}</strong></span>
            {graphStatus?.currentGeneration != null && <small>Generation {graphStatus.currentGeneration} · 水位 {graphStatus.appliedEventVersion}/{graphStatus.requestedEventVersion}</small>}
          </footer>
        </aside>

        <section className="semantic-chat-main">
          <header>
            <div><span className="eyebrow">多轮数据问答</span><h2>{activeSession.title}</h2></div>
            <span className="semantic-chat-context-badge"><ClockCounterClockwise size={15} />上下文连续</span>
          </header>
          <div className="semantic-chat-messages" aria-live="polite">
            {activeSession.messages.length === 0 && (
              <div className="semantic-chat-welcome">
                <span><Sparkle size={24} weight="fill" /></span>
                <h3>用业务语言直接询问数据</h3>
                <p>每个答案都会经过语义图、版本、权限与物化结果校验；证据不足时系统会明确拒答。</p>
                <div>{suggestionQuestions.map(item => <button type="button" key={item} onClick={() => void submitQuestion(item)}>{item}</button>)}</div>
              </div>
            )}
            {activeSession.messages.map(message => message.role === 'USER'
              ? <article className="semantic-chat-message user" key={message.id}><div>{message.content}</div><time>{new Date(message.createdAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</time></article>
              : <article
                className={`semantic-chat-message assistant ${message.errorCode || (message.plan && message.plan.status !== 'EXECUTED' && message.plan.status !== 'READY') ? 'blocked' : ''} ${selectedMessage?.id === message.id ? 'selected' : ''}`}
                key={message.id}
                onClick={() => setSelectedMessageID(message.id)}
              >
                <span className="semantic-chat-avatar"><Sparkle size={16} weight="fill" /></span>
                <div className="semantic-chat-answer">
                  <header><strong>智能分析助手</strong>{message.pending && <i>处理中</i>}{message.plan && <em className={message.plan.status.toLowerCase()}>{statusLabel(message.plan.status)}</em>}</header>
                  <p>{message.content}</p>
                  {message.execution && message.execution.result.rows.length > 0 && (
                    <div className="semantic-chat-result">
                      <table>
                        <thead><tr>{message.execution.result.columns.map(column => <th key={column}>{column}</th>)}</tr></thead>
                        <tbody>{message.execution.result.rows.slice(0, 10).map((row, rowIndex) => <tr key={rowIndex}>{message.execution?.result.columns.map((column, columnIndex) => <td key={`${column}-${columnIndex}`}>{formatValue(row[columnIndex])}</td>)}</tr>)}</tbody>
                      </table>
                    </div>
                  )}
                  <footer>
                    <span>{message.execution ? `${message.execution.result.rowCount} 行 · ${message.execution.result.durationMs} ms` : message.plan ? `${Math.round(message.plan.confidence * 100)}% 检索置信度` : message.errorCode}</span>
                    {message.execution && <span><ShieldCheck size={14} />{message.execution.evidence.lineage.length} 项可信证据</span>}
                  </footer>
                </div>
              </article>
            )}
            <div ref={messageEndRef} />
          </div>
          <form className="semantic-chat-composer" onSubmit={onSubmit}>
            <textarea
              aria-label="输入分析问题"
              value={question}
              onChange={event => setQuestion(event.target.value)}
              onKeyDown={onComposerKeyDown}
              placeholder="继续追问，例如：那上个月呢？或再按渠道拆分"
              rows={2}
              disabled={isPending}
            />
            <button className="primary-button" type="submit" aria-label="发送问题" disabled={!question.trim() || isPending}><PaperPlaneTilt size={18} weight="fill" /></button>
            <small>Enter 发送 · Shift + Enter 换行 · 追问继承上一轮已验证语义槽位</small>
          </form>
        </section>

        <aside className="semantic-chat-quality">
          <header><span className="eyebrow">答案验证</span><h2>效果与准确性</h2><p>区分检索置信度、执行证据和人工评价，不用单一分数掩盖风险。</p></header>
          <section className="semantic-chat-score-grid" aria-label="本会话质量统计">
            <div><span>回答成功率</span><strong>{quality.successRate == null ? '—' : `${quality.successRate}%`}</strong></div>
            <div><span>证据完整率</span><strong>{quality.evidenceRate == null ? '—' : `${quality.evidenceRate}%`}</strong></div>
            <div><span>人工准确率</span><strong>{quality.manualRate == null ? '—' : `${quality.manualRate}%`}</strong></div>
          </section>

          <section className="semantic-chat-current-quality">
            <header><strong>当前答案</strong><span>{selectedMessage?.errorCode ? '可信拒答' : statusLabel(selectedMessage?.plan?.status)}</span></header>
            {!selectedMessage ? <p className="semantic-chat-empty-quality">发送问题后，这里会展示逐项验证结果。</p> : <>
              <div className="semantic-chat-confidence"><span>检索路径置信度</span><strong>{selectedMessage.plan ? `${Math.round(selectedMessage.plan.confidence * 100)}%` : '—'}</strong></div>
              <ul>
                <li className={selectedMessage.execution ? 'pass' : ''}><Database size={16} /><span><strong>查询执行</strong><small>{selectedMessage.execution ? '受控查询已完成' : '未执行或执行失败'}</small></span>{selectedMessage.execution ? <CheckCircle /> : <WarningCircle />}</li>
                <li className={selectedMessage.execution?.evidence.executionRevalidated ? 'pass' : ''}><ShieldCheck size={16} /><span><strong>权限与版本</strong><small>{selectedMessage.execution ? '运行时已重新校验' : '等待可执行计划'}</small></span>{selectedMessage.execution?.evidence.executionRevalidated ? <CheckCircle /> : <WarningCircle />}</li>
                <li className={selectedMessage.execution?.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE' ? 'pass' : ''}><Graph size={16} /><span><strong>维度兼容</strong><small>{selectedMessage.execution?.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE' ? '已验证且无扇出风险' : '尚未通过验证'}</small></span>{selectedMessage.execution?.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE' ? <CheckCircle /> : <WarningCircle />}</li>
              </ul>
              {selectedMessage.plan?.evidence.length ? <details><summary>查看证据链（{selectedMessage.plan.evidence.length}）</summary><ol>{selectedMessage.plan.evidence.map(item => <li key={`${item.index}-${item.nodeKey}`}><span>{item.label}</span><small>{item.subjectType} · {item.authority} · {Math.round(item.confidence * 100)}%</small></li>)}</ol></details> : null}
              <div className="semantic-chat-feedback"><span>这个答案准确吗？</span><div><button className={selectedMessage.feedback === 'ACCURATE' ? 'active positive' : ''} type="button" onClick={() => rateSelected('ACCURATE')}><ThumbsUp size={15} />准确</button><button className={selectedMessage.feedback === 'INACCURATE' ? 'active negative' : ''} type="button" onClick={() => rateSelected('INACCURATE')}><ThumbsDown size={15} />不准确</button></div></div>
            </>}
          </section>

          <section className="semantic-chat-golden">
            <header><div><TestTube size={17} /><strong>黄金问题回放</strong></div>{goldenRate != null && <span>{goldenRate}% 通过</span>}</header>
            {goldenSets.length === 0 ? <p>尚未配置黄金问题集。可在语义治理流程中建立版本化测试门禁。</p> : <>
              <select aria-label="黄金问题集" value={selectedGoldenSetID} onChange={event => { setSelectedGoldenSetID(event.target.value); setGoldenRun(undefined) }}>
                {goldenSets.map(set => <option key={set.id} value={set.id}>{set.name} · V{set.version} · {set.status}</option>)}
              </select>
              {goldenRun && <div className="semantic-chat-golden-progress"><span>已完成 {goldenRun.completed}/{goldenRun.total}</span><progress value={goldenRun.completed} max={Math.max(1, goldenRun.total)} /></div>}
              <button className="quiet-button full" type="button" onClick={() => void runGoldenQuestions()} disabled={!selectedGoldenSetID || Boolean(goldenRun && goldenRun.completed < goldenRun.total)}><ChartLineUp size={16} />运行测试集</button>
            </>}
            {goldenError && <small className="semantic-chat-golden-error">{goldenError}</small>}
          </section>
        </aside>
      </section>
    </AppShell>
  )
}
