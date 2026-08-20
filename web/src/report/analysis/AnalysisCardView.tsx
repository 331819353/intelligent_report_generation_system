import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import {
  BarChart, BoxplotChart, FunnelChart, GaugeChart, GraphChart, HeatmapChart, LineChart,
  PieChart, RadarChart, SankeyChart, ScatterChart, TreeChart,
} from 'echarts/charts'
import {
  AriaComponent, GraphicComponent, GridComponent, LegendComponent, MarkAreaComponent, MarkLineComponent,
  MarkPointComponent, PolarComponent, RadarComponent, TooltipComponent, VisualMapComponent,
} from 'echarts/components'
import { init, use as registerEChartsComponents } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import {
  ArrowDownRight, ArrowRight, ArrowUpRight, Check, CheckCircle, Clock, Database,
  ChartDonut, FunnelSimple, Info, Lightbulb, ListChecks, MapTrifold, Network, ShieldCheck,
  Target, TrendDown, TrendUp, WarningCircle,
} from '@phosphor-icons/react'
import { analysisCardDefinition, type AnalysisCardCatalogItem, type AnalysisCardVariant } from './catalog.ts'
import { analysisCardVisualContract, type AnalysisCardSizeMode } from './visual-contracts.ts'
import { formatNumber, type QueryResult } from '../render/chart-option.ts'
import type { ReportComponent } from '../render/schema.ts'

registerEChartsComponents([
  BarChart, BoxplotChart, FunnelChart, GaugeChart, GraphChart, HeatmapChart, LineChart,
  PieChart, RadarChart, SankeyChart, ScatterChart, TreeChart,
  GraphicComponent, GridComponent, LegendComponent, MarkAreaComponent, MarkLineComponent, MarkPointComponent,
  PolarComponent, RadarComponent, TooltipComponent, VisualMapComponent,
  AriaComponent, CanvasRenderer,
])

type DataShape = {
  dimensionNames: string[]
  dimensionIndexes: number[]
  measureNames: string[]
  measureIndexes: number[]
  rows: unknown[][]
  labels: string[]
  values: number[][]
}

type ChartOption = Record<string, unknown>

const blues = ['#0B5CFF', '#4A8DF0', '#79ACEB', '#A9CCF4', '#D7E8FA', '#153B70']
const muted = '#75879B'

