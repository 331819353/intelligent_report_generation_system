import {
  ArrowClockwise,
  CheckCircle,
  Database,
  Info,
  SealCheck,
  WarningCircle,
  XCircle,
} from '@phosphor-icons/react'
import type { AskDataQuestionState } from '../../hooks/use-ask-data-question'
import type { QuestionScopeVerdict } from '../../lib/ask-data-api'
import { detailDataRequestAction } from './scope-exit'

type ConversationOutcomeProps = {
  state: AskDataQuestionState
  onRetry: () => void
  onDataRequest?: (verdict: QuestionScopeVerdict) => void
}

function shortHash(value: string | undefined) {
  if (!value) return '待记录'
  return `${value.slice(0, 10)}…${value.slice(-6)}`
}

/** WEB-003 只展示终态与受控完成引用；结果表、图和定向澄清分别由 WEB-005/WEB-004 接入。 */
export function ConversationOutcome({ state, onRetry, onDataRequest }: ConversationOutcomeProps) {
  if (state.phase === 'ERROR' && state.error) {
    return (
      <section className="ask-outcome-card is-error" role="alert" aria-labelledby="ask-outcome-error-title">
        <span className="ask-outcome-icon"><WarningCircle size={21} weight="fill" aria-hidden="true" /></span>
        <div>
          <h2 id="ask-outcome-error-title">本次分析未能继续</h2>
          <p>{state.error.message}</p>
          <small>错误类别：{state.error.kind}</small>
        </div>
        {state.error.retryable && <button type="button" onClick={onRetry}><ArrowClockwise size={14} />重新尝试</button>}
      </section>
    )
  }

  if (state.phase === 'CANCELED') {
    return (
      <section className="ask-outcome-card is-canceled" role="status" aria-labelledby="ask-outcome-canceled-title">
        <span className="ask-outcome-icon"><XCircle size={21} weight="fill" aria-hidden="true" /></span>
        <div><h2 id="ask-outcome-canceled-title">本次运行已取消</h2><p>已停止接收后续事件；不会把未完成结果展示为答案。</p></div>
        <button type="button" onClick={onRetry}><ArrowClockwise size={14} />重新分析</button>
      </section>
    )
  }

  const run = state.run
  if (state.phase !== 'TERMINAL' || !run) return null

  const scopeVerdict = run.completion?.scopeVerdict
  if (scopeVerdict && (scopeVerdict.outcome === 'OUT_OF_SCOPE' || scopeVerdict.outcome === 'BLOCKED')) {
    const dataRequestAction = detailDataRequestAction(scopeVerdict)
    return (
      <section className="ask-outcome-card is-scope-exit" role="status" aria-labelledby="ask-outcome-scope-title">
        <span className="ask-outcome-icon"><Database size={21} weight="duotone" aria-hidden="true" /></span>
        <div>
          <h2 id="ask-outcome-scope-title">{scopeVerdict.reason === 'SCOPE_DETAIL_LIST' ? '这类明细需要走取数申请' : '当前问题不在可信问数范围内'}</h2>
          <p>{scopeVerdict.userMessage || '当前问题无法在已治理的汇总分析范围内执行。'}</p>
          <small>范围分类：{scopeVerdict.type} · 规则版本 {scopeVerdict.lexiconVersion}</small>
        </div>
        {dataRequestAction && onDataRequest && <button type="button" onClick={() => onDataRequest(scopeVerdict)}>
          <Database size={14} weight="duotone" aria-hidden="true" />{dataRequestAction.label}
        </button>}
      </section>
    )
  }

  if (run.state === 'BLOCKED') {
    return (
      <section className="ask-outcome-card is-blocked" role="alert" aria-labelledby="ask-outcome-blocked-title">
        <span className="ask-outcome-icon"><WarningCircle size={21} weight="fill" aria-hidden="true" /></span>
        <div>
          <h2 id="ask-outcome-blocked-title">分析已被受控门禁阻断</h2>
          <p>当前证据不足以安全生成答案；系统已停止执行并保留可审计记录。</p>
          <small>阻断记录 {shortHash(run.completion?.artifactHash)} · 证据 {run.completion?.evidenceIds.length ?? 0} 项</small>
        </div>
      </section>
    )
  }

  if (run.state === 'CLARIFICATION_REQUIRED') {
    return (
      <section className="ask-outcome-card is-clarification" role="status" aria-labelledby="ask-outcome-clarification-title">
        <span className="ask-outcome-icon"><Info size={21} weight="fill" aria-hidden="true" /></span>
        <div>
          <h2 id="ask-outcome-clarification-title">需要确认一个业务口径</h2>
          <p>{run.completion?.clarification?.message || '系统发现多个可用口径，需要定向确认后继续。'}</p>
          <small>可选口径 {run.completion?.clarification?.options.length ?? 0} 个 · 选择交互将在定向澄清卡片中提供</small>
        </div>
      </section>
    )
  }

  if (run.state === 'CLARIFICATION_EXPIRED') {
    return (
      <section className="ask-outcome-card is-clarification" role="alert" aria-labelledby="ask-outcome-expired-title">
        <span className="ask-outcome-icon"><WarningCircle size={21} weight="fill" aria-hidden="true" /></span>
        <div>
          <h2 id="ask-outcome-expired-title">本次澄清已过期</h2>
          <p>原选择已失效；如口径版本同时发生变化，需要确认最新口径后重新分析。</p>
          <small>已冻结的运行预算不会计入等待时间，历史记录仍可查看。</small>
        </div>
      </section>
    )
  }

  return (
    <section className="ask-outcome-card is-answered" role="status" aria-live="polite" aria-labelledby="ask-outcome-answered-title">
      <span className="ask-outcome-icon"><CheckCircle size={21} weight="fill" aria-hidden="true" /></span>
      <div>
        <h2 id="ask-outcome-answered-title">最终回答已生成</h2>
        <p>分析结果已通过受控核验；结果内容将由已验证的表格与图表组件呈现。</p>
        <small><SealCheck size={13} weight="fill" aria-hidden="true" />回答记录 {shortHash(run.completion?.artifactHash)} · 证据 {run.completion?.evidenceIds.length ?? 0} 项</small>
      </div>
    </section>
  )
}
