import {
  ArrowClockwise,
  CheckCircle,
  Clock,
  Funnel,
  Lightbulb,
  SealCheck,
  ShieldCheck,
  Ticket,
  WarningCircle,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useState } from 'react'
import { AppButton } from '../AppButton'
import { SealedSetIntegrityPanel } from './SealedSetIntegrityPanel'
import { currentSubject } from '../../lib/auth'
import { semanticAPI, type EvaluationSetCatalogItem, type ReleaseCatalogItem } from '../../lib/semantic'
import {
  semanticOperationsAPI,
  type ActiveLearningCandidate,
  type FeedbackEvent,
  type FeedbackMetrics,
  type FeedbackSeverity,
  type FeedbackStage,
  type FeedbackTicket,
  type FeedbackTicketStatus,
} from '../../lib/semantic-operations'

type Props = {
  releases: ReleaseCatalogItem[]
  evaluationSets: EvaluationSetCatalogItem[]
  initialTicketId?: string
  onNotice: (tone: 'success' | 'error', message: string) => void
}

const statusLabels: Record<FeedbackTicketStatus, string> = {
  NEW: '待分诊', TRIAGED: '已分诊', ACCEPTED: '已受理', REJECTED: '不采纳', FIX_PROPOSED: '修复方案待审',
  FIX_APPROVED: '方案已批准', IN_RELEASE: '已进入发布', VERIFIED: '回归已验证', CLOSED: '已关闭',
}
const stageLabels: Record<FeedbackStage, string> = {
  UNDERSTANDING: '问题理解', RETRIEVAL: '语义检索', BINDING: '对象绑定', GRAPH: '关系图谱',
  COMPILE: '查询编译', EXECUTION: '数据执行', DATA: '结果数据', NARRATIVE: '答案叙述',
}
const issueLabels: Record<string, string> = {
  METRIC: '指标口径', DIMENSION: '维度识别', MEMBER: '成员匹配', TIME: '时间范围', COMPARISON: '对比逻辑',
  RESULT: '结果数据', NARRATIVE: '答案叙述', UNDERSTANDING: '问题理解', PERMISSION: '权限范围', OTHER: '其他问题',
}
const taskLabels: Record<string, string> = {
  UNRESOLVED_EXPRESSION: '未解析业务表达', FREQUENT_CLARIFICATION: '高频澄清模式', CONFUSABLE_METRIC: '易混淆指标',
  CONFUSABLE_MEMBER: '易混淆维度成员', RETRIEVAL_MISS: '语义检索缺口', REPORT_METRIC_COMBINATION: '报告指标组合',
  FEEDBACK_CLUSTER: '同类反馈聚类', DATA_REQUEST_CLUSTER: '高频取数需求',
}

function formatTime(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}

function candidateFixType(candidate: ActiveLearningCandidate) {
  const direct: Record<string, string> = { BUSINESS_TERM: 'BUSINESS_TERM', CERTIFIED_EXAMPLE: 'CERTIFIED_EXAMPLE' }
  if (direct[candidate.candidateType]) return direct[candidate.candidateType]
  if (candidate.taskType === 'CONFUSABLE_METRIC' || candidate.taskType === 'REPORT_METRIC_COMBINATION') return 'METRIC'
  if (candidate.taskType === 'CONFUSABLE_MEMBER') return 'MEMBER_ALIAS'
  if (candidate.taskType === 'FREQUENT_CLARIFICATION' || candidate.taskType === 'FEEDBACK_CLUSTER') return 'EVALUATION_CASE'
  return 'BUSINESS_TERM'
}

function nextAction(ticket: FeedbackTicket) {
  switch (ticket.status) {
    case 'NEW': return '完成分诊并指派给我'
    case 'TRIAGED': return '接受并进入修复'
    case 'ACCEPTED': return '提交修复候选'
    case 'FIX_PROPOSED': return '批准修复方案'
    case 'FIX_APPROVED': return '关联语义 Release'
    case 'IN_RELEASE': return '记录回归验证'
    case 'VERIFIED': return '关闭反馈闭环'
    default: return ''
  }
}

