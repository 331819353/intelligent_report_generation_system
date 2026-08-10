import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { exportTimeSpecFooter } from '../../export/timespec-footer.ts'
import type { ResolvedTimeSpec, TimeSpecView } from '../../lib/ask-data-api.ts'
import { reportTimeSpecSubtitle } from '../../report/runtime/timespec.ts'
import { renderTimeSpec, timeSpecSummaryLabel } from './timespec.ts'

type Fixture = {
  name: string
  input: ResolvedTimeSpec
  options: { locale?: string }
  expected: TimeSpecView
}

const fixtureURL = new URL('../../../../internal/askdata/testfixture/timespec/render-v1.json', import.meta.url)
const fixtures = JSON.parse(readFileSync(fixtureURL, 'utf8')) as Fixture[]

test('Go and TypeScript share at least 20 exact time rendering fixtures', () => {
  assert.ok(fixtures.length >= 20)
  for (const fixture of fixtures) {
    assert.deepEqual(renderTimeSpec(fixture.input, fixture.options), fixture.expected, fixture.name)
  }
})

test('summary, report subtitle and export footer consume the canonical renderer', () => {
  const fixture = fixtures.find(item => item.name === 'current_month_mtd_truncated_yoy')!
  const view = renderTimeSpec(fixture.input)
  assert.equal(timeSpecSummaryLabel(view), '本月至今（MTD） · 2026-08-01 至 2026-08-06 · 数据截止 2026-08-06')
  assert.equal(reportTimeSpecSubtitle(fixture.input), timeSpecSummaryLabel(view))
  assert.deepEqual(exportTimeSpecFooter(fixture.input), [
    ['时间口径', view.policyLabel], ['实际区间', view.rangeLabel], ['数据截止', view.asOfLabel],
    ['对比口径', view.comparisonLabel], ['提示', view.truncatedHint],
  ])
})

test('no comparison, no truncation and LAST_COMPLETE remain explicit', () => {
  const full = fixtures.find(item => item.name === 'full_current_month')!
  const fallback = fixtures.find(item => item.name === 'last_complete_month_fallback')!
  assert.equal(renderTimeSpec(full.input).comparisonLabel, '')
  assert.equal(renderTimeSpec(full.input).truncatedHint, '')
  assert.equal(renderTimeSpec(fallback.input).policyLabel, '已回退至上一完整月（LAST_COMPLETE）')
})

test('invalid time specs fail closed', () => {
  assert.deepEqual(renderTimeSpec({} as ResolvedTimeSpec), {
    rangeLabel: '', asOfLabel: '', policyLabel: '', comparisonLabel: '', truncatedHint: '',
  })
})
