import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, test, vi } from 'vitest'
import {
  semanticGovernanceAPI,
  type Dimension,
  type DimensionMember,
  type DimensionMetricCompatibility,
  type DimensionRefreshJob,
  type DimensionSurveyCandidate,
} from '../lib/semantic-governance'
import { SemanticGovernancePage } from './SemanticGovernancePage'

afterEach(() => vi.restoreAllMocks())

const createdAt = '2026-07-24T08:00:00Z'

const surveyCandidate = (overrides: Partial<DimensionSurveyCandidate> = {}): DimensionSurveyCandidate => ({
  id: 'candidate-1',
  surveyRunId: 'survey-1',
  datasetId: 'dataset-1',
  datasetVersionId: 'dataset-version-1',
  schemaHash: 'a'.repeat(64),
  materializationId: 'materialization-1',
  materializationSnapshotHash: 'b'.repeat(64),
  materializationRowCount: 800,
  fieldId: 'field-ecosystem',
  fieldCode: 'ecosystem',
  fieldRole: 'DIMENSION',
  canonicalType: 'STRING',
  semanticType: 'CATEGORY',
  riskHighCardinality: false,
  riskSensitive: false,
  evidence: { containsBusinessSamples: false },
  proposedCode: 'ecosystem',
  proposedName: '生态圈',
  proposedDescription: '组织归属维度',
  proposedDimensionType: 'ORGANIZATION',
  proposedMemberIndexPolicy: 'FULL',
  proposedHighCardinality: false,
  proposedSensitive: false,
  status: 'SUGGESTED',
  version: 3,
  generatedBy: 'survey-worker',
  updatedBy: 'survey-worker',
  createdAt,
  updatedAt: createdAt,
  profile: {
    id: 'profile-1',
    status: 'SUCCEEDED',
    profileVersion: 'dws-dimension-profile-v1',
    policyVersion: 'dimension-member-policy-v1',
    materializationId: 'materialization-1',
    materializationSnapshotHash: 'b'.repeat(64),
    expectedRowCount: 800,
    rowCount: 800,
    nonNullCount: 760,
    nullCount: 40,
    distinctCount: 12,
    distinctOverflow: false,
    distinctCap: 100000,
    distinctRatio: 12 / 760,
    riskHighCardinality: false,
    recommendedMemberIndexPolicy: 'FULL',
    attempt: 1,
    maxAttempts: 3,
    createdAt,
    updatedAt: createdAt,
    completedAt: createdAt,
  },
  ...overrides,
})

const dimension = (overrides: Partial<Dimension> = {}): Dimension => ({
  id: 'dimension-1',
  datasetId: 'dataset-1',
  datasetVersionId: 'dataset-version-1',
  fieldId: 'field-ecosystem',
  fieldCode: 'ecosystem',
  code: 'ecosystem',
  name: '生态圈',
  description: '组织归属维度',
  dimensionType: 'ORGANIZATION',
  memberIndexPolicy: 'FULL',
  highCardinality: false,
  sensitive: false,
  status: 'PUBLISHED',
  definitionHash: 'c'.repeat(64),
  version: 2,
  memberRefreshGeneration: 'generation-1',
  memberCount: 1,
  memberRefreshedAt: createdAt,
  lastMemberRefreshJobId: 'refresh-1',
  createdBy: 'user-1',
  updatedBy: 'user-1',
  createdAt,
  updatedAt: createdAt,
  ...overrides,
})

const member: DimensionMember = {
  id: 'member-1',
  dimensionId: 'dimension-1',
  memberKey: 'home-ecosystem',
  canonicalLabel: '智家生态圈',
  normalizedValue: '智家生态圈',
  status: 'ACTIVE',
  firstSeenAt: createdAt,
  lastSeenAt: createdAt,
  refreshGeneration: 'generation-1',
  lastRefreshJobId: 'refresh-1',
  updatedAt: createdAt,
}

const refreshJob: DimensionRefreshJob = {
  id: 'refresh-1',
  dimensionId: 'dimension-1',
  dimensionVersion: 2,
  datasetId: 'dataset-1',
  datasetVersionId: 'dataset-version-1',
  fieldId: 'field-ecosystem',
  fieldCode: 'ecosystem',
  memberIndexPolicy: 'FULL',
  materializationId: 'materialization-1',
  refreshGeneration: 'generation-1',
  status: 'SUCCEEDED',
  maxMembers: 100000,
  timeoutSeconds: 60,
  requestHash: 'd'.repeat(64),
  requestedBy: 'user-1',
  attempt: 1,
  maxAttempts: 3,
  memberCount: 1,
  createdAt,
  updatedAt: createdAt,
  completedAt: createdAt,
}

const compatibility: DimensionMetricCompatibility = {
  id: 'compatibility-1',
  dimensionId: 'dimension-1',
  metricId: 'metric-1',
  metricVersionId: 'metric-version-1',
  metricDatasetVersionId: 'dataset-version-1',
  compatibilityType: 'DIRECT',
  fanoutPolicy: 'SAFE',
  joinPath: [],
  evidenceSource: 'HUMAN',
  confidence: 1,
  status: 'PROPOSED',
  version: 1,
  createdBy: 'user-1',
  updatedBy: 'user-1',
  createdAt,
  updatedAt: createdAt,
}

