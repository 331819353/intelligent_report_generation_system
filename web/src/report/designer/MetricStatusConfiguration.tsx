import { useEffect, useState } from 'react'
import { ArrowClockwise, Funnel, Plus, SpinnerGap, Trash, WarningCircle } from '@phosphor-icons/react'
import { reportEditorAPI, type DataContextField } from '../api/editor.ts'
import type { ComponentFilterPolicy, FieldBinding, GlobalFilter, MetricAggregation } from '../render/schema.ts'
import { bindingForField } from './operations.ts'

type Props = {
  dataContextId: string
  fields: DataContextField[]
  fieldsOf: (dataContextId: string) => DataContextField[]
  measures: FieldBinding[]
  globalFilters: GlobalFilter[]
  filterPolicy: ComponentFilterPolicy
  onMeasuresChange: (next: FieldBinding[]) => void
  onFilterPolicyChange: (next: ComponentFilterPolicy) => void
}

const formulaLabels: Record<string, string> = {
  SUM: '求和', AVG: '平均值', MIN: '最小值', MAX: '最大值', COUNT: '计数', COUNT_DISTINCT: '去重计数',
}

function fieldLabel(field?: DataContextField) {
  if (!field) return '请选择字段'
  return field.name && field.name !== field.code ? `${field.name} · ${field.code}` : field.code
}

const formulaOptions = Object.entries(formulaLabels)

function valueFromOption(value: string, field?: DataContextField): string | number | boolean {
  const type = `${field?.canonicalType ?? ''} ${field?.semanticType ?? ''}`.toUpperCase()
  if (type.includes('BOOL')) return value.toLowerCase() === 'true' || value === '1' || value === '是'
  if (/(INT|NUMBER|NUMERIC|DECIMAL|FLOAT|DOUBLE)/.test(type)) {
    const numeric = Number(value)
    if (Number.isFinite(numeric)) return numeric
  }
  return value
}

function LocalFilterRow({ dataContextId, fields, filter, onChange, onRemove }: {
  dataContextId: string
  fields: DataContextField[]
  filter: ComponentFilterPolicy['localFilters'][number]
  onChange: (next: ComponentFilterPolicy['localFilters'][number]) => void
  onRemove: () => void
}) {
  const [values, setValues] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)
  const meta = fields.find(item => item.code === filter.field)
  const current = String(filter.value ?? '')

  useEffect(() => {
    if (!dataContextId || !filter.field) return
    const controller = new AbortController()
    queueMicrotask(() => {
      setLoading(true); setError(''); setValues([])
      void reportEditorAPI.listFilterOptions(dataContextId, filter.field, { signal: controller.signal }).then(result => {
        if (controller.signal.aborted) return
        setValues(result.values); setLoading(false)
        if (result.values.length > 0 && !result.values.includes(current)) {
          onChange({ ...filter, value: valueFromOption(result.values[0], meta) })
        }
      }).catch(caught => {
        if (controller.signal.aborted) return
        setLoading(false); setError(caught instanceof Error ? caught.message : '字段去重值读取失败')
      })
    })
    return () => controller.abort()
  }, [dataContextId, filter.field, reload]) // eslint-disable-line react-hooks/exhaustive-deps

  return <article className="report-metric-local-filter">
    <div className="report-metric-map-head"><strong>局部条件</strong><button className="report-metric-remove" type="button" aria-label="移除局部过滤项" onClick={onRemove}><Trash size={14} /></button></div>
    <div className="report-metric-filter-grid">
      <label>数据集字段
        <select value={filter.field} onChange={event => {
          const nextField = fields.find(item => item.code === event.target.value)
          onChange({ field: event.target.value, operator: filter.operator, value: '' })
          if (!nextField) setValues([])
        }}>
          {fields.map(field => <option key={field.code} value={field.code}>{fieldLabel(field)}</option>)}
        </select>
      </label>
      <label>条件
        <select value={filter.operator} onChange={event => onChange({ ...filter, operator: event.target.value as 'EQUALS' | 'NOT_EQUALS' })}>
          <option value="EQUALS">等于</option>
          <option value="NOT_EQUALS">不等于</option>
        </select>
      </label>
      <label>字段值
        <span className="report-metric-value-select">
          <select aria-label={`${fieldLabel(meta)}的字段值`} value={current} disabled={loading || values.length === 0}
            onChange={event => onChange({ ...filter, value: valueFromOption(event.target.value, meta) })}>
            {current && !values.includes(current) && <option value={current}>{current}</option>}
            {values.map(value => <option key={value} value={value}>{value}</option>)}
          </select>
          <button type="button" aria-label="刷新字段去重值" disabled={loading} onClick={() => setReload(value => value + 1)}>
            {loading ? <SpinnerGap className="spin" size={13} /> : <ArrowClockwise size={13} />}
          </button>
        </span>
      </label>
    </div>
    {error && <small className="report-metric-filter-error"><WarningCircle size={12} />{error}</small>}
    {!loading && !error && values.length === 0 && <small className="report-metric-filter-error"><WarningCircle size={12} />该字段当前没有可选值</small>}
  </article>
}

