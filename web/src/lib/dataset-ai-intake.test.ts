import assert from 'node:assert/strict'
import test from 'node:test'
import { autoConfirmDatasetAITables, datasetAIModificationNeedsTables, filterDatasetAICandidateTables, inferDatasetAIModelKind, inferDatasetAITableRole } from './dataset-ai-intake.ts'
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

test('keeps generic order detail ahead of unrelated specialized domains and removes explicit origin duplicates', () => {
  const tables = [
    { ...table('after-sales-mapped', '售后服务事实表', 'fact_after_sales'), originTableId: 'after-sales-source' },
    table('after-sales-source', '售后服务事实表', 'FACT_AFTER_SALES'),
    table('takeout', '外卖销售订单事实表', 'fact_takeout_sales_order'),
    table('order-detail', '销售订单明细事实表', 'fact_sales_order_detail'),
  ]
  assert.deepEqual(
    filterDatasetAICandidateTables(tables, '帮我生成订单详情数据', 'DWD', 3).map(item => item.id),
    ['order-detail'],
  )
})

test('keeps same-named tables when their asset identities differ', () => {
  const mysql = { ...table('mysql-orders', '销售订单表', 'fact_sales_order'), dataSourceId: 'mysql', dataSourceName: '交易 MySQL' }
  const oracle = { ...table('oracle-orders', '销售订单表', 'FACT_SALES_ORDER'), dataSourceId: 'oracle', dataSourceName: '财务 Oracle' }
  assert.deepEqual(
    filterDatasetAICandidateTables([mysql, oracle], '创建销售订单事实表', 'DWD', 4).map(item => item.id),
    ['mysql-orders', 'oracle-orders'],
  )
})

test('does not treat an industry word in a generic table description as a specialized-table conflict', () => {
  const genericOrderDetail = {
    ...table('order-detail', '销售订单明细事实表', 'fact_sales_order_item'),
    businessDescription: '记录外卖销售订单中每个商品明细项的事实表，一行代表一个订单中的一个商品行。',
    tags: ['作用:事实表', '功能:交易明细', '粒度:订单'],
  }
  const genericOrder = {
    ...table('order', '销售订单事实表', 'fact_sales_order'),
    businessDescription: '记录外卖销售订单的交易明细事实表，每行代表一笔销售订单。',
    tags: ['作用:事实表', '功能:交易明细', '粒度:订单'],
  }
  const afterSales = {
    ...table('after-sales', '售后服务事实表', 'fact_after_sales'),
    businessDescription: '记录订单售后申请、退款金额与处理状态。',
  }

  assert.deepEqual(
    filterDatasetAICandidateTables([afterSales, genericOrder, genericOrderDetail], '形成订单详情数据', 'DWD', 3).map(item => item.id),
    ['order-detail', 'order'],
  )
})

test('excludes tables already present on the modification canvas', () => {
  const tables = [
    table('product', '产品维度表', 'dim_product'),
    table('orders', '销售订单明细事实表', 'fact_sales_order_detail'),
  ]
  assert.deepEqual(
    filterDatasetAICandidateTables(tables, '补充订单详情数据', 'DWD', 3, { excludeTableIDs: ['product'] }).map(item => item.id),
    ['orders'],
  )
})

test('auto-confirms the table scope only when every candidate is explicitly named', () => {
  const orders = table('orders', '销售订单事实表', 'fact_sales_order')
  const customer = table('customer', '客户主数据表', 'dim_customer')

  const confirmed = autoConfirmDatasetAITables([orders], '创建销售订单事实表，保留订单和金额明细')
  assert.deepEqual(confirmed?.tables.map(item => item.id), ['orders'])
  assert.ok(confirmed?.reason)

  const both = autoConfirmDatasetAITables([orders, customer], '基于销售订单事实表和客户主数据表构建明细')
  assert.deepEqual(both?.tables.map(item => item.id), ['orders', 'customer'])

  // 客户主数据表未被指名（只提到“客户信息”）：范围有歧义，必须弹出确认卡片。
  assert.equal(autoConfirmDatasetAITables([orders, customer], '创建销售订单事实表，关联客户信息'), null)
  assert.equal(autoConfirmDatasetAITables([], '创建销售订单事实表'), null)
})

test('short generic fragments do not count as explicit table mentions', () => {
  const shortName = table('order', '订单', 'ord')
  assert.equal(autoConfirmDatasetAITables([shortName], '统计订单金额'), null)
  const technical = table('orders', '', 'fact_sales_order_detail')
  assert.deepEqual(
    autoConfirmDatasetAITables([technical], '基于 fact_sales_order_detail 构建明细')?.tables.map(item => item.id),
    ['orders'],
  )
})

test('routes missing-table modification clarification to the table picker', () => {
  assert.equal(datasetAIModificationNeedsTables(
    'DATASET_AI_CLARIFICATION_REQUIRED',
    '当前画布只有产品维度表，没有订单相关数据集；请问订单详情数据来自哪个数据集？',
  ), true)
  assert.equal(datasetAIModificationNeedsTables(
    'DATASET_AI_CLARIFICATION_REQUIRED',
    '你想修改哪个分组组件？',
  ), false)
  assert.equal(datasetAIModificationNeedsTables('DATASET_AI_INVALID_OUTPUT', '缺少订单数据表'), false)
})

test('recognizes application-layer requirements and splits scope tables into workflow roles', () => {
  assert.equal(inferDatasetAIModelKind('搭建销售看板应用层报表'), 'ADS')
  assert.equal(inferDatasetAITableRole(table('c', '渠道维表', 'dim_channel'), 'DWS'), 'DIMENSION')
  assert.equal(inferDatasetAITableRole(table('o', '销售订单事实表', 'fact_sales_order'), 'DWS'), 'PRIMARY')
  assert.equal(inferDatasetAITableRole(table('p', '产品', 'product', 'DIM'), 'ADS'), 'DIMENSION')
  // A DIM build treats every source as an entity source, never as a lookup dimension.
  assert.equal(inferDatasetAITableRole(table('c2', '渠道维表', 'dim_channel'), 'DIM'), 'PRIMARY')
})

test('uses governed role tags before the ODS fallback', () => {
  const rawDimension = {
    ...table('customer', '客户资料', 'customer_master', 'ODS'),
    tags: ['主题:客户画像', '作用:维度表', '粒度:客户'],
  }
  const rawFact = {
    ...table('orders', '订单流水', 'sales_order', 'ODS'),
    tags: ['主题:经营分析', '作用:事实表', '粒度:订单'],
  }
  assert.equal(inferDatasetAITableRole(rawDimension, 'DWS'), 'DIMENSION')
  assert.equal(inferDatasetAITableRole(rawFact, 'DWS'), 'PRIMARY')
})