function mockGovernance(canManage = true) {
  const candidate = surveyCandidate()
  const governedDimension = dimension()
  const permission = vi.spyOn(semanticGovernanceAPI, 'evaluatePermission')
    .mockImplementation(async action => ({ allowed: action === 'READ' || canManage }))
  const listCandidates = vi.spyOn(semanticGovernanceAPI, 'listCandidates')
    .mockResolvedValue({ items: [candidate], total: 1, limit: 200, offset: 0 })
  const updateCandidate = vi.spyOn(semanticGovernanceAPI, 'updateCandidate').mockResolvedValue(candidate)
  const acceptCandidate = vi.spyOn(semanticGovernanceAPI, 'acceptCandidate').mockResolvedValue({
    candidate: surveyCandidate({ status: 'ACCEPTED', version: 4, acceptedDimensionId: governedDimension.id }),
    dimension: governedDimension,
    memberRefreshJob: { ...refreshJob, status: 'QUEUED', memberCount: undefined },
    memberSearchReady: false,
    nextAction: 'WAIT_FOR_MEMBER_REFRESH',
  })
  const rejectCandidate = vi.spyOn(semanticGovernanceAPI, 'rejectCandidate')
    .mockResolvedValue(surveyCandidate({ status: 'REJECTED', version: 4, decisionReason: '不是稳定维度' }))
  const listDimensions = vi.spyOn(semanticGovernanceAPI, 'listDimensions')
    .mockResolvedValue({ items: [governedDimension], total: 1, limit: 200, offset: 0 })
  vi.spyOn(semanticGovernanceAPI, 'getDimension').mockResolvedValue(governedDimension)
  vi.spyOn(semanticGovernanceAPI, 'listMembers')
    .mockResolvedValue({ items: [member], total: 1, limit: 200, offset: 0 })
  vi.spyOn(semanticGovernanceAPI, 'listAliases')
    .mockResolvedValue({ items: [], total: 0, limit: 200, offset: 0 })
  vi.spyOn(semanticGovernanceAPI, 'listRefreshJobs')
    .mockResolvedValue({ items: [refreshJob], total: 1, limit: 50, offset: 0 })
  vi.spyOn(semanticGovernanceAPI, 'listCompatibilities')
    .mockResolvedValue({ items: [compatibility], total: 1, limit: 200, offset: 0 })
  const createRefreshJob = vi.spyOn(semanticGovernanceAPI, 'createRefreshJob')
    .mockResolvedValue({ ...refreshJob, id: 'refresh-2', status: 'QUEUED', memberCount: undefined })
  const createAlias = vi.spyOn(semanticGovernanceAPI, 'createAlias').mockResolvedValue({
    id: 'alias-1',
    dimensionId: governedDimension.id,
    dimensionMemberId: member.id,
    alias: '690',
    normalizedAlias: '690',
    aliasType: 'LEGACY',
    version: 1,
    createdAt,
    updatedAt: createdAt,
  })
  const verifyCompatibility = vi.spyOn(semanticGovernanceAPI, 'verifyCompatibility')
    .mockResolvedValue({ ...compatibility, status: 'VERIFIED', version: 2 })
  return {
    permission,
    listCandidates,
    updateCandidate,
    acceptCandidate,
    rejectCandidate,
    listDimensions,
    createRefreshJob,
    createAlias,
    verifyCompatibility,
  }
}

function renderPage() {
  return render(<MemoryRouter initialEntries={['/semantic-governance']}><SemanticGovernancePage /></MemoryRouter>)
}

