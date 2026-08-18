import type { DataContextField, EditorOperation, EditorOperationBundle } from '../api/editor.ts'
import type { ComponentManifest, ManifestIndex } from '../render/manifests.ts'
import { minimumSize, recommendedSize } from '../render/manifests.ts'
import {
  canvasOf, findBlock, findComponentBlock, orderedSections,
  type BlockType, type ComponentOptions, type FieldBinding, type GlobalFilter,
  type Block, type GridRect, type Page, type ReportComponent, type ReportDefinition, type Section, type Zone,
} from '../render/schema.ts'
import { findFreeRect, resolveLayout, resolveSlotPlacement } from './placement.ts'

/**
 * 编辑器把每一次画布操作翻译成 Report Operation v1 受控指令。
 *
 * 拖拽、缩放、加组件、改绑定全部走同一条 POST /operations 通道，因此人工编辑
 * 与 AI 改稿写入的是同一种指令、同一份 Definition、同一条修订链。
 */

/** 组件面板拖拽载荷的 MIME 类型；画布按它识别“从组件面板拖来的卡片”。 */
export const paletteDragType = 'application/x-report-component'

export function manifestRef(manifest: ComponentManifest) {
  return `${manifest.type}@${manifest.version}`
}

export function bundle(reportId: string, baseRevision: number, operations: EditorOperation[]): EditorOperationBundle {
  return { schemaVersion: '1.0', reportId, baseRevision, source: 'USER', aiRunId: null, scope: null, operations }
}

/** 拖拽/缩放：BLOCK_MOVE 与 BLOCK_RESIZE 已是服务端协议的一部分，无需新增指令。 */
export function layoutOperations(
  section: Section,
  blockId: string,
  rect: GridRect,
  columns: number,
  minimum: { w: number; h: number },
): EditorOperation[] {
  return resolveLayout(section.blocks, blockId, rect, columns, minimum).flatMap(change => {
    const operations: EditorOperation[] = []
    if (change.resized) {
      operations.push({ op: 'BLOCK_RESIZE', targetId: change.blockId, payload: { w: change.rect.w, h: change.rect.h } })
    }
    if (change.moved) {
      operations.push({ op: 'BLOCK_MOVE', targetId: change.blockId, payload: { x: change.rect.x, y: change.rect.y } })
    }
    return operations
  })
}

const blockTypes: Record<ComponentManifest['category'], BlockType> = {
  CHART: 'CHART', TABLE: 'TABLE', CONTENT: 'CONTENT', CONTROL: 'FILTER',
}

const dimensionPreference = ['CATEGORY', 'X_AXIS', 'DIMENSION', 'SERIES', 'TIME'] as const
const measurePreference = ['VALUE', 'Y_AXIS', 'SIZE'] as const

export function preferredRole(manifest: ComponentManifest, dimension: boolean): FieldBinding['role'] {
  const candidates = dimension ? dimensionPreference : measurePreference
  const match = candidates.find(role => manifest.dataContract.roles.includes(role))
  return match ?? (dimension ? 'DIMENSION' : 'VALUE')
}

/**
 * 一张卡片（Block）内部由若干区域（Zone）纵向排布，每个区域是自己的子网格。
 * 区域类型表达它在卡片中的角色：内容、洞察结论、卡片内筛选、页眉页脚。
 * 把图表与结论放进同一张卡片，它们才会作为一个整体被拖动、缩放与投影到移动端。
 */
export type ZoneKind = 'CONTENT' | 'INSIGHT' | 'FILTER' | 'HEADER' | 'FOOTER'

export const zoneKindLabels: Record<ZoneKind, string> = {
  CONTENT: '内容区',
  INSIGHT: '结论区',
  FILTER: '卡片内筛选',
  HEADER: '卡片页眉',
  FOOTER: '卡片页脚',
}

/** 结论与页眉页脚按内容高度自适应，内容区占据卡片剩余空间。 */
function zoneLayoutFor(kind: ZoneKind, columns: number, rows: number): Zone['layout'] {
  if (kind === 'CONTENT') {
    return { heightMode: 'FR', minHeight: 1, fr: 1, columns, rows, overflow: 'EXPAND', emptyPriority: 1 }
  }
  return { heightMode: 'AUTO', minHeight: 1, columns, rows, overflow: 'EXPAND', emptyPriority: 0 }
}

