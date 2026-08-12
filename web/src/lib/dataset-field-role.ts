/**
 * Preserve the semantic role selected on the source field when the designer
 * turns it into a grouped output. TIME is a contract role, not just a visual
 * dimension label, so it must survive a group/end round-trip.
 */
export function designerOutputRole(
  producedKind: 'ATTRIBUTE' | 'DIMENSION' | 'METRIC',
  configuredRole: string | undefined,
  graphRole: string | undefined,
  isDimension: boolean,
): string {
  if (producedKind === 'METRIC') return 'MEASURE'
  if (configuredRole === 'TIME') return 'TIME'
  return graphRole || (isDimension || producedKind === 'DIMENSION' ? 'DIMENSION' : configuredRole || 'ATTRIBUTE')
}
