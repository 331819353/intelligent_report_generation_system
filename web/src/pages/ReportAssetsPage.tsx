import {
  Archive, CaretDown, Check, ClockCounterClockwise, DotsThreeVertical, Eye, MagnifyingGlass,
  NotePencil, Plus, ShieldCheck, UploadSimple, WarningCircle, X,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { reportAssetsAPI, type AssetEvent, type PermissionGrant } from '../report/api/assets'
import { reportAssetFixtures } from '../report/assets/fixtures'
import { canRun, filterAssets, lifecycleLabels, type ReportAction, type ReportAsset, type ReportLifecycle, type ReportScope } from '../report/assets/model'
import { MiniReportPreview } from '../report/runtime/ReportCharts'

const avatarFallbacks = ['/report-assets/avatars/wang-min.png', '/report-assets/avatars/liu-yang.png', '/report-assets/avatars/chen-chen.png']

function avatarFor(asset: ReportAsset) {
  if (asset.ownerAvatar) return asset.ownerAvatar
  const score = [...asset.ownerUserId].reduce((total, value) => total + value.charCodeAt(0), 0)
  return avatarFallbacks[score % avatarFallbacks.length]
}

function formatUpdatedAt(value: string) {
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value)).replace('/', '-').replace(' ', ' ')
}

function AssetCard({ asset, selected, onSelect }: { asset: ReportAsset; selected: boolean; onSelect: () => void }) {
  return <article className={`report-asset-card ${selected ? 'is-selected' : ''}`.trim()}>
    <button className="report-asset-select" type="button" aria-label={`选择${asset.name}`} aria-pressed={selected} onClick={onSelect}>
      <MiniReportPreview kind={asset.previewKind} label={asset.name} />
      {selected && <span className="report-card-check"><Check size={13} weight="bold" /></span>}
    </button>
    <div className="report-asset-card-body">
      <div className="report-card-title-row"><strong>{asset.name}</strong><button type="button" aria-label={`${asset.name}更多操作`}><DotsThreeVertical size={17} /></button></div>
      <span className="report-code">{asset.code}</span>
      <div className="report-owner-row"><img src={avatarFor(asset)} alt="" /><span>{asset.ownerName}</span></div>
      <div className="report-card-meta"><span>{asset.currentVersionNo ? `v${asset.currentVersionNo}` : `r${asset.draftRevisionNo}`}</span><span>更新于 {formatUpdatedAt(asset.updatedAt)}</span></div>
      <span className={`report-lifecycle is-${asset.lifecycle.toLocaleLowerCase()}`}>{lifecycleLabels[asset.lifecycle]}</span>
    </div>
  </article>
}

const permissionActions = [
  ['VIEW', '查看'], ['EDIT', '编辑'], ['PUBLISH', '发布'], ['EXPORT', '导出'], ['SHARE', '分享'], ['AI_EDIT', 'AI 编辑'],
] as const

const snapshotGrants: PermissionGrant[] = [
  { id: 'snapshot-1', subjectType: 'ROLE', subjectId: 'snapshot-role-1', subjectName: '经营分析组', action: 'VIEW', createdAt: '' },
  { id: 'snapshot-2', subjectType: 'ROLE', subjectId: 'snapshot-role-1', subjectName: '经营分析组', action: 'EDIT', createdAt: '' },
  { id: 'snapshot-3', subjectType: 'ROLE', subjectId: 'snapshot-role-2', subjectName: '供应链负责人', action: 'VIEW', createdAt: '' },
  { id: 'snapshot-4', subjectType: 'USER', subjectId: 'snapshot-user-1', subjectName: '王敏', action: 'VIEW', createdAt: '' },
  { id: 'snapshot-5', subjectType: 'USER', subjectId: 'snapshot-user-1', subjectName: '王敏', action: 'EDIT', createdAt: '' },
  { id: 'snapshot-6', subjectType: 'USER', subjectId: 'snapshot-user-1', subjectName: '王敏', action: 'PUBLISH', createdAt: '' },
]

