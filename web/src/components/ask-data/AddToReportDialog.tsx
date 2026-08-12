import { CheckCircle, FileText, Plus, WarningCircle, X } from '@phosphor-icons/react'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { mapAskDataError, questionAPI, type AddToReportResult, type QuestionRun } from '../../lib/ask-data-api'
import { reportAssetsAPI } from '../../report/api/assets'
import type { ReportAsset } from '../../report/assets/model'

type AddToReportDialogProps = {
  open: boolean
  snapshot: boolean
  run?: QuestionRun
  onClose: () => void
  onApplied: (reportId: string) => void
}

export function AddToReportDialog({ open, snapshot, run, onClose, onApplied }: AddToReportDialogProps) {
  const navigate = useNavigate()
  const [reports, setReports] = useState<ReportAsset[]>([])
  const [selectedID, setSelectedID] = useState('')
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [intent, setIntent] = useState<AddToReportResult | null>(null)

  useEffect(() => {
    if (!open) return undefined
    let cancelled = false
    const timer = window.setTimeout(() => {
      if (cancelled) return
      setIntent(null); setError('')
      if (snapshot) {
        const fixtures: ReportAsset[] = [
          { id: '00000000-0000-4000-8000-000000000201', code: 'RPT-OPERATIONS-2026', name: '2026年经营分析月报', reportType: 'REPORT', ownerUserId: 'snapshot-owner', ownerName: '王敏', lifecycle: 'CHANGED', currentVersionNo: 7, draftRevisionNo: 12, unpublishedChanges: 2, updatedAt: '2026-08-10T09:30:00+08:00', visibleCount: 18, editableCount: 4, shared: false, allowedActions: ['VIEW', 'EDIT', 'PUBLISH'] },
          { id: '00000000-0000-4000-8000-000000000202', code: 'RPT-CHANNEL-2026', name: '渠道健康度分析报告', reportType: 'REPORT', ownerUserId: 'snapshot-owner', ownerName: '王敏', lifecycle: 'DRAFT_ONLY', draftRevisionNo: 4, unpublishedChanges: 1, updatedAt: '2026-08-09T16:20:00+08:00', visibleCount: 9, editableCount: 2, shared: false, allowedActions: ['VIEW', 'EDIT', 'PUBLISH'] },
        ]
        setLoading(false); setReports(fixtures); setSelectedID(fixtures[0].id)
        return
      }
      setLoading(true)
      void reportAssetsAPI.list({ scope: 'mine', limit: 100 }).then(result => {
        if (cancelled) return
        const editable = result.items.filter(item => item.allowedActions.includes('EDIT') && item.lifecycle !== 'OFFLINE')
        setReports(editable); setSelectedID(editable[0]?.id ?? '')
      }).catch(cause => { if (!cancelled) setError(cause instanceof Error ? cause.message : '报告列表加载失败') })
        .finally(() => { if (!cancelled) setLoading(false) })
    }, 0)
    return () => { cancelled = true; window.clearTimeout(timer) }
  }, [open, snapshot])

  const selected = useMemo(() => reports.find(report => report.id === selectedID), [reports, selectedID])

  const waitForTerminalIntent = async (initial: AddToReportResult) => {
    let result = initial
    for (let attempt = 0; result.status === 'PENDING' && attempt < 20; attempt += 1) {
      await new Promise(resolve => window.setTimeout(resolve, 500))
      result = await questionAPI.getAddToReportIntent(result.intentId)
    }
    return result
  }

  const applyIntentResult = (result: AddToReportResult) => {
    setIntent(result)
    if (result.status === 'APPLIED') onApplied(result.reportId)
    if (result.status === 'REJECTED') {
      const accessChanged = result.rejectionCode === 'REPORT_DATA_ACCESS_FORBIDDEN' || result.rejectionCode === 'REPORT_EDIT_FORBIDDEN'
      setError(accessChanged ? '当前数据或报告权限已发生变化，请刷新权限后重新生成变更预览。' : result.rejectionDetail || '报告草稿已发生变化，请重新生成变更预览。')
    } else if (result.status === 'EXPIRED') {
      setError('报告变更预览已过期，请重新生成。')
    } else if (result.status === 'PENDING') {
      setError('后台处理时间较长，可稍后在报告中心查看；当前页面会保留处理状态。')
    }
  }

  const createIntent = async () => {
    if (!run || !selectedID) return
    setBusy(true); setError('')
    try {
      if (snapshot) {
        setIntent({ intentId: '00000000-0000-4000-8000-000000000203', reportId: selectedID, runId: run.runId, status: 'PENDING_CONFIRMATION', previewHash: 'a'.repeat(64), replayed: false })
        return
      }
      const result = await waitForTerminalIntent(await questionAPI.addToReport({ runId: run.runId, reportId: selectedID, runVersion: run.recordVersion }))
      applyIntentResult(result)
    } catch (cause) {
      setError(mapAskDataError(cause).message)
    } finally {
      setBusy(false)
    }
  }

  const confirm = async () => {
    if (!intent?.previewHash) return
    setBusy(true); setError('')
    try {
      const initial = snapshot ? { ...intent, status: 'APPLIED' as const } : await questionAPI.confirmAddToReport(intent.intentId, intent.previewHash)
      applyIntentResult(await waitForTerminalIntent(initial))
    } catch (cause) {
      setError(mapAskDataError(cause).message)
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null
  return <div className="ask-dialog-overlay" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !busy) onClose() }}>
    <section className="ask-add-report-dialog" role="dialog" aria-modal="true" aria-labelledby="ask-add-report-title">
      <header><div><span><FileText size={18} /></span><div><h2 id="ask-add-report-title">加入报告</h2><p>将当前已验证答案固定到一个可编辑报告草稿。</p></div></div><button type="button" aria-label="关闭" disabled={busy} onClick={onClose}><X size={18} /></button></header>
      {loading && <div className="ask-dialog-feedback"><span className="ask-loader-ring" />正在加载可编辑报告…</div>}
      {!loading && !intent && <div className="ask-report-choice-list">
        {reports.map(report => <label className={selectedID === report.id ? 'is-selected' : ''} key={report.id}><input type="radio" name="report" value={report.id} checked={selectedID === report.id} onChange={() => setSelectedID(report.id)} /><span><strong>{report.name}</strong><small>{report.code} · 草稿 r{report.draftRevisionNo}</small></span><em>{report.lifecycle === 'DRAFT_ONLY' ? '待发布' : '有未发布修改'}</em></label>)}
        {reports.length === 0 && <div className="ask-dialog-empty"><FileText size={25} /><strong>当前没有可编辑报告</strong><p>可以先新建报告，再从问数答案继续添加。</p><button type="button" onClick={() => navigate('/reports/new')}><Plus size={15} />新建报告</button></div>}
      </div>}
      {intent?.status === 'PENDING' && <div className="ask-dialog-feedback"><span className="ask-loader-ring" /><strong>正在生成报告变更预览</strong><p>后台任务仍在处理，可稍后从报告中心继续。</p></div>}
      {intent?.status === 'PENDING_CONFIRMATION' && <div className="ask-report-preview"><CheckCircle size={24} weight="fill" /><div><strong>变更预览已生成</strong><p>将在“{selected?.name ?? '所选报告'}”中新增一个受治理分析组件，并固定当前 Run、语义版本和证据。</p><dl><div><dt>组件类型</dt><dd>系统按结果形态匹配</dd></div><div><dt>目标位置</dt><dd>所选报告内容末尾</dd></div><div><dt>证据状态</dt><dd>已验证</dd></div></dl></div></div>}
      {intent?.status === 'APPLIED' && <div className="ask-report-preview is-success"><CheckCircle size={30} weight="fill" /><div><strong>已加入报告</strong><p>报告已生成新草稿修订，当前问数证据保持可追溯。</p></div></div>}
      {(intent?.status === 'REJECTED' || intent?.status === 'EXPIRED') && <div className="ask-dialog-feedback"><WarningCircle size={26} /><strong>{intent.status === 'EXPIRED' ? '变更预览已过期' : '未能加入报告'}</strong><p>可以关闭弹窗后重新生成，已保存的报告内容不会受到影响。</p></div>}
      {error && <p className="ask-dialog-error" role="alert"><WarningCircle size={15} />{error}</p>}
      <footer>
        <button type="button" disabled={busy} onClick={onClose}>{intent?.status === 'APPLIED' ? '完成' : '取消'}</button>
        {!intent && reports.length > 0 && <button className="primary-button" type="button" disabled={busy || !selectedID || !run} onClick={() => void createIntent()}>{busy ? '生成中…' : '生成变更预览'}</button>}
        {intent?.status === 'PENDING_CONFIRMATION' && <button className="primary-button" type="button" disabled={busy} onClick={() => void confirm()}>{busy ? '应用中…' : '确认加入'}</button>}
        {intent?.status === 'REJECTED' && <button className="primary-button" type="button" disabled={busy} onClick={() => { setIntent(null); setError('') }}>重新生成</button>}
        {intent?.status === 'APPLIED' && <button className="primary-button" type="button" onClick={() => navigate(`/reports/${intent.reportId}?mode=edit`)}>打开报告</button>}
      </footer>
    </section>
  </div>
}
