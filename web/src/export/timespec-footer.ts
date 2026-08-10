import { renderTimeSpec } from '../askdata/format/timespec.ts'
import type { ResolvedTimeSpec } from '../lib/ask-data-api'

/** Returns bounded, already-rendered rows for CSV/PDF export footers. */
export function exportTimeSpecFooter(spec: ResolvedTimeSpec): string[][] {
  const view = renderTimeSpec(spec)
  return [
    ['时间口径', view.policyLabel],
    ['实际区间', view.rangeLabel],
    ['数据截止', view.asOfLabel],
    ...(view.comparisonLabel ? [['对比口径', view.comparisonLabel]] : []),
    ...(view.truncatedHint ? [['提示', view.truncatedHint]] : []),
  ]
}
