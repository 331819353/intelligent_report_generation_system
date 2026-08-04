import {
  ArrowRight,
  Check,
  CheckCircle,
  Crown,
  GlobeHemisphereWest,
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
  type AdminUser,
  type BusinessDomain,
} from '../lib/administration'
import { notifyDomainCatalogChanged } from '../lib/domain-context'

type GovernanceView = 'platform' | 'domains' | 'users'
type DialogState =
  | { kind: 'platform-administrators' }
  | { kind: 'create-domain' }
  | { kind: 'domain-admins'; domain: BusinessDomain }
  | { kind: 'user-domains'; user: AdminUser }
  | null

const fixedCapabilities = {
  platform: ['管理平台管理员', '创建及停用领域', '分配用户领域归属'],
  domain: ['管理领域数据配置', '审批数据源与数据集发布', '审批用户加入领域'],
  user: ['配置数据源与数据集', '查看领域内数据资产', '提交配置等待领域发布'],
}

/** 按平台、领域、用户三级固定边界管理身份与归属。 */
export function ManagementCenterPage() {
  const [view, setView] = useState<GovernanceView>('platform')
  const [domains, setDomains] = useState<BusinessDomain[]>([])
  const [users, setUsers] = useState<AdminUser[]>([])
  const [dialog, setDialog] = useState<DialogState>(null)
  const [loading, setLoading] = useState(true)
  const [busyKey, setBusyKey] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [nextDomains, nextUsers] = await Promise.all([
        administrationAPI.listManagedDomains(),
        administrationAPI.listUsers(),
      ])
      setDomains(nextDomains)
      setUsers(nextUsers)
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

  const platformAdministrators = users.filter(user => user.platformAdministrator)
  const domainAdministratorCount = useMemo(
    () => new Set(domains.flatMap(domain => domain.administrators.map(item => item.id))).size,
    [domains],
  )

  const setPlatformAdministrator = async (user: AdminUser) => {
    const enabled = !user.platformAdministrator
    setBusyKey(`platform:${user.id}`)
    setError('')
    setNotice('')
    try {
      await administrationAPI.setPlatformAdministrator(user.id, enabled)
      setUsers(current => current.map(item => item.id === user.id
        ? { ...item, platformAdministrator: enabled }
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

  const createDomain = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const input = {
      code: String(form.get('code') ?? '').trim().toLowerCase(),
      name: String(form.get('name') ?? '').trim(),
      description: String(form.get('description') ?? '').trim(),
      administratorUserIds: form.getAll('administratorUserIds').map(String),
    }
    if (!input.code || !input.name || input.administratorUserIds.length === 0) {
      setError('请填写领域名称、编码并至少指定一位领域管理员')
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

  const savePlatformAdministrators = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const selectedIDs = new Set(new FormData(event.currentTarget).getAll('userIds').map(String))
    if (selectedIDs.size === 0) {
      setError('平台至少需要保留一位管理员')
      return
    }
    const currentIDs = new Set(platformAdministrators.map(user => user.id))
    const additions = users.filter(user => selectedIDs.has(user.id) && !currentIDs.has(user.id))
    const removals = platformAdministrators.filter(user => !selectedIDs.has(user.id))
    setBusyKey('platform-administrators')
    setError('')
    try {
      for (const user of additions) {
        await administrationAPI.setPlatformAdministrator(user.id, true)
      }
      for (const user of removals) {
        await administrationAPI.setPlatformAdministrator(user.id, false)
      }
      await load()
      setDialog(null)
      setNotice('平台管理员已更新')
    } catch (cause) {
      await load()
      setError(cause instanceof Error ? cause.message : '平台管理员更新失败')
    } finally {
      setBusyKey('')
    }
  }

  const saveDomainAdministrators = async (
    event: FormEvent<HTMLFormElement>,
    domain: BusinessDomain,
  ) => {
    event.preventDefault()
    const userIDs = new FormData(event.currentTarget).getAll('userIds').map(String)
    if (userIDs.length === 0) {
      setError('每个领域至少需要一位领域管理员')
      return
    }
    setBusyKey(`domain-admins:${domain.id}`)
    setError('')
    try {
      await administrationAPI.replaceDomainAdministrators(domain.id, userIDs)
      await load()
      notifyDomainCatalogChanged()
      setDialog(null)
      setNotice(`领域“${domain.name}”的管理员已更新`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '领域管理员更新失败')
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
    user.domains
      .filter(domain => domain.memberRole === 'DOMAIN_ADMIN')
      .forEach(domain => selectedIDs.add(domain.id))
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
      setNotice(`${user.displayName}的领域归属已更新`)
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
      title="权限设定"
      eyebrow="访问控制"
      actions={view !== 'users' ? <button className="primary-button" type="button" onClick={() => {
        setError('')
        setNotice('')
        setDialog(view === 'domains' ? { kind: 'create-domain' } : { kind: 'platform-administrators' })
      }}><Plus size={17} weight="bold" />{view === 'domains' ? '新建领域' : '添加管理员'}</button> : undefined}
      className="administration-shell"
    >
      <section className="administration-stack">
        <div className="administration-hero governance-hero">
          <div>
            <span className="eyebrow">FIXED ACCESS MODEL</span>
            <h2>权限由组织层级决定</h2>
            <p>平台负责治理，领域负责发布，用户负责配置。能力随身份固定，不再创建角色或逐项勾选权限。</p>
            <div className="governance-path" aria-label="平台到用户的权限层级">
              <span><Crown size={15} />平台</span><ArrowRight size={14} />
              <span><GlobeHemisphereWest size={15} />领域</span><ArrowRight size={14} />
              <span><UserCircle size={15} />用户</span>
            </div>
          </div>
          <ShieldCheck size={62} weight="duotone" aria-hidden="true" />
        </div>

        <div className="administration-metrics" aria-label="权限设定概览">
          <article><Crown size={20} weight="duotone" /><span>平台管理员</span><strong>{platformAdministrators.length}</strong><small>负责平台控制面</small></article>
          <article><GlobeHemisphereWest size={20} weight="duotone" /><span>业务领域</span><strong>{domains.length}</strong><small>{domainAdministratorCount} 位领域管理员</small></article>
          <article><UsersThree size={20} weight="duotone" /><span>平台用户</span><strong>{users.length}</strong><small>按领域归属获得固定能力</small></article>
        </div>

        {(error || notice) && <div className={`administration-feedback ${error ? 'error' : 'success'}`} role={error ? 'alert' : 'status'}>
          {error || notice}
          <button type="button" aria-label="关闭提示" onClick={() => { setError(''); setNotice('') }}><X size={15} /></button>
        </div>}

        <div className="administration-workspace governance-workspace">
          <nav className="administration-tabs" aria-label="权限设定层级">
            <GovernanceTab active={view === 'platform'} icon={<Crown size={19} />} title="平台" note="平台身份与治理边界" onClick={() => setView('platform')} />
            <GovernanceTab active={view === 'domains'} icon={<GlobeHemisphereWest size={19} />} title="领域" note="领域管理员与生命周期" onClick={() => setView('domains')} />
            <GovernanceTab active={view === 'users'} icon={<UserCircle size={19} />} title="用户" note="用户状态与领域归属" onClick={() => setView('users')} />
            <div className="administration-security-note">
              <LockKey size={17} weight="duotone" />
              <span><strong>固定权限</strong><small>页面只调整身份与归属，不开放权限能力组合。</small></span>
            </div>
          </nav>

          <section className="administration-panel">
            {loading
              ? <div className="administration-empty" role="status"><SpinnerGap className="spin" size={32} /><strong>正在加载权限数据…</strong></div>
              : error && users.length === 0
                ? <div className="administration-empty"><LockKey size={34} /><strong>无法进入权限设定</strong><p>该页面仅对平台管理员开放。</p><button className="quiet-button" type="button" onClick={() => void load()}>重新加载</button></div>
                : view === 'platform'
                  ? <PlatformGovernance users={users} busyKey={busyKey} onToggle={user => void setPlatformAdministrator(user)} />
                  : view === 'domains'
                    ? <DomainGovernance domains={domains} users={users} busyKey={busyKey} onEdit={domain => setDialog({ kind: 'domain-admins', domain })} onStatus={domain => void updateDomainStatus(domain)} />
                    : <UserGovernance users={users} onEdit={user => setDialog({ kind: 'user-domains', user })} />}
          </section>
        </div>
      </section>

      {dialog?.kind === 'create-domain' && <DomainDialog
        title="新建业务领域"
        description="领域创建后自动启用，必须指定至少一位管理员。"
        users={users}
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
        onSubmit={event => void savePlatformAdministrators(event)}
      />}
      {dialog?.kind === 'domain-admins' && <AdministratorDialog
        domain={dialog.domain}
        users={users}
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={event => void saveDomainAdministrators(event, dialog.domain)}
      />}
      {dialog?.kind === 'user-domains' && <UserDomainDialog
        user={dialog.user}
        domains={domains}
        busy={Boolean(busyKey)}
        error={error}
        onClose={closeDialog}
        onSubmit={event => void saveUserDomains(event, dialog.user)}
      />}
    </AppShell>
  )
}

function GovernanceTab({ active, icon, title, note, onClick }: {
  active: boolean
  icon: React.ReactNode
  title: string
  note: string
  onClick: () => void
}) {
  return <button type="button" className={active ? 'active' : ''} onClick={onClick}>
    {icon}<span><strong>{title}</strong><small>{note}</small></span>
  </button>
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

function PlatformGovernance({ users, busyKey, onToggle }: {
  users: AdminUser[]
  busyKey: string
  onToggle: (user: AdminUser) => void
}) {
  return <div className="administration-view">
    <FixedScope level="PLATFORM" title="平台管理员" description="只管理平台控制面，不自动获得任意领域的数据访问权。" capabilities={fixedCapabilities.platform} />
    <header className="administration-view-heading"><div><span className="eyebrow">PLATFORM IDENTITY</span><h2>平台身份</h2><p>指定少量平台管理员；平台始终至少保留一位管理员。</p></div></header>
    <div className="governance-user-list">
      {users.map(user => <article key={user.id}>
        <UserIdentity user={user} />
        <span className={`identity-badge ${user.platformAdministrator ? 'platform' : ''}`}>
          {user.platformAdministrator ? <><Crown size={13} weight="fill" />平台管理员</> : '普通用户'}
        </span>
        <button
          className={user.platformAdministrator ? 'quiet-button danger-text' : 'quiet-button'}
          type="button"
          disabled={Boolean(busyKey) || user.status !== 'ACTIVE'}
          onClick={() => onToggle(user)}
        >
          {busyKey === `platform:${user.id}` && <SpinnerGap className="spin" size={14} />}
          {user.platformAdministrator ? '移出平台管理' : '设为平台管理员'}
        </button>
      </article>)}
    </div>
  </div>
}

function DomainGovernance({ domains, users, busyKey, onEdit, onStatus }: {
  domains: BusinessDomain[]
  users: AdminUser[]
  busyKey: string
  onEdit: (domain: BusinessDomain) => void
  onStatus: (domain: BusinessDomain) => void
}) {
  return <div className="administration-view">
    <FixedScope level="DOMAIN" title="领域管理员" description="负责本领域的数据配置、发布审批与成员准入，权限不会跨领域。" capabilities={fixedCapabilities.domain} />
    <header className="administration-view-heading"><div><span className="eyebrow">DOMAIN GOVERNANCE</span><h2>领域管理</h2><p>一个领域可有多位管理员，但不能没有管理员。</p></div></header>
    <div className="domain-governance-list">
      {domains.map(domain => {
        const memberCount = users.filter(user => user.domains.some(item => item.id === domain.id)).length
        return <article key={domain.id}>
          <div className="domain-management-avatar">{domain.name.slice(0, 1)}</div>
          <div className="domain-governance-name"><strong>{domain.name}</strong><small>{domain.code}{domain.default ? ' · 默认领域' : ''}</small></div>
          <div className="domain-governance-admins"><small>领域管理员</small><strong>{domain.administrators.map(item => item.displayName).join('、') || '未设置'}</strong></div>
          <div className="domain-governance-count"><strong>{memberCount}</strong><small>位成员</small></div>
          <span className={`domain-status ${domain.status.toLowerCase()}`}>{domain.status === 'ACTIVE' ? '已启用' : '已停用'}</span>
          <div className="domain-governance-actions">
            <button className="quiet-button" type="button" disabled={Boolean(busyKey)} onClick={() => onEdit(domain)}>设置管理员</button>
            <button className="quiet-button" type="button" disabled={Boolean(busyKey) || domain.default} onClick={() => onStatus(domain)}>
              {busyKey === `domain-status:${domain.id}` && <SpinnerGap className="spin" size={13} />}
              {domain.status === 'ACTIVE' ? '停用' : '启用'}
            </button>
          </div>
        </article>
      })}
    </div>
  </div>
}

function UserGovernance({ users, onEdit }: {
  users: AdminUser[]
  onEdit: (user: AdminUser) => void
}) {
  return <div className="administration-view">
    <FixedScope level="USER" title="领域用户" description="用户加入领域后获得统一的数据配置能力；发布操作由领域管理员完成。" capabilities={fixedCapabilities.user} />
    <header className="administration-view-heading"><div><span className="eyebrow">USER MEMBERSHIP</span><h2>用户归属</h2><p>用户可以加入多个领域，每个领域的数据边界相互隔离。</p></div></header>
    <div className="governance-user-list user-membership-list">
      {users.map(user => <article key={user.id}>
        <UserIdentity user={user} />
        <div className="user-domain-list">
          {user.domains.length > 0
            ? user.domains.map(domain => <span className={domain.memberRole === 'DOMAIN_ADMIN' ? 'administrator' : ''} key={domain.id}>
              {domain.memberRole === 'DOMAIN_ADMIN' && <Crown size={11} weight="fill" />}{domain.name}
            </span>)
            : <small>暂未加入领域</small>}
        </div>
        <button className="quiet-button" type="button" disabled={user.status !== 'ACTIVE'} onClick={() => onEdit(user)}>调整领域</button>
      </article>)}
    </div>
  </div>
}

function UserIdentity({ user }: { user: AdminUser }) {
  return <div className="member-identity">
    <span>{user.displayName.slice(0, 1)}</span>
    <div><strong>{user.displayName}</strong><small>{user.email}</small></div>
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

function DomainDialog({ title, description, users, busy, error, onClose, onSubmit }: {
  title: string
  description: string
  users: AdminUser[]
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
      <SelectionList label="领域管理员" name="administratorUserIds" users={users} />
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <CheckCircle size={16} />}{busy ? '正在创建…' : '创建领域'}</button></footer>
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
  const selected = new Set(users.filter(user => user.platformAdministrator).map(user => user.id))
  return <DialogFrame
    title="选择平台管理员"
    description="管理员只能从已完成注册的用户中选择；平台始终至少保留一位管理员。"
    busy={busy}
    error={error}
    onClose={onClose}
  >
    <form onSubmit={onSubmit}>
      <SelectionList label="已注册用户" name="userIds" users={users} selected={selected} />
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <Check size={16} />}{busy ? '保存中…' : '保存管理员'}</button></footer>
    </form>
  </DialogFrame>
}

function AdministratorDialog({ domain, users, busy, error, onClose, onSubmit }: {
  domain: BusinessDomain
  users: AdminUser[]
  busy: boolean
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  const selected = new Set(domain.administrators.map(item => item.id))
  return <DialogFrame title={`设置“${domain.name}”管理员`} description="领域管理员拥有本领域固定管理权限。" busy={busy} error={error} onClose={onClose}>
    <form onSubmit={onSubmit}>
      <SelectionList label="领域管理员" name="userIds" users={users} selected={selected} />
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <Check size={16} />}{busy ? '保存中…' : '保存管理员'}</button></footer>
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
  const memberships = new Map(user.domains.map(domain => [domain.id, domain]))
  return <DialogFrame title={`调整${user.displayName}的领域`} description="这里只调整领域归属，不改变任何权限能力。" busy={busy} error={error} onClose={onClose}>
    <form onSubmit={onSubmit}>
      <fieldset className="governance-selection"><legend>可加入领域</legend>
        {domains.map(domain => {
          const membership = memberships.get(domain.id)
          const administrator = membership?.memberRole === 'DOMAIN_ADMIN'
          return <label key={domain.id} className={administrator ? 'locked' : ''}>
            <input type="checkbox" name="domainIds" value={domain.id} defaultChecked={Boolean(membership)} disabled={administrator || domain.status !== 'ACTIVE'} />
            <span className="selection-check"><Check size={12} weight="bold" /></span>
            <span><strong>{domain.name}</strong><small>{administrator ? '领域管理员需先更换管理员后移除' : domain.description || domain.code}</small></span>
          </label>
        })}
      </fieldset>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="submit" disabled={busy}>{busy ? <SpinnerGap className="spin" size={16} /> : <Check size={16} />}{busy ? '保存中…' : '保存归属'}</button></footer>
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
      <span><strong>{user.displayName}</strong><small>{user.email}</small></span>
    </label>)}
  </fieldset>
}
