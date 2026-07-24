import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { dataSourceAPI, type DataSourceRecord, type DataSourcePublicationRequest } from '../lib/data-sources'
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

beforeEach(() => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({
    accessToken: accessTokenFor('reviewer-1'),
    refreshToken: 'refresh',
  }))
})

afterEach(() => {
  vi.restoreAllMocks()
  sessionStorage.clear()
})

test('待处理任务卡片打开数据源审批弹窗且审批区不出现发布按钮', async () => {
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

  const dialog = screen.getByRole('dialog', { name: '数据源发布审批' })
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
  const dialog = screen.getByRole('dialog', { name: '数据源发布审批' })
  await user.type(within(dialog).getByLabelText(/审批意见/), '请改用只读账号')
  await user.click(within(dialog).getByRole('button', { name: '驳回' }))

  expect(reject).toHaveBeenCalledWith('source-1', 'review-1', 1, '请改用只读账号')
  expect(await within(dialog).findByText('已驳回“外卖订单库”的发布申请')).toBeInTheDocument()
})

test('本人提交的申请不进入自己的待审批队列', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [pendingSource({ reviewRequesterId: 'reviewer-1' })] })
  const permission = vi.spyOn(dataSourceAPI, 'evaluatePermission')
  const user = userEvent.setup()
  render(<MemoryRouter><AdminPage /></MemoryRouter>)

  await user.click(await screen.findByRole('button', { name: '待处理任务 0' }))
  const dialog = screen.getByRole('dialog', { name: '数据源发布审批' })
  expect(within(dialog).getByText('当前没有待审批的数据源')).toBeInTheDocument()
  expect(within(dialog).getByText('本人提交的申请不会进入自己的审批队列。')).toBeInTheDocument()
  expect(permission).not.toHaveBeenCalled()
})
