import { CheckCircle, ShieldCheck, WarningCircle, X } from '@phosphor-icons/react'
import { useEffect, useState, type FormEvent } from 'react'
import { RequestError } from '../../lib/api'
import { currentSubject } from '../../lib/auth'
import {
  decisionAPI,
  type DecisionApprovalPolicy,
  type DecisionEvidence,
  type DecisionEvidenceInput,
} from '../../lib/decision-api'

type DecisionSource = {
  type: DecisionEvidence['sourceType']
  id: string
  label: string
  title?: string
  question?: string
  decision?: string
  expectedEffect?: string
}

type Props = {
  open: boolean
  source: DecisionSource
  onClose: () => void
  onCreated: (decisionId: string) => void
}

const reviewDate = () => new Date(Date.now() + 30 * 86400000).toISOString().slice(0, 10)

function decisionError(cause: unknown) {
  if (cause instanceof RequestError) {
    if (cause.detail.code === 'DECISION_FORBIDDEN') return '当前账号不是该领域的业务成员，或审批流程暂不可用。请在权限管理中完成领域成员与审批人配置。'
    if (/evidence|证据/i.test(cause.message)) return import.meta.env.DEV
      ? `当前来源还不能形成可审计证据：${cause.message}`
      : '当前来源还不能形成可审计证据。请刷新来源数据或确认该报告已有可用发布版本。'
    return cause.message
  }
  return cause instanceof Error ? cause.message : '决策创建失败，请稍后重试'
}

export function CreateDecisionDialog({ open, source, onClose, onCreated }: Props) {
  const [policies, setPolicies] = useState<DecisionApprovalPolicy[]>([])
  const [evidence, setEvidence] = useState<DecisionEvidenceInput | null>(null)
  const [title, setTitle] = useState(source.title ?? '')
  const [question, setQuestion] = useState(source.question ?? '')
  const [decision, setDecision] = useState(source.decision ?? '')
  const [expectedEffect, setExpectedEffect] = useState(source.expectedEffect ?? '')
  const [risks, setRisks] = useState('')
  const [policyId, setPolicyId] = useState('')
  const [date, setDate] = useState(reviewDate)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return undefined
    let cancelled = false
    void Promise.all([decisionAPI.listApprovalPolicies(), decisionAPI.prefillEvidence(source.type, source.id)])
      .then(([items, nextEvidence]) => {
        if (cancelled) return
        setPolicies(items); setPolicyId(items[0]?.id ?? ''); setEvidence(nextEvidence)
        if (!items.length) setError('当前领域没有可用的审批流程，请先在权限管理中配置审批人。')
      })
      .catch(cause => { if (!cancelled) setError(decisionError(cause)) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [open, source.decision, source.expectedEffect, source.id, source.question, source.title, source.type])

  if (!open) return null

  const create = async (event: FormEvent) => {
    event.preventDefault()
    const actorId = currentSubject()
    if (!actorId || !evidence || !policyId || !title.trim() || !question.trim() || !date) {
      setError('证据、标题、决策问题、复盘日期和审批流程均为必填项。')
      return
    }
    setBusy(true); setError('')
    try {
      const aggregate = await decisionAPI.create({
        ownerUserId: actorId,
        title: title.trim(), question: question.trim(), decision: decision.trim(), expectedEffect: expectedEffect.trim(),
        risks: risks.split('\n').map(item => item.trim()).filter(Boolean), evidenceMode: 'PLATFORM_VERIFIED',
        approvalPolicyId: policyId, reviewAt: `${date}T10:00:00+08:00`,
        options: decision.trim() ? [{ title: '基于已验证证据的建议方案', description: decision.trim(), selected: true }] : [],
        evidence: [evidence],
      })
      onCreated(aggregate.decision.id)
    } catch (cause) {
      setError(decisionError(cause))
    } finally { setBusy(false) }
  }

  return <div className="decision-create-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !busy) onClose() }}>
    <form className="decision-create-dialog decision-source-dialog" role="dialog" aria-modal="true" aria-labelledby="source-decision-title" onSubmit={create}>
      <header><div><span>从已验证结果形成决策</span><h2 id="source-decision-title">固定证据并创建决策草稿</h2></div><button type="button" disabled={busy} aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      <div className="decision-create-body">
        <p className="decision-create-note is-verified"><ShieldCheck size={18} weight="fill" />来源：{source.label}。平台将固定不可变制品、语义版本、数据时点与当前权限范围，后续可完整审计。</p>
        {loading && <div className="decision-source-loading"><span className="home-loading-dot" /><strong>正在校验证据与审批流程</strong></div>}
        {!loading && evidence && <div className="decision-source-evidence"><CheckCircle size={19} weight="fill" /><div><strong>平台证据已校验</strong><span>数据截至 {new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(evidence.asOf))}</span></div></div>}
        <label><span>决策标题 *</span><input autoFocus value={title} onChange={event => setTitle(event.target.value)} maxLength={256} placeholder="概括这项需要推进的经营决策" /></label>
        <label><span>需要决策的问题 *</span><textarea value={question} onChange={event => setQuestion(event.target.value)} maxLength={4096} placeholder="说明需要做出决定的业务问题" /></label>
        <div className="decision-create-grid"><label><span>审批流程 *</span><select value={policyId} onChange={event => setPolicyId(event.target.value)} disabled={loading}>{policies.map(policy => <option key={policy.id} value={policy.id}>{policy.name} · {policy.approverSummary}</option>)}</select></label><label><span>计划复盘日期 *</span><input type="date" min={new Date().toISOString().slice(0, 10)} value={date} onChange={event => setDate(event.target.value)} /></label></div>
        <label><span>拟定决策</span><textarea value={decision} onChange={event => setDecision(event.target.value)} maxLength={8192} placeholder="可先形成草稿，提交审批前继续完善" /></label>
        <label><span>预期效果</span><textarea value={expectedEffect} onChange={event => setExpectedEffect(event.target.value)} maxLength={4096} placeholder="描述预期改善的业务结果" /></label>
        <label><span>风险（每行一项）</span><textarea value={risks} onChange={event => setRisks(event.target.value)} placeholder="例如：执行节奏与跨团队资源协调风险" /></label>
        {error && <p className="decision-create-error" role="alert"><WarningCircle size={16} />{error}</p>}
      </div>
      <footer><button type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary" type="submit" disabled={busy || loading || !evidence || !policies.length}>{busy ? '正在固定证据…' : '创建决策草稿'}</button></footer>
    </form>
  </div>
}
