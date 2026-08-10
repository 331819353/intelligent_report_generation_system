export type GridRect = { x: number; y: number; w: number; h: number }
export type LayoutBlock = { id: string; layout: { desktop: GridRect } }
export type Collision = { firstId: string; secondId: string }
export type Slot = { id: string; grid: GridRect; componentId: string; mergedFrom?: string[] }
export type GridSize = { w: number; h: number }

export type CompactMode = 'NONE' | 'VERTICAL'
export type ZoneHeightMode = 'FIXED' | 'AUTO' | 'FR' | 'HIDDEN'
export type MobileHeightMode = 'AUTO' | 'FIXED' | 'ASPECT_RATIO'
export type MobileSlotMode = 'STACK' | 'CAROUSEL' | 'PRIMARY_ONLY' | 'COLLAPSE'

export type ZoneLayout = {
  heightMode: ZoneHeightMode
  minHeight: number
  maxHeight?: number
  fixedHeight?: number
  fr?: number
  emptyPriority?: number
}

export type Zone = {
  id: string
  type: 'HEADER' | 'FILTER' | 'INSIGHT' | 'CONTENT' | 'FOOTER'
  layout: ZoneLayout
  slots: Slot[]
}

export type MobileLayout = {
  order: number
  visible: boolean
  heightMode: MobileHeightMode
  slotMode: MobileSlotMode
  fixedHeight?: number
  aspectRatio?: number
  primarySlotId?: string
}

export type MobileSourceBlock = {
  id: string
  layout: { mobile: MobileLayout }
  zones: Zone[]
}

export type MobilePageSource = {
  id: string
  sections: Array<{ blocks: MobileSourceBlock[] }>
}

export type MobileBlock = {
  id: string
  order: number
  heightMode: MobileHeightMode
  slotMode: MobileSlotMode
  slots: Slot[]
  filterDrawerSlots: Slot[]
  queriedComponentIds: string[]
  primarySlotId?: string
  fixedHeight?: number
  aspectRatio?: number
  fullWidth: true
  componentPolicies: MobileComponentPolicy[]
}

export type MobileComponentPolicy = {
  componentId: string
  supported: boolean
  legendMode: 'VISIBLE' | 'HIDDEN' | 'SCROLL'
  labelDegradation: 'NONE' | 'HIDE_WHEN_DENSE' | 'ELLIPSIS' | 'ROTATE'
}

export type MobileComponent = { id: string; templateRef: { type: string; version: string } }
export type MobileManifest = {
  type: string
  version: string
  mobilePolicy: {
    supported: boolean
    defaultLegendMode: MobileComponentPolicy['legendMode']
    labelDegradation: MobileComponentPolicy['labelDegradation']
  }
}

export type DesktopCanvas = {
  columns: number
  baseRowHeight: number
  gapX: number
  gapY: number
  paddingX: number
  paddingY: number
}

export type PixelRect = { x: number; y: number; width: number; height: number }

export type SlotMergeCode =
  | 'REPORT_SLOT_MERGE_NOT_RECTANGULAR'
  | 'REPORT_SLOT_MERGE_MULTIPLE_COMPONENTS'
  | 'REPORT_SLOT_MERGE_BELOW_MIN_SIZE'

function intersects(a: GridRect, b: GridRect): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
}

/** The browser preview uses the same x-axis sweep and stable output order as Go. */
export function detectCollisions(blocks: LayoutBlock[]): Collision[] {
  const sorted = [...blocks].sort((a, b) =>
    a.layout.desktop.x - b.layout.desktop.x || a.id.localeCompare(b.id),
  )
  let active: LayoutBlock[] = []
  const collisions: Collision[] = []
  for (const current of sorted) {
    active = active.filter(candidate =>
      candidate.layout.desktop.x + candidate.layout.desktop.w > current.layout.desktop.x,
    )
    for (const candidate of active) {
      if (intersects(candidate.layout.desktop, current.layout.desktop)) {
        const [firstId, secondId] = [candidate.id, current.id].sort()
        collisions.push({ firstId, secondId })
      }
    }
    active.push(current)
  }
  return collisions.sort((a, b) => a.firstId.localeCompare(b.firstId) || a.secondId.localeCompare(b.secondId))
}

