import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'
import { App } from './App'

afterEach(() => vi.unstubAllGlobals())

test('renders login route', () => {
  render(<MemoryRouter initialEntries={['/login']}><App /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: '登录智能报告平台' })).toBeInTheDocument()
})

test('renders designer route', () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  render(<MemoryRouter initialEntries={['/designer/draft']}><App /></MemoryRouter>)
  expect(screen.getByRole('heading', { level: 1, name: '企业经营月度分析报告' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /AI 修改/ })).toBeInTheDocument()
})

test('viewer uses the shared report renderer', () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  render(<MemoryRouter initialEntries={['/reports/demo']}><App /></MemoryRouter>)
  expect(screen.getByLabelText('企业经营月度分析报告报告内容')).toHaveAttribute('data-render-mode', 'viewer')
  expect(screen.getByRole('img', { name: '营业收入趋势趋势图' })).toBeInTheDocument()
})

test('redirects anonymous users away from protected routes', () => {
  sessionStorage.clear()
  render(<MemoryRouter initialEntries={['/admin']}><App /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: '登录智能报告平台' })).toBeInTheDocument()
})

test('legacy new-dataset route also opens the configuration-center canvas', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const body = url.includes('/v1/datasets?')
      ? { items: [], total: 0, limit: 200, offset: 0 }
      : url.includes('/preview?')
      ? { columns: [url.includes('table-2') ? 'customer_id' : 'order_id'], rows: [[1]] }
      : url.includes('/columns')
      ? url.includes('table-2')
        ? { items: [{ id: 'column-2', tableId: 'table-2', columnName: 'customer_id', businessName: '客户编号', canonicalType: 'NUMBER', nullable: false, semanticType: 'IDENTIFIER' }] }
        : { items: [{ id: 'column-1', tableId: 'table-1', columnName: 'order_id', businessName: '订单编号', canonicalType: 'TEXT', nullable: false, semanticType: 'IDENTIFIER' }] }
      : { items: [
          { id: 'table-1', dataSourceId: 'source-1', dataSourceName: '订单库', dataSourceType: 'MYSQL', tableName: 'orders', schemaName: 'sales', businessName: '订单表', columnCount: 1 },
          { id: 'table-2', dataSourceId: 'source-2', dataSourceName: '客户库', dataSourceType: 'ORACLE', tableName: 'customers', schemaName: 'crm', businessName: '客户表', columnCount: 1 },
        ] }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  render(<MemoryRouter initialEntries={['/datasets/new/edit']}><App /></MemoryRouter>)
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await userEvent.click(within(dialog).getByRole('button', { name: /订单表/ }))
  expect(await within(dialog).findByLabelText('输出字段 order_id')).toBeChecked()
  await userEvent.click(within(dialog).getByRole('button', { name: /客户库/ }))
  await userEvent.click(within(dialog).getByRole('button', { name: /客户表/ }))
  expect(await within(dialog).findByLabelText('输出字段 customer_id')).toBeChecked()
  await userEvent.click(within(dialog).getByRole('button', { name: /关联组件双输入/ }))
  const relationDrawer = within(dialog).getByLabelText('配置表关联')
  const values = new Map<string, string>()
  const dataTransfer = { setData: (type: string, value: string) => values.set(type, value), getData: (type: string) => values.get(type) ?? '' }
  const connect = (sourceName: string, targetName: string) => {
    values.clear()
    const source = within(dialog).getByRole('button', { name: sourceName })
    const target = within(dialog).getByRole('button', { name: targetName })
    fireEvent.dragStart(source, { dataTransfer })
    fireEvent.drop(target, { dataTransfer })
    fireEvent.dragEnd(source, { dataTransfer })
  }
  connect('从数据节点 1 拖出连接', '连接到关联节点 1 槽位 1')
  connect('从数据节点 2 拖出连接', '连接到关联节点 1 槽位 2')
  expect(within(relationDrawer).getByLabelText('关联槽位 1')).toHaveTextContent('订单表')
  expect(within(relationDrawer).getByLabelText('关联槽位 2')).toHaveTextContent('客户表')
  expect(within(dialog).getByRole('button', { name: '配置关联 1' })).toBeInTheDocument()
})

test('renders the protected asset management metric route and navigation entry', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const body = url.includes('/permissions/evaluate')
      ? { allowed: true }
      : { items: [], total: 0, limit: 50, offset: 0 }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  render(<MemoryRouter initialEntries={['/assets/metrics']}><App /></MemoryRouter>)

  expect(screen.getByRole('heading', { level: 1, name: '资产管理中心' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '资产管理中心' })).toHaveClass('active')
  expect(screen.getByRole('link', { name: '指标资产' })).toHaveClass('active')
  expect(await screen.findByLabelText('搜索数据集或指标')).toBeEnabled()
  expect(screen.getByText('还没有可展示的普通数据集')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '新建指标' })).not.toBeInTheDocument()
})

test('renders the protected data source center route and navigation entry', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))

  render(<MemoryRouter initialEntries={['/data-sources']}><App /></MemoryRouter>)

  expect(screen.getByRole('heading', { level: 1, name: '数据源配置中心' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '数据源配置中心' })).toHaveClass('active')
  expect(await screen.findByText('还没有数据源')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '新建数据源' })).toBeEnabled()
})

test('renders the dataset configuration center route and renamed navigation entry', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: [], total: 0, limit: 200, offset: 0 }), { status: 200, headers: { 'Content-Type': 'application/json' } })))

  render(<MemoryRouter initialEntries={['/datasets']}><App /></MemoryRouter>)

  expect(screen.getByRole('heading', { level: 1, name: '数据集配置中心' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '数据集配置中心' })).toHaveClass('active')
  expect(await screen.findByText('还没有数据集')).toBeInTheDocument()
})

test('renders the protected asset management semantic route and navigation entry', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const body = String(input).includes('/permissions/evaluate')
      ? { allowed: true }
      : { items: [], total: 0, limit: 200, offset: 0 }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  render(<MemoryRouter initialEntries={['/assets/semantics']}><App /></MemoryRouter>)

  expect(screen.getByRole('heading', { level: 1, name: '资产管理中心' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '资产管理中心' })).toHaveClass('active')
  expect(screen.getByRole('link', { name: '语义资产' })).toHaveClass('active')
  expect(await screen.findByText('当前筛选下没有维度候选')).toBeInTheDocument()
})

test('opens dimension value mapping directly on the formal dimension directory', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const body = String(input).includes('/permissions/evaluate')
      ? { allowed: true }
      : { items: [], total: 0, limit: 200, offset: 0 }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  render(<MemoryRouter initialEntries={['/assets/dimension-values']}><App /></MemoryRouter>)

  expect(await screen.findByRole('tab', { name: '正式维度与成员' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('link', { name: '维度值映射' })).toHaveClass('active')
})
