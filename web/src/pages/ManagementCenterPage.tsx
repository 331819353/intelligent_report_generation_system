import {
  Check,
  CheckCircle,
  Database,
  GlobeHemisphereWest,
  LockKey,
  Plus,
  ShieldCheck,
  SpinnerGap,
  ToggleLeft,
  ToggleRight,
  UserCircle,
  UsersThree,
  X,
} from '@phosphor-icons/react'
import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { AppShell } from '../components/AppShell'
import {
  administrationAPI,
  type AdminRole,
  type AdminUser,
  type BusinessDomain,
  type PermissionDefinition,
} from '../lib/administration'
import {
  clearDomain,
  notifyDomainCatalogChanged,
  currentDomain,
} from '../lib/domain-context'

type ManagementView = 'domains' | 'members' | 'permissions'
type CreateDialog = 'domain' | 'role' | null

const resourceLabels: Record<string, string> = {
  TENANT: '租户',
  USER: '用户与角色',
  DATA_SOURCE: '数据源',
  DATA_ASSET: '数据资产',
  DATASET: '数据集',
  METRIC: '指标',
  REPORT: '报告',
  AI: '智能能力',
}

const actionLabels: Record<string, string> = {
  READ: '查看',
  CREATE: '创建',
  UPDATE: '编辑',
  MANAGE: '管理',
  PUBLISH: '发布',
  EXECUTE: '执行',
}

const formatDate = (value?: string) => {
  if (!value) return '尚未登录'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
  }).format(date)
}

