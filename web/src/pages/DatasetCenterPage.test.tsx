import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode } from 'react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { RequestError } from '../lib/api'
import type { DatasetAIProposal } from '../lib/dataset-ai'
import { datasetAPI, type AssetColumn, type AssetTable, type DatasetPublicationRequest, type DatasetRecord, type DatasetSummary, type PublishedVersionRecord } from '../lib/datasets'
import { DatasetCenterPage } from './DatasetCenterPage'

beforeEach(() => {
  vi.spyOn(datasetAPI, 'tablePreview').mockImplementation(async tableID => tableID === customerTable.id
    ? { columns: ['customer_id', 'customer_name'], rows: [['C001', '示例客户']] }
    : { columns: ['order_id', 'amount'], rows: [['O001', 100]] })
  vi.spyOn(datasetAPI, 'previewCandidate').mockResolvedValue({
    queryId: 'component-preview', dslHash: 'c'.repeat(64), planHash: 'd'.repeat(64),
    columns: ['preview_value'], rows: [['组件预览样本']], rowCount: 1, durationMs: 3,
  })
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  sessionStorage.clear()
})

const summary = (overrides: Partial<DatasetSummary> = {}): DatasetSummary => ({
  id: 'dataset-1', code: 'orders_detail', name: '订单明细', description: '订单业务明细数据', type: 'SINGLE_SOURCE',
  status: 'PUBLISHED', version: 4, dslHash: 'a'.repeat(64), currentPublishedVersionId: 'version-1', updatedAt: '2026-07-17T01:00:00Z', ...overrides,
})
const table: AssetTable = { id: 'table-1', dataSourceId: 'source-1', dataSourceName: '销售业务库', dataSourceType: 'MYSQL', tableName: 'orders', schemaName: 'sales', businessName: '订单表', columnCount: 2 }
const customerTable: AssetTable = { id: 'table-2', dataSourceId: 'source-2', dataSourceName: '客户业务库', dataSourceType: 'ORACLE', tableName: 'customers', schemaName: 'crm', businessName: '客户表', columnCount: 2 }
const columns: AssetColumn[] = [
  { id: 'column-1', tableId: table.id, columnName: 'order_id', businessName: '订单编号', canonicalType: 'STRING', nullable: false, semanticType: 'IDENTIFIER' },
  { id: 'column-2', tableId: table.id, columnName: 'amount', businessName: '订单金额', canonicalType: 'DECIMAL', nullable: false, semanticType: 'MEASURE' },
  { id: 'column-hidden-1', tableId: table.id, columnName: 'order_note', businessName: '订单备注', canonicalType: 'STRING', nullable: true, semanticType: 'TEXT' },
]
const customerColumns: AssetColumn[] = [
  { id: 'column-3', tableId: customerTable.id, columnName: 'customer_id', businessName: '客户编号', canonicalType: 'STRING', nullable: false, semanticType: 'IDENTIFIER' },
  { id: 'column-4', tableId: customerTable.id, columnName: 'customer_name', businessName: '客户名称', canonicalType: 'STRING', nullable: false, semanticType: 'ATTRIBUTE' },
  { id: 'column-hidden-2', tableId: customerTable.id, columnName: 'customer_region', businessName: '客户区域', canonicalType: 'STRING', nullable: true, semanticType: 'REGION' },
]
const aiProposal = (overrides: Partial<DatasetAIProposal> = {}): DatasetAIProposal => ({
  schemaVersion: '2.3',
  mode: 'CREATE',
  summary: '使用订单表生成可直接预览的明细数据集',
  assumptions: ['订单编号可作为结果主键。'],
  warnings: [],
  changeSet: { operations: [], fieldChanges: [] },
  plan: {
    dataset: { name: 'AI 订单明细', description: '由订单映射表自动配置的订单明细' },
    nodes: [{ id: 'node_1', tableId: table.id, alias: 'orders', selectedColumns: ['order_id', 'amount'] }],
    joins: [],
    groups: [],
    end: {
      name: '最终输出',
      input: { kind: 'NODE', id: 'node_1' },
      outputs: [
        { nodeId: 'node_1', column: 'order_id', name: '订单编号', code: 'order_id' },
        { nodeId: 'node_1', column: 'amount', name: '订单金额', code: 'amount' },
      ],
    },
  },
  ...overrides,
})
const record = (overrides: Partial<DatasetRecord> = {}): DatasetRecord => ({
  ...summary(), draftVersionId: 'draft-1', draftVersionNo: 1, draftRecordVersion: 2, planHash: 'b'.repeat(64),
  dsl: { dslVersion: '1.0', dataset: { code: 'orders_detail', name: '订单明细', description: '订单业务明细数据', type: 'SINGLE_SOURCE' }, nodes: [{}], fields: [{}, {}] },
  logicalPlan: {}, createdAt: '2026-07-17T00:00:00Z', updatedAt: '2026-07-17T01:00:00Z', ...overrides,
})
const publicationRequest = (overrides: Partial<DatasetPublicationRequest> = {}): DatasetPublicationRequest => ({
  id: 'publication-request-1', datasetId: 'dataset-1', status: 'PENDING', version: 1,
  draftVersionId: 'draft-1', expectedDatasetVersion: 4, expectedDraftRecordVersion: 2,
  expectedDslHash: 'a'.repeat(64), expectedPlanHash: 'b'.repeat(64), requesterId: 'user-1', requestNote: '',
  submittedAt: '2026-07-20T10:00:00Z', updatedAt: '2026-07-20T10:00:00Z', ...overrides,
})
const publishedDatasetVersion = (overrides: Partial<PublishedVersionRecord> = {}): PublishedVersionRecord => ({
  id: 'published-version-2', datasetId: 'dataset-1', versionNo: 2, status: 'PUBLISHED', dslVersion: '1.0',
  dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), dsl: record().dsl, logicalPlan: {},
  publishedAt: '2026-07-20T10:05:00Z', publishedBy: 'approver-1', datasetRecordVersion: 5,
  draftVersionId: 'draft-1', draftRecordVersion: 2, ...overrides,
})
const page = (items: DatasetSummary[]) => ({ items, total: items.length, limit: 200, offset: 0 })
const renderPage = () => render(<MemoryRouter><DatasetCenterPage /></MemoryRouter>)
const cardFor = (name: string) => screen.getByRole('heading', { level: 3, name }).closest('article') as HTMLElement

function RouteLocationProbe() {
  const location = useLocation()
  return <output aria-label="当前路由">{JSON.stringify({ pathname: location.pathname, state: location.state })}</output>
}

function DatasetCenterLocationProbe() {
  const location = useLocation()
  return <><output aria-label="数据集流程路由">{JSON.stringify({ pathname: location.pathname, state: location.state })}</output><DatasetCenterPage /></>
}

type TestGraphInput = { kind: 'NODE' | 'JOIN' | 'GROUP' | 'TRANSFORM'; id: string }

const sourcePortName = (input: TestGraphInput) => {
  const index = Number(input.id.match(/(\d+)$/)?.[1] || 1)
  return input.kind === 'NODE' ? `从数据节点 ${index} 拖出连接`
    : input.kind === 'JOIN' ? `从关联节点 ${index} 拖出连接`
      : input.kind === 'GROUP' ? `从分组组件 ${index} 拖出连接`
        : new RegExp(`^从(?!数据节点|关联节点|分组组件).+ ${index} 拖出连接$`)
}

function connectByLine(dialog: HTMLElement, source: TestGraphInput, target: HTMLElement) {
  const values = new Map<string, string>()
  const dataTransfer = { setData: (type: string, value: string) => values.set(type, value), getData: (type: string) => values.get(type) ?? '' }
  const sourcePort = within(dialog).getByRole('button', { name: sourcePortName(source) })
  fireEvent.dragStart(sourcePort, { dataTransfer })
  fireEvent.dragOver(target, { dataTransfer })
  fireEvent.drop(target, { dataTransfer })
  fireEvent.dragEnd(sourcePort, { dataTransfer })
}

async function addRelationBox(dialog: HTMLElement, user: ReturnType<typeof userEvent.setup>, left: TestGraphInput, right: TestGraphInput) {
  await user.click(within(dialog).getByRole('button', { name: /关联组件双输入/ }))
  const slotOne = within(dialog).getAllByRole('button', { name: /连接到关联节点 \d+ 槽位 1/ }).at(-1)!
  const slotTwo = within(dialog).getAllByRole('button', { name: /连接到关联节点 \d+ 槽位 2/ }).at(-1)!
  connectByLine(dialog, left, slotOne)
  connectByLine(dialog, right, slotTwo)
}

test('展示全部数据集并支持组合筛选', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([
    summary(),
    summary({ id: 'dataset-2', code: 'customer_summary', name: '客户汇总', type: 'CROSS_SOURCE', status: 'DRAFT' }),
  ]))
  const user = userEvent.setup()
  renderPage()

  await screen.findByRole('list', { name: '数据集资产清单' })
  expect(screen.getByText('显示 2 / 2')).toBeInTheDocument()
  await user.type(screen.getByLabelText('搜索数据集'), 'customer')
  expect(screen.getByRole('heading', { level: 3, name: '客户汇总' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { level: 3, name: '订单明细' })).not.toBeInTheDocument()
  await user.clear(screen.getByLabelText('搜索数据集'))
  await user.selectOptions(screen.getByLabelText('按数据集类型筛选'), 'CROSS_SOURCE')
  await user.selectOptions(screen.getByLabelText('按数据集状态筛选'), 'PUBLISHED')
  expect(screen.getByText('没有符合条件的数据集')).toBeInTheDocument()
})

test('映射表默认数据集在目录和详情中展示来源标识', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary({ originTableId: table.id })]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(record({ originTableId: table.id }))
  vi.spyOn(datasetAPI, 'preview').mockResolvedValue({ queryId: 'query-1', columns: ['order_id'], rows: [['O001']], rowCount: 1, durationMs: 5 })
  const user = userEvent.setup()
  renderPage()

  await screen.findByRole('heading', { level: 3, name: '订单明细' })
  const card = cardFor('订单明细')
  expect(await within(card).findByText('映射表数据集')).toHaveAttribute('title', '由已完成映射的数据资产自动创建')
  await user.click(within(card).getByRole('button', { name: '查看' }))

  const detailDialog = await screen.findByRole('dialog', { name: '数据集详情' })
  expect(await within(detailDialog).findByText('映射表数据集')).toBeInTheDocument()
})

test('新建弹窗通过拖拽或点选增加多表节点，确认关系后再要求名称与说明', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table, customerTable] })
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === customerTable.id ? customerColumns : columns }))
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const createDialog = await screen.findByRole('dialog', { name: '新建数据集' })
  expect(within(createDialog).getByRole('button', { name: /销售业务库/ })).toHaveAttribute('aria-expanded', 'true')
  const tableButton = within(createDialog).getByRole('button', { name: /订单表/ })
  expect(tableButton).toHaveAttribute('draggable', 'true')
  await user.click(tableButton)
  const orderDrawer = await within(createDialog).findByLabelText('配置表 订单表')
  expect(within(orderDrawer).queryByRole('radio', { name: '分组聚合' })).not.toBeInTheDocument()
  await user.click(within(createDialog).getByRole('button', { name: '预览数据节点 1数据' }))
  const orderPreview = await within(createDialog).findByRole('dialog', { name: /订单表.*数据预览/ })
  expect(await within(orderPreview).findByText('O001')).toBeInTheDocument()
  expect(within(orderPreview).getByText(/最多展示 5 行/)).toBeInTheDocument()
  await user.click(within(orderPreview).getByRole('button', { name: '关闭组件数据预览' }))
  await user.click(within(orderDrawer).getByLabelText('输出字段 order_note'))
  await user.click(within(orderDrawer).getByRole('button', { name: '完成' }))
  await user.click(within(createDialog).getByRole('button', { name: /客户业务库/ }))
  await user.click(within(createDialog).getByRole('button', { name: /客户表/ }))
  const customerDrawer = await within(createDialog).findByLabelText('配置表 客户表')
  await user.click(within(customerDrawer).getByLabelText('输出字段 customer_region'))
  await user.click(within(customerDrawer).getByRole('button', { name: '完成' }))
  await addRelationBox(createDialog, user, { kind: 'NODE', id: 'node_1' }, { kind: 'NODE', id: 'node_2' })
  expect(await within(createDialog).findByText(/2 个数据节点 · 1 个关联 · 0 个分组 · 1 个结束节点/)).toBeInTheDocument()
  expect(createDialog.querySelectorAll('.dataset-component-lines > path')).toHaveLength(3)
  expect(within(createDialog).getByText('数据资产节点')).toBeInTheDocument()
  expect(within(createDialog).queryByText('星型关系')).not.toBeInTheDocument()
  await user.click(within(createDialog).getByRole('button', { name: '保存配置' }))
  expect(within(createDialog).getByRole('alert')).toHaveTextContent('完成每个关联组件的槽位、连接方式和关联字段')
  await user.click(within(createDialog).getByRole('button', { name: '配置关联 1' }))
  const relationDrawer = within(createDialog).getByLabelText('配置表关联')
  await user.click(within(relationDrawer).getByRole('button', { name: 'INNER JOIN' }))
  expect(within(relationDrawer).queryByText('实际关联端点')).not.toBeInTheDocument()
  expect(within(relationDrawer).queryByText('关系基数')).not.toBeInTheDocument()
  expect(within(relationDrawer).getByLabelText('条件 1 左字段')).toHaveValue('order_id')
  expect(within(relationDrawer).getByLabelText('条件 1 右字段')).toHaveValue('customer_id')
  expect(within(relationDrawer).getByLabelText('条件 1 左字段')).not.toHaveTextContent('订单备注')
  expect(within(relationDrawer).getByLabelText('条件 1 右字段')).not.toHaveTextContent('客户区域')
  expect(within(relationDrawer).getByLabelText('关联输出 t1_order_id')).toBeChecked()
  expect(within(relationDrawer).getByLabelText('关联输出 t2_customer_name')).toBeChecked()
  await user.click(within(relationDrawer).getByRole('button', { name: '＋ 添加关联字段' }))
  await user.selectOptions(within(relationDrawer).getByLabelText('条件 2 左字段'), 'amount')
  await user.selectOptions(within(relationDrawer).getByLabelText('条件 2 右字段'), 'customer_name')
  await user.click(within(relationDrawer).getByRole('button', { name: '完成' }))
  await user.click(within(createDialog).getByRole('button', { name: /分组组件可添加多个/ }))
  connectByLine(createDialog, { kind: 'JOIN', id: 'join_1' }, within(createDialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  const groupingDrawer = within(createDialog).getByLabelText('配置分组组件')
  expect(within(groupingDrawer).getByLabelText('分组组件输入')).toHaveTextContent('关联结果 1')
  expect(within(groupingDrawer).getByLabelText('分组组件输入').tagName).toBe('DIV')
  await user.click(within(groupingDrawer).getByLabelText('分组维度 t1_order_id'))
  await user.click(within(groupingDrawer).getByLabelText('分组维度 t2_customer_id'))
  expect(within(groupingDrawer).getByLabelText('t1_order_id 字段别名')).toHaveTextContent('t1_order_id')
  expect(within(groupingDrawer).getByLabelText('t1_order_id 字段别名').tagName).toBe('OUTPUT')
  expect(within(groupingDrawer).queryByLabelText('t1_order_id 维度名称')).not.toBeInTheDocument()
  expect(within(groupingDrawer).queryByLabelText('t1_order_id 维度编码')).not.toBeInTheDocument()
  await user.click(within(groupingDrawer).getByLabelText('聚合指标 t1_amount'))
  await user.selectOptions(within(groupingDrawer).getByLabelText('t1_amount 聚合逻辑'), 'SUM')
  expect(within(groupingDrawer).getByLabelText('t1_amount 字段别名')).toHaveTextContent('t1_amount')
  expect(within(groupingDrawer).queryByLabelText('t1_amount 指标名称')).not.toBeInTheDocument()
  expect(within(groupingDrawer).queryByLabelText('t1_amount 指标编码')).not.toBeInTheDocument()
  await user.click(within(groupingDrawer).getByLabelText('聚合指标 t2_customer_name'))
  await user.selectOptions(within(groupingDrawer).getByLabelText('t2_customer_name 聚合逻辑'), 'COUNT')
  expect(createDialog.querySelectorAll('.dataset-component-lines > path')).toHaveLength(4)
  await user.click(within(groupingDrawer).getByRole('button', { name: '完成' }))
  connectByLine(createDialog, { kind: 'GROUP', id: 'group_1' }, within(createDialog).getByRole('button', { name: '连接到结束节点输入槽位' }))
  await user.click(within(createDialog).getByRole('button', { name: '保存配置' }))

  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))
  expect(within(metadataDialog).getByRole('alert')).toHaveTextContent('请填写数据集名称和说明')
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '订单经营汇总')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '按订单维度汇总销售金额')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  expect(create).toHaveBeenCalledTimes(1)
  const dsl = create.mock.calls[0][0]
  expect(dsl.dataset.name).toBe('订单经营汇总')
  expect(dsl.dataset.description).toBe('按订单维度汇总销售金额')
  expect(dsl.dataset.type).toBe('CROSS_SOURCE')
  expect(dsl.nodes).toHaveLength(2)
  expect(dsl.nodes[0].projection).not.toContain('order_note')
  expect(dsl.nodes[1].projection).not.toContain('customer_region')
  expect((dsl.joins as Array<Record<string, unknown>>)[0]).toMatchObject({ leftNodeId: 'node_1', rightNodeId: 'node_2', joinType: 'INNER', cardinality: 'UNKNOWN', manualConfirmed: true })
  expect(((dsl.joins as Array<Record<string, unknown>>)[0].conditions as unknown[])).toHaveLength(2)
  expect(dsl.fields).toHaveLength(4)
  expect(dsl.groupBy).toEqual(['field_t1_order_id', 'field_t2_customer_id'])
  expect((dsl.fields as Array<Record<string, unknown>>).find(field => field.code === 't1_amount')).toMatchObject({ role: 'MEASURE', expression: { type: 'AGGREGATE', function: 'SUM' } })
  expect((dsl.fields as Array<Record<string, unknown>>).find(field => field.code === 't2_customer_name')).toMatchObject({ role: 'MEASURE', expression: { type: 'AGGREGATE', function: 'COUNT' } })
  expect(await screen.findByRole('status')).toHaveTextContent('已创建“订单明细”')
})

