/* eslint-disable react-refresh/only-export-components -- 组件注册表必须与默认渲染器保持原子对应。 */
import { useState, type CSSProperties, type ReactNode } from 'react'
import type { ReportComponent, ReportComponentInteractionEvent, ReportParameter, ReportRendererMode, ReportRuntimeContext } from '../../lib/report-contract'
import { formalReportComponentTypes, reportComponentCatalog, type FormalReportComponentType } from '../../lib/report-component-catalog'

export type ReportComponentRendererProps = {
  component: ReportComponent
  runtime: ReportRuntimeContext
  mode: ReportRendererMode
  parameter?: ReportParameter
  onInteraction?: (event: ReportComponentInteractionEvent) => void
  interactionBusy?: boolean
  drillLevel?: number
}

export type ReportComponentRenderer = (props: ReportComponentRendererProps) => ReactNode
export type ReportComponentRegistry = Record<string, ReportComponentRenderer>

const formalRenderers: Record<FormalReportComponentType, ReportComponentRenderer> = {
  TITLE: TitleRenderer,
  RICH_TEXT: RichTextRenderer,
  FILTER: FilterRenderer,
  KPI: KpiRenderer,
  ADDITIONAL_INFO: AdditionalInfoRenderer,
  TABLE: TableRenderer,
  CHART: ChartRenderer,
  IMAGE: ImageRenderer,
  ATTACHMENT_LIST: AttachmentListRenderer,
  DATA_SOURCE: DataSourceRenderer,
  UPDATED_AT: UpdatedAtRenderer,
  CONCLUSION: ConclusionRenderer,
}

/** 注册表键由正式组件目录校验，新增目录项却遗漏渲染器时会在类型检查阶段失败。 */
export const defaultComponentRegistry: ReportComponentRegistry = Object.fromEntries(
  formalReportComponentTypes.map(type => [type, formalRenderers[type]]),
)

export function UnknownComponentRenderer({ component }: ReportComponentRendererProps) {
  return (
    <div className="report-component-state report-component-state--unknown" role="status">
      <strong>暂不支持的组件</strong>
      <span>{component.name}（{String(component.type)}）已安全降级。</span>
    </div>
  )
}

function TitleRenderer({ component }: ReportComponentRendererProps) {
  return (
    <div className="report-title-component">
      <span>经营分析中心</span>
      <h2>{readString(component.binding, 'text') ?? component.name}</h2>
    </div>
  )
}

function RichTextRenderer({ component }: ReportComponentRendererProps) {
  const text = readString(component.binding, 'text')
  const blocks = Array.isArray(component.binding?.blocks) ? component.binding.blocks.map(readRecord).filter(Boolean) as Record<string, unknown>[] : []
  return <article className="report-rich-text-component"><h3>{component.name}</h3>{blocks.length > 0 ? blocks.map(renderRichTextBlock) : text ? text.split('\n').map((line, index) => <p key={`${index}-${line}`}>{line}</p>) : <EmptyState label="暂无正文内容" />}</article>
}

function FilterRenderer(props: ReportComponentRendererProps) {
  const { parameter, runtime } = props
  if (!parameter) return <EmptyState label="尚未绑定报告参数" />
  const value = runtime.parameters[parameter.code]
  return <FilterControl key={`${parameter.id}:${JSON.stringify(value)}`} {...props} parameter={parameter} value={value} />
}

