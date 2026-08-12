import {
  ArrowsClockwise,
  ChartLineUp,
  Check,
  CheckCircle,
  ClipboardText,
  Crown,
  GlobeHemisphereWest,
  ListChecks,
  LockKey,
  Lifebuoy,
  Plus,
  Pulse,
  Scroll,
  ShieldCheck,
  SpinnerGap,
  Timer,
  GearSix,
  Gauge,
  UsersThree,
  X,
} from '@phosphor-icons/react'
import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { NavLink, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import {
  administrationAPI,
  type AdminUser,
  type BusinessDomain,
  type PlatformApproval,
  type PlatformAuditLog,
} from '../lib/administration'
import { currentSubject } from '../lib/auth'
import {
  backgroundTaskAPI,
  type BackgroundTask,
} from '../lib/background-tasks'
import { notifyDomainCatalogChanged } from '../lib/domain-context'
import { supportAPI, type SupportTicket } from '../lib/support'
import {
  operationalObservabilityAPI,
  type OperationalSnapshot,
  type OperationalWindow,
} from '../lib/operational-observability'

type ConfigurationView = 'domains' | 'permissions' | 'approvals' | 'tasks' | 'logs' | 'support' | 'observability'
type PermissionView = 'platform' | 'domains' | 'users'
type DialogState =
  | { kind: 'platform-administrators' }
  | { kind: 'create-domain' }
  | { kind: 'domain-administrator'; user?: AdminUser }
  | { kind: 'user-domains'; user: AdminUser }
  | { kind: 'support-transition'; ticket: SupportTicket; status: 'RESOLVED' | 'CLOSED' }
  | { kind: 'approval-rejection'; approval: PlatformApproval }
  | null

const fixedCapabilities = {
  platform: ['管理全平台配置', '进入任意活动领域', '使用全部业务功能'],
  domain: ['管理领域数据配置', '审批数据源与数据集发布', '审批用户加入领域'],
  user: ['配置数据源与数据集', '查看领域内数据资产', '提交配置等待领域发布'],
  account: ['查看注册账号', '停用或恢复账号', '停用时即时撤销会话'],
}

/** 按平台、领域、用户三级固定边界管理身份与归属。 */
export function ManagementCenterPage() {
  const { section } = useParams<{ section: string }>()
  const view: ConfigurationView = section === 'permissions' || section === 'approvals' || section === 'tasks' || section === 'logs' || section === 'support' || section === 'observability'
    ? section
    : 'domains'
  const [domains, setDomains] = useState<BusinessDomain[]>([])
  const [users, setUsers] = useState<AdminUser[]>([])
  const [approvals, setApprovals] = useState<PlatformApproval[]>([])
  const [tasks, setTasks] = useState<BackgroundTask[]>([])
  const [auditLogs, setAuditLogs] = useState<PlatformAuditLog[]>([])
  const [supportTickets, setSupportTickets] = useState<SupportTicket[]>([])
  const [observability, setObservability] = useState<OperationalSnapshot | null>(null)
  const [operationalWindow, setOperationalWindow] = useState<OperationalWindow>('24h')
  const [permissionView, setPermissionView] = useState<PermissionView>('platform')
  const [dialog, setDialog] = useState<DialogState>(null)
  const [loading, setLoading] = useState(true)
  const [busyKey, setBusyKey] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const signedInUserID = currentSubject()

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [nextDomains, nextUsers, nextApprovals, nextTasks, nextAuditLogs, nextSupportTickets, nextObservability] = await Promise.all([
        administrationAPI.listManagedDomains(),
        administrationAPI.listUsers(),
        administrationAPI.listPlatformApprovals(),
        backgroundTaskAPI.list('ALL', 100),
        administrationAPI.listPlatformAuditLogs(),
        supportAPI.list('queue'),
        operationalObservabilityAPI.snapshot(operationalWindow),
      ])
      setDomains(nextDomains)
      setUsers(nextUsers)
      setApprovals(nextApprovals)
      setTasks(nextTasks.items)
      setAuditLogs(nextAuditLogs)
      setSupportTickets(nextSupportTickets)
      setObservability(nextObservability)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '平台管理数据加载失败')
    } finally {
      setLoading(false)
    }
  }, [operationalWindow])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() }, 0)
    return () => window.clearTimeout(timer)
  }, [load])

  const platformAdministrators = users.filter(user => user.platformAdministrator)
  const domainAdministratorCount = useMemo(
    () => new Set(domains.flatMap(domain => domain.administrators.map(item => item.id))).size,
    [domains],
  )
  const pendingApprovalCount = approvals.filter(item => item.status === 'PENDING').length
  const activeTaskCount = tasks.filter(item => item.status === 'QUEUED' || item.status === 'RUNNING').length
  const failedTaskCount = tasks.filter(item => item.status === 'FAILED' || item.status === 'PARTIAL').length

  const setPlatformAdministrator = async (user: AdminUser) => {
    const enabled = !user.platformAdministrator
    setBusyKey(`platform:${user.id}`)
    setError('')
    setNotice('')
    try {
      await administrationAPI.setPlatformAdministrator(user.id, enabled)
      setUsers(current => current.map(item => item.id === user.id
        ? { ...item, platformAdministrator: enabled, domains: enabled ? [] : item.domains }
        : item))
      setNotice(`${user.displayName}已${enabled ? '设为' : '移出'}平台管理员`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '平台身份更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const updateDomainStatus = async (domain: BusinessDomain) => {
    const status = domain.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    setBusyKey(`domain-status:${domain.id}`)
    setError('')
    setNotice('')
    try {
      const updated = await administrationAPI.updateDomainStatus(domain.id, status)
      setDomains(current => current.map(item => item.id === domain.id
        ? { ...item, ...updated, administrators: item.administrators }
        : item))
      notifyDomainCatalogChanged()
      setNotice(`领域“${domain.name}”已${status === 'ACTIVE' ? '启用' : '停用'}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '领域状态更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const updateUserStatus = async (user: AdminUser) => {
    const status = user.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    setBusyKey(`user-status:${user.id}`)
    setError('')
    setNotice('')
    try {
      await administrationAPI.updateUserStatus(user.id, status)
      await load()
      setNotice(`${user.displayName}的账号已${status === 'ACTIVE' ? '恢复' : '停用'}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '用户状态更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const reviewApproval = async (approval: PlatformApproval, decision: 'APPROVED' | 'REJECTED', reason = '') => {
    setBusyKey(`approval:${approval.id}`)
    setError('')
    setNotice('')
    try {
      await administrationAPI.reviewPublication(approval, decision, reason)
      await load()
      setDialog(null)
      if (approval.kind === 'DOMAIN_ACCESS') notifyDomainCatalogChanged()
      const target = approval.kind === 'DOMAIN_ACCESS'
        ? `“${approval.requesterDisplayName}”的领域申请`
        : `“${approval.resourceName}”的发布申请`
      setNotice(`${target}已${decision === 'APPROVED' ? '通过' : '拒绝'}`)
    } catch (cause) {
      await load()
      setError(cause instanceof Error ? cause.message : '审批处理失败')
    } finally {
      setBusyKey('')
    }
  }

  const operateTask = async (task: BackgroundTask, operation: 'cancel' | 'retry') => {
    setBusyKey(`task:${operation}:${task.kind}:${task.id}`)
    setError('')
    setNotice('')
    try {
      if (operation === 'cancel') await backgroundTaskAPI.cancel(task)
      else await backgroundTaskAPI.retry(task)
      await load()
      setNotice(`后台任务“${task.name}”已${operation === 'cancel' ? '中止' : '重新入队'}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '后台任务操作失败')
    } finally {
      setBusyKey('')
    }
  }

  const transitionSupportTicket = async (ticket: SupportTicket, status: 'IN_PROGRESS' | 'RESOLVED' | 'CLOSED', resolutionNote = '') => {
    setBusyKey(`support:${ticket.id}:${status}`)
    setError('')
    setNotice('')
    try {
      const updated = await supportAPI.transition(ticket.id, status, resolutionNote, ticket.recordVersion)
      setSupportTickets(current => current.map(item => item.id === updated.id ? updated : item))
      setDialog(null)
      setNotice(`支持工单“${ticket.subject}”已更新为${status === 'IN_PROGRESS' ? '处理中' : status === 'RESOLVED' ? '已解决' : '已关闭'}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '支持工单状态更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const submitSupportTransition = (event: FormEvent<HTMLFormElement>, ticket: SupportTicket, status: 'RESOLVED' | 'CLOSED') => {
    event.preventDefault()
    const note = String(new FormData(event.currentTarget).get('resolutionNote') ?? '').trim()
    if (note.length < 4) {
      setError('请填写至少 4 个字的处理结果')
      return
    }
    void transitionSupportTicket(ticket, status, note)
  }

  const createDomain = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const input = {
      code: String(form.get('code') ?? '').trim().toLowerCase(),
      name: String(form.get('name') ?? '').trim(),
      description: String(form.get('description') ?? '').trim(),
    }
    if (!input.code || !input.name) {
      setError('请填写领域名称和编码')
      return
    }
    setBusyKey('create-domain')
    setError('')
    try {
      await administrationAPI.createDomain(input)
      await load()
      notifyDomainCatalogChanged()
      setDialog(null)
      setNotice(`领域“${input.name}”已创建`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '创建领域失败')
    } finally {
      setBusyKey('')
    }
  }

  const addPlatformAdministrators = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const selectedIDs = new Set(new FormData(event.currentTarget).getAll('userIds').map(String))
    if (selectedIDs.size === 0) {
      setError('请至少选择一位用户')
      return
    }
    const additions = users.filter(user => selectedIDs.has(user.id) && !user.platformAdministrator)
    setBusyKey('platform-administrators')
    setError('')
    try {
      for (const user of additions) {
        await administrationAPI.setPlatformAdministrator(user.id, true)
      }
      await load()
      setDialog(null)
      setNotice(`已新增 ${additions.length} 位平台管理员`)
    } catch (cause) {
      await load()
      setError(cause instanceof Error ? cause.message : '平台管理员更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const saveDomainAdministrator = async (
    event: FormEvent<HTMLFormElement>,
    currentUser?: AdminUser,
  ) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const userID = currentUser?.id || String(form.get('userId') ?? '')
    const selectedDomainIDs = new Set(form.getAll('domainIds').map(String))
    if (!userID || selectedDomainIDs.size === 0) {
      setError('请选择用户并至少分配一个领域')
      return
    }
    const existingDomainIDs = new Set(currentUser?.domains
      .filter(domain => domain.memberRole === 'DOMAIN_ADMIN')
      .map(domain => domain.id) ?? [])
    const additions = domains.filter(domain => selectedDomainIDs.has(domain.id) && !existingDomainIDs.has(domain.id))
    const removals = domains.filter(domain => existingDomainIDs.has(domain.id) && !selectedDomainIDs.has(domain.id))
    const unsafeRemoval = removals.find(domain => domain.administrators.length <= 1)
    if (unsafeRemoval) {
      setError(`领域“${unsafeRemoval.name}”至少需要保留一位管理员，请先新增其他管理员`)
      return
    }
    setBusyKey(`domain-administrator:${userID}`)
    setError('')
    try {
      for (const domain of additions) {
        await administrationAPI.replaceDomainAdministrators(domain.id, [
          ...domain.administrators.map(item => item.id),
          userID,
        ])
      }
      for (const domain of removals) {
        await administrationAPI.replaceDomainAdministrators(
          domain.id,
          domain.administrators.filter(item => item.id !== userID).map(item => item.id),
        )
      }
      await load()
      notifyDomainCatalogChanged()
      setDialog(null)
      setNotice(`${currentUser?.displayName || '领域管理员'}的管理领域已更新`)
    } catch (cause) {
      await load()
      setError(cause instanceof Error ? cause.message : '领域管理员配置失败')
    } finally {
      setBusyKey('')
    }
  }

  const removeDomainAdministrator = async (user: AdminUser) => {
    const managedDomains = domains.filter(domain => domain.administrators.some(item => item.id === user.id))
    const unsafeRemoval = managedDomains.find(domain => domain.administrators.length <= 1)
    if (unsafeRemoval) {
      setError(`领域“${unsafeRemoval.name}”至少需要保留一位管理员，请先新增其他管理员`)
      return
    }
    setBusyKey(`domain-administrator:${user.id}`)
    setError('')
    setNotice('')
    try {
      for (const domain of managedDomains) {
        await administrationAPI.replaceDomainAdministrators(
          domain.id,
          domain.administrators.filter(item => item.id !== user.id).map(item => item.id),
        )
      }
      await load()
      notifyDomainCatalogChanged()
      setNotice(`${user.displayName}已移出领域管理员`)
    } catch (cause) {
      await load()
      setError(cause instanceof Error ? cause.message : '移除领域管理员失败')
    } finally {
      setBusyKey('')
    }
  }

  const saveUserDomains = async (
    event: FormEvent<HTMLFormElement>,
    user: AdminUser,
  ) => {
    event.preventDefault()
    const selectedIDs = new Set(new FormData(event.currentTarget).getAll('domainIds').map(String))
    const currentIDs = new Set(user.domains.map(domain => domain.id))
    const additions = [...selectedIDs].filter(id => !currentIDs.has(id))
    const removals = [...currentIDs].filter(id => !selectedIDs.has(id))
    setBusyKey(`user-domains:${user.id}`)
    setError('')
    try {
      for (const domainID of additions) {
        await administrationAPI.assignUserDomain(user.id, domainID)
      }
      for (const domainID of removals) {
        await administrationAPI.revokeUserDomain(user.id, domainID)
      }
      await load()
      notifyDomainCatalogChanged()
      setDialog(null)
      setNotice(`${user.displayName}的所属领域已更新`)
    } catch (cause) {
      await load()
      setError(cause instanceof Error ? cause.message : '用户领域归属更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const closeDialog = () => {
    if (busyKey) return
    setDialog(null)
    setError('')
  }

  return (
    <AppShell
      title="平台管理中心"
      eyebrow="平台控制面"
      actions={view === 'domains' || view === 'permissions'
        ? undefined
        : <button className="primary-button" type="button" disabled={loading} onClick={() => void load()}>
          <ArrowsClockwise className={loading ? 'spin' : ''} size={17} />刷新运行状态
        </button>}
      className="administration-shell"
    >
      <section className="administration-stack platform-page-stack">
        <div className="platform-management-intro">
          <div><span className="eyebrow">PLATFORM CONTROL PLANE</span><h2>平台治理与运行控制面</h2><p>平台管理员不占用领域身份，但可进入任意领域并使用全部功能。</p></div>
          <div className="administration-metrics platform-management-metrics" aria-label="平台运行概览">
          <article><GlobeHemisphereWest size={20} weight="duotone" /><span>业务领域</span><strong>{domains.filter(item => item.status === 'ACTIVE').length}</strong><small>{domainAdministratorCount} 位领域管理员</small></article>
          <article><UsersThree size={20} weight="duotone" /><span>活跃用户</span><strong>{users.filter(item => item.status === 'ACTIVE').length}</strong><small>{platformAdministrators.length} 位平台管理员</small></article>
          <article className={pendingApprovalCount > 0 ? 'attention' : ''}><ClipboardText size={20} weight="duotone" /><span>待处理审批</span><strong>{pendingApprovalCount}</strong><small>领域准入与资产发布</small></article>
          <article className={failedTaskCount > 0 ? 'warning' : ''}><Pulse size={20} weight="duotone" /><span>运行中任务</span><strong>{activeTaskCount}</strong><small>{failedTaskCount} 个异常或部分完成</small></article>
          </div>
        </div>

        <nav className="platform-top-navigation" aria-label="平台管理模块">
          <NavLink to="/platform-management/domains"><GlobeHemisphereWest size={18} /><span><strong>领域管理</strong><small>新建与停用</small></span></NavLink>
          <NavLink to="/platform-management/permissions"><ShieldCheck size={18} /><span><strong>权限管理</strong><small>管理员与用户</small></span></NavLink>
          <NavLink to="/platform-management/approvals"><ListChecks size={18} /><span><strong>审批中心</strong><small>{pendingApprovalCount} 项待处理</small></span></NavLink>
          <NavLink to="/platform-management/tasks"><Pulse size={18} /><span><strong>后台任务</strong><small>{activeTaskCount} 项运行中</small></span></NavLink>
          <NavLink to="/platform-management/observability"><ChartLineUp size={18} /><span><strong>运行观测</strong><small>{observability?.health === 'CRITICAL' ? '存在异常' : observability?.health === 'ATTENTION' ? '需要关注' : '运行健康'}</small></span></NavLink>
          <NavLink to="/platform-management/support"><Lifebuoy size={18} /><span><strong>支持工单</strong><small>{supportTickets.filter(item => item.status === 'OPEN' || item.status === 'IN_PROGRESS').length} 项待跟进</small></span></NavLink>
          <NavLink to="/platform-management/logs"><Scroll size={18} /><span><strong>平台日志</strong><small>不可变轨迹</small></span></NavLink>
          <NavLink to="/platform-management/runtime-config"><GearSix size={18} /><span><strong>运行配置</strong><small>版本与回滚</small></span></NavLink>
        </nav>

        {(error || notice) && <div className={`administration-feedback ${error ? 'error' : 'success'}`} role={error ? 'alert' : 'status'}>
          {error || notice}
          <button type="button" aria-label="关闭提示" onClick={() => { setError(''); setNotice('') }}><X size={15} /></button>
        </div>}

        <div className="platform-module-page">
          <section className="administration-panel platform-module-panel">
            {loading
              ? <div className="administration-empty" role="status"><SpinnerGap className="spin" size={32} /><strong>正在同步平台运行状态…</strong></div>
              : error && users.length === 0
                ? <div className="administration-empty"><LockKey size={34} /><strong>无法进入平台管理中心</strong><p>该页面仅对平台管理员开放。</p><button className="quiet-button" type="button" onClick={() => void load()}>重新加载</button></div>
                : view === 'domains'
                  ? <DomainGovernance domains={domains} busyKey={busyKey} onCreate={() => setDialog({ kind: 'create-domain' })} onStatus={domain => void updateDomainStatus(domain)} />
                  : view === 'permissions'
                    ? <PermissionGovernance
                      domains={domains}
                      users={users}
                      busyKey={busyKey}
                      signedInUserID={signedInUserID}
                      permissionView={permissionView}
                      onPermissionViewChange={setPermissionView}
                      onAddPlatform={() => setDialog({ kind: 'platform-administrators' })}
                      onRemovePlatform={user => void setPlatformAdministrator(user)}
                      onAddDomain={() => setDialog({ kind: 'domain-administrator' })}
                      onManageDomain={user => setDialog({ kind: 'domain-administrator', user })}
                      onRemoveDomain={user => void removeDomainAdministrator(user)}
                      onManageUserDomains={user => setDialog({ kind: 'user-domains', user })}
                      onStatus={user => void updateUserStatus(user)}
                    />
                    : view === 'approvals'
                      ? <ApprovalCenter approvals={approvals} busyKey={busyKey} onDecision={(approval, decision) => decision === 'REJECTED' ? setDialog({ kind: 'approval-rejection', approval }) : void reviewApproval(approval, decision)} />
                      : view === 'tasks'
                        ? <BackgroundTaskCenter tasks={tasks} busyKey={busyKey} onOperate={(task, operation) => void operateTask(task, operation)} />
                        : view === 'observability'
                          ? observability && <OperationalObservabilityCenter snapshot={observability} window={operationalWindow} onWindowChange={setOperationalWindow} />
                        : view === 'support'
                          ? <SupportTicketCenter
                            tickets={supportTickets}
                            busyKey={busyKey}
                            onStart={ticket => void transitionSupportTicket(ticket, 'IN_PROGRESS')}
                            onReopen={ticket => void transitionSupportTicket(ticket, 'IN_PROGRESS')}
                            onResolve={ticket => setDialog({ kind: 'support-transition', ticket, status: 'RESOLVED' })}
                            onClose={ticket => setDialog({ kind: 'support-transition', ticket, status: 'CLOSED' })}
                          />
                          : <PlatformLogCenter logs={auditLogs} />}
          </section>
        </div>
      </section>

      {dialog?.kind === 'create-domain' && <DomainDialog
        title="新建业务领域"
        description="领域创建后自动启用；管理员请在权限管理中单独配置。"
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={createDomain}
      />}
      {dialog?.kind === 'platform-administrators' && <PlatformAdministratorDialog
        users={users}
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={event => void addPlatformAdministrators(event)}
      />}
      {dialog?.kind === 'domain-administrator' && <DomainAdministratorDialog
        user={dialog.user}
        domains={domains}
        users={users}
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={event => void saveDomainAdministrator(event, dialog.user)}
      />}
      {dialog?.kind === 'user-domains' && <UserDomainDialog
        user={dialog.user}
        domains={domains}
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={event => void saveUserDomains(event, dialog.user)}
      />}
      {dialog?.kind === 'support-transition' && <SupportTransitionDialog
        ticket={dialog.ticket}
        status={dialog.status}
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={event => submitSupportTransition(event, dialog.ticket, dialog.status)}
      />}
      {dialog?.kind === 'approval-rejection' && <ApprovalRejectionDialog
        approval={dialog.approval}
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={reason => void reviewApproval(dialog.approval, 'REJECTED', reason)}
      />}
    </AppShell>
  )
}

const operationalHealthLabel = { HEALTHY: '运行健康', ATTENTION: '需要关注', CRITICAL: '存在异常' } as const
const queueHealthLabel = { HEALTHY: '正常', ATTENTION: '有积压', CRITICAL: '需处理' } as const
const purposeLabel: Record<string, string> = {
  METADATA_COMPLETION: '元数据补全', REPORT_GENERATION: '报告生成', BLOCK_EDIT: '内容块编辑',
  CONCLUSION_GENERATION: '结论生成', DATA_SOURCE_CONFIG: '数据源配置', ASKDATA_NARRATIVE: '问数解读',
}

function compactNumber(value: number) {
  return new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}

function formatDuration(value: number) {
  if (value < 1000) return `${value} ms`
  return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)} 秒`
}

function formatAge(value: number) {
  if (value <= 0) return '无等待'
  if (value < 60) return `${value} 秒`
  if (value < 3600) return `${Math.ceil(value / 60)} 分钟`
  return `${(value / 3600).toFixed(1)} 小时`
}

function QuotaMeter({ label, used, limit, utilization, unit = '' }: { label: string; used: number; limit: number; utilization: number; unit?: string }) {
  const level = utilization >= 90 ? 'critical' : utilization >= 75 ? 'attention' : 'healthy'
  return <article className={`operational-quota is-${level}`}>
    <header><span>{label}</span><strong>{utilization.toFixed(1)}%</strong></header>
    <div aria-label={`${label}已使用 ${utilization.toFixed(1)}%`}><i style={{ width: `${Math.min(utilization, 100)}%` }} /></div>
    <footer><span>{compactNumber(used)}{unit}</span><small>额度 {compactNumber(limit)}{unit}</small></footer>
  </article>
}

function OperationalObservabilityCenter({ snapshot, window, onWindowChange }: {
  snapshot: OperationalSnapshot
  window: OperationalWindow
  onWindowChange: (value: OperationalWindow) => void
}) {
  const ai = snapshot.ai
  const askData = snapshot.askData
  return <div className="operational-center">
    <header className="platform-section-heading operational-heading">
      <div><span className="eyebrow">OPERATIONS & QUOTA</span><h3>运行健康与资源用量</h3><p>聚合展示问数链路、AI 配额和异步队列；仅保留计数与稳定错误码，不展示业务数据。</p></div>
      <div className="operational-heading-actions">
        <span className={`operational-health is-${snapshot.health.toLowerCase()}`}><Pulse size={16} weight="fill" />{operationalHealthLabel[snapshot.health]}</span>
        <label><span>观察窗口</span><select value={window} onChange={event => onWindowChange(event.target.value as OperationalWindow)}><option value="1h">最近 1 小时</option><option value="6h">最近 6 小时</option><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option></select></label>
      </div>
    </header>

    <section className="operational-summary" aria-label="运行概览">
      <article><Gauge size={22} weight="duotone" /><span>AI 请求成功率</span><strong>{ai.successRate.toFixed(1)}%</strong><small>{ai.succeededInWindow} 成功 · {ai.failedInWindow} 失败</small></article>
      <article className={askData.blockedInWindow > 0 ? 'attention' : ''}><ChartLineUp size={22} weight="duotone" /><span>问数回答率</span><strong>{askData.answerRate.toFixed(1)}%</strong><small>{askData.answeredInWindow} 已回答 · {askData.blockedInWindow} 阻断</small></article>
      <article><Timer size={22} weight="duotone" /><span>问数 P95 耗时</span><strong>{formatDuration(askData.p95DurationMs)}</strong><small>平均 {formatDuration(askData.averageDurationMs)}</small></article>
      <article className={snapshot.queues.some(item => item.status !== 'HEALTHY') ? 'attention' : ''}><Pulse size={22} weight="duotone" /><span>异步队列</span><strong>{snapshot.queues.filter(item => item.status === 'HEALTHY').length}/{snapshot.queues.length}</strong><small>{snapshot.queues.reduce((sum, item) => sum + item.pending + item.running, 0)} 项处理中</small></article>
    </section>

    <div className="operational-grid">
      <section className="operational-card operational-quota-card"><header><div><strong>AI 配额水位</strong><span>{ai.enabled ? '租户 AI 能力已启用' : '租户 AI 能力当前停用'}</span></div><small>月度口径按保守计费量统计</small></header><div>
        <QuotaMeter label="今日请求" used={ai.requestsToday} limit={ai.requestsDailyLimit} utilization={ai.requestUtilization} unit=" 次" />
        <QuotaMeter label="本月 Token" used={ai.tokensThisMonth} limit={ai.tokensMonthlyLimit} utilization={ai.tokenUtilization} />
        <QuotaMeter label="本月成本" used={ai.costMicrosThisMonth / 1_000_000} limit={ai.costMicrosMonthlyLimit / 1_000_000} utilization={ai.costUtilization} unit=" 元" />
      </div></section>

      <section className="operational-card operational-latency-card"><header><div><strong>链路响应</strong><span>判断体验退化与超时风险</span></div><small>最近 {window}</small></header><div>
        <article><span>AI 平均延迟</span><strong>{formatDuration(ai.averageLatencyMs)}</strong><small>P95 {formatDuration(ai.p95LatencyMs)}</small></article>
        <article><span>问数平均耗时</span><strong>{formatDuration(askData.averageDurationMs)}</strong><small>P95 {formatDuration(askData.p95DurationMs)}</small></article>
        <article><span>问数运行中</span><strong>{askData.activeInWindow}</strong><small>{askData.clarificationInWindow} 项等待补充</small></article>
      </div></section>
    </div>

    <section className="operational-card operational-queue-card"><header><div><strong>异步处理队列</strong><span>发现积压、失败和卡死任务</span></div><small>超过 15 分钟自动标记异常</small></header><div className="operational-queue-list">
      {snapshot.queues.map(queue => <article key={queue.code}><span className={`queue-signal is-${queue.status.toLowerCase()}`}><i />{queueHealthLabel[queue.status]}</span><div><strong>{queue.name}</strong><small>{queue.code}</small></div><dl><div><dt>待处理</dt><dd>{queue.pending}</dd></div><div><dt>运行中</dt><dd>{queue.running}</dd></div><div><dt>失败</dt><dd>{queue.failed}</dd></div><div><dt>最长等待</dt><dd>{formatAge(queue.oldestPendingSeconds)}</dd></div></dl></article>)}
    </div></section>

    <div className="operational-grid operational-bottom-grid">
      <section className="operational-card"><header><div><strong>AI 用途分布</strong><span>按业务用途核对资源消耗</span></div></header><div className="operational-purpose-list">
        {snapshot.purposes.length ? snapshot.purposes.map(item => <article key={item.purpose}><div><strong>{purposeLabel[item.purpose] ?? item.purpose}</strong><small>{item.count} 次请求</small></div><span>{compactNumber(item.tokens)} Token</span></article>) : <p className="operational-empty">当前窗口内没有 AI 请求</p>}
      </div></section>
      <section className="operational-card"><header><div><strong>失败原因</strong><span>稳定错误码可直接用于定位与告警</span></div></header><div className="operational-failure-list">
        {snapshot.failureCodes.length ? snapshot.failureCodes.map(item => <article key={`${item.source}:${item.code}`}><span>{item.source === 'ASK_DATA' ? '问数' : 'AI'}</span><code>{item.code}</code><strong>{item.count}</strong></article>) : <p className="operational-empty is-healthy"><CheckCircle size={20} />当前窗口内没有记录到失败</p>}
      </div></section>
    </div>
  </div>
}

function FixedScope({ level, title, description, capabilities }: {
  level: string
  title: string
  description: string
  capabilities: string[]
}) {
  return <section className="fixed-scope-card">
    <div><span className="eyebrow">{level}</span><h3>{title}<small>固定</small></h3><p>{description}</p></div>
    <ul>{capabilities.map(capability => <li key={capability}><CheckCircle size={15} weight="fill" />{capability}</li>)}</ul>
  </section>
}

function AddManagementSlot({ title, description, onClick }: {
  title: string
  description: string
  onClick: () => void
}) {
  return <button className="administration-add-slot" type="button" title={title} onClick={onClick}>
    <span><Plus size={19} weight="bold" /></span>
    <strong>{title}</strong>
    <small>{description}</small>
  </button>
}

function PermissionGovernance({
  domains,
  users,
  busyKey,
  signedInUserID,
  permissionView,
  onPermissionViewChange,
  onAddPlatform,
  onRemovePlatform,
  onAddDomain,
  onManageDomain,
  onRemoveDomain,
  onManageUserDomains,
  onStatus,
}: {
  domains: BusinessDomain[]
  users: AdminUser[]
  busyKey: string
  signedInUserID: string
  permissionView: PermissionView
  onPermissionViewChange: (view: PermissionView) => void
  onAddPlatform: () => void
  onRemovePlatform: (user: AdminUser) => void
  onAddDomain: () => void
  onManageDomain: (user: AdminUser) => void
  onRemoveDomain: (user: AdminUser) => void
  onManageUserDomains: (user: AdminUser) => void
  onStatus: (user: AdminUser) => void
}) {
  const platformAdministrators = users.filter(user => user.platformAdministrator)
  const domainAdministrators = users.filter(user => user.domains.some(domain => domain.memberRole === 'DOMAIN_ADMIN'))
  const ordinaryUsers = users.filter(user => !user.platformAdministrator && !user.domains.some(domain => domain.memberRole === 'DOMAIN_ADMIN'))
  return <div className="administration-view permission-management-view">
    <header className="administration-view-heading platform-section-heading">
      <div><span className="eyebrow">SERVICE ADMINISTRATION</span><h2>权限管理</h2><p>在这里统一维护平台管理员、领域管理员和普通用户。</p></div>
      <div className="permission-view-switch" role="tablist" aria-label="权限管理对象">
        <button type="button" role="tab" aria-selected={permissionView === 'platform'} className={permissionView === 'platform' ? 'active' : ''} onClick={() => onPermissionViewChange('platform')}>平台管理员</button>
        <button type="button" role="tab" aria-selected={permissionView === 'domains'} className={permissionView === 'domains' ? 'active' : ''} onClick={() => onPermissionViewChange('domains')}>领域管理员</button>
        <button type="button" role="tab" aria-selected={permissionView === 'users'} className={permissionView === 'users' ? 'active' : ''} onClick={() => onPermissionViewChange('users')}>普通用户</button>
      </div>
    </header>

    {permissionView === 'platform' && <section className="permission-management-section permission-switch-panel">
      <FixedScope level="PLATFORM" title="平台管理员" description="拥有全平台最高权限，不保存领域归属；可在列表中新增或移除平台管理员。" capabilities={fixedCapabilities.platform} />
      <div className="governance-user-list">
        <AddManagementSlot title="新增平台管理员" description="从已注册且未担任其他管理员的用户中选择" onClick={onAddPlatform} />
        {platformAdministrators.map(user => <article key={user.id}>
            <UserIdentity user={user} />
            <span className="identity-badge platform"><Crown size={13} weight="fill" />平台管理员</span>
            <button
              className="quiet-button danger-text"
              type="button"
              title={platformAdministrators.length <= 1 ? '平台至少需要保留一位管理员' : undefined}
              disabled={Boolean(busyKey) || platformAdministrators.length <= 1}
              onClick={() => onRemovePlatform(user)}
            >
              {busyKey === `platform:${user.id}` && <SpinnerGap className="spin" size={14} />}
              移除
            </button>
          </article>)}
      </div>
    </section>}

    {permissionView === 'domains' && <section className="permission-management-section permission-switch-panel">
      <FixedScope level="DOMAIN" title="领域管理员" description="领域管理员只管理被分配的领域；可新增管理员、调整管理领域或移除身份。" capabilities={fixedCapabilities.domain} />
      <div className="governance-user-list domain-administrator-list">
        <AddManagementSlot title="新增领域管理员" description="选择用户并分配一个或多个管理领域" onClick={onAddDomain} />
        {domainAdministrators.map(user => {
          const managedDomains = domains.filter(domain => domain.administrators.some(item => item.id === user.id))
          const removalBlocked = managedDomains.some(domain => domain.administrators.length <= 1)
          return <article key={user.id}>
          <UserIdentity user={user} />
          <div className="user-domain-list">
            {managedDomains.map(domain => <span className="administrator" key={domain.id}><Crown size={11} weight="fill" />{domain.name}</span>)}
          </div>
          <div className="domain-administrator-actions">
            <button className="quiet-button" type="button" disabled={Boolean(busyKey)} onClick={() => onManageDomain(user)}>管理领域</button>
            <button className="quiet-button danger-text" type="button" title={removalBlocked ? '请先为相关领域新增其他管理员' : undefined} disabled={Boolean(busyKey) || removalBlocked} onClick={() => onRemoveDomain(user)}>
              {busyKey === `domain-administrator:${user.id}` && <SpinnerGap className="spin" size={14} />}移除
            </button>
          </div>
        </article>})}
      </div>
    </section>}

    {permissionView === 'users' && <section className="permission-management-section permission-switch-panel embedded-user-management">
      <UserManagement users={ordinaryUsers} busyKey={busyKey} signedInUserID={signedInUserID} onEditDomains={onManageUserDomains} onStatus={onStatus} />
    </section>}
  </div>
}

function DomainGovernance({ domains, busyKey, onCreate, onStatus }: {
  domains: BusinessDomain[]
  busyKey: string
  onCreate: () => void
  onStatus: (domain: BusinessDomain) => void
}) {
  return <div className="administration-view">
    <header className="administration-view-heading"><div><span className="eyebrow">DOMAIN LIFECYCLE</span><h2>领域管理</h2><p>这里只负责领域的新建、启用和停用；管理员请前往权限管理配置。</p></div><small>{domains.length} 个领域</small></header>
    <div className="domain-governance-list">
      <AddManagementSlot title="新建领域" description="创建新的业务数据边界" onClick={onCreate} />
      {domains.map(domain => <article key={domain.id}>
          <div className="domain-management-avatar">{domain.name.slice(0, 1)}</div>
          <div className="domain-governance-name"><strong>{domain.name}</strong><small>{domain.code}{domain.default ? ' · 默认领域' : ''}</small></div>
          <div className="domain-governance-description"><small>领域说明</small><strong>{domain.description || '暂无说明'}</strong></div>
          <span className={`domain-status ${domain.status.toLowerCase()}`}>{domain.status === 'ACTIVE' ? '已启用' : '已停用'}</span>
          <div className="domain-governance-actions">
            <button className="quiet-button" type="button" disabled={Boolean(busyKey) || domain.default} onClick={() => onStatus(domain)}>
              {busyKey === `domain-status:${domain.id}` && <SpinnerGap className="spin" size={13} />}
              {domain.status === 'ACTIVE' ? '停用' : '启用'}
            </button>
          </div>
        </article>
      )}
    </div>
  </div>
}

const approvalKindLabels: Record<PlatformApproval['kind'], string> = {
  DOMAIN_ACCESS: '领域准入',
  DATA_SOURCE: '数据源发布',
  DATASET: '数据集发布',
}

const approvalStatusLabels: Record<PlatformApproval['status'], string> = {
  PENDING: '待处理',
  APPROVED: '已通过',
  REJECTED: '已拒绝',
  WITHDRAWN: '已撤回',
  CANCELLED: '已取消',
}

const formatPlatformTime = (value?: string) => {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(date)
}

function ApprovalCenter({ approvals, busyKey, onDecision }: {
  approvals: PlatformApproval[]
  busyKey: string
  onDecision: (approval: PlatformApproval, decision: 'APPROVED' | 'REJECTED') => void
}) {
  const [filter, setFilter] = useState<'PENDING' | 'ALL'>('PENDING')
  const visible = approvals.filter(item => filter === 'ALL' || item.status === 'PENDING')
  const counts = {
    domain: approvals.filter(item => item.status === 'PENDING' && item.kind === 'DOMAIN_ACCESS').length,
    source: approvals.filter(item => item.status === 'PENDING' && item.kind === 'DATA_SOURCE').length,
    dataset: approvals.filter(item => item.status === 'PENDING' && item.kind === 'DATASET').length,
  }
  return <div className="administration-view platform-approval-view">
    <header className="administration-view-heading platform-section-heading">
      <div><span className="eyebrow">APPROVAL CENTER</span><h2>审批中心</h2><p>统一查看并处理领域准入和资产发布队列；平台管理员与所属领域管理员均可审批发布申请。</p></div>
      <div className="platform-view-switch" aria-label="审批筛选">
        <button className={filter === 'PENDING' ? 'active' : ''} type="button" onClick={() => setFilter('PENDING')}>待处理</button>
        <button className={filter === 'ALL' ? 'active' : ''} type="button" onClick={() => setFilter('ALL')}>全部记录</button>
      </div>
    </header>
    <div className="approval-type-summary">
      <article><UsersThree size={18} /><span>领域准入</span><strong>{counts.domain}</strong></article>
      <article><ClipboardText size={18} /><span>数据源发布</span><strong>{counts.source}</strong></article>
      <article><ListChecks size={18} /><span>数据集发布</span><strong>{counts.dataset}</strong></article>
    </div>
    {visible.length === 0
      ? <div className="platform-module-empty"><CheckCircle size={30} weight="duotone" /><strong>当前没有待处理审批</strong><span>新的申请会自动汇总到这里。</span></div>
      : <div className="platform-approval-list">
        {visible.map(item => <article key={`${item.kind}:${item.id}`}>
          <span className={`approval-kind ${item.kind.toLowerCase()}`}>{approvalKindLabels[item.kind]}</span>
          <div className="approval-resource"><strong>{item.resourceName}</strong><small>{item.domainName} · {item.domainCode}</small></div>
          <div className="approval-requester"><strong>{item.requesterDisplayName}</strong><small>{item.requesterEmail}</small></div>
          <div className="approval-note"><span>{item.note || '未填写申请说明'}</span><small>{formatPlatformTime(item.submittedAt)}</small></div>
          <span className={`platform-status-badge ${item.status.toLowerCase()}`}>{approvalStatusLabels[item.status]}</span>
          <div className="approval-actions">
            {item.status === 'PENDING' ? <>
              <button className="quiet-button danger-text" type="button" disabled={Boolean(busyKey)} onClick={() => onDecision(item, 'REJECTED')}>拒绝</button>
              <button className="primary-button compact" type="button" disabled={Boolean(busyKey)} onClick={() => onDecision(item, 'APPROVED')}>
                {busyKey === `approval:${item.id}` && <SpinnerGap className="spin" size={13} />}通过
              </button>
            </> : <small>{item.reviewerDisplayName || '系统处理'}</small>}
          </div>
        </article>)}
      </div>}
  </div>
}

const taskStatusLabels: Record<BackgroundTask['status'], string> = {
  QUEUED: '排队中', RUNNING: '运行中', SUCCEEDED: '已完成', PARTIAL: '部分完成',
  FAILED: '失败', CANCELLED: '已中止', SKIPPED: '已跳过', STALE: '已失效',
}

function BackgroundTaskCenter({ tasks, busyKey, onOperate }: {
  tasks: BackgroundTask[]
  busyKey: string
  onOperate: (task: BackgroundTask, operation: 'cancel' | 'retry') => void
}) {
  const [filter, setFilter] = useState<'ACTIVE' | 'FAILED' | 'ALL'>('ACTIVE')
  const visible = tasks.filter(task => filter === 'ALL'
    || (filter === 'ACTIVE' ? task.status === 'QUEUED' || task.status === 'RUNNING' : task.status === 'FAILED' || task.status === 'PARTIAL'))
  return <div className="administration-view platform-task-view">
    <header className="administration-view-heading platform-section-heading">
      <div><span className="eyebrow">BACKGROUND OPERATIONS</span><h2>后台任务</h2><p>查看平台异步任务的阶段、进度与故障；中止和重试均保留审计记录。</p></div>
      <div className="platform-view-switch" aria-label="任务筛选">
        <button className={filter === 'ACTIVE' ? 'active' : ''} type="button" onClick={() => setFilter('ACTIVE')}>运行中</button>
        <button className={filter === 'FAILED' ? 'active' : ''} type="button" onClick={() => setFilter('FAILED')}>异常</button>
        <button className={filter === 'ALL' ? 'active' : ''} type="button" onClick={() => setFilter('ALL')}>全部</button>
      </div>
    </header>
    {visible.length === 0
      ? <div className="platform-module-empty"><Timer size={30} weight="duotone" /><strong>当前视图没有后台任务</strong><span>任务启动后会显示实时进度与安全操作。</span></div>
      : <div className="platform-task-list">
        {visible.map(task => <article key={`${task.kind}:${task.id}`}>
          <div className={`task-state-icon ${task.status.toLowerCase()}`}><Pulse size={18} weight="duotone" /></div>
          <div className="task-main"><span><strong>{task.name}</strong><em>{task.kindLabel}</em></span><small>{task.description || `${task.resourceType} · ${task.resourceId}`}</small></div>
          <div className="task-progress">
            <span><i style={{ width: `${task.progressPercent ?? (task.status === 'RUNNING' ? 42 : task.status === 'SUCCEEDED' ? 100 : 0)}%` }} /></span>
            <small>{task.progressText || (task.status === 'RUNNING' ? '正在处理' : taskStatusLabels[task.status])}</small>
          </div>
          <span className={`platform-status-badge ${task.status.toLowerCase()}`}>{taskStatusLabels[task.status]}</span>
          <div className="task-attempt"><strong>{task.attempt}/{task.maxAttempts}</strong><small>尝试次数</small></div>
          <div className="task-actions">
            {task.canCancel && <button className="quiet-button danger-text" type="button" disabled={Boolean(busyKey)} onClick={() => onOperate(task, 'cancel')}>中止</button>}
            {task.canRetry && <button className="quiet-button" type="button" disabled={Boolean(busyKey)} onClick={() => onOperate(task, 'retry')}>
              {busyKey === `task:retry:${task.kind}:${task.id}` && <SpinnerGap className="spin" size={13} />}重试
            </button>}
            {!task.canCancel && !task.canRetry && <small>{formatPlatformTime(task.updatedAt)}</small>}
          </div>
          {task.errorMessage && <p className="task-error">{task.errorCode ? `${task.errorCode} · ` : ''}{task.errorMessage}</p>}
        </article>)}
      </div>}
  </div>
}

const supportStatusLabels: Record<SupportTicket['status'], string> = {
  OPEN: '待受理', IN_PROGRESS: '处理中', RESOLVED: '已解决', CLOSED: '已关闭',
}
const supportCategoryLabels: Record<SupportTicket['category'], string> = {
  QUESTION: '问数分析', DATA: '数据资产', REPORT: '报告中心', ACCESS: '账号权限', SYSTEM: '平台运行', OTHER: '其他问题',
}

function SupportTicketCenter({ tickets, busyKey, onStart, onReopen, onResolve, onClose }: {
  tickets: SupportTicket[]
  busyKey: string
  onStart: (ticket: SupportTicket) => void
  onReopen: (ticket: SupportTicket) => void
  onResolve: (ticket: SupportTicket) => void
  onClose: (ticket: SupportTicket) => void
}) {
  const [filter, setFilter] = useState<'ACTIVE' | 'RESOLVED' | 'ALL'>('ACTIVE')
  const visible = tickets.filter(ticket => filter === 'ALL' || (filter === 'ACTIVE'
    ? ticket.status === 'OPEN' || ticket.status === 'IN_PROGRESS'
    : ticket.status === 'RESOLVED' || ticket.status === 'CLOSED'))
  return <div className="administration-view platform-support-view">
    <header className="administration-view-heading platform-section-heading">
      <div><span className="eyebrow">SERVICE DESK</span><h2>支持工单</h2><p>处理当前领域的产品使用与运行问题；处理结论会同步给提交人。</p></div>
      <div className="platform-view-switch" aria-label="工单筛选">
        <button className={filter === 'ACTIVE' ? 'active' : ''} type="button" onClick={() => setFilter('ACTIVE')}>待跟进</button>
        <button className={filter === 'RESOLVED' ? 'active' : ''} type="button" onClick={() => setFilter('RESOLVED')}>已处理</button>
        <button className={filter === 'ALL' ? 'active' : ''} type="button" onClick={() => setFilter('ALL')}>全部</button>
      </div>
    </header>
    {visible.length === 0
      ? <div className="platform-module-empty"><Lifebuoy size={30} weight="duotone" /><strong>当前没有支持工单</strong><span>用户从帮助中心提交的问题会自动进入这里。</span></div>
      : <div className="platform-support-list">{visible.map(ticket => <article key={ticket.id}>
        <span className={`support-priority is-${ticket.priority.toLowerCase()}`}>{ticket.priority === 'URGENT' ? '紧急' : ticket.priority === 'HIGH' ? '较高' : '普通'}</span>
        <div className="support-ticket-main"><strong>{ticket.subject}</strong><small>{supportCategoryLabels[ticket.category]} · {ticket.reporterName} · {ticket.id.slice(0, 8).toUpperCase()}</small><p>{ticket.description}</p>{ticket.errorCode && <code>{ticket.errorCode}</code>}</div>
        <div className="support-ticket-source"><small>发生页面</small><strong>{ticket.pageUrl || '未记录'}</strong><time>{formatPlatformTime(ticket.createdAt)}</time></div>
        <span className={`platform-status-badge ${ticket.status.toLowerCase()}`}>{supportStatusLabels[ticket.status]}</span>
        <div className="support-ticket-actions">
          {ticket.status === 'OPEN' && <button className="primary-button compact" type="button" disabled={Boolean(busyKey)} onClick={() => onStart(ticket)}>开始处理</button>}
          {ticket.status === 'IN_PROGRESS' && <><button className="quiet-button" type="button" disabled={Boolean(busyKey)} onClick={() => onClose(ticket)}>关闭</button><button className="primary-button compact" type="button" disabled={Boolean(busyKey)} onClick={() => onResolve(ticket)}>标记解决</button></>}
          {ticket.status === 'RESOLVED' && <><button className="quiet-button" type="button" disabled={Boolean(busyKey)} onClick={() => onReopen(ticket)}>重新处理</button><button className="quiet-button" type="button" disabled={Boolean(busyKey)} onClick={() => onClose(ticket)}>关闭</button></>}
          {ticket.status === 'CLOSED' && <small>{ticket.resolutionNote || '工单已关闭'}</small>}
        </div>
        {ticket.resolutionNote && <p className="support-resolution"><CheckCircle size={14} />{ticket.resolutionNote}</p>}
      </article>)}</div>}
  </div>
}

const auditActionLabels: Record<string, string> = {
  REGISTER: '用户注册', LOGIN: '用户登录', LOGOUT: '退出登录',
  CREATE: '创建资源', UPDATE_STATUS: '更新状态', UPDATE_USER_STATUS: '更新账号状态',
  SET_PLATFORM_ADMINISTRATOR: '调整平台管理员', REPLACE_ADMINISTRATORS: '调整领域管理员',
  ASSIGN_DOMAIN: '分配领域', REVOKE_DOMAIN: '移除领域',
  REVIEW_DOMAIN_APPLICATION: '审核领域申请', CANCEL_BACKGROUND_TASK: '中止后台任务',
  RETRY_BACKGROUND_TASK: '重试后台任务',
}

function PlatformLogCenter({ logs }: { logs: PlatformAuditLog[] }) {
  const [query, setQuery] = useState('')
  const normalized = query.trim().toLowerCase()
  const visible = logs.filter(log => !normalized || [log.action, log.resourceType, log.actorDisplayName, log.actorEmail]
    .some(value => value.toLowerCase().includes(normalized)))
  return <div className="administration-view platform-log-view">
    <header className="administration-view-heading platform-section-heading">
      <div><span className="eyebrow">IMMUTABLE AUDIT TRAIL</span><h2>平台日志</h2><p>记录平台治理、身份调整和运行操作。日志只追加、不可修改，不展示领域业务数据。</p></div>
      <label className="platform-log-search"><span>搜索日志</span><input type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder="操作、资源或操作者" /></label>
    </header>
    <div className="platform-log-table" role="table" aria-label="平台操作日志">
      <div className="platform-log-row platform-log-header" role="row"><span>时间</span><span>操作</span><span>操作者</span><span>资源</span><span>结果</span></div>
      {visible.map(log => <div className="platform-log-row" role="row" key={log.id}>
        <time dateTime={log.occurredAt}>{formatPlatformTime(log.occurredAt)}</time>
        <div><strong>{auditActionLabels[log.action] || log.action}</strong><small>{log.action}</small></div>
        <div><strong>{log.actorDisplayName}</strong><small>{log.actorEmail || '系统任务'}</small></div>
        <div><strong>{log.resourceType}</strong><small>{log.resourceId || '全局资源'}</small></div>
        <span className={`audit-result ${log.result.toLowerCase()}`}>{log.result === 'SUCCESS' ? '成功' : log.result === 'DENIED' ? '已拒绝' : '失败'}</span>
      </div>)}
      {visible.length === 0 && <div className="platform-module-empty"><Scroll size={28} /><strong>没有匹配的日志</strong></div>}
    </div>
  </div>
}

const accountStatusLabels: Record<AdminUser['status'], string> = {
  ACTIVE: '正常',
  DISABLED: '已停用',
  LOCKED: '已锁定',
}

const formatAccountTime = (value?: string) => {
  if (!value) return '从未登录'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(date)
}

function UserManagement({ users, busyKey, signedInUserID, onEditDomains, onStatus }: {
  users: AdminUser[]
  busyKey: string
  signedInUserID: string
  onEditDomains: (user: AdminUser) => void
  onStatus: (user: AdminUser) => void
}) {
  return <div className="administration-view user-management-view">
    <FixedScope level="USER" title="普通用户" description="展示不承担管理员职责的注册用户；平台管理员可停用或恢复账号。" capabilities={fixedCapabilities.account} />
    <header className="administration-view-heading"><div><span className="eyebrow">USER DIRECTORY</span><h2>普通用户</h2><p>查看普通用户信息与账号状态，支持停用不再使用的账号。</p></div><small>{users.length} 个普通用户</small></header>
    <div className="user-management-list">
      {users.map(user => {
        const protectedAccount = user.id === signedInUserID
        const protectionReason = user.id === signedInUserID
          ? '不能停用当前登录账号'
          : undefined
        return <article key={user.id}>
          <UserIdentity user={user} />
          <span className={`account-status ${user.status.toLowerCase()}`}>{accountStatusLabels[user.status]}</span>
          <div className="user-domain-list ordinary-user-domains">
            {user.domains.length > 0
              ? user.domains.map(domain => <span key={domain.id}>{domain.name}</span>)
              : <small>暂未加入领域</small>}
          </div>
          <div className="account-time-summary"><small>最近登录</small><strong>{formatAccountTime(user.lastLoginAt)}</strong></div>
          <div className="account-time-summary account-created-at"><small>注册时间</small><strong>{formatAccountTime(user.createdAt)}</strong></div>
          <div className="ordinary-user-actions">
            <button className="quiet-button" type="button" disabled={Boolean(busyKey) || user.status !== 'ACTIVE'} onClick={() => onEditDomains(user)}>管理领域</button>
            <button
              className={user.status === 'ACTIVE' ? 'quiet-button danger-text' : 'quiet-button'}
              type="button"
              title={user.status === 'ACTIVE' ? protectionReason : undefined}
              disabled={Boolean(busyKey) || (user.status === 'ACTIVE' && protectedAccount)}
              onClick={() => onStatus(user)}
            >
              {busyKey === `user-status:${user.id}` && <SpinnerGap className="spin" size={14} />}
              {user.status === 'ACTIVE' ? '停用' : '恢复'}
            </button>
          </div>
        </article>
      })}
    </div>
  </div>
}

function UserIdentity({ user }: { user: AdminUser }) {
  return <div className="member-identity">
    <span>{user.displayName.slice(0, 1)}</span>
    <div><strong>{user.displayName}</strong><small>{user.employeeNo} · {user.email}</small></div>
  </div>
}

function DialogFrame({ title, description, busy, error, onClose, children }: {
  title: string
  description: string
  busy: boolean
  error: string
  onClose: () => void
  children: React.ReactNode
}) {
  return <div className="administration-dialog-backdrop" role="presentation" onMouseDown={event => {
    if (event.target === event.currentTarget) onClose()
  }}>
    <section className="administration-dialog governance-dialog" role="dialog" aria-modal="true" aria-labelledby="governance-dialog-title">
      <header>
        <div><span className="eyebrow">ACCESS GOVERNANCE</span><h2 id="governance-dialog-title">{title}</h2><p>{description}</p></div>
        <button type="button" aria-label="关闭" disabled={busy} onClick={onClose}><X size={19} /></button>
      </header>
      {children}
      {error && <div className="dialog-feedback administration-feedback error" role="alert">{error}</div>}
    </section>
  </div>
}

function DomainDialog({ title, description, busy, error, onClose, onSubmit }: {
  title: string
  description: string
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  return <DialogFrame title={title} description={description} busy={busy} error={error} onClose={onClose}>
    <form onSubmit={onSubmit}>
      <label>领域名称<input name="name" autoFocus placeholder="例如：客户运营" /></label>
      <label>领域编码<input name="code" placeholder="customer_operations" /><small>以小写字母开头，可使用数字、下划线和短横线。</small></label>
      <label>说明<textarea name="description" placeholder="说明该领域承载的数据范围" /></label>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <CheckCircle size={16} />}{busy ? '正在创建…' : '创建领域'}</button></footer>
    </form>
  </DialogFrame>
}

function ApprovalRejectionDialog({ approval, busy, error, onClose, onSubmit }: {
  approval: PlatformApproval
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (reason: string) => void
}) {
  const [reason, setReason] = useState('')
  return <DialogFrame
    title="填写驳回原因"
    description={`申请：${approval.resourceName} · ${approvalKindLabels[approval.kind]}`}
    busy={busy}
    error={error}
    onClose={onClose}
  >
    <form onSubmit={event => { event.preventDefault(); if (reason.trim().length >= 4) onSubmit(reason.trim()) }}>
      <label>审核意见<textarea autoFocus minLength={4} maxLength={1000} required value={reason} onChange={event => setReason(event.target.value)} placeholder="说明需要修改的配置、口径或风险；申请人将据此修改后重新提交。" /><small>{reason.trim().length}/1000 · 至少 4 个字符</small></label>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button danger" type="submit" disabled={busy || reason.trim().length < 4}>{busy ? <SpinnerGap className="spin" size={16} /> : <X size={16} />}{busy ? '正在驳回…' : '确认驳回'}</button></footer>
    </form>
  </DialogFrame>
}

function SupportTransitionDialog({ ticket, status, busy, error, onClose, onSubmit }: {
  ticket: SupportTicket
  status: 'RESOLVED' | 'CLOSED'
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  return <DialogFrame
    title={status === 'RESOLVED' ? '确认问题已解决' : '关闭支持工单'}
    description={`工单：${ticket.subject}`}
    busy={busy}
    error={error}
    onClose={onClose}
  >
    <form onSubmit={onSubmit}>
      <label>处理结果<textarea name="resolutionNote" autoFocus minLength={4} maxLength={2000} required placeholder="说明定位结果、处理措施或后续建议，提交人会看到这段内容。" /></label>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <CheckCircle size={16} />}{busy ? '保存中…' : status === 'RESOLVED' ? '确认解决' : '确认关闭'}</button></footer>
    </form>
  </DialogFrame>
}

function PlatformAdministratorDialog({ users, busy, error, onClose, onSubmit }: {
  users: AdminUser[]
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  const candidates = users.filter(user => !user.platformAdministrator && user.domains.length === 0)
  return <DialogFrame
    title="新增平台管理员"
    description="从没有其他管理员或领域成员身份的活跃用户中选择。"
    busy={busy}
    error={error}
    onClose={onClose}
  >
    <form onSubmit={onSubmit}>
      <SelectionList label="可选用户" name="userIds" users={candidates} />
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy || candidates.length === 0}>{busy ? <SpinnerGap className="spin" size={16} /> : <Plus size={16} />}{busy ? '新增中…' : '新增管理员'}</button></footer>
    </form>
  </DialogFrame>
}

function DomainAdministratorDialog({ user, domains, users, busy, error, onClose, onSubmit }: {
  user?: AdminUser
  domains: BusinessDomain[]
  users: AdminUser[]
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  // An existing ordinary domain member is the most common person to promote.
  // Exclude only identities that already hold an administrator class; the API
  // atomically upgrades MEMBER to DOMAIN_ADMIN for the selected domains.
  const candidates = users.filter(item => item.status === 'ACTIVE' && !item.platformAdministrator &&
    !item.domains.some(domain => domain.memberRole === 'DOMAIN_ADMIN'))
  const selectedDomainIDs = new Set(user?.domains.filter(domain => domain.memberRole === 'DOMAIN_ADMIN').map(domain => domain.id) ?? [])
  const visibleDomains = domains.filter(domain => domain.status === 'ACTIVE' || selectedDomainIDs.has(domain.id))
  return <DialogFrame
    title={user ? `管理${user.displayName}的领域` : '新增领域管理员'}
    description="选择管理员负责的领域；同一位领域管理员可管理多个领域。"
    busy={busy}
    error={error}
    onClose={onClose}
  >
    <form onSubmit={onSubmit}>
      {user
        ? <div className="dialog-selected-user"><UserIdentity user={user} /><span className="identity-badge">领域管理员</span></div>
        : <label>选择用户<select name="userId" autoFocus defaultValue=""><option value="" disabled>请选择用户</option>{candidates.map(item => <option key={item.id} value={item.id}>{item.displayName} · {item.employeeNo}</option>)}</select></label>}
      <fieldset className="governance-selection"><legend>管理领域</legend>
        {visibleDomains.map(domain => <label key={domain.id}>
          <input type="checkbox" name="domainIds" value={domain.id} defaultChecked={selectedDomainIDs.has(domain.id)} />
          <span className="selection-check"><Check size={12} weight="bold" /></span>
          <span><strong>{domain.name}</strong><small>{domain.code}{domain.status === 'DISABLED' ? ' · 已停用' : ''}</small></span>
        </label>)}
      </fieldset>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy || (!user && candidates.length === 0)}>{busy ? <SpinnerGap className="spin" size={16} /> : <Check size={16} />}{busy ? '保存中…' : user ? '保存管理领域' : '新增管理员'}</button></footer>
    </form>
  </DialogFrame>
}

function UserDomainDialog({ user, domains, busy, error, onClose, onSubmit }: {
  user: AdminUser
  domains: BusinessDomain[]
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  const selectedDomainIDs = new Set(user.domains.map(domain => domain.id))
  return <DialogFrame
    title={`管理${user.displayName}的所属领域`}
    description="普通用户可同时加入多个领域；停用领域不再接受新的用户归属。"
    busy={busy}
    error={error}
    onClose={onClose}
  >
    <form onSubmit={onSubmit}>
      <div className="dialog-selected-user"><UserIdentity user={user} /><span className="identity-badge">普通用户</span></div>
      <fieldset className="governance-selection"><legend>所属领域</legend>
        {domains.map(domain => {
          const selected = selectedDomainIDs.has(domain.id)
          return <label key={domain.id} className={domain.status === 'DISABLED' && !selected ? 'locked' : ''}>
            <input type="checkbox" name="domainIds" value={domain.id} defaultChecked={selected} disabled={domain.status === 'DISABLED' && !selected} />
            <span className="selection-check"><Check size={12} weight="bold" /></span>
            <span><strong>{domain.name}</strong><small>{domain.code}{domain.status === 'DISABLED' ? ' · 已停用' : ''}</small></span>
          </label>
        })}
      </fieldset>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <Check size={16} />}{busy ? '保存中…' : '保存所属领域'}</button></footer>
    </form>
  </DialogFrame>
}

function SelectionList({ label, name, users, selected = new Set<string>() }: {
  label: string
  name: string
  users: AdminUser[]
  selected?: Set<string>
}) {
  return <fieldset className="governance-selection"><legend>{label}</legend>
    {users.filter(user => user.status === 'ACTIVE').map(user => <label key={user.id}>
      <input type="checkbox" name={name} value={user.id} defaultChecked={selected.has(user.id)} />
      <span className="selection-check"><Check size={12} weight="bold" /></span>
      <span><strong>{user.displayName}</strong><small>{user.employeeNo} · {user.email}</small></span>
    </label>)}
  </fieldset>
}