test('组件栏位于画布左侧且每个已放置组件都可从卡片下方预览最多五行', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.mocked(datasetAPI.tablePreview).mockResolvedValue({
    columns: ['order_id'],
    rows: [['R001'], ['R002'], ['R003'], ['R004'], ['R005'], ['R006']],
  })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  expect(datasetAPI.tablePreview).not.toHaveBeenCalled()
  await user.click(within(within(dialog).getByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))

  const palette = within(dialog).getByLabelText('画布组件栏')
  expect(palette.tagName).toBe('ASIDE')
  expect(within(palette).getAllByRole('button')).toHaveLength(16)
  const buttonLabels = (groupName: string) => within(within(palette).getByLabelText(groupName)).getAllByRole('button').map(button => button.querySelector('strong')?.textContent)
  expect(buttonLabels('流程组件')).toEqual(['分组组件', '关联组件', '结束节点'])
  expect(buttonLabels('文本组件')).toEqual(['大写转换', '空格清理', '文本替换', '小写转换', '字段截取', '字段拼接'])
  expect(buttonLabels('数值组件')).toEqual(['取绝对值', '数值取整', '数值运算'])
  expect(buttonLabels('日期组件')).toEqual(['日期转换'])
  expect(buttonLabels('规则组件')).toEqual(['空值填充', '类型转换', '条件映射'])
  expect(Array.from(palette.querySelectorAll<SVGElement>('button > svg[data-component-icon]')).map(icon => icon.dataset.componentIcon)).toEqual([
    'GROUP', 'JOIN', 'END',
    'TEXT_UPPER', 'TEXT_TRIM', 'TEXT_REPLACE', 'TEXT_LOWER', 'TEXT_SUBSTRING', 'TEXT_CONCAT',
    'NUMBER_ABSOLUTE', 'NUMBER_ROUNDING', 'NUMBER_ARITHMETIC',
    'DATE_FORMAT', 'NULL', 'CAST', 'CONDITION',
  ])
  expect(within(palette).getByLabelText('文本组件')).toHaveClass('component-text')
  expect(within(palette).getByLabelText('数值组件')).toHaveClass('component-number')
  expect(within(palette).getByLabelText('日期组件')).toHaveClass('component-date')
  await user.click(within(palette).getByRole('button', { name: /关联组件双输入/ }))
  await user.click(within(palette).getByRole('button', { name: /分组组件可添加多个/ }))
  await user.click(within(palette).getByRole('button', { name: /日期转换输出年、年月、年季或年月日/ }))

  const expectConfigWithoutPreview = (drawer: HTMLElement) => {
    expect(within(drawer).queryByText('组件数据预览')).not.toBeInTheDocument()
    expect(within(drawer).queryByRole('table')).not.toBeInTheDocument()
  }
  expectConfigWithoutPreview(within(dialog).getByLabelText('配置日期转换'))
  await user.click(within(dialog).getByRole('button', { name: '配置关联 1' }))
  expectConfigWithoutPreview(within(dialog).getByLabelText('配置表关联'))
  await user.click(within(dialog).getByRole('button', { name: '打开分组组件 1 配置' }))
  expectConfigWithoutPreview(within(dialog).getByLabelText('配置分组组件'))
  await user.click(within(dialog).getByRole('button', { name: '打开结束节点配置' }))
  expectConfigWithoutPreview(within(dialog).getByLabelText('配置结束节点'))
  await user.click(within(dialog).getByRole('button', { name: '配置数据节点 1' }))
  expectConfigWithoutPreview(within(dialog).getByLabelText('配置表 订单表'))
  expect(datasetAPI.tablePreview).not.toHaveBeenCalled()
  expect(datasetAPI.previewCandidate).not.toHaveBeenCalled()
  expect(within(dialog).queryByRole('dialog', { name: /数据预览/ })).not.toBeInTheDocument()

  expect(within(dialog).getByRole('button', { name: '预览数据节点 1数据' })).toHaveTextContent('点击预览')
  expect(within(dialog).getByRole('button', { name: '预览关联组件 1数据' })).toHaveTextContent('点击预览')
  expect(within(dialog).getByRole('button', { name: '预览分组组件 1数据' })).toHaveTextContent('点击预览')
  expect(within(dialog).getByRole('button', { name: '预览日期转换 1数据' })).toHaveTextContent('点击预览')
  expect(within(dialog).getByRole('button', { name: '预览结束节点数据' })).toHaveTextContent('点击预览')

  await user.click(within(dialog).getByRole('button', { name: '预览数据节点 1数据' }))
  const previewDialog = await within(dialog).findByRole('dialog', { name: /订单表.*数据预览/ })
  expect(await within(previewDialog).findByText('R005')).toBeInTheDocument()
  expect(within(previewDialog).queryByText('R006')).not.toBeInTheDocument()
})

test('细粒度文本与数值组件各自只暴露对应处理逻辑并沿用同类颜色', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(await within(dialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))

  const cases = [
    { button: /大写转换英文字母统一转大写/, label: '大写转换', operation: 'UPPER', color: 'component-text', fixed: true },
    { button: /空格清理去除文本首尾空格/, label: '空格清理', operation: 'TRIM', color: 'component-text', fixed: true },
    { button: /文本替换查找并替换指定文本/, label: '文本替换', operation: 'REPLACE', color: 'component-text', fixed: true },
    { button: /小写转换英文字母统一转小写/, label: '小写转换', operation: 'LOWER', color: 'component-text', fixed: true },
    { button: /取绝对值将负数转换为正数值/, label: '取绝对值', operation: 'ABS', color: 'component-number', fixed: true },
    { button: /数值取整四舍五入或上下取整/, label: '数值取整', operation: 'ROUND', color: 'component-number', fixed: false },
  ] as const

  for (const [index, item] of cases.entries()) {
    const number = index + 1
    await user.click(within(dialog).getByRole('button', { name: item.button }))
    const card = within(dialog).getByRole('button', { name: `打开${item.label} ${number} 配置` })
    expect(card).toHaveClass(item.color)
    connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, card)
    const drawer = within(dialog).getByLabelText(`配置${item.label}`)
    expect(drawer).toHaveClass(item.color)
    expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toHaveValue(item.operation)
    if (item.fixed) expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toBeDisabled()
    else expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toBeEnabled()
    await user.click(within(drawer).getByRole('button', { name: '完成' }))
  }
})

test('组件左右半区分别承担接收与拖出连线，点击热区仍打开组件配置', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(await within(dialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /分组组件可添加多个/ }))
  await user.click(within(within(dialog).getByLabelText('配置分组组件')).getByRole('button', { name: '保存并关闭分组配置' }))

  const sourceSide = within(dialog).getByRole('button', { name: '从数据节点 1 拖出连接' })
  const inputSide = within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' })
  const outputSide = within(dialog).getByRole('button', { name: '从分组组件 1 拖出连接' })
  expect(sourceSide).toHaveClass('component-side', 'output-side')
  expect(inputSide).toHaveClass('component-side', 'input-side')
  expect(outputSide).toHaveClass('component-side', 'output-side')
  expect(outputSide).toHaveAttribute('aria-disabled', 'true')
  expect(outputSide).toHaveAttribute('draggable', 'false')

  await user.click(outputSide)
  expect(within(dialog).getByLabelText('配置分组组件')).toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '保存并关闭分组配置' }))

  const originalElementFromPoint = document.elementFromPoint
  Object.defineProperty(document, 'elementFromPoint', { configurable: true, value: vi.fn(() => inputSide) })
  try {
    fireEvent.pointerDown(sourceSide, { button: 0, clientX: 20, clientY: 20, isPrimary: true, pointerId: 7 })
    fireEvent.pointerMove(window, { clientX: 80, clientY: 60, isPrimary: true, pointerId: 7 })
    fireEvent.pointerUp(window, { clientX: 120, clientY: 80, isPrimary: true, pointerId: 7 })
  } finally {
    if (originalElementFromPoint) Object.defineProperty(document, 'elementFromPoint', { configurable: true, value: originalElementFromPoint })
    else Reflect.deleteProperty(document, 'elementFromPoint')
  }
  await user.click(inputSide)
  expect(within(dialog).getByLabelText('分组组件输入')).toHaveTextContent('订单表 (t1)')
})

test('单表可以直接保存且不会引用已失效字段', async () => {
  const inactiveColumn: AssetColumn = { id: 'column-inactive', tableId: table.id, columnName: 'legacy_flag', businessName: '历史标记', canonicalType: 'STRING', nullable: true, semanticType: 'TEXT', assetStatus: 'INACTIVE' }
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: [...columns, inactiveColumn] })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const createDialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(createDialog).getByRole('button', { name: /订单表.*已映射/ }))
  const tableDrawer = await within(createDialog).findByLabelText('配置表 订单表')
  expect(within(tableDrawer).queryByText('历史标记')).not.toBeInTheDocument()
  await user.click(within(tableDrawer).getByLabelText('输出字段 order_note'))
  await user.click(within(tableDrawer).getByRole('button', { name: '完成' }))
  expect(createDialog.querySelectorAll('.dataset-component-lines > path')).toHaveLength(1)
  await user.click(within(createDialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '单表订单')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '单表直接保存验证')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const dsl = create.mock.calls[0][0]
  expect(dsl.joins).toEqual([])
  const projection = dsl.nodes[0].projection as string[]
  expect(projection).toEqual(['order_id', 'amount'])
  expect(projection).not.toContain('legacy_flag')
  expect(dsl.designer?.end?.input).toEqual({ kind: 'NODE', id: 'node_1' })
  expect(dsl.designer?.end?.outputs.map(output => output.key)).toEqual(projection.map(field => `node_1.${field}`))
})

test('数值运算组件支持整卡碰撞连线并生成可执行表达式', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(await within(dialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /数值运算两个字段加减乘除/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '打开数值运算 1 配置' }))

  const drawer = within(dialog).getByLabelText('配置数值运算')
  expect(within(drawer).getByLabelText('字段处理组件输入')).toHaveTextContent('订单表 (t1)')
  expect(within(drawer).getByLabelText('规则 1 输入字段 1')).toHaveValue('node_1.amount')
  expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toHaveValue('ADD')
  expect(within(drawer).getByLabelText('规则 1 输入字段 2')).toHaveValue('node_1.amount')
  expect(within(drawer).queryByLabelText('规则 1 保留小数位')).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '预览数值运算 1数据' }))
  const transformPreview = await within(dialog).findByRole('dialog', { name: '数值运算 1数据预览' })
  expect(await within(transformPreview).findByText('组件预览样本')).toBeInTheDocument()
  expect(datasetAPI.previewCandidate).toHaveBeenCalledWith(expect.objectContaining({
    designer: expect.objectContaining({ end: expect.objectContaining({ input: { kind: 'TRANSFORM', id: 'transform_1' } }) }),
  }), expect.any(String), {}, 5)
  await user.selectOptions(within(drawer).getByLabelText('规则 1 处理逻辑'), 'MULTIPLY')
  expect(within(drawer).getByLabelText('规则 1 输入字段 2')).toHaveValue('node_1.amount')
  await user.clear(within(drawer).getByLabelText('规则 1 输出编码'))
  await user.type(within(drawer).getByLabelText('规则 1 输出编码'), 'amount_squared')
  await user.click(within(drawer).getByRole('button', { name: '完成' }))
  connectByLine(dialog, { kind: 'TRANSFORM', id: 'transform_1' }, within(dialog).getByRole('button', { name: '连接到结束节点输入槽位' }))

  expect(within(dialog).getByText(/1 个字段处理/)).toBeInTheDocument()
  expect(dialog.querySelectorAll('.dataset-component-lines > path')).toHaveLength(2)
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '订单字段计算')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '验证字段处理组件生成可执行表达式')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const dsl = create.mock.calls[0][0]
  expect(dsl.designer?.transforms).toHaveLength(1)
  expect(dsl.designer?.transforms?.[0].componentType).toBe('NUMBER_ARITHMETIC')
  expect(dsl.designer?.end?.input).toEqual({ kind: 'TRANSFORM', id: 'transform_1' })
  expect((dsl.fields as Array<Record<string, unknown>>).find(field => field.code === 'amount_squared')).toMatchObject({
    expression: {
      type: 'MULTIPLY',
      arguments: [
        { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' },
        { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' },
      ],
    },
  })
})