export type AddToCardInput = {
  page: Page
  /** 组件要加入的卡片。 */
  blockId: string
  zoneKind: ZoneKind
  manifest: ComponentManifest
  title: string
  dataContextId: string
  fields: DataContextField[]
  newId: () => string
}

/**
 * 把组件作为一个新区域加入现有卡片。
 *
 * 区域跨满卡片宽度：卡片内的并排布局由区域的子网格表达，而不是再嵌一层卡片，
 * 否则同一种排版会有两种表示方式。
 */
export function addToCardOperations(input: AddToCardInput): { operations: EditorOperation[]; componentId: string } {
  const { page, blockId, zoneKind, manifest, title, dataContextId, fields, newId } = input
  const located = findBlock(page, blockId)
  if (!located) return { operations: [], componentId: '' }
  const block = located.block
  const componentId = newId()
  const zoneId = newId()
  const slotId = newId()

  const width = Math.max(block.layout.desktop.w, 1)
  const height = Math.max(recommendedSize(manifest).h, minimumSize(manifest).h)
  const component = buildComponent(componentId, manifest, title, dataContextId, fields)

  const zone: Zone = {
    // New regions go beneath what the card already shows.
    id: zoneId, order: block.zones.length + 1, type: zoneKind,
    layout: zoneLayoutFor(zoneKind, width, height),
    slots: [{ id: slotId, grid: { x: 0, y: 0, w: width, h: height }, componentId }],
  }
  const operations: EditorOperation[] = [
    { op: 'COMPONENT_CREATE', targetId: componentId, payload: { component } },
    { op: 'ZONE_CREATE', targetId: blockId, payload: { zone } },
  ]
  // 卡片要容纳新增区域，高度按新区域的行数增长。
  operations.push({
    op: 'BLOCK_RESIZE', targetId: blockId,
    payload: { w: block.layout.desktop.w, h: block.layout.desktop.h + height },
  })
  return { operations, componentId }
}

/**
 * 按数据集字段角色确定性地填充一份满足组件合同下限的绑定：维度优先取时间/类目
 * 字段，度量取 MEASURE 字段。这是“不依赖模型提供方”的保底识别；AI 识别只是在
 * 同一份字段目录上给出更贴合卡片意图的选择。
 */
export function defaultBinding(
  manifest: ComponentManifest, fields: DataContextField[],
): { dimensions: FieldBinding[]; measures: FieldBinding[] } {
  const identifierLike = (field: DataContextField) =>
    field.role === 'IDENTIFIER' || /(^|_)(id|no|code|uuid|key)$/i.test(field.code)
  const timeLike = (field: DataContextField) =>
    field.role === 'TIME' || field.semanticType === 'DATE' || field.semanticType === 'DATETIME' ||
    field.canonicalType === 'DATE' || field.canonicalType === 'DATETIME'
  // 折线/面积图天然沿时间展开；饼图/漏斗/柱状图/表格更适合先按类目分组。
  const timeFirst = /^(line|area)/.test(manifest.type) || (manifest.dataContract.roles.includes('TIME') && !manifest.dataContract.roles.includes('CATEGORY'))
  const rank = (field: DataContextField) => {
    if (identifierLike(field)) return 4
    if (timeLike(field)) return timeFirst ? 0 : 2
    if (field.role === 'DIMENSION' || field.semanticType === 'CATEGORY' || field.semanticType === 'REGION') return timeFirst ? 1 : 0
    return timeFirst ? 2 : 1
  }
  const measureRank = (field: DataContextField) => {
    if (/(amount|revenue|sales|gmv|profit|income|金额|销售额|收入)/i.test(`${field.code} ${field.name}`)) return 0
    if (/(quantity|qty|count|volume|数量|件数)/i.test(`${field.code} ${field.name}`)) return 1
    if ((field.aggregation || '').toUpperCase() === 'SUM') return 2
    return 3
  }
  const dimensionFields = fields.filter(field => field.role !== 'MEASURE').sort((left, right) => rank(left) - rank(right))
  const measureFields = fields.filter(field => field.role === 'MEASURE').sort((left, right) => measureRank(left) - measureRank(right))
  const dimensions: FieldBinding[] = Array.from({ length: manifest.dataContract.dimensions.min }, (_, index) => ({
    role: preferredRole(manifest, true), field: dimensionFields[index]?.code ?? '',
  }))
  const measures: FieldBinding[] = Array.from({ length: manifest.dataContract.measures.min }, (_, index) => ({
    role: preferredRole(manifest, false), field: measureFields[index]?.code ?? '',
  }))
  return { dimensions, measures }
}

