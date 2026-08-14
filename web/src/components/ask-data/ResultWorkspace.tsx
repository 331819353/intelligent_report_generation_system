import {
  CaretDown,
  CaretLeft,
  CaretRight,
  DownloadSimple,
  Eye,
  ShieldCheck,
  TrendUp,
} from '@phosphor-icons/react'
import { BarChart, LineChart } from 'echarts/charts'
import { AriaComponent, GridComponent, TooltipComponent } from 'echarts/components'
import { init, use as registerEChartsComponents } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { useEffect, useMemo, useRef, useState } from 'react'
import { exportTimeSpecFooter } from '../../export/timespec-footer'
import { renderTimeSpec, timePolicySourceLabel, timeSpecSummaryLabel } from '../../askdata/format/timespec'
import { AnswerStatusBadges, AnswerSummary } from '../../askdata/AnswerSummary'
import type {
  QuestionAnswerPresentation,
  QuestionResult,
  QuestionResultColumn,
  QuestionResultDataset,
  QuestionResultView,
} from '../../lib/ask-data-api'
import {
  eligibleResultViews,
  formatResultCell,
  initialResultView,
  numericResultValue,
  resultDataset,
  resultPageCount,
} from './result-presentation'

registerEChartsComponents([LineChart, BarChart, GridComponent, TooltipComponent, AriaComponent, CanvasRenderer])

type ResultWorkspaceProps = {
  result: QuestionResult
  answer?: QuestionAnswerPresentation
  onRetryNarrative?: () => void
}

type ResultChartProps = {
  result: QuestionResult
  view: QuestionResultView
  compact?: boolean
}

const formatChartNumber = (value: number) => new Intl.NumberFormat('zh-CN', {
  notation: value >= 1_000_000 ? 'compact' : 'standard',
  maximumFractionDigits: 1,
}).format(value)

function ResultChart({ result, view, compact = false }: ResultChartProps) {
  const chartRef = useRef<HTMLDivElement>(null)
  const dataset = resultDataset(result, view.datasetId)

  useEffect(() => {
    if (!chartRef.current || !dataset) return undefined
    const chart = init(chartRef.current)
		const dimensionKey = (view.dimensionKeys ?? [])[0]
		const measureKey = (view.measureKeys ?? [])[0]
    const labels = dataset.rows.map(row => row[dimensionKey] ?? '—')
    const values = dataset.rows.map(row => numericResultValue(row[measureKey]) ?? 0)
    const dimension = dataset.columns.find(column => column.key === dimensionKey)
    const measure = dataset.columns.find(column => column.key === measureKey)
    const common = {
      animationDuration: 360,
      aria: { enabled: true, description: `${dataset.label}，${measure?.label ?? '指标'}按${dimension?.label ?? '维度'}展示。` },
      tooltip: {
        trigger: 'axis',
        backgroundColor: '#ffffff',
        borderColor: '#dfe4ea',
        borderWidth: 1,
        textStyle: { color: '#273142', fontSize: 10 },
        valueFormatter: (value: number) => formatChartNumber(value),
      },
      grid: view.type === 'BAR'
        ? { left: compact ? 8 : 16, right: compact ? 63 : 76, top: 12, bottom: 12, outerBoundsMode: 'same', outerBoundsContain: 'axisLabel' }
        : { left: 10, right: 14, top: 20, bottom: 8, outerBoundsMode: 'same', outerBoundsContain: 'axisLabel' },
    }
    chart.setOption(view.type === 'BAR' ? {
      ...common,
      xAxis: { type: 'value', splitLine: { show: false }, axisLabel: { show: false }, axisLine: { show: false }, axisTick: { show: false } },
      yAxis: { type: 'category', inverse: true, data: labels, axisLabel: { color: '#67717f', fontSize: 9 }, axisLine: { show: false }, axisTick: { show: false } },
      series: [{
        type: 'bar', data: values.map((value, index) => ({ value, itemStyle: { color: index === 0 ? '#0872d3' : '#9cc5ec', borderRadius: [0, 4, 4, 0] } })),
        barWidth: compact ? 9 : 12, showBackground: true, backgroundStyle: { color: '#f1f4f8', borderRadius: 4 },
        label: { show: true, position: 'right', color: '#566171', fontSize: 7, formatter: ({ value }: { value: number }) => {
          const total = numericResultValue(result.summary.value) ?? 0
          const share = total > 0 ? ` (${(value / total * 100).toFixed(1)}%)` : ''
          return `${value.toLocaleString('zh-CN')}${share}`
        } },
      }],
    } : {
      ...common,
      xAxis: { type: 'category', boundaryGap: false, data: labels.map(label => String(label).slice(5)), axisLabel: { color: '#8d96a2', fontSize: 8 }, axisLine: { lineStyle: { color: '#dfe4ea' } }, axisTick: { show: false } },
      yAxis: { type: 'value', min: 0, splitNumber: 3, axisLabel: { color: '#8d96a2', fontSize: 8, formatter: (value: number) => formatChartNumber(value) }, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLine: { show: false }, axisTick: { show: false } },
      series: [{
        type: 'line', data: values, smooth: false, symbol: 'circle', symbolSize: 6,
        lineStyle: { width: 2, color: '#0872d3' }, itemStyle: { color: '#0872d3', borderColor: '#fff', borderWidth: 2 },
        label: { show: true, position: 'top', color: '#687485', fontSize: 7, formatter: ({ value }: { value: number }) => value.toLocaleString('zh-CN') },
      }],
    })
    const observer = new ResizeObserver(() => chart.resize())
    observer.observe(chartRef.current)
    return () => {
      observer.disconnect()
      chart.dispose()
    }
  }, [compact, dataset, result.summary.value, view])

  return <div className={`ask-result-chart ${compact ? 'is-compact' : ''}`.trim()} ref={chartRef} role="img" aria-label={dataset?.label ?? view.label} />
}