function FilterControl({ component, runtime, mode, parameter, onInteraction, interactionBusy, value }: Omit<ReportComponentRendererProps, 'parameter'> & { parameter: ReportParameter; value: unknown }) {
  const placeholder = readString(component.binding, 'placeholder') ?? '请选择'
  const optionState = runtime.parameterOptions?.[parameter.code]
  const configuredOptions = readOptions(component.binding?.options)
  const options = optionState?.status === 'READY' ? optionState.options ?? [] : configuredOptions
  const [draft, setDraft] = useState<unknown>(value ?? (parameter.multiValue ? [] : ''))
  const autoSubmit = component.interaction?.autoSubmit !== false

  function submit(nextValue: unknown) {
    setDraft(nextValue)
    if (autoSubmit) onInteraction?.({ value: nextValue })
  }

  const disabled = mode === 'designer' || interactionBusy || optionState?.status === 'LOADING'
  const control = readString(component.binding, 'control') ?? (options.length > 0 ? parameter.multiValue ? 'MULTI_SELECT' : 'SELECT' : parameter.dataType)
  return (
    <div className="report-filter-component" aria-busy={interactionBusy || optionState?.status === 'LOADING'}>
      <span>{component.name}</span>
      {control === 'MULTI_SELECT' ? (
        <select multiple value={arrayValue(draft).map(encodeOptionValue)} disabled={disabled} onChange={event => submit([...event.currentTarget.selectedOptions].map(option => decodeOptionValue(option.value, options)))} aria-label={component.name}>
          {options.map(option => <option key={encodeOptionValue(option.value)} value={encodeOptionValue(option.value)}>{option.label}</option>)}
        </select>
      ) : options.length > 0 || control === 'SELECT' ? (
        <select value={draft === undefined ? '' : encodeOptionValue(draft)} disabled={disabled} onChange={event => submit(event.currentTarget.value ? decodeOptionValue(event.currentTarget.value, options) : '')} aria-label={component.name}>
          <option value="">{placeholder}</option>
          {options.map(option => <option key={encodeOptionValue(option.value)} value={encodeOptionValue(option.value)}>{option.label}</option>)}
        </select>
      ) : (
        <input type={inputType(parameter.dataType)} value={draft === undefined ? '' : String(draft)} disabled={disabled} placeholder={placeholder} onChange={event => submit(event.currentTarget.value)} aria-label={component.name} />
      )}
      {!autoSubmit && mode === 'viewer' && <button type="button" disabled={disabled || Object.is(draft, value)} onClick={() => onInteraction?.({ value: draft })}>应用</button>}
      {optionState?.status === 'LOADING' && <small role="status">正在加载选项…</small>}
      {optionState?.status === 'ERROR' && <small role="alert">{optionState.errorMessage ?? '筛选选项加载失败'}</small>}
    </div>
  )
}

function KpiRenderer({ component, runtime }: ReportComponentRendererProps) {
  const data = useRuntimeData(component, runtime)
  if (isStateNode(data)) return data.node
  const value = data.record?.value
  const trend = readNumber(data.record, 'trend')
  return (
    <div className="report-kpi-component">
      <span>{component.name}</span>
      <strong>{formatValue(value)}</strong>
      {trend !== undefined && <small className={trend >= 0 ? 'is-positive' : 'is-negative'}>{trend >= 0 ? '↑' : '↓'} {Math.abs(trend)}%</small>}
    </div>
  )
}

function AdditionalInfoRenderer({ component }: ReportComponentRendererProps) {
  return <aside className="report-info-component"><span>补充说明</span><h3>{readString(component.binding, 'title') ?? component.name}</h3><p>{readString(component.binding, 'text') ?? '暂无附加信息'}</p></aside>
}

function TableRenderer({ component, runtime }: ReportComponentRendererProps) {
  const data = useRuntimeData(component, runtime)
  if (isStateNode(data)) return data.node
  const rows = Array.isArray(data.record?.rows) ? data.record.rows.map(readRecord).filter(Boolean) as Record<string, unknown>[] : []
  const configuredColumns = Array.isArray(component.binding?.columns) ? component.binding.columns.map(readRecord).filter(Boolean) as Record<string, unknown>[] : []
  const columns = configuredColumns.length > 0
    ? configuredColumns.map(item => ({ key: readString(item, 'key') ?? '', label: readString(item, 'label') ?? readString(item, 'key') ?? '' })).filter(item => item.key)
    : Object.keys(rows[0] ?? {}).map(key => ({ key, label: key }))
  if (rows.length === 0 || columns.length === 0) return <EmptyState label="暂无表格数据" />
  return (
    <div className="report-table-component">
      <h3>{component.name}</h3>
      <div><table><thead><tr>{columns.map(column => <th key={column.key} scope="col">{column.label}</th>)}</tr></thead><tbody>{rows.slice(0, 20).map((row, rowIndex) => <tr key={rowIndex}>{columns.map(column => <td key={column.key}>{formatValue(row[column.key])}</td>)}</tr>)}</tbody></table></div>
    </div>
  )
}

function ChartRenderer({ component, runtime, mode, parameter, onInteraction, interactionBusy, drillLevel = -1 }: ReportComponentRendererProps) {
  const data = useRuntimeData(component, runtime)
  if (isStateNode(data)) return data.node
  const points = readChartPoints(data.record?.points)
  const values = points.length > 0 ? points.map(point => point.value) : readNumberArray(data.record?.values)
  const labels = points.length > 0 ? points.map(point => point.label) : readStringArray(data.record?.labels)
  const chart = readRecord(component.binding?.chart)
  const chartType = readString(chart, 'type') ?? 'COLUMN'
  return (
    <div className="report-chart-component">
      <header><div><span>数据图表</span><h3>{component.name}</h3></div><small>{data.updatedAt ? '数据已更新' : '等待数据'}</small></header>
      <ChartGraphic type={chartType} values={values} labels={labels} name={component.name} />
      {mode === 'viewer' && <ChartInteractionControls component={component} parameter={parameter} runtime={runtime} points={points} busy={interactionBusy === true} drillLevel={drillLevel} onInteraction={onInteraction} />}
    </div>
  )
}

