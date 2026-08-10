import { useEffect, useRef } from 'react'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { AriaComponent, GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { init, use as registerEChartsComponents } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
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

export function ChannelContributionChart() {
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
    <section className="report-channel-panel">
      <strong>渠道收入同比增长</strong>
      <div ref={barRef} className="report-channel-bars" role="img" aria-label="渠道收入同比增长条形图" />
    </section>
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
