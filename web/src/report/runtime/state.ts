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

export function componentPresentation(state: string, boundTitle?: string): ComponentPresentation {
  const safeState = componentStates.includes(state as ReportComponentState)
    ? state as ReportComponentState
    : 'ERROR'
  const presentation = presentations[safeState]
  return {
    state: safeState,
    ...presentation,
    title: presentation.exposeBoundTitle && boundTitle ? boundTitle : presentation.title,
  }
}
