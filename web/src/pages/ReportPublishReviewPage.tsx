import {
  ArrowLeft, Check, CheckCircle, Desktop, DeviceMobile, FilePdf, Info, Monitor, Printer,
  ShieldCheck, SpinnerGap, Users, WarningCircle,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import {
  reportEditorAPI, type PublicationGate, type PublicationReviewResponse, type ReportDraft,
} from '../report/api/editor'
import {
  reportRuntimeAPI, type LoadedReport, type RuntimeExecution, type RuntimeQueryResult,
} from '../report/api/runtime'
import { ChannelContributionChart, RuntimeResultChart, WorkspaceRevenueTrendChart } from '../report/runtime/ReportCharts'

type PreviewMode = 'desktop' | 'mobile' | 'print' | 'viewer'
type PublishChoice = 'snapshot' | 'refresh'

const snapshotDraft: ReportDraft = {
  reportId: 'snapshot-report', tenantId: 'snapshot-tenant', schemaVersion: '1.0', revisionNo: 24,
  definitionHash: '6aeb4e1319415478774072c247ecc29cf7c403c51ba8577ad5dad2b6f9ef4fd0',
  updatedBy: 'snapshot-user', updatedAt: '2026-08-11T10:24:00+08:00',
  definition: {
    schemaVersion: '1.0', metadata: { id: 'snapshot-report', code: 'RP-MON-202607', name: '2026年7月经营月报', reportType: 'REPORT' },
    dataContexts: [{ id: 'enterprise-operations', datasetVersionId: 'enterprise-operation-v2.4.0', alias: '企业经营主题域' }],
    pages: [], components: [],
  },
}

const snapshotChecks: PublicationGate[] = [
  { id: 'SEMANTIC', label: '口径与语义', status: 'PASSED', summary: '关键指标口径一致，语义资产已匹配', issues: [] },
  { id: 'FRESHNESS', label: '数据新鲜度', status: 'PASSED', summary: '数据截至 2026-08-09 23:59，符合要求', issues: [] },
  { id: 'PERMISSION', label: '权限泄漏', status: 'PASSED', summary: '已完成脱敏与权限校验，无越权风险', issues: [] },
  { id: 'EXECUTION', label: '组件可执行性', status: 'PASSED', summary: '所有组件可正常渲染与计算', issues: [] },
  { id: 'RESPONSIVE', label: '移动端适配', status: 'PASSED', summary: '移动端布局与交互适配通过', issues: [] },
  { id: 'FACT', label: '事实与结论核验', status: 'WARNING', summary: '库存周转天数组件使用 T-1 数据快照', issues: [{ code: 'REPORT_DATA_SNAPSHOT_STALE', path: 'components.inventory-turnover', message: '与报告数据截至日不一致' }] },
]

const snapshotReview: PublicationReviewResponse = {
  reviewRunId: 'RUN-20260811-00172', checkedAt: '2026-08-11T10:24:26+08:00',
  preflight: { draft: snapshotDraft, checks: snapshotChecks, blockerCodes: [], warningCodes: ['REPORT_DATA_SNAPSHOT_STALE'] },
  impact: { visibleCount: 56, editableCount: 4, activeShareCount: 2, subscriptionCount: 3, currentVersionNo: 12, targetVersionNo: 13 },
  dependencyRefs: ['definition:v2.1.7', 'semantic:v5.3.2', 'evidence:v10', 'dataset:v2.4.0'],
  review: {
    recommendation: 'CONDITIONAL', headline: '建议：有条件发布',
    summary: '基于当前检查结果与影响评估，满足发布条件，但存在 1 项需人工确认的风险。',
    risks: [{ code: 'REPORT_DATA_SNAPSHOT_STALE', title: '库存周转快照需确认', explanation: '库存周转天数组件使用 T-1 数据快照，可能导致与当日其他指标的时点不一致。', evidence: '数据快照 2026-08-10 10:00；报告截至 2026-08-10 23:59。', suggestedAction: '保留当前快照并在报告中披露，或返回编辑器刷新该组件。' }],
  },
}

function shortRef(value: string, fallback: string) {
  const tail = value.split(':').at(-1)?.split('@')[0] || ''
  return tail && tail.length <= 18 ? tail : fallback
}

function formatCheckedAt(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
    .format(new Date(value)).replaceAll('/', '-')
}

function GateCard({ gate, selected, onSelect }: { gate: PublicationGate; selected: boolean; onSelect: () => void }) {
  const warning = gate.status === 'WARNING'
  const blocked = gate.status === 'BLOCKED'
  return <article className={`publish-gate-card ${warning ? 'is-warning' : blocked ? 'is-blocked' : 'is-passed'} ${selected ? 'is-selected' : ''}`}>
    <button type="button" onClick={onSelect} aria-pressed={selected}>
      <span className="publish-gate-icon">{warning || blocked ? <WarningCircle size={17} weight="fill" /> : <CheckCircle size={17} weight="fill" />}</span>
      <span><strong>{gate.label}</strong><small>{gate.summary}</small></span>
      <em>{warning ? '需确认' : blocked ? '阻断' : '通过'}</em>
    </button>
    {selected && gate.issues.length > 0 && <div className="publish-gate-evidence"><strong>固定证据</strong>{gate.issues.map(issue => <p key={`${issue.code}:${issue.path}`}><code>{issue.code}</code>{issue.message}</p>)}</div>}
  </article>
}

function SnapshotPreview({ mode }: { mode: PreviewMode }) {
  return <div className={`publish-preview-document is-${mode}`}>
    <header><div><h2>2026年7月经营月报</h2><span>单位：万元</span></div></header>
    <dl className="publish-preview-kpis"><div><dt>营业收入</dt><dd>1,256,320 <small>万元</small></dd><span>环比 <b>+8.7% ↑</b> 同比 <b>+15.2% ↑</b></span></div><div><dt>毛利率</dt><dd>23.6%</dd><span>环比 <b>+1.3pct ↑</b> 同比 <b>+2.6ppt ↑</b></span></div><div><dt>经营利润</dt><dd>152,680 <small>万元</small></dd><span>环比 <b>+12.4% ↑</b> 同比 <b>+18.7% ↑</b></span></div></dl>
    <div className="publish-preview-chart-grid"><section><h3>营业收入趋势</h3><WorkspaceRevenueTrendChart /></section><section><h3>渠道结构（按收入）</h3><ChannelContributionChart compact /></section></div>
    <section className="publish-preview-conclusion"><strong>AI 经营结论</strong><ul><li>收入同比增长 15.2%，主要受线上电商与海外渠道拉动。</li><li>毛利率同比提升 2.6 个百分点，成本效率改善显著。</li><li>经营利润同比增长 18.7%，盈利能力增强。</li></ul></section>
    {mode === 'viewer' && <div className="publish-preview-mask"><ShieldCheck size={18} weight="fill" />当前按业务用户权限脱敏预览</div>}
  </div>
}

function LivePreview({ draft, loaded, execution, mode }: { draft: ReportDraft; loaded: LoadedReport | null; execution: RuntimeExecution | null; mode: PreviewMode }) {
  const componentMap = useMemo(() => new Map(loaded?.definition.components.map(component => [component.id, component]) ?? []), [loaded])
  const ready = execution?.components.filter(item => item.result) ?? []
  const kpi = ready.find(item => item.result?.rows.length === 1 && (item.result.columns.length ?? 0) <= 4)?.result
  const charts = ready.filter(item => item.result && item.result.rows.length > 1 && item.result.columns.length > 1).slice(0, 2)
  return <div className={`publish-preview-document is-${mode}`}>
    <header><div><h2>{draft.definition.metadata.name}</h2><span>草稿 r{draft.revisionNo} · 发布预览</span></div></header>
    {kpi ? <dl className="publish-preview-kpis">{kpi.columns.slice(0, 3).map((column, index) => <div key={column}><dt>{column}</dt><dd>{String(kpi.rows[0]?.[index] ?? '—')}</dd><span>按最近发布运行结果预览</span></div>)}</dl> : <div className="publish-preview-empty"><Monitor size={24} /><strong>当前草稿暂无可复用运行结果</strong><span>结构与绑定已完成门禁；首次发布后按查看者权限执行组件。</span></div>}
    {charts.length > 0 && <div className="publish-preview-chart-grid">{charts.map(item => <section key={item.componentId}><h3>{componentMap.get(item.componentId)?.options.title || '数据组件'}</h3><RuntimeResultChart result={item.result as RuntimeQueryResult} templateType={componentMap.get(item.componentId)?.templateRef.type || 'line'} label={componentMap.get(item.componentId)?.options.title || '报告组件'} /></section>)}</div>}
    <section className="publish-preview-component-list"><strong>当前草稿包含 {draft.definition.components.length} 个组件</strong><p>{draft.definition.components.slice(0, 5).map(component => component.options.title || component.templateRef.type).join(' · ') || 'AI 将在发布制品中固定当前结构。'}</p></section>
  </div>
}

export function ReportPublishReviewPage() {
  const navigate = useNavigate()
  const { reportId = '' } = useParams()
  // 设计走查快照只在开发构建中可用，生产构建绝不返回虚构发布评审结论。
  const snapshot = import.meta.env.DEV && new URLSearchParams(window.location.search).get('snapshot') === 'publish-review'
  const [review, setReview] = useState<PublicationReviewResponse | null>(snapshot ? snapshotReview : null)
  const [loaded, setLoaded] = useState<LoadedReport | null>(null)
  const [execution, setExecution] = useState<RuntimeExecution | null>(null)
  // 与运行页一致：加载态由已结算的请求令牌推导，避免 effect 内同步 setState。
  const [settledReportId, setSettledReportId] = useState('')
  const [failure, setFailure] = useState<{ reportId: string; message: string } | null>(null)
  const loading = !snapshot && settledReportId !== reportId
  const error = failure?.reportId === reportId ? failure.message : ''
  const setError = (message: string) =>
    setFailure(message ? { reportId, message } : null)
  const [mode, setMode] = useState<PreviewMode>('desktop')
  const [selectedGate, setSelectedGate] = useState('FACT')
  const [choice, setChoice] = useState<PublishChoice>('snapshot')
  const [confirmed, setConfirmed] = useState(false)
  const [comment, setComment] = useState('')
  const [publishing, setPublishing] = useState(false)
  const [toast, setToast] = useState('')

  useEffect(() => {
    if (snapshot) return undefined
    const controller = new AbortController()
    void reportEditorAPI.getDraft(reportId)
      .then(async draft => {
        const next = await reportEditorAPI.reviewPublication(reportId, { sourceRevisionNo: draft.revisionNo })
        if (controller.signal.aborted) return
        setReview(next)
        try {
          const runtime = await reportRuntimeAPI.load(reportId)
          const page = runtime.definition.pages.slice().sort((a, b) => a.order - b.order)[0]
          if (controller.signal.aborted) return
          setLoaded(runtime)
          if (page) setExecution(await reportRuntimeAPI.execute(reportId, { pageId: page.id }, { signal: controller.signal }))
        } catch { /* Draft-only reports intentionally have no reusable published runtime. */ }
      })
      .catch(cause => {
        if (!controller.signal.aborted) {
          setFailure({ reportId, message: cause instanceof Error ? cause.message : '发布评审加载失败' })
        }
      })
      .finally(() => { if (!controller.signal.aborted) setSettledReportId(reportId) })
    return () => controller.abort()
  }, [reportId, snapshot])

  const publish = async () => {
    if (!review || !confirmed || choice !== 'snapshot' || review.preflight.blockerCodes.length > 0) return
    setPublishing(true); setError('')
    try {
      if (!snapshot) await reportEditorAPI.publish(reportId, {
        sourceRevisionNo: review.preflight.draft.revisionNo,
        acknowledgeStaleInsights: review.preflight.warningCodes.includes('REPORT_INSIGHT_STALE'),
        desktopPreviewHash: review.preflight.draft.definitionHash,
        mobilePreviewHash: review.preflight.draft.definitionHash,
        reviewRunId: review.reviewRunId, humanComment: comment.trim(),
        acknowledgedIssueCodes: review.preflight.warningCodes,
      })
      setToast(`v${review.impact.targetVersionNo} 已发布，正在生成不可变制品`)
      window.setTimeout(() => navigate(snapshot ? `/reports/${reportId}?snapshot=runtime` : `/reports/${reportId}`), 800)
    } catch (cause) { setError(cause instanceof Error ? cause.message : '报告发布失败') }
    finally { setPublishing(false) }
  }

  if (loading) return <AppShell className="report-publish-shell" lockBusinessDomain><div className="publish-loading"><SpinnerGap className="is-spinning" size={28} /><strong>AI 正在执行发布门禁与影响评估</strong><p>检查结果将固定为可审计的发布评审收据。</p></div></AppShell>
  if (!review) return <AppShell className="report-publish-shell" lockBusinessDomain><div className="publish-loading is-error"><WarningCircle size={28} /><strong>发布评审无法完成</strong><p>{error || '当前评审结果不可用。'}</p><button type="button" onClick={() => navigate(-1)}>返回编辑器</button></div></AppShell>

  const draft = review.preflight.draft
  const warningGate = review.preflight.checks.find(gate => gate.status !== 'PASSED')
  const recommendationClass = review.review.recommendation === 'BLOCK' ? 'is-blocked' : review.review.recommendation === 'CONDITIONAL' ? 'is-conditional' : 'is-allow'
  return <AppShell className="report-publish-shell" lockBusinessDomain>
    <main className="publish-workspace">
      <header className="publish-header"><div><button type="button" onClick={() => navigate(snapshot ? `/reports/${reportId}?mode=edit&snapshot=runtime-draft` : `/reports/${reportId}?mode=edit`)}><ArrowLeft size={15} />返回编辑器</button><div><h1>{draft.definition.metadata.name}</h1><span>草稿 r{draft.revisionNo}</span><small>AI 评审于 {formatCheckedAt(review.checkedAt)}</small></div></div><button className="primary-button" type="button" disabled={publishing || review.preflight.blockerCodes.length > 0} onClick={() => document.querySelector('.publish-human-confirm')?.scrollIntoView({ behavior: 'smooth', block: 'center' })}>提交人工确认并发布</button></header>

      <section className="publish-steps" aria-label="发布评审进度"><div className="is-done"><span>1</span><strong>AI 全量校验</strong><small>已完成</small></div><i /><div className="is-done"><span>2</span><strong>多端预览</strong><small>已完成</small></div><i /><div className="is-done"><span>3</span><strong>发布影响评估</strong><small>已完成</small></div><i /><div className="is-active"><span>4</span><strong>人工最终确认</strong><small>当前步骤</small></div></section>

      {error && <div className="publish-inline-error" role="alert"><WarningCircle size={16} />{error}<button type="button" onClick={() => setError('')}>关闭</button></div>}

      <div className="publish-body">
        <aside className="publish-gates"><header><div><h2>AI 发布检查</h2><Info size={14} /></div><span>LLM 自动门禁</span></header><div>{review.preflight.checks.map(gate => <GateCard key={gate.id} gate={gate} selected={selectedGate === gate.id} onSelect={() => setSelectedGate(gate.id)} />)}</div><footer><strong>检查结论（LLM）</strong><p>{review.preflight.checks.filter(gate => gate.status === 'PASSED').length} 项通过，{review.preflight.warningCodes.length} 项需人工确认后方可进入最终发布。</p></footer></aside>

        <section className="publish-preview"><nav>{([['desktop', Desktop, '桌面'], ['mobile', DeviceMobile, '移动'], ['print', Printer, '打印'], ['viewer', Users, '查看者权限']] as const).map(([value, Icon, label]) => <button className={mode === value ? 'is-active' : ''} type="button" key={value} onClick={() => setMode(value)}><Icon size={15} />{label}</button>)}</nav><div className="publish-preview-meta"><span>预览身份：业务用户（脱敏）</span><span>数据截至 {snapshot ? '2026-08-09 23:59' : formatCheckedAt(review.checkedAt)}</span></div><div className="publish-preview-canvas">{snapshot ? <SnapshotPreview mode={mode} /> : <LivePreview draft={draft} loaded={loaded} execution={execution} mode={mode} />}</div></section>

        <aside className="publish-review-panel"><header><div><h2>AI 发布评审</h2><span>LLM 认知判断与解释</span></div></header><section className={`publish-ai-verdict ${recommendationClass}`}><strong>AI 结论：{review.review.headline}</strong><p>{review.review.summary}</p></section><section className="publish-pins"><div><CheckCircle size={15} /><span>已固定 Definition</span><strong>{shortRef(review.dependencyRefs.find(ref => ref.startsWith('definition:')) || '', 'v2.1.7')}</strong></div><div><CheckCircle size={15} /><span>已绑定语义资产</span><strong>{shortRef(review.dependencyRefs.find(ref => ref.startsWith('semantic:')) || '', `${draft.definition.dataContexts.length} 项`)}</strong></div><div><CheckCircle size={15} /><span>已锁定 Evidence</span><strong>{shortRef(review.dependencyRefs.find(ref => ref.startsWith('evidence:')) || '', '当前版本')}</strong></div><div><CheckCircle size={15} /><span>依赖版本已固化</span><strong>{shortRef(review.dependencyRefs.find(ref => ref.startsWith('dataset:')) || '', `${review.dependencyRefs.length} 项`)}</strong></div></section><section className="publish-impact"><h3>影响范围</h3><div><span><Users size={17} /><strong>{review.impact.visibleCount}</strong><small>位可见用户</small></span><span><ShieldCheck size={17} /><strong>{review.impact.editableCount}</strong><small>位编辑者</small></span><span><Monitor size={17} /><strong>{review.impact.subscriptionCount}</strong><small>个订阅</small></span><span><FilePdf size={17} /><strong>PDF / XLSX</strong><small>制品产出</small></span></div></section>
          {warningGate && <section className="publish-risk"><h3>AI 风险解释（需人工确认）</h3><p>{review.review.risks[0]?.explanation || warningGate.summary}</p><button type="button" onClick={() => setSelectedGate(warningGate.id)}>查看证据详情</button></section>}
          <section className="publish-choices"><button className={choice === 'snapshot' ? 'is-selected' : ''} type="button" onClick={() => setChoice('snapshot')}><span>{choice === 'snapshot' ? <CheckCircle size={16} weight="fill" /> : <i />}</span><strong>按当前快照发布（推荐）</strong><small>保留当前数据快照，发布 v{review.impact.targetVersionNo}</small></button><button className={choice === 'refresh' ? 'is-selected' : ''} type="button" onClick={() => setChoice('refresh')}><span>{choice === 'refresh' ? <CheckCircle size={16} weight="fill" /> : <i />}</span><strong>返回编辑并刷新数据</strong><small>让 AI 刷新后重新进行发布评审</small></button></section>
          <section className="publish-human-confirm"><label><input type="checkbox" checked={confirmed} onChange={event => setConfirmed(event.target.checked)} />我已审阅风险与受影响范围</label><span>修改意见（选填）</span><textarea value={comment} maxLength={300} onChange={event => setComment(event.target.value)} placeholder="如需修改，请说明理由或建议，AI 将记录并纳入发布日志。" /><small>{comment.length}/300</small><button className="primary-button" type="button" disabled={!confirmed || choice !== 'snapshot' || publishing || review.preflight.blockerCodes.length > 0} onClick={() => void publish()}>{publishing ? <><SpinnerGap className="is-spinning" size={16} />正在发布…</> : `确认发布 v${review.impact.targetVersionNo}`}</button>{choice === 'refresh' && <button className="quiet-button" type="button" onClick={() => navigate(snapshot ? `/reports/${reportId}?mode=edit&snapshot=runtime-draft` : `/reports/${reportId}?mode=edit`)}>返回编辑器并交给 AI</button>}<p>Human 原则：只审核证据、提出意见、最终确认</p></section>
        </aside>
      </div>
      <footer className="publish-principles"><strong>产品原则（已落地）</strong><span><CheckCircle size={18} weight="fill" />LLM 自动做门禁<small>全量校验与风险识别</small></span><span><CheckCircle size={18} weight="fill" />LLM 自动候选判断<small>发布建议与风险解释</small></span><span><CheckCircle size={18} weight="fill" />LLM 自动异常分析<small>识别异常并定位证据</small></span><span><CheckCircle size={18} weight="fill" />LLM 自动结果核验<small>事实一致性与口径校验</small></span><span><CheckCircle size={18} weight="fill" />LLM 自动发布建议<small>综合门禁与影响输出建议</small></span><span className="is-human"><Users size={18} />Human 最终确认<small>审核证据与风险后确认发布</small></span></footer>
      {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
    </main>
  </AppShell>
}
