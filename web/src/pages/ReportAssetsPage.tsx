import {
  Archive, Check, ClockCounterClockwise, DotsThreeVertical, Eye, Funnel, ListBullets,
  MagnifyingGlass, NotePencil, Plus, ShareNetwork, ShieldCheck, SortAscending, SquaresFour,
  UploadSimple, Users, WarningCircle, X,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AppButton } from '../components/AppButton'
import { reportAssetsAPI, type AssetEvent, type PermissionGrant } from '../report/api/assets'
import { reportAssetFixtures } from '../report/assets/fixtures'
import { canRun, filterAssets, lifecycleLabels, type ReportAction, type ReportAsset, type ReportLifecycle, type ReportScope } from '../report/assets/model'
import { MiniReportPreview, ReportWorkspacePreview } from '../report/runtime/ReportCharts'

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

function AssetListRow({ asset, selected, onSelect }: { asset: ReportAsset; selected: boolean; onSelect: () => void }) {
  return <article className={`report-library-row ${selected ? 'is-selected' : ''}`.trim()}>
    <AppButton text className="report-library-row-main" type="button" aria-pressed={selected} onClick={onSelect}>
      <span className="report-library-row-identity">
        <span className="report-library-thumb"><MiniReportPreview kind={asset.previewKind} label={asset.name} /></span>
        <span><strong>{asset.name}</strong><small>{asset.code}</small></span>
      </span>
      <span className="report-library-owner"><img src={avatarFor(asset)} alt="" />{asset.ownerName}</span>
      <span className="report-library-version">{asset.currentVersionNo ? `v${asset.currentVersionNo}` : '—'} <small>/ r{asset.draftRevisionNo}</small></span>
      <span className={`report-lifecycle is-${asset.lifecycle.toLocaleLowerCase()}`}>{lifecycleLabels[asset.lifecycle]}</span>
      <span className="report-library-updated"><time>{formatUpdatedAt(asset.updatedAt)}</time><small>{asset.ownerName} 更新</small></span>
      <span className="report-library-access">企业经营<small>{asset.visibleCount} 人可查看</small></span>
    </AppButton>
    <AppButton text circle className="report-library-more" type="button" aria-label={`${asset.name}更多操作`} onClick={onSelect}><DotsThreeVertical size={17} weight="bold" /></AppButton>
  </article>
}

