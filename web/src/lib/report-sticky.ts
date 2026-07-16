import type { ReportBlock, ReportComponent, ReportPage, ReportValidationIssue, Sticky } from './report-contract'
import { LOGICAL_CANVAS_WIDTH, LOGICAL_COLUMN_WIDTH, LOGICAL_INNER_COLUMN_WIDTH, LOGICAL_INNER_ROW_HEIGHT, LOGICAL_ROW_HEIGHT, MAX_STICKY_TOP, MAX_STICKY_Z_INDEX } from './report-layout'

export type ReportStickyPlacementKind = 'BLOCK' | 'COMPONENT'

export type ReportStickyFailureCode =
  | 'INVALID_SCALE'
  | 'INVALID_VIEWPORT'
  | 'INVALID_TOP'
  | 'INVALID_Z_INDEX'
  | 'INVALID_SCOPE'
  | 'INVALID_CONTAINER'
  | 'INVALID_GEOMETRY'
  | 'OUTSIDE_CONTAINER'
  | 'NO_CAPACITY'

export type ReportStickyPlacement = {
  key: string
  kind: ReportStickyPlacementKind
  id: string
  blockID?: string
  path: string
  scopeKey: string
  top: number
  maxTop: number
  containerTop: number
  containerBottom: number
  translateY: number
  enabled: boolean
  stuck: boolean
  atBoundary: boolean
  zIndex: number
  reasonCode?: ReportStickyFailureCode
  reason?: string
}

export type ReportStickyPlan = {
  placements: ReportStickyPlacement[]
  issues: ReportValidationIssue[]
}

export type CalculateReportStickyPlacementsInput = {
  page: ReportPage
  /** 视口顶部相对页面顶部的逻辑坐标；页面尚未滚入时可以为负数。 */
  viewportTop: number
  /** 逻辑画布到屏幕像素的缩放比例。 */
  scale: number
  /** 只有真正通过权限和可见性判断、已挂载到页面的分块才参与计算。 */
  renderedBlockIDs: Iterable<string>
  /** 分块与组件分开传入，避免二者同名时互相覆盖。 */
  renderedComponentIDs: Iterable<string>
  /** 用于生成可直接并入报告校验结果的稳定路径。 */
  pageIndex?: number
}

type Rect = {
  left: number
  top: number
  width: number
  height: number
}

type Constraint = Rect & {
  key: string
}

type EnabledSticky = Extract<Sticky, { enabled: true }>

type StickyCandidate = {
  key: string
  kind: ReportStickyPlacementKind
  id: string
  blockID?: string
  path: string
  sticky: EnabledSticky
  rect: Rect
  ownerRect?: Rect
  parentTranslateY: number
  page: ReportPage
}

type StackEntry = ReportStickyPlacement & {
  left: number
  right: number
  height: number
}

type CandidateFailure = {
  code: ReportStickyFailureCode
  reason: string
  path?: string
}

const EPSILON = 1e-7

/**
 * 生成浏览态冻结位移。top 使用视口 CSS 像素，计算时除以 scale 还原为逻辑坐标，
 * 因而画布缩放不会改变用户看到的顶部留白。
 */
