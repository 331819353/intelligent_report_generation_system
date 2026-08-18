import {
  ArrowLeft, ArrowClockwise, Check, CheckCircle, ClockCounterClockwise, Copy,
  CalendarDots, DotsThree, DownloadSimple, Funnel, Info, NotePencil, ShareNetwork, ShieldCheck, SpinnerGap, WarningCircle, X,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import '../styles/report.css'
import { CreateDecisionDialog } from '../components/decision/CreateDecisionDialog'
import { ReportScheduleDialog } from '../components/report/ReportScheduleDialog'
import { administrationAPI, type ShareTarget } from '../lib/administration'
import { reportAssetsAPI } from '../report/api/assets'
import {
  reportRuntimeAPI, type ExportFormat, type LoadedReport, type ReportVersion,
  type ReportShareRecord, type RuntimeExecution, type RuntimePage,
} from '../report/api/runtime'
import type { ReportAsset } from '../report/assets/model'
import { ReportFilterStrip } from '../report/render/ReportFilterStrip'
import { ReportPageView } from '../report/render/ReportPageView'
import { emptyManifestIndex, indexManifests, listComponentManifests, type ManifestIndex } from '../report/render/manifests'
import { describeSelections, useReportRuntimeState, type ReportExecutionInput } from '../report/render/runtime-state'
import { orderedPages, orderedSections, placedComponentIDs } from '../report/render/schema'

function formatDateTime(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value)).replaceAll('/', '-')
}

function VersionHistory({ title, currentVersion, items, loading, error, onSelect, onClose }: {
  title: string; currentVersion: number; items: ReportVersion[]; loading: boolean; error: string
  onSelect: (versionNo: number) => void; onClose: () => void
}) {
  return <div className="report-drawer-backdrop" role="presentation" onMouseDown={onClose}>
    <aside className="report-version-drawer" aria-label="版本历史" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">版本历史</span><h2>{title}</h2></div><button type="button" aria-label="关闭版本历史" onClick={onClose}><X size={18} /></button></header>
      {loading && <p className="report-drawer-feedback"><SpinnerGap className="is-spinning" size={18} />正在读取不可变发布版本…</p>}
      {error && <p className="report-drawer-feedback is-error"><WarningCircle size={18} />{error}</p>}
      {!loading && !error && <ol>{items.map(item => <li className={item.versionNo === currentVersion ? 'is-current' : ''} key={item.id}>
        <span>v{item.versionNo}</span>
        <div>
          <strong>{item.versionNo === currentVersion ? '当前发布版本' : item.rollbackOfVersionNo ? `回滚自 v${item.rollbackOfVersionNo}` : '历史发布版本'}</strong>
          <time>{formatDateTime(item.publishedAt)}</time>
          <p>{item.rollbackReason || `制品状态：${item.artifactState}`}</p>
          {item.versionNo !== currentVersion && <button type="button" onClick={() => onSelect(item.versionNo)}>查看此版本</button>}
        </div>
      </li>)}</ol>}
    </aside>
  </div>
}

