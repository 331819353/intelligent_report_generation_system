import { type CSSProperties, useRef, useState } from 'react'
import { Copy, Trash } from '@phosphor-icons/react'
import { ReportBlockBoundary, ReportComponentBoundary, ComponentStateView } from '../runtime/ComponentStateView.tsx'
import { useMobileViewport } from './use-mobile-viewport.ts'
import { BlockHandles, SlotHandles } from './BlockInteraction.tsx'
import { useBlockInteraction, type DragMode } from './use-block-interaction.ts'
import { useGridInteraction, zoneGridCellSize } from './use-grid-interaction.ts'
import { ComponentView, type ComponentResult } from './ComponentView.tsx'
import { minimumSize, type ManifestIndex } from './manifests.ts'
import { paletteDragType } from '../designer/operations.ts'
import {
  blockComponentIDs, canvasOf, orderedSections,
  type Block, type Canvas, type GlobalFilter, type GridRect, type Page, type ReportComponent, type ReportDefinition, type Section, type Zone,
} from './schema.ts'

/**
 * 报告页渲染器。
 *
 * 唯一的渲染入口，编辑器画布与运行页共用同一份 Report Definition 与组件表现。
 * 编辑态读取 block / zone / slot 作为可操作布局；发布态则把已放置组件扁平化为
 * 自动流式画布，内部结构不会作为报告卡片边界暴露给阅读者。
 */

/**
 * 区域高度直接映射为 CSS 网格轨道，而不是先在 JS 里算像素：
 * FIXED 用固定高度，FR 用弹性份额，AUTO 用不低于 minHeight 的自适应高度，
 * HIDDEN 不渲染。这样区域高度在任意容器宽度下都保持定义声明的比例。
 */
function zoneTrack(zone: Zone): string {
  switch (zone.layout.heightMode) {
    case 'FIXED': return `${Math.max(zone.layout.fixedHeight ?? zone.layout.minHeight, 0)}px`
    case 'FR': return `${zone.layout.fr ?? 1}fr`
    default: return `minmax(${Math.max(zone.layout.minHeight, 0)}px, auto)`
  }
}

const overflowStyle: Record<Zone['layout']['overflow'], CSSProperties['overflow']> = {
  CLIP: 'hidden', SCROLL: 'auto', EXPAND: 'visible',
}

export type EditingHandlers = {
  /** 一次拖拽或缩放结束后提交；实现方负责碰撞消解与受控 Operation 生成。 */
  onLayoutChange(sectionId: string, blockId: string, rect: GridRect, mode: DragMode): void
  /** 卡片内槽位在所属区域的子网格中拖拽或缩放。 */
  onSlotLayoutChange(blockId: string, zoneId: string, slotId: string, rect: GridRect, mode: DragMode): void
  /** 区域在卡片内上移或下移。 */
  onZoneReorder(blockId: string, zoneId: string, direction: -1 | 1): void
  /** 从组件面板把组件放入分块预先声明的空槽位。 */
  onComponentDrop?(blockId: string, zoneId: string, slotId: string, manifestRef: string): void
  /** 分块标题独立于内部组件标题。 */
  onBlockTitleChange?(sectionId: string, blockId: string, title: string): void
  /** 卡片工具条：复制 / 删除整张卡片。未提供时不显示工具条。 */
  onDuplicateBlock?(sectionId: string, blockId: string): void
  onDeleteBlock?(sectionId: string, blockId: string): void
}

export type ReportPageViewProps = {
  definition: Pick<ReportDefinition, 'canvas' | 'components'>
  page: Page
  manifests: ManifestIndex
  results?: Map<string, ComponentResult>
  /** 草稿预览：没有执行结果时说明组件已配置，而不是报错。 */
  designMode?: boolean
  onRetryBlock?: (blockId: string) => void
  onSelectComponent?: (componentId: string, blockId: string) => void
  selectedComponentId?: string
  editing?: EditingHandlers
  /**
   * 联动上下文。传入后，声明为联动源且清单支持点击的组件即可被点击选中；
   * 具体影响范围由服务端按定义中的 Interaction 解析，渲染器不做推断。
   */
  interaction?: {
    roleFor(componentId: string): { source: boolean; selected: boolean; dimmed: boolean }
    onSelect(componentId: string, values: Record<string, unknown>): void
  }
  /** 运行页把筛选状态注入画布内的 CONTROL 组件，不再另起固定筛选栏。 */
  inlineFilters?: {
    definitions: GlobalFilter[]
    values: Record<string, unknown>
    onChange(filterId: string, value: unknown): void
  }
}

