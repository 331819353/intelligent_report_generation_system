import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { datasetAPI, type DatasetDSL, type DatasetSummary, type PublishedVersionRecord } from '../lib/datasets'
import { metricCandidateAPI, type MetricCandidate } from '../lib/metric-candidates'
import {
  metricAPI,
  type MetricDefinition,
  type MetricRecord,
  type MetricSummary,
  type MetricVersionRecord,
} from '../lib/metrics'
import { MetricCatalogPage } from './MetricCatalogPage'

afterEach(() => vi.restoreAllMocks())

describe('指标资产目录', () => {
  test('支持名称编码说明搜索以及状态、类型和来源组合筛选', async () => {
    const user = userEvent.setup()
    const metrics = [
      metricSummary(),
      metricSummary({ id: 'metric-2', code: 'gross_profit', name: '毛利润', description: '扣除营业成本后的利润', status: 'DRAFT', currentPublishedVersionId: undefined }),
      metricSummary({ id: 'metric-3', code: 'customer_count', name: '客户数', description: '去重客户数量', type: 'DERIVED', datasetId: 'dataset-2' }),
    ]
    mockCatalog(metrics)
    renderCatalog()

    expect(await screen.findByText('营业收入')).toBeInTheDocument()
    expect(screen.getByText('毛利润')).toBeInTheDocument()
    expect(screen.getByText('客户数')).toBeInTheDocument()

    await user.type(screen.getByLabelText('搜索指标'), '成本')
    expect(screen.getByText('毛利润')).toBeInTheDocument()
    expect(screen.queryByText('营业收入')).not.toBeInTheDocument()

    await user.clear(screen.getByLabelText('搜索指标'))
    await user.selectOptions(screen.getByLabelText('指标状态'), 'PUBLISHED')
    await user.selectOptions(screen.getByLabelText('指标类型筛选'), 'DERIVED')
    await user.selectOptions(screen.getByLabelText('来源数据集'), 'dataset-2')

    expect(screen.getByText('客户数')).toBeInTheDocument()
    expect(screen.queryByText('营业收入')).not.toBeInTheDocument()
    expect(screen.getByText('显示 1 / 3')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '重置' }))
    expect(screen.getByText('营业收入')).toBeInTheDocument()
    expect(screen.getByText('毛利润')).toBeInTheDocument()
  })

  test('详情按概览、口径、维度、来源和登记血缘展示精确发布版本', async () => {
    const user = userEvent.setup()
    const summary = metricSummary()
    const definition = metricDefinition()
    const record = metricRecord({ definition })
    const published = metricVersion({ definition })
    mockCatalog([summary])
    vi.spyOn(metricAPI, 'get').mockResolvedValue(record)
    vi.spyOn(metricAPI, 'getVersion').mockResolvedValue(published)
    vi.spyOn(metricAPI, 'getVersionUsage').mockResolvedValue({ reportDraftReferences: 3, downstreamDraftReferences: 2, downstreamPublishedReferences: 1, activeQueryRuns: 4 })
    vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(datasetVersion)
    renderCatalog()

    const row = (await screen.findByText('营业收入')).closest('article')
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: '查看' }))
    const dialog = await screen.findByRole('dialog', { name: '指标详情' })
    expect(await within(dialog).findByText('这个指标代表什么')).toBeInTheDocument()
    expect(within(dialog).getByText('当前发布', { exact: false })).toBeInTheDocument()

    await user.click(within(dialog).getByRole('tab', { name: '口径' }))
    expect(within(dialog).getByText('SUM(营业收入（revenue）)')).toBeInTheDocument()
    expect(within(dialog).getByText('2 位 · HALF_UP')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('tab', { name: '维度' }))
    expect(within(dialog).getByRole('cell', { name: '地区' })).toBeInTheDocument()
    expect(within(dialog).getByText('继承指标规则')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('tab', { name: '来源' }))
    expect(within(dialog).getByText('企业收入数据集')).toBeInTheDocument()
    expect(within(dialog).getByText('orders')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('tab', { name: '血缘' }))
    expect(within(dialog).getByText('从可信来源到当前指标版本')).toBeInTheDocument()
    expect(within(dialog).getByText('对象级“源字段 → 组件 → 报告版本”链路尚未由服务端开放', { exact: false })).toBeInTheDocument()
    expect(within(dialog).getByText('报告草稿引用')).toBeInTheDocument()
    expect(within(dialog).getByText('3')).toBeInTheDocument()
  })

  test('精确发布版本不可读取时不会回退展示草稿口径', async () => {
    const user = userEvent.setup()
    const summary = metricSummary()
    mockCatalog([summary])
    vi.spyOn(metricAPI, 'get').mockResolvedValue(metricRecord({
      definition: metricDefinition({ metric: { code: 'draft_only', name: '未发布草稿口径', description: '不应展示', type: 'ATOMIC' } }),
    }))
    vi.spyOn(metricAPI, 'getVersion').mockRejectedValue(new Error('forbidden'))
    const datasetVersionSpy = vi.spyOn(datasetAPI, 'getVersion')
    renderCatalog()

    const row = (await screen.findByText('营业收入')).closest('article')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '查看' }))
    const dialog = await screen.findByRole('dialog', { name: '指标详情' })

    expect(await within(dialog).findByText('精确发布版本暂时不可读取')).toBeInTheDocument()
    expect(within(dialog).getByText('精确发布版本读取失败，未回退到草稿')).toBeInTheDocument()
    expect(within(dialog).queryByText('未发布草稿口径')).not.toBeInTheDocument()
    expect(datasetVersionSpy).not.toHaveBeenCalled()
  })

  test('候选页签支持状态、提取方式和来源筛选并展示字段级生成依据', async () => {
    const user = userEvent.setup()
    const ready = metricCandidate()
    const blocked = metricCandidate({
      id: 'candidate-2', name: '客户数', code: 'customer_count', datasetId: 'dataset-2', datasetVersionId: 'dataset-version-2',
      status: 'BLOCKED', method: 'RULE', confidence: 0.68, blockReasons: ['当前数据集版本包含预聚合'],
    })
    mockCatalog([], [ready, blocked])
    vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(datasetVersion)
    renderCatalog()

    await user.click(await screen.findByRole('tab', { name: /候选指标/ }))
    expect(screen.getByText('订单金额')).toBeInTheDocument()
    expect(screen.getByText('客户数')).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('候选状态'), 'BLOCKED')
    await user.selectOptions(screen.getByLabelText('提取方式'), 'RULE')
    await user.selectOptions(screen.getByLabelText('来源数据集'), 'dataset-2')
    expect(screen.getByText('客户数')).toBeInTheDocument()
    expect(screen.queryByText('订单金额')).not.toBeInTheDocument()
    expect(screen.getByText('显示 1 / 2')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '重置' }))
    const row = screen.getByText('订单金额').closest('article')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '审核详情' }))
    const dialog = await screen.findByRole('dialog', { name: '候选指标详情' })
    expect(await within(dialog).findByText('SUM(营业收入（revenue）)')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('tab', { name: '生成依据' }))
    expect(within(dialog).getByText('字段级证据')).toBeInTheDocument()
    expect(within(dialog).getByText('字段 semanticType=AMOUNT，规则建议使用 SUM')).toBeInTheDocument()
    expect(within(dialog).getByText('需要业务负责人确认是否包含退款订单')).toBeInTheDocument()
  })

  test('接受候选时使用当前版本并将服务端创建的指标草稿加入正式目录', async () => {
    const user = userEvent.setup()
    const candidate = metricCandidate()
    const accepted = metricCandidate({ ...candidate, status: 'ACCEPTED', version: 4, acceptedMetricId: 'metric-created' })
    const createdMetric = metricRecord({ id: 'metric-created', code: candidate.code, name: candidate.name, description: candidate.description, status: 'DRAFT', currentPublishedVersionId: undefined })
    mockCatalog([], [candidate])
    vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(datasetVersion)
    const accept = vi.spyOn(metricCandidateAPI, 'accept').mockResolvedValue({ candidate: accepted, metric: createdMetric })
    renderCatalog()

    await user.click(await screen.findByRole('tab', { name: /候选指标/ }))
    const row = screen.getByText('订单金额').closest('article')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '审核详情' }))
    const dialog = await screen.findByRole('dialog', { name: '候选指标详情' })
    expect(await within(dialog).findByText('企业收入数据集')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '接受并创建草稿' }))

    expect(accept).toHaveBeenCalledWith(candidate.id, candidate.version)
    expect(await screen.findByText('已接受“订单金额”，指标草稿已创建')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '编辑指标草稿' })).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '关闭' }))
    await user.click(screen.getByRole('tab', { name: /正式指标/ }))
    const metricRow = screen.getByText('订单金额').closest('article')
    expect(within(metricRow as HTMLElement).getByText('草稿')).toBeInTheDocument()
  })

  test('拒绝候选必须填写原因并提交当前乐观锁版本', async () => {
    const user = userEvent.setup()
    const candidate = metricCandidate({ status: 'NEEDS_REVIEW' })
    const rejected = metricCandidate({ ...candidate, status: 'REJECTED', version: 4 })
    mockCatalog([], [candidate])
    vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(datasetVersion)
    const reject = vi.spyOn(metricCandidateAPI, 'reject').mockResolvedValue(rejected)
    renderCatalog()

    await user.click(await screen.findByRole('tab', { name: /候选指标/ }))
    const row = screen.getByText('订单金额').closest('article')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '审核详情' }))
    const dialog = await screen.findByRole('dialog', { name: '候选指标详情' })
    await user.click(within(dialog).getByRole('button', { name: '拒绝候选' }))
    const confirm = within(dialog).getByRole('button', { name: '确认拒绝' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByLabelText('拒绝原因'), '该字段包含税前金额，不符合经营口径')
    await user.click(confirm)

    expect(reject).toHaveBeenCalledWith(candidate.id, candidate.version, '该字段包含税前金额，不符合经营口径')
    expect(await screen.findByText('已拒绝候选“订单金额”')).toBeInTheDocument()
    expect(within(dialog).getByText('候选已由人工拒绝')).toBeInTheDocument()
  })
})

