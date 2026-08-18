import {
  ArrowDown, ArrowLeft, ArrowUp, ArrowUDownLeft, ArrowUDownRight, BracketsCurly, CaretDown, CaretRight, Check,
  CheckCircle, CirclesFour, Info, MagicWand,
  NotePencil, PencilSimple, Plus, ShieldCheck, Sparkle, SpinnerGap, Trash, WarningCircle, X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState, type DragEvent, type ReactNode } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import '../styles/report.css'
import { RequestError } from '../lib/api'
import {
  reportEditorAPI, type AIPreviewResponse, type DataContextCandidate, type DataContextField,
  type EditorComponent, type EditorOperation, type EditorOperationBundle,
  type DraftExecution, type EditorScope, type ReportDraft, type ReportStarterTemplate,
} from '../report/api/editor'
import { reportAssetsAPI } from '../report/api/assets'
import type { ReportAsset } from '../report/assets/model'
import { ReportPageView } from '../report/render/ReportPageView'
import { InteractionPanel } from '../report/designer/InteractionPanel'
import { EvidencePanel } from '../report/designer/EvidencePanel'
import { DataContextPanel, DefinitionJSONDialog, FilterPanel } from '../report/designer/DataPanels'
import { ComponentPalette } from '../report/designer/ComponentPalette'
import {
  emptyManifestIndex, indexManifests, listComponentManifests, minimumSize,
  type ComponentManifest, type ManifestIndex,
} from '../report/render/manifests'
import {
  canvasOf, findBlock, findComponentBlock, orderedPages, orderedSections,
  type BindingRole, type ComponentOptions, type FieldBinding, type GlobalFilter, type Page, type ReportDefinition, type ReportType,
} from '../report/render/schema'
import {
  addComponentOperations, addDataContextOperations, addToCardOperations, bundle, createFilterOperations, createInteractionOperations,
  createSectionOperations, defaultBinding, deleteFilterOperations, deleteInteractionOperations, duplicateBlockOperations, layoutOperations,
  removeBlockOperations, removeComponentOperations, removeDataContextOperations, renameSectionOperations,
  replaceComponentOperations, sectionReorderOperations, slotLayoutOperations, updateComponentOperations, updateFilterOperations, updateReportSettingsOperations,
  paletteDragType, zoneKindLabels, zoneReorderOperations,
  type FilterDraft, type InteractionDraft, type ZoneKind,
} from '../report/designer/operations'

const reportTypeLabels: Record<ReportType, { name: string; hint: string }> = {
  REPORT: { name: '报告', hint: '分章节的分析文档：图表 + 结论 + 明细，可导出、可定时分发' },
  DASHBOARD: { name: '报表', hint: '以卡片和筛选器为主的看板：一屏多卡片，交互筛选、联动、钻取' },
}

/** 绑定角色 / 表现属性的中文标签：面板不再把 X_AXIS、colorPaletteRef 这类合同键直接摊给用户。 */
const roleLabels: Record<string, string> = {
  X_AXIS: '横轴', Y_AXIS: '数值', SERIES: '系列', COLOR: '颜色', LABEL: '标签', TIME: '时间', TOOLTIP: '提示',
  CATEGORY: '类目', VALUE: '数值', DIMENSION: '维度', DETAIL: '明细', SIZE: '大小',
}
const optionLabels: Record<string, string> = {
  showLegend: '显示图例', showLabel: '显示数值标签', smooth: '平滑曲线', colorPaletteRef: '配色方案', nullPolicy: '空值处理',
  animation: '动画效果', orientation: '方向', topN: '只显示前 N 项', numberFormat: '数字格式', tablePageSize: '每页行数',
  mobileLegendMode: '移动端图例', insightRole: '结论类型', imageAssetId: '图片素材 ID',
}
const optionEnumLabels: Record<string, string> = {
  ZERO: '按 0 处理', HIDE: '隐藏', GAP: '断开', HORIZONTAL: '横向', VERTICAL: '纵向', VISIBLE: '显示', HIDDEN: '隐藏', SCROLL: '可滚动',
  SUMMARY: '总结', TREND: '趋势', COMPARISON: '对比', ANOMALY: '异常', ACTION: '建议',
}
const roleLabel = (role: string) => roleLabels[role] ?? role

/** 组件加入报告的两种方式：新建一张卡片，或加入当前卡片的一个区域。 */
type Placement =
  | { mode: 'NEW_CARD' }
  | { mode: 'INTO_CARD'; zoneKind: ZoneKind }

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

function NewReportCanvas() {
  return <div className="report-editor-main report-editor-new-main">
    <nav className="report-editor-outline report-editor-new-outline" aria-label="报告大纲">
      <header><strong>报告大纲</strong></header>
      <div><strong>尚未创建报告草稿</strong></div>
    </nav>
    <main className="report-editor-canvas report-editor-new-canvas">
      <article className="report-editor-paper report-editor-blank-paper" aria-label="空白报告画布">
        <div className="report-editor-writing-caret" />
        <div className="report-editor-blank-hint"><Sparkle size={18} weight="fill" /><span>创建后在这里编排报告结构</span></div>
      </article>
    </main>
  </div>
}

type NewReportMode = 'blank' | 'template' | 'ai' | 'import'

const newReportModeLabels: Record<NewReportMode, string> = { blank: '空白', template: '模板', ai: 'AI 生成', import: 'JSON 导入' }

/**
 * 新建面板：公共信息（名称、类型、数据来源）+ 四种起步方式页签 + 始终可见的主按钮。
 *  - 空白 / 模板 / JSON 导入：只依赖已发布的数据集版本，未配置模型提供方时依然可用；
 *  - AI 生成：额外需要可用的模型提供方，失败时可改用空白新建。
 * 四条入口产出的都是同一种 Report Definition，进入同一个编辑器与同一条发布链。
 */
