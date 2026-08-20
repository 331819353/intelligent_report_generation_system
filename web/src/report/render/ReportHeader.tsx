import {
  ArrowClockwise, CaretDown, ChartBar, Check, CheckCircle, DownloadSimple, Funnel,
  LockSimple, SlidersHorizontal,
} from '@phosphor-icons/react'
import { useState, type ReactNode } from 'react'
import { FilterControl } from './FilterControl.tsx'
import type { GlobalFilter, ReportHeaderStyle } from './schema.ts'

const reportHeaderStyles: Array<{ id: ReportHeaderStyle; name: string; description: string }> = [
  { id: '01', name: '清透分层', description: '标题与筛选分层，适合常规经营报告' },
  { id: '02', name: '沉浸蓝', description: '蓝色标题主视觉，适合管理层汇报' },
  { id: '03', name: '专业展开', description: '高级筛选默认展开，适合多维分析' },
]

export function ReportHeaderChooser({ value, onChange, compact = false }: {
  value?: ReportHeaderStyle
  onChange: (value: ReportHeaderStyle) => void
  compact?: boolean
}) {
  return <section className={`report-header-chooser ${compact ? 'is-compact' : ''}`} aria-label="选择报告头样式">
    <header><div><strong>选择报告头</strong><small>创建后将固定携带筛选能力和动效</small></div><span>3 种样式</span></header>
    <div>{reportHeaderStyles.map(style => <button type="button" key={style.id} aria-pressed={value === style.id}
      className={value === style.id ? 'is-selected' : ''} onClick={() => onChange(style.id)}>
      <span className="report-header-choice-preview"><img src={`/report-header-gallery/${style.id}.png`} alt={`${style.name}报告头预览`} /></span>
      <span className="report-header-choice-copy"><b>{style.id}</b><span><strong>{style.name}</strong><small>{style.description}</small></span>{value === style.id && <CheckCircle size={18} weight="fill" />}</span>
    </button>)}</div>
  </section>
}

const previewFilters: GlobalFilter[] = [
  { id: 'preview-period', type: 'DATE_RANGE', fieldRef: { dataContextId: 'preview', field: '报告周期' }, scope: { type: 'REPORT', targetIds: [] } },
  { id: 'preview-region', type: 'SINGLE_SELECT', fieldRef: { dataContextId: 'preview', field: '区域范围' }, scope: { type: 'REPORT', targetIds: [] }, defaultValue: { values: ['全国', '华东', '华南'] } },
  { id: 'preview-product', type: 'SINGLE_SELECT', fieldRef: { dataContextId: 'preview', field: '产品线' }, scope: { type: 'REPORT', targetIds: [] }, defaultValue: { values: ['全产品线', '智慧家居', '影音产品'] } },
  { id: 'preview-channel', type: 'SINGLE_SELECT', fieldRef: { dataContextId: 'preview', field: '销售渠道' }, scope: { type: 'REPORT', targetIds: [] }, defaultValue: { values: ['全渠道', '线上', '线下'] } },
]

const previewAdvancedFilters: GlobalFilter[] = [
  { id: 'preview-business', type: 'SINGLE_SELECT', fieldRef: { dataContextId: 'preview', field: '业务类型' }, scope: { type: 'REPORT', targetIds: [] }, defaultValue: { values: ['视听产业', '智慧家居'] } },
  { id: 'preview-customer', type: 'SINGLE_SELECT', fieldRef: { dataContextId: 'preview', field: '客户类型' }, scope: { type: 'REPORT', targetIds: [] }, defaultValue: { values: ['企业客户', '个人客户'] } },
  { id: 'preview-level', type: 'SINGLE_SELECT', fieldRef: { dataContextId: 'preview', field: '区域层级' }, scope: { type: 'REPORT', targetIds: [] }, defaultValue: { values: ['省级', '地市级'] } },
  { id: 'preview-caliber', type: 'SINGLE_SELECT', fieldRef: { dataContextId: 'preview', field: '数据口径' }, scope: { type: 'REPORT', targetIds: [] }, defaultValue: { values: ['含税收入', '不含税收入'] } },
]

function filterValueText(filter: GlobalFilter, value: unknown, index: number) {
  if (typeof value === 'string' && value) return value
  if (Array.isArray(value) && value.length) return value.join('、')
  if (value && typeof value === 'object') {
    const item = value as Record<string, unknown>
    if (item.start || item.endExclusive) return [item.start, item.endExclusive].filter(Boolean).join(' 至 ')
  }
  return filter.defaultValue?.values?.[0] || ['2026年上半年', '全国', '全产品线', '全渠道'][index] || '全部'
}

