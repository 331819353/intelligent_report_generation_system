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

  const solved = new Map<string, GridRect>([[blockId, rect]])
  if (detectCollisions(proposed).length > 0) {
    // 被拖动的区块固定在用户松手的位置，其余区块按 (y, x, id) 依次下移让位。
    // 若改用通用的纵向紧凑排布，排序会把落点让给 ID 更小的区块，用户就会看到
    // 拖拽「弹回原处」。
    const others = proposed
      .filter(block => block.id !== blockId)
      .sort((left, right) =>
        left.layout.desktop.y - right.layout.desktop.y ||
        left.layout.desktop.x - right.layout.desktop.x ||
        left.id.localeCompare(right.id))
    for (const block of others) {
      const candidate = { ...block.layout.desktop }
      for (;;) {
        const blocking = [...solved.entries()]
          .filter(([, placed]) => overlaps(placed, candidate))
          .map(([, placed]) => placed.y + placed.h)
        if (blocking.length === 0) break
        candidate.y = Math.max(...blocking)
      }
      solved.set(block.id, candidate)
    }
  } else {
    for (const block of proposed) solved.set(block.id, block.layout.desktop)
  }

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
