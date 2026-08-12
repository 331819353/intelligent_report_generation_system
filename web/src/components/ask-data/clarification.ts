import type { ClarificationEvidence, ClarificationOption } from '../../lib/ask-data-api'

export function clarificationOptionReady(option: ClarificationOption | undefined): boolean {
  const evidence = option?.evidence
  if (!option || option.evidenceIds.length === 0 || !option.optionId.trim() || !option.label.trim()) return false
  // Every submitted option is re-checked by the API against the immutable
  // completion artifact and its governed evidence references. Detailed public
  // evidence is optional enrichment: time-range and retry choices often have
  // policy evidence without a metric-owner card, and must not dead-end here.
  if (!evidence) return true
  if (!evidence.definition.trim()) return false
  if (!evidence.owner.id.trim() || !evidence.owner.displayName.trim()) return false
  if (!evidence.semanticVersion.trim() || !evidence.semanticStatus.trim()) return false
  if (!evidence.time.label.trim() || !evidence.time.start.trim() || !evidence.time.end.trim() || !evidence.time.timezone.trim()) return false
  if (!evidence.quality.dataAsOf.trim() || evidence.quality.rulesPassed < 0 || evidence.quality.rulesTotal < evidence.quality.rulesPassed) return false
  const score = evidence.quality.scorePermillion
  return score === undefined || Number.isInteger(score) && score >= 0 && score <= 1_000_000
}

export function qualityScoreLabel(option: ClarificationOption | undefined): string {
  const score = option?.evidence?.quality.scorePermillion
  return score === undefined ? '—' : (score / 10_000).toFixed(1)
}

export function qualityStatusLabel(status: ClarificationEvidence['quality']['status'] | undefined): string {
  if (status === 'PASS') return '通过'
  if (status === 'WARNING') return '需关注'
  if (status === 'FAIL') return '未通过'
  return '未知'
}

export function semanticStatusLabel(status: string | undefined): string {
  if (status === 'CERTIFIED' || status === 'PUBLISHED' || status === 'ACTIVE') return '已发布'
  return status || '状态未知'
}

export function timeRangeLabel(option: ClarificationOption | undefined): string {
  const time = option?.evidence?.time
  return time ? `${time.label} · ${time.start} 至 ${time.end}` : '时间证据未发布'
}

export function freshnessLabel(option: ClarificationOption | undefined): string {
  const value = option?.evidence?.quality.dataAsOf
  if (!value) return '未发布'
  const matched = value.match(/T(\d{2}:\d{2})/)
  return matched ? `${matched[1]} 更新` : value
}