function tableCell(value: string | null, column: QuestionResultColumn): string {
  const formatted = formatResultCell(value, column)
  if (formatted === '—') return formatted
  if (column.label.includes('占比')) return `${formatted}%`
  if (column.label.includes('客单价')) return `¥${formatted}`
  return formatted
}

function KPIBundle({ dataset, view }: { dataset: QuestionResultDataset; view: QuestionResultView }) {
  const row = dataset.rows[0]
  return <section className="ask-result-kpi-bundle" aria-labelledby="ask-kpi-bundle-title">
    <header>
      <div><strong id="ask-kpi-bundle-title">指标组结果</strong><small>同一受控查询口径</small></div>
      <span><ShieldCheck size={13} weight="fill" aria-hidden="true" />{view.measureKeys.length} 项已校验</span>
    </header>
    <dl>{view.measureKeys.map(key => {
      const column = dataset.columns.find(item => item.key === key)
      if (!column) return null
      return <div key={key}>
        <dt>{column.label}</dt>
        <dd>{tableCell(row[key] ?? null, column)}</dd>
        <small>当前发布数据快照</small>
      </div>
    })}</dl>
  </section>
}

function pageItems(totalPages: number, availablePages: number): Array<number | 'ellipsis'> {
  const visible = Math.min(totalPages, Math.max(availablePages, 3))
  if (visible <= 6) return Array.from({ length: visible }, (_, index) => index + 1)
  return [1, 2, 3, 'ellipsis', visible]
}

