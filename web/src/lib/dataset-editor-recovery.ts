import type { DatasetDraft } from './datasets'
import type { CanvasPoint, GraphEnd, GraphGroup, GraphJoin, GraphTransform } from './dataset-graph'

export type DatasetEditorRecoverySnapshot = {
  draft: DatasetDraft
  relationBoxes: GraphJoin[]
  groupBoxes: GraphGroup[]
  transformBoxes: GraphTransform[]
  endBox: GraphEnd | null
  nodePositions: Record<string, CanvasPoint>
  metadata: { name: string; description: string; domain: string; subject: string }
}

export type DatasetEditorRecovery = {
  schemaVersion: 1
  datasetID: string
  datasetVersion?: number
  generatedCode: string
  savedAt: string
  snapshot: DatasetEditorRecoverySnapshot
}

const prefix = 'dataset-modeling-draft-v1'

const storageKey = (scope: string, datasetID: string) => `${prefix}:${scope || 'default'}:${datasetID}`

const browserStorage = (): Storage | undefined => {
  try { return globalThis.localStorage } catch { return undefined }
}

/**
 * 画布允许保存尚未通过发布校验的中间态。浏览器恢复副本补齐服务端规范 DSL
 * 无法表达的断线、缺少输入等编辑中状态；一旦画布可编译，页面还会同步服务端草稿。
 */
export function saveDatasetEditorRecovery(
  scope: string,
  recovery: DatasetEditorRecovery,
  storage: Storage | undefined = browserStorage(),
): boolean {
  if (!storage) return false
  try {
    storage.setItem(storageKey(scope, recovery.datasetID), JSON.stringify(recovery))
    return true
  } catch {
    return false
  }
}

export function loadDatasetEditorRecovery(
  scope: string,
  datasetID: string,
  storage: Storage | undefined = browserStorage(),
): DatasetEditorRecovery | null {
  if (!storage) return null
  try {
    const parsed = JSON.parse(storage.getItem(storageKey(scope, datasetID)) || 'null') as Partial<DatasetEditorRecovery> | null
    if (!parsed || parsed.schemaVersion !== 1 || parsed.datasetID !== datasetID || !parsed.snapshot || typeof parsed.generatedCode !== 'string' || typeof parsed.savedAt !== 'string') return null
    return parsed as DatasetEditorRecovery
  } catch {
    return null
  }
}

export function removeDatasetEditorRecovery(
  scope: string,
  datasetID: string,
  storage: Storage | undefined = browserStorage(),
) {
  if (!storage) return
  try { storage.removeItem(storageKey(scope, datasetID)) } catch { /* 恢复副本清理失败不阻断编辑 */ }
}
