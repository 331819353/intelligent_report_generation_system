import {
  ArrowLeft, ArrowUDownLeft, ArrowUDownRight, CaretDown, CaretRight, Check,
  CheckCircle, CirclesFour, DotsThree, Info, MagicWand,
  NotePencil, Paperclip, ShieldCheck, Sparkle, SpinnerGap, WarningCircle, X,
} from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { RequestError } from '../lib/api'
import {
  reportEditorAPI, type AIPreviewResponse, type ComponentManifest, type DataContextCandidate,
  type EditorComponent, type EditorOperation, type EditorOperationBundle, type EditorPage,
  type EditorScope, type ReportDraft,
} from '../report/api/editor'
import { reportAssetsAPI } from '../report/api/assets'
import { reportRuntimeAPI, type LoadedReport, type RuntimeExecution, type RuntimeQueryResult } from '../report/api/runtime'
import type { ReportAsset } from '../report/assets/model'
import { ChannelContributionChart, RuntimeResultChart, RuntimeRevenueTrendChart } from '../report/runtime/ReportCharts'

type PublishedPreview = { loaded: LoadedReport; execution: RuntimeExecution }
type PartialDecision = 'retain' | 'exclude'

const snapshotReportId = '00000000-0000-4000-8000-000000000701'
const snapshotPageId = 'page_monthly_operations'
const snapshotSections = [
  { id: 'section_summary', name: '经营摘要', order: 0, blocks: [] },
  { id: 'section_revenue', name: '营业收入趋势', order: 1, blocks: [{ id: 'block_revenue', type: 'CHART', zones: [{ id: 'zone_revenue', type: 'CONTENT', slots: [{ id: 'slot_revenue', componentId: 'component_revenue' }] }] }] },
  { id: 'section_channel', name: '渠道贡献分析', order: 2, blocks: [{ id: 'block_channel', type: 'CHART', zones: [{ id: 'zone_channel', type: 'CONTENT', slots: [{ id: 'slot_channel', componentId: 'component_channel' }] }] }] },
  { id: 'section_conclusion', name: '经营结论', order: 3, blocks: [{ id: 'block_conclusion', type: 'CONTENT', zones: [{ id: 'zone_conclusion', type: 'INSIGHT', slots: [{ id: 'slot_conclusion', componentId: 'component_conclusion' }] }] }] },
] satisfies EditorPage['sections']

const snapshotDraft: ReportDraft = {
  reportId: snapshotReportId, tenantId: 'snapshot-tenant', schemaVersion: '1.0', revisionNo: 24,
  definitionHash: '7c9b6cc154ced4ee91e5b77abcc6a8d86e790132d3bcc65cac88c70f7c71e5f6',
  updatedBy: '王敏', updatedAt: '2026-08-10T10:42:00+08:00',
  definition: {
    schemaVersion: '1.0', metadata: { id: snapshotReportId, code: 'RP-OPS-MON-001', name: '2026年7月经营月报', reportType: 'REPORT', locale: 'zh-CN' },
    dataContexts: [{ id: 'context_enterprise_operations', alias: '企业经营主题集' }],
    pages: [{ id: snapshotPageId, name: '月度经营总览', order: 0, sections: snapshotSections }],
    components: [
      { id: 'component_revenue', templateRef: { type: 'bar-comparison', version: '1.0.0' }, dataBinding: { bindingMode: 'DATASET_FIELD', dataContextId: 'context_enterprise_operations', dimensions: [{ role: 'TIME', field: 'month' }], measures: [{ role: 'VALUE', field: 'revenue' }] }, options: { title: '营业收入趋势', subtitle: '万元', showLegend: true, mobileLegendMode: 'SCROLL' } },
      { id: 'component_channel', templateRef: { type: 'pie-donut', version: '1.0.0' }, dataBinding: { bindingMode: 'DATASET_FIELD', dataContextId: 'context_enterprise_operations', dimensions: [{ role: 'CATEGORY', field: 'channel' }], measures: [{ role: 'VALUE', field: 'revenue' }] }, options: { title: '渠道贡献分析', showLegend: true } },
      { id: 'component_conclusion', templateRef: { type: 'insight-text', version: '1.0.0' }, options: { title: '经营结论', richText: '收入实现稳健增长，线上渠道贡献提升，盈利能力持续优化。', insightRole: 'CONCLUSION' } },
    ],
  },
}

