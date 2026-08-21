import {
  ArrowDown, ArrowLeft, ArrowUp, ArrowUDownLeft, ArrowUDownRight, BracketsCurly, CaretDown, CaretRight, Check,
  CheckCircle, CirclesFour, Database, DotsThreeVertical, Eye, Funnel, Info, MagicWand,
  NotePencil, PencilSimple, Plus, ShieldCheck, Sparkle, SpinnerGap, Trash, WarningCircle, X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useRef, useState, type DragEvent, type ReactNode } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import '../styles/report.css'
import { RequestError } from '../lib/api'
import {
  reportEditorAPI, type AIPreviewResponse, type DataContextCandidate, type DataContextField,
  type EditorComponent, type EditorOperation, type EditorOperationBundle,
  type DraftExecution, type EditorScope, type ReportBlueprint, type ReportDraft, type ReportStarterTemplate,
} from '../report/api/editor'
import { reportAssetsAPI } from '../report/api/assets'
import type { ReportAsset } from '../report/assets/model'
import { ReportHeader, ReportHeaderChooser } from '../report/render/ReportHeader'
import { ReportPageView } from '../report/render/ReportPageView'
import { InteractionPanel } from '../report/designer/InteractionPanel'
import { EvidencePanel } from '../report/designer/EvidencePanel'
import { DataContextPanel, DefinitionJSONDialog, FilterPanel } from '../report/designer/DataPanels'
import { ComponentPalette } from '../report/designer/ComponentPalette'
import { ComponentBindingEditor } from '../report/designer/ComponentBindingEditor'
import { MetricStatusConfiguration } from '../report/designer/MetricStatusConfiguration'
import {
  editorBindingGroups, editorBindingsValid, emptyManifestIndex, indexManifests, latestComponentManifests, listComponentManifests, minimumSize,
  type ComponentManifest, type ManifestIndex,
} from '../report/render/manifests'
import {
  canvasOf, findBlock, findComponentBlock, orderedPages, orderedSections,
  type Block, type ComponentFilterPolicy, type ComponentOptions, type FieldBinding, type GlobalFilter, type Page, type ReportDefinition, type ReportHeaderStyle, type ReportType, type Section,
} from '../report/render/schema'
import {
  addDataContextOperations, bindingForField, bundle, createFilterOperations, createInteractionOperations,
  createSectionOperations, createSubsectionFrameOperations, defaultBinding, deleteFilterOperations, deleteInteractionOperations, duplicateBlockOperations, layoutOperations,
  findCompatibleTemplateSlot, placeComponentInSlotOperations,
  removeBlockOperations, removeComponentOperations, removeDataContextOperations, renameSectionOperations,
  renameBlockOperations, replaceComponentOperations, sectionReorderOperations, slotLayoutOperations, updateComponentOperations, updateFilterOperations, updateReportSettingsOperations,
  decodePalettePayload, paletteDragType, zoneKindForManifest, zoneKindLabels, zoneReorderOperations,
  type FilterDraft, type FrameworkRequest, type InteractionDraft,
} from '../report/designer/operations'

const reportTypeLabels: Record<ReportType, { name: string; hint: string }> = {
  REPORT: { name: '报告', hint: '分章节的分析文档：图表 + 结论 + 明细，可导出、可定时分发' },
  DASHBOARD: { name: '报表', hint: '以卡片和筛选器为主的看板：一屏多卡片，交互筛选、联动、钻取' },
}

const optionLabels: Record<string, string> = {
  showLegend: '显示图例', showLabel: '显示数值标签', smooth: '平滑曲线', colorPaletteRef: '配色方案', nullPolicy: '空值处理',
  animation: '动画效果', orientation: '方向', topN: '只显示前 N 项', numberFormat: '数字格式', tablePageSize: '每页行数',
  mobileLegendMode: '移动端图例', insightRole: '结论类型', imageAssetId: '图片素材 ID', cardVariant: '卡片版式',
}
const optionEnumLabels: Record<string, string> = {
  ZERO: '按 0 处理', HIDE: '隐藏', GAP: '断开', HORIZONTAL: '横向', VERTICAL: '纵向', VISIBLE: '显示', HIDDEN: '隐藏', SCROLL: '可滚动',
  SUMMARY: '总结', TREND: '趋势', COMPARISON: '对比', ANOMALY: '异常', ACTION: '建议',
  '01': '聚焦式', '02': '组合式', '03': '叙事式',
}
type PartialDecision = 'retain' | 'exclude'

function dateTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(date).replaceAll('/', '-')
}

/**
 * 服务端返回的受治理字段带有权威的角色。旧版本 API 只返回字段名时按命名回退，
 * 使编辑器在滚动升级期间仍可用；正常情况下不会走到这条分支。
 */
function governedFieldDefinitions(candidate?: DataContextCandidate): DataContextField[] {
  if (!candidate) return []
  if (candidate.fieldDefinitions?.length) return candidate.fieldDefinitions
  return candidate.fields.map(code => ({
    code, name: code, canonicalType: '', semanticType: '', aggregation: '',
    role: /(^|_)(amount|quantity|count|revenue|cost|profit|sales|value)($|_)/i.test(code) ? 'MEASURE' : 'DIMENSION',
  }))
}

function bindingResultSummary(manifest: ComponentManifest, fields: DataContextField[], dimensions: FieldBinding[], measures: FieldBinding[]) {
  const fieldName = (code: string) => fields.find(field => field.code === code)?.name || code
  const names = (bindings: FieldBinding[]) => bindings.filter(binding => binding.field).map(binding => fieldName(binding.field))
  const dimensionNames = names(dimensions)
  const primaryNames = names(measures.filter(binding => binding.role !== 'TOOLTIP'))
  const companionNames = names(measures.filter(binding => binding.role === 'TOOLTIP'))
  const primary = primaryNames.length ? `「${primaryNames.join('、')}」` : '待选择的核心数值'
  const scope = dimensionNames.length ? `按「${dimensionNames.join('、')}」` : ''
  if (manifest.type === 'metric-card') {
    return `突出展示${primary}${companionNames.length ? `，并用「${companionNames.join('、')}」说明变化` : ''}`
  }
  if (manifest.type === 'data-table') return `展示${dimensionNames.length ? `「${dimensionNames.join('、')}」和` : ''}${primary}`
  if (manifest.type === 'filter-control') return dimensionNames.length ? `用「${dimensionNames.join('、')}」筛选整份报告` : '选择一个报告筛选条件'
  return `${scope ? scope + '，' : ''}用${manifest.displayName}分析${primary}`
}

function objectName(draft: ReportDraft, id: string) {
  for (const page of draft.definition.pages) {
    if (page.id === id) return page.name
    for (const section of page.sections) {
      if (section.id === id) return section.name
      for (const block of section.blocks) {
        if (block.id === id) return section.name
      }
    }
  }
  return draft.definition.components.find(component => component.id === id)?.options.title || '当前报告对象'
}

function operationTitle(operation: EditorOperation, draft: ReportDraft) {
  const object = objectName(draft, operation.targetId)
  if (operation.op.includes('REORDER') || operation.op === 'BLOCK_MOVE') return `重排${object}`
  if (operation.op === 'DATA_BINDING_UPDATE') return `调整${object}绑定`
  if (operation.op.startsWith('INSIGHT_')) return `生成${object}`
  if (operation.op === 'COMPONENT_UPDATE' || operation.op === 'COMPONENT_REPLACE') return `优化${object}`
  if (operation.op.includes('CREATE')) return `新增${object}`
  if (operation.op.includes('DELETE')) return `移除${object}`
  return `更新${object}`
}

/**
 * 差异详情只呈现服务端返回的真实 Operation 内容：操作类型、目标对象与
 * 载荷键。绝不编造「调整前/调整后」的业务描述——那会把示例文案伪装成
 * AI 的真实修改意图。
 */
function operationDetail(operation: EditorOperation, draft: ReportDraft) {
  const payloadKeys = Object.keys(operation.payload ?? {})
  return {
    op: operation.op,
    target: `${objectName(draft, operation.targetId)}（${operation.targetId}）`,
    payload: payloadKeys.length > 0 ? payloadKeys.join('、') : '无附加载荷',
  }
}

function NewReportCanvas({ name, description, reportType, headerStyle }: { name: string; description: string; reportType: ReportType; headerStyle?: ReportHeaderStyle }) {
  return <div className="report-editor-main report-editor-new-main">
    <nav className="report-editor-outline report-editor-new-outline" aria-label="报告大纲">
      <header><strong>报告大纲</strong></header>
      <div><strong>尚未创建报告草稿</strong></div>
    </nav>
    <main className="report-editor-canvas report-editor-new-canvas">
      <article className="report-editor-paper report-editor-blank-paper" aria-label="空白报告画布">
        <ReportHeader style={headerStyle || '01'} title={name.trim() || '视听产业经营分析报告'}
          description={description.trim() || '多维度洞察业务经营情况，驱动增长决策'}
          meta={[`报告类型：${reportTypeLabels[reportType].name}`, '当前修订：r0', '展示模板预览']} filters={[]} />
      </article>
    </main>
  </div>
}

type NewReportMode = 'blank' | 'template' | 'blueprint' | 'ai' | 'import'

const newReportModeLabels: Record<NewReportMode, string> = { blank: '空白', template: '模板', blueprint: '蓝图配置', ai: 'AI 生成', import: '定义导入' }

function starterBlueprint(candidate: DataContextCandidate, name: string, reportType: ReportType): ReportBlueprint {
  // 服务端按 field code 排序后分别编号 m1/x1；这里使用同一规则生成可立即
  // 提交的手工起始蓝图，作者随后可以继续增删章节与卡片。
  const fields = [...(candidate.fieldDefinitions ?? [])].sort((left, right) => left.code.localeCompare(right.code))
  const measures = fields.filter(field => field.role.toUpperCase() === 'MEASURE')
  const dimensions = fields.filter(field => field.role.toUpperCase() !== 'MEASURE')
  const hasMetric = measures.length > 0
  const hasDimension = dimensions.length > 0
  const kind = hasMetric ? 'KPI' : hasDimension ? 'FILTER' : 'TEXT'
  const dataset = hasMetric || hasDimension ? 'd1' : ''
  return {
    schemaVersion: 'report-blueprint/1.0', title: name.trim() || '未命名智能报告', reportType,
    audience: 'BUSINESS', locale: 'zh-CN', theme: 'corporate-light',
    datasets: [{ ref: 'd1', alias: candidate.dataContext.alias || candidate.name }],
    sections: [{
      ref: 's1', title: '分析概览', question: '当前业务表现如何？', leadIn: false, layout: 'FLOW',
      rows: [{ cards: [{
        ref: 's1c1', kind, dataset, metrics: hasMetric ? ['m1'] : [], dimensions: !hasMetric && hasDimension ? ['x1'] : [],
        methods: hasMetric ? ['CURRENT_VALUE'] : [], narrative: hasMetric, weight: hasMetric ? 1 : 4,
        title: hasMetric ? (measures[0]?.name || '核心指标') : hasDimension ? (dimensions[0]?.name || '筛选条件') : '说明', topN: null,
      }] }],
    }],
    summary: { enabled: false }, recommend: { enabled: false }, style: { tone: 'REPORTING', length: 'MEDIUM' },
  }
}

/**
 * 新建面板：手工蓝图与 AI 蓝图走同一个确定性展开器；空白、模板和定义导入
 * 继续作为兼容入口。所有入口最终都产出同一种 Report Definition。
 */
