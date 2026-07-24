import { describe, expect, it } from 'vitest'
import { createReportDesignerTemplate } from './report-designer-template'
import { validateReportDocument } from './report-schema'

describe('report designer hierarchy template', () => {
  it('creates one fixed menu and multiple semantic content blocks', () => {
    const document = createReportDesignerTemplate()
    expect(validateReportDocument(document).errors).toEqual([])

    const page = document.pages[0]
    const menus = page.blocks.filter(block => block.kind === 'MENU')
    const contents = page.blocks.filter(block => block.kind === 'CONTENT')
    expect(menus).toHaveLength(1)
    expect(menus[0].grid).toEqual({ x: 0, y: 0, w: 12, h: 2 })
    expect(contents.length).toBeGreaterThan(1)
    expect(contents.every(block => block.grid.w === 12)).toBe(true)
    expect(contents.every(block => block.contentLayout?.areas.title && block.contentLayout.areas.conclusion && block.contentLayout.areas.components)).toBe(true)
  })

  it('persists the confirmed default ratios and an independently hidden conclusion area', () => {
    const document = createReportDesignerTemplate()
    const menu = document.pages[0].blocks.find(block => block.kind === 'MENU')!
    const growth = document.pages[0].blocks.find(block => block.id === 'block_growth')!

    expect(menu.menuLayout?.defaultRatios).toEqual({
      topColumns: [3, 1],
      bottomColumns: [1, 1],
      rowHeights: [2, 1],
    })
    expect(menu.menuLayout?.ratios).toEqual(menu.menuLayout?.defaultRatios)
    expect(growth.contentLayout?.visible).toBe(true)
    expect(growth.contentLayout?.areas.conclusion.visible).toBe(false)
  })
})
