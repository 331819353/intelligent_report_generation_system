import type { ComponentType, Grid, ReportComponent } from './report-contract'

export type ReportComponentCategory = 'CONTENT' | 'DATA' | 'MEDIA' | 'META'

export type ReportComponentDefinition = {
  type: ComponentType
  label: string
  category: ReportComponentCategory
  defaultGrid: Pick<Grid, 'w' | 'h'>
  minimumGrid: Pick<Grid, 'w' | 'h'>
  defaultStyle: Record<string, unknown>
  defaultBinding: Record<string, unknown>
  defaultInteraction: Record<string, unknown>
  defaultRefreshPolicy: Record<string, unknown>
}

export const formalReportComponentTypes = [
  'TITLE', 'RICH_TEXT', 'FILTER', 'KPI', 'ADDITIONAL_INFO', 'TABLE', 'CHART', 'IMAGE',
  'ATTACHMENT_LIST', 'DATA_SOURCE', 'UPDATED_AT', 'CONCLUSION',
] as const satisfies readonly ComponentType[]

export type FormalReportComponentType = typeof formalReportComponentTypes[number]

/**
 * 第一批正式组件的唯一目录。
 * 设计器面板、组件工厂、最小尺寸校验和渲染注册表都从此处派生，避免多处枚举漂移。
 */
export const reportComponentCatalog: Record<FormalReportComponentType, ReportComponentDefinition> = {
  TITLE: definition('TITLE', '标题', 'CONTENT', [24, 4], [4, 2], { fontSize: 34, fontWeight: 700 }, { text: '请输入报告标题' }, {}, 'NONE'),
  RICH_TEXT: definition('RICH_TEXT', '富文本', 'CONTENT', [16, 8], [4, 3], { align: 'LEFT' }, { blocks: [{ type: 'PARAGRAPH', text: '请输入正文内容' }] }, {}, 'NONE'),
  FILTER: definition('FILTER', '筛选器', 'DATA', [12, 4], [4, 3], { layout: 'HORIZONTAL' }, { parameterId: '', placeholder: '请选择', control: 'SELECT', operator: 'EQUALS', effectScope: { kind: 'REPORT' }, options: [] }, { autoSubmit: true }, 'INHERIT'),
  KPI: definition('KPI', '指标卡', 'DATA', [12, 8], [4, 4], { trendVisible: true }, { datasetVersionId: '', metricId: '', format: 'AUTO' }, {}, 'INHERIT'),
  ADDITIONAL_INFO: definition('ADDITIONAL_INFO', '附加信息卡', 'CONTENT', [12, 8], [4, 4], { tone: 'NEUTRAL' }, { title: '附加信息', text: '请输入说明' }, {}, 'NONE'),
  TABLE: definition('TABLE', '表格', 'DATA', [24, 16], [4, 4], { striped: true, compact: false }, { datasetVersionId: '', columns: [] }, {}, 'INHERIT'),
  CHART: definition('CHART', '图表', 'DATA', [24, 16], [4, 4], { legendPosition: 'TOP' }, { datasetVersionId: '', metricIds: [], dimensions: [], chart: { type: 'COLUMN' } }, { clickFilter: false }, 'INHERIT'),
  IMAGE: definition('IMAGE', '图片', 'MEDIA', [16, 12], [4, 4], { fit: 'CONTAIN' }, { url: '', alt: '报告图片' }, {}, 'NONE'),
  ATTACHMENT_LIST: definition('ATTACHMENT_LIST', '附件', 'MEDIA', [16, 8], [4, 4], { showSize: true }, { attachments: [] }, {}, 'NONE'),
  DATA_SOURCE: definition('DATA_SOURCE', '数据来源', 'META', [24, 4], [4, 2], { compact: true }, { datasetVersionIds: [] }, {}, 'ON_REPORT_RUN'),
  UPDATED_AT: definition('UPDATED_AT', '更新时间', 'META', [12, 4], [4, 2], { format: 'DATETIME' }, { label: '更新时间' }, {}, 'ON_REPORT_RUN'),
  CONCLUSION: definition('CONCLUSION', '结论', 'DATA', [16, 12], [4, 4], { showEvidenceLinks: true }, { metricIds: [], chartComponentIds: [] }, { showRevisionHistory: true }, 'ON_REPORT_RUN'),
}

/** 从正式目录构建完整默认配置，禁止新组件继续产生开放的临时对象。 */
export function createReportComponentDefaults(type: FormalReportComponentType): Pick<ReportComponent, 'name' | 'style' | 'binding' | 'interaction' | 'refreshPolicy'> {
  const item = reportComponentCatalog[type]
  return {
    name: item.label,
    style: structuredClone(item.defaultStyle),
    binding: structuredClone(item.defaultBinding),
    interaction: structuredClone(item.defaultInteraction),
    refreshPolicy: structuredClone(item.defaultRefreshPolicy),
  }
}

export function isFormalReportComponentType(type: ComponentType | string): type is FormalReportComponentType {
  return Object.prototype.hasOwnProperty.call(reportComponentCatalog, type)
}

function definition(
  type: FormalReportComponentType,
  label: string,
  category: ReportComponentCategory,
  defaultSize: [number, number],
  minimumSize: [number, number],
  defaultStyle: Record<string, unknown>,
  defaultBinding: Record<string, unknown>,
  defaultInteraction: Record<string, unknown>,
  refreshMode: 'NONE' | 'INHERIT' | 'ON_REPORT_RUN',
): ReportComponentDefinition {
  return {
    type,
    label,
    category,
    defaultGrid: { w: defaultSize[0], h: defaultSize[1] },
    minimumGrid: { w: minimumSize[0], h: minimumSize[1] },
    defaultStyle,
    defaultBinding,
    defaultInteraction,
    defaultRefreshPolicy: { mode: refreshMode },
  }
}
