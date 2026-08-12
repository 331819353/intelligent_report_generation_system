import { useCallback, useMemo, useState } from 'react'
import type { Interaction, ReportDefinition } from './schema.ts'

/**
 * 报告运行态：查看者当前施加在报告上的全部约束。
 *
 * 报告级筛选与图表联动看起来是两件事，实质是同一件：都会收窄组件查询。把它们
 * 放在一个模型里，运行页、编辑器草稿预览与发布预览就共用同一套状态与同一次
 * 执行调用，不会各自演化出一套「当前筛选」。
 */

/** 一次联动选择：源组件，以及它自身绑定维度上的取值。 */
export type ReportSelection = {
  componentId: string
  values: Record<string, unknown>
}

export type ReportExecutionInput = {
  pageId: string
  filterValues: Record<string, unknown>
  selections: ReportSelection[]
}

/** 纯函数形式的选择切换，便于调用方在同一个事件里拿到结果并发起执行。 */
export function nextSelections(
  current: ReportSelection[],
  componentId: string,
  values: Record<string, unknown>,
): ReportSelection[] {
  const existing = current.find(item => item.componentId === componentId)
  if (existing && sameValues(existing.values, values)) {
    return current.filter(item => item.componentId !== componentId)
  }
  return [...current.filter(item => item.componentId !== componentId), { componentId, values }]
}

function sameValues(left: Record<string, unknown>, right: Record<string, unknown>) {
  const leftKeys = Object.keys(left)
  if (leftKeys.length !== Object.keys(right).length) return false
  return leftKeys.every(key => JSON.stringify(left[key]) === JSON.stringify(right[key]))
}

/**
 * 组件在联动中的角色。渲染器据此决定是否可点击、是否要高亮或淡出，
 * 而不需要知道联动的具体语义。
 */
export type InteractionRole = {
  /** 该组件是某条联动的源，点击后会影响其它组件。 */
  source: boolean
  /** 该组件当前被选中（它自己发起了选择）。 */
  selected: boolean
  /** 存在选择且该组件不在受影响范围内，应淡出以示无关。 */
  dimmed: boolean
}

export type ReportRuntimeState = {
  filterValues: Record<string, unknown>
  selections: ReportSelection[]
  setFilterValue(filterId: string, value: unknown): void
  replaceFilterValues(values: Record<string, unknown>): void
  /**
   * 再次点击同一取值即取消选择——这是十字筛选的常规交互。
   * 返回切换后的选择列表，调用方可以据此立即发起执行，而不必依赖 effect
   * 观察状态变化（那会引入一次级联渲染）。
   */
  toggleSelection(componentId: string, values: Record<string, unknown>): ReportSelection[]
  clearSelections(): void
  roleFor(componentId: string): InteractionRole
  executionInput(pageId: string): ReportExecutionInput
  /** NAVIGATE_PAGE 是纯客户端效果，由调用方决定如何跳转。 */
  navigationTargetFor(componentId: string): string | undefined
}

export function useReportRuntimeState(
  definition: Pick<ReportDefinition, 'interactions'> | undefined,
): ReportRuntimeState {
  const [filterValues, setFilterValues] = useState<Record<string, unknown>>({})
  const [selections, setSelections] = useState<ReportSelection[]>([])

  const interactions: Interaction[] = useMemo(() => definition?.interactions ?? [], [definition])

  const sources = useMemo(
    () => new Set(interactions.map(interaction => interaction.sourceComponentId)),
    [interactions],
  )

  /** 当前选择所影响到的组件集合（含发起选择的源组件本身）。 */
  const affected = useMemo(() => {
    const result = new Set<string>()
    for (const selection of selections) {
      result.add(selection.componentId)
      for (const interaction of interactions) {
        if (interaction.sourceComponentId !== selection.componentId) continue
        for (const target of interaction.targetComponentIds) result.add(target)
      }
    }
    return result
  }, [interactions, selections])

  const setFilterValue = useCallback((filterId: string, value: unknown) => {
    setFilterValues(current => {
      if (value === undefined || value === '') {
        const rest = { ...current }
        delete rest[filterId]
        return rest
      }
      return { ...current, [filterId]: value }
    })
  }, [])

  const toggleSelection = useCallback((componentId: string, values: Record<string, unknown>) => {
    const next = nextSelections(selections, componentId, values)
    setSelections(next)
    return next
  }, [selections])

  const clearSelections = useCallback(() => setSelections([]), [])

  const roleFor = useCallback((componentId: string): InteractionRole => ({
    source: sources.has(componentId),
    selected: selections.some(item => item.componentId === componentId),
    dimmed: selections.length > 0 && !affected.has(componentId),
  }), [affected, selections, sources])

  const navigationTargetFor = useCallback((componentId: string) => interactions
    .find(interaction => interaction.sourceComponentId === componentId && interaction.action === 'NAVIGATE_PAGE')
    ?.targetPageId, [interactions])

  const executionInput = useCallback((pageId: string): ReportExecutionInput => ({
    pageId, filterValues, selections,
  }), [filterValues, selections])

  return {
    filterValues, selections,
    setFilterValue,
    replaceFilterValues: setFilterValues,
    toggleSelection, clearSelections, roleFor, executionInput, navigationTargetFor,
  }
}

/**
 * 描述当前选择，供界面展示「已按 X 联动筛选」并提供清除入口。
 * 只陈述实际发送给服务端的取值，不推测其业务含义。
 */
export function describeSelections(selections: ReportSelection[]): string {
  return selections
    .flatMap(selection => Object.entries(selection.values)
      .map(([field, value]) => `${field} = ${Array.isArray(value) ? value.join('、') : String(value)}`))
    .join('；')
}
