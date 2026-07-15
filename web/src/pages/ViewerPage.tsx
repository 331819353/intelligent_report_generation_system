/** 以只读形式呈现已发布报告及核心洞察。 */
export function ViewerPage() {
  return (
    <main className="viewer-page">
      <header className="viewer-header"><div><span className="eyebrow">经营分析中心</span><h1>企业经营月度分析</h1></div><div><button className="quiet-button">导出 PDF</button><button className="primary-button">筛选条件</button></div></header>
      <section className="viewer-canvas"><div className="viewer-metrics"><article><span>营业收入</span><strong>¥ 4.82 亿</strong><small className="positive">同比 +12.6%</small></article><article><span>企业数量</span><strong>1,284</strong><small>较上月 +32</small></article><article><span>高技术产业占比</span><strong>38.4%</strong><small className="positive">提升 2.1pp</small></article></div><div className="viewer-grid"><article className="viewer-chart"><div className="panel-heading"><div><span className="eyebrow">产业趋势</span><h2>营业收入及同比变化</h2></div></div><div className="line-art"><svg viewBox="0 0 600 180" role="img" aria-label="趋势图占位"><path d="M10 150 C90 140, 120 80, 200 95 S330 30, 400 70 S520 20, 590 35" fill="none" stroke="currentColor" strokeWidth="4" /></svg></div></article><article className="viewer-insight"><span className="eyebrow light">AI 核心结论</span><h2>增长动能持续增强</h2><p>本期营业收入同比增长 12.6%，高技术产业贡献了超过一半的新增收入。</p><small>基于 3 项指标 · 数据更新于 10:30</small></article></div></section>
    </main>
  )
}
