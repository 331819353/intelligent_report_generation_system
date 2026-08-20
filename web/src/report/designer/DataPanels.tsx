import { useMemo, useState } from 'react'
import { Check, Copy, Database, Funnel, Info, Plus, Trash, WarningCircle, X } from '@phosphor-icons/react'
import type { DataContextCandidate, DataContextField } from '../api/editor.ts'
import type { GlobalFilter, ReportDefinition } from '../render/schema.ts'
import { orderedPages, orderedSections } from '../render/schema.ts'
import { dataContextInUse, parseFilterOptions, suggestedFilterType, type FilterDraft } from './operations.ts'

/**
 * 报告级“数据集 / 筛选器 / 定义 JSON”三块面板。
 *
 * 报表（报告）开发的核心是一份 Report Definition JSON：卡片绑定的数据集是
 * definition.dataContexts，筛选器是 definition.globalFilters。这里的每个按钮都
 * 只产生受控 Operation，由编辑页统一提交为新修订；面板本身不持有任何草稿状态。
 */

const filterTypeLabels: Record<GlobalFilter['type'], string> = {
  SINGLE_SELECT: '单选',
  MULTI_SELECT: '多选',
  DATE: '日期',
  DATE_RANGE: '日期区间',
  RELATIVE_TIME: '相对时间',
  NUMBER_RANGE: '数值区间',
  SEARCH_SELECT: '搜索选择',
  PARAMETER_INPUT: '参数输入',
  SELECT: '选择（旧）',
  BOOLEAN: '是/否（旧）',
}

const editableFilterTypes: GlobalFilter['type'][] = [
  'MULTI_SELECT', 'SINGLE_SELECT', 'SEARCH_SELECT', 'DATE', 'DATE_RANGE', 'RELATIVE_TIME', 'NUMBER_RANGE', 'PARAMETER_INPUT',
]

const optionFilterTypes = new Set<GlobalFilter['type']>(['SINGLE_SELECT', 'MULTI_SELECT', 'SELECT'])

export function DataContextPanel({ definition, candidates, busy, error, onAdd, onRemove }: {
  definition: ReportDefinition
  candidates: DataContextCandidate[]
  busy: boolean
  error: string
  onAdd: (candidate: DataContextCandidate) => void
  onRemove: (dataContextId: string) => void
}) {
  const present = new Set(definition.dataContexts.map(context => context.id))
  const available = candidates.filter(candidate => !present.has(candidate.dataContext.id))
  const [selected, setSelected] = useState('')
  const chosen = available.find(candidate => candidate.dataContext.id === (selected || available[0]?.dataContext.id))
  const usage = (contextId: string) => definition.components.filter(component => component.dataBinding?.dataContextId === contextId).length

  return <section className="report-interaction-panel report-data-panel" aria-label="报告数据集">
    <header><strong><Database size={15} /> 数据集</strong><small>卡片只能绑定报告内已声明的数据集版本</small></header>
    <ul className="report-interaction-list">
      {definition.dataContexts.map(context => {
        const candidate = candidates.find(item => item.dataContext.id === context.id)
        const bound = usage(context.id)
        return <li key={context.id}>
          <span title={context.datasetVersionId}>{context.alias || candidate?.name || context.id}<em> · {bound} 张卡片{candidate ? '' : ' · 当前不可读'}</em></span>
          <button type="button" aria-label="移除数据集" disabled={busy || definition.dataContexts.length <= 1 || dataContextInUse(definition, context.id)}
            title={dataContextInUse(definition, context.id) ? '仍有卡片或筛选器引用该数据集' : ''}
            onClick={() => onRemove(context.id)}><Trash size={14} /></button>
        </li>
      })}
    </ul>
    {available.length > 0 ? <div className="report-interaction-form">
      <label>添加已发布数据集
        <select value={chosen?.dataContext.id ?? ''} onChange={event => setSelected(event.target.value)}>
          {available.map(candidate => <option key={candidate.dataContext.id} value={candidate.dataContext.id}>{candidate.name} · {candidate.fields.length} 字段</option>)}
        </select>
      </label>
      {error && <p className="report-interaction-note is-error"><WarningCircle size={15} />{error}</p>}
      <button className="primary-button" type="button" disabled={busy || !chosen} onClick={() => chosen && onAdd(chosen)}>
        <Plus size={15} />{busy ? '正在保存…' : '加入报告'}
      </button>
    </div> : <p className="report-interaction-note"><Info size={15} />当前领域内可读的已发布数据集都已加入报告。</p>}
  </section>
}

