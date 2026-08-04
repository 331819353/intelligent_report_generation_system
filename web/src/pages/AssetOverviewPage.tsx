import {
  ArrowClockwiseIcon,
  ArrowRightIcon,
  BookOpenTextIcon,
  BracketsCurlyIcon,
  CheckCircleIcon,
  CirclesThreeIcon,
  ClockIcon,
  FlowArrowIcon,
  FunctionIcon,
  GaugeIcon,
  GearSixIcon,
  GraphIcon,
  InfoIcon,
  LockKeyIcon,
  MagnifyingGlassIcon,
  PlugsConnectedIcon,
  ShieldCheckIcon,
  StackIcon,
  TreeStructureIcon,
  WarningCircleIcon,
  XIcon,
} from '@phosphor-icons/react'
import { FormEvent, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import {
  semanticAssetAPI,
  type BootstrapSemanticReleaseInput,
  type BootstrapSemanticReleasePreview,
  type SemanticCatalogObject,
  type SemanticCatalogReadiness,
  type SemanticRelease,
  type SemanticReleaseObject,
  type SemanticReleaseObjectType,
  type SemanticReleaseState,
} from '../lib/semantic-assets'
import { semanticChatAPI, type SemanticQASettings } from '../lib/semantic-chat'

type CatalogKey = 'overview' | 'business' | 'query' | 'understanding' | 'governance' | 'runtime'

type ObjectTypeMeta = {
  label: string
  shortLabel: string
  catalogs: Exclude<CatalogKey, 'overview'>[]
  description: string
  runtimeUse: string
  requiredFields: string[]
  editorHref?: string
}

const catalogDefinitions: Record<Exclude<CatalogKey, 'overview'>, {
  title: string
  eyebrow: string
  description: string
  accent: string
}> = {
  business: {
    title: '业务语义资产', eyebrow: 'What the business means', accent: 'blue',
    description: '定义业务概念、分析实体、指标、维度、时间与人群，是自然语言绑定的权威来源。',
  },
  query: {
    title: '查询语义资产', eyebrow: 'How the query is built', accent: 'violet',
    description: '规定粒度、关系、来源和字段映射，保证语义计划能够安全编译成确定性查询。',
  },
  understanding: {
    title: '理解增强资产', eyebrow: 'How language is bound', accent: 'cyan',
    description: '维护别名、规范值、认证问句和解析规则；向量与图仅是这些资产的可重建投影。',
  },
  governance: {
    title: '治理保障资产', eyebrow: 'Whether an answer is allowed', accent: 'green',
    description: '统一管理认证、权限、敏感等级、质量、新鲜度和发布门禁，决定运行时能否回答。',
  },
  runtime: {
    title: '系统运行配置', eyebrow: 'How the runtime is controlled', accent: 'amber',
    description: '面向平台管理员展示问答开关、图投影、置信阈值、路径预算和版本健康度。',
  },
}

const objectTypeMeta: Record<SemanticReleaseObjectType, ObjectTypeMeta> = {
  DOMAIN: {
    label: '主题域 / SemanticView', shortLabel: '主题域', catalogs: ['business'],
    description: '限定用户可以一起提问的业务空间。', runtimeUse: '身份授权后首先裁剪检索范围，阻止跨域同名对象误绑定。',
    requiredFields: ['title'],
  },
  BUSINESS_TERM: {
    label: '业务术语 / Alias', shortLabel: '业务术语', catalogs: ['business', 'understanding'],
    description: '把企业内部说法绑定到已批准概念。', runtimeUse: '用于问题规范化、精确召回、正负映射与歧义识别。',
    requiredFields: ['title', 'mappingType', 'targetIds'], editorHref: '/assets/semantics',
  },
  ENTITY: {
    label: '分析实体', shortLabel: '实体', catalogs: ['business'],
    description: '明确被度量的业务主体及最小粒度。', runtimeUse: '决定一行代表什么，并作为合法关系路径的起点。',
    requiredFields: ['title', 'grain', 'primaryKey'],
  },
  SEMANTIC_MODEL: {
    label: 'Semantic Model', shortLabel: '语义模型', catalogs: ['business', 'query'],
    description: '围绕一个主实体组织可查询度量、维度和来源。', runtimeUse: '为指标与维度提供统一执行上下文。',
    requiredFields: ['title', 'primaryEntityId', 'sourceDatasetId', 'grain'],
  },
  MEASURE: {
    label: '原子度量', shortLabel: '度量', catalogs: ['business'],
    description: '事实粒度上的基础聚合原子。', runtimeUse: '展开指标依赖和确定聚合阶段。',
    requiredFields: ['title', 'expression', 'aggregation'], editorHref: '/assets/metrics',
  },
  METRIC: {
    label: '业务指标', shortLabel: '指标', catalogs: ['business'],
    description: '面向业务消费的计算口径。', runtimeUse: '作为指标候选、兼容性检查和 Semantic Query IR 的核心对象。',
    requiredFields: ['title', 'formula', 'grain', 'defaultTimeDimensionId', 'sourceDatasetIds', 'permissionPolicyIds', 'qualityRuleIds'],
    editorHref: '/assets/metrics',
  },
  DIMENSION: {
    label: '维度 / 角色维度', shortLabel: '维度', catalogs: ['business'],
    description: '定义可分组、筛选、排序和下钻的业务切面。', runtimeUse: '绑定 group/filter/sort/drill 槽位并验证指标兼容性。',
    requiredFields: ['title', 'valueKey', 'usages'], editorHref: '/assets/metrics',
  },
  DIMENSION_VALUE: {
    label: '维度成员 / 值映射', shortLabel: '维度成员', catalogs: ['business', 'understanding'],
    description: '维度作用域内的规范值和别名。', runtimeUse: '以 dimension_id + canonical_code 唯一绑定用户说法。',
    requiredFields: ['title', 'dimensionId', 'canonicalCode'], editorHref: '/assets/dimension-values',
  },
  TIME: {
    label: '时间维度 / 日历', shortLabel: '时间', catalogs: ['business'],
    description: '定义事件时间、时区、日历和完整周期。', runtimeUse: '把相对时间转换为显式半开区间并选择正确业务时间。',
    requiredFields: ['title', 'timezone', 'calendar', 'completePeriodPolicy'],
  },
  COHORT: {
    label: 'Cohort / Segment', shortLabel: '人群', catalogs: ['business'],
    description: '可复用的人群、分群和观察窗口定义。', runtimeUse: '把“新客”等复杂人群词编译为受控筛选或子查询。',
    requiredFields: ['title', 'entryEvent', 'observationWindow', 'exclusions'],
  },
  RELATION: {
    label: '关系 / Join Contract', shortLabel: '关系', catalogs: ['query'],
    description: '定义有方向、有基数和 fanout 策略的认证关系。', runtimeUse: '生成 NebulaGraph 有界关系闭包并阻止错误 Join。',
    requiredFields: ['title', 'relationType', 'fromId', 'toId', 'cardinality', 'certified', 'fanoutPolicy'],
  },
  DATASET: {
    label: '认证来源 / 可问数据集', shortLabel: '数据集', catalogs: ['query'],
    description: '语义模型对应的权威物理或逻辑来源。', runtimeUse: '把查询计划限定到认证来源，并提供新鲜度和物化版本。',
    requiredFields: ['title', 'grain', 'source', 'freshness'], editorHref: '/datasets',
  },
  TABLE_COLUMN: {
    label: '字段映射', shortLabel: '字段', catalogs: ['query'],
    description: '语义成员到受控物理字段和类型的映射。', runtimeUse: '只在编译和受治理长尾路径中生成最小表列上下文。',
    requiredFields: ['title', 'datasetId', 'dataType'], editorHref: '/data-sources',
  },
  POLICY: {
    label: '权限 / 敏感策略', shortLabel: '策略', catalogs: ['governance'],
    description: '按租户、角色、用途和对象控制可见与可执行范围。', runtimeUse: '在检索前裁剪，并在图、计划和数仓执行阶段重复校验。',
    requiredFields: ['title', 'roles', 'purpose', 'effect', 'accessibleObjectIds'],
  },
  QUALITY_RULE: {
    label: '质量 / 新鲜度规则', shortLabel: '质量规则', catalogs: ['governance'],
    description: '定义执行前后的阻断或警告条件。', runtimeUse: '数据过期、缺失或不满足不变量时阻断回答。',
    requiredFields: ['title', 'targetId', 'severity', 'validator'],
  },
  CERTIFIED_EXAMPLE: {
    label: '认证问句 / 反例', shortLabel: '认证问句', catalogs: ['understanding'],
    description: '经过批准的问句、完整意图和回归证据。', runtimeUse: '用于示例召回、联合绑定和受影响黄金回归。',
    requiredFields: ['title', 'question', 'intent'],
  },
  PARSING_RULE: {
    label: '解析 / 歧义规则', shortLabel: '解析规则', catalogs: ['understanding'],
    description: '高精度、确定性的语言解析和阻断规则。', runtimeUse: '在模型前处理固定问法，并生成稳定的澄清或规范化结果。',
    requiredFields: ['title', 'ruleType', 'pattern', 'action'], editorHref: '/assets/parsing-rules',
  },
}

const objectTypeOrder = Object.keys(objectTypeMeta) as SemanticReleaseObjectType[]
const p0PackageTypes = new Set<SemanticReleaseObjectType>([
  'METRIC', 'DIMENSION', 'TIME', 'RELATION', 'DATASET', 'POLICY', 'QUALITY_RULE',
])

const fieldLabels: Record<string, string> = {
  title: '名称', code: '编码', description: '业务定义', aliases: '别名', positiveAliases: '正向别名',
  negativeAliases: '禁止映射', hardNegativeExamples: '反例', formula: '公式', expression: '表达式',
  aggregation: '聚合', grain: '粒度', primaryKey: '主键', primaryEntityId: '主实体',
  sourceDatasetId: '来源数据集', sourceDatasetIds: '来源数据集', groupableDimensionIds: '可用维度',
  defaultTimeDimensionId: '默认时间', permissionPolicyIds: '权限策略', qualityRuleIds: '质量规则',
  valueKey: '值键', usages: '用途', dimensionId: '所属维度', canonicalCode: '规范码',
  timezone: '时区', calendar: '日历', completePeriodPolicy: '完整周期', entryEvent: '入组事件',
  observationWindow: '观察窗口', exclusions: '排除规则', relationType: '关系类型', fromId: '起点',
  toId: '终点', cardinality: '基数', certified: '认证关系', fanoutPolicy: 'Fanout 策略',
  source: '物理来源', freshness: '新鲜度', datasetId: '数据集', dataType: '数据类型', roles: '角色',
  purpose: '用途', effect: '策略效果', accessibleObjectIds: '可访问对象', targetId: '目标资产',
  severity: '严重级别', validator: '验证器', question: '认证问句', intent: '黄金意图',
  ruleType: '规则类型', pattern: '匹配模式', action: '运行时动作', mappingType: '映射类型',
  targetIds: '目标资产', nativeMetricId: '原生指标', nativeMetricVersionId: '原生指标版本',
  nativeDatasetId: '原生数据集', nativeDatasetVersionId: '原生数据集版本',
}

const catalogIcons: Record<CatalogKey, ReactNode> = {
  overview: <GaugeIcon size={19} weight="duotone" />,
  business: <BookOpenTextIcon size={19} weight="duotone" />,
  query: <FlowArrowIcon size={19} weight="duotone" />,
  understanding: <CirclesThreeIcon size={19} weight="duotone" />,
  governance: <ShieldCheckIcon size={19} weight="duotone" />,
  runtime: <GearSixIcon size={19} weight="duotone" />,
}

const formatCount = (value: number) => new Intl.NumberFormat('zh-CN').format(value)
const formatDateTime = (value?: string) => value
  ? new Date(value).toLocaleString('zh-CN', { hour12: false })
  : '—'
const compactHash = (value?: string) => value
  ? value.length > 18 ? `${value.slice(0, 10)}…${value.slice(-6)}` : value
  : '—'

function isCatalogKey(value: string | null): value is CatalogKey {
  return value === 'overview' || value === 'business' || value === 'query' ||
    value === 'understanding' || value === 'governance' || value === 'runtime'
}

function contractTitle(item: SemanticReleaseObject) {
  return typeof item.contract.title === 'string' && item.contract.title.trim()
    ? item.contract.title.trim()
    : item.objectId
}

function valueText(value: unknown): string {
  if (value == null || value === '') return '—'
  if (typeof value === 'string') return value
  if (typeof value === 'number') return new Intl.NumberFormat('zh-CN').format(value)
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (Array.isArray(value)) return value.map(valueText).join('、') || '—'
  if (typeof value === 'object') return Object.entries(value as Record<string, unknown>)
    .map(([key, item]) => `${fieldLabels[key] ?? key}: ${valueText(item)}`).join('；')
  return String(value)
}

function contractReferences(contract: Record<string, unknown>) {
  const result: Array<{ field: string; values: string[] }> = []
  for (const [field, value] of Object.entries(contract)) {
    if (!/(Id|Ids|Ref|Refs)$/i.test(field)) continue
    const values = Array.isArray(value)
      ? value.filter((item): item is string => typeof item === 'string')
      : typeof value === 'string' ? [value] : []
    if (values.length) result.push({ field, values })
  }
  return result
}

function validationOf(release?: SemanticRelease) {
  const raw = release?.validationSummary
  if (!raw || typeof raw !== 'object') return undefined
  const record = raw as Record<string, unknown>
  const issues = Array.isArray(record.issues) ? record.issues as Array<Record<string, unknown>> : []
  return { status: typeof record.status === 'string' ? record.status : '', issues }
}

function releaseStatusLabel(status: SemanticRelease['status']) {
  const labels: Record<SemanticRelease['status'], string> = {
    DRAFT: '草稿', VALIDATING: '校验中', PROJECTING: '构建投影', READY: '可激活',
    ACTIVE: '活动版本', BLOCKED: '已阻断', SUPERSEDED: '已替代',
  }
  return labels[status]
}

function projectionLabel(target: string) {
  const labels: Record<string, string> = {
    EXECUTION_SEMANTIC_LAYER: '执行语义层', POSTGRES_REGISTRY: 'PostgreSQL Registry',
    SEARCH_INDEX: 'BM25 / 向量索引', NEBULA_GRAPH: 'NebulaGraph',
  }
  return labels[target] ?? target
}

function AssetDetailDrawer({
  item, release, onClose,
}: {
  item: SemanticReleaseObject
  release: SemanticRelease
  onClose: () => void
}) {
  const meta = objectTypeMeta[item.objectType]
  const references = contractReferences(item.contract)
  const mappingFields = ['aliases', 'positiveAliases', 'negativeAliases', 'hardNegativeExamples', 'question', 'pattern']
    .filter(field => item.contract[field] != null)
  const executionFields = ['formula', 'expression', 'aggregation', 'grain', 'source', 'freshness', 'cardinality', 'fanoutPolicy', 'defaultTimeDimensionId']
    .filter(field => item.contract[field] != null)
  return <div className="asset-detail-backdrop" role="presentation" onMouseDown={event => {
    if (event.target === event.currentTarget) onClose()
  }}>
    <aside className="asset-detail-drawer" role="dialog" aria-modal="true" aria-labelledby="asset-detail-title">
      <header>
        <div><span className="eyebrow">{meta.label}</span><h2 id="asset-detail-title">{contractTitle(item)}</h2><code>{item.objectType.toLowerCase()}:{item.objectId}:v{item.objectVersion}</code></div>
        <button type="button" aria-label="关闭资产详情" onClick={onClose}><XIcon size={18} /></button>
      </header>
      <div className="asset-detail-scroll">
        <section>
          <header><span>01</span><div><strong>概览</strong><small>统一资产信封</small></div></header>
          <dl className="asset-detail-facts">
            <div><dt>稳定 ID</dt><dd>{item.objectId}</dd></div><div><dt>对象版本</dt><dd>{item.objectVersion}</dd></div>
            <div><dt>语义版本</dt><dd>{release.semanticVersion}</dd></div><div><dt>状态</dt><dd>{releaseStatusLabel(release.status)} · {item.certification}</dd></div>
            <div><dt>Owner</dt><dd>{item.ownerId}</dd></div><div><dt>主题域</dt><dd>{item.domainId || '全局'}</dd></div>
            <div><dt>敏感等级</dt><dd>{item.sensitivity}</dd></div><div><dt>生效时间</dt><dd>{formatDateTime(item.validFrom)}</dd></div>
          </dl>
        </section>
        <section>
          <header><span>02</span><div><strong>业务定义</strong><small>适用范围与类型化合同</small></div></header>
          <p>{typeof item.contract.description === 'string' ? item.contract.description : meta.description}</p>
          <div className="asset-contract-grid">{Object.entries(item.contract).map(([field, value]) => <div key={field}><span>{fieldLabels[field] ?? field}</span><strong>{valueText(value)}</strong></div>)}</div>
        </section>
        <section>
          <header><span>03</span><div><strong>运行时合同</strong><small>在哪一步使用、缺失时怎样失败</small></div></header>
          <p>{meta.runtimeUse}</p>
          <div className="asset-required-fields"><span>发布必填</span>{meta.requiredFields.map(field => <code className={item.contract[field] == null ? 'missing' : ''} key={field}>{fieldLabels[field] ?? field}</code>)}</div>
          <p className="asset-detail-note"><LockKeyIcon size={15} />关键字段或引用不完整时只能保留草稿，不得进入问数运行时。</p>
        </section>
        <section>
          <header><span>04</span><div><strong>关系与依赖</strong><small>稳定对象引用与图投影来源</small></div></header>
          {references.length ? <div className="asset-reference-list">{references.map(reference => <div key={reference.field}><span>{fieldLabels[reference.field] ?? reference.field}</span>{reference.values.map(value => <code key={value}>{value}</code>)}</div>)}</div> : <p>该合同没有显式下游对象引用。</p>}
        </section>
        <section>
          <header><span>05</span><div><strong>映射与问法</strong><small>别名、值映射、认证问句和反例</small></div></header>
          {mappingFields.length ? <div className="asset-contract-grid">{mappingFields.map(field => <div key={field}><span>{fieldLabels[field] ?? field}</span><strong>{valueText(item.contract[field])}</strong></div>)}</div> : <p>当前版本没有发布映射或问法字段。</p>}
        </section>
        <section>
          <header><span>06</span><div><strong>执行映射</strong><small>按权限展示公式、来源、关系与时间</small></div></header>
          {executionFields.length ? <div className="asset-contract-grid">{executionFields.map(field => <div key={field}><span>{fieldLabels[field] ?? field}</span><strong>{valueText(item.contract[field])}</strong></div>)}</div> : <p>该对象不直接携带执行映射。</p>}
        </section>
        <section>
          <header><span>07</span><div><strong>治理状态</strong><small>认证、权限、质量与新鲜度</small></div></header>
          <div className="asset-governance-strip"><span className="ready"><CheckCircleIcon />{item.certification}</span><span><ShieldCheckIcon />{item.sensitivity}</span><span><ClockIcon />{item.validTo ? `有效至 ${formatDateTime(item.validTo)}` : '持续生效'}</span></div>
        </section>
        <section>
          <header><span>08</span><div><strong>准确率与覆盖</strong><small>不以使用量代替正式准确率</small></div></header>
          <p>对象级 Top1、误绑率和问句覆盖需由黄金评测与线上抽检产生；当前详情只证明该合同已进入固定发布版本，不宣称单对象达到 95%。</p>
        </section>
        <section>
          <header><span>09</span><div><strong>版本与审计</strong><small>不可变版本与回滚点</small></div></header>
          <dl className="asset-detail-facts"><div><dt>对象 Hash</dt><dd title={item.contentHash}>{compactHash(item.contentHash)}</dd></div><div><dt>发布 Hash</dt><dd title={release.contentHash}>{compactHash(release.contentHash)}</dd></div><div><dt>创建时间</dt><dd>{formatDateTime(item.createdAt)}</dd></div><div><dt>发布记录</dt><dd>{release.semanticVersion} · V{release.version}</dd></div></dl>
        </section>
      </div>
      <footer>
        <button className="quiet-button" type="button" onClick={onClose}>关闭</button>
        {meta.editorHref && <Link className="primary-button" to={meta.editorHref}>进入类型化编辑器<ArrowRightIcon /></Link>}
      </footer>
    </aside>
  </div>
}

function CatalogDirectory({
  catalog, objects, release, selectedType, onType, query, onQuery, onSelect,
}: {
  catalog: Exclude<CatalogKey, 'overview' | 'runtime'>
  objects: SemanticReleaseObject[]
  release?: SemanticRelease
  selectedType: SemanticReleaseObjectType | ''
  onType: (value: SemanticReleaseObjectType | '') => void
  query: string
  onQuery: (value: string) => void
  onSelect: (value: SemanticReleaseObject) => void
}) {
  const definition = catalogDefinitions[catalog]
  const types = objectTypeOrder.filter(type => objectTypeMeta[type].catalogs.includes(catalog))
  const filtered = objects.filter(item => {
    if (!objectTypeMeta[item.objectType].catalogs.includes(catalog)) return false
    if (selectedType && item.objectType !== selectedType) return false
    const search = query.trim().toLocaleLowerCase('zh-CN')
    return !search || [contractTitle(item), item.objectId, item.objectType, item.domainId]
      .some(value => value?.toLocaleLowerCase('zh-CN').includes(search))
  })
  return <section className={`asset-catalog-view ${definition.accent}`}>
    <header className="asset-section-heading"><div><span className="eyebrow">{definition.eyebrow}</span><h2>{definition.title}</h2><p>{definition.description}</p></div><span>{formatCount(filtered.length)} 个当前版本对象</span></header>
    <div className="asset-catalog-toolbar">
      <label><MagnifyingGlassIcon size={16} /><input aria-label={`搜索${definition.title}`} value={query} placeholder="搜索名称、稳定 ID 或主题域" onChange={event => onQuery(event.target.value)} /></label>
      <button className={!selectedType ? 'active' : ''} type="button" onClick={() => onType('')}>全部</button>
      {types.map(type => <button className={selectedType === type ? 'active' : ''} type="button" key={type} onClick={() => onType(type)}>{objectTypeMeta[type].shortLabel}<span>{objects.filter(item => item.objectType === type).length}</span></button>)}
    </div>
    {!release ? <div className="asset-empty-state"><StackIcon size={30} /><strong>尚无可读取的语义发布包</strong><p>先在发布治理中从当前已发布原生资产生成草稿，校验并完成四类投影。</p></div> : filtered.length ? <div className="asset-registry-table" role="table" aria-label={`${definition.title}目录`}>
      <div className="head" role="row"><span>资产</span><span>类型 / 主题域</span><span>版本</span><span>Owner</span><span>治理状态</span><span>运行时</span></div>
      {filtered.map(item => <button type="button" role="row" key={item.id} onClick={() => onSelect(item)}>
        <span><b>{contractTitle(item)}</b><code>{item.objectId}</code></span>
        <span><b>{objectTypeMeta[item.objectType].shortLabel}</b><small>{item.domainId || '全局'}</small></span>
        <span><b>V{item.objectVersion}</b><small title={item.contentHash}>{compactHash(item.contentHash)}</small></span>
        <span><b title={item.ownerId}>{compactHash(item.ownerId)}</b><small>{formatDateTime(item.createdAt)}</small></span>
        <span><em className="certified"><CheckCircleIcon size={12} weight="fill" />{item.certification}</em><small>{item.sensitivity}</small></span>
        <span><em className={release.status === 'ACTIVE' ? 'active' : 'draft'}>{release.status === 'ACTIVE' ? '线上读取' : '尚未激活'}</em><ArrowRightIcon size={14} /></span>
      </button>)}
    </div> : <div className="asset-empty-state"><MagnifyingGlassIcon size={30} /><strong>当前筛选没有资产</strong><p>清除搜索或切换资产类型；目录不会用其他版本对象填充结果。</p></div>}
  </section>
}

function BootstrapReleaseDialog({
  onClose, onCreated,
}: {
  onClose: () => void
  onCreated: (release: SemanticRelease) => void
}) {
  const date = new Date().toISOString().slice(0, 10).replaceAll('-', '.')
  const [input, setInput] = useState<BootstrapSemanticReleaseInput>({
    semanticVersion: `upgrade-${date}-v1`, defaultTimezone: 'Asia/Shanghai',
    defaultCalendar: 'GREGORIAN', completePeriodPolicy: 'EXCLUDE_INCOMPLETE',
    notes: '从当前已发布原生资产生成 v1.5 类型化语义草稿',
  })
  const [preview, setPreview] = useState<BootstrapSemanticReleasePreview>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function previewRelease(event: FormEvent) {
    event.preventDefault()
    setBusy(true); setError('')
    try { setPreview(await semanticAssetAPI.previewBootstrapRelease(input)) }
    catch (cause) { setError(cause instanceof Error ? cause.message : '升级草稿预检失败') }
    finally { setBusy(false) }
  }

  async function createRelease() {
    if (!preview?.eligible || busy) return
    setBusy(true); setError('')
    try { onCreated(await semanticAssetAPI.bootstrapRelease(input)) }
    catch (cause) { setError(cause instanceof Error ? cause.message : '升级草稿创建失败') }
    finally { setBusy(false) }
  }

  return <div className="asset-detail-backdrop" role="presentation" onMouseDown={event => {
    if (event.target === event.currentTarget && !busy) onClose()
  }}><form className="asset-bootstrap-dialog" role="dialog" aria-modal="true" aria-labelledby="asset-bootstrap-title" onSubmit={previewRelease}>
    <header><div><span className="eyebrow">Legacy to v1.5</span><h2 id="asset-bootstrap-title">生成类型化语义升级草稿</h2><p>只读取当前已发布且可证明的原生指标、维度、成员、数据集和权限，不会直接激活线上版本。</p></div><button type="button" aria-label="关闭" disabled={busy} onClick={onClose}><XIcon /></button></header>
    <div className="asset-bootstrap-fields">
      <label>语义版本<input required minLength={3} maxLength={128} value={input.semanticVersion} onChange={event => { setInput({ ...input, semanticVersion: event.target.value }); setPreview(undefined) }} /></label>
      <label>默认时区<input required value={input.defaultTimezone} onChange={event => { setInput({ ...input, defaultTimezone: event.target.value }); setPreview(undefined) }} /></label>
      <label>日历<select value={input.defaultCalendar} onChange={event => { setInput({ ...input, defaultCalendar: event.target.value as BootstrapSemanticReleaseInput['defaultCalendar'] }); setPreview(undefined) }}><option value="GREGORIAN">自然日历</option><option value="ISO_WEEK">ISO 周</option><option value="FISCAL">财务日历</option></select></label>
      <label>完整周期<select value={input.completePeriodPolicy} onChange={event => { setInput({ ...input, completePeriodPolicy: event.target.value as BootstrapSemanticReleaseInput['completePeriodPolicy'] }); setPreview(undefined) }}><option value="EXCLUDE_INCOMPLETE">排除未完整周期</option><option value="INCLUDE_INCOMPLETE">允许未完整周期</option><option value="NOT_APPLICABLE">不适用</option></select></label>
      <label className="wide">变更说明<textarea rows={3} maxLength={4096} value={input.notes} onChange={event => { setInput({ ...input, notes: event.target.value }); setPreview(undefined) }} /></label>
    </div>
    {preview && <section className={`asset-bootstrap-preview ${preview.eligible ? 'eligible' : 'blocked'}`}><header><strong>{preview.eligible ? '预检通过，可创建草稿' : '预检存在阻断项'}</strong><span>{preview.candidateCount} 个候选对象</span></header><div>{Object.entries(preview.sourceCounts).map(([key, value]) => <span key={key}>{key} <b>{value}</b></span>)}</div>{preview.issues.length > 0 && <ul>{preview.issues.map((issue, index) => <li className={issue.severity.toLowerCase()} key={`${issue.code}-${index}`}><b>{issue.severity}</b><span>{issue.message}</span><code>{issue.objectType || 'GLOBAL'} {issue.objectId || ''}</code></li>)}</ul>}</section>}
    {error && <p className="asset-action-error" role="alert">{error}</p>}
    <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="quiet-button" type="submit" disabled={busy}>{busy ? '正在检查…' : '预检当前资产'}</button><button className="primary-button" type="button" disabled={busy || !preview?.eligible} onClick={() => void createRelease()}>{busy ? '正在处理…' : '创建不可变草稿'}</button></footer>
  </form></div>
}

export function AssetOverviewPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const catalogParam = searchParams.get('catalog')
  const activeCatalog: CatalogKey = isCatalogKey(catalogParam) ? catalogParam : 'overview'
  const [readiness, setReadiness] = useState<SemanticCatalogReadiness>()
  const [activeRelease, setActiveRelease] = useState<SemanticReleaseState>()
  const [releases, setReleases] = useState<SemanticRelease[]>([])
  const [release, setRelease] = useState<SemanticRelease>()
  const [selectedReleaseId, setSelectedReleaseId] = useState('')
  const [nativeObjects, setNativeObjects] = useState<SemanticCatalogObject[]>([])
  const [settings, setSettings] = useState<SemanticQASettings>()
  const [canManage, setCanManage] = useState(false)
  const [loading, setLoading] = useState(true)
  const [releaseLoading, setReleaseLoading] = useState(false)
  const [error, setError] = useState('')
  const [actionError, setActionError] = useState('')
  const [busy, setBusy] = useState(false)
  const [reloadToken, setReloadToken] = useState(0)
  const [selectedType, setSelectedType] = useState<SemanticReleaseObjectType | ''>('')
  const [query, setQuery] = useState('')
  const [selectedObject, setSelectedObject] = useState<SemanticReleaseObject>()
  const [bootstrapOpen, setBootstrapOpen] = useState(false)
  const [evaluationSetId, setEvaluationSetId] = useState('')

  useEffect(() => {
    let active = true
    async function loadOverview() {
      await Promise.resolve()
      if (!active) return
      setLoading(true); setError('')
      const results = await Promise.allSettled([
        semanticAssetAPI.readiness(), semanticAssetAPI.activeRelease(),
        semanticAssetAPI.releases({ limit: 24 }), semanticAssetAPI.catalog({ limit: 200 }),
        semanticChatAPI.settings(), semanticAssetAPI.evaluatePermission('MANAGE'),
      ])
      if (!active) return
      const [readinessResult, activeResult, releasesResult, nativeResult, settingsResult, permissionResult] = results
      if (readinessResult.status === 'fulfilled') setReadiness(readinessResult.value)
      if (activeResult.status === 'fulfilled') setActiveRelease(activeResult.value)
      if (releasesResult.status === 'fulfilled') setReleases(releasesResult.value.items)
      if (nativeResult.status === 'fulfilled') setNativeObjects(nativeResult.value.items)
      if (settingsResult.status === 'fulfilled') setSettings(settingsResult.value)
      if (permissionResult.status === 'fulfilled') setCanManage(permissionResult.value.allowed)
      const nextReleaseId = activeResult.status === 'fulfilled' && activeResult.value.activeReleaseId
        ? activeResult.value.activeReleaseId
        : releasesResult.status === 'fulfilled' ? releasesResult.value.items[0]?.id ?? '' : ''
      setSelectedReleaseId(current => current || nextReleaseId)
      const failed = results.slice(0, 4).find(result => result.status === 'rejected')
      if (failed?.status === 'rejected') setError(failed.reason instanceof Error ? failed.reason.message : '资产中心加载失败')
      setLoading(false)
    }
    void loadOverview()
    return () => { active = false }
  }, [reloadToken])

  useEffect(() => {
    let active = true
    async function loadRelease() {
      if (!selectedReleaseId) return
      await Promise.resolve()
      if (!active) return
      setReleaseLoading(true); setActionError('')
      try {
        const item = await semanticAssetAPI.release(selectedReleaseId)
        if (active) setRelease(item)
      } catch (cause) {
        if (active) setActionError(cause instanceof Error ? cause.message : '发布包详情加载失败')
      } finally {
        if (active) setReleaseLoading(false)
      }
    }
    void loadRelease()
    return () => { active = false }
  }, [selectedReleaseId, reloadToken])

  const objects = useMemo(() => release?.objects ?? [], [release?.objects])
  const counts = useMemo(() => Object.fromEntries(
    (Object.keys(catalogDefinitions) as Exclude<CatalogKey, 'overview'>[]).map(catalog => [
      catalog,
      catalog === 'runtime' ? 0 : objects.filter(item => objectTypeMeta[item.objectType].catalogs.includes(catalog)).length,
    ]),
  ) as Record<Exclude<CatalogKey, 'overview'>, number>, [objects])
  const p0Ready = [...p0PackageTypes].filter(type => objects.some(item => item.objectType === type)).length
  const nativeGaps = nativeObjects.filter(item => !item.executionEligible)
  const validation = validationOf(release)

  const chooseCatalog = (catalog: CatalogKey) => {
    const next = new URLSearchParams(searchParams)
    if (catalog === 'overview') next.delete('catalog'); else next.set('catalog', catalog)
    setSearchParams(next)
    setSelectedType(''); setQuery(''); setSelectedObject(undefined)
  }

  async function validateRelease() {
    if (!release || busy) return
    setBusy(true); setActionError('')
    try {
      const updated = await semanticAssetAPI.validateRelease(release.id, release.version)
      setRelease(updated); setReloadToken(value => value + 1)
    } catch (cause) { setActionError(cause instanceof Error ? cause.message : '发布包校验失败') }
    finally { setBusy(false) }
  }

  async function activateRelease() {
    if (!release || !activeRelease || busy) return
    setBusy(true); setActionError('')
    try {
      await semanticAssetAPI.activateRelease(
        release.id, release.version, activeRelease.version,
        activeRelease.activeReleaseId ? evaluationSetId.trim() : '',
      )
      setReloadToken(value => value + 1)
    } catch (cause) { setActionError(cause instanceof Error ? cause.message : '语义版本激活失败') }
    finally { setBusy(false) }
  }

  const renderOverview = () => <section className="asset-center-overview">
    <header className="asset-center-hero">
      <div><span className="eyebrow">Runtime asset registry · v1.5</span><h2>把业务口径、执行语义和治理证据放进同一个版本</h2><p>这里展示的是问数运行时实际读取的不可变资产清单。单次 Intent、QueryPlan、SQL、结果和 ToolCall 属于运行记录，不在资产中心维护。</p><div className="asset-center-hero-actions"><button className="primary-button" type="button" onClick={() => chooseCatalog('business')}>浏览业务语义资产<ArrowRightIcon /></button>{canManage && <button className="quiet-button" type="button" onClick={() => setBootstrapOpen(true)}>生成升级草稿</button>}</div></div>
      <section className={`asset-release-summary ${release?.status.toLowerCase() ?? 'empty'}`}><header><span>{release?.status === 'ACTIVE' ? '当前线上语义版本' : '当前查看版本'}</span><em>{release ? releaseStatusLabel(release.status) : '尚无发布包'}</em></header><strong>{release?.semanticVersion || '等待创建'}</strong><code title={release?.contentHash}>{compactHash(release?.contentHash)}</code><dl><div><dt>对象</dt><dd>{release?.objectCount ?? 0}</dd></div><div><dt>P0 类型</dt><dd>{p0Ready}/{p0PackageTypes.size}</dd></div><div><dt>四投影</dt><dd>{release?.projections.filter(item => item.status === 'READY').length ?? 0}/4</dd></div></dl></section>
    </header>
    <section className="asset-center-kpis" aria-label="资产运行状态">
      <article><FunctionIcon size={19} weight="duotone" /><span>当前版本资产</span><strong>{formatCount(objects.length)}</strong><small>{new Set(objects.map(item => item.objectType)).size} 类类型化对象</small></article>
      <article><ShieldCheckIcon size={19} weight="duotone" /><span>认证对象</span><strong>{formatCount(objects.filter(item => item.certification === 'CERTIFIED').length)}</strong><small>活动版本只接受认证合同</small></article>
      <article><GraphIcon size={19} weight="duotone" /><span>NebulaGraph</span><strong>{readiness?.graph.status || '—'}</strong><small>{formatCount(readiness?.graph.nodeCount ?? 0)} 点 · {formatCount(readiness?.graph.edgeCount ?? 0)} 边</small></article>
      <article><WarningCircleIcon size={19} weight="duotone" /><span>原生待治理</span><strong>{formatCount(nativeGaps.length)}</strong><small>不会自动混入活动发布</small></article>
    </section>
    <section className="asset-five-catalogs"><header className="asset-section-heading"><div><span className="eyebrow">Five runtime catalogs</span><h2>五类运行时核心资产</h2><p>目录按“业务是什么、怎样查询、怎样理解、能否使用、如何受控”组织，不再按旧技术页面拆散。</p></div></header><div>{(Object.keys(catalogDefinitions) as Exclude<CatalogKey, 'overview'>[]).map(catalog => {
      const definition = catalogDefinitions[catalog]
      return <button className={definition.accent} type="button" key={catalog} onClick={() => chooseCatalog(catalog)}><span>{catalogIcons[catalog]}</span><div><small>{definition.eyebrow}</small><strong>{definition.title}</strong><p>{definition.description}</p></div><footer>{catalog === 'runtime' ? '平台控制面' : `${counts[catalog]} 个对象`}<ArrowRightIcon /></footer></button>
    })}</div></section>
    <section className="asset-overview-columns">
      <article className="asset-readiness-panel"><header><div><span className="eyebrow">Runtime gate</span><h3>问数就绪门禁</h3></div><span className={readiness?.questionEnabled ? 'ready' : 'blocked'}>{readiness?.questionEnabled ? '允许运行' : '阻断运行'}</span></header><ol>{(readiness?.checks ?? []).map(check => <li className={check.status.toLowerCase()} key={check.code}>{check.status === 'PASS' ? <CheckCircleIcon weight="fill" /> : <WarningCircleIcon weight="fill" />}<span><strong>{check.label}</strong><small>{check.detail}</small></span></li>)}</ol></article>
      <article className="asset-projection-panel"><header><div><span className="eyebrow">Derived projections</span><h3>可重建投影</h3></div><span>{release?.projections.filter(item => item.status === 'READY').length ?? 0}/4 READY</span></header><div>{release?.projections.map(projection => <section className={projection.status.toLowerCase()} key={projection.id}><span>{projectionLabel(projection.target)}</span><strong>{projection.status}</strong><small>{projection.resourceVersion || projection.errorCode || '等待构建'}</small><i><b style={{ width: projection.status === 'READY' ? '100%' : projection.status === 'RUNNING' ? '65%' : '18%' }} /></i></section>)}</div><p><InfoIcon size={15} />用户维护的是上游资产；向量、倒排索引和图点边不可在此手工修改。</p></article>
    </section>
    {nativeGaps.length > 0 && <section className="asset-native-gaps"><header className="asset-section-heading"><div><span className="eyebrow">Authoring backlog</span><h2>原生资产待治理项</h2><p>这些对象仍在类型化草稿或投影未完成状态，不会因为名称相似而进入当前运行版本。</p></div><span>{nativeGaps.length} 项</span></header><div>{nativeGaps.slice(0, 8).map(item => <article key={`${item.objectType}-${item.id}`}><WarningCircleIcon /><span><strong>{item.name}</strong><small>{item.objectType} · {item.code}</small></span><em>{item.readinessCode}</em></article>)}</div></section>}
  </section>

  const renderGovernance = () => <>
    <CatalogDirectory catalog="governance" objects={objects} release={release} selectedType={selectedType} onType={setSelectedType} query={query} onQuery={setQuery} onSelect={setSelectedObject} />
    <section className="asset-release-pipeline"><header className="asset-section-heading"><div><span className="eyebrow">Immutable release pipeline</span><h2>校验、投影、评测与原子激活</h2><p>线上活动资产不能直接修改；所有变更先形成不可变版本，再通过依赖、冲突、四投影和黄金问题门禁。</p></div>{release && <span className={release.status.toLowerCase()}>{releaseStatusLabel(release.status)}</span>}</header>
      {actionError && <p className="asset-action-error" role="alert">{actionError}</p>}
      <ol className="asset-release-steps"><li className={release ? 'done' : ''}><span>01</span><div><strong>创建版本草稿</strong><small>固定对象清单、Owner、生效期和 content hash</small></div></li><li className={release && release.status !== 'DRAFT' ? 'done' : ''}><span>02</span><div><strong>合同与关系校验</strong><small>必填、引用、Alias 冲突、fanout 和完整 P0 包</small></div></li><li className={release?.projections.every(item => item.status === 'READY') ? 'done' : ''}><span>03</span><div><strong>构建四类投影</strong><small>执行语义层、Registry、检索索引、NebulaGraph</small></div></li><li className={release?.evaluationSetId ? 'done' : ''}><span>04</span><div><strong>黄金问题门禁</strong><small>sealed E2E、Wilson、P0、安全、覆盖率与拒答精确率</small></div></li><li className={release?.status === 'ACTIVE' ? 'done' : ''}><span>05</span><div><strong>原子激活 / 可回滚</strong><small>所有投影 hash 一致后切换活动指针</small></div></li></ol>
      <div className="asset-release-actions">{release && (release.status === 'DRAFT' || release.status === 'BLOCKED') && canManage && <button className="primary-button" type="button" disabled={busy} onClick={() => void validateRelease()}>{busy ? '正在校验…' : '执行类型化合同校验'}</button>}{release?.status === 'READY' && canManage && <><label>sealed 评测集 ID（首次基线可留空）<input value={evaluationSetId} placeholder="已有活动版本时必填" onChange={event => setEvaluationSetId(event.target.value)} /></label><button className="primary-button" type="button" disabled={busy || Boolean(activeRelease?.activeReleaseId && !evaluationSetId.trim())} onClick={() => void activateRelease()}>{busy ? '正在激活…' : '通过门禁并原子激活'}</button></>}</div>
      {validation && <details className="asset-release-validation" open={validation.issues.length > 0}><summary>最近校验结果 · {validation.status || '未知'} · {validation.issues.length} 个问题</summary>{validation.issues.length > 0 ? <ul>{validation.issues.map((issue, index) => <li key={`${issue.code}-${index}`}><code>{String(issue.code ?? 'VALIDATION')}</code><span>{String(issue.message ?? '')}</span><small>{String(issue.objectType ?? '')} {String(issue.objectId ?? '')}</small></li>)}</ul> : <p>当前发布包没有记录校验问题。</p>}</details>}
      <div className="asset-release-history">{releases.map(item => <button type="button" className={`${item.status.toLowerCase()} ${item.id === selectedReleaseId ? 'selected' : ''}`} key={item.id} onClick={() => setSelectedReleaseId(item.id)}><span><strong>{item.semanticVersion}</strong><small>{formatDateTime(item.updatedAt)}</small></span><em>{releaseStatusLabel(item.status)}</em><code>{compactHash(item.contentHash)}</code></button>)}</div>
    </section>
  </>

  const renderRuntime = () => <section className="asset-runtime-view"><header className="asset-section-heading"><div><span className="eyebrow">Controlled runtime configuration</span><h2>系统运行配置</h2><p>这里只展示可安全公开的实际运行配置和健康状态；模型密钥、Prompt 正文和内部策略参数不会下发浏览器。</p></div><span>{settings?.enabled ? '问答已启用' : '问答已关闭'}</span></header>
    <div className="asset-runtime-grid">
      <article><header><PlugsConnectedIcon /><div><strong>问答路由</strong><small>Routing Policy</small></div><span className={settings?.enabled ? 'ready' : 'blocked'}>{settings?.enabled ? 'ENABLED' : 'DISABLED'}</span></header><dl><div><dt>语义 IR</dt><dd>默认路径</dd></div><div><dt>受治理 Text-to-SQL</dt><dd>未配置可靠方言 AST 时关闭</dd></div><div><dt>澄清 / 拒答</dt><dd>证据不足时启用</dd></div></dl></article>
      <article><header><GraphIcon /><div><strong>语义图策略</strong><small>NebulaGraph projection</small></div><span className={settings?.graphProjectionEnabled ? 'ready' : 'blocked'}>{settings?.graphProjectionEnabled ? 'ENABLED' : 'DISABLED'}</span></header><dl><div><dt>最大关系跳数</dt><dd>{settings?.maximumPathHops ?? '—'}</dd></div><div><dt>图水位</dt><dd>{readiness ? `${readiness.graph.appliedEventVersion}/${readiness.graph.requestedEventVersion}` : '—'}</dd></div><div><dt>活动生成</dt><dd>{readiness?.graph.generationId ? compactHash(readiness.graph.generationId) : '—'}</dd></div></dl></article>
      <article><header><GaugeIcon /><div><strong>置信与变更策略</strong><small>Confidence / Change Policy</small></div><span className="ready">VERSIONED</span></header><dl><div><dt>最小路径置信</dt><dd>{settings ? settings.minimumPathConfidence.toFixed(2) : '—'}</dd></div><div><dt>问答驱动变更</dt><dd>{settings?.questionChangeEnabled ? '允许生成待审 ChangeSet' : '关闭'}</dd></div><div><dt>更新时间</dt><dd>{formatDateTime(settings?.updatedAt)}</dd></div></dl></article>
      <article><header><BracketsCurlyIcon /><div><strong>发布与投影策略</strong><small>Release Policy</small></div><span className={release?.status === 'ACTIVE' ? 'ready' : 'blocked'}>{release?.status ?? 'NO RELEASE'}</span></header><dl><div><dt>语义版本</dt><dd>{release?.semanticVersion || '—'}</dd></div><div><dt>内容 Hash</dt><dd>{compactHash(release?.contentHash)}</dd></div><div><dt>投影一致</dt><dd>{release?.projections.filter(item => item.status === 'READY').length ?? 0}/4</dd></div></dl></article>
    </div>
    <section className="asset-runtime-principles"><header><LockKeyIcon /><div><strong>运行时配置的权限边界</strong><small>Model Profile、Prompt、Tool 参数和 SQL 白名单由平台管理员版本化维护。</small></div></header><ul><li><CheckCircleIcon />模型只能建议类型化动作，不能覆盖身份、语义版本和预算。</li><li><CheckCircleIcon />每次问题响应返回本轮实际 Tool、预算、路由和验证摘要。</li><li><CheckCircleIcon />权限、成本、数据质量失败不能通过模型重试绕过。</li><li><CheckCircleIcon />配置变更必须触发受影响黄金集回归并支持独立回滚。</li></ul></section>
  </section>

  return <AppShell title="资产管理中心" eyebrow="运行时资产 · 版本化治理" actions={<div className="asset-center-top-actions"><select aria-label="选择语义发布版本" value={selectedReleaseId} disabled={!releases.length || releaseLoading} onChange={event => setSelectedReleaseId(event.target.value)}>{releases.length ? releases.map(item => <option value={item.id} key={item.id}>{item.semanticVersion} · {releaseStatusLabel(item.status)}</option>) : <option value="">尚无发布版本</option>}</select><button className="quiet-button" type="button" disabled={loading || releaseLoading} onClick={() => setReloadToken(value => value + 1)}><ArrowClockwiseIcon className={loading || releaseLoading ? 'spinning' : ''} />刷新</button></div>}>
    <section className="asset-center-page">
      <nav className="asset-center-directory" aria-label="运行时资产目录"><button className={activeCatalog === 'overview' ? 'active' : ''} type="button" onClick={() => chooseCatalog('overview')}>{catalogIcons.overview}<span><strong>资产总览</strong><small>版本、门禁与覆盖</small></span></button><i />{(Object.keys(catalogDefinitions) as Exclude<CatalogKey, 'overview'>[]).map(catalog => <button className={`${activeCatalog === catalog ? 'active' : ''} ${catalogDefinitions[catalog].accent}`} type="button" key={catalog} onClick={() => chooseCatalog(catalog)}>{catalogIcons[catalog]}<span><strong>{catalogDefinitions[catalog].title}</strong><small>{catalog === 'runtime' ? '平台控制面' : `${counts[catalog]} 个当前对象`}</small></span></button>)}<footer><TreeStructureIcon /><span><strong>{release?.semanticVersion || '无活动版本'}</strong><small>{release?.status || '等待发布'} · {objects.length} 个对象</small></span></footer></nav>
      <main className="asset-center-content">{error && <div className="asset-action-error" role="alert">{error}</div>}{activeCatalog === 'overview' && renderOverview()}{activeCatalog === 'business' && <CatalogDirectory catalog="business" objects={objects} release={release} selectedType={selectedType} onType={setSelectedType} query={query} onQuery={setQuery} onSelect={setSelectedObject} />}{activeCatalog === 'query' && <CatalogDirectory catalog="query" objects={objects} release={release} selectedType={selectedType} onType={setSelectedType} query={query} onQuery={setQuery} onSelect={setSelectedObject} />}{activeCatalog === 'understanding' && <CatalogDirectory catalog="understanding" objects={objects} release={release} selectedType={selectedType} onType={setSelectedType} query={query} onQuery={setQuery} onSelect={setSelectedObject} />}{activeCatalog === 'governance' && renderGovernance()}{activeCatalog === 'runtime' && renderRuntime()}</main>
    </section>
    {selectedObject && release && <AssetDetailDrawer item={selectedObject} release={release} onClose={() => setSelectedObject(undefined)} />}
    {bootstrapOpen && <BootstrapReleaseDialog onClose={() => setBootstrapOpen(false)} onCreated={item => { setBootstrapOpen(false); setReleases(current => [item, ...current]); setSelectedReleaseId(item.id); setReloadToken(value => value + 1) }} />}
  </AppShell>
}
