import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { dataSourceAPI, type DataSourceColumnRecord, type DataSourceRecord, type DataSourceTableRecord, type MetadataJob } from '../lib/data-sources'
import { DataSourceCenterPage } from './DataSourceCenterPage'

beforeEach(() => {
  vi.spyOn(dataSourceAPI, 'latestActiveMetadataJob').mockResolvedValue({ job: null })
})
afterEach(() => vi.restoreAllMocks())

const source = (overrides: Partial<DataSourceRecord> = {}): DataSourceRecord => ({
  id: 'source-1', tenantId: 'tenant-1', code: 'sales_mysql', name: '销售业务库', type: 'MYSQL', status: 'ACTIVE',
  config: { host: 'mysql.internal', port: 3306, database: 'sales', username: 'report_reader' }, version: 3, ...overrides,
})

const renderPage = () => render(<MemoryRouter><DataSourceCenterPage /></MemoryRouter>)
const cardFor = (name: string) => screen.getByRole('heading', { level: 3, name }).closest('article') as HTMLElement
const job = (overrides: Partial<MetadataJob> = {}): MetadataJob => ({
  id: 'job-1', dataSourceId: 'source-1', kind: 'IMPORT', mode: 'FULL', status: 'QUEUED', stage: 'QUEUED', total: 1, completed: 0, succeeded: 0, skipped: 0, failed: 0, createdAt: '2026-07-17T01:00:00Z', ...overrides,
})
const metadataTable: DataSourceTableRecord = {
  id: 'table-1', dataSourceId: 'source-1', catalogName: 'sales', schemaName: 'public', tableName: 'orders', tableType: 'TABLE', businessName: '订单表', businessDescription: '订单交易明细', tags: ['主题:经营分析'], sensitivityLevel: 'INTERNAL', visibility: 'PRIVATE', manualLocked: false, businessVersion: 2, managementStatus: 'ENABLED', enrichmentStatus: 'SUCCEEDED', columnCount: 2, metadataVersion: 3, lastSyncAt: '2026-07-17T01:00:00Z',
}
const metadataColumns: DataSourceColumnRecord[] = [
  { id: 'column-1', tableId: 'table-1', columnName: 'order_id', ordinalPosition: 1, sourceComment: '主键', nativeType: 'bigint', canonicalType: 'INTEGER', nullable: false, businessName: '订单编号', businessDescription: '订单唯一编号', tags: ['作用:主数据'], sensitivityLevel: 'INTERNAL', semanticType: 'IDENTIFIER', manualLocked: false, assetStatus: 'ACTIVE', businessVersion: 3 },
  { id: 'column-2', tableId: 'table-1', columnName: 'amount', ordinalPosition: 2, sourceComment: '含税金额', nativeType: 'decimal(18,2)', canonicalType: 'DECIMAL', nullable: false, businessName: '订单金额', businessDescription: '订单含税金额', tags: ['作用:指标来源'], sensitivityLevel: 'CONFIDENTIAL', semanticType: 'AMOUNT', manualLocked: false, assetStatus: 'ACTIVE', businessVersion: 2 },
]

test('数据源连接摘要只在卡片中展示，表资产弹窗不重复展示', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [], total: 0 })
  const user = userEvent.setup()
  renderPage()

  await screen.findByRole('list', { name: '已有数据源清单' })
  const card = within(cardFor('销售业务库'))
  expect(card.getByText('mysql.internal')).toBeInTheDocument()
  expect(card.getByText('3306')).toBeInTheDocument()
  expect(card.getByText('sales')).toBeInTheDocument()
  expect(card.getByText('report_reader')).toBeInTheDocument()
  expect(card.queryByText(/password|secretRef/i)).not.toBeInTheDocument()
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))

  const dialog = screen.getByRole('dialog', { name: '数据表资产' })
  expect(dialog).not.toHaveTextContent('mysql.internal')
  expect(dialog).not.toHaveTextContent('report_reader')
  expect(dialog).not.toHaveTextContent('已安全保存，不可查看')
  expect(dialog).not.toHaveTextContent('secret')
})