function inlineFilterFor(component: ReportComponent, inlineFilters?: ReportPageViewProps['inlineFilters']) {
  const field = component.dataBinding?.dimensions?.[0]?.field
  const dataContextId = component.dataBinding?.dataContextId
  if (!field || !dataContextId || !inlineFilters) return undefined
  const filter = inlineFilters.definitions.find(item =>
    item.fieldRef.dataContextId === dataContextId && item.fieldRef.field === field)
  if (!filter) return undefined
  return {
    filter,
    value: inlineFilters.values[filter.id],
    onChange: (value: unknown) => inlineFilters.onChange(filter.id, value),
  }
}

type BlockContentProps = {
  block: Block
  components: Map<string, ReportComponent>
  manifests: ManifestIndex
  results?: Map<string, ComponentResult>
  designMode?: boolean
  mobile?: boolean
  onRetryBlock?: (blockId: string) => void
  onSelectComponent?: (componentId: string, blockId: string) => void
  selectedComponentId?: string
  interaction?: ReportPageViewProps['interaction']
  inlineFilters?: ReportPageViewProps['inlineFilters']
  editing?: EditingHandlers
}

/**
 * 一个区域是卡片内的子网格。编辑态下槽位可以在其中拖拽与缩放，用的是与页面
 * 栅格同一套指针换算——两级网格只有单元格尺寸和行数上界不同。
 */
function ZoneGrid({ zone, position, total, ...props }: BlockContentProps & {
  zone: Zone; position: number; total: number
}) {
  const {
    block, components, manifests, results, designMode, mobile,
    onRetryBlock, onSelectComponent, selectedComponentId, interaction, editing,
  } = props
  const gridRef = useRef<HTMLDivElement>(null)
  const [dropSlotId, setDropSlotId] = useState('')
  const columns = Math.max(zone.layout.columns, 1)
  const rows = Math.max(zone.layout.rows, 1)

  const minSizeFor = (slotId: string) => {
    const slot = zone.slots.find(item => item.id === slotId)
    const component = slot?.componentId ? components.get(slot.componentId) : undefined
    return minimumSize(component && manifests.get(component.templateRef.type, component.templateRef.version))
  }
  const slotDrag = useGridInteraction({
    containerRef: gridRef,
    bounds: { columns, rows },
    cellSize: zoneGridCellSize(columns, rows),
    minSizeFor,
    onCommit: (slotId, rect, mode) => editing?.onSlotLayoutChange(block.id, zone.id, slotId, rect, mode),
  })
  // 只有一个槽位时区域内没有可编排的空间，把手只会干扰点击选中。
  const slotEditable = Boolean(editing) && zone.slots.length > 1

  return <div className={`report-render-zone is-${zone.type.toLocaleLowerCase()}`} ref={gridRef}
    style={{
      gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
      gridTemplateRows: `repeat(${rows}, minmax(0, 1fr))`,
      overflow: overflowStyle[zone.layout.overflow] ?? 'visible',
      maxHeight: zone.layout.maxHeight ? `${zone.layout.maxHeight}px` : undefined,
    }}>
    {editing && total > 1 && <div className="report-zone-controls" contentEditable={false}>
      <span>{zone.type}</span>
      <button type="button" aria-label={`上移区域 ${zone.type}`} disabled={position === 0}
        onClick={event => { event.stopPropagation(); editing.onZoneReorder(block.id, zone.id, -1) }}>↑</button>
      <button type="button" aria-label={`下移区域 ${zone.type}`} disabled={position === total - 1}
        onClick={event => { event.stopPropagation(); editing.onZoneReorder(block.id, zone.id, 1) }}>↓</button>
    </div>}
    {zone.slots.map(slot => {
      const component = slot.componentId ? components.get(slot.componentId) : undefined
      const selected = Boolean(component && component.id === selectedComponentId)
      const rect = slotDrag.rectFor(slot.id, slot.grid)
      const dragging = slotDrag.drag?.id === slot.id
      const acceptsDrop = Boolean(editing?.onComponentDrop && !slot.componentId)
      const emptyLabel = zone.type === 'FILTER' ? '拖入筛选控件'
        : zone.type === 'INSIGHT' ? '拖入结论或文本组件'
          : zone.type === 'CONTENT' ? '拖入图表、指标或表格' : '拖入内容组件'
      return <div className={`report-render-slot ${selected ? 'is-selected' : ''} ${dragging ? 'is-dragging' : ''} ${dropSlotId === slot.id ? 'is-drop-target' : ''}`.trim()}
        key={slot.id}
        data-slot-id={slot.id}
        data-empty-slot={!slot.componentId ? 'true' : undefined}
        style={{
          gridColumn: `${rect.x + 1} / span ${Math.max(rect.w, 1)}`,
          gridRow: `${rect.y + 1} / span ${Math.max(rect.h, 1)}`,
        }}
        onClick={component && onSelectComponent
          ? event => { event.stopPropagation(); onSelectComponent(component.id, block.id) }
          : undefined}
        onDragOver={acceptsDrop ? event => {
          if (!event.dataTransfer.types.includes(paletteDragType)) return
          event.preventDefault(); event.stopPropagation(); event.dataTransfer.dropEffect = 'copy'; setDropSlotId(slot.id)
        } : undefined}
        onDragLeave={acceptsDrop ? event => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropSlotId('')
        } : undefined}
        onDrop={acceptsDrop ? event => {
          const ref = event.dataTransfer.getData(paletteDragType)
          if (!ref) return
          event.preventDefault(); event.stopPropagation(); setDropSlotId('')
          editing?.onComponentDrop?.(block.id, zone.id, slot.id, ref)
        } : undefined}>
        {editing && slot.cardKind && <span className="report-slot-kind-badge">{slot.cardKind}</span>}
        {component
          ? <ReportComponentBoundary fallback={<ComponentStateView state="ERROR" onAction={() => onRetryBlock?.(block.id)} />}>
            <ComponentView component={component} manifests={manifests} mobile={mobile} designMode={designMode}
              item={results?.get(component.id)} onRetry={() => onRetryBlock?.(block.id)}
              inlineFilter={inlineFilterFor(component, props.inlineFilters)}
              selected={interaction?.roleFor(component.id).selected}
              dimmed={interaction?.roleFor(component.id).dimmed}
              onSelect={interaction && interaction.roleFor(component.id).source
                ? values => interaction.onSelect(component.id, values)
                : undefined} />
          </ReportComponentBoundary>
          : slot.componentId
            ? <ComponentStateView state="ERROR" boundTitle="组件在定义中缺失" />
            : <div className="report-render-empty-slot"><strong>{zone.type}</strong><span>{editing ? emptyLabel : '待配置'}</span></div>}
        {slotEditable && editing && <SlotHandles
          slotId={slot.id} rect={slot.grid} columns={columns} rows={rows} minimum={minSizeFor(slot.id)}
          onStart={(event, mode) => slotDrag.start(event, slot.id, slot.grid, mode)}
          onNudge={(next, mode) => editing.onSlotLayoutChange(block.id, zone.id, slot.id, next, mode)} />}
      </div>
    })}
  </div>
}

