import { NavLink } from 'react-router-dom'

/** 资产管理中心区分可执行资产、向量语义资产和确定性解析规则。 */
export function AssetManagementTabs() {
  return (
    <nav className="asset-management-tabs" aria-label="资产管理分类">
      <NavLink to="/assets/overview">治理总览</NavLink>
      <NavLink to="/assets/metrics">指标与维度</NavLink>
      <NavLink to="/assets/semantics">业务词汇</NavLink>
      <NavLink to="/assets/dimension-values">维值决策图</NavLink>
      <NavLink to="/assets/parsing-rules">解析规则</NavLink>
    </nav>
  )
}
