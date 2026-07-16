import type { ReportDocument } from './report-contract'
import type { ReportChangeSource, ReportChangeTarget, ReportDraftChange, ReportOperationType } from './report-drafts'
import { applyReportJSONPatch, createReportJSONPatch, type ReportJSONPatch } from './report-json-patch'

export const MAX_EDITOR_HISTORY_ENTRIES = 100

export type ReportEditorOperationInput = {
  operationType: ReportOperationType
  summary: string
  source?: ReportChangeSource
  target?: ReportChangeTarget
  clientOperationId?: string
}

/** 复用报告草稿 API 合同，避免前端历史与服务端 changes 产生第二套字段命名。 */
export type ReportEditorAuditChange = ReportDraftChange

export type ReportEditorSnapshot = {
  document: ReportDocument
  minimumRowsByPage: Record<string, number>
  operation: string
}

type ReportEditorHistoryEntry = {
  operation: ReportEditorAuditChange
  summary: string
  forwardPatch: ReportJSONPatch
  inversePatch: ReportJSONPatch
  beforeMinimumRowsByPage: Record<string, number>
  afterMinimumRowsByPage: Record<string, number>
}

export type ReportEditorHistory = {
  /** present 是历史中唯一的完整报告 JSON；past/future 只保存正逆 Patch。 */
  present: ReportEditorSnapshot
  past: ReportEditorHistoryEntry[]
  future: ReportEditorHistoryEntry[]
  /** 保存失败时不能随 100 条撤销上限丢弃尚未落库的审计变化。 */
  pendingChanges: ReportEditorAuditChange[]
}

/** 创建内存历史；刷新后由服务端当前草稿重新建立，不恢复旧浏览器快照。 */
export function createReportEditorHistory(snapshot: ReportEditorSnapshot): ReportEditorHistory {
  return { present: cloneSnapshot(snapshot), past: [], future: [], pendingChanges: [] }
}

/**
 * 提交一次语义操作。minimumRowsByPage 只影响编辑画板，不进入报告 JSON Patch，
 * 从而避免把每个浏览器的临时空白区域写成业务事实。
 */
export function commitReportEditorHistory(
  history: ReportEditorHistory,
  snapshot: ReportEditorSnapshot,
  input: ReportEditorOperationInput,
): ReportEditorHistory {
  const patches = createReportJSONPatch(history.present.document, snapshot.document)
  const beforeRows = cloneRows(history.present.minimumRowsByPage)
  const afterRows = cloneRows(snapshot.minimumRowsByPage)
  if (patches.forward.length === 0 && rowsEqual(beforeRows, afterRows)) return history

  const operation = createAuditChange(input, patches.forward)
  const entry: ReportEditorHistoryEntry = {
    operation,
    summary: input.summary,
    forwardPatch: clonePatch(patches.forward),
    inversePatch: clonePatch(patches.inverse),
    beforeMinimumRowsByPage: beforeRows,
    afterMinimumRowsByPage: afterRows,
  }
  return {
    present: cloneSnapshot({ ...snapshot, operation: input.summary }),
    past: [...history.past, entry].slice(-MAX_EDITOR_HISTORY_ENTRIES),
    future: [],
    pendingChanges: patches.forward.length > 0 ? [...history.pendingChanges, cloneAuditChange(operation)] : history.pendingChanges,
  }
}

export function undoReportEditorHistory(history: ReportEditorHistory): ReportEditorHistory {
  const entry = history.past.at(-1)
  if (!entry) return history
  const summary = `撤销：${entry.summary}`
  const change = createAuditChange({
    operationType: 'UNDO', summary, source: 'USER',
    target: { ...entry.operation.target, referencedOperationId: entry.operation.clientOperationId },
  }, entry.inversePatch)
  return {
    present: {
      document: applyReportJSONPatch(history.present.document, entry.inversePatch),
      minimumRowsByPage: cloneRows(entry.beforeMinimumRowsByPage),
      operation: summary,
    },
    past: history.past.slice(0, -1),
    future: [entry, ...history.future],
    pendingChanges: entry.inversePatch.length > 0 ? [...history.pendingChanges, change] : history.pendingChanges,
  }
}

export function redoReportEditorHistory(history: ReportEditorHistory): ReportEditorHistory {
  const entry = history.future[0]
  if (!entry) return history
  const summary = `重做：${entry.summary}`
  const change = createAuditChange({
    operationType: 'REDO', summary, source: 'USER',
    target: { ...entry.operation.target, referencedOperationId: entry.operation.clientOperationId },
  }, entry.forwardPatch)
  return {
    present: {
      document: applyReportJSONPatch(history.present.document, entry.forwardPatch),
      minimumRowsByPage: cloneRows(entry.afterMinimumRowsByPage),
      operation: summary,
    },
    past: [...history.past, entry].slice(-MAX_EDITOR_HISTORY_ENTRIES),
    future: history.future.slice(1),
    pendingChanges: entry.forwardPatch.length > 0 ? [...history.pendingChanges, change] : history.pendingChanges,
  }
}

/** 保存成功后只确认本次请求携带的变化；保存期间产生的新变化继续留在队列。 */
export function acknowledgeReportEditorChanges(history: ReportEditorHistory, clientOperationIds: readonly string[]): ReportEditorHistory {
  if (clientOperationIds.length === 0) return history
  const acknowledged = new Set(clientOperationIds)
  const pendingChanges = history.pendingChanges.filter(change => !acknowledged.has(change.clientOperationId))
  return pendingChanges.length === history.pendingChanges.length ? history : { ...history, pendingChanges }
}

function createAuditChange(
  input: ReportEditorOperationInput,
  patch: ReportJSONPatch,
): ReportEditorAuditChange {
  return {
    clientOperationId: input.clientOperationId ?? globalThis.crypto.randomUUID(),
    operationType: input.operationType,
    source: input.source ?? 'USER',
    ...(input.target ? { target: { ...input.target } } : {}),
    patch: clonePatch(patch),
  }
}

function cloneSnapshot(snapshot: ReportEditorSnapshot): ReportEditorSnapshot {
  return {
    document: applyReportJSONPatch(snapshot.document, []),
    minimumRowsByPage: cloneRows(snapshot.minimumRowsByPage),
    operation: snapshot.operation,
  }
}

function cloneAuditChange(change: ReportEditorAuditChange): ReportEditorAuditChange {
  return {
    ...change,
    ...(change.target ? { target: { ...change.target } } : {}),
    patch: clonePatch(change.patch),
  }
}

function clonePatch(patch: ReportJSONPatch): ReportJSONPatch {
  return patch.map(operation => operation.op === 'remove'
    ? { ...operation }
    : { ...operation, value: structuredClone(operation.value) })
}

function cloneRows(rows: Record<string, number>): Record<string, number> {
  return { ...rows }
}

function rowsEqual(left: Record<string, number>, right: Record<string, number>): boolean {
  const leftKeys = Object.keys(left).sort()
  const rightKeys = Object.keys(right).sort()
  return leftKeys.length === rightKeys.length && leftKeys.every((key, index) => key === rightKeys[index] && left[key] === right[key])
}