/** 组件构造在「新建卡片」与「加入卡片」之间共用，避免两处默认值漂移。 */
function buildComponent(
  componentId: string, manifest: ComponentManifest, title: string,
  dataContextId: string, fields: DataContextField[],
): ReportComponent {
  const { dimensions, measures } = defaultBinding(manifest, fields)
  return {
    id: componentId,
    templateRef: { type: manifest.type, version: manifest.version },
    ...(dimensions.length || measures.length
      ? { dataBinding: { bindingMode: 'DATASET_FIELD' as const, dataContextId, dimensions, measures } }
      : {}),
    options: { ...(manifest.defaultOptions as ComponentOptions), title },
  }
}

export type AddComponentInput = {
  definition: ReportDefinition
  page: Page
  /** 落到哪个章节。为空时新建章节，让空白报告也能直接加组件。 */
  sectionId?: string
  manifest: ComponentManifest
  title: string
  dataContextId: string
  fields: DataContextField[]
  newId: () => string
  /** 拖放落点（网格坐标）；给定时卡片放在落点，与之重叠的卡片按碰撞规则下移。 */
  preferredRect?: { x: number; y: number }
  /** 报告还没有分区时自动创建的分区名；默认沿用卡片标题。 */
  sectionName?: string
}

/**
 * 把组件加入现有章节，并在该章节的网格里找一块不重叠的位置。
 * 之前每加一个组件就新建一个章节，报告永远只能是单列纵向堆叠；现在同一章节
 * 内可以并排放置，拖拽才有意义。
 */
export function addComponentOperations(input: AddComponentInput): { operations: EditorOperation[]; sectionId: string; componentId: string } {
  const { definition, page, manifest, title, dataContextId, fields, newId } = input
  const columns = canvasOf(definition).desktop.columns
  const componentId = newId()
  const blockId = newId()
  const zoneId = newId()
  const slotId = newId()

  const size = recommendedSize(manifest)
  const component = buildComponent(componentId, manifest, title, dataContextId, fields)

  const sections = orderedSections(page)
  const target = sections.find(section => section.id === input.sectionId) ?? sections.at(-1)
  const dropped = input.preferredRect
    ? {
      x: Math.min(Math.max(input.preferredRect.x, 0), Math.max(columns - Math.min(size.w, columns), 0)),
      y: Math.max(input.preferredRect.y, 0), w: Math.min(size.w, columns), h: size.h,
    }
    : undefined
  const rect = dropped ?? (target ? findFreeRect(target.blocks, size, columns) : { x: 0, y: 0, ...size })
  const block = {
    id: blockId, type: blockTypes[manifest.category] ?? 'CHART',
    layout: {
      desktop: rect,
      mobile: {
        order: (target ? target.blocks.length : 0) + 1,
        visible: true, heightMode: 'AUTO' as const, slotMode: 'STACK' as const,
      },
    },
    zones: [{
      id: zoneId, order: 1, type: 'CONTENT' as const,
      layout: {
        heightMode: 'AUTO' as const, minHeight: 1, columns: rect.w, rows: rect.h,
        overflow: 'EXPAND' as const, emptyPriority: 1,
      },
      slots: [{ id: slotId, grid: { x: 0, y: 0, w: rect.w, h: rect.h }, componentId }],
    }],
  }

  const operations: EditorOperation[] = [
    { op: 'COMPONENT_CREATE', targetId: componentId, payload: { component } },
  ]
  if (target) {
    operations.push({ op: 'BLOCK_CREATE', targetId: target.id, payload: { block } })
    if (dropped) {
      // 落点与既有卡片重叠时，沿用拖拽的碰撞消解：其它卡片让位，同一条修订提交。
      for (const change of resolveLayout([...target.blocks, block as Block], blockId, rect, columns, minimumSize(manifest))) {
        if (change.blockId === blockId) continue
        if (change.moved) operations.push({ op: 'BLOCK_MOVE', targetId: change.blockId, payload: { x: change.rect.x, y: change.rect.y } })
        if (change.resized) operations.push({ op: 'BLOCK_RESIZE', targetId: change.blockId, payload: { w: change.rect.w, h: change.rect.h } })
      }
    }
    return { operations, sectionId: target.id, componentId }
  }
  const sectionId = newId()
  operations.push({
    op: 'SECTION_CREATE', targetId: page.id,
    payload: { section: { id: sectionId, name: input.sectionName || title, order: 1, blocks: [block] } },
  })
  return { operations, sectionId, componentId }
}

