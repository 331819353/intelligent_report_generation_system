import assert from 'node:assert/strict'
import test from 'node:test'
import {
  mergeOperatingAnalysisDatasetSummaries,
  operatingAnalysisDatasetFixtures,
  operatingAnalysisDatasetSummaries,
} from './operating-analysis-datasets.ts'

test('operating report adds only self-contained ADS datasets', () => {
  assert.equal(operatingAnalysisDatasetFixtures.length, 3)
  assert.deepEqual(operatingAnalysisDatasetFixtures.map(item => item.summary.layer), ['ADS', 'ADS', 'ADS'])
  assert.equal(operatingAnalysisDatasetFixtures.every(item => item.upstreamCodes.length === 0), true)
  assert.equal(operatingAnalysisDatasetFixtures.every(item => item.summary.tags.includes('TPL-RPT-001')), true)
  assert.equal(operatingAnalysisDatasetFixtures.every(item => item.summary.description.includes('自包含')), true)
})

test('ADS identifiers and fields remain unique and governed', () => {
  const ids = operatingAnalysisDatasetFixtures.map(item => item.summary.id)
  const codes = operatingAnalysisDatasetFixtures.map(item => item.summary.code)
  assert.equal(new Set(ids).size, ids.length)
  assert.equal(new Set(codes).size, codes.length)
  for (const fixture of operatingAnalysisDatasetFixtures) {
    assert.equal(fixture.summary.status, 'PUBLISHED')
    assert.ok(fixture.grain.length > 0)
    assert.ok(fixture.fields.length >= 20)
    assert.equal(new Set(fixture.fields.map(field => field.code)).size, fixture.fields.length)
  }
})

test('the three ADS tables cover all report consumption themes', () => {
  const fieldCodes = (code: string) => new Set(operatingAnalysisDatasetFixtures
    .find(item => item.summary.code === code)?.fields.map(field => field.code))

  const profit = fieldCodes('ads_operating_report_profit')
  ;[
    'revenue_amount', 'retail_sales_yoy_rate', 'sales_gross_margin_rate', 'material_gross_margin_rate',
    'expense_amount', 'expense_rate', 'management_profit_amount', 'domestic_revenue_impact',
    'overseas_margin_impact', 'other_profit_impact', 'forecast_revenue_amount', 'forecast_profit_amount',
  ].forEach(code => assert.equal(profit.has(code), true, code))

  const receivable = fieldCodes('ads_operating_report_receivable')
  ;[
    'receivable_balance_amount', 'overdue_receivable_amount', 'overdue_rate', 'aging_over_90_amount',
    'planned_collection_amount', 'actual_collection_amount', 'collection_completion_rate', 'customer_risk_rank',
  ].forEach(code => assert.equal(receivable.has(code), true, code))

  const inventory = fieldCodes('ads_operating_report_inventory_logistics')
  ;[
    'logistics_expense_amount', 'reverse_transport_amount', 'potential_saving_amount', 'ltl_share_rate',
    'warehouse_utilization_rate', 'inventory_amount', 'inventory_turnover_days', 'stagnant_inventory_amount',
    'stockout_rate', 'replenishment_timely_rate', 'risk_rank',
  ].forEach(code => assert.equal(inventory.has(code), true, code))
})

test('built-in ADS remain visible unless the server returns the same dataset code', () => {
  assert.deepEqual(mergeOperatingAnalysisDatasetSummaries([]), operatingAnalysisDatasetSummaries)
  const persisted = {
    ...operatingAnalysisDatasetSummaries[0],
    id: 'persisted-operating-profit',
    version: 99,
  }
  const merged = mergeOperatingAnalysisDatasetSummaries([persisted])
  assert.equal(merged.filter(item => item.code === persisted.code).length, 1)
  assert.equal(merged.find(item => item.code === persisted.code)?.id, persisted.id)
  assert.equal(merged.length, operatingAnalysisDatasetSummaries.length)
})
