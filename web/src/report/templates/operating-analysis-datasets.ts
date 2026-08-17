import type { DatasetSummary } from '../../lib/datasets.ts'

export type OperatingAnalysisDatasetField = {
  code: string
  name: string
  description: string
  canonicalType: 'STRING' | 'DATE' | 'INTEGER' | 'DECIMAL'
  semanticType: 'IDENTIFIER' | 'DATE' | 'CATEGORY' | 'MEASURE'
  role: 'DIMENSION' | 'MEASURE'
  nullable?: boolean
}

export type OperatingAnalysisDatasetFixture = {
  subject: '经营损益' | '应收风险' | '库存物流'
  grain: string
  /** ADS 面向报告直接交付，不在目录中声明 DWS 前置依赖。 */
  upstreamCodes: string[]
  summary: DatasetSummary
  fields: OperatingAnalysisDatasetField[]
}

const dimension = (
  code: string,
  name: string,
  description: string,
  canonicalType: OperatingAnalysisDatasetField['canonicalType'] = 'STRING',
  semanticType: OperatingAnalysisDatasetField['semanticType'] = 'CATEGORY',
): OperatingAnalysisDatasetField => ({ code, name, description, canonicalType, semanticType, role: 'DIMENSION' })

const measure = (code: string, name: string, description: string): OperatingAnalysisDatasetField => ({
  code, name, description, canonicalType: 'DECIMAL', semanticType: 'MEASURE', role: 'MEASURE', nullable: true,
})

const reportMonth = dimension('report_month', '报告月份', '经营分析报告对应的自然月。', 'DATE', 'DATE')
const businessUnit = dimension('business_unit_code', '业务单元编码', '统一产业或经营组织编码。', 'STRING', 'IDENTIFIER')

function createADS(input: {
  subject: OperatingAnalysisDatasetFixture['subject']
  code: string
  name: string
  description: string
  grain: string
  fields: OperatingAnalysisDatasetField[]
  version: number
  updatedAt: string
}): OperatingAnalysisDatasetFixture {
  const id = `snapshot-${input.code.replaceAll('_', '-')}`
  return {
    subject: input.subject,
    grain: input.grain,
    upstreamCodes: [],
    fields: input.fields,
    summary: {
      id,
      code: input.code,
      name: input.name,
      description: input.description,
      type: 'CROSS_SOURCE',
      status: 'PUBLISHED',
      domainId: 'snapshot-enterprise-operations',
      layer: 'ADS',
      tags: ['TPL-RPT-001', input.subject, '报告直供'],
      version: input.version,
      dslHash: `tplrpt001ads${input.version}${input.code.length}`,
      currentPublishedVersionId: `${id}-v${input.version}`,
      updatedAt: input.updatedAt,
    },
  }
}

