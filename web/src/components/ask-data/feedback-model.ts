import type { QuestionFeedbackIssueType } from '../../lib/ask-data-api.ts'

export type FeedbackIssue = {
  type: Exclude<QuestionFeedbackIssueType, 'NONE'>
  label: string
  helper: string
}

export const FEEDBACK_ISSUES: readonly FeedbackIssue[] = [
  { type: 'METRIC', label: '指标口径', helper: '此选项用于反馈指标定义或计算范围方面的问题。' },
  { type: 'DIMENSION', label: '分析维度', helper: '用于分组或分析的维度选择不正确。' },
  { type: 'MEMBER', label: '维度值', helper: '区域、渠道、产品等具体维度值识别错误。' },
  { type: 'TIME', label: '时间范围', helper: '统计周期、实际截止日或对比区间不正确。' },
  { type: 'RELATIONSHIP', label: '关联关系', helper: '数据关联路径、粒度或业务关系不正确。' },
  { type: 'DATA', label: '数据结果', helper: '结果数值、明细或数据新鲜度存在问题。' },
  { type: 'PERMISSION', label: '权限与敏感信息', helper: '结果越权、缺少授权数据或暴露敏感信息。' },
  { type: 'EXPRESSION', label: '解释表达', helper: '结论、文字解释或表达方式不准确。' },
  { type: 'OTHER', label: '其他', helper: '以上类型未覆盖的问题。' },
] as const

export function feedbackIssue(type: QuestionFeedbackIssueType): FeedbackIssue | undefined {
  return FEEDBACK_ISSUES.find(issue => issue.type === type)
}
