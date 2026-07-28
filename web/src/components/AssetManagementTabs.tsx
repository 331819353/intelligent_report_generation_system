import { NavLink } from 'react-router-dom'

/** 资产管理中心将通用语义词库与 DWS 维度治理明确分开。 */
export function AssetManagementTabs() {
  return (
    <nav className="asset-management-tabs" aria-label="资产管理分类">
      <NavLink to="/assets/metrics">指标资产</NavLink>
      <NavLink to="/assets/semantics">语义资产</NavLink>
      <NavLink to="/assets/dimensions">维度资产</NavLink>
      <NavLink to="/assets/dimension-values">维度值映射</NavLink>
    </nav>
  )
}
