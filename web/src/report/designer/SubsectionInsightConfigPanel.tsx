import { Brain, CheckCircle, ListChecks, SlidersHorizontal, Sparkle, X } from '@phosphor-icons/react'
import { useMemo, useState } from 'react'

import type { Block, ReportComponent, SubsectionInsightConfig } from '../render/schema.ts'
import { frameSlotLabels } from './operations.ts'
import {
  effectiveSubsectionInsightConfig, equalSubsectionInsightItems, subsectionInsightCandidates, validateSubsectionInsightConfig,
} from './subsection-insight-config.ts'

export function SubsectionInsightConfigPanel({ block, component, components, busy, error, onClose, onSave }: {
  block: Block
  component: ReportComponent
  components: ReportComponent[]
  busy?: boolean
  error?: string
  onClose: () => void
  onSave: (config: SubsectionInsightConfig, generate: boolean) => void
}) {
  const candidates = useMemo(() => subsectionInsightCandidates(block, components), [block, components])
  const [config, setConfig] = useState<SubsectionInsightConfig>(() => effectiveSubsectionInsightConfig(component, candidates))
  const selected = new Set(config.analysisItems.map(item => item.componentId))
  const total = config.analysisItems.reduce((sum, item) => sum + item.weight, 0)
  const validationError = validateSubsectionInsightConfig(config)

  const updateApproach = (key: keyof SubsectionInsightConfig['analysisApproach'], value: string) => {
    setConfig(current => ({ ...current, analysisApproach: { ...current.analysisApproach, [key]: value } }))
  }
  const toggleCandidate = (componentId: string) => {
    const ids = candidates.map(item => item.componentId).filter(id => id === componentId ? !selected.has(id) : selected.has(id))
    setConfig(current => ({ ...current, analysisItems: equalSubsectionInsightItems(ids) }))
  }
  const updateWeight = (componentId: string, weight: number) => {
    setConfig(current => ({
      ...current,
      analysisItems: current.analysisItems.map(item => item.componentId === componentId ? { ...item, weight } : item),
    }))
  }

  return <section className="angle-insight-config" aria-labelledby="subsection-insight-config-title">
    <header className="angle-insight-config-head">
      <span><Brain size={18} weight="duotone" /></span>
      <div><h2 id="subsection-insight-config-title">小节智能结论配置</h2><small>{block.title || '未命名小节'} · {candidates.length} 个可用内容项</small></div>
      <button type="button" aria-label="关闭小节智能结论配置" onClick={onClose}><X size={17} /></button>
    </header>

    <div className="angle-insight-config-scroll">
      <section className="angle-insight-config-card">
        <header><span><Sparkle size={16} weight="fill" /></span><div><strong>分析思路</strong><small>这些内容会作为本小节结论的专用 LLM 指令</small></div></header>
        <label><span>如何分析</span><small>告诉模型分析步骤与组织方式</small>
          <textarea rows={4} maxLength={4096} value={config.analysisApproach.howToAnalyze}
            onChange={event => updateApproach('howToAnalyze', event.target.value)} />
        </label>
        <label><span>应该分析什么</span><small>限定需要回答的问题与关注点</small>
          <textarea rows={4} maxLength={4096} value={config.analysisApproach.analyzeWhat}
            onChange={event => updateApproach('analyzeWhat', event.target.value)} />
        </label>
        <label><span>不应该分析什么</span><small>明确事实边界、排除项与禁止推断</small>
          <textarea rows={4} maxLength={4096} value={config.analysisApproach.doNotAnalyze}
            onChange={event => updateApproach('doNotAnalyze', event.target.value)} />
        </label>
        <label><span>输出示例</span><small>仅作为表达结构示例，不会被当作事实</small>
          <textarea rows={5} maxLength={4096} value={config.analysisApproach.outputExample}
            onChange={event => updateApproach('outputExample', event.target.value)} />
        </label>
      </section>

      <section className="angle-insight-config-card is-items">
        <header><span><ListChecks size={16} /></span><div><strong>分析项</strong><small>默认使用本小节全部图表；仅在需要覆盖默认策略时调整</small></div></header>
        <div className="angle-insight-items">
          {candidates.map(candidate => {
            const item = config.analysisItems.find(value => value.componentId === candidate.componentId)
            const checked = Boolean(item)
            return <article className={checked ? 'is-selected' : ''} key={candidate.componentId}>
              <label className="angle-insight-item-select">
                <input type="checkbox" checked={checked} onChange={() => toggleCandidate(candidate.componentId)} />
                <span><CheckCircle size={17} weight={checked ? 'fill' : 'regular'} /></span>
                <span><strong>{candidate.title}</strong><small>{frameSlotLabels[candidate.role]} · {candidate.type}</small></span>
              </label>
              {item && <label className="angle-insight-item-weight"><span>权重</span>
                <span><input type="number" min={1} max={100} step={1} value={item.weight}
                  aria-label={`${candidate.title}权重`} onChange={event => updateWeight(candidate.componentId, Number(event.target.value))} /><em>%</em></span>
              </label>}
            </article>
          })}
        </div>
        <div className={`angle-insight-weight-total ${total === 100 ? 'is-valid' : 'is-invalid'}`}>
          <SlidersHorizontal size={15} /><span>权重合计</span><strong>{total}%</strong>
          <button type="button" disabled={config.analysisItems.length === 0}
            onClick={() => setConfig(current => ({ ...current, analysisItems: equalSubsectionInsightItems(current.analysisItems.map(item => item.componentId)) }))}>均分权重</button>
        </div>
      </section>
    </div>

    <footer className="angle-insight-config-footer">
      {(error || validationError) && <p role="alert">{error || validationError}</p>}
      <div>
        <button type="button" className="quiet-button" disabled={busy || Boolean(validationError)} onClick={() => onSave(config, false)}>仅保存配置</button>
        <button type="button" className="primary-button" disabled={busy || Boolean(validationError)} onClick={() => onSave(config, true)}>
          <Sparkle className={busy ? 'is-spinning' : ''} size={15} weight="fill" />{busy ? '正在生成…' : '应用配置并生成'}
        </button>
      </div>
    </footer>
  </section>
}
