import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from 'react'
import { AppShell } from '../components/AppShell'
import { AssetManagementTabs } from '../components/AssetManagementTabs'
import { RequestError } from '../lib/api'
import {
  createDimensionRefreshIdempotencyKey,
  semanticGovernanceAPI,
  type Dimension,
  type DimensionFieldRole,
  type DimensionMember,
  type DimensionMemberAlias,
  type DimensionMetricCompatibility,
  type DimensionRefreshJob,
  type DimensionSurveyAcceptance,
  type DimensionSurveyCandidate,
  type DimensionSurveyStatus,
  type DimensionType,
  type MemberIndexPolicy,
  type UpdateDimensionSurveyCandidateInput,
} from '../lib/semantic-governance'

type GovernanceView = 'candidates' | 'dimensions'
type Notice = { tone: 'success' | 'error' | 'info'; message: string }
type CandidateDraft = Omit<UpdateDimensionSurveyCandidateInput, 'expectedVersion'>
type GovernanceDialog =
  | { kind: 'edit-candidate'; candidate: DimensionSurveyCandidate; draft: CandidateDraft }
  | { kind: 'reject-candidate'; candidate: DimensionSurveyCandidate; reason: string }
  | { kind: 'create-alias'; dimension: Dimension; member: DimensionMember; alias: string; aliasType: DimensionMemberAlias['aliasType'] }

const dimensionTypeLabels: Record<DimensionType, string> = {
  STANDARD: '标准',
  TIME: '时间',
  GEOGRAPHY: '地理',
  ORGANIZATION: '组织',
  PRODUCT: '产品',
  CUSTOMER: '客户',
  OTHER: '其他',
}
const dimensionTypes = Object.keys(dimensionTypeLabels) as DimensionType[]
const policyLabels: Record<MemberIndexPolicy, string> = {
  FULL: 'FULL · 完整成员索引',
  EXACT_ONLY: 'EXACT_ONLY · 仅精确解析',
  NONE: 'NONE · 禁用成员索引',
}
const policies = Object.keys(policyLabels) as MemberIndexPolicy[]
const candidateStatusLabels: Record<DimensionSurveyStatus, string> = {
  SUGGESTED: '待治理',
  ACCEPTED: '已接收',
  REJECTED: '已拒绝',
  STALE: '已失效',
}
const fieldRoleLabels: Record<DimensionFieldRole, string> = {
  DIMENSION: '维度',
  ATTRIBUTE: '属性',
  TIME: '时间',
  IDENTIFIER: '标识',
}
const aliasTypeLabels: Record<DimensionMemberAlias['aliasType'], string> = {
  CODE: '代码',
  BUSINESS: '业务名称',
  ABBREVIATION: '缩写',
  LEGACY: '历史编码',
  LLM: 'LLM 建议',
  USER: '人工别名',
}
const compatibilityStatusLabels: Record<DimensionMetricCompatibility['status'], string> = {
  PROPOSED: '待验证',
  VERIFIED: '已验证',
  REJECTED: '已拒绝',
}
const refreshStatusLabels: Record<DimensionRefreshJob['status'], string> = {
  QUEUED: '排队中',
  RUNNING: '刷新中',
  SUCCEEDED: '已成功',
  FAILED: '失败',
  SKIPPED: '已跳过',
}
const profileStatusLabels: Record<DimensionSurveyCandidate['profile']['status'], string> = {
  NOT_QUEUED: '未入队',
  QUEUED: '等待画像',
  RUNNING: '画像中',
  SUCCEEDED: '实测完成',
  SKIPPED_POLICY: '策略短路',
  FAILED: '画像失败',
  STALE: '画像已失效',
}

const policyRank: Record<MemberIndexPolicy, number> = { FULL: 1, EXACT_ONLY: 2, NONE: 3 }
const shortID = (value: string) => value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value
const formatCount = (value: number | undefined) => value === undefined ? '—' : new Intl.NumberFormat('zh-CN').format(value)
const formatPercent = (value: number | undefined) =>
  value === undefined ? '—' : new Intl.NumberFormat('zh-CN', {
    style: 'percent',
    maximumFractionDigits: 2,
  }).format(value)
