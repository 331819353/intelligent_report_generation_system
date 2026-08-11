import {
  Buildings,
  CaretLeft,
  CaretRight,
  CheckCircle,
  Clock,
  Cube,
  Factory,
  GlobeHemisphereWest,
  MagnifyingGlass,
  PaperPlaneTilt,
  ShoppingCart,
  SpinnerGap,
  Storefront,
  UserCircle,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { AppShell } from '../components/AppShell'
import { AppButton } from '../components/AppButton'
import {
  administrationAPI,
  type DomainApplication,
  type DomainCatalogItem,
} from '../lib/administration'
import { bindBusinessDomain } from '../lib/auth'
import { notifyDomainCatalogChanged, selectDomain } from '../lib/domain-context'

type CatalogView = 'ALL' | 'ENTERABLE' | 'AVAILABLE' | 'APPLICATIONS'

const accessLabels: Record<DomainCatalogItem['accessStatus'], string> = {
  AVAILABLE: '可申请',
  PENDING: '审批中',
  APPROVED: '已通过',
  REJECTED: '已驳回',
  CANCELLED: '可申请',
  MEMBER: '已加入',
  DOMAIN_ADMIN: '已加入',
}

const applicationLabels: Record<DomainApplication['status'], string> = {
  PENDING: '审批中',
  APPROVED: '已通过',
  REJECTED: '已驳回',
  CANCELLED: '已撤回',
}

const joinedStatuses = new Set<DomainCatalogItem['accessStatus']>(['MEMBER', 'DOMAIN_ADMIN'])
const applyStatuses = new Set<DomainCatalogItem['accessStatus']>(['AVAILABLE', 'REJECTED', 'CANCELLED'])

const snapshotAdministrator = {
  id: 'snapshot-admin-wang',
  employeeNo: 'A-1024',
  email: 'wang.ning@example.com',
  displayName: '王宁',
}

const snapshotItems: DomainCatalogItem[] = [
  {
    id: 'snapshot-enterprise', code: 'BIZ_MANAGEMENT', name: '企业经营',
    description: '经营分析与管理决策支持，涵盖财务、预算、人力等核心经营主题。',
    status: 'ACTIVE', default: true, version: 4, createdAt: '2026-08-10T09:32:00+08:00',
    administrators: [snapshotAdministrator], accessStatus: 'MEMBER',
  },
  {
    id: 'snapshot-supply', code: 'SUPPLY_CHAIN', name: '供应链管理',
    description: '供应链计划、采购、库存、物流等端到端供应链运营分析。',
    status: 'ACTIVE', default: false, version: 3, createdAt: '2026-08-09T16:45:00+08:00',
    administrators: [snapshotAdministrator], accessStatus: 'MEMBER',
  },
  {
    id: 'snapshot-channel', code: 'SALES_CHANNEL', name: '渠道销售',
    description: '渠道销售目标、渠道库存与经销商表现分析，支持渠道策略优化。',
    status: 'ACTIVE', default: false, version: 2, createdAt: '2026-08-11T10:15:00+08:00',
    administrators: [snapshotAdministrator], accessStatus: 'AVAILABLE',
  },
  {
    id: 'snapshot-retail', code: 'RETAIL_OPERATION', name: '零售运营',
    description: '零售门店运营、商品、会员与促销效果分析，提升零售运营效率。',
    status: 'ACTIVE', default: false, version: 2, createdAt: '2026-08-08T11:20:00+08:00',
    administrators: [{ ...snapshotAdministrator, id: 'snapshot-admin-li', displayName: '李明' }], accessStatus: 'PENDING',
  },
  {
    id: 'snapshot-quality', code: 'MANUFACTURING_QUALITY', name: '制造质量',
    description: '制造过程质量监控、质量追溯与改进分析，保障产品质量。',
    status: 'ACTIVE', default: false, version: 2, createdAt: '2026-08-07T14:18:00+08:00',
    administrators: [{ ...snapshotAdministrator, id: 'snapshot-admin-zhao', displayName: '赵强' }], accessStatus: 'AVAILABLE',
  },
  {
    id: 'snapshot-overseas', code: 'OVERSEAS_BUSINESS', name: '海外业务',
    description: '海外市场销售、渠道与运营分析，支持全球化业务决策。',
    status: 'ACTIVE', default: false, version: 1, createdAt: '2026-08-06T09:05:00+08:00',
    administrators: [{ ...snapshotAdministrator, id: 'snapshot-admin-chen', displayName: '陈晨' }], accessStatus: 'AVAILABLE',
  },
]

const snapshotApplications: DomainApplication[] = [{
  id: 'snapshot-application-retail',
  domainId: 'snapshot-retail',
  domainCode: 'RETAIL_OPERATION',
  domainName: '零售运营',
  applicantUserId: 'snapshot-user',
  applicantEmail: 'zhang.wei@example.com',
  applicantDisplayName: '张伟',
  status: 'PENDING',
  reason: '用于月度零售经营复盘与门店效率分析',
  reviewComment: '',
  createdAt: '2026-08-11T10:15:00+08:00',
}]

const snapshotRoleLabels: Record<string, string> = {
  BIZ_MANAGEMENT: '业务分析师',
  SUPPLY_CHAIN: '业务阅读者',
}

const snapshotScopes: Record<string, string[]> = {
  SALES_CHANNEL: ['销售目标', '渠道库存', '经销商表现'],
}

function domainIcon(code: string) {
  if (code.includes('SUPPLY')) return Cube
  if (code.includes('SALES')) return ShoppingCart
  if (code.includes('RETAIL')) return Storefront
  if (code.includes('MANUFACTURING') || code.includes('QUALITY')) return Factory
  if (code.includes('OVERSEAS')) return GlobeHemisphereWest
  return Buildings
}

function formatDate(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date).replaceAll('/', '-').replace(' ', ' ')
}

