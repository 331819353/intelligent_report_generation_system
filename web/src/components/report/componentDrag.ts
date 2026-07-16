import type { ComponentType } from '../../lib/report-contract'
import { formalReportComponentTypes, reportComponentCatalog } from '../../lib/report-component-catalog'

export const REPORT_COMPONENT_DRAG_MIME = 'application/x-intelligent-report-component'

export const reportComponentPaletteItems: Array<{ type: ComponentType; label: string }> = formalReportComponentTypes.map(type => ({
  type,
  label: reportComponentCatalog[type].label,
}))

/** 原生拖放数据属于不可信输入，进入报告文档前必须收敛到合同枚举。 */
export function parseDraggedComponentType(value: string): ComponentType | undefined {
  return reportComponentPaletteItems.find(item => item.type === value)?.type
}