test('字段截取组件可选择数值上游字段并保存类型安全的截取参数', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(await within(dialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /字段截取按起始位置截取文本/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到字段截取 1 输入槽位' }))

  const drawer = within(dialog).getByLabelText('配置字段截取')
  expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toHaveValue('SUBSTRING')
  expect(within(drawer).getByLabelText('规则 1 输入字段 1')).toHaveValue('node_1.order_id')
  expect([...within(drawer).getByLabelText('规则 1 输入字段 1').querySelectorAll('option')].map(option => option.value)).toContain('node_1.amount')
  await user.selectOptions(within(drawer).getByLabelText('规则 1 输入字段 1'), 'node_1.amount')
  await user.clear(within(drawer).getByLabelText('规则 1 截取起始位置'))
  await user.type(within(drawer).getByLabelText('规则 1 截取起始位置'), '2')
  await user.clear(within(drawer).getByLabelText('规则 1 截取长度'))
  await user.type(within(drawer).getByLabelText('规则 1 截取长度'), '3')
  await user.clear(within(drawer).getByLabelText('规则 1 输出编码'))
  await user.type(within(drawer).getByLabelText('规则 1 输出编码'), 'order_short')
  await user.click(within(drawer).getByRole('button', { name: '完成' }))
  connectByLine(dialog, { kind: 'TRANSFORM', id: 'transform_1' }, within(dialog).getByRole('button', { name: '连接到结束节点输入槽位' }))
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))

  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '订单文本处理')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '验证字段截取表达式')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const dsl = create.mock.calls[0][0]
  expect(dsl.designer?.transforms?.[0].componentType).toBe('TEXT_SUBSTRING')
  expect((dsl.fields as Array<Record<string, unknown>>).find(field => field.code === 'order_short')).toMatchObject({
    expression: {
      type: 'SUBSTRING',
      arguments: [
        { type: 'CAST', targetType: 'STRING', argument: { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' } },
        { type: 'LITERAL', value: 2 },
        { type: 'LITERAL', value: 3 },
      ],
    },
  })
})

test('条件映射、空值填充、字段拼接与字段截取提供完整且符合直觉的配置', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(await within(dialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /条件映射按比较条件输出新值/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '打开条件映射 1 配置' }))
  let drawer = within(dialog).getByLabelText('配置条件映射')
  const operator = within(drawer).getByLabelText('规则 1 判断条件')
  expect(within(operator).getAllByRole('option').map(option => option.textContent)).toEqual([
    '等于', '不等于', '大于', '大于等于', '小于', '小于等于', '包含', '不包含', '在…中', '为空', '不为空',
  ])
  await user.selectOptions(within(drawer).getByLabelText('规则 1 输入字段 1'), 'node_1.amount')
  await user.selectOptions(operator, 'GT')
  await user.type(within(drawer).getByLabelText('规则 1 匹配值'), '100')
  await user.type(within(drawer).getByLabelText('规则 1 命中值'), '大额')
  await user.type(within(drawer).getByLabelText('规则 1 默认值'), '普通')
  await user.click(within(dialog).getByRole('button', { name: '预览条件映射 1数据' }))
  await within(dialog).findByRole('dialog', { name: '条件映射 1数据预览' })
  const conditionDSL = vi.mocked(datasetAPI.previewCandidate).mock.calls.at(-1)![0]
  expect(conditionDSL.fields).toEqual(expect.arrayContaining([expect.objectContaining({
    expression: {
      type: 'CASE',
      whens: [{ when: { type: 'GT', left: { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' }, right: { type: 'LITERAL', value: 100 } }, then: { type: 'LITERAL', value: '大额' } }],
      else: { type: 'LITERAL', value: '普通' },
    },
  })]))
  await user.click(within(dialog).getByRole('button', { name: '关闭组件数据预览' }))
  await user.selectOptions(within(drawer).getByLabelText('规则 1 输入字段 1'), 'node_1.order_id')
  await user.selectOptions(operator, 'IN')
  expect(within(drawer).getByLabelText('规则 1 条件值数组')).toHaveTextContent('每项可选上游字段或填写固定值')
  await user.type(within(drawer).getByLabelText('规则 1 候选值 1'), 'O001')
  await user.click(within(drawer).getByRole('button', { name: '＋ 添加候选值' }))
  await user.selectOptions(within(drawer).getByLabelText('规则 1 候选值 2 来源'), 'FIELD')
  await user.selectOptions(within(drawer).getByLabelText('规则 1 候选值 2 字段'), 'node_1.order_note')
  await user.click(within(dialog).getByRole('button', { name: '预览条件映射 1数据' }))
  await within(dialog).findByRole('dialog', { name: '条件映射 1数据预览' })
  const inConditionDSL = vi.mocked(datasetAPI.previewCandidate).mock.calls.at(-1)![0]
  expect(inConditionDSL.fields).toEqual(expect.arrayContaining([expect.objectContaining({
    expression: expect.objectContaining({
      type: 'CASE',
      whens: [expect.objectContaining({ when: {
        type: 'IN', left: { type: 'FIELD_REF', nodeId: 'node_1', field: 'order_id' },
        right: { type: 'ARRAY', arguments: [{ type: 'LITERAL', value: 'O001' }, { type: 'FIELD_REF', nodeId: 'node_1', field: 'order_note' }] },
      } })],
    }),
  })]))
  await user.click(within(dialog).getByRole('button', { name: '关闭组件数据预览' }))
  await user.click(within(drawer).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /空值填充仅为空时补固定值或字段/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '打开空值填充 2 配置' }))
  drawer = within(dialog).getByLabelText('配置空值填充')
  expect(within(drawer).getByLabelText('规则 1 补值来源')).toHaveValue('LITERAL')
  expect(within(drawer).getByLabelText('规则 1 空值填充值')).toHaveValue('-')
  expect(within(drawer).queryByLabelText('规则 1 输入字段 2')).not.toBeInTheDocument()
  await user.selectOptions(within(drawer).getByLabelText('规则 1 输入字段 1'), 'node_1.amount')
  expect(within(drawer).getByLabelText('规则 1 空值填充值')).toHaveValue('0')
  await user.selectOptions(within(drawer).getByLabelText('规则 1 补值来源'), 'FIELD')
  expect(within(drawer).getByLabelText('规则 1 输入字段 2')).toHaveValue('node_1.amount')
  await user.selectOptions(within(drawer).getByLabelText('规则 1 补值来源'), 'LITERAL')
  expect(within(drawer).queryByLabelText('规则 1 输入字段 2')).not.toBeInTheDocument()
  await user.clear(within(drawer).getByLabelText('规则 1 空值填充值'))
  await user.click(within(drawer).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /字段拼接用连接符拼接两字段/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '打开字段拼接 3 配置' }))
  drawer = within(dialog).getByLabelText('配置字段拼接')
  expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toHaveValue('CONCAT')
  expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toBeDisabled()
  expect(within(drawer).getByLabelText('规则 1 输入字段 2')).toBeInTheDocument()
  expect(within(drawer).getByLabelText('规则 1 字段连接符')).toBeInTheDocument()
  expect(within(drawer).getByText(/NULL 按空文本合并/)).toBeInTheDocument()
  await user.click(within(drawer).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /字段截取按起始位置截取文本/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '打开字段截取 4 配置' }))
  drawer = within(dialog).getByLabelText('配置字段截取')
  expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toHaveValue('SUBSTRING')
  expect(within(drawer).getByLabelText('规则 1 处理逻辑')).toBeDisabled()
  expect(within(drawer).queryByLabelText('规则 1 输入字段 2')).not.toBeInTheDocument()
  expect(within(drawer).queryByLabelText('规则 1 字段连接符')).not.toBeInTheDocument()
  expect(within(drawer).getByLabelText('规则 1 截取起始位置')).toHaveValue(1)
  expect(within(drawer).getByLabelText('规则 1 截取长度')).toHaveValue(10)
})

test('字段处理连接分组后保留有效输入状态并可点击预览分组产物', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /字段截取按起始位置截取文本/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到字段截取 1 输入槽位' }))
  await user.click(within(within(dialog).getByLabelText('配置字段截取')).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /分组组件可添加多个/ }))
  connectByLine(dialog, { kind: 'TRANSFORM', id: 'transform_1' }, within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  const groupDrawer = within(dialog).getByLabelText('配置分组组件')
  expect(within(groupDrawer).getByLabelText('分组组件输入')).toHaveTextContent('字段截取 1')
  expect(within(groupDrawer).queryByText('字段处理节点已失效')).not.toBeInTheDocument()
  expect(within(groupDrawer).queryByText('请先连接输入组件')).not.toBeInTheDocument()
  await user.click(within(groupDrawer).getByLabelText('分组维度 t1_order_id'))
  await user.click(within(groupDrawer).getByLabelText('聚合指标 t1_amount'))
  await user.selectOptions(within(groupDrawer).getByLabelText('t1_amount 聚合逻辑'), 'SUM')
  await user.click(within(dialog).getByRole('button', { name: '预览分组组件 1数据' }))
  const groupPreview = await within(dialog).findByRole('dialog', { name: '分组结果 1数据预览' })
  expect(await within(groupPreview).findByText('组件预览样本')).toBeInTheDocument()
})

test('日期转换按所选格式产出字符串编码且进入分组时不改变原分组能力', async () => {
  const createdAt: AssetColumn = { id: 'column-date', tableId: table.id, columnName: 'created_at', businessName: '下单时间', canonicalType: 'DATETIME', nullable: false, semanticType: 'DATE' }
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: [...columns, createdAt] })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /日期转换输出年、年月、年季或年月日/ }))
  let transformDrawer = within(dialog).getByLabelText('配置日期转换')
  expect(within(transformDrawer).getByLabelText('字段处理组件输入')).toHaveTextContent('尚未连接')
  expect(within(transformDrawer).queryByLabelText('规则 1 输出格式')).not.toBeInTheDocument()
  expect(within(dialog).getByLabelText('关系组件画布').querySelectorAll('.dataset-component-lines > path')).toHaveLength(1)

  await user.click(within(transformDrawer).getByRole('button', { name: '保存并关闭字段处理配置' }))
  expect(within(dialog).queryByLabelText('配置日期转换')).not.toBeInTheDocument()
  expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '打开日期转换 1 配置' }))
  transformDrawer = within(dialog).getByLabelText('配置日期转换')
  await user.click(within(transformDrawer).getByRole('button', { name: '完成' }))
  expect(within(dialog).queryByLabelText('配置日期转换')).not.toBeInTheDocument()
  expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '打开日期转换 1 配置' }))

  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到日期转换 1 输入槽位' }))
  transformDrawer = within(dialog).getByLabelText('配置日期转换')
  const format = within(transformDrawer).getByLabelText('规则 1 输出格式')
  expect(within(format).getAllByRole('option').map(option => option.textContent)).toEqual(['年（YYYY）', '年月（YYYYMM）', '年季（YYYYQn）', '年月日（YYYYMMDD）'])
  expect(format).toHaveValue('DAY')
  await user.selectOptions(format, 'MONTH')
  expect(within(transformDrawer).getByText('输出示例：202607')).toBeInTheDocument()
  expect(within(transformDrawer).getByLabelText('规则 1 输出名称')).toHaveValue('下单时间年月')
  expect(within(transformDrawer).getByLabelText('规则 1 输出编码')).toHaveValue('t1_created_at_yyyymm')
  await user.click(within(dialog).getByRole('button', { name: '预览日期转换 1数据' }))
  const preview = await within(dialog).findByRole('dialog', { name: '日期转换 1数据预览' })
  expect(await within(preview).findByText('组件预览样本')).toBeInTheDocument()
  expect(datasetAPI.previewCandidate).toHaveBeenLastCalledWith(expect.objectContaining({
    fields: expect.arrayContaining([expect.objectContaining({
      code: 't1_created_at_yyyymm', canonicalType: 'STRING',
      expression: expect.objectContaining({ type: 'DATE_FORMAT', unit: 'MONTH' }),
    })]),
  }), expect.any(String), {}, 5)
  await user.click(within(preview).getByRole('button', { name: '关闭组件数据预览' }))
  await user.click(within(transformDrawer).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /分组组件可添加多个/ }))
  let groupDrawer = within(dialog).getByLabelText('配置分组组件')
  expect(within(groupDrawer).getByLabelText('分组组件输入')).toHaveTextContent('尚未连接')
  connectByLine(dialog, { kind: 'TRANSFORM', id: 'transform_1' }, within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  groupDrawer = within(dialog).getByLabelText('配置分组组件')
  const processedDate = within(groupDrawer).getByRole('checkbox', { name: /分组维度 .*created_at_yyyymm/i })
  await user.click(processedDate)
  expect(within(groupDrawer).queryByRole('combobox', { name: /created_at_yyyymm 分组粒度/i })).not.toBeInTheDocument()
  expect(within(groupDrawer).queryByLabelText(/created_at_yyyymm 已处理日期粒度/i)).not.toBeInTheDocument()

  await user.click(within(dialog).getByRole('button', { name: '删除结束节点' }))
  await user.click(within(dialog).getByRole('button', { name: /结束节点唯一/ }))
  expect(within(dialog).getByLabelText('结束节点输入')).toHaveTextContent('尚未连接')
})

test('分组组件选择原始日期字段时不提供日期转换且按原字段分组', async () => {
  const createdAt: AssetColumn = { id: 'column-date', tableId: table.id, columnName: 'created_at', businessName: '下单时间', canonicalType: 'DATETIME', nullable: false, semanticType: 'DATE' }
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: [...columns, createdAt] })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /分组组件可添加多个/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  const drawer = within(dialog).getByLabelText('配置分组组件')
  await user.click(within(drawer).getByRole('checkbox', { name: /分组维度 .*created_at/i }))
  expect(within(drawer).queryByRole('combobox', { name: /created_at.*分组粒度/i })).not.toBeInTheDocument()
  expect(within(drawer).getByText(/日期年月日请先连接独立日期转换组件/)).toBeInTheDocument()
  await user.click(within(drawer).getByLabelText('聚合指标 t1_amount'))
  await user.selectOptions(within(drawer).getByLabelText('t1_amount 聚合逻辑'), 'SUM')
  await user.click(within(dialog).getByRole('button', { name: '预览分组组件 1数据' }))
  await within(dialog).findByRole('dialog', { name: '分组结果 1数据预览' })
  const previewDSL = vi.mocked(datasetAPI.previewCandidate).mock.calls.at(-1)![0]
  const dateField = previewDSL.fields.find(field => JSON.stringify(field.expression) === JSON.stringify({ type: 'FIELD_REF', nodeId: 'node_1', field: 'created_at' }))
  expect(dateField?.expression).toEqual({ type: 'FIELD_REF', nodeId: 'node_1', field: 'created_at' })
})

