import {
  ArrowDown, ArrowLeft, ArrowUp, ArrowUDownLeft, ArrowUDownRight, CaretDown, CaretRight, Check,
  CheckCircle, CirclesFour, Info, MagicWand,
  NotePencil, Plus, ShieldCheck, Sparkle, SpinnerGap, Trash, WarningCircle, X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
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
import {
  emptyManifestIndex, indexManifests, listComponentManifests, minimumSize,
  type ComponentManifest, type ManifestIndex,
} from '../report/render/manifests'
import {
  canvasOf, findComponentBlock, orderedPages, orderedSections,
  type BindingRole, type ComponentOptions, type FieldBinding, type Page,
} from '../report/render/schema'
import {
  addComponentOperations, addToCardOperations, bundle, createInteractionOperations,
  deleteInteractionOperations, layoutOperations, removeComponentOperations,
  sectionReorderOperations, updateComponentOperations, zoneKindLabels,
  type InteractionDraft, type ZoneKind,
} from '../report/designer/operations'

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

/**
 * 新建面板提供三条并列入口：
 *  - 空白新建与模板新建：只依赖已发布的数据集版本，未配置模型提供方时依然可用；
 *  - AI 生成：额外需要可用的模型提供方，失败时回退到空白新建。
 * 三条入口产出的都是同一种 Report Definition，进入同一个编辑器与同一条发布链。
 */
function NewReportTaskPanel({
  name, intent, contexts, contextId, contextsLoading, contextsError, templates, templatesLoading, selectedTemplateId, creating, error,
  onName, onIntent, onContext, onTemplate, onCreateBlank, onCreateTemplate, onGenerate,
}: {
  name: string
  intent: string
  contexts: DataContextCandidate[]
  contextId: string
  contextsLoading: boolean
  contextsError: string
  templates: ReportStarterTemplate[]
  templatesLoading: boolean
  selectedTemplateId: string
  creating: '' | 'blank' | 'template' | 'ai'
  error: string
  onName: (value: string) => void
  onIntent: (value: string) => void
  onContext: (value: string) => void
  onTemplate: (value: string) => void
  onCreateBlank: () => void
  onCreateTemplate: () => void
  onGenerate: () => void
}) {
  const selected = contexts.find(item => item.dataContext.id === contextId)
  const ready = !contextsLoading && !contextsError && contexts.length > 0
  return <aside className="report-editor-task-panel report-editor-new-panel">
    <header><div><h2>新建报告</h2><span>草稿 r0</span></div><em className={creating ? 'is-pending' : ''}>{creating ? '创建中' : '待创建'}</em></header>

    <section className="report-editor-new-form">
      <label>报告名称
        <input value={name} onChange={event => onName(event.target.value)} placeholder="例如：2026 年 7 月经营月报" maxLength={80} />
      </label>
      <label>数据来源
        <select aria-label="受治理数据上下文" value={contextId} disabled={!ready} onChange={event => onContext(event.target.value)}>
          {contexts.map(item => <option key={item.dataContext.id} value={item.dataContext.id}>{item.name}</option>)}
        </select>
      </label>
      {contextsLoading && <p className="report-editor-new-hint"><SpinnerGap className="is-spinning" size={14} />正在读取当前领域可用的已发布数据集…</p>}
      {contextsError && <p className="report-editor-new-hint is-error"><WarningCircle size={14} />{contextsError}</p>}
      {ready && selected && <p className="report-editor-new-hint">
        <Info size={14} />
        {selected.description || '已发布数据集版本'}·可用字段 {selected.fields.length} 个（已按你的列权限裁剪）
      </p>}
      {!contextsLoading && !contextsError && contexts.length === 0 && <p className="report-editor-new-hint is-error">
        <WarningCircle size={14} />当前业务领域还没有已发布的数据集版本，请先在「数据集」中发布一个版本。
      </p>}
    </section>

    <section className="report-editor-template-center" aria-label="报告模板中心">
      <header><div><strong>从模板开始</strong><small>直接生成可编辑的真实草稿和受治理数据绑定</small></div><span>{templates.length} 个模板</span></header>
      {templatesLoading && <p className="report-editor-new-hint"><SpinnerGap className="is-spinning" size={14} />正在读取模板…</p>}
      <div>{templates.map(template => <button className={selectedTemplateId === template.id ? 'is-selected' : ''} type="button" key={template.id} onClick={() => onTemplate(template.id)}><span>{template.category}</span><strong>{template.name}</strong><small>{template.description}</small><em>{template.componentCount} 个组件</em></button>)}</div>
      <button className="primary-button" type="button" disabled={Boolean(creating) || !ready || !selectedTemplateId || !name.trim()} onClick={onCreateTemplate}>{creating === 'template' ? <SpinnerGap className="is-spinning" size={16} /> : <CirclesFour size={16} />}{creating === 'template' ? '正在创建…' : '使用所选模板'}</button>
    </section>

    {error && <div className="report-editor-new-error" role="alert"><WarningCircle size={15} /><span>{error}</span></div>}

    <section className="report-editor-new-actions">
      <article>
        <div><strong>空白报告</strong><small>创建空白草稿，随后在编辑器中手动添加组件与数据绑定。不需要模型提供方。</small></div>
        <button className="primary-button" type="button" disabled={Boolean(creating) || !ready || !name.trim()} onClick={onCreateBlank}>
          {creating === 'blank' ? <SpinnerGap className="is-spinning" size={16} /> : <NotePencil size={16} />}
          <span>{creating === 'blank' ? '创建中' : '创建空白报告'}</span>
        </button>
      </article>
      <article>
        <div><strong>让 AI 生成结构</strong><small>由 AI 规划章节与组件并生成初始草稿；需要已配置的模型提供方，生成结果仍需人工确认后才能发布。</small></div>
        <textarea aria-label="AI 报告要求" value={intent} onChange={event => onIntent(event.target.value)} placeholder="描述报告目标、受众与期间…" />
        <button className="quiet-button" type="button" disabled={Boolean(creating) || !ready || !intent.trim()} onClick={onGenerate}>
          {creating === 'ai' ? <SpinnerGap className="is-spinning" size={16} /> : <MagicWand size={16} />}
          <span>{creating === 'ai' ? '生成中' : '让 AI 生成'}</span>
        </button>
      </article>
    </section>
  </aside>
}

export type ManualEditResult = {
  options: ComponentOptions
  binding?: { dimensions: FieldBinding[]; measures: FieldBinding[] }
}

/**
 * 属性与数据绑定面板。
 *
 * 可编辑的表现属性由组件清单的 optionSchema 生成，因此面板暴露的每一项都是
 * 渲染器真正会读取的配置；绑定只能使用服务端返回的受治理字段与合同声明的角色。
 * 提交仍走 COMPONENT_UPDATE / DATA_BINDING_UPDATE 受控 Operation。
 */
function ManualEditDialog({ component, manifest, fields, busy, error, onClose, onSave }: {
  component?: EditorComponent
  manifest?: ComponentManifest
  fields: DataContextField[]
  busy: boolean
  error: string
  onClose: () => void
  onSave: (result: ManualEditResult) => void
}) {
  const [options, setOptions] = useState<ComponentOptions>(() => ({ ...component?.options }))
  const [dimensions, setDimensions] = useState<FieldBinding[]>(component?.dataBinding?.dimensions ?? [])
  const [measures, setMeasures] = useState<FieldBinding[]>(component?.dataBinding?.measures ?? [])

  const contract = manifest?.dataContract
  // SEMANTIC_IR 绑定固定语义发布版本，只能由问数或 AI 编排产生，面板不得改写。
  const semanticBound = component?.dataBinding?.bindingMode === 'SEMANTIC_IR'
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
    {items.map((item, index) => <div className="report-editor-binding-row" key={`${label}-${index}`}>
      <select aria-label={`${label}角色`} value={item.role}
        onChange={event => onChange(items.map((row, position) => position === index ? { ...row, role: event.target.value as BindingRole } : row))}>
        {allowedRoles.map(role => <option key={role} value={role}>{role}</option>)}
      </select>
      <select aria-label={`${label}字段`} value={item.field}
        onChange={event => onChange(items.map((row, position) => position === index ? { ...row, field: event.target.value } : row))}>
        {availableFields.map(field => <option key={field.code} value={field.code}>{field.name || field.code} · {field.code}</option>)}
      </select>
      <button type="button" aria-label={`移除${label}`}
        onClick={() => onChange(items.filter((_, position) => position !== index))}><X size={14} /></button>
    </div>)}
  </div>

  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-editor-manual-modal" role="dialog" aria-modal="true" aria-labelledby="manual-editor-title" onMouseDown={event => event.stopPropagation()}>
      <header>
        <div><span className="eyebrow">属性与数据绑定</span><h2 id="manual-editor-title">{component?.options.title || component?.templateRef.type || '组件'}</h2></div>
        <button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button>
      </header>
      <div className="report-editor-manual-form">
        <p><Info size={15} />属性与绑定都通过受控 Operation 写入新修订，不会直接覆盖历史。</p>
        <label>组件标题<input value={options.title ?? ''} onChange={event => setOption('title', event.target.value)} /></label>
        <label>组件副标题<input value={options.subtitle ?? ''} onChange={event => setOption('subtitle', event.target.value)} /></label>
        {manifest?.renderer === 'TEXT' && <label>文字内容
          <textarea rows={4} value={options.richText ?? ''} onChange={event => setOption('richText', event.target.value)} />
        </label>}

        {styleProperties.length > 0 && <div className="report-editor-style-group">
          <strong>表现设置</strong>
          {styleProperties.map(([name, schema]) => {
            const value = (options as Record<string, unknown>)[name]
            if (schema.type === 'boolean') {
              return <label className="report-editor-style-toggle" key={name}>
                <input type="checkbox" checked={value === true} onChange={event => setOption(name, event.target.checked)} />
                <span>{schema.description || name}</span>
              </label>
            }
            if (schema.enum?.length) {
              return <label key={name}>{schema.description || name}
                <select value={String(value ?? '')} onChange={event => setOption(name, event.target.value || undefined)}>
                  <option value="">默认</option>
                  {schema.enum.map(item => <option key={item} value={item}>{item}</option>)}
                </select>
              </label>
            }
            if (schema.type === 'integer' || schema.type === 'number') {
              return <label key={name}>{schema.description || name}
                <input type="number" min={schema.minimum} max={schema.maximum} value={value === undefined ? '' : String(value)}
                  onChange={event => setOption(name, event.target.value === '' ? undefined : Number(event.target.value))} />
              </label>
            }
            return <label key={name}>{schema.description || name}
              <input value={String(value ?? '')} onChange={event => setOption(name, event.target.value || undefined)} />
            </label>
          })}
        </div>}

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
            组件合同：维度 {contract.dimensions.min}～{contract.dimensions.max} 个，度量 {contract.measures.min}～{contract.measures.max} 个
          </p>
          {rows('维度', dimensions, dimensionRoles, contract.dimensions.max, dimensionFields, setDimensions)}
          {rows('度量', measures, measureRoles, contract.measures.max, measureFields, setMeasures)}
          {!dimensionsValid && <p className="report-editor-inline-error"><WarningCircle size={15} />维度数量不满足组件合同</p>}
          {!measuresValid && <p className="report-editor-inline-error"><WarningCircle size={15} />度量数量不满足组件合同</p>}
        </div>}

        {error && <div className="report-editor-inline-error"><WarningCircle size={15} />{error}</div>}
      </div>
      <footer>
        <button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button>
        <button className="primary-button" type="button" disabled={busy || !options.title?.trim() || !bindingValid}
          onClick={() => onSave({
            options: { ...options, title: options.title?.trim(), subtitle: options.subtitle?.trim() },
            binding: bindable ? { dimensions, measures } : undefined,
          })}>{busy ? '正在保存…' : '保存为新修订'}</button>
      </footer>
    </section>
  </div>
}

