import reportExample from '../../../../api/examples/report-json-v1.json'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { describe, expect, test, vi } from 'vitest'
import { demoReportInteractionExecutor, demoReportRuntime } from '../../lib/demo-report-runtime'
import type { ReportInteractionExecutionResult } from '../../lib/report-interactions'
import type { ReportDocument, ReportRuntimeContext } from '../../lib/report-contract'
import type { ReportComponentRegistry } from './componentRegistry'
import { ReportRenderer, ValidatedReportRenderer } from './ReportRenderer'

describe('统一报告渲染器', () => {
  test('设计器与查看器从同一 JSON 还原相同组件树', () => {
    const designer = render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="designer" />)
    const designerIDs = componentIDs(designer.container)
    designer.unmount()
    const viewer = render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" />)
    expect(componentIDs(viewer.container)).toEqual(designerIDs)
    expect(designerIDs).toEqual(['title_main', 'filter_stat_month', 'chart_revenue_trend', 'conclusion_overview', 'source_note'])
    expect(screen.getByText('营业收入连续三个月增长，二季度增长动能较一季度明显增强。')).toBeInTheDocument()
  })

  test('同一次指针拖拽只在释放时提交最终分块坐标', () => {
    const onBlockGridChange = vi.fn()
    const view = render(<ReportRenderer document={reportExample as ReportDocument} runtime={demoReportRuntime} mode="designer" onBlockGridChange={onBlockGridChange} />)
    const block = view.container.querySelector<HTMLElement>('[data-block-id="block_source_note"]')!

    fireEvent.pointerDown(block, { pointerId: 1, clientX: 0, clientY: 0 })
    fireEvent.pointerMove(block, { pointerId: 1, clientX: 0, clientY: 60 })
    fireEvent.pointerMove(block, { pointerId: 1, clientX: 0, clientY: 110 })
    expect(onBlockGridChange).not.toHaveBeenCalled()
    expect(block).toHaveAttribute('aria-label', '分块 block_source_note，位置 1 列 14 行，尺寸 12 × 3')

    fireEvent.pointerUp(block, { pointerId: 1, clientX: 0, clientY: 110 })
    expect(onBlockGridChange).toHaveBeenCalledTimes(1)
    expect(onBlockGridChange).toHaveBeenCalledWith('page_overview', 'block_source_note', { x: 0, y: 13, w: 12, h: 3 })
  })

  test('组件缩放手势在取消时不提交，完成时只提交一次', () => {
    const onComponentGridChange = vi.fn()
    const view = render(<ReportRenderer document={reportExample as ReportDocument} runtime={demoReportRuntime} mode="designer" onComponentGridChange={onComponentGridChange} />)
    const component = view.container.querySelector<HTMLElement>('[data-component-id="chart_revenue_trend"]')!
    const handle = screen.getByRole('button', { name: '调整组件 营业收入趋势 尺寸' })

    fireEvent.pointerDown(handle, { pointerId: 2, clientX: 0, clientY: 0 })
    fireEvent.pointerMove(component, { pointerId: 2, clientX: 45, clientY: 28 })
    fireEvent.pointerCancel(component, { pointerId: 2 })
    expect(onComponentGridChange).not.toHaveBeenCalled()

    fireEvent.pointerDown(handle, { pointerId: 3, clientX: 0, clientY: 0 })
    fireEvent.pointerMove(component, { pointerId: 3, clientX: 45, clientY: 28 })
    fireEvent.pointerMove(component, { pointerId: 3, clientX: 85, clientY: 55 })
    expect(onComponentGridChange).not.toHaveBeenCalled()
    fireEvent.pointerUp(component, { pointerId: 3 })

    expect(onComponentGridChange).toHaveBeenCalledTimes(1)
    expect(onComponentGridChange).toHaveBeenCalledWith('page_overview', 'block_overview', 'chart_revenue_trend', { x: 0, y: 4, w: 36, h: 40 })
  })

  test('冻结配置只在查看态生成冻结节点和页面级宿主', () => {
    const designer = render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="designer" />)
    expect(designer.container.querySelector('[data-component-id="title_main"]')).not.toHaveClass('report-component--sticky')
    designer.unmount()

    const viewer = render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" />)
    expect(viewer.container.querySelector('[data-block-id="block_overview"]')).toHaveClass('report-block--sticky-host')
    expect(viewer.container.querySelector('[data-component-id="title_main"]')).toHaveClass('report-component--sticky')
    expect(viewer.container.querySelector('[data-component-id="filter_stat_month"]')).toHaveAttribute('data-sticky-state', 'armed')
  })

  test('待命冻结项不提前写入位移或冻结层级', () => {
    const view = render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" />)
    const block = view.container.querySelector<HTMLElement>('[data-block-id="block_overview"]')!
    const title = view.container.querySelector<HTMLElement>('[data-component-id="title_main"]')!

    expect(title).toHaveAttribute('data-sticky-state', 'armed')
    expect(title.style.transform).toBe('')
    expect(title.style.zIndex).toBe('')
    expect(block.style.transform).toBe('')
    expect(block.style.zIndex).toBe('')
  })

  test('查看器滚动按缩放后的逻辑坐标更新冻结位移', async () => {
    const view = render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" />)
    const canvas = view.container.querySelector<HTMLElement>('.report-page-canvas')!
    const canvasRect = vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({ left: 0, top: -100, right: 960, bottom: 656, width: 960, height: 756, x: 0, y: -100, toJSON: () => ({}) })

    fireEvent.scroll(window)

    await waitFor(() => expect(view.container.querySelector('[data-component-id="title_main"]')).toHaveAttribute('data-sticky-translate', '200.000'))

    // 联动提示插入画布上方后，即使没有新滚动事件也必须重测画布位置。
    canvasRect.mockReturnValue({ left: 0, top: -53.5, right: 960, bottom: 702.5, width: 960, height: 756, x: 0, y: -53.5, toJSON: () => ({}) })
    fireEvent.change(screen.getByRole('combobox', { name: '统计月份' }), { target: { value: JSON.stringify({ value: '2026-04' }) } })
    await waitFor(() => expect(view.container.querySelector('[data-component-id="title_main"]')).toHaveAttribute('data-sticky-translate', '107.000'))
  })

  test('子组件冻结层级不会提升父分块或污染其他作用域', async () => {
    const document = structuredClone(reportExample) as ReportDocument
    const overview = document.pages[0].blocks[0]
    const sourceBlock = document.pages[0].blocks[1]
    overview.components.find(component => component.id === 'filter_stat_month')!.sticky = { enabled: false }
    const source = sourceBlock.components.find(component => component.id === 'source_note')!
    source.grid.h = 4
    source.sticky = { enabled: true, top: 0, scope: 'BLOCK', zIndex: 100000 }

    const view = render(<ReportRenderer document={document} runtime={demoReportRuntime} mode="viewer" />)
    const canvas = view.container.querySelector<HTMLElement>('.report-page-canvas')!
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({ left: 0, top: -600, right: 960, bottom: 1560, width: 960, height: 2160, x: 0, y: -600, toJSON: () => ({}) })
    fireEvent.scroll(window)

    await waitFor(() => expect(view.container.querySelector('[data-component-id="source_note"]')).toHaveAttribute('data-sticky-state', 'stuck'))
    const overviewBlock = view.container.querySelector<HTMLElement>('[data-block-id="block_overview"]')!
    const sourceHost = view.container.querySelector<HTMLElement>('[data-block-id="block_source_note"]')!
    const title = view.container.querySelector<HTMLElement>('[data-component-id="title_main"]')!
    const sourceNote = view.container.querySelector<HTMLElement>('[data-component-id="source_note"]')!
    expect(overviewBlock.style.zIndex).toBe('')
    expect(sourceHost.style.zIndex).toBe('')
    expect(sourceHost).not.toHaveClass('report-block--sticky-host')
    expect(title.style.zIndex).toBe('100')
    expect(sourceNote.style.zIndex).toBe('100000')
  })

  test('冻结降级告警不改变画布测量并在重复重算后保持稳定', async () => {
    const document = structuredClone(reportExample) as ReportDocument
    const filter = document.pages[0].blocks[0].components.find(component => component.id === 'filter_stat_month')!
    filter.grid = { ...filter.grid, x: 0, y: 4 }
    const view = render(<ReportRenderer document={document} runtime={demoReportRuntime} mode="viewer" />)
    const canvas = view.container.querySelector<HTMLElement>('.report-page-canvas')!
    const stableStatusSlot = view.container.querySelector<HTMLElement>('.report-sticky-status-slot')!
    expect(stableStatusSlot).toHaveStyle({ height: '54px' })
    expect(stableStatusSlot).toBeEmptyDOMElement()
    vi.spyOn(canvas, 'getBoundingClientRect').mockImplementation(() => {
      // 旧实现只在告警出现时插入节点；这里模拟其 46px 高度，验证等高状态槽不会触发该反馈。
      const warning = view.container.querySelector('.report-sticky-warning')
      const stableSlot = warning?.parentElement?.classList.contains('report-sticky-status-slot') === true
      const top = warning && !stableSlot ? -604 : -650
      return { left: 0, top, right: 960, bottom: top + 2160, width: 960, height: 2160, x: 0, y: top, toJSON: () => ({}) }
    })

    fireEvent.scroll(window)
    await waitFor(() => expect(view.container.querySelector('[data-component-id="filter_stat_month"]')).toHaveAttribute('data-sticky-state', 'fallback'))
    const warning = screen.getByText('1 个冻结项因容器或空间限制已恢复普通文档流。')
    expect(warning.parentElement).toBe(stableStatusSlot)

    fireEvent.scroll(window)
    fireEvent.resize(window)
    await waitFor(() => expect(screen.getByText('1 个冻结项因容器或空间限制已恢复普通文档流。')).toBeInTheDocument())
    expect(view.container.querySelector('[data-component-id="filter_stat_month"]')).toHaveAttribute('data-sticky-state', 'fallback')
  })

  test('未知组件显示安全降级视图且保留其他组件', () => {
    const source = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ type: string; name: string }> }> }> }
    source.pages[0].blocks[0].components[0].type = 'FUTURE_CARD'
    source.pages[0].blocks[0].components[0].name = '未来组件'
    render(<ValidatedReportRenderer source={source} runtime={demoReportRuntime} mode="viewer" />)
    expect(screen.getByText('暂不支持的组件')).toBeInTheDocument()
    expect(screen.getByText(/未来组件.*FUTURE_CARD/)).toBeInTheDocument()
    expect(screen.getByRole('img', { name: '营业收入趋势趋势图' })).toBeInTheDocument()
  })

  test('单个组件异常由局部错误边界隔离', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const registry: ReportComponentRegistry = {
      CHART: () => { throw new Error('模拟图表异常') },
    }
    render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" registry={registry} />)
    expect(screen.getByText('组件暂时无法显示')).toBeInTheDocument()
    expect(screen.getByText('营业收入连续三个月增长，二季度增长动能较一季度明显增强。')).toBeInTheDocument()
  })

  test('合同损坏时阻止渲染并展示错误路径', () => {
    const source = structuredClone(reportExample) as unknown as { schemaVersion: string }
    source.schemaVersion = '2.0'
    render(<ValidatedReportRenderer source={source} runtime={demoReportRuntime} mode="viewer" />)
    expect(screen.getByRole('heading', { name: '报告配置无法加载' })).toBeInTheDocument()
    expect(screen.getByText('schemaVersion')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: '营业收入趋势趋势图' })).not.toBeInTheDocument()
  })

  test('权限或角色不满足时不渲染分块数据', () => {
    render(<ValidatedReportRenderer source={reportExample} runtime={{ ...demoReportRuntime, roleCodes: ['REPORT_DESIGNER'] }} mode="viewer" />)
    expect(screen.getByText('无权查看此分块')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: '营业收入趋势趋势图' })).not.toBeInTheDocument()
  })

  test('筛选变化生成精确数据集映射并刷新图表与依赖结论', async () => {
    const executor = vi.fn(demoReportInteractionExecutor)
    render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" onInteractionRequest={executor} />)

    fireEvent.change(screen.getByRole('combobox', { name: '统计月份' }), { target: { value: JSON.stringify({ value: '2026-04' }) } })

    await waitFor(() => expect(screen.getByText('2026-04 营业收入为 57，筛选结果已同步到图表与结论。')).toBeInTheDocument())
    expect(executor).toHaveBeenCalledWith(expect.objectContaining({
      type: 'PARAMETER_CHANGE', parameterId: 'param_stat_month', parameterCode: 'stat_month', value: '2026-04',
      affectedComponentIds: ['chart_revenue_trend', 'conclusion_overview'],
      targets: [{
        componentId: 'chart_revenue_trend', datasetVersionId: 'dsv_enterprise_revenue_v3', semanticFieldCode: 'stat_month',
        fieldId: 'field_stat_month', datasetParameterCode: 'stat_month', operator: 'EQUALS',
      }],
    }))
    expect(screen.getByRole('status')).toHaveTextContent('联动刷新完成')
  })

  test('较早请求晚返回时不会覆盖最新筛选结果', async () => {
    const first = deferred<ReportInteractionExecutionResult>()
    const second = deferred<ReportInteractionExecutionResult>()
    const executor = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const registry: ReportComponentRegistry = {
      FILTER: ({ onInteraction }) => <div><button type="button" onClick={() => onInteraction?.({ value: '2026-04' })}>请求四月</button><button type="button" onClick={() => onInteraction?.({ value: '2026-05' })}>请求五月</button></div>,
    }
    render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" registry={registry} onInteractionRequest={executor} />)

    fireEvent.click(screen.getByRole('button', { name: '请求四月' }))
    fireEvent.click(screen.getByRole('button', { name: '请求五月' }))
    expect(executor).toHaveBeenCalledTimes(2)
    expect(executor.mock.calls.map(call => call[0].requestRevision)).toEqual([1, 2])

    second.resolve(interactionResult('最新五月结果', '2026-05', 63))
    await waitFor(() => expect(screen.getByText('最新五月结果')).toBeInTheDocument())
    first.resolve(interactionResult('过期四月结果', '2026-04', 57))
    await Promise.resolve()
    expect(screen.getByText('最新五月结果')).toBeInTheDocument()
    expect(screen.queryByText('过期四月结果')).not.toBeInTheDocument()
  })

  test('不相交组件的并发请求分别完成且不会遗留加载态', async () => {
    const { document, runtime } = disjointInteractionFixture()
    const first = deferred<ReportInteractionExecutionResult>()
    const second = deferred<ReportInteractionExecutionResult>()
    const executor = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const registry: ReportComponentRegistry = {
      CHART: ({ component, runtime: current, onInteraction }) => {
        const state = current.componentData[component.id]
        const label = (state?.data as { label?: string } | undefined)?.label ?? ''
        return <div><button type="button" onClick={() => onInteraction?.({ value: '2026-05' })}>请求{component.id}</button><span>{component.id}:{state?.status}:{label}</span></div>
      },
    }
    render(<ReportRenderer document={document} runtime={runtime} mode="viewer" registry={registry} onInteractionRequest={executor} />)

    fireEvent.click(screen.getByRole('button', { name: '请求chart_revenue_trend' }))
    expect(screen.getByText('chart_revenue_trend:LOADING:')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '请求chart_secondary' }))
    expect(screen.getByText('chart_revenue_trend:LOADING:')).toBeInTheDocument()
    expect(screen.getByText('chart_secondary:LOADING:第二初始值')).toBeInTheDocument()

    second.resolve({ componentData: { chart_secondary: { status: 'READY', data: { label: '第二请求完成' } } } })
    await waitFor(() => expect(screen.getByText('chart_secondary:READY:第二请求完成')).toBeInTheDocument())
    expect(screen.getByText('chart_revenue_trend:LOADING:')).toBeInTheDocument()

    first.resolve({ componentData: { chart_revenue_trend: { status: 'READY', data: { label: '第一请求完成' } } } })
    await waitFor(() => expect(screen.getByText('chart_revenue_trend:READY:第一请求完成')).toBeInTheDocument())
  })

  test('联动失败后的成功重试会解除组件错误隔离', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const executor = vi.fn()
      .mockRejectedValueOnce(new Error('模拟首次失败'))
      .mockResolvedValueOnce(interactionResult('重试恢复结果', '2026-05', 63))
    render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" onInteractionRequest={executor} />)

    fireEvent.change(screen.getByRole('combobox', { name: '统计月份' }), { target: { value: JSON.stringify({ value: '2026-04' }) } })
    await waitFor(() => expect(screen.getAllByText('组件暂时无法显示')).toHaveLength(2))

    fireEvent.change(screen.getByRole('combobox', { name: '统计月份' }), { target: { value: JSON.stringify({ value: '2026-05' }) } })
    await waitFor(() => expect(screen.getByText('重试恢复结果')).toBeInTheDocument())
    expect(screen.queryByText('组件暂时无法显示')).not.toBeInTheDocument()
  })

  test('切换运行实例会清理下钻、忙碌状态并隔离旧异步结果', async () => {
    const pending = deferred<ReportInteractionExecutionResult>()
    const executor = vi.fn()
      .mockImplementationOnce(demoReportInteractionExecutor)
      .mockReturnValueOnce(pending.promise)
    const view = render(<ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" onInteractionRequest={executor} />)

    fireEvent.click(screen.getByRole('button', { name: '下钻至季度' }))
    await waitFor(() => expect(screen.getByText('当前：季度')).toBeInTheDocument())
    fireEvent.change(screen.getByRole('combobox', { name: '统计月份' }), { target: { value: JSON.stringify({ value: '2026-04' }) } })
    expect(screen.getByRole('combobox', { name: '统计月份' })).toBeDisabled()

    view.rerender(<ValidatedReportRenderer source={reportExample} runtime={structuredClone(demoReportRuntime)} mode="viewer" onInteractionRequest={executor} />)
    expect(screen.getByText('当前：汇总')).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '统计月份' })).not.toBeDisabled()

    pending.resolve(interactionResult('旧运行结果', '2026-04', 57))
    await Promise.resolve()
    expect(screen.queryByText('旧运行结果')).not.toBeInTheDocument()
  })

  test('严格模式下请求完成后会解除交互按钮忙碌态', async () => {
    render(<StrictMode><ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" onInteractionRequest={demoReportInteractionExecutor} /></StrictMode>)

    fireEvent.click(screen.getByRole('button', { name: '下钻至季度' }))

    await waitFor(() => expect(screen.getByText('当前：季度')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: '下钻至月份' })).not.toBeDisabled()
  })
})