function numeric(value: unknown) {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function dataShape(component: ReportComponent, result: QueryResult): DataShape {
  const dimensions = component.dataBinding?.dimensions ?? []
  const measures = component.dataBinding?.measures ?? []
  const index = new Map(result.columns.map((column, position) => [column, position]))
  const dimensionIndexes = dimensions.map((binding, position) => index.get(binding.field) ?? position)
  const measureIndexes = measures.map((binding, position) => index.get(binding.field) ?? dimensions.length + position)
  return {
    dimensionIndexes,
    measureIndexes,
    dimensionNames: dimensionIndexes.map((position, order) => result.columns[position] ?? dimensions[order]?.field ?? `维度 ${order + 1}`),
    measureNames: measureIndexes.map((position, order) => result.columns[position] ?? measures[order]?.field ?? `指标 ${order + 1}`),
    rows: result.rows,
    labels: result.rows.map((row, rowIndex) => String(row[dimensionIndexes[0] ?? -1] ?? `对象 ${rowIndex + 1}`)),
    values: measureIndexes.map(position => result.rows.map(row => numeric(row[position]))),
  }
}

function variantOf(component: ReportComponent): AnalysisCardVariant {
  return component.options.cardVariant === '02' || component.options.cardVariant === '03' ? component.options.cardVariant : '01'
}

function useSizeMode(ref: React.RefObject<HTMLDivElement | null>, mobile?: boolean) {
  const [mode, setMode] = useState<AnalysisCardSizeMode>(mobile ? 'narrow' : 'wide')
  useEffect(() => {
    const element = ref.current
    if (!element) return undefined
    const update = (width: number) => setMode(mobile || width < 390 ? 'narrow' : width < 620 ? 'compact' : 'wide')
    update(element.getBoundingClientRect().width)
    const observer = new ResizeObserver(() => update(element.getBoundingClientRect().width))
    observer.observe(element)
    return () => observer.disconnect()
  }, [mobile, ref])
  return mode
}

function baseOption(variant: AnalysisCardVariant, mode: AnalysisCardSizeMode): ChartOption {
  const narrow = mode === 'narrow'
  return {
    animation: false,
    color: variant === '03' ? ['#1459D9', '#4C8FF0', '#8AB8EF', '#C4DCF6'] : blues,
    textStyle: { fontFamily: 'Inter, "PingFang SC", "Microsoft YaHei", sans-serif', color: muted },
    tooltip: { trigger: 'axis', confine: true, borderWidth: 1, borderColor: '#D6E3F1', textStyle: { fontSize: 11 } },
    legend: { show: !narrow, bottom: 0, icon: 'circle', itemWidth: 7, itemHeight: 7, textStyle: { fontSize: 9, color: muted } },
    grid: { top: 14, right: narrow ? 8 : 18, bottom: narrow ? 24 : 34, left: narrow ? 30 : 45 },
  }
}

function categoryAxis(labels: string[], mode: AnalysisCardSizeMode, inverse = false) {
  return {
    type: 'category', data: labels, inverse,
    axisLine: { lineStyle: { color: '#D6E2EF' } }, axisTick: { show: false },
    axisLabel: { color: muted, fontSize: mode === 'narrow' ? 8 : 9, hideOverlap: true, interval: 'auto' },
  }
}

function valueAxis(mode: AnalysisCardSizeMode, show = true) {
  return {
    type: 'value', show,
    splitLine: { show, lineStyle: { color: '#E7EEF6', type: 'dashed' } },
    axisLabel: { color: '#8796A8', fontSize: mode === 'narrow' ? 8 : 9 },
  }
}

function valueAt(shape: DataShape, measure = 0, row = 0) {
  return shape.values[measure]?.[row] ?? 0
}

function formatValue(component: ReportComponent, value: unknown) {
  return formatNumber(value, component.options.numberFormat)
}

function pivot(shape: DataShape) {
  if (shape.dimensionIndexes.length < 2 || shape.measureIndexes.length === 0) return undefined
  const xValues = [...new Set(shape.rows.map(row => String(row[shape.dimensionIndexes[0]] ?? '—')))]
  const groups = [...new Set(shape.rows.map(row => String(row[shape.dimensionIndexes[1]] ?? '—')))]
  return {
    xValues,
    series: groups.map(name => ({
      name,
      data: xValues.map(x => {
        const row = shape.rows.find(candidate => String(candidate[shape.dimensionIndexes[0]] ?? '—') === x
          && String(candidate[shape.dimensionIndexes[1]] ?? '—') === name)
        return row ? numeric(row[shape.measureIndexes[0]]) : 0
      }),
    })),
  }
}

function sortedPairs(shape: DataShape, count = 10, ascending = false) {
  return shape.labels.map((label, index) => ({ label, value: valueAt(shape, 0, index), movement: valueAt(shape, 1, index) }))
    .sort((left, right) => ascending ? left.value - right.value : right.value - left.value).slice(0, count)
}

function scatterData(shape: DataShape) {
  return shape.rows.map((_, index) => ({
    name: shape.labels[index], value: [valueAt(shape, 0, index), valueAt(shape, 1, index) || valueAt(shape, 0, index), valueAt(shape, 3, index) || valueAt(shape, 2, index) || 8],
  }))
}

function heatmapData(shape: DataShape) {
  const p = pivot(shape)
  const x = p?.xValues ?? shape.labels.slice(0, 8)
  const y = p?.series.map(item => item.name) ?? [shape.measureNames[0] ?? '指标']
  const data = p
    ? p.series.flatMap((series, row) => series.data.map((value, column) => [column, row, value]))
    : (shape.values[0] ?? []).slice(0, 8).map((value, column) => [column, 0, value])
  return { x, y, data, max: Math.max(...data.map(item => numeric(item[2])), 1) }
}

function lines(shape: DataShape, variant: AnalysisCardVariant, count = 3) {
  return shape.values.slice(0, count).map((data, index) => ({
    type: 'line', name: shape.measureNames[index] || `指标 ${index + 1}`, data: data.slice(0, 24), smooth: true,
    symbol: variant === '02' ? 'none' : 'circle', symbolSize: index === 0 ? 5 : 4,
    lineStyle: { width: index === 0 ? 2.7 : 1.8, type: index > 0 && variant === '03' ? 'dashed' : 'solid' },
    areaStyle: variant === '02' && index === 0 ? { opacity: .14 } : undefined,
  }))
}

function paretoOption(shape: DataShape, variant: AnalysisCardVariant, mode: AnalysisCardSizeMode) {
  const pairs = sortedPairs(shape, mode === 'narrow' ? 8 : 14)
  const total = pairs.reduce((sum, pair) => sum + pair.value, 0) || 1
  let cumulative = 0
  return {
    ...baseOption(variant, mode), grid: { top: 12, right: 34, bottom: 28, left: 38 },
    xAxis: categoryAxis(pairs.map(pair => pair.label), mode),
    yAxis: [valueAxis(mode), { type: 'value', min: 0, max: 100, show: false }],
    series: [
      { type: 'bar', data: pairs.map(pair => pair.value), barMaxWidth: 18, itemStyle: { color: (params: { dataIndex: number }) => params.dataIndex < (variant === '03' ? 3 : 5) ? '#0B5CFF' : '#B7D0EA', borderRadius: [4, 4, 0, 0] } },
      { type: 'line', yAxisIndex: 1, smooth: true, symbolSize: 5, data: pairs.map(pair => Math.round((cumulative += pair.value) / total * 100)), lineStyle: { width: 2.2, color: '#153B70' } },
    ],
  }
}

function heatmapOption(shape: DataShape, variant: AnalysisCardVariant, mode: AnalysisCardSizeMode) {
  const heatmap = heatmapData(shape)
  return {
    ...baseOption(variant, mode), tooltip: { position: 'top' }, grid: { top: 8, right: 12, bottom: 30, left: mode === 'narrow' ? 38 : 56 },
    xAxis: categoryAxis(heatmap.x, mode), yAxis: categoryAxis(heatmap.y, mode),
    visualMap: { min: 0, max: heatmap.max, show: false, inRange: { color: ['#EDF5FF', '#A9CCF7', '#0B5CFF'] } },
    series: [{ type: 'heatmap', data: heatmap.data, label: { show: variant !== '01' && mode !== 'narrow', fontSize: 8, color: '#193653' }, itemStyle: { borderColor: '#fff', borderWidth: variant === '03' ? 3 : 2, borderRadius: 3 } }],
  }
}

function waterfallOption(shape: DataShape, variant: AnalysisCardVariant, mode: AnalysisCardSizeMode) {
  const values = shape.values[0] ?? []
  let running = 0
  const base: number[] = []
  const increase: Array<number | '-'> = []
  const decrease: Array<number | '-'> = []
  values.forEach((value, index) => {
    if (index === 0 || index === values.length - 1) { base.push(0); increase.push(value); decrease.push('-'); running = value }
    else if (value >= 0) { base.push(running); increase.push(value); decrease.push('-'); running += value }
    else { base.push(running + value); increase.push('-'); decrease.push(-value); running += value }
  })
  return {
    ...baseOption(variant, mode), xAxis: categoryAxis(shape.labels, mode), yAxis: valueAxis(mode),
    series: [
      { type: 'bar', stack: 'bridge', data: base, silent: true, itemStyle: { color: 'transparent' } },
      { type: 'bar', stack: 'bridge', data: increase, barMaxWidth: variant === '02' ? 20 : 28, itemStyle: { color: '#0B5CFF', borderRadius: [4, 4, 0, 0] }, label: { show: mode !== 'narrow', position: 'top', fontSize: 8 } },
      { type: 'bar', stack: 'bridge', data: decrease, barMaxWidth: variant === '02' ? 20 : 28, itemStyle: { color: '#9BBCE2', borderRadius: [4, 4, 0, 0] }, label: { show: mode !== 'narrow', position: 'top', fontSize: 8 } },
    ],
  }
}

function chartOption(item: AnalysisCardCatalogItem, shape: DataShape, variant: AnalysisCardVariant, mode: AnalysisCardSizeMode): ChartOption {
  const common = baseOption(variant, mode)
  const labels = shape.labels.slice(0, mode === 'narrow' ? 9 : 20)
  const values = (shape.values[0] ?? []).slice(0, labels.length)
  const p = pivot(shape)
  const standard = { ...common, xAxis: categoryAxis(labels, mode), yAxis: valueAxis(mode) }

  switch (item.id) {
    case 5: {
      const series = lines(shape, variant, variant === '03' ? 2 : 1)
      if (variant === '02' && series[0]) Object.assign(series[0], {
        markLine: { symbol: ['none', 'none'], lineStyle: { color: '#7B9EC4', type: 'dashed' }, data: [{ type: 'average' }] },
        markPoint: { data: [{ type: 'max', name: '最新' }] },
      })
      return { ...standard, series }
    }
    case 6: {
      const data = labels.slice(0, 3).map((name, index) => ({ name, value: values[index] ?? 0 }))
      if (variant === '02') return { ...common, grid: { top: 30, right: 14, bottom: 24, left: 14 }, xAxis: { type: 'value', show: false }, yAxis: { type: 'category', data: ['总量'], show: false }, series: data.map(item => ({ type: 'bar', stack: 'share', name: item.name, data: [item.value], barWidth: mode === 'narrow' ? 28 : 40 })) }
      return { ...common, tooltip: { trigger: 'item' }, legend: { show: false }, series: [{ type: 'pie', radius: variant === '01' ? ['42%', '70%'] : ['38%', '66%'], center: variant === '03' && mode !== 'narrow' ? ['36%', '50%'] : ['50%', '50%'], data, label: variant === '01' ? { show: true, formatter: '{b}\n{d}%', color: '#526B84', fontSize: 9 } : { show: false }, itemStyle: { borderColor: '#fff', borderWidth: 2 } }] }
    }
    case 7: {
      const source = p?.series ?? []
      const xValues = p?.xValues ?? labels
      const beforeAfter = variant === '03' && xValues.length > 1
      return {
        ...common,
        xAxis: categoryAxis(beforeAfter ? [xValues[0], xValues.at(-1) ?? xValues[0]] : xValues, mode),
        yAxis: valueAxis(mode),
        series: source.slice(0, 4).map(series => ({
          ...series,
          type: 'bar', stack: 'structure', barMaxWidth: beforeAfter ? 72 : 28,
          data: beforeAfter ? [series.data[0] ?? 0, series.data.at(-1) ?? 0] : series.data,
          label: { show: beforeAfter && mode !== 'narrow', position: 'inside', formatter: '{c}', color: '#fff', fontSize: 8 },
          itemStyle: { borderColor: '#fff', borderWidth: beforeAfter ? 1 : 0 },
        })),
      }
    }
    case 8: return paretoOption(shape, variant, mode)
    case 9: {
      if (variant === '01') return { ...standard, series: [{ type: 'bar', data: values, barWidth: '92%', itemStyle: { color: '#79ACEB', borderColor: '#fff', borderWidth: 1 } }] }
      if (variant === '02') {
        const groups = labels.slice(0, 3)
        const boxes = groups.map((_, group) => {
          const sample = (shape.values[group] ?? shape.values[0] ?? []).slice().sort((a, b) => a - b)
          const at = (ratio: number) => sample[Math.floor(Math.max(0, sample.length - 1) * ratio)] ?? 0
          return [at(0), at(.25), at(.5), at(.75), at(1)]
        })
        return { ...common, grid: { top: 12, right: 20, bottom: 24, left: 64 }, xAxis: valueAxis(mode), yAxis: categoryAxis(groups, mode), series: [{ type: 'boxplot', data: boxes, itemStyle: { color: '#DDEBFA', borderColor: '#0B5CFF', borderWidth: 1.5 } }] }
      }
      return { ...common, xAxis: valueAxis(mode), yAxis: valueAxis(mode, false), series: [{ type: 'line', smooth: .55, showSymbol: false, data: values.map((value, index) => [index, value]), lineStyle: { width: 3 }, areaStyle: { opacity: .18 } }] }
    }
    case 10: {
      const threshold = valueAt(shape, 1) || Math.max(...values) * .78
      if (variant === '02') return { ...standard, series: [{ type: 'scatter', data: values.map((value, index) => [index, value]), symbolSize: (data: number[]) => data[1] > threshold ? 15 : 7, itemStyle: { color: (params: { value: number[] }) => params.value[1] > threshold ? '#0B5CFF' : '#91B7DE' }, markLine: { symbol: ['none', 'none'], lineStyle: { type: 'dashed' }, data: [{ yAxis: threshold }] } }] }
      return { ...standard, series: [{ type: variant === '03' ? 'bar' : 'line', data: values, smooth: true, itemStyle: { color: '#6EA4DC', borderRadius: [4, 4, 0, 0] }, markArea: variant === '01' ? { silent: true, itemStyle: { color: 'rgba(98,156,220,.10)' }, data: [[{ yAxis: 0 }, { yAxis: threshold }]] } : undefined, markLine: { symbol: ['none', 'none'], lineStyle: { type: 'dashed' }, data: [{ yAxis: threshold }] }, markPoint: { data: [{ type: 'max', name: '异常峰值' }] } }] }
    }
    case 11: return { ...common, xAxis: valueAxis(mode), yAxis: valueAxis(mode), series: [{ type: 'scatter', data: scatterData(shape), symbolSize: variant === '02' ? (value: number[]) => Math.max(8, Math.min(30, Math.sqrt(Math.abs(value[2] ?? 10)) * 2)) : 8, itemStyle: { opacity: .68 } }, { type: 'line', data: values.map((_, index) => [valueAt(shape, 0, index), valueAt(shape, 1, index)]).sort((a, b) => a[0] - b[0]), smooth: variant === '03' ? .6 : true, showSymbol: false, lineStyle: { width: 2, type: variant === '02' ? 'dashed' : 'solid', color: '#153B70' } }] }
    case 12: {
      const points = scatterData(shape)
      const meanX = points.reduce((sum, point) => sum + numeric(point.value[0]), 0) / Math.max(points.length, 1)
      const meanY = points.reduce((sum, point) => sum + numeric(point.value[1]), 0) / Math.max(points.length, 1)
      return { ...common, xAxis: valueAxis(mode), yAxis: valueAxis(mode), series: [{ type: 'scatter', data: points, symbolSize: variant === '02' ? (value: number[]) => Math.max(9, Math.min(28, Math.sqrt(Math.abs(value[2] ?? 9)) * 2)) : 8, itemStyle: { opacity: .72 }, label: { show: variant !== '01' && mode !== 'narrow', formatter: '{b}', position: 'top', fontSize: 8 }, markArea: variant === '03' ? { silent: true, itemStyle: { color: 'rgba(11,92,255,.08)' }, data: [[{ xAxis: 'min', yAxis: meanY }, { xAxis: meanX, yAxis: 'max' }]] } : undefined, markLine: { symbol: ['none', 'none'], label: { show: false }, lineStyle: { color: '#7EB0E8', type: 'dashed' }, data: [{ xAxis: meanX }, { yAxis: meanY }] } }] }
    }
    case 13: return heatmapOption(shape, variant, mode)
    case 14: {
      const stageLabels = labels.slice(0, 4)
      return { ...common, legend: { show: false }, tooltip: { trigger: 'item' }, series: [{ type: 'funnel', top: 6, bottom: 6, left: variant === '01' ? '7%' : variant === '02' ? '16%' : '22%', width: variant === '01' ? '86%' : variant === '02' ? '68%' : '56%', minSize: '18%', maxSize: '100%', gap: variant === '02' ? 8 : 4, label: { show: true, position: 'inside', formatter: variant === '03' ? '{b}\n{c}' : '{b}  {c}', fontSize: mode === 'narrow' ? 8 : 10, color: '#fff' }, itemStyle: { borderColor: '#fff', borderWidth: variant === '02' ? 3 : 2, borderRadius: variant === '02' ? 7 : 3 }, data: stageLabels.map((name, index) => ({ name, value: values[index] ?? 0 })) }] }
    }
    case 15: {
      const links = shape.rows.slice(0, mode === 'narrow' ? 8 : 12).map(row => ({ source: String(row[shape.dimensionIndexes[0]] ?? '来源'), target: String(row[shape.dimensionIndexes[1]] ?? '去向'), value: numeric(row[shape.measureIndexes[0]]) }))
      const nodes = [...new Set(links.flatMap(link => [link.source, link.target]))].map((name, index) => ({ name, itemStyle: { color: blues[index % blues.length] } }))
      return { ...common, tooltip: { trigger: 'item' }, legend: { show: false }, series: [{ type: 'sankey', data: nodes, links, top: 10, right: 14, bottom: 10, left: 14, orient: variant === '03' && mode === 'narrow' ? 'vertical' : 'horizontal', nodeWidth: variant === '02' ? 16 : 11, nodeGap: variant === '03' ? 13 : 9, lineStyle: { color: 'gradient', opacity: variant === '02' ? .22 : .34, curveness: variant === '03' ? .42 : .55 }, label: { fontSize: mode === 'narrow' ? 8 : 9, color: '#3F5F80' }, itemStyle: { borderWidth: 0, borderRadius: 3 } }] }
    }
    case 16: {
      if (variant === '03') return { ...common, xAxis: categoryAxis(p?.xValues ?? labels, mode), yAxis: valueAxis(mode), series: (p?.series ?? lines(shape, variant, 5)).slice(0, 5).map(series => ({ ...series, type: 'line', smooth: true, symbol: 'none' })) }
      const cohort = heatmapData(shape)
      const x = cohort.x.slice(0, 7)
      const y = cohort.y.slice(0, 7)
      const cells = cohort.data.filter(cell => numeric(cell[0]) < x.length && numeric(cell[1]) < y.length && numeric(cell[0]) >= numeric(cell[1]))
      if (variant === '02') return {
        ...common, grid: { top: 8, right: 12, bottom: 30, left: mode === 'narrow' ? 38 : 56 },
        xAxis: categoryAxis(x, mode), yAxis: categoryAxis(y, mode),
        series: [{ type: 'scatter', data: cells, symbolSize: (value: number[]) => 6 + Math.sqrt(Math.max(0, numeric(value[2])) / cohort.max) * (mode === 'narrow' ? 18 : 27), itemStyle: { color: '#0B5CFF', opacity: .68 }, label: { show: mode !== 'narrow', formatter: (params: { value: number[] }) => `${numeric(params.value[2]).toFixed(0)}%`, fontSize: 7, color: '#264B70' } }],
      }
      return {
        ...common, tooltip: { position: 'top' }, grid: { top: 8, right: 12, bottom: 30, left: mode === 'narrow' ? 38 : 56 },
        xAxis: categoryAxis(x, mode), yAxis: categoryAxis(y, mode),
        visualMap: { min: 0, max: cohort.max, show: false, inRange: { color: ['#EDF5FF', '#A9CCF7', '#0B5CFF'] } },
        series: [{ type: 'heatmap', data: cells, label: { show: mode !== 'narrow', fontSize: 7, color: '#193653' }, itemStyle: { borderColor: '#fff', borderWidth: 2, borderRadius: 3 } }],
      }
    }
    case 18: {
      if (variant === '03') return { ...common, tooltip: { trigger: 'item' }, legend: { show: false }, series: [{ type: 'pie', radius: ['42%', '70%'], data: labels.slice(0, 3).map((name, index) => ({ name, value: Math.abs(values[index] ?? 0) })), label: { show: mode !== 'narrow', fontSize: 9 } }] }
      if (variant === '01') return { ...common, grid: { top: 38, right: 20, bottom: 24, left: 20 }, xAxis: { type: 'value', show: false }, yAxis: { type: 'category', data: ['贡献'], show: false }, series: labels.slice(0, 4).map((label, index) => ({ type: 'bar', stack: 'total', name: label, data: [Math.abs(values[index] ?? 0)], barWidth: 36 })) }
      return { ...common, grid: { top: 8, right: 34, bottom: 20, left: 68 }, xAxis: valueAxis(mode), yAxis: categoryAxis(labels, mode, true), series: [{ type: 'bar', data: values, barMaxWidth: 18, itemStyle: { color: (params: { value: number }) => params.value >= 0 ? '#0B5CFF' : '#9BBCE2', borderRadius: 4 }, label: { show: mode !== 'narrow', position: 'right', fontSize: 8 } }] }
    }
    case 19: return waterfallOption(shape, variant, mode)
    case 20:
    case 24: {
      if (item.id === 24 && variant === '02') return heatmapOption(shape, variant, mode)
      if (item.id === 24) {
        const raw = p?.series?.length
          ? p.series.slice(0, mode === 'narrow' ? 4 : 6).map(series => ({ label: series.name, value: series.data.reduce((sum, value) => sum + value, 0) / Math.max(series.data.length, 1) }))
          : shape.labels.slice(0, mode === 'narrow' ? 6 : 9).map((label, index) => ({ label, value: valueAt(shape, 0, index) }))
        if (variant === '03') return {
          ...common, grid: { top: 8, right: 34, bottom: 24, left: 76 },
          xAxis: valueAxis(mode), yAxis: categoryAxis(raw.map(pair => pair.label), mode),
          series: [
            { type: 'bar', name: '下界', data: raw.map(pair => -Math.abs(pair.value) * .62), barMaxWidth: 15, barGap: '-100%', itemStyle: { color: '#B9D4EF', borderRadius: [5, 0, 0, 5] } },
            { type: 'bar', name: '上界', data: raw.map(pair => Math.abs(pair.value)), barMaxWidth: 15, itemStyle: { color: '#0B5CFF', borderRadius: [0, 5, 5, 0] } },
          ],
        }
        return { ...common, grid: { top: 8, right: 34, bottom: 24, left: 76 }, xAxis: valueAxis(mode), yAxis: categoryAxis(raw.map(pair => pair.label), mode), series: [{ type: 'bar', data: raw.map(pair => pair.value), barMaxWidth: 18, itemStyle: { color: (params: { value: number }) => params.value >= 0 ? '#0B5CFF' : '#9BBCE2', borderRadius: 4 }, label: { show: mode !== 'narrow', position: 'right', fontSize: 8 } }] }
      }
      const pairs = sortedPairs(shape, mode === 'narrow' ? 7 : 11)
      if (item.id === 20 && variant === '02') return { ...common, grid: { top: 8, right: 16, bottom: 24, left: 78 }, xAxis: valueAxis(mode), yAxis: categoryAxis(pairs.map(pair => pair.label), mode), series: [{ type: 'scatter', data: pairs.flatMap((pair, row) => Array.from({ length: 5 }, (_, index) => [pair.value * (.55 + index * .2), row])), symbolSize: 7, itemStyle: { opacity: .58 } }] }
      if (variant === '03') return { ...common, grid: { top: 8, right: 34, bottom: 24, left: 76 }, xAxis: valueAxis(mode), yAxis: categoryAxis(pairs.map(pair => pair.label), mode), series: [{ type: 'boxplot', data: pairs.map(pair => { const spread = Math.max(Math.abs(pair.value) * .28, 4); return [pair.value - spread, pair.value - spread * .4, pair.value, pair.value + spread * .4, pair.value + spread] }), itemStyle: { color: '#DDEBFA', borderColor: '#0B5CFF', borderWidth: 1.5 } }] }
      return { ...common, grid: { top: 8, right: 34, bottom: 24, left: 76 }, xAxis: valueAxis(mode), yAxis: categoryAxis(pairs.map(pair => pair.label), mode), series: [{ type: 'bar', data: pairs.map(pair => pair.value), barMaxWidth: 16, itemStyle: { color: '#0B5CFF', borderRadius: 4 }, label: { show: mode !== 'narrow', position: 'right', fontSize: 8 } }] }
    }
    case 21: {
      if (variant === '02') {
        const nodes = labels.slice(0, 18).map((name, index) => ({ name, symbolSize: index === 0 ? 34 : 13 }))
        return { ...common, legend: { show: false }, series: [{ type: 'graph', layout: 'circular', data: nodes, links: nodes.slice(1).map(node => ({ source: nodes[0]?.name, target: node.name })), label: { show: true, fontSize: 8 }, lineStyle: { color: '#9ABCE0', curveness: .14 }, itemStyle: { color: '#0B5CFF' } }] }
      }
      const branches = labels.slice(1, 4).map((name, branch) => ({ name, children: labels.slice(4 + branch * 2, 6 + branch * 2).map(child => ({ name: child })) }))
      const tree = [{ name: labels[0] ?? '问题', children: branches }]
      return { ...common, legend: { show: false }, series: [{ type: 'tree', data: tree, top: 8, left: 20, bottom: 8, right: mode === 'narrow' ? 50 : 92, orient: 'LR', symbol: variant === '03' ? 'roundRect' : 'circle', symbolSize: variant === '03' ? [44, 18] : 10, label: { fontSize: 8 }, lineStyle: { color: '#91B9E7' }, itemStyle: { color: '#0B5CFF' } }] }
    }
    case 22: return { ...standard, series: lines(shape, variant, variant === '03' ? 3 : 2).map((series, index) => ({ ...series, lineStyle: { width: index === 0 ? 2.5 : 2, type: index === 0 ? 'solid' : 'dashed' }, areaStyle: index === 1 ? { opacity: variant === '02' ? .18 : .1 } : undefined })) }
    case 23: return { ...common, xAxis: categoryAxis(p?.xValues ?? labels, mode), yAxis: valueAxis(mode), series: (p?.series ?? lines(shape, variant, 3)).slice(0, 3).map((series, index) => ({ ...series, type: 'line', smooth: true, symbol: 'none', lineStyle: { width: index === 1 ? 3 : 1.8, type: index === 1 ? 'solid' : 'dashed' } })) }
    case 25: {
      if (variant === '02') return { ...common, xAxis: categoryAxis(['1', '2', '3', '4', '5'], mode), yAxis: categoryAxis(['1', '2', '3', '4', '5'], mode), visualMap: { min: 1, max: 25, show: false, inRange: { color: ['#E7F1FB', '#8DB7E2', '#0B5CFF'] } }, series: [{ type: 'heatmap', data: values.slice(0, 25).map((_, index) => [index % 5, Math.floor(index / 5), Math.max(1, valueAt(shape, 0, index) * valueAt(shape, 1, index))]), label: { show: true, fontSize: 8 }, itemStyle: { borderColor: '#fff', borderWidth: 3, borderRadius: 5 } }] }
      if (variant === '03') { const max = Math.max(...values, 1); return { ...common, radar: { radius: mode === 'narrow' ? '58%' : '72%', indicator: labels.slice(0, 8).map(name => ({ name, max })), splitNumber: 4, axisName: { fontSize: 8, color: muted } }, series: [{ type: 'radar', data: [{ value: values.slice(0, 8) }], areaStyle: { opacity: .26 }, symbolSize: 5 }] } }
      return { ...common, xAxis: valueAxis(mode), yAxis: valueAxis(mode), series: [{ type: 'scatter', data: scatterData(shape), symbolSize: (value: number[]) => Math.max(9, Math.min(34, Math.sqrt(Math.abs(value[2] ?? 8)) * 2.6)), itemStyle: { opacity: .7 }, label: { show: mode !== 'narrow', formatter: '{b}', position: 'top', fontSize: 8 } }] }
    }
    case 26: {
      if (variant === '03') return { ...common, xAxis: valueAxis(mode), yAxis: valueAxis(mode, false), series: shape.values.slice(0, 2).map((data, index) => ({ type: 'line', name: shape.measureNames[index], data: data.map((value, position) => [position, value]), smooth: .6, showSymbol: false, lineStyle: { width: 2.5 }, areaStyle: { opacity: .12 } })) }
      const groups = labels.slice(0, 2)
      const boxes = groups.map((_, index) => { const center = valueAt(shape, 0, index); const spread = Math.abs(valueAt(shape, 1, index)) || Math.abs(center * .08); return [center - spread * 1.4, center - spread, center, center + spread, center + spread * 1.4] })
      return variant === '01' ? { ...common, xAxis: categoryAxis(groups, mode), yAxis: valueAxis(mode), series: [{ type: 'boxplot', data: boxes, itemStyle: { color: '#DDEBFA', borderColor: '#0B5CFF', borderWidth: 2 } }] } : { ...common, grid: { top: 12, right: 22, bottom: 24, left: 60 }, xAxis: valueAxis(mode), yAxis: categoryAxis(groups, mode), series: [{ type: 'boxplot', data: boxes, itemStyle: { color: '#DDEBFA', borderColor: '#0B5CFF', borderWidth: 2 } }] }
    }
    case 27: {
      const max = Math.max(...values.map(Math.abs), 1)
      const points = labels.map((name, index) => ({ name, value: [8 + ((index * 31) % 84), 12 + ((index * 47) % 76), values[index] ?? 0] }))
      if (variant === '03') { const nodes = points.slice(0, 9).map(point => ({ name: point.name, x: numeric(point.value[0]), y: numeric(point.value[1]), value: point.value[2], symbolSize: 11 })); return { ...common, legend: { show: false }, series: [{ type: 'graph', layout: 'none', data: nodes, links: nodes.slice(1).map((node, index) => ({ source: nodes[index % Math.max(1, Math.floor(nodes.length / 2))]?.name, target: node.name })), label: { show: mode !== 'narrow', fontSize: 8 }, lineStyle: { color: '#6CA5DD', curveness: .22 }, edgeSymbol: ['none', 'arrow'], edgeSymbolSize: 5, itemStyle: { color: '#0B5CFF' } }] } }
      return { ...common, grid: { top: 6, right: 6, bottom: 6, left: 6 }, xAxis: { type: 'value', show: false, min: 0, max: 100 }, yAxis: { type: 'value', show: false, min: 0, max: 100 }, visualMap: { show: false, min: 0, max, inRange: { color: ['#C7E0FB', '#0B5CFF'] } }, series: [{ type: 'scatter', data: points, symbol: variant === '01' ? 'roundRect' : 'circle', symbolSize: (value: number[]) => variant === '01' ? [34, 20] : 8 + Math.abs(value[2] ?? 0) / max * 20, label: { show: true, formatter: '{b}', position: variant === '01' ? 'inside' : 'right', color: variant === '01' ? '#fff' : '#193653', fontSize: 8 }, itemStyle: { borderColor: '#fff', borderWidth: 2, opacity: .82 } }] }
    }
    case 28: {
      const current = values.at(-1) ?? 0
      const max = Math.abs(valueAt(shape, 1, shape.rows.length - 1)) || Math.max(current * 1.2, 100)
      const gauge = { type: 'gauge', startAngle: variant === '01' ? 90 : 210, endAngle: variant === '01' ? -270 : -30, min: 0, max, radius: variant === '01' ? '82%' : '65%', center: variant === '01' ? ['44%', '50%'] : ['50%', '39%'], progress: { show: true, roundCap: true, width: variant === '01' ? 10 : 11, itemStyle: { color: '#0B5CFF' } }, axisLine: { lineStyle: { width: variant === '01' ? 10 : 11, color: [[1, '#DDEAF7']] } }, axisTick: { show: false }, splitLine: { show: false }, axisLabel: { show: false }, pointer: { show: variant !== '01', width: 3, length: '52%' }, anchor: { show: variant !== '01', size: 7 }, title: { offsetCenter: [0, '43%'], fontSize: 8, color: muted }, detail: { offsetCenter: [0, variant === '01' ? '2%' : '12%'], fontSize: mode === 'narrow' ? 18 : 24, fontWeight: 700, color: '#0B5CFF', formatter: (value: number) => formatNumber(value, undefined) }, data: [{ value: current, name: shape.measureNames[0] || '当前状态' }] }
      if (variant === '01') return { ...common, legend: { show: false }, series: [gauge] }
      return {
        ...common, legend: { show: false }, grid: { top: '72%', right: 18, bottom: 2, left: 18 },
        xAxis: { type: 'category', data: labels, show: false }, yAxis: { type: 'value', show: false },
        series: [gauge, { type: 'line', data: values, smooth: true, symbol: 'none', lineStyle: { width: 2, color: variant === '02' ? '#79ACEB' : '#153B70' }, areaStyle: { opacity: .1, color: '#79ACEB' } }],
      }
    }
    case 29: {
      if (variant === '01') return { ...common, xAxis: categoryAxis(p?.xValues ?? labels, mode), yAxis: valueAxis(mode), series: (p?.series ?? lines(shape, variant, 4)).slice(0, 4).map(series => ({ ...series, type: 'bar', stack: 'aging', barMaxWidth: 26 })) }
      const stageLabels = (p?.xValues ?? labels).slice(0, variant === '02' ? 4 : 5)
      const stageValues = p?.series?.length
        ? stageLabels.map((_, stage) => p.series.reduce((sum, series) => sum + (series.data[stage] ?? 0), 0))
        : values.slice(0, stageLabels.length)
      return { ...common, xAxis: categoryAxis(stageLabels, mode), yAxis: valueAxis(mode, false), series: [{ type: variant === '02' ? 'bar' : 'scatter', data: variant === '02' ? stageValues : stageValues.flatMap((value, stage) => Array.from({ length: Math.max(1, Math.min(8, Math.round(Math.abs(value) / Math.max(...stageValues.map(Math.abs), 1) * 8))) }, (_, dot) => [stage, dot + 1])), symbolSize: variant === '03' ? 12 : undefined, barMaxWidth: 46, itemStyle: { borderRadius: variant === '02' ? 8 : undefined, opacity: .82 }, label: { show: variant === '02' && mode !== 'narrow', position: 'top', fontSize: 9 } }] }
    }
    case 30: {
      if (variant !== '03') return heatmapOption(shape, variant, mode)
      const source = (shape.values[0] ?? []).length ? shape.values[0] : [1]
      const max = Math.max(...source.map(Math.abs), 1)
      const rings = Array.from({ length: 4 }, (_, ring) => ({
        type: 'pie', silent: true, clockwise: true, startAngle: 90,
        radius: [`${16 + ring * 15}%`, `${29 + ring * 15}%`], center: ['50%', '50%'],
        label: { show: false }, labelLine: { show: false },
        data: Array.from({ length: 12 }, (_, sector) => {
          const value = Math.abs(source[(ring * 12 + sector) % source.length] ?? 0)
          const opacity = .12 + value / max * .82
          return { name: labels[sector % Math.max(labels.length, 1)] ?? `${sector + 1}`, value: 1, itemStyle: { color: `rgba(11,92,255,${opacity.toFixed(3)})`, borderColor: '#fff', borderWidth: 1 } }
        }),
      }))
      return { ...common, tooltip: { show: false }, legend: { show: false }, series: rings }
    }
    default: return { ...standard, series: lines(shape, variant) }
  }
}

function AnalysisECharts({ option, label, onPick, className = '' }: {
  option: ChartOption
  label: string
  onPick?: (category: string) => void
  className?: string
}) {
  const container = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const element = container.current
    if (!element) return undefined
    let chart: ReturnType<typeof init> | undefined
    let observer: ResizeObserver | undefined
    let frame = 0
    let cancelled = false
    const mount = () => {
      if (cancelled) return
      if (element.clientWidth <= 0 || element.clientHeight <= 0) {
        frame = requestAnimationFrame(mount)
        return
      }
      chart = init(element)
      chart.setOption(option, true)
      if (onPick) chart.on('click', (params: { name?: string }) => { if (params.name) onPick(params.name) })
      observer = new ResizeObserver(() => chart?.resize())
      observer.observe(element)
    }
    frame = requestAnimationFrame(mount)
    return () => {
      cancelled = true
      cancelAnimationFrame(frame)
      observer?.disconnect()
      chart?.dispose()
    }
  }, [onPick, option])
  return <div className={`report-analysis-chart ${className}`.trim()} ref={container} role="img" aria-label={label} />
}

