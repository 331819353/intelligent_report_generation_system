import { afterEach, describe, expect, test, vi } from 'vitest'
import { RequestError } from './api'
import { buildDatasetDSL, buildPreviewParameters, createDatasetPublishIdempotencyKey, datasetAPI, type AssetColumn, type AssetTable, type DatasetDraft, type PublishedVersionRecord, type PublishDatasetInput } from './datasets'

afterEach(() => vi.unstubAllGlobals())

const table: AssetTable = { id: 'table-1', dataSourceId: 'source-1', dataSourceName: '订单库', dataSourceType: 'MYSQL', tableName: 'orders', schemaName: 'sales', businessName: '订单', columnCount: 2 }
const columns: AssetColumn[] = [
  { id: 'column-1', tableId: table.id, columnName: 'order_date', businessName: '订单日期', canonicalType: 'DATE', nullable: false, semanticType: 'DATE' },
  { id: 'column-2', tableId: table.id, columnName: 'amount', businessName: '订单金额', canonicalType: 'DECIMAL', nullable: false, semanticType: 'AMOUNT' },
]

function draft(): DatasetDraft {
  return {
    code: 'monthly_orders', name: '月度订单', description: '订单汇总',
    nodes: [{ id: 'orders', alias: 'o', table, columns, selected: ['order_date', 'amount'] }],
    fields: [{ key: 'orders.order_date', role: 'TIME', aggregation: '' }, { key: 'orders.amount', role: 'MEASURE', aggregation: 'SUM' }],
    joins: [], parameters: [{ code: 'start_date', name: '开始日期', dataType: 'DATE', required: true, multiValue: false }],
    filters: [{ id: 'filter_start', nodeId: 'orders', field: 'order_date', operator: 'GTE', value: '', parameterCode: 'start_date' }],
    calculations: [{ id: 'field_double_amount', code: 'double_amount', name: '双倍金额', operation: 'ADD', leftKey: 'orders.amount', rightKey: 'orders.amount', canonicalType: 'DECIMAL' }],
    sorts: [{ fieldId: 'order_date', direction: 'ASC' }], grainDescription: '每一行代表一个订单日期', grainKeys: ['order_date'],
  }
}

describe('buildDatasetDSL', () => {
  test('生成包含参数过滤、聚合、计算、排序和粒度的 DSL', () => {
    const dsl = buildDatasetDSL(draft())
    expect(dsl.dataset.type).toBe('SINGLE_SOURCE')
    expect(dsl.nodes[0].projection).toEqual(['order_date', 'amount'])
    expect(dsl.fields).toHaveLength(3)
    expect((dsl.filters as Array<Record<string, unknown>>)[0]).toMatchObject({ id: 'filter_start', stage: 'PRE_AGGREGATION' })
    expect(dsl.groupBy).toEqual(['field_o_order_date'])
    expect(dsl.sorts).toEqual([{ fieldId: 'field_o_order_date', direction: 'ASC' }])
    expect(dsl.outputGrain).toEqual({ description: '每一行代表一个订单日期', keyFields: ['order_date'] })
  })

  test('缺少输出粒度时拒绝生成', () => {
    const value = draft()
    value.grainKeys = []
    expect(() => buildDatasetDSL(value)).toThrow('输出粒度')
  })

  test('不同数据源节点生成跨源 DSL 并保留人工确认状态', () => {
    const value = draft()
    const customerTable: AssetTable = { ...table, id: 'table-2', dataSourceId: 'source-2', dataSourceName: '客户库', dataSourceType: 'ORACLE', tableName: 'customers' }
    const customerColumn: AssetColumn = { ...columns[0], id: 'column-3', tableId: customerTable.id, columnName: 'customer_id', businessName: '客户编号', canonicalType: 'NUMBER' }
    value.nodes.push({ id: 'customers', alias: 'c', table: customerTable, columns: [customerColumn], selected: ['customer_id'] })
    value.fields.push({ key: 'customers.customer_id', role: 'IDENTIFIER', aggregation: '' })
    value.joins.push({ id: 'orders_customers', leftNodeId: 'orders', rightNodeId: 'customers', leftField: 'order_date', rightField: 'customer_id', joinType: 'INNER', cardinality: 'MANY_TO_ONE', manualConfirmed: false })
    value.grainKeys = ['o_order_date']
    const dsl = buildDatasetDSL(value)
    expect(dsl.dataset.type).toBe('CROSS_SOURCE')
    expect((dsl.joins as Array<Record<string, unknown>>)[0].manualConfirmed).toBe(false)
  })
})

describe('buildPreviewParameters', () => {
  test('保留标量并将逗号分隔输入转换为多值参数', () => {
    expect(buildPreviewParameters([
      { code: 'start_date', name: '开始日期', dataType: 'DATE', required: true, multiValue: false },
      { code: 'regions', name: '区域', dataType: 'STRING', required: false, multiValue: true },
    ], { start_date: '2026-01-01', regions: '华东, 华南' })).toEqual({ start_date: '2026-01-01', regions: ['华东', '华南'] })
  })

  test('缺少必填预览参数时立即提示', () => {
    expect(() => buildPreviewParameters(draft().parameters, {})).toThrow('开始日期')
  })
})