function ChartInteractionControls({ component, parameter, runtime, points, busy, drillLevel, onInteraction }: { component: ReportComponent; parameter?: ReportParameter; runtime: ReportRuntimeContext; points: ChartPoint[]; busy: boolean; drillLevel: number; onInteraction?: (event: ReportComponentInteractionEvent) => void }) {
  const semanticFieldCode = parameter?.semanticBinding?.semanticFieldCode
  const currentValue = parameter ? runtime.parameters[parameter.code] : undefined
  const selectable = component.interaction?.clickFilter === true && semanticFieldCode && onInteraction
  const selectablePoints = selectable ? points.filter(point => point.semanticValues[semanticFieldCode] !== undefined) : []
  const drill = readRecord(component.interaction?.drill)
  const levels = Array.isArray(drill?.levels) ? drill.levels.map(readRecord).filter(Boolean) as Record<string, unknown>[] : []
  const currentDrill = drillLevel >= 0 ? levels[drillLevel] : undefined
  const nextDrill = levels[drillLevel + 1]
  if (selectablePoints.length === 0 && levels.length === 0) return null
  return (
    <div className="report-chart-interactions" data-report-interactive="true">
      {selectablePoints.length > 0 && <div aria-label={`${component.name}联动数据点`}>{selectablePoints.map(point => {
        const value = point.semanticValues[semanticFieldCode!]
        return <button key={point.id} type="button" disabled={busy} aria-pressed={sameRuntimeValue(value, currentValue)} onClick={() => onInteraction?.({ value, label: point.label })}>{point.label}</button>
      })}</div>}
      {levels.length > 0 && <nav aria-label={`${component.name}下钻层级`}>
        <span>{currentDrill ? `当前：${readString(currentDrill, 'label')}` : '当前：汇总'}</span>
        {drillLevel >= 0 && <button type="button" disabled={busy} onClick={() => onInteraction?.({ value: undefined, drillLevel: drillLevel - 1, drillDirection: 'UP' })}>返回上级</button>}
        {nextDrill && <button type="button" disabled={busy} onClick={() => onInteraction?.({ value: undefined, drillLevel: drillLevel + 1, drillDirection: 'DOWN' })}>下钻至{readString(nextDrill, 'label')}</button>}
      </nav>}
    </div>
  )
}

function ChartGraphic({ type, values, labels, name }: { type: string; values: number[]; labels: string[]; name: string }) {
  if (values.length === 0) return <EmptyState label="暂无可展示的图表数据" />
  const maximum = Math.max(...values.map(Math.abs), 1)
  if (type === 'LINE') {
    const points = buildLinePoints(values)
    return <div className="report-line-chart"><svg viewBox="0 0 600 180" role="img" aria-label={`${name}趋势图`}><polyline points={points} fill="none" stroke="currentColor" strokeWidth="5" strokeLinejoin="round" strokeLinecap="round" /></svg><div>{labels.map((label, index) => <span key={`${index}-${label}`}>{label}</span>)}</div></div>
  }
  if (type === 'PIE') {
    const total = values.reduce((sum, value) => sum + Math.max(0, value), 0)
    if (total <= 0) return <EmptyState label="饼图数据之和必须大于零" />
    const colors = ['#2f6af7', '#16a085', '#f59e0b', '#8b5cf6', '#ef4444', '#64748b']
    const stops = values.map((value, index) => {
      const start = values.slice(0, index).reduce((sum, item) => sum + Math.max(0, item), 0) / total * 100
      const end = start + Math.max(0, value) / total * 100
      return `${colors[index % colors.length]} ${start.toFixed(2)}% ${end.toFixed(2)}%`
    })
    return <div className="report-pie-chart"><div role="img" aria-label={`${name}饼图`} style={{ background: `conic-gradient(${stops.join(',')})` }} /><ul>{values.map((value, index) => <li key={`${index}-${labels[index] ?? index}`}><i style={{ background: colors[index % colors.length] }} />{labels[index] ?? `项目 ${index + 1}`}：{value}</li>)}</ul></div>
  }
  const horizontal = type === 'BAR'
  return <div className={`report-bar-chart${horizontal ? ' report-bar-chart--horizontal' : ''}`} role="img" aria-label={`${name}${horizontal ? '条形图' : '柱状图'}`}>{values.map((value, index) => <div key={`${index}-${labels[index] ?? index}`}><span>{labels[index] ?? `项目 ${index + 1}`}</span><i style={{ '--chart-ratio': Math.abs(value) / maximum } as CSSProperties} /><strong>{value}</strong></div>)}</div>
}

