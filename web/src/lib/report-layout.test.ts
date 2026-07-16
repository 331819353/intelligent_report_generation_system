import reportExample from '../../../api/examples/report-json-v1.json'
import { describe, expect, test } from 'vitest'
import type { ReportDocument } from './report-contract'
import { addComponent, calculateCanvasScale, calculateStickyStack, createBlockAtCell, createBlockWithComponent, deleteComponent, deriveContentGridRows, deriveEmptyGridCells, duplicateComponent, gridsOverlap, pointerDeltaToGrid, pointerDeltaToInnerGrid, pointerPositionToInnerGrid, resetBlock, updateBlockGrid, updateBlockSticky, updateComponentGrid, updateComponentSticky } from './report-layout'
import { validateReportDocument } from './report-schema'

function exampleDocument(): ReportDocument {
  const result = validateReportDocument(structuredClone(reportExample))
  if (!result.document) throw new Error('测试样例未通过合同校验')
  return result.document
}

describe('报告长画板布局引擎', () => {
  test('分块移动到首屏以下时派生动态内容高度', () => {
    const result = updateBlockGrid(exampleDocument(), 'page_overview', 'block_source_note', { x: 0, y: 20, w: 12, h: 3 })
    expect(result.issue).toBeUndefined()
    expect(result.document?.pages[0].contentGridRows).toBe(23)
    expect(deriveContentGridRows(result.document!.pages[0])).toBe(23)
  })

  test('拒绝分块碰撞和横向越界', () => {
    const collision = updateBlockGrid(exampleDocument(), 'page_overview', 'block_source_note', { x: 0, y: 9, w: 12, h: 3 })
    expect(collision.document).toBeUndefined()
    expect(collision.issue?.reason).toContain('block_overview')
    const outside = updateBlockGrid(exampleDocument(), 'page_overview', 'block_source_note', { x: 11, y: 11, w: 2, h: 3 })
    expect(outside.issue?.reason).toContain('12 列')
  })

  test('相邻分块不视为碰撞', () => {
    expect(gridsOverlap({ x: 0, y: 0, w: 6, h: 2 }, { x: 6, y: 0, w: 6, h: 2 })).toBe(false)
    expect(gridsOverlap({ x: 0, y: 0, w: 6, h: 2 }, { x: 5, y: 1, w: 6, h: 2 })).toBe(true)
  })

  test('指针位移按缩放后的主网格吸附', () => {
    expect(pointerDeltaToGrid(79, 53, 0.5)).toEqual({ columns: 1, rows: 1 })
    expect(pointerDeltaToGrid(35, 20, 0.5)).toEqual({ columns: 0, rows: 0 })
  })

  test('画布按容器宽度缩小但不放大', () => {
    expect(calculateCanvasScale(960)).toBe(0.5)
    expect(calculateCanvasScale(1920)).toBe(1)
    expect(calculateCanvasScale(2560)).toBe(1)
  })

  test('分块缩放会重算四倍内网格并按比例收拢组件', () => {
    const result = updateBlockGrid(exampleDocument(), 'page_overview', 'block_overview', { x: 0, y: 0, w: 11, h: 9 })
    expect(result.issue).toBeUndefined()
    const block = result.document!.pages[0].blocks[0]
    expect(block.innerGrid).toEqual({ columns: 44, rows: 36 })
    expect(block.components.every(component => component.grid.x + component.grid.w <= 44 && component.grid.y + component.grid.h <= 36)).toBe(true)
    expect(block.components.some((component, index) => block.components.slice(0, index).some(previous => gridsOverlap(component.grid, previous.grid)))).toBe(false)
  })

  test('组件移动和缩放统一拒绝碰撞、越界及锁定修改', () => {
    const document = exampleDocument()
    expect(updateComponentGrid(document, 'page_overview', 'block_overview', 'chart_revenue_trend', { x: 0, y: 3, w: 32, h: 36 }).issue?.reason).toContain('title_main')
    expect(updateComponentGrid(document, 'page_overview', 'block_overview', 'chart_revenue_trend', { x: 0, y: 4, w: 49, h: 36 }).issue?.reason).toContain('48×40')
    expect(updateComponentGrid(document, 'page_overview', 'block_overview', 'title_main', { x: 0, y: 1, w: 48, h: 4 }).issue?.reason).toContain('已锁定')
    expect(updateComponentGrid(document, 'page_overview', 'block_overview', 'chart_revenue_trend', { x: Number.NaN, y: 4, w: 32, h: 36 }).issue?.reason).toContain('边界')
  })

  test('支持新增、复制和删除组件并生成报告内唯一标识', () => {
    const document = exampleDocument()
    document.pages[0].blocks[1].components = []
    const added = addComponent(document, 'page_overview', 'block_source_note', 'KPI', { x: 0, y: 0 })
    expect(added.componentID).toBe('component_kpi_1')
    expect(added.document?.pages[0].blocks[1].components[0].name).toBe('指标卡')
    const duplicated = duplicateComponent(added.document!, 'page_overview', 'block_source_note', added.componentID!)
    expect(duplicated.componentID).toBe('component_kpi_2')
    expect(duplicated.document?.pages[0].blocks[1].components).toHaveLength(2)
    const deleted = deleteComponent(duplicated.document!, 'page_overview', 'block_source_note', added.componentID!)
    expect(deleted.document?.pages[0].blocks[1].components.map(component => component.id)).toEqual(['component_kpi_2'])
  })

  test('指针位置和位移按四倍内网格吸附', () => {
    expect(pointerPositionToInnerGrid(61, 42, 0.5)).toEqual({ x: 3, y: 3 })
    expect(pointerDeltaToInnerGrid(20, 14, 0.5)).toEqual({ columns: 1, rows: 1 })
  })

  test('清空或删除分块后派生原区域的隐式基础单元', () => {
    const result = resetBlock(exampleDocument(), 'page_overview', 'block_source_note')
    expect(result.vacatedGrid).toEqual({ x: 0, y: 11, w: 12, h: 3 })
    expect(result.document?.pages[0].blocks.map(block => block.id)).toEqual(['block_overview'])
    expect(result.document?.pages[0].contentGridRows).toBe(10)
    const cells = deriveEmptyGridCells(result.document!.pages[0].blocks, 14)
    expect(cells).toHaveLength(48)
    expect(cells).toContainEqual({ x: 0, y: 11 })
    expect(cells).toContainEqual({ x: 11, y: 13 })
  })

  test('空白单元在选中或放入组件时才生成正式分块', () => {
    const reset = resetBlock(exampleDocument(), 'page_overview', 'block_source_note')
    const selected = createBlockAtCell(reset.document!, 'page_overview', { x: 3, y: 12 })
    expect(selected.blockID).toBe('block_empty_1')
    expect(selected.document?.pages[0].blocks[1].grid).toEqual({ x: 3, y: 12, w: 1, h: 1 })
    expect(selected.document?.pages[0].blocks[1].innerGrid).toEqual({ columns: 4, rows: 4 })
    const dropped = createBlockWithComponent(reset.document!, 'page_overview', { x: 4, y: 12 }, 'KPI')
    expect(dropped.document?.pages[0].blocks[1].components[0].grid).toEqual({ x: 0, y: 0, w: 4, h: 4 })
  })

  test('锁定分块不能清空或删除', () => {
    const document = exampleDocument()
    document.pages[0].blocks[0].locks.config = true
    expect(resetBlock(document, 'page_overview', 'block_overview').issue?.reason).toContain('已锁定')
  })

  test('多个冻结项按范围独立堆叠并遵守容器底边', () => {
    expect(calculateStickyStack([
      { id: 'page-title', requestedTop: 12, height: 60, scopeKey: 'page', containerTop: 0, containerBottom: 300 },
      { id: 'page-filter', requestedTop: 12, height: 48, scopeKey: 'page', containerTop: 0, containerBottom: 300 },
      { id: 'block-title', requestedTop: 8, height: 32, scopeKey: 'block-a', containerTop: 0, containerBottom: 80 },
    ])).toEqual([
      { id: 'page-title', top: 12, maxTop: 240, enabled: true },
      { id: 'page-filter', top: 72, maxTop: 252, enabled: true },
      { id: 'block-title', top: 8, maxTop: 48, enabled: true },
    ])
  })

  test('冻结容器无法容纳后续项时回退普通文档流', () => {
    const placements = calculateStickyStack([
      { id: 'first', requestedTop: 0, height: 70, scopeKey: 'small', containerTop: 0, containerBottom: 100 },
      { id: 'second', requestedTop: 0, height: 40, scopeKey: 'small', containerTop: 0, containerBottom: 100 },
    ])
    expect(placements[0].enabled).toBe(true)
    expect(placements[1]).toEqual({ id: 'second', top: 60, maxTop: 60, enabled: false })
  })

  test('冻结编辑只接受同页祖先并规范化禁用配置', () => {
    const document = exampleDocument()
    const blockResult = updateBlockSticky(document, 'page_overview', 'block_overview', {
      enabled: true, top: 16, scope: 'CONTAINER', containerId: 'page_overview', zIndex: 100,
    })
    expect(blockResult.document?.pages[0].blocks[0].sticky).toEqual({ enabled: true, top: 16, scope: 'CONTAINER', containerId: 'page_overview', zIndex: 100 })
    expect(updateBlockSticky(document, 'page_overview', 'block_overview', {
      enabled: true, top: 0, scope: 'CONTAINER', containerId: 'block_overview', zIndex: 100,
    }).issue?.path).toContain('containerId')

    const componentResult = updateComponentSticky(document, 'page_overview', 'block_overview', 'filter_stat_month', {
      enabled: true, top: 12, scope: 'CONTAINER', containerId: 'block_overview', zIndex: 110,
    })
    expect(componentResult.document?.pages[0].blocks[0].components[1].sticky).toEqual({ enabled: true, top: 12, scope: 'CONTAINER', containerId: 'block_overview', zIndex: 110 })
    const disabled = updateComponentSticky(componentResult.document!, 'page_overview', 'block_overview', 'filter_stat_month', { enabled: false })
    expect(disabled.document?.pages[0].blocks[0].components[1].sticky).toEqual({ enabled: false })
  })

  test('组件容器标识同时命中页面与分块时拒绝歧义配置', () => {
    const document = exampleDocument()
    document.pages[0].id = 'same-container'
    document.pages[0].blocks[0].id = 'same-container'

    const result = updateComponentSticky(document, 'same-container', 'same-container', 'filter_stat_month', {
      enabled: true,
      top: 0,
      scope: 'CONTAINER',
      containerId: 'same-container',
      zIndex: 100,
    })

    expect(result.document).toBeUndefined()
    expect(result.issue).toEqual({
      path: 'pages[0].blocks[0].components[1].sticky.containerId',
      reason: '冻结容器必须唯一命中当前目标的同页祖先',
    })
  })

  test('冻结编辑遵守配置锁和数值边界', () => {
    const document = exampleDocument()
    document.pages[0].blocks[0].locks.config = true
    expect(updateComponentSticky(document, 'page_overview', 'block_overview', 'filter_stat_month', {
      enabled: true, top: 0, scope: 'BLOCK', zIndex: 100,
    }).issue?.reason).toContain('配置已锁定')
    document.pages[0].blocks[0].locks.config = false
    expect(updateBlockSticky(document, 'page_overview', 'block_overview', {
      enabled: true, top: 10001, scope: 'PAGE', zIndex: 100,
    }).issue?.path).toContain('top')
    expect(updateComponentSticky(document, 'page_overview', 'block_overview', 'filter_stat_month', {
      enabled: true, top: 0, scope: 'BLOCK', zIndex: 100001,
    }).issue?.path).toContain('zIndex')
  })
})