test('删除唯一数据节点后再次选择映射表会重建直接结束连线', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const createDialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(createDialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(await within(createDialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))
  await user.click(within(createDialog).getByRole('button', { name: '移除订单表 (t1)' }))
  expect(within(createDialog).getByText('选择第一张映射表开始建模')).toBeInTheDocument()

  await user.click(within(createDialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(await within(createDialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))

  expect(within(createDialog).getByText(/1 个数据节点 · 0 个关联 · 0 个分组 · 1 个结束节点/)).toBeInTheDocument()
  expect(createDialog.querySelectorAll('.dataset-component-lines > path')).toHaveLength(1)
  await user.click(within(createDialog).getByRole('button', { name: '打开结束节点配置' }))
  expect(within(createDialog).getByLabelText('结束节点输入')).toHaveTextContent('数据节点 · 订单表 (t1)')
})

test('同一张物理表可以作为独立别名多次引用', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const createDialog = await screen.findByRole('dialog', { name: '新建数据集' })
  const assetButton = within(createDialog).getByRole('button', { name: /订单表.*已映射/ })
  await user.click(assetButton)
  await user.click(within(await within(createDialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))
  await user.click(assetButton)
  await user.click(within(await within(createDialog).findByLabelText('配置表 订单表')).getByRole('button', { name: '完成' }))
  expect(within(createDialog).getByText('已引用 2 次')).toBeInTheDocument()
  await addRelationBox(createDialog, user, { kind: 'NODE', id: 'node_1' }, { kind: 'NODE', id: 'node_2' })
  await user.click(within(createDialog).getByRole('button', { name: '配置关联 1' }))
  const relationDrawer = within(createDialog).getByLabelText('配置表关联')
  expect(within(relationDrawer).getByLabelText('关联槽位 1')).toHaveTextContent('数据节点 · 订单表 (t1)')
  expect(within(relationDrawer).getByLabelText('关联槽位 2')).toHaveTextContent('数据节点 · 订单表 (t2)')
  expect(within(relationDrawer).getByLabelText('关联槽位 1').tagName).toBe('DIV')
  expect(within(relationDrawer).queryByText('实际关联端点')).not.toBeInTheDocument()
  expect(within(relationDrawer).queryByText('关系基数')).not.toBeInTheDocument()
  await user.click(within(relationDrawer).getByRole('button', { name: '完成' }))
  connectByLine(createDialog, { kind: 'JOIN', id: 'join_1' }, within(createDialog).getByRole('button', { name: '连接到结束节点输入槽位' }))
  await user.click(within(createDialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '订单自关联')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '同一物理表的两个业务角色')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const dsl = create.mock.calls[0][0]
  expect(dsl.nodes).toHaveLength(2)
  expect(dsl.nodes.map(node => node.tableId)).toEqual(['table-1', 'table-1'])
  expect(dsl.nodes.map(node => node.alias)).toEqual(['t1', 't2'])
  expect((dsl.joins as Array<Record<string, unknown>>)[0].cardinality).toBe('UNKNOWN')
})

test('关联框支持把关联结果继续与表节点组成层级关系', async () => {
  const productTable: AssetTable = { ...table, id: 'table-3', dataSourceName: '商品业务库', tableName: 'products', businessName: '商品表' }
  const productColumns: AssetColumn[] = [
    { id: 'column-5', tableId: productTable.id, columnName: 'product_id', businessName: '商品编号', canonicalType: 'STRING', nullable: false, semanticType: 'IDENTIFIER' },
  ]
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table, customerTable, productTable] })
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === customerTable.id ? customerColumns : tableID === productTable.id ? productColumns : columns }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户业务库/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户表/ }))
  await user.click(within(dialog).getByRole('button', { name: /商品表/ }))
  await addRelationBox(dialog, user, { kind: 'NODE', id: 'node_1' }, { kind: 'NODE', id: 'node_2' })
  await user.click(within(dialog).getByLabelText('关联输出 t1_order_id'))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await addRelationBox(dialog, user, { kind: 'JOIN', id: 'join_1' }, { kind: 'NODE', id: 'node_3' })
  expect(within(dialog).getByLabelText('关联槽位 1')).toHaveTextContent('关联结果 1')
  await user.click(within(dialog).getByRole('button', { name: '配置关联 2' }))
  expect(within(dialog).getByLabelText('条件 1 左字段')).not.toHaveTextContent('订单编号')
})

test('关联组件可以从组件栏拖入画布并保留落点', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表/ }))
  const canvas = within(dialog).getByLabelText('关系组件画布')
  fireEvent.drop(canvas, {
    clientX: 720,
    clientY: 260,
    dataTransfer: { getData: (type: string) => type === 'text/dataset-component' ? 'JOIN' : '' },
  })

  expect(within(dialog).getByRole('button', { name: '配置关联 1' })).toHaveStyle({ left: '510px', top: '205px' })
  expect(within(dialog).getByLabelText('关联槽位 1')).toBeInTheDocument()
})

test('画布支持按数据流层级整理并全屏显示', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table, customerTable] })
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === customerTable.id ? customerColumns : columns }))
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /客户业务库/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await addRelationBox(dialog, user, { kind: 'NODE', id: 'node_1' }, { kind: 'NODE', id: 'node_2' })
  connectByLine(dialog, { kind: 'JOIN', id: 'join_1' }, within(dialog).getByRole('button', { name: '连接到结束节点输入槽位' }))

  await user.click(within(dialog).getByRole('button', { name: '整理' }))
  expect(within(dialog).getByRole('button', { name: '配置数据节点 1' })).toHaveStyle({ left: '42px', top: '48px' })
  expect(within(dialog).getByRole('button', { name: '配置数据节点 2' })).toHaveStyle({ left: '42px', top: '198px' })
  expect(within(dialog).getByRole('button', { name: '配置关联 1' })).toHaveStyle({ left: '342px', top: '123px' })

  const fullscreenTarget = dialog.querySelector('.dataset-template-canvas') as HTMLElement
  const originalFullscreenElement = Object.getOwnPropertyDescriptor(document, 'fullscreenElement')
  const originalExitFullscreen = Object.getOwnPropertyDescriptor(document, 'exitFullscreen')
  let fullscreenElement: Element | null = null
  Object.defineProperty(document, 'fullscreenElement', { configurable: true, get: () => fullscreenElement })
  Object.defineProperty(fullscreenTarget, 'requestFullscreen', { configurable: true, value: vi.fn(async () => {
    fullscreenElement = fullscreenTarget
    document.dispatchEvent(new Event('fullscreenchange'))
  }) })
  Object.defineProperty(document, 'exitFullscreen', { configurable: true, value: vi.fn(async () => {
    fullscreenElement = null
    document.dispatchEvent(new Event('fullscreenchange'))
  }) })

  await user.click(within(dialog).getByRole('button', { name: '全屏' }))
  expect(fullscreenTarget).toHaveClass('is-fullscreen')
  expect(within(dialog).getByRole('button', { name: '退出全屏' })).toHaveAttribute('aria-pressed', 'true')
  await user.click(within(dialog).getByRole('button', { name: '退出全屏' }))
  expect(fullscreenTarget).not.toHaveClass('is-fullscreen')

  await user.click(within(dialog).getByRole('button', { name: '配置关联 1' }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '保存整理布局')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '验证整理后的画布坐标写入版本')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))
  const designer = create.mock.calls[0][0].designer
  expect(designer?.nodePositions).toEqual({ node_1: { x: 42, y: 48 }, node_2: { x: 42, y: 198 } })
  expect(designer?.joins[0].position).toEqual({ x: 342, y: 123 })
  expect(designer?.end?.position).toEqual({ x: 642, y: 123 })

  if (originalFullscreenElement) Object.defineProperty(document, 'fullscreenElement', originalFullscreenElement)
  else delete (document as { fullscreenElement?: Element | null }).fullscreenElement
  if (originalExitFullscreen) Object.defineProperty(document, 'exitFullscreen', originalExitFullscreen)
  else delete (document as { exitFullscreen?: () => Promise<void> }).exitFullscreen
})

test('可以拖线连接节点且曲线按真实输入输出热区对齐但不显示端点圆圈', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table, customerTable] })
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === customerTable.id ? customerColumns : columns }))
  const rect = (left = 0, top = 0, width = 0, height = 0) => ({
    x: left, y: top, left, top, width, height, right: left + width, bottom: top + height,
    toJSON: () => ({}),
  }) as DOMRect
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const element = this as HTMLElement
    if (element.classList.contains('dataset-component-lines')) return rect(100, 200, 1400, 800)
    if (element.dataset.sourceKey === 'JOIN:join_1') return rect(755, 360, 10, 10)
    if (element.getAttribute('aria-label') === '连接到结束节点输入槽位') return rect(900, 350, 95, 80)
    return rect()
  })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户业务库/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户表/ }))
  await user.click(within(dialog).getByRole('button', { name: /关联组件双输入/ }))

  const values = new Map<string, string>()
  const dataTransfer = { setData: (type: string, value: string) => values.set(type, value), getData: (type: string) => values.get(type) ?? '' }
  const canvas = within(dialog).getByLabelText('关系组件画布')
  const firstSource = within(dialog).getByRole('button', { name: '从数据节点 1 拖出连接' })
  fireEvent.dragStart(firstSource, { dataTransfer })
  fireEvent.dragOver(canvas, { clientX: 420, clientY: 180, dataTransfer })
  expect(canvas.querySelector('.dataset-component-lines path.preview')).toBeInTheDocument()
  fireEvent.drop(within(dialog).getByRole('button', { name: '连接到关联节点 1 槽位 1' }), { dataTransfer })
  fireEvent.dragEnd(firstSource, { dataTransfer })

  values.clear()
  const secondSource = within(dialog).getByRole('button', { name: '从数据节点 2 拖出连接' })
  fireEvent.dragStart(secondSource, { dataTransfer })
  fireEvent.drop(within(dialog).getByRole('button', { name: '连接到关联节点 1 槽位 2' }), { dataTransfer })
  fireEvent.dragEnd(secondSource, { dataTransfer })
  connectByLine(dialog, { kind: 'JOIN', id: 'join_1' }, within(dialog).getByRole('button', { name: '连接到结束节点输入槽位' }))

  expect(within(dialog).getByLabelText('关联槽位 1')).toHaveTextContent('数据节点 · 订单表 (t1)')
  expect(within(dialog).getByLabelText('关联槽位 2')).toHaveTextContent('数据节点 · 客户表 (t2)')
  await user.click(within(dialog).getByRole('button', { name: '预览关联组件 1数据' }))
  const joinPreview = await within(dialog).findByRole('dialog', { name: '关联结果 1数据预览' })
  expect(await within(joinPreview).findByText('组件预览样本')).toBeInTheDocument()
  const connectionPaths = [...canvas.querySelectorAll<SVGPathElement>('.dataset-component-lines > path')]
  expect(connectionPaths).toHaveLength(3)
  for (const path of connectionPaths) {
    expect(path.getAttribute('d')).toContain(' C ')
    expect(path).not.toHaveAttribute('marker-start')
    expect(path).toHaveAttribute('marker-end', 'url(#dataset-edge-arrow)')
  }
  const joinOutput = within(dialog).getByRole('button', { name: '从关联节点 1 拖出连接' })
  const joinOutputPath = canvas.querySelector<SVGPathElement>('.dataset-flow-edge[data-source-key="JOIN:join_1"]')
  expect(joinOutput).toHaveAttribute('data-source-key', 'JOIN:join_1')
  expect(joinOutput).toHaveClass('component-side', 'output-side')
  expect(joinOutputPath?.getAttribute('d')).toMatch(/^M 664 165 C /)
  expect(joinOutputPath?.getAttribute('d')).toMatch(/, 800 190$/)
  await user.click(within(dialog).getByRole('button', { name: '删除关联节点 1 槽位 1 连线' }))
  expect(within(dialog).getByLabelText('关联槽位 1')).toHaveTextContent('尚未连接')
  expect(canvas.querySelectorAll('.dataset-component-lines > path')).toHaveLength(2)
})

test('分组组件可以作为关联节点输入并继续拖线', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table, customerTable] })
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === customerTable.id ? customerColumns : columns }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /分组组件可添加多个/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  const groupDrawer = within(dialog).getByLabelText('配置分组组件')
  await user.click(within(groupDrawer).getByLabelText('分组维度 t1_order_id'))
  await user.click(within(groupDrawer).getByLabelText('聚合指标 t1_amount'))
  await user.selectOptions(within(groupDrawer).getByLabelText('t1_amount 聚合逻辑'), 'SUM')
  await user.click(within(dialog).getByRole('button', { name: '预览分组组件 1数据' }))
  const groupPreview = await within(dialog).findByRole('dialog', { name: '分组结果 1数据预览' })
  expect(await within(groupPreview).findByText('组件预览样本')).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '从分组组件 1 拖出连接' })).toBeEnabled()
  await user.click(within(groupDrawer).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /客户业务库/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await addRelationBox(dialog, user, { kind: 'GROUP', id: 'group_1' }, { kind: 'NODE', id: 'node_2' })

  expect(within(dialog).getByLabelText('关联槽位 1')).toHaveTextContent('分组结果 1')
  expect(within(dialog).getByLabelText('条件 1 左字段')).toHaveTextContent('分组结果 1 / 订单编号 · t1_order_id · 维度')
  expect(within(dialog).getByLabelText('条件 1 左字段')).not.toHaveTextContent('订单备注')
  expect(within(dialog).getByLabelText('关系组件画布').querySelectorAll('.dataset-component-lines > path')).toHaveLength(4)
})

