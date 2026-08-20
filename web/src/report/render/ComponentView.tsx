import { useEffect, useMemo, useRef, useState } from 'react'
import { BarChart, FunnelChart, LineChart, PieChart, ScatterChart } from 'echarts/charts'
import { AriaComponent, GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { init, use as registerEChartsComponents } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { CaretLeft, CaretRight, Image as ImageIcon, Info } from '@phosphor-icons/react'
import { ComponentStateView } from '../runtime/ComponentStateView.tsx'
import { componentPresentation, type ReportComponentState } from '../runtime/state.ts'
import { buildChartOption, formatNumber, resolveColumns, singleMetric, type QueryResult } from './chart-option.ts'
import { FilterControl } from './FilterControl.tsx'
import type { ComponentManifest, ManifestIndex } from './manifests.ts'
import type { GlobalFilter, ReportComponent } from './schema.ts'

registerEChartsComponents([
  BarChart, LineChart, PieChart, ScatterChart, FunnelChart,
  GridComponent, LegendComponent, TooltipComponent, AriaComponent, CanvasRenderer,
])

export type ComponentResult = {
  componentId: string
  state: ReportComponentState | string
  result?: QueryResult
  errorCode?: string
}

function EChartsView({ option, label, onPick }: {
  option: Record<string, unknown>
  label: string
  /** 存在时图表可点击；参数是被点的类目值。 */
  onPick?: (category: string) => void
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const element = containerRef.current
    if (!element) return undefined
    const chart = init(element)
    chart.setOption(option, true)
    if (onPick) {
      chart.on('click', (params: { name?: string }) => {
        if (typeof params.name === 'string' && params.name !== '') onPick(params.name)
      })
    }
    const observer = new ResizeObserver(() => chart.resize())
    observer.observe(element)
    return () => { observer.disconnect(); chart.dispose() }
  }, [onPick, option])
  return <div ref={containerRef} className={`report-render-chart ${onPick ? 'is-pickable' : ''}`.trim()}
    role="img" aria-label={label} />
}

function TableView({ component, result }: { component: ReportComponent; result: QueryResult }) {
  const pageSize = Math.max(1, component.options.tablePageSize ?? 20)
  const [page, setPage] = useState(0)
  const pageCount = Math.max(1, Math.ceil(result.rows.length / pageSize))
  const current = Math.min(page, pageCount - 1)
  const rows = result.rows.slice(current * pageSize, current * pageSize + pageSize)
  const numericColumn = result.columns.map((_, index) =>
    result.rows.some(row => Number.isFinite(Number(row[index]))))
  return <div className="report-render-table">
    <div className="report-render-table-scroll">
      <table>
        <thead><tr>{result.columns.map(column => <th key={column}>{column}</th>)}</tr></thead>
        <tbody>{rows.map((row, rowIndex) => <tr key={current * pageSize + rowIndex}>
          {result.columns.map((column, columnIndex) => <td className={numericColumn[columnIndex] ? 'is-numeric' : ''} key={column}>
            {numericColumn[columnIndex]
              ? formatNumber(row[columnIndex], component.options.numberFormat)
              : String(row[columnIndex] ?? '—')}
          </td>)}
        </tr>)}</tbody>
      </table>
    </div>
    {pageCount > 1 && <nav className="report-render-table-pager" aria-label="表格分页">
      <button type="button" aria-label="上一页" disabled={current === 0} onClick={() => setPage(current - 1)}><CaretLeft size={13} /></button>
      <span>{current + 1} / {pageCount}<small>共 {result.rows.length} 行</small></span>
      <button type="button" aria-label="下一页" disabled={current >= pageCount - 1} onClick={() => setPage(current + 1)}><CaretRight size={13} /></button>
    </nav>}
  </div>
}

