import { useEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from 'react'
import { ArrowClockwise, CaretDown, DotsThree, DownloadSimple } from '@phosphor-icons/react'
import type { ComponentType, Grid, ReportBlock, ReportComponent, ReportComponentInteractionEvent, ReportDocument, ReportParameter, ReportRendererMode, ReportRuntimeContext, ReportSelection, ReportValidationIssue } from '../../lib/report-contract'
import { buildReportInteractionCommand, type ReportInteractionExecutor } from '../../lib/report-interactions'
import { calculateCanvasScale, deriveEmptyGridCells, LOGICAL_CANVAS_WIDTH, LOGICAL_ROW_HEIGHT, pointerDeltaToGrid, pointerDeltaToInnerGrid, pointerPositionToInnerGrid, type BlockResetMode, type EmptyGridCell } from '../../lib/report-layout'
import { validateReportDocument } from '../../lib/report-schema'
import { calculateReportStickyPlacements, reportStickyPlacementKey, type ReportStickyPlacement } from '../../lib/report-sticky'
import { defaultComponentRegistry, type ReportComponentRegistry, UnknownComponentRenderer } from './componentRegistry'
import { parseDraggedComponentType, REPORT_COMPONENT_DRAG_MIME } from './componentDrag'
import { ReportErrorBoundary } from './ReportErrorBoundary'

type ReportRendererProps = {
  document: ReportDocument
  runtime: ReportRuntimeContext
  mode: ReportRendererMode
  registry?: ReportComponentRegistry
  warnings?: ReportValidationIssue[]
  onBlockGridChange?: (pageID: string, blockID: string, grid: Grid) => void
  onComponentGridChange?: (pageID: string, blockID: string, componentID: string, grid: Grid) => void
  onComponentDrop?: (pageID: string, blockID: string, type: ComponentType, anchor: Pick<Grid, 'x' | 'y'>) => void
  onComponentDuplicate?: (pageID: string, blockID: string, componentID: string) => void
  onComponentDelete?: (pageID: string, blockID: string, componentID: string) => void
  pendingComponentType?: ComponentType
  onPendingComponentConsumed?: () => void
  onBlockReset?: (pageID: string, blockID: string, mode: BlockResetMode) => void
  onEmptyCellActivate?: (pageID: string, x: number, y: number) => void
  onEmptyCellDrop?: (pageID: string, x: number, y: number, type: ComponentType) => void
  designerContentRows?: Record<string, number>
  onInteractionRequest?: ReportInteractionExecutor
  selection?: ReportSelection
  onSelectionChange?: (selection: ReportSelection) => void
}

type ValidatedReportRendererProps = Omit<ReportRendererProps, 'document' | 'warnings'> & {
  source: unknown
}

const rendererIdentityKeys = new WeakMap<object, number>()
let nextRendererIdentityKey = 1

/** 在渲染入口执行正式 Schema 校验，结构损坏时禁止进入组件树。 */
export function ValidatedReportRenderer({ source, ...props }: ValidatedReportRendererProps) {
  const result = useMemo(() => validateReportDocument(source), [source])
  if (!result.document) return <ReportContractFailure issues={result.errors} />
  return <ReportRenderer {...props} document={result.document} warnings={result.warnings} />
}

/** 运行实例切换时重建会话；设计器文档更新不能重挂载，否则会中断连续拖拽。 */
export function ReportRenderer(props: ReportRendererProps) {
  const sessionKey = rendererIdentityKey(props.runtime)
  return <ReportRendererSession key={sessionKey} {...props} />
}

/** 设计器预览和在线查看器共用的只读渲染核心。 */
function ReportRendererSession({ document, runtime, mode, registry, warnings = [], onBlockGridChange, onComponentGridChange, onComponentDrop, onComponentDuplicate, onComponentDelete, pendingComponentType, onPendingComponentConsumed, onBlockReset, onEmptyCellActivate, onEmptyCellDrop, designerContentRows, onInteractionRequest, selection, onSelectionChange }: ReportRendererProps) {
  const pages = useMemo(() => [...document.pages].sort((left, right) => left.order - right.order), [document.pages])
  const [activePageID, setActivePageID] = useState(pages[0]?.id ?? '')
  const activePage = pages.find(page => page.id === activePageID) ?? pages[0]
  const renderers = useMemo(() => ({ ...defaultComponentRegistry, ...registry }), [registry])
  const [runtimeState, setRuntimeState] = useState(() => ({ source: runtime, value: runtime }))
  const runtimeSourceRef = useRef(runtime)
  const requestRevisionRef = useRef(0)
  const latestRequestByComponentRef = useRef(new Map<string, number>())
  const [interactionStatus, setInteractionStatus] = useState<{ kind: 'STATUS' | 'ERROR'; message: string }>()
  const [busyComponentIDs, setBusyComponentIDs] = useState<Set<string>>(() => new Set())
  const [drillLevels, setDrillLevels] = useState<Record<string, number>>({})
  const viewportRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLDivElement>(null)
  const [viewportWidth, setViewportWidth] = useState(960)
  const [stickyViewportTop, setStickyViewportTop] = useState(0)
  const scale = calculateCanvasScale(viewportWidth)
  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const updateWidth = () => setViewportWidth(viewport.clientWidth || 960)
    updateWidth()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(updateWidth)
    observer.observe(viewport)
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    if (mode !== 'viewer') return
    const canvas = canvasRef.current
    const viewport = viewportRef.current
    if (!canvas || !viewport) return
    const scrollContainer = findVerticalScrollContainer(viewport)
    const scrollElement = scrollContainer instanceof HTMLElement ? scrollContainer : undefined
    let animationFrame = 0

    // 滚动事件只排队一次测量，避免同一动画帧内反复读取布局并触发多次渲染。
    const updateViewportTop = () => {
      animationFrame = 0
      const canvasTop = canvas.getBoundingClientRect().top
      const boundaryTop = scrollElement
        ? Math.max(0, scrollElement.getBoundingClientRect().top + scrollElement.clientTop)
        : 0
      const next = (boundaryTop - canvasTop) / scale
      if (!Number.isFinite(next)) return
      setStickyViewportTop(current => Math.abs(current - next) < 0.1 ? current : next)
    }
    const scheduleUpdate = () => {
      if (animationFrame === 0) animationFrame = window.requestAnimationFrame(updateViewportTop)
    }

    scheduleUpdate()
    window.addEventListener('resize', scheduleUpdate, { passive: true })
    window.addEventListener('scroll', scheduleUpdate, { passive: true, capture: true })
    scrollElement?.addEventListener('scroll', scheduleUpdate, { passive: true })
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(scheduleUpdate)
    observer?.observe(canvas)
    // 运行提示、页签或兼容警告插入在画布上方时，父容器高度会变化并带动画布位置。
    if (viewport.parentElement) observer?.observe(viewport.parentElement)
    if (scrollElement) observer?.observe(scrollElement)
    return () => {
      if (animationFrame !== 0) window.cancelAnimationFrame(animationFrame)
      observer?.disconnect()
      window.removeEventListener('resize', scheduleUpdate)
      window.removeEventListener('scroll', scheduleUpdate, true)
      scrollElement?.removeEventListener('scroll', scheduleUpdate)
    }
  }, [activePageID, interactionStatus?.message, mode, pages.length, scale, warnings.length])
  useEffect(() => {
    runtimeSourceRef.current = runtime
  }, [runtime])
  // 运行上下文来源变化时在当前渲染内重置派生状态，避免旧报告运行结果泄漏到新运行实例。
  if (runtimeState.source !== runtime) {
    setRuntimeState({ source: runtime, value: runtime })
  }
  const interactiveRuntime = runtimeState.source === runtime ? runtimeState.value : runtime

  /** 交互状态属于本次查看运行，不写回版本化报告 JSON。 */
  async function handleComponentInteraction(componentID: string, event: ReportComponentInteractionEvent) {
    const sourceRuntime = runtime
    const built = buildReportInteractionCommand(document, interactiveRuntime.parameters, componentID, event)
    if (!built.command) {
      setInteractionStatus({ kind: 'ERROR', message: built.issue?.reason ?? '联动命令构建失败' })
      return
    }
    const revision = requestRevisionRef.current + 1
    requestRevisionRef.current = revision
    const command = { ...built.command, requestRevision: revision }
    if (command.drill) setDrillLevels(current => ({ ...current, [componentID]: command.drill!.level }))
    else if (command.type === 'DRILL_UP') setDrillLevels(current => ({ ...current, [componentID]: -1 }))
    updateInteractiveRuntime(current => ({ ...current, parameters: command.parameters }))
    setInteractionStatus({ kind: 'STATUS', message: `已应用联动，影响 ${command.affectedComponentIds.length} 个组件` })
    if (!onInteractionRequest) return

    const previousData = Object.fromEntries(command.affectedComponentIds.map(id => [id, interactiveRuntime.componentData[id]]))
    const participantIDs = [...new Set([componentID, ...command.affectedComponentIds])]
    for (const id of participantIDs) latestRequestByComponentRef.current.set(id, revision)
    setBusyComponentIDs(current => new Set([...current, ...participantIDs]))
    updateInteractiveRuntime(current => ({
      ...current,
      componentData: {
        ...current.componentData,
        ...Object.fromEntries(command.affectedComponentIds.map(id => [id, { ...current.componentData[id], status: 'LOADING' as const }])),
      },
    }))
    try {
      const result = await onInteractionRequest(command)
      if (runtimeSourceRef.current !== sourceRuntime) return
      const currentAffectedIDs = command.affectedComponentIds.filter(id => latestRequestByComponentRef.current.get(id) === revision)
      if (currentAffectedIDs.length === 0) return
      const currentAffectedSet = new Set(currentAffectedIDs)
      const previousCurrentData = Object.fromEntries(Object.entries(previousData).filter(([id]) => currentAffectedSet.has(id)))
      const resultCurrentData = Object.fromEntries(Object.entries(result?.componentData ?? {}).filter(([id]) => currentAffectedSet.has(id)))
      updateInteractiveRuntime(current => ({
        ...current,
        componentData: { ...current.componentData, ...previousCurrentData, ...resultCurrentData },
      }))
      if (requestRevisionRef.current === revision) setInteractionStatus({ kind: 'STATUS', message: `联动刷新完成，共更新 ${Object.keys(resultCurrentData).length} 个组件` })
    } catch {
      if (runtimeSourceRef.current !== sourceRuntime) return
      const currentAffectedIDs = command.affectedComponentIds.filter(id => latestRequestByComponentRef.current.get(id) === revision)
      if (currentAffectedIDs.length === 0) return
      updateInteractiveRuntime(current => ({
        ...current,
        componentData: {
          ...current.componentData,
          ...Object.fromEntries(currentAffectedIDs.map(id => [id, { status: 'ERROR' as const, errorMessage: '联动刷新失败，请稍后重试' }])),
        },
      }))
      if (requestRevisionRef.current === revision) setInteractionStatus({ kind: 'ERROR', message: '联动刷新失败，请稍后重试' })
    } finally {
      const completedParticipantIDs = participantIDs.filter(id => latestRequestByComponentRef.current.get(id) === revision)
      for (const id of completedParticipantIDs) latestRequestByComponentRef.current.delete(id)
      setBusyComponentIDs(current => {
        const next = new Set(current)
        for (const id of completedParticipantIDs) next.delete(id)
        return next
      })
    }
  }

  function updateInteractiveRuntime(update: (current: ReportRuntimeContext) => ReportRuntimeContext) {
    setRuntimeState(current => {
      const base = current.source === runtime ? current.value : runtime
      return { source: runtime, value: update(base) }
    })
  }
  if (!activePage) return <ReportContractFailure issues={[{ path: 'pages', reason: '报告没有可渲染页面' }]} />
  const contentRows = mode === 'designer' ? Math.max(activePage.contentGridRows, designerContentRows?.[activePage.id] ?? 10) : activePage.contentGridRows
  const logicalHeight = contentRows * LOGICAL_ROW_HEIGHT
  const emptyCells = mode === 'designer' && onEmptyCellActivate ? deriveEmptyGridCells(activePage.blocks, contentRows) : []
  const renderedBlocks = activePage.blocks.filter(block => blockIsVisible(block) && canView(block.permissionPolicy, interactiveRuntime))
  const renderedBlockIDs = renderedBlocks.map(block => block.id)
  const renderedComponentIDs = renderedBlocks.flatMap(block => visibleComponentsForBlock(block)
    .filter(component => canView(component.permissionPolicy, interactiveRuntime))
    .map(component => component.id))
  const stickyPlan = mode === 'viewer'
    ? calculateReportStickyPlacements({ page: activePage, viewportTop: stickyViewportTop, scale, renderedBlockIDs, renderedComponentIDs, pageIndex: document.pages.indexOf(activePage) })
    : { placements: [], issues: [] }
  const stickyPlacements = new Map(stickyPlan.placements.map(placement => [placement.key, placement]))
  return (
    <section className={`report-renderer report-renderer--${mode}`} data-render-mode={mode} aria-label={`${document.report.name}报告内容`}>
      {interactionStatus && <div className={`report-interaction-message${interactionStatus.kind === 'ERROR' ? ' report-interaction-message--error' : ''}`} role={interactionStatus.kind === 'ERROR' ? 'alert' : 'status'}>{interactionStatus.message}</div>}
      {warnings.length > 0 && (
        <div className="report-contract-warning" role="status">
          检测到 {warnings.length} 个可降级兼容问题，未知组件已使用占位视图。
        </div>
      )}
      {pages.length > 1 && (
        <nav className="report-page-tabs" aria-label="报告页面">
          {pages.map(page => <button key={page.id} type="button" aria-current={page.id === activePage.id ? 'page' : undefined} onClick={() => setActivePageID(page.id)}>{page.name}</button>)}
        </nav>
      )}
      {mode === 'viewer' && (
        // 状态槽始终保留相同高度，告警出现时既不改变自身测量输入，也不会覆盖正文或键盘焦点。
        <div className="report-sticky-status-slot" style={{ height: 54 }}>
          {stickyPlan.issues.length > 0 && <div className="report-sticky-warning" role="status">{stickyPlan.issues.length} 个冻结项因容器或空间限制已恢复普通文档流。</div>}
        </div>
      )}
      <div className="report-page-viewport" ref={viewportRef}>
        <div className="report-page-stage" style={{ width: LOGICAL_CANVAS_WIDTH * scale, height: logicalHeight * scale }}>
          <div
            ref={canvasRef}
            className="report-page-canvas"
            data-page-id={activePage.id}
            data-content-rows={contentRows}
            data-canvas-scale={scale.toFixed(4)}
            style={{ '--report-content-rows': contentRows, width: LOGICAL_CANVAS_WIDTH, height: logicalHeight, transform: `scale(${scale})` } as CSSProperties}
          >
            {onEmptyCellActivate && emptyCells.map(cell => (
              <EmptyGridCellButton key={`${cell.x}-${cell.y}`} pageID={activePage.id} cell={cell} scale={scale} pendingComponentType={pendingComponentType} onActivate={onEmptyCellActivate} onDrop={onEmptyCellDrop} onPendingComponentConsumed={onPendingComponentConsumed} />
            ))}
            {activePage.blocks.map(block => mode === 'viewer' && !blockIsVisible(block) ? null : canView(block.permissionPolicy, interactiveRuntime)
              ? <ReportBlockView key={block.id} pageID={activePage.id} block={block} mode={mode} runtime={interactiveRuntime} parameters={document.parameters ?? []} registry={renderers} scale={scale} onBlockGridChange={onBlockGridChange} onComponentGridChange={onComponentGridChange} onComponentDrop={onComponentDrop} onComponentDuplicate={onComponentDuplicate} onComponentDelete={onComponentDelete} pendingComponentType={pendingComponentType} onPendingComponentConsumed={onPendingComponentConsumed} onBlockReset={onBlockReset} onComponentInteraction={handleComponentInteraction} busyComponentIDs={busyComponentIDs} drillLevels={drillLevels} selection={selection} onSelectionChange={onSelectionChange} stickyPlacements={stickyPlacements} />
              : <PermissionDeniedBlock key={block.id} block={block} />)}
          </div>
        </div>
      </div>
    </section>
  )
}

