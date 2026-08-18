import { useEffect, useMemo, useState } from 'react'
import { ArrowRight, CheckCircle, Circle, Info, MagnifyingGlass, SpinnerGap, WarningCircle, X } from '@phosphor-icons/react'
import { useNavigate } from 'react-router-dom'
import { reportEditorAPI, type DataContextCandidate } from '../api/editor.ts'
import type { ReportType } from '../render/schema.ts'

/**
 * 「新建报告」向导弹窗：① 选类型并命名 → ② 勾选报告要用到的数据集 → 创建并进入编辑器。
 *
 * 只依赖已发布数据集目录，不依赖模型提供方。模板 / AI 生成 / JSON 导入等更多方式
 * 仍在 /reports/new 页面提供。
 */

const reportTypeChoices: Array<{ type: ReportType; name: string; hint: string }> = [
  { type: 'REPORT', name: '报告', hint: '分章节的分析文档：图表 + 结论 + 明细，可导出、可定时分发' },
  { type: 'DASHBOARD', name: '报表', hint: '以卡片和筛选器为主的看板：一屏多卡片，交互筛选、联动、钻取' },
]

export function NewReportDialog({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate()
  const [step, setStep] = useState<1 | 2>(1)
  const [reportType, setReportType] = useState<ReportType>('DASHBOARD')
  const [name, setName] = useState('')
  const [contexts, setContexts] = useState<DataContextCandidate[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [query, setQuery] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    void reportEditorAPI.listDataContexts()
      .then(result => { if (!cancelled) { setContexts(result.items); setLoadError('') } })
      .catch(cause => { if (!cancelled) setLoadError(cause instanceof Error ? cause.message : '数据集目录读取失败') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  const visible = useMemo(() => {
    const keyword = query.trim().toLocaleLowerCase()
    return contexts.filter(item => !keyword || item.name.toLocaleLowerCase().includes(keyword) || item.description.toLocaleLowerCase().includes(keyword))
  }, [contexts, query])
  const typeChoice = reportTypeChoices.find(item => item.type === reportType)!

  const create = async () => {
    if (creating || !name.trim() || selected.length === 0) return
    setCreating(true); setError('')
    try {
      const result = await reportEditorAPI.createBlank({ name: name.trim(), reportType, dataContextIds: selected })
      onClose()
      navigate(`/reports/${result.report.id}?mode=edit`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '创建失败，请稍后重试')
    } finally { setCreating(false) }
  }

  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-new-dialog" role="dialog" aria-modal="true" aria-labelledby="new-report-title" onMouseDown={event => event.stopPropagation()}>
      <header>
        <div><span className="eyebrow">新建 · 第 {step} 步 / 共 2 步</span><h2 id="new-report-title">{step === 1 ? '选择类型并命名' : `选择「${name.trim() || typeChoice.name}」要用到的数据集`}</h2></div>
        <button type="button" aria-label="关闭" onClick={onClose}><X size={18} /></button>
      </header>

      {step === 1 && <div className="report-new-dialog-body">
        <div className="report-new-type-grid">
          {reportTypeChoices.map(choice => <button type="button" key={choice.type} className={reportType === choice.type ? 'is-selected' : ''} onClick={() => setReportType(choice.type)}>
            {reportType === choice.type ? <CheckCircle size={18} weight="fill" /> : <Circle size={18} />}
            <strong>{choice.name}</strong><small>{choice.hint}</small>
          </button>)}
        </div>
        <label className="report-new-name">名称
          <input autoFocus value={name} maxLength={80} placeholder={reportType === 'DASHBOARD' ? '例如：经营看板' : '例如：2026 年 7 月经营月报'} onChange={event => setName(event.target.value)}
            onKeyDown={event => { if (event.key === 'Enter' && name.trim()) setStep(2) }} />
        </label>
        <p className="report-editor-binding-note"><Info size={15} />之后在编辑器里：从左侧组件面板拖卡片到画布 → 点击卡片绑定数据集、选择指标/维度 → 配置过滤字段 → 发布。</p>
      </div>}

      {step === 2 && <div className="report-new-dialog-body">
        <label className="report-new-search"><MagnifyingGlass size={16} /><input value={query} placeholder="搜索已发布数据集" onChange={event => setQuery(event.target.value)} /></label>
        {loading && <p className="report-editor-binding-note"><SpinnerGap className="is-spinning" size={15} />正在读取当前领域可用的已发布数据集…</p>}
        {loadError && <p className="report-editor-inline-error"><WarningCircle size={15} />{loadError}</p>}
        {!loading && !loadError && contexts.length === 0 && <p className="report-editor-inline-error"><WarningCircle size={15} />当前业务领域还没有已发布的数据集版本，请先在「数据集」中发布一个版本。</p>}
        <ul className="report-new-dataset-list">
          {visible.map(item => {
            const checked = selected.includes(item.dataContext.id)
            return <li key={item.dataContext.id}>
              <label>
                <input type="checkbox" checked={checked} onChange={event => setSelected(current => event.target.checked
                  ? [...current, item.dataContext.id] : current.filter(id => id !== item.dataContext.id))} />
                <span><strong>{item.name}</strong><small>{item.description || '已发布数据集版本'} · {item.fields.length} 个字段</small></span>
              </label>
            </li>
          })}
        </ul>
        <p className="report-editor-binding-note"><Info size={15} />已选 {selected.length} 个数据集；进入编辑器后仍可增删。字段列表按你的列权限裁剪。</p>
      </div>}

      {error && <p className="report-editor-inline-error report-new-dialog-error"><WarningCircle size={15} />{error}</p>}
      <footer>
        {step === 2 && <button className="quiet-button" type="button" disabled={creating} onClick={() => setStep(1)}>上一步</button>}
        <button className="quiet-button" type="button" disabled={creating} onClick={onClose}>取消</button>
        {step === 1
          ? <button className="primary-button" type="button" disabled={!name.trim()} onClick={() => setStep(2)}>选择数据集<ArrowRight size={16} /></button>
          : <button className="primary-button" type="button" disabled={creating || selected.length === 0} onClick={() => void create()}>
            {creating ? <SpinnerGap className="is-spinning" size={16} /> : <ArrowRight size={16} />}{creating ? '正在创建…' : `创建${typeChoice.name}并进入编辑器`}
          </button>}
      </footer>
    </section>
  </div>
}