/**
 * 移除组件时按它在卡片中的位置决定删到哪一层：卡片里还有别的区域就只删这一个
 * 区域，否则整张卡片一起删除。留下空槽位会让定义引用一个不存在的组件。
 */
export function removeComponentOperations(page: Page, componentId: string): EditorOperation[] {
  const located = findComponentBlock(page, componentId)
  const operations: EditorOperation[] = []
  if (located) {
    const { block } = located
    const zone = block.zones.find(item => item.slots.some(slot => slot.componentId === componentId))
    if (!zone || block.zones.length <= 1) {
      operations.push({ op: 'BLOCK_DELETE', targetId: block.id, payload: {} })
    } else if (zone.slots.length > 1) {
      const slot = zone.slots.find(item => item.componentId === componentId)
      if (slot) operations.push({ op: 'SLOT_DELETE', targetId: slot.id, payload: {} })
    } else {
      operations.push({ op: 'ZONE_DELETE', targetId: zone.id, payload: {} })
    }
  }
  operations.push({ op: 'COMPONENT_DELETE', targetId: componentId, payload: {} })
  return operations
}

export function updateComponentOperations(
  component: ReportComponent,
  options: ComponentOptions,
  binding: { dimensions: FieldBinding[]; measures: FieldBinding[] } | undefined,
  dataContextId: string | undefined,
): EditorOperation[] {
  const operations: EditorOperation[] = [
    { op: 'COMPONENT_UPDATE', targetId: component.id, payload: { options } },
  ]
  if (!binding) return operations
  const empty = binding.dimensions.length === 0 && binding.measures.length === 0
  // 卡片可以显式改绑到报告内的另一个数据集；未指定时沿用组件当前的数据上下文。
  const contextID = dataContextId ?? component.dataBinding?.dataContextId
  operations.push(empty || !contextID
    ? { op: 'DATA_BINDING_UPDATE', targetId: component.id, payload: { mode: 'CLEAR', dataBinding: null } }
    : {
      op: 'DATA_BINDING_UPDATE', targetId: component.id,
      payload: {
        mode: 'SET',
        dataBinding: { bindingMode: 'DATASET_FIELD', dataContextId: contextID, ...binding },
      },
    })
  return operations
}

export type InteractionDraft = {
  sourceComponentId: string
  targetComponentIds: string[]
  sourceField: string
  targetField: string
}

/**
 * 联动写入走 INTERACTION_CREATE / INTERACTION_DELETE，与其它编辑一样是受控
 * Operation，因此人工编排的联动和 AI 生成的联动进入同一份定义与同一条修订链。
 */
export function createInteractionOperations(draft: InteractionDraft, newId: () => string): EditorOperation[] {
  return [{
    op: 'INTERACTION_CREATE',
    targetId: draft.sourceComponentId,
    payload: {
      interaction: {
        id: newId(),
        sourceComponentId: draft.sourceComponentId,
        event: 'CLICK',
        action: 'FILTER',
        targetComponentIds: draft.targetComponentIds,
        fieldMappings: [{ sourceField: draft.sourceField, targetField: draft.targetField }],
      },
    },
  }]
}

export function deleteInteractionOperations(interactionId: string): EditorOperation[] {
  return [{ op: 'INTERACTION_DELETE', targetId: interactionId, payload: {} }]
}

