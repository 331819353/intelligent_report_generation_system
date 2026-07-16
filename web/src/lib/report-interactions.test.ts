import { describe, expect, test } from 'vitest'
import type { ReportComponent, ReportDocument, ReportParameter } from './report-contract'
import { buildReportInteractionCommand, normalizeReportParameterValue, type ReportEffectScope } from './report-interactions'

describe('报告参数值归一化', () => {
  test.each([
    ['INTEGER', '42', 42],
    ['DECIMAL', '12.50', 12.5],
    ['BOOLEAN', 'true', true],
    ['BOOLEAN', false, false],
    ['STRING', '华东', '华东'],
    ['DATE', '2026-07-16', '2026-07-16'],
    ['DATETIME', '2026-07-16T10:30:00+08:00', '2026-07-16T10:30:00+08:00'],
    ['DATE_YEAR', '2026', '2026'],
    ['DATE_MONTH', '2026-07', '2026-07'],
    ['DATE_QUARTER', '2026-Q3', '2026-Q3'],
  ])('将 %s 值规范化为运行参数', (dataType, rawValue, expected) => {
    expect(normalizeReportParameterValue(parameter({ dataType }), rawValue)).toEqual({ value: expected })
  })

  test('按元素类型规范化多值参数', () => {
    expect(normalizeReportParameterValue(parameter({ dataType: 'INTEGER', multiValue: true }), ['1', 2, '3'])).toEqual({ value: [1, 2, 3] })
  })

  test('日期范围和选择型值使用明确的运行表示', () => {
    expect(normalizeReportParameterValue(parameter({ dataType: 'DATE_RANGE' }), ['2026-07-01', '2026-07-31'])).toEqual({ value: ['2026-07-01', '2026-07-31'] })
    expect(normalizeReportParameterValue(parameter({ dataType: 'DATE_RANGE' }), ['2026-07-01'])).toEqual({ issue: '区域必须为两个日期组成的范围' })
    expect(normalizeReportParameterValue(parameter({ dataType: 'SINGLE_SELECT' }), 42)).toEqual({ value: 42 })
  })

  test.each([
    [parameter({ dataType: 'INTEGER' }), '9007199254740992', '参数必须为安全整数'],
    [parameter({ dataType: 'DECIMAL' }), 'not-a-number', '参数必须为有限数字'],
    [parameter({ dataType: 'BOOLEAN' }), 'yes', '参数必须为布尔值'],
    [parameter({ dataType: 'DATE_MONTH' }), '2026-13', '参数格式与声明类型不匹配'],
    [parameter({ dataType: 'STRING' }), ['华东'], '区域必须为单值'],
    [parameter({ dataType: 'STRING', multiValue: true }), '华东', '区域必须为多值数组'],
    [parameter({ required: true }), '', '区域不能为空'],
  ])('非法类型或基数失败关闭', (definition, rawValue, reason) => {
    expect(normalizeReportParameterValue(definition, rawValue)).toEqual({ issue: reason })
  })

  test('限制多值参数数量并允许可选参数清空', () => {
    expect(normalizeReportParameterValue(parameter({ multiValue: true }), Array.from({ length: 1001 }, (_, index) => String(index)))).toEqual({ issue: '区域最多允许 1000 项' })
    expect(normalizeReportParameterValue(parameter({ required: false }), '')).toEqual({ value: undefined })
  })
})