test('字段处理产物可作为关联输入且不会改变原有关联字段语义', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table, customerTable] })
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === customerTable.id ? customerColumns : columns }))
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /字段截取按起始位置截取文本/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '打开字段截取 1 配置' }))
  const transformDrawer = within(dialog).getByLabelText('配置字段截取')
  await user.clear(within(transformDrawer).getByLabelText('规则 1 输出编码'))
  await user.type(within(transformDrawer).getByLabelText('规则 1 输出编码'), 'order_short')
  await user.click(within(transformDrawer).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /客户业务库/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await addRelationBox(dialog, user, { kind: 'TRANSFORM', id: 'transform_1' }, { kind: 'NODE', id: 'node_2' })

  expect(within(dialog).getByLabelText('关联槽位 1')).toHaveTextContent('字段截取 1')
  expect(within(dialog).getByLabelText('条件 1 左字段')).not.toHaveTextContent('order_short')
  expect(within(dialog).queryByText(/阶段 A 的字段处理结果暂不能作为关联输入/)).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  connectByLine(dialog, { kind: 'JOIN', id: 'join_1' }, within(dialog).getByRole('button', { name: '打开结束节点配置' }))
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '字段处理后关联')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '验证字段处理产物可继续进入关联组件')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const dsl = create.mock.calls[0][0]
  expect(dsl.designer?.joins[0].left).toEqual({ kind: 'TRANSFORM', id: 'transform_1' })
  expect((dsl.fields as Array<Record<string, unknown>>).find(field => field.code === 'order_short')).toMatchObject({
    expression: { type: 'SUBSTRING' },
  })
})

test('分组产物可以继续进入字段处理并保存聚合表达式', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /分组组件可添加多个/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  const groupDrawer = within(dialog).getByLabelText('配置分组组件')
  await user.click(within(groupDrawer).getByLabelText('分组维度 t1_order_id'))
  await user.click(within(groupDrawer).getByLabelText('聚合指标 t1_amount'))
  await user.selectOptions(within(groupDrawer).getByLabelText('t1_amount 聚合逻辑'), 'SUM')
  await user.click(within(groupDrawer).getByRole('button', { name: '完成' }))

  await user.click(within(dialog).getByRole('button', { name: /类型转换规范字段的数据类型/ }))
  connectByLine(dialog, { kind: 'GROUP', id: 'group_1' }, within(dialog).getByRole('button', { name: '连接到类型转换 1 输入槽位' }))
  const transformDrawer = within(dialog).getByLabelText('配置类型转换')
  expect(within(transformDrawer).getByLabelText('字段处理组件输入')).toHaveTextContent('分组结果 1')
  expect(within(transformDrawer).getByLabelText('规则 1 输入字段 1')).toHaveValue('node_1.order_id')
  await user.click(within(transformDrawer).getByRole('button', { name: '完成' }))

  expect(within(dialog).queryByText('阶段 A 的字段处理暂不接收分组产物')).not.toBeInTheDocument()
  connectByLine(dialog, { kind: 'TRANSFORM', id: 'transform_1' }, within(dialog).getByRole('button', { name: '连接到结束节点输入槽位' }))
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '订单分组后转换')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '验证分组产物继续字段处理')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const dsl = create.mock.calls[0][0]
  expect(dsl.designer?.transforms?.[0].input).toEqual({ kind: 'GROUP', id: 'group_1' })
  expect(dsl.designer?.end?.input).toEqual({ kind: 'TRANSFORM', id: 'transform_1' })
  expect((dsl.fields as Array<Record<string, unknown>>).find(field => field.code === 't1_amount')).toMatchObject({
    role: 'MEASURE', expression: { type: 'AGGREGATE', function: 'SUM' },
  })
})

test('不完整分组可以随时关闭，但保存整个数据集时仍给出校验提示', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /分组组件可添加多个/ }))
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  let drawer = within(dialog).getByLabelText('配置分组组件')
  await user.click(within(drawer).getByLabelText('分组维度 t1_order_id'))

  await user.click(within(drawer).getByRole('button', { name: '完成' }))
  expect(within(dialog).queryByLabelText('配置分组组件')).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))
  expect(within(dialog).getByRole('alert')).toHaveTextContent('至少配置一个完整的聚合指标')

  await user.click(within(dialog).getByRole('button', { name: '打开分组组件 1 配置' }))
  drawer = within(dialog).getByLabelText('配置分组组件')
  await user.click(within(drawer).getByLabelText('聚合指标 t1_amount'))
  await user.click(within(drawer).getByRole('button', { name: '完成' }))
  expect(within(dialog).queryByLabelText('配置分组组件')).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))
  expect(within(dialog).getByRole('alert')).toHaveTextContent('至少配置一个完整的聚合指标')

  await user.click(within(dialog).getByRole('button', { name: '打开分组组件 1 配置' }))
  drawer = within(dialog).getByLabelText('配置分组组件')
  await user.selectOptions(within(drawer).getByLabelText('t1_amount 聚合逻辑'), 'SUM')
  await user.click(within(drawer).getByRole('button', { name: '完成' }))
  expect(within(dialog).queryByLabelText('配置分组组件')).not.toBeInTheDocument()
  connectByLine(dialog, { kind: 'GROUP', id: 'group_1' }, within(dialog).getByRole('button', { name: '连接到结束节点输入槽位' }))
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))
  expect(screen.getByRole('dialog', { name: '完善数据集信息' })).toBeInTheDocument()
})

test('画布阻止形成循环依赖并保持原有 DAG 连线', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  const assetButton = within(dialog).getByRole('button', { name: /订单表.*已映射/ })
  for (let index = 0; index < 3; index += 1) {
    await user.click(assetButton)
    await user.click(within(dialog).getByRole('button', { name: '完成' }))
  }
  await addRelationBox(dialog, user, { kind: 'NODE', id: 'node_1' }, { kind: 'NODE', id: 'node_2' })
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await addRelationBox(dialog, user, { kind: 'JOIN', id: 'join_1' }, { kind: 'NODE', id: 'node_3' })

  connectByLine(dialog, { kind: 'JOIN', id: 'join_2' }, within(dialog).getByRole('button', { name: '连接到关联节点 1 槽位 1' }))
  expect(within(dialog).getByRole('alert')).toHaveTextContent('形成循环依赖')

  await user.click(within(dialog).getByRole('button', { name: '配置关联 1' }))
  expect(within(dialog).getByLabelText('关联槽位 1')).toHaveTextContent('数据节点 · 订单表 (t1)')
})

test('支持多个命名分组产物并由结束节点定义最终输出', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table, customerTable] })
  vi.spyOn(datasetAPI, 'columns').mockImplementation(async tableID => ({ items: tableID === customerTable.id ? customerColumns : columns }))
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const create = vi.spyOn(datasetAPI, 'create').mockResolvedValue(record())
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.click(within(dialog).getByRole('button', { name: /订单表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: /客户业务库/ }))
  await user.click(within(dialog).getByRole('button', { name: /客户表/ }))
  await user.click(within(dialog).getByRole('button', { name: '完成' }))

  const addGroup = within(dialog).getByRole('button', { name: /分组组件可添加多个/ })
  await user.click(addGroup)
  let drawer = within(dialog).getByLabelText('配置分组组件')
  await user.clear(within(drawer).getByLabelText('分组产物名称'))
  await user.type(within(drawer).getByLabelText('分组产物名称'), '订单聚合')
  connectByLine(dialog, { kind: 'NODE', id: 'node_1' }, within(dialog).getByRole('button', { name: '连接到分组组件 1 输入槽位' }))
  drawer = within(dialog).getByLabelText('配置分组组件')
  expect(within(drawer).getByLabelText('分组组件输入')).toHaveTextContent('数据节点 · 订单表 (t1)')
  await user.click(within(drawer).getByLabelText('分组维度 t1_order_id'))
  await user.click(within(drawer).getByLabelText('聚合指标 t1_amount'))
  await user.selectOptions(within(drawer).getByLabelText('t1_amount 聚合逻辑'), 'SUM')
  expect(within(drawer).getByLabelText('t1_amount 字段别名')).toHaveTextContent('t1_amount')
  await user.click(within(drawer).getByRole('button', { name: '完成' }))

  await user.click(addGroup)
  drawer = within(dialog).getByLabelText('配置分组组件')
  await user.clear(within(drawer).getByLabelText('分组产物名称'))
  await user.type(within(drawer).getByLabelText('分组产物名称'), '客户聚合')
  connectByLine(dialog, { kind: 'NODE', id: 'node_2' }, within(dialog).getByRole('button', { name: '连接到分组组件 2 输入槽位' }))
  drawer = within(dialog).getByLabelText('配置分组组件')
  expect(within(drawer).getByLabelText('分组组件输入')).toHaveTextContent('数据节点 · 客户表 (t2)')
  await user.click(within(drawer).getByLabelText('分组维度 t2_customer_id'))
  await user.click(within(drawer).getByLabelText('聚合指标 t2_customer_name'))
  await user.selectOptions(within(drawer).getByLabelText('t2_customer_name 聚合逻辑'), 'COUNT')
  await user.click(within(drawer).getByRole('button', { name: '完成' }))

  expect(within(dialog).getAllByLabelText(/打开分组组件 \d 配置/)).toHaveLength(2)
  await addRelationBox(dialog, user, { kind: 'GROUP', id: 'group_1' }, { kind: 'GROUP', id: 'group_2' })
  const joinDrawer = within(dialog).getByLabelText('配置表关联')
  expect(within(joinDrawer).getByLabelText('条件 1 左字段')).toHaveTextContent('订单聚合 / 订单编号 · t1_order_id · 维度')
  expect(within(joinDrawer).getByLabelText('条件 1 左字段')).toHaveTextContent('订单聚合 / 订单金额 · t1_amount · SUM 指标')
  await user.click(within(joinDrawer).getByRole('button', { name: '完成' }))
  connectByLine(dialog, { kind: 'JOIN', id: 'join_1' }, within(dialog).getByRole('button', { name: '连接到结束节点输入槽位' }))

  await user.click(within(dialog).getByRole('button', { name: '打开结束节点配置' }))
  const endDrawer = within(dialog).getByLabelText('配置结束节点')
  expect(within(endDrawer).getByLabelText('结束节点输入')).toHaveTextContent('关联结果 1')
  const orderOutput = within(endDrawer).getByLabelText('最终输出 t1_order_id')
  expect(orderOutput).toBeChecked()
  await user.click(orderOutput)
  expect(within(endDrawer).queryByLabelText('t1_order_id 字段别名')).not.toBeInTheDocument()
  await user.click(orderOutput)
  expect(within(endDrawer).getByLabelText('t1_order_id 字段别名')).toHaveTextContent('t1_order_id')
  expect(within(endDrawer).getByLabelText('t1_order_id 字段别名').tagName).toBe('OUTPUT')
  expect(within(endDrawer).queryByLabelText('t1_order_id 最终名称')).not.toBeInTheDocument()
  expect(within(endDrawer).queryByLabelText('t1_order_id 最终编码')).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: '预览结束节点数据' }))
  const endPreview = await within(dialog).findByRole('dialog', { name: '最终输出数据预览' })
  expect(await within(endPreview).findByText('组件预览样本')).toBeInTheDocument()
  expect(datasetAPI.previewCandidate).toHaveBeenCalledWith(expect.objectContaining({
    designer: expect.objectContaining({ end: expect.objectContaining({ input: { kind: 'JOIN', id: 'join_1' } }) }),
  }), expect.any(String), {}, 5)
  await user.click(within(endDrawer).getByRole('button', { name: '完成' }))
  await user.click(within(dialog).getByRole('button', { name: '保存配置' }))

  const metadataDialog = screen.getByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), '双侧聚合结果')
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '两张表分别聚合后再关联')
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const dsl = create.mock.calls[0][0]
  expect(dsl.preAggregations).toHaveLength(2)
  expect(dsl.designer?.groups).toHaveLength(2)
  expect(dsl.designer?.groups.map(group => group.name)).toEqual(['订单聚合', '客户聚合'])
  expect(dsl.designer?.end?.outputs.find(output => output.key === 'node_1.order_id')).toMatchObject({ name: '订单编号', code: 't1_order_id' })
})

test('历史版本支持查看精确快照并二次确认回滚', async () => {
  const currentVersion = publishedDatasetVersion({
    id: 'published-version-current', versionNo: 4, status: 'PUBLISHED',
    draftVersionId: 'draft-current', draftRecordVersion: 4,
  })
  const staleVersion = publishedDatasetVersion({
    id: 'published-version-stale', versionNo: 3, status: 'STALE',
    dslHash: 'c'.repeat(64), planHash: 'd'.repeat(64), draftVersionId: 'draft-old', draftRecordVersion: 3,
    publishedAt: '2026-07-16T01:00:00Z', publishedBy: 'approver-old', datasetRecordVersion: 3,
  })
  const deprecatedVersion = publishedDatasetVersion({
    id: 'published-version-deprecated', versionNo: 2, status: 'DEPRECATED',
    dslHash: 'e'.repeat(64), planHash: 'f'.repeat(64), draftVersionId: 'draft-deprecated', draftRecordVersion: 2,
    publishedAt: '2026-07-15T01:00:00Z', datasetRecordVersion: 2,
  })
  const restored = record({
    version: 5, draftVersionId: 'draft-rollback', draftRecordVersion: 5,
    dslHash: staleVersion.dslHash, planHash: staleVersion.planHash, currentPublishedVersionId: currentVersion.id,
    updatedAt: '2026-07-18T01:00:00Z',
  })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary({ currentPublishedVersionId: currentVersion.id })]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(record({
    currentPublishedVersionId: currentVersion.id, draftVersionId: currentVersion.draftVersionId,
    draftRecordVersion: currentVersion.draftRecordVersion,
  }))
  const listVersions = vi.spyOn(datasetAPI, 'listVersions').mockResolvedValue({
    items: [currentVersion, staleVersion, deprecatedVersion], total: 3, limit: 200, offset: 0,
  })
  vi.spyOn(datasetAPI, 'getVersion').mockImplementation(async (_datasetID, versionID) => {
    if (versionID === staleVersion.id) return staleVersion
    if (versionID === deprecatedVersion.id) return deprecatedVersion
    return currentVersion
  })
  const previewVersion = vi.spyOn(datasetAPI, 'previewVersion').mockResolvedValue({
    queryId: 'version-preview', columns: ['order_id', 'amount'],
    rows: [['P001', 10], ['P002', 20], ['P003', 30], ['P004', 40], ['P005', 50]],
    rowCount: 5, durationMs: 4,
  })
  const rollback = vi.spyOn(datasetAPI, 'rollbackVersion').mockResolvedValue(restored)
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('订单明细')
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '历史版本' }))
  const historyDialog = await screen.findByRole('dialog', { name: '订单明细 · 历史版本' })
  const publishedVersionList = within(historyDialog).getByLabelText('数据集发布版本列表')
  expect(within(publishedVersionList).getByText('已发布')).toBeInTheDocument()
  expect(within(publishedVersionList).getByText('已失效')).toBeInTheDocument()
  expect(within(publishedVersionList).getByText('已废弃')).toBeInTheDocument()
  expect(within(publishedVersionList).getAllByRole('button')).toHaveLength(3)
  expect(within(historyDialog).getByRole('button', { name: '回滚到此版本' })).toBeDisabled()
  await user.click(within(historyDialog).getByRole('button', { name: /V3/ }))
  expect(within(historyDialog).getByText('R3')).toBeInTheDocument()
  expect(within(historyDialog).getByLabelText('发布 V3 画布排布')).toBeInTheDocument()
  expect(within(historyDialog).getByText('approver-old')).toBeInTheDocument()
  expect(within(historyDialog).getByText(staleVersion.id)).toBeInTheDocument()
  expect(within(historyDialog).getByText('P005')).toBeInTheDocument()
  expect(previewVersion).toHaveBeenCalledWith('dataset-1', staleVersion.id, expect.any(String), {}, 5)
  await user.click(within(historyDialog).getByRole('button', { name: '回滚到此版本' }))
  expect(within(historyDialog).getByLabelText('确认回滚发布版本')).toHaveTextContent('确认回滚到发布 V3')
  await user.click(within(historyDialog).getByRole('button', { name: '确认回滚' }))

  expect(rollback).toHaveBeenCalledWith('dataset-1', staleVersion.id, 4)
  expect(await screen.findByRole('status')).toHaveTextContent('已将发布 V3 回滚为新的当前配置 V5')
  expect(listVersions).toHaveBeenCalledTimes(1)
  expect(within(publishedVersionList).getAllByRole('button')).toHaveLength(3)
  expect(within(historyDialog).queryByText('R5')).not.toBeInTheDocument()
  expect(within(cardFor('订单明细')).getByText('V5')).toBeInTheDocument()
})