export function sectionReorderOperations(page: Page, sectionId: string, direction: -1 | 1): EditorOperation[] {
  const ordered = orderedSections(page)
  const index = ordered.findIndex(section => section.id === sectionId)
  const adjacent = ordered[index + direction]
  if (index < 0 || !adjacent) return []
  return [
    { op: 'SECTION_REORDER', targetId: sectionId, payload: { order: adjacent.order } },
    { op: 'SECTION_REORDER', targetId: adjacent.id, payload: { order: ordered[index].order } },
  ]
}

export type { ManifestIndex }

/**
 * 槽位拖拽：SLOT_UPDATE 改写槽位在区域子网格中的位置。
 *
 * 若求解结果超出区域当前行数，区域必须一并加高——服务端校验
 * grid.y+h ≤ zone.rows，否则这次拖拽只会换来一次保存失败。
 */
export function slotLayoutOperations(
  block: Block,
  zone: Zone,
  slotId: string,
  rect: GridRect,
  minimum: { w: number; h: number },
): EditorOperation[] {
  const placement = resolveSlotPlacement(
    zone.slots.map(slot => ({ id: slot.id, grid: slot.grid })),
    slotId, rect, zone.layout.columns, zone.layout.rows, minimum,
  )
  const componentBySlot = new Map(zone.slots.map(slot => [slot.id, slot.componentId]))
  const operations: EditorOperation[] = []
  if (placement.requiredRows > zone.layout.rows) {
    operations.push({
      op: 'ZONE_UPDATE', targetId: zone.id,
      payload: { type: zone.type, layout: { ...zone.layout, rows: placement.requiredRows } },
    })
    // 卡片也要跟着长高，否则加高的区域会被裁掉。
    operations.push({
      op: 'BLOCK_RESIZE', targetId: block.id,
      payload: {
        w: block.layout.desktop.w,
        h: block.layout.desktop.h + (placement.requiredRows - zone.layout.rows),
      },
    })
  }
  for (const change of placement.changes) {
    operations.push({
      op: 'SLOT_UPDATE', targetId: change.slotId,
      payload: { grid: change.rect, componentId: componentBySlot.get(change.slotId) ?? '' },
    })
  }
  return operations
}

/**
 * 区域上下移动：与章节重排同构，交换两个区域的 order。
 * order 是区域自己的结构字段，不再借用 emptyPriority 表达。
 */
export function zoneReorderOperations(block: Block, zoneId: string, direction: -1 | 1): EditorOperation[] {
  const ordered = block.zones.slice().sort((left, right) =>
    (left.order ?? left.layout.emptyPriority) - (right.order ?? right.layout.emptyPriority) || left.id.localeCompare(right.id))
  const index = ordered.findIndex(zone => zone.id === zoneId)
  const adjacent = ordered[index + direction]
  if (index < 0 || !adjacent) return []
  return [
    { op: 'ZONE_REORDER', targetId: zoneId, payload: { order: index + direction + 1 } },
    { op: 'ZONE_REORDER', targetId: adjacent.id, payload: { order: index + 1 } },
  ]
}

/**
 * 更换卡片的展示类型：同一组件 ID 换成另一份组件清单。标题/副标题保留，其它表现
 * 属性按新清单的 optionSchema 过滤后叠加新默认值；数据绑定按新合同的角色白名单
 * 与数量上限重映射，多余的维度/度量被截掉，不足的留给作者补齐（保存前面板会校验）。
 */
