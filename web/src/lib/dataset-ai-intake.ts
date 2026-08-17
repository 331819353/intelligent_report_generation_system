import type { AssetTable } from './datasets'

export type DatasetAIModelKind = 'DIM' | 'DWD' | 'DWS' | 'ADS'

const modelKindTerms: Record<DatasetAIModelKind, string[]> = {
  DIM: ['维度表', '维表', '维度', '主数据', '档案表'],
  DWD: ['事实表', '明细表', '事实', '明细', '流水表', '交易明细'],
  DWS: ['聚合表', '汇总表', '聚合', '汇总', '统计表', '日报', '月报'],
  ADS: ['应用层', '报表层', '看板', '大屏', 'ads'],
}

const businessTerms = [
  '销售', '订单', '客户', '商品', '产品', '区域', '渠道', '库存', '支付', '履约', '门店',
  '供应商', '采购', '收入', '金额', '数量', '设备', '营销', '活动', '组织', '员工', '日期',
  '明细', '详情', '流水', '售后', '外卖', '退货', '退款',
]

const specializedDomains = [
  { terms: ['售后', 'after_sales', 'aftersales'], pattern: /(售后|after[_\s-]?sales)/i },
  { terms: ['外卖', 'takeout'], pattern: /(外卖|takeout)/i },
  { terms: ['退货', 'return'], pattern: /(退货|退单|sales[_\s-]?return)/i },
  { terms: ['退款', 'refund'], pattern: /(退款|refund)/i },
  { terms: ['维修', 'repair'], pattern: /(维修|repair)/i },
  { terms: ['测试', 'test'], pattern: /(测试|\btest\b)/i },
  { terms: ['历史', '归档', 'history', 'archive'], pattern: /(历史|归档|history|archive)/i },
]

export function inferDatasetAIModelKind(requirement: string): DatasetAIModelKind | null {
  const normalized = requirement.trim().toLocaleLowerCase()
  if (!normalized) return null
  const matches = (Object.keys(modelKindTerms) as DatasetAIModelKind[])
    .filter(kind => modelKindTerms[kind].some(term => normalized.includes(term)))
  return matches.length === 1 ? matches[0] : null
}

/** A modification clarification about missing source data should become a table picker, not an error card. */
export function datasetAIModificationNeedsTables(code: string | undefined, message: string | undefined) {
  if (code !== 'DATASET_AI_CLARIFICATION_REQUIRED') return false
  const normalized = (message ?? '').replace(/\s+/g, '')
  return /(数据表|数据集|哪张表|哪个表|来自哪|缺少.*数据|没有.*数据|补充.*表|选择.*表)/.test(normalized)
}

export type DatasetAIClarificationCandidate = {
  componentKind: string
  componentId: string
  componentName: string
}

/** A pending intent clarification: the modification is paused until the user answers. */
export type DatasetAIClarification = {
  question: string
  candidates: DatasetAIClarificationCandidate[]
}

/**
 * The table confirmation card only earns its interruption when the scope is actually
 * ambiguous. When the requirement explicitly names every retrieved candidate table,
 * the selection is already the user's own words, so the flow continues automatically
 * and reports which tables were confirmed and why.
 */
export function autoConfirmDatasetAITables(candidates: AssetTable[], requirement: string): { tables: AssetTable[]; reason: string } | null {
  if (!candidates.length) return null
  const normalized = requirement.toLocaleLowerCase()
  // Short generic fragments ("订单", "order") must not count as an explicit mention:
  // Chinese identity names need at least 3 characters, technical names at least 6.
  const explicitlyNamed = (name: string | undefined) => {
    const trimmed = (name ?? '').trim().toLocaleLowerCase()
    if (!trimmed) return false
    const minLength = /[一-鿿]/.test(trimmed) ? 3 : 6
    return trimmed.length >= minLength && normalized.includes(trimmed)
  }
  const named = candidates.every(table => explicitlyNamed(table.businessName) || explicitlyNamed(table.tableName))
  if (!named) return null
  return {
    tables: candidates,
    reason: '需求中已逐一指名这些数据表，无需再次确认',
  }
}

function tableSearchText(table: AssetTable) {
  return [
    table.businessName, table.businessDescription, table.tableName,
    table.sourceComment, ...(table.tags ?? []),
  ].filter(Boolean).join(' ').toLocaleLowerCase()
}

// A domain word in a description is often only background context. For example, the
// generic "销售订单明细事实表" currently says it records takeout orders in its description;
// that must not make it an "外卖专用表" and remove it from a generic order search. Restrict
// conflict detection to identity metadata that actually classifies the table.
function tableIdentityText(table: AssetTable) {
  return [table.businessName, table.tableName, ...(table.tags ?? [])]
    .filter(Boolean).join(' ').toLocaleLowerCase()
}

function requirementTerms(requirement: string) {
  const normalized = requirement.toLocaleLowerCase()
  const named = businessTerms.filter(term => normalized.includes(term))
  const identifiers = normalized.split(/[^a-z0-9_]+/).filter(term => term.length >= 3)
  const aliases = [
    ...(/订单/.test(normalized) ? ['order'] : []),
    ...(/销售/.test(normalized) ? ['sales'] : []),
    ...(/(?:详情|详细|明细)/.test(normalized) ? ['明细', 'detail', '订单行'] : []),
    ...(/(?:商品|产品)/.test(normalized) ? ['商品', '产品', 'product'] : []),
    ...(/客户/.test(normalized) ? ['customer'] : []),
  ]
  return [...new Set([...named, ...identifiers, ...aliases])]
}