/** 提供领域、成员角色和角色权限的一体化管理员工作区。 */
export function ManagementCenterPage() {
  const [view, setView] = useState<ManagementView>('domains')
  const [domains, setDomains] = useState<BusinessDomain[]>([])
  const [roles, setRoles] = useState<AdminRole[]>([])
  const [users, setUsers] = useState<AdminUser[]>([])
  const [permissions, setPermissions] = useState<PermissionDefinition[]>([])
  const [selectedRoleID, setSelectedRoleID] = useState('')
  const [permissionSelection, setPermissionSelection] = useState<string[]>([])
  const [createDialog, setCreateDialog] = useState<CreateDialog>(null)
  const [loading, setLoading] = useState(true)
  const [busyKey, setBusyKey] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [platformAdministrator, setPlatformAdministrator] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [domainResult, nextRoles, nextUsers, nextPermissions] = await Promise.all([
        administrationAPI.listManagedDomains()
          .then(items => ({ items, platformAdministrator: true }))
          .catch(async () => ({
            items: await administrationAPI.listDomains(),
            platformAdministrator: false,
          })),
        administrationAPI.listRoles(),
        administrationAPI.listUsers(),
        administrationAPI.listPermissions(),
      ])
      setDomains(domainResult.items)
      setPlatformAdministrator(domainResult.platformAdministrator)
      setRoles(nextRoles)
      setUsers(nextUsers)
      setPermissions(nextPermissions)
      setSelectedRoleID(nextRoles[0]?.id || '')
      setPermissionSelection(nextRoles[0]?.permissionCodes ?? [])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '管理中心加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() }, 0)
    return () => window.clearTimeout(timer)
  }, [load])

  const selectedRole = roles.find(role => role.id === selectedRoleID)
  const selectRole = (roleID: string) => {
    setSelectedRoleID(roleID)
    setPermissionSelection(
      roles.find(role => role.id === roleID)?.permissionCodes ?? [],
    )
  }

  const groupedPermissions = useMemo(() => {
    const groups = new Map<string, PermissionDefinition[]>()
    permissions.forEach(permission => {
      const group = groups.get(permission.resourceType) ?? []
      group.push(permission)
      groups.set(permission.resourceType, group)
    })
    return [...groups.entries()]
  }, [permissions])

  const activeDomainCount = domains.filter(domain => domain.status === 'ACTIVE').length
  const customRoleCount = roles.filter(role => !role.system).length

  const submitDomain = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const input = {
      code: String(form.get('code') ?? '').trim().toLowerCase(),
      name: String(form.get('name') ?? '').trim(),
      description: String(form.get('description') ?? '').trim(),
      administratorUserIds: form.getAll('administratorUserIds').map(String),
    }
    if (!input.code || !input.name) {
      setError('请填写领域名称和领域编码')
      return
    }
    if (input.administratorUserIds.length === 0) {
      setError('请至少指定一位领域管理员')
      return
    }
    setBusyKey('create-domain')
    setError('')
    try {
      const domain = await administrationAPI.createDomain(input)
      setDomains(current => [...current, domain])
      notifyDomainCatalogChanged()
      setNotice(`领域“${domain.name}”已创建，可立即从侧栏切换`)
      setCreateDialog(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '创建领域失败')
    } finally {
      setBusyKey('')
    }
  }

  const submitRole = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const input = {
      code: String(form.get('code') ?? '').trim().toLowerCase(),
      name: String(form.get('name') ?? '').trim(),
      description: String(form.get('description') ?? '').trim(),
    }
    if (!input.code || !input.name) {
      setError('请填写角色名称和角色编码')
      return
    }
    setBusyKey('create-role')
    setError('')
    try {
      const role = await administrationAPI.createRole(input)
      const normalized = {
        ...role,
        permissionCodes: role.permissionCodes ?? [],
        userCount: role.userCount ?? 0,
      }
      setRoles(current => [...current, normalized])
      setSelectedRoleID(normalized.id)
      setPermissionSelection([])
      setView('permissions')
      setNotice(`角色“${normalized.name}”已创建，请继续配置权限`)
      setCreateDialog(null)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '创建角色失败')
    } finally {
      setBusyKey('')
    }
  }

  const toggleDomain = async (domain: BusinessDomain) => {
    const nextStatus = domain.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE'
    setBusyKey(`domain:${domain.id}`)
    setError('')
    try {
      const disablingCurrentDomain = nextStatus === 'DISABLED' && currentDomain()?.id === domain.id
      const updated = await administrationAPI.updateDomainStatus(domain.id, nextStatus)
      const nextDomains = domains.map(item => item.id === updated.id ? updated : item)
      setDomains(nextDomains)
      if (disablingCurrentDomain) {
        clearDomain()
        window.location.assign('/domain-access')
      } else {
        notifyDomainCatalogChanged()
      }
      setNotice(`领域“${updated.name}”已${updated.status === 'ACTIVE' ? '启用' : '停用'}`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '更新领域状态失败')
    } finally {
      setBusyKey('')
    }
  }

  const toggleUserRole = async (user: AdminUser, role: AdminRole) => {
    const assigned = user.roles.some(item => item.id === role.id)
    setBusyKey(`user-role:${user.id}:${role.id}`)
    setError('')
    try {
      if (assigned) {
        await administrationAPI.revokeUserRole(user.id, role.id)
      } else {
        await administrationAPI.assignUserRole(user.id, role.id)
      }
      setUsers(current => current.map(item => item.id !== user.id ? item : {
        ...item,
        roles: assigned
          ? item.roles.filter(userRole => userRole.id !== role.id)
          : [...item.roles, { id: role.id, code: role.code, name: role.name }],
      }))
      setRoles(current => current.map(item => item.id !== role.id ? item : {
        ...item,
        userCount: Math.max(0, item.userCount + (assigned ? -1 : 1)),
      }))
      setNotice(`${user.displayName}已${assigned ? '移除' : '分配'}“${role.name}”角色`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '更新用户角色失败')
    } finally {
      setBusyKey('')
    }
  }

  const toggleUserDomain = async (user: AdminUser, domain: BusinessDomain) => {
    const assigned = (user.domains ?? []).some(item => item.id === domain.id)
    setBusyKey(`user-domain:${user.id}:${domain.id}`)
    setError('')
    try {
      if (assigned) {
        await administrationAPI.revokeUserDomain(user.id, domain.id)
      } else {
        await administrationAPI.assignUserDomain(user.id, domain.id)
      }
      setUsers(current => current.map(item => item.id !== user.id ? item : {
        ...item,
        domains: assigned
          ? (item.domains ?? []).filter(userDomain => userDomain.id !== domain.id)
          : [...(item.domains ?? []), {
            id: domain.id,
            code: domain.code,
            name: domain.name,
            default: domain.default,
            memberRole: 'MEMBER' as const,
          }],
      }))
      setNotice(`${user.displayName}已${assigned ? '移出' : '加入'}“${domain.name}”领域`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '更新用户领域失败')
    } finally {
      setBusyKey('')
    }
  }

  const togglePermission = (code: string) => {
    setPermissionSelection(current =>
      current.includes(code) ? current.filter(item => item !== code) : [...current, code],
    )
  }

  const savePermissions = async () => {
    if (!selectedRole || selectedRole.system) return
    setBusyKey(`permissions:${selectedRole.id}`)
    setError('')
    try {
      await administrationAPI.replaceRolePermissions(selectedRole.id, permissionSelection)
      setRoles(current => current.map(role => role.id === selectedRole.id
        ? { ...role, permissionCodes: [...permissionSelection] }
        : role))
      setNotice(`“${selectedRole.name}”的权限已更新`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '保存角色权限失败')
    } finally {
      setBusyKey('')
    }
  }

  const actions = view === 'domains' && !platformAdministrator ? null : <button
    className="primary-button"
    type="button"
    onClick={() => {
      setError('')
      setCreateDialog(view === 'domains' ? 'domain' : 'role')
    }}
  >
    <Plus size={17} weight="bold" />
    {view === 'domains' ? '创建领域' : '创建角色'}
  </button>

  return (
    <AppShell title="管理中心" eyebrow="组织与权限" actions={actions} className="administration-shell">
      <section className="administration-stack">
        <div className="administration-hero">
          <div>
            <span className="eyebrow">ADMINISTRATION</span>
            <h2>统一管理领域、成员与访问权限</h2>
            <p>领域负责组织分析工作，角色负责定义能力边界；所有变更均由服务端鉴权并写入审计记录。</p>
          </div>
          <ShieldCheck size={62} weight="duotone" aria-hidden="true" />
        </div>

        <div className="administration-metrics" aria-label="管理中心概览">
          <article><GlobeHemisphereWest size={20} weight="duotone" /><span>启用领域</span><strong>{activeDomainCount}</strong><small>共 {domains.length} 个领域</small></article>
          <article><UsersThree size={20} weight="duotone" /><span>组织成员</span><strong>{users.length}</strong><small>当前租户账号</small></article>
          <article><LockKey size={20} weight="duotone" /><span>自定义角色</span><strong>{customRoleCount}</strong><small>另有 {roles.length - customRoleCount} 个系统角色</small></article>
        </div>

        {(error || notice) && <div className={`administration-feedback ${error ? 'error' : 'success'}`} role={error ? 'alert' : 'status'}>
          {error || notice}
          <button type="button" aria-label="关闭提示" onClick={() => { setError(''); setNotice('') }}><X size={15} /></button>
        </div>}

        <div className="administration-workspace">
          <nav className="administration-tabs" aria-label="管理中心功能">
            <button type="button" className={view === 'domains' ? 'active' : ''} onClick={() => setView('domains')}>
              <GlobeHemisphereWest size={19} /><span><strong>领域管理</strong><small>创建和维护业务领域</small></span>
            </button>
            <button type="button" className={view === 'members' ? 'active' : ''} onClick={() => setView('members')}>
              <UserCircle size={19} /><span><strong>成员与角色</strong><small>为成员分配职责</small></span>
            </button>
            <button type="button" className={view === 'permissions' ? 'active' : ''} onClick={() => setView('permissions')}>
              <ShieldCheck size={19} /><span><strong>权限配置</strong><small>配置角色能力范围</small></span>
            </button>
            <div className="administration-security-note">
              <LockKey size={17} weight="duotone" />
              <span><strong>安全提示</strong><small>系统角色权限为只读，避免误撤销核心管理能力。</small></span>
            </div>
          </nav>

          <section className="administration-panel">
            {loading
              ? <div className="administration-empty" role="status"><SpinnerGap className="spin" size={32} /><strong>正在加载管理数据…</strong></div>
              : error && domains.length === 0 && roles.length === 0
                ? <div className="administration-empty"><LockKey size={34} /><strong>无法进入管理中心</strong><p>请确认当前账号拥有用户管理权限。</p><button className="quiet-button" type="button" onClick={() => void load()}>重新加载</button></div>
                : <>
                  {view === 'domains' && <DomainManagement
                    domains={domains}
                    platformAdministrator={platformAdministrator}
                    busyKey={busyKey}
                    onToggle={toggleDomain}
                    onCreate={() => setCreateDialog('domain')}
                  />}
                  {view === 'members' && <MemberManagement
                    users={users}
                    roles={roles}
                    domains={domains.filter(domain => domain.status === 'ACTIVE')}
                    platformAdministrator={platformAdministrator}
                    busyKey={busyKey}
                    onToggleRole={toggleUserRole}
                    onToggleDomain={toggleUserDomain}
                  />}
                  {view === 'permissions' && <PermissionManagement
                    roles={roles}
                    selectedRole={selectedRole}
                    selectedRoleID={selectedRoleID}
                    permissionSelection={permissionSelection}
                    groupedPermissions={groupedPermissions}
                    busyKey={busyKey}
                    onSelectRole={selectRole}
                    onTogglePermission={togglePermission}
                    onSave={() => void savePermissions()}
                  />}
                </>}
          </section>
        </div>
      </section>

      {createDialog && <CreateManagementDialog
        type={createDialog}
        users={users}
        busy={Boolean(busyKey)}
        error={error}
        onClose={() => {
          if (busyKey) return
          setCreateDialog(null)
          setError('')
        }}
        onSubmit={createDialog === 'domain' ? submitDomain : submitRole}
      />}
    </AppShell>
  )
}

