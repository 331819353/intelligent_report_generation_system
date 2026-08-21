import { useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react'
import { createPortal } from 'react-dom'
import {
  ArrowsLeftRight, Check, CheckCircle, Database, FileText, MagnifyingGlass, RocketLaunch,
  SpinnerGap, WarningCircle, X,
} from '@phosphor-icons/react'
import { useNavigate } from 'react-router-dom'
import { datasetAPI, type DatasetPreview } from '../../lib/datasets.ts'
import { reportEditorAPI, type DataContextCandidate } from '../api/editor.ts'
import { ReportHeaderChooser } from '../render/ReportHeader.tsx'
import type { ReportHeaderStyle } from '../render/schema.ts'

/**
 * 新建报告启动台：名称、报告头与数据集在同一页完成，确认后直接进入画布。
 * 创建入口不再要求用户先区分报告/报表，也不暴露组件拖拽等已经淘汰的路径。
 */
const snapshotContexts: DataContextCandidate[] = [
  { dataContext: { id: 'snapshot-sales', datasetId: 'sales', datasetVersionId: 'v12' }, name: '销售订单明细事实表', description: '订单、交付、渠道与商品经营明细', fields: Array.from({ length: 28 }, (_, index) => `sales_${index + 1}`) },
  { dataContext: { id: 'snapshot-customer', datasetId: 'customer', datasetVersionId: 'v8' }, name: '客户经营主题数据集', description: '客户分层、区域、活跃与价值指标', fields: Array.from({ length: 19 }, (_, index) => `customer_${index + 1}`) },
  { dataContext: { id: 'snapshot-inventory', datasetId: 'inventory', datasetVersionId: 'v6' }, name: '库存健康度分析集', description: '库存数量、周转、库龄与风险状态', fields: Array.from({ length: 16 }, (_, index) => `inventory_${index + 1}`) },
]

type DatasetPreviewState = { loading: boolean; data?: DatasetPreview; error?: string }

function snapshotPreview(candidate: DataContextCandidate): DatasetPreview {
  const columns = candidate.fields
  const names = ['订单编号', '产品名称', '所属区域', '销售渠道', '客户类型', '下单日期', '交付日期', '销售数量', '销售金额', '毛利额']
  return {
    queryId: `snapshot-${candidate.dataContext.id}`,
    columns,
    columnMetadata: columns.map((code, index) => ({
      code, name: candidate.fieldDefinitions?.find(field => field.code === code)?.name || names[index] || `业务字段 ${index + 1}`,
      nullable: true,
    })),
    rows: Array.from({ length: 10 }, (_, rowIndex) => columns.map((_, columnIndex) => {
      const values: unknown[] = [
        `SO-${20260821001 + rowIndex}`, `产品 ${String.fromCharCode(65 + rowIndex % 6)}`,
        ['华东', '华南', '华北', '西南'][rowIndex % 4], ['线上', '直营网点', '经销商'][rowIndex % 3],
        ['企业客户', '个人客户'][rowIndex % 2], `2026-08-${String(10 + rowIndex).padStart(2, '0')}`,
        `2026-08-${String(12 + rowIndex).padStart(2, '0')}`, 12 + rowIndex * 3,
        (126800 + rowIndex * 7350).toLocaleString('zh-CN'), (31600 + rowIndex * 1840).toLocaleString('zh-CN'),
      ]
      return values[columnIndex] ?? `${rowIndex + 1}-${columnIndex + 1}`
    })),
    rowCount: 10,
    durationMs: 86,
  }
}

function DatasetPreviewPopover({ candidate, preview, anchor, onRetry, onClose, onPointerEnter, onPointerLeave }: {
  candidate: DataContextCandidate
  preview?: DatasetPreviewState
  anchor: DOMRect
  onRetry: () => void
  onClose: () => void
  onPointerEnter: () => void
  onPointerLeave: () => void
}) {
  const pan = useRef<{ pointerId: number; x: number; left: number } | undefined>(undefined)
  const [panning, setPanning] = useState(false)
  const stopPan = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    pan.current = undefined
    setPanning(false)
  }
  const data = preview?.data
  const width = Math.min(760, window.innerWidth - 24)
  const left = Math.max(12, Math.min(anchor.left + anchor.width / 2 - width / 2, window.innerWidth - width - 12))
  const maxHeight = Math.min(410, Math.max(300, anchor.top - 20))
  const top = Math.max(12, anchor.top - maxHeight - 12)
  const arrowX = Math.max(18, Math.min(width - 18, anchor.left + anchor.width / 2 - left))
  const placement = { left, top, width, height: maxHeight, '--preview-arrow-x': `${arrowX}px` } as CSSProperties

  return <section className="report-new-dataset-preview" style={placement} aria-label={`${candidate.name}前十条数据预览`}
    onMouseDown={event => event.stopPropagation()} onPointerEnter={onPointerEnter} onPointerLeave={onPointerLeave}>
    <header><div><span><Database size={16} weight="duotone" /></span><p><small>数据预览 · 前 10 条</small><strong>{candidate.name}</strong></p></div>
      <button type="button" aria-label="关闭数据预览" onClick={onClose}><X size={14} /></button></header>
    <div className="report-new-dataset-preview-hint"><ArrowsLeftRight size={14} />按住表格左右拖动，查看未展示字段</div>
    {preview?.loading && <div className="report-new-dataset-preview-state"><SpinnerGap className="is-spinning" size={18} />正在读取受权限保护的预览数据…</div>}
    {preview?.error && <div className="report-new-dataset-preview-state is-error"><WarningCircle size={18} /><span>{preview.error}</span><button type="button" onClick={onRetry}>重试</button></div>}
    {data && data.columns.length > 0 && data.rows.length > 0 && <div
      className={`report-new-dataset-preview-table ${panning ? 'is-panning' : ''}`}
      onPointerDown={event => {
        if (event.button !== 0) return
        pan.current = { pointerId: event.pointerId, x: event.clientX, left: event.currentTarget.scrollLeft }
        event.currentTarget.setPointerCapture(event.pointerId)
        setPanning(true)
      }}
      onPointerMove={event => {
        if (!pan.current || pan.current.pointerId !== event.pointerId) return
        event.currentTarget.scrollLeft = pan.current.left - (event.clientX - pan.current.x)
      }}
      onPointerUp={stopPan}
      onPointerCancel={stopPan}
    ><table><thead><tr><th aria-label="行号">#</th>{data.columns.map((column, index) => {
      const metadata = data.columnMetadata?.[index]
      const governedField = candidate.fieldDefinitions?.find(field => field.code === column)
      const label = metadata?.name?.trim() || governedField?.name?.trim() || column
      return <th key={`${column}-${index}`} title={metadata?.description || column}><strong>{label}</strong>{label !== column && <small>{column}</small>}</th>
    })}</tr></thead><tbody>{data.rows.slice(0, 10).map((row, rowIndex) => <tr key={rowIndex}><th>{rowIndex + 1}</th>{data.columns.map((_, columnIndex) => <td key={columnIndex}>{row[columnIndex] == null ? '—' : String(row[columnIndex])}</td>)}</tr>)}</tbody></table></div>}
    {data && (!data.columns.length || !data.rows.length) && <div className="report-new-dataset-preview-state">该数据集当前没有可预览的数据。</div>}
    <footer><span>仅展示样例，不会写入报告</span>{data && <em>{Math.min(data.rows.length, 10)} 行 · {data.columns.length} 个字段</em>}</footer>
  </section>
}

