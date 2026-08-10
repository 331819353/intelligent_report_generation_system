import {
  CaretDown,
  CheckCircle,
  Pulse,
  PushPin,
  Sparkle,
} from '@phosphor-icons/react'
import type { ReleaseDrift } from '../../lib/ask-data-api'

type ReleaseDriftEvidencePanelProps = {
  question: string
  drift: ReleaseDrift
}

function pinnedAt(value: string | undefined) {
  if (!value) return '2026-08-08 10:15'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(parsed).replaceAll('/', '-')
}

export function ReleaseDriftEvidencePanel({ question, drift }: ReleaseDriftEvidencePanelProps) {
  return <>
    <header className="ask-evidence-heading">
      <div><span className="ask-live-dot" /><span className="ask-evidence-heading-copy"><strong>理解与证据驾驶舱</strong><small>答案可追溯、口径可检验</small></span></div>
      <span className="ask-trust-score">96</span>
    </header>

    <div className="ask-release-evidence-sections">
      <section className="ask-release-evidence-card">
        <header><span><Sparkle size={14} weight="fill" /></span><strong>问题理解</strong><CaretDown size={13} weight="bold" /></header>
        <dl>
          <div><dt>问题</dt><dd>{question}</dd></div>
          <div><dt>指标</dt><dd>销售额</dd></div>
          <div><dt>分析维度</dt><dd>全部</dd></div>
          <div><dt>查询意图</dt><dd>获取本月销售额的准确数值</dd></div>
        </dl>
      </section>

      <section className="ask-release-evidence-card">
        <header><span><PushPin size={14} weight="fill" /></span><strong>Release Pin</strong><CaretDown size={13} weight="bold" /></header>
        <dl>
          <div><dt>原口径（已被取代）</dt><dd>Release {drift.previous.semanticVersion}</dd></div>
          <div><dt>当前口径（当前生效）</dt><dd>Release {drift.active.semanticVersion}</dd></div>
          <div><dt>状态</dt><dd><em>待确认</em></dd></div>
          <div><dt>绑定时间</dt><dd>{pinnedAt(drift.pinnedAt)}</dd></div>
        </dl>
      </section>

      <section className="ask-release-evidence-card">
        <header><span><Pulse size={14} weight="bold" /></span><strong>变更影响</strong><CaretDown size={13} weight="bold" /></header>
        <dl>
          <div><dt>变更范围</dt><dd>{drift.changes.length} 项对象</dd></div>
          <div><dt>影响内容</dt><dd>计算逻辑、归类规则</dd></div>
          <div><dt>影响程度</dt><dd>中等</dd></div>
          <div><dt>建议操作</dt><dd>确认后重新分析</dd></div>
        </dl>
        <p><CheckCircle size={12} weight="fill" />领域仍固定为“企业经营”，仅业务口径版本发生变化</p>
      </section>
    </div>
  </>
}
