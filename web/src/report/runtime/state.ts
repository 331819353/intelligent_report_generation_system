export const componentStates = [
  'LOADING', 'READY', 'EMPTY', 'PARTIAL', 'ERROR', 'NO_PERMISSION', 'STALE', 'TIMEOUT',
] as const

export type ReportComponentState = typeof componentStates[number]

export type ComponentPresentation = {
  state: ReportComponentState
  label: string
  title: string
  message: string
  action?: string
  tone: 'success' | 'info' | 'warning' | 'danger' | 'neutral'
  exposeBoundTitle: boolean
}
const presentations: Record<ReportComponentState, Omit<ComponentPresentation, 'state'>> = {
  LOADING: { label: '加载中', title: '正在加载组件', message: '正在按当前查看者权限查询数据…', tone: 'info', exposeBoundTitle: true },
  READY: { label: '已验证', title: '组件已完成', message: '数据正常，图表可交互。', tone: 'success', exposeBoundTitle: true },
  EMPTY: { label: '暂无数据', title: '当前筛选下暂无数据', message: '可调整筛选条件后重新查询。', tone: 'neutral', exposeBoundTitle: true },
  PARTIAL: { label: '部分数据', title: '组件部分完成', message: '受部分数据不可用影响，结果部分缺失。', action: '查看说明', tone: 'warning', exposeBoundTitle: true },
  ERROR: { label: '加载失败', title: '组件加载失败', message: '组件暂时不可用，可稍后重试。', action: '重试', tone: 'danger', exposeBoundTitle: true },
  NO_PERMISSION: { label: '无权限', title: '受限组件', message: '无权限查看', tone: 'neutral', exposeBoundTitle: false },
  STALE: { label: '数据过期', title: '组件数据已过期', message: '数据截止时间早于报告要求，建议刷新。', action: '刷新数据', tone: 'warning', exposeBoundTitle: true },
  TIMEOUT: { label: '响应超时', title: '组件响应超时', message: '查询超过时间预算，请重试。', action: '重试', tone: 'neutral', exposeBoundTitle: true },
}

/**
 * 组件失败原因的可读说明。
 *
 * 状态只说明「成功与否」，原因码说明「为什么」。没有这层映射时，草稿预览里
 * 一个固定了语义发布版本的组件只会显示「组件加载失败」，作者无从判断那其实
 * 是预期行为而不是配置错误。
 */
const errorCodeMessages: Record<string, { title: string; message: string; tone: ComponentPresentation['tone'] }> = {
  REPORT_DRAFT_PREVIEW_REQUIRES_PUBLISH: {
    title: '草稿预览不可执行',
    message: '该组件固定了语义发布版本，其受治理查询制品只对已发布版本开放；发布后即可查看数据。',
    tone: 'neutral',
  },
  REPORT_ROLLUP_MEASURE_NON_ADDITIVE: {
    title: '该度量不能按当前维度汇总',
    message: '平均值、去重计数与非可加度量无法跨被省略的维度重新汇总；请补齐维度或改用可加度量。',
    tone: 'warning',
  },
  REPORT_ROLLUP_MEASURE_UNDECLARED: {
    title: '度量缺少聚合口径',
    message: '数据集版本未声明该度量的聚合方式，无法安全汇总；请在数据集契约中补充。',
    tone: 'warning',
  },
  REPORT_ROLLUP_SOURCE_TRUNCATED: {
    title: '数据超出查询上限',
    message: '源数据在汇总前已被行数上限截断，合计值会失真；请增加筛选条件缩小范围。',
    tone: 'warning',
  },
}

export function componentPresentation(state: string, boundTitle?: string, errorCode?: string): ComponentPresentation {
  const safeState = componentStates.includes(state as ReportComponentState)
    ? state as ReportComponentState
    : 'ERROR'
  const presentation = presentations[safeState]
  const explained = errorCode ? errorCodeMessages[errorCode] : undefined
  if (explained) {
    return {
      state: safeState,
      ...presentation,
      title: explained.title,
      message: explained.message,
      tone: explained.tone,
      action: undefined,
    }
  }
  return {
    state: safeState,
    ...presentation,
    title: presentation.exposeBoundTitle && boundTitle ? boundTitle : presentation.title,
  }
}
