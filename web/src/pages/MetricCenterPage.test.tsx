import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { RequestError } from '../lib/api'
import {
  datasetAPI,
  type DatasetDSL,
  type DatasetSummary,
  type PublishedVersionRecord,
} from '../lib/datasets'
import {
  metricAPI,
  type MetricDefinition,
  type MetricRecord,
  type MetricVersionRecord,
} from '../lib/metrics'
import { MetricCenterPage } from './MetricCenterPage'

afterEach(() => vi.restoreAllMocks())

describe('指标中心原子指标编辑', () => {
  test('从精确数据集版本选择字段和维度后创建结构化原子指标', async () => {
    const user = userEvent.setup()
    const mocks = mockMetricCenter()
    let created: MetricRecord | undefined
    mocks.createSpy.mockImplementation(async definition => {
      created = metricRecord({
        code: definition.metric.code,
        name: definition.metric.name,
        description: definition.metric.description,
        definition,
      })
      return created
    })
    mocks.getSpy.mockImplementation(async () => created ?? metricRecord())
    renderMetricCenter('/metrics')

    await user.selectOptions(await screen.findByLabelText('指标数据集'), dataset.id)
    await waitFor(() => expect(mocks.datasetVersionListSpy).toHaveBeenCalledWith(dataset.id))
    await user.selectOptions(screen.getByLabelText('指标数据集版本'), dataVersion.id)
    await screen.findByRole('option', { name: /营业收入/ })
    await user.selectOptions(screen.getByLabelText('原子指标字段'), 'field_revenue')
    await user.type(screen.getByLabelText('指标编码'), 'revenue_total')
    await user.type(screen.getByLabelText('指标名称'), '营业收入')
    await user.click(screen.getByLabelText('允许维度 地区'))
    await user.click(screen.getByRole('button', { name: '创建草稿' }))

    await waitFor(() => expect(mocks.createSpy).toHaveBeenCalledTimes(1))
    const definition = mocks.createSpy.mock.calls[0]?.[0]
    expect(definition).toMatchObject({
      schemaVersion: '1.0',
      metric: { code: 'revenue_total', name: '营业收入', type: 'ATOMIC' },
      datasetId: dataset.id,
      datasetVersionId: dataVersion.id,
      expression: { type: 'FIELD_REF', fieldId: 'field_revenue' },
      aggregation: 'SUM',
      allowedDimensions: [{
        fieldId: 'field_region', name: '地区', hierarchyFieldIds: ['field_region'], sortDirection: 'ASC', nullLabel: '未分类',
      }],
      roundingMode: 'HALF_UP', nullHandling: 'IGNORE', divisionByZero: 'NULL',
    })
    expect(await screen.findByText('指标草稿已创建。')).toBeInTheDocument()
  })

  test('READ、MANAGE、PUBLISH 分离时发布者无需暗中保存草稿', async () => {
    const user = userEvent.setup()
    const definition = metricDefinition()
    const loaded = metricRecord({ definition })
    const reconciled = metricRecord({ ...loaded, version: 5, status: 'PUBLISHED', currentPublishedVersionId: metricVersion.id })
    const versionWithParameter = datasetVersion({
      dsl: { ...datasetDSL, parameters: [{ code: 'start_date', name: '开始日期', dataType: 'DATE', required: true, multiValue: false }] },
    })
    const mocks = mockMetricCenter({ loaded, dataVersion: versionWithParameter })
    mocks.permissionSpy.mockImplementation(async (_id, action) => ({ allowed: action !== 'MANAGE' }))
    mocks.getSpy.mockResolvedValueOnce(loaded).mockResolvedValueOnce(reconciled)
    mocks.publishSpy.mockResolvedValue(metricVersion)
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000099')
    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    const parameter = await screen.findByLabelText('指标参数 start_date')
    expect(screen.getByLabelText('指标名称')).toBeDisabled()
    expect(parameter).toBeEnabled()
    expect(screen.getByRole('button', { name: '保存草稿' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '发布指标' })).toBeEnabled()
    await user.type(parameter, '2026-07-01')
    await user.click(screen.getByRole('button', { name: '发布指标' }))

    expect(mocks.updateSpy).not.toHaveBeenCalled()
    expect(mocks.publishSpy).toHaveBeenCalledWith(loaded.id, {
      draftVersionId: loaded.draftVersionId,
      expectedVersion: loaded.version,
      expectedDraftRecordVersion: loaded.draftRecordVersion,
      expectedDefinitionHash: loaded.definitionHash,
      validationParameters: { start_date: '2026-07-01' },
    }, '00000000-0000-4000-8000-000000000099')
    expect(await screen.findByText(`指标已发布 · V1 · 精确版本 ${metricVersion.id}`)).toBeInTheDocument()
  })

  test('只有指标只读权限且无数据集目录权限时仍加载指标自身信息', async () => {
    const loaded = metricRecord({ currentPublishedVersionId: metricVersion.id, status: 'PUBLISHED' })
    const mocks = mockMetricCenter({ loaded })
    mocks.permissionSpy.mockImplementation(async (_id, action) => ({ allowed: action === 'READ' }))
    mocks.metricVersionListSpy.mockResolvedValue({ items: [metricVersionSummary(metricVersion)], total: 1, limit: 50, offset: 0 })
    mocks.metricVersionGetSpy.mockResolvedValue(metricVersion)
    mocks.usageSpy.mockResolvedValue({ reportDraftReferences: 2, downstreamDraftReferences: 3, downstreamPublishedReferences: 4, activeQueryRuns: 0 })
    mocks.datasetListSpy.mockRejectedValue(new RequestError({ code: 'PERMISSION_DENIED', message: '无数据集目录权限' }, 403))
    mocks.datasetVersionListSpy.mockRejectedValue(new RequestError({ code: 'PERMISSION_DENIED', message: '无数据集版本权限' }, 403))
    mocks.datasetGetVersionSpy.mockRejectedValue(new RequestError({ code: 'PERMISSION_DENIED', message: '无精确数据集版本权限' }, 403))

    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    expect(await screen.findByDisplayValue(loaded.name)).toBeDisabled()
    const definitionSnapshot = screen.getByRole('region', { name: '指标只读口径' })
    const definitionSummary = definitionSnapshot.querySelector('dl')
    expect(definitionSummary).not.toBeNull()
    expect(within(definitionSummary as HTMLElement).getByText(new RegExp(loaded.definition.datasetVersionId))).toBeInTheDocument()
    expect(within(definitionSummary as HTMLElement).getByText(/field_revenue/)).toBeInTheDocument()
    expect(within(definitionSummary as HTMLElement).getByText(/地区（field_region）/)).toBeInTheDocument()
    const manager = await screen.findByRole('region', { name: '指标发布版本管理' })
    expect(await within(manager).findByText('当前发布版本')).toBeInTheDocument()
    expect(within(manager).getByText('2')).toBeInTheDocument()
    expect(await within(manager).findByText(/当前无法读取精确数据集版本/)).toBeInTheDocument()
    expect(within(manager).getByRole('button', { name: '试算精确版本' })).toBeDisabled()
    expect(screen.queryByText(/加载指标编辑器失败/)).not.toBeInTheDocument()
    expect(screen.queryByText(/加载指标版本失败/)).not.toBeInTheDocument()
  })

  test('数据集目录故障时保留指标信息并展示真实错误', async () => {
    const loaded = metricRecord()
    const mocks = mockMetricCenter({ loaded })
    mocks.datasetListSpy.mockRejectedValue(new RequestError({ code: 'DATASET_FAILED', message: '数据集目录服务异常' }, 500))

    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    expect(await screen.findByDisplayValue(loaded.name)).toBeInTheDocument()
    expect(await screen.findByText('加载数据集目录失败：数据集目录服务异常')).toBeInTheDocument()
    expect(screen.queryByText(/当前账号没有读取该指标的权限/)).not.toBeInTheDocument()
  })

  test('有管理权限但精确数据集快照不可读时锁定定义操作', async () => {
    const loaded = metricRecord({ currentPublishedVersionId: metricVersion.id, status: 'PUBLISHED' })
    const mocks = mockMetricCenter({ loaded })
    mocks.metricVersionListSpy.mockResolvedValue({ items: [metricVersionSummary(metricVersion)], total: 1, limit: 50, offset: 0 })
    mocks.metricVersionGetSpy.mockResolvedValue(metricVersion)
    const denied = new RequestError({ code: 'PERMISSION_DENIED', message: '无精确数据集版本权限' }, 403)
    mocks.datasetListSpy.mockRejectedValue(denied)
    mocks.datasetVersionListSpy.mockRejectedValue(denied)
    mocks.datasetGetVersionSpy.mockRejectedValue(denied)

    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    expect(await screen.findByDisplayValue(loaded.name)).toBeDisabled()
    expect(screen.getByLabelText('指标数据集版本')).toBeDisabled()
    expect(screen.getByRole('button', { name: '保存草稿' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '试算' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '发布指标' })).toBeDisabled()
    const manager = await screen.findByRole('region', { name: '指标发布版本管理' })
    expect(await within(manager).findByRole('button', { name: '废弃版本' })).toBeEnabled()
    expect(within(manager).getByRole('button', { name: '试算精确版本' })).toBeDisabled()
  })

  test('发布结果未知时锁定编辑并使用同一请求和幂等键原样重试', async () => {
    const user = userEvent.setup()
    const loaded = metricRecord()
    const reconciled = metricRecord({ ...loaded, version: 5, status: 'PUBLISHED', currentPublishedVersionId: metricVersion.id })
    const mocks = mockMetricCenter({ loaded })
    mocks.getSpy.mockResolvedValueOnce(loaded).mockResolvedValueOnce(reconciled)
    let rejectFirst!: (reason: unknown) => void
    mocks.publishSpy
      .mockReturnValueOnce(new Promise<MetricVersionRecord>((_, reject) => { rejectFirst = reject }))
      .mockResolvedValueOnce(metricVersion)
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000088')
    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    await screen.findByDisplayValue(loaded.name)
    expect(screen.getByLabelText('指标数据集')).toBeDisabled()
    await user.click(screen.getByRole('button', { name: '发布指标' }))
    await waitFor(() => expect(mocks.publishSpy).toHaveBeenCalledTimes(1))
    expect(screen.getByLabelText('指标名称')).toBeDisabled()
    await act(async () => rejectFirst(new TypeError('Failed to fetch')))
    expect(await screen.findByText(/发布结果尚未确认/)).toHaveTextContent('Failed to fetch')
    expect(screen.getByRole('button', { name: '保存草稿' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: '重试刚才发布' }))

    expect(await screen.findByText(`指标已发布 · V1 · 精确版本 ${metricVersion.id}`)).toBeInTheDocument()
    expect(mocks.publishSpy).toHaveBeenCalledTimes(2)
    expect(mocks.publishSpy.mock.calls[1]).toEqual(mocks.publishSpy.mock.calls[0])
  })

  test('模糊发布重试返回冲突时停止重试并要求重新加载聚合状态', async () => {
    const user = userEvent.setup()
    const loaded = metricRecord()
    const mocks = mockMetricCenter({ loaded })
    mocks.publishSpy
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockRejectedValueOnce(new RequestError({ code: 'METRIC_VERSION_CONFLICT', message: '指标已被其他请求修改' }, 409))
    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    await screen.findByDisplayValue(loaded.name)
    await user.click(screen.getByRole('button', { name: '发布指标' }))
    await user.click(await screen.findByRole('button', { name: '重试刚才发布' }))

    expect(await screen.findByText(/无法从客户端确认远端聚合状态/)).toHaveTextContent('409')
    expect(screen.queryByRole('button', { name: '重试刚才发布' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新加载指标' })).toBeEnabled()
    expect(screen.getByLabelText('指标名称')).toBeDisabled()
  })
})

describe('指标不可变版本管理', () => {
  test('展示占用并按版本自己的数据集参数执行精确试算', async () => {
    const user = userEvent.setup()
    const loaded = metricRecord({ currentPublishedVersionId: metricVersion.id, status: 'PUBLISHED' })
    const parameterVersion = datasetVersion({
      dsl: { ...datasetDSL, parameters: [{ code: 'month', name: '统计月份', dataType: 'STRING', required: true, multiValue: false }] },
    })
    const published = metricPublishedVersion({
      definition: metricDefinition({
        datasetVersionId: parameterVersion.id,
        unit: '万元',
        numberFormat: '#,##0.0000',
        decimalScale: 4,
        additivity: 'SEMI_ADDITIVE',
        nonAdditiveDimensionFieldIds: ['field_region'],
        allowedDimensions: [{ fieldId: 'field_region', name: '地区', hierarchyFieldIds: ['field_region', 'field_city'], sortDirection: 'DESC', nullLabel: '其他地区' }],
      }),
      datasetVersionId: parameterVersion.id,
    })
    const mocks = mockMetricCenter({ loaded, dataVersion: parameterVersion, published })
    mocks.metricVersionListSpy.mockResolvedValue({ items: [metricVersionSummary(published)], total: 1, limit: 50, offset: 0 })
    mocks.metricVersionGetSpy.mockResolvedValue(published)
    mocks.usageSpy.mockResolvedValue({ reportDraftReferences: 3, downstreamDraftReferences: 4, downstreamPublishedReferences: 5, activeQueryRuns: 6 })
    mocks.versionPreviewSpy.mockResolvedValue({ queryId: 'query-version', columns: ['value'], rows: [['120.50']], rowCount: 1, durationMs: 6 })
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000077')
    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    const manager = await screen.findByRole('region', { name: '指标发布版本管理' })
    expect(await within(manager).findByText('当前发布版本')).toBeInTheDocument()
    expect(within(manager).getByText('3')).toBeInTheDocument()
    expect(within(manager).getByText('4')).toBeInTheDocument()
    expect(within(manager).getByText('5')).toBeInTheDocument()
    const completeDefinition = JSON.parse(within(manager).getByLabelText('指标版本完整定义 JSON').textContent ?? '{}') as MetricDefinition
    expect(completeDefinition).toMatchObject({
      metric: { code: 'revenue_total', name: '营业收入', type: 'ATOMIC' },
      unit: '万元', numberFormat: '#,##0.0000', decimalScale: 4,
      roundingMode: 'HALF_UP', nullHandling: 'IGNORE', divisionByZero: 'NULL',
      additivity: 'SEMI_ADDITIVE', nonAdditiveDimensionFieldIds: ['field_region'],
      allowedDimensions: [{ hierarchyFieldIds: ['field_region', 'field_city'], sortDirection: 'DESC', nullLabel: '其他地区' }],
    })
    await user.type(within(manager).getByLabelText('指标版本参数 month'), '2026-06')
    await user.click(within(manager).getByRole('button', { name: '试算精确版本' }))

    expect(mocks.versionPreviewSpy).toHaveBeenCalledWith(loaded.id, published.id, {
      queryId: '00000000-0000-4000-8000-000000000077',
      parameters: { month: '2026-06' },
      dimensionFieldIds: ['field_region'],
      maxRows: 0,
    })
    expect(await within(manager).findByText('120.50')).toBeInTheDocument()
  })

  test('废弃版本使用指标聚合版本并在成功后重新读取聚合状态', async () => {
    const user = userEvent.setup()
    const loaded = metricRecord({ currentPublishedVersionId: metricVersion.id, status: 'PUBLISHED', version: 8 })
    const changed = metricPublishedVersion({ status: 'DEPRECATED', metricRecordVersion: 9 })
    const reconciled = metricRecord({ ...loaded, version: 9, status: 'DEPRECATED', currentPublishedVersionId: undefined })
    const mocks = mockMetricCenter({ loaded })
    mocks.getSpy.mockResolvedValueOnce(loaded).mockResolvedValueOnce(reconciled)
    mocks.metricVersionListSpy.mockResolvedValue({ items: [metricVersionSummary(metricVersion)], total: 1, limit: 50, offset: 0 })
    mocks.metricVersionGetSpy.mockResolvedValue(metricVersion)
    mocks.transitionSpy.mockImplementation(async () => {
      mocks.metricVersionGetSpy.mockResolvedValue(changed)
      mocks.metricVersionListSpy.mockResolvedValue({ items: [metricVersionSummary(changed)], total: 1, limit: 50, offset: 0 })
      return changed
    })
    renderMetricCenter(`/metrics/${loaded.id}/edit`)

    const manager = await screen.findByRole('region', { name: '指标发布版本管理' })
    await within(manager).findByText('当前发布版本')
    await user.click(within(manager).getByRole('button', { name: '废弃版本' }))

    expect(mocks.transitionSpy).toHaveBeenCalledWith(loaded.id, metricVersion.id, {
      expectedVersion: metricVersion.metricRecordVersion,
      expectedStatus: 'PUBLISHED',
      targetStatus: 'DEPRECATED',
    })
    expect(await screen.findByText('指标版本 V1 已废弃')).toBeInTheDocument()
    expect(within(manager).queryByRole('button', { name: '废弃版本' })).not.toBeInTheDocument()
  })
})

test('路由切换后丢弃旧指标迟到的聚合响应', async () => {
  const user = userEvent.setup()
  const first = deferred<MetricRecord>()
  const oldMetric = metricRecord({ id: 'metric-old', name: '旧指标', definition: metricDefinition({ metric: { code: 'old_metric', name: '旧指标', description: '', type: 'ATOMIC' } }) })
  const newMetric = metricRecord({ id: 'metric-new', name: '新指标', definition: metricDefinition({ metric: { code: 'new_metric', name: '新指标', description: '', type: 'ATOMIC' } }) })
  const mocks = mockMetricCenter({ loaded: oldMetric })
  mocks.getSpy.mockImplementation(id => id === oldMetric.id ? first.promise : Promise.resolve(newMetric))
  render(<MemoryRouter initialEntries={[`/metrics/${oldMetric.id}/edit`]}><MetricRouteSwitch /></MemoryRouter>)

  await waitFor(() => expect(mocks.getSpy).toHaveBeenCalledWith(oldMetric.id))
  await user.click(screen.getByRole('button', { name: '切换指标' }))
  expect(await screen.findByDisplayValue('新指标')).toBeInTheDocument()
  await act(async () => first.resolve(oldMetric))

  await waitFor(() => expect(screen.queryByDisplayValue('旧指标')).not.toBeInTheDocument())
  expect(screen.getByDisplayValue('新指标')).toBeInTheDocument()
})

const dataset: DatasetSummary = {
  id: 'dataset-1', code: 'enterprise_revenue', name: '企业收入数据集', description: '', type: 'SINGLE_SOURCE',
  status: 'PUBLISHED', version: 4, dslHash: 'a'.repeat(64), currentPublishedVersionId: 'dataset-version-1', updatedAt: '2026-07-16T00:00:00Z',
}

const datasetDSL: DatasetDSL = {
  dslVersion: '1.0',
  dataset: { code: dataset.code, name: dataset.name, type: 'SINGLE_SOURCE' },
  nodes: [], joins: [], filters: [], groupBy: [], having: [], sorts: [], parameters: [],
  fields: [
    { id: 'field_revenue', code: 'revenue', name: '营业收入', role: 'MEASURE', canonicalType: 'DECIMAL', visible: true },
    { id: 'field_region', code: 'region', name: '地区', role: 'DIMENSION', canonicalType: 'STRING', visible: true },
    { id: 'field_month', code: 'month', name: '统计月份', role: 'TIME', canonicalType: 'DATE', visible: true },
  ],
}

const dataVersion = datasetVersion()

function datasetVersion(overrides: Partial<PublishedVersionRecord> = {}): PublishedVersionRecord {
  return {
    id: 'dataset-version-1', datasetId: dataset.id, versionNo: 1, status: 'PUBLISHED', dslVersion: '1.0',
    dslHash: 'b'.repeat(64), planHash: 'c'.repeat(64), dsl: datasetDSL, logicalPlan: {},
    publishedAt: '2026-07-16T01:00:00Z', publishedBy: 'user-1', datasetRecordVersion: 4,
    draftVersionId: 'dataset-draft-1', draftRecordVersion: 3, ...overrides,
  }
}

function metricDefinition(overrides: Partial<MetricDefinition> = {}): MetricDefinition {
  return {
    schemaVersion: '1.0',
    metric: { code: 'revenue_total', name: '营业收入', description: '营业收入总额', type: 'ATOMIC' },
    datasetId: dataset.id,
    datasetVersionId: dataVersion.id,
    expression: { type: 'FIELD_REF', fieldId: 'field_revenue' },
    aggregation: 'SUM', unit: '元', numberFormat: '#,##0.00', timeFieldId: 'field_month', timeGrain: 'MONTH',
    additivity: 'ADDITIVE', nonAdditiveDimensionFieldIds: [],
    allowedDimensions: [{ fieldId: 'field_region', name: '地区', hierarchyFieldIds: ['field_region'], sortDirection: 'ASC', nullLabel: '未分类' }],
    decimalScale: 2, roundingMode: 'HALF_UP', nullHandling: 'IGNORE', divisionByZero: 'NULL',
    ...overrides,
  }
}

function metricRecord(overrides: Partial<MetricRecord> = {}): MetricRecord {
  const definition = overrides.definition ?? metricDefinition()
  return {
    id: 'metric-1', code: definition.metric.code, name: definition.metric.name, description: definition.metric.description,
    type: definition.metric.type, status: 'DRAFT', version: 4, draftVersionId: 'metric-draft-1', draftVersionNo: 1,
    draftRecordVersion: 3, datasetId: definition.datasetId, datasetVersionId: definition.datasetVersionId,
    definitionHash: 'd'.repeat(64), definition, createdAt: '2026-07-16T00:00:00Z', updatedAt: '2026-07-16T00:00:00Z',
    ...overrides,
  }
}

function metricPublishedVersion(overrides: Partial<MetricVersionRecord> = {}): MetricVersionRecord {
  const definition = overrides.definition ?? metricDefinition()
  return {
    id: 'metric-version-1', metricId: 'metric-1', metricRecordVersion: 5,
    datasetId: definition.datasetId, datasetVersionId: definition.datasetVersionId,
    draftVersionId: 'metric-draft-1', draftRecordVersion: 3, versionNo: 1, status: 'PUBLISHED',
    definitionHash: 'e'.repeat(64), definition, publishedAt: '2026-07-16T02:00:00Z', publishedBy: 'user-1',
    ...overrides,
  }
}

const metricVersion = metricPublishedVersion()

function metricVersionSummary(version: MetricVersionRecord) {
  return {
    id: version.id, metricId: version.metricId, versionNo: version.versionNo, status: version.status,
    datasetId: version.datasetId, datasetVersionId: version.datasetVersionId,
    draftRecordVersion: version.draftRecordVersion, definitionHash: version.definitionHash,
    publishedAt: version.publishedAt, publishedBy: version.publishedBy,
  }
}

function mockMetricCenter(options: { loaded?: MetricRecord; dataVersion?: PublishedVersionRecord; published?: MetricVersionRecord } = {}) {
  const loaded = options.loaded ?? metricRecord()
  const selectedDataVersion = options.dataVersion ?? dataVersion
  const published = options.published ?? metricVersion
  const datasetListSpy = vi.spyOn(datasetAPI, 'list').mockResolvedValue({ items: [dataset], total: 1, limit: 200, offset: 0 })
  const datasetVersionListSpy = vi.spyOn(datasetAPI, 'listVersions').mockResolvedValue({ items: [versionSummary(selectedDataVersion)], total: 1, limit: 50, offset: 0 })
  const datasetGetVersionSpy = vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(selectedDataVersion)
  vi.spyOn(metricAPI, 'list').mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
  const permissionSpy = vi.spyOn(metricAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  const getSpy = vi.spyOn(metricAPI, 'get').mockResolvedValue(loaded)
  const createSpy = vi.spyOn(metricAPI, 'create').mockResolvedValue(loaded)
  const updateSpy = vi.spyOn(metricAPI, 'update').mockResolvedValue(loaded)
  vi.spyOn(metricAPI, 'validate').mockResolvedValue({ valid: true })
  vi.spyOn(metricAPI, 'preview').mockResolvedValue({ queryId: 'query-draft', columns: ['value'], rows: [['100']], rowCount: 1, durationMs: 5 })
  const publishSpy = vi.spyOn(metricAPI, 'publish')
  const metricVersionListSpy = vi.spyOn(metricAPI, 'listVersions').mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
  const metricVersionGetSpy = vi.spyOn(metricAPI, 'getVersion').mockResolvedValue(published)
  const usageSpy = vi.spyOn(metricAPI, 'getVersionUsage').mockResolvedValue({ reportDraftReferences: 0, downstreamDraftReferences: 0, downstreamPublishedReferences: 0, activeQueryRuns: 0 })
  const versionPreviewSpy = vi.spyOn(metricAPI, 'previewVersion').mockResolvedValue({ queryId: 'query-version', columns: ['value'], rows: [['100']], rowCount: 1, durationMs: 5 })
  const transitionSpy = vi.spyOn(metricAPI, 'transitionVersion').mockResolvedValue(metricPublishedVersion({ status: 'DEPRECATED' }))
  return {
    datasetListSpy, datasetVersionListSpy, datasetGetVersionSpy, permissionSpy, getSpy, createSpy, updateSpy, publishSpy,
    metricVersionListSpy, metricVersionGetSpy, usageSpy, versionPreviewSpy, transitionSpy,
  }
}

function versionSummary(version: PublishedVersionRecord) {
  return {
    id: version.id, datasetId: version.datasetId, versionNo: version.versionNo, status: version.status,
    dslVersion: version.dslVersion, dslHash: version.dslHash, planHash: version.planHash,
    draftRecordVersion: version.draftRecordVersion, publishedAt: version.publishedAt, publishedBy: version.publishedBy,
  }
}

function renderMetricCenter(path: string) {
  return render(<MemoryRouter initialEntries={[path]}><Routes>
    <Route path="/metrics" element={<MetricCenterPage />} />
    <Route path="/metrics/:metricId/edit" element={<MetricCenterPage />} />
  </Routes></MemoryRouter>)
}

function MetricRouteSwitch() {
  const navigate = useNavigate()
  return <><button type="button" onClick={() => navigate('/metrics/metric-new/edit')}>切换指标</button><Routes>
    <Route path="/metrics/:metricId/edit" element={<MetricCenterPage />} />
  </Routes></>
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
