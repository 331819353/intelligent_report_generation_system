import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { datasetAPI, type DatasetDSL, type DatasetSummary, type PublishedVersionRecord } from '../lib/datasets'
import {
  metricAPI,
  type MetricDefinition,
  type MetricRecord,
  type MetricSummary,
  type MetricVersionRecord,
} from '../lib/metrics'
import { metricCandidateAPI, type MetricCandidate } from '../lib/metric-candidates'
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

    await user.type(screen.getByLabelText('搜索数据集或指标'), '成本')
    expect(screen.getByText('毛利润')).toBeInTheDocument()
    expect(screen.queryByText('营业收入')).not.toBeInTheDocument()

    await user.clear(screen.getByLabelText('搜索数据集或指标'))
    await user.selectOptions(screen.getByLabelText('指标状态'), 'PUBLISHED')
    await user.selectOptions(screen.getByLabelText('指标类型筛选'), 'DERIVED')
    await user.selectOptions(screen.getByLabelText('来源数据集'), 'dataset-2')

    expect(screen.getByText('客户数')).toBeInTheDocument()
    expect(screen.queryByText('营业收入')).not.toBeInTheDocument()
    expect(screen.getByText('显示 1 / 2 个数据集')).toBeInTheDocument()

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

  test('按普通数据集展示指标并保留候选分区', async () => {
    mockCatalog([])
    renderCatalog()

    expect(await screen.findByRole('region', { name: '企业收入数据集指标展示区' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: '客户主题数据集指标展示区' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /候选区/ })).toBeInTheDocument()
    expect(screen.getByText(/原子指标与映射数据集保留为 DAG 内部构成/)).toBeInTheDocument()
  })

  test('不展示原子指标和映射数据集，并从数据集展示区携带安全拓展上下文新建指标', async () => {
    const user = userEvent.setup()
    mockCatalog([
      metricSummary({ id: 'metric-atomic', name: '内部订单金额', type: 'ATOMIC' }),
      metricSummary({ id: 'metric-derived', name: '月订单金额', type: 'DERIVED' }),
    ])
    render(<MemoryRouter initialEntries={['/metrics']}><Routes>
      <Route path="/metrics" element={<MetricCatalogPage />} />
      <Route path="/metrics/new" element={<CatalogLocationProbe />} />
    </Routes></MemoryRouter>)

    const zone = await screen.findByRole('region', { name: '企业收入数据集指标展示区' })
    expect(within(zone).getByText('月订单金额')).toBeInTheDocument()
    expect(screen.queryByText('内部订单金额')).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '订单映射表指标展示区' })).not.toBeInTheDocument()

    await user.click(within(zone).getByRole('button', { name: '新建指标' }))
    expect(JSON.parse((await screen.findByLabelText('当前路由')).textContent || '{}')).toEqual({
      pathname: '/metrics/new',
      state: { preferredDatasetId: 'dataset-1', safeDatasetExtension: true },
    })
  })

  test('候选区支持多选并逐项创建和发布指标', async () => {
    const user = userEvent.setup()
    const candidates = [
      metricCandidate({ id: 'candidate-1', name: '订单金额', code: 'order_amount' }),
      metricCandidate({ id: 'candidate-2', name: '客户数', code: 'customer_count', status: 'NEEDS_REVIEW' }),
      metricCandidate({ id: 'candidate-3', name: '阻塞候选', code: 'blocked_metric', status: 'BLOCKED', blockReasons: ['来源不可用'] }),
    ]
    mockCatalog([], candidates)
    const accept = vi.spyOn(metricCandidateAPI, 'accept').mockImplementation(async id => ({
      candidate: { ...candidates.find(candidate => candidate.id === id)!, status: 'ACCEPTED', version: 2, acceptedMetricId: `metric-${id}` },
      metric: metricRecord({
        id: `metric-${id}`, code: candidates.find(candidate => candidate.id === id)!.code,
        name: candidates.find(candidate => candidate.id === id)!.name,
        status: 'DRAFT', currentPublishedVersionId: undefined,
      }),
    }))
    const publish = vi.spyOn(metricAPI, 'publish').mockResolvedValue(metricVersion())
    renderCatalog()

    await user.click(await screen.findByRole('tab', { name: /候选区/ }))
    await user.click(screen.getByLabelText('选择候选指标 订单金额'))
    await user.click(screen.getByLabelText('选择候选指标 客户数'))
    expect(screen.getByLabelText('选择候选指标 阻塞候选')).toBeDisabled()
    await user.click(screen.getByRole('button', { name: '发布选中指标（2）' }))

    expect(await screen.findByText('已成功发布 2 个指标')).toBeInTheDocument()
    expect(accept).toHaveBeenCalledTimes(2)
    expect(publish).toHaveBeenCalledTimes(2)
    expect(publish.mock.calls[0][1]).toMatchObject({
      draftVersionId: 'metric-draft-1',
      expectedVersion: 5,
      expectedDraftRecordVersion: 4,
      expectedDefinitionHash: 'c'.repeat(64),
      validationParameters: {},
    })
  })

  test('二次确认后按乐观锁删除指标并保留数据集展示区', async () => {
    const user = userEvent.setup()
    const metric = metricSummary()
    mockCatalog([metric])
    const remove = vi.spyOn(metricAPI, 'delete').mockResolvedValue()
    renderCatalog()

    const row = (await screen.findByText('营业收入')).closest('article')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '删除' }))
    const dialog = screen.getByRole('dialog', { name: '删除指标' })
    expect(within(dialog).getByText(/历史版本和审计记录仍会保留/)).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '确认删除' }))
    expect(remove).toHaveBeenCalledWith(metric.id, metric.version)
    expect(screen.queryByText('营业收入')).not.toBeInTheDocument()
    const zone = screen.getByRole('region', { name: '企业收入数据集指标展示区' })
    expect(zone).toBeInTheDocument()
    expect(within(zone).getByText('暂无派生或复合指标')).toBeInTheDocument()
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

