import { renderTimeSpec } from '../../askdata/format/timespec.ts'
import type { ResolvedTimeSpec } from '../../lib/ask-data-api'

/** The report runtime's only supported time subtitle boundary. */
export function reportTimeSpecSubtitle(spec: ResolvedTimeSpec): string {
  const view = renderTimeSpec(spec)
  return [view.policyLabel, view.rangeLabel, view.asOfLabel].filter(Boolean).join(' · ')
}
