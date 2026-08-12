import { ArrowRight, CheckCircle, Info, LockKey, WarningCircle } from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'
import type { AskDataClientError, ClarificationOption, QuestionRun } from '../../lib/ask-data-api'
import {
  clarificationOptionReady,
  freshnessLabel,
  qualityScoreLabel,
  semanticStatusLabel,
  timeRangeLabel,
} from './clarification'

type ClarificationCardProps = {
  run: QuestionRun
  submitting?: boolean
  error?: AskDataClientError
  onSubmit: (optionId: string) => void
  onCancel: () => void
  onSelectionChange?: (option: ClarificationOption) => void
}

export function ClarificationCard({
  run,
  submitting = false,
  error,
  onSubmit,
  onCancel,
  onSelectionChange,
}: ClarificationCardProps) {
  const clarification = run.completion?.clarification
  const options = useMemo(() => clarification?.options ?? [], [clarification?.options])
  const [selectedID, setSelectedID] = useState(options[0]?.optionId ?? '')
  const selected = options.find(option => option.optionId === selectedID) ?? options[0]
  const ready = clarificationOptionReady(selected)

  useEffect(() => {
    if (selected) onSelectionChange?.(selected)
  }, [onSelectionChange, selected])

  const choose = (option: ClarificationOption) => {
    setSelectedID(option.optionId)
    onSelectionChange?.(option)
  }

  const conflictLabel = clarification?.conflictCode === 'TIME_RANGE_MISSING'
    ? '时间范围'
    : clarification?.conflictCode === 'DIMENSION_ROLE_AMBIGUOUS'
      ? '维度'
      : '指标口径'

  return (
    <section className="ask-clarification-card" aria-labelledby="ask-clarification-title">
      <header>
        <span>
          <strong id="ask-clarification-title">需要确认{conflictLabel}</strong>
          <Info size={13} weight="duotone" aria-hidden="true" />
        </span>
        <small>{clarification?.message || '检测到多个可用口径，请选择本次要使用的口径。'}</small>
      </header>

      <fieldset className="ask-clarification-options" disabled={submitting}>
        <legend className="sr-only">选择本次问题使用的业务口径</legend>
        {options.map((option, index) => {
          const evidence = option.evidence
          const optionReady = clarificationOptionReady(option)
          return <div className="ask-clarification-option-wrap" key={option.optionId}>
            <label className={`ask-clarification-option ${selected?.optionId === option.optionId ? 'is-selected' : ''} ${optionReady ? '' : 'is-incomplete'}`.trim()}>
              <input
                type="radio"
                name={`clarification-${clarification?.clarificationId}`}
                value={option.optionId}
                checked={selected?.optionId === option.optionId}
                onChange={() => choose(option)}
              />
              <span className="ask-clarification-option-content">
                <span className="ask-clarification-option-title">
                  <strong>{option.label}</strong>
                  <small className={optionReady ? '' : 'is-warning'}>{optionReady ? evidence ? semanticStatusLabel(evidence.semanticStatus) : '可选择' : '证据不完整'}</small>
                </span>
                {evidence ? <dl className="ask-clarification-option-grid">
                  <div className="is-wide"><dt>定义</dt><dd>{evidence.definition}</dd></div>
                  <div><dt>Owner</dt><dd>{evidence.owner.displayName}</dd></div>
                  <div><dt>版本</dt><dd>{evidence.semanticVersion}</dd></div>
                  <div className="is-wide"><dt>时间范围</dt><dd>{timeRangeLabel(option)}</dd></div>
                  <div><dt>质量评分</dt><dd>{qualityScoreLabel(option)}</dd></div>
                  <div><dt>数据新鲜度</dt><dd>{freshnessLabel(option)}</dd></div>
                </dl> : <p className="ask-clarification-missing"><CheckCircle size={13} weight="fill" aria-hidden="true" />已关联 {option.evidenceIds.length} 项治理证据，提交后将继续校验完整口径。</p>}
              </span>
            </label>
            {index === 0 && option.difference && <p className="ask-clarification-difference"><Info size={12} weight="fill" aria-hidden="true" /><strong>差异：</strong>{option.difference}</p>}
          </div>
        })}
      </fieldset>

      {error && <p className="ask-clarification-error" role="alert"><WarningCircle size={13} weight="fill" aria-hidden="true" />{error.message}</p>}

      <footer>
        <div className="ask-clarification-actions">
          <button className="primary-button" type="button" disabled={!ready || submitting} onClick={() => selected && onSubmit(selected.optionId)}>
            {submitting ? '正在提交…' : '按此口径继续'}<ArrowRight size={14} weight="bold" aria-hidden="true" />
          </button>
          <button type="button" disabled={submitting} onClick={onCancel}>取消本次问题</button>
        </div>
        <p><LockKey size={13} aria-hidden="true" />提交时将校验 Run v{run.recordVersion}，避免重复选择</p>
      </footer>
    </section>
  )
}