function AssetGridCard({ asset, selected, onSelect }: { asset: ReportAsset; selected: boolean; onSelect: () => void }) {
  return <AppButton text className={`report-library-grid-card ${selected ? 'is-selected' : ''}`.trim()} type="button" aria-pressed={selected} onClick={onSelect}>
    <MiniReportPreview kind={asset.previewKind} label={asset.name} />
    <span className="report-library-grid-heading"><strong>{asset.name}</strong><span className={`report-lifecycle is-${asset.lifecycle.toLocaleLowerCase()}`}>{lifecycleLabels[asset.lifecycle]}</span></span>
    <small>{asset.code}</small>
    <span><img src={avatarFor(asset)} alt="" />{asset.ownerName}<time>{formatUpdatedAt(asset.updatedAt)}</time></span>
  </AppButton>
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
      <header><div><span className="eyebrow">报告权限</span><h2 id="report-permission-title">{asset.name}</h2></div><AppButton text circle type="button" aria-label="关闭" onClick={onClose}><X size={18} /></AppButton></header>
      <p className="report-permission-note"><ShieldCheck size={16} weight="fill" />报告权限只控制对象操作，不能扩大任何人的数据权限。</p>
      {!snapshot && <div className="report-permission-add">
        <select aria-label="授权主体类型" value={subjectType} onChange={event => setSubjectType(event.target.value as 'USER' | 'ROLE')}><option value="USER">用户</option><option value="ROLE">角色</option></select>
        <input aria-label="用户或角色 ID" value={subjectId} onChange={event => setSubjectId(event.target.value)} placeholder="输入用户或角色 UUID" />
        <select aria-label="授权操作" value={subjectAction} onChange={event => setSubjectAction(event.target.value as PermissionGrant['action'])}>{permissionActions.map(([action, label]) => <option key={action} value={action}>{label}</option>)}</select>
        <AppButton plain size="small" type="button" disabled={!subjectId.trim() || busyKey === 'new'} onClick={() => void addGrant()}>添加授权</AppButton>
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
      <footer><AppButton variant="primary" size="small" type="button" onClick={onClose}>完成</AppButton></footer>
    </section>
  </div>
}

function ShareDialog({ asset, snapshot, onClose }: { asset: ReportAsset; snapshot: boolean; onClose: () => void }) {
  const [shareType, setShareType] = useState<'INTERNAL_USER' | 'INTERNAL_GROUP'>('INTERNAL_USER')
  const [principalId, setPrincipalId] = useState('')
  const [expiresOn, setExpiresOn] = useState('2026-09-09')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [shareURL, setShareURL] = useState('')

  const create = async () => {
    if (!principalId.trim()) return
    setBusy(true); setError('')
    try {
      const token = snapshot
        ? 'snapshot-secure-share-token'
        : (await reportAssetsAPI.createShare(asset.id, { shareType, principalId: principalId.trim(), expiresAt: new Date(`${expiresOn}T23:59:59`).toISOString() })).token
      setShareURL(`${window.location.origin}/api/v1/report-shares/${encodeURIComponent(token)}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '分享创建失败')
    } finally {
      setBusy(false)
    }
  }

  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal is-share" role="dialog" aria-modal="true" aria-labelledby="report-share-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">安全分享</span><h2 id="report-share-title">分享“{asset.name}”</h2></div><AppButton text circle type="button" aria-label="关闭" onClick={onClose}><X size={18} /></AppButton></header>
      <p className="report-permission-note"><ShieldCheck size={16} weight="fill" />链接仅定位报告，访问时仍会校验接收人的报告权限与数据权限。</p>
      {!shareURL && <div className="report-share-form">
        <label>接收对象<select value={shareType} onChange={event => setShareType(event.target.value as typeof shareType)}><option value="INTERNAL_USER">内部用户</option><option value="INTERNAL_GROUP">内部用户组</option></select></label>
        <label>用户或用户组 ID<input value={principalId} onChange={event => setPrincipalId(event.target.value)} placeholder="输入 UUID" /></label>
        <label>有效期至<input type="date" value={expiresOn} onChange={event => setExpiresOn(event.target.value)} /></label>
      </div>}
      {error && <p className="report-permission-error"><WarningCircle size={15} />{error}</p>}
      {shareURL && <div className="report-share-result"><Check size={18} weight="bold" /><div><strong>安全分享已创建</strong><span>{shareURL}</span></div><AppButton plain size="small" type="button" onClick={() => void navigator.clipboard.writeText(shareURL)}>复制链接</AppButton></div>}
      <footer><AppButton plain size="small" type="button" disabled={busy} onClick={onClose}>{shareURL ? '完成' : '取消'}</AppButton>{!shareURL && <AppButton variant="primary" size="small" type="button" disabled={busy || !principalId.trim() || !expiresOn} onClick={() => void create()}>{busy ? '正在创建…' : '创建分享'}</AppButton>}</footer>
    </section>
  </div>
}

function LifecycleDialog({ asset, restore = false, busy = false, error = '', onClose, onConfirm }: { asset: ReportAsset; restore?: boolean; busy?: boolean; error?: string; onClose: () => void; onConfirm: (reason: string) => void }) {
  const [reason, setReason] = useState('')
  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal is-archive" role="dialog" aria-modal="true" aria-labelledby="report-archive-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">资产生命周期</span><h2 id="report-archive-title">{restore ? '重新上架' : '下架'}“{asset.name}”</h2></div><AppButton text circle type="button" aria-label="关闭" onClick={onClose}><X size={18} /></AppButton></header>
      <p>{restore ? '重新上架前会校验已发布制品、组件版本与数据依赖；校验失败时报告保持下架。' : '下架后普通访问和已有分享将立即关闭；草稿、修订、发布版本与审计历史会完整保留。'}</p>
      {error && <p className="report-lifecycle-error"><WarningCircle size={15} />{error}</p>}
      <label>{restore ? '上架原因' : '下架原因'}<textarea value={reason} maxLength={1000} onChange={event => setReason(event.target.value)} placeholder={`请输入${restore ? '上架' : '下架'}原因`} /></label>
      <footer><AppButton plain size="small" type="button" disabled={busy} onClick={onClose}>取消</AppButton><AppButton variant={restore ? 'primary' : 'danger'} size="small" type="button" disabled={!reason.trim() || busy} onClick={() => onConfirm(reason.trim())}>{busy ? '处理中…' : `确认${restore ? '上架' : '下架'}`}</AppButton></footer>
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

function ReportAssetDrawer({ asset, events, eventsLoading, onClose, onView, onEdit, onPublish, onPermissions, onShare, onArchive, onRestore }: {
  asset: ReportAsset
  events: AssetEvent[]
  eventsLoading: boolean
  onClose: () => void
  onView: () => void
  onEdit: () => void
  onPublish: () => void
  onPermissions: () => void
  onShare: () => void
  onArchive: () => void
  onRestore: () => void
}) {
  const [showAllEvents, setShowAllEvents] = useState(false)
  return <aside className="report-asset-drawer" aria-label={`${asset.name}资产详情`}>
    <header className="report-drawer-header">
      <AppButton text circle className="report-drawer-close" type="button" aria-label="关闭报告详情" onClick={onClose}><X size={18} /></AppButton>
      <h2>{asset.name}</h2>
      <span>{asset.code}</span>
      <div className="report-drawer-actions">
        {canRun(asset, 'EDIT') && <AppButton variant="primary" size="small" type="button" onClick={onEdit}><NotePencil size={16} />继续编辑</AppButton>}
        {canRun(asset, 'VIEW') && <AppButton plain size="small" type="button" onClick={onView}><Eye size={16} />查看报告</AppButton>}
        {canRun(asset, 'PUBLISH') && <AppButton plain size="small" type="button" onClick={onPublish}><UploadSimple size={16} />{asset.currentVersionNo ? '发布新版本' : '发布报告'}</AppButton>}
        {canRun(asset, 'RESTORE') && <AppButton variant="primary" size="small" type="button" onClick={onRestore}><UploadSimple size={16} />重新上架</AppButton>}
        <AppButton plain circle size="small" className="report-drawer-more" type="button" aria-label="更多操作"><DotsThreeVertical size={18} weight="bold" /></AppButton>
      </div>
    </header>

    <div className="report-drawer-scroll">
      <dl className="report-drawer-meta">
        <div><dt>所有者</dt><dd><img src={avatarFor(asset)} alt="" />{asset.ownerName}</dd></div>
        <div><dt>当前版本 / 草稿</dt><dd>{asset.currentVersionNo ? `v${asset.currentVersionNo}` : '—'} / r{asset.draftRevisionNo}</dd></div>
        <div><dt>状态</dt><dd><span className={`report-lifecycle is-${asset.lifecycle.toLocaleLowerCase()}`}>{lifecycleLabels[asset.lifecycle]}</span></dd></div>
        <div><dt>最后更新</dt><dd>{formatUpdatedAt(asset.updatedAt)} · {asset.ownerName}</dd></div>
        <div><dt>数据新鲜度</dt><dd>打开报告后按查看者权限计算</dd></div>
        <div><dt>可见范围</dt><dd>企业经营 · {asset.visibleCount} 人可查看</dd></div>
        <div className="is-wide"><dt>描述</dt><dd>{asset.reportType === 'DASHBOARD' ? '持续监测关键经营指标与异常变化。' : '围绕经营目标、核心指标与变化原因形成的受治理分析报告。'}</dd></div>
        <div className="is-wide"><dt>标签</dt><dd className="report-drawer-tags"><span>经营分析</span><span>{asset.reportType === 'DASHBOARD' ? '看板' : '月度报告'}</span><span>企业经营</span></dd></div>
      </dl>

      <section className="report-drawer-section report-drawer-preview">
        <h3>预览</h3>
        <ReportWorkspacePreview asset={asset} />
      </section>

      <div className="report-drawer-lower-grid">
        <section className="report-drawer-section report-drawer-lifecycle">
          <header><h3>生命周期</h3><span>{events.length ? `共 ${events.length} 条` : ''}</span></header>
          <ol>
            {eventsLoading && <li><span /><div><strong>正在加载记录…</strong></div></li>}
            {!eventsLoading && events.slice(0, showAllEvents ? events.length : 4).map(event => <li key={event.id}><span className={event.eventType === 'ARCHIVED' ? 'is-warning' : 'is-success'} /><div><time>{formatUpdatedAt(event.createdAt)}</time><strong>{eventLabel(event, asset)}</strong>{event.reason && <small>{event.reason}</small>}</div></li>)}
            {!eventsLoading && events.length === 0 && <li><span /><div><strong>暂无生命周期记录</strong></div></li>}
          </ol>
          {events.length > 4 && <AppButton link size="small" type="button" aria-expanded={showAllEvents} onClick={() => setShowAllEvents(value => !value)}><ClockCounterClockwise size={14} />{showAllEvents ? '收起记录' : `查看全部 ${events.length} 条记录`}</AppButton>}
        </section>

        <div className="report-drawer-collaboration-column">
          <section className="report-drawer-section report-drawer-permissions">
            <header><h3>权限与协作</h3>{canRun(asset, 'PERMISSIONS') && <AppButton link size="small" type="button" onClick={onPermissions}>管理权限</AppButton>}</header>
            <div><span><Users size={16} />可查看<strong>{asset.visibleCount} 人</strong></span><span><NotePencil size={16} />可编辑<strong>{asset.editableCount} 人</strong></span></div>
            <div className="report-avatar-stack" aria-label={`${asset.visibleCount}人可访问`}><img src="/report-assets/avatars/wang-min.png" alt="" /><img src="/report-assets/avatars/liu-yang.png" alt="" /><img src="/report-assets/avatars/chen-chen.png" alt="" /><i>+{Math.max(asset.visibleCount - 3, 0)}</i></div>
          </section>

          <div className="report-drawer-secondary-actions">
            {canRun(asset, 'SHARE') && <AppButton link size="small" type="button" onClick={onShare}><ShareNetwork size={15} />安全分享</AppButton>}
            {canRun(asset, 'ARCHIVE') && asset.lifecycle !== 'OFFLINE' && <AppButton link size="small" variant="danger" type="button" onClick={onArchive}><Archive size={15} />下架报告</AppButton>}
          </div>
        </div>
      </div>
    </div>
  </aside>
}

export function ReportAssetsPage() {
  const navigate = useNavigate()
  // 设计走查快照只在开发构建中可用，生产构建绝不返回虚构报告资产。
  const snapshot = import.meta.env.DEV && new URLSearchParams(window.location.search).get('snapshot') === 'assets'
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
  const [shareAsset, setShareAsset] = useState<ReportAsset | null>(null)
  const [archiveAsset, setArchiveAsset] = useState<ReportAsset | null>(null)
  const [restoreAsset, setRestoreAsset] = useState<ReportAsset | null>(null)
  const [transitionBusy, setTransitionBusy] = useState(false)
  const [transitionError, setTransitionError] = useState('')
  const [toast, setToast] = useState('')
  const [viewMode, setViewMode] = useState<'list' | 'grid'>('list')
  const [sortOrder, setSortOrder] = useState<'latest' | 'oldest'>('latest')

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

  const visibleAssets = useMemo(() => {
    const filtered = snapshot ? filterAssets(assets, scope, lifecycle, query) : assets
    return [...filtered].sort((left, right) => sortOrder === 'latest'
      ? new Date(right.updatedAt).getTime() - new Date(left.updatedAt).getTime()
      : new Date(left.updatedAt).getTime() - new Date(right.updatedAt).getTime())
  }, [assets, lifecycle, query, scope, snapshot, sortOrder])
  const selected = visibleAssets.find(asset => asset.id === selectedID) ?? null
  const lifecycleCounts = useMemo(() => (['CHANGED', 'PUBLISHED', 'DRAFT_ONLY', 'OFFLINE'] as const).reduce<Record<ReportLifecycle, number>>((result, status) => {
    result[status] = assets.filter(asset => asset.lifecycle === status).length
    return result
  }, { CHANGED: 0, PUBLISHED: 0, DRAFT_ONLY: 0, OFFLINE: 0 }), [assets])
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

  return <AppShell className="report-assets-shell report-workbench-shell" eyebrow="智能报告" title="报告中心" lockBusinessDomain>
    <div className={`report-workbench ${selected ? 'is-detail-open' : ''}`.trim()}>
      <section className="report-library-panel" aria-label="报告资产列表">
        <header className="report-library-header"><div><h1>报告中心</h1><p>统一管理与发现报告资产，支持浏览、协作与发布</p></div><AppButton variant="primary" size="small" type="button" onClick={() => navigate(snapshot ? '/reports/new?snapshot=runtime-draft' : '/reports/new')}><Plus size={16} weight="bold" />新建报告</AppButton></header>

        <div className="report-library-controls">
          <label className="report-search"><MagnifyingGlass size={17} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索报告名称或编码" aria-label="搜索报告名称或编码" /></label>
          <div className="report-library-filter"><AppButton plain size="small" type="button" tabIndex={-1} aria-hidden="true"><Funnel size={17} /><span>筛选</span></AppButton><select value={lifecycle} aria-label="按生命周期筛选" onChange={event => setLifecycle(event.target.value as ReportLifecycle | 'ALL')}><option value="ALL">全部状态</option><option value="CHANGED">有未发布修改</option><option value="PUBLISHED">已发布</option><option value="DRAFT_ONLY">待发布</option><option value="OFFLINE">已下架</option></select></div>
        </div>

        <div className="report-scope-tabs" role="tablist" aria-label="资产范围">
          {([['all', '全部可见'], ['mine', '我创建'], ['shared', '共享给我']] as const).map(([value, label]) => <AppButton text key={value} type="button" role="tab" aria-selected={scope === value} onClick={() => setScope(value)}>{label}</AppButton>)}
        </div>

        <div className="report-library-toolbar">
          <div className="report-lifecycle-filters" aria-label="生命周期筛选">
            <AppButton plain round size="small" type="button" aria-pressed={lifecycle === 'ALL'} onClick={() => setLifecycle('ALL')}>全部 <span>{assets.length}</span></AppButton>
            {(['CHANGED', 'PUBLISHED', 'DRAFT_ONLY', 'OFFLINE'] as const).map(status => <AppButton plain round size="small" key={status} type="button" className={`is-${status.toLocaleLowerCase()}`} aria-pressed={lifecycle === status} onClick={() => setLifecycle(status)}>{lifecycleLabels[status]} <span>{lifecycleCounts[status]}</span></AppButton>)}
          </div>
          <div className="report-library-view-controls">
            <label><SortAscending size={16} /><span className="sr-only">排序</span><select value={sortOrder} aria-label="报告排序" onChange={event => setSortOrder(event.target.value as typeof sortOrder)}><option value="latest">最近更新</option><option value="oldest">最早更新</option></select></label>
            <div role="group" aria-label="报告展示方式"><AppButton text type="button" aria-label="网格视图" aria-pressed={viewMode === 'grid'} onClick={() => setViewMode('grid')}><SquaresFour size={17} /></AppButton><AppButton text type="button" aria-label="列表视图" aria-pressed={viewMode === 'list'} onClick={() => setViewMode('list')}><ListBullets size={18} /></AppButton></div>
          </div>
        </div>

        <div className={`report-library-results is-${viewMode}`}>
          {viewMode === 'list' && !loading && !error && visibleAssets.length > 0 && <div className="report-library-table-head" aria-hidden="true"><span>报告名称 / 编码</span><span>所有者</span><span>版本 / 草稿</span><span>状态</span><span>最近更新</span><span>可见范围</span><i /></div>}
          {loading && <div className="report-assets-feedback"><span className="report-loading-spinner" />正在加载报告资产…</div>}
          {!loading && error && <div className="report-assets-feedback is-error"><WarningCircle size={24} /><strong>报告暂时无法加载</strong><p>{error}</p><AppButton link type="button" onClick={() => void loadAssets()}>重新加载</AppButton></div>}
          {!loading && !error && visibleAssets.length === 0 && <div className="report-assets-feedback"><Archive size={27} /><strong>{query || lifecycle !== 'ALL' || scope !== 'all' ? '没有符合条件的报告' : '当前领域还没有报告'}</strong><p>{query || lifecycle !== 'ALL' || scope !== 'all' ? '调整搜索或筛选条件后重试。' : '创建第一份受治理报告，或等待有权限的报告共享给你。'}</p>{query || lifecycle !== 'ALL' || scope !== 'all' ? <AppButton link type="button" onClick={() => { setQuery(''); setLifecycle('ALL'); setScope('all') }}>清除筛选</AppButton> : <AppButton variant="primary" size="small" type="button" onClick={() => navigate(snapshot ? '/reports/new?snapshot=runtime-draft' : '/reports/new')}>新建报告</AppButton>}</div>}
          {!loading && !error && viewMode === 'list' && visibleAssets.map(asset => <AssetListRow key={asset.id} asset={asset} selected={asset.id === selected?.id} onSelect={() => setSelectedID(asset.id)} />)}
          {!loading && !error && viewMode === 'grid' && <div className="report-library-grid">{visibleAssets.map(asset => <AssetGridCard key={asset.id} asset={asset} selected={asset.id === selected?.id} onSelect={() => setSelectedID(asset.id)} />)}</div>}
          {!loading && !error && visibleAssets.length > 0 && nextCursor && <AppButton plain size="small" className="report-load-more" type="button" disabled={loadingMore} onClick={() => void loadAssets(nextCursor, true)}>{loadingMore ? '正在加载…' : '加载更多报告'}</AppButton>}
        </div>

        <footer className="report-library-footer"><span>共 {visibleAssets.length} 条报告</span><span>结果按当前领域与对象权限裁剪</span></footer>
      </section>
      {selected && <ReportAssetDrawer
        asset={selected}
        events={snapshot ? snapshotEvents : eventsReportID === selected.id ? events : []}
        eventsLoading={!snapshot && eventsReportID !== selected.id}
        onClose={() => setSelectedID('')}
        onView={() => navigate(snapshot ? `/reports/${selected.id}?snapshot=runtime` : `/reports/${selected.id}`)}
        onEdit={() => navigate(snapshot ? `/reports/${selected.id}?snapshot=runtime-draft` : `/reports/${selected.id}?mode=edit`)}
        onPublish={() => publish(selected)}
        onPermissions={() => setPermissionAsset(selected)}
        onShare={() => setShareAsset(selected)}
        onArchive={() => { setTransitionError(''); setArchiveAsset(selected) }}
        onRestore={() => { setTransitionError(''); setRestoreAsset(selected) }}
      />}
    </div>
    {permissionAsset && <PermissionDialog asset={permissionAsset} snapshot={snapshot} onChanged={() => { if (!snapshot) void loadAssets() }} onClose={() => setPermissionAsset(null)} />}
    {shareAsset && <ShareDialog asset={shareAsset} snapshot={snapshot} onClose={() => setShareAsset(null)} />}
    {archiveAsset && <LifecycleDialog asset={archiveAsset} busy={transitionBusy} error={transitionError} onClose={() => setArchiveAsset(null)} onConfirm={reason => void transition(archiveAsset, reason, false)} />}
    {restoreAsset && <LifecycleDialog asset={restoreAsset} restore busy={transitionBusy} error={transitionError} onClose={() => setRestoreAsset(null)} onConfirm={reason => void transition(restoreAsset, reason, true)} />}
    {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
  </AppShell>
}
