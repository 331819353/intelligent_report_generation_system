import type { QuestionAnswerPresentation } from '../lib/ask-data-api.ts'

export const degradedAnswerHint = '本次未生成文字结论，请查看数据与口径。'
export const degradedAnswerReason = '文字结论连续两次未通过事实校验，系统已隐藏相关文字。数据、图表、明细与口径不受影响。'

export type AnswerSummarySnapshot = {
  status: [string, string]
  headline: string
  message: string
  layers: [string, string, string]
}

// Deterministic presentation contract used by the component and snapshot test.
export function answerSummarySnapshot(answer: QuestionAnswerPresentation): AnswerSummarySnapshot {
  if (answer.narrativeDegraded) {
    return {
      status: ['结构化结果已核验', '文字结论未展示'],
      headline: '为什么没有自动结论？',
      message: answer.hint || degradedAnswerHint,
      layers: ['L1 结构化结果 · 已展示', 'L2 文字结论 · 已隐藏', 'L3 业务解读 · 问数默认关闭'],
    }
  }
  return {
    status: ['结构化结果已核验', '文字结论已核验'],
    headline: '已核验结论',
    message: answer.narrative?.summary ?? '',
    layers: ['L1 结构化结果 · 已展示', 'L2 文字结论 · 已展示', 'L3 业务解读 · 问数默认关闭'],
  }
}