test('点击数据源卡片展示全部表结构并提供管理操作', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  const tables = vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  const columns = vi.spyOn(dataSourceAPI, 'columns').mockResolvedValue({ items: metadataColumns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '管理销售业务库的数据表资产' }))
  const dialog = await screen.findByRole('dialog', { name: '数据表资产' })

  expect(tables).toHaveBeenCalledWith('source-1')
  expect(within(dialog).getByRole('button', { name: '新增数据表' })).toBeEnabled()
  expect(within(dialog).getByRole('button', { name: '开始增量刷新' })).toBeEnabled()
  expect(within(dialog).getByRole('option', { name: '增量刷新（仅变化字段）' })).toBeInTheDocument()
  expect(within(dialog).getByRole('option', { name: '全量刷新（全部重新处理）' })).toBeInTheDocument()
  expect(within(dialog).getByRole('note')).toHaveTextContent('未变化字段保留现有完善结果')
  expect(within(dialog).getByRole('note')).toHaveTextContent('源表被删除时停用对应资产')
  expect(within(dialog).queryByRole('button', { name: '测试连接' })).not.toBeInTheDocument()
  await user.click(await within(dialog).findByText('订单表'))
  expect(columns).toHaveBeenCalledWith('table-1')
  expect(await within(dialog).findByText('订单金额')).toBeInTheDocument()
  expect(within(dialog).getByText('decimal(18,2)')).toBeInTheDocument()
  const tableActions = within(dialog).getByLabelText('订单表操作')
  expect(within(tableActions).getByRole('button', { name: '修改' })).toBeEnabled()
  expect(within(tableActions).getByRole('button', { name: '刷新结构' })).toBeEnabled()
  expect(within(tableActions).getByRole('button', { name: '停用' })).toBeEnabled()
  expect(within(tableActions).getByRole('button', { name: '删除' })).toBeEnabled()
})

test('新增数据表时可发现源库表并全选后导入', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [], total: 0 })
  vi.spyOn(dataSourceAPI, 'discoverTables').mockResolvedValue({ items: [
    { catalogName: 'sales', schemaName: 'public', name: 'orders', type: 'TABLE', sourceComment: '', columns: [{ name: 'id', nativeType: 'bigint', canonicalType: 'INTEGER', nullable: false }] },
    { catalogName: 'sales', schemaName: 'public', name: 'customers', type: 'TABLE', sourceComment: '', columns: [{ name: 'id', nativeType: 'bigint', canonicalType: 'INTEGER', nullable: false }] },
  ], total: 2 })
  const importTables = vi.spyOn(dataSourceAPI, 'importTables').mockResolvedValue(job({ total: 2 }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '管理销售业务库的数据表资产' }))
  await user.click(await screen.findByRole('button', { name: '新增数据表' }))
  const picker = await screen.findByRole('dialog', { name: '新增数据表' })
  await user.click(within(picker).getByRole('checkbox', { name: '全选可新增表' }))
  await user.click(within(picker).getByRole('button', { name: '新增 2 张表' }))

  expect(importTables).toHaveBeenCalledWith('source-1', [
    { catalogName: 'sales', schemaName: 'public', tableName: 'orders' },
    { catalogName: 'sales', schemaName: 'public', tableName: 'customers' },
  ])
  expect(dataSourceAPI.discoverTables).toHaveBeenCalledWith('source-1')
  expect(await screen.findByText('已提交 2 张表的后台采样与 LLM 完善任务，可关闭当前弹窗')).toBeInTheDocument()
  expect(screen.getByRole('progressbar', { name: '元数据任务进度' })).toHaveAttribute('value', '0')
})

test('默认以增量模式提交后台刷新，也可切换为全量刷新', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  const refreshTables = vi.spyOn(dataSourceAPI, 'refreshTables').mockResolvedValue(job({ id: 'job-refresh', kind: 'REFRESH', mode: 'FULL' }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  const dialog = await screen.findByRole('dialog', { name: '数据表资产' })
  expect(within(dialog).getByLabelText('元数据刷新方式')).toHaveValue('INCREMENTAL')
  await user.selectOptions(within(dialog).getByLabelText('元数据刷新方式'), 'FULL')
  await user.click(within(dialog).getByRole('button', { name: '开始全量刷新' }))

  expect(refreshTables).toHaveBeenCalledWith('source-1', 'FULL')
  expect(await screen.findByText('已提交全量元数据后台刷新任务，可关闭当前弹窗')).toBeInTheDocument()
})

test('没有可刷新表时立即展示完成态而不是无限等待', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  const tables = vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [], total: 0 })
  const refreshTables = vi.spyOn(dataSourceAPI, 'refreshTables').mockResolvedValue(job({
    id: 'job-empty', kind: 'REFRESH', mode: 'INCREMENTAL', status: 'SUCCEEDED', stage: 'COMPLETE', total: 0, completed: 0, completedAt: '2026-07-17T01:01:00Z',
  }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  const dialog = await screen.findByRole('dialog', { name: '数据表资产' })
  await user.click(within(dialog).getByRole('button', { name: '开始增量刷新' }))

  expect(refreshTables).toHaveBeenCalledWith('source-1', 'INCREMENTAL')
  expect(await screen.findByText('当前没有可刷新的数据表资产')).toBeInTheDocument()
  const task = screen.getByRole('region', { name: '元数据后台任务' })
  const progress = within(task).getByRole('progressbar', { name: '元数据任务进度' })
  expect(progress).toHaveAttribute('max', '1')
  expect(progress).toHaveAttribute('value', '1')
  expect(progress).toHaveAttribute('aria-valuetext', '已完成，无需处理数据表')
  expect(within(task).getByText('100%')).toBeInTheDocument()
  expect(tables).toHaveBeenCalledTimes(2)
})