export function MetricStatusConfiguration({
  dataContextId, fields, fieldsOf, measures, globalFilters, filterPolicy,
  onMeasuresChange, onFilterPolicyChange,
}: Props) {
  const measureFields = fields.filter(field => field.role === 'MEASURE')
  const primary = measures.find(binding => binding.role === 'VALUE')
  const auxiliaries = measures.filter(binding => binding.role === 'TOOLTIP').slice(0, 2)
  const usedMetricFields = new Set(measures.map(binding => binding.field).filter(Boolean))
  const reportControls = globalFilters.filter(filter => filter.scope.type === 'REPORT')
  const usedControls = new Set(filterPolicy.globalMappings.map(mapping => mapping.filterId))

  const updateMetric = (role: 'VALUE' | 'TOOLTIP', position: number, fieldCode: string) => {
    const nextBinding = bindingForField(role, measureFields.find(field => field.code === fieldCode))
    if (role === 'VALUE') {
      onMeasuresChange([nextBinding, ...auxiliaries])
      return
    }
    const nextAuxiliaries = auxiliaries.map((binding, index) => index === position ? nextBinding : binding)
    onMeasuresChange([...(primary ? [primary] : []), ...nextAuxiliaries])
  }
  const renameMetric = (role: 'VALUE' | 'TOOLTIP', position: number, label: string) => {
    if (role === 'VALUE' && primary) onMeasuresChange([{ ...primary, label }, ...auxiliaries])
    if (role === 'TOOLTIP') onMeasuresChange([...(primary ? [primary] : []), ...auxiliaries.map((item, index) => index === position ? { ...item, label } : item)])
  }
  const changeAggregation = (role: 'VALUE' | 'TOOLTIP', position: number, aggregation: MetricAggregation) => {
    if (role === 'VALUE' && primary) onMeasuresChange([{ ...primary, aggregation }, ...auxiliaries])
    if (role === 'TOOLTIP') onMeasuresChange([...(primary ? [primary] : []), ...auxiliaries.map((item, index) => index === position ? { ...item, aggregation } : item)])
  }
  const addAuxiliary = () => {
    const available = measureFields.find(field => !usedMetricFields.has(field.code))
    onMeasuresChange([...(primary ? [primary] : []), ...auxiliaries, bindingForField('TOOLTIP', available)])
  }
  const removeAuxiliary = (position: number) =>
    onMeasuresChange([...(primary ? [primary] : []), ...auxiliaries.filter((_, index) => index !== position)])

  const metricRow = (binding: FieldBinding | undefined, role: 'VALUE' | 'TOOLTIP', position: number) => {
    const available = measureFields.filter(field => field.code === binding?.field || !usedMetricFields.has(field.code))
    return <article className={`report-metric-definition ${role === 'VALUE' ? 'is-primary' : ''}`} key={`${role}-${position}`}>
      <header><strong>{role === 'VALUE' ? '主指标' : `辅助指标 ${position + 1}`}</strong>
        {role === 'TOOLTIP' && <button type="button" aria-label={`移除辅助指标 ${position + 1}`} onClick={() => removeAuxiliary(position)}><Trash size={13} /></button>}
      </header>
      <label>指标名称
        <input value={binding?.label ?? ''} maxLength={200} placeholder="可手动填写，智能推荐会自动识别"
          onChange={event => renameMetric(role, position, event.target.value)} />
      </label>
      <label>指标字段
        <select value={binding?.field ?? ''} onChange={event => updateMetric(role, position, event.target.value)}>
          <option value="">请选择数据集字段</option>
          {available.map(field => <option key={field.code} value={field.code}>{fieldLabel(field)}</option>)}
        </select>
      </label>
      <label className="report-metric-formula">计算公式
        <select aria-label={`${role === 'VALUE' ? '主指标' : `辅助指标 ${position + 1}`}计算公式`}
          value={binding?.aggregation ?? ''} disabled={!binding?.field}
          onChange={event => changeAggregation(role, position, event.target.value as MetricAggregation)}>
          <option value="" disabled>请选择聚合方式</option>
          {formulaOptions.map(([value, label]) => <option key={value} value={value}>{label} · {value}({binding?.field || '字段'})</option>)}
        </select>
        <small>由查询后端在明细数据上执行聚合</small>
      </label>
    </article>
  }

  const controlLabel = (filter: GlobalFilter) => {
    const source = fieldsOf(filter.fieldRef.dataContextId).find(field => field.code === filter.fieldRef.field)
    return `${filter.label || source?.name || filter.fieldRef.field} · ${filter.fieldRef.field}`
  }
  const addGlobalMapping = () => {
    const control = reportControls.find(filter => !usedControls.has(filter.id))
    const field = fields[0]
    if (!control || !field) return
    onFilterPolicyChange({ ...filterPolicy, globalMappings: [...filterPolicy.globalMappings, { filterId: control.id, field: field.code }] })
  }
  const addLocalFilter = () => {
    const used = new Set(filterPolicy.localFilters.map(filter => filter.field))
    const field = fields.find(item => !used.has(item.code))
    if (!field) return
    onFilterPolicyChange({ ...filterPolicy, localFilters: [...filterPolicy.localFilters, { field: field.code, operator: 'EQUALS', value: '' }] })
  }
  const unusedLocalField = fields.some(field => !filterPolicy.localFilters.some(filter => filter.field === field.code))

  return <div className="report-metric-contract">
    <section className="report-metric-contract-section">
      <header><div><strong>指标区</strong><small>指标值只从当前数据集字段计算</small></div><span>1 主指标 · 最多 2 个辅助指标</span></header>
      {metricRow(primary, 'VALUE', 0)}
      {auxiliaries.map((binding, index) => metricRow(binding, 'TOOLTIP', index))}
      {auxiliaries.length < 2 && <button className="report-metric-add" type="button" disabled={!measureFields.some(field => !usedMetricFields.has(field.code))} onClick={addAuxiliary}>
        <Plus size={14} />添加辅助指标（可选）
      </button>}
    </section>

    <section className="report-metric-contract-section is-filters">
      <header><div><strong>过滤区</strong><small>全局映射与局部固定条件均可为空</small></div><Funnel size={16} /></header>
      <div className="report-metric-filter-subsection">
        <div className="report-metric-filter-title"><span><strong>全局</strong><small>筛选控件字段 → 当前数据集字段</small></span>
          <button type="button" disabled={!reportControls.some(filter => !usedControls.has(filter.id)) || fields.length === 0} onClick={addGlobalMapping}><Plus size={13} />添加映射</button></div>
        {filterPolicy.globalMappings.length === 0 && <p>未映射报告头筛选控件；这张卡片不会响应全局筛选。</p>}
        {filterPolicy.globalMappings.map((mapping, index) => {
          const availableControls = reportControls.filter(filter => filter.id === mapping.filterId || !usedControls.has(filter.id))
          return <article className="report-metric-global-map" key={`${mapping.filterId}-${index}`}>
            <div className="report-metric-map-head"><strong>筛选映射 {index + 1}</strong><button className="report-metric-remove" type="button" aria-label="移除全局筛选映射" onClick={() => onFilterPolicyChange({ ...filterPolicy, globalMappings: filterPolicy.globalMappings.filter((_, position) => position !== index) })}><Trash size={14} /></button></div>
            <label>筛选控件字段
              <select value={mapping.filterId} onChange={event => onFilterPolicyChange({ ...filterPolicy, globalMappings: filterPolicy.globalMappings.map((item, position) => position === index ? { ...item, filterId: event.target.value } : item) })}>
                {availableControls.map(filter => <option key={filter.id} value={filter.id}>{controlLabel(filter)}</option>)}
              </select>
            </label>
            <span className="report-metric-map-direction">映射到</span>
            <label>数据集中字段
              <select value={mapping.field} onChange={event => onFilterPolicyChange({ ...filterPolicy, globalMappings: filterPolicy.globalMappings.map((item, position) => position === index ? { ...item, field: event.target.value } : item) })}>
                {fields.map(field => <option key={field.code} value={field.code}>{fieldLabel(field)}</option>)}
              </select>
            </label>
          </article>
        })}
      </div>

      <div className="report-metric-filter-subsection">
        <div className="report-metric-filter-title"><span><strong>局部</strong><small>字段值取自当前数据集的去重结果</small></span>
          <button type="button" disabled={!unusedLocalField} onClick={addLocalFilter}><Plus size={13} />添加条件</button></div>
        {filterPolicy.localFilters.length === 0 && <p>未设置局部条件（可选）。</p>}
        {filterPolicy.localFilters.map((filter, index) => <LocalFilterRow key={`${filter.field}-${index}`}
          dataContextId={dataContextId} fields={fields} filter={filter}
          onChange={next => onFilterPolicyChange({ ...filterPolicy, localFilters: filterPolicy.localFilters.map((item, position) => position === index ? next : item) })}
          onRemove={() => onFilterPolicyChange({ ...filterPolicy, localFilters: filterPolicy.localFilters.filter((_, position) => position !== index) })} />)}
      </div>
    </section>
  </div>
}
