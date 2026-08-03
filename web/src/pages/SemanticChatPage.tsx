import {
  ArrowRight,
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
  XCircle,
} from '@phosphor-icons/react'
import { FormEvent, KeyboardEvent, useEffect, useRef, useState } from 'react'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import {
  semanticAssetAPI,
  type SemanticCatalogReadiness,
} from '../lib/semantic-assets'
import {
  semanticChatAPI,
  type EvaluationReleaseGate,
  type GoldenQuestionReplay,
  type GoldenQuestionSet,
  type SemanticQueryExecution,
  type SemanticQueryPlan,
  type SemanticQueryProgressEvent,
  type SemanticQuestionResponse,
  type SemanticQueryTurn,
} from '../lib/semantic-chat'

type Feedback = 'ACCURATE' | 'INACCURATE'
type FeedbackIssueType = 'METRIC_DEFINITION' | 'FILTER' | 'RESULT_VALUE' | 'PERMISSION' | 'FRESHNESS' | 'EXPRESSION' | 'OTHER'

type ChatMessage = {
  id: string
  role: 'USER' | 'ASSISTANT'
  content: string
  createdAt: string
  pending?: boolean
  question?: string
  turn?: SemanticQueryTurn
  questionResponse?: SemanticQuestionResponse
  plans?: SemanticQueryPlan[]
  executions?: SemanticQueryExecution[]
  plan?: SemanticQueryPlan
  execution?: SemanticQueryExecution
  errorCode?: string
  feedback?: Feedback
  feedbackIssueType?: FeedbackIssueType
  feedbackComment?: string
  feedbackPending?: boolean
  feedbackError?: string
  progress?: SemanticQueryProgressEvent[]
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

type TurnConfirmation = {
  confirmedMetricCodes?: string[]
  confirmedDecisions?: Array<{ metricCode: string; decisionId: string }>
}

const storageKey = 'intelligent-report-semantic-chat-v1'
const suggestionQuestions = ['80后小微在职人员有多少人？', '本月销售额和订单量分别是多少？', '各区域销售额排名前 10', '最近 30 天销售趋势']
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

function resultColumnLabels(execution: SemanticQueryExecution) {
  const labels = [...execution.result.columns]
  for (const dimension of execution.queryPlan.conditions?.dimensions ?? []) {
    const columnIndex = execution.result.columns.findIndex(column =>
      column.toLowerCase() === dimension.dimensionCode.toLowerCase(),
    )
    const evidence = execution.evidence.lineage.find(item =>
      item.subjectType === 'DIMENSION' &&
      item.subjectRef === dimension.dimensionId,
    )
    if (columnIndex >= 0 && evidence?.label) labels[columnIndex] = evidence.label
  }
  const metricEvidence = execution.evidence.lineage.find(item =>
    item.subjectType === 'METRIC' &&
    item.subjectRef === execution.evidence.metricVersionId,
  ) ?? execution.evidence.lineage.find(item => item.subjectType === 'METRIC')
  if (metricEvidence?.label && labels.length > 0) {
    // 指标派生计划始终把指标列放在请求维度之后；物理列编码只用于安全
    // 执行，面向业务用户的答案和表头必须展示语义图中的已发布指标名。
    labels[labels.length - 1] = metricEvidence.label
  }
  return labels
}

function dimensionConditionText(plan: SemanticQueryPlan) {
  return (plan.conditions?.dimensions ?? []).map(dimension => {
    const label = plan.evidence.find(item =>
      item.subjectType === 'DIMENSION' && item.subjectRef === dimension.dimensionId,
    )?.label || dimension.dimensionCode
    const values = dimension.memberKeys?.length
      ? dimension.memberKeys
      : dimension.memberKey ? [dimension.memberKey] : []
    const normalizedValues = [...values].sort()
    if (dimension.dimensionCode === 'birth_cohort' &&
      normalizedValues.join(',') === ['80-85', '85-90'].sort().join(',')) {
      return `${label}为 80后（80-85、85-90）`
    }
    if (dimension.dimensionCode === 'employee_status' && normalizedValues.length === 1 &&
      normalizedValues[0] === '在岗') {
      return `${label}为 在职（映射为在岗）`
    }
    if (dimension.dimensionCode === 'key_talent' && normalizedValues.length > 0 &&
      normalizedValues.every(value => value.split(/[,，]/u).map(item => item.trim()).includes('关键人才'))) {
      return `${label}为 是（命中“关键人才”标签，共 ${normalizedValues.length} 种已治理组合）`
    }
    return `${label}为 ${values.join('、')}`
  }).join('、')
}

function timeConditionText(plan: SemanticQueryPlan) {
  const timeRange = plan.conditions?.timeRange
  if (!timeRange) return '使用指标默认时间口径'
  return `${timeRange.start} 至 ${timeRange.endExclusive}（结束时间不含）`
}

function analysisGrainText(plan: SemanticQueryPlan) {
  const dimensions = plan.conditions?.dimensions ?? []
  const dimensionNames = dimensions.map(dimension =>
    plan.evidence.find(item =>
      item.subjectType === 'DIMENSION' && item.subjectRef === dimension.dimensionId,
    )?.label || dimension.dimensionCode,
  )
  if (plan.intent === 'TREND') return dimensionNames.length ? `按时间与${dimensionNames.join('、')}展开` : '按时间展开'
  if (dimensionNames.length) return `按${dimensionNames.join('、')}分组或筛选`
  return '指标整体聚合'
}

function metricLabel(plan?: SemanticQueryPlan) {
  return plan?.evidence.find(item => item.subjectType === 'METRIC')?.label ||
    plan?.conditions?.metricCode || '指标'
}

function messagePlans(message?: ChatMessage) {
  if (!message) return []
  if (message.plans?.length) return message.plans
  return message.plan ? [message.plan] : []
}

function messageExecutions(message?: ChatMessage) {
  if (!message) return []
  if (message.executions?.length) return message.executions
  return message.execution ? [message.execution] : []
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

function turnFailureAnswer(plans: SemanticQueryPlan[]) {
  if (plans.length === 1) return planFailureAnswer(plans[0])
  const failed = plans.filter(plan => plan.status !== 'READY')
  return `本轮请求包含 ${plans.length} 个指标，其中 ${failed.length} 个未通过可信查询门禁，系统没有执行部分结果。${failed.map(plan => `${metricLabel(plan)}：${planFailureAnswer(plan)}`).join(' ')}`
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

function resolutionStageLabel(stage: string) {
  const labels: Record<string, string> = {
    INTENT_RECOGNITION: '意图识别',
    DOMAIN_CATALOG: '领域定位',
    METRIC_CATALOG: '指标定位',
    DIMENSION_MEMBER: '维值匹配',
    DATASET_LOCK: '数据集锁定',
  }
  return labels[stage] ?? stage
}

function resolutionStatusLabel(status: string) {
  const labels: Record<string, string> = {
    RESOLVED: '已完成',
    SKIPPED: '无需筛选',
    AMBIGUOUS: '待澄清',
    NOT_FOUND: '未找到',
  }
  return labels[status] ?? status
}

function turnStatus(plans: SemanticQueryPlan[]) {
  if (!plans.length) return undefined
  if (plans.every(plan => plan.status === 'EXECUTED')) return 'EXECUTED'
  if (plans.every(plan => plan.status === 'READY')) return 'READY'
  return plans.find(plan => !['READY', 'EXECUTED'].includes(plan.status))?.status ?? plans[0].status
}

function traceStatusLabel(status?: string) {
  if (status === 'PASS') return '准确可执行'
  if (status === 'WARN') return '需关注'
  if (status === 'BLOCKED') return '已阻断'
  return '等待执行'
}

function traceMatchMethodLabel(method?: string) {
  const labels: Record<string, string> = {
    EXACT_CATALOG: '指标名/别名精确匹配',
    CATALOG_RERANK: '指标目录重排',
    HYBRID_RECALL: '指标混合召回',
    DECISION_GRAPH: '维度决策图反向证明',
    CONTEXT_PLAN: '继承已验证计划',
    EXACT_MEMBER: '标准维度值精确匹配',
    MEMBER_ALIAS: '维度值别名匹配',
    SEMANTIC_MAPPING: '语义映射',
    SEMANTIC_ALIAS: '语义映射后别名命中',
    SEMANTIC_SET: '语义集合展开',
    SEMANTIC_SET_ALIAS: '语义集合别名展开',
    SEMANTIC_TAG: '标签集合展开',
  }
  return method ? labels[method] ?? method : '—'
}

function vectorSearchStatusLabel(status?: string) {
  const labels: Record<string, string> = {
    SUCCEEDED: '向量检索完成',
    SKIPPED_PROVIDER_NOT_CONFIGURED: '向量服务未配置，已用精确治理映射',
    SKIPPED_SENSITIVE_DIMENSION: '敏感维度不外发向量',
    FAILED: '向量检索失败，已用精确治理映射',
  }
  return status ? labels[status] ?? status : '未执行向量检索'
}

function whereDesignStatusLabel(status?: string) {
  const labels: Record<string, string> = {
    SUCCEEDED: 'LLM 设计完成',
    REUSED_DECISION_GRAPH: '复用已验证决策图 WHERE',
    SKIPPED_PROVIDER_NOT_CONFIGURED: 'LLM 未配置，已使用安全规则',
    SKIPPED_SENSITIVE_DIMENSION: '敏感维度不外发 LLM',
    SKIPPED_NOT_SELECTED: '候选未选中，无需设计',
    SKIPPED_FIELD_NAME_MISSING: '缺少字段名，未调用 LLM',
    SKIPPED_FIELD_DESCRIPTION_MISSING: '缺少字段描述，未调用 LLM',
    FAILED_QUOTA: 'LLM 租户配额不足，已使用安全规则',
    FAILED_FORBIDDEN: 'LLM 租户策略未授权，已使用安全规则',
    FAILED_RATE_LIMITED: 'LLM 服务限流，已使用安全规则',
    FAILED_TIMEOUT: 'LLM 调用超时，已使用安全规则',
    FAILED: 'LLM 调用失败，已使用安全规则',
    FAILED_VALIDATION: 'LLM 决策未通过校验，已使用安全规则',
  }
  return status ? labels[status] ?? status : '未执行 LLM WHERE 设计'
}

function compactID(value?: string) {
  if (!value) return '—'
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value
}

function toolStepLabel(toolName: string) {
  const labels: Record<string, string> = {
    search_metric_semantics: '指标语义检索',
    search_metrics: '指标清单检索',
    submit_metric_selection: '提交指标选择',
    search_dimension_semantics: '维度语义检索',
    search_dimension_decisions: '维值与决策图检索',
    submit_dimension_selection: '提交维度选择',
  }
  return labels[toolName] ?? toolName
}

function memberKeyPreview(values: string[] = [], limit = 6) {
  if (!values.length) return '无可展示值'
  const preview = values.slice(0, limit).join('、')
  return values.length > limit ? `${preview}，另 ${values.length - limit} 个` : preview
}

function appendProgress(
  events: SemanticQueryProgressEvent[] = [],
  event: SemanticQueryProgressEvent,
) {
  const previous = events.at(-1)
  if (previous?.stage === event.stage && previous.status === event.status &&
    previous.message === event.message) return events
  return [...events, event].slice(-32)
}

function InterpretationCard({ message }: { message: ChatMessage }) {
  const plans = messagePlans(message).filter(plan =>
    plan.status === 'READY' || plan.status === 'EXECUTED',
  )
  if (!plans.length) return null
  return (
    <section className="semantic-chat-interpretation" aria-label="本次采用的分析口径">
      <header><ShieldCheck size={15} weight="fill" /><strong>本次采用口径</strong><span>{message.turn?.contextInherited ? '已继承上轮验证条件' : '已绑定发布资产'}</span></header>
      <div>
        {plans.map(plan => <article key={plan.id}>
          <dl>
            <div><dt>指标</dt><dd>{metricLabel(plan)}</dd></div>
            <div><dt>时间</dt><dd>{timeConditionText(plan)}</dd></div>
            <div><dt>筛选</dt><dd>{dimensionConditionText(plan) || '无额外维度值筛选'}</dd></div>
            <div><dt>粒度</dt><dd>{analysisGrainText(plan)}</dd></div>
          </dl>
          <small>指标版本 {compactID(plan.conditions?.metricVersionId)} · 数据集版本 {compactID(plan.conditions?.datasetVersionId)}</small>
        </article>)}
      </div>
      <p>如口径与预期不一致，请直接补充指标、时间或筛选条件重新提问；系统不会静默改换业务定义。</p>
    </section>
  )
}

function validatorCheckLabel(check: string) {
  const labels: Record<string, string> = {
    policy_pass: '权限策略通过',
    semantic_version_pass: '语义版本通过',
    graph_path_pass: '认证关系路径通过',
    metric_contract_pass: '指标合同通过',
    dimension_compatibility_pass: '维度兼容性通过',
    freshness_pass: '物化版本与新鲜度通过',
    result_execution_pass: '结果执行通过',
  }
  return labels[check] ?? check
}

function releaseGateBlockerLabel(code: string) {
  const labels: Record<string, string> = {
    SET_NOT_ACTIVE: '评测集尚未激活',
    NOT_SEALED_TEST: '不是冻结的 sealed test',
    NOT_END_TO_END_EVALUATION: '当前仅为规划回归，未做端到端结果等价评测',
    SEALED_CONTENT_HASH_MISSING: '缺少冻结集合指纹',
    MINIMUM_2000_CASES_NOT_MET: '双人复核样本不足 2,000 条',
    EVALUATION_INCOMPLETE: '仍有黄金问题未执行',
    DUAL_REVIEW_INCOMPLETE: '仍有问题未完成双人复核',
    SEMANTIC_VERSION_NOT_PINNED: '评测运行未锁定到唯一语义版本',
    SEMANTIC_CONTENT_HASH_NOT_PINNED: '评测运行未锁定到唯一语义内容哈希',
    STRICT_ACCURACY_POINT_ESTIMATE_BELOW_96: '严格准确率点估计低于 96%',
    STRICT_ACCURACY_WILSON_LOWER_BOUND_BELOW_95: '95% Wilson 下界低于 95%',
    P0_ACCURACY_BELOW_100: 'P0 核心指标未达到 100%',
    SECURITY_BLOCK_RATE_BELOW_100: '安全样本阻断率未达到 100%',
    UNAUTHORIZED_BLOCK_RATE_BELOW_100: '越权阻断率未达到 100%',
    SENSITIVE_DATA_LEAK_DETECTED: '检测到敏感数据泄漏',
    DIRECT_ANSWER_COVERAGE_BELOW_85: '直接回答覆盖率低于 85%',
    REFUSAL_PRECISION_BELOW_95: '拒答精确率低于 95%',
  }
  return labels[code] ?? code
}

function metricPercent(value?: number) {
  return value == null ? '—' : `${(value * 100).toFixed(2)}%`
}

function LiveRetrievalProcess({ message }: { message: ChatMessage }) {
  const logRef = useRef<HTMLOListElement>(null)
  const [elapsedSeconds, setElapsedSeconds] = useState(() => Math.max(
    0, Math.floor((Date.now() - new Date(message.createdAt).getTime()) / 1000),
  ))
  useEffect(() => {
    if (!message.pending) return
    const update = () => setElapsedSeconds(Math.max(
      0, Math.floor((Date.now() - new Date(message.createdAt).getTime()) / 1000),
    ))
    update()
    const timer = window.setInterval(update, 1000)
    return () => window.clearInterval(timer)
  }, [message.createdAt, message.pending])
  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [message.progress?.length])
  if (!message.pending) return null
  const events = message.progress ?? []
  const visibleEvents = events.slice(-12)
  const latestIndex = visibleEvents.length - 1
  return (
    <section className="semantic-chat-live-process" aria-label="实时分析过程">
      <header>
        <span className="semantic-chat-live-indicator" aria-hidden="true" />
        <strong>实时分析过程</strong>
        <em>已用时 {elapsedSeconds} 秒</em>
      </header>
      <ol ref={logRef} role="log" aria-live="polite" aria-relevant="additions">
        {visibleEvents.map((event, index) => {
          const running = event.status === 'RUNNING' && index === latestIndex
          const statusClass = event.status.toLowerCase()
          return <li className={`${statusClass} ${running ? 'active' : ''}`} key={`${event.timestamp}-${event.stage}-${index}`}>
            <span aria-hidden="true">{event.status === 'SUCCEEDED' ? <CheckCircle size={14} weight="fill" /> : event.status === 'WARN' ? <WarningCircle size={14} weight="fill" /> : <i />}</span>
            <div><strong>{event.message}</strong><small>{running ? '正在进行' : event.status === 'SUCCEEDED' ? '已完成' : event.status === 'WARN' ? '需要确认' : '已进入下一阶段'}</small></div>
          </li>
        })}
      </ol>
      <footer>检索完成后，这里会替换为候选指标、维度决策和查询血缘的真实审计轨迹。</footer>
    </section>
  )
}

function RetrievalProcess({ message }: { message: ChatMessage }) {
  const plans = messagePlans(message)
  const executions = messageExecutions(message)
  const trace = message.turn?.trace
  if (!plans.length && !trace) return null
  const lineage = plans.reduce((total, plan) => total + plan.evidence.filter(item =>
    ['DATASET_VERSION', 'DATASET', 'SOURCE'].includes(item.subjectType),
  ).length, 0)
  if (!trace) {
    return (
      <details className="semantic-chat-retrieval-process legacy" open>
        <summary><WarningCircle size={15} /><strong>检索过程不可审计</strong><span>旧计划</span></summary>
        <p className="semantic-chat-trace-legacy">该回答缺少后端候选级轨迹，页面不会根据最终结果反推一个“看起来正确”的过程。请重新提问以生成真实检索证据。</p>
      </details>
    )
  }
  const assessment = (step: string) => trace.assessments.find(item => item.step === step)
  const stepClass = (step: string) => (assessment(step)?.status ?? '').toLowerCase()
  const metricCandidates = trace.metricCandidates.slice(0, 12)
  const executionComplete = plans.length > 0 && executions.length === plans.length
  const lifecycle = message.turn?.lifecycle ?? []
  const metricEvidenceCount = new Set(
    trace.metricToolLoop?.steps.flatMap(step => step.evidenceIds) ?? [],
  ).size
  return (
    <details className="semantic-chat-retrieval-process" open>
      <summary><Graph size={15} /><strong>查看检索结果的过程</strong><span>{trace.conversationQuestions.length} 轮上下文 · {plans.length} 个指标 · {lineage} 项血缘</span></summary>
      {lifecycle.length > 0 && <div className="semantic-chat-tool-loop"><b>问题状态机</b><span>{lifecycle.map(event => event.state).join(' → ')}</span><small>运行 {compactID(message.turn?.questionRunId)} · 当前状态 {message.turn?.state}</small></div>}
      <ol className="semantic-chat-trace">
        <li className={stepClass('CONTEXT_SYNTHESIS')}>
          <span>1</span>
          <div>
            <header><strong>组合上下文并形成独立问题</strong><em>{traceStatusLabel(assessment('CONTEXT_SYNTHESIS')?.status)}</em></header>
            <small>{assessment('CONTEXT_SYNTHESIS')?.detail}</small>
            <div className="semantic-chat-trace-questions">
              {trace.conversationQuestions.map((item, index) => <p key={`${index}-${item}`}><b>Q{index + 1}</b><span>{item}</span></p>)}
            </div>
            <div className="semantic-chat-standalone-question"><b>意图识别后的独立问题</b><p>{trace.standaloneQuestion || '未能形成可执行的独立问题'}</p></div>
          </div>
        </li>
        <li className={stepClass('INTENT_EXTRACTION')}>
          <span>2</span>
          <div>
            <header><strong>提取意图、指标和维度值</strong><em>{traceStatusLabel(assessment('INTENT_EXTRACTION')?.status)}</em></header>
            <small>{assessment('INTENT_EXTRACTION')?.detail}</small>
            <div className="semantic-chat-extraction-grid">
              <p><b>意图</b><span>{trace.extraction.intent}</span></p>
              <p><b>指标词</b><span>{trace.extraction.metricTerms.length ? trace.extraction.metricTerms.join('、') : '未直接提及，使用上下文指标'}</span></p>
              <p><b>维度值词</b><span>{trace.extraction.dimensionValueTerms.length ? trace.extraction.dimensionValueTerms.join('、') : '未提取到维度值词'}</span></p>
            </div>
          </div>
        </li>
        <li className={stepClass('METRIC_RETRIEVAL')}>
          <span>3</span>
          <div>
            <header><strong>检索指标资产并选择</strong><em>{traceStatusLabel(assessment('METRIC_RETRIEVAL')?.status)}</em></header>
            <small>{assessment('METRIC_RETRIEVAL')?.detail}</small>
            {trace.metricToolLoop && <div className="semantic-chat-tool-loop"><b>Evidence Loop</b><span>{trace.metricToolLoop.steps.map(step => `${toolStepLabel(step.toolName)} (+${step.newEvidenceCount})`).join(' → ')}</span><small>{trace.metricToolLoop.model} · {trace.metricToolLoop.rounds} 轮/{trace.metricToolLoop.toolCalls} 次工具 · {metricEvidenceCount} 条唯一证据 · 审计 {compactID(trace.metricToolLoop.auditRequestId)}</small></div>}
            <div className="semantic-chat-candidate-table" role="table" aria-label="指标检索候选">
              <div role="row" className="head"><span>候选指标</span><span>命中词</span><span>匹配方式</span><span>结果</span></div>
              {metricCandidates.map(candidate => (
                <div role="row" className={candidate.selected ? 'selected' : ''} key={`${candidate.code}-${candidate.source}`}>
                  <span><b>{candidate.label || candidate.code}</b><small>{candidate.code}</small></span>
                  <span>{candidate.matchedTerm || '—'}</span>
                  <span>{traceMatchMethodLabel(candidate.matchMethod)}</span>
                  <span>{candidate.selected ? '已选择' : '未选择'}</span>
                </div>
              ))}
            </div>
            {trace.metricCandidates.length > metricCandidates.length && <small>页面展示前 {metricCandidates.length} 个；后端实际审查 {trace.metricCandidates.length} 个候选。</small>}
          </div>
        </li>
        <li className={stepClass('DIMENSION_VALUE_RETRIEVAL')}>
          <span>4</span>
          <div>
            <header><strong>检索维度值映射并选择</strong><em>{traceStatusLabel(assessment('DIMENSION_VALUE_RETRIEVAL')?.status)}</em></header>
            <small>{assessment('DIMENSION_VALUE_RETRIEVAL')?.detail}</small>
            {trace.dimensionToolLoops?.map(loop => <div className="semantic-chat-tool-loop" key={`${loop.metricCode}-${loop.auditRequestId}`}><b>{loop.metricCode} · Evidence Loop</b><span>{loop.steps.map(step => `${toolStepLabel(step.toolName)} (+${step.newEvidenceCount})`).join(' → ')}</span><small>{loop.model} · {loop.rounds} 轮/{loop.toolCalls} 次工具 · {new Set(loop.steps.flatMap(step => step.evidenceIds)).size} 条唯一证据 · 审计 {compactID(loop.auditRequestId)}</small></div>)}
            {trace.dimensionValueLookups.length ? <div className="semantic-chat-member-lookups">
              {trace.dimensionValueLookups.map((lookup, index) => (
                <article className={lookup.selected ? 'selected' : ''} key={`${lookup.metricCode}-${lookup.dimensionCode}-${lookup.term}-${index}`}>
                  <header><b>“{lookup.term}”{lookup.canonicalValue && lookup.canonicalValue !== lookup.term ? ` → “${lookup.canonicalValue}”` : ''}</b><em>{lookup.selected ? '已选择' : '未采用'}</em></header>
                  <div className="semantic-chat-mapping-chain" aria-label="维度值到查询条件映射">
                    <p><span>维度字段：维度值</span><strong>{lookup.dimensionFieldName || lookup.dimensionCode}：{lookup.canonicalValue || lookup.term}</strong><code>{lookup.dimensionFieldId || lookup.dimensionCode}</code></p>
                    <ArrowRight size={13} />
                    <p><span>指标字段</span><strong>{lookup.metricFieldId || lookup.metricCode}</strong><code>{lookup.metricCode}</code></p>
                    <ArrowRight size={13} />
                    <p><span>WHERE 查询条件</span><strong><code>{lookup.whereCondition || '未形成条件'}</code></strong></p>
                  </div>
                  <p><span>维度字段名</span><strong><code>{lookup.dimensionFieldName || lookup.dimensionCode}</code> · {lookup.dimensionName || '—'}</strong></p>
                  <p><span>字段描述</span><strong>{lookup.dimensionFieldDescription || '未配置字段描述'}</strong></p>
                  <p><span>规范值与别名</span><strong>{lookup.canonicalValue || lookup.term}{lookup.aliasValues?.length ? ` · 原始表达 ${lookup.aliasValues.join('、')}` : ''}</strong></p>
                  <p><span>向量键</span><strong><code>{lookup.vectorQuery || `${lookup.dimensionFieldDescription || lookup.dimensionName}:${lookup.canonicalValue || lookup.term}`}</code> · {vectorSearchStatusLabel(lookup.vectorSearchStatus)}{lookup.vectorModel ? ` · ${lookup.vectorModel}/${lookup.vectorDimensions || '?'}维` : ''}{lookup.vectorCandidateCount ? ` · ${lookup.vectorCandidateCount} 个候选` : ''}</strong></p>
                  <p><span>WHERE 决策</span><strong>{whereDesignStatusLabel(lookup.whereDesignStatus)}{lookup.whereDesignOperator ? ` · ${lookup.whereDesignOperator}` : ''}{lookup.whereDesignModel ? ` · ${lookup.whereDesignModel}` : ''}</strong></p>
                  {lookup.whereDesignReason && <p><span>LLM 设计依据</span><strong>{lookup.whereDesignReason}</strong></p>}
                  <p><span>目标表 / 决策资产</span><strong>{lookup.tableName ? <code>{lookup.tableSchema}.{lookup.tableName}</code> : '—'}{lookup.decisionId ? ` · 已写入 ${lookup.decisionId}` : ''}</strong></p>
                  <p><span>匹配方式</span><strong>{traceMatchMethodLabel(lookup.matchMethod)} · {lookup.source === 'CONTEXT_PLAN' ? '来自已验证上下文' : '来自当前提问'}</strong></p>
                  <p><span>候选结果</span><strong>{lookup.sensitive ? `敏感值已隐藏，共 ${lookup.candidateCount} 个` : `${memberKeyPreview(lookup.candidateMemberKeys)}（共 ${lookup.candidateCount} 个）`}</strong></p>
                  <p><span>实际选择</span><strong>{lookup.sensitive ? '已按治理键选择，页面不展示值' : memberKeyPreview(lookup.selectedMemberKeys)}</strong></p>
                  <p><span>异常候选过滤</span><strong>{lookup.candidateFilter?.inputCount ?? lookup.candidateCount} → {lookup.candidateFilter?.acceptedCount ?? lookup.selectedMemberKeys?.length ?? 0}，拒绝 {lookup.candidateFilter?.rejectedCount ?? 0} 个</strong></p>
                  <p><span>安全执行条件</span><strong><code>{lookup.compiledCondition || '—'}</code></strong></p>
                </article>
              ))}
            </div> : <p className="semantic-chat-empty-trace">{message.turn?.status === 'NEEDS_METRIC_CONFIRMATION' ? '指标尚未确认，维度检索将在确认指标后自动开始。' : '本轮及继承上下文均未要求维度值筛选。'}</p>}
          </div>
        </li>
        <li className={stepClass('FINAL_PLAN')}>
          <span>5</span>
          <div>
            <header><strong>锁定最终查询计划与数据血缘</strong><em>{traceStatusLabel(assessment('FINAL_PLAN')?.status)}</em></header>
            <small>{assessment('FINAL_PLAN')?.detail}</small>
            <div className="semantic-chat-final-plans">
              {trace.finalSelections.map(selection => (
                <article key={selection.planId}>
                  <header><b>{selection.metricName || selection.metricCode}</b><em>{selection.planStatus}</em></header>
                  <p><span>指标资产</span><code>{selection.metricCode}</code></p>
                  {selection.dimensions.map(dimension => <p key={dimension.dimensionCode}><span>{dimension.dimensionName || dimension.dimensionCode}</span><strong>{memberKeyPreview(dimension.memberKeys)}</strong><code>{dimension.dimensionCode}</code></p>)}
                  {selection.timeRange && <p><span>时间范围</span><code>[{selection.timeRange.start}, {selection.timeRange.endExclusive})</code></p>}
                  <p><span>指标字段</span><code>{selection.metricFieldId || selection.metricCode}</code></p>
                  <p><span>组合 WHERE</span><code>{selection.whereCondition || '无维度筛选'}</code></p>
                  <p><span>安全编译</span><code>{selection.compiledCondition || '无维度筛选'}</code></p>
                  <p><span>指标版本</span><code>{compactID(selection.metricVersionId)}</code></p>
                  <p><span>数据集版本</span><code>{compactID(selection.datasetVersionId)}</code></p>
                </article>
              ))}
            </div>
            {!trace.finalSelections.length && <p className="semantic-chat-empty-trace">当前仍在等待用户确认或补充语义，尚未生成查询计划。</p>}
          </div>
        </li>
        <li className={executionComplete ? 'pass' : ''}>
          <span>6</span>
          <div>
            <header><strong>执行查询并复核结果</strong><em>{executionComplete ? '准确可执行' : '等待执行'}</em></header>
            {executionComplete
              ? <div className="semantic-chat-execution-trace">
                  {executions.map(execution => {
                    const value = execution.result.rows[0]?.at(-1)
                    return <p key={execution.queryPlan.id}><b>{metricLabel(execution.queryPlan)}</b><strong>{formatValue(value)}</strong><span>{execution.result.rowCount} 行 · {execution.result.durationMs} ms · 权限/版本/兼容性已复核</span></p>
                  })}
                </div>
              : <small>仅在全部计划通过门禁后执行；当前未生成部分答案。</small>}
          </div>
        </li>
      </ol>
    </details>
  )
}

function ClarificationChoices({
  message,
  onConfirm,
}: {
  message: ChatMessage
  onConfirm: (confirmation: TurnConfirmation) => void
}) {
  const clarification = message.turn?.clarification
  const [selectedMetricCodes, setSelectedMetricCodes] = useState<string[]>([])
  const [selectedDecisionByGroup, setSelectedDecisionByGroup] = useState<Record<string, string>>({})
  if (!clarification) return null
  if (clarification.type === 'SEMANTIC_GAP') {
    return (
      <div className="semantic-chat-semantic-gap" role="status">
        <WarningCircle size={18} />
        <div><strong>当前语义资产还不能安全回答</strong><span>{clarification.message}</span><small>可以直接在下方补充明确的指标名称、维度名称或标准维度值后重新提问。</small></div>
      </div>
    )
  }
  if (clarification.type === 'METRIC') {
    const candidates = (clarification.metricCandidates ?? []).slice(0, 12)
    return (
      <div className="semantic-chat-clarification" role="group" aria-label="请选择指标">
        <p className="semantic-chat-clarification-intro"><strong>选择本次要查询的指标</strong><span>可多选；确认后将自动继续维度解析和数据查询。</span></p>
        {candidates.map(candidate => {
          const selected = selectedMetricCodes.includes(candidate.code)
          return (
          <button
            type="button"
            key={candidate.code}
            className={selected ? 'selected' : ''}
            aria-pressed={selected}
            onClick={event => {
              event.stopPropagation()
              setSelectedMetricCodes(current => selected
                ? current.filter(code => code !== candidate.code)
                : [...current, candidate.code])
            }}
          >
            <strong>{candidate.label || candidate.code}</strong>
            <span>{candidate.code}</span>
            <small>{candidate.tableName ? `${candidate.tableSchema}.${candidate.tableName}` : candidate.domain || '已发布指标'}</small>
          </button>
          )
        })}
        <button className="confirm" type="button" disabled={!selectedMetricCodes.length} onClick={event => { event.stopPropagation(); onConfirm({ confirmedMetricCodes: selectedMetricCodes }) }}><CheckCircle size={15} />确认指标并继续</button>
      </div>
    )
  }
  const candidates = (clarification.dimensionCandidates ?? []).slice(0, 16)
  const groups = Array.from(candidates.reduce((result, candidate) => {
    const term = candidate.term?.trim() || candidate.canonicalValue
    const key = `${candidate.metricCode}\u0000${term}`
    const group = result.get(key) ?? { key, term, metricCode: candidate.metricCode, candidates: [] as typeof candidates }
    group.candidates.push(candidate)
    result.set(key, group)
    return result
  }, new Map<string, { key: string; term: string; metricCode: string; candidates: typeof candidates }>()).values())
  const selectedDecisions = groups.map(group => {
    const decisionID = selectedDecisionByGroup[group.key]
    return group.candidates.find(candidate => candidate.decisionId === decisionID)
  }).filter((candidate): candidate is NonNullable<typeof candidate> => Boolean(candidate))
  return (
    <div className="semantic-chat-clarification" role="group" aria-label="请选择维度和值">
      <p className="semantic-chat-clarification-intro"><strong>为每个歧义词选择一个维度和值</strong><span>所有选择都会由服务端重新加载并验证决策图。</span></p>
      {groups.map(group => <fieldset key={group.key}><legend>“{group.term}” · {group.metricCode}</legend><div>{group.candidates.map(candidate => {
        const selected = selectedDecisionByGroup[group.key] === candidate.decisionId
        return <button type="button" className={selected ? 'selected' : ''} aria-pressed={selected} key={candidate.decisionId} onClick={event => { event.stopPropagation(); setSelectedDecisionByGroup(current => ({ ...current, [group.key]: candidate.decisionId })) }}><strong>{candidate.dimensionName || candidate.dimensionCode}：{candidate.canonicalValue}</strong><span>{candidate.dimensionCode}</span><small>{candidate.tableName ? `${candidate.tableSchema}.${candidate.tableName}` : '已发布决策图'}</small></button>
      })}</div></fieldset>)}
      <button className="confirm" type="button" disabled={!groups.length || selectedDecisions.length !== groups.length} onClick={event => {
        event.stopPropagation()
        onConfirm({
          confirmedMetricCodes: [...new Set(selectedDecisions.map(candidate => candidate.metricCode))],
          confirmedDecisions: selectedDecisions.map(candidate => ({ metricCode: candidate.metricCode, decisionId: candidate.decisionId })),
        })
      }}><CheckCircle size={15} />确认维度并继续查询</button>
    </div>
  )
}

export function SemanticChatPage() {
  const [sessions, setSessions] = useState<ChatSession[]>(readSessions)
  const [activeSessionID, setActiveSessionID] = useState(() => sessions[0].id)
  const [selectedMessageID, setSelectedMessageID] = useState('')
  const [question, setQuestion] = useState('')
  const [catalogReadiness, setCatalogReadiness] = useState<SemanticCatalogReadiness>()
  const [catalogError, setCatalogError] = useState('')
  const [goldenSets, setGoldenSets] = useState<GoldenQuestionSet[]>([])
  const [selectedGoldenSetID, setSelectedGoldenSetID] = useState('')
  const [goldenRun, setGoldenRun] = useState<GoldenRunState>()
  const [goldenError, setGoldenError] = useState('')
  const [evaluationGate, setEvaluationGate] = useState<EvaluationReleaseGate>()
  const controllerRef = useRef<AbortController | undefined>(undefined)
  const activeQuestionIDRef = useRef<string | undefined>(undefined)
  const messageEndRef = useRef<HTMLDivElement>(null)

  const activeSession = sessions.find(item => item.id === activeSessionID) ?? sessions[0]
  const assistantMessages = activeSession.messages.filter(message => message.role === 'ASSISTANT' && !message.pending)
  const selectedMessage = assistantMessages.find(message => message.id === selectedMessageID) ?? assistantMessages.at(-1)
  const selectedPlans = messagePlans(selectedMessage)
  const selectedExecutions = messageExecutions(selectedMessage)
  const selectedGoldenSet = goldenSets.find(item => item.id === selectedGoldenSetID)
  const selectedEvidence = selectedPlans.flatMap(plan => plan.evidence)
  const feedbackReady = selectedPlans.length > 0 && selectedExecutions.length === selectedPlans.length
  const isPending = activeSession.messages.some(message => message.pending)

  useEffect(() => {
    sessionStorage.setItem(storageKey, JSON.stringify(sessions.slice(0, 8).map(session => ({
      ...session,
      messages: session.messages.slice(-60),
    }))))
  }, [sessions])

  useEffect(() => {
    let active = true
    Promise.allSettled([
      semanticAssetAPI.readiness(),
      semanticChatAPI.listGoldenQuestionSets(),
    ]).then(results => {
      if (!active) return
      const [readiness, sets] = results
      if (readiness.status === 'fulfilled') setCatalogReadiness(readiness.value)
      else setCatalogError(readiness.reason instanceof Error ? readiness.reason.message : '资产就绪度加载失败')
      if (sets.status === 'fulfilled') {
        setGoldenSets(sets.value)
        const preferred = sets.value.find(item => item.status === 'ACTIVE') ?? sets.value[0]
        if (preferred) setSelectedGoldenSetID(preferred.id)
      }
    })
    return () => { active = false; controllerRef.current?.abort() }
  }, [])

  useEffect(() => {
    let active = true
    setEvaluationGate(undefined)
    if (!selectedGoldenSetID) return () => { active = false }
    semanticChatAPI.getEvaluationReleaseGate(selectedGoldenSetID).then(item => {
      if (active) setEvaluationGate(item)
    }).catch(cause => {
      if (active) setGoldenError(cause instanceof Error ? cause.message : '评测门禁加载失败')
    })
    return () => { active = false }
  }, [selectedGoldenSetID])

  useEffect(() => {
    if (typeof messageEndRef.current?.scrollIntoView === 'function') {
      messageEndRef.current.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
    }
  }, [activeSession.messages.length, isPending])

  const quality = (() => {
    const attempts = assistantMessages.filter(message => !message.pending)
    const executed = attempts.filter(message => {
      const plans = messagePlans(message)
      return plans.length > 0 && messageExecutions(message).length === plans.length
    })
    const evidenced = executed.filter(message => {
      const evidence = messageExecutions(message).map(execution => execution.evidence)
      return evidence.length > 0 && evidence.every(item =>
        item.executionRevalidated && item.permissionDecision &&
        item.freshnessDecision && item.compatibilityDecision,
      )
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
    cancelPendingQuestion()
    const session = createSession()
    setSessions(current => [session, ...current].slice(0, 8))
    setActiveSessionID(session.id)
    setSelectedMessageID('')
    setQuestion('')
  }

  function cancelPendingQuestion() {
    const activeQuestionID = activeQuestionIDRef.current
    if (activeQuestionID) {
      void semanticChatAPI.cancelQuestion(activeQuestionID).catch(() => undefined)
    }
    controllerRef.current?.abort()
  }

  async function submitQuestion(value = question, confirmation: TurnConfirmation = {}) {
    const content = value.trim()
    if (!content || isPending || catalogReadiness?.questionEnabled === false) return
    const sessionID = activeSession.id
    const hasConfirmation = Boolean(
      confirmation.confirmedMetricCodes?.length || confirmation.confirmedDecisions?.length,
    )
    const clarificationMessage = hasConfirmation
      ? [...activeSession.messages].reverse().find(message =>
        message.role === 'ASSISTANT' &&
        message.questionResponse?.status === 'CLARIFICATION_REQUIRED' &&
        message.question === content,
      )
      : undefined
    const userMessage: ChatMessage = { id: newID(), role: 'USER', content, createdAt: now() }
    const assistantCreatedAt = now()
    const assistantMessage: ChatMessage = {
      id: newID(), role: 'ASSISTANT', content: '正在理解问题并验证语义路径…',
      createdAt: assistantCreatedAt, pending: true, question: content,
      progress: [{
        timestamp: assistantCreatedAt,
        stage: 'REQUEST', status: 'RUNNING',
        message: '已提交问题，正在启动语义解析',
      }],
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
      const response = await semanticChatAPI.askQuestion({
        question: content,
        conversationId: activeSession.id,
        parentQuestionId: clarificationMessage?.questionResponse?.questionId,
        confirmedMetricCodes: confirmation.confirmedMetricCodes,
        confirmedDecisions: confirmation.confirmedDecisions,
        signal: controller.signal,
        onProgress: event => {
          if (event.questionId) activeQuestionIDRef.current = event.questionId
          updateMessage(
            sessionID, assistantMessage.id,
            message => ({
              ...message,
              content: event.message,
              progress: appendProgress(message.progress, event),
            }),
          )
        },
      })
      const plans = response.queryPlans
      const executions = response.executions
      if (response.status !== 'ANSWERED' || !response.answer) {
        updateMessage(sessionID, assistantMessage.id, message => ({
          ...message,
          pending: false,
          questionResponse: response,
          turn: response.planning,
          plans,
          plan: plans[0],
          errorCode: response.failure?.code,
          content: response.clarification?.message
            ?? response.failure?.message
            ?? (plans.length ? turnFailureAnswer(plans) : '没有找到可以证明的已发布指标。'),
        }))
        return
      }
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message,
        questionResponse: response,
        turn: response.planning,
        plans,
        executions,
        plan: plans[0],
        execution: executions[0],
        content: '查询、结果和答案忠实性已全部验证，正在展示结果…',
        progress: appendProgress(message.progress, {
          timestamp: now(), stage: 'RESULT_VERIFICATION', status: 'SUCCEEDED',
          message: 'SQL Guard、执行结果与答案证据已全部通过',
        }),
      }))
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message,
        pending: false,
        content: response.answer?.text ?? '已完成查询。',
      }))
    } catch (cause) {
      const error = answerError(cause)
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message, pending: false, errorCode: error.code, content: error.message,
      }))
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = undefined
        activeQuestionIDRef.current = undefined
      }
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

  async function rateSelected(feedback: Feedback) {
    if (!selectedMessage) return
    const allPlans = messagePlans(selectedMessage)
    const plans = allPlans.filter(plan => plan.status === 'EXECUTED')
    if (!plans.length || plans.length !== allPlans.length || selectedMessage.feedbackPending) return
    if (feedback === 'INACCURATE' && !selectedMessage.feedbackIssueType) {
      updateMessage(activeSession.id, selectedMessage.id, message => ({ ...message, feedbackError: '请先选择错误类型，便于进入对应治理队列。' }))
      return
    }
    const sessionID = activeSession.id
    const messageID = selectedMessage.id
    updateMessage(sessionID, messageID, message => ({ ...message, feedbackPending: true, feedbackError: undefined }))
    try {
      if (selectedMessage.questionResponse?.questionId) {
        await semanticChatAPI.submitQuestionFeedback(
          selectedMessage.questionResponse.questionId,
          feedback,
          selectedMessage.feedbackIssueType ?? 'OTHER',
          selectedMessage.feedbackComment ?? '',
        )
      } else {
        await Promise.all(plans.map(plan => semanticChatAPI.submitFeedback(plan.id, feedback, selectedMessage.feedbackIssueType ?? 'OTHER', selectedMessage.feedbackComment ?? '')))
      }
      updateMessage(sessionID, messageID, message => ({
        ...message, feedback, feedbackPending: false, feedbackError: undefined,
      }))
    } catch (cause) {
      updateMessage(sessionID, messageID, message => ({
        ...message,
        feedbackPending: false,
        feedbackError: cause instanceof Error ? cause.message : '反馈提交失败',
      }))
    }
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
      setEvaluationGate(await semanticChatAPI.getEvaluationReleaseGate(selectedGoldenSetID))
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
            <span className={`semantic-chat-graph-state ${catalogReadiness?.questionEnabled ? 'ready' : ''}`}><Graph size={16} /><strong>{catalogReadiness?.questionEnabled ? (catalogReadiness.status === 'WARN' ? '问数可用，部分资产待治理' : '问数资产已就绪') : catalogError || (catalogReadiness ? '问数资产门禁未通过' : '正在核对问数资产')}</strong></span>
            {catalogReadiness && <small>{catalogReadiness.semanticVersion || '尚无可用语义版本'} · 水位 {catalogReadiness.graph.appliedEventVersion}/{catalogReadiness.graph.requestedEventVersion}</small>}
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
                <div>{suggestionQuestions.map(item => <button type="button" disabled={catalogReadiness?.questionEnabled === false} key={item} onClick={() => void submitQuestion(item)}>{item}</button>)}</div>
              </div>
            )}
            {activeSession.messages.map(message => message.role === 'USER'
              ? <article className="semantic-chat-message user" key={message.id}><div>{message.content}</div><time>{new Date(message.createdAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</time></article>
              : <article
                className={`semantic-chat-message assistant ${message.errorCode || messagePlans(message).some(plan => plan.status !== 'EXECUTED' && plan.status !== 'READY') ? 'blocked' : ''} ${selectedMessage?.id === message.id ? 'selected' : ''}`}
                key={message.id}
                onClick={() => setSelectedMessageID(message.id)}
              >
                <span className="semantic-chat-avatar"><Sparkle size={16} weight="fill" /></span>
                <div className="semantic-chat-answer">
                  <header><strong>智能分析助手</strong>{message.pending && <i>处理中</i>}{messagePlans(message).length > 0 && <em className={(turnStatus(messagePlans(message)) ?? '').toLowerCase()}>{statusLabel(turnStatus(messagePlans(message)))}</em>}</header>
                  <InterpretationCard message={message} />
                  <p>{message.content}</p>
                  <LiveRetrievalProcess message={message} />
                  <ClarificationChoices
                    message={message}
                    onConfirm={confirmation => void submitQuestion(
                      message.question ?? message.content,
                      confirmation,
                    )}
                  />
                  <RetrievalProcess message={message} />
                  {messageExecutions(message).map(execution => execution.result.rows.length > 0 && (
                    <div className="semantic-chat-result-block" key={execution.queryPlan.id}>
                      {messageExecutions(message).length > 1 && <strong>{metricLabel(execution.queryPlan)}</strong>}
                      <div className="semantic-chat-result">
                        <table>
                          <thead><tr>{resultColumnLabels(execution).map((column, columnIndex) => <th key={`${column}-${columnIndex}`}>{column}</th>)}</tr></thead>
                          <tbody>{execution.result.rows.slice(0, 10).map((row, rowIndex) => <tr key={rowIndex}>{resultColumnLabels(execution).map((column, columnIndex) => <td key={`${column}-${columnIndex}`}>{formatValue(row[columnIndex])}</td>)}</tr>)}</tbody>
                        </table>
                      </div>
                    </div>
                  ))}
                  <footer>
                    <span>{messageExecutions(message).length
                      ? `${messageExecutions(message).reduce((total, item) => total + item.result.rowCount, 0)} 行 · ${messageExecutions(message).reduce((total, item) => total + item.result.durationMs, 0)} ms`
                      : messagePlans(message).length
                        ? `${messagePlans(message).length} 个计划已形成，尚未产生可验证结果`
                        : message.errorCode}</span>
                    {messageExecutions(message).length > 0 && <span><ShieldCheck size={14} />{messageExecutions(message).reduce((total, item) => total + item.evidence.lineage.length, 0)} 项可信证据</span>}
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
              disabled={isPending || catalogReadiness?.questionEnabled === false}
            />
            {isPending
              ? <button className="semantic-chat-cancel" type="button" aria-label="取消本轮问答" onClick={cancelPendingQuestion}><XCircle size={19} weight="fill" /></button>
              : <button className="primary-button" type="submit" aria-label="发送问题" disabled={!question.trim() || catalogReadiness?.questionEnabled === false}><PaperPlaneTilt size={18} weight="fill" /></button>}
            <small>{catalogReadiness?.questionEnabled === false ? `资产门禁阻断：${catalogReadiness.blockerCodes.join('、')}` : isPending ? '可取消本轮检索或查询；已取消的结果不会展示' : 'Enter 发送 · Shift + Enter 换行 · 追问继承上一轮已验证语义槽位'}</small>
          </form>
        </section>

        <aside className="semantic-chat-quality">
          <header><span className="eyebrow">答案验证</span><h2>效果与准确性</h2><p>可信等级来自确定性验证；用户反馈仅是产品信号，不计入正式准确率。</p></header>
          <section className="semantic-chat-score-grid" aria-label="本会话质量统计">
            <div><span>回答成功率</span><strong>{quality.successRate == null ? '—' : `${quality.successRate}%`}</strong></div>
            <div><span>证据完整率</span><strong>{quality.evidenceRate == null ? '—' : `${quality.evidenceRate}%`}</strong></div>
            <div><span>用户正向反馈</span><strong>{quality.manualRate == null ? '—' : `${quality.manualRate}%`}</strong></div>
          </section>

          <section className="semantic-chat-current-quality">
            <header><strong>当前答案</strong><span>{selectedMessage?.errorCode ? '可信拒答' : statusLabel(turnStatus(selectedPlans))}</span></header>
            {!selectedMessage ? <p className="semantic-chat-empty-quality">发送问题后，这里会展示逐项验证结果。</p> : <>
              <div className="semantic-chat-confidence"><span>确定性验证等级</span><strong>{selectedMessage.questionResponse?.resultVerification?.trustLevel ? `${selectedMessage.questionResponse.resultVerification.trustLevel} 级` : '未通过'}</strong></div>
              {selectedMessage.questionResponse && <div className="semantic-chat-confidence"><span>执行路径 · {selectedMessage.questionResponse.route}</span><strong>{selectedMessage.questionResponse.resultVerification?.trustLevel ? `可信 ${selectedMessage.questionResponse.resultVerification.trustLevel} 级` : selectedMessage.questionResponse.status}</strong></div>}
              {selectedPlans[0]?.resolution?.length ? <ol className="semantic-chat-resolution" aria-label="问答定位链路">{selectedPlans[0].resolution.map((step, index) => <li className={step.status === 'RESOLVED' || step.status === 'SKIPPED' ? 'pass' : ''} key={`${step.stage}-${index}`}><span>{index + 1}</span><div><strong>{resolutionStageLabel(step.stage)}</strong><small>{resolutionStatusLabel(step.status)}{step.candidateCount ? ` · ${step.candidateCount} 个候选` : ''}</small></div></li>)}</ol> : null}
              <ul>
                {selectedMessage.questionResponse?.sqlGuard && <li className={selectedMessage.questionResponse.sqlGuard.status === 'PASS' ? 'pass' : ''}><ShieldCheck size={16} /><span><strong>SQL Guard</strong><small>{selectedMessage.questionResponse.sqlGuard.status === 'PASS' ? `${selectedMessage.questionResponse.sqlGuard.checks.length} 项只读、白名单和预算门禁通过` : '执行计划被安全门禁阻断'}</small></span>{selectedMessage.questionResponse.sqlGuard.status === 'PASS' ? <CheckCircle /> : <WarningCircle />}</li>}
                {selectedMessage.questionResponse?.resultVerification && <li className={selectedMessage.questionResponse.resultVerification.status === 'PASS' ? 'pass' : ''}><CheckCircle size={16} /><span><strong>结果验证</strong><small>{selectedMessage.questionResponse.resultVerification.status === 'PASS' ? `${selectedMessage.questionResponse.resultVerification.checks.length} 项模式、行数、权限和哈希检查通过` : '结果不满足展示合同'}</small></span>{selectedMessage.questionResponse.resultVerification.status === 'PASS' ? <CheckCircle /> : <WarningCircle />}</li>}
                <li className={selectedExecutions.length === selectedPlans.length && selectedPlans.length > 0 ? 'pass' : ''}><Database size={16} /><span><strong>查询执行</strong><small>{selectedExecutions.length === selectedPlans.length && selectedPlans.length > 0 ? `${selectedExecutions.length} 个受控查询已完成` : '未执行或执行失败'}</small></span>{selectedExecutions.length === selectedPlans.length && selectedPlans.length > 0 ? <CheckCircle /> : <WarningCircle />}</li>
                <li className={selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.executionRevalidated) ? 'pass' : ''}><ShieldCheck size={16} /><span><strong>权限与版本</strong><small>{selectedExecutions.length > 0 ? '全部指标已在运行时重新校验' : '等待可执行计划'}</small></span>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.executionRevalidated) ? <CheckCircle /> : <WarningCircle />}</li>
                <li className={selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE') ? 'pass' : ''}><Graph size={16} /><span><strong>维度兼容</strong><small>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE') ? '全部指标均无扇出风险' : '尚未通过验证'}</small></span>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE') ? <CheckCircle /> : <WarningCircle />}</li>
                <li className={selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.queryPlanHash && item.evidence.resultHash) ? 'pass' : ''}><ShieldCheck size={16} /><span><strong>计划与结果快照</strong><small>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.queryPlanHash && item.evidence.resultHash) ? '可用哈希复核本次计划与结果' : '旧结果缺少可复核快照'}</small></span>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.queryPlanHash && item.evidence.resultHash) ? <CheckCircle /> : <WarningCircle />}</li>
              </ul>
              {selectedPlans.some(plan => plan.conditions?.metricVersionId) ? <details><summary>查看查询条件 JSON</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedPlans.map(plan => plan.conditions), null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.semanticIr ? <details><summary>查看 Semantic Query IR</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.semanticIr, null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.routing ? <details><summary>查看三路路由与能力门禁</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.routing, null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.understanding ? <details><summary>查看规范化与原文证据</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.understanding, null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.graphPlan ? <details><summary>查看 NebulaGraph 约束计划</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.graphPlan, null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.executionRegistry ? <details><summary>查看执行注册表证明</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.executionRegistry, null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.preflightProofs?.length ? <details><summary>查看 SQL 预检证明</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.preflightProofs, null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.toolRegistry?.length ? <details><summary>查看 Host Tool Registry</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.toolRegistry, null, 2)}</pre></details> : null}
              {selectedMessage.questionResponse?.accuracyEvidence ? <details><summary>查看统一 AccuracyEvidence</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedMessage.questionResponse.accuracyEvidence, null, 2)}</pre></details> : null}
              {selectedExecutions.length ? <details><summary>查看 AccuracyEvidence</summary><div className="semantic-chat-accuracy-evidence">
                {selectedExecutions.map(execution => <article key={execution.queryPlan.id}>
                  <strong>{metricLabel(execution.queryPlan)}</strong>
                  <dl>
                    <div><dt>语义版本</dt><dd>{execution.evidence.semanticVersion || `semantic-graph-${execution.evidence.graphGeneration}`}</dd></div>
                    <div><dt>验证时间</dt><dd>{execution.evidence.verifiedAt ? new Date(execution.evidence.verifiedAt).toLocaleString('zh-CN', { hour12: false }) : '旧结果未记录'}</dd></div>
                    <div><dt>计划哈希</dt><dd title={execution.evidence.queryPlanHash}>{compactID(execution.evidence.queryPlanHash)}</dd></div>
                    <div><dt>结果哈希</dt><dd title={execution.evidence.resultHash}>{compactID(execution.evidence.resultHash)}</dd></div>
                    <div><dt>查询追踪</dt><dd title={execution.evidence.queryTraceId}>{compactID(execution.evidence.queryTraceId)}</dd></div>
                  </dl>
                  <ul>{(execution.evidence.validatorChecks ?? []).map(check => <li key={check}><CheckCircle size={12} weight="fill" />{validatorCheckLabel(check)}</li>)}</ul>
                </article>)}
              </div></details> : null}
              {selectedEvidence.length ? <details><summary>查看证据链（{selectedEvidence.length}）</summary><ol>{selectedEvidence.map((item, index) => <li key={`${index}-${item.nodeKey}`}><span>{item.label}</span><small>{item.subjectType} · {item.authority} · {Math.round(item.confidence * 100)}%</small></li>)}</ol></details> : null}
              <div className="semantic-chat-feedback"><span>这个答案准确吗？反馈只进入运营治理，不计入黄金准确率。</span><select aria-label="答案错误类型" value={selectedMessage.feedbackIssueType ?? ''} disabled={!feedbackReady || selectedMessage.feedbackPending} onChange={event => updateMessage(activeSession.id, selectedMessage.id, message => ({ ...message, feedbackIssueType: event.target.value as FeedbackIssueType, feedbackError: undefined }))}><option value="">不准确时请选择错误类型</option><option value="METRIC_DEFINITION">指标口径错误</option><option value="FILTER">筛选条件错误</option><option value="RESULT_VALUE">数字或结果错误</option><option value="PERMISSION">权限问题</option><option value="FRESHNESS">数据新鲜度问题</option><option value="EXPRESSION">表达或图表问题</option><option value="OTHER">其他问题</option></select><textarea aria-label="补充结果点评" maxLength={2000} placeholder="可选：补充指标口径、维度筛选或结果方面的意见" value={selectedMessage.feedbackComment ?? ''} disabled={!feedbackReady || selectedMessage.feedbackPending} onChange={event => updateMessage(activeSession.id, selectedMessage.id, message => ({ ...message, feedbackComment: event.target.value }))} /><div><button className={selectedMessage.feedback === 'ACCURATE' ? 'active positive' : ''} type="button" disabled={!feedbackReady || selectedMessage.feedbackPending} onClick={() => void rateSelected('ACCURATE')}><ThumbsUp size={15} />{selectedMessage.feedbackPending ? '提交中' : '准确'}</button><button className={selectedMessage.feedback === 'INACCURATE' ? 'active negative' : ''} type="button" disabled={!feedbackReady || selectedMessage.feedbackPending} onClick={() => void rateSelected('INACCURATE')}><ThumbsDown size={15} />{selectedMessage.feedbackPending ? '提交中' : '不准确'}</button></div>{selectedMessage.feedbackError && <small role="alert">{selectedMessage.feedbackError}</small>}</div>
            </>}
          </section>

          <section className="semantic-chat-golden">
            <header><div><TestTube size={17} /><strong>黄金评测与发布门禁</strong></div>{evaluationGate && <span className={evaluationGate.decision === 'PASSED' ? 'passed' : 'blocked'}>{evaluationGate.decision === 'PASSED' ? '允许发布' : '阻断发布'}</span>}</header>
            {goldenSets.length === 0 ? <p>尚未配置黄金问题集。可在语义治理流程中建立版本化测试门禁。</p> : <>
              <select aria-label="黄金问题集" value={selectedGoldenSetID} onChange={event => { setSelectedGoldenSetID(event.target.value); setGoldenRun(undefined) }}>
                {goldenSets.map(set => <option key={set.id} value={set.id}>{set.name} · V{set.version} · {set.datasetSplit} · {set.status}</option>)}
              </select>
              {selectedGoldenSet && <p className="semantic-chat-evaluation-mode">{selectedGoldenSet.evaluationMode === 'END_TO_END_RESULT_EQUIVALENCE' ? '正式端到端结果等价评测' : '规划回归（不计入正式准确率）'}{goldenRate != null ? ` · 本次回放 ${goldenRate}%` : ''}</p>}
              {goldenRun && <div className="semantic-chat-golden-progress"><span>已完成 {goldenRun.completed}/{goldenRun.total}</span><progress value={goldenRun.completed} max={Math.max(1, goldenRun.total)} /></div>}
              <button className="quiet-button full" type="button" onClick={() => void runGoldenQuestions()} disabled={!selectedGoldenSetID || Boolean(goldenRun && goldenRun.completed < goldenRun.total)}><ChartLineUp size={16} />{selectedGoldenSet?.evaluationMode === 'END_TO_END_RESULT_EQUIVALENCE' ? '运行端到端评测' : '运行规划回归'}</button>
            </>}
            {evaluationGate && <div className="semantic-chat-release-gate">
              <div className="semantic-chat-release-metrics">
                <p><span>样本/已评测/双审</span><strong>{evaluationGate.totalCases}/{evaluationGate.evaluatedCases}/{evaluationGate.dualReviewedCases}</strong></p>
                <p><span>严格准确率</span><strong>{metricPercent(evaluationGate.strictAccuracy.pointEstimate)}</strong><small>Wilson 下界 {metricPercent(evaluationGate.strictAccuracy.wilsonLowerBound)}</small></p>
                <p><span>P0 / 安全阻断 / 越权</span><strong>{metricPercent(evaluationGate.p0Accuracy.pointEstimate)} / {metricPercent(evaluationGate.safetyBlockRate.pointEstimate)} / {metricPercent(evaluationGate.unauthorizedBlockRate.pointEstimate)}</strong></p>
                <p><span>覆盖率 / 拒答精确率</span><strong>{metricPercent(evaluationGate.directAnswerCoverage.pointEstimate)} / {metricPercent(evaluationGate.refusalPrecision.pointEstimate)}</strong></p>
              </div>
              {evaluationGate.sensitiveLeakCount > 0 && <p className="semantic-chat-golden-error">敏感泄漏 {evaluationGate.sensitiveLeakCount} 条</p>}
              {evaluationGate.blockers.length > 0 && <details open><summary>发布阻断项（{evaluationGate.blockers.length}）</summary><ul>{evaluationGate.blockers.map(code => <li key={code}>{releaseGateBlockerLabel(code)}</li>)}</ul></details>}
              {Object.keys(evaluationGate.failureStageCounts).length > 0 && <details><summary>首次错误阶段</summary><ul>{Object.entries(evaluationGate.failureStageCounts).map(([stage, count]) => <li key={stage}>{stage}：{count}</li>)}</ul></details>}
              <small>正式门槛：2,000+ sealed 双审样本，点估计 ≥96%、Wilson 下界 ≥95%、P0 与越权阻断 100%、敏感泄漏 0。</small>
            </div>}
            {goldenError && <small className="semantic-chat-golden-error">{goldenError}</small>}
          </section>
        </aside>
      </section>
    </AppShell>
  )
}