function MetricView({ component, result }: { component: ReportComponent; result: QueryResult }) {
  const bindings = component.dataBinding?.measures ?? []
  const primaryIndexes = bindings.map((binding, index) => ({ binding, index }))
    .filter(item => item.binding.role === 'VALUE')
  if (primaryIndexes.length === 0) {
    const metric = singleMetric(result, component.dataBinding)
    const columns = resolveColumns(result, component.dataBinding)
    const comparison = columns.valueIndexes[1]
    return <div className="report-render-metric"><article>
      <strong>{formatNumber(metric.value, component.options.numberFormat)}</strong>
      <span>{metric.label}</span>
      {comparison !== undefined && <em>{result.columns[comparison]} {formatNumber(result.rows[0]?.[comparison], component.options.numberFormat)}</em>}
    </article></div>
  }
  const byName = new Map(result.columns.map((column, index) => [column, index]))
  const dimensionCount = component.dataBinding?.dimensions?.length ?? 0
  const columnIndex = (bindingIndex: number) => byName.get(bindings[bindingIndex].field) ?? dimensionCount + bindingIndex
  return <div className={'report-render-metric ' + (primaryIndexes.length > 1 ? 'is-multiple' : '')}>
    {primaryIndexes.map((primary, position) => {
      const nextPrimary = primaryIndexes[position + 1]?.index ?? bindings.length
      const comparisons = bindings.map((binding, index) => ({ binding, index }))
        .filter(item => item.index > primary.index && item.index < nextPrimary && item.binding.role === 'TOOLTIP')
      const valueIndex = columnIndex(primary.index)
      return <article key={primary.binding.field + '-' + primary.index}>
        <span>{result.columns[valueIndex] || primary.binding.field}</span>
        <strong>{formatNumber(result.rows[0]?.[valueIndex], component.options.numberFormat)}</strong>
        {comparisons.length > 0 && <div>
          {comparisons.map(item => {
            const index = columnIndex(item.index)
            return <em key={item.binding.field + '-' + item.index}>
              <small>{result.columns[index] || item.binding.field}</small>{formatNumber(result.rows[0]?.[index], component.options.numberFormat)}
            </em>
          })}
        </div>}
      </article>
    })}
  </div>
}

/**
 * 富文本与洞察文本由服务端 SanitizeRichText 归一化后才允许进入 Definition，
 * 因此这里按纯文本渲染即可；渲染器不再向 DOM 注入 HTML。
 */
function TextView({ component }: { component: ReportComponent }) {
  const text = component.options.richText?.trim()
  if (!text) return <p className="report-render-text is-empty">尚未填写文字内容</p>
  return <div className={`report-render-text ${component.options.insightRole ? 'is-insight' : ''}`.trim()}>
    {component.options.insightRole && <span className="report-render-insight-role">{component.options.insightRole}</span>}
    {text.split(/\n+/).map((paragraph, index) => <p key={index}>{paragraph}</p>)}
  </div>
}

function ImageView({ component }: { component: ReportComponent }) {
  if (!component.options.imageAssetId) {
    return <div className="report-render-placeholder"><ImageIcon size={22} /><span>尚未选择图片素材</span></div>
  }
  return <img className="report-render-image" src={`/api/v1/report-assets/${encodeURIComponent(component.options.imageAssetId)}`}
    alt={component.options.title || '报告图片'} />
}

export type InlineFilterControl = {
  filter: GlobalFilter
  value: unknown
  onChange: (next: unknown) => void
}

/**
 * 筛选控件就是画布元素：编辑器先把字段与报告筛选定义关联，运行页再在原位置
 * 收集取值。这样筛选不会被运行页额外复制到一个固定顶部栏里。
 */
function ControlView({ component, inlineFilter }: { component: ReportComponent; inlineFilter?: InlineFilterControl }) {
  const bound = component.dataBinding?.dimensions?.map(item => item.field).join('、')
  if (inlineFilter) {
    return <div className="report-inline-filter">
      <FilterControl filter={inlineFilter.filter} value={inlineFilter.value} onChange={inlineFilter.onChange} />
    </div>
  }
  return <div className="report-render-placeholder is-control">
    <Info size={18} />
    <span>{bound ? `筛选字段：${bound}` : '该筛选组件尚未绑定字段'}</span>
    <small>{bound ? '发布后可直接在画布中的此位置筛选' : '先在右侧配置字段与筛选规则'}</small>
  </div>
}

export type ComponentViewProps = {
  component: ReportComponent
  manifests: ManifestIndex
  item?: ComponentResult
  mobile?: boolean
  /** 编辑器草稿预览没有执行结果，用它说明组件已配置但尚未运行。 */
  designMode?: boolean
  onRetry?: () => void
  /**
   * 该组件是某条联动的源时提供。参数是被点击处、按本组件绑定维度键出的取值；
   * 由服务端按定义中声明的 Interaction 决定它会影响哪些组件。
   */
  onSelect?: (values: Record<string, unknown>) => void
  selected?: boolean
  dimmed?: boolean
  inlineFilter?: InlineFilterControl
}