function componentIDs(container: HTMLElement): string[] {
  return [...container.querySelectorAll<HTMLElement>('[data-component-id]')].map(element => element.dataset.componentId ?? '')
}

function interactionResult(summary: string, month: string, value: number): ReportInteractionExecutionResult {
  return { componentData: {
    chart_revenue_trend: { status: 'READY', data: { points: [{ id: month, label: month, value, semanticValues: { stat_month: month } }] } },
    conclusion_overview: { status: 'READY', data: { summary } },
  } }
}

function disjointInteractionFixture(): { document: ReportDocument; runtime: ReportRuntimeContext } {
  const document = structuredClone(reportExample) as ReportDocument
  const primary = document.pages[0].blocks.flatMap(block => block.components).find(component => component.id === 'chart_revenue_trend')!
  const conclusion = document.pages[0].blocks.flatMap(block => block.components).find(component => component.id === 'conclusion_overview')!
  const primaryLinkage = primary.interaction!.linkage as Record<string, unknown>
  primaryLinkage.effectScope = { kind: 'COMPONENTS', componentIds: ['chart_revenue_trend'] }
  conclusion.binding!.chartComponentIds = []

  const secondary = structuredClone(primary)
  secondary.id = 'chart_secondary'
  secondary.name = '第二图表'
  ;(secondary.interaction!.linkage as Record<string, unknown>).parameterId = 'param_stat_month_secondary'
  ;(secondary.interaction!.linkage as Record<string, unknown>).effectScope = { kind: 'COMPONENTS', componentIds: ['chart_secondary'] }
  document.pages[0].blocks[0].components.push(secondary)

  const secondaryParameter = structuredClone(document.parameters![0])
  secondaryParameter.id = 'param_stat_month_secondary'
  secondaryParameter.code = 'stat_month_secondary'
  secondaryParameter.semanticBinding!.datasetFields[0].datasetParameterCode = 'stat_month_secondary'
  document.parameters!.push(secondaryParameter)

  const runtime = structuredClone(demoReportRuntime)
  runtime.parameters.stat_month_secondary = '2026-06'
  runtime.componentData.chart_secondary = { status: 'READY', data: { label: '第二初始值' } }
  return { document, runtime }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
