import {
  ArrowLeftIcon,
  ArrowRightIcon,
  BroomIcon,
  CheckCircleIcon,
  CheckIcon,
  CirclesThreeIcon,
  DatabaseIcon,
  GitBranchIcon,
  InfoIcon,
  LightbulbIcon,
  MagicWandIcon,
  MagnifyingGlassIcon,
  RowsIcon,
  ShieldCheckIcon,
  SparkleIcon,
  SpinnerGapIcon,
  StackIcon,
  TableIcon,
  TreeStructureIcon,
  WarningCircleIcon,
  XIcon,
  type Icon,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { datasetAPI, type DatasetLLMTrigger, type DatasetPreview, type DatasetSummary } from '../../lib/datasets'
import '../../styles/intelligent-modeling-advisor.css'

type ModelingKind = 'DIMENSION' | 'DETAIL' | 'SUBJECT' | 'APPLICATION'
type ModelingStep = 1 | 2 | 3 | 4 | 5
type CandidateMethod = 'DIRECT' | 'EXTRACTED'

type ModelingKindConfig = {
  id: ModelingKind
  trigger?: DatasetLLMTrigger
  label: string
  shortLabel: string
  sourceLayers: DatasetSummary['layer'][]
  targetLayer: DatasetSummary['layer']
  icon: Icon
  description: string
  outcome: string
  subjectLabel: string
  accent: string
}

type ModelCandidate = {
  id: string
  datasetID: string
  semanticKey: string
  semanticLabel: string
  tableCode: string
  method: CandidateMethod
  functionDescription: string
  fields: string[]
  confidence: number
}

type DatasetFinding = {
  dataset: DatasetSummary
  direct: boolean
  conclusion: string
  evidence: string
  candidates: ModelCandidate[]
}

type CandidateComparison = {
  id: string
  candidateIDs: string[]
  relation: 'CONTAINMENT' | 'DUPLICATE' | 'ANOMALY' | 'INDEPENDENT'
  title: string
  detail: string
  needsHuman: boolean
  keepID?: string
}

type AdvisorQuestion = {
  id: string
  title: string
  reason: string
  candidateIDs: string[]
  options: Array<{ value: string; label: string; description: string; recommended?: boolean }>
}

type DecisionMessage = {
  id: string
  role: 'ASSISTANT' | 'USER'
  content: string
  round: number
}

type DecisionThread = {
  messages: DecisionMessage[]
  draft: string
  suggestion?: string
  locked: boolean
}

type DatasetPreviewState = {
  dataset: DatasetSummary
  loading: boolean
  data?: DatasetPreview
  error?: string
}

type CleaningRule = {
  tableCode: string
  field: string
  issue: string
  defaultValue: string
  rule: string
  result: string
}

type DefaultPolicy = {
  type: string
  defaultValue: string
  storage: string
  protection: string
}

const defaultPolicies: DefaultPolicy[] = [
  { type: '关联键 / 主外键', defaultValue: '禁止自动填充', storage: '保留原始键；缺失键进入拒绝记录', protection: '不默认 TRIM 或转大小写；规范键须通过碰撞、覆盖率和扇出校验后人工启用' },
  { type: 'DATE', defaultValue: '0000-00-00', storage: '原始层保留零日期哨兵；强类型 DATE 写 NULL', protection: '增加 is_defaulted 与 raw_date，避免数据库不支持零日期时写入失败' },
  { type: 'TIMESTAMP / DATETIME', defaultValue: '0000-00-00 00:00:00', storage: '原始层保留哨兵；强类型时间写 NULL', protection: '保留来源时区和原值，不把缺失时间误认为真实时点' },
  { type: 'TIME', defaultValue: '00:00:00', storage: '写入标准 TIME，同时标记为缺省值', protection: '增加 is_defaulted，避免午夜业务时间与缺失值混淆' },
  { type: 'INTEGER / DECIMAL', defaultValue: '0', storage: '标准数值写 0，原始空值单独保留', protection: '增加 is_defaulted，汇总时可选择是否排除缺省补零' },
  { type: 'STRING / TEXT', defaultValue: 'NULL', storage: '使用真正的 SQL NULL，不写字符串“NULL”', protection: '保留 raw_value；仅实际缺失或空字符串使用缺省值' },
  { type: 'BOOLEAN', defaultValue: 'NULL', storage: '未知状态保持 SQL NULL', protection: '不默认写 false，避免把未知误判为否' },
  { type: 'ENUM / CODE', defaultValue: 'NULL', storage: '未识别值保留原码并写 NULL 标准码', protection: '不自动创造 UNKNOWN 枚举，待字典确认后再映射' },
  { type: 'UUID', defaultValue: 'NULL', storage: '非法或缺失 UUID 写 NULL 并隔离原值', protection: '不自动生成 UUID，避免制造无法追溯的实体关系' },
  { type: 'JSON / ARRAY / BINARY', defaultValue: 'NULL', storage: '缺失值保持 SQL NULL', protection: '不使用 {}、[] 或空字节替代未知，保留“缺失”与“空集合”的区别' },
]

const modelingKinds: ModelingKindConfig[] = [
  {
    id: 'DIMENSION', trigger: 'DIM_MODELING', label: '维度建模', shortLabel: '统一业务实体',
    sourceLayers: ['ODS'], targetLayer: 'DIM', icon: CirclesThreeIcon, accent: 'violet',
    description: '逐个判断 ODS 是否已经表达业务实体；否则提取可复用的客户、商品、组织等维度信息。',
    outcome: '产出经功能归并、清洗后的可落地维度表', subjectLabel: '维度信息',
  },
  {
    id: 'DETAIL', trigger: 'DWD_MODELING', label: '明细建模', shortLabel: '沉淀原子业务事实',
    sourceLayers: ['ODS'], targetLayer: 'DWD', icon: RowsIcon, accent: 'blue',
    description: '逐个判断 ODS 是否已经表达原子业务动作；否则提取订单、支付、履约等最细事实。',
    outcome: '产出经粒度归并、维度关联和清洗后的明细表', subjectLabel: '明细事实',
  },
  {
    id: 'SUBJECT', trigger: 'DWS_MODELING', label: '主题建模', shortLabel: '组织可复用分析主题',
    sourceLayers: ['DWD'], targetLayer: 'DWS', icon: StackIcon, accent: 'cyan',
    description: '逐个判断 DWD 能支撑哪些分析主题，比较主题功能与指标口径后形成公共汇总模型。',
    outcome: '产出经主题归并、指标清洗后的可复用汇总表', subjectLabel: '主题模型',
  },
  {
    id: 'APPLICATION', label: '应用建模', shortLabel: '交付具体业务场景',
    sourceLayers: ['DWS'], targetLayer: 'ADS', icon: TableIcon, accent: 'orange',
    description: '逐个判断 DWS 能支撑哪些看板、报告或接口场景，消除重复应用口径并形成交付方案。',
    outcome: '产出面向场景的 ADS 表、服务口径与刷新方案', subjectLabel: '应用模型',
  },
]

const stepLabels = ['选择类型', '选择数据集', '证据与会审', '落地方案', '提交确认']

const layerGuidance = [
  { layer: 'ODS', label: '贴源层', models: '维度建模 · 明细建模', note: '识别实体与原子事实' },
  { layer: 'DIM', label: '维度层', models: '公共维度上下文', note: '供明细与主题模型关联' },
  { layer: 'DWD', label: '明细层', models: '主题建模', note: '归并原子事实与分析口径' },
  { layer: 'DWS', label: '汇总层', models: '应用建模', note: '面向具体消费场景交付 ADS' },
]

const statusLabels: Record<string, string> = {
  PUBLISHED: '已发布', DRAFT: '草稿', VALIDATING: '校验中', STALE: '已失效',
  DEPRECATED: '已废弃', DISABLED: '已停用',
}

const semanticCatalog: Record<ModelingKind, Array<{
  key: string
  label: string
  hints: string[]
  fn: string
  fields: string[]
}>> = {
  DIMENSION: [
    { key: 'customer', label: '客户', hints: ['客户', 'customer', '会员', 'member'], fn: '统一客户身份、区域、等级与生命周期属性，供跨系统客户分析复用。', fields: ['customer_id', 'customer_code', 'customer_name', 'region_code', 'customer_level'] },
    { key: 'product', label: '商品', hints: ['商品', 'product', 'sku', '物料'], fn: '统一商品编码、名称、品类与规格属性，支撑商品经营分析。', fields: ['product_id', 'product_code', 'product_name', 'category_code', 'specification'] },
    { key: 'channel', label: '渠道', hints: ['渠道', 'channel', '平台'], fn: '统一渠道、平台和来源分类，支撑渠道归因与经营对比。', fields: ['channel_id', 'channel_code', 'channel_name', 'channel_type', 'platform_code'] },
    { key: 'store', label: '门店', hints: ['门店', 'store', '店铺'], fn: '统一门店身份、组织归属、区域和营业状态。', fields: ['store_id', 'store_code', 'store_name', 'region_code', 'store_status'] },
    { key: 'warehouse', label: '仓库', hints: ['库存', '仓库', 'inventory', 'warehouse'], fn: '统一仓库、库区与组织归属，支撑库存和履约分析。', fields: ['warehouse_id', 'warehouse_code', 'warehouse_name', 'region_code', 'warehouse_type'] },
  ],
  DETAIL: [
    { key: 'order_line', label: '订单行', hints: ['订单', 'order', '销售', 'sales'], fn: '按订单明细行记录商品、客户、数量、金额和履约状态，保持原子业务粒度。', fields: ['order_id', 'line_no', 'customer_id', 'product_id', 'paid_amount'] },
    { key: 'inventory_snapshot', label: '库存快照', hints: ['库存', 'inventory', '仓库'], fn: '按商品、仓库和快照时点记录可用量、占用量与在途量。', fields: ['snapshot_time', 'warehouse_id', 'product_id', 'available_qty', 'reserved_qty'] },
    { key: 'payment_event', label: '支付事件', hints: ['支付', 'payment', '收款'], fn: '按支付事件记录订单、渠道、金额、币种与支付状态。', fields: ['payment_id', 'order_id', 'payment_time', 'paid_amount', 'payment_status'] },
  ],
  SUBJECT: [
    { key: 'sales_operation', label: '销售经营', hints: ['订单', '销售', 'sales', '交易'], fn: '按统一时间与公共维度汇总销售额、销量、订单数、折扣和履约指标。', fields: ['business_date', 'channel_id', 'region_id', 'sales_amount', 'order_count'] },
    { key: 'inventory_operation', label: '库存运营', hints: ['库存', 'inventory', '仓库'], fn: '汇总库存余额、周转、缺货和补货指标，支撑库存运营分析。', fields: ['business_date', 'warehouse_id', 'category_id', 'stock_qty', 'turnover_days'] },
    { key: 'customer_operation', label: '客户运营', hints: ['客户', 'customer', '会员'], fn: '汇总客户新增、活跃、复购和价值分层指标。', fields: ['business_date', 'customer_level', 'active_customer_count', 'repeat_rate', 'customer_value'] },
  ],
  APPLICATION: [
    { key: 'channel_dashboard', label: '渠道经营看板', hints: ['渠道', 'channel', '销售'], fn: '为渠道经营看板和日报提供稳定的筛选维度、核心指标与下钻入口。', fields: ['report_date', 'channel_name', 'region_name', 'sales_amount', 'target_rate'] },
    { key: 'executive_dashboard', label: '经营驾驶舱', hints: ['经营', 'executive', '管理', '汇总'], fn: '为管理层驾驶舱提供收入、毛利、订单与运营健康度指标。', fields: ['report_date', 'org_name', 'revenue', 'gross_profit', 'operation_score'] },
    { key: 'inventory_alert', label: '库存预警应用', hints: ['库存', 'inventory', '缺货'], fn: '为库存预警页面和消息接口提供缺货、积压与补货建议。', fields: ['report_date', 'warehouse_name', 'product_name', 'risk_level', 'suggested_replenishment'] },
  ],
}

const prefixes: Record<ModelingKind, string> = {
  DIMENSION: 'dim', DETAIL: 'dwd', SUBJECT: 'dws', APPLICATION: 'ads',
}

function datasetReady(dataset: DatasetSummary) {
  return dataset.status === 'PUBLISHED' && Boolean(dataset.currentPublishedVersionId)
}

function displayDate(value: string) {
  return new Date(value).toLocaleString('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  })
}

function datasetText(dataset: DatasetSummary) {
  return [dataset.name, dataset.code, dataset.description, ...dataset.tags].join(' ').toLocaleLowerCase()
}

function isDirectModel(kind: ModelingKind, dataset: DatasetSummary) {
  const text = datasetText(dataset)
  if (kind === 'DIMENSION') return ['主数据', '维度', '档案', 'master'].some(hint => text.includes(hint))
  if (kind === 'DETAIL') return ['明细', '流水', '事件', 'detail', 'event'].some(hint => text.includes(hint))
  if (kind === 'SUBJECT') return ['主题', '汇总', 'summary', 'aggregate'].some(hint => text.includes(hint))
  return ['看板', '报表', '应用', '接口', 'dashboard', 'report'].some(hint => text.includes(hint))
}

function candidateCatalog(kind: ModelingKind, dataset: DatasetSummary) {
  const text = datasetText(dataset)
  const matches = semanticCatalog[kind].filter(item => item.hints.some(hint => text.includes(hint)))
  if (kind === 'DIMENSION' && /订单|order|销售|sales/.test(text)) {
    for (const key of ['customer', 'product', 'channel']) {
      const item = semanticCatalog.DIMENSION.find(candidate => candidate.key === key)
      if (item && !matches.some(match => match.key === key)) matches.push(item)
    }
  }
  return matches
}

function buildFindings(kind: ModelingKind, selectedDatasets: DatasetSummary[]): DatasetFinding[] {
  return selectedDatasets.map(dataset => {
    const catalog = candidateCatalog(kind, dataset)
    const direct = isDirectModel(kind, dataset) && catalog.length > 0
    const candidates = (direct ? catalog.slice(0, 1) : catalog).map((semantic, index) => ({
      id: `${dataset.id}:${semantic.key}`,
      datasetID: dataset.id,
      semanticKey: semantic.key,
      semanticLabel: semantic.label,
      tableCode: `${prefixes[kind]}_${semantic.key}`,
      method: direct ? 'DIRECT' as const : 'EXTRACTED' as const,
      functionDescription: semantic.fn,
      fields: semantic.fields,
      confidence: Math.max(.72, (direct ? .95 : .86) - index * .03),
    }))
    return {
      dataset,
      direct,
      candidates,
      conclusion: !candidates.length
        ? `该数据集既不是${modelingKinds.find(item => item.id === kind)?.subjectLabel}，现有元信息也不足以提取可落地候选，本次排除。`
        : direct
        ? `该数据集已具备${modelingKinds.find(item => item.id === kind)?.subjectLabel}特征，可直接形成候选模型。`
        : `该数据集不是完整的目标模型，但可提取 ${candidates.length} 组${modelingKinds.find(item => item.id === kind)?.subjectLabel}。`,
      evidence: `依据名称、说明、标签、来源层级和发布版本判断；字段级结果需在落地前以真实结构复核。`,
    }
  })
}

function buildComparisons(candidates: ModelCandidate[]): CandidateComparison[] {
  const groups = new Map<string, ModelCandidate[]>()
  for (const candidate of candidates) groups.set(candidate.semanticKey, [...(groups.get(candidate.semanticKey) ?? []), candidate])
  const comparisons: CandidateComparison[] = []
  for (const group of groups.values()) {
    if (group.length === 1) continue
    const sorted = [...group].sort((left, right) => right.confidence - left.confidence)
    const [best, second] = sorted
    const decisive = best.method === 'DIRECT' && second.method !== 'DIRECT' || best.confidence - second.confidence >= .07
    comparisons.push({
      id: `compare:${best.semanticKey}`,
      candidateIDs: sorted.map(candidate => candidate.id),
      relation: decisive ? 'CONTAINMENT' : 'DUPLICATE',
      title: `${best.semanticLabel}功能${decisive ? '存在包含关系' : '高度重叠'}`,
      detail: decisive
        ? `${best.tableCode} 的功能和字段覆盖更完整，系统保留它并把其他候选作为来源映射，不重复落表。`
        : `多个候选的功能、关键字段和置信度接近，仅凭元信息无法可靠决定保留哪一个。`,
      needsHuman: !decisive,
      keepID: decisive ? best.id : undefined,
    })
  }
  return comparisons
}

function buildQuestions(comparisons: CandidateComparison[], candidates: ModelCandidate[], datasets: DatasetSummary[]): AdvisorQuestion[] {
  return comparisons.filter(comparison => comparison.needsHuman).map(comparison => {
    const related = comparison.candidateIDs.map(id => candidates.find(candidate => candidate.id === id)).filter(Boolean) as ModelCandidate[]
    return {
      id: comparison.id,
      title: `${related[0]?.semanticLabel ?? '候选模型'}应保留哪个实现？`,
      reason: `${comparison.detail} 请结合主系统归属、数据完整率和下游使用习惯裁决。`,
      candidateIDs: comparison.candidateIDs,
      options: [
        ...related.slice(0, 2).map(candidate => ({
          value: candidate.id,
          label: candidate.tableCode,
          description: `保留来源“${datasets.find(dataset => dataset.id === candidate.datasetID)?.name ?? '未知'}”，其余只保留映射。`,
        })),
        { value: `merge:${comparison.id}`, label: '合并为统一模型', description: '两边字段均保留，增加来源优先级与冲突记录。', recommended: true },
      ],
    }
  })
}

function relatedQuestionCandidates(question: AdvisorQuestion, candidates: ModelCandidate[]) {
  return question.candidateIDs.map(id => candidates.find(candidate => candidate.id === id)).filter(Boolean) as ModelCandidate[]
}

function initialDecisionThread(question: AdvisorQuestion, candidates: ModelCandidate[], datasets: DatasetSummary[]): DecisionThread {
  const evidence = relatedQuestionCandidates(question, candidates).map(candidate => {
    const dataset = datasets.find(item => item.id === candidate.datasetID)
    return `${dataset?.name ?? candidate.tableCode}（${Math.round(candidate.confidence * 100)}%，${candidate.method === 'DIRECT' ? '直接模型' : '提取候选'}）`
  }).join('、')
  return {
    draft: '',
    locked: false,
    messages: [{
      id: `${question.id}:assistant:0`,
      role: 'ASSISTANT',
      round: 0,
      content: `当前证据涉及 ${evidence}。元信息仍缺少权威来源、主键唯一率、字段完整率、更新时效和下游依赖。请先查看样例数据，再补充任一业务事实；我会逐轮更新判断，不会仅凭默认选项直接定案。`,
    }],
  }
}

function decisionOptionLabel(question: AdvisorQuestion, value?: string) {
  return question.options.find(option => option.value === value)?.label ?? '尚未形成唯一建议'
}

function refineDecision(
  question: AdvisorQuestion,
  candidates: ModelCandidate[],
  datasets: DatasetSummary[],
  messages: DecisionMessage[],
  input: string,
  previousSuggestion?: string,
) {
  const related = relatedQuestionCandidates(question, candidates)
  const allEvidence = [...messages.filter(message => message.role === 'USER').map(message => message.content), input].join(' ').toLocaleLowerCase()
  const mergeOption = question.options.find(option => option.value.startsWith('merge:'))
  let suggestion = previousSuggestion
  let rationale = ''

  if (/合并|统一模型|都保留|字段互补|双来源/.test(allEvidence) && mergeOption) {
    suggestion = mergeOption.value
    rationale = '你说明了多来源需要同时保留或字段互补'
  } else {
    const matched = related.filter(candidate => {
      const dataset = datasets.find(item => item.id === candidate.datasetID)
      const tokens = [dataset?.name, dataset?.code, dataset?.originDataSourceName, dataset?.originTableName]
        .filter((value): value is string => Boolean(value && value.length >= 2))
      return tokens.some(token => allEvidence.includes(token.toLocaleLowerCase()))
    })
    if (matched.length === 1) {
      suggestion = matched[0].id
      const dataset = datasets.find(item => item.id === matched[0].datasetID)
      rationale = `你明确指向了“${dataset?.name ?? matched[0].tableCode}”及其来源`
    } else if (matched.length > 1 && mergeOption) {
      suggestion = mergeOption.value
      rationale = '补充信息同时指向多个候选，当前更适合保留来源优先级并记录冲突'
    }
  }

  const confirmedDimensions = [
    /权威|主系统|主数据|source of truth|唯一来源/.test(allEvidence) && '权威来源',
    /唯一率|重复率|主键|去重/.test(allEvidence) && '键质量',
    /完整率|空值率|覆盖率|缺失率/.test(allEvidence) && '字段完整性',
    /时效|延迟|更新频率|刷新/.test(allEvidence) && '更新时效',
    /下游|报表|看板|接口|依赖/.test(allEvidence) && '下游依赖',
  ].filter(Boolean) as string[]
  const missing = ['权威来源', '键质量', '字段完整性', '更新时效', '下游依赖'].filter(item => !confirmedDimensions.includes(item))
  const excerpt = input.trim().replace(/\s+/g, ' ').slice(0, 72)
  const content = suggestion
    ? `已记录“${excerpt}${input.trim().length > 72 ? '…' : ''}”。${rationale || '这条事实补强了上一轮判断'}，当前建议更新为「${decisionOptionLabel(question, suggestion)}」。${missing.length ? `仍建议补充 ${missing.slice(0, 2).join('、')}；你也可以继续查看样例或在证据充分后锁定结论。` : '五类关键依据已覆盖，可以选择最终方案并锁定结论。'}`
    : `已记录“${excerpt}${input.trim().length > 72 ? '…' : ''}”，但尚不能把这条信息唯一映射到某个候选。请明确哪个系统是权威来源，或补充主键唯一率、字段完整率、更新时效中的一项；我会在下一轮重新判断。`
  return { suggestion, content }
}

function snapshotPreview(dataset: DatasetSummary): DatasetPreview {
  if (/customer|客户|会员|crm/i.test(datasetText(dataset))) return {
    queryId: `preview:${dataset.id}`,
    columns: ['customer_id', 'customer_code', 'customer_name', 'region_code', 'customer_level', 'updated_at'],
    rows: [
      ['C000182', 'crm-000182', '海风商贸', 'CN-31', 'A', '2026-08-11 09:52:18'],
      ['C000207', 'CRM-000207', '云帆零售', 'CN-44', 'B', '2026-08-11 09:48:03'],
      ['C000233', null, '星河供应链', 'CN-33', 'A', '2026-08-11 09:41:56'],
      ['c000241', 'crm-000241', '远山生活', 'CN-11', 'C', '2026-08-11 09:35:29'],
      ['C000255', 'CRM-000255', null, 'CN-51', 'B', '2026-08-11 09:31:12'],
    ],
    rowCount: 5,
    durationMs: 96,
  }
  if (/inventory|库存|仓库/i.test(datasetText(dataset))) return {
    queryId: `preview:${dataset.id}`,
    columns: ['snapshot_time', 'warehouse_id', 'product_id', 'available_qty', 'reserved_qty', 'source_status'],
    rows: [
      ['2026-08-11 10:00:00', 'WH-E01', 'SKU-82031', 182, 12, 'VALID'],
      ['2026-08-11 10:00:00', 'wh-e01', 'SKU-82044', 0, 6, 'VALID'],
      ['2026-08-11 10:00:00', 'WH-S02', 'SKU-82067', null, 0, 'PENDING'],
      ['2026-08-11 10:00:00', 'WH-N03', 'SKU-82102', 47, 3, 'VALID'],
    ],
    rowCount: 4,
    durationMs: 103,
  }
  if (/summary|汇总|channel|渠道/i.test(datasetText(dataset))) return {
    queryId: `preview:${dataset.id}`,
    columns: ['business_date', 'channel_code', 'region_code', 'sales_amount', 'order_count', 'refreshed_at'],
    rows: [
      ['2026-08-10', 'ONLINE', 'CN-EAST', 428650.52, 1382, '2026-08-11 02:10:00'],
      ['2026-08-10', 'STORE', 'CN-SOUTH', 316208.00, 927, '2026-08-11 02:10:00'],
      ['2026-08-10', 'PARTNER', 'CN-NORTH', 187420.35, 466, '2026-08-11 02:10:00'],
    ],
    rowCount: 3,
    durationMs: 88,
  }
  return {
    queryId: `preview:${dataset.id}`,
    columns: ['order_id', 'line_no', 'customer_id', 'product_id', 'channel_code', 'paid_amount', 'status', 'created_at'],
    rows: [
      ['SO2026081100182', 1, 'C000182', 'SKU-82031', 'ONLINE', 399.00, 'PAID', '2026-08-11 09:51:26'],
      ['SO2026081100182', 2, 'c000182', 'SKU-82044', 'ONLINE', 129.90, 'PAID', '2026-08-11 09:51:26'],
      ['SO2026081100207', 1, 'C000207', 'SKU-82067', 'STORE', 0, 'CANCELLED', '2026-08-11 09:46:03'],
      ['SO2026081100233', 1, null, 'SKU-82102', 'PARTNER', 869.00, 'SHIPPED', '2026-08-11 09:39:47'],
      ['SO2026081100241', 1, 'C000241', 'SKU-82118', 'ONLINE', 219.50, 'PAID', '2026-08-11 09:32:16'],
    ],
    rowCount: 5,
    durationMs: 112,
  }
}

function resolveCandidates(candidates: ModelCandidate[], comparisons: CandidateComparison[], questions: AdvisorQuestion[], answers: Record<string, string>) {
  const removed = new Set<string>()
  for (const comparison of comparisons) {
    const answer = answers[comparison.id]
    const keepID = comparison.keepID ?? (answer?.startsWith('merge:') ? comparison.candidateIDs[0] : answer)
    if (!keepID) continue
    for (const id of comparison.candidateIDs) if (id !== keepID) removed.add(id)
  }
  return candidates.filter(candidate => !removed.has(candidate.id)).map(candidate => {
    const merged = questions.some(question => answers[question.id]?.startsWith('merge:') && question.candidateIDs.includes(candidate.id))
    return merged ? { ...candidate, tableCode: `${candidate.tableCode}_unified` } : candidate
  }).sort((left, right) => right.confidence - left.confidence)
}

function cleaningRuleFor(field: string, tableCode: string): CleaningRule {
  if (/(^|_)(id|key|code|line_no)$/.test(field)) return {
    tableCode,
    field,
    issue: '缺失、重复、格式漂移或大小写差异',
    defaultValue: '禁止填充',
    rule: '保留原始键，不默认 TRIM 或转大小写；仅在键碰撞为 0、关联覆盖率不下降、扇出不增加并经人工确认后生成 normalized_key',
    result: 'raw_key + 可选 normalized_key + 影响评估',
  }
  if (/(^|_)(date|time|from|to)$/.test(field)) {
    const timestamp = /(^|_)(time|from|to)$/.test(field)
    return {
      tableCode,
      field,
      issue: '格式、时区、非法日期或缺失值',
      defaultValue: timestamp ? '0000-00-00 00:00:00' : '0000-00-00',
      rule: '合法值按来源时区解析；原始层保留零日期哨兵；目标强类型字段写 NULL，并记录 raw_value 与 is_defaulted',
      result: '标准时间 + 原始值 + 缺省标记',
    }
  }
  if (/amount|qty|count|rate|score|revenue|profit|value|days|number|total|price|cost|replenishment/.test(field)) return {
    tableCode,
    field,
    issue: '精度、单位、正负号或缺失值可能不一致',
    defaultValue: '0',
    rule: '统一目标数值类型与单位；缺失值补 0，同时保留 raw_value 并写入 is_defaulted，避免与真实 0 混淆',
    result: '标准数值 + 原始值 + 缺省标记',
  }
  if (/status|level|type|category/.test(field)) return {
    tableCode,
    field,
    issue: '空值、别名与枚举漂移',
    defaultValue: 'NULL',
    rule: '非空原码按受控字典映射；缺失或未识别值写 SQL NULL，原码保留并进入字典待确认清单',
    result: '标准码 + 原始码 + 映射状态',
  }
  return {
    tableCode,
    field,
    issue: '首尾空格、空值或字符编码差异',
    defaultValue: 'NULL',
    rule: '保留原始文本；清洗副本可去除首尾空格；仅实际缺失或空字符串写 SQL NULL，不写字符串“NULL”',
    result: '清洗文本 + 原始文本 + 缺省标记',
  }
}

function buildCleaningRules(candidates: ModelCandidate[]) {
  return candidates.flatMap(candidate => candidate.fields.map(field => cleaningRuleFor(field, candidate.tableCode)))
}

function outputRole(kind: ModelingKind, index: number) {
  if (index === 0) return kind === 'APPLICATION' ? '首要消费场景' : '首要可落地模型'
  return '独立业务功能模型'
}

export function IntelligentModelingAdvisor({
  open, datasets, initialSelectedDatasetIDs, busy, activeModelingLabels, onClose, onSubmit,
}: {
  open: boolean
  datasets: DatasetSummary[]
  initialSelectedDatasetIDs: string[]
  busy: boolean
  activeModelingLabels: string[]
  onClose: () => void
  onSubmit: (trigger: DatasetLLMTrigger | undefined, label: string, datasetIDs: string[]) => Promise<boolean>
}) {
  const [step, setStep] = useState<ModelingStep>(1)
  const [kind, setKind] = useState<ModelingKind>('DIMENSION')
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(() => new Set(initialSelectedDatasetIDs))
  const [keyword, setKeyword] = useState('')
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [decisionThreads, setDecisionThreads] = useState<Record<string, DecisionThread>>({})
  const [preview, setPreview] = useState<DatasetPreviewState | null>(null)
  const [supplement, setSupplement] = useState('')
  const [analysisStage, setAnalysisStage] = useState(0)
  const [submitting, setSubmitting] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [confirmations, setConfirmations] = useState({ decisions: false, fields: false, draft: false })
  const analysisTimers = useRef<number[]>([])
  const bodyRef = useRef<HTMLDivElement>(null)

  const config = modelingKinds.find(item => item.id === kind) ?? modelingKinds[0]
  const eligible = useMemo(() => datasets.filter(dataset => config.sourceLayers.includes(dataset.layer)), [config.sourceLayers, datasets])
  const visibleDatasets = useMemo(() => {
    const query = keyword.trim().toLocaleLowerCase()
    return eligible.filter(dataset => !query || [dataset.name, dataset.code, dataset.description, dataset.originDataSourceName ?? '']
      .some(value => value.toLocaleLowerCase().includes(query)))
  }, [eligible, keyword])
  const selectedDatasets = useMemo(() => eligible.filter(dataset => selectedIDs.has(dataset.id)), [eligible, selectedIDs])
  const findings = useMemo(() => buildFindings(kind, selectedDatasets), [kind, selectedDatasets])
  const candidates = useMemo(() => findings.flatMap(finding => finding.candidates), [findings])
  const comparisons = useMemo(() => buildComparisons(candidates), [candidates])
  const questions = useMemo(() => buildQuestions(comparisons, candidates, selectedDatasets), [candidates, comparisons, selectedDatasets])
  const resolvedCandidates = useMemo(() => resolveCandidates(candidates, comparisons, questions, answers), [answers, candidates, comparisons, questions])
  const cleaningRules = useMemo(() => buildCleaningRules(resolvedCandidates), [resolvedCandidates])
  const answeredCount = questions.filter(question => answers[question.id] && decisionThreads[question.id]?.locked).length
  const canContinueAnalysis = analysisStage >= 4 && candidates.length > 0 && answeredCount === questions.length
  const canSubmit = confirmations.decisions && confirmations.fields && confirmations.draft
  const autoResolvedCount = comparisons.filter(comparison => !comparison.needsHuman).length

  useEffect(() => () => analysisTimers.current.forEach(timer => window.clearTimeout(timer)), [])

  useEffect(() => {
    if (open) bodyRef.current?.scrollTo({ top: 0 })
  }, [open, step])

  useEffect(() => {
    if (!open) return undefined
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      if (preview) setPreview(null)
      else if (!submitting) onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose, open, preview, submitting])

  if (!open) return null

  const selectKind = (nextKind: ModelingKind) => {
    const nextConfig = modelingKinds.find(item => item.id === nextKind) ?? modelingKinds[0]
    setKind(nextKind)
    setAnswers({})
    setDecisionThreads({})
    setPreview(null)
    setSupplement('')
    setConfirmations({ decisions: false, fields: false, draft: false })
    setSelectedIDs(current => new Set([...current].filter(id => {
      const dataset = datasets.find(item => item.id === id)
      return dataset && nextConfig.sourceLayers.includes(dataset.layer) && datasetReady(dataset)
    })))
  }

  const startAnalysis = () => {
    analysisTimers.current.forEach(timer => window.clearTimeout(timer))
    setAnswers({})
    setDecisionThreads({})
    setPreview(null)
    setAnalysisStage(1)
    setStep(3)
    analysisTimers.current = [
      window.setTimeout(() => setAnalysisStage(2), 420),
      window.setTimeout(() => setAnalysisStage(3), 860),
      window.setTimeout(() => setAnalysisStage(4), 1_240),
    ]
  }

  const openDatasetPreview = async (dataset: DatasetSummary) => {
    setPreview({ dataset, loading: true })
    if (dataset.id.startsWith('snapshot-')) {
      setPreview({ dataset, loading: false, data: snapshotPreview(dataset) })
      return
    }
    if (!dataset.currentPublishedVersionId) {
      setPreview({ dataset, loading: false, error: '该数据集没有可预览的发布版本，请先完成发布。' })
      return
    }
    try {
      const data = await datasetAPI.previewVersion(dataset.id, dataset.currentPublishedVersionId, crypto.randomUUID(), {}, 10)
      setPreview(current => current?.dataset.id === dataset.id ? { dataset, loading: false, data } : current)
    } catch (cause) {
      const error = cause instanceof Error ? cause.message : '加载发布版本预览失败'
      setPreview(current => current?.dataset.id === dataset.id ? { dataset, loading: false, error } : current)
    }
  }

  const updateDecisionThread = (question: AdvisorQuestion, update: (thread: DecisionThread) => DecisionThread) => {
    setDecisionThreads(current => ({
      ...current,
      [question.id]: update(current[question.id] ?? initialDecisionThread(question, candidates, selectedDatasets)),
    }))
  }

  const setDecisionDraft = (question: AdvisorQuestion, draft: string) => {
    updateDecisionThread(question, thread => ({ ...thread, draft }))
  }

  const sendDecisionMessage = (question: AdvisorQuestion) => {
    updateDecisionThread(question, thread => {
      const input = thread.draft.trim()
      if (!input || thread.locked) return thread
      const round = thread.messages.filter(message => message.role === 'USER').length + 1
      const result = refineDecision(question, candidates, selectedDatasets, thread.messages, input, thread.suggestion)
      return {
        ...thread,
        draft: '',
        suggestion: result.suggestion,
        locked: false,
        messages: [
          ...thread.messages,
          { id: `${question.id}:user:${round}`, role: 'USER', content: input, round },
          { id: `${question.id}:assistant:${round}`, role: 'ASSISTANT', content: result.content, round },
        ],
      }
    })
  }

  const selectDecisionOption = (question: AdvisorQuestion, value: string) => {
    setAnswers(current => ({ ...current, [question.id]: value }))
    updateDecisionThread(question, thread => ({ ...thread, locked: false }))
  }

  const setDecisionLocked = (question: AdvisorQuestion, locked: boolean) => {
    if (locked && !answers[question.id]) return
    updateDecisionThread(question, thread => ({ ...thread, locked }))
  }

  const submit = async () => {
    if (!canSubmit || submitting || busy) return
    setSubmitting(true)
    const success = await onSubmit(config.trigger, config.label, [...selectedIDs])
    setSubmitting(false)
    if (success) setSubmitted(true)
  }

  const renderFooter = () => {
    if (submitted) {
      return <footer className="modeling-advisor-footer is-submitted">
        <span><CheckCircleIcon size={18} weight="fill" />{config.trigger ? '建模任务已进入后台，当前弹窗可以安全关闭。' : '应用建模方案已生成；自动落地前仍需接入 ADS 执行能力。'}</span>
        <button className="modeling-advisor-primary" type="button" onClick={onClose}>返回数据集列表</button>
      </footer>
    }
    const footerMessage = step === 1
      ? '先选择目标建模方式，系统会严格限制主分析数据层。'
      : step === 2
        ? `已选 ${selectedIDs.size} 个可建模数据集`
        : step === 3
          ? !candidates.length ? '未识别到可落地候选，请返回重新选择数据集' : questions.length ? `仅剩 ${questions.length - answeredCount} 项会审结论待锁定` : '所有候选均可依据元信息自动裁决，无需人工选择'
          : step === 4 ? `可落地 ${resolvedCandidates.length} 个模型，涉及 ${cleaningRules.length} 条字段规则` : '最后确认一次范围与安全边界。'
    return <footer className="modeling-advisor-footer">
      <div>{step > 1 && <button className="modeling-advisor-back" type="button" disabled={submitting} onClick={() => setStep(current => Math.max(1, current - 1) as ModelingStep)}><ArrowLeftIcon size={16} />上一步</button>}</div>
      <span>{footerMessage}</span>
      {step === 1 && <button className="modeling-advisor-primary" type="button" onClick={() => setStep(2)}>选择数据集<ArrowRightIcon size={16} /></button>}
      {step === 2 && <button className="modeling-advisor-primary" type="button" disabled={!selectedIDs.size} onClick={startAnalysis}><SparkleIcon size={16} weight="fill" />开始识别与比较</button>}
      {step === 3 && <button className="modeling-advisor-primary" type="button" disabled={!canContinueAnalysis} onClick={() => setStep(4)}>生成落地方案<ArrowRightIcon size={16} /></button>}
      {step === 4 && <button className="modeling-advisor-primary" type="button" onClick={() => setStep(5)}>进入提交确认<ArrowRightIcon size={16} /></button>}
      {step === 5 && <button className="modeling-advisor-primary" type="button" disabled={!canSubmit || submitting || busy} onClick={() => void submit()}>{submitting ? <SpinnerGapIcon className="is-spinning" size={17} /> : <MagicWandIcon size={17} weight="fill" />}{submitting ? '正在提交…' : config.trigger ? '确认并生成建模草稿' : '确认并生成应用方案'}</button>}
    </footer>
  }

  return <div className="modeling-advisor-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !submitting) onClose() }}>
    <section className="modeling-advisor" role="dialog" aria-modal="true" aria-labelledby="modeling-advisor-title">
      <header className="modeling-advisor-header">
        <span className="modeling-advisor-brand"><MagicWandIcon size={23} weight="duotone" /></span>
        <div><span>AI 智能建模</span><h2 id="modeling-advisor-title">智能建模</h2><p>选择建模类型，系统将分析数据集证据并生成可审阅方案。</p></div>
        <div className="modeling-advisor-context">
          {activeModelingLabels.length ? <strong><SpinnerGapIcon className="is-spinning" size={15} />{activeModelingLabels.join('、')}执行中</strong> : <strong><ShieldCheckIcon size={15} weight="fill" />全程人工确认后生效</strong>}
          <small>仅生成草稿 · 不自动发布</small>
        </div>
        <button className="modeling-advisor-close" type="button" aria-label="关闭智能建模" disabled={submitting} onClick={onClose}><XIcon size={19} /></button>
      </header>

      <nav className="modeling-advisor-steps" aria-label="智能建模步骤">
        {stepLabels.map((label, index) => {
          const value = index + 1
          const completed = value < step || submitted
          const active = value === step && !submitted
          return <button key={label} type="button" disabled={value > step || submitting || submitted} className={`${completed ? 'is-complete' : ''}${active ? ' is-active' : ''}`} onClick={() => { if (value < step) setStep(value as ModelingStep) }}>
            <span>{completed ? <CheckIcon size={13} weight="bold" /> : value}</span><strong>{label}</strong>
          </button>
        })}
      </nav>

      <div ref={bodyRef} className="modeling-advisor-body">
        {step === 1 && <section className="modeling-kind-step">
          <div className="modeling-section-heading">
            <div><span>第 1 步</span><h3>你希望构建哪一类数据模型？</h3><p>建模类型决定可选数据层、需要确认的业务问题和最终输出。</p></div>
            <span className="modeling-safe-badge"><ShieldCheckIcon size={16} weight="fill" />建议可随时返回修改</span>
          </div>
          <div className="modeling-kind-grid">
            {modelingKinds.map(item => {
              const KindIcon = item.icon
              const selected = item.id === kind
              const readyCount = datasets.filter(dataset => item.sourceLayers.includes(dataset.layer) && datasetReady(dataset)).length
              return <button className={`modeling-kind-card ${item.accent}${selected ? ' is-selected' : ''}`} type="button" aria-pressed={selected} key={item.id} onClick={() => selectKind(item.id)}>
                <span className="modeling-kind-icon"><KindIcon size={25} weight="duotone" /></span>
                <span className="modeling-kind-copy"><strong>{item.label}</strong><em>{item.shortLabel}</em></span>
                {selected && <span className="modeling-kind-check"><CheckIcon size={13} weight="bold" /></span>}
                <p>{item.description}</p>
                <dl><div><dt>主分析层</dt><dd>{item.sourceLayers.join(' / ')}</dd></div><div><dt>目标层</dt><dd>{item.targetLayer}</dd></div></dl>
                <footer><span>{item.outcome}</span><strong>{readyCount} 个可用</strong></footer>
              </button>
            })}
          </div>
          <section className="modeling-layer-map" aria-label="数仓分层与建模类型关系">
            <header><TreeStructureIcon size={18} /><div><strong>分层建模路径</strong><small>系统会根据上游成熟度推荐，不跨层猜测业务口径。</small></div></header>
            <div>{layerGuidance.map((item, index) => <article className={config.sourceLayers.includes(item.layer as DatasetSummary['layer']) ? 'is-source' : config.targetLayer === item.layer ? 'is-target' : ''} key={item.layer}>
              <span>{item.layer}</span><div><strong>{item.label}</strong><em>{item.models}</em><small>{item.note}</small></div>{index < layerGuidance.length - 1 && <ArrowRightIcon size={16} />}
            </article>)}</div>
          </section>
        </section>}

        {step === 2 && <section className="modeling-dataset-step">
          <div className="modeling-section-heading">
            <div><span>第 2 步 · {config.label}</span><h3>选择需要共同分析的数据集</h3><p>当前只展示 {config.sourceLayers.join('、')} 层；系统会先逐个判断，再把全部候选放到一起比较。</p></div>
            <span className="modeling-target-badge"><ArrowRightIcon size={15} />{config.sourceLayers.join(' / ')} → {config.targetLayer}</span>
          </div>
          <div className="modeling-dataset-layout">
            <section className="modeling-dataset-picker">
              <header><label><MagnifyingGlassIcon size={17} /><input type="search" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="搜索数据集名称、编码或来源" /></label><span>可用 {eligible.filter(datasetReady).length} / {eligible.length}</span></header>
              <div className="modeling-dataset-list">
                {!visibleDatasets.length && <div className="modeling-empty"><DatabaseIcon size={28} /><strong>当前没有符合条件的数据集</strong><span>请先完成上游数据集发布，或返回选择其他建模类型。</span></div>}
                {visibleDatasets.map(dataset => {
                  const ready = datasetReady(dataset)
                  const selected = selectedIDs.has(dataset.id)
                  return <label className={`${selected ? 'is-selected' : ''}${!ready ? ' is-disabled' : ''}`} key={dataset.id}>
                    <input type="checkbox" checked={selected} disabled={!ready} onChange={() => setSelectedIDs(current => { const next = new Set(current); if (next.has(dataset.id)) next.delete(dataset.id); else next.add(dataset.id); return next })} />
                    <span className={`modeling-dataset-layer ${dataset.layer.toLowerCase()}`}>{dataset.layer}</span>
                    <span className="modeling-dataset-copy"><strong>{dataset.name}</strong><em>{dataset.code}</em><small>{dataset.description || '暂无业务说明'}</small></span>
                    <span className="modeling-dataset-meta"><strong>{statusLabels[dataset.status] ?? dataset.status}</strong><small>V{dataset.version} · {displayDate(dataset.updatedAt)}</small>{!ready && <em>需先发布</em>}</span>
                  </label>
                })}
              </div>
            </section>
            <aside className="modeling-selection-summary">
              <header><span><CheckCircleIcon size={18} weight="fill" /></span><div><strong>本次共同分析范围</strong><small>{selectedDatasets.length ? `已选择 ${selectedDatasets.length} 个数据集` : '等待选择数据集'}</small></div></header>
              <div className="modeling-selection-flow"><span>逐表识别</span><ArrowRightIcon size={17} /><span>跨表归并</span><ArrowRightIcon size={17} /><span>{config.targetLayer} 落地</span></div>
              <ol>
                {selectedDatasets.map(dataset => <li key={dataset.id}><DatabaseIcon size={15} /><span><strong>{dataset.name}</strong><small>{dataset.originDataSourceName || dataset.layer}</small></span><button type="button" aria-label={`移除${dataset.name}`} onClick={() => setSelectedIDs(current => { const next = new Set(current); next.delete(dataset.id); return next })}><XIcon size={13} /></button></li>)}
                {!selectedDatasets.length && <li className="is-placeholder"><InfoIcon size={16} /><span>建议一次选择同一领域的相关数据集，才能发现包含、重复和异常信息。</span></li>}
              </ol>
              <footer><LightbulbIcon size={16} weight="fill" /><span>选择越完整，跨来源功能重叠判断越可靠；未发布数据不会参与推理。</span></footer>
            </aside>
          </div>
        </section>}

        {step === 3 && <section className="modeling-analysis-step">
          <div className="modeling-section-heading">
            <div><span>第 3 步 · 证据与会审</span><h3>{analysisStage < 4 ? '正在执行分层推理链…' : `已识别 ${candidates.length} 个候选，仅 ${questions.length} 项需要人机协作会审`}</h3><p>{analysisStage < 4 ? '先逐表理解功能，再比较所有候选；不会用固定问题打断你。' : '系统先展示依据和数据样例，再用多轮业务补充更新判断；结论由用户最终锁定。'}</p></div>
            <span className={`modeling-analysis-state ${analysisStage >= 4 ? 'is-complete' : ''}`}>{analysisStage >= 4 ? <CheckCircleIcon size={16} weight="fill" /> : <SpinnerGapIcon className="is-spinning" size={16} />}{analysisStage >= 4 ? `完成 · 自动裁决 ${autoResolvedCount} 项` : '分析进行中'}</span>
          </div>
          {analysisStage < 4 ? <section className="modeling-analysis-loading">
            <SpinnerGapIcon className="is-spinning" size={34} />
            <strong>正在构建可解释的建模结论</strong>
            <div className="modeling-analysis-progress">
              {['读取发布版本与元信息', `逐表判断是否为${config.subjectLabel}`, `比较功能包含、重复与异常`, '生成清洗规则与待确认项'].map((label, index) => <div className={analysisStage > index ? 'is-complete' : analysisStage === index + 1 ? 'is-running' : ''} key={label}><span>{analysisStage > index ? <CheckIcon size={12} weight="bold" /> : index + 1}</span><strong>{label}</strong><small>{analysisStage > index ? '已完成' : analysisStage === index + 1 ? '分析中' : '等待中'}</small></div>)}
            </div>
          </section> : <div className="modeling-reasoning-layout">
            <section className="modeling-identification-panel">
              <header><DatabaseIcon size={18} /><div><strong>一、逐个数据集识别</strong><small>先判断是否已经是目标模型，再判断能否提取目标信息</small></div></header>
              <div className="modeling-finding-list">
                {findings.map(finding => <article key={finding.dataset.id}>
                  <header><span>{finding.dataset.layer}</span><div><strong>{finding.dataset.name}</strong><small>{finding.dataset.code}</small></div><button className="modeling-preview-trigger" type="button" onClick={() => void openDatasetPreview(finding.dataset)}><TableIcon size={13} />预览数据</button><em className={finding.direct ? 'is-direct' : !finding.candidates.length ? 'is-excluded' : ''}>{finding.direct ? `已是${config.subjectLabel}` : finding.candidates.length ? `可提取${config.subjectLabel}` : '本次排除'}</em></header>
                  <p>{finding.conclusion}</p><small className="modeling-evidence">证据：{finding.evidence}</small>
                  <div>{finding.candidates.map(candidate => <section key={candidate.id}><span>{Math.round(candidate.confidence * 100)}%</span><div><strong>{candidate.tableCode}</strong><small>{candidate.functionDescription}</small></div><em>{candidate.fields.slice(0, 3).join(' · ')}</em></section>)}</div>
                </article>)}
              </div>
            </section>
            <section className="modeling-comparison-panel">
              <header><GitBranchIcon size={18} /><div><strong>二、跨候选功能比较与裁决</strong><small>检查包含、重复、异常；能自动抉择时只保留最合理方案</small></div></header>
              <div className="modeling-check-summary">
                <article><strong>功能重叠</strong><span>{comparisons.length ? `${comparisons.length} 组` : '未发现'}</span><small>同一业务功能候选</small></article>
                <article><strong>自动裁决</strong><span>{autoResolvedCount} 组</span><small>证据充分，保留更合理者</small></article>
                <article><strong>异常线索</strong><span>0 个阻断</span><small>字段异常转入清洗规则</small></article>
              </div>
              <div className="modeling-comparison-list">
                {!comparisons.length && <div className={`modeling-auto-decision ${!candidates.length ? 'is-empty' : ''}`}>{candidates.length ? <CheckCircleIcon size={18} weight="fill" /> : <WarningCircleIcon size={18} weight="fill" />}<div><strong>{candidates.length ? '候选功能彼此独立' : '没有可落地候选'}</strong><small>{candidates.length ? '没有发现需要合并的包含或重复关系，全部进入可落地清单。' : '所选数据集不表达当前目标功能，也无法从现有元信息中提取；请返回重新选择。'}</small></div></div>}
                {comparisons.map(comparison => <article className={comparison.needsHuman ? 'needs-human' : ''} key={comparison.id}><span>{comparison.relation === 'CONTAINMENT' ? '包含' : comparison.relation === 'DUPLICATE' ? '重复' : comparison.relation === 'ANOMALY' ? '异常' : '独立'}</span><div><strong>{comparison.title}</strong><small>{comparison.detail}</small></div><em>{comparison.needsHuman ? '待人工裁决' : '已自动保留最优'}</em></article>)}
              </div>
              <section className="modeling-human-decision">
                <header><WarningCircleIcon size={18} weight="fill" /><div><strong>人机协作会审</strong><small>{questions.length ? '先核验证据和样例，再通过多轮补充更新判断；只有用户锁定的结论才会进入落地方案。' : '当前没有无法裁决的问题，无需为流程而确认。'}</small></div><em>已锁定 {answeredCount}/{questions.length}</em></header>
                {!questions.length && <div className="modeling-no-human"><ShieldCheckIcon size={20} weight="fill" /><span><strong>无需人工裁决</strong><small>系统已保留更完整的候选，并记录被合并来源与理由。</small></span></div>}
                {questions.map((question, index) => {
                  const thread = decisionThreads[question.id] ?? initialDecisionThread(question, candidates, selectedDatasets)
                  const related = relatedQuestionCandidates(question, candidates)
                  return <article className={`modeling-decision-question${thread.locked ? ' is-locked' : ''}`} key={question.id}>
                    <header><span>{index + 1}</span><div><strong>{question.title}</strong><small>{question.reason}</small></div><em>{thread.locked ? '结论已锁定' : `会审第 ${thread.messages.filter(message => message.role === 'USER').length + 1} 轮`}</em></header>
                    <section className="modeling-question-evidence">
                      <header><InfoIcon size={14} /><strong>可核验依据</strong><small>来源、字段和置信度只是起点，点击表查看真实样例</small></header>
                      <div>{related.map(candidate => {
                        const dataset = selectedDatasets.find(item => item.id === candidate.datasetID)
                        if (!dataset) return null
                        return <article key={candidate.id}>
                          <span><strong>{candidate.tableCode}</strong><em>{Math.round(candidate.confidence * 100)}%</em></span>
                          <p>{dataset.name}<small>{dataset.originDataSourceName || dataset.layer} · V{dataset.version}</small></p>
                          <small>{candidate.fields.join(' · ')}</small>
                          <button type="button" onClick={() => void openDatasetPreview(dataset)}><TableIcon size={13} />查看表数据</button>
                        </article>
                      })}</div>
                    </section>
                    <section className="modeling-decision-thread">
                      <header><SparkleIcon size={14} weight="fill" /><strong>多轮判断记录</strong><small>业务事实会保留在本次裁决证据中</small></header>
                      <div className="modeling-decision-messages">{thread.messages.map(message => <div className={message.role === 'USER' ? 'is-user' : 'is-assistant'} key={message.id}><span>{message.role === 'USER' ? '你' : '智能建模'}</span><p>{message.content}</p>{message.round > 0 && <em>第 {message.round} 轮</em>}</div>)}</div>
                      {thread.suggestion && <div className="modeling-current-suggestion"><span><LightbulbIcon size={15} weight="fill" /><strong>当前建议</strong><em>{decisionOptionLabel(question, thread.suggestion)}</em></span><button type="button" disabled={thread.locked} onClick={() => selectDecisionOption(question, thread.suggestion!)}>采用当前建议</button></div>}
                      <form onSubmit={event => { event.preventDefault(); sendDecisionMessage(question) }}>
                        <textarea rows={2} value={thread.draft} disabled={thread.locked} onChange={event => setDecisionDraft(question, event.target.value)} placeholder="补充可验证事实，例如：CRM 是客户权威源，customer_id 唯一率 99.98%，订单侧仅保留下单快照……" />
                        <button type="submit" disabled={thread.locked || !thread.draft.trim()}><ArrowRightIcon size={14} />发送并重新判断</button>
                      </form>
                    </section>
                    <div className="modeling-decision-options">{question.options.map(option => <label className={answers[question.id] === option.value ? 'is-selected' : ''} key={option.value}><input type="radio" name={`modeling-question-${question.id}`} value={option.value} disabled={thread.locked} checked={answers[question.id] === option.value} onChange={() => selectDecisionOption(question, option.value)} /><span><strong>{option.label}{thread.suggestion === option.value ? <em>当前建议</em> : option.recommended && !thread.suggestion ? <em>初始建议</em> : null}</strong><small>{option.description}</small></span><CheckCircleIcon size={17} weight="fill" /></label>)}</div>
                    <footer className="modeling-decision-lock">
                      <span>{thread.locked ? <CheckCircleIcon size={17} weight="fill" /> : <InfoIcon size={17} />}<span><strong>{thread.locked ? `已锁定：${decisionOptionLabel(question, answers[question.id])}` : answers[question.id] ? `待锁定：${decisionOptionLabel(question, answers[question.id])}` : '请在充分核验后选择结论'}</strong><small>{thread.locked ? '该结论将进入落地方案；如需补充新证据，可解除锁定继续讨论。' : '选择结果不等于确认，必须显式锁定后流程才可继续。'}</small></span></span>
                      <button type="button" disabled={!thread.locked && !answers[question.id]} onClick={() => setDecisionLocked(question, !thread.locked)}>{thread.locked ? '解除锁定并继续讨论' : '锁定此结论'}</button>
                    </footer>
                  </article>
                })}
                <label className="modeling-supplement"><span><strong>补充业务信息（可选）</strong><small>说明主系统、特殊过滤、字段权威来源或不能合并的数据。</small></span><textarea value={supplement} onChange={event => setSupplement(event.target.value)} placeholder="例如：客户等级以 CRM 为准；测试门店必须过滤；渠道编码 00 与 OFFLINE 等价……" rows={3} /></label>
              </section>
            </section>
          </div>}
        </section>}

        {step === 4 && <section className="modeling-review-step">
          <div className="modeling-section-heading">
            <div><span>第 4 步 · 落地方案</span><h3>{resolvedCandidates.length} 个可落地 {config.targetLayer} 模型，{cleaningRules.length} 条字段清洗规则</h3><p>每个模型都有业务功能、来源、字段规则和质量门禁；被归并的候选不会重复建表。</p></div>
            <span className="modeling-confidence"><SparkleIcon size={16} weight="fill" />结论可追溯</span>
          </div>
          <div className="modeling-review-layout">
            <div className="modeling-review-main">
              <section className="modeling-plan-flow">
                <header><GitBranchIcon size={19} /><div><strong>建模流程</strong><small>{selectedDatasets.length} 个输入 → {candidates.length} 个候选 → {resolvedCandidates.length} 个落地模型</small></div></header>
                <ol>
                  <li><span><DatabaseIcon size={18} /></span><div><strong>01 · 锁定元信息证据</strong><p>{selectedDatasets.map(dataset => dataset.name).join('、')}</p><small>记录发布版本、结构哈希、来源、说明与标签。</small></div><em>已完成</em></li>
                  <li><span><SparkleIcon size={18} /></span><div><strong>02 · 逐表识别与提取</strong><p>识别 {findings.filter(finding => finding.direct).length} 个直接模型，提取 {findings.filter(finding => !finding.direct).flatMap(finding => finding.candidates).length} 个候选信息。</p><small>每个候选均保留功能说明、来源和字段线索。</small></div><em>{candidates.length} 个候选</em></li>
                  <li><span><GitBranchIcon size={18} /></span><div><strong>03 · 功能归并与冲突裁决</strong><p>检查包含、重复和元信息异常；自动裁决 {autoResolvedCount} 组，人工裁决 {questions.length} 组。</p><small>被合并候选转为来源映射，不重复落表。</small></div><em>{resolvedCandidates.length} 个保留</em></li>
                  <li><span><BroomIcon size={18} /></span><div><strong>04 · 字段清洗与缺省值处理</strong><p>关联键不默认改大小写或填充；日期、数字、文本及其他类型按显式缺省策略处理。</p><small>所有回填均保留原值和 is_defaulted，异常值不静默删除。</small></div><em>{cleaningRules.length} 条规则</em></li>
                  <li><span><TableIcon size={18} /></span><div><strong>05 · 转换、关联与拆表</strong><p>{kind === 'DIMENSION' ? '生成代理键与来源映射，按业务实体拆分公共维度。' : kind === 'DETAIL' ? '锁定原子粒度，关联当前已发布 DIM 并检测 1:N 扇出。' : kind === 'SUBJECT' ? '统一指标口径与汇总粒度，关联公共 DIM 并设置迟到回刷。' : '按消费场景裁剪字段、指标和刷新频率，形成 ADS 表与服务契约。'}</p><small>每个独立业务功能一张核心表，共享关系进入辅助表。</small></div><em>{resolvedCandidates.length} 张核心表</em></li>
                  <li><span><ShieldCheckIcon size={18} /></span><div><strong>06 · 质量门禁后生成草稿</strong><p>唯一性、完整性、枚举有效性、金额守恒、关联覆盖率和行数膨胀校验。</p><small>阻断项失败立即停止，不自动发布或替换下游。</small></div><em>人工发布</em></li>
                </ol>
              </section>
              <section className="modeling-cleaning-panel">
                <header><BroomIcon size={18} /><div><strong>字段清洗清单</strong><small>每条规则同时展示缺省值、保护措施和产出，关联键默认保持原样</small></div></header>
                <div><table><thead><tr><th>目标模型</th><th>字段</th><th>发现的问题</th><th>缺省值</th><th>清洗与转换规则</th><th>产出</th></tr></thead><tbody>{cleaningRules.map((rule, index) => <tr key={`${rule.tableCode}:${rule.field}:${index}`}><td>{rule.tableCode}</td><td>{rule.field}</td><td>{rule.issue}</td><td><strong className="modeling-default-value">{rule.defaultValue}</strong></td><td>{rule.rule}</td><td>{rule.result}</td></tr>)}</tbody></table></div>
                <section className="modeling-default-policies">
                  <header><ShieldCheckIcon size={17} weight="fill" /><div><strong>全类型缺省值策略</strong><small>遵循最小侵害：不覆盖原值、不混淆缺失与真实值、不制造关联键</small></div></header>
                  <div>{defaultPolicies.map(policy => <article key={policy.type}><span><strong>{policy.type}</strong><em>{policy.defaultValue}</em></span><p>{policy.storage}</p><small>{policy.protection}</small></article>)}</div>
                  <footer><WarningCircleIcon size={15} weight="fill" /><span><strong>零日期兼容说明：</strong><code>0000-00-00</code> 作为源数据缺省哨兵保留；PostgreSQL 等不支持零日期的强类型字段写 SQL NULL，并通过 raw_value 与 is_defaulted 保留完整语义。</span></footer>
                </section>
              </section>
            </div>
            <aside className="modeling-output-panel">
              <header><TableIcon size={18} /><div><strong>可落地模型清单</strong><small>目标层：{config.targetLayer}</small></div></header>
              {resolvedCandidates.map((candidate, index) => <article key={candidate.id}><span>{index + 1}</span><div><strong>{candidate.tableCode}</strong><small>{candidate.functionDescription}</small></div><em>{outputRole(kind, index)}</em></article>)}
              <section><strong>归并与人机决策记录</strong>
                {!comparisons.length && <p><CheckCircleIcon size={15} weight="fill" /><span>候选功能重叠</span><em>未发现</em></p>}
                {comparisons.map(comparison => <p key={comparison.id}><CheckCircleIcon size={15} weight="fill" /><span>{comparison.title}</span><em>{comparison.needsHuman ? '已由用户裁决' : '系统自动裁决'}</em></p>)}
                {supplement && <p><CheckCircleIcon size={15} weight="fill" /><span>补充业务信息</span><em title={supplement}>{supplement}</em></p>}
              </section>
              <footer><InfoIcon size={16} /><span>{config.trigger ? '本方案只生成可编辑草稿；发布、物化和下游替换仍沿用现有审批流程。' : '应用建模分析已完整覆盖；当前服务尚无 ADS 自动执行器，本次只生成可审核方案。'}</span></footer>
            </aside>
          </div>
        </section>}

        {step === 5 && <section className="modeling-submit-step">
          {!submitted ? <>
            <div className="modeling-section-heading">
              <div><span>第 5 步 · 安全提交</span><h3>{config.trigger ? '确认后生成可编辑的建模草稿' : '确认后生成可审核的应用建模方案'}</h3><p>{config.trigger ? '本次提交不会覆盖现有已发布版本，也不会自动运行物化。' : 'ADS 自动落地能力尚未接入，系统不会伪造后台任务或声称已经落表。'}</p></div>
              <span className="modeling-safe-badge"><ShieldCheckIcon size={16} weight="fill" />可审计 · 不自动发布</span>
            </div>
            <div className="modeling-submit-layout">
              <section className="modeling-submit-summary">
                <header><MagicWandIcon size={20} weight="duotone" /><div><strong>{config.label}执行摘要</strong><small>核对输入、裁决与预期输出</small></div></header>
                <dl><div><dt>建模类型</dt><dd>{config.label}</dd></div><div><dt>主分析层</dt><dd>{config.sourceLayers.join(' / ')}</dd></div><div><dt>目标数据层</dt><dd>{config.targetLayer}</dd></div><div><dt>选中数据集</dt><dd>{selectedDatasets.length} 个</dd></div><div><dt>可落地模型</dt><dd>{resolvedCandidates.length} 个</dd></div><div><dt>人工裁决</dt><dd>{questions.length} 项</dd></div></dl>
                <div>{selectedDatasets.map(dataset => <span key={dataset.id}><DatabaseIcon size={14} />{dataset.name}<em>{dataset.layer}</em></span>)}</div>
              </section>
              <section className="modeling-submit-confirmations">
                <header><ShieldCheckIcon size={19} /><div><strong>提交前确认</strong><small>三项全部确认后才能继续</small></div></header>
                <label className={confirmations.decisions ? 'is-checked' : ''}><input type="checkbox" checked={confirmations.decisions} onChange={event => setConfirmations(current => ({ ...current, decisions: event.target.checked }))} /><span><strong>候选识别与归并结果无误</strong><small>已核对各数据集的目标模型判断、业务功能和重复候选裁决。</small></span><CheckCircleIcon size={19} weight="fill" /></label>
                <label className={confirmations.fields ? 'is-checked' : ''}><input type="checkbox" checked={confirmations.fields} onChange={event => setConfirmations(current => ({ ...current, fields: event.target.checked }))} /><span><strong>字段清洗、缺省值与拆表范围可接受</strong><small>关联键未默认规范化；所有回填均保留原值、缺省标记和决策证据。</small></span><CheckCircleIcon size={19} weight="fill" /></label>
                <label className={confirmations.draft ? 'is-checked' : ''}><input type="checkbox" checked={confirmations.draft} onChange={event => setConfirmations(current => ({ ...current, draft: event.target.checked }))} /><span><strong>{config.trigger ? '仅生成草稿，不自动发布' : '仅生成应用方案，不声称已经落地'}</strong><small>{config.trigger ? '草稿必须完成预览、质量门禁和人工审批后才能服务下游。' : '待 DWS→ADS 执行器接入后，仍需重新校验真实字段和数据质量。'}</small></span><CheckCircleIcon size={19} weight="fill" /></label>
                <footer><WarningCircleIcon size={17} weight="fill" /><span>如果后端发现元信息已变化，本次建模会停止并提示重新分析，不会基于过期结构继续生成。</span></footer>
              </section>
            </div>
          </> : <section className="modeling-submitted-state">
            <span><CheckCircleIcon size={42} weight="fill" /></span>
            <h3>{config.trigger ? `${config.label}任务已提交` : '应用建模方案已生成'}</h3>
            <p>{config.trigger ? `系统将按已确认的识别、归并和清洗规则生成 ${config.targetLayer} 草稿。完成后可在模型资产清单中查看、调整并提交发布。` : '系统已形成 DWS→ADS 的候选识别、功能归并、字段清洗和拆表方案；自动落表需等待 ADS 执行能力接入。'}</p>
            <div>{config.trigger ? <SpinnerGapIcon className="is-spinning" size={18} /> : <ShieldCheckIcon size={18} weight="fill" />}<strong>{config.trigger ? '后台正在读取锁定版本与元信息' : '方案已完成，未创建虚假的后台任务'}</strong><small>{config.trigger ? '你可以关闭弹窗，任务会继续运行' : '可据此进入人工 ADS 画布进行复核'}</small></div>
            <ol><li className="is-active"><CheckIcon size={13} />识别候选</li><li className="is-active"><CheckIcon size={13} />功能归并</li><li className="is-active"><CheckIcon size={13} />清洗方案</li><li className={config.trigger ? '' : 'is-active'}>{config.trigger ? <span>4</span> : <CheckIcon size={13} />}{config.trigger ? '写入可编辑草稿' : '形成应用方案'}</li></ol>
          </section>}
        </section>}
      </div>
      {preview && <div className="modeling-preview-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) setPreview(null) }}>
        <section className="modeling-preview-dialog" role="dialog" aria-modal="true" aria-labelledby="modeling-preview-title">
          <header>
            <span><TableIcon size={20} weight="duotone" /></span>
            <div><strong id="modeling-preview-title">{preview.dataset.name}</strong><small>{preview.dataset.code} · {preview.dataset.layer} · 发布版本 V{preview.dataset.version}</small></div>
            <button type="button" aria-label="关闭数据预览" onClick={() => setPreview(null)}><XIcon size={17} /></button>
          </header>
          <div className="modeling-preview-notice"><InfoIcon size={15} /><span>只读展示发布版本的前 10 行，用于核验键值格式、空值、枚举和来源差异；样例不能代替全量质量统计。</span></div>
          <div className="modeling-preview-content">
            {preview.loading && <div className="modeling-preview-state"><SpinnerGapIcon className="is-spinning" size={28} /><strong>正在读取发布版本样例…</strong></div>}
            {!preview.loading && preview.error && <div className="modeling-preview-state is-error"><WarningCircleIcon size={28} weight="fill" /><strong>预览加载失败</strong><span>{preview.error}</span></div>}
            {!preview.loading && preview.data && <table><thead><tr>{preview.data.columns.map(column => <th key={column}>{column}</th>)}</tr></thead><tbody>{preview.data.rows.map((row, rowIndex) => <tr key={`${preview.data?.queryId}:${rowIndex}`}>{preview.data!.columns.map((column, columnIndex) => {
              const value = row[columnIndex]
              return <td className={value == null ? 'is-null' : ''} key={`${column}:${columnIndex}`}>{value == null ? 'NULL' : typeof value === 'object' ? JSON.stringify(value) : String(value)}</td>
            })}</tr>)}</tbody></table>}
            {!preview.loading && preview.data && !preview.data.rows.length && <div className="modeling-preview-state"><DatabaseIcon size={28} /><strong>发布版本暂无可展示数据</strong></div>}
          </div>
          <footer><span>{preview.data ? `返回 ${preview.data.rowCount} 行 · 查询耗时 ${preview.data.durationMs} ms` : '数据保持只读，不会触发建模或清洗'}</span><button type="button" onClick={() => setPreview(null)}>返回会审</button></footer>
        </section>
      </div>}
      {renderFooter()}
    </section>
  </div>
}