function rendererIdentityKey(value: object): number {
  const existing = rendererIdentityKeys.get(value)
  if (existing !== undefined) return existing
  const created = nextRendererIdentityKey
  nextRendererIdentityKey += 1
  rendererIdentityKeys.set(value, created)
  return created
}

type ReportBlockViewProps = {
  pageID: string
  block: ReportBlock
  mode: ReportRendererMode
  runtime: ReportRuntimeContext
  parameters: ReportParameter[]
  registry: ReportComponentRegistry
  scale: number
  onBlockGridChange?: ReportRendererProps['onBlockGridChange']
  onComponentGridChange?: ReportRendererProps['onComponentGridChange']
  onComponentDrop?: ReportRendererProps['onComponentDrop']
  onComponentDuplicate?: ReportRendererProps['onComponentDuplicate']
  onComponentDelete?: ReportRendererProps['onComponentDelete']
  pendingComponentType?: ComponentType
  onPendingComponentConsumed?: () => void
  onBlockReset?: ReportRendererProps['onBlockReset']
  onComponentInteraction: (componentID: string, event: ReportComponentInteractionEvent) => void
  busyComponentIDs: Set<string>
  drillLevels: Record<string, number>
  selection?: ReportSelection
  onSelectionChange?: (selection: ReportSelection) => void
  stickyPlacements: Map<string, ReportStickyPlacement>
}