export function FilterPanel({ definition, candidates, fieldsOf, selectedBlockId, onlyBlock = false, defaultContextId, busy, error, onCreate, onUpdate, onDelete }: {
  definition: ReportDefinition
  candidates: DataContextCandidate[]
  fieldsOf: (dataContextId: string) => DataContextField[]
  /** 当前选中卡片；“仅作用于选中卡片”的作用域从它开始。 */
  selectedBlockId: string
  /** 卡片视角：只列出作用于该卡片的筛选器，新建默认只作用于该卡片。 */
  onlyBlock?: boolean
  /** 新建筛选器默认使用的数据集（卡片视角下取卡片绑定的数据集）。 */
  defaultContextId?: string
  busy: boolean
  error: string
  onCreate: (draft: FilterDraft) => void
  onUpdate: (filter: GlobalFilter, draft: FilterDraft) => void
  onDelete: (filterId: string) => void
}) {
  const allFilters = definition.globalFilters ?? []
  const filters = onlyBlock
    ? allFilters.filter(filter => filter.scope.type === 'REPORT' || (filter.scope.type === 'BLOCK' && filter.scope.targetIds.includes(selectedBlockId)))
    : allFilters
  const contexts = definition.dataContexts
  const [contextId, setContextId] = useState(defaultContextId || contexts[0]?.id || '')
  const fields = fieldsOf(contextId || contexts[0]?.id || '')
  const [field, setField] = useState('')
  const effectiveField = fields.some(item => item.code === field) ? field : fields[0]?.code ?? ''
  const fieldMeta = fields.find(item => item.code === effectiveField)
  const [type, setType] = useState<GlobalFilter['type'] | ''>('')
  const effectiveType = type || suggestedFilterType(fieldMeta)
  const [optionsText, setOptionsText] = useState('')
  const [optionDrafts, setOptionDrafts] = useState<Record<string, string>>({})
  const [scopeMode, setScopeMode] = useState<'REPORT' | 'BLOCK'>(onlyBlock && selectedBlockId ? 'BLOCK' : 'REPORT')
  const [targets, setTargets] = useState<string[]>([])
  // 新增表单默认收起：已有筛选器时面板先展示现状，点“添加”再展开表单。
  const [adding, setAdding] = useState(false)
  const formOpen = adding || filters.length === 0

  const blocks = useMemo(() => orderedPages(definition).flatMap(page => orderedSections(page).flatMap(section => section.blocks.map(block => {
    const componentId = block.zones.flatMap(zone => zone.slots.map(slot => slot.componentId)).find(Boolean)
    const component = definition.components.find(item => item.id === componentId)
    return { id: block.id, name: component?.options.title || component?.templateRef.type || block.id, sectionName: section.name }
  }))), [definition])
  const contextName = (id: string) => contexts.find(context => context.id === id)?.alias || candidates.find(item => item.dataContext.id === id)?.name || id
  const fieldMetaOf = (filter: GlobalFilter) => fieldsOf(filter.fieldRef.dataContextId).find(item => item.code === filter.fieldRef.field)
  const filterLabel = (filter: GlobalFilter) => filter.label?.trim() || fieldMetaOf(filter)?.name || filter.fieldRef.field
  const chosenTargets = scopeMode === 'BLOCK' ? (targets.length ? targets : selectedBlockId ? [selectedBlockId] : []) : []
  const parsedOptions = parseFilterOptions(optionsText)
  const needsOptions = optionFilterTypes.has(effectiveType)
  const ready = Boolean(contextId && effectiveField) && (!needsOptions || parsedOptions.length > 0) && (scopeMode === 'REPORT' || chosenTargets.length > 0)

  const submit = () => {
    onCreate({
      dataContextId: contextId, field: effectiveField, type: effectiveType,
      label: fieldMeta?.name || effectiveField,
      ...(needsOptions ? { options: parsedOptions } : {}),
      scope: scopeMode === 'REPORT' ? { type: 'REPORT' } : { type: 'BLOCK', targetIds: chosenTargets },
    })
    setOptionsText('')
    setAdding(false)
  }

  return <section className="report-interaction-panel report-filter-panel" aria-label="报告筛选器">
    <header><strong><Funnel size={15} /> {onlyBlock ? '过滤字段' : '筛选器'}</strong><small>{onlyBlock ? '作用于这张卡片；输入控件固定显示在报告头下方' : '筛选栏属于报告固定结构，不参与正文布局'}</small></header>
    {filters.length > 0 && <ul className="report-interaction-list">
      {filters.map(filter => {
        const storedOptions = filter.options ?? []
        const optionText = optionDrafts[filter.id] ?? storedOptions.join('，')
        const nextOptions = parseFilterOptions(optionText)
        const optionsChanged = nextOptions.join('\u0000') !== storedOptions.join('\u0000')
        return <li className="report-filter-item" key={filter.id}>
          <div className="report-filter-item-head">
            <span>
              <strong>{filterLabel(filter)}</strong><small>{contextName(filter.fieldRef.dataContextId)} · {filter.fieldRef.field} · {filterTypeLabels[filter.type] ?? filter.type}</small>
            </span>
            <span className="report-filter-actions">
              <select aria-label="筛选器作用范围" disabled={busy} value={filter.scope.type === 'REPORT' ? 'REPORT' : 'BLOCK'}
                onChange={event => onUpdate(filter, {
                  dataContextId: filter.fieldRef.dataContextId, field: filter.fieldRef.field, type: filter.type,
                  scope: event.target.value === 'REPORT' ? { type: 'REPORT' } : { type: 'BLOCK', targetIds: selectedBlockId ? [selectedBlockId] : [] },
                })}>
                <option value="REPORT">全报告</option>
                <option value="BLOCK" disabled={!selectedBlockId && filter.scope.type !== 'BLOCK'}>选中卡片</option>
              </select>
              <button type="button" aria-label="删除筛选器" disabled={busy} onClick={() => onDelete(filter.id)}><Trash size={14} /></button>
            </span>
          </div>
          {optionFilterTypes.has(filter.type) && <div className="report-filter-option-editor">
            <label htmlFor={`filter-options-${filter.id}`}>候选值{storedOptions.length === 0 && <em>必填</em>}</label>
            <div><input id={`filter-options-${filter.id}`} value={optionText} placeholder="例如：是，否"
              onChange={event => setOptionDrafts(current => ({ ...current, [filter.id]: event.target.value }))} />
              <button type="button" disabled={busy || nextOptions.length === 0 || !optionsChanged}
                onClick={() => onUpdate(filter, {
                  dataContextId: filter.fieldRef.dataContextId, field: filter.fieldRef.field, type: filter.type,
                  label: filterLabel(filter), options: nextOptions,
                  scope: filter.scope.type === 'REPORT' ? { type: 'REPORT' } : { type: 'BLOCK', targetIds: filter.scope.targetIds },
                })}><Check size={13} />保存值</button></div>
            {storedOptions.length === 0 && <small><WarningCircle size={12} />单选/多选必须先配置真实候选值，运行页才会显示选择框。</small>}
          </div>}
        </li>
      })}
    </ul>}
    {contexts.length === 0 && <p className="report-interaction-note"><Info size={15} />先为报告添加数据集，再配置筛选器。</p>}
    {contexts.length > 0 && !formOpen && <button className="quiet-button report-filter-add" type="button" disabled={busy} onClick={() => setAdding(true)}>
      <Plus size={15} />{onlyBlock ? '为此卡片添加过滤字段' : '添加筛选条件'}
    </button>}
    {contexts.length > 0 && formOpen && <div className="report-interaction-form">
      {contexts.length > 1 && <label>数据集
        <select value={contextId} onChange={event => { setContextId(event.target.value); setField(''); setType('') }}>
          {contexts.map(context => <option key={context.id} value={context.id}>{contextName(context.id)}</option>)}
        </select>
      </label>}
      <label>字段
        <select value={effectiveField} onChange={event => { setField(event.target.value); setType('') }}>
          {fields.map(item => <option key={item.code} value={item.code}>{item.name || item.code} · {item.code}</option>)}
        </select>
      </label>
      <label>类型
        <select value={effectiveType} onChange={event => { setType(event.target.value as GlobalFilter['type']); setOptionsText('') }}>
          {editableFilterTypes.map(item => <option key={item} value={item}>{filterTypeLabels[item]}</option>)}
        </select>
      </label>
      {needsOptions && <label>候选值（必填）
        <input value={optionsText} onChange={event => setOptionsText(event.target.value)} placeholder="例如：是，否；支持逗号、分号或换行" />
        <small>这里填写数据字段中的真实值；未配置时不能创建选择型筛选。</small>
      </label>}
      <label>作用范围
        <select value={scopeMode} onChange={event => setScopeMode(event.target.value as 'REPORT' | 'BLOCK')}>
          <option value="REPORT">全报告（所有绑定该数据集的卡片）</option>
          <option value="BLOCK">指定卡片</option>
        </select>
      </label>
      {scopeMode === 'BLOCK' && <div className="report-interaction-targets">
        <span>作用卡片</span>
        {blocks.map(block => <label key={block.id}>
          <input type="checkbox" checked={chosenTargets.includes(block.id)}
            onChange={event => setTargets(current => {
              const base = current.length ? current : chosenTargets
              return event.target.checked ? [...new Set([...base, block.id])] : base.filter(id => id !== block.id)
            })} />
          <span>{block.name}</span><em>{block.sectionName}</em>
        </label>)}
        {blocks.length === 0 && <p className="report-interaction-note"><Info size={15} />画布上还没有卡片。</p>}
      </div>}
      {error && <p className="report-interaction-note is-error"><WarningCircle size={15} />{error}</p>}
      <div className="report-filter-form-actions">
        {filters.length > 0 && <button className="quiet-button" type="button" disabled={busy} onClick={() => setAdding(false)}>取消</button>}
        <button className="primary-button" type="button" disabled={busy || !ready} onClick={submit}>
          <Plus size={15} />{busy ? '正在保存…' : onlyBlock ? '为此卡片添加过滤字段' : '加入筛选栏'}
        </button>
      </div>
    </div>}
  </section>
}