export function calculateReportStickyPlacements(input: CalculateReportStickyPlacementsInput): ReportStickyPlan {
  const renderedBlockIDs = new Set(input.renderedBlockIDs)
  const renderedComponentIDs = new Set(input.renderedComponentIDs)
  const pageIndex = input.pageIndex ?? 0
  const placements: ReportStickyPlacement[] = []
  const issues: ReportValidationIssue[] = []
  const stackEntries: StackEntry[] = []
  const pageRect: Rect = {
    left: 0,
    top: 0,
    width: LOGICAL_CANVAS_WIDTH,
    height: input.page.contentGridRows * LOGICAL_ROW_HEIGHT,
  }

  function append(candidate: StickyCandidate): ReportStickyPlacement {
    const placement = placeCandidate(candidate, input.viewportTop, input.scale, pageRect, stackEntries)
    placements.push(placement)
    if (!placement.enabled && placement.reason) {
      issues.push({ path: placement.path, reason: placement.reason })
    }
    if (placement.enabled) {
      stackEntries.push({
        ...placement,
        left: candidate.rect.left,
        right: candidate.rect.left + candidate.rect.width,
        height: candidate.rect.height,
      })
    }
    return placement
  }

  // 先按自然纵向、横向坐标排序，再以原数组顺序打破平局，确保重渲染不改变堆叠次序。
  const blocks = stableGridOrder(input.page.blocks)
  for (const { item: block, index: blockIndex } of blocks) {
    if (!renderedBlockIDs.has(block.id)) continue
    const blockPath = `pages[${pageIndex}].blocks[${blockIndex}]`
    const blockRect = rectForBlock(block)
    let blockTranslateY = 0

    if (block.sticky?.enabled === true) {
      const placement = append({
        key: reportStickyPlacementKey('BLOCK', block.id),
        kind: 'BLOCK',
        id: block.id,
        path: `${blockPath}.sticky`,
        sticky: block.sticky,
        rect: blockRect,
        parentTranslateY: 0,
        page: input.page,
      })
      blockTranslateY = placement.enabled ? placement.translateY : 0
    }

    const components = stableGridOrder(block.components)
    for (const { item: component, index: componentIndex } of components) {
      if (!component.visible || !renderedComponentIDs.has(component.id) || component.sticky?.enabled !== true) continue
      append({
        key: reportStickyPlacementKey('COMPONENT', component.id, block.id),
        kind: 'COMPONENT',
        id: component.id,
        blockID: block.id,
        path: `${blockPath}.components[${componentIndex}].sticky`,
        sticky: component.sticky,
        rect: rectForComponent(blockRect, component),
        ownerRect: blockRect,
        parentTranslateY: blockTranslateY,
        page: input.page,
      })
    }
  }

  return { placements, issues }
}

/** 为渲染器提供不会被分块/组件同名碰撞的查找键。 */
export function reportStickyPlacementKey(kind: ReportStickyPlacementKind, id: string, blockID?: string): string {
  return kind === 'BLOCK' ? `BLOCK:${id}` : `COMPONENT:${blockID ?? ''}:${id}`
}

function placeCandidate(candidate: StickyCandidate, viewportTop: number, scale: number, pageRect: Rect, stackEntries: StackEntry[]): ReportStickyPlacement {
  const naturalTop = candidate.rect.top + candidate.parentTranslateY
  const fallbackZIndex = Number.isInteger(candidate.sticky.zIndex) ? candidate.sticky.zIndex : 0
  const base = {
    key: candidate.key,
    kind: candidate.kind,
    id: candidate.id,
    blockID: candidate.blockID,
    path: candidate.path,
    top: finiteOr(naturalTop, 0),
    maxTop: finiteOr(naturalTop, 0),
    containerTop: 0,
    containerBottom: finiteOr(pageRect.height, 0),
    translateY: 0,
    enabled: false,
    stuck: false,
    atBoundary: false,
    zIndex: fallbackZIndex,
  }

  const configFailure = validateCandidateConfig(candidate, viewportTop, scale)
  if (configFailure) return failedPlacement(base, candidate.key, configFailure)
  if (!validRect(pageRect) || !validRect(candidate.rect) || (candidate.ownerRect !== undefined && !validRect(candidate.ownerRect))) {
    return failedPlacement(base, candidate.key, { code: 'INVALID_GEOMETRY', reason: '冻结项或约束容器的布局几何无效，已回退普通文档流' })
  }

  const resolved = resolveConstraint(candidate, pageRect)
  if ('failure' in resolved) return failedPlacement(base, candidate.key, resolved.failure)
  const constraint = resolved.constraint
  const effectiveRect = { ...candidate.rect, top: naturalTop }
  const maxTop = constraint.top + constraint.height - effectiveRect.height
  const constrainedBase = {
    ...base,
    scopeKey: constraint.key,
    maxTop,
    containerTop: constraint.top,
    containerBottom: constraint.top + constraint.height,
    zIndex: candidate.sticky.zIndex,
  }

  if (!rectContains(constraint, effectiveRect)) {
    return failedPlacement(constrainedBase, constraint.key, { code: 'OUTSIDE_CONTAINER', reason: '冻结项超出约束容器，已回退普通文档流' })
  }

  const logicalOffset = candidate.sticky.top / scale
  // 先模拟单个 position:sticky 的底边钳制，再叠加同范围且横向相交的已放置项。
  const ownTop = Math.min(Math.max(naturalTop, constraint.top, viewportTop + logicalOffset), maxTop)
  const stackBottom = stackEntries.reduce((bottom, previous) => {
    if (previous.scopeKey !== constraint.key || !rangesOverlap(candidate.rect.left, candidate.rect.left + candidate.rect.width, previous.left, previous.right)) return bottom
    // 父分块的位移已计入子组件自然坐标，不能再把整个父分块当作同级冻结项重复堆叠。
    if (candidate.kind === 'COMPONENT' && previous.kind === 'BLOCK' && candidate.blockID === previous.id) return bottom
    return Math.max(bottom, previous.top + previous.height)
  }, constraint.top)
  const top = Math.max(ownTop, stackBottom)
  if (top > maxTop + EPSILON) {
    return failedPlacement(constrainedBase, constraint.key, { code: 'NO_CAPACITY', reason: '冻结范围剩余空间不足，已回退普通文档流' })
  }

  const translateY = Math.max(0, top - naturalTop)
  return {
    ...constrainedBase,
    top,
    translateY,
    enabled: true,
    stuck: translateY > EPSILON,
    atBoundary: Math.abs(top - maxTop) <= EPSILON,
  }
}

