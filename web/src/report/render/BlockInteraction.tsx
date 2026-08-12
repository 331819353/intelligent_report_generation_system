import type { GridRect } from './schema.ts'
import type { DragMode } from './use-block-interaction.ts'

/** 拖拽把手：整块头部负责移动，右下角负责缩放。都用键盘可达的按钮语义。 */
export function BlockHandles({ blockId, rect, columns, minimum, onStart, onNudge }: {
  blockId: string
  rect: GridRect
  columns: number
  minimum: { w: number; h: number }
  onStart(event: React.PointerEvent, mode: DragMode): void
  onNudge(next: GridRect, mode: DragMode): void
}) {
  const keyboard = (event: React.KeyboardEvent, mode: DragMode) => {
    const step = event.shiftKey ? 2 : 1
    let next: GridRect | null = null
    if (mode === 'move') {
      if (event.key === 'ArrowLeft') next = { ...rect, x: Math.max(rect.x - step, 0) }
      if (event.key === 'ArrowRight') next = { ...rect, x: Math.min(rect.x + step, columns - rect.w) }
      if (event.key === 'ArrowUp') next = { ...rect, y: Math.max(rect.y - step, 0) }
      if (event.key === 'ArrowDown') next = { ...rect, y: rect.y + step }
    } else {
      if (event.key === 'ArrowLeft') next = { ...rect, w: Math.max(rect.w - step, minimum.w) }
      if (event.key === 'ArrowRight') next = { ...rect, w: Math.min(rect.w + step, columns - rect.x) }
      if (event.key === 'ArrowUp') next = { ...rect, h: Math.max(rect.h - step, minimum.h) }
      if (event.key === 'ArrowDown') next = { ...rect, h: rect.h + step }
    }
    if (!next) return
    event.preventDefault()
    onNudge(next, mode)
  }
  return <>
    <button className="report-block-move-handle" type="button"
      aria-label={`移动区块 ${blockId}，可用方向键微调`}
      onPointerDown={event => onStart(event, 'move')}
      onKeyDown={event => keyboard(event, 'move')}>
      <span aria-hidden="true" />
    </button>
    <button className="report-block-resize-handle" type="button"
      aria-label={`调整区块 ${blockId} 尺寸，可用方向键微调`}
      onPointerDown={event => onStart(event, 'resize')}
      onKeyDown={event => keyboard(event, 'resize')} />
  </>
}
