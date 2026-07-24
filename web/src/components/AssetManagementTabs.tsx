import { NavLink } from 'react-router-dom'

/** 资产管理中心的一级目录，统一承载指标、语义定义和维度成员倒排。 */
export function AssetManagementTabs() {
  return (
    <nav className="asset-management-tabs" aria-label="资产管理分类">
      <NavLink to="/assets/metrics">指标资产</NavLink>
      <NavLink to="/assets/semantics">语义资产</NavLink>
      <NavLink to="/assets/dimension-values">维度值映射</NavLink>
    </nav>
  )
}