function ComponentLibraryDialog({ manifests, fields, contextId, sectionName, cardName, busy, error, onClose, onAdd }: {
  manifests: ComponentManifest[]
  fields: DataContextField[]
  contextId: string
  sectionName: string
  /** 当前选中组件所在的卡片；为空时只能新建卡片。 */
  cardName: string
  busy: boolean
  error: string
  onClose: () => void
  onAdd: (manifest: ComponentManifest, title: string, placement: Placement) => void
}) {
  const [placement, setPlacement] = useState<Placement>({ mode: 'NEW_CARD' })
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
          {needsData && <p className={enoughFields && contextId ? '' : 'is-error'}><Info size={15} />{enoughFields && contextId ? `将从当前受治理数据上下文中配置 ${selected?.dataContract.dimensions.min ?? 0} 个维度、${selected?.dataContract.measures.min ?? 0} 个度量；加入后可在画布上拖拽调整位置与大小。` : '当前数据上下文的可用字段不足，无法满足此组件合同。'}</p>}
          {!needsData && <p><Info size={15} />该组件无需数据绑定，可直接加入报告结构。</p>}
          {error && <p className="is-error"><WarningCircle size={15} />{error}</p>}
        </aside>
      </div>
      <footer><button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" type="button" disabled={busy || !selected || !title.trim() || (needsData && (!enoughFields || !contextId))} onClick={() => selected && onAdd(selected, title.trim(), placement)}>{busy ? '正在添加…' : '添加到报告'}</button></footer>
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
  const [contexts, setContexts] = useState<DataContextCandidate[]>([])
  const [contextId, setContextId] = useState('')
  const [contextsLoading, setContextsLoading] = useState(newMode)
  const [contextsError, setContextsError] = useState('')
  const [starterTemplates, setStarterTemplates] = useState<ReportStarterTemplate[]>([])
  const [starterTemplatesLoading, setStarterTemplatesLoading] = useState(newMode)
  const [selectedTemplateId, setSelectedTemplateId] = useState('')
  const [newCreating, setNewCreating] = useState<'' | 'blank' | 'template' | 'ai'>('')
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
  const [manualOpen, setManualOpen] = useState(false)
  const [manualBusy, setManualBusy] = useState(false)
  const [manualError, setManualError] = useState('')
  const [componentLibraryOpen, setComponentLibraryOpen] = useState(false)
  const [componentBusy, setComponentBusy] = useState(false)
  const [componentError, setComponentError] = useState('')
  const [deleteSectionOpen, setDeleteSectionOpen] = useState(false)
  const [interactionBusy, setInteractionBusy] = useState(false)
  const [interactionError, setInteractionError] = useState('')
  const [lastReceipt, setLastReceipt] = useState<{ from: number; to: number; count: number; source: string } | null>(null)

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
  const fieldsForDraft = useMemo(
    () => governedFieldDefinitions(contexts.find(item => item.dataContext.id === currentDataContextId)),
    [contexts, currentDataContextId],
  )
  // 绑定面板只能使用当前报告数据上下文对应的、服务端已按列权限裁剪的字段。
  const bindableFields = useMemo(() => {
    const contextID = selectedComponent?.dataBinding?.dataContextId ?? currentDataContextId
    return governedFieldDefinitions(contexts.find(item => item.dataContext.id === contextID))
  }, [contexts, currentDataContextId, selectedComponent])
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

  const saveManual = async (result: ManualEditResult) => {
    if (!draft || !selectedComponent) return
    setManualBusy(true); setManualError('')
    const operations = updateComponentOperations(selectedComponent, result.options, result.binding, currentDataContextId)
    const saved = await commit(operations, result.binding ? '属性与数据绑定已保存为新修订' : '组件属性已保存为新修订', setManualError)
    if (saved) setManualOpen(false)
    setManualBusy(false)
  }

  const addComponent = async (manifest: ComponentManifest, title: string, placement: Placement) => {
    if (!draft || !page || !canEdit) return
    setComponentBusy(true); setComponentError('')
    if (placement.mode === 'INTO_CARD' && selectedCardId) {
      const { operations, componentId } = addToCardOperations({
        page, blockId: selectedCardId, zoneKind: placement.zoneKind,
        manifest, title, dataContextId: currentDataContextId, fields: fieldsForDraft,
        newId: () => crypto.randomUUID(),
      })
      const saved = await commit(operations, `${manifest.displayName}已加入当前卡片并生成新修订`, setComponentError)
      if (saved) { setSelectedComponentId(componentId); setComponentLibraryOpen(false) }
      setComponentBusy(false)
      return
    }
    const { operations, sectionId, componentId } = addComponentOperations({
      definition: draft.definition, page, sectionId: activeSection?.id, manifest, title,
      dataContextId: currentDataContextId, fields: fieldsForDraft, newId: () => crypto.randomUUID(),
    })
    const saved = await commit(operations, `${manifest.displayName}已加入报告并生成新修订`, setComponentError)
    if (saved) {
      setActiveSectionId(sectionId); setSelectedComponentId(componentId); setComponentLibraryOpen(false)
    }
    setComponentBusy(false)
  }

  const deleteSelectedComponent = async () => {
    if (!draft || !page || !selectedComponent || !canEdit) return
    const operations = removeComponentOperations(page, selectedComponent.id)
    const saved = await commit(operations, '组件已移除并生成新修订', setActionError)
    if (saved) setSelectedComponentId('')
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

  const moveSection = async (direction: -1 | 1) => {
    if (!draft || !page || !activeSection || !canEdit) return
    const operations = sectionReorderOperations(page, activeSection.id, direction)
    await commit(operations, direction < 0 ? '章节已上移' : '章节已下移', setActionError)
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
        name: newName.trim(), description: intent.trim(), dataContextId: contextId || undefined,
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
      const result = await reportEditorAPI.createAI({ intent: intent.trim() })
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
        name: newName.trim(), description: intent.trim(), dataContextId: contextId,
      })
      navigate(`/reports/${result.report.id}?mode=edit`, { replace: true })
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : '模板报告创建失败，请检查所选数据集字段')
    } finally { setNewCreating('') }
  }

  if (newMode) return <AppShell className="report-editor-shell" lockBusinessDomain>
    <div className="report-editor-workspace report-editor-new-workspace">
      <header className="report-editor-header"><div><button type="button" onClick={() => navigate('/reports')}><ArrowLeft size={15} />返回报告工作台</button><div className="report-editor-title"><h1>新建报告</h1><span>草稿 r0</span><small>创建后进入编辑器</small></div></div></header>
      <div className="report-editor-body"><NewReportCanvas /><NewReportTaskPanel
        name={newName} intent={intent} contexts={contexts} contextId={contextId}
        contextsLoading={contextsLoading} contextsError={contextsError}
        templates={starterTemplates} templatesLoading={starterTemplatesLoading} selectedTemplateId={selectedTemplateId}
        creating={newCreating} error={actionError}
        onName={setNewName} onIntent={setIntent} onContext={setContextId} onTemplate={setSelectedTemplateId}
        onCreateBlank={() => void createBlankReport()} onCreateTemplate={() => void createFromTemplate()} onGenerate={() => void createNewReport()}
      /></div>
    </div>
  </AppShell>

  if (loading) return <AppShell className="report-editor-shell" lockBusinessDomain><div className="report-editor-loading"><SpinnerGap className="is-spinning" size={28} /><strong>正在读取报告草稿与权限</strong><p>随后加载可复用的已发布运行结果。</p></div></AppShell>
  if (!draft || !page) return <AppShell className="report-editor-shell" lockBusinessDomain><div className="report-editor-loading is-error"><WarningCircle size={28} /><strong>报告编辑器无法打开</strong><p>{loadError || '当前草稿没有可编辑页面。'}</p><button type="button" onClick={() => navigate('/reports')}>返回报告工作台</button></div></AppShell>

  return <AppShell className="report-editor-shell" lockBusinessDomain>
    <div className="report-editor-workspace">
      <header className="report-editor-header">
        <div>
          <button type="button" onClick={() => navigate('/reports')}><ArrowLeft size={15} />返回报告工作台</button>
          <div className="report-editor-title"><h1>{draft.definition.metadata.name}</h1><span>草稿 r{draft.revisionNo}</span><small>已自动保存 {dateTime(draft.updatedAt).split(' ').at(-1)}</small></div>
        </div>
        <div className="report-editor-header-actions">
          <button type="button" aria-label="撤销" disabled={!canEdit || applying} onClick={() => void undoRedo(false)}><ArrowUDownLeft size={20} /></button>
          <button type="button" aria-label="重做" disabled={!canEdit || applying} onClick={() => void undoRedo(true)}><ArrowUDownRight size={20} /></button>
          <button className="quiet-button" type="button" disabled={!canEdit || manifests.list().length === 0} onClick={() => { setComponentError(''); setComponentLibraryOpen(true) }}><Plus size={16} />添加组件</button>
          <button className="quiet-button" type="button" disabled={!canEdit || !selectedComponent} onClick={() => setManualOpen(true)}><NotePencil size={16} />属性与绑定</button>
          <button className="quiet-button is-danger" type="button" disabled={!canEdit || !selectedComponent} onClick={() => void deleteSelectedComponent()}><Trash size={16} />删除组件</button>
          <button className="quiet-button" type="button" disabled={!canPublish || Boolean(aiPreview)} title={aiPreview ? '请先应用或退回当前 AI 方案' : ''} onClick={() => navigate(`/reports/${reportId}/publish-review`)}><ShieldCheck size={16} />预览与发布</button>
        </div>
      </header>

      <div className="report-editor-structure-actions" aria-label="章节布局操作">
        <span>当前章节：<strong>{activeSection?.name || '尚未添加章节'}</strong></span>
        <span className="report-editor-selection-hint">{selectedComponent ? `已选中：${selectedComponent.options.title || selectedComponent.templateRef.type}` : '点击画布上的组件即可选中并编辑'}</span>
        <button type="button" disabled={!activeSection || sections[0]?.id === activeSection?.id} onClick={() => void moveSection(-1)}><ArrowUp size={15} />上移</button>
        <button type="button" disabled={!activeSection || sections.at(-1)?.id === activeSection?.id} onClick={() => void moveSection(1)}><ArrowDown size={15} />下移</button>
        <button className="is-danger" type="button" disabled={!activeSection} onClick={() => { setComponentError(''); setDeleteSectionOpen(true) }}><Trash size={15} />删除章节</button>
      </div>

      {actionError && <div className="report-editor-action-error" role="alert"><WarningCircle size={16} /><span>{actionError}</span>{actionError.includes('最新修订') && <button type="button" onClick={() => void reloadDraft()}>打开最新修订</button>}<button type="button" aria-label="关闭错误" onClick={() => setActionError('')}><X size={14} /></button></div>}

      <div className="report-editor-body">
        <div className="report-editor-main">
          <nav className="report-editor-outline" aria-label="报告大纲">
            <header><strong>报告大纲</strong></header>
            {sections.map(section => <button className={section.id === activeSectionId ? 'is-active' : ''} type="button" key={section.id}
              onClick={() => {
                setActiveSectionId(section.id)
                document.getElementById(`report-section-${section.id}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
              }}><span />{section.name}</button>)}
            {sections.length === 0 && <p>尚未添加章节</p>}
          </nav>
          <main className={`report-editor-canvas ${aiPreview ? 'has-ai-proposal' : ''}`.trim()}>
            <article className="report-editor-paper report-editor-live-paper">
              <header>
                <div><h2>{draft.definition.metadata.name}</h2><ShieldCheck size={17} weight="fill" /></div>
                <small>拖动区块顶部的把手移动位置，拖动右下角调整大小；每次改动都会保存为新修订。</small>
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
                  const located = findComponentBlock(page, componentId)
                  if (located) setActiveSectionId(located.section.id)
                  else if (blockId) setActiveSectionId(activeSectionId)
                }}
                editing={canEdit ? { onLayoutChange: (sectionId, blockId, rect) => void changeLayout(sectionId, blockId, rect) } : undefined} />
            </article>
            {aiPreview && <span className="report-editor-preview-label">AI 方案预览，不会自动保存</span>}
          </main>
        </div>

        <aside className="report-editor-task-panel">
          <header><div><h2>AI 改稿会话</h2><span>{aiPreview?.aiRunId || '等待开始'}</span></div><em className={aiPreview ? 'is-pending' : ''}>{previewing ? '思考中' : aiPreview ? '待确认' : '可输入'}</em></header>
          <section className={`report-editor-ai-message ${previewing ? 'is-thinking' : ''}`.trim()}><span><Sparkle size={15} weight="fill" /></span><p>{previewing ? '正在读取当前修订并校验可用数据与证据…' : aiPreview ? `我已生成 ${aiPreview.preview.bundle.operations.length} 项受控修改。你可以在执行计划中选择操作，确认后再应用到新修订。` : '告诉我想怎样修改这份报告。我会先生成可审查的执行计划，不会直接覆盖草稿。'}</p></section>
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
          {selectedComponent && selectedComponent.dataBinding && <EvidencePanel
            key={selectedComponent.id} reportId={reportId} component={selectedComponent} canEdit={canEdit} />}

          {selectedComponent && <InteractionPanel definition={draft.definition} manifests={manifests}
            sourceComponentId={selectedComponent.id} busy={interactionBusy} error={interactionError}
            onCreate={interactionDraft => void addInteraction(interactionDraft)}
            onDelete={interactionId => void removeInteraction(interactionId)} />}

          <section className="report-editor-composer">
            <textarea aria-label="AI 改稿要求" value={intent} onChange={event => setIntent(event.target.value)} placeholder="描述你希望 AI 如何修改报告…" />
            <div>
              <select aria-label="AI 修改作用域" value={scopeMode} onChange={event => setScopeMode(event.target.value as typeof scopeMode)}><option value="page">当前页面</option><option value="section">当前章节</option></select>
              <button className="primary-button" type="button" disabled={previewing || !intent.trim() || !canAIEdit} onClick={() => void generatePreview()}>{previewing ? <SpinnerGap className="is-spinning" size={16} /> : <MagicWand size={16} />}<span>{aiPreview ? '重新生成' : '发送'}</span></button>
            </div>
          </section>
        </aside>
      </div>

      {/* 收据只陈述本地可确证的事实：修订号、操作数与绑定统计。
          阻断问题与证据校验结论由发布评审的确定性门禁给出，这里不预判。 */}
      <footer className="report-editor-receipt">
        <span className="report-editor-receipt-label"><CaretDown size={15} />修改收据</span>
        <div><strong>r{lastReceipt?.from ?? draft.revisionNo}</strong><span>→</span><strong>r{lastReceipt?.to ?? draft.revisionNo + (aiPreview ? 1 : 0)}</strong><small>目标修订</small></div>
        <div><strong>{lastReceipt?.count ?? selectedOperationCount}</strong><small>{lastReceipt ? '已执行操作' : '拟执行操作'}</small></div>
        <div><strong>{draft.definition.components.length}</strong><small>组件</small></div>
        <div><strong>{draft.definition.components.filter(component => component.dataBinding).length}</strong><small>已绑定数据</small></div>
        <span>发布前检查将在「预览与发布」中给出阻断与告警项</span>
      </footer>
    </div>
    {manualOpen && <ManualEditDialog
      key={selectedComponent?.id}
      component={selectedComponent} manifest={selectedManifest} fields={bindableFields}
      busy={manualBusy} error={manualError}
      onClose={() => setManualOpen(false)} onSave={result => void saveManual(result)}
    />}
    {componentLibraryOpen && <ComponentLibraryDialog manifests={manifests.list()} fields={fieldsForDraft} contextId={currentDataContextId}
      sectionName={activeSection?.name ?? ''}
      cardName={selectedCard ? selectedComponent?.options.title || '当前卡片' : ''}
      busy={componentBusy} error={componentError}
      onClose={() => setComponentLibraryOpen(false)}
      onAdd={(manifest, title, placement) => void addComponent(manifest, title, placement)} />}
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
