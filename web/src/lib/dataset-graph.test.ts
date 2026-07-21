import { describe, expect, it } from 'vitest'

import {
  generatedGraphFieldIdentity,
  graphContains,
  graphConnectionError,
  graphLeaves,
  graphProducedFieldLabel,
  graphProducedFields,
  graphRoot,
  hydrateDesignerGraph,
  layoutDesignerGraph,
  serializeDesignerGraph,
  validateDesignerGraph,
  wouldCreateGraphCycle,
  type DesignerGraphV1,
} from './dataset-graph'
import type { AssetColumn, AssetTable, DatasetDSL, DesignerNode, FieldOption, JoinOption } from './datasets'

const assetTable = (id: string, businessName: string): AssetTable => ({
  id,
  dataSourceId: 'source_1',
  dataSourceName: '测试数据源',
  dataSourceType: 'MYSQL',
  tableName: id,
  schemaName: 'public',
  businessName,
  columnCount: 2,
})

const column = (tableId: string, columnName: string, businessName: string, canonicalType = 'STRING'): AssetColumn => ({
  id: `${tableId}_${columnName}`,
  tableId,
  columnName,
  businessName,
  canonicalType,
  nullable: false,
  semanticType: '',
})

const node = (id: string, businessName: string, columns: AssetColumn[]): DesignerNode => ({
  id,
  alias: id,
  table: assetTable(`table_${id}`, businessName),
  columns,
  selected: columns.map(item => item.columnName),
})

const nodes: DesignerNode[] = [
  node('customer', '客户表', [
    column('table_customer', 'customer_id', '客户ID'),
    column('table_customer', 'amount', '交易金额', 'DECIMAL'),
  ]),
  node('region', '区域表', [
    column('table_region', 'region_code', '区域编码'),
    column('table_region', 'order_id', '订单ID'),
  ]),
]

const fields: FieldOption[] = nodes.flatMap(item => item.columns.map(itemColumn => ({
  key: `${item.id}.${itemColumn.columnName}`,
  role: 'ATTRIBUTE',
  aggregation: '',
  output: true,
})))

const emptyDSL = (designer: DesignerGraphV1): DatasetDSL => ({
  dslVersion: '1.0',
  dataset: { code: 'graph_test', name: '图模型测试', type: 'SINGLE_SOURCE' },
  nodes: [],
  fields: [],
  designer,
})

