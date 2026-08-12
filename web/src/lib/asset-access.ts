import { apiRequest } from './api'

export type AssetSharingScope = 'PRIVATE' | 'DOMAIN'
export type AssetResourceType =
  | 'DATA_SOURCE'
  | 'DATASET'
  | 'METRIC'
  | 'DIMENSION'
  | 'SEMANTIC_TAG'
  | 'SEMANTIC_ASSET'

export type AssetSharing = {
  resourceType: AssetResourceType
  resourceId: string
  domainId: string
  domainName: string
  ownerUserId?: string
  sharingScope: AssetSharingScope
}

// One governed object that reads this asset. Restricting an asset to PRIVATE
// does not delete these, it removes the permission they depend on - which is
// why the impact is previewed before the change rather than after.
export type AssetAccessDependent = {
  type: 'DATASET' | 'SEMANTIC_MODEL' | 'REPORT'
  id: string
  name: string
  status: string
  ownerUserId?: string
  foreignOwner: boolean
}

export type AssetAccessImpact = AssetSharing & {
  targetScope: AssetSharingScope
  domainMemberCount: number
  dependents: AssetAccessDependent[]
  blockingDependents: number
  canApply: boolean
  blockedCode?: 'ASSET_SCOPE_NARROW_BLOCKED' | 'ASSET_SCOPE_OWNER_REQUIRED'
}

export const assetAccessAPI = {
  impact(
    resourceType: AssetResourceType,
    resourceID: string,
    sharingScope: AssetSharingScope,
  ) {
    return apiRequest<AssetAccessImpact>(
      `/v1/asset-access/${resourceType}/${encodeURIComponent(resourceID)}/impact?sharingScope=${sharingScope}`,
      { cache: 'no-store' },
    )
  },
  get(resourceType: AssetResourceType, resourceID: string) {
    return apiRequest<AssetSharing>(
      `/v1/asset-access/${resourceType}/${encodeURIComponent(resourceID)}`,
      { cache: 'no-store' },
    )
  },
  update(
    resourceType: AssetResourceType,
    resourceID: string,
    sharingScope: AssetSharingScope,
  ) {
    return apiRequest<AssetSharing>(
      `/v1/asset-access/${resourceType}/${encodeURIComponent(resourceID)}`,
      { method: 'PATCH', body: JSON.stringify({ sharingScope }) },
    )
  },
}
