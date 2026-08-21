import type { Block, ReportComponent, SubsectionInsightConfig } from '../render/schema.ts'
import { frameSlotLabels, frameSlotRole, type FrameSlotRole } from './operations.ts'

export type SubsectionInsightCandidate = {
  componentId: string
  title: string
  role: Exclude<FrameSlotRole, 'CONCLUSION'>
  type: string
}

export const defaultSubsectionInsightApproach: SubsectionInsightConfig['analysisApproach'] = {
  howToAnalyze: '逐项阅读所选论据、图表、明细与附录的标题、指标、维度、过滤条件和已有叙事，识别相互印证、差异与证据缺口；按照权重分配分析篇幅。',
  analyzeWhat: '分析本小节所选内容已经明确表达的核心发现、关系、风险与可执行建议，并形成小节级智能结论。',
  doNotAnalyze: '不得补造未提供的指标值、趋势、因果关系、对比结论或业务事实；不得分析未选择的内容；不得把模板占位文案当作真实结论。',
  outputExample: '小节结论：……\n核心发现：……\n风险提示：……\n建议动作：……',
}

export function subsectionInsightCandidates(block: Block, components: ReportComponent[]): SubsectionInsightCandidate[] {
  const byId = new Map(components.map(component => [component.id, component]))
  const seen = new Set<string>()
  const result: SubsectionInsightCandidate[] = []
  for (const zone of block.zones.slice().sort((left, right) => (left.order ?? 0) - (right.order ?? 0))) {
    for (const slot of zone.slots) {
      const role = frameSlotRole(slot.cardKind)
      const component = slot.componentId ? byId.get(slot.componentId) : undefined
      if (!component || !role || role === 'CONCLUSION' || seen.has(component.id)) continue
      seen.add(component.id)
      result.push({
        componentId: component.id,
        title: component.options.title?.trim() || `${frameSlotLabels[role]} ${result.length + 1}`,
        role,
        type: component.templateRef.type,
      })
    }
  }
  return result
}

export function equalSubsectionInsightItems(componentIds: string[]) {
  if (componentIds.length === 0) return []
  const base = Math.floor(100 / componentIds.length)
  const remainder = 100 % componentIds.length
  return componentIds.map((componentId, index) => ({ componentId, weight: base + (index < remainder ? 1 : 0) }))
}

export function defaultSubsectionInsightConfig(candidates: SubsectionInsightCandidate[]): SubsectionInsightConfig {
  return {
    analysisApproach: { ...defaultSubsectionInsightApproach },
    analysisItems: equalSubsectionInsightItems(candidates.map(item => item.componentId)),
  }
}

export function effectiveSubsectionInsightConfig(component: ReportComponent, candidates: SubsectionInsightCandidate[]): SubsectionInsightConfig {
  const available = new Set(candidates.map(item => item.componentId))
  const persisted = component.options.subsectionInsightConfig
  if (!persisted) return defaultSubsectionInsightConfig(candidates)
  const selected = persisted.analysisItems.filter(item => available.has(item.componentId))
  const total = selected.reduce((sum, item) => sum + item.weight, 0)
  return {
    analysisApproach: {
      howToAnalyze: persisted.analysisApproach.howToAnalyze || defaultSubsectionInsightApproach.howToAnalyze,
      analyzeWhat: persisted.analysisApproach.analyzeWhat || defaultSubsectionInsightApproach.analyzeWhat,
      doNotAnalyze: persisted.analysisApproach.doNotAnalyze || defaultSubsectionInsightApproach.doNotAnalyze,
      outputExample: persisted.analysisApproach.outputExample || defaultSubsectionInsightApproach.outputExample,
    },
    analysisItems: selected.length > 0 && total === 100
      ? selected.map(item => ({ ...item }))
      : equalSubsectionInsightItems(selected.length > 0 ? selected.map(item => item.componentId) : [...available]),
  }
}

export function validateSubsectionInsightConfig(config: SubsectionInsightConfig) {
  const approach = config.analysisApproach
  if ([approach.howToAnalyze, approach.analyzeWhat, approach.doNotAnalyze, approach.outputExample].some(value => !value.trim())) {
    return '请完整填写分析思路的四项内容'
  }
  if (config.analysisItems.length === 0) return '请至少选择一个小节内容作为分析项'
  if (new Set(config.analysisItems.map(item => item.componentId)).size !== config.analysisItems.length) return '分析项不能重复'
  if (config.analysisItems.some(item => !Number.isInteger(item.weight) || item.weight < 1 || item.weight > 100)) {
    return '每个分析项权重必须是 1–100 的整数'
  }
  const total = config.analysisItems.reduce((sum, item) => sum + item.weight, 0)
  return total === 100 ? '' : `分析项权重合计必须为 100%，当前为 ${total}%`
}
