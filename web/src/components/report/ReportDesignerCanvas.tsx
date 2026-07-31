import { useCallback, useEffect, useRef, useState, type ChangeEvent, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import { CaretDown, ChartLineUp, Eye, EyeSlash, GearSix, GridFour, ListChecks, SlidersHorizontal, TextT, TreeStructure } from '@phosphor-icons/react'
import type { BlockSticky, ComponentSticky, ComponentType, Grid, ReportBlock, ReportDocument, ReportRuntimeContext, ReportSelection, ReportTemplate, ReportValidationIssue, Sticky } from '../../lib/report-contract'
import type { ReportDraftChange, ReportEditorState } from '../../lib/report-drafts'
import { acknowledgeReportEditorChanges, commitReportEditorHistory, createReportEditorHistory, redoReportEditorHistory, undoReportEditorHistory, type ReportEditorHistory, type ReportEditorOperationInput, type ReportEditorSnapshot } from '../../lib/report-history'
import { addComponent, createBlockAtCell, createBlockWithComponent, deleteComponent, duplicateComponent, MAX_EDITOR_CONTENT_ROWS, MAX_STICKY_TOP, MAX_STICKY_Z_INDEX, resetBlock, updateBlockDefinition, updateBlockGrid, updateBlockSticky, updateComponentGrid, updateComponentSticky, updateReportTemplate, type BlockResetMode, type LayoutUpdateResult } from '../../lib/report-layout'
import { validateReportDocument } from '../../lib/report-schema'
import { buildReportTemplatePromptContext, defaultReportTemplate } from '../../lib/report-template'
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

type DesignerViewMode = 'DESIGN' | 'PREVIEW' | 'CONFIG'

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
  const [viewMode, setViewMode] = useState<DesignerViewMode>('DESIGN')
  const [copyMessage, setCopyMessage] = useState('')
  const [templateSelected, setTemplateSelected] = useState(true)
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

  function handleTemplateChange(template: ReportTemplate, summary: string) {
    commit(updateReportTemplate(document, template), {
      operationType: 'TEMPLATE_UPDATE',
      summary,
    })
  }

  function selectTarget(nextSelection: ReportSelection) {
    setTemplateSelected(false)
    setSelection(nextSelection)
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

  function copyConfiguration() {
    void navigator.clipboard.writeText(JSON.stringify(document, null, 2)).then(
      () => setCopyMessage('配置已复制'),
      () => setCopyMessage('复制失败，请手动选择'),
    )
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
            selection={templateSelected ? undefined : effectiveSelection}
            templateSelected={templateSelected}
            canEdit={canEdit}
            onTemplateSelect={() => setTemplateSelected(true)}
            onSelectionChange={selectTarget}
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
            <div className="report-view-switch" role="tablist" aria-label="报告视图">
              <button type="button" role="tab" aria-selected={viewMode === 'DESIGN'} onClick={() => setViewMode('DESIGN')}>设计</button>
              <button type="button" role="tab" aria-selected={viewMode === 'PREVIEW'} onClick={() => setViewMode('PREVIEW')}>预览</button>
              <button type="button" role="tab" aria-selected={viewMode === 'CONFIG'} onClick={() => setViewMode('CONFIG')}>配置</button>
            </div>
            <button type="button" disabled={!canEdit || history.past.length === 0} onClick={undo}>撤销</button>
            <button type="button" disabled={!canEdit || history.future.length === 0} onClick={redo}>重做</button>
          </div>
          {!canEdit && <div className="report-layout-issue" role="status">当前账号仅可查看报告，没有编辑权限。</div>}
          {issue && <div className="report-layout-issue" role="alert"><code>{issue.path}</code>：{issue.reason}</div>}
          <div className="report-designer-canvas-scroll">
            {viewMode === 'CONFIG' ? (
              <section className="report-config-source" aria-label="报告 JSON 配置">
                <header>
                  <div><strong>报告配置文件</strong><span>设计画板与预览引擎的唯一输入</span></div>
                  <button type="button" onClick={copyConfiguration}>{copyMessage || '复制 JSON'}</button>
                </header>
                <pre>{JSON.stringify(document, null, 2)}</pre>
              </section>
            ) : (
              <ReportRenderer
                document={document}
                runtime={runtime}
                mode={viewMode === 'PREVIEW' ? 'viewer' : 'designer'}
                warnings={warnings}
                onBlockGridChange={viewMode === 'DESIGN' && canEdit ? handleBlockGridChange : undefined}
                onComponentGridChange={viewMode === 'DESIGN' && canEdit ? handleComponentGridChange : undefined}
                onComponentDrop={viewMode === 'DESIGN' && canEdit ? handleComponentDrop : undefined}
                onComponentDuplicate={viewMode === 'DESIGN' && canEdit ? handleComponentDuplicate : undefined}
                onComponentDelete={viewMode === 'DESIGN' && canEdit ? handleComponentDelete : undefined}
                onBlockReset={viewMode === 'DESIGN' && canEdit ? handleBlockReset : undefined}
                onEmptyCellActivate={viewMode === 'DESIGN' && canEdit ? handleEmptyCellActivate : undefined}
                onEmptyCellDrop={viewMode === 'DESIGN' && canEdit ? handleEmptyCellDrop : undefined}
                designerContentRows={history.present.minimumRowsByPage}
                pendingComponentType={pendingComponentType}
                onPendingComponentConsumed={onPendingComponentConsumed}
                selection={viewMode === 'DESIGN' && !templateSelected ? effectiveSelection : undefined}
                onSelectionChange={viewMode === 'DESIGN' ? selectTarget : undefined}
              />
            )}
          </div>
        </main>
        <aside className="report-inspector-panel">
          <DesignerInspector
            target={selectedTarget}
            template={document.template ?? defaultReportTemplate}
            templateSelected={templateSelected}
            canEdit={canEdit}
            onTemplateChange={handleTemplateChange}
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
            <p id="report-reset-description">将移除该分块中的 <strong>{pendingReset.componentCount}</strong> 个组件，并把原区域恢复为可添加 4×3 分块的空白区域。</p>
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

function DesignerStructureTree({ document, selection, templateSelected, canEdit, onTemplateSelect, onSelectionChange, onBlockChange }: { document: ReportDocument; selection?: ReportSelection; templateSelected: boolean; canEdit: boolean; onTemplateSelect: () => void; onSelectionChange: (selection: ReportSelection) => void; onBlockChange: (block: ReportBlock, summary: string) => void }) {
  const pages = [...document.pages].sort((left, right) => left.order - right.order)
  const [collapsedPageIDs, setCollapsedPageIDs] = useState<Set<string>>(() => new Set())
  const [collapsedBlockIDs, setCollapsedBlockIDs] = useState<Set<string>>(() => new Set())
  const allCollapsed = pages.length > 0 && pages.every(page => collapsedPageIDs.has(page.id))

  const togglePage = (pageID: string) => {
    setCollapsedPageIDs(current => {
      const next = new Set(current)
      if (next.has(pageID)) next.delete(pageID)
      else next.add(pageID)
      return next
    })
  }

  const toggleBlock = (blockKey: string) => {
    setCollapsedBlockIDs(current => {
      const next = new Set(current)
      if (next.has(blockKey)) next.delete(blockKey)
      else next.add(blockKey)
      return next
    })
  }

  const toggleAll = () => {
    if (allCollapsed) {
      setCollapsedPageIDs(new Set())
      setCollapsedBlockIDs(new Set())
      return
    }
    setCollapsedPageIDs(new Set(pages.map(page => page.id)))
    setCollapsedBlockIDs(new Set(pages.flatMap(page => page.blocks.map(block => `${page.id}:${block.id}`))))
  }

  return (
    <nav className="report-structure-tree" aria-label="报告页面结构">
      <button type="button" className={`report-template-node${templateSelected ? ' is-selected' : ''}`} onClick={onTemplateSelect}>
        <GearSix size={16} weight="duotone" />
        <span><strong>全局模板</strong><small>{document.template?.name ?? defaultReportTemplate.name}</small></span>
      </button>
      <div className="report-tree-toolbar">
        <span>{pages.length} 个页面</span>
        <button type="button" onClick={toggleAll}>{allCollapsed ? '展开全部' : '折叠全部'}</button>
      </div>
      {pages.map(page => {
        const pageCollapsed = collapsedPageIDs.has(page.id)
        return (
        <section key={page.id}>
          <button type="button" className="report-tree-page" aria-expanded={!pageCollapsed} onClick={() => togglePage(page.id)}>
            <CaretDown className={`report-tree-caret${pageCollapsed ? ' is-collapsed' : ''}`} size={13} />
            <ListChecks size={16} />
            <strong>{page.name}</strong>
            <span>{page.blocks.length}</span>
          </button>
          {!pageCollapsed && <div className="report-tree-children">
            {page.blocks.map(block => {
              const selected = selection?.blockID === block.id && selection.pageID === page.id && selection.kind === 'BLOCK'
              const blockKey = `${page.id}:${block.id}`
              const blockCollapsed = collapsedBlockIDs.has(blockKey)
              const blockName = block.name ?? block.id
              return (
                <div className="report-tree-block" key={block.id}>
                  <div className={`report-tree-block-row${selected ? ' is-selected' : ''}`}>
                    <button type="button" className="report-tree-toggle" aria-expanded={!blockCollapsed} aria-label={`${blockCollapsed ? '展开' : '折叠'}${blockName}`} onClick={() => toggleBlock(blockKey)}>
                      <CaretDown className={`report-tree-caret${blockCollapsed ? ' is-collapsed' : ''}`} size={12} />
                    </button>
                    <button type="button" className="report-tree-block-select" onClick={() => onSelectionChange({ kind: 'BLOCK', pageID: page.id, blockID: block.id })}>
                      <BlockKindIcon block={block} />
                      <span>{blockName}</span>
                      <small>{block.grid.w}×{block.grid.h}</small>
                    </button>
                    <EyeToggle
                      visible={semanticBlockVisible(block)}
                      disabled={!canEdit || block.locks.config}
                      label={`${blockName}显示`}
                      onChange={() => onBlockChange(toggleBlockVisibility(block), `${semanticBlockVisible(block) ? '隐藏' : '显示'}${blockName}`)}
                    />
                  </div>
                  {!blockCollapsed && block.kind === 'MENU' && block.menuLayout && (
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
                  {!blockCollapsed && block.kind === 'CONTENT' && block.contentLayout && (
                    <div className="report-tree-leaves">
                      {(Object.keys(block.contentLayout.areas) as ContentAreaKey[]).map(areaKey => {
                        const area = block.contentLayout!.areas[areaKey]
                        if (!area) return null
                        const required = areaKey === 'title'
                        return <TreeSemanticLeaf key={areaKey} label={contentAreaLabel(areaKey)} visible={area.visible} disabled={!canEdit || block.locks.config || required} onSelect={() => onSelectionChange({ kind: 'BLOCK', pageID: page.id, blockID: block.id })} onToggle={() => {
                          if (required) return
                          const next = structuredClone(block)
                          next.contentLayout!.areas[areaKey]!.visible = !area.visible
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
          </div>}
        </section>
        )
      })}
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

function DesignerInspector({ target, template, templateSelected, canEdit, onTemplateChange, onBlockChange, onBlockStickyChange, onComponentStickyChange }: { target?: SelectedTarget; template: ReportTemplate; templateSelected: boolean; canEdit: boolean; onTemplateChange: (template: ReportTemplate, summary: string) => void; onBlockChange: (block: ReportBlock, summary: string) => void; onBlockStickyChange: (sticky: BlockSticky) => void; onComponentStickyChange: (sticky: ComponentSticky) => void }) {
  const [tab, setTab] = useState<InspectorTab>(() => target?.block.kind ? 'LAYOUT' : 'INTERACTION')
  if (templateSelected) return <ReportTemplateInspector template={template} canEdit={canEdit} onChange={onTemplateChange} />
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

function ReportTemplateInspector({ template, canEdit, onChange }: { template: ReportTemplate; canEdit: boolean; onChange: (template: ReportTemplate, summary: string) => void }) {
  function emit(summary: string, update: (next: ReportTemplate) => void) {
    const next = structuredClone(template)
    update(next)
    onChange(next, summary)
  }

  return (
    <>
      <header className="report-inspector-header report-template-header">
        <div><GearSix size={18} weight="duotone" /><span>全局模板</span></div>
        <strong>{template.name}</strong>
        <small>同时约束 AI 上下文与渲染样式</small>
      </header>
      <div className="report-inspector-body report-template-editor">
        <InspectorSection title="模板身份">
          <TemplateTextField key={`name:${template.name}`} required label="模板名称" value={template.name} disabled={!canEdit} maxLength={100} onCommit={value => emit('修改模板名称', next => { next.name = value })} />
          <label className="report-template-field"><span>字体体系</span><select value={template.typography.fontFamily} disabled={!canEdit} onChange={event => emit('修改模板字体', next => { next.typography.fontFamily = event.target.value as ReportTemplate['typography']['fontFamily'] })}><option value="SYSTEM">系统无衬线</option><option value="SERIF">衬线字体</option><option value="MONOSPACE">等宽字体</option></select></label>
        </InspectorSection>
        <InspectorSection title="全局提示词上下文">
          <TemplateTextField key={`prompt:${template.promptContext}`} multiline label="提示词" value={template.promptContext} disabled={!canEdit} maxLength={4000} onCommit={value => emit('修改模板提示词', next => { next.promptContext = value })} />
          <p className="report-field-help">AI 生成或修改报告前，应把该上下文作为全局约束注入，不写入单个分块提示词。</p>
        </InspectorSection>
        <InspectorSection title="标题与正文">
          <div className="report-template-number-grid">
            <TemplateNumberField label="标题大小" suffix="px" value={template.typography.title.fontSize} min={12} max={72} disabled={!canEdit} onChange={value => emit('修改全局标题大小', next => { next.typography.title.fontSize = value })} />
            <TemplateNumberField label="标题字重" value={template.typography.title.fontWeight} min={400} max={900} step={100} disabled={!canEdit} onChange={value => emit('修改全局标题字重', next => { next.typography.title.fontWeight = value })} />
            <TemplateNumberField label="正文字号" suffix="px" value={template.typography.body.fontSize} min={10} max={24} disabled={!canEdit} onChange={value => emit('修改全局正文字号', next => { next.typography.body.fontSize = value })} />
          </div>
          <TemplateColorField label="标题颜色" value={template.typography.title.color} disabled={!canEdit} onChange={value => emit('修改全局标题颜色', next => { next.typography.title.color = value })} />
          <TemplateColorField label="正文颜色" value={template.typography.body.color} disabled={!canEdit} onChange={value => emit('修改全局正文颜色', next => { next.typography.body.color = value })} />
        </InspectorSection>
        <InspectorSection title="全局配色">
          <TemplateColorField label="主色" value={template.palette.primary} disabled={!canEdit} onChange={value => emit('修改模板主色', next => { next.palette.primary = value })} />
          <TemplateColorField label="强调色" value={template.palette.accent} disabled={!canEdit} onChange={value => emit('修改模板强调色', next => { next.palette.accent = value })} />
          <TemplateColorField label="辅助色" value={template.palette.muted} disabled={!canEdit} onChange={value => emit('修改模板辅助色', next => { next.palette.muted = value })} />
        </InspectorSection>
        <InspectorSection title="画布">
          <TemplateColorField label="画布背景" value={template.canvas.backgroundColor} disabled={!canEdit} onChange={value => emit('修改画布背景', next => { next.canvas.backgroundColor = value })} />
          <TemplateColorField label="网格颜色" value={template.canvas.gridColor} disabled={!canEdit} onChange={value => emit('修改画布网格颜色', next => { next.canvas.gridColor = value })} />
        </InspectorSection>
        <InspectorSection title="分块样式">
          <TemplateColorField label="分块背景" value={template.block.backgroundColor} disabled={!canEdit} onChange={value => emit('修改分块背景', next => { next.block.backgroundColor = value })} />
          <TemplateColorField label="边框颜色" value={template.block.borderColor} disabled={!canEdit} onChange={value => emit('修改分块边框', next => { next.block.borderColor = value })} />
          <div className="report-template-number-grid">
            <TemplateNumberField label="圆角" suffix="px" value={template.block.borderRadius} min={0} max={32} disabled={!canEdit} onChange={value => emit('修改分块圆角', next => { next.block.borderRadius = value })} />
            <TemplateNumberField label="内边距" suffix="px" value={template.block.padding} min={0} max={24} disabled={!canEdit} onChange={value => emit('修改分块内边距', next => { next.block.padding = value })} />
          </div>
          <label className="report-template-field"><span>阴影强度</span><select value={template.block.shadow} disabled={!canEdit} onChange={event => emit('修改分块阴影', next => { next.block.shadow = event.target.value as ReportTemplate['block']['shadow'] })}><option value="NONE">无阴影</option><option value="SOFT">柔和</option><option value="MEDIUM">中等</option></select></label>
        </InspectorSection>
        <InspectorSection title="AI 合成上下文">
          <pre className="report-template-prompt-preview">{buildReportTemplatePromptContext(template)}</pre>
        </InspectorSection>
      </div>
    </>
  )
}

function TemplateTextField({ label, value, multiline = false, required = false, disabled, maxLength, onCommit }: { label: string; value: string; multiline?: boolean; required?: boolean; disabled: boolean; maxLength: number; onCommit: (value: string) => void }) {
  const [draft, setDraft] = useState(value)
  const shared = { value: draft, disabled, maxLength, onChange: (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => setDraft(event.target.value), onBlur: () => { const next = draft.trim(); if (required && !next) return setDraft(value); if (draft !== value) onCommit(required ? next : draft) } }
  return <label className="report-template-field"><span>{label}</span>{multiline ? <textarea rows={6} {...shared} /> : <input type="text" {...shared} />}</label>
}

function TemplateNumberField({ label, suffix, value, min, max, step = 1, disabled, onChange }: { label: string; suffix?: string; value: number; min: number; max: number; step?: number; disabled: boolean; onChange: (value: number) => void }) {
  return <label className="report-template-number"><span>{label}</span><div><input type="number" value={value} min={min} max={max} step={step} disabled={disabled} onChange={event => { const next = event.currentTarget.valueAsNumber; if (Number.isFinite(next)) onChange(Math.min(max, Math.max(min, Math.round(next / step) * step))) }} />{suffix && <em>{suffix}</em>}</div></label>
}

function TemplateColorField({ label, value, disabled, onChange }: { label: string; value: string; disabled: boolean; onChange: (value: string) => void }) {
  return <label className="report-template-color"><span>{label}</span><div><input type="color" value={value} disabled={disabled} onChange={event => onChange(event.target.value.toUpperCase())} /><code>{value.toUpperCase()}</code></div></label>
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
          if (!area) return null
          const required = areaKey === 'title'
          return <ToggleRow key={areaKey} label={contentAreaLabel(areaKey)} description={required ? '必填 · 不可隐藏' : `${area.componentIds.length} 个组件`} checked={area.visible} disabled={disabled || required} onChange={() => {
            if (required) return
            const next = structuredClone(block)
            next.contentLayout!.areas[areaKey]!.visible = !area.visible
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
  return { title: '标题区（必填）', filter: '筛选区', conclusion: '结论区', chart: '图表区', components: '图表区（兼容）' }[key]
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