export function validateSlotMerge(
  slots: Slot[],
  slotIds: string[],
  minimum: GridSize = { w: 1, h: 1 },
): SlotMergeCode | undefined {
  if (slotIds.length < 2 || new Set(slotIds).size !== slotIds.length) {
    return 'REPORT_SLOT_MERGE_NOT_RECTANGULAR'
  }
  const wanted = new Set(slotIds)
  const selected = slots.filter(slot => wanted.has(slot.id))
  if (selected.length !== slotIds.length) return 'REPORT_SLOT_MERGE_NOT_RECTANGULAR'

  let minX = Number.MAX_SAFE_INTEGER
  let minY = Number.MAX_SAFE_INTEGER
  let maxX = 0
  let maxY = 0
  let area = 0
  let occupied = 0
  for (const slot of selected) {
    minX = Math.min(minX, slot.grid.x)
    minY = Math.min(minY, slot.grid.y)
    maxX = Math.max(maxX, slot.grid.x + slot.grid.w)
    maxY = Math.max(maxY, slot.grid.y + slot.grid.h)
    area += slot.grid.w * slot.grid.h
    if (slot.componentId) occupied++
  }
  const overlapping = selected.some((slot, left) =>
    selected.slice(left + 1).some(candidate => intersects(slot.grid, candidate.grid)),
  )
  if (overlapping || area !== (maxX - minX) * (maxY - minY)) {
    return 'REPORT_SLOT_MERGE_NOT_RECTANGULAR'
  }
  if (occupied > 1) return 'REPORT_SLOT_MERGE_MULTIPLE_COMPONENTS'
  if (maxX - minX < minimum.w || maxY - minY < minimum.h) {
    return 'REPORT_SLOT_MERGE_BELOW_MIN_SIZE'
  }
  return undefined
}

export function slotRenderSize(block: GridSize, zone: GridSize, slot: GridSize): GridSize {
  if (block.w < 1 || block.h < 1 || zone.w < 1 || zone.h < 1 || slot.w < 1 || slot.h < 1) {
    return { w: 0, h: 0 }
  }
  return {
    w: Math.ceil(block.w * slot.w / zone.w),
    h: Math.ceil(block.h * slot.h / zone.h),
  }
}

/** Converts runtime pixels without adding derived values to Report Definition JSON. */
export function desktopPixelRect(canvas: DesktopCanvas, grid: GridRect, containerWidth: number): PixelRect {
  if (canvas.columns < 1 || containerWidth <= 0 || grid.x < 0 || grid.y < 0 ||
      grid.w < 1 || grid.h < 1 || grid.x + grid.w > canvas.columns) {
    throw new Error('desktop pixel conversion input is invalid')
  }
  const usableWidth = containerWidth - 2 * canvas.paddingX - (canvas.columns - 1) * canvas.gapX
  if (usableWidth <= 0) throw new Error('desktop container is too narrow for the configured grid')
  const columnWidth = usableWidth / canvas.columns
  return {
    x: canvas.paddingX + grid.x * (columnWidth + canvas.gapX),
    y: canvas.paddingY + grid.y * (canvas.baseRowHeight + canvas.gapY),
    width: grid.w * columnWidth + (grid.w - 1) * canvas.gapX,
    height: grid.h * canvas.baseRowHeight + (grid.h - 1) * canvas.gapY,
  }
}

export function compactBlocks(blocks: LayoutBlock[], mode: CompactMode): LayoutBlock[] {
  const result = blocks.map(block => ({
    ...block,
    layout: { ...block.layout, desktop: { ...block.layout.desktop } },
  }))
  if (mode === 'NONE') return result
  const order = result.map((_, index) => index).sort((left, right) => {
    const a = result[left]
    const b = result[right]
    return a.layout.desktop.y - b.layout.desktop.y ||
      a.layout.desktop.x - b.layout.desktop.x || a.id.localeCompare(b.id)
  })
  const placed: number[] = []
  for (const index of order) {
    const current = result[index]
    let top = 0
    for (const previousIndex of placed) {
      const previous = result[previousIndex].layout.desktop
      if (previous.x < current.layout.desktop.x + current.layout.desktop.w &&
          current.layout.desktop.x < previous.x + previous.w) {
        top = Math.max(top, previous.y + previous.h)
      }
    }
    current.layout.desktop.y = top
    placed.push(index)
  }
  return result
}