const snapshotPreview: AIPreviewResponse = {
  aiRunId: 'AI-20260810-042',
  preview: {
    beforeHash: snapshotDraft.definitionHash,
    afterHash: '8b8b9fd0fbfe7c663c755ca297339d4da52d8fe1e637bd1968ea271202e8bc44',
    affectedComponents: ['component_revenue', 'component_conclusion'],
    bundle: {
      schemaVersion: '1.0', reportId: snapshotReportId, baseRevision: 24, source: 'AI', aiRunId: 'AI-20260810-042', scope: { pageId: snapshotPageId },
      operations: [
        { op: 'SECTION_REORDER', targetId: 'section_summary', payload: { order: 0 } },
        { op: 'DATA_BINDING_UPDATE', targetId: 'component_revenue', payload: { mode: 'SET', dataBinding: snapshotDraft.definition.components[0].dataBinding } },
        { op: 'INSIGHT_UPDATE', targetId: 'component_conclusion', payload: { richText: '毛利率同比下降 1.4pct，主要由促销折扣与原材料价格变化驱动。', evidenceId: 'evidence_margin_202607' } },
      ],
    },
  },
}

function dateTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(date).replaceAll('/', '-')
}

function componentIDs(section?: EditorPage['sections'][number]) {
  return section?.blocks.flatMap(block => block.zones.flatMap(zone => zone.slots.map(slot => slot.componentId).filter((id): id is string => Boolean(id)))) ?? []
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

function numericMetrics(preview?: PublishedPreview) {
  const metrics: Array<{ label: string; value: string }> = []
  for (const item of preview?.execution.components ?? []) {
    const result = item.result
    if (!result?.rows[0]) continue
    result.columns.forEach((column, index) => {
      const raw = result.rows[0]?.[index]
      if (metrics.length < 3 && Number.isFinite(Number(raw))) metrics.push({ label: column, value: String(raw) })
    })
    if (metrics.length >= 3) break
  }
  return metrics
}

function findChart(preview: PublishedPreview | undefined, components: EditorComponent[], pattern: RegExp): { result: RuntimeQueryResult; type: string; label: string } | undefined {
  if (!preview) return undefined
  const resultMap = new Map(preview.execution.components.map(item => [item.componentId, item]))
  for (const component of components) {
    if (!pattern.test(component.templateRef.type)) continue
    const item = resultMap.get(component.id)
    if (item?.result) return { result: item.result, type: component.templateRef.type, label: component.options.title || component.templateRef.type }
  }
  return undefined
}

function DraftCanvas({ draft, page, activeSectionId, preview, published, snapshot, onSelectSection, onManualEdit }: {
  draft: ReportDraft; page: EditorPage; activeSectionId: string; preview?: AIPreviewResponse; published?: PublishedPreview; snapshot: boolean; onSelectSection: (id: string) => void; onManualEdit: () => void
}) {
  const metrics = numericMetrics(published)
  const trend = findChart(published, draft.definition.components, /(line|bar|trend|area)/i)
  const channel = findChart(published, draft.definition.components, /(pie|donut)/i)
  const hasProposal = Boolean(preview)
  return <div className="report-editor-main">
    <nav className="report-editor-outline" aria-label="报告大纲"><header><strong>报告大纲</strong><button type="button" aria-label="收起大纲"><CaretDown size={14} /></button></header>{page.sections.slice().sort((a, b) => a.order - b.order).map(section => <button className={section.id === activeSectionId ? 'is-active' : ''} type="button" key={section.id} onClick={() => onSelectSection(section.id)}><span />{section.name}</button>)}</nav>
    <main className={`report-editor-canvas ${hasProposal ? 'has-ai-proposal' : ''}`}>
      <article className="report-editor-paper">
        <header><div><h2>{page.sections[0]?.name || page.name}</h2><ShieldCheck size={17} weight="fill" /></div><button type="button" onClick={onManualEdit}><NotePencil size={15} />手动精修</button></header>
        <p>{draft.definition.metadata.description || (snapshot ? '2026年7月企业经营整体稳中有进，收入端保持增长，渠道表现分化，盈利能力持续优化。' : '当前草稿按报告结构与受治理数据绑定生成设计预览。')}</p>
        <dl className="report-editor-kpis">{snapshot ? <><div><dt>营业收入</dt><dd>1,256,320<small>万元</small></dd><span>同比 <b>+8.7% ↑</b></span></div><div><dt>毛利率</dt><dd>23.6%</dd><span>同比 <b>+1.2pct ↑</b></span></div><div><dt>经营利润</dt><dd>152,680<small>万元</small></dd><span>同比 <b>+12.3% ↑</b></span></div></> : metrics.length ? metrics.map(metric => <div key={metric.label}><dt>{metric.label}</dt><dd>{metric.value}</dd><span><b>当前已发布数据</b></span></div>) : <><div><dt>组件配置</dt><dd>{draft.definition.components.length}</dd><span>草稿定义</span></div><div><dt>数据绑定</dt><dd>{draft.definition.components.filter(item => item.dataBinding).length}</dd><span>受治理绑定</span></div><div><dt>全局筛选</dt><dd>{draft.definition.globalFilters?.length ?? 0}</dd><span>设计预览</span></div></>}</dl>
        {hasProposal && <div className="report-editor-ai-insert">AI 方案将修改 {preview?.preview.affectedComponents.length ?? 0} 个组件，应用后写入新修订</div>}
        <section className={`report-editor-trend ${activeSectionId === page.sections[1]?.id ? 'is-selected' : ''}`} id={`editor-section-${page.sections[1]?.id}`}><header><strong>{page.sections[1]?.name || trend?.label || '趋势分析'}</strong><small>（设计预览）</small></header>{snapshot ? <RuntimeRevenueTrendChart /> : trend ? <RuntimeResultChart result={trend.result} templateType={trend.type} label={trend.label} /> : <div className="report-editor-data-placeholder"><CirclesFour size={22} /><strong>当前草稿组件已配置</strong><span>没有可复用的已发布运行结果；发布前预览将按当前权限执行。</span></div>}</section>
        <section className="report-editor-channel" id={`editor-section-${page.sections[2]?.id}`}>{snapshot ? <ChannelContributionChart /> : channel ? <RuntimeResultChart result={channel.result} templateType={channel.type} label={channel.label} /> : <div className="report-editor-data-placeholder is-compact"><CirclesFour size={20} /><strong>{page.sections[2]?.name || '渠道分析'}</strong><span>等待受治理运行结果</span></div>}</section>
        <section className="report-editor-conclusion" id={`editor-section-${page.sections[3]?.id}`}><CheckCircle size={17} weight="fill" /><div><strong>{page.sections[3]?.name || '经营结论'}</strong><p>{snapshot ? '收入实现稳健增长，线上渠道贡献提升，盈利能力持续优化；工程渠道短期承压，需关注项目节奏与回款。' : draft.definition.components.find(component => component.options.richText)?.options.richText || '当前草稿未配置经营结论。'}</p></div></section>
      </article>
      {hasProposal && <span className="report-editor-preview-label">AI 方案预览，不会自动保存</span>}
    </main>
  </div>
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
 * 新建面板提供两条并列入口：
 *  - 空白新建：只依赖已发布的数据集版本，未配置模型提供方时依然可用；
 *  - AI 生成：额外需要可用的模型提供方，失败时回退到空白新建。
 * 面板只展示服务端返回的真实候选与真实错误，不展示虚构的执行阶段或消歧问题。
 */
function NewReportTaskPanel({
  name, intent, contexts, contextId, contextsLoading, contextsError, creating, error,
  onName, onIntent, onContext, onCreateBlank, onGenerate,
}: {
  name: string
  intent: string
  contexts: DataContextCandidate[]
  contextId: string
  contextsLoading: boolean
  contextsError: string
  creating: '' | 'blank' | 'ai'
  error: string
  onName: (value: string) => void
  onIntent: (value: string) => void
  onContext: (value: string) => void
  onCreateBlank: () => void
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
  title: string
  subtitle: string
  binding?: { dimensions: Array<{ role: string; field: string }>; measures: Array<{ role: string; field: string }> }
}

/**
 * WEB-RPT-003 属性与数据绑定面板。
 * 绑定只能使用服务端返回的受治理字段与组件合同声明的角色；提交仍走
 * COMPONENT_UPDATE / DATA_BINDING_UPDATE 受控 Operation，不直接覆盖草稿 JSON。
 */
function ManualEditDialog({ component, manifest, fields, busy, error, onClose, onSave }: {
  component?: EditorComponent
  manifest?: ComponentManifest
  fields: string[]
  busy: boolean
  error: string
  onClose: () => void
  onSave: (result: ManualEditResult) => void
}) {
  const [title, setTitle] = useState(component?.options.title || '')
  const [subtitle, setSubtitle] = useState(component?.options.subtitle || '')
  const [dimensions, setDimensions] = useState<Array<{ role: string; field: string }>>(
    component?.dataBinding?.dimensions ?? [],
  )
  const [measures, setMeasures] = useState<Array<{ role: string; field: string }>>(
    component?.dataBinding?.measures ?? [],
  )

  const contract = manifest?.dataContract
  // SEMANTIC_IR 绑定固定语义发布版本，只能由问数或 AI 编排产生，面板不得改写。
  const semanticBound = component?.dataBinding?.bindingMode === 'SEMANTIC_IR'
  const bindable = Boolean(contract) && fields.length > 0 && !semanticBound
  const roles = contract?.roles ?? []
  const dimensionRoles = roles.filter(role => role !== 'VALUE' && role !== 'Y_AXIS')
  const measureRoles = roles.filter(role => role === 'VALUE' || role === 'Y_AXIS' || role === 'SIZE')
  const dimensionsValid = !contract || (dimensions.length >= contract.dimensions.min && dimensions.length <= contract.dimensions.max)
  const measuresValid = !contract || (measures.length >= contract.measures.min && measures.length <= contract.measures.max)
  const complete = dimensions.every(item => item.field && item.role) && measures.every(item => item.field && item.role)
  const bindingValid = !bindable || (dimensionsValid && measuresValid && complete)

  const rows = (
    label: string, items: Array<{ role: string; field: string }>, allowedRoles: string[], max: number,
    onChange: (next: Array<{ role: string; field: string }>) => void,
  ) => <div className="report-editor-binding-group">
    <div className="report-editor-binding-head">
      <strong>{label}</strong>
      <button type="button" disabled={items.length >= max || allowedRoles.length === 0}
        onClick={() => onChange([...items, { role: allowedRoles[0] ?? '', field: fields[0] ?? '' }])}>添加</button>
    </div>
    {items.length === 0 && <p className="report-editor-binding-empty">尚未选择{label}</p>}
    {items.map((item, index) => <div className="report-editor-binding-row" key={`${label}-${index}`}>
      <select aria-label={`${label}角色`} value={item.role}
        onChange={event => onChange(items.map((row, position) => position === index ? { ...row, role: event.target.value } : row))}>
        {allowedRoles.map(role => <option key={role} value={role}>{role}</option>)}
      </select>
      <select aria-label={`${label}字段`} value={item.field}
        onChange={event => onChange(items.map((row, position) => position === index ? { ...row, field: event.target.value } : row))}>
        {fields.map(field => <option key={field} value={field}>{field}</option>)}
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
        <label>组件标题<input value={title} onChange={event => setTitle(event.target.value)} /></label>
        <label>组件副标题<input value={subtitle} onChange={event => setSubtitle(event.target.value)} /></label>

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
          {rows('维度', dimensions, dimensionRoles, contract.dimensions.max, setDimensions)}
          {rows('度量', measures, measureRoles, contract.measures.max, setMeasures)}
          {!dimensionsValid && <p className="report-editor-inline-error"><WarningCircle size={15} />维度数量不满足组件合同</p>}
          {!measuresValid && <p className="report-editor-inline-error"><WarningCircle size={15} />度量数量不满足组件合同</p>}
        </div>}

        {error && <div className="report-editor-inline-error"><WarningCircle size={15} />{error}</div>}
      </div>
      <footer>
        <button className="quiet-button" type="button" disabled={busy} onClick={onClose}>取消</button>
        <button className="primary-button" type="button" disabled={busy || !title.trim() || !bindingValid}
          onClick={() => onSave({
            title: title.trim(), subtitle: subtitle.trim(),
            binding: bindable ? { dimensions, measures } : undefined,
          })}>{busy ? '正在保存…' : '保存为新修订'}</button>
      </footer>
    </section>
  </div>
}

export function ReportEditorPage() {
  const { reportId: routeReportId } = useParams()
  const location = useLocation()
  const navigate = useNavigate()
  const newMode = location.pathname === '/reports/new'
  const reportId = routeReportId ?? snapshotReportId
  const snapshot = import.meta.env.DEV && new URLSearchParams(location.search).get('snapshot') === 'runtime-draft'
  const [draft, setDraft] = useState<ReportDraft | null>(snapshot && !newMode ? snapshotDraft : null)
  const [asset, setAsset] = useState<ReportAsset | null>(null)
  const [published, setPublished] = useState<PublishedPreview | undefined>()
  // 加载状态由已结算的报告 ID 推导，避免在 effect 内同步 setState。
  const [settledReportId, setSettledReportId] = useState('')
  const [loadFailure, setLoadFailure] = useState<{ reportId: string; message: string } | null>(null)
  const loading = !snapshot && !newMode && settledReportId !== reportId
  const loadError = loadFailure?.reportId === reportId ? loadFailure.message : ''
  const [intent, setIntent] = useState('')
  const [newName, setNewName] = useState('')
  const [contexts, setContexts] = useState<DataContextCandidate[]>([])
  const [contextId, setContextId] = useState('')
  const [contextsLoading, setContextsLoading] = useState(newMode)
  const [contextsError, setContextsError] = useState('')
  const [newCreating, setNewCreating] = useState<'' | 'blank' | 'ai'>('')
  const [scopeMode, setScopeMode] = useState<'page' | 'section'>('page')
  const [activeSectionId, setActiveSectionId] = useState(snapshotSections[0].id)
  const [aiPreview, setAIPreview] = useState<AIPreviewResponse | undefined>(snapshot ? snapshotPreview : undefined)
  const [previewing, setPreviewing] = useState(false)
  const [applying, setApplying] = useState(false)
  const [operationSelection, setOperationSelection] = useState<Set<number>>(() => new Set(snapshotPreview.preview.bundle.operations.map((_, index) => index)))
  const [expandedOperations, setExpandedOperations] = useState<Set<number>>(() => new Set([0, 1, 2]))
  const [partialDecision, setPartialDecision] = useState<PartialDecision>('retain')
  const [actionError, setActionError] = useState('')
  const [toast, setToast] = useState('')
  const [manifests, setManifests] = useState<ComponentManifest[]>([])
  const [manualOpen, setManualOpen] = useState(false)
  const [manualBusy, setManualBusy] = useState(false)
  const [manualError, setManualError] = useState('')
  const [lastReceipt, setLastReceipt] = useState<{ from: number; to: number; count: number; source: string } | null>(null)

  const notify = (message: string) => { setToast(message); window.setTimeout(() => setToast(''), 2600) }

  useEffect(() => {
    if (snapshot || newMode) return undefined
    let cancelled = false
    void Promise.all([reportEditorAPI.getDraft(reportId), reportAssetsAPI.list({ limit: 100 })])
      .then(async ([nextDraft, assets]) => {
        if (cancelled) return
        setDraft(nextDraft)
        setAsset(assets.items.find(item => item.id === reportId) ?? null)
        const page = nextDraft.definition.pages.slice().sort((a, b) => a.order - b.order)[0]
        setActiveSectionId(page?.sections.slice().sort((a, b) => a.order - b.order)[0]?.id || '')
        if (!page) return
        try {
          const loaded = await reportRuntimeAPI.load(reportId)
          const execution = await reportRuntimeAPI.execute(reportId, { pageId: loaded.definition.pages[0]?.id || page.id })
          if (!cancelled) setPublished({ loaded, execution })
        } catch { /* Draft-only reports intentionally have no published runtime preview. */ }
      })
      .catch(cause => {
        if (!cancelled) {
          setLoadFailure({ reportId, message: cause instanceof Error ? cause.message : '报告草稿加载失败' })
        }
      })
      .finally(() => { if (!cancelled) setSettledReportId(reportId) })
    return () => { cancelled = true }
  }, [newMode, reportId, snapshot])

  // 编辑页读取组件模板合同，数据绑定面板据此限制可选角色与维度/度量数量。
  useEffect(() => {
    if (newMode) return undefined
    let cancelled = false
    void reportEditorAPI.listComponentManifests()
      .then(result => { if (!cancelled) setManifests(result.items) })
      .catch(() => { /* 合同不可用时绑定面板降级为只读，不阻塞编辑器加载。 */ })
    return () => { cancelled = true }
  }, [newMode])

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

  const page = useMemo(() => draft?.definition.pages.slice().sort((a, b) => a.order - b.order)[0], [draft])
  const activeSection = page?.sections.find(section => section.id === activeSectionId) || page?.sections[0]
  const selectedComponent = useMemo(() => {
    if (!draft) return undefined
    const ids = componentIDs(activeSection)
    return draft.definition.components.find(component => ids.includes(component.id)) || draft.definition.components[0]
  }, [activeSection, draft])
  // 绑定面板只能使用当前报告数据上下文对应的、服务端已按列权限裁剪的字段。
  const bindableFields = useMemo(() => {
    const contextID = selectedComponent?.dataBinding?.dataContextId ?? draft?.definition.dataContexts[0]?.id
    return contexts.find(item => item.dataContext.id === contextID)?.fields ?? []
  }, [contexts, draft, selectedComponent])
  const selectedManifest = useMemo(
    () => manifests.find(item =>
      item.type === selectedComponent?.templateRef.type && item.version === selectedComponent?.templateRef.version),
    [manifests, selectedComponent],
  )
  const canAIEdit = snapshot || Boolean(asset?.allowedActions.includes('AI_EDIT'))
  const canEdit = snapshot || Boolean(asset?.allowedActions.includes('EDIT'))
  const canPublish = snapshot || Boolean(asset?.allowedActions.includes('PUBLISH'))
  const selectedOperationCount = aiPreview?.preview.bundle.operations.filter((_, index) => operationSelection.has(index)).length ?? 0

  const reloadDraft = async () => {
    if (snapshot) return
    setActionError('')
    try { const next = await reportEditorAPI.getDraft(reportId); setDraft(next); setAIPreview(undefined); notify(`已打开服务端最新修订 r${next.revisionNo}`) }
    catch (cause) { setActionError(cause instanceof Error ? cause.message : '最新修订加载失败') }
  }

  const generatePreview = async () => {
    if (!draft || !page || !intent.trim() || !canAIEdit) return
    const dataContextId = draft.definition.dataContexts[0]?.id
    if (!dataContextId) { setActionError('当前报告没有可供 AI 使用的受治理数据上下文'); return }
    const scope: EditorScope = { pageId: page.id, ...(scopeMode === 'section' && activeSection ? { sectionId: activeSection.id } : {}) }
    setPreviewing(true); setActionError('')
    try {
      const result = snapshot ? await new Promise<AIPreviewResponse>(resolve => window.setTimeout(() => resolve(snapshotPreview), 420)) : await reportEditorAPI.previewAI(reportId, { intent: intent.trim(), dataContextId, scope })
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
    const operations = aiPreview.preview.bundle.operations.filter((operation, index) => operationSelection.has(index) && !(partialDecision === 'exclude' && operation.op.startsWith('INSIGHT_')))
    const bundle: EditorOperationBundle = { ...aiPreview.preview.bundle, operations }
    try {
      if (snapshot) {
        const next = { ...draft, revisionNo: draft.revisionNo + 1, updatedAt: new Date().toISOString() }
        setDraft(next)
        setLastReceipt({ from, to: next.revisionNo, count: operations.length, source: 'AI' })
      } else {
        const result = await reportEditorAPI.applyOperations(reportId, bundle)
        setDraft(result.draft)
        setLastReceipt({ from, to: result.draft.revisionNo, count: operations.length, source: 'AI' })
      }
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
      if (snapshot) {
        setDraft(current => current ? { ...current, revisionNo: Math.max(1, current.revisionNo + (redo ? 1 : -1)), updatedAt: new Date().toISOString() } : current)
      } else {
        const result = redo ? await reportEditorAPI.redo(reportId) : await reportEditorAPI.undo(reportId)
        setDraft(result.draft)
      }
      setAIPreview(undefined); notify(redo ? '已重做并生成新修订' : '已撤销并生成新修订')
    } catch (cause) { setActionError(cause instanceof Error ? cause.message : redo ? '当前没有可重做的修订' : '当前没有可撤销的修订') }
  }

  const saveManual = async (result: ManualEditResult) => {
    if (!draft || !selectedComponent) return
    setManualBusy(true); setManualError('')
    const { title, subtitle, binding } = result
    const operations: EditorOperation[] = [
      { op: 'COMPONENT_UPDATE', targetId: selectedComponent.id, payload: { options: { ...selectedComponent.options, title, subtitle } } },
    ]
    // 数据绑定走独立的 DATA_BINDING_UPDATE：清空绑定与设置绑定是不同的受控语义。
    if (binding) {
      const empty = binding.dimensions.length === 0 && binding.measures.length === 0
      const dataContextId = selectedComponent.dataBinding?.dataContextId ?? draft.definition.dataContexts[0]?.id
      operations.push(empty || !dataContextId
        ? { op: 'DATA_BINDING_UPDATE', targetId: selectedComponent.id, payload: { mode: 'CLEAR', dataBinding: null } }
        : {
          op: 'DATA_BINDING_UPDATE', targetId: selectedComponent.id,
          payload: {
            mode: 'SET',
            dataBinding: {
              bindingMode: 'DATASET_FIELD', dataContextId,
              dimensions: binding.dimensions, measures: binding.measures,
            },
          },
        })
    }
    try {
      const saved = await reportEditorAPI.applyOperations(reportId, {
        schemaVersion: '1.0', reportId, baseRevision: draft.revisionNo,
        source: 'USER', aiRunId: null, scope: null, operations,
      })
      setDraft(saved.draft)
      setManualOpen(false); notify(binding ? '属性与数据绑定已保存为新修订' : '组件属性已保存为新修订')
    } catch (cause) {
      if (cause instanceof RequestError && cause.detail.code === 'REPORT_REVISION_CONFLICT') {
        setManualError('草稿已被其他协作者更新，请先打开服务端最新修订。')
      } else {
        setManualError(cause instanceof Error ? cause.message : '保存失败')
      }
    } finally { setManualBusy(false) }
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

  if (newMode) return <AppShell className="report-editor-shell" lockBusinessDomain>
    <div className="report-editor-workspace report-editor-new-workspace">
      <header className="report-editor-header"><div><button type="button" onClick={() => navigate('/reports')}><ArrowLeft size={15} />返回报告工作台</button><div className="report-editor-title"><h1>新建报告</h1><span>草稿 r0</span><small>创建后进入编辑器</small></div></div></header>
      <div className="report-editor-body"><NewReportCanvas /><NewReportTaskPanel
        name={newName} intent={intent} contexts={contexts} contextId={contextId}
        contextsLoading={contextsLoading} contextsError={contextsError}
        creating={newCreating} error={actionError}
        onName={setNewName} onIntent={setIntent} onContext={setContextId}
        onCreateBlank={() => void createBlankReport()} onGenerate={() => void createNewReport()}
      /></div>
    </div>
  </AppShell>

  if (loading) return <AppShell className="report-editor-shell" lockBusinessDomain><div className="report-editor-loading"><SpinnerGap className="is-spinning" size={28} /><strong>正在读取报告草稿与权限</strong><p>随后加载可复用的已发布运行结果。</p></div></AppShell>
  if (!draft || !page) return <AppShell className="report-editor-shell" lockBusinessDomain><div className="report-editor-loading is-error"><WarningCircle size={28} /><strong>报告编辑器无法打开</strong><p>{loadError || '当前草稿没有可编辑页面。'}</p><button type="button" onClick={() => navigate('/reports')}>返回报告工作台</button></div></AppShell>

  return <AppShell className="report-editor-shell" lockBusinessDomain>
    <div className="report-editor-workspace">
      <header className="report-editor-header"><div><button type="button" onClick={() => navigate(snapshot ? '/reports?snapshot=assets' : '/reports')}><ArrowLeft size={15} />返回报告工作台</button><div className="report-editor-title"><h1>{draft.definition.metadata.name}</h1><span>草稿 r{draft.revisionNo}</span><small>已自动保存 {dateTime(draft.updatedAt).split(' ').at(-1)}</small></div></div><div className="report-editor-header-actions"><button type="button" aria-label="撤销" disabled={!canEdit || applying} onClick={() => void undoRedo(false)}><ArrowUDownLeft size={20} /></button><button type="button" aria-label="重做" disabled={!canEdit || applying} onClick={() => void undoRedo(true)}><ArrowUDownRight size={20} /></button><button className="quiet-button" type="button" disabled={!canEdit} onClick={() => setManualOpen(true)}><NotePencil size={16} />手动精修</button><button className="quiet-button" type="button" disabled={!canPublish || Boolean(aiPreview)} title={aiPreview ? '请先应用或退回当前 AI 方案' : ''} onClick={() => navigate(`/reports/${reportId}/publish-review${snapshot ? '?snapshot=publish-review' : ''}`)}><ShieldCheck size={16} />预览与发布</button><button type="button" aria-label="更多操作"><DotsThree size={19} /></button></div></header>

      <section className="report-editor-progress" aria-label="AI 改稿进度"><div className={aiPreview || previewing ? 'is-done' : ''}><CheckCircle size={18} weight="fill" />读取当前修订</div><i /><div className={aiPreview || previewing ? 'is-done' : ''}><CheckCircle size={18} weight="fill" />规划修改</div><i /><div className={aiPreview ? 'is-done' : previewing ? 'is-active' : ''}>{previewing ? <SpinnerGap className="is-spinning" size={18} /> : <CheckCircle size={18} weight="fill" />}验证数据与证据</div><i /><div className={aiPreview ? 'is-active' : ''}><span>4</span>等待确认</div><small>AI 方案预览，不会自动保存 <Info size={14} /></small></section>

      {actionError && <div className="report-editor-action-error" role="alert"><WarningCircle size={16} /><span>{actionError}</span>{actionError.includes('最新修订') && <button type="button" onClick={() => void reloadDraft()}>打开最新修订</button>}<button type="button" aria-label="关闭错误" onClick={() => setActionError('')}><X size={14} /></button></div>}

      <div className="report-editor-body"><DraftCanvas draft={draft} page={page} activeSectionId={activeSection?.id || ''} preview={aiPreview} published={published} snapshot={snapshot} onSelectSection={id => { setActiveSectionId(id); document.getElementById(`editor-section-${id}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }) }} onManualEdit={() => setManualOpen(true)} />
        <aside className="report-editor-task-panel"><header><div><h2>AI 改稿会话</h2><span>{aiPreview?.aiRunId || '等待开始'}</span></div><em className={aiPreview ? 'is-pending' : ''}>{previewing ? '思考中' : aiPreview ? '待确认' : '可输入'}</em></header>
          <section className={`report-editor-ai-message ${previewing ? 'is-thinking' : ''}`}><span><Sparkle size={15} weight="fill" /></span><p>{previewing ? '正在读取当前修订并校验可用数据与证据…' : aiPreview ? `我已生成 ${aiPreview.preview.bundle.operations.length} 项受控修改。你可以在执行计划中选择操作，确认后再应用到新修订。` : '告诉我想怎样修改这份报告。我会先生成可审查的执行计划，不会直接覆盖草稿。'}</p></section>
          <dl><div><dt>范围</dt><dd>{scopeMode === 'page' ? '当前页面' : activeSection?.name || '当前章节'}</dd></div><div><dt>基准修订</dt><dd>r{aiPreview?.preview.bundle.baseRevision ?? draft.revisionNo}</dd></div><div><dt>发起人</dt><dd>{snapshot ? '王敏 · 10:41' : `当前用户 · ${dateTime(new Date().toISOString()).split(' ').at(-1)}`}</dd></div><div><dt>影响对象</dt><dd>{aiPreview?.preview.affectedComponents.length ?? 0} 个组件</dd></div></dl>
          <section className="report-editor-plan"><h3>执行计划 <span>（共 {aiPreview?.preview.bundle.operations.length ?? 0} 项）</span></h3>{aiPreview ? aiPreview.preview.bundle.operations.map((operation, index) => { const detail = operationDetail(operation, draft); const expanded = expandedOperations.has(index); return <article className={operationSelection.has(index) ? 'is-selected' : ''} key={`${operation.op}:${operation.targetId}:${index}`}><button type="button" className="report-editor-plan-title" onClick={() => setOperationSelection(current => { const next = new Set(current); if (next.has(index)) next.delete(index); else next.add(index); return next })}><CheckCircle size={17} weight={operationSelection.has(index) ? 'fill' : 'regular'} /><strong>{operationTitle(operation, draft)}</strong><span>{expanded ? <CaretDown size={14} /> : <CaretRight size={14} />}</span></button>{expanded && <div><p><span>操作</span><code>{detail.op}</code></p><p><span>目标</span>{detail.target}</p><p><span>载荷字段</span>{detail.payload}</p><button type="button" onClick={() => setExpandedOperations(current => { const next = new Set(current); next.delete(index); return next })}>收起详情</button></div>}{!expanded && <button className="report-editor-expand" type="button" onClick={() => setExpandedOperations(current => new Set(current).add(index))}>展开详情</button>}</article> }) : <div className="report-editor-plan-empty"><Sparkle size={22} /><strong>等待 AI 改稿要求</strong><p>AI 只会在所选作用域内生成受控修改方案。</p></div>}
          </section>
          {aiPreview && aiPreview.preview.bundle.operations.some(operation => operation.op.startsWith('INSIGHT_')) && <section className="report-editor-decision"><h3>待人工确认项 <span>1 项</span></h3><p><WarningCircle size={15} />是否应用 AI 生成的智能结论？<small>结论文本需通过事实校验后才会随报告发布。</small></p><div><button className={partialDecision === 'retain' ? 'is-selected' : ''} type="button" onClick={() => setPartialDecision('retain')}>保留结论</button><button className={partialDecision === 'exclude' ? 'is-selected is-danger' : ''} type="button" onClick={() => setPartialDecision('exclude')}>排除结论</button></div></section>}
          <section className="report-editor-review-actions"><button className="primary-button" type="button" disabled={!aiPreview || applying || selectedOperationCount === 0} onClick={() => void applyPreview()}>{applying ? <><SpinnerGap className="is-spinning" size={16} />正在生成修订…</> : <>审查并应用 {selectedOperationCount} 项</>}</button><button className="quiet-button" type="button" disabled={!aiPreview || applying} onClick={() => { setAIPreview(undefined); setActionError(''); notify('AI 方案已退回，草稿未发生变化') }}>退回 AI 调整</button></section>
          <section className="report-editor-composer"><textarea aria-label="AI 改稿要求" value={intent} onChange={event => setIntent(event.target.value)} placeholder="描述你希望 AI 如何修改报告…" /><div><select aria-label="AI 修改作用域" value={scopeMode} onChange={event => setScopeMode(event.target.value as typeof scopeMode)}><option value="page">当前页面</option><option value="section">当前章节</option></select><button className="report-editor-attach" type="button" aria-label="附加证据"><Paperclip size={17} /></button><button className="primary-button" type="button" disabled={previewing || !intent.trim() || !canAIEdit} onClick={() => void generatePreview()}>{previewing ? <SpinnerGap className="is-spinning" size={16} /> : <MagicWand size={16} />}<span>{aiPreview ? '重新生成' : '发送'}</span></button></div></section>
        </aside>
      </div>

      {/* 收据只陈述本地可确证的事实：修订号、操作数与绑定统计。
          阻断问题与证据校验结论由发布评审的确定性门禁给出，这里不预判。 */}
      <footer className="report-editor-receipt"><span className="report-editor-receipt-label"><CaretDown size={15} />修改收据</span><div><strong>r{lastReceipt?.from ?? draft.revisionNo}</strong><span>→</span><strong>r{lastReceipt?.to ?? draft.revisionNo + (aiPreview ? 1 : 0)}</strong><small>目标修订</small></div><div><strong>{lastReceipt?.count ?? selectedOperationCount}</strong><small>{lastReceipt ? '已执行操作' : '拟执行操作'}</small></div><div><strong>{draft.definition.components.length}</strong><small>组件</small></div><div><strong>{draft.definition.components.filter(component => component.dataBinding).length}</strong><small>已绑定数据</small></div><span>发布前检查将在「预览与发布」中给出阻断与告警项</span></footer>
    </div>
    {manualOpen && <ManualEditDialog
      component={selectedComponent} manifest={selectedManifest} fields={bindableFields}
      busy={manualBusy} error={manualError}
      onClose={() => setManualOpen(false)} onSave={result => void saveManual(result)}
    />}
    {toast && <div className="report-toast" role="status"><Check size={16} weight="bold" />{toast}</div>}
  </AppShell>
}
