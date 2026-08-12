import assert from 'node:assert/strict'
import test from 'node:test'

import { buildChartOption, formatNumber, resolveColumns, singleMetric } from './chart-option.ts'
import type { DataBinding } from './schema.ts'

const binding: DataBinding = {
  bindingMode: 'DATASET_FIELD',
  dataContextId: 'ctx',
  dimensions: [{ role: 'CATEGORY', field: 'channel' }],
  measures: [{ role: 'VALUE', field: 'revenue' }],
}

const result = {
  columns: ['channel', 'revenue'],
  rows: [['线上', 120], ['线下', 80], ['工程', 40]] as unknown[][],
}

test('binding roles decide which column is the category and which are measures', () => {
  assert.deepEqual(resolveColumns(result, binding), { categoryIndex: 0, seriesIndex: -1, valueIndexes: [1] })
})

test('column order still resolves when the runner returns measures first', () => {
  const swapped = { columns: ['revenue', 'channel'], rows: [[120, '线上']] as unknown[][] }
  assert.deepEqual(resolveColumns(swapped, binding), { categoryIndex: 1, seriesIndex: -1, valueIndexes: [0] })
})

test('without a binding the resolver falls back to value-type inference', () => {
  const columns = resolveColumns(result, undefined)
  assert.equal(columns.categoryIndex, 0)
  assert.deepEqual(columns.valueIndexes, [1])
})

test('component options change the rendered chart rather than being ignored', () => {
  const base = buildChartOption({
    type: 'bar-comparison', options: {}, binding, result,
  }) as { legend: { show?: boolean }; series: Array<{ type: string; data: unknown[] }>; animation: boolean }
  assert.equal(base.legend.show, true)
  assert.equal(base.series[0].type, 'bar')
  assert.equal(base.animation, true)

  const tuned = buildChartOption({
    type: 'bar-comparison', options: { showLegend: false, animation: false, topN: 2 }, binding, result,
  }) as { legend: { show?: boolean }; series: Array<{ data: unknown[] }>; animation: boolean }
  assert.equal(tuned.legend.show, false)
  assert.equal(tuned.animation, false)
  // topN 先按首个度量降序截断，而不是照原样把所有行画出来。
  assert.deepEqual(tuned.series[0].data, [120, 80])
})

test('orientation swaps the axes instead of needing a separate component', () => {
  const vertical = buildChartOption({ type: 'bar-comparison', options: {}, binding, result }) as { xAxis: { type: string } }
  const horizontal = buildChartOption({
    type: 'bar-comparison', options: { orientation: 'HORIZONTAL' }, binding, result,
  }) as { xAxis: { type: string }; yAxis: { type: string } }
  assert.equal(vertical.xAxis.type, 'category')
  assert.equal(horizontal.xAxis.type, 'value')
  assert.equal(horizontal.yAxis.type, 'category')
})

test('null policy decides between gaps and zeroes', () => {
  const withNulls = { columns: ['channel', 'revenue'], rows: [['线上', null], ['线下', 80]] as unknown[][] }
  const gap = buildChartOption({ type: 'line-trend', options: { nullPolicy: 'GAP' }, binding, result: withNulls }) as { series: Array<{ data: unknown[] }> }
  const zero = buildChartOption({ type: 'line-trend', options: { nullPolicy: 'ZERO' }, binding, result: withNulls }) as { series: Array<{ data: unknown[] }> }
  assert.deepEqual(gap.series[0].data, [null, 80])
  assert.deepEqual(zero.series[0].data, [0, 80])
})

test('a SERIES role pivots a long table into one series per group', () => {
  const grouped: DataBinding = {
    bindingMode: 'DATASET_FIELD', dataContextId: 'ctx',
    dimensions: [{ role: 'X_AXIS', field: 'month' }, { role: 'SERIES', field: 'channel' }],
    measures: [{ role: 'VALUE', field: 'revenue' }],
  }
  const long = {
    columns: ['month', 'channel', 'revenue'],
    rows: [['01', '线上', 10], ['01', '线下', 5], ['02', '线上', 12], ['02', '线下', 6]] as unknown[][],
  }
  const option = buildChartOption({ type: 'bar-comparison', options: {}, binding: grouped, result: long }) as {
    series: Array<{ name: string; data: unknown[] }>; xAxis: { data: string[] }
  }
  assert.deepEqual(option.xAxis.data, ['01', '02'])
  assert.deepEqual(option.series.map(item => item.name), ['线上', '线下'])
  assert.deepEqual(option.series[0].data, [10, 12])
  assert.deepEqual(option.series[1].data, [5, 6])
})

test('pie components read the measure through the same binding roles', () => {
  const option = buildChartOption({ type: 'pie-donut', options: {}, binding, result }) as {
    series: Array<{ type: string; data: Array<{ name: string; value: number }> }>
  }
  assert.equal(option.series[0].type, 'pie')
  assert.deepEqual(option.series[0].data, [
    { name: '线上', value: 120 }, { name: '线下', value: 80 }, { name: '工程', value: 40 },
  ])
})

test('metric cards read the first bound measure, not the first column', () => {
  assert.deepEqual(singleMetric(result, binding), { label: 'revenue', value: 120 })
})

test('numberFormat is applied by the renderer rather than baked into the data', () => {
  assert.equal(formatNumber(1234.567, undefined), '1,234.57')
  assert.equal(formatNumber(1234.567, 'INTEGER'), '1,235')
  assert.equal(formatNumber(0.1234, 'PERCENT'), '12.34%')
  assert.equal(formatNumber(1234.5, 'DECIMAL_3'), '1,234.500')
  assert.equal(formatNumber(null, undefined), '—')
})
