import type { ComponentManifest } from './manifests.ts'
import type { ComponentOptions, DataBinding, FieldBinding } from './schema.ts'

export type QueryResult = {
  columns: string[]
  rows: unknown[][]
  hash?: string
  partial?: boolean
}

/**
 * 调色板按 options.colorPaletteRef 选取。未声明时使用企业主题默认色板，
 * 而不是每个图表各写一套颜色。
 */
const palettes: Record<string, string[]> = {
  default: ['#2864dc', '#14a27f', '#79a8f3', '#f0ae2c', '#8b5cf6', '#e05252', '#0ea5e9', '#98a2b3'],
  cool: ['#2864dc', '#0ea5e9', '#14a27f', '#5b8bea', '#84c9bd', '#b7c6da'],
  warm: ['#e05252', '#f0ae2c', '#f97316', '#d946a0', '#8b5cf6', '#b7c6da'],
  neutral: ['#475467', '#667085', '#98a2b3', '#b7c6da', '#dfe4ea'],
}

export function paletteFor(options: ComponentOptions): string[] {
  return palettes[options.colorPaletteRef?.trim() || 'default'] ?? palettes.default
}

/** 维度类角色。度量类角色决定哪些列进入 series。 */
const dimensionRoles = new Set(['DIMENSION', 'CATEGORY', 'X_AXIS', 'TIME', 'SERIES', 'COLOR', 'LABEL', 'DETAIL'])
const measureRoles = new Set(['VALUE', 'Y_AXIS', 'SIZE'])

export type ResolvedColumns = {
  /** 类目轴列索引；没有维度绑定时为 -1。 */
  categoryIndex: number
  /** 度量列索引，按绑定顺序。 */
  valueIndexes: number[]
  /** 分组（多系列）列索引；未绑定 SERIES 时为 -1。 */
  seriesIndex: number
}

/**
 * 把数据绑定的逻辑角色映射到查询结果的列。
 *
 * DATASET_FIELD 的执行结果按「维度在前、度量在后」的绑定顺序返回列，
 * 因此角色可以直接定位到列，而不需要靠列名或值类型去猜。绑定不可用
 * （例如 SEMANTIC_IR 结果列由语义层决定）时才回退到类型推断。
 */
export function resolveColumns(result: QueryResult, binding?: DataBinding): ResolvedColumns {
  const byName = new Map(result.columns.map((column, index) => [column, index]))
  const indexOf = (item: FieldBinding, fallback: number) => byName.get(item.field) ?? fallback

  const dimensions = (binding?.dimensions ?? []).filter(item => dimensionRoles.has(item.role))
  const measures = (binding?.measures ?? []).filter(item => measureRoles.has(item.role))

  if (measures.length > 0) {
    const valueIndexes = measures
      .map((item, offset) => indexOf(item, dimensions.length + offset))
      .filter(index => index >= 0 && index < result.columns.length)
    const category = dimensions.find(item => item.role !== 'SERIES')
    const series = dimensions.find(item => item.role === 'SERIES')
    if (valueIndexes.length > 0) {
      return {
        categoryIndex: category ? indexOf(category, dimensions.indexOf(category)) : -1,
        seriesIndex: series ? indexOf(series, dimensions.indexOf(series)) : -1,
        valueIndexes,
      }
    }
  }

  // 回退：第一列出现字符串的列作为类目，其余可解析为有限数字的列作为度量。
  const categoryIndex = result.columns.findIndex((_, index) =>
    result.rows.some(row => typeof row[index] === 'string'))
  const valueIndexes = result.columns
    .map((_, index) => index)
    .filter(index => index !== categoryIndex && result.rows.some(row => Number.isFinite(Number(row[index]))))
  return { categoryIndex, seriesIndex: -1, valueIndexes: valueIndexes.length ? valueIndexes : [Math.min(1, result.columns.length - 1)] }
}

function numeric(value: unknown, nullPolicy: ComponentOptions['nullPolicy']): number | null {
  if (value === null || value === undefined || value === '') return nullPolicy === 'ZERO' ? 0 : null
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return nullPolicy === 'ZERO' ? 0 : null
  return parsed
}