function NewReportTaskPanel({
  name, reportType, headerStyle, intent, contexts, contextId, contextsLoading, contextsError, templates, templatesLoading, selectedTemplateId, creating, error,
  blueprintText, importText, onName, onReportType, onIntent, onContext, onTemplate, onCreateBlank, onCreateTemplate, onCreateBlueprint, onGenerate,
  onBlueprintText, onImportText, onImport, onHeaderStyle,
}: {
  name: string
  reportType: ReportType
  headerStyle?: ReportHeaderStyle
  intent: string
  contexts: DataContextCandidate[]
  contextId: string
  contextsLoading: boolean
  contextsError: string
  templates: ReportStarterTemplate[]
  templatesLoading: boolean
  selectedTemplateId: string
  creating: '' | NewReportMode
  error: string
  blueprintText: string
  importText: string
  onName: (value: string) => void
  onReportType: (value: ReportType) => void
  onHeaderStyle: (value: ReportHeaderStyle) => void
  onIntent: (value: string) => void
  onBlueprintText: (value: string) => void
  onImportText: (value: string) => void
  onImport: () => void
  onContext: (value: string) => void
  onTemplate: (value: string) => void
  onCreateBlank: () => void
  onCreateTemplate: () => void
  onCreateBlueprint: () => void
  onGenerate: () => void
}) {
  const [mode, setMode] = useState<NewReportMode>('blank')
  const selected = contexts.find(item => item.dataContext.id === contextId)
  const ready = !contextsLoading && !contextsError && contexts.length > 0
  const busy = Boolean(creating)
  const headerReady = reportType !== 'REPORT' || Boolean(headerStyle)
  const primary = {
    blank: { label: '创建空白报告', icon: <NotePencil size={16} />, disabled: !headerReady || !ready || !name.trim(), run: onCreateBlank },
    template: { label: '使用所选模板创建', icon: <CirclesFour size={16} />, disabled: !headerReady || !ready || !selectedTemplateId || !name.trim(), run: onCreateTemplate },
    blueprint: { label: '按蓝图生成报告', icon: <BracketsCurly size={16} />, disabled: !headerReady || !ready || !blueprintText.trim(), run: onCreateBlueprint },
    ai: { label: '让 AI 配置蓝图', icon: <MagicWand size={16} />, disabled: !headerReady || !ready || !intent.trim(), run: onGenerate },
    import: { label: '导入为新草稿', icon: <BracketsCurly size={16} />, disabled: !headerReady || !importText.trim(), run: onImport },
  }[mode]

  return <aside className="report-editor-task-panel report-editor-new-panel">
    <header><div><h2>新建{reportTypeLabels[reportType].name}</h2><span>草稿 r0</span></div><em className={busy ? 'is-pending' : ''}>{busy ? '创建中' : '待创建'}</em></header>

    <div className="report-editor-new-scroll">
      <section className="report-editor-new-form">
        <label>名称
          <input value={name} onChange={event => onName(event.target.value)} placeholder="例如：2026 年 7 月经营月报" maxLength={80} />
        </label>
        <fieldset className="report-editor-type-picker">
          <legend>类型</legend>
          {(Object.keys(reportTypeLabels) as ReportType[]).map(type => <label key={type} className={reportType === type ? 'is-selected' : ''}>
            <input type="radio" name="report-type" value={type} checked={reportType === type} onChange={() => onReportType(type)} />
            <span><strong>{reportTypeLabels[type].name}</strong><small>{reportTypeLabels[type].hint}</small></span>
          </label>)}
        </fieldset>
        {reportType === 'REPORT' && <ReportHeaderChooser value={headerStyle} onChange={onHeaderStyle} compact />}
        {mode !== 'import' && <label>数据来源
          <select aria-label="受治理数据上下文" value={contextId} disabled={!ready} onChange={event => onContext(event.target.value)}>
            {contexts.map(item => <option key={item.dataContext.id} value={item.dataContext.id}>{item.name}</option>)}
          </select>
        </label>}
        {contextsLoading && <p className="report-editor-new-hint"><SpinnerGap className="is-spinning" size={14} />正在读取当前领域可用的已发布数据集…</p>}
        {contextsError && <p className="report-editor-new-hint is-error"><WarningCircle size={14} />{contextsError}</p>}
        {ready && selected && mode !== 'import' && <p className="report-editor-new-hint">
          <Info size={14} />
          <span>{(selected.description || '已发布数据集版本').replace(/[。;；]\s*$/, '')}。可用字段 {selected.fields.length} 个（已按你的列权限裁剪）；进入编辑器后还可以再加数据集。</span>
        </p>}
        {!contextsLoading && !contextsError && contexts.length === 0 && <p className="report-editor-new-hint is-error">
          <WarningCircle size={14} />当前业务领域还没有已发布的数据集版本，请先在「数据集」中发布一个版本。
        </p>}
      </section>

      <div className="report-editor-panel-tabs" role="tablist" aria-label="起步方式">
        {(Object.keys(newReportModeLabels) as NewReportMode[]).map(item => <button key={item} type="button" role="tab" aria-selected={mode === item}
          className={mode === item ? 'is-active' : ''} onClick={() => setMode(item)}>{newReportModeLabels[item]}</button>)}
      </div>

      <section className="report-editor-new-mode">
        {mode === 'blank' && <p className="report-editor-new-hint"><Info size={14} /><span>创建空白草稿，随后在编辑器中添加数据集、卡片、筛选器与联动。不需要模型提供方。</span></p>}
        {mode === 'template' && <div className="report-editor-template-center">
          <header><div><strong>从模板开始</strong><small>直接生成可编辑的真实草稿和受治理数据绑定</small></div><span>{templates.length} 个模板</span></header>
          {templatesLoading && <p className="report-editor-new-hint"><SpinnerGap className="is-spinning" size={14} />正在读取模板…</p>}
          <div>{templates.map(template => <button className={selectedTemplateId === template.id ? 'is-selected' : ''} type="button" key={template.id} onClick={() => onTemplate(template.id)}><span>{template.category}</span><strong>{template.name}</strong><small>{template.description}</small><em>{template.componentCount} 个组件</em></button>)}</div>
        </div>}
        {mode === 'blueprint' && <div className="report-editor-new-actions">
          <article>
            <div><strong>手工配置 Report Blueprint</strong><small>只配置章节、语义卡片和 m/x 短引用；组件、ID 与 24 列坐标由服务端确定性展开。这里不调用模型。</small></div>
            <textarea className="is-code" aria-label="报告蓝图 JSON" value={blueprintText} onChange={event => onBlueprintText(event.target.value)} spellCheck={false} />
          </article>
        </div>}
        {mode === 'ai' && <div className="report-editor-new-actions">
          <article>
            <div><strong>让 AI 配置 Report Blueprint</strong><small>模型只规划章节、语义卡片和受治理的短引用，不接触 SQL、字段名、ID 或坐标；服务端校验并确定性展开后生成草稿。</small></div>
            <textarea aria-label="AI 报告要求" value={intent} onChange={event => onIntent(event.target.value)} placeholder="描述报告目标、受众与期间…" />
          </article>
        </div>}
        {mode === 'import' && <div className="report-editor-new-actions">
          <article>
            <div><strong>从 JSON 导入</strong><small>粘贴一份 Report Definition 1.0（编辑器「定义 JSON」导出的内容）。导入时会分配新的报告 ID 与编码；名称留空则沿用 JSON 中的名称。</small></div>
            <textarea aria-label="报告定义 JSON" value={importText} onChange={event => onImportText(event.target.value)} placeholder='{"schemaVersion":"1.0","metadata":{...},"dataContexts":[...],"pages":[...],"components":[...]}' spellCheck={false} />
          </article>
        </div>}
      </section>
    </div>

    <footer className="report-editor-new-footer">
      {error && <div className="report-editor-new-error" role="alert"><WarningCircle size={15} /><span>{error}</span></div>}
      {headerReady ? <button className="primary-button" type="button" disabled={busy || primary.disabled} onClick={primary.run}>
        {busy ? <SpinnerGap className="is-spinning" size={16} /> : primary.icon}
        <span>{busy ? '正在创建…' : primary.label}</span>
      </button> : <span className="report-editor-new-hint"><Info size={14} />选择上方报告头后即可创建</span>}
    </footer>
  </aside>
}

export type ManualEditResult = {
  options: ComponentOptions
  binding?: { dimensions: FieldBinding[]; measures: FieldBinding[]; filterPolicy?: ComponentFilterPolicy }
  /** 卡片绑定的数据集（报告内的数据上下文）；改绑数据集会随绑定一起写入。 */
  dataContextId?: string
  /** 更换展示类型：先 COMPONENT_REPLACE 到新清单，再写入属性与绑定。 */
  replaceWith?: ComponentManifest
}

type BindingSuggestion = { dimensions: FieldBinding[]; measures: FieldBinding[]; rationale?: string }

type FilterFieldSummary = {
  dataset: string
  field: string
  scope: string
}

/**
 * 把卡片的作者表单翻译成一份可核验的数据合同摘要。这里展示的是当前真实绑定，
 * 而不是模板示例值；未填写的合同角色会明确标成「待配置」。
 */
function BindingMappingSummary({ manifest, dataset, fields, dimensions, measures, filters }: {
  manifest: ComponentManifest
  dataset: string
  fields: DataContextField[]
  dimensions: FieldBinding[]
  measures: FieldBinding[]
  filters: FilterFieldSummary[]
}) {
  const groups = editorBindingGroups(manifest)
  const fieldLabel = (code: string) => {
    const field = fields.find(item => item.code === code)
    return field?.name && field.name !== code ? `${field.name} · ${code}` : code
  }
  const bindingsFor = (kind: 'DIMENSION' | 'MEASURE') => kind === 'DIMENSION' ? dimensions : measures

  return <section className="report-binding-map" aria-label="字段映射总览">
    <header>
      <div><strong>字段映射总览</strong><small>应用后，卡片将严格按以下数据合同查询</small></div>
      <span>{groups.length} 类字段角色</span>
    </header>
    <div className="report-binding-map-row is-dataset">
      <span><Database size={14} />数据集</span>
      <strong>{dataset || '待选择数据集'}</strong>
    </div>
    {groups.map(group => {
      const bindings = bindingsFor(group.kind).filter(binding => group.roles.includes(binding.role))
      return <div className="report-binding-map-row" key={group.id}>
        <span>{group.kind === 'MEASURE' ? '指标字段' : '维度字段'}<small>{group.label}</small></span>
        <div className="report-binding-map-values">
          {bindings.length > 0
            ? bindings.map((binding, index) => <strong key={`${binding.role}-${binding.field}-${index}`} className={binding.field ? '' : 'is-empty'}>
                {binding.field ? fieldLabel(binding.field) : '待配置'}
              </strong>)
            : <strong className="is-empty">{group.min > 0 ? '待配置' : '可选'}</strong>}
        </div>
      </div>
    })}
    <div className="report-binding-map-row is-filter">
      <span><Funnel size={14} />过滤字段<small>报告级 / 卡片级</small></span>
      <div className="report-binding-map-values">
        {filters.length > 0
          ? filters.map((filter, index) => <strong key={`${filter.dataset}-${filter.field}-${filter.scope}-${index}`}>
              {filter.field}<small>{filter.dataset} · {filter.scope}</small>
            </strong>)
          : <strong className="is-empty">未设置（可选）</strong>}
      </div>
    </div>
  </section>
}

/** 只保留组件清单 optionSchema 声明的表现属性；标题/副标题/富文本是所有清单共有的基础项。 */
function pruneOptions(options: ComponentOptions, manifest: ComponentManifest): ComponentOptions {
  const allowed = new Set(['title', 'subtitle', 'richText', ...Object.keys(manifest.optionSchema.properties ?? {})])
  return Object.fromEntries(Object.entries(options).filter(([name, value]) => allowed.has(name) && value !== undefined)) as ComponentOptions
}

/**
 * 属性与数据绑定面板。
 *
 * 可编辑的表现属性由组件清单的 optionSchema 生成，因此面板暴露的每一项都是
 * 渲染器真正会读取的配置；绑定只能使用服务端返回的受治理字段与合同声明的角色。
 * 提交仍走 COMPONENT_UPDATE / DATA_BINDING_UPDATE 受控 Operation。
 */
/**
 * 卡片配置面板（右侧内联）：数据集 → 展示类型 → 指标（度量）与维度 → 过滤字段 → 样式。
 * 保存走 COMPONENT_REPLACE / COMPONENT_UPDATE / DATA_BINDING_UPDATE 受控 Operation。
 */
