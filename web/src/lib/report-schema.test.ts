import reportExample from '../../../api/examples/report-json-v1.json'
import { describe, expect, test } from 'vitest'
import { validateReportDocument } from './report-schema'

describe('报告 JSON 前端合同校验', () => {
  test('接受与 Go 服务端共用的正式样例', () => {
    const result = validateReportDocument(reportExample)
    expect(result.errors).toEqual([])
    expect(result.warnings).toEqual([])
    expect(result.document?.schemaVersion).toBe('1.0')
    expect(result.document?.pages[0].contentGridRows).toBe(14)
  })

  test('拒绝损坏的画布并返回可定位的中文错误', () => {
    const source = structuredClone(reportExample) as unknown as { canvas: { logicalWidth: number } }
    source.canvas.logicalWidth = 1280
    const result = validateReportDocument(source)
    expect(result.document).toBeUndefined()
    expect(result.errors).toContainEqual({ path: 'canvas.logicalWidth', reason: '必须为 1920' })
  })

  test('将未知组件降级为兼容警告而非整页错误', () => {
    const source = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ type: string }> }> }> }
    source.pages[0].blocks[0].components[0].type = 'FUTURE_CARD'
    const result = validateReportDocument(source)
    expect(result.document).toBeDefined()
    expect(result.errors).toEqual([])
    expect(result.warnings).toContainEqual({
      path: 'pages[0].blocks[0].components[0].type',
      reason: '不在允许的枚举范围内',
    })
  })

  test('按组件类型拒绝未知配置和非法图表类型', () => {
    const unknownStyle = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ style?: Record<string, unknown> }> }> }> }
    unknownStyle.pages[0].blocks[0].components[2].style!.executableOption = true
    expect(validateReportDocument(unknownStyle).errors).toContainEqual({
      path: 'pages[0].blocks[0].components[2].style',
      reason: '包含未知字段 executableOption',
    })

    const invalidChart = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ binding?: { chart?: { type: string } } }> }> }> }
    invalidChart.pages[0].blocks[0].components[2].binding!.chart!.type = 'RADAR'
    expect(validateReportDocument(invalidChart).errors).toContainEqual({
      path: 'pages[0].blocks[0].components[2].binding.chart.type',
      reason: '不在允许的枚举范围内',
    })
  })

  test('按组件目录拒绝小于正式最小尺寸的网格', () => {
    const source = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ grid: { w: number } }> }> }> }
    source.pages[0].blocks[0].components[2].grid.w = 3
    expect(validateReportDocument(source).errors).toContainEqual({
      path: 'pages[0].blocks[0].components[2].grid.w',
      reason: '不能小于 4',
    })
  })

  test('按组件类型限制刷新策略并拒绝结论专属配置泄漏', () => {
    const source = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ refreshPolicy?: { mode: string }; conclusion?: Record<string, unknown> }> }> }> }
    source.pages[0].blocks[0].components[0].refreshPolicy = { mode: 'INHERIT' }
    source.pages[0].blocks[0].components[2].conclusion = { mode: 'MANUAL' }
    const errors = validateReportDocument(source).errors
    expect(errors).toContainEqual({ path: 'pages[0].blocks[0].components[0].refreshPolicy.mode', reason: '必须为 "NONE"' })
    expect(errors.some(error => error.path === 'pages[0].blocks[0].components[2].conclusion')).toBe(true)
  })

  test('允许旧草稿渐进补齐筛选配置，但拒绝启用后缺少图表联动合同', () => {
    const legacyDraft = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ binding?: Record<string, unknown> }> }> }> }
    legacyDraft.pages[0].blocks[0].components[1].binding = { parameterId: 'param_stat_month' }
    expect(validateReportDocument(legacyDraft).errors).toEqual([])

    const missingLinkage = structuredClone(reportExample) as unknown as { pages: Array<{ blocks: Array<{ components: Array<{ interaction?: Record<string, unknown> }> }> }> }
    delete missingLinkage.pages[0].blocks[0].components[2].interaction!.linkage
    expect(validateReportDocument(missingLinkage).errors.some(error => error.path === 'pages[0].blocks[0].components[2].interaction.linkage')).toBe(true)
  })

  test('语义映射必须包含精确字段和数据集参数编码', () => {
    const source = structuredClone(reportExample) as unknown as { parameters: Array<{ semanticBinding?: { datasetFields: Array<Record<string, unknown>> } }> }
    delete source.parameters[0].semanticBinding!.datasetFields[0].datasetParameterCode
    expect(validateReportDocument(source).errors).toContainEqual({
      path: 'parameters[0].semanticBinding.datasetFields[0].datasetParameterCode',
      reason: '缺少必填字段',
    })
  })

  test('冻结判别合同拒绝禁用携参、启用缺字段和分块 BLOCK 作用域', () => {
    const disabledWithParameters = structuredClone(reportExample) as unknown as StickyFixture
    disabledWithParameters.pages[0].blocks[0].sticky = { enabled: false, top: 0 }
    expect(validateReportDocument(disabledWithParameters).document).toBeUndefined()

    const missingTop = structuredClone(reportExample) as unknown as StickyFixture
    missingTop.pages[0].blocks[0].components[0].sticky = { enabled: true, scope: 'PAGE', zIndex: 1 }
    expect(validateReportDocument(missingTop).errors.some(error => error.path.endsWith('.sticky.top'))).toBe(true)

    const invalidBlockScope = structuredClone(reportExample) as unknown as StickyFixture
    invalidBlockScope.pages[0].blocks[0].sticky = { enabled: true, top: 0, scope: 'BLOCK', zIndex: 1 }
    expect(validateReportDocument(invalidBlockScope).document).toBeUndefined()
  })

  test('组件冻结接受所属分块并拒绝未知或歧义祖先容器', () => {
    const valid = structuredClone(reportExample) as unknown as StickyFixture
    valid.pages[0].blocks[0].components[0].sticky = { enabled: true, top: 12, scope: 'CONTAINER', containerId: 'block_overview', zIndex: 100 }
    expect(validateReportDocument(valid).errors).toEqual([])

    const unknown = structuredClone(valid)
    unknown.pages[0].blocks[0].components[0].sticky = { enabled: true, top: 12, scope: 'CONTAINER', containerId: 'block_unknown', zIndex: 100 }
    expect(validateReportDocument(unknown).errors).toContainEqual({
      path: 'pages[0].blocks[0].components[0].sticky.containerId',
      reason: '必须引用所属页面或分块祖先',
    })

    const ambiguous = structuredClone(valid)
    ambiguous.pages[0].id = ambiguous.pages[0].blocks[0].id
    ambiguous.pages[0].blocks[0].components[0].sticky = { enabled: true, top: 12, scope: 'CONTAINER', containerId: ambiguous.pages[0].id, zIndex: 100 }
    expect(validateReportDocument(ambiguous).errors).toContainEqual({
      path: 'pages[0].blocks[0].components[0].sticky.containerId',
      reason: '同时匹配多个祖先类型，容器引用存在歧义',
    })
  })
})

type StickyFixture = {
  pages: Array<{
    id: string
    blocks: Array<{
      id: string
      sticky: unknown
      components: Array<{ sticky: unknown }>
    }>
  }>
}