function PermissionDialog({ asset, snapshot, onClose, onChanged }: { asset: ReportAsset; snapshot: boolean; onClose: () => void; onChanged: () => void }) {
  const [grants, setGrants] = useState<PermissionGrant[]>(snapshot ? snapshotGrants : [])
  const [loading, setLoading] = useState(!snapshot)
  const [busyKey, setBusyKey] = useState('')
  const [error, setError] = useState('')
  const [subjectType, setSubjectType] = useState<'USER' | 'ROLE'>('USER')
  const [subjectId, setSubjectId] = useState('')
  const [subjectAction, setSubjectAction] = useState<PermissionGrant['action']>('VIEW')

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    void reportAssetsAPI.listPermissions(asset.id).then(result => { if (!cancelled) setGrants(result.items) })
      .catch(cause => { if (!cancelled) setError(cause instanceof Error ? cause.message : '权限加载失败') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [asset.id, snapshot])

  const rows = useMemo(() => {
    const grouped = new Map<string, { subjectType: 'USER' | 'ROLE'; subjectId: string; subjectName: string; grants: Map<PermissionGrant['action'], PermissionGrant> }>()
    grants.forEach(grant => {
      const key = `${grant.subjectType}:${grant.subjectId}`
      const row = grouped.get(key) ?? { subjectType: grant.subjectType, subjectId: grant.subjectId, subjectName: grant.subjectName || grant.subjectId, grants: new Map() }
      row.grants.set(grant.action, grant)
      grouped.set(key, row)
    })
    return [...grouped.values()]
  }, [grants])

  const toggle = async (row: (typeof rows)[number], action: PermissionGrant['action']) => {
    const existing = row.grants.get(action)
    const key = `${row.subjectType}:${row.subjectId}:${action}`
    setBusyKey(key); setError('')
    try {
      if (snapshot) {
        setGrants(items => existing ? items.filter(item => item.id !== existing.id) : [...items, { id: `snapshot-${Date.now()}`, subjectType: row.subjectType, subjectId: row.subjectId, subjectName: row.subjectName, action, createdAt: '' }])
      } else if (existing) {
        await reportAssetsAPI.revokePermission(asset.id, existing.id)
        setGrants(items => items.filter(item => item.id !== existing.id))
      } else {
        const created = await reportAssetsAPI.grantPermission(asset.id, { subjectType: row.subjectType, subjectId: row.subjectId, action })
        setGrants(items => [...items, created])
        onChanged()
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '权限更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const addGrant = async () => {
    if (!subjectId.trim()) return
    setBusyKey('new'); setError('')
    try {
      if (snapshot) {
        setGrants(items => [...items, { id: `snapshot-${Date.now()}`, subjectType, subjectId: subjectId.trim(), subjectName: subjectId.trim(), action: subjectAction, createdAt: '' }])
      } else {
        const created = await reportAssetsAPI.grantPermission(asset.id, { subjectType, subjectId: subjectId.trim(), action: subjectAction })
        setGrants(items => items.some(item => item.id === created.id) ? items : [...items, created])
        onChanged()
      }
      setSubjectId('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '新增授权失败，请确认用户或角色 ID')
    } finally {
      setBusyKey('')
    }
  }

  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal is-permission" role="dialog" aria-modal="true" aria-labelledby="report-permission-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">报告权限</span><h2 id="report-permission-title">{asset.name}</h2></div><button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      <p className="report-permission-note"><ShieldCheck size={16} weight="fill" />报告权限只控制对象操作，不能扩大任何人的数据权限。</p>
      {!snapshot && <div className="report-permission-add">
        <select aria-label="授权主体类型" value={subjectType} onChange={event => setSubjectType(event.target.value as 'USER' | 'ROLE')}><option value="USER">用户</option><option value="ROLE">角色</option></select>
        <input aria-label="用户或角色 ID" value={subjectId} onChange={event => setSubjectId(event.target.value)} placeholder="输入用户或角色 UUID" />
        <select aria-label="授权操作" value={subjectAction} onChange={event => setSubjectAction(event.target.value as PermissionGrant['action'])}>{permissionActions.map(([action, label]) => <option key={action} value={action}>{label}</option>)}</select>
        <button className="quiet-button" type="button" disabled={!subjectId.trim() || busyKey === 'new'} onClick={() => void addGrant()}>添加授权</button>
      </div>}
      {error && <p className="report-permission-error"><WarningCircle size={15} />{error}</p>}
      <div className="report-permission-table" role="table" aria-label="报告授权">
        <div role="row" className="is-header"><span role="columnheader">用户或角色</span>{permissionActions.map(([action, label]) => <span role="columnheader" key={action}>{label}</span>)}</div>
        {loading && <p className="report-permission-empty">正在加载授权…</p>}
        {!loading && rows.length === 0 && <p className="report-permission-empty">暂无额外授权，仅 Owner 可管理。</p>}
        {rows.map(row => <div role="row" key={`${row.subjectType}:${row.subjectId}`}>
          <span role="cell"><strong>{row.subjectName}</strong><small>{row.subjectType === 'ROLE' ? '角色' : '用户'}</small></span>
          {permissionActions.map(([action]) => <label role="cell" key={action}><input type="checkbox" disabled={Boolean(busyKey)} checked={row.grants.has(action)} onChange={() => void toggle(row, action)} /><span /></label>)}
        </div>)}
      </div>
      <footer><button className="primary-button" type="button" onClick={onClose}>完成</button></footer>
    </section>
  </div>
}

function LifecycleDialog({ asset, restore = false, busy = false, error = '', onClose, onConfirm }: { asset: ReportAsset; restore?: boolean; busy?: boolean; error?: string; onClose: () => void; onConfirm: (reason: string) => void }) {
  const [reason, setReason] = useState('')
  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal is-archive" role="dialog" aria-modal="true" aria-labelledby="report-archive-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">资产生命周期</span><h2 id="report-archive-title">{restore ? '重新上架' : '下架'}“{asset.name}”</h2></div><button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      <p>{restore ? '重新上架前会校验已发布制品、组件版本与数据依赖；校验失败时报告保持下架。' : '下架后普通访问和已有分享将立即关闭；草稿、修订、发布版本与审计历史会完整保留。'}</p>
      {error && <p className="report-lifecycle-error"><WarningCircle size={15} />{error}</p>}
      <label>{restore ? '上架原因' : '下架原因'}<textarea value={reason} maxLength={1000} onChange={event => setReason(event.target.value)} placeholder={`请输入${restore ? '上架' : '下架'}原因`} /></label>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className={restore ? 'primary-button' : 'danger-button'} type="button" disabled={!reason.trim() || busy} onClick={() => onConfirm(reason.trim())}>{busy ? '处理中…' : `确认${restore ? '上架' : '下架'}`}</button></footer>
    </section>
  </div>
}

function eventLabel(event: AssetEvent, asset: ReportAsset) {
  const actor = event.actorName || asset.ownerName
  const version = typeof event.payload.versionNo === 'number' ? ` v${event.payload.versionNo}` : ''
  const labels: Record<string, string> = {
    CREATED: `${actor} 创建了报告`, OWNER_CHANGED: `${actor} 变更了 Owner`, PUBLISHED: `${actor} 发布了${version} 版本`,
    ROLLED_BACK: `${actor} 回滚了发布版本`, PERMISSION_GRANTED: `${actor} 新增了 ${event.action ?? ''} 授权`,
    PERMISSION_REVOKED: `${actor} 撤销了 ${event.action ?? ''} 授权`, ARCHIVED: `${actor} 下架了报告`,
    RESTORED: `${actor} 重新上架了报告`, SHARE_CREATED: `${actor} 创建了分享`, SHARE_REVOKED: `${actor} 撤销了分享`,
  }
  return labels[event.eventType] ?? `${actor} 更新了报告资产`
}

function AssetInspector({ asset, events, eventsLoading, onView, onEdit, onPublish, onPermissions, onArchive, onRestore }: {
  asset: ReportAsset
  events: AssetEvent[]
  eventsLoading: boolean
  onView: () => void
  onEdit: () => void
  onPublish: () => void
  onPermissions: () => void
  onArchive: () => void
  onRestore: () => void
}) {
  return <aside className="report-asset-inspector" aria-label={`${asset.name}资产详情`}>
    <header><div><h2>{asset.name}</h2><span className={`report-lifecycle is-${asset.lifecycle.toLocaleLowerCase()}`}>{lifecycleLabels[asset.lifecycle]}</span></div><button type="button" aria-label="关闭详情"><X size={17} /></button></header>
    <dl className="report-asset-overview">
      <div><dt>Owner</dt><dd><img src={avatarFor(asset)} alt="" />{asset.ownerName}</dd></div>
      <div><dt>当前版本</dt><dd>{asset.currentVersionNo ? `v${asset.currentVersionNo}（已发布）` : '尚未发布'}</dd></div>
      <div><dt>草稿版本</dt><dd>r{asset.draftRevisionNo}</dd></div>
    </dl>
    {asset.lifecycle === 'CHANGED' && <p className="report-change-note"><span />{asset.unpublishedChanges} 项修改尚未发布</p>}
    {asset.lifecycle === 'OFFLINE' && <p className="report-offline-note">普通访问已关闭，历史版本仍保留。</p>}
    <div className="report-inspector-actions">
      {canRun(asset, 'PUBLISH') && <button className="primary-button" type="button" onClick={onPublish}><UploadSimple size={16} />{asset.currentVersionNo ? '发布新版本' : '发布报告'}</button>}
      <div>
        {canRun(asset, 'VIEW') && <button type="button" onClick={onView}><Eye size={16} />查看</button>}
        {canRun(asset, 'EDIT') && <button type="button" onClick={onEdit}><NotePencil size={16} />继续编辑</button>}
        {canRun(asset, 'RESTORE') && <button type="button" onClick={onRestore}><UploadSimple size={16} />重新上架</button>}
      </div>
    </div>
    <section className="report-inspector-section">
      <div className="report-inspector-section-title"><h3>权限管理</h3>{canRun(asset, 'PERMISSIONS') && <button type="button" onClick={onPermissions}>管理权限</button>}</div>
      <div className="report-avatar-stack" aria-label={`${asset.visibleCount}人可访问`}>
        <img src="/report-assets/avatars/wang-min.png" alt="" /><img src="/report-assets/avatars/liu-yang.png" alt="" /><img src="/report-assets/avatars/chen-chen.png" alt="" /><span>+{Math.max(asset.visibleCount - 3, 0)}</span>
      </div>
      <p>可见范围：{asset.visibleCount} 人可访问 · {asset.editableCount} 人可编辑</p>
    </section>
    <section className="report-inspector-section report-lifecycle-timeline">
      <h3>生命周期</h3>
      <ol>
        {eventsLoading && <li><span /><div><strong>正在加载生命周期记录…</strong></div></li>}
        {!eventsLoading && events.slice(0, 3).map(event => <li key={event.id}><span className={event.eventType === 'ARCHIVED' ? 'is-warning' : 'is-success'} /><div><strong>{eventLabel(event, asset)}</strong><time>{formatUpdatedAt(event.createdAt)}{event.reason ? ` · ${event.reason}` : ''}</time></div></li>)}
        {!eventsLoading && events.length === 0 && <li><span /><div><strong>暂无生命周期记录</strong></div></li>}
      </ol>
      <button className="report-history-button" type="button"><ClockCounterClockwise size={15} />共 {events.length} 条记录</button>
    </section>
    {canRun(asset, 'ARCHIVE') && asset.lifecycle !== 'OFFLINE' && <button className="report-archive-action" type="button" onClick={onArchive}><Archive size={17} />下架报告</button>}
  </aside>
}

export function ReportAssetsPage() {
  const navigate = useNavigate()
  const snapshot = new URLSearchParams(window.location.search).get('snapshot') === 'assets'
  const [assets, setAssets] = useState<ReportAsset[]>(snapshot ? reportAssetFixtures : [])
  const [loading, setLoading] = useState(!snapshot)
  const [error, setError] = useState('')
  const [scope, setScope] = useState<ReportScope>('all')
  const [lifecycle, setLifecycle] = useState<ReportLifecycle | 'ALL'>('ALL')
  const [query, setQuery] = useState('')
  const [selectedID, setSelectedID] = useState(snapshot ? reportAssetFixtures[0].id : '')
  const [nextCursor, setNextCursor] = useState('')
  const [loadingMore, setLoadingMore] = useState(false)
  const [events, setEvents] = useState<AssetEvent[]>([])
  const [eventsReportID, setEventsReportID] = useState('')
  const [permissionAsset, setPermissionAsset] = useState<ReportAsset | null>(null)
  const [archiveAsset, setArchiveAsset] = useState<ReportAsset | null>(null)
  const [restoreAsset, setRestoreAsset] = useState<ReportAsset | null>(null)
  const [transitionBusy, setTransitionBusy] = useState(false)
  const [transitionError, setTransitionError] = useState('')
  const [toast, setToast] = useState('')

  const loadAssets = useCallback(async (cursor = '', append = false) => {
    if (snapshot) return
    if (append) setLoadingMore(true)
    else setLoading(true)
    if (!append) setError('')
    try {
      const page = await reportAssetsAPI.list({ scope, lifecycle, search: query, cursor: cursor || undefined, limit: 60 })
      setAssets(items => append ? [...items, ...page.items] : page.items)
      setNextCursor(page.nextCursor ?? '')
      if (!append) setSelectedID(current => page.items.some(item => item.id === current) ? current : page.items[0]?.id ?? '')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '报告资产加载失败')
    } finally {
      setLoading(false); setLoadingMore(false)
    }
  }, [lifecycle, query, scope, snapshot])

  useEffect(() => {
    if (snapshot) return undefined
    const timeout = window.setTimeout(() => void loadAssets(), 220)
    return () => window.clearTimeout(timeout)
  }, [loadAssets, snapshot])

  const visibleAssets = useMemo(() => snapshot ? filterAssets(assets, scope, lifecycle, query) : assets, [assets, lifecycle, query, scope, snapshot])
  const selected = assets.find(asset => asset.id === selectedID) ?? visibleAssets[0] ?? null
  const snapshotEvents = useMemo<AssetEvent[]>(() => selected ? [
    { id: `${selected.id}-1`, eventType: 'PUBLISHED', actorName: selected.ownerName, payload: { versionNo: selected.currentVersionNo ?? 1 }, createdAt: '2026-08-07T16:24:00+08:00' },
    { id: `${selected.id}-2`, eventType: 'PERMISSION_GRANTED', actorName: selected.ownerName, action: 'EDIT', payload: {}, createdAt: '2026-08-10T10:12:00+08:00' },
  ] : [], [selected])
  const notify = (message: string) => { setToast(message); window.setTimeout(() => setToast(''), 2400) }
  const updateAsset = (id: string, update: (asset: ReportAsset) => ReportAsset) => setAssets(items => items.map(item => item.id === id ? update(item) : item))

  const publish = (asset: ReportAsset) => {
    if (!snapshot) {
      navigate(`/reports/${asset.id}?mode=publish`)
      return
    }
    updateAsset(asset.id, item => ({ ...item, lifecycle: 'PUBLISHED', currentVersionNo: (item.currentVersionNo ?? 0) + 1, unpublishedChanges: 0 }))
    notify(`“${asset.name}”已生成新的不可变发布版本`)
  }

  useEffect(() => {
    if (snapshot || !selected) return undefined
    let cancelled = false
    void reportAssetsAPI.listEvents(selected.id).then(result => {
      if (!cancelled) { setEvents(result.items); setEventsReportID(selected.id) }
    }).catch(() => {
      if (!cancelled) { setEvents([]); setEventsReportID(selected.id) }
    })
    return () => { cancelled = true }
  }, [selected, snapshot])

  const transition = async (asset: ReportAsset, reason: string, restore: boolean) => {
    setTransitionBusy(true); setTransitionError('')
    try {
      if (!snapshot) await (restore ? reportAssetsAPI.restore(asset.id, reason) : reportAssetsAPI.archive(asset.id, reason))
      if (snapshot) updateAsset(asset.id, item => ({
        ...item,
        lifecycle: restore ? (item.currentVersionNo ? 'PUBLISHED' : 'DRAFT_ONLY') : 'OFFLINE',
        allowedActions: restore
          ? [...new Set<ReportAction>([...item.allowedActions.filter(action => action !== 'RESTORE'), 'VIEW', 'EDIT', 'PUBLISH', 'ARCHIVE'])]
          : ['VERSIONS', 'PERMISSIONS', 'RESTORE'],
      }))
      else await loadAssets()
      setArchiveAsset(null); setRestoreAsset(null)
      notify(`“${asset.name}”已${restore ? '通过校验并重新上架' : '下架，历史版本仍保留'}`)
    } catch (cause) {
      setTransitionError(cause instanceof Error ? cause.message : `${restore ? '上架' : '下架'}失败`)
    } finally {
      setTransitionBusy(false)
    }
  }

  return <AppShell className="report-assets-shell" eyebrow="智能报告" title="报告资产中心" lockBusinessDomain actions={<button className="primary-button report-new-button" type="button" onClick={() => navigate(snapshot ? '/reports/new?snapshot=runtime-draft' : '/reports/new')}><Plus size={17} weight="bold" />新建报告</button>}>
    <p className="report-page-subtitle">管理当前领域内的报告、权限与发布状态</p>
    <div className="report-assets-layout">
      <section className="report-assets-main" aria-label="报告资产列表">
        <div className="report-assets-toolbar">
          <label className="report-search"><MagnifyingGlass size={17} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索报告名称或编码" aria-label="搜索报告名称或编码" /></label>
          <div className="report-scope-tabs" role="tablist" aria-label="资产范围">
            {([['all', '全部可见'], ['mine', '我创建'], ['shared', '共享给我']] as const).map(([value, label]) => <button key={value} type="button" role="tab" aria-selected={scope === value} onClick={() => setScope(value)}>{label}</button>)}
          </div>
          <label className="report-status-filter">状态：<select value={lifecycle} onChange={event => setLifecycle(event.target.value as ReportLifecycle | 'ALL')}><option value="ALL">全部</option><option value="DRAFT_ONLY">待发布</option><option value="CHANGED">有未发布修改</option><option value="PUBLISHED">已发布</option><option value="OFFLINE">已下架</option></select><CaretDown size={14} aria-hidden="true" /></label>
        </div>
        {loading && <div className="report-assets-feedback"><span className="report-loading-spinner" />正在加载报告资产…</div>}
        {!loading && error && <div className="report-assets-feedback is-error"><WarningCircle size={22} />{error}<button type="button" onClick={() => window.location.reload()}>重试</button></div>}
        {!loading && !error && visibleAssets.length === 0 && <div className="report-assets-feedback"><Archive size={24} />没有符合条件的报告资产<button type="button" onClick={() => { setQuery(''); setLifecycle('ALL'); setScope('all') }}>清除筛选</button></div>}
        {!loading && !error && visibleAssets.length > 0 && <div className="report-asset-grid">{visibleAssets.map(asset => <AssetCard key={asset.id} asset={asset} selected={asset.id === selected?.id} onSelect={() => setSelectedID(asset.id)} />)}</div>}
        {!loading && !error && visibleAssets.length > 0 && (nextCursor ? <button className="report-load-more" type="button" disabled={loadingMore} onClick={() => void loadAssets(nextCursor, true)}>{loadingMore ? '正在加载…' : '加载更多报告'}</button> : <p className="report-assets-end">— 已加载全部 {visibleAssets.length} 项 —</p>)}
      </section>
      {selected && <AssetInspector
        asset={selected}
        events={snapshot ? snapshotEvents : eventsReportID === selected.id ? events : []}
        eventsLoading={!snapshot && eventsReportID !== selected.id}
        onView={() => navigate(snapshot ? `/reports/${selected.id}?snapshot=runtime` : `/reports/${selected.id}`)}
        onEdit={() => navigate(snapshot ? `/reports/${selected.id}?snapshot=runtime-draft` : `/reports/${selected.id}?mode=edit`)}
        onPublish={() => publish(selected)}
        onPermissions={() => setPermissionAsset(selected)}
        onArchive={() => { setTransitionError(''); setArchiveAsset(selected) }}
        onRestore={() => { setTransitionError(''); setRestoreAsset(selected) }}
      />}
    </div>
    {permissionAsset && <PermissionDialog asset={permissionAsset} snapshot={snapshot} onChanged={() => { if (!snapshot) void loadAssets() }} onClose={() => setPermissionAsset(null)} />}
    {archiveAsset && <LifecycleDialog asset={archiveAsset} busy={transitionBusy} error={transitionError} onClose={() => setArchiveAsset(null)} onConfirm={reason => void transition(archiveAsset, reason, false)} />}
    {restoreAsset && <LifecycleDialog asset={restoreAsset} restore busy={transitionBusy} error={transitionError} onClose={() => setRestoreAsset(null)} onConfirm={reason => void transition(restoreAsset, reason, true)} />}
    {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
  </AppShell>
}