export function NewReportDialog({ onClose, snapshot = false }: { onClose: () => void; snapshot?: boolean }) {
  const navigate = useNavigate()
  const [headerStyle, setHeaderStyle] = useState<ReportHeaderStyle>()
  const [name, setName] = useState('')
  const [contexts, setContexts] = useState<DataContextCandidate[]>(snapshot ? snapshotContexts : [])
  const [loading, setLoading] = useState(!snapshot)
  const [loadError, setLoadError] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [query, setQuery] = useState('')
  const [previewContextId, setPreviewContextId] = useState('')
  const [previewAnchor, setPreviewAnchor] = useState<DOMRect>()
  const [previews, setPreviews] = useState<Record<string, DatasetPreviewState>>({})
  const previewRequests = useRef(new Set<string>())
  const previewCloseTimer = useRef<number | undefined>(undefined)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (snapshot) return undefined
    let cancelled = false
    void reportEditorAPI.listDataContexts()
      .then(result => { if (!cancelled) { setContexts(result.items); setLoadError('') } })
      .catch(cause => { if (!cancelled) setLoadError(cause instanceof Error ? cause.message : '数据集目录读取失败') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [snapshot])

  useEffect(() => () => {
    if (previewCloseTimer.current) window.clearTimeout(previewCloseTimer.current)
  }, [])

  const visible = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase()
    return contexts.filter(item => !keyword || item.name.toLocaleLowerCase().includes(keyword) || item.description.toLocaleLowerCase().includes(keyword))
  }, [contexts, query])
  const selectedContexts = contexts.filter(item => selected.includes(item.dataContext.id))
  const previewCandidate = contexts.find(item => item.dataContext.id === previewContextId)
  const ready = Boolean(name.trim() && headerStyle && selected.length > 0 && !loading && !loadError)

  const toggleContext = (contextId: string) => {
    setSelected(current => current.includes(contextId)
      ? current.filter(id => id !== contextId)
      : [...current, contextId])
  }

  const loadPreview = async (candidate: DataContextCandidate, force = false) => {
    const contextId = candidate.dataContext.id
    setPreviewContextId(contextId)
    if (!force && (previews[contextId]?.data || previewRequests.current.has(contextId))) return
    previewRequests.current.add(contextId)
    setPreviews(current => ({ ...current, [contextId]: { loading: true } }))
    try {
      const data = snapshot
        ? snapshotPreview(candidate)
        : await datasetAPI.previewVersion(
          candidate.dataContext.datasetId, candidate.dataContext.datasetVersionId, crypto.randomUUID(), {}, 10,
        )
      setPreviews(current => ({ ...current, [contextId]: { loading: false, data } }))
    } catch (cause) {
      setPreviews(current => ({
        ...current,
        [contextId]: { loading: false, error: cause instanceof Error ? cause.message : '预览数据读取失败' },
      }))
    } finally {
      previewRequests.current.delete(contextId)
    }
  }

  const openPreview = (candidate: DataContextCandidate, element: HTMLElement) => {
    if (previewCloseTimer.current) window.clearTimeout(previewCloseTimer.current)
    setPreviewAnchor(element.getBoundingClientRect())
    void loadPreview(candidate)
  }
  const keepPreviewOpen = () => {
    if (previewCloseTimer.current) window.clearTimeout(previewCloseTimer.current)
  }
  const schedulePreviewClose = () => {
    if (previewCloseTimer.current) window.clearTimeout(previewCloseTimer.current)
    previewCloseTimer.current = window.setTimeout(() => {
      setPreviewContextId('')
      setPreviewAnchor(undefined)
    }, 140)
  }
  const closePreview = () => {
    if (previewCloseTimer.current) window.clearTimeout(previewCloseTimer.current)
    setPreviewContextId('')
    setPreviewAnchor(undefined)
  }

  const create = async () => {
    if (creating || !ready) return
    setCreating(true); setError('')
    try {
      if (snapshot) {
        onClose()
        navigate('/reports/snapshot-report-editor?mode=edit&snapshot=report-editor-canvas-add')
        return
      }
      const result = await reportEditorAPI.createBlank({
        name: name.trim(), reportType: 'REPORT', headerStyle, dataContextIds: selected,
      })
      onClose()
      navigate(`/reports/${result.report.id}?mode=edit`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '创建失败，请稍后重试')
    } finally { setCreating(false) }
  }

  return <div className="report-modal-backdrop report-new-launch-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-new-dialog report-new-launchpad" role="dialog" aria-modal="true" aria-labelledby="new-report-title"
      onMouseDown={event => event.stopPropagation()}>
      <header className="report-new-launch-header">
        <div className="report-new-launch-heading">
          <span><FileText size={21} weight="duotone" /></span>
          <div><small>REPORT STUDIO</small><h2 id="new-report-title">创建新报告</h2><p>完成基础设置后，直接进入清晰画布开始分析。</p></div>
        </div>
        <button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button>
      </header>

      <div className="report-new-launch-body">
        <main className="report-new-launch-form">
          <section className="report-new-launch-section is-name">
            <header><span>01</span><div><strong>报告名称</strong><small>使用清晰、可识别的业务名称</small></div></header>
            <label className="report-new-launch-name">
              <input autoFocus value={name} maxLength={80} placeholder="例如：2026 年 8 月经营分析报告"
                aria-label="报告名称" onChange={event => setName(event.target.value)} />
              <em>{name.trim().length}/80</em>
            </label>
          </section>

          <section className="report-new-launch-section is-header">
            <header><span>02</span><div><strong>报告头</strong><small>选择进入画布后默认携带的标题与筛选风格</small></div></header>
            <ReportHeaderChooser value={headerStyle} onChange={setHeaderStyle} />
          </section>

          <section className="report-new-launch-section is-datasets">
            <header><span>03</span><div><strong>数据集</strong><small>可多选；鼠标移入可预览前 10 条权限内数据</small></div>
              <b>{selected.length > 0 ? `已选 ${selected.length}` : '至少选择 1 个'}</b></header>
            <label className="report-new-launch-search"><MagnifyingGlass size={17} />
              <input value={query} placeholder="搜索已发布数据集" aria-label="搜索已发布数据集" onChange={event => setQuery(event.target.value)} />
              {query && <button type="button" aria-label="清除搜索" onClick={() => setQuery('')}><X size={13} /></button>}
            </label>
            {loading && <div className="report-new-launch-state"><SpinnerGap className="is-spinning" size={18} />正在加载可用数据集…</div>}
            {loadError && <div className="report-new-launch-state is-error"><WarningCircle size={18} />{loadError}</div>}
            {!loading && !loadError && contexts.length === 0 && <div className="report-new-launch-state is-error"><WarningCircle size={18} />当前业务领域还没有已发布的数据集，请先发布数据集版本。</div>}
            {!loading && !loadError && visible.length === 0 && contexts.length > 0 && <div className="report-new-launch-state">没有匹配的数据集，换个关键词试试。</div>}
            <ul className="report-new-launch-datasets">
              {visible.map(item => {
                const checked = selected.includes(item.dataContext.id)
                return <li key={item.dataContext.id}>
                  <button type="button" className={checked ? 'is-selected' : ''} aria-pressed={checked}
                    aria-label={`${checked ? '取消选择' : '选择'}数据集 ${item.name}`} onClick={() => toggleContext(item.dataContext.id)}
                    onPointerEnter={event => openPreview(item, event.currentTarget)} onPointerLeave={schedulePreviewClose}
                    onFocus={event => openPreview(item, event.currentTarget)} onBlur={schedulePreviewClose}>
                    <span className="report-new-launch-dataset-icon"><Database size={17} weight="duotone" /></span>
                    <span><strong>{item.name}</strong><small>{item.description || '已发布数据集版本'}</small></span>
                    <em>{item.fields.length} 字段</em>
                    <i>{checked ? <Check size={13} weight="bold" /> : null}</i>
                  </button>
                </li>
              })}
            </ul>
          </section>
        </main>

        <aside className="report-new-launch-summary" aria-label="创建摘要">
          <header><span><RocketLaunch size={18} weight="duotone" /></span><div><strong>开启画布</strong><small>所有设置均可在创建后继续调整</small></div></header>
          <div className={`report-new-launch-cover is-style-${headerStyle || 'empty'}`}>
            {headerStyle
              ? <img src={`/report-header-gallery/${headerStyle}.png`} alt="已选报告头预览" />
              : <div><FileText size={28} weight="duotone" /><span>选择报告头后显示预览</span></div>}
          </div>
          <div className="report-new-launch-report-name"><small>即将创建</small><strong>{name.trim() || '未命名报告'}</strong><span>报告 · 空白画布</span></div>
          <ol className="report-new-launch-checklist">
            <li className={name.trim() ? 'is-done' : ''}><span>{name.trim() ? <Check size={12} weight="bold" /> : '1'}</span><div><strong>报告名称</strong><small>{name.trim() || '等待填写'}</small></div></li>
            <li className={headerStyle ? 'is-done' : ''}><span>{headerStyle ? <Check size={12} weight="bold" /> : '2'}</span><div><strong>报告头</strong><small>{headerStyle ? `样式 ${headerStyle}` : '等待选择'}</small></div></li>
            <li className={selected.length > 0 ? 'is-done' : ''}><span>{selected.length > 0 ? <Check size={12} weight="bold" /> : '3'}</span><div><strong>数据集</strong><small>{selected.length > 0 ? `${selected.length} 个已选择` : '等待选择'}</small></div></li>
          </ol>
          {selectedContexts.length > 0 && <div className="report-new-launch-selected">
            <small>已选数据集</small><div>{selectedContexts.map(item => <span key={item.dataContext.id}>{item.name}</span>)}</div>
          </div>}
          <p><CheckCircle size={15} weight="fill" />创建后自动保存为草稿，并直接打开报告画布。</p>
        </aside>
      </div>

      {error && <div className="report-new-launch-error" role="alert"><WarningCircle size={16} /><span>{error}</span></div>}
      <footer className="report-new-launch-footer">
        <span>{ready ? '配置已完成，可以进入画布' : '完成名称、报告头和数据集后即可继续'}</span>
        <div><button className="quiet-button" type="button" disabled={creating} onClick={onClose}>取消</button>
          <button className="primary-button" type="button" disabled={creating || !ready} onClick={() => void create()}>
            {creating ? <SpinnerGap className="is-spinning" size={16} /> : <RocketLaunch size={16} weight="fill" />}
            {creating ? '正在创建…' : '开启画布'}
          </button></div>
      </footer>
    </section>
    {previewCandidate && previewAnchor && createPortal(<DatasetPreviewPopover candidate={previewCandidate} anchor={previewAnchor}
      preview={previews[previewCandidate.dataContext.id]} onRetry={() => void loadPreview(previewCandidate, true)} onClose={closePreview}
      onPointerEnter={keepPreviewOpen} onPointerLeave={schedulePreviewClose} />, document.body)}
  </div>
}
