import {
  Buildings,
  ArrowsClockwise,
  CaretLeft,
  CaretRight,
  Check,
  CheckCircle,
  Crown,
  Cube,
  DownloadSimple,
  Eye,
  FileText,
  GlobeHemisphereWest,
  LockKey,
  MagnifyingGlass,
  PencilSimple,
  Plus,
  ShieldCheck,
  SpinnerGap,
  UserCircle,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'
import { AppButton } from '../components/AppButton'
import { SubjectAttributesPanel } from '../components/SubjectAttributesPanel'
import { AppShell } from '../components/AppShell'
import {
  administrationAPI,
  type AdminUser,
  type AdminUserDomain,
  type BusinessDomain,
  type UserDeactivationPreview,
  type UserLifecycleBatch,
  type UserLifecycleMapping,
} from '../lib/administration'
import { currentSubject } from '../lib/auth'
import { notifyDomainCatalogChanged } from '../lib/domain-context'

type AccessRole = AdminUserDomain['memberRole']
type AccessDraft = { domainId: string; role: AccessRole }
type StatusFilter = 'ALL' | AdminUser['status']
type RoleFilter = 'ALL' | 'PLATFORM_ADMIN' | AccessRole
type TransferSelection = Record<string, string>

const lifecycleCategoryLabels: Record<string, string> = {
  DATA_SOURCE: '数据源', DATASET: '数据集', SEMANTIC_DOMAIN: '语义领域', SAVED_QUESTION: '保存的问题',
  REPORT: '报告', REPORT_SCHEDULE: '报告订阅计划', FEEDBACK_TICKET: '语义反馈工单',
  DATA_REQUEST_ASSIGNMENT: '数据申请', DECISION: '决策', DECISION_ACTION: '决策行动',
  KPI_BUNDLE: 'KPI 组合', TIME_CONTRACT: '时间合同', SEMANTIC_METRIC: '指标',
  SEMANTIC_DIMENSION: '维度', SEMANTIC_RELATIONSHIP: '语义关系', BUSINESS_TERM_VERSION: '业务词条',
  CERTIFIED_EXAMPLE_VERSION: '认证问法', RELEASE_REFERENCE: '语义发布引用',
  REPORT_SUBSCRIPTION: '报告订阅', REPORT_DELIVERY: '待发送报告', CONVERSATION_HISTORY: '问数历史',
  DATA_REQUEST_APPROVAL: '待审批数据申请', DECISION_APPROVAL: '待审批决策',
  RUNTIME_CONFIG_DRAFT: '运行配置草稿', DOMAIN_ADMIN: '最后一位领域管理员', PLATFORM_ADMIN: '最后一位平台管理员',
}

function transferKey(category: string, domainID: string) {
  return `${category}|${domainID}`
}

const statusLabels: Record<AdminUser['status'], string> = {
  ACTIVE: '启用',
  DISABLED: '已停用',
  LOCKED: '已锁定',
}

const capabilityGroups: Record<AccessRole, Array<{ icon: typeof Eye; label: string; items: string[] }>> = {
  MEMBER: [
    { icon: Eye, label: '数据权限', items: ['查看'] },
    { icon: FileText, label: '功能权限', items: ['新建分析', '编辑报告'] },
    { icon: ShieldCheck, label: '管理权限', items: ['无管理权限'] },
  ],
  DOMAIN_ADMIN: [
    { icon: Eye, label: '数据权限', items: ['查看', '导出'] },
    { icon: FileText, label: '功能权限', items: ['新建分析', '编辑分析', '发布审批'] },
    { icon: ShieldCheck, label: '管理权限', items: ['成员管理', '配置管理'] },
  ],
}

const snapshotDomains: BusinessDomain[] = [
  {
    id: 'snapshot-enterprise', code: 'BIZ_MANAGEMENT', name: '企业经营',
    description: '经营分析与管理决策支持，涵盖财务、预算、人力等核心经营主题。',
    status: 'ACTIVE', default: true, version: 4, createdAt: '2026-08-08T09:32:00+08:00',
    accessSensitivity: 'INTERNAL',
    administrators: [{ id: 'snapshot-zhang', employeeNo: 'ZW00123', email: 'zhangwei@haier.com', displayName: '张伟' }],
  },
  {
    id: 'snapshot-supply', code: 'SUPPLY_CHAIN', name: '供应链管理',
    description: '供应链计划、采购、库存、物流等端到端供应链运营分析。',
    status: 'ACTIVE', default: false, version: 3, createdAt: '2026-08-07T16:45:00+08:00',
    accessSensitivity: 'CONFIDENTIAL',
    administrators: [{ id: 'snapshot-zhou', employeeNo: 'ZT00314', email: 'zhoutao@haier.com', displayName: '周涛' }],
  },
  {
    id: 'snapshot-channel', code: 'SALES_CHANNEL', name: '渠道销售',
    description: '渠道销售目标、渠道库存与经销商表现分析，支持渠道策略优化。',
    status: 'ACTIVE', default: false, version: 2, createdAt: '2026-08-06T10:15:00+08:00',
    accessSensitivity: 'INTERNAL',
    administrators: [{ id: 'snapshot-zhao', employeeNo: 'ZM00632', email: 'zhaomin@haier.com', displayName: '赵敏' }],
  },
  {
    id: 'snapshot-quality', code: 'MANUFACTURING_QUALITY', name: '制造质量',
    description: '制造过程质量监控、质量追溯与改进分析。',
    status: 'ACTIVE', default: false, version: 2, createdAt: '2026-08-05T14:18:00+08:00',
    accessSensitivity: 'RESTRICTED',
    administrators: [{ id: 'snapshot-zhao', employeeNo: 'ZM00632', email: 'zhaomin@haier.com', displayName: '赵敏' }],
  },
]

function snapshotUser(
  id: string,
  displayName: string,
  employeeNo: string,
  email: string,
  domains: AdminUserDomain[],
  status: AdminUser['status'] = 'ACTIVE',
  lastLoginAt = '2026-08-11T09:15:00+08:00',
): AdminUser {
  return {
    id, displayName, employeeNo, email, domains, status, lastLoginAt,
    platformAdministrator: false,
    createdAt: '2026-07-18T10:00:00+08:00',
  }
}

const snapshotUsers: AdminUser[] = [
  snapshotUser('snapshot-zhang', '张伟', 'ZW00123', 'zhangwei@haier.com', [
    { id: 'snapshot-enterprise', code: 'BIZ_MANAGEMENT', name: '企业经营', default: true, memberRole: 'DOMAIN_ADMIN' },
    { id: 'snapshot-supply', code: 'SUPPLY_CHAIN', name: '供应链管理', default: false, memberRole: 'MEMBER' },
    { id: 'snapshot-channel', code: 'SALES_CHANNEL', name: '渠道销售', default: false, memberRole: 'MEMBER' },
  ]),
  snapshotUser('snapshot-li', '李娜', 'LN00876', 'lina@haier.com', [
    { id: 'snapshot-supply', code: 'SUPPLY_CHAIN', name: '供应链管理', default: false, memberRole: 'MEMBER' },
    { id: 'snapshot-enterprise', code: 'BIZ_MANAGEMENT', name: '企业经营', default: true, memberRole: 'MEMBER' },
  ], 'ACTIVE', '2026-08-10T16:42:00+08:00'),
  snapshotUser('snapshot-wang', '王强', 'WQ00451', 'wangqiang@haier.com', [
    { id: 'snapshot-quality', code: 'MANUFACTURING_QUALITY', name: '制造质量', default: false, memberRole: 'MEMBER' },
  ], 'ACTIVE', '2026-08-10T08:33:00+08:00'),
  snapshotUser('snapshot-zhao', '赵敏', 'ZM00632', 'zhaomin@haier.com', [
    { id: 'snapshot-channel', code: 'SALES_CHANNEL', name: '渠道销售', default: false, memberRole: 'DOMAIN_ADMIN' },
    { id: 'snapshot-quality', code: 'MANUFACTURING_QUALITY', name: '制造质量', default: false, memberRole: 'DOMAIN_ADMIN' },
  ], 'ACTIVE', '2026-08-09T14:21:00+08:00'),
  snapshotUser('snapshot-chen', '陈磊', 'CL00999', 'chenlei@haier.com', [
    { id: 'snapshot-channel', code: 'SALES_CHANNEL', name: '渠道销售', default: false, memberRole: 'MEMBER' },
  ], 'DISABLED', '2026-08-05T10:11:00+08:00'),
  snapshotUser('snapshot-liu', '刘洋', 'LY00721', 'liuyang@haier.com', [], 'ACTIVE', '2026-08-11T07:58:00+08:00'),
  snapshotUser('snapshot-sun', '孙悦', 'SY00588', 'sunyue@haier.com', [
    { id: 'snapshot-enterprise', code: 'BIZ_MANAGEMENT', name: '企业经营', default: true, memberRole: 'MEMBER' },
    { id: 'snapshot-supply', code: 'SUPPLY_CHAIN', name: '供应链管理', default: false, memberRole: 'MEMBER' },
  ], 'ACTIVE', '2026-08-08T17:05:00+08:00'),
  snapshotUser('snapshot-zhou', '周涛', 'ZT00314', 'zhoutao@haier.com', [
    { id: 'snapshot-supply', code: 'SUPPLY_CHAIN', name: '供应链管理', default: false, memberRole: 'DOMAIN_ADMIN' },
    { id: 'snapshot-enterprise', code: 'BIZ_MANAGEMENT', name: '企业经营', default: true, memberRole: 'MEMBER' },
  ], 'ACTIVE', '2026-08-09T11:26:00+08:00'),
  snapshotUser('snapshot-wu', '吴迪', 'WD00277', 'wudi@haier.com', [
    { id: 'snapshot-quality', code: 'MANUFACTURING_QUALITY', name: '制造质量', default: false, memberRole: 'MEMBER' },
  ], 'DISABLED', '2026-08-04T09:12:00+08:00'),
  snapshotUser('snapshot-gao', '高翔', 'GX00111', 'gaoxiang@haier.com', [
    { id: 'snapshot-channel', code: 'SALES_CHANNEL', name: '渠道销售', default: false, memberRole: 'MEMBER' },
    { id: 'snapshot-enterprise', code: 'BIZ_MANAGEMENT', name: '企业经营', default: true, memberRole: 'MEMBER' },
    { id: 'snapshot-supply', code: 'SUPPLY_CHAIN', name: '供应链管理', default: false, memberRole: 'MEMBER' },
  ], 'ACTIVE', '2026-08-11T08:19:00+08:00'),
]

const avatarPaths = [
  '/report-assets/avatars/liu-yang.png',
  '/report-assets/avatars/wang-min.png',
  '/report-assets/avatars/chen-chen.png',
]

function avatarFor(user: AdminUser) {
  const score = [...user.id].reduce((total, character) => total + character.charCodeAt(0), 0)
  return avatarPaths[score % avatarPaths.length]
}

function formatDateTime(value?: string) {
  if (!value) return '从未登录'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date).replaceAll('/', '-')
}