function renderCatalog() {
  return render(<MemoryRouter><MetricCatalogPage /></MemoryRouter>)
}

function mockCatalog(metrics: MetricSummary[], candidates: MetricCandidate[] = []) {
  vi.spyOn(metricAPI, 'list').mockResolvedValue({ items: metrics, total: metrics.length, limit: 200, offset: 0 })
  vi.spyOn(datasetAPI, 'list').mockResolvedValue({ items: datasets, total: datasets.length, limit: 200, offset: 0 })
  vi.spyOn(metricCandidateAPI, 'list').mockResolvedValue({ items: candidates, total: candidates.length, limit: 200, offset: 0 })
}

const datasets: DatasetSummary[] = [
  { id: 'dataset-1', code: 'enterprise_revenue', name: '企业收入数据集', description: '', type: 'SINGLE_SOURCE', status: 'PUBLISHED', version: 3, dslHash: 'a'.repeat(64), currentPublishedVersionId: 'dataset-version-1', updatedAt: '2026-07-16T00:00:00Z' },
  { id: 'dataset-2', code: 'customer_profile', name: '客户主题数据集', description: '', type: 'SINGLE_SOURCE', status: 'PUBLISHED', version: 2, dslHash: 'b'.repeat(64), currentPublishedVersionId: 'dataset-version-2', updatedAt: '2026-07-16T00:00:00Z' },
]

