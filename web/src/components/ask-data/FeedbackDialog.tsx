import {
  Article,
  CalendarBlank,
  ChartBar,
  ChartPieSlice,
  Check,
  CheckCircle,
  Cube,
  DotsThreeCircle,
  Info,
  ListBullets,
  ShieldWarning,
  TreeStructure,
  X,
} from '@phosphor-icons/react'
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'

import type {
  QuestionFeedbackIssueType,
  QuestionFeedbackSubmission,
  QuestionRun,
} from '../../lib/ask-data-api.ts'
import { FEEDBACK_ISSUES, feedbackIssue } from './feedback-model.ts'

type FeedbackDialogProps = {
  open: boolean
  run: QuestionRun
  onClose: () => void
  onSubmit: (submission: QuestionFeedbackSubmission) => Promise<void>
}

type FeedbackStep = 'ISSUE' | 'DETAIL' | 'SUCCESS'

function IssueIcon({ type }: { type: QuestionFeedbackIssueType }) {
  const props = { size: 20, weight: 'duotone' as const, 'aria-hidden': true }
  const icons: Record<Exclude<QuestionFeedbackIssueType, 'NONE'>, ReactNode> = {
    METRIC: <ChartBar {...props} />,
    DIMENSION: <Cube {...props} />,
    MEMBER: <ListBullets {...props} />,
    TIME: <CalendarBlank {...props} />,
    RELATIONSHIP: <TreeStructure {...props} />,
    DATA: <ChartPieSlice {...props} />,
    PERMISSION: <ShieldWarning {...props} />,
    EXPRESSION: <Article {...props} />,
    OTHER: <DotsThreeCircle {...props} />,
  }
  return type === 'NONE' ? null : icons[type]
}

export function FeedbackDialog({ open, run, onClose, onSubmit }: FeedbackDialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [step, setStep] = useState<FeedbackStep>('ISSUE')
  const [issueType, setIssueType] = useState<Exclude<QuestionFeedbackIssueType, 'NONE'>>('METRIC')
  const [comment, setComment] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const selectedIssue = feedbackIssue(issueType)

  useEffect(() => {
    const dialog = dialogRef.current
    if (!dialog) return
    if (open && !dialog.open) {
      setStep('ISSUE')
      setIssueType('METRIC')
      setComment('')
      setError('')
      setSubmitting(false)
      dialog.showModal()
      dialog.querySelector<HTMLInputElement>('input[name="feedback-issue"]:checked')?.focus({ preventScroll: true })
    } else if (!open && dialog.open) {
      dialog.close()
    }
  }, [open])

  const close = () => {
    if (submitting) return
    dialogRef.current?.close()
    onClose()
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (step === 'ISSUE') {
      setStep('DETAIL')
      setError('')
      return
    }
    if (step !== 'DETAIL' || submitting) return
    setSubmitting(true)
    setError('')
    try {
      await onSubmit({
        runId: run.runId,
        runVersion: run.recordVersion,
        rating: 'INACCURATE',
        issueType,
        comment,
      })
      setStep('SUCCESS')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '反馈提交失败，请稍后重试。')
    } finally {
      setSubmitting(false)
    }
  }

  return <dialog
    className="ask-feedback-dialog"
    ref={dialogRef}
    aria-labelledby="ask-feedback-title"
    onCancel={event => { event.preventDefault(); close() }}
    onClose={() => { if (open) onClose() }}
  >
    {step === 'SUCCESS' ? <section className="ask-feedback-success" aria-live="polite">
      <span><CheckCircle size={42} weight="duotone" aria-hidden="true" /></span>
      <h2 id="ask-feedback-title">已收到反馈</h2>
      <p>反馈已绑定当前回答并进入人工复核，不会直接修改本次答案或生产语义。</p>
      <button className="primary-button" type="button" onClick={close}>完成</button>
    </section> : <form onSubmit={submit}>
      <header className="ask-feedback-dialog-heading">
        <div>
          <h2 id="ask-feedback-title">反馈这条回答</h2>
          <p>已绑定当前回答 · Run v{run.recordVersion}</p>
        </div>
        <button type="button" aria-label="关闭反馈" onClick={close} disabled={submitting}><X size={18} /></button>
      </header>

      <ol className="ask-feedback-steps" aria-label="反馈步骤">
        <li className={step === 'ISSUE' ? 'is-active' : 'is-complete'}><span>{step === 'DETAIL' ? <Check size={12} weight="bold" /> : '1'}</span>选择问题</li>
        <li className={step === 'DETAIL' ? 'is-active' : ''}><span>2</span>补充说明</li>
      </ol>

      {step === 'ISSUE' ? <fieldset className="ask-feedback-issue-fieldset">
        <legend>主要问题是什么？</legend>
        <div className="ask-feedback-issue-grid">
          {FEEDBACK_ISSUES.map(issue => <label key={issue.type} className={issueType === issue.type ? 'is-selected' : ''}>
            <input
              type="radio"
              name="feedback-issue"
              value={issue.type}
              checked={issueType === issue.type}
              onChange={() => setIssueType(issue.type)}
            />
            <IssueIcon type={issue.type} />
            <span>{issue.label}</span>
            {issueType === issue.type && <CheckCircle size={16} weight="fill" aria-hidden="true" />}
          </label>)}
        </div>
        <p className="ask-feedback-helper"><Info size={15} aria-hidden="true" />{selectedIssue?.helper}</p>
      </fieldset> : <section className="ask-feedback-detail-step">
        <h3>补充说明</h3>
        <div className="ask-feedback-selected-issue">
          <span><IssueIcon type={issueType} /></span>
          <div><strong>{selectedIssue?.label}</strong><small>{selectedIssue?.helper}</small></div>
          <button type="button" onClick={() => setStep('ISSUE')} disabled={submitting}>修改</button>
        </div>
        <label htmlFor="ask-feedback-comment">问题描述 <span>（可选）</span></label>
        <textarea
          id="ask-feedback-comment"
          value={comment}
          maxLength={2000}
          rows={5}
          placeholder="请补充你期望的口径、时间或结果，帮助我们更快定位问题。"
          onChange={event => setComment(event.target.value.replace(/[\r\n\t]+/g, ' '))}
          disabled={submitting}
          autoFocus
        />
        <small className="ask-feedback-counter">{[...comment].length}/2000</small>
      </section>}

      {error && <p className="ask-feedback-error" role="alert">{error}</p>}
      <p className="ask-feedback-governance"><ShieldWarning size={16} aria-hidden="true" />您的反馈将进入人工审核流程，不会直接修改当前回答或生产语义。</p>

      <footer>
        <button className="quiet-button" type="button" onClick={step === 'DETAIL' ? () => setStep('ISSUE') : close} disabled={submitting}>
          {step === 'DETAIL' ? '上一步' : '取消'}
        </button>
        <button className="primary-button" type="submit" disabled={submitting}>
          {step === 'ISSUE' ? '下一步' : submitting ? '正在提交…' : '提交反馈'}
        </button>
      </footer>
    </form>}
  </dialog>
}
