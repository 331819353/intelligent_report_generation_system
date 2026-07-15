import { AppShell } from '../components/AppShell'

const metrics = [
  ['数据源', '3', 'MySQL · Oracle · Excel'],
  ['已发布报告', '12', '本月新增 4 份'],
  ['数据集', '28', '6 个跨源数据集'],
  ['待处理任务', '2', '无失败任务'],
]

/** 展示租户工作台的关键指标与最近报告。 */
export function AdminPage() {
  return (
    <AppShell title="工作台" eyebrow="概览" actions={<button className="primary-button">新建报告</button>}>
      <section className="content-stack">
        <div className="welcome-card"><div><span className="eyebrow light">数据工作空间</span><h2>下午好，报告设计师</h2><p>从数据源到正式归档，所有工作都在统一的租户边界内完成。</p></div><div className="orbit">AI</div></div>
        <div className="metric-grid">{metrics.map(([label, value, detail]) => <article className="metric-card" key={label}><span>{label}</span><strong>{value}</strong><small>{detail}</small></article>)}</div>
        <section className="panel"><div className="panel-heading"><div><span className="eyebrow">最近报告</span><h2>继续你的工作</h2></div><button className="quiet-button">查看全部</button></div><div className="report-list"><article><span className="status-dot published" /><div><strong>企业经营月度分析</strong><small>已发布 · 10 分钟前更新</small></div><span>经营分析</span></article><article><span className="status-dot draft" /><div><strong>产业链风险监测</strong><small>草稿 · 昨天编辑</small></div><span>风险洞察</span></article></div></section>
      </section>
    </AppShell>
  )
}
