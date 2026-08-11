import {
  ArrowLeft, ArrowClockwise, Check, CheckCircle, ClockCounterClockwise, Copy,
  DotsThree, DownloadSimple, Funnel, Info, NotePencil, ShareNetwork, ShieldCheck, SpinnerGap, WarningCircle, X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { reportAssetsAPI } from '../report/api/assets'
import {
  reportRuntimeAPI, type ExportFormat, type LoadedReport, type ReportVersion, type RuntimeComponent,
  type RuntimeComponentResult, type RuntimeExecution, type RuntimeFilter, type RuntimePage,
} from '../report/api/runtime'
import type { ReportAsset } from '../report/assets/model'
import { ChannelContributionChart, RuntimeResultChart, RuntimeRevenueTrendChart } from '../report/runtime/ReportCharts'
import { ComponentStateView, ReportBlockBoundary, ReportComponentBoundary } from '../report/runtime/ComponentStateView'
import type { ReportComponentState } from '../report/runtime/state'

type SnapshotFilters = { period: string; region: string; channel: string }
const snapshotDefaultFilters: SnapshotFilters = { period: '2026-07', region: '全部区域', channel: '全部渠道' }
const snapshotSections = ['经营摘要', '营业收入趋势', '渠道贡献分析', '区域对比', '库存周转', '费用效率', '经营结论']
const snapshotVersions: ReportVersion[] = [
  { id: 'snapshot-v12', reportId: 'snapshot-report', versionNo: 12, sourceRevisionNo: 18, definitionHash: 'snapshot-v12', publishedBy: '王敏', publishedAt: '2026-08-10T09:30:00+08:00', staleInsightsAcknowledged: false, artifactState: 'READY' },
  { id: 'snapshot-v11', reportId: 'snapshot-report', versionNo: 11, sourceRevisionNo: 16, definitionHash: 'snapshot-v11', publishedBy: '王敏', publishedAt: '2026-08-03T18:20:00+08:00', staleInsightsAcknowledged: false, artifactState: 'READY' },
  { id: 'snapshot-v10', reportId: 'snapshot-report', versionNo: 10, sourceRevisionNo: 13, definitionHash: 'snapshot-v10', publishedBy: '王敏', publishedAt: '2026-07-29T11:06:00+08:00', staleInsightsAcknowledged: false, artifactState: 'READY' },
]

function formatDateTime(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value)).replaceAll('/', '-')
}

function filterLabel(filter: RuntimeFilter) {
  const value = filter.fieldRef.field.toLocaleLowerCase()
  if (/period|month|月份|期间|date/.test(value)) return '期间'
  if (/region|区域|area/.test(value)) return '区域'
  if (/channel|渠道/.test(value)) return '渠道'
  return filter.fieldRef.field
}

function parseFilterInput(filter: RuntimeFilter, raw: string): unknown {
  if (filter.type === 'MULTI_SELECT') return raw.split(',').map(item => item.trim()).filter(Boolean)
  if (filter.type === 'BOOLEAN') return raw === 'true'
  if (filter.type === 'NUMBER_RANGE' || filter.type === 'DATE_RANGE' || filter.type === 'RELATIVE_TIME') return JSON.parse(raw)
  if (filter.type === 'PARAMETER_INPUT' && raw !== '' && Number.isFinite(Number(raw))) return Number(raw)
  return raw
}

function componentIDsForPage(page?: RuntimePage) {
  if (!page) return []
  return page.sections.flatMap(section => section.blocks.flatMap(block => block.zones.flatMap(zone => zone.slots.map(slot => slot.componentId).filter((id): id is string => Boolean(id)))))
}

function VersionHistory({ title, currentVersion, items, loading, error, onSelect, onClose }: {
  title: string; currentVersion: number; items: ReportVersion[]; loading: boolean; error: string; onSelect: (versionNo: number) => void; onClose: () => void
}) {
  return <div className="report-drawer-backdrop" role="presentation" onMouseDown={onClose}>
    <aside className="report-version-drawer" aria-label="版本历史" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">版本历史</span><h2>{title}</h2></div><button type="button" aria-label="关闭版本历史" onClick={onClose}><X size={18} /></button></header>
      {loading && <p className="report-drawer-feedback"><SpinnerGap className="is-spinning" size={18} />正在读取不可变发布版本…</p>}
      {error && <p className="report-drawer-feedback is-error"><WarningCircle size={18} />{error}</p>}
      {!loading && !error && <ol>{items.map(item => <li className={item.versionNo === currentVersion ? 'is-current' : ''} key={item.id}>
        <span>v{item.versionNo}</span><div><strong>{item.versionNo === currentVersion ? '当前发布版本' : item.rollbackOfVersionNo ? `回滚自 v${item.rollbackOfVersionNo}` : '历史发布版本'}</strong><time>{formatDateTime(item.publishedAt)}</time><p>{item.rollbackReason || `制品状态：${item.artifactState}`}</p>{item.versionNo !== currentVersion && <button type="button" onClick={() => onSelect(item.versionNo)}>查看此版本</button>}</div>
      </li>)}</ol>}
    </aside>
  </div>
}

