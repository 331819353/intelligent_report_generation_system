import type { BlockSticky, ComponentSticky, ComponentType, Grid, ReportBlock, ReportComponent, ReportDocument, ReportPage, ReportTemplate, ReportValidationIssue, Sticky } from './report-contract'
import { createReportComponentDefaults, isFormalReportComponentType, reportComponentCatalog } from './report-component-catalog'

export const LOGICAL_CANVAS_WIDTH = 1920
export const LOGICAL_VIEWPORT_HEIGHT = 1080
export const LOGICAL_COLUMN_WIDTH = 160
export const LOGICAL_ROW_HEIGHT = 108
export const INNER_GRID_MULTIPLIER = 4
export const LOGICAL_INNER_COLUMN_WIDTH = LOGICAL_COLUMN_WIDTH / INNER_GRID_MULTIPLIER
export const LOGICAL_INNER_ROW_HEIGHT = LOGICAL_ROW_HEIGHT / INNER_GRID_MULTIPLIER
export const MAX_EDITOR_CONTENT_ROWS = 1000
export const MAX_STICKY_TOP = 10000
export const MAX_STICKY_Z_INDEX = 100000

export type LayoutUpdateResult = {
  document?: ReportDocument
  issue?: ReportValidationIssue
  componentID?: string
  blockID?: string
  vacatedGrid?: Grid
}

export type EmptyGridCell = Pick<Grid, 'x' | 'y'>
export type BlockResetMode = 'CLEAR' | 'DELETE'

export type StickyStackItem = {
  id: string
  requestedTop: number
  height: number
  scopeKey: string
  containerTop: number
  containerBottom: number
}

export type StickyStackPlacement = {
  id: string
  top: number
  maxTop: number
  enabled: boolean
}

/** 根据分块底边派生页面内容行数，首屏始终至少保留 10 行。 */
export function deriveContentGridRows(page: Pick<ReportPage, 'blocks'>): number {
  return Math.max(10, ...page.blocks.map(block => block.grid.y + block.grid.h))
}

/**
 * 应用一次分块移动或缩放，并在写入草稿前拒绝越界、碰撞和无界长画板。
 * 返回新文档，避免直接修改作为版本事实来源的输入对象。
 */
export function updateBlockGrid(document: ReportDocument, pageID: string, blockID: string, candidate: Grid): LayoutUpdateResult {
  const pageIndex = document.pages.findIndex(page => page.id === pageID)
  if (pageIndex < 0) return { issue: { path: 'pages', reason: `页面 ${pageID} 不存在` } }
  const page = document.pages[pageIndex]
  const blockIndex = page.blocks.findIndex(block => block.id === blockID)
  if (blockIndex < 0) return { issue: { path: `pages[${pageIndex}].blocks`, reason: `分块 ${blockID} 不存在` } }
  const block = page.blocks[blockIndex]
  if (block.locks.layout) return { issue: { path: `pages[${pageIndex}].blocks[${blockIndex}].locks.layout`, reason: '分块布局已锁定' } }
  const grid = normalizeGrid(candidate)
  const path = `pages[${pageIndex}].blocks[${blockIndex}].grid`
  if (grid.x < 0 || grid.y < 0 || grid.w < 1 || grid.h < 1 || grid.x + grid.w > 12) {
    return { issue: { path, reason: '分块超出 12 列画板边界或尺寸无效' } }
  }
  if (grid.y + grid.h > MAX_EDITOR_CONTENT_ROWS) {
    return { issue: { path, reason: `编辑态画板最多允许 ${MAX_EDITOR_CONTENT_ROWS} 行` } }
  }
  const rowResize = resizeContentRow(document, pageIndex, blockIndex, grid, path)
  if (rowResize) return rowResize
  const collision = page.blocks.find(block => block.id !== blockID && gridsOverlap(grid, block.grid))
  if (collision) {
    const movingWithoutResize = grid.w === block.grid.w && grid.h === block.grid.h
    if (movingWithoutResize && collision.grid.w === block.grid.w && collision.grid.h === block.grid.h && !collision.locks.layout) {
      const nextBlocks = page.blocks.map(item => item.id === blockID
        ? { ...item, grid: { ...collision.grid } }
        : item.id === collision.id ? { ...item, grid: { ...block.grid } } : item)
      const nextPage = { ...page, blocks: nextBlocks, contentGridRows: deriveContentGridRows({ blocks: nextBlocks }) }
      return {
        document: {
          ...document,
          pages: document.pages.map((item, index) => index === pageIndex ? nextPage : item),
        },
      }
    }
    return { issue: { path, reason: `分块与 ${collision.id} 重叠；只有同尺寸分块可直接拖拽换位` } }
  }

  const nextInnerGrid = { columns: grid.w * INNER_GRID_MULTIPLIER, rows: grid.h * INNER_GRID_MULTIPLIER }
  const resizedComponents = resizeBlockComponents(block, nextInnerGrid)
  if (!resizedComponents) {
    return { issue: { path: `pages[${pageIndex}].blocks[${blockIndex}].components`, reason: '缩放后的内网格无法容纳现有组件，请先调整或删除组件' } }
  }
  const nextBlock = { ...block, grid, innerGrid: nextInnerGrid, components: resizedComponents }
  const nextBlocks = page.blocks.map((item, index) => index === blockIndex ? nextBlock : item)
  const nextPage = { ...page, blocks: nextBlocks, contentGridRows: deriveContentGridRows({ blocks: nextBlocks }) }
  return {
    document: {
      ...document,
      pages: document.pages.map((item, index) => index === pageIndex ? nextPage : item),
    },
  }
}