export const operatingAnalysisDatasetFixtures: OperatingAnalysisDatasetFixture[] = [
  createADS({
    subject: '经营损益',
    code: 'ads_operating_report_profit',
    name: '经营分析报告损益应用表',
    description: '自包含经营摘要、国内外收入与毛利、费用树、利润瀑布和次月预测所需数据，直接供 TPL-RPT-001 使用。',
    grain: '报告月 × 报告章节 × 对比口径 × 分析维度类型 × 分析维度值',
    version: 9,
    updatedAt: '2026-08-17T18:18:00+08:00',
    fields: [
      reportMonth,
      dimension('forecast_month', '预测月份', '损益预测对应月份。', 'DATE', 'DATE'),
      dimension('report_section_code', '报告章节编码', '核心摘要、整体损益、国内损益、海外损益或预测章节。'),
      dimension('business_scope', '境内外范围', '整体、国内或海外。'),
      businessUnit,
      dimension('region_code', '区域编码', '海外或国内区域编码。', 'STRING', 'IDENTIFIER'),
      dimension('chain_group_code', '链群编码', '链群经营组织编码。', 'STRING', 'IDENTIFIER'),
      dimension('channel_l1_code', '一级渠道编码', '大渠道编码。', 'STRING', 'IDENTIFIER'),
      dimension('channel_l2_code', '二级渠道编码', '小渠道编码。', 'STRING', 'IDENTIFIER'),
      dimension('category_code', '品类编码', '零售行情品类编码。', 'STRING', 'IDENTIFIER'),
      dimension('brand_family_code', '品牌系编码', '品牌系编码。', 'STRING', 'IDENTIFIER'),
      dimension('brand_code', '品牌编码', '经营品牌编码。', 'STRING', 'IDENTIFIER'),
      dimension('sales_scene', '销售场景', '线上或线下。'),
      dimension('expense_l1_code', '一级费用项目', '费用树一级项目编码。', 'STRING', 'IDENTIFIER'),
      dimension('expense_l2_code', '二级费用项目', '费用树二级项目编码。', 'STRING', 'IDENTIFIER'),
      dimension('expense_l3_code', '三级费用项目', '费用树三级项目编码。', 'STRING', 'IDENTIFIER'),
      dimension('comparison_type', '对比口径', '实际、目标、预测、同期、同比或完成率。'),
      dimension('analysis_dimension_type', '分析维度类型', '链群、渠道、品牌、区域、费用项目或利润动因。'),
      dimension('analysis_dimension_value', '分析维度值', '当前分析维度对应的成员值。'),
      measure('revenue_amount', '收入', '当前口径收入金额。'),
      measure('target_revenue_amount', '目标收入', '目标口径收入金额。'),
      measure('prior_year_revenue_amount', '同期收入', '上年同期收入金额。'),
      measure('revenue_completion_rate', '收入完成率', '实际收入除以目标收入。'),
      measure('revenue_yoy_rate', '收入同比', '实际收入相对同期的变化率。'),
      measure('revenue_share_rate', '收入占比', '品牌或渠道收入占整体收入比例。'),
      measure('average_unit_price', '单价', '收入除以有效销量。'),
      measure('retail_sales_yoy_rate', '零售额同比', '线上或线下零售额同比变化率。'),
      measure('market_share_yoy_delta', '份额同比', '市场份额同比变动。'),
      measure('sales_gross_margin_rate', '销售毛利率', '销售毛利除以收入。'),
      measure('target_gross_margin_rate', '目标毛利率', '目标口径销售毛利率。'),
      measure('prior_year_gross_margin_rate', '同期毛利率', '上年同期销售毛利率。'),
      measure('material_gross_margin_rate', '材料毛利率', '当前材料毛利率。'),
      measure('cost_impact_amount', '成本贡献', '成本变化对材料毛利率的贡献。'),
      measure('policy_impact_amount', '政策影响', '政策变化对材料毛利率的贡献。'),
      measure('price_impact_amount', '价格影响', '价格变化对材料毛利率的贡献。'),
      measure('structure_impact_amount', '结构影响', '业务结构变化对材料毛利率的贡献。'),
      measure('expense_amount', '费额', '当前口径费用金额。'),
      measure('target_expense_amount', '目标费额', '目标口径费用金额。'),
      measure('prior_year_expense_amount', '同期费额', '上年同期费用金额。'),
      measure('expense_amount_target_delta', '费额较目标', '实际费额减目标费额。'),
      measure('expense_amount_yoy_delta', '费额较同期', '实际费额减同期费额。'),
      measure('expense_rate', '费用率', '费用金额除以收入。'),
      measure('target_expense_rate', '目标费用率', '目标口径费用率。'),
      measure('prior_year_expense_rate', '同期费用率', '上年同期费用率。'),
      measure('expense_rate_target_delta', '费率较目标', '实际费率减目标费率。'),
      measure('expense_rate_yoy_delta', '费率同比差', '实际费率减同期费率。'),
      measure('management_profit_amount', '管理利润', '当前口径管理利润。'),
      measure('target_profit_amount', '目标利润', '目标口径管理利润。'),
      measure('prior_year_profit_amount', '同期利润', '上年同期管理利润。'),
      measure('profit_completion_rate', '利润完成率', '实际利润除以目标利润。'),
      measure('profit_rate', '利润率', '管理利润除以收入。'),
      measure('domestic_revenue_impact', '国内收入影响', '国内收入变化对利润的影响。'),
      measure('overseas_revenue_impact', '海外收入影响', '海外收入变化对利润的影响。'),
      measure('domestic_margin_impact', '国内毛利率影响', '国内毛利率变化对利润的影响。'),
      measure('overseas_margin_impact', '海外毛利率影响', '海外毛利率变化对利润的影响。'),
      measure('sales_expense_impact', '销售费用影响', '销售费用变化对利润的影响。'),
      measure('management_expense_impact', '管理费用影响', '管理费用变化对利润的影响。'),
      measure('rd_expense_impact', '研发费用影响', '研发费用变化对利润的影响。'),
      measure('finance_expense_impact', '财务费用影响', '财务费用变化对利润的影响。'),
      measure('manufacturing_expense_impact', '制造费用影响', '制造费用变化对利润的影响。'),
      measure('other_profit_impact', '其他倒挤', '其余无法归类的利润差异。'),
      measure('forecast_revenue_amount', '预测收入', '次月预测收入。'),
      measure('forecast_profit_amount', '预测利润', '次月预测管理利润。'),
      measure('risk_level', '风险等级', '按目标偏差与同比变化生成的风险等级数值。'),
    ],
  }),

  createADS({
    subject: '应收风险',
    code: 'ads_operating_report_receivable',
    name: '经营分析报告应收应用表',
    description: '自包含应收整体、账龄结构、回款进展和重点客户风险数据，直接供 TPL-RPT-001 使用。',
    grain: '报告月 × 分析卡片 × 业务单元 × 客户 × 账龄区间',
    version: 7,
    updatedAt: '2026-08-17T18:16:00+08:00',
    fields: [
      reportMonth,
      dimension('analysis_card_code', '分析卡片编码', '应收整体、账龄风险或重点客户卡片。'),
      businessUnit,
      dimension('responsible_org_code', '责任组织编码', '回款责任组织编码。', 'STRING', 'IDENTIFIER'),
      dimension('responsible_owner_code', '责任人编码', '回款责任人编码。', 'STRING', 'IDENTIFIER'),
      dimension('customer_code', '客户编码', '统一客户编码。', 'STRING', 'IDENTIFIER'),
      dimension('customer_name', '客户名称', '客户业务名称。'),
      dimension('aging_bucket', '账龄区间', '0-30 天、31-60 天、61-90 天或 90 天以上。'),
      dimension('comparison_type', '对比口径', '本期、同期、目标、计划或实际。'),
      measure('receivable_balance_amount', '应收余额', '报告期末应收余额。'),
      measure('target_receivable_amount', '目标应收', '目标口径应收余额。'),
      measure('prior_year_receivable_amount', '同期应收', '上年同期应收余额。'),
      measure('overdue_receivable_amount', '逾期应收', '超过到期日仍未结清的应收余额。'),
      measure('overdue_rate', '逾期率', '逾期应收除以应收余额。'),
      measure('aging_0_30_amount', '0-30 天应收', '账龄 0-30 天应收金额。'),
      measure('aging_31_60_amount', '31-60 天应收', '账龄 31-60 天应收金额。'),
      measure('aging_61_90_amount', '61-90 天应收', '账龄 61-90 天应收金额。'),
      measure('aging_over_90_amount', '90 天以上应收', '账龄超过 90 天应收金额。'),
      measure('planned_collection_amount', '计划回款额', '本期计划回款金额。'),
      measure('actual_collection_amount', '实际回款额', '本期实际回款金额。'),
      measure('collection_gap_amount', '回款缺口', '计划回款额减实际回款额。'),
      measure('collection_completion_rate', '回款完成率', '实际回款额除以计划回款额。'),
      measure('customer_risk_rank', '客户风险排名', '按逾期金额降序生成的客户排名。'),
      measure('risk_level', '风险等级', '客户应收风险等级数值。'),
    ],
  }),

  createADS({
    subject: '库存物流',
    code: 'ads_operating_report_inventory_logistics',
    name: '经营分析报告库存物流应用表',
    description: '自包含物流费用、反向运输、零担、仓储利用率及库存周转风险数据，直接供 TPL-RPT-001 使用。',
    grain: '报告月 × 分析卡片 × 业务单元 × 仓库/物料/路线',
    version: 8,
    updatedAt: '2026-08-17T18:14:00+08:00',
    fields: [
      reportMonth,
      dimension('analysis_card_code', '分析卡片编码', '物流概览、反向运输、零担、仓储或库存风险卡片。'),
      businessUnit,
      dimension('industry_code', '产业编码', '产业编码。', 'STRING', 'IDENTIFIER'),
      dimension('sub_industry_code', '子产业编码', '子产业编码。', 'STRING', 'IDENTIFIER'),
      dimension('expense_scene', '费用场景', '物流费用归属场景。'),
      dimension('business_scene', '业务场景', '物流业务使用场景。'),
      dimension('warehouse_code', '仓库编码', '仓库唯一编码。', 'STRING', 'IDENTIFIER'),
      dimension('material_code', '物料编码', '物料唯一编码。', 'STRING', 'IDENTIFIER'),
      dimension('route_code', '运输路线编码', '运输路线唯一编码。', 'STRING', 'IDENTIFIER'),
      dimension('comparison_type', '对比口径', '本期、同期、同比、目标或风险阈值。'),
      measure('logistics_expense_amount', '物流费用', '报告展示物流费用。'),
      measure('transport_volume', '体积', '报告展示运输体积。'),
      measure('warehouse_area', '面积', '报告展示仓储面积。'),
      measure('logistics_unit_price', '物流单价', '物流费用除以运输体积。'),
      measure('reverse_transport_amount', '反向运输金额', '报告展示反向运输金额。'),
      measure('potential_saving_amount', '潜在节约金额', '路线优化预计可节约金额。'),
      measure('ltl_expense_amount', '零担运费', '零担运输费用。'),
      measure('ltl_volume', '零担体积', '零担运输体积。'),
      measure('ltl_unit_price', '零担单价', '零担运费除以零担体积。'),
      measure('ltl_share_rate', '零担占比', '零担体积除以总运输体积。'),
      measure('ltl_share_yoy_delta', '零担占比同比差', '零担占比相对同期的差值。'),
      measure('production_ratio_qoq_delta', '产比环比差', '运输产比相对上期的差值。'),
      measure('ltl_unit_price_delta', '零担单价差', '零担单价相对整车单价的差值。'),
      measure('warehouse_utilization_rate', '仓储利用率', '仓储占用面积除以可用面积。'),
      measure('warehouse_utilization_yoy_delta', '仓储利用率同比变动', '仓储利用率相对同期的差值。'),
      measure('inventory_amount', '库存金额', '报告期末库存金额。'),
      measure('inventory_turnover_days', '库存周转天数', '库存周转天数。'),
      measure('inventory_turnover_rate', '库存周转率', '库存周转率。'),
      measure('high_inventory_amount', '高库存金额', '超过目标库存的金额。'),
      measure('stagnant_inventory_amount', '呆滞库存金额', '超过呆滞阈值的库存金额。'),
      measure('stagnant_days', '呆滞天数', '物料连续无消耗天数。'),
      measure('stockout_rate', '缺货率', '缺货数量除以需求数量。'),
      measure('stockout_material_count', '缺货物料数', '发生缺货的物料数量。'),
      measure('replenishment_timely_rate', '补货及时率', '按期完成补货的比例。'),
      measure('risk_rank', '风险或 TopN 排名', '按潜在节约、零担占比、低利用率或库存风险生成的排名。'),
      measure('risk_level', '风险等级', '库存或物流风险等级数值。'),
    ],
  }),
]

export const operatingAnalysisDatasetSummaries = operatingAnalysisDatasetFixtures.map(item => item.summary)

/** 服务端同编码资产优先；尚未持久化时补入报告内置 ADS，保证普通目录可见。 */
export function mergeOperatingAnalysisDatasetSummaries(items: DatasetSummary[]) {
  const persistedCodes = new Set(items.map(item => item.code))
  return [
    ...operatingAnalysisDatasetSummaries.filter(item => !persistedCodes.has(item.code)),
    ...items,
  ]
}

export function operatingAnalysisDatasetByID(id: string) {
  return operatingAnalysisDatasetFixtures.find(item => item.summary.id === id)
}
