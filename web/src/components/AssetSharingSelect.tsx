import { useState, useSyncExternalStore } from 'react'
import {
  assetAccessAPI,
  type AssetResourceType,
  type AssetSharingScope,
} from '../lib/asset-access'
import { currentSubject } from '../lib/auth'
import { currentDomainID, subscribeDomainChange } from '../lib/domain-context'

const labels: Record<AssetSharingScope, string> = {
  PRIVATE: '仅自己',
  DOMAIN: '领域内共享',
  PLATFORM: '平台共享',
}

export function AssetSharingSelect({
  resourceType,
  resourceID,
  value,
  ownerUserID,
  assetDomainID,
  disabled = false,
  onChange,
}: {
  resourceType: AssetResourceType
  resourceID: string
  value: AssetSharingScope
  ownerUserID: string | undefined
  assetDomainID: string | undefined
  disabled?: boolean
  onChange: (value: AssetSharingScope) => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const selectedDomainID = useSyncExternalStore(
    subscribeDomainChange,
    currentDomainID,
    () => '',
  )
  const isOwner = Boolean(ownerUserID) &&
    ownerUserID === currentSubject() &&
    Boolean(assetDomainID) &&
    assetDomainID === selectedDomainID

  const update = async (next: AssetSharingScope) => {
    if (next === value) return
    setBusy(true)
    setError('')
    try {
      const result = await assetAccessAPI.update(resourceType, resourceID, next)
      onChange(result.sharingScope)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '更新共享范围失败')
    } finally {
      setBusy(false)
    }
  }

  if (!isOwner) {
    return <span
      className={`asset-sharing-select scope-${value.toLowerCase()} is-readonly`}
      title="仅资产持有者可以在资产所属领域修改共享状态"
      onClick={event => event.stopPropagation()}
    >
      <span className="asset-sharing-select-label">{labels[value]}</span>
    </span>
  }

  return <label
    className={`asset-sharing-select scope-${value.toLowerCase()}${error ? ' has-error' : ''}`}
    title={error || '平台共享只允许跨领域读取；编辑仍限定在资产所属领域'}
    onClick={event => event.stopPropagation()}
  >
    <span className="asset-sharing-select-label">{busy ? '更新中' : labels[value]}</span>
    <svg className="asset-sharing-select-chevron" aria-hidden="true" viewBox="0 0 12 12">
      <path d="m3 4.75 3 3 3-3" />
    </svg>
    <select
      aria-label="共享范围"
      value={value}
      disabled={disabled || busy}
      onChange={event => void update(event.target.value as AssetSharingScope)}
    >
      <option value="PRIVATE">仅自己</option>
      <option value="DOMAIN">领域内共享</option>
      <option value="PLATFORM">平台共享</option>
    </select>
  </label>
}
