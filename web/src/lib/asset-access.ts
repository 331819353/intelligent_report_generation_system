import { apiRequest } from './api'

export type AssetSharingScope = 'PRIVATE' | 'DOMAIN' | 'PLATFORM'
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

export const assetAccessAPI = {
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