function validateCandidateConfig(candidate: StickyCandidate, viewportTop: number, scale: number): CandidateFailure | undefined {
  const sticky = candidate.sticky
  if (!Number.isInteger(sticky.top) || sticky.top < 0 || sticky.top > MAX_STICKY_TOP) {
    return { code: 'INVALID_TOP', path: `${candidate.path}.top`, reason: `冻结顶部偏移必须是 0 至 ${MAX_STICKY_TOP} 的整数 CSS 像素` }
  }
  if (!Number.isInteger(sticky.zIndex) || sticky.zIndex < 1 || sticky.zIndex > MAX_STICKY_Z_INDEX) {
    return { code: 'INVALID_Z_INDEX', path: `${candidate.path}.zIndex`, reason: `冻结层级必须是 1 至 ${MAX_STICKY_Z_INDEX} 的整数` }
  }
  const allowedScopes = candidate.kind === 'BLOCK' ? ['PAGE', 'CONTAINER'] : ['PAGE', 'BLOCK', 'CONTAINER']
  if (!allowedScopes.includes(sticky.scope)) {
    return { code: 'INVALID_SCOPE', path: `${candidate.path}.scope`, reason: `${candidate.kind === 'BLOCK' ? '分块' : '组件'}冻结范围无效` }
  }
  if (sticky.scope !== 'CONTAINER' && sticky.containerId !== undefined) {
    return { code: 'INVALID_CONTAINER', path: `${candidate.path}.containerId`, reason: '仅 CONTAINER 冻结范围允许设置容器标识' }
  }
  if (!Number.isFinite(scale) || scale <= 0) {
    return { code: 'INVALID_SCALE', reason: '画布缩放必须是大于 0 的有限数，冻结项已回退普通文档流' }
  }
  if (!Number.isFinite(viewportTop)) {
    return { code: 'INVALID_VIEWPORT', reason: '浏览窗口顶部坐标无效，冻结项已回退普通文档流' }
  }
  return undefined
}

