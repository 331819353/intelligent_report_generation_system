import { act, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { datasetAPI, type AssetColumn, type AssetTable, type DatasetDSL, type DatasetRecord, type PublishedVersionRecord } from '../lib/datasets'
import { DatasetDesignerPage, PreviewTable } from './DatasetDesignerPage'

afterEach(() => vi.restoreAllMocks())

test('数据预览展示结构化 Join 风险告警', () => {
  render(<PreviewTable preview={{
    queryId: 'query-1', columns: ['revenue'], rows: [[20]], rowCount: 1, durationMs: 8,
    warnings: [{ code: 'JOIN_FANOUT_RISK', message: '关联结果可能发生扇出。', joinId: 'orders_customers', estimatedRows: 4 }],
  }} />)
  expect(screen.getByRole('region', { name: 'Join 风险提示' })).toBeInTheDocument()
  expect(screen.getByText('关联结果可能发生扇出。')).toBeInTheDocument()
  expect(screen.getByText('预计 4 行')).toBeInTheDocument()
})

describe('数据集发布审批', () => {
  test('只冻结最近保存的草稿并提交审批，不直接生成发布版本', async () => {
    const user = userEvent.setup()
    const saved = datasetRecord({ version: 5, draftRecordVersion: 4, dslHash: 'b'.repeat(64) })
    const { requestPublicationSpy, updateSpy } = mockDesigner(saved)
    requestPublicationSpy.mockResolvedValue({
      id: 'publication-request-1', datasetId: saved.id, status: 'PENDING', version: 1,
      draftVersionId: saved.draftVersionId, expectedDatasetVersion: saved.version,
      expectedDraftRecordVersion: saved.draftRecordVersion, expectedDslHash: saved.dslHash,
      expectedPlanHash: saved.planHash, requesterId: 'user-1', requestNote: '',
      submittedAt: '2026-07-20T10:00:00Z', updatedAt: '2026-07-20T10:00:00Z',
    })
    renderDesigner()

    await screen.findByLabelText('预览参数 start_date')
    await user.click(screen.getByRole('button', { name: '保存草稿' }))
    await screen.findByText('草稿已保存 · 版本 5')
    await user.type(screen.getByLabelText('预览参数 start_date'), '2026-01-01')
    await user.click(screen.getByRole('button', { name: '提交发布审批' }))

    expect(await screen.findByText('发布审批已提交 · publication-request-1 · 当前状态：PENDING')).toBeInTheDocument()
    expect(requestPublicationSpy).toHaveBeenCalledWith(saved.id, {
      draftVersionId: saved.draftVersionId,
      expectedVersion: saved.version,
      expectedDraftRecordVersion: saved.draftRecordVersion,
      expectedDslHash: saved.dslHash,
      validationParameters: { start_date: '2026-01-01' },
    })
    expect(updateSpy).toHaveBeenCalledTimes(1)
  })

  test('有未保存修改时不会暗中保存或提交审批', async () => {
    const user = userEvent.setup()
    const { requestPublicationSpy, updateSpy } = mockDesigner(datasetRecord())
    renderDesigner()

    const name = await screen.findByLabelText('数据集名称')
    await user.clear(name)
    await user.type(name, '尚未保存的新名称')
    await user.click(screen.getByRole('button', { name: '提交发布审批' }))

    expect(await screen.findByText('当前草稿有未保存修改，请先保存草稿后再提交发布审批')).toBeInTheDocument()
    expect(updateSpy).not.toHaveBeenCalled()
    expect(requestPublicationSpy).not.toHaveBeenCalled()
  })

  test('审批提交失败时展示稳定错误并恢复按钮', async () => {
    const user = userEvent.setup()
    const loaded = datasetRecord()
    const { requestPublicationSpy } = mockDesigner(loaded)
    requestPublicationSpy.mockRejectedValue(new Error('当前草稿已有待审批申请'))
    renderDesigner()

    await user.type(await screen.findByLabelText('预览参数 start_date'), '2026-07-01')
    await user.click(screen.getByRole('button', { name: '提交发布审批' }))

    expect(await screen.findByText('当前草稿已有待审批申请')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: '提交发布审批' })).toBeEnabled())
  })
})

