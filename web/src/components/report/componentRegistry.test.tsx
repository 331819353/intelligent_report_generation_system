import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { createReportComponentDefaults, formalReportComponentTypes } from '../../lib/report-component-catalog'
import type { ReportComponent, ReportParameter, ReportRuntimeContext } from '../../lib/report-contract'
import { ReportErrorBoundary } from './ReportErrorBoundary'
import { defaultComponentRegistry } from './componentRegistry'

describe('第一批报告组件渲染器', () => {
  test('目录中的每类组件都有正式渲染器并可从 JSON 配置创建', () => {
    for (const type of formalReportComponentTypes) expect(defaultComponentRegistry[type], type).toBeTypeOf('function')
    const cases = [
      ['TITLE', '测试标题'], ['RICH_TEXT', '正文内容'], ['FILTER', '统计月份'], ['KPI', '1,234.5'],
      ['ADDITIONAL_INFO', '补充说明'], ['TABLE', '华东'], ['IMAGE', '未配置可用的图片地址'],
      ['ATTACHMENT_LIST', '经营附件'], ['DATA_SOURCE', '经营数据集'], ['UPDATED_AT', '2026'], ['CONCLUSION', '增长保持稳定'],
    ] as const
    for (const [type, expected] of cases) {
      const component = componentOf(type)
      if (type === 'TITLE') component.binding = { text: '测试标题' }
      if (type === 'RICH_TEXT') component.binding = { text: '正文内容' }
      if (type === 'FILTER') {
        component.name = '统计月份'
        component.binding = { ...component.binding, parameterId: 'param_stat_month' }
      }
      if (type === 'ATTACHMENT_LIST') component.binding = { attachments: [{ name: '经营附件', url: '/files/demo.pdf' }] }
      const view = renderRenderer(component, runtimeFor(component.id, type), type === 'FILTER' ? monthParameter : undefined)
      expect(screen.getByText(expected, { exact: false }), type).toBeInTheDocument()
      view.unmount()
    }
  })

  test.each(['COLUMN', 'BAR', 'LINE', 'PIE'] as const)('图表正式支持 %s 类型', chartType => {
    const component = componentOf('CHART')
    component.name = '销售结构'
    component.binding = { ...component.binding, chart: { type: chartType } }
    renderRenderer(component, runtimeFor(component.id, 'CHART'))
    const names = { COLUMN: '销售结构柱状图', BAR: '销售结构条形图', LINE: '销售结构趋势图', PIE: '销售结构饼图' }
    expect(screen.getByRole('img', { name: names[chartType] })).toBeInTheDocument()
  })

  test('筛选器按参数 code 读取运行值并支持手动提交', () => {
    const component = componentOf('FILTER')
    component.name = '统计月份'
    component.binding = {
      ...component.binding,
      parameterId: monthParameter.id,
      control: 'SELECT',
      options: [{ label: '2026年5月', value: '2026-05' }, { label: '2026年6月', value: '2026-06' }],
    }
    component.interaction = { autoSubmit: false }
    const onInteraction = vi.fn()
    const Renderer = defaultComponentRegistry.FILTER
    render(<Renderer component={component} runtime={{ parameters: { [monthParameter.code]: '2026-06', [monthParameter.id]: '错误值' }, componentData: {} }} mode="viewer" parameter={monthParameter} onInteraction={onInteraction} />)

    const select = screen.getByRole('combobox', { name: '统计月份' })
    expect(select).toHaveValue(JSON.stringify({ value: '2026-06' }))
    fireEvent.change(select, { target: { value: JSON.stringify({ value: '2026-05' }) } })
    expect(onInteraction).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '应用' }))
    expect(onInteraction).toHaveBeenCalledWith({ value: '2026-05' })
  })

  test('筛选选项展示加载和错误状态', () => {
    const component = componentOf('FILTER')
    component.name = '统计月份'
    component.binding = { ...component.binding, parameterId: monthParameter.id }
    const Renderer = defaultComponentRegistry.FILTER
    const loading = render(<Renderer component={component} runtime={{ parameters: {}, parameterOptions: { stat_month: { status: 'LOADING' } }, componentData: {} }} mode="viewer" parameter={monthParameter} />)
    expect(screen.getByRole('status')).toHaveTextContent('正在加载选项')
    expect(screen.getByRole('combobox', { name: '统计月份' })).toBeDisabled()
    loading.unmount()

    render(<Renderer component={component} runtime={{ parameters: {}, parameterOptions: { stat_month: { status: 'ERROR', errorMessage: '选项服务不可用' } }, componentData: {} }} mode="viewer" parameter={monthParameter} />)
    expect(screen.getByRole('alert')).toHaveTextContent('选项服务不可用')
  })

  test('图表仅用机器可读语义值触发点击联动并提供下钻按钮', () => {
    const component = componentOf('CHART')
    component.name = '区域收入'
    component.binding = { ...component.binding, datasetVersionId: 'dsv_sales', dimensions: [{ fieldId: 'field_region', role: 'CATEGORY' }], chart: { type: 'BAR' } }
    component.interaction = {
      clickFilter: true,
      linkage: { parameterId: 'param_region', operator: 'EQUALS', effectScope: { kind: 'REPORT' } },
      drill: { levels: [
        { fieldId: 'field_city', semanticFieldCode: 'enterprise_city', label: '城市' },
        { fieldId: 'field_district', semanticFieldCode: 'enterprise_district', label: '区县' },
      ] },
    }
    const parameter: ReportParameter = {
      id: 'param_region', code: 'region', name: '区域', dataType: 'STRING', required: true, multiValue: false, scope: 'REPORT',
      semanticBinding: { semanticFieldCode: 'enterprise_region', datasetFields: [{ datasetVersionId: 'dsv_sales', fieldId: 'field_region', datasetParameterCode: 'region' }] },
    }
    const runtime: ReportRuntimeContext = {
      parameters: { region: '华东' },
      componentData: { [component.id]: { status: 'READY', data: { points: [
        { id: 'east', label: '华东', value: 60, semanticValues: { enterprise_region: '华东' } },
        { id: 'south', label: '华南', value: 40, semanticValues: { enterprise_region: '华南' } },
      ] } } },
    }
    const onInteraction = vi.fn()
    const Renderer = defaultComponentRegistry.CHART
    const view = render(<Renderer component={component} runtime={runtime} mode="viewer" parameter={parameter} onInteraction={onInteraction} drillLevel={-1} />)

    expect(screen.getByRole('button', { name: '华东' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: '华南' }))
    expect(onInteraction).toHaveBeenCalledWith({ value: '华南', label: '华南' })
    fireEvent.click(screen.getByRole('button', { name: '下钻至城市' }))
    expect(onInteraction).toHaveBeenCalledWith({ value: undefined, drillLevel: 0, drillDirection: 'DOWN' })

    view.rerender(<Renderer component={component} runtime={runtime} mode="viewer" parameter={parameter} onInteraction={onInteraction} drillLevel={1} />)
    fireEvent.click(screen.getByRole('button', { name: '返回上级' }))
    expect(onInteraction).toHaveBeenCalledWith({ value: undefined, drillLevel: 0, drillDirection: 'UP' })
  })

  test('统一展示加载态，并由局部错误边界隔离异常态', () => {
    const component = componentOf('KPI')
    const first = renderRenderer(component, { parameters: {}, componentData: { [component.id]: { status: 'LOADING' } } })
    expect(screen.getByRole('status')).toHaveTextContent('正在加载组件数据')
    first.unmount()

    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const Renderer = defaultComponentRegistry.KPI
    render(<ReportErrorBoundary componentName={component.name}><Renderer component={component} mode="viewer" runtime={{ parameters: {}, componentData: { [component.id]: { status: 'ERROR', errorMessage: '模拟失败' } } }} /></ReportErrorBoundary>)
    expect(screen.getByRole('alert')).toHaveTextContent('组件暂时无法显示')
  })

  test('图片和附件拒绝可执行资源协议', () => {
    const image = componentOf('IMAGE')
    image.binding = { url: 'javascript:alert(1)', alt: '恶意图片' }
    const first = renderRenderer(image, runtimeFor(image.id, 'IMAGE'))
    expect(screen.getByText('未配置可用的图片地址')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: '恶意图片' })).not.toBeInTheDocument()
    first.unmount()

    const attachment = componentOf('ATTACHMENT_LIST')
    attachment.binding = { attachments: [{ name: '不安全附件', url: 'data:text/html,<script>alert(1)</script>' }] }
    renderRenderer(attachment, runtimeFor(attachment.id, 'ATTACHMENT_LIST'))
    expect(screen.getByText('不安全附件')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '不安全附件' })).not.toBeInTheDocument()
  })
})