export function formatNumber(value: unknown, numberFormat?: string): string {
  if (value === null || value === undefined) return '—'
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return String(value)
  const format = numberFormat?.trim()
  if (!format) return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(parsed)
  if (format === 'PERCENT') return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(parsed * 100)}%`
  if (format === 'INTEGER') return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 0 }).format(parsed)
  if (format.startsWith('DECIMAL_')) {
    const digits = Number(format.slice('DECIMAL_'.length))
    if (Number.isFinite(digits)) {
      return new Intl.NumberFormat('zh-CN', { minimumFractionDigits: digits, maximumFractionDigits: digits }).format(parsed)
    }
  }
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(parsed)
}

const axisText = { color: '#8d96a4', fontSize: 10 }
const tooltipStyle = {
  backgroundColor: '#fff', borderColor: '#dfe4ea',
  textStyle: { color: '#344054', fontSize: 11 },
}

type SeriesShape = 'bar' | 'line' | 'area' | 'pie' | 'scatter' | 'funnel' | 'waterfall'

/**
 * 组件类型 → 图形。清单里没有「图形」字段，因此这里保留一张显式的映射表：
 * 它是有限的、可枚举的注册表，而不是对类型名做正则猜测。未登记的 ECHARTS
 * 组件按柱状图渲染并在返回值中标记，调用方可据此提示清单缺少渲染登记。
 */
const shapes: Record<string, SeriesShape> = {
  'bar-comparison': 'bar',
  'bar-horizontal': 'bar',
  'waterfall-chart': 'waterfall',
  'line-trend': 'line',
  'area-stacked': 'area',
  'pie-donut': 'pie',
  scatter: 'scatter',
  funnel: 'funnel',
}

export function shapeFor(manifest: ComponentManifest | undefined, type: string): { shape: SeriesShape; registered: boolean } {
  const shape = shapes[type]
  if (shape) return { shape, registered: true }
  if (manifest?.renderer === 'ECHARTS') return { shape: 'bar', registered: false }
  return { shape: 'bar', registered: false }
}

export type ChartInput = {
  manifest?: ComponentManifest
  type: string
  options: ComponentOptions
  binding?: DataBinding
  result: QueryResult
  /** 移动端投影下按清单的 mobilePolicy 降级图例与标签。 */
  mobile?: boolean
}

/**
 * 由配置生成 ECharts option：图形来自组件类型登记，数据来自绑定角色解析出的
 * 列，其余表现全部来自 options —— 渲染器本身不含任何特定报告的业务逻辑。
 */
export function buildChartOption(input: ChartInput): Record<string, unknown> {
  const { manifest, type, options, binding, result, mobile } = input
  const { shape } = shapeFor(manifest, type)
  const columns = resolveColumns(result, binding)
  const palette = paletteFor(options)
  const nullPolicy = options.nullPolicy
  const horizontal = options.orientation === 'HORIZONTAL' || type === 'bar-horizontal'

  const legendMode = mobile
    ? options.mobileLegendMode ?? manifest?.mobilePolicy.defaultLegendMode ?? 'VISIBLE'
    : options.showLegend === false ? 'HIDDEN' : 'VISIBLE'
  const legend = legendMode === 'HIDDEN'
    ? { show: false }
    : {
      show: true, top: 0, left: 'center', type: legendMode === 'SCROLL' ? 'scroll' : 'plain',
      itemWidth: 13, itemHeight: 7, textStyle: { color: '#667085', fontSize: 10 },
    }

  const label = {
    show: options.showLabel === true,
    color: '#697386', fontSize: 10,
    formatter: ({ value }: { value: unknown }) => formatNumber(value, options.numberFormat),
  }

  let rows = result.rows
  if (options.topN && options.topN > 0 && columns.valueIndexes.length > 0) {
    const primary = columns.valueIndexes[0]
    rows = rows.slice().sort((left, right) => Number(right[primary] ?? 0) - Number(left[primary] ?? 0)).slice(0, options.topN)
  }

  const categories = rows.map(row =>
    columns.categoryIndex >= 0 ? String(row[columns.categoryIndex] ?? '—') : '')

  const base = {
    animation: options.animation !== false,
    color: palette,
    aria: { enabled: true, description: options.title || type },
    tooltip: {
      trigger: shape === 'pie' || shape === 'funnel' || shape === 'scatter' ? 'item' : 'axis',
      ...tooltipStyle,
      valueFormatter: (value: unknown) => formatNumber(value, options.numberFormat),
    },
    legend,
  }

  if (shape === 'pie' || shape === 'funnel') {
    const valueIndex = columns.valueIndexes[0] ?? 0
    const data = rows.map(row => ({
      name: columns.categoryIndex >= 0 ? String(row[columns.categoryIndex] ?? '—') : String(result.columns[valueIndex]),
      value: numeric(row[valueIndex], nullPolicy) ?? 0,
    })).filter(item => nullPolicy !== 'HIDE' || item.value !== 0)
    if (shape === 'funnel') {
      return { ...base, series: [{ type: 'funnel', left: '12%', width: '76%', label: { ...label, show: true, position: 'inside' }, data }] }
    }
    return {
      ...base,
      legend: legendMode === 'HIDDEN' ? { show: false } : { ...legend, orient: 'vertical', right: 8, top: 'middle', left: 'auto' },
      series: [{
        type: 'pie', radius: ['42%', '68%'], center: [legendMode === 'HIDDEN' ? '50%' : '38%', '52%'],
        label: { show: options.showLabel === true, fontSize: 10 }, data,
      }],
    }
  }

  if (shape === 'scatter') {
    const [xIndex, yIndex] = [columns.valueIndexes[0] ?? 0, columns.valueIndexes[1] ?? columns.valueIndexes[0] ?? 1]
    return {
      ...base,
      grid: { left: 52, right: 24, top: 34, bottom: 32 },
      xAxis: { type: 'value', name: result.columns[xIndex], nameTextStyle: axisText, axisLabel: axisText, splitLine: { lineStyle: { color: '#edf0f4' } } },
      yAxis: { type: 'value', name: result.columns[yIndex], nameTextStyle: axisText, axisLabel: axisText, splitLine: { lineStyle: { color: '#edf0f4' } } },
      series: [{
        type: 'scatter', symbolSize: 9,
        data: rows.map(row => [numeric(row[xIndex], nullPolicy) ?? 0, numeric(row[yIndex], nullPolicy) ?? 0]),
      }],
    }
  }

  if (shape === 'waterfall') {
    const valueIndex = columns.valueIndexes[0] ?? 0
    const values = rows.map(row => numeric(row[valueIndex], nullPolicy) ?? 0)
    const helper: Array<number | string> = []
    const totals: Array<number | string> = []
    const gains: Array<number | string> = []
    const losses: Array<number | string> = []
    let running = values[0] ?? 0

    values.forEach((value, index) => {
      const endpoint = index === 0 || index === values.length - 1
      if (endpoint) {
        helper.push(0); totals.push(value); gains.push('-'); losses.push('-')
        if (index === 0) running = value
        return
      }
      const next = running + value
      helper.push(Math.min(running, next)); totals.push('-')
      gains.push(value >= 0 ? value : '-'); losses.push(value < 0 ? Math.abs(value) : '-')
      running = next
    })

    const waterfallLabel = (prefix = '') => ({
      show: options.showLabel !== false,
      position: 'top',
      color: '#52647a',
      fontSize: 10,
      fontWeight: 650,
      formatter: ({ value }: { value: unknown }) => `${prefix}${formatNumber(value, options.numberFormat)}`,
    })
    const categoryAxis = {
      type: 'category', data: categories,
      axisTick: { show: false }, axisLine: { lineStyle: { color: '#dce6f0' } },
      axisLabel: { ...axisText, hideOverlap: true, interval: mobile ? 'auto' : 0 },
    }
    const valueAxis = {
      type: 'value', axisTick: { show: false }, axisLine: { show: false },
      splitLine: { lineStyle: { color: '#edf2f7', type: 'dashed' } },
      axisLabel: { ...axisText, formatter: (value: number) => formatNumber(value, options.numberFormat) },
    }
    return {
      ...base,
      legend: { show: false },
      grid: { left: 58, right: 24, top: 28, bottom: mobile ? 48 : 42 },
      xAxis: categoryAxis,
      yAxis: valueAxis,
      series: [
        { name: '辅助', type: 'bar', stack: 'waterfall', silent: true, itemStyle: { color: 'transparent' }, emphasis: { itemStyle: { color: 'transparent' } }, data: helper },
        { name: '结果', type: 'bar', stack: 'waterfall', barMaxWidth: 38, label: waterfallLabel(), itemStyle: { color: '#1769d2', borderRadius: [5, 5, 1, 1] }, data: totals },
        { name: '正向影响', type: 'bar', stack: 'waterfall', barMaxWidth: 38, label: waterfallLabel('+'), itemStyle: { color: '#16a37a', borderRadius: [5, 5, 1, 1] }, data: gains },
        { name: '负向影响', type: 'bar', stack: 'waterfall', barMaxWidth: 38, label: waterfallLabel('-'), itemStyle: { color: '#e35d67', borderRadius: [5, 5, 1, 1] }, data: losses },
      ],
    }
  }

  // 柱 / 线 / 面积共用同一套直角坐标系配置。
  const seriesFromColumns = columns.valueIndexes.map(index => ({
    name: result.columns[index],
    type: shape === 'bar' ? 'bar' : 'line',
    smooth: shape !== 'bar' && options.smooth === true,
    stack: shape === 'area' ? 'total' : undefined,
    areaStyle: shape === 'area' ? { opacity: 0.16 } : undefined,
    connectNulls: nullPolicy === 'GAP' ? false : true,
    symbolSize: 4,
    barMaxWidth: 26,
    label,
    itemStyle: { borderRadius: shape === 'bar' ? (horizontal ? [0, 2, 2, 0] : [2, 2, 0, 0]) : 0 },
    lineStyle: { width: 1.8 },
    data: rows.map(row => numeric(row[index], nullPolicy)),
  }))

  // 绑定了 SERIES 角色时，把长表按分组列透视成多条系列。
  const series = columns.seriesIndex >= 0 && columns.valueIndexes.length === 1
    ? pivotSeries(rows, columns, result, { shape, smooth: options.smooth === true, label, nullPolicy, horizontal })
    : seriesFromColumns
  const axisCategories = columns.seriesIndex >= 0 && columns.valueIndexes.length === 1
    ? [...new Set(categories)]
    : categories

  const categoryAxis = {
    type: 'category', data: axisCategories, boundaryGap: shape !== 'line',
    axisTick: { show: false }, axisLine: { lineStyle: { color: '#dfe4ea' } },
    axisLabel: { ...axisText, hideOverlap: true },
  }
  const valueAxis = {
    type: 'value', axisTick: { show: false }, axisLine: { show: false },
    splitLine: { lineStyle: { color: '#edf0f4' } },
    axisLabel: { ...axisText, formatter: (value: number) => formatNumber(value, options.numberFormat) },
  }

  return {
    ...base,
    grid: { left: horizontal ? 82 : 56, right: 24, top: legendMode === 'HIDDEN' ? 16 : 34, bottom: 30 },
    xAxis: horizontal ? valueAxis : categoryAxis,
    yAxis: horizontal ? { ...categoryAxis, inverse: true, boundaryGap: true } : valueAxis,
    series,
  }
}

function pivotSeries(
  rows: unknown[][],
  columns: ResolvedColumns,
  result: QueryResult,
  style: { shape: SeriesShape; smooth: boolean; label: unknown; nullPolicy: ComponentOptions['nullPolicy']; horizontal: boolean },
) {
  const valueIndex = columns.valueIndexes[0]
  const categories = [...new Set(rows.map(row => String(row[columns.categoryIndex] ?? '—')))]
  const groups = [...new Set(rows.map(row => String(row[columns.seriesIndex] ?? '—')))]
  const lookup = new Map(rows.map(row =>
    [`${row[columns.categoryIndex]} ${row[columns.seriesIndex]}`, numeric(row[valueIndex], style.nullPolicy)]))
  return groups.map(group => ({
    name: group,
    type: style.shape === 'bar' ? 'bar' : 'line',
    smooth: style.shape !== 'bar' && style.smooth,
    stack: style.shape === 'area' ? 'total' : undefined,
    areaStyle: style.shape === 'area' ? { opacity: 0.16 } : undefined,
    symbolSize: 4,
    barMaxWidth: 26,
    label: style.label,
    itemStyle: { borderRadius: style.shape === 'bar' ? (style.horizontal ? [0, 2, 2, 0] : [2, 2, 0, 0]) : 0 },
    lineStyle: { width: 1.8 },
    data: categories.map(category => lookup.get(`${category} ${group}`) ?? null),
  }))
}

/** 供 metric-card 使用：从结果中取出首个度量的单值与其列名。 */
export function singleMetric(result: QueryResult, binding?: DataBinding) {
  const columns = resolveColumns(result, binding)
  const index = columns.valueIndexes[0] ?? 0
  return { label: result.columns[index] ?? '', value: result.rows[0]?.[index] ?? null }
}
