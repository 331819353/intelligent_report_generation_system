import { AppShell } from '../components/AppShell'

/** 展示报告设计器的组件栏、画布和属性面板骨架。 */
export function DesignerPage() {
  return (
    <AppShell title="企业经营月度分析" eyebrow="报告设计器" actions={<><button className="quiet-button">预览</button><button className="primary-button">发布</button></>}>
      <div className="designer-layout">
        <aside className="tool-panel"><span className="eyebrow">组件</span><h2>内容组件</h2>{['标题', '筛选器', '指标卡', '图表', '结论'].map(item => <button key={item} className="component-tile">＋ {item}</button>)}</aside>
        <section className="canvas-wrap"><div className="canvas-toolbar"><span>1920 × 1080 首屏基准</span><strong>缩放 50%</strong></div><div className="canvas"><div className="canvas-grid"><article className="demo-block title-block"><span className="block-tag">1 × 1</span><h3>企业经营月度分析</h3><button className="ai-float">✦ AI 修改</button></article><article className="demo-block chart-block"><span className="block-tag">8 × 4</span><div className="chart-placeholder"><i /><i /><i /><i /><i /></div></article><article className="demo-block insight-block"><span className="block-tag">4 × 4</span><span className="eyebrow">核心结论</span><p>营业收入保持增长，高技术产业贡献主要增量。</p></article></div></div></section>
        <aside className="property-panel"><span className="eyebrow">属性</span><h2>分块设置</h2><label>分块名称<input value="经营概览" readOnly /></label><label>浏览态冻结<select defaultValue="off"><option value="off">不冻结</option><option value="on">顶部冻结</option></select></label><button className="quiet-button full">清空分块</button></aside>
      </div>
    </AppShell>
  )
}
