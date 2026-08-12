import {
  ArrowClockwise,
  CheckCircle,
  ClockCounterClockwise,
  CloudArrowUp,
  Code,
  Database,
  GearSix,
  ChartLineUp,
  ListChecks,
  Lifebuoy,
  LockKey,
  Plus,
  RocketLaunch,
  ShieldCheck,
  SpinnerGap,
  WarningCircle,
  X,
} from '@phosphor-icons/react'
import { FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { NavLink, useSearchParams } from 'react-router-dom'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import { administrationAPI, type BusinessDomain } from '../lib/administration'
import { currentSubject, currentTenantID } from '../lib/auth'
import { RequestError } from '../lib/api'
import {
  runtimeConfigAPI,
  type DeploymentParameter,
  type RuntimeConfigDefinition,
  type RuntimeConfigRolloutNode,
  type RuntimeConfigScope,
  type RuntimeConfigState,
  type RuntimeConfigVersion,
} from '../lib/runtime-config'

const stateLabel: Record<RuntimeConfigState, string> = {
  DRAFT: '草稿', IN_REVIEW: '待审批', APPROVED: '已批准', ROLLING_OUT: '下发中', ACTIVE: '已生效',
  SUPERSEDED: '已替代', REJECTED: '已拒绝', FAILED: '下发失败', ROLLED_BACK: '已回滚',
}
const scopeLabel: Record<RuntimeConfigScope, string> = { TENANT: '全租户', DOMAIN: '业务领域', WORKER: '运行节点' }
const keyLabels: Record<string, { name: string; description: string }> = {
  'domain.askdataEnabled': { name: '启用问数工作台', description: '控制指定领域是否开放自然语言问数入口。' },
  'budget.dailyRuns': { name: '每日治理运行额度', description: '限制租户或领域每天可执行的受治理分析次数。' },
  'degradation.narrativeEnabled': { name: '启用可信叙事生成', description: '允许基于已验证结果生成解释与摘要。' },
  'worker.maxConcurrentJobs': { name: '节点最大并发任务', description: '限制单个工作节点并行处理的后台任务数。' },
  'provider.routingMode': { name: '模型服务路由策略', description: '配置多服务提供方之间的流量路由方式。' },
}
const workerOptions = [
  { id: 'API', name: 'API 服务' },
  { id: 'ASKDATA_WORKER', name: '问数执行节点' },
  { id: 'REPORT_WORKER', name: '报告执行节点' },
]

function formatTime(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value))
}

function scopedName(item: RuntimeConfigVersion, domains: BusinessDomain[]) {
  if (item.scopeType === 'TENANT') return '全租户'
  if (item.scopeType === 'WORKER') return workerOptions.find(value => value.id === item.scopeId)?.name ?? item.scopeId
  return domains.find(value => value.id === item.scopeId)?.name ?? '已移除领域'
}

function readableError(cause: unknown, fallback: string) {
  if (cause instanceof RequestError && cause.status === 409) return '配置已被其他管理员更新，页面已同步最新版本，请重新确认后操作。'
  return cause instanceof Error ? cause.message : fallback
}