function ReportBlockView({ pageID, block, mode, runtime, parameters, registry, scale, onBlockGridChange, onComponentGridChange, onComponentDrop, onComponentDuplicate, onComponentDelete, pendingComponentType, onPendingComponentConsumed, onBlockReset, onComponentInteraction, busyComponentIDs, drillLevels, selection, onSelectionChange, stickyPlacements }: ReportBlockViewProps) {
  const [interaction, setInteraction] = useState<{ mode: 'move' | 'resize'; startX: number; startY: number; grid: Grid; previewGrid: Grid; captureTarget: HTMLElement; pointerId: number }>()
  const stickyPlacement = stickyPlacements.get(reportStickyPlacementKey('BLOCK', block.id))
  const pageScopedChildSticky = block.components.some(component => {
    const placement = stickyPlacements.get(reportStickyPlacementKey('COMPONENT', component.id, block.id))
    return placement?.enabled === true && component.sticky.enabled
      && (component.sticky.scope === 'PAGE' || component.sticky.scope === 'CONTAINER' && component.sticky.containerId === pageID)
  })
  // 子组件的冻结层级只属于子项；把它提升到分块会让整个宿主遮挡其他分块。
  const displayGrid = interaction?.previewGrid ?? block.grid
  const style = gridStyle(displayGrid, stickyPlacement?.stuck ? stickyPlacement.zIndex : block.zIndex)
  if (stickyPlacement?.stuck) {
    style.transform = `translateY(${stickyPlacement.translateY}px)`
    style.willChange = 'transform'
  }
  const editable = mode === 'designer' && onBlockGridChange !== undefined && !block.locks.layout
  const resettable = mode === 'designer' && onBlockReset !== undefined && !block.locks.layout && !block.locks.config && !block.locks.dataSnapshot
  const selected = selection?.kind === 'BLOCK' && selection.pageID === pageID && selection.blockID === block.id

  function startInteraction(event: PointerEvent<HTMLElement>, interactionMode: 'move' | 'resize') {
    if (!editable) return
    if (interactionMode === 'move' && (event.target as HTMLElement).closest('button,input,select,textarea,a,[role="button"],[data-report-interactive]')) return
    event.preventDefault()
    event.currentTarget.setPointerCapture?.(event.pointerId)
    setInteraction({ mode: interactionMode, startX: event.clientX, startY: event.clientY, grid: block.grid, previewGrid: block.grid, captureTarget: event.currentTarget, pointerId: event.pointerId })
  }

  function updateInteraction(event: PointerEvent<HTMLElement>) {
    if (!interaction || !editable) return
    const delta = pointerDeltaToGrid(event.clientX - interaction.startX, event.clientY - interaction.startY, scale)
    const next = interaction.mode === 'move'
      ? { ...interaction.grid, x: interaction.grid.x + delta.columns, y: interaction.grid.y + delta.rows }
      : { ...interaction.grid, w: interaction.grid.w + delta.columns, h: interaction.grid.h + delta.rows }
    if (sameGrid(next, interaction.previewGrid)) return
    // 指针移动只更新本地预览；一次手势在释放时才形成一个历史和审计 Patch。
    setInteraction({ ...interaction, previewGrid: next })
  }

  function stopInteraction() {
    if (!interaction) return
    releaseCapturedPointer(interaction.captureTarget, interaction.pointerId)
    setInteraction(undefined)
    if (!sameGrid(interaction.previewGrid, interaction.grid)) onBlockGridChange?.(pageID, block.id, interaction.previewGrid)
  }

  function cancelInteraction() {
    if (!interaction) return
    releaseCapturedPointer(interaction.captureTarget, interaction.pointerId)
    setInteraction(undefined)
  }

  function handleKeyDown(event: KeyboardEvent<HTMLElement>) {
    if (!editable || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
    event.preventDefault()
    const horizontal = event.key === 'ArrowLeft' ? -1 : event.key === 'ArrowRight' ? 1 : 0
    const vertical = event.key === 'ArrowUp' ? -1 : event.key === 'ArrowDown' ? 1 : 0
    const next = event.shiftKey
      ? { ...block.grid, w: block.grid.w + horizontal, h: block.grid.h + vertical }
      : { ...block.grid, x: block.grid.x + horizontal, y: block.grid.y + vertical }
    onBlockGridChange(pageID, block.id, next)
  }

  function handleDragOver(event: React.DragEvent<HTMLElement>) {
    if (!onComponentDrop || block.locks.layout) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }

  function handleDrop(event: React.DragEvent<HTMLElement>) {
    if (!onComponentDrop || block.locks.layout) return
    const type = parseDraggedComponentType(event.dataTransfer.getData(REPORT_COMPONENT_DRAG_MIME))
    if (!type) return
    event.preventDefault()
    event.stopPropagation()
    const bounds = event.currentTarget.getBoundingClientRect()
    const anchor = pointerPositionToInnerGrid(event.clientX - bounds.left, event.clientY - bounds.top, scale)
    onComponentDrop(pageID, block.id, type, anchor)
  }

  function handleClick(event: React.MouseEvent<HTMLElement>) {
    if (!pendingComponentType || !onComponentDrop || block.locks.layout) return
    if ((event.target as HTMLElement).closest('button,input,select,textarea,a,[role="button"],[data-report-interactive],[data-component-id]')) return
    const bounds = event.currentTarget.getBoundingClientRect()
    const anchor = pointerPositionToInnerGrid(event.clientX - bounds.left, event.clientY - bounds.top, scale)
    onComponentDrop(pageID, block.id, pendingComponentType, anchor)
    onPendingComponentConsumed?.()
  }

  return (
    <article
      className={`report-block report-block--${(block.kind ?? 'GENERIC').toLowerCase()}${editable ? ' report-block--editable' : ''}${interaction ? ' report-block--interacting' : ''}${selected ? ' report-block--selected' : ''}${stickyPlacement?.enabled ? ' report-block--sticky' : ''}${stickyPlacement?.stuck ? ' report-block--stuck' : ''}${pageScopedChildSticky ? ' report-block--sticky-host' : ''}${!blockIsVisible(block) ? ' report-block--hidden' : ''}`}
      data-block-id={block.id}
      data-selected={selected ? 'true' : undefined}
      data-sticky-state={stickyState(stickyPlacement)}
      data-sticky-translate={stickyPlacement?.enabled ? stickyPlacement.translateY.toFixed(3) : undefined}
      style={style}
      tabIndex={mode === 'designer' ? 0 : undefined}
      aria-label={mode === 'designer' ? `分块 ${block.id}，位置 ${displayGrid.x + 1} 列 ${displayGrid.y + 1} 行，尺寸 ${displayGrid.w} × ${displayGrid.h}` : undefined}
      onFocus={event => { if (event.target === event.currentTarget) onSelectionChange?.({ kind: 'BLOCK', pageID, blockID: block.id }) }}
      onPointerDown={event => { onSelectionChange?.({ kind: 'BLOCK', pageID, blockID: block.id }); startInteraction(event, 'move') }}
      onPointerMove={updateInteraction}
      onPointerUp={stopInteraction}
      onPointerCancel={cancelInteraction}
      onKeyDown={handleKeyDown}
      onDragOver={handleDragOver}
      onDrop={handleDrop}
      onClick={handleClick}
    >
      {mode === 'designer' && <span className="report-block-label">{block.name ?? block.id} · {displayGrid.w} × {displayGrid.h}</span>}
      {resettable && (
        <div className="report-block-actions" aria-label={`分块 ${block.id} 操作`}>
          <button type="button" aria-label={`清空分块 ${block.id}`} onPointerDown={event => event.stopPropagation()} onClick={() => onBlockReset(pageID, block.id, 'CLEAR')}>清空</button>
          <button type="button" aria-label={`删除分块 ${block.id}`} onPointerDown={event => event.stopPropagation()} onClick={() => onBlockReset(pageID, block.id, 'DELETE')}>删除</button>
        </div>
      )}
      {block.kind === 'MENU' && block.menuLayout
        ? <ReportMenuView block={block} mode={mode} runtime={runtime} parameters={parameters} />
        : (
          <div className="report-inner-grid" style={{ '--inner-columns': block.innerGrid.columns, '--inner-rows': block.innerGrid.rows } as CSSProperties}>
            {visibleComponentsForBlock(block).map(component => (
              <ReportComponentView key={component.id} pageID={pageID} block={block} component={component} mode={mode} runtime={runtime} parameters={parameters} registry={registry} scale={scale} onComponentGridChange={onComponentGridChange} onComponentDuplicate={onComponentDuplicate} onComponentDelete={onComponentDelete} onInteraction={event => onComponentInteraction(component.id, event)} interactionBusy={busyComponentIDs.has(component.id)} drillLevel={drillLevels[component.id] ?? -1} selected={selection?.kind === 'COMPONENT' && selection.pageID === pageID && selection.blockID === block.id && selection.componentID === component.id} onSelectionChange={onSelectionChange} stickyPlacement={stickyPlacements.get(reportStickyPlacementKey('COMPONENT', component.id, block.id))} />
            ))}
            {mode === 'designer' && block.contentLayout && Object.entries(block.contentLayout.areas)
              .filter(([, area]) => !area.visible)
              .map(([areaName, area]) => <HiddenContentArea key={areaName} name={areaName} componentIDs={area.componentIds} block={block} />)}
          </div>
        )}
      {editable && <button className="report-resize-handle" type="button" aria-label={`调整分块 ${block.id} 尺寸`} onPointerDown={event => { event.stopPropagation(); startInteraction(event, 'resize') }}>↘</button>}
    </article>
  )
}

function ReportMenuView({ block, mode, runtime, parameters }: { block: ReportBlock; mode: ReportRendererMode; runtime: ReportRuntimeContext; parameters: ReportParameter[] }) {
  const layout = block.menuLayout!
  const topVisible = [layout.cells.logoTitle.visible, layout.cells.actions.visible]
  const bottomVisible = [layout.cells.globalFilters.visible, layout.cells.navigation.visible]
  const bottomEmpty = !bottomVisible[0] && !bottomVisible[1]
  const rowStyle = {
    gridTemplateRows: bottomEmpty ? '1fr 0fr' : `${layout.ratios.rowHeights[0]}fr ${layout.ratios.rowHeights[1]}fr`,
  }
  const topStyle = {
    gridTemplateColumns: topVisible.every(Boolean) ? `${layout.ratios.topColumns[0]}fr ${layout.ratios.topColumns[1]}fr` : '1fr',
  }
  const bottomStyle = {
    gridTemplateColumns: bottomVisible.every(Boolean) ? `${layout.ratios.bottomColumns[0]}fr ${layout.ratios.bottomColumns[1]}fr` : '1fr',
  }
  // 隐藏宫格不保留占位节点，否则单个可见宫格无法真正横向填满该行。
  const hiddenCell = null
  return (
    <div className="report-menu-layout" style={rowStyle} data-default-ratios={layout.usesDefaultRatios ? 'true' : 'false'}>
      <div className="report-menu-row report-menu-row--top" style={topStyle}>
        {layout.cells.logoTitle.visible ? (
          <div className="report-menu-cell report-menu-brand">
            <strong>{layout.cells.logoTitle.logoText}</strong>
            <div><h2>{layout.cells.logoTitle.title}</h2>{layout.cells.logoTitle.subtitle && <span>{layout.cells.logoTitle.subtitle}</span>}</div>
          </div>
        ) : hiddenCell}
        {layout.cells.actions.visible ? (
          <div className="report-menu-cell report-menu-actions">
            {layout.cells.actions.items.map(item => <button key={item} type="button" disabled={mode === 'designer'}>{menuActionIcon(item)}<span>{item}</span></button>)}
          </div>
        ) : hiddenCell}
      </div>
      {!bottomEmpty || mode === 'designer' ? (
        <div className={`report-menu-row report-menu-row--bottom${bottomEmpty ? ' report-menu-row--collapsed' : ''}`} style={bottomStyle}>
          {layout.cells.globalFilters.visible ? (
            <div className="report-menu-cell report-menu-filters">
              {layout.cells.globalFilters.parameterIds.map(parameterID => {
                const parameter = parameters.find(item => item.id === parameterID)
                if (!parameter) return <span key={parameterID}>未找到筛选参数</span>
                const options = runtime.parameterOptions?.[parameter.code]?.options ?? []
                return <label key={parameterID}><span>{parameter.name}</span><select value={String(runtime.parameters[parameter.code] ?? '')} disabled aria-label={`全局筛选 ${parameter.name}`}><option value="">全部</option>{options.map(option => <option key={String(option.value)} value={String(option.value)}>{option.label}</option>)}</select><CaretDown size={13} /></label>
              })}
            </div>
          ) : hiddenCell}
          {layout.cells.navigation.visible ? (
            <nav className="report-menu-cell report-menu-navigation" aria-label="报告导航">
              {layout.cells.navigation.items.map((item, index) => <button key={`${index}-${item.label}`} type="button" className={index === 0 ? 'is-active' : ''}>{item.label}</button>)}
            </nav>
          ) : hiddenCell}
        </div>
      ) : null}
    </div>
  )
}

function menuActionIcon(label: string) {
  if (label.includes('刷新')) return <ArrowClockwise size={16} />
  if (label.includes('导出')) return <DownloadSimple size={16} />
  return <DotsThree size={18} />
}

function HiddenContentArea({ name, componentIDs, block }: { name: string; componentIDs: string[]; block: ReportBlock }) {
  const components = block.components.filter(component => componentIDs.includes(component.id))
  if (components.length === 0) return null
  const left = Math.min(...components.map(component => component.grid.x))
  const top = Math.min(...components.map(component => component.grid.y))
  const right = Math.max(...components.map(component => component.grid.x + component.grid.w))
  const bottom = Math.max(...components.map(component => component.grid.y + component.grid.h))
  const labels: Record<string, string> = { title: '标题区（标题 + 筛选）', conclusion: '结论区', components: '组件图' }
  return <div className="report-content-area-hidden" style={gridStyle({ x: left, y: top, w: right - left, h: bottom - top })}><span>{labels[name] ?? name}已隐藏</span></div>
}

function visibleComponentsForBlock(block: ReportBlock): ReportComponent[] {
  const hiddenIDs = new Set(Object.values(block.contentLayout?.areas ?? {})
    .filter(area => !area.visible)
    .flatMap(area => area.componentIds))
  return block.components.filter(component => component.visible && !hiddenIDs.has(component.id))
}

function blockIsVisible(block: ReportBlock): boolean {
  if (block.visible === false) return false
  if (block.kind === 'MENU') return block.menuLayout?.visible !== false
  if (block.kind === 'CONTENT') return block.contentLayout?.visible !== false
  return true
}

function EmptyGridCellButton({ pageID, cell, scale, pendingComponentType, onActivate, onDrop, onPendingComponentConsumed }: { pageID: string; cell: EmptyGridCell; scale: number; pendingComponentType?: ComponentType; onActivate: NonNullable<ReportRendererProps['onEmptyCellActivate']>; onDrop?: ReportRendererProps['onEmptyCellDrop']; onPendingComponentConsumed?: () => void }) {
  function handleClick() {
    if (pendingComponentType && onDrop) {
      onDrop(pageID, cell.x, cell.y, pendingComponentType)
      onPendingComponentConsumed?.()
      return
    }
    onActivate(pageID, cell.x, cell.y)
  }

  function handleDrop(event: React.DragEvent<HTMLButtonElement>) {
    if (!onDrop) return
    const type = parseDraggedComponentType(event.dataTransfer.getData(REPORT_COMPONENT_DRAG_MIME))
    if (!type) return
    event.preventDefault()
    onDrop(pageID, cell.x, cell.y, type)
  }

  return (
    <button
      className="report-empty-cell"
      type="button"
      style={gridStyle({ ...cell, w: 1, h: 1 })}
      aria-label={`空白单元，第 ${cell.x + 1} 列，第 ${cell.y + 1} 行`}
      data-cell-scale={scale.toFixed(4)}
      onClick={handleClick}
      onDragOver={event => { if (onDrop) event.preventDefault() }}
      onDrop={handleDrop}
    />
  )
}

function ReportComponentView({ pageID, block, component, mode, runtime, parameters, registry, scale, onComponentGridChange, onComponentDuplicate, onComponentDelete, onInteraction, interactionBusy, drillLevel, selected, onSelectionChange, stickyPlacement }: { pageID: string; block: ReportBlock; component: ReportComponent; mode: ReportRendererMode; runtime: ReportRuntimeContext; parameters: ReportParameter[]; registry: ReportComponentRegistry; scale: number; onComponentGridChange?: ReportRendererProps['onComponentGridChange']; onComponentDuplicate?: ReportRendererProps['onComponentDuplicate']; onComponentDelete?: ReportRendererProps['onComponentDelete']; onInteraction: (event: ReportComponentInteractionEvent) => void; interactionBusy: boolean; drillLevel: number; selected: boolean; onSelectionChange?: (selection: ReportSelection) => void; stickyPlacement?: ReportStickyPlacement }) {
  const Renderer = canView(component.permissionPolicy, runtime) ? registry[component.type] ?? UnknownComponentRenderer : PermissionDeniedComponent
  const [interaction, setInteraction] = useState<{ mode: 'move' | 'resize'; startX: number; startY: number; grid: Grid; previewGrid: Grid; captureTarget: HTMLElement; pointerId: number }>()
  const editable = mode === 'designer' && onComponentGridChange !== undefined && !block.locks.layout && !component.manualLocked
  const parameterID = component.type === 'FILTER'
    ? readString(component.binding, 'parameterId')
    : readString(readRecord(component.interaction?.linkage), 'parameterId')
  const parameter = parameters.find(item => item.id === parameterID)
  const displayGrid = interaction?.previewGrid ?? component.grid
  const style = gridStyle(displayGrid, stickyPlacement?.stuck ? stickyPlacement.zIndex : component.zIndex)
  if (stickyPlacement?.stuck) {
    style.transform = `translateY(${stickyPlacement.translateY}px)`
    style.willChange = 'transform'
  }

  function startInteraction(event: PointerEvent<HTMLElement>, interactionMode: 'move' | 'resize') {
    if (mode === 'designer') event.stopPropagation()
    if (!editable) return
    if (interactionMode === 'move' && (event.target as HTMLElement).closest('button,input,select,textarea,a,[role="button"],[data-report-interactive]')) return
    event.preventDefault()
    event.currentTarget.setPointerCapture?.(event.pointerId)
    setInteraction({ mode: interactionMode, startX: event.clientX, startY: event.clientY, grid: component.grid, previewGrid: component.grid, captureTarget: event.currentTarget, pointerId: event.pointerId })
  }

  function updateInteraction(event: PointerEvent<HTMLElement>) {
    if (!interaction || !editable) return
    const delta = pointerDeltaToInnerGrid(event.clientX - interaction.startX, event.clientY - interaction.startY, scale)
    const next = interaction.mode === 'move'
      ? { ...interaction.grid, x: interaction.grid.x + delta.columns, y: interaction.grid.y + delta.rows }
      : { ...interaction.grid, w: interaction.grid.w + delta.columns, h: interaction.grid.h + delta.rows }
    if (sameGrid(next, interaction.previewGrid)) return
    // 组件拖拽与分块保持同一语义：移动中预览，释放后只提交最终坐标。
    setInteraction({ ...interaction, previewGrid: next })
  }

  function stopInteraction() {
    if (!interaction) return
    releaseCapturedPointer(interaction.captureTarget, interaction.pointerId)
    setInteraction(undefined)
    if (!sameGrid(interaction.previewGrid, interaction.grid)) onComponentGridChange?.(pageID, block.id, component.id, interaction.previewGrid)
  }

  function cancelInteraction() {
    if (!interaction) return
    releaseCapturedPointer(interaction.captureTarget, interaction.pointerId)
    setInteraction(undefined)
  }

  function handleKeyDown(event: KeyboardEvent<HTMLElement>) {
    if (mode === 'designer') event.stopPropagation()
    if (!editable) return
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'd' && onComponentDuplicate) {
      event.preventDefault()
      onComponentDuplicate(pageID, block.id, component.id)
      return
    }
    if ((event.key === 'Delete' || event.key === 'Backspace') && onComponentDelete) {
      event.preventDefault()
      onComponentDelete(pageID, block.id, component.id)
      return
    }
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
    event.preventDefault()
    const horizontal = event.key === 'ArrowLeft' ? -1 : event.key === 'ArrowRight' ? 1 : 0
    const vertical = event.key === 'ArrowUp' ? -1 : event.key === 'ArrowDown' ? 1 : 0
    const next = event.shiftKey
      ? { ...component.grid, w: component.grid.w + horizontal, h: component.grid.h + vertical }
      : { ...component.grid, x: component.grid.x + horizontal, y: component.grid.y + vertical }
    onComponentGridChange(pageID, block.id, component.id, next)
  }

  return (
    <section
      className={`report-component report-component--${component.type.toLowerCase()}${editable ? ' report-component--editable' : ''}${interaction ? ' report-component--interacting' : ''}${selected ? ' report-component--selected' : ''}${stickyPlacement?.enabled ? ' report-component--sticky' : ''}${stickyPlacement?.stuck ? ' report-component--stuck' : ''}`}
      data-component-id={component.id}
      data-selected={selected ? 'true' : undefined}
      data-sticky-state={stickyState(stickyPlacement)}
      data-sticky-translate={stickyPlacement?.enabled ? stickyPlacement.translateY.toFixed(3) : undefined}
      style={style}
      tabIndex={mode === 'designer' ? 0 : undefined}
      aria-label={mode === 'designer' ? `组件 ${component.name}，位置 ${displayGrid.x + 1} 列 ${displayGrid.y + 1} 行，尺寸 ${displayGrid.w} × ${displayGrid.h}` : undefined}
      onFocus={() => onSelectionChange?.({ kind: 'COMPONENT', pageID, blockID: block.id, componentID: component.id })}
      onPointerDown={event => { onSelectionChange?.({ kind: 'COMPONENT', pageID, blockID: block.id, componentID: component.id }); startInteraction(event, 'move') }}
      onPointerMove={updateInteraction}
      onPointerUp={stopInteraction}
      onPointerCancel={cancelInteraction}
      onKeyDown={handleKeyDown}
    >
      <ReportErrorBoundary componentName={component.name} resetKey={runtime.componentData[component.id]}>
        <Renderer component={component} runtime={runtime} mode={mode} parameter={parameter} onInteraction={onInteraction} interactionBusy={interactionBusy} drillLevel={drillLevel} />
      </ReportErrorBoundary>
      {editable && (
        <>
          <div className="report-component-actions" aria-label={`${component.name}组件操作`}>
            <button type="button" aria-label={`复制组件 ${component.name}`} onPointerDown={event => event.stopPropagation()} onClick={() => onComponentDuplicate?.(pageID, block.id, component.id)}>复制</button>
            <button type="button" aria-label={`删除组件 ${component.name}`} onPointerDown={event => event.stopPropagation()} onClick={() => onComponentDelete?.(pageID, block.id, component.id)}>删除</button>
          </div>
          <button className="report-component-resize-handle" type="button" aria-label={`调整组件 ${component.name} 尺寸`} onPointerDown={event => startInteraction(event, 'resize')}>↘</button>
        </>
      )}
    </section>
  )
}

