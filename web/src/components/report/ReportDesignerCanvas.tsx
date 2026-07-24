import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import { CaretDown, ChartLineUp, Eye, EyeSlash, GearSix, GridFour, ListChecks, SlidersHorizontal, TextT, TreeStructure } from '@phosphor-icons/react'
import type { BlockSticky, ComponentSticky, ComponentType, Grid, ReportBlock, ReportDocument, ReportRuntimeContext, ReportSelection, ReportValidationIssue, Sticky } from '../../lib/report-contract'
import type { ReportDraftChange, ReportEditorState } from '../../lib/report-drafts'
import { acknowledgeReportEditorChanges, commitReportEditorHistory, createReportEditorHistory, redoReportEditorHistory, undoReportEditorHistory, type ReportEditorHistory, type ReportEditorOperationInput, type ReportEditorSnapshot } from '../../lib/report-history'
import { addComponent, createBlockAtCell, createBlockWithComponent, deleteComponent, duplicateComponent, MAX_EDITOR_CONTENT_ROWS, MAX_STICKY_TOP, MAX_STICKY_Z_INDEX, resetBlock, updateBlockDefinition, updateBlockGrid, updateBlockSticky, updateComponentGrid, updateComponentSticky, type BlockResetMode, type LayoutUpdateResult } from '../../lib/report-layout'
import { validateReportDocument } from '../../lib/report-schema'
import { ComponentPalette } from './ComponentPalette'
import { ReportContractFailure, ReportRenderer } from './ReportRenderer'

export type ReportDesignerCanvasProps = {
  source: unknown
  runtime: ReportRuntimeContext
  onChange?: (document: ReportDocument) => void
  onTransition?: (transition: ReportDesignerTransition) => void
  initialEditorState?: ReportEditorState
  initialPendingChanges?: ReportDraftChange[]
  acknowledgedClientOperationIds?: readonly string[]
  loadGeneration?: string | number
  pendingComponentType?: ComponentType
  onPendingComponentTypeChange?: (type: ComponentType) => void
  onPendingComponentConsumed?: () => void
}

export type ReportDesignerTransition = {
  document: ReportDocument
  editorState: ReportEditorState
  pendingChanges: ReportDraftChange[]
}

/** 校验服务端草稿，并在内存中维护 Patch 历史；浏览器会话不再是报告事实来源。 */
export function ReportDesignerCanvas({ source, runtime, onChange, onTransition, initialEditorState, initialPendingChanges, acknowledgedClientOperationIds, loadGeneration = 0, pendingComponentType, onPendingComponentTypeChange, onPendingComponentConsumed }: ReportDesignerCanvasProps) {
  const validation = validateReportDocument(source)
  if (!validation.document) return <ReportContractFailure issues={validation.errors} />
  return <EditableDocument key={loadGeneration} initialDocument={validation.document} initialEditorState={initialEditorState} initialPendingChanges={initialPendingChanges} runtime={runtime} warnings={validation.warnings} onChange={onChange} onTransition={onTransition} acknowledgedClientOperationIds={acknowledgedClientOperationIds} pendingComponentType={pendingComponentType} onPendingComponentTypeChange={onPendingComponentTypeChange} onPendingComponentConsumed={onPendingComponentConsumed} />
}

