import {
  ChartBar,
  ChatCenteredDots,
  Database,
  House,
  PresentationChart,
  Stack,
  TreeStructure,
} from '@phosphor-icons/react'
import type { ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router-dom'

type AppShellProps = {
  title: string
  eyebrow: string
  children: ReactNode
  actions?: ReactNode
  className?: string
}

/** 为后台业务页面提供统一侧栏、顶栏和内容容器。 */
export function AppShell({ title, eyebrow, children, actions, className = '' }: AppShellProps) {
  const location = useLocation()
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
            to="/assets/metrics"
            className={({ isActive }) => isActive || location.pathname.startsWith('/assets/') ? 'active' : ''}
          >
            <TreeStructure aria-hidden="true" size={18} />资产管理中心
          </NavLink>
          <NavLink to="/assistant"><ChatCenteredDots aria-hidden="true" size={18} />智能问答</NavLink>
          <span className="sidebar-section-label reports">报告</span>
          <NavLink to="/designer/draft"><ChartBar aria-hidden="true" size={18} />报告设计器</NavLink>
          <NavLink to="/reports/demo"><PresentationChart aria-hidden="true" size={18} />在线报告</NavLink>
        </nav>
        <div className="tenant-chip">
          <span className="tenant-avatar">演</span>
          <span><small>当前租户</small><strong>演示组织</strong></span>
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
