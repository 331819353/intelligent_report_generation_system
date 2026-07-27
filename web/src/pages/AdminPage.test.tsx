import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { dataSourceAPI, type DataSourceRecord, type DataSourcePublicationRequest } from '../lib/data-sources'
import {
  datasetAPI,
  type DatasetPublicationRequest,
  type DatasetSummary,
} from '../lib/datasets'
import { metricCandidateAPI, type MetricCandidate } from '../lib/metric-candidates'
import { metricAPI } from '../lib/metrics'
import {
  semanticGovernanceAPI,
  type DimensionSurveyCandidate,
} from '../lib/semantic-governance'
import { backgroundTaskAPI, type BackgroundTask } from '../lib/background-tasks'
import { AdminPage } from './AdminPage'

const accessTokenFor = (subject: string) => `header.${btoa(JSON.stringify({ sub: subject }))}.signature`
const pendingSource = (overrides: Partial<DataSourceRecord> = {}): DataSourceRecord => ({
  id: 'source-1',
  tenantId: 'tenant-1',
  code: 'takeout_mysql',
  name: '外卖订单库',
  type: 'MYSQL',
  status: 'DRAFT',
  config: { host: 'host.docker.internal', port: 13306, database: 'takeout_master', username: 'takeout_user' },
  configVersionId: 'version-4',
  configVersion: 4,
  validationStatus: 'PASSED',
  publicationStatus: 'UNPUBLISHED',
  hasUnpublishedChanges: true,
  reviewStatus: 'PENDING',
  reviewRequestId: 'review-1',
  reviewRequestVersion: 1,
  reviewRequesterId: 'requester-1',
  reviewSubmittedAt: '2026-07-24T08:30:00Z',
  version: 4,
  ...overrides,
})
const request = (overrides: Partial<DataSourcePublicationRequest> = {}): DataSourcePublicationRequest => ({
  id: 'review-1',
  dataSourceId: 'source-1',
  configVersionId: 'version-4',
  configHash: 'a'.repeat(64),
  status: 'PENDING',
  version: 1,
  requesterUserId: 'requester-1',
  requestNote: '',
  submittedAt: '2026-07-24T08:30:00Z',
  updatedAt: '2026-07-24T08:30:00Z',
  ...overrides,
})
const pendingDataset: DatasetSummary = {
  id: 'dataset-1',
  code: 'sales_detail',
  name: '销售明细',
  description: '',
  type: 'DWD',
  layer: 'DWD',
  tags: [],
  status: 'DRAFT',
  version: 4,
  dslHash: 'a'.repeat(64),
  updatedAt: '2026-07-24T08:00:00Z',
}
const pendingDatasetRequest: DatasetPublicationRequest = {
  id: 'dataset-review-1',
  datasetId: 'dataset-1',
  status: 'PENDING',
  version: 1,
  draftVersionId: 'draft-4',
  expectedDatasetVersion: 4,
  expectedDraftRecordVersion: 3,
  expectedDslHash: 'a'.repeat(64),
  expectedPlanHash: 'b'.repeat(64),
  requesterId: 'requester-1',
  requestNote: '用于正式指标',
  submittedAt: '2026-07-24T08:30:00Z',
  updatedAt: '2026-07-24T08:30:00Z',
  metricCandidateStatus: 'SUCCEEDED',
  metricCandidateTotal: 2,
  metricCandidateReady: 1,
  metricCandidateReview: 1,
  metricCandidateBlocked: 0,
}

beforeEach(() => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({
    accessToken: accessTokenFor('reviewer-1'),
    refreshToken: 'refresh',
  }))
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [] })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue({ items: [], total: 0, limit: 200, offset: 0 })
  vi.spyOn(metricAPI, 'evaluatePermission').mockResolvedValue({ allowed: false })
  vi.spyOn(semanticGovernanceAPI, 'evaluatePermission').mockResolvedValue({ allowed: false })
  vi.spyOn(backgroundTaskAPI, 'list').mockResolvedValue({
    items: [],
    activeCount: 0,
    generatedAt: '2026-07-25T04:00:00Z',
  })
})

