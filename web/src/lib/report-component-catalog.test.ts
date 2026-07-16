import reportExample from '../../../api/examples/report-json-v1.json'
import { describe, expect, test } from 'vitest'
import { reportComponentPaletteItems } from '../components/report/componentDrag'
import { createReportComponentDefaults, formalReportComponentTypes, reportComponentCatalog } from './report-component-catalog'
import type { ReportDocument } from './report-contract'
import { addComponent, updateComponentGrid } from './report-layout'
import { validateReportDocument } from './report-schema'

describe('报告组件正式目录', () => {
  test('面板、默认配置和最小尺寸由同一目录派生', () => {
    expect(reportComponentPaletteItems.map(item => item.type)).toEqual(formalReportComponentTypes)
    for (const type of formalReportComponentTypes) {
      const item = reportComponentCatalog[type]
      expect(item.defaultGrid.w).toBeGreaterThanOrEqual(item.minimumGrid.w)
      expect(item.defaultGrid.h).toBeGreaterThanOrEqual(item.minimumGrid.h)
      expect(createReportComponentDefaults(type).name).toBe(item.label)
    }
  })

  test('每类正式组件的工厂默认值都通过报告 Schema 并可重新加载', () => {
    for (const type of formalReportComponentTypes) {
      const document = structuredClone(reportExample) as unknown as ReportDocument
      document.pages[0].blocks[1].components = []
      const result = addComponent(document, 'page_overview', 'block_source_note', type, { x: 0, y: 0 })
      expect(result.issue, type).toBeUndefined()
      expect(validateReportDocument(JSON.parse(JSON.stringify(result.document))).errors, type).toEqual([])
    }
  })

  test('组件缩放不能突破目录声明的正式最小尺寸', () => {
    const document = structuredClone(reportExample) as unknown as ReportDocument
    const result = updateComponentGrid(document, 'page_overview', 'block_overview', 'chart_revenue_trend', { x: 0, y: 4, w: 3, h: 4 })
    expect(result.issue?.reason).toContain('图表组件最小尺寸为 4×4')
  })
})
