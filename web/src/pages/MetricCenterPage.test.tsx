import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { RequestError } from '../lib/api'
import {
  datasetAPI,
  type DatasetDSL,
  type DatasetRecord,
  type DatasetSummary,
  type PublishedVersionRecord,
} from '../lib/datasets'
import {
  metricAIAPI,
  type MetricAuthoringProposal,
  type MetricAuthoringProposalResult,
} from '../lib/metric-ai'
import {
  metricAPI,
  type MetricDefinition,
  type MetricRecord,
  type MetricVersionRecord,
} from '../lib/metrics'
import { MetricCenterPage } from './MetricCenterPage'

afterEach(() => vi.restoreAllMocks())

describe('指标配置中心原子指标编辑', () => {
  test('新建页提供可选 AI 参考条件，但不展示手工指标配置', async () => {
    const user = userEvent.setup()
    const mocks = mockMetricCenter()
    renderMetricCenter('/metrics/new')

    const requirement = await screen.findByLabelText('指标创建需求')
    const generate = screen.getByRole('button', { name: '生成指标配置' })
    expect(generate).toHaveClass('metric-ai-generate-button')
    expect(requirement.parentElement).toHaveClass('metric-ai-request-row')
    expect(generate.parentElement).toBe(requirement.parentElement)
    expect(generate).toBeDisabled()
    expect(screen.queryByLabelText('指标编码')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('指标数据集')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('原子指标字段')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '创建草稿' })).not.toBeInTheDocument()
    expect(document.querySelector('.metric-catalog')).toBeNull()
    expect(mocks.datasetListSpy).toHaveBeenCalledWith(200, 0)
    expect(screen.getByLabelText(`优先使用数据集 ${dataset.name}`)).not.toBeChecked()
    expect(screen.getByLabelText('统计日期字段')).toBeDisabled()
    expect(screen.getByRole('radio', { name: '由 AI 判断' })).toBeChecked()
    expect(screen.getByRole('button', { name: '清空条件' })).toBeDisabled()
    const hints = screen.getByRole('region', { name: '给 AI 更多参考' })
    expect(hints.querySelector('fieldset')).toBeNull()
    expect(hints.querySelectorAll('.metric-ai-hint-card')).toHaveLength(5)

    await user.type(requirement, '统计已支付订单销售额，按支付月份汇总。')
    expect(generate).toBeEnabled()
  })

  test('生成完成后自动弹窗展示方案，关闭后可再次打开', async () => {
    const user = userEvent.setup()
    mockMetricCenter()
    vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'DATA_GAP',
      summary: '当前授权数据缺少退款金额。',
    }))
    renderMetricCenter('/metrics/new')

    await user.type(await screen.findByLabelText('指标创建需求'), '创建净销售额指标。')
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))

    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(within(dialog).getByRole('article', { name: 'AI 指标审核提案' })).toBeInTheDocument()
    expect(document.body.style.overflow).toBe('hidden')

    await user.click(within(dialog).getByRole('button', { name: '关闭指标提案' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(document.body.style.overflow).toBe('')

    await user.click(screen.getByRole('button', { name: '查看生成结果' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  test('同名映射数据集展示数据源和源表，并把来源信息传给 AI', async () => {
    const user = userEvent.setup()
    const mocks = mockMetricCenter()
    const oracleDataset: DatasetSummary = {
      ...dataset,
      id: 'order-items-oracle',
      name: '销售订单明细表',
      type: 'MAPPED_TABLE',
      originTableId: 'oracle-order-items',
      originTableName: 'SALES_ORDER_ITEMS',
      originDataSourceName: '经营分析 Oracle 主题库',
      currentPublishedVersionId: 'oracle-order-items-version',
    }
    const workbookDataset: DatasetSummary = {
      ...dataset,
      id: 'order-items-workbook',
      name: '销售订单明细表',
      type: 'MAPPED_TABLE',
      originTableId: 'workbook-order-items',
      originTableName: '销售订单',
      originDataSourceName: '多工作表多级表头示例',
      currentPublishedVersionId: 'workbook-order-items-version',
    }
    mocks.datasetListSpy.mockResolvedValue({
      items: [oracleDataset, workbookDataset], total: 2, limit: 200, offset: 0,
    })
    vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'DATA_GAP', summary: '测试来源信息。',
    }))
    renderMetricCenter('/metrics/new')

    const oracleChoice = await screen.findByLabelText('优先使用数据集 销售订单明细表（数据源：经营分析 Oracle 主题库；源表：SALES_ORDER_ITEMS）')
    const workbookChoice = screen.getByLabelText('优先使用数据集 销售订单明细表（数据源：多工作表多级表头示例；源表：销售订单）')
    expect(screen.getByText('经营分析 Oracle 主题库 · SALES_ORDER_ITEMS')).toBeInTheDocument()
    expect(screen.getByText('多工作表多级表头示例 · 销售订单')).toBeInTheDocument()

    await user.click(oracleChoice)
    await user.click(workbookChoice)
    await user.type(screen.getByLabelText('指标创建需求'), '创建销售订单金额指标。')
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))

    expect(metricAIAPI.propose).toHaveBeenCalledWith({
      requirement: '创建销售订单金额指标。\n\n【AI 参考条件】\n以下条件由用户选择；未指定的内容请由 AI 基于授权资产补全：\n- 优先参考的已发布数据集：销售订单明细表（数据源：经营分析 Oracle 主题库；源表：SALES_ORDER_ITEMS）、销售订单明细表（数据源：多工作表多级表头示例；源表：销售订单）',
    })
  })

  test('选择已发布数据集后按其字段提供日期和维度，并稳定合并为自然语言需求', async () => {
    const user = userEvent.setup()
    const mocks = mockMetricCenter()
    const proposeSpy = vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'DATA_GAP', summary: '测试方案。',
    }))
    renderMetricCenter('/metrics/new')

    const datasetChoice = await screen.findByLabelText(`优先使用数据集 ${dataset.name}`)
    expect(screen.queryByLabelText(`分析维度 ${dataset.name} 地区`)).not.toBeInTheDocument()
    await user.click(datasetChoice)
    await waitFor(() => expect(mocks.datasetGetVersionSpy).toHaveBeenCalledWith(dataset.id, dataset.currentPublishedVersionId))

    const dateField = await screen.findByLabelText('统计日期字段')
    expect(within(dateField).getByRole('option', { name: `${dataset.name} · 统计月份（month）` })).toBeInTheDocument()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 营业收入`)).toBeInTheDocument()
    expect(screen.getByLabelText(`分析维度 ${dataset.name} 地区`)).toBeInTheDocument()
    await user.selectOptions(dateField, `${dataset.id}::field_month`)
    await user.click(screen.getByLabelText(`统计字段 ${dataset.name} 营业收入`))
    await user.click(screen.getByLabelText(`分析维度 ${dataset.name} 地区`))
    await user.click(screen.getByRole('radio', { name: '求和（SUM）' }))

    const requirement = '创建月度区域销售额指标。'
    await user.type(screen.getByLabelText('指标创建需求'), requirement)
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))

    const expectedRequirement = `${requirement}\n\n【AI 参考条件】\n以下条件由用户选择；未指定的内容请由 AI 基于授权资产补全：\n- 优先参考的已发布数据集：企业收入数据集\n- 统计字段（SUM 数值聚合对象）：企业收入数据集 / 营业收入（revenue）\n- 统计日期字段：企业收入数据集 / 统计月份（month）\n- 分析维度：企业收入数据集 / 地区（region）\n- 统计口径与聚合：求和（SUM）`
    expect(proposeSpy).toHaveBeenCalledWith({ requirement: expectedRequirement })
    expect(expectedRequirement).not.toContain(dataset.id)
    expect(screen.getByText(`合并后 ${expectedRequirement.length} / 4000 字`)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '清空条件' }))
    expect(datasetChoice).not.toBeChecked()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 营业收入`)).not.toBeInTheDocument()
    expect(screen.getByRole('radio', { name: '由 AI 判断' })).toBeChecked()
    expect(screen.queryByRole('article', { name: 'AI 指标审核提案' })).not.toBeInTheDocument()
  })

  test('统计字段按聚合语义展示候选，切换聚合时清除不再合法的字段', async () => {
    const user = userEvent.setup()
    const statisticalVersion = datasetVersion({
      dsl: {
        ...datasetDSL,
        fields: [
          ...datasetDSL.fields,
          { id: 'field_order_id', code: 'order_id', name: '订单编号', role: 'IDENTIFIER', canonicalType: 'STRING', visible: true },
          { id: 'field_channel', code: 'channel', name: '销售渠道', role: 'ATTRIBUTE', canonicalType: 'STRING', visible: true },
          { id: 'field_sequence', code: 'sequence', name: '业务序号', role: 'DIMENSION', canonicalType: 'INTEGER', visible: true },
          { id: 'field_paid', code: 'paid', name: '是否支付', role: 'MEASURE', canonicalType: 'BOOLEAN', visible: true },
          { id: 'field_business_date', code: 'business_date', name: '业务日期', role: 'ATTRIBUTE', canonicalType: 'DATE', visible: true },
          { id: 'field_hidden', code: 'hidden_code', name: '隐藏字段', role: 'IDENTIFIER', canonicalType: 'STRING', visible: false },
        ],
      },
    })
    mockMetricCenter({ dataVersion: statisticalVersion })
    const proposeSpy = vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'DATA_GAP', summary: '测试计数方案。',
    }))
    renderMetricCenter('/metrics/new')

    await user.click(await screen.findByLabelText(`优先使用数据集 ${dataset.name}`))
    await screen.findByRole('radiogroup', { name: '统计口径与聚合' })
    expect(await screen.findByText('AI 判断时可参考标识符、维度、属性及可聚合数值字段；日期时间不作为统计对象。')).toBeInTheDocument()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 订单编号`)).toBeInTheDocument()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 销售渠道`)).toBeInTheDocument()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 业务序号`)).toBeInTheDocument()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 营业收入`)).toBeInTheDocument()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 是否支付`)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 统计月份`)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 业务日期`)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 隐藏字段`)).not.toBeInTheDocument()

    const orderID = screen.getByLabelText(`统计字段 ${dataset.name} 订单编号`)
    await user.click(orderID)
    for (const [numericAggregation, optionName] of [['SUM', '求和（SUM）'], ['AVG', '平均值（AVG）'], ['MIN', '最小值（MIN）'], ['MAX', '最大值（MAX）']] as const) {
      await user.click(screen.getByRole('radio', { name: optionName }))
      expect(screen.getByText(`${numericAggregation} 仅可选择数值型度量或数值属性字段。`)).toBeInTheDocument()
      expect(screen.getByLabelText(`统计字段 ${dataset.name} 营业收入`)).toBeInTheDocument()
      expect(screen.queryByLabelText(`统计字段 ${dataset.name} 订单编号`)).not.toBeInTheDocument()
      expect(screen.queryByLabelText(`统计字段 ${dataset.name} 业务序号`)).not.toBeInTheDocument()
    }

    await user.click(screen.getByRole('radio', { name: '去重计数（COUNT DISTINCT）' }))
    expect(screen.getByText('COUNT DISTINCT 优先选择标识符、维度或属性字段，也支持数值业务字段。')).toBeInTheDocument()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 订单编号`)).not.toBeChecked()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 业务序号`)).toBeInTheDocument()
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 营业收入`)).toBeInTheDocument()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 是否支付`)).not.toBeInTheDocument()

    await user.click(screen.getByRole('radio', { name: '计数（COUNT）' }))
    const countedOrderID = screen.getByLabelText(`统计字段 ${dataset.name} 订单编号`)
    const paid = screen.getByLabelText(`统计字段 ${dataset.name} 是否支付`)
    expect(screen.getByText('COUNT 可选择任意非日期的可见输出字段并按非空值计数；留空时可由 AI 判断是否统计记录数。')).toBeInTheDocument()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 统计月份`)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 业务日期`)).not.toBeInTheDocument()
    await user.click(countedOrderID)
    await user.click(paid)

    const requirement = '统计已支付订单数。'
    await user.type(screen.getByLabelText('指标创建需求'), requirement)
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))
    const expectedRequirement = `${requirement}\n\n【AI 参考条件】\n以下条件由用户选择；未指定的内容请由 AI 基于授权资产补全：\n- 优先参考的已发布数据集：企业收入数据集\n- 统计字段（COUNT 非空计数对象）：企业收入数据集 / 订单编号（order_id）、企业收入数据集 / 是否支付（paid）\n- 统计口径与聚合：计数（COUNT）`
    expect(proposeSpy).toHaveBeenCalledWith({ requirement: expectedRequirement })

    await user.click(screen.getByRole('radio', { name: '由 AI 判断' }))
    expect(screen.getByLabelText(`统计字段 ${dataset.name} 订单编号`)).toBeChecked()
    expect(screen.queryByLabelText(`统计字段 ${dataset.name} 是否支付`)).not.toBeInTheDocument()
  })

  test('数据集审批返回后恢复原始指标需求但不自动调用 AI', async () => {
    mockMetricCenter()
    const proposeSpy = vi.spyOn(metricAIAPI, 'propose')
    const requirement = '基于订单表和客户表，创建一个月度各区域销售额的指标'
    render(<MemoryRouter initialEntries={[{ pathname: '/metrics/new', state: { metricAIRequirement: requirement } }]}><Routes>
      <Route path="/metrics/new" element={<MetricCenterPage />} />
    </Routes></MemoryRouter>)

    expect(await screen.findByLabelText('指标创建需求')).toHaveValue(requirement)
    expect(screen.getByRole('button', { name: '生成指标配置' })).toBeEnabled()
    expect(proposeSpy).not.toHaveBeenCalled()
  })

  test('从数据集展示区进入时预选拓展基线，并把追加式 DAG 约束传给后续改造流程', async () => {
    const user = userEvent.setup()
    mockMetricCenter()
    vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'MODIFY_DATASET',
      summary: '需要在当前数据集追加月度聚合输出',
      targetDatasetId: dataset.id,
      targetDatasetVersionId: dataVersion.id,
      datasetInstruction: '追加月度销售额输出。',
    }))
    render(<MemoryRouter initialEntries={[{
      pathname: '/metrics/new',
      state: { preferredDatasetId: dataset.id, safeDatasetExtension: true },
    }]}><Routes>
      <Route path="/metrics/new" element={<MetricCenterPage />} />
      <Route path="/datasets/:datasetId/edit" element={<LocationProbe />} />
    </Routes></MemoryRouter>)

    expect(await screen.findByText(`正在基于“${dataset.name}”拓展指标`)).toBeInTheDocument()
    expect(screen.getByLabelText(`优先使用数据集 ${dataset.name}`)).toBeChecked()
    const requirement = '创建月度销售额指标。'
    await user.type(screen.getByLabelText('指标创建需求'), requirement)
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))

    expect(metricAIAPI.propose).toHaveBeenCalledWith({
      requirement: expect.stringContaining('【数据集 DAG 安全拓展约束】'),
    })
    await user.click(await screen.findByRole('button', { name: '确认方案并继续改造' }))
    const route = JSON.parse((await screen.findByLabelText('当前路由')).textContent || '{}')
    expect(route.pathname).toBe(`/datasets/${dataset.id}/edit`)
    expect(route.state).toMatchObject({
      metricAIRequirement: requirement,
      preferredDatasetId: dataset.id,
      safeDatasetExtension: true,
      returnTo: '/metrics/new',
    })
    expect(route.state.metricAIInstruction).toContain('追加月度销售额输出。')
    expect(route.state.metricAIInstruction).toContain('不得删除、改名或改变现有输出、过滤、关联、分组与聚合逻辑')
  })

  test('原子指标编辑器允许对字符串标识符计数，并在切回数值聚合时清除非法字段', async () => {
    const user = userEvent.setup()
    const countVersion = datasetVersion({
      dsl: {
        ...datasetDSL,
        fields: [
          ...datasetDSL.fields,
          { id: 'field_order_id', code: 'order_id', name: '订单编号', role: 'IDENTIFIER', canonicalType: 'STRING', visible: true },
        ],
      },
    })
    mockMetricCenter({ dataVersion: countVersion })
    renderMetricCenter('/metrics/metric-1/edit')

    const aggregation = await screen.findByLabelText('指标聚合')
    await user.selectOptions(aggregation, 'COUNT')
    const atomicField = screen.getByLabelText('原子指标字段')
    expect(within(atomicField).getByRole('option', { name: '订单编号 · IDENTIFIER · STRING' })).toBeInTheDocument()
    await user.selectOptions(atomicField, 'field_order_id')
    expect(atomicField).toHaveValue('field_order_id')
    expect(screen.getByLabelText('单位')).toHaveValue('')
    expect(screen.getByLabelText('数字格式')).toHaveValue('#,##0')
    expect(screen.getByLabelText('小数位数')).toHaveValue(0)
    expect(screen.getByLabelText('可加性')).toHaveValue('ADDITIVE')

    await user.selectOptions(aggregation, 'SUM')
    expect(screen.getByLabelText('原子指标字段')).toHaveValue('')
    expect(within(screen.getByLabelText('原子指标字段')).queryByRole('option', { name: '订单编号 · IDENTIFIER · STRING' })).not.toBeInTheDocument()
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

describe('指标配置中心 AI 审核提案', () => {
  test('CREATE_ON_DATASET 展示完整只读配置，确认后直接创建草稿', async () => {
    const user = userEvent.setup()
    const mocks = mockMetricCenter()
    const candidate = metricDefinition({
      metric: { code: 'paid_revenue', name: '已支付销售额', description: '已支付订单金额总额', type: 'ATOMIC' },
    })
    const created = metricRecord({ id: 'metric-created', code: candidate.metric.code, name: candidate.metric.name, description: candidate.metric.description, definition: candidate })
    mocks.createSpy.mockResolvedValue(created)
    mocks.getSpy.mockResolvedValue(created)
    const proposeSpy = vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'CREATE_ON_DATASET',
      summary: '可复用企业收入数据集的已发布版本创建指标',
      targetDatasetId: dataset.id,
      targetDatasetVersionId: dataVersion.id,
      candidateMetricDefinition: candidate,
      clarificationQuestions: ['请确认退款在次月发生时仍按支付月统计。'],
    }))
    renderMetricCenter('/metrics/new')

    const requirement = '创建已支付销售额，汇总已支付且未退款订单金额，按统计月份汇总。'
    await user.type(await screen.findByLabelText('指标创建需求'), requirement)
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))

    const proposal = await screen.findByRole('article', { name: 'AI 指标审核提案' })
    expect(proposeSpy).toHaveBeenCalledWith({ requirement })
    const preview = within(proposal).getByRole('region', { name: 'AI 生成配置预览' })
    expect(within(preview).getByText('已支付销售额')).toBeInTheDocument()
    expect(within(preview).getByText(/指标编码 · paid_revenue/)).toBeInTheDocument()
    expect(within(preview).getByText('已支付订单金额总额')).toBeInTheDocument()
    expect(within(preview).getByText('已匹配并锁定精确发布版本')).toBeInTheDocument()
    expect(within(preview).getByText('已匹配原子字段')).toBeInTheDocument()
    expect(within(preview).getByText('元')).toBeInTheDocument()
    expect(within(preview).getByText('#,##0.00')).toBeInTheDocument()
    expect(within(preview).getByText('地区')).toBeInTheDocument()
    expect(within(preview).getByText('HALF_UP')).toBeInTheDocument()
    expect(within(preview).getByText('查看高级计算设置与完整配置').closest('details')).not.toHaveAttribute('open')
    expect(within(preview).queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('指标编码')).not.toBeInTheDocument()
    const confirm = within(proposal).getByRole('button', { name: '确认方案并创建草稿' })
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(mocks.createSpy).toHaveBeenCalledWith(candidate))
    expect(mocks.datasetGetVersionSpy).toHaveBeenCalledWith(dataset.id, dataVersion.id)
    expect(await screen.findByText('AI 生成的指标草稿已创建。')).toBeInTheDocument()
    expect(mocks.updateSpy).not.toHaveBeenCalled()
  })

  test('MODIFY_DATASET 跳转到目标数据集并携带改造指令', async () => {
    const user = userEvent.setup()
    mockMetricCenter()
    vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'MODIFY_DATASET',
      summary: '需要先补充退款状态字段',
      targetDatasetId: dataset.id,
      targetDatasetVersionId: dataVersion.id,
      datasetInstruction: '在订单数据集中增加退款状态，并保留支付时间与金额字段。',
    }))
    render(<MemoryRouter initialEntries={['/metrics/new']}><Routes>
      <Route path="/metrics/new" element={<MetricCenterPage />} />
      <Route path="/datasets/:datasetId/edit" element={<LocationProbe />} />
    </Routes></MemoryRouter>)

    const requirement = '创建净销售额，按支付月份统计已支付金额扣除退款金额。'
    await user.type(await screen.findByLabelText('指标创建需求'), requirement)
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))
    await user.click(await screen.findByRole('button', { name: '确认方案并继续改造' }))

    const route = JSON.parse((await screen.findByLabelText('当前路由')).textContent || '{}')
    expect(route).toEqual({
      pathname: `/datasets/${dataset.id}/edit`,
      state: {
        metricAIInstruction: '在订单数据集中增加退款状态，并保留支付时间与金额字段。',
        metricAIRequirement: requirement,
        metricAIHints: {
          preferredTableIds: [], aggregation: 'SUM', measureFields: [], dimensionFields: [], timeGrain: 'MONTH',
        },
        returnTo: '/metrics/new',
      },
    })
  })

  test('CREATE_DATASET 确认后进入新建流程，不打开映射表数据集', async () => {
    const user = userEvent.setup()
    mockMetricCenter()
    const instruction = '以订单映射表和客户映射表为来源，新建普通数据集；关联客户后输出销售额、区域和月份。'
    vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'CREATE_DATASET',
      summary: '指标 AI 不会原地改造映射表数据集，需要新建普通数据集承载关联结果。',
      targetDatasetId: '',
      targetDatasetVersionId: '',
      datasetInstruction: instruction,
    }))
    render(<MemoryRouter initialEntries={['/metrics/new']}><Routes>
      <Route path="/metrics/new" element={<MetricCenterPage />} />
      <Route path="/datasets/new/edit" element={<LocationProbe />} />
    </Routes></MemoryRouter>)

    const requirement = '基于订单表和客户表创建月度各区域销售额指标。'
    await user.type(await screen.findByLabelText('指标创建需求'), requirement)
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))

    const proposal = await screen.findByRole('article', { name: 'AI 指标审核提案' })
    expect(within(proposal).getByText('不会修改作为来源的映射表数据集')).toBeInTheDocument()
    await user.click(within(proposal).getByRole('button', { name: '确认方案并新建数据集' }))

    expect(JSON.parse((await screen.findByLabelText('当前路由')).textContent || '{}')).toEqual({
      pathname: '/datasets/new/edit',
      state: {
        metricAIInstruction: instruction,
        metricAIRequirement: requirement,
        metricAIHints: {
          preferredTableIds: [], aggregation: 'SUM', measureFields: [], dimensionFields: [], timeGrain: 'MONTH',
        },
        returnTo: '/metrics/new',
      },
    })
  })

  test('CREATE_DATASET 将 COUNT、月份、维度和证据中的物理字段传给数据集 AI', async () => {
    const user = userEvent.setup()
    const mocks = mockMetricCenter()
    const orderDataset: DatasetSummary = {
      ...dataset, id: 'orders-mapped', name: '订单映射表', type: 'MAPPED_TABLE',
      originTableId: 'orders-table', currentPublishedVersionId: 'orders-version',
    }
    const customerDataset: DatasetSummary = {
      ...dataset, id: 'customers-mapped', name: '客户映射表', type: 'MAPPED_TABLE',
      originTableId: 'customers-table', currentPublishedVersionId: 'customers-version',
    }
    const orderVersion = datasetVersion({
      id: 'orders-version', datasetId: orderDataset.id,
      dsl: {
        ...datasetDSL,
        nodes: [{ id: 'orders', tableId: 'orders-table' }],
        fields: [
          { id: 'order-id', code: 'order_id', name: '订单编号', role: 'IDENTIFIER', canonicalType: 'INTEGER', visible: true, expression: { type: 'FIELD_REF', nodeId: 'orders', field: 'ORDER_ID' } },
          { id: 'created-at', code: 'created_at', name: '下单时间', role: 'TIME', canonicalType: 'DATETIME', visible: true, expression: { type: 'FIELD_REF', nodeId: 'orders', field: 'CREATED_AT' } },
        ],
      },
    })
    const customerVersion = datasetVersion({
      id: 'customers-version', datasetId: customerDataset.id,
      dsl: {
        ...datasetDSL,
        nodes: [{ id: 'customers', tableId: 'customers-table' }],
        fields: [
          { id: 'region-code', code: 'region_code', name: '地区代码', role: 'DIMENSION', canonicalType: 'STRING', visible: true, expression: { type: 'FIELD_REF', nodeId: 'customers', field: 'region_code' } },
        ],
      },
    })
    mocks.datasetListSpy.mockResolvedValue({ items: [orderDataset, customerDataset], total: 2, limit: 200, offset: 0 })
    mocks.datasetGetVersionSpy.mockImplementation(async (datasetId, versionId) => {
      if (datasetId === orderDataset.id && versionId === orderVersion.id) return orderVersion
      if (datasetId === customerDataset.id && versionId === customerVersion.id) return customerVersion
      throw new Error('unexpected dataset version')
    })
    const instruction = '关联订单和客户，按地区及下单月份分组，使用 COUNT(ORDER_ID) 统计交易量。'
    vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'CREATE_DATASET',
      summary: '新建普通数据集承载关联与聚合结果。',
      datasetInstruction: instruction,
      retrievalEvidence: [
        { sourceType: 'FIELD', sourceId: 'order-id', datasetId: orderDataset.id, datasetVersionId: orderVersion.id, reason: '订单计数对象' },
        { sourceType: 'FIELD', sourceId: 'created-at', datasetId: orderDataset.id, datasetVersionId: orderVersion.id, reason: '统计日期' },
        { sourceType: 'FIELD', sourceId: 'region-code', datasetId: customerDataset.id, datasetVersionId: customerVersion.id, reason: '地区维度' },
      ],
    }))
    render(<MemoryRouter initialEntries={['/metrics/new']}><Routes>
      <Route path="/metrics/new" element={<MetricCenterPage />} />
      <Route path="/datasets/new/edit" element={<LocationProbe />} />
    </Routes></MemoryRouter>)

    await user.type(await screen.findByLabelText('指标创建需求'), '生成月度区域交易量。')
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))
    await user.click(await screen.findByRole('button', { name: '确认方案并新建数据集' }))

    await waitFor(() => expect(mocks.datasetGetVersionSpy).toHaveBeenCalledTimes(2))
    expect(JSON.parse((await screen.findByLabelText('当前路由')).textContent || '{}')).toMatchObject({
      pathname: '/datasets/new/edit',
      state: {
        metricAIHints: {
          preferredTableIds: ['orders-table', 'customers-table'],
          aggregation: 'COUNT',
          measureFields: [{ tableId: 'orders-table', column: 'ORDER_ID' }],
          timeField: { tableId: 'orders-table', column: 'CREATED_AT' },
          dimensionFields: [{ tableId: 'customers-table', column: 'region_code' }],
          timeGrain: 'MONTH',
        },
      },
    })
  })

  test('可重新生成同一方案，也可追加意见生成修改方案', async () => {
    const user = userEvent.setup()
    mockMetricCenter()
    const requirement = '创建月度区域销售额指标。'
    const proposalBase: Partial<MetricAuthoringProposal> = {
      strategy: 'MODIFY_DATASET', targetDatasetId: dataset.id, targetDatasetVersionId: dataVersion.id,
      datasetInstruction: '补充区域字段。',
    }
    const proposeSpy = vi.spyOn(metricAIAPI, 'propose')
      .mockResolvedValueOnce(metricAIResult({ ...proposalBase, summary: '初始方案。' }))
      .mockResolvedValueOnce(metricAIResult({ ...proposalBase, summary: '重新生成的方案。' }))
      .mockResolvedValueOnce(metricAIResult({ ...proposalBase, summary: '已根据新意见调整方案。' }))
    renderMetricCenter('/metrics/new')

    await user.click(await screen.findByRole('radio', { name: '求和（SUM）' }))
    await user.type(await screen.findByLabelText('指标创建需求'), requirement)
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))
    const requirementWithHints = `${requirement}\n\n【AI 参考条件】\n以下条件由用户选择；未指定的内容请由 AI 基于授权资产补全：\n- 统计口径与聚合：求和（SUM）`
    let proposal = await screen.findByRole('article', { name: 'AI 指标审核提案' })
    await user.click(within(proposal).getByRole('button', { name: '重新生成方案' }))
    expect(await within(proposal).findByText('重新生成的方案。')).toBeInTheDocument()
    expect(proposeSpy).toHaveBeenNthCalledWith(1, { requirement: requirementWithHints })
    expect(proposeSpy).toHaveBeenNthCalledWith(2, { requirement: requirementWithHints })

    await user.click(within(proposal).getByRole('button', { name: '根据意见修改方案' }))
    const opinion = '销售额使用含税金额，区域以客户当前所属区域为准。'
    const revision = within(proposal).getByLabelText('方案修改意见')
    expect(Number(revision.getAttribute('maxlength'))).toBeLessThan(4000)
    await user.type(revision, opinion)
    await user.click(within(proposal).getByRole('button', { name: '生成修改方案' }))

    proposal = await screen.findByRole('article', { name: 'AI 指标审核提案' })
    expect(await within(proposal).findByText('已根据新意见调整方案。')).toBeInTheDocument()
    const revisedRequirement = `${requirementWithHints}\n\n用户补充意见：${opinion}`
    expect(proposeSpy).toHaveBeenNthCalledWith(3, { requirement: revisedRequirement })
    expect(revisedRequirement.match(/【AI 参考条件】/g)).toHaveLength(1)
    expect(within(proposal).queryByLabelText('方案修改意见')).not.toBeInTheDocument()
  })

  test('业务主视图按步骤展示方案并默认收起 UUID 与检索技术信息', async () => {
    const user = userEvent.setup()
    mockMetricCenter()
    const targetID = '412b3965-6ede-4220-bf60-ed59c6d3b69b'
    vi.spyOn(metricAIAPI, 'propose').mockResolvedValue(metricAIResult({
      strategy: 'MODIFY_DATASET', targetDatasetId: targetID, targetDatasetVersionId: '29edfdda-ba53-4720-84cf-f0dc2b239302',
      summary: `需要改造订单事实表（ID: ${targetID}）。完成后创建区域销售额指标。`,
      datasetInstruction: `从客户信息表引入地区代码。通过客户 ID 建立关联。发布新版本。`,
      retrievalEvidence: [{ sourceType: 'DATASET', sourceId: targetID, datasetId: targetID, datasetVersionId: '29edfdda-ba53-4720-84cf-f0dc2b239302', reason: '目标数据集可管理。' }],
      assumptions: ['销售额使用订单金额。'], warnings: ['需要确认退款口径。'],
    }))
    renderMetricCenter('/metrics/new')

    await user.type(await screen.findByLabelText('指标创建需求'), '创建区域销售额指标。')
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))
    const proposal = await screen.findByRole('article', { name: 'AI 指标审核提案' })
    const overview = within(proposal).getByRole('region', { name: '方案概览' })
    expect(overview).not.toHaveTextContent(targetID)
    const plan = within(proposal).getByRole('region', { name: '数据集执行方案' })
    expect(within(plan).getAllByRole('listitem')).toHaveLength(3)
    expect(within(proposal).getByRole('region', { name: '审核要点' })).toHaveTextContent('AI 当前假设')
    expect(within(proposal).getByText('查看 AI 采用的业务依据（1 项）').closest('details')).not.toHaveAttribute('open')
    expect(within(proposal).getByText('技术信息').closest('details')).not.toHaveAttribute('open')
  })

  test('待澄清和数据缺口提案都不提供应用动作', async () => {
    const user = userEvent.setup()
    mockMetricCenter()
    const proposeSpy = vi.spyOn(metricAIAPI, 'propose')
      .mockResolvedValueOnce(metricAIResult({
        strategy: 'NEEDS_CLARIFICATION',
        summary: '需要确认退款订单是否计入',
        clarificationQuestions: ['退款发生在次月时应冲减哪个月份？'],
      }))
      .mockResolvedValueOnce(metricAIResult({
        strategy: 'DATA_GAP',
        summary: '授权目录中没有退款金额字段',
      }))
    renderMetricCenter('/metrics/new')

    await user.type(await screen.findByLabelText('指标创建需求'), '创建净销售额，按月统计销售金额扣除退款金额。')
    await user.click(screen.getByRole('button', { name: '生成指标配置' }))

    let proposal = await screen.findByRole('article', { name: 'AI 指标审核提案' })
    expect(within(proposal).getByText('还需要一点业务信息')).toBeInTheDocument()
    expect(within(proposal).queryByRole('button', { name: /确认方案并创建草稿|确认方案并继续改造|确认方案并新建数据集/ })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '生成指标配置' }))
    proposal = await screen.findByRole('article', { name: 'AI 指标审核提案' })
    expect(within(proposal).getByText('当前不能安全创建')).toBeInTheDocument()
    expect(within(proposal).queryByRole('button', { name: /确认方案并创建草稿|确认方案并继续改造|确认方案并新建数据集/ })).not.toBeInTheDocument()
    expect(proposeSpy).toHaveBeenCalledTimes(2)
  })
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

