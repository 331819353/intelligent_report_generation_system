import { afterEach, describe, expect, test, vi } from 'vitest'
import { metricAPI, type MetricDefinition } from './metrics'

afterEach(() => {
  vi.unstubAllGlobals()
  sessionStorage.clear()
})

describe('指标 API 合同', () => {
  test('目录和精确版本读取携带分页并禁用缓存', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [], total: 0, limit: 20, offset: 40 }))
    vi.stubGlobal('fetch', fetchMock)

    await metricAPI.list(20, 40)
    await metricAPI.getVersion('metric/id', 'version/id')
    await metricAPI.getVersionUsage('metric/id', 'version/id')

    const [listURL, listInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    const [versionURL, versionInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit]
    const [usageURL, usageInit] = fetchMock.mock.calls[2] as unknown as [string, RequestInit]
    expect(listURL).toBe('/api/v1/metrics?limit=20&offset=40')
    expect(listInit.cache).toBe('no-store')
    expect(versionURL).toBe('/api/v1/metrics/metric%2Fid/versions/version%2Fid')
    expect(versionInit.cache).toBe('no-store')
    expect(usageURL).toBe('/api/v1/metrics/metric%2Fid/versions/version%2Fid/usage')
    expect(usageInit.cache).toBe('no-store')
  })

  test('发布冻结精确草稿身份、定义摘要和幂等键', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: 'metric-version-1' }))
    vi.stubGlobal('fetch', fetchMock)

    await metricAPI.publish('metric-1', {
      draftVersionId: 'draft-1', expectedVersion: 4, expectedDraftRecordVersion: 3,
      expectedDefinitionHash: 'a'.repeat(64), validationParameters: { month: '2026-06' },
    }, '00000000-0000-4000-8000-000000000001')

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/metrics/metric-1/publish')
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe('00000000-0000-4000-8000-000000000001')
    expect(JSON.parse(String(init.body))).toEqual({
      draftVersionId: 'draft-1', expectedVersion: 4, expectedDraftRecordVersion: 3,
      expectedDefinitionHash: 'a'.repeat(64), validationParameters: { month: '2026-06' },
    })
  })

  test('创建只提交结构化定义，精确版本试算不提交表达式计算结果', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: 'metric-1' }))
    vi.stubGlobal('fetch', fetchMock)

    await metricAPI.create(definition)
    await metricAPI.validate('metric-1')
    await metricAPI.previewVersion('metric-1', 'version-1', {
      queryId: 'query-1', parameters: {}, dimensionFieldIds: ['field_region'], maxRows: 100,
    })

    const [createURL, createInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    const [validateURL, validateInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit]
    const [previewURL, previewInit] = fetchMock.mock.calls[2] as unknown as [string, RequestInit]
    expect(createURL).toBe('/api/v1/metrics')
    expect(JSON.parse(String(createInit.body))).toEqual({ definition })
    expect(validateURL).toBe('/api/v1/metrics/metric-1/validate')
    expect(validateInit.body).toBe('{}')
    expect(previewURL).toBe('/api/v1/metrics/metric-1/versions/version-1/preview')
    expect(JSON.parse(String(previewInit.body))).toEqual({
      queryId: 'query-1', parameters: {}, dimensionFieldIds: ['field_region'], maxRows: 100,
    })
  })

  test('删除提交主对象乐观锁且不拼接未编码标识', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await metricAPI.delete('metric/id', 7)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/metrics/metric%2Fid')
    expect(init.method).toBe('DELETE')
    expect(JSON.parse(String(init.body))).toEqual({ expectedVersion: 7 })
  })
})

const definition: MetricDefinition = {
  schemaVersion: '1.0',
  metric: { code: 'revenue', name: '营业收入', description: '', type: 'ATOMIC' },
  datasetId: 'dataset-1', datasetVersionId: 'dataset-version-1',
  expression: { type: 'FIELD_REF', fieldId: 'field_revenue' }, aggregation: 'SUM',
  unit: '元', numberFormat: '#,##0.00', timeFieldId: '', timeGrain: 'NONE', additivity: 'ADDITIVE',
  nonAdditiveDimensionFieldIds: [], allowedDimensions: [], decimalScale: 2,
  roundingMode: 'HALF_UP', nullHandling: 'IGNORE', divisionByZero: 'NULL',
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