function DomainManagement({
  domains,
  platformAdministrator,
  busyKey,
  onToggle,
  onCreate,
}: {
  domains: BusinessDomain[]
  platformAdministrator: boolean
  busyKey: string
  onToggle: (domain: BusinessDomain) => void
  onCreate: () => void
}) {
  return <div className="administration-view">
    <header className="administration-view-heading">
      <div><span className="eyebrow">业务范围</span><h2>领域管理</h2><p>领域会出现在全局侧栏的切换器中，停用后不再允许进入。</p></div>
      {platformAdministrator && <button className="quiet-button" type="button" onClick={onCreate}><Plus size={16} />创建领域</button>}
    </header>
    {domains.length === 0
      ? <div className="administration-empty"><GlobeHemisphereWest size={34} /><strong>还没有业务领域</strong><p>创建第一个领域后，成员即可从侧栏切换。</p></div>
      : <div className="domain-management-grid">
        {domains.map(domain => <article key={domain.id} className={domain.status === 'ACTIVE' ? '' : 'disabled'}>
          <header>
            <span className="domain-management-avatar">{domain.name.slice(0, 1)}</span>
            <span className={`domain-status ${domain.status.toLowerCase()}`}>{domain.status === 'ACTIVE' ? '已启用' : '已停用'}</span>
          </header>
          <div><h3>{domain.name}{domain.default && <em>默认</em>}</h3><code>{domain.code}</code></div>
          <p>{domain.description || '暂未填写领域说明'}</p>
          <p className="domain-administrators">领域管理员：{domain.administrators?.map(item => item.displayName).join('、') || '未指定'}</p>
          <footer>
            <small>创建于 {formatDate(domain.createdAt)}</small>
            <button
              type="button"
              disabled={!platformAdministrator || domain.default || Boolean(busyKey)}
              aria-label={`${domain.status === 'ACTIVE' ? '停用' : '启用'}${domain.name}`}
              title={!platformAdministrator ? '仅平台管理员可以启停领域' : domain.default ? '默认领域不可停用' : undefined}
              onClick={() => onToggle(domain)}
            >
              {busyKey === `domain:${domain.id}`
                ? <SpinnerGap className="spin" size={20} />
                : domain.status === 'ACTIVE'
                  ? <ToggleRight size={24} weight="fill" />
                  : <ToggleLeft size={24} />}
            </button>
          </footer>
        </article>)}
      </div>}
  </div>
}