function PermissionDeniedComponent() {
  return <div className="report-component-state"><strong>无权查看此组件</strong><span>请联系报告管理员申请权限。</span></div>
}

function PermissionDeniedBlock({ block }: { block: ReportBlock }) {
  return <article className="report-block" style={gridStyle(block.grid, block.zIndex)}><div className="report-component-state"><strong>无权查看此分块</strong></div></article>
}

export function ReportContractFailure({ issues }: { issues: ReportValidationIssue[] }) {
  return (
    <section className="report-contract-failure" role="alert">
      <span>REPORT JSON ERROR</span>
      <h2>报告配置无法加载</h2>
      <p>版本化报告 JSON 未通过合同校验，已停止渲染以避免展示不完整内容。</p>
      <ul>{issues.slice(0, 5).map(issue => <li key={`${issue.path}-${issue.reason}`}><code>{issue.path}</code>：{issue.reason}</li>)}</ul>
    </section>
  )
}

function stickyState(placement?: ReportStickyPlacement): 'armed' | 'stuck' | 'boundary' | 'fallback' | undefined {
  if (!placement) return undefined
  if (!placement.enabled) return 'fallback'
  if (placement.atBoundary) return 'boundary'
  return placement.stuck ? 'stuck' : 'armed'
}

/** 找到真实裁剪纵向内容的最近祖先；查看器没有内嵌滚动区时退回窗口。 */
function findVerticalScrollContainer(element: HTMLElement): HTMLElement | Window {
  for (let current = element.parentElement; current; current = current.parentElement) {
    const overflowY = window.getComputedStyle(current).overflowY
    if (/(auto|scroll|overlay)/.test(overflowY) && current.scrollHeight > current.clientHeight) return current
  }
  return window
}

