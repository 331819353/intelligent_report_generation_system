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
  type SemanticQueryTurn,
} from '../lib/semantic-chat'

type Feedback = 'ACCURATE' | 'INACCURATE'

type ChatMessage = {
  id: string
  role: 'USER' | 'ASSISTANT'
  content: string
  createdAt: string
  pending?: boolean
  question?: string
  turn?: SemanticQueryTurn
  plans?: SemanticQueryPlan[]
  executions?: SemanticQueryExecution[]
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

type TurnConfirmation = {
  confirmedMetricCodes?: string[]
  confirmedDecisions?: Array<{ metricCode: string; decisionId: string }>
}

const storageKey = 'intelligent-report-semantic-chat-v1'
const workforceMetricCode = 'metric_dws_employee_profile_regenerated_20260727_em_904c04ae2441'
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

function buildAnswer(execution: SemanticQueryExecution, question = '') {
  const { result, comparison } = execution
  if (result.rowCount === 0 || result.rows.length === 0) return '在当前筛选条件与数据权限范围内没有查询到结果。'
  const first = result.rows[0] ?? []
  const columns = resultColumnLabels(execution)
  const conditions = dimensionConditionText(execution.queryPlan)
  const scope = question.includes('小微') ||
    execution.queryPlan.conditions?.metricCode === workforceMetricCode
    ? '在小微人员范围内，'
    : ''
  let answer = result.rowCount === 1 && columns.length === 1 && conditions
    ? `查询结果：${scope}按${conditions}的口径，${columns[0]}为 ${formatValue(first[0])}。`
    : result.rowCount === 1
      ? `查询结果：${columns.slice(0, 4).map((column, index) => `${column}为 ${formatValue(first[index])}`).join('，')}。`
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

function buildTurnAnswer(executions: SemanticQueryExecution[], question = '') {
  if (executions.length === 1) return buildAnswer(executions[0], question)
  const values = executions.map(execution => {
    const label = metricLabel(execution.queryPlan)
    if (!execution.result.rows.length) return `${label}未查询到结果`
    const row = execution.result.rows[0]
    return `${label}为 ${formatValue(row[row.length - 1])}`
  })
  return `已完成 ${executions.length} 个指标的可信查询：${values.join('；')}。`
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

function memberKeyPreview(values: string[] = [], limit = 6) {
  if (!values.length) return '无可展示值'
  const preview = values.slice(0, limit).join('、')
  return values.length > limit ? `${preview}，另 ${values.length - limit} 个` : preview
}

function RetrievalProcess({ message }: { message: ChatMessage }) {
  const plans = messagePlans(message)
  if (!plans.length) return null
  const executions = messageExecutions(message)
  const trace = message.turn?.trace
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
  const executionComplete = executions.length === plans.length
  return (
    <details className="semantic-chat-retrieval-process" open>
      <summary><Graph size={15} /><strong>查看检索结果的过程</strong><span>{trace.conversationQuestions.length} 轮上下文 · {plans.length} 个指标 · {lineage} 项血缘</span></summary>
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
            </div> : <p className="semantic-chat-empty-trace">本轮及继承上下文均未要求维度值筛选。</p>}
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
  if (!clarification) return null
  if (clarification.type === 'SEMANTIC_GAP') return null
  if (clarification.type === 'METRIC') {
    return (
      <div className="semantic-chat-clarification" role="group" aria-label="请选择指标">
        {(clarification.metricCandidates ?? []).slice(0, 12).map(candidate => (
          <button
            type="button"
            key={candidate.code}
            onClick={event => {
              event.stopPropagation()
              onConfirm({ confirmedMetricCodes: [candidate.code] })
            }}
          >
            <strong>{candidate.label || candidate.code}</strong>
            <span>{candidate.code}</span>
            <small>{candidate.tableName ? `${candidate.tableSchema}.${candidate.tableName}` : candidate.domain || '已发布指标'}</small>
          </button>
        ))}
      </div>
    )
  }
  return (
    <div className="semantic-chat-clarification" role="group" aria-label="请选择维度和值">
      {(clarification.dimensionCandidates ?? []).slice(0, 16).map(candidate => (
        <button
          type="button"
          key={candidate.decisionId}
          onClick={event => {
            event.stopPropagation()
            onConfirm({
              confirmedMetricCodes: [candidate.metricCode],
              confirmedDecisions: [{
                metricCode: candidate.metricCode,
                decisionId: candidate.decisionId,
              }],
            })
          }}
        >
          <strong>{candidate.dimensionName || candidate.dimensionCode}：{candidate.canonicalValue}</strong>
          <span>{candidate.dimensionCode}</span>
          <small>{candidate.tableName ? `${candidate.tableSchema}.${candidate.tableName}` : '已发布决策图'}</small>
        </button>
      ))}
    </div>
  )
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
  const selectedPlans = messagePlans(selectedMessage)
  const selectedExecutions = messageExecutions(selectedMessage)
  const selectedConfidence = selectedPlans.length
    ? Math.min(...selectedPlans.map(plan => plan.confidence))
    : undefined
  const selectedEvidence = selectedPlans.flatMap(plan => plan.evidence)
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
    controllerRef.current?.abort()
    const session = createSession()
    setSessions(current => [session, ...current].slice(0, 8))
    setActiveSessionID(session.id)
    setSelectedMessageID('')
    setQuestion('')
  }

  async function submitQuestion(value = question, confirmation: TurnConfirmation = {}) {
    const content = value.trim()
    if (!content || isPending) return
    const sessionID = activeSession.id
    const priorQuestions = activeSession.messages
      .filter(message => message.role === 'USER')
      .slice(-2)
      .map(message => message.content)
    const contextMessage = [...activeSession.messages].reverse().find(message =>
      message.role === 'ASSISTANT' && messagePlans(message).some(plan =>
        plan.status === 'READY' || plan.status === 'EXECUTED',
      ),
    )
    const contextPlanIDs = messagePlans(contextMessage).filter(plan =>
      plan.status === 'READY' || plan.status === 'EXECUTED',
    ).map(plan => plan.id)
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
      const turn = await semanticChatAPI.planTurn({
        question: content,
        priorQuestions,
        contextQueryPlanIds: contextPlanIDs,
        confirmedMetricCodes: confirmation.confirmedMetricCodes,
        confirmedDecisions: confirmation.confirmedDecisions,
        signal: controller.signal,
      })
      const plans = turn.plans
      if (turn.clarification || !plans.length || plans.some(plan => plan.status !== 'READY')) {
        updateMessage(sessionID, assistantMessage.id, message => ({
          ...message, pending: false, turn, plans, plan: plans[0],
          content: turn.clarification?.message
            ?? (plans.length ? turnFailureAnswer(plans) : '没有找到可以证明的已发布指标。'),
        }))
        return
      }
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message, turn, plans, plan: plans[0],
        content: `已验证 ${plans.length} 个指标的语义路径，正在执行受控查询…`,
      }))
      const executions = await Promise.all(plans.map(plan =>
        semanticChatAPI.executePlan(plan, controller.signal),
      ))
      const executedPlans = executions.map(execution => execution.queryPlan)
      updateMessage(sessionID, assistantMessage.id, message => ({
        ...message,
        pending: false,
        plans: executedPlans,
        executions,
        plan: executedPlans[0],
        execution: executions[0],
        content: buildTurnAnswer(executions, content),
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
                className={`semantic-chat-message assistant ${message.errorCode || messagePlans(message).some(plan => plan.status !== 'EXECUTED' && plan.status !== 'READY') ? 'blocked' : ''} ${selectedMessage?.id === message.id ? 'selected' : ''}`}
                key={message.id}
                onClick={() => setSelectedMessageID(message.id)}
              >
                <span className="semantic-chat-avatar"><Sparkle size={16} weight="fill" /></span>
                <div className="semantic-chat-answer">
                  <header><strong>智能分析助手</strong>{message.pending && <i>处理中</i>}{messagePlans(message).length > 0 && <em className={(turnStatus(messagePlans(message)) ?? '').toLowerCase()}>{statusLabel(turnStatus(messagePlans(message)))}</em>}</header>
                  <p>{message.content}</p>
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
                        ? `${Math.round(Math.min(...messagePlans(message).map(plan => plan.confidence)) * 100)}% 最低检索置信度`
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
            <header><strong>当前答案</strong><span>{selectedMessage?.errorCode ? '可信拒答' : statusLabel(turnStatus(selectedPlans))}</span></header>
            {!selectedMessage ? <p className="semantic-chat-empty-quality">发送问题后，这里会展示逐项验证结果。</p> : <>
              <div className="semantic-chat-confidence"><span>{selectedPlans.length > 1 ? `${selectedPlans.length} 个指标最低置信度` : '检索路径置信度'}</span><strong>{selectedConfidence == null ? '—' : `${Math.round(selectedConfidence * 100)}%`}</strong></div>
              {selectedPlans[0]?.resolution?.length ? <ol className="semantic-chat-resolution" aria-label="问答定位链路">{selectedPlans[0].resolution.map((step, index) => <li className={step.status === 'RESOLVED' || step.status === 'SKIPPED' ? 'pass' : ''} key={`${step.stage}-${index}`}><span>{index + 1}</span><div><strong>{resolutionStageLabel(step.stage)}</strong><small>{resolutionStatusLabel(step.status)}{step.candidateCount ? ` · ${step.candidateCount} 个候选` : ''}</small></div></li>)}</ol> : null}
              <ul>
                <li className={selectedExecutions.length === selectedPlans.length && selectedPlans.length > 0 ? 'pass' : ''}><Database size={16} /><span><strong>查询执行</strong><small>{selectedExecutions.length === selectedPlans.length && selectedPlans.length > 0 ? `${selectedExecutions.length} 个受控查询已完成` : '未执行或执行失败'}</small></span>{selectedExecutions.length === selectedPlans.length && selectedPlans.length > 0 ? <CheckCircle /> : <WarningCircle />}</li>
                <li className={selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.executionRevalidated) ? 'pass' : ''}><ShieldCheck size={16} /><span><strong>权限与版本</strong><small>{selectedExecutions.length > 0 ? '全部指标已在运行时重新校验' : '等待可执行计划'}</small></span>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.executionRevalidated) ? <CheckCircle /> : <WarningCircle />}</li>
                <li className={selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE') ? 'pass' : ''}><Graph size={16} /><span><strong>维度兼容</strong><small>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE') ? '全部指标均无扇出风险' : '尚未通过验证'}</small></span>{selectedExecutions.length > 0 && selectedExecutions.every(item => item.evidence.compatibilityDecision === 'VERIFIED_NON_UNSAFE') ? <CheckCircle /> : <WarningCircle />}</li>
              </ul>
              {selectedPlans.some(plan => plan.conditions?.metricVersionId) ? <details><summary>查看查询条件 JSON</summary><pre className="semantic-chat-condition-json">{JSON.stringify(selectedPlans.map(plan => plan.conditions), null, 2)}</pre></details> : null}
              {selectedEvidence.length ? <details><summary>查看证据链（{selectedEvidence.length}）</summary><ol>{selectedEvidence.map((item, index) => <li key={`${index}-${item.nodeKey}`}><span>{item.label}</span><small>{item.subjectType} · {item.authority} · {Math.round(item.confidence * 100)}%</small></li>)}</ol></details> : null}
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
