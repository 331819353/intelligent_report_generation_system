/**
 * Report Definition 1.0 的前端权威镜像。
 *
 * 编辑器与运行页共用这一套类型：JSON 报告定义是编辑器和渲染器唯一的事实来源，
 * 因此两侧不允许各自声明「够用就行」的子集类型——那会让布局、组件配置和绑定
 * 在某一侧被静默丢弃。字段命名与 internal/report/model.go 一一对应。
 */

export type ReportType = 'REPORT' | 'DASHBOARD'
export type BindingMode = 'SEMANTIC_IR' | 'DATASET_FIELD'
export type BlockType = 'ANALYSIS_CARD' | 'KPI_GROUP' | 'CHART' | 'TABLE' | 'CONTENT' | 'FILTER'
export type ZoneType = 'HEADER' | 'FILTER' | 'INSIGHT' | 'CONTENT' | 'FOOTER'
export type ZoneHeightMode = 'AUTO' | 'FIXED' | 'FR' | 'HIDDEN'
export type OverflowPolicy = 'CLIP' | 'SCROLL' | 'EXPAND'
export type MobileHeightMode = 'AUTO' | 'FIXED' | 'ASPECT_RATIO'
export type MobileSlotMode = 'STACK' | 'CAROUSEL' | 'PRIMARY_ONLY' | 'COLLAPSE'
export type Orientation = 'HORIZONTAL' | 'VERTICAL'
export type NullPolicy = 'GAP' | 'ZERO' | 'HIDE'
export type MobileLegendMode = 'VISIBLE' | 'HIDDEN' | 'SCROLL'
export type BindingRole =
  | 'DIMENSION' | 'SERIES' | 'X_AXIS' | 'Y_AXIS' | 'VALUE'
  | 'CATEGORY' | 'TIME' | 'COLOR' | 'SIZE' | 'LABEL' | 'DETAIL' | 'TOOLTIP'

export type GridRect = { x: number; y: number; w: number; h: number }

export type DesktopCanvas = {
  designWidth: number
  columns: number
  baseCellWidth: number
  baseRowHeight: number
  gapX: number
  gapY: number
  paddingX: number
  paddingY: number
}

export type MobileCanvas = { columns: number; gapY: number; paddingX: number; paddingY: number }
export type Canvas = { desktop: DesktopCanvas; mobile: MobileCanvas }

/**
 * 服务端画布当前被冻结为 1920/24 栏。前端仍从定义里读取它而不是硬编码，
 * 只有在旧草稿缺字段时才回落到这份与 Go 端一致的默认值。
 */
export const defaultCanvas: Canvas = {
  desktop: {
    designWidth: 1920, columns: 24, baseCellWidth: 80, baseRowHeight: 54,
    gapX: 12, gapY: 12, paddingX: 24, paddingY: 24,
  },
  mobile: { columns: 1, gapY: 12, paddingX: 12, paddingY: 12 },
}

export type FieldBinding = { role: BindingRole; field: string }

export type DataBinding = {
  bindingMode: BindingMode
  dataContextId?: string
  dimensions?: FieldBinding[]
  measures?: FieldBinding[]
  semanticQueryRef?: Record<string, unknown>
}

export type ComponentOptions = {
  title?: string
  subtitle?: string
  showLegend?: boolean
  showLabel?: boolean
  smooth?: boolean
  orientation?: Orientation
  topN?: number
  colorPaletteRef?: string
  numberFormat?: string
  nullPolicy?: NullPolicy
  animation?: boolean
  mobileLegendMode?: MobileLegendMode
  richText?: string
  imageAssetId?: string
  insightRole?: string
  tablePageSize?: number
  cardVariant?: '01' | '02' | '03'
}

export type ReportComponent = {
  id: string
  templateRef: { type: string; version: string }
  dataBinding?: DataBinding
  options: ComponentOptions
}

export type Slot = { id: string; grid: GridRect; componentId?: string; cardKind?: string; mergedFrom?: string[] }

export type ZoneLayout = {
  heightMode: ZoneHeightMode
  minHeight: number
  maxHeight?: number
  fixedHeight?: number
  fr?: number
  columns: number
  rows: number
  overflow: OverflowPolicy
  emptyPriority: number
}

/**
 * 卡片内的一个区域。order 决定它在卡片中的上下位置。
 *
 * 顺序曾经借用 layout.emptyPriority 表达，而后者同时决定空区域腾出的高度优先
 * 分配给谁；两个使用方的排序方向相反，于是「先渲染」与「先获得空间」无法同时
 * 成立。两者现在各自独立。
 */
export type Zone = { id: string; order?: number; type: ZoneType; layout: ZoneLayout; slots: Slot[] }