function ImageRenderer({ component, runtime }: ReportComponentRendererProps) {
  const state = runtime.componentData[component.id]
  if (state?.status === 'ERROR') throw new Error(state.errorMessage ?? '图片加载失败')
  if (state?.status === 'LOADING') return <LoadingState label={component.name} />
  const data = readRecord(state?.data)
  const url = safeResourceURL(readString(data, 'url') ?? readString(component.binding, 'url'), 'IMAGE')
  const alt = readString(data, 'alt') ?? readString(component.binding, 'alt') ?? component.name
  return url ? <figure className="report-image-component"><img src={url} alt={alt} /><figcaption>{component.name}</figcaption></figure> : <EmptyState label="未配置可用的图片地址" />
}

function AttachmentListRenderer({ component, runtime }: ReportComponentRendererProps) {
  const state = runtime.componentData[component.id]
  if (state?.status === 'ERROR') throw new Error(state.errorMessage ?? '附件加载失败')
  if (state?.status === 'LOADING') return <LoadingState label={component.name} />
  const data = readRecord(state?.data)
  const raw = Array.isArray(data?.attachments) ? data.attachments : Array.isArray(component.binding?.attachments) ? component.binding.attachments : []
  const attachments = raw.map(readRecord).filter(Boolean) as Record<string, unknown>[]
  return <div className="report-attachment-component"><h3>{component.name}</h3>{attachments.length > 0 ? <ul>{attachments.map((item, index) => { const url = safeResourceURL(readString(item, 'url'), 'ATTACHMENT'); const name = readString(item, 'name') ?? `附件 ${index + 1}`; return <li key={`${index}-${name}`}>{url ? <a href={url}>{name}</a> : <span>{name}</span>}<small>{readString(item, 'sizeLabel') ?? ''}</small></li> })}</ul> : <EmptyState label="暂无附件" />}</div>
}

function DataSourceRenderer({ component, runtime }: ReportComponentRendererProps) {
  const data = useRuntimeData(component, runtime, false)
  if (isStateNode(data)) return data.node
  const sources = readStringArray(data.record?.sources)
  return <div className="report-source-component"><strong>{component.name}</strong><span>{sources.length > 0 ? sources.join('、') : '来源将在报告运行后展示'}</span><small>{readString(data.record, 'updatedAtLabel') ?? ''}</small></div>
}

function UpdatedAtRenderer({ component, runtime }: ReportComponentRendererProps) {
  const state = runtime.componentData[component.id]
  if (state?.status === 'ERROR') throw new Error(state.errorMessage ?? '更新时间读取失败')
  if (state?.status === 'LOADING') return <LoadingState label={component.name} />
  const data = readRecord(state?.data)
  const latest = readString(data, 'updatedAt') ?? state?.updatedAt ?? latestRuntimeUpdate(runtime)
  return <div className="report-updated-component"><span>{readString(component.binding, 'label') ?? component.name}</span><strong>{latest ? formatDateTime(latest) : '等待报告运行'}</strong></div>
}

function ConclusionRenderer({ component, runtime }: ReportComponentRendererProps) {
  const data = useRuntimeData(component, runtime)
  if (isStateNode(data)) return data.node
  const summary = readString(data.record, 'summary')
  return <div className="report-conclusion-component"><span>核心结论</span><h3>{component.name}</h3><p>{summary ?? '暂无足够数据生成结论。'}</p><small>{data.updatedAt ? '结论已关联运行数据' : '等待报告运行'}</small></div>
}

type RuntimeData = { record?: Record<string, unknown>; updatedAt?: string }
type StateNode = { node: ReactNode }
type ChartPoint = { id: string; label: string; value: number; semanticValues: Record<string, unknown> }

/** 数据型组件统一处理加载和错误状态，避免每个渲染器产生不一致的降级语义。 */
function useRuntimeData(component: ReportComponent, runtime: ReportRuntimeContext, requireData = true): RuntimeData | StateNode {
  const state = runtime.componentData[component.id]
  if (state?.status === 'ERROR') throw new Error(state.errorMessage ?? `${reportComponentCatalog[component.type as FormalReportComponentType]?.label ?? component.name}运行失败`)
  if (state?.status === 'LOADING') return { node: <LoadingState label={component.name} /> }
  if (requireData && !state?.data) return { node: <EmptyState label={`${component.name}暂无运行数据`} /> }
  return { record: readRecord(state?.data), updatedAt: state?.updatedAt }
}