test('单表刷新提交全量 REFRESH 任务且只包含当前表', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  vi.spyOn(dataSourceAPI, 'columns').mockResolvedValue({ items: metadataColumns })
  const importTables = vi.spyOn(dataSourceAPI, 'importTables')
  const refreshTables = vi.spyOn(dataSourceAPI, 'refreshTables').mockResolvedValue(job({ id: 'job-single-refresh', kind: 'REFRESH' }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  const dialog = await screen.findByRole('dialog', { name: '数据表资产' })
  await user.click(await within(dialog).findByText('订单表'))
  await user.click(within(within(dialog).getByLabelText('订单表操作')).getByRole('button', { name: '刷新结构' }))

  expect(refreshTables).toHaveBeenCalledWith('source-1', 'FULL', ['table-1'])
  expect(importTables).not.toHaveBeenCalled()
  const task = screen.getByRole('region', { name: '元数据后台任务' })
  expect(within(task).getByText('刷新表结构')).toBeInTheDocument()
  expect(within(task).queryByText('新增数据表')).not.toBeInTheDocument()
})

test('表资产修改支持字段业务映射且只提交发生变化的字段', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  vi.spyOn(dataSourceAPI, 'columns').mockResolvedValue({ items: metadataColumns })
  const updateTable = vi.spyOn(dataSourceAPI, 'updateTable').mockResolvedValue(metadataTable)
  const updateColumn = vi.spyOn(dataSourceAPI, 'updateColumn').mockResolvedValue({ ...metadataColumns[0], businessName: '订单ID', businessVersion: 4 })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  const assetDialog = await screen.findByRole('dialog', { name: '数据表资产' })
  await user.click(await within(assetDialog).findByText('订单表'))
  await within(assetDialog).findByText('订单编号')
  await user.click(within(within(assetDialog).getByLabelText('订单表操作')).getByRole('button', { name: '修改' }))

  const editDialog = await screen.findByRole('dialog', { name: '修改数据表资产' })
  const businessName = await within(editDialog).findByLabelText('order_id业务字段名称')
  expect(within(editDialog).getByLabelText('order_id语义类型')).toHaveValue('IDENTIFIER')
  await user.clear(businessName)
  await user.type(businessName, '订单ID')
  await user.click(within(editDialog).getByRole('button', { name: '保存修改' }))

  expect(updateTable).not.toHaveBeenCalled()
  expect(updateColumn).toHaveBeenCalledTimes(1)
  expect(updateColumn).toHaveBeenCalledWith('column-1', {
    businessName: '订单ID', businessDescription: '订单唯一编号', tags: ['作用:主数据'], sensitivityLevel: 'INTERNAL',
    semanticType: 'IDENTIFIER', manualLocked: false, expectedVersion: 3,
  })
  expect(await screen.findByRole('status')).toHaveTextContent('已修改表资产')
})