function ResultTable({ dataset, result }: { dataset: QuestionResultDataset; result: QuestionResult }) {
  const [pageSize, setPageSize] = useState(dataset.pageSize)
  const [page, setPage] = useState(1)
  const totalPages = resultPageCount({ ...dataset, pageSize })
  const availablePages = Math.max(1, Math.ceil(dataset.rows.length / pageSize))
  const safePage = Math.min(page, availablePages)
  const rows = dataset.rows.slice((safePage - 1) * pageSize, safePage * pageSize)

  const exportPage = () => {
    const header = dataset.columns.map(column => column.label)
    const body = rows.map(row => dataset.columns.map(column => row[column.key] ?? ''))
    const footer = result.resolvedTimeSpec ? exportTimeSpecFooter(result.resolvedTimeSpec) : [['数据口径', '当前已发布数据快照']]
    const csv = [header, ...body, [], ...footer].map(values => values.map(value => `"${String(value).replaceAll('"', '""')}"`).join(',')).join('\n')
    const url = URL.createObjectURL(new Blob([`\uFEFF${csv}`], { type: 'text/csv;charset=utf-8' }))
    const link = document.createElement('a')
    link.href = url
    link.download = 'ask-data-result-page.csv'
    link.click()
    URL.revokeObjectURL(url)
  }

  return <section className="ask-result-detail" aria-labelledby="result-detail-title">
    <header>
      <div><strong id="result-detail-title">{dataset.label}</strong><small>（当前页）</small></div>
      <button type="button" onClick={exportPage}><DownloadSimple size={13} aria-hidden="true" />导出当前页</button>
    </header>
    <div className="ask-result-data-table">
      <table>
        <caption className="sr-only">{dataset.label}</caption>
        <thead><tr>{dataset.columns.map(column => <th key={column.key} scope="col">{column.label}</th>)}</tr></thead>
        <tbody>{rows.map((row, rowIndex) => <tr key={`${safePage}-${rowIndex}`}>
          {dataset.columns.map(column => <td key={column.key}>{tableCell(row[column.key] ?? null, column)}</td>)}
        </tr>)}</tbody>
      </table>
    </div>
    <footer className="ask-result-pagination">
      <span>共 {dataset.totalRows.toLocaleString('zh-CN')} 行{dataset.rows.length < dataset.totalRows ? ` · 已加载前 ${dataset.rows.length} 行` : ''}</span>
      <label><span className="sr-only">每页行数</span><select value={pageSize} onChange={event => { setPageSize(Number(event.target.value)); setPage(1) }}>
        {[5, 10, 20].filter(size => size <= Math.max(20, dataset.rows.length)).map(size => <option key={size} value={size}>{size} 条/页</option>)}
      </select><CaretDown size={11} aria-hidden="true" /></label>
      <nav aria-label="结果分页">
        <button type="button" aria-label="上一页" disabled={safePage === 1} onClick={() => setPage(value => Math.max(1, value - 1))}><CaretLeft size={12} /></button>
        {pageItems(totalPages, availablePages).map((item, index) => item === 'ellipsis'
          ? <span key={`ellipsis-${index}`}>…</span>
          : <button type="button" key={item} aria-current={safePage === item ? 'page' : undefined} disabled={item > availablePages} onClick={() => setPage(item)}>{item}</button>)}
        <button type="button" aria-label="下一页" disabled={safePage >= availablePages} onClick={() => setPage(value => Math.min(availablePages, value + 1))}><CaretRight size={12} /></button>
      </nav>
    </footer>
  </section>
}

