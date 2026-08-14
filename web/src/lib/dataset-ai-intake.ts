import type { AssetTable } from './datasets'

export type DatasetAIModelKind = 'DIM' | 'DWD' | 'DWS'

const modelKindTerms: Record<DatasetAIModelKind, string[]> = {
  DIM: ['维度表', '维表', '维度', '主数据', '档案表'],
  DWD: ['事实表', '明细表', '事实', '明细', '流水表', '交易明细'],
  DWS: ['聚合表', '汇总表', '聚合', '汇总', '统计表', '日报', '月报'],
}

const businessTerms = [
  '销售', '订单', '客户', '商品', '产品', '区域', '渠道', '库存', '支付', '履约', '门店',
  '供应商', '采购', '收入', '金额', '数量', '设备', '营销', '活动', '组织', '员工', '日期',
]

export function inferDatasetAIModelKind(requirement: string): DatasetAIModelKind | null {
  const normalized = requirement.trim().toLocaleLowerCase()
  if (!normalized) return null
  const matches = (Object.keys(modelKindTerms) as DatasetAIModelKind[])
    .filter(kind => modelKindTerms[kind].some(term => normalized.includes(term)))
  return matches.length === 1 ? matches[0] : null
}

function tableSearchText(table: AssetTable) {
  return [
    table.businessName, table.businessDescription, table.tableName, table.schemaName,
    table.sourceComment, ...(table.tags ?? []),
  ].filter(Boolean).join(' ').toLocaleLowerCase()
}

function requirementTerms(requirement: string) {
  const normalized = requirement.toLocaleLowerCase()
  const named = businessTerms.filter(term => normalized.includes(term))
  const identifiers = normalized.split(/[^a-z0-9_]+/).filter(term => term.length >= 3)
  return [...new Set([...named, ...identifiers])]
}

export function filterDatasetAICandidateTables(
  tables: AssetTable[],
  requirement: string,
  modelKind: DatasetAIModelKind,
  limit = 4,
) {
  const terms = requirementTerms(requirement)
  const ranked = tables.map((table, index) => {
    const text = tableSearchText(table)
    const semanticScore = terms.reduce((total, term) => total + (text.includes(term) ? term.length >= 3 ? 8 : 5 : 0), 0)
    let score = semanticScore
    const layer = table.datasetLayer
    if (modelKind === 'DWS' && (layer === 'DWD' || layer === 'DIM')) score += 6
    if (modelKind === 'DWD' && (!layer || layer === 'ODS')) score += 3
    if (modelKind === 'DIM' && (!layer || layer === 'ODS' || layer === 'DIM')) score += 3
    if (modelKind === 'DIM' && /(dim|master|customer|product|客户|商品|主数据|档案)/i.test(text)) score += 4
    if (modelKind === 'DWD' && /(fact|detail|order|trade|订单|交易|流水|明细)/i.test(text)) score += 4
    if (modelKind === 'DWS' && /(dwd|dim|daily|monthly|明细|维度|日报|月报)/i.test(text)) score += 3
    return { table, score, semanticScore, index }
  }).sort((left, right) => right.score - left.score || left.index - right.index)

  const relevant = ranked.filter(item => terms.length ? item.semanticScore > 0 : item.score > 0)
  return (relevant.length ? relevant : ranked).slice(0, Math.max(1, limit)).map(item => item.table)
}
