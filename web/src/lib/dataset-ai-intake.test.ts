import assert from 'node:assert/strict'
import test from 'node:test'
import { filterDatasetAICandidateTables, inferDatasetAIModelKind } from './dataset-ai-intake.ts'
import type { AssetTable } from './datasets.ts'

const table = (id: string, businessName: string, tableName: string, datasetLayer?: AssetTable['datasetLayer']): AssetTable => ({
  id, businessName, tableName, datasetLayer, schemaName: 'sales', dataSourceId: 'source', dataSourceName: '销售库',
  dataSourceType: 'MYSQL', columnCount: 8,
})

test('recognizes explicit fact, detail, dimension and aggregate requirements', () => {
  assert.equal(inferDatasetAIModelKind('创建销售订单事实表'), 'DWD')
  assert.equal(inferDatasetAIModelKind('生成订单明细'), 'DWD')
  assert.equal(inferDatasetAIModelKind('建设客户维度表'), 'DIM')
  assert.equal(inferDatasetAIModelKind('按区域生成月度聚合表'), 'DWS')
})

test('asks for confirmation when the requirement has no clear model kind', () => {
  assert.equal(inferDatasetAIModelKind('分析客户区域和销售金额'), null)
  assert.equal(inferDatasetAIModelKind('把事实表汇总成聚合表'), null)
})

test('filters candidate tables before confirmation', () => {
  const tables = [
    table('customer', '客户主数据表', 'dim_customer'),
    table('orders', '销售订单事实表', 'fact_sales_order'),
    table('product', '商品主数据表', 'dim_product'),
  ]
  assert.deepEqual(filterDatasetAICandidateTables(tables, '创建销售订单事实表，关联客户信息', 'DWD', 2).map(item => item.id), ['orders', 'customer'])
  assert.deepEqual(filterDatasetAICandidateTables(tables, '创建销售订单事实表', 'DWD', 2).map(item => item.id), ['orders'])
  assert.equal(filterDatasetAICandidateTables(tables, '建设客户维度表', 'DIM', 1)[0].id, 'customer')
})