function datasetRecord(overrides: Partial<DatasetRecord> = {}): DatasetRecord {
  return {
    ...dataset,
    draftVersionId: 'dataset-draft-1', draftVersionNo: 1, draftRecordVersion: 3,
    planHash: 'c'.repeat(64), dsl: datasetDSL, logicalPlan: {}, createdAt: '2026-07-16T00:00:00Z',
    ...overrides,
  }
}

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

function metricAIResult(overrides: Partial<MetricAuthoringProposal> = {}): MetricAuthoringProposalResult {
  return {
    requestId: 'metric-ai-request-1',
    retrievalContextHash: 'f'.repeat(64),
    proposal: {
      schemaVersion: '1.0', strategy: 'DATA_GAP', summary: '', targetDatasetId: '', targetDatasetVersionId: '',
      reuseMetricVersionId: '', retrievalEvidence: [], candidateMetricDefinition: null, datasetInstruction: '',
      clarificationQuestions: [], assumptions: [], warnings: [], ...overrides,
    },
  }
}

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
  const datasetGetSpy = vi.spyOn(datasetAPI, 'get').mockResolvedValue(datasetRecord())
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
    datasetListSpy, datasetGetSpy, datasetVersionListSpy, datasetGetVersionSpy, permissionSpy, getSpy, createSpy, updateSpy, publishSpy,
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
    <Route path="/metrics/new" element={<MetricCenterPage />} />
    <Route path="/metrics/:metricId/edit" element={<MetricCenterPage />} />
  </Routes></MemoryRouter>)
}

function LocationProbe() {
  const location = useLocation()
  return <output aria-label="当前路由">{JSON.stringify({ pathname: location.pathname, state: location.state })}</output>
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