function logicalTableKey(table: AssetTable) {
	const identity = (table.originTableId || table.id).trim().toLocaleLowerCase()
	if (identity) return `asset:${identity}`
  const technical = table.tableName.toLocaleLowerCase().replace(/[^a-z0-9\u4e00-\u9fff]/g, '')
  const business = (table.businessName || '').toLocaleLowerCase()
    .replace(/(?:事实表|明细表|维度表|数据表|映射表|表)$/g, '')
    .replace(/[^a-z0-9\u4e00-\u9fff]/g, '')
	return `name:${technical || business}`
}

type CandidateFilterOptions = {
  context?: string
  excludeTableIDs?: Iterable<string>
}

export function filterDatasetAICandidateTables(
  tables: AssetTable[],
  requirement: string,
  modelKind: DatasetAIModelKind,
  limit = 4,
  options: CandidateFilterOptions = {},
) {
  const normalizedRequirement = requirement.toLocaleLowerCase().replace(/\s+/g, '')
  const terms = requirementTerms(requirement)
  const contextTerms = requirementTerms(options.context ?? '')
  const excludedTableIDs = new Set(options.excludeTableIDs ?? [])
  const orderIntent = /(订单|order)/i.test(normalizedRequirement)
  const detailIntent = /(详情|详细|明细|detail)/i.test(normalizedRequirement)
  const salesIntent = /(销售|sales)/i.test(normalizedRequirement)
  const ranked = tables.map((table, index) => {
    const text = tableSearchText(table)
    const identityText = tableIdentityText(table)
    let semanticScore = terms.reduce((total, term) => total + (text.includes(term) ? term.length >= 3 ? 8 : 5 : 0), 0)
    if (orderIntent && /(订单|order)/i.test(text)) semanticScore += 14
    if (detailIntent && /(明细|详情|订单行|流水|detail|line|fact)/i.test(text)) semanticScore += 12
    if (detailIntent && /(订单行|行项目|明细项|order[_\s-]?(?:item|line)|sales[_\s-]?order[_\s-]?(?:item|line))/i.test(text)) semanticScore += 8
    if (salesIntent && /(销售|sales)/i.test(text)) semanticScore += 8
    const contextScore = contextTerms.reduce((total, term) => total + (text.includes(term) ? 2 : 0), 0)
    const domainConflicts = specializedDomains.filter(domain => domain.pattern.test(identityText) && !domain.terms.some(term => normalizedRequirement.includes(term))).length
    let score = semanticScore
    score += contextScore
    const layer = table.datasetLayer
    if (modelKind === 'DWS' && (layer === 'DWD' || layer === 'DIM')) score += 6
    if (modelKind === 'ADS' && (layer === 'DWS' || layer === 'DIM')) score += 6
    if (modelKind === 'DWD' && (!layer || layer === 'ODS')) score += 3
    if (modelKind === 'DIM' && (!layer || layer === 'ODS' || layer === 'DIM')) score += 3
    if (modelKind === 'DIM' && /(dim|master|customer|product|客户|商品|主数据|档案)/i.test(text)) score += 4
    if (modelKind === 'DWD' && /(fact|detail|order|trade|订单|交易|流水|明细)/i.test(text)) score += 4
    if (modelKind === 'DWS' && /(dwd|dim|daily|monthly|明细|维度|日报|月报)/i.test(text)) score += 3
    score -= domainConflicts * 40
    return { table, score, semanticScore, domainConflicts, index }
  }).sort((left, right) => right.score - left.score || left.index - right.index)

  const relevant = ranked.filter(item => !excludedTableIDs.has(item.table.id) && (terms.length ? item.semanticScore > 0 : item.score > 0))
  const domainMatched = relevant.filter(item => item.domainConflicts === 0)
  const pool = domainMatched.length
    ? domainMatched
    : relevant.some(item => item.domainConflicts > 0)
      ? []
      : relevant.length
        ? relevant
        : terms.length
          ? []
          : ranked.filter(item => !excludedTableIDs.has(item.table.id))
  const seen = new Set<string>()
  return pool.filter(item => {
    const key = logicalTableKey(item.table)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  }).slice(0, Math.max(1, limit)).map(item => item.table)
}

/**
 * Split confirmed tables into the PRIMARY_SOURCE and DIMENSION_SOURCE workflow roles.
 * Published modeled layers are authoritative. Raw ODS tables must still be allowed to
 * declare their role through governed tags/names; checking ODS before that metadata used
 * to misclassify every raw dimension as a primary source.
 * A blueprint may still re-pair tables, but the role tells the server which side of a
 * join is the entity/fact and which is the lookup.
 */
export function inferDatasetAITableRole(table: AssetTable, modelKind: DatasetAIModelKind): 'PRIMARY' | 'DIMENSION' {
  if (modelKind === 'DIM') return 'PRIMARY'
  if (table.datasetLayer === 'DIM') return 'DIMENSION'
  const identity = tableIdentityText(table)
  if (table.datasetLayer === 'DWD' || table.datasetLayer === 'DWS' || table.datasetLayer === 'ADS') return 'PRIMARY'
  if (/(^|[^a-z])dim[_\s-]|维表|维度|主数据|档案|role\s*[:：]\s*dimension|作用\s*[:：]\s*(?:维度表|主数据)/.test(identity)) return 'DIMENSION'
  return 'PRIMARY'
}