function EditableDocument({ initialDocument, initialEditorState, initialPendingChanges, runtime, warnings, onChange, onTransition, acknowledgedClientOperationIds, pendingComponentType, onPendingComponentTypeChange, onPendingComponentConsumed }: { initialDocument: ReportDocument; initialEditorState?: ReportEditorState; initialPendingChanges?: ReportDraftChange[]; runtime: ReportRuntimeContext; warnings: ReportValidationIssue[]; onChange?: (document: ReportDocument) => void; onTransition?: (transition: ReportDesignerTransition) => void; acknowledgedClientOperationIds?: readonly string[]; pendingComponentType?: ComponentType; onPendingComponentTypeChange?: (type: ComponentType) => void; onPendingComponentConsumed?: () => void }) {
  const [history, setHistory] = useState(() => ({
    ...createReportEditorHistory(createSnapshot(initialDocument, initialEditorState?.minimumRowsByPage, initialPendingChanges?.length ? '恢复旧会话草稿' : '初始状态')),
    // 恢复操作由页面在用户明确确认后生成，Canvas 只把它纳入同一保存队列。
    pendingChanges: structuredClone(initialPendingChanges ?? []),
  }))
  const [issue, setIssue] = useState<ReportValidationIssue>()
  const [pendingReset, setPendingReset] = useState<{ pageID: string; blockID: string; mode: BlockResetMode; componentCount: number }>()
  const [selection, setSelection] = useState<ReportSelection | undefined>(() => firstBlockSelection(initialDocument))
  const historyRef = useRef(history)
  const document = history.present.document
  const canEdit = runtime.permissions?.includes('report:edit') === true
  const effectiveSelection = resolveSelection(document, selection) ? selection : firstBlockSelection(document)
  const selectedTarget = resolveSelection(document, effectiveSelection)

  const emitTransition = useCallback((nextHistory: ReportEditorHistory) => {
    onTransition?.({
      document: structuredClone(nextHistory.present.document),
      editorState: { minimumRowsByPage: { ...nextHistory.present.minimumRowsByPage } },
      pendingChanges: structuredClone(nextHistory.pendingChanges),
    })
  }, [onTransition])

  const applyHistory = useCallback((nextHistory: ReportEditorHistory) => {
    if (nextHistory === historyRef.current) return
    historyRef.current = nextHistory
    setHistory(nextHistory)
    onChange?.(structuredClone(nextHistory.present.document))
    emitTransition(nextHistory)
  }, [emitTransition, onChange])

  useEffect(() => {
    if (!acknowledgedClientOperationIds?.length) return
    // 仅移除服务端已确认的操作；保存期间产生的新 Patch 与现有撤销栈都必须保留。
    const nextHistory = acknowledgeReportEditorChanges(historyRef.current, acknowledgedClientOperationIds)
    if (nextHistory === historyRef.current) return
    historyRef.current = nextHistory
    setHistory(nextHistory)
    emitTransition(nextHistory)
  }, [acknowledgedClientOperationIds, emitTransition])

  useEffect(() => {
    if (!canEdit) return
    function handleHistoryShortcut(event: globalThis.KeyboardEvent) {
      if (!(event.ctrlKey || event.metaKey)) return
      if (event.target instanceof Element && event.target.closest('input,textarea,select,[role="dialog"]')) return
      const key = event.key.toLowerCase()
      if (key === 'z') {
        event.preventDefault()
        setIssue(undefined)
        applyHistory(event.shiftKey ? redoReportEditorHistory(historyRef.current) : undoReportEditorHistory(historyRef.current))
      } else if (key === 'y') {
        event.preventDefault()
        setIssue(undefined)
        applyHistory(redoReportEditorHistory(historyRef.current))
      }
    }
    window.addEventListener('keydown', handleHistoryShortcut)
    return () => window.removeEventListener('keydown', handleHistoryShortcut)
  }, [applyHistory, canEdit])

  function handleBlockGridChange(pageID: string, blockID: string, grid: Grid) {
    const current = document.pages.find(page => page.id === pageID)?.blocks.find(block => block.id === blockID)?.grid
    const resized = current && (current.w !== grid.w || current.h !== grid.h)
    const summary = resized ? '调整分块尺寸' : '调整分块位置'
    commit(updateBlockGrid(document, pageID, blockID, grid), {
      operationType: resized ? 'BLOCK_RESIZE' : 'BLOCK_MOVE', summary, target: { pageId: pageID, blockId: blockID },
    })
  }

  function handleComponentGridChange(pageID: string, blockID: string, componentID: string, grid: Grid) {
    const current = document.pages.find(page => page.id === pageID)?.blocks.find(block => block.id === blockID)?.components.find(component => component.id === componentID)?.grid
    const resized = current && (current.w !== grid.w || current.h !== grid.h)
    const summary = resized ? '调整组件尺寸' : '调整组件位置'
    commit(updateComponentGrid(document, pageID, blockID, componentID, grid), {
      operationType: resized ? 'COMPONENT_RESIZE' : 'COMPONENT_MOVE', summary, target: { pageId: pageID, blockId: blockID, componentId: componentID },
    })
  }

  function handleComponentDrop(pageID: string, blockID: string, type: ComponentType, anchor: Pick<Grid, 'x' | 'y'>) {
    const result = addComponent(document, pageID, blockID, type, anchor)
    commit(result, {
      operationType: 'COMPONENT_CREATE', summary: '新增组件',
      target: { pageId: pageID, blockId: blockID, componentId: result.componentID, createdComponentId: result.componentID },
    })
  }

  function handleComponentDuplicate(pageID: string, blockID: string, componentID: string) {
    const result = duplicateComponent(document, pageID, blockID, componentID)
    commit(result, {
      operationType: 'COMPONENT_COPY', summary: '复制组件',
      target: { pageId: pageID, blockId: blockID, componentId: result.componentID, sourceComponentId: componentID, createdComponentId: result.componentID },
    })
  }

  function handleComponentDelete(pageID: string, blockID: string, componentID: string) {
    commit(deleteComponent(document, pageID, blockID, componentID), {
      operationType: 'COMPONENT_DELETE', summary: '删除组件', target: { pageId: pageID, blockId: blockID, componentId: componentID },
    })
  }

  function handleBlockStickyChange(sticky: BlockSticky) {
    if (selectedTarget?.kind !== 'BLOCK') return
    commit(updateBlockSticky(document, selectedTarget.page.id, selectedTarget.block.id, sticky), {
      operationType: 'BLOCK_STICKY_UPDATE', summary: '调整分块浏览态冻结', target: { pageId: selectedTarget.page.id, blockId: selectedTarget.block.id },
    })
  }

  function handleComponentStickyChange(sticky: ComponentSticky) {
    if (selectedTarget?.kind !== 'COMPONENT') return
    commit(updateComponentSticky(document, selectedTarget.page.id, selectedTarget.block.id, selectedTarget.component.id, sticky), {
      operationType: 'COMPONENT_STICKY_UPDATE', summary: '调整组件浏览态冻结', target: { pageId: selectedTarget.page.id, blockId: selectedTarget.block.id, componentId: selectedTarget.component.id },
    })
  }

  function handleBlockDefinitionChange(nextBlock: ReportBlock, summary: string) {
    const target = selectedTarget?.block
    if (!target) return
    commit(updateBlockDefinition(document, selectedTarget.page.id, target.id, () => nextBlock), {
      operationType: 'BLOCK_CONFIG_UPDATE',
      summary,
      target: { pageId: selectedTarget.page.id, blockId: target.id },
    })
  }

  function handleBlockReset(pageID: string, blockID: string, mode: BlockResetMode) {
    if (!canEdit) return setIssue({ path: '$', reason: '当前用户没有报告编辑权限' })
    const block = document.pages.find(page => page.id === pageID)?.blocks.find(item => item.id === blockID)
    if (!block) return setIssue({ path: 'pages', reason: `分块 ${blockID} 不存在` })
    if (block.components.length > 0) {
      setPendingReset({ pageID, blockID, mode, componentCount: block.components.length })
      return
    }
    executeBlockReset(pageID, blockID, mode)
  }

  function executeBlockReset(pageID: string, blockID: string, mode: BlockResetMode) {
    const action = mode === 'CLEAR' ? '清空' : '删除'
    setPendingReset(undefined)
    commit(resetBlock(document, pageID, blockID), {
      operationType: mode === 'CLEAR' ? 'BLOCK_CLEAR' : 'BLOCK_DELETE', summary: `${action}分块`, target: { pageId: pageID, blockId: blockID },
    }, pageID)
  }

  function handleEmptyCellActivate(pageID: string, x: number, y: number) {
    const result = createBlockAtCell(document, pageID, { x, y })
    commit(result, {
      operationType: 'BLOCK_CREATE', summary: '创建基础分块', target: { pageId: pageID, blockId: result.blockID },
    })
  }

  function handleEmptyCellDrop(pageID: string, x: number, y: number, type: ComponentType) {
    const result = createBlockWithComponent(document, pageID, { x, y }, type)
    commit(result, {
      operationType: 'BLOCK_CREATE', summary: '在空白单元新增组件',
      target: { pageId: pageID, blockId: result.blockID, componentId: result.componentID, createdComponentId: result.componentID },
    })
  }

  function commit(result: LayoutUpdateResult, operation: ReportEditorOperationInput, vacatedPageID?: string) {
    if (!canEdit) {
      setIssue({ path: '$', reason: '当前用户没有报告编辑权限' })
      return
    }
    if (!result.document) {
      setIssue(result.issue)
      return
    }
    setIssue(undefined)
    const currentHistory = historyRef.current
    const minimumRowsByPage = { ...currentHistory.present.minimumRowsByPage }
    if (result.vacatedGrid && vacatedPageID) {
      minimumRowsByPage[vacatedPageID] = Math.max(minimumRowsByPage[vacatedPageID] ?? 10, result.vacatedGrid.y + result.vacatedGrid.h)
    }
    applyHistory(commitReportEditorHistory(currentHistory, { document: result.document, minimumRowsByPage, operation: operation.summary }, operation))
  }

  function undo() {
    setIssue(undefined)
    applyHistory(undoReportEditorHistory(historyRef.current))
  }

  function redo() {
    setIssue(undefined)
    applyHistory(redoReportEditorHistory(historyRef.current))
  }

  function handleResetDialogKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    if (event.key === 'Escape') {
      event.preventDefault()
      setPendingReset(undefined)
      return
    }
    if (event.key !== 'Tab') return
    const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('button')]
    const first = buttons[0]
    const last = buttons.at(-1)
    if (event.shiftKey && globalThis.document.activeElement === first) {
      event.preventDefault()
      last?.focus()
    } else if (!event.shiftKey && globalThis.document.activeElement === last) {
      event.preventDefault()
      first?.focus()
    }
  }

  return (
    <>
      <div className="report-designer-workbench">
        <aside className="report-structure-panel">
          <header><TreeStructure size={18} weight="duotone" /><div><strong>页面结构</strong><span>JSON 层级</span></div></header>
          <DesignerStructureTree
            document={document}
            selection={effectiveSelection}
            canEdit={canEdit}
            onSelectionChange={setSelection}
            onBlockChange={handleBlockDefinitionChange}
          />
          <details className="report-component-library">
            <summary><GridFour size={16} />组件库<CaretDown size={14} /></summary>
            <p>拖入内容区，或选择后点击空白分格。</p>
            <ComponentPalette selectedType={pendingComponentType} onSelect={onPendingComponentTypeChange} />
          </details>
        </aside>
        <main className="report-designer-center">
          <div className="report-history-toolbar" aria-label="报告编辑历史">
            <div><strong>设计画板</strong><span>160 × 108 分格 · 12 列 · 纵向自动扩展</span></div>
            <button type="button" disabled={!canEdit || history.past.length === 0} onClick={undo}>撤销</button>
            <button type="button" disabled={!canEdit || history.future.length === 0} onClick={redo}>重做</button>
            <span>当前：{history.present.operation}</span>
          </div>
          {!canEdit && <div className="report-layout-issue" role="status">当前账号仅可查看报告，没有编辑权限。</div>}
          {issue && <div className="report-layout-issue" role="alert"><code>{issue.path}</code>：{issue.reason}</div>}
          <div className="report-designer-canvas-scroll">
            <ReportRenderer
              document={document}
              runtime={runtime}
              mode="designer"
              warnings={warnings}
              onBlockGridChange={canEdit ? handleBlockGridChange : undefined}
              onComponentGridChange={canEdit ? handleComponentGridChange : undefined}
              onComponentDrop={canEdit ? handleComponentDrop : undefined}
              onComponentDuplicate={canEdit ? handleComponentDuplicate : undefined}
              onComponentDelete={canEdit ? handleComponentDelete : undefined}
              onBlockReset={canEdit ? handleBlockReset : undefined}
              onEmptyCellActivate={canEdit ? handleEmptyCellActivate : undefined}
              onEmptyCellDrop={canEdit ? handleEmptyCellDrop : undefined}
              designerContentRows={history.present.minimumRowsByPage}
              pendingComponentType={pendingComponentType}
              onPendingComponentConsumed={onPendingComponentConsumed}
              selection={effectiveSelection}
              onSelectionChange={setSelection}
            />
          </div>
        </main>
        <aside className="report-inspector-panel">
          <DesignerInspector
            target={selectedTarget}
            canEdit={canEdit}
            onBlockChange={handleBlockDefinitionChange}
            onBlockStickyChange={handleBlockStickyChange}
            onComponentStickyChange={handleComponentStickyChange}
          />
        </aside>
      </div>
      {pendingReset && (
        <div className="report-confirm-backdrop">
          <section className="report-confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="report-reset-title" aria-describedby="report-reset-description" onKeyDown={handleResetDialogKeyDown}>
            <span>不可逆操作确认</span>
            <h3 id="report-reset-title">{pendingReset.mode === 'CLEAR' ? '清空分块' : '删除分块'}</h3>
            <p id="report-reset-description">将移除该分块中的 <strong>{pendingReset.componentCount}</strong> 个组件，并把原区域恢复为空白 1×1 基础单元。</p>
            <div>
              <button type="button" autoFocus onClick={() => setPendingReset(undefined)}>取消</button>
              <button type="button" className="danger-button" onClick={() => executeBlockReset(pendingReset.pageID, pendingReset.blockID, pendingReset.mode)}>{pendingReset.mode === 'CLEAR' ? '确认清空' : '确认删除'}</button>
            </div>
          </section>
        </div>
      )}
    </>
  )
}