function StatStrip({ component, shape, limit = 3 }: { component: ReportComponent; shape: DataShape; limit?: number }) {
  const entries = shape.measureNames.slice(0, limit).map((name, index) => ({ name, value: valueAt(shape, index) }))
  for (const pair of sortedPairs(shape, limit)) {
    if (entries.length >= limit) break
    entries.push({ name: pair.label, value: pair.value })
  }
  return <div className="report-analysis-stat-strip">{entries.map((entry, index) => <span key={`${entry.name}-${index}`}>
    <small>{entry.name}</small><strong>{formatValue(component, entry.value)}</strong>
  </span>)}</div>
}

function BreakdownStrip({ component, shape, limit = 3 }: { component: ReportComponent; shape: DataShape; limit?: number }) {
  return <div className="report-analysis-stat-strip is-breakdown">{shape.labels.slice(0, limit).map((label, index) => <span key={`${label}-${index}`}>
    <small><i style={{ background: blues[index % blues.length] }} />{label}</small><strong>{formatValue(component, valueAt(shape, 0, index))}</strong>
  </span>)}</div>
}

function MetricPill({ label, value, positive }: { label: string; value: string; positive: boolean }) {
  return <span className={`report-analysis-pill ${positive ? '' : 'is-negative'}`.trim()}>
    {positive ? <ArrowUpRight size={13} /> : <ArrowDownRight size={13} />}
    <small>{label}</small><strong>{value}</strong>
  </span>
}