describe('SemanticGovernancePage', () => {
  test('edits candidate semantics while tightening sensitive-member policy', async () => {
    const mocks = mockGovernance()
    const user = userEvent.setup()
    renderPage()

    expect(await screen.findByRole('heading', { name: 'DWS 字段治理队列' })).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: '编辑' }))
    const dialog = screen.getByRole('dialog', { name: '编辑维度候选' })
    const name = within(dialog).getByLabelText('候选维度名称')
    await user.clear(name)
    await user.type(name, '智家生态圈')
    await user.selectOptions(within(dialog).getByLabelText('候选维度类型'), 'CUSTOMER')
    await user.click(within(dialog).getByRole('checkbox', { name: /敏感维度/ }))
    expect(within(dialog).getByLabelText('候选成员索引策略')).toHaveValue('NONE')
    await user.click(within(dialog).getByRole('button', { name: '保存治理信息' }))

    await waitFor(() => expect(mocks.updateCandidate).toHaveBeenCalledWith('candidate-1', {
      expectedVersion: 3,
      code: 'ecosystem',
      name: '智家生态圈',
      description: '组织归属维度',
      dimensionType: 'CUSTOMER',
      memberIndexPolicy: 'NONE',
      highCardinality: false,
      sensitive: true,
    }))
    expect(await screen.findByText(/风险标记和策略只能保持或收紧/)).toBeInTheDocument()
  })

  test('keeps candidate acceptance explicitly not ready for inverted-index search', async () => {
    const mocks = mockGovernance()
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('button', { name: '接收' }))
    expect(mocks.acceptCandidate).toHaveBeenCalledWith('candidate-1', 3)
    expect(await screen.findByText(/当前倒排尚未就绪/)).toBeInTheDocument()
    expect(screen.getByText(/等待任务成功并验证维度—指标兼容关系/)).toBeInTheDocument()
    expect(screen.queryByText('倒排已就绪')).not.toBeInTheDocument()
  })

  test('shows aggregate profile evidence and blocks unsafe FULL while profiling is pending', async () => {
    const mocks = mockGovernance()
    mocks.listCandidates.mockResolvedValue({
      items: [surveyCandidate({
        profile: {
          ...surveyCandidate().profile,
          id: 'profile-pending',
          status: 'QUEUED',
          rowCount: undefined,
          nonNullCount: undefined,
          nullCount: undefined,
          distinctCount: undefined,
          distinctRatio: undefined,
          recommendedMemberIndexPolicy: undefined,
          attempt: 0,
          completedAt: undefined,
        },
      })],
      total: 1,
      limit: 200,
      offset: 0,
    })
    const user = userEvent.setup()
    renderPage()

    expect(await screen.findByText('等待画像')).toBeInTheDocument()
    expect(screen.getByText(/当前画像尚未完成，只能以 NONE 安全接收/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '接收' })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: '编辑' }))
    const dialog = screen.getByRole('dialog', { name: '编辑维度候选' })
    expect(within(dialog).getByLabelText('候选成员索引策略')).toHaveValue('NONE')
    expect(within(dialog).getByRole('option', { name: /FULL/ })).toBeDisabled()
  })

  test('renders measured NDV and null rate without exposing sampled values', async () => {
    mockGovernance()
    renderPage()

    expect(await screen.findByText('实测完成')).toBeInTheDocument()
    expect(screen.getByText('5%')).toBeInTheDocument()
    expect(screen.getByText('12')).toBeInTheDocument()
    expect(screen.queryByText(/样本值/)).not.toBeInTheDocument()
  })

  test('rejects candidates only with an auditable reason', async () => {
    const mocks = mockGovernance()
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('button', { name: '拒绝' }))
    const dialog = screen.getByRole('dialog', { name: '拒绝维度候选' })
    await user.type(within(dialog).getByLabelText('候选拒绝原因'), '该字段是展示属性，不是稳定分析维度')
    await user.click(within(dialog).getByRole('button', { name: '确认拒绝' }))

    await waitFor(() => expect(mocks.rejectCandidate).toHaveBeenCalledWith(
      'candidate-1',
      3,
      '该字段是展示属性，不是稳定分析维度',
    ))
  })

  test('closes the dimension governance loop with refresh, aliases and compatibility verification', async () => {
    const mocks = mockGovernance()
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('tab', { name: '正式维度与成员' }))
    expect(await screen.findByText(/成员快照已生成/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '查看治理详情' }))
    const detail = await screen.findByRole('dialog', { name: '生态圈治理详情' })

    expect(within(detail).getByText('智家生态圈')).toBeInTheDocument()
    expect(within(detail).getByText(/还需至少一条 VERIFIED/)).toBeInTheDocument()
    await user.click(within(detail).getByRole('button', { name: '提交成员刷新' }))
    await waitFor(() => expect(mocks.createRefreshJob).toHaveBeenCalledWith(
      'dimension-1',
      2,
      expect.any(String),
    ))
    expect(await screen.findByText(/倒排需等任务成功/)).toBeInTheDocument()

    await user.click(within(detail).getByRole('button', { name: '新增别名' }))
    const aliasDialog = screen.getByRole('dialog', { name: '新增维度成员别名' })
    await user.type(within(aliasDialog).getByLabelText('维度成员别名'), '690')
    await user.click(within(aliasDialog).getByRole('button', { name: '新增别名' }))
    await waitFor(() => expect(mocks.createAlias).toHaveBeenCalledWith({
      dimensionId: 'dimension-1',
      dimensionMemberId: 'member-1',
      alias: '690',
      aliasType: 'LEGACY',
    }))

    await user.click(within(detail).getByRole('button', { name: '验证关系' }))
    await waitFor(() => expect(mocks.verifyCompatibility).toHaveBeenCalledWith('compatibility-1', 1))
    expect(await screen.findByText(/只有成员索引也可用时/)).toBeInTheDocument()
  })

  test('uses global DATASET permissions and leaves governance mutations disabled in read-only mode', async () => {
    const mocks = mockGovernance(false)
    renderPage()

    expect(await screen.findByText(/当前为只读模式/)).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: '编辑' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '接收' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '拒绝' })).toBeDisabled()
    expect(mocks.permission).toHaveBeenCalledWith('READ')
    expect(mocks.permission).toHaveBeenCalledWith('MANAGE')
  })
})
