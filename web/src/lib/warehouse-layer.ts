/**
 * 物理表资产的“层级:”标签合同（与服务端 internal/warehouselayer 一致）。
 *
 * 元数据清洗为每张表判定所处数仓层级并写入恰好一个 `层级:` 标签；人工可在资产页
 * 修改。该标签描述这张物理表的既有粒度，是它默认映射数据集进入数仓的层级，也是
 * 数据集画布单表直落时的默认层级。字段资产不携带层级标签。
 */
export type WarehouseLayer = 'ODS' | 'DIM' | 'DWD' | 'DWS' | 'ADS'

export const WAREHOUSE_LAYER_TAG_PREFIX = '层级:'
export const warehouseLayers: WarehouseLayer[] = ['ODS', 'DIM', 'DWD', 'DWS', 'ADS']

export const warehouseLayerLabels: Record<WarehouseLayer, string> = {
  ODS: 'ODS 贴源',
  DIM: 'DIM 维度',
  DWD: 'DWD 明细',
  DWS: 'DWS 汇总',
  ADS: 'ADS 应用',
}

export function isWarehouseLayer(value: string): value is WarehouseLayer {
  return (warehouseLayers as string[]).includes(value)
}

export function isLayerTag(tag: string): boolean {
  return tag.trim().startsWith(WAREHOUSE_LAYER_TAG_PREFIX)
}

/** 标签集合中声明的层级；没有合法层级标签时返回 undefined。 */
export function layerFromTags(tags: readonly string[] | undefined): WarehouseLayer | undefined {
  for (const tag of tags ?? []) {
    if (!isLayerTag(tag)) continue
    const layer = tag.trim().slice(WAREHOUSE_LAYER_TAG_PREFIX.length).trim().toUpperCase()
    if (isWarehouseLayer(layer)) return layer
  }
  return undefined
}

/** 返回把层级标签替换为给定层级后的新标签集合；layer 为空时只移除。 */
export function replaceLayerTag(tags: readonly string[], layer: WarehouseLayer | ''): string[] {
  const rest = tags.filter(tag => !isLayerTag(tag))
  return layer ? [...rest, WAREHOUSE_LAYER_TAG_PREFIX + layer] : rest
}