test('表与字段顺序保存，部分失败后重试不会重复提交已成功项', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  vi.spyOn(dataSourceAPI, 'columns').mockResolvedValue({ items: metadataColumns })
  const updatedTable = { ...metadataTable, businessName: '订单事实表', businessVersion: 3 }
  const updatedOrderID = { ...metadataColumns[0], businessName: '订单ID', businessVersion: 4 }
  const updatedAmount = { ...metadataColumns[1], businessName: '含税金额', businessVersion: 3 }
  const updateTable = vi.spyOn(dataSourceAPI, 'updateTable').mockResolvedValue(updatedTable)
  const updateColumn = vi.spyOn(dataSourceAPI, 'updateColumn')
    .mockResolvedValueOnce(updatedOrderID)
    .mockRejectedValueOnce(new Error('version conflict'))
    .mockResolvedValueOnce(updatedAmount)
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  const assetDialog = await screen.findByRole('dialog', { name: '数据表资产' })
  await user.click(await within(assetDialog).findByText('订单表'))
  await user.click(within(within(assetDialog).getByLabelText('订单表操作')).getByRole('button', { name: '修改' }))
  const editDialog = await screen.findByRole('dialog', { name: '修改数据表资产' })
  await within(editDialog).findByLabelText('order_id业务字段名称')

  await user.clear(within(editDialog).getByLabelText('业务名称'))
  await user.type(within(editDialog).getByLabelText('业务名称'), '订单事实表')
  await user.clear(within(editDialog).getByLabelText('order_id业务字段名称'))
  await user.type(within(editDialog).getByLabelText('order_id业务字段名称'), '订单ID')
  await user.clear(within(editDialog).getByLabelText('amount业务字段名称'))
  await user.type(within(editDialog).getByLabelText('amount业务字段名称'), '含税金额')
  const save = within(editDialog).getByRole('button', { name: '保存修改' })
  await user.click(save)

  expect(await within(editDialog).findByRole('alert')).toHaveTextContent('已保存 2 项；字段“amount”保存失败：version conflict。未保存修改已保留，请重试。')
  expect(updateTable).toHaveBeenCalledTimes(1)
  expect(updateColumn).toHaveBeenCalledTimes(2)
  expect(updateTable.mock.invocationCallOrder[0]).toBeLessThan(updateColumn.mock.invocationCallOrder[0])
  expect(updateColumn.mock.invocationCallOrder[0]).toBeLessThan(updateColumn.mock.invocationCallOrder[1])
  expect(updateColumn).toHaveBeenNthCalledWith(1, 'column-1', expect.objectContaining({ businessName: '订单ID', expectedVersion: 3 }))
  expect(updateColumn).toHaveBeenNthCalledWith(2, 'column-2', expect.objectContaining({ businessName: '含税金额', expectedVersion: 2 }))

  await waitFor(() => expect(save).toBeEnabled())
  await user.click(save)
  expect(await screen.findByText('已修改表资产“订单事实表”')).toBeInTheDocument()
  expect(updateTable).toHaveBeenCalledTimes(1)
  expect(updateColumn).toHaveBeenCalledTimes(3)
  expect(updateColumn).toHaveBeenNthCalledWith(3, 'column-2', expect.objectContaining({ businessName: '含税金额', expectedVersion: 2 }))
})

test('恢复后台任务后展示真实进度并在终态刷新资产和消息', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  const tables = vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  vi.mocked(dataSourceAPI.latestActiveMetadataJob).mockResolvedValue({ job: job({ id: 'job-progress', kind: 'REFRESH', mode: 'INCREMENTAL', total: 3 }) })
  vi.spyOn(dataSourceAPI, 'getMetadataJob').mockResolvedValue(job({ id: 'job-progress', kind: 'REFRESH', mode: 'INCREMENTAL', status: 'SUCCEEDED', stage: 'COMPLETE', total: 3, completed: 3, succeeded: 2, skipped: 1 }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  const progress = await screen.findByRole('progressbar', { name: '元数据任务进度' })
  expect(progress).toHaveAttribute('max', '3')
  expect(progress).toHaveAttribute('value', '0')
  expect(progress).toHaveAttribute('aria-valuetext', '已处理 0 / 3 张，等待后台执行')
  expect(within(screen.getByRole('region', { name: '元数据后台任务' })).getByRole('status')).toHaveAttribute('aria-live', 'polite')

  expect(await screen.findByText('增量刷新完成：2 张成功，跳过 1 张未变化表', {}, { timeout: 2500 })).toBeInTheDocument()
  expect(progress).toHaveAttribute('value', '3')
  expect(progress).toHaveAttribute('aria-valuetext', '已处理 3 / 3 张，已完成')
  expect(dataSourceAPI.getMetadataJob).toHaveBeenCalledWith('source-1', 'job-progress')
  expect(tables).toHaveBeenCalledTimes(2)
})

test.each(['PARTIAL', 'FAILED'] as const)('%s 任务在进度卡内展示逐表安全失败原因', async status => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  vi.mocked(dataSourceAPI.latestActiveMetadataJob).mockResolvedValue({ job: job({
    id: `job-${status.toLowerCase()}`,
    status,
    stage: status === 'FAILED' ? 'FAILED' : 'COMPLETE',
    total: 2,
    completed: 2,
    succeeded: status === 'PARTIAL' ? 1 : 0,
    failed: status === 'PARTIAL' ? 1 : 2,
    failures: [{
      catalogName: 'sales',
      schemaName: 'public',
      tableName: 'orders',
      errorCode: 'LLM_COMPLETION_FAILED',
      errorMessage: 'LLM 表结构完善失败',
    }],
  }) })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))

  const progressCard = await screen.findByRole('region', { name: '元数据后台任务' })
  const failures = within(progressCard).getByRole('list', { name: '逐表失败明细' })
  expect(failures).toHaveTextContent('public.orders：LLM 表结构完善失败')
  expect(document.querySelector('.data-source-toast')).toBeNull()
})

