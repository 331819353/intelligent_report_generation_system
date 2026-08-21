import { useEffect, useState } from 'react'
import { reportEditorAPI } from '../api/editor.ts'
import type { GlobalFilter } from './schema.ts'

/**
 * 报告级筛选的输入控件。
 *
 * 之前筛选栏对区间与相对时间类型要求用户手写 JSON（`JSON.parse(raw)`），
 * 那实际上把这些筛选类型变成了不可用状态。控件按筛选类型给出对应的输入方式，
 * 并直接产出服务端 ParseFilterValue 接受的取值结构。
 */

const relativeUnits: Array<{ value: string; label: string }> = [
  { value: 'DAY', label: '天' },
  { value: 'WEEK', label: '周' },
  { value: 'MONTH', label: '月' },
  { value: 'QUARTER', label: '季度' },
  { value: 'YEAR', label: '年' },
]

type DateRange = { start?: string; endExclusive?: string }
type NumberRange = { minimum?: number; maximum?: number }
type Relative = { unit?: string; offset?: number }

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

export function FilterControl({ filter, value, onChange }: {
  filter: GlobalFilter
  value: unknown
  onChange: (next: unknown) => void
}) {
  const label = filter.label?.trim() || filter.fieldRef.field
  const defaults = filter.defaultValue
  const configuredOptions = filter.options ?? []
  const optionKey = `${filter.fieldRef.dataContextId}\u0000${filter.fieldRef.field}\u0000${filter.type}`
  const selectable = filter.type === 'SINGLE_SELECT' || filter.type === 'SELECT' || filter.type === 'MULTI_SELECT' || filter.type === 'SEARCH_SELECT'
  const [discovered, setDiscovered] = useState<{ key: string; values: string[]; loading: boolean; error: string }>({ key: '', values: [], loading: false, error: '' })

  useEffect(() => {
    if (!selectable || configuredOptions.length > 0 || !filter.fieldRef.dataContextId || !filter.fieldRef.field) return
    const controller = new AbortController()
    queueMicrotask(() => {
      if (controller.signal.aborted) return
      setDiscovered({ key: optionKey, values: [], loading: true, error: '' })
      void reportEditorAPI.listFilterOptions(filter.fieldRef.dataContextId, filter.fieldRef.field, { signal: controller.signal }).then(result => {
        if (!controller.signal.aborted) setDiscovered({ key: optionKey, values: result.values, loading: false, error: '' })
      }).catch(cause => {
        if (!controller.signal.aborted) setDiscovered({ key: optionKey, values: [], loading: false, error: cause instanceof Error ? cause.message : '候选项读取失败' })
      })
    })
    return () => controller.abort()
  }, [configuredOptions.length, filter.fieldRef.dataContextId, filter.fieldRef.field, optionKey, selectable])

  const options = configuredOptions.length > 0 ? configuredOptions : discovered.key === optionKey ? discovered.values : []
  const optionsLoading = configuredOptions.length === 0 && discovered.key === optionKey && discovered.loading
  const optionsError = configuredOptions.length === 0 && discovered.key === optionKey ? discovered.error : ''

  switch (filter.type) {
    case 'DATE':
      return <label className="report-filter-control">
        <span>{label}</span>
        <input type="date" value={typeof value === 'string' ? value : ''}
          onChange={event => onChange(event.target.value || undefined)} />
      </label>

    case 'DATE_RANGE': {
      const range = asRecord(value) as DateRange
      const update = (patch: DateRange) => {
        const next = { ...range, ...patch }
        onChange(next.start && next.endExclusive ? next : undefined)
      }
      return <div className="report-filter-control is-range">
        <span>{label}</span>
        <div>
          <input type="date" aria-label={`${label}开始`} value={range.start ?? ''}
            onChange={event => update({ start: event.target.value || undefined })} />
          <i>至</i>
          <input type="date" aria-label={`${label}结束（不含）`} value={range.endExclusive ?? ''}
            onChange={event => update({ endExclusive: event.target.value || undefined })} />
        </div>
      </div>
    }

    case 'NUMBER_RANGE': {
      const range = asRecord(value) as NumberRange
      const update = (patch: NumberRange) => {
        const next = { ...range, ...patch }
        onChange(next.minimum === undefined && next.maximum === undefined ? undefined : next)
      }
      const read = (raw: string) => raw === '' ? undefined : Number(raw)
      return <div className="report-filter-control is-range">
        <span>{label}</span>
        <div>
          <input type="number" aria-label={`${label}下限`} value={range.minimum ?? ''}
            onChange={event => update({ minimum: read(event.target.value) })} />
          <i>～</i>
          <input type="number" aria-label={`${label}上限`} value={range.maximum ?? ''}
            onChange={event => update({ maximum: read(event.target.value) })} />
        </div>
      </div>
    }

    case 'RELATIVE_TIME': {
      const relative = asRecord(value) as Relative
      const update = (patch: Relative) => {
        const next = { unit: relative.unit ?? defaults?.unit ?? 'MONTH', offset: relative.offset ?? 0, ...patch }
        onChange(next)
      }
      return <div className="report-filter-control is-relative">
        <span>{label}</span>
        <div>
          <span>最近</span>
          <input type="number" min={0} aria-label={`${label}偏移`} value={relative.offset ?? 0}
            onChange={event => update({ offset: Number(event.target.value) || 0 })} />
          <select aria-label={`${label}单位`} value={relative.unit ?? defaults?.unit ?? 'MONTH'}
            onChange={event => update({ unit: event.target.value })}>
            {relativeUnits.map(unit => <option key={unit.value} value={unit.value}>{unit.label}</option>)}
          </select>
        </div>
      </div>
    }

    case 'BOOLEAN':
      return <label className="report-filter-control is-boolean">
        <span>{label}</span>
        <input type="checkbox" checked={value === true} onChange={event => onChange(event.target.checked)} />
      </label>

    case 'MULTI_SELECT': {
      const selected = Array.isArray(value) ? value as string[] : []
      if (options.length === 0) {
        return <div className="report-filter-control is-options-empty">
          <span>{label}</span><button type="button" disabled>{optionsLoading ? '正在读取候选项…' : optionsError ? '候选项读取失败' : '暂无可选项'}</button>
        </div>
      }
      return <div className="report-filter-control is-multi">
        <span>{label}</span>
        <details className="report-filter-multi-select">
          <summary>{selected.length > 0 ? `已选 ${selected.length} 项` : '全部'}</summary>
          <div>{options.map(option => <label key={option}>
            <input type="checkbox" checked={selected.includes(option)}
              onChange={event => {
                const next = event.target.checked
                  ? [...selected, option]
                  : selected.filter(item => item !== option)
                onChange(next.length ? next : undefined)
              }} />
            {option}
          </label>)}</div>
        </details>
      </div>
    }

    case 'SEARCH_SELECT': {
      const listId = `report-filter-options-${filter.id}`
      return <label className="report-filter-control">
        <span>{label}</span>
        <input list={options.length > 0 ? listId : undefined} value={typeof value === 'string' ? value : ''}
          placeholder="全部" onChange={event => onChange(event.target.value || undefined)} />
        {options.length > 0 && <datalist id={listId}>{options.map(option => <option key={option} value={option} />)}</datalist>}
      </label>
    }

    case 'SINGLE_SELECT':
    case 'SELECT': {
      return <label className="report-filter-control">
        <span>{label}</span>
        <select value={typeof value === 'string' ? value : ''} disabled={optionsLoading || options.length === 0}
          onChange={event => onChange(event.target.value || undefined)}>
          <option value="">{optionsLoading ? '正在读取候选项…' : optionsError ? '候选项读取失败' : options.length === 0 ? '暂无可选项' : '全部'}</option>
          {typeof value === 'string' && value && !options.includes(value) && <option value={value}>{value}</option>}
          {options.map(option => <option key={option} value={option}>{option}</option>)}
        </select>
      </label>
    }
  }

  // 只有参数输入允许自由填写；选择型筛选必须来自数据集字段的去重值。
  return <label className="report-filter-control">
    <span>{label}</span>
    <input value={value === undefined || value === null ? '' : String(value)}
      placeholder={defaults?.values?.join('、') || '使用发布默认值'}
      onChange={event => {
        const raw = event.target.value
        if (raw === '') { onChange(undefined); return }
        if (filter.type === 'PARAMETER_INPUT' && Number.isFinite(Number(raw))) { onChange(Number(raw)); return }
        onChange(raw)
      }} />
  </label>
}
