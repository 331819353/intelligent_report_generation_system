import { Brain, CheckCircle, ListChecks, SlidersHorizontal, Sparkle, X } from '@phosphor-icons/react'
import { useMemo, useState } from 'react'

import type { AngleInsightConfig, ReportComponent, Section } from '../render/schema.ts'
import {
  angleInsightSubsections, effectiveAngleInsightConfig, equalAngleInsightItems, validateAngleInsightConfig,
} from './angle-insight-config.ts'

const layoutLabels: Record<string, string> = {
  CONCLUSION_TOP: '结论上置',
  CONCLUSION_LEFT: '结论左置',
}

export function AngleInsightConfigPanel({ section, component, busy, error, onClose, onSave }: {
  section: Section
  component: ReportComponent
  busy?: boolean
  error?: string
  onClose: () => void
  onSave: (config: AngleInsightConfig, generate: boolean) => void
}) {
  const subsections = useMemo(() => angleInsightSubsections(section), [section])
  const [config, setConfig] = useState<AngleInsightConfig>(() => effectiveAngleInsightConfig(component, section))
  const selected = new Set(config.analysisItems.map(item => item.subsectionId))
  const total = config.analysisItems.reduce((sum, item) => sum + item.weight, 0)
  const validationError = validateAngleInsightConfig(config)

  const updateApproach = (key: keyof AngleInsightConfig['analysisApproach'], value: string) => {
    setConfig(current => ({ ...current, analysisApproach: { ...current.analysisApproach, [key]: value } }))
  }
  const toggleSubsection = (subsectionId: string) => {
    const ids = subsections.map(block => block.id).filter(id => id === subsectionId ? !selected.has(id) : selected.has(id))
    setConfig(current => ({ ...current, analysisItems: equalAngleInsightItems(ids) }))
  }
  const updateWeight = (subsectionId: string, weight: number) => {
    setConfig(current => ({
      ...current,
      analysisItems: current.analysisItems.map(item => item.subsectionId === subsectionId ? { ...item, weight } : item),
    }))
  }

  return <section className="angle-insight-config" aria-labelledby="angle-insight-config-title">
    <header className="angle-insight-config-head">
      <span><Brain size={18} weight="duotone" /></span>
      <div><h2 id="angle-insight-config-title">智能结论配置</h2><small>{section.name} · {subsections.length} 个可用小节</small></div>
      <button type="button" aria-label="关闭智能结论配置" onClick={onClose}><X size={17} /></button>
    </header>

    <div className="angle-insight-config-scroll">
      <section className="angle-insight-config-card">
        <header><span><Sparkle size={16} weight="fill" /></span><div><strong>分析思路</strong><small>这些内容会作为本结论的专用 LLM 指令</small></div></header>
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
        <header><span><ListChecks size={16} /></span><div><strong>分析项</strong><small>选择本分析角度下参与结论生成的小节</small></div></header>
        <div className="angle-insight-items">
          {subsections.map((block, index) => {
            const item = config.analysisItems.find(candidate => candidate.subsectionId === block.id)
            const checked = Boolean(item)
            const layout = block.cardKind?.replace('LAYOUT_SUBSECTION_', '') ?? ''
            return <article className={checked ? 'is-selected' : ''} key={block.id}>
              <label className="angle-insight-item-select">
                <input type="checkbox" checked={checked} onChange={() => toggleSubsection(block.id)} />
                <span><CheckCircle size={17} weight={checked ? 'fill' : 'regular'} /></span>
                <span><strong>{block.title || `小节 ${index + 1}`}</strong><small>{layoutLabels[layout] ?? layout}</small></span>
              </label>
              {item && <label className="angle-insight-item-weight"><span>权重</span>
                <span><input type="number" min={1} max={100} step={1} value={item.weight}
                  aria-label={`${block.title || `小节 ${index + 1}`}权重`}
                  onChange={event => updateWeight(block.id, Number(event.target.value))} /><em>%</em></span>
              </label>}
            </article>
          })}
        </div>
        <div className={`angle-insight-weight-total ${total === 100 ? 'is-valid' : 'is-invalid'}`}>
          <SlidersHorizontal size={15} /><span>权重合计</span><strong>{total}%</strong>
          <button type="button" disabled={config.analysisItems.length === 0}
            onClick={() => setConfig(current => ({ ...current, analysisItems: equalAngleInsightItems(current.analysisItems.map(item => item.subsectionId)) }))}>均分权重</button>
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
