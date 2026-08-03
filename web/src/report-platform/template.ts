import type { CardType, ReportCardDefinition, ReportDefinition, ReportGrid } from './types'

export const REPORT_SCHEMA_URL = 'https://schemas.intelligent-report.local/report-1.0.schema.json' as const

export function createReportDefinition(): ReportDefinition {
  return {
    $schema: REPORT_SCHEMA_URL,
    schemaVersion: '1.0.0',
    report: {
      code: `report_${new Date().toISOString().replace(/\D/g, '').slice(0, 14)}`,
      title: '未命名经营报表',
      name: '未命名经营报表',
      type: 'REPORT',
      status: 'DRAFT',
      themeId: 'business-light',
      language: 'zh-CN',
      timezone: 'Asia/Shanghai',
      visibility: 'PRIVATE',
      onlineEnabled: true,
      pdfArchiveEnabled: false,
      defaultRefreshPolicy: 'CACHE',
    },
    layout: { columns: 12, rowHeight: 8, margin: 12, breakpoints: { lg: 1200, md: 768, sm: 0 } },
    globalFilters: [],
    cards: [createCard('TITLE', 'report-title', { x: 0, y: 0, w: 12, h: 8 })],
  }
}

export function createCard(type: CardType, id = `${type.toLowerCase()}-${crypto.randomUUID().slice(0, 8)}`, lg?: ReportGrid): ReportCardDefinition {
  const grid = lg ?? defaultGrid(type)
  const definition: ReportCardDefinition = {
    id,
    type,
    cardVersion: '1.0.0',
    layout: {
      lg: { ...grid },
      md: { x: 0, y: grid.y, w: 12, h: grid.h },
      sm: { x: 0, y: grid.y, w: 12, h: Math.max(grid.h, type === 'TABLE' ? 28 : 18) },
    },
    appearance: { title: cardTitle(type), showHeader: type !== 'TITLE', heightMode: 'FIXED' },
    config: type === 'CHART' ? { chartType: 'bar', legend: true } : {},
    binding: { semanticModelId: '', metrics: [], dimensions: [], globalFilterBindings: [], filters: [], sort: [], limit: type === 'RANKING' ? 10 : 100 },
    interactions: [],
  }
  if (type === 'TITLE') definition.config = { text: '经营分析报告', subtitle: '拖入卡片并绑定已发布指标' }
  if (type === 'CONCLUSION') definition.config = { template: '本期{metric}为{value}，较基线{change}。' }
  return definition
}

function defaultGrid(type: CardType): ReportGrid {
  if (type === 'TITLE') return { x: 0, y: 0, w: 12, h: 8 }
  if (type === 'COMPARISON' || type === 'CONCLUSION') return { x: 0, y: 10, w: 4, h: 18 }
  if (type === 'TABLE') return { x: 0, y: 10, w: 12, h: 32 }
  return { x: 0, y: 10, w: 6, h: 28 }
}

function cardTitle(type: CardType): string {
  return { TITLE: '报告标题', CONCLUSION: '业务结论', CHART: '指标趋势', COMPARISON: '指标对比', RANKING: '指标排名', TABLE: '明细表格' }[type]
}