function componentOf(type: typeof formalReportComponentTypes[number]): ReportComponent {
  return {
    id: `test_${type.toLowerCase()}`,
    type,
    ...createReportComponentDefaults(type),
    grid: { x: 0, y: 0, w: 4, h: 4 },
    visible: true,
    manualLocked: false,
    sticky: { enabled: false },
    sourceTrace: [],
  }
}

const monthParameter: ReportParameter = {
  id: 'param_stat_month', code: 'stat_month', name: '统计月份', dataType: 'DATE_MONTH', required: true, multiValue: false, scope: 'REPORT',
}

function runtimeFor(componentID: string, type: string): ReportRuntimeContext {
  const dataByType: Record<string, unknown> = {
    KPI: { value: 1234.5, trend: 8.2 },
    TABLE: { rows: [{ 区域: '华东', 收入: 120 }] },
    CHART: { labels: ['华东', '华南'], values: [60, 40] },
    DATA_SOURCE: { sources: ['经营数据集'] },
    UPDATED_AT: { updatedAt: '2026-07-16T09:00:00+08:00' },
    CONCLUSION: { summary: '增长保持稳定' },
  }
  return { parameters: { stat_month: '2026-06' }, componentData: { [componentID]: { status: 'READY', data: dataByType[type], updatedAt: '2026-07-16T09:00:00+08:00' } } }
}

function renderRenderer(component: ReportComponent, runtime: ReportRuntimeContext, parameter?: ReportParameter) {
  const Renderer = defaultComponentRegistry[component.type]
  return render(<Renderer component={component} runtime={runtime} mode="viewer" parameter={parameter} />)
}
