import { afterEach, expect, test, vi } from 'vitest'
import { hydrateDatasetDraft } from './dataset-draft'
import { datasetAPI, type AssetTable, type DatasetRecord } from './datasets'

afterEach(() => vi.restoreAllMocks())

test('全局分组还原为独立分组组件状态而不是表节点聚合模式', async () => {
  const tables: AssetTable[] = [
    { id: 'table-a', dataSourceId: 'source-a', dataSourceName: 'A', dataSourceType: 'MYSQL', tableName: 'customers', schemaName: 's', businessName: '客户', columnCount: 1 },
    { id: 'table-b', dataSourceId: 'source-b', dataSourceName: 'B', dataSourceType: 'ORACLE', tableName: 'orders', schemaName: 's', businessName: '订单', columnCount: 2 },
  ]
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === 'table-a'
    ? [{ id: 'a-id', tableId: 'table-a', columnName: 'customer_id', businessName: '客户ID', canonicalType: 'NUMBER', nullable: false, semanticType: 'IDENTIFIER' }]
    : [
      { id: 'b-id', tableId: 'table-b', columnName: 'customer_id', businessName: '客户ID', canonicalType: 'NUMBER', nullable: false, semanticType: 'IDENTIFIER' },
      { id: 'b-amount', tableId: 'table-b', columnName: 'amount', businessName: '金额', canonicalType: 'NUMBER', nullable: false, semanticType: 'QUANTITY' },
    ] }))
  const record = {
    id: 'dataset-1', code: 'customer_orders', name: '客户订单', description: '测试', type: 'CROSS_SOURCE', status: 'DRAFT', version: 1,
    draftVersionId: 'draft-1', draftVersionNo: 1, draftRecordVersion: 1, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {}, createdAt: '', updatedAt: '',
    dsl: {
      dslVersion: '1.0', dataset: { code: 'customer_orders', name: '客户订单', description: '测试', type: 'CROSS_SOURCE' },
      nodes: [
        { id: 'a', tableId: 'table-a', alias: 'a' },
        { id: 'b', tableId: 'table-b', alias: 'b' },
      ],
      joins: [],
      fields: [
        { id: 'field_a_id', code: 'a_id', name: '客户ID', role: 'DIMENSION', canonicalType: 'INTEGER', expression: { type: 'FIELD_REF', nodeId: 'a', field: 'customer_id' } },
        { id: 'field_b_id', code: 'b_id', name: '客户ID', role: 'DIMENSION', canonicalType: 'INTEGER', expression: { type: 'FIELD_REF', nodeId: 'b', field: 'customer_id' } },
        { id: 'field_b_amount', code: 'b_amount', name: '金额', role: 'MEASURE', canonicalType: 'DECIMAL', expression: { type: 'AGGREGATE', function: 'SUM', argument: { type: 'FIELD_REF', nodeId: 'b', field: 'amount' } } },
      ],
      groupBy: ['field_a_id', 'field_b_id'], filters: [], parameters: [], sorts: [], outputGrain: { description: '每行一个客户订单', keyFields: ['a_id'] },
    },
  } as unknown as DatasetRecord

  const draft = await hydrateDatasetDraft(record, tables)
  expect(draft.nodes.map(node => node.groupingEnabled)).toEqual([false, false])
  expect(draft.finalConfigured).toBe(true)
  expect(draft.finalGroupingEnabled).toBe(true)
  expect(draft.fields.find(field => field.key === 'a.customer_id')?.finalGroupBy).toBe(true)
  expect(draft.fields.find(field => field.key === 'b.amount')).toMatchObject({ finalMetric: true, finalAggregation: 'SUM' })
})

