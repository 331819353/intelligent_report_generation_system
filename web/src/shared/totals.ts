export type Additivity = 'FULLY_ADDITIVE' | 'SEMI_ADDITIVE' | 'NON_ADDITIVE'
export type ResultColumnRole = 'DIMENSION' | 'METRIC' | 'TIME'

export type ResultColumn = {
  name: string
  role: ResultColumnRole
  metricVersionId?: string
  additivity?: Additivity
  totalsNotSummable: boolean
  recomputedTotal?: string
  unit?: string
  currency?: string
  displayPrecision: number
}

export type TotalsBehavior =
  | { mode: 'SUM' }
  | { mode: 'RECOMPUTED'; value: string; note: string }
  | { mode: 'HIDDEN'; note: string }

export type ComponentAdditivityContract = {
  stackingRequiresAdditive: boolean
}

export type ComponentAvailability =
  | { allowed: true }
  | { allowed: false; reason: string }

const exactDecimal = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/
const maxExactDecimalLength = 512

const recomputedNote = '合计为重算值，不等于各行相加'
const hiddenNote = '该指标不可直接相加，且当前没有可用的重算合计'
const nonMetricNote = '非指标列不计算合计'
const additiveRequiredNote = '该图表要求完全可加指标，当前指标不支持堆叠或占比展示'

export function isExactDecimal(value: string): boolean {
  return value.length <= maxExactDecimalLength && exactDecimal.test(value)
}

export function resolveTotalsBehavior(col: ResultColumn): TotalsBehavior {
  if (col.role !== 'METRIC') return { mode: 'HIDDEN', note: nonMetricNote }
  if (col.additivity === 'FULLY_ADDITIVE' && !col.totalsNotSummable) return { mode: 'SUM' }
  if (col.totalsNotSummable && col.recomputedTotal !== undefined && isExactDecimal(col.recomputedTotal)) {
    return { mode: 'RECOMPUTED', value: col.recomputedTotal, note: recomputedNote }
  }
  return { mode: 'HIDDEN', note: hiddenNote }
}

export function resolveComponentAvailability(
  manifest: ComponentAdditivityContract,
  columns: readonly ResultColumn[],
): ComponentAvailability {
  if (!manifest.stackingRequiresAdditive) return { allowed: true }
  const metrics = columns.filter(column => column.role === 'METRIC')
  if (metrics.length > 0 && metrics.every(column =>
    column.additivity === 'FULLY_ADDITIVE' && !column.totalsNotSummable)) {
    return { allowed: true }
  }
  return { allowed: false, reason: additiveRequiredNote }
}

type ParsedDecimal = { coefficient: bigint; scale: number }

function parseExactDecimal(value: string): ParsedDecimal | undefined {
  if (!isExactDecimal(value)) return undefined
  const negative = value.startsWith('-')
  const unsigned = negative ? value.slice(1) : value
  const [integer, fraction = ''] = unsigned.split('.')
  const coefficient = BigInt(`${negative ? '-' : ''}${integer}${fraction}`)
  return { coefficient, scale: fraction.length }
}

function powerOfTen(exponent: number): bigint {
  return 10n ** BigInt(exponent)
}

// sumExactDecimals is the only client-side SUM primitive used by table,
// report and export adapters. It never converts governed decimal strings to
// Number, so values such as 0.1 + 0.2 remain exact.
export function sumExactDecimals(values: readonly (string | null)[]): string | undefined {
  const parsed: ParsedDecimal[] = []
  for (const value of values) {
    if (value === null) continue
    const item = parseExactDecimal(value)
    if (!item) return undefined
    parsed.push(item)
  }
  if (parsed.length === 0) return undefined
  let scale = 0
  for (const item of parsed) scale = Math.max(scale, item.scale)
  const total = parsed.reduce(
    (sum, item) => sum + item.coefficient * powerOfTen(scale - item.scale),
    0n,
  )
  const negative = total < 0n
  const digits = (negative ? -total : total).toString().padStart(scale + 1, '0')
  if (scale === 0) return `${negative ? '-' : ''}${digits}`
  const integer = digits.slice(0, -scale)
  const fraction = digits.slice(-scale).replace(/0+$/, '')
  if (!fraction) return `${negative ? '-' : ''}${integer}`
  return `${negative ? '-' : ''}${integer}.${fraction}`
}

export function resolveTotalValue(
  column: ResultColumn,
  rowValues: readonly (string | null)[],
): string | undefined {
  const behavior = resolveTotalsBehavior(column)
  if (behavior.mode === 'RECOMPUTED') return behavior.value
  if (behavior.mode === 'SUM') return sumExactDecimals(rowValues)
  return undefined
}