function CardInspector({ mode, component, manifest: currentManifest, manifests, reportContexts, contextNameOf, fieldsOf, defaultContextId, canAI, onSuggest, busy, error, onClose, onSave, filterPanel, filterCount, filterFields, globalFilters }: {
  mode: 'data' | 'appearance'
  component?: EditorComponent
  manifest?: ComponentManifest
  /** 作用于该卡片的过滤字段面板（由页面注入，避免面板自持草稿状态）。 */
  filterPanel: ReactNode
  /** 当前卡片可见的报告级与卡片级过滤字段数量。 */
  filterCount: number
  /** 当前实际生效的过滤字段，用于和数据集、指标、维度一起形成完整映射。 */
  filterFields: FilterFieldSummary[]
  /** 报告头已经配置的筛选控件；指标状态卡只引用它们，不重复创建控件。 */
  globalFilters: GlobalFilter[]
  /** 可切换的展示类型（组件清单）。 */
  manifests: ComponentManifest[]
  /** 报告内已声明的数据集。 */
  reportContexts: Array<{ id: string; alias?: string }>
  contextNameOf: (dataContextId: string) => string
  fieldsOf: (dataContextId: string) => DataContextField[]
  defaultContextId: string
  canAI: boolean
  /** 让模型在所选数据集里识别适合这张卡片的度量与维度；不可用时返回 null。 */
  onSuggest?: (input: { dataContextId: string; manifest: ComponentManifest; title: string }) => Promise<BindingSuggestion | null>
  busy: boolean
  error: string
  onClose: () => void
  onSave: (result: ManualEditResult) => void
}) {
  const initialContextId = component?.dataBinding?.dataContextId ?? defaultContextId
  const hydrateInitialMetric = (binding: FieldBinding, role = binding.role) => {
    if (currentManifest?.type !== 'analysis-metric-status') return binding
    const field = fieldsOf(initialContextId).find(item => item.code === binding.field)
    return { ...bindingForField(role, field), ...(binding.label?.trim() ? { label: binding.label.trim() } : {}) }
  }
  const [options, setOptions] = useState<ComponentOptions>(() => ({ ...component?.options }))
  const [dimensions, setDimensions] = useState<FieldBinding[]>(component?.dataBinding?.dimensions ?? [])
  const [measures, setMeasures] = useState<FieldBinding[]>(() => {
    const existing = component?.dataBinding?.measures ?? []
    if (currentManifest?.type !== 'analysis-metric-status') return existing
    return existing.slice(0, 3).map((binding, index) => hydrateInitialMetric(binding, index === 0 ? 'VALUE' : 'TOOLTIP'))
  })
  const [dataContextId, setDataContextId] = useState(initialContextId)
  const [filterPolicy, setFilterPolicy] = useState<ComponentFilterPolicy>(() => {
    if (component?.dataBinding?.filterPolicy) return {
      globalMappings: [...component.dataBinding.filterPolicy.globalMappings],
      localFilters: [...component.dataBinding.filterPolicy.localFilters],
    }
    const contextID = initialContextId
    const available = fieldsOf(contextID)
    return {
      globalMappings: globalFilters.filter(filter => filter.scope.type === 'REPORT' && filter.fieldRef.dataContextId === contextID && available.some(field => field.code === filter.fieldRef.field))
        .map(filter => ({ filterId: filter.id, field: filter.fieldRef.field })),
      localFilters: [],
    }
  })
  const [manifestRef, setManifestRef] = useState(currentManifest ? `${currentManifest.type}@${currentManifest.version}` : '')
  const [suggesting, setSuggesting] = useState(false)
  const [suggestion, setSuggestion] = useState<BindingSuggestion | null>(null)
  const [suggestError, setSuggestError] = useState('')

  const manifest = manifests.find(item => `${item.type}@${item.version}` === manifestRef) ?? currentManifest
  const metricStatus = manifest?.type === 'analysis-metric-status'
  const replacing = Boolean(currentManifest && manifest && (manifest.type !== currentManifest.type || manifest.version !== currentManifest.version))
  const fields = fieldsOf(dataContextId)
  const contract = manifest?.dataContract
  // SEMANTIC_IR 绑定固定语义发布版本，只能由问数或 AI 编排产生，面板不得改写。
  const semanticBound = component?.dataBinding?.bindingMode === 'SEMANTIC_IR'
  // 组件库只露出每种组件的最新合同；既有草稿若固定在旧版本，仍保留当前版本供查看。
  const switchableLatest = latestComponentManifests(manifests).filter(item => item.renderer !== 'CONTROL' && item.renderer !== 'IMAGE')
  const switchable = currentManifest && !switchableLatest.some(item => item.type === currentManifest.type && item.version === currentManifest.version)
    ? [currentManifest, ...switchableLatest] : switchableLatest
  const dimensionFields = fields.filter(field => field.role !== 'MEASURE')
  const measureFields = fields.filter(field => field.role === 'MEASURE')
  const bindable = Boolean(contract) && fields.length > 0 && !semanticBound
  const complete = (metricStatus ? [] : dimensions).every(item => item.field && item.role && dimensionFields.some(field => field.code === item.field)) &&
    measures.every(item => item.field && item.role && measureFields.some(field => field.code === item.field))
  const metricMeasuresValid = !metricStatus || (
    measures.filter(item => item.role === 'VALUE').length === 1 && measures.filter(item => item.role === 'TOOLTIP').length <= 2 &&
    measures.every(item => Boolean(item.label?.trim()) && Boolean(item.field) && Boolean(item.aggregation))
  )
  const metricFiltersValid = !metricStatus || (
    filterPolicy.globalMappings.every(mapping => globalFilters.some(filter => filter.id === mapping.filterId && filter.scope.type === 'REPORT') && fields.some(field => field.code === mapping.field)) &&
    filterPolicy.localFilters.every(filter => fields.some(field => field.code === filter.field) && filter.value !== '')
  )
  const bindingValid = !bindable || Boolean(manifest && editorBindingsValid(manifest, metricStatus ? [] : dimensions, measures) && complete && metricMeasuresValid && metricFiltersValid)
  const resultSummary = manifest ? bindingResultSummary(manifest, fields, dimensions, measures) : ''

  // 表现属性直接由清单的 optionSchema 生成，避免面板与渲染器各自维护一份白名单。
  const styleProperties = Object.entries(manifest?.optionSchema.properties ?? {})
    .filter(([name]) => name !== 'title' && name !== 'subtitle' && name !== 'richText')
    .sort(([left], [right]) => left.localeCompare(right))

  const setOption = (name: string, value: unknown) =>
    setOptions(current => ({ ...current, [name]: value }))

  const applyBinding = (next: { dimensions: FieldBinding[]; measures: FieldBinding[] }, contextID = dataContextId, forMetricStatus = metricStatus) => {
    const decorate = (item: FieldBinding) => {
      if (!forMetricStatus) return item
      const governed = fieldsOf(contextID).find(field => field.code === item.field)
      const base = bindingForField(item.role, governed)
      return { ...base, ...(item.label?.trim() ? { label: item.label.trim() } : {}) }
    }
    setDimensions((forMetricStatus ? [] : next.dimensions).filter(item => item.field).map(decorate))
    const nextMeasures = next.measures.filter(item => item.field)
    setMeasures((forMetricStatus ? nextMeasures.slice(0, 3).map((item, index) => ({ ...item, role: index === 0 ? 'VALUE' as const : 'TOOLTIP' as const })) : nextMeasures).map(decorate))
  }
  const changeContext = (nextId: string) => {
    setDataContextId(nextId); setSuggestion(null); setSuggestError('')
    // 换数据集后旧字段不再成立：按新数据集的字段角色重新填充下限绑定。
    if (manifest) applyBinding(defaultBinding(manifest, fieldsOf(nextId)), nextId, manifest.type === 'analysis-metric-status')
    setFilterPolicy({ globalMappings: [], localFilters: [] })
  }
  const changeManifest = (ref: string) => {
    setManifestRef(ref); setSuggestion(null); setSuggestError('')
    const next = manifests.find(item => `${item.type}@${item.version}` === ref)
    if (!next) return
    // 表现属性只保留新清单 optionSchema 认识的键（标题/副标题始终保留），否则服务端会拒绝未知选项。
    setOptions(current => pruneOptions(current, next))
    // 组件类型变化时按它自己的编辑档案重建最小字段集合，避免把旧图形角色带入新图形。
    applyBinding(defaultBinding(next, fields), dataContextId, next.type === 'analysis-metric-status')
    if (next.type === 'analysis-metric-status') setFilterPolicy({ globalMappings: [], localFilters: [] })
  }
  const suggest = async () => {
    if (!onSuggest || !manifest) return
    setSuggesting(true); setSuggestError(''); setSuggestion(null)
    try {
      const result = await onSuggest({ dataContextId, manifest, title: options.title ?? '' })
      if (!result) { setSuggestError('暂未找到合适的智能推荐，请直接从下方选择。'); return }
      setSuggestion(result)
      applyBinding(result)
    } catch (cause) {
      setSuggestError(cause instanceof Error ? cause.message : '智能推荐暂时不可用')
    } finally { setSuggesting(false) }
  }
  const recommend = () => {
    if (canAI && onSuggest) void suggest()
    else if (manifest) applyBinding(defaultBinding(manifest, fields))
  }

  const save = () => onSave({
    options: { ...(manifest ? pruneOptions(options, manifest) : options), title: options.title?.trim(), subtitle: options.subtitle?.trim() },
    binding: bindable ? { dimensions: metricStatus ? [] : dimensions, measures, ...(metricStatus ? { filterPolicy } : component?.dataBinding?.filterPolicy ? { filterPolicy: component.dataBinding.filterPolicy } : {}) } : undefined,
    dataContextId: bindable ? dataContextId : undefined,
    replaceWith: replacing ? manifest : undefined,
  })

  return <section className="report-card-inspector" aria-labelledby="manual-editor-title">
      <header>
        <div className="report-card-inspector-title"><h2 id="manual-editor-title">{mode === 'data' ? '组件配置' : '外观设置'}</h2><span>{manifest?.displayName || component?.options.title || component?.templateRef.type || '画布元素'}</span></div>
        <div className="report-card-inspector-actions">
          <button type="button" aria-label="取消选中" onClick={onClose}><X size={18} /></button>
        </div>
      </header>
      <div className="report-editor-manual-form">
        {mode === 'data' && <>
        <section className="report-profile-source">
          <header className="report-profile-heading"><span className="report-profile-heading-icon"><Database size={16} /></span><div><strong>{metricStatus ? '数据源' : '先确定分析基础'}</strong><small>{metricStatus ? '指标值与局部过滤值均来自所选数据集' : '选择这张图使用的数据和呈现方式'}</small></div></header>
          {!semanticBound && reportContexts.length > 0 && <label>{metricStatus ? '数据集' : '数据来源'}
            <select aria-label="卡片数据集" value={dataContextId} onChange={event => changeContext(event.target.value)}>
              {reportContexts.map(context => <option key={context.id} value={context.id}>{contextNameOf(context.id)}</option>)}
            </select>
          </label>}
          {!semanticBound && switchable.length > 0 && <label>分析方式
            <select aria-label="组件类型" value={manifestRef} onChange={event => changeManifest(event.target.value)}>
              {switchable.map(item => <option key={item.type + '@' + item.version} value={item.type + '@' + item.version}>{item.displayName}</option>)}
            </select>
          </label>}
          <label>卡片标题
            <input value={options.title ?? ''} maxLength={200} placeholder="请输入卡片标题" onChange={event => setOption('title', event.target.value)} />
          </label>
          {replacing && <p className="report-editor-binding-note"><Info size={13} />应用后改为「{manifest?.displayName}」，字段会按新组件合同重新配置。</p>}
        </section>

        {semanticBound && <p className="report-editor-binding-note">
          <ShieldCheck size={15} />该组件使用 SEMANTIC_IR 绑定并固定了语义发布版本，只能通过语义升级流程调整。
        </p>}
        {!semanticBound && !contract && <p className="report-editor-binding-note">
          <Info size={15} />未找到该组件的模板合同，暂不能在此编辑数据绑定。
        </p>}
        {!semanticBound && contract && fields.length === 0 && <p className="report-editor-binding-note">
          <WarningCircle size={15} />当前报告的数据上下文没有可用字段。
        </p>}

        {bindable && contract && manifest && <section className="report-profile-fields">
          <header>
            <div className="report-profile-heading"><span className="report-profile-heading-icon"><CirclesFour size={16} /></span><div><strong>{metricStatus ? '卡片配置' : '再回答几个业务问题'}</strong><small>{metricStatus ? '配置主指标、按需添加辅助指标与过滤项' : '不同图表只显示各自真正需要的内容'}</small></div></div>
            <div className="report-editor-binding-assist">
              <button type="button" disabled={busy || suggesting} title={canAI && onSuggest ? '根据图表用途智能选择合适字段' : '按字段类型生成一套可用配置'} onClick={recommend}>
                {suggesting ? <SpinnerGap className="is-spinning" size={14} /> : <MagicWand size={14} />}{suggesting ? '推荐中…' : '智能推荐'}
              </button>
            </div>
          </header>
          {suggestion && <p className="report-editor-binding-note"><Sparkle size={14} weight="fill" />已生成推荐配置{suggestion.rationale ? `：${suggestion.rationale}` : ''}。点击「应用」后生效。</p>}
          {suggestError && <p className="report-editor-inline-error"><WarningCircle size={15} />{suggestError}</p>}
          {metricStatus ? <MetricStatusConfiguration
            dataContextId={dataContextId} fields={fields} fieldsOf={fieldsOf} measures={measures}
            globalFilters={globalFilters} filterPolicy={filterPolicy}
            onMeasuresChange={setMeasures} onFilterPolicyChange={setFilterPolicy} /> : <>
            <ComponentBindingEditor manifest={manifest} dimensions={dimensions} measures={measures}
              dimensionFields={dimensionFields} measureFields={measureFields}
              onDimensionsChange={setDimensions} onMeasuresChange={setMeasures} />
            <BindingMappingSummary manifest={manifest} dataset={contextNameOf(dataContextId)} fields={fields}
              dimensions={dimensions} measures={measures} filters={filterFields} />
          </>}
          {!bindingValid && <p className="report-editor-inline-error"><WarningCircle size={15} />请完成该组件要求的核心字段配置</p>}
          {!metricStatus && manifest.editorProfile && <section className="report-profile-result">
            <span><Eye size={17} /></span>
            <div><small>配置结果</small><strong>{resultSummary}</strong>
              <p>{manifest.editorProfile.example.description}</p>
              {manifest.editorProfile.example.items.length > 0 && <ul>
                {manifest.editorProfile.example.items.map(item => <li key={item}>{item}</li>)}
              </ul>}
            </div>
          </section>}
        </section>}
        </>}

        {mode === 'appearance' && <>
        <h4 className="report-inspector-group">内容</h4>
        <label>标题<input value={options.title ?? ''} placeholder="卡片标题" onChange={event => setOption('title', event.target.value)} /></label>
        <label>副标题<input value={options.subtitle ?? ''} placeholder="可选" onChange={event => setOption('subtitle', event.target.value)} /></label>
        {manifest?.renderer === 'TEXT' && <label>文字内容
          <textarea rows={4} value={options.richText ?? ''} onChange={event => setOption('richText', event.target.value)} />
        </label>}

        {styleProperties.length > 0 && <div className="report-editor-style-group">
          <h4 className="report-inspector-group">外观</h4>
          {styleProperties.map(([name, schema]) => {
            const value = (options as Record<string, unknown>)[name]
            const label = optionLabels[name] || schema.description || name
            if (schema.type === 'boolean') {
              return <label className="report-editor-style-toggle" key={name}>
                <input type="checkbox" checked={value === true} onChange={event => setOption(name, event.target.checked)} />
                <span>{label}</span>
              </label>
            }
            if (schema.enum?.length) {
              return <label key={name}>{label}
                <select value={String(value ?? '')} onChange={event => setOption(name, event.target.value || undefined)}>
                  <option value="">默认</option>
                  {schema.enum.map(item => <option key={item} value={item}>{optionEnumLabels[item] ?? item}</option>)}
                </select>
              </label>
            }
            if (schema.type === 'integer' || schema.type === 'number') {
              return <label key={name}>{label}
                <input type="number" min={schema.minimum} max={schema.maximum} value={value === undefined ? '' : String(value)}
                  onChange={event => setOption(name, event.target.value === '' ? undefined : Number(event.target.value))} />
              </label>
            }
            return <label key={name}>{label}
              <input value={String(value ?? '')} placeholder="默认" onChange={event => setOption(name, event.target.value || undefined)} />
            </label>
          })}
        </div>}
        </>}
        {mode === 'data' && !metricStatus && filterPanel && <details className="report-inspector-disclosure">
          <summary>
            <span className="report-inspector-disclosure-icon"><Funnel size={16} /></span>
            <span><strong>卡片筛选</strong><small>{filterCount > 0 ? `${filterCount} 个条件已生效` : '可选，仅限制当前图表的数据'}</small></span>
            <CaretDown className="report-inspector-disclosure-caret" size={15} />
          </summary>
          <div className="report-inspector-disclosure-content">{filterPanel}</div>
        </details>}
        {error && <div className="report-editor-inline-error"><WarningCircle size={15} />{error}</div>}
      </div>
      <footer className="report-card-inspector-footer">
        <span>{bindingValid && options.title?.trim() ? '配置完成后统一应用' : '请先完成必填配置'}</span>
        <button className="primary-button report-inspector-apply" type="button" disabled={busy || !options.title?.trim() || !bindingValid} onClick={save}>{busy ? '应用中…' : '应用配置'}</button>
      </footer>
    </section>
}

