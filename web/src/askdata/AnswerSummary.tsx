import {
  ArrowClockwise,
  CaretDown,
  CheckCircle,
  EyeSlash,
  LockSimple,
  ShieldCheck,
} from '@phosphor-icons/react'
import { useState } from 'react'
import type { QuestionAnswerPresentation } from '../lib/ask-data-api'
import { answerSummarySnapshot, degradedAnswerReason } from './answer-summary'

type AnswerSummaryProps = {
  answer: QuestionAnswerPresentation
  onRetryNarrative?: () => void
}

export function AnswerStatusBadges({ answer }: { answer: QuestionAnswerPresentation }) {
  const snapshot = answerSummarySnapshot(answer)
  return <span className="ask-answer-status-badges" aria-label={snapshot.status.join('，')}>
    <span className="is-verified"><CheckCircle size={10} weight="fill" aria-hidden="true" />{snapshot.status[0]}</span>
    {answer.narrativeDegraded
      ? <span className="is-withheld"><EyeSlash size={10} weight="fill" aria-hidden="true" />{snapshot.status[1]}</span>
      : <span className="is-verified"><ShieldCheck size={10} weight="fill" aria-hidden="true" />{snapshot.status[1]}</span>}
  </span>
}

/** ANS-003: only verified prose or the governed L1 fallback can render here. */
export function AnswerSummary({ answer, onRetryNarrative }: AnswerSummaryProps) {
  const [open, setOpen] = useState(answer.narrativeDegraded)
  const snapshot = answerSummarySnapshot(answer)

  if (!answer.narrativeDegraded && answer.narrative) {
    return <section className="ask-answer-summary is-verified" aria-labelledby="ask-answer-summary-title">
      <header><ShieldCheck size={14} weight="fill" aria-hidden="true" /><strong id="ask-answer-summary-title">{snapshot.headline}</strong></header>
      <p>{answer.narrative.summary}</p>
      {answer.narrative.findings.length > 0 && <ul>{answer.narrative.findings.map(finding => <li key={finding}>{finding}</li>)}</ul>}
    </section>
  }

  return <section className="ask-answer-summary is-degraded" aria-labelledby="ask-answer-degraded-title">
    <button type="button" aria-expanded={open} aria-controls="ask-answer-degraded-detail" onClick={() => setOpen(value => !value)}>
      <span><ShieldCheck size={14} weight="fill" aria-hidden="true" /><strong id="ask-answer-degraded-title">{snapshot.headline}</strong></span>
      <CaretDown className={open ? 'is-open' : ''} size={13} aria-hidden="true" />
    </button>
    {open && <div id="ask-answer-degraded-detail">
      <p>{degradedAnswerReason}</p>
      <div>
        {onRetryNarrative && <button type="button" onClick={onRetryNarrative}><ArrowClockwise size={12} aria-hidden="true" />重新生成结论</button>}
        <button type="button" title={snapshot.message} onClick={() => document.getElementById('clarification-evidence-answer-layers')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })}>查看校验依据</button>
      </div>
    </div>}
  </section>
}

export function AnswerLayerStatus({ answer }: { answer: QuestionAnswerPresentation }) {
  return <div className="ask-answer-layer-status" aria-label="答案层级状态">
    <div><CheckCircle size={13} weight="fill" aria-hidden="true" /><span><strong>L1 结构化结果</strong><small>已展示</small></span></div>
    <div className={answer.narrativeDegraded ? 'is-withheld' : ''}>
      {answer.narrativeDegraded ? <EyeSlash size={13} weight="fill" aria-hidden="true" /> : <ShieldCheck size={13} weight="fill" aria-hidden="true" />}
      <span><strong>L2 文字结论</strong><small>{answer.narrativeDegraded ? '已隐藏' : '已展示'}</small></span>
    </div>
    <div className="is-disabled"><LockSimple size={13} weight="fill" aria-hidden="true" /><span><strong>L3 业务解读</strong><small>问数默认关闭</small></span></div>
  </div>
}