describe('数据集发布版本 API', () => {
  test('读取可变数据集聚合时禁用缓存', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: 'dataset-1' }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.get('dataset/id')

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/datasets/dataset%2Fid')
    expect(init.cache).toBe('no-store')
  })

  test('发布请求携带冻结草稿信封与幂等键', async () => {
    const published = publishedRecord()
    const fetchMock = vi.fn(async () => jsonResponse(published, 201))
    vi.stubGlobal('fetch', fetchMock)
    const input: PublishDatasetInput = {
      draftVersionId: 'draft-version-1', expectedVersion: 5, expectedDraftRecordVersion: 3,
      expectedDslHash: 'a'.repeat(64), validationParameters: { start_date: '2026-01-01' },
    }

    await expect(datasetAPI.publish('dataset/id', input, 'publish-key')).resolves.toEqual(published)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/datasets/dataset%2Fid/publish')
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe('publish-key')
    expect(JSON.parse(String(init.body))).toEqual(input)
  })

  test('按父数据集访问版本目录、精确版本、使用汇总和版本预览', async () => {
    const published = publishedRecord()
    const usage = { reportDraftReferences: 2, downstreamDraftReferences: 3, downstreamPublishedReferences: 4, activeQueryRuns: 1 }
    const preview = { queryId: 'query-1', columns: ['order_date'], rows: [['2026-01-01']], rowCount: 1, durationMs: 8 }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ items: [published], total: 1, limit: 20, offset: 10 }))
      .mockResolvedValueOnce(jsonResponse(published))
      .mockResolvedValueOnce(jsonResponse(usage))
      .mockResolvedValueOnce(jsonResponse(preview))
      .mockResolvedValueOnce(jsonResponse({ ...published, status: 'STALE' }))
      .mockResolvedValueOnce(jsonResponse({ allowed: true }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.listVersions('dataset/id', 20, 10)
    await datasetAPI.getVersion('dataset/id', 'version/id')
    await datasetAPI.getVersionUsage('dataset/id', 'version/id')
    await datasetAPI.previewVersion('dataset/id', 'version/id', 'query-1', { start_date: '2026-01-01' }, 50)
    await datasetAPI.transitionVersion('dataset/id', 'version/id', { expectedVersion: 7, expectedStatus: 'PUBLISHED', targetStatus: 'STALE' })
    await datasetAPI.evaluatePermission('dataset/id', 'PUBLISH')

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/datasets/dataset%2Fid/versions?limit=20&offset=10')
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).cache).toBe('no-store')
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/datasets/dataset%2Fid/versions/version%2Fid')
    expect((fetchMock.mock.calls[1]?.[1] as RequestInit).cache).toBe('no-store')
    expect(fetchMock.mock.calls[2]?.[0]).toBe('/api/v1/datasets/dataset%2Fid/versions/version%2Fid/usage')
    expect((fetchMock.mock.calls[2]?.[1] as RequestInit).cache).toBe('no-store')
    const [previewURL, previewInit] = fetchMock.mock.calls[3] as unknown as [string, RequestInit]
    expect(previewURL).toBe('/api/v1/datasets/dataset%2Fid/versions/version%2Fid/preview')
    expect(JSON.parse(String(previewInit.body))).toEqual({ queryId: 'query-1', parameters: { start_date: '2026-01-01' }, maxRows: 50 })
    const [statusURL, statusInit] = fetchMock.mock.calls[4] as unknown as [string, RequestInit]
    expect(statusURL).toBe('/api/v1/datasets/dataset%2Fid/versions/version%2Fid/status')
    expect(JSON.parse(String(statusInit.body))).toEqual({ expectedVersion: 7, expectedStatus: 'PUBLISHED', targetStatus: 'STALE' })
    const [permissionURL, permissionInit] = fetchMock.mock.calls[5] as unknown as [string, RequestInit]
    expect(permissionURL).toBe('/api/v1/permissions/evaluate')
    expect(JSON.parse(String(permissionInit.body))).toEqual({ resourceType: 'DATASET', action: 'PUBLISH', objectId: 'dataset/id' })
  })

  test('生成 UUID 形状的发布幂等键', () => {
    expect(createDatasetPublishIdempotencyKey()).toMatch(/^[0-9a-f-]{36}$/i)
  })

  test('发布校验错误保留全部路径、稳定代码和原因', () => {
    const error = new RequestError({
      code: 'DATASET_PUBLISH_VALIDATION_FAILED', message: '数据集发布前校验失败',
      details: [
        { path: 'nodes[0]', code: 'PUBLISH_DEPENDENCY_CHANGED', reason: '上游结构已变化' },
        { path: 'joins[1]', code: 'JOIN_FANOUT_RISK', reason: '关联存在扇出风险' },
      ],
    }, 422)
    expect(error.message).toContain('nodes[0] [PUBLISH_DEPENDENCY_CHANGED] 上游结构已变化')
    expect(error.message).toContain('joins[1] [JOIN_FANOUT_RISK] 关联存在扇出风险')
  })
})

function publishedRecord(): PublishedVersionRecord {
  return {
    id: 'published-version-1', datasetId: 'dataset-1', versionNo: 1, status: 'PUBLISHED',
    dslVersion: '1.0', dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), dsl: buildDatasetDSL(draft()),
    logicalPlan: {}, publishedAt: '2026-07-16T10:00:00Z', publishedBy: 'user-1',
    datasetRecordVersion: 6, draftVersionId: 'draft-version-1', draftRecordVersion: 3,
  }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