describe('报告筛选和联动命令', () => {
  test('将 JSON 中的参数 ID 解析为运行参数 code，并保留其他参数', () => {
    const result = buildReportInteractionCommand(reportFixture(), { tenant_period: '2026-07', region: '华北' }, 'filter_region', { value: '华东' })

    expect(result.issue).toBeUndefined()
    expect(result.command).toMatchObject({
      type: 'PARAMETER_CHANGE',
      sourceComponentId: 'filter_region',
      parameterId: 'param_region',
      parameterCode: 'region',
      value: '华东',
      parameters: { tenant_period: '2026-07', region: '华东' },
    })
  })

  test.each<{ name: string; scope: ReportEffectScope; affected: string[] }>([
    { name: 'REPORT', scope: { kind: 'REPORT' }, affected: ['table_sales', 'chart_sales', 'chart_cost', 'table_remote'] },
    { name: 'PAGE', scope: { kind: 'PAGE' }, affected: ['table_sales', 'chart_sales', 'chart_cost'] },
    { name: 'BLOCK', scope: { kind: 'BLOCK' }, affected: ['table_sales', 'chart_sales'] },
    { name: 'COMPONENTS', scope: { kind: 'COMPONENTS', componentIds: ['chart_cost', 'table_remote'] }, affected: ['chart_cost', 'table_remote'] },
  ])('$name 作用域只刷新匹配的组件', ({ scope, affected }) => {
    const document = reportFixture()
    setFilterScope(document, scope)

    const result = buildReportInteractionCommand(document, {}, 'filter_region', { value: '华东' })

    expect(result.issue).toBeUndefined()
    expect(result.command?.affectedComponentIds).toEqual(affected)
    expect(result.command?.targets.map(item => item.componentId)).toEqual(affected)
  })

  test('同一语义字段为两个数据集选择各自的字段映射', () => {
    const document = reportFixture()
    setFilterScope(document, { kind: 'REPORT' })

    const result = buildReportInteractionCommand(document, {}, 'filter_region', { value: '华东' })
    const salesTargets = result.command?.targets.filter(item => item.datasetVersionId === 'dsv_sales') ?? []
    const costTargets = result.command?.targets.filter(item => item.datasetVersionId === 'dsv_cost') ?? []

    expect(salesTargets).not.toHaveLength(0)
    expect(costTargets).not.toHaveLength(0)
    expect(salesTargets.every(item => item.semanticFieldCode === 'enterprise_region' && item.fieldId === 'field_sales_region')).toBe(true)
    expect(costTargets.every(item => item.semanticFieldCode === 'enterprise_region' && item.fieldId === 'field_cost_area')).toBe(true)
  })

  test('目标数据集缺少语义映射时整次命令失败关闭', () => {
    const document = reportFixture()
    document.parameters![0].semanticBinding!.datasetFields = [{ datasetVersionId: 'dsv_sales', fieldId: 'field_sales_region', datasetParameterCode: 'sales_region' }]
    setFilterScope(document, { kind: 'COMPONENTS', componentIds: ['chart_cost'] })

    const result = buildReportInteractionCommand(document, { region: '华北' }, 'filter_region', { value: '华东' })

    expect(result.command).toBeUndefined()
    expect(result.issue?.reason).toContain('在数据集 dsv_cost 中没有映射')
  })

  test('目标数据集存在重复语义映射时整次命令失败关闭', () => {
    const document = reportFixture()
    document.parameters![0].semanticBinding!.datasetFields.push({ datasetVersionId: 'dsv_cost', fieldId: 'field_cost_region_duplicate', datasetParameterCode: 'cost_region_duplicate' })
    setFilterScope(document, { kind: 'COMPONENTS', componentIds: ['chart_cost'] })

    const result = buildReportInteractionCommand(document, {}, 'filter_region', { value: '华东' })

    expect(result.command).toBeUndefined()
    expect(result.issue?.reason).toContain('在数据集 dsv_cost 中存在歧义映射')
  })

  test('非目标数据集的重复或不完整映射也会使整次解析失败', () => {
    const duplicate = reportFixture()
    duplicate.parameters![0].semanticBinding!.datasetFields.push({ datasetVersionId: 'dsv_cost', fieldId: 'field_duplicate', datasetParameterCode: 'cost_duplicate' })
    setFilterScope(duplicate, { kind: 'COMPONENTS', componentIds: ['chart_sales'] })
    expect(buildReportInteractionCommand(duplicate, {}, 'filter_region', { value: '华东' }).issue?.reason).toContain('在数据集 dsv_cost 中存在歧义映射')

    const incomplete = reportFixture()
    incomplete.parameters![0].semanticBinding!.datasetFields[1].fieldId = ''
    setFilterScope(incomplete, { kind: 'COMPONENTS', componentIds: ['chart_sales'] })
    expect(buildReportInteractionCommand(incomplete, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('语义字段映射不完整')
  })

  test('指定组件不存在或目标列表为空时失败关闭', () => {
    const missing = reportFixture()
    setFilterScope(missing, { kind: 'COMPONENTS', componentIds: ['component_missing'] })
    expect(buildReportInteractionCommand(missing, {}, 'filter_region', { value: '华东' }).issue?.reason).toContain('component_missing 不存在')

    const empty = reportFixture()
    setFilterScope(empty, { kind: 'COMPONENTS', componentIds: [] })
    expect(buildReportInteractionCommand(empty, {}, 'filter_region', { value: '华东' }).issue?.reason).toContain('至少需要一个目标组件')
  })

  test('显式目标缺少数据集绑定时不会被静默跳过', () => {
    const document = reportFixture()
    setFilterScope(document, { kind: 'COMPONENTS', componentIds: ['filter_region', 'chart_sales'] })

    const result = buildReportInteractionCommand(document, {}, 'filter_region', { value: '华东' })

    expect(result.command).toBeUndefined()
    expect(result.issue?.reason).toBe('目标组件 filter_region 缺少数据集版本绑定')
  })

  test('错误作用域成员和非法操作符不会被静默降级', () => {
    const invalidScope = reportFixture()
    findComponent(invalidScope, 'filter_region').binding!.effectScope = { kind: 'COMPONENTS', componentIds: ['chart_sales', 42] }
    expect(buildReportInteractionCommand(invalidScope, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('联动影响范围配置无效')

    const invalidOperator = reportFixture()
    findComponent(invalidOperator, 'filter_region').binding!.operator = 'CONTAINS'
    expect(buildReportInteractionCommand(invalidOperator, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('联动操作符无效')

    const missingOperator = reportFixture()
    delete findComponent(missingOperator, 'filter_region').binding!.operator
    expect(buildReportInteractionCommand(missingOperator, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('联动操作符无效')
  })

  test('操作符与参数基数不兼容时失败关闭', () => {
    const single = reportFixture()
    findComponent(single, 'filter_region').binding!.operator = 'IN'
    expect(buildReportInteractionCommand(single, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('IN 操作符只允许多值参数')

    const multiple = reportFixture()
    multiple.parameters![0].multiValue = true
    findComponent(multiple, 'filter_region').binding!.operator = 'EQUALS'
    expect(buildReportInteractionCommand(multiple, {}, 'filter_region', { value: ['华东'] }).issue?.reason).toBe('EQUALS 操作符只允许单值参数')
  })

  test('重复组件或参数标识在解析前失败关闭', () => {
    const duplicateComponent = reportFixture()
    findComponent(duplicateComponent, 'chart_cost').id = 'chart_sales'
    expect(buildReportInteractionCommand(duplicateComponent, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('组件标识 chart_sales 重复')

    const duplicateParameter = reportFixture()
    duplicateParameter.parameters!.push({ ...structuredClone(duplicateParameter.parameters![0]), code: 'region_copy' })
    expect(buildReportInteractionCommand(duplicateParameter, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('报告参数标识 param_region 重复')

    const duplicateCode = reportFixture()
    duplicateCode.parameters!.push({ ...structuredClone(duplicateCode.parameters![0]), id: 'param_region_copy' })
    expect(buildReportInteractionCommand(duplicateCode, {}, 'filter_region', { value: '华东' }).issue?.reason).toBe('报告参数编码 region 重复')
  })

  test('页面参数不能由其他页面的筛选组件触发', () => {
    const document = reportFixture()
    document.parameters![0].scope = 'PAGE'
    document.parameters![0].pageId = 'page_remote'

    const result = buildReportInteractionCommand(document, {}, 'filter_region', { value: '华东' })

    expect(result.command).toBeUndefined()
    expect(result.issue?.reason).toBe('页面参数 region 不能从其他页面触发')
  })

  test('图表点击通过相同的参数和语义映射生成联动命令', () => {
    const document = reportFixture()
    const chart = findComponent(document, 'chart_sales')
    chart.interaction = {
      ...chart.interaction,
      clickFilter: true,
      linkage: { parameterId: 'param_region', operator: 'EQUALS', effectScope: { kind: 'COMPONENTS', componentIds: ['chart_sales', 'chart_cost'] } },
    }

    const result = buildReportInteractionCommand(document, { region: '华北' }, 'chart_sales', { value: '华南', label: '华南' })

    expect(result.issue).toBeUndefined()
    expect(result.command).toMatchObject({ type: 'CHART_FILTER', parameterCode: 'region', value: '华南', affectedComponentIds: ['chart_sales', 'chart_cost'] })
    expect(result.command?.targets).toEqual([
      {
        componentId: 'chart_sales', datasetVersionId: 'dsv_sales', semanticFieldCode: 'enterprise_region', fieldId: 'field_sales_region',
        datasetParameterCode: 'sales_region', operator: 'EQUALS',
      },
      {
        componentId: 'chart_cost', datasetVersionId: 'dsv_cost', semanticFieldCode: 'enterprise_region', fieldId: 'field_cost_area',
        datasetParameterCode: 'cost_area', operator: 'EQUALS',
      },
    ])
  })

  test('图表来源不在显式目标内时仍独立校验点击维度', () => {
    const document = reportFixture()
    const chart = findComponent(document, 'chart_sales')
    chart.interaction = {
      ...chart.interaction,
      clickFilter: true,
      linkage: { parameterId: 'param_region', operator: 'EQUALS', effectScope: { kind: 'COMPONENTS', componentIds: ['chart_cost'] } },
    }

    const result = buildReportInteractionCommand(document, {}, 'chart_sales', { value: '华南', label: '华南' })

    expect(result.issue).toBeUndefined()
    expect(result.command?.targets.map(item => item.componentId)).toEqual(['chart_cost'])
  })
})

describe('报告图表下钻命令', () => {
  test('生成下钻和返回上级命令，且只刷新来源图表', () => {
    const document = reportFixture()
    const down = buildReportInteractionCommand(document, { region: '华东' }, 'chart_sales', { value: '杭州市', drillLevel: 0, drillDirection: 'DOWN' })
    const upToLevel = buildReportInteractionCommand(document, { region: '华东' }, 'chart_sales', { value: undefined, drillLevel: 0, drillDirection: 'UP' })
    const up = buildReportInteractionCommand(document, { region: '华东' }, 'chart_sales', { value: undefined, drillLevel: -1, drillDirection: 'UP' })

    expect(down.command).toEqual({
      type: 'DRILL_DOWN',
      sourceComponentId: 'chart_sales',
      parameters: { region: '华东' },
      affectedComponentIds: ['chart_sales'],
      targets: [],
      drill: { componentId: 'chart_sales', level: 0, fieldId: 'field_city', semanticFieldCode: 'enterprise_city', label: '城市' },
    })
    expect(up.command).toEqual({
      type: 'DRILL_UP', sourceComponentId: 'chart_sales', parameters: { region: '华东' }, affectedComponentIds: ['chart_sales'], targets: [],
    })
    expect(upToLevel.command).toMatchObject({
      type: 'DRILL_UP', drill: { componentId: 'chart_sales', level: 0, fieldId: 'field_city' },
    })
  })

  test('下钻层级越界时失败关闭', () => {
    const result = buildReportInteractionCommand(reportFixture(), {}, 'chart_sales', { value: undefined, drillLevel: 2 })
    expect(result.command).toBeUndefined()
    expect(result.issue?.reason).toBe('下钻层级超出配置范围')
  })
})

describe('交互解析不可变性', () => {
  test('成功与失败解析都不修改报告定义或调用方参数', () => {
    const document = reportFixture()
    const originalDocument = structuredClone(document)
    const parameters = { region: '华北', nested: { retained: true }, items: ['a', 'b'] }
    const originalParameters = structuredClone(parameters)

    const success = buildReportInteractionCommand(document, parameters, 'filter_region', { value: '华东' })
    expect(success.command?.parameters).not.toBe(parameters)
    ;(success.command?.parameters.nested as { retained: boolean }).retained = false
    ;(success.command?.parameters.items as string[])[0] = 'changed'
    expect(document).toEqual(originalDocument)
    expect(parameters).toEqual(originalParameters)

    setFilterScope(document, { kind: 'COMPONENTS', componentIds: ['component_missing'] })
    const beforeFailure = structuredClone(document)
    buildReportInteractionCommand(document, parameters, 'filter_region', { value: '华南' })
    expect(document).toEqual(beforeFailure)
    expect(parameters).toEqual(originalParameters)
  })
})

function parameter(overrides: Partial<ReportParameter> = {}): ReportParameter {
  return {
    id: 'param_region',
    code: 'region',
    name: '区域',
    dataType: 'STRING',
    required: false,
    multiValue: false,
    scope: 'REPORT',
    ...overrides,
  }
}

function reportFixture(): ReportDocument {
  return {
    schemaVersion: '1.0',
    report: {
      code: 'interaction_fixture', name: '联动测试报告', type: 'DASHBOARD', language: 'zh-CN', status: 'DRAFT', visibility: 'PRIVATE',
      onlineEnabled: true, pdfArchiveEnabled: false, defaultRefreshPolicy: 'REALTIME', timezone: 'Asia/Shanghai',
    },
    canvas: {
      logicalWidth: 1920, viewportHeight: 1080, gridColumns: 12, viewportGridRows: 10, contentGridRows: 'AUTO', minContentGridRows: 10,
      innerGridMultiplier: 4, scaleMode: 'FIT_WIDTH', verticalOverflow: 'SCROLL',
    },
    parameters: [{
      ...parameter({ required: true }),
      semanticBinding: {
        semanticFieldCode: 'enterprise_region',
        datasetFields: [
          { datasetVersionId: 'dsv_sales', fieldId: 'field_sales_region', datasetParameterCode: 'sales_region' },
          { datasetVersionId: 'dsv_cost', fieldId: 'field_cost_area', datasetParameterCode: 'cost_area' },
        ],
      },
    }],
    pages: [
      {
        id: 'page_overview', name: '概览', order: 1, contentGridRows: 10,
        blocks: [
          {
            id: 'block_primary', grid: { x: 0, y: 0, w: 8, h: 5 }, innerGrid: { columns: 32, rows: 20 },
            locks: { layout: false, config: false, dataSnapshot: false }, sticky: { enabled: false },
            components: [
              component('filter_region', 'FILTER', undefined, {
                binding: { parameterId: 'param_region', operator: 'EQUALS', effectScope: { kind: 'BLOCK' }, placeholder: '请选择区域' },
              }),
              component('table_sales', 'TABLE', 'dsv_sales'),
              component('chart_sales', 'CHART', 'dsv_sales', {
                binding: { datasetVersionId: 'dsv_sales', dimensions: [{ fieldId: 'field_sales_region', role: 'CATEGORY' }] },
                interaction: {
                  clickFilter: false,
                  drill: { levels: [{ fieldId: 'field_city', semanticFieldCode: 'enterprise_city', label: '城市' }] },
                },
              }),
            ],
          },
          {
            id: 'block_secondary', grid: { x: 8, y: 0, w: 4, h: 5 }, innerGrid: { columns: 16, rows: 20 },
            locks: { layout: false, config: false, dataSnapshot: false }, sticky: { enabled: false },
            components: [component('chart_cost', 'CHART', 'dsv_cost')],
          },
        ],
      },
      {
        id: 'page_remote', name: '异地页', order: 2, contentGridRows: 10,
        blocks: [{
          id: 'block_remote', grid: { x: 0, y: 0, w: 12, h: 5 }, innerGrid: { columns: 48, rows: 20 },
          locks: { layout: false, config: false, dataSnapshot: false }, sticky: { enabled: false },
          components: [component('table_remote', 'TABLE', 'dsv_cost')],
        }],
      },
    ],
  }
}

function component(id: string, type: ReportComponent['type'], datasetVersionId?: string, overrides: Partial<ReportComponent> = {}): ReportComponent {
  return {
    id,
    type,
    name: id,
    grid: { x: 0, y: 0, w: 8, h: 4 },
    visible: true,
    manualLocked: false,
    binding: datasetVersionId ? { datasetVersionId } : {},
    interaction: {},
    sticky: { enabled: false },
    refreshPolicy: { mode: type === 'FILTER' ? 'INHERIT' : 'INHERIT' },
    sourceTrace: [],
    ...overrides,
  }
}

function findComponent(document: ReportDocument, id: string): ReportComponent {
  const result = document.pages.flatMap(page => page.blocks).flatMap(block => block.components).find(item => item.id === id)
  if (!result) throw new Error(`测试组件 ${id} 不存在`)
  return result
}

function setFilterScope(document: ReportDocument, scope: ReportEffectScope) {
  const filter = findComponent(document, 'filter_region')
  filter.binding = { ...filter.binding, effectScope: scope }
}