const formatDate = (value?: string) => {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

const errorMessage = (cause: unknown) => {
  if (cause instanceof RequestError && cause.status === 409) {
    return '资源已被其他治理操作更新，请重新加载后再试。'
  }
  return cause instanceof Error ? cause.message : '语义治理服务暂不可用，请稍后重试。'
}

const stricterPolicy = (
  left: MemberIndexPolicy,
  right: MemberIndexPolicy,
): MemberIndexPolicy => policyRank[left] >= policyRank[right] ? left : right

const candidatePolicyFloor = (candidate: DimensionSurveyCandidate): MemberIndexPolicy => {
  let floor = candidate.proposedMemberIndexPolicy
  if (candidate.proposedSensitive || candidate.riskSensitive) {
    floor = 'NONE'
  } else if (candidate.proposedHighCardinality || candidate.riskHighCardinality) {
    floor = stricterPolicy(floor, 'EXACT_ONLY')
  }

  const profile = candidate.profile
  if (profile.status === 'SUCCEEDED' && profile.recommendedMemberIndexPolicy) {
    return stricterPolicy(floor, profile.recommendedMemberIndexPolicy)
  }
  if (
    profile.status === 'SKIPPED_POLICY' &&
    profile.resultCode === 'IDENTIFIER_FIELD_PROFILE_SKIPPED' &&
    profile.recommendedMemberIndexPolicy === 'EXACT_ONLY'
  ) {
    return stricterPolicy(floor, 'EXACT_ONLY')
  }
  return 'NONE'
}

const candidatePolicyBlockReason = (candidate: DimensionSurveyCandidate) => {
  if (
    policyRank[candidate.proposedMemberIndexPolicy] >=
    policyRank[candidatePolicyFloor(candidate)]
  ) return ''
  if (candidate.profile.status === 'QUEUED' || candidate.profile.status === 'RUNNING') {
    return '当前画像尚未完成，只能以 NONE 安全接收。'
  }
  if (candidate.profile.status === 'FAILED' || candidate.profile.status === 'STALE') {
    return '当前画像失败或已失效，只能以 NONE 安全接收。'
  }
  return '当前画像或风险结论不允许该成员策略，请先编辑并收紧。'
}

const candidateDraft = (candidate: DimensionSurveyCandidate): CandidateDraft => ({
  code: candidate.proposedCode,
  name: candidate.proposedName,
  description: candidate.proposedDescription,
  dimensionType: candidate.proposedDimensionType,
  memberIndexPolicy: candidatePolicyFloor(candidate),
  highCardinality: candidate.proposedHighCardinality,
  sensitive: candidate.proposedSensitive,
})

const acceptedNextStep = (result: DimensionSurveyAcceptance) => {
  if (result.memberSearchReady) {
    return '正式维度已创建，成员索引已有可用快照；仍需验证维度—指标兼容关系，值检索才会返回指标。'
  }
  switch (result.nextAction) {
    case 'WAIT_FOR_MEMBER_REFRESH':
      return '正式维度已创建，完整成员刷新已排队；当前倒排尚未就绪，请等待任务成功并验证维度—指标兼容关系。'
    case 'USE_EXACT_MATCH_ONLY':
      return '正式维度已创建，但不会自动枚举成员；当前仅保留精确解析策略，尚未形成可浏览的成员倒排。'
    case 'MEMBER_INDEX_DISABLED':
      return '正式维度已创建，但成员索引已禁用；该维度不会参与维度值检索。'
  }
}

const dimensionReadiness = (dimension: Dimension) => {
  if (dimension.status !== 'PUBLISHED') return '维度尚未发布，不参与维度值检索。'
  if (dimension.sensitive) return '敏感维度不会返回成员或别名，也不会进入成员语义检索。'
  if (dimension.memberIndexPolicy === 'NONE') return '成员索引已禁用，不参与维度值检索。'
  if (dimension.memberIndexPolicy === 'EXACT_ONLY') return '不自动枚举成员；等待受控的按需精确解析能力。'
  if (!dimension.memberRefreshedAt) return '还没有成功的成员快照，需要提交并等待 FULL 刷新完成。'
  return '成员快照已生成；还需至少一条 VERIFIED 且非 UNSAFE 的指标兼容关系。'
}

/** 将 DWS 字段候选、正式维度、成员索引和指标兼容验证串成可审计的治理闭环。 */
export function SemanticGovernancePage({ initialView = 'candidates' }: { initialView?: GovernanceView }) {
  const [view, setView] = useState<GovernanceView>(initialView)
  const [permissionsReady, setPermissionsReady] = useState(false)
  const [capabilities, setCapabilities] = useState({ read: false, manage: false })
  const [candidates, setCandidates] = useState<DimensionSurveyCandidate[]>([])
  const [candidateTotal, setCandidateTotal] = useState(0)
  const [candidateStatus, setCandidateStatus] = useState<DimensionSurveyStatus | ''>('SUGGESTED')
  const [candidateRole, setCandidateRole] = useState<DimensionFieldRole | ''>('')
  const [dimensions, setDimensions] = useState<Dimension[]>([])
  const [dimensionTotal, setDimensionTotal] = useState(0)
  const [dimensionQuery, setDimensionQuery] = useState('')
  const [dimensionStatus, setDimensionStatus] = useState<'' | 'DRAFT' | 'PUBLISHED' | 'DEPRECATED'>('PUBLISHED')
  const [candidateLoading, setCandidateLoading] = useState(true)
  const [dimensionLoading, setDimensionLoading] = useState(true)
  const [busyKey, setBusyKey] = useState('')
  const [loadError, setLoadError] = useState('')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [dialog, setDialog] = useState<GovernanceDialog | null>(null)
  const [selectedDimension, setSelectedDimension] = useState<Dimension | null>(null)
  const [members, setMembers] = useState<DimensionMember[]>([])
  const [memberTotal, setMemberTotal] = useState(0)
  const [aliases, setAliases] = useState<DimensionMemberAlias[]>([])
  const [refreshJobs, setRefreshJobs] = useState<DimensionRefreshJob[]>([])
  const [compatibilities, setCompatibilities] = useState<DimensionMetricCompatibility[]>([])

  const [detailLoading, setDetailLoading] = useState(false)
  const [memberQuery, setMemberQuery] = useState('')

  useEffect(() => {
    let active = true
    Promise.all([
      semanticGovernanceAPI.evaluatePermission('READ'),
      semanticGovernanceAPI.evaluatePermission('MANAGE'),
    ]).then(([read, manage]) => {
      if (active) setCapabilities({ read: read.allowed, manage: manage.allowed })
    }).catch(cause => {
      if (active) setLoadError(errorMessage(cause))
    }).finally(() => {
      if (active) setPermissionsReady(true)
    })
    return () => { active = false }
  }, [])

  const loadCandidates = useCallback(async () => {
    if (!permissionsReady || !capabilities.read) return
    await Promise.resolve()
    setCandidateLoading(true)
    try {
      const page = await semanticGovernanceAPI.listCandidates({
        status: candidateStatus,
        fieldRole: candidateRole,
      })
      setCandidates(page.items)
      setCandidateTotal(page.total)
      setLoadError('')
    } catch (cause) {
      setLoadError(errorMessage(cause))
    } finally {
      setCandidateLoading(false)
    }
  }, [candidateRole, candidateStatus, capabilities.read, permissionsReady])

  const loadDimensions = useCallback(async () => {
    if (!permissionsReady || !capabilities.read) return
    await Promise.resolve()
    setDimensionLoading(true)
    try {
      const page = await semanticGovernanceAPI.listDimensions({
        q: dimensionQuery.trim(),
        status: dimensionStatus,
      })
      setDimensions(page.items)
      setDimensionTotal(page.total)
      setLoadError('')
    } catch (cause) {
      setLoadError(errorMessage(cause))
    } finally {
      setDimensionLoading(false)
    }
  }, [capabilities.read, dimensionQuery, dimensionStatus, permissionsReady])

  useEffect(() => {
    if (!permissionsReady || !capabilities.read) return
    let active = true
    semanticGovernanceAPI.listCandidates({
      status: candidateStatus,
      fieldRole: candidateRole,
    }).then(page => {
      if (!active) return
      setCandidates(page.items)
      setCandidateTotal(page.total)
      setLoadError('')
    }).catch(cause => {
      if (active) setLoadError(errorMessage(cause))
    }).finally(() => {
      if (active) setCandidateLoading(false)
    })
    return () => { active = false }
  }, [candidateRole, candidateStatus, capabilities.read, permissionsReady])

  useEffect(() => {
    if (!permissionsReady || !capabilities.read) return
    let active = true
    semanticGovernanceAPI.listDimensions({
      q: dimensionQuery.trim(),
      status: dimensionStatus,
    }).then(page => {
      if (!active) return
      setDimensions(page.items)
      setDimensionTotal(page.total)
      setLoadError('')
    }).catch(cause => {
      if (active) setLoadError(errorMessage(cause))
    }).finally(() => {
      if (active) setDimensionLoading(false)
    })
    return () => { active = false }
  }, [capabilities.read, dimensionQuery, dimensionStatus, permissionsReady])

  const loadDimensionDetail = useCallback(async (dimension: Dimension, query = '') => {
    setSelectedDimension(dimension)
    setDetailLoading(true)
    try {
      const [current, memberPage, aliasPage, jobPage, compatibilityPage] = await Promise.all([
        semanticGovernanceAPI.getDimension(dimension.id),
        dimension.sensitive
          ? Promise.resolve({ items: [], total: 0, limit: 200, offset: 0 })
          : semanticGovernanceAPI.listMembers(dimension.id, query.trim()),
        dimension.sensitive
          ? Promise.resolve({ items: [], total: 0, limit: 200, offset: 0 })
          : semanticGovernanceAPI.listAliases(dimension.id),
        semanticGovernanceAPI.listRefreshJobs(dimension.id),
        semanticGovernanceAPI.listCompatibilities(dimension.id),
      ])
      setSelectedDimension(current)
      setMembers(memberPage.items)
      setMemberTotal(memberPage.total)
      setAliases(aliasPage.items)
      setRefreshJobs(jobPage.items)
      setCompatibilities(compatibilityPage.items)
      setDimensions(items => items.map(item => item.id === current.id ? current : item))
      setLoadError('')
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setDetailLoading(false)
    }
  }, [])

  const suggestedCount = useMemo(
    () => candidates.filter(candidate => candidate.status === 'SUGGESTED').length,
    [candidates],
  )
  const riskyCount = useMemo(
    () => candidates.filter(candidate => candidate.riskHighCardinality || candidate.riskSensitive).length,
    [candidates],
  )
  const publishedCount = useMemo(
    () => dimensions.filter(dimension => dimension.status === 'PUBLISHED').length,
    [dimensions],
  )

  const updateCandidate = async (event: FormEvent) => {
    event.preventDefault()
    if (dialog?.kind !== 'edit-candidate') return
    const { candidate, draft } = dialog
    if (!draft.name.trim()) {
      setNotice({ tone: 'error', message: '维度名称不能为空。' })
      return
    }
    setBusyKey(`candidate:${candidate.id}`)
    try {
      await semanticGovernanceAPI.updateCandidate(candidate.id, {
        expectedVersion: candidate.version,
        ...draft,
        name: draft.name.trim(),
        description: draft.description.trim(),
      })
      setDialog(null)
      setNotice({ tone: 'success', message: '候选治理信息已保存，风险标记和策略只能保持或收紧。' })
      await loadCandidates()
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
      if (cause instanceof RequestError && cause.status === 409) await loadCandidates()
    } finally {
      setBusyKey('')
    }
  }

  const acceptCandidate = async (candidate: DimensionSurveyCandidate) => {
    const blocked = candidatePolicyBlockReason(candidate)
    if (blocked) {
      setNotice({ tone: 'error', message: blocked })
      return
    }
    setBusyKey(`candidate:${candidate.id}`)
    try {
      const result = await semanticGovernanceAPI.acceptCandidate(candidate.id, candidate.version)
      setNotice({ tone: 'info', message: acceptedNextStep(result) })
      await loadCandidates()
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
      if (cause instanceof RequestError && cause.status === 409) await loadCandidates()
    } finally {
      setBusyKey('')
    }
  }

  const rejectCandidate = async (event: FormEvent) => {
    event.preventDefault()
    if (dialog?.kind !== 'reject-candidate') return
    const reason = dialog.reason.trim()
    if (!reason) {
      setNotice({ tone: 'error', message: '拒绝候选时必须填写原因。' })
      return
    }
    setBusyKey(`candidate:${dialog.candidate.id}`)
    try {
      await semanticGovernanceAPI.rejectCandidate(dialog.candidate.id, dialog.candidate.version, reason)
      setDialog(null)
      setNotice({ tone: 'success', message: '候选已拒绝，原因已进入治理审计。' })
      await loadCandidates()
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
      if (cause instanceof RequestError && cause.status === 409) await loadCandidates()
    } finally {
      setBusyKey('')
    }
  }

  const submitRefresh = async (dimension: Dimension) => {
    setBusyKey(`refresh:${dimension.id}`)
    try {
      const job = await semanticGovernanceAPI.createRefreshJob(
        dimension.id,
        dimension.version,
        createDimensionRefreshIdempotencyKey(),
      )
      setNotice({
        tone: 'info',
        message: `成员刷新任务已提交（${refreshStatusLabels[job.status]}）。倒排需等任务成功，且指标兼容关系验证后才可用于值检索。`,
      })
      await loadDimensionDetail(dimension, memberQuery)
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setBusyKey('')
    }
  }

  const createAlias = async (event: FormEvent) => {
    event.preventDefault()
    if (dialog?.kind !== 'create-alias') return
    const alias = dialog.alias.trim()
    if (!alias) {
      setNotice({ tone: 'error', message: '别名不能为空。' })
      return
    }
    setBusyKey(`alias:${dialog.member.id}`)
    try {
      await semanticGovernanceAPI.createAlias({
        dimensionId: dialog.dimension.id,
        dimensionMemberId: dialog.member.id,
        alias,
        aliasType: dialog.aliasType,
      })
      setDialog(null)
      setNotice({
        tone: 'success',
        message: `已为“${dialog.member.canonicalLabel}”新增${aliasTypeLabels[dialog.aliasType]}“${alias}”。`,
      })
      await loadDimensionDetail(dialog.dimension, memberQuery)
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setBusyKey('')
    }
  }

  const verifyCompatibility = async (item: DimensionMetricCompatibility) => {
    if (!selectedDimension) return
    setBusyKey(`compatibility:${item.id}`)
    try {
      await semanticGovernanceAPI.verifyCompatibility(item.id, item.version)
      setNotice({
        tone: 'success',
        message: '兼容关系已验证；只有成员索引也可用时，维度值才能定位该指标。',
      })
      await loadDimensionDetail(selectedDimension, memberQuery)
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setBusyKey('')
    }
  }

  return (
    <AppShell title="资产管理中心" eyebrow="指标 · 语义 · 维度值">
      <AssetManagementTabs />
      <section className="semantic-governance">
        {notice && (
          <div className={`semantic-notice ${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}>
            <span>{notice.message}</span>
            <button type="button" aria-label="关闭提示" onClick={() => setNotice(null)}>×</button>
          </div>
        )}

        {!permissionsReady ? (
          <section className="semantic-state">正在检查语义治理权限…</section>
        ) : !capabilities.read ? (
          <section className="semantic-state denied">
            <strong>当前账号没有语义治理读取权限</strong>
            <span>语义治理复用全局 DATASET:READ；请联系管理员授权后重试。</span>
          </section>
        ) : (
          <>
            {!capabilities.manage && (
              <div className="semantic-readonly" role="note">
                当前为只读模式。编辑、接收、拒绝、成员刷新、别名新增和兼容关系验证需要全局 DATASET:MANAGE。
              </div>
            )}
            {loadError && <div className="semantic-load-error" role="alert">{loadError}</div>}
            <div className="semantic-summary" aria-label="语义治理概览">
              <article><span>当前候选</span><strong>{formatCount(candidateTotal)}</strong><small>{suggestedCount} 条在当前页待治理</small></article>
              <article><span>风险候选</span><strong>{formatCount(riskyCount)}</strong><small>敏感或高基数，策略不可放宽</small></article>
              <article><span>正式维度</span><strong>{formatCount(dimensionTotal)}</strong><small>{publishedCount} 条在当前页已发布</small></article>
              <article><span>检索门槛</span><strong>2</strong><small>成员快照 + 已验证指标兼容</small></article>
            </div>

            <div className="semantic-view-tabs" role="tablist" aria-label="语义治理视图">
              <button type="button" role="tab" aria-selected={view === 'candidates'} onClick={() => setView('candidates')}>
                DWS 维度候选
              </button>
              <button type="button" role="tab" aria-selected={view === 'dimensions'} onClick={() => setView('dimensions')}>
                正式维度与成员
              </button>
            </div>

            {view === 'candidates' ? (
              <CandidateDirectory
                candidates={candidates}
                total={candidateTotal}
                loading={candidateLoading}
                status={candidateStatus}
                role={candidateRole}
                canManage={capabilities.manage}
                busyKey={busyKey}
                onStatusChange={value => { setCandidateLoading(true); setCandidateStatus(value) }}
                onRoleChange={value => { setCandidateLoading(true); setCandidateRole(value) }}
                onReload={() => void loadCandidates()}
                onEdit={candidate => setDialog({ kind: 'edit-candidate', candidate, draft: candidateDraft(candidate) })}
                onAccept={candidate => void acceptCandidate(candidate)}
                onReject={candidate => setDialog({ kind: 'reject-candidate', candidate, reason: '' })}
              />
            ) : (
              <DimensionDirectory
                dimensions={dimensions}
                total={dimensionTotal}
                loading={dimensionLoading}
                query={dimensionQuery}
                status={dimensionStatus}
                onQueryChange={value => { setDimensionLoading(true); setDimensionQuery(value) }}
                onStatusChange={value => { setDimensionLoading(true); setDimensionStatus(value) }}
                onReload={() => void loadDimensions()}
                onOpen={dimension => {
                  setMemberQuery('')
                  void loadDimensionDetail(dimension)
                }}
              />
            )}
          </>
        )}
      </section>

      {selectedDimension && (
        <DimensionDetail
          dimension={selectedDimension}
          loading={detailLoading}
          members={members}
          memberTotal={memberTotal}
          aliases={aliases}
          refreshJobs={refreshJobs}
          compatibilities={compatibilities}
          memberQuery={memberQuery}
          canManage={capabilities.manage}
          busyKey={busyKey}
          onMemberQueryChange={setMemberQuery}
          onMemberSearch={() => void loadDimensionDetail(selectedDimension, memberQuery)}
          onClose={() => setSelectedDimension(null)}
          onRefresh={() => void submitRefresh(selectedDimension)}
          onCreateAlias={member => setDialog({
            kind: 'create-alias',
            dimension: selectedDimension,
            member,
            alias: '',
            aliasType: 'LEGACY',
          })}
          onVerify={item => void verifyCompatibility(item)}
        />
      )}

      {dialog?.kind === 'edit-candidate' && (
        <CandidateEditDialog
          state={dialog}
          busy={busyKey === `candidate:${dialog.candidate.id}`}
          onChange={draft => setDialog({ ...dialog, draft })}
          onClose={() => setDialog(null)}
          onSubmit={updateCandidate}
        />
      )}
      {dialog?.kind === 'reject-candidate' && (
        <RejectCandidateDialog
          state={dialog}
          busy={busyKey === `candidate:${dialog.candidate.id}`}
          onReasonChange={reason => setDialog({ ...dialog, reason })}
          onClose={() => setDialog(null)}
          onSubmit={rejectCandidate}
        />
      )}
      {dialog?.kind === 'create-alias' && (
        <AliasDialog
          state={dialog}
          busy={busyKey === `alias:${dialog.member.id}`}
          onChange={(alias, aliasType) => setDialog({ ...dialog, alias, aliasType })}
          onClose={() => setDialog(null)}
          onSubmit={createAlias}
        />
      )}
    </AppShell>
  )
}

function CandidateDirectory({
  candidates,
  total,
  loading,
  status,
  role,
  canManage,
  busyKey,
  onStatusChange,
  onRoleChange,
  onReload,
  onEdit,
  onAccept,
  onReject,
}: {
  candidates: DimensionSurveyCandidate[]
  total: number
  loading: boolean
  status: DimensionSurveyStatus | ''
  role: DimensionFieldRole | ''
  canManage: boolean
  busyKey: string
  onStatusChange: (value: DimensionSurveyStatus | '') => void
  onRoleChange: (value: DimensionFieldRole | '') => void
  onReload: () => void
  onEdit: (candidate: DimensionSurveyCandidate) => void
  onAccept: (candidate: DimensionSurveyCandidate) => void
  onReject: (candidate: DimensionSurveyCandidate) => void
}) {
  return (
    <section className="semantic-directory" aria-label="DWS 维度勘测候选">
      <header>
        <div>
          <span className="eyebrow">Dimension Survey</span>
          <h2>DWS 字段治理队列</h2>
          <p>候选固定到精确数据集版本、schema 和物化快照；接收前可编辑业务语义，但不能放宽已识别风险。</p>
        </div>
        <button className="quiet-button" type="button" onClick={onReload} disabled={loading}>重新加载</button>
      </header>
      <div className="semantic-filters">
        <label>候选状态
          <select aria-label="候选状态" value={status} onChange={event => onStatusChange(event.target.value as DimensionSurveyStatus | '')}>
            <option value="">全部状态</option>
            {Object.entries(candidateStatusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
        <label>字段用途
          <select aria-label="候选字段用途" value={role} onChange={event => onRoleChange(event.target.value as DimensionFieldRole | '')}>
            <option value="">全部用途</option>
            {Object.entries(fieldRoleLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
        <output>{total} 条结果</output>
      </div>
      {loading ? (
        <div className="semantic-empty">正在加载 DWS 勘测候选…</div>
      ) : !candidates.length ? (
        <div className="semantic-empty"><strong>当前筛选下没有维度候选</strong><span>DWS 发布并完成活动物化后，符合条件的非度量字段会进入治理队列。</span></div>
      ) : (
        <div className="semantic-candidate-list">
          {candidates.map(candidate => {
            const mutable = candidate.status === 'SUGGESTED' && canManage
            const busy = busyKey === `candidate:${candidate.id}`
            const policyBlocked = candidatePolicyBlockReason(candidate)
            const profile = candidate.profile
            const nullRate = profile.rowCount === undefined || profile.nullCount === undefined
              ? undefined
              : profile.rowCount === 0 ? 0 : profile.nullCount / profile.rowCount
            const distinctDisplay = profile.distinctOverflow
              ? `>${formatCount(profile.distinctCap)}`
              : formatCount(profile.distinctCount)
            return (
              <article className="semantic-candidate-card" key={candidate.id}>
                <header>
                  <div>
                    <span className="semantic-field-role">{fieldRoleLabels[candidate.fieldRole]}</span>
                    <div><h3>{candidate.proposedName}</h3><span className={`semantic-status ${candidate.status.toLowerCase()}`}>{candidateStatusLabels[candidate.status]}</span></div>
                    <p>{candidate.proposedDescription || '暂无业务说明'}</p>
                  </div>
                  <div className="semantic-card-actions">
                    <button type="button" onClick={() => onEdit(candidate)} disabled={!mutable || busy}>编辑</button>
                    <button
                      type="button"
                      className="accept"
                      onClick={() => onAccept(candidate)}
                      disabled={!mutable || busy || Boolean(policyBlocked)}
                      title={policyBlocked}
                    >
                      接收
                    </button>
                    <button type="button" className="reject" onClick={() => onReject(candidate)} disabled={!mutable || busy}>拒绝</button>
                  </div>
                </header>
                <dl className="semantic-facts">
                  <div><dt>字段</dt><dd>{candidate.fieldCode}<small>{candidate.canonicalType} · {candidate.semanticType || '未标注语义类型'}</small></dd></div>
                  <div><dt>维度类型</dt><dd>{dimensionTypeLabels[candidate.proposedDimensionType]}</dd></div>
                  <div><dt>成员策略</dt><dd>{policyLabels[candidate.proposedMemberIndexPolicy]}</dd></div>
                  <div><dt>物化行数</dt><dd>{formatCount(candidate.materializationRowCount)}</dd></div>
                </dl>
                <section className={`semantic-profile-summary ${profile.status.toLowerCase()}`} aria-label="字段画像证据">
                  <header>
                    <strong>字段画像</strong>
                    <span>{profileStatusLabels[profile.status]}</span>
                  </header>
                  {profile.status === 'SUCCEEDED' ? (
                    <dl>
                      <div><dt>实测行数</dt><dd>{formatCount(profile.rowCount)}</dd></div>
                      <div><dt>非空 / 空值</dt><dd>{formatCount(profile.nonNullCount)} / {formatCount(profile.nullCount)}</dd></div>
                      <div><dt>NDV</dt><dd>{distinctDisplay}</dd></div>
                      <div><dt>空值率</dt><dd>{formatPercent(nullRate)}</dd></div>
                    </dl>
                  ) : profile.status === 'SKIPPED_POLICY' ? (
                    <p>
                      未读取业务值：
                      {profile.resultCode === 'SENSITIVE_FIELD_PROFILE_SKIPPED'
                        ? '敏感字段按策略直接禁用成员索引。'
                        : '标识符按策略仅允许精确解析。'}
                    </p>
                  ) : (
                    <p>没有可用于放宽成员策略的实测证据；当前仅可安全选择 NONE。</p>
                  )}
                  <footer>
                    <span>建议策略 {profile.recommendedMemberIndexPolicy
                      ? policyLabels[profile.recommendedMemberIndexPolicy]
                      : '尚未形成'}</span>
                    {profile.resultCode && <span>结论码 {profile.resultCode}</span>}
                  </footer>
                </section>
                {policyBlocked && <div className="semantic-policy-block" role="note">{policyBlocked}</div>}
                <div className="semantic-risk-row">
                  {candidate.riskSensitive && <span className="risk">勘测识别：敏感</span>}
                  {candidate.riskHighCardinality && <span className="risk">勘测识别：高基数</span>}
                  {candidate.proposedSensitive && !candidate.riskSensitive && <span>人工收紧：敏感</span>}
                  {candidate.proposedHighCardinality && !candidate.riskHighCardinality && <span>人工收紧：高基数</span>}
                  {!candidate.riskSensitive && !candidate.riskHighCardinality && !candidate.proposedSensitive && !candidate.proposedHighCardinality && <span className="safe">未识别敏感或高基数风险</span>}
                </div>
                <footer>
                  <span>数据集版本 {shortID(candidate.datasetVersionId)}</span>
                  <span>schema {shortID(candidate.schemaHash)}</span>
                  {candidate.decisionReason && <strong>结论：{candidate.decisionReason}</strong>}
                </footer>
              </article>
            )
          })}
        </div>
      )}
    </section>
  )
}

function DimensionDirectory({
  dimensions,
  total,
  loading,
  query,
  status,
  onQueryChange,
  onStatusChange,
  onReload,
  onOpen,
}: {
  dimensions: Dimension[]
  total: number
  loading: boolean
  query: string
  status: '' | 'DRAFT' | 'PUBLISHED' | 'DEPRECATED'
  onQueryChange: (value: string) => void
  onStatusChange: (value: '' | 'DRAFT' | 'PUBLISHED' | 'DEPRECATED') => void
  onReload: () => void
  onOpen: (dimension: Dimension) => void
}) {
  return (
    <section className="semantic-directory" aria-label="正式维度目录">
      <header>
        <div>
          <span className="eyebrow">Governed Dimensions</span>
          <h2>正式维度与检索状态</h2>
          <p>成员刷新只建立值域快照；维度值定位指标还需要 VERIFIED 且非 UNSAFE 的兼容关系。</p>
        </div>
        <button className="quiet-button" type="button" onClick={onReload} disabled={loading}>重新加载</button>
      </header>
      <div className="semantic-filters dimensions">
        <label>搜索维度
          <input aria-label="搜索正式维度" value={query} onChange={event => onQueryChange(event.target.value)} placeholder="名称或编码" />
        </label>
        <label>维度状态
          <select aria-label="正式维度状态" value={status} onChange={event => onStatusChange(event.target.value as typeof status)}>
            <option value="">全部状态</option>
            <option value="DRAFT">草稿</option>
            <option value="PUBLISHED">已发布</option>
            <option value="DEPRECATED">已停用</option>
          </select>
        </label>
        <output>{total} 条结果</output>
      </div>
      {loading ? (
        <div className="semantic-empty">正在加载正式维度…</div>
      ) : !dimensions.length ? (
        <div className="semantic-empty"><strong>当前筛选下没有正式维度</strong><span>可先在“DWS 维度候选”中审核并接收候选。</span></div>
      ) : (
        <div className="semantic-dimension-grid">
          {dimensions.map(dimension => (
            <article key={dimension.id}>
              <header>
                <div><h3>{dimension.name}</h3><span>{dimension.code}</span></div>
                <span className={`semantic-status ${dimension.status.toLowerCase()}`}>{dimension.status}</span>
              </header>
              <p>{dimension.description || '暂无业务说明'}</p>
              <div className="semantic-risk-row">
                <span>{dimensionTypeLabels[dimension.dimensionType]}</span>
                <span>{policyLabels[dimension.memberIndexPolicy]}</span>
                {dimension.sensitive && <span className="risk">敏感</span>}
                {dimension.highCardinality && <span className="risk">高基数</span>}
              </div>
              <div className={`semantic-readiness ${dimension.memberRefreshedAt ? 'has-snapshot' : ''}`}>
                <strong>{dimension.memberRefreshedAt ? `${formatCount(dimension.memberCount)} 个成员` : '成员倒排未就绪'}</strong>
                <span>{dimensionReadiness(dimension)}</span>
              </div>
              <footer>
                <span>字段 {dimension.fieldCode}</span>
                <button type="button" onClick={() => onOpen(dimension)}>查看治理详情</button>
              </footer>
            </article>
          ))}
        </div>
      )}
    </section>
  )
}

function DimensionDetail({
  dimension,
  loading,
  members,
  memberTotal,
  aliases,
  refreshJobs,
  compatibilities,
  memberQuery,
  canManage,
  busyKey,
  onMemberQueryChange,
  onMemberSearch,
  onClose,
  onRefresh,
  onCreateAlias,
  onVerify,
}: {
  dimension: Dimension
  loading: boolean
  members: DimensionMember[]
  memberTotal: number
  aliases: DimensionMemberAlias[]
  refreshJobs: DimensionRefreshJob[]
  compatibilities: DimensionMetricCompatibility[]
  memberQuery: string
  canManage: boolean
  busyKey: string
  onMemberQueryChange: (value: string) => void
  onMemberSearch: () => void
  onClose: () => void
  onRefresh: () => void
  onCreateAlias: (member: DimensionMember) => void
  onVerify: (item: DimensionMetricCompatibility) => void
}) {
  const verified = compatibilities.filter(item => item.status === 'VERIFIED' && item.fanoutPolicy !== 'UNSAFE').length
  const refreshAllowed = dimension.status === 'PUBLISHED' &&
    dimension.memberIndexPolicy === 'FULL' &&
    !dimension.sensitive &&
    !dimension.highCardinality
  const aliasByMember = useMemo(() => {
    const grouped = new Map<string, DimensionMemberAlias[]>()
    aliases.forEach(alias => grouped.set(alias.dimensionMemberId, [...(grouped.get(alias.dimensionMemberId) ?? []), alias]))
    return grouped
  }, [aliases])

  return (
    <div className="semantic-detail-backdrop" role="presentation" onMouseDown={event => {
      if (event.currentTarget === event.target) onClose()
    }}>
      <aside className="semantic-detail" role="dialog" aria-modal="true" aria-label={`${dimension.name}治理详情`}>
        <header>
          <div><span className="eyebrow">Dimension Detail</span><h2>{dimension.name}</h2><p>{dimension.code} · {dimension.fieldCode}</p></div>
          <button type="button" aria-label="关闭维度详情" onClick={onClose}>×</button>
        </header>
        {loading ? <div className="semantic-detail-loading">正在加载成员、刷新任务和兼容关系…</div> : (
          <div className="semantic-detail-body">
            <section className="semantic-detail-status">
              <div>
                <span>成员策略</span>
                <strong>{policyLabels[dimension.memberIndexPolicy]}</strong>
                <small>最后成功快照：{formatDate(dimension.memberRefreshedAt)}</small>
              </div>
              <div>
                <span>可用兼容关系</span>
                <strong>{verified}</strong>
                <small>只计 VERIFIED 且非 UNSAFE</small>
              </div>
              <p>{dimensionReadiness(dimension)}</p>
              <button type="button" className="primary-button" onClick={onRefresh} disabled={!canManage || !refreshAllowed || busyKey === `refresh:${dimension.id}`}>
                {busyKey === `refresh:${dimension.id}` ? '正在提交…' : '提交成员刷新'}
              </button>
            </section>
            {!refreshAllowed && (
              <div className="semantic-boundary-note" role="note">
                {dimension.sensitive
                  ? '不可刷新：敏感维度禁止 FULL 扫描，成员和别名也不会从列表接口返回。'
                  : dimension.highCardinality
                    ? '不可刷新：高基数维度禁止 FULL 扫描，应使用 EXACT_ONLY 或 NONE。'
                    : dimension.status !== 'PUBLISHED'
                      ? '不可刷新：只有已发布维度可以提交成员刷新。'
                      : '不可刷新：该维度的成员策略不是 FULL。'}
              </div>
            )}

            <section className="semantic-detail-section" aria-label="成员刷新任务">
              <header><div><h3>刷新任务</h3><p>任务成功前不会覆盖旧快照；失败原因保留为稳定结果码。</p></div></header>
              {!refreshJobs.length ? <div className="semantic-mini-empty">还没有成员刷新任务。</div> : (
                <div className="semantic-job-list">
                  {refreshJobs.map(job => (
                    <article key={job.id}>
                      <span className={`semantic-status ${job.status.toLowerCase()}`}>{refreshStatusLabels[job.status]}</span>
                      <div><strong>{formatDate(job.createdAt)}</strong><small>{job.resultCode || `第 ${job.attempt}/${job.maxAttempts} 次尝试`}</small></div>
                      <span>{job.memberCount === undefined ? '—' : `${formatCount(job.memberCount)} 个成员`}</span>
                    </article>
                  ))}
                </div>
              )}
            </section>

            <section className="semantic-detail-section" aria-label="维度成员">
              <header>
                <div><h3>成员与历史别名</h3><p>例如：为规范成员“智家生态圈”新增历史编码“690”。</p></div>
                {!dimension.sensitive && (
                  <div className="semantic-member-search">
                    <input aria-label="搜索维度成员" value={memberQuery} onChange={event => onMemberQueryChange(event.target.value)} placeholder="成员值或标签" />
                    <button type="button" onClick={onMemberSearch}>查询</button>
                  </div>
                )}
              </header>
              {dimension.sensitive ? (
                <div className="semantic-mini-empty sensitive">敏感维度成员与别名不可浏览。</div>
              ) : !members.length ? (
                <div className="semantic-mini-empty">{dimension.memberIndexPolicy === 'FULL' ? '当前没有活动成员；请先完成成员刷新。' : '该策略不会自动枚举可浏览成员。'}</div>
              ) : (
                <>
                  <p className="semantic-result-count">显示 {members.length} / {memberTotal} 个活动成员</p>
                  <div className="semantic-member-list">
                    {members.map(member => (
                      <article key={member.id}>
                        <div>
                          <strong>{member.canonicalLabel}</strong>
                          <small>{member.memberKey}</small>
                        </div>
                        <div className="semantic-aliases">
                          {(aliasByMember.get(member.id) ?? []).map(alias => (
                            <span key={alias.id}>{alias.alias}<small>{aliasTypeLabels[alias.aliasType]}</small></span>
                          ))}
                          {!(aliasByMember.get(member.id) ?? []).length && <em>暂无别名</em>}
                        </div>
                        <button type="button" onClick={() => onCreateAlias(member)} disabled={!canManage}>新增别名</button>
                      </article>
                    ))}
                  </div>
                </>
              )}
            </section>

            <section className="semantic-detail-section" aria-label="维度指标兼容关系">
              <header><div><h3>维度—指标兼容</h3><p>成员倒排与兼容关系必须同时就绪，值检索才会返回指标。</p></div></header>
              {!compatibilities.length ? (
                <div className="semantic-mini-empty warning">尚无兼容关系。请先通过治理 API 提议指标兼容关系，再回到此处验证；页面不会把成员刷新误报为检索可用。</div>
              ) : (
                <div className="semantic-compatibility-list">
                  {compatibilities.map(item => (
                    <article key={item.id}>
                      <div><strong>指标版本 {shortID(item.metricVersionId)}</strong><small>{item.compatibilityType} · {item.fanoutPolicy} · {item.evidenceSource}</small></div>
                      <span className={`semantic-status ${item.status.toLowerCase()}`}>{compatibilityStatusLabels[item.status]}</span>
                      {item.status === 'PROPOSED' && (
                        <button
                          type="button"
                          onClick={() => onVerify(item)}
                          disabled={!canManage || item.fanoutPolicy === 'UNSAFE' || busyKey === `compatibility:${item.id}`}
                          title={item.fanoutPolicy === 'UNSAFE' ? 'UNSAFE 关系不能验证' : ''}
                        >
                          {item.fanoutPolicy === 'UNSAFE' ? '不可验证' : '验证关系'}
                        </button>
                      )}
                    </article>
                  ))}
                </div>
              )}
            </section>
          </div>
        )}
      </aside>
    </div>
  )
}

function CandidateEditDialog({
  state,
  busy,
  onChange,
  onClose,
  onSubmit,
}: {
  state: Extract<GovernanceDialog, { kind: 'edit-candidate' }>
  busy: boolean
  onChange: (draft: CandidateDraft) => void
  onClose: () => void
  onSubmit: (event: FormEvent) => void
}) {
  const { candidate, draft } = state
  const policyFloor = candidatePolicyFloor(candidate)
  const patch = (next: Partial<CandidateDraft>) => {
    const result = { ...draft, ...next }
    if (result.sensitive) {
      result.memberIndexPolicy = 'NONE'
    } else if (result.highCardinality && result.memberIndexPolicy === 'FULL') {
      result.memberIndexPolicy = 'EXACT_ONLY'
    }
    result.memberIndexPolicy = stricterPolicy(result.memberIndexPolicy, policyFloor)
    onChange(result)
  }
  return (
    <div className="semantic-dialog-backdrop" role="presentation">
      <section className="semantic-dialog" role="dialog" aria-modal="true" aria-label="编辑维度候选">
        <header><div><span className="eyebrow">Review Candidate</span><h2>编辑维度候选</h2></div><button type="button" aria-label="关闭编辑候选" onClick={onClose}>×</button></header>
        <form onSubmit={onSubmit}>
          <div className="semantic-form-grid">
            <label>字段编码<input value={draft.code} readOnly aria-label="候选字段编码" /><small>当前候选的稳定业务编码；此页面不改写字段身份。</small></label>
            <label>维度名称<input required maxLength={256} value={draft.name} onChange={event => patch({ name: event.target.value })} aria-label="候选维度名称" /></label>
            <label className="wide">维度说明<textarea rows={4} maxLength={4096} value={draft.description} onChange={event => patch({ description: event.target.value })} aria-label="候选维度说明" /></label>
            <label>维度类型
              <select value={draft.dimensionType} onChange={event => patch({ dimensionType: event.target.value as DimensionType })} aria-label="候选维度类型">
                {dimensionTypes.map(type => <option value={type} key={type}>{dimensionTypeLabels[type]}</option>)}
              </select>
            </label>
            <label>成员索引策略
              <select value={draft.memberIndexPolicy} onChange={event => patch({ memberIndexPolicy: event.target.value as MemberIndexPolicy })} aria-label="候选成员索引策略">
                {policies.map(policy => (
                  <option
                    value={policy}
                    key={policy}
                    disabled={policyRank[policy] < policyRank[policyFloor]}
                  >
                    {policyLabels[policy]}
                  </option>
                ))}
              </select>
              <small>
                只能保持或收紧原建议及画像下限；画像待处理、失败或失效时仅允许 NONE。
              </small>
            </label>
          </div>
          <div className="semantic-risk-controls">
            <label>
              <input
                type="checkbox"
                checked={draft.sensitive}
                disabled={candidate.proposedSensitive}
                onChange={event => patch({ sensitive: event.target.checked })}
              />
              <span>敏感维度<small>{candidate.riskSensitive ? '勘测已识别，不能取消' : '人工标记后不能再放宽'}</small></span>
            </label>
            <label>
              <input
                type="checkbox"
                checked={draft.highCardinality}
                disabled={candidate.proposedHighCardinality}
                onChange={event => patch({ highCardinality: event.target.checked })}
              />
              <span>高基数维度<small>{candidate.riskHighCardinality ? '勘测已识别，不能取消' : '人工标记后不能再放宽'}</small></span>
            </label>
          </div>
          <footer><button type="button" className="quiet-button" onClick={onClose}>取消</button><button type="submit" className="primary-button" disabled={busy}>{busy ? '保存中…' : '保存治理信息'}</button></footer>
        </form>
      </section>
    </div>
  )
}

function RejectCandidateDialog({
  state,
  busy,
  onReasonChange,
  onClose,
  onSubmit,
}: {
  state: Extract<GovernanceDialog, { kind: 'reject-candidate' }>
  busy: boolean
  onReasonChange: (reason: string) => void
  onClose: () => void
  onSubmit: (event: FormEvent) => void
}) {
  return (
    <div className="semantic-dialog-backdrop" role="presentation">
      <section className="semantic-dialog compact" role="dialog" aria-modal="true" aria-label="拒绝维度候选">
        <header><div><span className="eyebrow">Reject Candidate</span><h2>拒绝“{state.candidate.proposedName}”</h2></div><button type="button" aria-label="关闭拒绝候选" onClick={onClose}>×</button></header>
        <form onSubmit={onSubmit}>
          <label>拒绝原因<textarea required rows={5} maxLength={2000} value={state.reason} onChange={event => onReasonChange(event.target.value)} aria-label="候选拒绝原因" placeholder="说明字段不适合作为维度的业务原因" /></label>
          <footer><button type="button" className="quiet-button" onClick={onClose}>取消</button><button type="submit" className="semantic-danger-button" disabled={busy}>{busy ? '提交中…' : '确认拒绝'}</button></footer>
        </form>
      </section>
    </div>
  )
}

function AliasDialog({
  state,
  busy,
  onChange,
  onClose,
  onSubmit,
}: {
  state: Extract<GovernanceDialog, { kind: 'create-alias' }>
  busy: boolean
  onChange: (alias: string, aliasType: DimensionMemberAlias['aliasType']) => void
  onClose: () => void
  onSubmit: (event: FormEvent) => void
}) {
  return (
    <div className="semantic-dialog-backdrop" role="presentation">
      <section className="semantic-dialog compact" role="dialog" aria-modal="true" aria-label="新增维度成员别名">
        <header><div><span className="eyebrow">Member Alias</span><h2>为“{state.member.canonicalLabel}”新增别名</h2></div><button type="button" aria-label="关闭新增别名" onClick={onClose}>×</button></header>
        <form onSubmit={onSubmit}>
          <label>别名<input required maxLength={1024} value={state.alias} onChange={event => onChange(event.target.value, state.aliasType)} aria-label="维度成员别名" placeholder="例如 690" /></label>
          <label>别名类型
            <select value={state.aliasType} onChange={event => onChange(state.alias, event.target.value as DimensionMemberAlias['aliasType'])} aria-label="维度成员别名类型">
              {Object.entries(aliasTypeLabels).map(([value, label]) => <option value={value} key={value}>{label}</option>)}
            </select>
          </label>
          <div className="semantic-boundary-note">别名是租户内治理数据，会参与精确匹配；不会把成员值发送给外部向量服务。</div>
          <footer><button type="button" className="quiet-button" onClick={onClose}>取消</button><button type="submit" className="primary-button" disabled={busy}>{busy ? '保存中…' : '新增别名'}</button></footer>
        </form>
      </section>
    </div>
  )
}
