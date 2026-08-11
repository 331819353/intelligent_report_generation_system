import { useEffect, useRef } from 'react'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { AriaComponent, GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { init, use as registerEChartsComponents } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import type { RuntimeQueryResult } from '../api/runtime'
import type { ReportAsset } from '../assets/model'

registerEChartsComponents([BarChart, LineChart, PieChart, GridComponent, LegendComponent, TooltipComponent, AriaComponent, CanvasRenderer])

function useReportChart(setOption: (width: number) => object, label: string) {
  const chartRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!chartRef.current) return undefined
    const element = chartRef.current
    const chart = init(element)
    const render = () => chart.setOption({ animation: false, aria: { enabled: true, description: label }, ...setOption(element.clientWidth) }, true)
    render()
    const observer = new ResizeObserver(() => { chart.resize(); render() })
    observer.observe(element)
    return () => { observer.disconnect(); chart.dispose() }
  }, [label, setOption])
  return chartRef
}

const months = ['2025-07', '2025-08', '2025-09', '2025-10', '2025-11', '2025-12', '2026-01', '2026-02', '2026-03', '2026-04', '2026-05', '2026-06', '2026-07']
const currentRevenue = [82, 94, 98, 84, 88, 101, 100, 53, 69, 98, 98, 126, 129]
const previousRevenue = [71, 82, 85, 72, 76, 89, 90, 49, 65, 80, 83, 93, 96]

export function RevenueTrendChart() {
  const ref = useReportChart(() => ({
    color: ['#2864dc', '#c9d3e3'],
    tooltip: { trigger: 'axis', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 11 } },
    legend: { top: 0, left: 112, itemWidth: 15, itemHeight: 7, textStyle: { color: '#667085', fontSize: 9 }, data: ['本期收入', '去年同期'] },
    grid: { left: 48, right: 18, top: 32, bottom: 24 },
    xAxis: { type: 'category', boundaryGap: false, data: months, axisLabel: { color: '#8c95a3', fontSize: 8 }, axisLine: { lineStyle: { color: '#dfe4ea' } }, axisTick: { show: false } },
    yAxis: { type: 'value', min: 0, max: 160, interval: 40, axisLabel: { color: '#8c95a3', fontSize: 8, formatter: (value: number) => `${value * 10000}` }, axisLine: { show: false }, axisTick: { show: false }, splitLine: { lineStyle: { color: '#edf0f4', type: 'dashed' } } },
    series: [
      { name: '本期收入', type: 'line', smooth: false, symbolSize: 5, data: currentRevenue, lineStyle: { width: 2, color: '#2864dc' }, itemStyle: { color: '#2864dc' } },
      { name: '去年同期', type: 'line', smooth: false, symbol: 'none', data: previousRevenue, lineStyle: { width: 1.5, color: '#c9d3e3', type: 'dashed' } },
    ],
  }), '2025年7月至2026年7月营业收入趋势，本期收入总体增长。')
  return <div ref={ref} className="report-revenue-chart" role="img" aria-label="营业收入趋势图" />
}

