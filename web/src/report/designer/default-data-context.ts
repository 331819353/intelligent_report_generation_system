import type { DataContextCandidate } from '../api/editor.ts'

type ReportDataContext = { id: string; alias?: string }

function factTableScore(value: string) {
  const normalized = value.trim().toLocaleLowerCase()
  if (!normalized) return 0
  if (normalized.includes('维度表') || /(^|[_\s-])dim([_\s-]|$)/i.test(normalized)) return -10
  if (normalized.includes('事实表')) return 20
  if (normalized.includes('事实')) return 12
  if (/(^|[_\s-])fact([_\s-]|$)/i.test(normalized)) return 10
  return 0
}

/**
 * 卡片默认使用报告已选数据集中的第一张事实表。受治理目录当前没有单独暴露
 * “事实/维度”枚举，因此同时使用报告别名、目录名称和说明判断；没有事实表时
 * 保持报告数据集的原始顺序，兼容历史草稿。
 */
export function defaultFactDataContextId(
  reportContexts: ReportDataContext[],
  candidates: DataContextCandidate[],
) {
  const catalog = new Map(candidates.map(candidate => [candidate.dataContext.id, candidate]))
  let selected = reportContexts[0]
  let selectedScore = 0
  for (const context of reportContexts) {
    const candidate = catalog.get(context.id)
    const score = Math.max(
      factTableScore(context.alias ?? ''),
      factTableScore(candidate?.name ?? ''),
      factTableScore(candidate?.description ?? ''),
    )
    if (score > selectedScore) {
      selected = context
      selectedScore = score
    }
  }
  return selected?.id ?? ''
}