function MetricStatusCard({ component, shape, variant, mode }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant; mode: AnalysisCardSizeMode }) {
  const primary = valueAt(shape)
  if (variant === '01') return <div className="report-analysis-kpi-layout is-hero-deltas">
    <span className="report-analysis-eyebrow">{shape.measureNames[0] || '核心指标'}</span>
    <strong className="report-analysis-hero-value">{formatValue(component, primary)}</strong>
    <div className="report-analysis-pill-row">
      <MetricPill label={shape.measureNames[1] || '同比'} value={formatValue(component, valueAt(shape, 1))} positive={valueAt(shape, 1) >= 0} />
      <MetricPill label={shape.measureNames[2] || '环比'} value={formatValue(component, valueAt(shape, 2))} positive={valueAt(shape, 2) >= 0} />
    </div>
  </div>
  if (variant === '03') return <div className="report-analysis-kpi-layout is-orbit-score">
    <img className="report-analysis-kpi-orbit" src="/report-assets/kpi-orbit.png" alt="" aria-hidden="true" />
    <strong className="report-analysis-orbit-value">{formatValue(component, primary)}</strong>
    <div className="report-analysis-pill-row">
      <MetricPill label={shape.measureNames[1] || '同比'} value={formatValue(component, valueAt(shape, 1))} positive={valueAt(shape, 1) >= 0} />
      <MetricPill label={shape.measureNames[2] || '环比'} value={formatValue(component, valueAt(shape, 2))} positive={valueAt(shape, 2) >= 0} />
    </div>
  </div>
  const maximum = Math.max(Math.abs(primary), Math.abs(valueAt(shape, 1)), 100)
  const option: ChartOption = {
    ...baseOption(variant, mode), legend: { show: false },
    series: [{
      type: 'gauge', startAngle: 90, endAngle: -270, min: 0, max: maximum,
      radius: '82%', center: ['50%', '50%'],
      progress: { show: true, roundCap: true, width: 11, itemStyle: { color: '#0B5CFF' } },
      axisLine: { lineStyle: { width: 11, color: [[1, '#DDEAF7']] } },
      axisTick: { show: false }, splitLine: { show: false }, axisLabel: { show: false }, pointer: { show: false },
      detail: { offsetCenter: [0, '4%'], fontSize: mode === 'narrow' ? 20 : 30, fontWeight: 760, color: '#0B5CFF', formatter: () => formatValue(component, primary) },
      title: { offsetCenter: [0, '31%'], fontSize: 9, color: muted }, data: [{ value: primary, name: shape.measureNames[0] || '综合评分' }],
    }],
  }
  return <div className="report-analysis-kpi-layout is-comparison-ring">
    <AnalysisECharts option={option} label="指标状态环" className="is-kpi-ring" />
    <div className="report-analysis-kpi-sides">
      <span><small>{shape.measureNames[1] || '对比一'}</small><strong>{formatValue(component, valueAt(shape, 1))}</strong></span>
      <span><small>{shape.measureNames[2] || '对比二'}</small><strong>{formatValue(component, valueAt(shape, 2))}</strong></span>
    </div>
  </div>
}

