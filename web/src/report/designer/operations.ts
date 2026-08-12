import type { DataContextField, EditorOperation, EditorOperationBundle } from '../api/editor.ts'
import type { ComponentManifest, ManifestIndex } from '../render/manifests.ts'
import { minimumSize, recommendedSize } from '../render/manifests.ts'
import {
  canvasOf, findBlock, findComponentBlock, orderedSections,
  type BlockType, type ComponentOptions, type FieldBinding,
  type GridRect, type Page, type ReportComponent, type ReportDefinition, type Section, type Zone,
} from '../render/schema.ts'
import { findFreeRect, resolveLayout } from './placement.ts'

/**
 * 编辑器把每一次画布操作翻译成 Report Operation v1 受控指令。
 *
 * 拖拽、缩放、加组件、改绑定全部走同一条 POST /operations 通道，因此人工编辑
 * 与 AI 改稿写入的是同一种指令、同一份 Definition、同一条修订链。
 */

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
    id: zoneId, type: zoneKind,
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

/** 组件构造在「新建卡片」与「加入卡片」之间共用，避免两处默认值漂移。 */
function buildComponent(
  componentId: string, manifest: ComponentManifest, title: string,
  dataContextId: string, fields: DataContextField[],
): ReportComponent {
  const dimensionFields = fields.filter(field => field.role !== 'MEASURE')
  const measureFields = fields.filter(field => field.role === 'MEASURE')
  const dimensions: FieldBinding[] = Array.from({ length: manifest.dataContract.dimensions.min }, (_, index) => ({
    role: preferredRole(manifest, true), field: dimensionFields[index]?.code ?? '',
  }))
  const measures: FieldBinding[] = Array.from({ length: manifest.dataContract.measures.min }, (_, index) => ({
    role: preferredRole(manifest, false), field: measureFields[index]?.code ?? '',
  }))
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
  const rect = target ? findFreeRect(target.blocks, size, columns) : { x: 0, y: 0, ...size }
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
      id: zoneId, type: 'CONTENT' as const,
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
    return { operations, sectionId: target.id, componentId }
  }
  const sectionId = newId()
  operations.push({
    op: 'SECTION_CREATE', targetId: page.id,
    payload: { section: { id: sectionId, name: title, order: 1, blocks: [block] } },
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
  const contextID = component.dataBinding?.dataContextId ?? dataContextId
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