function ShareDialog({ reportId, title, versionId, filterSnapshot, snapshot, onClose, onNotify }: {
  reportId: string; title: string; versionId: string; filterSnapshot: Record<string, unknown>; snapshot: boolean; onClose: () => void; onNotify: (message: string) => void
}) {
  const [shareType, setShareType] = useState<'INTERNAL_USER' | 'INTERNAL_GROUP'>('INTERNAL_USER')
  const [principalId, setPrincipalId] = useState('')
  const [expiresOn, setExpiresOn] = useState(() => new Date(Date.now() + 30 * 86400000).toISOString().slice(0, 10))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [shareURL, setShareURL] = useState('')
  const create = async () => {
    if (!principalId.trim()) return
    setBusy(true); setError('')
    try {
      const token = snapshot ? 'snapshot-secure-report-token' : (await reportRuntimeAPI.createShare(reportId, {
        reportVersionId: versionId, shareType, principalId: principalId.trim(), filterSnapshot,
        expiresAt: new Date(`${expiresOn}T23:59:59`).toISOString(),
      })).token
      setShareURL(`${window.location.origin}/api/v1/report-shares/${encodeURIComponent(token)}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '安全分享创建失败')
    } finally { setBusy(false) }
  }
  const copy = async () => { await navigator.clipboard.writeText(shareURL); onNotify('安全分享链接已复制') }
  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal is-share" role="dialog" aria-modal="true" aria-labelledby="runtime-share-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">安全分享</span><h2 id="runtime-share-title">分享“{title}”</h2></div><button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      <p className="report-permission-note"><ShieldCheck size={16} weight="fill" />分享固定当前版本与筛选快照；访问时仍按接收人的报告权限和数据权限执行。</p>
      {!shareURL && <div className="report-share-form"><label>接收对象<select value={shareType} onChange={event => setShareType(event.target.value as typeof shareType)}><option value="INTERNAL_USER">内部用户</option><option value="INTERNAL_GROUP">内部用户组</option></select></label><label>用户或用户组 ID<input value={principalId} onChange={event => setPrincipalId(event.target.value)} placeholder="输入 UUID" /></label><label>有效期至<input type="date" value={expiresOn} onChange={event => setExpiresOn(event.target.value)} /></label></div>}
      {error && <p className="report-permission-error"><WarningCircle size={15} />{error}</p>}
      {shareURL && <div className="report-share-result"><Check size={18} weight="bold" /><div><strong>安全分享已创建</strong><span>{shareURL}</span></div><button className="quiet-button" type="button" onClick={() => void copy()}><Copy size={14} />复制链接</button></div>}
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>{shareURL ? '完成' : '取消'}</button>{!shareURL && <button className="primary-button" type="button" disabled={busy || !principalId.trim() || !expiresOn} onClick={() => void create()}>{busy ? '正在创建…' : '创建分享'}</button>}</footer>
    </section>
  </div>
}

function ExportDialog({ reportId, versionNo, page, filterValues, asOf, timezone, snapshot, onClose }: {
  reportId: string; versionNo: number; page?: RuntimePage; filterValues: Record<string, unknown>; asOf: string; timezone: string; snapshot: boolean; onClose: () => void
}) {
  const [format, setFormat] = useState<ExportFormat>('PDF')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [job, setJob] = useState<{ id: string; state: string } | null>(null)
  const create = async () => {
    setBusy(true); setError('')
    try {
      const created = snapshot ? { id: 'EXP-20260810-0012', state: 'PENDING' } : await reportRuntimeAPI.createExport(reportId, {
        versionNo, format, pageIds: page ? [page.id] : undefined, filterValues, asOf: asOf || new Date().toISOString(), timezone: timezone || 'UTC',
      })
      setJob({ id: created.id, state: created.state })
    } catch (cause) { setError(cause instanceof Error ? cause.message : '导出任务创建失败') } finally { setBusy(false) }
  }
  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-export-modal" role="dialog" aria-modal="true" aria-labelledby="runtime-export-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">导出报告</span><h2 id="runtime-export-title">固定 v{versionNo} 与当前筛选快照</h2></div><button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      {!job && <div className="report-export-form"><label>导出格式<select value={format} onChange={event => setFormat(event.target.value as ExportFormat)}><option value="PDF">PDF 报告</option><option value="PNG">PNG 长图</option><option value="XLSX">Excel 数据</option><option value="CSV">CSV 数据</option></select></label><dl><div><dt>报告版本</dt><dd>v{versionNo}</dd></div><div><dt>导出页面</dt><dd>{page?.name || '当前页面'}</dd></div><div><dt>数据时点</dt><dd>{formatDateTime(asOf)}</dd></div><div><dt>时区</dt><dd>{timezone || 'UTC'}</dd></div></dl><p><Info size={15} />导出任务将在后台生成，可在任务中心查看进度；制品过期后需重新创建。</p></div>}
      {error && <p className="report-permission-error"><WarningCircle size={15} />{error}</p>}
      {job && <div className="report-export-result"><CheckCircle size={23} weight="fill" /><div><strong>导出任务已创建</strong><span>{job.id} · {job.state === 'PENDING' ? '等待处理' : job.state}</span></div></div>}
      <footer><button className="quiet-button" type="button" onClick={onClose}>{job ? '完成' : '取消'}</button>{!job && <button className="primary-button" type="button" disabled={busy} onClick={() => void create()}><DownloadSimple size={16} />{busy ? '正在创建…' : '创建导出任务'}</button>}</footer>
    </section>
  </div>
}

function ResultTable({ component, item }: { component: RuntimeComponent; item: RuntimeComponentResult }) {
  const result = item.result
  if (!result && component.options.richText) return <p className="runtime-live-richtext">{component.options.richText}</p>
  if (!result) return <ComponentStateView state={item.state} boundTitle={component.options.title} />
  const rows = result.rows.slice(0, 8)
  const oneRowMetrics = rows.length === 1 && result.columns.length <= 4
  if (oneRowMetrics) return <dl className="runtime-live-kpis">{result.columns.map((column, index) => <div key={column}><dt>{column}</dt><dd>{String(rows[0]?.[index] ?? '—')}</dd></div>)}</dl>
  if (/(chart|line|bar|pie|donut|trend)/i.test(component.templateRef.type) && rows.length > 0 && result.columns.length > 1) return <RuntimeResultChart result={result} templateType={component.templateRef.type} label={component.options.title || component.templateRef.type} />
  return <div className="runtime-live-table-wrap"><table><thead><tr>{result.columns.map(column => <th key={column}>{column}</th>)}</tr></thead><tbody>{rows.map((row, rowIndex) => <tr key={rowIndex}>{result.columns.map((column, columnIndex) => <td key={column}>{String(row[columnIndex] ?? '—')}</td>)}</tr>)}</tbody></table>{result.rows.length > rows.length && <p>当前预览前 {rows.length} 行，共 {result.rows.length} 行。</p>}</div>
}

function LiveRuntimeDocument({ loaded, page, execution, refreshing, onRetry }: {
  loaded: LoadedReport; page?: RuntimePage; execution: RuntimeExecution | null; refreshing: boolean; onRetry: (blockId: string) => void
}) {
  const componentMap = useMemo(() => new Map(loaded.definition.components.map(component => [component.id, component])), [loaded])
  const resultMap = useMemo(() => new Map(execution?.components.map(item => [item.componentId, item]) ?? []), [execution])
  if (!page) return <div className="runtime-report-level-empty"><WarningCircle size={24} /><strong>发布版本没有可显示页面</strong><p>请联系报告 Owner 检查不可变 Definition。</p></div>
  return <div className="runtime-live-sections">{page.sections.slice().sort((a, b) => a.order - b.order).map(section => <section id={`runtime-section-${section.id}`} className="runtime-live-section" key={section.id}>
    <header><h2>{section.name}</h2><span><ShieldCheck size={14} weight="fill" />发布制品 v{loaded.versionNo}</span></header>
    <div className="runtime-live-blocks">{section.blocks.map(block => {
      const ids = block.zones.flatMap(zone => zone.slots.map(slot => slot.componentId).filter((id): id is string => Boolean(id)))
      return <ReportBlockBoundary key={block.id}>{ids.map(id => {
        const component = componentMap.get(id)
        const item = resultMap.get(id) ?? { componentId: id, state: refreshing ? 'LOADING' : 'ERROR' as ReportComponentState }
        if (!component) return <ComponentStateView key={id} state="ERROR" />
        const showResult = item.state === 'READY' || item.state === 'PARTIAL'
        return <article className="runtime-live-component" key={id}><div className="runtime-live-component-title"><div><h3>{item.state === 'NO_PERMISSION' ? '受限组件' : component.options.title || component.templateRef.type}</h3>{item.state !== 'NO_PERMISSION' && component.options.subtitle && <p>{component.options.subtitle}</p>}</div><span className={`report-state-chip is-${item.state.toLocaleLowerCase()}`}>{item.state}</span></div><ReportComponentBoundary fallback={<ComponentStateView state="ERROR" onAction={() => onRetry(block.id)} />}>{showResult ? <><ResultTable component={component} item={item} />{item.state === 'PARTIAL' && <p className="runtime-partial-disclosure"><WarningCircle size={14} />查询返回部分结果，请谨慎解读。</p>}</> : <ComponentStateView state={item.state} boundTitle={item.state === 'NO_PERMISSION' ? undefined : component.options.title} onAction={() => onRetry(block.id)} />}</ReportComponentBoundary></article>
      })}</ReportBlockBoundary>
    })}</div>
  </section>)}</div>
}

function SnapshotDocument({ recoveryState }: { recoveryState: Record<string, ReportComponentState> }) {
  return <>
    <section className="runtime-summary" id="runtime-section-summary"><header><h2>经营摘要</h2><ShieldCheck size={17} weight="fill" /></header><p>2026年7月企业经营整体稳中有进，收入端保持增长，渠道表现分化，盈利能力持续优化。</p><dl><div><dt>营业收入</dt><dd>1,256,320 <small>万元</small></dd><span>同比 <strong>+8.7% ↑</strong></span></div><div><dt>毛利率</dt><dd>23.6%</dd><span>同比 <strong>+1.2pct ↑</strong></span></div><div><dt>经营利润</dt><dd>152,680 <small>万元</small></dd><span>同比 <strong>+12.3% ↑</strong></span></div></dl></section>
    <div className="runtime-partial-banner"><WarningCircle size={16} weight="fill" /><span>部分数据为估算值，预计对营业收入影响约 0.6%。</span><button type="button">查看说明</button></div>
    <section className="runtime-chart-card" id="runtime-section-revenue"><header><h3>营业收入趋势 <small>（万元）</small></h3></header><ReportComponentBoundary><RuntimeRevenueTrendChart /></ReportComponentBoundary></section>
    <section className="runtime-channel-card" id="runtime-section-channel"><ReportComponentBoundary><ChannelContributionChart /></ReportComponentBoundary></section>
    <section className="runtime-verified-conclusion" id="runtime-section-conclusion"><CheckCircle size={17} weight="fill" /><div><strong>经营结论 <span>（已验证）</span></strong><p>收入实现稳健增长，线上渠道贡献提升，盈利能力持续优化；工程渠道短期承压，需关注项目节奏与回款。</p></div><button type="button">查看验证记录</button></section>
    <span className="runtime-snapshot-state" aria-live="polite">营业收入趋势：{recoveryState.revenue}；区域对比：{recoveryState.region}</span>
  </>
}

export function ReportRuntimePage() {
  const navigate = useNavigate()
  const { reportId = '' } = useParams()
  const query = new URLSearchParams(window.location.search)
  // 设计走查快照只在开发构建中可用，生产构建绝不返回虚构报告数据。
  const snapshot = import.meta.env.DEV && query.get('snapshot') === 'runtime'
  const requestedVersion = Number(query.get('version')) || undefined
  const [loaded, setLoaded] = useState<LoadedReport | null>(null)
  const [assetMeta, setAssetMeta] = useState<ReportAsset | null>(null)
  const [execution, setExecution] = useState<RuntimeExecution | null>(null)
  const [versions, setVersions] = useState<ReportVersion[]>(snapshot ? snapshotVersions : [])
  const [versionsLoading, setVersionsLoading] = useState(false)
  const [versionsError, setVersionsError] = useState('')
  // 加载状态由「已结算的请求令牌」推导，避免在 effect 内同步 setState，
  // 同时保证切换报告或版本时不会短暂展示上一份报告的数据与错误。
  const requestToken = `${reportId}#${requestedVersion ?? ''}`
  const [settledToken, setSettledToken] = useState('')
  const [failure, setFailure] = useState<{ token: string; message: string } | null>(null)
  const loading = !snapshot && settledToken !== requestToken
  const loadError = failure?.token === requestToken ? failure.message : ''
  const setLoadError = (message: string) =>
    setFailure(message ? { token: requestToken, message } : null)
  const [refreshing, setRefreshing] = useState(false)
  const [snapshotFilters, setSnapshotFilters] = useState(snapshotDefaultFilters)
  const [snapshotApplied, setSnapshotApplied] = useState(snapshotDefaultFilters)
  const [filterInputs, setFilterInputs] = useState<Record<string, string>>({})
  const [appliedFilterValues, setAppliedFilterValues] = useState<Record<string, unknown>>({})
  const [versionsOpen, setVersionsOpen] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  const [moreOpen, setMoreOpen] = useState(false)
  const [toast, setToast] = useState('')
  const [recoveryState, setRecoveryState] = useState<Record<string, ReportComponentState>>({ revenue: 'PARTIAL', region: 'EMPTY' })
  const abortRef = useRef<AbortController | null>(null)

  const notify = (message: string) => { setToast(message); window.setTimeout(() => setToast(''), 2600) }

  useEffect(() => {
    if (snapshot) return undefined
    const controller = new AbortController()
    abortRef.current = controller
    void Promise.all([reportRuntimeAPI.load(reportId, requestedVersion), reportRuntimeAPI.listVersions(reportId)])
      .then(async ([runtime, versionPage]) => {
        if (controller.signal.aborted) return
        setLoaded(runtime); setVersions(versionPage.items)
        const page = runtime.definition.pages.slice().sort((a, b) => a.order - b.order)[0]
        if (!page) return
        const [value, assets] = await Promise.all([
          reportRuntimeAPI.execute(reportId, { pageId: page.id, filterValues: {} }, { versionNo: requestedVersion, signal: controller.signal }),
          reportAssetsAPI.list({ search: runtime.definition.metadata.code, limit: 20 }).catch(() => ({ items: [] as ReportAsset[] })),
        ])
        if (!controller.signal.aborted) {
          setExecution(value)
          setAssetMeta(assets.items.find(item => item.id === reportId) ?? null)
        }
      })
      .catch(cause => {
        if (!controller.signal.aborted) {
          setFailure({ token: requestToken, message: cause instanceof Error ? cause.message : '报告加载失败' })
        }
      })
      .finally(() => { if (!controller.signal.aborted) setSettledToken(requestToken) })
    return () => controller.abort()
  }, [reportId, requestedVersion, requestToken, snapshot])

  const page = useMemo(() => loaded?.definition.pages.slice().sort((a, b) => a.order - b.order)[0], [loaded])
  const title = snapshot ? '2026年7月经营月报' : loaded?.definition.metadata.name || '报告运行页'
  const versionNo = snapshot ? 12 : loaded?.versionNo || requestedVersion || 0
  const currentVersion = versions.find(item => item.versionNo === versionNo)
  const ownerName = snapshot ? '王敏' : assetMeta?.ownerName || '报告 Owner'
  const asOf = snapshot ? '2026-08-09T23:59:00+08:00' : execution?.asOf || ''
  const timezone = snapshot ? 'Asia/Shanghai' : execution?.timezone || 'UTC'
  const activeSections = snapshot ? snapshotSections : page?.sections.slice().sort((a, b) => a.order - b.order).map(section => section.name) ?? []

  const run = async (values: Record<string, unknown>, blockId?: string) => {
    if (snapshot) { setSnapshotApplied(snapshotFilters); notify(blockId ? '组件已恢复' : '已按当前筛选刷新报告'); return }
    if (!loaded || !page) return
    abortRef.current?.abort()
    const controller = new AbortController(); abortRef.current = controller
    setRefreshing(true); setLoadError('')
    try {
      const next = await reportRuntimeAPI.execute(reportId, { pageId: page.id, visibleBlockIds: blockId ? [blockId] : undefined, filterValues: values }, { versionNo: requestedVersion, signal: controller.signal })
      setExecution(previous => {
        if (!blockId || !previous) return next
        const refreshed = new Map(next.components.map(item => [item.componentId, item]))
        return { ...next, components: previous.components.map(item => refreshed.get(item.componentId) ?? item) }
      })
      notify(blockId ? '组件已按当前权限重新执行' : '数据已按当前查看者权限刷新')
    } catch (cause) {
      if (!controller.signal.aborted) setLoadError(cause instanceof Error ? cause.message : '报告执行失败')
    } finally { if (!controller.signal.aborted) setRefreshing(false) }
  }

  const applyFilters = () => {
    if (snapshot) { void run({}); return }
    const next: Record<string, unknown> = {}
    try {
      loaded?.definition.globalFilters.forEach(filter => {
        const raw = filterInputs[filter.id]
        if (raw !== undefined && raw.trim() !== '') next[filter.id] = parseFilterInput(filter, raw)
      })
    } catch { notify('筛选值格式不正确，请检查输入'); return }
    setAppliedFilterValues(next); void run(next)
  }

  const openVersions = () => {
    setVersionsOpen(true)
    if (snapshot || versions.length) return
    setVersionsLoading(true); setVersionsError('')
    void reportRuntimeAPI.listVersions(reportId).then(result => setVersions(result.items)).catch(cause => setVersionsError(cause instanceof Error ? cause.message : '版本历史加载失败')).finally(() => setVersionsLoading(false))
  }

  const executionStates = useMemo(() => {
    if (snapshot) return { READY: 8, PARTIAL: 1, STALE: 1, LOADING: 0 }
    const ids = componentIDsForPage(page)
    const stateMap = new Map(execution?.components.map(item => [item.componentId, item.state]) ?? [])
    return ids.reduce<Record<'READY' | 'PARTIAL' | 'STALE' | 'LOADING', number>>((counts, id) => {
      const state = refreshing ? 'LOADING' : stateMap.get(id) || 'LOADING'
      if (state in counts) counts[state as keyof typeof counts] += 1
      return counts
    }, { READY: 0, PARTIAL: 0, STALE: 0, LOADING: 0 })
  }, [execution, page, refreshing, snapshot])

  const liveRecoveryItems = useMemo(() => {
    if (!loaded || !page || !execution) return []
    const componentMap = new Map(loaded.definition.components.map(component => [component.id, component]))
    const blockMap = new Map<string, string>()
    page.sections.forEach(section => section.blocks.forEach(block => block.zones.forEach(zone => zone.slots.forEach(slot => { if (slot.componentId) blockMap.set(slot.componentId, block.id) }))))
    return execution.components.filter(item => item.state !== 'READY').slice(0, 3).map(item => ({ item, component: componentMap.get(item.componentId), blockId: blockMap.get(item.componentId) || '' }))
  }, [execution, loaded, page])

  const keyChanges = snapshot ? [
    '营业收入同比 +8.7%，环比 +5.1%，较上渠道增长显著。', '线下渠道增长放缓，工程渠道受项目节奏影响同比下降。', '毛利率同比提升 1.2pct，费用率保持稳定。', '区域表现分化，华东、华南贡献提升明显。',
  ] : loaded?.definition.components.map(component => component.options.richText?.trim()).filter((value): value is string => Boolean(value)).slice(0, 4) ?? []

  return <AppShell className="report-runtime-shell report-runtime-selected" lockBusinessDomain>
    <div className="runtime-workspace">
      <header className="runtime-report-header"><button className="runtime-back" type="button" onClick={() => navigate(snapshot ? '/reports?snapshot=assets' : '/reports')}><ArrowLeft size={15} />返回报告工作台</button><div className="runtime-title-row"><div><h1>{title}</h1><span className="report-publish-badge">已发布</span><span className="report-version-badge">v{versionNo}</span></div><div className="runtime-header-actions"><button className="quiet-button" type="button" disabled={!snapshot && !assetMeta?.allowedActions.includes('SHARE')} onClick={() => setShareOpen(true)}><ShareNetwork size={17} />分享</button><button className="quiet-button" type="button" disabled={!snapshot && !assetMeta?.allowedActions.includes('EDIT')} onClick={() => navigate(`/reports/${reportId}?mode=edit${snapshot ? '&snapshot=runtime-draft' : ''}`)}><NotePencil size={17} />编辑报告</button><button className="primary-button" type="button" disabled={!snapshot && (!loaded?.definition.runtimePolicy.exportEnabled || !assetMeta?.allowedActions.includes('EXPORT'))} onClick={() => setExportOpen(true)}><DownloadSimple size={17} />导出报告</button><div className="runtime-more-wrap"><button className="quiet-button runtime-icon-button" type="button" aria-label="更多操作" aria-expanded={moreOpen} onClick={() => setMoreOpen(value => !value)}><DotsThree size={20} weight="bold" /></button>{moreOpen && <div className="runtime-more-menu"><button type="button" onClick={() => { setMoreOpen(false); openVersions() }}><ClockCounterClockwise size={15} />版本历史</button><button type="button" onClick={() => { setMoreOpen(false); navigate(snapshot ? '/reports?snapshot=assets' : '/reports') }}><ArrowLeft size={15} />返回报告中心</button></div>}</div></div></div><div className="runtime-report-meta"><span>拥有者：{ownerName}</span><span>更新于 {snapshot ? '2026-08-10 10:12' : formatDateTime(assetMeta?.updatedAt || currentVersion?.publishedAt)}</span><i /><span>数据截至 {formatDateTime(asOf)}</span><i /><button type="button" onClick={openVersions}>版本历史</button></div></header>

      <section className="runtime-filter-bar" aria-label="报告筛选">
        <div className="runtime-filter-fields">{snapshot ? <><label><span>期间</span><select value={snapshotFilters.period} onChange={event => setSnapshotFilters(value => ({ ...value, period: event.target.value }))}><option>2026-07</option><option>2026-06</option><option>2026-05</option></select></label><label><span>区域</span><select value={snapshotFilters.region} onChange={event => setSnapshotFilters(value => ({ ...value, region: event.target.value }))}><option>全部区域</option><option>华东</option><option>华南</option><option>华北</option></select></label><label><span>渠道</span><select value={snapshotFilters.channel} onChange={event => setSnapshotFilters(value => ({ ...value, channel: event.target.value }))}><option>全部渠道</option><option>线上渠道</option><option>线下渠道</option><option>工程渠道</option></select></label></> : loaded?.definition.globalFilters.map(filter => <label key={filter.id}><span>{filterLabel(filter)}</span><input type={filter.type === 'DATE' ? 'date' : 'text'} value={filterInputs[filter.id] ?? ''} placeholder={filter.defaultValue?.values?.join(', ') || '使用发布默认值'} onChange={event => setFilterInputs(current => ({ ...current, [filter.id]: event.target.value }))} /></label>)}</div>
        <div className="runtime-filter-actions"><button className="quiet-button" type="button" disabled={refreshing} onClick={() => void run(snapshot ? {} : appliedFilterValues)}><ArrowClockwise className={refreshing ? 'is-spinning' : ''} size={16} />刷新数据</button><button className="primary-button" type="button" disabled={refreshing} onClick={applyFilters}><Funnel size={16} />应用筛选</button></div>
      </section>

      {loading && <div className="runtime-report-feedback"><SpinnerGap className="is-spinning" size={25} /><strong>正在加载不可变发布制品</strong><p>随后会按当前查看者权限执行可见组件。</p></div>}
      {!loading && loadError && !loaded && <div className="runtime-report-feedback is-error"><WarningCircle size={25} /><strong>报告加载失败</strong><p>{loadError}</p><button type="button" onClick={() => window.location.reload()}>重新加载</button></div>}
      {!loading && (snapshot || loaded) && <div className="runtime-report-layout">
        <nav className="runtime-section-nav" aria-label="报告章节">{activeSections.map((section, index) => <a className={index === 0 ? 'is-active' : ''} href={snapshot ? `#runtime-section-${index === 0 ? 'summary' : index === 1 ? 'revenue' : index === 2 ? 'channel' : index === activeSections.length - 1 ? 'conclusion' : 'summary'}` : `#runtime-section-${page?.sections.slice().sort((a, b) => a.order - b.order)[index]?.id}`} key={`${section}-${index}`}><span />{section}</a>)}</nav>
        <main className="runtime-report-document">{loadError && <div className="runtime-inline-error"><WarningCircle size={15} />{loadError}<button type="button" onClick={() => void run(appliedFilterValues)}>重试</button></div>}{snapshot ? <SnapshotDocument recoveryState={recoveryState} /> : loaded && <LiveRuntimeDocument loaded={loaded} page={page} execution={execution} refreshing={refreshing} onRetry={blockId => void run(appliedFilterValues, blockId)} />}</main>
        <aside className="runtime-context-rail">
          <section><h2>关键变化</h2>{keyChanges.length ? <ul>{keyChanges.map((item, index) => <li key={index}>{item}</li>)}</ul> : <p className="runtime-rail-empty">当前发布版本未配置关键变化摘要。</p>}</section>
          <section><h2>数据状态</h2><p>共 {snapshot ? 10 : componentIDsForPage(page).length} 个组件</p><div className="runtime-state-summary"><div className="is-ready"><strong>{executionStates.READY}</strong><span>已验证</span></div><div className="is-partial"><strong>{executionStates.PARTIAL}</strong><span>部分数据</span></div><div className="is-stale"><strong>{executionStates.STALE}</strong><span>数据过期</span></div><div className="is-loading"><strong>{executionStates.LOADING}</strong><span>加载中</span></div></div>{(snapshot || executionStates.PARTIAL > 0) && <div className="runtime-state-warning"><WarningCircle size={16} weight="fill" /><span>{snapshot ? '1 个组件存在部分数据，预计对营业收入影响约 0.6%' : '存在部分结果，请查看组件内的数据披露。'}</span><button type="button">查看说明</button></div>}</section>
          <section><h2>报告信息</h2><dl><div><dt>报告版本</dt><dd>v{versionNo}</dd></div><div><dt>发布时间</dt><dd>{snapshot ? '2026-08-10 09:30' : formatDateTime(currentVersion?.publishedAt)}</dd></div><div><dt>生成任务</dt><dd>{snapshot ? '月度经营月报 · 定时任务' : `运行时执行 · ${timezone}`}</dd></div><div><dt>数据范围</dt><dd>{snapshot ? '企业经营主题域' : `${loaded?.definition.components.length ?? 0} 个发布组件`}</dd></div><div><dt>负责人</dt><dd>{snapshot ? '企业经营分析组' : ownerName}</dd></div></dl><button type="button" onClick={openVersions}>查看版本记录</button></section>
          <section><h2>组件恢复</h2>{snapshot ? <><div className="runtime-recovery-item"><span className={recoveryState.revenue === 'READY' ? 'is-ready' : 'is-partial'} /><div><strong>{recoveryState.revenue === 'READY' ? '已恢复 - 营业收入趋势（图表）' : '部分数据 - 营业收入趋势（图表）'}</strong><small>{recoveryState.revenue === 'READY' ? '最新执行已验证' : '预计影响：0.6%'}</small></div>{recoveryState.revenue !== 'READY' && <button type="button" onClick={() => { setRecoveryState(current => ({ ...current, revenue: 'READY' })); notify('营业收入趋势已恢复') }}>尝试恢复</button>}</div><div className="runtime-recovery-item"><span className={recoveryState.region === 'READY' ? 'is-ready' : 'is-empty'} /><div><strong>{recoveryState.region === 'READY' ? '已恢复 - 区域对比（图表）' : '无数据 - 区域对比（图表）'}</strong><small>{recoveryState.region === 'READY' ? '最新执行已验证' : '无可用数据'}</small></div>{recoveryState.region !== 'READY' && <button type="button" onClick={() => { setRecoveryState(current => ({ ...current, region: 'READY' })); notify('区域对比已恢复') }}>尝试恢复</button>}</div></> : liveRecoveryItems.length ? liveRecoveryItems.map(({ item, component, blockId }) => <div className="runtime-recovery-item" key={item.componentId}><span className={`is-${item.state.toLocaleLowerCase()}`} /><div><strong>{item.state === 'NO_PERMISSION' ? '受限组件' : `${item.state} - ${component?.options.title || component?.templateRef.type || '组件'}`}</strong><small>{item.errorCode || '可重新执行当前分块'}</small></div>{item.state !== 'NO_PERMISSION' && <button type="button" disabled={!blockId || refreshing} onClick={() => void run(appliedFilterValues, blockId)}>尝试恢复</button>}</div>) : <p className="runtime-rail-empty"><CheckCircle size={16} weight="fill" />当前组件均已正常完成。</p>}</section>
        </aside>
      </div>}
    </div>
    {versionsOpen && <VersionHistory title={title} currentVersion={versionNo} items={versions} loading={versionsLoading} error={versionsError} onClose={() => setVersionsOpen(false)} onSelect={next => navigate(`${snapshot ? `/reports/${reportId}?snapshot=runtime&version=${next}` : `/reports/${reportId}?version=${next}`}`)} />}
    {shareOpen && <ShareDialog reportId={reportId} title={title} versionId={snapshot ? 'snapshot-v12' : loaded?.versionId || ''} filterSnapshot={snapshot ? snapshotApplied : appliedFilterValues} snapshot={snapshot} onClose={() => setShareOpen(false)} onNotify={notify} />}
    {exportOpen && <ExportDialog reportId={reportId} versionNo={versionNo} page={page} filterValues={snapshot ? snapshotApplied : appliedFilterValues} asOf={asOf} timezone={timezone} snapshot={snapshot} onClose={() => setExportOpen(false)} />}
    {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
    <span className="report-applied-filter-sr" aria-live="polite">{snapshot ? `已应用：${snapshotApplied.period}，${snapshotApplied.region}，${snapshotApplied.channel}` : `已应用 ${Object.keys(appliedFilterValues).length} 个筛选`}</span>
  </AppShell>
}