type SelectedTarget =
  | { kind: 'BLOCK'; page: ReportDocument['pages'][number]; block: ReportDocument['pages'][number]['blocks'][number] }
  | { kind: 'COMPONENT'; page: ReportDocument['pages'][number]; block: ReportDocument['pages'][number]['blocks'][number]; component: ReportDocument['pages'][number]['blocks'][number]['components'][number] }

type MenuCellKey = keyof NonNullable<ReportBlock['menuLayout']>['cells']
type ContentAreaKey = keyof NonNullable<ReportBlock['contentLayout']>['areas']

function DesignerStructureTree({ document, selection, canEdit, onSelectionChange, onBlockChange }: { document: ReportDocument; selection?: ReportSelection; canEdit: boolean; onSelectionChange: (selection: ReportSelection) => void; onBlockChange: (block: ReportBlock, summary: string) => void }) {
  const pages = [...document.pages].sort((left, right) => left.order - right.order)
  return (
    <nav className="report-structure-tree" aria-label="报告页面结构">
      {pages.map(page => (
        <section key={page.id}>
          <div className="report-tree-page"><CaretDown size={13} /><ListChecks size={16} /><strong>{page.name}</strong><span>{page.blocks.length}</span></div>
          <div className="report-tree-children">
            {page.blocks.map(block => {
              const selected = selection?.blockID === block.id && selection.pageID === page.id && selection.kind === 'BLOCK'
              return (
                <div className="report-tree-block" key={block.id}>
                  <button type="button" className={selected ? 'is-selected' : ''} onClick={() => onSelectionChange({ kind: 'BLOCK', pageID: page.id, blockID: block.id })}>
                    <CaretDown size={12} /><BlockKindIcon block={block} /><span>{block.name ?? block.id}</span>
                    <small>{block.grid.w}×{block.grid.h}</small>
                    <EyeToggle
                      visible={semanticBlockVisible(block)}
                      disabled={!canEdit || block.locks.config}
                      label={`${block.name ?? block.id}显示`}
                      onChange={() => onBlockChange(toggleBlockVisibility(block), `${semanticBlockVisible(block) ? '隐藏' : '显示'}${block.name ?? block.id}`)}
                    />
                  </button>
                  {block.kind === 'MENU' && block.menuLayout && (
                    <div className="report-tree-leaves">
                      {(Object.keys(block.menuLayout.cells) as MenuCellKey[]).map(cellKey => {
                        const cell = block.menuLayout!.cells[cellKey]
                        return <TreeSemanticLeaf key={cellKey} label={menuCellLabel(cellKey)} visible={cell.visible} disabled={!canEdit || block.locks.config} onSelect={() => onSelectionChange({ kind: 'BLOCK', pageID: page.id, blockID: block.id })} onToggle={() => {
                          const next = structuredClone(block)
                          next.menuLayout!.cells[cellKey].visible = !cell.visible
                          onBlockChange(next, `${cell.visible ? '隐藏' : '显示'}${menuCellLabel(cellKey)}`)
                        }} />
                      })}
                    </div>
                  )}
                  {block.kind === 'CONTENT' && block.contentLayout && (
                    <div className="report-tree-leaves">
                      {(Object.keys(block.contentLayout.areas) as ContentAreaKey[]).map(areaKey => {
                        const area = block.contentLayout!.areas[areaKey]
                        return <TreeSemanticLeaf key={areaKey} label={contentAreaLabel(areaKey)} visible={area.visible} disabled={!canEdit || block.locks.config} onSelect={() => onSelectionChange({ kind: 'BLOCK', pageID: page.id, blockID: block.id })} onToggle={() => {
                          const next = structuredClone(block)
                          next.contentLayout!.areas[areaKey].visible = !area.visible
                          onBlockChange(next, `${area.visible ? '隐藏' : '显示'}${contentAreaLabel(areaKey)}`)
                        }} />
                      })}
                      {block.components.map(component => (
                        <button key={component.id} type="button" className={`report-tree-component${selection?.kind === 'COMPONENT' && selection.componentID === component.id ? ' is-selected' : ''}`} onClick={() => onSelectionChange({ kind: 'COMPONENT', pageID: page.id, blockID: block.id, componentID: component.id })}>
                          <span /><ChartLineUp size={13} /><em>{component.name}</em>
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </section>
      ))}
    </nav>
  )
}

function BlockKindIcon({ block }: { block: ReportBlock }) {
  if (block.kind === 'MENU') return <GridFour size={15} weight="duotone" />
  if (block.kind === 'CONTENT') return <ChartLineUp size={15} weight="duotone" />
  return <GridFour size={15} />
}

function TreeSemanticLeaf({ label, visible, disabled, onSelect, onToggle }: { label: string; visible: boolean; disabled: boolean; onSelect: () => void; onToggle: () => void }) {
  return (
    <button type="button" className="report-tree-leaf" onClick={onSelect}>
      <span /><TextT size={13} /><em>{label}</em>
      <EyeToggle visible={visible} disabled={disabled} label={`${label}显示`} onChange={onToggle} />
    </button>
  )
}

function EyeToggle({ visible, disabled, label, onChange }: { visible: boolean; disabled: boolean; label: string; onChange: () => void }) {
  return (
    <span
      role="button"
      tabIndex={disabled ? -1 : 0}
      aria-label={label}
      aria-pressed={visible}
      aria-disabled={disabled}
      onClick={event => { event.stopPropagation(); if (!disabled) onChange() }}
      onKeyDown={event => {
        if (disabled || !['Enter', ' '].includes(event.key)) return
        event.preventDefault()
        event.stopPropagation()
        onChange()
      }}
    >
      {visible ? <Eye size={14} /> : <EyeSlash size={14} />}
    </span>
  )
}

type InspectorTab = 'LAYOUT' | 'CONTENT' | 'INTERACTION' | 'JSON'

function DesignerInspector({ target, canEdit, onBlockChange, onBlockStickyChange, onComponentStickyChange }: { target?: SelectedTarget; canEdit: boolean; onBlockChange: (block: ReportBlock, summary: string) => void; onBlockStickyChange: (sticky: BlockSticky) => void; onComponentStickyChange: (sticky: ComponentSticky) => void }) {
  const [tab, setTab] = useState<InspectorTab>(() => target?.block.kind ? 'LAYOUT' : 'INTERACTION')
  const block = target?.block
  const targetName = target?.kind === 'COMPONENT' ? target.component.name : block?.name ?? block?.id ?? '未选择'
  return (
    <>
      <header className="report-inspector-header"><div><SlidersHorizontal size={18} weight="duotone" /><span>属性面板</span></div><strong>{targetName}</strong>{block && <small>{block.kind === 'MENU' ? '菜单区 · 唯一顶部区块' : block.kind === 'CONTENT' ? '内容区 · 可独立隐藏' : '通用分块'}</small>}</header>
      <div className="report-inspector-tabs" role="tablist" aria-label="属性类型">
        <InspectorTabButton tab="LAYOUT" active={tab} label="布局" icon={<GridFour size={15} />} onSelect={setTab} />
        <InspectorTabButton tab="CONTENT" active={tab} label="内容" icon={<TextT size={15} />} onSelect={setTab} />
        <InspectorTabButton tab="INTERACTION" active={tab} label="交互" icon={<GearSix size={15} />} onSelect={setTab} />
        <InspectorTabButton tab="JSON" active={tab} label="JSON" icon={<ListChecks size={15} />} onSelect={setTab} />
      </div>
      <div className="report-inspector-body">
        {!target && <p className="report-inspector-empty">请从结构树或画板中选择一个分块。</p>}
        {target && tab === 'LAYOUT' && target.kind === 'BLOCK' && block?.kind === 'MENU' && block.menuLayout && <MenuLayoutEditor block={block} canEdit={canEdit} onChange={onBlockChange} />}
        {target && tab === 'LAYOUT' && target.kind === 'BLOCK' && block?.kind === 'CONTENT' && block.contentLayout && <ContentLayoutEditor block={block} canEdit={canEdit} onChange={onBlockChange} />}
        {target && tab === 'LAYOUT' && (target.kind === 'COMPONENT' || !block?.kind) && <GridSummary target={target} />}
        {target && tab === 'CONTENT' && block?.kind === 'MENU' && block.menuLayout && <MenuContentEditor block={block} canEdit={canEdit} onChange={onBlockChange} />}
        {target && tab === 'CONTENT' && block?.kind === 'CONTENT' && block.contentLayout && <ContentLayoutEditor block={block} canEdit={canEdit} onChange={onBlockChange} />}
        {target && tab === 'CONTENT' && !block?.kind && <p className="report-inspector-empty">当前为兼容分块，可通过组件配置内容。</p>}
        {target && tab === 'INTERACTION' && <StickyEditor target={target} canEdit={canEdit} onBlockChange={onBlockStickyChange} onComponentChange={onComponentStickyChange} />}
        {target && tab === 'JSON' && <pre className="report-json-preview">{JSON.stringify(target.kind === 'BLOCK' ? target.block : target.component, null, 2)}</pre>}
      </div>
    </>
  )
}

function InspectorTabButton({ tab, active, label, icon, onSelect }: { tab: InspectorTab; active: InspectorTab; label: string; icon: ReactNode; onSelect: (tab: InspectorTab) => void }) {
  return <button type="button" role="tab" aria-selected={tab === active} onClick={() => onSelect(tab)}>{icon}<span>{label}</span></button>
}

function MenuLayoutEditor({ block, canEdit, onChange }: { block: ReportBlock; canEdit: boolean; onChange: (block: ReportBlock, summary: string) => void }) {
  const layout = block.menuLayout!
  const disabled = !canEdit || block.locks.config
  function changeRatio(group: keyof typeof layout.ratios, index: 0 | 1, value: number) {
    if (!Number.isFinite(value) || value <= 0) return
    const next = structuredClone(block)
    next.menuLayout!.ratios[group][index] = value
    next.menuLayout!.usesDefaultRatios = false
    onChange(next, '调整菜单区默认比例')
  }
  function restoreDefaults() {
    const next = structuredClone(block)
    next.menuLayout!.ratios = structuredClone(next.menuLayout!.defaultRatios)
    next.menuLayout!.usesDefaultRatios = true
    onChange(next, '恢复菜单区默认比例')
  }
  return (
    <>
      <InspectorSection title="菜单区显示">
        <ToggleRow label="显示菜单区" checked={layout.visible} disabled={disabled} onChange={() => {
          const next = structuredClone(block)
          next.menuLayout!.visible = !layout.visible
          onChange(next, `${layout.visible ? '隐藏' : '显示'}菜单区`)
        }} />
        <div className="report-grid-readout"><span>X<strong>{block.grid.x}</strong></span><span>Y<strong>{block.grid.y}</strong></span><span>W<strong>{block.grid.w}</strong></span><span>H<strong>{block.grid.h}</strong></span></div>
        <p className="report-field-help">菜单区固定占据顶部 12×2 分格；布局位置不可拖离。</p>
      </InspectorSection>
      <InspectorSection title="默认比例">
        <RatioEditor label="第一行 · Logo/标题 : 功能区" values={layout.ratios.topColumns} disabled={disabled} onChange={(index, value) => changeRatio('topColumns', index, value)} />
        <RatioEditor label="第二行 · 全局筛选 : 导航区" values={layout.ratios.bottomColumns} disabled={disabled} onChange={(index, value) => changeRatio('bottomColumns', index, value)} />
        <RatioEditor label="上下两行高度" values={layout.ratios.rowHeights} disabled={disabled} onChange={(index, value) => changeRatio('rowHeights', index, value)} />
        <button type="button" className="report-restore-button" disabled={disabled || layout.usesDefaultRatios} onClick={restoreDefaults}>恢复默认比例 3:1 / 1:1 / 2:1</button>
      </InspectorSection>
      <InspectorSection title="空内容填充规则">
        <p className="report-rule-note">同一行任一宫格为空时，另一宫格横向填充；仅当第 3、4 宫格都为空时，第一行才纵向填充整个菜单区。</p>
      </InspectorSection>
    </>
  )
}

function MenuContentEditor({ block, canEdit, onChange }: { block: ReportBlock; canEdit: boolean; onChange: (block: ReportBlock, summary: string) => void }) {
  const layout = block.menuLayout!
  const disabled = !canEdit || block.locks.config
  return <InspectorSection title="四宫格内容">
    {(Object.keys(layout.cells) as MenuCellKey[]).map(cellKey => {
      const cell = layout.cells[cellKey]
      return <ToggleRow key={cellKey} label={menuCellLabel(cellKey)} checked={cell.visible} disabled={disabled} onChange={() => {
        const next = structuredClone(block)
        next.menuLayout!.cells[cellKey].visible = !cell.visible
        onChange(next, `${cell.visible ? '隐藏' : '显示'}${menuCellLabel(cellKey)}`)
      }} />
    })}
  </InspectorSection>
}

function ContentLayoutEditor({ block, canEdit, onChange }: { block: ReportBlock; canEdit: boolean; onChange: (block: ReportBlock, summary: string) => void }) {
  const layout = block.contentLayout!
  const disabled = !canEdit || block.locks.config
  return (
    <>
      <InspectorSection title="内容区显示">
        <ToggleRow label="显示整个内容区" checked={layout.visible} disabled={disabled} onChange={() => {
          const next = structuredClone(block)
          next.contentLayout!.visible = !layout.visible
          onChange(next, `${layout.visible ? '隐藏' : '显示'}整个内容区`)
        }} />
      </InspectorSection>
      <InspectorSection title="内部区域">
        {(Object.keys(layout.areas) as ContentAreaKey[]).map(areaKey => {
          const area = layout.areas[areaKey]
          return <ToggleRow key={areaKey} label={contentAreaLabel(areaKey)} description={`${area.componentIds.length} 个组件`} checked={area.visible} disabled={disabled} onChange={() => {
            const next = structuredClone(block)
            next.contentLayout!.areas[areaKey].visible = !area.visible
            onChange(next, `${area.visible ? '隐藏' : '显示'}${contentAreaLabel(areaKey)}`)
          }} />
        })}
      </InspectorSection>
    </>
  )
}

function GridSummary({ target }: { target: SelectedTarget }) {
  const grid = target.kind === 'BLOCK' ? target.block.grid : target.component.grid
  return <InspectorSection title="网格位置"><div className="report-grid-readout"><span>X<strong>{grid.x}</strong></span><span>Y<strong>{grid.y}</strong></span><span>W<strong>{grid.w}</strong></span><span>H<strong>{grid.h}</strong></span></div></InspectorSection>
}

function InspectorSection({ title, children }: { title: string; children: ReactNode }) {
  return <section className="report-inspector-section"><header><strong>{title}</strong></header>{children}</section>
}

function ToggleRow({ label, description, checked, disabled, onChange }: { label: string; description?: string; checked: boolean; disabled: boolean; onChange: () => void }) {
  return <label className="report-toggle-row"><span><strong>{label}</strong>{description && <small>{description}</small>}</span><input type="checkbox" checked={checked} disabled={disabled} onChange={onChange} /><i /></label>
}

function RatioEditor({ label, values, disabled, onChange }: { label: string; values: [number, number]; disabled: boolean; onChange: (index: 0 | 1, value: number) => void }) {
  return <label className="report-ratio-editor"><span>{label}</span><div><input aria-label={`${label}第一项`} type="number" min=".1" max="12" step=".1" value={values[0]} disabled={disabled} onChange={event => onChange(0, event.currentTarget.valueAsNumber)} /><em>:</em><input aria-label={`${label}第二项`} type="number" min=".1" max="12" step=".1" value={values[1]} disabled={disabled} onChange={event => onChange(1, event.currentTarget.valueAsNumber)} /></div></label>
}

function semanticBlockVisible(block: ReportBlock) {
  if (block.kind === 'MENU') return block.visible !== false && block.menuLayout?.visible !== false
  if (block.kind === 'CONTENT') return block.visible !== false && block.contentLayout?.visible !== false
  return block.visible !== false
}

function toggleBlockVisibility(block: ReportBlock): ReportBlock {
  const next = structuredClone(block)
  const visible = semanticBlockVisible(block)
  if (next.kind === 'MENU' && next.menuLayout) next.menuLayout.visible = !visible
  else if (next.kind === 'CONTENT' && next.contentLayout) next.contentLayout.visible = !visible
  else next.visible = !visible
  return next
}

function menuCellLabel(key: MenuCellKey) {
  return { logoTitle: 'Logo + 总标题', actions: '功能区', globalFilters: '全局筛选区', navigation: '导航区' }[key]
}

function contentAreaLabel(key: ContentAreaKey) {
  return { title: '标题区（标题 + 筛选）', conclusion: '结论区', components: '组件图' }[key]
}

function StickyEditor({ target, canEdit, onBlockChange, onComponentChange }: { target?: SelectedTarget; canEdit: boolean; onBlockChange: (sticky: BlockSticky) => void; onComponentChange: (sticky: ComponentSticky) => void }) {
  if (!target) return <section className="report-sticky-editor" aria-label="浏览态冻结设置"><span>请选择分块或组件后配置浏览态冻结。</span></section>
  const sticky = target.kind === 'BLOCK' ? target.block.sticky : target.component.sticky
  const disabled = !canEdit || target.block.locks.config
  const targetName = target.kind === 'BLOCK' ? `分块 ${target.block.id}` : `组件 ${target.component.name}`
  const enabledSticky = sticky.enabled ? sticky : undefined
  const containerIDAmbiguous = target.kind === 'COMPONENT' && target.page.id === target.block.id
  const scopes: Array<{ value: 'PAGE' | 'BLOCK' | 'CONTAINER'; label: string }> = target.kind === 'BLOCK'
    ? [{ value: 'PAGE', label: '当前页面' }, { value: 'CONTAINER', label: '指定祖先容器' }]
    : [
        { value: 'BLOCK', label: '所属分块' },
        { value: 'PAGE', label: '当前页面' },
        ...(!containerIDAmbiguous ? [{ value: 'CONTAINER' as const, label: '指定祖先容器' }] : []),
      ]

  function emit(sticky: Sticky) {
    if (target!.kind === 'BLOCK') {
      // 分块契约不接受 BLOCK 作用域，事件值异常时保持原配置不变。
      if (sticky.enabled && sticky.scope === 'BLOCK') return
      onBlockChange(sticky)
      return
    }
    onComponentChange(sticky)
  }

  function changeEnabled(enabled: boolean) {
    if (!enabled) return emit({ enabled: false })
    emit(target!.kind === 'BLOCK'
      ? { enabled: true, top: 0, scope: 'PAGE', zIndex: 100 }
      : { enabled: true, top: 0, scope: 'BLOCK', zIndex: 100 })
  }

  function changeScope(scope: 'PAGE' | 'BLOCK' | 'CONTAINER') {
    if (!enabledSticky) return
    if (scope === 'CONTAINER') {
      // 页面与分块同名时无法唯一解析容器，设计器不生成歧义配置。
      if (containerIDAmbiguous) return
      emit({ enabled: true, top: enabledSticky.top, scope, containerId: target!.kind === 'BLOCK' ? target!.page.id : target!.block.id, zIndex: enabledSticky.zIndex })
      return
    }
    if (scope === 'BLOCK') {
      if (target!.kind !== 'COMPONENT') return
      emit({ enabled: true, top: enabledSticky.top, scope, zIndex: enabledSticky.zIndex })
      return
    }
    emit({ enabled: true, top: enabledSticky.top, scope: 'PAGE', zIndex: enabledSticky.zIndex })
  }

  return (
    <section className="report-sticky-editor" aria-label="浏览态冻结设置">
      <header><div><span>浏览态冻结</span><strong>{targetName}</strong></div>{target.block.locks.config && <small>配置已锁定</small>}</header>
      <label className="report-sticky-toggle"><input type="checkbox" checked={sticky.enabled} disabled={disabled} onChange={event => changeEnabled(event.target.checked)} />启用浏览态冻结</label>
      {enabledSticky && (
        <div className="report-sticky-fields">
          <label>冻结作用域<select aria-label="冻结作用域" value={enabledSticky.scope} disabled={disabled} onChange={event => changeScope(event.target.value as 'PAGE' | 'BLOCK' | 'CONTAINER')}>{scopes.map(scope => <option key={scope.value} value={scope.value}>{scope.label}</option>)}</select></label>
          {enabledSticky.scope === 'CONTAINER' && (
            <label>约束容器<select aria-label="约束容器" value={enabledSticky.containerId} disabled={disabled} onChange={event => emit({ ...enabledSticky, containerId: event.target.value })}>
              <option value={target.page.id}>页面：{target.page.name}</option>
              {target.kind === 'COMPONENT' && target.block.id !== target.page.id && <option value={target.block.id}>分块：{target.block.id}</option>}
            </select></label>
          )}
          <label>顶部偏移（CSS px）<input aria-label="顶部偏移" type="number" min="0" max={MAX_STICKY_TOP} step="1" value={enabledSticky.top} disabled={disabled} onChange={event => emit({ ...enabledSticky, top: event.currentTarget.valueAsNumber })} /></label>
          <label>冻结层级<input aria-label="冻结层级" type="number" min="1" max={MAX_STICKY_Z_INDEX} step="1" value={enabledSticky.zIndex} disabled={disabled} onChange={event => emit({ ...enabledSticky, zIndex: event.currentTarget.valueAsNumber })} /></label>
        </div>
      )}
    </section>
  )
}

function firstBlockSelection(document: ReportDocument): ReportSelection | undefined {
  const page = [...document.pages].sort((left, right) => left.order - right.order)[0]
  return page?.blocks[0] ? { kind: 'BLOCK', pageID: page.id, blockID: page.blocks[0].id } : undefined
}

function resolveSelection(document: ReportDocument, selection?: ReportSelection): SelectedTarget | undefined {
  if (!selection) return undefined
  const page = document.pages.find(item => item.id === selection.pageID)
  const block = page?.blocks.find(item => item.id === selection.blockID)
  if (!page || !block) return undefined
  if (selection.kind === 'BLOCK') return { kind: 'BLOCK', page, block }
  const component = block.components.find(item => item.id === selection.componentID)
  return component ? { kind: 'COMPONENT', page, block, component } : undefined
}

function createSnapshot(document: ReportDocument, minimumRows: unknown, operation: string): ReportEditorSnapshot {
  const candidate = isRecord(minimumRows) ? minimumRows : {}
  const minimumRowsByPage = Object.fromEntries(document.pages.map(page => {
    const stored = candidate[page.id]
    const safeStored = typeof stored === 'number' && Number.isFinite(stored) ? Math.round(stored) : page.contentGridRows
    return [page.id, Math.min(MAX_EDITOR_CONTENT_ROWS, Math.max(page.contentGridRows, 10, safeStored))]
  }))
  return { document, minimumRowsByPage, operation }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}