function resolveConstraint(candidate: StickyCandidate, pageRect: Rect): { constraint: Constraint } | { failure: CandidateFailure } {
  const pageConstraint: Constraint = { ...pageRect, key: `PAGE:${candidate.page.id}` }
  const blockConstraint = candidate.ownerRect === undefined
    ? undefined
    : { ...candidate.ownerRect, top: candidate.ownerRect.top + candidate.parentTranslateY, key: `BLOCK:${candidate.blockID}` }

  if (candidate.sticky.scope === 'PAGE') return { constraint: pageConstraint }
  if (candidate.sticky.scope === 'BLOCK') {
    if (candidate.kind !== 'COMPONENT' || blockConstraint === undefined) {
      return { failure: { code: 'INVALID_SCOPE', path: `${candidate.path}.scope`, reason: '只有组件允许使用所属分块冻结范围' } }
    }
    return { constraint: blockConstraint }
  }

  const containerID = candidate.sticky.containerId
  if (typeof containerID !== 'string' || containerID.length === 0) {
    return { failure: { code: 'INVALID_CONTAINER', path: `${candidate.path}.containerId`, reason: 'CONTAINER 冻结范围必须指定容器标识' } }
  }
  if (candidate.kind === 'BLOCK') {
    return containerID === candidate.page.id
      ? { constraint: pageConstraint }
      : { failure: { code: 'INVALID_CONTAINER', path: `${candidate.path}.containerId`, reason: '分块只能引用所属页面作为冻结容器' } }
  }
  if (containerID === candidate.page.id && containerID === candidate.blockID) {
    return { failure: { code: 'INVALID_CONTAINER', path: `${candidate.path}.containerId`, reason: '冻结容器同时命中页面和所属分块，无法确定约束范围' } }
  }
  if (containerID === candidate.page.id) return { constraint: pageConstraint }
  if (containerID === candidate.blockID && blockConstraint !== undefined) return { constraint: blockConstraint }
  return { failure: { code: 'INVALID_CONTAINER', path: `${candidate.path}.containerId`, reason: '组件只能引用所属页面或所属分块作为冻结容器' } }
}

function failedPlacement(base: Omit<ReportStickyPlacement, 'scopeKey'> & { scopeKey?: string }, scopeKey: string, failure: CandidateFailure): ReportStickyPlacement {
  return {
    ...base,
    path: failure.path ?? base.path,
    scopeKey,
    reasonCode: failure.code,
    reason: failure.reason,
  }
}

function rectForBlock(block: ReportBlock): Rect {
  return {
    left: block.grid.x * LOGICAL_COLUMN_WIDTH,
    top: block.grid.y * LOGICAL_ROW_HEIGHT,
    width: block.grid.w * LOGICAL_COLUMN_WIDTH,
    height: block.grid.h * LOGICAL_ROW_HEIGHT,
  }
}

function rectForComponent(blockRect: Rect, component: ReportComponent): Rect {
  return {
    left: blockRect.left + component.grid.x * LOGICAL_INNER_COLUMN_WIDTH,
    top: blockRect.top + component.grid.y * LOGICAL_INNER_ROW_HEIGHT,
    width: component.grid.w * LOGICAL_INNER_COLUMN_WIDTH,
    height: component.grid.h * LOGICAL_INNER_ROW_HEIGHT,
  }
}

function stableGridOrder<T extends { grid: { x: number; y: number } }>(items: T[]): Array<{ item: T; index: number }> {
  return items
    .map((item, index) => ({ item, index }))
    .sort((left, right) => sortNumber(left.item.grid.y) - sortNumber(right.item.grid.y)
      || sortNumber(left.item.grid.x) - sortNumber(right.item.grid.x)
      || left.index - right.index)
}

function rectContains(container: Rect, item: Rect): boolean {
  return item.left >= container.left - EPSILON
    && item.top >= container.top - EPSILON
    && item.left + item.width <= container.left + container.width + EPSILON
    && item.top + item.height <= container.top + container.height + EPSILON
}

function rangesOverlap(leftStart: number, leftEnd: number, rightStart: number, rightEnd: number): boolean {
  return leftStart < rightEnd - EPSILON && leftEnd > rightStart + EPSILON
}

function validRect(rect: Rect): boolean {
  return Number.isFinite(rect.left)
    && Number.isFinite(rect.top)
    && Number.isFinite(rect.width)
    && Number.isFinite(rect.height)
    && rect.width > 0
    && rect.height > 0
}

function sortNumber(value: number): number {
  return Number.isFinite(value) ? value : Number.POSITIVE_INFINITY
}

function finiteOr(value: number, fallback: number): number {
  return Number.isFinite(value) ? value : fallback
}
