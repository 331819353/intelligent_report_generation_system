import { useCallback, useEffect, useRef, useState } from 'react'
import type { GridRect } from './schema.ts'

/**
 * 网格拖拽与缩放的交互状态，同时服务两级网格。
 *
 * 卡片在章节栅格上定位，槽位在卡片内某个区域的子网格上定位。两者的差别只有
 * 单元格尺寸和行数是否有上界，因此共用同一套指针换算，而不是各写一份——两份
 * 实现迟早会在取整、夹取或提交时机上分叉。
 *
 * 它只把指针位移换算成网格坐标并回调；落点求解、碰撞消解与受控 Operation 的
 * 生成都在 designer 层完成，渲染器不持有报告写入逻辑。
 */

export type DragMode = 'move' | 'resize'

export type DragState = {
  id: string
  mode: DragMode
  rect: GridRect
}

export type GridBounds = {
  columns: number
  /** 子网格的行数上界。页面栅格纵向可无限延伸，因此可以不设。 */
  rows?: number
}

export type CellSize = { width: number; height: number }

export type GridInteractionHandle = {
  drag: DragState | null
  /** 供渲染时读取：正在拖拽的对象用预览矩形，其余用定义中的矩形。 */
  rectFor(id: string, rect: GridRect): GridRect
  start(event: React.PointerEvent, id: string, rect: GridRect, mode: DragMode): void
}

export type GridInteractionOptions = {
  containerRef: React.RefObject<HTMLElement | null>
  bounds: GridBounds
  /** 从容器实测一个单元格的像素尺寸。返回 null 表示此刻不可拖拽。 */
  cellSize(container: HTMLElement): CellSize | null
  minSizeFor(id: string): { w: number; h: number }
  onCommit(id: string, rect: GridRect, mode: DragMode): void
}

function clamp(rect: GridRect, bounds: GridBounds, minimum: { w: number; h: number }, mode: DragMode): GridRect {
  if (mode === 'move') {
    const x = Math.min(Math.max(rect.x, 0), Math.max(bounds.columns - rect.w, 0))
    const y = bounds.rows === undefined
      ? Math.max(rect.y, 0)
      : Math.min(Math.max(rect.y, 0), Math.max(bounds.rows - rect.h, 0))
    return { ...rect, x, y }
  }
  const w = Math.min(Math.max(rect.w, minimum.w), Math.max(bounds.columns - rect.x, minimum.w))
  const h = bounds.rows === undefined
    ? Math.max(rect.h, minimum.h)
    : Math.min(Math.max(rect.h, minimum.h), Math.max(bounds.rows - rect.y, minimum.h))
  return { ...rect, w, h }
}

export function useGridInteraction(options: GridInteractionOptions): GridInteractionHandle {
  const { containerRef, bounds, cellSize, minSizeFor, onCommit } = options
  const [drag, setDrag] = useState<DragState | null>(null)
  // 指针事件在 window 上监听，指针移出容器或松开时也能正确结束一次拖拽。
  const session = useRef<{
    id: string; mode: DragMode; origin: GridRect
    startX: number; startY: number; cell: CellSize
  } | null>(null)
  const latest = useRef<DragState | null>(null)

  const start = useCallback((event: React.PointerEvent, id: string, rect: GridRect, mode: DragMode) => {
    const container = containerRef.current
    if (!container || event.button !== 0) return
    const cell = cellSize(container)
    if (!cell || !Number.isFinite(cell.width) || !Number.isFinite(cell.height) ||
      cell.width <= 0 || cell.height <= 0) {
      return
    }
    event.preventDefault()
    event.stopPropagation()
    session.current = { id, mode, origin: rect, startX: event.clientX, startY: event.clientY, cell }
    const initial = { id, mode, rect }
    latest.current = initial
    setDrag(initial)
  }, [cellSize, containerRef])

  useEffect(() => {
    if (!drag) return undefined
    const move = (event: PointerEvent) => {
      const current = session.current
      if (!current) return
      const deltaX = Math.round((event.clientX - current.startX) / current.cell.width)
      const deltaY = Math.round((event.clientY - current.startY) / current.cell.height)
      const minimum = minSizeFor(current.id)
      const moved = current.mode === 'move'
        ? { ...current.origin, x: current.origin.x + deltaX, y: current.origin.y + deltaY }
        : { ...current.origin, w: current.origin.w + deltaX, h: current.origin.h + deltaY }
      const next = { id: current.id, mode: current.mode, rect: clamp(moved, bounds, minimum, current.mode) }
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
      onCommit(current.id, rect, current.mode)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', finish)
    window.addEventListener('pointercancel', finish)
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', finish)
      window.removeEventListener('pointercancel', finish)
    }
  }, [bounds, drag, minSizeFor, onCommit])

  return {
    drag,
    rectFor: (id, rect) => drag?.id === id ? drag.rect : rect,
    start,
  }
}

/** 页面栅格：列宽按间距摊分，行高固定。 */
export function pageGridCellSize(columns: number, gapX: number, gapY: number, baseRowHeight: number) {
  return (container: HTMLElement): CellSize | null => {
    const usable = container.clientWidth - (columns - 1) * gapX
    if (usable <= 0) return null
    return { width: usable / columns + gapX, height: baseRowHeight + gapY }
  }
}

/** 区域子网格：无间距，行高由实测高度按行数均分。 */
export function zoneGridCellSize(columns: number, rows: number) {
  return (container: HTMLElement): CellSize | null => {
    if (columns < 1 || rows < 1 || container.clientWidth <= 0 || container.clientHeight <= 0) return null
    return { width: container.clientWidth / columns, height: container.clientHeight / rows }
  }
}