export function ReportHeader({
  style = '01', title, description, meta, filters, values = {}, onChange, onApply, applying = false,
  onConfigure, onExport, compact = false, locked = false, actions,
}: {
  style?: ReportHeaderStyle
  title: string
  description?: string
  meta?: string[]
  filters: GlobalFilter[]
  values?: Record<string, unknown>
  onChange?: (filterId: string, value: unknown) => void
  onApply?: () => void
  applying?: boolean
  onConfigure?: () => void
  onExport?: () => void
  compact?: boolean
  locked?: boolean
  actions?: ReactNode
}) {
  const [advancedByStyle, setAdvancedByStyle] = useState<Partial<Record<ReportHeaderStyle, boolean>>>({})
  const [applied, setApplied] = useState(false)
  const readonlyPreview = filters.length === 0
  const primaryFilters = readonlyPreview ? previewFilters : filters.slice(0, 4)
  const advancedFilters = readonlyPreview ? previewAdvancedFilters : filters.slice(4, 8)
  const hasAdvancedFilters = advancedFilters.length > 0
  const advanced = hasAdvancedFilters && (advancedByStyle[style] ?? style === '03')
  const allVisibleFilters = [...primaryFilters, ...advancedFilters]
  const chips = allVisibleFilters.map((filter, index) => ({
    id: filter.id, label: filter.fieldRef.field, value: filterValueText(filter, values[filter.id], index),
  }))
  const change = (filterId: string, value: unknown) => onChange?.(filterId, value)
  const apply = () => {
    setApplied(true)
    window.setTimeout(() => setApplied(false), 950)
    onApply?.()
  }
  const metadata = meta?.length ? meta : ['视听产业', '全国经营范围', '数据更新于今天']

  return <section className={`report-designed-header is-style-${style} ${compact ? 'is-compact' : ''} ${advanced ? 'is-advanced' : ''} ${applied ? 'is-applied' : ''}`.trim()} aria-label="报告头">
    <div className="report-designed-title">
      <div className="report-designed-title-icon"><ChartBar size={23} weight="fill" /></div>
      <div className="report-designed-title-copy"><div><h1>{title}</h1>{locked && <span><LockSimple size={11} />报告头</span>}</div><p>{description || '多维度洞察业务经营情况，驱动增长决策'}</p><footer>{metadata.map(item => <span key={item}>{item}</span>)}</footer></div>
      <div className="report-designed-title-actions">{actions}<button type="button" disabled={!onExport} onClick={onExport}><DownloadSimple size={15} />导出报告</button></div>
    </div>

    <div className="report-designed-filter-card">
      <header><div><span><Funnel size={16} weight="duotone" /></span><p><strong>筛选条件</strong><small>调整报告分析范围</small></p></div>
        {hasAdvancedFilters && <button type="button" className="report-designed-advanced-toggle" aria-expanded={advanced} onClick={() => setAdvancedByStyle(current => ({ ...current, [style]: !advanced }))}><SlidersHorizontal size={14} />高级筛选<CaretDown size={12} /></button>}
      </header>
      <fieldset className="report-designed-filter-fields" disabled={!onChange || readonlyPreview}>
        {primaryFilters.map(filter => <FilterControl key={filter.id} filter={filter} value={values[filter.id]} onChange={value => change(filter.id, value)} />)}
      </fieldset>
      <div className="report-designed-filter-summary"><span>已选条件</span><div>{chips.map(chip => <b key={chip.id}>{chip.label}：{chip.value}</b>)}</div>
        <section>{onConfigure && <button type="button" className="report-designed-configure" onClick={onConfigure}><SlidersHorizontal size={13} />配置</button>}
          {onChange && filters.length > 0 && <button type="button" onClick={() => filters.forEach(filter => change(filter.id, undefined))}><ArrowClockwise size={13} />重置</button>}
          {onApply && filters.length > 0 && <button type="button" className="is-primary" disabled={applying} onClick={apply}>{applied ? <CheckCircle size={14} weight="fill" /> : <Check size={14} />}{applying ? '应用中…' : applied ? '已应用' : '应用筛选'}</button>}
        </section>
      </div>
      {hasAdvancedFilters && <div className="report-designed-advanced-panel" aria-hidden={!advanced}>
        <header><div><strong>高级筛选</strong><small>组合多个条件缩小分析范围</small></div><span>{advancedFilters.length} 个可用字段</span></header>
        <fieldset disabled={!onChange || readonlyPreview}>{advancedFilters.map(filter => <FilterControl key={`advanced-${filter.id}`} filter={filter} value={values[filter.id]} onChange={value => change(filter.id, value)} />)}</fieldset>
      </div>}
    </div>
  </section>
}