test('后端判定发布版本缺少源草稿快照时展示错误且保持发布列表不变', async () => {
  const legacyVersion = publishedDatasetVersion({
    id: 'published-version-legacy', versionNo: 3, status: 'STALE',
    dslHash: 'c'.repeat(64), planHash: 'd'.repeat(64), draftVersionId: 'draft-missing', draftRecordVersion: 3,
  })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary()]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(record())
  const listVersions = vi.spyOn(datasetAPI, 'listVersions').mockResolvedValue({ items: [legacyVersion], total: 1, limit: 200, offset: 0 })
  vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(legacyVersion)
  vi.spyOn(datasetAPI, 'previewVersion').mockResolvedValue({ queryId: 'legacy-preview', columns: [], rows: [], rowCount: 0, durationMs: 1 })
  const rollback = vi.spyOn(datasetAPI, 'rollbackVersion').mockRejectedValue(new RequestError({
    code: 'DATASET_VERSION_ROLLBACK_UNAVAILABLE', message: '发布版本缺少唯一且可验证的源草稿修订，无法安全回滚',
  }, 409))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('订单明细')
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '历史版本' }))
  const historyDialog = await screen.findByRole('dialog', { name: '订单明细 · 历史版本' })
  const publishedVersionList = within(historyDialog).getByLabelText('数据集发布版本列表')
  await user.click(within(historyDialog).getByRole('button', { name: '回滚到此版本' }))
  await user.click(within(historyDialog).getByRole('button', { name: '确认回滚' }))

  expect(await within(historyDialog).findByRole('alert')).toHaveTextContent('发布版本缺少唯一且可验证的源草稿修订，无法安全回滚')
  expect(rollback).toHaveBeenCalledWith('dataset-1', legacyVersion.id, 4)
  expect(listVersions).toHaveBeenCalledTimes(1)
  expect(within(publishedVersionList).getAllByRole('button')).toHaveLength(1)
})

test('后端判定发布版本源草稿快照歧义时展示错误且保持发布列表不变', async () => {
  const published = publishedDatasetVersion({
    id: 'published-version-ambiguous', versionNo: 3, status: 'STALE',
    dslHash: 'c'.repeat(64), planHash: 'd'.repeat(64), draftVersionId: 'draft-ambiguous', draftRecordVersion: 3,
  })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary()]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(record())
  const listVersions = vi.spyOn(datasetAPI, 'listVersions').mockResolvedValue({ items: [published], total: 1, limit: 200, offset: 0 })
  vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(published)
  vi.spyOn(datasetAPI, 'previewVersion').mockResolvedValue({ queryId: 'ambiguous-preview', columns: [], rows: [], rowCount: 0, durationMs: 1 })
  const rollback = vi.spyOn(datasetAPI, 'rollbackVersion').mockRejectedValue(new RequestError({
    code: 'DATASET_VERSION_ROLLBACK_UNAVAILABLE', message: '发布版本缺少唯一且可验证的源草稿修订，无法安全回滚',
  }, 409))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('订单明细')
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '历史版本' }))
  const historyDialog = await screen.findByRole('dialog', { name: '订单明细 · 历史版本' })
  const publishedVersionList = within(historyDialog).getByLabelText('数据集发布版本列表')
  await user.click(within(historyDialog).getByRole('button', { name: '回滚到此版本' }))
  await user.click(within(historyDialog).getByRole('button', { name: '确认回滚' }))

  expect(await within(historyDialog).findByRole('alert')).toHaveTextContent('发布版本缺少唯一且可验证的源草稿修订，无法安全回滚')
  expect(rollback).toHaveBeenCalledWith('dataset-1', published.id, 4)
  expect(listVersions).toHaveBeenCalledTimes(1)
  expect(within(publishedVersionList).getAllByRole('button')).toHaveLength(1)
})

test('可查看、停用、恢复并二次确认删除数据集', async () => {
  vi.spyOn(datasetAPI, 'list')
    .mockResolvedValueOnce(page([summary()]))
    .mockResolvedValueOnce(page([summary({ status: 'DISABLED', version: 5, currentPublishedVersionId: undefined })]))
    .mockResolvedValueOnce(page([summary({ status: 'PUBLISHED', version: 6, currentPublishedVersionId: 'version-1' })]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(record())
  const preview = vi.spyOn(datasetAPI, 'preview').mockResolvedValue({ queryId: 'query-1', columns: ['order_id', 'amount'], rows: [
    ['A001', 10], ['A002', 20], ['A003', 30], ['A004', 40], ['A005', 50], ['A006', 60],
  ], rowCount: 6, durationMs: 8 })
  const disable = vi.spyOn(datasetAPI, 'disable').mockResolvedValue(record({ status: 'DISABLED', version: 5, currentPublishedVersionId: undefined }))
  const restore = vi.spyOn(datasetAPI, 'restore').mockResolvedValue(record({ status: 'PUBLISHED', version: 6, currentPublishedVersionId: 'version-1' }))
  const remove = vi.spyOn(datasetAPI, 'delete').mockResolvedValue()
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('订单明细')
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '查看' }))
  const detailDialog = await screen.findByRole('dialog', { name: '数据集详情' })
  expect(detailDialog).toHaveTextContent('订单业务明细数据')
  expect(preview).toHaveBeenCalledWith('dataset-1', expect.any(String), {}, 5)
  expect(within(detailDialog).getAllByRole('row')).toHaveLength(6)
  expect(within(detailDialog).queryByText('A006')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: '关闭' }))
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '停用' }))
  const disableDialog = screen.getByRole('dialog', { name: '停用数据集' })
  await user.click(within(disableDialog).getByRole('button', { name: '确认停用' }))
  expect(disable).toHaveBeenCalledWith('dataset-1', 4)
  expect(await within(cardFor('订单明细')).findByText('已停用')).toBeInTheDocument()
  expect(within(cardFor('订单明细')).queryByRole('button', { name: '停用' })).not.toBeInTheDocument()
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '恢复' }))
  const restoreDialog = screen.getByRole('dialog', { name: '恢复数据集' })
  expect(restoreDialog).toHaveTextContent('优先恢复到停用前')
  await user.click(within(restoreDialog).getByRole('button', { name: '确认恢复' }))
  expect(restore).toHaveBeenCalledWith('dataset-1', 5)
  expect(await within(cardFor('订单明细')).findByText('已发布')).toBeInTheDocument()
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '删除' }))
  const deleteDialog = screen.getByRole('dialog', { name: '删除数据集' })
  await user.click(within(deleteDialog).getByRole('button', { name: '确认删除' }))
  expect(remove).toHaveBeenCalledWith('dataset-1', 6)
  expect(await screen.findByText('还没有数据集')).toBeInTheDocument()
})

test('发布按钮冻结当前草稿，审批通过后才生成精确发布版本', async () => {
  const pending = publicationRequest({ requestNote: '用于月度区域销售额指标' })
  const approved = publicationRequest({
    status: 'APPROVED', version: 2, requestNote: pending.requestNote, reviewerId: 'approver-1',
    reviewNote: '校验通过', publishedVersionId: 'published-version-2', reviewedAt: '2026-07-20T10:05:00Z',
  })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary({ status: 'DRAFT', currentPublishedVersionId: undefined })]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(record({ status: 'DRAFT', currentPublishedVersionId: undefined }))
  vi.spyOn(datasetAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  vi.spyOn(datasetAPI, 'listPublicationRequests')
    .mockResolvedValueOnce({ items: [], total: 0, limit: 50, offset: 0 })
    .mockResolvedValueOnce({ items: [pending], total: 1, limit: 50, offset: 0 })
    .mockResolvedValueOnce({ items: [approved], total: 1, limit: 50, offset: 0 })
  const submit = vi.spyOn(datasetAPI, 'requestPublication').mockResolvedValue(pending)
  const approve = vi.spyOn(datasetAPI, 'approvePublication').mockResolvedValue({ request: approved, publishedVersion: publishedDatasetVersion() })
  const user = userEvent.setup()
  renderPage()

  await screen.findByRole('heading', { level: 3, name: '订单明细' })
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '发布' }))
  const dialog = await screen.findByRole('dialog', { name: '订单明细 · 发布审批' })
  await user.type(within(dialog).getByLabelText('申请说明（选填）'), pending.requestNote)
  await user.click(within(dialog).getByRole('button', { name: '提交发布审批' }))

  expect(submit).toHaveBeenCalledWith('dataset-1', {
    draftVersionId: 'draft-1', expectedVersion: 4, expectedDraftRecordVersion: 2,
    expectedDslHash: 'a'.repeat(64), validationParameters: {},
  }, pending.requestNote)
  expect(await within(dialog).findByText('当前精确草稿已经在审批中，无需重复提交。')).toBeInTheDocument()
  await user.type(within(dialog).getByLabelText('审批意见'), '校验通过')
  await user.click(within(dialog).getByRole('button', { name: '审批通过并发布' }))

  expect(approve).toHaveBeenCalledWith('dataset-1', pending.id, pending.version, '校验通过')
  expect(await within(dialog).findByText('当前精确草稿已审批发布。再次修改并保存后可提交新的审批。')).toBeInTheDocument()
  expect(within(dialog).getByText(/指标候选自动提取中/)).toBeInTheDocument()
  expect(within(dialog).getByText('published-version-2')).toBeInTheDocument()
})

test('指标改造数据集审批完成后可带回原始需求继续生成', async () => {
  const requirement = '基于订单表和客户表，创建一个月度各区域销售额的指标'
  const approved = publicationRequest({ status: 'APPROVED', publishedVersionId: 'published-version-2' })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary({ status: 'PUBLISHED' })]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(record({ status: 'PUBLISHED', currentPublishedVersionId: 'published-version-2' }))
  vi.spyOn(datasetAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  vi.spyOn(datasetAPI, 'listPublicationRequests').mockResolvedValue({ items: [approved], total: 1, limit: 50, offset: 0 })
  const user = userEvent.setup()
  render(<MemoryRouter initialEntries={[{
    pathname: '/datasets', state: { returnTo: '/metrics/new', metricAIRequirement: requirement },
  }]}><Routes>
    <Route path="/datasets" element={<DatasetCenterPage />} />
    <Route path="/metrics/new" element={<RouteLocationProbe />} />
  </Routes></MemoryRouter>)

  await screen.findByRole('heading', { level: 3, name: '订单明细' })
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '发布' }))
  const dialog = await screen.findByRole('dialog', { name: '订单明细 · 发布审批' })
  await user.click(await within(dialog).findByRole('button', { name: '返回指标中心继续生成' }))

  const route = JSON.parse((await screen.findByLabelText('当前路由')).textContent || '{}')
  expect(route).toEqual({ pathname: '/metrics/new', state: { metricAIRequirement: requirement } })
})

test('修改数据集继续使用配置中心画板并保存当前版本', async () => {
  const editable = record({
    dsl: {
      dslVersion: '1.0',
      dataset: { code: 'orders_detail', name: '订单明细', description: '订单业务明细数据', type: 'SINGLE_SOURCE' },
      nodes: [{ id: 'node_1', type: 'TABLE', dataSourceId: table.dataSourceId, tableId: table.id, alias: 't1', projection: ['order_id', 'amount'] }],
      fields: [
        { id: 'field_order_id', code: 'order_id', name: '订单编号', role: 'IDENTIFIER', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'order_id' }, canonicalType: 'STRING', nullable: false, visible: true },
        { id: 'field_amount', code: 'amount', name: '订单金额', role: 'MEASURE', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' }, canonicalType: 'DECIMAL', nullable: false, visible: true },
      ],
      joins: [], filters: [], parameters: [], groupBy: [], sorts: [], outputGrain: { description: '每行一笔订单', keyFields: ['order_id'] },
    },
  })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary()]))
  const persisted = record({
    ...editable,
    version: 5,
    description: '更新后的订单业务说明',
    dsl: {
      ...editable.dsl,
      dataset: { ...editable.dsl.dataset, description: '更新后的订单业务说明' },
    },
  })
  vi.spyOn(datasetAPI, 'get').mockResolvedValueOnce(editable).mockResolvedValueOnce(persisted)
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const preview = vi.spyOn(datasetAPI, 'previewDraft').mockResolvedValue({
    queryId: 'end-preview', dslHash: 'c'.repeat(64), planHash: 'd'.repeat(64), baseVersion: 4, columns: ['order_id', 'amount'],
    rows: [['P001', 10], ['P002', 20], ['P003', 30], ['P004', 40], ['P005', 50], ['P006', 60]],
    rowCount: 6, durationMs: 5,
  })
  const update = vi.spyOn(datasetAPI, 'update').mockResolvedValue(editable)
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('订单明细')
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '修改' }))
  const editDialog = await screen.findByRole('dialog', { name: '修改数据集' })
  expect(within(editDialog).getAllByText('订单表')).toHaveLength(2)
  expect(within(editDialog).getByText(/1 个数据节点 · 0 个关联 · 0 个分组 · 1 个结束节点/)).toBeInTheDocument()

  await user.click(within(editDialog).getByRole('button', { name: '预览结束节点数据' }))
  let previewDialog = await within(editDialog).findByRole('dialog', { name: '最终输出数据预览' })
  expect(await within(previewDialog).findByText('P005')).toBeInTheDocument()
  expect(within(previewDialog).queryByText('P006')).not.toBeInTheDocument()
  expect(preview).toHaveBeenCalledWith('dataset-1', 4, expect.objectContaining({
    dataset: expect.objectContaining({ code: 'orders_detail' }),
  }), expect.any(String), {}, 5)
  expect(within(previewDialog).getByRole('button', { name: '刷新' })).toBeInTheDocument()
  await user.click(within(previewDialog).getByRole('button', { name: '关闭组件数据预览' }))

  await user.click(within(editDialog).getByRole('button', { name: '打开结束节点配置' }))
  const endDrawer = within(editDialog).getByLabelText('配置结束节点')
  expect(within(endDrawer).getByLabelText('order_id 字段别名').tagName).toBe('OUTPUT')
  await user.click(within(endDrawer).getByRole('button', { name: '完成' }))

  preview.mockRejectedValue(new RequestError({ code: 'QUERY-004-EXECUTION-FAILED', message: '数据源查询执行失败' }, 502))
  await user.click(within(editDialog).getByRole('button', { name: '预览结束节点数据' }))
  previewDialog = await within(editDialog).findByRole('dialog', { name: '最终输出数据预览' })
  const diagnostic = await within(previewDialog).findByRole('alert')
  expect(diagnostic).toHaveTextContent('异常原因')
  expect(diagnostic).toHaveTextContent('数据源查询执行失败')
  expect(diagnostic).toHaveTextContent('处理建议')
  expect(diagnostic).toHaveTextContent(/连接|凭据|物理表/)
  await user.click(within(previewDialog).getByRole('button', { name: '关闭组件数据预览' }))

  await user.click(within(editDialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = screen.getByRole('dialog', { name: '保存数据集修改' })
  await user.clear(within(metadataDialog).getByLabelText('数据集说明'))
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), '更新后的订单业务说明')
  await user.click(within(metadataDialog).getByRole('button', { name: '保存修改' }))

  expect(update).toHaveBeenCalledWith('dataset-1', 4, expect.objectContaining({ name: '订单明细', description: '更新后的订单业务说明', code: 'orders_detail' }), expect.any(Object))
  expect(datasetAPI.get).toHaveBeenCalledTimes(2)
  expect(await screen.findByRole('status')).toHaveTextContent('已保存“订单明细”的最新配置')
})