function gridStyle(grid: { x: number; y: number; w: number; h: number }, zIndex?: number): CSSProperties {
  const style: CSSProperties = {
    gridColumn: `${grid.x + 1} / span ${grid.w}`,
    gridRow: `${grid.y + 1} / span ${grid.h}`,
  }
  // 未配置层级时不写入 z-index，避免普通网格项意外形成独立层叠组。
  if (zIndex !== undefined) style.zIndex = zIndex
  return style
}

function sameGrid(left: Grid, right: Grid): boolean {
  return left.x === right.x && left.y === right.y && left.w === right.w && left.h === right.h
}

/** pointercancel 可能先丢失捕获；只释放仍由当前节点持有的指针，避免抛出 DOMException。 */
function releaseCapturedPointer(target: HTMLElement, pointerId: number) {
  if (!target.releasePointerCapture) return
  if (target.hasPointerCapture && !target.hasPointerCapture(pointerId)) return
  target.releasePointerCapture(pointerId)
}

function canView(policy: Record<string, unknown> | undefined, runtime: ReportRuntimeContext): boolean {
  const required = policy?.requiredPermission
  const allowedRoles = Array.isArray(policy?.allowedRoleCodes)
    ? policy.allowedRoleCodes.filter(role => typeof role === 'string') as string[]
    : []
  const hasPermission = typeof required !== 'string' || runtime.permissions?.includes(required) === true
  const hasAllowedRole = allowedRoles.length === 0 || allowedRoles.some(role => runtime.roleCodes?.includes(role) === true)
  return hasPermission && hasAllowedRole
}

function readRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function readString(value: unknown, key: string): string | undefined {
  const candidate = readRecord(value)?.[key]
  return typeof candidate === 'string' ? candidate : undefined
}