function NewReportTaskPanel({
  name, reportType, intent, contexts, contextId, contextsLoading, contextsError, templates, templatesLoading, selectedTemplateId, creating, error,
  importText, onName, onReportType, onIntent, onContext, onTemplate, onCreateBlank, onCreateTemplate, onGenerate, onImportText, onImport,
}: {
  name: string
  reportType: ReportType
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
  importText: string
  onName: (value: string) => void
  onReportType: (value: ReportType) => void
  onIntent: (value: string) => void
  onImportText: (value: string) => void
  onImport: () => void
  onContext: (value: string) => void
  onTemplate: (value: string) => void
  onCreateBlank: () => void
  onCreateTemplate: () => void
  onGenerate: () => void
}) {
  const [mode, setMode] = useState<NewReportMode>('blank')
  const selected = contexts.find(item => item.dataContext.id === contextId)
  const ready = !contextsLoading && !contextsError && contexts.length > 0
  const busy = Boolean(creating)
  const primary = {
    blank: { label: '创建空白报告', icon: <NotePencil size={16} />, disabled: !ready || !name.trim(), run: onCreateBlank },
    template: { label: '使用所选模板创建', icon: <CirclesFour size={16} />, disabled: !ready || !selectedTemplateId || !name.trim(), run: onCreateTemplate },
    ai: { label: '让 AI 生成', icon: <MagicWand size={16} />, disabled: !ready || !intent.trim(), run: onGenerate },
    import: { label: '导入为新草稿', icon: <BracketsCurly size={16} />, disabled: !importText.trim(), run: onImport },
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
        {mode === 'ai' && <div className="report-editor-new-actions">
          <article>
            <div><strong>让 AI 生成结构</strong><small>由 AI 规划章节与组件并生成初始草稿；需要已配置的模型提供方，生成结果仍需人工确认后才能发布。</small></div>
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
      <button className="primary-button" type="button" disabled={busy || primary.disabled} onClick={primary.run}>
        {busy ? <SpinnerGap className="is-spinning" size={16} /> : primary.icon}
        <span>{busy ? '正在创建…' : primary.label}</span>
      </button>
    </footer>
  </aside>
}

export type ManualEditResult = {
  options: ComponentOptions
  binding?: { dimensions: FieldBinding[]; measures: FieldBinding[] }
  /** 卡片绑定的数据集（报告内的数据上下文）；改绑数据集会随绑定一起写入。 */
  dataContextId?: string
  /** 更换展示类型：先 COMPONENT_REPLACE 到新清单，再写入属性与绑定。 */
  replaceWith?: ComponentManifest
}

type BindingSuggestion = { dimensions: FieldBinding[]; measures: FieldBinding[]; rationale?: string }

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
function CardInspector({ component, manifest: currentManifest, manifests, reportContexts, contextNameOf, fieldsOf, defaultContextId, canAI, onSuggest, busy, error, onClose, onSave, onDelete, filterPanel }: {
  component?: EditorComponent
  manifest?: ComponentManifest
  onDelete: () => void
  /** 作用于该卡片的过滤字段面板（由页面注入，避免面板自持草稿状态）。 */
  filterPanel: ReactNode
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
  const [options, setOptions] = useState<ComponentOptions>(() => ({ ...component?.options }))
  const [dimensions, setDimensions] = useState<FieldBinding[]>(component?.dataBinding?.dimensions ?? [])
  const [measures, setMeasures] = useState<FieldBinding[]>(component?.dataBinding?.measures ?? [])
  const [dataContextId, setDataContextId] = useState(component?.dataBinding?.dataContextId ?? defaultContextId)
  const [manifestRef, setManifestRef] = useState(currentManifest ? `${currentManifest.type}@${currentManifest.version}` : '')
  const [suggesting, setSuggesting] = useState(false)
  const [suggestion, setSuggestion] = useState<BindingSuggestion | null>(null)
  const [suggestError, setSuggestError] = useState('')

  const manifest = manifests.find(item => `${item.type}@${item.version}` === manifestRef) ?? currentManifest
  const replacing = Boolean(currentManifest && manifest && (manifest.type !== currentManifest.type || manifest.version !== currentManifest.version))
  const fields = fieldsOf(dataContextId)
  const contract = manifest?.dataContract
  // SEMANTIC_IR 绑定固定语义发布版本，只能由问数或 AI 编排产生，面板不得改写。
  const semanticBound = component?.dataBinding?.bindingMode === 'SEMANTIC_IR'
  // 可切换到的展示类型：同一渲染家族之外也允许（例如柱状图换成表格），绑定会按新合同重映射。
  const switchable = manifests.filter(item => item.renderer !== 'CONTROL' && item.renderer !== 'IMAGE')
  const dimensionFields = fields.filter(field => field.role !== 'MEASURE')
  const measureFields = fields.filter(field => field.role === 'MEASURE')
  const bindable = Boolean(contract) && fields.length > 0 && !semanticBound
  const roles = contract?.roles ?? []
  const dimensionRoles = roles.filter(role => role !== 'VALUE' && role !== 'Y_AXIS')
  const measureRoles = roles.filter(role => role === 'VALUE' || role === 'Y_AXIS' || role === 'SIZE')
  const dimensionsValid = !contract || (dimensions.length >= contract.dimensions.min && dimensions.length <= contract.dimensions.max)
  const measuresValid = !contract || (measures.length >= contract.measures.min && measures.length <= contract.measures.max)
  const complete = dimensions.every(item => item.field && item.role && dimensionFields.some(field => field.code === item.field)) &&
    measures.every(item => item.field && item.role && measureFields.some(field => field.code === item.field))
  const bindingValid = !bindable || (dimensionsValid && measuresValid && complete)

  // 表现属性直接由清单的 optionSchema 生成，避免面板与渲染器各自维护一份白名单。
  const styleProperties = Object.entries(manifest?.optionSchema.properties ?? {})
    .filter(([name]) => name !== 'title' && name !== 'subtitle' && name !== 'richText')
    .sort(([left], [right]) => left.localeCompare(right))

  const setOption = (name: string, value: unknown) =>
    setOptions(current => ({ ...current, [name]: value }))

  const applyBinding = (next: { dimensions: FieldBinding[]; measures: FieldBinding[] }) => {
    setDimensions(next.dimensions.filter(item => item.field))
    setMeasures(next.measures.filter(item => item.field))
  }
  const changeContext = (nextId: string) => {
    setDataContextId(nextId); setSuggestion(null); setSuggestError('')
    // 换数据集后旧字段不再成立：按新数据集的字段角色重新填充下限绑定。
    if (manifest) applyBinding(defaultBinding(manifest, fieldsOf(nextId)))
  }
  const changeManifest = (ref: string) => {
    setManifestRef(ref); setSuggestion(null); setSuggestError('')
    const next = manifests.find(item => `${item.type}@${item.version}` === ref)
    if (!next) return
    // 表现属性只保留新清单 optionSchema 认识的键（标题/副标题始终保留），否则服务端会拒绝未知选项。
    setOptions(current => pruneOptions(current, next))
    // 展示类型变化时按新合同的角色白名单与上限重映射当前绑定。
    const roles = next.dataContract.roles
    const dimensionFallback = roles.find(role => role !== 'VALUE' && role !== 'Y_AXIS' && role !== 'SIZE') ?? 'DIMENSION'
    const measureFallback = roles.find(role => role === 'VALUE' || role === 'Y_AXIS' || role === 'SIZE') ?? 'VALUE'
    setDimensions(current => current.slice(0, next.dataContract.dimensions.max).map(item => ({ ...item, role: roles.includes(item.role) ? item.role : dimensionFallback })))
    setMeasures(current => current.slice(0, next.dataContract.measures.max).map(item => ({ ...item, role: roles.includes(item.role) ? item.role : measureFallback })))
  }
  const suggest = async () => {
    if (!onSuggest || !manifest) return
    setSuggesting(true); setSuggestError(''); setSuggestion(null)
    try {
      const result = await onSuggest({ dataContextId, manifest, title: options.title ?? '' })
      if (!result) { setSuggestError('AI 没有给出可用的绑定建议，可改用「按字段角色填充」。'); return }
      setSuggestion(result)
      applyBinding(result)
    } catch (cause) {
      setSuggestError(cause instanceof Error ? cause.message : 'AI 识别失败')
    } finally { setSuggesting(false) }
  }

  const rows = (
    label: string, items: FieldBinding[], allowedRoles: BindingRole[], max: number,
    availableFields: DataContextField[],
    onChange: (next: FieldBinding[]) => void,
  ) => <div className="report-editor-binding-group">
    <div className="report-editor-binding-head">
      <strong>{label}</strong>
      <button type="button" disabled={items.length >= max || allowedRoles.length === 0}
        onClick={() => onChange([...items, { role: allowedRoles[0], field: availableFields[0]?.code ?? '' }])}>添加</button>
    </div>
    {items.length === 0 && <p className="report-editor-binding-empty">尚未选择{label}</p>}
    {items.map((item, index) => <div className={`report-editor-binding-row ${allowedRoles.length > 1 ? '' : 'is-single-role'}`.trim()} key={`${label}-${index}`}>
      {allowedRoles.length > 1
        ? <select aria-label={`${label}角色`} value={item.role} title="该字段在图中扮演的角色"
          onChange={event => onChange(items.map((row, position) => position === index ? { ...row, role: event.target.value as BindingRole } : row))}>
          {allowedRoles.map(role => <option key={role} value={role}>{roleLabel(role)}</option>)}
        </select>
        : <span className="report-editor-binding-role" title={item.role}>{roleLabel(item.role)}</span>}
      <select aria-label={`${label}字段`} value={item.field}
        onChange={event => onChange(items.map((row, position) => position === index ? { ...row, field: event.target.value } : row))}>
        {availableFields.map(field => <option key={field.code} value={field.code}>{field.name || field.code} · {field.code}</option>)}
      </select>
      <button type="button" aria-label={`移除${label}`}
        onClick={() => onChange(items.filter((_, position) => position !== index))}><X size={14} /></button>
    </div>)}
  </div>

  return <section className="report-card-inspector" aria-labelledby="manual-editor-title">
      <header>
        <div><span className="eyebrow">卡片配置</span><h2 id="manual-editor-title">{component?.options.title || component?.templateRef.type || '组件'}</h2></div>
        <button type="button" aria-label="取消选中" onClick={onClose}><X size={18} /></button>
      </header>
      <div className="report-editor-manual-form">
        <h4 className="report-inspector-group">数据</h4>
        {!semanticBound && reportContexts.length > 0 && <label>数据集
          <select aria-label="卡片数据集" value={dataContextId} onChange={event => changeContext(event.target.value)}>
            {reportContexts.map(context => <option key={context.id} value={context.id}>{contextNameOf(context.id)}</option>)}
          </select>
        </label>}
        {!semanticBound && switchable.length > 0 && <label>展示类型
          <select aria-label="展示类型" value={manifestRef} onChange={event => changeManifest(event.target.value)}>
            {switchable.map(item => <option key={`${item.type}@${item.version}`} value={`${item.type}@${item.version}`}>{item.displayName}</option>)}
          </select>
          {replacing && <small className="report-editor-binding-note"><Info size={13} />保存后卡片将换成「{manifest?.displayName}」，指标与维度已按新图形重新对应。</small>}
        </label>}

        {semanticBound && <p className="report-editor-binding-note">
          <ShieldCheck size={15} />该组件使用 SEMANTIC_IR 绑定并固定了语义发布版本，只能通过语义升级流程调整。
        </p>}
        {!semanticBound && !contract && <p className="report-editor-binding-note">
          <Info size={15} />未找到该组件的模板合同，暂不能在此编辑数据绑定。
        </p>}
        {!semanticBound && contract && fields.length === 0 && <p className="report-editor-binding-note">
          <WarningCircle size={15} />当前报告的数据上下文没有可用字段。
        </p>}

        {bindable && contract && <div className="report-editor-binding">
          <p className="report-editor-binding-contract">
            这类卡片需要 {contract.measures.min === contract.measures.max ? contract.measures.min : `${contract.measures.min}～${contract.measures.max}`} 个指标
            {contract.dimensions.max > 0 ? `、${contract.dimensions.min === contract.dimensions.max ? contract.dimensions.min : `${contract.dimensions.min}～${contract.dimensions.max}`} 个维度` : '，不需要维度'}
          </p>
          <div className="report-editor-binding-assist">
            <button type="button" disabled={busy || suggesting || !manifest} title="按数据集里字段的角色（度量/维度/时间）自动填入" onClick={() => manifest && applyBinding(defaultBinding(manifest, fields))}>自动填充</button>
            <button type="button" disabled={busy || suggesting || !canAI || !onSuggest || !manifest} title={canAI ? '让模型按卡片标题在数据集里挑选指标与维度' : '当前不可用：需要模型提供方与 AI 编辑权限'} onClick={() => void suggest()}>
              {suggesting ? <SpinnerGap className="is-spinning" size={14} /> : <MagicWand size={14} />}{suggesting ? 'AI 识别中…' : 'AI 识别指标与维度'}
            </button>
          </div>
          {suggestion && <p className="report-editor-binding-note"><Sparkle size={14} weight="fill" />AI 建议已填入：{suggestion.dimensions.length} 个维度、{suggestion.measures.length} 个指标{suggestion.rationale ? `（${suggestion.rationale}）` : ''}。点击「保存」才会生效。</p>}
          {suggestError && <p className="report-editor-inline-error"><WarningCircle size={15} />{suggestError}</p>}
          {rows('指标', measures, measureRoles, contract.measures.max, measureFields, setMeasures)}
          {contract.dimensions.max > 0 && rows('维度', dimensions, dimensionRoles, contract.dimensions.max, dimensionFields, setDimensions)}
          {!measuresValid && <p className="report-editor-inline-error"><WarningCircle size={15} />指标数量不符合要求</p>}
          {!dimensionsValid && <p className="report-editor-inline-error"><WarningCircle size={15} />维度数量不符合要求</p>}
        </div>}

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
        {error && <div className="report-editor-inline-error"><WarningCircle size={15} />{error}</div>}
      </div>
      <footer>
        <button className="quiet-button is-danger" type="button" disabled={busy} onClick={onDelete}><Trash size={15} />删除卡片</button>
        <button className="primary-button" type="button" disabled={busy || !options.title?.trim() || !bindingValid}
          onClick={() => onSave({
            options: { ...(manifest ? pruneOptions(options, manifest) : options), title: options.title?.trim(), subtitle: options.subtitle?.trim() },
            binding: bindable ? { dimensions, measures } : undefined,
            dataContextId: bindable ? dataContextId : undefined,
            replaceWith: replacing ? manifest : undefined,
          })}>{busy ? '正在保存…' : '保存'}</button>
      </footer>
      {filterPanel}
    </section>
}

function ComponentLibraryDialog({ manifests, reportContexts, contextNameOf, fieldsOf, defaultContextId, sectionName, cardName, busy, error, onClose, onAdd }: {
  manifests: ComponentManifest[]
  reportContexts: Array<{ id: string; alias?: string }>
  contextNameOf: (dataContextId: string) => string
  fieldsOf: (dataContextId: string) => DataContextField[]
  defaultContextId: string
  sectionName: string
  /** 当前选中组件所在的卡片；为空时只能新建卡片。 */
  cardName: string
  busy: boolean
  error: string
  onClose: () => void
  onAdd: (manifest: ComponentManifest, title: string, placement: Placement, dataContextId: string) => void
}) {
  const [placement, setPlacement] = useState<Placement>({ mode: 'NEW_CARD' })
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
          </dl>}
          <div className="report-component-placement">
            <strong>放置方式</strong>
            <label>
              <input type="radio" name="placement" checked={placement.mode === 'NEW_CARD'}
                onChange={() => setPlacement({ mode: 'NEW_CARD' })} />
              <span>新建卡片<small>成为一张可独立拖拽的卡片</small></span>
            </label>
            <label className={cardName ? '' : 'is-disabled'}>
              <input type="radio" name="placement" disabled={!cardName}
                checked={placement.mode === 'INTO_CARD'}
                onChange={() => setPlacement({ mode: 'INTO_CARD', zoneKind: 'INSIGHT' })} />
              <span>加入「{cardName || '当前卡片'}」<small>与卡片内已有内容一起移动和缩放</small></span>
            </label>
            {placement.mode === 'INTO_CARD' && <select aria-label="卡片内区域" value={placement.zoneKind}
              onChange={event => setPlacement({ mode: 'INTO_CARD', zoneKind: event.target.value as ZoneKind })}>
              {(Object.keys(zoneKindLabels) as ZoneKind[]).map(kind =>
                <option key={kind} value={kind}>{zoneKindLabels[kind]}</option>)}
            </select>}
          </div>
          {needsData && <p className={enoughFields && contextId ? '' : 'is-error'}><Info size={15} />{enoughFields && contextId ? `将按「${contextNameOf(contextId)}」的字段角色预填 ${selected?.dataContract.dimensions.min ?? 0} 个维度、${selected?.dataContract.measures.min ?? 0} 个度量；加入后点击卡片可改数据集、展示类型与绑定，也可让 AI 识别度量与维度。` : '所选数据集的可用字段不足，无法满足此组件合同。'}</p>}
          {!needsData && <p><Info size={15} />该组件无需数据绑定，可直接加入报告结构。</p>}
          {error && <p className="is-error"><WarningCircle size={15} />{error}</p>}
        </aside>
      </div>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={busy || !selected || !title.trim() || (needsData && (!enoughFields || !contextId))} onClick={() => selected && onAdd(selected, title.trim(), placement, contextId)}>{busy ? '正在添加…' : '添加到报告'}</button></footer>
    </section>
  </div>
}

/**
 * 报告编辑器。
 *
 * 画布用的是与运行页完全相同的 ReportPageView：编辑器只是多传了一个 editing
 * 回调，把拖拽与缩放翻译成 BLOCK_MOVE / BLOCK_RESIZE 受控 Operation。因此
 * 「编辑时看到的排版」与「发布后看到的排版」来自同一份 JSON 和同一套渲染实现。
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
  const [importText, setImportText] = useState('')
  const [contexts, setContexts] = useState<DataContextCandidate[]>([])
  const [contextId, setContextId] = useState('')
  const [contextsLoading, setContextsLoading] = useState(newMode)
  const [contextsError, setContextsError] = useState('')
  const [starterTemplates, setStarterTemplates] = useState<ReportStarterTemplate[]>([])
  const [starterTemplatesLoading, setStarterTemplatesLoading] = useState(newMode)
  const [selectedTemplateId, setSelectedTemplateId] = useState('')
  const [newCreating, setNewCreating] = useState<'' | 'blank' | 'template' | 'ai' | 'import'>('')
  const [scopeMode, setScopeMode] = useState<'page' | 'section'>('page')
  const [activeSectionId, setActiveSectionId] = useState('')
  const [selectedComponentId, setSelectedComponentId] = useState('')
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
  const [sidePanel, setSidePanel] = useState<'ai' | 'data'>('data')
  const [lastReceipt, setLastReceipt] = useState<{ from: number; to: number; count: number; source: string } | null>(null)
  const [renamingSectionId, setRenamingSectionId] = useState('')
  const [renamingTitle, setRenamingTitle] = useState(false)

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
  }), [draft])

  const executing = Boolean(draft) && settledSignature !== dataSignature

  // 按当前草稿执行组件查询。执行按当前用户权限进行，不产生任何版本或制品。
  useEffect(() => {
    if (newMode || !reportId) return undefined
    const page = draft ? orderedPages(draft.definition)[0] : undefined
    if (!page) return undefined
    const controller = new AbortController()
    void reportEditorAPI.executeDraft(reportId, { pageId: page.id }, { signal: controller.signal })
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
  const activeSection = sections.find(section => section.id === activeSectionId) ?? sections[0]
  const selectedComponent = useMemo(
    () => draft?.definition.components.find(component => component.id === selectedComponentId),
    [draft, selectedComponentId],
  )
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
  const contextNameOf = (dataContextId: string) =>
    reportContexts.find(context => context.id === dataContextId)?.alias ||
    contexts.find(item => item.dataContext.id === dataContextId)?.name || dataContextId
  const selectedManifest = selectedComponent
    ? manifests.get(selectedComponent.templateRef.type, selectedComponent.templateRef.version)
    : undefined
  const canAIEdit = Boolean(asset?.allowedActions.includes('AI_EDIT'))
  const canEdit = Boolean(asset?.allowedActions.includes('EDIT'))
  const canPublish = Boolean(asset?.allowedActions.includes('PUBLISH'))
  const selectedOperationCount = aiPreview?.preview.bundle.operations.filter((_, index) => operationSelection.has(index)).length ?? 0
  const results = useMemo(
    () => new Map((execution?.components ?? []).map(item => [item.componentId, item])),
    [execution],
  )

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
    await commit(createFilterOperations(draft.definition, filterDraft, () => crypto.randomUUID()), '筛选器已保存为新修订', setFilterError)
    setFilterBusy(false)
  }

  const updateFilter = async (filter: GlobalFilter, filterDraft: FilterDraft) => {
    if (!draft || !canEdit) return
    setFilterBusy(true); setFilterError('')
    await commit(updateFilterOperations(filter, filterDraft), '筛选器作用范围已更新', setFilterError)
    setFilterBusy(false)
  }

  const deleteFilter = async (filterId: string) => {
    if (!draft || !canEdit) return
    setFilterBusy(true); setFilterError('')
    await commit(deleteFilterOperations(filterId), '筛选器已移除', setFilterError)
    setFilterBusy(false)
  }

  const addComponent = async (
    manifest: ComponentManifest, title: string, placement: Placement, dataContextId: string,
    drop?: { sectionId?: string; rect: { x: number; y: number } },
  ) => {
    if (!draft || !page || !canEdit) return
    setComponentBusy(true); setComponentError('')
    const contextID = dataContextId || currentDataContextId
    const contextFields = fieldsOf(contextID)
    if (placement.mode === 'INTO_CARD' && selectedCardId) {
      const { operations, componentId } = addToCardOperations({
        page, blockId: selectedCardId, zoneKind: placement.zoneKind,
        manifest, title, dataContextId: contextID, fields: contextFields,
        newId: () => crypto.randomUUID(),
      })
      const saved = await commit(operations, `${manifest.displayName}已加入当前卡片并生成新修订`, setComponentError)
      if (saved) { setSelectedComponentId(componentId); setComponentLibraryOpen(false) }
      setComponentBusy(false)
      return
    }
    const { operations, sectionId, componentId } = addComponentOperations({
      definition: draft.definition, page, sectionId: drop?.sectionId ?? activeSection?.id, manifest, title,
      dataContextId: contextID, fields: contextFields, newId: () => crypto.randomUUID(),
      preferredRect: drop?.rect, sectionName: `${sectionNoun} 1`,
    })
    const saved = await commit(operations, `${manifest.displayName}已加入报告并生成新修订`, drop ? setActionError : setComponentError)
    if (saved) {
      setActiveSectionId(sectionId); setSelectedComponentId(componentId); setComponentLibraryOpen(false)
    }
    setComponentBusy(false)
  }

  /**
   * 从组件面板拖到画布：按落点所在章节的网格算出行列，卡片就落在那里；落在章节
   * 外或空章节时放到该章节（或最后一个章节）的起点。没有章节时新建章节。
   */
  const dropFromPalette = (event: DragEvent<HTMLElement>) => {
    if (!draft || !page || !canEdit) return
    const ref = event.dataTransfer.getData(paletteDragType)
    if (!ref) return
    event.preventDefault()
    const [type, version] = ref.split('@')
    const manifest = manifests.get(type, version)
    if (!manifest) return
    const target = (event.target as HTMLElement).closest('.report-render-section') as HTMLElement | null
    const sectionId = target?.dataset.sectionId
    let rect = { x: 0, y: 0 }
    const grid = target?.querySelector('.report-render-grid') as HTMLElement | null
    if (grid && sectionId) {
      const canvas = canvasOf(draft.definition).desktop
      const bounds = grid.getBoundingClientRect()
      const cellWidth = (bounds.width + canvas.gapX) / canvas.columns
      const rowHeight = canvas.baseRowHeight + canvas.gapY
      const section = page.sections.find(item => item.id === sectionId)
      const originY = section && section.blocks.length ? Math.min(...section.blocks.map(block => block.layout.desktop.y)) : 0
      rect = {
        x: Math.max(0, Math.floor((event.clientX - bounds.left) / cellWidth)),
        y: Math.max(0, originY + Math.floor((event.clientY - bounds.top) / rowHeight)),
      }
    }
    void addComponent(manifest, manifest.displayName, { mode: 'NEW_CARD' }, currentDataContextId, { sectionId, rect })
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
    const saved = await commit(result.operations, '卡片已复制', setActionError)
    if (saved && result.componentId) setSelectedComponentId(result.componentId)
  }

  const deleteBlock = async (blockId: string) => {
    if (!draft || !page || !canEdit) return
    const operations = removeBlockOperations(page, blockId)
    const saved = await commit(operations, '卡片已删除（可撤销）', setActionError)
    if (saved && selectedCardId === blockId) setSelectedComponentId('')
  }

  const sectionNoun = draft?.definition.metadata.reportType === 'DASHBOARD' ? '分区' : '章节'

  const addSection = async () => {
    if (!draft || !page || !canEdit) return
    const { operations, sectionId } = createSectionOperations(page, `${sectionNoun} ${sections.length + 1}`, () => crypto.randomUUID())
    const saved = await commit(operations, `已新建${sectionNoun}`, setActionError)
    if (saved) { setActiveSectionId(sectionId); setRenamingSectionId(sectionId) }
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
        name: newName.trim(), description: intent.trim(), dataContextId: contextId || undefined, reportType: newReportType,
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
      const result = await reportEditorAPI.createAI({ intent: intent.trim(), reportType: newReportType })
      navigate(`/reports/${result.report.id}?mode=edit`, { replace: true })
    } catch (cause) {
      setActionError(cause instanceof Error
        ? `${cause.message}（可改用「创建空白报告」，它不依赖模型提供方）`
        : 'AI 新建报告失败，可改用「创建空白报告」')
    } finally { setNewCreating('') }
  }

  const createFromTemplate = async () => {
    if (!newName.trim() || !contextId || !selectedTemplateId || newCreating) return
    setNewCreating('template'); setActionError('')
    try {
      const result = await reportEditorAPI.instantiateStarterTemplate(selectedTemplateId, {
        name: newName.trim(), description: intent.trim(), dataContextId: contextId, reportType: newReportType,
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
      if ((event.key === 'Delete' || event.key === 'Backspace') && selectedCardId) {
        event.preventDefault(); void deleteBlock(selectedCardId)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
    // undoRedo/deleteBlock 每次渲染都是新函数；它们只读最新的 draft/selection，用最新引用即可。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [newMode, selectedCardId, draft?.revisionNo, canEdit])

  if (newMode) return <AppShell className="report-editor-shell" lockBusinessDomain>
    <div className="report-editor-workspace report-editor-new-workspace">
      <header className="report-editor-header"><div><button type="button" onClick={() => navigate('/reports')}><ArrowLeft size={15} />返回报告工作台</button><div className="report-editor-title"><h1>新建报告</h1><span>草稿 r0</span><small>创建后进入编辑器</small></div></div></header>
      <div className="report-editor-body"><NewReportCanvas /><NewReportTaskPanel
        name={newName} reportType={newReportType} intent={intent} contexts={contexts} contextId={contextId}
        contextsLoading={contextsLoading} contextsError={contextsError}
        templates={starterTemplates} templatesLoading={starterTemplatesLoading} selectedTemplateId={selectedTemplateId}
        creating={newCreating} error={actionError} importText={importText}
        onName={setNewName} onReportType={setNewReportType} onIntent={setIntent} onContext={setContextId} onTemplate={setSelectedTemplateId}
        onCreateBlank={() => void createBlankReport()} onCreateTemplate={() => void createFromTemplate()} onGenerate={() => void createNewReport()}
        onImportText={setImportText} onImport={() => void importDefinition()}
      /></div>
    </div>
  </AppShell>

  if (loading) return <AppShell className="report-editor-shell" lockBusinessDomain defaultSidebarCollapsed><div className="report-editor-loading"><SpinnerGap className="is-spinning" size={28} /><strong>正在读取报告草稿与权限</strong><p>随后加载可复用的已发布运行结果。</p></div></AppShell>
  if (!draft || !page) return <AppShell className="report-editor-shell" lockBusinessDomain defaultSidebarCollapsed><div className="report-editor-loading is-error"><WarningCircle size={28} /><strong>报告编辑器无法打开</strong><p>{loadError || '当前草稿没有可编辑页面。'}</p><button type="button" onClick={() => navigate('/reports')}>返回报告工作台</button></div></AppShell>

  return <AppShell className="report-editor-shell" lockBusinessDomain defaultSidebarCollapsed>
    <div className="report-editor-workspace">
      <header className="report-editor-header">
        <div>
          <button type="button" onClick={() => navigate('/reports')}><ArrowLeft size={15} />返回报告工作台</button>
          <div className="report-editor-title">
            {renamingTitle
              ? <input className="report-editor-title-input" autoFocus defaultValue={draft.definition.metadata.name} maxLength={80} aria-label="报告名称"
                onBlur={event => void renameReport(event.target.value)}
                onKeyDown={event => { if (event.key === 'Enter') (event.target as HTMLInputElement).blur(); if (event.key === 'Escape') setRenamingTitle(false) }} />
              : <h1 title={canEdit ? '点击重命名' : ''} onClick={() => canEdit && setRenamingTitle(true)}>{draft.definition.metadata.name}{canEdit && <PencilSimple size={13} />}</h1>}
            <span>{reportTypeLabels[draft.definition.metadata.reportType]?.name ?? draft.definition.metadata.reportType} · 草稿 r{draft.revisionNo}</span><small>已自动保存 {dateTime(draft.updatedAt).split(' ').at(-1)}</small>
          </div>
        </div>
        <div className="report-editor-header-actions">
          <button className="quiet-button" type="button" onClick={() => setJsonOpen(true)}><BracketsCurly size={16} />定义 JSON</button>
          <button type="button" aria-label="撤销" disabled={!canEdit || applying} onClick={() => void undoRedo(false)}><ArrowUDownLeft size={20} /></button>
          <button type="button" aria-label="重做" disabled={!canEdit || applying} onClick={() => void undoRedo(true)}><ArrowUDownRight size={20} /></button>
          <button className="quiet-button" type="button" disabled={!canEdit || manifests.list().length === 0} title="按对话框方式添加组件（也可以直接从左侧组件面板拖拽）" onClick={() => { setComponentError(''); setComponentLibraryOpen(true) }}><Plus size={16} />添加组件</button>
          <button className="quiet-button" type="button" disabled={!canPublish || Boolean(aiPreview)} title={aiPreview ? '请先应用或退回当前 AI 方案' : ''} onClick={() => navigate(`/reports/${reportId}/publish-review`)}><ShieldCheck size={16} />预览与发布</button>
        </div>
      </header>

      {actionError && <div className="report-editor-action-error" role="alert"><WarningCircle size={16} /><span>{actionError}</span>{actionError.includes('最新修订') && <button type="button" onClick={() => void reloadDraft()}>打开最新修订</button>}<button type="button" aria-label="关闭错误" onClick={() => setActionError('')}><X size={14} /></button></div>}

      <div className="report-editor-body">
        <div className="report-editor-main">
          <nav className="report-editor-outline report-editor-sidebar" aria-label="组件面板与大纲">
            <ComponentPalette manifests={manifests.list()} disabled={!canEdit}
              onPick={manifest => void addComponent(manifest, manifest.displayName, { mode: 'NEW_CARD' }, currentDataContextId)} />
            <header><strong>{sectionNoun}</strong>{canEdit && <button type="button" className="report-outline-add" title={`新建${sectionNoun}`} onClick={() => void addSection()}><Plus size={13} />新建</button>}</header>
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
            {sections.length === 0 && <p>拖入第一张卡片即自动创建{sectionNoun}</p>}
          </nav>
          <main className={`report-editor-canvas ${aiPreview ? 'has-ai-proposal' : ''}`.trim()}
            onDragOver={event => { if (event.dataTransfer.types.includes(paletteDragType)) { event.preventDefault(); event.dataTransfer.dropEffect = 'copy' } }}
            onDrop={dropFromPalette}>
            <article className="report-editor-paper report-editor-live-paper" onClick={event => { if ((event.target as HTMLElement).closest('.report-render-block')) return; setSelectedComponentId('') }}>
              <header>
                <div><h2>{draft.definition.metadata.name}</h2><ShieldCheck size={17} weight="fill" /></div>
                <small>{sections.length ? '拖动卡片顶部移动、右下角缩放；点击卡片在右侧配置' : '从左侧组件面板把卡片拖到这里'}</small>
              </header>
              {draft.definition.metadata.description && <p className="report-editor-description">{draft.definition.metadata.description}</p>}
              <p className="report-editor-preview-status" aria-live="polite">
                {executing
                  ? <><SpinnerGap className="is-spinning" size={14} />正在按你的权限执行当前草稿…</>
                  : executionError
                    ? <><WarningCircle size={14} />{executionError}</>
                    : execution
                      ? <><CheckCircle size={14} weight="fill" />草稿预览 r{execution.revisionNo} · 数据截至 {dateTime(execution.asOf)}</>
                      : <><Info size={14} />尚未执行草稿预览</>}
              </p>
              {/* 首次执行返回前按设计态呈现；之后组件状态一律来自真实执行结果。 */}
              <ReportPageView definition={draft.definition} page={page} manifests={manifests} results={results}
                designMode={!execution}
                selectedComponentId={selectedComponentId}
                onSelectComponent={(componentId, blockId) => {
                  setSelectedComponentId(componentId)
                  if (componentId) setSidePanel('data')
                  const located = findComponentBlock(page, componentId)
                  if (located) setActiveSectionId(located.section.id)
                  else if (blockId) setActiveSectionId(activeSectionId)
                }}
                editing={canEdit ? {
                  onLayoutChange: (sectionId, blockId, rect) => void changeLayout(sectionId, blockId, rect),
                  onSlotLayoutChange: (blockId, zoneId, slotId, rect) => void changeSlotLayout(blockId, zoneId, slotId, rect),
                  onZoneReorder: (blockId, zoneId, direction) => void reorderZone(blockId, zoneId, direction),
                  onDuplicateBlock: (_sectionId, blockId) => void duplicateBlock(blockId),
                  onDeleteBlock: (_sectionId, blockId) => void deleteBlock(blockId),
                } : undefined} />
            </article>
            {aiPreview && <span className="report-editor-preview-label">AI 方案预览，不会自动保存</span>}
          </main>
        </div>

        <aside className="report-editor-task-panel">
          <div className="report-editor-panel-tabs" role="tablist" aria-label="侧栏面板">
            <button type="button" role="tab" aria-selected={sidePanel === 'data'} className={sidePanel === 'data' ? 'is-active' : ''} onClick={() => setSidePanel('data')}>{selectedComponent ? '卡片配置' : '数据与筛选'}</button>
            <button type="button" role="tab" aria-selected={sidePanel === 'ai'} className={sidePanel === 'ai' ? 'is-active' : ''} onClick={() => setSidePanel('ai')}>AI 改稿<small>试验</small></button>
          </div>
          {sidePanel === 'data' && <div className="report-editor-panel-body is-data">
            {selectedComponent ? <>
              <CardInspector
                key={selectedComponent.id}
                component={selectedComponent} manifest={selectedManifest} manifests={manifests.list()}
                reportContexts={reportContexts} contextNameOf={contextNameOf} fieldsOf={fieldsOf} defaultContextId={currentDataContextId}
                canAI={canAIEdit} onSuggest={suggestBinding}
                busy={manualBusy || !canEdit} error={manualError}
                onClose={() => setSelectedComponentId('')} onSave={result => void saveManual(result)}
                onDelete={() => void deleteSelectedComponent()}
                filterPanel={<FilterPanel definition={draft.definition} candidates={contexts} fieldsOf={fieldsOf} selectedBlockId={selectedCardId}
                  onlyBlock defaultContextId={selectedComponent.dataBinding?.dataContextId ?? currentDataContextId}
                  busy={filterBusy || !canEdit} error={filterError}
                  onCreate={filterDraft => void createFilter(filterDraft)} onUpdate={(filter, filterDraft) => void updateFilter(filter, filterDraft)}
                  onDelete={filterId => void deleteFilter(filterId)} />}
              />
              {selectedComponent.dataBinding && <EvidencePanel key={`evidence-${selectedComponent.id}`} reportId={reportId} component={selectedComponent} canEdit={canEdit} />}
              <InteractionPanel definition={draft.definition} manifests={manifests}
                sourceComponentId={selectedComponent.id} busy={interactionBusy} error={interactionError}
                onCreate={interactionDraft => void addInteraction(interactionDraft)}
                onDelete={interactionId => void removeInteraction(interactionId)} />
            </> : <>
              <p className="report-editor-binding-note report-editor-panel-intro"><Info size={15} />点击画布上的卡片可配置它的数据集、指标与过滤字段；下面是报告级的数据集与筛选器。</p>
              <DataContextPanel definition={draft.definition} candidates={contexts} busy={dataBusy || !canEdit} error={dataError}
                onAdd={candidate => void addDataContext(candidate)} onRemove={dataContextId => void removeDataContext(dataContextId)} />
              <FilterPanel definition={draft.definition} candidates={contexts} fieldsOf={fieldsOf} selectedBlockId={selectedCardId}
                busy={filterBusy || !canEdit} error={filterError}
                onCreate={filterDraft => void createFilter(filterDraft)} onUpdate={(filter, filterDraft) => void updateFilter(filter, filterDraft)}
                onDelete={filterId => void deleteFilter(filterId)} />
            </>}
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
        <span>{sections.length} 个{sectionNoun} · {draft.definition.components.length} 张卡片 · {draft.definition.components.filter(component => component.dataBinding).length} 张已绑定数据 · {(draft.definition.globalFilters ?? []).length} 个筛选器</span>
        {lastReceipt && <span>上次 AI 应用：r{lastReceipt.from} → r{lastReceipt.to}，{lastReceipt.count} 项</span>}
        <span className="report-editor-statusbar-hint">{selectedComponent ? '已选中卡片：Delete 删除 · Esc 取消选中' : '发布前检查在「预览与发布」中进行'}</span>
      </footer>
    </div>
    {jsonOpen && <DefinitionJSONDialog definition={draft.definition} onClose={() => setJsonOpen(false)} />}
    {componentLibraryOpen && <ComponentLibraryDialog manifests={manifests.list()}
      reportContexts={reportContexts} contextNameOf={contextNameOf} fieldsOf={fieldsOf} defaultContextId={currentDataContextId}
      sectionName={activeSection?.name ?? ''}
      cardName={selectedCard ? selectedComponent?.options.title || '当前卡片' : ''}
      busy={componentBusy} error={componentError}
      onClose={() => setComponentLibraryOpen(false)}
      onAdd={(manifest, title, placement, dataContextId) => void addComponent(manifest, title, placement, dataContextId)} />}
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