function BlockZones(props: BlockContentProps) {
  // 区域按声明顺序渲染。服务端规范化会做同样的排序，但渲染器不依赖它已经发生。
  const zones = props.block.zones
    .filter(zone => zone.layout.heightMode !== 'HIDDEN')
    .slice()
    .sort((left, right) =>
      (left.order ?? left.layout.emptyPriority) - (right.order ?? right.layout.emptyPriority) || left.id.localeCompare(right.id))
  if (zones.length === 0) {
    return <div className="report-render-placeholder"><span>该区块尚未配置内容区域</span></div>
  }
  return <div className="report-render-block-zones" style={{ gridTemplateRows: zones.map(zoneTrack).join(' ') }}>
    {zones.map((zone, position) => <ZoneGrid {...props} key={zone.id}
      zone={zone} position={position} total={zones.length} />)}
  </div>
}

/** 桌面端：每个章节是一张按画布列数铺开的网格，区块按 layout.desktop 定位。 */
function SectionGrid({ section, canvas, content, editing, manifests, components, selectedComponentId, onSelectComponent }: {
  section: Section
  canvas: Canvas
  content: (block: Block) => React.ReactNode
  editing?: EditingHandlers
  manifests: ManifestIndex
  components: Map<string, ReportComponent>
  selectedComponentId?: string
  onSelectComponent?: (componentId: string, blockId: string) => void
}) {
  const gridRef = useRef<HTMLDivElement>(null)
  // 区块的 y 坐标在整页范围内递增，而章节各自成栅格，因此按章节内最小 y
  // 归一化，既保留章节内的相对位置，又让章节自然纵向堆叠。
  const originY = section.blocks.length
    ? Math.min(...section.blocks.map(block => block.layout.desktop.y))
    : 0

  const minSizeFor = (blockId: string) => {
    const block = section.blocks.find(item => item.id === blockId)
    if (block?.title && (['FILTER', 'INSIGHT', 'CONTENT'] as const).every(kind => block.zones.some(zone => zone.type === kind))) {
      return { w: 12, h: 8 }
    }
    const componentId = block ? blockComponentIDs(block)[0] : undefined
    const component = componentId ? components.get(componentId) : undefined
    return minimumSize(component && manifests.get(component.templateRef.type, component.templateRef.version))
  }

  const interaction = useBlockInteraction({
    metrics: {
      columns: canvas.desktop.columns, gapX: canvas.desktop.gapX,
      gapY: canvas.desktop.gapY, baseRowHeight: canvas.desktop.baseRowHeight,
    },
    containerRef: gridRef,
    minSizeFor,
    onCommit: (blockId, rect, mode) => editing?.onLayoutChange(section.id, blockId, rect, mode),
  })

  if (section.blocks.length === 0) {
    return <div className="report-render-placeholder is-section"><span>{editing ? '空分区：从左侧拖入图表、指标、文本或筛选控件' : '本分区暂无内容'}</span></div>
  }
  const singleBlock = section.blocks.length === 1
  return <div className={`report-render-grid ${singleBlock ? 'is-single-block' : ''} ${interaction.drag ? 'is-dragging' : ''}`.trim()} ref={gridRef} style={{
    gridTemplateColumns: `repeat(${canvas.desktop.columns}, minmax(0, 1fr))`,
    gridAutoRows: `${canvas.desktop.baseRowHeight}px`,
    columnGap: `${canvas.desktop.gapX}px`,
    rowGap: `${canvas.desktop.gapY}px`,
  }}>
    {section.blocks.map(block => {
      const rect = interaction.rectFor(block.id, block.layout.desktop)
      const dragging = interaction.drag?.blockId === block.id
      const componentIds = blockComponentIDs(block)
      const selected = Boolean(selectedComponentId) && componentIds.includes(selectedComponentId ?? '')
      return <ReportBlockBoundary key={block.id}>
        <div className={`report-render-block is-${block.type.toLocaleLowerCase()} ${dragging ? 'is-dragging' : ''} ${selected ? 'is-selected' : ''}`.trim()}
          data-block-id={block.id}
          style={{
            gridColumn: singleBlock ? '1 / -1' : `${rect.x + 1} / span ${Math.max(rect.w, 1)}`,
            gridRow: `${rect.y - originY + 1} / span ${Math.max(rect.h, 1)}`,
          }}
          onClick={editing && onSelectComponent && componentIds[0]
            ? event => { event.stopPropagation(); onSelectComponent(componentIds[0], block.id) }
            : undefined}>
          {editing && block.cardKind && <span className="report-block-kind-badge">{block.cardKind}</span>}
          {block.title && <header className="report-render-block-head">
            {editing?.onBlockTitleChange
              ? <input key={block.title} defaultValue={block.title} maxLength={200} aria-label="元素组标题"
                onClick={event => event.stopPropagation()}
                onKeyDown={event => {
                  if (event.key === 'Enter') (event.target as HTMLInputElement).blur()
                  if (event.key === 'Escape') { (event.target as HTMLInputElement).value = block.title ?? ''; (event.target as HTMLInputElement).blur() }
                }}
                onBlur={event => editing.onBlockTitleChange?.(section.id, block.id, event.target.value)} />
              : <h3>{block.title}</h3>}
            {section.question && <p>{section.question}</p>}
          </header>}
          {content(block)}
          {editing && <BlockHandles blockId={block.id} rect={block.layout.desktop} columns={canvas.desktop.columns}
            minimum={minSizeFor(block.id)}
            onStart={(event, mode) => interaction.start(event, block.id, block.layout.desktop, mode)}
            onNudge={(next, mode) => editing.onLayoutChange(section.id, block.id, next, mode)} />}
          {editing && (editing.onDuplicateBlock || editing.onDeleteBlock) && <div className="report-block-toolbar" role="toolbar" aria-label="画布元素操作">
            {editing.onDuplicateBlock && <button type="button" title="复制元素" aria-label="复制元素"
              onClick={event => { event.stopPropagation(); editing.onDuplicateBlock?.(section.id, block.id) }}><Copy size={14} /></button>}
            {editing.onDeleteBlock && <button type="button" className="is-danger" title="删除元素" aria-label="删除元素"
              onClick={event => { event.stopPropagation(); editing.onDeleteBlock?.(section.id, block.id) }}><Trash size={14} /></button>}
          </div>}
        </div>
      </ReportBlockBoundary>
    })}
  </div>
}