function isStateNode(value: RuntimeData | StateNode): value is StateNode {
  return 'node' in value
}

function LoadingState({ label }: { label: string }) {
  return <div className="report-component-state" role="status"><strong>{label}</strong><span>正在加载组件数据…</span></div>
}

function EmptyState({ label }: { label: string }) {
  return <div className="report-component-state"><span>{label}</span></div>
}

function readRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function readString(value: unknown, key: string): string | undefined {
  const candidate = readRecord(value)?.[key]
  return typeof candidate === 'string' ? candidate : undefined
}

function readNumber(value: unknown, key: string): number | undefined {
  const candidate = readRecord(value)?.[key]
  return typeof candidate === 'number' && Number.isFinite(candidate) ? candidate : undefined
}

function readStringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter(item => typeof item === 'string') : []
}

function readNumberArray(value: unknown): number[] {
  return Array.isArray(value) ? value.filter(item => typeof item === 'number' && Number.isFinite(item)) : []
}

function readChartPoints(value: unknown): ChartPoint[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item, index) => {
    const record = readRecord(item)
    const label = readString(record, 'label')
    const number = record?.value
    const semanticValues = readRecord(record?.semanticValues)
    if (!label || typeof number !== 'number' || !Number.isFinite(number) || !semanticValues) return []
    return [{ id: readString(record, 'id') ?? `point_${index}`, label, value: number, semanticValues }]
  })
}

function readOptions(value: unknown): Array<{ label: string; value: string | number | boolean }> {
  if (!Array.isArray(value)) return []
  return value.flatMap(item => {
    const record = readRecord(item)
    const label = readString(record, 'label')
    const optionValue = record?.value
    return label && ['string', 'number', 'boolean'].includes(typeof optionValue) ? [{ label, value: optionValue as string | number | boolean }] : []
  })
}

function encodeOptionValue(value: unknown): string {
  return JSON.stringify({ value })
}

function decodeOptionValue(encoded: string, options: Array<{ label: string; value: string | number | boolean }>): string | number | boolean | undefined {
  return options.find(option => encodeOptionValue(option.value) === encoded)?.value
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function inputType(dataType: string): 'date' | 'month' | 'number' | 'text' {
  if (dataType === 'DATE') return 'date'
  if (dataType === 'DATE_MONTH') return 'month'
  if (dataType === 'INTEGER' || dataType === 'DECIMAL') return 'number'
  return 'text'
}

function sameRuntimeValue(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}

function formatValue(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  if (typeof value === 'number') return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value)
  return String(value)
}

function formatDateTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function latestRuntimeUpdate(runtime: ReportRuntimeContext): string | undefined {
  return Object.values(runtime.componentData).map(item => item.updatedAt).filter((item): item is string => Boolean(item)).sort().at(-1)
}

/** 图片和附件采用不同协议白名单，附件禁止可执行的 data/blob 地址。 */
function safeResourceURL(value: string | undefined, kind: 'IMAGE' | 'ATTACHMENT'): string | undefined {
  if (!value) return undefined
  if (value.startsWith('/') || value.startsWith('./') || value.startsWith('../')) return value
  if (kind === 'IMAGE' && /^data:image\/(png|jpeg|gif|webp);base64,/i.test(value)) return value
  try {
    const url = new URL(value)
    if (url.protocol === 'https:' || url.protocol === 'http:') return value
    return kind === 'IMAGE' && url.protocol === 'blob:' ? value : undefined
  } catch {
    return undefined
  }
}

function buildLinePoints(values: number[]): string {
  if (values.length === 1) return '300,90'
  const minimum = Math.min(...values)
  const maximum = Math.max(...values)
  const range = maximum - minimum || 1
  return values.map((value, index) => {
    const x = 15 + index * (570 / (values.length - 1))
    const y = 160 - ((value - minimum) / range) * 135
    return `${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')
}

/** 富文本采用结构化块而非不可信 HTML，在保留基础排版能力的同时避免脚本注入。 */
function renderRichTextBlock(block: Record<string, unknown>, index: number): ReactNode {
  const type = readString(block, 'type') ?? 'PARAGRAPH'
  const text = readString(block, 'text') ?? ''
  if (type === 'HEADING') return <h4 key={index}>{text}</h4>
  if (type === 'QUOTE') return <blockquote key={index}>{text}</blockquote>
  if (type === 'LIST_ITEM') return <ul key={index}><li>{text}</li></ul>
  return <p key={index}>{text}</p>
}
