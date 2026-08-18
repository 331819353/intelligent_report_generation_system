import { ChartBar, ChartLine, ChartPieSlice, DotsSixVertical, Funnel, Gauge, Image as ImageIcon, Info, ListBullets, Sparkle, Table, TextT } from '@phosphor-icons/react'
import type { ComponentManifest } from '../render/manifests.ts'
import { manifestRef, paletteDragType } from './operations.ts'

const categoryLabels: Record<ComponentManifest['category'], string> = {
  CHART: '图表', TABLE: '表格', CONTENT: '内容', CONTROL: '控件',
}

function paletteIcon(manifest: ComponentManifest) {
  const type = manifest.type
  if (type.startsWith('line') || type.startsWith('area')) return <ChartLine size={18} weight="duotone" />
  if (type.startsWith('bar')) return <ChartBar size={18} weight="duotone" />
  if (type.startsWith('pie') || type.startsWith('funnel')) return <ChartPieSlice size={18} weight="duotone" />
  if (type === 'metric-card') return <Gauge size={18} weight="duotone" />
  if (type === 'data-table') return <Table size={18} weight="duotone" />
  if (type === 'insight-text') return <Sparkle size={18} weight="duotone" />
  if (type === 'rich-text') return <TextT size={18} weight="duotone" />
  if (type === 'image') return <ImageIcon size={18} weight="duotone" />
  if (manifest.renderer === 'CONTROL') return <Funnel size={18} weight="duotone" />
  return <ListBullets size={18} weight="duotone" />
}

/** 组件面板：拖入或点击都会创建一个独立画布元素，并由布局算法寻找空位。 */
export function ComponentPalette({ manifests, disabled, onPick }: {
  manifests: ComponentManifest[]
  disabled: boolean
  onPick: (manifest: ComponentManifest) => void
}) {
  const groups = (Object.keys(categoryLabels) as ComponentManifest['category'][])
    .map(category => ({ category, items: manifests.filter(item => item.category === category) }))
    .filter(group => group.items.length > 0)
  return <div className="report-palette" aria-label="组件面板">
    <header><strong>画布元素</strong><small>拖入画布或点击添加，位置会自动排布</small></header>
    {groups.map(group => <section key={group.category}>
      <h3>{categoryLabels[group.category]}</h3>
      <div className="report-palette-grid">
        {group.items.map(manifest => <button type="button" key={manifestRef(manifest)} className="report-palette-item"
          draggable={!disabled} disabled={disabled}
          title={`${manifest.displayName} · 维度 ${manifest.dataContract.dimensions.min}～${manifest.dataContract.dimensions.max}，度量 ${manifest.dataContract.measures.min}～${manifest.dataContract.measures.max}`}
          onDragStart={event => {
            event.dataTransfer.setData(paletteDragType, manifestRef(manifest))
            event.dataTransfer.effectAllowed = 'copy'
          }}
          onClick={() => onPick(manifest)}>
          <span className="report-palette-grip"><DotsSixVertical size={12} /></span>
          <span className="report-palette-icon">{paletteIcon(manifest)}</span>
          <strong>{manifest.displayName}</strong>
          <small>{manifest.recommendedSize.w}×{manifest.recommendedSize.h}</small>
        </button>)}
      </div>
    </section>)}
    {manifests.length === 0 && <p className="report-interaction-note"><Info size={15} />组件清单尚未加载。</p>}
  </div>
}
