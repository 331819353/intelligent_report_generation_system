import { detectCollisions, type LayoutBlock } from './layout/index.ts'
import type { Block, GridRect } from '../render/schema.ts'

/**
 * 网格编排的纯函数层：新组件落位、拖拽落点求解与碰撞消解。
 *
 * 服务端会拒绝任何区块重叠（REPORT_LAYOUT_COLLISION），因此编辑器必须在提交
 * 受控 Operation 之前就把布局解成无重叠状态，而不是把冲突留给保存失败。
 */

export function clampRect(rect: GridRect, columns: number, minimum: { w: number; h: number }): GridRect {
  const w = Math.min(Math.max(rect.w, minimum.w, 1), columns)
  const h = Math.max(rect.h, minimum.h, 1)
  return {
    w, h,
    x: Math.min(Math.max(rect.x, 0), columns - w),
    y: Math.max(rect.y, 0),
  }
}

function toLayoutBlocks(blocks: Block[]): LayoutBlock[] {
  return blocks.map(block => ({ id: block.id, layout: { desktop: { ...block.layout.desktop } } }))
}

/**
 * 在章节网格中为新区块找一个不与现有区块重叠的位置：优先在已有内容的同一行
 * 右侧留白处放置，放不下时另起一行，让「再加一个组件」自然形成并排布局。
 */
export function findFreeRect(blocks: Block[], size: { w: number; h: number }, columns: number): GridRect {
  const width = Math.min(Math.max(size.w, 1), columns)
  const height = Math.max(size.h, 1)
  const occupied = blocks.map(block => block.layout.desktop)
  const originY = occupied.length ? Math.min(...occupied.map(rect => rect.y)) : 0
  const maxY = occupied.length ? Math.max(...occupied.map(rect => rect.y + rect.h)) : originY
  for (let y = originY; y <= maxY; y++) {
    for (let x = 0; x + width <= columns; x++) {
      const candidate = { x, y, w: width, h: height }
      if (!occupied.some(rect => overlaps(rect, candidate))) return candidate
    }
  }
  return { x: 0, y: maxY, w: width, h: height }
}

function overlaps(left: GridRect, right: GridRect): boolean {
  return left.x < right.x + right.w && right.x < left.x + left.w &&
    left.y < right.y + right.h && right.y < left.y + left.h
}

/**
 * 让位求解：被拖动的对象固定在用户松手的位置，与它重叠的对象按 (y, x, id)
 * 依次下移。
 *
 * 若改用通用的纵向紧凑排布，排序会把落点让给 ID 更小的对象，用户会看到拖拽
 * 「弹回原处」。区块与槽位共用这一条规则，卡片内外的手感才一致。
 */
function displaceOverlaps(
  items: LayoutBlock[], anchorId: string, anchorRect: GridRect,
): Map<string, GridRect> {
  const solved = new Map<string, GridRect>([[anchorId, anchorRect]])
  if (detectCollisions(items).length === 0) {
    for (const item of items) solved.set(item.id, item.layout.desktop)
    return solved
  }
  const others = items
    .filter(item => item.id !== anchorId)
    .sort((left, right) =>
      left.layout.desktop.y - right.layout.desktop.y ||
      left.layout.desktop.x - right.layout.desktop.x ||
      left.id.localeCompare(right.id))
  for (const item of others) {
    const candidate = { ...item.layout.desktop }
    for (;;) {
      const blocking = [...solved.values()]
        .filter(placed => overlaps(placed, candidate))
        .map(placed => placed.y + placed.h)
      if (blocking.length === 0) break
      candidate.y = Math.max(...blocking)
    }
    solved.set(item.id, candidate)
  }
  return solved
}

export type LayoutChange = { blockId: string; rect: GridRect; moved: boolean; resized: boolean }

/**
 * 求解一次拖拽/缩放后的章节布局。
 *
 * 没有碰撞时保持其余区块原样，用户刻意留出的空白不会被压缩；出现碰撞时才
 * 按纵向紧凑规则消解，并把被动移动的区块一并返回，供调用方生成 BLOCK_MOVE。
 */
export function resolveLayout(
  blocks: Block[],
  blockId: string,
  target: GridRect,
  columns: number,
  minimum: { w: number; h: number },
): LayoutChange[] {
  const rect = clampRect(target, columns, minimum)
  const originals = new Map(blocks.map(block => [block.id, block.layout.desktop]))
  const proposed: LayoutBlock[] = toLayoutBlocks(blocks).map(block => block.id === blockId
    ? { ...block, layout: { desktop: rect } }
    : block)

  const solved = displaceOverlaps(proposed, blockId, rect)

  const changes: LayoutChange[] = []
  for (const block of proposed) {
    const original = originals.get(block.id)
    const next = solved.get(block.id)
    if (!original || !next) continue
    const moved = next.x !== original.x || next.y !== original.y
    const resized = next.w !== original.w || next.h !== original.h
    if (moved || resized) changes.push({ blockId: block.id, rect: next, moved, resized })
  }
  return changes
}

/**
 * 槽位落位求解。
 *
 * 槽位所在的区域子网格是有界的：服务端校验 grid.x+w ≤ zone.columns、
 * grid.y+h ≤ zone.rows，并拒绝同一区域内槽位重叠。因此这里既要夹取到边界，
 * 也要在必要时把区域的行数抬高，否则一次拖拽只会换来一次保存失败。
 */
export type SlotRect = { id: string; grid: GridRect }

export type SlotPlacement = {
  changes: Array<{ slotId: string; rect: GridRect }>
  /** 求解后所需的最小行数；大于当前 rows 时区域需要一并加高。 */
  requiredRows: number
}

export function resolveSlotPlacement(
  slots: SlotRect[],
  slotId: string,
  target: GridRect,
  columns: number,
  rows: number,
  minimum: { w: number; h: number },
): SlotPlacement {
  const width = Math.min(Math.max(target.w, minimum.w, 1), Math.max(columns, 1))
  const rect: GridRect = {
    w: width,
    h: Math.max(target.h, minimum.h, 1),
    x: Math.min(Math.max(target.x, 0), Math.max(columns - width, 0)),
    y: Math.max(target.y, 0),
  }
  const originals = new Map(slots.map(slot => [slot.id, slot.grid]))
  const proposed = slots.map(slot => ({
    id: slot.id,
    layout: { desktop: slot.id === slotId ? rect : { ...slot.grid } },
  }))
  // 与区块同一套让位规则：卡片内外的拖拽手感一致，重叠判定也只有一处实现。
  const solved = displaceOverlaps(proposed, slotId, rect)

  const changes: Array<{ slotId: string; rect: GridRect }> = []
  let requiredRows = Math.max(rows, 1)
  for (const item of proposed) {
    const next = solved.get(item.id)
    const original = originals.get(item.id)
    if (!next || !original) continue
    requiredRows = Math.max(requiredRows, next.y + next.h)
    if (next.x !== original.x || next.y !== original.y || next.w !== original.w || next.h !== original.h) {
      changes.push({ slotId: item.id, rect: next })
    }
  }
  return { changes, requiredRows }
}
