import type { ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router-dom'

type AppShellProps = {
  title: string
  eyebrow: string
  children: ReactNode
  actions?: ReactNode
}

/** 为后台业务页面提供统一侧栏、顶栏和内容容器。 */
export function AppShell({ title, eyebrow, children, actions }: AppShellProps) {
  const location = useLocation()
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand-mark">IR</div>
        <div className="brand-copy"><strong>智能报告</strong><span>Insight Studio</span></div>
        <nav aria-label="主导航">
          <NavLink to="/admin">工作台</NavLink>
          <NavLink to="/data-sources">数据源配置中心</NavLink>
          <NavLink to="/datasets">数据集配置中心</NavLink>
          <NavLink
            to="/assets/metrics"
            className={({ isActive }) => isActive || location.pathname.startsWith('/assets/') ? 'active' : ''}
          >
            资产管理中心
          </NavLink>
          <NavLink to="/designer/draft">报告设计器</NavLink>
          <NavLink to="/reports/demo">在线报告</NavLink>
        </nav>
        <div className="tenant-chip"><span>当前租户</span><strong>演示组织</strong></div>
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
