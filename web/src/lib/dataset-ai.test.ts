import { describe, expect, it } from 'vitest'
import { datasetAIPlanFromEditor, datasetAIRequestContext, materializeDatasetAIPlan, type DatasetAIGraphPlan } from './dataset-ai'
import { buildDatasetDSL, type AssetColumn, type AssetTable, type DatasetDraft } from './datasets'

const orders: AssetTable = { id: 'orders-table', dataSourceId: 'source-1', dataSourceName: '销售库', dataSourceType: 'MYSQL', tableName: 'orders', schemaName: 'sales', businessName: '订单表', columnCount: 3 }
const customers: AssetTable = { id: 'customers-table', dataSourceId: 'source-1', dataSourceName: '销售库', dataSourceType: 'MYSQL', tableName: 'customers', schemaName: 'sales', businessName: '客户表', columnCount: 3 }
const columns: Record<string, AssetColumn[]> = {
  [orders.id]: [
    { id: 'o1', tableId: orders.id, columnName: 'customer_id', businessName: '客户编号', canonicalType: 'STRING', nullable: false, semanticType: 'IDENTIFIER' },
    { id: 'o2', tableId: orders.id, columnName: 'amount', businessName: '订单金额', canonicalType: 'DECIMAL', nullable: false, semanticType: 'AMOUNT' },
    { id: 'o3', tableId: orders.id, columnName: 'removed', businessName: '已删除字段', canonicalType: 'STRING', nullable: true, semanticType: 'TEXT', assetStatus: 'INACTIVE' },
  ],
  [customers.id]: [
    { id: 'c1', tableId: customers.id, columnName: 'customer_id', businessName: '客户编号', canonicalType: 'STRING', nullable: false, semanticType: 'IDENTIFIER' },
    { id: 'c2', tableId: customers.id, columnName: 'region', businessName: '客户地区', canonicalType: 'STRING', nullable: true, semanticType: 'REGION' },
    { id: 'c3', tableId: customers.id, columnName: 'customer_name', businessName: '客户名称', canonicalType: 'STRING', nullable: true, semanticType: 'ATTRIBUTE' },
  ],
}
const empty: DatasetDraft = { code: '', name: '', description: '', nodes: [], fields: [], joins: [], filters: [], parameters: [], calculations: [], sorts: [], grainDescription: '', grainKeys: [] }

const plan = (): DatasetAIGraphPlan => ({
  dataset: { name: '地区订单汇总', description: '按客户地区汇总订单金额' },
  nodes: [
    { id: 'node_1', tableId: customers.id, alias: 'customers', selectedColumns: ['customer_id', 'region'] },
    { id: 'node_2', tableId: orders.id, alias: 'orders', selectedColumns: ['customer_id', 'amount'] },
  ],
  joins: [{
    id: 'join_1', name: '客户订单关联', left: { kind: 'NODE', id: 'node_1' }, right: { kind: 'NODE', id: 'node_2' }, joinType: 'LEFT',
    conditions: [{ leftNodeId: 'node_1', leftColumn: 'customer_id', rightNodeId: 'node_2', rightColumn: 'customer_id' }],
  }],
  groups: [{
    id: 'group_1', name: '地区汇总', input: { kind: 'JOIN', id: 'join_1' },
    dimensions: [{ nodeId: 'node_1', column: 'region', grouping: '' }],
    metrics: [{ nodeId: 'node_2', column: 'amount', aggregation: 'SUM' }],
  }],
  end: { name: '最终输出', input: { kind: 'GROUP', id: 'group_1' }, outputs: [
    { nodeId: 'node_1', column: 'region', name: '客户地区', code: 'customer_region' },
    { nodeId: 'node_2', column: 'amount', name: '订单金额', code: 'order_amount' },
  ] },
})

const planWithGroupsBeforeAndAfterJoin = (): DatasetAIGraphPlan => {
  const result = plan()
  result.joins[0] = { ...result.joins[0], right: { kind: 'GROUP', id: 'group_1' } }
  result.groups = [
    {
      id: 'group_1', name: '关联前订单汇总', input: { kind: 'NODE', id: 'node_2' },
      dimensions: [{ nodeId: 'node_2', column: 'customer_id', grouping: '' }],
      metrics: [{ nodeId: 'node_2', column: 'amount', aggregation: 'SUM' }],
    },
    {
      id: 'group_2', name: '关联后地区汇总', input: { kind: 'JOIN', id: 'join_1' },
      dimensions: [{ nodeId: 'node_1', column: 'region', grouping: '' }],
      metrics: [{ nodeId: 'node_2', column: 'amount', aggregation: 'SUM' }],
    },
  ]
  result.end.input = { kind: 'GROUP', id: 'group_2' }
  return result
}