function GoalProgressCard({ component, shape, variant, mode }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant; mode: AnalysisCardSizeMode }) {
  const actual = valueAt(shape)
  const target = Math.abs(valueAt(shape, 1)) || 1
  const reference = valueAt(shape, 2)
  const ratio = Math.max(0, Math.min(actual / target * 100, 100))
  if (variant === '03') {
    const option: ChartOption = {
      ...baseOption(variant, mode), legend: { show: false },
      series: [{ type: 'gauge', startAngle: 210, endAngle: -30, min: 0, max: target, radius: '92%', center: ['50%', '58%'], progress: { show: true, roundCap: true, width: 15, itemStyle: { color: '#0B5CFF' } }, axisLine: { lineStyle: { width: 15, color: [[1, '#DCE9F6']] } }, axisTick: { show: false }, splitLine: { show: false }, axisLabel: { show: false }, pointer: { show: mode !== 'narrow', width: 4, length: '55%' }, anchor: { show: mode !== 'narrow', size: 8 }, detail: { offsetCenter: [0, mode === 'narrow' ? '0%' : '14%'], fontSize: mode === 'narrow' ? 20 : 30, fontWeight: 760, color: '#0B5CFF', formatter: `${ratio.toFixed(1)}%` }, title: { offsetCenter: [0, mode === 'narrow' ? '39%' : '46%'], fontSize: 9, color: muted }, data: [{ value: actual, name: `目标 ${formatValue(component, target)}` }] }],
    }
    return <AnalysisECharts option={option} label="目标达成仪表盘" />
  }
  return <div className={`report-analysis-goal is-${variant === '01' ? 'bullet' : 'actual-target'}`}>
    <header><span><small>{shape.measureNames[0] || '实际值'}</small><strong>{formatValue(component, actual)}</strong></span><span><small>{shape.measureNames[1] || '目标值'}</small><strong>{formatValue(component, target)}</strong></span></header>
    <div className="report-analysis-bullet" style={{ '--analysis-progress': `${ratio}%`, '--analysis-reference': `${Math.max(0, Math.min(reference / target * 100, 100))}%` } as CSSProperties}><i /><b /><em /></div>
    <footer><strong>达成 {ratio.toFixed(1)}%</strong><span>{variant === '01' ? `同期 ${formatValue(component, reference)}` : `剩余 ${formatValue(component, Math.max(target - actual, 0))}`}</span></footer>
  </div>
}