function ShareDialog({ reportId, title, versionId, filterSnapshot, onClose, onNotify }: {
  reportId: string; title: string; versionId: string; filterSnapshot: Record<string, unknown>
  onClose: () => void; onNotify: (message: string) => void
}) {
  const [shareType, setShareType] = useState<'INTERNAL_USER' | 'INTERNAL_GROUP'>('INTERNAL_USER')
  const [principalId, setPrincipalId] = useState('')
  const [expiresOn, setExpiresOn] = useState(() => new Date(Date.now() + 30 * 86400000).toISOString().slice(0, 10))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [shareURL, setShareURL] = useState('')
  const [createdShare, setCreatedShare] = useState<ReportShareRecord | null>(null)
  const [shares, setShares] = useState<ReportShareRecord[]>([])
  const [shareTargets, setShareTargets] = useState<ShareTarget[]>([])
  const [loadingShares, setLoadingShares] = useState(true)
  const [openedAt] = useState(() => Date.now())
  const loadShares = useCallback(async () => {
    setLoadingShares(true)
    try {
      const [result, targets] = await Promise.all([reportRuntimeAPI.listShares(reportId), administrationAPI.listShareTargets()])
      setShares(result.items); setShareTargets(targets)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '分享记录加载失败')
    } finally { setLoadingShares(false) }
  }, [reportId])
  useEffect(() => {
    const timer = window.setTimeout(() => { void loadShares() }, 0)
    return () => window.clearTimeout(timer)
  }, [loadShares])
  const eligibleTargets = shareTargets.filter(item => item.type === (shareType === 'INTERNAL_USER' ? 'USER' : 'ROLE'))
  const targetNames = new Map(shareTargets.map(item => [item.id, item]))
  const create = async () => {
    if (!principalId.trim()) return
    setBusy(true); setError('')
    try {
      const created = await reportRuntimeAPI.createShare(reportId, {
        reportVersionId: versionId, shareType, principalId: principalId.trim(), filterSnapshot,
        expiresAt: new Date(`${expiresOn}T23:59:59`).toISOString(),
      })
      setShareURL(`${window.location.origin}/report-shares/${encodeURIComponent(created.token)}`)
      setCreatedShare(created.share as ReportShareRecord)
      await loadShares()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '安全分享创建失败')
    } finally { setBusy(false) }
  }
  const revoke = async (share: ReportShareRecord) => {
    setBusy(true); setError('')
    try {
      await reportRuntimeAPI.revokeShare(reportId, share.id)
      setShareURL(''); setCreatedShare(null)
      onNotify('安全分享已撤销')
      await loadShares()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '安全分享撤销失败')
    } finally { setBusy(false) }
  }
  const copy = async () => { await navigator.clipboard.writeText(shareURL); onNotify('安全分享链接已复制') }
  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal is-share" role="dialog" aria-modal="true" aria-labelledby="runtime-share-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">安全分享</span><h2 id="runtime-share-title">分享“{title}”</h2></div><button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      <p className="report-permission-note"><ShieldCheck size={16} weight="fill" />分享固定当前版本与筛选快照；访问时仍按接收人的报告权限和数据权限执行。</p>
      {!shareURL && <div className="report-share-form">
        <label>接收对象<select value={shareType} onChange={event => { setShareType(event.target.value as typeof shareType); setPrincipalId('') }}><option value="INTERNAL_USER">内部用户</option><option value="INTERNAL_GROUP">内部用户组</option></select></label>
        <label>选择接收人<select aria-label="选择接收人" value={principalId} onChange={event => setPrincipalId(event.target.value)}><option value="">请选择{shareType === 'INTERNAL_USER' ? '当前领域用户' : '内部用户组'}</option>{eligibleTargets.map(item => <option value={item.id} key={item.id}>{item.name} · {item.detail}</option>)}</select></label>
        <label>有效期至<input type="date" value={expiresOn} onChange={event => setExpiresOn(event.target.value)} /></label>
      </div>}
      {error && <p className="report-permission-error"><WarningCircle size={15} />{error}</p>}
      {shareURL && <div className="report-share-result"><Check size={18} weight="bold" /><div><strong>安全分享已创建</strong><span>{shareURL}</span></div><button className="quiet-button" type="button" onClick={() => void copy()}><Copy size={14} />复制链接</button>{createdShare && <button className="quiet-button is-danger" type="button" disabled={busy} onClick={() => void revoke(createdShare)}>立即撤销</button>}</div>}
      <section className="report-share-history" aria-label="已有分享">
        <header><strong>已有分享</strong><span>{loadingShares ? '加载中…' : `${shares.filter(item => !item.revokedAt && !item.expiredAt).length} 个有效`}</span></header>
        {!loadingShares && shares.length === 0 && <p>还没有为当前报告创建分享。</p>}
        {!loadingShares && shares.slice(0, 8).map(item => {
          const inactive = Boolean(item.revokedAt || item.expiredAt || new Date(item.expiresAt).getTime() <= openedAt)
          const target = targetNames.get(item.principalId)
          return <div className={inactive ? 'is-inactive' : ''} key={item.id}><span><strong>{target?.name || (item.shareType === 'INTERNAL_USER' ? '内部用户' : '内部用户组')}</strong><small>{target?.detail || '接收对象已不在当前可选目录'}</small></span><time>{inactive ? item.revokedAt ? '已撤销' : '已过期' : `有效至 ${new Date(item.expiresAt).toLocaleDateString('zh-CN')}`}</time>{!inactive && <button type="button" disabled={busy} onClick={() => void revoke(item)}>撤销</button>}</div>
        })}
      </section>
      <footer>
        <button className="quiet-button" type="button" disabled={busy} onClick={onClose}>{shareURL ? '完成' : '取消'}</button>
        {!shareURL && <button className="primary-button" type="button" disabled={busy || !principalId.trim() || !expiresOn} onClick={() => void create()}>{busy ? '正在创建…' : '创建分享'}</button>}
      </footer>
    </section>
  </div>
}

