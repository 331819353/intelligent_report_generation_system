import { ChartBar, ChartLine, ChartPieSlice, Gauge, Image as ImageIcon, Info, ListBullets, MagnifyingGlass, SlidersHorizontal, Sparkle, Table, TextT } from '@phosphor-icons/react'
import { useMemo, useState } from 'react'
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
  return <ListBullets size={18} weight="duotone" />
}

/** 内容面板：报告头与筛选栏由系统固定，这里只提供作者可以编排的正文元素。 */
export function ComponentPalette({ manifests, disabled, onPick }: {
  manifests: ComponentManifest[]
  disabled: boolean
  onPick: (manifest: ComponentManifest) => void
}) {
  const [query, setQuery] = useState('')
  const [filterOpen, setFilterOpen] = useState(false)
  const [category, setCategory] = useState<ComponentManifest['category'] | ''>('')
  const designable = useMemo(() => manifests.filter(manifest => manifest.renderer !== 'CONTROL'), [manifests])
  const filtered = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase()
    return designable.filter(manifest => (!category || manifest.category === category) && (!normalized ||
      manifest.displayName.toLocaleLowerCase().includes(normalized) ||
      manifest.type.toLocaleLowerCase().includes(normalized) ||
      categoryLabels[manifest.category].includes(normalized)))
  }, [category, designable, query])
  const unique = (items: ComponentManifest[]) => Array.from(new Map(items.map(item => [manifestRef(item), item])).values())
  const favoriteTypes = ['line-trend', 'bar-comparison', 'data-table', 'insight-text', 'image']
  const favorites = unique(favoriteTypes
    .map(type => filtered.find(item => item.type === type))
    .filter((item): item is ComponentManifest => Boolean(item)))
  const recent = unique(filtered.filter(item => !favorites.includes(item)).slice(0, 5))
  const essentials = unique(filtered.filter(item =>
    item.category === 'CONTENT' || item.category === 'TABLE').slice(0, 8))
  const sections = query.trim()
    ? [{ label: '搜索结果', items: filtered }]
    : [
        { label: '收藏', items: favorites },
        { label: '最近使用', items: recent },
        { label: '基础元素', items: essentials },
      ].filter(section => section.items.length > 0)
  return <div className="report-palette" aria-label="组件面板">
    <header><strong>内容</strong><small>只编排报告正文</small></header>
    <div className="report-palette-search">
      <MagnifyingGlass size={16} />
      <input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索元素" aria-label="搜索画布元素" />
      <button type="button" aria-label="筛选元素" aria-expanded={filterOpen} title="按类型筛选" onClick={() => setFilterOpen(open => !open)}><SlidersHorizontal size={16} /></button>
    </div>
    {filterOpen && <div className="report-palette-filters" aria-label="元素类型筛选">
      <button type="button" className={!category ? 'is-active' : ''} onClick={() => setCategory('')}>全部</button>
      {(['CHART', 'TABLE', 'CONTENT'] as ComponentManifest['category'][]).map(item => <button type="button" key={item}
        className={category === item ? 'is-active' : ''} onClick={() => setCategory(item)}>{categoryLabels[item]}</button>)}
    </div>}
    {sections.map(section => <section key={section.label}>
      <h3>{section.label}</h3>
      <div className="report-palette-grid">
        {section.items.map(manifest => <button type="button" key={`${section.label}:${manifestRef(manifest)}`} className="report-palette-item"
          draggable={!disabled} disabled={disabled}
          title={`${manifest.displayName} · 维度 ${manifest.dataContract.dimensions.min}～${manifest.dataContract.dimensions.max}，度量 ${manifest.dataContract.measures.min}～${manifest.dataContract.measures.max}`}
          onDragStart={event => {
            event.dataTransfer.setData(paletteDragType, manifestRef(manifest))
            event.dataTransfer.effectAllowed = 'copy'
          }}
          onClick={() => onPick(manifest)}>
          <span className="report-palette-icon">{paletteIcon(manifest)}</span>
          <strong>{manifest.displayName}</strong>
          <small>{categoryLabels[manifest.category]}</small>
        </button>)}
      </div>
    </section>)}
    {designable.length === 0 && <p className="report-interaction-note"><Info size={15} />内容元素清单尚未加载。</p>}
    {designable.length > 0 && filtered.length === 0 && <p className="report-interaction-note"><Info size={15} />没有匹配的内容元素。</p>}
  </div>
}
