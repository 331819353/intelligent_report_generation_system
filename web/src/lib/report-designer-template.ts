import reportExample from '../../../api/examples/report-json-v1.json'
import type { ReportBlock, ReportComponent, ReportDocument } from './report-contract'
import { defaultReportTemplate } from './report-template'

type BlockTemplate = {
  id: string
  name: string
  x: number
  y: number
  title: string
  showFilter?: boolean
  showChart?: boolean
  showConclusion?: boolean
  chartType?: 'LINE' | 'COLUMN' | 'BAR' | 'PIE'
}

const BLOCK_TEMPLATES: BlockTemplate[] = [
  { id: 'revenue', name: '营业收入趋势', x: 0, y: 0, title: '营业收入', showFilter: true, showChart: true, showConclusion: true, chartType: 'LINE' },
  { id: 'profit', name: '利润达成情况', x: 4, y: 0, title: '利润达成', showChart: true, chartType: 'COLUMN' },
  { id: 'customer', name: '客户增长分析', x: 8, y: 0, title: '客户增长', showChart: true, showConclusion: true, chartType: 'LINE' },
  { id: 'region', name: '区域经营表现', x: 0, y: 3, title: '区域表现', showFilter: true, showChart: true, chartType: 'BAR' },
  { id: 'product', name: '产品收入结构', x: 4, y: 3, title: '产品结构', showChart: true, showConclusion: true, chartType: 'PIE' },
  { id: 'risk', name: '经营风险提示', x: 8, y: 3, title: '风险提示', showConclusion: true },
]

/** 新建设计稿直接呈现两行 4×3 分块，配置与统一渲染器共用同一份报告 JSON。 */
export function createReportDesignerTemplate(): ReportDocument {
  const document = structuredClone(reportExample) as ReportDocument
  const sourceBlock = document.pages[0].blocks[0]
  const page = document.pages[0]

  page.blocks = BLOCK_TEMPLATES.map(template => createContentBlock(sourceBlock, template))
  page.contentGridRows = 10
  document.template = structuredClone(defaultReportTemplate)
  document.report.name = '企业经营月度分析报告'
  document.report.description = '由 160 × 108 逻辑分格驱动的可配置经营报告'
  return document
}

function createContentBlock(source: ReportBlock, template: BlockTemplate): ReportBlock {
  const titleID = `title_${template.id}`
  const filterID = `filter_${template.id}`
  const chartID = `chart_${template.id}`
  const conclusionID = `conclusion_${template.id}`
  const titleWidth = template.showFilter ? 10 : 16
  const bodyWidth = template.showChart && template.showConclusion ? 10 : 16
  const components: ReportComponent[] = [
    createTitle(source.components[0], titleID, template.title, titleWidth),
  ]

  if (template.showFilter) components.push(createFilter(source.components[1], filterID))
  if (template.showChart) components.push(createChart(source.components[2], chartID, template.name, template.chartType ?? 'LINE', bodyWidth))
  if (template.showConclusion) {
    components.push(createConclusion(
      source.components[3],
      conclusionID,
      template.name,
      template.showChart ? chartID : undefined,
      template.showChart ? 10 : 0,
      template.showChart ? 6 : 16,
    ))
  }

  return {
    id: `block_${template.id}`,
    kind: 'CONTENT',
    name: template.name,
    visible: true,
    grid: { x: template.x, y: template.y, w: 4, h: 3 },
    innerGrid: { columns: 16, rows: 12 },
    locks: { layout: false, config: false, dataSnapshot: false },
    sticky: { enabled: false },
    style: { padding: 2, background: 'WHITE', radius: 14, shadow: 'SOFT' },
    permissionPolicy: structuredClone(source.permissionPolicy),
    contentLayout: {
      visible: true,
      areas: {
        title: { visible: true, componentIds: [titleID] },
        filter: { visible: template.showFilter === true, componentIds: template.showFilter ? [filterID] : [] },
        conclusion: { visible: template.showConclusion === true, componentIds: template.showConclusion ? [conclusionID] : [] },
        chart: { visible: template.showChart === true, componentIds: template.showChart ? [chartID] : [] },
      },
    },
    components,
  }
}

function createTitle(source: ReportComponent, id: string, text: string, width: number): ReportComponent {
  const component = structuredClone(source)
  component.id = id
  component.name = `${text}标题`
  component.grid = { x: 0, y: 0, w: width, h: 3 }
  component.manualLocked = false
  component.sticky = { enabled: false }
  component.binding = { text }
  return component
}

function createFilter(source: ReportComponent, id: string): ReportComponent {
  const component = structuredClone(source)
  component.id = id
  component.name = '统计周期'
  component.grid = { x: 10, y: 0, w: 6, h: 3 }
  component.sticky = { enabled: false }
  return component
}

function createChart(source: ReportComponent, id: string, name: string, type: 'LINE' | 'COLUMN' | 'BAR' | 'PIE', width: number): ReportComponent {
  const component = structuredClone(source)
  component.id = id
  component.name = name
  component.grid = { x: 0, y: 3, w: width, h: 9 }
  component.sticky = { enabled: false }
  component.binding = {
    ...component.binding,
    chart: { type },
  }
  component.interaction = { clickFilter: false }
  return component
}

function createConclusion(source: ReportComponent, id: string, name: string, chartID: string | undefined, x: number, width: number): ReportComponent {
  const component = structuredClone(source)
  component.id = id
  component.name = `${name}结论`
  component.grid = { x, y: 3, w: width, h: 9 }
  component.sticky = { enabled: false }
  component.binding = {
    ...component.binding,
    chartComponentIds: chartID ? [chartID] : [],
  }
  return component
}