function ComponentLibraryDialog({ manifests, reportContexts, contextNameOf, fieldsOf, defaultContextId, sectionName, busy, error, onClose, onAdd }: {
  manifests: ComponentManifest[]
  reportContexts: Array<{ id: string; alias?: string }>
  contextNameOf: (dataContextId: string) => string
  fieldsOf: (dataContextId: string) => DataContextField[]
  defaultContextId: string
  sectionName: string
  busy: boolean
  error: string
  onClose: () => void
  onAdd: (manifest: ComponentManifest, title: string, dataContextId: string) => void
}) {
  const [contextId, setContextId] = useState(defaultContextId)
  const fields = fieldsOf(contextId)
  const [selectedRef, setSelectedRef] = useState(() => manifests[0] ? `${manifests[0].type}@${manifests[0].version}` : '')
  const selected = manifests.find(item => `${item.type}@${item.version}` === selectedRef)
  const [title, setTitle] = useState(selected?.displayName || '')
  const needsData = Boolean(selected && (selected.dataContract.dimensions.min > 0 || selected.dataContract.measures.min > 0))
  const enoughFields = Boolean(selected &&
    fields.filter(field => field.role !== 'MEASURE').length >= selected.dataContract.dimensions.min &&
    fields.filter(field => field.role === 'MEASURE').length >= selected.dataContract.measures.min)

  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-component-library" role="dialog" aria-modal="true" aria-labelledby="component-library-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span className="eyebrow">组件库</span><h2 id="component-library-title">添加报告组件</h2></div><button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button></header>
      <div className="report-component-library-body">
        <div className="report-component-grid">
          {manifests.map(manifest => {
            const ref = `${manifest.type}@${manifest.version}`
            return <button className={ref === selectedRef ? 'is-selected' : ''} type="button" key={ref} onClick={() => { setSelectedRef(ref); setTitle(manifest.displayName) }}>
              <span><CirclesFour size={18} /></span><strong>{manifest.displayName}</strong><small>{manifest.category} · {manifest.recommendedSize.w}×{manifest.recommendedSize.h}</small>
            </button>
          })}
        </div>
        <aside className="report-component-config">
          <h3>组件配置</h3>
          <label>组件标题<input value={title} maxLength={80} onChange={event => setTitle(event.target.value)} /></label>
          {needsData && reportContexts.length > 0 && <label>数据集
            <select aria-label="卡片数据集" value={contextId} onChange={event => setContextId(event.target.value)}>
              {reportContexts.map(context => <option key={context.id} value={context.id}>{contextNameOf(context.id)}</option>)}
            </select>
          </label>}
          {selected && <dl>
            <div><dt>模板</dt><dd>{selected.displayName}</dd></div>
            <div><dt>渲染方式</dt><dd>{selected.renderer}</dd></div>
            <div><dt>尺寸</dt><dd>{selected.recommendedSize.w} × {selected.recommendedSize.h}</dd></div>
            <div><dt>数据要求</dt><dd>维度 {selected.dataContract.dimensions.min}～{selected.dataContract.dimensions.max}，度量 {selected.dataContract.measures.min}～{selected.dataContract.measures.max}</dd></div>
            <div><dt>所在章节</dt><dd>{sectionName || '新建章节'}</dd></div>
            <div><dt>目标区域</dt><dd>{zoneKindLabels[zoneKindForManifest(selected)]}</dd></div>
          </dl>}
          <div className="report-component-placement">
            <strong>放置方式</strong>
            <p><Info size={15} />组件会加入当前{sectionName ? `“${sectionName}”` : '章节'}，系统根据类型和推荐尺寸自动寻找画布位置。</p>
          </div>
          {needsData && <p className={enoughFields && contextId ? '' : 'is-error'}><Info size={15} />{enoughFields && contextId ? `将按「${contextNameOf(contextId)}」的字段角色预填 ${selected?.dataContract.dimensions.min ?? 0} 个维度、${selected?.dataContract.measures.min ?? 0} 个度量；加入后点击卡片可改数据集、展示类型与绑定，也可让 AI 识别度量与维度。` : '所选数据集的可用字段不足，无法满足此组件合同。'}</p>}
          {!needsData && <p><Info size={15} />该组件无需数据绑定，可直接加入报告结构。</p>}
          {error && <p className="is-error"><WarningCircle size={15} />{error}</p>}
        </aside>
      </div>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={busy || !selected || !title.trim() || (needsData && (!enoughFields || !contextId))} onClick={() => selected && onAdd(selected, title.trim(), contextId)}>{busy ? '正在添加…' : '添加到画布'}</button></footer>
    </section>
  </div>
}

/**
 * 报告编辑器。
 *
 * 画布与运行页共用 ReportPageView 和同一份 Definition。编辑器把拖拽与缩放
 * 翻译成受控 Operation；发布后渲染器隐藏内部 block / zone / slot，并按组件
 * 类型自动重排为阅读画布。
 */
