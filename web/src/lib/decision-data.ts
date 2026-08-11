import type { DecisionAction, DecisionEvidenceMode, DecisionRecord, DecisionStatus } from './decision-api'

export const decisionStatusMeta: Record<DecisionStatus, { label: string; tone: string }> = {
  DRAFT: { label: '草稿', tone: 'neutral' },
  IN_REVIEW: { label: '待审批', tone: 'amber' },
  APPROVED: { label: '已批准', tone: 'green' },
  REJECTED: { label: '已驳回', tone: 'red' },
  IN_EXECUTION: { label: '执行中', tone: 'blue' },
  REVIEW_DUE: { label: '待复盘', tone: 'purple' },
  CLOSED: { label: '已关闭', tone: 'neutral' },
  REOPENED: { label: '已重开', tone: 'blue' },
  CANCELED: { label: '已取消', tone: 'neutral' },
}

export const decisionEvidenceMeta: Record<DecisionEvidenceMode, { label: string; shortLabel: string; verified: boolean }> = {
  PLATFORM_VERIFIED: { label: '平台已验证证据', shortLabel: '已验证', verified: true },
  MANUAL_WITHOUT_PLATFORM_EVIDENCE: { label: '无平台证据的手工决策', shortLabel: '未验证', verified: false },
}

export function decisionOwnerLabel(ownerUserId: string, currentUserId: string) {
  if (currentUserId && ownerUserId === currentUserId) return '我'
  const suffix = ownerUserId.replaceAll('-', '').slice(-6)
  return suffix ? `用户 ···${suffix}` : '负责人不可用'
}

export function formatDecisionDate(value: string, withTime = false) {
  const parsed = new Date(value)
  if (Number.isNaN(parsed.valueOf())) return '—'
  return new Intl.DateTimeFormat('zh-CN', withTime
    ? { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }
    : { year: 'numeric', month: '2-digit', day: '2-digit' }).format(parsed).replaceAll('/', '-')
}

export function actionProgress(actions: DecisionAction[]) {
  const active = actions.filter(item => item.status !== 'CANCELED')
  if (!active.length) return { completed: 0, total: 0, percent: 0 }
  const completed = active.filter(item => item.status === 'DONE').length
  return { completed, total: active.length, percent: Math.round((completed / active.length) * 100) }
}

export function filterDecisions<T extends DecisionRecord>(items: T[], input: { query: string; status: string; evidenceMode: string; startDate: string; endDate: string }): T[] {
  const query = input.query.trim().toLocaleLowerCase('zh-CN')
  const start = input.startDate ? new Date(`${input.startDate}T00:00:00`).valueOf() : Number.NEGATIVE_INFINITY
  const end = input.endDate ? new Date(`${input.endDate}T23:59:59.999`).valueOf() : Number.POSITIVE_INFINITY
  return items.filter(item => {
    const reviewAt = new Date(item.reviewAt).valueOf()
    const matchesReviewWindow = !input.startDate && !input.endDate
      ? true
      : !Number.isNaN(reviewAt) && reviewAt >= start && reviewAt <= end
    return (!query || `${item.title} ${item.question} ${item.decision}`.toLocaleLowerCase('zh-CN').includes(query))
      && (!input.status || item.status === input.status)
      && (!input.evidenceMode || item.evidenceMode === input.evidenceMode)
      && matchesReviewWindow
  })
}

export function urgentDecisions(items: DecisionRecord[], now = new Date()) {
  const soon = now.valueOf() + 7 * 24 * 60 * 60 * 1000
  const open = new Set<DecisionStatus>(['DRAFT', 'IN_REVIEW', 'IN_EXECUTION', 'REVIEW_DUE', 'REOPENED'])
  return [...items]
    .filter(item => open.has(item.status) && new Date(item.reviewAt).valueOf() <= soon)
    .sort((left, right) => new Date(left.reviewAt).valueOf() - new Date(right.reviewAt).valueOf())
}