export function SemanticOperationsPanel({ releases, evaluationSets, initialTicketId = '', onNotice }: Props) {
  const [tickets, setTickets] = useState<FeedbackTicket[]>([])
  const [candidates, setCandidates] = useState<ActiveLearningCandidate[]>([])
  const [metrics, setMetrics] = useState<FeedbackMetrics>({ total: 0, rejected: 0, closed: 0, overdue: 0, closureRate: 0 })
  const [selectedId, setSelectedId] = useState(initialTicketId)
  const [events, setEvents] = useState<FeedbackEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [statusFilter, setStatusFilter] = useState<'ACTIVE' | 'CLOSED' | 'ALL'>('ACTIVE')
  const [severity, setSeverity] = useState<FeedbackSeverity>('P1')
  const [stage, setStage] = useState<FeedbackStage>('UNDERSTANDING')
  const [note, setNote] = useState('')
  const [candidateId, setCandidateId] = useState('')
  const [releaseId, setReleaseId] = useState('')
  const [evaluationCaseId, setEvaluationCaseId] = useState('')
  const [evaluationCases, setEvaluationCases] = useState<Array<{ id: string; label: string }>>([])

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const [ticketPage, nextMetrics, candidatePage] = await Promise.all([
        semanticOperationsAPI.tickets(), semanticOperationsAPI.metrics(), semanticOperationsAPI.candidates(),
      ])
      setTickets(ticketPage.items ?? [])
      setMetrics(nextMetrics)
      setCandidates(candidatePage.items ?? [])
      setSelectedId(current => current && ticketPage.items.some(item => item.id === current) ? current : ticketPage.items[0]?.id ?? '')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '语义质量运营数据加载失败')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() }, 0)
    return () => window.clearTimeout(timer)
  }, [load])

  const selected = tickets.find(item => item.id === selectedId)
  useEffect(() => {
    if (!selectedId) return undefined
    let cancelled = false
    void semanticOperationsAPI.ticket(selectedId).then(result => {
      if (!cancelled) { setEvents(result.events ?? []); setSeverity(result.ticket.severity); setStage(result.ticket.attributedStage || result.ticket.suggestedStage); setNote('') }
    }).catch(cause => { if (!cancelled) setError(cause instanceof Error ? cause.message : '反馈工单详情加载失败') })
    return () => { cancelled = true }
  }, [selectedId])

  useEffect(() => {
    if (selected?.status !== 'IN_RELEASE') return
    let cancelled = false
    const drafts = evaluationSets.filter(item => item.status === 'DRAFT')
    void Promise.allSettled(drafts.map(item => semanticAPI.evaluationCases(item.id, 100, 0))).then(results => {
      if (cancelled) return
      setEvaluationCases(results.flatMap((result, index) => result.status === 'fulfilled'
        ? result.value.items.map(item => ({ id: item.id, label: `${drafts[index].name} · ${item.caseKey}` })) : []))
    })
    return () => { cancelled = true }
  }, [evaluationSets, selected?.status])

  const approvedCandidates = candidates.filter(item => item.reviewStatus === 'APPROVED')
  const usableReleases = releases.filter(item => ['READY', 'ACTIVE', 'SUPERSEDED'].includes(item.status))
  const visibleTickets = tickets.filter(item => statusFilter === 'ALL' || (statusFilter === 'CLOSED'
    ? item.status === 'CLOSED' || item.status === 'REJECTED' : item.status !== 'CLOSED' && item.status !== 'REJECTED'))
  const pendingCandidates = candidates.filter(item => item.reviewStatus === 'PENDING')
  const closureRate = Math.round((metrics.closureRate || 0) * 100)

  const refreshTicket = async (id: string) => {
    const detail = await semanticOperationsAPI.ticket(id)
    setTickets(current => current.map(item => item.id === id ? detail.ticket : item))
    setEvents(detail.events ?? [])
    return detail.ticket
  }

  const transition = async () => {
    if (!selected) return
    setBusy(`ticket:${selected.id}`); setError('')
    try {
      let input: Parameters<typeof semanticOperationsAPI.transition>[1]
      switch (selected.status) {
        case 'NEW': input = { ExpectedVersion: selected.recordVersion, TargetStatus: 'TRIAGED', OwnerUserID: currentSubject(), Severity: severity, AttributedStage: stage }; break
        case 'TRIAGED': input = { ExpectedVersion: selected.recordVersion, TargetStatus: 'ACCEPTED' }; break
        case 'ACCEPTED': {
          const candidate = approvedCandidates.find(item => item.id === candidateId)
          if (!candidate) throw new Error('请先在改进候选中批准一项方案')
          input = { ExpectedVersion: selected.recordVersion, TargetStatus: 'FIX_PROPOSED', FixCandidateType: candidateFixType(candidate), FixCandidateID: candidate.id }
          break
        }
        case 'FIX_PROPOSED': input = { ExpectedVersion: selected.recordVersion, TargetStatus: 'FIX_APPROVED' }; break
        case 'FIX_APPROVED':
          if (!releaseId) throw new Error('请选择已完成投影的语义 Release')
          input = { ExpectedVersion: selected.recordVersion, TargetStatus: 'IN_RELEASE', LinkedReleaseID: releaseId }; break
        case 'IN_RELEASE':
          if (!evaluationCaseId) throw new Error('请先在发布中心建立并复核回归用例')
          input = { ExpectedVersion: selected.recordVersion, TargetStatus: 'VERIFIED', LinkedEvaluationCaseID: evaluationCaseId, ResolutionNote: note.trim() }; break
        case 'VERIFIED': input = { ExpectedVersion: selected.recordVersion, TargetStatus: 'CLOSED', ResolutionNote: note.trim(), UserResponse: note.trim() }; break
        default: return
      }
      const updated = await semanticOperationsAPI.transition(selected.id, input)
      await refreshTicket(updated.id)
      setNote(''); onNotice('success', `反馈工单已更新为“${statusLabels[updated.status]}”`)
      const nextMetrics = await semanticOperationsAPI.metrics(); setMetrics(nextMetrics)
    } catch (cause) { onNotice('error', cause instanceof Error ? cause.message : '反馈工单更新失败') }
    finally { setBusy('') }
  }

  const rejectTicket = async () => {
    if (!selected || selected.status !== 'TRIAGED' || note.trim().length < 4) return
    setBusy(`ticket:${selected.id}`)
    try {
      const updated = await semanticOperationsAPI.transition(selected.id, { ExpectedVersion: selected.recordVersion, TargetStatus: 'REJECTED', ResolutionNote: note.trim(), UserResponse: note.trim() })
      await refreshTicket(updated.id); setNote(''); onNotice('success', '反馈已说明原因并完成退回')
    } catch (cause) { onNotice('error', cause instanceof Error ? cause.message : '反馈退回失败') }
    finally { setBusy('') }
  }

  const reviewCandidate = async (candidate: ActiveLearningCandidate, decision: 'APPROVED' | 'REJECTED') => {
    setBusy(`candidate:${candidate.id}`)
    try {
      const updated = await semanticOperationsAPI.reviewCandidate(candidate.id, decision)
      setCandidates(current => current.map(item => item.id === updated.id ? updated : item))
      onNotice('success', decision === 'APPROVED' ? '改进候选已批准，可用于反馈修复方案' : '改进候选已拒绝并进入抑制期')
    } catch (cause) { onNotice('error', cause instanceof Error ? cause.message : '改进候选审核失败') }
    finally { setBusy('') }
  }

  return <div className="semantic-operations">
    <section className="semantic-ops-metrics" aria-label="语义质量运营指标">
      <article><Ticket size={20} /><span><small>反馈工单</small><strong>{metrics.total}</strong></span></article>
      <article><Clock size={20} /><span><small>SLA 逾期</small><strong>{metrics.overdue}</strong></span></article>
      <article><CheckCircle size={20} /><span><small>闭环率</small><strong>{closureRate}%</strong></span></article>
      <article><Lightbulb size={20} /><span><small>改进候选</small><strong>{pendingCandidates.length}</strong></span></article>
    </section>

    <div className="semantic-ops-grid">
      <section className="semantic-ticket-queue">
        <header><div><span>用户反馈</span><h3>质量工单队列</h3></div><div><Funnel size={15} /><select aria-label="筛选反馈工单" value={statusFilter} onChange={event => setStatusFilter(event.target.value as typeof statusFilter)}><option value="ACTIVE">处理中</option><option value="CLOSED">已结束</option><option value="ALL">全部</option></select><AppButton text circle aria-label="刷新" onClick={() => void load()}><ArrowClockwise size={17} /></AppButton></div></header>
        {loading && <div className="semantic-ops-state"><Clock size={24} /><strong>正在加载质量工单</strong></div>}
        {!loading && error && <div className="semantic-ops-state is-error"><WarningCircle size={24} /><strong>{error}</strong><AppButton onClick={() => void load()}>重新加载</AppButton></div>}
        {!loading && !error && visibleTickets.length === 0 && <div className="semantic-ops-state"><CheckCircle size={26} /><strong>当前没有{statusFilter === 'ACTIVE' ? '待处理' : ''}反馈工单</strong><small>用户在问数结果中提交“不准确”反馈后会自动进入这里</small></div>}
        <div className="semantic-ticket-list">{visibleTickets.map(item => <button className={selectedId === item.id ? 'is-selected' : ''} type="button" key={item.id} onClick={() => setSelectedId(item.id)}><span className={`is-${item.severity.toLowerCase()}`}>{item.severity}</span><div><strong>{issueLabels[item.issueType] || item.issueType}</strong><small>{stageLabels[item.attributedStage || item.suggestedStage]} · {formatTime(item.updatedAt)}</small></div><em className={`is-${item.status.toLowerCase()}`}>{statusLabels[item.status]}</em></button>)}</div>
      </section>

      <section className="semantic-ticket-detail">
        {!selected && <div className="semantic-ops-state"><Ticket size={27} /><strong>选择一张反馈工单</strong><small>查看来源证据并推进受治理修复</small></div>}
        {selected && <>
          <header><div><span>反馈 #{selected.id.slice(0, 8).toUpperCase()}</span><h3>{issueLabels[selected.issueType] || selected.issueType}</h3><p>问数运行 {selected.questionRunId.slice(0, 8).toUpperCase()} · 提交 {formatTime(selected.createdAt)}</p></div><em className={`is-${selected.status.toLowerCase()}`}>{statusLabels[selected.status]}</em></header>
          <ol className="semantic-ticket-lifecycle">{(['NEW', 'TRIAGED', 'ACCEPTED', 'FIX_PROPOSED', 'FIX_APPROVED', 'IN_RELEASE', 'VERIFIED', 'CLOSED'] as FeedbackTicketStatus[]).map((status, index) => { const current = (['NEW', 'TRIAGED', 'ACCEPTED', 'FIX_PROPOSED', 'FIX_APPROVED', 'IN_RELEASE', 'VERIFIED', 'CLOSED'] as FeedbackTicketStatus[]).indexOf(selected.status); return <li className={index < current ? 'is-done' : index === current ? 'is-current' : ''} key={status}><span>{index < current ? <CheckCircle size={14} weight="fill" /> : index + 1}</span><small>{statusLabels[status]}</small></li> })}</ol>
          <div className="semantic-ticket-facts"><dl><div><dt>严重度</dt><dd>{selected.severity}</dd></div><div><dt>建议归因</dt><dd>{stageLabels[selected.suggestedStage]}</dd></div><div><dt>当前归因</dt><dd>{stageLabels[selected.attributedStage || selected.suggestedStage]}</dd></div><div><dt>SLA</dt><dd>{formatTime(selected.slaDueAt)}</dd></div></dl>{selected.linkedReleaseId && <p><SealCheck size={16} />已关联 Release {selected.linkedReleaseId.slice(0, 8).toUpperCase()}</p>}{selected.linkedEvaluationCaseId && <p><ShieldCheck size={16} />已固定回归用例 {selected.linkedEvaluationCaseId.slice(0, 8).toUpperCase()}</p>}</div>
          {!['REJECTED', 'CLOSED'].includes(selected.status) && <section className="semantic-ticket-action"><strong>{nextAction(selected)}</strong>
            {selected.status === 'NEW' && <div className="semantic-ticket-fields"><label>严重度<select value={severity} onChange={event => setSeverity(event.target.value as FeedbackSeverity)}><option value="P0">P0 · 4 小时</option><option value="P1">P1 · 1 个工作日</option><option value="P2">P2 · 3 个工作日</option></select></label><label>问题归因<select value={stage} onChange={event => setStage(event.target.value as FeedbackStage)}>{Object.entries(stageLabels).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label></div>}
            {selected.status === 'ACCEPTED' && <label>已批准改进候选<select value={candidateId} onChange={event => setCandidateId(event.target.value)}><option value="">请选择候选</option>{approvedCandidates.map(item => <option value={item.id} key={item.id}>{taskLabels[item.taskType] || item.taskType} · {item.id.slice(0, 8).toUpperCase()}</option>)}</select><small>{approvedCandidates.length ? '候选保持草稿状态，进入 Release 前仍需正常认证。' : '请先在下方“主动学习候选”中批准一项改进建议。'}</small></label>}
            {selected.status === 'FIX_APPROVED' && <label>语义 Release<select value={releaseId} onChange={event => setReleaseId(event.target.value)}><option value="">请选择 Release</option>{usableReleases.map(item => <option value={item.id} key={item.id}>{item.semanticVersion} · {item.status}</option>)}</select><small>只允许关联已完成投影或已激活的不可变 Release。</small></label>}
            {selected.status === 'IN_RELEASE' && <><label>回归用例<select value={evaluationCaseId} onChange={event => setEvaluationCaseId(event.target.value)}><option value="">请选择已进入复核集的用例</option>{evaluationCases.map(item => <option value={item.id} key={item.id}>{item.label}</option>)}</select><small>{evaluationCases.length ? '验证收据会固定该用例，关闭前不可替换。' : '发布中心暂时没有可用的草稿复核用例，请先生成评测复核集。'}</small></label><label>验证说明<textarea value={note} onChange={event => setNote(event.target.value)} placeholder="记录回归结果、口径变化和影响范围" /></label></>}
            {selected.status === 'VERIFIED' && <label>用户可见结论<textarea value={note} onChange={event => setNote(event.target.value)} placeholder="说明修复内容、验证结果与生效版本" /></label>}
            {selected.status === 'TRIAGED' && <label>处理说明（不采纳时必填）<textarea value={note} onChange={event => setNote(event.target.value)} placeholder="记录受理判断或不采纳原因" /></label>}
            <footer>{selected.status === 'TRIAGED' && <AppButton variant="danger" disabled={busy !== '' || note.trim().length < 4} onClick={() => void rejectTicket()}>不采纳并说明</AppButton>}<AppButton variant="primary" disabled={busy !== '' || selected.status === 'ACCEPTED' && !candidateId || selected.status === 'FIX_APPROVED' && !releaseId || selected.status === 'IN_RELEASE' && !evaluationCaseId || selected.status === 'VERIFIED' && note.trim().length < 4} onClick={() => void transition()}>{busy === `ticket:${selected.id}` ? '正在提交…' : nextAction(selected)}</AppButton></footer>
          </section>}
          {selected.resolutionNote && <div className="semantic-ticket-resolution"><CheckCircle size={17} /><span><strong>处理结论</strong><small>{selected.resolutionNote}</small></span></div>}
          <section className="semantic-ticket-events"><strong>审计轨迹</strong>{events.map(event => <div key={event.id}><span>{event.eventNo}</span><p><b>{event.fromStatus ? statusLabels[event.fromStatus] : '创建工单'} → {statusLabels[event.toStatus]}</b><small>{formatTime(event.createdAt)} · 操作人 {event.actorUserId.slice(0, 8).toUpperCase()}</small></p></div>)}</section>
        </>}
      </section>
    </div>

    <SealedSetIntegrityPanel evaluationSets={evaluationSets} onNotice={onNotice} />

    <section className="semantic-candidate-center">
      <header><div><span>主动学习</span><h3>语义改进候选</h3><p>系统仅挖掘哈希、稳定 ID 与聚合计数；人工批准只产生草稿候选，不会绕过认证或 Release 门禁。</p></div><strong>{pendingCandidates.length} 项待审核</strong></header>
      {candidates.length === 0 ? <div className="semantic-ops-state"><Lightbulb size={26} /><strong>当前没有改进候选</strong><small>后台任务会持续聚合真实澄清、反馈和高频取数模式</small></div> : <div className="semantic-candidate-list">{candidates.map(item => <article key={item.id}><span><Lightbulb size={19} /></span><div><strong>{taskLabels[item.taskType] || item.taskType}</strong><small>{item.candidateType} · 出现 {item.occurrenceCount} 次 · 最近 {formatTime(item.lastSeenAt)}</small><p>候选 {item.candidateKeyHash.slice(0, 16)}… · {item.representativeRunIds.length} 条受权运行证据</p></div><em className={`is-${item.reviewStatus.toLowerCase()}`}>{item.reviewStatus === 'PENDING' ? '待审核' : item.reviewStatus === 'APPROVED' ? '已批准' : '已拒绝'}</em>{item.reviewStatus === 'PENDING' && <footer><AppButton disabled={busy !== ''} onClick={() => void reviewCandidate(item, 'REJECTED')}>拒绝</AppButton><AppButton variant="primary" disabled={busy !== ''} onClick={() => void reviewCandidate(item, 'APPROVED')}>批准为草稿候选</AppButton></footer>}</article>)}</div>}
    </section>
  </div>
}
