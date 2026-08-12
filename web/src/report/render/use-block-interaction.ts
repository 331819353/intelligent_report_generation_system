import { useCallback, useEffect, useRef, useState } from 'react'
import type { GridRect } from './schema.ts'

/**
 * 网格拖拽与缩放的交互状态。
 *
 * 它只把指针位移换算成网格坐标并回调；落点求解、碰撞消解与受控 Operation 的
 * 生成都在 designer 层完成，渲染器不持有报告写入逻辑。
 */

export type DragMode = 'move' | 'resize'

export type DragState = {
  blockId: string
  mode: DragMode
  rect: GridRect
  valid: boolean
}

export type GridMetrics = {
  columns: number
  gapX: number
  gapY: number
  baseRowHeight: number
}

export type BlockInteractionHandle = {
  drag: DragState | null
  /** 供区块渲染时读取：正在拖拽的区块用预览矩形，其余用定义中的矩形。 */
  rectFor(blockId: string, rect: GridRect): GridRect
  start(event: React.PointerEvent, blockId: string, rect: GridRect, mode: DragMode): void
}

export type BlockInteractionOptions = {
  metrics: GridMetrics
  containerRef: React.RefObject<HTMLDivElement | null>
  minSizeFor(blockId: string): { w: number; h: number }
  onCommit(blockId: string, rect: GridRect, mode: DragMode): void
}

export function useBlockInteraction(options: BlockInteractionOptions): BlockInteractionHandle {
  const { metrics, containerRef, minSizeFor, onCommit } = options
  const [drag, setDrag] = useState<DragState | null>(null)
  // 指针事件在 window 上监听，指针移出画布或松开时也能正确结束一次拖拽。
  const session = useRef<{
    blockId: string; mode: DragMode; origin: GridRect
    startX: number; startY: number; cellWidth: number; rowHeight: number
  } | null>(null)
  const latest = useRef<DragState | null>(null)

  const start = useCallback((event: React.PointerEvent, blockId: string, rect: GridRect, mode: DragMode) => {
    const container = containerRef.current
    if (!container || event.button !== 0) return
    event.preventDefault()
    event.stopPropagation()
    const width = container.clientWidth
    const cellWidth = (width - (metrics.columns - 1) * metrics.gapX) / metrics.columns
    if (!Number.isFinite(cellWidth) || cellWidth <= 0) return
    session.current = {
      blockId, mode, origin: rect, startX: event.clientX, startY: event.clientY,
      cellWidth: cellWidth + metrics.gapX, rowHeight: metrics.baseRowHeight + metrics.gapY,
    }
    const initial = { blockId, mode, rect, valid: true }
    latest.current = initial
    setDrag(initial)
  }, [containerRef, metrics.baseRowHeight, metrics.columns, metrics.gapX, metrics.gapY])

  useEffect(() => {
    if (!drag) return undefined
    const move = (event: PointerEvent) => {
      const current = session.current
      if (!current) return
      const deltaX = Math.round((event.clientX - current.startX) / current.cellWidth)
      const deltaY = Math.round((event.clientY - current.startY) / current.rowHeight)
      const minimum = minSizeFor(current.blockId)
      const rect = current.mode === 'move'
        ? {
          ...current.origin,
          x: Math.min(Math.max(current.origin.x + deltaX, 0), metrics.columns - current.origin.w),
          y: Math.max(current.origin.y + deltaY, 0),
        }
        : {
          ...current.origin,
          w: Math.min(Math.max(current.origin.w + deltaX, minimum.w), metrics.columns - current.origin.x),
          h: Math.max(current.origin.h + deltaY, minimum.h),
        }
      const next = { blockId: current.blockId, mode: current.mode, rect, valid: true }
      latest.current = next
      setDrag(next)
    }
    const finish = () => {
      const current = session.current
      const result = latest.current
      session.current = null
      latest.current = null
      setDrag(null)
      if (!current || !result) return
      const { origin } = current
      const { rect } = result
      if (rect.x === origin.x && rect.y === origin.y && rect.w === origin.w && rect.h === origin.h) return
      onCommit(current.blockId, rect, current.mode)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', finish)
    window.addEventListener('pointercancel', finish)
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', finish)
      window.removeEventListener('pointercancel', finish)
    }
  }, [drag, metrics.columns, minSizeFor, onCommit])

  return {
    drag,
    rectFor: (blockId, rect) => drag?.blockId === blockId ? drag.rect : rect,
    start,
  }
}

