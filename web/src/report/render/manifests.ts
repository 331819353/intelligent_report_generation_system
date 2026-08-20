import { apiRequest } from '../../lib/api.ts'
import type { BindingRole, FieldBinding, GridRect } from './schema.ts'

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

export type EditorBindingKind = 'DIMENSION' | 'MEASURE'

export type EditorBindingGroup = {
  id: string
  label: string
  description: string
  kind: EditorBindingKind
  roles: BindingRole[]
  min: number
  max: number
  addLabel: string
  nestedUnder?: string
  maxPerParent?: number
}

export type ComponentEditorProfile = {
  componentType: string
  componentVersion: string
  example: {
    title: string
    description: string
    items: string[]
  }
  bindingGroups: EditorBindingGroup[]
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
  /** 与不可变渲染清单分离的作者体验合同，由服务端按精确组件版本下发。 */
  editorProfile?: ComponentEditorProfile
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

function compareVersion(left: string, right: string): number {
  const a = left.split('.').map(Number)
  const b = right.split('.').map(Number)
  for (let index = 0; index < Math.max(a.length, b.length); index += 1) {
    const difference = (a[index] ?? 0) - (b[index] ?? 0)
    if (difference !== 0) return difference
  }
  return 0
}

/** 组件库只展示每种组件的最新合同；精确旧版本仍留在索引中供既有报告渲染。 */
export function latestComponentManifests(items: ComponentManifest[]): ComponentManifest[] {
  const latest = new Map<string, ComponentManifest>()
  for (const manifest of items) {
    const current = latest.get(manifest.type)
    if (!current || compareVersion(manifest.version, current.version) > 0) latest.set(manifest.type, manifest)
  }
  return Array.from(latest.values()).sort((left, right) =>
    left.category.localeCompare(right.category) || left.displayName.localeCompare(right.displayName))
}

/** 旧清单缺少编辑档案时仍提供一份最小兼容表单；内置清单正常不会走回退。 */
export function editorBindingGroups(manifest: ComponentManifest): EditorBindingGroup[] {
  if (manifest.editorProfile) return manifest.editorProfile.bindingGroups
  const dimensionRoles = manifest.dataContract.roles.filter(role =>
    role !== 'VALUE' && role !== 'Y_AXIS' && role !== 'SIZE')
  const measureRoles = manifest.dataContract.roles.filter(role =>
    role === 'VALUE' || role === 'Y_AXIS' || role === 'SIZE')
  const groups: EditorBindingGroup[] = []
  if (manifest.dataContract.dimensions.max > 0) {
    groups.push({
      id: 'dimensions', label: '维度', description: '用于分组、分类或时间轴',
      kind: 'DIMENSION', roles: dimensionRoles.length ? dimensionRoles : ['DIMENSION'],
      min: manifest.dataContract.dimensions.min, max: manifest.dataContract.dimensions.max, addLabel: '添加维度',
    })
  }
  if (manifest.dataContract.measures.max > 0) {
    groups.push({
      id: 'measures', label: '指标', description: '用于展示和比较的数值',
      kind: 'MEASURE', roles: measureRoles.length ? measureRoles : ['VALUE'],
      min: manifest.dataContract.measures.min, max: manifest.dataContract.measures.max, addLabel: '添加指标',
    })
  }
  return groups
}

export function editorBindingsValid(
  manifest: ComponentManifest,
  dimensions: FieldBinding[],
  measures: FieldBinding[],
): boolean {
  const groups = editorBindingGroups(manifest)
  for (const kind of ['DIMENSION', 'MEASURE'] as const) {
    const bindings = kind === 'DIMENSION' ? dimensions : measures
    const kindGroups = groups.filter(group => group.kind === kind)
    if (bindings.some(binding => !kindGroups.some(group => group.roles.includes(binding.role)))) return false
    for (const group of kindGroups) {
      const count = bindings.filter(binding => group.roles.includes(binding.role)).length
      if (count < group.min || count > group.max) return false
    }
    for (const child of kindGroups.filter(group => group.nestedUnder)) {
      const parent = kindGroups.find(group => group.id === child.nestedUnder)
      if (!parent) return false
      let parentSeen = false
      let children = 0
      for (const binding of bindings) {
        if (parent.roles.includes(binding.role)) {
          parentSeen = true
          children = 0
        } else if (child.roles.includes(binding.role)) {
          children += 1
          if (!parentSeen || children > (child.maxPerParent ?? child.max)) return false
        }
      }
    }
  }
  return true
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