test('AI 可以从空画布生成方案，校验后原子应用并一键撤销', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const validate = vi.spyOn(datasetAPI, 'validate')
    .mockRejectedValueOnce(new Error('校验服务暂时不可用'))
    .mockResolvedValueOnce({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ requestId: 'ai-request-1', proposal: aiProposal() }),
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  expect(fetchMock).not.toHaveBeenCalled()
  const toolbar = within(dialog).getByRole('toolbar', { name: 'AI 方案操作' })
  const toolbarButtons = within(toolbar).getAllByRole('button')
  expect(toolbarButtons.map(button => button.textContent)).toEqual(['展开', '折叠', '应用', '撤销', '重试'])
  toolbarButtons.forEach(button => expect(button).toBeDisabled())
  const prompt = within(dialog).getByLabelText('描述数据集生成或修改目标')
  expect(prompt.tagName).toBe('TEXTAREA')
  expect(prompt).toHaveAttribute('rows', '1')
  await user.type(prompt, '第一行要求{enter}第二行要求')
  expect(prompt).toHaveValue('第一行要求\n第二行要求')
  await user.clear(prompt)
  await user.type(prompt, '生成订单明细，保留订单编号和金额')
  await user.click(within(dialog).getByRole('button', { name: 'AI 生成流程' }))

  const proposalHeading = await within(dialog).findByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })
  const proposalCard = proposalHeading.closest('article') as HTMLElement
  expect(within(proposalCard).queryAllByRole('button')).toHaveLength(0)
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/datasets/ai/proposals', expect.objectContaining({
    method: 'POST',
    body: JSON.stringify({ instruction: '生成订单明细，保留订单编号和金额' }),
  }))
  const expand = within(toolbar).getByRole('button', { name: '展开' })
  const collapse = within(toolbar).getByRole('button', { name: '折叠' })
  const apply = within(toolbar).getByRole('button', { name: '应用' })
  const undo = within(toolbar).getByRole('button', { name: '撤销' })
  const retry = within(toolbar).getByRole('button', { name: '重试' })
  expect(expand).toBeDisabled()
  expect(collapse).toBeEnabled()
  expect(apply).toBeEnabled()
  expect(undo).toBeDisabled()
  expect(retry).toBeDisabled()
  expect(collapse).toHaveAttribute('aria-expanded', 'true')
  expect(collapse).toHaveAttribute('aria-controls')
  await user.click(collapse)
  expect(within(dialog).getByText('方案流程')).not.toBeVisible()
  expect(apply).toBeEnabled()
  expect(collapse).toBeDisabled()
  expect(expand).toBeEnabled()
  expect(expand).toHaveAttribute('aria-expanded', 'false')
  await user.click(expand)
  expect(within(dialog).getByText('方案流程')).toBeInTheDocument()
  expect(within(dialog).queryByRole('button', { name: /关闭.*方案/ })).not.toBeInTheDocument()
  fireEvent.mouseDown(within(dialog).getByLabelText('AI 自动配置数据流'))
  expect(screen.getByRole('dialog', { name: '新建数据集' })).toBeInTheDocument()
  await user.click(apply)

  expect(await within(dialog).findByRole('alert')).toHaveTextContent('校验服务暂时不可用')
  expect(within(dialog).getByText('选择第一张映射表开始建模')).toBeInTheDocument()
  expect(retry).toBeEnabled()
  await user.click(retry)

  expect(validate).toHaveBeenCalledTimes(2)
  expect(await within(dialog).findByText(/1 个数据节点 · 0 个关联 · 0 个分组 · 1 个结束节点/)).toBeInTheDocument()
  expect(within(dialog).getByText('已应用到画布')).toBeInTheDocument()
  expect(within(dialog).getByText('AI 方案已应用：使用订单表生成可直接预览的明细数据集')).toBeInTheDocument()
  expect(apply).toBeDisabled()
  expect(undo).toBeEnabled()
  expect(retry).toBeDisabled()
  expect(within(proposalCard).queryAllByRole('button')).toHaveLength(0)
  await user.click(undo)
  expect(await within(dialog).findByText('选择第一张映射表开始建模')).toBeInTheDocument()
  expect(within(dialog).getByText('已撤销本次 AI 方案，恢复到应用前的画布')).toBeInTheDocument()
  expect(within(dialog).queryByText('已应用到画布')).not.toBeInTheDocument()
  within(toolbar).getAllByRole('button').forEach(button => expect(button).toBeDisabled())
})

test('AI 方案中的字段处理组件会随候选图一起应用到画布', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const transformProposal = aiProposal({
    summary: '将订单编号转为大写后输出',
    plan: {
      dataset: { name: 'AI 订单编号', description: '验证字段处理组件应用到画布' },
      nodes: [{ id: 'node_1', tableId: table.id, alias: 'orders', selectedColumns: ['order_id', 'amount'] }],
      joins: [], groups: [],
      transforms: [{
        id: 'transform_1', name: '订单编号大写', family: 'TEXT', componentType: 'TEXT_UPPER', input: { kind: 'NODE', id: 'node_1' },
        rules: [{
          id: 'rule_1', operation: 'UPPER', inputKeys: ['node_1.order_id'],
          output: { id: 'order_id_upper', name: '大写订单编号', code: 'order_id_upper', canonicalType: 'STRING' },
        }],
      }],
      end: {
        name: '最终输出', input: { kind: 'TRANSFORM', id: 'transform_1' },
        outputs: [
          { nodeId: 'node_1', column: 'order_id', key: 'transform_1.order_id_upper', name: '大写订单编号', code: 'order_id_upper' },
          { nodeId: 'node_1', column: 'amount', name: '订单金额', code: 'amount' },
        ],
      },
    },
  })
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: true, status: 200, json: async () => ({ requestId: 'ai-transform-request', proposal: transformProposal }),
  }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.type(within(dialog).getByLabelText('描述数据集生成或修改目标'), '将订单编号转为大写后输出')
  await user.click(within(dialog).getByRole('button', { name: 'AI 生成流程' }))
  await within(dialog).findByRole('heading', { level: 3, name: '将订单编号转为大写后输出' })
  await user.click(within(dialog).getByRole('button', { name: '应用' }))

  expect(await within(dialog).findByText(/1 个数据节点 · 0 个关联 · 0 个分组 · 1 个结束节点 · 1 个字段处理/)).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: '打开大写转换 1 配置' })).toHaveTextContent('订单编号大写')
  await user.click(within(dialog).getByRole('button', { name: '撤销' }))
  expect(await within(dialog).findByText('已撤销本次 AI 方案，恢复到应用前的画布')).toBeInTheDocument()
  expect(within(dialog).queryByRole('button', { name: '打开大写转换 1 配置' })).not.toBeInTheDocument()
})

test('AI 生成期间继续手工编辑时拒绝展示过期方案', async () => {
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  let resolveFetch!: (response: { ok: boolean; status: number; json: () => Promise<unknown> }) => void
  const fetchMock = vi.fn()
    .mockImplementationOnce(() => new Promise(resolve => { resolveFetch = resolve }))
    .mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ requestId: 'ai-request-retry', proposal: aiProposal() }),
    })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  renderPage()

  await screen.findByText('还没有数据集')
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await user.type(within(dialog).getByLabelText('描述数据集生成或修改目标'), '生成订单数据集')
  await user.click(within(dialog).getByRole('button', { name: 'AI 生成流程' }))
  expect(within(dialog).getByRole('status')).toHaveTextContent('正在理解业务目标并规划 DAG')

  await user.click(within(dialog).getByRole('button', { name: /订单表.*已映射/ }))
  expect(await within(dialog).findByLabelText('配置表 订单表')).toBeInTheDocument()
  resolveFetch({ ok: true, status: 200, json: async () => ({ requestId: 'ai-request-2', proposal: aiProposal() }) })

  expect(await within(dialog).findByRole('alert')).toHaveTextContent('生成期间画布已发生变化')
  const toolbar = within(dialog).getByRole('toolbar', { name: 'AI 方案操作' })
  expect(within(toolbar).getByRole('button', { name: '应用' })).toBeDisabled()
  const retry = within(toolbar).getByRole('button', { name: '按原要求重试' })
  expect(retry).toBeEnabled()
  const prompt = within(dialog).getByLabelText('描述数据集生成或修改目标')
  await user.clear(prompt)
  await user.type(prompt, '基于当前画布重新生成订单数据集')
  expect(within(toolbar).getByRole('button', { name: '根据修改重新生成' })).toBe(retry)
  await user.click(within(toolbar).getByRole('button', { name: '根据修改重新生成' }))

  expect(await within(dialog).findByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(2)
  const [, retryInit] = fetchMock.mock.calls[1] as [string, RequestInit]
  const retryBody = JSON.parse(String(retryInit.body)) as { instruction: string; current: { nodes: Array<{ id: string; tableId: string }> } }
  expect(retryBody.instruction).toBe('基于当前画布重新生成订单数据集')
  expect(retryBody.current.nodes).toEqual([expect.objectContaining({ id: 'node_1', tableId: table.id })])
  expect(retry).toBeDisabled()
})

test('AI 方案应用后结束节点按点击执行当前候选 DSL 并展示新预览', async () => {
  const editable = record({
    dsl: {
      dslVersion: '1.0',
      dataset: { code: 'orders_detail', name: '订单明细', description: '订单业务明细数据', type: 'SINGLE_SOURCE' },
      nodes: [{ id: 'node_1', type: 'TABLE', dataSourceId: table.dataSourceId, tableId: table.id, alias: 'orders', projection: ['order_id', 'amount'] }],
      fields: [
        { id: 'field_order_id', code: 'order_id', name: '订单编号', role: 'IDENTIFIER', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'order_id' }, canonicalType: 'STRING', nullable: false, visible: true },
        { id: 'field_amount', code: 'amount', name: '订单金额', role: 'MEASURE', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' }, canonicalType: 'DECIMAL', nullable: false, visible: true },
      ],
      joins: [], filters: [], parameters: [], groupBy: [], sorts: [], outputGrain: { description: '每行一笔订单', keyFields: ['order_id'] },
    },
  })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary()]))
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(editable)
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: 'a'.repeat(64), planHash: 'b'.repeat(64), logicalPlan: {} })
  const preview = vi.spyOn(datasetAPI, 'previewDraft')
    .mockResolvedValueOnce({
      queryId: 'saved-preview', dslHash: 'c'.repeat(64), planHash: 'd'.repeat(64), baseVersion: 4,
      columns: ['order_id'], rows: [['应用前旧数据']], rowCount: 1, durationMs: 4,
    })
    .mockResolvedValueOnce({
      queryId: 'candidate-preview', dslHash: 'e'.repeat(64), planHash: 'f'.repeat(64), baseVersion: 4,
      columns: ['order_id', 'amount'], rows: [['应用后新数据', 88]], rowCount: 1, durationMs: 5,
    })
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({
      requestId: 'ai-request-preview-invalidation',
      proposal: aiProposal({
        mode: 'MODIFY',
        changeSet: {
          operations: [{ action: 'UPDATE', componentKind: 'DATASET', componentId: 'dataset-1', componentName: 'AI 订单明细', fields: ['name'], inputChanges: [], description: '更新数据集名称。' }],
          fieldChanges: [],
        },
      }),
    }),
  }))
  const user = userEvent.setup()
  renderPage()

  await screen.findByRole('heading', { level: 3, name: '订单明细' })
  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '修改' }))
  const dialog = await screen.findByRole('dialog', { name: '修改数据集' })
  await user.click(await within(dialog).findByRole('button', { name: '预览结束节点数据' }))
  let previewDialog = await within(dialog).findByRole('dialog', { name: '最终输出数据预览' })
  expect(await within(previewDialog).findByText('应用前旧数据')).toBeInTheDocument()
  expect(preview).toHaveBeenCalledTimes(1)
  await user.click(within(previewDialog).getByRole('button', { name: '关闭组件数据预览' }))

  await user.type(within(dialog).getByLabelText('描述数据集生成或修改目标'), '更新当前数据集流程')
  await user.click(within(dialog).getByRole('button', { name: 'AI 修改流程' }))
  await within(dialog).findByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })
  await user.click(within(within(dialog).getByRole('toolbar', { name: 'AI 方案操作' })).getByRole('button', { name: '应用' }))
  expect(await within(dialog).findByText('已应用到画布')).toBeInTheDocument()

  await user.click(within(dialog).getByRole('button', { name: '预览结束节点数据' }))
  previewDialog = await within(dialog).findByRole('dialog', { name: '最终输出数据预览' })
  expect(await within(previewDialog).findByText('应用后新数据')).toBeInTheDocument()
  expect(within(previewDialog).queryByText('应用前旧数据')).not.toBeInTheDocument()
  expect(within(previewDialog).getByText(/最多展示 5 行/)).toBeInTheDocument()
  expect(preview).toHaveBeenCalledTimes(2)
  const [, expectedVersion, candidateDSL, , parameters, maxRows] = preview.mock.calls[1]
  expect(expectedVersion).toBe(4)
  expect(candidateDSL.dataset).toEqual(expect.objectContaining({ code: 'orders_detail', name: 'AI 订单明细' }))
  expect(candidateDSL.fields).toEqual(expect.arrayContaining([
    expect.objectContaining({ code: 'order_id', visible: true }),
    expect.objectContaining({ code: 'amount', visible: true }),
  ]))
  expect(parameters).toEqual({})
  expect(maxRows).toBe(5)
})