/** 定义 JSON：报表开发的“源文件”。只读展示、复制、下载；导入在新建页完成。 */
export function DefinitionJSONDialog({ definition, onClose }: {
  definition: ReportDefinition
  onClose: () => void
}) {
  const text = useMemo(() => JSON.stringify(definition, null, 2), [definition])
  const [copied, setCopied] = useState(false)
  const download = () => {
    const blob = new Blob([text], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `${definition.metadata.code || 'report'}.report.json`
    anchor.click()
    URL.revokeObjectURL(url)
  }
  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-editor-json-modal" role="dialog" aria-modal="true" aria-labelledby="definition-json-title" onMouseDown={event => event.stopPropagation()}>
      <header>
        <div><span className="eyebrow">Report Definition {definition.schemaVersion}</span><h2 id="definition-json-title">定义 JSON</h2></div>
        <button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button>
      </header>
      <p className="report-editor-binding-note"><Info size={15} />这份 JSON 就是报表本身：数据集、筛选器、页面/章节/卡片布局与组件绑定都在其中。可复制或下载后在「新建报告 → 从 JSON 导入」中生成新的草稿。</p>
      <textarea className="report-editor-json" readOnly value={text} rows={24} aria-label="报告定义 JSON" />
      <footer>
        <button className="quiet-button" type="button" onClick={download}>下载 .json</button>
        <button className="primary-button" type="button" onClick={() => {
          void navigator.clipboard.writeText(text).then(() => { setCopied(true); window.setTimeout(() => setCopied(false), 1800) })
        }}>{copied ? <Check size={16} /> : <Copy size={16} />}{copied ? '已复制' : '复制 JSON'}</button>
      </footer>
    </section>
  </div>
}
