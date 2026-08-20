import catalogSource from './analysis-card-catalog.json'
import type { BindingRole, ComponentOptions } from '../render/schema.ts'
import type { ComponentManifest, EditorBindingGroup, ManifestCategory } from '../render/manifests.ts'

export type AnalysisCardVariant = '01' | '02' | '03'
export type AnalysisRendererKind =
  | 'metric' | 'progress' | 'comparison' | 'ranking' | 'trend' | 'composition'
  | 'structure' | 'concentration' | 'distribution' | 'anomaly' | 'relationship'
  | 'quadrant' | 'matrix' | 'funnel' | 'flow' | 'cohort' | 'lifecycle'
  | 'contribution' | 'waterfall' | 'drivers' | 'root-cause' | 'forecast'
  | 'scenario' | 'sensitivity' | 'risk' | 'experiment' | 'geospatial'
  | 'monitoring' | 'pipeline' | 'calendar' | 'detail' | 'timeline' | 'insight'
  | 'action' | 'data-info' | 'scope' | 'long-form'

export type AnalysisCardCatalogItem = {
  id: number
  slug: string
  type: string
  name: string
  question: string
  subtypes: string[]
  presentations: string[]
  rendererKind: AnalysisRendererKind
  category: ManifestCategory
  bindingGroups: EditorBindingGroup[]
}

export const analysisCardCatalog = catalogSource as AnalysisCardCatalogItem[]

const byType = new Map(analysisCardCatalog.map(item => [item.type, item]))

export const analysisCardVariants: Array<{ id: AnalysisCardVariant; name: string; description: string }> = [
  { id: '01', name: '聚焦式', description: '单一主视觉，留白更充分' },
  { id: '02', name: '组合式', description: '主图与辅助指标紧凑组合' },
  { id: '03', name: '叙事式', description: '柔和蓝色强调与编辑式信息层级' },
]

export function analysisCardDefinition(type: string) {
  return byType.get(type)
}

export function isAnalysisCardManifest(manifest: ComponentManifest) {
  return byType.has(manifest.type)
}

export function analysisCardOption(variant: AnalysisCardVariant): Pick<ComponentOptions, 'cardVariant'> {
  return { cardVariant: variant }
}

export function bindingKind(role: BindingRole, item: AnalysisCardCatalogItem) {
  return item.bindingGroups.find(group => group.roles.includes(role))?.kind
}
