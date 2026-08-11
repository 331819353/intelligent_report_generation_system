import {
  Bell,
  BookOpenText,
  CaretDown,
  ChatCircleDots,
  Check,
  ClipboardText,
  Database,
  FileText,
  GearSix,
  GlobeHemisphereWest,
  House,
  Path,
  Question,
  ShieldCheck,
  SidebarSimple,
  SignOut,
  Stack,
  UserCircle,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { administrationAPI, type BusinessDomain } from '../lib/administration'
import {
  bindBusinessDomain,
  currentTokens,
  forceLogout,
  logout,
  switchBusinessDomain,
} from '../lib/auth'
import {
  clearDomain,
  currentDomain,
  domainCatalogChangedEvent,
} from '../lib/domain-context'
import { formatHomeTime, workItemDestination, workTypeLabel } from '../lib/home-data'
import { homeAPI, type WorkInboxItem } from '../lib/home-api'
import { AppButton } from './AppButton'

type AppShellProps = {
  title?: string
  titleMeta?: ReactNode
  eyebrow?: string
  children: ReactNode
  actions?: ReactNode
  className?: string
  lockBusinessDomain?: boolean
}

type NavItem = {
  label: string
  to?: string
  icon: typeof House
  adminOnly?: boolean
  children?: NavItem[]
}

type NavGroup = {
  label: string
  items: NavItem[]
}

const snapshotDomain: BusinessDomain = {
  id: 'snapshot-enterprise-operations',
  code: 'ENTERPRISE_OPERATIONS',
  name: '企业经营',
  description: '企业经营分析与决策领域',
  status: 'ACTIVE',
  default: true,
  version: 1,
  createdAt: '2026-08-10T00:00:00+08:00',
  administrators: [],
}

const snapshotDomains: BusinessDomain[] = [
  snapshotDomain,
  { ...snapshotDomain, id: 'snapshot-supply-chain', code: 'SUPPLY_CHAIN', name: '供应链管理', description: '供应链履约、库存与质量分析', default: false },
  { ...snapshotDomain, id: 'snapshot-channel-sales', code: 'CHANNEL_SALES', name: '渠道销售', description: '渠道、门店与销售经营分析', default: false },
]

const navigation: NavGroup[] = [
  {
    label: '业务工作',
    items: [
      { label: '分析首页', to: '/home', icon: House },
      { label: '问数工作台', to: '/ask-data', icon: ChatCircleDots },
      { label: '报告中心', to: '/reports', icon: FileText },
    ],
  },
  {
    label: '协同与行动',
    items: [
      { label: '审批中心', to: '/approvals', icon: ShieldCheck },
      { label: '任务中心', to: '/tasks', icon: ClipboardText },
      { label: '决策与行动', to: '/decisions', icon: Path },
    ],
  },
  {
    label: '数据与治理',
    items: [
      // 数据集是报告数据绑定的前置资产，必须可以从导航直达。
      { label: '数据资产', to: '/data-sources', icon: Database },
      { label: '数据集', to: '/datasets', icon: Stack },
      {
        label: '权限管理',
        icon: ShieldCheck,
        children: [
          { label: '领域访问', to: '/domain-access', icon: GlobeHemisphereWest },
          { label: '用户权限', to: '/platform-management/users', icon: UserCircle, adminOnly: true },
          { label: '角色配置', to: '/platform-management/permissions', icon: ShieldCheck, adminOnly: true },
        ],
      },
      { label: '系统管理', to: '/platform-management/domains', icon: GearSix, adminOnly: true },
    ],
  },
]

type NotificationItem = {
  id: string
  title: string
  meta: string
  kind: string
  source?: WorkInboxItem
  href?: string
}

const snapshotNotifications: NotificationItem[] = [
  { id: 'snapshot-1', title: '毛利率异常波动原因分析', meta: '今天 12:00 前处理', kind: '高优先级' },
  { id: 'snapshot-2', title: '新品上市复盘报告待发布', meta: '刘洋于 10:18 提交', kind: '报告' },
  { id: 'snapshot-3', title: '渠道政策效果评估待确认', meta: '决策 DEC-20260810-03', kind: '决策' },
]

/** 为业务、协同和治理页面提供统一的全局框架。 */
export function AppShell({ title = '智能分析决策平台', titleMeta, eyebrow = '工作台', children, actions, className = '' }: AppShellProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const domainSwitcherRef = useRef<HTMLDivElement>(null)
  const utilityRef = useRef<HTMLDivElement>(null)
  const designSnapshot = import.meta.env.DEV && Boolean(new URLSearchParams(window.location.search).get('snapshot'))
  const hasRealAccessToken = currentTokens()?.accessToken.split('.').length === 3
  const [domains, setDomains] = useState<BusinessDomain[]>(designSnapshot ? snapshotDomains : [])
  const [selectedDomain, setSelectedDomain] = useState<BusinessDomain | null>(() => currentDomain() ?? (designSnapshot ? snapshotDomain : null))
  const [domainMenuOpen, setDomainMenuOpen] = useState(false)
  const [canManage, setCanManage] = useState<boolean | null>(designSnapshot ? true : hasRealAccessToken ? null : false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [permissionNavigationOpen, setPermissionNavigationOpen] = useState(true)
  const [openUtility, setOpenUtility] = useState<'notifications' | 'help' | 'user' | null>(null)
  const [notifications, setNotifications] = useState<NotificationItem[]>(designSnapshot ? snapshotNotifications : [])
  const [notificationTotal, setNotificationTotal] = useState(designSnapshot ? 8 : 0)
  const [notificationLoading, setNotificationLoading] = useState(!designSnapshot)
  const [notificationError, setNotificationError] = useState('')

  useEffect(() => {
    if (designSnapshot) return undefined
    // 真实登录令牌是三段式 JWT；组件测试使用的占位令牌不触发额外网络请求。
    if (!hasRealAccessToken) return undefined
    let cancelled = false
    const loadDomains = () => administrationAPI.listDomains()
      .then(items => {
        if (cancelled) return
        setDomains(items)
        const active = items.filter(item => item.status === 'ACTIVE')
        const stored = currentDomain()
        if (stored && !active.some(item => item.id === stored.id)) {
          setSelectedDomain(null)
          clearDomain()
          return
        }
        const next = active.find(item => item.id === stored?.id)
          ?? active.find(item => item.default)
          ?? active[0]
          ?? null
        setSelectedDomain(next)
        if (next && next.id !== stored?.id) {
          void switchBusinessDomain(next).catch(() => forceLogout('BUSINESS_DOMAIN_UNAVAILABLE'))
        } else if (next) {
          void bindBusinessDomain(next.id).catch(() => forceLogout('BUSINESS_DOMAIN_UNAVAILABLE'))
        }
      })
      .catch(() => {
        if (!cancelled) setDomains([])
      })
    const refreshDomains = () => { void loadDomains() }
    void loadDomains()
    window.addEventListener(domainCatalogChangedEvent, refreshDomains)
    void administrationAPI.canManage()
      .then(allowed => {
        if (!cancelled) setCanManage(allowed)
      })
      .catch(() => {
        if (!cancelled) setCanManage(false)
      })
    return () => {
      cancelled = true
      window.removeEventListener(domainCatalogChangedEvent, refreshDomains)
    }
  }, [designSnapshot, hasRealAccessToken])

  useEffect(() => {
    if (designSnapshot) return undefined
    let cancelled = false
    void homeAPI.listWorkItems({ unread: true, limit: 200 })
      .then(result => {
        if (cancelled) return
        setNotificationTotal(result.items.length)
        setNotificationError('')
        setNotifications(result.items.slice(0, 5).map(item => ({
          id: `${item.type}:${item.objectId}`,
          title: item.summary,
          meta: item.slaDueAt ? `${item.overdue ? '已逾期' : '截止'} ${formatHomeTime(item.slaDueAt)}` : formatHomeTime(item.updatedAt),
          kind: workTypeLabel(item.type),
          source: item,
          href: workItemDestination(item),
        })))
      })
      .catch(() => {
        if (!cancelled) {
          setNotificationTotal(0)
          setNotifications([])
          setNotificationError('通知暂时无法加载，请稍后重试')
        }
      })
      .finally(() => { if (!cancelled) setNotificationLoading(false) })
    return () => { cancelled = true }
  }, [designSnapshot])

  useEffect(() => {
    if (!domainMenuOpen && !openUtility) return undefined
    const close = (event: MouseEvent) => {
      const target = event.target as Node
      if (!domainSwitcherRef.current?.contains(target)) setDomainMenuOpen(false)
      if (!utilityRef.current?.contains(target)) setOpenUtility(null)
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setDomainMenuOpen(false)
        setOpenUtility(null)
      }
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', escape)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', escape)
    }
  }, [domainMenuOpen, openUtility])

  const activeDomains = useMemo(
    () => domains.filter(item => item.status === 'ACTIVE'),
    [domains],
  )

  const chooseDomain = async (domain: BusinessDomain) => {
    setDomainMenuOpen(false)
    if (designSnapshot) {
      setSelectedDomain(domain)
      return
    }
    try {
      await switchBusinessDomain(domain)
      setSelectedDomain(domain)
    } catch {
      forceLogout('BUSINESS_DOMAIN_UNAVAILABLE')
    }
  }

  const signOut = async () => {
    if (!designSnapshot) await logout()
    navigate('/login')
  }

  const openNotification = (item: NotificationItem) => {
    setOpenUtility(null)
    if (item.source && !designSnapshot) {
      void homeAPI.markWorkItemRead(item.source).then(() => {
        setNotifications(current => current.filter(candidate => candidate.id !== item.id))
        setNotificationTotal(current => Math.max(0, current - 1))
      }).catch(() => undefined)
    }
    if (item.href) navigate(item.href)
  }

  const userName = designSnapshot ? '王敏' : '当前用户'

  return (
    <div className={`app-shell app-shell-v2 ${sidebarCollapsed ? 'is-sidebar-collapsed' : ''} ${className}`.trim()}>
      <header className="global-header">
        <NavLink className="global-brand" to="/home" aria-label="返回分析首页">
          <img className="brand-logo" src="/haier-logo.svg" alt="Haier 海尔" />
          <strong>智能分析决策平台</strong>
        </NavLink>

        <div className="global-utilities" ref={utilityRef}>
          <AppButton text circle className="global-utility-button" type="button" aria-label="通知" aria-expanded={openUtility === 'notifications'} onClick={() => setOpenUtility(value => value === 'notifications' ? null : 'notifications')}>
            <Bell size={20} aria-hidden="true" />{notificationTotal > 0 && <span className="notification-count">{notificationTotal > 99 ? '99+' : notificationTotal}</span>}
          </AppButton>
          <AppButton text className="global-help-button" type="button" aria-expanded={openUtility === 'help'} onClick={() => setOpenUtility(value => value === 'help' ? null : 'help')}>
            <Question size={18} aria-hidden="true" /><span>帮助中心</span>
          </AppButton>
          <AppButton text className="global-user-button" type="button" aria-expanded={openUtility === 'user'} onClick={() => setOpenUtility(value => value === 'user' ? null : 'user')}>
            {designSnapshot ? <img src="/report-assets/avatars/wang-min.png" alt="" /> : <UserCircle size={30} weight="duotone" aria-hidden="true" />}
            <span>{userName}</span><CaretDown size={14} aria-hidden="true" />
          </AppButton>

          {openUtility === 'notifications' && <section className="utility-popover notification-popover" aria-label="通知中心">
            <header><div><strong>通知</strong><span>{notificationLoading ? '加载中' : notificationError ? '加载失败' : `${notificationTotal} 条未读`}</span></div><AppButton text circle type="button" aria-label="关闭通知" onClick={() => setOpenUtility(null)}><X size={16} /></AppButton></header>
            <div className="notification-popover-list">
              {notifications.map(item => <AppButton text type="button" key={item.id} onClick={() => openNotification(item)}><span>{item.kind}</span><strong>{item.title}</strong><small>{item.meta}</small></AppButton>)}
              {!notificationLoading && notificationError && <p className="notification-empty is-error"><WarningCircle size={18} />{notificationError}</p>}
              {!notificationLoading && !notificationError && notifications.length === 0 && <p className="notification-empty"><Check size={18} />当前没有未读通知</p>}
            </div>
            <footer><AppButton link type="button" onClick={() => { setOpenUtility(null); navigate('/home') }}>返回首页查看全部待办</AppButton></footer>
          </section>}

          {openUtility === 'help' && <section className="utility-popover help-popover" aria-label="帮助中心">
            <header><div><strong>帮助中心</strong><span>快速找到使用说明</span></div><AppButton text circle type="button" aria-label="关闭帮助" onClick={() => setOpenUtility(null)}><X size={16} /></AppButton></header>
            <AppButton text type="button" onClick={() => setOpenUtility(null)}><BookOpenText size={18} /><span><strong>平台使用指南</strong><small>了解问数、报告与决策流程</small></span></AppButton>
            <AppButton text type="button" onClick={() => setOpenUtility(null)}><ChatCircleDots size={18} /><span><strong>联系平台支持</strong><small>反馈问题并附带当前页面</small></span></AppButton>
          </section>}

          {openUtility === 'user' && <section className="utility-popover user-popover" aria-label="用户菜单">
            <header><div><strong>{userName}</strong><span>企业经营 · 业务分析师</span></div></header>
            <AppButton text type="button" onClick={() => setOpenUtility(null)}><UserCircle size={18} />个人设置</AppButton>
            <AppButton text type="button" onClick={() => void signOut()}><SignOut size={18} />退出登录</AppButton>
          </section>}
        </div>
      </header>

      <aside className="sidebar">
        <nav aria-label="主导航">
          {navigation.map(group => <section className="sidebar-nav-group" key={group.label} aria-labelledby={`nav-${group.label}`}>
            <h2 id={`nav-${group.label}`}>{group.label}</h2>
            {group.items.map(item => {
              const Icon = item.icon
              if (item.children) {
                const visibleChildren = item.children.filter(child => !child.adminOnly || canManage !== false)
                const branchActive = visibleChildren.some(child => child.to && location.pathname === child.to)
                return <div className={`sidebar-nav-branch ${branchActive ? 'is-active' : ''}`.trim()} key={item.label}>
                  <AppButton
                    text
                    className="sidebar-nav-parent"
                    type="button"
                    aria-expanded={permissionNavigationOpen}
                    onClick={() => setPermissionNavigationOpen(open => !open)}
                  >
                    <Icon aria-hidden="true" size={19} />
                    <span>{item.label}</span>
                    <CaretDown className="sidebar-nav-caret" aria-hidden="true" size={14} />
                  </AppButton>
                  {permissionNavigationOpen && <div className="sidebar-nav-children">
                    {visibleChildren.map(child => {
                      const ChildIcon = child.icon
                      return <NavLink key={child.label} to={child.to!} title={sidebarCollapsed ? child.label : undefined}>
                        <ChildIcon aria-hidden="true" size={17} /><span>{child.label}</span>
                      </NavLink>
                    })}
                  </div>}
                </div>
              }
              const available = !item.adminOnly || canManage !== false
              return available
                ? <NavLink key={item.label} to={item.to!} title={sidebarCollapsed ? item.label : undefined}><Icon aria-hidden="true" size={19} /><span>{item.label}</span></NavLink>
                : <AppButton text className="sidebar-planned-link" type="button" disabled aria-disabled="true" key={item.label} title={`${item.label}将在后续页面确认后开放`}><Icon aria-hidden="true" size={19} /><span>{item.label}</span></AppButton>
            })}
          </section>)}
        </nav>
        <div className="sidebar-footer">
          <div className="sidebar-domain-wrap" ref={domainSwitcherRef}>
            {domainMenuOpen && <div className="domain-menu sidebar-domain-menu" role="menu" aria-label="切换业务领域">
              <header><span>切换领域</span><small>{activeDomains.length} 个可用领域</small></header>
              {activeDomains.length > 0
                ? activeDomains.map(domain => <AppButton
                  text
                  type="button"
                  role="menuitemradio"
                  aria-checked={selectedDomain?.id === domain.id}
                  key={domain.id}
                  onClick={() => void chooseDomain(domain)}
                >
                  <span className="domain-option-icon">{domain.name.slice(0, 1)}</span>
                  <span><strong>{domain.name}</strong><small>{domain.description || domain.code}</small></span>
                  {selectedDomain?.id === domain.id && <Check size={16} weight="bold" aria-hidden="true" />}
                </AppButton>)
                : <p>暂无可用领域</p>}
            </div>}
            <AppButton
              text
              className="sidebar-domain-selector"
              type="button"
              aria-haspopup="menu"
              aria-expanded={domainMenuOpen}
              aria-label="切换业务领域"
              onClick={() => setDomainMenuOpen(open => !open)}
            >
              <GlobeHemisphereWest size={18} weight="duotone" aria-hidden="true" />
              <span><small>当前领域</small><strong>{selectedDomain?.name || '选择业务领域'}</strong></span>
              <CaretDown size={15} aria-hidden="true" />
            </AppButton>
          </div>
          <AppButton text className="sidebar-collapse" type="button" aria-label={sidebarCollapsed ? '展开导航' : '收起导航'} onClick={() => setSidebarCollapsed(value => !value)}>
            <SidebarSimple size={18} aria-hidden="true" /><span>{sidebarCollapsed ? '展开菜单' : '收起菜单'}</span>
          </AppButton>
        </div>
      </aside>

      <main className="main-stage">
        <header className="topbar">
          <div><span className="eyebrow">{eyebrow}</span><div className="topbar-title-row"><h1>{title}</h1>{titleMeta}</div></div>
          <div className="topbar-actions">{actions}</div>
        </header>
        {children}
      </main>
    </div>
  )
}