function metricSummary(overrides: Partial<MetricSummary> = {}): MetricSummary {
  return {
    id: 'metric-1', code: 'revenue_total', name: '营业收入', description: '已确认订单的营业收入总额', type: 'ATOMIC', status: 'PUBLISHED',
    version: 5, currentPublishedVersionId: 'metric-version-1', datasetId: 'dataset-1', datasetVersionId: 'dataset-version-1', updatedAt: '2026-07-19T04:30:00Z',
    ...overrides,
  }
}

function metricDefinition(overrides: Partial<MetricDefinition> = {}): MetricDefinition {
  return {
    schemaVersion: '1.0', metric: { code: 'revenue_total', name: '营业收入', description: '已确认订单的营业收入总额', type: 'ATOMIC' },
    datasetId: 'dataset-1', datasetVersionId: 'dataset-version-1', expression: { type: 'FIELD_REF', fieldId: 'field_revenue' }, aggregation: 'SUM',
    unit: '元', numberFormat: '#,##0.00', timeFieldId: 'field_month', timeGrain: 'MONTH', additivity: 'ADDITIVE', nonAdditiveDimensionFieldIds: [],
    allowedDimensions: [{ fieldId: 'field_region', name: '地区', hierarchyFieldIds: ['field_region'], sortDirection: 'ASC', nullLabel: '未分类' }],
    decimalScale: 2, roundingMode: 'HALF_UP', nullHandling: 'IGNORE', divisionByZero: 'NULL', ...overrides,
  }
}

