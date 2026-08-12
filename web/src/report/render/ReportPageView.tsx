import { type CSSProperties, useRef } from 'react'
import { ReportBlockBoundary, ReportComponentBoundary, ComponentStateView } from '../runtime/ComponentStateView.tsx'
import { mobileBlockHeight, toMobileLayout } from '../designer/layout/index.ts'
import { useMobileViewport } from './use-mobile-viewport.ts'
import { BlockHandles, SlotHandles } from './BlockInteraction.tsx'
import { useBlockInteraction, type DragMode } from './use-block-interaction.ts'
import { useGridInteraction, zoneGridCellSize } from './use-grid-interaction.ts'
import { ComponentView, type ComponentResult } from './ComponentView.tsx'
import { minimumSize, type ManifestIndex } from './manifests.ts'
import {
  blockComponentIDs, canvasOf, orderedSections,
  type Block, type Canvas, type GridRect, type Page, type ReportComponent, type ReportDefinition, type Section, type Zone,
} from './schema.ts'

/**
 * 报告页渲染器。
 *
 * 唯一的渲染入口，编辑器画布与运行页共用：布局完全由 Report Definition 的
 * canvas / block.layout / zone.layout / slot.grid 驱动，组件表现由组件清单与
 * options 驱动。渲染器内部没有任何针对具体报告的分支或数据；编辑态与运行态
 * 只差一个 editing 开关，因此「所见」与「发布后所得」共用同一份布局实现。
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
      return <div className={`report-render-slot ${selected ? 'is-selected' : ''} ${dragging ? 'is-dragging' : ''}`.trim()}
        key={slot.id}
        style={{
          gridColumn: `${rect.x + 1} / span ${Math.max(rect.w, 1)}`,
          gridRow: `${rect.y + 1} / span ${Math.max(rect.h, 1)}`,
        }}
        onClick={component && onSelectComponent
          ? event => { event.stopPropagation(); onSelectComponent(component.id, block.id) }
          : undefined}>
        {component
          ? <ReportComponentBoundary fallback={<ComponentStateView state="ERROR" onAction={() => onRetryBlock?.(block.id)} />}>
            <ComponentView component={component} manifests={manifests} mobile={mobile} designMode={designMode}
              item={results?.get(component.id)} onRetry={() => onRetryBlock?.(block.id)}
              selected={interaction?.roleFor(component.id).selected}
              dimmed={interaction?.roleFor(component.id).dimmed}
              onSelect={interaction && interaction.roleFor(component.id).source
                ? values => interaction.onSelect(component.id, values)
                : undefined} />
          </ReportComponentBoundary>
          : slot.componentId
            ? <ComponentStateView state="ERROR" boundTitle="组件在定义中缺失" />
            : <div className="report-render-empty-slot"><span>空槽位</span></div>}
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
    .sort((left, right) => left.order - right.order || left.id.localeCompare(right.id))
  if (zones.length === 0) {
    return <div className="report-render-placeholder"><span>该区块尚未配置内容区域</span></div>
  }
  return <div className="report-render-block-zones" style={{ gridTemplateRows: zones.map(zoneTrack).join(' ') }}>
    {zones.map((zone, position) => <ZoneGrid {...props} key={zone.id}
      zone={zone} position={position} total={zones.length} />)}
  </div>
}

/** 桌面端：每个章节是一张按画布列数铺开的网格，区块按 layout.desktop 定位。 */
function SectionGrid({ section, canvas, content, editing, manifests, components }: {
  section: Section
  canvas: Canvas
  content: (block: Block) => React.ReactNode
  editing?: EditingHandlers
  manifests: ManifestIndex
  components: Map<string, ReportComponent>
}) {
  const gridRef = useRef<HTMLDivElement>(null)
  // 区块的 y 坐标在整页范围内递增，而章节各自成栅格，因此按章节内最小 y
  // 归一化，既保留章节内的相对位置，又让章节自然纵向堆叠。
  const originY = section.blocks.length
    ? Math.min(...section.blocks.map(block => block.layout.desktop.y))
    : 0

  const minSizeFor = (blockId: string) => {
    const block = section.blocks.find(item => item.id === blockId)
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
    return <div className="report-render-placeholder is-section"><span>空章节，可从组件库加入内容</span></div>
  }
  return <div className={`report-render-grid ${interaction.drag ? 'is-dragging' : ''}`.trim()} ref={gridRef} style={{
    gridTemplateColumns: `repeat(${canvas.desktop.columns}, minmax(0, 1fr))`,
    gridAutoRows: `${canvas.desktop.baseRowHeight}px`,
    columnGap: `${canvas.desktop.gapX}px`,
    rowGap: `${canvas.desktop.gapY}px`,
  }}>
    {section.blocks.map(block => {
      const rect = interaction.rectFor(block.id, block.layout.desktop)
      const dragging = interaction.drag?.blockId === block.id
      return <ReportBlockBoundary key={block.id}>
        <div className={`report-render-block is-${block.type.toLocaleLowerCase()} ${dragging ? 'is-dragging' : ''}`.trim()}
          data-block-id={block.id}
          style={{
            gridColumn: `${rect.x + 1} / span ${Math.max(rect.w, 1)}`,
            gridRow: `${rect.y - originY + 1} / span ${Math.max(rect.h, 1)}`,
          }}>
          {content(block)}
          {editing && <BlockHandles blockId={block.id} rect={block.layout.desktop} columns={canvas.desktop.columns}
            minimum={minSizeFor(block.id)}
            onStart={(event, mode) => interaction.start(event, block.id, block.layout.desktop, mode)}
            onNudge={(next, mode) => editing.onLayoutChange(section.id, block.id, next, mode)} />}
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
    {orderedSections(page).map(section => <section className="report-render-section"
      id={`report-section-${section.id}`} data-section-id={section.id} key={section.id}>
      <header className="report-render-section-head"><h2>{section.name}</h2></header>
      <SectionGrid section={section} canvas={canvas} editing={editing} manifests={manifests} components={components}
        content={block => <BlockZones {...props} block={block} components={components} manifests={manifests} />} />
    </section>)}
  </div>
}

/**
 * 移动端：按定义里的 layout.mobile 投影为单列，复用与服务端执行计划一致的
 * toMobileLayout（顺序、PRIMARY_ONLY 裁剪、筛选抽屉分离）。
 */
function MobilePage(props: ReportPageViewProps) {
  const { definition, page, manifests, results, designMode, onRetryBlock, interaction } = props
  const components = new Map(definition.components.map(component => [component.id, component]))
  const projected = toMobileLayout(
    page as Parameters<typeof toMobileLayout>[0],
    definition.components.map(component => ({ id: component.id, templateRef: component.templateRef })),
    manifests.list(),
  )
  return <div className="report-render-page is-mobile">
    {projected.blocks.map(block => {
      let height: number | undefined
      try {
        height = block.heightMode === 'AUTO' ? undefined : mobileBlockHeight({
          order: block.order, visible: true, heightMode: block.heightMode, slotMode: block.slotMode,
          fixedHeight: block.fixedHeight, aspectRatio: block.aspectRatio,
        }, typeof window === 'undefined' ? 375 : window.innerWidth, -1)
      } catch { height = undefined }
      return <ReportBlockBoundary key={block.id}>
        <div className="report-render-mobile-block" style={{ height: height ? `${height}px` : undefined }}>
          {block.slots.map(slot => {
            const component = components.get(slot.componentId)
            if (!component) return null
            return <ReportComponentBoundary key={slot.id}
              fallback={<ComponentStateView state="ERROR" onAction={() => onRetryBlock?.(block.id)} />}>
              <ComponentView component={component} manifests={manifests} mobile designMode={designMode}
                item={results?.get(component.id)} onRetry={() => onRetryBlock?.(block.id)}
                selected={interaction?.roleFor(component.id).selected}
                dimmed={interaction?.roleFor(component.id).dimmed}
                onSelect={interaction && interaction.roleFor(component.id).source
                  ? values => interaction.onSelect(component.id, values)
                  : undefined} />
            </ReportComponentBoundary>
          })}
        </div>
      </ReportBlockBoundary>
    })}
  </div>
}

export function ReportPageView(props: ReportPageViewProps) {
  const mobile = useMobileViewport()
  if (props.page.sections.length === 0) {
    return <div className="report-render-placeholder is-page">
      <strong>报告还没有内容</strong>
      <span>从组件库添加指标卡、图表、表格或文字组件开始搭建。</span>
    </div>
  }
  // 编辑态始终使用桌面栅格：拖拽编排的是桌面布局，移动端是它的确定性投影。
  return mobile && !props.editing ? <MobilePage {...props} /> : <DesktopPage {...props} />
}