export function replaceComponentOperations(
  component: ReportComponent,
  manifest: ComponentManifest,
): EditorOperation[] {
  const allowed = new Set(Object.keys(manifest.optionSchema.properties ?? {}))
  const kept = Object.fromEntries(Object.entries(component.options ?? {}).filter(([name]) => allowed.has(name)))
  const options: ComponentOptions = {
    ...(manifest.defaultOptions as ComponentOptions), ...kept,
    title: component.options.title, subtitle: component.options.subtitle,
  }
  let dataBinding = component.dataBinding
  if (dataBinding && dataBinding.bindingMode === 'DATASET_FIELD') {
    const dimensions = (dataBinding.dimensions ?? []).slice(0, manifest.dataContract.dimensions.max)
      .map(item => ({ ...item, role: manifest.dataContract.roles.includes(item.role) ? item.role : preferredRole(manifest, true) }))
    const measures = (dataBinding.measures ?? []).slice(0, manifest.dataContract.measures.max)
      .map(item => ({ ...item, role: manifest.dataContract.roles.includes(item.role) ? item.role : preferredRole(manifest, false) }))
    dataBinding = dimensions.length || measures.length
      ? { ...dataBinding, dimensions, measures }
      : undefined
  }
  const replacement: ReportComponent = {
    id: component.id,
    templateRef: { type: manifest.type, version: manifest.version },
    ...(dataBinding ? { dataBinding } : {}),
    options,
  }
  return [{ op: 'COMPONENT_REPLACE', targetId: component.id, payload: { component: replacement } }]
}

/**
 * 报告级数据集：DATA_CONTEXT_CREATE 只声明想用的数据集版本，服务端会从当前用户
 * 可见的受治理目录重新解析 ID 与查询策略；删除受定义校验保护，仍被卡片或筛选器
 * 引用的数据集无法移除。
 */
export function addDataContextOperations(
  definition: ReportDefinition,
  candidate: { id: string; datasetId: string; datasetVersionId: string; alias?: string },
): EditorOperation[] {
  return [{
    op: 'DATA_CONTEXT_CREATE', targetId: definition.metadata.id,
    payload: {
      dataContext: {
        id: candidate.id, datasetId: candidate.datasetId, datasetVersionId: candidate.datasetVersionId,
        alias: candidate.alias || candidate.datasetId, defaultParameters: [],
        queryPolicy: { timeoutMs: 10000, maxRows: 5000, cacheTtlSeconds: 300 },
      },
    },
  }]
}

export function removeDataContextOperations(dataContextId: string): EditorOperation[] {
  return [{ op: 'DATA_CONTEXT_DELETE', targetId: dataContextId, payload: {} }]
}

/** 数据集在报告内是否仍被卡片绑定或筛选器引用；被引用时不能移除。 */
export function dataContextInUse(definition: ReportDefinition, dataContextId: string): boolean {
  return definition.components.some(component => component.dataBinding?.dataContextId === dataContextId) ||
    (definition.globalFilters ?? []).some(filter => filter.fieldRef.dataContextId === dataContextId)
}

export type FilterDraft = {
  dataContextId: string
  field: string
  type: GlobalFilter['type']
  /** 全报告生效，或只作用于选中的卡片。 */
  scope: { type: 'REPORT' } | { type: 'BLOCK'; targetIds: string[] }
}

/** 按字段的语义类型建议筛选器类型：时间字段用日期区间，度量用数值区间，其余多选。 */
export function suggestedFilterType(field: DataContextField | undefined): GlobalFilter['type'] {
  if (!field) return 'MULTI_SELECT'
  if (field.role === 'TIME' || field.semanticType === 'DATE' || field.semanticType === 'DATETIME') return 'DATE_RANGE'
  if (field.role === 'MEASURE') return 'NUMBER_RANGE'
  if (field.semanticType === 'BOOLEAN') return 'SINGLE_SELECT'
  return 'MULTI_SELECT'
}

/**
 * 筛选器写入走 FILTER_CREATE / FILTER_UPDATE / FILTER_DELETE。筛选器的取值在运行页
 * 顶部的筛选栏由使用者填写并由服务端解析生效；这里只声明字段、类型与作用范围。
 */
export function createFilterOperations(definition: ReportDefinition, draft: FilterDraft, newId: () => string): EditorOperation[] {
  const filter: GlobalFilter = {
    id: newId(), type: draft.type,
    fieldRef: { dataContextId: draft.dataContextId, field: draft.field },
    scope: draft.scope.type === 'REPORT' ? { type: 'REPORT', targetIds: [] } : { type: 'BLOCK', targetIds: draft.scope.targetIds },
  }
  return [{ op: 'FILTER_CREATE', targetId: definition.metadata.id, payload: { filter } }]
}