test('关闭弹窗后轮询临时失败仍自动重试并展示最终结果', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  const tables = vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  vi.mocked(dataSourceAPI.latestActiveMetadataJob).mockResolvedValue({ job: job({ id: 'job-retry', kind: 'REFRESH', mode: 'INCREMENTAL', total: 2 }) })
  const getMetadataJob = vi.spyOn(dataSourceAPI, 'getMetadataJob')
    .mockRejectedValueOnce(new Error('temporary'))
    .mockResolvedValueOnce(job({ id: 'job-retry', kind: 'REFRESH', mode: 'INCREMENTAL', status: 'SUCCEEDED', stage: 'COMPLETE', total: 2, completed: 2, succeeded: 2, completedAt: '2026-07-17T01:02:00Z' }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  await screen.findByRole('progressbar', { name: '元数据任务进度' })
  await user.click(screen.getByRole('button', { name: '关闭数据表资产' }))
  expect(screen.queryByRole('dialog', { name: '数据表资产' })).not.toBeInTheDocument()

  expect(await screen.findByText('temporary；将自动重试', {}, { timeout: 2500 })).toBeInTheDocument()
  expect(await screen.findByText('增量刷新完成：2 张成功', {}, { timeout: 4500 })).toBeInTheDocument()
  expect(getMetadataJob).toHaveBeenCalledTimes(2)
  // 弹窗关闭时不再把后台源的表清单写入当前页面；重新打开时会重新加载。
  expect(tables).toHaveBeenCalledTimes(1)
}, 7000)

test('切换数据源不会让迟到的表发现结果覆盖当前清单', async () => {
  const secondSource = source({ id: 'source-2', code: 'finance_mysql', name: '财务业务库', config: { host: 'finance.internal', port: 3306, database: 'finance', username: 'reader' } })
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source(), secondSource] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [], total: 0 })
  let resolveFirstDiscovery!: (value: Awaited<ReturnType<typeof dataSourceAPI.discoverTables>>) => void
  const firstDiscovery = new Promise<Awaited<ReturnType<typeof dataSourceAPI.discoverTables>>>(resolve => { resolveFirstDiscovery = resolve })
  vi.spyOn(dataSourceAPI, 'discoverTables').mockImplementation(sourceId => sourceId === 'source-1' ? firstDiscovery : Promise.resolve({
    items: [{ catalogName: 'finance', schemaName: 'public', name: 'accounts', type: 'TABLE', sourceComment: '', columns: [] }], total: 1,
  }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  await user.click(await screen.findByRole('button', { name: '新增数据表' }))
  await screen.findByText('正在从数据源刷新表清单…')
  await user.click(screen.getByRole('button', { name: '取消' }))
  await user.click(screen.getByRole('button', { name: '关闭数据表资产' }))

  await user.click(within(cardFor('财务业务库')).getByRole('button', { name: '查看' }))
  await user.click(await screen.findByRole('button', { name: '新增数据表' }))
  expect(await screen.findByText('accounts')).toBeInTheDocument()
  resolveFirstDiscovery({ items: [{ catalogName: 'sales', schemaName: 'public', name: 'late_orders', type: 'TABLE', sourceComment: '', columns: [] }], total: 1 })
  await waitFor(() => expect(screen.queryByText('late_orders')).not.toBeInTheDocument())
  expect(screen.getByText('accounts')).toBeInTheDocument()
})

test('查看另一数据源不会停止原数据源后台任务的完成通知', async () => {
  const secondSource = source({ id: 'source-2', code: 'finance_mysql', name: '财务业务库', config: { host: 'finance.internal', port: 3306, database: 'finance', username: 'reader' } })
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source(), secondSource] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [], total: 0 })
  vi.mocked(dataSourceAPI.latestActiveMetadataJob).mockImplementation(sourceId => Promise.resolve({ job: sourceId === 'source-1'
    ? job({ id: 'job-source-1', kind: 'REFRESH', mode: 'INCREMENTAL' })
    : job({ id: 'job-source-2', dataSourceId: 'source-2', kind: 'REFRESH', mode: 'FULL' }) }))
  const getMetadataJob = vi.spyOn(dataSourceAPI, 'getMetadataJob').mockImplementation((sourceId, jobId) => Promise.resolve(sourceId === 'source-1'
    ? job({ id: jobId, status: 'SUCCEEDED', stage: 'COMPLETE', kind: 'REFRESH', mode: 'INCREMENTAL', completed: 1, succeeded: 1, completedAt: '2026-07-17T01:03:00Z' })
    : job({ id: jobId, dataSourceId: 'source-2', status: 'RUNNING', stage: 'DISCOVERY', kind: 'REFRESH', mode: 'FULL' })))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  await screen.findByRole('progressbar', { name: '元数据任务进度' })
  await user.click(screen.getByRole('button', { name: '关闭数据表资产' }))
  await user.click(within(cardFor('财务业务库')).getByRole('button', { name: '查看' }))
  const secondTask = await screen.findByRole('region', { name: '元数据后台任务' })
  expect(within(secondTask).getByText('全量刷新')).toBeInTheDocument()

  expect(await screen.findByText('增量刷新完成：1 张成功', {}, { timeout: 2500 })).toBeInTheDocument()
  expect(getMetadataJob).toHaveBeenCalledWith('source-1', 'job-source-1')
  expect(within(secondTask).getByText('全量刷新')).toBeInTheDocument()
}, 5000)