export function ResultWorkspace({ result, answer, onRetryNarrative }: ResultWorkspaceProps) {
  const views = useMemo(() => eligibleResultViews(result), [result])
  const initial = useMemo(() => initialResultView(result), [result])
  const [activeViewID, setActiveViewID] = useState(initial?.id ?? '')
  const [timeSpecOpen, setTimeSpecOpen] = useState(!answer?.narrativeDegraded)
  const activeView = views.find(view => view.id === activeViewID) ?? initial
  const lineView = views.find(view => view.type === 'LINE')
  const barView = views.find(view => view.type === 'BAR')
  const tableView = views.find(view => view.type === 'TABLE')
  const tableDataset = tableView ? resultDataset(result, tableView.datasetId) : undefined
	const bundleDataset = activeView?.type === 'KPI_BUNDLE' ? resultDataset(result, activeView.datasetId) : undefined

  if (!activeView) return null
  const showLine = activeView.type === 'LINE' && lineView
  const showBar = activeView.type === 'BAR' && barView
  const comparison = result.summary.comparison
  const timeSpec = result.resolvedTimeSpec ? renderTimeSpec(result.resolvedTimeSpec) : undefined
  const timeSpecID = 'ask-result-time-spec'

  return <section className="ask-result-workspace" aria-labelledby="ask-result-title">
    <header className="ask-result-workspace-heading">
      <div><strong id="ask-result-title">{result.title}</strong><ShieldCheck size={13} weight="fill" aria-label="结果已通过校验" />{answer && <AnswerStatusBadges answer={answer} />}</div>
      <div>
        {tableView && <button type="button" onClick={() => setActiveViewID(tableView.id)}><Eye size={13} aria-hidden="true" />查看明细</button>}
        <button type="button" onClick={() => tableDataset && document.querySelector<HTMLButtonElement>('.ask-result-detail header button')?.click()}><DownloadSimple size={13} aria-hidden="true" />导出<CaretDown size={10} aria-hidden="true" /></button>
      </div>
    </header>

    <div className="ask-result-kpi-row">
      <div>
        <strong>{result.summary.formattedValue}</strong>
        <small>{timeSpec ? timeSpecSummaryLabel(timeSpec) : '当前已发布数据快照'}</small>
        <div className="ask-result-time-actions">
          {timeSpec?.truncatedHint && <span>{timeSpec.truncatedHint}</span>}
          {timeSpec && <button type="button" aria-expanded={timeSpecOpen} aria-controls={timeSpecID} onClick={() => setTimeSpecOpen(value => !value)}>
            {timeSpecOpen ? '收起时间口径' : '查看时间口径'}<CaretDown className={timeSpecOpen ? 'is-open' : ''} size={10} aria-hidden="true" />
          </button>}
        </div>
      </div>
      {comparison && <div><span>{comparison.label}</span><strong className={`is-${comparison.direction.toLowerCase()}`}>{comparison.formattedChange}<TrendUp size={15} weight="bold" aria-hidden="true" /></strong>{timeSpec?.comparisonLabel && <small>{timeSpec.comparisonLabel}</small>}</div>}
    </div>

    {timeSpecOpen && timeSpec && result.resolvedTimeSpec && <div id={timeSpecID} className="ask-result-time-spec">
      <dl>
        <div><dt>实际区间</dt><dd>{timeSpec.rangeLabel}</dd></div>
        <div><dt>数据截止</dt><dd>{timeSpec.asOfLabel}</dd></div>
        <div><dt>策略来源</dt><dd>{timePolicySourceLabel(result.resolvedTimeSpec)}</dd></div>
        {timeSpec.comparisonLabel && <div className="is-wide"><dt>对比口径</dt><dd>{timeSpec.comparisonLabel}</dd></div>}
        <div><dt>业务时区</dt><dd>{result.resolvedTimeSpec.timezone}</dd></div>
      </dl>
    </div>}

    {answer && <AnswerSummary answer={answer} onRetryNarrative={onRetryNarrative} />}

    <div className="ask-result-view-tabs" role="tablist" aria-label="结果视图">
      {views.filter(view => view.type !== 'KPI').map(view => <button
        type="button" role="tab" key={view.id} aria-selected={activeView.id === view.id}
        onClick={() => setActiveViewID(view.id)}
      >{view.label}</button>)}
    </div>

	{activeView.type === 'KPI_BUNDLE' && bundleDataset && <KPIBundle dataset={bundleDataset} view={activeView} />}

    {(showLine || showBar) && <div className={`ask-result-visual-grid ${showBar ? 'is-channel-focus' : ''}`.trim()}>
      {showLine && <section><header><strong>{resultDataset(result, lineView.datasetId)?.label}</strong><small>（元）</small></header><ResultChart result={result} view={lineView} /></section>}
      {showLine && barView && <section><header><strong>{resultDataset(result, barView.datasetId)?.label}</strong><small>（元｜占比）</small></header><ResultChart result={result} view={barView} compact /></section>}
      {showBar && <section><header><strong>{resultDataset(result, barView.datasetId)?.label}</strong><small>（元）</small></header><ResultChart result={result} view={barView} /></section>}
    </div>}

    {tableDataset && <ResultTable key={tableDataset.id} dataset={tableDataset} result={result} />}
    <p className="ask-result-freshness"><ShieldCheck size={12} aria-hidden="true" />{timeSpec ? `${timeSpec.asOfLabel}（${result.resolvedTimeSpec?.timezone}）` : '当前已发布数据快照 · 已通过受控校验'}</p>
  </section>
}
