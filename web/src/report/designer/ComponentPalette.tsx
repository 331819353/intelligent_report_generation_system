import {
  ArrowsClockwise, ArrowsLeftRight, CalendarDots, ChartBar, ChartBarHorizontal, ChartDonut,
  ChartLineUp, ChartScatter, ClockCounterClockwise, Flask, Funnel, Gauge, GitBranch, GridFour,
  FileText, Info, Kanban, Lightbulb, ListChecks, MagnifyingGlass, MapTrifold, Path, Pulse, Ranking,
  SlidersHorizontal, Table, Target, TreeStructure, UsersThree, Warning,
} from '@phosphor-icons/react'
import { useMemo, useState, type ReactNode } from 'react'
import {
  analysisCardCatalog, analysisCardOption, analysisCardVariants, isAnalysisCardManifest,
  type AnalysisCardCatalogItem, type AnalysisCardVariant, type AnalysisRendererKind,
} from '../analysis/catalog.ts'
import type { ComponentManifest } from '../render/manifests.ts'
import type { ComponentOptions } from '../render/schema.ts'
import { encodePalettePayload, paletteDragType } from './operations.ts'

const groups = [
  { id: 'overview', label: '概览与变化', range: [1, 6] },
  { id: 'structure', label: '结构与关系', range: [7, 13] },
  { id: 'journey', label: '流程与生命周期', range: [14, 17] },
  { id: 'diagnosis', label: '归因与诊断', range: [18, 21] },
  { id: 'decision', label: '预测与决策', range: [22, 26] },
  { id: 'operations', label: '运营与表达', range: [27, 37] },
] as const

function familyIcon(kind: AnalysisRendererKind, size = 17): ReactNode {
  const props = { size, weight: 'duotone' as const }
  switch (kind) {
    case 'metric': return <Gauge {...props} />
    case 'progress': return <Target {...props} />
    case 'comparison': return <ArrowsLeftRight {...props} />
    case 'ranking': return <Ranking {...props} />
    case 'trend': case 'forecast': return <ChartLineUp {...props} />
    case 'composition': case 'contribution': return <ChartDonut {...props} />
    case 'structure': case 'waterfall': return <ChartBar {...props} />
    case 'concentration': case 'drivers': case 'sensitivity': return <ChartBarHorizontal {...props} />
    case 'distribution': case 'relationship': case 'quadrant': case 'risk': return <ChartScatter {...props} />
    case 'matrix': case 'cohort': return <GridFour {...props} />
    case 'funnel': return <Funnel {...props} />
    case 'flow': return <Path {...props} />
    case 'lifecycle': return <ArrowsClockwise {...props} />
    case 'root-cause': return <TreeStructure {...props} />
    case 'scenario': return <GitBranch {...props} />
    case 'experiment': return <Flask {...props} />
    case 'geospatial': return <MapTrifold {...props} />
    case 'monitoring': return <Pulse {...props} />
    case 'pipeline': return <Kanban {...props} />
    case 'calendar': return <CalendarDots {...props} />
    case 'detail': return <Table {...props} />
    case 'timeline': return <ClockCounterClockwise {...props} />
    case 'insight': return <Lightbulb {...props} />
    case 'action': return <ListChecks {...props} />
    case 'data-info': return <Info {...props} />
    case 'scope': return <SlidersHorizontal {...props} />
    case 'long-form': return <FileText {...props} />
    default: return <UsersThree {...props} />
  }
}

function AnalysisCardThumbnail({ item, variant }: { item: AnalysisCardCatalogItem; variant: AnalysisCardVariant }) {
  return <span className={`report-analysis-thumb is-${item.rendererKind} is-variant-${variant}`} aria-hidden="true">
    <img src={`/analysis-card-gallery/${String(item.id).padStart(2, '0')}-${item.slug}/${variant}.${item.id === 37 ? 'png' : 'webp'}`} alt="" loading="lazy" />
  </span>
}

