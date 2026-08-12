import {
  ArrowSquareOut,
  Bell,
  BookOpenText,
  Buildings,
  CaretDown,
  ChatCircleDots,
  Check,
  ClipboardText,
  Cube,
  Database,
  Factory,
  FileText,
  GearSix,
  GlobeHemisphereWest,
  House,
  Info,
  Path,
  Question,
  ShieldCheck,
  SidebarSimple,
  SignOut,
  SpinnerGap,
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
  currentProfile,
  currentSubject,
  currentTokens,
  forceLogout,
  logout,
  switchBusinessDomain,
} from '../lib/auth'
import {
  clearDomain,
  currentDomain,
  domainCatalogChangedEvent,
  selectDomain,
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
  controlPlane?: boolean
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
  accessSensitivity: 'INTERNAL',
  administrators: [],
}

const snapshotDomains: BusinessDomain[] = [
  snapshotDomain,
  { ...snapshotDomain, id: 'snapshot-supply-chain', code: 'SUPPLY_CHAIN', name: '供应链管理', description: '供应链计划、采购、库存、物流等端到端供应链运营分析。', default: false },
  { ...snapshotDomain, id: 'snapshot-manufacturing-quality', code: 'MANUFACTURING_QUALITY', name: '制造质量', description: '制造过程质量监控、质量追溯与改进分析，保障产品质量。', default: false },
]

function domainVisual(code: string) {
  if (code.includes('SUPPLY')) return { icon: Cube, tone: 'violet' }
  if (code.includes('MANUFACTURING') || code.includes('QUALITY')) return { icon: Factory, tone: 'green' }
  return { icon: Buildings, tone: 'cyan' }
}