describe('dataset AI editor conversion', () => {
  it('omits current context for the first request on a blank new canvas', () => {
    expect(datasetAIPlanFromEditor(empty, { version: '1.0', nodePositions: {}, nodeNames: {}, joins: [], groups: [], transforms: [] }, { name: '', description: '' })).toBeUndefined()
  })

  it('always prefers the live canvas over a staged AI proposal', () => {
    const live = plan()
    const staged = plan()
    staged.dataset.name = '尚未应用的候选'
    expect(datasetAIRequestContext(live, staged, { forceLiveCanvas: false, stagedProposalApplied: false })).toBe(live)
    expect(datasetAIRequestContext(undefined, staged, { forceLiveCanvas: false, stagedProposalApplied: false })).toBe(staged)
    expect(datasetAIRequestContext(undefined, staged, { forceLiveCanvas: true, stagedProposalApplied: false })).toBeUndefined()
  })

  it('does not materialize date conversion metadata inside a group dimension', async () => {
    const candidate = plan()
    candidate.groups[0].dimensions[0].grouping = 'MONTH'
    const result = await materializeDatasetAIPlan(candidate, [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_group')
    expect(result.graph.groups[0].dimensions[0]).toEqual(expect.objectContaining({ key: 'node_1.region' }))
    expect(result.graph.groups[0].dimensions[0]).not.toHaveProperty('grouping')
  })

  it('materializes and round-trips AI generated fine-grained transforms', async () => {
    const transformPlan: DatasetAIGraphPlan = {
      dataset: { name: '客户区域映射', description: '使用候选值数组映射客户区域' },
      nodes: [{ id: 'node_1', tableId: customers.id, alias: 'customers', selectedColumns: ['customer_id', 'region'] }],
      joins: [], groups: [],
      transforms: [{
        id: 'transform_1', name: '区域条件映射', family: 'CONDITION', componentType: 'CONDITION', input: { kind: 'NODE', id: 'node_1' },
        rules: [{
          id: 'rule_1', operation: 'CASE', inputKeys: ['node_1.region'], conditionOperator: 'IN', thenValue: '目标区域', elseValue: '其他区域',
          conditionValues: [{ id: 'value_1', mode: 'LITERAL', value: '华东' }, { id: 'value_2', mode: 'FIELD', value: 'node_1.customer_id' }],
          output: { id: 'region_group', name: '区域分组', code: 'region_group', canonicalType: 'STRING' },
        }],
      }],
      end: { name: '最终输出', input: { kind: 'TRANSFORM', id: 'transform_1' }, outputs: [{ nodeId: 'node_1', column: 'region', key: 'transform_1.region_group', name: '区域分组', code: 'region_group' }] },
    }
    const result = await materializeDatasetAIPlan(transformPlan, [customers], async tableID => columns[tableID], empty, 'dataset_ai_transform')
    expect(result.graph.transforms).toHaveLength(1)
    expect(result.graph.end?.input).toEqual({ kind: 'TRANSFORM', id: 'transform_1' })
    expect(buildDatasetDSL(result.draft).fields[0]).toMatchObject({
      code: 'region_group',
      expression: { type: 'CASE', whens: [{ when: { type: 'IN', right: { type: 'ARRAY' } } }] },
    })
    expect(datasetAIPlanFromEditor(result.draft, result.graph, result.metadata)).toMatchObject({
      transforms: [{ id: 'transform_1', componentType: 'CONDITION' }],
      end: { input: { kind: 'TRANSFORM', id: 'transform_1' }, outputs: [{ key: 'transform_1.region_group' }] },
    })
  })

  it('materializes the complete transform-group-join-group-transform workflow', async () => {
    const workflowPlan: DatasetAIGraphPlan = {
      dataset: { name: '地区订单完整加工', description: '关联前加工与汇总，关联后再次汇总并加工输出' },
      nodes: [
        { id: 'node_1', tableId: customers.id, alias: 'customers', selectedColumns: ['customer_id', 'region', 'customer_name'] },
        { id: 'node_2', tableId: orders.id, alias: 'orders', selectedColumns: ['customer_id', 'amount'] },
      ],
      transforms: [
        {
          id: 'transform_1', name: '地区转大写', family: 'TEXT', componentType: 'TEXT_UPPER', input: { kind: 'NODE', id: 'node_1' },
          rules: [{ id: 'rule_1', operation: 'UPPER', inputKeys: ['node_1.region'], output: { id: 'region_upper', name: '大写地区', code: 'region_upper', canonicalType: 'STRING' } }],
        },
        {
          id: 'transform_2', name: '地区转小写', family: 'TEXT', componentType: 'TEXT_LOWER', input: { kind: 'GROUP', id: 'group_3' },
          rules: [{ id: 'rule_2', operation: 'LOWER', inputKeys: ['transform_1.region_upper'], output: { id: 'region_lower', name: '小写地区', code: 'region_lower', canonicalType: 'STRING' } }],
        },
      ],
      groups: [
        {
          id: 'group_1', name: '客户预汇总', input: { kind: 'TRANSFORM', id: 'transform_1' },
          dimensions: [
            { nodeId: 'node_1', column: 'customer_id', grouping: '' },
            { nodeId: 'transform_1', column: 'region_upper', grouping: '' },
          ],
          metrics: [{ nodeId: 'node_1', column: 'customer_name', aggregation: 'COUNT' }],
        },
        {
          id: 'group_2', name: '订单预汇总', input: { kind: 'NODE', id: 'node_2' },
          dimensions: [{ nodeId: 'node_2', column: 'customer_id', grouping: '' }],
          metrics: [{ nodeId: 'node_2', column: 'amount', aggregation: 'SUM' }],
        },
        {
          id: 'group_3', name: '地区订单汇总', input: { kind: 'JOIN', id: 'join_1' },
          dimensions: [{ nodeId: 'transform_1', column: 'region_upper', grouping: '' }],
          metrics: [{ nodeId: 'node_2', column: 'amount', aggregation: 'SUM' }],
        },
      ],
      joins: [{
        id: 'join_1', name: '客户订单关联', left: { kind: 'GROUP', id: 'group_1' }, right: { kind: 'GROUP', id: 'group_2' }, joinType: 'LEFT',
        conditions: [{ leftNodeId: 'node_1', leftColumn: 'customer_id', rightNodeId: 'node_2', rightColumn: 'customer_id' }],
      }],
      end: {
        name: '最终输出', input: { kind: 'TRANSFORM', id: 'transform_2' }, outputs: [
          { nodeId: 'node_1', column: 'region', key: 'transform_2.region_lower', name: '小写地区', code: 'region_lower' },
          { nodeId: 'node_2', column: 'amount', key: 'node_2.amount', name: '订单金额', code: 'order_amount' },
        ],
      },
    }

    const result = await materializeDatasetAIPlan(workflowPlan, [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_workflow')
    expect(result.graph.groups.find(group => group.id === 'group_1')?.dimensions.map(item => item.key)).toContain('transform_1.region_upper')
    expect(result.graph.end?.input).toEqual({ kind: 'TRANSFORM', id: 'transform_2' })

    const dsl = buildDatasetDSL(result.draft)
    expect(dsl.preAggregations).toEqual(expect.arrayContaining([
      expect.objectContaining({
        id: 'group_1', nodeId: 'node_1',
        groupBy: expect.arrayContaining([expect.objectContaining({ field: 'region_upper', expression: expect.objectContaining({ type: 'UPPER' }) })]),
      }),
      expect.objectContaining({ id: 'group_2', nodeId: 'node_2' }),
    ]))
    expect(dsl.fields).toEqual(expect.arrayContaining([
      expect.objectContaining({ code: 'region_lower', expression: expect.objectContaining({ type: 'LOWER', argument: expect.objectContaining({ type: 'FIELD_REF', nodeId: 'node_1', field: 'region_upper' }) }) }),
      expect.objectContaining({ code: 'order_amount', expression: expect.objectContaining({ type: 'AGGREGATE', function: 'SUM' }) }),
    ]))
    expect(datasetAIPlanFromEditor(result.draft, result.graph, result.metadata)).toMatchObject({
      groups: expect.arrayContaining([expect.objectContaining({ id: 'group_1', dimensions: expect.arrayContaining([expect.objectContaining({ nodeId: 'transform_1', column: 'region_upper' })]) })]),
      end: { input: { kind: 'TRANSFORM', id: 'transform_2' }, outputs: [{ key: 'transform_2.region_lower' }, { key: 'node_2.amount' }] },
    })
  })

  it('materializes a complete proposal and still produces the existing strict DSL', async () => {
    const result = await materializeDatasetAIPlan(plan(), [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1')
    expect(result.draft.nodes).toHaveLength(2)
    expect(result.draft.joins[0]).toMatchObject({ joinType: 'LEFT', manualConfirmed: true, leftField: 'customer_id', rightField: 'customer_id' })
    expect(result.graph.groups[0].metrics[0]).toMatchObject({ key: 'node_2.amount', aggregation: 'SUM' })
    expect(result.graph.end?.outputs.map(output => output.code)).toEqual(['customer_region', 'order_amount'])

    const dsl = buildDatasetDSL(result.draft)
    expect(dsl.nodes).toHaveLength(2)
    expect(dsl.joins).toHaveLength(1)
    expect(dsl.groupBy).toEqual(['field_customers_region'])
    expect((dsl.fields as Array<Record<string, unknown>>).find(field => field.code === 'order_amount')).toMatchObject({ role: 'MEASURE', expression: { type: 'AGGREGATE', function: 'SUM' } })
  })

  it('serializes the live graph as modification context without SQL', async () => {
    const result = await materializeDatasetAIPlan(plan(), [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1')
    const current = datasetAIPlanFromEditor(result.draft, result.graph, result.metadata)
    expect(current).toMatchObject({
      dataset: { name: '地区订单汇总' },
      nodes: [{ id: 'node_1' }, { id: 'node_2' }],
      joins: [{ id: 'join_1', joinType: 'LEFT' }],
      groups: [{ id: 'group_1' }],
      end: { input: { kind: 'GROUP', id: 'group_1' } },
    })
    expect(JSON.stringify(current)).not.toMatch(/\bSELECT\b|\bJOIN\s+(?:`|"|\[)/i)
  })

  it('preserves untouched editor identity, output selections, layout and grain during modification', async () => {
    const initial = await materializeDatasetAIPlan(plan(), [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1')
    const baseDraft: DatasetDraft = {
      ...initial.draft,
      fields: initial.draft.fields.map(field => field.key === 'node_1.region' ? { ...field, name: '自定义地区名称', code: 'custom_region' } : field),
      grainDescription: '每行代表一个客户地区',
      grainKeys: ['customer_region'],
    }
    const baseGraph = {
      ...initial.graph,
      nodePositions: { ...initial.graph.nodePositions, node_1: { x: 111, y: 222 } },
      joins: initial.graph.joins.map(join => ({ ...join, position: { x: 333, y: 222 }, outputKeys: ['node_1.region', 'node_2.amount'] })),
      groups: initial.graph.groups.map(group => ({
        ...group,
        position: { x: 555, y: 222 },
        dimensions: group.dimensions.map(dimension => ({ ...dimension, name: '保留的地区维度', code: 'kept_region' })),
        metrics: group.metrics.map(metric => ({ ...metric, name: '保留的金额指标', code: 'kept_amount' })),
      })),
      end: initial.graph.end ? { ...initial.graph.end, position: { x: 777, y: 222 } } : undefined,
    }
    const modified = plan()
    modified.dataset.description = '只修改数据集说明'

    const result = await materializeDatasetAIPlan(modified, [orders, customers], async tableID => columns[tableID], baseDraft, 'dataset_ai_1', baseGraph)

    expect(result.draft.fields.find(field => field.key === 'node_1.region')).toMatchObject({ name: '自定义地区名称', code: 'custom_region' })
    expect(result.draft.grainDescription).toBe('每行代表一个客户地区')
    expect(result.draft.grainKeys).toEqual(['customer_region'])
    expect(result.graph.nodePositions.node_1).toEqual({ x: 111, y: 222 })
    expect(result.graph.joins[0]).toMatchObject({ position: { x: 333, y: 222 }, outputKeys: ['node_1.region', 'node_2.amount'] })
    expect(result.graph.groups[0]).toMatchObject({
      position: { x: 555, y: 222 },
      dimensions: [{ name: '保留的地区维度', code: 'kept_region' }],
      metrics: [{ name: '保留的金额指标', code: 'kept_amount' }],
    })
    expect(result.graph.end?.position).toEqual({ x: 777, y: 222 })
  })

  it('只移除关联后分组时保留关联前分组的身份、连线和布局', async () => {
    const initialPlan = planWithGroupsBeforeAndAfterJoin()
    const initial = await materializeDatasetAIPlan(initialPlan, [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1')
    const baseGraph = {
      ...initial.graph,
      groups: initial.graph.groups.map(group => group.id === 'group_1'
        ? {
            ...group,
            position: { x: 444, y: 188 },
            dimensions: group.dimensions.map(item => ({ ...item, name: '保留的客户维度', code: 'kept_customer' })),
            metrics: group.metrics.map(item => ({ ...item, name: '保留的金额指标', code: 'kept_amount' })),
          }
        : group),
    }
    const modified = planWithGroupsBeforeAndAfterJoin()
    modified.groups = modified.groups.filter(group => group.id !== 'group_2')
    modified.end.input = { kind: 'JOIN', id: 'join_1' }

    const result = await materializeDatasetAIPlan(modified, [orders, customers], async tableID => columns[tableID], initial.draft, 'dataset_ai_1', baseGraph)

    expect(result.graph.groups).toHaveLength(1)
    expect(result.graph.groups[0]).toMatchObject({
      id: 'group_1',
      input: { kind: 'NODE', id: 'node_2' },
      position: { x: 444, y: 188 },
      dimensions: [{ name: '保留的客户维度', code: 'kept_customer' }],
      metrics: [{ name: '保留的金额指标', code: 'kept_amount' }],
    })
    expect(result.graph.joins[0].right).toEqual({ kind: 'GROUP', id: 'group_1' })
    expect(result.graph.end?.input).toEqual({ kind: 'JOIN', id: 'join_1' })

    const current = datasetAIPlanFromEditor(result.draft, result.graph, result.metadata)
    expect(current?.groups.map(group => group.id)).toEqual(['group_1'])
    expect(current?.joins[0].right).toEqual({ kind: 'GROUP', id: 'group_1' })
    expect(buildDatasetDSL(result.draft).preAggregations).toEqual([{
      id: 'group_1', nodeId: 'node_2', joinId: 'join_1', joinSide: 'RIGHT',
      groupBy: [{ field: 'customer_id' }], metrics: [{ field: 'amount', function: 'SUM' }],
    }])
  })

  it('修改时将新增字段补入旧 Join 输出白名单并完整生成分组与输出 DSL', async () => {
    const initialPlan = planWithGroupsBeforeAndAfterJoin()
    initialPlan.nodes[0].selectedColumns = ['customer_id']
    initialPlan.groups[1].dimensions = [{ nodeId: 'node_1', column: 'customer_id', grouping: '' }]
    initialPlan.end.outputs = [
      { nodeId: 'node_1', column: 'customer_id', name: '客户编号', code: 'customer_id' },
      { nodeId: 'node_2', column: 'amount', name: '订单金额', code: 'order_amount' },
    ]
    const initial = await materializeDatasetAIPlan(initialPlan, [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1')
    const filteredGraph = {
      ...initial.graph,
      joins: initial.graph.joins.map(join => ({ ...join, outputKeys: ['node_1.customer_id', 'node_2.amount'] })),
    }

    const modified = planWithGroupsBeforeAndAfterJoin()
    const result = await materializeDatasetAIPlan(modified, [orders, customers], async tableID => columns[tableID], initial.draft, 'dataset_ai_1', filteredGraph)

    expect(result.draft.nodes.find(node => node.id === 'node_1')?.selected).toContain('region')
    expect(result.graph.joins[0].outputKeys).toEqual(['node_1.customer_id', 'node_2.amount', 'node_1.region'])
    expect(result.graph.groups.find(group => group.id === 'group_2')?.dimensions.map(item => item.key)).toEqual(['node_1.region'])
    expect(result.graph.end?.outputs.map(output => output.key)).toEqual(['node_1.region', 'node_2.amount'])

    const dsl = buildDatasetDSL(result.draft)
    expect(dsl.nodes.find(node => node.id === 'node_1')?.projection).toContain('region')
    expect(dsl.groupBy).toEqual(['field_customers_region'])
    expect(dsl.fields).toEqual(expect.arrayContaining([
      expect.objectContaining({ code: 'customer_region', role: 'DIMENSION' }),
      expect.objectContaining({ code: 'order_amount', role: 'MEASURE', expression: expect.objectContaining({ type: 'AGGREGATE', function: 'SUM' }) }),
    ]))
  })

  it('下游必需字段已被关联上游分组截断时返回明确错误', async () => {
    const invalid = planWithGroupsBeforeAndAfterJoin()
    invalid.nodes[0].selectedColumns.push('customer_name')
    invalid.groups.unshift({
      id: 'group_customers', name: '关联前客户汇总', input: { kind: 'NODE', id: 'node_1' },
      dimensions: [{ nodeId: 'node_1', column: 'customer_id', grouping: '' }],
      metrics: [{ nodeId: 'node_1', column: 'region', aggregation: 'COUNT' }],
    })
    invalid.joins[0].left = { kind: 'GROUP', id: 'group_customers' }
    invalid.groups.find(group => group.id === 'group_2')!.dimensions = [{ nodeId: 'node_1', column: 'customer_name', grouping: '' }]
    invalid.end.outputs[0] = { nodeId: 'node_1', column: 'customer_name', name: '客户名称', code: 'customer_name' }

    await expect(materializeDatasetAIPlan(invalid, [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1'))
      .rejects.toThrow('关联“客户订单关联”无法向下游提供字段 node_1.customer_name')
  })

  it('将更下游 Join 新增的关联键补入所属上游 Join 分支', async () => {
    const nestedPlan = (conditionColumn: 'customer_id' | 'region'): DatasetAIGraphPlan => {
      const result = planWithGroupsBeforeAndAfterJoin()
      result.nodes[0].selectedColumns = conditionColumn === 'region' ? ['customer_id', 'region'] : ['customer_id']
      result.nodes.push({
        id: 'node_3', tableId: customers.id, alias: 'region_lookup',
        selectedColumns: conditionColumn === 'region' ? ['customer_id', 'region'] : ['customer_id'],
      })
      result.groups = result.groups.filter(group => group.id === 'group_1')
      result.joins.push({
        id: 'join_2', name: '地区再关联', left: { kind: 'JOIN', id: 'join_1' }, right: { kind: 'NODE', id: 'node_3' }, joinType: 'INNER',
        conditions: [{ leftNodeId: 'node_1', leftColumn: conditionColumn, rightNodeId: 'node_3', rightColumn: conditionColumn }],
      })
      result.end = {
        name: '最终输出', input: { kind: 'JOIN', id: 'join_2' },
        outputs: [{ nodeId: 'node_2', column: 'amount', name: '订单金额', code: 'order_amount' }],
      }
      return result
    }

    const initial = await materializeDatasetAIPlan(nestedPlan('customer_id'), [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1')
    const filteredGraph = {
      ...initial.graph,
      joins: initial.graph.joins.map(join => join.id === 'join_1'
        ? { ...join, outputKeys: ['node_1.customer_id', 'node_2.amount'] }
        : join),
    }

    const result = await materializeDatasetAIPlan(nestedPlan('region'), [orders, customers], async tableID => columns[tableID], initial.draft, 'dataset_ai_1', filteredGraph)

    expect(result.graph.joins.find(join => join.id === 'join_1')?.outputKeys).toEqual(['node_1.customer_id', 'node_2.amount', 'node_1.region'])
    expect(result.graph.joins.find(join => join.id === 'join_1')?.outputKeys).not.toContain('node_3.region')
    const dsl = buildDatasetDSL(result.draft)
    const dslJoins = dsl.joins as Array<{ id: string; conditions: unknown[] }>
    expect(dslJoins.find(join => join.id === 'join_2')).toMatchObject({
      conditions: [{
        leftExpression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'region' },
        rightExpression: { type: 'FIELD_REF', nodeId: 'node_3', field: 'region' },
      }],
    })
  })

  it('rejects stale or inactive fields before changing editor state', async () => {
    const invalid = plan()
    invalid.nodes[1].selectedColumns.push('removed')
    await expect(materializeDatasetAIPlan(invalid, [orders, customers], async tableID => columns[tableID], empty, 'dataset_ai_1')).rejects.toThrow('已失效的字段')
  })
})