export function ReportEditorPage() {
  const { reportId: routeReportId } = useParams()
  const location = useLocation()
  const navigate = useNavigate()
  const newMode = location.pathname === '/reports/new'
  const reportId = routeReportId ?? ''
  const [draft, setDraft] = useState<ReportDraft | null>(null)
  const [asset, setAsset] = useState<ReportAsset | null>(null)
  const [execution, setExecution] = useState<DraftExecution | null>(null)
  // 与草稿加载一致：执行中的状态由「已结算的数据签名」推导，避免在 effect 内
  // 同步 setState 触发级联渲染。
  const [settledSignature, setSettledSignature] = useState('')
  const [executionError, setExecutionError] = useState('')
  // 加载状态由已结算的报告 ID 推导，避免在 effect 内同步 setState。
  const [settledReportId, setSettledReportId] = useState('')
  const [loadFailure, setLoadFailure] = useState<{ reportId: string; message: string } | null>(null)
  const loading = !newMode && settledReportId !== reportId
  const loadError = loadFailure?.reportId === reportId ? loadFailure.message : ''
  const [intent, setIntent] = useState('')
  const [newName, setNewName] = useState('')
  const [newReportType, setNewReportType] = useState<ReportType>('REPORT')
  const [newHeaderStyle, setNewHeaderStyle] = useState<ReportHeaderStyle>()
  const [blueprintText, setBlueprintText] = useState('')
  const [importText, setImportText] = useState('')
  const [contexts, setContexts] = useState<DataContextCandidate[]>([])
  const [contextId, setContextId] = useState('')
  const [contextsLoading, setContextsLoading] = useState(newMode)
  const [contextsError, setContextsError] = useState('')
  const [starterTemplates, setStarterTemplates] = useState<ReportStarterTemplate[]>([])
  const [starterTemplatesLoading, setStarterTemplatesLoading] = useState(newMode)
  const [selectedTemplateId, setSelectedTemplateId] = useState('')
  const [newCreating, setNewCreating] = useState<'' | NewReportMode>('')
  const [scopeMode, setScopeMode] = useState<'page' | 'section'>('page')
  const [activeSectionId, setActiveSectionId] = useState('')
  const [selectedComponentId, setSelectedComponentId] = useState('')
  const [selectionInitialized, setSelectionInitialized] = useState(false)
  const [aiPreview, setAIPreview] = useState<AIPreviewResponse | undefined>()
  const [previewing, setPreviewing] = useState(false)
  const [applying, setApplying] = useState(false)
  const [operationSelection, setOperationSelection] = useState<Set<number>>(() => new Set())
  const [expandedOperations, setExpandedOperations] = useState<Set<number>>(() => new Set())
  const [partialDecision, setPartialDecision] = useState<PartialDecision>('retain')
  const [actionError, setActionError] = useState('')
  const [toast, setToast] = useState('')
  const [manifests, setManifests] = useState<ManifestIndex>(emptyManifestIndex)
  const [manualBusy, setManualBusy] = useState(false)
  const [manualError, setManualError] = useState('')
  const [componentLibraryOpen, setComponentLibraryOpen] = useState(false)
  const [componentBusy, setComponentBusy] = useState(false)
  const [componentError, setComponentError] = useState('')
  const [deleteSectionOpen, setDeleteSectionOpen] = useState(false)
  const [interactionBusy, setInteractionBusy] = useState(false)
  const [interactionError, setInteractionError] = useState('')
  const [dataBusy, setDataBusy] = useState(false)
  const [dataError, setDataError] = useState('')
  const [filterBusy, setFilterBusy] = useState(false)
  const [filterError, setFilterError] = useState('')
  const [jsonOpen, setJsonOpen] = useState(false)
  const [sidePanel, setSidePanel] = useState<'ai' | 'data' | 'appearance' | 'interaction'>('data')
  const [reportInspectorView, setReportInspectorView] = useState<'overview' | 'datasets' | 'filters'>('overview')
  const [designFilterValues, setDesignFilterValues] = useState<Record<string, unknown>>({})
  const [editorView, setEditorView] = useState<'edit' | 'preview'>('edit')
  const [editorScale, setEditorScale] = useState(.5)
  const [previewPageCount, setPreviewPageCount] = useState(1)
  const [lastReceipt, setLastReceipt] = useState<{ from: number; to: number; count: number; source: string } | null>(null)
  const [renamingSectionId, setRenamingSectionId] = useState('')
  const [renamingTitle, setRenamingTitle] = useState(false)
  const canvasRef = useRef<HTMLElement>(null)
  const paperRef = useRef<HTMLElement>(null)

  const notify = (message: string) => { setToast(message); window.setTimeout(() => setToast(''), 2600) }

  useEffect(() => {
    if (newMode) return undefined
    let cancelled = false
    void Promise.all([reportEditorAPI.getDraft(reportId), reportAssetsAPI.list({ limit: 100 })])
      .then(async ([nextDraft, assets]) => {
        if (cancelled) return
        setDraft(nextDraft)
        setAsset(assets.items.find(item => item.id === reportId) ?? null)
        const page = orderedPages(nextDraft.definition)[0]
        setActiveSectionId(page ? orderedSections(page)[0]?.id ?? '' : '')
      })
      .catch(cause => {
        if (!cancelled) {
          setLoadFailure({ reportId, message: cause instanceof Error ? cause.message : '报告草稿加载失败' })
        }
      })
      .finally(() => { if (!cancelled) setSettledReportId(reportId) })
    return () => { cancelled = true }
  }, [newMode, reportId])

  // 组件清单同时驱动渲染器、组件库与属性面板，编辑页与运行页读的是同一个注册表。
  useEffect(() => {
    if (newMode) return undefined
    let cancelled = false
    void listComponentManifests()
      .then(result => { if (!cancelled) setManifests(indexManifests(result.items)) })
      .catch(() => { /* 清单不可用时绑定面板降级为只读，不阻塞编辑器加载。 */ })
    return () => { cancelled = true }
  }, [newMode])

  /**
   * 草稿的数据签名：组件绑定与报告级筛选。
   *
   * 拖拽和改标题会改变定义哈希但不会改变查询，因此用签名而不是哈希来决定是否
   * 重新执行——排版调整保持零查询，改绑定则立刻看到真实数据。
   */
  const dataSignature = useMemo(() => JSON.stringify({
    page: draft ? orderedPages(draft.definition)[0]?.id ?? '' : '',
    bindings: draft?.definition.components.map(component => [component.id, component.dataBinding]) ?? [],
    filters: draft?.definition.globalFilters ?? [],
    filterValues: designFilterValues,
  }), [designFilterValues, draft])

  const executing = Boolean(draft) && settledSignature !== dataSignature

  // 按当前草稿执行组件查询。执行按当前用户权限进行，不产生任何版本或制品。
  useEffect(() => {
    if (newMode || !reportId) return undefined
    const page = draft ? orderedPages(draft.definition)[0] : undefined
    if (!page) return undefined
    const controller = new AbortController()
    void reportEditorAPI.executeDraft(reportId, { pageId: page.id, filterValues: designFilterValues }, { signal: controller.signal })
      .then(result => {
        if (controller.signal.aborted) return
        setExecution(result); setExecutionError('')
      })
      .catch(cause => {
        if (controller.signal.aborted) return
        setExecutionError(cause instanceof Error ? cause.message : '草稿预览执行失败')
      })
      .finally(() => { if (!controller.signal.aborted) setSettledSignature(dataSignature) })
    return () => controller.abort()
    // dataSignature 已经涵盖了 draft 中影响查询的全部内容。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataSignature, newMode, reportId])

  // 受治理数据上下文在两种模式下都需要：新建页用于选择数据来源，
  // 编辑页用于给数据绑定面板提供当前用户有权使用的字段列表。
  useEffect(() => {
    let cancelled = false
    void reportEditorAPI.listDataContexts()
      .then(result => {
        if (cancelled) return
        setContexts(result.items)
        setContextId(result.items[0]?.dataContext.id ?? '')
        setContextsError('')
      })
      .catch(cause => {
        if (!cancelled) setContextsError(cause instanceof Error ? cause.message : '受治理数据上下文读取失败')
      })
      .finally(() => { if (!cancelled) setContextsLoading(false) })
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    if (!newMode || blueprintText) return
    const selected = contexts.find(item => item.dataContext.id === contextId)
    if (selected) setBlueprintText(JSON.stringify(starterBlueprint(selected, newName, newReportType), null, 2))
  }, [blueprintText, contextId, contexts, newMode, newName, newReportType])

  useEffect(() => {
    if (!newMode) return undefined
    let cancelled = false
    void reportEditorAPI.listStarterTemplates()
      .then(result => {
        if (cancelled) return
        setStarterTemplates(result.items)
        setSelectedTemplateId(result.items[0]?.id ?? '')
      })
      .catch(cause => { if (!cancelled) setActionError(cause instanceof Error ? cause.message : '报告模板读取失败') })
      .finally(() => { if (!cancelled) setStarterTemplatesLoading(false) })
    return () => { cancelled = true }
  }, [newMode])

  const page = useMemo<Page | undefined>(() => draft ? orderedPages(draft.definition)[0] : undefined, [draft])
  const sections = useMemo(() => page ? orderedSections(page) : [], [page])
  const contentComponents = useMemo(() => draft?.definition.components.filter(component =>
    component.templateRef.type !== 'filter-control' &&
    manifests.get(component.templateRef.type, component.templateRef.version)?.renderer !== 'CONTROL') ?? [], [draft, manifests])
  const activeSection = sections.find(section => section.id === activeSectionId) ?? sections[0]
  const selectedComponent = useMemo(
    () => contentComponents.find(component => component.id === selectedComponentId),
    [contentComponents, selectedComponentId],
  )

  useEffect(() => {
    if (newMode || selectionInitialized || !draft) return
    setSelectionInitialized(true)
    const firstComponent = contentComponents[0]
    if (firstComponent) setSelectedComponentId(firstComponent.id)
  }, [contentComponents, draft, newMode, selectionInitialized])
  const selectedCard = useMemo(
    () => page && selectedComponentId ? findComponentBlock(page, selectedComponentId) : undefined,
    [page, selectedComponentId],
  )
  const selectedCardId = selectedCard?.block.id ?? ''
  const currentDataContextId = draft?.definition.dataContexts[0]?.id ?? ''
  // 绑定面板只能使用报告内数据集对应的、服务端已按列权限裁剪的字段。
  const fieldsByContext = useMemo(
    () => new Map(contexts.map(item => [item.dataContext.id, governedFieldDefinitions(item)])),
    [contexts],
  )
  const fieldsOf = (dataContextId: string): DataContextField[] => fieldsByContext.get(dataContextId) ?? []
  const reportContexts = draft?.definition.dataContexts ?? []
  const reportFilterCount = draft?.definition.globalFilters?.length ?? 0
  // 旧草稿没有持久化 label 时也立即使用当前受治理目录中的中文业务名展示；
  // 新建和筛选器后续保存会把同一名称写回定义，发布制品不再依赖技术字段码。
  const displayedGlobalFilters = (draft?.definition.globalFilters ?? []).map(filter => {
    if (filter.label?.trim()) return filter
    const name = fieldsByContext.get(filter.fieldRef.dataContextId)?.find(item => item.code === filter.fieldRef.field)?.name
    return name ? { ...filter, label: name } : filter
  })
  const contextNameOf = (dataContextId: string) =>
    reportContexts.find(context => context.id === dataContextId)?.alias ||
    contexts.find(item => item.dataContext.id === dataContextId)?.name || dataContextId
  const selectedFilters = draft?.definition.globalFilters?.filter(filter =>
    filter.scope.type === 'REPORT' || (filter.scope.type === 'BLOCK' && filter.scope.targetIds.includes(selectedCardId)),
  ) ?? []
  const selectedFilterCount = selectedFilters.length
  const selectedFilterFields: FilterFieldSummary[] = selectedFilters.map(filter => {
    const field = fieldsOf(filter.fieldRef.dataContextId).find(item => item.code === filter.fieldRef.field)
    return {
      dataset: contextNameOf(filter.fieldRef.dataContextId),
      field: field?.name && field.name !== field.code ? `${field.name} · ${field.code}` : filter.fieldRef.field,
      scope: filter.scope.type === 'REPORT' ? '报告级' : '卡片级',
    }
  })
  const selectedManifest = selectedComponent
    ? manifests.get(selectedComponent.templateRef.type, selectedComponent.templateRef.version)
    : undefined

  useEffect(() => {
    if (selectedComponentId) setReportInspectorView('overview')
  }, [selectedComponentId])
  const canAIEdit = Boolean(asset?.allowedActions.includes('AI_EDIT'))
  const canEdit = Boolean(asset?.allowedActions.includes('EDIT'))
  const canPublish = Boolean(asset?.allowedActions.includes('PUBLISH'))
  const selectedOperationCount = aiPreview?.preview.bundle.operations.filter((_, index) => operationSelection.has(index)).length ?? 0
  const results = useMemo(
    () => new Map((execution?.components ?? []).map(item => [item.componentId, item])),
    [execution],
  )

  // Definition 始终在 1920px 设计宽度上排版；编辑器只改变观看比例，不再让
  // 纸张宽度随面板挤压而重排。这样编辑态、预览态和最终导出使用同一套坐标。
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || editorView !== 'edit') return undefined
    const update = () => {
      const available = Math.max(canvas.clientWidth - 52, 480)
      setEditorScale(Math.min(.62, Math.max(.25, available / 1920)))
    }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(canvas)
    return () => observer.disconnect()
  }, [editorView])

  // 预览态使用真实 1920×1080 页面，并把内容高度向上补齐到完整页数。
  useEffect(() => {
    const paper = paperRef.current
    if (!paper || editorView !== 'preview') return undefined
    let frame = 0
    const measure = () => {
      window.cancelAnimationFrame(frame)
      frame = window.requestAnimationFrame(() => {
        paper.style.height = 'auto'
        const pages = Math.max(1, Math.ceil(paper.scrollHeight / 1080))
        paper.style.height = `${pages * 1080}px`
        setPreviewPageCount(pages)
      })
    }
    measure()
    const observer = new ResizeObserver(measure)
    Array.from(paper.children).forEach(child => observer.observe(child))
    window.addEventListener('resize', measure)
    return () => {
      window.cancelAnimationFrame(frame)
      window.removeEventListener('resize', measure)
      observer.disconnect()
      paper.style.removeProperty('height')
    }
  }, [draft?.revisionNo, editorView, execution?.asOf, displayedGlobalFilters.length])

  /** 所有画布写操作的唯一出口：提交受控 Operation 并换回服务端归一化后的草稿。 */
  const commit = async (operations: EditorOperation[], message: string, onError: (text: string) => void) => {
    if (!draft || operations.length === 0) return null
    try {
      const saved = await reportEditorAPI.applyOperations(reportId, bundle(reportId, draft.revisionNo, operations))
      setDraft(saved.draft)
      if (message) notify(message)
      return saved.draft
    } catch (cause) {
      if (cause instanceof RequestError && cause.detail.code === 'REPORT_REVISION_CONFLICT') {
        onError('草稿已被其他协作者更新，请先打开服务端最新修订。')
      } else if (cause instanceof RequestError && cause.detail.code === 'REPORT_LAYOUT_COLLISION') {
        onError('该位置与其他区块重叠，请调整落点。')
      } else {
        onError(cause instanceof Error ? cause.message : '操作失败')
      }
      return null
    }
  }

  const reloadDraft = async () => {
    setActionError('')
    try {
      const next = await reportEditorAPI.getDraft(reportId)
      setDraft(next); setAIPreview(undefined)
      notify(`已打开服务端最新修订 r${next.revisionNo}`)
    } catch (cause) { setActionError(cause instanceof Error ? cause.message : '最新修订加载失败') }
  }

  const generatePreview = async () => {
    if (!draft || !page || !intent.trim() || !canAIEdit) return
    const dataContextId = draft.definition.dataContexts[0]?.id
    if (!dataContextId) { setActionError('当前报告没有可供 AI 使用的受治理数据上下文'); return }
    const scope: EditorScope = { pageId: page.id, ...(scopeMode === 'section' && activeSection ? { sectionId: activeSection.id } : {}) }
    setPreviewing(true); setActionError('')
    try {
      const result = await reportEditorAPI.previewAI(reportId, { intent: intent.trim(), dataContextId, scope })
      setAIPreview(result)
      setOperationSelection(new Set(result.preview.bundle.operations.map((_, index) => index)))
      setExpandedOperations(new Set(result.preview.bundle.operations.slice(0, 4).map((_, index) => index)))
      notify(`AI 已生成 ${result.preview.bundle.operations.length} 项受控修改`)
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : 'AI 改稿方案生成失败')
    } finally { setPreviewing(false) }
  }

  const applyPreview = async () => {
    if (!draft || !aiPreview || selectedOperationCount === 0) return
    setApplying(true); setActionError('')
    const from = draft.revisionNo
    const operations = aiPreview.preview.bundle.operations.filter((operation, index) =>
      operationSelection.has(index) && !(partialDecision === 'exclude' && operation.op.startsWith('INSIGHT_')))
    // AI 方案沿用服务端返回的 bundle（保留 aiRunId 与 scope），只裁剪用户未勾选的操作。
    const nextBundle: EditorOperationBundle = { ...aiPreview.preview.bundle, operations }
    try {
      const result = await reportEditorAPI.applyOperations(reportId, nextBundle)
      setDraft(result.draft)
      setLastReceipt({ from, to: result.draft.revisionNo, count: operations.length, source: 'AI' })
      setAIPreview(undefined)
      notify(`已应用 ${operations.length} 项修改并生成新修订`)
    } catch (cause) {
      if (cause instanceof RequestError && cause.detail.code === 'REPORT_REVISION_CONFLICT') setActionError('草稿已被其他协作者更新，请打开服务端最新修订后重新生成方案。')
      else setActionError(cause instanceof Error ? cause.message : 'AI 修改应用失败')
    } finally { setApplying(false) }
  }

  const undoRedo = async (redo: boolean) => {
    if (!draft || !canEdit) return
    setActionError('')
    try {
      const result = redo ? await reportEditorAPI.redo(reportId) : await reportEditorAPI.undo(reportId)
      setDraft(result.draft)
      setAIPreview(undefined); notify(redo ? '已重做并生成新修订' : '已撤销并生成新修订')
    } catch (cause) { setActionError(cause instanceof Error ? cause.message : redo ? '当前没有可重做的修订' : '当前没有可撤销的修订') }
  }

  /** 拖拽与缩放：碰撞消解后可能连带移动其他区块，一次提交为同一条修订。 */
  const changeLayout = async (sectionId: string, blockId: string, rect: { x: number; y: number; w: number; h: number }) => {
    if (!draft || !page || !canEdit) return
    const section = page.sections.find(item => item.id === sectionId)
    if (!section) return
    const block = section.blocks.find(item => item.id === blockId)
    const componentId = block?.zones.flatMap(zone => zone.slots.map(slot => slot.componentId)).find(Boolean)
    const component = draft.definition.components.find(item => item.id === componentId)
    const minimum = minimumSize(component ? manifests.get(component.templateRef.type, component.templateRef.version) : undefined)
    const operations = layoutOperations(section, blockId, rect, canvasOf(draft.definition).desktop.columns, minimum)
    await commit(operations, '布局已保存为新修订', setActionError)
  }

  /** 卡片内槽位拖拽：区域行数不够时一并加高区域与卡片。 */
  const changeSlotLayout = async (
    blockId: string, zoneId: string, slotId: string,
    rect: { x: number; y: number; w: number; h: number },
  ) => {
    if (!draft || !page || !canEdit) return
    const located = findBlock(page, blockId)
    const zone = located?.block.zones.find(item => item.id === zoneId)
    if (!located || !zone) return
    const componentId = zone.slots.find(slot => slot.id === slotId)?.componentId
    const component = draft.definition.components.find(item => item.id === componentId)
    const minimum = minimumSize(component ? manifests.get(component.templateRef.type, component.templateRef.version) : undefined)
    const operations = slotLayoutOperations(located.block, zone, slotId, rect, minimum)
    if (operations.length === 0) return
    await commit(operations, '卡片内布局已保存为新修订', setActionError)
  }

  const reorderZone = async (blockId: string, zoneId: string, direction: -1 | 1) => {
    if (!draft || !page || !canEdit) return
    const located = findBlock(page, blockId)
    if (!located) return
    const operations = zoneReorderOperations(located.block, zoneId, direction)
    if (operations.length === 0) return
    await commit(operations, direction < 0 ? '区域已上移' : '区域已下移', setActionError)
  }

  const saveManual = async (result: ManualEditResult) => {
    if (!draft || !selectedComponent) return
    setManualBusy(true); setManualError('')
    // 先换展示类型（COMPONENT_REPLACE），再写属性与绑定，三者进入同一条修订。
    const operations: EditorOperation[] = [
      ...(result.replaceWith ? replaceComponentOperations(selectedComponent, result.replaceWith) : []),
      ...updateComponentOperations(selectedComponent, result.options, result.binding, result.dataContextId ?? selectedComponent.dataBinding?.dataContextId ?? currentDataContextId),
    ]
    await commit(operations, result.replaceWith ? '展示类型、属性与绑定已保存为新修订' : result.binding ? '卡片数据绑定已保存为新修订' : '卡片属性已保存为新修订', setManualError)
    setManualBusy(false)
  }

  /**
   * AI 识别度量与维度：模型只在所选数据集的受治理字段目录里、按该卡片的组件合同
   * 挑选绑定；结果填入面板，是否保存仍由人决定（保存走 USER 操作）。
   */
  const suggestBinding = async (input: { dataContextId: string; manifest: ComponentManifest; title: string }): Promise<BindingSuggestion | null> => {
    if (!draft || !canAIEdit) return null
    const result = await reportEditorAPI.suggestCardBinding(reportId, {
      componentId: selectedComponent?.id, dataContextId: input.dataContextId,
      manifestType: input.manifest.type, manifestVersion: input.manifest.version, title: input.title,
    })
    const fields = fieldsOf(input.dataContextId)
    const known = (item: FieldBinding) => fields.some(field => field.code === item.field)
    const contract = input.manifest.dataContract
    return {
      dimensions: (result.suggestion.dimensions ?? []).filter(known).slice(0, contract.dimensions.max),
      measures: (result.suggestion.measures ?? []).filter(known).slice(0, contract.measures.max),
      rationale: result.suggestion.rationale || `AI 运行 ${result.aiRunId}`,
    }
  }

  const addDataContext = async (candidate: DataContextCandidate) => {
    if (!draft || !canEdit) return
    setDataBusy(true); setDataError('')
    await commit(addDataContextOperations(draft.definition, {
      id: candidate.dataContext.id, datasetId: candidate.dataContext.datasetId,
      datasetVersionId: candidate.dataContext.datasetVersionId, alias: candidate.name,
    }), `数据集「${candidate.name}」已加入报告`, setDataError)
    setDataBusy(false)
  }

  const removeDataContext = async (dataContextId: string) => {
    if (!draft || !canEdit) return
    setDataBusy(true); setDataError('')
    await commit(removeDataContextOperations(dataContextId), '数据集已从报告移除', setDataError)
    setDataBusy(false)
  }

  const createFilter = async (filterDraft: FilterDraft) => {
    if (!draft || !canEdit) return
    setFilterBusy(true); setFilterError('')
    await commit(createFilterOperations(draft.definition, filterDraft, () => crypto.randomUUID()), '报告筛选已更新', setFilterError)
    setFilterBusy(false)
  }

  const updateFilter = async (filter: GlobalFilter, filterDraft: FilterDraft) => {
    if (!draft || !canEdit) return
    setFilterBusy(true); setFilterError('')
    await commit(updateFilterOperations(filter, filterDraft), '报告筛选规则已更新', setFilterError)
    setFilterBusy(false)
  }

  const deleteFilter = async (filterId: string) => {
    if (!draft || !canEdit) return
    setFilterBusy(true); setFilterError('')
    const filter = draft.definition.globalFilters?.find(item => item.id === filterId)
    const linkedControls = filter ? draft.definition.components.filter(component => {
      const manifest = manifests.get(component.templateRef.type, component.templateRef.version)
      return (component.templateRef.type === 'filter-control' || manifest?.renderer === 'CONTROL') &&
        component.dataBinding?.dataContextId === filter.fieldRef.dataContextId &&
        component.dataBinding?.dimensions?.[0]?.field === filter.fieldRef.field
    }) : []
    const operations = [
      ...deleteFilterOperations(filterId),
      ...(page ? linkedControls.flatMap(component => removeComponentOperations(page, component.id)) : []),
    ]
    const saved = await commit(operations, '报告筛选已移除', setFilterError)
    if (saved && linkedControls.some(component => component.id === selectedComponentId)) setSelectedComponentId('')
    setFilterBusy(false)
  }

  /**
   * 组件直接落到画布，由 Definition 的布局算法寻找可用位置。内部仍生成最小
   * block/zone/slot 结构供服务端校验，但作者只需要面对可拖动的独立元素。
   */
  const dropFromPalette = (event: DragEvent<HTMLElement>) => {
    const payload = decodePalettePayload(event.dataTransfer.getData(paletteDragType))
    if (!payload) return
    event.preventDefault()
    const [type, version] = payload.ref.split('@')
    const manifest = manifests.get(type, version)
    if (!manifest) { setActionError('组件清单不存在或已经更新'); return }
    void addCanvasComponent(manifest, manifest.displayName, currentDataContextId, payload.options)
  }

  const addCanvasComponent = async (
    manifest: ComponentManifest, title = manifest.displayName, dataContextId = currentDataContextId,
    initialOptions?: Partial<ComponentOptions>,
  ) => {
    if (!draft || !page || !canEdit) return
    setComponentBusy(true); setComponentError(''); setActionError('')
    const templateTarget = findCompatibleTemplateSlot(page, activeSection?.id, manifest)
    if (templateTarget) {
      const result = placeComponentInSlotOperations({
        page, ...templateTarget, manifest, title, dataContextId,
        fields: fieldsOf(dataContextId), initialOptions, newId: () => crypto.randomUUID(),
      })
      if (result.error) {
        setActionError(result.error); setComponentError(result.error); setComponentBusy(false)
        return
      }
      const saved = await commit(result.operations, `${manifest.displayName}已填入展示模板`, setActionError)
      if (saved) {
        setActiveSectionId(templateTarget.sectionId); setSelectedComponentId(result.componentId)
        setSidePanel('data'); setComponentLibraryOpen(false)
      }
      setComponentBusy(false)
      return
    }
    // 没有可用槽位时自动补一个标准小节，保证数据组件始终属于“主题—分析角度—
    // 小节”的叙事结构；框架创建与组件落位仍在同一次受控修订中完成。
    const frame = createSubsectionFrameOperations({
      definition: draft.definition, page, sectionId: activeSection?.id,
      layout: 'CONCLUSION_TOP', chartCount: 2, includeDetail: false, includeAppendix: false,
      title: `小节 ${Math.max((activeSection?.blocks.length ?? 0) + 1, 1)}`,
      sectionName: `${sectionNoun} 1`, newId: () => crypto.randomUUID(),
    })
    const frameOperation = frame.operations[0]
    const framedPage: Page = frameOperation.op === 'BLOCK_CREATE'
      ? {
          ...page,
          sections: page.sections.map(section => section.id === frame.sectionId
            ? { ...section, blocks: [...section.blocks, (frameOperation.payload as { block: Block }).block] }
            : section),
        }
      : {
          ...page,
          sections: [...page.sections, (frameOperation.payload as { section: Section }).section],
        }
    const freshTarget = findCompatibleTemplateSlot(framedPage, frame.sectionId, manifest)
    if (!freshTarget) {
      setActionError('新建小节没有适合该组件的内容区域，请先选择对应的小节布局')
      setComponentBusy(false)
      return
    }
    const placed = placeComponentInSlotOperations({
      page: framedPage, ...freshTarget, manifest, title, dataContextId,
      fields: fieldsOf(dataContextId), initialOptions, newId: () => crypto.randomUUID(),
    })
    if (placed.error) {
      setActionError(placed.error); setComponentError(placed.error); setComponentBusy(false)
      return
    }
    const saved = await commit([...frame.operations, ...placed.operations], `${manifest.displayName}已加入新小节`, setActionError)
    if (saved) {
      setActiveSectionId(frame.sectionId); setSelectedComponentId(placed.componentId)
      setSidePanel('data'); setComponentLibraryOpen(false)
    }
    setComponentBusy(false)
  }

  const placeComponentInSlot = async (
    blockId: string, zoneId: string, slotId: string, ref: string,
    configuredTitle?: string, configuredContextId?: string,
  ) => {
    if (!draft || !page || !canEdit) return
    setComponentBusy(true); setComponentError(''); setActionError('')
    const payload = decodePalettePayload(ref)
    if (!payload) { setActionError('组件拖拽信息无效'); setComponentBusy(false); return }
    const [type, version] = payload.ref.split('@')
    const manifest = manifests.get(type, version)
    if (!manifest) { setActionError('组件清单不存在或已经更新'); setComponentBusy(false); return }
    const contextID = configuredContextId || currentDataContextId
    const result = placeComponentInSlotOperations({
      page, blockId, zoneId, slotId, manifest, title: configuredTitle || manifest.displayName,
      dataContextId: contextID, fields: fieldsOf(contextID), initialOptions: payload.options, newId: () => crypto.randomUUID(),
    })
    if (result.error) { setActionError(result.error); setComponentError(result.error); setComponentBusy(false); return }
    const saved = await commit(result.operations, `${manifest.displayName}已放入现有元素组`, setActionError)
    if (saved) { setSelectedComponentId(result.componentId); setSidePanel('data'); setComponentLibraryOpen(false) }
    setComponentBusy(false)
  }

  const pickComponentForSlot = (manifest: ComponentManifest, options?: Partial<ComponentOptions>, title = manifest.displayName, dataContextId = currentDataContextId) => {
    void addCanvasComponent(manifest, title, dataContextId, options)
  }

  const deleteSelectedComponent = async () => {
    if (!draft || !page || !selectedComponent || !canEdit) return
    const operations = removeComponentOperations(page, selectedComponent.id)
    const saved = await commit(operations, '组件已移除并生成新修订', setActionError)
    if (saved) setSelectedComponentId('')
  }

  const duplicateBlock = async (blockId: string) => {
    if (!draft || !page || !canEdit) return
    const result = duplicateBlockOperations({ definition: draft.definition, page, blockId, newId: () => crypto.randomUUID() })
    if (!result) return
    const saved = await commit(result.operations, '画布元素已复制', setActionError)
    if (saved && result.componentId) setSelectedComponentId(result.componentId)
  }

  const deleteBlock = async (blockId: string) => {
    if (!draft || !page || !canEdit) return
    const operations = removeBlockOperations(page, blockId)
    const saved = await commit(operations, '画布元素已删除（可撤销）', setActionError)
    if (saved && selectedCardId === blockId) setSelectedComponentId('')
  }

  const sectionNoun = draft?.definition.metadata.reportType === 'DASHBOARD' ? '分区' : '分析角度'

  const addSection = async () => {
    if (!draft || !page || !canEdit) return
    const { operations, sectionId } = createSectionOperations(page, `${sectionNoun} ${sections.length + 1}`, () => crypto.randomUUID())
    const saved = await commit(operations, `已新建${sectionNoun}`, setActionError)
    if (saved) { setActiveSectionId(sectionId); setRenamingSectionId(sectionId) }
  }

  const addFramework = async (request: FrameworkRequest) => {
    if (request.kind === 'ANGLE') {
      await addSection()
      return
    }
    if (!draft || !page || !canEdit) return
    const subsectionCount = activeSection?.blocks.filter(block => block.cardKind?.startsWith('LAYOUT_SUBSECTION_')).length ?? 0
    const result = createSubsectionFrameOperations({
      definition: draft.definition,
      page,
      sectionId: activeSection?.id,
      layout: request.layout,
      chartCount: request.chartCount,
      includeDetail: request.includeDetail,
      includeAppendix: request.includeAppendix,
      title: `小节 ${subsectionCount + 1}`,
      sectionName: `${sectionNoun} 1`,
      newId: () => crypto.randomUUID(),
    })
    const saved = await commit(result.operations, `小节已加入当前${sectionNoun}`, setActionError)
    if (saved) {
      setActiveSectionId(result.sectionId)
      setSelectedComponentId('')
      document.getElementById(`report-section-${result.sectionId}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
    }
  }

  const renameBlock = async (sectionId: string, blockId: string, title: string) => {
    if (!draft || !page || !canEdit) return
    const block = page.sections.find(section => section.id === sectionId)?.blocks.find(item => item.id === blockId)
    if (!block) return
    await commit(renameBlockOperations(block, title), '元素组标题已更新', setActionError)
  }

  const renameSection = async (sectionId: string, name: string) => {
    setRenamingSectionId('')
    const current = sections.find(section => section.id === sectionId)
    const trimmed = name.trim()
    if (!draft || !canEdit || !current || !trimmed || trimmed === current.name) return
    await commit(renameSectionOperations(sectionId, trimmed), `${sectionNoun}已重命名`, setActionError)
  }

  const renameReport = async (name: string) => {
    setRenamingTitle(false)
    const trimmed = name.trim()
    if (!draft || !canEdit || !trimmed || trimmed === draft.definition.metadata.name) return
    await commit(updateReportSettingsOperations(draft.definition, { name: trimmed }), '报告名称已更新', setActionError)
  }

  const addInteraction = async (draft: InteractionDraft) => {
    if (!canEdit) return
    setInteractionBusy(true); setInteractionError('')
    await commit(createInteractionOperations(draft, () => crypto.randomUUID()), '联动已保存为新修订', setInteractionError)
    setInteractionBusy(false)
  }

  const removeInteraction = async (interactionId: string) => {
    if (!canEdit) return
    setInteractionBusy(true); setInteractionError('')
    await commit(deleteInteractionOperations(interactionId), '联动已移除并生成新修订', setInteractionError)
    setInteractionBusy(false)
  }

  const moveSection = async (direction: -1 | 1, sectionId = activeSection?.id) => {
    if (!draft || !page || !sectionId || !canEdit) return
    const operations = sectionReorderOperations(page, sectionId, direction)
    await commit(operations, direction < 0 ? `${sectionNoun}已上移` : `${sectionNoun}已下移`, setActionError)
  }

  const deleteActiveSection = async () => {
    if (!draft || !page || !activeSection || !canEdit) return
    const ids = activeSection.blocks.flatMap(block =>
      block.zones.flatMap(zone => zone.slots.map(slot => slot.componentId).filter((id): id is string => Boolean(id))))
    const operations: EditorOperation[] = [
      { op: 'SECTION_DELETE', targetId: activeSection.id, payload: {} },
      ...ids.map(id => ({ op: 'COMPONENT_DELETE', targetId: id, payload: {} })),
    ]
    setComponentBusy(true); setComponentError('')
    const saved = await commit(operations, '章节及其组件已移除并生成新修订', setComponentError)
    if (saved) {
      setActiveSectionId(orderedPages(saved.definition)[0]?.sections[0]?.id ?? '')
      setSelectedComponentId(''); setDeleteSectionOpen(false)
    }
    setComponentBusy(false)
  }

  const createBlankReport = async () => {
    if (!newName.trim() || newCreating) return
    setNewCreating('blank'); setActionError('')
    try {
      const result = await reportEditorAPI.createBlank({
        name: newName.trim(), description: intent.trim(), dataContextId: contextId || undefined, reportType: newReportType, headerStyle: newHeaderStyle,
      })
      navigate(`/reports/${result.report.id}?mode=edit`, { replace: true })
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : '空白报告创建失败，请稍后重试')
    } finally { setNewCreating('') }
  }

  const createNewReport = async () => {
    if (!intent.trim() || newCreating) return
    setNewCreating('ai'); setActionError('')
    try {
      const result = await reportEditorAPI.createAI({ intent: intent.trim(), reportType: newReportType, headerStyle: newHeaderStyle, dataContextId: contextId || undefined })
      navigate(`/reports/${result.report.id}?mode=edit`, { replace: true })
    } catch (cause) {
      setActionError(cause instanceof Error
        ? `${cause.message}（可改用「创建空白报告」，它不依赖模型提供方）`
        : 'AI 新建报告失败，可改用「创建空白报告」')
    } finally { setNewCreating('') }
  }

  const createFromBlueprint = async () => {
    if (!blueprintText.trim() || !contextId || newCreating) return
    setNewCreating('blueprint'); setActionError('')
    try {
      let parsed: ReportBlueprint
      try { parsed = JSON.parse(blueprintText) as ReportBlueprint } catch { throw new Error('蓝图 JSON 无法解析，请检查逗号、括号与引号') }
      if (parsed?.schemaVersion !== 'report-blueprint/1.0' || !Array.isArray(parsed.sections)) {
        throw new Error('这不是一份 Report Blueprint 1.0：缺少 schemaVersion 或 sections')
      }
      const configured: ReportBlueprint = {
        ...parsed, title: newName.trim() || parsed.title, reportType: newReportType,
      }
      const result = await reportEditorAPI.createFromBlueprint({ blueprint: configured, dataContextIds: [contextId], headerStyle: newHeaderStyle })
      navigate(`/reports/${result.report.id}?mode=edit`, { replace: true })
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : '蓝图展开失败，请检查卡片合同与短引用')
    } finally { setNewCreating('') }
  }

  const createFromTemplate = async () => {
    if (!newName.trim() || !contextId || !selectedTemplateId || newCreating) return
    setNewCreating('template'); setActionError('')
    try {
      const result = await reportEditorAPI.instantiateStarterTemplate(selectedTemplateId, {
        name: newName.trim(), description: intent.trim(), dataContextId: contextId, reportType: newReportType, headerStyle: newHeaderStyle,
      })
      navigate(`/reports/${result.report.id}?mode=edit`, { replace: true })
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : '模板报告创建失败，请检查所选数据集字段')
    } finally { setNewCreating('') }
  }

  /** 从 JSON 导入：同一份 Report Definition，分配新 ID/编码后交给服务端同一条校验链。 */
  const importDefinition = async () => {
    if (!importText.trim() || newCreating) return
    setNewCreating('import'); setActionError('')
    try {
      let parsed: ReportDefinition
      try { parsed = JSON.parse(importText) as ReportDefinition } catch { throw new Error('JSON 无法解析，请检查粘贴的内容是否完整') }
      if (!parsed || typeof parsed !== 'object' || !parsed.metadata || !Array.isArray(parsed.pages)) {
        throw new Error('这不是一份 Report Definition：缺少 metadata 或 pages')
      }
      const id = crypto.randomUUID()
      const definition: ReportDefinition = {
        ...parsed,
        metadata: {
          ...parsed.metadata, id, code: `report_${id.replace(/-/g, '').slice(0, 16)}`,
          name: newName.trim() || parsed.metadata.name, reportType: parsed.metadata.reportType || newReportType,
          headerStyle: newReportType === 'REPORT' ? newHeaderStyle || parsed.metadata.headerStyle || '01' : parsed.metadata.headerStyle,
        },
      }
      const result = await reportEditorAPI.createFromDefinition(definition)
      navigate(`/reports/${result.report.id}?mode=edit`, { replace: true })
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : 'JSON 导入失败')
    } finally { setNewCreating('') }
  }

  // 键盘：Delete/Backspace 删除选中卡片，Esc 取消选中，⌘/Ctrl+Z 撤销、⇧⌘Z 重做。输入框内不拦截。
  useEffect(() => {
    if (newMode) return undefined
    const handler = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      const typing = Boolean(target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT' || target.isContentEditable))
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'z' && !typing) {
        event.preventDefault(); void undoRedo(event.shiftKey); return
      }
      if (typing) return
      if (event.key === 'Escape') { setSelectedComponentId(''); return }
      if ((event.key === 'Delete' || event.key === 'Backspace') && selectedComponentId) {
        event.preventDefault(); void deleteSelectedComponent()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
    // undoRedo/deleteBlock 每次渲染都是新函数；它们只读最新的 draft/selection，用最新引用即可。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [newMode, selectedComponentId, draft?.revisionNo, canEdit])

  if (newMode) return <AppShell className="report-editor-shell" lockBusinessDomain>
    <div className="report-editor-workspace report-editor-new-workspace">
      <header className="report-editor-header"><div><button type="button" onClick={() => navigate('/reports')}><ArrowLeft size={15} />返回报告工作台</button><div className="report-editor-title"><h1>新建报告</h1><span>草稿 r0</span><small>创建后进入编辑器</small></div></div></header>
      <div className="report-editor-body"><NewReportCanvas name={newName} description={intent} reportType={newReportType} headerStyle={newHeaderStyle} /><NewReportTaskPanel
        name={newName} reportType={newReportType} headerStyle={newHeaderStyle} intent={intent} contexts={contexts} contextId={contextId}
        contextsLoading={contextsLoading} contextsError={contextsError}
        templates={starterTemplates} templatesLoading={starterTemplatesLoading} selectedTemplateId={selectedTemplateId}
        creating={newCreating} error={actionError} blueprintText={blueprintText} importText={importText}
        onName={setNewName} onReportType={setNewReportType} onHeaderStyle={setNewHeaderStyle} onIntent={setIntent} onContext={value => {
          setContextId(value)
          const selected = contexts.find(item => item.dataContext.id === value)
          if (selected) setBlueprintText(JSON.stringify(starterBlueprint(selected, newName, newReportType), null, 2))
        }} onTemplate={setSelectedTemplateId}
        onCreateBlank={() => void createBlankReport()} onCreateTemplate={() => void createFromTemplate()} onCreateBlueprint={() => void createFromBlueprint()} onGenerate={() => void createNewReport()}
        onBlueprintText={setBlueprintText}
        onImportText={setImportText} onImport={() => void importDefinition()}
      /></div>
    </div>
  </AppShell>

  if (loading) return <AppShell className="report-editor-shell" lockBusinessDomain defaultSidebarCollapsed><div className="report-editor-loading"><SpinnerGap className="is-spinning" size={28} /><strong>正在读取报告草稿与权限</strong><p>随后加载可复用的已发布运行结果。</p></div></AppShell>
  if (!draft || !page) return <AppShell className="report-editor-shell" lockBusinessDomain defaultSidebarCollapsed><div className="report-editor-loading is-error"><WarningCircle size={28} /><strong>报告编辑器无法打开</strong><p>{loadError || '当前草稿没有可编辑页面。'}</p><button type="button" onClick={() => navigate('/reports')}>返回报告工作台</button></div></AppShell>

  return <AppShell className="report-editor-shell" lockBusinessDomain defaultSidebarCollapsed>
    <div className={`report-editor-workspace is-${editorView}-view`}>
      <header className="report-editor-header">
        <div className="report-editor-header-leading">
          <img className="report-editor-brand" src="/haier-logo.svg" alt="Haier 海尔" />
          <span className="report-editor-header-divider" aria-hidden="true" />
          <button className="report-editor-back" type="button" aria-label="返回报告工作台" title="返回报告工作台" onClick={() => navigate('/reports')}><ArrowLeft size={19} /></button>
          <div className="report-editor-title">
            {renamingTitle
              ? <input className="report-editor-title-input" autoFocus defaultValue={draft.definition.metadata.name} maxLength={80} aria-label="报告名称"
                onBlur={event => void renameReport(event.target.value)}
                onKeyDown={event => { if (event.key === 'Enter') (event.target as HTMLInputElement).blur(); if (event.key === 'Escape') setRenamingTitle(false) }} />
              : <h1 title={canEdit ? '点击重命名' : ''} onClick={() => canEdit && setRenamingTitle(true)}>{draft.definition.metadata.name}{canEdit && <PencilSimple size={13} />}</h1>}
            <span><CheckCircle size={14} weight="fill" />草稿已自动保存 {dateTime(draft.updatedAt).split(' ').at(-1)}</span>
          </div>
        </div>
        <div className="report-editor-view-switch" role="tablist" aria-label="编辑器视图">
          <button type="button" role="tab" aria-selected={editorView === 'edit'} className={editorView === 'edit' ? 'is-active' : ''}
            onClick={() => setEditorView('edit')}>编辑</button>
          <button type="button" role="tab" aria-selected={editorView === 'preview'} className={editorView === 'preview' ? 'is-active' : ''}
            onClick={() => { setEditorView('preview'); setSelectedComponentId('') }}>预览</button>
        </div>
        <div className="report-editor-header-actions">
          <button type="button" aria-label="撤销" disabled={!canEdit || applying} onClick={() => void undoRedo(false)}><ArrowUDownLeft size={20} /></button>
          <button type="button" aria-label="重做" disabled={!canEdit || applying} onClick={() => void undoRedo(true)}><ArrowUDownRight size={20} /></button>
          <button className={`report-editor-ai-trigger ${sidePanel === 'ai' ? 'is-active' : ''}`} type="button" disabled={!canAIEdit}
            aria-label="AI 改稿" title="AI 改稿" onClick={() => { setEditorView('edit'); setSidePanel('ai') }}><Sparkle size={18} weight="fill" /></button>
          <button className="report-editor-publish primary-button" type="button" disabled={!canPublish || Boolean(aiPreview)} title={aiPreview ? '请先应用或退回当前 AI 方案' : ''} onClick={() => navigate(`/reports/${reportId}/publish-review`)}><Eye size={17} />发布</button>
          <button className="report-editor-more" type="button" aria-label="打开报告定义" title="打开报告定义 JSON" onClick={() => setJsonOpen(true)}><DotsThreeVertical size={20} /></button>
        </div>
      </header>

      {actionError && <div className="report-editor-action-error" role="alert"><WarningCircle size={16} /><span>{actionError}</span>{actionError.includes('最新修订') && <button type="button" onClick={() => void reloadDraft()}>打开最新修订</button>}<button type="button" aria-label="关闭错误" onClick={() => setActionError('')}><X size={14} /></button></div>}

      <div className="report-editor-body">
        <div className="report-editor-main">
          <nav className="report-editor-outline report-editor-sidebar" aria-label="组件面板与大纲">
            <ComponentPalette manifests={latestComponentManifests(manifests.list())} disabled={!canEdit}
              themeName={draft.definition.metadata.name}
              onPick={pickComponentForSlot} onAddFramework={kind => void addFramework(kind)} />
            <details className="report-editor-structure-panel">
              <summary><span>页面结构</span><em>{sections.length}</em><CaretRight size={14} /></summary>
              <header><strong>{sectionNoun}</strong><span>{canEdit && <>
                <button type="button" className="report-outline-add" title={`新建${sectionNoun}`} onClick={() => void addSection()}><Plus size={13} />新建</button>
              </>}</span></header>
              <ul className="report-outline-list">
              {sections.map((section, index) => <li key={section.id} className={`report-outline-item ${section.id === activeSectionId ? 'is-active' : ''}`.trim()}>
                {renamingSectionId === section.id
                  ? <input autoFocus defaultValue={section.name} maxLength={60} aria-label={`${sectionNoun}名称`}
                    onBlur={event => void renameSection(section.id, event.target.value)}
                    onKeyDown={event => { if (event.key === 'Enter') (event.target as HTMLInputElement).blur(); if (event.key === 'Escape') setRenamingSectionId('') }} />
                  : <button type="button" className="report-outline-name" title={section.name}
                    onClick={() => {
                      setActiveSectionId(section.id)
                      document.getElementById(`report-section-${section.id}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
                    }}
                    onDoubleClick={() => canEdit && setRenamingSectionId(section.id)}>
                    <span>{section.name}</span><em>{section.blocks.length}</em>
                  </button>}
                {canEdit && renamingSectionId !== section.id && <span className="report-outline-actions">
                  <button type="button" aria-label="重命名" title="重命名" onClick={() => setRenamingSectionId(section.id)}><PencilSimple size={12} /></button>
                  <button type="button" aria-label="上移" title="上移" disabled={index === 0} onClick={() => { setActiveSectionId(section.id); void moveSection(-1, section.id) }}><ArrowUp size={12} /></button>
                  <button type="button" aria-label="下移" title="下移" disabled={index === sections.length - 1} onClick={() => { setActiveSectionId(section.id); void moveSection(1, section.id) }}><ArrowDown size={12} /></button>
                  <button type="button" className="is-danger" aria-label={`删除${sectionNoun}`} title={`删除${sectionNoun}`} onClick={() => { setActiveSectionId(section.id); setComponentError(''); setDeleteSectionOpen(true) }}><Trash size={12} /></button>
                </span>}
              </li>)}
              </ul>
              {sections.length === 0 && <p>添加第一个元素后自动创建{sectionNoun}</p>}
            </details>
          </nav>
          <main ref={canvasRef} className={`report-editor-canvas ${aiPreview ? 'has-ai-proposal' : ''}`.trim()}
            onDragOver={event => { if (event.dataTransfer.types.includes(paletteDragType)) { event.preventDefault(); event.dataTransfer.dropEffect = 'copy' } }}
            onDrop={dropFromPalette}>
            <div className="report-editor-canvas-spec" aria-live="polite">
              <span>{editorView === 'edit' ? `编辑缩放 ${Math.round(editorScale * 100)}%` : '输出预览'}</span>
              <strong>1920 × 1080</strong><em>{editorView === 'preview' ? `${previewPageCount} 页` : '自动分页'}</em>
            </div>
            <article ref={paperRef} className="report-editor-paper report-editor-live-paper"
              data-page-count={editorView === 'preview' ? previewPageCount : undefined}
              style={{ zoom: editorView === 'edit' ? editorScale : 1 }}
              onClick={event => { if (editorView !== 'edit' || (event.target as HTMLElement).closest('.report-render-block')) return; setSelectedComponentId('') }}>
              {editorView === 'preview' && previewPageCount > 1 && <div className="report-editor-page-guides" aria-hidden="true">
                {Array.from({ length: previewPageCount - 1 }, (_, index) => <span key={index} style={{ top: `${(index + 1) * 1080}px` }} />)}
              </div>}
              <ReportHeader style={draft.definition.metadata.headerStyle || '01'} title={draft.definition.metadata.name}
                description={draft.definition.metadata.description}
                meta={[
                  `报告类型：${reportTypeLabels[draft.definition.metadata.reportType]?.name ?? draft.definition.metadata.reportType}`,
                  `当前修订：r${draft.revisionNo}`, `数据源：${reportContexts.length} 个`,
                  executing ? '正在刷新预览' : executionError ? '预览暂不可用' : execution ? `数据截至 ${dateTime(execution.asOf)}` : '尚未执行预览',
                ]}
                filters={displayedGlobalFilters} values={designFilterValues}
                onChange={(filterId, value) => setDesignFilterValues(current => ({ ...current, [filterId]: value }))}
                onApply={() => notify('筛选条件已应用到报告预览')} applying={executing}
                onConfigure={editorView === 'edit' ? () => { setSelectedComponentId(''); setSidePanel('data'); setReportInspectorView('filters') } : undefined}
                onExport={() => notify('发布后可导出报告')} locked={editorView === 'edit'} />
              {/* 空白报告只显示报告头与筛选；首次拖入组件时会自动创建首个分区。 */}
              {page.sections.length > 0 && <ReportPageView definition={draft.definition} page={page} manifests={manifests} results={results}
                designMode={!execution}
                selectedComponentId={editorView === 'edit' ? selectedComponentId : ''}
                onSelectComponent={editorView === 'edit' ? (componentId, blockId) => {
                  setSelectedComponentId(componentId)
                  if (componentId) setSidePanel('data')
                  const located = findComponentBlock(page, componentId)
                  if (located) setActiveSectionId(located.section.id)
                  else if (blockId) setActiveSectionId(activeSectionId)
                } : undefined}
                editing={editorView === 'edit' && canEdit ? {
                  onLayoutChange: (sectionId, blockId, rect) => void changeLayout(sectionId, blockId, rect),
                  onSlotLayoutChange: (blockId, zoneId, slotId, rect) => void changeSlotLayout(blockId, zoneId, slotId, rect),
                  onZoneReorder: (blockId, zoneId, direction) => void reorderZone(blockId, zoneId, direction),
                  onComponentDrop: (blockId, zoneId, slotId, ref) => void placeComponentInSlot(blockId, zoneId, slotId, ref),
                  onBlockTitleChange: (sectionId, blockId, title) => void renameBlock(sectionId, blockId, title),
                  onDuplicateBlock: (_sectionId, blockId) => void duplicateBlock(blockId),
                  onDeleteBlock: (_sectionId, blockId) => void deleteBlock(blockId),
                } : undefined} />}
            </article>
            {aiPreview && <span className="report-editor-preview-label">AI 方案预览，不会自动保存</span>}
          </main>
        </div>

        <aside className="report-editor-task-panel">
          <div className="report-editor-panel-tabs" role="tablist" aria-label="侧栏面板">
            <button type="button" role="tab" aria-selected={sidePanel === 'data'} className={sidePanel === 'data' ? 'is-active' : ''} onClick={() => setSidePanel('data')}>数据</button>
            <button type="button" role="tab" aria-selected={sidePanel === 'appearance'} className={sidePanel === 'appearance' ? 'is-active' : ''} onClick={() => setSidePanel('appearance')}>外观</button>
            <button type="button" role="tab" aria-selected={sidePanel === 'interaction'} className={sidePanel === 'interaction' ? 'is-active' : ''} onClick={() => setSidePanel('interaction')}>交互</button>
            <button type="button" role="tab" aria-label="AI 改稿" aria-selected={sidePanel === 'ai'} className={`report-editor-panel-ai ${sidePanel === 'ai' ? 'is-active' : ''}`} onClick={() => setSidePanel('ai')}><Sparkle size={15} weight="fill" /></button>
          </div>
          {(sidePanel === 'data' || sidePanel === 'appearance') && <div className="report-editor-panel-body is-data">
            {selectedComponent ? <>
              <CardInspector
                key={`${selectedComponent.id}:${sidePanel}`}
                mode={sidePanel}
                component={selectedComponent} manifest={selectedManifest} manifests={manifests.list()}
                reportContexts={reportContexts} contextNameOf={contextNameOf} fieldsOf={fieldsOf} defaultContextId={currentDataContextId}
                canAI={canAIEdit} onSuggest={suggestBinding}
                busy={manualBusy || !canEdit} error={manualError} filterCount={selectedFilterCount} filterFields={selectedFilterFields}
                globalFilters={displayedGlobalFilters}
                onClose={() => setSelectedComponentId('')} onSave={result => void saveManual(result)}
                filterPanel={sidePanel === 'data' ? <FilterPanel definition={draft.definition} candidates={contexts} fieldsOf={fieldsOf} selectedBlockId={selectedCardId}
                  onlyBlock defaultContextId={selectedComponent.dataBinding?.dataContextId ?? currentDataContextId}
                  busy={filterBusy || !canEdit} error={filterError}
                  onCreate={filterDraft => void createFilter(filterDraft)} onUpdate={(filter, filterDraft) => void updateFilter(filter, filterDraft)}
                  onDelete={filterId => void deleteFilter(filterId)} /> : null}
              />
            </> : sidePanel === 'data' ? <>
              {reportInspectorView === 'overview' && <section className="report-inspector-overview" aria-labelledby="report-settings-title">
                <header>
                  <div><span>报告级设置</span><h2 id="report-settings-title">数据与筛选</h2></div>
                  <em>r{draft.revisionNo}</em>
                </header>
                <div className="report-inspector-overview-list">
                  <button type="button" onClick={() => setReportInspectorView('datasets')}>
                    <span className="report-inspector-overview-icon"><Database size={18} /></span>
                    <span><strong>数据源</strong><small>{reportContexts.length > 0 ? `${reportContexts.length} 个已关联` : '尚未关联'}</small></span>
                    <CaretRight size={16} />
                  </button>
                  <button type="button" onClick={() => setReportInspectorView('filters')}>
                    <span className="report-inspector-overview-icon"><Funnel size={18} /></span>
                    <span><strong>报告筛选</strong><small>{reportFilterCount > 0 ? `${reportFilterCount} 个已配置` : '固定显示在报告头下方'}</small></span>
                    <CaretRight size={16} />
                  </button>
                </div>
                <p className="report-inspector-overview-tip"><CirclesFour size={16} />选择画布中的卡片，可继续设置它的指标、维度与样式。</p>
              </section>}
              {reportInspectorView === 'datasets' && <section className="report-inspector-detail" aria-label="数据源配置">
                <header>
                  <button type="button" aria-label="返回报告配置" onClick={() => setReportInspectorView('overview')}><ArrowLeft size={17} /></button>
                  <div><h2>数据源</h2><span>{reportContexts.length} 个已关联</span></div>
                </header>
                <DataContextPanel definition={draft.definition} candidates={contexts} busy={dataBusy || !canEdit} error={dataError}
                  onAdd={candidate => void addDataContext(candidate)} onRemove={dataContextId => void removeDataContext(dataContextId)} />
              </section>}
              {reportInspectorView === 'filters' && <section className="report-inspector-detail" aria-label="报告筛选配置">
                <header>
                  <button type="button" aria-label="返回报告配置" onClick={() => setReportInspectorView('overview')}><ArrowLeft size={17} /></button>
                  <div><h2>报告筛选</h2><span>{reportFilterCount} 个已配置 · 固定区域</span></div>
                </header>
                <FilterPanel definition={draft.definition} candidates={contexts} fieldsOf={fieldsOf} selectedBlockId={selectedCardId}
                  busy={filterBusy || !canEdit} error={filterError}
                  onCreate={filterDraft => void createFilter(filterDraft)} onUpdate={(filter, filterDraft) => void updateFilter(filter, filterDraft)}
                  onDelete={filterId => void deleteFilter(filterId)} />
              </section>}
            </> : <div className="report-editor-panel-empty"><Sparkle size={20} /><strong>选择一个画布元素</strong><p>选中后可调整标题、说明和图表表现。</p></div>}
          </div>}
          {sidePanel === 'interaction' && <div className="report-editor-panel-body is-data is-interaction">
            {selectedComponent ? <>
              {selectedComponent.dataBinding && <EvidencePanel key={`evidence-${selectedComponent.id}`} reportId={reportId} component={selectedComponent} canEdit={canEdit} />}
              <InteractionPanel definition={draft.definition} manifests={manifests}
                sourceComponentId={selectedComponent.id} busy={interactionBusy} error={interactionError}
                onCreate={interactionDraft => void addInteraction(interactionDraft)}
                onDelete={interactionId => void removeInteraction(interactionId)} />
            </> : <div className="report-editor-panel-empty"><Sparkle size={20} /><strong>选择一个画布元素</strong><p>选中后可配置筛选联动、钻取和证据。</p></div>}
          </div>}
          {sidePanel === 'ai' && <div className="report-editor-panel-body is-ai">
          <header><div><h2>AI 改稿会话</h2><span>{aiPreview?.aiRunId || '等待开始'}</span></div><em className={aiPreview ? 'is-pending' : ''}>{previewing ? '思考中' : aiPreview ? '待确认' : '可输入'}</em></header>
          <section className={`report-editor-ai-message ${previewing ? 'is-thinking' : ''}`.trim()}><span><Sparkle size={15} weight="fill" /></span><p>{previewing ? '正在读取当前修订并校验可用数据与证据…' : aiPreview ? `我已生成 ${aiPreview.preview.bundle.operations.length} 项受控修改。你可以在执行计划中选择操作，确认后再应用到新修订。` : '告诉我想怎样修改这份报告。我会先生成可审查的执行计划，不会直接覆盖草稿。整稿改写目前处于试验阶段；针对单张卡片，推荐在「卡片配置」中使用「AI 识别指标与维度」。'}</p></section>
          <dl>
            <div><dt>范围</dt><dd>{scopeMode === 'page' ? '当前页面' : activeSection?.name || '当前章节'}</dd></div>
            <div><dt>基准修订</dt><dd>r{aiPreview?.preview.bundle.baseRevision ?? draft.revisionNo}</dd></div>
            <div><dt>影响对象</dt><dd>{aiPreview?.preview.affectedComponents.length ?? 0} 个组件</dd></div>
          </dl>
          <section className="report-editor-plan">
            <h3>执行计划 <span>（共 {aiPreview?.preview.bundle.operations.length ?? 0} 项）</span></h3>
            {aiPreview ? aiPreview.preview.bundle.operations.map((operation, index) => {
              const detail = operationDetail(operation, draft)
              const expanded = expandedOperations.has(index)
              return <article className={operationSelection.has(index) ? 'is-selected' : ''} key={`${operation.op}:${operation.targetId}:${index}`}>
                <button type="button" className="report-editor-plan-title" onClick={() => setOperationSelection(current => {
                  const next = new Set(current)
                  if (next.has(index)) next.delete(index); else next.add(index)
                  return next
                })}><CheckCircle size={17} weight={operationSelection.has(index) ? 'fill' : 'regular'} /><strong>{operationTitle(operation, draft)}</strong><span>{expanded ? <CaretDown size={14} /> : <CaretRight size={14} />}</span></button>
                {expanded && <div><p><span>操作</span><code>{detail.op}</code></p><p><span>目标</span>{detail.target}</p><p><span>载荷字段</span>{detail.payload}</p><button type="button" onClick={() => setExpandedOperations(current => { const next = new Set(current); next.delete(index); return next })}>收起详情</button></div>}
                {!expanded && <button className="report-editor-expand" type="button" onClick={() => setExpandedOperations(current => new Set(current).add(index))}>展开详情</button>}
              </article>
            }) : <div className="report-editor-plan-empty"><Sparkle size={22} /><strong>等待 AI 改稿要求</strong><p>AI 只会在所选作用域内生成受控修改方案。</p></div>}
          </section>
          {aiPreview && aiPreview.preview.bundle.operations.some(operation => operation.op.startsWith('INSIGHT_')) && <section className="report-editor-decision"><h3>待人工确认项</h3><p><WarningCircle size={15} />是否应用 AI 生成的智能结论？<small>结论文本需通过事实校验后才会随报告发布。</small></p><div><button className={partialDecision === 'retain' ? 'is-selected' : ''} type="button" onClick={() => setPartialDecision('retain')}>保留结论</button><button className={partialDecision === 'exclude' ? 'is-selected is-danger' : ''} type="button" onClick={() => setPartialDecision('exclude')}>排除结论</button></div></section>}
          <section className="report-editor-review-actions">
            <button className="primary-button" type="button" disabled={!aiPreview || applying || selectedOperationCount === 0} onClick={() => void applyPreview()}>{applying ? <><SpinnerGap className="is-spinning" size={16} />正在生成修订…</> : <>审查并应用 {selectedOperationCount} 项</>}</button>
            <button className="quiet-button" type="button" disabled={!aiPreview || applying} onClick={() => { setAIPreview(undefined); setActionError(''); notify('AI 方案已退回，草稿未发生变化') }}>退回 AI 调整</button>
          </section>
          <section className="report-editor-composer">
            <textarea aria-label="AI 改稿要求" value={intent} onChange={event => setIntent(event.target.value)} placeholder="描述你希望 AI 如何修改报告…" />
            <div>
              <select aria-label="AI 修改作用域" value={scopeMode} onChange={event => setScopeMode(event.target.value as typeof scopeMode)}><option value="page">当前页面</option><option value="section">当前章节</option></select>
              <button className="primary-button" type="button" disabled={previewing || !intent.trim() || !canAIEdit} onClick={() => void generatePreview()}>{previewing ? <SpinnerGap className="is-spinning" size={16} /> : <MagicWand size={16} />}<span>{aiPreview ? '重新生成' : '发送'}</span></button>
            </div>
          </section>
          </div>}
        </aside>
      </div>

      {/* 收据只陈述本地可确证的事实：修订号、操作数与绑定统计。
          阻断问题与证据校验结论由发布评审的确定性门禁给出，这里不预判。 */}
      <footer className="report-editor-statusbar">
        <span><strong>r{draft.revisionNo}</strong> 每次修改即自动保存为新修订，可撤销</span>
        <span>{sections.length} 个{sectionNoun} · {contentComponents.length} 个内容元素 · {contentComponents.filter(component => component.dataBinding).length} 个已绑定数据 · {(draft.definition.globalFilters ?? []).length} 个报告筛选</span>
        {lastReceipt && <span>上次 AI 应用：r{lastReceipt.from} → r{lastReceipt.to}，{lastReceipt.count} 项</span>}
        <span className="report-editor-statusbar-hint">{selectedComponent ? '已选中组件：Delete 删除 · Esc 取消选中' : '发布前检查在「预览与发布」中进行'}</span>
      </footer>
    </div>
    {jsonOpen && <DefinitionJSONDialog definition={draft.definition} onClose={() => setJsonOpen(false)} />}
    {componentLibraryOpen && <ComponentLibraryDialog manifests={latestComponentManifests(manifests.list())}
      reportContexts={reportContexts} contextNameOf={contextNameOf} fieldsOf={fieldsOf} defaultContextId={currentDataContextId}
      sectionName={activeSection?.name ?? ''}
      busy={componentBusy} error={componentError}
      onClose={() => setComponentLibraryOpen(false)}
      onAdd={(manifest, title, dataContextId) => pickComponentForSlot(manifest, undefined, title, dataContextId)} />}
    {deleteSectionOpen && activeSection && <div className="report-modal-backdrop" role="presentation" onMouseDown={() => setDeleteSectionOpen(false)}>
      <section className="report-modal report-delete-section-dialog" role="dialog" aria-modal="true" aria-labelledby="delete-section-title" onMouseDown={event => event.stopPropagation()}>
        <header><div><span className="eyebrow">结构变更</span><h2 id="delete-section-title">删除“{activeSection.name}”</h2></div><button type="button" aria-label="关闭" onClick={() => setDeleteSectionOpen(false)}><X size={18} /></button></header>
        <div><WarningCircle size={20} /><p>该章节内的 {activeSection.blocks.flatMap(block => block.zones.flatMap(zone => zone.slots.filter(slot => slot.componentId))).length} 个组件将一并移除。系统会生成新修订，仍可通过撤销恢复。</p>{componentError && <span>{componentError}</span>}</div>
        <footer><button className="quiet-button" type="button" disabled={componentBusy} onClick={() => setDeleteSectionOpen(false)}>取消</button><button className="primary-button is-danger" type="button" disabled={componentBusy} onClick={() => void deleteActiveSection()}>{componentBusy ? '正在删除…' : '确认删除'}</button></footer>
      </section>
    </div>}
    {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
  </AppShell>
}