function MemberManagement({
  users,
  roles,
  domains,
  platformAdministrator,
  busyKey,
  onToggleRole,
  onToggleDomain,
}: {
  users: AdminUser[]
  roles: AdminRole[]
  domains: BusinessDomain[]
  platformAdministrator: boolean
  busyKey: string
  onToggleRole: (user: AdminUser, role: AdminRole) => void
  onToggleDomain: (user: AdminUser, domain: BusinessDomain) => void
}) {
  return <div className="administration-view">
    <header className="administration-view-heading">
      <div><span className="eyebrow">IDENTITY & ACCESS</span><h2>成员与角色</h2><p>点击角色即可为成员分配或移除职责，变更立即生效。</p></div>
    </header>
    {users.length === 0
      ? <div className="administration-empty"><UsersThree size={34} /><strong>暂无可管理成员</strong></div>
      : <div className="member-management-list">
        {users.map(user => <article key={user.id}>
          <div className="member-identity">
            <span>{user.displayName.slice(0, 1)}</span>
            <div><strong>{user.displayName}</strong><small>{user.email}</small></div>
          </div>
          <div className="member-meta"><span className={user.status.toLowerCase()}>{user.status === 'ACTIVE' ? '正常' : user.status}</span><small>最近登录：{formatDate(user.lastLoginAt)}</small></div>
          <div className="member-domain-list" aria-label={`${user.displayName}的领域`}>
            <small>领域</small>
            {domains.map(domain => {
              const assigned = (user.domains ?? []).some(item => item.id === domain.id)
              const assignment = (user.domains ?? []).find(item => item.id === domain.id)
              const domainAdministrator = assignment?.memberRole === 'DOMAIN_ADMIN'
              const busy = busyKey === `user-domain:${user.id}:${domain.id}`
              return <button
                type="button"
                key={domain.id}
                className={assigned ? 'assigned' : ''}
                aria-pressed={assigned}
                disabled={!platformAdministrator || Boolean(busyKey) || domainAdministrator}
                title={!platformAdministrator ? '仅平台管理员可以直接调整成员领域' : domainAdministrator ? '领域管理员须先由平台管理员完成替换' : undefined}
                onClick={() => onToggleDomain(user, domain)}
              >
                {busy ? <SpinnerGap className="spin" size={13} /> : assigned && <Check size={13} weight="bold" />}
                {domain.name}{domainAdministrator ? ' · 管理员' : ''}
              </button>
            })}
          </div>
          <div className="member-role-list" aria-label={`${user.displayName}的角色`}>
            <small>角色</small>
            {roles.map(role => {
              const assigned = user.roles.some(item => item.id === role.id)
              const busy = busyKey === `user-role:${user.id}:${role.id}`
              return <button
                type="button"
                key={role.id}
                className={assigned ? 'assigned' : ''}
                aria-pressed={assigned}
                disabled={Boolean(busyKey)}
                onClick={() => onToggleRole(user, role)}
              >
                {busy ? <SpinnerGap className="spin" size={13} /> : assigned && <Check size={13} weight="bold" />}
                {role.name}
              </button>
            })}
          </div>
        </article>)}
      </div>}
  </div>
}

function PermissionManagement({
  roles,
  selectedRole,
  selectedRoleID,
  permissionSelection,
  groupedPermissions,
  busyKey,
  onSelectRole,
  onTogglePermission,
  onSave,
}: {
  roles: AdminRole[]
  selectedRole?: AdminRole
  selectedRoleID: string
  permissionSelection: string[]
  groupedPermissions: Array<[string, PermissionDefinition[]]>
  busyKey: string
  onSelectRole: (id: string) => void
  onTogglePermission: (code: string) => void
  onSave: () => void
}) {
  const dirty = selectedRole
    ? [...permissionSelection].sort().join('|') !== [...selectedRole.permissionCodes].sort().join('|')
    : false
  return <div className="permission-management">
    <aside>
      <header><span className="eyebrow">角色</span><h2>权限配置</h2></header>
      {roles.map(role => <button
        type="button"
        key={role.id}
        className={selectedRoleID === role.id ? 'active' : ''}
        onClick={() => onSelectRole(role.id)}
      >
        {role.system ? <LockKey size={16} /> : <ShieldCheck size={16} />}
        <span><strong>{role.name}</strong><small>{role.userCount} 位成员 · {role.permissionCodes.length} 项权限</small></span>
      </button>)}
    </aside>
    <section>
      {selectedRole
        ? <>
          <header className="permission-heading">
            <div>
              <span className="eyebrow">{selectedRole.system ? '系统角色 · 只读' : '自定义角色'}</span>
              <h2>{selectedRole.name}</h2>
              <p>{selectedRole.description || '按业务职责勾选该角色可以使用的功能。'}</p>
            </div>
            {!selectedRole.system && <button className="primary-button" type="button" disabled={!dirty || Boolean(busyKey)} onClick={onSave}>
              {busyKey === `permissions:${selectedRole.id}` ? <SpinnerGap className="spin" size={16} /> : <CheckCircle size={16} />}
              {busyKey === `permissions:${selectedRole.id}` ? '保存中…' : '保存权限'}
            </button>}
          </header>
          <div className="permission-group-list">
            {groupedPermissions.map(([resource, items]) => <section key={resource}>
              <header><Database size={17} weight="duotone" /><strong>{resourceLabels[resource] || resource}</strong><small>{items.length} 项能力</small></header>
              <div>
                {items.map(permission => {
                  const checked = permissionSelection.includes(permission.code)
                  return <label key={permission.code} className={checked ? 'checked' : ''}>
                    <input
                      type="checkbox"
                      checked={checked}
                      disabled={selectedRole.system}
                      onChange={() => onTogglePermission(permission.code)}
                    />
                    <span className="permission-check">{checked && <Check size={13} weight="bold" />}</span>
                    <span><strong>{permission.name}</strong><small>{actionLabels[permission.action] || permission.action} · {permission.code}</small></span>
                  </label>
                })}
              </div>
            </section>)}
          </div>
        </>
        : <div className="administration-empty"><ShieldCheck size={34} /><strong>请选择一个角色</strong></div>}
    </section>
  </div>
}

