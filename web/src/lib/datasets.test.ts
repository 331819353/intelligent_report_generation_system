import { afterEach, describe, expect, test, vi } from 'vitest'
import { RequestError } from './api'
import { buildComponentPreviewDSL, buildDatasetDSL, buildPreviewParameters, createDatasetPublishIdempotencyKey, datasetAPI, type AssetColumn, type AssetTable, type DatasetDraft, type PublishedVersionRecord, type PublishDatasetInput } from './datasets'

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
    value.joins.push({ id: 'orders_customers', leftNodeId: 'orders', rightNodeId: 'customers', leftField: 'order_date', rightField: 'customer_id', joinType: 'INNER', cardinality: '', manualConfirmed: false })
    value.grainKeys = ['o_order_date']
    const dsl = buildDatasetDSL(value)
    expect(dsl.dataset.type).toBe('CROSS_SOURCE')
    expect((dsl.joins as Array<Record<string, unknown>>)[0].manualConfirmed).toBe(false)
    expect((dsl.joins as Array<Record<string, unknown>>)[0].cardinality).toBe('UNKNOWN')
  })

  test('支持自定义输出字段、日期分组函数和计算字段聚合', () => {
    const value = draft()
    value.groupingEnabled = true
    value.fields[0] = { ...value.fields[0], code: 'order_month', name: '订单月份', groupBy: true, grouping: 'MONTH' }
    value.calculations[0] = { ...value.calculations[0], aggregation: 'AVG' }
    value.grainKeys = ['order_month']

    const dsl = buildDatasetDSL(value)
    expect(dsl.fields[0]).toMatchObject({
      code: 'order_month',
      name: '订单月份',
      role: 'DIMENSION',
      expression: { type: 'DATE_TRUNC', unit: 'MONTH' },
    })
    expect(dsl.fields[2]).toMatchObject({
      name: '双倍金额',
      expression: { type: 'AGGREGATE', function: 'AVG', argument: { type: 'ADD' } },
    })
    expect(dsl.groupBy).toEqual(['field_o_order_date'])
  })

  test('明细模式不生成分组计划', () => {
    const value = draft()
    value.groupingEnabled = false
    value.fields[1] = { ...value.fields[1], aggregation: '' }
    value.calculations = []

    const dsl = buildDatasetDSL(value)
    expect(dsl.groupBy).toEqual([])
  })

  test('关联后配置独立决定最终分组字段和指标逻辑', () => {
    const value = draft()
    value.nodes[0].groupingEnabled = true
    value.fields[0] = { ...value.fields[0], groupBy: true, grouping: 'DAY', output: true, finalGroupBy: true, finalGrouping: 'MONTH', finalOutput: false }
    value.fields[1] = { ...value.fields[1], metric: true, aggregation: 'SUM', output: true, finalMetric: true, finalAggregation: 'AVG', finalOutput: false }
    value.finalConfigured = true
    value.finalGroupingEnabled = true
    value.calculations = []

    const dsl = buildDatasetDSL(value)
    expect(dsl.fields).toHaveLength(2)
    expect(dsl.fields[0]).toMatchObject({ expression: { type: 'DATE_TRUNC', unit: 'MONTH' } })
    expect(dsl.fields[1]).toMatchObject({ role: 'MEASURE', expression: { type: 'AGGREGATE', function: 'AVG' } })
    expect(dsl.groupBy).toEqual(['field_o_order_date'])
  })

  test('保存数据节点先分组再进入关联槽位的执行拓扑', () => {
    const value = draft()
    const customerTable: AssetTable = { ...table, id: 'table-2', dataSourceId: 'source-2', dataSourceName: '客户库', dataSourceType: 'ORACLE', tableName: 'customers' }
    const customerColumn: AssetColumn = { ...columns[0], id: 'column-3', tableId: customerTable.id, columnName: 'customer_id', businessName: '客户编号', canonicalType: 'NUMBER' }
    value.nodes.push({ id: 'customers', alias: 'c', table: customerTable, columns: [customerColumn], selected: ['customer_id'] })
    value.fields = [
      { key: 'orders.order_date', role: 'TIME', aggregation: '', finalGroupBy: true, finalGrouping: 'MONTH' },
      { key: 'orders.amount', role: 'MEASURE', aggregation: '', finalMetric: true, finalAggregation: 'SUM' },
      { key: 'customers.customer_id', role: 'IDENTIFIER', aggregation: '', finalOutput: true },
    ]
    value.joins = [{ id: 'join_1', leftNodeId: 'orders', rightNodeId: 'customers', leftField: 'order_date', rightField: 'customer_id', joinType: 'LEFT', cardinality: '', manualConfirmed: true }]
    value.calculations = []
    value.finalConfigured = true
    value.finalGroupingEnabled = true
    value.preAggregation = { id: 'group_1', nodeId: 'orders', joinId: 'join_1', joinSide: 'LEFT' }
    value.finalOutputKeys = ['orders.order_date', 'orders.amount', 'customers.customer_id']
    value.grainKeys = ['o_order_date']

    const dsl = buildDatasetDSL(value)

    expect(dsl.preAggregations).toEqual([{
      id: 'group_1', nodeId: 'orders', joinId: 'join_1', joinSide: 'LEFT',
      groupBy: [{ field: 'order_date', unit: 'MONTH' }], metrics: [{ field: 'amount', function: 'SUM' }],
    }])
    expect(dsl.groupBy).toEqual([])
    expect((dsl.fields as Array<Record<string, unknown>>).map(field => field.expression)).toEqual([
      { type: 'FIELD_REF', nodeId: 'orders', field: 'order_date' },
      { type: 'FIELD_REF', nodeId: 'orders', field: 'amount' },
      { type: 'FIELD_REF', nodeId: 'customers', field: 'customer_id' },
    ])
  })

  test('Designer V1 持久化排布并将多个关联前分组编译为独立产物', () => {
    const value = draft()
    const customerTable: AssetTable = { ...table, id: 'table-2', dataSourceId: 'source-2', dataSourceName: '客户库', dataSourceType: 'ORACLE', tableName: 'customers', businessName: '客户' }
    const customerColumns: AssetColumn[] = [
      { ...columns[0], id: 'column-3', tableId: customerTable.id, columnName: 'customer_id', businessName: '客户编号', canonicalType: 'NUMBER' },
      { ...columns[1], id: 'column-4', tableId: customerTable.id, columnName: 'customer_name', businessName: '客户名称', canonicalType: 'TEXT' },
    ]
    value.nodes.push({ id: 'customers', alias: 'c', table: customerTable, columns: customerColumns, selected: ['customer_id', 'customer_name'] })
    value.fields.push(
      { key: 'customers.customer_id', role: 'IDENTIFIER', aggregation: '', code: 'customer_id', name: '客户编号' },
      { key: 'customers.customer_name', role: 'ATTRIBUTE', aggregation: '', code: 'customer_name', name: '客户名称' },
    )
    value.joins = [{ id: 'join_1', leftNodeId: 'orders', rightNodeId: 'customers', leftField: 'order_date', rightField: 'customer_id', joinType: 'LEFT', cardinality: '', manualConfirmed: true }]
    value.calculations = []
    value.grainKeys = ['order_month']
    value.designer = {
      version: '1.0',
      nodePositions: { orders: { x: 28, y: 44 }, customers: { x: 28, y: 244 } },
      nodeNames: { orders: '订单明细', customers: '客户明细' },
      groups: [
        { id: 'group_orders', name: '订单月汇总', input: { kind: 'NODE', id: 'orders' }, position: { x: 330, y: 44 }, dimensions: [{ key: 'orders.order_date', name: '订单月份', code: 'order_month', grouping: 'MONTH' }], metrics: [{ key: 'orders.amount', name: '交易金额', code: 'sales_amount', aggregation: 'SUM' }] },
        { id: 'group_customers', name: '客户汇总', input: { kind: 'NODE', id: 'customers' }, position: { x: 330, y: 244 }, dimensions: [{ key: 'customers.customer_id', name: '客户编号', code: 'customer_id' }], metrics: [{ key: 'customers.customer_name', name: '客户名称数', code: 'customer_name_count', aggregation: 'COUNT' }] },
      ],
      joins: [{ id: 'join_1', name: '月度客户关联', left: { kind: 'GROUP', id: 'group_orders' }, right: { kind: 'GROUP', id: 'group_customers' }, position: { x: 630, y: 144 }, outputKeys: ['orders.order_date', 'orders.amount', 'customers.customer_id'] }],
      end: { id: 'end_1', name: '月度客户结果', input: { kind: 'JOIN', id: 'join_1' }, position: { x: 930, y: 144 }, outputs: [
        { key: 'orders.order_date', name: '月份', code: 'order_month' },
        { key: 'orders.amount', name: '成交金额', code: 'sales_amount' },
        { key: 'customers.customer_id', name: '客户', code: 'customer_id' },
      ] },
    }

    const dsl = buildDatasetDSL(value)

    expect(dsl.preAggregations).toEqual([
      { id: 'group_orders', nodeId: 'orders', joinId: 'join_1', joinSide: 'LEFT', groupBy: [{ field: 'order_date', unit: 'MONTH' }], metrics: [{ field: 'amount', function: 'SUM' }] },
      { id: 'group_customers', nodeId: 'customers', joinId: 'join_1', joinSide: 'RIGHT', groupBy: [{ field: 'customer_id' }], metrics: [{ field: 'customer_name', function: 'COUNT' }] },
    ])
    expect(dsl.fields.map(field => ({ code: field.code, name: field.name, role: field.role, expression: field.expression }))).toEqual([
      { code: 'order_month', name: '月份', role: 'DIMENSION', expression: { type: 'FIELD_REF', nodeId: 'orders', field: 'order_date' } },
      { code: 'sales_amount', name: '成交金额', role: 'MEASURE', expression: { type: 'FIELD_REF', nodeId: 'orders', field: 'amount' } },
      { code: 'customer_id', name: '客户', role: 'DIMENSION', expression: { type: 'FIELD_REF', nodeId: 'customers', field: 'customer_id' } },
    ])
    expect(dsl.designer?.nodePositions.orders).toEqual({ x: 28, y: 44 })
    expect(dsl.designer?.groups).toHaveLength(2)
  })

  test('结束节点前的根分组生成最终 groupBy 与聚合字段', () => {
    const value = draft()
    value.calculations = []
    value.grainKeys = ['order_month']
    value.designer = {
      version: '1.0', nodePositions: { orders: { x: 20, y: 40 } }, nodeNames: { orders: '订单' }, joins: [],
      groups: [{ id: 'group_final', name: '月度汇总', input: { kind: 'NODE', id: 'orders' }, position: { x: 320, y: 40 }, dimensions: [{ key: 'orders.order_date', name: '订单月份', code: 'order_month', grouping: 'MONTH' }], metrics: [{ key: 'orders.amount', name: '月成交额', code: 'monthly_sales', aggregation: 'SUM' }] }],
      end: { id: 'end_1', name: '月度订单结果', input: { kind: 'GROUP', id: 'group_final' }, position: { x: 620, y: 40 }, outputs: [{ key: 'orders.order_date', name: '月份', code: 'order_month' }, { key: 'orders.amount', name: '成交额', code: 'monthly_sales' }] },
    }

    const dsl = buildDatasetDSL(value)

    expect(dsl.preAggregations).toEqual([])
    expect(dsl.groupBy).toEqual(['field_o_order_date'])
    expect(dsl.fields[0]).toMatchObject({ code: 'order_month', name: '月份', expression: { type: 'DATE_TRUNC', unit: 'MONTH' } })
    expect(dsl.fields[1]).toMatchObject({ code: 'monthly_sales', name: '成交额', role: 'MEASURE', expression: { type: 'AGGREGATE', function: 'SUM' } })
  })

  test('结束节点前的字段处理组件把结构化表达式写入可执行字段', () => {
    const value = draft()
    value.calculations = []
    value.grainKeys = ['amount_text']
    value.designer = {
      version: '1.0', nodePositions: { orders: { x: 20, y: 40 } }, nodeNames: { orders: '订单' }, joins: [], groups: [],
      transforms: [
        {
          id: 'transform_amount', name: '金额加倍', family: 'NUMBER', input: { kind: 'NODE', id: 'orders' }, position: { x: 320, y: 40 },
          rules: [{ id: 'double_amount', operation: 'ADD', inputKeys: ['orders.amount', 'orders.amount'], output: { id: 'double_amount', name: '双倍金额', code: 'double_amount', canonicalType: 'DECIMAL' } }],
        },
        {
          id: 'transform_text', name: '金额文本', family: 'CAST', input: { kind: 'TRANSFORM', id: 'transform_amount' }, position: { x: 620, y: 40 },
          rules: [{ id: 'amount_text', operation: 'CAST', inputKeys: ['transform_amount.double_amount'], targetType: 'STRING', output: { id: 'amount_text', name: '金额文本', code: 'amount_text', canonicalType: 'STRING' } }],
        },
      ],
      end: { id: 'end_1', name: '处理结果', input: { kind: 'TRANSFORM', id: 'transform_text' }, position: { x: 920, y: 40 }, outputs: [{ key: 'transform_text.amount_text', name: '金额文本', code: 'amount_text' }] },
    }

    const dsl = buildDatasetDSL(value)

    expect(dsl.fields).toEqual([expect.objectContaining({
      code: 'amount_text', canonicalType: 'STRING', expression: {
        type: 'CAST', targetType: 'STRING', argument: {
          type: 'ADD', arguments: [
            { type: 'FIELD_REF', nodeId: 'orders', field: 'amount' },
            { type: 'FIELD_REF', nodeId: 'orders', field: 'amount' },
          ],
        },
      },
    })])
    expect(dsl.designer?.transforms).toHaveLength(2)
  })

  test('根分组产物可以继续字段处理且不会丢失聚合口径', () => {
    const value = draft()
    value.calculations = []
    value.grainKeys = ['order_month']
    value.designer = {
      version: '1.0', nodePositions: { orders: { x: 20, y: 40 } }, nodeNames: { orders: '订单' }, joins: [],
      groups: [{ id: 'group_final', name: '月度汇总', input: { kind: 'NODE', id: 'orders' }, position: { x: 320, y: 40 }, dimensions: [{ key: 'orders.order_date', name: '订单月份', code: 'order_month', grouping: 'MONTH' }], metrics: [{ key: 'orders.amount', name: '月成交额', code: 'monthly_sales', aggregation: 'SUM' }] }],
      transforms: [{
        id: 'transform_summary', name: '汇总结果格式化', family: 'CAST', input: { kind: 'GROUP', id: 'group_final' }, position: { x: 620, y: 40 },
        rules: [
          { id: 'month_text', operation: 'CAST', inputKeys: ['orders.order_date'], targetType: 'STRING', output: { id: 'month_text', name: '月份文本', code: 'month_text', canonicalType: 'STRING' } },
          { id: 'sales_text', operation: 'CAST', inputKeys: ['orders.amount'], targetType: 'STRING', output: { id: 'sales_text', name: '成交额文本', code: 'sales_text', canonicalType: 'STRING' } },
        ],
      }],
      end: { id: 'end_1', name: '格式化汇总', input: { kind: 'TRANSFORM', id: 'transform_summary' }, position: { x: 920, y: 40 }, outputs: [{ key: 'transform_summary.month_text', name: '月份', code: 'month_text' }, { key: 'transform_summary.sales_text', name: '成交额', code: 'sales_text' }] },
    }

    const dsl = buildDatasetDSL(value)
    const hiddenDimension = dsl.fields.find(field => field.visible === false)

    expect(dsl.preAggregations).toEqual([])
    expect(dsl.fields.find(field => field.code === 'month_text')).toMatchObject({
      role: 'DIMENSION', expression: { type: 'CAST', argument: { type: 'DATE_TRUNC', unit: 'MONTH' } },
    })
    expect(dsl.fields.find(field => field.code === 'sales_text')).toMatchObject({
      role: 'MEASURE', expression: { type: 'CAST', argument: { type: 'AGGREGATE', function: 'SUM' } },
    })
    expect(hiddenDimension).toMatchObject({ code: 'order_month', role: 'DIMENSION', expression: { type: 'DATE_TRUNC', unit: 'MONTH' } })
    expect(dsl.groupBy).toEqual([hiddenDimension?.id])
  })

  test('组件预览只物化目标组件及其上游子图', () => {
    const value = draft()
    value.designer = {
      version: '1.0', nodePositions: { orders: { x: 20, y: 40 } }, nodeNames: { orders: '订单' }, joins: [], groups: [],
      transforms: [
        {
          id: 'transform_amount', name: '金额加倍', family: 'NUMBER', input: { kind: 'NODE', id: 'orders' }, position: { x: 320, y: 40 },
          rules: [{ id: 'double_amount', operation: 'ADD', inputKeys: ['orders.amount', 'orders.amount'], output: { id: 'double_amount', name: '双倍金额', code: 'double_amount', canonicalType: 'DECIMAL' } }],
        },
        {
          id: 'transform_text', name: '金额文本', family: 'CAST', input: { kind: 'TRANSFORM', id: 'transform_amount' }, position: { x: 620, y: 40 },
          rules: [{ id: 'amount_text', operation: 'CAST', inputKeys: ['transform_amount.double_amount'], targetType: 'STRING', output: { id: 'amount_text', name: '金额文本', code: 'amount_text', canonicalType: 'STRING' } }],
        },
      ],
      end: { id: 'end_1', name: '最终输出', input: { kind: 'TRANSFORM', id: 'transform_text' }, position: { x: 920, y: 40 }, outputs: [{ key: 'transform_text.amount_text', name: '金额文本', code: 'amount_text' }] },
    }

    const preview = buildComponentPreviewDSL(value, { kind: 'TRANSFORM', id: 'transform_amount' })

    expect(preview.designer?.transforms).toHaveLength(1)
    expect(preview.designer?.transforms?.[0].id).toBe('transform_amount')
    expect(preview.designer?.end?.input).toEqual({ kind: 'TRANSFORM', id: 'transform_amount' })
    expect(preview.fields).toEqual(expect.arrayContaining([
      expect.objectContaining({ code: 'order_date' }),
      expect.objectContaining({ code: 'amount' }),
      expect.objectContaining({ code: 'double_amount', expression: { type: 'ADD', arguments: expect.any(Array) } }),
    ]))
    expect(preview.fields).not.toEqual(expect.arrayContaining([expect.objectContaining({ code: 'amount_text' })]))
  })

  test('结束节点隐藏根分组维度时仍保留真实聚合粒度并清理失效排序', () => {
    const value = draft()
    value.calculations = []
    value.sorts = [{ fieldId: 'removed_output', direction: 'ASC' }]
    value.designer = {
      version: '1.0', nodePositions: { orders: { x: 20, y: 40 } }, nodeNames: { orders: '订单' }, joins: [],
      groups: [{ id: 'group_final', name: '月度汇总', input: { kind: 'NODE', id: 'orders' }, position: { x: 320, y: 40 }, dimensions: [{ key: 'orders.order_date', name: '订单月份', code: 'order_month', grouping: 'MONTH' }], metrics: [{ key: 'orders.amount', name: '月成交额', code: 'monthly_sales', aggregation: 'SUM' }] }],
      end: { id: 'end_1', name: '月度订单结果', input: { kind: 'GROUP', id: 'group_final' }, position: { x: 620, y: 40 }, outputs: [{ key: 'orders.amount', name: '成交额', code: 'monthly_sales' }] },
    }

    const dsl = buildDatasetDSL(value)
    const dimension = dsl.fields.find(field => field.code === 'order_month')

    expect(dimension).toMatchObject({ visible: false, expression: { type: 'DATE_TRUNC', unit: 'MONTH' } })
    expect(dsl.groupBy).toEqual([dimension?.id])
    expect(dsl.outputGrain).toMatchObject({ keyFields: ['order_month'] })
    expect(dsl.sorts).toEqual([])
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
  test('数据集字段目录过滤资产审计保留的失效字段', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [
      { ...columns[0], assetStatus: 'ACTIVE' },
      { ...columns[1], assetStatus: 'INACTIVE' },
    ] }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await datasetAPI.columns(table.id)

    expect(result.items.map(item => item.columnName)).toEqual(['order_date'])
  })

  test('建模资产目录只读取启用且已完成 LLM 映射的表', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [], total: 0 }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.mappingTables(30, 60)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/assets/tables?limit=30&offset=60&status=ACTIVE&managementStatus=ENABLED&enrichedOnly=true')
    expect(init.cache).toBe('no-store')
  })

  test('数据节点预览只请求受控的前五行采样接口', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ columns: ['id'], rows: [[1]] }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.tablePreview(table.id, 5)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe(`/api/v1/assets/tables/${table.id}/preview?maxRows=5`)
    expect(init.cache).toBe('no-store')
  })

  test('指标编辑器读取数据集摘要目录时携带分页并禁用缓存', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [], total: 0, limit: 25, offset: 50 }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.list(25, 50)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/datasets?limit=25&offset=50')
    expect(init.cache).toBe('no-store')
  })

  test('读取可变数据集聚合时禁用缓存', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: 'dataset-1' }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.get('dataset/id')

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/datasets/dataset%2Fid')
    expect(init.cache).toBe('no-store')
  })

  test('停用与恢复都携带聚合版本并使用独立生命周期接口', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ id: 'dataset/id', status: 'DISABLED', version: 8 }))
      .mockResolvedValueOnce(jsonResponse({ id: 'dataset/id', status: 'PUBLISHED', version: 9 }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.disable('dataset/id', 7)
    await datasetAPI.restore('dataset/id', 8)

    const [disableURL, disableInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(disableURL).toBe('/api/v1/datasets/dataset%2Fid/disable')
    expect(disableInit.method).toBe('POST')
    expect(JSON.parse(String(disableInit.body))).toEqual({ expectedVersion: 7 })
    const [restoreURL, restoreInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit]
    expect(restoreURL).toBe('/api/v1/datasets/dataset%2Fid/restore')
    expect(restoreInit.method).toBe('POST')
    expect(JSON.parse(String(restoreInit.body))).toEqual({ expectedVersion: 8 })
  })

  test('读取配置修订历史并按精确修订回滚', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ items: [], total: 0, limit: 25, offset: 5 }))
      .mockResolvedValueOnce(jsonResponse({ id: 'revision/id' }))
      .mockResolvedValueOnce(jsonResponse({ queryId: 'revision-preview', columns: ['customer_id'], rows: [[1]], rowCount: 1, durationMs: 3 }))
      .mockResolvedValueOnce(jsonResponse({ id: 'dataset/id', version: 8 }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.listRevisions('dataset/id', 25, 5)
    await datasetAPI.getRevision('dataset/id', 'revision/id')
    await datasetAPI.previewRevision('dataset/id', 'revision/id', 'revision-preview', {})
    await datasetAPI.rollbackRevision('dataset/id', 'revision/id', 7)

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/datasets/dataset%2Fid/revisions?limit=25&offset=5')
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).cache).toBe('no-store')
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/datasets/dataset%2Fid/revisions/revision%2Fid')
    expect((fetchMock.mock.calls[1]?.[1] as RequestInit).cache).toBe('no-store')
    const [previewURL, previewInit] = fetchMock.mock.calls[2] as unknown as [string, RequestInit]
    expect(previewURL).toBe('/api/v1/datasets/dataset%2Fid/revisions/revision%2Fid/preview')
    expect(previewInit.method).toBe('POST')
    expect(JSON.parse(String(previewInit.body))).toEqual({ queryId: 'revision-preview', parameters: {}, maxRows: 5 })
    const [rollbackURL, rollbackInit] = fetchMock.mock.calls[3] as unknown as [string, RequestInit]
    expect(rollbackURL).toBe('/api/v1/datasets/dataset%2Fid/revisions/revision%2Fid/rollback')
    expect(rollbackInit.method).toBe('POST')
    expect(JSON.parse(String(rollbackInit.body))).toEqual({ expectedVersion: 7 })
  })

  test('未保存草稿预览携带当前候选 DSL、并发版本且禁止缓存', async () => {
    const candidate = buildDatasetDSL(draft())
    const fetchMock = vi.fn(async () => jsonResponse({
      queryId: 'candidate-preview', dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), baseVersion: 7,
      columns: ['order_date'], rows: [['2026-01-01']], rowCount: 1, durationMs: 4,
    }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.previewDraft('dataset/id', 7, candidate, 'candidate-preview', { start_date: '2026-01-01' }, 5)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/datasets/dataset%2Fid/draft/preview')
    expect(init.method).toBe('POST')
    expect(init.cache).toBe('no-store')
    expect(JSON.parse(String(init.body))).toEqual({
      queryId: 'candidate-preview', expectedVersion: 7, dsl: candidate,
      parameters: { start_date: '2026-01-01' }, maxRows: 5,
    })
  })

  test('发布审批 API 冻结草稿并支持查询、通过和拒绝', async () => {
    const request = {
      id: 'request/id', datasetId: 'dataset/id', status: 'PENDING', version: 1,
      draftVersionId: 'draft-version-1', expectedDatasetVersion: 5, expectedDraftRecordVersion: 3,
      expectedDslHash: 'a'.repeat(64), expectedPlanHash: 'b'.repeat(64), requesterId: 'user-1',
      requestNote: '指标所需数据集', submittedAt: '2026-07-20T10:00:00Z', updatedAt: '2026-07-20T10:00:00Z',
    }
    const approved = { request: { ...request, status: 'APPROVED' }, publishedVersion: publishedRecord() }
    const rejected = { ...request, status: 'REJECTED', reviewNote: '字段口径待确认' }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(request, 202))
      .mockResolvedValueOnce(jsonResponse({ items: [request], total: 1, limit: 20, offset: 5 }))
      .mockResolvedValueOnce(jsonResponse(approved))
      .mockResolvedValueOnce(jsonResponse(rejected))
    vi.stubGlobal('fetch', fetchMock)
    const input: PublishDatasetInput = {
      draftVersionId: 'draft-version-1', expectedVersion: 5, expectedDraftRecordVersion: 3,
      expectedDslHash: 'a'.repeat(64), validationParameters: { start_date: '2026-01-01' },
    }

    await expect(datasetAPI.requestPublication('dataset/id', input, '指标所需数据集')).resolves.toEqual(request)
    await datasetAPI.listPublicationRequests('dataset/id', 20, 5)
    await datasetAPI.approvePublication('dataset/id', 'request/id', 1, '校验通过')
    await datasetAPI.rejectPublication('dataset/id', 'request/id', 1, '字段口径待确认')

    const [submitURL, submitInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(submitURL).toBe('/api/v1/datasets/dataset%2Fid/publish-requests')
    expect(submitInit.method).toBe('POST')
    expect(JSON.parse(String(submitInit.body))).toEqual({ ...input, note: '指标所需数据集' })
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/v1/datasets/dataset%2Fid/publish-requests?limit=20&offset=5')
    expect((fetchMock.mock.calls[1]?.[1] as RequestInit).cache).toBe('no-store')
    const [approveURL, approveInit] = fetchMock.mock.calls[2] as unknown as [string, RequestInit]
    expect(approveURL).toBe('/api/v1/datasets/dataset%2Fid/publish-requests/request%2Fid/approve')
    expect(JSON.parse(String(approveInit.body))).toEqual({ expectedVersion: 1, note: '校验通过' })
    const [rejectURL, rejectInit] = fetchMock.mock.calls[3] as unknown as [string, RequestInit]
    expect(rejectURL).toBe('/api/v1/datasets/dataset%2Fid/publish-requests/request%2Fid/reject')
    expect(JSON.parse(String(rejectInit.body))).toEqual({ expectedVersion: 1, reason: '字段口径待确认' })
  })

  test('按父数据集访问版本目录、精确版本、使用汇总、版本预览和安全回滚', async () => {
    const published = publishedRecord()
    const usage = { reportDraftReferences: 2, downstreamDraftReferences: 3, downstreamPublishedReferences: 4, activeQueryRuns: 1 }
    const preview = { queryId: 'query-1', columns: ['order_date'], rows: [['2026-01-01']], rowCount: 1, durationMs: 8 }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ items: [published], total: 1, limit: 20, offset: 10 }))
      .mockResolvedValueOnce(jsonResponse(published))
      .mockResolvedValueOnce(jsonResponse(usage))
      .mockResolvedValueOnce(jsonResponse(preview))
      .mockResolvedValueOnce(jsonResponse({ id: 'dataset/id', version: 8 }))
      .mockResolvedValueOnce(jsonResponse({ ...published, status: 'STALE' }))
      .mockResolvedValueOnce(jsonResponse({ allowed: true }))
    vi.stubGlobal('fetch', fetchMock)

    await datasetAPI.listVersions('dataset/id', 20, 10)
    await datasetAPI.getVersion('dataset/id', 'version/id')
    await datasetAPI.getVersionUsage('dataset/id', 'version/id')
    await datasetAPI.previewVersion('dataset/id', 'version/id', 'query-1', { start_date: '2026-01-01' }, 50)
    await datasetAPI.rollbackVersion('dataset/id', 'version/id', 7)
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
    const [rollbackURL, rollbackInit] = fetchMock.mock.calls[4] as unknown as [string, RequestInit]
    expect(rollbackURL).toBe('/api/v1/datasets/dataset%2Fid/versions/version%2Fid/rollback')
    expect(rollbackInit.method).toBe('POST')
    expect(JSON.parse(String(rollbackInit.body))).toEqual({ expectedVersion: 7 })
    const [statusURL, statusInit] = fetchMock.mock.calls[5] as unknown as [string, RequestInit]
    expect(statusURL).toBe('/api/v1/datasets/dataset%2Fid/versions/version%2Fid/status')
    expect(JSON.parse(String(statusInit.body))).toEqual({ expectedVersion: 7, expectedStatus: 'PUBLISHED', targetStatus: 'STALE' })
    const [permissionURL, permissionInit] = fetchMock.mock.calls[6] as unknown as [string, RequestInit]
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