test('删除数据表只调用资产删除接口而不删除数据源', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValueOnce({ items: [metadataTable], total: 1 }).mockResolvedValueOnce({ items: [], total: 0 })
  vi.spyOn(dataSourceAPI, 'columns').mockResolvedValue({ items: metadataColumns })
  const deleteTable = vi.spyOn(dataSourceAPI, 'deleteTable').mockResolvedValue()
  const deleteSource = vi.spyOn(dataSourceAPI, 'delete')
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '管理销售业务库的数据表资产' }))
  const dialog = await screen.findByRole('dialog', { name: '数据表资产' })
  await user.click(await within(dialog).findByText('订单表'))
  await user.click(within(within(dialog).getByLabelText('订单表操作')).getByRole('button', { name: '删除' }))
  const confirm = screen.getByRole('dialog', { name: '删除数据表资产' })
  expect(confirm).toHaveTextContent('不会删除或修改源数据库中的原表')
  await user.click(within(confirm).getByRole('button', { name: '确认删除资产' }))

  expect(deleteTable).toHaveBeenCalledWith('table-1')
  expect(deleteSource).not.toHaveBeenCalled()
})

test('新建数据源使用结构化连接字段而非 JDBC 或 secretRef', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [] })
  const created = source({ id: 'source-new', code: 'finance_oracle', name: '财务分析库', type: 'ORACLE', status: 'DRAFT', config: { host: 'oracle.internal', port: 1521, database: 'FREEPDB1', username: 'reader' }, version: 1 })
  const create = vi.spyOn(dataSourceAPI, 'create').mockResolvedValue(created)
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据源')
  await user.click(screen.getByRole('button', { name: '新建数据源' }))
  const dialog = screen.getByRole('dialog', { name: '新建数据源' })
  await user.type(within(dialog).getByLabelText('数据源名称'), '财务分析库')
  await user.type(within(dialog).getByLabelText('数据源编码'), 'finance_oracle')
  await user.selectOptions(within(dialog).getByLabelText('数据源类型'), 'ORACLE')
  await user.type(within(dialog).getByLabelText('Host'), 'oracle.internal')
  await user.clear(within(dialog).getByLabelText('Port'))
  await user.type(within(dialog).getByLabelText('Port'), '1521')
  await user.type(within(dialog).getByLabelText('Database'), 'FREEPDB1')
  await user.type(within(dialog).getByLabelText('Username'), 'reader')
  await user.type(within(dialog).getByLabelText('Password'), 'db-password')
  await user.click(within(dialog).getByRole('button', { name: '创建数据源' }))

  expect(create).toHaveBeenCalledWith({ code: 'finance_oracle', name: '财务分析库', type: 'ORACLE', host: 'oracle.internal', port: 1521, database: 'FREEPDB1', username: 'reader', password: 'db-password' })
  expect(JSON.stringify(create.mock.calls[0][0])).not.toContain('jdbc')
  expect(JSON.stringify(create.mock.calls[0][0])).not.toContain('secretRef')
  expect(await screen.findByText('财务分析库')).toBeInTheDocument()
})

