import type {
  QuestionRunEvent,
  QuestionRunState,
} from '../../lib/ask-data-api.ts'

export type ConversationProgressStatus = 'complete' | 'active' | 'pending' | 'blocked'

export type ConversationProgressItem = {
  key: string
  label: string
  detail: string
  status: ConversationProgressStatus
  timestamp?: string
}

type ProgressDefinition = {
  key: string
  label: string
  detail: string
  activatesAt: QuestionRunState[]
  completesBefore: number
}

const stateRank: Record<QuestionRunState, number> = {
  RECEIVED: 0,
  AUTHORIZED: 1,
  CONTEXT_READY: 2,
  UNDERSTANDING: 3,
  RETRIEVING: 4,
  BINDING: 5,
  GRAPH_VALIDATING: 6,
  IR_READY: 7,
  PLAN_VALIDATING: 8,
  EXECUTING: 9,
  RESULT_VERIFYING: 10,
  ANSWER_VERIFYING: 11,
  CLARIFICATION_REQUIRED: 12,
  CLARIFICATION_EXPIRED: 12,
  ANSWERED: 12,
  BLOCKED: 12,
}

const progressDefinitions: ProgressDefinition[] = [
  {
    key: 'received',
    label: '已接收',
    detail: '已接收问题，开始创建分析运行',
    activatesAt: ['RECEIVED'],
    completesBefore: 1,
  },
  {
    key: 'authorized',
    label: '权限校验',
    detail: '已通过权限校验，具备访问所需数据的权限',
    activatesAt: ['AUTHORIZED'],
    completesBefore: 2,
  },
  {
    key: 'understanding',
    label: '理解问题',
    detail: '正在识别问题意图、指标、维度与时间范围',
    activatesAt: ['CONTEXT_READY', 'UNDERSTANDING'],
    completesBefore: 4,
  },
  {
    key: 'retrieving',
    label: '语义检索',
    detail: '正在检索相关指标、维度与已发布数据资产',
    activatesAt: ['RETRIEVING'],
    completesBefore: 5,
  },
  {
    key: 'binding',
    label: '确认口径',
    detail: '正在对齐业务口径、对象版本与筛选条件',
    activatesAt: ['BINDING'],
    completesBefore: 6,
  },
  {
    key: 'graph',
    label: '验证关系',
    detail: '正在验证指标、维度与认证数据链路的关系',
    activatesAt: ['GRAPH_VALIDATING'],
    completesBefore: 7,
  },
  {
    key: 'plan',
    label: '生成计划',
    detail: '正在生成并校验确定性的查询执行计划',
    activatesAt: ['IR_READY', 'PLAN_VALIDATING'],
    completesBefore: 9,
  },
  {
    key: 'execution',
    label: '执行查询',
    detail: '正在查询已发布数据资产并计算结果',
    activatesAt: ['EXECUTING'],
    completesBefore: 10,
  },
  {
    key: 'verification',
    label: '核验答案',
    detail: '正在核验结果与生成文字的事实一致性',
    activatesAt: ['RESULT_VERIFYING', 'ANSWER_VERIFYING'],
    completesBefore: 12,
  },
  {
    key: 'complete',
    label: '完成',
    detail: '生成分析结论并展示受控结果',
    activatesAt: ['ANSWERED', 'CLARIFICATION_REQUIRED', 'CLARIFICATION_EXPIRED', 'BLOCKED'],
    completesBefore: 13,
  },
]

function formatTimestamp(value: string | undefined): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return undefined
  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(date)
}

function eventForDefinition(
  definition: ProgressDefinition,
  events: QuestionRunEvent[],
): QuestionRunEvent | undefined {
  return [...events].reverse().find(event => definition.activatesAt.includes(event.state))
}

export function buildConversationProgress(
  currentState: QuestionRunState,
  events: QuestionRunEvent[],
): ConversationProgressItem[] {
  const currentRank = stateRank[currentState]
  const terminalBlocked = currentState === 'BLOCKED' || currentState === 'CLARIFICATION_REQUIRED' || currentState === 'CLARIFICATION_EXPIRED'

  return progressDefinitions.map(definition => {
    const event = eventForDefinition(definition, events)
    const active = definition.activatesAt.includes(currentState)
    const completed = currentRank >= definition.completesBefore
    const status: ConversationProgressStatus = active && terminalBlocked
      ? 'blocked'
      : active && currentState !== 'ANSWERED'
        ? 'active'
        : completed || currentState === 'ANSWERED' && definition.key === 'complete'
          ? 'complete'
          : 'pending'

    return {
      key: definition.key,
      label: definition.label,
      detail: definition.detail,
      status,
      timestamp: formatTimestamp(event?.createdAt),
    }
  })
}

export function latestQuestionState(
  fallback: QuestionRunState,
  events: QuestionRunEvent[],
): QuestionRunState {
  return events.at(-1)?.state ?? fallback
}