/** 在分块内更新组件坐标，统一执行锁定、边界和碰撞校验。 */
export function updateComponentGrid(document: ReportDocument, pageID: string, blockID: string, componentID: string, candidate: Grid): LayoutUpdateResult {
  return updateBlockComponents(document, pageID, blockID, (block, path) => {
    const componentIndex = block.components.findIndex(component => component.id === componentID)
    if (componentIndex < 0) return { issue: { path: `${path}.components`, reason: `组件 ${componentID} 不存在` } }
    const component = block.components[componentIndex]
    if (block.locks.layout || component.manualLocked) return { issue: { path: `${path}.components[${componentIndex}]`, reason: '组件布局已锁定' } }
    const grid = normalizeGrid(candidate)
    const gridPath = `${path}.components[${componentIndex}].grid`
    const issue = validateInnerGrid(grid, block.innerGrid, block.components, componentID, gridPath)
    if (issue) return { issue }
    return { components: block.components.map((item, index) => index === componentIndex ? { ...item, grid } : item) }
  })
}

/** 更新分块冻结配置，并把禁用态收敛为不携带残留参数的规范形式。 */
export function updateBlockSticky(document: ReportDocument, pageID: string, blockID: string, candidate: BlockSticky): LayoutUpdateResult {
  const pageIndex = document.pages.findIndex(page => page.id === pageID)
  if (pageIndex < 0) return { issue: { path: 'pages', reason: `页面 ${pageID} 不存在` } }
  const page = document.pages[pageIndex]
  const blockIndex = page.blocks.findIndex(block => block.id === blockID)
  if (blockIndex < 0) return { issue: { path: `pages[${pageIndex}].blocks`, reason: `分块 ${blockID} 不存在` } }
  const path = `pages[${pageIndex}].blocks[${blockIndex}]`
  const block = page.blocks[blockIndex]
  if (block.locks.config) return { issue: { path: `${path}.locks.config`, reason: '分块配置已锁定' } }
  const result = normalizeSticky(candidate, `${path}.sticky`, ['PAGE', 'CONTAINER'], [pageID])
  if (!result.sticky) return { issue: result.issue }
  const nextBlock = { ...block, sticky: result.sticky }
  const nextPage = { ...page, blocks: page.blocks.map((item, index) => index === blockIndex ? nextBlock : item) }
  return { document: { ...document, pages: document.pages.map((item, index) => index === pageIndex ? nextPage : item) } }
}

/** 更新分块的展示语义；菜单比例、区域显隐等属性与其他编辑一样进入统一历史。 */
export function updateBlockDefinition(document: ReportDocument, pageID: string, blockID: string, update: (block: ReportBlock) => ReportBlock): LayoutUpdateResult {
  const pageIndex = document.pages.findIndex(page => page.id === pageID)
  if (pageIndex < 0) return { issue: { path: 'pages', reason: `页面 ${pageID} 不存在` } }
  const page = document.pages[pageIndex]
  const blockIndex = page.blocks.findIndex(block => block.id === blockID)
  if (blockIndex < 0) return { issue: { path: `pages[${pageIndex}].blocks`, reason: `分块 ${blockID} 不存在` } }
  const block = page.blocks[blockIndex]
  if (block.locks.config) {
    return { issue: { path: `pages[${pageIndex}].blocks[${blockIndex}].locks.config`, reason: '分块配置已锁定' } }
  }
  const nextBlock = update(structuredClone(block))
  if (nextBlock.id !== block.id) {
    return { issue: { path: `pages[${pageIndex}].blocks[${blockIndex}].id`, reason: '分块标识不能通过属性面板修改' } }
  }
  const nextPage = { ...page, blocks: page.blocks.map((item, index) => index === blockIndex ? nextBlock : item) }
  return { document: { ...document, pages: document.pages.map((item, index) => index === pageIndex ? nextPage : item) } }
}

/** 更新报告级模板；模板只包含安全提示词文本和受限设计令牌。 */
export function updateReportTemplate(document: ReportDocument, template: ReportTemplate): LayoutUpdateResult {
  return { document: { ...document, template: structuredClone(template) } }
}

/** 更新组件冻结配置；CONTAINER 在 V1 中只能指向所属页面或所属分块。 */
export function updateComponentSticky(document: ReportDocument, pageID: string, blockID: string, componentID: string, candidate: ComponentSticky): LayoutUpdateResult {
  return updateBlockComponents(document, pageID, blockID, (block, path) => {
    const componentIndex = block.components.findIndex(component => component.id === componentID)
    if (componentIndex < 0) return { issue: { path: `${path}.components`, reason: `组件 ${componentID} 不存在` } }
    if (block.locks.config) return { issue: { path: `${path}.locks.config`, reason: '分块配置已锁定' } }
    const stickyPath = `${path}.components[${componentIndex}].sticky`
    const result = normalizeSticky(candidate, stickyPath, ['PAGE', 'BLOCK', 'CONTAINER'], [pageID, blockID])
    if (!result.sticky) return { issue: result.issue }
    return {
      components: block.components.map((component, index) => index === componentIndex ? { ...component, sticky: result.sticky! } : component),
    }
  })
}

/** 在指定内网格位置新增组件；目标被占用时按行向后寻找最近空位。 */
export function addComponent(document: ReportDocument, pageID: string, blockID: string, type: ComponentType, anchor: Pick<Grid, 'x' | 'y'>): LayoutUpdateResult {
  let createdID: string | undefined
  const result = updateBlockComponents(document, pageID, blockID, (block, path) => {
    if (block.locks.layout) return { issue: { path: `${path}.locks.layout`, reason: '分块布局已锁定' } }
    const prepared = prepareContentAreaPlacement(block, type, anchor)
    const grid = findAvailableGrid(prepared.candidate, block.innerGrid, prepared.components.map(component => component.grid))
    if (!grid) return { issue: { path: `${path}.components`, reason: '分块内没有足够空间放置该组件' } }
    createdID = nextComponentID(document, type)
    if (!isFormalReportComponentType(type)) return { issue: { path: `${path}.components`, reason: `组件类型 ${type} 尚未开放创建` } }
    const component: ReportComponent = {
      id: createdID,
      type,
      ...createReportComponentDefaults(type),
      grid,
      visible: true,
      manualLocked: false,
      sticky: { enabled: false },
      sourceTrace: [],
    }
    return { components: [...prepared.components, component] }
  })
  const nextDocument = result.document && createdID
    ? assignComponentToContentArea(result.document, pageID, blockID, createdID, type)
    : result.document
  return { ...result, document: nextDocument, componentID: nextDocument ? createdID : undefined }
}

/** 复制组件的配置和来源信息，并为副本分配新的组件标识和空闲网格。 */
export function duplicateComponent(document: ReportDocument, pageID: string, blockID: string, componentID: string): LayoutUpdateResult {
  let createdID: string | undefined
  let createdType: ComponentType | undefined
  const result = updateBlockComponents(document, pageID, blockID, (block, path) => {
    const componentIndex = block.components.findIndex(component => component.id === componentID)
    if (componentIndex < 0) return { issue: { path: `${path}.components`, reason: `组件 ${componentID} 不存在` } }
    if (block.locks.layout || block.components[componentIndex].manualLocked) {
      return { issue: { path: `${path}.components[${componentIndex}]`, reason: '组件布局已锁定' } }
    }
    const source = block.components[componentIndex]
    createdType = source.type
    const grid = findAvailableGrid({ ...source.grid, x: source.grid.x + 1, y: source.grid.y + 1 }, block.innerGrid, block.components.map(component => component.grid))
    if (!grid) return { issue: { path: `${path}.components`, reason: '分块内没有足够空间放置组件副本' } }
    createdID = nextComponentID(document, source.type)
    const copy = structuredClone(source)
    copy.id = createdID
    copy.name = `${source.name} 副本`
    copy.grid = grid
    copy.manualLocked = false
    return { components: [...block.components, copy] }
  })
  const nextDocument = result.document && createdID && createdType
    ? assignComponentToContentArea(result.document, pageID, blockID, createdID, createdType)
    : result.document
  return { ...result, document: nextDocument, componentID: nextDocument ? createdID : undefined }
}

/** 删除组件时尊重分块和组件布局锁，避免绕过设计器权限语义。 */
export function deleteComponent(document: ReportDocument, pageID: string, blockID: string, componentID: string): LayoutUpdateResult {
  const result = updateBlockComponents(document, pageID, blockID, (block, path) => {
    const componentIndex = block.components.findIndex(component => component.id === componentID)
    if (componentIndex < 0) return { issue: { path: `${path}.components`, reason: `组件 ${componentID} 不存在` } }
    if (block.locks.layout || block.components[componentIndex].manualLocked) {
      return { issue: { path: `${path}.components[${componentIndex}]`, reason: '组件布局已锁定' } }
    }
    if (block.kind === 'CONTENT' && block.components[componentIndex].type === 'TITLE') {
      const titleIDs = block.contentLayout?.areas.title.componentIds ?? []
      const remainingTitles = block.components.filter(component => component.id !== componentID && component.type === 'TITLE' && titleIDs.includes(component.id))
      if (remainingTitles.length === 0) {
        return { issue: { path: `${path}.components[${componentIndex}]`, reason: '标题区为必填区域，不能删除最后一个标题组件' } }
      }
    }
    return { components: block.components.filter(component => component.id !== componentID) }
  })
  if (!result.document) return result
  const next = structuredClone(result.document)
  const nextBlock = next.pages.find(page => page.id === pageID)?.blocks.find(block => block.id === blockID)
  if (nextBlock?.contentLayout) {
    Object.values(nextBlock.contentLayout.areas).forEach(area => {
      if (area) area.componentIds = area.componentIds.filter(id => id !== componentID)
    })
  }
  return { document: next }
}

/**
 * 清空和删除都会释放正式分块定义，差异由历史操作类型和后续审计记录表达。
 * 空白 1×1 基础单元保持为编辑态隐式状态，不写入版本化报告 JSON。
 */
export function resetBlock(document: ReportDocument, pageID: string, blockID: string): LayoutUpdateResult {
  const pageIndex = document.pages.findIndex(page => page.id === pageID)
  if (pageIndex < 0) return { issue: { path: 'pages', reason: `页面 ${pageID} 不存在` } }
  const page = document.pages[pageIndex]
  const blockIndex = page.blocks.findIndex(block => block.id === blockID)
  if (blockIndex < 0) return { issue: { path: `pages[${pageIndex}].blocks`, reason: `分块 ${blockID} 不存在` } }
  const block = page.blocks[blockIndex]
  if (block.locks.layout || block.locks.config || block.locks.dataSnapshot) {
    return { issue: { path: `pages[${pageIndex}].blocks[${blockIndex}].locks`, reason: '分块已锁定，不能清空或删除' } }
  }
  const nextBlocks = page.blocks.filter(item => item.id !== blockID)
  const nextPage = { ...page, blocks: nextBlocks, contentGridRows: deriveContentGridRows({ blocks: nextBlocks }) }
  return {
    document: { ...document, pages: document.pages.map((item, index) => index === pageIndex ? nextPage : item) },
    vacatedGrid: { ...block.grid },
  }
}

/** 选中空白锚点时创建默认 4×3 内容分块，并自动放入必填标题区。 */
export function createBlockAtCell(document: ReportDocument, pageID: string, cell: EmptyGridCell): LayoutUpdateResult {
  const pageIndex = document.pages.findIndex(page => page.id === pageID)
  if (pageIndex < 0) return { issue: { path: 'pages', reason: `页面 ${pageID} 不存在` } }
  const grid = normalizeGrid({ x: cell.x, y: cell.y, w: 4, h: 3 })
  if (grid.x < 0 || grid.x + grid.w > 12 || grid.y < 0 || grid.y + grid.h > MAX_EDITOR_CONTENT_ROWS) {
    return { issue: { path: `pages[${pageIndex}].blocks`, reason: '默认 4×3 分块超出编辑态画板边界' } }
  }
  const page = document.pages[pageIndex]
  const occupied = page.blocks.find(block => gridsOverlap(grid, block.grid))
  if (occupied) return { issue: { path: `pages[${pageIndex}].blocks`, reason: `默认分块区域已被 ${occupied.id} 占用` } }
  const blockID = nextBlockID(document)
  const titleID = nextComponentID(document, 'TITLE')
  const block: ReportDocument['pages'][number]['blocks'][number] = {
    id: blockID,
    kind: 'CONTENT',
    name: '新建内容分块',
    visible: true,
    grid,
    innerGrid: { columns: grid.w * INNER_GRID_MULTIPLIER, rows: grid.h * INNER_GRID_MULTIPLIER },
    locks: { layout: false, config: false, dataSnapshot: false },
    sticky: { enabled: false },
    style: { padding: 2, background: 'WHITE', radius: 14, shadow: 'SOFT' },
    contentLayout: {
      visible: true,
      areas: {
        title: { visible: true, componentIds: [titleID] },
        filter: { visible: false, componentIds: [] },
        conclusion: { visible: false, componentIds: [] },
        chart: { visible: false, componentIds: [] },
      },
    },
    components: [{
      id: titleID,
      type: 'TITLE',
      ...createReportComponentDefaults('TITLE'),
      name: '分块标题',
      grid: { x: 0, y: 0, w: 16, h: 3 },
      visible: true,
      manualLocked: false,
      sticky: { enabled: false },
      sourceTrace: [],
    }],
  }
  const nextBlocks = [...page.blocks, block]
  const nextPage = { ...page, blocks: nextBlocks, contentGridRows: deriveContentGridRows({ blocks: nextBlocks }) }
  return {
    document: { ...document, pages: document.pages.map((item, index) => index === pageIndex ? nextPage : item) },
    blockID,
  }
}

/** 组件落入空白锚点时原子创建默认 4×3 分块和目标组件。 */
export function createBlockWithComponent(document: ReportDocument, pageID: string, cell: EmptyGridCell, type: ComponentType): LayoutUpdateResult {
  const blockResult = createBlockAtCell(document, pageID, cell)
  if (!blockResult.document || !blockResult.blockID) return blockResult
  if (type === 'TITLE') {
    const componentID = blockResult.document.pages.find(page => page.id === pageID)?.blocks.find(block => block.id === blockResult.blockID)?.components[0]?.id
    return { ...blockResult, componentID }
  }
  const componentResult = addComponent(blockResult.document, pageID, blockResult.blockID, type, { x: 0, y: 0 })
  return {
    ...componentResult,
    blockID: blockResult.blockID,
  }
}

/** 派生 4×3 默认分块的可用锚点。 */
export function deriveEmptyGridCells(blocks: ReportPage['blocks'], contentRows: number): EmptyGridCell[] {
  const rows = Math.min(MAX_EDITOR_CONTENT_ROWS, Math.max(10, Math.round(finiteOr(contentRows, 10))))
  const cells: EmptyGridCell[] = []
  for (let y = 0; y <= rows - 3; y += 3) {
    for (let x = 0; x <= 8; x += 4) {
      const candidate = { x, y, w: 4, h: 3 }
      if (!blocks.some(block => gridsOverlap(candidate, block.grid))) {
        cells.push({ x, y })
      }
    }
  }
  return cells
}

/** 把指针像素位移吸附为主网格整数位移。 */
export function pointerDeltaToGrid(deltaX: number, deltaY: number, scale: number): { columns: number; rows: number } {
  const safeScale = scale > 0 ? scale : 1
  return {
    columns: Math.round(finiteOr(deltaX, 0) / (LOGICAL_COLUMN_WIDTH * safeScale)),
    rows: Math.round(finiteOr(deltaY, 0) / (LOGICAL_ROW_HEIGHT * safeScale)),
  }
}

/** 把指针在分块中的相对位置换算为 4 倍内网格坐标。 */
export function pointerPositionToInnerGrid(offsetX: number, offsetY: number, scale: number): Pick<Grid, 'x' | 'y'> {
  const safeScale = scale > 0 ? scale : 1
  return {
    x: Math.max(0, Math.floor(finiteOr(offsetX, 0) / (LOGICAL_INNER_COLUMN_WIDTH * safeScale))),
    y: Math.max(0, Math.floor(finiteOr(offsetY, 0) / (LOGICAL_INNER_ROW_HEIGHT * safeScale))),
  }
}

/** 把组件指针位移吸附为 4 倍内网格整数位移。 */
export function pointerDeltaToInnerGrid(deltaX: number, deltaY: number, scale: number): { columns: number; rows: number } {
  const safeScale = scale > 0 ? scale : 1
  return {
    columns: Math.round(finiteOr(deltaX, 0) / (LOGICAL_INNER_COLUMN_WIDTH * safeScale)),
    rows: Math.round(finiteOr(deltaY, 0) / (LOGICAL_INNER_ROW_HEIGHT * safeScale)),
  }
}

/** 画布只缩小不放大，保持逻辑像素与导出基准一致。 */
export function calculateCanvasScale(viewportWidth: number): number {
  if (!Number.isFinite(viewportWidth) || viewportWidth <= 0) return 1
  return Math.min(1, viewportWidth / LOGICAL_CANVAS_WIDTH)
}

/**
 * 计算同一冻结范围内的顶部堆叠位置和容器停止边界。
 * 容器空间不足时返回 enabled=false，由在线态和打印态统一回退到普通文档流。
 */
export function calculateStickyStack(items: StickyStackItem[]): StickyStackPlacement[] {
  const scopeBottom = new Map<string, number>()
  return items.map(item => {
    const containerTop = finiteOr(item.containerTop, 0)
    const containerBottom = Math.max(containerTop, finiteOr(item.containerBottom, containerTop))
    const height = Math.max(0, finiteOr(item.height, 0))
    const maxTop = Math.max(containerTop, containerBottom - height)
    const top = Math.max(containerTop, finiteOr(item.requestedTop, 0), scopeBottom.get(item.scopeKey) ?? containerTop)
    const enabled = height > 0 && top <= maxTop
    if (enabled) scopeBottom.set(item.scopeKey, top + height)
    return { id: item.id, top: Math.min(top, maxTop), maxTop, enabled }
  })
}

export function gridsOverlap(left: Grid, right: Grid): boolean {
  return left.x < right.x + right.w && left.x + left.w > right.x && left.y < right.y + right.h && left.y + left.h > right.y
}

function normalizeGrid(grid: Grid): Grid {
  return {
    x: Math.round(finiteOr(grid.x, -1)),
    y: Math.round(finiteOr(grid.y, -1)),
    w: Math.round(finiteOr(grid.w, 0)),
    h: Math.round(finiteOr(grid.h, 0)),
  }
}

/**
 * 内容分块缩放按整行联动：同一行高度始终一致，横向缩放由相邻分块补偿，
 * 因此完整行仍严格占满 12 列（1920 px）。
 */
function resizeContentRow(document: ReportDocument, pageIndex: number, blockIndex: number, candidate: Grid, path: string): LayoutUpdateResult | undefined {
  const page = document.pages[pageIndex]
  const block = page.blocks[blockIndex]
  const resizing = candidate.x === block.grid.x && candidate.y === block.grid.y
    && (candidate.w !== block.grid.w || candidate.h !== block.grid.h)
  if (!resizing || block.kind !== 'CONTENT') return undefined

  const row = page.blocks
    .map((item, index) => ({ item, index }))
    .filter(entry => entry.item.kind === 'CONTENT' && entry.item.grid.y === block.grid.y && entry.item.grid.h === block.grid.h)
    .sort((left, right) => left.item.grid.x - right.item.grid.x)
  if (row.some(entry => entry.index !== blockIndex && entry.item.locks.layout)) {
    return { issue: { path, reason: '同一行存在已锁定分块，不能联动调整整行尺寸' } }
  }

  const grids = new Map<number, Grid>(row.map(entry => [entry.index, { ...entry.item.grid }]))
  const targetPosition = row.findIndex(entry => entry.index === blockIndex)
  const targetGrid = { ...candidate }
  if (candidate.w !== block.grid.w && row.length > 1) {
    if (targetPosition < row.length - 1) {
      const neighbor = row[targetPosition + 1]
      const neighborRight = neighbor.item.grid.x + neighbor.item.grid.w
      const neighborWidth = neighborRight - (candidate.x + candidate.w)
      if (neighborWidth < 1) return { issue: { path, reason: '右侧分块已达到最小宽度，不能继续放大' } }
      grids.set(neighbor.index, { ...neighbor.item.grid, x: candidate.x + candidate.w, w: neighborWidth })
    } else {
      const neighbor = row[targetPosition - 1]
      const targetRight = block.grid.x + block.grid.w
      targetGrid.x = targetRight - candidate.w
      const neighborWidth = targetGrid.x - neighbor.item.grid.x
      if (neighborWidth < 1 || targetGrid.x < 0) return { issue: { path, reason: '左侧分块已达到最小宽度，不能继续放大' } }
      grids.set(neighbor.index, { ...neighbor.item.grid, w: neighborWidth })
    }
  } else if (row.length === 1) {
    targetGrid.x = 0
    targetGrid.w = 12
  }
  grids.set(blockIndex, targetGrid)

  const heightDelta = candidate.h - block.grid.h
  if (heightDelta !== 0) {
    row.forEach(entry => grids.set(entry.index, { ...grids.get(entry.index)!, h: candidate.h }))
  }

  const nextBlocks: ReportBlock[] = []
  for (let index = 0; index < page.blocks.length; index += 1) {
    const source = page.blocks[index]
    let grid = grids.get(index) ?? { ...source.grid }
    if (heightDelta !== 0 && source.grid.y >= block.grid.y + block.grid.h) {
      grid = { ...grid, y: grid.y + heightDelta }
    }
    if (grid.x < 0 || grid.y < 0 || grid.w < 1 || grid.h < 1 || grid.x + grid.w > 12 || grid.y + grid.h > MAX_EDITOR_CONTENT_ROWS) {
      return { issue: { path, reason: '整行联动调整后有分块超出画板边界' } }
    }
    const nextInnerGrid = { columns: grid.w * INNER_GRID_MULTIPLIER, rows: grid.h * INNER_GRID_MULTIPLIER }
    const components = resizeBlockComponents(source, nextInnerGrid)
    if (!components) return { issue: { path, reason: `调整后的 ${source.id} 无法容纳现有组件` } }
    nextBlocks.push({ ...source, grid, innerGrid: nextInnerGrid, components })
  }
  for (let index = 0; index < nextBlocks.length; index += 1) {
    const collision = nextBlocks.slice(0, index).find(item => gridsOverlap(item.grid, nextBlocks[index].grid))
    if (collision) return { issue: { path, reason: `整行联动调整后与 ${collision.id} 重叠` } }
  }
  const nextPage = { ...page, blocks: nextBlocks, contentGridRows: deriveContentGridRows({ blocks: nextBlocks }) }
  return { document: { ...document, pages: document.pages.map((item, index) => index === pageIndex ? nextPage : item) } }
}

function assignComponentToContentArea(document: ReportDocument, pageID: string, blockID: string, componentID: string, type: ComponentType): ReportDocument {
  const next = structuredClone(document)
  const block = next.pages.find(page => page.id === pageID)?.blocks.find(item => item.id === blockID)
  if (!block?.contentLayout) return next
  const areaName = type === 'TITLE' ? 'title' : type === 'FILTER' ? 'filter' : type === 'CONCLUSION' ? 'conclusion' : 'chart'
  const area = block.contentLayout.areas[areaName] ?? { visible: false, componentIds: [] }
  area.componentIds = [...area.componentIds, componentID]
  area.visible = true
  block.contentLayout.areas[areaName] = area
  return next
}

function normalizeSticky(candidate: BlockSticky, path: string, scopes: readonly ['PAGE', 'CONTAINER'], ancestorIDs: string[]): { sticky?: BlockSticky; issue?: ReportValidationIssue }
function normalizeSticky(candidate: ComponentSticky, path: string, scopes: readonly ['PAGE', 'BLOCK', 'CONTAINER'], ancestorIDs: string[]): { sticky?: ComponentSticky; issue?: ReportValidationIssue }
function normalizeSticky(candidate: Sticky, path: string, scopes: readonly StickyScope[], ancestorIDs: string[]): { sticky?: Sticky; issue?: ReportValidationIssue } {
  if (!candidate.enabled) return { sticky: { enabled: false } }
  if (!Number.isInteger(candidate.top) || candidate.top < 0 || candidate.top > MAX_STICKY_TOP) {
    return { issue: { path: `${path}.top`, reason: `顶部偏移必须是 0 到 ${MAX_STICKY_TOP} 的整数` } }
  }
  if (!scopes.includes(candidate.scope)) {
    return { issue: { path: `${path}.scope`, reason: `冻结作用域 ${candidate.scope} 不适用于当前目标` } }
  }
  if (!Number.isInteger(candidate.zIndex) || candidate.zIndex < 1 || candidate.zIndex > MAX_STICKY_Z_INDEX) {
    return { issue: { path: `${path}.zIndex`, reason: `冻结层级必须是 1 到 ${MAX_STICKY_Z_INDEX} 的整数` } }
  }
  if (candidate.scope === 'CONTAINER') {
    // 组件的页面与分块允许同名；容器标识必须且只能命中一个祖先，歧义时失败关闭。
    const ancestorMatchCount = ancestorIDs.filter(ancestorID => ancestorID === candidate.containerId).length
    if (!candidate.containerId || ancestorMatchCount !== 1) {
      return { issue: { path: `${path}.containerId`, reason: '冻结容器必须唯一命中当前目标的同页祖先' } }
    }
    return { sticky: { enabled: true, top: candidate.top, scope: candidate.scope, containerId: candidate.containerId, zIndex: candidate.zIndex } }
  }
  return { sticky: { enabled: true, top: candidate.top, scope: candidate.scope, zIndex: candidate.zIndex } }
}

type StickyScope = 'PAGE' | 'BLOCK' | 'CONTAINER'

type ComponentMutationResult = { components?: ReportComponent[]; issue?: ReportValidationIssue }

function updateBlockComponents(document: ReportDocument, pageID: string, blockID: string, mutate: (block: ReportDocument['pages'][number]['blocks'][number], path: string) => ComponentMutationResult): LayoutUpdateResult {
  const pageIndex = document.pages.findIndex(page => page.id === pageID)
  if (pageIndex < 0) return { issue: { path: 'pages', reason: `页面 ${pageID} 不存在` } }
  const blockIndex = document.pages[pageIndex].blocks.findIndex(block => block.id === blockID)
  if (blockIndex < 0) return { issue: { path: `pages[${pageIndex}].blocks`, reason: `分块 ${blockID} 不存在` } }
  const path = `pages[${pageIndex}].blocks[${blockIndex}]`
  const result = mutate(document.pages[pageIndex].blocks[blockIndex], path)
  if (!result.components) return { issue: result.issue }
  const nextBlock = { ...document.pages[pageIndex].blocks[blockIndex], components: result.components }
  const nextPage = { ...document.pages[pageIndex], blocks: document.pages[pageIndex].blocks.map((block, index) => index === blockIndex ? nextBlock : block) }
  return { document: { ...document, pages: document.pages.map((page, index) => index === pageIndex ? nextPage : page) } }
}

function validateInnerGrid(grid: Grid, bounds: { columns: number; rows: number }, components: ReportComponent[], excludedID: string, path: string): ReportValidationIssue | undefined {
  if (grid.x < 0 || grid.y < 0 || grid.w < 1 || grid.h < 1 || grid.x + grid.w > bounds.columns || grid.y + grid.h > bounds.rows) {
    return { path, reason: `组件超出 ${bounds.columns}×${bounds.rows} 内网格边界或尺寸无效` }
  }
  const component = components.find(item => item.id === excludedID)
  if (component && isFormalReportComponentType(component.type)) {
    const minimum = reportComponentCatalog[component.type].minimumGrid
    if (grid.w < minimum.w || grid.h < minimum.h) {
      return { path, reason: `${reportComponentCatalog[component.type].label}组件最小尺寸为 ${minimum.w}×${minimum.h}` }
    }
  }
  const collision = components.find(component => component.id !== excludedID && gridsOverlap(grid, component.grid))
  return collision ? { path, reason: `组件与 ${collision.id} 重叠，已取消本次调整` } : undefined
}

function resizeBlockComponents(block: ReportBlock, next: { columns: number; rows: number }): ReportComponent[] | undefined {
  if (block.kind !== 'CONTENT' || !block.contentLayout) {
    return resizeComponentsToInnerGrid(block.components, block.innerGrid, next)
  }
  const supportedTypes = new Set<ComponentType>(['TITLE', 'FILTER', 'CHART', 'CONCLUSION'])
  const typeCounts = new Map<ComponentType, number>()
  block.components.forEach(component => typeCounts.set(component.type, (typeCounts.get(component.type) ?? 0) + 1))
  const canUseSemanticLayout = block.components.every(component => supportedTypes.has(component.type))
    && [...typeCounts.values()].every(count => count <= 1)
  if (!canUseSemanticLayout) return resizeComponentsToInnerGrid(block.components, block.innerGrid, next)

  const headerHeight = Math.min(3, next.rows)
  const bodyHeight = next.rows - headerHeight
  const hasFilter = (typeCounts.get('FILTER') ?? 0) > 0
  const hasChart = (typeCounts.get('CHART') ?? 0) > 0
  const hasConclusion = (typeCounts.get('CONCLUSION') ?? 0) > 0
  const sideWidth = Math.min(Math.max(4, Math.round(next.columns * 0.375)), Math.max(4, next.columns - 4))
  const mainWidth = next.columns - sideWidth

  const components = block.components.map(component => {
    if (component.type === 'TITLE') return { ...component, grid: { x: 0, y: 0, w: hasFilter ? mainWidth : next.columns, h: headerHeight } }
    if (component.type === 'FILTER') return { ...component, grid: { x: mainWidth, y: 0, w: sideWidth, h: headerHeight } }
    if (component.type === 'CHART') return { ...component, grid: { x: 0, y: headerHeight, w: hasConclusion ? mainWidth : next.columns, h: bodyHeight } }
    return { ...component, grid: { x: hasChart ? mainWidth : 0, y: headerHeight, w: hasChart ? sideWidth : next.columns, h: bodyHeight } }
  })
  for (const component of components) {
    const minimum = isFormalReportComponentType(component.type) ? reportComponentCatalog[component.type].minimumGrid : { w: 1, h: 1 }
    if (component.grid.w < minimum.w || component.grid.h < minimum.h) return undefined
  }
  return components
}

function resizeComponentsToInnerGrid(components: ReportComponent[], previous: { columns: number; rows: number }, next: { columns: number; rows: number }): ReportComponent[] | undefined {
  if (previous.columns === next.columns && previous.rows === next.rows) return components
  const placed: ReportComponent[] = []
  for (const component of components) {
    const minimum = isFormalReportComponentType(component.type) ? reportComponentCatalog[component.type].minimumGrid : { w: 1, h: 1 }
    const desired = {
      x: Math.floor(component.grid.x * next.columns / previous.columns),
      y: Math.floor(component.grid.y * next.rows / previous.rows),
      w: Math.max(minimum.w, Math.round(component.grid.w * next.columns / previous.columns)),
      h: Math.max(minimum.h, Math.round(component.grid.h * next.rows / previous.rows)),
    }
    const grid = findAvailableGrid(desired, next, placed.map(item => item.grid))
    if (!grid) return undefined
    placed.push({ ...component, grid })
  }
  return placed
}

function findAvailableGrid(candidate: Grid, bounds: { columns: number; rows: number }, occupied: Grid[]): Grid | undefined {
  const width = Math.min(Math.max(1, Math.round(finiteOr(candidate.w, 1))), bounds.columns)
  const height = Math.min(Math.max(1, Math.round(finiteOr(candidate.h, 1))), bounds.rows)
  const startX = Math.min(Math.max(0, Math.round(finiteOr(candidate.x, 0))), bounds.columns - width)
  const startY = Math.min(Math.max(0, Math.round(finiteOr(candidate.y, 0))), bounds.rows - height)
  const preferred = { x: startX, y: startY, w: width, h: height }
  if (!occupied.some(grid => gridsOverlap(preferred, grid))) return preferred
  for (let y = 0; y <= bounds.rows - height; y += 1) {
    for (let x = 0; x <= bounds.columns - width; x += 1) {
      const grid = { x, y, w: width, h: height }
      if (!occupied.some(item => gridsOverlap(grid, item))) return grid
    }
  }
  return undefined
}

function defaultComponentSize(type: ComponentType, bounds: { columns: number; rows: number }): Pick<Grid, 'w' | 'h'> {
  const size = isFormalReportComponentType(type) ? reportComponentCatalog[type].defaultGrid : { w: 12, h: 8 }
  return { w: Math.min(size.w, bounds.columns), h: Math.min(size.h, bounds.rows) }
}

function prepareContentAreaPlacement(block: ReportBlock, type: ComponentType, anchor: Pick<Grid, 'x' | 'y'>): { components: ReportComponent[]; candidate: Grid } {
  const components = structuredClone(block.components)
  if (block.kind !== 'CONTENT' || !block.contentLayout || block.innerGrid.rows < 7) {
    return { components, candidate: { ...anchor, ...defaultComponentSize(type, block.innerGrid) } }
  }
  const columns = block.innerGrid.columns
  const rows = block.innerGrid.rows
  const headerHeight = Math.min(3, rows)
  const sideWidth = Math.min(6, Math.max(4, Math.floor(columns * 0.375)))
  const mainWidth = columns - sideWidth
  const bodyHeight = rows - headerHeight
  const areas = block.contentLayout.areas

  if (type === 'FILTER') {
    const titleIDs = new Set(areas.title.componentIds)
    components.forEach(component => {
      if (titleIDs.has(component.id)) component.grid = { ...component.grid, x: 0, y: 0, w: mainWidth, h: headerHeight }
    })
    return { components, candidate: { x: mainWidth, y: 0, w: sideWidth, h: headerHeight } }
  }
  if (type === 'CHART') {
    const conclusionIDs = new Set(areas.conclusion?.componentIds ?? [])
    const hasConclusion = components.some(component => conclusionIDs.has(component.id))
    if (hasConclusion) {
      components.forEach(component => {
        if (conclusionIDs.has(component.id)) component.grid = { ...component.grid, x: mainWidth, y: headerHeight, w: sideWidth, h: bodyHeight }
      })
    }
    return { components, candidate: { x: 0, y: headerHeight, w: hasConclusion ? mainWidth : columns, h: bodyHeight } }
  }
  if (type === 'CONCLUSION') {
    const chartIDs = new Set([...(areas.chart?.componentIds ?? []), ...(areas.components?.componentIds ?? [])])
    const hasChart = components.some(component => chartIDs.has(component.id))
    if (hasChart) {
      components.forEach(component => {
        if (chartIDs.has(component.id)) component.grid = { ...component.grid, x: 0, y: headerHeight, w: mainWidth, h: bodyHeight }
      })
    }
    return { components, candidate: { x: hasChart ? mainWidth : 0, y: headerHeight, w: hasChart ? sideWidth : columns, h: bodyHeight } }
  }
  return { components, candidate: { ...anchor, ...defaultComponentSize(type, block.innerGrid) } }
}

function nextComponentID(document: ReportDocument, type: ComponentType): string {
  const prefix = `component_${type.toLowerCase()}`
  const ids = new Set(document.pages.flatMap(page => page.blocks.flatMap(block => block.components.map(component => component.id))))
  let sequence = 1
  while (ids.has(`${prefix}_${sequence}`)) sequence += 1
  return `${prefix}_${sequence}`
}

function nextBlockID(document: ReportDocument): string {
  const ids = new Set(document.pages.flatMap(page => page.blocks.map(block => block.id)))
  let sequence = 1
  while (ids.has(`block_empty_${sequence}`)) sequence += 1
  return `block_empty_${sequence}`
}

function finiteOr(value: number, fallback: number): number {
  return Number.isFinite(value) ? value : fallback
}