export function ChannelContributionChart({ compact = false }: { compact?: boolean } = {}) {
  const pieRef = useReportChart(() => ({
    color: ['#2864dc', '#5b8bea', '#84c9bd', '#b7c6da'],
    tooltip: { trigger: 'item', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
    series: [{ type: 'pie', radius: ['43%', '69%'], center: ['50%', '52%'], label: { show: false }, data: [{ value: 50.4, name: '线上渠道' }, { value: 33.6, name: '线下渠道' }, { value: 12.1, name: '工程渠道' }, { value: 4, name: '其他渠道' }] }],
  }), '渠道收入贡献，线上渠道占比最高。')
  const barRef = useReportChart(() => ({
    color: ['#2864dc'],
    tooltip: { trigger: 'axis', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
    grid: { left: 67, right: 33, top: 5, bottom: 5 },
    xAxis: { type: 'value', show: false, min: -3, max: 15 },
    yAxis: { type: 'category', inverse: true, data: ['线上渠道', '线下渠道', '工程渠道', '其他渠道'], axisLine: { show: false }, axisTick: { show: false }, axisLabel: { color: '#5f6877', fontSize: 8 } },
    series: [{ type: 'bar', barWidth: 8, data: [12.4, 3.2, -2.1, 6.6], itemStyle: { color: ({ value }: { value: number }) => value < 0 ? '#f3a30d' : '#2864dc', borderRadius: 4 }, label: { show: true, position: 'right', color: '#697386', fontSize: 8, formatter: ({ value }: { value: number }) => `${value > 0 ? '+' : ''}${value}%` } }],
  }), '渠道收入贡献与同比增长，线上渠道贡献最高。')
  return <div className="report-channel-chart" aria-label="渠道收入贡献与同比增长图">
    <section className="report-channel-panel">
      <strong>渠道收入贡献（万元）</strong>
      <div className="report-channel-pie-wrap"><div ref={pieRef} role="img" aria-label="渠道收入贡献占比图" /><ul><li><i className="is-online" />线上渠道 <b>632,420（50.4%）</b></li><li><i className="is-offline" />线下渠道 <b>421,850（33.6%）</b></li><li><i className="is-project" />工程渠道 <b>152,050（12.1%）</b></li><li><i className="is-other" />其他渠道 <b>50,000（4.0%）</b></li></ul></div>
    </section>
    {!compact && <section className="report-channel-panel">
      <strong>渠道收入同比增长</strong>
      <div ref={barRef} className="report-channel-bars" role="img" aria-label="渠道收入同比增长条形图" />
    </section>}
  </div>
}

const previewData: Record<ReportAsset['previewKind'], { line: number[]; bar: number[]; pie: number[] }> = {
  operations: { line: [22, 30, 26, 41, 38, 52, 49], bar: [32, 48, 37, 59], pie: [42, 31, 17, 10] },
  sales: { line: [18, 29, 35, 31, 47, 55, 63], bar: [64, 55, 47, 35], pie: [48, 28, 16, 8] },
  quality: { line: [54, 46, 42, 38, 31, 28, 23], bar: [53, 41, 30, 24], pie: [34, 29, 22, 15] },
  inventory: { line: [45, 39, 44, 35, 33, 28, 26], bar: [28, 36, 43, 51], pie: [26, 32, 24, 18] },
  channel: { line: [21, 35, 29, 43, 48, 44, 58], bar: [58, 47, 36, 23], pie: [39, 34, 18, 9] },
  cashflow: { line: [30, 38, 33, 46, 41, 56, 61], bar: [25, 39, 44, 57], pie: [45, 25, 20, 10] },
}

export function MiniReportPreview({ kind, label }: { kind: ReportAsset['previewKind']; label: string }) {
  const data = previewData[kind]
  const ref = useReportChart(() => ({
    color: ['#2864dc', '#79a2ee', '#8bcfc3', '#cbd7e7'],
    grid: [
      { left: 10, right: '52%', top: 10, bottom: '51%' },
      { left: '54%', right: 9, top: 10, bottom: '51%' },
      { left: '54%', right: 9, top: '57%', bottom: 9 },
    ],
    xAxis: [
      { type: 'category', data: data.line.map((_, index) => index), gridIndex: 0, show: false },
      { type: 'category', data: data.bar.map((_, index) => index), gridIndex: 1, show: false },
      { type: 'value', gridIndex: 2, show: false },
    ],
    yAxis: [
      { type: 'value', gridIndex: 0, show: false, splitLine: { show: true, lineStyle: { color: '#eef2f7' } } },
      { type: 'value', gridIndex: 1, show: false, splitLine: { show: true, lineStyle: { color: '#eef2f7' } } },
      { type: 'category', gridIndex: 2, data: ['A', 'B', 'C'], show: false },
    ],
    series: [
      { type: 'line', xAxisIndex: 0, yAxisIndex: 0, data: data.line, symbol: 'none', lineStyle: { width: 1.4 }, areaStyle: { opacity: .04 } },
      { type: 'bar', xAxisIndex: 1, yAxisIndex: 1, data: data.bar, barWidth: '46%', itemStyle: { borderRadius: [2, 2, 0, 0] } },
      { type: 'pie', radius: ['24%', '43%'], center: ['24%', '76%'], label: { show: false }, data: data.pie },
      { type: 'bar', xAxisIndex: 2, yAxisIndex: 2, data: [42, 68, 55], barWidth: 5, itemStyle: { color: '#91aee3', borderRadius: 3 } },
    ],
  }), `${label}缩略预览`)
  return <div className="report-mini-preview" ref={ref} role="img" aria-label={`${label}缩略预览`} />
}

function IncomeStructureChart() {
  const ref = useReportChart(() => ({
    color: ['#2563eb', '#5b8def', '#79a8f3', '#7ec8bb', '#b7c9e7'],
    tooltip: { trigger: 'item', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
    legend: { orient: 'vertical', right: 0, top: 'middle', itemWidth: 7, itemHeight: 7, itemGap: 11, textStyle: { color: '#667085', fontSize: 9 }, data: ['冰箱', '洗衣机', '空调', '热水器', '厨电'] },
    series: [{
      type: 'pie', radius: ['48%', '72%'], center: ['37%', '52%'], label: { show: false },
      data: [{ value: 33.6, name: '冰箱' }, { value: 24.8, name: '洗衣机' }, { value: 20.1, name: '空调' }, { value: 12.7, name: '热水器' }, { value: 8.8, name: '厨电' }],
    }],
  }), '收入结构：冰箱占33.6%，洗衣机占24.8%，空调占20.1%。')
  return <div ref={ref} className="report-workspace-donut" role="img" aria-label="收入结构图" />
}

export function WorkspaceRevenueTrendChart() {
  const ref = useReportChart(() => ({
    color: ['#3478e5', '#17a981'],
    tooltip: { trigger: 'axis', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
    legend: { top: 0, left: 76, itemWidth: 12, itemHeight: 7, textStyle: { color: '#667085', fontSize: 8 }, data: ['营业收入', '同比增速'] },
    grid: { left: 43, right: 34, top: 29, bottom: 23 },
    xAxis: { type: 'category', data: ['2026-02', '2026-03', '2026-04', '2026-05', '2026-06', '2026-07'], axisTick: { show: false }, axisLine: { lineStyle: { color: '#dfe4ea' } }, axisLabel: { color: '#7d8795', fontSize: 8 } },
    yAxis: [
      { type: 'value', min: 0, max: 240, interval: 60, axisTick: { show: false }, axisLine: { show: false }, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#8d96a4', fontSize: 8, formatter: (value: number) => `${value},000` } },
      { type: 'value', min: -30, max: 30, interval: 15, axisTick: { show: false }, axisLine: { show: false }, splitLine: { show: false }, axisLabel: { color: '#8d96a4', fontSize: 8, formatter: (value: number) => `${value}%` } },
    ],
    series: [
      { name: '营业收入', type: 'bar', barWidth: 14, data: [178, 161, 143, 181, 202, 187], itemStyle: { borderRadius: [2, 2, 0, 0] } },
      { name: '同比增速', type: 'line', yAxisIndex: 1, data: [3, 1, -9, 2, 11, 5], symbolSize: 4, lineStyle: { width: 1.6 } },
    ],
  }), '2026年2月至7月营业收入与同比增速趋势。')
  return <div ref={ref} className="report-workspace-revenue" role="img" aria-label="营业收入与同比增速趋势图" />
}

export function RuntimeRevenueTrendChart() {
  const ref = useReportChart(() => ({
    color: ['#2f70df', '#18a77d'],
    tooltip: { trigger: 'axis', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
    legend: { top: 0, left: 'center', itemWidth: 13, itemHeight: 7, textStyle: { color: '#667085', fontSize: 9 }, data: ['营业收入', '同比增速'] },
    grid: { left: 48, right: 42, top: 31, bottom: 26 },
    xAxis: { type: 'category', data: months, axisTick: { show: false }, axisLine: { lineStyle: { color: '#dfe4ea' } }, axisLabel: { color: '#7d8795', fontSize: 8 } },
    yAxis: [
      { type: 'value', min: 0, max: 240, interval: 60, axisTick: { show: false }, axisLine: { show: false }, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#8d96a4', fontSize: 8, formatter: (value: number) => `${value},000` } },
      { type: 'value', min: -30, max: 30, interval: 15, axisTick: { show: false }, axisLine: { show: false }, splitLine: { show: false }, axisLabel: { color: '#8d96a4', fontSize: 8, formatter: (value: number) => `${value}%` } },
    ],
    series: [
      { name: '营业收入', type: 'bar', barWidth: 15, data: [160, 145, 166, 146, 144, 181, 175, 96, 130, 166, 146, 171, 196], itemStyle: { borderRadius: [2, 2, 0, 0] } },
      { name: '同比增速', type: 'line', yAxisIndex: 1, data: [-2, -5, 1, -6, -6, 5, 2, -18, -5, 2, -2, 4, 9], symbolSize: 4, lineStyle: { width: 1.7 } },
    ],
  }), '2025年7月至2026年7月营业收入与同比增速趋势。')
  return <div ref={ref} className="report-runtime-revenue-chart" role="img" aria-label="营业收入与同比增速趋势图" />
}

export function RuntimeResultChart({ result, templateType, label }: { result: RuntimeQueryResult; templateType: string; label: string }) {
  const normalizedType = templateType.toLocaleLowerCase()
  const categoryIndex = result.columns.findIndex((_, index) => result.rows.some(row => typeof row[index] === 'string'))
  const xIndex = categoryIndex >= 0 ? categoryIndex : 0
  const numericIndexes = result.columns.map((_, index) => index).filter(index => index !== xIndex && result.rows.some(row => Number.isFinite(Number(row[index]))))
  const ref = useReportChart(() => {
    if (normalizedType.includes('pie') || normalizedType.includes('donut')) {
      const valueIndex = numericIndexes[0] ?? Math.min(1, result.columns.length - 1)
      return {
        color: ['#2864dc', '#5b8bea', '#84c9bd', '#f0ae2c', '#b7c6da'],
        tooltip: { trigger: 'item', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
        legend: { orient: 'vertical', right: 12, top: 'middle', itemWidth: 8, itemHeight: 8, textStyle: { color: '#667085', fontSize: 9 } },
        series: [{ type: 'pie', radius: ['38%', '68%'], center: ['36%', '52%'], label: { show: false }, data: result.rows.slice(0, 12).map(row => ({ name: String(row[xIndex] ?? '—'), value: Number(row[valueIndex] ?? 0) })) }],
      }
    }
    const line = normalizedType.includes('line') || normalizedType.includes('trend')
    const indexes = numericIndexes.slice(0, 5)
    return {
      color: ['#2864dc', '#14a27f', '#79a8f3', '#f0ae2c', '#98a2b3'],
      tooltip: { trigger: 'axis', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
      legend: { top: 0, left: 'center', itemWidth: 13, itemHeight: 7, textStyle: { color: '#667085', fontSize: 9 } },
      grid: { left: 48, right: 22, top: 34, bottom: 30 },
      xAxis: { type: 'category', data: result.rows.slice(0, 30).map(row => String(row[xIndex] ?? '—')), axisTick: { show: false }, axisLine: { lineStyle: { color: '#dfe4ea' } }, axisLabel: { color: '#7d8795', fontSize: 8 } },
      yAxis: { type: 'value', axisTick: { show: false }, axisLine: { show: false }, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#8d96a4', fontSize: 8 } },
      series: indexes.map(index => ({ name: result.columns[index], type: line ? 'line' : 'bar', smooth: false, symbolSize: 4, barMaxWidth: 22, data: result.rows.slice(0, 30).map(row => Number(row[index] ?? 0)), itemStyle: { borderRadius: line ? 0 : [2, 2, 0, 0] }, lineStyle: { width: 1.8 } })),
    }
  }, `${label}，按当前查看者权限执行的发布报告图表。`)
  return <div ref={ref} className="runtime-live-chart" role="img" aria-label={`${label}图表`} />
}

function RegionIncomeChart() {
  const ref = useReportChart(() => ({
    color: ['#2f6ee5', '#b8cdf5'],
    tooltip: { trigger: 'axis', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
    legend: { top: 0, right: 8, itemWidth: 12, itemHeight: 7, textStyle: { color: '#667085', fontSize: 8 }, data: ['本期', '上期'] },
    grid: { left: 38, right: 10, top: 28, bottom: 22 },
    xAxis: { type: 'category', data: ['华东', '华南', '华北', '西南', '华中', '其他'], axisTick: { show: false }, axisLine: { lineStyle: { color: '#dfe4ea' } }, axisLabel: { color: '#7d8795', fontSize: 8 } },
    yAxis: { type: 'value', max: 100, interval: 25, axisTick: { show: false }, axisLine: { show: false }, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#8d96a4', fontSize: 8 } },
    series: [
      { name: '本期', type: 'bar', barWidth: 10, data: [81, 61, 42, 40, 27, 19], itemStyle: { borderRadius: [2, 2, 0, 0] } },
      { name: '上期', type: 'bar', barWidth: 10, data: [72, 68, 47, 45, 32, 28], itemStyle: { borderRadius: [2, 2, 0, 0] } },
    ],
  }), '区域收入对比，华东区域本期收入最高。')
  return <div ref={ref} className="report-workspace-small-chart" role="img" aria-label="区域收入对比图" />
}

function InventoryTrendChart() {
  const ref = useReportChart(() => ({
    color: ['#2f6ee5'],
    tooltip: { trigger: 'axis', backgroundColor: '#fff', borderColor: '#dfe4ea', textStyle: { color: '#344054', fontSize: 10 } },
    grid: { left: 34, right: 12, top: 18, bottom: 22 },
    xAxis: { type: 'category', boundaryGap: false, data: ['02月', '03月', '04月', '05月', '06月', '07月'], axisTick: { show: false }, axisLine: { lineStyle: { color: '#dfe4ea' } }, axisLabel: { color: '#7d8795', fontSize: 8 } },
    yAxis: { type: 'value', min: 20, max: 60, interval: 10, axisTick: { show: false }, axisLine: { show: false }, splitLine: { lineStyle: { color: '#edf0f4' } }, axisLabel: { color: '#8d96a4', fontSize: 8 } },
    series: [{ type: 'line', data: [53, 50, 42, 41, 36, 31], symbolSize: 4, lineStyle: { width: 2 }, areaStyle: { opacity: .04 } }],
  }), '库存周转天数从53天持续下降至31天。')
  return <div ref={ref} className="report-workspace-small-chart" role="img" aria-label="库存周转趋势图" />
}

export function ReportWorkspacePreview({ asset }: { asset: ReportAsset }) {
  return <article className="report-workspace-paper" aria-label={`${asset.name}报告缩略预览`}>
    <header><div><strong>核心摘要</strong><span>2026年07月（较上月）</span></div><small>资产缩略预览 · 打开报告查看实时数据</small></header>
    <div className="report-workspace-kpis">
      <section><span>营业收入（万元）</span><strong>186,560</strong><em className="is-positive">+8.72% ↑</em></section>
      <section><span>毛利率</span><strong>13.47<small>%</small></strong><em className="is-negative">-0.82pp ↓</em></section>
      <section><span>订单满足率</span><strong>96.32<small>%</small></strong><em className="is-positive">+1.21pp ↑</em></section>
      <section><span>库存周转天数（天）</span><strong>32.5</strong><em className="is-positive">-3.6 ↓</em></section>
    </div>
    <div className="report-workspace-chart-grid is-primary">
      <section><h3>营业收入趋势（万元）</h3><WorkspaceRevenueTrendChart /></section>
      <section><h3>收入结构（按产品线）</h3><IncomeStructureChart /></section>
    </div>
    <div className="report-workspace-chart-grid">
      <section><h3>区域收入对比（万元）</h3><RegionIncomeChart /></section>
      <section><h3>库存周转天数趋势（天）</h3><InventoryTrendChart /></section>
    </div>
  </article>
}