function ComparisonCard({ component, shape, variant, mode }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant; mode: AnalysisCardSizeMode }) {
  const current = valueAt(shape)
  const baseline = valueAt(shape, 1) || valueAt(shape, 0, 1)
  const delta = valueAt(shape, 2) || current - baseline
  if (variant === '01') return <div className="report-analysis-versus">
    <article><small>{shape.labels[0] || '当前'}</small><strong>{formatValue(component, current)}</strong></article>
    <span><b>VS</b><i style={{ height: `${Math.max(18, Math.min(78, Math.abs(delta) / Math.max(Math.abs(current), Math.abs(baseline), 1) * 100))}%` }} /></span>
    <article><small>{shape.labels[1] || shape.measureNames[1] || '对比'}</small><strong>{formatValue(component, baseline)}</strong></article>
    <footer>差异 <strong>{delta >= 0 ? '+' : ''}{formatValue(component, delta)}</strong></footer>
  </div>
  const distance = Math.max(Math.abs(current - baseline), Math.abs(current) * .05, Math.abs(baseline) * .05, 1)
  const lower = Math.min(current, baseline)
  const upper = Math.max(current, baseline)
  const option: ChartOption = variant === '02'
    ? { ...baseOption(variant, mode), grid: { top: 14, right: 28, bottom: 26, left: 68 }, xAxis: valueAxis(mode), yAxis: categoryAxis([shape.labels[0] || '实际', shape.labels[1] || '预算'], mode), series: [{ type: 'bar', data: [current, baseline], barWidth: 22, itemStyle: { color: (params: { dataIndex: number }) => params.dataIndex === 0 ? '#0B5CFF' : '#A8C6E5', borderRadius: 6 }, label: { show: true, position: 'right', fontSize: 9 } }] }
    : { ...baseOption(variant, mode), grid: { top: 24, right: 32, bottom: 28, left: 32 }, xAxis: { type: 'value', min: lower - distance * .28, max: upper + distance * .28, show: false }, yAxis: { type: 'category', data: ['对比'], show: false }, series: [{ type: 'line', data: [[baseline, 0], [current, 0]], symbolSize: 18, lineStyle: { width: 5, color: '#9DC1E8' }, itemStyle: { color: '#0B5CFF', borderColor: '#fff', borderWidth: 3 }, label: { show: true, formatter: (params: { dataIndex: number }) => params.dataIndex === 0 ? `${shape.labels[1] || 'B'}\n${formatValue(component, baseline)}` : `${shape.labels[0] || 'A'}\n${formatValue(component, current)}`, position: 'top', fontSize: 9, color: '#193653' } }] }
  return <div className="report-analysis-comparison-chart"><AnalysisECharts option={option} label="对象差异对比" /><footer>差异 <strong>{delta >= 0 ? '+' : ''}{formatValue(component, delta)}</strong></footer></div>
}

function RankingCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const rows = sortedPairs(shape, variant === '01' ? 5 : 3, variant === '03')
  return <ol className={`report-analysis-ranking is-variant-${variant}`}>{rows.map((row, index) => <li key={`${row.label}-${index}`}>
    <b>{variant === '01' ? index + 1 : variant === '02' ? `TOP ${index + 1}` : `↓ ${index + 1}`}</b>
    <span><strong>{row.label}</strong><small>{shape.measureNames[0] || '排名指标'}</small></span>
    <em>{formatValue(component, row.value)}</em>
    <i>{row.movement >= 0 ? <ArrowUpRight size={12} /> : <ArrowDownRight size={12} />}{formatValue(component, Math.abs(row.movement))}</i>
  </li>)}</ol>
}