/** 将服务端已有的配置版本、双人审批、灰度下发和回滚能力落成完整控制面。 */
export function RuntimeConfigPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [definitions, setDefinitions] = useState<RuntimeConfigDefinition[]>([])
  const [parameters, setParameters] = useState<DeploymentParameter[]>([])
  const [versions, setVersions] = useState<RuntimeConfigVersion[]>([])
  const [domains, setDomains] = useState<BusinessDomain[]>([])
  const [selected, setSelected] = useState<RuntimeConfigVersion | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [dialog, setDialog] = useState<'create' | 'reject' | null>(null)
  const actorId = currentSubject()
  const selectedIdRef = useRef('')

  const load = useCallback(async (preferredId = searchParams.get('versionId') ?? '') => {
    setLoading(true)
    setError('')
    try {
      const [nextDefinitions, nextParameters, nextVersions, nextDomains] = await Promise.all([
        runtimeConfigAPI.definitions(), runtimeConfigAPI.deploymentParameters(), runtimeConfigAPI.list(), administrationAPI.listManagedDomains(),
      ])
      setDefinitions(nextDefinitions)
      setParameters(nextParameters)
      setVersions(nextVersions)
      setDomains(nextDomains)
      const targetId = preferredId || selectedIdRef.current || nextVersions[0]?.id || ''
      const summary = nextVersions.find(item => item.id === targetId) ?? nextVersions[0] ?? null
      if (summary) {
        const detail = await runtimeConfigAPI.get(summary.id)
        setSelected(detail)
        selectedIdRef.current = detail.id
        setSearchParams(current => {
          const next = new URLSearchParams(current)
          next.set('versionId', detail.id)
          return next
        }, { replace: true })
      } else {
        setSelected(null)
      }
    } catch (cause) {
      setError(readableError(cause, '运行配置加载失败'))
    } finally {
      setLoading(false)
    }
  }, [searchParams, setSearchParams])

  useEffect(() => {
    const timer = window.setTimeout(() => { void load() }, 0)
    return () => window.clearTimeout(timer)
    // 初次进入后由显式刷新维护选中态，避免 query 对象变化触发重复请求。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const choose = async (item: RuntimeConfigVersion) => {
    setBusy(`open:${item.id}`)
    setError('')
    try {
      const detail = await runtimeConfigAPI.get(item.id)
      setSelected(detail)
      selectedIdRef.current = detail.id
      setSearchParams({ versionId: detail.id }, { replace: true })
    } catch (cause) {
      setError(readableError(cause, '配置详情加载失败'))
    } finally { setBusy('') }
  }

  const runTransition = async (operation: 'submit' | 'approve' | 'apply' | 'rollback') => {
    if (!selected) return
    setBusy(operation)
    setError('')
    setNotice('')
    try {
      const next = await runtimeConfigAPI.transition(selected.id, operation, selected.recordVersion)
      setSelected(next)
      setNotice(operation === 'submit' ? '配置已提交审批' : operation === 'approve' ? '配置已批准，可开始安全下发' : operation === 'apply' ? '配置已进入灰度下发队列' : '已回滚至上一生效版本')
      await load(next.id)
    } catch (cause) {
      setError(readableError(cause, '配置状态更新失败'))
      if (cause instanceof RequestError && cause.status === 409) await load(selected.id)
    } finally { setBusy('') }
  }

  const reject = async (reason: string) => {
    if (!selected) return
    setBusy('reject')
    setError('')
    try {
      const next = await runtimeConfigAPI.reject(selected.id, selected.recordVersion, reason)
      setDialog(null)
      setNotice('配置已拒绝并保留完整审计记录')
      await load(next.id)
    } catch (cause) {
      setError(readableError(cause, '拒绝配置失败'))
      if (cause instanceof RequestError && cause.status === 409) await load(selected.id)
    } finally { setBusy('') }
  }

  const acknowledgeRestart = async (node: RuntimeConfigRolloutNode) => {
    if (!selected) return
    setBusy(`restart:${node.id}`)
    setError('')
    try {
      const next = await runtimeConfigAPI.acknowledgeRestart(selected.id, node.id)
      setSelected(next)
      setNotice(`${node.consumerType} 已确认重启并载入新配置`)
      await load(next.id)
    } catch (cause) {
      setError(readableError(cause, '重启确认失败'))
      if (cause instanceof RequestError && cause.status === 409) await load(selected.id)
    } finally { setBusy('') }
  }

  const activeCount = versions.filter(item => item.state === 'ACTIVE').length
  const reviewCount = versions.filter(item => item.state === 'IN_REVIEW').length
  const rolloutCount = versions.filter(item => item.state === 'ROLLING_OUT').length
  const parameterHealth = parameters.filter(item => item.configured).length

  return <AppShell
    title="运行配置中心"
    eyebrow="平台控制面"
    className="administration-shell runtime-config-shell"
    controlPlane
    actions={<><AppButton type="button" disabled={loading || Boolean(busy)} onClick={() => void load()}><ArrowClockwise className={loading ? 'spin' : ''} size={17} />刷新</AppButton><AppButton variant="primary" type="button" onClick={() => setDialog('create')}><Plus size={17} />新建配置版本</AppButton></>}
  >
    <section className="runtime-config-page">
      <header className="runtime-config-overview">
        <div><span className="eyebrow">RUNTIME GOVERNANCE</span><h2>让每一次线上配置变更都可审、可追溯、可回滚</h2><p>在线参数与部署密钥分区管理；配置提交后需由另一位平台管理员批准，再按节点顺序安全下发。</p></div>
        <div className="runtime-config-metrics">
          <article><CheckCircle size={20} /><span>生效版本</span><strong>{activeCount}</strong></article>
          <article className={reviewCount ? 'attention' : ''}><ListChecks size={20} /><span>待审批</span><strong>{reviewCount}</strong></article>
          <article><RocketLaunch size={20} /><span>下发中</span><strong>{rolloutCount}</strong></article>
          <article className={parameterHealth < parameters.length ? 'attention' : ''}><LockKey size={20} /><span>部署参数就绪</span><strong>{parameterHealth}/{parameters.length}</strong></article>
        </div>
      </header>

      <nav className="platform-top-navigation runtime-config-navigation" aria-label="平台管理模块">
        <NavLink to="/platform-management/domains"><Database size={18} /><span><strong>领域管理</strong><small>新建与停用</small></span></NavLink>
        <NavLink to="/platform-management/permissions"><ShieldCheck size={18} /><span><strong>权限管理</strong><small>管理员与用户</small></span></NavLink>
        <NavLink to="/platform-management/approvals"><ListChecks size={18} /><span><strong>审批中心</strong><small>统一治理队列</small></span></NavLink>
        <NavLink to="/platform-management/tasks"><CloudArrowUp size={18} /><span><strong>后台任务</strong><small>运行与重试</small></span></NavLink>
        <NavLink to="/platform-management/observability"><ChartLineUp size={18} /><span><strong>运行观测</strong><small>健康与配额</small></span></NavLink>
        <NavLink to="/platform-management/support"><Lifebuoy size={18} /><span><strong>支持工单</strong><small>问题跟进</small></span></NavLink>
        <NavLink to="/platform-management/logs"><Code size={18} /><span><strong>平台日志</strong><small>不可变轨迹</small></span></NavLink>
        <NavLink to="/platform-management/runtime-config"><GearSix size={18} /><span><strong>运行配置</strong><small>版本与回滚</small></span></NavLink>
      </nav>

      {(error || notice) && <div className={`administration-feedback ${error ? 'error' : 'success'}`} role={error ? 'alert' : 'status'}>{error || notice}<AppButton text circle type="button" aria-label="关闭提示" onClick={() => { setError(''); setNotice('') }}><X size={15} /></AppButton></div>}

      <div className="runtime-config-workspace">
        <aside className="runtime-config-versions">
          <header><div><strong>配置版本</strong><span>共 {versions.length} 个</span></div></header>
          <div>
            {versions.map(item => <AppButton text type="button" className={selected?.id === item.id ? 'active' : ''} onClick={() => void choose(item)} key={item.id}>
              <span className={`runtime-config-state is-${item.state.toLowerCase()}`}>{stateLabel[item.state]}</span>
              <span><strong>{scopedName(item, domains)} · V{item.versionNo}</strong><small>{scopeLabel[item.scopeType]} · {formatTime(item.updatedAt)}</small><em>{item.impactSummary || '未填写影响说明'}</em></span>
              {busy === `open:${item.id}` && <SpinnerGap className="spin" size={15} />}
            </AppButton>)}
            {!loading && versions.length === 0 && <div className="runtime-config-empty"><GearSix size={28} /><strong>还没有配置版本</strong><span>新建首个版本后即可进入审批与下发流程。</span></div>}
          </div>
        </aside>

        <main className="runtime-config-detail">
          {loading && !selected ? <div className="runtime-config-empty"><SpinnerGap className="spin" size={30} /><strong>正在读取运行配置…</strong></div>
            : selected ? <>
              <header className="runtime-config-detail-header">
                <div><div><span className={`runtime-config-state is-${selected.state.toLowerCase()}`}>{stateLabel[selected.state]}</span><small>{scopeLabel[selected.scopeType]}</small></div><h2>{scopedName(selected, domains)} · 配置版本 V{selected.versionNo}</h2><p>{selected.impactSummary || '本版本未填写影响说明'}</p></div>
                <div className="runtime-config-actions">
                  {selected.state === 'DRAFT' && <AppButton variant="primary" disabled={Boolean(busy)} onClick={() => void runTransition('submit')}><CloudArrowUp size={16} />提交审批</AppButton>}
                  {selected.state === 'IN_REVIEW' && <><AppButton variant="danger" plain disabled={Boolean(busy) || selected.createdBy === actorId} title={selected.createdBy === actorId ? '发起人不能审批自己的配置' : undefined} onClick={() => setDialog('reject')}>拒绝</AppButton><AppButton variant="primary" disabled={Boolean(busy) || selected.createdBy === actorId} title={selected.createdBy === actorId ? '需由另一位平台管理员批准' : undefined} onClick={() => void runTransition('approve')}><ShieldCheck size={16} />批准</AppButton></>}
                  {selected.state === 'APPROVED' && <AppButton variant="primary" disabled={Boolean(busy)} onClick={() => void runTransition('apply')}><RocketLaunch size={16} />开始下发</AppButton>}
                  {selected.state === 'ACTIVE' && selected.baseVersionId && <AppButton variant="warning" plain disabled={Boolean(busy)} onClick={() => void runTransition('rollback')}><ClockCounterClockwise size={16} />回滚上一版本</AppButton>}
                </div>
              </header>

              {selected.state === 'IN_REVIEW' && selected.createdBy === actorId && <div className="runtime-config-policy-note"><ShieldCheck size={18} /><span><strong>等待另一位管理员审批</strong><small>平台执行职责分离：配置发起人不能批准或拒绝自己的变更。</small></span></div>}

              <section className="runtime-config-section"><header><div><strong>配置内容</strong><span>{selected.compatibility === 'HOT_RELOAD' ? '支持在线热更新' : '应用后需重启节点'}</span></div><code>{selected.configHash.slice(0, 12)}</code></header><div className="runtime-config-values">
                {Object.entries(selected.config).map(([key, value]) => <article key={key}><span><strong>{keyLabels[key]?.name ?? key}</strong><small>{key}</small></span><em>{typeof value === 'boolean' ? value ? '已启用' : '已停用' : String(value)}</em></article>)}
              </div></section>

              <section className="runtime-config-section runtime-config-timeline"><header><div><strong>版本轨迹</strong><span>记录版本锁与关键治理时间</span></div><small>记录版本 {selected.recordVersion}</small></header><div>
                <span><i /><strong>创建草稿</strong><small>{formatTime(selected.createdAt)}</small></span>
                <span className={selected.submittedAt ? 'done' : ''}><i /><strong>提交审批</strong><small>{formatTime(selected.submittedAt)}</small></span>
                <span className={selected.approvedAt ? 'done' : selected.rejectedAt ? 'failed' : ''}><i /><strong>{selected.rejectedAt ? '审批拒绝' : '审批通过'}</strong><small>{formatTime(selected.approvedAt ?? selected.rejectedAt)}</small></span>
                <span className={selected.activatedAt ? 'done' : ''}><i /><strong>配置生效</strong><small>{formatTime(selected.activatedAt)}</small></span>
              </div>{selected.rejectionReason && <p><WarningCircle size={15} />拒绝原因：{selected.rejectionReason}</p>}</section>

              {Boolean(selected.rolloutNodes?.length) && <section className="runtime-config-section"><header><div><strong>下发节点</strong><span>节点按顺序完成验证与应用</span></div></header><div className="runtime-config-nodes">
                {selected.rolloutNodes?.map(node => <article key={node.id}><span className={`is-${node.state.toLowerCase()}`}>{node.state === 'APPLIED' ? <CheckCircle size={18} /> : node.state === 'FAILED' ? <WarningCircle size={18} /> : <CloudArrowUp size={18} />}</span><div><strong>{node.consumerType}</strong><small>第 {node.ordinal} 阶段 · 尝试 {node.attempt} 次{node.failureCode ? ` · ${node.failureCode}` : ''}</small></div><em>{node.state === 'APPLIED' ? '已应用' : node.state === 'WAITING_RESTART' ? '等待重启' : node.state === 'PENDING' ? '等待下发' : node.state === 'FAILED' ? '应用失败' : '已取消'}</em>{node.state === 'WAITING_RESTART' && <AppButton size="small" disabled={Boolean(busy)} onClick={() => void acknowledgeRestart(node)}>{busy === `restart:${node.id}` ? <SpinnerGap className="spin" size={14} /> : <ArrowClockwise size={14} />}确认已重启</AppButton>}</article>)}
              </div></section>}
            </> : <div className="runtime-config-empty"><GearSix size={30} /><strong>选择或新建一个配置版本</strong><span>右侧将展示审批轨迹、配置差异和下发节点。</span></div>}
        </main>
      </div>

      <section className="runtime-config-deployment"><header><div><strong>部署参数与密钥引用</strong><span>只显示配置状态，不在平台内读取或修改明文</span></div><LockKey size={20} /></header><div>
        {parameters.map(item => <article key={item.name}><span className={item.configured ? 'ready' : 'missing'}>{item.configured ? <CheckCircle size={18} /> : <WarningCircle size={18} />}</span><div><strong>{item.name}</strong><small>{item.category === 'SECRET_REFERENCE' ? '密钥引用' : '部署参数'} · {item.configured ? '已配置' : '未配置'}</small></div><p>{item.changeGuidance}</p></article>)}
      </div></section>
    </section>

    {dialog === 'create' && <CreateRuntimeConfigDialog definitions={definitions} domains={domains} versions={versions} onClose={() => setDialog(null)} onCreated={async value => { setDialog(null); setNotice('配置草稿已创建'); await load(value.id) }} />}
    {dialog === 'reject' && selected && <RejectRuntimeConfigDialog busy={busy === 'reject'} onClose={() => setDialog(null)} onSubmit={reason => void reject(reason)} />}
  </AppShell>
}

