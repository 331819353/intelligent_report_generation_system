export type ReportRendererMode = 'designer' | 'viewer'

export type Grid = {
  x: number
  y: number
  w: number
  h: number
}

export type DisabledSticky = { enabled: false }

type EnabledStickyBase = {
  enabled: true
  top: number
  zIndex: number
}

type PageSticky = EnabledStickyBase & {
  scope: 'PAGE'
  /** 非容器范围显式禁止夹带容器标识，避免经变量结构赋值绕过判别联合。 */
  containerId?: never
}

type BlockScopeSticky = EnabledStickyBase & {
  scope: 'BLOCK'
  /** 非容器范围显式禁止夹带容器标识，避免经变量结构赋值绕过判别联合。 */
  containerId?: never
}

type ContainerSticky = EnabledStickyBase & {
  scope: 'CONTAINER'
  containerId: string
}

/** 分块只能相对页面或唯一命中的祖先容器冻结。 */
export type BlockSticky = DisabledSticky | PageSticky | ContainerSticky

/** 组件额外允许相对所属分块冻结。 */
export type ComponentSticky = DisabledSticky | PageSticky | BlockScopeSticky | ContainerSticky

/** 规划器处理分块与组件时使用的冻结配置总联合。 */
export type Sticky = BlockSticky | ComponentSticky

export type SourceTrace = {
  sourceType: string
  sourceId: string
  location?: string
  excerptHash?: string
  usage: string
}

export type ComponentType =
  | 'TITLE'
  | 'RICH_TEXT'
  | 'FILTER'
  | 'KPI'
  | 'ADDITIONAL_INFO'
  | 'TABLE'
  | 'CROSSTAB'
  | 'CHART'
  | 'RANKING'
  | 'IMAGE'
  | 'ATTACHMENT_LIST'
  | 'DATA_SOURCE'
  | 'UPDATED_AT'
  | 'CONCLUSION'
  | 'DIVIDER'
  | 'DECORATION'

export type ReportComponent = {
  id: string
  type: ComponentType
  name: string
  grid: Grid
  zIndex?: number
  visible: boolean
  manualLocked: boolean
  style?: Record<string, unknown>
  binding?: Record<string, unknown>
  interaction?: Record<string, unknown>
  sticky: ComponentSticky
  refreshPolicy?: Record<string, unknown>
  permissionPolicy?: Record<string, unknown>
  sourceTrace: SourceTrace[]
  conclusion?: Record<string, unknown>
  extensions?: Record<string, unknown>
}

export type ReportMenuRatios = {
  /** 第一行：Logo+总标题 / 功能区，默认 3:1。 */
  topColumns: [number, number]
  /** 第二行：全局筛选 / 导航区，默认 1:1。 */
  bottomColumns: [number, number]
  /** 上下两行，默认 2:1。 */
  rowHeights: [number, number]
}

export type ReportMenuLayout = {
  visible: boolean
  defaultRatios: ReportMenuRatios
  ratios: ReportMenuRatios
  usesDefaultRatios: boolean
  cells: {
    logoTitle: { visible: boolean; logoText: string; title: string; subtitle?: string }
    actions: { visible: boolean; items: string[] }
    globalFilters: { visible: boolean; parameterIds: string[] }
    navigation: { visible: boolean; items: Array<{ label: string; targetBlockId?: string }> }
  }
}

export type ReportContentArea = {
  visible: boolean
  componentIds: string[]
}

export type ReportContentLayout = {
  visible: boolean
  areas: {
    title: ReportContentArea
    conclusion: ReportContentArea
    components: ReportContentArea
  }
}

export type ReportBlock = {
  id: string
  kind?: 'MENU' | 'CONTENT'
  name?: string
  visible?: boolean
  grid: Grid
  innerGrid: { columns: number; rows: number }
  zIndex?: number
  locks: { layout: boolean; config: boolean; dataSnapshot: boolean }
  sticky: BlockSticky
  style?: Record<string, unknown>
  permissionPolicy?: Record<string, unknown>
  menuLayout?: ReportMenuLayout
  contentLayout?: ReportContentLayout
  components: ReportComponent[]
}

export type ReportPage = {
  id: string
  name: string
  order: number
  background?: Record<string, unknown>
  contentGridRows: number
  blocks: ReportBlock[]
}

export type ReportParameter = {
  id: string
  code: string
  name: string
  dataType: string
  required: boolean
  multiValue: boolean
  defaultValue?: unknown
  scope: 'REPORT' | 'PAGE'
  pageId?: string
  optionSource?: Record<string, unknown>
  semanticBinding?: {
    semanticFieldCode: string
    datasetFields: Array<{
      datasetVersionId: string
      fieldId: string
      datasetParameterCode: string
    }>
  }
}

export type ReportDocument = {
  schemaVersion: '1.0'
  report: {
    id?: string
    code: string
    name: string
    description?: string
    type: 'DASHBOARD' | 'REPORT'
    language: string
    status: 'DRAFT' | 'PUBLISHED' | 'ARCHIVED'
    visibility: 'PRIVATE' | 'TENANT' | 'PUBLIC'
    onlineEnabled: boolean
    pdfArchiveEnabled: boolean
    defaultRefreshPolicy: 'REALTIME' | 'CACHE' | 'MATERIALIZED' | 'SNAPSHOT'
    timezone: string
  }
  canvas: {
    logicalWidth: 1920
    viewportHeight: 1080
    gridColumns: 12
    viewportGridRows: 10
    contentGridRows: 'AUTO'
    minContentGridRows: 10
    innerGridMultiplier: 4
    scaleMode: 'FIT_WIDTH'
    verticalOverflow: 'SCROLL'
  }
  theme?: Record<string, unknown>
  parameters?: ReportParameter[]
  dataRequirements?: Array<Record<string, unknown>>
  pages: ReportPage[]
  generation?: Record<string, unknown>
  extensions?: Record<string, unknown>
}

export type ComponentRuntimeState = {
  status: 'READY' | 'LOADING' | 'ERROR'
  data?: unknown
  errorMessage?: string
  updatedAt?: string
}

/** 运行上下文只保存本次查看所需的数据和参数，不改变版本化报告定义。 */
export type ReportRuntimeContext = {
  parameters: Record<string, unknown>
  parameterOptions?: Record<string, {
    status: 'READY' | 'LOADING' | 'ERROR'
    options?: Array<{ label: string; value: string | number | boolean }>
    errorMessage?: string
  }>
  componentData: Record<string, ComponentRuntimeState>
  permissions?: string[]
  roleCodes?: string[]
}

export type ReportComponentInteractionEvent = {
  value: unknown
  label?: string
  drillLevel?: number
  drillDirection?: 'DOWN' | 'UP'
}

export type ReportValidationIssue = {
  path: string
  reason: string
}

export type ReportSelection =
  | { kind: 'BLOCK'; pageID: string; blockID: string }
  | { kind: 'COMPONENT'; pageID: string; blockID: string; componentID: string }