test('新建文件数据源只上传创建，在新增数据表时解析预览并提交 Sheet 映射', async () => {
	vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [] })
	vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [], total: 0 })
	const upload = vi.spyOn(dataSourceAPI, 'uploadExcel').mockResolvedValue({
		id: 'file-1', filename: 'analysis.xlsx', version: 1, versionId: 'version-1', sizeBytes: 1024,
		workbookSummary: { inspectionStatus: 'PENDING' },
	})
	const inspect = vi.spyOn(dataSourceAPI, 'inspectExcelSource').mockResolvedValue({ sampleLimit: 10, sheets: [
			{ name: 'Sales', headerRow: 1, skipEmptyRows: true, columns: [{ name: 'month', canonicalType: 'DATE', nullable: false }, { name: 'amount', canonicalType: 'DECIMAL', nullable: false }], rows: [['2026-01-01', '12.50']] },
			{ name: 'Customers', headerRow: 2, skipEmptyRows: true, columns: [{ name: 'customer_id', canonicalType: 'NUMBER', nullable: false }], rows: [['1']] },
		] })
	const created = source({ id: 'source-excel', code: 'excel_analysis', name: 'Excel 经营分析', type: 'EXCEL', status: 'DRAFT', config: {}, fileAssetId: 'file-1', version: 1 })
	const create = vi.spyOn(dataSourceAPI, 'create').mockResolvedValue(created)
	const testConnection = vi.spyOn(dataSourceAPI, 'test').mockResolvedValue({ serverVersion: 'Excel xlsx', latencyMs: 1 })
	const discover = vi.spyOn(dataSourceAPI, 'discoverTables')
	const importTables = vi.spyOn(dataSourceAPI, 'importTables').mockResolvedValue(job({ id: 'job-excel', dataSourceId: 'source-excel', total: 2 }))
	const user = userEvent.setup()
	renderPage()

	await screen.findByText('还没有数据源')
	await user.click(screen.getByRole('button', { name: '新建数据源' }))
	const dialog = screen.getByRole('dialog', { name: '新建数据源' })
	await user.type(within(dialog).getByLabelText('数据源名称'), 'Excel 经营分析')
	await user.type(within(dialog).getByLabelText('数据源编码'), 'excel_analysis')
	await user.selectOptions(within(dialog).getByLabelText('数据源类型'), 'EXCEL')
	const file = new File(['workbook'], 'analysis.xlsx', { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' })
	const fileInput = within(dialog).getByLabelText('Excel 文件')
	expect(fileInput).toHaveClass('excel-source-file-input')
	expect(within(dialog).getByText('尚未选择文件', { selector: 'strong' })).toBeInTheDocument()
	await user.upload(fileInput, file)
	expect(within(dialog).getByText('analysis.xlsx', { selector: 'strong' })).toBeInTheDocument()
	expect(within(dialog).getByText('重新选择文件')).toBeInTheDocument()
	expect(within(dialog).queryByText('结构验证通过')).not.toBeInTheDocument()
	expect(within(dialog).queryByRole('button', { name: '分析前 10 行' })).not.toBeInTheDocument()
	await user.click(within(dialog).getByRole('button', { name: '上传并创建数据源' }))

	const assets = await screen.findByRole('dialog', { name: '数据表资产' })
	expect(upload).toHaveBeenCalledWith(file)
	expect(create).toHaveBeenCalledWith({ code: 'excel_analysis', name: 'Excel 经营分析', type: 'EXCEL', fileAssetId: 'file-1' })
	expect(testConnection).toHaveBeenCalledWith('source-excel')
	expect(inspect).not.toHaveBeenCalled()
	expect(discover).not.toHaveBeenCalled()
	expect(importTables).not.toHaveBeenCalled()
	expect(await screen.findByText(/请点击“新增数据表”解析、预览并选择/)).toBeInTheDocument()

	await user.click(within(assets).getByRole('button', { name: '新增数据表' }))
	const picker = await screen.findByRole('dialog', { name: '新增数据表' })
	expect(inspect).toHaveBeenCalledWith('source-excel')
	expect(await within(picker).findByText('Sheet 结构解析完成')).toBeInTheDocument()
	expect(within(picker).getByText('表头第 2 行 · 1 字段 · 跳过空行')).toBeInTheDocument()
	expect(within(picker).getByText('DECIMAL')).toBeInTheDocument()
	expect(within(picker).getByText('2026-01-01')).toBeInTheDocument()
	await user.click(within(picker).getByRole('checkbox', { name: '全选可映射 Sheet' }))
	await user.click(within(picker).getByRole('button', { name: '提交 2 个 Sheet 映射' }))

	await waitFor(() => expect(importTables).toHaveBeenCalled())
	expect(importTables).toHaveBeenCalledWith('source-excel', [
		{ catalogName: '', schemaName: 'WORKBOOK', tableName: 'Sales' },
		{ catalogName: '', schemaName: 'WORKBOOK', tableName: 'Customers' },
	])
	expect(create.mock.invocationCallOrder[0]).toBeLessThan(testConnection.mock.invocationCallOrder[0])
	expect(testConnection.mock.invocationCallOrder[0]).toBeLessThan(inspect.mock.invocationCallOrder[0])
	expect(inspect.mock.invocationCallOrder[0]).toBeLessThan(importTables.mock.invocationCallOrder[0])
	expect(await screen.findByText(/已提交 2 个 Sheet 的后台采样与 LLM 映射任务/)).toBeInTheDocument()
})

