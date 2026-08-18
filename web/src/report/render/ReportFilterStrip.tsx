import { Check, Funnel, LockSimple, SlidersHorizontal } from '@phosphor-icons/react'
import { FilterControl } from './FilterControl.tsx'
import type { GlobalFilter } from './schema.ts'

/**
 * 报告固定筛选栏。
 *
 * 它属于报告框架而不是内容画布：作者可以配置字段，但不能拖拽、缩放或删除
 * 这块区域。编辑器、发布预览与运行页共用同一结构，避免筛选器再次退化成卡片。
 */
export function ReportFilterStrip({ filters, values = {}, onChange, onApply, applying = false, onConfigure, compact = false }: {
  filters: GlobalFilter[]
  values?: Record<string, unknown>
  onChange?: (filterId: string, value: unknown) => void
  onApply?: () => void
  applying?: boolean
  onConfigure?: () => void
  compact?: boolean
}) {
  return <section className={`report-fixed-filter-strip ${filters.length === 0 ? 'is-empty' : ''} ${compact ? 'is-compact' : ''}`.trim()} aria-label="报告筛选">
    <header>
      <span className="report-fixed-filter-icon"><Funnel size={16} weight="duotone" /></span>
      <div><strong>筛选条件</strong><small><LockSimple size={11} />固定区域</small></div>
    </header>
    {filters.length > 0
      ? <fieldset className="report-fixed-filter-fields" disabled={!onChange}>
        {filters.map(filter => <FilterControl key={filter.id} filter={filter} value={values[filter.id]}
          onChange={value => onChange?.(filter.id, value)} />)}
      </fieldset>
      : <p className="report-fixed-filter-empty">尚未配置筛选条件</p>}
    <div className="report-fixed-filter-actions">
      {onConfigure && <button className="quiet-button" type="button" onClick={onConfigure}><SlidersHorizontal size={14} />配置</button>}
      {onApply && filters.length > 0 && <button className="primary-button" type="button" disabled={applying} onClick={onApply}>
        <Check size={14} />{applying ? '应用中…' : '应用筛选'}
      </button>}
    </div>
  </section>
}