test('指标改造提案进入普通数据集后自动生成一次 AI 修改方案且不保存', async () => {
  const editable = record({
    dsl: {
      dslVersion: '1.0',
      dataset: { code: 'orders_detail', name: '订单明细', description: '订单业务明细数据', type: 'SINGLE_SOURCE' },
      nodes: [{ id: 'node_1', type: 'TABLE', dataSourceId: table.dataSourceId, tableId: table.id, alias: 'orders', projection: ['order_id', 'amount'] }],
      fields: [
        { id: 'field_order_id', code: 'order_id', name: '订单编号', role: 'IDENTIFIER', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'order_id' }, canonicalType: 'STRING', nullable: false, visible: true },
        { id: 'field_amount', code: 'amount', name: '订单金额', role: 'MEASURE', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' }, canonicalType: 'DECIMAL', nullable: false, visible: true },
      ],
      joins: [], filters: [], parameters: [], groupBy: [], sorts: [], outputGrain: { description: '每行一笔订单', keyFields: ['order_id'] },
    },
  })
  const instruction = '增加退款状态字段，并保留支付时间与订单金额。'
  const metricAIHints = {
    preferredTableIds: [table.id], aggregation: 'COUNT',
    measureFields: [{ tableId: table.id, column: 'order_id' }],
    dimensionFields: [], timeGrain: 'MONTH',
  }
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(editable)
  const update = vi.spyOn(datasetAPI, 'update')
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ requestId: 'ai-request-auto-modify', proposal: aiProposal({ mode: 'MODIFY' }) }),
  })
  vi.stubGlobal('fetch', fetchMock)

  render(<StrictMode><MemoryRouter initialEntries={[{
    pathname: '/datasets/dataset-1/edit',
    state: { metricAIInstruction: instruction, metricAIHints, returnTo: '/metrics/new' },
  }]}><Routes>
    <Route path="/datasets/:datasetId/edit" element={<DatasetCenterPage />} />
  </Routes></MemoryRouter></StrictMode>)

  const dialog = await screen.findByRole('dialog', { name: '修改数据集' })
  expect(await within(dialog).findByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })).toBeInTheDocument()
  expect(within(dialog).getByText('已从指标提案带入数据集改造目标，正在自动生成 AI 画布方案。')).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
  const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
  expect(path).toBe('/api/v1/datasets/dataset-1/ai/proposals')
  const body = JSON.parse(String(init.body)) as { instruction: string; current: { nodes: Array<{ tableId: string }> }; hints: unknown }
  expect(body.instruction).toBe(instruction)
  expect(body.current.nodes).toEqual([expect.objectContaining({ tableId: table.id })])
  expect(body.hints).toEqual(metricAIHints)
  expect(update).not.toHaveBeenCalled()
})

test('指标提案自动生成失败后保留手动重试且不自动应用方案', async () => {
  const user = userEvent.setup()
  const instruction = '新建订单销售分析数据集，并保留订单金额。'
  const metricAIHints = {
    preferredTableIds: [table.id], aggregation: 'SUM',
    measureFields: [{ tableId: table.id, column: 'amount' }],
    dimensionFields: [], timeGrain: '',
  }
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const validate = vi.spyOn(datasetAPI, 'validate')
  const create = vi.spyOn(datasetAPI, 'create')
  const fetchMock = vi.fn()
    .mockResolvedValueOnce({
      ok: false,
      status: 502,
      json: async () => ({
        code: 'DATASET_AI_INVALID_OUTPUT',
        reasonCode: 'FIELD_REFERENCE_INVALID',
        stage: 'PLAN_VALIDATION',
        repairAttempted: true,
        message: 'AI 方案未通过数据集安全校验',
        requestId: 'request-invalid-plan-1',
      }),
    })
    .mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ requestId: 'ai-request-manual-retry', proposal: aiProposal() }),
    })
  vi.stubGlobal('fetch', fetchMock)

  render(<MemoryRouter initialEntries={[{
    pathname: '/datasets/new/edit',
    state: { metricAIInstruction: instruction, metricAIHints, returnTo: '/metrics/new' },
  }]}><Routes>
    <Route path="/datasets/:datasetId/edit" element={<DatasetCenterPage />} />
  </Routes></MemoryRouter>)

  const dialog = await screen.findByRole('dialog', { name: '新建数据集' })
  const alert = await within(dialog).findByRole('alert')
  expect(alert).toHaveTextContent('系统已自动修复一次仍失败')
  expect(alert).toHaveTextContent('DATASET_AI_INVALID_OUTPUT')
  expect(alert).toHaveTextContent('FIELD_REFERENCE_INVALID')
  expect(alert).toHaveTextContent('PLAN_VALIDATION')
  expect(alert).toHaveTextContent('已尝试')
  expect(alert).toHaveTextContent('HTTP502')
  expect(alert).toHaveTextContent('request-invalid-plan-1')
  expect(alert).toHaveTextContent('精确字段')
  expect(fetchMock).toHaveBeenCalledTimes(1)
  const retry = within(within(dialog).getByRole('toolbar', { name: 'AI 方案操作' })).getByRole('button', { name: '按原要求重试' })
  expect(retry).toBeEnabled()
  await user.click(retry)

  expect(await within(dialog).findByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(2)
  const [, retryInit] = fetchMock.mock.calls[1] as [string, RequestInit]
  expect(JSON.parse(String(retryInit.body))).toMatchObject({ instruction, hints: metricAIHints })
  expect(validate).not.toHaveBeenCalled()
  expect(create).not.toHaveBeenCalled()
  expect(within(dialog).getByRole('button', { name: '应用' })).toBeEnabled()
})

test('同一指标路由返回后不会再次自动生成方案', async () => {
  const instruction = '新建订单销售分析数据集。'
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ requestId: 'ai-request-once', proposal: aiProposal() }),
  })
  vi.stubGlobal('fetch', fetchMock)
  const entry = {
    pathname: '/datasets/new/edit', key: 'metric-handoff-entry',
    state: { metricAIInstruction: instruction, returnTo: '/metrics/new' },
  }
  const route = <Routes><Route path="/datasets/:datasetId/edit" element={<DatasetCenterPage />} /></Routes>

  const first = render(<MemoryRouter initialEntries={[entry]}>{route}</MemoryRouter>)
  expect(await screen.findByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
  first.unmount()

  render(<MemoryRouter initialEntries={[entry]}>{route}</MemoryRouter>)
  const returnedDialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await within(returnedDialog).findByRole('button', { name: /订单表.*已映射/ })
  expect(within(returnedDialog).getByLabelText('描述数据集生成或修改目标')).toHaveValue('')
  expect(within(returnedDialog).queryByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })).not.toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
})

test('指标新建数据集提案预填 AI 目标，保存后保留返回信息并直接进入发布审批', async () => {
  const user = userEvent.setup()
  const instruction = '以订单映射表为来源新建普通数据集，输出订单编号与销售额。'
  const requirement = '创建月度各区域销售额指标。'
  const saved = record({
    id: 'dataset-created', code: 'monthly_region_sales', name: '月度区域销售数据集', description: '用于月度区域销售额指标',
    status: 'DRAFT', currentPublishedVersionId: undefined,
  })
  vi.spyOn(datasetAPI, 'list')
    .mockResolvedValueOnce(page([]))
    .mockResolvedValueOnce(page([{ ...summary(), ...saved }]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: saved.dslHash, planHash: saved.planHash, logicalPlan: {} })
  vi.spyOn(datasetAPI, 'create').mockResolvedValue(saved)
  vi.spyOn(datasetAPI, 'get').mockResolvedValue(saved)
  vi.spyOn(datasetAPI, 'listPublicationRequests').mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
  vi.spyOn(datasetAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ requestId: 'ai-request-auto-create', proposal: aiProposal() }),
  })
  vi.stubGlobal('fetch', fetchMock)

  const routeState = { metricAIInstruction: instruction, metricAIRequirement: requirement, returnTo: '/metrics/new' }
  render(<MemoryRouter initialEntries={[{ pathname: '/datasets/new/edit', state: routeState }]}><Routes>
    <Route path="/datasets/:datasetId/edit" element={<DatasetCenterLocationProbe />} />
    <Route path="/datasets" element={<DatasetCenterLocationProbe />} />
  </Routes></MemoryRouter>)

  const createDialog = await screen.findByRole('dialog', { name: '新建数据集' })
  expect(await within(createDialog).findByRole('heading', { level: 3, name: '使用订单表生成可直接预览的明细数据集' })).toBeInTheDocument()
  expect(within(createDialog).getByText('已从指标提案带入新数据集构建目标，正在自动生成 AI 画布方案。')).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(1)
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/datasets/ai/proposals', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ instruction }),
  }))

  await user.click(within(createDialog).getByRole('button', { name: /订单表.*已映射/ }))
  const tableDrawer = await within(createDialog).findByLabelText('配置表 订单表')
  await user.click(within(tableDrawer).getByRole('button', { name: '完成' }))
  await user.click(within(createDialog).getByRole('button', { name: '保存配置' }))
  const metadataDialog = await screen.findByRole('dialog', { name: '完善数据集信息' })
  await user.type(within(metadataDialog).getByLabelText('数据集名称'), saved.name)
  await user.type(within(metadataDialog).getByLabelText('数据集说明'), saved.description)
  await user.click(within(metadataDialog).getByRole('button', { name: '创建数据集' }))

  const publicationDialog = await screen.findByRole('dialog', { name: `${saved.name} · 发布审批` })
  expect(within(publicationDialog).getByRole('button', { name: '提交发布审批' })).toBeEnabled()
  expect(JSON.parse(screen.getByLabelText('数据集流程路由').textContent || '{}')).toEqual({ pathname: '/datasets', state: routeState })
})

test('修改数据集加载完成前禁用 AI，完成后携带当前 DAG 调用对象级提案接口', async () => {
  const editable = record({
    dsl: {
      dslVersion: '1.0',
      dataset: { code: 'orders_detail', name: '订单明细', description: '订单业务明细数据', type: 'SINGLE_SOURCE' },
      nodes: [{ id: 'node_1', type: 'TABLE', dataSourceId: table.dataSourceId, tableId: table.id, alias: 'orders', projection: ['order_id', 'amount'] }],
      fields: [
        { id: 'field_order_id', code: 'order_id', name: '订单编号', role: 'IDENTIFIER', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'order_id' }, canonicalType: 'STRING', nullable: false, visible: true },
        { id: 'field_amount', code: 'amount', name: '订单金额', role: 'MEASURE', expression: { type: 'FIELD_REF', nodeId: 'node_1', field: 'amount' }, canonicalType: 'DECIMAL', nullable: false, visible: true },
      ],
      joins: [], filters: [], parameters: [], groupBy: [], sorts: [], outputGrain: { description: '每行一笔订单', keyFields: ['order_id'] },
    },
  })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue(page([summary()]))
  vi.spyOn(datasetAPI, 'mappingTables').mockResolvedValue({ items: [table] })
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: columns })
  let resolveGet!: (value: DatasetRecord) => void
  vi.spyOn(datasetAPI, 'get').mockImplementation(() => new Promise(resolve => { resolveGet = resolve }))
  const fetchMock = vi.fn()
    .mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({
        requestId: 'ai-request-modify',
        proposal: aiProposal({
          mode: 'MODIFY',
          changeSet: { operations: [
            { action: 'REMOVE', componentKind: 'GROUP', componentId: 'group_after', componentName: '关联后地区汇总', fields: [], inputChanges: [], description: '取消关联后的分组。' },
            {
              action: 'UPDATE', componentKind: 'END', componentId: 'end_1', componentName: '最终输出', fields: ['input', 'outputs'],
              inputChanges: [{ field: 'input', from: { kind: 'GROUP', id: 'group_after' }, to: { kind: 'JOIN', id: 'join_1' } }],
              description: '将输出直接连接到关联结果。',
            },
          ], fieldChanges: [] },
        }),
      }),
    })
    .mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ requestId: 'ai-request-noop', proposal: aiProposal({ mode: 'MODIFY' }) }),
    })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  renderPage()

  await screen.findByRole('heading', { level: 3, name: '订单明细' })
  await user.click(screen.getByRole('button', { name: '新建数据集' }))
  const createDialog = await screen.findByRole('dialog', { name: '新建数据集' })
  await within(createDialog).findByRole('button', { name: /订单表.*已映射/ })
  await user.click(within(createDialog).getByRole('button', { name: '关闭新建数据集' }))

  await user.click(within(cardFor('订单明细')).getByRole('button', { name: '修改' }))
  const editDialog = await screen.findByRole('dialog', { name: '修改数据集' })
  const prompt = within(editDialog).getByLabelText('描述数据集生成或修改目标')
  expect(prompt).toBeDisabled()
  expect(within(editDialog).getByRole('status')).toHaveTextContent('正在准备当前画布与可用数据资产')

  resolveGet(editable)
  expect(await within(editDialog).findByText('告诉 AI 接下来怎么改')).toBeInTheDocument()
  expect(prompt).toBeEnabled()
  await user.type(prompt, '把数据集说明改得更清楚，其他配置保持不变')
  await user.click(within(editDialog).getByRole('button', { name: 'AI 修改流程' }))
  expect(await within(editDialog).findByText('使用订单表生成可直接预览的明细数据集')).toBeInTheDocument()

  const changes = within(editDialog).getByRole('list', { name: '本次修改清单' })
  expect(within(changes).getByText('删除')).toBeInTheDocument()
  expect(within(changes).getByText('关联后地区汇总')).toBeInTheDocument()
  expect(within(changes).getByText('分组')).toBeInTheDocument()
  expect(within(changes).getByText('修改')).toBeInTheDocument()
  expect(within(changes).getByText('最终输出')).toBeInTheDocument()
  expect(within(changes).getByText('输出 · 修改字段：上游输入、输出字段')).toBeInTheDocument()
  expect(within(changes).queryByText('关联前订单汇总')).not.toBeInTheDocument()

  expect(fetchMock).toHaveBeenCalledTimes(1)
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
  expect(url).toBe('/api/v1/datasets/dataset-1/ai/proposals')
  const body = JSON.parse(String(init.body)) as { instruction: string; current: { nodes: Array<{ id: string; tableId: string }>; end: { input: TestGraphInput } } }
  expect(body.instruction).toBe('把数据集说明改得更清楚，其他配置保持不变')
  expect(body.current.nodes).toEqual([expect.objectContaining({ id: 'node_1', tableId: table.id })])
  expect(body.current.end.input).toEqual({ kind: 'NODE', id: 'node_1' })

  await user.type(prompt, '保持当前流程不变')
  await user.click(within(editDialog).getByRole('button', { name: '继续修改' }))
  expect(await within(editDialog).findByText('当前流程已符合要求，无需变更。')).toBeInTheDocument()
  expect(within(within(editDialog).getByRole('toolbar', { name: 'AI 方案操作' })).getByRole('button', { name: '应用' })).toBeDisabled()
})