test('后台任务入口展示真实进度并可以中止运行任务', async () => {
  const task: BackgroundTask = {
    id: 'job-12345678',
    kind: 'DATA_SOURCE_METADATA',
    kindLabel: '数据表元数据完善',
    name: '123',
    description: 'takeout_master · IMPORT · FULL · LLM',
    status: 'RUNNING',
    sourceStatus: 'RUNNING',
    resourceType: 'DATA_SOURCE',
    resourceId: 'source-1',
    processed: 2,
    total: 4,
    progressPercent: 50,
    progressText: '已处理 2 / 4',
    attempt: 1,
    maxAttempts: 3,
    canCancel: true,
    canRetry: false,
    createdAt: '2026-07-25T03:55:00Z',
    startedAt: '2026-07-25T03:56:00Z',
    updatedAt: '2026-07-25T03:59:00Z',
  }
  vi.mocked(backgroundTaskAPI.list).mockResolvedValue({
    items: [task],
    activeCount: 1,
    generatedAt: '2026-07-25T04:00:00Z',
  })
  const cancel = vi.spyOn(backgroundTaskAPI, 'cancel').mockResolvedValue({
    ...task,
    status: 'CANCELLED',
    sourceStatus: 'FAILED',
    errorCode: 'USER_CANCELLED',
    progressPercent: 100,
    progressText: '已中止',
    canCancel: false,
    cancelDisabledReason: '任务已经结束',
    canRetry: false,
    retryDisabledReason: '该任务当前不支持安全重试',
  })
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '后台任务 1' }))
  const dialog = screen.getByRole('dialog', { name: '任务运行中心' })
  expect(within(dialog).getByText('数据表元数据完善')).toBeInTheDocument()
  expect(within(dialog).getByText('已处理 2 / 4')).toBeInTheDocument()
  expect(within(dialog).getByRole('progressbar', { name: '123进度' })).toHaveValue(50)

  await user.click(within(dialog).getByRole('button', { name: '中止' }))
  expect(cancel).toHaveBeenCalledWith(task)
  expect(await within(dialog).findByText('“123”的数据表元数据完善任务已中止')).toBeInTheDocument()
})

test('主题建模后任务中心优先展示主题任务并隐藏无关自动处理', async () => {
  const dwsTask: BackgroundTask = {
    id: 'dws-job-1',
    kind: 'DWS_MODELING',
    kindLabel: 'LLM 市场通用 DWS 建模',
    name: '综合分析主题建模',
    description: '4 张 DWD · 5 张 DIM 规划上下文',
    status: 'SUCCEEDED',
    sourceStatus: 'SUCCEEDED',
    resourceType: 'DATASET',
    resourceId: 'dwd-1',
    progressPercent: 100,
    progressText: '执行成功',
    attempt: 1,
    maxAttempts: 5,
    canCancel: false,
    canRetry: false,
    createdAt: '2026-07-26T15:42:52Z',
    updatedAt: '2026-07-26T15:42:53Z',
    completedAt: '2026-07-26T15:42:53Z',
  }
  const tagTask: BackgroundTask = {
    ...dwsTask,
    id: 'tag-job-1',
    kind: 'DATASET_TAG_SUGGESTION',
    kindLabel: '数据集标签建议',
    name: '订单事实明细表',
    status: 'RUNNING',
    sourceStatus: 'RUNNING',
    progressPercent: undefined,
    progressText: '正在执行，暂时无法估算剩余时间',
    canCancel: true,
    completedAt: undefined,
  }
  vi.mocked(backgroundTaskAPI.list).mockResolvedValue({
    items: [tagTask, dwsTask],
    activeCount: 1,
    generatedAt: '2026-07-26T15:43:00Z',
  })
  sessionStorage.setItem(
    'intelligent-report-background-task-focus',
    'DWS_MODELING',
  )
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '后台任务 1' }))
  const dialog = screen.getByRole('dialog', { name: '任务运行中心' })
  expect(await within(dialog).findByText(
    '当前仅显示主题建模任务，已隐藏无关的自动标签、指标提取等任务。',
  )).toBeInTheDocument()
  expect(within(dialog).getByText('综合分析主题建模')).toBeInTheDocument()
  expect(within(dialog).queryByText('订单事实明细表')).not.toBeInTheDocument()
  expect(backgroundTaskAPI.list).toHaveBeenCalledWith('ALL')

  await user.click(within(dialog).getByRole('button', { name: '显示全部任务' }))
  expect(within(dialog).getByText('订单事实明细表')).toBeInTheDocument()
})

