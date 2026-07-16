import { render, screen } from '@testing-library/react'
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

test('loads assets from different sources into the dataset designer', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes('/permissions/evaluate')) {
      return new Response(JSON.stringify({ allowed: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    const body = url.includes('/columns')
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
  await userEvent.click(await screen.findByRole('button', { name: /订单表/ }))
  expect(await screen.findByRole('checkbox', { name: '选择 order_id' })).toBeChecked()
  await userEvent.click(screen.getByRole('button', { name: /客户表/ }))
  expect(await screen.findByRole('checkbox', { name: '选择 customer_id' })).toBeChecked()
  expect(screen.getByText('关联 t1 → t2')).toBeInTheDocument()
  expect(screen.getByRole('checkbox', { name: '已核对基数' })).not.toBeChecked()
})

test('renders the protected metric center route and navigation entry', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'test-access', refreshToken: 'test-refresh' }))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const body = url.includes('/permissions/evaluate')
      ? { allowed: true }
      : { items: [], total: 0, limit: 50, offset: 0 }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  render(<MemoryRouter initialEntries={['/metrics']}><App /></MemoryRouter>)

  expect(screen.getByRole('heading', { level: 1, name: '指标中心' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: '指标中心' })).toHaveClass('active')
  expect(await screen.findByLabelText('指标编码')).toBeEnabled()
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
