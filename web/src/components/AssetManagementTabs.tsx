import { ArrowLeftIcon, LockKeyIcon } from '@phosphor-icons/react'
import { Link } from 'react-router-dom'

/** 原生编辑器只维护类型化草稿；统一目录和发布治理回到资产中心。 */
export function AssetManagementTabs() {
  return (
    <nav className="asset-management-tabs asset-editor-context" aria-label="类型化资产编辑器上下文">
      <Link to="/assets/overview"><ArrowLeftIcon size={15} weight="bold" />返回统一资产中心</Link>
      <span><LockKeyIcon size={14} weight="duotone" />当前为类型化草稿编辑器；保存不会直接修改线上活动语义版本</span>
    </nav>
  )
}