test('修改数据源时连接信息预填且密码可留空保留', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  const update = vi.spyOn(dataSourceAPI, 'update').mockResolvedValue(source({ name: '销售主库', status: 'DRAFT', version: 4 }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '修改' }))
  const dialog = screen.getByRole('dialog', { name: '修改数据源' })
  expect(within(dialog).getByLabelText('Host')).toHaveValue('mysql.internal')
  expect(within(dialog).getByLabelText('Password')).toHaveValue('')
  await user.clear(within(dialog).getByLabelText('数据源名称'))
  await user.type(within(dialog).getByLabelText('数据源名称'), '销售主库')
  await user.click(within(dialog).getByRole('button', { name: '保存修改' }))

  expect(update).toHaveBeenCalledWith('source-1', { code: 'sales_mysql', name: '销售主库', type: 'MYSQL', host: 'mysql.internal', port: 3306, database: 'sales', username: 'report_reader', password: '' })
  expect(await screen.findByText('销售主库')).toBeInTheDocument()
})

test('运行中数据源可暂停并从服务端刷新为已暂停', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValueOnce({ items: [source()] }).mockResolvedValueOnce({ items: [source({ status: 'DISABLED', version: 4 })] })
  const disable = vi.spyOn(dataSourceAPI, 'disable').mockResolvedValue()
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '暂停' }))
  expect(disable).toHaveBeenCalledWith('source-1')
  expect(within(cardFor('销售业务库')).getByText('已暂停')).toBeInTheDocument()
  expect(within(cardFor('销售业务库')).getByRole('button', { name: '恢复' })).toBeEnabled()
})

test('可测试数据源连通性并展示数据库版本和耗时', async () => {
  vi.spyOn(dataSourceAPI, 'list')
    .mockResolvedValueOnce({ items: [source({ status: 'DRAFT' })] })
    .mockResolvedValueOnce({ items: [source({ status: 'ACTIVE', version: 4 })] })
  const testConnection = vi.spyOn(dataSourceAPI, 'test').mockResolvedValue({ serverVersion: '8.4.10', latencyMs: 41 })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('待验证')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '测试连接' }))

  expect(testConnection).toHaveBeenCalledWith('source-1')
  const toast = await screen.findByRole('status')
  expect(toast).toHaveTextContent('8.4.10 · 41 ms')
  expect(toast).toHaveClass('data-source-toast', 'success')
  expect(screen.getByRole('region', { name: '数据源配置中心内容' })).not.toContainElement(toast)
  expect(within(cardFor('销售业务库')).getByText('运行中')).toBeInTheDocument()
})

test('可按名称编码、类型和状态组合筛选数据源', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [
    source(),
    source({ id: 'source-2', code: 'finance_oracle', name: '财务分析库', type: 'ORACLE', status: 'DRAFT' }),
  ] })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('显示 2 / 2')
  await user.type(screen.getByLabelText('搜索数据源'), 'finance')
  expect(screen.getByRole('heading', { level: 3, name: '财务分析库' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { level: 3, name: '销售业务库' })).not.toBeInTheDocument()
  await user.clear(screen.getByLabelText('搜索数据源'))
  await user.selectOptions(screen.getByLabelText('按类型筛选'), 'ORACLE')
  await user.selectOptions(screen.getByLabelText('按状态筛选'), 'ACTIVE')
  expect(screen.getByText('没有符合条件的数据源')).toBeInTheDocument()
  expect(screen.getByText('显示 0 / 2')).toBeInTheDocument()
})

test('查看、修改、测试、暂停和删除按钮使用不同语义颜色', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  renderPage()

  await screen.findByText('销售业务库')
  const card = within(cardFor('销售业务库'))
  expect(card.getByRole('button', { name: '查看' })).toHaveClass('action-view')
  expect(card.getByRole('button', { name: '修改' })).toHaveClass('action-edit')
  expect(card.getByRole('button', { name: '测试连接' })).toHaveClass('action-test')
  expect(card.getByRole('button', { name: '暂停' })).toHaveClass('action-pause')
  expect(card.getByRole('button', { name: '删除' })).toHaveClass('action-delete')
})

test('删除前二次确认并从清单移除', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  const remove = vi.spyOn(dataSourceAPI, 'delete').mockResolvedValue()
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '删除' }))
  const dialog = screen.getByRole('dialog', { name: '删除数据源' })
  await user.click(within(dialog).getByRole('button', { name: '确认删除' }))

  expect(remove).toHaveBeenCalledWith('source-1')
  expect(await screen.findByText('还没有数据源')).toBeInTheDocument()
})
