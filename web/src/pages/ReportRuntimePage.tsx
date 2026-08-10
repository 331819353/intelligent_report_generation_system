import {
  ArrowClockwise, Check, CheckCircle, ClockCounterClockwise, DownloadSimple, FileText, Funnel,
  ShareNetwork, ShieldCheck, WarningCircle, X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { reportRuntimeAPI, type LoadedReport } from '../report/api/runtime'
import { ChannelContributionChart, RevenueTrendChart } from '../report/runtime/ReportCharts'
import { ComponentStateView, LazyRuntimeBlock, ReportBlockBoundary, ReportComponentBoundary } from '../report/runtime/ComponentStateView'
import type { ReportComponentState } from '../report/runtime/state'

type FilterState = { period: string; region: string; channel: string }

const defaultFilters: FilterState = { period: '2026-07', region: '全部区域', channel: '全部渠道' }

const componentStatusCards: Array<{ index: string; title: string; state: ReportComponentState }> = [
  { index: '三', title: '产品结构分析', state: 'READY' },
  { index: '四', title: '区域表现概览', state: 'PARTIAL' },
  { index: '五', title: '供应链健康度', state: 'STALE' },
  { index: '六', title: '费用效率分析', state: 'LOADING' },
  { index: '七', title: '新品贡献分析', state: 'EMPTY' },
  { index: '八', title: '库存周转分析', state: 'ERROR' },
  { index: '九', title: '客户满意度分析', state: 'TIMEOUT' },
  { index: '十', title: '受限组件', state: 'NO_PERMISSION' },
]

const stateCounts = [
  ['READY', '2', '已验证'], ['PARTIAL', '1', '部分数据'], ['STALE', '1', '数据过期'], ['LOADING', '1', '加载中'],
  ['EMPTY', '1', '暂无数据'], ['ERROR', '1', '加载失败'], ['TIMEOUT', '1', '响应超时'], ['NO_PERMISSION', '1', '无权限'],
] as const

function VersionHistory({ draft, onClose }: { draft: boolean; onClose: () => void }) {
  return <aside className="report-version-drawer" aria-label="版本记录">
    <header><div><span className="eyebrow">版本记录</span><h2>2026年7月经营月报</h2></div><button type="button" aria-label="关闭版本记录" onClick={onClose}><X size={18} /></button></header>
    <ol>
      {draft && <li className="is-current"><span>r1</span><div><strong>当前草稿</strong><time>2026-08-10 10:38</time><p>由“新建报告”创建，尚未发布。</p></div></li>}
      <li className={!draft ? 'is-current' : ''}><span>v12</span><div><strong>当前发布版本</strong><time>2026-08-10 09:30</time><p>更新 7 月经营数据与渠道分析。</p></div></li>
      <li><span>v11</span><div><strong>历史发布版本</strong><time>2026-08-03 18:20</time><p>调整供应链健康度说明。</p></div></li>
      <li><span>v10</span><div><strong>历史发布版本</strong><time>2026-07-29 11:06</time><p>月报初始发布。</p></div></li>
    </ol>
  </aside>
}

function RuntimeStatusCard({ item, state, onRetry }: { item: typeof componentStatusCards[number]; state: ReportComponentState; onRetry: () => void }) {
  const safeTitle = state === 'NO_PERMISSION' ? undefined : item.title
  return <section className="report-small-block">
    <header><h3>{item.index}、{state === 'NO_PERMISSION' ? '受限组件' : item.title}</h3><span className={`report-state-chip is-${state.toLocaleLowerCase()}`}>{state === 'READY' ? '已验证' : state === 'PARTIAL' ? '部分数据' : state === 'STALE' ? '数据过期' : state === 'LOADING' ? '加载中' : state === 'EMPTY' ? '暂无数据' : state === 'ERROR' ? '加载失败' : state === 'TIMEOUT' ? '响应超时' : '无权限'}</span></header>
    <ReportComponentBoundary fallback={<ComponentStateView state="ERROR" compact onAction={onRetry} />}>
      <ComponentStateView state={state} boundTitle={safeTitle} compact onAction={onRetry} />
    </ReportComponentBoundary>
  </section>
}

export function ReportRuntimePage() {
  const navigate = useNavigate()
  const { reportId = 'new' } = useParams()
  const snapshot = new URLSearchParams(window.location.search).get('snapshot')
  const designSnapshot = snapshot === 'runtime' || snapshot === 'runtime-draft'
  const draft = reportId === 'new' || snapshot === 'runtime-draft'
  const [loaded, setLoaded] = useState<LoadedReport | null>(null)
  const [loading, setLoading] = useState(!designSnapshot && !draft)
  const [loadError, setLoadError] = useState('')
  const [filters, setFilters] = useState(defaultFilters)
  const [appliedFilters, setAppliedFilters] = useState(defaultFilters)
  const [states, setStates] = useState<Record<string, ReportComponentState>>(() => Object.fromEntries(componentStatusCards.map(item => [item.index, item.state])))
  const [versionsOpen, setVersionsOpen] = useState(false)
  const [toast, setToast] = useState('')
  const [refreshing, setRefreshing] = useState(false)

  useEffect(() => {
    if (designSnapshot || draft) return undefined
    let cancelled = false
    void reportRuntimeAPI.load(reportId).then(value => { if (!cancelled) setLoaded(value) })
      .catch(cause => { if (!cancelled) setLoadError(cause instanceof Error ? cause.message : '报告加载失败') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [designSnapshot, draft, reportId])

  const title = loaded?.definition.metadata.name ?? '2026年7月经营月报'
  const version = loaded?.versionNo ?? 12
  const notify = (message: string) => { setToast(message); window.setTimeout(() => setToast(''), 2400) }
  const refresh = () => {
    setRefreshing(true)
    setStates(Object.fromEntries(componentStatusCards.map(item => [item.index, 'LOADING'])))
    window.setTimeout(() => {
      setStates(Object.fromEntries(componentStatusCards.map(item => [item.index, item.state])))
      setRefreshing(false)
      notify('数据已按当前查看者权限刷新')
    }, 650)
  }
  const retry = (index: string) => {
    setStates(current => ({ ...current, [index]: 'LOADING' }))
    window.setTimeout(() => setStates(current => ({ ...current, [index]: 'READY' })), 520)
  }
  const statusSummary = useMemo(() => stateCounts.map(([state, count, label]) => ({ state, count: states ? Object.values(states).filter(value => value === state).length || Number(count) : Number(count), label })), [states])

  return <AppShell
    className="report-runtime-shell"
    eyebrow="智能报告"
    title={title}
    titleMeta={<><span className={`report-publish-badge ${draft ? 'is-draft' : ''}`}>{draft ? '待发布' : '已发布'}</span><span className="report-version-badge">{draft ? 'r1' : `v${version}`}</span></>}
    lockBusinessDomain
    actions={<>
      {draft
        ? <><button className="quiet-button" type="button" onClick={() => notify('草稿已保存')}><Check size={16} />保存草稿</button><button className="primary-button" type="button" onClick={() => notify('发布前校验已启动')}><UploadSimpleIcon />发布报告</button></>
        : <button className="quiet-button" type="button" disabled={refreshing} onClick={refresh}><ArrowClockwise className={refreshing ? 'is-spinning' : ''} size={16} />刷新数据</button>}
      <button className="quiet-button" type="button" onClick={() => setVersionsOpen(true)}><ClockCounterClockwise size={16} />版本记录</button>
      <span className="report-asof">数据截至 2026-08-09 23:59</span>
    </>}
  >
    <button className="report-back-link" type="button" onClick={() => navigate(designSnapshot ? '/reports?snapshot=assets' : '/reports')}>返回报告资产中心</button>
    <section className="report-filter-bar" aria-label="报告筛选">
      <label><span>期间</span><select value={filters.period} onChange={event => setFilters(value => ({ ...value, period: event.target.value }))}><option>2026-07</option><option>2026-06</option><option>2026-05</option></select></label>
      <label><span>区域</span><select value={filters.region} onChange={event => setFilters(value => ({ ...value, region: event.target.value }))}><option>全部区域</option><option>华东</option><option>华南</option><option>华北</option></select></label>
      <label><span>渠道</span><select value={filters.channel} onChange={event => setFilters(value => ({ ...value, channel: event.target.value }))}><option>全部渠道</option><option>线上渠道</option><option>线下渠道</option><option>工程渠道</option></select></label>
      <button className="primary-button" type="button" onClick={() => { setAppliedFilters(filters); notify('筛选已应用') }}><Funnel size={15} />应用筛选</button>
    </section>
    {loading && <div className="report-runtime-feedback"><span className="report-loading-spinner" />正在校验不可变发布制品…</div>}
    {!loading && loadError && <div className="report-runtime-feedback is-error"><WarningCircle size={24} /><strong>报告加载失败</strong><p>{loadError}</p><button type="button" onClick={() => window.location.reload()}>重试</button></div>}
    {!loading && !loadError && <div className="report-runtime-layout">
      <div className="report-runtime-content">
        <section className="report-summary-card">
          <header><h2>经营摘要（已验证）</h2><ShieldCheck size={16} weight="fill" /></header>
          <p>2026年7月企业经营整体稳中有进，收入端保持增长，渠道表现分化，盈利能力持续优化。</p>
          <dl>
            <div><dt>营业收入（万元）</dt><dd>1,256,320</dd><span>同比 <strong>+8.7%</strong></span></div>
            <div><dt>毛利率</dt><dd>23.6%</dd><span>同比 <strong>+1.2pct</strong></span></div>
            <div><dt>经营利润（万元）</dt><dd>152,680</dd><span>同比 <strong>+12.3%</strong></span></div>
          </dl>
          <footer><span>来源：企业经营数据集 › 月度经营主题集</span><span>数据截至 2026-08-09 23:59</span></footer>
        </section>
        <section className="report-main-block">
          <header><h2>一、营业收入趋势</h2></header>
          <p className="report-chart-label">营业收入（万元）</p>
          <ReportComponentBoundary><RevenueTrendChart /></ReportComponentBoundary>
          <div className="report-insight-strip"><strong>结论</strong> 7月收入同比增长 8.7%，延续增长趋势，其中线上渠道增速快于线下。<span><CheckCircle size={13} weight="fill" />已验证</span></div>
          <h2 className="report-section-heading">二、渠道贡献分析</h2>
          <ReportComponentBoundary><ChannelContributionChart /></ReportComponentBoundary>
          <p className="report-partial-note"><WarningCircle size={14} weight="fill" />部分系列数据暂不可用，未计入贡献分析</p>
        </section>
        <div className="report-state-grid">
          {componentStatusCards.map(item => <ReportBlockBoundary key={item.index} fallback={<ComponentStateView state="ERROR" compact onAction={() => retry(item.index)} />}><LazyRuntimeBlock><RuntimeStatusCard item={item} state={states[item.index] ?? item.state} onRetry={() => retry(item.index)} /></LazyRuntimeBlock></ReportBlockBoundary>)}
        </div>
      </div>
      <aside className="report-runtime-rail">
        <section><h2>关键变化</h2><ul><li>营业收入同比 +8.7%，环比 +5.1%，线上渠道增长显著。</li><li>线下渠道增长放缓，工程渠道受项目节奏影响同比下降。</li><li>毛利率同比提升 1.2pct，费用率保持稳定。</li><li>区域表现分化，华东、华南贡献提升明显。</li></ul></section>
        <section><h2>数据状态</h2><p>共 10 个组件</p><div className="report-state-summary">{statusSummary.map(item => <div key={item.state} className={`is-${item.state.toLocaleLowerCase()}`}><strong>{item.count}</strong><span>{item.label}</span></div>)}</div><h3>说明</h3><p>不同状态的组件对报告可用性影响不同，建议优先关注未验证或异常的部分。</p><button type="button" onClick={() => notify('已展开状态说明')}>查看状态说明</button></section>
        <section><h2>报告信息</h2><dl><div><dt>报告版本</dt><dd>{draft ? '草稿 r1' : `v${version}`}</dd></div><div><dt>发布时间</dt><dd>{draft ? '尚未发布' : '2026-08-10 09:30'}</dd></div><div><dt>生成任务</dt><dd>月度经营月报 · 定时任务</dd></div><div><dt>数据范围</dt><dd>企业经营主题集</dd></div><div><dt>负责人</dt><dd>企业经营分析组</dd></div></dl><button type="button" onClick={() => setVersionsOpen(true)}>查看版本记录</button></section>
        <section><h2>相关操作</h2><button type="button" onClick={() => notify('PDF 导出任务已创建')}><DownloadSimple size={16} />导出报告（PDF）</button><button type="button" onClick={() => notify('分享链接已复制；打开时仍会校验查看者权限')}><ShareNetwork size={16} />分享报告链接</button></section>
      </aside>
    </div>}
    {versionsOpen && <VersionHistory draft={draft} onClose={() => setVersionsOpen(false)} />}
    {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
    <span className="report-applied-filter-sr" aria-live="polite">已应用：{appliedFilters.period}，{appliedFilters.region}，{appliedFilters.channel}</span>
  </AppShell>
}

function UploadSimpleIcon() {
  return <FileText size={16} />
}
