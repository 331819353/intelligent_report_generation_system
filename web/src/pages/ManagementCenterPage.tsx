import {
  Check,
  CheckCircle,
  Database,
  LockKey,
  Plus,
  ShieldCheck,
  SpinnerGap,
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
  type PermissionDefinition,
} from '../lib/administration'

type PermissionView = 'permissions' | 'members'

const resourceLabels: Record<string, string> = {
  TENANT: '租户',
  USER: '用户与角色',
  DATA_SOURCE: '数据源',
  DATA_ASSET: '数据资产',
  DATASET: '数据集',
}

const supportedPermissionResources = new Set(Object.keys(resourceLabels))

const actionLabels: Record<string, string> = {
  READ: '查看',
  CREATE: '创建',
  UPDATE: '编辑',
  MANAGE: '管理',
  PUBLISH: '发布',
  EXECUTE: '执行',
}

/** 提供角色权限配置与成员授权。 */
export function ManagementCenterPage() {
  const [view, setView] = useState<PermissionView>('permissions')
  const [roles, setRoles] = useState<AdminRole[]>([])
  const [users, setUsers] = useState<AdminUser[]>([])
  const [permissions, setPermissions] = useState<PermissionDefinition[]>([])
  const [selectedRoleID, setSelectedRoleID] = useState('')
  const [permissionSelection, setPermissionSelection] = useState<string[]>([])
  const [createRoleOpen, setCreateRoleOpen] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busyKey, setBusyKey] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [nextRoles, nextUsers, nextPermissions] = await Promise.all([
        administrationAPI.listRoles(),
        administrationAPI.listUsers(),
        administrationAPI.listPermissions(),
      ])
      setRoles(nextRoles)
      setUsers(nextUsers)
      setPermissions(nextPermissions)
      setSelectedRoleID(current => {
        const nextID = nextRoles.some(role => role.id === current)
          ? current
          : nextRoles[0]?.id || ''
        setPermissionSelection(
          nextRoles.find(role => role.id === nextID)?.permissionCodes ?? [],
        )
        return nextID
      })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '权限设定加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() }, 0)
    return () => window.clearTimeout(timer)
  }, [load])

  const selectedRole = roles.find(role => role.id === selectedRoleID)
  const groupedPermissions = useMemo(() => {
    const groups = new Map<string, PermissionDefinition[]>()
    permissions
      .filter(permission => supportedPermissionResources.has(permission.resourceType))
      .forEach(permission => {
        const group = groups.get(permission.resourceType) ?? []
        group.push(permission)
        groups.set(permission.resourceType, group)
      })
    return [...groups.entries()]
  }, [permissions])

  const selectRole = (roleID: string) => {
    setSelectedRoleID(roleID)
    setPermissionSelection(
      roles.find(role => role.id === roleID)?.permissionCodes ?? [],
    )
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
      const created = await administrationAPI.createRole(input)
      const role = {
        ...created,
        permissionCodes: created.permissionCodes ?? [],
        userCount: created.userCount ?? 0,
      }
      setRoles(current => [...current, role])
      setSelectedRoleID(role.id)
      setPermissionSelection([])
      setView('permissions')
      setCreateRoleOpen(false)
      setNotice(`角色“${role.name}”已创建，请继续配置权限`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '创建角色失败')
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
      setError(cause instanceof Error ? cause.message : '更新成员授权失败')
    } finally {
      setBusyKey('')
    }
  }

  const togglePermission = (code: string) => {
    setPermissionSelection(current =>
      current.includes(code)
        ? current.filter(item => item !== code)
        : [...current, code],
    )
  }

  const savePermissions = async () => {
    if (!selectedRole || selectedRole.system) return
    setBusyKey(`permissions:${selectedRole.id}`)
    setError('')
    try {
      await administrationAPI.replaceRolePermissions(
        selectedRole.id,
        permissionSelection,
      )
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

  const customRoleCount = roles.filter(role => !role.system).length

  return (
    <AppShell
      title="权限设定"
      eyebrow="访问控制"
      actions={<button className="primary-button" type="button" onClick={() => {
        setError('')
        setCreateRoleOpen(true)
      }}><Plus size={17} weight="bold" />创建角色</button>}
      className="administration-shell"
    >
      <section className="administration-stack">
        <div className="administration-hero">
          <div>
            <span className="eyebrow">PERMISSIONS</span>
            <h2>配置角色权限与成员授权</h2>
            <p>权限范围仅覆盖数据源、数据资产和数据集配置，所有变更均由服务端鉴权并记录审计。</p>
          </div>
          <ShieldCheck size={62} weight="duotone" aria-hidden="true" />
        </div>

        <div className="administration-metrics" aria-label="权限设定概览">
          <article><LockKey size={20} weight="duotone" /><span>自定义角色</span><strong>{customRoleCount}</strong><small>另有 {roles.length - customRoleCount} 个系统角色</small></article>
          <article><UsersThree size={20} weight="duotone" /><span>可授权成员</span><strong>{users.length}</strong><small>当前租户账号</small></article>
          <article><ShieldCheck size={20} weight="duotone" /><span>权限能力</span><strong>{groupedPermissions.reduce((total, [, items]) => total + items.length, 0)}</strong><small>仅保留配置相关权限</small></article>
        </div>

        {(error || notice) && <div className={`administration-feedback ${error ? 'error' : 'success'}`} role={error ? 'alert' : 'status'}>
          {error || notice}
          <button type="button" aria-label="关闭提示" onClick={() => { setError(''); setNotice('') }}><X size={15} /></button>
        </div>}

        <div className="administration-workspace">
          <nav className="administration-tabs" aria-label="权限设定功能">
            <button type="button" className={view === 'permissions' ? 'active' : ''} onClick={() => setView('permissions')}>
              <ShieldCheck size={19} /><span><strong>角色权限</strong><small>配置角色能力范围</small></span>
            </button>
            <button type="button" className={view === 'members' ? 'active' : ''} onClick={() => setView('members')}>
              <UserCircle size={19} /><span><strong>成员授权</strong><small>为成员分配角色</small></span>
            </button>
            <div className="administration-security-note">
              <LockKey size={17} weight="duotone" />
              <span><strong>安全提示</strong><small>系统角色权限为只读，避免误撤销核心管理能力。</small></span>
            </div>
          </nav>

          <section className="administration-panel">
            {loading
              ? <div className="administration-empty" role="status"><SpinnerGap className="spin" size={32} /><strong>正在加载权限数据…</strong></div>
              : error && roles.length === 0
                ? <div className="administration-empty"><LockKey size={34} /><strong>无法进入权限设定</strong><p>请确认当前账号拥有用户管理权限。</p><button className="quiet-button" type="button" onClick={() => void load()}>重新加载</button></div>
                : view === 'permissions'
                  ? <PermissionManagement
                    roles={roles}
                    selectedRole={selectedRole}
                    selectedRoleID={selectedRoleID}
                    permissionSelection={permissionSelection}
                    groupedPermissions={groupedPermissions}
                    busyKey={busyKey}
                    onSelectRole={selectRole}
                    onTogglePermission={togglePermission}
                    onSave={() => void savePermissions()}
                  />
                  : <MemberAuthorization
                    users={users}
                    roles={roles}
                    busyKey={busyKey}
                    onToggleRole={toggleUserRole}
                  />}
          </section>
        </div>
      </section>

      {createRoleOpen && <CreateRoleDialog
        busy={Boolean(busyKey)}
        error={error}
        onClose={() => {
          if (busyKey) return
          setCreateRoleOpen(false)
          setError('')
        }}
        onSubmit={submitRole}
      />}
    </AppShell>
  )
}

function MemberAuthorization({
  users,
  roles,
  busyKey,
  onToggleRole,
}: {
  users: AdminUser[]
  roles: AdminRole[]
  busyKey: string
  onToggleRole: (user: AdminUser, role: AdminRole) => void
}) {
  return <div className="administration-view">
    <header className="administration-view-heading">
      <div><span className="eyebrow">MEMBER ACCESS</span><h2>成员授权</h2><p>点击角色即可为成员授予或撤销对应配置权限，变更立即生效。</p></div>
    </header>
    {users.length === 0
      ? <div className="administration-empty"><UsersThree size={34} /><strong>暂无可授权成员</strong></div>
      : <div className="member-management-list">
        {users.map(user => <article key={user.id}>
          <div className="member-identity">
            <span>{user.displayName.slice(0, 1)}</span>
            <div><strong>{user.displayName}</strong><small>{user.email}</small></div>
          </div>
          <div className="member-meta"><span className={user.status.toLowerCase()}>{user.status === 'ACTIVE' ? '正常' : user.status}</span><small>{user.roles.length} 个角色</small></div>
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
              <p>{selectedRole.description || '按业务职责勾选该角色可以使用的配置能力。'}</p>
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

function CreateRoleDialog({
  busy,
  error,
  onClose,
  onSubmit,
}: {
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  return <div className="administration-dialog-backdrop" role="presentation" onMouseDown={event => {
    if (event.target === event.currentTarget) onClose()
  }}>
    <section className="administration-dialog" role="dialog" aria-modal="true" aria-labelledby="administration-dialog-title">
      <header>
        <div><span className="eyebrow">ACCESS ROLE</span><h2 id="administration-dialog-title">创建自定义角色</h2><p>创建后继续配置权限并分配给成员。</p></div>
        <button type="button" aria-label="关闭" disabled={busy} onClick={onClose}><X size={19} /></button>
      </header>
      <form onSubmit={onSubmit}>
        <label>角色名称<input name="name" autoFocus placeholder="例如：数据集管理员" /></label>
        <label>角色编码<input name="code" placeholder="dataset_admin" /><small>以小写字母开头，可使用数字、下划线和短横线。</small></label>
        <label>说明<textarea name="description" placeholder="说明该角色负责的数据配置范围" /></label>
        {error && <div className="administration-feedback error" role="alert">{error}</div>}
        <footer>
          <button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button>
          <button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <CheckCircle size={16} />}{busy ? '正在创建…' : '创建角色'}</button>
        </footer>
      </form>
    </section>
  </div>
}
