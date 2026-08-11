import type { ConversationSummary, QuestionRunState } from './ask-data-api'

export type ConversationGroup = { label: string; items: ConversationSummary[] }

export const conversationStateLabel: Record<QuestionRunState, string> = {
  RECEIVED: '已接收', AUTHORIZED: '已授权', CONTEXT_READY: '上下文就绪', UNDERSTANDING: '理解中',
  RETRIEVING: '检索中', BINDING: '绑定中', GRAPH_VALIDATING: '关系校验', IR_READY: '查询就绪',
  PLAN_VALIDATING: '计划校验', EXECUTING: '查询中', RESULT_VERIFYING: '结果校验', ANSWER_VERIFYING: '答案校验',
  CLARIFICATION_REQUIRED: '待确认', ANSWERED: '已完成', BLOCKED: '已阻断', CLARIFICATION_EXPIRED: '已超时',
}

export function formatConversationTime(value: string, now = new Date()): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间未知'
  const sameDay = date.getFullYear() === now.getFullYear() && date.getMonth() === now.getMonth() && date.getDate() === now.getDate()
  const time = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false }).format(date)
  if (sameDay) return time
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit' }).format(date).replace('/', '-')
}

function dayStart(value: Date): number {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime()
}

export function groupConversations(items: ConversationSummary[], now = new Date()): ConversationGroup[] {
  const groups = new Map<string, ConversationSummary[]>([['置顶', []], ['今天', []], ['昨天', []], ['更早', []]])
  const today = dayStart(now)
  items.forEach(item => {
    if (item.pinned) {
      groups.get('置顶')?.push(item)
      return
    }
    const updated = new Date(item.updatedAt)
    const difference = today - dayStart(updated)
    const label = difference === 0 ? '今天' : difference === 86_400_000 ? '昨天' : '更早'
    groups.get(label)?.push(item)
  })
  return [...groups].map(([label, groupItems]) => ({ label, items: groupItems })).filter(group => group.items.length > 0)
}