function CreateRuntimeConfigDialog({ definitions, domains, versions, onClose, onCreated }: { definitions: RuntimeConfigDefinition[]; domains: BusinessDomain[]; versions: RuntimeConfigVersion[]; onClose: () => void; onCreated: (value: RuntimeConfigVersion) => void | Promise<void> }) {
  const initialDefinition = definitions.find(item => item.scopeTypes.includes('DOMAIN'))
  const [scopeType, setScopeType] = useState<RuntimeConfigScope>('DOMAIN')
  const [scopeId, setScopeId] = useState(domains.find(item => item.default)?.id ?? domains[0]?.id ?? '')
  const [enabledKeys, setEnabledKeys] = useState<string[]>(initialDefinition ? [initialDefinition.key] : [])
  const [values, setValues] = useState<Record<string, boolean | number | string>>(initialDefinition ? {
    [initialDefinition.key]: initialDefinition.type === 'boolean' ? true : initialDefinition.type === 'integer' ? initialDefinition.minimum ?? 1 : initialDefinition.enum?.[0] ?? '',
  } : {})
  const [impactSummary, setImpactSummary] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const available = definitions.filter(item => item.scopeTypes.includes(scopeType))
  const base = versions.find(item => item.scopeType === scopeType && item.scopeId === scopeId && (item.state === 'ACTIVE' || item.state === 'SUPERSEDED'))

  const changeScope = (next: RuntimeConfigScope) => {
    setScopeType(next)
    setScopeId(next === 'TENANT' ? currentTenantID() : next === 'WORKER' ? workerOptions[0].id : domains.find(item => item.default)?.id ?? domains[0]?.id ?? '')
    const first = definitions.find(item => item.scopeTypes.includes(next))
    setEnabledKeys(first ? [first.key] : [])
    setValues(first ? { [first.key]: first.type === 'boolean' ? true : first.type === 'integer' ? first.minimum ?? 1 : first.enum?.[0] ?? '' } : {})
  }
  const toggleKey = (definition: RuntimeConfigDefinition) => {
    const enabled = enabledKeys.includes(definition.key)
    setEnabledKeys(current => enabled ? current.filter(key => key !== definition.key) : [...current, definition.key])
    if (!enabled) setValues(current => ({ ...current, [definition.key]: definition.type === 'boolean' ? true : definition.type === 'integer' ? definition.minimum ?? 1 : definition.enum?.[0] ?? '' }))
  }
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const config = Object.fromEntries(enabledKeys.map(key => [key, values[key]]))
    if (!scopeId || !enabledKeys.length || !impactSummary.trim()) return
    setBusy(true); setError('')
    try {
      const created = await runtimeConfigAPI.create({ scopeType, scopeId, baseVersionId: base?.id, config, impactSummary: impactSummary.trim() })
      await onCreated(created)
    } catch (cause) { setError(readableError(cause, '配置版本创建失败')) } finally { setBusy(false) }
  }
  return <div className="administration-dialog-backdrop"><section className="administration-dialog runtime-config-dialog" role="dialog" aria-modal="true" aria-labelledby="create-runtime-config-title"><header><div><span className="eyebrow">NEW CONFIGURATION</span><h2 id="create-runtime-config-title">新建运行配置版本</h2><p>选择作用范围与本次变更项，创建后进入双人审批流程。</p></div><AppButton text circle type="button" disabled={busy} aria-label="关闭" onClick={onClose}><X size={17} /></AppButton></header><form onSubmit={submit}>
    <fieldset className="runtime-config-scope"><legend>作用范围</legend>{(['DOMAIN', 'TENANT', 'WORKER'] as RuntimeConfigScope[]).map(scope => <label className={scopeType === scope ? 'active' : ''} key={scope}><input type="radio" name="scope" checked={scopeType === scope} onChange={() => changeScope(scope)} /><span><strong>{scopeLabel[scope]}</strong><small>{scope === 'DOMAIN' ? '仅影响一个业务领域' : scope === 'TENANT' ? '全租户共享策略' : '指定执行节点'}</small></span></label>)}</fieldset>
    <label><span>目标</span>{scopeType === 'DOMAIN' ? <select value={scopeId} onChange={event => setScopeId(event.target.value)}>{domains.filter(item => item.status === 'ACTIVE').map(item => <option value={item.id} key={item.id}>{item.name}</option>)}</select> : scopeType === 'WORKER' ? <select value={scopeId} onChange={event => setScopeId(event.target.value)}>{workerOptions.map(item => <option value={item.id} key={item.id}>{item.name}</option>)}</select> : <input value={scopeId} readOnly />}</label>
    <div className="runtime-config-fieldset"><span>配置项</span>{available.map(definition => { const enabled = enabledKeys.includes(definition.key); const label = keyLabels[definition.key]; return <article className={enabled ? 'active' : ''} key={definition.key}><label><input type="checkbox" checked={enabled} onChange={() => toggleKey(definition)} /><span><strong>{label?.name ?? definition.key}</strong><small>{label?.description ?? definition.description}</small></span></label>{enabled && <div>{definition.type === 'boolean' ? <select value={String(values[definition.key])} onChange={event => setValues(current => ({ ...current, [definition.key]: event.target.value === 'true' }))}><option value="true">启用</option><option value="false">停用</option></select> : definition.type === 'integer' ? <input type="number" min={definition.minimum} max={definition.maximum} value={Number(values[definition.key])} onChange={event => setValues(current => ({ ...current, [definition.key]: Number(event.target.value) }))} /> : <select value={String(values[definition.key])} onChange={event => setValues(current => ({ ...current, [definition.key]: event.target.value }))}>{definition.enum?.map(value => <option value={value} key={value}>{value === 'ROUND_ROBIN' ? '轮询分配' : value === 'PRIMARY_FAILOVER' ? '主服务故障切换' : value}</option>)}</select>}<small>{definition.compatibility === 'HOT_RELOAD' ? '在线热更新' : '需重启节点'}</small></div>}</article>})}</div>
    <label><span>影响说明</span><textarea maxLength={2000} value={impactSummary} onChange={event => setImpactSummary(event.target.value)} placeholder="说明变更原因、影响范围与验证方式" /></label>
    {base && <p className="runtime-config-base"><ClockCounterClockwise size={15} />基于当前 {stateLabel[base.state]} 版本 V{base.versionNo} 创建，可在生效后安全回滚。</p>}
    {error && <div className="administration-feedback error" role="alert">{error}</div>}
    <footer><AppButton type="button" disabled={busy} onClick={onClose}>取消</AppButton><AppButton variant="primary" type="submit" disabled={busy || !scopeId || !enabledKeys.length || !impactSummary.trim()}>{busy ? <SpinnerGap className="spin" size={15} /> : <Plus size={15} />}创建草稿</AppButton></footer>
  </form></section></div>
}