/**
 * 组件渲染的唯一入口：按清单声明的 renderer 分派，按 options 表现，
 * 按 dataBinding 的角色解析结果列。这里不含任何针对具体报告的分支。
 */
export function ComponentView({
  component, manifests, item, mobile, designMode, onRetry, onSelect, selected, dimmed, inlineFilter,
}: ComponentViewProps) {
  const manifest = manifests.get(component.templateRef.type, component.templateRef.version)
  const result = item?.result
  const state = item?.state ?? (designMode ? 'EMPTY' : 'LOADING')

  /**
   * 点击一个类目即选中它。图表只知道自己被点在哪个类目上，因此把取值按本组件
   * 绑定的类目字段键出；「这次点击会影响谁」由服务端依据声明的联动决定。
   * 组件清单未声明 CLICK_FILTER 能力时不接受点击。
   */
  const pick = useMemo(() => {
    if (!onSelect || !result || !manifest?.supportedInteractions.includes('CLICK_FILTER')) return undefined
    const columns = resolveColumns(result, component.dataBinding)
    if (columns.categoryIndex < 0) return undefined
    const field = result.columns[columns.categoryIndex]
    if (!field) return undefined
    return (category: string) => onSelect({ [field]: category })
  }, [component.dataBinding, manifest, onSelect, result])

  const option = useMemo(() => {
    if (!result || manifest?.renderer !== 'ECHARTS') return undefined
    return buildChartOption({
      manifest, type: component.templateRef.type, options: component.options,
      binding: component.dataBinding, result, mobile,
    })
  }, [component.dataBinding, component.options, component.templateRef.type, manifest, mobile, result])

  const body = () => {
    // 文本、图片与控件不依赖查询结果，可以在草稿态直接呈现真实内容。
    if (manifest?.renderer === 'TEXT') return <TextView component={component} />
    if (manifest?.renderer === 'IMAGE') return <ImageView component={component} />
    if (manifest?.renderer === 'CONTROL') return <ControlView component={component} inlineFilter={inlineFilter} />
    if (!manifest) {
      return <ComponentStateView state="ERROR" boundTitle={`未注册的组件模板 ${component.templateRef.type}@${component.templateRef.version}`} />
    }
    if (!result) {
      if (designMode) {
        return <div className="report-render-placeholder">
          <Info size={18} />
          <span>{component.dataBinding ? '已绑定受治理数据' : '该组件不需要数据绑定'}</span>
          <small>{component.dataBinding ? '预览与发布时按查看者权限执行查询' : ''}</small>
        </div>
      }
      return <ComponentStateView state={state} boundTitle={component.options.title} errorCode={item?.errorCode} onAction={onRetry} />
    }
    if (result.rows.length === 0) return <ComponentStateView state="EMPTY" boundTitle={component.options.title} onAction={onRetry} />
    if (manifest.renderer === 'ECHARTS' && option) {
      return <EChartsView option={option} onPick={pick} label={`${component.options.title || manifest.displayName}图表`} />
    }
    if (manifest.category === 'TABLE') return <TableView component={component} result={result} />
    if (component.templateRef.type === 'metric-card') return <MetricView component={component} result={result} />
    return <TableView component={component} result={result} />
  }

  const failed = !result && state !== 'READY' && state !== 'EMPTY' && !designMode &&
    manifest?.renderer !== 'TEXT' && manifest?.renderer !== 'IMAGE' && manifest?.renderer !== 'CONTROL'

  const classes = ['report-render-component']
  if (selected) classes.push('is-selected-source')
  if (dimmed) classes.push('is-dimmed')
  return <article className={classes.join(' ')} data-component-id={component.id}>
    <header className="report-render-component-head">
      <div>
        <h3>{state === 'NO_PERMISSION' ? '受限组件' : component.options.title || manifest?.displayName || component.templateRef.type}</h3>
        {state !== 'NO_PERMISSION' && component.options.subtitle && <p>{component.options.subtitle}</p>}
      </div>
      {item && state !== 'READY' && <span className={`report-state-chip is-${String(state).toLocaleLowerCase()}`}>{componentPresentation(String(state)).label}</span>}
    </header>
    <div className="report-render-component-body">{failed
      ? <ComponentStateView state={state} boundTitle={state === 'NO_PERMISSION' ? undefined : component.options.title} errorCode={item?.errorCode} onAction={onRetry} />
      : body()}</div>
    {item?.state === 'PARTIAL' && <p className="report-render-disclosure">查询返回部分结果，请谨慎解读。</p>}
  </article>
}

export type { ComponentManifest }
