/**
 * 数据集层级合同（与服务端 internal/dataset 保持一致）：
 *
 * - `layer` 只描述粒度合同：ODS 贴源 / DIM 实体 / DWD 明细 / DWS 汇总 / ADS 应用。
 * - 血缘方式由画布拓扑推导，不单独持久化：
 *   - SOURCE  源表直落：恰好一张物理表、无 Join。物理表（含导入表、既有宽表）
 *             可以声明任意层级直接进入数仓，并保持源表既有粒度；
 *   - MODELED 分层加工：全部节点引用已发布数据集版本，层级必须满足
 *             ODS→DIM/DWD→DWS→ADS 的方向约束。
 */
import { layerFromTags } from './warehouse-layer.ts'

export type LayerChoice = 'ODS' | 'DIM' | 'DWD' | 'DWS' | 'ADS'
export type DatasetLineage = 'SOURCE' | 'MODELED'

export const allLayerChoices: LayerChoice[] = ['ODS', 'DIM', 'DWD', 'DWS', 'ADS']

export type LayerChoiceDraft = {
  nodes: Array<{ table: { sourceKind?: 'TABLE' | 'DATASET'; datasetLayer?: LayerChoice; tags?: string[] } }>
  joins?: unknown[]
}

/** 由画布拓扑推导血缘方式；与服务端 Document.Lineage() 同一判据。 */
export function datasetLineage(draft: LayerChoiceDraft): DatasetLineage {
  const single = draft.nodes.length === 1 && draft.nodes[0].table.sourceKind !== 'DATASET'
  return single && !(draft.joins?.length) ? 'SOURCE' : 'MODELED'
}

/**
 * 当前画布可选的层级列表；首项是默认层级。
 * SOURCE 血缘开放全部五层：默认取物理表“层级:”标签（元数据清洗判定、人工可改）
 * 声明的层级，没有标签时默认 ODS；MODELED 血缘按上游层级推导。
 */
export function chooseDatasetLayers(draft: LayerChoiceDraft): LayerChoice[] {
  if (datasetLineage(draft) === 'SOURCE') {
    const tagged = layerFromTags(draft.nodes[0].table.tags)
    return tagged ? [tagged, ...allLayerChoices.filter(layer => layer !== tagged)] : [...allLayerChoices]
  }
  const datasetNodes = draft.nodes.filter(node => node.table.sourceKind === 'DATASET')
  if (datasetNodes.length !== draft.nodes.length) {
    throw new Error('分层建模只能引用已发布数据集版本；物理表请以单表源直落方式声明层级，或先落 ODS')
  }
  const upstreamLayers = new Set(datasetNodes.map(node => node.table.datasetLayer).filter(Boolean))
  if (!upstreamLayers.size) throw new Error('无法识别上游数据集层级')
  if ([...upstreamLayers].every(layer => layer === 'ODS' || layer === 'DIM') && upstreamLayers.has('ODS')) {
    // 只有纯 ODS 可以选择 DIM；DWD 允许一张事实 ODS 加可选 DIM。
    return upstreamLayers.has('DIM') ? ['DWD'] : ['DWD', 'DIM']
  }
  if ([...upstreamLayers].every(layer => layer === 'DWD' || layer === 'DIM') && upstreamLayers.has('DWD')) return ['DWS']
  if ([...upstreamLayers].every(layer => layer === 'DWS')) return ['ADS']
  throw new Error('上游数据集层级不符合 ODS→DIM/DWD→DWS→ADS 合同')
}

/** 各层级在源表直落时对输出合同的要求，用于保存前提示。 */
export function sourceLayerRequirement(layer: LayerChoice): string {
  switch (layer) {
    case 'ODS': return '逐行贴源：保持源表行粒度，不需要声明粒度键。'
    case 'DIM': return '实体粒度：需要声明实体粒度说明与业务键，源表每行应是一个业务实体。'
    case 'DWD': return '明细粒度：源表每行应是一条业务事实/事件；可不声明粒度键。'
    case 'DWS': return '汇总粒度：需要声明粒度说明与粒度键，且至少一个度量字段；源表应已完成汇总。'
    case 'ADS': return '应用粒度：需要声明面向消费场景的粒度说明与粒度键；源表应是可直接消费的结果表。'
  }
}