function ExportDialog({ reportId, versionNo, page, filterValues, asOf, timezone, onClose }: {
  reportId: string; versionNo: number; page?: RuntimePage; filterValues: Record<string, unknown>
  asOf: string; timezone: string; onClose: () => void
}) {
  const [format, setFormat] = useState<ExportFormat>('PDF')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [job, setJob] = useState<{ id: string; state: string; failureCode?: string } | null>(null)
  const create = async () => {
    setBusy(true); setError('')
    try {
      const created = await reportRuntimeAPI.createExport(reportId, {
        versionNo, format, pageIds: page ? [page.id] : undefined, filterValues,
        asOf: asOf || new Date().toISOString(), timezone: timezone || 'UTC',
      })
      setJob({ id: created.id, state: created.state, failureCode: created.failureCode })
    } catch (cause) { setError(cause instanceof Error ? cause.message : '导出任务创建失败') } finally { setBusy(false) }
  }
  useEffect(() => {
    if (!job || !['PENDING', 'RUNNING'].includes(job.state)) return
    let cancelled = false
    const timer = window.setInterval(() => {
      void reportRuntimeAPI.getExport(reportId, job.id).then(next => {
        if (!cancelled) setJob({ id: next.id, state: next.state, failureCode: next.failureCode })
      }).catch(() => { /* 保持任务卡片，下一轮继续读取。 */ })
    }, 1200)
    return () => { cancelled = true; window.clearInterval(timer) }
  }, [job, reportId])
  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-export-modal" role="dialog" aria-modal="true" aria-labelledby="runtime-export-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">导出报告</span><h2 id="runtime-export-title">固定 v{versionNo} 与当前筛选快照</h2></div><button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      {!job && <div className="report-export-form">
        <label>导出格式<select value={format} onChange={event => setFormat(event.target.value as ExportFormat)}><option value="PDF">PDF 报告</option><option value="PNG">PNG 长图</option><option value="XLSX">Excel 数据</option><option value="CSV">CSV 数据</option></select></label>
        <dl><div><dt>报告版本</dt><dd>v{versionNo}</dd></div><div><dt>导出页面</dt><dd>{page?.name || '当前页面'}</dd></div><div><dt>数据时点</dt><dd>{formatDateTime(asOf)}</dd></div><div><dt>时区</dt><dd>{timezone || 'UTC'}</dd></div></dl>
        <p><Info size={15} />导出任务将在后台生成，可在任务中心查看进度；制品过期后需重新创建。</p>
      </div>}
      {error && <p className="report-permission-error"><WarningCircle size={15} />{error}</p>}
      {job && <div className={`report-export-result ${job.state === 'FAILED' ? 'is-error' : ''}`.trim()}>
        {job.state === 'FAILED' ? <WarningCircle size={23} weight="fill" /> : <CheckCircle size={23} weight="fill" />}
        <div><strong>{job.state === 'READY' ? '导出文件已生成' : job.state === 'FAILED' ? '导出生成失败' : '正在生成导出文件'}</strong>
          <span>{job.id} · {{ PENDING: '等待处理', RUNNING: '正在生成', READY: '可下载', FAILED: '生成失败' }[job.state] ?? job.state}</span>
          {job.failureCode && <span>{job.failureCode}</span>}
        </div>
        {job.state === 'READY' && <a className="primary-button" href={reportRuntimeAPI.exportDownloadURL(reportId, job.id)}><DownloadSimple size={16} />下载文件</a>}
      </div>}
      <footer><button className="quiet-button" type="button" onClick={onClose}>{job ? '完成' : '取消'}</button>{!job && <button className="primary-button" type="button" disabled={busy} onClick={() => void create()}><DownloadSimple size={16} />{busy ? '正在创建…' : '创建导出任务'}</button>}</footer>
    </section>
  </div>
}