function LifecycleCard({ component, shape, variant, mode }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant; mode: AnalysisCardSizeMode }) {
  const labels = shape.labels.slice(0, 4)
  if (variant === '01') {
    const option: ChartOption = { ...baseOption(variant, mode), xAxis: categoryAxis(labels, mode), yAxis: valueAxis(mode, false), series: [{ type: 'line', data: (shape.values[0] ?? []).slice(0, 4), smooth: .62, symbolSize: 8, lineStyle: { width: 3 }, areaStyle: { opacity: .16 }, label: { show: mode !== 'narrow', position: 'top', fontSize: 9 } }] }
    return <div className="report-analysis-lifecycle-curve"><AnalysisECharts option={option} label="生命周期曲线" /><StatStrip component={component} shape={shape} limit={4} /></div>
  }
  if (variant === '02') {
    const option: ChartOption = {
      ...baseOption(variant, mode), tooltip: { trigger: 'item' }, legend: { show: false },
      series: [{
        type: 'pie', radius: ['48%', '76%'], center: ['50%', '50%'], startAngle: 90,
        data: labels.map((name, index) => ({ name, value: Math.abs(valueAt(shape, 0, index)) || 1 })),
        label: { show: true, formatter: '{b}\n{d}%', color: '#31506F', fontSize: mode === 'narrow' ? 7 : 9 },
        itemStyle: { borderColor: '#F8FBFF', borderWidth: 4, borderRadius: 5 },
      }],
      graphic: [{ type: 'text', left: 'center', top: '43%', style: { text: `生命周期\n${formatValue(component, (shape.values[0] ?? []).reduce((sum, value) => sum + value, 0))}`, textAlign: 'center', fill: '#1D4F83', font: `${mode === 'narrow' ? 10 : 13}px Inter`, lineHeight: mode === 'narrow' ? 15 : 19, fontWeight: 700 } }],
    }
    return <div className="report-analysis-lifecycle is-ring"><AnalysisECharts option={option} label="生命周期环" /></div>
  }
  const option: ChartOption = {
    ...baseOption(variant, mode), grid: { top: 25, right: 24, bottom: 34, left: 24 },
    xAxis: { type: 'category', data: labels, show: false }, yAxis: { type: 'value', show: false },
    series: [{
      type: 'line', data: (shape.values[0] ?? []).slice(0, 4), smooth: .62, symbolSize: 11,
      lineStyle: { width: 3, color: '#4A8DF0' }, areaStyle: { opacity: .11, color: '#79ACEB' },
      itemStyle: { color: '#0B5CFF', borderColor: '#fff', borderWidth: 3 },
      label: { show: true, formatter: (params: { dataIndex: number; value: number }) => `${labels[params.dataIndex] ?? ''}\n${formatValue(component, params.value)}`, position: 'bottom', fontSize: mode === 'narrow' ? 7 : 9, lineHeight: 14, color: '#31506F' },
    }],
  }
  return <div className="report-analysis-lifecycle is-arc"><AnalysisECharts option={option} label="生命周期阶段弧线" /></div>
}

function ScenarioCard({ component, shape, variant, option }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant; option: ChartOption }) {
  if (variant === '01') return <AnalysisECharts option={option} label="情景模拟曲线" />
  const rows = sortedPairs(shape, 3)
  if (variant === '02') return <div className="report-analysis-scenario-cards">{rows.map((row, index) => <article key={row.label}>
    <span>{index === 0 ? <ArrowUpRight size={17} /> : index === 1 ? <Target size={17} /> : <ArrowDownRight size={17} />}</span>
    <small>{row.label}</small><strong>{formatValue(component, row.value)}</strong><em>{row.movement >= 0 ? '+' : ''}{formatValue(component, row.movement)}</em>
  </article>)}</div>
  return <div className="report-analysis-scenario-tree"><div><Target size={22} /><strong>{shape.dimensionNames[0] || '关键假设'}</strong></div><ArrowRight size={22} /><section>{rows.map(row => <article key={row.label}><small>{row.label}</small><strong>{formatValue(component, row.value)}</strong></article>)}</section></div>
}

function DetailTableCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const columns = [...shape.dimensionIndexes, ...shape.measureIndexes]
  const pageSize = Math.max(3, Math.min(component.options.topN ?? (variant === '01' ? 6 : variant === '02' ? 5 : 4), 8))
  return <div className={`report-analysis-table is-variant-${variant}`}>
    {variant === '01' && <header><FunnelSimple size={14} />当前筛选 · {shape.rows.length} 条结果</header>}
    <div><table><thead><tr>{columns.map(index => <th key={index}>{shape.dimensionNames[shape.dimensionIndexes.indexOf(index)] ?? shape.measureNames[shape.measureIndexes.indexOf(index)]}</th>)}</tr></thead>
      <tbody>{shape.rows.slice(0, pageSize).map((row, rowIndex) => <tr key={rowIndex}>{columns.map(index => <td key={index}>{shape.measureIndexes.includes(index) ? formatValue(component, row[index]) : String(row[index] ?? '—')}</td>)}</tr>)}</tbody></table></div>
    <footer><span>共 {shape.rows.length} 条</span><strong>1 / {Math.max(1, Math.ceil(shape.rows.length / pageSize))}</strong></footer>
  </div>
}

function TimelineCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const limit = Math.min(shape.rows.length, variant === '03' ? 5 : 6)
  if (variant === '03') return <ol className="report-analysis-timeline is-vertical">{shape.rows.slice(0, limit).map((row, index) => <li key={index}>
    <span><CheckCircle size={18} weight={index === 0 ? 'fill' : 'regular'} /></span>
    <article><small>{String(row[shape.dimensionIndexes[1]] ?? '')}</small><strong>{String(row[shape.dimensionIndexes[0]] ?? shape.labels[index])}</strong><em>{formatValue(component, row[shape.measureIndexes[0]])}</em></article>
  </li>)}</ol>
  return <ol className={`report-analysis-timeline is-horizontal is-variant-${variant}`}>{shape.rows.slice(0, limit).map((row, index) => <li key={index}>
    <span>{variant === '01' ? index + 1 : index === limit - 1 ? <Check size={15} /> : <Clock size={15} />}</span>
    <strong>{String(row[shape.dimensionIndexes[0]] ?? shape.labels[index])}</strong><small>{String(row[shape.dimensionIndexes[1]] ?? '')}</small>
  </li>)}</ol>
}

function InsightCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const subject = shape.labels[0] || '核心对象'
  return <div className={`report-analysis-insight is-variant-${variant}`}>
    <header>{variant === '03' ? <MapTrifold size={23} /> : <Lightbulb size={23} weight="duotone" />}<span><small>核心发现</small><strong>{subject}是当前最值得关注的业务变化</strong></span></header>
    {variant === '01' && <div className="report-analysis-insight-bar"><i style={{ width: `${Math.max(12, Math.min(100, Math.abs(valueAt(shape)) % 101))}%` }} /><span>{formatValue(component, valueAt(shape))}</span></div>}
    <StatStrip component={component} shape={shape} limit={variant === '03' ? 3 : 2} />
  </div>
}

function ActionCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const rows = shape.rows.slice(0, 3)
  return <div className={`report-analysis-actions is-variant-${variant}`}>
    <header><ListChecks size={18} /><strong>{variant === '03' ? '行动完成进度' : '优先行动'}</strong></header>
    <section>{rows.map((row, index) => <article key={index}>
      <b>{variant === '01' ? `P${index + 1}` : index + 1}</b>
      <span><strong>{String(row[shape.dimensionIndexes[0]] ?? shape.labels[index])}</strong><small>{shape.dimensionIndexes.slice(1).map(position => String(row[position] ?? '')).filter(Boolean).join(' · ')}</small></span>
      {variant === '03' ? <progress value={Math.max(0, Math.min(100, numeric(row[shape.measureIndexes[0]])))} max={100} /> : <em>{formatValue(component, row[shape.measureIndexes[0]])}</em>}
    </article>)}</section>
  </div>
}

function DataInfoCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const label = shape.labels[0] || shape.measureNames[0] || '指标定义'
  const metrics = shape.measureNames.slice(0, 4)
  if (variant === '02') return <div className="report-analysis-data-type">
    <small>数据类型与统计口径</small><strong>{label}</strong><div>{metrics.map((name, index) => <span key={name}><small>{name}</small><b>{formatValue(component, valueAt(shape, index))}</b></span>)}</div>
  </div>
  return <div className={`report-analysis-info-orbit is-variant-${variant}`}>
    <div><Database size={22} /><strong>{label}</strong><small>{variant === '01' ? '指标口径' : '数据质量'}</small></div>
    <section>{metrics.map((name, index) => <article key={name}><span>{index % 2 === 0 ? <ShieldCheck size={16} /> : <CheckCircle size={16} />}</span><small>{name}</small><strong>{formatValue(component, valueAt(shape, index))}</strong></article>)}</section>
  </div>
}

function ScopeCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const entries = shape.dimensionNames.map((name, index) => ({ name, value: String(shape.rows[0]?.[shape.dimensionIndexes[index]] ?? '全部') })).slice(0, 4)
  if (variant === '03') return <div className="report-analysis-scope-radial">
    <div><FunnelSimple size={22} /><strong>{formatValue(component, valueAt(shape))}</strong><small>当前结果</small></div>
    <section>{entries.map(entry => <article key={entry.name}><small>{entry.name}</small><strong>{entry.value}</strong></article>)}</section>
  </div>
  return <div className={`report-analysis-scope is-variant-${variant}`}>
    <header><FunnelSimple size={18} /><strong>当前分析范围</strong></header>
    <section>{entries.map(entry => <label key={entry.name}><small>{entry.name}</small><span>{entry.value}</span></label>)}</section>
    <footer><button type="button">{variant === '01' ? '查看结果' : '应用筛选'}</button>{variant === '02' && <button type="button" className="is-secondary">重置</button>}<span>结果 {formatValue(component, valueAt(shape))}</span></footer>
  </div>
}

