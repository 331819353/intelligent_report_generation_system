import type { ReportTemplate } from './report-contract'

export const defaultReportTemplate: ReportTemplate = {
  id: 'template_business_light',
  name: '商务浅色',
  promptContext: '使用克制、专业、清晰的经营分析风格。标题简洁，图表优先表达趋势和对比；分块使用浅色背景与适度留白，结论必须突出证据和行动建议。',
  typography: {
    fontFamily: 'SYSTEM',
    title: { fontSize: 18, color: '#172033', fontWeight: 700 },
    body: { fontSize: 12, color: '#344054' },
  },
  palette: {
    primary: '#2864DC',
    accent: '#0E7490',
    muted: '#667085',
  },
  canvas: {
    backgroundColor: '#F8FAFC',
    gridColor: '#DFE6EE',
  },
  block: {
    backgroundColor: '#FFFFFF',
    borderColor: '#E1E7EF',
    borderRadius: 14,
    padding: 2,
    shadow: 'SOFT',
  },
}

/** 把安全设计令牌合成为 AI 可直接注入的全局上下文，不包含任意 CSS 或脚本。 */
export function buildReportTemplatePromptContext(template: ReportTemplate): string {
  return [
    template.promptContext.trim(),
    `全局字体：${fontFamilyLabel(template.typography.fontFamily)}。`,
    `标题：${template.typography.title.fontSize}px、字重 ${template.typography.title.fontWeight}、颜色 ${template.typography.title.color}。`,
    `正文：${template.typography.body.fontSize}px、颜色 ${template.typography.body.color}。`,
    `主色 ${template.palette.primary}，强调色 ${template.palette.accent}，辅助色 ${template.palette.muted}。`,
    `分块：背景 ${template.block.backgroundColor}、边框 ${template.block.borderColor}、圆角 ${template.block.borderRadius}px、内边距 ${template.block.padding}px、阴影 ${template.block.shadow}。`,
  ].filter(Boolean).join('\n')
}

export function reportTemplateCSSVariables(template: ReportTemplate | undefined): Record<string, string> {
  const value = template ?? defaultReportTemplate
  return {
    '--report-font-family': fontFamilyCSS(value.typography.fontFamily),
    '--report-title-size': `${value.typography.title.fontSize}px`,
    '--report-title-color': value.typography.title.color,
    '--report-title-weight': String(value.typography.title.fontWeight),
    '--report-body-size': `${value.typography.body.fontSize}px`,
    '--report-body-color': value.typography.body.color,
    '--report-primary': value.palette.primary,
    '--report-accent': value.palette.accent,
    '--report-muted': value.palette.muted,
    '--report-canvas-background': value.canvas.backgroundColor,
    '--report-grid-color': value.canvas.gridColor,
    '--report-block-background': value.block.backgroundColor,
    '--report-block-border': value.block.borderColor,
    '--report-block-radius': `${value.block.borderRadius}px`,
    '--report-block-padding': `${value.block.padding}px`,
    '--report-block-shadow': shadowCSS(value.block.shadow),
  }
}

function fontFamilyCSS(fontFamily: ReportTemplate['typography']['fontFamily']): string {
  if (fontFamily === 'SERIF') return 'Georgia, "Songti SC", serif'
  if (fontFamily === 'MONOSPACE') return 'ui-monospace, SFMono-Regular, Menlo, monospace'
  return 'Inter, "PingFang SC", "Microsoft YaHei", system-ui, sans-serif'
}

function fontFamilyLabel(fontFamily: ReportTemplate['typography']['fontFamily']): string {
  return { SYSTEM: '系统无衬线字体', SERIF: '衬线字体', MONOSPACE: '等宽字体' }[fontFamily]
}

function shadowCSS(shadow: ReportTemplate['block']['shadow']): string {
  if (shadow === 'NONE') return 'none'
  if (shadow === 'MEDIUM') return '0 10px 28px #22304826, 0 2px 6px #22304814'
  return '0 5px 18px #22304812, 0 1px 3px #2230480d'
}