function CreateManagementDialog({
  type,
  users,
  busy,
  error,
  onClose,
  onSubmit,
}: {
  type: Exclude<CreateDialog, null>
  users: AdminUser[]
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  const isDomain = type === 'domain'
  return <div className="administration-dialog-backdrop" role="presentation" onMouseDown={event => {
    if (event.target === event.currentTarget) onClose()
  }}>
    <section className="administration-dialog" role="dialog" aria-modal="true" aria-labelledby="administration-dialog-title">
      <header>
        <div><span className="eyebrow">{isDomain ? 'BUSINESS DOMAIN' : 'ACCESS ROLE'}</span><h2 id="administration-dialog-title">{isDomain ? '创建业务领域' : '创建自定义角色'}</h2><p>{isDomain ? '创建后会立即出现在侧栏领域切换器中。' : '创建后继续配置权限并分配给成员。'}</p></div>
        <button type="button" aria-label="关闭" disabled={busy} onClick={onClose}><X size={19} /></button>
      </header>
      <form onSubmit={onSubmit}>
        <label>{isDomain ? '领域名称' : '角色名称'}<input name="name" autoFocus placeholder={isDomain ? '例如：客户运营' : '例如：报告分析师'} /></label>
        <label>{isDomain ? '领域编码' : '角色编码'}<input name="code" placeholder={isDomain ? 'customer-operations' : 'report_analyst'} /><small>以小写字母开头，可使用数字、下划线和短横线。</small></label>
        <label>说明<textarea name="description" placeholder={isDomain ? '说明该领域负责的业务范围和分析目标' : '说明该角色的主要职责'} /></label>
        {isDomain && <fieldset className="domain-administrator-picker">
          <legend>领域管理员</legend>
          <small>至少指定一位；创建后仅平台管理员可以调整。</small>
          {users.filter(user => user.status === 'ACTIVE').map(user => <label key={user.id}>
            <input type="checkbox" name="administratorUserIds" value={user.id} />
            <span><strong>{user.displayName}</strong><small>{user.email}</small></span>
          </label>)}
        </fieldset>}
        {error && <div className="administration-dialog-error" role="alert">{error}</div>}
        <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <Plus size={16} />}{busy ? '正在创建…' : '确认创建'}</button></footer>
      </form>
    </section>
  </div>
}