/**
 * 报告运行页。
 *
 * 页面本身不含任何报告内容渲染逻辑：正文交给共享的 ReportPageView，由发布
 * 制品里的 Report Definition 驱动布局，由组件清单驱动组件渲染。因此「预览」
 * 与「发布后查看」呈现的是同一份配置的同一种渲染结果。
 */
export function ReportRuntimePage() {
  const navigate = useNavigate()
  const { reportId = '' } = useParams()
  const query = new URLSearchParams(window.location.search)
  const requestedVersion = Number(query.get('version')) || undefined
  const requestedVersionId = query.get('versionId') || ''
  const shareToken = query.get('share') || ''
  const [loaded, setLoaded] = useState<LoadedReport | null>(null)
  const [manifests, setManifests] = useState<ManifestIndex>(emptyManifestIndex)
  const [assetMeta, setAssetMeta] = useState<ReportAsset | null>(null)
  const [execution, setExecution] = useState<RuntimeExecution | null>(null)
  const [versions, setVersions] = useState<ReportVersion[]>([])
  const [versionsLoading, setVersionsLoading] = useState(false)
  const [versionsError, setVersionsError] = useState('')
  // 加载状态由「已结算的请求令牌」推导，避免在 effect 内同步 setState，
  // 同时保证切换报告或版本时不会短暂展示上一份报告的数据与错误。
  const requestToken = `${reportId}#${requestedVersion ?? requestedVersionId}`
  const [settledToken, setSettledToken] = useState('')
  const [failure, setFailure] = useState<{ token: string; message: string } | null>(null)
  const loading = settledToken !== requestToken
  const loadError = failure?.token === requestToken ? failure.message : ''
  const [refreshing, setRefreshing] = useState(false)
  // 报告级筛选与图表联动共用一份运行态，因此两者对执行请求的影响永远一致。
  const runtimeState = useReportRuntimeState(loaded?.definition)
  const [appliedFilterValues, setAppliedFilterValues] = useState<Record<string, unknown>>({})
  const [versionsOpen, setVersionsOpen] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  const [scheduleOpen, setScheduleOpen] = useState(false)
  const [createDecisionOpen, setCreateDecisionOpen] = useState(false)
  const [moreOpen, setMoreOpen] = useState(false)
  const [toast, setToast] = useState('')
  const abortRef = useRef<AbortController | null>(null)

  const notify = (message: string) => { setToast(message); window.setTimeout(() => setToast(''), 2600) }
  const { replaceFilterValues } = runtimeState

  // 组件清单是渲染器的注册表：没有它就无法确定组件用哪种渲染实现。
  useEffect(() => {
    let cancelled = false
    void listComponentManifests()
      .then(result => { if (!cancelled) setManifests(indexManifests(result.items)) })
      .catch(() => { /* 清单不可用时组件按「未注册模板」显式失败，不做类型猜测。 */ })
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    abortRef.current = controller
    void reportRuntimeAPI.listVersions(reportId)
      .then(async versionPage => {
        const resolvedVersion = requestedVersion ?? versionPage.items.find(item => item.id === requestedVersionId)?.versionNo
        if (requestedVersionId && !resolvedVersion) throw new Error('指定的报告版本不存在或已不可用')
        const runtime = await reportRuntimeAPI.load(reportId, resolvedVersion)
        if (controller.signal.aborted) return
        setLoaded(runtime); setVersions(versionPage.items)
        const page = orderedPages(runtime.definition)[0]
        if (!page) return
        const shared = shareToken ? sessionStorage.getItem(`intelligent-report-share:${shareToken}`) : ''
        let initialFilters: Record<string, unknown> = {}
        if (shared) {
          try {
            const parsed = JSON.parse(shared) as { reportId?: string; versionNo?: number; filterSnapshot?: Record<string, unknown> }
            if (parsed.reportId === reportId && parsed.versionNo === runtime.versionNo) initialFilters = parsed.filterSnapshot ?? {}
          } catch { /* 损坏的分享快照按空筛选处理，服务端权限校验仍然生效。 */ }
        }
        const [value, assets] = await Promise.all([
          reportRuntimeAPI.execute(reportId, { pageId: page.id, filterValues: initialFilters }, { versionNo: runtime.versionNo, signal: controller.signal }),
          reportAssetsAPI.list({ search: runtime.definition.metadata.code, limit: 20 }).catch(() => ({ items: [] as ReportAsset[] })),
        ])
        if (!controller.signal.aborted) {
          setExecution(value)
          setAppliedFilterValues(initialFilters)
          // 分享快照里的取值已经是服务端接受的结构，直接还原即可；
          // 过去要先转成字符串再由用户重新解析，区间类筛选会在这一步失真。
          replaceFilterValues(initialFilters)
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
  }, [replaceFilterValues, reportId, requestedVersion, requestedVersionId, requestToken, shareToken])

  const page = useMemo(() => loaded ? orderedPages(loaded.definition)[0] : undefined, [loaded])
  const title = loaded?.definition.metadata.name || '报告运行页'
  const versionNo = loaded?.versionNo || requestedVersion || 0
  const currentVersion = versions.find(item => item.versionNo === versionNo)
  const ownerName = assetMeta?.ownerName || '报告 Owner'
  const asOf = execution?.asOf || ''
  const timezone = execution?.timezone || 'UTC'
  const filters = loaded?.definition.globalFilters ?? []
  const sections = page ? orderedSections(page) : []
  const results = useMemo(
    () => new Map((execution?.components ?? []).map(item => [item.componentId, item])),
    [execution],
  )
  const run = async (input: ReportExecutionInput, blockId?: string) => {
    if (!loaded || !page) return
    abortRef.current?.abort()
    const controller = new AbortController(); abortRef.current = controller
    setRefreshing(true); setFailure(null)
    try {
      const next = await reportRuntimeAPI.execute(reportId, {
        ...input, visibleBlockIds: blockId ? [blockId] : undefined,
      }, { versionNo: loaded.versionNo, signal: controller.signal })
      setExecution(previous => {
        if (!blockId || !previous) return next
        const refreshed = new Map(next.components.map(item => [item.componentId, item]))
        return { ...next, components: previous.components.map(item => refreshed.get(item.componentId) ?? item) }
      })
      notify(blockId ? '组件已按当前权限重新执行' : '数据已按当前查看者权限刷新')
    } catch (cause) {
      if (!controller.signal.aborted) {
        setFailure({ token: requestToken, message: cause instanceof Error ? cause.message : '报告执行失败' })
      }
    } finally { if (!controller.signal.aborted) setRefreshing(false) }
  }

  const currentInput = () => runtimeState.executionInput(page?.id ?? '')

  const applyFilters = () => {
    setAppliedFilterValues(runtimeState.filterValues)
    void run(currentInput())
  }

  /**
   * 联动点击后立即重新执行：选择本身就是一次筛选，不需要用户再点「应用筛选」。
   * 切换与执行发生在同一个事件里，因此不需要用 effect 去观察状态变化。
   * 服务端只会按定义中声明的联动施加影响。
   */
  const selectComponent = (componentId: string, values: Record<string, unknown>) => {
    const selections = runtimeState.toggleSelection(componentId, values)
    if (page) void run({ pageId: page.id, filterValues: runtimeState.filterValues, selections })
  }

  const openVersions = () => {
    setVersionsOpen(true)
    if (versions.length) return
    setVersionsLoading(true); setVersionsError('')
    void reportRuntimeAPI.listVersions(reportId)
      .then(result => setVersions(result.items))
      .catch(cause => setVersionsError(cause instanceof Error ? cause.message : '版本历史加载失败'))
      .finally(() => setVersionsLoading(false))
  }

  const executionStates = useMemo(() => {
    const ids = placedComponentIDs(page)
    return ids.reduce<Record<'READY' | 'PARTIAL' | 'STALE' | 'LOADING', number>>((counts, id) => {
      const state = refreshing ? 'LOADING' : results.get(id)?.state || 'LOADING'
      if (state in counts) counts[state as keyof typeof counts] += 1
      return counts
    }, { READY: 0, PARTIAL: 0, STALE: 0, LOADING: 0 })
  }, [page, refreshing, results])

  const recoveryItems = useMemo(() => {
    if (!loaded || !page || !execution) return []
    const componentMap = new Map(loaded.definition.components.map(component => [component.id, component]))
    const blockMap = new Map<string, string>()
    page.sections.forEach(section => section.blocks.forEach(block => block.zones.forEach(zone => zone.slots.forEach(slot => {
      if (slot.componentId) blockMap.set(slot.componentId, block.id)
    }))))
    return execution.components.filter(item => item.state !== 'READY').slice(0, 3)
      .map(item => ({ item, component: componentMap.get(item.componentId), blockId: blockMap.get(item.componentId) || '' }))
  }, [execution, loaded, page])

  // 关键变化直接取自定义里的洞察组件文本；没有配置就如实说明，不编造摘要。
  const keyChanges = loaded?.definition.components
    .filter(component => component.options.insightRole)
    .map(component => component.options.richText?.trim())
    .filter((value): value is string => Boolean(value))
    .slice(0, 4) ?? []

  return <AppShell className="report-runtime-shell report-runtime-selected" lockBusinessDomain>
    <div className="runtime-workspace">
      <header className="runtime-report-header">
        <div className="runtime-header-topline">
          <button className="runtime-back" type="button" onClick={() => navigate('/reports')}><ArrowLeft size={15} />报告工作台</button>
          <span>发布画布</span>
        </div>
        <div className="runtime-title-row">
          <div className="runtime-report-identity">
            <div className="runtime-title-labels"><span>智能报告</span><span className="report-publish-badge">已发布</span><span className="report-version-badge">v{versionNo}</span></div>
            <h1>{title}</h1>
            <p>{loaded?.definition.metadata.description || '基于受治理数据生成的可交互分析报告。'}</p>
            <div className="runtime-report-meta">
              <span>负责人 {ownerName}</span>
              <i /><span>更新 {formatDateTime(assetMeta?.updatedAt || currentVersion?.publishedAt)}</span>
              <i /><span>数据截至 {formatDateTime(asOf)}</span>
              <i /><button type="button" onClick={openVersions}>v{versionNo} 版本记录</button>
            </div>
          </div>
          <div className="runtime-header-actions">
            <button className="quiet-button" type="button" disabled={refreshing} onClick={() => void run(currentInput())}><ArrowClockwise className={refreshing ? 'is-spinning' : ''} size={16} />刷新</button>
            <button className="quiet-button" type="button" disabled={!loaded?.versionId} onClick={() => setCreateDecisionOpen(true)}><ShieldCheck size={17} />形成决策</button>
            <button className="quiet-button" type="button" disabled={!assetMeta?.allowedActions.includes('EDIT')} onClick={() => navigate(`/reports/${reportId}?mode=edit`)}><NotePencil size={17} />编辑</button>
            <button className="quiet-button" type="button" disabled={!loaded?.definition.runtimePolicy?.exportEnabled || !assetMeta?.allowedActions.includes('EXPORT')} onClick={() => setExportOpen(true)}><DownloadSimple size={17} />导出</button>
            <div className="runtime-more-wrap">
              <button className="quiet-button runtime-icon-button" type="button" aria-label="更多操作" aria-expanded={moreOpen} onClick={() => setMoreOpen(value => !value)}><DotsThree size={20} weight="bold" /></button>
              {moreOpen && <div className="runtime-more-menu">
                <button type="button" disabled={!assetMeta?.allowedActions.includes('SHARE')} onClick={() => { setMoreOpen(false); setShareOpen(true) }}><ShareNetwork size={15} />分享报告</button>
                <button type="button" disabled={!loaded?.versionId} onClick={() => { setMoreOpen(false); setScheduleOpen(true) }}><CalendarDots size={15} />订阅报告</button>
                <button type="button" onClick={() => { setMoreOpen(false); openVersions() }}><ClockCounterClockwise size={15} />版本历史</button>
                <button type="button" onClick={() => { setMoreOpen(false); navigate('/reports') }}><ArrowLeft size={15} />返回报告中心</button>
              </div>}
            </div>
          </div>
        </div>
      </header>

      {!loading && loaded && <ReportFilterStrip filters={filters} values={runtimeState.filterValues}
        onChange={runtimeState.setFilterValue} onApply={applyFilters} applying={refreshing} />}

      {loading && <div className="runtime-report-feedback"><SpinnerGap className="is-spinning" size={25} /><strong>正在加载不可变发布制品</strong><p>随后会按当前查看者权限执行可见组件。</p></div>}
      {!loading && loadError && !loaded && <div className="runtime-report-feedback is-error"><WarningCircle size={25} /><strong>报告加载失败</strong><p>{loadError}</p><button type="button" onClick={() => window.location.reload()}>重新加载</button></div>}
      {!loading && loaded && <div className="runtime-report-layout">
        <nav className="runtime-section-nav" aria-label="报告章节">
          {sections.map((section, index) => <a className={index === 0 ? 'is-active' : ''} href={`#report-section-${section.id}`} key={section.id}><span />{section.name}</a>)}
        </nav>
        <main className="runtime-report-document">
          {loadError && <div className="runtime-inline-error"><WarningCircle size={15} />{loadError}<button type="button" onClick={() => void run(currentInput())}>重试</button></div>}
          {runtimeState.selections.length > 0 && <div className="runtime-selection-chip" role="status">
            <Funnel size={14} weight="fill" />
            <span>图表联动：{describeSelections(runtimeState.selections)}</span>
            <button type="button" onClick={() => runtimeState.clearSelections()}>清除联动</button>
          </div>}
          {page
            ? <ReportPageView definition={loaded.definition} page={page} manifests={manifests}
              results={results} onRetryBlock={blockId => void run(currentInput(), blockId)}
              interaction={{ roleFor: runtimeState.roleFor, onSelect: selectComponent }} />
            : <div className="runtime-report-level-empty"><WarningCircle size={24} /><strong>发布版本没有可显示页面</strong><p>请联系报告 Owner 检查不可变 Definition。</p></div>}
        </main>
        <aside className="runtime-context-rail">
          <section><h2>关键变化</h2>{keyChanges.length
            ? <ul>{keyChanges.map((item, index) => <li key={index}>{item}</li>)}</ul>
            : <p className="runtime-rail-empty">当前发布版本未配置智能结论。</p>}</section>
          <section><h2>数据状态</h2><p>共 {placedComponentIDs(page).length} 个组件</p>
            <div className="runtime-state-summary">
              <div className="is-ready"><strong>{executionStates.READY}</strong><span>已验证</span></div>
              <div className="is-partial"><strong>{executionStates.PARTIAL}</strong><span>部分数据</span></div>
              <div className="is-stale"><strong>{executionStates.STALE}</strong><span>数据过期</span></div>
              <div className="is-loading"><strong>{executionStates.LOADING}</strong><span>加载中</span></div>
            </div>
            {executionStates.PARTIAL > 0 && <div className="runtime-state-warning"><WarningCircle size={16} weight="fill" /><span>存在部分结果，请查看组件内的数据披露。</span></div>}
          </section>
          <section><h2>报告信息</h2><dl>
            <div><dt>报告版本</dt><dd>v{versionNo}</dd></div>
            <div><dt>发布时间</dt><dd>{formatDateTime(currentVersion?.publishedAt)}</dd></div>
            <div><dt>执行时区</dt><dd>{timezone}</dd></div>
            <div><dt>数据范围</dt><dd>{loaded.definition.components.length} 个发布组件</dd></div>
            <div><dt>负责人</dt><dd>{ownerName}</dd></div>
          </dl><button type="button" onClick={openVersions}>查看版本记录</button></section>
          <section><h2>组件恢复</h2>{recoveryItems.length ? recoveryItems.map(({ item, component, blockId }) => <div className="runtime-recovery-item" key={item.componentId}>
            <span className={`is-${item.state.toLocaleLowerCase()}`} />
            <div><strong>{item.state === 'NO_PERMISSION' ? '受限组件' : `${item.state} - ${component?.options.title || component?.templateRef.type || '组件'}`}</strong><small>{item.errorCode || '可重新执行当前元素'}</small></div>
            {item.state !== 'NO_PERMISSION' && <button type="button" disabled={!blockId || refreshing} onClick={() => void run(currentInput(), blockId)}>尝试恢复</button>}
          </div>) : <p className="runtime-rail-empty"><CheckCircle size={16} weight="fill" />当前组件均已正常完成。</p>}</section>
        </aside>
      </div>}
    </div>
    {versionsOpen && <VersionHistory title={title} currentVersion={versionNo} items={versions} loading={versionsLoading} error={versionsError}
      onClose={() => setVersionsOpen(false)} onSelect={next => navigate(`/reports/${reportId}?version=${next}`)} />}
    {shareOpen && <ShareDialog reportId={reportId} title={title} versionId={loaded?.versionId || ''} filterSnapshot={appliedFilterValues} onClose={() => setShareOpen(false)} onNotify={notify} />}
    {exportOpen && <ExportDialog reportId={reportId} versionNo={versionNo} page={page} filterValues={appliedFilterValues} asOf={asOf} timezone={timezone} onClose={() => setExportOpen(false)} />}
    {scheduleOpen && loaded && <ReportScheduleDialog open reportId={reportId} reportVersionId={loaded.versionId} reportName={title} timezone={timezone}
      canEdit={Boolean(assetMeta?.allowedActions.includes('EDIT'))} onClose={() => setScheduleOpen(false)} onOpenReport={href => { setScheduleOpen(false); navigate(href) }} />}
    {loaded && <CreateDecisionDialog
      open={createDecisionOpen}
      source={{ type: 'REPORT_VERSION', id: loaded.versionId, label: `${title} · 发布版本 v${loaded.versionNo}`, title: `${title}经营决策`, question: `基于${title} v${loaded.versionNo}，下一步应采取什么经营行动？`, expectedEffect: '将报告中的已验证经营发现转化为可审批、可执行并可复盘的行动方案。' }}
      onClose={() => setCreateDecisionOpen(false)}
      onCreated={decisionId => { setCreateDecisionOpen(false); navigate(`/decisions?decisionId=${encodeURIComponent(decisionId)}`) }}
    />}
    {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
    <span className="report-applied-filter-sr" aria-live="polite">已应用 {Object.keys(appliedFilterValues).length} 个筛选</span>
  </AppShell>
}
