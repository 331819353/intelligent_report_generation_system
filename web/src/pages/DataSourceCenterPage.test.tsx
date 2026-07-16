import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import { dataSourceAPI, type DataSourceRecord } from '../lib/data-sources'
import { DataSourceCenterPage } from './DataSourceCenterPage'

afterEach(() => vi.restoreAllMocks())

const source = (overrides: Partial<DataSourceRecord> = {}): DataSourceRecord => ({
  id: 'source-1',
  tenantId: 'tenant-1',
  code: 'sales_mysql',
  name: '销售业务库',
  type: 'MYSQL',
  status: 'ACTIVE',
  config: {},
  version: 3,
  ...overrides,
})

test('展示已有数据源清单和当前状态', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [source(), source({ id: 'source-2', code: 'finance_oracle', name: '财务分析库', type: 'ORACLE', status: 'DRAFT', version: 1 })] })

  render(<MemoryRouter><DataSourceCenterPage /></MemoryRouter>)

  const list = await screen.findByRole('list', { name: '已有数据源清单' })
  expect(within(list).getByText('销售业务库')).toBeInTheDocument()
  expect(within(list).getByText('财务分析库')).toBeInTheDocument()
  expect(within(list).getByText('运行中')).toBeInTheDocument()
  expect(within(list).getByText('待验证')).toBeInTheDocument()
  expect(screen.getByLabelText('2 个数据源')).toBeInTheDocument()
})

test('通过新建按钮创建数据库数据源并立即加入清单', async () => {
  vi.spyOn(dataSourceAPI, 'list').mockResolvedValue({ items: [] })
  const create = vi.spyOn(dataSourceAPI, 'create').mockResolvedValue(source({ id: 'source-new', code: 'finance_oracle', name: '财务分析库', type: 'ORACLE', status: 'DRAFT', version: 1 }))
  const user = userEvent.setup()

  render(<MemoryRouter><DataSourceCenterPage /></MemoryRouter>)
  await screen.findByText('还没有数据源')
  await user.click(screen.getByRole('button', { name: '新建数据源' }))

  const dialog = screen.getByRole('dialog', { name: '新建数据源' })
  await user.type(within(dialog).getByLabelText('数据源名称'), '财务分析库')
  await user.type(within(dialog).getByLabelText('数据源编码'), 'finance_oracle')
  await user.selectOptions(within(dialog).getByLabelText('数据源类型'), 'ORACLE')
  await user.type(within(dialog).getByLabelText('凭证引用'), 'env://ORACLE_SOURCE_SECRET')
  await user.click(within(dialog).getByRole('button', { name: '创建数据源' }))

  expect(create).toHaveBeenCalledWith({
    code: 'finance_oracle',
    name: '财务分析库',
    type: 'ORACLE',
    config: {},
    secretRef: 'env://ORACLE_SOURCE_SECRET',
  })
  expect(await screen.findByText('财务分析库')).toBeInTheDocument()
  expect(screen.queryByRole('dialog', { name: '新建数据源' })).not.toBeInTheDocument()
})