describe('已发布版本管理', () => {
  test('标记当前发布版本、展示使用汇总，并使用快照参数精确预览', async () => {
    const user = userEvent.setup()
    const snapshotDSL: DatasetDSL = {
      ...dsl,
      parameters: [{ code: 'snapshot_date', name: '快照日期', dataType: 'DATE', required: true, multiValue: false }],
    }
    const published = publishedVersion({ id: 'version-current', versionNo: 2, dsl: snapshotDSL })
    const olderPublished = publishedVersion({ id: 'version-older-published', versionNo: 1 })
    const loaded = datasetRecord({ currentPublishedVersionId: published.id })
    const { getSpy, listVersionsSpy, getVersionSpy, usageSpy, versionPreviewSpy } = mockDesigner(loaded)
    getSpy.mockResolvedValue(loaded)
    listVersionsSpy.mockResolvedValue({ items: [olderPublished, published], total: 2, limit: 50, offset: 0 })
    getVersionSpy.mockResolvedValue(published)
    usageSpy.mockResolvedValue({ reportDraftReferences: 11, downstreamDraftReferences: 12, downstreamPublishedReferences: 13, activeQueryRuns: 14 })
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000077')
    renderDesigner()

    const manager = await screen.findByRole('region', { name: '已发布版本管理' })
    expect(await within(manager).findByText('当前发布版本')).toBeInTheDocument()
    expect(within(manager).getAllByText('当前发布')).toHaveLength(1)
    expect(within(manager).getByText(published.id)).toBeInTheDocument()
    expect(within(manager).getByText('11')).toBeInTheDocument()
    expect(within(manager).getByText('12')).toBeInTheDocument()
    expect(within(manager).getByText('13')).toBeInTheDocument()
    expect(within(manager).getByText('14')).toBeInTheDocument()

    await user.type(within(manager).getByLabelText('版本参数 snapshot_date'), '2026-07-01')
    await user.click(within(manager).getByRole('button', { name: '预览精确版本' }))

    expect(versionPreviewSpy).toHaveBeenCalledWith(
      loaded.id,
      published.id,
      '00000000-0000-4000-8000-000000000077',
      { snapshot_date: '2026-07-01' },
    )
    expect(await within(manager).findByText('2026-01-01')).toBeInTheDocument()
  })

  test('按聚合记录版本执行单向状态迁移并刷新当前发布指针', async () => {
    const user = userEvent.setup()
    const published = publishedVersion({ id: 'version-to-stale', versionNo: 3 })
    const changed = publishedVersion({ ...published, status: 'STALE', datasetRecordVersion: 9 })
    const loaded = datasetRecord({ version: 8, currentPublishedVersionId: published.id })
    const reconciled = datasetRecord({ ...loaded, version: 9, status: 'STALE', currentPublishedVersionId: undefined })
    const { getSpy, listVersionsSpy, getVersionSpy, transitionSpy } = mockDesigner(loaded)
    getSpy.mockResolvedValueOnce(loaded).mockResolvedValue(reconciled)
    listVersionsSpy.mockResolvedValue({ items: [published], total: 1, limit: 50, offset: 0 })
    getVersionSpy.mockResolvedValue(published)
    transitionSpy.mockImplementation(async () => {
      getVersionSpy.mockResolvedValue(changed)
      return changed
    })
    renderDesigner()

    const manager = await screen.findByRole('region', { name: '已发布版本管理' })
    await within(manager).findByText('当前发布版本')
    await user.click(within(manager).getByRole('button', { name: '标记为失效' }))

    expect(transitionSpy).toHaveBeenCalledWith(loaded.id, published.id, {
      expectedVersion: loaded.version,
      expectedStatus: 'PUBLISHED',
      targetStatus: 'STALE',
    })
    expect(await screen.findByText('版本 V3 已更新为 STALE')).toBeInTheDocument()
    await waitFor(() => expect(within(manager).getByRole('button', { name: '预览精确版本' })).toBeDisabled())
    expect(within(manager).queryByRole('button', { name: '标记为失效' })).not.toBeInTheDocument()
    expect(within(manager).getByRole('button', { name: '废弃版本' })).toBeEnabled()
  })

  test('状态迁移成功但聚合 GET 拒绝时保留新状态并锁定后续操作', async () => {
    const user = userEvent.setup()
    const published = publishedVersion({ id: 'version-get-failed', versionNo: 4 })
    const changed = publishedVersion({ ...published, status: 'STALE', datasetRecordVersion: 10 })
    const loaded = datasetRecord({ version: 9, currentPublishedVersionId: published.id })
    const { getSpy, listVersionsSpy, getVersionSpy, transitionSpy } = mockDesigner(loaded)
    getSpy.mockResolvedValueOnce(loaded).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    listVersionsSpy.mockResolvedValue({ items: [published], total: 1, limit: 50, offset: 0 })
    getVersionSpy.mockResolvedValue(published)
    transitionSpy.mockResolvedValue(changed)
    renderDesigner()

    const manager = await screen.findByRole('region', { name: '已发布版本管理' })
    await within(manager).findByText('当前发布版本')
    await user.click(within(manager).getByRole('button', { name: '标记为失效' }))

    expect(await screen.findByText('版本 V4 已更新为 STALE')).toBeInTheDocument()
    expect(await screen.findByText(/版本状态已更新，但无法确认最新聚合状态/)).toHaveTextContent('Failed to fetch')
    expect(screen.getByRole('button', { name: '重新加载草稿' })).toBeEnabled()
    expect(within(manager).getByRole('button', { name: '预览精确版本' })).toBeDisabled()
    expect(within(manager).getByRole('button', { name: '废弃版本' })).toBeDisabled()
    expect(within(manager).queryByRole('button', { name: '标记为失效' })).not.toBeInTheDocument()
  })

  test('状态迁移成功但聚合 GET 低于响应下界时保留新状态并要求重载', async () => {
    const user = userEvent.setup()
    const published = publishedVersion({ id: 'version-low-watermark', versionNo: 5 })
    const changed = publishedVersion({ ...published, status: 'DEPRECATED', datasetRecordVersion: 12 })
    const loaded = datasetRecord({ version: 11, currentPublishedVersionId: published.id })
    const staleAggregate = datasetRecord({ ...loaded, version: 11 })
    const { getSpy, listVersionsSpy, getVersionSpy, transitionSpy } = mockDesigner(loaded)
    getSpy.mockResolvedValueOnce(loaded).mockResolvedValueOnce(staleAggregate)
    listVersionsSpy.mockResolvedValue({ items: [published], total: 1, limit: 50, offset: 0 })
    getVersionSpy.mockResolvedValue(published)
    transitionSpy.mockResolvedValue(changed)
    renderDesigner()

    const manager = await screen.findByRole('region', { name: '已发布版本管理' })
    await within(manager).findByText('当前发布版本')
    await user.click(within(manager).getByRole('button', { name: '废弃版本' }))

    expect(await screen.findByText('版本 V5 已更新为 DEPRECATED')).toBeInTheDocument()
    expect(await screen.findByText(/聚合版本低于状态迁移响应下界/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新加载草稿' })).toBeEnabled()
    expect(within(manager).getByRole('button', { name: '预览精确版本' })).toBeDisabled()
    expect(within(manager).queryByRole('button', { name: '废弃版本' })).not.toBeInTheDocument()
  })

  test('READ、MANAGE 与 PUBLISH 能力分别约束读取、编辑和版本写入', async () => {
    const published = publishedVersion()
    const { permissionSpy, listVersionsSpy, getVersionSpy } = mockDesigner(datasetRecord())
    permissionSpy.mockImplementation(async (_id, action) => ({ allowed: action === 'READ' }))
    listVersionsSpy.mockResolvedValue({ items: [published], total: 1, limit: 50, offset: 0 })
    getVersionSpy.mockResolvedValue(published)
    renderDesigner()

    const manager = await screen.findByRole('region', { name: '已发布版本管理' })
    await within(manager).findByText(published.id)
    expect(screen.getByRole('button', { name: '保存草稿' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '提交发布审批' })).toBeDisabled()
    expect(within(manager).getByRole('button', { name: '标记为失效' })).toBeDisabled()
    expect(within(manager).getByRole('button', { name: '废弃版本' })).toBeDisabled()
    expect(within(manager).getByRole('button', { name: '预览精确版本' })).toBeEnabled()
  })

  test('路由切换后丢弃旧数据集迟到的版本目录响应', async () => {
    const user = userEvent.setup()
    const firstPage = deferred<Awaited<ReturnType<typeof datasetAPI.listVersions>>>()
    const oldVersion = publishedVersion({ id: 'version-old-route', datasetId: 'dataset-1', versionNo: 1 })
    const newVersion = publishedVersion({ id: 'version-new-route', datasetId: 'dataset-2', versionNo: 2 })
    const { getSpy, listVersionsSpy, getVersionSpy } = mockDesigner(datasetRecord())
    getSpy.mockImplementation(async id => datasetRecord({ id, currentPublishedVersionId: id === 'dataset-2' ? newVersion.id : oldVersion.id }))
    listVersionsSpy.mockImplementation(id => id === 'dataset-1'
      ? firstPage.promise
      : Promise.resolve({ items: [newVersion], total: 1, limit: 50, offset: 0 }))
    getVersionSpy.mockImplementation(async (_id, versionID) => versionID === newVersion.id ? newVersion : oldVersion)
    render(
      <MemoryRouter initialEntries={['/datasets/dataset-1/edit']}>
        <RouteSwitchHarness />
      </MemoryRouter>,
    )

    await waitFor(() => expect(listVersionsSpy).toHaveBeenCalledWith('dataset-1'))
    await user.click(screen.getByRole('button', { name: '切换数据集' }))
    expect(await screen.findByText(newVersion.id)).toBeInTheDocument()
    await act(async () => firstPage.resolve({ items: [oldVersion], total: 1, limit: 50, offset: 0 }))

    await waitFor(() => expect(screen.queryByText(oldVersion.id)).not.toBeInTheDocument())
    expect(screen.getByText(newVersion.id)).toBeInTheDocument()
  })
})

const table: AssetTable = {
  id: 'table-1', dataSourceId: 'source-1', dataSourceName: '订单库', dataSourceType: 'MYSQL',
  tableName: 'orders', schemaName: 'sales', businessName: '订单', columnCount: 1,
}
const column: AssetColumn = {
  id: 'column-1', tableId: table.id, columnName: 'order_date', businessName: '订单日期',
  canonicalType: 'DATE', nullable: false, semanticType: 'DATE',
}
const dsl: DatasetDSL = {
  dslVersion: '1.0',
  dataset: { code: 'monthly_orders', name: '月度订单数据集', description: '订单汇总', type: 'SINGLE_SOURCE' },
  nodes: [{ id: 'orders', type: 'TABLE', datasourceId: table.dataSourceId, tableId: table.id, alias: 'o', projection: ['order_date'], sourceFilters: [] }],
  joins: [],
  fields: [{ id: 'field_o_order_date', code: 'order_date', name: '订单日期', role: 'TIME', expression: { type: 'FIELD_REF', nodeId: 'orders', field: 'order_date' }, canonicalType: 'DATE', nullable: false, visible: true }],
  filters: [{ id: 'filter_start', stage: 'PRE_AGGREGATION', optional: true, expression: { type: 'GTE', left: { type: 'FIELD_REF', nodeId: 'orders', field: 'order_date' }, right: { type: 'PARAM_REF', code: 'start_date' } } }],
  groupBy: ['field_o_order_date'], having: [], sorts: [{ fieldId: 'field_o_order_date', direction: 'ASC' }],
  parameters: [{ code: 'start_date', name: '开始日期', dataType: 'DATE', required: true, multiValue: false }],
  outputGrain: { description: '每一行代表一个订单日期', keyFields: ['order_date'] },
  executionPolicy: { mode: 'REALTIME', timeoutMs: 5000, previewLimit: 200, resultLimit: 10000, cacheTtlSeconds: 300, materialization: { enabled: false } },
}

function datasetRecord(overrides: Partial<DatasetRecord> = {}): DatasetRecord {
  return {
    id: 'dataset-1', code: 'monthly_orders', name: '月度订单数据集', description: '订单汇总',
    type: 'SINGLE_SOURCE', status: 'DRAFT', version: 4, draftVersionId: 'draft-version-1', draftVersionNo: 1,
    draftRecordVersion: 3, dslHash: 'a'.repeat(64), planHash: 'd'.repeat(64), dsl,
    logicalPlan: {}, createdAt: '2026-07-16T00:00:00Z', updatedAt: '2026-07-16T01:00:00Z',
    ...overrides,
  }
}

function publishedVersion(overrides: Partial<PublishedVersionRecord> = {}): PublishedVersionRecord {
  return {
    id: 'published-version-1', datasetId: 'dataset-1', versionNo: 1, status: 'PUBLISHED',
    dslVersion: '1.0', dslHash: 'b'.repeat(64), planHash: 'd'.repeat(64), dsl, logicalPlan: {},
    publishedAt: '2026-07-16T10:00:00Z', publishedBy: 'user-1', datasetRecordVersion: 6,
    draftVersionId: 'draft-version-1', draftRecordVersion: 4,
    ...overrides,
  }
}

function mockDesigner(saved: DatasetRecord) {
  vi.spyOn(datasetAPI, 'tables').mockResolvedValue({ items: [table] })
  const getSpy = vi.spyOn(datasetAPI, 'get').mockResolvedValue(datasetRecord())
  const permissionSpy = vi.spyOn(datasetAPI, 'evaluatePermission').mockResolvedValue({ allowed: true })
  const listVersionsSpy = vi.spyOn(datasetAPI, 'listVersions').mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
  const getVersionSpy = vi.spyOn(datasetAPI, 'getVersion').mockResolvedValue(publishedVersion())
  const usageSpy = vi.spyOn(datasetAPI, 'getVersionUsage').mockResolvedValue({ reportDraftReferences: 0, downstreamDraftReferences: 0, downstreamPublishedReferences: 0, activeQueryRuns: 0 })
  const versionPreviewSpy = vi.spyOn(datasetAPI, 'previewVersion').mockResolvedValue({ queryId: 'version-query', columns: ['order_date'], rows: [['2026-01-01']], rowCount: 1, durationMs: 4 })
  const transitionSpy = vi.spyOn(datasetAPI, 'transitionVersion').mockResolvedValue(publishedVersion({ status: 'STALE' }))
  vi.spyOn(datasetAPI, 'columns').mockResolvedValue({ items: [column] })
  vi.spyOn(datasetAPI, 'validate').mockResolvedValue({ valid: true, dslHash: saved.dslHash, planHash: saved.planHash, logicalPlan: {} })
  const updateSpy = vi.spyOn(datasetAPI, 'update').mockResolvedValue(saved)
  const requestPublicationSpy = vi.spyOn(datasetAPI, 'requestPublication')
  return { getSpy, requestPublicationSpy, updateSpy, permissionSpy, listVersionsSpy, getVersionSpy, usageSpy, versionPreviewSpy, transitionSpy }
}

function renderDesigner() {
  render(
    <MemoryRouter initialEntries={['/datasets/dataset-1/edit']}>
      <Routes><Route path="/datasets/:datasetId/edit" element={<DatasetDesignerPage />} /></Routes>
    </MemoryRouter>,
  )
}

function RouteSwitchHarness() {
  const navigate = useNavigate()
  return <><button onClick={() => navigate('/datasets/dataset-2/edit')}>切换数据集</button><Routes><Route path="/datasets/:datasetId/edit" element={<DatasetDesignerPage />} /></Routes></>
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