function domainRoleLabel(domain: BusinessDomain, subject: string, snapshot: boolean) {
  if (snapshot) return domain.code.includes('ENTERPRISE') ? '领域管理员' : '领域成员'
  if (domain.administrators.some(item => item.id === subject)) return '领域管理员'
  return '领域成员'
}

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
      { label: '语义资产', to: '/semantic', icon: Cube },
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
export function AppShell({ title = '智能分析决策平台', titleMeta, eyebrow = '工作台', children, actions, className = '', controlPlane = false }: AppShellProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const domainSwitcherRef = useRef<HTMLDivElement>(null)
  const utilityRef = useRef<HTMLDivElement>(null)
  const searchParams = new URLSearchParams(window.location.search)
  const designSnapshot = import.meta.env.DEV && Boolean(searchParams.get('snapshot'))
  const platformRoute = controlPlane || location.pathname.startsWith('/platform-management')
  const domainSwitchPreview = designSnapshot && searchParams.get('domainMenu') === 'open'
  const hasRealAccessToken = currentTokens()?.accessToken.split('.').length === 3
  const [domains, setDomains] = useState<BusinessDomain[]>(designSnapshot ? snapshotDomains : [])
  const [selectedDomain, setSelectedDomain] = useState<BusinessDomain | null>(() => designSnapshot ? snapshotDomain : currentDomain())
  const [domainMenuOpen, setDomainMenuOpen] = useState(domainSwitchPreview)
  const [switchingDomainID, setSwitchingDomainID] = useState('')
  const [domainSwitchFeedback, setDomainSwitchFeedback] = useState<{ kind: 'success' | 'error'; text: string } | null>(null)
  const [canManage, setCanManage] = useState<boolean | null>(designSnapshot ? true : hasRealAccessToken ? null : false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [permissionNavigationOpen, setPermissionNavigationOpen] = useState(true)
  const [openUtility, setOpenUtility] = useState<'notifications' | 'help' | 'user' | null>(null)
  const [notifications, setNotifications] = useState<NotificationItem[]>(designSnapshot ? snapshotNotifications : [])
  const [notificationTotal, setNotificationTotal] = useState(designSnapshot ? 8 : 0)
  const [notificationLoading, setNotificationLoading] = useState(!designSnapshot)
  const [notificationError, setNotificationError] = useState('')
  const [profile, setProfile] = useState(() => designSnapshot
    ? { displayName: '王敏', avatarUrl: '/report-assets/avatars/wang-min.png', roles: ['DOMAIN_ADMIN'] }
    : { displayName: '', avatarUrl: '', roles: [] as string[] })

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
    void currentProfile()
      .then(result => {
        if (!cancelled) setProfile({
          displayName: result.displayName.trim(),
          avatarUrl: result.avatarUrl?.trim() ?? '',
          roles: Array.isArray(result.roles) ? result.roles : [],
        })
      })
      .catch(() => {
        if (!cancelled) setProfile(current => ({ ...current, displayName: '' }))
      })
    return () => {
      cancelled = true
      window.removeEventListener(domainCatalogChangedEvent, refreshDomains)
    }
  }, [designSnapshot, hasRealAccessToken])

  useEffect(() => {
    if (designSnapshot) return undefined
    // The global work inbox is a business-domain projection. A platform-only
    // administrator has no selected domain, so requesting it would correctly
    // return BUSINESS_DOMAIN_REQUIRED and the shared API recovery would then
    // bounce the control plane to /domain-access.
    if (platformRoute || !selectedDomain?.id) return undefined
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
  }, [designSnapshot, platformRoute, selectedDomain?.id])

  const businessNotificationsAvailable = designSnapshot || Boolean(selectedDomain?.id) && !platformRoute
  const visibleNotifications = businessNotificationsAvailable ? notifications : []
  const visibleNotificationTotal = businessNotificationsAvailable ? notificationTotal : 0
  const visibleNotificationLoading = businessNotificationsAvailable && notificationLoading
  const visibleNotificationError = businessNotificationsAvailable ? notificationError : ''

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

  useEffect(() => {
    if (!domainSwitchFeedback) return undefined
    const timer = window.setTimeout(() => setDomainSwitchFeedback(null), 3600)
    return () => window.clearTimeout(timer)
  }, [domainSwitchFeedback])

  const activeDomains = useMemo(
    () => domains.filter(item => item.status === 'ACTIVE'),
    [domains],
  )

  const chooseDomain = (domain: BusinessDomain) => {
    if (domain.id === selectedDomain?.id) {
      setDomainMenuOpen(false)
      return
    }
    setSwitchingDomainID(domain.id)
    setDomainSwitchFeedback(null)
    if (designSnapshot) {
      setSelectedDomain(domain)
      setDomainMenuOpen(false)
      setSwitchingDomainID('')
      setDomainSwitchFeedback({ kind: 'success', text: `已切换至${domain.name}，工作台内容已刷新` })
      try {
        selectDomain(domain)
      } catch {
        setDomainSwitchFeedback({ kind: 'error', text: '设计预览无法保存领域上下文，请刷新后重试' })
      }
      return
    }
    void switchBusinessDomain(domain)
      .then(() => {
        setSelectedDomain(domain)
        setDomainMenuOpen(false)
        setDomainSwitchFeedback({ kind: 'success', text: `已切换至${domain.name}，工作台内容已刷新` })
      })
      .catch(() => setDomainSwitchFeedback({ kind: 'error', text: `${domain.name}暂时无法进入，请检查访问权限后重试` }))
      .finally(() => setSwitchingDomainID(''))
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

  const userName = profile.displayName || '当前用户'
  const userRole = canManage || profile.roles.includes('platform_admin')
    ? '平台管理员'
    : profile.roles.includes('DOMAIN_ADMIN')
      ? '领域管理员'
      : '业务分析师'

  return (
    <div className={`app-shell app-shell-v2 ${import.meta.env.DEV && new URLSearchParams(location.search).get('qa') === '1920' ? 'qa-viewport-1920' : ''} ${sidebarCollapsed ? 'is-sidebar-collapsed' : ''} ${className}`.trim()}>
      <header className="global-header">
        <NavLink className="global-brand" to="/home" aria-label="返回分析首页">
          <img className="brand-logo" src="/haier-logo.svg" alt="Haier 海尔" />
          <strong>智能分析决策平台</strong>
        </NavLink>

        <div className="global-utilities" ref={utilityRef}>
          <AppButton text circle className="global-utility-button" type="button" aria-label="通知" aria-expanded={openUtility === 'notifications'} onClick={() => setOpenUtility(value => value === 'notifications' ? null : 'notifications')}>
            <Bell size={20} aria-hidden="true" />{visibleNotificationTotal > 0 && <span className="notification-count">{visibleNotificationTotal > 99 ? '99+' : visibleNotificationTotal}</span>}
          </AppButton>
          <AppButton text className="global-help-button" type="button" aria-expanded={openUtility === 'help'} onClick={() => setOpenUtility(value => value === 'help' ? null : 'help')}>
            <Question size={18} aria-hidden="true" /><span>帮助中心</span>
          </AppButton>
          <AppButton text className="global-user-button" type="button" aria-expanded={openUtility === 'user'} onClick={() => setOpenUtility(value => value === 'user' ? null : 'user')}>
            {profile.avatarUrl ? <img src={profile.avatarUrl} alt="" /> : <UserCircle size={30} weight="duotone" aria-hidden="true" />}
            <span>{userName}</span><CaretDown size={14} aria-hidden="true" />
          </AppButton>

          {openUtility === 'notifications' && <section className="utility-popover notification-popover" aria-label="通知中心">
            <header><div><strong>通知</strong><span>{visibleNotificationLoading ? '加载中' : visibleNotificationError ? '加载失败' : `${visibleNotificationTotal} 条未读`}</span></div><AppButton text circle type="button" aria-label="关闭通知" onClick={() => setOpenUtility(null)}><X size={16} /></AppButton></header>
            <div className="notification-popover-list">
              {visibleNotifications.map(item => <AppButton text type="button" key={item.id} onClick={() => openNotification(item)}><span>{item.kind}</span><strong>{item.title}</strong><small>{item.meta}</small></AppButton>)}
              {!visibleNotificationLoading && visibleNotificationError && <p className="notification-empty is-error"><WarningCircle size={18} />{visibleNotificationError}</p>}
              {!visibleNotificationLoading && !visibleNotificationError && visibleNotifications.length === 0 && <p className="notification-empty"><Check size={18} />当前没有未读通知</p>}
            </div>
            <footer><AppButton link type="button" onClick={() => { setOpenUtility(null); navigate('/home') }}>返回首页查看全部待办</AppButton></footer>
          </section>}

          {openUtility === 'help' && <section className="utility-popover help-popover" aria-label="帮助中心">
            <header><div><strong>帮助中心</strong><span>快速找到使用说明</span></div><AppButton text circle type="button" aria-label="关闭帮助" onClick={() => setOpenUtility(null)}><X size={16} /></AppButton></header>
            <AppButton text type="button" onClick={() => { setOpenUtility(null); navigate('/help') }}><BookOpenText size={18} /><span><strong>平台使用指南</strong><small>了解问数、报告与决策流程</small></span></AppButton>
            <AppButton text type="button" onClick={() => { setOpenUtility(null); navigate('/help?support=1') }}><ChatCircleDots size={18} /><span><strong>联系平台支持</strong><small>反馈问题并附带当前页面</small></span></AppButton>
          </section>}

          {openUtility === 'user' && <section className="utility-popover user-popover" aria-label="用户菜单">
            <header><div><strong>{userName}</strong><span>{selectedDomain?.name || '未选择领域'} · {userRole}</span></div></header>
            <AppButton text type="button" onClick={() => { setOpenUtility(null); navigate('/profile') }}><UserCircle size={18} />个人设置</AppButton>
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
              if (item.adminOnly && canManage === false) return null
              return <NavLink key={item.label} to={item.to!} title={sidebarCollapsed ? item.label : undefined}><Icon aria-hidden="true" size={19} /><span>{item.label}</span></NavLink>
            })}
          </section>)}
        </nav>
        <div className="sidebar-footer">
          <div className="sidebar-domain-wrap" ref={domainSwitcherRef}>
            {domainMenuOpen && <div className="domain-menu sidebar-domain-menu" role="menu" aria-label="切换业务领域">
              <header>
                <strong>切换领域</strong>
                <NavLink to={designSnapshot ? '/domain-access?snapshot=domain-access-v2' : '/domain-access'} onClick={() => setDomainMenuOpen(false)}>管理领域访问<ArrowSquareOut size={14} /></NavLink>
              </header>
              <div className="domain-menu-section-label">当前领域</div>
              {activeDomains.length > 0
                ? activeDomains.map(domain => {
                  const visual = domainVisual(domain.code)
                  const DomainIcon = visual.icon
                  const isCurrent = selectedDomain?.id === domain.id
                  const switching = switchingDomainID === domain.id
                  return <AppButton
                    text
                    className={`domain-option is-${visual.tone}`}
                    type="button"
                    role="menuitemradio"
                    aria-checked={isCurrent}
                    disabled={Boolean(switchingDomainID && !switching)}
                    key={domain.id}
                    onClick={() => chooseDomain(domain)}
                  >
                    <span className="domain-option-icon"><DomainIcon size={24} weight="duotone" /></span>
                    <span className="domain-option-copy">
                      <span className="domain-option-title"><strong>{domain.name}</strong>{isCurrent && <em>当前</em>}</span>
                      <small>角色：{domainRoleLabel(domain, currentSubject(), designSnapshot)}</small>
                      <span className="domain-option-description">{domain.description || domain.code}</span>
                    </span>
                    {switching && <SpinnerGap className="domain-option-spinner" size={17} />}
                  </AppButton>
                })
                : <p>暂无可用领域</p>}
              <footer><Info size={16} /><span>切换后将刷新当前工作台数据与内容</span></footer>
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
      {domainSwitchFeedback && <div className={`domain-switch-feedback is-${domainSwitchFeedback.kind}`} role="status">
        {domainSwitchFeedback.kind === 'success' ? <Check size={16} weight="bold" /> : <WarningCircle size={16} />}
        <span>{domainSwitchFeedback.text}</span>
        <AppButton text circle type="button" aria-label="关闭领域切换提示" onClick={() => setDomainSwitchFeedback(null)}><X size={14} /></AppButton>
      </div>}
    </div>
  )
}
