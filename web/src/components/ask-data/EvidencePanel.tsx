import { CalendarBlank, CaretDown, CheckCircle, Pulse, ShareNetwork, ShieldCheck, Sparkle, WarningCircle } from '@phosphor-icons/react'
import { useState, type ReactNode } from 'react'
import { AnswerLayerStatus } from '../../askdata/AnswerSummary'
import { renderTimeSpec } from '../../askdata/format/timespec'
import type { ClarificationOption, QuestionAnswerPresentation, QuestionResult, QuestionRun } from '../../lib/ask-data-api'
import {
  clarificationOptionReady,
  freshnessLabel,
  qualityScoreLabel,
  qualityStatusLabel,
  semanticStatusLabel,
} from './clarification'

type EvidencePanelProps = {
  question: string
  run: QuestionRun
  option?: ClarificationOption
  result?: QuestionResult
	graphDegraded?: boolean
  answer?: QuestionAnswerPresentation
}

type SectionProps = {
	id: string
	title: string
	icon: ReactNode
	badge?: string
	badgeTone?: 'success' | 'warning'
	children: ReactNode
}

function Section({ id, title, icon, badge, badgeTone = 'success', children }: SectionProps) {
  const [open, setOpen] = useState(true)
  return <section className="ask-evidence-section">
    <button type="button" aria-expanded={open} aria-controls={`clarification-evidence-${id}`} onClick={() => setOpen(value => !value)}>
			<span className="ask-evidence-title-icon">{icon}</span><span>{title}{badge && <small className={`ask-evidence-sync-badge is-${badgeTone}`}>{badge}</small>}</span><CaretDown className={open ? 'is-open' : ''} size={14} aria-hidden="true" />
    </button>
    {open && <div id={`clarification-evidence-${id}`} className="ask-evidence-body">{children}</div>}
  </section>
}

