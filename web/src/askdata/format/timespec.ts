import type { ResolvedTimeSpec, TimeSpecView } from '../../lib/ask-data-api'

export type RenderTimeSpecOptions = { locale?: string }

const emptyView = (): TimeSpecView => ({
  rangeLabel: '', asOfLabel: '', policyLabel: '', comparisonLabel: '', truncatedHint: '',
})

const periodLabels: Record<string, string> = {
  TODAY: '今日', YESTERDAY: '昨日', CURRENT_WEEK: '本周', PREVIOUS_WEEK: '上周',
  CURRENT_MONTH: '本月', PREVIOUS_MONTH: '上月', CURRENT_QUARTER: '本季度', PREVIOUS_QUARTER: '上季度',
  CURRENT_YEAR: '本年', PREVIOUS_YEAR: '上年', CURRENT_FISCAL_MONTH: '本财月', PREVIOUS_FISCAL_MONTH: '上财月',
  CURRENT_FISCAL_QUARTER: '本财季', PREVIOUS_FISCAL_QUARTER: '上财季', CURRENT_FISCAL_YEAR: '本财年',
  PREVIOUS_FISCAL_YEAR: '上财年', EXPLICIT_DAY: '指定日期', EXPLICIT_MONTH: '指定月份',
  EXPLICIT_YEAR: '指定年份', LAST_12_MONTHS: '近 12 个月', ABSOLUTE: '指定区间', EXPLICIT_RANGE: '指定区间',
}

const grainLabels: Record<string, string> = {
  DAY: '日', WEEK: '周', MONTH: '月', QUARTER: '季度', YEAR: '年',
}

const policySourceLabels: Record<ResolvedTimeSpec['policySource'], string> = {
  METRIC: '指标口径', TIME_CONTRACT: '时间合同', DOMAIN: '业务域默认', PLATFORM_DEFAULT: '平台默认',
}

function validInstant(value: string): boolean {
  return typeof value === 'string' && value !== '' && Number.isFinite(Date.parse(value))
}

function validTimeZone(value: string): boolean {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: value }).format(0)
    return value !== ''
  } catch {
    return false
  }
}

function validateSpec(spec: ResolvedTimeSpec): boolean {
  if (!spec) return false
  const fiscal = typeof spec.grain === 'string' && spec.grain.startsWith('FISCAL_')
  if (!spec.requestedPeriod || !['DAY', 'WEEK', 'MONTH', 'QUARTER', 'YEAR', 'FISCAL_MONTH', 'FISCAL_QUARTER', 'FISCAL_YEAR'].includes(spec.grain) ||
    !['MTD', 'FULL_PERIOD', 'LAST_COMPLETE'].includes(spec.policyApplied) ||
    !['METRIC', 'TIME_CONTRACT', 'DOMAIN', 'PLATFORM_DEFAULT'].includes(spec.policySource) ||
    !validInstant(spec.resolvedStart) || !validInstant(spec.resolvedEndExclusive) || !validInstant(spec.dataAvailableThrough) ||
    Date.parse(spec.resolvedEndExclusive) <= Date.parse(spec.resolvedStart) || !validTimeZone(spec.timezone) ||
    fiscal !== Boolean(spec.calendarVersionId) || spec.truncatedByDataAvailability && spec.periodFallbackApplied) return false
  const comparison = spec.comparison
  return !comparison || (
    ['YEAR_OVER_YEAR', 'MONTH_OVER_MONTH', 'QUARTER_OVER_QUARTER', 'WEEK_OVER_WEEK', 'PERIOD_OVER_PERIOD'].includes(comparison.type) &&
    Number.isSafeInteger(comparison.periods) && comparison.periods >= 1 &&
    ['SAME_DAY_COUNT', 'SAME_CALENDAR_RANGE'].includes(comparison.alignment) &&
    validInstant(comparison.resolvedStart) && validInstant(comparison.resolvedEndExclusive) &&
    Date.parse(comparison.resolvedEndExclusive) > Date.parse(comparison.resolvedStart)
  )
}

function dateInTimeZone(value: string, timeZone: string): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone, year: 'numeric', month: '2-digit', day: '2-digit',
  }).formatToParts(new Date(value))
  const values = Object.fromEntries(parts.map(part => [part.type, part.value]))
  return `${values.year}-${values.month}-${values.day}`
}

function previousDate(value: string): string {
  const [year, month, day] = value.split('-').map(Number)
  const date = new Date(Date.UTC(year, month - 1, day))
  date.setUTCDate(date.getUTCDate() - 1)
  return date.toISOString().slice(0, 10)
}

function policyLabel(spec: ResolvedTimeSpec): string {
  const period = periodLabels[spec.requestedPeriod] ?? '请求周期'
  if (spec.policyApplied === 'MTD') return spec.requestedPeriod === 'TODAY' ? '今日（MTD）' : `${period}至今（MTD）`
  if (spec.policyApplied === 'FULL_PERIOD') return `${period}完整周期（FULL_PERIOD）`
  if (spec.periodFallbackApplied) return `已回退至上一完整${grainLabels[spec.grain.replace('FISCAL_', '')] ?? '周期'}（LAST_COMPLETE）`
  return `${period}上一完整周期（LAST_COMPLETE）`
}

/** The browser equivalent of Go answer.RenderTimeSpec. Keep strings fixture-locked. */
export function renderTimeSpec(spec: ResolvedTimeSpec, options: RenderTimeSpecOptions = {}): TimeSpecView {
  if ((options.locale ?? 'zh-CN') !== 'zh-CN' || !validateSpec(spec)) return emptyView()
  const start = dateInTimeZone(spec.resolvedStart, spec.timezone)
  const end = previousDate(dateInTimeZone(spec.resolvedEndExclusive, spec.timezone))
  const asOf = dateInTimeZone(spec.dataAvailableThrough, spec.timezone)
  let comparisonLabel = ''
  if (spec.comparison) {
    const comparisonStart = dateInTimeZone(spec.comparison.resolvedStart, spec.timezone)
    const comparisonEnd = previousDate(dateInTimeZone(spec.comparison.resolvedEndExclusive, spec.timezone))
    const alignment = spec.comparison.alignment === 'SAME_DAY_COUNT' ? '按相同天数对齐' : '按相同自然日期对齐'
    comparisonLabel = `对比期 ${comparisonStart} 至 ${comparisonEnd}，${alignment}`
    if (spec.comparison.overflowApplied) comparisonLabel += '，月末已对齐至最后一天'
  }
  return {
    rangeLabel: `${start} 至 ${end}`,
    asOfLabel: `数据截止 ${asOf}`,
    policyLabel: policyLabel(spec),
    comparisonLabel,
    truncatedHint: spec.truncatedByDataAvailability ? `数据仅更新至 ${asOf}，结果已按可用范围裁剪` : '',
  }
}

export function timeSpecSummaryLabel(view: TimeSpecView): string {
  return [view.policyLabel, view.rangeLabel, view.asOfLabel].filter(Boolean).join(' · ')
}

export function timePolicySourceLabel(spec: ResolvedTimeSpec): string {
  return policySourceLabels[spec.policySource] ?? ''
}
