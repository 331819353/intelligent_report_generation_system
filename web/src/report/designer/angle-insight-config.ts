import type { AngleInsightConfig, ReportComponent, Section } from '../render/schema.ts'

export const defaultAngleInsightApproach: AngleInsightConfig['analysisApproach'] = {
  howToAnalyze: '逐项阅读所选小节的结论、指标、维度、过滤条件与证据，识别相互印证、差异和证据缺口；按照权重分配分析篇幅。',
  analyzeWhat: '分析所选小节已经明确表达的核心发现、关联关系、风险与可执行建议，并形成分析角度级综合结论。',
  doNotAnalyze: '不得补造未提供的指标值、趋势、因果关系、对比结论或业务事实；不得分析未选择的小节；不得把模板占位文案当作真实结论。',
  outputExample: '综合结论：……\n核心发现：……\n风险提示：……\n建议动作：……',
}

export function angleInsightSubsections(section: Section) {
  return section.blocks.filter(block => block.cardKind?.startsWith('LAYOUT_SUBSECTION_'))
}

export function equalAngleInsightItems(subsectionIds: string[]) {
  if (subsectionIds.length === 0) return []
  const base = Math.floor(100 / subsectionIds.length)
  const remainder = 100 % subsectionIds.length
  return subsectionIds.map((subsectionId, index) => ({ subsectionId, weight: base + (index < remainder ? 1 : 0) }))
}

export function defaultAngleInsightConfig(section: Section): AngleInsightConfig {
  return {
    analysisApproach: { ...defaultAngleInsightApproach },
    analysisItems: equalAngleInsightItems(angleInsightSubsections(section).map(block => block.id)),
  }
}

export function effectiveAngleInsightConfig(component: ReportComponent, section: Section): AngleInsightConfig {
  const available = new Set(angleInsightSubsections(section).map(block => block.id))
  const persisted = component.options.angleInsightConfig
  if (!persisted) return defaultAngleInsightConfig(section)
  const selected = persisted.analysisItems.filter(item => available.has(item.subsectionId))
  const total = selected.reduce((sum, item) => sum + item.weight, 0)
  return {
    analysisApproach: {
      howToAnalyze: persisted.analysisApproach.howToAnalyze || defaultAngleInsightApproach.howToAnalyze,
      analyzeWhat: persisted.analysisApproach.analyzeWhat || defaultAngleInsightApproach.analyzeWhat,
      doNotAnalyze: persisted.analysisApproach.doNotAnalyze || defaultAngleInsightApproach.doNotAnalyze,
      outputExample: persisted.analysisApproach.outputExample || defaultAngleInsightApproach.outputExample,
    },
    analysisItems: selected.length > 0 && total === 100
      ? selected.map(item => ({ ...item }))
      : equalAngleInsightItems((selected.length > 0 ? selected.map(item => item.subsectionId) : [...available])),
  }
}

export function validateAngleInsightConfig(config: AngleInsightConfig) {
  const approach = config.analysisApproach
  if ([approach.howToAnalyze, approach.analyzeWhat, approach.doNotAnalyze, approach.outputExample].some(value => !value.trim())) {
    return '请完整填写分析思路的四项内容'
  }
  if (config.analysisItems.length === 0) return '请至少选择一个分析小节'
  if (config.analysisItems.some(item => !Number.isInteger(item.weight) || item.weight < 1 || item.weight > 100)) {
    return '每个分析项权重必须是 1–100 的整数'
  }
  const total = config.analysisItems.reduce((sum, item) => sum + item.weight, 0)
  return total === 100 ? '' : `分析项权重合计必须为 100%，当前为 ${total}%`
}