function metricCandidate(overrides: Partial<MetricCandidate> = {}): MetricCandidate {
  return {
    id: 'candidate-1', datasetId: 'dataset-1', datasetVersionId: 'dataset-version-1',
    dslHash: 'e'.repeat(64), name: '订单金额', code: 'order_amount', description: '订单金额合计',
    status: 'READY', method: 'HYBRID', confidence: .95, proposedDefinition: metricDefinition(),
    sourceFieldIds: ['field_revenue'], evidence: [], assumptions: [], warnings: [], blockReasons: [],
    fingerprint: 'f'.repeat(64), version: 1, createdAt: '2026-07-20T00:00:00Z', updatedAt: '2026-07-20T00:00:00Z',
    ...overrides,
  }
}

const datasets: DatasetSummary[] = [
  { id: 'dataset-1', code: 'enterprise_revenue', name: '企业收入数据集', description: '', type: 'SINGLE_SOURCE', status: 'PUBLISHED', version: 3, dslHash: 'a'.repeat(64), currentPublishedVersionId: 'dataset-version-1', updatedAt: '2026-07-16T00:00:00Z' },
  { id: 'dataset-2', code: 'customer_profile', name: '客户主题数据集', description: '', type: 'SINGLE_SOURCE', status: 'PUBLISHED', version: 2, dslHash: 'b'.repeat(64), currentPublishedVersionId: 'dataset-version-2', updatedAt: '2026-07-16T00:00:00Z' },
  { id: 'dataset-3', code: 'mapped_orders', name: '订单映射表', description: '', type: 'MAPPED_TABLE', status: 'PUBLISHED', originTableId: 'table-orders', version: 1, dslHash: 'c'.repeat(64), currentPublishedVersionId: 'dataset-version-3', updatedAt: '2026-07-16T00:00:00Z' },
]

function CatalogLocationProbe() {
  const location = useLocation()
  return <output aria-label="当前路由">{JSON.stringify({ pathname: location.pathname, state: location.state })}</output>
}

function metricSummary(overrides: Partial<MetricSummary> = {}): MetricSummary {
  return {
    id: 'metric-1', code: 'revenue_total', name: '营业收入', description: '已确认订单的营业收入总额', type: 'DERIVED', status: 'PUBLISHED',
    version: 5, currentPublishedVersionId: 'metric-version-1', datasetId: 'dataset-1', datasetVersionId: 'dataset-version-1', updatedAt: '2026-07-19T04:30:00Z',
    ...overrides,
  }
}

function metricDefinition(overrides: Partial<MetricDefinition> = {}): MetricDefinition {
  return {
    schemaVersion: '1.0', metric: { code: 'revenue_total', name: '营业收入', description: '已确认订单的营业收入总额', type: 'DERIVED' },
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
