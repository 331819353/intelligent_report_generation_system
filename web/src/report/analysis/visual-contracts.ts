import type { AnalysisCardVariant } from './catalog.ts'

export type AnalysisCardSizeMode = 'wide' | 'compact' | 'narrow'

export type AnalysisCardVisualContract = {
  id: string
  categoryId: number
  variant: AnalysisCardVariant
  motif: string
  mainVisual: string
  supportingRoles: string[]
  responsive: {
    compact: string
    narrow: string
  }
}

type ContractSeed = readonly [motif: string, mainVisual: string, supportingRoles: readonly string[]]

/**
 * 111 张参考图的运行时视觉合同。
 *
 * 这里的 motif 是渲染器的稳定分派键；同一类别的三张图必须拥有不同 motif，
 * 以防止实现退化成“同一个通用图表换三套颜色”。所有名称与数值仍由数据绑定提供。
 */
const seeds: Record<number, readonly [ContractSeed, ContractSeed, ContractSeed]> = {
  1: [
    ['hero-deltas', '居中大数字与双变化胶囊', ['主指标', '同比', '环比']],
    ['comparison-ring', '中央状态环与左右对比指标', ['主指标', '对比指标', '状态']],
    ['orbit-score', '椭圆轨道包围的综合评分', ['主指标', '辅助指标', '状态']],
  ],
  2: [
    ['bullet-progress', '实际值、目标线与同期标记的子弹图', ['实际值', '目标值', '同期值']],
    ['actual-target-progress', '实际/目标双数字与水平进度条', ['实际值', '目标值', '剩余值']],
    ['goal-gauge', '半圆目标仪表盘', ['达成率', '实际值', '目标值']],
  ],
  3: [
    ['split-versus', '左右对象数值与中部 VS 柱', ['对象A', '对象B', '差异']],
    ['paired-budget-bars', '实际与预算的成组柱状对比', ['实际值', '预算值', '差异率']],
    ['dumbbell-comparison', 'A/B 端点卡与哑铃连接线', ['对象A', '对象B', '差异']],
  ],
  4: [
    ['rank-table', '五行序号排行榜', ['排名对象', '排名值', '排名变化']],
    ['growth-podium', '三行增长榜', ['排名对象', '增长值', '变化方向']],
    ['bottom-ranking', '三行下降榜', ['排名对象', '下降值', '变化方向']],
  ],
  5: [
    ['single-line-card', '蓝色标题带内的单指标折线', ['时间', '趋势指标']],
    ['area-baseline', '面积趋势、虚线基准与最新值', ['时间', '趋势指标', '参考线']],
    ['dual-line-comparison', '双折线对比趋势', ['时间', '指标A', '指标B']],
  ],
  6: [
    ['external-label-donut', '带外部标签的环形构成图', ['构成项', '占比值']],
    ['stacked-share-strip', '水平 100% 堆叠条与图例指标卡', ['构成项', '占比值']],
    ['donut-segment-tiles', '环形图与三块构成信息卡', ['构成项', '占比值', '历史份额']],
  ],
  7: [
    ['stacked-period-bars', '多阶段 100% 堆叠柱', ['阶段', '构成项', '结构值']],
    ['mix-trend-deltas', '渠道结构趋势与变化摘要', ['阶段', '构成项', '结构值']],
    ['before-after-shift', '前后堆叠条、迁移箭头与变化清单', ['阶段', '构成项', '变化值']],
  ],
  8: [
    ['pareto-kpis', '帕累托柱线图与三项集中度指标', ['分析对象', '贡献值', '累计占比']],
    ['pareto-top-share', '帕累托图与 Top5/Top10 摘要', ['分析对象', '贡献值', '头部份额']],
    ['head-tail-pareto', '头部与长尾分区的帕累托曲线', ['分析对象', '贡献值', '长尾区间']],
  ],
  9: [
    ['histogram-quartiles', '直方图与中位数/分位数摘要', ['区间', '频数', '分位数']],
    ['grouped-boxplots', '多分组水平箱线图', ['分组', '分布值']],
    ['density-statistics', '密度曲线与均值/中位数/离散度', ['分布值', '均值', '中位数', '标准差']],
  ],
  10: [
    ['control-line-alert', '控制线、正常区间与异常峰值', ['观察对象', '观测值', '阈值']],
    ['outlier-cloud', '点云分布与离群点标注', ['观察对象', '观测值', '阈值']],
    ['threshold-bars', '柱状观测、阈值线与告警状态带', ['观察对象', '观测值', '阈值']],
  ],
  11: [
    ['positive-regression', '正相关散点与回归趋势', ['X指标', 'Y指标', '相关系数']],
    ['bubble-regression', '气泡散点与负相关趋势', ['X指标', 'Y指标', '规模指标']],
    ['nonlinear-relation', '非线性散点与拟合曲线', ['X指标', 'Y指标', '拟合关系']],
  ],
  12: [
    ['cluster-quadrants', '四色群组四象限', ['对象', '横轴指标', '纵轴指标']],
    ['bubble-quadrants', '带规模的气泡四象限', ['对象', '横轴指标', '纵轴指标', '规模']],
    ['priority-matrix', '强调优先象限的点阵矩阵', ['对象', '重要性', '紧迫性']],
  ],
  13: [
    ['heatmap-totals', '带行列合计的交叉热力图', ['行维度', '列维度', '单元格指标']],
    ['highlight-heatmap', '强调异常单元格的热力矩阵', ['行维度', '列维度', '单元格指标']],
    ['bubble-cross-table', '图形列头与气泡单元格矩阵', ['行维度', '列维度', '单元格指标']],
  ],
  14: [
    ['wide-funnel', '宽体递减漏斗与总转化率', ['阶段', '阶段数量', '总转化率']],
    ['icon-stage-funnel', '阶段图标与紧凑漏斗', ['阶段', '阶段数量', '阶段转化率']],
    ['loss-focused-funnel', '突出最大流失环节的漏斗', ['阶段', '阶段数量', '流失量']],
  ],
  15: [
    ['source-target-sankey', '多来源到多去向桑基图', ['来源', '去向', '流量']],
    ['channel-hub-flow', '渠道经由中心节点的流向图', ['来源', '中心节点', '去向', '流量']],
    ['migration-sankey', '状态迁移流与中央汇总', ['原状态', '新状态', '迁移量']],
  ],
  16: [
    ['triangular-cohort', '三角形 Cohort 留存热力图', ['队列', '队列年龄', '留存指标']],
    ['circle-retention-grid', '圆点编码的队列留存矩阵', ['队列', '队列年龄', '留存指标']],
    ['cohort-curves', '多批次留存曲线', ['队列', '队列年龄', '留存指标']],
  ],
  17: [
    ['lifecycle-curve', '钟形生命周期曲线与阶段指标带', ['阶段', '阶段指标', '对象数量']],
    ['circular-lifecycle', '环形箭头与阶段说明', ['阶段', '阶段指标']],
    ['lifecycle-arc', '四阶段弧线与节点图标', ['阶段', '阶段指标']],
  ],
  18: [
    ['contribution-stack', '100% 贡献堆叠条与外部标注', ['贡献对象', '贡献值']],
    ['diverging-contribution', '正负双向贡献条', ['贡献对象', '贡献值']],
    ['contributor-donut', '贡献环与新旧对象摘要', ['贡献对象', '贡献值', '对象类型']],
  ],
  19: [
    ['revenue-waterfall', '收入从期初到期末的桥接瀑布', ['变化项', '变化值']],
    ['profit-waterfall', '利润增减拆解与总计柱', ['变化项', '变化值']],
    ['variance-bridge', '计划到实际的差异桥接', ['变化项', '差异值']],
  ],
  20: [
    ['impact-whiskers', '正负影响条与置信区间', ['驱动因素', '影响值', '区间']],
    ['feature-beeswarm', '特征重要性蜂群散点', ['驱动因素', '影响值', '样本']],
    ['importance-lollipop', '重要性棒棒糖与区间线', ['驱动因素', '重要性', '区间']],
  ],
  21: [
    ['evidence-tree', '三层根因树与证据卡', ['问题节点', '父节点', '影响值']],
    ['radial-fishbone', '放射式鱼骨根因结构', ['问题节点', '父节点', '影响值']],
    ['compact-diagnostic-tree', '紧凑型诊断树', ['问题节点', '父节点', '影响值']],
  ],
  22: [
    ['forecast-target-band', '实际、预测、置信带与目标线', ['时间', '实际值', '预测值', '目标值']],
    ['forecast-fan', '多层置信扇形预测', ['时间', '预测值', '置信区间']],
    ['compact-forecast', '紧凑折线与预测区间', ['时间', '实际值', '预测值']],
  ],
  23: [
    ['scenario-lines', '乐观/基准/悲观三情景曲线', ['时间', '情景', '结果值']],
    ['scenario-cards', '三张情景结果卡', ['情景', '结果值', '变化值']],
    ['scenario-tree', '假设节点分叉到三种结果', ['假设', '情景', '结果值']],
  ],
  24: [
    ['sensitivity-tornado', '单因素敏感性龙卷风图', ['假设因素', '结果变化']],
    ['two-way-sensitivity', '双因素 5×5 敏感性矩阵', ['因素A', '因素B', '结果值']],
    ['elasticity-bars', '弹性系数正负条', ['假设因素', '弹性值']],
  ],
  25: [
    ['probability-impact-bubbles', '概率×影响渐变气泡矩阵', ['风险对象', '概率', '影响', '敞口']],
    ['scored-risk-matrix', '带风险评分的 5×5 矩阵', ['风险对象', '概率等级', '影响等级']],
    ['radial-risk-distribution', '放射式风险概率分布', ['风险对象', '风险值']],
  ],
  26: [
    ['ab-intervals', 'A/B 均值、置信区间与提升率', ['实验组', '结果值', '置信区间']],
    ['horizontal-error-bars', '两组水平误差线与指标带', ['实验组', '结果值', '置信区间']],
    ['dual-distributions', '实验组/对照组双分布曲线', ['实验组', '结果分布', '显著性']],
  ],
  27: [
    ['regional-choropleth', '区域分级地图', ['区域', '地理指标']],
    ['city-hotspots', '城市热点气泡地图', ['城市', '经度', '纬度', '地理指标']],
    ['cross-region-flows', '跨区域流向地图', ['来源区域', '目标区域', '流量']],
  ],
  28: [
    ['availability-ring', '可用率状态环与侧边指标', ['当前值', '阈值', '状态']],
    ['latency-gauge-trend', '延迟仪表盘、趋势与状态', ['延迟', '阈值', '趋势']],
    ['throughput-gauge', '吞吐仪表盘与迷你趋势', ['吞吐量', '容量', '趋势']],
  ],
  29: [
    ['aging-stage-columns', '按阶段和账龄堆叠的存量柱', ['阶段', '账龄', '存量']],
    ['pipeline-stage-cards', '虚线管道内的四阶段卡片', ['阶段', '存量', '推进率']],
    ['recruiting-dot-pipeline', '候选人圆点编码的五阶段管道', ['阶段', '对象', '存量']],
  ],
  30: [
    ['calendar-heatmap', '月历热力图', ['日期', '节奏指标']],
    ['weekday-hour-matrix', '星期×小时热力矩阵', ['星期', '小时', '节奏指标']],
    ['radial-rhythm', '放射式周期热力图', ['周期', '时段', '节奏指标']],
  ],
  31: [
    ['filterable-detail-table', '带筛选摘要的完整明细表', ['明细维度', '明细指标']],
    ['striped-detail-table', '条纹行与状态标签明细表', ['明细维度', '明细指标']],
    ['compact-query-table', '紧凑查询结果与分页摘要', ['明细维度', '明细指标']],
  ],
  32: [
    ['horizontal-milestones', '横向编号里程碑时间线', ['事件', '时间', '结果值']],
    ['incident-process', '故障处理流程图标时间线', ['事件', '时间', '状态']],
    ['vertical-release-timeline', '纵向版本发布记录', ['事件', '时间', '结果值']],
  ],
  33: [
    ['headline-evidence', '大结论、贡献条与证据指标', ['关键对象', '核心指标', '证据指标']],
    ['headline-evidence-cards', '结论标题与双证据卡', ['关键对象', '核心指标', '证据指标']],
    ['regional-insight', '区域结论图形与摘要胶囊', ['关键对象', '核心指标', '证据指标']],
  ],
  34: [
    ['action-rows', '三条行动建议列表', ['行动', '负责人', '截止时间', '监控指标']],
    ['action-cards', '三张策略行动卡', ['行动', '负责人', '优先级']],
    ['numbered-checklist', '编号清单与完成进度', ['行动', '负责人', '完成度']],
  ],
  35: [
    ['definition-satellites', '中央指标定义与卫星信息点', ['指标名称', '口径', '质量状态', '来源']],
    ['type-definition', '大号数据类型与信息胶囊', ['指标名称', '口径', '样本量', '刷新时间']],
    ['quality-satellites', '中央质量环与数据质量卫星', ['指标名称', '完整性', '及时性', '来源']],
  ],
  36: [
    ['header-filter-form', '蓝色标题带内的筛选表单', ['筛选维度', '筛选值', '结果数']],
    ['horizontal-filter-grid', '横向筛选网格与应用/重置操作', ['筛选维度', '筛选值', '结果数']],
    ['radial-scope-summary', '中央范围摘要与四周条件卡', ['筛选维度', '筛选值', '结果数']],
  ],
  37: [
    ['kpi-over-narrative', '四项证据指标置于双栏长文本上方', ['结论对象', '核心证据', '变化证据', '长文本']],
    ['narrative-with-kpi-rail', '长文本主栏与右侧指标证据栏', ['结论对象', '长文本', '核心证据']],
    ['evidence-sections', '分段论据与嵌入式指标卡', ['论据标题', '长文本', '证据指标']],
  ],
}

const variants: AnalysisCardVariant[] = ['01', '02', '03']

export const analysisCardVisualContracts: AnalysisCardVisualContract[] = Object.entries(seeds)
  .flatMap(([categoryId, entries]) => entries.map((entry, index) => ({
    id: `${categoryId.padStart(2, '0')}-${variants[index]}`,
    categoryId: Number(categoryId),
    variant: variants[index],
    motif: entry[0],
    mainVisual: entry[1],
    supportingRoles: [...entry[2]],
    responsive: {
      compact: '缩短标签、收紧图表网格，辅助指标改为等宽紧凑排列',
      narrow: '主视觉保持可读，辅助信息转为纵向或横向滚动，隐藏非必要轴标签',
    },
  })))

const byId = new Map(analysisCardVisualContracts.map(contract => [contract.id, contract]))

export function analysisCardVisualContract(categoryId: number, variant: AnalysisCardVariant) {
  return byId.get(`${String(categoryId).padStart(2, '0')}-${variant}`)
}