export function calculateZoneHeights(
  zones: Zone[],
  totalHeight: number,
  priority = [...zones]
    .sort((a, b) => (b.layout.emptyPriority ?? 0) - (a.layout.emptyPriority ?? 0) || a.id.localeCompare(b.id))
    .map(zone => zone.id),
): Record<string, number> {
  const result: Record<string, number> = {}
  let remaining = totalHeight
  let weighted: Zone[] = []
  let totalWeight = 0
  for (const zone of zones) {
    switch (zone.layout.heightMode) {
      case 'HIDDEN':
        result[zone.id] = 0
        break
      case 'FIXED': {
        const height = zone.layout.fixedHeight ?? zone.layout.minHeight
        result[zone.id] = height
        remaining -= height
        break
      }
      case 'AUTO':
        result[zone.id] = zone.layout.minHeight
        remaining -= zone.layout.minHeight
        break
      case 'FR':
        result[zone.id] = zone.layout.minHeight
        remaining -= zone.layout.minHeight
        weighted.push(zone)
        if ((zone.layout.fr ?? 0) > 0) totalWeight += zone.layout.fr!
        break
    }
  }
  while (remaining > 0 && weighted.length > 0 && totalWeight > 0) {
    const available = remaining
    let progress = 0
    const next: Zone[] = []
    let nextWeight = 0
    for (const zone of weighted) {
      let share = Math.max(1, Math.floor(available * zone.layout.fr! / totalWeight))
      share = Math.min(share, remaining)
      if (zone.layout.maxHeight !== undefined) {
        share = Math.min(share, zone.layout.maxHeight - result[zone.id])
      }
      if (share > 0) {
        result[zone.id] += share
        remaining -= share
        progress += share
      }
      if (zone.layout.maxHeight === undefined || result[zone.id] < zone.layout.maxHeight) {
        next.push(zone)
        nextWeight += zone.layout.fr!
      }
      if (remaining === 0) break
    }
    if (progress === 0) break
    weighted = next
    totalWeight = nextWeight
  }

  const byId = new Map(zones.map(zone => [zone.id, zone]))
  let freed = 0
  for (const zone of zones) {
    if (zone.layout.heightMode !== 'HIDDEN' && zone.slots.length === 0) {
      freed += result[zone.id]
      result[zone.id] = 0
    }
  }
  for (const id of priority) {
    if (freed === 0) break
    const zone = byId.get(id)
    if (!zone || zone.slots.length === 0 || !['AUTO', 'FR'].includes(zone.layout.heightMode)) continue
    let capacity = freed
    if (zone.layout.maxHeight !== undefined) capacity = Math.min(capacity, zone.layout.maxHeight - result[id])
    if (capacity > 0) {
      result[id] += capacity
      freed -= capacity
    }
  }
  return result
}

export function mobileBlockHeight(layout: MobileLayout, containerWidth: number, autoHeight: number): number {
  if (layout.heightMode === 'AUTO' && autoHeight >= 0) return autoHeight
  if (layout.heightMode === 'FIXED' && (layout.fixedHeight ?? 0) >= 40) return layout.fixedHeight!
  if (layout.heightMode === 'ASPECT_RATIO' && containerWidth > 0 && (layout.aspectRatio ?? 0) > 0) {
    return Math.round(containerWidth / layout.aspectRatio!)
  }
  throw new Error('mobile height mode is invalid')
}

export function toMobileLayout(
  page: MobilePageSource,
  components: MobileComponent[] = [],
  manifests: MobileManifest[] = [],
): { id: string; blocks: MobileBlock[] } {
  const componentsById = new Map(components.map(component => [component.id, component]))
  const manifestsByRef = new Map(manifests.map(manifest => [`${manifest.type}@${manifest.version}`, manifest]))
  const blocks: MobileBlock[] = []
  for (const section of page.sections) {
    for (const block of section.blocks) {
      const mobile = block.layout.mobile
      if (!mobile.visible) continue
      let slots = block.zones
        .filter(zone => zone.layout.heightMode !== 'HIDDEN' && zone.type !== 'FILTER')
        .flatMap(zone => zone.slots)
      const filterDrawerSlots = block.zones
        .filter(zone => zone.layout.heightMode !== 'HIDDEN' && zone.type === 'FILTER')
        .flatMap(zone => zone.slots)
      const slotOrder = (a: Slot, b: Slot) =>
        a.grid.y - b.grid.y || a.grid.x - b.grid.x || a.id.localeCompare(b.id)
      slots = [...slots].sort(slotOrder)
      filterDrawerSlots.sort(slotOrder)
      if (mobile.slotMode === 'PRIMARY_ONLY') {
        slots = slots.filter(slot => slot.id === mobile.primarySlotId).slice(0, 1)
      }
      const componentPolicies = slots.flatMap(slot => {
        const component = componentsById.get(slot.componentId)
        const manifest = component && manifestsByRef.get(`${component.templateRef.type}@${component.templateRef.version}`)
        return manifest ? [{
          componentId: component.id,
          supported: manifest.mobilePolicy.supported,
          legendMode: manifest.mobilePolicy.defaultLegendMode,
          labelDegradation: manifest.mobilePolicy.labelDegradation,
        }] : []
      })
      blocks.push({
        id: block.id,
        order: mobile.order,
        heightMode: mobile.heightMode,
        slotMode: mobile.slotMode,
        slots,
        filterDrawerSlots,
        queriedComponentIds: slots.map(slot => slot.componentId).filter(Boolean),
        primarySlotId: mobile.primarySlotId,
        fixedHeight: mobile.fixedHeight,
        aspectRatio: mobile.aspectRatio,
        fullWidth: true,
        componentPolicies,
      })
    }
  }
  blocks.sort((a, b) => a.order - b.order || a.id.localeCompare(b.id))
  return { id: page.id, blocks }
}
