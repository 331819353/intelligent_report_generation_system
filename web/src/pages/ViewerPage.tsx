import reportExample from '../../../api/examples/report-json-v1.json'
import { ArrowLeft, DownloadSimple, FunnelSimple } from '@phosphor-icons/react'
import { useNavigate } from 'react-router-dom'
import { ValidatedReportRenderer } from '../components/report/ReportRenderer'
import { demoReportInteractionExecutor, demoReportRuntime } from '../lib/demo-report-runtime'

/** 在线查看器只提供运行上下文，页面结构完全交给共享渲染核心还原。 */
export function ViewerPage() {
  const navigate = useNavigate()
  return (
    <main className="viewer-page">
      <header className="viewer-header">
        <button className="viewer-back" type="button" aria-label="返回工作台" onClick={() => navigate('/admin')}><ArrowLeft size={18} /></button>
        <div><span className="eyebrow">经营分析中心</span><h1>企业经营月度分析报告</h1></div>
        <div><button className="quiet-button"><DownloadSimple aria-hidden="true" size={17} />导出 PDF</button><button className="primary-button"><FunnelSimple aria-hidden="true" size={17} />筛选条件</button></div>
      </header>
      <section className="viewer-canvas"><ValidatedReportRenderer source={reportExample} runtime={demoReportRuntime} mode="viewer" onInteractionRequest={demoReportInteractionExecutor} /></section>
    </main>
  )
}