function DesktopPage(props: ReportPageViewProps) {
  const { definition, page, manifests, editing } = props
  const canvas = canvasOf(definition)
  const components = new Map(definition.components.map(component => [component.id, component]))
  return <div className={`report-render-page ${editing ? 'is-editing' : ''}`.trim()}>
    {orderedSections(page).map(section => {
      const titleOwnedByBlock = section.blocks.length === 1 && section.blocks[0]?.title === section.name
      return <section className="report-render-section"
        id={`report-section-${section.id}`} data-section-id={section.id} key={section.id}>
        {!titleOwnedByBlock && <header className="report-render-section-head"><h2>{section.name}</h2>{section.question && <p>{section.question}</p>}</header>}
        <SectionGrid section={section} canvas={canvas} editing={editing} manifests={manifests} components={components}
          selectedComponentId={props.selectedComponentId} onSelectComponent={props.onSelectComponent}
          content={block => <BlockZones {...props} block={block} components={components} manifests={manifests} />} />
      </section>
    })}
  </div>
}

/**
 * 发布态把 Definition 中的分块降级为内部编排信息，再把每个已配置组件提升为
 * 独立画布元素。元素按类型自动选择跨度并以 dense grid 重排，因此运行页不会
 * 暴露分块边框、空槽位或分块标题，也不会因旧坐标留下大片空白。
 */
