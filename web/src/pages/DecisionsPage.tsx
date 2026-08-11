import {
  ArrowClockwise,
  BookmarkSimple,
  CalendarBlank,
  CaretDown,
  CaretLeft,
  CaretRight,
  CheckCircle,
  ClipboardText,
  Clock,
  FileText,
  Flag,
  MagnifyingGlass,
  Path,
  Plus,
  ShieldCheck,
  User,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import { currentSubject } from '../lib/auth'
import {
  decisionAPI,
  type DecisionAggregate,
  type DecisionRecord,
  type DecisionScope,
  type DecisionStatus,
} from '../lib/decision-api'
import {
  actionProgress,
  decisionEvidenceMeta,
  decisionOwnerLabel,
  decisionStatusMeta,
  filterDecisions,
  formatDecisionDate,
  urgentDecisions,
} from '../lib/decision-data'
import { currentDomain } from '../lib/domain-context'

type DecisionVisual = DecisionRecord & {
  ownerDisplayName?: string
  ownerDepartment?: string
  ownerAvatar?: string
  evidenceCount?: number
  actionPercent?: number
  actionLabel?: string
}

type ScopeState = Record<DecisionScope, DecisionVisual[]>

const scopeTabs: Array<{ key: DecisionScope; label: string }> = [
  { key: 'MINE', label: '我发起' },
  { key: 'APPROVALS', label: '待我审批' },
  { key: 'ACTIONS', label: '我负责行动' },
  { key: 'REVIEWS', label: '待我复盘' },
]

const snapshotOwner = '00000000-0000-4000-8000-000000000001'

function snapshotDecision(index: number, patch: Partial<DecisionVisual>): DecisionVisual {
  return {
    schemaVersion: '1.0',
    id: `10000000-0000-4000-8000-${String(index).padStart(12, '0')}`,
    ownerUserId: snapshotOwner,
    createdBy: snapshotOwner,
    title: '经营决策',
    question: '当前经营问题应该如何处理？',
    decision: '按已确认方案推进，并在复盘日检查结果。',
    expectedEffect: '改善关键经营指标并降低执行风险。',
    risks: ['跨团队资源协调可能影响行动进度'],
    status: 'IN_EXECUTION',
    evidenceMode: 'PLATFORM_VERIFIED',
    approvalPolicyId: 'enterprise-operation-standard',
    requiredApprovals: 1,
    reviewAt: '2026-08-31T10:00:00+08:00',
    recordVersion: 3,
    createdAt: '2026-07-20T09:00:00+08:00',
    updatedAt: '2026-08-10T09:30:00+08:00',
    ownerDisplayName: '王敏',
    ownerDepartment: '企业经营部',
    ownerAvatar: '/report-assets/avatars/wang-min.png',
    evidenceCount: 2,
    actionPercent: 0,
    actionLabel: '—',
    ...patch,
  }
}

const snapshotMine: DecisionVisual[] = [
  snapshotDecision(1, { title: '渠道政策调整专项方案', question: '动态调整渠道政策以提升终端覆盖与增量', status: 'IN_REVIEW', reviewAt: '2026-08-14T10:00:00+08:00', actionLabel: '—', ownerDisplayName: '王敏', ownerDepartment: '企业经营部', evidenceCount: 2 }),
  snapshotDecision(2, { title: '区域销售目标纠偏行动', question: '针对华东区域进度偏差制定纠偏行动方案', reviewAt: '2026-08-31T10:00:00+08:00', actionPercent: 65, actionLabel: '进行中', ownerDisplayName: '李强', ownerDepartment: '销售运营部', ownerAvatar: '/report-assets/avatars/chen-chen.png', evidenceCount: 3 }),
  snapshotDecision(3, { title: '库存周转改善计划', question: '优化库存结构，提升周转效率', status: 'REVIEW_DUE', reviewAt: '2026-08-12T10:00:00+08:00', actionPercent: 100, actionLabel: '已完成', ownerDisplayName: '张磊', ownerDepartment: '供应链管理部', ownerAvatar: '/report-assets/avatars/liu-yang.png', evidenceCount: 2 }),
  snapshotDecision(4, { title: '新品上市资源投放决策', question: '关于 X5 系列新品上市资源投放的决策', status: 'DRAFT', evidenceMode: 'MANUAL_WITHOUT_PLATFORM_EVIDENCE', reviewAt: '', actionLabel: '—', evidenceCount: 0 }),
  snapshotDecision(5, { title: '第3季度价格策略优化方案', question: '优化产品价格体系以提升利润率', status: 'CLOSED', reviewAt: '2026-07-28T10:00:00+08:00', actionPercent: 100, actionLabel: '已完成', ownerDisplayName: '刘洋', ownerDepartment: '产品管理部', evidenceCount: 4 }),
  snapshotDecision(6, { title: '渠道返利执行规范修订决策', question: '修订渠道返利执行规范，提升执行一致性', status: 'CLOSED', reviewAt: '2026-07-20T10:00:00+08:00', actionPercent: 100, actionLabel: '已完成', ownerDisplayName: '陈晨', ownerDepartment: '销售运营部', ownerAvatar: '/report-assets/avatars/chen-chen.png', evidenceCount: 3 }),
  snapshotDecision(7, { title: '服务体系升级行动方案', question: '完善服务网络与能力，提升客户满意度', reviewAt: '2026-09-15T10:00:00+08:00', actionPercent: 40, actionLabel: '进行中', ownerDisplayName: '赵敏', ownerDepartment: '服务管理部', ownerAvatar: '/report-assets/avatars/liu-yang.png', evidenceCount: 2 }),
  snapshotDecision(8, { title: '数字化营销平台建设决策', question: '建设统一营销中台，提升运营效率', status: 'IN_REVIEW', evidenceMode: 'MANUAL_WITHOUT_PLATFORM_EVIDENCE', reviewAt: '2026-08-21T10:00:00+08:00', actionLabel: '—', ownerDisplayName: '孙伟', ownerDepartment: '数字化推进部', ownerAvatar: '/report-assets/avatars/chen-chen.png', evidenceCount: 0 }),
]

const snapshotScopes: ScopeState = {
  MINE: snapshotMine,
  APPROVALS: snapshotMine.slice(0, 6),
  ACTIONS: [...snapshotMine.slice(1, 8), snapshotDecision(9, { title: '渠道费用管控行动', actionPercent: 25, actionLabel: '进行中' }), snapshotDecision(10, { title: '经营指标归因补充', actionPercent: 0, actionLabel: '待开始' })],
  REVIEWS: [snapshotMine[2], snapshotMine[4], snapshotMine[5]],
}

function snapshotAggregate(record: DecisionVisual): DecisionAggregate {
  const evidenceCount = record.evidenceCount ?? 0
  const actionTotal = record.actionPercent === 100 ? 3 : record.actionPercent ? 4 : 0
  const actionDone = Math.round(actionTotal * (record.actionPercent ?? 0) / 100)
  return {
    decision: record,
    options: [
      { id: `${record.id}-option-1`, title: '推荐方案', description: record.decision, selected: true },
      { id: `${record.id}-option-2`, title: '维持现状', description: '保持当前策略，继续观察一个周期。', selected: false },
    ],
    evidence: Array.from({ length: evidenceCount }, (_, index) => ({
      schemaVersion: '1.0', id: `${record.id}-evidence-${index}`, sourceType: index % 2 ? 'REPORT_VERSION' : 'ANSWER_ARTIFACT', sourceId: record.id, summary: index % 2 ? '经营分析报告已发布版本' : '已验证问数分析结论', verified: true, asOf: '2026-08-09T18:00:00+08:00',
    })),
    approvals: record.status === 'DRAFT' ? [] : [{ id: `${record.id}-approval`, approverUserId: '00000000-0000-4000-8000-000000000002', sequenceNo: 1, status: record.status === 'IN_REVIEW' ? 'PENDING' : 'APPROVED', comment: '', decidedAt: record.status === 'IN_REVIEW' ? undefined : '2026-08-05T10:00:00+08:00' }],
    actions: Array.from({ length: actionTotal }, (_, index) => ({
      schemaVersion: '1.0', id: `${record.id}-action-${index}`, decisionId: record.id, title: `行动项 ${index + 1}`, description: '按决策计划完成责任事项', assigneeUserId: record.ownerUserId, dueAt: record.reviewAt, status: index < actionDone ? 'DONE' : 'DOING', deliverableRefs: [], recordVersion: 1, createdAt: record.createdAt, updatedAt: record.updatedAt,
    })),
    outcomeMetrics: [],
  }
}

const emptyScopes = (): ScopeState => ({ MINE: [], APPROVALS: [], ACTIONS: [], REVIEWS: [] })

const initialCreate = () => ({
  title: '', question: '', decision: '', expectedEffect: '', risks: '', approvalPolicyId: '', optionTitle: '', reviewDate: new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10),
})

