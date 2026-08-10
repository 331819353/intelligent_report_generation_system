import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import {
  resolveTotalsBehavior as resolveAskDataTotals,
  resolveTotalValue as resolveAskDataTotalValue,
} from '../askdata/result/totals.ts'
import {
  resolveTotalsBehavior as resolveReportTotals,
  resolveTotalValue as resolveReportTotalValue,
} from '../report/runtime/totals.ts'
import {
  resolveTotalsBehavior as resolveExportTotals,
  resolveTotalValue as resolveExportTotalValue,
} from '../export/totals.ts'
import {
  resolveComponentAvailability,
  resolveTotalsBehavior,
  sumExactDecimals,
  type ResultColumn,
} from './totals.ts'

const base: ResultColumn = {
  name: 'metric_value', role: 'METRIC', metricVersionId: 'metric-v1',
  additivity: 'FULLY_ADDITIVE', totalsNotSummable: false, displayPrecision: 2,
}

test('resolves all three additivity classes without unsafe fallback', () => {
  assert.deepEqual(resolveTotalsBehavior(base), { mode: 'SUM' })
  assert.deepEqual(resolveTotalsBehavior({
    ...base, additivity: 'SEMI_ADDITIVE', totalsNotSummable: true, recomputedTotal: '19.25',
  }), {
    mode: 'RECOMPUTED', value: '19.25', note: '合计为重算值，不等于各行相加',
  })
  assert.deepEqual(resolveTotalsBehavior({
    ...base, additivity: 'NON_ADDITIVE', totalsNotSummable: true,
  }), {
    mode: 'HIDDEN', note: '该指标不可直接相加，且当前没有可用的重算合计',
  })
  assert.equal(resolveTotalsBehavior({
    ...base, additivity: 'NON_ADDITIVE', totalsNotSummable: true, recomputedTotal: 'NaN',
  }).mode, 'HIDDEN')
})

test('AskData, report and export expose the same governed decision functions', () => {
  assert.equal(resolveAskDataTotals, resolveTotalsBehavior)
  assert.equal(resolveReportTotals, resolveTotalsBehavior)
  assert.equal(resolveExportTotals, resolveTotalsBehavior)
  assert.equal(resolveAskDataTotalValue, resolveReportTotalValue)
  assert.equal(resolveReportTotalValue, resolveExportTotalValue)
})

test('sums decimal strings exactly without Number coercion', () => {
  assert.equal(sumExactDecimals(['0.1', '0.2']), '0.3')
  assert.equal(sumExactDecimals(['9007199254740992.01', '0.09', null]), '9007199254740992.1')
  assert.equal(resolveAskDataTotalValue(base, ['0.1', '0.2']), '0.3')
  assert.equal(resolveExportTotalValue({
    ...base, additivity: 'NON_ADDITIVE', totalsNotSummable: true,
  }, ['10', '20']), undefined)
})

test('manifest additivity guard removes stacking and share-of-total options', () => {
  assert.deepEqual(resolveComponentAvailability({ stackingRequiresAdditive: true }, [base]), { allowed: true })
  const unavailable = resolveComponentAvailability({ stackingRequiresAdditive: true }, [{
    ...base, additivity: 'NON_ADDITIVE', totalsNotSummable: true,
  }])
  assert.equal(unavailable.allowed, false)
  if (!unavailable.allowed) assert.match(unavailable.reason, /完全可加指标/)
  assert.deepEqual(resolveComponentAvailability({ stackingRequiresAdditive: false }, []), { allowed: true })
})

test('Component Manifest requires an explicit boolean additivity guard', () => {
  const schema = JSON.parse(readFileSync('../api/schemas/component-manifest-v1.schema.json', 'utf8')) as {
    required: string[]
    properties: Record<string, { type?: string }>
  }
  assert.equal(schema.required.includes('stackingRequiresAdditive'), true)
  assert.equal(schema.properties.stackingRequiresAdditive.type, 'boolean')
})
