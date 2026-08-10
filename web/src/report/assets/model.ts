export type ReportLifecycle = 'DRAFT_ONLY' | 'PUBLISHED' | 'CHANGED' | 'OFFLINE'
export type ReportScope = 'all' | 'mine' | 'shared'
export type ReportAction = 'VIEW' | 'EDIT' | 'PUBLISH' | 'VERSIONS' | 'PERMISSIONS' | 'ARCHIVE' | 'RESTORE' | 'EXPORT' | 'SHARE' | 'AI_EDIT'

export type ReportAsset = {
  id: string
  code: string
  name: string
  reportType: 'REPORT' | 'DASHBOARD'
  ownerUserId: string
  ownerName: string
  ownerAvatar?: string
  lifecycle: ReportLifecycle
  currentVersionNo?: number
  draftRevisionNo: number
  unpublishedChanges: number
  updatedAt: string
  visibleCount: number
  editableCount: number
  shared: boolean
  allowedActions: ReportAction[]
  previewKind: 'operations' | 'sales' | 'quality' | 'inventory' | 'channel' | 'cashflow'
}

export const lifecycleLabels: Record<ReportLifecycle, string> = {
  DRAFT_ONLY: '待发布',
  PUBLISHED: '已发布',
  CHANGED: '有未发布修改',
  OFFLINE: '已下架',
}

export function canRun(asset: ReportAsset, action: ReportAction) {
  return asset.allowedActions.includes(action)
}

export function filterAssets(assets: ReportAsset[], scope: ReportScope, lifecycle: ReportLifecycle | 'ALL', query: string, subject = 'owner-wang') {
  const normalized = query.trim().toLocaleLowerCase('zh-CN')
  return assets.filter(asset => {
    if (scope === 'mine' && asset.ownerUserId !== subject) return false
    if (scope === 'shared' && !asset.shared) return false
    if (lifecycle !== 'ALL' && asset.lifecycle !== lifecycle) return false
    return !normalized || asset.name.toLocaleLowerCase('zh-CN').includes(normalized) || asset.code.toLocaleLowerCase('zh-CN').includes(normalized)
  })
}
