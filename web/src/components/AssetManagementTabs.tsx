import { NavLink } from 'react-router-dom'

/** 资产管理中心只保留两类用户心智：可执行的数据资产与纠正表达的语义资产。 */
export function AssetManagementTabs() {
  return (
    <nav className="asset-management-tabs" aria-label="资产管理分类">
      <NavLink to="/assets/metrics">数据资产</NavLink>
      <NavLink to="/assets/semantics">语义资产</NavLink>
    </nav>
  )
}
