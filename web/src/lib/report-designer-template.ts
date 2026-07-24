import reportExample from '../../../api/examples/report-json-v1.json'
import type { ReportBlock, ReportDocument, ReportMenuRatios } from './report-contract'

const DEFAULT_MENU_RATIOS: ReportMenuRatios = {
  topColumns: [3, 1],
  bottomColumns: [1, 1],
  rowHeights: [2, 1],
}

/** 新建设计稿使用层级优先模板；正式示例 JSON 保持兼容，避免影响历史草稿。 */
export function createReportDesignerTemplate(): ReportDocument {
  const document = structuredClone(reportExample) as ReportDocument
  const page = document.pages[0]
  const overview = structuredClone(page.blocks[0])

  const menu: ReportBlock = {
    id: 'block_menu',
    kind: 'MENU',
    name: '菜单区',
    visible: true,
    grid: { x: 0, y: 0, w: 12, h: 2 },
    innerGrid: { columns: 48, rows: 8 },
    zIndex: 200,
    locks: { layout: true, config: false, dataSnapshot: false },
    sticky: { enabled: true, top: 0, scope: 'PAGE', zIndex: 200 },
    style: { padding: 0, background: 'NAVY' },
    menuLayout: {
      visible: true,
      defaultRatios: structuredClone(DEFAULT_MENU_RATIOS),
      ratios: structuredClone(DEFAULT_MENU_RATIOS),
      usesDefaultRatios: true,
      cells: {
        logoTitle: {
          visible: true,
          logoText: 'IR',
          title: '企业经营月度分析报告',
          subtitle: 'Intelligent Report',
        },
        actions: { visible: true, items: ['刷新', '导出', '更多'] },
        globalFilters: { visible: true, parameterIds: ['param_stat_month'] },
        navigation: {
          visible: true,
          items: [
            { label: '经营概览', targetBlockId: 'block_overview' },
            { label: '增长分析', targetBlockId: 'block_growth' },
          ],
        },
      },
    },
    components: [],
  }

  overview.kind = 'CONTENT'
  overview.name = '内容区 01 · 经营概览'
  overview.visible = true
  overview.grid = { x: 0, y: 2, w: 12, h: 8 }
  overview.innerGrid = { columns: 48, rows: 32 }
  overview.sticky = { enabled: false }
  overview.components[0].grid = { x: 0, y: 0, w: 32, h: 4 }
  overview.components[0].sticky = { enabled: false }
  overview.components[1].grid = { x: 32, y: 0, w: 16, h: 4 }
  overview.components[1].sticky = { enabled: false }
  overview.components[2].grid = { x: 0, y: 4, w: 32, h: 28 }
  overview.components[3].grid = { x: 32, y: 4, w: 16, h: 28 }
  overview.contentLayout = {
    visible: true,
    areas: {
      title: { visible: true, componentIds: ['title_main', 'filter_stat_month'] },
      conclusion: { visible: true, componentIds: ['conclusion_overview'] },
      components: { visible: true, componentIds: ['chart_revenue_trend'] },
    },
  }

  const growth = createGrowthBlock(overview)
  page.blocks = [menu, overview, growth]
  page.contentGridRows = 17
  document.report.name = '企业经营月度分析报告'
  return document
}

function createGrowthBlock(source: ReportBlock): ReportBlock {
  const block = structuredClone(source)
  block.id = 'block_growth'
  block.name = '内容区 02 · 增长分析'
  block.grid = { x: 0, y: 10, w: 12, h: 7 }
  block.innerGrid = { columns: 48, rows: 28 }
  block.components = block.components.map(component => {
    const next = structuredClone(component)
    next.id = `${component.id}_growth`
    next.name = component.type === 'TITLE' ? '增长分析标题'
      : component.type === 'FILTER' ? '增长分析筛选'
        : component.type === 'CHART' ? '季度增长趋势'
          : '增长分析结论'
    if (next.type === 'TITLE') {
      next.binding = { text: '增长与动能分析' }
      next.grid = { x: 0, y: 0, w: 32, h: 4 }
    } else if (next.type === 'FILTER') {
      next.grid = { x: 32, y: 0, w: 16, h: 4 }
    } else if (next.type === 'CHART') {
      next.grid = { x: 0, y: 4, w: 32, h: 24 }
    } else {
      next.grid = { x: 32, y: 4, w: 16, h: 24 }
      if (next.binding) next.binding = {
        ...next.binding,
        chartComponentIds: ['chart_revenue_trend_growth'],
      }
    }
    next.sticky = { enabled: false }
    return next
  })
  block.contentLayout = {
    visible: true,
    areas: {
      title: { visible: true, componentIds: ['title_main_growth', 'filter_stat_month_growth'] },
      conclusion: { visible: false, componentIds: ['conclusion_overview_growth'] },
      components: { visible: true, componentIds: ['chart_revenue_trend_growth'] },
    },
  }
  return block
}