export function EvidencePanel({ question, run, option, result, graphDegraded = false, answer }: EvidencePanelProps) {
  const resolvedOption = option ?? (result?.evidence ? {
    optionId: `result:${result.summary.metricLabel}`,
    label: result.summary.metricLabel,
    evidenceIds: result.evidenceIds,
    evidence: result.evidence,
  } : undefined)
  const ready = clarificationOptionReady(resolvedOption)
  const evidence = resolvedOption?.evidence
  const trust = evidence?.quality.scorePermillion === undefined ? '—' : (evidence.quality.scorePermillion / 10_000).toFixed(1)
  const timeSpec = result?.resolvedTimeSpec ? renderTimeSpec(result.resolvedTimeSpec) : undefined

  return <>
    <header className="ask-evidence-heading">
			<div><span className={`ask-live-dot ${ready ? '' : 'is-pending'}`.trim()} /><span className="ask-evidence-heading-copy"><strong>证据与可信度</strong><small>{ready ? result ? '答案口径与证据同步' : '所选口径与证据同步' : '等待完整受控证据'}</small></span>{graphDegraded && <span className="ask-graph-degraded-badge"><ShareNetwork size={10} weight="bold" aria-hidden="true" />关系校验已降级</span>}</div>
      <span className={`ask-trust-score ${ready ? '' : 'is-pending'}`.trim()}>{trust}</span>
    </header>
    {!ready || !resolvedOption ? <div className="ask-live-evidence-state" role="status">
      <span><ShieldCheck size={22} weight="duotone" aria-hidden="true" /></span>
      <strong>该候选证据不完整</strong>
      <p>Owner、版本、实际时间与质量信息全部通过公共合同后才能继续。</p>
    </div> : !evidence ? <div className="ask-live-evidence-state" role="status">
      <span><CheckCircle size={22} weight="duotone" aria-hidden="true" /></span>
      <strong>选择已受治理证据约束</strong>
      <p>已关联 {resolvedOption.evidenceIds.length} 项不可变证据；提交后将展示最终指标、时间与质量口径。</p>
    </div> : <div className="ask-evidence-sections ask-clarification-evidence-sections">
      <Section id="intent" title="问题理解" icon={<Sparkle size={14} weight="fill" />}>
        <dl className="ask-evidence-grid ask-evidence-grid-single">
          <div><dt>问题</dt><dd>{question}</dd></div>
          <div><dt>所选指标</dt><dd>{resolvedOption.label}</dd></div>
          <div className="is-wide"><dt>查询目标</dt><dd>{result ? '展示已核验数值、趋势与明细' : '获取所选口径的准确数值'}</dd></div>
        </dl>
      </Section>
			{graphDegraded && <Section id="relationship" title="关系校验" badge="降级" badgeTone="warning" icon={<ShareNetwork size={14} weight="bold" />}>
				<p className="ask-graph-degraded-note"><WarningCircle size={13} weight="fill" aria-hidden="true" />图服务不可用，已使用认证降级路径校验。</p>
				<dl className="ask-evidence-grid ask-graph-degraded-grid">
					<div><dt>校验来源</dt><dd>PostgreSQL 注册表</dd></div>
					<div><dt>关系范围</dt><dd>已认证 · 最多 1 hop</dd></div>
					<div><dt>Fanout</dt><dd>仅 SAFE</dd></div>
					<div><dt>结果状态</dt><dd>已回答</dd></div>
				</dl>
				<details className="ask-graph-degraded-details">
					<summary>查看降级原因</summary>
					<p>Nebula 图服务暂不可用；系统仅对单模型或唯一已认证的 1-hop SAFE 关系执行回退，未猜测 Join。</p>
				</details>
			</Section>}
      {answer && <Section id="answer-layers" title="答案层级" badge={answer.narrativeDegraded ? '仅结构化' : '完整'} badgeTone={answer.narrativeDegraded ? 'warning' : 'success'} icon={<ShieldCheck size={14} weight="fill" />}>
        <AnswerLayerStatus answer={answer} />
        {answer.narrativeDegraded && <p className="ask-answer-layer-note">未通过校验的文字未进入答案。</p>}
      </Section>}
      <Section id="metric" title="已选口径" icon={<ShieldCheck size={14} weight="fill" />}>
        <div className="ask-source-card">
          <span className="ask-source-icon blue"><CheckCircle size={14} weight="fill" /></span>
          <span><strong>{resolvedOption.label}</strong><small>{evidence.owner.displayName} · {evidence.semanticVersion}</small></span>
          <span className="ask-source-status">{semanticStatusLabel(evidence.semanticStatus)}</span>
        </div>
        <p className="ask-evidence-definition">{evidence.definition}</p>
        <dl className="ask-evidence-grid">
          <div><dt>Owner</dt><dd>{evidence.owner.displayName}</dd></div>
          <div><dt>语义版本</dt><dd>{evidence.semanticVersion}</dd></div>
          <div><dt>证据引用</dt><dd>{resolvedOption.evidenceIds.length} 项</dd></div>
          <div><dt>Run 版本</dt><dd>v{run.recordVersion}</dd></div>
        </dl>
      </Section>
      <Section id="time" title="时间范围" badge={timeSpec ? '与结果一致' : undefined} icon={<CalendarBlank size={14} weight="fill" />}>
        {timeSpec && result ? <dl className="ask-evidence-grid">
          <div><dt>时间口径</dt><dd>{timeSpec.policyLabel}</dd></div>
          <div><dt>业务时区</dt><dd>{result.resolvedTimeSpec?.timezone}</dd></div>
          <div className="is-wide"><dt>实际区间</dt><dd>{timeSpec.rangeLabel}</dd></div>
          <div className="is-wide"><dt>数据截止</dt><dd>{timeSpec.asOfLabel}</dd></div>
        </dl> : <dl className="ask-evidence-grid">
          <div><dt>时间策略</dt><dd>{evidence.time.label}</dd></div>
          <div><dt>业务时区</dt><dd>{evidence.time.timezone}</dd></div>
          <div className="is-wide"><dt>实际区间</dt><dd>{evidence.time.start} 至 {evidence.time.end}</dd></div>
        </dl>}
      </Section>
      <Section id="quality" title="质量与新鲜度" icon={<Pulse size={14} weight="bold" />}>
        <div className="ask-quality-score"><strong>{qualityScoreLabel(resolvedOption)}</strong><span>质量分<small>{qualityStatusLabel(evidence.quality.status)} · {evidence.quality.rulesPassed}/{evidence.quality.rulesTotal} 规则</small></span></div>
        <dl className="ask-freshness-list"><div><dt>数据新鲜度</dt><dd>{freshnessLabel(resolvedOption)}</dd></div><div><dt>质量状态</dt><dd>{qualityStatusLabel(evidence.quality.status)}</dd></div></dl>
      </Section>
    </div>}
  </>
}
