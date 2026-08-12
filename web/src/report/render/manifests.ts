import { apiRequest } from '../../lib/api.ts'
import type { BindingRole, GridRect } from './schema.ts'

/**
 * 组件清单（Component Manifest）是组件系统唯一的注册表，由服务端
 * internal/report/template/manifests/*.json 提供。渲染器按 manifest.renderer
 * 决定用哪个渲染实现，绝不根据组件类型名做字符串猜测。
 */
export type ManifestRenderer = 'ECHARTS' | 'REACT' | 'TEXT' | 'IMAGE' | 'CONTROL'
export type ManifestCategory = 'CHART' | 'TABLE' | 'CONTENT' | 'CONTROL'

export type OptionPropertySchema = {
  type: 'boolean' | 'string' | 'integer' | 'number'
  description?: string
  enum?: string[]
  minimum?: number
  maximum?: number
}

export type ComponentManifest = {
  type: string
  version: string
  renderer: ManifestRenderer
  displayName: string
  category: ManifestCategory
  minSize: { w: number; h: number }
  recommendedSize: { w: number; h: number }
  dataContract: {
    dimensions: { min: number; max: number }
    measures: { min: number; max: number }
    timeField?: { required: boolean }
    roles: BindingRole[]
  }
  stackingRequiresAdditive?: boolean
  optionSchema: {
    type: 'object'
    additionalProperties: false
    required: string[]
    properties: Record<string, OptionPropertySchema>
  }
  defaultOptions: Record<string, unknown>
  mobilePolicy: {
    supported: boolean
    defaultLegendMode: 'VISIBLE' | 'HIDDEN' | 'SCROLL'
    labelDegradation: 'NONE' | 'HIDE_WHEN_DENSE' | 'ELLIPSIS' | 'ROTATE'
  }
  supportedInteractions: Array<'CLICK_FILTER' | 'DRILL_DOWN' | 'BRUSH' | 'ZOOM' | 'SORT' | 'PAGINATE'>
}

export type ManifestIndex = {
  /** 按 type@version 精确查找。运行期不做「取最新版」回退：发布时固定的版本缺失即失败。 */
  get(type: string, version: string): ComponentManifest | undefined
  list(): ComponentManifest[]
}

export function indexManifests(items: ComponentManifest[]): ManifestIndex {
  const byRef = new Map(items.map(item => [`${item.type}@${item.version}`, item]))
  return {
    get: (type, version) => byRef.get(`${type}@${version}`),
    list: () => items,
  }
}

export const emptyManifestIndex: ManifestIndex = indexManifests([])

export function listComponentManifests() {
  return apiRequest<{ items: ComponentManifest[] }>('/v1/report-component-manifests')
}

/** 组件在网格中的最小可渲染尺寸；拖拽缩放不得把组件压到清单声明的下限以下。 */
export function minimumSize(manifest?: ComponentManifest): { w: number; h: number } {
  return { w: manifest?.minSize.w ?? 2, h: manifest?.minSize.h ?? 2 }
}

export function recommendedSize(manifest?: ComponentManifest): { w: number; h: number } {
  return {
    w: Math.max(manifest?.recommendedSize.w ?? 8, manifest?.minSize.w ?? 2),
    h: Math.max(manifest?.recommendedSize.h ?? 5, manifest?.minSize.h ?? 2),
  }
}

export function fitsCanvas(rect: GridRect, columns: number): boolean {
  return rect.x >= 0 && rect.y >= 0 && rect.w >= 1 && rect.h >= 1 && rect.x + rect.w <= columns
}
