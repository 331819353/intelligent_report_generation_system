import {
  CaretUpDown,
  ChartBar,
  ChatCenteredDots,
  Check,
  Database,
  GearSix,
  GlobeHemisphereWest,
  House,
  Stack,
  TreeStructure,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { administrationAPI, type BusinessDomain } from '../lib/administration'
import {
  bindBusinessDomain,
  currentTokens,
  forceLogout,
  switchBusinessDomain,
} from '../lib/auth'
import {
  currentDomain,
  domainCatalogChangedEvent,
} from '../lib/domain-context'

type AppShellProps = {
  title?: string
  eyebrow?: string
  children: ReactNode
  actions?: ReactNode
  className?: string
}

/** 为后台业务页面提供统一侧栏、顶栏和内容容器。 */
export function AppShell({ title = '智能报表平台', eyebrow = '工作台', children, actions, className = '' }: AppShellProps) {
  const location = useLocation()
  const domainSwitcherRef = useRef<HTMLDivElement>(null)
  const [domains, setDomains] = useState<BusinessDomain[]>([])
  const [selectedDomain, setSelectedDomain] = useState<BusinessDomain | null>(() => currentDomain())
  const [domainMenuOpen, setDomainMenuOpen] = useState(false)
  const [canManage, setCanManage] = useState(false)

  useEffect(() => {
    // 真实登录令牌是三段式 JWT；组件测试使用的占位令牌不触发额外网络请求。
    if (currentTokens()?.accessToken.split('.').length !== 3) return undefined
    let cancelled = false
    const loadDomains = () => administrationAPI.listDomains()
      .then(items => {
        if (cancelled) return
        setDomains(items)
        const active = items.filter(item => item.status === 'ACTIVE')
        const stored = currentDomain()
        if (stored && !active.some(item => item.id === stored.id)) {
          setSelectedDomain(null)
          forceLogout('BUSINESS_DOMAIN_DISABLED')
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
  }, [])

  useEffect(() => {
    if (!domainMenuOpen) return undefined
    const close = (event: MouseEvent) => {
      if (!domainSwitcherRef.current?.contains(event.target as Node)) setDomainMenuOpen(false)
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [domainMenuOpen])

  const activeDomains = useMemo(
    () => domains.filter(item => item.status === 'ACTIVE'),
    [domains],
  )

  const chooseDomain = async (domain: BusinessDomain) => {
    setDomainMenuOpen(false)
    try {
      await switchBusinessDomain(domain)
      setSelectedDomain(domain)
    } catch {
      forceLogout('BUSINESS_DOMAIN_UNAVAILABLE')
    }
  }

  return (
    <div className={`app-shell ${className}`.trim()}>
      <aside className="sidebar">
        <img className="brand-logo" src="/haier-logo.svg" alt="Haier 海尔" />
        <div className="brand-copy"><strong>智能分析决策平台</strong><span>Decision Intelligence</span></div>
        <nav aria-label="主导航">
          <span className="sidebar-section-label">工作空间</span>
          <NavLink to="/admin"><House aria-hidden="true" size={18} />工作台</NavLink>
          <NavLink to="/data-sources"><Database aria-hidden="true" size={18} />数据源配置中心</NavLink>
          <NavLink to="/datasets"><Stack aria-hidden="true" size={18} />数据集配置中心</NavLink>
          <NavLink
            to="/assets/overview"
            className={({ isActive }) => isActive || location.pathname.startsWith('/assets/') ? 'active' : ''}
          >
            <TreeStructure aria-hidden="true" size={18} />资产管理中心
          </NavLink>
          <NavLink to="/assistant"><ChatCenteredDots aria-hidden="true" size={18} />智能问答</NavLink>
          <span className="sidebar-section-label reports">报告</span>
          <NavLink to="/report-studio/draft"><ChartBar aria-hidden="true" size={18} />报告设计器</NavLink>
          {canManage && <>
            <span className="sidebar-section-label reports">管理</span>
            <NavLink to="/management"><GearSix aria-hidden="true" size={18} />管理中心</NavLink>
          </>}
        </nav>
        <div className="sidebar-footer">
          <div className="domain-switcher-wrap" ref={domainSwitcherRef}>
            {domainMenuOpen && <div className="domain-menu" role="menu" aria-label="切换业务领域">
              <header><span>切换领域</span><small>{activeDomains.length} 个可用领域</small></header>
              {activeDomains.length > 0
                ? activeDomains.map(domain => <button
                  type="button"
                  role="menuitemradio"
                  aria-checked={selectedDomain?.id === domain.id}
                  key={domain.id}
                  onClick={() => void chooseDomain(domain)}
                >
                  <span className="domain-option-icon">{domain.name.slice(0, 1)}</span>
                  <span><strong>{domain.name}</strong><small>{domain.description || domain.code}</small></span>
                  {selectedDomain?.id === domain.id && <Check size={16} weight="bold" aria-hidden="true" />}
                </button>)
                : <p>暂无可用领域</p>}
              {canManage && <NavLink to="/management" onClick={() => setDomainMenuOpen(false)}>
                <GearSix size={15} aria-hidden="true" />管理领域
              </NavLink>}
            </div>}
            <button
              className="domain-switcher"
              type="button"
              aria-haspopup="menu"
              aria-expanded={domainMenuOpen}
              onClick={() => setDomainMenuOpen(open => !open)}
            >
              <GlobeHemisphereWest size={18} weight="duotone" aria-hidden="true" />
              <span><small>当前领域</small><strong>{selectedDomain?.name || '选择业务领域'}</strong></span>
              <CaretUpDown size={15} aria-hidden="true" />
            </button>
          </div>
          <div className="tenant-chip">
            <span className="tenant-avatar">演</span>
            <span><small>当前租户</small><strong>演示组织</strong></span>
          </div>
        </div>
      </aside>
      <main className="main-stage">
        <header className="topbar">
          <div><span className="eyebrow">{eyebrow}</span><h1>{title}</h1></div>
          <div className="topbar-actions">{actions}</div>
        </header>
        {children}
      </main>
    </div>
  )
}