export type MobileBlockLayout = {
  order: number
  visible: boolean
  heightMode: MobileHeightMode
  slotMode: MobileSlotMode
  fixedHeight?: number
  aspectRatio?: number
  primarySlotId?: string
}

export type BlockLayout = { desktop: GridRect; mobile: MobileBlockLayout }
export type Block = {
  id: string; type: BlockType; title?: string; cardKind?: string
  layoutIntent?: { span: number; minRows: number; narrativeAttach: string; manualOverride: boolean }
  layout: BlockLayout; zones: Zone[]
}
export type Section = {
  id: string; name: string; order: number; question?: string
  layoutIntent?: { mode: string }; blocks: Block[]
}
export type Page = { id: string; name: string; order: number; sections: Section[] }

export type DataContext = {
  id: string
  datasetId?: string
  datasetVersionId?: string
  alias?: string
  defaultParameters?: Array<Record<string, unknown>>
  queryPolicy?: { timeoutMs: number; maxRows: number; cacheTtlSeconds: number }
}

export type GlobalFilter = {
  id: string
  type:
    | 'SINGLE_SELECT' | 'MULTI_SELECT' | 'DATE' | 'DATE_RANGE' | 'RELATIVE_TIME'
    | 'NUMBER_RANGE' | 'SEARCH_SELECT' | 'PARAMETER_INPUT' | 'SELECT' | 'BOOLEAN'
  fieldRef: { dataContextId: string; field: string }
  scope: { type: 'REPORT' | 'PAGE' | 'SECTION' | 'BLOCK' | 'COMPONENT'; targetIds: string[] }
  defaultValue?: {
    mode?: string; unit?: string; offset?: number; values?: string[]
    minimum?: number; maximum?: number; boolean?: boolean
  }
}

export type Interaction = {
  id: string
  sourceComponentId: string
  event: 'CLICK' | 'SELECT' | 'BRUSH' | 'ZOOM'
  action: 'FILTER' | 'DRILL_DOWN' | 'HIGHLIGHT' | 'NAVIGATE_PAGE'
  targetComponentIds: string[]
  targetPageId?: string
  fieldMappings: Array<{ sourceField: string; targetField: string }>
}

export type RuntimePolicy = {
  refreshMode: 'MANUAL' | 'ON_OPEN' | 'INTERVAL'
  refreshIntervalSeconds: number
  maxConcurrentQueries: number
  componentTimeoutMs: number
  exportEnabled: boolean
  failureMode: 'PARTIAL' | 'STRICT'
}

export type ReportDefinition = {
  schemaVersion: string
  metadata: {
    id: string; code: string; name: string; description?: string
    reportType: ReportType; locale?: string
  }
  templateRef?: Record<string, unknown>
  themeRef?: Record<string, unknown>
  canvas?: Canvas
  dataContexts: DataContext[]
  globalFilters?: GlobalFilter[]
  pages: Page[]
  components: ReportComponent[]
  interactions?: Interaction[]
  runtimePolicy?: RuntimePolicy
  provenance?: Record<string, unknown>
}

export function canvasOf(definition: Pick<ReportDefinition, 'canvas'>): Canvas {
  const desktop = definition.canvas?.desktop
  const mobile = definition.canvas?.mobile
  return {
    desktop: desktop?.columns ? desktop : defaultCanvas.desktop,
    mobile: mobile?.columns ? mobile : defaultCanvas.mobile,
  }
}

export function orderedPages(definition: Pick<ReportDefinition, 'pages'>): Page[] {
  return definition.pages.slice().sort((left, right) => left.order - right.order)
}

export function orderedSections(page: Pick<Page, 'sections'>): Section[] {
  return page.sections.slice().sort((left, right) => left.order - right.order)
}

/** 报告结构里所有已放置的组件 ID，按章节 → 区块 → 区域 → 槽位的稳定顺序。 */
export function placedComponentIDs(page?: Pick<Page, 'sections'>): string[] {
  if (!page) return []
  return orderedSections(page).flatMap(section => section.blocks.flatMap(blockComponentIDs))
}

export function blockComponentIDs(block: Block): string[] {
  return block.zones.flatMap(zone => zone.slots
    .map(slot => slot.componentId)
    .filter((id): id is string => Boolean(id)))
}

export function findBlock(page: Page, blockId: string): { section: Section; block: Block } | undefined {
  for (const section of page.sections) {
    for (const block of section.blocks) {
      if (block.id === blockId) return { section, block }
    }
  }
  return undefined
}

export function findComponentBlock(page: Page, componentId: string): { section: Section; block: Block } | undefined {
  for (const section of page.sections) {
    for (const block of section.blocks) {
      if (blockComponentIDs(block).includes(componentId)) return { section, block }
    }
  }
  return undefined
}