function primaryRole(user: AdminUser) {
  if (user.platformAdministrator) return '平台管理员'
  if (user.domains.some(domain => domain.memberRole === 'DOMAIN_ADMIN')) return '领域管理员'
  return '普通成员'
}

function domainIcon(code: string) {
  if (code.includes('SUPPLY')) return Cube
  if (code.includes('SALES')) return GlobeHemisphereWest
  return Buildings
}

function draftFor(user: AdminUser): AccessDraft[] {
  return user.domains.map(domain => ({ domainId: domain.id, role: domain.memberRole }))
}

async function loadPermissionData() {
  return Promise.all([
    administrationAPI.listManagedDomains(),
    administrationAPI.listUsers(),
  ])
}

/** 平台管理员按用户维护领域归属与当前真实支持的成员角色。 */
export function UserPermissionsPage() {
  const params = new URLSearchParams(window.location.search)
  const designSnapshot = import.meta.env.DEV && Boolean(params.get('snapshot'))
  const requestedUserID = params.get('userId') ?? ''
  const initialUserID = designSnapshot && !snapshotUsers.some(user => user.id === requestedUserID)
    ? snapshotUsers[0].id
    : requestedUserID || (designSnapshot ? snapshotUsers[0].id : '')
  const [domains, setDomains] = useState<BusinessDomain[]>(designSnapshot ? snapshotDomains : [])
  const [users, setUsers] = useState<AdminUser[]>(designSnapshot ? snapshotUsers : [])
  const [selectedID, setSelectedID] = useState(initialUserID)
  const [draft, setDraft] = useState<AccessDraft[]>(designSnapshot ? draftFor(snapshotUsers[0]) : [])
  const [roleDomainID, setRoleDomainID] = useState(designSnapshot ? snapshotUsers[0].domains[0].id : '')
  const [addDomainID, setAddDomainID] = useState('')
  const [addingDomain, setAddingDomain] = useState(false)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('ALL')
  const [roleFilter, setRoleFilter] = useState<RoleFilter>('ALL')
  const [domainFilter, setDomainFilter] = useState('ALL')
  const [loading, setLoading] = useState(!designSnapshot)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState(params.get('from') === 'approval' ? '领域申请已通过，请继续确认成员角色与生效权限。' : '')
  const [deactivationPreview, setDeactivationPreview] = useState<UserDeactivationPreview | null>(null)
  const [deactivationBatch, setDeactivationBatch] = useState<UserLifecycleBatch | null>(null)
  const [transferSelection, setTransferSelection] = useState<TransferSelection>({})
  const [lifecycleBusy, setLifecycleBusy] = useState(false)
  const signedInUserID = currentSubject()

  const refresh = async () => {
    const [nextDomains, nextUsers] = await loadPermissionData()
    const nextSelected = nextUsers.find(user => user.id === selectedID) ?? nextUsers[0]
    setDomains(nextDomains)
    setUsers(nextUsers)
    setSelectedID(nextSelected?.id ?? '')
    setDraft(nextSelected ? draftFor(nextSelected) : [])
    setRoleDomainID(nextSelected?.domains[0]?.id ?? '')
  }

  useEffect(() => {
    if (designSnapshot) return undefined
    let cancelled = false
    const timer = window.setTimeout(() => {
      setLoading(true)
      setError('')
      void loadPermissionData().then(([nextDomains, nextUsers]) => {
        if (cancelled) return
        const nextSelected = nextUsers.find(user => user.id === requestedUserID) ?? nextUsers[0]
        setDomains(nextDomains)
        setUsers(nextUsers)
        setSelectedID(nextSelected?.id ?? '')
        setDraft(nextSelected ? draftFor(nextSelected) : [])
        setRoleDomainID(nextSelected?.domains[0]?.id ?? '')
        setLoading(false)
      }).catch(cause => {
        if (cancelled) return
        setError(cause instanceof Error ? cause.message : '用户权限加载失败')
        setLoading(false)
      })
    }, 0)
    return () => { cancelled = true; window.clearTimeout(timer) }
  }, [designSnapshot, requestedUserID])

  const selected = users.find(user => user.id === selectedID) ?? null

  const chooseUser = (userID: string) => {
    const user = users.find(item => item.id === userID)
    setSelectedID(user?.id ?? '')
    const nextDraft = user ? draftFor(user) : []
    setDraft(nextDraft)
    setRoleDomainID(nextDraft[0]?.domainId ?? '')
    setAddDomainID('')
    setAddingDomain(false)
  }

  const visibleUsers = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase('zh-CN')
    return users.filter(user => {
      if (statusFilter !== 'ALL' && user.status !== statusFilter) return false
      if (roleFilter === 'PLATFORM_ADMIN' && !user.platformAdministrator) return false
      if (roleFilter === 'DOMAIN_ADMIN' && !user.domains.some(domain => domain.memberRole === 'DOMAIN_ADMIN')) return false
      if (roleFilter === 'MEMBER' && (user.platformAdministrator || user.domains.some(domain => domain.memberRole === 'DOMAIN_ADMIN'))) return false
      if (domainFilter !== 'ALL' && !user.domains.some(domain => domain.id === domainFilter)) return false
      if (!normalized) return true
      return [user.displayName, user.employeeNo, user.email, ...user.domains.map(domain => `${domain.name} ${domain.code}`)]
        .some(value => value.toLocaleLowerCase('zh-CN').includes(normalized))
    })
  }, [domainFilter, query, roleFilter, statusFilter, users])

  const draftDomainIDs = new Set(draft.map(item => item.domainId))
  const availableDomains = domains.filter(domain => domain.status === 'ACTIVE' && !draftDomainIDs.has(domain.id))
  const currentRole = draft.find(item => item.domainId === roleDomainID)?.role ?? 'MEMBER'
  const selectedDomain = domains.find(domain => domain.id === roleDomainID)

  const addDomain = () => {
    if (!addDomainID) return
    setDraft(current => [...current, { domainId: addDomainID, role: 'MEMBER' }])
    setRoleDomainID(addDomainID)
    setAddDomainID('')
    setAddingDomain(false)
  }

  const removeDomain = (domainID: string) => {
    setDraft(current => current.filter(item => item.domainId !== domainID))
    if (roleDomainID === domainID) {
      const replacement = draft.find(item => item.domainId !== domainID)
      setRoleDomainID(replacement?.domainId ?? '')
    }
  }

  const changeRole = (role: AccessRole) => {
    setDraft(current => current.map(item => item.domainId === roleDomainID ? { ...item, role } : item))
  }

  const save = async () => {
    if (!selected || selected.platformAdministrator) return
    const currentByDomain = new Map(selected.domains.map(domain => [domain.id, domain.memberRole]))
    const nextByDomain = new Map(draft.map(item => [item.domainId, item.role]))
    const unsafeRemoval = domains.find(domain => {
      const wasAdministrator = currentByDomain.get(domain.id) === 'DOMAIN_ADMIN'
      const remainsAdministrator = nextByDomain.get(domain.id) === 'DOMAIN_ADMIN'
      return wasAdministrator && !remainsAdministrator && domain.administrators.length <= 1
    })
    if (unsafeRemoval) {
      setError(`领域“${unsafeRemoval.name}”至少需要保留一位管理员，请先新增其他管理员`)
      return
    }

    setBusy(true)
    setError('')
    setNotice('')
    try {
      if (designSnapshot) {
        const nextDomains = draft.map(item => {
          const domain = domains.find(value => value.id === item.domainId)!
          return { id: domain.id, code: domain.code, name: domain.name, default: domain.default, memberRole: item.role }
        })
        setUsers(current => current.map(user => user.id === selected.id ? { ...user, domains: nextDomains } : user))
      } else {
        for (const item of draft) {
          const previousRole = currentByDomain.get(item.domainId)
          if (!previousRole) await administrationAPI.assignUserDomain(selected.id, item.domainId)
          if (item.role === 'DOMAIN_ADMIN' && previousRole !== 'DOMAIN_ADMIN') {
            const domain = domains.find(value => value.id === item.domainId)
            if (domain) await administrationAPI.replaceDomainAdministrators(domain.id, [
              ...domain.administrators.map(administrator => administrator.id),
              selected.id,
            ])
          }
          if (item.role === 'MEMBER' && previousRole === 'DOMAIN_ADMIN') {
            const domain = domains.find(value => value.id === item.domainId)
            if (domain) await administrationAPI.replaceDomainAdministrators(
              domain.id,
              domain.administrators.filter(administrator => administrator.id !== selected.id).map(administrator => administrator.id),
            )
          }
        }
        for (const domain of selected.domains) {
          if (nextByDomain.has(domain.id)) continue
          if (domain.memberRole === 'DOMAIN_ADMIN') {
            const source = domains.find(value => value.id === domain.id)
            if (source) await administrationAPI.replaceDomainAdministrators(
              source.id,
              source.administrators.filter(administrator => administrator.id !== selected.id).map(administrator => administrator.id),
            )
          }
          await administrationAPI.revokeUserDomain(selected.id, domain.id)
        }
        await refresh()
      }
      notifyDomainCatalogChanged()
      setNotice(`${selected.displayName}的领域与角色配置已生效`)
    } catch (cause) {
      if (!designSnapshot) await refresh()
      setError(cause instanceof Error ? cause.message : '保存权限配置失败')
    } finally {
      setBusy(false)
    }
  }

  const updateStatus = async () => {
    if (!selected || selected.id === signedInUserID) return
    if (selected.status === 'ACTIVE') {
      setLifecycleBusy(true)
      setError('')
      setNotice('')
      try {
        const preview = designSnapshot ? {
          targetUserId: selected.id,
          canDisable: true,
          counts: { REPORT: 2, DATASET: 1, REPORT_SUBSCRIPTION: 1 },
          items: [
            { category: 'REPORT', domainId: selected.domains[0]?.id ?? '', objectId: 'snapshot-report-1', disposition: 'TRANSFER' as const, sourceVersion: '1' },
            { category: 'REPORT', domainId: selected.domains[0]?.id ?? '', objectId: 'snapshot-report-2', disposition: 'TRANSFER' as const, sourceVersion: '1' },
            { category: 'DATASET', domainId: selected.domains[0]?.id ?? '', objectId: 'snapshot-dataset-1', disposition: 'TRANSFER' as const, sourceVersion: '2' },
            { category: 'REPORT_SUBSCRIPTION', domainId: selected.domains[0]?.id ?? '', objectId: 'snapshot-subscription-1', disposition: 'AUTO_CLOSE' as const, sourceVersion: '1' },
          ],
        } satisfies UserDeactivationPreview : await administrationAPI.previewUserDeactivation(selected.id)
        const defaults: TransferSelection = {}
        for (const item of preview.items.filter(value => value.disposition === 'TRANSFER')) {
          const key = transferKey(item.category, item.domainId)
          if (defaults[key]) continue
          const candidate = users.find(user => user.id !== selected.id && user.status === 'ACTIVE' &&
            (!item.domainId || user.platformAdministrator || user.domains.some(domain => domain.id === item.domainId)))
          if (candidate) defaults[key] = candidate.id
        }
        setTransferSelection(defaults)
        setDeactivationBatch(null)
        setDeactivationPreview(preview)
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : '停用影响分析加载失败')
      } finally {
        setLifecycleBusy(false)
      }
      return
    }
    const nextStatus = 'ACTIVE'
    setBusy(true)
    setError('')
    try {
      if (designSnapshot) {
        setUsers(current => current.map(user => user.id === selected.id ? { ...user, status: nextStatus } : user))
      } else {
        await administrationAPI.updateUserStatus(selected.id, nextStatus)
        await refresh()
      }
      setNotice(`${selected.displayName}的账号已${nextStatus === 'ACTIVE' ? '恢复' : '停用'}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '账号状态更新失败')
    } finally {
      setBusy(false)
    }
  }

  const executeDeactivation = async () => {
    if (!selected || !deactivationPreview?.canDisable) return
    const transferGroups = new Map<string, UserLifecycleMapping>()
    for (const item of deactivationPreview.items.filter(value => value.disposition === 'TRANSFER')) {
      const key = transferKey(item.category, item.domainId)
      const receiverUserId = transferSelection[key]
      if (!receiverUserId) {
        setError(`请为${lifecycleCategoryLabels[item.category] ?? item.category}选择接收人`)
        return
      }
      transferGroups.set(key, { category: item.category, domainId: item.domainId, receiverUserId })
    }
    setLifecycleBusy(true)
    setError('')
    try {
      if (designSnapshot) {
        setUsers(current => current.map(user => user.id === selected.id ? { ...user, status: 'DISABLED' } : user))
        setDeactivationPreview(null)
        setNotice(`${selected.displayName}的资产已完成转交，账号与当前会话已停用`)
      } else {
        const batch = await administrationAPI.executeUserDeactivation(selected.id, [...transferGroups.values()])
        setDeactivationBatch(batch)
        if (batch.status === 'TRANSFER_FAILED') {
          setError('部分资产转交失败，系统未完成停用。请重试本次安全停用。')
          return
        }
        await refresh()
        setDeactivationPreview(null)
        setNotice(`${selected.displayName}的资产已完成转交，账号与当前会话已停用`)
      }
      notifyDomainCatalogChanged()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '安全停用执行失败')
    } finally {
      setLifecycleBusy(false)
    }
  }

  const retryDeactivation = async () => {
    if (!deactivationBatch) return
    setLifecycleBusy(true)
    setError('')
    try {
      const batch = await administrationAPI.retryUserDeactivation(deactivationBatch.id, deactivationBatch.recordVersion)
      setDeactivationBatch(batch)
      if (batch.status === 'COMPLETED') {
        await refresh()
        setDeactivationPreview(null)
        setNotice('资产转交已恢复完成，账号与当前会话已安全停用')
      } else {
        setError('转交仍未完成，请根据失败项目调整接收人后重试')
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '重试安全停用失败')
    } finally {
      setLifecycleBusy(false)
    }
  }

  const exportUsers = () => {
    const header = ['姓名', '工号', '邮箱', '角色', '所属领域', '状态', '最近登录']
    const rows = visibleUsers.map(user => [
      user.displayName, user.employeeNo, user.email, primaryRole(user),
      user.domains.map(domain => domain.name).join('、'), statusLabels[user.status], formatDateTime(user.lastLoginAt),
    ])
    const csv = [header, ...rows].map(row => row.map(value => `"${String(value).replaceAll('"', '""')}"`).join(',')).join('\n')
    const link = document.createElement('a')
    link.href = URL.createObjectURL(new Blob([`\ufeff${csv}`], { type: 'text/csv;charset=utf-8' }))
    link.download = `用户权限-${new Date().toISOString().slice(0, 10)}.csv`
    link.click()
    URL.revokeObjectURL(link.href)
  }

  return <AppShell className="user-permissions-shell" controlPlane>
    <section className="user-permissions-page">
      <header className="user-permissions-heading">
        <div className="user-permissions-breadcrumb"><span>权限管理</span><CaretRight size={12} /><strong>用户权限</strong></div>
        <h1>用户权限</h1>
        <p>管理成员所属领域、角色与实际生效权限</p>
      </header>

      <div className="user-permissions-toolbar">
        <label className="user-permissions-search">
          <MagnifyingGlass size={18} aria-hidden="true" />
          <input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索姓名、邮箱或工号" aria-label="搜索姓名、邮箱或工号" />
          {query && <AppButton text circle type="button" aria-label="清空搜索" onClick={() => setQuery('')}><X size={14} /></AppButton>}
        </label>
        <div className="user-permissions-filters">
          <label><span className="sr-only">状态</span><select value={statusFilter} onChange={event => setStatusFilter(event.target.value as StatusFilter)}><option value="ALL">全部状态</option><option value="ACTIVE">启用</option><option value="DISABLED">已停用</option><option value="LOCKED">已锁定</option></select></label>
          <label><span className="sr-only">角色</span><select value={roleFilter} onChange={event => setRoleFilter(event.target.value as RoleFilter)}><option value="ALL">全部角色</option><option value="PLATFORM_ADMIN">平台管理员</option><option value="DOMAIN_ADMIN">领域管理员</option><option value="MEMBER">普通成员</option></select></label>
          <label><span className="sr-only">领域</span><select value={domainFilter} onChange={event => setDomainFilter(event.target.value)}><option value="ALL">全部领域</option>{domains.map(domain => <option value={domain.id} key={domain.id}>{domain.name}</option>)}</select></label>
          <AppButton type="button" onClick={() => { setStatusFilter('ALL'); setRoleFilter('ALL'); setDomainFilter('ALL'); setQuery('') }}>重置</AppButton>
          <AppButton type="button" onClick={exportUsers}><DownloadSimple size={16} />批量导出</AppButton>
        </div>
      </div>

      {(error || notice) && <div className={`user-permissions-feedback ${error ? 'is-error' : 'is-success'}`} role={error ? 'alert' : 'status'}>
        {error ? <WarningCircle size={18} /> : <CheckCircle size={18} />}
        <span>{error || notice}</span>
        <AppButton text circle type="button" aria-label="关闭提示" onClick={() => { setError(''); setNotice('') }}><X size={14} /></AppButton>
      </div>}

      <div className="user-permissions-workspace">
        <section className="user-permissions-table-panel" aria-label="用户目录">
          <div className="user-permissions-table-header" role="row">
            <span aria-hidden="true" /><span>姓名</span><span>工号</span><span>邮箱</span><span>所属领域</span><span>角色</span><span>领域数量</span><span>状态</span><span>最近登录时间</span><span>操作</span>
          </div>
          <div className="user-permissions-table-body">
            {loading && <div className="user-permissions-state"><SpinnerGap className="spin" size={28} /><strong>正在加载用户目录…</strong></div>}
            {!loading && visibleUsers.map(user => {
              const active = user.id === selected?.id
              return <article className={`user-permissions-row ${active ? 'is-selected' : ''}`} role="row" tabIndex={0} key={user.id} onClick={() => chooseUser(user.id)} onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') chooseUser(user.id) }}>
                <span className={`user-row-check ${active ? 'is-checked' : ''}`} aria-hidden="true">{active && <Check size={12} weight="bold" />}</span>
                <span className="user-row-person"><img src={avatarFor(user)} alt="" /><strong>{user.displayName}</strong></span>
                <code>{user.employeeNo || '—'}</code>
                <span className="user-row-email" title={user.email}>{user.email}</span>
                <span className="user-row-domains" title={user.domains.map(domain => domain.name).join('、')}>{user.domains[0]?.name ?? '暂未加入'}{user.domains.length > 1 ? ` 等 ${user.domains.length} 个` : ''}</span>
                <span>{primaryRole(user)}</span>
                <span>{user.platformAdministrator ? '全部' : user.domains.length}</span>
                <span className={`user-status-dot is-${user.status.toLocaleLowerCase()}`}><i />{statusLabels[user.status]}</span>
                <time dateTime={user.lastLoginAt}>{formatDateTime(user.lastLoginAt)}</time>
                <AppButton link type="button" onClick={event => { event.stopPropagation(); chooseUser(user.id) }}>编辑</AppButton>
              </article>
            })}
            {!loading && visibleUsers.length === 0 && <div className="user-permissions-state"><UserCircle size={30} weight="duotone" /><strong>没有匹配的用户</strong><small>请调整搜索条件或筛选范围</small></div>}
          </div>
          <footer className="user-permissions-pagination">
            <span>共 {visibleUsers.length} 条</span><span className="user-page-size">10条/页<CaretRight size={13} /></span><AppButton text circle disabled aria-label="上一页"><CaretLeft size={14} /></AppButton><span className="user-page-current">1</span><AppButton text circle disabled aria-label="下一页"><CaretRight size={14} /></AppButton><span>前往</span><span className="user-page-input">1</span><span>页</span>
          </footer>
        </section>

        <aside className="user-access-editor" aria-label="编辑成员权限">
          {selected ? <>
            <header><h2>编辑成员权限</h2><AppButton text circle type="button" aria-label="关闭编辑面板" onClick={() => chooseUser('')}><X size={18} /></AppButton></header>
            <section className="user-access-identity">
              <img src={avatarFor(selected)} alt="" />
              <div><strong>{selected.displayName}<span className={`user-account-badge is-${selected.status.toLocaleLowerCase()}`}>{statusLabels[selected.status]}</span></strong><small>工号：{selected.employeeNo || '—'}</small><small>邮箱：{selected.email}</small><small>身份：{primaryRole(selected)}</small><small>最近登录：{formatDateTime(selected.lastLoginAt)}</small></div>
            </section>

            {selected.platformAdministrator ? <section className="user-access-fixed"><Crown size={22} weight="duotone" /><div><strong>平台管理员为固定最高权限</strong><p>平台管理员不保存领域归属。如需调整，请前往角色配置页移除平台管理员身份。</p></div></section> : <>
              <section className="user-access-domains">
                <header><h3>领域管理 <span>（已选 {draft.length} 个）</span></h3><AppButton plain type="button" disabled={availableDomains.length === 0} onClick={() => setAddingDomain(value => !value)}><Plus size={14} />添加领域</AppButton></header>
                <div className="user-access-domain-list">
                  {draft.map(item => {
                    const domain = domains.find(value => value.id === item.domainId)
                    if (!domain) return null
                    const Icon = domainIcon(domain.code)
                    return <article className={roleDomainID === domain.id ? 'is-active' : ''} key={domain.id} onClick={() => setRoleDomainID(domain.id)}>
                      <span className="user-domain-icon"><Icon size={20} weight="duotone" /></span>
                      <div><strong>{domain.name}</strong><small>{domain.code}</small></div>
                      <p>{domain.description}</p>
                      <AppButton text circle type="button" aria-label={`移除${domain.name}`} onClick={event => { event.stopPropagation(); removeDomain(domain.id) }}><X size={14} /></AppButton>
                    </article>
                  })}
                  {draft.length === 0 && <div className="user-access-empty-domain"><LockKey size={20} /><span>尚未分配领域，添加后才会产生领域权限。</span></div>}
                </div>
                {addingDomain && <div className="user-access-add-domain"><select value={addDomainID} onChange={event => setAddDomainID(event.target.value)} aria-label="选择要添加的领域"><option value="">选择要添加的领域</option>{availableDomains.map(domain => <option value={domain.id} key={domain.id}>{domain.name}</option>)}</select><AppButton type="button" disabled={!addDomainID} onClick={addDomain}><Check size={15} />确认</AppButton></div>}
              </section>

              <section className="user-access-role">
                <header><h3>角色设置</h3>{selectedDomain && <small>当前领域：{selectedDomain.name}</small>}</header>
                <label><span>角色</span><select value={currentRole} disabled={!roleDomainID} onChange={event => changeRole(event.target.value as AccessRole)}><option value="MEMBER">普通成员</option><option value="DOMAIN_ADMIN">领域管理员</option></select></label>
                <p>{currentRole === 'DOMAIN_ADMIN' ? '可管理当前领域成员、配置与发布审批。' : '可在所属领域中查看资产、创建分析并提交配置。'}</p>
              </section>

              <section className="user-effective-access">
                <header><h3>生效权限</h3><AppButton link type="button" onClick={() => setNotice('详细权限由服务端按角色与领域实时计算。')}>查看详情</AppButton></header>
                <div className="user-effective-groups">{capabilityGroups[currentRole].map(group => { const Icon = group.icon; return <div key={group.label}><strong><Icon size={16} weight="duotone" />{group.label}</strong><span>{group.items.map(item => <i key={item}>{item}</i>)}</span></div> })}</div>
                <p>生效权限将基于该成员的角色与所属领域自动计算。</p>
              </section>

              <SubjectAttributesPanel
                userID={selected.id}
                disabled={busy || selected.platformAdministrator}
                onNotice={(tone, message) => {
                  if (tone === 'success') { setNotice(message); setError('') }
                  else { setError(message) }
                }}
              />
            </>}

            <footer>
              <AppButton type="button" variant="primary" disabled={busy || selected.platformAdministrator} onClick={() => void save()}>{busy ? <SpinnerGap className="spin" size={16} /> : <Check size={16} weight="bold" />}{busy ? '保存中…' : '保存配置'}</AppButton>
              <AppButton link type="button" disabled={busy || lifecycleBusy || selected.id === signedInUserID} onClick={() => void updateStatus()}>{lifecycleBusy ? '正在分析影响…' : selected.status === 'ACTIVE' ? '安全停用此账号' : '恢复此账号'}</AppButton>
            </footer>
          </> : <div className="user-permissions-state"><PencilSimple size={30} weight="duotone" /><strong>选择一位用户</strong><small>查看并编辑其领域与角色配置</small></div>}
        </aside>
      </div>

      {deactivationPreview && selected && <div className="user-lifecycle-backdrop" role="presentation" onMouseDown={event => { if (event.currentTarget === event.target && !lifecycleBusy) setDeactivationPreview(null) }}>
        <section className="user-lifecycle-dialog" role="dialog" aria-modal="true" aria-labelledby="user-lifecycle-title">
          <header>
            <span><ShieldCheck size={22} weight="duotone" /></span>
            <div><h2 id="user-lifecycle-title">安全停用 {selected.displayName}</h2><p>系统将先处理资产归属与未完成工作，再撤销会话并停用账号。</p></div>
            <AppButton text circle type="button" aria-label="关闭" disabled={lifecycleBusy} onClick={() => setDeactivationPreview(null)}><X size={18} /></AppButton>
          </header>

          {!deactivationPreview.canDisable && <div className="user-lifecycle-blocked" role="alert">
            <WarningCircle size={22} weight="fill" />
            <div><strong>当前不能直接停用</strong><p>该用户仍是唯一管理员或持有待审批职责。请先在对应模块新增管理员或完成审批，再重新分析。</p></div>
          </div>}

          <div className="user-lifecycle-summary">
            <div><strong>{deactivationPreview.items.filter(item => item.disposition === 'TRANSFER').length}</strong><span>需转交资产</span></div>
            <div><strong>{deactivationPreview.items.filter(item => item.disposition === 'AUTO_CLOSE').length}</strong><span>自动关闭事项</span></div>
            <div><strong>{deactivationPreview.items.filter(item => item.disposition === 'READ_ONLY').length}</strong><span>保留只读历史</span></div>
            <div className={deactivationPreview.canDisable ? 'is-safe' : 'is-blocked'}><strong>{deactivationPreview.items.filter(item => item.disposition === 'BLOCK').length}</strong><span>阻断事项</span></div>
          </div>

          <div className="user-lifecycle-items">
            {Array.from(new Map(deactivationPreview.items.map(item => [transferKey(item.category, item.domainId), item])).values()).map(item => {
              const count = deactivationPreview.items.filter(candidate => candidate.category === item.category && candidate.domainId === item.domainId).length
              const domain = domains.find(value => value.id === item.domainId)
              const candidates = users.filter(user => user.id !== selected.id && user.status === 'ACTIVE' &&
                (!item.domainId || user.platformAdministrator || user.domains.some(userDomain => userDomain.id === item.domainId)))
              return <article className={`is-${item.disposition.toLocaleLowerCase()}`} key={transferKey(item.category, item.domainId)}>
                <span className="user-lifecycle-item-icon">{item.disposition === 'BLOCK' ? <WarningCircle size={18} /> : item.disposition === 'TRANSFER' ? <GlobeHemisphereWest size={18} /> : <CheckCircle size={18} />}</span>
                <div><strong>{lifecycleCategoryLabels[item.category] ?? item.category}</strong><small>{domain?.name ?? '平台范围'} · {count} 项</small></div>
                {item.disposition === 'TRANSFER' ? <label><span className="sr-only">选择接收人</span><select value={transferSelection[transferKey(item.category, item.domainId)] ?? ''} onChange={event => setTransferSelection(current => ({ ...current, [transferKey(item.category, item.domainId)]: event.target.value }))}><option value="">选择接收人</option>{candidates.map(user => <option value={user.id} key={user.id}>{user.displayName} · {primaryRole(user)}</option>)}</select></label> : <span className="user-lifecycle-disposition">{item.disposition === 'AUTO_CLOSE' ? '停用时自动关闭' : item.disposition === 'READ_ONLY' ? '历史记录保留' : '需先人工解除'}</span>}
              </article>
            })}
          </div>

          <footer>
            <p><LockKey size={15} />执行成功后将立即撤销该用户所有登录会话，过程保留完整审计记录。</p>
            <span><AppButton type="button" disabled={lifecycleBusy} onClick={() => setDeactivationPreview(null)}>取消</AppButton>{deactivationBatch?.status === 'TRANSFER_FAILED' ? <AppButton variant="primary" type="button" disabled={lifecycleBusy} onClick={() => void retryDeactivation()}>{lifecycleBusy ? <SpinnerGap className="spin" size={16} /> : <ArrowsClockwise size={16} />}重试转交</AppButton> : <AppButton variant="primary" type="button" disabled={lifecycleBusy || !deactivationPreview.canDisable} onClick={() => void executeDeactivation()}>{lifecycleBusy ? <SpinnerGap className="spin" size={16} /> : <ShieldCheck size={16} />}{lifecycleBusy ? '正在安全停用…' : '确认转交并停用'}</AppButton>}</span>
          </footer>
        </section>
      </div>}
    </section>
  </AppShell>
}