export function updateFilterOperations(filter: GlobalFilter, draft: FilterDraft): EditorOperation[] {
  const next: GlobalFilter = {
    ...filter, type: draft.type,
    fieldRef: { dataContextId: draft.dataContextId, field: draft.field },
    scope: draft.scope.type === 'REPORT' ? { type: 'REPORT', targetIds: [] } : { type: 'BLOCK', targetIds: draft.scope.targetIds },
  }
  return [{ op: 'FILTER_UPDATE', targetId: filter.id, payload: { filter: next } }]
}

export function deleteFilterOperations(filterId: string): EditorOperation[] {
  return [{ op: 'FILTER_DELETE', targetId: filterId, payload: {} }]
}

/** 复制一张卡片：组件与卡片都拿新 ID，落在分区内第一块空位；同一条修订提交。 */
export function duplicateBlockOperations(input: {
  definition: ReportDefinition
  page: Page
  blockId: string
  newId: () => string
}): { operations: EditorOperation[]; componentId: string } | null {
  const located = findBlock(input.page, input.blockId)
  if (!located) return null
  const { section, block } = located
  const columns = canvasOf(input.definition).desktop.columns
  const operations: EditorOperation[] = []
  const componentIds = new Map<string, string>()
  let firstComponentId = ''
  for (const zone of block.zones) {
    for (const slot of zone.slots) {
      if (!slot.componentId) continue
      const component = input.definition.components.find(item => item.id === slot.componentId)
      if (!component) continue
      const nextId = input.newId()
      componentIds.set(slot.componentId, nextId)
      if (!firstComponentId) firstComponentId = nextId
      operations.push({ op: 'COMPONENT_CREATE', targetId: nextId, payload: { component: { ...component, id: nextId } } })
    }
  }
  const rect = findFreeRect(section.blocks, { w: block.layout.desktop.w, h: block.layout.desktop.h }, columns)
  const copy: Block = {
    ...block,
    id: input.newId(),
    layout: { ...block.layout, desktop: rect, mobile: { ...block.layout.mobile, order: section.blocks.length + 1 } },
    zones: block.zones.map(zone => ({
      ...zone, id: input.newId(),
      slots: zone.slots.map(slot => ({ ...slot, id: input.newId(), componentId: slot.componentId ? componentIds.get(slot.componentId) : slot.componentId })),
    })),
  }
  operations.push({ op: 'BLOCK_CREATE', targetId: section.id, payload: { block: copy } })
  return { operations, componentId: firstComponentId }
}

/** 删除一张卡片及其全部组件。 */
export function removeBlockOperations(page: Page, blockId: string): EditorOperation[] {
  const located = findBlock(page, blockId)
  if (!located) return []
  const componentIds = located.block.zones.flatMap(zone => zone.slots.map(slot => slot.componentId).filter((id): id is string => Boolean(id)))
  return [
    { op: 'BLOCK_DELETE', targetId: blockId, payload: {} },
    ...componentIds.map(id => ({ op: 'COMPONENT_DELETE' as const, targetId: id, payload: {} })),
  ]
}

/** 新建一个空分区，排在最后。 */
export function createSectionOperations(page: Page, name: string, newId: () => string): { operations: EditorOperation[]; sectionId: string } {
  const sectionId = newId()
  const order = Math.max(0, ...page.sections.map(section => section.order)) + 1
  return {
    sectionId,
    operations: [{ op: 'SECTION_CREATE', targetId: page.id, payload: { section: { id: sectionId, name, order, blocks: [] } } }],
  }
}

export function renameSectionOperations(sectionId: string, name: string): EditorOperation[] {
  return [{ op: 'SECTION_UPDATE', targetId: sectionId, payload: { name } }]
}

/** 改报告名称/描述：REPORT_SETTINGS_UPDATE 需要整份 metadata 与 runtimePolicy。 */
export function updateReportSettingsOperations(definition: ReportDefinition, patch: { name?: string; description?: string }): EditorOperation[] {
  return [{
    op: 'REPORT_SETTINGS_UPDATE', targetId: definition.metadata.id,
    payload: {
      metadata: { ...definition.metadata, ...(patch.name !== undefined ? { name: patch.name } : {}), ...(patch.description !== undefined ? { description: patch.description } : {}) },
      runtimePolicy: definition.runtimePolicy,
    },
  }]
}