function loadError(cause: unknown) {
  if (cause instanceof RequestError) return cause.message
  return cause instanceof Error ? cause.message : '请求失败，请稍后重试'
}

/** 决策组合页使用受权决策读模型；设计快照仅在显式开发参数下启用。 */
export function DecisionsPage() {
  const navigate = useNavigate()
  const snapshot = import.meta.env.DEV && new URLSearchParams(window.location.search).get('snapshot') === 'decisions'
  const actorID = snapshot ? snapshotOwner : currentSubject()
  const [activeScope, setActiveScope] = useState<DecisionScope>('MINE')
  const [scopes, setScopes] = useState<ScopeState>(snapshot ? snapshotScopes : emptyScopes)
  const [hasMore, setHasMore] = useState<Record<DecisionScope, boolean>>({ MINE: false, APPROVALS: false, ACTIONS: false, REVIEWS: false })
  const [loading, setLoading] = useState(!snapshot)
  const [error, setError] = useState('')
  const [reloadRevision, setReloadRevision] = useState(0)
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('')
  const [evidenceMode, setEvidenceMode] = useState('')
  const [startDate, setStartDate] = useState('')
  const [endDate, setEndDate] = useState('')
  const [bookmarks, setBookmarks] = useState<Set<string>>(new Set())
  const [selectedRows, setSelectedRows] = useState<Set<string>>(new Set())
  const [moreOpenID, setMoreOpenID] = useState('')
  const [detail, setDetail] = useState<DecisionAggregate | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [createForm, setCreateForm] = useState(initialCreate)
  const [createBusy, setCreateBusy] = useState(false)
  const [createError, setCreateError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    Promise.all(scopeTabs.map(async tab => ({ tab: tab.key, result: await decisionAPI.list(tab.key, 200) })))
      .then(results => {
        if (cancelled) return
        const next = emptyScopes()
        const more = { MINE: false, APPROVALS: false, ACTIONS: false, REVIEWS: false }
        results.forEach(({ tab, result }) => {
          next[tab] = result.items
          more[tab] = Boolean(result.nextCursor)
        })
        setScopes(next)
        setHasMore(more)
      })
      .catch(cause => { if (!cancelled) setError(loadError(cause)) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [reloadRevision, snapshot])

  const visible = useMemo(() => filterDecisions(scopes[activeScope], { query, status, evidenceMode, startDate, endDate }), [activeScope, endDate, evidenceMode, query, scopes, startDate, status])
  const urgent = useMemo(() => {
    const unique = new Map<string, DecisionVisual>()
    Object.values(scopes).flat().forEach(item => unique.set(item.id, item))
    return urgentDecisions([...unique.values()], new Date('2026-08-10T12:00:00+08:00')).slice(0, 4) as DecisionVisual[]
  }, [scopes])

  const openDetail = async (item: DecisionVisual) => {
    setDetailError('')
    setDetailLoading(true)
    if (snapshot) {
      setDetail(snapshotAggregate(item))
      setDetailLoading(false)
      return
    }
    try { setDetail(await decisionAPI.get(item.id)) } catch (cause) { setDetailError(loadError(cause)) } finally { setDetailLoading(false) }
  }

  const submitDecision = async () => {
    if (!detail) return
    setDetailLoading(true)
    setDetailError('')
    try {
      if (snapshot) {
        const aggregate = { ...detail, decision: { ...detail.decision, status: 'IN_REVIEW' as DecisionStatus, recordVersion: detail.decision.recordVersion + 1 } }
        setDetail(aggregate)
        setScopes(current => ({ ...current, MINE: current.MINE.map(item => item.id === aggregate.decision.id ? { ...item, status: 'IN_REVIEW', recordVersion: aggregate.decision.recordVersion } : item) }))
      } else {
        const aggregate = await decisionAPI.submit(detail.decision.id, detail.decision.recordVersion)
        setDetail(aggregate)
        setReloadRevision(value => value + 1)
      }
      setNotice('决策已提交审批，审批人会在审批中心处理')
    } catch (cause) { setDetailError(loadError(cause)) } finally { setDetailLoading(false) }
  }

  const createDecision = async (event: FormEvent) => {
    event.preventDefault()
    setCreateError('')
    if (!actorID) { setCreateError('当前会话未提供可用的用户标识，请重新登录后再试'); return }
    if (!createForm.title.trim() || !createForm.question.trim() || !createForm.approvalPolicyId.trim() || !createForm.reviewDate) {
      setCreateError('请填写标题、决策问题、复盘日期和审批策略编码')
      return
    }
    setCreateBusy(true)
    try {
      if (snapshot) {
        const record = snapshotDecision(20, { title: createForm.title.trim(), question: createForm.question.trim(), decision: createForm.decision.trim(), expectedEffect: createForm.expectedEffect.trim(), risks: createForm.risks.split('\n').map(value => value.trim()).filter(Boolean), status: 'DRAFT', evidenceMode: 'MANUAL_WITHOUT_PLATFORM_EVIDENCE', reviewAt: `${createForm.reviewDate}T10:00:00+08:00`, approvalPolicyId: createForm.approvalPolicyId.trim(), evidenceCount: 0, actionPercent: 0 })
        setScopes(current => ({ ...current, MINE: [record, ...current.MINE] }))
        setDetail(snapshotAggregate(record))
      } else {
        const aggregate = await decisionAPI.create({
          ownerUserId: actorID,
          title: createForm.title.trim(),
          question: createForm.question.trim(),
          decision: createForm.decision.trim(),
          expectedEffect: createForm.expectedEffect.trim(),
          risks: createForm.risks.split('\n').map(value => value.trim()).filter(Boolean),
          evidenceMode: 'MANUAL_WITHOUT_PLATFORM_EVIDENCE',
          approvalPolicyId: createForm.approvalPolicyId.trim(),
          reviewAt: `${createForm.reviewDate}T10:00:00+08:00`,
          options: createForm.optionTitle.trim() ? [{ title: createForm.optionTitle.trim(), description: createForm.decision.trim(), selected: true }] : [],
          evidence: [],
        })
        setDetail(aggregate)
        setReloadRevision(value => value + 1)
      }
      setCreateOpen(false)
      setCreateForm(initialCreate())
      setNotice('决策草稿已创建，可在详情中提交审批')
    } catch (cause) { setCreateError(loadError(cause)) } finally { setCreateBusy(false) }
  }

  const toggleSelection = (id: string) => setSelectedRows(current => {
    const next = new Set(current)
    if (next.has(id)) next.delete(id); else next.add(id)
    return next
  })

  const toggleBookmark = (id: string) => setBookmarks(current => {
    const next = new Set(current)
    if (next.has(id)) next.delete(id); else next.add(id)
    return next
  })

  const reload = () => {
    if (snapshot) return
    setLoading(true)
    setError('')
    setReloadRevision(value => value + 1)
  }

  return <AppShell className="decisions-shell" title="决策与行动" eyebrow="" titleMeta={<span className="decisions-title-domain">领域：{currentDomain()?.name || '企业经营'}<CaretDown size={13} /></span>}>
    <section className="decisions-page" aria-label="决策与行动工作区">
      <nav className="decisions-scope-tabs" aria-label="决策范围">
        {scopeTabs.map(tab => <button key={tab.key} type="button" aria-current={activeScope === tab.key ? 'page' : undefined} onClick={() => setActiveScope(tab.key)}>
          {tab.label}<strong>（{scopes[tab.key].length}{hasMore[tab.key] ? '+' : ''}）</strong>
        </button>)}
      </nav>

      <div className="decisions-board">
        <div className="decisions-list-pane">
          <header className="decisions-toolbar">
            <label className="decisions-search"><MagnifyingGlass size={17} /><span className="sr-only">搜索决策</span><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索决策标题、关键词" /></label>
            <label><span className="sr-only">状态</span><select value={status} onChange={event => setStatus(event.target.value)}><option value="">全部状态</option>{Object.entries(decisionStatusMeta).map(([value, meta]) => <option value={value} key={value}>{meta.label}</option>)}</select><CaretDown size={13} /></label>
            <label><span className="sr-only">证据模式</span><select value={evidenceMode} onChange={event => setEvidenceMode(event.target.value)}><option value="">全部证据模式</option>{Object.entries(decisionEvidenceMeta).map(([value, meta]) => <option value={value} key={value}>{meta.shortLabel}</option>)}</select><CaretDown size={13} /></label>
            <div className="decisions-date-range"><CalendarBlank size={16} /><input aria-label="复盘开始日期" type="date" value={startDate} onChange={event => setStartDate(event.target.value)} /><span>~</span><input aria-label="复盘结束日期" type="date" value={endDate} onChange={event => setEndDate(event.target.value)} /></div>
            <button className="decisions-refresh" type="button" aria-label="刷新决策列表" onClick={reload}><ArrowClockwise size={18} /></button>
            <button className="decisions-create" type="button" onClick={() => { setCreateError(''); setCreateOpen(true) }}><Plus size={17} weight="bold" />新建决策<CaretDown size={13} /></button>
          </header>

          <div className="decisions-table-wrap" aria-live="polite" aria-busy={loading}>
            <div className="decisions-table-header" role="row">
              <span><input aria-label="选择当前页全部决策" type="checkbox" checked={visible.length > 0 && visible.every(item => selectedRows.has(item.id))} onChange={event => setSelectedRows(event.target.checked ? new Set(visible.map(item => item.id)) : new Set())} /></span>
              <span>决策标题</span><span>状态</span><span>证据模式</span><span>决策发起人</span><span>证据</span><span>行动进度</span><span>计划复盘时间</span><span>操作</span>
            </div>
            {loading && <div className="decisions-state"><span className="home-loading-dot" /><strong>正在加载当前领域的决策</strong><small>四个范围分别按当前用户权限读取</small></div>}
            {!loading && error && <div className="decisions-state is-error"><WarningCircle size={28} /><strong>决策列表暂时无法加载</strong><small>{error}</small><button type="button" onClick={reload}><ArrowClockwise size={15} />重新加载</button></div>}
            {!loading && !error && visible.map(item => {
              const statusMeta = decisionStatusMeta[item.status]
              const evidenceMeta = decisionEvidenceMeta[item.evidenceMode]
              const bookmarked = bookmarks.has(item.id)
              return <div className="decisions-table-row" role="row" key={item.id}>
                <span><input aria-label={`选择${item.title}`} type="checkbox" checked={selectedRows.has(item.id)} onChange={() => toggleSelection(item.id)} /></span>
                <span className="decision-title-cell"><button type="button" aria-label={bookmarked ? `取消关注${item.title}` : `关注${item.title}`} onClick={() => toggleBookmark(item.id)}><BookmarkSimple size={17} weight={bookmarked ? 'fill' : 'regular'} /></button><span><strong>{item.title}</strong><small>{item.question}</small></span></span>
                <span><em className={`decision-status is-${statusMeta.tone}`}>{statusMeta.label}</em></span>
                <span className={`decision-evidence-mode ${evidenceMeta.verified ? 'is-verified' : ''}`}><ShieldCheck size={15} />{evidenceMeta.shortLabel}</span>
                <span className="decision-owner-cell">{item.ownerAvatar ? <img src={item.ownerAvatar} alt="" /> : <User size={19} weight="duotone" />}<span><strong>{item.ownerDisplayName ?? decisionOwnerLabel(item.ownerUserId, actorID)}</strong><small>{item.ownerDepartment ?? '展示资料待后端补全'}</small></span></span>
                <span className={`decision-evidence-cell ${evidenceMeta.verified ? 'is-verified' : ''}`}><strong>{evidenceMeta.shortLabel}</strong><small>{typeof item.evidenceCount === 'number' ? `${item.evidenceCount} 项依据` : '打开详情查看'}</small></span>
                <span className="decision-progress-cell"><small>{item.actionLabel ?? '打开详情查看'}</small>{typeof item.actionPercent === 'number' ? <><span><i style={{ width: `${item.actionPercent}%` }} /></span><strong>{item.actionPercent}%</strong></> : <strong>—</strong>}</span>
                <span>{item.reviewAt ? formatDecisionDate(item.reviewAt) : '—'}</span>
                <span className="decision-row-actions"><button type="button" onClick={() => void openDetail(item)}>查看</button><button type="button" aria-haspopup="menu" aria-expanded={moreOpenID === item.id} aria-label={`更多${item.title}操作`} onClick={() => setMoreOpenID(current => current === item.id ? '' : item.id)}>更多</button>{moreOpenID === item.id && <span className="decision-row-menu" role="menu">
                  <button type="button" role="menuitem" onClick={() => { toggleBookmark(item.id); setMoreOpenID('') }}>{bookmarked ? '取消关注' : '关注决策'}</button>
                  <button type="button" role="menuitem" onClick={() => { setMoreOpenID(''); void openDetail(item) }}>打开详情</button>
                  {item.status === 'IN_REVIEW' && <button type="button" role="menuitem" onClick={() => navigate('/approvals')}>前往审批中心</button>}
                </span>}</span>
              </div>
            })}
            {!loading && !error && visible.length === 0 && <div className="decisions-state"><CheckCircle size={28} /><strong>{query || status || evidenceMode || startDate || endDate ? '没有匹配当前条件的决策' : '当前范围没有决策'}</strong><small>生产页面不会用演示数据补齐空态</small></div>}
          </div>
          <footer className="decisions-pagination"><span>共 {visible.length} 条{hasMore[activeScope] ? '，更多结果可继续分页' : ''}</span><div><button type="button" disabled aria-label="上一页"><CaretLeft size={14} /></button><button className="is-current" type="button">1</button><button type="button" disabled={!hasMore[activeScope]} aria-label="下一页"><CaretRight size={14} /></button><button type="button">20 条/页<CaretDown size={13} /></button></div></footer>
        </div>

        <aside className="decisions-urgent" aria-label="紧急关注">
          <header><h2>紧急关注</h2><span>{urgent.length}</span></header>
          {urgent.map(item => <button key={item.id} type="button" onClick={() => void openDetail(item)}><span className="urgent-dot" /><strong>{item.title}</strong><em>{item.status === 'IN_REVIEW' ? '待审批' : item.status === 'REVIEW_DUE' ? '待复盘' : '临近复盘'}</em><small>计划复盘时间：{formatDecisionDate(item.reviewAt)}</small></button>)}
          {!urgent.length && <div className="decisions-urgent-empty"><CheckCircle size={22} /><span>未来 7 天没有紧急事项</span></div>}
        </aside>
      </div>
    </section>

    {(detail || detailLoading || detailError) && <div className="decision-drawer-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !detailLoading) { setDetail(null); setDetailError('') } }}>
      <aside className="decision-detail-drawer" role="dialog" aria-modal="true" aria-labelledby="decision-detail-title">
        <header><div><span>决策详情</span><h2 id="decision-detail-title">{detail?.decision.title ?? '正在加载'}</h2></div><button type="button" aria-label="关闭决策详情" onClick={() => { setDetail(null); setDetailError('') }}><X size={18} /></button></header>
        {detailLoading && !detail && <div className="decisions-state"><span className="home-loading-dot" /><strong>正在加载完整决策记录</strong></div>}
        {detailError && <div className="decisions-state is-error"><WarningCircle size={28} /><strong>无法打开决策详情</strong><small>{detailError}</small></div>}
        {detail && <div className="decision-detail-content">
          <div className="decision-detail-summary"><em className={`decision-status is-${decisionStatusMeta[detail.decision.status].tone}`}>{decisionStatusMeta[detail.decision.status].label}</em><span>{decisionEvidenceMeta[detail.decision.evidenceMode].label}</span><p>{detail.decision.question}</p></div>
          <section><h3><Path size={17} />决策结论</h3><p>{detail.decision.decision || '尚未填写决策结论'}</p><small>预期效果：{detail.decision.expectedEffect || '尚未填写'}</small></section>
          <section><h3><FileText size={17} />备选方案 <span>{detail.options.length}</span></h3>{detail.options.length ? detail.options.map(option => <div className={`decision-detail-item ${option.selected ? 'is-selected' : ''}`} key={option.id}><strong>{option.title}{option.selected && <em>已选择</em>}</strong><p>{option.description || '无补充说明'}</p></div>) : <p>当前草稿没有备选方案。</p>}</section>
          <section><h3><ShieldCheck size={17} />证据与审批</h3><dl><div><dt>已验证证据</dt><dd>{detail.evidence.length} 项</dd></div><div><dt>审批进度</dt><dd>{detail.approvals.filter(item => item.status === 'APPROVED').length}/{detail.decision.requiredApprovals}</dd></div><div><dt>审批策略</dt><dd>{detail.decision.approvalPolicyId}</dd></div></dl></section>
          <section><h3><ClipboardText size={17} />行动进度</h3>{(() => { const progress = actionProgress(detail.actions); return <div className="decision-detail-progress"><span><i style={{ width: `${progress.percent}%` }} /></span><strong>{progress.completed}/{progress.total} 已完成</strong></div> })()}{detail.actions.slice(0, 4).map(action => <div className="decision-detail-item" key={action.id}><strong>{action.title}<em>{action.status}</em></strong><p>截止 {formatDecisionDate(action.dueAt, true)}</p></div>)}</section>
          <section><h3><Clock size={17} />复盘与风险</h3><dl><div><dt>计划复盘</dt><dd>{formatDecisionDate(detail.decision.reviewAt, true)}</dd></div><div><dt>风险项</dt><dd>{detail.decision.risks.length || 0} 项</dd></div></dl>{detail.decision.risks.map(risk => <p className="decision-risk" key={risk}><Flag size={14} />{risk}</p>)}</section>
        </div>}
        {detail && <footer>{detail.decision.status === 'DRAFT' && detail.decision.ownerUserId === actorID && <button className="primary" type="button" disabled={detailLoading} onClick={() => void submitDecision()}>{detailLoading ? '正在提交…' : '提交审批'}</button>}{detail.decision.status === 'IN_REVIEW' && <button className="primary" type="button" onClick={() => navigate('/approvals')}>前往审批中心</button>}<button type="button" onClick={() => setDetail(null)}>关闭</button></footer>}
      </aside>
    </div>}

    {createOpen && <div className="decision-create-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !createBusy) setCreateOpen(false) }}>
      <form className="decision-create-dialog" role="dialog" aria-modal="true" aria-labelledby="decision-create-title" onSubmit={createDecision}>
        <header><div><span>新建决策</span><h2 id="decision-create-title">创建手工决策草稿</h2></div><button type="button" disabled={createBusy} aria-label="关闭新建决策" onClick={() => setCreateOpen(false)}><X size={18} /></button></header>
        <div className="decision-create-body">
          <p className="decision-create-note"><WarningCircle size={18} />从本页新建的草稿会明确标记为“无平台证据”。从问数答案或报告创建时，后续将由来源页面固定已验证证据。</p>
          <label><span>决策标题 *</span><input autoFocus value={createForm.title} onChange={event => setCreateForm(current => ({ ...current, title: event.target.value }))} placeholder="例如：渠道政策调整专项方案" maxLength={256} /></label>
          <label><span>需要决策的问题 *</span><textarea value={createForm.question} onChange={event => setCreateForm(current => ({ ...current, question: event.target.value }))} placeholder="描述需要做出决定的业务问题" maxLength={4096} /></label>
          <div className="decision-create-grid"><label><span>审批策略编码 *</span><input value={createForm.approvalPolicyId} onChange={event => setCreateForm(current => ({ ...current, approvalPolicyId: event.target.value }))} placeholder="由当前业务领域管理员提供" maxLength={128} /></label><label><span>计划复盘日期 *</span><input type="date" value={createForm.reviewDate} onChange={event => setCreateForm(current => ({ ...current, reviewDate: event.target.value }))} /></label></div>
          <label><span>备选/推荐方案</span><input value={createForm.optionTitle} onChange={event => setCreateForm(current => ({ ...current, optionTitle: event.target.value }))} placeholder="例如：分区域分阶段调整" maxLength={256} /></label>
          <label><span>决策结论</span><textarea value={createForm.decision} onChange={event => setCreateForm(current => ({ ...current, decision: event.target.value }))} placeholder="草稿可先留空，提交审批前再完善" maxLength={8192} /></label>
          <label><span>预期效果</span><textarea value={createForm.expectedEffect} onChange={event => setCreateForm(current => ({ ...current, expectedEffect: event.target.value }))} placeholder="描述希望改善的经营结果" maxLength={4096} /></label>
          <label><span>风险（每行一项）</span><textarea value={createForm.risks} onChange={event => setCreateForm(current => ({ ...current, risks: event.target.value }))} placeholder="例如：跨团队资源协调可能影响进度" /></label>
          {createError && <p className="decision-create-error" role="alert"><WarningCircle size={16} />{createError}</p>}
        </div>
        <footer><button type="button" disabled={createBusy} onClick={() => setCreateOpen(false)}>取消</button><button className="primary" type="submit" disabled={createBusy}>{createBusy ? '正在创建…' : '创建草稿'}</button></footer>
      </form>
    </div>}

    {notice && <div className="home-notice" role="status"><Path size={17} /><span>{notice}</span><button type="button" aria-label="关闭提示" onClick={() => setNotice('')}>×</button></div>}
  </AppShell>
}