function domainRole(domain: DomainCatalogItem, snapshot: boolean) {
  if (snapshot && snapshotRoleLabels[domain.code]) return snapshotRoleLabels[domain.code]
  if (domain.accessStatus === 'DOMAIN_ADMIN') return '领域管理员'
  if (domain.accessStatus === 'MEMBER') return '领域成员'
  return '—'
}

/** 领域访问归属于权限管理，承担目录浏览、进入领域和自助申请。 */
export function DomainAccessPage() {
  const designSnapshot = import.meta.env.DEV && Boolean(new URLSearchParams(window.location.search).get('snapshot'))
  const [items, setItems] = useState<DomainCatalogItem[]>(designSnapshot ? snapshotItems : [])
  const [applications, setApplications] = useState<DomainApplication[]>(designSnapshot ? snapshotApplications : [])
  const [selected, setSelected] = useState<DomainCatalogItem | null>(designSnapshot ? snapshotItems[2] : null)
  const [view, setView] = useState<CatalogView>('ALL')
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(!designSnapshot)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [showIsolationNote, setShowIsolationNote] = useState(false)
  const [applicationTarget, setApplicationTarget] = useState<DomainCatalogItem | null>(null)
  const [reason, setReason] = useState('')

  const load = useCallback(async () => {
    if (designSnapshot) return
    setLoading(true)
    setError('')
    try {
      const [catalog, myApplications] = await Promise.all([
        administrationAPI.listDomainCatalog(),
        administrationAPI.listMyDomainApplications(),
      ])
      setItems(catalog)
      setApplications(myApplications)
      setSelected(current => catalog.find(item => item.id === current?.id)
        ?? catalog.find(item => applyStatuses.has(item.accessStatus))
        ?? catalog[0]
        ?? null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '领域目录加载失败')
    } finally {
      setLoading(false)
    }
  }, [designSnapshot])

  useEffect(() => {
    let cancelled = false
    queueMicrotask(() => { if (!cancelled) void load() })
    return () => { cancelled = true }
  }, [load])

  const applicationByDomain = useMemo(() => {
    const index = new Map<string, DomainApplication>()
    applications.forEach(application => {
      const current = index.get(application.domainId)
      if (!current || new Date(application.createdAt) > new Date(current.createdAt)) index.set(application.domainId, application)
    })
    return index
  }, [applications])

  const normalizedQuery = query.trim().toLocaleLowerCase()
  const visibleItems = useMemo(() => items.filter(domain => {
    if (view === 'ENTERABLE' && !joinedStatuses.has(domain.accessStatus)) return false
    if (view === 'AVAILABLE' && !applyStatuses.has(domain.accessStatus)) return false
    if (view === 'APPLICATIONS') return false
    if (!normalizedQuery) return true
    return `${domain.name} ${domain.code} ${domain.description}`.toLocaleLowerCase().includes(normalizedQuery)
  }), [items, normalizedQuery, view])

  const visibleApplications = useMemo(() => applications.filter(application => {
    if (!normalizedQuery) return true
    return `${application.domainName} ${application.domainCode} ${application.reason}`.toLocaleLowerCase().includes(normalizedQuery)
  }), [applications, normalizedQuery])

  const enterableCount = items.filter(domain => joinedStatuses.has(domain.accessStatus)).length
  const availableCount = items.filter(domain => applyStatuses.has(domain.accessStatus)).length

  const chooseView = (next: CatalogView) => {
    setView(next)
    setError('')
    if (next === 'APPLICATIONS') setSelected(null)
    else if (!selected) setSelected(items.find(item => applyStatuses.has(item.accessStatus)) ?? items[0] ?? null)
  }

  const enter = async (domain: DomainCatalogItem) => {
    if (designSnapshot) {
      setError('设计预览中已模拟进入领域，真实环境将切换领域会话。')
      return
    }
    setBusy(true)
    setError('')
    try {
      await bindBusinessDomain(domain.id)
      const memberships = await administrationAPI.listDomains()
      selectDomain(memberships.find(item => item.id === domain.id) ?? domain)
      notifyDomainCatalogChanged()
      window.location.assign('/home')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '进入领域失败')
    } finally {
      setBusy(false)
    }
  }

  const openApplication = (domain: DomainCatalogItem) => {
    setSelected(domain)
    setReason('')
    setApplicationTarget(domain)
    setError('')
  }

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!applicationTarget || !reason.trim()) return
    setBusy(true)
    setError('')
    try {
      if (designSnapshot) {
        setApplications(current => [{
          id: `snapshot-${applicationTarget.id}`,
          domainId: applicationTarget.id,
          domainCode: applicationTarget.code,
          domainName: applicationTarget.name,
          applicantUserId: 'snapshot-user',
          applicantEmail: 'zhang.wei@example.com',
          applicantDisplayName: '张伟',
          status: 'PENDING',
          reason: reason.trim(),
          reviewComment: '',
          createdAt: new Date('2026-08-11T11:20:00+08:00').toISOString(),
        }, ...current])
        setItems(current => current.map(item => item.id === applicationTarget.id ? { ...item, accessStatus: 'PENDING' } : item))
      } else {
        await administrationAPI.applyDomain(applicationTarget.id, reason.trim())
        await load()
      }
      setApplicationTarget(null)
      setReason('')
      setView('APPLICATIONS')
      setSelected(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '领域申请提交失败')
    } finally {
      setBusy(false)
    }
  }

  const detailApplication = selected ? applicationByDomain.get(selected.id) : undefined
  const detailScopes = selected
    ? designSnapshot && snapshotScopes[selected.code] ? snapshotScopes[selected.code] : ['领域数据', '指标与报告']
    : []

  return <AppShell className="domain-access-shell">
    <section className="domain-access-page">
      <header className="domain-access-heading">
        <div className="domain-access-heading-copy">
          <div className="domain-access-breadcrumb"><span>权限管理</span><CaretRight size={12} /><strong>领域访问</strong></div>
          <h1>业务领域</h1>
          <p>选择一个领域开始分析，或申请新的访问权限</p>
        </div>
      </header>

      <div className="domain-access-toolbar">
        <div className="domain-access-tabs" role="tablist" aria-label="领域目录视图">
          {([
            ['ALL', `全部 ${items.length}`],
            ['ENTERABLE', `可进入 ${enterableCount}`],
            ['AVAILABLE', `可申请 ${availableCount}`],
            ['APPLICATIONS', `我的申请 ${applications.length}`],
          ] as const).map(([value, label]) => <AppButton
            text
            type="button"
            role="tab"
            aria-selected={view === value}
            className={view === value ? 'is-active' : ''}
            key={value}
            onClick={() => chooseView(value)}
          >{label}</AppButton>)}
        </div>
        <div className="domain-access-tools">
          <label className="domain-access-search">
            <MagnifyingGlass size={18} aria-hidden="true" />
            <input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索领域名称或编码" aria-label="搜索领域名称或编码" />
            {query && <AppButton text circle type="button" aria-label="清空搜索" onClick={() => setQuery('')}><X size={14} /></AppButton>}
          </label>
          <AppButton link type="button" onClick={() => setShowIsolationNote(value => !value)}>了解领域隔离<CaretRight size={13} /></AppButton>
        </div>
      </div>

      {showIsolationNote && <div className="domain-access-isolation-note" role="status">
        <GlobeHemisphereWest size={20} weight="duotone" />
        <span><strong>领域是全局权限上下文</strong>切换后，数据源、数据集、指标和报告均按新领域重新加载。</span>
        <AppButton text circle type="button" aria-label="关闭说明" onClick={() => setShowIsolationNote(false)}><X size={15} /></AppButton>
      </div>}

      {error && <div className={`domain-access-feedback ${error.startsWith('设计预览') ? 'is-info' : 'is-error'}`} role="alert">
        <WarningCircle size={18} /><span>{error}</span><AppButton text circle type="button" aria-label="关闭提示" onClick={() => setError('')}><X size={14} /></AppButton>
      </div>}

      <div className={`domain-access-workspace ${selected && view !== 'APPLICATIONS' ? 'has-detail' : ''}`.trim()}>
        <section className="domain-access-table-panel" aria-label={view === 'APPLICATIONS' ? '我的领域申请' : '领域目录'}>
          {view === 'APPLICATIONS'
            ? <>
              <div className="domain-application-header" role="row">
                <span>领域</span><span>申请用途</span><span>访问状态</span><span>提交时间</span><span>审批说明</span>
              </div>
              <div className="domain-application-body">
                {visibleApplications.map(application => <article className="domain-application-row" key={application.id}>
                  <div><span className="domain-catalog-icon is-application"><Clock size={19} /></span><span><strong>{application.domainName}</strong><small>{application.domainCode}</small></span></div>
                  <p>{application.reason || '未填写申请用途'}</p>
                  <span className={`domain-status-badge is-${application.status.toLocaleLowerCase()}`}>{applicationLabels[application.status]}</span>
                  <time dateTime={application.createdAt}>{formatDate(application.createdAt)}</time>
                  <span>{application.reviewComment || (application.status === 'PENDING' ? '等待领域管理员审批' : '—')}</span>
                </article>)}
                {!loading && visibleApplications.length === 0 && <div className="domain-access-empty"><GlobeHemisphereWest size={30} weight="duotone" /><strong>{query ? '没有匹配的申请记录' : '还没有领域申请'}</strong><span>{query ? '请调整搜索关键词' : '可从领域目录选择领域并提交申请'}</span><AppButton link type="button" onClick={() => { setQuery(''); chooseView('ALL') }}>返回领域目录</AppButton></div>}
              </div>
            </>
            : <>
              <div className="domain-catalog-header" role="row">
                <span>领域</span><span>领域说明</span><span>我的角色</span><span>访问状态</span><span>最近更新</span><span>操作</span>
              </div>
              <div className="domain-catalog-body">
                {loading && <div className="domain-access-loading"><SpinnerGap className="spin" size={26} /><strong>正在加载领域目录…</strong></div>}
                {!loading && visibleItems.map(domain => {
                  const Icon = domainIcon(domain.code)
                  const joined = joinedStatuses.has(domain.accessStatus)
                  const pending = domain.accessStatus === 'PENDING'
                  return <article
                    className={`domain-catalog-row ${selected?.id === domain.id ? 'is-selected' : ''}`.trim()}
                    key={domain.id}
                    role="button"
                    tabIndex={0}
                    aria-pressed={selected?.id === domain.id}
                    onClick={() => setSelected(domain)}
                    onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') setSelected(domain) }}
                  >
                    <div className="domain-catalog-name"><span className={`domain-catalog-icon tone-${domain.code.length % 5}`}><Icon size={21} weight="duotone" /></span><span><strong>{domain.name}</strong><small>{domain.code}</small></span></div>
                    <p>{domain.description || '暂无领域说明'}</p>
                    <span>{domainRole(domain, designSnapshot)}</span>
                    <span className={`domain-status-badge is-${domain.accessStatus.toLocaleLowerCase()}`}>{pending && <Clock size={13} />}{joined && <CheckCircle size={13} />}{accessLabels[domain.accessStatus]}</span>
                    <time dateTime={domain.createdAt}>{formatDate(domain.createdAt)}</time>
                    <div className="domain-catalog-action">
                      {joined
                        ? <AppButton variant="primary" size="small" type="button" disabled={busy} onClick={event => { event.stopPropagation(); void enter(domain) }}>进入领域</AppButton>
                        : pending
                          ? <AppButton plain size="small" type="button" onClick={event => { event.stopPropagation(); chooseView('APPLICATIONS') }}>查看进度</AppButton>
                          : <AppButton plain size="small" type="button" onClick={event => { event.stopPropagation(); setSelected(domain) }}>申请加入</AppButton>}
                    </div>
                  </article>
                })}
                {!loading && visibleItems.length === 0 && <div className="domain-access-empty"><MagnifyingGlass size={28} /><strong>没有匹配的业务领域</strong><span>请调整搜索条件或切换目录视图</span><AppButton link type="button" onClick={() => { setQuery(''); chooseView('ALL') }}>清除筛选</AppButton></div>}
              </div>
              <footer className="domain-catalog-pagination">
                <span>共 {visibleItems.length} 条</span><span className="domain-page-size">10条/页<CaretRight size={12} /></span>
                <AppButton text circle size="small" type="button" aria-label="上一页" disabled><CaretLeft size={14} /></AppButton>
                <span className="domain-page-current">1</span>
                <AppButton text circle size="small" type="button" aria-label="下一页" disabled><CaretRight size={14} /></AppButton>
                <span>前往</span><span className="domain-page-input">1</span><span>页</span>
              </footer>
            </>}
        </section>

        {selected && view !== 'APPLICATIONS' && <aside className="domain-access-detail" aria-label={`${selected.name}领域详情`}>
          <header><div><h2>{selected.name}</h2><code>{selected.code}</code></div><AppButton text circle type="button" aria-label="关闭领域详情" onClick={() => setSelected(null)}><X size={20} /></AppButton></header>
          <section><h3>领域说明</h3><p>{selected.description || '暂无领域说明'}</p></section>
          <section><h3>申请后可访问</h3><div className="domain-scope-tags">{detailScopes.map(scope => <span key={scope}>{scope}</span>)}</div></section>
          <section><h3>访问说明</h3><p>加入后可获得该领域内的数据访问权限，具体操作范围由领域角色授权。</p></section>
          <section><h3>领域管理员</h3><div className="domain-administrator-summary"><UserCircle size={19} /><span>{selected.administrators.map(item => item.displayName).join('、') || '暂未配置'}</span></div></section>
          <footer>
            {joinedStatuses.has(selected.accessStatus)
              ? <AppButton variant="primary" size="large" type="button" disabled={busy} onClick={() => void enter(selected)}>进入领域</AppButton>
              : selected.accessStatus === 'PENDING'
                ? <AppButton variant="primary" size="large" type="button" onClick={() => chooseView('APPLICATIONS')}>查看申请进度</AppButton>
                : <AppButton variant="primary" size="large" type="button" disabled={busy} onClick={() => openApplication(selected)}>申请加入</AppButton>}
            <small>{detailApplication?.status === 'REJECTED' ? `上次申请：${detailApplication.reviewComment || '已驳回'}` : '申请后由领域管理员审批'}</small>
          </footer>
        </aside>}
      </div>
    </section>

    {applicationTarget && <div className="domain-application-backdrop" role="presentation" onMouseDown={event => {
      if (event.target === event.currentTarget && !busy) setApplicationTarget(null)
    }}>
      <section className="domain-application-dialog" role="dialog" aria-modal="true" aria-labelledby="domain-application-title">
        <header><div><span>领域访问申请</span><h2 id="domain-application-title">申请加入“{applicationTarget.name}”</h2><p>说明实际工作用途，便于领域管理员判断所需权限。</p></div><AppButton text circle type="button" aria-label="关闭申请" disabled={busy} onClick={() => setApplicationTarget(null)}><X size={20} /></AppButton></header>
        <form onSubmit={submit}>
          <label>申请用途 <b>*</b><textarea autoFocus required maxLength={1000} value={reason} onChange={event => setReason(event.target.value)} placeholder="请简要说明需要访问该领域的工作原因" /><small>{reason.length} / 1000</small></label>
          <div className="domain-application-policy"><GlobeHemisphereWest size={18} /><span>领域之间的数据、指标与报告相互隔离，授权仅在当前领域生效。</span></div>
          <footer><AppButton plain type="button" disabled={busy} onClick={() => setApplicationTarget(null)}>取消</AppButton><AppButton variant="primary" type="submit" disabled={busy || !reason.trim()}>{busy ? <SpinnerGap className="spin" size={16} /> : <PaperPlaneTilt size={16} />}{busy ? '提交中…' : '提交申请'}</AppButton></footer>
        </form>
      </section>
    </div>}
  </AppShell>
}