test('重新打开时严格按节点 projection 还原输出字段', async () => {
  const table: AssetTable = { id: 'table-a', dataSourceId: 'source-a', dataSourceName: 'A', dataSourceType: 'MYSQL', tableName: 'customers', schemaName: 's', businessName: '客户', columnCount: 2 }
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: [
    { id: 'a-id', tableId: 'table-a', columnName: 'customer_id', businessName: '客户ID', canonicalType: 'NUMBER', nullable: false, semanticType: 'IDENTIFIER' },
    { id: 'a-name', tableId: 'table-a', columnName: 'customer_name', businessName: '客户名称', canonicalType: 'TEXT', nullable: false, semanticType: 'ATTRIBUTE' },
  ] })
  const record = {
    id: 'dataset-1', code: 'customers', name: '客户', description: '测试', type: 'SINGLE_SOURCE', status: 'DRAFT', version: 2,
    draftVersionId: 'draft-1', draftVersionNo: 1, draftRecordVersion: 2, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {}, createdAt: '', updatedAt: '',
    dsl: {
      dslVersion: '1.0', dataset: { code: 'customers', name: '客户', description: '测试', type: 'SINGLE_SOURCE' },
      nodes: [{ id: 'customers', tableId: 'table-a', alias: 'c', projection: ['customer_id'] }],
      joins: [],
      fields: [{ id: 'field_customer_id', code: 'customer_id', name: '客户ID', role: 'IDENTIFIER', canonicalType: 'INTEGER', expression: { type: 'FIELD_REF', nodeId: 'customers', field: 'customer_id' } }],
      groupBy: [], filters: [], parameters: [], sorts: [], outputGrain: { description: '每行一个客户', keyFields: ['customer_id'] },
    },
  } as unknown as DatasetRecord

  const draft = await hydrateDatasetDraft(record, [table])

  expect(draft.nodes[0].selected).toEqual(['customer_id'])
  expect(draft.fields.find(field => field.key === 'customers.customer_id')?.output).toBe(true)
  expect(draft.fields.find(field => field.key === 'customers.customer_name')?.output).toBe(false)
})

