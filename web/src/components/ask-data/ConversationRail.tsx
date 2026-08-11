import {
  Archive, CaretRight, DotsThreeVertical, Funnel, MagnifyingGlass, PencilSimple,
  PushPin, PushPinSlash, WarningCircle, X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { conversationStateLabel, formatConversationTime, groupConversations } from '../../lib/conversation-data'
import { mapAskDataError, questionAPI, type ConversationSummary } from '../../lib/ask-data-api'

type ConversationRailProps = {
  snapshot: boolean
  activeConversationId?: string
  refreshKey: number
  onNew: () => void
  onSelect: (conversation: ConversationSummary) => void
}

const snapshotConversations: ConversationSummary[] = [
  ['100', '哪些渠道导致本月毛利率下降？', '2026-08-10T10:12:00+08:00', false],
  ['101', '本月各产品线毛利率表现如何？', '2026-08-09T16:42:00+08:00', false],
  ['102', '促销活动对毛利率的影响分析', '2026-08-09T10:20:00+08:00', false],
  ['103', '各区域毛利率同比变化情况', '2026-08-09T09:15:00+08:00', false],
  ['104', '毛利率异常预警原因分析', '2026-08-08T14:12:00+08:00', false],
  ['105', '本季度毛利率趋势预测', '2026-08-07T10:30:00+08:00', false],
  ['106', '新品上市对毛利率的影响', '2026-08-06T16:06:00+08:00', false],
].map(([suffix, label, updatedAt, pinned]) => ({
  conversationId: `00000000-0000-4000-8000-000000000${suffix}`,
  latestRunId: `00000000-0000-4000-8000-000000001${suffix}`,
  label: String(label), state: 'ANSWERED', pinned: Boolean(pinned), archived: false,
  release: { releaseId: '00000000-0000-4000-8000-000000000124', contentHash: 'c'.repeat(64) },
  releaseDrifted: false, clarificationPending: false, narrativeDegraded: false,
  runCount: suffix === '100' ? 4 : 1, recordVersion: 1, updatedAt: String(updatedAt),
} satisfies ConversationSummary))

export function ConversationRail({ snapshot, activeConversationId, refreshKey, onNew, onSelect }: ConversationRailProps) {
  const [items, setItems] = useState<ConversationSummary[]>(snapshot ? snapshotConversations : [])
  const [query, setQuery] = useState('')
  const [archived, setArchived] = useState(false)
  const [loading, setLoading] = useState(!snapshot)
  const [error, setError] = useState('')
  const [openMenu, setOpenMenu] = useState('')
  const [renameTarget, setRenameTarget] = useState<ConversationSummary | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [nextCursor, setNextCursor] = useState('')
  const menuRef = useRef<HTMLDivElement>(null)

  const load = async (cursor = '', append = false) => {
    if (snapshot) return
    setLoading(true); setError('')
    try {
      const result = await questionAPI.listConversations({ search: query.trim(), archived, limit: 50, cursor: cursor || undefined })
      setItems(current => append ? [...current, ...result.items] : result.items)
      setNextCursor(result.nextCursor ?? '')
    } catch (cause) {
      setError(mapAskDataError(cause).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (snapshot) return undefined
    const timer = window.setTimeout(() => { void load() }, 220)
    return () => window.clearTimeout(timer)
  // load intentionally follows these query inputs; refreshKey invalidates after a new run.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [archived, query, refreshKey, snapshot])

  useEffect(() => {
    if (!openMenu) return undefined
    const close = (event: MouseEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) setOpenMenu('')
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [openMenu])

  const visibleItems = useMemo(() => {
    if (!snapshot) return items
    const normalized = query.trim().toLocaleLowerCase('zh-CN')
    return items.filter(item => item.archived === archived && (!normalized || item.label.toLocaleLowerCase('zh-CN').includes(normalized)))
  }, [archived, items, query, snapshot])
  const groups = useMemo(() => groupConversations(visibleItems, snapshot ? new Date('2026-08-10T12:00:00+08:00') : new Date()), [snapshot, visibleItems])
  const updateItem = (updated: ConversationSummary) => setItems(current => current.map(item => item.conversationId === updated.conversationId ? updated : item))
  const mutate = async (item: ConversationSummary, action: 'pin' | 'unpin' | 'archive' | 'restore') => {
    setBusy(true); setError(''); setOpenMenu('')
    try {
      if (snapshot) {
        if (action === 'archive') setItems(current => current.filter(candidate => candidate.conversationId !== item.conversationId))
        else updateItem({ ...item, pinned: action === 'pin', archived: action === 'restore' ? false : item.archived, recordVersion: item.recordVersion + 1 })
      } else {
        const updated = await questionAPI.mutateConversation(item.conversationId, action, item.recordVersion)
        if (action === 'archive' || action === 'restore') setItems(current => current.filter(candidate => candidate.conversationId !== item.conversationId))
        else updateItem(updated)
      }
    } catch (cause) {
      setError(mapAskDataError(cause).message)
      if (!snapshot) void load()
    } finally {
      setBusy(false)
    }
  }

  const rename = async () => {
    if (!renameTarget || !renameValue.trim()) return
    setBusy(true); setError('')
    try {
      const label = renameValue.trim()
      if (snapshot) updateItem({ ...renameTarget, label, recordVersion: renameTarget.recordVersion + 1 })
      else updateItem(await questionAPI.renameConversation(renameTarget.conversationId, renameTarget.recordVersion, label))
      setRenameTarget(null)
    } catch (cause) {
      setError(mapAskDataError(cause).message)
    } finally {
      setBusy(false)
    }
  }

  return <aside className="ask-session-rail" aria-label="问数会话">
    <div className="ask-workspace-tabs" role="tablist" aria-label="问数工作台视图">
      <button type="button" role="tab" aria-selected="true" onClick={onNew}>问数</button>
      <a href={snapshot ? '/ask-data?snapshot=data-requests' : '/ask-data?workspace=data-requests'} role="tab" aria-selected="false">我的申请</a>
    </div>
    <div className="ask-session-toolbar">
      <label className="ask-session-search"><MagnifyingGlass size={15} aria-hidden="true" /><span className="sr-only">搜索会话</span><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索会话或问题" /></label>
      <button className={archived ? 'is-active' : ''} type="button" aria-label={archived ? '显示进行中的会话' : '显示已归档会话'} aria-pressed={archived} onClick={() => setArchived(value => !value)}><Funnel size={16} /></button>
    </div>
    {loading && items.length === 0 && <div className="ask-session-feedback"><span className="ask-loader-ring" />正在加载会话…</div>}
    {!loading && error && items.length === 0 && <div className="ask-session-feedback is-error"><WarningCircle size={17} />{error}<button type="button" onClick={() => void load()}>重试</button></div>}
    <div className="ask-session-list">
      {groups.map(group => <section key={group.label}><h2>{group.label}</h2>{group.items.map(item => <div className={`ask-session-row ${activeConversationId === item.conversationId ? 'is-active' : ''}`} key={item.conversationId}>
        <button type="button" className="ask-session-select" onClick={() => onSelect(item)}>
          <span>{item.label}</span><small>{conversationStateLabel[item.state]} · {item.state === 'ANSWERED' ? '已验证' : formatConversationTime(item.updatedAt, snapshot ? new Date('2026-08-10T12:00:00+08:00') : new Date())}</small>
        </button>
        <div className="ask-session-menu-wrap" ref={openMenu === item.conversationId ? menuRef : undefined}>
          <button type="button" className="ask-session-more" aria-label={`${item.label}更多操作`} aria-expanded={openMenu === item.conversationId} onClick={() => setOpenMenu(value => value === item.conversationId ? '' : item.conversationId)}><DotsThreeVertical size={16} /></button>
          {openMenu === item.conversationId && <div className="ask-session-menu" role="menu">
            <button type="button" role="menuitem" disabled={busy || item.archived} onClick={() => void mutate(item, item.pinned ? 'unpin' : 'pin')}>{item.pinned ? <PushPinSlash size={14} /> : <PushPin size={14} />}{item.pinned ? '取消置顶' : '置顶'}</button>
            <button type="button" role="menuitem" disabled={busy || item.archived} onClick={() => { setRenameTarget(item); setRenameValue(item.label); setOpenMenu('') }}><PencilSimple size={14} />重命名</button>
            <button type="button" role="menuitem" disabled={busy} onClick={() => void mutate(item, item.archived ? 'restore' : 'archive')}><Archive size={14} />{item.archived ? '恢复' : '归档'}</button>
          </div>}
        </div>
      </div>)}</section>)}
      {!loading && groups.length === 0 && <div className="ask-empty-search"><span>没有符合条件的会话</span><button type="button" onClick={() => { setQuery(''); setArchived(false) }}>清除筛选</button></div>}
      {nextCursor && <button className="ask-load-more-conversations" type="button" disabled={loading} onClick={() => void load(nextCursor, true)}>{loading ? '加载中…' : '加载更多'}<CaretRight size={13} /></button>}
    </div>
    <footer className="ask-session-footer"><span>{snapshot ? '共 28 条会话' : `已加载 ${items.length} 条会话`}</span>{error && items.length > 0 && <small>{error}</small>}</footer>
    {renameTarget && <div className="ask-rename-overlay" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) setRenameTarget(null) }}><section role="dialog" aria-modal="true" aria-labelledby="rename-conversation-title">
      <header><div><strong id="rename-conversation-title">重命名会话</strong><small>名称仅用于整理历史，不修改原始问数证据。</small></div><button type="button" aria-label="关闭" onClick={() => setRenameTarget(null)}><X size={17} /></button></header>
      <label>会话名称<input autoFocus maxLength={120} value={renameValue} onChange={event => setRenameValue(event.target.value)} /></label>
      <footer><button type="button" onClick={() => setRenameTarget(null)}>取消</button><button className="primary-button" type="button" disabled={busy || !renameValue.trim()} onClick={() => void rename()}>{busy ? '保存中…' : '保存'}</button></footer>
    </section></div>}
  </aside>
}
