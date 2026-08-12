import { useMemo, useState } from 'react'
import { ArrowRight, Info, Trash, WarningCircle } from '@phosphor-icons/react'
import type { ManifestIndex } from '../render/manifests.ts'
import type { Interaction, ReportComponent, ReportDefinition } from '../render/schema.ts'

/**
 * 图表联动的编排面板。
 *
 * 联动一直是 Report Definition 的一等公民（校验、环检测、能力校验都已就绪），
 * 但没有任何入口可以创建它，于是整层能力实际上是死的。这里给出最小但完整的
 * 编排：点击某个组件的类目时，按映射字段筛选选定的目标组件。
 */

export type InteractionDraft = {
  sourceComponentId: string
  targetComponentIds: string[]
  sourceField: string
  targetField: string
}

/** 组件的类目字段——联动只能沿组件已绑定的维度传播。 */
function categoryFields(component: ReportComponent | undefined): string[] {
  if (!component?.dataBinding?.dimensions) return []
  return component.dataBinding.dimensions.map(item => item.field).filter(Boolean)
}

function supportsClick(manifests: ManifestIndex, component: ReportComponent | undefined) {
  if (!component) return false
  const manifest = manifests.get(component.templateRef.type, component.templateRef.version)
  return Boolean(manifest?.supportedInteractions.includes('CLICK_FILTER'))
}

/** 固定了语义发布版本的组件其查询计划哈希不可变，因此不能作为被联动筛选的目标。 */
function canBeFiltered(component: ReportComponent | undefined) {
  return Boolean(component?.dataBinding && component.dataBinding.bindingMode === 'DATASET_FIELD')
}

export function InteractionPanel({ definition, manifests, sourceComponentId, busy, error, onCreate, onDelete }: {
  definition: ReportDefinition
  manifests: ManifestIndex
  sourceComponentId: string
  busy: boolean
  error: string
  onCreate: (draft: InteractionDraft) => void
  onDelete: (interactionId: string) => void
}) {
  const componentsById = useMemo(
    () => new Map(definition.components.map(component => [component.id, component])),
    [definition],
  )
  const source = componentsById.get(sourceComponentId)
  const existing = (definition.interactions ?? []).filter(item => item.sourceComponentId === sourceComponentId)

  const sourceFields = categoryFields(source)
  const [sourceField, setSourceField] = useState(sourceFields[0] ?? '')
  const [targets, setTargets] = useState<string[]>([])

  const candidates = definition.components.filter(component =>
    component.id !== sourceComponentId && canBeFiltered(component))
  const targetFieldFor = (targetId: string) => {
    const fields = categoryFields(componentsById.get(targetId))
    return fields.includes(sourceField) ? sourceField : fields[0] ?? ''
  }
  // 一条联动共用一个目标字段映射，因此只允许选择目标字段一致的组件。
  const targetField = targets.length > 0 ? targetFieldFor(targets[0]) : ''
  const mismatched = targets.filter(id => targetFieldFor(id) !== targetField)

  const clickable = supportsClick(manifests, source)
  const ready = clickable && sourceField && targets.length > 0 && targetField && mismatched.length === 0

  if (!source) {
    return <p className="report-interaction-empty"><Info size={15} />选中一个组件后可以为它配置联动。</p>
  }

  return <section className="report-interaction-panel" aria-label="图表联动">
    <header><strong>图表联动</strong><small>点击「{source.options.title || source.templateRef.type}」时筛选其它组件</small></header>

    {existing.length > 0 && <ul className="report-interaction-list">
      {existing.map(interaction => <li key={interaction.id}>
        <span>{describeInteraction(interaction, componentsById)}</span>
        <button type="button" aria-label="删除联动" disabled={busy} onClick={() => onDelete(interaction.id)}>
          <Trash size={14} />
        </button>
      </li>)}
    </ul>}

    {!clickable && <p className="report-interaction-note">
      <WarningCircle size={15} />该组件的模板未声明点击能力，不能作为联动来源。
    </p>}
    {clickable && sourceFields.length === 0 && <p className="report-interaction-note">
      <WarningCircle size={15} />该组件没有绑定维度，无法作为联动来源。
    </p>}
    {clickable && candidates.length === 0 && <p className="report-interaction-note">
      <Info size={15} />没有可被联动筛选的目标组件（目标需使用数据集字段绑定）。
    </p>}

    {clickable && sourceFields.length > 0 && candidates.length > 0 && <div className="report-interaction-form">
      <label>来源字段
        <select value={sourceField} onChange={event => setSourceField(event.target.value)}>
          {sourceFields.map(field => <option key={field} value={field}>{field}</option>)}
        </select>
      </label>
      <div className="report-interaction-targets">
        <span>影响组件</span>
        {candidates.map(component => {
          const field = targetFieldFor(component.id)
          return <label key={component.id}>
            <input type="checkbox" checked={targets.includes(component.id)}
              onChange={event => setTargets(current => event.target.checked
                ? [...current, component.id]
                : current.filter(id => id !== component.id))} />
            <span>{component.options.title || component.templateRef.type}</span>
            <em>{field ? `按 ${field} 筛选` : '无可用维度'}</em>
          </label>
        })}
      </div>
      {mismatched.length > 0 && <p className="report-interaction-note is-error">
        <WarningCircle size={15} />所选目标的筛选字段不一致，请分成多条联动配置。
      </p>}
      {error && <p className="report-interaction-note is-error"><WarningCircle size={15} />{error}</p>}
      <button className="primary-button" type="button" disabled={busy || !ready}
        onClick={() => onCreate({ sourceComponentId, targetComponentIds: targets, sourceField, targetField })}>
        <ArrowRight size={15} />{busy ? '正在保存…' : '添加联动'}
      </button>
    </div>}
  </section>
}

function describeInteraction(interaction: Interaction, components: Map<string, ReportComponent>) {
  const names = interaction.targetComponentIds
    .map(id => components.get(id)?.options.title || id)
    .join('、')
  const mapping = interaction.fieldMappings[0]
  return `${interaction.event} → ${interaction.action}：按 ${mapping?.targetField ?? '—'} 筛选 ${names || '—'}`
}
