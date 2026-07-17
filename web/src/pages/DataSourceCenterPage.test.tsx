import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import { dataSourceAPI, type DataSourceColumnRecord, type DataSourceRecord, type DataSourceTableRecord } from '../lib/data-sources'
import { DataSourceCenterPage } from './DataSourceCenterPage'

afterEach(() => vi.restoreAllMocks())

const source = (overrides: Partial<DataSourceRecord> = {}): DataSourceRecord => ({
  id: 'source-1', tenantId: 'tenant-1', code: 'sales_mysql', name: '销售业务库', type: 'MYSQL', status: 'ACTIVE',
  config: { host: 'mysql.internal', port: 3306, database: 'sales', username: 'report_reader' }, version: 3, ...overrides,
})

const renderPage = () => render(<MemoryRouter><DataSourceCenterPage /></MemoryRouter>)
const cardFor = (name: string) => screen.getByRole('heading', { level: 3, name }).closest('article') as HTMLElement
const metadataTable: DataSourceTableRecord = {
  id: 'table-1', dataSourceId: 'source-1', catalogName: 'sales', schemaName: 'public', tableName: 'orders', tableType: 'TABLE', businessName: '订单表', businessDescription: '订单交易明细', tags: ['主题:经营分析'], sensitivityLevel: 'INTERNAL', visibility: 'PRIVATE', manualLocked: false, businessVersion: 2, managementStatus: 'ENABLED', enrichmentStatus: 'SUCCEEDED', columnCount: 2, metadataVersion: 3, lastSyncAt: '2026-07-17T01:00:00Z',
}
const metadataColumns: DataSourceColumnRecord[] = [
  { id: 'column-1', tableId: 'table-1', columnName: 'order_id', ordinalPosition: 1, nativeType: 'bigint', canonicalType: 'INTEGER', nullable: false, businessName: '订单编号', assetStatus: 'ACTIVE' },
  { id: 'column-2', tableId: 'table-1', columnName: 'amount', ordinalPosition: 2, nativeType: 'decimal(18,2)', canonicalType: 'DECIMAL', nullable: false, businessName: '订单金额', assetStatus: 'ACTIVE' },
]

test('数据源连接摘要只在卡片中展示，表资产弹窗不重复展示', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [], total: 0 })
  const user = userEvent.setup()
  renderPage()

  await screen.findByRole('list', { name: '已有数据源清单' })
  expect(within(cardFor('销售业务库')).getByText('mysql.internal:3306')).toBeInTheDocument()
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
  expect(within(dialog).getByRole('button', { name: '刷新全部元数据' })).toBeEnabled()
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
  const importTables = vi.spyOn(dataSourceAPI, 'importTables').mockResolvedValue({ items: [{ id: 'table-1' }, { id: 'table-2' }], total: 2 })
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
  expect(await screen.findByRole('status')).toHaveTextContent('采样、LLM 完善和资产入库')
})

test('可刷新全部已纳管表结构并重新执行 LLM 加工', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source()] })
  vi.spyOn(dataSourceAPI, 'tables').mockResolvedValue({ items: [metadataTable], total: 1 })
  const refreshTables = vi.spyOn(dataSourceAPI, 'refreshTables').mockResolvedValue({ status: 'SUCCEEDED', total: 1, succeeded: 1, technicalUpdated: 1, failed: 0, items: [{ id: 'table-1', tableName: 'orders', status: 'SUCCEEDED', stage: 'COMPLETE' }] })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('销售业务库')
  await user.click(within(cardFor('销售业务库')).getByRole('button', { name: '查看' }))
  const dialog = await screen.findByRole('dialog', { name: '数据表资产' })
  await user.click(within(dialog).getByRole('button', { name: '刷新全部元数据' }))

  expect(refreshTables).toHaveBeenCalledWith('source-1')
  expect(await screen.findByRole('status')).toHaveTextContent('已刷新 1 张表的结构并重新完成 LLM 加工')
})

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
