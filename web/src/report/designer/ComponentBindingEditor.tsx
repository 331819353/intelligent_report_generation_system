import { Plus, X } from '@phosphor-icons/react'
import type { DataContextField } from '../api/editor.ts'
import { editorBindingGroups, type ComponentManifest, type EditorBindingGroup } from '../render/manifests.ts'
import type { FieldBinding } from '../render/schema.ts'

type BindingEditorProps = {
  manifest: ComponentManifest
  dimensions: FieldBinding[]
  measures: FieldBinding[]
  dimensionFields: DataContextField[]
  measureFields: DataContextField[]
  onDimensionsChange: (next: FieldBinding[]) => void
  onMeasuresChange: (next: FieldBinding[]) => void
}

function fieldLabel(field: DataContextField) {
  return field.name && field.name !== field.code ? field.name + ' · ' + field.code : field.code
}

function BindingSelect({ binding, fields, usedFields, label, onChange, onRemove }: {
  binding: FieldBinding
  fields: DataContextField[]
  usedFields: Set<string>
  label: string
  onChange: (field: string) => void
  onRemove?: () => void
}) {
  const available = fields.filter(field => field.code === binding.field || !usedFields.has(field.code))
  return <div className="report-profile-binding-row">
    <select aria-label={label} value={binding.field} onChange={event => onChange(event.target.value)}>
      <option value="">请选择字段</option>
      {available.map(field => <option key={field.code} value={field.code}>{fieldLabel(field)}</option>)}
    </select>
    {onRemove && <button type="button" aria-label={'移除' + label} onClick={onRemove}><X size={14} /></button>}
  </div>
}

function groupBindings(group: EditorBindingGroup, items: FieldBinding[]) {
  return items.map((binding, index) => ({ binding, index }))
    .filter(item => group.roles.includes(item.binding.role))
}

function firstUnusedField(fields: DataContextField[], bindings: FieldBinding[]) {
  const used = new Set(bindings.map(binding => binding.field))
  return fields.find(field => !used.has(field.code))?.code ?? ''
}

function selectedLabel(count: number, required: boolean) {
  if (count > 0) return `已选 ${count} 项`
  return required ? '必选' : '可选'
}

export function ComponentBindingEditor({
  manifest, dimensions, measures, dimensionFields, measureFields,
  onDimensionsChange, onMeasuresChange,
}: BindingEditorProps) {
  const groups = editorBindingGroups(manifest)
  const allBindings = [...dimensions, ...measures]
  const usedFields = new Set(allBindings.map(binding => binding.field).filter(Boolean))

  const update = (group: EditorBindingGroup, index: number, field: string) => {
    const current = group.kind === 'DIMENSION' ? dimensions : measures
    const setCurrent = group.kind === 'DIMENSION' ? onDimensionsChange : onMeasuresChange
    setCurrent(current.map((binding, position) => position === index ? { ...binding, field } : binding))
  }
  const remove = (group: EditorBindingGroup, index: number) => {
    const current = group.kind === 'DIMENSION' ? dimensions : measures
    const setCurrent = group.kind === 'DIMENSION' ? onDimensionsChange : onMeasuresChange
    setCurrent(current.filter((_, position) => position !== index))
  }
  const add = (group: EditorBindingGroup) => {
    const current = group.kind === 'DIMENSION' ? dimensions : measures
    const fields = group.kind === 'DIMENSION' ? dimensionFields : measureFields
    const setCurrent = group.kind === 'DIMENSION' ? onDimensionsChange : onMeasuresChange
    setCurrent([...current, { role: group.roles[0], field: firstUnusedField(fields, allBindings) }])
  }

  return <div className="report-profile-bindings">
    {groups.filter(group => !group.nestedUnder).map(group => {
      const items = groupBindings(group, group.kind === 'DIMENSION' ? dimensions : measures)
      const child = groups.find(candidate => candidate.nestedUnder === group.id)
      const fields = group.kind === 'DIMENSION' ? dimensionFields : measureFields
      const canAdd = items.length < group.max && fields.some(field => !usedFields.has(field.code))

      if (child) {
        const childTotal = groupBindings(child, measures).length
        return <section className="report-profile-group is-metric-group" key={group.id}>
          <header>
            <div><strong>{group.label}</strong><small>{group.description}</small></div>
            <span>{selectedLabel(items.length, group.min > 0)}</span>
          </header>
          {items.map((item, position) => {
            const nextParent = items[position + 1]?.index ?? measures.length
            const children = measures.map((binding, index) => ({ binding, index }))
              .filter(candidate => candidate.index > item.index && candidate.index < nextParent && child.roles.includes(candidate.binding.role))
            const canAddChild = children.length < (child.maxPerParent ?? child.max) && childTotal < child.max &&
              measureFields.some(field => !usedFields.has(field.code))
            const addChild = () => {
              const next = measures.slice()
              next.splice(nextParent, 0, { role: child.roles[0], field: firstUnusedField(measureFields, allBindings) })
              onMeasuresChange(next)
            }
            const removeParent = () => onMeasuresChange(measures.filter((_, index) => index < item.index || index >= nextParent))
            return <article className="report-profile-metric" key={group.id + '-' + item.index}>
              <div className="report-profile-metric-head">
                <span><strong>核心数值 {position + 1}</strong></span>
                {items.length > group.min && <button type="button" aria-label={'移除核心数值 ' + (position + 1)} onClick={removeParent}><X size={14} /></button>}
              </div>
              <BindingSelect binding={item.binding} fields={measureFields} usedFields={usedFields}
                label={'核心数值 ' + (position + 1)} onChange={field => update(group, item.index, field)} />
              <div className="report-profile-companions">
                <div><span><strong>{child.label}</strong><small>{child.description}</small></span>
                  <button type="button" disabled={!canAddChild} onClick={addChild}><Plus size={12} />{child.addLabel}</button></div>
                {children.length === 0 && <p>可选，为该核心数值补充同比、环比或目标达成率</p>}
                {children.map((candidate, childIndex) => <BindingSelect key={child.id + '-' + candidate.index}
                  binding={candidate.binding} fields={measureFields} usedFields={usedFields}
                  label={child.label + ' ' + (childIndex + 1)}
                  onChange={field => update(child, candidate.index, field)}
                  onRemove={() => remove(child, candidate.index)} />)}
              </div>
            </article>
          })}
          {items.length === 0 && <p className="report-profile-empty">请先选择一个要突出展示的数值</p>}
          <button className="report-profile-add" type="button" disabled={!canAdd} onClick={() => add(group)}><Plus size={13} />{group.addLabel}</button>
        </section>
      }

      return <section className="report-profile-group" key={group.id}>
        <header>
          <div><strong>{group.label}</strong><small>{group.description}</small></div>
          <span>{selectedLabel(items.length, group.min > 0)}</span>
        </header>
        {items.length === 0 && <p className="report-profile-empty">{group.min > 0 ? '请选择一项' : '这项可以暂时不配置'}</p>}
        {items.map((item, index) => <div className="report-profile-binding" key={group.id + '-' + item.index}>
          <BindingSelect binding={item.binding} fields={fields} usedFields={usedFields}
            label={group.label + ' ' + (index + 1)} onChange={field => update(group, item.index, field)}
            onRemove={items.length > group.min ? () => remove(group, item.index) : undefined} />
        </div>)}
        {group.max > group.min && <button className="report-profile-add" type="button" disabled={!canAdd} onClick={() => add(group)}><Plus size={13} />{group.addLabel}</button>}
      </section>
    })}
  </div>
}
