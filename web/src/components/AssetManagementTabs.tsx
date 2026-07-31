import { NavLink } from 'react-router-dom'

/** 资产管理中心区分可执行资产、向量语义资产和确定性解析规则。 */
export function AssetManagementTabs() {
  return (
    <nav className="asset-management-tabs" aria-label="资产管理分类">
      <NavLink to="/assets/metrics">数据资产</NavLink>
      <NavLink to="/assets/semantics">语义资产</NavLink>
      <NavLink to="/assets/parsing-rules">语义解析规则</NavLink>
    </nav>
  )
}
