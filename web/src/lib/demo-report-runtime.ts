import type { ComponentRuntimeState, ReportRuntimeContext } from './report-contract'
import type { ReportInteractionExecutor } from './report-interactions'

const monthlyRevenuePoints = [
  chartPoint('month_2026_01', '1月', 42, { stat_month: '2026-01' }),
  chartPoint('month_2026_02', '2月', 48, { stat_month: '2026-02' }),
  chartPoint('month_2026_03', '3月', 46, { stat_month: '2026-03' }),
  chartPoint('month_2026_04', '4月', 57, { stat_month: '2026-04' }),
  chartPoint('month_2026_05', '5月', 63, { stat_month: '2026-05' }),
  chartPoint('month_2026_06', '6月', 72, { stat_month: '2026-06' }),
]

const quarterRevenuePoints = [
  chartPoint('quarter_2026_q1', '一季度', 136, { stat_quarter: '2026-Q1' }),
  chartPoint('quarter_2026_q2', '二季度', 192, { stat_quarter: '2026-Q2' }),
]

/** 示例运行上下文与报告 JSON 分离，模拟后续报告运行服务返回的数据。 */
export const demoReportRuntime: ReportRuntimeContext = {
  parameters: { stat_month: '2026-06' },
  parameterOptions: {
    stat_month: {
      status: 'READY',
      options: [
        { label: '2026年4月', value: '2026-04' },
        { label: '2026年5月', value: '2026-05' },
        { label: '2026年6月', value: '2026-06' },
      ],
    },
  },
  componentData: {
    chart_revenue_trend: {
      status: 'READY',
      data: { points: monthlyRevenuePoints },
      updatedAt: '2026-07-15T10:30:00+08:00',
    },
    chart_revenue_trend_growth: {
      status: 'READY',
      data: { points: quarterRevenuePoints },
      updatedAt: '2026-07-15T10:30:00+08:00',
    },
    conclusion_overview: {
      status: 'READY',
      data: { summary: '营业收入连续三个月增长，二季度增长动能较一季度明显增强。' },
      updatedAt: '2026-07-15T10:30:00+08:00',
    },
    conclusion_overview_growth: {
      status: 'READY',
      data: { summary: '二季度营业收入较一季度提升 41.2%，增长动能持续增强。' },
      updatedAt: '2026-07-15T10:30:00+08:00',
    },
    source_note: {
      status: 'READY',
      data: { sources: ['企业经营数据集 V3'], updatedAtLabel: '数据更新于 2026-07-15 10:30' },
    },
  },
  permissions: ['report:view', 'report:edit'],
  roleCodes: ['REPORT_VIEWER', 'REPORT_DESIGNER'],
}

/**
 * 示例执行器只模拟报告运行服务的返回合同。
 * 真正上线时服务端必须重新解析作用域和字段映射，不能信任浏览器提交的 targets。
 */
export const demoReportInteractionExecutor: ReportInteractionExecutor = async command => {
  const now = new Date().toISOString()
  let points = monthlyRevenuePoints
  // 返回到中间层级时类型是 DRILL_UP，但展示数据仍由目标层级决定。
  if (command.drill?.semanticFieldCode === 'stat_quarter') points = quarterRevenuePoints
  if (command.type === 'PARAMETER_CHANGE' || command.type === 'CHART_FILTER') {
    points = monthlyRevenuePoints.filter(point => point.semanticValues.stat_month === command.parameters.stat_month)
  }
  const month = typeof command.parameters.stat_month === 'string' ? command.parameters.stat_month : undefined
  const result: Record<string, ComponentRuntimeState> = {}
  if (command.affectedComponentIds.includes('chart_revenue_trend')) {
    result.chart_revenue_trend = { status: 'READY', data: { points }, updatedAt: now }
  }
  if (command.affectedComponentIds.includes('conclusion_overview')) {
    const selected = points[0]
    result.conclusion_overview = {
      status: 'READY',
      data: { summary: month && selected ? `${month} 营业收入为 ${selected.value}，筛选结果已同步到图表与结论。` : '营业收入连续三个月增长，二季度增长动能较一季度明显增强。' },
      updatedAt: now,
    }
  }
  return { componentData: result }
}

function chartPoint(id: string, label: string, value: number, semanticValues: Record<string, string>) {
  return { id, label, value, semanticValues }
}
