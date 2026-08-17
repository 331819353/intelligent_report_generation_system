import {
  Archive, BracketsCurly, CaretLeft, CaretRight, Check, ClockCounterClockwise, Copy, DownloadSimple, FileText,
  FloppyDisk, Hand, MagnifyingGlass, Minus, PaperPlaneTilt, PencilSimple, Plus, PlusCircle, SquaresFour, Table, Tag, Trash,
  TreeStructure, UserCircle, X,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { AppButton } from '../components/AppButton'
import { AppShell } from '../components/AppShell'
import { analysisTemplateFixtures } from '../report/templates/fixtures'
import {
  appendAnalysisNode, conclusionStyleLabels, contentCategoryLabels, countAnalysisNodes, createAnalysisNode,
  createTemplateSkeleton, defaultConclusionFormat, defaultItemConfig,
  displayFormLabels, explanationTemplateDefinitions, findAnalysisNode, metricRoleLabels, normalizeAnalysisTemplate,
  removeAnalysisNode, templateTypeLabels, updateAnalysisNode,
  type AnalysisExplanationSection, type AnalysisExplanationTemplateType, type AnalysisNode, type AnalysisTemplate, type ConclusionFormat,
  type TemplateStatus, type TemplateType,
} from '../report/templates/model'
import '../styles/template-center.css'

const templateStorageKey = 'intelligent-report.analysis-templates.v9'
const editorRecoveryStorageKey = 'intelligent-report.analysis-template-editor-recovery.v2'
const obsoleteTemplateStorageKeys = [
  'intelligent-report.analysis-templates.v8',
  'intelligent-report.analysis-template-editor-recovery.v1',
]

type TemplateEditorRecovery = {
  template: AnalysisTemplate
  savedAt: string
}

function cloneTemplates(items: AnalysisTemplate[]) {
  return JSON.parse(JSON.stringify(items)) as AnalysisTemplate[]
}

function mergeBundledTemplateUpdates(items: AnalysisTemplate[]) {
  const bundledByID = new Map(analysisTemplateFixtures.map(template => [template.id, template]))
  const merged = items.map(template => {
    const bundled = bundledByID.get(template.id)
    return bundled && bundled.version > template.version
      ? normalizeAnalysisTemplate(cloneTemplates([bundled])[0])
      : template
  })
  const existingIDs = new Set(merged.map(template => template.id))
  return [...merged, ...cloneTemplates(analysisTemplateFixtures.filter(template => !existingIDs.has(template.id))).map(normalizeAnalysisTemplate)]
}

function loadTemplates() {
  try {
    // v9 resets the newly designed template center to the single bundled
    // operating-analysis template. Older cards and recovery drafts must not
    // leak back into the new catalog.
    obsoleteTemplateStorageKeys.forEach(key => window.localStorage.removeItem(key))
    const stored = window.localStorage.getItem(templateStorageKey)
    if (stored) {
      const parsed = JSON.parse(stored) as AnalysisTemplate[]
      if (Array.isArray(parsed) && parsed.length > 0) return mergeBundledTemplateUpdates(parsed.map(normalizeAnalysisTemplate))
    }
  } catch {
    // Local persistence is an enhancement; the built-in templates remain usable if storage is blocked.
  }
  return cloneTemplates(analysisTemplateFixtures).map(normalizeAnalysisTemplate)
}

function makeID(prefix: string) {
  return `${prefix}-${typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : Date.now().toString(36)}`
}

function generateTemplateCode(type: TemplateType) {
  const stamp = Date.now().toString(36).slice(-6).toLocaleUpperCase()
  return `TPL-${type === 'REPORT' ? 'RPT' : 'TBL'}-${stamp}`
}

function templateStatusLabel(status: TemplateStatus) {
  if (status === 'ACTIVE') return '已发布'
  if (status === 'OFFLINE') return '已下架'
  return '草稿'
}

function saveEditorRecovery(template: AnalysisTemplate) {
  try {
    const recovery: TemplateEditorRecovery = {
      template: { ...template, status: 'DRAFT' },
      savedAt: new Date().toISOString(),
    }
    window.localStorage.setItem(editorRecoveryStorageKey, JSON.stringify(recovery))
  } catch {
    // Recovery is best-effort; the explicit draft action remains available.
  }
}

function loadEditorRecovery(): TemplateEditorRecovery | undefined {
  try {
    const stored = window.localStorage.getItem(editorRecoveryStorageKey)
    if (!stored) return undefined
    const recovery = JSON.parse(stored) as TemplateEditorRecovery
    if (!recovery?.template?.id || !recovery.savedAt) return undefined
    return { ...recovery, template: normalizeAnalysisTemplate(recovery.template) }
  } catch {
    return undefined
  }
}

function clearEditorRecovery(templateID?: string) {
  try {
    if (templateID) {
      const recovery = loadEditorRecovery()
      if (recovery && recovery.template.id !== templateID) return
    }
    window.localStorage.removeItem(editorRecoveryStorageKey)
  } catch {
    // Ignore blocked storage and continue the requested operation.
  }
}

function cloneNodeForDuplicate(node: AnalysisNode): AnalysisNode {
  return {
    ...node,
    id: makeID('analysis-node'),
    explanationSections: node.explanationSections.map(section => ({
      ...section,
      id: makeID('explanation-section'),
      fields: { ...section.fields },
      conclusionFormat: section.conclusionFormat ? { ...section.conclusionFormat, requiredFields: [...section.conclusionFormat.requiredFields] } : undefined,
      itemConfig: section.itemConfig ? {
        ...section.itemConfig,
        metricDisplays: section.itemConfig.metricDisplays.map(metric => ({ ...metric, id: makeID('metric') })),
        comparisons: [...section.itemConfig.comparisons],
        dimensions: [...section.itemConfig.dimensions],
        filters: [...section.itemConfig.filters],
        breakdownRules: [...section.itemConfig.breakdownRules],
      } : undefined,
      items: section.items.map(item => ({ ...item, id: makeID('explanation-item') })),
    })),
    children: node.children.map(cloneNodeForDuplicate),
  }
}

function flattenAnalysisNodes(nodes: AnalysisNode[]): AnalysisNode[] {
  return nodes.flatMap(node => [node, ...flattenAnalysisNodes(node.children)])
}

function findAnalysisNodeParent(nodes: AnalysisNode[], id: string, parent?: AnalysisNode): AnalysisNode | undefined {
  for (const node of nodes) {
    if (node.id === id) return parent
    const match = findAnalysisNodeParent(node.children, id, node)
    if (match) return match
  }
  return undefined
}

function analysisNodeSearchText(node: AnalysisNode) {
  return JSON.stringify({
    id: node.id,
    title: node.title,
    description: node.description,
    explanationSections: node.explanationSections,
  }).toLocaleLowerCase()
}

function formatUpdatedAt(value: string) {
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(new Date(value)).replace('/', '-')
}

function TemplateTypeIcon({ type, size = 18 }: { type: TemplateType; size?: number }) {
  return type === 'REPORT' ? <FileText size={size} /> : <Table size={size} />
}

function TemplateCard({ template, onOpen, onDuplicate, onDownload, onToggleStatus, onDelete }: {
  template: AnalysisTemplate
  onOpen: () => void
  onDuplicate: () => void
  onDownload: () => void
  onToggleStatus: () => void
  onDelete: () => void
}) {
  const nodeCount = countAnalysisNodes(template.analysisTree)
  return <article className="template-card">
    <button className="template-card-main" type="button" onClick={onOpen} aria-label={`打开${template.name}分析思路`}>
      <span className={`template-card-icon is-${template.templateType.toLocaleLowerCase()}`}><TemplateTypeIcon type={template.templateType} size={22} /></span>
      <span className="template-card-copy">
        <span className="template-card-title"><strong>{template.name}</strong><i className={`is-${template.status.toLocaleLowerCase()}`}>{templateStatusLabel(template.status)}</i></span>
        <small>{template.code} · v{template.version}</small>
        <p>{template.description}</p>
      </span>
    </button>
    <div className="template-card-tags">{template.tags.slice(0, 3).map(tag => <span key={tag}><Tag size={12} />{tag}</span>)}</div>
    <dl>
      <div><dt>分析节点</dt><dd>{nodeCount}</dd></div>
      <div><dt>已生成</dt><dd>{template.usageCount} 次</dd></div>
      <div><dt>最近更新</dt><dd>{formatUpdatedAt(template.updatedAt)}</dd></div>
    </dl>
    <footer><span><UserCircle size={15} />{template.owner}</span><div className="template-card-actions">
      <button type="button" onClick={onOpen} aria-label={`编辑${template.name}`} title="编辑"><PencilSimple size={13} /><span>编辑</span></button>
      <button type="button" onClick={onDuplicate} aria-label={`复制${template.name}`} title="复制模板"><Copy size={13} /></button>
      <button type="button" onClick={onDownload} aria-label={`导出${template.name}`} title="导出 JSON"><DownloadSimple size={13} /></button>
      <button className={`is-status-action ${template.status === 'ACTIVE' ? 'is-unpublish' : 'is-publish'}`} type="button" onClick={onToggleStatus} aria-label={`${template.status === 'ACTIVE' ? '下架' : '发布'}${template.name}`} title={template.status === 'ACTIVE' ? '下架模板' : '发布模板'}>{template.status === 'ACTIVE' ? <Archive size={13} /> : <PaperPlaneTilt size={13} />}</button>
      <button className="is-danger" type="button" onClick={onDelete} aria-label={`删除${template.name}`} title="删除模板"><Trash size={13} /></button>
    </div></footer>
  </article>
}

type NewTemplateDraft = { templateType: TemplateType }

function NewTemplateDialog({ onClose, onCreate }: { onClose: () => void; onCreate: (draft: NewTemplateDraft) => void }) {
  const [templateType, setTemplateType] = useState<TemplateType>('REPORT')
  return <div className="template-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="new-template-dialog" role="dialog" aria-modal="true" aria-labelledby="new-template-title" onMouseDown={event => event.stopPropagation()}>
      <header><div><span>新建模板</span><h2 id="new-template-title">选择模板类型</h2><p>选择后直接进入分析思路画布，模板信息在保存时填写。</p></div><AppButton text circle type="button" aria-label="关闭" onClick={onClose}><X size={19} /></AppButton></header>
      <div className="new-template-form is-type-only">
        <fieldset><legend>模板类型</legend><div className="new-template-types">
          {(['REPORT', 'TABLE'] as const).map(type => <label className={templateType === type ? 'is-selected' : ''} key={type}>
            <input type="radio" name="templateType" value={type} checked={templateType === type} onChange={() => setTemplateType(type)} />
            <span><TemplateTypeIcon type={type} size={20} /></span><strong>{templateTypeLabels[type]}模板</strong><small>{type === 'REPORT' ? '适合章节化、叙事型分析输出' : '适合指标监控、表格型固定输出'}</small>
          </label>)}
        </div></fieldset>
      </div>
      <footer><AppButton plain size="small" type="button" onClick={onClose}>取消</AppButton><AppButton variant="primary" size="small" type="button" onClick={() => onCreate({ templateType })}><TreeStructure size={15} weight="bold" />进入画布</AppButton></footer>
    </section>
  </div>
}

function TemplateRecoveryDialog({ recovery, onRestore, onStartNew, onCancel }: {
  recovery: TemplateEditorRecovery
  onRestore: () => void
  onStartNew: () => void
  onCancel: () => void
}) {
  return <div className="template-decision-backdrop" role="presentation">
    <section className="template-decision-dialog" role="alertdialog" aria-modal="true" aria-labelledby="template-recovery-title">
      <header><span className="template-decision-icon"><ClockCounterClockwise size={21} /></span><div><small>发现未完成编辑</small><h2 id="template-recovery-title">继续上一次模板设计吗？</h2></div></header>
      <div className="template-decision-content"><strong>{recovery.template.name}</strong><p>恢复后将回到上次离开时的画布；也可以保留模板卡片并开始一个全新模板。</p><span>最近编辑：{formatUpdatedAt(recovery.savedAt)}</span></div>
      <footer><AppButton plain size="small" type="button" onClick={onCancel}>取消</AppButton><AppButton plain size="small" type="button" onClick={onStartNew}>不恢复，新建模板</AppButton><AppButton variant="primary" size="small" type="button" onClick={onRestore}><ClockCounterClockwise size={15} />恢复上次编辑</AppButton></footer>
    </section>
  </div>
}

function EditorCloseDialog({ templateName, onSaveDraft, onDiscard, onContinue }: {
  templateName: string
  onSaveDraft: () => void
  onDiscard: () => void
  onContinue: () => void
}) {
  return <div className="template-decision-backdrop" role="presentation">
    <section className="template-decision-dialog" role="alertdialog" aria-modal="true" aria-labelledby="template-close-title">
      <header><span className="template-decision-icon is-draft"><FloppyDisk size={21} /></span><div><small>关闭编辑窗口</small><h2 id="template-close-title">要保存为草稿吗？</h2></div></header>
      <div className="template-decision-content"><strong>{templateName}</strong><p>保存草稿后可在模板列表继续编辑；下次新建模板时，也会询问是否恢复本次内容。</p></div>
      <footer><AppButton plain size="small" type="button" onClick={onContinue}>继续编辑</AppButton><AppButton plain size="small" type="button" onClick={onDiscard}>放弃修改</AppButton><AppButton variant="primary" size="small" type="button" onClick={onSaveDraft}><FloppyDisk size={15} />保存草稿并关闭</AppButton></footer>
    </section>
  </div>
}

type TemplateSaveInput = {
  name: string
  description: string
  tags: string[]
}

function TemplateSaveDialog({ template, closeAfterSave, onCancel, onSaveDraft, onPublish }: {
  template: AnalysisTemplate
  closeAfterSave: boolean
  onCancel: () => void
  onSaveDraft: (input: TemplateSaveInput) => void
  onPublish: (input: TemplateSaveInput) => void
}) {
  const [name, setName] = useState(template.name.startsWith('未命名') ? '' : template.name)
  const [description, setDescription] = useState(template.description)
  const [tags, setTags] = useState(template.tags.join('，'))
  const ready = name.trim().length > 0
  const input = (): TemplateSaveInput => ({
    name: name.trim(),
    description: description.trim(),
    tags: tags.split(/[,，]/).map(item => item.trim()).filter(Boolean),
  })

  return <div className="template-decision-backdrop" role="presentation">
    <section className="template-decision-dialog template-save-dialog" role="dialog" aria-modal="true" aria-labelledby="template-save-title">
      <header><span className="template-decision-icon"><FloppyDisk size={21} /></span><div><small>{closeAfterSave ? '保存草稿并关闭' : '保存模板'}</small><h2 id="template-save-title">补充模板信息</h2></div></header>
      <div className="template-save-form">
        <div className="template-save-system-fields">
          <span><small>模板类型</small><strong>{templateTypeLabels[template.templateType]}模板</strong></span>
          <span><small>模板编码</small><strong>{template.code}</strong><em>系统自动生成</em></span>
        </div>
        <label><span>模板名称 <i>必填</i></span><input autoFocus value={name} onChange={event => setName(event.target.value)} placeholder="例如：月度经营分析模板" maxLength={60} /></label>
        <label><span>模板说明</span><textarea value={description} onChange={event => setDescription(event.target.value)} placeholder="说明模板适用场景与预期产出" maxLength={200} /></label>
        <label><span>模板标签</span><input value={tags} onChange={event => setTags(event.target.value)} placeholder="多个标签用逗号分隔" maxLength={100} /></label>
      </div>
      <footer><AppButton plain size="small" type="button" onClick={onCancel}>取消</AppButton>{!closeAfterSave && <AppButton plain size="small" type="button" disabled={!ready} onClick={() => onSaveDraft(input())}><FloppyDisk size={15} />保存草稿</AppButton>}<AppButton variant="primary" size="small" type="button" disabled={!ready} onClick={() => closeAfterSave ? onSaveDraft(input()) : onPublish(input())}>{closeAfterSave ? <FloppyDisk size={15} /> : <PaperPlaneTilt size={15} />}{closeAfterSave ? '保存草稿并关闭' : '保存并发布'}</AppButton></footer>
    </section>
  </div>
}

type MindMapNodeProps = {
  node: AnalysisNode
  parentID: string | null
  branchColor: string
  root: boolean
  selectedID: string
  searchMatchIDs: ReadonlySet<string>
  activeSearchID: string
  inlineEditingKey: string
  onSelect: (id: string) => void
  onBeginEdit: (id: string, section: InlineEditorSection) => void
  onEndEdit: () => void
  onUpdate: (id: string, update: (node: AnalysisNode) => AnalysisNode) => void
  onAddChild: (parentID: string) => void
  onDelete: (id: string) => void
}

type InlineEditorSection = 'TITLE' | `CUSTOM:${string}`

const mindMapBranchColors = ['#ff6b6b', '#ff9559', '#5fc89a', '#5ecfc8', '#7a9ff1', '#b985df']
const explanationTemplateOrder: AnalysisExplanationTemplateType[] = ['CONCLUSION_OUTPUT', 'METRIC_DISPLAY', 'ANALYSIS_OBJECTIVE', 'DATA_SCOPE', 'ANALYSIS_RULE', 'DISPLAY_STANDARD', 'BUSINESS_CONTEXT', 'CUSTOM']

type MindMapConnection = { id: string; path: string; color: string }

function MindMapConnections({ revision, scale }: { revision: string; scale: number }) {
  const [connections, setConnections] = useState<MindMapConnection[]>([])
  const [size, setSize] = useState({ width: 1, height: 1 })
  const svgRef = useRef<SVGSVGElement>(null)

  useLayoutEffect(() => {
    const content = svgRef.current?.parentElement
    if (!content) return undefined
    let frame = 0
    const draw = () => {
      const contentRect = content.getBoundingClientRect()
      const nodeElements = [...content.querySelectorAll<HTMLElement>('[data-node-id]')]
      const byID = new Map(nodeElements.map(element => [element.dataset.nodeId ?? '', element]))
      const next = nodeElements.flatMap<MindMapConnection>(element => {
        const parentID = element.dataset.parentId
        if (!parentID) return []
        const parent = byID.get(parentID)
        const childTitle = element.querySelector<HTMLElement>('[data-node-title]')
        if (!parent || !childTitle) return []
        const parentRect = parent.getBoundingClientRect()
        const childRect = element.getBoundingClientRect()
        const childTitleRect = childTitle.getBoundingClientRect()
        const sourceX = (parentRect.right - contentRect.left) / scale
        const sourceY = (parentRect.top - contentRect.top) / scale + 45
        const targetX = (childRect.left - contentRect.left) / scale
        const targetRight = (childTitleRect.right - contentRect.left) / scale
        const targetY = (childTitleRect.bottom - contentRect.top) / scale + 5
        const gap = Math.max(12, targetX - sourceX)
        const laneX = sourceX + gap * .5
        const deltaY = targetY - sourceY
        const direction = deltaY >= 0 ? 1 : -1
        const radius = Math.max(0, Math.min(10, Math.abs(deltaY) / 2, gap / 2))
        const path = Math.abs(deltaY) < 1
          ? `M ${sourceX} ${sourceY} L ${targetX} ${targetY} L ${targetRight} ${targetY}`
          : `M ${sourceX} ${sourceY} L ${laneX - radius} ${sourceY} Q ${laneX} ${sourceY} ${laneX} ${sourceY + direction * radius} L ${laneX} ${targetY - direction * radius} Q ${laneX} ${targetY} ${laneX + radius} ${targetY} L ${targetX} ${targetY} L ${targetRight} ${targetY}`
        return [{
          id: `${parentID}:${element.dataset.nodeId}`,
          path,
          color: element.dataset.branchColor || mindMapBranchColors[0],
        }]
      })
      setSize({ width: Math.max(content.scrollWidth, content.clientWidth), height: Math.max(content.scrollHeight, content.clientHeight) })
      setConnections(next)
    }
    const schedule = () => { window.cancelAnimationFrame(frame); frame = window.requestAnimationFrame(draw) }
    schedule()
    const observer = new ResizeObserver(schedule)
    observer.observe(content)
    content.querySelectorAll('[data-node-id]').forEach(element => observer.observe(element))
    window.addEventListener('resize', schedule)
    return () => { window.cancelAnimationFrame(frame); observer.disconnect(); window.removeEventListener('resize', schedule) }
  }, [revision, scale])

  return <svg ref={svgRef} className="mind-map-connections" width={size.width} height={size.height} viewBox={`0 0 ${size.width} ${size.height}`} aria-hidden="true">
    {connections.map(connection => <path key={connection.id} d={connection.path} stroke={connection.color} />)}
  </svg>
}

function InlineEditButton({ label, onClick, done = false }: { label: string; onClick: () => void; done?: boolean }) {
  return <button className={`mind-inline-edit ${done ? 'is-done' : ''}`} type="button" aria-label={label} onClick={event => { event.stopPropagation(); onClick() }}>
    {done ? <Check size={12} weight="bold" /> : <PencilSimple size={11} />}
  </button>
}

function EditableDesignValue({ label, value, onEdit }: { label: string; value: string; onEdit: () => void }) {
  return <div><dt>{label}</dt><dd>{value
    ? <span>{value}<InlineEditButton label={`修改${label}`} onClick={onEdit} /></span>
    : <button className="mind-inline-add" type="button" onClick={event => { event.stopPropagation(); onEdit() }}><Plus size={10} weight="bold" />添加{label}</button>}
  </dd></div>
}

function ContentConclusionBody({ value, editing, onEdit, onUpdate }: {
  value: ConclusionFormat
  editing: boolean
  onEdit: () => void
  onUpdate: (update: Partial<ConclusionFormat>) => void
}) {
  return editing ? <div className="mind-section-editor mind-content-card-editor">
    <label><span>结论样例</span><textarea autoFocus value={value.referenceExample} onChange={event => onUpdate({ referenceExample: event.target.value })} placeholder="输入可供生成时参考的完整结论样例" /></label>
    <div><label><span>结论格式</span><select value={value.style} onChange={event => onUpdate({ style: event.target.value as ConclusionFormat['style'] })}>{Object.entries(conclusionStyleLabels).map(([key, label]) => <option value={key} key={key}>{label}</option>)}</select></label>
      <label><span>结论字数</span><input type="number" min={50} max={3000} step={50} value={value.maxLength} onChange={event => onUpdate({ maxLength: Math.max(50, Number(event.target.value) || 50) })} /></label></div>
    <label><span>必含内容</span><input value={value.requiredFields.join('，')} onChange={event => onUpdate({ requiredFields: event.target.value.split(/[,，]/).map(item => item.trim()).filter(Boolean) })} placeholder="核心发现，原因判断，行动建议" /></label>
    <label><span>写作要求</span><textarea value={value.instruction} onChange={event => onUpdate({ instruction: event.target.value })} placeholder="描述结论的语气、结构和约束" /></label>
  </div> : <dl>
    <EditableDesignValue label="结论样例" value={value.referenceExample} onEdit={onEdit} />
    <EditableDesignValue label="结论格式" value={conclusionStyleLabels[value.style]} onEdit={onEdit} />
    <EditableDesignValue label="结论字数" value={`不超过 ${value.maxLength} 字`} onEdit={onEdit} />
    <EditableDesignValue label="必含内容" value={value.requiredFields.join('、')} onEdit={onEdit} />
    <EditableDesignValue label="写作要求" value={value.instruction} onEdit={onEdit} />
  </dl>
}

function ContentMetricBody({ value, editing, onEdit, onUpdate }: {
  value: ReturnType<typeof defaultItemConfig>
  editing: boolean
  onEdit: () => void
  onUpdate: (update: (config: ReturnType<typeof defaultItemConfig>) => ReturnType<typeof defaultItemConfig>) => void
}) {
  return editing ? <div className="mind-section-editor mind-content-card-editor">
    <div className="mind-metric-editor-list">
      {value.metricDisplays.map((metric, index) => <section className="mind-metric-editor" key={metric.id}>
        <header><strong>指标 {index + 1}</strong><button type="button" aria-label={`删除指标${metric.metric || index + 1}`} onClick={() => onUpdate(current => ({ ...current, metricDisplays: current.metricDisplays.filter(item => item.id !== metric.id) }))}><Trash size={11} /></button></header>
        <label><span>指标名称</span><input autoFocus={index === 0} value={metric.metric} onChange={event => onUpdate(current => ({ ...current, metricDisplays: current.metricDisplays.map(item => item.id === metric.id ? { ...item, metric: event.target.value } : item) }))} placeholder="例如：营业收入" /></label>
        <label><span>指标角色</span><select value={metric.role} onChange={event => onUpdate(current => ({ ...current, metricDisplays: current.metricDisplays.map(item => item.id === metric.id ? { ...item, role: event.target.value as typeof metric.role } : item) }))}>{Object.entries(metricRoleLabels).map(([key, label]) => <option value={key} key={key}>{label}</option>)}</select></label>
        <label><span>展示形式</span><select value={metric.displayForm} onChange={event => onUpdate(current => ({ ...current, metricDisplays: current.metricDisplays.map(item => item.id === metric.id ? { ...item, displayForm: event.target.value as typeof metric.displayForm } : item) }))}>{Object.entries(displayFormLabels).map(([key, label]) => <option value={key} key={key}>{label}</option>)}</select></label>
        <label><span>展示要求</span><textarea value={metric.displayRequirements} onChange={event => onUpdate(current => ({ ...current, metricDisplays: current.metricDisplays.map(item => item.id === metric.id ? { ...item, displayRequirements: event.target.value } : item) }))} placeholder="说明该指标的时间范围、标注或阈值" /></label>
      </section>)}
      <button className="mind-inline-add is-block" type="button" onClick={() => onUpdate(current => ({ ...current, metricDisplays: [...current.metricDisplays, { id: makeID('metric'), metric: '', role: 'CORE', displayForm: 'LINE_CHART', displayRequirements: '' }] }))}><Plus size={10} weight="bold" />添加指标</button>
    </div>
    <div className="mind-analysis-condition-title"><strong>分析条件</strong><small>指标卡自带对比、维度、筛选和拆解规则</small></div>
    <label><span>对比口径</span><input value={value.comparisons.join('，')} onChange={event => onUpdate(current => ({ ...current, comparisons: event.target.value.split(/[,，]/).map(item => item.trim()).filter(Boolean) }))} placeholder="实际 vs 目标，同比，环比" /></label>
    <label><span>分析维度</span><input value={value.dimensions.join('，')} onChange={event => onUpdate(current => ({ ...current, dimensions: event.target.value.split(/[,，]/).map(item => item.trim()).filter(Boolean) }))} placeholder="区域，渠道，品牌" /></label>
    <label><span>筛选条件</span><input value={value.filters.join('，')} onChange={event => onUpdate(current => ({ ...current, filters: event.target.value.split(/[,，]/).map(item => item.trim()).filter(Boolean) }))} placeholder="国内，在营渠道，已确认收入" /></label>
    <label><span>拆解 / 排序</span><input value={value.breakdownRules.join('，')} onChange={event => onUpdate(current => ({ ...current, breakdownRules: event.target.value.split(/[,，]/).map(item => item.trim()).filter(Boolean) }))} placeholder="按贡献倒挤，列出 Top5，按区域拆解" /></label>
    <label><span>组合展示要求</span><textarea value={value.displayRequirements} onChange={event => onUpdate(current => ({ ...current, displayRequirements: event.target.value }))} placeholder="说明多个指标组合时的布局与联动要求" /></label>
  </div> : <>
    <div className="mind-metric-display-list">
      {value.metricDisplays.length > 0 ? value.metricDisplays.map(metric => <article key={metric.id}>
        <header><strong>{metric.metric || '未命名指标'}</strong><span className="is-role">{metricRoleLabels[metric.role]}</span><span>{displayFormLabels[metric.displayForm]}</span><InlineEditButton label={`修改指标${metric.metric}`} onClick={onEdit} /></header>
        <p>{metric.displayRequirements || '暂未配置该指标的展示要求'}</p>
      </article>) : <button className="mind-inline-add is-block" type="button" onClick={event => { event.stopPropagation(); onEdit() }}><Plus size={10} weight="bold" />添加第一个指标</button>}
    </div>
    <dl>
      <EditableDesignValue label="对比口径" value={value.comparisons.join('、')} onEdit={onEdit} />
      <EditableDesignValue label="分析维度" value={value.dimensions.join('、')} onEdit={onEdit} />
      <EditableDesignValue label="筛选条件" value={value.filters.join('、')} onEdit={onEdit} />
      <EditableDesignValue label="拆解规则" value={value.breakdownRules.join('、')} onEdit={onEdit} />
      <EditableDesignValue label="组合要求" value={value.displayRequirements} onEdit={onEdit} />
    </dl>
  </>
}

function ExplanationDesignSection({ section, editing, onEdit, onDone, onUpdate, onDelete }: {
  section: AnalysisExplanationSection
  editing: boolean
  onEdit: () => void
  onDone: () => void
  onUpdate: (update: (section: AnalysisExplanationSection) => AnalysisExplanationSection) => void
  onDelete: () => void
}) {
  const definition = explanationTemplateDefinitions[section.templateType]
  const typeClass = section.templateType.toLocaleLowerCase().replaceAll('_', '-')
  const conclusionValue = section.conclusionFormat ?? defaultConclusionFormat()
  const metricValue = section.itemConfig ?? defaultItemConfig()
  const custom = section.templateType === 'CUSTOM'
  return <section className={`mind-node-design-section mind-content-card is-${typeClass}`}>
    <header><span className={`mind-explanation-type is-${typeClass}`}>{definition.title}</span>{editing
      ? <input className="mind-custom-title-input" aria-label="内容卡片标题" value={section.title} onChange={event => onUpdate(current => ({ ...current, title: event.target.value }))} placeholder="内容卡片标题" />
      : <strong>{section.title || definition.title}</strong>}
      <InlineEditButton label={editing ? '完成内容卡片编辑' : `修改内容卡片${section.title}`} done={editing} onClick={editing ? onDone : onEdit} />
      <button className="mind-custom-delete" type="button" aria-label={`删除内容卡片${section.title}`} onClick={event => { event.stopPropagation(); onDelete() }}><Trash size={11} /></button>
    </header>
    {section.templateType === 'CONCLUSION_OUTPUT' && <ContentConclusionBody value={conclusionValue} editing={editing} onEdit={onEdit} onUpdate={update => onUpdate(current => ({ ...current, conclusionFormat: { ...(current.conclusionFormat ?? defaultConclusionFormat()), ...update } }))} />}
    {section.templateType === 'METRIC_DISPLAY' && <ContentMetricBody value={metricValue} editing={editing} onEdit={onEdit} onUpdate={update => onUpdate(current => ({ ...current, itemConfig: update(current.itemConfig ?? defaultItemConfig()) }))} />}
    {!custom && definition.fields.length > 0 && (editing ? <div className="mind-section-editor mind-content-card-editor">
      {definition.fields.map((field, index) => <label key={field.key}><span>{field.label}</span>{field.multiline
        ? <textarea autoFocus={index === 0} value={section.fields[field.key] ?? ''} onChange={event => onUpdate(current => ({ ...current, fields: { ...current.fields, [field.key]: event.target.value } }))} placeholder={field.placeholder} />
        : <input autoFocus={index === 0} value={section.fields[field.key] ?? ''} onChange={event => onUpdate(current => ({ ...current, fields: { ...current.fields, [field.key]: event.target.value } }))} placeholder={field.placeholder} />}</label>)}
    </div> : <dl>{definition.fields.map(field => <EditableDesignValue key={field.key} label={field.label} value={section.fields[field.key] ?? ''} onEdit={onEdit} />)}</dl>)}
    {custom && (editing ? <div className="mind-section-editor mind-custom-section-editor">
      {section.items.map((item, index) => <div className="mind-explanation-item-editor" key={item.id}>
        <header><strong>自定义项 {index + 1}</strong><button type="button" aria-label={`删除自定义项${item.label || index + 1}`} onClick={() => onUpdate(current => ({ ...current, items: current.items.filter(candidate => candidate.id !== item.id) }))}><Trash size={10} /></button></header>
        <label><span>字段名称</span><input autoFocus={index === 0} value={item.label} onChange={event => onUpdate(current => ({ ...current, items: current.items.map(candidate => candidate.id === item.id ? { ...candidate, label: event.target.value } : candidate) }))} placeholder="例如：补充说明" /></label>
        <label><span>字段内容</span><textarea value={item.content} onChange={event => onUpdate(current => ({ ...current, items: current.items.map(candidate => candidate.id === item.id ? { ...candidate, content: event.target.value } : candidate) }))} placeholder="输入具体内容" /></label>
      </div>)}
      <button className="mind-inline-add is-block" type="button" onClick={() => onUpdate(current => ({ ...current, items: [...current.items, { id: makeID('explanation-item'), label: '', content: '' }] }))}><Plus size={10} weight="bold" />添加自定义字段</button>
    </div> : <dl>{section.items.length > 0
      ? section.items.map(item => <EditableDesignValue key={item.id} label={item.label || '自定义字段'} value={item.content} onEdit={onEdit} />)
      : <div><dt>自定义内容</dt><dd><button className="mind-inline-add" type="button" onClick={event => { event.stopPropagation(); onEdit() }}><Plus size={10} weight="bold" />添加自定义字段</button></dd></div>}
    </dl>)}
  </section>
}

function ExplanationTemplatePicker({ nodeTitle, onSelect, onClose }: {
  nodeTitle: string
  onSelect: (type: AnalysisExplanationTemplateType) => void
  onClose: () => void
}) {
  return <section className="mind-explanation-template-picker" role="dialog" aria-label={`为${nodeTitle}选择内容卡片模板`} onClick={event => event.stopPropagation()}>
    <header><span><strong>选择内容卡片</strong><small>任何节点都可以自由组合所需内容，选择后直接编辑</small></span><button type="button" aria-label="关闭内容卡片选择" onClick={onClose}><X size={12} /></button></header>
    <div className="mind-content-template-groups">{Object.entries(contentCategoryLabels).map(([category, categoryLabel]) => {
      const types = explanationTemplateOrder.filter(type => explanationTemplateDefinitions[type].category === category)
      if (types.length === 0) return null
      return <section key={category}><strong>{categoryLabel}</strong><div>{types.map(type => {
        const definition = explanationTemplateDefinitions[type]
        return <button className={`is-${type.toLocaleLowerCase().replaceAll('_', '-')}`} type="button" key={type} onClick={() => onSelect(type)}>
          <i>{definition.title.slice(0, 1)}</i><span><strong>{definition.title}</strong><small>{definition.description}</small></span><CaretRight size={12} />
        </button>
      })}</div></section>
    })}</div>
  </section>
}

function MindMapNode({ node, parentID, branchColor, root, selectedID, searchMatchIDs, activeSearchID, inlineEditingKey, onSelect, onBeginEdit, onEndEdit, onUpdate, onAddChild, onDelete }: MindMapNodeProps) {
  const [explanationPickerOpen, setExplanationPickerOpen] = useState(false)
  const editingTitle = inlineEditingKey === `${node.id}:TITLE`
  const editing = inlineEditingKey.startsWith(`${node.id}:`)
  const nodeProps = { selectedID, searchMatchIDs, activeSearchID, inlineEditingKey, onSelect, onBeginEdit, onEndEdit, onUpdate, onAddChild, onDelete }
  return <li>
    <article
      className={`mind-node ${root ? 'is-root' : ''} ${selectedID === node.id ? 'is-selected' : ''} ${searchMatchIDs.has(node.id) ? 'is-search-match' : ''} ${activeSearchID === node.id ? 'is-search-active' : ''} ${editing ? 'is-editing' : ''}`.trim()}
      aria-label={`分析节点：${node.title}`}
      data-node-id={node.id}
      data-parent-id={parentID ?? undefined}
      data-branch-color={branchColor}
      style={{ '--branch-color': branchColor } as CSSProperties}
      onClick={() => onSelect(node.id)}
    >
      <header><small>{node.children.length} 个下级</small><div>
        <button type="button" aria-label={`继续拆解${node.title}`} title="继续拆解问题" onClick={event => { event.stopPropagation(); onAddChild(node.id) }}><Plus size={13} /></button>
        <button type="button" aria-label={`删除${node.title}`} onClick={event => { event.stopPropagation(); onDelete(node.id) }}><Trash size={13} /></button>
      </div></header>
      <div className="mind-node-summary">
        {editingTitle ? <div className="mind-node-title-editor"><input autoFocus data-node-title value={node.title} aria-label="节点名称" onChange={event => onUpdate(node.id, current => ({ ...current, title: event.target.value }))} /><InlineEditButton label="完成题目编辑" done onClick={onEndEdit} /></div>
          : <div className="mind-node-title-row"><strong data-node-title>{node.title}</strong><InlineEditButton label={`修改题目：${node.title}`} onClick={() => onBeginEdit(node.id, 'TITLE')} /></div>}
        <div className="mind-node-design-groups">
          {node.explanationSections.map(section => <ExplanationDesignSection
            key={section.id}
            section={section}
            editing={inlineEditingKey === `${node.id}:CUSTOM:${section.id}`}
            onEdit={() => onBeginEdit(node.id, `CUSTOM:${section.id}`)}
            onDone={onEndEdit}
            onUpdate={update => onUpdate(node.id, current => ({ ...current, explanationSections: current.explanationSections.map(candidate => candidate.id === section.id ? update(candidate) : candidate) }))}
            onDelete={() => onUpdate(node.id, current => ({ ...current, explanationSections: current.explanationSections.filter(candidate => candidate.id !== section.id) }))}
          />)}
          <button className="mind-add-section" type="button" aria-expanded={explanationPickerOpen} onClick={event => { event.stopPropagation(); setExplanationPickerOpen(current => !current) }}><PlusCircle size={12} />新增内容卡片</button>
          {explanationPickerOpen && <ExplanationTemplatePicker nodeTitle={node.title} onClose={() => setExplanationPickerOpen(false)} onSelect={type => {
            const sectionID = makeID('explanation-section')
            const definition = explanationTemplateDefinitions[type]
            const itemConfig = type === 'METRIC_DISPLAY' ? {
              ...defaultItemConfig(),
              metricDisplays: [{ id: makeID('metric'), metric: '', role: 'CORE' as const, displayForm: 'LINE_CHART' as const, displayRequirements: '' }],
            } : undefined
            const section: AnalysisExplanationSection = {
              id: sectionID,
              templateType: type,
              title: definition.title,
              fields: Object.fromEntries(definition.fields.map(field => [field.key, ''])),
              conclusionFormat: type === 'CONCLUSION_OUTPUT' ? defaultConclusionFormat() : undefined,
              itemConfig,
              items: definition.customItemLabels.map(label => ({ id: makeID('explanation-item'), label, content: '' })),
            }
            onUpdate(node.id, current => ({ ...current, explanationSections: [...current.explanationSections, section] }))
            setExplanationPickerOpen(false)
            onBeginEdit(node.id, `CUSTOM:${sectionID}`)
          }} />}
        </div>
      </div>
    </article>
    {node.children.length > 0 && <ul>{node.children.map((child, index) => <MindMapNode key={child.id} node={child} parentID={node.id} branchColor={root ? mindMapBranchColors[index % mindMapBranchColors.length] : branchColor} root={false} {...nodeProps} />)}</ul>}
  </li>
}

function TemplateMindMapDialog({ template, onClose, onSave }: { template: AnalysisTemplate; onClose: () => void; onSave: (template: AnalysisTemplate) => void }) {
  const [draft, setDraft] = useState(() => normalizeAnalysisTemplate(cloneTemplates([template])[0]))
  const [baselineJSON, setBaselineJSON] = useState(() => JSON.stringify(draft))
  const [selectedID, setSelectedID] = useState(template.analysisTree[0]?.id ?? '')
  const [jsonOpen, setJsonOpen] = useState(false)
  const [inlineEditingKey, setInlineEditingKey] = useState('')
  const [canvasQuery, setCanvasQuery] = useState('')
  const [searchIndex, setSearchIndex] = useState(0)
  const [zoom, setZoom] = useState(100)
  const [panning, setPanning] = useState(false)
  const [closeDialogOpen, setCloseDialogOpen] = useState(false)
  const [saveDialogMode, setSaveDialogMode] = useState<'SAVE' | 'CLOSE_DRAFT'>()
  const [feedback, setFeedback] = useState('')
  const canvasRef = useRef<HTMLDivElement>(null)
  const panStart = useRef({ x: 0, y: 0, left: 0, top: 0 })
  const json = JSON.stringify(draft, null, 2)
  const isDirty = JSON.stringify(draft) !== baselineJSON
  const allNodes = useMemo(() => flattenAnalysisNodes(draft.analysisTree), [draft.analysisTree])
  const normalizedCanvasQuery = canvasQuery.trim().toLocaleLowerCase()
  const searchMatches = useMemo(() => normalizedCanvasQuery
    ? allNodes.filter(node => analysisNodeSearchText(node).includes(normalizedCanvasQuery))
    : [], [allNodes, normalizedCanvasQuery])
  const searchMatchIDs = useMemo(() => new Set(searchMatches.map(node => node.id)), [searchMatches])
  const safeSearchIndex = searchMatches.length > 0 ? searchIndex % searchMatches.length : 0
  const activeSearchID = searchMatches.length > 0 ? searchMatches[safeSearchIndex].id : ''

  const requestClose = useCallback(() => {
    if (!isDirty && draft.status !== 'DRAFT') {
      clearEditorRecovery(draft.id)
      onClose()
      return
    }
    setCloseDialogOpen(true)
  }, [draft.id, draft.status, isDirty, onClose])

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      if (saveDialogMode) setSaveDialogMode(undefined)
      else if (closeDialogOpen) setCloseDialogOpen(false)
      else requestClose()
    }
    document.addEventListener('keydown', closeOnEscape)
    return () => document.removeEventListener('keydown', closeOnEscape)
  }, [closeDialogOpen, requestClose, saveDialogMode])

  useEffect(() => {
    if (!isDirty) return undefined
    const timer = window.setTimeout(() => saveEditorRecovery(draft), 300)
    return () => window.clearTimeout(timer)
  }, [draft, isDirty])

  useEffect(() => {
    const preserveDraft = () => { if (isDirty) saveEditorRecovery(draft) }
    window.addEventListener('beforeunload', preserveDraft)
    return () => window.removeEventListener('beforeunload', preserveDraft)
  }, [draft, isDirty])

  useEffect(() => {
    const centerRoot = () => {
      const canvas = canvasRef.current
      const rootNode = canvas?.querySelector<HTMLElement>('.template-mind-tree > li > .mind-node')
      if (!canvas || !rootNode) return
      rootNode.scrollIntoView({ block: 'center', inline: 'start' })
    }
    const timers = [40, 160, 320].map(delay => window.setTimeout(centerRoot, delay))
    return () => timers.forEach(timer => window.clearTimeout(timer))
  }, [])

  useEffect(() => {
    if (!activeSearchID) return undefined
    const timer = window.setTimeout(() => {
      const element = canvasRef.current?.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(activeSearchID)}"]`)
      element?.scrollIntoView({ block: 'center', inline: 'center', behavior: 'smooth' })
      setSelectedID(activeSearchID)
    }, 80)
    return () => window.clearTimeout(timer)
  }, [activeSearchID])

  const updateNode = (id: string, update: (node: AnalysisNode) => AnalysisNode) => setDraft(current => ({ ...current, analysisTree: updateAnalysisNode(current.analysisTree, id, update) }))
  const selectNode = (id: string) => setSelectedID(id)

  const addNode = (parentID: string | null) => {
    const id = makeID('analysis-node')
    const child = createAnalysisNode(id, parentID ? '要进一步回答什么问题？' : '要回答什么核心问题？')
    setDraft(current => ({ ...current, analysisTree: appendAnalysisNode(current.analysisTree, parentID, child) }))
    setSelectedID(id)
    setInlineEditingKey(`${id}:TITLE`)
    window.setTimeout(() => canvasRef.current?.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'center', inline: 'center', behavior: 'smooth' }), 100)
  }

  const deleteNode = (id: string) => {
    const target = findAnalysisNode(draft.analysisTree, id)
    if (!target || !window.confirm(`确认删除“${target.title}”及其全部下级节点吗？`)) return
    const parent = findAnalysisNodeParent(draft.analysisTree, id)
    const nextTree = removeAnalysisNode(draft.analysisTree, id)
    setDraft(current => ({ ...current, analysisTree: nextTree }))
    setSelectedID(parent?.id ?? nextTree[0]?.id ?? '')
    setInlineEditingKey('')
  }

  const changeZoom = (nextZoom: number) => setZoom(Math.min(200, Math.max(50, nextZoom)))
  const moveSearch = (direction: number) => {
    if (searchMatches.length === 0) return
    setSearchIndex(current => (current + direction + searchMatches.length) % searchMatches.length)
  }

  const copyJSON = async () => {
    await navigator.clipboard.writeText(json)
    setFeedback('JSON 已复制')
    window.setTimeout(() => setFeedback(''), 1800)
  }

  const downloadJSON = () => {
    const url = URL.createObjectURL(new Blob([json], { type: 'application/json;charset=utf-8' }))
    const link = document.createElement('a')
    link.href = url
    link.download = `${draft.code || draft.id}.json`
    link.click()
    URL.revokeObjectURL(url)
    setFeedback('JSON 已下载')
    window.setTimeout(() => setFeedback(''), 1800)
  }

  const commitTemplate = (input: TemplateSaveInput, status: TemplateStatus, closeAfterSave = false) => {
    const saved = {
      ...draft,
      ...input,
      status,
      version: status === 'ACTIVE' && draft.status !== 'ACTIVE' ? draft.version + 1 : draft.version,
      updatedAt: new Date().toISOString(),
    }
    setBaselineJSON(JSON.stringify(saved))
    setDraft(saved)
    onSave(saved)
    setSaveDialogMode(undefined)
    setCloseDialogOpen(false)
    if (status === 'ACTIVE') clearEditorRecovery(saved.id)
    else saveEditorRecovery(saved)
    if (closeAfterSave) onClose()
    else {
      setFeedback(status === 'ACTIVE' ? '模板已保存并发布' : '模板已保存为草稿')
      window.setTimeout(() => setFeedback(''), 1800)
    }
  }

  const discardAndClose = () => {
    clearEditorRecovery(draft.id)
    onClose()
  }

  return <div className="template-editor-backdrop" role="presentation">
    <section className="template-editor" role="dialog" aria-modal="true" aria-labelledby="template-editor-title">
      <header className="template-editor-header">
        <div className={`template-editor-type is-${draft.templateType.toLocaleLowerCase()}`}><TemplateTypeIcon type={draft.templateType} size={20} /></div>
        <div className="template-editor-heading"><span>{templateTypeLabels[draft.templateType]}模板 · {draft.code}</span><h2 id="template-editor-title">{draft.name}</h2></div>
        <div className="template-editor-meta"><span>v{draft.version}</span><span>{countAnalysisNodes(draft.analysisTree)} 个节点</span><span>JSON Schema {draft.schemaVersion}</span></div>
        <div className="template-editor-actions"><AppButton plain size="small" type="button" aria-pressed={jsonOpen} onClick={() => setJsonOpen(current => !current)}><BracketsCurly size={16} />JSON</AppButton><AppButton plain size="small" type="button" onClick={() => void copyJSON()}><Copy size={16} />复制</AppButton><AppButton plain size="small" type="button" onClick={downloadJSON}><DownloadSimple size={16} />下载</AppButton><AppButton variant="primary" size="small" type="button" onClick={() => setSaveDialogMode('SAVE')}><FloppyDisk size={16} />保存模板</AppButton><AppButton text circle type="button" aria-label="关闭编辑器" onClick={requestClose}><X size={20} /></AppButton></div>
      </header>

      <div className={`template-editor-body ${jsonOpen ? 'is-inspector-open' : 'is-canvas-only'}`}>
        <main className="mind-map-panel">
          <header><div><TreeStructure size={18} /><span><strong>分析思路工作画布</strong><small>从核心问题开始逐步拆解，通常两到三层即可；指标、口径、规则和结论通过内容卡片配置</small></span></div><div className="mind-map-toolbar">
            <label className="mind-map-search"><MagnifyingGlass size={14} /><input value={canvasQuery} onChange={event => { setCanvasQuery(event.target.value); setSearchIndex(0) }} onKeyDown={event => { if (event.key === 'Enter') moveSearch(event.shiftKey ? -1 : 1) }} placeholder="搜索节点、指标或说明" aria-label="搜索画布内容" />{normalizedCanvasQuery && <span>{searchMatches.length > 0 ? `${safeSearchIndex + 1}/${searchMatches.length}` : '0'}</span>}</label>
            {normalizedCanvasQuery && <div className="mind-map-search-nav"><button type="button" aria-label="上一个搜索结果" disabled={searchMatches.length === 0} onClick={() => moveSearch(-1)}><CaretLeft size={13} /></button><button type="button" aria-label="下一个搜索结果" disabled={searchMatches.length === 0} onClick={() => moveSearch(1)}><CaretRight size={13} /></button></div>}
            <div className="mind-map-zoom"><button type="button" aria-label="缩小画布" onClick={() => changeZoom(zoom - 10)} disabled={zoom <= 50}><Minus size={13} /></button><button type="button" aria-label="恢复 100%" className="mind-map-zoom-value" onClick={() => changeZoom(100)}>{zoom}%</button><button type="button" aria-label="放大画布" onClick={() => changeZoom(zoom + 10)} disabled={zoom >= 200}><Plus size={13} /></button></div>
            <span className="mind-map-pan-hint"><Hand size={13} />拖动画布</span>
          </div></header>
          <div
            className={`mind-map-canvas ${panning ? 'is-panning' : ''}`}
            ref={canvasRef}
            onPointerDown={event => {
              const target = event.target as HTMLElement
              if (target.closest('.mind-node, button, input, textarea, select')) return
              panStart.current = { x: event.clientX, y: event.clientY, left: event.currentTarget.scrollLeft, top: event.currentTarget.scrollTop }
              event.currentTarget.setPointerCapture(event.pointerId)
              setPanning(true)
            }}
            onPointerMove={event => {
              if (!panning) return
              event.currentTarget.scrollLeft = panStart.current.left - (event.clientX - panStart.current.x)
              event.currentTarget.scrollTop = panStart.current.top - (event.clientY - panStart.current.y)
            }}
            onPointerUp={event => { if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId); setPanning(false) }}
            onPointerCancel={() => setPanning(false)}
            onWheel={event => { if (!event.ctrlKey && !event.metaKey) return; event.preventDefault(); changeZoom(zoom + (event.deltaY < 0 ? 10 : -10)) }}
          >
            <div className="mind-map-content" style={{ zoom: zoom / 100 }}>
              <MindMapConnections revision={`${json}:${inlineEditingKey}:${jsonOpen}:${zoom}`} scale={zoom / 100} />
              <ul className="template-mind-tree">{draft.analysisTree.map((node, index) => <MindMapNode
                key={node.id}
                node={node}
                parentID={null}
                branchColor={mindMapBranchColors[index % mindMapBranchColors.length]}
                root
                selectedID={selectedID}
                searchMatchIDs={searchMatchIDs}
                activeSearchID={activeSearchID}
                inlineEditingKey={inlineEditingKey}
                onSelect={selectNode}
                onBeginEdit={(id, section) => { selectNode(id); setInlineEditingKey(`${id}:${section}`) }}
                onEndEdit={() => setInlineEditingKey('')}
                onUpdate={updateNode}
                onAddChild={addNode}
                onDelete={deleteNode}
              />)}</ul>
            </div>
            {draft.analysisTree.length === 0 && <div className="mind-map-empty"><TreeStructure size={28} /><strong>还没有核心问题</strong><span>先写下这份模板最终需要回答的问题。</span><AppButton variant="primary" size="small" type="button" onClick={() => addNode(null)}><Plus size={15} />添加核心问题</AppButton></div>}
          </div>
        </main>

        {jsonOpen && <aside className="node-inspector">
          <div className="template-json-panel">
            <header><div><BracketsCurly size={17} /><span><strong>模板 JSON</strong><small>当前思路的可持久化数据</small></span></div><div className="template-json-status"><span className="json-valid"><Check size={13} weight="bold" />结构有效</span><button type="button" aria-label="关闭 JSON" onClick={() => setJsonOpen(false)}><X size={14} /></button></div></header>
            <pre>{json}</pre>
            <footer><AppButton plain size="small" type="button" onClick={() => void copyJSON()}><Copy size={15} />复制 JSON</AppButton><AppButton variant="primary" size="small" type="button" onClick={downloadJSON}><DownloadSimple size={15} />下载文件</AppButton></footer>
          </div>
        </aside>}
      </div>
      <footer className="template-editor-footer"><span><ClockCounterClockwise size={14} />编辑中自动保留恢复快照，关闭时可保存为草稿</span><span>模板结构将以 JSON 数据沉淀，可供报告生成流程直接读取</span></footer>
      {feedback && <div className="template-editor-toast"><Check size={15} weight="bold" />{feedback}</div>}
      {closeDialogOpen && !saveDialogMode && <EditorCloseDialog templateName={draft.name} onSaveDraft={() => { setCloseDialogOpen(false); setSaveDialogMode('CLOSE_DRAFT') }} onDiscard={discardAndClose} onContinue={() => setCloseDialogOpen(false)} />}
      {saveDialogMode && <TemplateSaveDialog
        template={draft}
        closeAfterSave={saveDialogMode === 'CLOSE_DRAFT'}
        onCancel={() => setSaveDialogMode(undefined)}
        onSaveDraft={input => commitTemplate(input, 'DRAFT', saveDialogMode === 'CLOSE_DRAFT')}
        onPublish={input => commitTemplate(input, 'ACTIVE')}
      />}
    </section>
  </div>
}