test('重新打开时还原数据节点先分组再进入关联槽位的拓扑元数据', async () => {
  const tables: AssetTable[] = [
    { id: 'table-a', dataSourceId: 'source-a', dataSourceName: 'A', dataSourceType: 'MYSQL', tableName: 'customers', schemaName: 's', businessName: '客户', columnCount: 2 },
    { id: 'table-b', dataSourceId: 'source-b', dataSourceName: 'B', dataSourceType: 'ORACLE', tableName: 'orders', schemaName: 's', businessName: '订单', columnCount: 2 },
  ]
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === 'table-a' ? [
    { id: 'a-id', tableId: 'table-a', columnName: 'customer_id', businessName: '客户ID', canonicalType: 'NUMBER', nullable: false, semanticType: 'IDENTIFIER' },
    { id: 'a-name', tableId: 'table-a', columnName: 'customer_name', businessName: '客户名称', canonicalType: 'TEXT', nullable: false, semanticType: 'ATTRIBUTE' },
  ] : [
    { id: 'b-id', tableId: 'table-b', columnName: 'customer_id', businessName: '客户ID', canonicalType: 'NUMBER', nullable: false, semanticType: 'IDENTIFIER' },
    { id: 'b-amount', tableId: 'table-b', columnName: 'amount', businessName: '金额', canonicalType: 'NUMBER', nullable: false, semanticType: 'QUANTITY' },
  ] }))
  const record = {
    id: 'dataset-1', code: 'customer_orders', name: '客户订单', description: '测试', type: 'CROSS_SOURCE', status: 'DRAFT', version: 3,
    draftVersionId: 'draft-1', draftVersionNo: 1, draftRecordVersion: 3, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {}, createdAt: '', updatedAt: '',
    dsl: {
      dslVersion: '1.0', dataset: { code: 'customer_orders', name: '客户订单', type: 'CROSS_SOURCE' },
      nodes: [
        { id: 'a', tableId: 'table-a', alias: 'a', projection: ['customer_id', 'customer_name'] },
        { id: 'b', tableId: 'table-b', alias: 'b', projection: ['customer_id', 'amount'] },
      ],
      joins: [{ id: 'join_1', leftNodeId: 'a', rightNodeId: 'b', joinType: 'LEFT', cardinality: 'UNKNOWN', manualConfirmed: true, conditions: [{ leftExpression: { type: 'FIELD_REF', nodeId: 'a', field: 'customer_id' }, operator: 'EQUALS', rightExpression: { type: 'FIELD_REF', nodeId: 'b', field: 'customer_id' } }] }],
      preAggregations: [
        { id: 'group_1', nodeId: 'a', joinId: 'join_1', joinSide: 'LEFT', groupBy: [{ field: 'customer_id' }], metrics: [{ field: 'customer_name', function: 'COUNT_DISTINCT' }] },
        { id: 'group_2', nodeId: 'b', joinId: 'join_1', joinSide: 'RIGHT', groupBy: [{ field: 'customer_id' }], metrics: [{ field: 'amount', function: 'SUM' }] },
      ],
      fields: [
        { id: 'field_a_id', code: 'a_customer_id', name: '客户ID', role: 'DIMENSION', canonicalType: 'INTEGER', expression: { type: 'FIELD_REF', nodeId: 'a', field: 'customer_id' } },
        { id: 'field_a_name', code: 'a_customer_name', name: '客户数', role: 'MEASURE', canonicalType: 'INTEGER', expression: { type: 'FIELD_REF', nodeId: 'a', field: 'customer_name' } },
        { id: 'field_b_id', code: 'b_customer_id', name: '订单客户ID', role: 'IDENTIFIER', canonicalType: 'INTEGER', expression: { type: 'FIELD_REF', nodeId: 'b', field: 'customer_id' } },
        { id: 'field_b_amount', code: 'b_amount', name: '订单金额', role: 'MEASURE', canonicalType: 'DECIMAL', expression: { type: 'FIELD_REF', nodeId: 'b', field: 'amount' } },
      ],
      groupBy: [], filters: [], parameters: [], sorts: [], outputGrain: { description: '每行一个客户', keyFields: ['a_customer_id'] },
      designer: {
        version: '1.0',
        nodePositions: { a: { x: 24, y: 36 }, b: { x: 24, y: 236 } },
        nodeNames: { a: '客户原表', b: '订单原表' },
        groups: [
          { id: 'group_1', name: '客户分组结果', input: { kind: 'NODE', id: 'a' }, position: { x: 324, y: 36 }, dimensions: [{ key: 'a.customer_id', name: '客户ID', code: 'customer_id' }], metrics: [{ key: 'a.customer_name', name: '客户数', code: 'customer_count', aggregation: 'COUNT_DISTINCT' }] },
          { id: 'group_2', name: '订单分组结果', input: { kind: 'NODE', id: 'b' }, position: { x: 324, y: 236 }, dimensions: [{ key: 'b.customer_id', name: '客户ID', code: 'customer_id' }], metrics: [{ key: 'b.amount', name: '订单金额', code: 'order_amount', aggregation: 'SUM' }] },
        ],
        joins: [{ id: 'join_1', name: '客户订单关联结果', left: { kind: 'GROUP', id: 'group_1' }, right: { kind: 'GROUP', id: 'group_2' }, position: { x: 624, y: 136 }, outputKeys: ['a.customer_id', 'a.customer_name', 'b.amount'] }],
        end: { id: 'end_1', name: '客户订单输出', input: { kind: 'JOIN', id: 'join_1' }, position: { x: 924, y: 136 }, outputs: [{ key: 'a.customer_id', name: '客户ID', code: 'customer_id' }, { key: 'b.amount', name: '订单金额', code: 'order_amount' }] },
      },
    },
  } as unknown as DatasetRecord

  const draft = await hydrateDatasetDraft(record, tables)

  expect(draft.preAggregation).toEqual({ id: 'group_1', nodeId: 'a', joinId: 'join_1', joinSide: 'LEFT' })
  expect(draft.preAggregations).toEqual([
    { id: 'group_1', nodeId: 'a', joinId: 'join_1', joinSide: 'LEFT' },
    { id: 'group_2', nodeId: 'b', joinId: 'join_1', joinSide: 'RIGHT' },
  ])
  expect(draft.finalOutputKeys).toEqual(['a.customer_id', 'a.customer_name', 'b.customer_id', 'b.amount'])
  expect(draft.fields.find(field => field.key === 'a.customer_id')).toMatchObject({ finalGroupBy: true, finalGrouping: '' })
  expect(draft.fields.find(field => field.key === 'a.customer_name')).toMatchObject({ finalMetric: true, finalAggregation: 'COUNT_DISTINCT' })
  expect(draft.fields.find(field => field.key === 'b.amount')).toMatchObject({ finalMetric: true, finalAggregation: 'SUM' })
  expect(draft.designer?.nodePositions.a).toEqual({ x: 24, y: 36 })
  expect(draft.designer?.groups.map(group => group.name)).toEqual(['客户分组结果', '订单分组结果'])
  expect(draft.designer?.end?.outputs).toEqual([
    { key: 'a.customer_id', name: '客户ID', code: 'customer_id' },
    { key: 'b.amount', name: '订单金额', code: 'order_amount' },
  ])
})
