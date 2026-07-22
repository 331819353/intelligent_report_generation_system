import { afterEach, expect, test, vi } from 'vitest'
import { dataSourceAPI, type DataSourceConnectionInput } from './data-sources'

afterEach(() => {
  vi.unstubAllGlobals()
  sessionStorage.clear()
})

test('数据源管理 API 使用结构化连接字段和明确生命周期端点', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  const requests: Array<{ url: string; init?: RequestInit }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), init })
    if (init?.method === 'POST' && String(input).endsWith('/disable')) return new Response(null, { status: 204 })
    if (init?.method === 'POST' && String(input).endsWith('/enable')) return new Response(null, { status: 204 })
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    return new Response(JSON.stringify({ id: 'source-1' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  const connection: DataSourceConnectionInput = { code: 'sales', name: 'Sales', type: 'MYSQL', host: 'db.internal', port: 3306, database: 'sales', username: 'reader', password: 'secret' }

  await dataSourceAPI.update('source/id', connection)
  await dataSourceAPI.test('source/id')
  await dataSourceAPI.inspectExcelSource('source/id')
  await dataSourceAPI.sync('source/id')
  await dataSourceAPI.refreshTables('source/id')
  await dataSourceAPI.disable('source/id')
  await dataSourceAPI.enable('source/id')
  await dataSourceAPI.delete('source/id')

  expect(requests.map(request => [request.url, request.init?.method])).toEqual([
    ['/api/v1/data-sources/source%2Fid', 'PUT'],
    ['/api/v1/data-sources/source%2Fid/test', 'POST'],
    ['/api/v1/data-sources/source%2Fid/file-inspection', 'POST'],
    ['/api/v1/data-sources/source%2Fid/sync', 'POST'],
    ['/api/v1/data-sources/source%2Fid/tables/refresh', 'POST'],
    ['/api/v1/data-sources/source%2Fid/disable', 'POST'],
    ['/api/v1/data-sources/source%2Fid/enable', 'POST'],
    ['/api/v1/data-sources/source%2Fid', 'DELETE'],
  ])
  expect(JSON.parse(String(requests[0].init?.body))).toEqual(connection)
  expect(String(requests[0].init?.body)).not.toContain('jdbc')
  expect(String(requests[0].init?.body)).not.toContain('secretRef')
})

test('文件首次上传与覆盖上传使用不同版本端点', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  const requests: Array<{ url: string; init?: RequestInit }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), init })
    return new Response(JSON.stringify({ id: 'file-1', version: 2 }), { status: 201, headers: { 'Content-Type': 'application/json' } })
  }))
  const file = new File(['workbook'], 'analysis.xlsx', { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' })

  await dataSourceAPI.uploadExcel(file)
  await dataSourceAPI.uploadExcelVersion('file/id', file)

  expect(requests.map(request => [request.url, request.init?.method])).toEqual([
    ['/api/v1/excel-files', 'POST'],
    ['/api/v1/excel-files/file%2Fid/versions', 'POST'],
  ])
  expect(requests[0].init?.body).toBeInstanceOf(FormData)
  expect(requests[1].init?.body).toBeInstanceOf(FormData)
})

test('按数据源分页读取全部活动表结构', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  const urls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    urls.push(url)
    return new Response(JSON.stringify({
      items: [{ id: 'table-1', dataSourceId: 'source/id', tableName: 'orders' }],
      total: 1,
      limit: 200,
      offset: 0,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  const result = await dataSourceAPI.tables('source/id')

  expect(result.total).toBe(1)
  expect(result.items).toHaveLength(1)
  expect(urls[0]).toContain('/api/v1/assets/tables?')
  expect(urls[0]).toContain('dataSourceId=source%2Fid')
  expect(urls[0]).toContain('status=ACTIVE')
  expect(urls[0]).toContain('limit=200')
})