test('失败的分层建模任务可以从任务运行中心重试', async () => {
  const task: BackgroundTask = {
    id: 'job-dwd-retry',
    kind: 'DWD_FACT_MODELING',
    kindLabel: '事实落地',
    name: '订单事实表',
    description: 'mapped_orders · 领域:运营 · LLM 分层建模',
    status: 'FAILED',
    sourceStatus: 'FAILED',
    resourceType: 'DATASET',
    resourceId: 'dataset-orders',
    progressPercent: 100,
    progressText: '执行失败',
    attempt: 3,
    maxAttempts: 3,
    canCancel: false,
    cancelDisabledReason: '任务已经结束',
    canRetry: true,
    errorCode: 'WAREHOUSE_MODELING_INVALID_OUTPUT',
    errorMessage: '建模输出未通过校验',
    createdAt: '2026-07-25T03:55:00Z',
    updatedAt: '2026-07-25T03:59:00Z',
    completedAt: '2026-07-25T03:59:00Z',
  }
  vi.mocked(backgroundTaskAPI.list).mockResolvedValue({
    items: [task],
    activeCount: 0,
    generatedAt: '2026-07-25T04:00:00Z',
  })
  const retry = vi.spyOn(backgroundTaskAPI, 'retry').mockResolvedValue({
    ...task,
    status: 'QUEUED',
    sourceStatus: 'PENDING',
    attempt: 0,
    progressPercent: 0,
    progressText: '等待 worker 领取',
    canRetry: false,
    retryDisabledReason: '任务仍在运行',
    errorCode: undefined,
    errorMessage: undefined,
    completedAt: undefined,
  })
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '后台任务 0' }))
  const dialog = screen.getByRole('dialog', { name: '任务运行中心' })
  await user.click(within(dialog).getByRole('button', { name: '最近完成' }))
  await user.click(await within(dialog).findByRole('button', { name: '重试' }))

  expect(retry).toHaveBeenCalledWith(task)
  expect(await within(dialog).findByText(
    '“订单事实表”的事实落地任务已重新提交',
  )).toBeInTheDocument()
})

afterEach(() => {
  vi.restoreAllMocks()
  sessionStorage.clear()
})

test('待处理任务卡片打开统一审批中心并按分类展示数据源任务', async () => {
  const source = pendingSource()
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source] })
  vi.spyOn(dataSourceAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  const approve = vi.spyOn(dataSourceAPI, 'approvePublicationRequest').mockResolvedValue({
    request: request({ status: 'APPROVED', version: 2 }),
    source: { ...source, status: 'ACTIVE', reviewStatus: 'APPROVED', publicationStatus: 'PUBLISHED' },
  })
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  const taskCard = await screen.findByRole('button', { name: '待处理任务 1' })
  await user.click(taskCard)

  const dialog = screen.getByRole('dialog', { name: '统一审批中心' })
  expect(within(dialog).getByRole('button', { name: /数据源\s*1/ })).toHaveAttribute('aria-current', 'page')
  expect(within(dialog).getByRole('button', { name: /数据集\s*0/ })).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: /指标\s*0/ })).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: /维度\s*0/ })).toBeInTheDocument()
  expect(within(dialog).getByRole('heading', { level: 3, name: '外卖订单库' })).toBeInTheDocument()
  expect(within(dialog).getByText('host.docker.internal')).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '审批通过' })).toBeEnabled()
  expect(within(dialog).getByRole('button', { name: '驳回' })).toBeDisabled()
  expect(within(dialog).queryByRole('button', { name: '发布' })).not.toBeInTheDocument()

  await user.click(within(dialog).getByRole('button', { name: '审批通过' }))
  expect(approve).toHaveBeenCalledWith('source-1', 'review-1', 1, '')
  expect(await within(dialog).findByText(/已审批通过，测试版本已自动上线/)).toBeInTheDocument()
  expect(await screen.findByRole('button', { name: '待处理任务 0' })).toBeInTheDocument()
})

test('驳回必须填写审批意见并从工作台完成', async () => {
  const source = pendingSource()
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source] })
  vi.spyOn(dataSourceAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  const reject = vi.spyOn(dataSourceAPI, 'rejectPublicationRequest').mockResolvedValue(request({ status: 'REJECTED', version: 2, reviewNote: '请改用只读账号' }))
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '待处理任务 1' }))
  const dialog = screen.getByRole('dialog', { name: '统一审批中心' })
  await user.type(within(dialog).getByLabelText(/审批意见/), '请改用只读账号')
  await user.click(within(dialog).getByRole('button', { name: '驳回' }))

  expect(reject).toHaveBeenCalledWith('source-1', 'review-1', 1, '请改用只读账号')
  expect(await within(dialog).findByText('已驳回“外卖订单库”的数据源发布申请')).toBeInTheDocument()
})

test('具备发布权限的管理员可查看并审批自己的数据源申请', async () => {
  const source = pendingSource({ reviewRequesterId: 'reviewer-1' })
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source] })
  const permission = vi.spyOn(dataSourceAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  const approve = vi.spyOn(dataSourceAPI, 'approvePublicationRequest').mockResolvedValue({
    request: request({ status: 'APPROVED', version: 2, requesterUserId: 'reviewer-1' }),
    source: { ...source, status: 'ACTIVE', reviewStatus: 'APPROVED', publicationStatus: 'PUBLISHED' },
  })
  const reject = vi.spyOn(dataSourceAPI, 'rejectPublicationRequest')
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '待处理任务 1' }))
  const dialog = screen.getByRole('dialog', { name: '统一审批中心' })
  expect(within(dialog).getByText(/MYSQL · takeout_mysql · 我的申请/)).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '审批通过' })).toBeEnabled()
  await user.click(within(dialog).getByRole('button', { name: '审批通过' }))
  expect(permission).toHaveBeenCalledWith('source-1', 'PUBLISH')
  expect(approve).toHaveBeenCalledWith('source-1', 'review-1', 1, '')
  expect(reject).not.toHaveBeenCalled()
})