function conclusionParagraphs(component: ReportComponent, shape: DataShape) {
  const subject = shape.labels[0] || shape.dimensionNames[0] || '当前业务'
  const primary = shape.measureNames[0] || '核心指标'
  const secondary = shape.measureNames[1] || '结构指标'
  const provided = component.options.richText?.trim().split(/\n+/).map(value => value.trim()).filter(Boolean) ?? []
  const fallback = [
    `${subject}的${primary}构成当前结果的主要支撑。结合业务范围与数据表现看，增长基础仍然稳定，但后续改善需要同时关注规模、效率与结构质量。`,
    `${secondary}揭示了不同对象之间的表现分化。头部对象保持领先，中腰部仍有可提升空间，应优先识别偏离整体趋势的区域、产品或渠道。`,
    `建议围绕关键证据建立持续跟踪机制，把指标变化、执行动作与责任人放在同一复盘节奏中，并在下一周期验证改善是否真实发生。`,
  ]
  return [...provided, ...fallback.slice(provided.length)].slice(0, 3)
}

function LongFormConclusionCard({ component, shape, variant }: { component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  const title = component.options.title || `${shape.labels[0] || '业务'}综合结论`
  const paragraphs = conclusionParagraphs(component, shape)
  const metrics = Array.from({ length: 4 }, (_, index) => ({
    name: shape.measureNames[index] || `证据指标 ${index + 1}`,
    value: formatValue(component, valueAt(shape, index)),
    change: shape.values[index + 4]?.[0] ?? shape.values[index + 1]?.[1] ?? 0,
  }))
  if (variant === '01') return <div className="report-analysis-long-form is-kpi-over">
    <header><strong>{title}</strong><small>综合经营洞察</small></header>
    <section className="report-analysis-long-kpis">{metrics.map((metric, index) => <article key={`${metric.name}-${index}`}>
      <small>{metric.name}</small><strong>{metric.value}</strong><em className={metric.change < 0 ? 'is-negative' : ''}>{metric.change >= 0 ? <TrendUp size={12} /> : <TrendDown size={12} />}{metric.change >= 0 ? '+' : ''}{formatValue(component, metric.change)}</em>
    </article>)}</section>
    <section className="report-analysis-long-columns">{paragraphs.map((paragraph, index) => <p key={index}><b>{index + 1}</b>{paragraph}</p>)}</section>
    <footer><CheckCircle size={14} weight="fill" />已基于绑定数据生成结论证据链</footer>
  </div>
  if (variant === '02') return <div className="report-analysis-long-form is-narrative-rail">
    <main><header><small>经营分析结论</small><strong>{title}</strong></header><blockquote>{paragraphs[0]}</blockquote>{paragraphs.slice(1).map((paragraph, index) => <p key={index}>{paragraph}</p>)}</main>
    <aside><article className="is-primary"><small>{metrics[0].name}</small><strong>{metrics[0].value}</strong><em>{metrics[0].change >= 0 ? '+' : ''}{formatValue(component, metrics[0].change)}</em></article>{metrics.slice(1).map((metric, index) => <article key={`${metric.name}-${index}`}><small>{metric.name}</small><strong>{metric.value}</strong></article>)}</aside>
  </div>
  const icons = [<Network size={19} key="network" />, <ChartDonut size={19} key="structure" />, <WarningCircle size={19} key="risk" />]
  return <div className="report-analysis-long-form is-evidence-sections">
    <header><span><Lightbulb size={20} weight="duotone" /></span><div><small>总体判断</small><strong>{title}</strong></div></header>
    <section>{paragraphs.map((paragraph, index) => <article key={index}><span>{icons[index]}</span><div><strong>{index === 0 ? '规模与增长' : index === 1 ? '结构与效率' : '风险与行动'}</strong><p>{paragraph}</p></div><aside><small>{metrics[index].name}</small><strong>{metrics[index].value}</strong><em className={metrics[index].change < 0 ? 'is-negative' : ''}>{metrics[index].change >= 0 ? '+' : ''}{formatValue(component, metrics[index].change)}</em></aside></article>)}</section>
    <footer>结论共引用 {Math.min(shape.measureNames.length || 4, 4)} 项受治理指标</footer>
  </div>
}

function CardSupport({ item, component, shape, variant }: { item: AnalysisCardCatalogItem; component: ReportComponent; shape: DataShape; variant: AnalysisCardVariant }) {
  if (item.id === 6 && variant !== '01') return <BreakdownStrip component={component} shape={shape} limit={3} />
  if ([8, 9, 14, 16, 22, 24, 26, 28].includes(item.id)) return <StatStrip component={component} shape={shape} limit={variant === '03' ? 3 : 2} />
  if (item.id === 10 && variant === '03') return <div className="report-analysis-alert-strip"><WarningCircle size={14} /><span>发现超阈值对象</span><strong>{shape.values[0]?.filter(value => value > (valueAt(shape, 1) || Number.POSITIVE_INFINITY)).length}</strong></div>
  if (item.id === 27) return <div className="report-analysis-map-caption"><MapTrifold size={15} /><span>{shape.dimensionNames[0] || '区域'}空间分布</span><strong>{shape.labels.length} 个对象</strong></div>
  return null
}

function specialCard(item: AnalysisCardCatalogItem, component: ReportComponent, shape: DataShape, variant: AnalysisCardVariant, mode: AnalysisCardSizeMode, option: ChartOption): ReactNode | undefined {
  switch (item.id) {
    case 1: return <MetricStatusCard component={component} shape={shape} variant={variant} mode={mode} />
    case 2: return <GoalProgressCard component={component} shape={shape} variant={variant} mode={mode} />
    case 3: return <ComparisonCard component={component} shape={shape} variant={variant} mode={mode} />
    case 4: return <RankingCard component={component} shape={shape} variant={variant} />
    case 17: return <LifecycleCard component={component} shape={shape} variant={variant} mode={mode} />
    case 23: return <ScenarioCard component={component} shape={shape} variant={variant} option={option} />
    case 31: return <DetailTableCard component={component} shape={shape} variant={variant} />
    case 32: return <TimelineCard component={component} shape={shape} variant={variant} />
    case 33: return <InsightCard component={component} shape={shape} variant={variant} />
    case 34: return <ActionCard component={component} shape={shape} variant={variant} />
    case 35: return <DataInfoCard component={component} shape={shape} variant={variant} />
    case 36: return <ScopeCard component={component} shape={shape} variant={variant} />
    case 37: return <LongFormConclusionCard component={component} shape={shape} variant={variant} />
    default: return undefined
  }
}

export function AnalysisCardView({ component, result, mobile, onPick }: {
  component: ReportComponent
  result: QueryResult
  mobile?: boolean
  onPick?: (category: string) => void
}) {
  const root = useRef<HTMLDivElement>(null)
  const mode = useSizeMode(root, mobile)
  const item = analysisCardDefinition(component.templateRef.type)
  const shape = dataShape(component, result)
  const variant = variantOf(component)
  const contract = item ? analysisCardVisualContract(item.id, variant) : undefined
  const option = item ? chartOption(item, shape, variant, mode) : {}
  if (!item || !contract) return <div className="report-analysis-empty"><Info size={18} />暂无可视化合同</div>
  const special = specialCard(item, component, shape, variant, mode, option)
  return <div ref={root} className={`report-analysis-card is-size-${mode} is-motif-${contract.motif}`} data-analysis-contract={contract.id} data-analysis-size={mode}>
    <header className="report-analysis-card-head">
      <div><strong>{component.options.title || shape.measureNames[0] || item.name}</strong>{component.options.subtitle && <small>{component.options.subtitle}</small>}</div>
      <span>{contract.mainVisual}</span>
    </header>
    <div className="report-analysis-card-content">{special ?? <>
        {item.id === 27 && <img className="report-analysis-map-base" src={`/report-assets/maps/geospatial-${variant}.png`} alt="" aria-hidden="true" />}
        <AnalysisECharts option={option} onPick={onPick} label={`${component.options.title || item.name}：${contract.mainVisual}`} />
        <CardSupport item={item} component={component} shape={shape} variant={variant} />
      </>}</div>
  </div>
}