function RejectRuntimeConfigDialog({ busy, onClose, onSubmit }: { busy: boolean; onClose: () => void; onSubmit: (reason: string) => void }) {
  const [reason, setReason] = useState('')
  return <div className="administration-dialog-backdrop"><section className="administration-dialog runtime-config-reject" role="dialog" aria-modal="true" aria-labelledby="reject-runtime-config-title"><header><div><span className="eyebrow">REVIEW DECISION</span><h2 id="reject-runtime-config-title">拒绝配置变更</h2><p>请记录可执行的修改意见，发起人可据此创建新版本。</p></div><AppButton text circle type="button" disabled={busy} aria-label="关闭" onClick={onClose}><X size={17} /></AppButton></header><form onSubmit={event => { event.preventDefault(); if (reason.trim()) onSubmit(reason.trim()) }}><label><span>拒绝原因</span><textarea autoFocus minLength={1} maxLength={1000} value={reason} onChange={event => setReason(event.target.value)} placeholder="说明风险、缺失信息或需要调整的配置项" /></label><footer><AppButton type="button" disabled={busy} onClick={onClose}>取消</AppButton><AppButton variant="danger" type="submit" disabled={busy || !reason.trim()}>{busy && <SpinnerGap className="spin" size={15} />}确认拒绝</AppButton></footer></form></section></div>
}
