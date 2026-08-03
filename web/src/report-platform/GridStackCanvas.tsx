import { useEffect, useMemo, useRef } from 'react'
import { GridStack, type GridStackNode } from 'gridstack'
import 'gridstack/dist/gridstack.min.css'
import type { CardType, ReportBreakpoint, ReportCardDefinition, ReportDefinition, ReportGrid } from './types'

export type GridStackCanvasProps = {
  definition: ReportDefinition
  breakpoint: ReportBreakpoint
  editable: boolean
  selectedCardId?: string
  renderCard: (card: ReportCardDefinition) => React.ReactNode
  onSelect?: (cardId: string) => void
  onLayoutChange?: (cardId: string, grid: ReportGrid) => void
  onAddCard?: (type: CardType, anchor: Pick<ReportGrid, 'x' | 'y'>) => void
}

export function GridStackCanvas({ definition, breakpoint, editable, selectedCardId, renderCard, onSelect, onLayoutChange, onAddCard }: GridStackCanvasProps) {
  const rootRef = useRef<HTMLDivElement>(null)
  const layoutFingerprint = useMemo(() => definition.cards.map(card => `${card.id}:${JSON.stringify(card.layout[breakpoint])}`).join('|'), [breakpoint, definition.cards])
  const callbacks = useRef({ onLayoutChange })
  useEffect(() => { callbacks.current = { onLayoutChange } }, [onLayoutChange])

  useEffect(() => {
    if (!rootRef.current) return
    const grid = GridStack.init({ column: 12, cellHeight: definition.layout.rowHeight, margin: definition.layout.margin, float: false, animate: true, disableDrag: !editable, disableResize: !editable, minRow: 1 }, rootRef.current)
    if (!grid) return
    const onChange = (_event: Event, nodes: GridStackNode[]) => {
      if (!editable) return
      nodes.forEach(node => {
        const cardId = node.el?.dataset.cardId
        if (!cardId || node.x === undefined || node.y === undefined || node.w === undefined || node.h === undefined) return
        callbacks.current.onLayoutChange?.(cardId, { x: node.x, y: node.y, w: node.w, h: node.h })
      })
    }
    grid.on('change', onChange)
    return () => { grid.off('change'); grid.destroy(false) }
  }, [breakpoint, definition.layout.margin, definition.layout.rowHeight, editable, layoutFingerprint])

  function handleDrop(event: React.DragEvent<HTMLDivElement>) {
    if (!editable || !onAddCard) return
    const type = event.dataTransfer.getData('application/x-report-card') as CardType
    if (!type) return
    event.preventDefault()
    const rect = event.currentTarget.getBoundingClientRect()
    const x = Math.max(0, Math.min(11, Math.floor((event.clientX - rect.left) / rect.width * 12)))
    const y = Math.max(0, Math.floor((event.clientY - rect.top + event.currentTarget.scrollTop) / (definition.layout.rowHeight + definition.layout.margin)))
    onAddCard(type, { x, y })
  }

  return (
    <div className="grid-stack rpt-gridstack" ref={rootRef} onDragOver={event => { if (editable) event.preventDefault() }} onDrop={handleDrop}>
      {definition.cards.map(card => {
        const grid = card.layout[breakpoint]
        return <div className={`grid-stack-item${selectedCardId === card.id ? ' rpt-gridstack-item--selected' : ''}`} key={card.id} data-card-id={card.id} gs-x={grid.x} gs-y={grid.y} gs-w={grid.w} gs-h={grid.h} onPointerDown={() => onSelect?.(card.id)}><div className="grid-stack-item-content">{renderCard(card)}</div></div>
      })}
    </div>
  )
}
