import type { ReportAsset } from '../report/assets/model.ts'
import { lifecycleLabels } from '../report/assets/model.ts'
import type { ConversationSummary, DecisionSummary, WorkInboxItem } from './home-api.ts'

export type HomeWorkKind = 'question' | 'report' | 'decision'
export type HomeWorkItem = {
  id: string
  title: string
  meta: string
  viewedAt: string
  range: string
  kind: HomeWorkKind
  href?: string
}

export type HomeTaskPriority = 'high' | 'medium' | 'low'
export type HomeTaskItem = {
  id: string
  source: WorkInboxItem
  title: string
  summary: string
  due: string
  owner: string
  priority: HomeTaskPriority
  href?: string
}

const conversationStateLabels: Record<string, string> = {
  ANSWERED: '已完成',
  CLARIFICATION_REQUIRED: '待澄清',
  BLOCKED: '已阻断',
  CLARIFICATION_EXPIRED: '澄清已过期',
}

const decisionStatusLabels: Record<string, string> = {
  DRAFT: '草稿',
  IN_REVIEW: '待审批',
  APPROVED: '已审批',
  REJECTED: '已驳回',
  IN_EXECUTION: '执行中',
  REVIEW_DUE: '待复盘',
  CLOSED: '已关闭',
  REOPENED: '已重开',
  CANCELED: '已取消',
}

const workTypeLabels: Record<string, string> = {
  DOMAIN_ACCESS_APPROVAL: '领域访问申请',
  DATA_SOURCE_PUBLICATION: '数据源发布申请',
  DATASET_PUBLICATION: '数据集发布申请',
  DATA_REQUEST: '取数申请',
  FEEDBACK_TICKET: '反馈工单',
  DECISION_APPROVAL: '决策审批',
  ACTION_ASSIGNED: '决策行动',
  DECISION_REVIEW_DUE: '决策复盘',
  REPORT_EXPORT_FAILED: '报告导出失败',
}

export function formatHomeTime(value: string, now = new Date()) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间未知'
  const difference = now.getTime() - date.getTime()
  if (difference >= 0 && difference < 60_000) return '刚刚'
  if (difference >= 0 && difference < 60 * 60_000) return `${Math.max(1, Math.floor(difference / 60_000))} 分钟前`
  const sameDay = date.getFullYear() === now.getFullYear() && date.getMonth() === now.getMonth() && date.getDate() === now.getDate()
  const time = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false }).format(date)
  if (sameDay) return `今天 ${time}`
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (date.getFullYear() === yesterday.getFullYear() && date.getMonth() === yesterday.getMonth() && date.getDate() === yesterday.getDate()) return `昨天 ${time}`
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(date).replaceAll('/', '-')
}

export function conversationToHomeWork(item: ConversationSummary, domainName: string): HomeWorkItem {
  const status = item.releaseDrifted ? '口径有更新' : item.clarificationPending ? '待澄清' : conversationStateLabels[item.state] ?? '进行中'
  return {
    id: item.conversationId,
    title: item.label || '分析会话',
    meta: `问数 · ${domainName || '当前领域'}`,
    viewedAt: formatHomeTime(item.updatedAt),
    range: item.runCount > 1 ? `${item.runCount} 轮 · ${status}` : status,
    kind: 'question',
    href: `/ask-data?runId=${encodeURIComponent(item.latestRunId)}`,
  }
}

export function reportToHomeWork(item: ReportAsset): HomeWorkItem {
  return {
    id: item.id,
    title: item.name,
    meta: `${item.reportType === 'DASHBOARD' ? '看板' : '报告'} · ${lifecycleLabels[item.lifecycle]}`,
    viewedAt: formatHomeTime(item.updatedAt),
    range: item.currentVersionNo ? `已发布 v${item.currentVersionNo}` : `草稿 r${item.draftRevisionNo}`,
    kind: 'report',
    href: `/reports/${encodeURIComponent(item.id)}`,
  }
}

export function decisionToHomeWork(item: DecisionSummary): HomeWorkItem {
  const reviewAt = new Date(item.reviewAt)
  return {
    id: item.id,
    title: item.title,
    meta: `决策 · ${decisionStatusLabels[item.status] ?? item.status}`,
    viewedAt: formatHomeTime(item.updatedAt),
    range: Number.isNaN(reviewAt.getTime()) ? '复盘时间待定' : `复盘 ${new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit' }).format(reviewAt)}`,
    kind: 'decision',
  }
}

export function workItemDestination(item: WorkInboxItem): string | undefined {
  switch (item.type) {
    case 'DOMAIN_ACCESS_APPROVAL': return '/domain-access'
    case 'DATA_SOURCE_PUBLICATION': return '/data-sources'
    case 'DATASET_PUBLICATION': return '/datasets'
    case 'DATA_REQUEST': return '/ask-data?workspace=data-requests'
    case 'REPORT_EXPORT_FAILED': return /^\/reports\/[0-9a-f-]+$/i.test(item.sourceHref) ? item.sourceHref : '/reports'
    default: return undefined
  }
}

export function workItemToHomeTask(item: WorkInboxItem, now = new Date()): HomeTaskItem {
  const due = item.slaDueAt ? new Date(item.slaDueAt) : undefined
  const remaining = due && !Number.isNaN(due.getTime()) ? due.getTime() - now.getTime() : undefined
  const priority: HomeTaskPriority = item.overdue || remaining !== undefined && remaining <= 48 * 60 * 60_000
    ? 'high'
    : remaining !== undefined && remaining <= 7 * 24 * 60 * 60_000
      ? 'medium'
      : 'low'
  const requester = item.requesterUserId ? `${item.requesterUserId.slice(0, 8)}…` : '系统'
  return {
    id: `${item.type}:${item.objectId}`,
    source: item,
    title: workTypeLabels[item.type] ?? '待处理事项',
    summary: item.summary,
    due: due && !Number.isNaN(due.getTime())
      ? `${item.overdue ? '已逾期' : '截止'}：${new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(due)}`
      : '未设置 SLA 截止时间',
    owner: `发起人 ${requester}`,
    priority,
    href: workItemDestination(item),
  }
}

export function workTypeLabel(type: string) {
  return workTypeLabels[type] ?? '待办'
}
