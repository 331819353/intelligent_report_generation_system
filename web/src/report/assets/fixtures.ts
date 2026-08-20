import type { ReportAsset } from './model'

const commonActions = ['VIEW', 'EDIT', 'PUBLISH', 'VERSIONS', 'PERMISSIONS', 'ARCHIVE', 'DELETE', 'SHARE'] as const

export const reportAssetFixtures: ReportAsset[] = [
  {
    id: '00000000-0000-4000-8000-000000000701', code: 'RP-SCM-MON-001', name: '供应链月度经营报告', reportType: 'REPORT',
    ownerUserId: 'owner-wang', ownerName: '王敏', ownerAvatar: '/report-assets/avatars/wang-min.png', lifecycle: 'CHANGED', currentVersionNo: 7, draftRevisionNo: 23,
    unpublishedChanges: 3, updatedAt: '2026-08-10T10:12:00+08:00', visibleCount: 56, editableCount: 4, shared: false,
    allowedActions: [...commonActions],
  },
  {
    id: '00000000-0000-4000-8000-000000000702', code: 'RP-CHE-SALES-WK-012', name: '华东销售周报', reportType: 'REPORT',
    ownerUserId: 'owner-liu', ownerName: '刘洋', ownerAvatar: '/report-assets/avatars/liu-yang.png', lifecycle: 'PUBLISHED', currentVersionNo: 12, draftRevisionNo: 12,
    unpublishedChanges: 0, updatedAt: '2026-08-09T17:18:00+08:00', visibleCount: 38, editableCount: 3, shared: true,
    allowedActions: ['VIEW', 'VERSIONS'],
  },
  {
    id: '00000000-0000-4000-8000-000000000703', code: 'RP-QM-COST-ANL-003', name: '质量成本分析', reportType: 'REPORT',
    ownerUserId: 'owner-chen', ownerName: '陈晨', ownerAvatar: '/report-assets/avatars/chen-chen.png', lifecycle: 'DRAFT_ONLY', draftRevisionNo: 9,
    unpublishedChanges: 9, updatedAt: '2026-08-09T11:03:00+08:00', visibleCount: 8, editableCount: 2, shared: true,
    allowedActions: ['EDIT', 'PUBLISH', 'PERMISSIONS', 'ARCHIVE'],
  },
  {
    id: '00000000-0000-4000-8000-000000000704', code: 'RP-INV-HEALTH-005', name: '库存健康度看板', reportType: 'DASHBOARD',
    ownerUserId: 'owner-zhang', ownerName: '张磊', ownerAvatar: '/report-assets/avatars/liu-yang.png', lifecycle: 'PUBLISHED', currentVersionNo: 9, draftRevisionNo: 9,
    unpublishedChanges: 0, updatedAt: '2026-08-08T16:47:00+08:00', visibleCount: 41, editableCount: 5, shared: true,
    allowedActions: ['VIEW', 'EDIT', 'VERSIONS'],
  },
  {
    id: '00000000-0000-4000-8000-000000000705', code: 'RP-CH-REVIEW-004', name: '渠道复盘报告', reportType: 'REPORT',
    ownerUserId: 'owner-li', ownerName: '李娜', ownerAvatar: '/report-assets/avatars/chen-chen.png', lifecycle: 'OFFLINE', currentVersionNo: 3, draftRevisionNo: 14,
    unpublishedChanges: 0, updatedAt: '2026-08-07T10:22:00+08:00', visibleCount: 12, editableCount: 2, shared: true,
    allowedActions: ['VERSIONS', 'PERMISSIONS', 'RESTORE'],
  },
  {
    id: '00000000-0000-4000-8000-000000000706', code: 'RP-FI-CF-DASH-006', name: '财务现金流看板', reportType: 'DASHBOARD',
    ownerUserId: 'owner-zhou', ownerName: '周明', ownerAvatar: '/report-assets/avatars/liu-yang.png', lifecycle: 'PUBLISHED', currentVersionNo: 8, draftRevisionNo: 8,
    unpublishedChanges: 0, updatedAt: '2026-08-06T09:15:00+08:00', visibleCount: 25, editableCount: 2, shared: true,
    allowedActions: ['VIEW', 'VERSIONS'],
  },
]