/** 管理报告与报表两类分析思路模板，并将可视化配置沉淀为 JSON。 */
export function TemplateCenterPage() {
  const [templates, setTemplates] = useState(loadTemplates)
  const [query, setQuery] = useState('')
  const [typeFilter, setTypeFilter] = useState<TemplateType | 'ALL'>('ALL')
  const [statusFilter, setStatusFilter] = useState<TemplateStatus | 'ALL'>('ALL')
  const [createOpen, setCreateOpen] = useState(false)
  const [editingID, setEditingID] = useState('')
  const [creatingTemplate, setCreatingTemplate] = useState<AnalysisTemplate>()
  const [recoveryPrompt, setRecoveryPrompt] = useState<TemplateEditorRecovery>()
  const [pageFeedback, setPageFeedback] = useState('')

  useEffect(() => {
    try { window.localStorage.setItem(templateStorageKey, JSON.stringify(templates)) } catch { /* keep the in-memory editor usable */ }
  }, [templates])

  const visibleTemplates = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase()
    return templates.filter(template => {
      if (typeFilter !== 'ALL' && template.templateType !== typeFilter) return false
      if (statusFilter !== 'ALL' && template.status !== statusFilter) return false
      if (!normalized) return true
      return [template.name, template.code, template.description, ...template.tags].some(value => value.toLocaleLowerCase().includes(normalized))
    })
  }, [query, statusFilter, templates, typeFilter])

  const editingTemplate = creatingTemplate?.id === editingID ? creatingTemplate : templates.find(template => template.id === editingID)
  const reportCount = templates.filter(template => template.templateType === 'REPORT').length
  const tableCount = templates.filter(template => template.templateType === 'TABLE').length
  const publishedCount = templates.filter(template => template.status === 'ACTIVE').length

  const showPageFeedback = (message: string) => {
    setPageFeedback(message)
    window.setTimeout(() => setPageFeedback(''), 1800)
  }

  const beginCreateTemplate = () => {
    const recovery = loadEditorRecovery()
    if (recovery) {
      setRecoveryPrompt(recovery)
      return
    }
    setCreateOpen(true)
  }

  const restoreLastEdit = () => {
    if (!recoveryPrompt) return
    setTemplates(current => current.some(template => template.id === recoveryPrompt.template.id)
      ? current.map(template => template.id === recoveryPrompt.template.id ? recoveryPrompt.template : template)
      : [recoveryPrompt.template, ...current])
    setEditingID(recoveryPrompt.template.id)
    setRecoveryPrompt(undefined)
  }

  const startNewWithoutRecovery = () => {
    clearEditorRecovery(recoveryPrompt?.template.id)
    setRecoveryPrompt(undefined)
    setCreateOpen(true)
  }

  const createTemplate = (input: NewTemplateDraft) => {
    const id = makeID('template')
    const template = createTemplateSkeleton({
      id,
      templateType: input.templateType,
      name: input.templateType === 'REPORT' ? '未命名报告模板' : '未命名报表模板',
      code: generateTemplateCode(input.templateType),
      description: '',
      now: new Date().toISOString(),
    })
    setCreatingTemplate(template)
    clearEditorRecovery()
    setCreateOpen(false)
    setEditingID(id)
  }

  const saveTemplate = (template: AnalysisTemplate) => {
    setTemplates(current => current.some(item => item.id === template.id)
      ? current.map(item => item.id === template.id ? template : item)
      : [template, ...current])
    setCreatingTemplate(current => current?.id === template.id ? undefined : current)
  }

  const duplicateTemplate = (template: AnalysisTemplate) => {
    const now = new Date().toISOString()
    let copyNumber = 1
    let copyCode = `${template.code}-COPY`
    while (templates.some(item => item.code === copyCode)) {
      copyNumber += 1
      copyCode = `${template.code}-COPY-${copyNumber}`
    }
    const copy: AnalysisTemplate = {
      ...cloneTemplates([template])[0],
      id: makeID('template'),
      code: copyCode,
      name: `${template.name} 副本${copyNumber > 1 ? ` ${copyNumber}` : ''}`,
      status: 'DRAFT',
      version: 1,
      updatedAt: now,
      usageCount: 0,
      analysisTree: template.analysisTree.map(cloneNodeForDuplicate),
    }
    setTemplates(current => [copy, ...current])
    showPageFeedback(`已复制“${template.name}”`)
  }

  const downloadTemplate = (template: AnalysisTemplate) => {
    const url = URL.createObjectURL(new Blob([JSON.stringify(template, null, 2)], { type: 'application/json;charset=utf-8' }))
    const link = document.createElement('a')
    link.href = url
    link.download = `${template.code || template.id}.json`
    link.click()
    URL.revokeObjectURL(url)
    showPageFeedback(`已导出“${template.name}”`)
  }

  const toggleTemplateStatus = (template: AnalysisTemplate) => {
    const status: TemplateStatus = template.status === 'ACTIVE' ? 'OFFLINE' : 'ACTIVE'
    setTemplates(current => current.map(item => item.id === template.id ? { ...item, status, updatedAt: new Date().toISOString() } : item))
    showPageFeedback(status === 'ACTIVE' ? `已发布“${template.name}”` : `已下架“${template.name}”`)
  }

  const deleteTemplate = (template: AnalysisTemplate) => {
    if (!window.confirm(`确认删除模板“${template.name}”吗？删除后无法恢复。`)) return
    setTemplates(current => current.filter(item => item.id !== template.id))
    clearEditorRecovery(template.id)
    showPageFeedback(`已删除“${template.name}”`)
  }

  return <AppShell className="template-center-shell report-workbench-shell" eyebrow="智能报告" title="模板中心" lockBusinessDomain hidePageHeader>
    <main className="template-center">
      <header className="template-center-heading"><div><h1>模板中心</h1><p>将分析方法沉淀为可复用模板，统一报告与报表的生成思路</p></div><AppButton variant="primary" size="small" type="button" onClick={beginCreateTemplate}><Plus size={16} weight="bold" />新建模板</AppButton></header>

      <section className="template-summary" aria-label="模板概览">
        <article><span className="is-all"><SquaresFour size={18} /></span><div><small>全部模板</small><strong>{templates.length}</strong></div><p>当前领域可用</p></article>
        <article><span className="is-report"><FileText size={18} /></span><div><small>报告模板</small><strong>{reportCount}</strong></div><p>章节化分析输出</p></article>
        <article><span className="is-table"><Table size={18} /></span><div><small>报表模板</small><strong>{tableCount}</strong></div><p>指标化固定输出</p></article>
        <article><span className="is-active"><Check size={18} weight="bold" /></span><div><small>已发布</small><strong>{publishedCount}</strong></div><p>当前对用户可用</p></article>
      </section>

      <section className="template-library">
        <header className="template-library-controls">
          <label className="template-search"><MagnifyingGlass size={17} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索模板名称、编码或标签" aria-label="搜索模板" /></label>
          <div className="template-type-tabs" role="tablist" aria-label="模板类型">{([['ALL', '全部'], ['REPORT', '报告'], ['TABLE', '报表']] as const).map(([value, label]) => <AppButton text type="button" role="tab" aria-selected={typeFilter === value} key={value} onClick={() => setTypeFilter(value)}>{label}<span>{value === 'ALL' ? templates.length : templates.filter(item => item.templateType === value).length}</span></AppButton>)}</div>
          <label className="template-status-filter"><span>状态</span><select value={statusFilter} onChange={event => setStatusFilter(event.target.value as TemplateStatus | 'ALL')}><option value="ALL">全部状态</option><option value="ACTIVE">已发布</option><option value="DRAFT">草稿</option><option value="OFFLINE">已下架</option></select></label>
        </header>
        <div className="template-library-context"><div><TreeStructure size={16} /><span>每个模板包含一套可配置的分析思路，点击卡片可查看和编辑思维导图。</span></div><small>共 {visibleTemplates.length} 个模板</small></div>
        {visibleTemplates.length > 0 ? <div className="template-grid">{visibleTemplates.map(template => <TemplateCard
          key={template.id}
          template={template}
          onOpen={() => setEditingID(template.id)}
          onDuplicate={() => duplicateTemplate(template)}
          onDownload={() => downloadTemplate(template)}
          onToggleStatus={() => toggleTemplateStatus(template)}
          onDelete={() => deleteTemplate(template)}
        />)}</div> : <div className="template-empty"><MagnifyingGlass size={28} /><strong>没有符合条件的模板</strong><span>调整搜索内容或筛选条件后重试。</span><AppButton link type="button" onClick={() => { setQuery(''); setTypeFilter('ALL'); setStatusFilter('ALL') }}>清除筛选</AppButton></div>}
      </section>
    </main>
    {createOpen && <NewTemplateDialog onClose={() => setCreateOpen(false)} onCreate={createTemplate} />}
    {editingTemplate && <TemplateMindMapDialog template={editingTemplate} onClose={() => { setEditingID(''); setCreatingTemplate(undefined) }} onSave={saveTemplate} />}
    {recoveryPrompt && <TemplateRecoveryDialog recovery={recoveryPrompt} onRestore={restoreLastEdit} onStartNew={startNewWithoutRecovery} onCancel={() => setRecoveryPrompt(undefined)} />}
    {pageFeedback && <div className="template-center-toast"><Check size={15} weight="bold" />{pageFeedback}</div>}
  </AppShell>
}
