import type { CardType, ReportCardDefinition, ReportDefinition, ReportGrid } from './types'
import { createCard, REPORT_SCHEMA_URL } from './template'

type LegacyRecord = Record<string, unknown>

export type ReportMigrationResult = { definition: ReportDefinition; migrated: boolean; warnings: string[] }

export function migrateReportDefinition(source: unknown): ReportMigrationResult {
  if (isRecord(source) && source.schemaVersion === '1.0.0') return { definition: structuredClone(source) as ReportDefinition, migrated: false, warnings: [] }
  if (!isRecord(source) || source.schemaVersion !== '1.0') throw new Error('不支持的 Report DSL 版本')
  const report = isRecord(source.report) ? source.report : {}
  const cards: ReportCardDefinition[] = []
  const warnings: string[] = []
  const pages = Array.isArray(source.pages) ? source.pages : []
  for (const page of pages) {
    if (!isRecord(page) || !Array.isArray(page.blocks)) continue
    for (const block of page.blocks) {
      if (!isRecord(block) || !Array.isArray(block.components)) continue
      const blockGrid = readGrid(block.grid) ?? { x: 0, y: 0, w: 12, h: 20 }
      const innerGrid = isRecord(block.innerGrid) ? block.innerGrid : {}
      const innerColumns = positiveNumber(innerGrid.columns) ?? Math.max(1, blockGrid.w * 4)
      const innerRows = positiveNumber(innerGrid.rows) ?? Math.max(1, blockGrid.h * 4)
      for (const component of block.components) {
        if (!isRecord(component)) continue
        const type = migrateCardType(component.type)
        if (!type) { warnings.push(`旧组件 ${String(component.type)} 未进入新 Card SDK，迁移时已跳过`); continue }
        const componentGrid = readGrid(component.grid) ?? { x: 0, y: 0, w: innerColumns, h: innerRows }
        const grid = projectGrid(blockGrid, componentGrid, innerColumns, innerRows)
        const card = createCard(type, stringValue(component.id) || undefined, grid)
        card.appearance.title = stringValue(component.name) || card.appearance.title
        card.config = isRecord(component.style) ? structuredClone(component.style) : card.config
        const binding = isRecord(component.binding) ? component.binding : {}
        const metricIDs = stringArray(binding.metricIds)
        if (typeof binding.metricId === 'string') metricIDs.unshift(binding.metricId)
        card.binding.metrics = [...new Set(metricIDs)].map((id, index) => ({ id, role: index === 0 ? 'value' : 'series' }))
        const dimensionIDs = stringArray(binding.dimensionIds)
        if (typeof binding.dimensionId === 'string') dimensionIDs.unshift(binding.dimensionId)
        card.binding.dimensions = [...new Set(dimensionIDs)].map((id, index) => ({ id, role: index === 0 ? 'category' : 'series' }))
        card.binding.semanticModelId = stringValue(binding.semanticModelId) || stringValue(binding.datasetVersionId) || ''
        cards.push(card)
      }
    }
  }
  if (!cards.length) cards.push(createCard('TITLE', 'report-title', { x: 0, y: 0, w: 12, h: 8 }))
  packCardLayouts(cards)
  const title = stringValue(report.name) || stringValue(report.title) || '迁移报告'
  return {
    migrated: true,
    warnings,
    definition: {
      $schema: REPORT_SCHEMA_URL,
      schemaVersion: '1.0.0',
      report: {
        id: stringValue(report.id) || undefined,
        code: stringValue(report.code) || `migrated_${Date.now()}`,
        title,
        name: title,
        description: stringValue(report.description) || undefined,
        type: report.type === 'DASHBOARD' ? 'DASHBOARD' : 'REPORT',
        status: report.status === 'PUBLISHED' ? 'PUBLISHED' : 'DRAFT',
        themeId: 'business-light',
        language: stringValue(report.language) || 'zh-CN',
        timezone: stringValue(report.timezone) || 'Asia/Shanghai',
        visibility: report.visibility === 'TENANT' || report.visibility === 'PUBLIC' ? report.visibility : 'PRIVATE',
        onlineEnabled: report.onlineEnabled !== false,
        pdfArchiveEnabled: report.pdfArchiveEnabled === true,
        defaultRefreshPolicy: report.defaultRefreshPolicy === 'REALTIME' || report.defaultRefreshPolicy === 'MATERIALIZED' || report.defaultRefreshPolicy === 'SNAPSHOT' ? report.defaultRefreshPolicy : 'CACHE',
      },
      layout: { columns: 12, rowHeight: 8, margin: 12, breakpoints: { lg: 1200, md: 768, sm: 0 } },
      globalFilters: migrateFilters(source.parameters),
      cards,
    },
  }
}