function metricRecord(overrides: Partial<MetricRecord> = {}): MetricRecord {
  const definition = overrides.definition ?? metricDefinition()
  return {
    ...metricSummary(), draftVersionId: 'metric-draft-1', draftVersionNo: 2, draftRecordVersion: 4,
    definitionHash: 'c'.repeat(64), definition, createdAt: '2026-07-15T00:00:00Z', ...overrides,
  }
}

function metricVersion(overrides: Partial<MetricVersionRecord> = {}): MetricVersionRecord {
  const definition = overrides.definition ?? metricDefinition()
  return {
    id: 'metric-version-1', metricId: 'metric-1', metricRecordVersion: 5, datasetId: definition.datasetId, datasetVersionId: definition.datasetVersionId,
    draftVersionId: 'metric-draft-1', draftRecordVersion: 4, versionNo: 3, status: 'PUBLISHED', definitionHash: 'd'.repeat(64), definition,
    publishedAt: '2026-07-19T04:00:00Z', publishedBy: 'user-1', ...overrides,
  }
}

function metricCandidate(overrides: Partial<MetricCandidate> = {}): MetricCandidate {
  return {
    id: 'candidate-1', datasetId: 'dataset-1', datasetVersionId: 'dataset-version-1', dslHash: 'e'.repeat(64),
    name: '订单金额', code: 'order_amount_sum', description: '已确认订单金额之和', status: 'READY', method: 'HYBRID', confidence: 0.92,
    proposedDefinition: metricDefinition({ metric: { code: 'order_amount_sum', name: '订单金额', description: '已确认订单金额之和', type: 'ATOMIC' } }),
    sourceFieldIds: ['field_revenue'],
    evidence: [{ property: 'aggregation', source: 'SEMANTIC_TYPE_RULE', detail: '字段 semanticType=AMOUNT，规则建议使用 SUM' }],
    assumptions: ['需要业务负责人确认是否包含退款订单'], warnings: [], blockReasons: [], fingerprint: 'f'.repeat(64), version: 3,
    createdAt: '2026-07-20T01:00:00Z', updatedAt: '2026-07-20T02:00:00Z', ...overrides,
  }
}

const datasetDSL: DatasetDSL = {
  dslVersion: '1.0', dataset: { code: 'enterprise_revenue', name: '企业收入数据集', type: 'SINGLE_SOURCE' },
  nodes: [{ id: 'orders', type: 'TABLE', datasourceId: 'source-orders', tableId: 'table-orders', alias: 'orders' }],
  fields: [
    { id: 'field_revenue', code: 'revenue', name: '营业收入', role: 'MEASURE', canonicalType: 'DECIMAL', expression: { type: 'FIELD_REF', nodeId: 'orders', field: 'amount' } },
    { id: 'field_region', code: 'region', name: '地区', role: 'DIMENSION', canonicalType: 'STRING', expression: { type: 'FIELD_REF', nodeId: 'orders', field: 'region' } },
  ],
}

const datasetVersion: PublishedVersionRecord = {
  id: 'dataset-version-1', datasetId: 'dataset-1', versionNo: 3, status: 'PUBLISHED', dslVersion: '1.0', dslHash: 'e'.repeat(64), planHash: 'f'.repeat(64),
  dsl: datasetDSL, logicalPlan: {}, publishedAt: '2026-07-18T00:00:00Z', publishedBy: 'user-1', datasetRecordVersion: 3, draftVersionId: 'dataset-draft-1', draftRecordVersion: 2,
}
