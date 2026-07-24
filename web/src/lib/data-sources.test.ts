import { afterEach, expect, test, vi } from 'vitest'
import { dataSourceAPI, type DataSourceColumnBusinessMetadataInput, type DataSourceConnectionInput } from './data-sources'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  sessionStorage.clear()
})

test('数据源管理 API 使用结构化连接字段和明确生命周期端点', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  const requests: Array<{ url: string; init?: RequestInit }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), init })
    if (init?.method === 'POST' && String(input).endsWith('/test')) {
      return new Response(JSON.stringify({
        id: 'test-job-1', dataSourceId: 'source/id', configVersionId: 'version-1',
        status: 'SUCCEEDED', attempt: 1, maxAttempts: 3,
        serverVersion: '8.4.10', latencyMs: 12,
        requestedAt: '2026-07-24T00:00:00Z',
        testedAt: '2026-07-24T00:00:01Z', expiresAt: '2026-07-24T00:30:01Z',
      }), { status: 202, headers: { 'Content-Type': 'application/json' } })
    }
    if (init?.method === 'POST' && String(input).endsWith('/disable')) return new Response(null, { status: 204 })
    if (init?.method === 'POST' && String(input).endsWith('/enable')) return new Response(null, { status: 204 })
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    return new Response(JSON.stringify({ id: 'source-1' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  const connection: DataSourceConnectionInput = { code: 'sales', name: 'Sales', type: 'MYSQL', host: 'db.internal', port: 3306, database: 'sales', username: 'reader', password: 'secret' }

  await dataSourceAPI.update('source/id', { ...connection, expectedVersion: 7 })
  await dataSourceAPI.test('source/id')
  await dataSourceAPI.publish('source/id')
  await dataSourceAPI.inspectExcelSource('source/id')
  await dataSourceAPI.sync('source/id')
  await dataSourceAPI.refreshTables('source/id')
  await dataSourceAPI.disable('source/id')
  await dataSourceAPI.enable('source/id')
  await dataSourceAPI.delete('source/id')

  expect(requests.map(request => [request.url, request.init?.method])).toEqual([
    ['/api/v1/data-sources/source%2Fid', 'PUT'],
    ['/api/v1/data-sources/source%2Fid/test', 'POST'],
    ['/api/v1/data-sources/source%2Fid/publish', 'POST'],
    ['/api/v1/data-sources/source%2Fid/file-inspection', 'POST'],
    ['/api/v1/data-sources/source%2Fid/sync', 'POST'],
    ['/api/v1/data-sources/source%2Fid/tables/refresh', 'POST'],
    ['/api/v1/data-sources/source%2Fid/disable', 'POST'],
    ['/api/v1/data-sources/source%2Fid/enable', 'POST'],
    ['/api/v1/data-sources/source%2Fid', 'DELETE'],
  ])
  expect(JSON.parse(String(requests[0].init?.body))).toEqual({ ...connection, expectedVersion: 7 })
  expect(String(requests[0].init?.body)).not.toContain('jdbc')
  expect(String(requests[0].init?.body)).not.toContain('secretRef')
  expect(new Headers(requests[1].init?.headers).get('Idempotency-Key')).toBeTruthy()
  expect(JSON.parse(String(requests[5].init?.body))).toEqual({ mode: 'INCREMENTAL', sampleDataMode: 'MASK' })
})

test('连接测试在 202 入队后轮询安全任务直到形成证明', async () => {
  vi.useFakeTimers()
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  const requests: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    requests.push(`${init?.method || 'GET'} ${url}`)
    const base = {
      id: 'test-job-1', dataSourceId: 'source-1', configVersionId: 'version-7',
      attempt: 1, maxAttempts: 3, requestedAt: '2026-07-24T00:00:00Z',
    }
    if (init?.method === 'POST') {
      return new Response(JSON.stringify({ ...base, status: 'QUEUED' }), {
        status: 202, headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response(JSON.stringify({
      ...base, status: 'SUCCEEDED', serverVersion: 'Oracle 23ai', latencyMs: 27,
      testedAt: '2026-07-24T00:00:01Z', expiresAt: '2026-07-24T00:30:01Z',
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  const pending = dataSourceAPI.test('source-1')
  await vi.advanceTimersByTimeAsync(500)
  await expect(pending).resolves.toEqual({
    serverVersion: 'Oracle 23ai',
    latencyMs: 27,
    configVersionId: 'version-7',
    testedAt: '2026-07-24T00:00:01Z',
    expiresAt: '2026-07-24T00:30:01Z',
  })
  expect(requests).toEqual([
    'POST /api/v1/data-sources/source-1/test',
    'GET /api/v1/data-sources/source-1/connection-tests/test-job-1',
  ])
  expect(sessionStorage.getItem('intelligent-report-connection-test-jobs-v1')).toBeNull()
})

test('页面刷新后按持久 jobId 恢复后台连接测试且不重复入队', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  sessionStorage.setItem('intelligent-report-connection-test-jobs-v1', JSON.stringify({
    'source-1': {
      sourceId: 'source-1', jobId: 'persisted-job-1', configVersionId: 'version-9',
    },
  }))
  const requests: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push(`${init?.method || 'GET'} ${String(input)}`)
    return new Response(JSON.stringify({
      id: 'persisted-job-1', dataSourceId: 'source-1', configVersionId: 'version-9',
      status: 'SUCCEEDED', attempt: 1, maxAttempts: 3,
      requestedAt: '2026-07-24T00:00:00Z', testedAt: '2026-07-24T00:00:01Z',
      expiresAt: '2026-07-24T00:30:01Z', serverVersion: 'MySQL 8.4', latencyMs: 8,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  await expect(dataSourceAPI.test('source-1')).resolves.toMatchObject({
    configVersionId: 'version-9', serverVersion: 'MySQL 8.4',
  })
  expect(requests).toEqual([
    'GET /api/v1/data-sources/source-1/connection-tests/persisted-job-1',
  ])
  expect(sessionStorage.getItem('intelligent-report-connection-test-jobs-v1')).toBeNull()
})

test('AbortSignal 停止前台轮询但保留 jobId 供下次恢复', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    expect(init?.signal).toBeInstanceOf(AbortSignal)
    return new Response(JSON.stringify({
      id: 'test-job-abort', dataSourceId: 'source-abort', configVersionId: 'version-a',
      status: 'QUEUED', attempt: 0, maxAttempts: 3,
      requestedAt: '2026-07-24T00:00:00Z',
    }), { status: 202, headers: { 'Content-Type': 'application/json' } })
  }))
  const controller = new AbortController()
  const pending = dataSourceAPI.test('source-abort', { signal: controller.signal })
  await new Promise(resolve => window.setTimeout(resolve, 0))
  controller.abort()

  await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  expect(dataSourceAPI.pendingConnectionTestSourceIds()).toContain('source-abort')
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

test('字段业务元数据接口按数组格式提交说明和标签', async () => {
  sessionStorage.setItem('intelligent-report-auth', JSON.stringify({ accessToken: 'access', refreshToken: 'refresh' }))
  const requests: Array<{ url: string; init?: RequestInit }> = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), init })
    return new Response(JSON.stringify({ id: 'column-1' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  const metadata: DataSourceColumnBusinessMetadataInput = {
    businessName: '订单编号',
    businessDescription: '跨系统订单主键',
    tags: ['作用:主数据', '自定义:订单'],
    sensitivityLevel: 'INTERNAL',
    semanticType: 'IDENTIFIER',
    manualLocked: true,
    expectedVersion: 3,
  }

  await dataSourceAPI.updateColumn('column/id', metadata)

  expect(requests).toHaveLength(1)
  expect(requests[0].url).toBe('/api/v1/assets/columns/column%2Fid/business-metadata')
  expect(requests[0].init?.method).toBe('PUT')
  expect(JSON.parse(String(requests[0].init?.body))).toEqual(metadata)
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
