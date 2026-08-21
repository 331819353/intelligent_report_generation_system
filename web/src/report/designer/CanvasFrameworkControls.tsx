import { Check, FileText, Minus, Plus, Table, X } from '@phosphor-icons/react'
import { useState } from 'react'

import type { SubsectionLayout } from './operations.ts'

export type SubsectionComposerValue = {
  layout: SubsectionLayout
  chartCount: number
  includeDetail: boolean
  includeAppendix: boolean
}

const subsectionLayouts: Array<{
  id: SubsectionLayout
  name: string
  description: string
  preview: string
}> = [
  {
    id: 'CONCLUSION_TOP',
    name: '结论上置',
    description: '结论通栏，论据图表在下方自适应排列',
    preview: '/report-framework/layout-conclusion-top.png',
  },
  {
    id: 'CONCLUSION_LEFT',
    name: '结论左置',
    description: '结论占左半区，论据图表在右侧自动成行',
    preview: '/report-framework/layout-conclusion-left.png',
  },
]

export function CanvasQuickAdd({ kind, disabled, onClick }: {
  kind: 'ANGLE' | 'SUBSECTION'
  disabled?: boolean
  onClick: () => void
}) {
  const angle = kind === 'ANGLE'
  return <button type="button" className={`report-canvas-quick-add is-${kind.toLocaleLowerCase()}`}
    disabled={disabled} onClick={event => { event.stopPropagation(); onClick() }}>
    <span><Plus size={18} weight="bold" /></span>
    <strong>{angle ? '添加分析对象' : '添加小节'}</strong>
    <small>{angle ? '创建新的分析角度并设置首个小节' : '继续补充结论、论据或明细'}</small>
  </button>
}

export function CanvasSubsectionComposer({ disabled, required, onCancel, onConfirm }: {
  disabled?: boolean
  required?: boolean
  onCancel?: () => void
  onConfirm: (value: SubsectionComposerValue) => void
}) {
  const [layout, setLayout] = useState<SubsectionLayout>('CONCLUSION_TOP')
  const [chartCount, setChartCount] = useState(4)
  const [includeDetail, setIncludeDetail] = useState(false)
  const [includeAppendix, setIncludeAppendix] = useState(false)

  return <section className="report-canvas-subsection-composer" aria-label="添加小节">
    <header>
      <div><span>新建小节</span><strong>选择内容布局</strong><small>确定结论与图表的组合方式，创建后仍可调整内容。</small></div>
      {!required && onCancel && <button type="button" aria-label="取消添加小节" disabled={disabled} onClick={onCancel}><X size={18} /></button>}
    </header>
    <div className="report-canvas-subsection-count">
      <span><strong>图表数量</strong><small>论据图表 · 1–6 个</small></span>
      <div aria-label="图表数量">
        <button type="button" aria-label="减少图表" disabled={disabled || chartCount <= 1}
          onClick={() => setChartCount(value => Math.max(1, value - 1))}><Minus size={16} /></button>
        <output>{chartCount}</output>
        <button type="button" aria-label="增加图表" disabled={disabled || chartCount >= 6}
          onClick={() => setChartCount(value => Math.min(6, value + 1))}><Plus size={16} /></button>
      </div>
    </div>
    <div className="report-canvas-subsection-layouts" role="radiogroup" aria-label="小节内容布局">
      {subsectionLayouts.map(item => <button type="button" role="radio" aria-checked={layout === item.id}
        className={layout === item.id ? 'is-selected' : ''} disabled={disabled} key={item.id} onClick={() => setLayout(item.id)}>
        <img src={item.preview} alt={`${item.name}布局预览`} />
        <span><strong>{item.name}</strong><small>{item.description}</small></span>
        <em>{layout === item.id ? <><Check size={15} weight="bold" />已选择</> : <><Plus size={15} />选择</>}</em>
      </button>)}
    </div>
    <div className="report-canvas-subsection-footer">
      <div aria-label="附加内容区域">
        <span><strong>附加区域</strong><small>按需添加，可为空</small></span>
        <button type="button" aria-pressed={includeDetail} className={includeDetail ? 'is-active' : ''} disabled={disabled}
          onClick={() => setIncludeDetail(value => !value)}><Table size={16} />明细</button>
        <button type="button" aria-pressed={includeAppendix} className={includeAppendix ? 'is-active' : ''} disabled={disabled}
          onClick={() => setIncludeAppendix(value => !value)}><FileText size={16} />附录</button>
      </div>
      <button type="button" className="primary-button" disabled={disabled}
        onClick={() => onConfirm({ layout, chartCount, includeDetail, includeAppendix })}>
        <Check size={17} weight="bold" />{disabled ? '正在创建…' : '确认添加'}
      </button>
    </div>
  </section>
}