function FallbackPalette({ manifests, disabled, onPick }: {
  manifests: ComponentManifest[]
  disabled: boolean
  onPick: (manifest: ComponentManifest, options?: Partial<ComponentOptions>) => void
}) {
  return <section className="report-palette-fallback">
    <p><Warning size={14} />新版分析卡片合同尚未加载，暂时显示兼容组件。</p>
    {manifests.filter(manifest => manifest.renderer !== 'CONTROL').map(manifest => <button type="button" key={`${manifest.type}@${manifest.version}`}
      disabled={disabled} onClick={() => onPick(manifest)}>{manifest.displayName}</button>)}
  </section>
}

/** 按业务问题组织的分析卡片库；每个语义类型固定提供三种可复用版式。 */
export function ComponentPalette({ manifests, disabled, onPick }: {
  manifests: ComponentManifest[]
  disabled: boolean
  onPick: (manifest: ComponentManifest, options?: Partial<ComponentOptions>) => void
}) {
  const [query, setQuery] = useState('')
  const [filterOpen, setFilterOpen] = useState(false)
  const [group, setGroup] = useState('')
  const analysisManifests = useMemo(() => new Map(manifests.filter(isAnalysisCardManifest).map(item => [item.type, item])), [manifests])
  const normalized = query.trim().toLocaleLowerCase()
  const visible = useMemo(() => analysisCardCatalog.filter(item => {
    const selectedGroup = groups.find(candidate => candidate.id === group)
    const inGroup = !selectedGroup || (item.id >= selectedGroup.range[0] && item.id <= selectedGroup.range[1])
    const matches = !normalized || [item.name, item.question, ...item.subtypes, ...item.presentations]
      .some(value => value.toLocaleLowerCase().includes(normalized))
    return inGroup && matches && analysisManifests.has(item.type)
  }), [analysisManifests, group, normalized])

  return <div className="report-palette" aria-label="分析卡片库">
    <header><strong>分析卡片</strong><small>37 类业务问题 · 每类 3 种版式</small></header>
    <div className="report-palette-search">
      <MagnifyingGlass size={16} />
      <input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索问题、类型或图形" aria-label="搜索分析卡片" />
      <button type="button" aria-label="筛选分析卡片" aria-expanded={filterOpen} title="按分析阶段筛选" onClick={() => setFilterOpen(open => !open)}><SlidersHorizontal size={16} /></button>
    </div>
    {filterOpen && <div className="report-palette-filters" aria-label="分析阶段筛选">
      <button type="button" className={!group ? 'is-active' : ''} onClick={() => setGroup('')}>全部</button>
      {groups.map(item => <button type="button" key={item.id} className={group === item.id ? 'is-active' : ''}
        onClick={() => setGroup(item.id)}>{item.label}</button>)}
    </div>}
    {analysisManifests.size === 0
      ? <FallbackPalette manifests={manifests} disabled={disabled} onPick={onPick} />
      : <div className="report-analysis-categories">
        {visible.map(item => {
          const manifest = analysisManifests.get(item.type)
          if (!manifest) return null
          return <section className="report-analysis-category" key={item.type}>
            <header>
              <span>{familyIcon(item.rendererKind)}</span>
              <div><strong>{String(item.id).padStart(2, '0')} · {item.name}</strong><small>{item.question}</small></div>
            </header>
            <div className="report-analysis-variants" aria-label={`${item.name}版式`}>
              {analysisCardVariants.map(variant => {
                const options = analysisCardOption(variant.id)
                return <button type="button" key={variant.id} className={`is-variant-${variant.id}`}
                  draggable={!disabled} disabled={disabled}
                  title={`${variant.name}：${variant.description}。${item.presentations.join(' / ')}`}
                  onDragStart={event => {
                    event.dataTransfer.setData(paletteDragType, encodePalettePayload(manifest, options))
                    event.dataTransfer.effectAllowed = 'copy'
                  }}
                  onClick={() => onPick(manifest, options)}>
                  <AnalysisCardThumbnail item={item} variant={variant.id} />
                  <span>{variant.name}</span>
                </button>
              })}
            </div>
          </section>
        })}
      </div>}
    {analysisManifests.size > 0 && visible.length === 0 && <p className="report-interaction-note"><Info size={15} />没有匹配的分析卡片。</p>}
  </div>
}