describe('dataset graph', () => {
  it('按上游稳定编码自动生成只读字段身份，且不依赖聚合逻辑', () => {
    expect(generatedGraphFieldIdentity({ key: 'node_2.customer_id', name: '客户编号', code: 't2_CUSTOMER_ID' })).toEqual({
      name: '客户编号',
      code: 't2_CUSTOMER_ID',
    })
    expect(generatedGraphFieldIdentity({ key: 'node_2.customer-id', name: '', code: 't2.CUSTOMER-ID' })).toEqual({
      name: 't2_CUSTOMER_ID',
      code: 't2_CUSTOMER_ID',
    })
  })

  it('按依赖层级稳定整理，且不受整理前坐标影响', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: {
        customer: { x: 999, y: 777 },
        order: { x: 888, y: 666 },
        region: { x: 777, y: 555 },
      },
      nodeNames: { customer: '客户表', order: '订单表', region: '区域表' },
      groups: [
        {
          id: 'group_customer',
          name: '客户汇总',
          input: { kind: 'NODE', id: 'customer' },
          position: { x: 700, y: 700 },
          dimensions: [],
          metrics: [],
        },
        {
          id: 'group_region',
          name: '区域汇总',
          input: { kind: 'NODE', id: 'region' },
          position: { x: 600, y: 600 },
          dimensions: [],
          metrics: [],
        },
      ],
      joins: [
        {
          id: 'join_customer_order',
          name: '客户订单关联',
          left: { kind: 'GROUP', id: 'group_customer' },
          right: { kind: 'NODE', id: 'order' },
          position: { x: 500, y: 500 },
          outputKeys: [],
        },
        {
          id: 'join_all',
          name: '完整关联',
          left: { kind: 'JOIN', id: 'join_customer_order' },
          right: { kind: 'GROUP', id: 'group_region' },
          position: { x: 400, y: 400 },
          outputKeys: [],
        },
      ],
      end: {
        id: 'end_1',
        name: '最终输出',
        input: { kind: 'JOIN', id: 'join_all' },
        position: { x: 300, y: 300 },
        outputs: [],
      },
    }

    const arranged = layoutDesignerGraph(graph, ['customer', 'order', 'region'])
    const arrangedAgain = layoutDesignerGraph({
      ...graph,
      nodePositions: Object.fromEntries(Object.keys(graph.nodePositions).map((id, index) => [id, { x: index, y: index }])),
      groups: graph.groups.map((item, index) => ({ ...item, position: { x: index, y: index } })),
      joins: graph.joins.map((item, index) => ({ ...item, position: { x: index, y: index } })),
      end: graph.end && { ...graph.end, position: { x: 1, y: 1 } },
    }, ['customer', 'order', 'region'])

    expect(arrangedAgain).toEqual(arranged)
    expect(arranged.nodePositions.customer.x).toBeLessThan(arranged.groups[0].position.x)
    expect(arranged.groups[0].position.x).toBeLessThan(arranged.joins[0].position.x)
    expect(arranged.joins[0].position.x).toBeLessThan(arranged.joins[1].position.x)
    expect(arranged.joins[1].position.x).toBeLessThan(arranged.end!.position.x)
    expect(new Set(Object.values(arranged.nodePositions).map(item => item.y)).size).toBe(3)
  })

  it('serialize 后 hydrate 精确保留节点、关联、分组和结束节点坐标及名称', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { customer: { x: 17, y: 23 }, region: { x: 91, y: 37 } },
      nodeNames: { customer: '客户数据节点', region: '区域数据节点' },
      joins: [{
        id: 'join_1',
        name: '客户区域结果',
        left: { kind: 'GROUP', id: 'group_customer' },
        right: { kind: 'GROUP', id: 'group_region' },
        position: { x: 417, y: 223 },
        outputKeys: ['customer.customer_id', 'region.region_code'],
      }],
      groups: [
        {
          id: 'group_customer',
          name: '客户聚合结果',
          input: { kind: 'NODE', id: 'customer' },
          position: { x: 137, y: 83 },
          dimensions: [{ key: 'customer.customer_id', name: '客户标识', code: 'customer_key', grouping: 'DAY' }],
          metrics: [{ key: 'customer.amount', name: '客户金额', code: 'customer_amount', aggregation: 'SUM' }],
        },
        {
          id: 'group_region',
          name: '区域聚合结果',
          input: { kind: 'NODE', id: 'region' },
          position: { x: 149, y: 271 },
          dimensions: [{ key: 'region.region_code', name: '区域', code: 'region_key' }],
          metrics: [{ key: 'region.order_id', name: '订单量', code: 'order_count', aggregation: 'COUNT' }],
        },
      ],
      end: {
        id: 'end_1',
        name: '数据集最终结果',
        input: { kind: 'JOIN', id: 'join_1' },
        position: { x: 713, y: 211 },
        outputs: [
          { key: 'customer.customer_id', name: '客户ID', code: 'customer_id' },
          { key: 'region.region_code', name: '所属区域', code: 'region_code' },
        ],
      },
    }
    const serialized = serializeDesignerGraph(graph)
    const legacyJoins: JoinOption[] = [{
      id: 'join_1',
      leftNodeId: 'customer',
      rightNodeId: 'region',
      leftField: 'customer_id',
      rightField: 'region_code',
      joinType: 'LEFT',
      cardinality: 'UNKNOWN',
      manualConfirmed: true,
    }]

    const hydrated = hydrateDesignerGraph(emptyDSL(serialized), nodes, legacyJoins, fields)

    expect(hydrated).toEqual(serialized)
    expect(hydrated.nodeNames).toEqual({ customer: '客户数据节点', region: '区域数据节点' })
    expect(hydrated.joins[0]).toMatchObject({ name: '客户区域结果', position: { x: 417, y: 223 } })
    expect(hydrated.groups.map(item => [item.name, item.position])).toEqual([
      ['客户聚合结果', { x: 137, y: 83 }],
      ['区域聚合结果', { x: 149, y: 271 }],
    ])
    expect(hydrated.end).toMatchObject({ name: '数据集最终结果', position: { x: 713, y: 211 } })
  })

  it('fixed designer 缺少结束节点时将无关联单表修复为数据节点直连结束节点', () => {
    const singleNode = nodes[0]
    const singleFields = fields.filter(field => field.key.startsWith(`${singleNode.id}.`))
    const designer: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { [singleNode.id]: { x: 77, y: 91 } },
      nodeNames: { [singleNode.id]: '客户数据节点' },
      joins: [],
      groups: [],
    }

    const hydrated = hydrateDesignerGraph(emptyDSL(designer), [singleNode], [], singleFields)

    expect(hydrated.end).toMatchObject({
      id: 'end_1',
      input: { kind: 'NODE', id: singleNode.id },
      position: { x: 377, y: 91 },
    })
    expect(hydrated.end?.outputs.map(output => output.key)).toEqual([
      'customer.customer_id',
      'customer.amount',
    ])
  })

  it('多个分组分别产出字段，并用组件名、字段名、编码和语义角色构造下游标签', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { customer: { x: 0, y: 0 }, region: { x: 0, y: 150 } },
      nodeNames: { customer: '客户数据节点', region: '区域数据节点' },
      joins: [{
        id: 'join_1',
        name: '客户区域关联',
        left: { kind: 'GROUP', id: 'group_customer' },
        right: { kind: 'GROUP', id: 'group_region' },
        position: { x: 600, y: 75 },
        outputKeys: [],
      }],
      groups: [
        {
          id: 'group_customer',
          name: '客户汇总结果',
          input: { kind: 'NODE', id: 'customer' },
          position: { x: 300, y: 0 },
          dimensions: [{ key: 'customer.customer_id', name: '客户标识', code: 'customer_key' }],
          metrics: [{ key: 'customer.amount', name: '客户总金额', code: 'customer_amount_sum', aggregation: 'SUM' }],
        },
        {
          id: 'group_region',
          name: '区域汇总结果',
          input: { kind: 'NODE', id: 'region' },
          position: { x: 300, y: 150 },
          dimensions: [{ key: 'region.region_code', name: '销售区域', code: 'sales_region' }],
          metrics: [{ key: 'region.order_id', name: '区域订单数', code: 'region_order_count', aggregation: 'COUNT' }],
        },
      ],
    }

    const customerOutputs = graphProducedFields({ kind: 'GROUP', id: 'group_customer' }, graph, nodes, fields)
    const regionOutputs = graphProducedFields({ kind: 'GROUP', id: 'group_region' }, graph, nodes, fields)

    expect(customerOutputs.map(item => item.key)).toEqual(['customer.customer_id', 'customer.amount'])
    expect(regionOutputs.map(item => item.key)).toEqual(['region.region_code', 'region.order_id'])
    expect(customerOutputs).not.toEqual(regionOutputs)
    expect(customerOutputs.map(graphProducedFieldLabel)).toEqual([
      '客户汇总结果 / 客户标识 · customer_key · 维度',
      '客户汇总结果 / 客户总金额 · customer_amount_sum · SUM 指标',
    ])
    expect(regionOutputs.map(graphProducedFieldLabel)).toEqual([
      '区域汇总结果 / 销售区域 · sales_region · 维度',
      '区域汇总结果 / 区域订单数 · region_order_count · COUNT 指标',
    ])
    expect(customerOutputs.every(item => item.producerName === '客户汇总结果')).toBe(true)
    expect(regionOutputs.every(item => item.producerName === '区域汇总结果')).toBe(true)
  })

  it('字段处理组件保留上游字段并生成可串联的结构化表达式', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { customer: { x: 0, y: 0 } },
      nodeNames: { customer: '客户数据节点' },
      joins: [],
      groups: [],
      transforms: [
        {
          id: 'transform_amount', name: '金额换算', family: 'NUMBER', componentType: 'NUMBER_ARITHMETIC', input: { kind: 'NODE', id: 'customer' }, position: { x: 300, y: 0 },
          rules: [{ id: 'rule_double', operation: 'ADD', inputKeys: ['customer.amount', 'customer.amount'], output: { id: 'double_amount', name: '双倍金额', code: 'double_amount', canonicalType: 'DECIMAL' } }],
        },
        {
          id: 'transform_cast', name: '金额文本化', family: 'CAST', componentType: 'CAST', input: { kind: 'TRANSFORM', id: 'transform_amount' }, position: { x: 600, y: 0 },
          rules: [{ id: 'rule_cast', operation: 'CAST', inputKeys: ['transform_amount.double_amount'], targetType: 'STRING', output: { id: 'amount_text', name: '金额文本', code: 'amount_text', canonicalType: 'STRING' } }],
        },
      ],
      end: { id: 'end_1', name: '最终输出', input: { kind: 'TRANSFORM', id: 'transform_cast' }, position: { x: 900, y: 0 }, outputs: [] },
    }

    const outputs = graphProducedFields(graph.end?.input, graph, nodes, fields)

    expect(outputs.map(item => item.key)).toEqual(['customer.customer_id', 'customer.amount', 'transform_amount.double_amount', 'transform_cast.amount_text'])
    expect(outputs.at(-1)?.expression).toEqual({
      type: 'CAST', targetType: 'STRING', argument: {
        type: 'ADD',
        arguments: [
          { type: 'FIELD_REF', nodeId: 'customer', field: 'amount' },
          { type: 'FIELD_REF', nodeId: 'customer', field: 'amount' },
        ],
      },
    })
    expect(validateDesignerGraph(graph, ['customer']).valid).toBe(true)
    const hydrated = hydrateDesignerGraph(emptyDSL(serializeDesignerGraph(graph)), nodes.slice(0, 1), [], fields.filter(field => field.key.startsWith('customer.')))
    expect(hydrated).toEqual(serializeDesignerGraph(graph))
    expect(hydrated.transforms?.map(transform => transform.componentType)).toEqual(['NUMBER_ARITHMETIC', 'CAST'])
  })

  it('日期处理生成字符串格式化表达式，并把旧日期组件从截断语义迁移为年月编码', () => {
    const dateNode = node('orders', '订单表', [column('table_orders', 'created_at', '下单时间', 'DATETIME')])
    const dateFields: FieldOption[] = [{ key: 'orders.created_at', name: '下单时间', code: 'created_at', role: 'ATTRIBUTE', aggregation: '', output: true }]
    const graph: DesignerGraphV1 = {
      version: '1.0', nodePositions: { orders: { x: 0, y: 0 } }, nodeNames: { orders: '订单表' }, joins: [], groups: [],
      transforms: [{
        id: 'transform_date', name: '日期处理 1', family: 'DATE', input: { kind: 'NODE', id: 'orders' }, position: { x: 300, y: 0 },
        rules: [{ id: 'rule_1', operation: 'DATE_FORMAT', inputKeys: ['orders.created_at'], unit: 'MONTH', output: { id: 'output_1', name: '下单时间年月', code: 'created_at_yyyymm', canonicalType: 'STRING' } }],
      }],
    }

    const formatted = graphProducedFields({ kind: 'TRANSFORM', id: 'transform_date' }, graph, [dateNode], dateFields).at(-1)
    expect(formatted).toMatchObject({ code: 'created_at_yyyymm', canonicalType: 'STRING', grouping: undefined })
    expect(formatted?.expression).toEqual({
      type: 'DATE_FORMAT', unit: 'MONTH', argument: { type: 'FIELD_REF', nodeId: 'orders', field: 'created_at' },
    })

    const legacy = structuredClone(graph)
    legacy.transforms![0].rules[0] = {
      ...legacy.transforms![0].rules[0], operation: 'DATE_TRUNC',
      output: { id: 'output_1', name: '下单时间日期处理结果', code: 'created_at_day', canonicalType: 'DATETIME' },
    }
    const migrated = hydrateDesignerGraph(emptyDSL(legacy), [dateNode], [], dateFields)
    expect(migrated.transforms?.[0].rules[0]).toMatchObject({
      operation: 'DATE_FORMAT', unit: 'MONTH',
      output: { name: '下单时间年月', code: 'created_at_yyyymm', canonicalType: 'STRING' },
    })
  })

  it('文本处理生成带受控参数的 Unicode 截取与替换表达式', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0', nodePositions: { customer: { x: 0, y: 0 } }, nodeNames: { customer: '客户数据节点' }, joins: [], groups: [],
      transforms: [{
        id: 'transform_text', name: '客户编码清理', family: 'TEXT', input: { kind: 'NODE', id: 'customer' }, position: { x: 300, y: 0 },
        rules: [
          { id: 'rule_slice', operation: 'SUBSTRING', inputKeys: ['customer.customer_id'], start: 2, length: 4, output: { id: 'customer_slice', name: '客户编码片段', code: 'customer_slice', canonicalType: 'STRING' } },
          { id: 'rule_replace', operation: 'REPLACE', inputKeys: ['customer.customer_id'], searchValue: '-', replacementValue: '', output: { id: 'customer_clean', name: '清理后编码', code: 'customer_clean', canonicalType: 'STRING' } },
        ],
      }],
      end: { id: 'end_1', name: '最终输出', input: { kind: 'TRANSFORM', id: 'transform_text' }, position: { x: 600, y: 0 }, outputs: [] },
    }

    const outputs = graphProducedFields(graph.end?.input, graph, nodes, fields)
    expect(outputs.find(item => item.key === 'transform_text.customer_slice')?.expression).toEqual({
      type: 'SUBSTRING', arguments: [
        { type: 'FIELD_REF', nodeId: 'customer', field: 'customer_id' },
        { type: 'LITERAL', value: 2 },
        { type: 'LITERAL', value: 4 },
      ],
    })
    expect(outputs.find(item => item.key === 'transform_text.customer_clean')?.expression).toEqual({
      type: 'REPLACE', arguments: [
        { type: 'FIELD_REF', nodeId: 'customer', field: 'customer_id' },
        { type: 'LITERAL', value: '-' },
        { type: 'LITERAL', value: '' },
      ],
    })
  })

  it('数值、条件、空值和字段合并生成类型安全且可执行的表达式', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0', nodePositions: { customer: { x: 0, y: 0 } }, nodeNames: { customer: '客户数据节点' }, joins: [], groups: [],
      transforms: [
        {
          id: 'transform_number', name: '金额取整', family: 'NUMBER', input: { kind: 'NODE', id: 'customer' }, position: { x: 300, y: 0 },
          rules: [{ id: 'rule_round', operation: 'ROUND', inputKeys: ['customer.amount'], precision: 1, output: { id: 'amount_round', name: '取整金额', code: 'amount_round', canonicalType: 'DECIMAL' } }],
        },
        {
          id: 'transform_condition', name: '金额分档', family: 'CONDITION', input: { kind: 'NODE', id: 'customer' }, position: { x: 300, y: 120 },
          rules: [
            { id: 'rule_gt', operation: 'CASE', inputKeys: ['customer.amount'], conditionOperator: 'GT', matchValue: '100', thenValue: '大额', elseValue: '普通', output: { id: 'amount_level', name: '金额档位', code: 'amount_level', canonicalType: 'STRING' } },
            { id: 'rule_contains', operation: 'CASE', inputKeys: ['customer.amount'], conditionOperator: 'CONTAINS', matchValue: '12', thenValue: '包含', elseValue: '不包含', output: { id: 'amount_contains', name: '金额包含判断', code: 'amount_contains', canonicalType: 'STRING' } },
          ],
        },
        {
          id: 'transform_null', name: '金额补空', family: 'NULL', input: { kind: 'NODE', id: 'customer' }, position: { x: 300, y: 240 },
          rules: [{ id: 'rule_fill', operation: 'COALESCE', inputKeys: ['customer.amount'], fallbackMode: 'LITERAL', fallbackValue: '0', output: { id: 'amount_filled', name: '补空金额', code: 'amount_filled', canonicalType: 'DECIMAL' } }],
        },
        {
          id: 'transform_merge', name: '客户金额编码', family: 'SPLIT_MERGE', input: { kind: 'NODE', id: 'customer' }, position: { x: 300, y: 360 },
          rules: [{ id: 'rule_merge', operation: 'CONCAT', inputKeys: ['customer.customer_id', 'customer.amount'], separator: '-', output: { id: 'customer_amount', name: '客户金额编码', code: 'customer_amount', canonicalType: 'STRING' } }],
        },
      ],
    }

    expect(graphProducedFields({ kind: 'TRANSFORM', id: 'transform_number' }, graph, nodes, fields).at(-1)?.expression).toEqual({
      type: 'ROUND', arguments: [{ type: 'FIELD_REF', nodeId: 'customer', field: 'amount' }, { type: 'LITERAL', value: 1 }],
    })
    const conditions = graphProducedFields({ kind: 'TRANSFORM', id: 'transform_condition' }, graph, nodes, fields)
    expect(conditions.find(field => field.code === 'amount_level')?.expression).toEqual({
      type: 'CASE',
      whens: [{ when: { type: 'GT', left: { type: 'FIELD_REF', nodeId: 'customer', field: 'amount' }, right: { type: 'LITERAL', value: 100 } }, then: { type: 'LITERAL', value: '大额' } }],
      else: { type: 'LITERAL', value: '普通' },
    })
    expect(conditions.find(field => field.code === 'amount_contains')?.expression).toMatchObject({
      whens: [{ when: { type: 'CONTAINS', left: { type: 'CAST', targetType: 'STRING' }, right: { type: 'LITERAL', value: '12' } } }],
    })
    expect(graphProducedFields({ kind: 'TRANSFORM', id: 'transform_null' }, graph, nodes, fields).at(-1)?.expression).toEqual({
      type: 'COALESCE', arguments: [{ type: 'FIELD_REF', nodeId: 'customer', field: 'amount' }, { type: 'LITERAL', value: 0 }],
    })
    expect(graphProducedFields({ kind: 'TRANSFORM', id: 'transform_merge' }, graph, nodes, fields).at(-1)?.expression).toEqual({
      type: 'CONCAT',
      arguments: [
        { type: 'COALESCE', arguments: [{ type: 'FIELD_REF', nodeId: 'customer', field: 'customer_id' }, { type: 'LITERAL', value: '' }] },
        { type: 'LITERAL', value: '-' },
        { type: 'COALESCE', arguments: [{ type: 'CAST', targetType: 'STRING', argument: { type: 'FIELD_REF', nodeId: 'customer', field: 'amount' } }, { type: 'LITERAL', value: '' }] },
      ],
    })
  })

  it('循环拓扑会被递归保护，不产生伪叶子、伪根或堆栈溢出', () => {
    const cyclic: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: {},
      nodeNames: {},
      joins: [],
      groups: [
        { id: 'group_a', name: '循环 A', input: { kind: 'GROUP', id: 'group_b' }, position: { x: 0, y: 0 }, dimensions: [], metrics: [] },
        { id: 'group_b', name: '循环 B', input: { kind: 'GROUP', id: 'group_a' }, position: { x: 0, y: 0 }, dimensions: [], metrics: [] },
      ],
      end: { id: 'end_1', name: '结束', input: { kind: 'GROUP', id: 'group_a' }, position: { x: 0, y: 0 }, outputs: [] },
    }

    expect(graphLeaves({ kind: 'GROUP', id: 'group_a' }, cyclic)).toEqual([])
    expect(graphContains({ kind: 'GROUP', id: 'group_a' }, { kind: 'NODE', id: 'missing' }, cyclic)).toBe(false)
    expect(graphProducedFields({ kind: 'GROUP', id: 'group_a' }, cyclic, [], [])).toEqual([])
    expect(graphRoot([], cyclic)).toBeUndefined()

    const arranged = layoutDesignerGraph(cyclic, [])
    for (const item of [...arranged.groups, arranged.end!]) {
      expect(Number.isFinite(item.position.x)).toBe(true)
      expect(Number.isFinite(item.position.y)).toBe(true)
    }
  })

  it('保存前拒绝缺失输入和引用已删除组件，并返回可直接展示的中文错误', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { customer: { x: 0, y: 0 } },
      nodeNames: { customer: '客户数据节点' },
      groups: [{
        id: 'group_customer',
        name: '客户汇总',
        position: { x: 300, y: 0 },
        dimensions: [],
        metrics: [],
      }],
      joins: [{
        id: 'join_customer',
        name: '客户关联',
        left: { kind: 'GROUP', id: 'group_customer' },
        right: { kind: 'NODE', id: 'deleted_order' },
        position: { x: 600, y: 0 },
        outputKeys: [],
      }],
      end: {
        id: 'end_1',
        name: '最终输出',
        input: { kind: 'JOIN', id: 'join_customer' },
        position: { x: 900, y: 0 },
        outputs: [],
      },
    }

    const result = validateDesignerGraph(graph, ['customer'])

    expect(result.valid).toBe(false)
    expect(result.issues.map(item => item.code)).toEqual(expect.arrayContaining(['MISSING_INPUT', 'INVALID_REFERENCE']))
    expect(result.errors).toEqual(expect.arrayContaining([
      '分组组件「客户汇总」尚未连接输入组件。',
      '关联组件「客户关联」的槽位 2 引用的数据节点「deleted_order」不存在或已被删除。',
    ]))
  })

  it('保存前拒绝组件自环、跨组件循环和全局重复 ID', () => {
    const selfLoop: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { customer: { x: 0, y: 0 } },
      nodeNames: { customer: '客户表' },
      groups: [{
        id: 'group_self',
        name: '自循环汇总',
        input: { kind: 'GROUP', id: 'group_self' },
        position: { x: 300, y: 0 },
        dimensions: [],
        metrics: [],
      }],
      joins: [],
      end: { id: 'end_1', name: '最终输出', input: { kind: 'GROUP', id: 'group_self' }, position: { x: 600, y: 0 }, outputs: [] },
    }
    const selfResult = validateDesignerGraph(selfLoop, ['customer'])
    expect(selfResult.valid).toBe(false)
    expect(selfResult.issues.map(item => item.code)).toEqual(expect.arrayContaining(['SELF_LOOP', 'CYCLE']))
    expect(selfResult.errors).toContain('不能将分组组件「自循环汇总」连接到自身。')

    const cyclic: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { duplicated: { x: 0, y: 0 } },
      nodeNames: { duplicated: '重复 ID 数据节点' },
      groups: [{
        id: 'duplicated',
        name: '循环汇总',
        input: { kind: 'JOIN', id: 'join_cycle' },
        position: { x: 300, y: 0 },
        dimensions: [],
        metrics: [],
      }],
      joins: [{
        id: 'join_cycle',
        name: '循环关联',
        left: { kind: 'GROUP', id: 'duplicated' },
        right: { kind: 'NODE', id: 'duplicated' },
        position: { x: 600, y: 0 },
        outputKeys: [],
      }],
      end: { id: 'end_1', name: '最终输出', input: { kind: 'JOIN', id: 'join_cycle' }, position: { x: 900, y: 0 }, outputs: [] },
    }
    const cycleResult = validateDesignerGraph(cyclic, ['duplicated'])
    expect(cycleResult.valid).toBe(false)
    expect(cycleResult.issues.map(item => item.code)).toEqual(expect.arrayContaining(['DUPLICATE_COMPONENT_ID', 'CYCLE']))
    expect(cycleResult.errors.some(message => message.includes('画布存在循环依赖'))).toBe(true)
  })

  it('连线阶段可预判新增边是否形成环，并校验失效端点', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { customer: { x: 0, y: 0 } },
      nodeNames: { customer: '客户数据节点' },
      groups: [
        {
          id: 'group_a',
          name: '一级汇总',
          input: { kind: 'NODE', id: 'customer' },
          position: { x: 300, y: 0 },
          dimensions: [],
          metrics: [],
        },
        {
          id: 'group_b',
          name: '二级汇总',
          input: { kind: 'GROUP', id: 'group_a' },
          position: { x: 600, y: 0 },
          dimensions: [],
          metrics: [],
        },
      ],
      joins: [],
      end: { id: 'end_1', name: '最终输出', input: { kind: 'GROUP', id: 'group_b' }, position: { x: 900, y: 0 }, outputs: [] },
    }

    expect(wouldCreateGraphCycle({ kind: 'GROUP', id: 'group_b' }, { kind: 'GROUP', id: 'group_a' }, graph)).toBe(true)
    expect(wouldCreateGraphCycle({ kind: 'GROUP', id: 'group_a' }, { kind: 'GROUP', id: 'group_a' }, graph)).toBe(true)
    expect(wouldCreateGraphCycle({ kind: 'NODE', id: 'customer' }, { kind: 'GROUP', id: 'group_a' }, graph)).toBe(false)
    expect(wouldCreateGraphCycle({ kind: 'GROUP', id: 'group_b' }, { kind: 'OUTPUT', id: 'end_1' }, graph)).toBe(false)

    expect(graphConnectionError({ kind: 'GROUP', id: 'group_b' }, { kind: 'GROUP', id: 'group_a' }, graph, ['customer']))
      .toBe('连接分组组件「二级汇总」与分组组件「一级汇总」会形成循环依赖，请调整连线。')
    expect(graphConnectionError({ kind: 'NODE', id: 'customer' }, { kind: 'GROUP', id: 'group_a' }, graph, ['customer'])).toBeUndefined()
    expect(graphConnectionError({ kind: 'NODE', id: 'deleted' }, { kind: 'GROUP', id: 'group_a' }, graph, ['customer']))
      .toBe('数据节点「deleted」不存在或已被删除，请重新连线。')
  })

  it('完整且无环的画布通过 DAG 校验', () => {
    const graph: DesignerGraphV1 = {
      version: '1.0',
      nodePositions: { customer: { x: 0, y: 0 }, region: { x: 0, y: 150 } },
      nodeNames: { customer: '客户表', region: '区域表' },
      groups: [{
        id: 'group_customer',
        name: '客户汇总',
        input: { kind: 'NODE', id: 'customer' },
        position: { x: 300, y: 0 },
        dimensions: [],
        metrics: [],
      }],
      joins: [{
        id: 'join_customer_region',
        name: '客户区域关联',
        left: { kind: 'GROUP', id: 'group_customer' },
        right: { kind: 'NODE', id: 'region' },
        position: { x: 600, y: 75 },
        outputKeys: [],
      }],
      end: { id: 'end_1', name: '最终输出', input: { kind: 'JOIN', id: 'join_customer_region' }, position: { x: 900, y: 75 }, outputs: [] },
    }

    expect(validateDesignerGraph(graph, ['customer', 'region'])).toEqual({ valid: true, issues: [], errors: [] })
  })
})