function RuntimeAutoPage(props: ReportPageViewProps & { mobile?: boolean }) {
  const { definition, page, manifests, results, designMode, onRetryBlock, interaction, mobile } = props
  const components = new Map(definition.components.map(component => [component.id, component]))

  return <div className={`report-render-page is-auto ${mobile ? 'is-mobile' : ''}`.trim()}>
    {orderedSections(page).map(section => {
      const elements = section.blocks.flatMap(block => block.zones.flatMap(zone => zone.slots.flatMap(slot => {
        const component = slot.componentId ? components.get(slot.componentId) : undefined
        return component ? [{ block, zone, component }] : []
      })))
      if (elements.length === 0) return null
      return <section className="report-render-section report-runtime-auto-section"
        id={`report-section-${section.id}`} data-section-id={section.id} key={section.id}>
        <header className="report-render-section-head"><div><span /> <h2>{section.name}</h2></div>{section.question && <p>{section.question}</p>}</header>
        <div className="report-runtime-auto-grid">
          {elements.map(({ block, zone, component }) => {
            const manifest = manifests.get(component.templateRef.type, component.templateRef.version)
            const kind = manifest?.renderer === 'CONTROL' || zone.type === 'FILTER' ? 'filter'
              : manifest?.category === 'TABLE' ? 'table'
                : component.templateRef.type === 'metric-card' ? 'metric'
                  : manifest?.renderer === 'TEXT' || zone.type === 'INSIGHT' ? 'narrative'
                    : manifest?.renderer === 'IMAGE' ? 'image' : 'visual'
            const role = interaction?.roleFor(component.id)
            return <ReportBlockBoundary key={`${block.id}:${component.id}`}>
              <div className={`report-runtime-element is-${kind}`} data-block-id={block.id} data-component-id={component.id}>
                <ReportComponentBoundary fallback={<ComponentStateView state="ERROR" onAction={() => onRetryBlock?.(block.id)} />}>
                  <ComponentView component={component} manifests={manifests} mobile={mobile} designMode={designMode}
                    item={results?.get(component.id)} onRetry={() => onRetryBlock?.(block.id)}
                    inlineFilter={inlineFilterFor(component, props.inlineFilters)} selected={role?.selected} dimmed={role?.dimmed}
                    onSelect={interaction && role?.source ? values => interaction.onSelect(component.id, values) : undefined} />
                </ReportComponentBoundary>
              </div>
            </ReportBlockBoundary>
          })}
        </div>
      </section>
    })}
  </div>
}

export function ReportPageView(props: ReportPageViewProps) {
  const mobile = useMobileViewport()
  if (props.page.sections.length === 0) {
    return <div className={`report-render-placeholder is-page ${props.editing ? 'is-editing' : ''}`.trim()}>
      <strong>{props.editing ? '把第一个元素拖入画布' : '报告还没有内容'}</strong>
      <span>{props.editing ? '图表、指标、文本与筛选控件会按内容类型自动排布。' : '画布中暂时没有可显示的元素。'}</span>
      {props.editing && <ol className="report-render-placeholder-steps">
        <li>添加元素</li><li>配置数据</li><li>在画布放置筛选</li><li>预览与发布</li>
      </ol>}
    </div>
  }
  // 编辑态保留可操作的结构栅格；发布态一律提升为独立元素并自动重排。
  return props.editing ? <DesktopPage {...props} /> : <RuntimeAutoPage {...props} mobile={mobile} />
}