test('统一审批中心聚合数据集发布申请并可直接审批', async () => {
  vi.mocked(datasetAPI.list).mockResolvedValue({
    items: [pendingDataset],
    total: 1,
    limit: 200,
    offset: 0,
  })
  vi.spyOn(datasetAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  vi.spyOn(datasetAPI, 'listPublicationRequests').mockResolvedValue({
    items: [{ ...pendingDatasetRequest, metricCandidateStatus: 'PENDING', metricCandidateTotal: 0, metricCandidateReady: 0, metricCandidateReview: 0 }],
    total: 1,
    limit: 200,
    offset: 0,
  })
  const approve = vi.spyOn(datasetAPI, 'approvePublication').mockResolvedValue({
    request: { ...pendingDatasetRequest, status: 'APPROVED', version: 2 },
    publishedVersion: {
      id: 'published-1',
      datasetId: 'dataset-1',
      versionNo: 1,
      status: 'PUBLISHED',
      dslVersion: '1.0',
      dslHash: 'a'.repeat(64),
      planHash: 'b'.repeat(64),
      dsl: { dslVersion: '1.0', dataset: { code: 'sales_detail', name: '销售明细', type: 'DWD' }, nodes: [], fields: [] },
      logicalPlan: {},
      publishedAt: '2026-07-24T09:00:00Z',
      publishedBy: 'reviewer-1',
      datasetRecordVersion: 5,
      draftVersionId: 'draft-4',
      draftRecordVersion: 3,
    },
  })
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '待处理任务 1' }))
  const dialog = screen.getByRole('dialog', { name: '统一审批中心' })
  expect(within(dialog).getByRole('button', { name: /数据集\s*1/ })).toHaveAttribute('aria-current', 'page')
  expect(within(dialog).getByRole('heading', { level: 3, name: '销售明细' })).toBeInTheDocument()
  expect(within(dialog).getByText('待审批')).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '审批通过' })).toBeEnabled()

  await user.click(within(dialog).getByRole('button', { name: '审批通过' }))
  expect(approve).toHaveBeenCalledWith('dataset-1', 'dataset-review-1', 1, '')
  expect(await within(dialog).findByText(/已审批通过；不可变发布版本已生成，后台加工任务已启动/)).toBeInTheDocument()
  expect(await screen.findByRole('button', { name: '待处理任务 0' })).toBeInTheDocument()
})

test('统一审批中心把指标候选和维度候选归入独立分类入口', async () => {
  const metricCandidate = {
    id: 'metric-candidate-1',
    name: '订单金额',
    code: 'order_amount',
    description: '统计订单金额',
    status: 'NEEDS_REVIEW',
    method: 'HYBRID',
    confidence: 0.86,
    sourceFieldIds: ['amount'],
    warnings: ['需要确认退款口径'],
    semantic: { caliber: '有效订单金额求和' },
    createdAt: '2026-07-24T08:30:00Z',
  } as MetricCandidate
  const dimensionCandidate = {
    id: 'dimension-candidate-1',
    proposedName: '区域',
    proposedDescription: '订单所属区域',
    proposedDimensionType: 'GEOGRAPHY',
    proposedMemberIndexPolicy: 'FULL',
    fieldCode: 'region',
    fieldRole: 'DIMENSION',
    riskSensitive: false,
    riskHighCardinality: false,
    createdAt: '2026-07-24T08:31:00Z',
  } as DimensionSurveyCandidate
  vi.mocked(metricAPI.evaluatePermission).mockResolvedValue({ allowed: true })
  vi.spyOn(metricCandidateAPI, 'list').mockResolvedValue({
    items: [metricCandidate],
    total: 1,
    limit: 200,
    offset: 0,
  })
  vi.mocked(semanticGovernanceAPI.evaluatePermission).mockResolvedValue({ allowed: true })
  vi.spyOn(semanticGovernanceAPI, 'listCandidates').mockResolvedValue({
    items: [dimensionCandidate],
    total: 1,
    limit: 200,
    offset: 0,
  })
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '待处理任务 2' }))
  const dialog = screen.getByRole('dialog', { name: '统一审批中心' })
  expect(within(dialog).getByRole('button', { name: /指标\s*1/ })).toHaveAttribute('aria-current', 'page')
  expect(within(dialog).getByRole('heading', { level: 3, name: '订单金额' })).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '前往指标审批' })).toBeInTheDocument()

  await user.click(within(dialog).getByRole('button', { name: /维度\s*1/ }))
  expect(within(dialog).getByRole('heading', { level: 3, name: '区域' })).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '前往维度治理' })).toBeInTheDocument()
})