function migrateFilters(value: unknown): ReportDefinition['globalFilters'] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item, index) => {
    if (!isRecord(item)) return []
    const semanticBinding = isRecord(item.semanticBinding) ? item.semanticBinding : {}
    const fields = Array.isArray(semanticBinding.datasetFields) ? semanticBinding.datasetFields : []
    const first = fields.find(isRecord) as LegacyRecord | undefined
    const dimensionId = stringValue(first?.fieldId) || stringValue(semanticBinding.semanticFieldCode)
    const modelId = stringValue(first?.datasetVersionId)
    if (!dimensionId || !modelId) return []
    return [{
      id: stringValue(item.id) || `filter-${index + 1}`,
      label: stringValue(item.name) || `筛选 ${index + 1}`,
      type: item.dataType === 'DATE_RANGE' ? 'DATE_RANGE' as const : item.multiValue === true ? 'MULTI_SELECT' as const : 'SELECT' as const,
      source: { semanticModelId: modelId, dimensionId }, operator: item.dataType === 'DATE_RANGE' ? 'between' as const : item.multiValue === true ? 'in' as const : 'equals' as const,
      defaultValue: item.defaultValue, required: item.required === true, multiValue: item.multiValue === true,
    }]
  })
}

function migrateCardType(value: unknown): CardType | undefined {
  return ({ TITLE: 'TITLE', CONCLUSION: 'CONCLUSION', CHART: 'CHART', KPI: 'COMPARISON', RANKING: 'RANKING', TABLE: 'TABLE', CROSSTAB: 'TABLE' } as Record<string, CardType>)[String(value)]
}

function projectGrid(block: ReportGrid, component: ReportGrid, columns: number, rows: number): ReportGrid {
  const x = Math.min(11, Math.max(0, block.x + Math.floor(component.x / columns * block.w)))
  const y = Math.max(0, block.y * 4 + Math.floor(component.y / rows * block.h * 4))
  const w = Math.min(12 - x, Math.max(1, Math.round(component.w / columns * block.w)))
  const h = Math.max(4, Math.round(component.h / rows * block.h * 4))
  return { x, y, w, h }
}

function packCardLayouts(cards: ReportCardDefinition[]) {
  let y = 0
  for (const card of cards) {
    const h = card.layout.lg.h
    card.layout.lg = { ...card.layout.lg, x: Math.min(card.layout.lg.x, 12 - card.layout.lg.w), y }
    card.layout.md = { x: 0, y, w: 12, h }
    card.layout.sm = { x: 0, y, w: 12, h: Math.max(h, 18) }
    y += h + 2
  }
}

function readGrid(value: unknown): ReportGrid | undefined {
  if (!isRecord(value)) return undefined
  const x = nonNegativeNumber(value.x), y = nonNegativeNumber(value.y), w = positiveNumber(value.w), h = positiveNumber(value.h)
  return x === undefined || y === undefined || w === undefined || h === undefined ? undefined : { x, y, w, h }
}
function isRecord(value: unknown): value is LegacyRecord { return Boolean(value) && typeof value === 'object' && !Array.isArray(value) }
function stringValue(value: unknown): string { return typeof value === 'string' ? value.trim() : '' }
function stringArray(value: unknown): string[] { return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string' && Boolean(item)) : [] }
function nonNegativeNumber(value: unknown): number | undefined { return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : undefined }
function positiveNumber(value: unknown): number | undefined { return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined }
