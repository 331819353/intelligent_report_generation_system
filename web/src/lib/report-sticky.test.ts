import { describe, expect, test } from 'vitest'
import type { BlockSticky, ComponentSticky, Grid, ReportBlock, ReportComponent, ReportPage, Sticky } from './report-contract'
import { calculateReportStickyPlacements, reportStickyPlacementKey } from './report-sticky'

type EnabledSticky = Extract<Sticky, { enabled: true }>
type PageEnabledSticky = Extract<EnabledSticky, { scope: 'PAGE' }>
type BlockEnabledSticky = Extract<EnabledSticky, { scope: 'BLOCK' }>
type ContainerEnabledSticky = Extract<EnabledSticky, { scope: 'CONTAINER' }>
type StickyOverrides =
  | { top?: number; zIndex?: number; scope?: 'PAGE'; containerId?: never }
  | { top?: number; zIndex?: number; scope: 'BLOCK'; containerId?: never }
  | { top?: number; zIndex?: number; scope: 'CONTAINER'; containerId: string }

function sticky(): PageEnabledSticky
function sticky(overrides: Extract<StickyOverrides, { scope?: 'PAGE' }>): PageEnabledSticky
function sticky(overrides: Extract<StickyOverrides, { scope: 'BLOCK' }>): BlockEnabledSticky
function sticky(overrides: Extract<StickyOverrides, { scope: 'CONTAINER' }>): ContainerEnabledSticky
function sticky(overrides: StickyOverrides = {}): EnabledSticky {
  const top = overrides.top ?? 0
  const zIndex = overrides.zIndex ?? 100
  if (overrides.scope === 'BLOCK') return { enabled: true, top, scope: 'BLOCK', zIndex }
  if (overrides.scope === 'CONTAINER') return { enabled: true, top, scope: 'CONTAINER', containerId: overrides.containerId, zIndex }
  return { enabled: true, top, scope: 'PAGE', zIndex }
}

function component(id: string, grid: Grid, stickyConfig: ComponentSticky, visible = true): ReportComponent {
  return {
    id,
    type: 'TITLE',
    name: id,
    grid,
    visible,
    manualLocked: false,
    sticky: stickyConfig,
    sourceTrace: [],
  }
}

function block(id: string, grid: Grid, stickyConfig: BlockSticky, components: ReportComponent[] = []): ReportBlock {
  return {
    id,
    grid,
    innerGrid: { columns: grid.w * 4, rows: grid.h * 4 },
    locks: { layout: false, config: false, dataSnapshot: false },
    sticky: stickyConfig,
    components,
  }
}

function page(blocks: ReportBlock[], contentGridRows = 10, id = 'page-main'): ReportPage {
  return { id, name: id, order: 0, contentGridRows, blocks }
}

function calculate(target: ReportPage, viewportTop: number, scale = 1, renderedBlockIDs?: string[], renderedComponentIDs?: string[]) {
  return calculateReportStickyPlacements({
    page: target,
    viewportTop,
    scale,
    renderedBlockIDs: renderedBlockIDs ?? target.blocks.map(item => item.id),
    renderedComponentIDs: renderedComponentIDs ?? target.blocks.flatMap(item => item.components.filter(child => child.visible).map(child => child.id)),
  })
}

describe('报告浏览态冻结规划器', () => {
  test('滚动前保持自然位置，滚动中冻结并在页面底边停止', () => {
    const target = page([
      block('summary', { x: 0, y: 1, w: 12, h: 1 }, sticky(), []),
    ], 3)

    expect(calculate(target, 0).placements[0]).toMatchObject({
      top: 108,
      translateY: 0,
      enabled: true,
      stuck: false,
      atBoundary: false,
    })
    expect(calculate(target, 150).placements[0]).toMatchObject({
      top: 150,
      translateY: 42,
      enabled: true,
      stuck: true,
      atBoundary: false,
    })
    expect(calculate(target, 500).placements[0]).toMatchObject({
      top: 216,
      maxTop: 216,
      translateY: 108,
      enabled: true,
      atBoundary: true,
    })
  })

  test('顶部偏移按 CSS 像素换算，缩放后仍保持相同屏幕距离', () => {
    const target = page([
      block('content', { x: 0, y: 0, w: 12, h: 4 }, { enabled: false }, [
        component('toolbar', { x: 0, y: 0, w: 8, h: 2 }, sticky({ top: 24 })),
      ]),
    ])
    const placement = calculate(target, 40, 0.5).placements[0]

    expect(placement.top).toBe(88)
    expect(placement.translateY).toBe(88)
    expect((placement.top - 40) * 0.5).toBe(24)
  })

  test('同一页面范围只对横向相交项堆叠，左右并排项共享顶部位置', () => {
    const target = page([
      block('left', { x: 0, y: 0, w: 6, h: 1 }, sticky(), []),
      block('right', { x: 6, y: 0, w: 6, h: 1 }, sticky(), []),
    ])
    const result = calculate(target, 120)

    expect(result.placements.map(item => [item.id, item.top])).toEqual([
      ['left', 120],
      ['right', 120],
    ])
    expect(result.issues).toEqual([])
  })

  test('多个横向相交组件按自然几何稳定堆叠且只包含实际渲染项', () => {
    const target = page([
      block('content', { x: 0, y: 0, w: 8, h: 5 }, { enabled: false }, [
        // 数组顺序与自然顺序相反，用于确认规划顺序不随 JSON 排列抖动。
        component('third-hidden', { x: 0, y: 4, w: 16, h: 2 }, sticky()),
        component('second', { x: 0, y: 2, w: 16, h: 2 }, sticky()),
        component('first', { x: 0, y: 0, w: 16, h: 2 }, sticky()),
      ]),
    ])
    const result = calculate(target, 100, 1, ['content'], ['first', 'second'])

    expect(result.placements.map(item => ({ id: item.id, top: item.top, translateY: item.translateY }))).toEqual([
      { id: 'first', top: 100, translateY: 100 },
      { id: 'second', top: 154, translateY: 100 },
    ])
  })

  test('同一冻结范围容量不足时后续项失败关闭而不是互相遮挡', () => {
    const target = page([
      block('first', { x: 0, y: 0, w: 12, h: 1 }, sticky(), []),
      block('second', { x: 0, y: 1, w: 12, h: 1 }, sticky(), []),
    ], 2)
    const result = calculate(target, 20)

    expect(result.placements[0]).toMatchObject({ id: 'first', enabled: true, top: 20 })
    expect(result.placements[1]).toMatchObject({
      id: 'second',
      enabled: false,
      translateY: 0,
      reasonCode: 'NO_CAPACITY',
      reason: '冻结范围剩余空间不足，已回退普通文档流',
    })
    expect(result.issues).toEqual([
      { path: 'pages[0].blocks[1].sticky', reason: '冻结范围剩余空间不足，已回退普通文档流' },
    ])
  })

  test('非法或跨级 CONTAINER 引用失败关闭，合法所属容器正常约束', () => {
    const target = page([
      block('content', { x: 0, y: 2, w: 8, h: 3 }, sticky({ scope: 'CONTAINER', containerId: 'unknown' }), [
        component('invalid-child', { x: 0, y: 0, w: 8, h: 1 }, sticky({ scope: 'CONTAINER', containerId: 'other-block' })),
        component('valid-child', { x: 0, y: 1, w: 8, h: 1 }, sticky({ scope: 'CONTAINER', containerId: 'content' })),
      ]),
    ])
    const result = calculate(target, 300)

    expect(result.placements[0]).toMatchObject({ kind: 'BLOCK', enabled: false, reasonCode: 'INVALID_CONTAINER' })
    expect(result.placements[1]).toMatchObject({ id: 'invalid-child', enabled: false, reasonCode: 'INVALID_CONTAINER' })
    expect(result.placements[2]).toMatchObject({
      id: 'valid-child',
      scopeKey: 'BLOCK:content',
      enabled: true,
      top: 300,
      maxTop: 513,
    })
  })

  test('页面与所属分块同名时 CONTAINER 引用因跨类型歧义失败关闭', () => {
    const target = page([
      block('same-id', { x: 0, y: 0, w: 8, h: 2 }, { enabled: false }, [
        component('child', { x: 0, y: 0, w: 8, h: 1 }, sticky({ scope: 'CONTAINER', containerId: 'same-id' })),
      ]),
    ], 10, 'same-id')
    const result = calculate(target, 100)

    expect(result.placements[0]).toMatchObject({
      enabled: false,
      reasonCode: 'INVALID_CONTAINER',
      reason: '冻结容器同时命中页面和所属分块，无法确定约束范围',
    })
  })

  test('父分块和子组件同时冻结时，子组件位移扣除父分块已有位移', () => {
    const target = page([
      block('parent', { x: 0, y: 2, w: 8, h: 4 }, sticky(), [
        component('child', { x: 0, y: 1, w: 8, h: 1 }, sticky({ top: 100 })),
      ]),
    ])
    const result = calculate(target, 300)
    const parent = result.placements.find(item => item.kind === 'BLOCK')!
    const child = result.placements.find(item => item.kind === 'COMPONENT')!

    expect(parent).toMatchObject({ top: 300, translateY: 84 })
    // 子组件自然顶部为 243；父分块先下移 84 后，子组件只需再下移 73 即到达 400。
    expect(child).toMatchObject({ top: 400, translateY: 73, enabled: true })
    expect(243 + parent.translateY + child.translateY).toBe(child.top)
  })

  test('分块和组件同名时使用不同 placement key', () => {
    const target = page([
      block('shared', { x: 0, y: 0, w: 8, h: 2 }, sticky(), [
        component('shared', { x: 0, y: 1, w: 8, h: 1 }, sticky({ scope: 'BLOCK' })),
      ]),
    ])
    const result = calculate(target, 80)

    expect(result.placements.map(item => item.key)).toEqual([
      reportStickyPlacementKey('BLOCK', 'shared'),
      reportStickyPlacementKey('COMPONENT', 'shared', 'shared'),
    ])
    expect(new Set(result.placements.map(item => item.key)).size).toBe(2)
  })
})
