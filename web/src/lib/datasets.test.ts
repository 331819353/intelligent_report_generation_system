import { describe, expect, test } from 'vitest'
import { buildDatasetDSL, buildPreviewParameters, type AssetColumn, type AssetTable, type DatasetDraft } from './datasets'

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
